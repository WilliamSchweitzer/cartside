package shoppingassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WilliamSchweitzer/cartside/backend/internal/aiprotocol"
	"github.com/WilliamSchweitzer/cartside/backend/internal/observability"
)

type ErrorCode string

const (
	InvalidInput   ErrorCode = "invalid_input"
	TooLarge       ErrorCode = "payload_too_large"
	Timeout        ErrorCode = "timeout"
	RateLimited    ErrorCode = "rate_limited"
	Unavailable    ErrorCode = "provider_unavailable"
	InvalidOutput  ErrorCode = "invalid_provider_output"
	CatalogFailure ErrorCode = "catalog_unavailable"
	CatalogChanged ErrorCode = "catalog_changed"
)

type Error struct{ Code ErrorCode }

func (e *Error) Error() string  { return "shopping assistant: " + string(e.Code) }
func failure(c ErrorCode) error { return &Error{Code: c} }

// Client owns a private HTTP transport, no cookie jar, redirects or automatic retries.
type Client struct {
	endpoint, credential string
	http                 *http.Client
	metrics              *observability.Metrics
}

func NewClient(endpoint, credential string, metrics ...*observability.Metrics) (*Client, error) {
	u, e := url.Parse(endpoint)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/internal/ai/chat/completions" || len(credential) < 32 || strings.HasPrefix(credential, "replace_with_") || strings.ContainsAny(credential, "\r\n") {
		return nil, failure(InvalidInput)
	}
	var metric *observability.Metrics
	if len(metrics) > 0 {
		metric = metrics[0]
	}
	return &Client{metrics: metric, endpoint: endpoint, credential: credential, http: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 2 * time.Second, MaxResponseHeaderBytes: 4096}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Complete(ctx context.Context, input aiprotocol.Input) (result aiprotocol.Output, resultErr error) {
	start := time.Now()
	status := 0
	transportCancelled := false
	defer func() {
		outcome := "success"
		var failure *Error
		if errors.As(resultErr, &failure) {
			outcome = string(failure.Code)
		} else if resultErr != nil {
			outcome = "provider_unavailable"
		}
		// A rejected internal request is not a provider/transport outage.
		// Keep the existing public error contract while separating telemetry.
		if status >= 400 && status < 500 && status != 408 && status != 429 {
			outcome = "request_rejected"
		}
		if transportCancelled {
			outcome = "cancelled"
		}
		c.metrics.AssistantCompleted(outcome, time.Since(start))
		slog.InfoContext(ctx, "assistant_call_completed", "request_id", observability.ID(ctx), "operation", "shopping_completion", "provider", "mock", "duration_ms", float64(time.Since(start).Microseconds())/1000, "status", status, "outcome", outcome)
	}()
	var out aiprotocol.Output
	content, e := json.Marshal(input)
	if e != nil {
		return out, failure(InvalidInput)
	}
	body, _ := json.Marshal(struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
	}{Model: aiprotocol.Model, Messages: []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{{"user", string(content)}}})
	if len(body) > aiprotocol.MaxRequestBytes {
		return out, failure(TooLarge)
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(body))
	if e != nil {
		return out, failure(InvalidInput)
	}
	req.Header.Set(observability.Header, observability.ID(ctx))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cartside-AI-Key", c.credential)
	res, e := c.http.Do(req)
	if e != nil {
		transportCancelled = errors.Is(e, context.Canceled)
		if errors.Is(e, context.DeadlineExceeded) || ctx.Err() != nil {
			return out, failure(Timeout)
		}
		return out, failure(Unavailable)
	}
	status = res.StatusCode
	defer res.Body.Close()
	if res.StatusCode == 429 {
		return out, failure(RateLimited)
	}
	if res.StatusCode == 504 || res.StatusCode == 408 {
		return out, failure(Timeout)
	}
	if res.StatusCode != 200 {
		return out, failure(Unavailable)
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, aiprotocol.MaxResponseBytes+1))
	if e != nil {
		transportCancelled = errors.Is(e, context.Canceled)
		if errors.Is(e, context.DeadlineExceeded) || ctx.Err() != nil {
			return out, failure(Timeout)
		}
		return out, failure(Unavailable)
	}
	if len(b) > aiprotocol.MaxResponseBytes {
		return out, failure(TooLarge)
	}
	var envelope struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created *int64 `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index   *int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	if aiprotocol.Strict(b, &envelope) != nil || len(envelope.ID) == 0 || len(envelope.ID) > 100 || envelope.Object != "chat.completion" || envelope.Created == nil || *envelope.Created < 0 || envelope.Model != aiprotocol.Model || len(envelope.Choices) != 1 {
		return out, failure(InvalidOutput)
	}
	ch := envelope.Choices[0]
	if ch.Index == nil || *ch.Index != 0 || ch.Message.Role != "assistant" || ch.Finish != "stop" || aiprotocol.Strict([]byte(ch.Message.Content), &out) != nil {
		return out, failure(InvalidOutput)
	}
	if out.Version != 1 || !bounded(out.Text, 400) || out.Suggestions == nil || len(out.Suggestions) > 5 {
		return aiprotocol.Output{}, failure(InvalidOutput)
	}
	seen := map[string]bool{}
	allowed := map[string]bool{}
	for _, p := range input.Candidates {
		allowed[p.ProductID] = true
	}
	for _, s := range out.Suggestions {
		if !productIDPattern.MatchString(s.ProductID) || !allowed[s.ProductID] || seen[s.ProductID] || s.Quantity < 1 || s.Quantity > 99 || !bounded(s.Reason, 120) {
			return aiprotocol.Output{}, failure(InvalidOutput)
		}
		seen[s.ProductID] = true
	}
	return out, nil
}
func bounded(s string, n int) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && len(s) <= n
}
