package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
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

// provenanceRow is a ci_field_value row without its ids and the times of
// its recording.
type provenanceRow struct {
	discovered, source string
	rank               int
	observedAt         string
	override           string
	overridden         bool
	protected          bool
}

// TestMatchedProvenanceRows covers the provenance a re-sighting records for
// the fields it writes or observes under an override (REC-10, REC-12)
// against PostgreSQL: the store with RecordDiscoveredMany (the server's
// adapter) records the same rows as the one without it, string and JSON
// values alike, the override columns untouched, and every write takes the
// time of its transaction as discovered_at.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMatchedProvenanceRows(t *testing.T) {
	f := scopetest.Seed(t, "76")
	bg := context.Background()
	overrides := override.NewPGRepository(f.App)
	stores := []struct {
		name       string
		store      discovery.ProvenanceRecorder
		serial, ip string
	}{
		{"per field", provenance{repo: overrides}, "PROV-SN-1", "192.0.2.161"},
		{"many", manyProvenance{provenance{repo: overrides}}, "PROV-SN-2", "192.0.2.162"},
	}
	t0 := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	t1, t2 := t0.Add(time.Minute), t0.Add(2*time.Minute)
	snapshots := make([][]map[string]provenanceRow, len(stores))
	for s, store := range stores {
		handler := discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)).WithProvenance(store.store)
		mux := chi.NewRouter()
		handler.RegisterRoutes(mux)
		ingest := func(model string, attrs map[string]any, observedAt time.Time) discovery.BulkIngestResponse {
			t.Helper()
			rec := plugins.RecordFromResult("redfish", &plugins.Result{CIType: "server", Name: "prov-" + store.serial, IP: store.ip,
				Serial: store.serial, Model: model, Attributes: attrs}, observedAt)
			body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body))
			req = req.WithContext(tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusAccepted {
				t.Fatalf("%s: ingest %s: %d %s", store.name, model, w.Code, w.Body.String())
			}
			var resp discovery.BulkIngestResponse
			if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			return resp
		}
		var ciID string
		recorded := map[string]time.Time{}
		read := func(step string) map[string]provenanceRow {
			t.Helper()
			rows, err := f.Admin.Query(bg, `SELECT v.field_name, COALESCE(v.discovered_value::text, ''), COALESCE(v.discovered_source, ''),
					COALESCE(v.discovered_rank, 0), v.observed_at, COALESCE(v.override_value::text, ''), v.override_at IS NOT NULL,
					v.protected, v.discovered_at, c.id::text
				FROM ci c JOIN ci_field_value v ON v.ci_id = c.id
				WHERE c.organization_id = $1 AND c.serial_number = $2`, f.OrgA, store.serial)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			out := map[string]provenanceRow{}
			for rows.Next() {
				var field string
				var r provenanceRow
				var observed, at time.Time
				if err = rows.Scan(&field, &r.discovered, &r.source, &r.rank, &observed, &r.override, &r.overridden,
					&r.protected, &at, &ciID); err != nil {
					t.Fatal(err)
				}
				r.observedAt = stamp(observed)
				// A field written again carries the time of its new write.
				if before, ok := recorded[field]; ok && r.observedAt != snapshotObserved(snapshots[s], field) && !at.After(before) {
					t.Errorf("%s/%s: %s discovered_at %s not after %s", store.name, step, field, at, before)
				}
				recorded[field] = at
				out[field] = r
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			snapshots[s] = append(snapshots[s], out)
			return out
		}

		if resp := ingest("m1", map[string]any{"rack_u": 3, "psu": map[string]any{"count": 2, "redundant": true}}, t0); resp.Created != 1 {
			t.Fatalf("%s: first record: %+v", store.name, resp)
		}
		read("created")
		// The model and both attributes change: decided and recorded.
		if resp := ingest("m2", map[string]any{"rack_u": 4, "psu": map[string]any{"count": 2, "redundant": false}}, t1); resp.Updated != 1 {
			t.Fatalf("%s: second record: %+v", store.name, resp)
		}
		got := read("updated")
		want := provenanceRow{discovered: `"m2"`, source: "redfish", rank: 90, observedAt: stamp(t1)}
		if got["model"] != want {
			t.Errorf("%s: model after the update = %+v, want %+v", store.name, got["model"], want)
		}
		want = provenanceRow{discovered: `{"count": 2, "redundant": false}`, source: "redfish", rank: 90, observedAt: stamp(t1)}
		if got["psu"] != want {
			t.Errorf("%s: psu after the update = %+v, want %+v", store.name, got["psu"], want)
		}
		// Under a protected override a differing value is observed: the
		// provenance keeps it, the override stays (REC-12).
		if _, err := overrides.SetOverride(f.OrgCtx(f.OrgA), f.OrgA, ciID, "model", "m-manual", f.User, "label", true); err != nil {
			t.Fatal(err)
		}
		ingest("m3", map[string]any{"rack_u": 4, "psu": map[string]any{"count": 2, "redundant": false}}, t2)
		got = read("under override")
		want = provenanceRow{discovered: `"m3"`, source: "redfish", rank: 90, observedAt: stamp(t2), override: `"m-manual"`, overridden: true, protected: true}
		if got["model"] != want {
			t.Errorf("%s: model under the override = %+v, want %+v", store.name, got["model"], want)
		}
	}
	for step := range snapshots[0] {
		// The two devices differ in their identity only.
		for _, rows := range [][]map[string]provenanceRow{snapshots[0], snapshots[1]} {
			for _, field := range []string{"name", "serial_number", "management_ip"} {
				delete(rows[step], field)
			}
		}
		if !maps.Equal(snapshots[0][step], snapshots[1][step]) {
			t.Errorf("step %d: rows differ:\n%s: %+v\n%s: %+v", step, stores[0].name, snapshots[0][step], stores[1].name, snapshots[1][step])
		}
	}
}

// snapshotObserved is the observed_at of field in the last snapshot.
func snapshotObserved(snapshots []map[string]provenanceRow, field string) string {
	if len(snapshots) == 0 {
		return ""
	}
	return snapshots[len(snapshots)-1][field].observedAt
}

func stamp(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }
