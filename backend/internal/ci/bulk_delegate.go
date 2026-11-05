package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// The delegating actions of POST /cis/bulk (BLK-02, E-132; WP-780): edges,
// lifecycle transitions, template changes and the export of a selection.
// Their logic belongs to other packages; the bulk reaches it only through
// the ports of this file (BulkRelationships, BulkLifecycle, BulkExporter,
// BulkEntitlements), which the server wires with adapters
// (server/report_adapters.go): export imports ci, so ci never imports
// export. change_template stays in this package (WP-253, WP-710). Every
// action runs per id like the field operations (report, job ci.bulk from
// 100 ids on), except export: it creates exactly one export job for all
// ids and answers 202 with it.

// The delegating actions of POST /cis/bulk.
const (
	BulkAddRelationship     = "add_relationship"
	BulkRemoveRelationship  = "remove_relationship"
	BulkLifecycleTransition = "lifecycle_transition"
	BulkChangeTemplate      = "change_template"
	BulkExport              = "export"
	// BulkExportJobKind is the kind of the job the action export answers
	// with: an export job (POST /api/v1/exports), not a job ci.bulk.
	BulkExportJobKind = "export"
)

// Directions of add_relationship and remove_relationship.
const (
	bulkOutgoing = "outgoing"
	bulkIncoming = "incoming"
)

// ErrBulkActionUnavailable: the port of a delegating action is not wired
// (a profile without database or without blob storage): 503 for the whole
// request.
var ErrBulkActionUnavailable = errors.New("the bulk action is not available in this deployment")

// BulkEdge is the edge of add_relationship or remove_relationship for one
// CI: CI → Other, with Incoming Other → CI.
type BulkEdge struct {
	CIID     string
	OtherID  string
	RelType  string
	Incoming bool
	// Actor is the author of the run (the caller, for a job its creator).
	Actor string
}

// BulkRelationships creates and removes manual edges (relationship
// package): CheckRelType refuses a type a manual edge cannot have (a
// projected type, one the catalogue lacks); AddRelationship creates the
// edge like POST /relationships, RemoveRelationship deletes it like DELETE
// /relationships/{id} without force. Their errors name the outcome for the
// id: ErrNotFound for the CI itself, *BulkItemError otherwise.
type BulkRelationships interface {
	CheckRelType(ctx context.Context, orgID, relType string) error
	AddRelationship(ctx context.Context, orgID string, edge *BulkEdge) error
	RemoveRelationship(ctx context.Context, orgID string, edge *BulkEdge) error
}

// BulkTransition is the lifecycle transition of lifecycle_transition.
type BulkTransition struct {
	ToState   string
	FromState *string
	Context   map[string]any
	Reason    string
}

// BulkLifecycle runs the transactional lifecycle transition of one CI
// (lifecycle.Service.Transition with its effective model, WP-724); errors
// as in BulkRelationships.
type BulkLifecycle interface {
	Transition(ctx context.Context, orgID, ciID string, req *BulkTransition) error
}

// BulkExportJob is the export job of the action export.
type BulkExportJob struct {
	ID     string
	Status string
}

// BulkExporter creates one export job of the CIs ids in format like POST
// /api/v1/exports (filters.ids); ErrBulkActionUnavailable without blob
// storage.
type BulkExporter interface {
	CreateExport(ctx context.Context, orgID, format string, ids []string) (*BulkExportJob, error)
}

// BulkEntitlements reports whether the organization is entitled to a
// feature; entitlement.(*Service).IsEnabled implements it.
type BulkEntitlements interface {
	IsEnabled(ctx context.Context, orgID, featureKey string) bool
}

// BulkDelegates are the ports of the delegating actions; a nil port makes
// its action answer 503.
type BulkDelegates struct {
	Relationships BulkRelationships
	Lifecycle     BulkLifecycle
	Exporter      BulkExporter
	Entitlements  BulkEntitlements
}

// WithBulkDelegates wires the delegating actions of POST /cis/bulk.
func (s *Service) WithBulkDelegates(d BulkDelegates) *Service {
	s.delegates = d
	return s
}

// BulkItemError is the outcome of one id that a port reports: the status
// the single operation would answer, its detail, its problem type (empty:
// https://reticora.io/problems/<status>) and the fields it names.
type BulkItemError struct {
	Status  int
	Detail  string
	Problem string
	Fields  []string
}

// validation: the detail the port names.
func (e *BulkItemError) Error() string { return e.Detail }

