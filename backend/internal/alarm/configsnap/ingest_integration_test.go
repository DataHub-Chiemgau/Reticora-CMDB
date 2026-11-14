package configsnap_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm/configsnap"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The ingest of the device configurations on the edge against PostgreSQL
// (ALR-07, EVT-03, WP-1054): the edge handler of .config called directly,
// with the configuration store, the alarm lifecycle and the outbox.
//
// Retest: cd backend && go test -count=1 -p 1 -run 'TestConfigIngest' ./internal/alarm/configsnap/

// secret is a value the collector masked; it must reach no event.
const secret = "s3cr3t-community"

// ingestEnv is organization A with monitoring_network, an org-wide
// collector and a router with management address 10.54.0.1; organization B
// has a collector without the feature.
type ingestEnv struct {
	t          *testing.T
	f          *scopetest.Fixture
	h          *discovery.Handler
	collector  string
	collectorB string
	router     string
	ip         string
	at         time.Time
}

func newIngestEnv(t *testing.T) *ingestEnv {
	t.Helper()
	f := scopetest.Seed(t, "c5")
	e := &ingestEnv{t: t, f: f, ip: "10.54.0.1", at: time.Now().UTC().Add(-time.Hour).Truncate(time.Second)}
	features := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
	if _, err := features.Grant(f.OrgCtx(f.OrgA), &entitlement.Entitlement{OrganizationID: f.OrgA,
		FeatureKey: entitlement.FeatureMonitoringNetwork, Plan: entitlement.PlanPro, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.collector = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp1054', 'active') RETURNING id::text`, f.OrgA)
	e.collectorB = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp1054-b', 'active') RETURNING id::text`, f.OrgB)
	e.router = f.CI(t, f.OrgA, f.Client1, "wp1054-router")
	e.exec(`UPDATE ci SET management_ip = $2 WHERE id = $1`, e.router, e.ip)
	ingester := configsnap.NewIngester(f.App, configsnap.NewPGStore(f.App), alarm.NewPGService(f.App))
	e.h = discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)).WithEdgeConfig(ingester, features)
	return e
}

func (e *ingestEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *ingestEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// message is a configuration snapshot of ci collected at at.
func (e *ingestEnv) message(ciID, text string, at time.Time) []byte {
	e.t.Helper()
	content, err := probewire.GzipConfig(text)
	if err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(text))
	data, err := probewire.Encode(&probewire.ConfigSnapshot{CIID: ciID, ManagementIP: e.ip, CollectedAt: at,
		Source: probewire.ConfigSourceSSH, Trigger: probewire.ConfigTriggerSchedule, Platform: "ios",
		SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(text)), MaskedCount: 1, Content: content})
	if err != nil {
		e.t.Fatal(err)
	}
	return data
}

func (e *ingestEnv) send(org, collector string, data []byte) discovery.EdgeReply {
	e.t.Helper()
	return e.h.EdgeConfig(context.Background(), org, collector, data)
}

// version sends the n-th configuration version of the router.
func (e *ingestEnv) version(n int) discovery.EdgeReply {
	e.t.Helper()
	text := "hostname r1\nsnmp-server community <masked> RO\ninterface Gi0/" + string(rune('0'+n)) + "\n"
	return e.send(e.f.OrgA, e.collector, e.message(e.router, text, e.at.Add(time.Duration(n)*time.Minute)))
}

func (e *ingestEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM `+sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *ingestEnv) events() []map[string]any {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox WHERE organization_id = $1 AND type = 'ci.config_changed'
		AND entity_id = $2 ORDER BY sequence`, e.f.OrgA, e.router)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			e.t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// TestConfigIngestFirstVersionNoEvent: the first version of a CI is stored
// (202) without event and without alarm.
func TestConfigIngestFirstVersionNoEvent(t *testing.T) {
	e := newIngestEnv(t)
	r := e.version(1)
	if r.Status != http.StatusAccepted || r.Outcome != configsnap.OutcomeStored || r.SnapshotID == "" {
		t.Fatalf("first version: %+v, want 202 stored with snapshot_id", r)
	}
	if n := len(e.events()); n != 0 {
		t.Errorf("%d ci.config_changed for the first version, want none", n)
	}
	if n := e.count(`alarm WHERE organization_id = $1 AND source_kind = 'config'`, e.f.OrgA); n != 0 {
		t.Errorf("%d config alarms for the first version, want none", n)
	}
}

