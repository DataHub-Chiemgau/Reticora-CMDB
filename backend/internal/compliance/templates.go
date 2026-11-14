package compliance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// The shipped compliance rule templates (SEC-03, WP-1130, E-238 option 1,
// E-240 option 2): a catalog in the code whose entries an organization
// instantiates as an inactive rule with its controls (WP-1128). Only the
// templates of the confirmed list ship (decision of 2026-11-09, point 6:
// the recommended list): firmware version maintained, no open critical
// finding, no open unauthorized change, management IP set on network
// devices, no CI with lifecycle state eol and status active.

// TemplateControl names a control of the catalog (compliance_control) by
// standard and control id.
type TemplateControl struct {
	Standard  string `json:"standard"`
	ControlID string `json:"control_id"`
}

// Template is one rule template.
type Template struct {
	Key     string
	Version int
	// Name and Description per language (de, en).
	Name, Description map[string]string
	Severity          string
	Category          string
	// CITypeKey is the key of the CI type the rule applies to (empty: every
	// type); instantiate resolves it in the organization.
	CITypeKey string
	// CITypeKeys are the keys of several CI types the rule applies to
	// (network devices): instantiate resolves those the organization has
	// and adds applies_when ci_type_id in their ids; none of them is
	// ErrTemplateCIType.
	CITypeKeys  []string
	Expression  JSONMap
	AppliesWhen JSONMap
	// Remediation per language.
	Remediation map[string]string
	Controls    []TemplateControl
}

// networkDeviceTypes are the system CI types of network devices with a
// management IP (category network without the passive patch_panel).
var networkDeviceTypes = []string{"switch", "router", "firewall", "access_point"}

