// Phase-1 acceptance audit, group T2-auth-authz: regression tests of the
// SSRF protection of the IGA SCIM connectors (shared egress client).
//
//   - WP-049 (SEC-08), gap (high) "IGA SCIM egress regression test (probe
//     only today)": creating a connector with a metadata, RFC 1918,
//     loopback, ULA, localhost or plain-http base URL -> 400; a public https
//     URL -> 201; AllowPrivate (on-premises profile) admits RFC 1918 but
//     never the metadata service; at runtime a connector of the registry
//     refuses a loopback TLS server (ErrBlockedDestination) without reaching
//     it. The update path is a defect: see t2_egress_auditdefect_test.go.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestT2IGAEgress' ./internal/iga/
package iga

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

const t2IGAOrg = "00000000-0000-0000-0000-0000000000aa"

// t2IGARequest serves a connector request for the test organization.
func t2IGARequest(mux http.Handler, method, path, body string) int {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: t2IGAOrg, UserID: "u"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Code
}

func t2IGAMux(opts egress.Options) http.Handler {
	mux := chi.NewRouter()
	NewHandler(NewMemoryRepository(), nil, nil, nil, nil).WithEgress(opts).RegisterRoutes(mux)
	return mux
}

func TestT2IGAEgressCreateValidation(t *testing.T) {
	for _, c := range []struct {
		name, url    string
		allowPrivate bool
		want         int
	}{
		{"metadata service", "https://169.254.169.254/scim/v2", false, http.StatusBadRequest},
		{"RFC 1918", "https://10.0.0.5/scim/v2", false, http.StatusBadRequest},
		{"loopback", "https://127.0.0.1/scim/v2", false, http.StatusBadRequest},
		{"IPv6 ULA", "https://[fd00::1]/scim/v2", false, http.StatusBadRequest},
		{"localhost", "https://localhost/scim/v2", false, http.StatusBadRequest},
		{"plain http", "http://scim.example.com/scim/v2", false, http.StatusBadRequest},
		{"public https", "https://scim.example.com/scim/v2", false, http.StatusCreated},
		{"AllowPrivate: RFC 1918", "https://10.0.0.5/scim/v2", true, http.StatusCreated},
		{"AllowPrivate: metadata service", "https://169.254.169.254/scim/v2", true, http.StatusBadRequest},
		{"AllowPrivate: plain http", "http://10.0.0.5/scim/v2", true, http.StatusBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := t2IGARequest(t2IGAMux(egress.Options{AllowPrivate: c.allowPrivate}), http.MethodPost, "/api/v1/iga/connectors",
				`{"name":"t2","type":"scim","base_url":"`+c.url+`"}`)
			if got != c.want {
				t.Errorf("create SCIM connector with %s (AllowPrivate %v): %d, want %d", c.url, c.allowPrivate, got, c.want)
			}
		})
	}
}

func TestT2IGAEgressRuntimeBlocksLoopback(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"Resources":[],"totalResults":0}`))
	}))
	defer srv.Close()
	reg := NewRegistry(nil, nil)
	c, err := NewSCIMConnector(srv.URL, "t2-token", reg.client)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.ReadAccounts(context.Background(), 1, 10); !errors.Is(err, egress.ErrBlockedDestination) {
		t.Errorf("ReadAccounts against a loopback server: %v, want ErrBlockedDestination", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("loopback server received %d request(s), want 0", n)
	}
}
