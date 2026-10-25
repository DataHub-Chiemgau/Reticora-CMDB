package override

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

const selectColumns = `
	id::text, organization_id::text, ci_id::text, field_name,
	discovered_value, COALESCE(discovered_source, ''), discovered_at,
	override_value, COALESCE(override_author::text, ''),
	COALESCE(override_reason, ''), override_at, protected,
	created_at, updated_at, COALESCE(discovered_rank, 0), observed_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed override repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// policyPriorities resolves the effective priorities inside a transaction.
func policyPrioritiesTx(ctx context.Context, tx pgx.Tx, orgID string) []string {
	var raw []byte
	err := tx.QueryRow(ctx,
		"SELECT priorities FROM source_priority_policy WHERE organization_id = $1 AND is_default",
		orgID).Scan(&raw)
	if err != nil {
		return DefaultPriorities
	}
	var priorities []string
	if err := json.Unmarshal(raw, &priorities); err != nil || len(priorities) == 0 {
		return DefaultPriorities
	}
	return priorities
}

func (r *PGRepository) withEffectiveTx(ctx context.Context, tx pgx.Tx, fv *FieldValue) *FieldValue {
	priorities := policyPrioritiesTx(ctx, tx, fv.OrganizationID)
	out := *fv
	out.EffectiveValue = ResolveEffective(fv, priorities)
	out.Diverged = IsDiverged(fv, priorities)
	return &out
}

// visibleCI restricts field value rows to CIs visible under the transaction's
// tenant scope: the ci policy filters the subquery by client scope, while
// ci_field_value carries no client column yet (WP-025).
const visibleCI = "EXISTS (SELECT 1 FROM ci WHERE ci.id = ci_field_value.ci_id)"

// requireVisibleCI rejects writes to a CI the tenant scope does not show; the
// foreign key alone would accept a CI of another client. It locks the CI row
// (lockCI).
func requireVisibleCI(ctx context.Context, tx pgx.Tx, ciID string) error {
	visible, err := lockCI(ctx, tx, ciID)
	if err != nil {
		return err
	}
	if !visible {
		return fmt.Errorf("not found")
	}
	return nil
}

// lockCI locks the row of the CI before any of its field values and reports
// whether the CI is visible, in one statement. Every write of a field value
// updates the CI row as well (syncCIField, the projection trigger of
// migration 000135), while the ingest's record transaction and ci.Update lock
// the CI row first (discovery LockCI, SELECT … FOR UPDATE): taking it first
// here too keeps one lock order, so the two wait for each other instead of
// deadlocking (40P01). Only a row the scope may update is locked; the others
// are not written either.
func lockCI(ctx context.Context, tx pgx.Tx, ciID string) (visible bool, err error) {
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1),
		EXISTS (SELECT 1 FROM ci WHERE id = $1 FOR UPDATE)`, ciID).Scan(&visible, &locked); err != nil {
		return false, fmt.Errorf("check ci visibility: %w", err)
	}
	return visible, nil
}

// ListForCI returns all tracked field values of a CI with resolved effective
// values and divergence flags.
func (r *PGRepository) ListForCI(ctx context.Context, orgID, ciID string) ([]FieldValue, error) {
	var out []FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM ci_field_value WHERE ci_id = $1 AND "+visibleCI+" ORDER BY field_name ASC",
			selectColumns), ciID)
		if err != nil {
			return fmt.Errorf("list field values: %w", err)
		}
		// The rows are read completely before the effective values are
		// resolved: the connection cannot run a query while rows are open.
		var values []*FieldValue
		for rows.Next() {
			fv, err := scan(rows)
			if err != nil {
				rows.Close()
				return err
			}
			values = append(values, fv)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, fv := range values {
			out = append(out, *r.withEffectiveTx(ctx, tx, fv))
		}
		return nil
	})
	return out, err
}

