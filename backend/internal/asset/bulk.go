package asset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Bulk operations on assets (API-08, BLK-01, BLK-02; WP-781): POST
// /api/v1/assets/bulk with the contract of POST /cis/bulk (WP-304): an
// action (default set_status), 1–500 ids, the status of set_status and the
// parameters of the other actions in params. From 100 ids on the operation
// runs as a job asset.bulk and the request answers 202 with it; below it
// runs in the request and answers the report per id (ci.BulkResult), so
// one failing asset does not fail the others.
//
// The bulk writes nothing of its own: every action runs per asset through
// the path of its single operation. set_status and set_fields go through
// applyUpdate like PATCH /assets/{id} (field permissions, If-Match "*",
// Repository.Update, trail and asset.changed); set_location books a
// transfer through the movement service (WP-274); add_tags and remove_tags
// write asset_tag_link like PUT /assets/{id}/tags (WP-755) with the version
// read as If-Match; assign runs the assignment service (WP-275);
// lifecycle_transition the transactional transition with the system guards
// (WP-279); archive the tag archived and the transition to retired in one
// transaction (E-132 (a)). The movements, assignments, transitions and the
// export are reached through the ports of BulkDelegates, which the server
// wires (server/report_adapters.go).

// Bulk limits and the job kind of the asset bulk.
const (
	MaxBulkIDs     = ci.MaxBulkIDs
	BulkJobFromIDs = ci.BulkJobFromIDs
	BulkJobKind    = "asset.bulk"
)

// The actions of POST /api/v1/assets/bulk.
const (
	BulkSetStatus           = "set_status"
	BulkSetFields           = "set_fields"
	BulkSetLocation         = "set_location"
	BulkAddTags             = "add_tags"
	BulkRemoveTags          = "remove_tags"
	BulkAssign              = "assign"
	BulkLifecycleTransition = "lifecycle_transition"
	BulkArchive             = "archive"
	BulkExport              = "export"
	// ArchivedTag is the system tag archive sets (WP-281); archiveState
	// is the lifecycle state archive enters (E-132 (a)).
	ArchivedTag  = "archived"
	archiveState = "retired"
)

// Report outcomes and bulk tuning, as in the CI bulk.
const (
	bulkResultOK      = "ok"
	bulkResultError   = "error"
	bulkResultSkipped = "skipped"
	bulkCancelEvery   = 25
	bulkWorkers       = 8
	bulkMalformedID   = "invalid asset id"
	bulkArchiveReason = "bulk archive"
)

// Statuses are the values of asset.status (E-120); the check is in code
// since 000055 dropped asset_status_check.
var Statuses = []string{"in_stock", "assigned", "maintenance", "retired", "disposed", "lost"}

// bulkActionPermissions are the rights of each action beyond asset:write,
// which the route middleware checks (WP-781): archive enters retired, which
// needs lifecycle:transition on the single path; set_location is a movement
// (asset:move), assign the right of POST /assets/{id}/assign.
var bulkActionPermissions = map[string][]identity.Permission{
	BulkSetStatus:           nil,
	BulkSetFields:           nil,
	BulkAddTags:             nil,
	BulkRemoveTags:          nil,
	BulkArchive:             {identity.PermLifecycleTransition},
	BulkSetLocation:         {identity.PermAssetMove},
	BulkAssign:              {identity.PermAssignmentWrite},
	BulkLifecycleTransition: {identity.PermLifecycleTransition},
	BulkExport:              {identity.PermExportRun},
}

// bulkParamNames are the parameters of each action.
var bulkParamNames = map[string][]string{
	BulkSetStatus:           nil,
	BulkArchive:             nil,
	BulkSetFields:           {"fields"},
	BulkSetLocation:         {"to_location_id"},
	BulkAddTags:             {"tags"},
	BulkRemoveTags:          {"tags"},
	BulkAssign:              {"assigned_user_id", "assigned_team_id", "due_at", "notes"},
	BulkLifecycleTransition: {"to_state", "from_state", "context", "reason"},
	BulkExport:              {"format"},
}

// BulkRequest is the body of POST /api/v1/assets/bulk.
type BulkRequest struct {
	Action string   `json:"action"`
	IDs    []string `json:"ids"`
	// Status is the target of set_status; no other action takes it.
	Status string `json:"status,omitempty"`
	// Params are the parameters of the other actions, kept as sent so a
	// job decodes them again.
	Params json.RawMessage `json:"params,omitempty"`
	// params are the decoded Params, malformed the ids that are no UUID
	// (validateBulk).
	params    *bulkParams
	malformed map[string]bool
}

// bulkParams are the decoded parameters of an action.
type bulkParams struct {
	fields       json.RawMessage
	toLocationID string
	tags         []string
	assignment   *BulkAssignment
	transition   *ci.BulkTransition
	format       string
}

