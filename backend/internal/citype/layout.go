package citype

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// FormLayout is the layout of a form for a CI type or a template,
// optionally bound to a role (table form_layout, REST /api/v1/layouts,
// WP-259, MET-50). A template layout applies to the template id, whatever
// its version (E-111). role_id names a role or a custom role of the
// organization (E-110). version rises with every change; it is the ETag
// (API-07) and the "layout version" of WP-316. There are no system
// layouts: a layout of a system type is a row of the organization.
type FormLayout struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	Key            string         `json:"key"`
	CITypeID       string         `json:"ci_type_id,omitempty"`
	TemplateID     string         `json:"template_id,omitempty"`
	RoleID         string         `json:"role_id,omitempty"`
	Priority       int            `json:"priority"`
	Version        int64          `json:"version"`
	IsActive       bool           `json:"is_active"`
	Layout         LayoutDocument `json:"layout"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// LayoutDocument is the content of a layout: its sections in order.
type LayoutDocument struct {
	Sections []LayoutSection `json:"sections"`
}

// LayoutSection is one section of a form: a key unique in the layout, a
// title in German and English (MET-01), the keys of the catalog rules
// that make it visible (WP-716; empty: always visible) and its fields.
type LayoutSection struct {
	Key         string        `json:"key"`
	Title       LocalizedText `json:"title"`
	VisibleWhen []string      `json:"visible_when"`
	Fields      []LayoutField `json:"fields"`
}

// LayoutField places an effective field of the target in a section with a
// width of 1 to 12 grid columns.
type LayoutField struct {
	Name  string `json:"name"`
	Width int    `json:"width"`
}

// LayoutInput is the request of POST /api/v1/layouts.
type LayoutInput struct {
	Key        string          `json:"key"`
	CITypeID   string          `json:"ci_type_id,omitempty"`
	TemplateID string          `json:"template_id,omitempty"`
	RoleID     string          `json:"role_id,omitempty"`
	Priority   *int            `json:"priority,omitempty"`
	IsActive   *bool           `json:"is_active,omitempty"`
	Layout     json.RawMessage `json:"layout"`
}

// LayoutPatch is the request of PATCH /api/v1/layouts/{id}. role_id takes
// null to unbind the role. key and the target are immutable: the fields
// are read only to refuse them (422).
type LayoutPatch struct {
	RoleID     json.RawMessage `json:"role_id,omitempty"`
	Priority   *int            `json:"priority,omitempty"`
	IsActive   *bool           `json:"is_active,omitempty"`
	Layout     json.RawMessage `json:"layout,omitempty"`
	Key        *string         `json:"key,omitempty"`
	CITypeID   *string         `json:"ci_type_id,omitempty"`
	TemplateID *string         `json:"template_id,omitempty"`
}

// LayoutFilter narrows the layout list; empty fields do not filter.
type LayoutFilter struct {
	CITypeID   string
	TemplateID string
	RoleID     string
}

// Layout errors.
var (
	ErrLayoutNotFound = errors.New("layout not found")
	// ErrLayoutVersionMismatch: the If-Match version is not the current
	// version of the layout (412, API-07).
	ErrLayoutVersionMismatch = errors.New("the layout was changed since the If-Match version")
)

// reservedSectionKeys are section keys the resolved form uses itself
// (WP-719): the instance attributes and the required fields.
var reservedSectionKeys = map[string]bool{"_instance": true, "_required": true}

// LayoutStore is the layout catalog (MET-50, API-06, WP-259); the
// PostgreSQL repository implements it.
type LayoutStore interface {
	// ListLayoutPage returns a page newest first on (created_at, id), at
	// most one layout more than the limit, and the number the filter
	// selects.
	ListLayoutPage(ctx context.Context, orgID string, filter *LayoutFilter, page api.PaginationParams) ([]FormLayout, int, error)
	GetLayout(ctx context.Context, orgID, id string) (*FormLayout, error)
	CreateLayout(ctx context.Context, orgID string, in *LayoutInput) (*FormLayout, error)
	// UpdateLayout changes a layout; ifMatch, when set, must be its current
	// version (api.IfMatchAny matches any), otherwise
	// ErrLayoutVersionMismatch.
	UpdateLayout(ctx context.Context, orgID, id string, patch *LayoutPatch, ifMatch *int64) (*FormLayout, error)
	DeleteLayout(ctx context.Context, orgID, id string, ifMatch *int64) error
}

// layoutFieldError is a 422 on a field of a layout request.
// validation: the field and a fixed message.
func layoutFieldError(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// checkLayoutKey checks the key (1 to 100 characters).
func checkLayoutKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 100 {
		return "", layoutFieldError("key", "key is required (at most 100 characters)")
	}
	return value, nil
}

// checkLayoutRoleID checks the form of a role id; empty is no role.
func checkLayoutRoleID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" && !api.IsUUID(value) {
		return "", layoutFieldError("role_id", "role_id must name a role of the organization")
	}
	return value, nil
}

// rawLayoutSection is a section as sent; the title is read as a plain map
// so that a title without de or en is refused.
type rawLayoutSection struct {
	Key         string            `json:"key"`
	Title       map[string]string `json:"title"`
	VisibleWhen []string          `json:"visible_when"`
	Fields      []rawLayoutField  `json:"fields"`
}

type rawLayoutField struct {
	Name  string `json:"name"`
	Width *int   `json:"width"`
}

// parseLayoutDocument checks the form of a layout without the target:
// sections with unique, not reserved keys, titles in de and en (MET-01),
// fields with a name and a width of 1 to 12 (default 12), each field in
// one section only. A refusal is a 422 with the path of the value, e.g.
// layout.sections[1].fields[0].width.
func parseLayoutDocument(raw json.RawMessage) (LayoutDocument, error) {
	doc := LayoutDocument{Sections: []LayoutSection{}}
	if isJSONNull(raw) {
		return doc, layoutFieldError("layout", "layout is required")
	}
	var in struct {
		Sections []rawLayoutSection `json:"sections"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || in.Sections == nil {
		return doc, layoutFieldError("layout", "layout must be an object {sections: [{key, title, visible_when, fields}]}")
	}
	sectionKeys := map[string]bool{}
	fieldSection := map[string]bool{}
	for i, s := range in.Sections {
		path := "layout.sections[" + strconv.Itoa(i) + "]"
		key := strings.TrimSpace(s.Key)
		switch {
		case key == "" || utf8.RuneCountInString(key) > 100:
			return doc, layoutFieldError(path+".key", "a section key is required (at most 100 characters)")
		case reservedSectionKeys[key]:
			return doc, layoutFieldError(path+".key", "the section keys _instance and _required are reserved")
		case sectionKeys[key]:
			return doc, layoutFieldError(path+".key", "the section key is used twice")
		}
		sectionKeys[key] = true
		title := LocalizedText{}
		for lang, text := range s.Title {
			title[lang] = text
		}
		if title["de"] == "" || title["en"] == "" || title.Validate() != nil {
			return doc, layoutFieldError(path+".title", "the title needs de and en with 1 to 200 characters each")
		}
		visible := []string{}
		for j, ruleKey := range s.VisibleWhen {
			ruleKey = strings.TrimSpace(ruleKey)
			if ruleKey == "" {
				return doc, layoutFieldError(path+".visible_when["+strconv.Itoa(j)+"]", "visible_when names catalog rules by key")
			}
			visible = append(visible, ruleKey)
		}
		fields := []LayoutField{}
		for j, f := range s.Fields {
			fpath := path + ".fields[" + strconv.Itoa(j) + "]"
			name := strings.TrimSpace(f.Name)
			if name == "" {
				return doc, layoutFieldError(fpath+".name", "a field name is required")
			}
			if fieldSection[name] {
				return doc, layoutFieldError(fpath+".name", "a field may appear in one section only")
			}
			fieldSection[name] = true
			width := 12
			if f.Width != nil {
				width = *f.Width
			}
			if width < 1 || width > 12 {
				return doc, layoutFieldError(fpath+".width", "width must be between 1 and 12")
			}
			fields = append(fields, LayoutField{Name: name, Width: width})
		}
		doc.Sections = append(doc.Sections, LayoutSection{Key: key, Title: title, VisibleWhen: visible, Fields: fields})
	}
	return doc, nil
}

