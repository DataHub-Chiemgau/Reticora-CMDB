package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
)

// provenance adapts the override repository to discovery as the server does.
type provenance struct{ repo *override.PGRepository }

func (p provenance) RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string, observedAt time.Time) (*discovery.FieldProvenance, error) {
	fv, err := p.repo.RecordDiscovered(ctx, orgID, ciID, fieldName, value, source, observedAt)
	if err != nil {
		return nil, err
	}
	return &discovery.FieldProvenance{Diverged: fv.Diverged}, nil
}

func (p provenance) Decide(ctx context.Context, w *override.Write) override.Decision {
	return override.DecideAutomatedWrite(ctx, p.repo, w)
}

// TestIngestComparesSourceTimes covers WP-084 (GLO-12, REC-05) against
// PostgreSQL:
//   - the provenance of each field stores source, rank and the record's
//     observed_at, separate from the time it was recorded;
//   - a value of the same rank wins only with a newer observed_at; a record
//     that arrives later but was observed earlier changes nothing;
//   - a higher rank wins, a lower one does not;
//   - a refused source does not become the CI's discovery_source;
//   - records more than five minutes in the future are rejected and counted,
//     records within five minutes are accepted.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestComparesSourceTimes(t *testing.T) {
	f := scopetest.Seed(t, "76")
	bg := context.Background()
	cis := ci.NewPGRepository(f.App)
	handler := discovery.NewHandler(discovery.NewPGRepository(f.App), cis).
		WithProvenance(provenance{repo: override.NewPGRepository(f.App)})
	mux := chi.NewRouter()
	handler.RegisterRoutes(mux)

	t0 := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	ingest := func(source, model string, observedAt time.Time) discovery.BulkIngestResponse {
		t.Helper()
		rec := plugins.RecordFromResult(source, &plugins.Result{CIType: "server", Name: "obs-srv", IP: "192.0.2.76",
			Serial: "OBS-SN-76", Model: model}, observedAt)
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body))
		req = req.WithContext(tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest %s %s: %d %s", source, model, w.Code, w.Body.String())
		}
		var resp discovery.BulkIngestResponse
		if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	type state struct {
		model, discoverySource, provSource string
		rank                               int
		observedAt, recordedAt             time.Time
	}
	read := func() state {
		t.Helper()
		var s state
		if err := f.Admin.QueryRow(bg, `SELECT c.model, c.discovery_source, v.discovered_source, v.discovered_rank, v.observed_at, v.discovered_at
			FROM ci c JOIN ci_field_value v ON v.ci_id = c.id AND v.field_name = 'model'
			WHERE c.organization_id = $1 AND c.serial_number = 'OBS-SN-76'`, f.OrgA).
			Scan(&s.model, &s.discoverySource, &s.provSource, &s.rank, &s.observedAt, &s.recordedAt); err != nil {
			t.Fatal(err)
		}
		return s
	}
	expect := func(step string, want state) {
		t.Helper()
		got := read()
		if got.model != want.model || got.discoverySource != want.discoverySource || got.provSource != want.provSource ||
			got.rank != want.rank || !got.observedAt.Equal(want.observedAt) {
			t.Errorf("%s: %+v, want %+v", step, got, want)
		}
		if got.recordedAt.Sub(got.observedAt) < time.Hour {
			t.Errorf("%s: the provenance stores the arrival time %s as observed_at", step, got.recordedAt)
		}
	}

	if resp := ingest("redfish", "m1", t0); resp.Created != 1 {
		t.Fatalf("first record: %+v", resp)
	}
	expect("created by redfish", state{model: "m1", discoverySource: "redfish", provSource: "redfish", rank: 90, observedAt: t0})

	// Same rank, observed earlier, arrives later: refused, and IPMI does not
	// become the discovery source.
	ingest("ipmi", "m-old", t0.Add(-time.Hour))
	expect("older ipmi", state{model: "m1", discoverySource: "redfish", provSource: "redfish", rank: 90, observedAt: t0})
	// Same rank, same source time: no newer observation, refused.
	ingest("ipmi", "m-same", t0)
	expect("ipmi at the same time", state{model: "m1", discoverySource: "redfish", provSource: "redfish", rank: 90, observedAt: t0})

	// Same rank, newer: wins.
	t1 := t0.Add(time.Minute)
	ingest("ipmi", "m2", t1)
	expect("newer ipmi", state{model: "m2", discoverySource: "ipmi", provSource: "ipmi", rank: 90, observedAt: t1})

	// Lower rank, even newer: refused.
	ingest("snmp", "m-snmp", t1.Add(time.Minute))
	expect("newer snmp", state{model: "m2", discoverySource: "ipmi", provSource: "ipmi", rank: 90, observedAt: t1})

	// More than five minutes in the future: rejected and counted.
	before := discovery.FutureRecordsRejected()
	future := time.Now().UTC().Add(10 * time.Minute)
	if resp := ingest("ipmi", "m-future", future); resp.RejectedFuture != 1 || resp.Received != 1 || resp.Updated != 0 {
		t.Errorf("future record: %+v", resp)
	}
	if discovery.FutureRecordsRejected() != before+1 {
		t.Errorf("future records counted %d, want %d", discovery.FutureRecordsRejected(), before+1)
	}
	expect("future ipmi", state{model: "m2", discoverySource: "ipmi", provSource: "ipmi", rank: 90, observedAt: t1})

	// Within the five minutes the record counts as current.
	soon := time.Now().UTC().Add(4 * time.Minute).Truncate(time.Microsecond)
	if resp := ingest("ipmi", "m3", soon); resp.RejectedFuture != 0 || resp.Updated != 1 {
		t.Errorf("record four minutes ahead: %+v", resp)
	}
	if got := read(); got.model != "m3" || !got.observedAt.Equal(soon) {
		t.Errorf("record four minutes ahead: %+v", got)
	}
}
