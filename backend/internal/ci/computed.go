package ci

// Computed attributes in the write path (WP-717, MET-10 computed): the
// value of an attribute definition with an expression (FieldDefinition.
// Computed, WP-258) is evaluated with rules.EvalExpr from the state a write
// leaves the CI in, inside the repository transaction of that write, and
// stored under its key in ci.attributes. Every changed value gets a
// ci_field_value row of the source computed (rank 100) in the same
// transaction, which ci_field_namespaces() projects into _provenance. The
// recomputation is part of the change that triggers it: the same version,
// history entry and ci.changed, no event of its own. Changes of the asset
// take effect with the refresh job (WP-718).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/jackc/pgx/v5"
)

// SourceComputed is the provenance source of a computed value
// (ci_field_value.discovered_source); RankComputed its rank. The rank is
// stored with the row (discovered_rank), so the central write decision
// (WP-058) refuses every automation source on a stored computed value
// (lower_rank); override.SourceRanks (REC-03) stays as it is.
const (
	SourceComputed = "computed"
	RankComputed   = 100
)

// CodeComputed is the code of a write of a computed attribute (422).
const CodeComputed = "computed"

// WithComputed makes the repository compute the values of computed
// attributes in its write transactions (WP-717): Create, CreateMany,
// Update, UpdateRecord, ChangeType, Merge and the recomputation after an
// override (RecomputeTx). fields resolves the definitions (citype's
// repository); the package does not import citype for it. Without it no
// value is computed.
func (r *PGRepository) WithComputed(fields FieldResolver) *PGRepository {
	r.computed = fields
	return r
}

// fieldsMemoKey carries a fieldsMemo in a context.
type fieldsMemoKey struct{}

// fieldsMemo holds the field definitions resolved for a write by CI type
// and CI, so the validation of the write and the computation in its
// transaction resolve them once (NFR-03): the service validates with them,
// the repository computes with them.
type fieldsMemo struct {
	mu   sync.Mutex
	defs map[string][]fieldmeta.FieldDefinition
}

// WithFieldsMemo returns ctx with a memo of the field definitions resolved
// under it: the observed-value check of the ingest (InvalidObserved,
// ObservedRules) and the validation of a write fill it, the computation of
// the CI repository (WithComputed) reads it instead of resolving again. A
// context that carries one keeps it. The memo lives as long as the
// context: one ingest transaction, one write.
func WithFieldsMemo(ctx context.Context) context.Context {
	if _, ok := ctx.Value(fieldsMemoKey{}).(*fieldsMemo); ok {
		return ctx
	}
	return context.WithValue(ctx, fieldsMemoKey{}, &fieldsMemo{defs: map[string][]fieldmeta.FieldDefinition{}})
}

func memoKey(ciTypeID, ciID string) string {
	return strings.ToLower(ciTypeID) + "/" + strings.ToLower(ciID)
}

