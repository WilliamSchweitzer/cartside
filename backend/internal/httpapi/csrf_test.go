package httpapi

import "testing"

func TestBrowserOriginConfiguration(t *testing.T) {
	for _, value := range []string{"http://localhost:9080", "http://localhost:9080/", "https://example.com", "http://127.0.0.1", "https://[::1]:443"} {
		if _, err := NewCSRF(value); err != nil {
			t.Errorf("valid origin %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "null", "localhost:9080", "ftp://localhost", "http://", "http:///path", "http://user:password@localhost", "http://localhost/a", "http://localhost//", "http://localhost?", "http://localhost?q=x", "http://localhost#", "http://localhost#fragment", "http://localhost:", "http://localhost:0", "http://localhost:65536", "http://localhost:abc", "http://localhost:80:90", "http://[not-ip]", "http://local host", "http://localhost,evil", "http://localhost/%2f", "http://bad^host", "http://-host", "http://host..example"} {
		if _, err := NewCSRF(value); err == nil {
			t.Errorf("accepted invalid origin %q", value)
		}
	}
	for _, pair := range [][2]string{{"http://example.com", "http://EXAMPLE.com:80/"}, {"https://example.com:443", "https://example.com"}, {"http://localhost:09080", "http://localhost:9080"}} {
		a, e1 := NewCSRF(pair[0])
		b, e2 := NewCSRF(pair[1])
		if e1 != nil || e2 != nil || a != b {
			t.Errorf("effective origins differ: %v", pair)
		}
	}
}
