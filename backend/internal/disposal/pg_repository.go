package disposal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Audit action and resource type of a recorded disposal (WP-751).
const (
	ActionCreated = "disposal_record.created"
	ResourceType  = "disposal_record"
)

const recordCols = `id::text, organization_id::text, COALESCE(asset_id::text,''), COALESCE(ci_id::text,''), method,
	protection_class, COALESCE(security_level,''), COALESCE(data_carrier,''), COALESCE(supplier_id::text,''),
	COALESCE(evidence_document_id::text,''), COALESCE(certificate_ref,''), COALESCE(performed_by,''),
	COALESCE(performer_surrogate::text,''), COALESCE(witness_surrogate::text,''), performed_at, COALESCE(notes,''),
	record_hash, created_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed disposal repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func scanRecord(s pgx.Row) (*Record, error) {
	rec := &Record{}
	var protectionClass *int16
	if err := s.Scan(&rec.ID, &rec.OrganizationID, &rec.AssetID, &rec.CIID, &rec.Method, &protectionClass,
		&rec.SecurityLevel, &rec.DataCarrier, &rec.SupplierID, &rec.EvidenceDocumentID, &rec.CertificateRef,
		&rec.PerformedBy, &rec.performerSurrogate, &rec.witnessSurrogate, &rec.PerformedAt, &rec.Notes,
		&rec.RecordHash, &rec.CreatedAt); err != nil {
		return nil, err
	}
	if protectionClass != nil {
		pc := int(*protectionClass)
		rec.ProtectionClass = &pc
	}
	return rec, nil
}

// disposalVisible restricts disposal records to those whose asset or CI is
// visible under the transaction's tenant scope: their policies filter the
// subqueries. The policy of disposal_record filters by the client and site
// derived from the asset or CI (migration 000145, WP-353); the predicate stays
// as defense in depth and also requires a referenced CI to be visible when
// the scope follows the asset.
const disposalVisible = `(disposal_record.asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = disposal_record.asset_id))
	AND (disposal_record.ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = disposal_record.ci_id))`

// referencesVisible checks before the insert that the asset or CI, the
// supplier and the evidence document of rec are visible under the
// transaction's tenant scope (their policies filter the subqueries; a
// soft-deleted document counts as missing): foreign keys are checked
// without row level security, so they alone would accept an object outside
// the principal's scope. A reference that is not visible is api.ErrNotFound.
func referencesVisible(ctx context.Context, tx pgx.Tx, rec *Record) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT
			($1::uuid IS NULL OR EXISTS (SELECT 1 FROM asset WHERE id = $1::uuid))
		AND ($2::uuid IS NULL OR EXISTS (SELECT 1 FROM ci WHERE id = $2::uuid))
		AND ($3::uuid IS NULL OR EXISTS (SELECT 1 FROM supplier WHERE id = $3::uuid))
		AND ($4::uuid IS NULL OR EXISTS (SELECT 1 FROM document WHERE id = $4::uuid AND deleted_at IS NULL))`,
		nilIfEmpty(rec.AssetID), nilIfEmpty(rec.CIID), nilIfEmpty(rec.SupplierID), nilIfEmpty(rec.EvidenceDocumentID)).
		Scan(&ok); err != nil {
		return fmt.Errorf("check disposal references: %w", err)
	}
	if !ok {
		return api.ErrNotFound
	}
	return nil
}

// requireVisible fails with api.ErrNotFound when the written row of table
// does not satisfy visible, i.e. when a write pointed it at an object
// outside the tenant scope; the transaction is then rolled back.
func requireVisible(ctx context.Context, tx pgx.Tx, table, visible, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND "+visible+")", id).Scan(&ok); err != nil {
		return fmt.Errorf("check %s visibility: %w", table, err)
	}
	if !ok {
		return api.ErrNotFound
	}
	return nil
}

// storePersons puts the names of the performer and the witness into the
// PII vault (subjects of kind person) in tx and keeps only their surrogates
// (convention 16, SEC-05). Without a vault (no master key) a name cannot be
// stored: the plaintext column performed_by is no longer written.
func storePersons(ctx context.Context, tx pgx.Tx, rec *Record) error {
	if rec.PerformedBy == "" && rec.Witness == "" {
		return nil
	}
	vault := pii.Default()
	if vault == nil {
		return fmt.Errorf("store disposal persons: %w", pii.ErrNoKey)
	}
	for _, p := range []struct {
		name      string
		surrogate *string
	}{{rec.PerformedBy, &rec.performerSurrogate}, {rec.Witness, &rec.witnessSurrogate}} {
		if p.name == "" {
			continue
		}
		id, err := vault.CreateTx(ctx, tx, rec.OrganizationID, pii.KindPerson, pii.Data{"name": p.name})
		if err != nil {
			return fmt.Errorf("store disposal person: %w", err)
		}
		*p.surrogate = id
	}
	return nil
}

