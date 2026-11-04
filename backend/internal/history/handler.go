package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for unified history and point-in-time state.
type Handler struct {
	repo  Repository
	jobs  SnapshotJobs
	queue SnapshotQueue
	// cis and creatorPolicy serve the field permissions (WP-727).
	cis           CILookup
	creatorPolicy CreatorPolicy
}

// SnapshotJobKind is the job kind of a state snapshot (JOB-04).
const SnapshotJobKind = "history.snapshot"

// SnapshotJobs runs state snapshots as jobs; jobs.Service implements it.
type SnapshotJobs interface {
	RunDetached(ctx context.Context, orgID, kind string, params any, createdBy string,
		run func(ctx context.Context) (any, error)) (*jobs.Job, error)
}

// WithJobs makes POST /history/snapshots a job (JOB-04).
func (h *Handler) WithJobs(j SnapshotJobs) *Handler {
	h.jobs = j
	return h
}

// SnapshotQueue submits state snapshots to the job queue on NATS (TEC-14),
// whose workers run them with RunSnapshotJob; jobs.Queue implements it.
type SnapshotQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// WithQueue runs the snapshot jobs on the job queue instead of in this
// process: a job outlives the replica that accepted it.
func (h *Handler) WithQueue(q SnapshotQueue) *Handler {
	h.queue = q
	return h
}

// NewHandler creates a new history handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers history routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/history/{entityType}/{id}", h.List)
	r.Get("/api/v1/assets/{id}/state", h.AssetState)
	r.Get("/api/v1/cis/{id}/state", h.CIState)
	r.Post("/api/v1/history/snapshots", h.CreateSnapshot)
}

// SnapshotRequest asks for the state of an entity at a point in time.
type SnapshotRequest struct {
	EntityType string     `json:"entity_type"`
	EntityID   string     `json:"entity_id"`
	At         *time.Time `json:"at,omitempty"`
}

// CreateSnapshot handles POST /api/v1/history/snapshots: the state
// snapshot runs as a job (JOB-04, E-25) whose result is the replayed state;
// the answer is 202 with the job. The job runs on the job queue when there
// is one (TEC-14), otherwise in the background of this process. Without the
// job model the snapshot is answered at once (200). An entity the caller
// cannot see (unknown, of another organization, client or site) is 404
// before a job exists; without a recorded change up to at the job fails
// (F-164).
func (h *Handler) CreateSnapshot(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req SnapshotRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.EntityType != "ci" && req.EntityType != "asset" {
		api.WriteFieldError(w, &api.FieldError{Field: "entity_type", Message: "must be ci or asset", Status: http.StatusUnprocessableEntity})
		return
	}
	if req.EntityID == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "entity_id", Message: "is required"})
		return
	}
	if err := api.RequireUUID("entity_id", req.EntityID); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	at := time.Now().UTC()
	if req.At != nil {
		at = req.At.UTC()
	}
	orgID := t.OrganizationID
	visible, err := h.repo.Visible(r.Context(), orgID, req.EntityType, req.EntityID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if !visible {
		api.WriteRepoError(w, api.ErrNotFound)
		return
	}
	params := SnapshotRequest{EntityType: req.EntityType, EntityID: req.EntityID, At: &at}
	// A CI snapshot is projected with the policy of the caller (WP-727),
	// resolved before the job runs detached from the request.
	var policy *permission.FieldPolicy
	if req.EntityType == "ci" {
		if policy, err = permission.PolicyFrom(r.Context()); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	run := func(ctx context.Context) (any, error) { return h.projectedSnapshot(ctx, orgID, &params, policy) }
	if h.jobs == nil && h.queue == nil {
		snapshot, serr := run(r.Context())
		if serr != nil {
			writeSnapshotError(w, serr)
			return
		}
		api.WriteJSON(w, http.StatusOK, snapshot)
		return
	}
	var job *jobs.Job
	if h.queue != nil {
		job, _, err = h.queue.Submit(r.Context(), orgID, SnapshotJobKind, params, jobs.EnqueueOptions{CreatedBy: t.UserID})
	} else {
		job, err = h.jobs.RunDetached(r.Context(), orgID, SnapshotJobKind, params, t.UserID, run)
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status, "kind": job.Kind})
}

