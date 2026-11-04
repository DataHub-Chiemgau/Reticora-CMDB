package document

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The malware scan of document versions (SEC-09, E-42, WP-734): every
// stored version is a job ScanJobKind on the job queue, submitted after the
// commit of the version. The job streams the blob to clamd (INSTREAM) and
// writes status, audit and the event document.scanned in one transaction.
// The sweep ScanSweepKind submits the versions still pending_scan or
// scan_failed again, so a scanner outage releases the versions on its own
// once clamd is back.
const (
	ScanJobKind   = "document.scan"
	ScanSweepKind = "document.scan_sweep"
)

// Audit actions of a scan result: clean, infected (quarantine) and failed
// after the last attempt.
const (
	ActionScanClean   = "document.scan_clean"
	ActionQuarantined = "document.quarantined"
	ActionScanFailed  = "document.scan_failed"
)

// scanSweepInterval is the slot of the sweep (its cron spec and the slot in
// the idempotency key of a rescan).
const scanSweepInterval = 15 * time.Minute

// scanSweepBatch bounds the versions one organization resubmits per sweep.
const scanSweepBatch = 500

// ScanParams are the parameters of a document.scan job.
type ScanParams struct {
	DocumentID string `json:"document_id"`
	Version    int    `json:"version"`
}

// ScanSubmitter submits jobs to the job queue (jobs.Queue).
type ScanSubmitter interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// scanKey is the idempotency key of the scan of a version after its upload.
func scanKey(docID string, version int) string {
	return ScanJobKind + ":" + docID + ":" + strconv.Itoa(version)
}

// WithScans submits the malware scan of every stored version to submitter
// (the job queue). Without it (no NATS, --no-db) a new version stays
// pending_scan and its download answers 409 (fail-closed, E-42).
func (h *Handler) WithScans(submitter ScanSubmitter) *Handler {
	if q, ok := submitter.(*jobs.Queue); ok && q == nil {
		submitter = nil
	}
	h.scans = submitter
	return h
}

// enqueueScan submits the scan of version v once its transaction has
// committed; the creator of the job is the uploader. A failed submission
// leaves the version pending_scan for the sweep.
func (h *Handler) enqueueScan(ctx context.Context, v *Version) {
	if h.scans == nil {
		return
	}
	database.AfterCommit(ctx, func(ctx context.Context) {
		if _, _, err := h.scans.Submit(context.WithoutCancel(ctx), v.OrganizationID, ScanJobKind,
			ScanParams{DocumentID: v.DocumentID, Version: v.Version},
			jobs.EnqueueOptions{IdempotencyKey: scanKey(v.DocumentID, v.Version), CreatedBy: uuidOrEmpty(v.UploadedBy)}); err != nil {
			slog.WarnContext(ctx, "submit document scan", "document", v.DocumentID, "version", v.Version, "error", err)
		}
	})
}

// clamd answers.
var (
	errScanAnswer  = errors.New("unexpected clamd answer")
	errScanAddress = errors.New("no clamd address")
)

// clamdChunk is the size of one INSTREAM chunk, clamdAnswerLimit the
// longest answer read.
const (
	clamdChunk       = 32 << 10
	clamdAnswerLimit = 4096
)

// Clamd is the client of the clamd INSTREAM command.
type Clamd struct {
	Addr    string
	Timeout time.Duration
}

// Scan streams r to clamd with zINSTREAM: chunks with a 4-byte big-endian
// length prefix, ended by a zero length. It returns the signature of a
// finding ("… FOUND") or "" for OK. Every other answer, INSTREAM size limit
// exceeded included, and every network error or timeout is an error and
// never clean.
func (c *Clamd) Scan(ctx context.Context, r io.Reader) (infected bool, signature string, err error) {
	if c.Addr == "" {
		return false, "", errScanAddress
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = config.DefaultClamdTimeout
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return false, "", fmt.Errorf("connect clamd: %w", err)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	writeErr := streamToClamd(conn, r)
	// clamd answers a refused stream (size limit) and closes; its answer
	// names the cause better than the write error.
	answer, readErr := bufio.NewReader(io.LimitReader(conn, clamdAnswerLimit)).ReadString(0)
	text := strings.TrimSpace(strings.TrimRight(answer, "\x00\n"))
	switch {
	case readErr != nil && text == "":
		if writeErr != nil {
			return false, "", fmt.Errorf("stream to clamd: %w", writeErr)
		}
		return false, "", fmt.Errorf("read clamd answer: %w", readErr)
	case strings.HasSuffix(text, " FOUND"):
		sig := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "stream:"), " FOUND"))
		if sig == "" || writeErr != nil {
			return false, "", fmt.Errorf("%w: %q", errScanAnswer, text)
		}
		return true, sig, nil
	case text == "stream: OK" && writeErr == nil:
		return false, "", nil
	default:
		return false, "", fmt.Errorf("%w: %q", errScanAnswer, text)
	}
}

