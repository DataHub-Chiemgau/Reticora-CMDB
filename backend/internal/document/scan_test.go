package document

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/go-chi/chi/v5"
)

// clamdStub is a clamd on a local port that answers INSTREAM with answer;
// an empty answer never answers (timeout). It records the streamed bytes.
type clamdStub struct {
	ln     net.Listener
	mu     sync.Mutex
	answer string
	got    []string
	wg     sync.WaitGroup
}

func newClamdStub(t *testing.T, answer string) *clamdStub {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &clamdStub{ln: ln, answer: answer}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(s.close)
	return s
}

func (s *clamdStub) addr() string { return s.ln.Addr().String() }

func (s *clamdStub) setAnswer(answer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = answer
}

func (s *clamdStub) close() {
	_ = s.ln.Close()
	s.wg.Wait()
}

func (s *clamdStub) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { _ = conn.Close() }()
			s.handle(conn)
		}()
	}
}

func (s *clamdStub) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	cmd, err := r.ReadString(0)
	if err != nil || cmd != "zINSTREAM\x00" {
		_, _ = conn.Write([]byte("UNKNOWN COMMAND\x00"))
		return
	}
	var body strings.Builder
	for {
		var size [4]byte
		if _, err = io.ReadFull(r, size[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(size[:])
		if n == 0 {
			break
		}
		if _, err = io.CopyN(&body, r, int64(n)); err != nil {
			return
		}
	}
	s.mu.Lock()
	s.got = append(s.got, body.String())
	answer := s.answer
	s.mu.Unlock()
	if answer == "" {
		// No answer: the client runs into its timeout.
		time.Sleep(time.Second)
		return
	}
	_, _ = conn.Write([]byte(answer + "\x00"))
}

// submitStub records the submitted scans.
type submitStub struct {
	mu   sync.Mutex
	jobs []jobs.Job
	err  error
}

func (s *submitStub) Submit(_ context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.jobs {
		if s.jobs[i].IdempotencyKey == opts.IdempotencyKey {
			return &s.jobs[i], false, nil
		}
	}
	job := jobs.Job{ID: opts.IdempotencyKey, OrganizationID: orgID, Kind: kind, Status: jobs.StatusQueued, Params: raw,
		IdempotencyKey: opts.IdempotencyKey, CreatedBy: opts.CreatedBy, MaxAttempts: 5}
	s.jobs = append(s.jobs, job)
	return &job, true, nil
}

// List implements JobLister over the recorded jobs.
func (s *submitStub) List(_ context.Context, orgID string, filter jobs.Filter, page api.PaginationParams) ([]jobs.Job, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []jobs.Job
	for _, j := range s.jobs {
		if j.OrganizationID == orgID && j.Kind == filter.Kind && j.Status == filter.Status {
			out = append(out, j)
		}
	}
	total := len(out)
	if page.Offset >= len(out) {
		return nil, total, nil
	}
	out = out[page.Offset:]
	if len(out) > page.Limit {
		out = out[:page.Limit]
	}
	return out, total, nil
}

// finish sets the status of every recorded job.
func (s *submitStub) finish(status jobs.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.jobs {
		s.jobs[i].Status = status
	}
}

func (s *submitStub) all() []jobs.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]jobs.Job(nil), s.jobs...)
}

// scanFixture is a document with one uploaded version, the submitted scan
// and a scanner against a clamd stub.
type scanFixture struct {
	repo      *MemoryRepository
	mux       chi.Router
	id        string
	submitted *submitStub
	clamd     *clamdStub
	scanner   *Scanner
}

func newScanFixture(t *testing.T, answer string) *scanFixture {
	t.Helper()
	repo := NewMemoryRepository()
	doc := &Document{OrganizationID: "org-1", Title: "Scan", FileName: "scan.pdf", MimeType: "application/pdf", Category: "general"}
	if err := repo.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	blobs := newFakeBlobStore()
	f := &scanFixture{repo: repo, mux: chi.NewRouter(), id: doc.ID, submitted: &submitStub{}, clamd: newClamdStub(t, answer)}
	NewHandler(repo, blobs).WithScans(f.submitted).RegisterRoutes(f.mux)
	f.scanner = NewScanner(repo, blobs, config.DocumentSettings{ClamdAddr: f.clamd.addr(), ClamdTimeout: 300 * time.Millisecond})
	return f
}

// upload stores body as the next version.
func (f *scanFixture) upload(t *testing.T, body string) {
	t.Helper()
	if w := contentRequest(t, f.mux, http.MethodPost, "/api/v1/documents/"+f.id+"/versions", "application/pdf", body); w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
}

