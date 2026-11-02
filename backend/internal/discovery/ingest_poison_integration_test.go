package discovery_test

// A malformed typed value on the ingest queue (WP-501, MET-11, CI-03,
// finding F-143): a batch [valid, malformed hardware_uuid, valid] goes the
// production path over a dedicated nats-server with JetStream, HTTP ingest
// publishes, the lane consumer (NATSIngestQueue.Run) processes. The
// malformed hardware_uuid is dropped, the rest of its record is stored, every
// message is acknowledged at its first delivery and none is redelivered or
// dead-lettered. The batch is sent as DeviceRecords and in the deprecated
// items contract, whose malformed value no contract check refuses: it
// reaches the consumer as sent. Before WP-501 the DeviceRecord contract
// refused the whole batch with 422, and the malformed value of an item
// failed its record for good in the candidate query (terminated,
// dead-lettered).
//
// The middle record is a second observation of the first device (same
// serial number): each batch stores exactly two devices, the first with the
// model of the middle record and without its hardware_uuid.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  TEST_NATS_SERVER_BIN=<nats-server 2.10+> go test -count=1 -v -run TestIngestQueueDropsInvalidTypedValue ./internal/discovery/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

func TestIngestQueueDropsInvalidTypedValue(t *testing.T) {
	f := scopetest.Seed(t, "01")
	srv := &outageNATS{t: t, bin: edgeNATSServerBin(t), port: auditFreePort(t), store: t.TempDir()}
	srv.start()
	t.Cleanup(srv.stop)
	bg := context.Background()

	setup, cancelSetup := context.WithTimeout(bg, 30*time.Second)
	defer cancelSetup()
	queue, err := discovery.NewNATSIngestQueue(setup, srv.url(), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	// Wired as server/router.go and cmd/server: the CI service validates the
	// observed values, the queue's consumer processes what HTTP publishes.
	cis := ci.NewPGRepository(f.App)
	h := discovery.NewHandler(discovery.NewPGRepository(f.App), cis).
		WithObservedValidator(ci.NewService(cis).WithFieldResolver(citype.NewPGRepository(f.App)))
	queue.Attach(h)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	runCtx, stopRun := context.WithCancel(bg)
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		queue.Run(runCtx)
	}()
	t.Cleanup(func() {
		stopRun()
		<-ran
	})
	deadBefore := discovery.IngestDeadLetters()

	post := func(t *testing.T, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", strings.NewReader(body)).
			WithContext(tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})))
		var resp discovery.BulkIngestResponse
		if uerr := json.Unmarshal(w.Body.Bytes(), &resp); w.Code != http.StatusAccepted || uerr != nil || resp.Queued != 3 || resp.Rejected != 0 {
			t.Fatalf("ingest = %d %s, want 202 with the three records queued: a malformed typed value refuses no batch", w.Code, w.Body.String())
		}
	}

	type device struct{ uuid, model string }
	devices := func(t *testing.T, serials ...string) map[string]device {
		t.Helper()
		rows, qerr := f.Admin.Query(bg, `SELECT serial_number, COALESCE(hardware_uuid::text, ''), COALESCE(model, '') FROM ci
			WHERE organization_id = $1 AND serial_number = ANY($2) AND deleted_at IS NULL`, f.OrgA, serials)
		if qerr != nil {
			t.Fatal(qerr)
		}
		defer rows.Close()
		out := map[string]device{}
		for rows.Next() {
			var serial string
			var d device
			if serr := rows.Scan(&serial, &d.uuid, &d.model); serr != nil {
				t.Fatal(serr)
			}
			out[serial] = d
		}
		return out
	}
	consumerInfo := func(t *testing.T) *jetstream.ConsumerInfo {
		t.Helper()
		nc, cerr := nats.Connect(srv.url(), nats.NoReconnect())
		if cerr != nil {
			t.Fatal(cerr)
		}
		defer nc.Close()
		js, jerr := jetstream.New(nc)
		if jerr != nil {
			t.Fatal(jerr)
		}
		cons, cerr := js.Consumer(bg, discovery.IngestStream, discovery.IngestLaneConsumerPrefix+"0")
		if cerr != nil {
			t.Fatal(cerr)
		}
		info, ierr := cons.Info(bg)
		if ierr != nil {
			t.Fatal(ierr)
		}
		return info
	}

	// Each batch is [valid, malformed hardware_uuid, valid]: once as
	// DeviceRecords and once in the deprecated items contract, whose
	// malformed value is not refused by any contract check and reaches the
	// consumer as sent.
	at := time.Now().UTC().Add(-time.Minute)
	first := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "poison-typed-a", IP: "192.0.2.201",
		Serial: "SN-01-POISON-A", Model: "model-1"}, at)
	malformed := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "poison-typed-a", IP: "192.0.2.201",
		Serial: "SN-01-POISON-A", Model: "model-2"}, at.Add(time.Second))
	malformed.Identity.HardwareUUID = "not-a-uuid"
	last := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "poison-typed-b", IP: "192.0.2.202",
		Serial: "SN-01-POISON-B"}, at.Add(2*time.Second))
	records, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{first, malformed, last}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, body, a, b string }{
		{"records", string(records), "SN-01-POISON-A", "SN-01-POISON-B"},
		{"items", `{"collector_id":"poison-typed","items":[
			{"ci_type_name":"server","name":"poison-typed-c","serial_number":"SN-01-POISON-C","model":"model-1"},
			{"ci_type_name":"server","name":"poison-typed-c","serial_number":"SN-01-POISON-C","model":"model-2","hardware_uuid":"not-a-uuid"},
			{"ci_type_name":"server","name":"poison-typed-d","serial_number":"SN-01-POISON-D"}]}`, "SN-01-POISON-C", "SN-01-POISON-D"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The three messages of this batch, delivered once each.
			delivered := consumerInfo(t).Delivered.Consumer + 3
			post(t, c.body)
			settled := func(info *jetstream.ConsumerInfo) bool {
				return info.NumPending == 0 && info.NumAckPending == 0 && info.AckFloor.Consumer >= delivered
			}
			deadline := time.Now().Add(30 * time.Second)
			got := devices(t, c.a, c.b)
			for (len(got) < 2 || got[c.a].model != "model-2" || !settled(consumerInfo(t))) && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
				got = devices(t, c.a, c.b)
			}
			if len(got) != 2 {
				t.Errorf("devices stored: %v, want exactly two (%s and %s)", got, c.a, c.b)
			}
			if a := got[c.a]; a.model != "model-2" || a.uuid != "" {
				t.Errorf("device %s = %+v, want model-2 from the malformed record and no hardware_uuid (the malformed value dropped)", c.a, a)
			}
			if _, ok := got[c.b]; !ok {
				t.Errorf("the record after the malformed one (%s) was not stored", c.b)
			}
			// Every message acknowledged at its first delivery; a redelivery
			// loop would show as pending, redelivered or dead-lettered
			// messages.
			time.Sleep(time.Second)
			info := consumerInfo(t)
			if !settled(info) || info.NumRedelivered != 0 || info.Delivered.Consumer != delivered {
				t.Errorf("consumer: pending %d, ack pending %d, ack floor %d, redelivered %d, delivered %d; want delivered %d, each message acknowledged once",
					info.NumPending, info.NumAckPending, info.AckFloor.Consumer, info.NumRedelivered, info.Delivered.Consumer, delivered)
			}
			if n := discovery.IngestDeadLetters() - deadBefore; n != 0 {
				t.Errorf("dead letters grew by %d, want 0", n)
			}
		})
	}
}
