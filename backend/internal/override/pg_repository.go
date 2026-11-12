package override

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
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
	// recompute follows a changed value with the computed attributes of
	// the CI (WithRecompute, WP-717); nil computes nothing.
	recompute Recomputer
}

// Recomputer recomputes the computed attributes of a CI in the caller's
// transaction (WP-717, MET-10 computed). The CI repository implements it;
// this package cannot import it (the CI package imports this one).
type Recomputer interface {
	RecomputeTx(ctx context.Context, tx pgx.Tx, orgID, ciID string) error
}

// WithRecompute makes SetOverride and ClearOverride recompute the computed
// attributes of the CI after they changed one of its values, in their
// transaction: the new values belong to the version step of the override
// (no second version, no second ci_change row).
func (r *PGRepository) WithRecompute(recompute Recomputer) *PGRepository {
	r.recompute = recompute
	return r
}

// errComputed refuses an override of a computed attribute (WP-717): its
// value follows its expression.
var errComputed = fmt.Errorf("%w: the attribute is computed", ErrNotOverridable)

// checkNotComputed refuses fieldName when it is a computed attribute of the
// CI (attribute_definition.computed, WP-258): a global definition, one of
// the type chain of the CI or of an attribute set of that chain. A layer
// cannot remove an expression (MET-16), so one computed definition makes
// the attribute computed.
func checkNotComputed(ctx context.Context, tx pgx.Tx, ciID, fieldName string) error {
	if isColumn(fieldName) {
		return nil
	}
	var computed bool
	if err := tx.QueryRow(ctx, `
		WITH chain AS (SELECT c.id FROM ci JOIN LATERAL ci_type_ancestors(ci.ci_type_id) c ON true WHERE ci.id = $1)
		SELECT EXISTS (SELECT 1 FROM attribute_definition a
			WHERE a.key = $2 AND a.computed IS NOT NULL AND (
				a.scope = 'global'
				OR a.ci_type_id IN (SELECT id FROM chain)
				OR a.attribute_set_id IN (SELECT s.attribute_set_id FROM ci_type_attribute_set s WHERE s.ci_type_id IN (SELECT id FROM chain))))`,
		ciID, fieldName).Scan(&computed); err != nil {
		return fmt.Errorf("read computed attribute %s: %w", fieldName, err)
	}
	if computed {
		return errComputed
	}
	return nil
}

// recomputeTx recomputes the computed attributes of the CI (WithRecompute).
func (r *PGRepository) recomputeTx(ctx context.Context, tx pgx.Tx, orgID, ciID string) error {
	if r.recompute == nil {
		return nil
	}
	return r.recompute.RecomputeTx(ctx, tx, orgID, ciID)
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

// liveCI is visibleCI without soft-deleted CIs: the field values of a
// deleted CI are gone for the manual paths (GET /cis/{id}/fields, override
// set and clear, the accept of an override_conflict item) as the CI is for
// GET /cis/{id} (F-422).
const liveCI = "EXISTS (SELECT 1 FROM ci WHERE ci.id = ci_field_value.ci_id AND ci.deleted_at IS NULL)"

// requireVisibleCI rejects writes to a CI the tenant scope does not show; the
// foreign key alone would accept a CI of another client. It locks the CI row
// (lockCI).
func requireVisibleCI(ctx context.Context, tx pgx.Tx, ciID string) error {
	visible, _, err := lockCI(ctx, tx, ciID)
	if err != nil {
		return err
	}
	if !visible {
		return fmt.Errorf("not found")
	}
	return nil
}

// requireLiveCI is requireVisibleCI for a manual write: a soft-deleted CI
// is not found either (F-422).
func requireLiveCI(ctx context.Context, tx pgx.Tx, ciID string) error {
	_, live, err := lockCI(ctx, tx, ciID)
	if err != nil {
		return err
	}
	if !live {
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
// are not written either. live reports a visible CI that is not
// soft-deleted.
func lockCI(ctx context.Context, tx pgx.Tx, ciID string) (visible, live bool, err error) {
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1),
		EXISTS (SELECT 1 FROM ci WHERE id = $1 AND deleted_at IS NULL),
		EXISTS (SELECT 1 FROM ci WHERE id = $1 FOR UPDATE)`, ciID).Scan(&visible, &live, &locked); err != nil {
		return false, false, fmt.Errorf("check ci visibility: %w", err)
	}
	return visible, live, nil
}

// ListForCI returns all tracked field values of a CI with resolved effective
// values and divergence flags. A CI that is unknown, not visible or
// soft-deleted is ErrNotFound (F-422).
func (r *PGRepository) ListForCI(ctx context.Context, orgID, ciID string) ([]FieldValue, error) {
	var out []FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var live bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1 AND deleted_at IS NULL)`, ciID).Scan(&live); err != nil {
			return fmt.Errorf("check ci visibility: %w", err)
		}
		if !live {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM ci_field_value WHERE ci_id = $1 AND "+liveCI+" ORDER BY field_name ASC",
			selectColumns), ciID)
		if err != nil {
			return fmt.Errorf("list field values: %w", err)
		}
		// The rows are read completely before the effective values are
		// resolved: the connection cannot run a query while rows are open.
		var values []*FieldValue
		for rows.Next() {
			fv, scanErr := scan(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			values = append(values, fv)
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		names, err := authorNamesTx(ctx, tx, values)
		if err != nil {
			return err
		}
		for _, fv := range values {
			resolved := r.withEffectiveTx(ctx, tx, fv)
			resolved.OverrideAuthorName = names[fv.OverrideAuthor]
			out = append(out, *resolved)
		}
		return nil
	})
	return out, err
}