// snapshot replays the state of the entity of req at req.At from its
// change trail (TrailUpTo). An entity the caller cannot see is
// api.ErrNotFound; one without a recorded change up to req.At (before its
// creation or before the first recorded change) is a
// *NoRecordedStateError, not an empty state (F-164).
func (h *Handler) snapshot(ctx context.Context, orgID string, req *SnapshotRequest) (*StateSnapshot, error) {
	visible, err := h.repo.Visible(ctx, orgID, req.EntityType, req.EntityID)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, api.ErrNotFound
	}
	trail, err := h.repo.TrailUpTo(ctx, orgID, req.EntityType, req.EntityID, *req.At)
	if err != nil {
		return nil, err
	}
	if len(trail) == 0 {
		return nil, &NoRecordedStateError{At: *req.At}
	}
	snapshot := Replay(trail)
	snapshot.EntityType, snapshot.EntityID, snapshot.At = req.EntityType, req.EntityID, *req.At
	return &snapshot, nil
}

// projectedSnapshot is the snapshot of req, for a CI projected with policy
// (nil: the structural fields only, WP-727).
func (h *Handler) projectedSnapshot(ctx context.Context, orgID string, req *SnapshotRequest, policy *permission.FieldPolicy) (*StateSnapshot, error) {
	snapshot, err := h.snapshot(ctx, orgID, req)
	if err != nil || req.EntityType != "ci" {
		return snapshot, err
	}
	p, err := h.projection(ctx, orgID, req.EntityID, policy)
	if err != nil {
		return nil, err
	}
	if p != nil {
		p.state(snapshot)
	}
	return snapshot, nil
}

// writeSnapshotError answers a failed state snapshot: an invisible entity
// and one without a recorded state are 404.
func writeSnapshotError(w http.ResponseWriter, err error) {
	var none *NoRecordedStateError
	if errors.As(err, &none) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "no recorded state before "+none.At.UTC().Format(time.RFC3339Nano))
		return
	}
	api.WriteRepoError(w, err)
}

// RunSnapshotJob runs a snapshot job of the job queue (handler of
// history.snapshot) with the tenant and scope of its creator
// (jobs.CreatorContext) and projects a CI snapshot with the field policy
// of the creator (WithCreatorPolicy, WP-727); it only reads, so a repeated
// attempt is harmless.
func (h *Handler) RunSnapshotJob(ctx context.Context, job *jobs.Job) (any, error) {
	var req SnapshotRequest
	if err := json.Unmarshal(job.Params, &req); err != nil {
		return nil, fmt.Errorf("decode snapshot job parameters: %w", err)
	}
	if (req.EntityType != "ci" && req.EntityType != "asset") || req.EntityID == "" || req.At == nil {
		return nil, fmt.Errorf("invalid snapshot job parameters: %s/%s", req.EntityType, req.EntityID)
	}
	ctx = jobs.CreatorContext(ctx, job)
	var policy *permission.FieldPolicy
	if req.EntityType == "ci" {
		var err error
		if policy, err = h.creatorPolicyOf(ctx, job); err != nil {
			return nil, err
		}
	}
	return h.projectedSnapshot(ctx, job.OrganizationID, &req, policy)
}

// ListResponse is a page of a history, api.ListResponse plus
// AuditIncluded: whether the audit entries of the entity are part of it
// (E-78). It sets next_cursor itself, since WriteJSON fills it only for an
// api.ListResponse.
type ListResponse struct {
	Data          []Change `json:"data"`
	Total         int      `json:"total"`
	Limit         int      `json:"limit"`
	Offset        int      `json:"offset"`
	HasMore       bool     `json:"has_more"`
	NextCursor    string   `json:"next_cursor,omitempty"`
	AuditIncluded bool     `json:"audit_included"`
}

