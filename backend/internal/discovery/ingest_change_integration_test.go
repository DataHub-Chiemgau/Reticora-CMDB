// AUD-03 in the ingest (WP-489; AUD-03, CI-11, EVT-02; F-095, F-250): every
// change one ingest record makes on a known CI (fields, status unknown ->
// active, reclassification generic_device -> specific type) runs in one
// transaction through the transaction variant of the CI repository
// (ci.RecordUpdater) and is one change: one change set in ci_change with
// change_kind updated or type_changed, the record's collector, source
// (collector), observation time and record id as correlation id, and
// exactly one event ci.changed. The audit action ci.type_changed stays.
//
//   - TestIngestRecordIsOneChange: a record with a type change gives one
//     change set (type_changed, with collector_id, source, observed_at and
//     correlation_id = record_id), one ci.changed naming ci_type_id and the
//     fields, the specific type, the audit entry ci.type_changed and the
//     search document after the commit; the same record again (only
//     last_seen_at) writes no ci_change and no event; a field change
//     without type change is one change set updated.
//   - TestIngestReclassificationRollsBackWithTheRecord: a failure after the
//     field update (injected on the ci_type_id row of the change set) rolls
//     back the fields, the status and the type change: no ci_change, no
//     event, no audit entry, no search document.
//   - TestIngestNeverLowersOrSwitchesSpecificType: a generic_device record
//     of a specific CI does not lower its type, a record of another specific
//     type does not change it (REC-02 creates a new CI), and the repository
//     refuses a switch between specific types in the record update while
//     the fields are written.
//
// The CI repository is wired as in main.go: PGRepository with audit and
// event outbox, decorated by ci.NewIndexingRepository. The records arrive
// as messages from the queue (ProcessMessages), as from a collector.
//
// Retest (F-095, F-250):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable go test -count=1 -v \
//	  -run 'TestIngestRecordIsOneChange|TestIngestReclassificationRollsBackWithTheRecord|TestIngestNeverLowersOrSwitchesSpecificType' ./internal/discovery/
package discovery_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// changeIngest is the ingest of one organization with the CI repository
// wired as in main.go.
type changeIngest struct {
	t         *testing.T
	f         *scopetest.Fixture
	cis       ci.Repository
	h         *discovery.Handler
	collector string
	types     map[string]string
}

func newChangeIngest(t *testing.T) *changeIngest {
	t.Helper()
	f := scopetest.Seed(t, "0c")
	cis := ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox()),
		search.NewCIIndexer(search.NewPGRepository(f.App)))
	h := discovery.NewHandler(discovery.NewPGRepository(f.App), cis, relationship.NewPGRepository(f.App)).
		WithProvenance(manyProvenance{provenance{repo: override.NewPGRepository(f.App)}})
	c := &changeIngest{t: t, f: f, cis: cis, h: h, collector: f.ID(), types: map[string]string{}}
	for _, key := range []string{"generic_device", "switch", "server"} {
		var id string
		if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatalf("type %s: %v", key, err)
		}
		c.types[key] = id
	}
	return c
}

