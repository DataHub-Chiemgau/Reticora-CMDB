package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// Template migration of a CI (WP-253, OVR-03, MET-16): the assignment of a
// template (ci_template, WP-710) moves from one published version to
// another. The defaults of the new version go into the attribute fields
// without an effective value; overridden fields (ci_field_value with
// override_at) and instance attributes (attributes._instance) are never
// written, ci_field_value stays as it is (OVR-02, OVR-03). The defaults are
// no manual write and create no override. Each migrated CI gets one
// ci_change row template_migration in a change set of its own, the audit
// entry ci.template_migrated and the event ci.changed. The job type
// template.migrate, its endpoint, trigger scope and report are WP-712.

// Outcomes of a template migration of one CI.
const (
	TemplateMigrated = "migrated"
	TemplateSkipped  = "skipped"
)

// ChangeTypeTemplateMigration is the ci_change class of a template
// migration (chk_ci_change_type, migration 000210).
const ChangeTypeTemplateMigration = "template_migration"

// ErrNoTemplateResolver: the plan carries no TemplateFieldResolver, so the
// definitions of the target version cannot be composed.
var ErrNoTemplateResolver = errors.New("template migration needs a template field resolver")

// TemplateMigrationPlan is the migration of one template from FromVersion
// to ToVersion. Fields composes the definitions of a CI with a given
// template list (WP-710; citype.PGRepository implements it, citype does not
// import ci). JobID names the job of the migration (WP-712), "" without.
type TemplateMigrationPlan struct {
	TemplateID  string
	FromVersion int
	ToVersion   int
	JobID       string
	Fields      TemplateFieldResolver
}

// TemplateMigrationResult is the outcome for one CI: migrated or skipped
// (the assignment is not on FromVersion, or the CI has none), the keys of
// the written defaults, the keys of the fields with a default of the
// target version that an override keeps unchanged, and the findings of the
// validation against the target version (they do not block, like CI-11).
// CI is the CI after the migration (before it for skipped and a dry run).
type TemplateMigrationResult struct {
	CIID       string      `json:"ci_id"`
	Outcome    string      `json:"outcome"`
	Defaults   []string    `json:"defaults"`
	Overridden []string    `json:"overridden"`
	Findings   []Violation `json:"findings"`
	CI         *Item       `json:"-"`
}

// TemplateMigrator migrates the template assignment of one CI (WP-253).
// With dryRun it computes the same result without locking or writing.
type TemplateMigrator interface {
	MigrateTemplate(ctx context.Context, orgID, ciID string, plan TemplateMigrationPlan, dryRun bool) (*TemplateMigrationResult, error)
}

// check refuses a plan that names no template or no two different
// versions (422).
func (p *TemplateMigrationPlan) check() error {
	var violations []Violation
	if !api.IsUUID(p.TemplateID) {
		violations = append(violations, Violation{Field: "template_id", Detail: "not the ID (UUID) of a template"})
	}
	if p.FromVersion < 1 {
		violations = append(violations, Violation{Field: "from_version", Detail: "must be a version (1 or more)"})
	}
	switch {
	case p.ToVersion < 1:
		violations = append(violations, Violation{Field: "to_version", Detail: "must be a version (1 or more)"})
	case p.ToVersion == p.FromVersion:
		violations = append(violations, Violation{Field: "to_version", Detail: "must differ from from_version"})
	}
	if len(violations) > 0 {
		return &ValidationError{Violations: violations}
	}
	if p.Fields == nil {
		return ErrNoTemplateResolver
	}
	return nil
}

// templatesOnly makes a TemplateFieldResolver usable for resolveWithRules,
// which then always resolves with the given template list.
type templatesOnly struct{ TemplateFieldResolver }

func (templatesOnly) ResolveFields(context.Context, string, string, string) ([]fieldmeta.FieldDefinition, error) {
	return nil, ErrNoTemplateResolver
}

// templateMigration is the computed migration of one CI row.
type templateMigration struct {
	result   TemplateMigrationResult
	before   *Item
	defaults map[string]any
	// stored is the write form of the attributes with the defaults.
	stored map[string]any
}

// MigrateTemplate implements TemplateMigrator. It runs through
// database.WithRequestTenant and so joins a transaction of the context
// (WP-712; under database.Savepoint a *UniqueError discards only this CI).
// The CI is locked (FOR UPDATE) and the assignment checked under the lock:
// on to_version already, on another version or missing, the result is
// skipped without a write, so a repeated call is idempotent. The
// definitions of the target version come from plan.Fields with the CI's
// template list in which to_version replaces from_version, before the
// assignment changes; a composition conflict (MET-16) is a *ValidationError
// naming the attribute and leaves the CI as it is.
func (r *PGRepository) MigrateTemplate(ctx context.Context, orgID, ciID string, plan TemplateMigrationPlan, dryRun bool) (*TemplateMigrationResult, error) {
	if err := plan.check(); err != nil {
		return nil, err
	}
	var result *TemplateMigrationResult
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		m, err := r.planTemplateMigration(ctx, tx, orgID, ciID, &plan, !dryRun)
		if err != nil {
			return err
		}
		result = &m.result
		if dryRun || m.result.Outcome == TemplateSkipped {
			return nil
		}
		return r.writeTemplateMigration(ctx, tx, orgID, &plan, m)
	})
	if err != nil {
		return nil, r.uniqueField(ctx, orgID, err)
	}
	return result, nil
}