// ProblemType implements api.TypedProblem.
func (e *BulkItemError) ProblemType() string { return e.Problem }

// featureNotEntitledError refuses the export of an organization without
// the feature export_csv (403 feature-not-entitled).
type featureNotEntitledError struct{ feature string }

// validation: names the feature key.
func (e *featureNotEntitledError) Error() string {
	return "the organization is not entitled to the feature " + e.feature
}

// ProblemType implements api.TypedProblem.
func (e *featureNotEntitledError) ProblemType() string { return api.ProblemFeatureNotEntitled }

// delegateParamNames are the parameters of the delegating actions.
var delegateParamNames = map[string][]string{
	BulkAddRelationship:     {"rel_type", "target_ci_id", "direction"},
	BulkRemoveRelationship:  {"rel_type", "target_ci_id", "direction"},
	BulkLifecycleTransition: {"to_state", "from_state", "context", "reason"},
	BulkChangeTemplate:      {"template_id", "to_version", "replace_template_id"},
	BulkExport:              {"format"},
}

// delegateParams are the decoded parameters of a delegating action.
type delegateParams struct {
	relType, targetCIID string
	incoming            bool
	transition          BulkTransition
	templateID          string
	toVersion           *int
	replaceTemplateID   string
	format              string
	// templateTypeID and version are resolved before the run
	// (prepareDelegate): the CI type of the template and the target
	// version (to_version or the template's current version).
	templateTypeID string
	version        int
}

// requiredString decodes the required string parameter name.
func requiredString(members map[string]json.RawMessage, name string) (string, error) {
	raw, ok := members[name]
	if !ok {
		return "", paramsError(name, "is required")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", paramsError(name, "must be a string")
	}
	if value = strings.TrimSpace(value); value == "" {
		return "", paramsError(name, "is required")
	}
	return value, nil
}

// requiredID decodes the required UUID parameter name.
func requiredID(members map[string]json.RawMessage, name string) (string, error) {
	value, err := requiredString(members, name)
	if err != nil {
		return "", err
	}
	if !api.IsUUID(value) {
		return "", paramsError(name, "must be a UUID")
	}
	return strings.ToLower(value), nil
}

// isNull reports whether a parameter is absent or null.
func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// validateDelegateParams decodes and checks the parameters of a delegating
// action (422 on params.<name>).
func validateDelegateParams(action string, members map[string]json.RawMessage) (*delegateParams, error) {
	d := &delegateParams{}
	var err error
	switch action {
	case BulkAddRelationship, BulkRemoveRelationship:
		if d.relType, err = requiredString(members, "rel_type"); err != nil {
			return nil, err
		}
		if d.targetCIID, err = requiredID(members, "target_ci_id"); err != nil {
			return nil, err
		}
		direction := bulkOutgoing
		if raw := members["direction"]; !isNull(raw) {
			if json.Unmarshal(raw, &direction) != nil || (direction != bulkOutgoing && direction != bulkIncoming) {
				return nil, paramsError("direction", "must be outgoing or incoming")
			}
		}
		d.incoming = direction == bulkIncoming
	case BulkLifecycleTransition:
		if d.transition.ToState, err = requiredString(members, "to_state"); err != nil {
			return nil, err
		}
		if raw := members["from_state"]; !isNull(raw) {
			var from string
			if json.Unmarshal(raw, &from) != nil {
				return nil, paramsError("from_state", "must be a string")
			}
			d.transition.FromState = &from
		}
		if raw := members["context"]; !isNull(raw) {
			if json.Unmarshal(raw, &d.transition.Context) != nil {
				return nil, paramsError("context", "must be an object")
			}
		}
		if raw := members["reason"]; !isNull(raw) {
			if json.Unmarshal(raw, &d.transition.Reason) != nil {
				return nil, paramsError("reason", "must be a string")
			}
		}
	case BulkChangeTemplate:
		return d, validateTemplateParams(members, d)
	case BulkExport:
		format, formatErr := requiredString(members, "format")
		switch {
		case formatErr != nil:
			return nil, formatErr
		case format == "datev":
			return nil, paramsError("format", "datev exports assets; use POST /api/v1/exports")
		case format != "csv" && format != "json":
			return nil, paramsError("format", "must be csv or json")
		}
		d.format = format
	}
	return d, nil
}

