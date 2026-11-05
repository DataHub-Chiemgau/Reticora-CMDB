package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// The field operations of POST /cis/bulk (BLK-02, CI-08, OVR-03; WP-304).
// Every action that writes CI fields (set_status, set_fields, set_location,
// set_owner and the status of archive) writes each id through Service.Update
// as a manual change of the initiating user (author; for a job its creator)
// with If-Match "*" and the reason bulk (bulk:<job_id> in a job): the same
// path as PATCH /cis/{id}, with its field permissions, rules, computed
// attributes, version and overrides. The tags (add_tags, remove_tags and
// the tag of archive) go through the tag path of PUT /cis/{id}/tags with
// the version read as If-Match.

// The actions of POST /cis/bulk besides BulkSetStatus.
const (
	BulkSetFields   = "set_fields"
	BulkSetLocation = "set_location"
	BulkAddTags     = "add_tags"
	BulkRemoveTags  = "remove_tags"
	BulkSetOwner    = "set_owner"
	BulkArchive     = "archive"
	// ArchivedTag is the system tag archive sets (WP-281, migration
	// 000218).
	ArchivedTag = "archived"
)

// bulkActionPermissions is the right each action needs (WP-304). The route
// middleware checks ci:write for /cis/bulk; Service.Bulk checks every
// other right of the table before any change and before a job
// (checkBulkPermission). The field operations need ci:write alone; the
// delegating actions (WP-780, bulk_delegate.go) the right of their single
// operation: change_template ci:write like PUT /cis/{id}/templates.
var bulkActionPermissions = map[string]identity.Permission{
	BulkSetStatus:           identity.PermCIWrite,
	BulkSetFields:           identity.PermCIWrite,
	BulkSetLocation:         identity.PermCIWrite,
	BulkAddTags:             identity.PermCIWrite,
	BulkRemoveTags:          identity.PermCIWrite,
	BulkSetOwner:            identity.PermCIWrite,
	BulkArchive:             identity.PermCIWrite,
	BulkAddRelationship:     identity.PermRelationshipWrite,
	BulkRemoveRelationship:  identity.PermRelationshipWrite,
	BulkLifecycleTransition: identity.PermLifecycleTransition,
	BulkChangeTemplate:      identity.PermCIWrite,
	BulkExport:              identity.PermExportRun,
}

// BulkActions returns the actions of POST /cis/bulk, sorted.
func BulkActions() []string {
	out := make([]string, 0, len(bulkActionPermissions))
	for action := range bulkActionPermissions {
		out = append(out, action)
	}
	sort.Strings(out)
	return out
}

// BulkActionPermission returns the right an action needs; false for an
// unknown action.
func BulkActionPermission(action string) (identity.Permission, bool) {
	perm, ok := bulkActionPermissions[action]
	return perm, ok
}

// checkBulkPermission checks the right of the action beyond ci:write,
// which the route middleware checks: without a principal it counts as
// missing (fail-closed), a *api.FieldForbiddenError with the right (403
// for the whole request).
func checkBulkPermission(ctx context.Context, action string) error {
	perm, ok := bulkActionPermissions[action]
	if !ok || perm == identity.PermCIWrite {
		return nil
	}
	if p, found := identity.PrincipalFromContext(ctx); found && p.Has(perm) {
		return nil
	}
	return &api.FieldForbiddenError{Action: string(perm)}
}

// bulkParams are the decoded parameters of an action (BulkRequest.Params).
type bulkParams struct {
	// fields is the merge patch of set_fields as sent: every id decodes
	// its own UpdateRequest, so the writes share no map.
	fields json.RawMessage
	// locationID is the location of set_location ("" clears it).
	locationID *string
	// tags are the catalog keys of add_tags and remove_tags.
	tags []string
	// ownerUserID and ownerTeamID are the owner of set_owner ("" clears).
	ownerUserID, ownerTeamID *string
	// delegate are the parameters of a delegating action (WP-780).
	delegate *delegateParams
}

// setFieldsExcluded are the members of UpdateCIRequest set_fields does not
// take: the status, location, asset link and owner have their own actions
// or paths, the reason is the bulk's, the discovery members are no manual
// values.
var setFieldsExcluded = []string{"status", "location_id", "asset_id", "owner_user_id", "owner_team_id",
	"change_reason", "discovery_source", "last_seen_at"}

// paramsError is a 422 on the parameter name for the whole request.
func paramsError(name, message string) error {
	return &api.FieldError{Field: "params." + name, Message: message, Status: http.StatusUnprocessableEntity}
}

