package citype

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// Kinds of a catalog rule (WP-716, MET-45). allowed_values_when is a
// validation with a condition and an enum.
const (
	RuleKindValidation = "validation"
	RuleKindRequired   = "required"
	RuleKindReadonly   = "readonly"
	RuleKindVisible    = "visible"
)

// RuleKinds are the kinds of a catalog rule.
var RuleKinds = []string{RuleKindValidation, RuleKindRequired, RuleKindReadonly, RuleKindVisible}

// MetamodelRule is a named rule of the rule catalog (table metamodel_rule,
// REST /api/v1/rules, WP-716). It targets the attribute attribute_key of
// exactly one target: a CI type, an attribute set or a template version
// (E-111). condition is a condition of MET-45 (null = always), ast the
// value rule of a validation. organization_id empty marks a read-only
// system rule (E-10). version rises with every change. The rules are
// evaluated when writing from WP-257 on; the rules of the attribute
// definitions (validation, conditional) stay beside them (E-114).
type MetamodelRule struct {
	ID                string          `json:"id"`
	OrganizationID    string          `json:"organization_id,omitempty"`
	Key               string          `json:"key"`
	AttributeKey      string          `json:"attribute_key"`
	Kind              string          `json:"kind"`
	CITypeID          string          `json:"ci_type_id,omitempty"`
	AttributeSetID    string          `json:"attribute_set_id,omitempty"`
	TemplateVersionID string          `json:"template_version_id,omitempty"`
	Condition         json.RawMessage `json:"condition"`
	AST               json.RawMessage `json:"ast"`
	Message           LocalizedText   `json:"message"`
	IsSystem          bool            `json:"is_system"`
	IsActive          bool            `json:"is_active"`
	Version           int64           `json:"version"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// RuleInput is the request of POST /api/v1/rules.
type RuleInput struct {
	Key               string          `json:"key"`
	AttributeKey      string          `json:"attribute_key"`
	Kind              string          `json:"kind"`
	CITypeID          string          `json:"ci_type_id,omitempty"`
	AttributeSetID    string          `json:"attribute_set_id,omitempty"`
	TemplateVersionID string          `json:"template_version_id,omitempty"`
	Condition         json.RawMessage `json:"condition,omitempty"`
	AST               json.RawMessage `json:"ast,omitempty"`
	Message           json.RawMessage `json:"message,omitempty"`
	IsActive          *bool           `json:"is_active,omitempty"`
}

// RulePatch is the request of PATCH /api/v1/rules/{id}. condition, ast and
// message take null to remove them. key and the target are immutable: the
// fields are read only to refuse them (422).
type RulePatch struct {
	AttributeKey      *string         `json:"attribute_key,omitempty"`
	Kind              *string         `json:"kind,omitempty"`
	Condition         json.RawMessage `json:"condition,omitempty"`
	AST               json.RawMessage `json:"ast,omitempty"`
	Message           json.RawMessage `json:"message,omitempty"`
	IsActive          *bool           `json:"is_active,omitempty"`
	Key               *string         `json:"key,omitempty"`
	CITypeID          *string         `json:"ci_type_id,omitempty"`
	AttributeSetID    *string         `json:"attribute_set_id,omitempty"`
	TemplateVersionID *string         `json:"template_version_id,omitempty"`
}

// RuleFilter narrows the rule list; empty fields do not filter.
type RuleFilter struct {
	CITypeID          string
	AttributeSetID    string
	TemplateVersionID string
	AttributeKey      string
	Kind              string
}

// RuleTargets names the targets whose active rules ActiveRules reads.
type RuleTargets struct {
	CITypeIDs          []string
	AttributeSetIDs    []string
	TemplateVersionIDs []string
}

// Rule catalog errors.
var (
	ErrRuleNotFound = errors.New("rule not found")
	// ErrRuleReadOnly: system rules cannot be changed or deleted by an
	// organization (E-10).
	ErrRuleReadOnly = errors.New("system rules are read-only")
)

// RuleStore is the rule catalog (API-06, MET-45, WP-716); the PostgreSQL
// repository implements it.
type RuleStore interface {
	// ListRulePage returns a page newest first on (created_at, id), at most
	// one rule more than the limit, and the number the filter selects.
	ListRulePage(ctx context.Context, orgID string, filter *RuleFilter, page api.PaginationParams) ([]MetamodelRule, int, error)
	GetRule(ctx context.Context, orgID, id string) (*MetamodelRule, error)
	CreateRule(ctx context.Context, orgID string, in *RuleInput) (*MetamodelRule, error)
	UpdateRule(ctx context.Context, orgID, id string, patch *RulePatch) (*MetamodelRule, error)
	DeleteRule(ctx context.Context, orgID, id string) error
	// ActiveRules returns the active rules of the targets the organization
	// sees (its own and the system rules), ordered by key and id (WP-257).
	ActiveRules(ctx context.Context, orgID string, targets RuleTargets) ([]MetamodelRule, error)
}

// ruleFieldError is a 422 on a field of a rule request.
func ruleFieldError(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// pathToken is one property of a parser path, e.g. rules[1] or field.
var pathToken = regexp.MustCompile(`^[a-z_]+(\[\d+\])*$`)

// expressionPath is the field of a refused expression: the field name
// joined with the path the parser puts in front of its message (e.g.
// condition.rules[1].field for "rules[1].field: …", ast.rules[0].pattern for
// "rules[0]: pattern: …").
func expressionPath(field, message string) string {
	parts := strings.Split(message, ": ")
	path := []string{field}
	for _, p := range parts[:len(parts)-1] {
		for _, token := range strings.Split(p, ".") {
			if !pathToken.MatchString(token) {
				return strings.Join(path, ".")
			}
			if token != "condition" && token != "rule" {
				path = append(path, token)
			}
		}
	}
	return strings.Join(path, ".")
}

// parseRuleCondition checks a condition strictly (WP-715).
// validation: the texts of rules.ParseCondition, naming the path that fails.
func parseRuleCondition(raw json.RawMessage) (rules.Rule, error) {
	return rules.ParseCondition(raw)
}

// parseRuleAST checks the value rule of a validation strictly (WP-715).
// validation: the texts of rules.Parse and rules.Rule.Validate, naming the
// path that fails.
func parseRuleAST(raw json.RawMessage) (rules.Rule, error) {
	return rules.Parse(raw)
}

// isJSONNull reports whether raw is absent or JSON null.
func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || strings.TrimSpace(string(raw)) == "null"
}

// normalizeCondition checks a condition and returns it in its canonical
// form; null stays nil. A refused condition is a 422 on its path.
func normalizeCondition(raw json.RawMessage) (json.RawMessage, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	cond, err := parseRuleCondition(raw)
	if err != nil {
		return nil, ruleFieldError(expressionPath("condition", err.Error()), err.Error())
	}
	out, err := json.Marshal(cond)
	if err != nil {
		return nil, fmt.Errorf("encode condition: %w", err)
	}
	return out, nil
}

// normalizeAST checks the value rule of a validation and returns it in its
// canonical form; null stays nil. A refused rule is a 422 on its path.
func normalizeAST(raw json.RawMessage) (json.RawMessage, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	rule, err := parseRuleAST(raw)
	if err != nil {
		return nil, ruleFieldError(expressionPath("ast", err.Error()), err.Error())
	}
	out, err := json.Marshal(rule)
	if err != nil {
		return nil, fmt.Errorf("encode ast: %w", err)
	}
	return out, nil
}

// parseRuleMessage checks the i18n message (MET-01): an object with de
// and/or en of 1 to 500 characters; null is no message.
func parseRuleMessage(raw json.RawMessage) (LocalizedText, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var texts map[string]any
	if err := json.Unmarshal(raw, &texts); err != nil || len(texts) == 0 {
		return nil, ruleFieldError("message", "message must be an object with the languages "+strings.Join(Languages, ", "))
	}
	out := LocalizedText{}
	for lang, v := range texts {
		text, ok := v.(string)
		switch {
		case !slices.Contains(Languages, lang):
			return nil, ruleFieldError("message", "message supports the languages "+strings.Join(Languages, ", "))
		case !ok || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 500:
			return nil, ruleFieldError("message."+lang, "message."+lang+" must have 1 to 500 characters")
		}
		out[lang] = text
	}
	return out, nil
}

// checkRuleKey checks a key or an attribute key (1 to 100 characters).
func checkRuleKey(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 100 {
		return "", ruleFieldError(field, field+" is required (at most 100 characters)")
	}
	return value, nil
}

// checkRuleShape checks kind and the ast of a rule: ast is required
// exactly for a validation.
func checkRuleShape(rule *MetamodelRule) error {
	if !slices.Contains(RuleKinds, rule.Kind) {
		return ruleFieldError("kind", "kind must be one of "+strings.Join(RuleKinds, ", "))
	}
	if rule.Kind == RuleKindValidation && rule.AST == nil {
		return ruleFieldError("ast", "a validation rule requires ast")
	}
	if rule.Kind != RuleKindValidation && rule.AST != nil {
		return ruleFieldError("ast", "only a validation rule has ast")
	}
	return nil
}

// newRule checks a create request and returns the rule it describes.
func newRule(in *RuleInput) (*MetamodelRule, error) {
	key, err := checkRuleKey("key", in.Key)
	if err != nil {
		return nil, err
	}
	attr, err := checkRuleKey("attribute_key", in.AttributeKey)
	if err != nil {
		return nil, err
	}
	rule := &MetamodelRule{Key: key, AttributeKey: attr, Kind: in.Kind, IsActive: in.IsActive == nil || *in.IsActive,
		CITypeID: strings.TrimSpace(in.CITypeID), AttributeSetID: strings.TrimSpace(in.AttributeSetID),
		TemplateVersionID: strings.TrimSpace(in.TemplateVersionID)}
	targets := 0
	for _, t := range []struct{ field, id string }{
		{"ci_type_id", rule.CITypeID}, {"attribute_set_id", rule.AttributeSetID}, {"template_version_id", rule.TemplateVersionID},
	} {
		if t.id == "" {
			continue
		}
		targets++
		if !api.IsUUID(t.id) {
			return nil, ruleFieldError(t.field, t.field+" must name a "+strings.TrimSuffix(strings.ReplaceAll(t.field, "_", " "), " id"))
		}
	}
	if targets != 1 {
		return nil, ruleFieldError("ci_type_id", "exactly one of ci_type_id, attribute_set_id and template_version_id is required")
	}
	if rule.Condition, err = normalizeCondition(in.Condition); err != nil {
		return nil, err
	}
	if rule.AST, err = normalizeAST(in.AST); err != nil {
		return nil, err
	}
	if rule.Message, err = parseRuleMessage(in.Message); err != nil {
		return nil, err
	}
	return rule, checkRuleShape(rule)
}

// apply changes rule; key and the target are immutable (422). A kind other
// than validation drops an ast the patch does not set.
func (p *RulePatch) apply(rule *MetamodelRule) error {
	for _, f := range []struct {
		field string
		value *string
	}{
		{"key", p.Key}, {"ci_type_id", p.CITypeID}, {"attribute_set_id", p.AttributeSetID},
		{"template_version_id", p.TemplateVersionID},
	} {
		if f.value != nil {
			return ruleFieldError(f.field, f.field+" is immutable")
		}
	}
	var err error
	if p.AttributeKey != nil {
		if rule.AttributeKey, err = checkRuleKey("attribute_key", *p.AttributeKey); err != nil {
			return err
		}
	}
	if p.Kind != nil {
		rule.Kind = *p.Kind
		if rule.Kind != RuleKindValidation && p.AST == nil {
			rule.AST = nil
		}
	}
	if p.Condition != nil {
		if rule.Condition, err = normalizeCondition(p.Condition); err != nil {
			return err
		}
	}
	if p.AST != nil {
		if rule.AST, err = normalizeAST(p.AST); err != nil {
			return err
		}
	}
	if p.Message != nil {
		if rule.Message, err = parseRuleMessage(p.Message); err != nil {
			return err
		}
	}
	setBool(&rule.IsActive, p.IsActive)
	return checkRuleShape(rule)
}

// ruleFields maps database constraints onto request fields.
var ruleFields = map[string]string{
	"metamodel_rule_org_key":                  "key",
	"metamodel_rule_system_key":               "key",
	"metamodel_rule_template_version_key":     "key",
	"metamodel_rule_key_check":                "key",
	"metamodel_rule_attribute_key_check":      "attribute_key",
	"metamodel_rule_kind_check":               "kind",
	"metamodel_rule_ast_check":                "ast",
	"metamodel_rule_message_i18n":             "message",
	"metamodel_rule_ci_type_id_fkey":          "ci_type_id",
	"metamodel_rule_attribute_set_id_fkey":    "attribute_set_id",
	"metamodel_rule_template_version_id_fkey": "template_version_id",
}

const ruleColumns = `id::text, COALESCE(organization_id::text, ''), key, attribute_key, kind,
	COALESCE(ci_type_id::text, ''), COALESCE(attribute_set_id::text, ''), COALESCE(template_version_id::text, ''),
	condition, ast, message, is_active, version, created_at, updated_at`

func scanRule(row pgx.Row) (*MetamodelRule, error) {
	var (
		r                       MetamodelRule
		condition, ast, message []byte
	)
	if err := row.Scan(&r.ID, &r.OrganizationID, &r.Key, &r.AttributeKey, &r.Kind, &r.CITypeID, &r.AttributeSetID,
		&r.TemplateVersionID, &condition, &ast, &message, &r.IsActive, &r.Version, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if len(condition) > 0 {
		r.Condition = json.RawMessage(condition)
	}
	if len(ast) > 0 {
		r.AST = json.RawMessage(ast)
	}
	if len(message) > 0 {
		if err := json.Unmarshal(message, &r.Message); err != nil {
			return nil, fmt.Errorf("decode message: %w", err)
		}
	}
	r.IsSystem = r.OrganizationID == ""
	return &r, nil
}

func collectRules(rows pgx.Rows) ([]MetamodelRule, error) {
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (MetamodelRule, error) {
		r, serr := scanRule(row)
		if serr != nil {
			return MetamodelRule{}, serr
		}
		return *r, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	if out == nil {
		out = []MetamodelRule{}
	}
	return out, nil
}

// getRuleTx reads a rule the transaction sees (the organization's and the
// system rules); lock locks an own rule for a change.
func getRuleTx(ctx context.Context, tx pgx.Tx, id string, lock bool) (*MetamodelRule, error) {
	if !api.IsUUID(id) {
		return nil, ErrRuleNotFound
	}
	query := `SELECT ` + ruleColumns + ` FROM metamodel_rule WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	r, err := scanRule(tx.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRuleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read rule: %w", err)
	}
	return r, nil
}

