package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// REST of the monitoring profiles and monitoring groups and the effective
// values of a probe (PRB-01, WP-946, E-160, E-178):
//
//	GET    /api/v1/monitoring/profiles                         list (level, level_id, probe_type)
//	POST   /api/v1/monitoring/profiles                         create
//	GET    /api/v1/monitoring/profiles/{id}                    read
//	PATCH  /api/v1/monitoring/profiles/{id}                    replace params
//	DELETE /api/v1/monitoring/profiles/{id}                    delete
//	GET    /api/v1/monitoring/groups                           list (client_id)
//	POST   /api/v1/monitoring/groups                           create
//	GET    /api/v1/monitoring/groups/{id}                      read
//	PATCH  /api/v1/monitoring/groups/{id}                      name, priority
//	DELETE /api/v1/monitoring/groups/{id}                      delete with its profiles
//	GET    /api/v1/monitoring/groups/{id}/members              members
//	POST   /api/v1/monitoring/groups/{id}/members              add up to 500 CIs
//	DELETE /api/v1/monitoring/groups/{id}/members/{ci_id}      remove a CI
//	GET    /api/v1/monitoring/probes/{id}/effective            effective values with origin
//
// A profile write checks the override set against the type limits
// (ValidateLayer: unknown or planned type, value outside the limits,
// timeout not below the interval, 422 with field) and the interval against
// min_probe_interval_s of the plan (CheckInterval, 422 interval_s); the
// level target must be visible (404), one profile per level, target and
// type (409); an org profile is written only by an org-wide principal
// (403). Every write on profiles and groups takes the budget lock first
// (database.LockQuota(ctx, org, "probe_budget"), WP-940), counts the probe
// budget of the organization before and after the change in the same
// transaction and refuses a raise above a quota with 403 entitlement-limit
// (AllowProbeChange) without partial change; afterwards it raises the
// config_version of every collector a probe in the scope of the write is
// assigned to (WP-948 hands out the new set on the next request). The route
// audit writes the snapshot of the row; no outbox event (the register of
// WP-867 has no type for profiles and groups).

// MaxGroupMembersPerRequest bounds the CIs of one POST /members.
const MaxGroupMembersPerRequest = 500

// maxGroupName bounds the name of a monitoring group
// (monitoring_group_name_check).
const maxGroupName = 200

// Errors of the profile and group writes; writeMonitoringError maps them.
var (
	// ErrProfileNotFound: missing or invisible profile (404).
	ErrProfileNotFound = errors.New("monitoring profile not found")
	// ErrGroupNotFound: missing or invisible monitoring group (404).
	ErrGroupNotFound = errors.New("monitoring group not found")
	// ErrLevelNotFound: the level target (client, group, CI) is missing,
	// of another organization or outside the caller's scope (404).
	ErrLevelNotFound = errors.New("level target not found")
	// ErrCINotFound: a CI of a membership is missing or invisible (404).
	ErrCINotFound = errors.New("ci not found")
	// ErrMemberNotFound: the CI is no member of the group (404).
	ErrMemberNotFound = errors.New("ci is no member of the monitoring group")
	// ErrProfileTaken: one profile per level, target and type (409).
	ErrProfileTaken = &api.ConflictError{Field: "level_id", Message: "a profile of this level, target and probe type already exists"}
	// ErrGroupNameTaken: the group name is unique per organization (409).
	ErrGroupNameTaken = &api.ConflictError{Field: "name", Message: "a monitoring group of this name already exists"}
	// errOrgWideOnly: org-wide rows are written only by principals without
	// client and site restriction (E-09, 403).
	errOrgWideOnly = &api.FieldError{Field: "level", Status: http.StatusForbidden, Code: "org_wide_only",
		Message: "only a principal without client and site restriction writes org-wide settings"}
	// errMemberOfOtherClient: a CI of another client than the group's
	// (monitoring_group_member_client_rule, 422).
	errMemberOfOtherClient = unprocessable("ci_ids", "a monitoring group of a client takes only CIs of that client")
)

// ProfileFilter narrows ListProfiles; empty fields do not filter.
type ProfileFilter struct {
	Level     string
	LevelID   string
	ProbeType string
	Limit     int
	Offset    int
}

// GroupUpdate is a PATCH of a monitoring group; nil leaves a field.
type GroupUpdate struct {
	Name     *string
	Priority *int
}

