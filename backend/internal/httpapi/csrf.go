package httpapi

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// CSRF holds the explicitly configured browser origin. Its zero value fails closed.
type CSRF struct{ scheme, hostname, port string }

// NewCSRF validates a required HTTP(S) browser origin at startup.
func NewCSRF(origin string) (CSRF, error) {
	invalid := errors.New("invalid BROWSER_ORIGIN")
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" ||
		u.Path != "" && u.Path != "/" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") {
		return CSRF{}, invalid
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, " ,\\\t\r\n") {
		return CSRF{}, invalid
	}
	// Brackets are reserved for IP literals; unbracketed colons are invalid.
	if strings.HasPrefix(u.Host, "[") {
		if net.ParseIP(host) == nil {
			return CSRF{}, invalid
		}
	} else if !validOriginHostname(host) {
		return CSRF{}, invalid
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return CSRF{}, invalid
	}
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	} else {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return CSRF{}, invalid
		}
		port = strconv.Itoa(n)
	}
	return CSRF{u.Scheme, strings.ToLower(host), port}, nil
}

// Middleware is separate from authentication and must run after it.
func (c CSRF) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		origins := r.Header.Values("Origin")
		headers := r.Header.Values("X-CSRF-Protection")
		sites, present := r.Header[http.CanonicalHeaderKey("Sec-Fetch-Site")]
		valid := c.scheme != "" && len(origins) == 1 && len(headers) == 1 && headers[0] == "1"
		if valid {
			origin, err := NewCSRF(origins[0])
			// Origin header serialization has no trailing slash.
			valid = err == nil && !strings.HasSuffix(origins[0], "/") && origin == c
		}
		if present && (len(sites) != 1 || sites[0] != "same-origin") {
			valid = false
		}
		if !valid {
			w.Header().Set("Cache-Control", "no-store")
			catalogError(w, http.StatusForbidden, "CSRF protection required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validOriginHostname(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	host = strings.TrimSuffix(host, ".")
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
