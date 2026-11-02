package override_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// stubRepository answers SetOverride and ClearOverride with fixed results;
// everything else is the memory repository.
type stubRepository struct {
	*override.MemoryRepository
	fv  *override.FieldValue
	err error
}

func (s stubRepository) SetOverride(context.Context, string, string, string, any, string, string, bool) (*override.FieldValue, error) {
	return s.fv, s.err
}

func (s stubRepository) ClearOverride(context.Context, string, string, string) (*override.FieldValue, error) {
	return s.fv, s.err
}

// serveOverride sends one request to the override routes of repo as a
// principal of org (no tenant when org is empty).
func serveOverride(repo override.Repository, org, method, path, body string) *httptest.ResponseRecorder {
	mux := chi.NewRouter()
	override.NewHandler(repo).RegisterRoutes(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: org}))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestClearOverrideMapsErrors covers WP-412 (retest of F-232, API-03):
// DELETE /cis/{id}/fields/{name}/override answers 404 only for an unknown
// CI or field (override.ErrNotFound); any other repository failure goes
// through api.WriteRepoError, without its text.
func TestClearOverrideMapsErrors(t *testing.T) {
	const path = "/api/v1/cis/ci-1/fields/model/override"
	refused := fmt.Errorf("clear override: %w", errors.New("dial tcp 10.0.0.5:5432: connect: connection refused"))
	for name, c := range map[string]struct {
		repo   override.Repository
		status int
		detail string
	}{
		"unknown field (memory repository)": {override.NewMemoryRepository(), http.StatusNotFound, "field value not found"},
		"not found from the repository": {stubRepository{err: fmt.Errorf("clear: %w", override.ErrNotFound)},
			http.StatusNotFound, "field value not found"},
		"repository failure": {stubRepository{err: refused}, http.StatusInternalServerError, "the request could not be completed"},
		"deadlock":           {stubRepository{err: &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}}, http.StatusConflict, "please retry"},
	} {
		t.Run(name, func(t *testing.T) {
			w := serveOverride(c.repo, "org-1", http.MethodDelete, path, "")
			if w.Code != c.status || !strings.Contains(w.Body.String(), c.detail) {
				t.Errorf("status %d %s, want %d with %q", w.Code, w.Body.String(), c.status, c.detail)
			}
			if body := w.Body.String(); strings.Contains(body, "10.0.0.5") || strings.Contains(body, "deadlock detected") {
				t.Errorf("response echoes the repository error: %s", body)
			}
		})
	}
	if w := serveOverride(override.NewMemoryRepository(), "", http.MethodDelete, path, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("without tenant: %d, want 401", w.Code)
	}
}

// TestOverrideChangesReturnTheCIVersion covers WP-412 (API-07): PUT and
// DELETE of an override answer with the CI's new version as ETag; a
// repository without CI versions (memory profile) sends none.
func TestOverrideChangesReturnTheCIVersion(t *testing.T) {
	const path = "/api/v1/cis/ci-1/fields/model/override"
	changed := stubRepository{fv: &override.FieldValue{CIID: "ci-1", FieldName: "model", CIVersion: 7}}
	for method, body := range map[string]string{http.MethodPut: `{"value":"m","reason":"r"}`, http.MethodDelete: ""} {
		w := serveOverride(changed, "org-1", method, path, body)
		if w.Code != http.StatusOK || w.Header().Get("ETag") != `"7"` {
			t.Errorf("%s: %d ETag %q, want 200 and \"7\"", method, w.Code, w.Header().Get("ETag"))
		}
		if strings.Contains(w.Body.String(), "version") {
			t.Errorf("%s: body carries the version: %s", method, w.Body.String())
		}
	}

	memory := override.NewMemoryRepository()
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		w := serveOverride(memory, "org-1", method, path, `{"value":"m","reason":"r"}`)
		if w.Code != http.StatusOK || w.Header().Get("ETag") != "" {
			t.Errorf("memory repository, %s: %d ETag %q, want 200 without ETag", method, w.Code, w.Header().Get("ETag"))
		}
	}
}

// TestOverrideRefusesReservedNames covers WP-478 (GLO-13, E-23; F-092):
// PUT and DELETE of an override with a reserved namespace as name answer
// 400 with the field "name" and do not reach the repository; the
// repositories refuse the names as well (ErrReservedName).
func TestOverrideRefusesReservedNames(t *testing.T) {
	// The stub would answer 200: a 400 shows the handler refused first.
	changed := stubRepository{fv: &override.FieldValue{CIID: "ci-1", CIVersion: 7}}
	memory := override.NewMemoryRepository()
	for _, name := range []string{"_overrides", "_observed", "_provenance", "_instance"} {
		path := "/api/v1/cis/ci-1/fields/" + name + "/override"
		for method, body := range map[string]string{http.MethodPut: `{"value":"x","reason":"r"}`, http.MethodDelete: ""} {
			for repoName, repo := range map[string]override.Repository{"stub": changed, "memory": memory} {
				w := serveOverride(repo, "org-1", method, path, body)
				if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"field":"name"`) {
					t.Errorf("%s %s (%s): %d %s, want 400 with the field name", method, name, repoName, w.Code, w.Body.String())
				}
			}
		}
		if _, err := memory.SetOverride(context.Background(), "org-1", "ci-1", name, "x", "", "r", true); !errors.Is(err, override.ErrReservedName) {
			t.Errorf("memory SetOverride %s: %v, want ErrReservedName", name, err)
		}
		if _, err := memory.ClearOverride(context.Background(), "org-1", "ci-1", name); !errors.Is(err, override.ErrReservedName) {
			t.Errorf("memory ClearOverride %s: %v, want ErrReservedName", name, err)
		}
	}
	// A normal attribute and a name that only starts with "_" stay possible.
	for _, name := range []string{"owner", "_instance_note"} {
		w := serveOverride(memory, "org-1", http.MethodPut, "/api/v1/cis/ci-1/fields/"+name+"/override", `{"value":"x","reason":"r"}`)
		if w.Code != http.StatusOK {
			t.Errorf("PUT %s: %d %s, want 200", name, w.Code, w.Body.String())
		}
	}
	// A repository refusal reaches the client as the same 400.
	refused := stubRepository{err: fmt.Errorf("set: %w", override.ErrReservedName)}
	for method, body := range map[string]string{http.MethodPut: `{"value":"x","reason":"r"}`, http.MethodDelete: ""} {
		w := serveOverride(refused, "org-1", method, "/api/v1/cis/ci-1/fields/owner/override", body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"field":"name"`) {
			t.Errorf("%s with ErrReservedName from the repository: %d %s, want 400", method, w.Code, w.Body.String())
		}
	}
}

// TestSourcePolicyDefaultFollowsREC03 covers WP-478 (REC-03, F-230): GET
// /reconciliation/source-policy without a policy of the organization
// returns the REC-03 order (override.DefaultPriorities).
func TestSourcePolicyDefaultFollowsREC03(t *testing.T) {
	w := serveOverride(override.NewMemoryRepository(), "org-1", http.MethodGet, "/api/v1/reconciliation/source-policy", "")
	want := `"priorities":["manual","manual_override","override","import","workflow","ipmi","redfish","agent","snmp","api","integration","wmi","nas","ssh","sweep"]`
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
		t.Errorf("GET source-policy: %d %s, want %s", w.Code, w.Body.String(), want)
	}
}