// streamToClamd writes the command and r as INSTREAM chunks.
func streamToClamd(w io.Writer, r io.Reader) error {
	if _, err := w.Write([]byte("zINSTREAM\x00")); err != nil {
		return err
	}
	buf := make([]byte, 4+clamdChunk)
	for {
		n, err := io.ReadFull(r, buf[4:])
		if n > 0 {
			binary.BigEndian.PutUint32(buf[:4], uint32(n))
			if _, werr := w.Write(buf[:4+n]); werr != nil {
				return werr
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read document content: %w", err)
		}
	}
	_, err := w.Write([]byte{0, 0, 0, 0})
	return err
}

// Scanner runs the document.scan jobs.
type Scanner struct {
	repo   Repository
	blobs  blob.Store
	bucket string
	clamd  *Clamd
	// pool writes status, audit and event in one transaction; without it
	// (tests without database) only the status is stored.
	pool *pgxpool.Pool
}

// NewScanner creates the scanner of the document versions with the clamd
// address and timeout of settings.
func NewScanner(repo Repository, blobs blob.Store, settings config.DocumentSettings) *Scanner {
	return &Scanner{repo: repo, blobs: blobs, bucket: documentBucket,
		clamd: &Clamd{Addr: settings.ClamdAddr, Timeout: settings.ClamdTimeout}}
}

// WithPool writes the result with audit and event in a transaction of pool.
func (s *Scanner) WithPool(pool *pgxpool.Pool) *Scanner {
	s.pool = pool
	return s
}

// ScanResult is the result of a document.scan job.
type ScanResult struct {
	DocumentID string `json:"document_id"`
	Version    int    `json:"version"`
	Result     string `json:"result"`
	// Skipped: the version is gone or was scanned already.
	Skipped bool `json:"skipped,omitempty"`
}

// orgContext is ctx with the organization-wide scope of orgID: the scan is
// a system task of the organization, not of the uploader's scope.
func orgContext(ctx context.Context, orgID string) context.Context {
	scope := database.OrgWideScope(orgID, "")
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: orgID})
	return database.ContextWithTenantScope(ctx, &scope)
}

// RunJob is the handler of document.scan: it reads the blob and scans it
// outside any transaction. A failed scan is an error, retried with the
// backoff of the kind; on the last attempt it stores scan_failed and ends
// without error, so the job sends no job.failed. A version that is gone
// (deleted document) or was scanned already ends without a scan.
func (s *Scanner) RunJob(ctx context.Context, job *jobs.Job) (any, error) {
	var p ScanParams
	if err := json.Unmarshal(job.Params, &p); err != nil || p.DocumentID == "" || p.Version < 1 {
		return nil, fmt.Errorf("document scan: invalid parameters %s", job.Params)
	}
	orgCtx := orgContext(ctx, job.OrganizationID)
	out := &ScanResult{DocumentID: p.DocumentID, Version: p.Version}
	v, err := s.repo.GetVersion(orgCtx, job.OrganizationID, p.DocumentID, p.Version)
	if errors.Is(err, api.ErrNotFound) {
		out.Skipped = true
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("document scan: %w", err)
	}
	if v.ScanStatus == ScanClean || v.ScanStatus == ScanInfected {
		out.Result, out.Skipped = v.ScanStatus, true
		return out, nil
	}

	infected, signature, scanErr := s.scan(ctx, v)
	status := ScanClean
	switch {
	case scanErr != nil:
		if job.Attempts < job.MaxAttempts {
			return nil, fmt.Errorf("document scan: %w", scanErr)
		}
		slog.WarnContext(ctx, "document scan failed for good", "document", v.DocumentID, "version", v.Version, "error", scanErr)
		status, signature = ScanFailed, ""
	case infected:
		status = ScanInfected
	}
	if err = s.record(ctx, job.OrganizationID, v, status, signature); err != nil {
		return nil, fmt.Errorf("document scan: store result: %w", err)
	}
	out.Result = status
	return out, nil
}