// validateTemplateParams decodes the parameters of change_template.
func validateTemplateParams(members map[string]json.RawMessage, d *delegateParams) error {
	var err error
	if d.templateID, err = requiredID(members, "template_id"); err != nil {
		return err
	}
	if raw := members["to_version"]; !isNull(raw) {
		var version int
		if json.Unmarshal(raw, &version) != nil || version < 1 {
			return paramsError("to_version", "must be a version (1 or more)")
		}
		d.toVersion = &version
	}
	if raw := members["replace_template_id"]; !isNull(raw) {
		var replace string
		if json.Unmarshal(raw, &replace) != nil || !api.IsUUID(strings.TrimSpace(replace)) {
			return paramsError("replace_template_id", "must be a UUID")
		}
		if d.replaceTemplateID = strings.ToLower(strings.TrimSpace(replace)); d.replaceTemplateID == d.templateID {
			return paramsError("replace_template_id", "must differ from template_id")
		}
	}
	return nil
}

// isDelegateAction reports whether action is a delegating action.
func isDelegateAction(action string) bool {
	_, ok := delegateParamNames[action]
	return ok
}

// prepareDelegate checks a delegating action for the whole request before
// any change and before a job: its port (503 without), the entitlement
// export_csv of export (403 feature-not-entitled), the rel_type of the
// edge actions (422 on params.rel_type), the template and version of
// change_template (422 on params.template_id, params.to_version,
// params.replace_template_id). It resolves the target version of
// change_template; a job decodes and prepares again (RunBulkJob).
func (s *Service) prepareDelegate(ctx context.Context, orgID string, req *BulkRequest) error {
	if !isDelegateAction(req.Action) || req.params == nil || req.params.delegate == nil {
		return nil
	}
	d := req.params.delegate
	switch req.Action {
	case BulkAddRelationship, BulkRemoveRelationship:
		if s.delegates.Relationships == nil {
			return ErrBulkActionUnavailable
		}
		if err := s.delegates.Relationships.CheckRelType(ctx, orgID, d.relType); err != nil {
			if api.IsUnavailable(err) {
				return err
			}
			return paramsError("rel_type", "must be a relationship type of the catalogue that an edge stores, not a projected one")
		}
	case BulkLifecycleTransition:
		if s.delegates.Lifecycle == nil {
			return ErrBulkActionUnavailable
		}
	case BulkChangeTemplate:
		return s.prepareTemplate(ctx, orgID, d)
	case BulkExport:
		if s.delegates.Exporter == nil || s.delegates.Entitlements == nil {
			return ErrBulkActionUnavailable
		}
		if !s.delegates.Entitlements.IsEnabled(ctx, orgID, entitlement.FeatureExportCSV) {
			return &featureNotEntitledError{feature: entitlement.FeatureExportCSV}
		}
		if len(req.malformed) > 0 {
			return &api.FieldError{Field: "ids", Message: "an export takes CI ids (UUIDs) only"}
		}
	}
	return nil
}

// templatePorts are the parts change_template needs: the template store
// and the field resolver of the metamodel (citype.PGRepository) and the
// template migration of the repository.
func (s *Service) templatePorts() (TemplateVersions, TemplateFieldResolver, TemplateMigrator, bool) {
	versions, okVersions := s.fields.(TemplateVersions)
	resolver, okResolver := s.fields.(TemplateFieldResolver)
	migrator, okMigrator := s.repo.(TemplateMigrator)
	return versions, resolver, migrator, okVersions && okResolver && okMigrator
}

// prepareTemplate checks the templates of change_template and resolves
// the target version: an unknown template or replace_template_id, a
// to_version that is no published version, or a template without one is
// 422 for the whole request.
func (s *Service) prepareTemplate(ctx context.Context, orgID string, d *delegateParams) error {
	versions, _, _, ok := s.templatePorts()
	if !ok {
		return ErrBulkActionUnavailable
	}
	tmpl, err := versions.GetTemplate(ctx, orgID, d.templateID)
	if errors.Is(err, citype.ErrTemplateNotFound) {
		return paramsError("template_id", "unknown template")
	}
	if err != nil {
		return err
	}
	d.templateTypeID = tmpl.CITypeID
	if d.replaceTemplateID != "" {
		if _, err = versions.GetTemplate(ctx, orgID, d.replaceTemplateID); errors.Is(err, citype.ErrTemplateNotFound) {
			return paramsError("replace_template_id", "unknown template")
		} else if err != nil {
			return err
		}
	}
	if d.toVersion == nil {
		if tmpl.CurrentVersion == nil {
			return paramsError("to_version", "the template has no published version")
		}
		d.version = *tmpl.CurrentVersion
		return nil
	}
	v, err := versions.GetTemplateVersion(ctx, orgID, d.templateID, *d.toVersion)
	switch {
	case errors.Is(err, citype.ErrTemplateVersionNotFound):
		return paramsError("to_version", "the template has no such version")
	case err != nil:
		return err
	case v.Status != citype.TemplateVersionPublished:
		return paramsError("to_version", "is no published version of the template")
	}
	d.version = *d.toVersion
	return nil
}