// Get returns one field value.
func (r *PGRepository) Get(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error) {
	var out *FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		fv, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT %s FROM ci_field_value WHERE ci_id = $1 AND field_name = $2 AND "+visibleCI,
			selectColumns), ciID, fieldName))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("get field value: %w", err)
		}
		out = r.withEffectiveTx(ctx, tx, fv)
		return nil
	})
	return out, err
}

// RecordDiscovered upserts only the discovered columns. The override columns
// are excluded from the upsert's SET clause, so a protected manual override
// is never overwritten by discovery (spec §13).
func (r *PGRepository) RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string, observedAt time.Time) (*FieldValue, error) {
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	var out *FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireVisibleCI(ctx, tx, ciID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO ci_field_value (
				organization_id, ci_id, field_name, discovered_value,
				discovered_source, discovered_at, discovered_rank, observed_at
			) VALUES ($1, $2, $3, $4, $5, now(), $6, $7)
			ON CONFLICT (ci_id, field_name) DO UPDATE SET
				discovered_value = EXCLUDED.discovered_value,
				discovered_source = EXCLUDED.discovered_source,
				discovered_at = EXCLUDED.discovered_at,
				discovered_rank = EXCLUDED.discovered_rank,
				observed_at = EXCLUDED.observed_at
			RETURNING %s`, selectColumns),
			orgID, ciID, fieldName, jsonValue(value), nilIfEmpty(source), SourceRank(source), observedAt.UTC())
		fv, err := scan(row)
		if err != nil {
			return fmt.Errorf("record discovered value: %w", err)
		}
		out = r.withEffectiveTx(ctx, tx, fv)
		return nil
	})
	return out, err
}

// RecordDiscoveredMany upserts the discovered columns of several fields of
// one CI in one statement, like RecordDiscovered per field: a new CI records
// all its fields in one round trip instead of one transaction per field
// (NFR-03, WP-206). The override columns stay untouched.
func (r *PGRepository) RecordDiscoveredMany(ctx context.Context, orgID, ciID string, values map[string]any, source string, observedAt time.Time) error {
	if len(values) == 0 {
		return nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	raw := make([]*string, len(names))
	for i, name := range names {
		if v, ok := jsonValue(values[name]).(string); ok {
			raw[i] = &v
		}
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireVisibleCI(ctx, tx, ciID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ci_field_value (
				organization_id, ci_id, field_name, discovered_value,
				discovered_source, discovered_at, discovered_rank, observed_at
			)
			SELECT $1, $2, f.name, f.value::jsonb, $5, now(), $6, $7
			FROM unnest($3::text[], $4::text[]) AS f(name, value)
			ON CONFLICT (ci_id, field_name) DO UPDATE SET
				discovered_value = EXCLUDED.discovered_value,
				discovered_source = EXCLUDED.discovered_source,
				discovered_at = EXCLUDED.discovered_at,
				discovered_rank = EXCLUDED.discovered_rank,
				observed_at = EXCLUDED.observed_at`,
			orgID, ciID, names, raw, nilIfEmpty(source), SourceRank(source), observedAt.UTC()); err != nil {
			return fmt.Errorf("record discovered values: %w", err)
		}
		return nil
	})
}

// DiscoveredValues are the discovered fields of one CI with their source
// and observation time.
type DiscoveredValues struct {
	CIID       string
	Values     map[string]any
	Source     string
	ObservedAt time.Time
}

// recordDiscoveredBatchSQL upserts the discovered values of the fields of
// several CIs ($2 … $7, one element per field) whose CIs ($8, each once) the
// tenant scope shows.
const recordDiscoveredBatchSQL = `
	WITH visible AS MATERIALIZED (
		SELECT v.id FROM unnest($8::uuid[]) AS k(id)
		CROSS JOIN LATERAL (SELECT ci.id FROM ci WHERE ci.id = k.id) v
	)
	INSERT INTO ci_field_value (
		organization_id, ci_id, field_name, discovered_value,
		discovered_source, discovered_at, discovered_rank, observed_at
	)
	SELECT $1, f.ci_id::uuid, f.name, f.value::jsonb, f.source, now(), f.rank, f.observed_at
	FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::integer[], $7::timestamptz[])
	     WITH ORDINALITY AS f(ci_id, name, value, source, rank, observed_at, ord)
	JOIN visible ON visible.id = f.ci_id::uuid
	ORDER BY f.ord
	ON CONFLICT (ci_id, field_name) DO UPDATE SET
		discovered_value = EXCLUDED.discovered_value,
		discovered_source = EXCLUDED.discovered_source,
		discovered_at = EXCLUDED.discovered_at,
		discovered_rank = EXCLUDED.discovered_rank,
		observed_at = EXCLUDED.observed_at`

