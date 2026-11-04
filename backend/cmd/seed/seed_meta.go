package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The Phase-2 content of an organization (WP-325, SIM-01 [P2]).
type phase2Level int

const (
	// phase2None: the small organizations of -orgs (WP-439).
	phase2None phase2Level = iota
	// phase2Subset: acme, a derived type, a template with one published
	// version and the user viewer.
	phase2Subset
	// phase2Full: the demo organization.
	phase2Full
)

// The metamodel keys of the Phase-2 seed.
const (
	// ParentTypeKey is the system type the seed derives its types from.
	ParentTypeKey = "server"
	// ChassisTypeKey and BladeTypeKey are the derived types (WP-248).
	ChassisTypeKey = "blade_chassis"
	BladeTypeKey   = "blade_server"
	// ComputedKey is the computed system definition of WP-718; the seed
	// uses it in templates and layouts and never creates it.
	ComputedKey = "warranty_days_left"
)

// seedRoles are the users of the standard roles besides the
// administrator, by Phase-2 level: role@<domain> with the role of that
// name; the client technician is scoped to the client hq.
func seedRoles(level phase2Level) []string {
	switch level {
	case phase2Full:
		return []string{"engineer", "viewer", "client_technician"}
	case phase2Subset:
		return []string{"viewer"}
	default:
		return nil
	}
}

// metaAttribute is an attribute definition of a derived type or of a set.
type metaAttribute struct {
	key, de, en, dataType string
	enum                  []string
}

// metaType is a type derived from ParentTypeKey with its attributes.
type metaType struct {
	key, name, de string
	attributes    []metaAttribute
}

// metaSet is an attribute set (WP-250) with its attributes.
type metaSet struct {
	key, de, en string
	attributes  []metaAttribute
}

// metaItem is an item of a template version: an attribute of the type or
// the computed system definition (ComputedKey); required sets
// required_override.
type metaItem struct {
	key      string
	required bool
}

// metaTemplate is a template of a derived type with the items of each
// version; every version is published.
type metaTemplate struct {
	key, typeKey, de, en string
	versions             [][]metaItem
}

// metaLayout is the form layout of a template for a role (WP-259).
type metaLayout struct {
	template, role string
	sections       []metaSection
}

type metaSection struct {
	key, de, en string
	fields      []string
}

// metaRule is a catalog rule on a derived type (WP-716, evaluated from
// WP-257 on).
type metaRule struct {
	key, typeKey, attribute, kind string
	condition, ast, message       string
}

// metaGrant is a field grant of a role on a CI attribute of a derived
// type (WP-264, E-43).
type metaGrant struct {
	role, typeKey, field string
	level                permission.Level
}

// metaContent is the Phase-2 metamodel of an organization.
type metaContent struct {
	types     []metaType
	sets      []metaSet
	typeSets  map[string][]string
	templates []metaTemplate
	rules     []metaRule
	layouts   []metaLayout
	grants    []metaGrant
	// sensitive are CI attributes of derived types marked sensitive
	// without a grant: typeKey → field.
	sensitive [][2]string
}

var (
	chassisType = metaType{key: ChassisTypeKey, name: "Blade chassis", de: "Blade-Chassis", attributes: []metaAttribute{
		{key: "chassis_slots", de: "Einschübe", en: "Slots", dataType: "integer"},
		{key: "chassis_asset_tag", de: "Chassis-Kennung", en: "Chassis tag", dataType: "text"},
	}}
	bladeType = metaType{key: BladeTypeKey, name: "Blade server", de: "Blade-Server", attributes: []metaAttribute{
		{key: "blade_slot", de: "Einschub", en: "Slot", dataType: "integer"},
		{key: "blade_role", de: "Rolle", en: "Role", dataType: "enum", enum: []string{"compute", "storage"}},
		{key: "storage_pool", de: "Speicherpool", en: "Storage pool", dataType: "text"},
	}}
	bladeTemplateV1 = []metaItem{{key: "blade_slot"}, {key: "blade_role"}, {key: ComputedKey}}
	bladeTemplate   = metaTemplate{key: "blade_server_compute", typeKey: BladeTypeKey, de: "Blade-Server (Compute)",
		en: "Blade server (compute)", versions: [][]metaItem{
			bladeTemplateV1,
			{{key: "blade_slot", required: true}, {key: "blade_role"}, {key: "storage_pool"}, {key: ComputedKey}},
		}}
	chassisTemplate = metaTemplate{key: "blade_chassis_standard", typeKey: ChassisTypeKey, de: "Blade-Chassis (Standard)",
		en: "Blade chassis (standard)", versions: [][]metaItem{
			{{key: "chassis_slots"}, {key: "chassis_asset_tag"}, {key: ComputedKey}},
			{{key: "chassis_slots", required: true}, {key: "chassis_asset_tag"}, {key: ComputedKey}},
		}}
)

