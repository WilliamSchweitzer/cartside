package httpapi

import (
	"context"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/WilliamSchweitzer/cartside/backend/internal/aiprotocol"
	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/shoppingassistant"
)

type Assistant interface {
	Suggest(context.Context, string) (shoppingassistant.Result, error)
}

type assistantUsage struct {
	start time.Time
	count int
	busy  bool
}

// AssistantLimiter is a bounded fixed-window limiter. Only expired, idle entries
// can be removed; capacity pressure never resets an active user's allowance.
type AssistantLimiter struct {
	mu              sync.Mutex
	entries         map[auth.Principal]*assistantUsage
	limit, capacity int
	window          time.Duration
	now             func() time.Time
}

func NewAssistantLimiter(limit, capacity int, window time.Duration, now func() time.Time) (*AssistantLimiter, error) {
	if limit <= 0 || capacity <= 0 || window <= 0 || now == nil {
		return nil, errors.New("invalid assistant limits")
	}
	return &AssistantLimiter{entries: make(map[auth.Principal]*assistantUsage), limit: limit, capacity: capacity, window: window, now: now}, nil
}
func (l *AssistantLimiter) admit(p auth.Principal) (func(), string, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, entry := range l.entries {
		if !entry.busy && !now.Before(entry.start.Add(l.window)) {
			delete(l.entries, key)
		}
	}
	entry := l.entries[p]
	if entry == nil {
		if len(l.entries) >= l.capacity {
			return nil, "assistant_capacity", 1
		}
		entry = &assistantUsage{start: now}
		l.entries[p] = entry
	}
	if entry.busy {
		return nil, "assistant_busy", 1
	}
	if entry.count >= l.limit {
		return nil, "assistant_rate_limited", max(1, int(math.Ceil(entry.start.Add(l.window).Sub(now).Seconds())))
	}
	entry.count++
	entry.busy = true
	return func() { l.mu.Lock(); entry.busy = false; l.mu.Unlock() }, "", 0
}

func assistantHandler(service Assistant, limiter *AssistantLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		p, ok := auth.FromContext(r.Context())
		if !ok || p.Issuer == "" || p.Subject == "" {
			assistantError(w, 401, "unauthorized", "Authentication required.")
			return
		}
		if service == nil || limiter == nil {
			assistantError(w, 503, "assistant_unavailable", "Shopping assistant is disabled.")
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || len(r.Header.Values("Content-Type")) != 1 {
			assistantError(w, 400, "invalid_input", "Expected a JSON message.")
			return
		}
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2048))
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				assistantError(w, 413, "payload_too_large", "Message body exceeds 2048 bytes.")
			} else {
				assistantError(w, 400, "invalid_input", "Could not read JSON message.")
			}
			return
		}
		var input struct {
			Message string `json:"message"`
		}
		if !validJSONUnicode(b) || aiprotocol.Strict(b, &input) != nil || !utf8.ValidString(input.Message) || len(input.Message) > 256 || strings.TrimSpace(input.Message) == "" {
			assistantError(w, 400, "invalid_input", "Message must contain 1–256 UTF-8 bytes and be nonblank.")
			return
		}
		if r.Context().Err() != nil {
			return
		}
		release, code, retry := limiter.admit(p)
		if release == nil {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			assistantError(w, 429, code, "Assistant usage limit reached; try later.")
			return
		}
		defer release()
		result, err := service.Suggest(r.Context(), input.Message)
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			assistantServiceError(w, err)
			return
		}
		if result.Suggestions == nil {
			result.Suggestions = []shoppingassistant.Suggestion{}
		}
		catalogJSON(w, 200, result)
	}
}
func assistantError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	catalogJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
}
func assistantServiceError(w http.ResponseWriter, err error) {
	status, code, message := 502, "provider_unavailable", "Assistant provider unavailable."
	var failure *shoppingassistant.Error
	if errors.As(err, &failure) {
		switch failure.Code {
		case shoppingassistant.InvalidInput:
			status, code, message = 400, "invalid_input", "Invalid assistant message."
		case shoppingassistant.Timeout:
			status, code, message = 504, "timeout", "Assistant request timed out."
		case shoppingassistant.RateLimited:
			status, code, message = 429, "provider_rate_limited", "Assistant provider usage limit reached."
			w.Header().Set("Retry-After", "1")
		case shoppingassistant.InvalidOutput, shoppingassistant.TooLarge:
			code, message = "invalid_provider_output", "Assistant provider returned invalid output."
		case shoppingassistant.CatalogFailure:
			status, code, message = 503, "catalog_unavailable", "Catalog unavailable."
		case shoppingassistant.CatalogChanged:
			status, code, message = 409, "catalog_changed", "Suggested products changed; try again."
		}
	}
	assistantError(w, status, code, message)
}

// assistantBoundary preserves shared authentication/CSRF policies while exposing
// machine-readable errors for the assistant contract.
type assistantBoundary struct {
	http.ResponseWriter
	rejected bool
}

func (w *assistantBoundary) WriteHeader(status int) {
	if status == 401 || status == 403 {
		w.rejected = true
		code, message := "unauthorized", "Authentication required."
		if status == 403 {
			code, message = "csrf_required", "CSRF protection required."
		}
		assistantError(w.ResponseWriter, status, code, message)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *assistantBoundary) Write(b []byte) (int, error) {
	if w.rejected {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Reject those
// escapes so byte counting always applies to the customer's actual message.
func validJSONUnicode(b []byte) bool {
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		n, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