// authorNamesTx resolves the display names of the override authors of
// values in the reader's tenant transaction (UI-15, REC-10): the display
// name of a user, the name of a service account. Only the id is stored
// (SEC-05); the RLS of app_user and service_account keeps the names of
// other organizations out, and a deleted or unknown author has no name.
func authorNamesTx(ctx context.Context, tx pgx.Tx, values []*FieldValue) (map[string]string, error) {
	seen := map[string]bool{}
	var ids []string
	for _, fv := range values {
		if fv.OverrideAuthor != "" && !seen[fv.OverrideAuthor] {
			seen[fv.OverrideAuthor] = true
			ids = append(ids, fv.OverrideAuthor)
		}
	}
	names := map[string]string{}
	if len(ids) == 0 {
		return names, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, display_name FROM app_user WHERE id = ANY($1::text[]::uuid[])
		UNION ALL
		SELECT id::text, name FROM service_account WHERE id = ANY($1::text[]::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve override authors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("resolve override authors: %w", err)
		}
		if name != "" {
			names[id] = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve override authors: %w", err)
	}
	return names, nil
}

// Get returns one field value. It marks an instance attribute of the CI
// (FieldValue.InstanceAttribute, MET-14, WP-714); for an instance attribute
// without provenance row it returns a FieldValue with only the keys and
// that mark instead of ErrNotFound, so the central write decision refuses
// it (ReasonInstanceAttribute).
func (r *PGRepository) Get(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error) {
	return r.get(ctx, orgID, ciID, fieldName, true)
}

// getForDecision is Get without EffectiveValue and Diverged, which
// DecideAutomatedWrite does not read: it saves the policy query.
func (r *PGRepository) getForDecision(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error) {
	return r.get(ctx, orgID, ciID, fieldName, false)
}

func (r *PGRepository) get(ctx context.Context, orgID, ciID, fieldName string, effective bool) (*FieldValue, error) {
	var out *FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// One statement: the row, if any, and whether the field is an
		// instance attribute; without a row, an instance attribute yields
		// one row with an empty id.
		var instance bool
		fv, err := scan(instanceScanner{tx.QueryRow(ctx, fmt.Sprintf(`
			WITH inst AS (
				SELECT EXISTS (SELECT 1 FROM ci_instance_field_definition d
					WHERE d.ci_id = $1 AND d.name = $2 AND EXISTS (SELECT 1 FROM ci WHERE ci.id = d.ci_id)) AS instance
			)
			SELECT %[1]s, inst.instance FROM ci_field_value, inst
			WHERE ci_id = $1 AND field_name = $2 AND %[2]s
			UNION ALL
			SELECT '', '', '', '', NULL::jsonb, '', NULL::timestamptz, NULL::jsonb, '', '', NULL::timestamptz, false,
				now(), now(), 0, NULL::timestamptz, true
			FROM inst WHERE inst.instance AND NOT EXISTS (
				SELECT 1 FROM ci_field_value WHERE ci_id = $1 AND field_name = $2 AND %[2]s)`,
			selectColumns, visibleCI), ciID, fieldName), &instance})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("get field value: %w", err)
		}
		if fv.ID == "" {
			out = &FieldValue{OrganizationID: orgID, CIID: ciID, FieldName: fieldName, InstanceAttribute: true}
			return nil
		}
		out = fv
		if effective {
			out = r.withEffectiveTx(ctx, tx, fv)
		}
		out.InstanceAttribute = instance
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
	rank, rankErr := r.recordRankSQL(ctx, orgID, "(SELECT rc.ci_type_id FROM ci rc WHERE rc.id = $2::uuid)", "$3::text", "$5::text", "$6::integer")
	if rankErr != nil {
		return nil, rankErr
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
			) VALUES ($1, $2, $3, $4, $5, now(), %s, $7)
			ON CONFLICT (ci_id, field_name) DO UPDATE SET
				discovered_value = EXCLUDED.discovered_value,
				discovered_source = EXCLUDED.discovered_source,
				discovered_at = EXCLUDED.discovered_at,
				discovered_rank = EXCLUDED.discovered_rank,
				observed_at = EXCLUDED.observed_at
			RETURNING %s`, rank, selectColumns),
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
	rank, err := r.recordRankSQL(ctx, orgID, "(SELECT rc.ci_type_id FROM ci rc WHERE rc.id = $2::uuid)", "f.name", "$5::text", "$6::integer")
	if err != nil {
		return err
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
			SELECT $1, $2, f.name, f.value::jsonb, $5, now(), `+rank+`, $7
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
	// Rank is the rank stored with the values; 0 is SourceRank(Source).
	// The computed attributes of a new CI (source computed, WP-717) carry
	// 100, which SourceRanks (REC-03) does not know.
	Rank int
}

// recordDiscoveredBatchSQL upserts the discovered values of the fields of
// several CIs ($2 … $7, one element per field) whose CIs ($8, each once) the
// tenant scope shows, with the effective rank of each value (REC-04);
// recordDiscoveredBatchPlainSQL is the statement for an organization
// without source ranks, which records the given ranks.
var recordDiscoveredBatchSQL = recordDiscoveredBatchWith(effectiveRankSQL("$1::uuid", "visible.ci_type_id", "f.name", "f.source", "f.rank"))

var recordDiscoveredBatchPlainSQL = recordDiscoveredBatchWith("f.rank")

func recordDiscoveredBatchWith(rank string) string {
	return `
	WITH visible AS MATERIALIZED (
		SELECT v.id, v.ci_type_id FROM unnest($8::uuid[]) AS k(id)
		CROSS JOIN LATERAL (SELECT ci.id, ci.ci_type_id FROM ci WHERE ci.id = k.id) v
	)
	INSERT INTO ci_field_value (
		organization_id, ci_id, field_name, discovered_value,
		discovered_source, discovered_at, discovered_rank, observed_at
	)
	SELECT $1, f.ci_id::uuid, f.name, f.value::jsonb, f.source, now(),
		` + rank + `, f.observed_at
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
}