// validateParams decodes and checks the parameters of the action: unknown
// or missing parameters and invalid values are 422 on params.<name>.
func validateParams(req *BulkRequest) error {
	members := map[string]json.RawMessage{}
	if raw := bytes.TrimSpace(req.Params); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &members); err != nil {
			return &api.FieldError{Field: "params", Message: "must be an object", Status: http.StatusUnprocessableEntity}
		}
	}
	allowed := map[string][]string{
		BulkSetStatus: nil, BulkArchive: nil,
		BulkSetFields:   {"fields"},
		BulkSetLocation: {"location_id"},
		BulkAddTags:     {"tags"}, BulkRemoveTags: {"tags"},
		BulkSetOwner: {"owner_user_id", "owner_team_id"},
	}[req.Action]
	if isDelegateAction(req.Action) {
		allowed = delegateParamNames[req.Action]
	}
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !slices.Contains(allowed, name) {
			return paramsError(name, "is no parameter of "+req.Action)
		}
	}
	p := &bulkParams{}
	var err error
	switch req.Action {
	case BulkSetFields:
		p.fields, err = validateSetFields(members["fields"])
	case BulkSetLocation:
		p.locationID, err = optionalID(members, "location_id")
		if err == nil && p.locationID == nil {
			err = paramsError("location_id", "is required")
		}
	case BulkAddTags, BulkRemoveTags:
		p.tags, err = validateTagParams(members["tags"])
	case BulkSetOwner:
		if p.ownerUserID, err = optionalID(members, "owner_user_id"); err != nil {
			break
		}
		if p.ownerTeamID, err = optionalID(members, "owner_team_id"); err == nil && p.ownerUserID == nil && p.ownerTeamID == nil {
			err = paramsError("owner_user_id", "owner_user_id or owner_team_id is required")
		}
	default:
		if isDelegateAction(req.Action) {
			p.delegate, err = validateDelegateParams(req.Action, members)
		}
	}
	if err != nil {
		return err
	}
	req.params = p
	return nil
}

// optionalID decodes the parameter name: absent is nil, null or "" clear
// (""), any other value must be a UUID.
func optionalID(members map[string]json.RawMessage, name string) (*string, error) {
	raw, ok := members[name]
	if !ok {
		return nil, nil
	}
	value := ""
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, paramsError(name, "must be a string")
		}
	}
	if value = strings.TrimSpace(value); value != "" {
		if !api.IsUUID(value) {
			return nil, paramsError(name, "must be a UUID or empty")
		}
		value = strings.ToLower(value)
	}
	return &value, nil
}

// validateTagParams decodes params.tags: a non-empty list of catalog keys
// in form (whether the catalog holds them checks checkBulkTags).
func validateTagParams(raw json.RawMessage) ([]string, error) {
	var tags []string
	if len(raw) == 0 {
		return nil, paramsError("tags", "is required")
	}
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil, paramsError("tags", "must be an array of tag keys")
	}
	if len(tags) == 0 {
		return nil, paramsError("tags", "must name at least one tag")
	}
	keys, violations := catalogKeys(tags)
	if len(violations) > 0 {
		return nil, paramsError("tags", "unknown tag: "+tags[violationIndex(violations[0].Field)])
	}
	return keys, nil
}

// violationIndex is the index of a violation on tags[<i>].
func violationIndex(field string) int {
	var i int
	if _, err := fmt.Sscanf(field, "tags[%d]", &i); err != nil {
		return 0
	}
	return i
}

// validateSetFields checks params.fields of set_fields: a merge patch of
// UpdateCIRequest without the excluded members, with at least one member,
// no null on a required one, no reserved attribute key, no attribute
// tags (E-125, a tag goes through add_tags) and valid typed columns.
func validateSetFields(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, paramsError("fields", "is required")
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return nil, paramsError("fields", "must be an object")
	}
	if len(members) == 0 {
		return nil, paramsError("fields", "must name at least one field")
	}
	for _, name := range setFieldsExcluded {
		if _, ok := members[name]; ok {
			return nil, paramsError("fields", name+" is not set by "+BulkSetFields)
		}
	}
	var req UpdateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, paramsError("fields", "is no change of CI fields: "+jsonProblem(err))
	}
	var violations *ValidationError
	if errors.As(req.nullViolations(), &violations) {
		return nil, paramsError("fields", "must not be null: "+strings.Join(violationFields(violations.Violations), ", "))
	}
	var reserved *ReservedKeyError
	if errors.As(CheckReservedKeys("attributes", req.Attributes), &reserved) {
		return nil, paramsError("fields", "reserved namespace, not allowed as an attribute key: "+strings.Join(reserved.Fields, ", "))
	}
	if _, ok := req.Attributes[LegacyTagsKey]; ok {
		return nil, paramsError("fields", "attributes."+LegacyTagsKey+" is no attribute; tags change through "+BulkAddTags+" and "+BulkRemoveTags)
	}
	if errors.As(validateTypedColumns(typedPatch(&req)), &violations) {
		return nil, paramsError("fields", "invalid value of: "+strings.Join(violationFields(violations.Violations), ", "))
	}
	return raw, nil
}