// newLayout checks a create request and returns the layout it describes.
func newLayout(in *LayoutInput) (*FormLayout, error) {
	key, err := checkLayoutKey(in.Key)
	if err != nil {
		return nil, err
	}
	l := &FormLayout{Key: key, CITypeID: strings.TrimSpace(in.CITypeID), TemplateID: strings.TrimSpace(in.TemplateID),
		IsActive: in.IsActive == nil || *in.IsActive}
	if in.Priority != nil {
		l.Priority = *in.Priority
	}
	switch {
	case (l.CITypeID == "") == (l.TemplateID == ""):
		return nil, layoutFieldError("ci_type_id", "exactly one of ci_type_id and template_id is required")
	case l.CITypeID != "" && !api.IsUUID(l.CITypeID):
		return nil, layoutFieldError("ci_type_id", "ci_type_id must name a ci type")
	case l.TemplateID != "" && !api.IsUUID(l.TemplateID):
		return nil, layoutFieldError("template_id", "template_id must name a template")
	}
	if l.RoleID, err = checkLayoutRoleID(in.RoleID); err != nil {
		return nil, err
	}
	if l.Layout, err = parseLayoutDocument(in.Layout); err != nil {
		return nil, err
	}
	return l, nil
}

// apply changes l; key and the target are immutable (422).
func (p *LayoutPatch) apply(l *FormLayout) error {
	for _, f := range []struct {
		field string
		value *string
	}{{"key", p.Key}, {"ci_type_id", p.CITypeID}, {"template_id", p.TemplateID}} {
		if f.value != nil {
			return layoutFieldError(f.field, f.field+" is immutable")
		}
	}
	var err error
	if p.RoleID != nil {
		role := ""
		if !isJSONNull(p.RoleID) {
			if json.Unmarshal(p.RoleID, &role) != nil {
				return layoutFieldError("role_id", "role_id must name a role of the organization")
			}
		}
		if l.RoleID, err = checkLayoutRoleID(role); err != nil {
			return err
		}
	}
	if p.Priority != nil {
		l.Priority = *p.Priority
	}
	setBool(&l.IsActive, p.IsActive)
	if p.Layout != nil {
		if l.Layout, err = parseLayoutDocument(p.Layout); err != nil {
			return err
		}
	}
	return nil
}