// RecordDiscoveredBatch upserts the discovered columns of the fields of
// several CIs, each field at most once in batch, with one statement, each CI
// like RecordDiscoveredMany (NFR-03, WP-333); the override columns stay
// untouched. A CI may come in several entries with different sources and
// fields, e.g. its discovered fields and its computed attributes (rank
// 100, WP-717). The visibility check is
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
			rank := b.Rank
			if rank == 0 {
				rank = SourceRank(b.Source)
			}
			ids, names, raw = append(ids, b.CIID), append(names, name), append(raw, value)
			sources, ranks, observed = append(sources, source), append(ranks, int32(rank)), append(observed, observedAt.UTC())
		}
	}
	if len(ids) == 0 {
		return nil
	}
	table, err := r.rankTable(ctx, orgID)
	if err != nil {
		return err
	}
	statement := recordDiscoveredBatchSQL
	if table.empty() {
		statement = recordDiscoveredBatchPlainSQL
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, statement, orgID, ids, names, raw, sources, ranks, observed, cis)
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
// A reserved namespace is ErrReservedName, a structural column or an
// instance attribute of the CI (MET-14, WP-256) ErrNotOverridable.
func (r *PGRepository) SetOverride(ctx context.Context, orgID, ciID, fieldName string, value any, author, reason string, protected bool) (*FieldValue, error) {
	if reason == "" {
		return nil, fmt.Errorf("override reason is required")
	}
	if IsReservedName(fieldName) {
		return nil, ErrReservedName
	}
	if structuralFields[fieldName] {
		return nil, ErrNotOverridable
	}
	var out *FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireLiveCI(ctx, tx, ciID); err != nil {
			return err
		}
		// An instance attribute of the CI has its own write path with its
		// own permission (MET-14, WP-256): PUT /cis/{id}/instance-attributes.
		_, instance, pathErr := attributePath(ctx, tx, ciID, fieldName)
		if pathErr != nil {
			return pathErr
		}
		if instance {
			return ErrNotOverridable
		}
		if computedErr := checkNotComputed(ctx, tx, ciID, fieldName); computedErr != nil {
			return computedErr
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
		// The CI shows the override at once (OVR-01, CI-10), its computed
		// attributes follow (WP-717).
		if syncErr := syncCIField(ctx, tx, ciID, fieldName, value); syncErr != nil {
			return syncErr
		}
		if recomputeErr := r.recomputeTx(ctx, tx, orgID, ciID); recomputeErr != nil {
			return recomputeErr
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
	if IsReservedName(fieldName) {
		return ErrReservedName
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
// transaction the CI returns to the observed value, when there is one and
// the field is no instance attribute of the CI (MET-14, WP-714), its
// version rises and its history records the change (raiseVersion, API-07),
// and the change is audited (OVR-01). An unknown, invisible or soft-deleted
// CI (F-422) and a field without a value are ErrNotFound, a reserved
// namespace ErrReservedName; a field without an override keeps the version.
func (r *PGRepository) ClearOverride(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error) {
	if IsReservedName(fieldName) {
		return nil, ErrReservedName
	}
	var out *FieldValue
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// The CI row first (lockCI).
		_, live, err := lockCI(ctx, tx, ciID)
		if err != nil {
			return err
		}
		if !live {
			return ErrNotFound
		}
		if computedErr := checkNotComputed(ctx, tx, ciID, fieldName); computedErr != nil {
			return computedErr
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
			RETURNING %s`, liveCI, selectColumns), ciID, fieldName)
		fv, err := scan(row)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("clear override: %w", err)
		}
		changes := map[string]any{"field": fieldName}
		after := before
		// An instance attribute keeps its manual value: an observed value
		// never reaches attributes._instance (MET-14, WP-714).
		instance := false
		if !isColumn(fieldName) {
			var pathErr error
			if _, instance, pathErr = attributePath(ctx, tx, ciID, fieldName); pathErr != nil {
				return pathErr
			}
		}
		if fv.DiscoveredAt != nil && !structuralFields[fieldName] && !instance {
			if syncErr := syncCIField(ctx, tx, ciID, fieldName, fv.DiscoveredValue); syncErr != nil {
				return syncErr
			}
			if recomputeErr := r.recomputeTx(ctx, tx, orgID, ciID); recomputeErr != nil {
				return recomputeErr
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

// OverrideState is the state of a field's override that RestoreOverrideTx
// writes back: value (nil: the field is overridden with null), author (a
// user id, otherwise none), reason, protection and the time it was set.
type OverrideState struct {
	Value     any
	Author    string
	Reason    string
	At        time.Time
	Protected bool
}

// ClearOverrideTx removes the override of a field in the caller's
// transaction tx, like ClearOverride: the CI returns to the observed value
// when there is one and the field is no structural column or instance
// attribute of the CI, and the change is audited (ci.override_cleared).
// With raise the CI row is locked first (lockCI), its version rises and a
// ci_change row override records the change (raiseVersion, WP-412);
// without, the caller holds the row lock and its write in tx raises the
// version already (a field update; a second raise would make F-021 again).
// The computed attributes are not recomputed: a caller whose write feeds
// them writes the observed value itself. A field without override is no
// error: it reports false and changes nothing.
func ClearOverrideTx(ctx context.Context, tx pgx.Tx, orgID, ciID, fieldName string, raise bool) (bool, error) {
	if IsReservedName(fieldName) {
		return false, ErrReservedName
	}
	var before any
	if raise {
		if _, live, err := lockCI(ctx, tx, ciID); err != nil {
			return false, err
		} else if !live {
			return false, ErrNotFound
		}
		var err error
		if before, err = ciFieldValue(ctx, tx, ciID, fieldName); err != nil {
			return false, err
		}
	}
	var discoveredRaw []byte
	var discoveredSource *string
	var discoveredAt *time.Time
	err := tx.QueryRow(ctx, `
		UPDATE ci_field_value SET
			override_value = NULL, override_author = NULL,
			override_reason = NULL, override_at = NULL, protected = false
		WHERE ci_id = $1 AND field_name = $2 AND (override_at IS NOT NULL OR override_value IS NOT NULL)
		RETURNING discovered_value, discovered_source, discovered_at`, ciID, fieldName).
		Scan(&discoveredRaw, &discoveredSource, &discoveredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("clear override %s: %w", fieldName, err)
	}
	changes := map[string]any{"field": fieldName}
	after := before
	instance := false
	if !isColumn(fieldName) {
		var pathErr error
		if _, instance, pathErr = attributePath(ctx, tx, ciID, fieldName); pathErr != nil {
			return false, pathErr
		}
	}
	if discoveredAt != nil && !structuralFields[fieldName] && !instance {
		var discovered any
		if len(discoveredRaw) > 0 {
			if decodeErr := json.Unmarshal(discoveredRaw, &discovered); decodeErr != nil {
				return false, fmt.Errorf("decode discovered %s: %w", fieldName, decodeErr)
			}
		}
		if syncErr := syncCIField(ctx, tx, ciID, fieldName, discovered); syncErr != nil {
			return false, syncErr
		}
		changes["restored_value"] = discovered
		if discoveredSource != nil {
			changes["restored_source"] = *discoveredSource
		}
		after = discovered
	}
	if raise {
		if _, raiseErr := raiseVersion(ctx, tx, orgID, ciID, fieldName, before, after); raiseErr != nil {
			return false, raiseErr
		}
	}
	if auditErr := recordAudit(ctx, tx, orgID, ciID, "ci.override_cleared", changes); auditErr != nil {
		return false, auditErr
	}
	return true, nil
}

// RestoreOverrideTx writes the override state of a field back in the
// caller's transaction tx: value, author, reason, protection and the
// explicit time state.At, over whatever override the field has now. The CI
// takes the value when it holds another one (not for a structural column,
// which the caller's write sets), and the change is audited
// (ci.override_set). raise is as in ClearOverrideTx.
func RestoreOverrideTx(ctx context.Context, tx pgx.Tx, orgID, ciID, fieldName string, state *OverrideState, raise bool) error {
	if IsReservedName(fieldName) {
		return ErrReservedName
	}
	if strings.TrimSpace(state.Reason) == "" {
		return fmt.Errorf("override reason is required")
	}
	if raise {
		if _, live, err := lockCI(ctx, tx, ciID); err != nil {
			return err
		} else if !live {
			return ErrNotFound
		}
	}
	at := state.At
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ci_field_value (
			organization_id, ci_id, field_name, override_value,
			override_author, override_reason, override_at, protected
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (ci_id, field_name) DO UPDATE SET
			override_value = EXCLUDED.override_value,
			override_author = EXCLUDED.override_author,
			override_reason = EXCLUDED.override_reason,
			override_at = EXCLUDED.override_at,
			protected = EXCLUDED.protected`,
		orgID, ciID, fieldName, jsonValue(state.Value), userID(state.Author), state.Reason, at.UTC(), state.Protected); err != nil {
		return fmt.Errorf("restore override %s: %w", fieldName, err)
	}
	before, err := ciFieldValue(ctx, tx, ciID, fieldName)
	if err != nil {
		return err
	}
	if !structuralFields[fieldName] && !sameValue(before, state.Value) {
		if syncErr := syncCIField(ctx, tx, ciID, fieldName, state.Value); syncErr != nil {
			return syncErr
		}
	}
	if raise {
		if _, raiseErr := raiseVersion(ctx, tx, orgID, ciID, fieldName, before, state.Value); raiseErr != nil {
			return raiseErr
		}
	}
	return recordAudit(ctx, tx, orgID, ciID, "ci.override_set", map[string]any{
		"field": fieldName, "value": state.Value, "reason": state.Reason, "protected": state.Protected,
		"restored_at": at.UTC(),
	})
}

// sameValue reports whether two field values have the same JSON.
func sameValue(a, b any) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
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

// instanceScanner scans a row of selectColumns followed by the instance
// attribute flag (Get).
type instanceScanner struct {
	row      pgx.Row
	instance *bool
}

func (s instanceScanner) Scan(dest ...any) error {
	return s.row.Scan(append(dest, s.instance)...)
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

// ErrNotOverridable rejects an override of a structural CI column or of an
// instance attribute of the CI (MET-14).
var ErrNotOverridable = errors.New("field cannot be overridden")

// ErrReservedName rejects a reserved attribute namespace (_overrides,
// _observed, _provenance, _instance) as the field of an override: the
// namespaces are no fields (GLO-13, CI-10, E-23); the handler answers 400
// with the field "name". The CHECK constraint
// ci_field_value_field_name_not_reserved (migration 000166) holds the rule
// in the database as well.
var ErrReservedName = errors.New("reserved attribute namespace, not a field name (GLO-13)")

// IsReservedName reports whether fieldName is a reserved attribute
// namespace (GLO-13).
func IsReservedName(fieldName string) bool {
	return fieldmeta.ReservedNames[fieldName]
}

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

// effectiveRankSQL is the SQL expression of the effective rank (REC-04) of
// source for field of a CI of type ciType in organization org: the entry of
// the CI's type, else the organization-wide entry, else def. Manual and
// unknown sources have no entries (CHECK of source_rank_policy) and keep
// def.
func effectiveRankSQL(org, ciType, field, source, def string) string {
	return fmt.Sprintf(`COALESCE(
		(SELECT p.rank FROM source_rank_policy p
		  WHERE p.organization_id = %[1]s AND p.ci_type_id = %[2]s AND p.attribute_key = %[3]s AND p.source = %[4]s),
		(SELECT p.rank FROM source_rank_policy p
		  WHERE p.organization_id = %[1]s AND p.ci_type_id IS NULL AND p.attribute_key = %[3]s AND p.source = %[4]s),
		%[5]s)`, org, ciType, field, source, def)
}

// recordRankSQL is the SQL of the rank a recorded value stores: the
// effective rank expression (effectiveRankSQL) when the organization keeps
// source ranks, else def, so an organization without entries records as
// before without reading the policy per value.
func (r *PGRepository) recordRankSQL(ctx context.Context, orgID, ciType, field, source, def string) (string, error) {
	table, err := r.rankTable(ctx, orgID)
	if err != nil {
		return "", err
	}
	if table.empty() {
		return def, nil
	}
	return effectiveRankSQL(recordOrgParam, ciType, field, source, def), nil
}

// recordOrgParam is the organization parameter of the record statements.
const recordOrgParam = "$1::uuid"

// decisionState reads the provenance of a field and the CI type of the CI
// in one statement (REC-04): fv is nil without a provenance row, a
// FieldValue with only InstanceAttribute set for an instance attribute
// without one (as Get), and the CI type is empty for a CI the scope does
// not show.
func (r *PGRepository) decisionState(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, string, error) {
	var out *FieldValue
	var ciType string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var instance bool
		fv, err := scan(decisionScanner{row: tx.QueryRow(ctx, fmt.Sprintf(`
			WITH ct AS (
				SELECT COALESCE(ci.ci_type_id::text, '') AS type_id FROM ci WHERE ci.id = $1
			), inst AS (
				SELECT EXISTS (SELECT 1 FROM ci_instance_field_definition d
					WHERE d.ci_id = $1 AND d.name = $2 AND EXISTS (SELECT 1 FROM ci WHERE ci.id = d.ci_id)) AS instance
			)
			SELECT %[1]s, inst.instance, COALESCE((SELECT type_id FROM ct), '') FROM ci_field_value, inst
			WHERE ci_id = $1 AND field_name = $2 AND %[2]s
			UNION ALL
			SELECT '', '', '', '', NULL::jsonb, '', NULL::timestamptz, NULL::jsonb, '', '', NULL::timestamptz, false,
				now(), now(), 0, NULL::timestamptz, inst.instance, COALESCE((SELECT type_id FROM ct), '')
			FROM inst WHERE NOT EXISTS (
				SELECT 1 FROM ci_field_value WHERE ci_id = $1 AND field_name = $2 AND %[2]s)`,
			selectColumns, visibleCI), ciID, fieldName), instance: &instance, ciType: &ciType})
		if err != nil {
			return fmt.Errorf("get field value for decision: %w", err)
		}
		switch {
		case fv.ID != "":
			fv.InstanceAttribute = instance
			out = fv
		case instance:
			out = &FieldValue{OrganizationID: orgID, CIID: ciID, FieldName: fieldName, InstanceAttribute: true}
		}
		return nil
	})
	return out, ciType, err
}

// decisionScanner scans a row of selectColumns followed by the instance
// attribute flag and the CI type (decisionState).
type decisionScanner struct {
	row      pgx.Row
	instance *bool
	ciType   *string
}

func (s decisionScanner) Scan(dest ...any) error {
	return s.row.Scan(append(dest, s.instance, s.ciType)...)
}

// rankTable returns the rank table of the organization from the cache of
// the instance, read anew after rankCacheTTL or a write on this instance.
func (r *PGRepository) rankTable(ctx context.Context, orgID string) (*RankTable, error) {
	t, gen, ok := ranksOfInstance.get(orgID)
	if ok {
		return t, nil
	}
	entries, err := r.SourceRankEntries(ctx, orgID)
	if err != nil {
		return nil, err
	}
	t = NewRankTable(entries)
	// A write that committed and invalidated while this read ran may be
	// missing from the table: it then serves this decision only.
	ranksOfInstance.putIfGeneration(orgID, gen, t)
	return t, nil
}

const rankEntryColumns = `COALESCE(ci_type_id::text, ''), attribute_key, source, rank, COALESCE(updated_by::text, ''), updated_at`

// SourceRankEntries returns the stored source ranks of the organization
// (REC-04), sorted by attribute, type and source.
func (r *PGRepository) SourceRankEntries(ctx context.Context, orgID string) ([]RankEntry, error) {
	var out []RankEntry
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = rankEntriesTx(ctx, tx, orgID, false)
		return err
	})
	return out, err
}

func rankEntriesTx(ctx context.Context, tx pgx.Tx, orgID string, lock bool) ([]RankEntry, error) {
	query := "SELECT " + rankEntryColumns + " FROM source_rank_policy WHERE organization_id = $1"
	if lock {
		query += " FOR UPDATE"
	}
	rows, err := tx.Query(ctx, query, orgID)
	if err != nil {
		return nil, fmt.Errorf("read source ranks: %w", err)
	}
	defer rows.Close()
	out := []RankEntry{}
	for rows.Next() {
		var e RankEntry
		var at time.Time
		if err := rows.Scan(&e.CITypeID, &e.AttributeKey, &e.Source, &e.Rank, &e.UpdatedBy, &at); err != nil {
			return nil, fmt.Errorf("read source ranks: %w", err)
		}
		at = at.UTC()
		e.UpdatedAt = &at
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read source ranks: %w", err)
	}
	sortEntries(out)
	return out, nil
}

// ReplaceSourceRanks replaces the source ranks of the organization with
// entries (validated by the caller) and returns the former ones. A CI type
// that is neither a system type nor one of the organization is a 422 field
// error. The audit entry reconciliation.source_ranks_updated with before
// and after is written in the transaction; after the commit the rank
// table of this instance is read anew (REC-04, AK6 of WP-1105).
func (r *PGRepository) ReplaceSourceRanks(ctx context.Context, orgID, actor string, entries []RankEntry) (before, after []RankEntry, txErr error) {
	txErr = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// One replacement at a time per organization.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('source_rank_policy:' || $1::uuid::text, 0))`, orgID); err != nil {
			return fmt.Errorf("lock source ranks: %w", err)
		}
		if err := checkRankTypes(ctx, tx, orgID, entries); err != nil {
			return err
		}
		var err error
		if before, err = rankEntriesTx(ctx, tx, orgID, true); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM source_rank_policy WHERE organization_id = $1`, orgID); err != nil {
			return fmt.Errorf("replace source ranks: %w", err)
		}
		if len(entries) > 0 {
			types, keys, sources, ranks := make([]*string, len(entries)), make([]string, len(entries)), make([]string, len(entries)), make([]int32, len(entries))
			for i := range entries {
				if entries[i].CITypeID != "" {
					types[i] = &entries[i].CITypeID
				}
				keys[i], sources[i], ranks[i] = entries[i].AttributeKey, entries[i].Source, int32(entries[i].Rank) //nolint:gosec // rank is 1-99, checked by the caller
			}
			var by any
			if api.IsUUID(actor) {
				by = actor
			}
			if _, err = tx.Exec(ctx, `
				INSERT INTO source_rank_policy (organization_id, ci_type_id, attribute_key, source, rank, updated_by)
				SELECT $1, e.ci_type_id, e.attribute_key, e.source, e.rank,
					(SELECT u.id FROM app_user u WHERE u.id = $6::uuid AND u.organization_id = $1)
				FROM unnest($2::uuid[], $3::text[], $4::text[], $5::integer[]) AS e(ci_type_id, attribute_key, source, rank)`,
				orgID, types, keys, sources, ranks, by); err != nil {
				return fmt.Errorf("replace source ranks: %w", err)
			}
		}
		if after, err = rankEntriesTx(ctx, tx, orgID, false); err != nil {
			return err
		}
		err = database.BeforeCommit(ctx, func() error {
			return recordRankAudit(ctx, tx, orgID, actor, before, after)
		})
		if err != nil {
			return err
		}
		database.AfterCommit(ctx, func(context.Context) { ranksOfInstance.invalidate(orgID) })
		return nil
	})
	if txErr != nil {
		return nil, nil, txErr
	}
	return before, after, nil
}

// checkRankTypes refuses a CI type that is neither a system type nor one
// of the organization (422 on the field of the first such entry).
func checkRankTypes(ctx context.Context, tx pgx.Tx, orgID string, entries []RankEntry) error {
	ids := []string{}
	for i := range entries {
		if entries[i].CITypeID != "" {
			ids = append(ids, entries[i].CITypeID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM ci_type WHERE id = ANY ($1::uuid[]) AND (organization_id IS NULL OR organization_id = $2)`, ids, orgID)
	if err != nil {
		return fmt.Errorf("check CI types: %w", err)
	}
	known := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("check CI types: %w", err)
		}
		known[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check CI types: %w", err)
	}
	for i := range entries {
		if entries[i].CITypeID != "" && !known[entries[i].CITypeID] {
			return &api.FieldError{Field: fmt.Sprintf("entries[%d].ci_type_id", i), Message: "unknown CI type", Status: http.StatusUnprocessableEntity}
		}
	}
	return nil
}

// recordRankAudit writes reconciliation.source_ranks_updated with the
// entries before and after the change.
func recordRankAudit(ctx context.Context, tx pgx.Tx, orgID, actor string, before, after []RankEntry) error {
	actorType := "system"
	if strings.TrimSpace(actor) != "" {
		actorType = "user"
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: AuditSourceRanksUpdated, ResourceType: "reconciliation",
		Changes: map[string]any{"before": auditEntries(before), "after": auditEntries(after)},
	}); err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}

// auditEntries are the entries without bookkeeping columns.
func auditEntries(entries []RankEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for i := range entries {
		e := map[string]any{"attribute_key": entries[i].AttributeKey, "source": entries[i].Source, "rank": entries[i].Rank}
		if entries[i].CITypeID != "" {
			e["ci_type_id"] = entries[i].CITypeID
		}
		out = append(out, e)
	}
	return out
}
