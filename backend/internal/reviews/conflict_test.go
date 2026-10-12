package reviews_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// overrides treats the listed fields as manual overrides and records which
// ones were cleared.
type overrides struct {
	mu        sync.Mutex
	protected map[string]bool
	cleared   []string
}

func (o *overrides) RecordDiscovered(context.Context, string, string, string, any, string, time.Time) (*discovery.FieldProvenance, error) {
	return &discovery.FieldProvenance{}, nil
}

func (o *overrides) Decide(ctx context.Context, w *override.Write) override.Decision {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.protected[w.CIID+"/"+w.Field] {
		return override.Decision{Conflict: true, Reason: override.ReasonOverrideConflict}
	}
	return override.DecideAutomatedWrite(ctx, nil, w)
}

func (o *overrides) ClearOverride(_ context.Context, _, ciID, field string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.protected, ciID+"/"+field)
	o.cleared = append(o.cleared, ciID+"/"+field)
	return nil
}

type conflictEnv struct {
	t   *testing.T
	mux *chi.Mux
	ctx context.Context
}

func (e *conflictEnv) call(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(e.ctx))
	return w
}

func (e *conflictEnv) ingest(items string) discovery.BulkIngestResponse {
	e.t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/bulk", bytes.NewBufferString(`{"collector_id":"c","items":[`+items+`]}`))
	e.mux.ServeHTTP(w, req.WithContext(e.ctx))
	if w.Code != http.StatusAccepted {
		e.t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	var resp discovery.BulkIngestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func (e *conflictEnv) open(kind string) []reviews.Item {
	e.t.Helper()
	w := e.call(http.MethodGet, "/api/v1/review-items?status=open&kind="+kind, "")
	var list api.ListResponse[reviews.Item]
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		e.t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	return list.Data
}

func (e *conflictEnv) resolve(id, body string) reviews.Item {
	e.t.Helper()
	w := e.call(http.MethodPost, "/api/v1/review-items/"+id+"/resolve", body)
	var item reviews.Item
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &item) != nil {
		e.t.Fatalf("resolve %s: %d %s", body, w.Code, w.Body.String())
	}
	return item
}

// TestOverrideConflictLifecycle covers WP-089 (REC-12): a reported value
// differing from a manual override opens one override_conflict per field;
// dismiss keeps the override and suppresses that value until the source
// reports another; accept removes the override so the observed value
// applies.
func TestOverrideConflictLifecycle(t *testing.T) {
	repo := discovery.NewMemoryRepository()
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	device := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "manual", SerialNumber: "OC-89", Attributes: map[string]any{}}
	if err := cis.Create(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	prov := &overrides{protected: map[string]bool{device.ID + "/name": true}}
	mux := chi.NewRouter()
	discovery.NewHandler(repo, cis).WithProvenance(prov).RegisterRoutes(mux)
	env := &conflictEnv{t: t, mux: mux, ctx: tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "u-89"})}
	report := func(name string) discovery.BulkIngestResponse {
		return env.ingest(`{"ci_type_name":"server","name":"` + name + `","serial_number":"OC-89","source":"redfish"}`)
	}

	report("seen-1")
	report("seen-1")
	items := env.open(reviews.KindOverrideConflict)
	if len(items) != 1 || items[0].Payload["observed_value"] != "seen-1" || items[0].ObjectKey != "ci:"+device.ID+"/name" {
		t.Fatalf("one item per field: %+v", items)
	}

	// Dismiss: the override stays, the same value is suppressed.
	if got := env.resolve(items[0].ID, `{"action":"dismiss"}`); got.Status != reviews.StatusDismissed || got.Action != reviews.ActionDismiss {
		t.Fatalf("dismissed: %+v", got)
	}
	if resp := report("seen-1"); resp.ReviewItems != 0 || len(env.open(reviews.KindOverrideConflict)) != 0 {
		t.Errorf("dismissed value reported again: %+v", resp)
	}
	if got, _ := cis.GetByID(context.Background(), "org-1", device.ID); got.Name != "manual" {
		t.Errorf("override lost: %q", got.Name)
	}

	// A new value opens a new item; accept removes the override.
	if resp := report("seen-2"); resp.ReviewItems != 1 {
		t.Fatalf("new value: %+v", resp)
	}
	items = env.open(reviews.KindOverrideConflict)
	if len(items) != 1 || items[0].Payload["observed_value"] != "seen-2" {
		t.Fatalf("item of the new value: %+v", items)
	}
	if got := env.resolve(items[0].ID, `{"action":"accept"}`); got.Status != reviews.StatusResolved || got.Action != reviews.ActionAccept {
		t.Fatalf("accepted: %+v", got)
	}
	if len(prov.cleared) != 1 || prov.cleared[0] != device.ID+"/name" {
		t.Errorf("cleared overrides: %v", prov.cleared)
	}
	// Without the override the source writes the field again.
	report("seen-3")
	if got, _ := cis.GetByID(context.Background(), "org-1", device.ID); got.Name != "seen-3" {
		t.Errorf("name after accept: %q", got.Name)
	}
}