// RecordDiscoveredBatch upserts the discovered columns of the fields of
// several CIs, each at most once in batch, with one statement, each CI like
// RecordDiscoveredMany (NFR-03, WP-333); the override columns stay
// untouched. The visibility check is
// part of the statement: the fields of a CI the tenant scope does not show
// are not written, and the call reports "not found". It reads each CI once
// by its key (WP-334): a semi join of the values with the CIs of the
// organization made the planner, with statistics from before a large
// ingest, compare every value with every CI.
func (r *PGRepository) RecordDiscoveredBatch(ctx context.Context, orgID string, batch []DiscoveredValues) error {
	var ids, names, cis []string
	var raw, sources []*string
	var ranks []int32
	var observed []time.Time
	seen := make(map[string]bool, len(batch))
	for _, b := range batch {
		if !seen[b.CIID] {
			seen[b.CIID] = true
			cis = append(cis, b.CIID)
		}
		observedAt := b.ObservedAt
		if observedAt.IsZero() {
			observedAt = time.Now()
		}
		var source *string
		if strings.TrimSpace(b.Source) != "" {
			source = &b.Source
		}
		fields := make([]string, 0, len(b.Values))
		for name := range b.Values {
			fields = append(fields, name)
		}
		sort.Strings(fields)
		for _, name := range fields {
			var value *string
			if v, ok := jsonValue(b.Values[name]).(string); ok {
				value = &v
			}
			ids, names, raw = append(ids, b.CIID), append(names, name), append(raw, value)
			sources, ranks, observed = append(sources, source), append(ranks, int32(SourceRank(b.Source))), append(observed, observedAt.UTC())
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, recordDiscoveredBatchSQL, orgID, ids, names, raw, sources, ranks, observed, cis)
		if err != nil {
			return fmt.Errorf("record discovered values: %w", err)
		}
		if tag.RowsAffected() != int64(len(ids)) {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

// SetOverride sets or replaces the manual override of a field. The override
// is a manual write: in the same transaction the CI takes the value, its
// version rises and its history records the change (raiseVersion, API-07).
func (r *PGRepository) SetOverride(ctx context.Context, orgID, ciID, fieldName string, value any, author, reason string, protected bool) (*FieldValue, error) {
	if reason == "" {
		return nil, fmt.Errorf("override reason is required")
	}
	if structuralFields[fieldName] {
		return nil, ErrNotOverridable
	}
	var out *FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireVisibleCI(ctx, tx, ciID); err != nil {
			return err
		}
		before, err := ciFieldValue(ctx, tx, ciID, fieldName)
		if err != nil {
			return err
		}
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO ci_field_value (
				organization_id, ci_id, field_name, override_value,
				override_author, override_reason, override_at, protected
			) VALUES ($1, $2, $3, $4, $5, $6, now(), $7)
			ON CONFLICT (ci_id, field_name) DO UPDATE SET
				override_value = EXCLUDED.override_value,
				override_author = EXCLUDED.override_author,
				override_reason = EXCLUDED.override_reason,
				override_at = EXCLUDED.override_at,
				protected = EXCLUDED.protected
			RETURNING %s`, selectColumns),
			orgID, ciID, fieldName, jsonValue(value), nilIfEmpty(author), reason, protected)
		fv, err := scan(row)
		if err != nil {
			return fmt.Errorf("set override: %w", err)
		}
		// The CI shows the override at once (OVR-01, CI-10).
		if syncErr := syncCIField(ctx, tx, ciID, fieldName, value); syncErr != nil {
			return syncErr
		}
		version, err := raiseVersion(ctx, tx, orgID, ciID, fieldName, before, value)
		if err != nil {
			return err
		}
		if auditErr := recordAudit(ctx, tx, orgID, ciID, "ci.override_set", map[string]any{
			"field": fieldName, "value": value, "reason": reason, "protected": protected,
		}); auditErr != nil {
			return auditErr
		}
		out = r.withEffectiveTx(ctx, tx, fv)
		out.CIVersion = version
		return nil
	})
	return out, err
}

// SetOverrideTx records a manual override of a field inside the caller's
// transaction, so a manual write and its override commit together (OVR-01).
// author is stored when it is a user id; value nil records the removal of
// the field, which stays protected like any other override.
func SetOverrideTx(ctx context.Context, tx pgx.Tx, orgID, ciID, fieldName string, value any, author, reason string, protected bool) error {
	if reason == "" {
		return fmt.Errorf("override reason is required")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ci_field_value (
			organization_id, ci_id, field_name, override_value,
			override_author, override_reason, override_at, protected
		) VALUES ($1, $2, $3, $4, $5, $6, now(), $7)
		ON CONFLICT (ci_id, field_name) DO UPDATE SET
			override_value = EXCLUDED.override_value,
			override_author = EXCLUDED.override_author,
			override_reason = EXCLUDED.override_reason,
			override_at = EXCLUDED.override_at,
			protected = EXCLUDED.protected`,
		orgID, ciID, fieldName, jsonValue(value), userID(author), reason, protected); err != nil {
		return fmt.Errorf("set override %s: %w", fieldName, err)
	}
	return nil
}

// userID returns author when it is a UUID (an app_user id), else nil: the
// override_author column holds user ids only.
func userID(author string) any {
	if len(author) != 36 {
		return nil
	}
	for i, c := range author {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return nil
			}
		case (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F'):
			return nil
		}
	}
	return author
}

// ClearOverride removes the manual override of a field. In the same
// transaction the CI returns to the observed value, when there is one, its
// version rises and its history records the change (raiseVersion, API-07),
// and the change is audited (OVR-01). An unknown or invisible CI and a field
// without a value are ErrNotFound; a field without an override keeps the
// version.
func (r *PGRepository) ClearOverride(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error) {
	var out *FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// The CI row first (lockCI).
		visible, err := lockCI(ctx, tx, ciID)
		if err != nil {
			return err
		}
		if !visible {
			return ErrNotFound
		}
		var overridden bool
		if err = tx.QueryRow(ctx, `SELECT override_at IS NOT NULL OR override_value IS NOT NULL
			FROM ci_field_value WHERE ci_id = $1 AND field_name = $2`, ciID, fieldName).Scan(&overridden); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("read override: %w", err)
		}
		before, err := ciFieldValue(ctx, tx, ciID, fieldName)
		if err != nil {
			return err
		}
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			UPDATE ci_field_value SET
				override_value = NULL, override_author = NULL,
				override_reason = NULL, override_at = NULL, protected = false
			WHERE ci_id = $1 AND field_name = $2 AND %s
			RETURNING %s`, visibleCI, selectColumns), ciID, fieldName)
		fv, err := scan(row)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("clear override: %w", err)
		}
		changes := map[string]any{"field": fieldName}
		after := before
		if fv.DiscoveredAt != nil && !structuralFields[fieldName] {
			if syncErr := syncCIField(ctx, tx, ciID, fieldName, fv.DiscoveredValue); syncErr != nil {
				return syncErr
			}
			changes["restored_value"] = fv.DiscoveredValue
			changes["restored_source"] = fv.DiscoveredSource
			after = fv.DiscoveredValue
		}
		var version int64
		if overridden {
			if version, err = raiseVersion(ctx, tx, orgID, ciID, fieldName, before, after); err != nil {
				return err
			}
		} else if err = tx.QueryRow(ctx, `SELECT version FROM ci WHERE id = $1`, ciID).Scan(&version); err != nil {
			return fmt.Errorf("read ci version: %w", err)
		}
		if auditErr := recordAudit(ctx, tx, orgID, ciID, "ci.override_cleared", changes); auditErr != nil {
			return auditErr
		}
		out = r.withEffectiveTx(ctx, tx, fv)
		out.CIVersion = version
		return nil
	})
	return out, err
}

// Conflicts returns field values where discovered and effective diverge.
func (r *PGRepository) Conflicts(ctx context.Context, orgID string, page api.PaginationParams) ([]FieldValue, int, error) {
	var out []FieldValue
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// Divergence: an override exists and differs from the discovered value.
		const divergence = "override_value IS NOT NULL AND discovered_value IS NOT NULL AND override_value IS DISTINCT FROM discovered_value"
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM ci_field_value WHERE organization_id = $1 AND "+visibleCI+" AND "+divergence,
			orgID).Scan(&total); err != nil {
			return fmt.Errorf("count conflicts: %w", err)
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM ci_field_value WHERE organization_id = $1 AND %s AND %s ORDER BY updated_at DESC LIMIT $2 OFFSET $3",
			selectColumns, visibleCI, divergence), orgID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list conflicts: %w", err)
		}
		// The rows are read completely before the effective values are
		// resolved: the connection cannot run a query while rows are open.
		var values []*FieldValue
		for rows.Next() {
			fv, err := scan(rows)
			if err != nil {
				rows.Close()
				return err
			}
			values = append(values, fv)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, fv := range values {
			out = append(out, *r.withEffectiveTx(ctx, tx, fv))
		}
		return nil
	})
	return out, total, err
}

// Policy returns the tenant's default source-priority policy.
func (r *PGRepository) Policy(ctx context.Context, orgID string) (*SourcePolicy, error) {
	var out *SourcePolicy
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var p SourcePolicy
		var raw []byte
		err := tx.QueryRow(ctx, `
			SELECT id::text, organization_id::text, name, priorities, is_default
			FROM source_priority_policy
			WHERE organization_id = $1 AND is_default`, orgID).
			Scan(&p.ID, &p.OrganizationID, &p.Name, &raw, &p.IsDefault)
		if err != nil {
			if err == pgx.ErrNoRows {
				out = &SourcePolicy{
					OrganizationID: orgID, Name: "default",
					Priorities: append([]string{}, DefaultPriorities...), IsDefault: true,
				}
				return nil
			}
			return fmt.Errorf("get source policy: %w", err)
		}
		_ = json.Unmarshal(raw, &p.Priorities)
		out = &p
		return nil
	})
	return out, err
}

// UpsertPolicy stores the tenant's default source-priority policy.
func (r *PGRepository) UpsertPolicy(ctx context.Context, policy *SourcePolicy) error {
	if len(policy.Priorities) == 0 {
		return fmt.Errorf("priorities must not be empty")
	}
	return database.WithRequestTenant(ctx, r.pool, policy.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		name := policy.Name
		if name == "" {
			name = "default"
		}
		raw, _ := json.Marshal(policy.Priorities)
		_, err := tx.Exec(ctx, `
			INSERT INTO source_priority_policy (organization_id, name, priorities, is_default)
			VALUES ($1, $2, $3, true)
			ON CONFLICT (organization_id, name) DO UPDATE SET
				priorities = EXCLUDED.priorities, is_default = EXCLUDED.is_default`,
			policy.OrganizationID, name, string(raw))
		if err != nil {
			return fmt.Errorf("upsert source policy: %w", err)
		}
		return nil
	})
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*FieldValue, error) {
	fv := &FieldValue{}
	var discoveredRaw, overrideRaw []byte
	var discoveredAt, overrideAt, observedAt *time.Time
	if err := s.Scan(
		&fv.ID, &fv.OrganizationID, &fv.CIID, &fv.FieldName,
		&discoveredRaw, &fv.DiscoveredSource, &discoveredAt,
		&overrideRaw, &fv.OverrideAuthor, &fv.OverrideReason, &overrideAt,
		&fv.Protected, &fv.CreatedAt, &fv.UpdatedAt, &fv.DiscoveredRank, &observedAt,
	); err != nil {
		return nil, err
	}
	if len(discoveredRaw) > 0 {
		_ = json.Unmarshal(discoveredRaw, &fv.DiscoveredValue)
	}
	if len(overrideRaw) > 0 {
		_ = json.Unmarshal(overrideRaw, &fv.OverrideValue)
	}
	fv.DiscoveredAt = discoveredAt
	fv.ObservedAt = observedAt
	fv.OverrideAt = overrideAt
	return fv, nil
}

func jsonValue(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(raw)
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// ErrNotOverridable rejects an override of a structural CI column.
var ErrNotOverridable = errors.New("field cannot be overridden")

// overrideColumns maps the CI columns an override sets to their SQL type;
// structuralFields are columns no override touches. Every other field is an
// attribute. They mirror the effective view of the ci package.
var (
	overrideColumns = map[string]string{
		"name": "text", "status": "text", "manufacturer": "text", "model": "text",
		"serial_number": "text", "hardware_uuid": "uuid", "management_ip": "inet",
		"primary_mac": "macaddr", "hostname": "text", "fqdn": "text", "os_name": "text",
		"os_version": "text", "firmware_version": "text", "sys_object_id": "text",
	}
	structuralFields = map[string]bool{
		"id": true, "organization_id": true, "client_id": true, "location_id": true, "site_id": true,
		"room_id": true, "ci_type_id": true, "attributes": true, "discovery_source": true, "is_manual": true,
		"first_seen_at": true, "last_seen_at": true, "created_at": true, "updated_at": true, "deleted_at": true,
	}
)

// attributePath is the path of the attribute fieldName in ci.attributes:
// instance attributes live in attributes._instance (CI-10).
func attributePath(ctx context.Context, tx pgx.Tx, ciID, fieldName string) (path []string, instance bool, err error) {
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci_instance_field_definition WHERE ci_id = $1 AND name = $2)`,
		ciID, fieldName).Scan(&instance); err != nil {
		return nil, false, fmt.Errorf("instance attribute %s: %w", fieldName, err)
	}
	if instance {
		return []string{"_instance", fieldName}, true, nil
	}
	return []string{fieldName}, false, nil
}