// TestConfigIngestSecondVersionEmitsEvent: a second, different version
// writes exactly one ci.config_changed with previous_sha256 and both
// snapshot ids; its id is the new configuration version (the snapshot), as
// api/events.schema.json defines it.
func TestConfigIngestSecondVersionEmitsEvent(t *testing.T) {
	e := newIngestEnv(t)
	first := e.version(1)
	second := e.version(2)
	if second.Status != http.StatusAccepted {
		t.Fatalf("second version: %+v", second)
	}
	evs := e.events()
	if len(evs) != 1 {
		t.Fatalf("%d ci.config_changed, want one", len(evs))
	}
	d := evs[0]
	prev := e.scalar(`SELECT sha256 FROM config_snapshot WHERE id = $1`, first.SnapshotID)
	cur := e.scalar(`SELECT sha256 FROM config_snapshot WHERE id = $1`, second.SnapshotID)
	if d["id"] != second.SnapshotID || d["ci_id"] != e.router || d["snapshot_id"] != second.SnapshotID || d["previous_snapshot_id"] != first.SnapshotID ||
		d["sha256"] != cur || d["previous_sha256"] != prev || d["hash"] != cur || d["client_id"] != e.f.Client1 ||
		d["source"] != "ssh" || d["trigger"] != "schedule" || d["platform"] != "ios" || d["masked_count"] != 1.0 || d["collected_at"] == nil {
		t.Errorf("ci.config_changed data %v", d)
	}
}

// TestConfigIngestRaisesConfigAlarm: the second version opens the alarm
// running-config of the CI (source config, severity info); a third
// version counts a second occurrence.
func TestConfigIngestRaisesConfigAlarm(t *testing.T) {
	e := newIngestEnv(t)
	e.version(1)
	second := e.version(2)
	var kind, sev, state, details string
	var occurrences int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT source_kind, severity, state, occurrence_count, details::text FROM alarm
		WHERE organization_id = $1 AND source_kind = 'config' AND source_id = $2`, e.f.OrgA, e.router).
		Scan(&kind, &sev, &state, &occurrences, &details); err != nil {
		t.Fatal(err)
	}
	if kind != "config" || sev != "info" || state != "open" || occurrences != 1 || !strings.Contains(details, second.SnapshotID) {
		t.Errorf("config alarm %s/%s/%s/%d %s", kind, sev, state, occurrences, details)
	}
	e.version(3)
	if n := e.scalar(`SELECT occurrence_count::text FROM alarm WHERE organization_id = $1 AND source_kind = 'config' AND source_id = $2`,
		e.f.OrgA, e.router); n != "2" {
		t.Errorf("occurrence_count %s after a third version, want 2", n)
	}
}

// TestConfigIngestSameHashUnchanged: the same configuration again is 200
// unchanged with the stored version; no row, no event.
func TestConfigIngestSameHashUnchanged(t *testing.T) {
	e := newIngestEnv(t)
	first := e.version(1)
	text := "hostname r1\nsnmp-server community <masked> RO\ninterface Gi0/1\n"
	r := e.send(e.f.OrgA, e.collector, e.message(e.router, text, e.at.Add(time.Hour)))
	if r.Status != http.StatusOK || r.Outcome != configsnap.OutcomeUnchanged || r.SnapshotID != first.SnapshotID {
		t.Fatalf("same hash: %+v, want 200 unchanged with the stored version", r)
	}
	if n := e.count(`config_snapshot WHERE ci_id = $1`, e.router); n != 1 {
		t.Errorf("%d versions, want 1", n)
	}
}

// TestConfigIngestRedeliveryIdempotent: the same message twice writes one
// row and one event.
func TestConfigIngestRedeliveryIdempotent(t *testing.T) {
	e := newIngestEnv(t)
	e.version(1)
	msg := e.message(e.router, "hostname r1-changed\n", e.at.Add(10*time.Minute))
	for range 2 {
		if r := e.send(e.f.OrgA, e.collector, msg); r.Status/100 != 2 {
			t.Fatalf("delivery: %+v", r)
		}
	}
	if n := e.count(`config_snapshot WHERE ci_id = $1`, e.router); n != 2 {
		t.Errorf("%d versions, want 2", n)
	}
	if n := len(e.events()); n != 1 {
		t.Errorf("%d events, want 1", n)
	}
}

// TestConfigIngestForeignCI404: a CI of organization B, an unknown CI, a
// CI with another management address and a CI of another client than the
// one the collector is bound to are 404 without a row.
func TestConfigIngestForeignCI404(t *testing.T) {
	e := newIngestEnv(t)
	foreign := e.f.CI(t, e.f.OrgB, "", "wp1054-foreign")
	e.exec(`UPDATE ci SET management_ip = $2 WHERE id = $1`, foreign, e.ip)
	other := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1054-other")
	e.exec(`UPDATE ci SET management_ip = '10.54.0.99' WHERE id = $1`, other)
	for name, id := range map[string]string{"foreign": foreign, "unknown": e.f.ID(), "other address": other} {
		if r := e.send(e.f.OrgA, e.collector, e.message(id, "hostname x\n", e.at)); r.Status != http.StatusNotFound {
			t.Errorf("%s CI: %+v, want 404", name, r)
		}
	}
	// Outside the scope of the collector: a collector bound to client 1
	// sends for a CI of client 2 with the same management address.
	bound := e.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'wp1054-client1', 'active')
		RETURNING id::text`, e.f.OrgA, e.f.Client1)
	outside := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp1054-client2")
	e.exec(`UPDATE ci SET management_ip = $2 WHERE id = $1`, outside, e.ip)
	if r := e.send(e.f.OrgA, bound, e.message(outside, "hostname x\n", e.at)); r.Status != http.StatusNotFound {
		t.Errorf("CI outside the collector's scope: %+v, want 404", r)
	}
	if n := e.count(`config_snapshot WHERE organization_id = ANY ($1::uuid[])`, []string{e.f.OrgA, e.f.OrgB}); n != 0 {
		t.Errorf("%d versions stored, want none", n)
	}
}