// BulkAssignment is the assignment of the action assign: the body of POST
// /api/v1/assets/{id}/assign without signature.
type BulkAssignment struct {
	UserID string
	TeamID string
	DueAt  *time.Time
	Notes  string
}

// BulkMovements books the transfer of an asset to a location through the
// movement service (WP-274); an asset not visible is ErrNotFound, other
// outcomes a field error or a *ci.BulkItemError.
type BulkMovements interface {
	Transfer(ctx context.Context, orgID, assetID, toLocationID, actor string) error
}

// BulkAssignments assigns a free asset through the assignment service
// (WP-275); errors as BulkMovements.
type BulkAssignments interface {
	Assign(ctx context.Context, orgID, assetID string, a *BulkAssignment) error
}

// BulkLifecycle runs the transactional lifecycle transition of an asset
// with its effective model and the system guards (WP-279); it joins the
// transaction of ctx. Errors as BulkMovements.
type BulkLifecycle interface {
	Transition(ctx context.Context, orgID, assetID string, t *ci.BulkTransition) error
}

// BulkExporter creates one export job of the assets ids in format (WP-782
// wires it); without it the action export is unknown (422 action).
type BulkExporter interface {
	CreateExport(ctx context.Context, orgID, format string, ids []string) (*ci.BulkExportJob, error)
}

// BulkDelegates are the ports of the actions that run outside this
// package; a nil port makes its action answer 503 (export: 422).
type BulkDelegates struct {
	Movements    BulkMovements
	Assignments  BulkAssignments
	Lifecycle    BulkLifecycle
	Exporter     BulkExporter
	Entitlements ci.BulkEntitlements
}

// TagCatalog reports the keys the tag catalog of the organization lacks
// (PGRepository); without it every well-formed key counts as known.
type TagCatalog interface {
	UnknownTags(ctx context.Context, orgID string, keys []string) ([]string, error)
}

// bulkCancels is the cancel checkpoint of a bulk job (JOB-03); jobs.Service
// and jobs.Queue implement it.
type bulkCancels interface {
	IsCancelled(ctx context.Context, orgID, id string) (bool, error)
	SaveCancelledResult(ctx context.Context, orgID, id string, result any) (*jobs.Job, error)
}

// BulkJobs runs bulk jobs in this process; jobs.Service implements it.
type BulkJobs interface {
	Enqueue(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
	ClaimJob(ctx context.Context, orgID, id string) (*jobs.Job, error)
	Succeed(ctx context.Context, orgID, id string, result any) (*jobs.Job, error)
	bulkCancels
}

// BulkQueue submits bulk jobs to the job queue (TEC-14), whose workers run
// them with RunBulkJob; jobs.Queue implements it.
type BulkQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
	bulkCancels
}

// bulkWiring holds the jobs and ports of the bulk.
type bulkWiring struct {
	jobs          BulkJobs
	queue         BulkQueue
	delegates     BulkDelegates
	creatorPolicy ci.CreatorPolicy
}

// WithBulkJobs runs bulks from BulkJobFromIDs ids on as jobs asset.bulk.
func (h *Handler) WithBulkJobs(j BulkJobs) *Handler {
	h.bulk.jobs = j
	return h
}

// WithBulkQueue runs the bulk jobs on the job queue instead of in this
// process.
func (h *Handler) WithBulkQueue(q BulkQueue) *Handler {
	h.bulk.queue = q
	return h
}

// WithBulkDelegates wires the ports of the delegating actions.
func (h *Handler) WithBulkDelegates(d *BulkDelegates) *Handler {
	h.bulk.delegates = *d
	return h
}

// WithCreatorPolicy sets how a bulk job of the queue resolves the field
// policy of its creator (as ci.Service.WithCreatorPolicy, WP-728); without
// it such a job fails (fail-closed).
func (h *Handler) WithCreatorPolicy(fn ci.CreatorPolicy) *Handler {
	h.bulk.creatorPolicy = fn
	return h
}

// featureNotEntitledError refuses export without the feature export_csv.
type featureNotEntitledError struct{ feature string }

// validation: names the feature key.
func (e *featureNotEntitledError) Error() string {
	return "the organization is not entitled to the feature " + e.feature
}

// ProblemType implements api.TypedProblem.
func (e *featureNotEntitledError) ProblemType() string { return api.ProblemFeatureNotEntitled }

// paramsError is a 422 on params.<name> for the whole request.
func paramsError(name, message string) error {
	return &api.FieldError{Field: "params." + name, Message: message, Status: http.StatusUnprocessableEntity}
}