// isColumn reports whether fieldName is a CI column rather than an
// attribute.
func isColumn(fieldName string) bool {
	_, ok := overrideColumns[fieldName]
	return ok || structuralFields[fieldName]
}

// ciFieldValue reads the value the CI row holds for the column or attribute
// fieldName (nil when there is none).
func ciFieldValue(ctx context.Context, tx pgx.Tx, ciID, fieldName string) (any, error) {
	var raw []byte
	if isColumn(fieldName) {
		// fieldName is one of the fixed column names of isColumn.
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT to_jsonb(%s) FROM ci WHERE id = $1`, fieldName), ciID).Scan(&raw); err != nil {
			return nil, fmt.Errorf("read ci %s: %w", fieldName, err)
		}
	} else {
		path, _, err := attributePath(ctx, tx, ciID, fieldName)
		if err != nil {
			return nil, err
		}
		if err := tx.QueryRow(ctx, `SELECT attributes #> $2::text[] FROM ci WHERE id = $1`, ciID, path).Scan(&raw); err != nil {
			return nil, fmt.Errorf("read ci %s: %w", fieldName, err)
		}
	}
	var value any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("decode ci %s: %w", fieldName, err)
		}
	}
	return value, nil
}

// raiseVersion records an override change as the manual write it is (rank
// 100, API-07): ci.version rises by one and a ci_change row of type override
// carries the field and the new version, so checkPrecondition of the ci
// package refuses a PATCH of the field with an older If-Match (409, F-021).
// The field is named as a PATCH names it: a column by its name, an attribute
// as "attributes" with the member as old and new value, since attributes
// count as one field there. Personal values are stored as vault surrogates
// (SEC-05), as in every CI change. It returns the new version.
func raiseVersion(ctx context.Context, tx pgx.Tx, orgID, ciID, fieldName string, oldValue, newValue any) (int64, error) {
	name := fieldName
	if !isColumn(fieldName) {
		name = "attributes"
		oldValue, newValue = map[string]any{fieldName: oldValue}, map[string]any{fieldName: newValue}
	}
	oldJSON, err := changeJSON(ctx, tx, orgID, name, oldValue)
	if err != nil {
		return 0, err
	}
	newJSON, err := changeJSON(ctx, tx, orgID, name, newValue)
	if err != nil {
		return 0, err
	}
	var version int64
	err = tx.QueryRow(ctx, `
		WITH raised AS (
			UPDATE ci SET version = version + 1, updated_at = now()
			WHERE id = $2 AND organization_id = $1
			RETURNING version
		)
		INSERT INTO ci_change (organization_id, ci_id, actor_id, change_type, field_name, old_value, new_value, ci_version)
		SELECT $1, $2, $3, 'override', $4, $5::jsonb, $6::jsonb, version FROM raised
		RETURNING ci_version`,
		orgID, ciID, userID(strings.TrimSpace(tenant.FromContext(ctx).UserID)), name, oldJSON, newJSON).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("raise ci version: %w", err)
	}
	return version, nil
}