// planTemplateMigration reads CI ciID (with lock: FOR UPDATE) and computes
// its migration: the defaults, the overridden fields and the findings.
func (r *PGRepository) planTemplateMigration(ctx context.Context, tx pgx.Tx, orgID, ciID string, plan *TemplateMigrationPlan,
	lock bool) (*templateMigration, error) {
	query := fmt.Sprintf("SELECT %s, %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL",
		ciSelectColumns, ciTemplatesColumn)
	if lock {
		query += " FOR UPDATE"
	}
	before, err := scanCIWithTemplates(tx.QueryRow(ctx, query, ciID, orgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get ci for template migration: %w", err)
	}
	m := &templateMigration{before: before, defaults: map[string]any{}, result: TemplateMigrationResult{
		CIID: before.ID, Outcome: TemplateSkipped, Defaults: []string{}, Overridden: []string{}, Findings: []Violation{}, CI: before,
	}}
	at := slices.IndexFunc(before.Templates, func(t citype.CITemplate) bool { return strings.EqualFold(t.TemplateID, plan.TemplateID) })
	if at < 0 || before.Templates[at].Version != plan.FromVersion {
		return m, nil
	}
	list := slices.Clone(before.Templates)
	list[at].Version = plan.ToVersion
	// The definitions of the target version, composed in this transaction
	// with the layer order of citype (MET-16, WP-252).
	txCtx := database.ContextWithTx(ctx, tx)
	defs, _, err := resolveWithRules(txCtx, templatesOnly{plan.Fields}, nil, orgID, before.CITypeID, ciID, &list)
	if err != nil {
		return nil, err
	}
	keys, err := templateVersionKeys(ctx, tx, plan.TemplateID, plan.ToVersion)
	if err != nil {
		return nil, err
	}
	overrides, err := fieldOverrides(ctx, tx, ciID)
	if err != nil {
		return nil, err
	}
	instance, err := instanceFieldNames(ctx, tx, ciID)
	if err != nil {
		return nil, err
	}
	// The effective values: the overrides over the row (overlayOverrides),
	// the typed columns taking precedence (withTyped).
	effective := *before
	effective.Attributes = cloneAttributes(before.Attributes)
	for field, value := range overrides {
		applyOverride(&effective, field, value)
	}
	values := withTyped(effective.Attributes, typedValues(&effective))
	m.collectDefaults(defs, keys, overrides, instance, values)

	after := cloneAttributes(before.Attributes)
	for key, value := range m.defaults {
		after[key] = value
		values[key] = value
	}
	if verr := validateAttributes(defs, values, nil, nil); verr != nil {
		var ve *ValidationError
		if !errors.As(verr, &ve) {
			return nil, verr
		}
		m.result.Findings = append(m.result.Findings, ve.Violations...)
	}
	slices.SortStableFunc(m.result.Findings, func(a, b Violation) int { return strings.Compare(a.Field, b.Field) })
	m.stored = storedAttributes(after, instance)
	m.result.Outcome = TemplateMigrated
	if !lock || len(m.defaults) == 0 {
		return m, nil
	}
	// The defaults fall under the unique attributes (MET-15), checked in
	// the writing transaction like checkUniqueBack (WP-700), so the check
	// holds also while the unique index does not exist yet.
	if uerr := CheckUnique(txCtx, r, orgID, before.CITypeID, before.ClientID, ciID, m.defaults); uerr != nil {
		return nil, uerr
	}
	return m, nil
}

// collectDefaults picks the defaults of the target version: every field of
// an item of the version (keys) whose composed definition has a default,
// that is no instance attribute or typed column and has no effective value.
// An overridden field keeps its value and is reported in overridden, a
// default that does not fit its definition is a finding.
func (m *templateMigration) collectDefaults(defs []fieldmeta.FieldDefinition, keys map[string]bool, overrides map[string]any,
	instance []string, values map[string]any) {
	for i := range defs {
		def := &defs[i]
		if !keys[def.Name] || def.DefaultValue == "" || slices.Contains(instance, def.Name) || typedColumns[def.Name] != "" {
			continue
		}
		if _, overridden := overrides[def.Name]; overridden {
			m.result.Overridden = append(m.result.Overridden, def.Name)
			continue
		}
		if !isBlank(values[def.Name]) {
			continue
		}
		value := typedDefault(def)
		if err := fieldmeta.ValidateValue(def, value); err != nil {
			m.result.Findings = append(m.result.Findings, Violation{Field: def.Name,
				Detail: "default not written: " + trimFieldPrefix(def.Name, err.Error())})
			continue
		}
		m.defaults[def.Name] = value
		m.result.Defaults = append(m.result.Defaults, def.Name)
	}
	slices.Sort(m.result.Defaults)
	slices.Sort(m.result.Overridden)
}

// typedDefault is the default of def as a value of its data type: the
// text for the string types, the decoded JSON for numbers, booleans, lists
// and json; a default that is no JSON stays text (ValidateValue reports it).
func typedDefault(def *fieldmeta.FieldDefinition) any {
	switch fieldmeta.CanonicalType(def.DataType) {
	case fieldmeta.TypeInteger, fieldmeta.TypeDecimal, fieldmeta.TypeBoolean, fieldmeta.TypeMultiselect, fieldmeta.TypeJSON:
	default:
		if !def.IsList() {
			return def.DefaultValue
		}
	}
	var value any
	if err := json.Unmarshal([]byte(def.DefaultValue), &value); err != nil {
		return def.DefaultValue
	}
	return value
}

// cloneAttributes copies an attribute map one level deep.
func cloneAttributes(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// templateVersionKeys returns the attribute keys of the items of a
// template version.
func templateVersionKeys(ctx context.Context, tx pgx.Tx, templateID string, version int) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT a.key FROM template_version v
		JOIN template_version_item i ON i.template_version_id = v.id
		JOIN attribute_definition a ON a.id = i.attribute_definition_id
		WHERE v.template_id = $1 AND v.version = $2`, templateID, version)
	if err != nil {
		return nil, fmt.Errorf("read template version items: %w", err)
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read template version items: %w", err)
	}
	keys := make(map[string]bool, len(found))
	for _, key := range found {
		keys[key] = true
	}
	return keys, nil
}

// fieldOverrides returns the overridden fields of a CI with their value
// (ci_field_value with override_at, also an override with value null).
func fieldOverrides(ctx context.Context, tx pgx.Tx, ciID string) (map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT field_name, override_value FROM ci_field_value
		WHERE ci_id = $1 AND override_at IS NOT NULL`, ciID)
	if err != nil {
		return nil, fmt.Errorf("read overrides: %w", err)
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var field string
		var value any
		if err = rows.Scan(&field, &value); err != nil {
			return nil, fmt.Errorf("scan override: %w", err)
		}
		out[field] = value
	}
	return out, rows.Err()
}

