package discovery_test

// Ingest while NATS is down (WP-449, OPS-06, F-136): an ingest request is
// refused visibly and at once (503 with Retry-After) and the refused record
// never reaches the stream, not even after the reconnect. Before WP-449 the
// publish waited in the client's reconnect buffer, the request ended with
// 500 after the JetStream timeout (5 s), and the buffered message was stored
// once NATS was back: a record the client had been told was not accepted was
// processed anyway. The outage suite (tests/outage/load.sh) counts every
// record of its load; this is the same check without the installed stack.
//
// A real nats-server (TEST_NATS_SERVER_BIN) with JetStream is stopped and
// started on the same port and store; no database is needed, the queue's
// consumers do not run.
//
// Retest:
//   cd backend && TEST_NATS_SERVER_BIN=<nats-server> go test -count=1 -run TestIngestQueueOutageRefusesWithoutLateRecords ./internal/discovery/

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// outageNATS is a nats-server with JetStream that the test stops and starts
// again on the same port and store directory.
type outageNATS struct {
	t     *testing.T
	bin   string
	port  int
	store string
	cmd   *exec.Cmd
	out   *strings.Builder
}

func (s *outageNATS) start() {
	s.t.Helper()
	s.out = &strings.Builder{}
	s.cmd = exec.Command(s.bin, "-a", "127.0.0.1", "-p", fmt.Sprint(s.port), "-js", "-sd", s.store)
	s.cmd.Stdout, s.cmd.Stderr = s.out, s.out
	if err := s.cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	for range 100 {
		if nc, err := nats.Connect(s.url(), nats.NoReconnect()); err == nil {
			nc.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("nats-server did not start:\n%s", s.out.String())
}

func (s *outageNATS) stop() {
	if s.cmd == nil {
		return
	}
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
	s.cmd = nil
}

func (s *outageNATS) url() string { return fmt.Sprintf("nats://127.0.0.1:%d", s.port) }

func TestIngestQueueOutageRefusesWithoutLateRecords(t *testing.T) {
	srv := &outageNATS{t: t, bin: edgeNATSServerBin(t), port: auditFreePort(t), store: t.TempDir()}
	srv.start()
	t.Cleanup(srv.stop)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queue, err := discovery.NewNATSIngestQueue(ctx, srv.url(), 1, nats.ReconnectWait(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	handler := discovery.NewHandler(nil, nil)
	queue.Attach(handler)
	mux := chi.NewRouter()
	handler.RegisterRoutes(mux)
	orgCtx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "00000000-0000-4000-8000-000000000449"})

	post := func(serial string) (*httptest.ResponseRecorder, time.Duration) {
		t.Helper()
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, IP: "192.0.2.49", Serial: serial},
			time.Now().UTC().Add(-time.Minute))
		body, merr := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
		if merr != nil {
			t.Fatal(merr)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/bulk", bytes.NewReader(body)).WithContext(orgCtx)
		w := httptest.NewRecorder()
		start := time.Now()
		mux.ServeHTTP(w, req)
		return w, time.Since(start)
	}
	stored := func() []string {
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
		stream, serr := js.Stream(ctx, discovery.IngestStream)
		if serr != nil {
			t.Fatal(serr)
		}
		info, ierr := stream.Info(ctx)
		if ierr != nil {
			t.Fatal(ierr)
		}
		var serials []string
		for seq := info.State.FirstSeq; info.State.Msgs > 0 && seq <= info.State.LastSeq; seq++ {
			msg, gerr := stream.GetMsg(ctx, seq)
			if gerr != nil {
				continue
			}
			var m discovery.IngestMessage
			if uerr := json.Unmarshal(msg.Data, &m); uerr != nil {
				t.Fatal(uerr)
			}
			serials = append(serials, m.Item.SerialNumber)
		}
		return serials
	}

	if w, _ := post("OUTAGE-BEFORE"); w.Code != http.StatusAccepted {
		t.Fatalf("ingest before the outage: %d %s", w.Code, w.Body.String())
	}

	srv.stop()
	// The client notices the closed connection at once; give it a moment.
	time.Sleep(500 * time.Millisecond)
	// Every request during the outage is refused visibly (503 with
	// Retry-After, no internals) and without waiting for the JetStream
	// timeout.
	for i := range 3 {
		w, took := post(fmt.Sprintf("OUTAGE-DURING-%d", i))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("ingest %d during the outage: %d after %s (%s), want 503", i, w.Code, took.Round(time.Millisecond), w.Body.String())
			continue
		}
		if took > 2*time.Second {
			t.Errorf("refusal %d took %s; want it at once, not after the publish timeout", i, took)
		}
		if w.Header().Get("Retry-After") == "" {
			t.Errorf("503 without Retry-After: %v", w.Header())
		}
		if strings.Contains(w.Body.String(), "nats") || strings.Contains(w.Body.String(), "ingest queue") {
			t.Errorf("503 names internals: %s", w.Body.String())
		}
	}

	srv.start()
	var after *httptest.ResponseRecorder
	for range 100 {
		if after, _ = post("OUTAGE-AFTER"); after.Code == http.StatusAccepted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if after.Code != http.StatusAccepted {
		t.Fatalf("ingest after the reconnect: %d %s", after.Code, after.Body.String())
	}
	// Give a buffered publish the time to arrive (it would right after the
	// reconnect), then compare: accepted == stored.
	time.Sleep(time.Second)
	got := stored()
	if strings.Join(got, ",") != "OUTAGE-BEFORE,OUTAGE-AFTER" {
		t.Errorf("records in the stream: %v; want exactly the two accepted ones (OUTAGE-BEFORE, OUTAGE-AFTER)", got)
	}
}
