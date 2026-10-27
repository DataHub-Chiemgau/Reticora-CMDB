// Typed CI columns in discovery on PostgreSQL (WP-501, MET-11, CI-03,
// finding F-143).
//
//   - TestAuditDefectDiscoveryTypedValuesValidated (moved from
//     typedvalues_auditdefect_test.go): discovery checks the typed CI
//     columns with their MET-11 data type, as a manual write does
//     (ci.InvalidTypedColumns, InvalidObserved and ObservedRules take them
//     as implicit definitions). A multi-line hostname, a hardware_uuid that
//     is no UUID, a malformed MAC or IP is dropped (column NULL) and counted
//     in invalid_values; the rest of the record is stored. Neither ingest
//     contract refuses or truncates the batch: POST /ingest/bulk with items
//     and POST /discovery/ingest with DeviceRecords answer 202, every device
//     is stored, no record is rejected. Before WP-501 the multi-line
//     hostname was stored, a malformed uuid or IP failed its record and the
//     DeviceRecord contract answered 422 for the whole batch.
//
// The queue path (dedicated nats-server) is TestIngestQueueDropsInvalidTypedValue
// in ingest_poison_integration_test.go; the manual write keeps refusing the
// same values (ci/validation_test.go TestObservedTypedValuesFollowManualWrites).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -run 'TestAuditDefectDiscoveryTypedValues' ./internal/discovery/
package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// typedRow is the stored state of one discovered CI.
type typedRow struct {
	found                           bool
	hostname, uuid, mac, ip, serial string
}