// scan reads the blob of v and streams it to clamd.
func (s *Scanner) scan(ctx context.Context, v *Version) (infected bool, signature string, err error) {
	if s.blobs == nil {
		return false, "", errors.New("no blob storage")
	}
	if v.StorageKey == "" {
		return false, "", errors.New("version without stored content")
	}
	rc, err := s.blobs.Get(ctx, s.bucket, v.StorageKey)
	if err != nil {
		return false, "", fmt.Errorf("read document content: %w", err)
	}
	defer func() { _ = rc.Close() }()
	return s.clamd.Scan(ctx, rc)
}

// scanAction is the audit action of a scan status.
func scanAction(status string) string {
	switch status {
	case ScanClean:
		return ActionScanClean
	case ScanInfected:
		return ActionQuarantined
	default:
		return ActionScanFailed
	}
}

// record stores the result of v in one transaction of the organization:
// first the status (SetScanResult joins it), then the audit entry with
// source system, last the event document.scanned. A repeated scan_failed
// (the sweep retries a failed version) only refreshes the status; audit
// and event were written by the first.
func (s *Scanner) record(ctx context.Context, orgID string, v *Version, status, signature string) error {
	if s.pool == nil {
		return s.repo.SetScanResult(orgContext(ctx, orgID), orgID, v.DocumentID, v.Version, status, signature)
	}
	scope := database.OrgWideScope(orgID, "")
	return database.WithTenant(database.WithoutTx(ctx), s.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		txCtx := database.ContextWithTx(ctx, tx)
		if err := s.repo.SetScanResult(txCtx, orgID, v.DocumentID, v.Version, status, signature); err != nil {
			return err
		}
		if status == ScanFailed && v.ScanStatus == ScanFailed {
			return nil
		}
		clientID, siteID, err := linkPlacement(ctx, tx, orgID, v.DocumentID)
		if err != nil {
			return err
		}
		if _, err = audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
			OrganizationID: orgID,
			ActorType:      "system",
			Action:         scanAction(status),
			ResourceType:   "document",
			ResourceID:     v.DocumentID,
			Source:         "system",
			Changes: map[string]any{
				"version":        v.Version,
				"version_id":     v.ID,
				"scan_status":    status,
				"scan_signature": signature,
			},
		}); err != nil {
			return fmt.Errorf("audit %s: %w", scanAction(status), err)
		}
		if _, err = events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.DocumentScanned, EntityID: v.DocumentID,
			Data: events.DocumentScannedData{ID: v.DocumentID, DocumentID: v.DocumentID, Version: v.Version, Result: status,
				ScanSignature: signature, UploadedBy: v.UploadedBy, ClientID: clientID, SiteID: siteID}}); err != nil {
			return fmt.Errorf("event %s: %w", events.DocumentScanned, err)
		}
		return nil
	})
}

// linkPlacement returns the client and the site of the document when all
// its links carry the same one (document_link.client_id/site_id); either is
// empty otherwise, and both without links (organization-wide).
func linkPlacement(ctx context.Context, tx pgx.Tx, orgID, docID string) (clientID, siteID string, err error) {
	err = tx.QueryRow(ctx, `
		SELECT CASE WHEN count(*) > 0 AND count(client_id) = count(*) AND count(DISTINCT client_id) = 1
		            THEN min(client_id::text) ELSE '' END,
		       CASE WHEN count(*) > 0 AND count(site_id) = count(*) AND count(DISTINCT site_id) = 1
		            THEN min(site_id::text) ELSE '' END
		  FROM document_link WHERE organization_id = $1 AND document_id = $2`, orgID, docID).Scan(&clientID, &siteID)
	if err != nil {
		return "", "", fmt.Errorf("document link placement: %w", err)
	}
	return clientID, siteID, nil
}

// JobLister lists the jobs of an organization (jobs.Service).
type JobLister interface {
	List(ctx context.Context, orgID string, filter jobs.Filter, page api.PaginationParams) ([]jobs.Job, int, error)
}