// Templates is the catalog of the shipped templates.
var Templates = []Template{
	{
		Key: "firmware_maintained", Version: 1, Severity: "medium", Category: "patch_management",
		Name: map[string]string{"de": "Firmware-Version gepflegt", "en": "Firmware version maintained"},
		Description: map[string]string{
			"de": "Die Firmware-Version des CIs ist erfasst, damit Schwachstellen und Updates zugeordnet werden können.",
			"en": "The firmware version of the CI is recorded so vulnerabilities and updates can be matched.",
		},
		Remediation: map[string]string{
			"de": "Firmware-Version per Discovery erfassen oder im CI pflegen.",
			"en": "Record the firmware version through discovery or on the CI.",
		},
		Expression: JSONMap{"op": "not_empty", "field": "firmware_version"},
		Controls:   []TemplateControl{{"iso27001", "5.9"}, {"iso27001", "8.8"}, {"nis2", "21.2.e"}},
	},
	{
		Key: "no_open_critical_finding", Version: 1, Severity: "critical", Category: "vulnerability",
		Name: map[string]string{"de": "Kein offenes kritisches Finding", "en": "No open critical finding"},
		Description: map[string]string{
			"de": "Das CI hat kein offenes oder bestätigtes Finding der Schwere critical.",
			"en": "The CI has no open or acknowledged finding of severity critical.",
		},
		Remediation: map[string]string{
			"de": "Kritische Findings beheben oder mit Begründung als akzeptiert schließen.",
			"en": "Remediate the critical findings or close them as accepted with a reason.",
		},
		Expression: JSONMap{"op": "lt", "field": InputMaxOpenSeverityRank, "value": 4},
		Controls:   []TemplateControl{{"iso27001", "8.8"}, {"nis2", "21.2.e"}, {"kritis", "8a.1"}},
	},
	{
		Key: "no_unauthorized_change", Version: 1, Severity: "high", Category: "change_management",
		Name: map[string]string{"de": "Keine offene unautorisierte Änderung", "en": "No open unauthorized change"},
		Description: map[string]string{
			"de": "Am CI ist keine Konfigurationsänderung außerhalb eines Wartungsfensters offen.",
			"en": "The CI has no open configuration change outside a maintenance window.",
		},
		Remediation: map[string]string{
			"de": "Die Änderung prüfen und das Finding bestätigen oder schließen.",
			"en": "Review the change and acknowledge or close the finding.",
		},
		Expression: JSONMap{"op": "eq", "field": InputOpenCountPrefix + "unauthorized_change", "value": 0},
		Controls:   []TemplateControl{{"iso27001", "8.9"}, {"iso27001", "8.32"}, {"nis2", "21.2.b"}, {"kritis", "8a.1a"}},
	},
	{
		Key: "management_ip_set", Version: 1, Severity: "low", Category: "inventory",
		CITypeKeys: networkDeviceTypes,
		Name:       map[string]string{"de": "Management-IP bei Netzwerkgeräten gesetzt", "en": "Management IP set on network devices"},
		Description: map[string]string{
			"de": "Ein Netzwerkgerät hat eine Management-IP, damit Monitoring und Discovery es erreichen.",
			"en": "A network device has a management IP so monitoring and discovery reach it.",
		},
		Remediation: map[string]string{
			"de": "Management-IP im CI eintragen oder per Discovery übernehmen.",
			"en": "Enter the management IP on the CI or take it over from discovery.",
		},
		Expression: JSONMap{"op": "not_empty", "field": "management_ip"},
		Controls:   []TemplateControl{{"iso27001", "5.9"}, {"iso27001", "8.9"}, {"nis2", "21.2.i"}},
	},
	{
		Key: "no_active_eol", Version: 1, Severity: "high", Category: "lifecycle",
		Name: map[string]string{"de": "Kein aktives CI am Lebensende", "en": "No active CI at end of life"},
		Description: map[string]string{
			"de": "Ein CI im Lebenszyklus-Zustand eol ist nicht mehr im Status active.",
			"en": "A CI in lifecycle state eol is no longer in status active.",
		},
		Remediation: map[string]string{
			"de": "Das CI ersetzen und außer Betrieb nehmen oder den Lebenszyklus korrigieren.",
			"en": "Replace and retire the CI or correct its lifecycle state.",
		},
		AppliesWhen: JSONMap{"op": "eq", "field": "lifecycle_state", "value": "eol"},
		Expression:  JSONMap{"op": "ne", "field": "status", "value": "active"},
		Controls:    []TemplateControl{{"iso27001", "5.9"}, {"iso27001", "8.8"}, {"nis2", "21.2.e"}, {"kritis", "8a.1"}},
	},
}

// templateLanguages are the languages of the catalog; the first is the
// fallback.
var templateLanguages = []string{"de", "en"}

// TemplateByKey returns the template of key.
func TemplateByKey(key string) (*Template, bool) {
	for i := range Templates {
		if Templates[i].Key == key {
			return &Templates[i], true
		}
	}
	return nil, false
}

// RequestLanguage is the language of the request by Accept-Language: the
// first of de and en by weight, de without one.
func RequestLanguage(r *http.Request) string {
	type tag struct {
		lang string
		q    float64
		pos  int
	}
	var tags []tag
	for i, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		lang := strings.ToLower(strings.TrimSpace(fields[0]))
		if base, _, ok := strings.Cut(lang, "-"); ok {
			lang = base
		}
		q := 1.0
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(strings.TrimSpace(f), "q="); ok {
				if parsed, err := strconv.ParseFloat(v, 64); err == nil {
					q = parsed
				}
			}
		}
		tags = append(tags, tag{lang, q, i})
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].q > tags[j].q })
	for _, t := range tags {
		for _, l := range templateLanguages {
			if t.lang == l && t.q > 0 {
				return l
			}
		}
	}
	return templateLanguages[0]
}