// List handles GET /api/v1/history/{entityType}/{id}. The route needs
// ci:read; the history of an entity type needs its read permission, and the
// audit entries of a CI are part of it only with audit:read (CI-06, E-78).
// The trail is read under the caller's tenant scope. A CI change is one
// entry per change set with its field_changes; change_kind keeps the CI
// changes of one kind, an unknown kind or another entity type is 400
// (AUD-03).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	entityType := chi.URLParam(r, "entityType")
	required, ok := readPermissions[entityType]
	if !ok {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "unsupported entity type")
		return
	}
	principal, ok := identity.PrincipalFromContext(r.Context())
	if !ok || !principal.Has(required) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing required permission: "+string(required))
		return
	}
	opts := ListOptions{IncludeAudit: entityType == "ci" && principal.Has(identity.PermAuditRead)}
	if q := r.URL.Query(); q.Has("change_kind") {
		opts.ChangeKind = q.Get("change_kind")
		if !ChangeKinds[opts.ChangeKind] {
			api.WriteFieldError(w, &api.FieldError{Field: "change_kind",
				Message: "must be one of created, updated, deleted, restored, merged, type_changed, relationship_added, relationship_removed, status_changed"})
			return
		}
		if entityType != "ci" {
			api.WriteFieldError(w, &api.FieldError{Field: "change_kind", Message: "filters the history of a ci only"})
			return
		}
	}
	page := api.ParsePagination(r)
	id := chi.URLParam(r, "id")
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, entityType, id, page, opts)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	// The values of the fields the principal may not read are left out
	// (WP-727); total stays the number of entries.
	if entityType == "ci" {
		if items, err = h.projectList(r.Context(), t.OrganizationID, id, items); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	// An entity without changes answers "data": [], not null (F-401).
	resp := ListResponse{
		Data: api.NonNilSlice(items), Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total, AuditIncluded: opts.IncludeAudit,
	}
	if resp.HasMore && len(items) > 0 {
		resp.NextCursor = api.EncodeOffsetCursor(page.Offset + len(items))
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// AssetState handles GET /api/v1/assets/{id}/state?at=...
func (h *Handler) AssetState(w http.ResponseWriter, r *http.Request) {
	h.state(w, r, "asset")
}

// CIState handles GET /api/v1/cis/{id}/state?at=...
func (h *Handler) CIState(w http.ResponseWriter, r *http.Request) {
	h.state(w, r, "ci")
}

// state reconstructs the entity state at the requested point in time
// (spec §16). Without an "at" parameter the current replay of the full trail
// is returned. An entity the caller cannot see and one without a recorded
// change up to at are 404 (JOB-04, F-164).
func (h *Handler) state(w http.ResponseWriter, r *http.Request, entityType string) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	at := time.Now().UTC()
	if raw := r.URL.Query().Get("at"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid at timestamp (RFC3339 expected)")
			return
		}
		at = parsed
	}
	// The state of a CI carries only the fields the principal may read
	// (WP-727).
	var policy *permission.FieldPolicy
	if entityType == "ci" {
		var err error
		if policy, err = permission.PolicyFrom(r.Context()); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	snapshot, err := h.projectedSnapshot(r.Context(), t.OrganizationID,
		&SnapshotRequest{EntityType: entityType, EntityID: chi.URLParam(r, "id"), At: &at}, policy)
	if err != nil {
		writeSnapshotError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, snapshot)
}

// readPermissions lists the entity types with a history and the read
// permission each one's history needs (E-78); the locations are the common
// tree of type location (E-19), the retired location_node has none (F-226).
var readPermissions = map[string]identity.Permission{
	"ci":           identity.PermCIRead,
	"relationship": identity.PermRelationshipRead,
	"location":     identity.PermLocationRead,
	"asset":        identity.PermAssetRead,
	"reservation":  identity.PermAssetRead,
	"composition":  identity.PermAssetRead,
}

// Field permissions of the CI history (RBA-07, E-43, WP-727): GET
// /history/ci/{id}, GET /cis/{id}/state and the result of the snapshot job
// carry no value of a field the principal may not read. The type and scope
// of the CI come from the CI lookup (WithCIs); a CI it does not find
// (deleted, unknown) or a handler without lookup leaves only the
// structural fields under a restricted policy (fail-closed). The
// projection follows the CI DTO (permission.Readable): typed columns and
// attributes are projected, the instance attributes of the CI as
// _instance.<key>, the structural members stay. It reads nothing but the
// facts of the CI.

// CIFacts are the type, the scope and the instance attribute keys of a CI.
type CIFacts struct {
	TypeID       string
	ClientID     string
	SiteID       string
	InstanceKeys []string
}

// CILookup reads the facts of a CI the caller sees; nil without error for
// one it does not find.
type CILookup interface {
	CIFacts(ctx context.Context, orgID, ciID string) (*CIFacts, error)
}

// WithCIs gives the handler the type and scope of the CIs whose history
// it projects.
func (h *Handler) WithCIs(l CILookup) *Handler {
	h.cis = l
	return h
}

// CreatorPolicy resolves the field policy of the user who created a
// snapshot job: the queue runs it without principal (jobs.CreatorContext),
// so the result is projected with the policy of the creator
// (permission.FieldResolver.PolicyForGrants with the creator's
// AccessGrants).
type CreatorPolicy func(ctx context.Context, orgID, userID string) (*permission.FieldPolicy, error)

// WithCreatorPolicy sets the resolver of the creator's policy. Without it
// a queued CI snapshot keeps only its structural fields (fail-closed).
func (h *Handler) WithCreatorPolicy(p CreatorPolicy) *Handler {
	h.creatorPolicy = p
	return h
}

// creatorPolicyOf returns the field policy of the creator of job: nil
// (structural fields only) without resolver or when the creator may not
// read CIs at all.
func (h *Handler) creatorPolicyOf(ctx context.Context, job *jobs.Job) (*permission.FieldPolicy, error) {
	if h.creatorPolicy == nil {
		return nil, nil
	}
	policy, err := h.creatorPolicy(ctx, job.OrganizationID, job.CreatedBy)
	if err != nil {
		return nil, fmt.Errorf("resolve the field permissions of the job creator: %w", err)
	}
	if !policy.Level(permission.KindCI, "", "", permission.ObjectRef{}).Readable() {
		return nil, nil
	}
	return policy, nil
}

// ciProjection projects the history values of one CI.
type ciProjection struct {
	// policy nil reads only the structural fields.
	policy *permission.FieldPolicy
	typeID string
	ref    permission.ObjectRef
	keys   []string
}

// projection returns the projection of the CI's history for policy; nil
// when the policy restricts nothing.
func (h *Handler) projection(ctx context.Context, orgID, ciID string, policy *permission.FieldPolicy) (*ciProjection, error) {
	if !permission.NeedsProjection(policy, permission.KindCI) {
		return nil, nil
	}
	p := &ciProjection{ref: permission.ObjectRef{ID: ciID}}
	if h.cis == nil {
		return p, nil
	}
	facts, err := h.cis.CIFacts(ctx, orgID, ciID)
	if err != nil {
		return nil, err
	}
	if facts == nil {
		return p, nil
	}
	p.typeID, p.keys = facts.TypeID, facts.InstanceKeys
	p.ref.ClientID, p.ref.SiteID = facts.ClientID, facts.SiteID
	if policy.Level(permission.KindCI, facts.TypeID, "", p.ref).Readable() {
		p.policy = policy
	}
	return p, nil
}

// readable reports whether the DTO path of the CI is readable.
func (p *ciProjection) readable(path string, opts ...permission.ProjectOption) bool {
	if len(opts) == 0 {
		opts = []permission.ProjectOption{permission.WithInstanceKeys(p.keys)}
	}
	return permission.Readable(p.policy, permission.KindCI, p.typeID, p.ref, path, opts...)
}

// field reports whether the field of the bare name the change history, the
// overrides and the findings use is readable; attributes are projected per
// key.
func (p *ciProjection) field(name string) bool {
	if name == memberAttributes {
		return true
	}
	if strings.HasPrefix(name, memberAttributes+".") {
		return p.readable(name)
	}
	return p.readable(permission.CIFieldPath(name))
}

// Members of the history values with a projection of their own.
const (
	memberAttributes = "attributes"
	memberAsset      = "asset"
	namespaceInst    = "_instance"
)

// projectedNamespaces are the stored attribute namespaces keyed by field
// name (CI-10).
var projectedNamespaces = map[string]bool{"_overrides": true, "_observed": true, "_provenance": true}

// attributes projects an attributes value per key: the stored instance
// values (_instance) as instance attributes, the projected namespaces by
// their field names; a value that is no object is left out.
func (p *ciProjection) attributes(value any) any {
	attrs, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		switch {
		case k == namespaceInst || projectedNamespaces[k]:
			inner, isMap := v.(map[string]any)
			if !isMap {
				continue
			}
			kept := map[string]any{}
			for name, iv := range inner {
				if (k == namespaceInst && p.readable("attributes."+name, permission.WithInstanceKeys([]string{name}))) ||
					(k != namespaceInst && p.field(name)) {
					kept[name] = iv
				}
			}
			out[k] = kept
		case p.readable("attributes." + k):
			out[k] = v
		}
	}
	return out
}