// bulkExport creates the one export job of the action export (WP-780).
func (s *Service) bulkExport(ctx context.Context, orgID string, req *BulkRequest) (*jobs.Job, error) {
	exp, err := s.delegates.Exporter.CreateExport(ctx, orgID, req.params.delegate.format, req.IDs)
	if err != nil {
		return nil, err
	}
	return &jobs.Job{ID: exp.ID, OrganizationID: orgID, Kind: BulkExportJobKind, Status: jobs.Status(exp.Status),
		RefTable: "export_job", RefID: exp.ID}, nil
}

// BulkJobLocation is the Location of the 202 of POST /cis/bulk: the
// export job of the action export, otherwise the job ci.bulk.
func BulkJobLocation(job *jobs.Job) string {
	if job.Kind == BulkExportJobKind {
		return "/api/v1/exports/" + job.ID
	}
	return "/api/v1/jobs/" + job.ID
}

// bulkDelegate applies a delegating action to one id and reports it.
func (s *Service) bulkDelegate(ctx context.Context, op *bulkOp, id string, d *delegateParams) BulkItemResult {
	switch op.req.Action {
	case BulkAddRelationship, BulkRemoveRelationship:
		return s.bulkRelationship(ctx, op, id, d)
	case BulkLifecycleTransition:
		return s.bulkTransition(ctx, op, id, d)
	case BulkChangeTemplate:
		return s.bulkChangeTemplate(ctx, op, id, d)
	}
	return bulkFailed(id, &BulkItemError{Status: http.StatusUnprocessableEntity, Detail: "the action does not run per id"})
}

// bulkRelationship creates or removes the edge of one CI. A CI the caller
// cannot see is 404.
func (s *Service) bulkRelationship(ctx context.Context, op *bulkOp, id string, d *delegateParams) BulkItemResult {
	if s.delegates.Relationships == nil {
		return bulkFailed(id, ErrBulkActionUnavailable)
	}
	if _, err := s.repo.GetByID(ctx, op.orgID, id); err != nil {
		return bulkFailed(id, err)
	}
	edge := &BulkEdge{CIID: id, OtherID: d.targetCIID, RelType: d.relType, Incoming: d.incoming, Actor: op.author}
	var err error
	if op.req.Action == BulkAddRelationship {
		err = s.delegates.Relationships.AddRelationship(ctx, op.orgID, edge)
	} else {
		err = s.delegates.Relationships.RemoveRelationship(ctx, op.orgID, edge)
	}
	if err != nil {
		return bulkFailed(id, err)
	}
	return bulkOK(id)
}

// bulkTransition runs the lifecycle transition of one CI after the action
// grants of lifecycle:transition on its type and scope (WP-728, like the
// single transition). A CI the caller cannot see is 404.
func (s *Service) bulkTransition(ctx context.Context, op *bulkOp, id string, d *delegateParams) BulkItemResult {
	if s.delegates.Lifecycle == nil {
		return bulkFailed(id, ErrBulkActionUnavailable)
	}
	item, err := s.repo.GetByID(ctx, op.orgID, id)
	if err != nil {
		return bulkFailed(id, err)
	}
	if err = permission.CheckActionOn(ctx, permission.KindCI, identity.PermLifecycleTransition,
		func(context.Context) ([]permission.ActionTarget, error) {
			return []permission.ActionTarget{ciTarget(item)}, nil
		}); err != nil {
		return bulkFailed(id, err)
	}
	transition := d.transition
	if err = s.delegates.Lifecycle.Transition(ctx, op.orgID, id, &transition); err != nil {
		return bulkFailed(id, err)
	}
	return bulkOK(id)
}

// typeChains reads the type chain of a CI type (citype.PGRepository).
type typeChains interface {
	TypeChain(ctx context.Context, orgID, id string) ([]citype.Type, error)
}