// writeTemplateMigration writes the computed migration m in tx: the
// assignment on to_version, the defaults with version + 1, one ci_change
// template_migration in a change set of its own, the audit entry
// ci.template_migrated and the event ci.changed (the last two last in the
// transaction, lastInTx). ci.schema_version follows the type
// (trg_ci_schema_version, 000183); without defaults the attributes and so
// the schema version stay.
func (r *PGRepository) writeTemplateMigration(ctx context.Context, tx pgx.Tx, orgID string, plan *TemplateMigrationPlan,
	m *templateMigration) error {
	ciID := m.before.ID
	if _, err := tx.Exec(ctx, `UPDATE ci_template SET version = $3 WHERE ci_id = $1 AND template_id = $2 AND version = $4`,
		ciID, plan.TemplateID, plan.ToVersion, plan.FromVersion); err != nil {
		return fmt.Errorf("migrate ci template: %w", err)
	}
	set := "version = version + 1, updated_at = NOW()"
	args := []any{ciID, orgID}
	if len(m.defaults) > 0 {
		set = "attributes = $3::jsonb || ci_attribute_namespaces(attributes), " + set
		args = append(args, m.stored)
	}
	after, err := scanCIWithTemplates(tx.QueryRow(ctx, fmt.Sprintf(
		"UPDATE ci SET %s WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL RETURNING %s, %s",
		set, ciSelectColumns, ciTemplatesColumn), args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return locationError(fmt.Errorf("migrate ci template: %w", err))
	}
	if factsErr := loadAssetFacts(ctx, tx, []*Item{after}); factsErr != nil {
		return factsErr
	}
	m.result.CI = after
	comment, err := json.Marshal(map[string]any{
		"template_id": plan.TemplateID, "from_version": plan.FromVersion, "to_version": plan.ToVersion,
		"defaults": m.result.Defaults, "findings": m.result.Findings, "job_id": nilIfEmpty(plan.JobID),
	})
	if err != nil {
		return fmt.Errorf("encode template migration: %w", err)
	}
	changeSet := newChangeSet(ChangeKindUpdated)
	changeSet.comment = string(comment)
	if changeErr := r.recordCIChange(ctx, tx, changeSet, orgID, ciID, after.Version, ChangeTypeTemplateMigration, "attributes",
		m.before.Attributes, after.Attributes); changeErr != nil {
		return changeErr
	}
	if auditErr := r.recordAudit(ctx, tx, "ci.template_migrated", after, map[string]interface{}{
		"template_id": plan.TemplateID, "from_version": plan.FromVersion, "to_version": plan.ToVersion,
		"defaults": m.result.Defaults, "findings": m.result.Findings,
	}); auditErr != nil {
		return auditErr
	}
	changed := []string{"templates"}
	if len(m.defaults) > 0 {
		changed = append(changed, "attributes")
	}
	return r.recordEvent(ctx, tx, events.CIChanged, after, changed)
}