// seedCI inserts a CI of type typeKey with status, serial number and
// management IP; it has no search document.
func (c *changeIngest) seedCI(typeKey, status, name, serial, ip string) string {
	c.t.Helper()
	id := c.f.ID()
	if _, err := c.f.Admin.Exec(context.Background(), `INSERT INTO ci (id, organization_id, ci_type_id, name, status, serial_number, management_ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, c.f.OrgA, c.types[typeKey], name, status, serial, ip); err != nil {
		c.t.Fatal(err)
	}
	return id
}

// send processes item as one record of the collector from the queue and
// returns its record id and the processing error.
func (c *changeIngest) send(at time.Time, item discovery.IngestItem) (string, error) {
	c.t.Helper()
	recordID := plugins.NewRecordID()
	_, err := c.h.ProcessMessages(context.Background(), []discovery.IngestMessage{{
		OrganizationID: c.f.OrgA, CollectorID: c.collector, Scope: &discovery.MessageScope{UserID: c.f.User},
		Item: item, RecordID: recordID, ObservedAt: at,
	}})
	return recordID, err
}

// changeRow is a ci_change row with its AUD-03 columns.
type changeRow struct {
	setID, kind, field, changeType, collector, source, correlation string
	observedAt                                                     *time.Time
}

func (c *changeIngest) changes(ciID string) []changeRow {
	c.t.Helper()
	rows, err := c.f.Admin.Query(context.Background(), `SELECT COALESCE(change_set_id::text, ''), change_kind, COALESCE(field_name, ''),
		change_type, COALESCE(collector_id::text, ''), COALESCE(source, ''), COALESCE(correlation_id, ''), observed_at
		FROM ci_change WHERE ci_id = $1 ORDER BY created_at, field_name`, ciID)
	if err != nil {
		c.t.Fatal(err)
	}
	defer rows.Close()
	var out []changeRow
	for rows.Next() {
		var r changeRow
		if err := rows.Scan(&r.setID, &r.kind, &r.field, &r.changeType, &r.collector, &r.source, &r.correlation, &r.observedAt); err != nil {
			c.t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		c.t.Fatal(err)
	}
	return out
}

// changedEvents returns the changed_fields of the ci.changed events of ciID.
func (c *changeIngest) changedEvents(ciID string) [][]string {
	c.t.Helper()
	rows, err := c.f.Admin.Query(context.Background(), `SELECT COALESCE(ARRAY(SELECT jsonb_array_elements_text(data->'changed_fields')), '{}')
		FROM event_outbox WHERE organization_id = $1 AND type = $2 AND entity_id = $3 ORDER BY sequence`, c.f.OrgA, events.CIChanged, ciID)
	if err != nil {
		c.t.Fatal(err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		var fields []string
		if err := rows.Scan(&fields); err != nil {
			c.t.Fatal(err)
		}
		out = append(out, fields)
	}
	if err := rows.Err(); err != nil {
		c.t.Fatal(err)
	}
	return out
}

func (c *changeIngest) count(query string, args ...any) int {
	c.t.Helper()
	var n int
	if err := c.f.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		c.t.Fatal(err)
	}
	return n
}

// ciState is the type key, status and manufacturer of a CI.
func (c *changeIngest) ciState(id string) (typeKey, status, manufacturer string) {
	c.t.Helper()
	if err := c.f.Admin.QueryRow(context.Background(), `SELECT t.key, c.status, COALESCE(c.manufacturer, '')
		FROM ci c JOIN ci_type t ON t.id = c.ci_type_id WHERE c.id = $1`, id).Scan(&typeKey, &status, &manufacturer); err != nil {
		c.t.Fatal(err)
	}
	return typeKey, status, manufacturer
}

func hasField(fields []string, name string) bool {
	for _, f := range fields {
		if f == name {
			return true
		}
	}
	return false
}

func TestIngestRecordIsOneChange(t *testing.T) {
	c := newChangeIngest(t)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	dev := c.seedCI("generic_device", "unknown", "wp489-sw", "SN-WP489-1", "198.51.100.89")

	// The SNMP record classifies the device as a switch and reports its
	// manufacturer and model.
	switchRecord := discovery.IngestItem{CITypeName: "switch", Name: "wp489-sw", SerialNumber: "SN-WP489-1",
		ManagementIP: "198.51.100.89", Manufacturer: "Cisco", Model: "C9300-48P", Source: "snmp"}
	recordID, err := c.send(at, switchRecord)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if typeKey, status, manufacturer := c.ciState(dev); typeKey != "switch" || status != "active" || manufacturer != "Cisco" {
		t.Fatalf("CI after the record: type %s, status %s, manufacturer %q; want switch, active, Cisco", typeKey, status, manufacturer)
	}
	rows := c.changes(dev)
	sets := map[string]bool{}
	fields := map[string]bool{}
	for _, r := range rows {
		sets[r.setID] = true
		fields[r.field] = true
		if r.kind != ci.ChangeKindTypeChanged || r.collector != c.collector || r.source != audit.SourceCollector ||
			r.correlation != recordID || r.observedAt == nil || !r.observedAt.Equal(at) {
			t.Errorf("ci_change row %+v; want change_kind type_changed, collector %s, source collector, correlation_id %s, observed_at %s",
				r, c.collector, recordID, at)
		}
	}
	if len(sets) != 1 || sets[""] {
		t.Errorf("the record wrote %d change sets (%v), want exactly one", len(sets), sets)
	}
	for _, f := range []string{"ci_type_id", "status", "manufacturer", "model"} {
		if !fields[f] {
			t.Errorf("the change set has no row for %s: %+v", f, rows)
		}
	}
	for _, r := range rows {
		if r.field == "ci_type_id" && r.changeType != "type_changed" {
			t.Errorf("the type row has change_type %s, want type_changed", r.changeType)
		}
	}
	evs := c.changedEvents(dev)
	if len(evs) != 1 {
		t.Fatalf("the record wrote %d ci.changed events, want exactly one: %v", len(evs), evs)
	}
	for _, f := range []string{"ci_type_id", "status", "manufacturer"} {
		if !hasField(evs[0], f) {
			t.Errorf("changed_fields %v lacks %s", evs[0], f)
		}
	}
	if n := c.count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id = $2 AND action = 'ci.type_changed'`, c.f.OrgA, dev); n != 1 {
		t.Errorf("%d audit entries ci.type_changed, want 1", n)
	}
	// The CI is indexed after the commit with the state of the record.
	if n := c.count(`SELECT count(*) FROM search_document WHERE entity_id = $1 AND summary LIKE '%Cisco%'`, dev); n != 1 {
		t.Errorf("%d search documents of the CI with the new manufacturer, want 1", n)
	}

	// The same record again is a sighting: last_seen_at only, no history
	// and no event (AUD-03 "nicht historisiert").
	seenBefore := at.Add(-24 * time.Hour)
	if _, seenErr := c.f.Admin.Exec(context.Background(), `UPDATE ci SET last_seen_at = $2 WHERE id = $1`, dev, seenBefore); seenErr != nil {
		t.Fatal(seenErr)
	}
	if _, err = c.send(at.Add(time.Minute), switchRecord); err != nil {
		t.Fatalf("sighting: %v", err)
	}
	if got := c.changes(dev); len(got) != len(rows) {
		t.Errorf("the sighting wrote %d ci_change rows, want none", len(got)-len(rows))
	}
	if got := c.changedEvents(dev); len(got) != 1 {
		t.Errorf("the sighting wrote %d ci.changed events, want none", len(got)-1)
	}
	var seenAfter time.Time
	if seenErr := c.f.Admin.QueryRow(context.Background(), `SELECT last_seen_at FROM ci WHERE id = $1`, dev).Scan(&seenAfter); seenErr != nil {
		t.Fatal(seenErr)
	}
	if !seenAfter.After(seenBefore) {
		t.Errorf("last_seen_at %s not advanced from %s by the sighting", seenAfter, seenBefore)
	}

	// A field change without type change: one change set updated with the
	// record's origin, one more event.
	changedModel := switchRecord
	changedModel.Model = "C9300-24P"
	recordID, err = c.send(at.Add(2*time.Minute), changedModel)
	if err != nil {
		t.Fatalf("model change: %v", err)
	}
	added := c.changes(dev)[len(rows):]
	if len(added) != 1 || added[0].field != "model" || added[0].kind != ci.ChangeKindUpdated || added[0].correlation != recordID ||
		added[0].collector != c.collector || added[0].setID == "" || sets[added[0].setID] {
		t.Errorf("model change rows %+v; want one new change set updated for model with the record's origin", added)
	}
	if got := c.changedEvents(dev); len(got) != 2 || !hasField(got[1], "model") || hasField(got[1], "ci_type_id") {
		t.Errorf("ci.changed events after the model change: %v; want one more naming model only", got)
	}
}

