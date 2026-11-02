// Phase-1 acceptance audit, group T2-auth-authz: regression tests of the
// SSRF protection of the IGA SCIM connectors (shared egress client).
//
//   - WP-049 (SEC-08), gap (high) "IGA SCIM egress regression test (probe
//     only today)": creating a connector with a metadata, RFC 1918,
//     loopback, ULA, localhost or plain-http base URL -> 400; a public https
//     URL -> 201; AllowPrivate (on-premises profile) admits RFC 1918 but
//     never the metadata service; at runtime a connector of the registry
//     refuses a loopback TLS server (ErrBlockedDestination) without reaching
//     it.
//   - WP-563 (SEC-08, F-203): updating a connector holds a changed base URL
//     to the same rules, 400 with the field base_url and nothing stored
//     (TestAuditT2IGAUpdateValidatesBaseURL, moved from
//     t2_egress_auditdefect_test.go).
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestT2IGAEgress|TestAuditT2IGAUpdateValidatesBaseURL' ./internal/iga/
package iga

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestAuditT2IGAUpdateValidatesBaseURL covers WP-563 (F-203): PATCH of a
// connector refuses a changed base URL that create refuses (metadata,
// RFC 1918, loopback, ULA, localhost, plain http) with 400 on the field
// base_url and keeps the stored one; AllowPrivate admits RFC 1918 but not
// the metadata service; a public https URL and an unchanged stored URL
// are accepted.
func TestAuditT2IGAUpdateValidatesBaseURL(t *testing.T) {
	const stored = "https://scim.example.com/scim/v2"
	serve := func(opts egress.Options) (*MemoryRepository, http.Handler, string) {
		repo := NewMemoryRepository()
		mux := chi.NewRouter()
		NewHandler(repo, nil, nil, nil, nil).WithEgress(opts).RegisterRoutes(mux)
		c := &ConnectorConfig{OrganizationID: t2IGAOrg, Name: "t2", Type: ConnectorTypeSCIM, BaseURL: stored}
		if err := repo.CreateConnector(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		return repo, mux, c.ID
	}
	patch := func(mux http.Handler, id, baseURL string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/iga/connectors/"+id, strings.NewReader(`{"base_url":"`+baseURL+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: t2IGAOrg, UserID: "u"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	storedURL := func(repo *MemoryRepository, id string) string {
		c, err := repo.GetConnector(context.Background(), t2IGAOrg, id)
		if err != nil {
			t.Fatal(err)
		}
		return c.BaseURL
	}

	repo, mux, id := serve(egress.Options{})
	for _, url := range []string{
		"https://169.254.169.254/scim/v2", "https://10.0.0.5/scim/v2", "https://127.0.0.1/scim/v2",
		"https://[fd00::1]/scim/v2", "https://localhost/scim/v2", "http://scim.example.com/scim/v2",
		"https://[2002:a9fe:a9fe::1]/scim/v2",
	} {
		w := patch(mux, id, url)
		if w.Code != http.StatusBadRequest {
			t.Errorf("update SCIM connector base_url to %s: %d, want 400 (as on create)", url, w.Code)
			continue
		}
		var problem struct {
			Field string `json:"field"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.Field != "base_url" {
			t.Errorf("update to %s: problem %s, want field base_url", url, w.Body.String())
		}
		if got := storedURL(repo, id); got != stored {
			t.Errorf("refused update to %s stored base_url %s", url, got)
		}
	}
	if w := patch(mux, "00000000-0000-4000-8000-999999999999", "https://10.0.0.5/scim/v2"); w.Code != http.StatusNotFound {
		t.Errorf("update of an unknown connector: %d, want 404", w.Code)
	}
	if w := patch(mux, id, "https://scim2.example.com/scim/v2"); w.Code != http.StatusOK || storedURL(repo, id) != "https://scim2.example.com/scim/v2" {
		t.Errorf("update to a public https URL: %d %s", w.Code, w.Body.String())
	}

	// A URL stored before the check is no change: other fields stay editable.
	legacy := &ConnectorConfig{OrganizationID: t2IGAOrg, Name: "legacy", Type: ConnectorTypeSCIM, BaseURL: "http://10.0.0.5/scim/v2"}
	if err := repo.CreateConnector(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	if w := patch(mux, legacy.ID, legacy.BaseURL); w.Code != http.StatusOK {
		t.Errorf("update with the unchanged stored base_url: %d %s, want 200", w.Code, w.Body.String())
	}

	repo, mux, id = serve(egress.Options{AllowPrivate: true})
	for url, want := range map[string]int{
		"https://10.0.0.5/scim/v2":        http.StatusOK,
		"https://169.254.169.254/scim/v2": http.StatusBadRequest,
		"http://10.0.0.6/scim/v2":         http.StatusBadRequest,
	} {
		if w := patch(mux, id, url); w.Code != want {
			t.Errorf("AllowPrivate: update to %s: %d %s, want %d", url, w.Code, w.Body.String(), want)
		}
	}
	if got := storedURL(repo, id); got != "https://10.0.0.5/scim/v2" {
		t.Errorf("AllowPrivate: stored base_url %s", got)
	}
}