// run runs the scan job j as attempt n.
func (f *scanFixture) run(t *testing.T, j jobs.Job, attempt int) (*ScanResult, error) {
	t.Helper()
	j.Attempts, j.MaxAttempts = attempt, 5
	out, err := f.scanner.RunJob(context.Background(), &j)
	if err != nil {
		return nil, err
	}
	return out.(*ScanResult), nil
}

func (f *scanFixture) status(t *testing.T, n int) string {
	t.Helper()
	v, err := f.repo.GetVersion(context.Background(), "org-1", f.id, n)
	if err != nil {
		t.Fatal(err)
	}
	return v.ScanStatus
}

func (f *scanFixture) download(t *testing.T) int {
	t.Helper()
	return contentRequest(t, f.mux, http.MethodGet, "/api/v1/documents/"+f.id+"/content", "", "").Code
}

// TestScanUploadSubmitsOneJob covers SEC-09 (WP-734): the upload submits
// one document.scan with the document and the version under the key
// document.scan:<document>:<version>; a failed submission keeps the upload
// and leaves the version pending_scan.
func TestScanUploadSubmitsOneJob(t *testing.T) {
	f := newScanFixture(t, "stream: OK")
	f.upload(t, "%PDF-one")
	got := f.submitted.all()
	if len(got) != 1 || got[0].Kind != ScanJobKind || got[0].IdempotencyKey != "document.scan:"+f.id+":1" ||
		string(got[0].Params) != `{"document_id":"`+f.id+`","version":1}` {
		t.Fatalf("submitted %+v", got)
	}
	f.submitted.err = errors.New("queue down")
	f.upload(t, "%PDF-two")
	if f.status(t, 2) != ScanPending || len(f.submitted.all()) != 1 {
		t.Fatalf("version 2 after a failed submission: %s, jobs %d", f.status(t, 2), len(f.submitted.all()))
	}
}

// TestScanResults covers SEC-09 (WP-734, E-42) with a clamd stub: FOUND
// quarantines the version with the signature, OK makes it clean and
// downloadable, the content reaches clamd unchanged. INSTREAM size limit
// exceeded, another answer and a timeout are failed attempts that keep the
// version locked; on the last attempt the job stores scan_failed and ends
// without error, so no job.failed follows.
func TestScanResults(t *testing.T) {
	f := newScanFixture(t, "stream: Win.Test.EICAR_HDB-1 FOUND")
	f.upload(t, "%PDF-eicar")
	job := f.submitted.all()[0]
	out, err := f.run(t, job, 1)
	if err != nil || out.Result != ScanInfected {
		t.Fatalf("FOUND: %+v %v", out, err)
	}
	if f.status(t, 1) != ScanInfected || f.download(t) != http.StatusConflict || f.clamd.got[0] != "%PDF-eicar" {
		t.Fatalf("FOUND: status %s, download %d, streamed %q", f.status(t, 1), f.download(t), f.clamd.got)
	}
	// A scanned version is not scanned again (redelivery).
	if out, err = f.run(t, job, 2); err != nil || !out.Skipped || len(f.clamd.got) != 1 {
		t.Fatalf("second run of a scanned version: %+v %v", out, err)
	}

	f = newScanFixture(t, "stream: OK")
	f.upload(t, "%PDF-clean")
	if out, err = f.run(t, f.submitted.all()[0], 1); err != nil || out.Result != ScanClean || f.status(t, 1) != ScanClean ||
		f.download(t) != http.StatusOK {
		t.Fatalf("OK: %+v %v, status %s", out, err, f.status(t, 1))
	}

	for name, answer := range map[string]string{
		"size limit": "INSTREAM size limit exceeded. ERROR",
		"error":      "stream: Can't allocate memory ERROR",
		"timeout":    "",
	} {
		t.Run(name, func(t *testing.T) {
			f := newScanFixture(t, answer)
			f.upload(t, "%PDF-x")
			job := f.submitted.all()[0]
			for attempt := 1; attempt < 5; attempt++ {
				if out, err := f.run(t, job, attempt); err == nil {
					t.Fatalf("attempt %d: %+v without error", attempt, out)
				}
				if f.status(t, 1) != ScanPending || f.download(t) != http.StatusConflict {
					t.Fatalf("attempt %d: status %s, download %d", attempt, f.status(t, 1), f.download(t))
				}
			}
			out, err := f.run(t, job, 5)
			if err != nil || out.Result != ScanFailed || f.status(t, 1) != ScanFailed || f.download(t) != http.StatusConflict {
				t.Fatalf("last attempt: %+v %v, status %s", out, err, f.status(t, 1))
			}
		})
	}
}

