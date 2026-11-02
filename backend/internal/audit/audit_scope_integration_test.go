package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// TestAuditScope reads the audit log with the principal's tenant scope
// (TEN-06, WP-016): a principal restricted to client 1 does not see entries
// about client-2 CIs, an org-wide principal sees all of them, and the chain
// verification covers the whole organization without a request scope.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditScope(t *testing.T) {
	f := scopetest.Seed(t, "2a")
	own := f.CI(t, f.OrgA, f.Client1, "audit-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "audit-c2")

	recorder := audit.NewPGRecorder()
	scope := database.OrgWideScope(f.OrgA, "")
	if err := database.WithTenant(context.Background(), f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		for _, id := range []string{own, foreign} {
			if _, err := recorder.Record(ctx, tx, audit.Entry{OrganizationID: f.OrgA, Action: "ci.updated", ResourceType: "ci", ResourceID: id}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("record entries: %v", err)
	}

	handler := audit.NewHandler(f.App)
	list := func(ctx context.Context) map[string]bool {
		t.Helper()
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit?limit=100", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.List(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list: status %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Data []audit.Entry `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := map[string]bool{}
		for _, e := range body.Data {
			out[e.ResourceID] = true
		}
		return out
	}
	if got := list(f.ClientCtx(f.Client1)); !got[own] || got[foreign] {
		t.Fatalf("client-1 audit log: own=%v foreign=%v, want true/false", got[own], got[foreign])
	}
	if got := list(f.OrgCtx(f.OrgA)); !got[own] || !got[foreign] {
		t.Fatalf("org-wide audit log: own=%v foreign=%v, want true/true", got[own], got[foreign])
	}

	result, err := audit.Verify(context.Background(), f.App, f.OrgA)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !result.Intact || result.Checked != 2 {
		t.Fatalf("verify: %+v, want an intact chain of 2 entries", result)
	}
}

// TestAuditListWithoutScope covers the open evidence of WP-016 (TEN-06,
// evidence WP-536): the audit list runs only in the caller's tenant
// transaction. Without a tenant scope it returns ErrNoTenantScope and no
// entry, with the scope of organization B naming organization A
// ErrTenantMismatch; through the handler both answer a generic error
// without entries. The org-wide principal of organization A lists the entry
// (control).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditListWithoutScope(t *testing.T) {
	f := scopetest.Seed(t, "95")
	ci := f.CI(t, f.OrgA, f.Client1, "audit-noscope")
	scope := database.OrgWideScope(f.OrgA, "")
	if err := database.WithTenant(context.Background(), f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		_, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: f.OrgA, Action: "ci.updated", ResourceType: "ci", ResourceID: ci})
		return err
	}); err != nil {
		t.Fatalf("record entry: %v", err)
	}
	handler := audit.NewHandler(f.App)

	entries, err := handler.ListEntries(context.Background(), f.OrgA)
	if !errors.Is(err, database.ErrNoTenantScope) || len(entries) != 0 {
		t.Errorf("list without scope: %d entries, %v; want none and ErrNoTenantScope", len(entries), err)
	}
	entries, err = handler.ListEntries(f.OrgCtx(f.OrgB), f.OrgA)
	if !errors.Is(err, database.ErrTenantMismatch) || len(entries) != 0 {
		t.Errorf("list A with B scope: %d entries, %v; want none and ErrTenantMismatch", len(entries), err)
	}
	entries, err = handler.ListEntries(f.OrgCtx(f.OrgA), f.OrgA)
	if err != nil || len(entries) != 1 || entries[0].ResourceID != ci {
		t.Fatalf("list A: %+v, %v; want the one entry", entries, err)
	}

	// The handler with tenant info but without a database scope.
	for name, ctx := range map[string]context.Context{"no scope": context.Background(), "scope of B": f.OrgCtx(f.OrgB)} {
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit?limit=100", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.List(rec, req)
		if rec.Code < 400 || strings.Contains(rec.Body.String(), ci) || strings.Contains(rec.Body.String(), "tenant scope") {
			t.Errorf("GET /audit with %s: %d %s, want a generic error without entries", name, rec.Code, rec.Body.String())
		}
	}
}
