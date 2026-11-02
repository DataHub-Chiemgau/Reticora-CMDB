package citype

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestResolverLayerOrder covers WP-589 (DB-03, MET-16, F-306) against
// PostgreSQL, after decision E-92 (transitional rule, option 2): the
// resolver composes the layers system → organization → type → instance.
// Keys keep the order of the layer that brings them first, whatever their
// sort order; for a key every layer defines, the bounds of all layers
// apply and the description of the last layer wins, layer by layer as the
// narrower ones are removed. The system, organization and type layers are
// read only from attribute_definition: removing their rows there removes
// them from the resolved definitions, and attribute_definition and
// ci_instance_field_definition are the only tables holding attribute
// definitions. The instance layer stays in ci_instance_field_definition
// (MET-14 [P2], WP-256).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestResolverLayerOrder(t *testing.T) {
	f := scopetest.Seed(t, "89")
	bg := context.Background()
	repo := NewPGRepository(f.App)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ptr := func(v float64) *float64 { return &v }

	typ := &Type{OrganizationID: f.OrgA, Key: "wp589-layers", Name: "wp589-layers", IsObservable: true}
	if err := repo.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	var ciID string
	if err := f.Admin.QueryRow(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		VALUES ($1, $2, $3, 'wp589-layers', 'active') RETURNING id::text`, f.OrgA, f.Client1, typ.ID).Scan(&ciID); err != nil {
		t.Fatalf("seed CI: %v", err)
	}

	// The system layer (no organization) sorts last by sort_order; the
	// layers below sort first.
	var systemIDs []string
	for _, key := range []string{"wp589_l_sys", "wp589_size"} {
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO attribute_definition
				(organization_id, ci_type_id, scope, key, data_type, description, sort_order, validation)
			VALUES (NULL, NULL, 'global', $1, 'integer', 'system', 9, '{"op": "range", "min": 0}') RETURNING id::text`,
			key).Scan(&id); err != nil {
			t.Fatalf("system definition %s: %v", key, err)
		}
		systemIDs = append(systemIDs, id)
	}
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(context.Background(), `DELETE FROM attribute_definition WHERE id = ANY($1::uuid[])`, systemIDs)
	})
	define := func(typeID, key, description string, rule *rules.Rule) {
		t.Helper()
		d := &AttributeDefinition{CITypeID: typeID, Key: key, DataType: "integer", Description: description,
			UniqueScope: "org", Inheritable: true, Validation: rule}
		if err := repo.CreateAttributeDefinition(ctx, f.OrgA, d); err != nil {
			t.Fatalf("definition %s (%s): %v", key, description, err)
		}
	}
	define("", "wp589_l_org", "organization", nil)
	define("", "wp589_size", "organization", &rules.Rule{Op: rules.OpRange, Max: ptr(100)})
	define(typ.ID, "wp589_l_type", "type", nil)
	define(typ.ID, "wp589_size", "type", &rules.Rule{Op: rules.OpRange, Min: ptr(10)})
	for _, req := range []UpsertInstanceFieldRequest{
		{Name: "wp589_l_inst", DataType: "integer", Description: "instance"},
		{Name: "wp589_size", DataType: "integer", Description: "instance", Validation: &fieldmeta.ValidationRules{Max: ptr(50)}},
	} {
		if _, err := repo.UpsertInstanceField(ctx, f.OrgA, ciID, req); err != nil {
			t.Fatalf("instance field %s: %v", req.Name, err)
		}
	}

	// resolve returns the WP-589 keys in resolved order and the size
	// definition.
	resolve := func() ([]string, *fieldmeta.FieldDefinition) {
		t.Helper()
		defs, err := ResolveFields(ctx, repo, f.OrgA, typ.ID, ciID)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		var keys []string
		var size *fieldmeta.FieldDefinition
		for i := range defs {
			if strings.HasPrefix(defs[i].Name, "wp589_") {
				keys = append(keys, defs[i].Name)
			}
			if defs[i].Name == "wp589_size" {
				size = &defs[i]
			}
		}
		return keys, size
	}
	bounds := func(d *fieldmeta.FieldDefinition) (lo, hi float64) {
		lo, hi = -1, -1
		if d.Validation != nil && d.Validation.Min != nil {
			lo = *d.Validation.Min
		}
		if d.Validation != nil && d.Validation.Max != nil {
			hi = *d.Validation.Max
		}
		return lo, hi
	}

	keys, size := resolve()
	want := []string{"wp589_l_sys", "wp589_size", "wp589_l_org", "wp589_l_type", "wp589_l_inst"}
	if !slices.Equal(keys, want) {
		t.Errorf("resolved keys %v, want the layer order %v", keys, want)
	}
	if lo, hi := bounds(size); size == nil || size.Description != "instance" || lo != 10 || hi != 50 {
		t.Fatalf("composed wp589_size = %+v, want description instance, range 10..50", size)
	}
	if err := fieldmeta.ValidateValue(*size, float64(60)); err == nil {
		t.Error("60 accepted above the instance maximum 50")
	}

	// Removing the narrower layers one by one: the next wider layer wins.
	steps := []struct {
		remove      func() error
		description string
		lo, hi      float64
		keys        []string
	}{
		{func() error { return repo.DeleteInstanceField(ctx, f.OrgA, ciID, "wp589_size") },
			"type", 10, 100, want},
		{func() error {
			_, err := f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE ci_type_id = $1`, typ.ID)
			return err
		}, "organization", 0, 100, []string{"wp589_l_sys", "wp589_size", "wp589_l_org", "wp589_l_inst"}},
		{func() error {
			_, err := f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE organization_id = $1 AND ci_type_id IS NULL`, f.OrgA)
			return err
		}, "system", 0, -1, []string{"wp589_l_sys", "wp589_size", "wp589_l_inst"}},
	}
	for _, step := range steps {
		if err := step.remove(); err != nil {
			t.Fatal(err)
		}
		keys, size = resolve()
		lo, hi := bounds(size)
		if size == nil || size.Description != step.description || lo != step.lo || hi != step.hi || !slices.Equal(keys, step.keys) {
			t.Errorf("after removing a layer: keys %v, wp589_size %+v; want %v, description %s, range %v..%v",
				keys, size, step.keys, step.description, step.lo, step.hi)
		}
	}
	if _, err := f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE id = ANY($1::uuid[])`, systemIDs); err != nil {
		t.Fatal(err)
	}
	if keys, _ = resolve(); !slices.Equal(keys, []string{"wp589_l_inst"}) {
		t.Errorf("without the rows of attribute_definition: %v, want only the instance layer", keys)
	}

	// Only attribute_definition (system, organization, type) and
	// ci_instance_field_definition (instance) hold attribute definitions.
	rows, err := f.Admin.Query(bg, `SELECT table_name FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name IN ('data_type', 'enum_values', 'validation')
		GROUP BY table_name HAVING count(*) = 3 ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if !slices.Equal(tables, []string{"attribute_definition", "ci_instance_field_definition"}) {
		t.Errorf("tables holding attribute definitions: %v", tables)
	}
	var instanceRows int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM attribute_definition WHERE key = 'wp589_l_inst'`).Scan(&instanceRows); err != nil ||
		instanceRows != 0 {
		t.Errorf("instance attribute in attribute_definition: %d (%v)", instanceRows, err)
	}
}
