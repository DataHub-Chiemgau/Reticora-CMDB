package probe_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Retest (WP-976, MON-02, MON-03):
//
//	cd backend && go test -count=1 -p 1 -run 'TestProbeValues' ./internal/probe/

// bridgeEnv is organization A with an icmp probe of a CI without
// management IP, assigned to a collector.
type bridgeEnv struct {
	t                    *testing.T
	f                    *scopetest.Fixture
	ci, probeID, collect string
	epoch                int64
	store                *probe.PGIngestStore
}

func newBridgeEnv(t *testing.T) *bridgeEnv {
	t.Helper()
	f := scopetest.Seed(t, "93")
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(context.Background(), `DELETE FROM metric_sample WHERE organization_id = $1`, f.OrgA)
	})
	e := &bridgeEnv{t: t, f: f, store: probe.NewPGIngestStore(f.App)}
	group := e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp976') RETURNING id::text`, f.OrgA)
	e.collect = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp976', 'active') RETURNING id::text`, f.OrgA)
	e.exec(`UPDATE collector_group_member SET group_id = $1, last_alive_at = now() WHERE collector_id = $2`, group, e.collect)
	e.ci = f.CI(t, f.OrgA, f.Client1, "wp976-ci")
	e.probeID = e.scalar(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', '10.76.0.1')
		RETURNING id::text`, f.OrgA, e.ci, group)
	e.epoch = 3
	e.exec(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch) VALUES ($1, $2, $3, $4)`, e.probeID, f.OrgA, e.collect, e.epoch)
	return e
}

func (e *bridgeEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *bridgeEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *bridgeEnv) write(seq uint64, at time.Time) (probe.IngestOutcome, error) {
	return e.store.WriteResults(context.Background(), e.f.OrgA, []probe.ResultsMessage{{CollectorID: e.collect, Results: []probewire.Result{{
		ProbeID: e.probeID, Epoch: e.epoch, Seq: seq, ObservedAt: at, Status: probewire.StatusOK,
		Values: map[string]float64{"rtt_min_ms": 1.25, "jitter_ms": 0.5}, Labels: map[string]string{"path": "a"},
	}}}})
}

func (e *bridgeEnv) samples() string {
	return e.scalar(`SELECT count(*)::text FROM metric_sample WHERE organization_id = $1 AND ci_id = $2`, e.f.OrgA, e.ci)
}

func TestProbeValuesAppearInCIMetrics(t *testing.T) {
	e := newBridgeEnv(t)
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if out, err := e.write(1, at); err != nil || out.Written != 1 || out.Samples != 2 {
		t.Fatalf("write: %+v %v, want 1 result and 2 samples", out, err)
	}
	router := chi.NewRouter()
	monitoring.NewHandler(monitoring.NewPGMetricStore(e.f.App)).RegisterRoutes(router)
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis/"+e.ci+"/metrics?name=rtt_min_ms", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET metrics: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Series []monitoring.CISeries `json:"series"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Series) != 1 || resp.Series[0].Name != "rtt_min_ms" || len(resp.Series[0].Points) != 1 {
		t.Fatalf("series %+v, want rtt_min_ms with one point", resp.Series)
	}
	var labels string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT labels::text FROM metric_sample WHERE ci_id = $1 AND metric_name = 'rtt_min_ms'`,
		e.ci).Scan(&labels); err != nil {
		t.Fatal(err)
	}
	var l map[string]string
	_ = json.Unmarshal([]byte(labels), &l)
	if l["kind"] != "probe" || l["probe_id"] != e.probeID || l["path"] != "a" {
		t.Errorf("labels %s, want kind=probe, probe_id and the result labels", labels)
	}
}

func TestProbeValuesNotDuplicatedOnRedelivery(t *testing.T) {
	e := newBridgeEnv(t)
	at := time.Now().UTC().Add(-time.Minute)
	for range 2 {
		if _, err := e.write(7, at); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.samples(); n != "2" {
		t.Errorf("%s samples after a repeated delivery, want 2", n)
	}
}

// TestProbeValuesNotDuplicatedWithinOneCall: the same batch twice in one
// fetch (a repeated send of the collector) writes one result and one set
// of samples; a second result with the same (probe, epoch, seq) but another
// time (a sequence restart of the collector) is a result of its own with
// its own samples (probe_result_key, AK4).
func TestProbeValuesNotDuplicatedWithinOneCall(t *testing.T) {
	e := newBridgeEnv(t)
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond).Add(123456 * time.Nanosecond)
	msg := probe.ResultsMessage{CollectorID: e.collect, Results: []probewire.Result{{
		ProbeID: e.probeID, Epoch: e.epoch, Seq: 11, ObservedAt: at, Status: probewire.StatusOK,
		Values: map[string]float64{"rtt_min_ms": 1.25, "jitter_ms": 0.5},
	}}}
	out, err := e.store.WriteResults(context.Background(), e.f.OrgA, []probe.ResultsMessage{msg, msg})
	if err != nil {
		t.Fatal(err)
	}
	if out.Written != 1 || out.Samples != 2 {
		t.Errorf("twice in one call: %+v, want 1 result and 2 samples", out)
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_result WHERE probe_id = $1`, e.probeID); n != "1" {
		t.Errorf("%s results, want 1", n)
	}
	if n := e.samples(); n != "2" {
		t.Errorf("%s samples, want 2", n)
	}
	// Same seq after a restart of the collector, another time.
	restarted := msg
	restarted.Results = []probewire.Result{msg.Results[0]}
	restarted.Results[0].ObservedAt = at.Add(time.Second)
	out, err = e.store.WriteResults(context.Background(), e.f.OrgA, []probe.ResultsMessage{msg, restarted})
	if err != nil {
		t.Fatal(err)
	}
	if out.Written != 1 || out.Samples != 2 {
		t.Errorf("known result plus restarted seq: %+v, want 1 result and 2 samples", out)
	}
	if n := e.samples(); n != "4" {
		t.Errorf("%s samples, want 4", n)
	}
}

func TestProbeValuesRollBackWithResults(t *testing.T) {
	e := newBridgeEnv(t)
	// A failing sample insert rolls back the results of the batch too.
	e.exec(`CREATE FUNCTION wp976_refuse() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'wp976 refused'; END $$`)
	e.exec(`CREATE TRIGGER wp976_refuse BEFORE INSERT ON metric_sample FOR EACH ROW
		WHEN (NEW.metric_name = 'jitter_ms') EXECUTE FUNCTION wp976_refuse()`)
	t.Cleanup(func() {
		_, _ = e.f.Admin.Exec(context.Background(), `DROP TRIGGER IF EXISTS wp976_refuse ON metric_sample`)
		_, _ = e.f.Admin.Exec(context.Background(), `DROP FUNCTION IF EXISTS wp976_refuse()`)
	})
	if _, err := e.write(9, time.Now().UTC()); err == nil {
		t.Fatal("the write succeeded despite the refused sample")
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_result WHERE probe_id = $1`, e.probeID); n != "0" {
		t.Errorf("%s results kept after the rollback, want 0", n)
	}
	if n := e.samples(); n != "0" {
		t.Errorf("%s samples kept after the rollback, want 0", n)
	}
}
