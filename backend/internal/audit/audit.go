// Package audit provides the audit log with hash-chain integrity.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdvisoryLockID is the lock namespace for audit log serialisation.
const AdvisoryLockID int32 = 0x52657469 // "Reti" in hex

// Entry represents a single audit log entry.
type Entry struct {
	ID             string                 `json:"id"`
	OrganizationID string                 `json:"organization_id"`
	Seq            int64                  `json:"seq"`
	Timestamp      time.Time              `json:"timestamp"`
	ActorID        string                 `json:"actor_id,omitempty"`
	ActorType      string                 `json:"actor_type"`
	Action         string                 `json:"action"`
	ResourceType   string                 `json:"resource_type"`
	ResourceID     string                 `json:"resource_id,omitempty"`
	Changes        map[string]interface{} `json:"changes,omitempty"`
	// Source is where the change came from: ui, api, collector, import or
	// system (AUD-07).
	Source string `json:"source"`
	// RequestID names the request that made the change, CorrelationID ties
	// the entries of one business operation together.
	RequestID     string `json:"request_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	PreviousHash  string `json:"previous_hash"`
	Hash          string `json:"hash"`
}

// VerifyResult reports audit hash-chain verification status.
type VerifyResult struct {
	Intact       bool   `json:"intact"`
	Checked      int    `json:"checked"`
	BrokenID     string `json:"broken_id,omitempty"`
	BrokenAt     int    `json:"broken_at,omitempty"`
	BrokenReason string `json:"broken_reason,omitempty"`
}

// TxRecorder records audit entries inside an existing transaction.
type TxRecorder interface {
	Record(ctx context.Context, tx pgx.Tx, entry Entry) (*Entry, error)
}

// PGRecorder records audit entries in PostgreSQL.
type PGRecorder struct{}

// NewPGRecorder creates a PostgreSQL audit recorder.
func NewPGRecorder() *PGRecorder {
	return &PGRecorder{}
}

// GenesisHash is the previous_hash of the first entry of a chain: 32 zero
// bytes, hex-encoded (AUD-04).
var GenesisHash = strings.Repeat("0", 64)

// ComputeHash computes the hash of an audit entry (AUD-04):
//
//	hash = SHA-256(previous_hash || SHA-256(canonical JSON))
//
// over the binary values, hex-encoded. The canonical JSON is the
// serialization of the persisted row, identical to the database function
// audit_entry_hash (migration 000117): organization_id and seq first, a
// microsecond-precision RFC3339 UTC timestamp, plain ids and the stored
// JSONB changes payload. Because the BEFORE INSERT trigger computes the
// hash from the stored columns, verification over the stored row always
// reproduces the stored hash.
func ComputeHash(entry *Entry) string {
	payload := canonicalHashPayload(
		entry.OrganizationID,
		entry.Seq,
		entry.Timestamp.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z"),
		entry.ActorID,
		entry.ActorType,
		entry.Action,
		entry.ResourceType,
		entry.ResourceID,
		entry.Changes,
		sourceOrDefault(entry.Source),
		entry.RequestID,
		entry.CorrelationID,
	)
	inner := sha256.Sum256([]byte(payload))
	prev, err := hex.DecodeString(entry.PreviousHash)
	if err != nil {
		// Not a hash: the chain is broken anyway; hash the text so the
		// result cannot match a valid entry.
		prev = []byte(entry.PreviousHash)
	}
	h := sha256.Sum256(append(prev, inner[:]...))
	return hex.EncodeToString(h[:])
}

// canonicalHashPayload builds the canonical JSON hashed both here and in
// audit_entry_hash: a JSON object with fixed key order where `changes` is the
// compact JSON serialization of the payload (jsonb-normalized on write).
func canonicalHashPayload(orgID string, seq int64, timestamp, actorID, actorType, action, resourceType, resourceID string,
	changes map[string]interface{}, source, requestID, correlationID string) string {
	changesJSON := "{}"
	if changes != nil {
		if b, err := json.Marshal(changes); err == nil {
			changesJSON = string(b)
		}
	}
	q := func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			return `""`
		}
		return string(b)
	}
	var sb strings.Builder
	sb.WriteString(`{"organization_id":`)
	sb.WriteString(q(orgID))
	sb.WriteString(`,"seq":`)
	sb.WriteString(strconv.FormatInt(seq, 10))
	sb.WriteString(`,"timestamp":`)
	sb.WriteString(q(timestamp))
	sb.WriteString(`,"actor_id":`)
	sb.WriteString(q(actorID))
	sb.WriteString(`,"actor_type":`)
	sb.WriteString(q(actorType))
	sb.WriteString(`,"action":`)
	sb.WriteString(q(action))
	sb.WriteString(`,"resource_type":`)
	sb.WriteString(q(resourceType))
	sb.WriteString(`,"resource_id":`)
	sb.WriteString(q(resourceID))
	sb.WriteString(`,"changes":`)
	sb.WriteString(changesJSON)
	sb.WriteString(`,"source":`)
	sb.WriteString(q(source))
	sb.WriteString(`,"request_id":`)
	sb.WriteString(q(requestID))
	sb.WriteString(`,"correlation_id":`)
	sb.WriteString(q(correlationID))
	sb.WriteString(`}`)
	return sb.String()
}

// AcquireAdvisoryLock acquires the transaction-scoped advisory lock of the
// organization's audit chain. The lock is released when the transaction
// commits or rolls back. The insert trigger of audit_log takes the same lock
// (audit_org_lock_key, migration 000117), so entries are chained in commit
// order.
func AcquireAdvisoryLock(ctx context.Context, tx pgx.Tx, orgID string) error {
	// Use a hash of the org ID as the second lock key for per-tenant serialisation.
	orgHash := sha256.Sum256([]byte(orgID))
	orgKey := int32(orgHash[0])<<24 | int32(orgHash[1])<<16 | int32(orgHash[2])<<8 | int32(orgHash[3])

	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)", int32(AdvisoryLockID), orgKey)
	if err != nil {
		return fmt.Errorf("audit: acquire advisory lock: %w", err)
	}
	return nil
}

// Record appends an audit entry inside tx and returns the persisted entry.
func (r *PGRecorder) Record(ctx context.Context, tx pgx.Tx, entry Entry) (*Entry, error) {
	if entry.OrganizationID == "" {
		return nil, fmt.Errorf("audit: organization_id is required")
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	entry.Timestamp = entry.Timestamp.UTC().Truncate(time.Microsecond)
	if entry.ActorType == "" {
		entry.ActorType = "system"
	}
	if entry.Changes == nil {
		entry.Changes = map[string]interface{}{}
	}
	// Request, correlation and source come from the request that makes the
	// change (AUD-07); an entry of that request marks it as audited.
	if info, ok := requestInfoFrom(ctx); ok {
		if entry.RequestID == "" {
			entry.RequestID = info.RequestID
		}
		if entry.CorrelationID == "" {
			entry.CorrelationID = info.CorrelationID
		}
		if entry.Source == "" {
			entry.Source = info.Source
		}
	}
	MarkRecorded(ctx)
	entry.Source = sourceOrDefault(entry.Source)

	if err := AcquireAdvisoryLock(ctx, tx, entry.OrganizationID); err != nil {
		return nil, err
	}

	// The insert trigger sets seq, previous_hash and hash under the chain
	// lock (migration 000117).
	changesJSON, err := json.Marshal(entry.Changes)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal changes: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO audit_log (
			organization_id,
			timestamp,
			actor_id,
			actor_type,
			action,
			resource_type,
			resource_id,
			changes,
			source,
			request_id,
			correlation_id,
			hash
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, '')
		RETURNING id::text, seq, timestamp, previous_hash, hash
	`, entry.OrganizationID, entry.Timestamp, nilIfEmpty(entry.ActorID), entry.ActorType, entry.Action,
		entry.ResourceType, nilIfEmpty(entry.ResourceID), string(changesJSON), entry.Source,
		nilIfEmpty(entry.RequestID), nilIfEmpty(entry.CorrelationID)).
		Scan(&entry.ID, &entry.Seq, &entry.Timestamp, &entry.PreviousHash, &entry.Hash); err != nil {
		return nil, fmt.Errorf("audit: insert entry: %w", err)
	}

	// The timestamptz column stores microsecond precision while the hash input
	// uses nanoseconds; re-reading the persisted timestamp can therefore yield a
	// value that differs from the hashed one. Truncating after the read keeps
	// the caller-visible timestamp consistent with the stored value and does
	// not alter the hash chain.
	entry.Timestamp = entry.Timestamp.UTC().Truncate(time.Microsecond)

	return &entry, nil
}

