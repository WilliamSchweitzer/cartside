// Package auth verifies Keycloak access tokens independently of gateway sessions.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const TokenHeader = "X-Access-Token"

type Principal struct {
	Subject string `json:"subject"`
	Issuer  string `json:"issuer"`
}
type principalKey struct{}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

type Verifier struct {
	verifier *oidc.IDTokenVerifier
	issuer   string
}

// New validates configuration without contacting Keycloak. RemoteKeySet caches keys
// and refreshes them on an unknown signing key, supporting provider key rotation.
func New(issuer, audience, jwksURL string) (*Verifier, error) {
	for _, raw := range []string{issuer, jwksURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(raw) != raw {
			return nil, errors.New("invalid authentication URL configuration")
		}
	}
	if strings.TrimSpace(audience) == "" || audience != strings.TrimSpace(audience) {
		return nil, errors.New("invalid authentication audience configuration")
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx := oidc.ClientContext(context.Background(), client)
	keys := oidc.NewRemoteKeySet(ctx, jwksURL)
	return &Verifier{oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: audience, SupportedSigningAlgs: []string{oidc.RS256}}), issuer}, nil
}

func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		values := r.Header.Values(TokenHeader)
		if len(values) != 1 || values[0] == "" || len(values[0]) > 16384 || strings.ContainsAny(values[0], " \t\r\n,") {
			unauthorized(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		token, err := v.verifier.Verify(ctx, values[0])
		if err != nil {
			unauthorized(w)
			return
		}
		var claims struct {
			NotBefore *json.Number `json:"nbf"`
			IssuedAt  *json.Number `json:"iat"`
		}
		if token.Claims(&claims) != nil || token.Issuer != v.issuer || strings.TrimSpace(token.Subject) == "" || !token.Expiry.After(time.Now()) {
			unauthorized(w)
			return
		}
		// Enforce applicable time claims without the library's nbf clock-skew allowance.
		for _, claim := range []*json.Number{claims.NotBefore, claims.IssuedAt} {
			if claim != nil {
				n, err := claim.Float64()
				if err != nil || n > float64(time.Now().UnixNano())/1e9 {
					unauthorized(w)
					return
				}
			}
		}
		principal := Principal{Subject: token.Subject, Issuer: token.Issuer}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}