// phase2Content is the Phase-2 metamodel of the level: the full one for
// demo, a derived type with a template of one published version for acme.
func phase2Content(level phase2Level) *metaContent {
	switch level {
	case phase2Full:
		return &metaContent{
			types: []metaType{chassisType, bladeType},
			sets: []metaSet{
				{key: "blade_power", de: "Stromversorgung", en: "Power supply", attributes: []metaAttribute{
					{key: "psu_count", de: "Netzteile", en: "Power supplies", dataType: "integer"},
					{key: "psu_redundancy", de: "Redundanz", en: "Redundancy", dataType: "enum", enum: []string{"n", "n+1", "2n"}},
				}},
				{key: "blade_support", de: "Wartungsvertrag", en: "Support", attributes: []metaAttribute{
					{key: "support_level", de: "Servicelevel", en: "Support level", dataType: "text"},
				}},
			},
			typeSets: map[string][]string{
				ChassisTypeKey: {"blade_power", "blade_support"},
				BladeTypeKey:   {"blade_support"},
			},
			templates: []metaTemplate{chassisTemplate, bladeTemplate},
			rules: []metaRule{
				{key: "seed-chassis-asset-tag", typeKey: ChassisTypeKey, attribute: "chassis_asset_tag", kind: citype.RuleKindValidation,
					ast:     `{"op":"pattern","pattern":"^BC-[0-9]{4}$"}`,
					message: `{"de":"Die Chassis-Kennung lautet BC-0000 bis BC-9999.","en":"The chassis tag is BC-0000 to BC-9999."}`},
				{key: "seed-storage-pool-required", typeKey: BladeTypeKey, attribute: "storage_pool", kind: citype.RuleKindRequired,
					condition: `{"op":"eq","field":"blade_role","value":"storage"}`,
					message:   `{"de":"Ein Storage-Blade braucht einen Speicherpool.","en":"A storage blade needs a storage pool."}`},
			},
			layouts: []metaLayout{
				{template: chassisTemplate.key, role: "engineer", sections: []metaSection{
					{key: "chassis", de: "Chassis", en: "Chassis", fields: []string{"chassis_slots", "chassis_asset_tag"}},
					{key: "power", de: "Strom und Wartung", en: "Power and support", fields: []string{"psu_count", "psu_redundancy", "support_level"}},
					{key: "warranty", de: "Garantie", en: "Warranty", fields: []string{ComputedKey}},
				}},
				{template: chassisTemplate.key, role: "viewer", sections: []metaSection{
					{key: "overview", de: "Übersicht", en: "Overview", fields: []string{"chassis_slots", ComputedKey}},
				}},
				{template: bladeTemplate.key, role: "engineer", sections: []metaSection{
					{key: "blade", de: "Blade", en: "Blade", fields: []string{"blade_slot", "blade_role", "storage_pool"}},
					{key: "support", de: "Wartung und Garantie", en: "Support and warranty", fields: []string{"support_level", ComputedKey}},
				}},
				{template: bladeTemplate.key, role: "viewer", sections: []metaSection{
					{key: "overview", de: "Übersicht", en: "Overview", fields: []string{"blade_role", ComputedKey}},
				}},
			},
			grants: []metaGrant{
				{role: "viewer", typeKey: BladeTypeKey, field: "attributes.blade_slot", level: permission.LevelNone},
				{role: "viewer", typeKey: BladeTypeKey, field: "attributes.blade_role", level: permission.LevelRead},
			},
			sensitive: [][2]string{{BladeTypeKey, "attributes.storage_pool"}},
		}
	case phase2Subset:
		return &metaContent{
			types: []metaType{bladeType},
			templates: []metaTemplate{{key: bladeTemplate.key, typeKey: BladeTypeKey, de: bladeTemplate.de, en: bladeTemplate.en,
				versions: [][]metaItem{bladeTemplateV1}}},
		}
	default:
		return nil
	}
}