func TestAuditDefectDiscoveryTypedValuesValidated(t *testing.T) {
	f := scopetest.Seed(t, "dd")
	bg := context.Background()
	cis := ci.NewPGRepository(f.App)
	svc := ci.NewService(cis).WithFieldResolver(citype.NewPGRepository(f.App))
	mux := chi.NewRouter()
	// Wiring as server/router.go: the discovery handler validates observed
	// values with the CI service (WithObservedValidator).
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).WithObservedValidator(svc).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	post := func(path string, body []byte) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
		req.Header.Set("X-Collector-ID", "audit-dd-collector")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp
	}
	row := func(name string) typedRow {
		var r typedRow
		err := f.Admin.QueryRow(bg, `SELECT COALESCE(hostname, ''), COALESCE(hardware_uuid::text, ''), COALESCE(primary_mac::text, ''),
			COALESCE(host(management_ip), ''), COALESCE(serial_number, '') FROM ci WHERE organization_id = $1 AND name = $2`, f.OrgA, name).
			Scan(&r.hostname, &r.uuid, &r.mac, &r.ip, &r.serial)
		r.found = err == nil
		return r
	}
	type check struct {
		name  string
		valid func(typedRow) bool
		want  string
	}
	verify := func(t *testing.T, code int, resp map[string]any, invalid int, checks []check) {
		t.Helper()
		if code != http.StatusAccepted {
			t.Errorf("ingest = %d %v, want 202: one malformed field must not refuse or truncate the batch", code, resp)
		}
		for _, c := range checks {
			r := row(c.name)
			if !r.found {
				t.Errorf("device %s was not stored (records of the batch lost)", c.name)
				continue
			}
			if !c.valid(r) {
				t.Errorf("device %s stored %+v: %s", c.name, r, c.want)
			}
		}
		if got, _ := resp["invalid_values"].(float64); int(got) != invalid {
			t.Errorf("invalid_values = %v, want %d (the dropped values)", resp["invalid_values"], invalid)
		}
		if got, _ := resp["rejected"].(float64); got != 0 {
			t.Errorf("rejected = %v, want 0: a malformed value fails no record (%v)", resp["rejected"], resp)
		}
	}
	singleLine := func(r typedRow) bool { return !strings.ContainsAny(r.hostname, "\r\n") }

	// A batch with only a line-break defect shows what is stored; a batch
	// with malformed typed values shows that the batch is neither refused
	// nor truncated.
	t.Run("legacy items: multi-line hostname is not stored", func(t *testing.T) {
		code, resp := post("/api/v1/ingest/bulk", []byte(`{"collector_id":"audit-dd-collector","items":[
			{"ci_type_name":"server","name":"audit-dd-i-valid","serial_number":"SN-DD-I1","hostname":"valid-host"},
			{"ci_type_name":"server","name":"audit-dd-i-host","serial_number":"SN-DD-I2","hostname":"line1\nline2"}]}`))
		verify(t, code, resp, 1, []check{
			{"audit-dd-i-valid", func(r typedRow) bool { return r.hostname == "valid-host" }, "valid hostname stored"},
			{"audit-dd-i-host", func(r typedRow) bool { return singleLine(r) && r.serial == "SN-DD-I2" }, "multi-line hostname must not be stored (POST /cis answers 422)"},
		})
	})

	t.Run("legacy items: malformed uuid/mac/ip neither refuse nor truncate the batch", func(t *testing.T) {
		code, resp := post("/api/v1/ingest/bulk", []byte(`{"collector_id":"audit-dd-collector","items":[
			{"ci_type_name":"server","name":"audit-dd-t-valid","serial_number":"SN-DD-T1","hostname":"valid-host-t"},
			{"ci_type_name":"server","name":"audit-dd-t-uuid","serial_number":"SN-DD-T3","hardware_uuid":"not-a-uuid"},
			{"ci_type_name":"server","name":"audit-dd-t-mac","serial_number":"SN-DD-T4","primary_mac":"zz:zz"},
			{"ci_type_name":"server","name":"audit-dd-t-ip","serial_number":"SN-DD-T5","management_ip":"999.1.1.1"},
			{"ci_type_name":"server","name":"audit-dd-t-last","serial_number":"SN-DD-T6"}]}`))
		verify(t, code, resp, 3, []check{
			{"audit-dd-t-valid", func(r typedRow) bool { return r.hostname == "valid-host-t" }, "valid record stored"},
			{"audit-dd-t-uuid", func(r typedRow) bool { return r.uuid == "" && r.serial == "SN-DD-T3" }, "invalid hardware_uuid dropped, other fields kept"},
			{"audit-dd-t-mac", func(r typedRow) bool { return r.mac == "" && r.serial == "SN-DD-T4" }, "invalid primary_mac dropped, other fields kept"},
			{"audit-dd-t-ip", func(r typedRow) bool { return r.ip == "" && r.serial == "SN-DD-T5" }, "invalid management_ip dropped, other fields kept"},
			{"audit-dd-t-last", func(r typedRow) bool { return r.serial == "SN-DD-T6" }, "record after the malformed ones stored"},
		})
	})

	observed := time.Now().UTC().Add(-time.Minute)
	rec := func(name, ip, serial string) plugins.DeviceRecord {
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: name, IP: ip, Serial: serial}, observed)
	}
	records := func(t *testing.T, rs ...plugins.DeviceRecord) []byte {
		t.Helper()
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: rs})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	t.Run("records: multi-line hostname is not stored", func(t *testing.T) {
		good := rec("audit-dd-r-valid", "192.0.2.161", "SN-DD-R1")
		host := rec("audit-dd-r-host", "192.0.2.162", "SN-DD-R2")
		host.Identity.Hostname = "rline1\nrline2"
		code, resp := post("/api/v1/discovery/ingest", records(t, good, host))
		verify(t, code, resp, 1, []check{
			{"audit-dd-r-valid", func(typedRow) bool { return true }, ""},
			{"audit-dd-r-host", func(r typedRow) bool { return singleLine(r) && r.serial == "SN-DD-R2" }, "multi-line hostname must not be stored (POST /cis answers 422)"},
		})
	})

	t.Run("records: malformed hardware_uuid neither refuses nor truncates the batch", func(t *testing.T) {
		good := rec("audit-dd-s-valid", "192.0.2.171", "SN-DD-S1")
		uuid := rec("audit-dd-s-uuid", "192.0.2.172", "SN-DD-S2")
		uuid.Identity.HardwareUUID = "1234"
		last := rec("audit-dd-s-last", "192.0.2.173", "SN-DD-S3")
		code, resp := post("/api/v1/discovery/ingest", records(t, good, uuid, last))
		verify(t, code, resp, 1, []check{
			{"audit-dd-s-valid", func(typedRow) bool { return true }, ""},
			{"audit-dd-s-uuid", func(r typedRow) bool { return r.uuid == "" && r.serial == "SN-DD-S2" }, "invalid hardware_uuid dropped, other fields kept"},
			{"audit-dd-s-last", func(r typedRow) bool { return r.serial == "SN-DD-S3" }, "record after the malformed one stored"},
		})
	})

	t.Run("records: malformed primary_mac is dropped, a valid one stored", func(t *testing.T) {
		good := rec("audit-dd-m-valid", "192.0.2.181", "SN-DD-M1")
		good.Identity.PrimaryMAC = "00:1a:2b:3c:4d:51"
		bad := rec("audit-dd-m-bad", "192.0.2.182", "SN-DD-M2")
		bad.Identity.PrimaryMAC = "zz:zz"
		bad.Identity.Hostname = "m-host"
		code, resp := post("/api/v1/discovery/ingest", records(t, good, bad))
		verify(t, code, resp, 1, []check{
			{"audit-dd-m-valid", func(r typedRow) bool { return r.mac == "00:1a:2b:3c:4d:51" }, "valid primary_mac stored"},
			{"audit-dd-m-bad", func(r typedRow) bool { return r.mac == "" && r.hostname == "m-host" && r.serial == "SN-DD-M2" },
				"invalid primary_mac dropped, other fields kept"},
		})
	})

	t.Run("records: a malformed value of a known device leaves the stored one", func(t *testing.T) {
		first := rec("audit-dd-u", "192.0.2.191", "SN-DD-U1")
		first.Identity.HardwareUUID = "c0de0501-0000-4000-8000-000000000001"
		first.Identity.Hostname = "u-host-1"
		code, resp := post("/api/v1/discovery/ingest", records(t, first))
		verify(t, code, resp, 0, []check{
			{"audit-dd-u", func(r typedRow) bool { return r.uuid == "c0de0501-0000-4000-8000-000000000001" }, "valid hardware_uuid stored"},
		})
		second := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "audit-dd-u", IP: "192.0.2.191", Serial: "SN-DD-U1",
			Model: "u-model-2"}, time.Now().UTC().Add(-30*time.Second))
		second.Identity.HardwareUUID = "not-a-uuid"
		code, resp = post("/api/v1/discovery/ingest", records(t, second))
		verify(t, code, resp, 1, []check{
			{"audit-dd-u", func(r typedRow) bool { return r.uuid == "c0de0501-0000-4000-8000-000000000001" }, "the malformed hardware_uuid is not written"},
		})
		if updated, _ := resp["updated"].(float64); updated != 1 {
			t.Errorf("updated = %v, want 1 (the known device)", resp["updated"])
		}
		var model string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(model, '') FROM ci WHERE organization_id = $1 AND name = 'audit-dd-u'`, f.OrgA).Scan(&model); err != nil || model != "u-model-2" {
			t.Errorf("model of the known device = %q, %v; want u-model-2: the rest of the record is stored", model, err)
		}
	})
}