// TestConfigIngestWithoutMonitoringNetwork403: organization B without the
// feature gets 403 with the problem type of the entitlement.
func TestConfigIngestWithoutMonitoringNetwork403(t *testing.T) {
	e := newIngestEnv(t)
	ciB := e.f.CI(t, e.f.OrgB, "", "wp1054-b")
	r := e.send(e.f.OrgB, e.collectorB, e.message(ciB, "hostname b\n", e.at))
	want := (&entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureMonitoringNetwork}).ProblemType()
	if r.Status != http.StatusForbidden || r.Type != want {
		t.Errorf("without monitoring_network: %+v, want 403 %s", r, want)
	}
}

// TestConfigIngestHashMismatch400: a SHA-256 that is not the content's, a
// size that does not match, a configuration larger than 2 MiB (matching
// hash and size) and an unknown major schema version are 400 without a
// row.
func TestConfigIngestHashMismatch400(t *testing.T) {
	e := newIngestEnv(t)
	var snap probewire.ConfigSnapshot
	if err := json.Unmarshal(e.message(e.router, "hostname r1\n", e.at), &snap); err != nil {
		t.Fatal(err)
	}
	bad := snap
	bad.SHA256 = strings.Repeat("ab", 32)
	size := snap
	size.SizeBytes++
	version := snap
	version.SchemaVersion = "9.0"
	// More than 2 MiB with a matching hash and size.
	bigText := strings.Repeat("interface Gi0/1\n description uplink\n", 2<<20/36+1024)
	var big probewire.ConfigSnapshot
	if err := json.Unmarshal(e.message(e.router, bigText, e.at), &big); err != nil {
		t.Fatal(err)
	}
	if big.SizeBytes <= 2<<20 {
		t.Fatalf("test content of %d bytes, want more than 2 MiB", big.SizeBytes)
	}
	for name, m := range map[string]*probewire.ConfigSnapshot{"hash": &bad, "size": &size, "schema version": &version, "too large": &big} {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if r := e.send(e.f.OrgA, e.collector, data); r.Status != http.StatusBadRequest {
			t.Errorf("%s: %+v, want 400", name, r)
		}
	}
	if n := e.count(`config_snapshot WHERE ci_id = $1`, e.router); n != 0 {
		t.Errorf("%d versions stored, want none", n)
	}
}

// TestConfigIngestEventCarriesNoContent: neither the configuration text nor
// a value of it reaches the event or the alarm.
func TestConfigIngestEventCarriesNoContent(t *testing.T) {
	e := newIngestEnv(t)
	e.send(e.f.OrgA, e.collector, e.message(e.router, "hostname r1\n", e.at))
	e.send(e.f.OrgA, e.collector, e.message(e.router, "hostname r1\nenable secret "+secret+"\n", e.at.Add(time.Minute)))
	raw := e.scalar(`SELECT data::text FROM event_outbox WHERE organization_id = $1 AND type = 'ci.config_changed' AND entity_id = $2`,
		e.f.OrgA, e.router)
	alarmRaw := e.scalar(`SELECT summary || details::text FROM alarm WHERE organization_id = $1 AND source_kind = 'config'`, e.f.OrgA)
	for _, s := range []string{raw, alarmRaw} {
		if strings.Contains(s, secret) || strings.Contains(s, "hostname") || strings.Contains(s, "content") {
			t.Errorf("content in %s", s)
		}
	}
}