// users creates the users of the standard roles (seedRoles) in the setup
// transaction, like the administrator: role@<domain> with the role of that
// name, the client technician scoped to the client hq. Their permissions
// come from the role matrix the organization's roles hold
// (permission.StandardRoles).
func (o *orgSeed) users(ctx context.Context, tx pgx.Tx) error {
	for _, role := range seedRoles(o.phase2) {
		userID := o.uid("user", role)
		if _, err := tx.Exec(ctx, `INSERT INTO app_user (id, organization_id, email, display_name) VALUES ($1, $2, $3, $4)
			ON CONFLICT (id) DO NOTHING`, userID, o.id, role+"@"+o.domain, role); err != nil {
			return err
		}
		var scopeClient any
		if role == "client_technician" {
			scopeClient = o.uid("client", "hq")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id)
			SELECT $1, $2, r.id, $4 FROM role r WHERE r.organization_id = $1 AND r.name = $3
			AND NOT EXISTS (SELECT 1 FROM role_assignment a WHERE a.user_id = $2 AND a.role_id = r.id)`,
			o.id, userID, role, scopeClient); err != nil {
			return err
		}
	}
	return nil
}

// phase2Steps runs the Phase-2 steps of the organization after its setup
// transaction (K20): one transaction of its own through database.WithTenant,
// organization-wide as the administrator, which the services of the
// metamodel and the field permissions join (database.ContextWithTx); the
// administrator is the actor of their audit entries. Objects created
// through the services are found again by their keys, so a second run
// creates nothing. No step queues a job: none of the definitions is
// searchable, unique or computed.
func (o *orgSeed) phase2Steps(ctx context.Context, pool *pgxpool.Pool) error {
	content := phase2Content(o.phase2)
	if content == nil {
		return nil
	}
	admin := o.uid("user", "admin")
	scope := database.OrgWideScope(o.id, admin)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: o.id, UserID: admin})
	return database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		m := &metaSeed{org: o, tx: tx, admin: admin, repo: citype.NewPGRepository(pool),
			grants: permission.NewPGFieldGrants(pool), types: map[string]string{}, sets: map[string]string{},
			templates: map[string]string{}}
		ctx = database.ContextWithTx(ctx, tx)
		steps := []struct {
			name string
			fn   func(context.Context, *metaContent) error
		}{
			{"types", m.ensureTypes},
			{"attribute sets", m.ensureSets},
			{"templates", m.ensureTemplates},
			{"rules", m.ensureRules},
			{"layouts", m.ensureLayouts},
			{"field permissions", m.ensureFieldPermissions},
		}
		for _, s := range steps {
			if err := s.fn(ctx, content); err != nil {
				return fmt.Errorf("%s: %w", s.name, err)
			}
		}
		return nil
	})
}

// metaSeed holds the Phase-2 transaction of an organization and the ids
// of the objects created or found, by key.
type metaSeed struct {
	org       *orgSeed
	tx        pgx.Tx
	admin     string
	repo      *citype.PGRepository
	grants    *permission.PGFieldGrants
	types     map[string]string
	sets      map[string]string
	templates map[string]string
}

// lookup returns the id the query finds, "" for none.
func (m *metaSeed) lookup(ctx context.Context, query string, args ...any) (string, error) {
	var id string
	err := m.tx.QueryRow(ctx, query, args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// definition creates an attribute definition of a type or a set through
// citype.PGRepository.CreateAttributeDefinition unless the key exists.
func (m *metaSeed) definition(ctx context.Context, typeID, setID string, a *metaAttribute) error {
	id, err := m.lookup(ctx, `SELECT id::text FROM attribute_definition WHERE organization_id = $1 AND key = $2
		AND ci_type_id IS NOT DISTINCT FROM NULLIF($3, '')::uuid AND attribute_set_id IS NOT DISTINCT FROM NULLIF($4, '')::uuid`,
		m.org.id, a.key, typeID, setID)
	if err != nil || id != "" {
		return err
	}
	return m.repo.CreateAttributeDefinition(ctx, m.org.id, &citype.AttributeDefinition{
		CITypeID: typeID, AttributeSetID: setID, Key: a.key, DisplayName: citype.LocalizedText{"de": a.de, "en": a.en},
		Description: "Seed (WP-325)", DataType: a.dataType, EnumValues: a.enum, Inheritable: true,
	})
}

// ensureTypes derives the types from ParentTypeKey (parent_type_id,
// WP-248) with their attributes.
func (m *metaSeed) ensureTypes(ctx context.Context, c *metaContent) error {
	var parentID, category string
	if err := m.tx.QueryRow(ctx, `SELECT id::text, COALESCE(category, '') FROM ci_type WHERE organization_id IS NULL AND key = $1`,
		ParentTypeKey).Scan(&parentID, &category); err != nil {
		return fmt.Errorf("system type %s: %w", ParentTypeKey, err)
	}
	for i := range c.types {
		t := &c.types[i]
		id, err := m.lookup(ctx, `SELECT id::text FROM ci_type WHERE organization_id = $1 AND key = $2`, m.org.id, t.key)
		if err != nil {
			return err
		}
		if id == "" {
			typ := &citype.Type{OrganizationID: m.org.id, Key: t.key, Name: t.name,
				DisplayName: citype.LocalizedText{"de": t.de, "en": t.name}, Category: category, ParentTypeID: parentID,
				Description: "Seed (WP-325): derived from " + ParentTypeKey}
			if err = m.repo.Create(ctx, typ); err != nil {
				return fmt.Errorf("%s: %w", t.key, err)
			}
			id = typ.ID
		}
		m.types[t.key] = id
		for j := range t.attributes {
			if err = m.definition(ctx, id, "", &t.attributes[j]); err != nil {
				return fmt.Errorf("%s.%s: %w", t.key, t.attributes[j].key, err)
			}
		}
	}
	return nil
}

// ensureSets creates the attribute sets with their attributes (WP-250) and
// assigns them to the types; a type with sets keeps its assignment.
func (m *metaSeed) ensureSets(ctx context.Context, c *metaContent) error {
	for i := range c.sets {
		s := &c.sets[i]
		id, err := m.lookup(ctx, `SELECT id::text FROM attribute_set WHERE organization_id = $1 AND key = $2`, m.org.id, s.key)
		if err != nil {
			return err
		}
		if id == "" {
			set := &citype.AttributeSet{Key: s.key, DisplayName: citype.LocalizedText{"de": s.de, "en": s.en},
				Description: "Seed (WP-325)", IsActive: true}
			if err = m.repo.CreateAttributeSet(ctx, m.org.id, set); err != nil {
				return fmt.Errorf("%s: %w", s.key, err)
			}
			id = set.ID
		}
		m.sets[s.key] = id
		for j := range s.attributes {
			if err = m.definition(ctx, "", id, &s.attributes[j]); err != nil {
				return fmt.Errorf("%s.%s: %w", s.key, s.attributes[j].key, err)
			}
		}
	}
	for _, t := range c.types {
		keys := c.typeSets[t.key]
		if len(keys) == 0 {
			continue
		}
		var assigned bool
		if err := m.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci_type_attribute_set WHERE ci_type_id = $1)`,
			m.types[t.key]).Scan(&assigned); err != nil || assigned {
			return err
		}
		assignments := make([]citype.AttributeSetAssignment, 0, len(keys))
		for pos, key := range keys {
			assignments = append(assignments, citype.AttributeSetAssignment{AttributeSetID: m.sets[key], Position: pos})
		}
		if _, err := m.repo.SetTypeAttributeSets(ctx, m.org.id, m.types[t.key], assignments); err != nil {
			return fmt.Errorf("sets of %s: %w", t.key, err)
		}
	}
	return nil
}

