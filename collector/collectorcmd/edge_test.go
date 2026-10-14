package collectorcmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// withEdge replaces the edge requester for one test.
func withEdge(t *testing.T, fn func(ctx context.Context, suffix string, data []byte) ([]byte, error)) {
	t.Helper()
	previous := edgeRequest
	edgeRequest = fn
	t.Cleanup(func() { edgeRequest = previous })
}

// TestUploadPrefersTheEdge covers the collector side of COL-08: with the
// edge channel the records go there and not over HTTP; without it, or when
// no server answers the subject, the HTTP ingest takes them.
func TestUploadPrefersTheEdge(t *testing.T) {
	var httpUploads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpUploads.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	up, err := newUploader(context.Background(), testConfig(srv.URL, ""))
	if err != nil {
		t.Fatal(err)
	}
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "edge-srv", IP: "192.0.2.10"}, time.Now().Add(-time.Minute))

	var edgeSubjects []string
	withEdge(t, func(_ context.Context, suffix string, _ []byte) ([]byte, error) {
		edgeSubjects = append(edgeSubjects, suffix)
		return []byte(`{"status":202,"license_status":"active"}`), nil
	})
	if err = up.uploadRecords(context.Background(), []plugins.DeviceRecord{rec}); err != nil {
		t.Fatal(err)
	}
	if len(edgeSubjects) != 1 || edgeSubjects[0] != edgeIngestSuffix || httpUploads.Load() != 0 {
		t.Errorf("edge %v, http %d", edgeSubjects, httpUploads.Load())
	}

	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errNoEdge })
	if err = up.uploadRecords(context.Background(), []plugins.DeviceRecord{rec}); err != nil {
		t.Fatal(err)
	}
	if httpUploads.Load() != 1 {
		t.Errorf("fallback to HTTP: %d uploads", httpUploads.Load())
	}
}

// TestEdgeRefusals: an expired license pauses the collector, other refusals
// are upload errors (the batch is spooled by the caller).
func TestEdgeRefusals(t *testing.T) {
	up, err := newUploader(context.Background(), testConfig("http://127.0.0.1:1", ""))
	if err != nil {
		t.Fatal(err)
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`{"status":403,"type":"` + problemLicenseExpired + `"}`), nil
	})
	if err = up.postViaEdge(context.Background(), []byte(`{}`)); !errors.Is(err, errLicenseExpired) || !up.paused.Load() {
		t.Fatalf("expired license: %v, paused %v", err, up.paused.Load())
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`{"status":202,"license_status":"active"}`), nil
	})
	if err = up.postViaEdge(context.Background(), []byte(`{}`)); err != nil || up.paused.Load() {
		t.Fatalf("renewed license: %v, paused %v", err, up.paused.Load())
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`{"status":422,"error":"records[0].source missing"}`), nil
	})
	if err = up.postViaEdge(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("refused batch reported as delivered")
	}
}

// TestHeartbeatOverTheEdge: the heartbeat takes the edge and reads the
// license state from the reply; without the edge it is not handled there.
func TestHeartbeatOverTheEdge(t *testing.T) {
	withEdge(t, func(_ context.Context, suffix string, _ []byte) ([]byte, error) {
		if suffix != edgeHeartbeatSuffix {
			t.Errorf("subject suffix %q", suffix)
		}
		return []byte(`{"status":204,"license_status":"active","collector_status":"active"}`), nil
	})
	if license, ok, handled := heartbeatViaEdge(context.Background(), []byte(`{}`)); !handled || !ok || license != "active" {
		t.Errorf("edge heartbeat: %q %v %v", license, ok, handled)
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errNoEdge })
	if _, _, handled := heartbeatViaEdge(context.Background(), []byte(`{}`)); handled {
		t.Error("handled without the edge")
	}
}