// TemplateView is a template in one language.
type TemplateView struct {
	Key         string            `json:"key"`
	Version     int               `json:"version"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Severity    string            `json:"severity"`
	Category    string            `json:"category"`
	CITypeKey   string            `json:"ci_type_key,omitempty"`
	CITypeKeys  []string          `json:"ci_type_keys,omitempty"`
	Expression  JSONMap           `json:"expression"`
	AppliesWhen JSONMap           `json:"applies_when,omitempty"`
	Remediation string            `json:"remediation_hint"`
	Controls    []TemplateControl `json:"controls"`
}

func text(m map[string]string, lang string) string {
	if v := m[lang]; v != "" {
		return v
	}
	return m[templateLanguages[0]]
}

func (t *Template) view(lang string) TemplateView {
	return TemplateView{Key: t.Key, Version: t.Version, Name: text(t.Name, lang), Description: text(t.Description, lang),
		Severity: t.Severity, Category: t.Category, CITypeKey: t.CITypeKey, CITypeKeys: t.CITypeKeys, Expression: t.Expression,
		AppliesWhen: t.AppliesWhen, Remediation: text(t.Remediation, lang), Controls: t.Controls}
}

// TemplateStore instantiates templates (the PostgreSQL repository).
type TemplateStore interface {
	// InstantiateTemplate creates the inactive rule of the template in the
	// language lang with its controls, in one transaction: ErrTemplateCIType
	// for a CI type the organization does not have, ErrRuleNameTaken for a
	// name in use.
	InstantiateTemplate(ctx context.Context, orgID string, t *Template, lang string) (*Rule, error)
}

var (
	// ErrTemplateCIType: the CI type of the template is no type of the
	// organization (422 on ci_type).
	ErrTemplateCIType = errors.New("the CI type of the template does not exist in the organization")
	// ErrRuleNameTaken: a rule of the name exists (409).
	ErrRuleNameTaken = errors.New("a compliance rule of this name exists")
	// ErrTemplatesUnavailable: the memory profile has no control catalog.
	ErrTemplatesUnavailable = errors.New("instantiating a rule template needs PostgreSQL")
)

// templateMarker is the line instantiate appends to the description: the
// template and its version stay with the rule and in its audit row.
func templateMarker(t *Template) string {
	return fmt.Sprintf("[template %s v%d]", t.Key, t.Version)
}

// InstantiateTemplate implements TemplateStore.
func (r *PGRepository) InstantiateTemplate(ctx context.Context, orgID string, t *Template, lang string) (*Rule, error) {
	rule := &Rule{OrganizationID: orgID, Name: text(t.Name, lang),
		Description: text(t.Description, lang) + "\n\n" + templateMarker(t), Severity: t.Severity, Category: t.Category,
		Expression: t.Expression, AppliesWhen: t.AppliesWhen, RemediationHint: text(t.Remediation, lang), Active: false}
	// One transaction: the lookups, the rule and its controls join it.
	err := database.WithRequestTenantTx(ctx, r.pool, orgID, func(ctx context.Context) error {
		var controls []string
		lerr := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			return resolveTemplate(ctx, tx, orgID, t, rule, &controls)
		})
		if lerr != nil {
			return lerr
		}
		if err := r.CreateRule(ctx, rule); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrRuleNameTaken
			}
			return err
		}
		return r.ReplaceRuleControls(ctx, orgID, rule.ID, controls)
	})
	if err != nil {
		return nil, err
	}
	return rule, nil
}

// resolveTemplate sets the CI type of the rule from the key of the template
// and resolves its controls to the ids of the catalog.
func resolveTemplate(ctx context.Context, tx pgx.Tx, orgID string, t *Template, rule *Rule, controls *[]string) error {
	if t.CITypeKey != "" {
		// An own type of the organization before the system type of the key.
		err := tx.QueryRow(ctx, `SELECT id::text FROM ci_type WHERE key = $1 AND (organization_id = $2 OR organization_id IS NULL)
			ORDER BY organization_id NULLS LAST LIMIT 1`, t.CITypeKey, orgID).Scan(&rule.CITypeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTemplateCIType
		}
		if err != nil {
			return fmt.Errorf("resolve the CI type of the template: %w", err)
		}
	}
	if len(t.CITypeKeys) > 0 {
		cond, err := typeCondition(ctx, tx, orgID, t.CITypeKeys)
		if err != nil {
			return err
		}
		if rule.AppliesWhen == nil {
			rule.AppliesWhen = cond
		} else {
			rule.AppliesWhen = JSONMap{"op": "all", "rules": []any{map[string]any(rule.AppliesWhen), map[string]any(cond)}}
		}
	}
	for _, c := range t.Controls {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM compliance_control WHERE standard = $1 AND control_id = $2`,
			c.Standard, c.ControlID).Scan(&id); err != nil {
			return fmt.Errorf("resolve control %s %s of template %s: %w", c.Standard, c.ControlID, t.Key, err)
		}
		*controls = append(*controls, id)
	}
	return nil
}

