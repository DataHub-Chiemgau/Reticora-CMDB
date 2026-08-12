package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersSetsBaseline(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	w := httptest.NewRecorder()

	SecurityHeaders(next).ServeHTTP(w, req)

	expected := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "strict-origin-when-cross-origin",
		"Content-Security-Policy":      "default-src 'self'; frame-ancestors 'none'; base-uri 'self'",
		"Cross-Origin-Resource-Policy": "same-origin",
	}
	for header, want := range expected {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	// Plain HTTP must not advertise HSTS.
	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS over plain HTTP = %q, want empty", got)
	}
}

func TestSecurityHeadersSendsHSTSOverTLS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "https://example.com/api/v1/cis", nil)
	req.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()

	SecurityHeaders(next).ServeHTTP(w, req)

	if got := w.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("HSTS header missing on TLS request")
	}
}

func TestSecurityHeadersTrustsForwardedProto(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()

	SecurityHeaders(next).ServeHTTP(w, req)

	if got := w.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("HSTS header missing behind TLS-terminating proxy")
	}
}