// TestConflictingValuesKeepTheReportedValue covers WP-089 (REC-06): a
// lower-ranked serial number for a CI matched by hardware UUID is not
// written but reviewed as conflicting_values with the value as reported;
// accept applies it, dismiss keeps the stored one.
func TestConflictingValuesKeepTheReportedValue(t *testing.T) {
	repo := discovery.NewMemoryRepository()
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	device := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "srv", SerialNumber: "SN-REDFISH",
		HardwareUUID: "4c4c4544-0042-3510-8051-b4c04f4a4e33", DiscoverySource: ci.SourceRedfish, Attributes: map[string]any{}}
	if err := cis.Create(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	discovery.NewHandler(repo, cis).RegisterRoutes(mux)
	env := &conflictEnv{t: t, mux: mux, ctx: tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})}

	resp := env.ingest(`{"ci_type_name":"server","name":"srv","hardware_uuid":"4c4c4544-0042-3510-8051-b4c04f4a4e33","serial_number":"sn-ssh","source":"ssh"}`)
	if resp.ReviewItems != 1 {
		t.Fatalf("lower-ranked serial: %+v", resp)
	}
	items := env.open(reviews.KindConflictingValues)
	if len(items) != 1 {
		t.Fatalf("conflicting_values: %+v", items)
	}
	incoming, _ := items[0].Payload["incoming"].(map[string]any)
	existing, _ := items[0].Payload["existing"].(map[string]any)
	if incoming["serial_number"] != "sn-ssh" || existing["serial_number"] != "SN-REDFISH" || items[0].Payload["incoming_source"] != "ssh" {
		t.Errorf("payload: %+v", items[0].Payload)
	}
	if got, _ := cis.GetByID(context.Background(), "org-1", device.ID); got.SerialNumber != "SN-REDFISH" {
		t.Errorf("stored serial overwritten: %q", got.SerialNumber)
	}
	// Repeated reports keep one item; accept applies the reported value.
	env.ingest(`{"ci_type_name":"server","name":"srv","hardware_uuid":"4c4c4544-0042-3510-8051-b4c04f4a4e33","serial_number":"sn-ssh","source":"ssh"}`)
	if items = env.open(reviews.KindConflictingValues); len(items) != 1 {
		t.Fatalf("after a repeated report: %+v", items)
	}
	env.resolve(items[0].ID, `{"action":"accept"}`)
	if got, _ := cis.GetByID(context.Background(), "org-1", device.ID); got.SerialNumber != "sn-ssh" {
		t.Errorf("serial after accept: %q", got.SerialNumber)
	}
	// An equal or higher rank writes without a review.
	if resp = env.ingest(`{"ci_type_name":"server","name":"srv","hardware_uuid":"4c4c4544-0042-3510-8051-b4c04f4a4e33","serial_number":"SN-NEW","source":"redfish"}`); resp.ReviewItems != 0 {
		t.Errorf("higher rank: %+v", resp)
	}
	if got, _ := cis.GetByID(context.Background(), "org-1", device.ID); got.SerialNumber != "SN-NEW" {
		t.Errorf("serial after redfish: %q", got.SerialNumber)
	}
}
