package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// TestIngestRecordLedger covers WP-085 (REC-01) against PostgreSQL: the
// ingest_record ledger keeps processed record_ids per organization under
// RLS, a repeated record_id is not processed again within 48 hours, an older
// entry no longer counts and is removed; records of a batch are processed in
// observed_at order. With TEST_NATS_URL the same records also run through the
// ingest.> stream and its consumer.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestRecordLedger(t *testing.T) {
	f := scopetest.Seed(t, "77")
	bg := context.Background()
	repo := discovery.NewPGRepository(f.App)
	cis := ci.NewPGRepository(f.App)
	handler := discovery.NewHandler(repo, cis)
	mux := chi.NewRouter()
	handler.RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	record := func(serial, model string, observedAt time.Time) plugins.DeviceRecord {
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "ledger-" + serial, IP: "192.0.2.77",
			Serial: serial, Model: model}, observedAt)
	}
	post := func(records ...plugins.DeviceRecord) discovery.BulkIngestResponse {
		t.Helper()
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: records})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx)
		req.Header.Set("X-Collector-ID", "col-77")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
		var resp discovery.BulkIngestResponse
		if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	model := func(serial string) string {
		t.Helper()
		var m string
		if err := f.Admin.QueryRow(bg, `SELECT model FROM ci WHERE organization_id = $1 AND serial_number = $2`, f.OrgA, serial).Scan(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	t0 := time.Now().UTC().Add(-time.Hour)
	first := record("LEDGER-1", "m1", t0)
	if resp := post(first); resp.Created != 1 {
		t.Fatalf("first: %+v", resp)
	}
	var collector string
	if err := f.Admin.QueryRow(bg, `SELECT collector_id FROM ingest_record WHERE organization_id = $1 AND record_id = $2`,
		f.OrgA, first.RecordID).Scan(&collector); err != nil || collector != "col-77" {
		t.Fatalf("ledger row: %q, %v", collector, err)
	}
	if resp := post(first); resp.Duplicates != 1 || resp.Updated != 0 {
		t.Errorf("repeated record_id: %+v", resp)
	}

	// The ledger is per organization and under RLS.
	var visible int
	scopeB := database.OrgWideScope(f.OrgB, f.User)
	if err := database.WithTenant(bg, f.App, &scopeB, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM ingest_record WHERE record_id = $1`, first.RecordID).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Errorf("ledger row visible to another organization: %d, %v", visible, err)
	}

	// After 48 hours the record_id counts again, and the old entry is removed.
	if _, err := f.Admin.Exec(bg, `UPDATE ingest_record SET processed_at = now() - interval '49 hours' WHERE record_id = $1`, first.RecordID); err != nil {
		t.Fatal(err)
	}
	stale := record("LEDGER-2", "x", t0)
	post(stale)
	var left int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ingest_record WHERE record_id = $1 AND processed_at < now() - interval '48 hours'`,
		first.RecordID).Scan(&left); err != nil || left != 0 {
		t.Errorf("expired ledger rows left: %d, %v", left, err)
	}
	if resp := post(first); resp.Duplicates != 0 {
		t.Errorf("record_id after 48 hours: %+v", resp)
	}

	// Order by observed_at within a batch.
	if resp := post(record("LEDGER-1", "m3", t0.Add(2*time.Minute)), record("LEDGER-1", "m2", t0.Add(time.Minute))); resp.Updated != 2 {
		t.Errorf("batch: %+v", resp)
	}
	if m := model("LEDGER-1"); m != "m3" {
		t.Errorf("model after the batch: %s, want m3", m)
	}

	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Log("TEST_NATS_URL not set; skipping the ingest.> stream")
		return
	}
	queueCtx, cancel := context.WithTimeout(bg, 10*time.Second)
	queue, err := discovery.NewNATSIngestQueue(queueCtx, url)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	queued := discovery.NewHandler(repo, cis)
	queue.Attach(queued)
	qmux := chi.NewRouter()
	queued.RegisterRoutes(qmux)
	mux = qmux
	runCtx, stop := context.WithCancel(bg)
	defer stop()
	go queue.Run(runCtx)

	streamed := record("LEDGER-3", "s1", t0)
	if resp := post(streamed, streamed, record("LEDGER-3", "s2", t0.Add(time.Minute))); resp.Queued != 3 || resp.Created != 0 {
		t.Fatalf("queued ingest: %+v", resp)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var n int
		var m string
		_ = f.Admin.QueryRow(bg, `SELECT count(*), COALESCE(max(model), '') FROM ci WHERE organization_id = $1 AND serial_number = 'LEDGER-3'`,
			f.OrgA).Scan(&n, &m)
		if n == 1 && m == "s2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream not consumed: %d CIs, model %q", n, m)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var processed int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM ingest_record WHERE organization_id = $1 AND record_id = $2`,
		f.OrgA, streamed.RecordID).Scan(&processed); err != nil || processed != 1 {
		t.Errorf("streamed record in the ledger: %d, %v", processed, err)
	}
}
