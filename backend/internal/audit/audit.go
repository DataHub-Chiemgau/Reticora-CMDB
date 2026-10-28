// Package audit provides the audit log with hash-chain integrity.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
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
// over the binary values (the canonical JSON as UTF-8), hex-encoded. The
// canonical JSON is the serialization of the persisted row, identical to the
// database function audit_entry_hash (migration 000158): organization_id and
// seq first, a microsecond-precision RFC3339 UTC timestamp, plain ids and the
// stored changes payload in its jsonb text form. Because the BEFORE INSERT
// trigger computes the hash from the stored columns, verification over the
// stored row always reproduces the stored hash.
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
// audit_entry_hash: a JSON object with fixed key order whose strings are
// escaped like to_jsonb(text)::text and where `changes` is the payload in its
// jsonb text form (jsonbText).
func canonicalHashPayload(orgID string, seq int64, timestamp, actorID, actorType, action, resourceType, resourceID string,
	changes map[string]interface{}, source, requestID, correlationID string) string {
	changesJSON := "{}"
	if changes != nil {
		if text, err := jsonbText(changes); err == nil {
			changesJSON = text
		}
	}
	var sb strings.Builder
	sb.WriteString(`{"organization_id":`)
	writeJSONBString(&sb, orgID)
	sb.WriteString(`,"seq":`)
	sb.WriteString(strconv.FormatInt(seq, 10))
	sb.WriteString(`,"timestamp":`)
	writeJSONBString(&sb, timestamp)
	sb.WriteString(`,"actor_id":`)
	writeJSONBString(&sb, actorID)
	sb.WriteString(`,"actor_type":`)
	writeJSONBString(&sb, actorType)
	sb.WriteString(`,"action":`)
	writeJSONBString(&sb, action)
	sb.WriteString(`,"resource_type":`)
	writeJSONBString(&sb, resourceType)
	sb.WriteString(`,"resource_id":`)
	writeJSONBString(&sb, resourceID)
	sb.WriteString(`,"changes":`)
	sb.WriteString(changesJSON)
	sb.WriteString(`,"source":`)
	writeJSONBString(&sb, source)
	sb.WriteString(`,"request_id":`)
	writeJSONBString(&sb, requestID)
	sb.WriteString(`,"correlation_id":`)
	writeJSONBString(&sb, correlationID)
	sb.WriteString(`}`)
	return sb.String()
}

// jsonbText returns v as PostgreSQL prints it once the recorder stored its
// JSON encoding as jsonb (jsonb::text): object keys ordered by byte length,
// then bytewise; ", " between members and elements, ": " after a key;
// strings escaped like escape_json, numbers as numeric prints them.
func jsonbText(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return "", err
	}
	var sb strings.Builder
	writeJSONB(&sb, generic)
	return sb.String(), nil
}

func writeJSONB(sb *strings.Builder, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) < len(keys[j])
			}
			return keys[i] < keys[j]
		})
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(", ")
			}
			writeJSONBString(sb, k)
			sb.WriteString(": ")
			writeJSONB(sb, x[k])
		}
		sb.WriteByte('}')
	case []any:
		sb.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				sb.WriteString(", ")
			}
			writeJSONB(sb, e)
		}
		sb.WriteByte(']')
	case string:
		writeJSONBString(sb, x)
	case json.Number:
		sb.WriteString(numericText(string(x)))
	case bool:
		sb.WriteString(strconv.FormatBool(x))
	default:
		sb.WriteString("null")
	}
}

// writeJSONBString writes s as a JSON string escaped like PostgreSQL's
// escape_json: \b \f \n \r \t \" \\, other control characters as \u00xx,
// everything else (also non-ASCII, <, >, &) unchanged.
func writeJSONBString(sb *strings.Builder, s string) {
	const hexDigits = "0123456789abcdef"
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		default:
			if c < 0x20 {
				sb.WriteString(`\u00`)
				sb.WriteByte(hexDigits[c>>4])
				sb.WriteByte(hexDigits[c&0xf])
			} else {
				sb.WriteByte(c)
			}
		}
	}
	sb.WriteByte('"')
}