// ProfileStore keeps monitoring profiles and monitoring groups (WP-946);
// PGRepository and MemoryRepository implement it with the same rules. Every
// call runs in the caller's tenant scope.
type ProfileStore interface {
	ListProfiles(ctx context.Context, f *ProfileFilter) ([]MonitoringProfile, int, error)
	GetProfile(ctx context.Context, id string) (*MonitoringProfile, error)
	CreateProfile(ctx context.Context, p *MonitoringProfile) (*MonitoringProfile, error)
	UpdateProfile(ctx context.Context, id string, params json.RawMessage) (*MonitoringProfile, error)
	DeleteProfile(ctx context.Context, id string) error
	ListGroups(ctx context.Context, clientID string, limit, offset int) ([]MonitoringGroup, int, error)
	GetGroup(ctx context.Context, id string) (*MonitoringGroup, error)
	CreateGroup(ctx context.Context, g *MonitoringGroup) (*MonitoringGroup, error)
	UpdateGroup(ctx context.Context, id string, u *GroupUpdate) (*MonitoringGroup, error)
	// DeleteGroup deletes the group with its memberships and profiles and
	// returns the number of deleted profiles.
	DeleteGroup(ctx context.Context, id string) (int, error)
	ListGroupMembers(ctx context.Context, groupID string, limit, offset int) ([]MonitoringGroupMember, int, error)
	// AddGroupMembers adds the CIs (existing members stay) and returns the
	// memberships of the named CIs.
	AddGroupMembers(ctx context.Context, groupID string, ciIDs []string) ([]MonitoringGroupMember, error)
	RemoveGroupMember(ctx context.Context, groupID, ciID string) error
}

