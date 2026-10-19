//go:build auditdefect

package ci_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed).
//
// WP-057 (CI-04, OVR-01, CH9) P1 "Manual bulk status change silently dropped
// when the CI has a status override, reported as success": ci/bulk.go
// runBulk calls Service.Update without Manual, so pg_repository.go
// dropOverriddenFields removes the status and Update returns the unchanged
// CI without error; POST /api/v1/cis/bulk answers {"result":"ok"}.
// WP-057 P2 "Manual bulk status change creates no override" (no author, no
// override_at) and WP-061 (API-07) P2 "User bulk set_status does not raise
// ci.version; a stale PATCH silently reverts the bulk change".
//
// WP-061 (API-07, OVR-01) P1 "Override set/clear does not raise ci.version,
// so a stale If-Match PATCH silently overwrites the newer manual override":
// override/pg_repository.go SetOverride/ClearOverride -> syncCIField update
// the ci row without version = version + 1 and without a ci_change row, so
// checkPrecondition never sees the override.
//
// WP-057 (CI-04: JSON Merge Patch RFC 7396) P2 "PATCH null on top-level CI
// fields is ignored": UpdateRequest decodes null into a nil *string, which
// means "unchanged"; the client gets 200 and the value stays.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3BulkStatusIsManualWrite|TestAuditT3OverrideWritesRaiseVersion|TestAuditT3MergePatchNullClearsTypedField' ./internal/ci/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// auditT3CIRouter wires the production CI handler and override handler on
// the PostgreSQL repositories, as the server does for /api/v1/cis/**.
type auditT3CIRouter struct {
	t    *testing.T
	f    *scopetest.Fixture
	mux  *chi.Mux
	ctx  context.Context
	user string
}

func newAuditT3CIRouter(t *testing.T, tag string) *auditT3CIRouter {
	t.Helper()
	f := scopetest.Seed(t, tag)
	user := f.AppUser(t, f.OrgA, "audit-t3-editor")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App))).RegisterRoutes(mux)
	override.NewHandler(override.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &auditT3CIRouter{t: t, f: f, mux: mux, ctx: ctx, user: user}
}

func (r *auditT3CIRouter) call(method, path, ifMatch, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req.WithContext(r.ctx))
	return w
}

func (r *auditT3CIRouter) str(sql string, args ...any) string {
	r.t.Helper()
	var s string
	if err := r.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		r.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (r *auditT3CIRouter) version(ciID string) int64 {
	r.t.Helper()
	v, err := strconv.ParseInt(r.str(`SELECT version::text FROM ci WHERE id = $1`, ciID), 10, 64)
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}

func auditT3Tag(v int64) string { return `"` + strconv.FormatInt(v, 10) + `"` }

func TestAuditT3BulkStatusIsManualWrite(t *testing.T) {
	r := newAuditT3CIRouter(t, "6a")
	bulk := func(id, status string) (*httptest.ResponseRecorder, ci.BulkResult) {
		t.Helper()
		w := r.call(http.MethodPost, "/api/v1/cis/bulk", "", `{"action":"set_status","ids":["`+id+`"],"status":"`+status+`"}`)
		var res ci.BulkResult
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatalf("bulk body: %v (%s)", err, w.Body.String())
			}
		}
		return w, res
	}

	t.Run("applied over an existing manual status override", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-bulk-ovr")
		if w := r.call(http.MethodPatch, "/api/v1/cis/"+id, "", `{"status":"inactive"}`); w.Code != http.StatusOK {
			t.Fatalf("precondition PATCH status: %d %s", w.Code, w.Body.String())
		}
		if n := r.str(`SELECT count(*)::text FROM ci_field_value WHERE ci_id = $1 AND field_name = 'status' AND override_at IS NOT NULL`, id); n != "1" {
			t.Fatalf("precondition: status override rows = %s, want 1", n)
		}
		w, res := bulk(id, "active")
		if w.Code != http.StatusOK || res.Succeeded != 1 {
			t.Fatalf("bulk set_status: %d %s", w.Code, w.Body.String())
		}
		if got := r.str(`SELECT status FROM ci WHERE id = $1`, id); got != "active" {
			t.Errorf("bulk set_status reported %q for the CI but ci.status = %q: the manual bulk change was silently dropped (want 'active', or a non-ok item result)",
				res.Items[0].Result, got)
		}
	})

	t.Run("records a manual override with the initiating user", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-bulk-new")
		if w, res := bulk(id, "maintenance"); w.Code != http.StatusOK || res.Succeeded != 1 {
			t.Fatalf("bulk set_status: %d %s", w.Code, w.Body.String())
		}
		if got := r.str(`SELECT status FROM ci WHERE id = $1`, id); got != "maintenance" {
			t.Fatalf("precondition: ci.status = %q, want maintenance", got)
		}
		got := r.str(`SELECT count(*)::text FROM ci_field_value WHERE ci_id = $1 AND field_name = 'status'
			AND override_at IS NOT NULL AND override_author = $2`, id, r.user)
		if got != "1" {
			t.Errorf("manual bulk set_status left %s override rows authored by the user, want 1 (OVR-01: every manual field change is an override with author and time)", got)
		}
	})

	t.Run("raises the version so a stale PATCH of status conflicts", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-bulk-ver")
		v0 := r.version(id)
		if w, res := bulk(id, "maintenance"); w.Code != http.StatusOK || res.Succeeded != 1 {
			t.Fatalf("bulk set_status: %d %s", w.Code, w.Body.String())
		}
		if v1 := r.version(id); v1 != v0+1 {
			t.Errorf("version after manual bulk set_status = %d, want %d (API-07: manual write raises the version)", v1, v0+1)
		}
		w := r.call(http.MethodPatch, "/api/v1/cis/"+id, auditT3Tag(v0), `{"status":"active"}`)
		if w.Code != http.StatusConflict {
			t.Errorf("PATCH status with the pre-bulk If-Match %s = %d, want 409 (the stale edit silently reverted the bulk status)", auditT3Tag(v0), w.Code)
		}
		if got := r.str(`SELECT status FROM ci WHERE id = $1`, id); got != "maintenance" {
			t.Errorf("ci.status after the stale PATCH = %q, want the bulk value maintenance", got)
		}
	})
}