// TestScanStaysPendingWhileScannerDown covers E-42 (fail-closed): with
// clamd unreachable every attempt fails and the version stays locked;
// without blob content the scan fails as well.
func TestScanStaysPendingWhileScannerDown(t *testing.T) {
	f := newScanFixture(t, "stream: OK")
	f.upload(t, "%PDF-down")
	f.clamd.close()
	for attempt := 1; attempt < 5; attempt++ {
		if _, err := f.run(t, f.submitted.all()[0], attempt); err == nil {
			t.Fatalf("attempt %d with clamd down succeeded", attempt)
		}
	}
	if f.status(t, 1) != ScanPending || f.download(t) != http.StatusConflict {
		t.Fatalf("status %s, download %d, want pending_scan and 409", f.status(t, 1), f.download(t))
	}
	if _, _, err := (&Clamd{}).Scan(context.Background(), strings.NewReader("x")); err == nil {
		t.Fatal("scan without clamd address succeeded")
	}
}

// TestSweepRescansAfterScannerReturns covers E-42: the sweep resubmits a
// pending_scan or scan_failed version without an open scan job under the
// key …:rescan:<slot UTC> with the uploader as creator, skips one with an
// open job, and once clamd is back the rescan makes the version clean and
// downloadable. A deleted document is not resubmitted.
func TestSweepRescansAfterScannerReturns(t *testing.T) {
	f := newScanFixture(t, "")
	f.upload(t, "%PDF-sweep")
	job := f.submitted.all()[0]
	if out, err := f.run(t, job, 5); err != nil || out.Result != ScanFailed {
		t.Fatalf("last attempt with clamd hanging: %+v %v", out, err)
	}
	now := time.Date(2026, 11, 4, 10, 37, 12, 0, time.UTC)
	orgs := func(context.Context) ([]string, error) { return []string{"org-1", "org-2"}, nil }
	sweeper := NewScanSweeper(f.repo, orgs, f.submitted, f.submitted).WithClock(func() time.Time { return now })

	// The first job is still queued: nothing is resubmitted.
	if err := sweeper.SweepOnce(context.Background()); err != nil || len(f.submitted.all()) != 1 {
		t.Fatalf("sweep with an open job: %v, jobs %d", err, len(f.submitted.all()))
	}
	f.submitted.finish(jobs.StatusSucceeded)
	if err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.submitted.all()
	if len(got) != 2 || got[1].IdempotencyKey != "document.scan:"+f.id+":1:rescan:2026-11-04T10:30:00Z" ||
		string(got[1].Params) != `{"document_id":"`+f.id+`","version":1}` {
		t.Fatalf("rescan %+v", got)
	}
	// The same slot again: the rescan job is open.
	if err := sweeper.SweepOnce(context.Background()); err != nil || len(f.submitted.all()) != 2 {
		t.Fatalf("second sweep in the slot: %v, jobs %d", err, len(f.submitted.all()))
	}

	f.clamd.setAnswer("stream: OK")
	if out, err := f.run(t, got[1], 1); err != nil || out.Result != ScanClean || f.status(t, 1) != ScanClean ||
		f.download(t) != http.StatusOK {
		t.Fatalf("rescan after clamd returned: %+v %v, status %s", out, err, f.status(t, 1))
	}
	f.submitted.finish(jobs.StatusSucceeded)
	now = now.Add(scanSweepInterval)
	if err := sweeper.SweepOnce(context.Background()); err != nil || len(f.submitted.all()) != 2 {
		t.Fatalf("sweep after the clean scan: %v, jobs %d", err, len(f.submitted.all()))
	}

	// A deleted document is no longer swept.
	f.upload(t, "%PDF-later")
	if err := f.repo.Delete(context.Background(), "org-1", f.id); err != nil {
		t.Fatal(err)
	}
	f.submitted.finish(jobs.StatusSucceeded)
	if err := sweeper.SweepOnce(context.Background()); err != nil || len(f.submitted.all()) != 3 {
		t.Fatalf("sweep of a deleted document: %v, jobs %d", err, len(f.submitted.all()))
	}
	if out, err := f.run(t, f.submitted.all()[2], 1); err != nil || !out.Skipped {
		t.Fatalf("scan of a deleted document: %+v %v", out, err)
	}
}