// layoutFields maps database constraints onto request fields.
var layoutFields = map[string]string{
	"form_layout_org_key":          "key",
	"form_layout_key_check":        "key",
	"form_layout_type_role":        "role_id",
	"form_layout_template_role":    "role_id",
	"form_layout_ci_type_id_fkey":  "ci_type_id",
	"form_layout_template_id_fkey": "template_id",
	"form_layout_one_target":       "ci_type_id",
}

const layoutColumns = `id::text, organization_id::text, key, COALESCE(ci_type_id::text, ''), COALESCE(template_id::text, ''),
	COALESCE(role_id::text, ''), priority, version, is_active, layout, created_at, updated_at`

func scanLayout(row pgx.Row) (*FormLayout, error) {
	var (
		l   FormLayout
		doc []byte
	)
	if err := row.Scan(&l.ID, &l.OrganizationID, &l.Key, &l.CITypeID, &l.TemplateID, &l.RoleID, &l.Priority, &l.Version,
		&l.IsActive, &doc, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(doc, &l.Layout); err != nil {
		return nil, fmt.Errorf("decode layout: %w", err)
	}
	if l.Layout.Sections == nil {
		l.Layout.Sections = []LayoutSection{}
	}
	return &l, nil
}

func collectLayouts(rows pgx.Rows) ([]FormLayout, error) {
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (FormLayout, error) {
		l, serr := scanLayout(row)
		if serr != nil {
			return FormLayout{}, serr
		}
		return *l, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list layouts: %w", err)
	}
	if out == nil {
		out = []FormLayout{}
	}
	return out, nil
}

// getLayoutTx reads a layout of the organization; lock locks it for a
// change.
func getLayoutTx(ctx context.Context, tx pgx.Tx, id string, lock bool) (*FormLayout, error) {
	if !api.IsUUID(id) {
		return nil, ErrLayoutNotFound
	}
	query := `SELECT ` + layoutColumns + ` FROM form_layout WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	l, err := scanLayout(tx.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLayoutNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read layout: %w", err)
	}
	return l, nil
}

// layoutTargetTypeTx checks the target of a layout under the caller's row
// level security (an own or a system row, otherwise 422 on the target
// field) and returns the CI type whose effective fields the layout uses:
// the type itself or the type of the template (template.ci_type_id; a
// template adds no attribute keys of its own, WP-251).
func layoutTargetTypeTx(ctx context.Context, tx pgx.Tx, l *FormLayout) (string, error) {
	if l.TemplateID != "" {
		tpl, err := getTemplateTx(ctx, tx, l.TemplateID, false)
		if errors.Is(err, ErrTemplateNotFound) {
			return "", layoutFieldError("template_id", "template not found")
		}
		if err != nil {
			return "", err
		}
		return tpl.CITypeID, nil
	}
	_, found, err := ciTypeOrganizationTx(ctx, tx, l.CITypeID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", layoutFieldError("ci_type_id", "ci type not found")
	}
	return l.CITypeID, nil
}

// checkLayoutRoleTx checks that role_id names a role or a custom role of
// the organization (the id set of GET /api/v1/roles, E-110), otherwise 422.
func checkLayoutRoleTx(ctx context.Context, tx pgx.Tx, orgID, roleID string) error {
	if roleID == "" {
		return nil
	}
	var found bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM role WHERE id = $2 AND organization_id = $1)
		OR EXISTS (SELECT 1 FROM custom_role WHERE id = $2 AND organization_id = $1)`, orgID, roleID).Scan(&found)
	if err != nil {
		return fmt.Errorf("check layout role: %w", err)
	}
	if !found {
		return layoutFieldError("role_id", "role_id must name a role of the organization")
	}
	return nil
}