// object projects a CI as the history keeps it (the value of a create or
// delete, the audit before and after, the fields of a state): the
// attributes per key, the asset facts only without a restriction of the
// asset policy (the asset is not known), every other member by its DTO
// path; a value that is no object stays as it is.
func (p *ciProjection) object(value any) any {
	obj, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := make(map[string]any, len(obj))
	for member, v := range obj {
		switch {
		case member == memberAttributes:
			if v != nil {
				v = p.attributes(v)
			}
			out[member] = v
		case member == memberAsset:
			if v == nil || !p.policy.Restricted(permission.KindAsset) {
				out[member] = v
			}
		case p.readable(member):
			out[member] = v
		}
	}
	return out
}

// fieldChanges keeps the readable elements of a change set, the
// attributes projected per key.
func (p *ciProjection) fieldChanges(changes []FieldChange) []FieldChange {
	if changes == nil {
		return nil
	}
	out := make([]FieldChange, 0, len(changes))
	for _, fc := range changes {
		switch {
		case fc.Field == memberAttributes:
			fc.Old, fc.New = p.attributes(fc.Old), p.attributes(fc.New)
		case !p.field(fc.Field):
			continue
		}
		out = append(out, fc)
	}
	return out
}

// names keeps the readable entries of a list naming fields (fields of a
// route audit, override conflicts, overwritten fields, defaults) or
// elements with a field (the field changes of an update audit, the values
// of a route audit, findings); a value that is no list is left out.
func (p *ciProjection) names(value any) any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(list))
	for _, v := range list {
		switch e := v.(type) {
		case string:
			if p.field(e) {
				out = append(out, e)
			}
		case map[string]any:
			if kept, keep := p.element(e); keep {
				out = append(out, kept)
			}
		}
	}
	return out
}