// ScanSweeper resubmits the versions without a clean or infected result
// (SEC-09, E-42): pending_scan (a submission that got lost, content from
// phase 1) and scan_failed (the scanner was down).
type ScanSweeper struct {
	repo      Repository
	orgs      func(ctx context.Context) ([]string, error)
	submitter ScanSubmitter
	jobs      JobLister
	now       func() time.Time
}

// NewScanSweeper creates the sweep: orgs lists the organizations
// (database.OrganizationIDs), submitter is the job queue and lister the job
// service that shows the open scan jobs.
func NewScanSweeper(repo Repository, orgs func(ctx context.Context) ([]string, error), submitter ScanSubmitter, lister JobLister) *ScanSweeper {
	return &ScanSweeper{repo: repo, orgs: orgs, submitter: submitter, jobs: lister, now: time.Now}
}

// WithClock sets the clock of the slot (tests).
func (s *ScanSweeper) WithClock(now func() time.Time) *ScanSweeper {
	s.now = now
	return s
}

// Schedule is the scheduler kind document.scan_sweep, every 15 minutes.
func (s *ScanSweeper) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: ScanSweepKind, Spec: "*/15 * * * *", MaxCatchUp: 1, Timeout: 5 * time.Minute, Run: s.SweepOnce}
}

// SweepOnce resubmits, per organization, every open version without a
// queued or running scan job, with the key
// document.scan:<document>:<version>:rescan:<slot UTC> and the uploader as
// creator, so a job.failed reaches the uploader only. It returns the first
// error and goes on with the next organization.
func (s *ScanSweeper) SweepOnce(ctx context.Context) error {
	orgs, err := s.orgs(ctx)
	if err != nil {
		return fmt.Errorf("%s: list organizations: %w", ScanSweepKind, err)
	}
	slot := s.now().UTC().Truncate(scanSweepInterval).Format(time.RFC3339)
	var firstErr error
	for _, orgID := range orgs {
		if _, serr := s.sweepOrganization(ctx, orgID, slot); serr != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: organization %s: %w", ScanSweepKind, orgID, serr)
		}
	}
	return firstErr
}

// sweepOrganization resubmits the open versions of one organization and
// returns how many it submitted.
func (s *ScanSweeper) sweepOrganization(ctx context.Context, orgID, slot string) (int, error) {
	pending, err := s.repo.PendingScans(orgContext(ctx, orgID), orgID, scanSweepBatch)
	if err != nil || len(pending) == 0 {
		return 0, err
	}
	open, err := s.openScans(ctx, orgID)
	if err != nil {
		return 0, err
	}
	submitted := 0
	for i := range pending {
		v := &pending[i]
		if open[scanKey(v.DocumentID, v.Version)] {
			continue
		}
		if _, _, err = s.submitter.Submit(ctx, orgID, ScanJobKind, ScanParams{DocumentID: v.DocumentID, Version: v.Version},
			jobs.EnqueueOptions{IdempotencyKey: scanKey(v.DocumentID, v.Version) + ":rescan:" + slot,
				CreatedBy: uuidOrEmpty(v.UploadedBy)}); err != nil {
			return submitted, fmt.Errorf("submit scan of %s version %d: %w", v.DocumentID, v.Version, err)
		}
		submitted++
	}
	return submitted, nil
}

// openScans returns the versions with a queued or running scan job, keyed
// like scanKey. It lists outside a request (organization-wide).
func (s *ScanSweeper) openScans(ctx context.Context, orgID string) (map[string]bool, error) {
	open := map[string]bool{}
	const pageSize = 200
	for _, status := range []jobs.Status{jobs.StatusQueued, jobs.StatusRunning} {
		for offset := 0; ; offset += pageSize {
			list, _, err := s.jobs.List(ctx, orgID, jobs.Filter{Kind: ScanJobKind, Status: status},
				api.PaginationParams{Limit: pageSize, Offset: offset})
			if err != nil {
				return nil, fmt.Errorf("list open scan jobs: %w", err)
			}
			for i := range list {
				var p ScanParams
				if json.Unmarshal(list[i].Params, &p) == nil {
					open[scanKey(p.DocumentID, p.Version)] = true
				}
			}
			if len(list) < pageSize {
				break
			}
		}
	}
	return open, nil
}
