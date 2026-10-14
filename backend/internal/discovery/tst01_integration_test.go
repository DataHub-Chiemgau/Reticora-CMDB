package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestTST01Reconciliation is the reconciliation case matrix of TST-01
// (WP-202) against PostgreSQL: every case is a named subtest that sends
// device records through POST /api/v1/discovery/ingest and checks the
// resulting CIs, field provenance and review items. The cases cover identity
// resolution with its blocklists (placeholder serials, clone UUIDs, locally
// administered and virtual router MACs), observed_at ordering and source
// ranks, the override conflict, resurrection of a deleted CI, client binding
// and the review types the ingest opens. Each case works on its own devices,
// so the cases are independent of their order.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTST01Reconciliation(t *testing.T) {
	f := scopetest.Seed(t, "bf")
	bg := context.Background()
	overrides := override.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)).
		WithProvenance(provenance{repo: overrides}).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	ingest := func(t *testing.T, recs ...plugins.DeviceRecord) discovery.BulkIngestResponse {
		t.Helper()
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: recs})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
		var resp discovery.BulkIngestResponse
		if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	ip := 0
	// record is an SNMP observation of a server an hour ago with its own
	// management IP, so only the identity under test can match.
	record := func(name string, mutate func(*plugins.DeviceRecord)) plugins.DeviceRecord {
		ip++
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: name,
			IP: fmt.Sprintf("198.18.%d.%d", ip/250, ip%250+1)}, time.Now().Add(-time.Hour))
		if mutate != nil {
			mutate(&rec)
		}
		return rec
	}
	count := func(t *testing.T, where string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND `+where,
			append([]any{f.OrgA}, args...)...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	reviewsOf := func(t *testing.T, kind, like string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = $2
			AND status = 'open' AND (payload::text LIKE $3 OR candidate_ci_ids::text LIKE $3)`,
			f.OrgA, kind, "%"+like+"%").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	field := func(t *testing.T, serial, column string) string {
		t.Helper()
		var v string
		if err := f.Admin.QueryRow(bg, `SELECT coalesce(`+column+`, '') FROM ci WHERE organization_id = $1 AND serial_number = $2
			AND deleted_at IS NULL`, f.OrgA, serial).Scan(&v); err != nil {
			t.Fatalf("%s of %s: %v", column, serial, err)
		}
		return v
	}
	withSerial := func(serial string) func(*plugins.DeviceRecord) {
		return func(r *plugins.DeviceRecord) { r.Identity.SerialNumber = serial }
	}
	at := func(source, serial, model string, observed time.Time) plugins.DeviceRecord {
		rec := record("rank-"+serial, withSerial(serial))
		rec.Source, rec.ObservedAt, rec.Classification.Model = source, observed, model
		return rec
	}

	t.Run("01 new device creates a CI", func(t *testing.T) {
		if resp := ingest(t, record("tst-new", withSerial("TST-01"))); resp.Created != 1 {
			t.Fatalf("%+v", resp)
		}
		if count(t, `serial_number = 'TST-01'`) != 1 {
			t.Error("CI missing")
		}
	})
	t.Run("02 serial number matches the existing CI", func(t *testing.T) {
		ingest(t, record("tst-serial-a", withSerial("TST-02")))
		if resp := ingest(t, record("tst-serial-b", withSerial("TST-02"))); resp.Updated != 1 || resp.Created != 0 {
			t.Fatalf("%+v", resp)
		}
		if count(t, `serial_number = 'TST-02'`) != 1 {
			t.Error("duplicate CI")
		}
	})
	for i, placeholder := range []string{"To Be Filled By O.E.M.", "00000000"} {
		t.Run(fmt.Sprintf("%02d placeholder serial %q is blocklisted", 3+i, placeholder), func(t *testing.T) {
			name := fmt.Sprintf("tst-placeholder-%d", i)
			ingest(t, record(name+"-a", withSerial(placeholder)))
			if resp := ingest(t, record(name+"-b", withSerial(placeholder))); resp.Created != 1 {
				t.Fatalf("second device with the placeholder serial: %+v", resp)
			}
			if count(t, `name LIKE $2`, name+"-%") != 2 {
				t.Error("devices merged over a placeholder serial")
			}
		})
	}
	t.Run("05 hardware UUID matches", func(t *testing.T) {
		uuid := func(r *plugins.DeviceRecord) { r.Identity.HardwareUUID = "4c4c4544-0054-5354-8030-b4c04f545305" }
		ingest(t, record("tst-uuid-a", uuid))
		if resp := ingest(t, record("tst-uuid-b", uuid)); resp.Updated != 1 || resp.Created != 0 {
			t.Fatalf("%+v", resp)
		}
	})
	t.Run("06 clone hardware UUID is blocklisted", func(t *testing.T) {
		clone := func(r *plugins.DeviceRecord) { r.Identity.HardwareUUID = "03000200-0400-0500-0006-000700080009" }
		ingest(t, record("tst-clone-a", clone))
		if resp := ingest(t, record("tst-clone-b", clone)); resp.Created != 1 {
			t.Fatalf("%+v", resp)
		}
	})
	t.Run("07 MAC address matches", func(t *testing.T) {
		mac := func(r *plugins.DeviceRecord) { r.Identity.PrimaryMAC = "00:1b:21:54:53:07" }
		ingest(t, record("tst-mac-a", mac))
		if resp := ingest(t, record("tst-mac-b", mac)); resp.Updated != 1 || resp.Created != 0 {
			t.Fatalf("%+v", resp)
		}
	})
	for i, mac := range []string{"02:00:00:54:53:08", "00:00:5e:00:01:09"} {
		t.Run(fmt.Sprintf("%02d MAC %s (locally administered / VRRP) is blocklisted", 8+i, mac), func(t *testing.T) {
			set := func(r *plugins.DeviceRecord) { r.Identity.PrimaryMAC = mac }
			ingest(t, record(fmt.Sprintf("tst-vmac-%d-a", i), set))
			if resp := ingest(t, record(fmt.Sprintf("tst-vmac-%d-b", i), set)); resp.Created != 1 {
				t.Fatalf("%+v", resp)
			}
		})
	}
	t.Run("10 a MAC on several CIs is ambiguous", func(t *testing.T) {
		a, b := f.CI(t, f.OrgA, "", "tst-amb-a"), f.CI(t, f.OrgA, "", "tst-amb-b")
		// primary_mac is unique: the second CI carries the MAC in its
		// fingerprint, as a further interface reported by discovery.
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = '00:1b:21:54:53:10' WHERE id = $1`, a); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET attributes = coalesce(attributes, '{}'::jsonb) ||
			'{"fingerprint": {"mac_addresses": ["00:1b:21:54:53:10"]}}'::jsonb WHERE id = $1`, b); err != nil {
			t.Fatal(err)
		}
		resp := ingest(t, record("tst-amb-new", func(r *plugins.DeviceRecord) { r.Identity.PrimaryMAC = "00:1b:21:54:53:10" }))
		if resp.Updated != 0 || resp.ReviewItems != 1 {
			t.Fatalf("%+v", resp)
		}
		if reviewsOf(t, "ambiguous_identity", a) != 1 {
			t.Error("no ambiguous_identity review naming the candidates")
		}
	})
	t.Run("11 hostname never merges: new CI and probable_duplicate", func(t *testing.T) {
		host := func(r *plugins.DeviceRecord) { r.Identity.Hostname = "tst-host-11" }
		ingest(t, record("tst-host-a", host))
		if resp := ingest(t, record("tst-host-b", host)); resp.Created != 1 || resp.ReviewItems != 1 {
			t.Fatalf("%+v", resp)
		}
		if reviewsOf(t, "probable_duplicate", "tst-host-11") != 1 {
			t.Error("no probable_duplicate review")
		}
	})
	t0 := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	t.Run("12 a higher-ranked source replaces the value", func(t *testing.T) {
		ingest(t, at("snmp", "TST-12", "m-snmp", t0))
		ingest(t, at("redfish", "TST-12", "m-redfish", t0))
		if got := field(t, "TST-12", "model"); got != "m-redfish" {
			t.Errorf("model %q", got)
		}
	})
	t.Run("13 a lower-ranked source does not replace the value", func(t *testing.T) {
		ingest(t, at("redfish", "TST-13", "m-redfish", t0))
		ingest(t, at("snmp", "TST-13", "m-snmp", t0.Add(time.Hour)))
		if got := field(t, "TST-13", "model"); got != "m-redfish" {
			t.Errorf("model %q", got)
		}
	})
	t.Run("14 same rank: the newer observation wins", func(t *testing.T) {
		ingest(t, at("snmp", "TST-14", "m-old", t0))
		ingest(t, at("snmp", "TST-14", "m-new", t0.Add(time.Minute)))
		if got := field(t, "TST-14", "model"); got != "m-new" {
			t.Errorf("model %q", got)
		}
	})
	t.Run("15 same rank: an older observation arriving later is refused", func(t *testing.T) {
		ingest(t, at("snmp", "TST-15", "m-current", t0))
		ingest(t, at("snmp", "TST-15", "m-stale", t0.Add(-time.Minute)))
		if got := field(t, "TST-15", "model"); got != "m-current" {
			t.Errorf("model %q", got)
		}
	})
	t.Run("16 a record from the future is rejected", func(t *testing.T) {
		resp := ingest(t, at("snmp", "TST-16", "m-future", time.Now().UTC().Add(time.Hour)))
		if resp.RejectedFuture != 1 || resp.Created != 0 || count(t, `serial_number = 'TST-16'`) != 0 {
			t.Fatalf("%+v", resp)
		}
	})
	t.Run("17 a differing observation of an overridden field opens override_conflict", func(t *testing.T) {
		ingest(t, at("snmp", "TST-17", "m-observed", t0))
		id := field(t, "TST-17", "id::text")
		if _, err := overrides.SetOverride(ctx, f.OrgA, id, "model", "m-manual", f.User, "Typenschild", true); err != nil {
			t.Fatal(err)
		}
		ingest(t, at("snmp", "TST-17", "m-observed-2", t0.Add(time.Minute)))
		if got := field(t, "TST-17", "model"); got != "m-manual" {
			t.Errorf("override lost: model %q", got)
		}
		if reviewsOf(t, "override_conflict", id) != 1 {
			t.Error("no override_conflict review")
		}
	})
	t.Run("18 an observation equal to the override opens no conflict", func(t *testing.T) {
		ingest(t, at("snmp", "TST-18", "m-a", t0))
		id := field(t, "TST-18", "id::text")
		if _, err := overrides.SetOverride(ctx, f.OrgA, id, "model", "m-same", f.User, "abgeglichen", true); err != nil {
			t.Fatal(err)
		}
		ingest(t, at("snmp", "TST-18", "m-same", t0.Add(time.Minute)))
		if reviewsOf(t, "override_conflict", id) != 0 {
			t.Error("override_conflict for an equal value")
		}
	})
	t.Run("19 a deleted CI seen again is held for resurrected_device", func(t *testing.T) {
		ingest(t, record("tst-resurrect", withSerial("TST-19")))
		id := field(t, "TST-19", "id::text")
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if resp := ingest(t, record("tst-resurrect-again", withSerial("TST-19"))); resp.Created != 0 {
			t.Fatalf("a new CI instead of the review: %+v", resp)
		}
		if reviewsOf(t, "resurrected_device", id) != 1 {
			t.Error("no resurrected_device review")
		}
	})
	t.Run("20 the client binding keeps another client's CI apart", func(t *testing.T) {
		other := f.CI(t, f.OrgA, f.Client2, "tst-client-2")
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET serial_number = 'TST-20' WHERE id = $1`, other); err != nil {
			t.Fatal(err)
		}
		rec := record("tst-client-1", withSerial("TST-20"))
		rec.ClientID = f.Client1
		if resp := ingest(t, rec); resp.Created != 1 || resp.Updated != 0 {
			t.Fatalf("%+v", resp)
		}
		if count(t, `id = $2 AND name = 'tst-client-2'`, other) != 1 {
			t.Error("client-2 CI changed")
		}
	})
	t.Run("21 a device of an unknown type is reviewed as unclassified", func(t *testing.T) {
		rec := record("tst-unclassified", withSerial("TST-21"))
		rec.Classification.CIType = "tst-unknown-type"
		if resp := ingest(t, rec); resp.ReviewItems != 1 {
			t.Fatalf("%+v", resp)
		}
		if reviewsOf(t, "unclassified_device", "TST-21") != 1 {
			t.Error("no unclassified_device review")
		}
	})
}