// element projects an element naming a field ({field, old_value,
// new_value} of an update audit, {field, old, new} of a route audit,
// {field, message} of a finding): left out for an unreadable field or
// without field, the values of attributes projected per key.
func (p *ciProjection) element(e map[string]any) (map[string]any, bool) {
	name, _ := e["field"].(string)
	if name == "" || !p.field(name) {
		return nil, false
	}
	if name != memberAttributes {
		return e, true
	}
	out := make(map[string]any, len(e))
	for k, v := range e {
		if k != "field" {
			v = p.attributes(v)
		}
		out[k] = v
	}
	return out, true
}

// auditListMembers are the members of a CI audit entry's changes that list
// fields or name a field per element.
var auditListMembers = map[string]bool{
	"fields": true, "override_conflicts": true, "overwritten_fields": true, "defaults": true,
	"findings": true, "values": true,
}

// audit projects the changes of an audit entry about the CI itself: before
// and after like a CI, the lists of fields to the readable ones.
func (p *ciProjection) audit(value any) any {
	changes, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := make(map[string]any, len(changes))
	for k, v := range changes {
		switch {
		case k == "before" || k == "after":
			out[k] = p.object(v)
		case auditListMembers[k] && v != nil:
			out[k] = p.names(v)
		default:
			out[k] = v
		}
	}
	return out
}

// changes projects the history entries of the CI ciID: an entry of an
// unreadable field is left out, the values of attributes, create and
// delete entries are projected, the audit entries about the CI itself as
// their changes; the moves of the CI's location carry no CI field, the
// audit entries of its contact links and interfaces no value of the CI.
func (p *ciProjection) changes(ciID string, items []Change) []Change {
	out := make([]Change, 0, len(items))
	for i := range items {
		c := items[i]
		switch {
		case c.Source == SourceLocationChange:
		case c.Source == SourceAudit:
			if c.EntityID == ciID {
				c.OldValue, c.NewValue = p.audit(c.OldValue), p.audit(c.NewValue)
			}
		case c.FieldName == "":
			c.OldValue, c.NewValue = p.object(c.OldValue), p.object(c.NewValue)
		case c.FieldName == memberAttributes:
			c.OldValue, c.NewValue = p.attributes(c.OldValue), p.attributes(c.NewValue)
		case !p.field(c.FieldName):
			continue
		}
		c.FieldChanges = p.fieldChanges(c.FieldChanges)
		out = append(out, c)
	}
	return out
}

// projectList projects the history of the CI ciID with the policy of the
// request.
func (h *Handler) projectList(ctx context.Context, orgID, ciID string, items []Change) ([]Change, error) {
	policy, err := permission.PolicyFrom(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.projection(ctx, orgID, ciID, policy)
	if err != nil || p == nil {
		return items, err
	}
	return p.changes(ciID, items), nil
}

// state projects the fields of a state snapshot.
func (p *ciProjection) state(s *StateSnapshot) {
	if fields, ok := p.object(s.Fields).(map[string]any); ok {
		s.Fields = fields
	}
}