// typeCondition is the condition "ci_type_id in" the ids of the CI types
// of keys in the organization (an own type before the system type of a
// key); keys the organization lacks are skipped, none at all is
// ErrTemplateCIType.
func typeCondition(ctx context.Context, tx pgx.Tx, orgID string, keys []string) (JSONMap, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (key) id::text FROM ci_type
		WHERE key = ANY($1) AND (organization_id = $2 OR organization_id IS NULL)
		ORDER BY key, organization_id NULLS LAST`, keys, orgID)
	if err != nil {
		return nil, fmt.Errorf("resolve the CI types of the template: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("resolve the CI types of the template: %w", err)
	}
	if len(ids) == 0 {
		return nil, ErrTemplateCIType
	}
	values := make([]any, len(ids))
	for i, id := range ids {
		values[i] = id
	}
	return JSONMap{"op": "in", "field": "ci_type_id", "value": values}, nil
}

// InstantiateTemplate implements TemplateStore: the memory profile has no
// control catalog.
func (r *MemoryRepository) InstantiateTemplate(context.Context, string, *Template, string) (*Rule, error) {
	return nil, ErrTemplatesUnavailable
}

// tplTypes names the CI type keys of a template for the 422.
func tplTypes(t *Template) string {
	if len(t.CITypeKeys) > 0 {
		return strings.Join(t.CITypeKeys, ", ")
	}
	return t.CITypeKey
}

// ListRuleTemplates handles GET /api/v1/compliance/rule-templates.
func (h *Handler) ListRuleTemplates(w http.ResponseWriter, r *http.Request) {
	if _, ok := org(w, r); !ok {
		return
	}
	lang := RequestLanguage(r)
	out := make([]TemplateView, 0, len(Templates))
	for i := range Templates {
		out = append(out, Templates[i].view(lang))
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

// InstantiateRuleTemplate handles POST
// /api/v1/compliance/rule-templates/{key}/instantiate: 201 with the
// inactive rule and its controls; an unknown key is 404, a CI type the
// organization lacks 422 on ci_type, a name in use 409.
func (h *Handler) InstantiateRuleTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	tpl, found := TemplateByKey(chi.URLParam(r, "key"))
	if !found {
		api.WriteError(w, http.StatusNotFound, "Not Found", "rule template not found")
		return
	}
	rule, err := h.repo.InstantiateTemplate(r.Context(), t.OrganizationID, tpl, RequestLanguage(r))
	switch {
	case errors.Is(err, ErrTemplateCIType):
		api.WriteFieldError(w, &api.FieldError{Field: "ci_type", Message: "the CI type " + tplTypes(tpl) + " does not exist",
			Status: http.StatusUnprocessableEntity})
		return
	case errors.Is(err, ErrRuleNameTaken):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrRuleNameTaken.Error())
		return
	case errors.Is(err, ErrTemplatesUnavailable):
		api.WriteUnavailable(w, ErrTemplatesUnavailable.Error())
		return
	case err != nil:
		writeComplianceError(w, r, err)
		return
	}
	ids, err := h.repo.RuleControlIDs(r.Context(), t.OrganizationID, rule.ID)
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, RuleWithControls{Rule: rule, ControlIDs: api.NonNilSlice(ids)})
}
