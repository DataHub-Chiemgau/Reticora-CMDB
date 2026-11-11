package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// addon sends method to /addons/{key} (or the catalog when key is empty)
// as operator op.
func (e *planEnv) addon(op, method, key, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	path := "/api/v1/admin/orgs/" + e.f.OrgA + "/addons/" + key
	if key == "" {
		path = "/api/v1/admin/addons"
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range e.session(op) {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

// TestOperatorBooksAndCancelsAddon covers PKG-03 (WP-883): booking answers
// 201, booking again 200, cancelling 204 and keeps the row disabled with
// its valid_until; cancelling again is 404. The catalog lists the add-ons.
func TestOperatorBooksAndCancelsAddon(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanEssential)
	until := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	body := `{"valid_until":"` + until.Format(time.RFC3339) + `","source":"billing"}`
	if w := e.addon("op-1", http.MethodPost, entitlement.FeatureIGA, body); w.Code != http.StatusCreated {
		t.Fatalf("book: %d %s", w.Code, w.Body.String())
	}
	if w := e.addon("op-1", http.MethodPost, entitlement.FeatureIGA, body); w.Code != http.StatusOK {
		t.Fatalf("book again: %d %s", w.Code, w.Body.String())
	}
	row := e.rows()[entitlement.FeatureIGA]
	if !row.Enabled || row.Origin != entitlement.OriginAddon {
		t.Errorf("booked row %+v", row)
	}
	if _, ok, _ := e.svc.Check(orgContext(context.Background(), e.f.OrgA), e.f.OrgA, entitlement.FeatureIGA); !ok {
		t.Error("iga not entitled right after the booking")
	}
	if w := e.addon("op-1", http.MethodDelete, entitlement.FeatureIGA, ""); w.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	var validUntil *time.Time
	var enabled bool
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT enabled, valid_until FROM entitlement
		WHERE organization_id = $1 AND feature_key = 'iga'`, e.f.OrgA).Scan(&enabled, &validUntil); err != nil {
		t.Fatal(err)
	}
	if enabled || validUntil == nil || !validUntil.Equal(until) {
		t.Errorf("cancelled row: enabled %v valid_until %v, want disabled with %v", enabled, validUntil, until)
	}
	if w := e.addon("op-1", http.MethodDelete, entitlement.FeatureIGA, ""); w.Code != http.StatusNotFound {
		t.Errorf("cancel again: %d, want 404", w.Code)
	}
	if w := e.addon("op-1", http.MethodDelete, entitlement.FeatureAI, ""); w.Code != http.StatusNotFound {
		t.Errorf("cancel a never booked add-on: %d, want 404", w.Code)
	}
	if w := e.addon("op-1", http.MethodPost, entitlement.FeatureAI, `{"valid_until":"2000-01-01T00:00:00Z"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("past valid_until: %d, want 422", w.Code)
	}
	if w := e.addon("op-1", http.MethodPost, entitlement.FeatureAI, ""); w.Code != http.StatusCreated {
		t.Errorf("book without body: %d %s", w.Code, w.Body.String())
	}
	w := e.addon("op-1", http.MethodGet, "", "")
	var catalog struct {
		Data []entitlement.AddonInfo `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Data) != len(entitlement.Addons) {
		t.Errorf("catalog: %d %s", w.Code, w.Body.String())
	}
}

// TestAddonBookingRefusesPlanKeys covers PKG-03: a key that is no add-on is
// 422 with field key, for booking and cancelling.
func TestAddonBookingRefusesPlanKeys(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanStandard)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if w := e.addon("op-1", method, entitlement.FeatureMonitoring, ""); w.Code != http.StatusUnprocessableEntity ||
			!strings.Contains(w.Body.String(), `"key"`) {
			t.Errorf("%s monitoring: %d %s, want 422 on key", method, w.Code, w.Body.String())
		}
	}
	if row := e.rows()[entitlement.FeatureMonitoring]; row.Origin != entitlement.OriginPlan || !row.Enabled {
		t.Errorf("monitoring row changed: %+v", row)
	}
}

// TestPlanChangeLeavesAddonsUntouched covers PKG-03 and AK5: a booked iga
// survives essential → enterprise → essential unchanged.
func TestPlanChangeLeavesAddonsUntouched(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanEssential)
	if w := e.addon("op-1", http.MethodPost, entitlement.FeatureIGA, ""); w.Code != http.StatusCreated {
		t.Fatalf("book: %d %s", w.Code, w.Body.String())
	}
	before := e.rows()[entitlement.FeatureIGA]
	for _, plan := range []string{"enterprise", "essential"} {
		if w, _ := e.change("op-1", `{"plan":"`+plan+`","reason":"addons stay"}`); w.Code != http.StatusOK {
			t.Fatalf("change to %s: %d %s", plan, w.Code, w.Body.String())
		}
		if after := e.rows()[entitlement.FeatureIGA]; after.Plan != before.Plan || !after.Enabled || after.Origin != entitlement.OriginAddon {
			t.Errorf("iga after the change to %s: %+v, before %+v", plan, after, before)
		}
	}
}

// TestAddonBookingIsAudited covers AK3: addon.book and addon.cancel are in
// operator_audit, each with an entitlement.changed naming the add-on; a
// failing audit entry rolls the booking back (500, no row, no event).
func TestAddonBookingIsAudited(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanStandard)
	bg := context.Background()
	const failing = "op-wp883-audit-fails"
	drop := func() {
		if _, err := e.f.Admin.Exec(bg, `ALTER TABLE operator_audit DROP CONSTRAINT IF EXISTS wp883_audit_fails`); err != nil {
			t.Fatal(err)
		}
	}
	drop()
	if _, err := e.f.Admin.Exec(bg, `ALTER TABLE operator_audit ADD CONSTRAINT wp883_audit_fails
		CHECK (operator_id <> '`+failing+`') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(drop)
	eventCount := func() int {
		return e.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'entitlement.changed'`, e.f.OrgA)
	}
	events0 := eventCount()
	mark := e.count(`SELECT COALESCE(max(id), 0) FROM operator_audit`)
	if w := e.addon(failing, http.MethodPost, entitlement.FeatureAI, ""); w.Code != http.StatusInternalServerError {
		t.Errorf("book with a failing audit: %d, want 500", w.Code)
	}
	if _, ok := e.rows()[entitlement.FeatureAI]; ok || eventCount() != events0 {
		t.Error("the booking without audit entry was not rolled back")
	}
	if w := e.addon("op-1", http.MethodPost, entitlement.FeatureAI, ""); w.Code != http.StatusCreated {
		t.Fatalf("book: %d %s", w.Code, w.Body.String())
	}
	if w := e.addon("op-1", http.MethodDelete, entitlement.FeatureAI, ""); w.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if n := e.count(`SELECT count(*) FROM operator_audit WHERE target_org = $1 AND action IN ('addon.book', 'addon.cancel')
		AND details->>'feature_key' = 'ai' AND details ? 'after' AND id > $2`, e.f.OrgA, mark); n != 2 {
		t.Errorf("audit entries addon.book/addon.cancel: %d, want 2", n)
	}
	r, err := e.f.Admin.Query(bg, `SELECT data FROM event_outbox WHERE organization_id = $1 AND type = 'entitlement.changed'
		ORDER BY sequence DESC LIMIT 2`, e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := 0
	for r.Next() {
		var raw []byte
		if err = r.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var data events.EntitlementChangedData
		if err = json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		if data.Addon != entitlement.FeatureAI || data.Plan != "standard" || data.PlanBefore != "" {
			t.Errorf("event data %s", raw)
		}
		n++
	}
	if n != 2 || eventCount() != events0+2 {
		t.Errorf("events: %d new, want 2", eventCount()-events0)
	}
}