// bulkActions returns the actions the request may name, sorted: export
// only with an exporter.
func bulkActions(export bool) []string {
	out := make([]string, 0, len(bulkActionPermissions))
	for action := range bulkActionPermissions {
		if action != BulkExport || export {
			out = append(out, action)
		}
	}
	sort.Strings(out)
	return out
}

// validateBulk normalizes and checks a bulk request like ci.validateBulk
// (WP-690): a known action (export only with an exporter), 1–500 distinct
// ids in canonical lower-case form, a malformed id reported on its own
// (400), a valid status for set_status only and the parameters of the
// action (422 on params.<name>).
func validateBulk(req *BulkRequest, export bool) error {
	if req.Action == "" {
		req.Action = BulkSetStatus
	}
	if !slices.Contains(bulkActions(export), req.Action) {
		return &api.FieldError{Field: "action", Message: "must be one of " + strings.Join(bulkActions(export), ", "),
			Status: http.StatusUnprocessableEntity}
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id = strings.TrimSpace(id); api.IsUUID(id) {
			id = strings.ToLower(id)
		}
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0:
		return &api.FieldError{Field: "ids", Message: "at least one id is required"}
	case len(ids) > MaxBulkIDs:
		return &api.FieldError{Field: "ids", Message: fmt.Sprintf("at most %d ids per request", MaxBulkIDs), Status: http.StatusUnprocessableEntity}
	}
	req.IDs, req.malformed = ids, nil
	for _, id := range ids {
		if !api.IsUUID(id) {
			if req.malformed == nil {
				req.malformed = map[string]bool{}
			}
			req.malformed[id] = true
		}
	}
	switch {
	case req.Action != BulkSetStatus && req.Status != "":
		return &api.FieldError{Field: "status", Message: "only " + BulkSetStatus + " takes a status", Status: http.StatusUnprocessableEntity}
	case req.Action == BulkSetStatus && !slices.Contains(Statuses, req.Status):
		return &api.FieldError{Field: "status", Message: "must be one of " + strings.Join(Statuses, ", "), Status: http.StatusUnprocessableEntity}
	}
	return validateParams(req)
}

// validateParams decodes and checks the parameters of the action.
func validateParams(req *BulkRequest) error {
	members := map[string]json.RawMessage{}
	if raw := bytes.TrimSpace(req.Params); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &members); err != nil {
			return &api.FieldError{Field: "params", Message: "must be an object", Status: http.StatusUnprocessableEntity}
		}
	}
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !slices.Contains(bulkParamNames[req.Action], name) {
			return paramsError(name, "is no parameter of "+req.Action)
		}
	}
	p := &bulkParams{}
	var err error
	switch req.Action {
	case BulkSetFields:
		p.fields, err = validateSetFields(members["fields"])
	case BulkSetLocation:
		p.toLocationID, err = requiredID(members, "to_location_id")
	case BulkAddTags, BulkRemoveTags:
		p.tags, err = validateTagParams(members["tags"])
	case BulkAssign:
		p.assignment, err = validateAssignParams(members)
	case BulkLifecycleTransition:
		p.transition, err = validateTransitionParams(members)
	case BulkExport:
		p.format, err = requiredString(members, "format")
		if err == nil && p.format != "csv" && p.format != "json" {
			err = paramsError("format", "must be csv or json")
		}
	}
	if err != nil {
		return err
	}
	req.params = p
	return nil
}

// requiredString decodes the required, non-empty string parameter name.
func requiredString(members map[string]json.RawMessage, name string) (string, error) {
	value, err := optionalString(members, name)
	if err == nil && value == "" {
		err = paramsError(name, "is required")
	}
	return value, err
}

// optionalString decodes the string parameter name; absent or null is "".
func optionalString(members map[string]json.RawMessage, name string) (string, error) {
	raw := bytes.TrimSpace(members[name])
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", paramsError(name, "must be a string")
	}
	return strings.TrimSpace(value), nil
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

// setFieldsExcluded are the members of UpdateAssetRequest set_fields does
// not take: the status, the location, the lifecycle state and the holder
// cache have their own actions or canonical paths.
var setFieldsExcluded = []string{"status", "location_id", "lifecycle_state", "assigned_user_id", "assigned_team_id", "assignment_due_at"}

