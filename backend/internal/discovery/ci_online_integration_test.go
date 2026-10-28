package discovery_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestIngestRaisesCIOnlineOnce is the retest of F-268 (WP-585, REC-09,
// LCY-05, EVT-02) against PostgreSQL, with the CI repository writing its
// events as the server does: the offline detector makes a CI not seen for
// four days unknown with ci.offline; a record observed before the last
// sighting brings it not back and raises no ci.online (GLO-12, WP-580); a
// newer sighting makes it active in the record's transaction with exactly
// one outbox row ci.online, after ci.changed, with the data of the
// detector's event; a further sighting of the active CI raises none, and
// the detector then changes nothing.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestRaisesCIOnlineOnce(t *testing.T) {
	f := scopetest.Seed(t, "82")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	h := discovery.NewHandler(discovery.NewPGRepository(f.App),
		ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox()))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	record := func(observed time.Time) plugins.DeviceRecord {
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "online-585", IP: "192.0.2.185", Serial: "ONLINE-585"}, observed)
	}
	var id string
	status := func() string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT id::text, status FROM ci WHERE organization_id = $1 AND serial_number = 'ONLINE-585'
			AND deleted_at IS NULL`, f.OrgA).Scan(&id, &s); err != nil {
			t.Fatalf("CI ONLINE-585: %v", err)
		}
		return s
	}
	// outbox lists the types of the CI's events in entity_seq order.
	outbox := func() []string {
		t.Helper()
		var types []string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(array_agg(type ORDER BY entity_seq), '{}') FROM event_outbox
			WHERE organization_id = $1 AND entity_id = $2`, f.OrgA, id).Scan(&types); err != nil {
			t.Fatal(err)
		}
		return types
	}
	count := func(types []string, typ string) int {
		n := 0
		for _, ty := range types {
			if ty == typ {
				n++
			}
		}
		return n
	}

	stale := time.Now().UTC().Add(-4 * 24 * time.Hour).Truncate(time.Microsecond)
	if resp := ingestRecords(t, mux, ctx, record(stale)); resp.Created != 1 {
		t.Fatalf("first sighting: %+v, want 1 created", resp)
	}
	if s := status(); s != ci.StatusActive {
		t.Fatalf("new CI: status %q, want active", s)
	}
	// Not seen for four days, beyond three times the default scope interval
	// of 24 hours: the detector makes the CI unknown with ci.offline.
	if _, err := lifecycle.NewOfflineDetector(f.App).RunOrg(bg, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if s, types := status(), outbox(); s != ci.StatusUnknown || count(types, events.CIOffline) != 1 || count(types, events.CIOnline) != 0 {
		t.Fatalf("after the detector: status %q, events %v; want unknown with one ci.offline", s, types)
	}

	// A record observed before the last sighting brings the CI not back.
	ingestRecords(t, mux, ctx, record(stale.Add(-time.Hour)))
	if s, types := status(), outbox(); s != ci.StatusUnknown || count(types, events.CIOnline) != 0 {
		t.Errorf("after an older record: status %q, events %v; want unknown without ci.online", s, types)
	}

	// Seen again: active with exactly one ci.online after ci.changed.
	seen := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if resp := ingestRecords(t, mux, ctx, record(seen)); resp.Updated != 1 {
		t.Fatalf("second sighting: %+v, want 1 updated", resp)
	}
	types := outbox()
	if s := status(); s != ci.StatusActive || count(types, events.CIOnline) != 1 {
		t.Fatalf("after the sighting: status %q, events %v; want active with one ci.online", s, types)
	}
	if n := len(types); n < 2 || types[n-1] != events.CIOnline || types[n-2] != events.CIChanged {
		t.Errorf("events %v: want ci.changed and then ci.online last", types)
	}
	var raw []byte
	if err := f.Admin.QueryRow(bg, `SELECT data FROM event_outbox WHERE organization_id = $1 AND entity_id = $2 AND type = 'ci.online'`,
		f.OrgA, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data["id"] != id || data["name"] != "online-585" || data["status"] != ci.StatusActive ||
		data["last_seen_at"] != seen.Format(time.RFC3339) {
		t.Errorf("ci.online data %v; want CI %s online-585, status active, last_seen_at %s", data, id, seen.Format(time.RFC3339))
	}

	// The CI is active already: a further sighting raises no ci.online.
	if resp := ingestRecords(t, mux, ctx, record(seen.Add(30*time.Second))); resp.Updated != 1 {
		t.Fatalf("third sighting: %+v, want 1 updated", resp)
	}
	if s, types := status(), outbox(); s != ci.StatusActive || count(types, events.CIOnline) != 1 {
		t.Errorf("after the third sighting: status %q, events %v; want active with still one ci.online", s, types)
	}
	// Nor does the detector: the CI is active and seen.
	if _, err := lifecycle.NewOfflineDetector(f.App).RunOrg(bg, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if s, types := status(), outbox(); s != ci.StatusActive || count(types, events.CIOnline) != 1 || count(types, events.CIOffline) != 1 {
		t.Errorf("after the detector again: status %q, events %v; want active with one ci.offline and one ci.online", s, types)
	}
}
