package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// OrgExportJobKind is the job kind of the full export of an organization
// (TLC-05, WP-1149): every table of the organization as JSONL in one
// snapshot, with a manifest, in the bucket exports.
const OrgExportJobKind = "org.export"

// Limits of the full export (AK3).
const (
	// OrgExportBatchSize is the number of rows read per statement.
	OrgExportBatchSize = 5000
	// OrgExportPartLimit is the largest part of a table in bytes.
	OrgExportPartLimit = 64 << 20
)

// orgExportFormat names the format of the parts in the manifest.
const orgExportFormat = "jsonl"

// Content types of the objects of the full export.
const (
	orgExportPartType     = "application/x-ndjson"
	orgExportManifestType = "application/json"
)

// orgExportPIITable is the name of the decrypted vault in the manifest
// (AK4); its parts lie next to the manifest, not under tables/.
const orgExportPIITable = "pii_subject"

// Errors of the full export; the cause of a failed run goes to the log,
// the job keeps the sanitized text (API-03).
var (
	errOrgExportUnavailable = errors.New("the full export needs the database and the object storage")
	errOrgExportFailed      = errors.New("the full export failed; the tables exported before are kept for the next attempt")
	errOrgExportNoVault     = errors.New("the full export cannot decrypt the personal data: no PII vault is configured")
)

// OrgExportManifest is manifest.json of a full export and the result of
// its job (AK5): per table the rows, the parts with key, bytes and SHA-256
// and the snapshot it was read in. A table is listed once it is complete;
// Pending holds the parts of the table in progress, so a later attempt
// removes them (AK6).
type OrgExportManifest struct {
	OrganizationID string           `json:"organization_id"`
	JobID          string           `json:"job_id"`
	Format         string           `json:"format"`
	StartedAt      time.Time        `json:"started_at"`
	CompletedAt    *time.Time       `json:"completed_at,omitempty"`
	Tables         []OrgExportTable `json:"tables"`
	Pending        *OrgExportTable  `json:"pending,omitempty"`
}

// OrgExportTable is one table of the manifest.
type OrgExportTable struct {
	Name           string          `json:"name"`
	Rows           int64           `json:"rows"`
	Parts          []OrgExportPart `json:"parts"`
	OmittedColumns []string        `json:"omitted_columns,omitempty"`
	// SnapshotAt is the start of the transaction the table was read in,
	// Snapshot its pg_current_snapshot(): tables of one attempt share it,
	// a resumed attempt reads the rest in a new one (AK6).
	SnapshotAt time.Time `json:"snapshot_at"`
	Snapshot   string    `json:"snapshot"`
}