// lockOwnRuleTx locks a rule of the organization for a change; a system
// rule is visible but read-only (403), recognized before locking (the
// UPDATE policy hides it from FOR UPDATE).
func lockOwnRuleTx(ctx context.Context, tx pgx.Tx, id string) (*MetamodelRule, error) {
	r, err := getRuleTx(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if r.IsSystem {
		return nil, ErrRuleReadOnly
	}
	return getRuleTx(ctx, tx, id, true)
}

// checkRuleTargetTx checks the target of a rule under the caller's row
// level security: it must be visible, a row of the organization or a
// system row (otherwise 422 on the target field). The rules of a published
// or retired template version are immutable (409, E-111).
func checkRuleTargetTx(ctx context.Context, tx pgx.Tx, rule *MetamodelRule) error {
	switch {
	case rule.CITypeID != "":
		_, found, err := ciTypeOrganizationTx(ctx, tx, rule.CITypeID)
		if err != nil {
			return err
		}
		if !found {
			return ruleFieldError("ci_type_id", "ci type not found")
		}
	case rule.AttributeSetID != "":
		if _, err := getAttributeSetTx(ctx, tx, rule.AttributeSetID, false); errors.Is(err, ErrAttributeSetNotFound) {
			return ruleFieldError("attribute_set_id", "attribute set not found")
		} else if err != nil {
			return err
		}
	case rule.TemplateVersionID != "":
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM template_version WHERE id = $1 FOR SHARE`, rule.TemplateVersionID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			// A system version is visible but cannot be locked; its status
			// decides like that of an own version.
			err = tx.QueryRow(ctx, `SELECT status FROM template_version WHERE id = $1`, rule.TemplateVersionID).Scan(&status)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ruleFieldError("template_version_id", "template version not found")
		}
		if err != nil {
			return fmt.Errorf("read template version: %w", err)
		}
		if status != TemplateVersionDraft {
			return ErrTemplateVersionImmutable
		}
	}
	return nil
}

// checkVisibleRule refuses a visible rule on a CI type for an attribute the
// type requires statically (ResolveFields of the type without CI): a
// required field cannot be hidden (422 on attribute_key).
func (r *PGRepository) checkVisibleRule(ctx context.Context, orgID string, rule *MetamodelRule) error {
	if rule.Kind != RuleKindVisible || rule.CITypeID == "" {
		return nil
	}
	fields, err := r.ResolveFields(ctx, orgID, rule.CITypeID, "")
	if err != nil {
		var conflict *CompositionError
		if errors.As(err, &conflict) {
			return nil
		}
		return err
	}
	for i := range fields {
		if fields[i].Name == rule.AttributeKey && fields[i].Required {
			return ruleFieldError("attribute_key", "the attribute is required by the ci type and cannot be hidden")
		}
	}
	return nil
}

// jsonArg is a JSON column value; nil is SQL NULL.
func jsonArg(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	return []byte(raw)
}

// messageArg is the message column value; no message is SQL NULL.
func messageArg(m LocalizedText) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// ListRulePage implements RuleStore: the organization's and the system
// rules newest first (API-02), filtered by target, attribute key and kind.
func (r *PGRepository) ListRulePage(ctx context.Context, orgID string, filter *RuleFilter, page api.PaginationParams) ([]MetamodelRule, int, error) {
	out := []MetamodelRule{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		const where = `($1 = '' OR ci_type_id::text = $1) AND ($2 = '' OR attribute_set_id::text = $2)
			AND ($3 = '' OR template_version_id::text = $3) AND ($4 = '' OR attribute_key = $4) AND ($5 = '' OR kind = $5)`
		args := []any{filter.CITypeID, filter.AttributeSetID, filter.TemplateVersionID, filter.AttributeKey, filter.Kind}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM metamodel_rule WHERE `+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count rules: %w", err)
		}
		keyset, tail, pageArgs, err := api.NewestFirstSQL(page, len(args)+1)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+ruleColumns+` FROM metamodel_rule WHERE `+where+` AND `+keyset+` `+tail,
			append(args, pageArgs...)...)
		if err != nil {
			return fmt.Errorf("list rules: %w", err)
		}
		out, err = collectRules(rows)
		return err
	})
	return out, total, err
}