// items resolves the items of a template version: attributes of the type
// by key, ComputedKey as the system definition of WP-718.
func (m *metaSeed) items(ctx context.Context, typeID string, in []metaItem) ([]citype.TemplateVersionItem, error) {
	out := make([]citype.TemplateVersionItem, 0, len(in))
	for pos, it := range in {
		var id string
		var err error
		if it.key == ComputedKey {
			id, err = m.lookup(ctx, `SELECT id::text FROM attribute_definition WHERE organization_id IS NULL AND ci_type_id IS NULL
				AND scope = 'global' AND key = $1`, it.key)
		} else {
			id, err = m.lookup(ctx, `SELECT id::text FROM attribute_definition WHERE ci_type_id = $1 AND key = $2`, typeID, it.key)
		}
		if err != nil {
			return nil, err
		}
		if id == "" {
			return nil, fmt.Errorf("attribute %s not found", it.key)
		}
		item := citype.TemplateVersionItem{AttributeDefinitionID: id, Position: pos}
		if it.required {
			required := true
			item.RequiredOverride = &required
		}
		out = append(out, item)
	}
	return out, nil
}

// ensureTemplates creates the templates and publishes each version through
// the direct way of WP-251: version 1 is the draft of the new template,
// every further version a draft copy of the current one; the items are set
// on the draft, then it is published (composition check, published_at,
// published_by, current_version). A published version is kept.
func (m *metaSeed) ensureTemplates(ctx context.Context, c *metaContent) error {
	for i := range c.templates {
		t := &c.templates[i]
		typeID := m.types[t.typeKey]
		id, err := m.lookup(ctx, `SELECT id::text FROM template WHERE organization_id = $1 AND key = $2`, m.org.id, t.key)
		if err != nil {
			return err
		}
		if id == "" {
			tpl := &citype.Template{Key: t.key, CITypeID: typeID, DisplayName: citype.LocalizedText{"de": t.de, "en": t.en},
				Description: "Seed (WP-325)", IsActive: true}
			if err = m.repo.CreateTemplate(ctx, m.org.id, tpl); err != nil {
				return fmt.Errorf("%s: %w", t.key, err)
			}
			id = tpl.ID
		}
		m.templates[t.key] = id
		for n, spec := range t.versions {
			version := n + 1
			var status string
			err = m.tx.QueryRow(ctx, `SELECT status FROM template_version WHERE template_id = $1 AND version = $2`, id, version).Scan(&status)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				created, cerr := m.repo.CreateTemplateVersion(ctx, m.org.id, id)
				if cerr != nil {
					return fmt.Errorf("%s version %d: %w", t.key, version, cerr)
				}
				if created.Version != version {
					return fmt.Errorf("%s: created version %d, want %d", t.key, created.Version, version)
				}
			case err != nil:
				return err
			case status != citype.TemplateVersionDraft:
				continue
			}
			items, ierr := m.items(ctx, typeID, spec)
			if ierr != nil {
				return fmt.Errorf("%s version %d: %w", t.key, version, ierr)
			}
			if _, err = m.repo.SetTemplateVersionItems(ctx, m.org.id, id, version, items); err != nil {
				return fmt.Errorf("%s version %d: items: %w", t.key, version, err)
			}
			if _, err = m.repo.PublishTemplateVersion(ctx, m.org.id, id, version); err != nil {
				return fmt.Errorf("%s version %d: publish: %w", t.key, version, err)
			}
		}
	}
	return nil
}

