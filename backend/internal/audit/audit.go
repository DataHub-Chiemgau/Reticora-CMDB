// Package audit provides the audit log with hash-chain integrity.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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
	Timestamp      time.Time              `json:"timestamp"`
	ActorID        string                 `json:"actor_id,omitempty"`
	ActorType      string                 `json:"actor_type"`
	Action         string                 `json:"action"`
	ResourceType   string                 `json:"resource_type"`
	ResourceID     string                 `json:"resource_id,omitempty"`
	Changes        map[string]interface{} `json:"changes,omitempty"`
	PreviousHash   string                 `json:"previous_hash"`
	Hash           string                 `json:"hash"`
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

// ComputeHash computes the SHA-256 hash for an audit entry, chaining from the previous hash.
func ComputeHash(entry *Entry) string {
	data := struct {
		Timestamp    string                 `json:"timestamp"`
		ActorID      string                 `json:"actor_id"`
		ActorType    string                 `json:"actor_type"`
		Action       string                 `json:"action"`
		ResourceType string                 `json:"resource_type"`
		ResourceID   string                 `json:"resource_id"`
		Changes      map[string]interface{} `json:"changes,omitempty"`
		PreviousHash string                 `json:"previous_hash"`
	}{
		Timestamp:    entry.Timestamp.UTC().Format(time.RFC3339Nano),
		ActorID:      entry.ActorID,
		ActorType:    entry.ActorType,
		Action:       entry.Action,
		ResourceType: entry.ResourceType,
		ResourceID:   entry.ResourceID,
		Changes:      entry.Changes,
		PreviousHash: entry.PreviousHash,
	}

	b, _ := json.Marshal(data)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// AcquireAdvisoryLock acquires a transaction-scoped advisory lock for audit log serialisation.
// The lock is automatically released when the transaction commits or rolls back.
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

	if err := AcquireAdvisoryLock(ctx, tx, entry.OrganizationID); err != nil {
		return nil, err
	}

	entry.PreviousHash = ""
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(hash, '')
		FROM audit_log
		WHERE organization_id = $1
		ORDER BY timestamp DESC, id DESC
		LIMIT 1
	`, entry.OrganizationID).Scan(&entry.PreviousHash); err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("audit: read previous hash: %w", err)
	}

	entry.Hash = ComputeHash(&entry)
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
			previous_hash,
			hash
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)
		RETURNING id::text, timestamp
	`, entry.OrganizationID, entry.Timestamp, nilIfEmpty(entry.ActorID), entry.ActorType, entry.Action,
		entry.ResourceType, nilIfEmpty(entry.ResourceID), string(changesJSON), entry.PreviousHash, entry.Hash).
		Scan(&entry.ID, &entry.Timestamp); err != nil {
		return nil, fmt.Errorf("audit: insert entry: %w", err)
	}

	return &entry, nil
}

// Verify walks the persisted audit chain for an organization and reports integrity.
func Verify(ctx context.Context, pool *pgxpool.Pool, orgID string) (VerifyResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("audit: begin verify transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return VerifyResult{}, fmt.Errorf("audit: set tenant context: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT
			id::text,
			organization_id::text,
			timestamp,
			COALESCE(actor_id::text, ''),
			actor_type,
			action,
			resource_type,
			COALESCE(resource_id::text, ''),
			COALESCE(changes, '{}'::jsonb),
			COALESCE(previous_hash, ''),
			hash
		FROM audit_log
		WHERE organization_id = $1
		ORDER BY timestamp ASC, id ASC
	`, orgID)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("audit: query entries: %w", err)
	}
	defer rows.Close()

	entries, err := scanEntries(rows)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyEntries(entries), nil
}

// VerifyEntries verifies an in-memory sequence of audit entries in chain order.
func VerifyEntries(entries []Entry) VerifyResult {
	prevHash := ""
	for i := range entries {
		if entries[i].PreviousHash != prevHash {
			return VerifyResult{Intact: false, Checked: i, BrokenID: entries[i].ID, BrokenAt: i + 1, BrokenReason: "previous_hash mismatch"}
		}
		computed := ComputeHash(&entries[i])
		if computed != entries[i].Hash {
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
	entries, total, err := h.list(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
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
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) list(ctx context.Context, orgID string, page api.PaginationParams) ([]Entry, int, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("audit: begin list transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return nil, 0, fmt.Errorf("audit: set tenant context: %w", err)
	}

	var total int
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM audit_log WHERE organization_id = $1", orgID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("audit: count entries: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT
			id::text,
			organization_id::text,
			timestamp,
			COALESCE(actor_id::text, ''),
			actor_type,
			action,
			resource_type,
			COALESCE(resource_id::text, ''),
			COALESCE(changes, '{}'::jsonb),
			COALESCE(previous_hash, ''),
			hash
		FROM audit_log
		WHERE organization_id = $1
		ORDER BY timestamp DESC, id DESC
		LIMIT $2 OFFSET $3
	`, orgID, page.Limit, page.Offset)
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
	entries := []Entry{}
	for rows.Next() {
		var entry Entry
		var changes []byte
		if err := rows.Scan(
			&entry.ID,
			&entry.OrganizationID,
			&entry.Timestamp,
			&entry.ActorID,
			&entry.ActorType,
			&entry.Action,
			&entry.ResourceType,
			&entry.ResourceID,
			&changes,
			&entry.PreviousHash,
			&entry.Hash,
		); err != nil {
			return nil, fmt.Errorf("audit: scan entry: %w", err)
		}
		if len(changes) > 0 {
			if err := json.Unmarshal(changes, &entry.Changes); err != nil {
				return nil, fmt.Errorf("audit: decode changes: %w", err)
			}
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: iterate entries: %w", err)
	}
	return entries, nil
}

// Log is an in-memory audit log.
type Log struct {
	entries  []Entry
	lastHash string
}

// NewLog creates a new audit log.
func NewLog() *Log {
	return &Log{}
}

// Append adds a new entry to the audit log with hash-chain integrity.
func (l *Log) Append(entry *Entry) {
	if entry.ActorType == "" {
		entry.ActorType = "user"
	}
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