// changeJSON is the stored JSON of a historized value of fieldName.
func changeJSON(ctx context.Context, tx pgx.Tx, orgID, fieldName string, value any) (string, error) {
	sealed, _, err := pii.SealFieldTx(ctx, tx, orgID, fieldName, value)
	if err != nil {
		return "", fmt.Errorf("seal ci change value: %w", err)
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		return "", fmt.Errorf("marshal ci change value: %w", err)
	}
	return string(raw), nil
}

// syncCIField writes value into the CI column or attribute fieldName; nil
// clears the column or removes the attribute.
func syncCIField(ctx context.Context, tx pgx.Tx, ciID, fieldName string, value any) error {
	var err error
	if sqlType, ok := overrideColumns[fieldName]; ok {
		var text any
		if value != nil {
			text = fmt.Sprint(value)
		}
		_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE ci SET %s = $2::text::%s, updated_at = now() WHERE id = $1`, fieldName, sqlType), ciID, text)
	} else {
		path, instance, pathErr := attributePath(ctx, tx, ciID, fieldName)
		if pathErr != nil {
			return pathErr
		}
		if value == nil {
			_, err = tx.Exec(ctx, `UPDATE ci SET attributes = COALESCE(attributes, '{}'::jsonb) #- $2::text[], updated_at = now() WHERE id = $1`, ciID, path)
		} else {
			_, err = tx.Exec(ctx, `UPDATE ci SET attributes = jsonb_set(
				CASE WHEN $4 AND NOT COALESCE(attributes, '{}'::jsonb) ? '_instance'
					THEN COALESCE(attributes, '{}'::jsonb) || '{"_instance": {}}'::jsonb ELSE COALESCE(attributes, '{}'::jsonb) END,
				$2::text[], $3::jsonb, true), updated_at = now() WHERE id = $1`, ciID, path, jsonValue(value), instance)
		}
	}
	if err != nil {
		return fmt.Errorf("update ci %s: %w", fieldName, err)
	}
	return nil
}

// recordAudit writes the audit entry of an override change on the CI.
func recordAudit(ctx context.Context, tx pgx.Tx, orgID, ciID, action string, changes map[string]any) error {
	actor := strings.TrimSpace(tenant.FromContext(ctx).UserID)
	actorType := "system"
	if actor != "" {
		actorType = "user"
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: action, ResourceType: "ci", ResourceID: ciID, Changes: changes,
	}); err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}