// Verify walks the persisted audit chain for an organization and reports integrity.
//
// The chain links every entry of the organization, so the check reads it
// org-wide whoever triggers it (the verify endpoint, the compliance report or
// the audit-verify command); it returns only the integrity result (E-08).
func Verify(ctx context.Context, pool *pgxpool.Pool, orgID string) (VerifyResult, error) {
	scope := database.OrgWideScope(orgID, "")
	var result VerifyResult
	err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = verifyTx(ctx, tx, orgID)
		return err
	})
	return result, err
}

func verifyTx(ctx context.Context, tx pgx.Tx, orgID string) (VerifyResult, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			id::text,
			organization_id::text,
			seq,
			timestamp,
			COALESCE(actor_id::text, ''),
			actor_type,
			action,
			resource_type,
			COALESCE(resource_id::text, ''),
			COALESCE(changes, '{}'::jsonb),
			source,
			COALESCE(request_id, ''),
			COALESCE(correlation_id, ''),
			COALESCE(previous_hash, ''),
			hash,
			audit_entry_hash(organization_id, seq, timestamp, actor_id, actor_type, action, resource_type,
				resource_id, changes, source, request_id, correlation_id, previous_hash)
		FROM audit_log
		WHERE organization_id = $1
		ORDER BY seq ASC
	`, orgID)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("audit: query entries: %w", err)
	}
	defer rows.Close()

	entries, expected, err := scanEntriesWithExpected(rows)
	if err != nil {
		return VerifyResult{}, err
	}
	return verifyEntries(entries, expected), nil
}

// VerifyEntries verifies an in-memory sequence of audit entries in chain
// order. It is used for entries that never touched the database (in-memory
// log, export/import) where the canonical hash input is exactly what
// ComputeHash produces from the entry values.
func VerifyEntries(entries []Entry) VerifyResult {
	expected := make([]string, len(entries))
	for i := range entries {
		expected[i] = ComputeHash(&entries[i])
	}
	return verifyEntries(entries, expected)
}

// verifyEntries checks the sequence, the chain linkage and the per-entry
// hashes and reports the first violation. expected[i] is the hash that the
// entry's stored column values must produce; for database rows it comes from
// audit_entry_hash so the JSONB serialization cannot diverge.
func verifyEntries(entries []Entry, expected []string) VerifyResult {
	prevHash := GenesisHash
	for i := range entries {
		if entries[i].Seq != int64(i+1) {
			return VerifyResult{Intact: false, Checked: i, BrokenID: entries[i].ID, BrokenAt: i + 1, BrokenReason: "sequence gap"}
		}
		if entries[i].PreviousHash != prevHash {
			return VerifyResult{Intact: false, Checked: i, BrokenID: entries[i].ID, BrokenAt: i + 1, BrokenReason: "previous_hash mismatch"}
		}
		if expected[i] != entries[i].Hash {
			return VerifyResult{Intact: false, Checked: i, BrokenID: entries[i].ID, BrokenAt: i + 1, BrokenReason: "hash mismatch"}
		}
		prevHash = entries[i].Hash
	}
	return VerifyResult{Intact: true, Checked: len(entries)}
}

// Handler exposes audit read and verification endpoints.
type Handler struct {
	pool *pgxpool.Pool
}

// NewHandler creates an audit HTTP handler backed by PostgreSQL.
func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

// RegisterRoutes registers audit routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/audit", h.List)
	r.Post("/api/v1/audit/verify", h.Verify)
}

// List handles GET /api/v1/audit.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	filter, err := ParseListFilter(r)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	entries, total, err := h.list(r.Context(), t.OrganizationID, &filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Entry]{
		Data:    entries,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Verify handles POST /api/v1/audit/verify.
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	result, err := Verify(r.Context(), h.pool, t.OrganizationID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, result)
}

// restrictedResource hides entries about resources outside a restricted
// principal's scope: the resource tables' policies filter the subqueries,
// while audit_log carries no client column. Entries about deleted resources
// are hidden from restricted principals as well (fail-closed); org-wide
// principals see the whole log.
const restrictedResource = `CASE audit_log.resource_type
	WHEN 'ci' THEN EXISTS (SELECT 1 FROM ci WHERE ci.id = audit_log.resource_id)
	WHEN 'asset' THEN EXISTS (SELECT 1 FROM asset WHERE asset.id = audit_log.resource_id)
	WHEN 'contact' THEN EXISTS (SELECT 1 FROM contact WHERE contact.id = audit_log.resource_id)
	WHEN 'credential' THEN EXISTS (SELECT 1 FROM credential WHERE credential.id = audit_log.resource_id)
	WHEN 'document' THEN EXISTS (SELECT 1 FROM document WHERE document.id = audit_log.resource_id)
	WHEN 'ticket' THEN EXISTS (SELECT 1 FROM ticket WHERE ticket.id = audit_log.resource_id)
	ELSE true END`

func (h *Handler) list(ctx context.Context, orgID string, filter *ListFilter, page api.PaginationParams) ([]Entry, int, error) {
	var entries []Entry
	var total int
	err := database.WithRequestTenant(ctx, h.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		entries, total, err = listTx(ctx, tx, orgID, filter, page)
		return err
	})
	return entries, total, err
}

func listTx(ctx context.Context, tx pgx.Tx, orgID string, filter *ListFilter, page api.PaginationParams) ([]Entry, int, error) {
	where := "organization_id = $1"
	args := []any{orgID}
	if scope, ok := database.TenantScopeFromContext(ctx); ok &&
		(!scope.Clients.IsAll() || !scope.Sites.IsAll() || !scope.Teams.IsAll()) {
		where += " AND " + restrictedResource
	}
	where, args = filter.where(where, args)

	var total int
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM audit_log WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("audit: count entries: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT
			id::text,
			organization_id::text,
			seq,
			timestamp,
			COALESCE(actor_id::text, ''),
			actor_type,
			action,
			resource_type,
			COALESCE(resource_id::text, ''),
			COALESCE(changes, '{}'::jsonb),
			source,
			COALESCE(request_id, ''),
			COALESCE(correlation_id, ''),
			COALESCE(previous_hash, ''),
			hash
		FROM audit_log
		WHERE `+where+`
		ORDER BY seq DESC
		LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2)+`
	`, append(args, page.Limit, page.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("audit: list entries: %w", err)
	}
	defer rows.Close()

	entries, err := scanEntries(rows)
	if err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

func scanEntries(rows pgx.Rows) ([]Entry, error) {
	entries, _, err := scanEntriesWithExpected(rows)
	return entries, err
}

// scanEntriesWithExpected scans audit rows; when the query projects the
// trailing audit_entry_hash(...) column, the database-computed expected hash
// is returned per row. Callers reading rows without that column (List) get nil.
func scanEntriesWithExpected(rows pgx.Rows) ([]Entry, []string, error) {
	entries := []Entry{}
	expected := []string{}
	hasExpected := len(rows.FieldDescriptions()) == 16
	for rows.Next() {
		var entry Entry
		var changes []byte
		var exp *string
		dest := []any{
			&entry.ID,
			&entry.OrganizationID,
			&entry.Seq,
			&entry.Timestamp,
			&entry.ActorID,
			&entry.ActorType,
			&entry.Action,
			&entry.ResourceType,
			&entry.ResourceID,
			&changes,
			&entry.Source,
			&entry.RequestID,
			&entry.CorrelationID,
			&entry.PreviousHash,
			&entry.Hash,
		}
		if hasExpected {
			dest = append(dest, &exp)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, fmt.Errorf("audit: scan entry: %w", err)
		}
		if len(changes) > 0 {
			if err := json.Unmarshal(changes, &entry.Changes); err != nil {
				return nil, nil, fmt.Errorf("audit: decode changes: %w", err)
			}
		}
		entries = append(entries, entry)
		if exp != nil {
			expected = append(expected, *exp)
		} else {
			expected = append(expected, "")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("audit: iterate entries: %w", err)
	}
	return entries, expected, nil
}

// Log is an in-memory audit log with one chain.
type Log struct {
	entries  []Entry
	lastHash string
}

// NewLog creates a new audit log.
func NewLog() *Log {
	return &Log{lastHash: GenesisHash}
}

// Append adds a new entry to the audit log with hash-chain integrity.
func (l *Log) Append(entry *Entry) {
	if entry.ActorType == "" {
		entry.ActorType = "user"
	}
	entry.Seq = int64(len(l.entries) + 1)
	entry.PreviousHash = l.lastHash
	entry.Hash = ComputeHash(entry)
	l.lastHash = entry.Hash
	l.entries = append(l.entries, *entry)
}

// Entries returns all log entries.
func (l *Log) Entries() []Entry {
	return l.entries
}

// Verify checks the integrity of the hash chain.
func (l *Log) Verify() bool {
	return VerifyEntries(l.entries).Intact
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func sourceOrDefault(source string) string {
	if source == "" {
		return SourceSystem
	}
	return source
}

// Sources of a change (AUD-07).
const (
	SourceUI        = "ui"
	SourceAPI       = "api"
	SourceCollector = "collector"
	SourceImport    = "import"
	SourceSystem    = "system"
)

var validSources = map[string]bool{SourceUI: true, SourceAPI: true, SourceCollector: true, SourceImport: true, SourceSystem: true}

// RequestInfo is the request context of the audit entries a request
// writes: its request and correlation id and the source of the change.
type RequestInfo struct {
	RequestID     string
	CorrelationID string
	Source        string
	recorded      *atomic.Bool
}

type requestInfoKey struct{}

// WithRequestInfo attaches the request context of audit entries to ctx.
func WithRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	info.recorded = &atomic.Bool{}
	return context.WithValue(ctx, requestInfoKey{}, info)
}

func requestInfoFrom(ctx context.Context) (RequestInfo, bool) {
	info, ok := ctx.Value(requestInfoKey{}).(RequestInfo)
	return info, ok && info.recorded != nil
}

// RequestInfoFrom returns the request context of audit entries.
func RequestInfoFrom(ctx context.Context) (RequestInfo, bool) {
	return requestInfoFrom(ctx)
}

// MarkRecorded notes that an audit entry was written for the request of
// ctx; the route audit then writes none of its own.
func MarkRecorded(ctx context.Context) {
	if info, ok := requestInfoFrom(ctx); ok {
		info.recorded.Store(true)
	}
}

// Recorded reports whether an audit entry was written for the request of
// ctx (by a domain recorder, with old and new values).
func Recorded(ctx context.Context) bool {
	info, ok := requestInfoFrom(ctx)
	return ok && info.recorded.Load()
}

// ListFilter narrows GET /api/v1/audit (AUD-05).
type ListFilter struct {
	Action        string
	ResourceType  string
	ResourceID    string
	ActorID       string
	Source        string
	RequestID     string
	CorrelationID string
	From, To      *time.Time
}

// ParseListFilter reads the filter query parameters.
func ParseListFilter(r *http.Request) (ListFilter, error) {
	q := r.URL.Query()
	f := ListFilter{
		Action: q.Get("action"), ResourceType: q.Get("resource_type"), ResourceID: q.Get("resource_id"),
		ActorID: q.Get("actor_id"), Source: q.Get("source"), RequestID: q.Get("request_id"),
		CorrelationID: q.Get("correlation_id"),
	}
	if f.Source != "" && !validSources[f.Source] {
		return ListFilter{}, fmt.Errorf("source must be one of ui, api, collector, import, system")
	}
	for _, id := range []struct{ name, value string }{{"resource_id", f.ResourceID}, {"actor_id", f.ActorID}} {
		if id.value != "" && !IsUUID(id.value) {
			return ListFilter{}, fmt.Errorf("%s must be a UUID", id.name)
		}
	}
	for _, bound := range []struct {
		name string
		dst  **time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(bound.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return ListFilter{}, fmt.Errorf("%s must be an RFC 3339 timestamp", bound.name)
			}
			*bound.dst = &t
		}
	}
	return f, nil
}

// where appends the filter's conditions.
func (f *ListFilter) where(where string, args []any) (clause string, values []any) {
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args)))
	}
	for _, c := range []struct{ cond, value string }{
		{"action = ?", f.Action}, {"resource_type = ?", f.ResourceType}, {"resource_id = ?::uuid", f.ResourceID},
		{"actor_id = ?::uuid", f.ActorID}, {"source = ?", f.Source}, {"request_id = ?", f.RequestID},
		{"correlation_id = ?", f.CorrelationID},
	} {
		if c.value != "" {
			add(c.cond, c.value)
		}
	}
	if f.From != nil {
		add("timestamp >= ?", *f.From)
	}
	if f.To != nil {
		add("timestamp < ?", *f.To)
	}
	return where, args
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s is a UUID (resource and actor ids of entries).
func IsUUID(s string) bool { return uuidPattern.MatchString(s) }