// reveal fills performer and witness of recs from their vault subjects. A
// record without surrogate (written before migration 000245) keeps the
// plaintext of performed_by; a subject that was deleted (crypto-shredding)
// shows no name.
func reveal(ctx context.Context, tx pgx.Tx, orgID string, recs ...*Record) error {
	var ids []string
	for _, rec := range recs {
		for _, id := range []string{rec.performerSurrogate, rec.witnessSurrogate} {
			if id != "" {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	subjects := map[string]*pii.Subject{}
	if vault := pii.Default(); vault != nil {
		var err error
		if subjects, err = vault.ResolveTx(ctx, tx, orgID, ids); err != nil {
			return fmt.Errorf("resolve disposal persons: %w", err)
		}
	}
	name := func(id string) string {
		if s := subjects[id]; s != nil {
			return s.Data["name"]
		}
		return ""
	}
	for _, rec := range recs {
		if rec.performerSurrogate != "" {
			rec.PerformedBy = name(rec.performerSurrogate)
		}
		if rec.witnessSurrogate != "" {
			rec.Witness = name(rec.witnessSurrogate)
		}
	}
	return nil
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Record, int, error) {
	out := []Record{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		if filter.Method != "" {
			where = append(where, fmt.Sprintf("method = $%d", pos))
			args = append(args, filter.Method)
			pos++
		}
		if filter.AssetID != "" {
			where = append(where, fmt.Sprintf("asset_id = $%d", pos))
			args = append(args, filter.AssetID)
			pos++
		}
		if filter.CIID != "" {
			where = append(where, fmt.Sprintf("ci_id = $%d", pos))
			args = append(args, filter.CIID)
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM disposal_record WHERE "+disposalVisible+" AND "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+recordCols+" FROM disposal_record WHERE "+disposalVisible+" AND "+clause+fmt.Sprintf(" ORDER BY performed_at DESC LIMIT $%d OFFSET $%d", pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rec, scanErr := scanRecord(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *rec)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		recs := make([]*Record, len(out))
		for i := range out {
			recs[i] = &out[i]
		}
		return reveal(ctx, tx, orgID, recs...)
	})
	return out, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Record, error) {
	var rec *Record
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rec, err = scanRecord(tx.QueryRow(ctx, "SELECT "+recordCols+" FROM disposal_record WHERE "+disposalVisible+" AND organization_id = $1 AND id = $2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("disposal record not found")
		}
		if err != nil {
			return err
		}
		return reveal(ctx, tx, orgID, rec)
	})
	return rec, err
}

// Create records a disposal in one transaction: the visibility of the
// referenced objects, the vault subjects of the persons, the row (the
// database sets record_hash and the scope) and, last before the commit,
// the audit entry disposal_record.created (database.BeforeCommit, WP-334).
// Any error rolls everything back, the audit entry included.
func (r *PGRepository) Create(ctx context.Context, rec *Record) error {
	return database.WithRequestTenant(ctx, r.pool, rec.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if rec.PerformedAt.IsZero() {
			rec.PerformedAt = time.Now().UTC()
		}
		if err := referencesVisible(ctx, tx, rec); err != nil {
			return err
		}
		if err := storePersons(ctx, tx, rec); err != nil {
			return err
		}
		var protectionClass any
		if rec.ProtectionClass != nil {
			protectionClass = *rec.ProtectionClass
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO disposal_record (organization_id, asset_id, ci_id, method, protection_class, security_level,
				data_carrier, supplier_id, evidence_document_id, certificate_ref, performer_surrogate, witness_surrogate,
				performed_at, notes)
			VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8::uuid, $9::uuid, $10, $11::uuid, $12::uuid, $13, $14)
			RETURNING id::text, record_hash, created_at
		`, rec.OrganizationID, nilIfEmpty(rec.AssetID), nilIfEmpty(rec.CIID), rec.Method, protectionClass,
			nilIfEmpty(rec.SecurityLevel), nilIfEmpty(rec.DataCarrier), nilIfEmpty(rec.SupplierID),
			nilIfEmpty(rec.EvidenceDocumentID), nilIfEmpty(rec.CertificateRef), nilIfEmpty(rec.performerSurrogate),
			nilIfEmpty(rec.witnessSurrogate), rec.PerformedAt, nilIfEmpty(rec.Notes)).
			Scan(&rec.ID, &rec.RecordHash, &rec.CreatedAt); err != nil {
			return err
		}
		if err := requireVisible(ctx, tx, "disposal_record", disposalVisible, rec.ID); err != nil {
			return err
		}
		entry := auditEntry(ctx, rec)
		return database.BeforeCommit(ctx, func() error {
			if _, err := audit.NewPGRecorder().Record(ctx, tx, entry); err != nil {
				return fmt.Errorf("audit %s: %w", ActionCreated, err)
			}
			return nil
		})
	})
}

// auditEntry is the entry disposal_record.created of rec: the record hash,
// the object, the method and the DIN 66399 classification; no personal
// data (the names live in the vault only).
func auditEntry(ctx context.Context, rec *Record) audit.Entry {
	changes := map[string]any{
		"record_hash":      rec.RecordHash,
		"method":           rec.Method,
		"protection_class": nil,
		"security_level":   nil,
	}
	if rec.AssetID != "" {
		changes["asset_id"] = rec.AssetID
	}
	if rec.CIID != "" {
		changes["ci_id"] = rec.CIID
	}
	if rec.ProtectionClass != nil {
		changes["protection_class"] = *rec.ProtectionClass
	}
	if rec.SecurityLevel != "" {
		changes["security_level"] = rec.SecurityLevel
	}
	actorID, actorType := "", "system"
	if user := strings.TrimSpace(tenant.FromContext(ctx).UserID); user != "" {
		actorType = "user"
		if audit.IsUUID(user) {
			actorID = user
		}
	}
	return audit.Entry{
		OrganizationID: rec.OrganizationID,
		ActorID:        actorID,
		ActorType:      actorType,
		Action:         ActionCreated,
		ResourceType:   ResourceType,
		ResourceID:     rec.ID,
		Changes:        changes,
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