// rawJSON is nil for an empty text.
func rawJSON(text string) json.RawMessage {
	if text == "" {
		return nil
	}
	return json.RawMessage(text)
}

// ensureRules creates the catalog rules on the types (WP-716).
func (m *metaSeed) ensureRules(ctx context.Context, c *metaContent) error {
	for _, r := range c.rules {
		id, err := m.lookup(ctx, `SELECT id::text FROM metamodel_rule WHERE organization_id = $1 AND key = $2`, m.org.id, r.key)
		if err != nil || id != "" {
			if err != nil {
				return err
			}
			continue
		}
		if _, err = m.repo.CreateRule(ctx, m.org.id, &citype.RuleInput{Key: r.key, AttributeKey: r.attribute, Kind: r.kind,
			CITypeID: m.types[r.typeKey], Condition: rawJSON(r.condition), AST: rawJSON(r.ast),
			Message: rawJSON(r.message)}); err != nil {
			return fmt.Errorf("%s: %w", r.key, err)
		}
	}
	return nil
}

// layoutKey is the key of the layout of a template for a role.
func layoutKey(template, role string) string { return "seed-" + template + "-" + role }

// roleID returns the id of the organization's standard role name.
func (m *metaSeed) roleID(ctx context.Context, name string) (string, error) {
	var id string
	if err := m.tx.QueryRow(ctx, `SELECT id::text FROM role WHERE organization_id = $1 AND name = $2`, m.org.id, name).Scan(&id); err != nil {
		return "", fmt.Errorf("role %s: %w", name, err)
	}
	return id, nil
}