// GetRule implements RuleStore.
func (r *PGRepository) GetRule(ctx context.Context, orgID, id string) (*MetamodelRule, error) {
	var out *MetamodelRule
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = getRuleTx(ctx, tx, id, false)
		return err
	})
	return out, err
}

// CreateRule implements RuleStore: a rule of the organization on a target
// it sees, audited as metamodel_rule.created. A taken key is 409, a rule
// on a published or retired template version 409.
func (r *PGRepository) CreateRule(ctx context.Context, orgID string, in *RuleInput) (*MetamodelRule, error) {
	rule, err := newRule(in)
	if err != nil {
		return nil, err
	}
	if verr := r.checkVisibleRule(ctx, orgID, rule); verr != nil {
		return nil, verr
	}
	var out *MetamodelRule
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if terr := checkRuleTargetTx(ctx, tx, rule); terr != nil {
			return terr
		}
		created, ierr := scanRule(tx.QueryRow(ctx, `INSERT INTO metamodel_rule
				(organization_id, key, attribute_key, kind, condition, ast, message, is_active,
				 ci_type_id, attribute_set_id, template_version_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING `+ruleColumns,
			orgID, rule.Key, rule.AttributeKey, rule.Kind, jsonArg(rule.Condition), jsonArg(rule.AST), messageArg(rule.Message),
			rule.IsActive, nilIfEmpty(rule.CITypeID), nilIfEmpty(rule.AttributeSetID), nilIfEmpty(rule.TemplateVersionID)))
		if ierr != nil {
			return api.FieldDBError(ierr, ruleFields)
		}
		out = created
		return recordMetamodelAudit(ctx, tx, orgID, "metamodel_rule.created", "metamodel_rule", created.ID, nil, created)
	})
	return out, err
}