// maxNumericWeight is the most digits before the decimal point a numeric
// holds; jsonb refuses larger numbers.
const maxNumericWeight = 131072

// numericText prints a JSON number like PostgreSQL's numeric: in decimal
// notation, with as many digits after the point as the input has minus its
// exponent, and zero without a sign.
func numericText(n string) string {
	negative := strings.HasPrefix(n, "-")
	mantissa, exponent := strings.TrimPrefix(n, "-"), 0
	if i := strings.IndexAny(mantissa, "eE"); i >= 0 {
		e, err := strconv.Atoi(mantissa[i+1:])
		if err != nil || e > maxNumericWeight || e < -maxNumericWeight {
			return n
		}
		mantissa, exponent = mantissa[:i], e
	}
	intPart, fraction, _ := strings.Cut(mantissa, ".")
	digits := intPart + fraction
	point := len(intPart) + exponent
	scale := max(len(fraction)-exponent, 0)
	if point < 0 {
		digits = strings.Repeat("0", -point) + digits
		point = 0
	}
	if missing := point + scale - len(digits); missing > 0 {
		digits += strings.Repeat("0", missing)
	}
	var sb strings.Builder
	if negative && strings.Trim(digits, "0") != "" {
		sb.WriteByte('-')
	}
	if whole := strings.TrimLeft(digits[:point], "0"); whole != "" {
		sb.WriteString(whole)
	} else {
		sb.WriteByte('0')
	}
	if scale > 0 {
		sb.WriteByte('.')
		sb.WriteString(digits[point : point+scale])
	}
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
	if err := normalize(ctx, &entry); err != nil {
		return nil, err
	}

	if err := AcquireAdvisoryLock(ctx, tx, entry.OrganizationID); err != nil {
		return nil, err
	}

	// The insert trigger sets seq, previous_hash and hash under the chain
	// lock (migration 000117).
	changesJSON, err := sealedChanges(ctx, tx, &entry)
	if err != nil {
		return nil, err
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
		entry.ResourceType, nilIfEmpty(entry.ResourceID), changesJSON, entry.Source,
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

// normalize completes an entry before it is written: timestamp, actor type,
// changes, and the request, correlation and source of the request that
// makes the change (AUD-07).
func normalize(ctx context.Context, entry *Entry) error {
	if entry.OrganizationID == "" {
		return fmt.Errorf("audit: organization_id is required")
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
	return nil
}

// sealedChanges stores the personal values of the changes as vault
// surrogates (SEC-05, AUD-04) and returns the JSON the row stores.
func sealedChanges(ctx context.Context, tx pgx.Tx, entry *Entry) (string, error) {
	sealed, _, err := pii.SealTx(ctx, tx, entry.OrganizationID, entry.Changes)
	if err != nil {
		return "", fmt.Errorf("audit: seal personal data: %w", err)
	}
	if m, ok := sealed.(map[string]interface{}); ok {
		entry.Changes = m
	}
	changesJSON, err := json.Marshal(entry.Changes)
	if err != nil {
		return "", fmt.Errorf("audit: marshal changes: %w", err)
	}
	return string(changesJSON), nil
}

// TxBatchRecorder appends several entries of one organization inside tx with
// one statement (NFR-03, WP-333).
type TxBatchRecorder interface {
	RecordMany(ctx context.Context, tx pgx.Tx, entries []Entry) ([]Entry, error)
}

// RecordMany appends the entries of one organization inside tx with one
// INSERT and returns the persisted entries in their order. The insert
// trigger takes the chain lock of the organization on the first row, holds
// it until the transaction ends and chains the rows in the order of entries
// (AUD-01, AUD-04): a row-level trigger sees the rows the statement inserted
// before it. A chain out of order is an error, never a written entry.
func (r *PGRecorder) RecordMany(ctx context.Context, tx pgx.Tx, entries []Entry) ([]Entry, error) {
	out := make([]Entry, len(entries))
	if len(entries) == 0 {
		return out, nil
	}
	n := len(entries)
	orgs, actors, actorTypes, actions := make([]string, n), make([]*string, n), make([]string, n), make([]string, n)
	resourceTypes, resourceIDs, changes, sources := make([]string, n), make([]*string, n), make([]string, n), make([]string, n)
	requests, correlations, timestamps := make([]*string, n), make([]*string, n), make([]time.Time, n)
	for i := range entries {
		entry := entries[i]
		if err := normalize(ctx, &entry); err != nil {
			return nil, err
		}
		if entry.OrganizationID != entries[0].OrganizationID {
			return nil, fmt.Errorf("audit: entries of several organizations in one batch")
		}
		changesJSON, err := sealedChanges(ctx, tx, &entry)
		if err != nil {
			return nil, err
		}
		out[i] = entry
		orgs[i], timestamps[i], actorTypes[i], actions[i] = entry.OrganizationID, entry.Timestamp, entry.ActorType, entry.Action
		resourceTypes[i], changes[i], sources[i] = entry.ResourceType, changesJSON, entry.Source
		actors[i], resourceIDs[i] = textOrNil(entry.ActorID), textOrNil(entry.ResourceID)
		requests[i], correlations[i] = textOrNil(entry.RequestID), textOrNil(entry.CorrelationID)
	}
	rows, err := tx.Query(ctx, `
		WITH input AS MATERIALIZED (
			SELECT gen_random_uuid() AS id, e.*
			FROM unnest($1::text[], $2::timestamptz[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[],
			            $8::text[], $9::text[], $10::text[], $11::text[])
			     WITH ORDINALITY AS e(organization_id, ts, actor_id, actor_type, action, resource_type, resource_id,
			                          changes, source, request_id, correlation_id, ord)
		), ins AS (
			INSERT INTO audit_log (id, organization_id, timestamp, actor_id, actor_type, action, resource_type,
				resource_id, changes, source, request_id, correlation_id, hash)
			SELECT id, organization_id::uuid, ts, actor_id::uuid, actor_type, action, resource_type, resource_id::uuid,
				changes::jsonb, source, request_id, correlation_id, ''
			FROM input ORDER BY ord
			RETURNING id, seq, timestamp, previous_hash, hash
		)
		SELECT input.ord, ins.id::text, ins.seq, ins.timestamp, ins.previous_hash, ins.hash
		FROM input JOIN ins ON ins.id = input.id ORDER BY input.ord`,
		orgs, timestamps, actors, actorTypes, actions, resourceTypes, resourceIDs, changes, sources, requests, correlations)
	if err != nil {
		return nil, fmt.Errorf("audit: insert entries: %w", err)
	}
	defer rows.Close()
	read := 0
	for rows.Next() {
		var ord int64
		var e Entry
		if err := rows.Scan(&ord, &e.ID, &e.Seq, &e.Timestamp, &e.PreviousHash, &e.Hash); err != nil {
			return nil, fmt.Errorf("audit: insert entries: %w", err)
		}
		i := int(ord) - 1
		if i != read || (read > 0 && e.Seq != out[read-1].Seq+1) {
			return nil, fmt.Errorf("audit: batch chained out of order at entry %d", read)
		}
		out[i].ID, out[i].Seq, out[i].PreviousHash, out[i].Hash = e.ID, e.Seq, e.PreviousHash, e.Hash
		out[i].Timestamp = e.Timestamp.UTC().Truncate(time.Microsecond)
		read++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: insert entries: %w", err)
	}
	if read != n {
		return nil, fmt.Errorf("audit: %d of %d entries written", read, n)
	}
	return out, nil
}

// textOrNil is the array element of an optional text column.
func textOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
	start, prevHash := int64(1), GenesisHash
	// A chain whose oldest entries the retention run removed starts after
	// the anchor its audit.retention entry records (AUD-09).
	if len(entries) > 0 && entries[0].Seq != 1 {
		if through, anchor, ok := retentionAnchor(entries, entries[0].Seq-1); ok {
			start, prevHash = through+1, anchor
		}
	}
	for i := range entries {
		position := i + int(start)
		if entries[i].Seq != int64(position) {
			return VerifyResult{Intact: false, Checked: i, BrokenID: entries[i].ID, BrokenAt: position, BrokenReason: "sequence gap"}
		}
		if entries[i].PreviousHash != prevHash {
			return VerifyResult{Intact: false, Checked: i, BrokenID: entries[i].ID, BrokenAt: position, BrokenReason: "previous_hash mismatch"}
		}
		if expected[i] != entries[i].Hash {
			return VerifyResult{Intact: false, Checked: i, BrokenID: entries[i].ID, BrokenAt: position, BrokenReason: "hash mismatch"}
		}
		prevHash = entries[i].Hash
	}
	return VerifyResult{Intact: true, Checked: len(entries)}
}

// RetentionAction is the action of the entry a retention run appends: it
// anchors the chain after the removed oldest entries (AUD-09).
const RetentionAction = "audit.retention"

// retentionAnchor finds the retention entry that removed the entries up to
// through and returns the hash of the last removed entry. The retention
// entry is itself part of the verified chain.
func retentionAnchor(entries []Entry, through int64) (seq int64, anchor string, ok bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		e := &entries[i]
		if e.Action != RetentionAction {
			continue
		}
		deletedThrough, _ := e.Changes["deleted_through_seq"].(float64)
		hash, _ := e.Changes["anchor_hash"].(string)
		if int64(deletedThrough) == through && hash != "" {
			return through, hash, true
		}
	}
	return 0, "", false
}

// Purge removes the organization's entries older than cutoff — the oldest
// part of its chain, always keeping the newest entry so the sequence goes
// on — and appends an audit.retention entry with the number removed and the
// anchor (seq and hash of the last removed entry), all in tx (NFR-06,
// AUD-09). The database permits the deletion only for entries older than
// ten years and only under app.audit_retention (migration 000119).
func Purge(ctx context.Context, tx pgx.Tx, orgID string, cutoff time.Time) (int, error) {
	if err := AcquireAdvisoryLock(ctx, tx, orgID); err != nil {
		return 0, err
	}
	var through int64
	var anchor string
	err := tx.QueryRow(ctx, `
		SELECT seq, hash FROM audit_log
		WHERE organization_id = $1
		  AND seq < (SELECT max(seq) FROM audit_log WHERE organization_id = $1)
		  AND seq < COALESCE((SELECT min(seq) FROM audit_log WHERE organization_id = $1 AND timestamp >= $2),
		                     9223372036854775807)
		ORDER BY seq DESC LIMIT 1`, orgID, cutoff.UTC()).Scan(&through, &anchor)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("audit: find entries to remove: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.audit_retention', 'on', true)`); err != nil {
		return 0, fmt.Errorf("audit: declare retention: %w", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM audit_log WHERE organization_id = $1 AND seq <= $2`, orgID, through)
	if err != nil {
		return 0, fmt.Errorf("audit: remove entries: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.audit_retention', 'off', true)`); err != nil {
		return 0, fmt.Errorf("audit: end retention: %w", err)
	}
	removed := int(tag.RowsAffected())
	if _, err = (&PGRecorder{}).Record(ctx, tx, Entry{
		OrganizationID: orgID, ActorType: "system", Action: RetentionAction, ResourceType: "audit_log", Source: SourceSystem,
		Changes: map[string]interface{}{"deleted": removed, "deleted_through_seq": through, "anchor_hash": anchor,
			"cutoff": cutoff.UTC().Format(time.RFC3339)},
	}); err != nil {
		return removed, err
	}
	return removed, nil
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
	// audit-log is the catalog path (API-05); audit stays until its sunset
	// (API-01).
	for _, base := range []string{"/api/v1/audit-log", "/api/v1/audit"} {
		r.Get(base, h.List)
		r.Post(base+"/verify", h.Verify)
	}
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
	// A restricted principal sees only the entries about objects it may
	// read (restrictedResource, scope.go); audit_log carries no client
	// column.
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
	// The API shows the personal values the surrogates stand for; deleted
	// ones are null (SEC-05).
	changes := make([]any, len(entries))
	for i := range entries {
		changes[i] = entries[i].Changes
	}
	if _, err = pii.RevealTx(ctx, tx, orgID, changes); err != nil {
		return nil, 0, fmt.Errorf("audit: resolve personal data: %w", err)
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
// validation: fixed texts naming the refused parameter.
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
