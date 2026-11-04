package document_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/go-chi/chi/v5"
)

// stubClamd answers every INSTREAM with its current answer.
type stubClamd struct {
	ln     net.Listener
	mu     sync.Mutex
	answer string
}

func startStubClamd(t *testing.T, answer string) *stubClamd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &stubClamd{ln: ln, answer: answer}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			s.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })
	return s
}

func (s *stubClamd) set(answer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = answer
}

func (s *stubClamd) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	if cmd, err := r.ReadString(0); err != nil || cmd != "zINSTREAM\x00" {
		return
	}
	for {
		var size [4]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(size[:])
		if n == 0 {
			break
		}
		if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
			return
		}
	}
	s.mu.Lock()
	answer := s.answer
	s.mu.Unlock()
	_, _ = conn.Write([]byte(answer + "\x00"))
}

// committedSubmitter checks on every submission that the version is
// committed (visible to another connection) before it hands the job to
// the queue.
type committedSubmitter struct {
	t     *testing.T
	w     *versionWorld
	queue *jobs.Queue
	calls int
}

func (s *committedSubmitter) Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	s.calls++
	p := params.(document.ScanParams)
	if got := s.w.scalar(s.t, `SELECT count(*)::text FROM document_version WHERE document_id = $1 AND version = $2`, p.DocumentID, p.Version); got != "1" {
		s.t.Errorf("scan of version %d submitted before its commit (rows visible: %s)", p.Version, got)
	}
	return s.queue.Submit(ctx, orgID, kind, params, opts)
}

// scanDelivery is a message of the job queue for Process.
type scanDelivery struct{ data []byte }

func (d *scanDelivery) Data() []byte                     { return d.data }
func (d *scanDelivery) Ack() error                       { return nil }
func (d *scanDelivery) NakWithDelay(time.Duration) error { return nil }
func deliveryFor(t *testing.T, job *jobs.Job) *scanDelivery {
	t.Helper()
	data, err := json.Marshal(jobs.Message{OrganizationID: job.OrganizationID, JobID: job.ID, Kind: job.Kind})
	if err != nil {
		t.Fatal(err)
	}
	return &scanDelivery{data: data}
}