// rememberFields stores the definitions of a CI of ciTypeID (ciID "" for a
// new CI) in the context's memo, when it has one.
func rememberFields(ctx context.Context, ciTypeID, ciID string, defs []fieldmeta.FieldDefinition) {
	memo, ok := ctx.Value(fieldsMemoKey{}).(*fieldsMemo)
	if !ok {
		return
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	memo.defs[memoKey(ciTypeID, ciID)] = defs
}

// rememberedFields returns the definitions the context's memo holds.
func rememberedFields(ctx context.Context, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, bool) {
	memo, ok := ctx.Value(fieldsMemoKey{}).(*fieldsMemo)
	if !ok {
		return nil, false
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	defs, found := memo.defs[memoKey(ciTypeID, ciID)]
	return defs, found
}

// computedDefinitions returns the computed definitions of a CI of ciTypeID
// (ciID "" for a new CI) in tx: from the context's memo, else resolved in
// tx once (and remembered). Without WithComputed there are none.
func (r *PGRepository) computedDefinitions(ctx context.Context, tx pgx.Tx, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	if r.computed == nil || !api.IsUUID(ciTypeID) {
		return nil, nil
	}
	defs, ok := rememberedFields(ctx, ciTypeID, ciID)
	if !ok {
		var err error
		if defs, err = resolveFields(database.ContextWithTx(ctx, tx), r.computed, orgID, ciTypeID, ciID); err != nil {
			return nil, err
		}
		rememberFields(ctx, ciTypeID, ciID, defs)
	}
	var out []fieldmeta.FieldDefinition
	for i := range defs {
		if defs[i].Computed != nil {
			out = append(out, defs[i])
		}
	}
	return out, nil
}

// computeEnv is what an evaluation needs besides the values of the CI:
// the time, the organization's time zone (today, only read when an
// expression uses it) and the whitelisted fields of the linked assets
// (only read when an expression uses one), each with at most one query per
// transaction or batch.
type computeEnv struct {
	now    time.Time
	loc    *time.Location
	assets map[string]map[string]any
}

// loadComputeEnv reads the time zone and the asset fields defs need for
// the CIs linked to assetIDs, with one statement and only when an
// expression needs them.
func loadComputeEnv(ctx context.Context, tx pgx.Tx, orgID string, defs []fieldmeta.FieldDefinition, assetIDs []string) (*computeEnv, error) {
	env := &computeEnv{now: time.Now().UTC(), loc: time.UTC, assets: map[string]map[string]any{}}
	today, asset := false, false
	for i := range defs {
		e := defs[i].Computed
		today = today || e.UsesToday()
		asset = asset || slices.ContainsFunc(e.Fields(), func(f string) bool { return strings.HasPrefix(f, rules.AssetPrefix) })
	}
	var ids []string
	if asset {
		for _, id := range assetIDs {
			if id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	if !today && len(ids) == 0 {
		return env, nil
	}
	var tz *string
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT CASE WHEN $3 THEN (SELECT timezone FROM organization WHERE id = $1) END,
		COALESCE((SELECT jsonb_object_agg(a.id::text, jsonb_build_object(
			'warranty_until', to_char(a.warranty_until, 'YYYY-MM-DD'), 'purchase_date', to_char(a.purchase_date, 'YYYY-MM-DD'),
			'purchase_price', a.purchase_price, 'inventory_no', a.inventory_no, 'serial_number', a.serial_number, 'status', a.status,
			'warranty_end', to_char(a.warranty_until, 'YYYY-MM-DD'), 'purchase_cost', a.purchase_price, 'asset_tag', a.inventory_no))
			FROM asset a WHERE a.id = ANY($2::uuid[])), '{}'::jsonb)`, orgID, ids, today).Scan(&tz, &raw); err != nil {
		return nil, fmt.Errorf("read inputs of computed attributes: %w", err)
	}
	if tz != nil && *tz != "" {
		if loc, err := time.LoadLocation(*tz); err == nil {
			env.loc = loc
		}
	}
	if err := json.Unmarshal(raw, &env.assets); err != nil {
		return nil, fmt.Errorf("decode asset fields: %w", err)
	}
	return env, nil
}

// evaluate computes every definition of defs for a CI with values (its
// attributes with the typed columns, as validated) linked to assetID. The
// result has every key of defs; nil is an empty value.
func (env *computeEnv) evaluate(defs []fieldmeta.FieldDefinition, values map[string]any, assetID string) map[string]any {
	inputs := make(map[string]any, len(values)+len(rules.AssetFields))
	for k, v := range values {
		inputs[k] = v
	}
	for name, v := range env.assets[strings.ToLower(assetID)] {
		inputs[rules.AssetPrefix+name] = v
	}
	out := make(map[string]any, len(defs))
	for i := range defs {
		out[defs[i].Name] = conformComputed(&defs[i], rules.EvalExpr(defs[i].Computed, inputs, env.now, env.loc))
	}
	return out
}

// conformComputed fits a computed value to the data type of its
// definition: an integer is rounded; a value the definition refuses is
// empty, as a value of the wrong kind in an expression.
func conformComputed(def *fieldmeta.FieldDefinition, v any) any {
	if v == nil {
		return nil
	}
	if n, ok := v.(float64); ok && def.DataType == fieldmeta.TypeInteger {
		v = math.Round(n)
	}
	if fieldmeta.ValidateValue(def, v) != nil {
		return nil
	}
	return v
}

// sameJSONValue reports whether two values have the same JSON.
func sameJSONValue(a, b any) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// computedChanges returns the computed values that differ from stored:
// the key with its new value, nil for a value that becomes empty while
// stored holds one.
func computedChanges(stored, results map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range results {
		old, had := stored[key]
		switch {
		case value == nil:
			if had && old != nil {
				out[key] = nil
			}
		case !had || !sameJSONValue(old, value):
			out[key] = value
		}
	}
	return out
}

// computedValue is the provenance of one computed value of a CI; a nil
// value removes it.
type computedValue struct {
	ciID, field string
	value       any
}

// computedValuesOf lists the changes of one CI in key order.
func computedValuesOf(ciID string, changes map[string]any) []computedValue {
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]computedValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, computedValue{ciID: ciID, field: k, value: changes[k]})
	}
	return out
}

// writeComputedProvenance records the provenance of changed computed
// values with one statement: a value gets a ci_field_value row of the
// source computed with rank 100 and observed_at = at (the time of the
// computation), an emptied one loses its computed row. The override
// columns stay untouched.
func writeComputedProvenance(ctx context.Context, tx pgx.Tx, orgID string, at time.Time, changes []computedValue) error {
	if len(changes) == 0 {
		return nil
	}
	var setCIs, setNames, goneCIs, goneNames []string
	var values []*string
	for _, c := range changes {
		if c.value == nil {
			goneCIs, goneNames = append(goneCIs, c.ciID), append(goneNames, c.field)
			continue
		}
		raw, err := json.Marshal(c.value)
		if err != nil {
			return fmt.Errorf("encode computed value %s: %w", c.field, err)
		}
		text := string(raw)
		setCIs, setNames, values = append(setCIs, c.ciID), append(setNames, c.field), append(values, &text)
	}
	if _, err := tx.Exec(ctx, `
		WITH gone AS (
			DELETE FROM ci_field_value v USING unnest($5::uuid[], $6::text[]) AS d(ci_id, name)
			WHERE v.ci_id = d.ci_id AND v.field_name = d.name AND v.discovered_source = 'computed' AND v.override_at IS NULL
		)
		INSERT INTO ci_field_value (
			organization_id, ci_id, field_name, discovered_value,
			discovered_source, discovered_at, discovered_rank, observed_at
		)
		SELECT $1, f.ci_id, f.name, f.value::jsonb, 'computed', now(), 100, $7
		FROM unnest($2::uuid[], $3::text[], $4::text[]) AS f(ci_id, name, value)
		ON CONFLICT (ci_id, field_name) DO UPDATE SET
			discovered_value = EXCLUDED.discovered_value,
			discovered_source = EXCLUDED.discovered_source,
			discovered_at = EXCLUDED.discovered_at,
			discovered_rank = EXCLUDED.discovered_rank,
			observed_at = EXCLUDED.observed_at`,
		orgID, setCIs, setNames, values, goneCIs, goneNames, at.UTC()); err != nil {
		return fmt.Errorf("record computed values: %w", err)
	}
	return nil
}

// computedOfNew computes the values of new CIs before they are inserted
// (Create, CreateMany): the definitions once per CI type, the inputs with
// one query for all items. Each item's attributes take its values (an
// empty one removes the key); the result holds per item position every
// computed key with its value (nil: empty), nil for an item whose type
// computes nothing.
func (r *PGRepository) computedOfNew(ctx context.Context, tx pgx.Tx, orgID string, items []*Item) ([]map[string]any, *computeEnv, error) {
	if r.computed == nil {
		return nil, nil, nil
	}
	byType := map[string][]fieldmeta.FieldDefinition{}
	var all []fieldmeta.FieldDefinition
	var assets []string
	for _, item := range items {
		defs, seen := byType[strings.ToLower(item.CITypeID)]
		if !seen {
			var err error
			if defs, err = r.computedDefinitions(ctx, tx, orgID, item.CITypeID, ""); err != nil {
				return nil, nil, err
			}
			byType[strings.ToLower(item.CITypeID)] = defs
			all = append(all, defs...)
		}
		if len(defs) > 0 {
			assets = append(assets, item.AssetID)
		}
	}
	if len(all) == 0 {
		return nil, nil, nil
	}
	env, err := loadComputeEnv(ctx, tx, orgID, all, assets)
	if err != nil {
		return nil, nil, err
	}
	out := make([]map[string]any, len(items))
	for i, item := range items {
		defs := byType[strings.ToLower(item.CITypeID)]
		if len(defs) == 0 {
			continue
		}
		if item.Attributes == nil {
			item.Attributes = map[string]any{}
		}
		results := env.evaluate(defs, withTyped(presentAttributes(item.Attributes), typedValues(item)), item.AssetID)
		for key, value := range results {
			if value == nil {
				delete(item.Attributes, key)
			} else {
				item.Attributes[key] = value
			}
		}
		out[i] = results
	}
	return out, env, nil
}

// nonEmpty returns the values of m that are not nil.
func nonEmpty(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if v != nil {
			out[k] = v
		}
	}
	return out
}

// computedUpdate computes the values of the CI before an update applies
// req (updateRow): from the state after the update (attributes merged with
// the patch, typed columns, asset link). It returns the keys of the
// computed definitions and the values that differ from the stored ones,
// and sets on req the attributes the update writes so the row takes the
// computed values (a computed key of an automated write is replaced).
// computedAt is the time of the computation.
func (r *PGRepository) computedUpdate(ctx context.Context, tx pgx.Tx, orgID string, before *Item, req *UpdateRequest) (keys map[string]bool, changed map[string]any, computedAt time.Time, err error) {
	defs, err := r.computedDefinitions(ctx, tx, orgID, before.CITypeID, before.ID)
	if err != nil || len(defs) == 0 {
		return nil, nil, computedAt, err
	}
	assetID := before.AssetID
	if req.AssetID != nil {
		assetID = *req.AssetID
	}
	env, err := loadComputeEnv(ctx, tx, orgID, defs, []string{assetID})
	if err != nil {
		return nil, nil, computedAt, err
	}
	attrs := MergePatch(before.Attributes, req.Attributes)
	typed := typedValues(before)
	for name, value := range typedPatch(req) {
		typed[name] = value
	}
	results := env.evaluate(defs, withTyped(attrs, typed), assetID)
	keys = make(map[string]bool, len(defs))
	for i := range defs {
		keys[defs[i].Name] = true
	}
	if patch := computedChanges(attrs, results); len(patch) > 0 {
		next := make(map[string]any, len(req.Attributes)+len(patch))
		for k, v := range req.Attributes {
			next[k] = v
		}
		for k, v := range patch {
			next[k] = v
		}
		req.Attributes = next
	}
	return keys, computedChanges(before.Attributes, results), env.now, nil
}

// computedScanner appends the names of the computed provenance rows of
// the CI to a scan of ciSelectColumns.
type computedScanner struct {
	ciScanner
	names *[]string
}

func (s computedScanner) Scan(dest ...any) error {
	return s.ciScanner.Scan(append(dest, s.names)...)
}

// recomputeRow computes the values of the computed attributes of the CI
// row ciID from its stored state in tx and writes the changed ones with
// their provenance (one UPDATE without a version step and one provenance
// statement): after a type change (the definitions of the new type),
// a merge (the merged survivor) and an override of an input
// (RecomputeTx). A computed value whose key has no computed definition
// any more is removed with its provenance. It reports whether a value
// changed.
func (r *PGRepository) recomputeRow(ctx context.Context, tx pgx.Tx, orgID, ciID string) (bool, error) {
	if r.computed == nil {
		return false, nil
	}
	var stale []string
	item, err := scanCI(computedScanner{ciScanner: tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s,
			COALESCE((SELECT array_agg(v.field_name ORDER BY v.field_name) FROM ci_field_value v
				WHERE v.ci_id = ci.id AND v.discovered_source = 'computed' AND v.override_at IS NULL), '{}')
			FROM ci WHERE id = $1 AND organization_id = $2`, ciSelectColumns), ciID, orgID), names: &stale})
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("read ci for computed attributes: %w", err)
	}
	defs, err := r.computedDefinitions(ctx, tx, orgID, item.CITypeID, ciID)
	if err != nil {
		return false, err
	}
	results := map[string]any{}
	env := &computeEnv{now: time.Now().UTC()}
	if len(defs) > 0 {
		if env, err = loadComputeEnv(ctx, tx, orgID, defs, []string{item.AssetID}); err != nil {
			return false, err
		}
		results = env.evaluate(defs, withTyped(item.Attributes, typedValues(item)), item.AssetID)
	}
	var gone []string
	for _, key := range stale {
		if _, defined := results[key]; !defined {
			results[key] = nil
			gone = append(gone, key)
		}
	}
	changed := computedChanges(item.Attributes, results)
	for _, key := range gone {
		// The provenance row goes even when the attribute is gone already.
		changed[key] = nil
	}
	if len(changed) == 0 {
		return false, nil
	}
	set, removed := map[string]any{}, []string{}
	for key, value := range changed {
		if value == nil {
			removed = append(removed, key)
		} else {
			set[key] = value
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE ci SET attributes = (COALESCE(attributes, '{}'::jsonb) - $3::text[]) || $4::jsonb
		WHERE id = $1 AND organization_id = $2`, ciID, orgID, removed, set); err != nil {
		return false, fmt.Errorf("write computed attributes: %w", err)
	}
	return true, writeComputedProvenance(ctx, tx, orgID, env.now, computedValuesOf(ciID, changed))
}

// RecomputeTx implements override.Recomputer (WP-717): after an override
// changed a value of the CI (SetOverride, ClearOverride), the computed
// values follow in the same transaction; they belong to the version step
// of the override, which writes its own history row.
func (r *PGRepository) RecomputeTx(ctx context.Context, tx pgx.Tx, orgID, ciID string) error {
	_, err := r.recomputeRow(ctx, tx, orgID, ciID)
	return err
}

// computedWriteViolations checks a manual write of computed attributes
// (WP-717): on a creation (existing nil) every computed key of attrs is a
// violation; on an update a computed key with the stored value is no
// change and returned in drop, any other value (null too) is a violation
// on attributes.<key> with the detail computed.
func computedWriteViolations(defs []fieldmeta.FieldDefinition, attrs, existing map[string]any, creating bool) (drop []string, violations []Violation) {
	for i := range defs {
		if defs[i].Computed == nil {
			continue
		}
		key := defs[i].Name
		value, present := attrs[key]
		if !present {
			continue
		}
		if !creating && sameValue(existing[key], value) {
			drop = append(drop, key)
			continue
		}
		violations = append(violations, Violation{Field: "attributes." + key, Detail: CodeComputed, Code: CodeComputed})
	}
	sort.Strings(drop)
	return drop, violations
}