// OrgExportPart is one object of a table.
type OrgExportPart struct {
	Key    string `json:"key"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// done reports whether the table is complete in the manifest.
func (m *OrgExportManifest) done(name string) bool {
	for i := range m.Tables {
		if m.Tables[i].Name == name {
			return true
		}
	}
	return false
}

// keys lists every object the manifest names, the manifest not included.
func (m *OrgExportManifest) keys() []string {
	var out []string
	for i := range m.Tables {
		for _, p := range m.Tables[i].Parts {
			out = append(out, p.Key)
		}
	}
	if m.Pending != nil {
		for _, p := range m.Pending.Parts {
			out = append(out, p.Key)
		}
	}
	return out
}

// OrgExportProgress records the progress of a job; jobs.Service
// implements it.
type OrgExportProgress interface {
	Progress(ctx context.Context, orgID, id string, percent int) error
}

// OrgExporter runs the jobs org.export (AK3 to AK6). It reads in a tenant
// transaction of the organization, REPEATABLE READ and READ ONLY
// (database.WithTenantSnapshot), so every table of one attempt shows the
// same state, and writes the parts below
// org/<org>/org-export/<job>/ in the bucket exports.
type OrgExporter struct {
	pool      *pgxpool.Pool
	blobs     blob.Store
	bucket    string
	progress  OrgExportProgress
	batchSize int
	partLimit int
	now       func() time.Time
	afterStep func(ctx context.Context, table string) error
}

// NewOrgExporter builds the exporter on the application pool and the
// object storage; progress may be nil. Without pool or storage its jobs
// fail.
func NewOrgExporter(pool *pgxpool.Pool, blobs blob.Store, progress OrgExportProgress) *OrgExporter {
	return &OrgExporter{pool: pool, blobs: blobs, bucket: ExportBucket, progress: progress,
		batchSize: OrgExportBatchSize, partLimit: OrgExportPartLimit, now: time.Now}
}

// WithLimits sets the rows per batch and the bytes per part (tests).
func (e *OrgExporter) WithLimits(batchSize, partLimit int) *OrgExporter {
	if batchSize > 0 {
		e.batchSize = batchSize
	}
	if partLimit > 0 {
		e.partLimit = partLimit
	}
	return e
}

// WithStepHook runs fn after every batch and every completed table with
// the name of the table (tests): an error ends the attempt like a crash
// of the process, the objects of the job are kept.
func (e *OrgExporter) WithStepHook(fn func(ctx context.Context, table string) error) *OrgExporter {
	e.afterStep = fn
	return e
}

// OrgExportPrefix is the key prefix of the objects of a full export.
func OrgExportPrefix(orgID, jobID string) string {
	return blob.OrgKey(orgID, "org-export", jobID)
}

// OrgExportManifestKey is the key of manifest.json of a full export.
func OrgExportManifestKey(orgID, jobID string) string {
	return OrgExportPrefix(orgID, jobID) + "/manifest.json"
}

// cancelled reports whether the job was cancelled by its user (JOB-03):
// the run context ends with the cause jobs.ErrCancelled.
func cancelled(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), jobs.ErrCancelled)
}

// RunJob runs one attempt of a job org.export. A later attempt after a
// crash keeps the tables the manifest lists as complete and exports the
// others in a new snapshot (AK6). A cancel by the user ends the attempt
// before its next batch and removes the objects of the job, as does the
// failure of the last attempt.
func (e *OrgExporter) RunJob(ctx context.Context, job *jobs.Job) (any, error) {
	if e == nil || e.pool == nil || e.blobs == nil {
		return nil, errOrgExportUnavailable
	}
	orgID := job.OrganizationID
	manifest, err := e.loadManifest(ctx, orgID, job.ID)
	if err != nil {
		slog.ErrorContext(ctx, "org export: read manifest", "job", job.ID, "error", err)
		return nil, errOrgExportFailed
	}
	if manifest == nil {
		manifest = &OrgExportManifest{OrganizationID: orgID, JobID: job.ID, Format: orgExportFormat, StartedAt: e.now().UTC()}
	}
	// The parts of a table an earlier attempt did not complete go; the
	// table is exported again.
	if manifest.Pending != nil {
		for _, p := range manifest.Pending.Parts {
			if derr := e.blobs.Delete(ctx, e.bucket, p.Key); derr != nil && !errors.Is(derr, blob.ErrNotFound) {
				slog.WarnContext(ctx, "org export: remove part of an incomplete table", "job", job.ID, "error", derr)
			}
		}
		manifest.Pending = nil
	}
	if cancelled(ctx) {
		return nil, e.discard(ctx, manifest)
	}
	scope := database.OrgWideScope(orgID, job.CreatedBy)
	err = database.WithTenantSnapshot(ctx, e.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		return e.run(ctx, tx, manifest)
	})
	if cancelled(ctx) {
		return nil, e.discard(ctx, manifest)
	}
	if err != nil {
		slog.ErrorContext(ctx, "org export failed", "job", job.ID, "organization_id", orgID, "attempt", job.Attempts, "error", err)
		// The last attempt leaves no part behind: the parts hold personal
		// data decrypted (E-165), and a failed job gets no expiry the
		// cleanup of the exports would follow.
		if job.MaxAttempts > 0 && job.Attempts >= job.MaxAttempts {
			e.removeObjects(ctx, manifest)
		}
		if errors.Is(err, errOrgExportNoVault) {
			return nil, errOrgExportNoVault
		}
		return nil, errOrgExportFailed
	}
	finished := e.now().UTC()
	manifest.CompletedAt = &finished
	if err := e.saveManifest(ctx, manifest); err != nil {
		slog.ErrorContext(ctx, "org export: write manifest", "job", job.ID, "error", err)
		return nil, errOrgExportFailed
	}
	return manifest, nil
}

// run exports every table the manifest does not list yet in the snapshot
// of tx.
func (e *OrgExporter) run(ctx context.Context, tx pgx.Tx, m *OrgExportManifest) error {
	var snapshotAt time.Time
	var snapshot string
	if err := tx.QueryRow(ctx, `SELECT now(), pg_current_snapshot()::text`).Scan(&snapshotAt, &snapshot); err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	tables, err := OrgTables(ctx, tx)
	if err != nil {
		return err
	}
	total := len(tables) + 1
	report := func() {
		if e.progress == nil {
			return
		}
		pct := min(99, (len(m.Tables)*100)/total)
		if perr := e.progress.Progress(context.WithoutCancel(ctx), m.OrganizationID, m.JobID, pct); perr != nil {
			slog.WarnContext(ctx, "org export: progress", "job", m.JobID, "error", perr)
		}
	}
	report()
	steps := make([]func() (*OrgExportTable, error), 0, total)
	for i := range tables {
		t := tables[i]
		if m.done(t.Name) {
			continue
		}
		steps = append(steps, func() (*OrgExportTable, error) { return e.exportTable(ctx, tx, m, &t) })
	}
	if !m.done(orgExportPIITable) {
		steps = append(steps, func() (*OrgExportTable, error) { return e.exportPII(ctx, tx, m) })
	}
	for _, step := range steps {
		if cancelled(ctx) {
			return jobs.ErrCancelled
		}
		entry, err := step()
		if err != nil {
			return err
		}
		entry.SnapshotAt, entry.Snapshot = snapshotAt.UTC(), snapshot
		m.Tables = append(m.Tables, *entry)
		m.Pending = nil
		if err := e.saveManifest(ctx, m); err != nil {
			return err
		}
		report()
		if err := e.step(ctx, entry.Name); err != nil {
			return err
		}
	}
	return nil
}

// step runs the hook of the tests.
func (e *OrgExporter) step(ctx context.Context, table string) error {
	if e.afterStep == nil {
		return nil
	}
	return e.afterStep(ctx, table)
}

// partWriter collects the lines of a table into parts of at most
// partLimit bytes (a single longer line is a part of its own) and stores
// each full part, recording it in the manifest as pending.
type partWriter struct {
	e     *OrgExporter
	ctx   context.Context
	m     *OrgExportManifest
	entry *OrgExportTable
	key   func(n int) string
	buf   bytes.Buffer
}

func (w *partWriter) line(raw []byte) error {
	if w.buf.Len() > 0 && w.buf.Len()+len(raw)+1 > w.e.partLimit {
		if err := w.flush(); err != nil {
			return err
		}
	}
	w.buf.Write(raw)
	w.buf.WriteByte('\n')
	w.entry.Rows++
	return nil
}

// flush records the part in the manifest as pending before it stores it:
// a crash between the two leaves no part the next attempt or a cancel does
// not know (a delete of a part never stored is not found and ignored).
func (w *partWriter) flush() error {
	if w.buf.Len() == 0 {
		return nil
	}
	key := w.key(len(w.entry.Parts) + 1)
	data := w.buf.Bytes()
	sum := sha256.Sum256(data)
	w.entry.Parts = append(w.entry.Parts, OrgExportPart{Key: key, Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	pending := *w.entry
	pending.Parts = append([]OrgExportPart(nil), w.entry.Parts...)
	w.m.Pending = &pending
	if err := w.e.saveManifest(w.ctx, w.m); err != nil {
		return err
	}
	if err := w.e.blobs.Put(w.ctx, w.e.bucket, key, bytes.NewReader(data), int64(len(data)), orgExportPartType); err != nil {
		return fmt.Errorf("store %s: %w", key, err)
	}
	w.buf.Reset()
	return nil
}

// exportTable writes the rows of the organization of one table in batches
// of batchSize: by primary key, without one by (tableoid, ctid) through a
// cursor of the snapshot; global rows (NULL) stay out. The surrogate
// references in the rows are revealed (E-165).
func (e *OrgExporter) exportTable(ctx context.Context, tx pgx.Tx, m *OrgExportManifest, t *OrgTable) (*OrgExportTable, error) {
	entry := &OrgExportTable{Name: t.Name, Parts: []OrgExportPart{}, OmittedColumns: t.Omitted}
	prefix := OrgExportPrefix(m.OrganizationID, m.JobID) + "/tables/" + t.Name
	w := &partWriter{e: e, ctx: ctx, m: m, entry: entry, key: func(n int) string { return fmt.Sprintf("%s/part-%04d.jsonl", prefix, n) }}
	var next func() ([][]byte, error)
	if len(t.PrimaryKey) > 0 {
		next = e.keysetBatches(ctx, tx, m.OrganizationID, t)
	} else {
		var closeCursor func()
		var err error
		if next, closeCursor, err = e.cursorBatches(ctx, tx, m.OrganizationID, t); err != nil {
			return nil, err
		}
		defer closeCursor()
	}
	for {
		if cancelled(ctx) {
			return nil, jobs.ErrCancelled
		}
		lines, err := next()
		if err != nil {
			return nil, err
		}
		if lines, err = revealLines(ctx, tx, m.OrganizationID, lines); err != nil {
			return nil, fmt.Errorf("reveal %s: %w", t.Name, err)
		}
		for _, l := range lines {
			if err := w.line(l); err != nil {
				return nil, err
			}
		}
		if len(lines) < e.batchSize {
			break
		}
		if err := e.step(ctx, t.Name); err != nil {
			return nil, err
		}
	}
	if err := w.flush(); err != nil {
		return nil, err
	}
	return entry, nil
}

// orgRowSQL is the select list and source of the rows of t: row_to_json of
// the exported columns (the secret ones are not selected, AK2) of the rows
// whose organization column is $1.
func orgRowSQL(t *OrgTable) string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = "t." + ident(c)
	}
	return "row_to_json(r)::text FROM public." + ident(t.Source) + " t CROSS JOIN LATERAL (SELECT " +
		strings.Join(cols, ", ") + ") r WHERE t." + ident(t.OrgColumn) + " = $1"
}

func ident(s string) string { return pgx.Identifier{s}.Sanitize() }

// keysetBatches reads t by its primary key: $2 the batch size, from $3 on
// the key of the last row of the previous batch.
func (e *OrgExporter) keysetBatches(ctx context.Context, tx pgx.Tx, orgID string, t *OrgTable) func() ([][]byte, error) {
	keys := make([]string, len(t.PrimaryKey))
	keyText := make([]string, len(t.PrimaryKey))
	casts := make([]string, len(t.PrimaryKey))
	for i, k := range t.PrimaryKey {
		keys[i] = "t." + ident(k.Name)
		keyText[i] = keys[i] + "::text"
		casts[i] = fmt.Sprintf("$%d::%s", i+3, k.Type)
	}
	base := "SELECT ARRAY[" + strings.Join(keyText, ", ") + "], " + orgRowSQL(t)
	order := " ORDER BY " + strings.Join(keys, ", ") + " LIMIT $2"
	first := base + order
	following := base + " AND (" + strings.Join(keys, ", ") + ") > (" + strings.Join(casts, ", ") + ")" + order
	var last []string
	return func() ([][]byte, error) {
		q, args := first, []any{orgID, e.batchSize}
		if last != nil {
			q = following
			for _, k := range last {
				args = append(args, k)
			}
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", t.Name, err)
		}
		defer rows.Close()
		var lines [][]byte
		for rows.Next() {
			var key []string
			var line string
			if err := rows.Scan(&key, &line); err != nil {
				return nil, fmt.Errorf("read %s: %w", t.Name, err)
			}
			if len(key) != len(keys) {
				return nil, fmt.Errorf("read %s: key has %d columns, want %d", t.Name, len(key), len(keys))
			}
			lines = append(lines, []byte(line))
			last = key
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read %s: %w", t.Name, err)
		}
		return lines, nil
	}
}

// cursorBatches reads a table without primary key through a cursor of the
// snapshot transaction, ordered by (tableoid, ctid); a view (FromView) has
// no ctid and is read in its own order.
func (e *OrgExporter) cursorBatches(ctx context.Context, tx pgx.Tx, orgID string, t *OrgTable) (next func() ([][]byte, error), closeCursor func(), err error) {
	order := " ORDER BY t.tableoid, t.ctid"
	if t.FromView() {
		order = ""
	}
	cursor := ident("org_export_" + t.Name)
	if _, err := tx.Exec(ctx, "DECLARE "+cursor+" NO SCROLL CURSOR FOR SELECT "+orgRowSQL(t)+order, orgID); err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", t.Name, err)
	}
	fetch := fmt.Sprintf("FETCH FORWARD %d FROM %s", e.batchSize, cursor)
	next = func() ([][]byte, error) {
		rows, err := tx.Query(ctx, fetch)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", t.Name, err)
		}
		defer rows.Close()
		var lines [][]byte
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return nil, fmt.Errorf("read %s: %w", t.Name, err)
			}
			lines = append(lines, []byte(line))
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read %s: %w", t.Name, err)
		}
		return lines, nil
	}
	closeCursor = func() {
		if _, cerr := tx.Exec(ctx, "CLOSE "+cursor); cerr != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "org export: close cursor", "table", t.Name, "error", cerr)
		}
	}
	return next, closeCursor, nil
}

// revealLines replaces the surrogate references ({"$pii": id}) of the
// lines with the values they stand for, in one vault read per batch
// (E-165); a deleted subject becomes null. Lines without a reference stay
// byte for byte. Without a vault a reference fails the attempt
// (errOrgExportNoVault) instead of becoming null: the table would be
// complete in the manifest with its values lost.
func revealLines(ctx context.Context, tx pgx.Tx, orgID string, lines [][]byte) ([][]byte, error) {
	var idx []int
	var docs []any
	for i, l := range lines {
		if !bytes.Contains(l, []byte(`"`+pii.RefKey+`"`)) {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(l))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		idx = append(idx, i)
		docs = append(docs, v)
	}
	if len(docs) == 0 {
		return lines, nil
	}
	if pii.Default() == nil && hasRef(docs) {
		return nil, errOrgExportNoVault
	}
	revealed, err := pii.RevealTx(ctx, tx, orgID, docs)
	if err != nil {
		return nil, err
	}
	list, _ := revealed.([]any)
	for j, i := range idx {
		raw, merr := json.Marshal(list[j])
		if merr != nil {
			return nil, merr
		}
		lines[i] = raw
	}
	return lines, nil
}

// hasRef reports whether v holds a reference to a subject (an empty id is
// a value redacted without vault, it stays null).
func hasRef(v any) bool {
	if id, ok := pii.RefID(v); ok {
		return id != ""
	}
	switch t := v.(type) {
	case map[string]any:
		for _, c := range t {
			if hasRef(c) {
				return true
			}
		}
	case []any:
		for _, c := range t {
			if hasRef(c) {
				return true
			}
		}
	}
	return false
}

// piiLine is one line of pii_subject.jsonl (AK4).
type piiLine struct {
	SurrogateID string   `json:"surrogate_id"`
	Kind        string   `json:"kind"`
	ParentID    *string  `json:"parent_id"`
	CreatedAt   string   `json:"created_at"`
	Data        pii.Data `json:"data"`
}

// exportPII writes the subjects of the vault decrypted to pii_subject.jsonl
// next to the manifest (AK4); their ciphertext, blind index and data key
// never leave the database.
func (e *OrgExporter) exportPII(ctx context.Context, tx pgx.Tx, m *OrgExportManifest) (*OrgExportTable, error) {
	entry := &OrgExportTable{Name: orgExportPIITable, Parts: []OrgExportPart{}}
	prefix := OrgExportPrefix(m.OrganizationID, m.JobID)
	w := &partWriter{e: e, ctx: ctx, m: m, entry: entry, key: func(n int) string {
		if n == 1 {
			return prefix + "/pii_subject.jsonl"
		}
		return fmt.Sprintf("%s/pii_subject-part-%04d.jsonl", prefix, n)
	}}
	after := "00000000-0000-0000-0000-000000000000"
	for {
		if cancelled(ctx) {
			return nil, jobs.ErrCancelled
		}
		rows, err := tx.Query(ctx, `SELECT surrogate_id::text, kind, parent_id::text, to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
			FROM pii_subject WHERE organization_id = $1 AND surrogate_id > $2::uuid ORDER BY surrogate_id LIMIT $3`,
			m.OrganizationID, after, e.batchSize)
		if err != nil {
			return nil, fmt.Errorf("read pii_subject: %w", err)
		}
		batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (piiLine, error) {
			var l piiLine
			serr := r.Scan(&l.SurrogateID, &l.Kind, &l.ParentID, &l.CreatedAt)
			return l, serr
		})
		if err != nil {
			return nil, fmt.Errorf("read pii_subject: %w", err)
		}
		if len(batch) > 0 {
			vault := pii.Default()
			if vault == nil {
				return nil, errOrgExportNoVault
			}
			ids := make([]string, len(batch))
			for i := range batch {
				ids[i] = batch[i].SurrogateID
			}
			subjects, rerr := vault.ResolveTx(ctx, tx, m.OrganizationID, ids)
			if rerr != nil {
				return nil, fmt.Errorf("decrypt pii_subject: %w", rerr)
			}
			for i := range batch {
				if s := subjects[batch[i].SurrogateID]; s != nil {
					batch[i].Data = s.Data
				}
				raw, merr := json.Marshal(&batch[i])
				if merr != nil {
					return nil, merr
				}
				if err := w.line(raw); err != nil {
					return nil, err
				}
			}
			after = batch[len(batch)-1].SurrogateID
		}
		if len(batch) < e.batchSize {
			break
		}
		if err := e.step(ctx, orgExportPIITable); err != nil {
			return nil, err
		}
	}
	if err := w.flush(); err != nil {
		return nil, err
	}
	return entry, nil
}

// loadManifest reads manifest.json of the job; nil when there is none.
func (e *OrgExporter) loadManifest(ctx context.Context, orgID, jobID string) (*OrgExportManifest, error) {
	key := OrgExportManifestKey(orgID, jobID)
	exists, err := blob.Exists(ctx, e.blobs, e.bucket, key)
	if err != nil || !exists {
		return nil, err
	}
	rc, err := e.blobs.Get(ctx, e.bucket, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var m OrgExportManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.OrganizationID != orgID || m.JobID != jobID {
		return nil, fmt.Errorf("manifest %s belongs to another job", key)
	}
	return &m, nil
}

// saveManifest stores manifest.json of the job.
func (e *OrgExporter) saveManifest(ctx context.Context, m *OrgExportManifest) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	key := OrgExportManifestKey(m.OrganizationID, m.JobID)
	if err := e.blobs.Put(ctx, e.bucket, key, bytes.NewReader(raw), int64(len(raw)), orgExportManifestType); err != nil {
		return fmt.Errorf("store manifest: %w", err)
	}
	return nil
}

// discard removes every object of a cancelled job (JOB-03) and returns
// jobs.ErrCancelled; the run context has ended, the deletes run without
// its cancel.
func (e *OrgExporter) discard(ctx context.Context, m *OrgExportManifest) error {
	e.removeObjects(ctx, m)
	return jobs.ErrCancelled
}

// removeObjects deletes every part the manifest lists (complete and
// pending) and the manifest, without the cancel of ctx.
func (e *OrgExporter) removeObjects(ctx context.Context, m *OrgExportManifest) {
	ctx = context.WithoutCancel(ctx)
	for _, key := range append(m.keys(), OrgExportManifestKey(m.OrganizationID, m.JobID)) {
		if err := e.blobs.Delete(ctx, e.bucket, key); err != nil && !errors.Is(err, blob.ErrNotFound) {
			slog.WarnContext(ctx, "org export: remove object of the job", "job", m.JobID, "error", err)
		}
	}
}
