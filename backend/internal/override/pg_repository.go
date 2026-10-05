package override

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const selectColumns = `
	id::text, organization_id::text, ci_id::text, field_name,
	discovered_value, COALESCE(discovered_source, ''), discovered_at,
	override_value, COALESCE(override_author::text, ''),
	COALESCE(override_reason, ''), override_at, protected,
	created_at, updated_at
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
// foreign key alone would accept a CI of another client.
func requireVisibleCI(ctx context.Context, tx pgx.Tx, ciID string) error {
	var visible bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1)", ciID).Scan(&visible); err != nil {
		return fmt.Errorf("check ci visibility: %w", err)
	}
	if !visible {
		return fmt.Errorf("not found")
	}
	return nil
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
func (r *PGRepository) RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string) (*FieldValue, error) {
	var out *FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireVisibleCI(ctx, tx, ciID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO ci_field_value (
				organization_id, ci_id, field_name, discovered_value,
				discovered_source, discovered_at
			) VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (ci_id, field_name) DO UPDATE SET
				discovered_value = EXCLUDED.discovered_value,
				discovered_source = EXCLUDED.discovered_source,
				discovered_at = EXCLUDED.discovered_at
			RETURNING %s`, selectColumns),
			orgID, ciID, fieldName, jsonValue(value), nilIfEmpty(source))
		fv, err := scan(row)
		if err != nil {
			return fmt.Errorf("record discovered value: %w", err)
		}
		out = r.withEffectiveTx(ctx, tx, fv)
		return nil
	})
	return out, err
}

// SetOverride sets or replaces the manual override of a field.
func (r *PGRepository) SetOverride(ctx context.Context, orgID, ciID, fieldName string, value any, author, reason string, protected bool) (*FieldValue, error) {
	if reason == "" {
		return nil, fmt.Errorf("override reason is required")
	}
	var out *FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireVisibleCI(ctx, tx, ciID); err != nil {
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
		out = r.withEffectiveTx(ctx, tx, fv)
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

// ClearOverride removes the manual override of a field.
func (r *PGRepository) ClearOverride(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error) {
	var out *FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			UPDATE ci_field_value SET
				override_value = NULL, override_author = NULL,
				override_reason = NULL, override_at = NULL, protected = false
			WHERE ci_id = $1 AND field_name = $2 AND %s
			RETURNING %s`, visibleCI, selectColumns), ciID, fieldName)
		fv, err := scan(row)
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("clear override: %w", err)
		}
		out = r.withEffectiveTx(ctx, tx, fv)
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
	var discoveredAt, overrideAt *time.Time
	if err := s.Scan(
		&fv.ID, &fv.OrganizationID, &fv.CIID, &fv.FieldName,
		&discoveredRaw, &fv.DiscoveredSource, &discoveredAt,
		&overrideRaw, &fv.OverrideAuthor, &fv.OverrideReason, &overrideAt,
		&fv.Protected, &fv.CreatedAt, &fv.UpdatedAt,
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