// errTemplateOtherType is the detail of a template outside the type chain
// of a CI: the CI changes its type first (CI-11).
const errTemplateOtherType = "the template belongs to a CI type outside the type chain of the CI; " +
	"change the type of the CI first with POST /api/v1/cis/{id}/type"

// templateInChain reports whether the template's CI type is in the type
// chain of the CI type; without a chain reader the write path decides.
func (s *Service) templateInChain(ctx context.Context, orgID, ciTypeID, templateTypeID string) (bool, error) {
	chains, ok := s.fields.(typeChains)
	if !ok {
		return true, nil
	}
	chain, err := chains.TypeChain(ctx, orgID, ciTypeID)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(chain, func(t citype.Type) bool { return strings.EqualFold(t.ID, templateTypeID) }), nil
}

// bulkChangeTemplate changes the template of one CI (E-132): (a) a CI that
// holds template_id in another version migrates to the target version
// (TemplateMigrator, WP-253: defaults only into fields without a value,
// overrides stay, findings do not block); on the target version already it
// is ok without a change. (b) With replace_template_id a CI that holds
// that template takes template_id in its place through the write path of
// PUT /cis/{id}/templates (WP-710). (c) A CI with neither template, or a
// template of a type outside its type chain, is 422.
func (s *Service) bulkChangeTemplate(ctx context.Context, op *bulkOp, id string, d *delegateParams) BulkItemResult {
	_, resolver, migrator, ok := s.templatePorts()
	if !ok {
		return bulkFailed(id, ErrBulkActionUnavailable)
	}
	item, err := s.readForTags(ctx, op.orgID, id)
	if err != nil {
		return bulkFailed(id, err)
	}
	holds := func(templateID string) int {
		return slices.IndexFunc(item.Templates, func(t citype.CITemplate) bool { return strings.EqualFold(t.TemplateID, templateID) })
	}
	at, replaceAt := holds(d.templateID), -1
	if d.replaceTemplateID != "" {
		replaceAt = holds(d.replaceTemplateID)
	}
	if at < 0 || replaceAt >= 0 {
		inChain, chainErr := s.templateInChain(ctx, op.orgID, item.CITypeID, d.templateTypeID)
		switch {
		case chainErr != nil:
			return bulkFailed(id, chainErr)
		case !inChain:
			return bulkFailed(id, templateFieldError(errTemplateOtherType))
		}
	}
	switch {
	case replaceAt >= 0 && at >= 0:
		return bulkFailed(id, templateFieldError("the CI holds the template already; it cannot replace another one"))
	case replaceAt >= 0:
		list := slices.Clone(item.Templates)
		list[replaceAt] = citype.CITemplate{TemplateID: d.templateID, Version: d.version, Position: replaceAt}
		if _, err = s.Update(ctx, op.orgID, id, op.manual(UpdateRequest{Templates: &list})); err != nil {
			return bulkFailed(id, err)
		}
		return bulkOK(id)
	case at < 0:
		return bulkFailed(id, templateFieldError("the CI holds neither the template nor replace_template_id"))
	case item.Templates[at].Version == d.version:
		return bulkOK(id)
	}
	result, err := migrator.MigrateTemplate(ctx, op.orgID, id, TemplateMigrationPlan{TemplateID: d.templateID,
		FromVersion: item.Templates[at].Version, ToVersion: d.version, JobID: op.jobID, Fields: resolver}, false)
	if err != nil {
		return bulkFailed(id, err)
	}
	report := bulkOK(id)
	if len(result.Findings) > 0 {
		report.Findings = result.Findings
	}
	return report
}

// templateFieldError is a 422 on template_id for one id.
func templateFieldError(detail string) error {
	return &BulkItemError{Status: http.StatusUnprocessableEntity, Detail: detail, Fields: []string{"template_id"}}
}

// bulkItemFailure maps a *BulkItemError onto the report of its id.
func bulkItemFailure(e *BulkItemError) bulkFailure {
	status := e.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	return bulkFailure{code: status, msg: e.Detail, fields: e.Fields}
}

// unavailableFailure is the report of an id whose port is missing.
func unavailableFailure() bulkFailure {
	return bulkFailure{code: http.StatusServiceUnavailable, msg: ErrBulkActionUnavailable.Error()}
}

// IsFeatureNotEntitled reports whether err refuses a bulk action for a
// feature the organization is not entitled to (403 feature-not-entitled).
func IsFeatureNotEntitled(err error) bool {
	var refused *featureNotEntitledError
	return errors.As(err, &refused)
}