// jsonProblem is the part of a decode error a client can act on: the
// unknown member or the member of the wrong type.
func jsonProblem(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return typeErr.Field + " has the wrong type"
	}
	if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field ") {
		return "unknown field " + strings.TrimPrefix(msg, "json: unknown field ")
	}
	return "malformed"
}

// checkBulkTags checks the keys of add_tags and remove_tags against the
// tag catalog of the organization (WP-281): an unknown key is 422 on
// params.tags for the whole request, before any change.
func (s *Service) checkBulkTags(ctx context.Context, orgID string, req *BulkRequest) error {
	if (req.Action != BulkAddTags && req.Action != BulkRemoveTags) || req.params == nil {
		return nil
	}
	store, err := s.tagStore()
	if err != nil {
		return err
	}
	known := map[string]bool{}
	const pageSize = 500
	for offset := 0; ; offset += pageSize {
		tags, _, listErr := store.ListTags(ctx, orgID, api.PaginationParams{Limit: pageSize, Offset: offset})
		if listErr != nil {
			return listErr
		}
		for i := range tags {
			known[tags[i].Key] = true
		}
		if len(tags) <= pageSize {
			break
		}
	}
	for _, key := range req.params.tags {
		if !known[key] {
			return paramsError("tags", "unknown tag: "+key)
		}
	}
	return nil
}

// bulkOp is one run of a bulk operation: the organization, the author of
// its changes and the reason of their overrides.
type bulkOp struct {
	orgID, author, reason string
	// jobID is the job of the run ("" for a run without job).
	jobID string
	req   *BulkRequest
}

// manual is the update of one id as a manual change of the run (WP-368):
// the author, the reason and If-Match "*".
func (op *bulkOp) manual(req UpdateRequest) UpdateRequest { //nolint:gocritic // a request value like Service.Update takes
	anyVersion := api.IfMatchAny
	req.ChangeReason, req.IfMatch, req.Manual = op.reason, &anyVersion, &ManualChange{Author: op.author}
	return req
}

// bulkApply applies the operation to one id and reports it.
func (s *Service) bulkApply(ctx context.Context, op *bulkOp, id string) BulkItemResult {
	p := op.req.params
	if p == nil {
		p = &bulkParams{}
	}
	if isDelegateAction(op.req.Action) && p.delegate != nil {
		return s.bulkDelegate(ctx, op, id, p.delegate)
	}
	switch op.req.Action {
	case BulkSetFields:
		return s.bulkSetFields(ctx, op, id, p.fields)
	case BulkSetLocation:
		item, err := s.Update(ctx, op.orgID, id, op.manual(UpdateRequest{LocationID: p.locationID}))
		switch {
		case err != nil:
			return bulkFailed(id, err)
		case !strings.EqualFold(item.LocationID, *p.locationID):
			return bulkNotApplied(id, "the location was not applied")
		}
		return bulkOK(id)
	case BulkSetOwner:
		item, err := s.Update(ctx, op.orgID, id, op.manual(UpdateRequest{OwnerUserID: p.ownerUserID, OwnerTeamID: p.ownerTeamID}))
		switch {
		case err != nil:
			return bulkFailed(id, err)
		case (p.ownerUserID != nil && !strings.EqualFold(item.OwnerUserID, *p.ownerUserID)) ||
			(p.ownerTeamID != nil && !strings.EqualFold(item.OwnerTeamID, *p.ownerTeamID)):
			return bulkNotApplied(id, "the owner was not applied")
		}
		return bulkOK(id)
	case BulkAddTags, BulkRemoveTags:
		return s.bulkTags(ctx, op, id, p.tags)
	case BulkArchive:
		return s.bulkArchive(ctx, op, id)
	}
	status := op.req.Status
	item, err := s.Update(ctx, op.orgID, id, op.manual(UpdateRequest{Status: &status}))
	switch {
	case err != nil:
		return bulkFailed(id, err)
	case item.Status != status:
		// A write that left the status is no success (API-08).
		return bulkNotApplied(id, fmt.Sprintf("the status was not applied, the CI keeps status %s", item.Status))
	}
	return bulkOK(id)
}