// UpdateRule implements RuleStore: attribute key, kind, condition, ast,
// message and is_active; version rises by one. A system rule is 403, a
// rule of a published or retired template version 409.
func (r *PGRepository) UpdateRule(ctx context.Context, orgID, id string, patch *RulePatch) (*MetamodelRule, error) {
	current, err := r.GetRule(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if current.IsSystem {
		return nil, ErrRuleReadOnly
	}
	candidate := *current
	if perr := patch.apply(&candidate); perr != nil {
		return nil, perr
	}
	if verr := r.checkVisibleRule(ctx, orgID, &candidate); verr != nil {
		return nil, verr
	}
	var out *MetamodelRule
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, lerr := lockOwnRuleTx(ctx, tx, id)
		if lerr != nil {
			return lerr
		}
		rule := *before
		if perr := patch.apply(&rule); perr != nil {
			return perr
		}
		if terr := checkRuleTargetTx(ctx, tx, before); terr != nil {
			return terr
		}
		updated, uerr := scanRule(tx.QueryRow(ctx, `UPDATE metamodel_rule
			SET attribute_key = $2, kind = $3, condition = $4, ast = $5, message = $6, is_active = $7, version = version + 1
			WHERE id = $1 RETURNING `+ruleColumns,
			id, rule.AttributeKey, rule.Kind, jsonArg(rule.Condition), jsonArg(rule.AST), messageArg(rule.Message), rule.IsActive))
		if uerr != nil {
			return api.FieldDBError(uerr, ruleFields)
		}
		out = updated
		return recordMetamodelAudit(ctx, tx, orgID, "metamodel_rule.updated", "metamodel_rule", id, before, updated)
	})
	return out, err
}