// TestDocumentMalwareScan covers WP-734 (SEC-09, MGT-01, E-42) against
// PostgreSQL with a clamd stub and the job queue without NATS: an upload
// submits exactly one document.scan with the key
// document.scan:<document>:<version> and the uploader as creator, after
// the commit of the version; a repeated submission is the same job. FOUND
// writes status infected, the audit document.quarantined and the event
// document.scanned in one transaction, with scan_signature in the clear in
// both (no $pii) and the client of the links. While clamd fails the
// version stays locked; the last attempt stores scan_failed and the job
// succeeds (no job.failed). Once clamd answers again, the sweep submits a
// rescan, the version becomes clean and the download answers 200.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentMalwareScan(t *testing.T) {
	w := &versionWorld{linkWorld: newLinkWorld(t), blobDir: t.TempDir()}
	f := w.f
	ctx := context.Background()
	clamd := startStubClamd(t, "stream: Win.Test.EICAR_HDB-1 FOUND")
	blobs := blob.NewFileStore(w.blobDir)
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	queue := jobs.NewQueue(svc, nil)
	scanner := document.NewScanner(w.repo, blobs, config.DocumentSettings{ClamdAddr: clamd.ln.Addr().String(), ClamdTimeout: 2 * time.Second}).
		WithPool(f.App)
	queue.Handle(jobs.Kind{Name: document.ScanJobKind, MaxAttempts: 5, Timeout: 2 * time.Minute,
		Backoff: jobs.ExponentialBackoff(30*time.Second, 15*time.Minute)}, scanner.RunJob)
	submitter := &committedSubmitter{t: t, w: w, queue: queue}
	w.mux = chi.NewRouter()
	document.NewHandler(w.repo, blobs).WithScans(submitter).RegisterRoutes(w.mux)

	jobOf := func(t *testing.T, key string) *jobs.Job {
		t.Helper()
		id := w.scalar(t, `SELECT id::text FROM job WHERE organization_id = $1 AND idempotency_key = $2`, f.OrgA, key)
		job, err := svc.Get(ctx, f.OrgA, id)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	process := func(t *testing.T, job *jobs.Job) {
		t.Helper()
		if out := queue.Process(ctx, deliveryFor(t, job)); !out.Ran {
			t.Fatalf("job %s did not run: %+v", job.ID, out)
		}
	}

	// Infected: one job after the commit, status, audit and event in one
	// transaction.
	d := w.doc(t, "scan-infected", "asset", w.asset1)
	if rec := w.call(w.orgScope(), http.MethodPut, "/api/v1/documents/"+d+"/content", "application/pdf", "%PDF-eicar"); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	key := "document.scan:" + d + ":1"
	if got := w.scalar(t, `SELECT count(*)::text || '|' || min(status) || '|' || min(created_by::text) || '|' || min(params::text)
		FROM job WHERE organization_id = $1 AND kind = 'document.scan' AND idempotency_key = $2`, f.OrgA, key); got !=
		"1|queued|"+w.user+`|{"version": 1, "document_id": "`+d+`"}` {
		t.Fatalf("scan job after the upload: %s", got)
	}
	if again, created, err := queue.Submit(ctx, f.OrgA, document.ScanJobKind, document.ScanParams{DocumentID: d, Version: 1},
		jobs.EnqueueOptions{IdempotencyKey: key}); err != nil || created || again.ID != jobOf(t, key).ID {
		t.Fatalf("repeated submission: %+v created %v %v", again, created, err)
	}
	if submitter.calls != 1 {
		t.Fatalf("submissions of the upload: %d, want 1", submitter.calls)
	}
	process(t, jobOf(t, key))
	if got := w.scalar(t, `SELECT status || '|' || (result->>'result') FROM job WHERE organization_id = $1 AND idempotency_key = $2`, f.OrgA, key); got != "succeeded|infected" {
		t.Fatalf("scan job: %s", got)
	}
	if got := w.scalar(t, `SELECT scan_status || '|' || scan_signature FROM document_version WHERE document_id = $1`, d); got != "infected|Win.Test.EICAR_HDB-1" {
		t.Fatalf("version after FOUND: %s", got)
	}
	auditChanges := w.scalar(t, `SELECT changes::text FROM audit_log WHERE organization_id = $1 AND resource_id = $2 AND action = 'document.quarantined'`, f.OrgA, d)
	eventData := w.scalar(t, `SELECT data::text FROM event_outbox WHERE organization_id = $1 AND type = 'document.scanned' AND entity_id = $2`, f.OrgA, d)
	for what, raw := range map[string]string{"audit": auditChanges, "event": eventData} {
		if !strings.Contains(raw, `"scan_signature": "Win.Test.EICAR_HDB-1"`) || strings.Contains(raw, "$pii") {
			t.Fatalf("%s carries the signature not in the clear: %s", what, raw)
		}
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(eventData), &data); err != nil {
		t.Fatal(err)
	}
	if data["id"] != d || data["document_id"] != d || data["version"] != float64(1) || data["result"] != "infected" ||
		data["uploaded_by"] != w.user || data["client_id"] != f.Client1 {
		t.Fatalf("event data %v", data)
	}
	if got := w.scalar(t, `SELECT count(DISTINCT x)::text FROM (
		SELECT xmin::text AS x FROM document_version WHERE document_id::text = $1::text
		UNION ALL SELECT xmin::text FROM audit_log WHERE organization_id = $2 AND resource_id::text = $1::text AND action = 'document.quarantined'
		UNION ALL SELECT xmin::text FROM event_outbox WHERE organization_id = $2 AND type = 'document.scanned' AND entity_id::text = $1::text) t`, d, f.OrgA); got != "1" {
		t.Fatalf("status, audit and event in %s transactions, want 1", got)
	}

	// Scanner failing: locked; the last attempt stores scan_failed and the
	// job succeeds without job.failed.
	clamd.set("INSTREAM size limit exceeded. ERROR")
	c := w.doc(t, "scan-later", "asset", w.asset1)
	if rec := w.call(w.orgScope(), http.MethodPut, "/api/v1/documents/"+c+"/content", "application/pdf", "%PDF-later"); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	ckey := "document.scan:" + c + ":1"
	process(t, jobOf(t, ckey))
	if got := w.scalar(t, `SELECT status || '|' || attempts::text || '|' || (SELECT scan_status FROM document_version WHERE document_id = $3)
		FROM job WHERE organization_id = $1 AND idempotency_key = $2`, f.OrgA, ckey, c); got != "queued|1|pending_scan" {
		t.Fatalf("after a failed attempt: %s", got)
	}
	if _, err := f.Admin.Exec(ctx, `UPDATE job SET attempts = 4, run_after = now() - interval '1 second' WHERE organization_id = $1 AND idempotency_key = $2`,
		f.OrgA, ckey); err != nil {
		t.Fatal(err)
	}
	process(t, jobOf(t, ckey))
	if got := w.scalar(t, `SELECT status || '|' || (result->>'result') || '|' || (SELECT scan_status FROM document_version WHERE document_id = $3)
		FROM job WHERE organization_id = $1 AND idempotency_key = $2`, f.OrgA, ckey, c); got != "succeeded|scan_failed|scan_failed" {
		t.Fatalf("after the last attempt: %s", got)
	}
	if got := w.scalar(t, `SELECT count(*)::text FROM event_outbox WHERE organization_id = $1 AND type = 'job.failed'`, f.OrgA); got != "0" {
		t.Fatalf("job.failed events: %s, want 0", got)
	}
	if rec := w.call(w.orgScope(), http.MethodGet, "/api/v1/documents/"+c+"/content", "", ""); rec.Code != http.StatusConflict {
		t.Fatalf("download of a scan_failed version: %d", rec.Code)
	}

	// clamd is back: the sweep submits a rescan, the version is released.
	clamd.set("stream: OK")
	now := time.Now().UTC()
	sweeper := document.NewScanSweeper(w.repo, func(context.Context) ([]string, error) { return []string{f.OrgA}, nil }, queue, svc).
		WithClock(func() time.Time { return now })
	if err := sweeper.SweepOnce(ctx); err != nil {
		t.Fatal(err)
	}
	rkey := ckey + ":rescan:" + now.Truncate(15*time.Minute).Format(time.RFC3339)
	rescan := jobOf(t, rkey)
	if rescan.CreatedBy != w.user {
		t.Fatalf("rescan created by %q, want the uploader", rescan.CreatedBy)
	}
	if got := w.scalar(t, `SELECT count(*)::text FROM job WHERE organization_id = $1 AND kind = 'document.scan' AND status = 'queued'`, f.OrgA); got != "1" {
		t.Fatalf("open scan jobs after the sweep: %s, want 1 (the infected version is not rescanned)", got)
	}
	if err := sweeper.SweepOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.scalar(t, `SELECT count(*)::text FROM job WHERE organization_id = $1 AND kind = 'document.scan'`, f.OrgA); got != "3" {
		t.Fatalf("scan jobs after a second sweep with an open rescan: %s, want 3", got)
	}
	process(t, rescan)
	if got := w.scalar(t, `SELECT scan_status FROM document_version WHERE document_id = $1`, c); got != document.ScanClean {
		t.Fatalf("version after the rescan: %s", got)
	}
	if got := w.scalar(t, `SELECT count(*)::text FROM audit_log WHERE organization_id = $1 AND resource_id = $2 AND action = 'document.scan_clean'`, f.OrgA, c); got != "1" {
		t.Fatalf("audit document.scan_clean: %s", got)
	}
	if rec := w.call(w.orgScope(), http.MethodGet, "/api/v1/documents/"+c+"/content", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("download after the rescan: %d %s", rec.Code, rec.Body.String())
	}
}