// profileView is a monitoring profile as the API returns it: level_id is
// the target of the level (null at org).
type profileView struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	Level          string          `json:"level"`
	LevelID        *string         `json:"level_id"`
	ProbeType      string          `json:"probe_type"`
	Params         json.RawMessage `json:"params"`
	ClientID       string          `json:"client_id,omitempty"`
	SiteID         string          `json:"site_id,omitempty"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

func viewOf(p *MonitoringProfile) profileView {
	v := profileView{ID: p.ID, OrganizationID: p.OrganizationID, Level: p.Level, ProbeType: p.ProbeType,
		Params: normalizeParams(p.Params), ClientID: p.ClientID, SiteID: p.SiteID, UpdatedAt: p.UpdatedAt}
	if id := p.TargetID(); id != "" {
		v.LevelID = &id
	}
	return v
}

// registerProfileRoutes mounts the profile, group and effective routes
// (called by RegisterRoutes).
func (h *Handler) registerProfileRoutes(r chi.Router) {
	r.Get("/api/v1/monitoring/probes/{id}/effective", h.effective)
	r.Get("/api/v1/monitoring/profiles", h.listProfiles)
	r.Post("/api/v1/monitoring/profiles", h.createProfile)
	r.Get("/api/v1/monitoring/profiles/{id}", h.getProfile)
	r.Patch("/api/v1/monitoring/profiles/{id}", h.updateProfile)
	r.Delete("/api/v1/monitoring/profiles/{id}", h.deleteProfile)
	r.Get("/api/v1/monitoring/groups", h.listGroups)
	r.Post("/api/v1/monitoring/groups", h.createGroup)
	r.Get("/api/v1/monitoring/groups/{id}", h.getGroup)
	r.Patch("/api/v1/monitoring/groups/{id}", h.updateGroup)
	r.Delete("/api/v1/monitoring/groups/{id}", h.deleteGroup)
	r.Get("/api/v1/monitoring/groups/{id}/members", h.listGroupMembers)
	r.Post("/api/v1/monitoring/groups/{id}/members", h.addGroupMembers)
	r.Delete("/api/v1/monitoring/groups/{id}/members/{ci_id}", h.removeGroupMember)
}

// profiles returns the profile store; 501 when the store has none.
func (h *Handler) profiles(w http.ResponseWriter, r *http.Request) (ProfileStore, bool) {
	if !requireOrg(w, r) {
		return nil, false
	}
	ps, ok := h.store.(ProfileStore)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "monitoring profiles are not available")
		return nil, false
	}
	return ps, true
}

// writeMonitoringError maps the errors of the profile and group store.
func writeMonitoringError(w http.ResponseWriter, err error) {
	for _, nf := range []struct {
		err    error
		detail string
	}{
		{ErrProfileNotFound, "monitoring profile not found"}, {ErrGroupNotFound, "monitoring group not found"},
		{ErrLevelNotFound, "the target of the level was not found"}, {ErrCINotFound, "ci not found"},
		{ErrMemberNotFound, "the ci is no member of the monitoring group"},
	} {
		if errors.Is(err, nf.err) {
			api.WriteError(w, http.StatusNotFound, "Not Found", nf.detail)
			return
		}
	}
	writeError(w, err)
}

// orgWide reports whether the caller has no client and site restriction.
func orgWide(ctx context.Context) bool {
	s, ok := database.TenantScopeFromContext(ctx)
	return ok && s.Clients.IsAll() && s.Sites.IsAll()
}

// validLevel reports whether level is a profile level.
func validLevel(level string) bool {
	for _, l := range ProfileLevels {
		if l == level {
			return true
		}
	}
	return false
}

// checkProfileParams checks the override set of a profile for the type: the
// type limits and the parameter schema (422 with field, the type field
// named probe_type).
func checkProfileParams(probeType string, params json.RawMessage) error {
	t, ok := probewire.Lookup(probeType)
	switch {
	case probeType == "":
		return &ValidationError{Violations: []probewire.FieldError{{Field: "probe_type", Message: "probe_type is required"}}}
	case !ok:
		return &ValidationError{Violations: []probewire.FieldError{{Field: "probe_type", Message: fmt.Sprintf("unknown probe type %q", probeType)}}}
	case !t.Available():
		return &ValidationError{Violations: []probewire.FieldError{{Field: "probe_type",
			Message: fmt.Sprintf("probe type %s is planned and cannot be used yet", probeType)}}}
	}
	errs := ValidateLayer(probeType, params)
	for i := range errs {
		if errs[i].Field == "type" {
			errs[i].Field = "probe_type"
		}
	}
	if len(errs) > 0 {
		return &ValidationError{Violations: errs}
	}
	return nil
}

// profileInterval returns interval_s of an override set (ok false without
// one).
func profileInterval(params json.RawMessage) (int64, bool) {
	o := ParseOverrides(params)
	n, ok := o[KeyIntervalS].(json.Number)
	if !ok {
		return 0, false
	}
	v, err := n.Int64()
	return v, err == nil
}

// checkPlanInterval refuses an interval_s below min_probe_interval_s of
// the plan (PKG-04, WP-946 AK6; Service.CheckInterval, 422 interval_s).
func checkPlanInterval(ctx context.Context, gate BudgetGate, orgID string, params json.RawMessage) error {
	iv, ok := profileInterval(params)
	if gate == nil || !ok {
		return nil
	}
	return gate.CheckInterval(ctx, orgID, int(max(min(iv, 1<<31-1), -1<<31)))
}

// decodeParams reads params of a profile body: a JSON object ({} for
// none).
func decodeParams(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, &ValidationError{Violations: []probewire.FieldError{{Field: "params", Message: "params is required"}}}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, &ValidationError{Violations: []probewire.FieldError{{Field: "params", Message: "params must be a JSON object"}}}
	}
	return raw, nil
}

func (h *Handler) effective(w http.ResponseWriter, r *http.Request) {
	if !requireOrg(w, r) {
		return
	}
	p, err := h.store.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, err)
		return
	}
	layers, err := h.store.Layers(r.Context(), p)
	if err != nil {
		writeError(w, err)
		return
	}
	e := Resolve(p, layers)
	if e.Thresholds == nil {
		e.Thresholds = map[string]Field{}
	}
	if e.Params == nil {
		e.Params = map[string]Field{}
	}
	api.WriteJSON(w, http.StatusOK, e)
}

func (h *Handler) listProfiles(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := &ProfileFilter{Level: q.Get("level"), LevelID: q.Get("level_id"), ProbeType: q.Get("probe_type"),
		Limit: page.Limit, Offset: page.Offset}
	if f.Level != "" && !validLevel(f.Level) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "level must be org, client, group or ci")
		return
	}
	if f.LevelID != "" && !api.IsUUID(f.LevelID) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "level_id must be a UUID")
		return
	}
	items, total, err := ps.ListProfiles(r.Context(), f)
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	out := make([]profileView, 0, len(items))
	for i := range items {
		out = append(out, viewOf(&items[i]))
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[profileView]{Data: out, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+len(out) < total})
}

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	p, err := ps.GetProfile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, viewOf(p))
}

// profileCreateRequest is the body of POST /monitoring/profiles.
type profileCreateRequest struct {
	Level     string          `json:"level"`
	LevelID   *string         `json:"level_id"`
	ProbeType string          `json:"probe_type"`
	Params    json.RawMessage `json:"params"`
}

func (h *Handler) createProfile(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	var req profileCreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	p, err := profileFromRequest(&req)
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	p.OrganizationID = tenant.FromContext(r.Context()).OrganizationID
	created, err := ps.CreateProfile(r.Context(), p)
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, viewOf(created))
}

// profileFromRequest checks what needs no read: level and level_id, the
// override set against the type.
func profileFromRequest(req *profileCreateRequest) (*MonitoringProfile, error) {
	level := strings.TrimSpace(req.Level)
	if !validLevel(level) {
		return nil, unprocessable("level", "level must be org, client, group or ci")
	}
	levelID := ""
	if req.LevelID != nil {
		levelID = *req.LevelID
	}
	switch {
	case level == LevelOrg && levelID != "":
		return nil, unprocessable("level_id", "level_id must be empty at level org")
	case level != LevelOrg && levelID == "":
		return nil, unprocessable("level_id", "level_id is required at level "+level)
	case level != LevelOrg && !api.IsUUID(levelID):
		return nil, ErrLevelNotFound
	}
	params, err := decodeParams(req.Params)
	if err != nil {
		return nil, err
	}
	probeType := strings.TrimSpace(req.ProbeType)
	if err := checkProfileParams(probeType, params); err != nil {
		return nil, err
	}
	p := &MonitoringProfile{Level: level, ProbeType: probeType, Params: params}
	switch level {
	case LevelClient:
		p.ClientID = levelID
	case LevelGroup:
		p.GroupID = levelID
	case LevelCI:
		p.CIID = levelID
	}
	return p, nil
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	var req struct {
		Params json.RawMessage `json:"params"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body; only params can be changed")
		return
	}
	params, err := decodeParams(req.Params)
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	updated, err := ps.UpdateProfile(r.Context(), chi.URLParam(r, "id"), params)
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, viewOf(updated))
}

func (h *Handler) deleteProfile(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	if err := ps.DeleteProfile(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeMonitoringError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