func TestAuditT3OverrideWritesRaiseVersion(t *testing.T) {
	r := newAuditT3CIRouter(t, "6b")

	t.Run("PUT override raises the version and blocks a stale PATCH of the same field", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-ovr-put")
		base := "/api/v1/cis/" + id
		g := r.call(http.MethodGet, base, "", "")
		etag0 := g.Header().Get("ETag")
		if g.Code != http.StatusOK || etag0 == "" {
			t.Fatalf("GET: %d etag=%q", g.Code, etag0)
		}
		if w := r.call(http.MethodPut, base+"/fields/model/override", "", `{"value":"manual-model","reason":"audit","protected":true}`); w.Code != http.StatusOK {
			t.Fatalf("PUT override: %d %s", w.Code, w.Body.String())
		}
		if etag1 := r.call(http.MethodGet, base, "", "").Header().Get("ETag"); etag1 == etag0 {
			t.Errorf("ETag after PUT override = %s, unchanged (API-07: an override is a manual write and raises the version)", etag1)
		}
		w := r.call(http.MethodPatch, base, etag0, `{"model":"stale-model"}`)
		if w.Code != http.StatusConflict {
			t.Errorf("PATCH model with the If-Match read before the override (%s) = %d, want 409 fields=[model]", etag0, w.Code)
		} else if !strings.Contains(w.Body.String(), "model") {
			t.Errorf("409 body does not name the field model: %s", w.Body.String())
		}
		if got := r.str(`SELECT coalesce(model, '') FROM ci WHERE id = $1`, id); got != "manual-model" {
			t.Errorf("ci.model after the stale PATCH = %q, want the protected override manual-model (silent lost update)", got)
		}
		// A stale PATCH of a different field is not a conflict (field-granular API-07).
		if w := r.call(http.MethodPatch, base, etag0, `{"hostname":"audit-t3-other-field"}`); w.Code != http.StatusOK {
			t.Errorf("PATCH of another field with the same If-Match = %d, want 200", w.Code)
		}
	})

	t.Run("DELETE override restoring the observed value raises the version", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-ovr-del")
		base := "/api/v1/cis/" + id
		if w := r.call(http.MethodPut, base+"/fields/model/override", "", `{"value":"manual-model","reason":"audit"}`); w.Code != http.StatusOK {
			t.Fatalf("PUT override: %d %s", w.Code, w.Body.String())
		}
		if _, err := r.f.Admin.Exec(context.Background(), `UPDATE ci_field_value SET discovered_value = '"observed-model"',
			discovered_source = 'snmp', discovered_at = now(), discovered_rank = 80 WHERE ci_id = $1 AND field_name = 'model'`, id); err != nil {
			t.Fatal(err)
		}
		v0 := r.version(id)
		if w := r.call(http.MethodDelete, base+"/fields/model/override", "", ""); w.Code != http.StatusOK {
			t.Fatalf("DELETE override: %d %s", w.Code, w.Body.String())
		}
		if got := r.str(`SELECT coalesce(model, '') FROM ci WHERE id = $1`, id); got != "observed-model" {
			t.Fatalf("precondition: ci.model after clear = %q, want observed-model", got)
		}
		if v1 := r.version(id); v1 <= v0 {
			t.Errorf("version after clearing the override (effective value manual-model -> observed-model) = %d, want > %d", v1, v0)
		}
	})
}

func TestAuditT3MergePatchNullClearsTypedField(t *testing.T) {
	r := newAuditT3CIRouter(t, "6c")
	id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-null")
	if _, err := r.f.Admin.Exec(context.Background(), `UPDATE ci SET hostname = 'host-a', model = 'model-a', firmware_version = '1.2.3' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"hostname", "model", "firmware_version"} {
		t.Run(field, func(t *testing.T) {
			w := r.call(http.MethodPatch, "/api/v1/cis/"+id, "", `{"`+field+`":null}`)
			got := r.str(`SELECT coalesce(`+field+`, '<NULL>') FROM ci WHERE id = $1`, id)
			if w.Code != http.StatusOK || got != "<NULL>" {
				t.Errorf("PATCH {%q:null} = %d, stored %s = %q; want 200 and NULL (RFC 7396: null removes the member)", field, w.Code, field, got)
			}
		})
	}
}