// checkLayoutRulesTx checks that every key in visible_when names a catalog
// rule the organization sees (its own or a system rule, WP-716), otherwise
// 422 on the path of the key.
func checkLayoutRulesTx(ctx context.Context, tx pgx.Tx, doc *LayoutDocument) error {
	var keys []string
	for _, s := range doc.Sections {
		keys = append(keys, s.VisibleWhen...)
	}
	if len(keys) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT key FROM metamodel_rule WHERE key = ANY($1)`, keys)
	if err != nil {
		return fmt.Errorf("read layout rules: %w", err)
	}
	known, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read layout rules: %w", err)
	}
	exists := map[string]bool{}
	for _, k := range known {
		exists[k] = true
	}
	for i, s := range doc.Sections {
		for j, k := range s.VisibleWhen {
			if !exists[k] {
				return layoutFieldError("layout.sections["+strconv.Itoa(i)+"].visible_when["+strconv.Itoa(j)+"]",
					"no catalog rule has this key")
			}
		}
	}
	return nil
}

// checkLayoutFields checks the fields of the layout against the effective
// fields of the CI type without a CI (ResolveFields: system, organization,
// type chain and sets, WP-248); an unknown name is 422 on its path.
func (r *PGRepository) checkLayoutFields(ctx context.Context, orgID, typeID string, doc *LayoutDocument) error {
	defs, err := r.ResolveFields(ctx, orgID, typeID, "")
	if err != nil {
		return err
	}
	names := make(map[string]bool, len(defs))
	for i := range defs {
		names[defs[i].Name] = true
	}
	for i, s := range doc.Sections {
		for j, f := range s.Fields {
			if !names[f.Name] {
				return layoutFieldError("layout.sections["+strconv.Itoa(i)+"].fields["+strconv.Itoa(j)+"].name",
					"the target has no field with this name")
			}
		}
	}
	return nil
}

// layoutTargetType reads the CI type of the layout's target in a
// transaction of its own (before the fields are resolved).
func (r *PGRepository) layoutTargetType(ctx context.Context, orgID string, l *FormLayout) (string, error) {
	var typeID string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var terr error
		typeID, terr = layoutTargetTypeTx(ctx, tx, l)
		return terr
	})
	return typeID, err
}

// validateLayoutTx runs the checks of a write inside its transaction: the
// target is visible, the role belongs to the organization, the rule keys
// exist.
func validateLayoutTx(ctx context.Context, tx pgx.Tx, orgID string, l *FormLayout) error {
	if _, err := layoutTargetTypeTx(ctx, tx, l); err != nil {
		return err
	}
	if err := checkLayoutRoleTx(ctx, tx, orgID, l.RoleID); err != nil {
		return err
	}
	return checkLayoutRulesTx(ctx, tx, &l.Layout)
}

// checkIfMatch compares an If-Match version with the current one.
func checkIfMatch(ifMatch *int64, version int64) error {
	if ifMatch != nil && *ifMatch != api.IfMatchAny && *ifMatch != version {
		return ErrLayoutVersionMismatch
	}
	return nil
}

// ListLayoutPage implements LayoutStore: the organization's layouts newest
// first (API-02), filtered by target and role.
func (r *PGRepository) ListLayoutPage(ctx context.Context, orgID string, filter *LayoutFilter, page api.PaginationParams) ([]FormLayout, int, error) {
	out := []FormLayout{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		const where = `($1 = '' OR ci_type_id::text = $1) AND ($2 = '' OR template_id::text = $2)
			AND ($3 = '' OR role_id::text = $3)`
		args := []any{filter.CITypeID, filter.TemplateID, filter.RoleID}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM form_layout WHERE `+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count layouts: %w", err)
		}
		keyset, tail, pageArgs, err := api.NewestFirstSQL(page, len(args)+1)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+layoutColumns+` FROM form_layout WHERE `+where+` AND `+keyset+` `+tail,
			append(args, pageArgs...)...)
		if err != nil {
			return fmt.Errorf("list layouts: %w", err)
		}
		out, err = collectLayouts(rows)
		return err
	})
	return out, total, err
}

