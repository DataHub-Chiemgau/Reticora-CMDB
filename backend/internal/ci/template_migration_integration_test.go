package ci_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/bootstrap"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestTemplateMigrationOnPostgreSQL covers OVR-03 (template part), MET-16
// and AUD-03 for the template migration of a CI (WP-253) against
// PostgreSQL, through the repository chain of bootstrap.NewCIRepository
// (WP-418):
//   - a default of the target version lands only in an empty field, typed
//     (integer as a JSON number); a field with a value keeps it;
//   - an overridden field keeps its value and is reported in overridden,
//     an override with value null stays empty; an instance attribute under
//     _instance stays; ci_field_value is unchanged;
//   - a required attribute of the target version the CI lacks is a
//     finding and does not block;
//   - ci_template.version is the target version; exactly one ci_change
//     template_migration (field attributes, change_kind updated, own
//     change_set_id, comment with template, versions, defaults and job)
//     and history.Replay ends at the attributes after the migration; the
//     audit entry ci.template_migrated and the event ci.changed with
//     templates and attributes are written;
//   - a repetition is skipped and writes nothing; a dry run computes the
//     same result and writes nothing;
//   - a migration without defaults keeps ci.schema_version and names only
//     templates in the event;
//   - a default another CI holds for a unique attribute is a
//     *ci.UniqueError; under database.Savepoint only that CI is discarded,
//     the next one of the transaction is migrated;
//   - a composition conflict of the target version with an instance
//     definition of the CI (MET-16) leaves the CI unmigrated and names the
//     attribute.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTemplateMigrationOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "c3")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA,
		Type: identity.PrincipalTypeUser, Permissions: []identity.Permission{identity.PermCIRead, identity.PermCIWrite}})

	types := citype.NewPGRepository(f.App)
	base := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	repo := bootstrap.NewCIRepository(base, search.NewMemoryRepository(), nil, nil, nil, nil)
	migrator, ok := repo.(ci.TemplateMigrator)
	if !ok {
		t.Fatalf("the repository chain %T does not offer the template migration (SR-11)", repo)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	text := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	typ := &citype.Type{OrganizationID: f.OrgA, Key: "w253-host", Name: "w253-host", IsObservable: true}
	if err := types.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	define := func(key, dataType string, unique bool) citype.AttributeDefinition {
		t.Helper()
		d := citype.AttributeDefinition{CITypeID: typ.ID, Key: key, DataType: dataType, Unique: unique}
		if unique {
			d.UniqueScope = "org"
		}
		if err := types.CreateAttributeDefinition(ctx, f.OrgA, &d); err != nil {
			t.Fatalf("definition %s: %v", key, err)
		}
		return d
	}
	owner, size, locked := define("w253_owner", "text", false), define("w253_size", "integer", false), define("w253_locked", "text", false)
	nulled, kept, needed := define("w253_nulled", "text", false), define("w253_kept", "text", false), define("w253_needed", "text", false)
	plain, serial, level := define("w253_plain", "text", false), define("w253_serial", "text", true), define("w253_level", "integer", false)

	item := func(def citype.AttributeDefinition, defaultValue string) citype.TemplateVersionItem {
		it := citype.TemplateVersionItem{AttributeDefinitionID: def.ID}
		if defaultValue != "" {
			it.DefaultOverride = json.RawMessage(defaultValue)
		}
		return it
	}
	publish := func(tpl *citype.Template, version int, items []citype.TemplateVersionItem) {
		t.Helper()
		for i := range items {
			items[i].Position = i
		}
		if _, err := types.SetTemplateVersionItems(ctx, f.OrgA, tpl.ID, version, items); err != nil {
			t.Fatalf("template %s v%d items: %v", tpl.Key, version, err)
		}
		if _, err := types.PublishTemplateVersion(ctx, f.OrgA, tpl.ID, version); err != nil {
			t.Fatalf("publish %s v%d: %v", tpl.Key, version, err)
		}
	}
	// template creates a template of the type with the published versions
	// 1 (v1 items) and 2 (v2 items).
	template := func(key string, v1, v2 []citype.TemplateVersionItem) citype.Template {
		t.Helper()
		tpl := citype.Template{Key: key, CITypeID: typ.ID, IsActive: true}
		if err := tpl.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := types.CreateTemplate(ctx, f.OrgA, &tpl); err != nil {
			t.Fatalf("template %s: %v", key, err)
		}
		publish(&tpl, 1, v1)
		if _, err := types.CreateTemplateVersion(ctx, f.OrgA, tpl.ID); err != nil {
			t.Fatalf("template %s v2: %v", key, err)
		}
		publish(&tpl, 2, v2)
		return tpl
	}
	required := true
	primary := template("w253-primary", []citype.TemplateVersionItem{item(owner, "")}, []citype.TemplateVersionItem{
		item(owner, `"ops"`), item(size, `5`), item(locked, `"from-v2"`), item(nulled, `"from-v2"`), item(kept, `"from-v2"`),
		{AttributeDefinitionID: needed.ID, RequiredOverride: &required},
	})
	quiet := template("w253-quiet", nil, []citype.TemplateVersionItem{item(plain, "")})
	unique := template("w253-unique", nil, []citype.TemplateVersionItem{item(serial, `"SER-1"`)})
	narrow := template("w253-narrow", nil, []citype.TemplateVersionItem{
		{AttributeDefinitionID: level.ID, ValidationOverride: json.RawMessage(`{"min":10}`)}})

	newCI := func(name string, attrs map[string]any, templates ...citype.Template) *ci.Item {
		t.Helper()
		c := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: typ.ID, Name: name, Status: "active", Attributes: attrs}
		if err := repo.Create(ctx, c); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		for i := range templates {
			if _, err := f.Admin.Exec(bg, `INSERT INTO ci_template (organization_id, ci_id, template_id, version, position)
				VALUES ($1, $2, $3, 1, $4)`, f.OrgA, c.ID, templates[i].ID, i); err != nil {
				t.Fatal(err)
			}
		}
		return c
	}
	planFor := func(tpl citype.Template) ci.TemplateMigrationPlan {
		return ci.TemplateMigrationPlan{TemplateID: tpl.ID, FromVersion: 1, ToVersion: 2, JobID: "", Fields: types}
	}
	templateVersion := func(ciID, templateID string) int {
		return count(`SELECT version FROM ci_template WHERE ci_id = $1 AND template_id = $2`, ciID, templateID)
	}
	migrations := func(ciID string) int {
		return count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'template_migration'`, ciID)
	}
	overrides := func(ciID string) string {
		return text(`SELECT COALESCE(jsonb_agg(to_jsonb(v) ORDER BY v.field_name), '[]'::jsonb)::text FROM ci_field_value v WHERE v.ci_id = $1`, ciID)
	}

	// The CI: w253_kept has a value, w253_locked an override with a value,
	// w253_nulled an override with null, w253_note is an instance attribute.
	device := newCI("w253-device", map[string]any{"w253_kept": "mine"}, primary)
	manual := &ci.ManualChange{Author: f.User}
	for _, attrs := range []map[string]any{{"w253_locked": "manual", "w253_nulled": "tmp"}, {"w253_nulled": nil}} {
		if _, err := repo.Update(ctx, f.OrgA, device.ID, ci.UpdateRequest{Attributes: attrs, Manual: manual}); err != nil {
			t.Fatalf("manual update: %v", err)
		}
	}
	if _, err := types.UpsertInstanceField(ctx, f.OrgA, device.ID, &citype.UpsertInstanceFieldRequest{Name: "w253_note", DataType: "text"}); err != nil {
		t.Fatalf("instance field: %v", err)
	}
	if _, err := repo.Update(ctx, f.OrgA, device.ID, ci.UpdateRequest{Attributes: map[string]any{"w253_note": "inst"}}); err != nil {
		t.Fatalf("instance value: %v", err)
	}
	if n := count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND override_at IS NOT NULL
		AND field_name IN ('w253_locked', 'w253_nulled')`, device.ID); n != 2 {
		t.Fatalf("%d overrides, want 2", n)
	}
	fieldValues := overrides(device.ID)
	versionBefore := count(`SELECT version FROM ci WHERE id = $1`, device.ID)
	changesBefore := count(`SELECT count(*) FROM ci_change WHERE ci_id = $1`, device.ID)

	checkResult := func(step string, got *ci.TemplateMigrationResult) {
		t.Helper()
		if got.Outcome != ci.TemplateMigrated || got.CIID != device.ID ||
			!slices.Equal(got.Defaults, []string{"w253_owner", "w253_size"}) ||
			!slices.Equal(got.Overridden, []string{"w253_locked", "w253_nulled"}) ||
			len(got.Findings) != 1 || got.Findings[0].Field != "w253_needed" {
			t.Fatalf("%s: %+v, want migrated with defaults [w253_owner w253_size], overridden [w253_locked w253_nulled] "+
				"and the finding of w253_needed", step, got)
		}
	}

	// Dry run: the same result, nothing written.
	dry, err := migrator.MigrateTemplate(ctx, f.OrgA, device.ID, planFor(primary), true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	checkResult("dry run", dry)
	if templateVersion(device.ID, primary.ID) != 1 || count(`SELECT version FROM ci WHERE id = $1`, device.ID) != versionBefore ||
		count(`SELECT count(*) FROM ci_change WHERE ci_id = $1`, device.ID) != changesBefore ||
		count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.template_migrated'`, device.ID) != 0 {
		t.Fatal("the dry run wrote")
	}

	// The migration.
	plan := planFor(primary)
	plan.JobID = "00000000-0000-4000-8000-0000000000c3"
	got, err := migrator.MigrateTemplate(ctx, f.OrgA, device.ID, plan, false)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	checkResult("migrate", got)
	if got.CI == nil || got.CI.Version != int64(versionBefore)+1 || len(got.CI.Templates) != 1 || got.CI.Templates[0].Version != 2 {
		t.Fatalf("migrated CI: %+v", got.CI)
	}
	if v := templateVersion(device.ID, primary.ID); v != 2 {
		t.Fatalf("ci_template.version %d, want 2", v)
	}
	var attrs map[string]any
	if err = json.Unmarshal([]byte(text(`SELECT attributes::text FROM ci WHERE id = $1`, device.ID)), &attrs); err != nil {
		t.Fatal(err)
	}
	instance, _ := attrs["_instance"].(map[string]any)
	_, hasNulled := attrs["w253_nulled"]
	if attrs["w253_owner"] != "ops" || attrs["w253_size"] != float64(5) || attrs["w253_kept"] != "mine" ||
		attrs["w253_locked"] != "manual" || hasNulled || instance["w253_note"] != "inst" {
		t.Fatalf("attributes after the migration: %v", attrs)
	}
	if stored := text(`SELECT jsonb_typeof(attributes->'w253_size') FROM ci WHERE id = $1`, device.ID); stored != "number" {
		t.Fatalf("w253_size stored as %s, want a number", stored)
	}
	if after := overrides(device.ID); after != fieldValues {
		t.Fatalf("ci_field_value changed:\n%s\nwant\n%s", after, fieldValues)
	}

	// History: one row template_migration in a change set of its own.
	if n := migrations(device.ID); n != 1 {
		t.Fatalf("%d ci_change rows template_migration, want 1", n)
	}
	var comment struct {
		TemplateID  string         `json:"template_id"`
		FromVersion int            `json:"from_version"`
		ToVersion   int            `json:"to_version"`
		Defaults    []string       `json:"defaults"`
		Findings    []ci.Violation `json:"findings"`
		JobID       *string        `json:"job_id"`
	}
	row := f.Admin.QueryRow(bg, `SELECT comment, field_name, change_kind, change_set_id IS NOT NULL, ci_version,
		(SELECT count(*) FROM ci_change o WHERE o.change_set_id = c.change_set_id)
		FROM ci_change c WHERE ci_id = $1 AND change_type = 'template_migration'`, device.ID)
	var raw, field, kind string
	var hasSet bool
	var ciVersion int64
	var inSet int
	if err = row.Scan(&raw, &field, &kind, &hasSet, &ciVersion, &inSet); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(raw), &comment); err != nil || comment.TemplateID != primary.ID || comment.FromVersion != 1 ||
		comment.ToVersion != 2 || !slices.Equal(comment.Defaults, got.Defaults) || len(comment.Findings) != 1 ||
		comment.JobID == nil || *comment.JobID != plan.JobID {
		t.Fatalf("comment %s", raw)
	}
	if field != "attributes" || kind != ci.ChangeKindUpdated || !hasSet || inSet != 1 || ciVersion != got.CI.Version {
		t.Fatalf("ci_change: field %s, kind %s, set %v (%d rows), version %d", field, kind, hasSet, inSet, ciVersion)
	}
	rows, err := f.Admin.Query(bg, `SELECT change_type, COALESCE(field_name, ''), COALESCE(change_kind, ''), new_value
		FROM ci_change WHERE ci_id = $1 ORDER BY created_at, id`, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	var trail []history.Change
	for rows.Next() {
		c := history.Change{EntityType: "ci", EntityID: device.ID}
		if err = rows.Scan(&c.ChangeType, &c.FieldName, &c.ChangeKind, &c.NewValue); err != nil {
			t.Fatal(err)
		}
		trail = append(trail, c)
	}
	rows.Close()
	replayed, _ := json.Marshal(history.Replay(trail).Fields["attributes"])
	current, _ := json.Marshal(got.CI.Attributes)
	var a, b any
	_ = json.Unmarshal(replayed, &a)
	_ = json.Unmarshal(current, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("history.Replay: attributes %s, want %s", replayed, current)
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.template_migrated'
		AND changes->>'template_id' = $2 AND (changes->>'to_version')::int = 2`, device.ID, primary.ID); n != 1 {
		t.Fatalf("%d audit entries ci.template_migrated, want 1", n)
	}
	if n := count(`SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'
		AND data->'changed_fields' = '["templates", "attributes"]'::jsonb`, device.ID); n != 1 {
		t.Fatalf("%d events ci.changed with templates and attributes, want 1", n)
	}

	// A repetition is skipped and writes nothing.
	again, err := migrator.MigrateTemplate(ctx, f.OrgA, device.ID, plan, false)
	if err != nil || again.Outcome != ci.TemplateSkipped || len(again.Defaults) != 0 {
		t.Fatalf("repetition: %+v, %v, want skipped", again, err)
	}
	if n := migrations(device.ID); n != 1 {
		t.Fatalf("the repetition wrote: %d rows template_migration", n)
	}
	// A CI without the template is skipped as well.
	bare := newCI("w253-bare", nil)
	if res, merr := migrator.MigrateTemplate(ctx, f.OrgA, bare.ID, plan, false); merr != nil || res.Outcome != ci.TemplateSkipped {
		t.Fatalf("CI without the template: %+v, %v", res, merr)
	}

	// Without defaults the schema version stays and the event names only
	// the templates.
	still := newCI("w253-still", map[string]any{"w253_plain": "p"}, quiet)
	schemaBefore := count(`SELECT schema_version FROM ci WHERE id = $1`, still.ID)
	// A new definition raises the schema version of the type.
	define("w253_extra", "text", false)
	if count(`SELECT schema_version FROM ci_type WHERE id = $1`, typ.ID) <= schemaBefore {
		t.Fatal("the schema version of the type did not rise")
	}
	res, err := migrator.MigrateTemplate(ctx, f.OrgA, still.ID, planFor(quiet), false)
	if err != nil || res.Outcome != ci.TemplateMigrated || len(res.Defaults) != 0 {
		t.Fatalf("migration without defaults: %+v, %v", res, err)
	}
	if v := count(`SELECT schema_version FROM ci WHERE id = $1`, still.ID); v != schemaBefore {
		t.Fatalf("schema_version %d after a migration without defaults, want %d", v, schemaBefore)
	}
	if n := count(`SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'
		AND data->'changed_fields' = '["templates"]'::jsonb`, still.ID); n != 1 {
		t.Fatalf("%d events ci.changed with templates only, want 1", n)
	}

	// Unique: another CI holds the default.
	newCI("w253-holder", map[string]any{"w253_serial": "SER-1"})
	clash := newCI("w253-clash", nil, unique)
	other := newCI("w253-other", nil, primary)
	taken := func(err error) bool {
		var ue *ci.UniqueError
		return errors.As(err, &ue) && slices.Equal(ue.Fields, []string{"w253_serial"})
	}
	if _, err = migrator.MigrateTemplate(ctx, f.OrgA, clash.ID, planFor(unique), false); !taken(err) {
		t.Fatalf("default a CI holds: %v, want a conflict on w253_serial", err)
	}
	err = database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(txCtx context.Context) error {
		uerr := database.Savepoint(txCtx, func(ctx context.Context) error {
			_, merr := migrator.MigrateTemplate(ctx, f.OrgA, clash.ID, planFor(unique), false)
			return merr
		})
		if !taken(uerr) {
			t.Errorf("under a savepoint: %v, want a conflict on w253_serial", uerr)
		}
		return database.Savepoint(txCtx, func(ctx context.Context) error {
			_, merr := migrator.MigrateTemplate(ctx, f.OrgA, other.ID, planFor(primary), false)
			return merr
		})
	})
	if err != nil {
		t.Fatalf("block transaction: %v", err)
	}
	if templateVersion(clash.ID, unique.ID) != 1 || migrations(clash.ID) != 0 || templateVersion(other.ID, primary.ID) != 2 ||
		migrations(other.ID) != 1 {
		t.Fatal("the savepoint did not discard only the CI with the conflict")
	}

	// A composition conflict of the target version: the instance
	// definition caps w253_level at 5, version 2 asks for at least 10.
	capped := newCI("w253-capped", nil, narrow)
	five := 5.0
	if _, err = types.UpsertInstanceField(ctx, f.OrgA, capped.ID, &citype.UpsertInstanceFieldRequest{Name: "w253_level",
		DataType: "integer", Validation: &fieldmeta.ValidationRules{Max: &five}}); err != nil {
		t.Fatalf("instance definition: %v", err)
	}
	_, err = migrator.MigrateTemplate(ctx, f.OrgA, capped.ID, planFor(narrow), false)
	ve, isValidation := ci.AsValidationError(err)
	if !isValidation || len(ve.Violations) != 1 || ve.Violations[0].Field != "w253_level" || ve.Violations[0].Code != citype.CodeRangeEmpty {
		t.Fatalf("composition conflict: %v, want the violation of w253_level (%s)", err, citype.CodeRangeEmpty)
	}
	if templateVersion(capped.ID, narrow.ID) != 1 || migrations(capped.ID) != 0 {
		t.Fatal("a CI with a composition conflict was migrated")
	}

	// An invalid plan is refused before any read.
	if _, err = migrator.MigrateTemplate(ctx, f.OrgA, device.ID, ci.TemplateMigrationPlan{TemplateID: primary.ID, FromVersion: 2,
		ToVersion: 2, Fields: types}, false); err == nil {
		t.Fatal("a plan with the same versions was accepted")
	}
}
