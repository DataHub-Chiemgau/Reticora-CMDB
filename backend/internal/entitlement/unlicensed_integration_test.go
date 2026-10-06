package entitlement_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestUnlicensedCIs covers WP-073 (ENT-03, ENT-08, CH21, E-22) against
// PostgreSQL:
//   - a new discovered device over max_cis becomes an unlicensed_ci review
//     item with the full device record instead of a CI, once per device;
//     existing CIs keep updating;
//   - a manual adoption is refused while the limit is reached (problem type
//     entitlement-limit); dismiss discards the item and is audited;
//   - raising the limit adopts the waiting items as CIs (audited);
//   - a downgrade deletes no CI, the stock stays readable, only new CIs are
//     blocked (REST) or held (ingest).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestUnlicensedCIs(t *testing.T) {
	f := scopetest.Seed(t, "65")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	ciRepo := ci.NewPGRepository(f.App)
	svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
	h := discovery.NewHandler(discovery.NewPGRepository(f.App), ciRepo).WithLimits(svc)
	svc.WithUnlicensed(h)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	countCIs := func() int {
		t.Helper()
		_, total, err := ciRepo.List(ctx, f.OrgA, ci.FilterParams{}, api.PaginationParams{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		return total
	}
	setLimit := func(limit int) {
		t.Helper()
		if _, err := svc.Grant(ctx, entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureCMDBCore,
			Plan: entitlement.PlanEssential, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxCIs: int64(limit)}}); err != nil {
			t.Fatal(err)
		}
	}
	serve := func(method, path, body string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rctx := tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(rctx))
		return w.Code, w.Body.Bytes()
	}
	device := func(n int, model string) map[string]any {
		return map[string]any{"ci_type_name": "server", "name": fmt.Sprintf("unlic-%s-%d", f.ID(), n),
			"manufacturer": "Acme", "model": model, "serial_number": fmt.Sprintf("UNLIC-65-%d", n),
			"fingerprint": map[string]any{}, "raw_data": map[string]any{"slot": n}}
	}
	ingest := func(items ...map[string]any) discovery.BulkIngestResponse {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"items": items})
		status, raw := serve(http.MethodPost, "/api/v1/ingest/bulk", string(body))
		var resp discovery.BulkIngestResponse
		if status != http.StatusAccepted || json.Unmarshal(raw, &resp) != nil {
			t.Fatalf("ingest: %d %s", status, raw)
		}
		return resp
	}
	openItems := func() []discovery.ReviewItem {
		t.Helper()
		_, raw := serve(http.MethodGet, "/api/v1/discovery/review-items?status=open&kind=unlicensed_ci", "")
		var list api.ListResponse[discovery.ReviewItem]
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatal(err)
		}
		return list.Data
	}
	auditActions := func(itemID string) []string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT action FROM audit_log WHERE resource_id = $1 ORDER BY timestamp`, itemID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var a string
			if err = rows.Scan(&a); err != nil {
				t.Fatal(err)
			}
			out = append(out, a)
		}
		return out
	}

	// One CI fits under the limit, the other two devices are held.
	base := countCIs()
	setLimit(base + 1)
	queuedBefore := discovery.UnlicensedQueued()
	if resp := ingest(device(1, "m1"), device(2, "m1"), device(3, "m1")); resp.Created != 1 || resp.Unlicensed != 2 {
		t.Fatalf("first ingest: %+v, want 1 created and 2 unlicensed", resp)
	}
	held := openItems()
	if len(held) != 2 || countCIs() != base+1 || discovery.UnlicensedQueued()-queuedBefore != 2 {
		t.Fatalf("held items %d, CIs %d (base %d)", len(held), countCIs(), base)
	}
	snapshot, _ := held[0].Payload["device"].(map[string]any)
	if snapshot["serial_number"] == nil || snapshot["raw_data"] == nil || held[0].Payload["ci_type_id"] == "" {
		t.Errorf("device record snapshot incomplete: %+v", held[0].Payload)
	}

	// The same devices again: the CI is updated, the held ones are not queued
	// twice.
	if resp := ingest(device(1, "m2"), device(2, "m2"), device(3, "m2")); resp.Updated != 1 || resp.Unlicensed != 2 || resp.Created != 0 {
		t.Fatalf("second ingest: %+v", resp)
	}
	if n := len(openItems()); n != 2 {
		t.Fatalf("held items after the second ingest: %d, want 2", n)
	}
	modelOf := func(serial string) string {
		t.Helper()
		var model string
		if scanErr := f.Admin.QueryRow(bg, `SELECT model FROM ci WHERE organization_id = $1 AND serial_number = $2`,
			f.OrgA, serial).Scan(&model); scanErr != nil {
			t.Fatalf("CI %s: %v", serial, scanErr)
		}
		return model
	}
	if got := modelOf("UNLIC-65-1"); got != "m2" {
		t.Fatalf("existing CI not updated over the limit: model %q", got)
	}

	// Manual adoption is refused at the limit; dismiss discards and audits.
	status, raw := serve(http.MethodPost, "/api/v1/discovery/review-items/"+held[0].ID+"/resolve", `{"action":"create"}`)
	if status != http.StatusForbidden || !strings.Contains(string(raw), api.ProblemEntitlementLimit) {
		t.Fatalf("adoption at the limit: %d %s", status, raw)
	}
	if status, raw = serve(http.MethodPost, "/api/v1/discovery/review-items/"+held[0].ID+"/resolve", `{"action":"merge","ci_id":"x"}`); status != http.StatusUnprocessableEntity {
		t.Fatalf("merge of an unlicensed_ci: %d %s", status, raw)
	}
	if status, raw = serve(http.MethodPost, "/api/v1/discovery/review-items/"+held[0].ID+"/resolve", `{"action":"dismiss"}`); status != http.StatusOK ||
		!strings.Contains(string(raw), `"status":"dismissed"`) {
		t.Fatalf("dismiss: %d %s", status, raw)
	}
	if got := auditActions(held[0].ID); len(got) != 1 || got[0] != "unlicensed_ci.dismissed" {
		t.Errorf("dismiss audit: %v", got)
	}

	// Raising the limit adopts the waiting device.
	setLimit(base + 10)
	if n := len(openItems()); n != 0 {
		t.Fatalf("open items after raising the limit: %d", n)
	}
	if countCIs() != base+2 {
		t.Fatalf("CIs after adoption: %d, want %d", countCIs(), base+2)
	}
	if got := auditActions(held[1].ID); len(got) != 1 || got[0] != "unlicensed_ci.resolved" {
		t.Errorf("adoption audit: %v", got)
	}
	adoptedSerial := held[1].Payload["device"].(map[string]any)["serial_number"].(string)
	if got := modelOf(adoptedSerial); got != "m1" {
		t.Errorf("adopted CI from the device record: model %q", got)
	}

	// Downgrade below the stock: nothing is deleted, the stock stays
	// readable, new CIs are blocked (REST) or held (ingest).
	setLimit(1)
	if got := countCIs(); got != base+2 {
		t.Fatalf("downgrade changed the stock: %d, want %d", got, base+2)
	}
	if err := svc.AllowCreate(ctx, f.OrgA, entitlement.LimitMaxCIs, int64(countCIs())); err == nil {
		t.Error("REST creation allowed after the downgrade")
	}
	if resp := ingest(device(1, "m3"), device(4, "m3")); resp.Updated != 1 || resp.Unlicensed != 1 || resp.Created != 0 {
		t.Errorf("ingest after the downgrade: %+v", resp)
	}
}