// GetLayout implements LayoutStore.
func (r *PGRepository) GetLayout(ctx context.Context, orgID, id string) (*FormLayout, error) {
	var out *FormLayout
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = getLayoutTx(ctx, tx, id, false)
		return err
	})
	return out, err
}

// CreateLayout implements LayoutStore: a layout of the organization on a
// target it sees, checked against the effective fields of the target,
// audited as form_layout.created. A taken key or a second layout for the
// same target and role is 409.
func (r *PGRepository) CreateLayout(ctx context.Context, orgID string, in *LayoutInput) (*FormLayout, error) {
	l, err := newLayout(in)
	if err != nil {
		return nil, err
	}
	typeID, err := r.layoutTargetType(ctx, orgID, l)
	if err != nil {
		return nil, err
	}
	if ferr := r.checkLayoutFields(ctx, orgID, typeID, &l.Layout); ferr != nil {
		return nil, ferr
	}
	var out *FormLayout
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if verr := validateLayoutTx(ctx, tx, orgID, l); verr != nil {
			return verr
		}
		created, ierr := scanLayout(tx.QueryRow(ctx, `INSERT INTO form_layout
				(organization_id, key, ci_type_id, template_id, role_id, priority, is_active, layout)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+layoutColumns,
			orgID, l.Key, nilIfEmpty(l.CITypeID), nilIfEmpty(l.TemplateID), nilIfEmpty(l.RoleID), l.Priority, l.IsActive, l.Layout))
		if ierr != nil {
			return api.FieldDBError(ierr, layoutFields)
		}
		out = created
		return recordMetamodelAudit(ctx, tx, orgID, "form_layout.created", "form_layout", created.ID, nil, created)
	})
	return out, err
}

// UpdateLayout implements LayoutStore: role, priority, is_active and the
// layout; version rises by one. An If-Match version other than the current
// one is ErrLayoutVersionMismatch (412).
func (r *PGRepository) UpdateLayout(ctx context.Context, orgID, id string, patch *LayoutPatch, ifMatch *int64) (*FormLayout, error) {
	current, err := r.GetLayout(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	candidate := *current
	if perr := patch.apply(&candidate); perr != nil {
		return nil, perr
	}
	if patch.Layout != nil {
		typeID, terr := r.layoutTargetType(ctx, orgID, &candidate)
		if terr != nil {
			return nil, terr
		}
		if ferr := r.checkLayoutFields(ctx, orgID, typeID, &candidate.Layout); ferr != nil {
			return nil, ferr
		}
	}
	var out *FormLayout
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, lerr := getLayoutTx(ctx, tx, id, true)
		if lerr != nil {
			return lerr
		}
		if verr := checkIfMatch(ifMatch, before.Version); verr != nil {
			return verr
		}
		l := *before
		if perr := patch.apply(&l); perr != nil {
			return perr
		}
		if verr := validateLayoutTx(ctx, tx, orgID, &l); verr != nil {
			return verr
		}
		updated, uerr := scanLayout(tx.QueryRow(ctx, `UPDATE form_layout
			SET role_id = $2, priority = $3, is_active = $4, layout = $5, version = version + 1
			WHERE id = $1 RETURNING `+layoutColumns,
			id, nilIfEmpty(l.RoleID), l.Priority, l.IsActive, l.Layout))
		if uerr != nil {
			return api.FieldDBError(uerr, layoutFields)
		}
		out = updated
		return recordMetamodelAudit(ctx, tx, orgID, "form_layout.updated", "form_layout", id, before, updated)
	})
	return out, err
}

// DeleteLayout implements LayoutStore, audited as form_layout.deleted.
func (r *PGRepository) DeleteLayout(ctx context.Context, orgID, id string, ifMatch *int64) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, err := getLayoutTx(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if verr := checkIfMatch(ifMatch, before.Version); verr != nil {
			return verr
		}
		if _, err = tx.Exec(ctx, `DELETE FROM form_layout WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete layout: %w", err)
		}
		return recordMetamodelAudit(ctx, tx, orgID, "form_layout.deleted", "form_layout", id, before, nil)
	})
}

// cloneTypeLayoutsTx copies the type layouts of the source type to its
// clone (WP-259, assigned by WP-249) with role, priority, is_active and
// layout; the copies start at version 1 and take the clone's key as the
// prefix of their key. No audit entry per copied layout, like the copied
// attribute definitions.
func cloneTypeLayoutsTx(ctx context.Context, tx pgx.Tx, orgID, sourceID, cloneID, cloneKey string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO form_layout (organization_id, key, ci_type_id, role_id, priority, is_active, layout)
		SELECT $1, left($4 || '.' || key, 100), $3, role_id, priority, is_active, layout
		FROM form_layout WHERE ci_type_id = $2`, orgID, sourceID, cloneID, cloneKey); err != nil {
		return fmt.Errorf("clone layouts of ci type: %w", api.FieldDBError(err, layoutFields))
	}
	return nil
}