// bulkSetFields writes the merge patch of set_fields to one id. An
// attribute that is an instance attribute of the CI is 422 for the id:
// the PATCH path would write it to _instance, which only the instance
// attribute API writes (WP-256).
func (s *Service) bulkSetFields(ctx context.Context, op *bulkOp, id string, raw json.RawMessage) BulkItemResult {
	var req UpdateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return bulkFailed(id, paramsError("fields", "is no change of CI fields"))
	}
	if len(req.Attributes) > 0 {
		defs, err := s.instanceFields(ctx, op.orgID, id)
		if err != nil {
			return bulkFailed(id, err)
		}
		var violations []Violation
		for i := range defs {
			if _, sent := req.Attributes[defs[i].Name]; sent {
				violations = append(violations, Violation{Field: "attributes." + defs[i].Name,
					Detail: "is an instance attribute of the CI; it changes through /cis/{id}/instance-attributes"})
			}
		}
		if len(violations) > 0 {
			return bulkFailed(id, &ValidationError{Violations: violations})
		}
	}
	if _, err := s.Update(ctx, op.orgID, id, op.manual(req)); err != nil {
		return bulkFailed(id, err)
	}
	return bulkOK(id)
}

// readForTags reads the CI of one id for its tags and checks the action
// grant of the run on it (the tag path checks none itself, WP-728).
func (s *Service) readForTags(ctx context.Context, orgID, id string) (*Item, error) {
	item, err := s.repo.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if actionErr := permission.CheckActionOn(ctx, permission.KindCI, permission.ActionFrom(ctx),
		func(context.Context) ([]permission.ActionTarget, error) {
			return []permission.ActionTarget{ciTarget(item)}, nil
		}); actionErr != nil {
		return nil, actionErr
	}
	return item, nil
}

// withTags returns the tag keys of a CI with keys added or removed, sorted.
func withTags(current, keys []string, add bool) []string {
	out := make([]string, 0, len(current)+len(keys))
	for _, key := range current {
		if add || !slices.Contains(keys, key) {
			out = append(out, key)
		}
	}
	if add {
		for _, key := range keys {
			if !slices.Contains(out, key) {
				out = append(out, key)
			}
		}
	}
	sort.Strings(out)
	return out
}

// sameTags reports whether two tag lists hold the same keys.
func sameTags(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	sort.Strings(x)
	sort.Strings(y)
	return slices.Equal(x, y)
}

// bulkTags adds or removes the tags of one CI: the new key list goes
// through the tag path of PUT /cis/{id}/tags with the version read as
// If-Match (a tag change in between is 409 for the id, API-07). A CI that
// already has (or lacks) every tag is ok without a write.
func (s *Service) bulkTags(ctx context.Context, op *bulkOp, id string, keys []string) BulkItemResult {
	store, err := s.tagStore()
	if err != nil {
		return bulkFailed(id, err)
	}
	item, err := s.readForTags(ctx, op.orgID, id)
	if err != nil {
		return bulkFailed(id, err)
	}
	next := withTags(item.Tags, keys, op.req.Action == BulkAddTags)
	if sameTags(next, item.Tags) {
		return bulkOK(id)
	}
	version := item.Version
	if _, err = store.ReplaceCITags(ctx, op.orgID, id, next, &version); err != nil {
		return bulkFailed(id, err)
	}
	return bulkOK(id)
}

// bulkArchive archives one CI (BLK-02, E-132): status inactive as a
// manual change and the system tag archived, in one transaction of the
// repository (TxRunner); when a part fails, the CI stays unchanged. A CI
// that is inactive and archived already is ok without a write.
func (s *Service) bulkArchive(ctx context.Context, op *bulkOp, id string) BulkItemResult {
	store, err := s.tagStore()
	if err != nil {
		return bulkFailed(id, err)
	}
	err = s.bulkTx(ctx, op.orgID, func(ctx context.Context) error {
		item, readErr := s.readForTags(ctx, op.orgID, id)
		if readErr != nil {
			return readErr
		}
		if item.Status != StatusInactive {
			inactive := StatusInactive
			updated, updateErr := s.Update(ctx, op.orgID, id, op.manual(UpdateRequest{Status: &inactive}))
			if updateErr != nil {
				return updateErr
			}
			if updated.Status != StatusInactive {
				return errStatusNotApplied
			}
			item = updated
		}
		if slices.Contains(item.Tags, ArchivedTag) {
			return nil
		}
		version := item.Version
		_, tagErr := store.ReplaceCITags(ctx, op.orgID, id, withTags(item.Tags, []string{ArchivedTag}, true), &version)
		return tagErr
	})
	switch {
	case errors.Is(err, errStatusNotApplied):
		return bulkNotApplied(id, "the status inactive was not applied")
	case err != nil:
		return bulkFailed(id, err)
	}
	return bulkOK(id)
}

// errStatusNotApplied rolls an archive back whose status write left the
// status.
var errStatusNotApplied = errors.New("the status was not applied")

// bulkTx runs fn in one transaction of the repository (TxRunner) that the
// writes of fn's context join; without one fn runs as it is.
func (s *Service) bulkTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	if runner, ok := s.repo.(TxRunner); ok {
		return runner.RunInTx(ctx, orgID, fn)
	}
	return fn(ctx)
}