// setFieldsMembers are the members of UpdateAssetRequest set_fields takes.
var setFieldsMembers = func() []string {
	var out []string
	t := reflect.TypeOf(UpdateRequest{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" && !slices.Contains(setFieldsExcluded, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}()

// validateSetFields checks params.fields of set_fields: a merge patch of
// UpdateAssetRequest with at least one member, none excluded or unknown
// (tags have their own actions) and every value of its type.
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
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch {
		case slices.Contains(setFieldsExcluded, name):
			return nil, paramsError("fields", name+" is not set by "+BulkSetFields)
		case name == fieldTags:
			return nil, paramsError("fields", "tags change through "+BulkAddTags+" and "+BulkRemoveTags)
		case !slices.Contains(setFieldsMembers, name):
			return nil, paramsError("fields", name+" is no field of an asset")
		}
	}
	if _, err := decodeFields(raw); err != nil {
		return nil, paramsError("fields", "a value does not match the type of its field")
	}
	return raw, nil
}

// decodeFields decodes the merge patch of set_fields into a request of its
// own (every id gets one, so the writes share no map).
func decodeFields(raw json.RawMessage) (*UpdateRequest, error) {
	var req UpdateRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

// validateTagParams decodes params.tags: a non-empty list of catalog keys
// in form; whether the catalog holds them checks prepareBulk.
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
	keys, violations := tagKeys(tags)
	if len(violations) > 0 {
		var i int
		if _, err := fmt.Sscanf(violations[0].Field, "tags[%d]", &i); err != nil || i >= len(tags) {
			i = 0
		}
		return nil, paramsError("tags", "unknown tag: "+tags[i])
	}
	return keys, nil
}

// validateAssignParams decodes the parameters of assign: exactly one
// holder as a UUID, an optional due date (RFC 3339) and notes.
func validateAssignParams(members map[string]json.RawMessage) (*BulkAssignment, error) {
	a := &BulkAssignment{}
	var err error
	if a.UserID, err = optionalString(members, "assigned_user_id"); err != nil {
		return nil, err
	}
	if a.TeamID, err = optionalString(members, "assigned_team_id"); err != nil {
		return nil, err
	}
	switch {
	case a.UserID != "" && a.TeamID != "":
		return nil, paramsError("assigned_user_id", "exactly one of assigned_user_id and assigned_team_id is allowed")
	case a.UserID == "" && a.TeamID == "":
		return nil, paramsError("assigned_user_id", "one of assigned_user_id and assigned_team_id is required")
	case a.UserID != "" && !api.IsUUID(a.UserID):
		return nil, paramsError("assigned_user_id", "must be a UUID")
	case a.TeamID != "" && !api.IsUUID(a.TeamID):
		return nil, paramsError("assigned_team_id", "must be a UUID")
	}
	a.UserID, a.TeamID = strings.ToLower(a.UserID), strings.ToLower(a.TeamID)
	if raw := bytes.TrimSpace(members["due_at"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var due time.Time
		if json.Unmarshal(raw, &due) != nil {
			return nil, paramsError("due_at", "must be a date-time (RFC 3339)")
		}
		a.DueAt = &due
	}
	if a.Notes, err = optionalString(members, "notes"); err != nil {
		return nil, err
	}
	return a, nil
}

// validateTransitionParams decodes the parameters of lifecycle_transition.
func validateTransitionParams(members map[string]json.RawMessage) (*ci.BulkTransition, error) {
	t := &ci.BulkTransition{}
	var err error
	if t.ToState, err = requiredString(members, "to_state"); err != nil {
		return nil, err
	}
	if raw := bytes.TrimSpace(members["from_state"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var from string
		if json.Unmarshal(raw, &from) != nil {
			return nil, paramsError("from_state", "must be a string")
		}
		t.FromState = &from
	}
	if raw := bytes.TrimSpace(members["context"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if json.Unmarshal(raw, &t.Context) != nil {
			return nil, paramsError("context", "must be an object")
		}
	}
	if t.Reason, err = optionalString(members, "reason"); err != nil {
		return nil, err
	}
	return t, nil
}

// checkBulkPermission checks the rights of the action beyond asset:write
// for the whole request: without a principal they count as missing
// (fail-closed), a missing one is 403 naming it.
func checkBulkPermission(ctx context.Context, action string) error {
	for _, perm := range bulkActionPermissions[action] {
		if err := checkPermission(ctx, perm); err != nil {
			return err
		}
	}
	return nil
}

// prepareBulk checks what the action needs for the whole request before
// any change and before a job: its port (503 without), the catalog keys
// of the tags (422 on params.tags), the entitlement export_csv of export
// (403 feature-not-entitled).
func (h *Handler) prepareBulk(ctx context.Context, orgID string, req *BulkRequest) error {
	d := &h.bulk.delegates
	switch req.Action {
	case BulkSetLocation:
		if d.Movements == nil {
			return ci.ErrBulkActionUnavailable
		}
	case BulkAssign:
		if d.Assignments == nil {
			return ci.ErrBulkActionUnavailable
		}
	case BulkLifecycleTransition:
		if d.Lifecycle == nil {
			return ci.ErrBulkActionUnavailable
		}
	case BulkArchive:
		if _, ok := h.repo.(TagStore); !ok || d.Lifecycle == nil {
			return ci.ErrBulkActionUnavailable
		}
		return h.checkCatalog(ctx, orgID, []string{ArchivedTag})
	case BulkAddTags, BulkRemoveTags:
		if _, ok := h.repo.(TagStore); !ok {
			return ci.ErrBulkActionUnavailable
		}
		return h.checkCatalog(ctx, orgID, req.params.tags)
	case BulkExport:
		if d.Entitlements == nil {
			return ci.ErrBulkActionUnavailable
		}
		if !d.Entitlements.IsEnabled(ctx, orgID, entitlement.FeatureExportCSV) {
			return &featureNotEntitledError{feature: entitlement.FeatureExportCSV}
		}
		if len(req.malformed) > 0 {
			return &api.FieldError{Field: "ids", Message: "an export takes asset ids (UUIDs) only"}
		}
	}
	return nil
}

// checkCatalog refuses keys the tag catalog lacks (422 on params.tags).
func (h *Handler) checkCatalog(ctx context.Context, orgID string, keys []string) error {
	catalog, ok := h.repo.(TagCatalog)
	if !ok {
		return nil
	}
	unknown, err := catalog.UnknownTags(ctx, orgID, keys)
	if err != nil {
		return err
	}
	if len(unknown) > 0 {
		return paramsError("tags", "unknown tag: "+unknown[0])
	}
	return nil
}

// RunBulk validates and runs a bulk: below BulkJobFromIDs ids (or without
// a job service) in the request with the report, from there on as a job
// asset.bulk, submitted to the job queue when there is one, otherwise run
// in the background of this process; export answers its one export job.
// Every check of the whole request runs before any change and any job.
func (h *Handler) RunBulk(ctx context.Context, orgID string, req *BulkRequest) (*ci.BulkResult, *jobs.Job, error) {
	if err := validateBulk(req, h.bulk.delegates.Exporter != nil); err != nil {
		return nil, nil, err
	}
	if err := checkBulkPermission(ctx, req.Action); err != nil {
		return nil, nil, err
	}
	if err := h.prepareBulk(ctx, orgID, req); err != nil {
		return nil, nil, err
	}
	user := tenant.FromContext(ctx).UserID
	switch {
	case req.Action == BulkExport:
		exp, err := h.bulk.delegates.Exporter.CreateExport(ctx, orgID, req.params.format, req.IDs)
		if err != nil {
			return nil, nil, err
		}
		return nil, &jobs.Job{ID: exp.ID, OrganizationID: orgID, Kind: ci.BulkExportJobKind, Status: jobs.Status(exp.Status),
			RefTable: "export_job", RefID: exp.ID}, nil
	case len(req.IDs) < BulkJobFromIDs || (h.bulk.jobs == nil && h.bulk.queue == nil):
		return h.runBulk(ctx, orgID, req, nil), nil, nil
	case h.bulk.queue != nil:
		job, _, err := h.bulk.queue.Submit(ctx, orgID, BulkJobKind, req, jobs.EnqueueOptions{CreatedBy: user})
		if err != nil {
			return nil, nil, fmt.Errorf("submit asset bulk job: %w", err)
		}
		return nil, job, nil
	}
	job, _, err := h.bulk.jobs.Enqueue(ctx, orgID, BulkJobKind, req, jobs.EnqueueOptions{CreatedBy: user})
	if err != nil {
		return nil, nil, fmt.Errorf("enqueue asset bulk job: %w", err)
	}
	// The job runs with the caller's tenant, scope and policy after the
	// request.
	go h.runBulkJob(context.WithoutCancel(ctx), orgID, job.ID, req)
	return nil, job, nil
}

// runBulkJob claims the in-process job, runs the bulk and records the
// report; a job cancelled while it ran keeps the partial report.
func (h *Handler) runBulkJob(ctx context.Context, orgID, jobID string, req *BulkRequest) {
	claimed, err := h.bulk.jobs.ClaimJob(ctx, orgID, jobID)
	if err != nil || claimed == nil {
		slog.ErrorContext(ctx, "claim asset bulk job", "job", jobID, "error", err)
		return
	}
	result := h.runBulk(ctx, orgID, req, cancelCheck(ctx, h.bulk.jobs, orgID, jobID))
	if keepCancelled(ctx, h.bulk.jobs, orgID, jobID, result) {
		return
	}
	if _, err = h.bulk.jobs.Succeed(ctx, orgID, jobID, result); err != nil {
		if errors.Is(err, jobs.ErrTransition) && keepCancelled(ctx, h.bulk.jobs, orgID, jobID, result) {
			return
		}
		slog.ErrorContext(ctx, "record asset bulk job", "job", jobID, "error", err)
	}
}

// RunBulkJob runs a bulk job of the job queue (handler of asset.bulk) with
// the tenant and scope of its creator (jobs.CreatorContext) and the field
// policy of the creator (WithCreatorPolicy; without it the job fails).
// The rights of the action were checked before the job was submitted; the
// run has no principal and does not check them again. The report is the
// job result.
func (h *Handler) RunBulkJob(ctx context.Context, job *jobs.Job) (any, error) {
	var req BulkRequest
	if err := json.Unmarshal(job.Params, &req); err != nil {
		return nil, fmt.Errorf("decode asset bulk job parameters: %w", err)
	}
	if err := validateBulk(&req, false); err != nil {
		return nil, fmt.Errorf("invalid asset bulk job parameters: %w", err)
	}
	if h.bulk.creatorPolicy == nil {
		return nil, ci.ErrNoCreatorPolicy
	}
	runCtx := jobs.CreatorContext(ctx, job)
	policy, err := h.bulk.creatorPolicy(runCtx, job)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ci.ErrNoCreatorPolicy, err)
	}
	runCtx = permission.WithPolicy(runCtx, policy)
	var cancels bulkCancels
	if h.bulk.queue != nil {
		cancels = h.bulk.queue
	} else if h.bulk.jobs != nil {
		cancels = h.bulk.jobs
	}
	jobCtx := context.WithoutCancel(ctx)
	result := h.runBulk(runCtx, job.OrganizationID, &req, cancelCheck(jobCtx, cancels, job.OrganizationID, job.ID))
	if cancels != nil && keepCancelled(jobCtx, cancels, job.OrganizationID, job.ID, result) {
		return nil, nil
	}
	return result, nil
}

// cancelCheck asks whether the job was cancelled; nil without jobs.
func cancelCheck(ctx context.Context, c bulkCancels, orgID, jobID string) func() bool {
	if c == nil {
		return nil
	}
	return func() bool {
		cancelled, err := c.IsCancelled(ctx, orgID, jobID)
		if err != nil {
			slog.WarnContext(ctx, "check asset bulk job cancel", "job", jobID, "error", err)
		}
		return err == nil && cancelled
	}
}

// keepCancelled stores the report of a job cancelled while it ran on the
// job and reports whether it was cancelled.
func keepCancelled(ctx context.Context, c bulkCancels, orgID, jobID string, result *ci.BulkResult) bool {
	if !result.Cancelled {
		if cancelled, err := c.IsCancelled(ctx, orgID, jobID); err != nil || !cancelled {
			return false
		}
	}
	if _, err := c.SaveCancelledResult(ctx, orgID, jobID, result); err != nil {
		slog.ErrorContext(ctx, "record the report of a cancelled asset bulk job", "job", jobID, "error", err)
	}
	return true
}

// bulkOp is one run of a bulk: the organization, the request, the field
// policy of the run and the actor of its movements.
type bulkOp struct {
	orgID  string
	req    *BulkRequest
	policy *permission.FieldPolicy
	actor  string
}

// runBulk applies the action to every id, bulkWorkers at a time, and
// reports each in the order of the ids. cancelled (nil: none) is asked
// before every bulkCancelEvery-th id; after a cancel the remaining ids are
// skipped.
func (h *Handler) runBulk(ctx context.Context, orgID string, req *BulkRequest, cancelled func() bool) *ci.BulkResult {
	result := &ci.BulkResult{Action: req.Action, Total: len(req.IDs), Items: make([]ci.BulkItemResult, len(req.IDs))}
	op := &bulkOp{orgID: orgID, req: req, actor: actorOf(ctx)}
	policy, policyErr := permission.PolicyFrom(ctx)
	op.policy = policy
	itemCtx := context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	slots := make(chan struct{}, bulkWorkers)
	for i, id := range req.IDs {
		if ctx.Err() != nil || (cancelled != nil && i > 0 && i%bulkCancelEvery == 0 && cancelled()) {
			result.Cancelled = errors.Is(context.Cause(ctx), jobs.ErrCancelled) || ctx.Err() == nil
			for j := i; j < len(req.IDs); j++ {
				result.Items[j] = ci.BulkItemResult{ID: req.IDs[j], Result: bulkResultSkipped}
			}
			break
		}
		switch {
		case req.malformed[id]:
			result.Items[i] = ci.BulkItemResult{ID: id, Result: bulkResultError, Code: http.StatusBadRequest, Error: bulkMalformedID,
				Type: problemType(nil, http.StatusBadRequest)}
			continue
		case policyErr != nil:
			result.Items[i] = bulkFailed(id, policyErr)
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, id string) {
			defer func() { <-slots; wg.Done() }()
			result.Items[i] = h.bulkApply(itemCtx, op, id)
		}(i, id)
	}
	wg.Wait()
	for i := range result.Items {
		switch result.Items[i].Result {
		case bulkResultOK:
			result.Succeeded++
		case bulkResultSkipped:
			result.Skipped++
		default:
			result.Failed++
		}
	}
	return result
}

// actorOf is the acting user of a run: the tenant user, else the
// principal's subject.
func actorOf(ctx context.Context) string {
	if id := tenant.FromContext(ctx).UserID; id != "" {
		return id
	}
	if p, ok := identity.PrincipalFromContext(ctx); ok {
		return p.Subject
	}
	return ""
}

// bulkApply applies the action to one asset and reports it.
func (h *Handler) bulkApply(ctx context.Context, op *bulkOp, id string) ci.BulkItemResult {
	var err error
	switch op.req.Action {
	case BulkSetStatus, BulkSetFields:
		err = h.bulkUpdate(ctx, op, id)
	case BulkSetLocation:
		err = h.bulk.delegates.Movements.Transfer(ctx, op.orgID, id, op.req.params.toLocationID, op.actor)
	case BulkAddTags, BulkRemoveTags:
		err = h.bulkTags(ctx, op, id)
	case BulkAssign:
		assignment := *op.req.params.assignment
		err = h.bulk.delegates.Assignments.Assign(ctx, op.orgID, id, &assignment)
	case BulkLifecycleTransition:
		transition := *op.req.params.transition
		err = h.bulk.delegates.Lifecycle.Transition(ctx, op.orgID, id, &transition)
	case BulkArchive:
		err = h.bulkArchive(ctx, op, id)
	default:
		err = &ci.BulkItemError{Status: http.StatusUnprocessableEntity, Detail: "the action does not run per id"}
	}
	if err != nil {
		return bulkFailed(id, err)
	}
	return ci.BulkItemResult{ID: id, Result: bulkResultOK, Code: http.StatusOK}
}

// bulkUpdate writes set_status or set_fields through applyUpdate, the
// write path of PATCH, with If-Match "*" (the bulk names no version).
func (h *Handler) bulkUpdate(ctx context.Context, op *bulkOp, id string) error {
	req := &UpdateRequest{}
	if op.req.Action == BulkSetStatus {
		status := op.req.Status
		req.Status = &status
	} else {
		decoded, err := decodeFields(op.req.params.fields)
		if err != nil {
			return paramsError("fields", "invalid value")
		}
		req = decoded
	}
	anyVersion := api.IfMatchAny
	req.IfMatch = &anyVersion
	_, err := h.applyUpdate(ctx, op.orgID, id, op.policy, req)
	return err
}

// bulkTags adds or removes the tags of one asset through the tag path of
// PUT /assets/{id}/tags with the version read as If-Match; an asset with
// nothing to change is ok without a write.
func (h *Handler) bulkTags(ctx context.Context, op *bulkOp, id string) error {
	current, err := h.repo.GetByID(ctx, op.orgID, id)
	if err != nil {
		return err
	}
	keys := slices.Clone(current.Tags)
	for _, tag := range op.req.params.tags {
		has := slices.Contains(keys, tag)
		switch {
		case op.req.Action == BulkAddTags && !has:
			keys = append(keys, tag)
		case op.req.Action == BulkRemoveTags && has:
			keys = slices.DeleteFunc(keys, func(k string) bool { return k == tag })
		}
	}
	if len(keys) == len(current.Tags) {
		return nil
	}
	version := current.Version
	_, err = h.repo.(TagStore).ReplaceTags(ctx, op.orgID, id, keys, &version)
	return err
}

// txRunner runs fn in one transaction that the repository calls with its
// context join (PGRepository.RunInTx).
type txRunner interface {
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
}

// bulkArchive archives one asset (E-132 (a)): the tag archived and the
// transition to retired in one transaction, the tag first with the version
// read as If-Match. A failed transition (retired not reachable: 422
// invalid-transition) rolls the tag back; an asset in retired with the tag
// is ok without a change.
func (h *Handler) bulkArchive(ctx context.Context, op *bulkOp, id string) error {
	archive := func(ctx context.Context) error {
		current, err := h.repo.GetByID(ctx, op.orgID, id)
		if err != nil {
			return err
		}
		if !slices.Contains(current.Tags, ArchivedTag) {
			version := current.Version
			if _, err = h.repo.(TagStore).ReplaceTags(ctx, op.orgID, id, append(slices.Clone(current.Tags), ArchivedTag), &version); err != nil {
				return err
			}
		}
		if current.LifecycleState == archiveState {
			return nil
		}
		return h.bulk.delegates.Lifecycle.Transition(ctx, op.orgID, id, &ci.BulkTransition{ToState: archiveState, Reason: bulkArchiveReason})
	}
	if tx, ok := h.repo.(txRunner); ok {
		return tx.RunInTx(ctx, op.orgID, archive)
	}
	return archive(ctx)
}

// bulkFailed is the report of an id whose change failed with err.
func bulkFailed(id string, err error) ci.BulkItemResult {
	code, msg, fields := bulkError(err)
	return ci.BulkItemResult{ID: id, Result: bulkResultError, Code: code, Error: msg, Type: problemType(err, code), Fields: fields}
}

// problemType is the problem type of the report of an id: the type of an
// api.TypedProblem, otherwise https://reticora.io/problems/<code>.
func problemType(err error, code int) string {
	var typed api.TypedProblem
	if err != nil && errors.As(err, &typed) && typed.ProblemType() != "" {
		return typed.ProblemType()
	}
	return fmt.Sprintf("https://reticora.io/problems/%d", code)
}

// bulkError maps the error of one id onto the status, message and fields
// its single operation answers (writeAssetError): 404 for an asset not
// found or outside the scope, 403 for refused field permissions (with the
// fields) or an asset the caller may not change, 409 for a taken inventory
// number, barcode or RFID tag and for a field changed since the read (with
// the fields), 422 for violations; database errors sanitized like the
// single PATCH, an unreachable dependency 503, everything else 500.
func bulkError(err error) (code int, msg string, fields []string) {
	var item *ci.BulkItemError
	var forbidden *api.FieldForbiddenError
	var conflict *ConflictError
	var taken *api.ConflictError
	var tagsErr *TagsError
	var fe *api.FieldError
	switch {
	case errors.As(err, &item):
		if item.Status == 0 {
			return http.StatusInternalServerError, item.Detail, item.Fields
		}
		return item.Status, item.Detail, item.Fields
	case errors.Is(err, ci.ErrBulkActionUnavailable):
		return http.StatusServiceUnavailable, ci.ErrBulkActionUnavailable.Error(), nil
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, ErrNotFound.Error(), nil
	case errors.Is(err, ErrCINotFound):
		return http.StatusNotFound, ErrCINotFound.Error(), nil
	case errors.Is(err, ErrNotWritable):
		return http.StatusForbidden, ErrNotWritable.Error(), nil
	case errors.As(err, &forbidden):
		return http.StatusForbidden, forbidden.Error(), forbidden.Fields
	case errors.As(err, &conflict):
		return http.StatusConflict, conflict.Error(), conflict.Fields
	case errors.As(err, &taken):
		return http.StatusConflict, taken.Error(), []string{taken.Field}
	case errors.Is(err, ErrPreconditionRequired):
		return http.StatusPreconditionRequired, err.Error(), nil
	case errors.Is(err, ErrPreconditionFailed):
		return http.StatusPreconditionFailed, err.Error(), nil
	case errors.As(err, &tagsErr):
		return http.StatusUnprocessableEntity, tagsErr.Error(), []string{fieldTags}
	case errors.As(err, &fe):
		if fe.Status != 0 {
			return fe.Status, fe.Error(), []string{fe.Field}
		}
		return http.StatusBadRequest, fe.Error(), []string{fe.Field}
	}
	if status, _, detail, ok := api.ClassifyDBError(err); ok {
		return status, detail, nil
	}
	if api.IsUnavailable(err) {
		return http.StatusServiceUnavailable, "a required service is temporarily unavailable, try again later", nil
	}
	return http.StatusInternalServerError, "the asset could not be updated", nil
}

// UnknownTags implements TagCatalog without a catalog: every well-formed
// key counts as a catalog tag.
func (r *MemoryRepository) UnknownTags(_ context.Context, _ string, keys []string) ([]string, error) {
	var unknown []string
	for _, key := range keys {
		if !tagKeyPattern.MatchString(key) {
			unknown = append(unknown, key)
		}
	}
	return unknown, nil
}

// Bulk handles POST /api/v1/assets/bulk (API-08, BLK-01, BLK-02): below
// 100 ids the report per id (200); from 100 ids on 202 with the job
// asset.bulk whose result is the report; export 202 with its export job.
func (h *Handler) Bulk(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req BulkRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	result, job, err := h.RunBulk(r.Context(), t.OrganizationID, &req)
	var refused *featureNotEntitledError
	switch {
	case errors.Is(err, ci.ErrBulkActionUnavailable):
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", ci.ErrBulkActionUnavailable.Error())
	case errors.As(err, &refused):
		api.WriteTypedProblem(w, http.StatusForbidden, err)
	case err != nil:
		writeAssetError(w, r, err)
	case job != nil:
		w.Header().Set("Location", ci.BulkJobLocation(job))
		api.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status, "kind": job.Kind})
	default:
		api.WriteJSON(w, http.StatusOK, result)
	}
}
