package ci_test

// Phase-1 acceptance audit, group T3-data-ingest: test harness (no test of
// its own). It wires the production CI and override handlers on the
// PostgreSQL repositories, as the server does for /api/v1/cis/**. Used by
// bulk_integration_test.go (TestAuditT3BulkStatusIsManualWrite, WP-368),
// concurrency_integration_test.go (TestAuditT3OverrideWritesRaiseVersion,
// WP-412) and audit_t3_manual_writes_auditdefect_test.go (WP-057 defect).

import (
	"context"
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
