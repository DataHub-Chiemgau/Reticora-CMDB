//go:build auditdefect

package discovery

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed).
//
// WP-085 (REC-01, ING-01; no silent data loss) P2 "One failing record
// dead-letters (queue) or rejects (HTTP) the valid records behind it in the
// same group/lane": processItems stops at the first failing record
// (discovery.go) and consumeBatch decides the fate of every unsettled
// message of the group from that single error: Nak below ingestMaxDeliver,
// Term at the last delivery (consumer.go). The synchronous HTTP path answers
// 403 for the whole request and persists none of the valid records.
// Requirement: each valid record is processed and acknowledged; only the
// failing record is rejected or dead-lettered.
//
// The poison record is a record of client 2 sent by a principal restricted
// to client 1 (a permanent, deterministic refusal); the valid records belong
// to client 1. PostgreSQL repositories with RLS, four lanes.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3PoisonRecordDoesNotTakeValidRecordsDown' ./internal/discovery/

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
)

func TestAuditT3PoisonRecordDoesNotTakeValidRecordsDown(t *testing.T) {
	f := scopetest.Seed(t, "6f")
	bg := context.Background()
	h := NewHandler(NewPGRepository(f.App), ci.NewPGRepository(f.App)).WithIngestLanes(4)
	ti := tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}
	ciRows := func(serial string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, f.OrgA, serial).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ipSeq := 10
	record := func(serial, client string, at time.Time) plugins.DeviceRecord {
		ipSeq++
		r := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, Serial: serial, IP: fmt.Sprintf("192.0.2.%d", ipSeq)}, at)
		r.ClientID = client
		return r
	}

	t.Run("queue path", func(t *testing.T) {
		t0 := time.Now().UTC().Add(-time.Hour)
		poison := record("AUDIT-T3-POISON-Q", f.Client2, t0)
		items := []IngestItem{itemFromRecord(&poison)}
		for i := 0; i < 8; i++ {
			r := record(fmt.Sprintf("AUDIT-T3-GOOD-Q-%d", i), f.Client1, t0.Add(time.Duration(i+1)*time.Second))
			items = append(items, itemFromRecord(&r))
		}
		msgs := newIngestMessages(f.ClientCtx(f.Client1), ti, "audit-t3-col", items)
		poisonLane := msgs[0].lane(4)
		sharing := 0
		for i := 1; i < len(msgs); i++ {
			if msgs[i].lane(4) == poisonLane {
				sharing++
			}
		}
		if sharing == 0 {
			t.Fatalf("precondition: no valid record shares lane %d with the poison record", poisonLane)
		}
		deliver := func(attempt uint64) []*fakeDelivery {
			var fakes []*fakeDelivery
			var deliveries []ingestDelivery
			for i := range msgs {
				raw, err := json.Marshal(&msgs[i])
				if err != nil {
					t.Fatal(err)
				}
				d := &fakeDelivery{data: raw, delivered: attempt}
				fakes = append(fakes, d)
				deliveries = append(deliveries, d)
			}
			consumeBatch(bg, h, deliveries)
			return fakes
		}
		// First delivery: every valid record is processed and acknowledged.
		for i, d := range deliver(1) {
			serial := msgs[i].Item.SerialNumber
			if i == 0 {
				if n := ciRows(serial); n != 0 {
					t.Errorf("out-of-scope poison record created %d CI rows", n)
				}
				continue
			}
			if d.result != "ack" || ciRows(serial) != 1 {
				t.Errorf("valid record %s (lane %d, poison lane %d) at delivery 1: %s with %d CI rows, want ack and 1 row",
					serial, msgs[i].lane(4), poisonLane, d.result, ciRows(serial))
			}
		}
		// Last delivery: no valid record is dead-lettered.
		for i, d := range deliver(ingestMaxDeliver) {
			if i == 0 {
				continue
			}
			serial := msgs[i].Item.SerialNumber
			if d.result == "term" || ciRows(serial) != 1 {
				t.Errorf("valid record %s at the last delivery: %s with %d CI rows, want it acknowledged and persisted (never dead-lettered)",
					serial, d.result, ciRows(serial))
			}
		}
	})

	t.Run("http path", func(t *testing.T) {
		mux := chi.NewRouter()
		NewHandler(NewPGRepository(f.App), ci.NewPGRepository(f.App)).RegisterRoutes(mux)
		post := func(records []plugins.DeviceRecord) *httptest.ResponseRecorder {
			t.Helper()
			body, err := json.Marshal(plugins.DeviceRecordBatch{Records: records})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).
				WithContext(tenant.WithTenant(f.ClientCtx(f.Client1), ti)))
			return w
		}
		at := time.Now().UTC().Add(-time.Minute)
		// Control: the same kind of batch without the poison record is accepted.
		if w := post([]plugins.DeviceRecord{record("AUDIT-T3-CONTROL-H", f.Client1, at)}); w.Code != http.StatusAccepted || ciRows("AUDIT-T3-CONTROL-H") != 1 {
			t.Fatalf("control batch without poison: %d %s", w.Code, w.Body.String())
		}
		records := []plugins.DeviceRecord{record("AUDIT-T3-POISON-H", f.Client2, at)}
		for i := 0; i < 2; i++ {
			records = append(records, record(fmt.Sprintf("AUDIT-T3-GOOD-H-%d", i), f.Client1, at.Add(time.Duration(i+1)*time.Second)))
		}
		w := post(records)
		for _, r := range records[1:] {
			if n := ciRows(r.Identity.SerialNumber); n != 1 {
				t.Errorf("HTTP ingest with one out-of-scope record answered %d; valid record %s has %d CI rows, want 1 (the valid records must not be rejected with it)",
					w.Code, r.Identity.SerialNumber, n)
			}
		}
		if n := ciRows("AUDIT-T3-POISON-H"); n != 0 {
			t.Errorf("out-of-scope poison record created %d CI rows", n)
		}
	})
}