func TestIngestReclassificationRollsBackWithTheRecord(t *testing.T) {
	c := newChangeIngest(t)
	bg := context.Background()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	dev := c.seedCI("generic_device", "unknown", "wp489-rb", "SN-WP489-2", "198.51.100.90")

	// The ci_type_id row of the change set fails: it is written after the
	// field update and the type change of the record.
	if _, err := c.f.Admin.Exec(bg, `CREATE OR REPLACE FUNCTION wp489_fail_type_row() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.ci_id = '`+dev+`'::uuid AND NEW.field_name = 'ci_type_id' THEN
				RAISE EXCEPTION 'injected failure after the field update';
			END IF;
			RETURN NEW;
		END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.f.Admin.Exec(bg, `CREATE TRIGGER wp489_fail_type_row BEFORE INSERT ON ci_change FOR EACH ROW EXECUTE FUNCTION wp489_fail_type_row()`); err != nil {
		t.Fatal(err)
	}
	dropped := false
	drop := func() {
		if dropped {
			return
		}
		dropped = true
		if _, err := c.f.Admin.Exec(context.Background(), `DROP TRIGGER IF EXISTS wp489_fail_type_row ON ci_change; DROP FUNCTION IF EXISTS wp489_fail_type_row()`); err != nil {
			t.Errorf("drop injected trigger: %v", err)
		}
	}
	t.Cleanup(drop)

	record := discovery.IngestItem{CITypeName: "switch", Name: "wp489-rb", SerialNumber: "SN-WP489-2",
		ManagementIP: "198.51.100.90", Manufacturer: "Cisco", Model: "C9300-48P", Source: "snmp"}
	if _, err := c.send(at, record); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("ingest with the injected failure: %v; want the injected failure", err)
	}
	if typeKey, status, manufacturer := c.ciState(dev); typeKey != "generic_device" || status != "unknown" || manufacturer != "" {
		t.Errorf("CI after the failed record: type %s, status %s, manufacturer %q; want generic_device, unknown and no manufacturer",
			typeKey, status, manufacturer)
	}
	if rows := c.changes(dev); len(rows) != 0 {
		t.Errorf("the failed record left ci_change rows: %+v", rows)
	}
	if evs := c.changedEvents(dev); len(evs) != 0 {
		t.Errorf("the failed record left ci.changed events: %v", evs)
	}
	if n := c.count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id = $2`, c.f.OrgA, dev); n != 0 {
		t.Errorf("the failed record left %d audit entries", n)
	}
	if n := c.count(`SELECT count(*) FROM search_document WHERE entity_id = $1`, dev); n != 0 {
		t.Errorf("the failed record indexed the CI (%d documents)", n)
	}

	// Without the failure the record goes through as one change.
	drop()
	if _, err := c.send(at.Add(time.Minute), record); err != nil {
		t.Fatalf("ingest after the failure: %v", err)
	}
	if typeKey, status, _ := c.ciState(dev); typeKey != "switch" || status != "active" {
		t.Errorf("CI after the redelivered record: type %s, status %s; want switch, active", typeKey, status)
	}
	sets := map[string]bool{}
	for _, r := range c.changes(dev) {
		sets[r.setID] = true
	}
	if len(sets) != 1 || len(c.changedEvents(dev)) != 1 {
		t.Errorf("redelivered record: %d change sets and %d ci.changed events, want 1 and 1", len(sets), len(c.changedEvents(dev)))
	}
}

