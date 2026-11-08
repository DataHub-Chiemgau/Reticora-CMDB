package importer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// Limits of the API import (E-49, WP-303).
const (
	// MaxAPIRows is the largest number of JSON rows in one request.
	MaxAPIRows = 5_000
	// MaxAPIBodyBytes is the largest body of POST /api/v1/imports, 16 MiB.
	MaxAPIBodyBytes = 16 << 20
)

// SourceAPI is the import_source of a run over JSON rows in job.params
// and the source of its projection; a run of a document has none in its
// parameters.
const SourceAPI = "api"

// CodeStagingUnavailable: the staged rows of an API run are missing or do
// not match rows_sha256; the run writes no row.
const CodeStagingUnavailable = "import-staging-unavailable"

// StagingSweepKind is the system job that deletes the staged rows of runs
// that ended without a run (WP-303).
const StagingSweepKind = "import.staging.sweep"

// stagingWindow is how far back the sweep looks at finished jobs.
const stagingWindow = 7 * 24 * time.Hour

// StagingKey is the blob key of the staged rows of an API import in the
// bucket export.ExportBucket, below the reports of the runs. id is drawn
// by the server: the job id does not exist before the job is queued, and
// a queued job is announced at once (JOB-01).
func StagingKey(orgID, id string) string {
	return blob.OrgKey(orgID, "imports", "staging", id, "rows.json")
}

// ownsStagingKey reports whether key is a staging key of the organization.
func ownsStagingKey(orgID, key string) bool {
	return blob.CheckOrgKey(orgID, key) == nil && strings.HasPrefix(key, blob.OrgKey(orgID, "imports", "staging")+"/") &&
		strings.HasSuffix(key, "/rows.json")
}

// newStagingID draws a random UUID (version 4) for a staging object.
func newStagingID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// rowsDigest is the hex SHA-256 of the staged rows (rows_sha256).
func rowsDigest(rows []byte) string {
	sum := sha256.Sum256(rows)
	return hex.EncodeToString(sum[:])
}

// stageRows writes the rows as a new staging object, outside any database
// transaction (K20), and returns its key.
func (h *ImportHandler) stageRows(ctx context.Context, orgID string, rows []byte) (string, error) {
	id, err := newStagingID()
	if err != nil {
		return "", err
	}
	key := StagingKey(orgID, id)
	if err := h.blobs.Put(ctx, export.ExportBucket, key, bytes.NewReader(rows), int64(len(rows)), "application/json"); err != nil {
		return "", err
	}
	return key, nil
}

// dropStaged deletes a staging object; a missing object counts as deleted
// (blob.FileStore reports it as an error).
func dropStaged(ctx context.Context, store blob.Store, orgID, key string) error {
	if !ownsStagingKey(orgID, key) {
		return blob.ErrForeignKey
	}
	if err := store.Delete(ctx, export.ExportBucket, key); err != nil && !errors.Is(err, blob.ErrNotFound) {
		return err
	}
	return nil
}

// dropStagedLogged is dropStaged with the error logged.
func (h *ImportHandler) dropStagedLogged(ctx context.Context, orgID, key string) {
	if err := dropStaged(ctx, h.blobs, orgID, key); err != nil {
		slog.WarnContext(ctx, "import: delete staged rows", "error", err)
	}
}

// loadStaged reads the staged rows of an API run, outside any database
// transaction (K20), and checks them against rows_sha256.
func (h *ImportHandler) loadStaged(ctx context.Context, orgID string, p *JobParams) ([]byte, error) {
	unavailable := &ParseError{Code: CodeStagingUnavailable, Message: "the rows of the import are no longer available", Fatal: true}
	if !ownsStagingKey(orgID, p.StagingKey) || p.RowsSHA256 == "" {
		return nil, unavailable
	}
	body, err := h.blobs.Get(ctx, export.ExportBucket, p.StagingKey)
	if err != nil {
		slog.WarnContext(ctx, "import: read staged rows", "error", err)
		return nil, unavailable
	}
	defer func() { _ = body.Close() }()
	rows, err := io.ReadAll(io.LimitReader(body, MaxAPIBodyBytes+1))
	if err != nil || len(rows) > MaxAPIBodyBytes || rowsDigest(rows) != p.RowsSHA256 {
		return nil, unavailable
	}
	return rows, nil
}

// jobIdempotencyKey is the job key of a start with the header
// Idempotency-Key (WP-303): hex(sha256) over principal type, subject, the
// header value and the SHA-256 of the request body. The header value is
// never stored; with the body hash the same header with another body gives
// another job, also after the 24 hours of the HTTP idempotency. Empty
// without header.
func jobIdempotencyKey(ctx context.Context, header string, body []byte) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	p, _ := identity.PrincipalFromContext(ctx)
	bodySum := sha256.Sum256(body)
	h := sha256.New()
	for _, part := range []string{string(p.Type), p.Subject, header, hex.EncodeToString(bodySum[:])} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// StagingSweep deletes the staged rows of import jobs that ended without
// deleting them themselves: jobs cancelled before their start and jobs
// the reaper failed (import.staging.sweep, @every 1h). A run deletes its
// staged rows when it ends for good (perform).
type StagingSweep struct {
	pool  *pgxpool.Pool
	blobs blob.Store
	now   func() time.Time
}

// NewStagingSweep returns the sweep over pool and the blob store.
func NewStagingSweep(pool *pgxpool.Pool, blobs blob.Store) *StagingSweep {
	return &StagingSweep{pool: pool, blobs: blobs, now: func() time.Time { return time.Now().UTC() }}
}

// Schedule registers the sweep every hour.
func (s *StagingSweep) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: StagingSweepKind, Spec: "@every 1h", Run: func(ctx context.Context) error {
		_, err := s.Run(ctx)
		return err
	}}
}

// Run sweeps every organization (database.OrganizationIDs, a system
// read); a failing organization does not stop the others. It returns the
// staging objects deleted (or already missing).
func (s *StagingSweep) Run(ctx context.Context) (int, error) {
	orgs, err := database.OrganizationIDs(ctx, s.pool)
	if err != nil {
		return 0, err
	}
	total := 0
	var errs []error
	for _, org := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		n, oerr := s.RunOrg(ctx, org)
		total += n
		if oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
		}
	}
	return total, errors.Join(errs...)
}

// RunOrg reads, in a tenant transaction of the organization, the staging
// keys of the import jobs in an end state that finished within the last 7
// days, and deletes their objects after the transaction (K20).
func (s *StagingSweep) RunOrg(ctx context.Context, orgID string) (int, error) {
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	var keys []string
	err := database.WithTenant(ctx, s.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT params->>'staging_key' FROM job
			WHERE organization_id = $1 AND kind = $2 AND status IN ('succeeded', 'failed', 'cancelled')
			  AND params ? 'staging_key' AND finished_at >= $3`, orgID, JobKind, s.now().Add(-stagingWindow))
		if err != nil {
			return fmt.Errorf("list import jobs: %w", err)
		}
		keys, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, key := range keys {
		if derr := dropStaged(ctx, s.blobs, orgID, key); derr != nil {
			errs = append(errs, derr)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}
