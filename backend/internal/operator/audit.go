// Package operator implements the operator path /api/v1/admin (SEC-07):
// personalized operator identities from the IdP group "operators" with MFA,
// the break-glass token RETICORA_OPERATOR_TOKEN whose every use raises an
// alarm, and the organization-spanning operator_audit with a hash chain of
// its own.
package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// genesisHash is the previous hash of the first entry.
const genesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// AuditEntry is one row of operator_audit.
type AuditEntry struct {
	ID           int64          `json:"id"`
	Timestamp    time.Time      `json:"timestamp"`
	OperatorID   string         `json:"operator_id"`
	OperatorKind string         `json:"operator_kind"`
	Action       string         `json:"action"`
	TargetOrg    string         `json:"target_org,omitempty"`
	Status       int            `json:"status"`
	Details      map[string]any `json:"details"`
	PreviousHash string         `json:"previous_hash"`
	EntryHash    string         `json:"entry_hash"`
}

// entryHash chains an entry to its predecessor. The fields are joined with a
// separator that none of them can contain unescaped (details are JSON).
func entryHash(e *AuditEntry) (string, error) {
	details, err := json.Marshal(e.Details)
	if err != nil {
		return "", fmt.Errorf("operator audit: encode details: %w", err)
	}
	h := sha256.New()
	for _, part := range []string{
		e.PreviousHash,
		e.Timestamp.UTC().Format(time.RFC3339Nano),
		e.OperatorID, e.OperatorKind, e.Action, e.TargetOrg,
		strconv.Itoa(e.Status), string(details),
	} {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{':'})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Auditor writes and verifies operator_audit.
type Auditor struct {
	pool *pgxpool.Pool
}

// NewAuditor creates the auditor.
func NewAuditor(pool *pgxpool.Pool) *Auditor { return &Auditor{pool: pool} }

// Record appends an entry. A transaction-level advisory lock serializes
// writers so that the chain has no forks.
func (a *Auditor) Record(ctx context.Context, e *AuditEntry) error {
	if a == nil || a.pool == nil {
		return fmt.Errorf("operator audit is not configured")
	}
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	// Microseconds: the precision PostgreSQL keeps, so the stored entry
	// hashes like the written one.
	e.Timestamp = time.Now().UTC().Truncate(time.Microsecond)
	return database.WithOperator(ctx, a.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('operator_audit'))`); err != nil {
			return fmt.Errorf("lock operator audit: %w", err)
		}
		e.PreviousHash = genesisHash
		if err := tx.QueryRow(ctx, `SELECT entry_hash FROM operator_audit ORDER BY id DESC LIMIT 1`).Scan(&e.PreviousHash); err != nil && err != pgx.ErrNoRows {
			return fmt.Errorf("read operator audit head: %w", err)
		}
		hash, err := entryHash(e)
		if err != nil {
			return err
		}
		e.EntryHash = hash
		var target *string
		if e.TargetOrg != "" {
			target = &e.TargetOrg
		}
		if err = tx.QueryRow(ctx, `INSERT INTO operator_audit
			(timestamp, operator_id, operator_kind, action, target_org, status, details, previous_hash, entry_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			e.Timestamp, e.OperatorID, e.OperatorKind, e.Action, target, e.Status, e.Details, e.PreviousHash, e.EntryHash).Scan(&e.ID); err != nil {
			return fmt.Errorf("insert operator audit: %w", err)
		}
		return nil
	})
}

const auditColumns = `id, timestamp, operator_id, operator_kind, action, COALESCE(target_org::text, ''),
	status, details, previous_hash, entry_hash`

func scanEntry(row pgx.Row) (AuditEntry, error) {
	var e AuditEntry
	err := row.Scan(&e.ID, &e.Timestamp, &e.OperatorID, &e.OperatorKind, &e.Action, &e.TargetOrg,
		&e.Status, &e.Details, &e.PreviousHash, &e.EntryHash)
	e.Timestamp = e.Timestamp.UTC()
	return e, err
}

// List returns entries, newest first.
func (a *Auditor) List(ctx context.Context, limit, offset int) ([]AuditEntry, int, error) {
	var entries []AuditEntry
	var total int
	err := database.WithOperator(ctx, a.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM operator_audit`).Scan(&total); err != nil {
			return fmt.Errorf("count operator audit: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT `+auditColumns+` FROM operator_audit ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
		if err != nil {
			return fmt.Errorf("list operator audit: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			e, scanErr := scanEntry(rows)
			if scanErr != nil {
				return fmt.Errorf("scan operator audit: %w", scanErr)
			}
			entries = append(entries, e)
		}
		return rows.Err()
	})
	return entries, total, err
}

// VerifyResult is the outcome of a chain verification.
type VerifyResult struct {
	Valid   bool   `json:"valid"`
	Checked int    `json:"checked"`
	Broken  *int64 `json:"broken_at,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Verify recomputes the whole chain.
func (a *Auditor) Verify(ctx context.Context) (VerifyResult, error) {
	result := VerifyResult{Valid: true}
	err := database.WithOperator(ctx, a.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+auditColumns+` FROM operator_audit ORDER BY id`)
		if err != nil {
			return fmt.Errorf("read operator audit: %w", err)
		}
		defer rows.Close()
		previous := genesisHash
		for rows.Next() {
			e, scanErr := scanEntry(rows)
			if scanErr != nil {
				return fmt.Errorf("scan operator audit: %w", scanErr)
			}
			result.Checked++
			want, hashErr := entryHash(&e)
			switch {
			case hashErr != nil:
				return hashErr
			case e.PreviousHash != previous:
				result.Valid, result.Broken, result.Reason = false, &e.ID, "previous hash does not match the preceding entry"
			case e.EntryHash != want:
				result.Valid, result.Broken, result.Reason = false, &e.ID, "entry hash does not match its content"
			}
			if !result.Valid {
				return nil
			}
			previous = e.EntryHash
		}
		return rows.Err()
	})
	return result, err
}

// actionFor names an /admin request in the audit: method and path below
// /api/v1/admin.
func actionFor(method, path string) string {
	return method + " " + strings.TrimPrefix(path, "/api/v1/admin")
}