func TestIngestNeverLowersOrSwitchesSpecificType(t *testing.T) {
	c := newChangeIngest(t)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	dev := c.seedCI("switch", "active", "wp489-spec", "SN-WP489-3", "198.51.100.91")

	// A generic_device record of the switch matches it and keeps its type.
	if _, err := c.send(at, discovery.IngestItem{CITypeName: "generic_device", Name: "wp489-spec", SerialNumber: "SN-WP489-3",
		ManagementIP: "198.51.100.91", Source: "sweep"}); err != nil {
		t.Fatalf("generic record: %v", err)
	}
	if typeKey, _, _ := c.ciState(dev); typeKey != "switch" {
		t.Errorf("a generic_device record lowered the switch to %s", typeKey)
	}
	// A record of another specific type does not change the switch (REC-02
	// creates a CI of its own).
	if _, err := c.send(at.Add(time.Minute), discovery.IngestItem{CITypeName: "server", Name: "wp489-spec", SerialNumber: "SN-WP489-3",
		ManagementIP: "198.51.100.91", Source: "ssh"}); err != nil {
		t.Fatalf("server record: %v", err)
	}
	if typeKey, _, _ := c.ciState(dev); typeKey != "switch" {
		t.Errorf("a server record changed the switch to %s", typeKey)
	}
	for _, r := range c.changes(dev) {
		if r.kind == ci.ChangeKindTypeChanged || r.field == "ci_type_id" {
			t.Errorf("type change recorded for a specific CI: %+v", r)
		}
	}

	// The record update refuses a switch between specific types in its
	// transaction and still writes the fields.
	updater, ok := c.cis.(ci.RecordUpdater)
	if !ok {
		t.Fatal("the CI repository wired as in main.go offers no record update (ci.RecordUpdater)")
	}
	ctx := tenant.WithTenant(c.f.OrgCtx(c.f.OrgA), tenant.TenantInfo{OrganizationID: c.f.OrgA, UserID: c.f.User})
	manufacturer := "Juniper"
	res, err := updater.UpdateRecord(ctx, c.f.OrgA, dev, &ci.RecordUpdate{Update: ci.UpdateRequest{Manufacturer: &manufacturer},
		TypeID: c.types["server"], Origin: ci.RecordOrigin{CollectorID: c.collector, ObservedAt: at, RecordID: "wp489-switch"}})
	if err != nil {
		t.Fatalf("record update: %v", err)
	}
	if res.TypeChanged || res.CI.CITypeID != c.types["switch"] || res.CI.Manufacturer != "Juniper" {
		t.Errorf("record update with a specific target: changed %v, type %s, manufacturer %s; want unchanged switch with Juniper",
			res.TypeChanged, res.CI.CITypeID, res.CI.Manufacturer)
	}
	var last []changeRow
	for _, r := range c.changes(dev) {
		if r.correlation == "wp489-switch" {
			last = append(last, r)
		}
	}
	if len(last) != 1 || last[0].field != "manufacturer" || last[0].kind != ci.ChangeKindUpdated {
		t.Errorf("rows of the refused switch: %+v; want one manufacturer row updated", last)
	}
	// The record update also never lowers the switch to generic_device.
	if res, err = updater.UpdateRecord(ctx, c.f.OrgA, dev, &ci.RecordUpdate{TypeID: c.types["generic_device"]}); err != nil {
		t.Fatalf("record update to generic_device: %v", err)
	}
	if res.TypeChanged || res.CI.CITypeID != c.types["switch"] {
		t.Errorf("record update lowered the switch: changed %v, type %s", res.TypeChanged, res.CI.CITypeID)
	}
}