// ensureLayouts creates the form layouts of the templates per role
// (WP-259).
func (m *metaSeed) ensureLayouts(ctx context.Context, c *metaContent) error {
	for _, l := range c.layouts {
		key := layoutKey(l.template, l.role)
		id, err := m.lookup(ctx, `SELECT id::text FROM form_layout WHERE organization_id = $1 AND key = $2`, m.org.id, key)
		if err != nil || id != "" {
			if err != nil {
				return err
			}
			continue
		}
		role, err := m.roleID(ctx, l.role)
		if err != nil {
			return err
		}
		doc := citype.LayoutDocument{Sections: make([]citype.LayoutSection, 0, len(l.sections))}
		for _, s := range l.sections {
			fields := make([]citype.LayoutField, 0, len(s.fields))
			for _, f := range s.fields {
				fields = append(fields, citype.LayoutField{Name: f, Width: 6})
			}
			doc.Sections = append(doc.Sections, citype.LayoutSection{Key: s.key, Title: citype.LocalizedText{"de": s.de, "en": s.en},
				VisibleWhen: []string{}, Fields: fields})
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if _, err = m.repo.CreateLayout(ctx, m.org.id, &citype.LayoutInput{Key: key, TemplateID: m.templates[l.template],
			RoleID: role, Layout: raw}); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// ensureFieldPermissions creates the field grants of roles on CI
// attributes and the sensitivity marks (WP-264, E-43); the field
// permissions of the asset fields follow in WP-819.
func (m *metaSeed) ensureFieldPermissions(ctx context.Context, c *metaContent) error {
	for _, g := range c.grants {
		role, err := m.roleID(ctx, g.role)
		if err != nil {
			return err
		}
		typeID, field := m.types[g.typeKey], g.field
		id, err := m.lookup(ctx, `SELECT id::text FROM field_grant WHERE organization_id = $1 AND role_id = $2
			AND object_kind = 'ci' AND ci_type_id = $3 AND field_key = $4`, m.org.id, role, typeID, field)
		if err != nil || id != "" {
			if err != nil {
				return err
			}
			continue
		}
		if _, err = m.grants.CreateFieldGrant(ctx, m.org.id, &permission.FieldGrant{RoleID: &role, ObjectKind: permission.KindCI,
			CITypeID: &typeID, FieldKey: &field, Level: g.level, CreatedBy: &m.admin}); err != nil {
			return fmt.Errorf("grant %s %s: %w", g.role, g.field, err)
		}
	}
	for _, s := range c.sensitive {
		typeID, field := m.types[s[0]], s[1]
		id, err := m.lookup(ctx, `SELECT id::text FROM field_sensitivity WHERE organization_id = $1 AND object_kind = 'ci'
			AND ci_type_id = $2 AND field_key = $3`, m.org.id, typeID, field)
		if err != nil || id != "" {
			if err != nil {
				return err
			}
			continue
		}
		if _, err = m.grants.CreateSensitiveField(ctx, m.org.id, &permission.SensitiveField{ObjectKind: permission.KindCI,
			CITypeID: &typeID, FieldKey: field, CreatedBy: &m.admin}); err != nil {
			return fmt.Errorf("sensitivity %s: %w", field, err)
		}
	}
	return nil
}
