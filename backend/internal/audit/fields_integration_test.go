package audit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAuditFieldsAndFiltersPG covers AUD-05 and AUD-07 (WP-133) against
// PostgreSQL: an entry stores source, request and correlation id from the
// request context, they enter the hash (a changed correlation id breaks the
// chain), the source is checked, and GET /api/v1/audit filters by action,
// resource, actor, source, request, correlation id and time.
func TestAuditFieldsAndFiltersPG(t *testing.T) {
	f := scopetest.Seed(t, "95")
	bg := context.Background()
	recorder := audit.NewPGRecorder()
	record := func(ctx context.Context, e audit.Entry) *audit.Entry {
		t.Helper()
		var out *audit.Entry
		scope := database.OrgWideScope(f.OrgA, "")
		if err := database.WithTenant(ctx, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, err = recorder.Record(ctx, tx, e)
			return err
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
		return out
	}
	resource := f.ID()
	reqCtx := audit.WithRequestInfo(bg, audit.RequestInfo{RequestID: "req-1", CorrelationID: "op-7", Source: audit.SourceAPI})
	first := record(reqCtx, audit.Entry{OrganizationID: f.OrgA, ActorID: f.User, ActorType: "api_key", Action: "assets.create",
		ResourceType: "assets", ResourceID: resource})
	if first.Source != audit.SourceAPI || first.RequestID != "req-1" || first.CorrelationID != "op-7" || !audit.Recorded(reqCtx) {
		t.Errorf("entry from the request context: %+v", first)
	}
	second := record(bg, audit.Entry{OrganizationID: f.OrgA, Action: "reservation.expire", ResourceType: "reservations"})
	if second.Source != audit.SourceSystem || second.RequestID != "" {
		t.Errorf("system entry: %+v", second)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO audit_log (organization_id, action, resource_type, source, hash) VALUES ($1, 'x', 'y', 'mail', '')`, f.OrgA); err == nil {
		t.Error("an unknown source was stored")
	}

	h := audit.NewHandler(f.App)
	list := func(query string) (int, []audit.Entry) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit?"+query, nil)
		ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		w := httptest.NewRecorder()
		h.List(w, req.WithContext(ctx))
		var body api.ListResponse[audit.Entry]
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Data
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for query, want := range map[string]string{
		"correlation_id=op-7":        first.ID,
		"request_id=req-1":           first.ID,
		"source=api":                 first.ID,
		"action=reservation.expire":  second.ID,
		"resource_id=" + resource:    first.ID,
		"actor_id=" + f.User:         first.ID,
		"resource_type=reservations": second.ID,
		"to=" + future + "&source=system&resource_type=reservations": second.ID,
	} {
		code, entries := list(query)
		if code != http.StatusOK || len(entries) != 1 || entries[0].ID != want {
			t.Errorf("%s: %d %+v, want %s", query, code, entries, want)
		}
	}
	if _, entries := list("from=" + future); len(entries) != 0 {
		t.Errorf("from the future: %+v", entries)
	}
	for _, bad := range []string{"source=mail", "resource_id=x", "from=yesterday"} {
		if code, _ := list(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}

	if result, err := audit.Verify(bg, f.App, f.OrgA); err != nil || !result.Intact {
		t.Fatalf("chain: %+v %v", result, err)
	}
	// The correlation id is part of the hash.
	conn, err := f.Admin.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(bg, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(bg, `UPDATE audit_log SET correlation_id = 'op-8' WHERE id = $1`, first.ID)
	_, _ = conn.Exec(bg, `SET session_replication_role = origin`)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := audit.Verify(bg, f.App, f.OrgA); err != nil || result.Intact || result.BrokenID != first.ID {
		t.Errorf("after changing the correlation id: %+v %v", result, err)
	}
}
