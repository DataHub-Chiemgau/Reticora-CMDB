package identity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCheckIssuer covers WP-095 (INS-02): the discovery document must be
// served and announce exactly the configured issuer (a trailing slash
// aside); another issuer, an error status or an unreachable host fail.
func TestCheckIssuer(t *testing.T) {
	announce := ""
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realms/reticora/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"issuer": %q}`, announce)
	}))
	defer srv.Close()
	issuer := srv.URL + "/realms/reticora"
	ctx := context.Background()

	announce = issuer
	if got, err := CheckIssuer(ctx, srv.Client(), issuer+"/"); err != nil || got != issuer {
		t.Errorf("matching issuer: %q, %v", got, err)
	}
	announce = "http://localhost:8180/realms/reticora"
	if _, err := CheckIssuer(ctx, srv.Client(), issuer); err == nil || !strings.Contains(err.Error(), "announces the issuer") {
		t.Errorf("other issuer: %v", err)
	}
	announce, status = issuer, http.StatusServiceUnavailable
	if _, err := CheckIssuer(ctx, srv.Client(), issuer); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("error status: %v", err)
	}
	if _, err := CheckIssuer(ctx, srv.Client(), srv.URL+"/realms/other"); err == nil {
		t.Error("missing realm accepted")
	}
	if _, err := CheckIssuer(ctx, nil, "http://127.0.0.1:1/realms/reticora"); err == nil {
		t.Error("unreachable issuer accepted")
	}
}