// DeleteRule implements RuleStore, audited as metamodel_rule.deleted. A
// system rule is 403, a rule of a published or retired template version
// 409.
func (r *PGRepository) DeleteRule(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, err := lockOwnRuleTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if terr := checkRuleTargetTx(ctx, tx, before); terr != nil {
			return terr
		}
		if _, err = tx.Exec(ctx, `DELETE FROM metamodel_rule WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete rule: %w", err)
		}
		return recordMetamodelAudit(ctx, tx, orgID, "metamodel_rule.deleted", "metamodel_rule", id, before, nil)
	})
}

// ActiveRules implements RuleStore (WP-257): the active rules of the given
// targets, ordered by key and id.
func (r *PGRepository) ActiveRules(ctx context.Context, orgID string, targets RuleTargets) ([]MetamodelRule, error) {
	out := []MetamodelRule{}
	ids := func(list []string) []string {
		valid := make([]string, 0, len(list))
		for _, id := range list {
			if api.IsUUID(id) {
				valid = append(valid, id)
			}
		}
		return valid
	}
	types, sets, versions := ids(targets.CITypeIDs), ids(targets.AttributeSetIDs), ids(targets.TemplateVersionIDs)
	if len(types)+len(sets)+len(versions) == 0 {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+ruleColumns+` FROM metamodel_rule
			WHERE is_active AND (ci_type_id = ANY($1::uuid[]) OR attribute_set_id = ANY($2::uuid[])
				OR template_version_id = ANY($3::uuid[]))
			ORDER BY key, id`, types, sets, versions)
		if err != nil {
			return fmt.Errorf("read active rules: %w", err)
		}
		out, err = collectRules(rows)
		return err
	})
	return out, err
}
