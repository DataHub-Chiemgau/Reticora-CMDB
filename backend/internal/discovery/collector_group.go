package discovery

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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Collector groups (PRB-03, COL-06, WP-947, E-160, E-174, E-176): the
// collectors of a customer network that share the probes of the group, in
// mode distributed or failover, with a liveness timeout of 20-300 s and an
// optional anchor probe. A collector is in at most one group; every new
// collector gets a group of its own (WP-936).
//
//	GET    /api/v1/collector-groups                               list (client_id, site_id)
//	POST   /api/v1/collector-groups                               create
//	GET    /api/v1/collector-groups/{id}                          read
//	PATCH  /api/v1/collector-groups/{id}                          name, mode, liveness_timeout_s, anchor_probe_id
//	DELETE /api/v1/collector-groups/{id}                          delete (409 while probes run in it)
//	POST   /api/v1/collector-groups/{id}/members                  add or move a collector
//	DELETE /api/v1/collector-groups/{id}/members/{collector_id}   remove a collector
//
// A collector of another client than the group's is 422 collector_id
// (collector_group_member_client_rule); a collector of another group moves
// in one transaction. The last member of a group with probes stays (409).
// The assignment engine (WP-941, job probe.assign) recomputes every group in
// its run; the routes set no mark of their own and write no event (the
// register has only collector_group.unreachable, .reachable and .failover).

// Modes of a collector group (E-174).
const (
	CollectorGroupDistributed = "distributed"
	CollectorGroupFailover    = "failover"
)

// Liveness timeout of a collector group in seconds (E-176, PRB-10).
const (
	DefaultLivenessTimeoutS = 30
	MinLivenessTimeoutS     = 20
	MaxLivenessTimeoutS     = 300
)

// DefaultMemberWeight is the weight of a member without one.
const DefaultMemberWeight = 100

// maxCollectorGroupName bounds the name (collector_group_name_check).
const maxCollectorGroupName = 200

// CollectorGroup is a collector group with its members as the API returns
// it.
type CollectorGroup struct {
	ID               string                 `json:"id"`
	OrganizationID   string                 `json:"organization_id"`
	ClientID         string                 `json:"client_id,omitempty"`
	SiteID           string                 `json:"site_id,omitempty"`
	Name             string                 `json:"name"`
	Mode             string                 `json:"mode"`
	LivenessTimeoutS int                    `json:"liveness_timeout_s"`
	AnchorProbeID    string                 `json:"anchor_probe_id,omitempty"`
	Status           string                 `json:"status"`
	StatusChangedAt  time.Time              `json:"status_changed_at"`
	Members          []CollectorGroupMember `json:"members"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

// CollectorGroupMember is a collector of a group with its liveness and the
// number of probes assigned to it (probe_assignment).
type CollectorGroupMember struct {
	CollectorID    string     `json:"collector_id"`
	GroupID        string     `json:"group_id"`
	Priority       int        `json:"priority"`
	Weight         int        `json:"weight"`
	MemberStatus   string     `json:"member_status"`
	LastAliveAt    *time.Time `json:"last_alive_at,omitempty"`
	ClockOffsetMS  *int       `json:"clock_offset_ms,omitempty"`
	CapacityHint   *int       `json:"capacity_hint,omitempty"`
	ConfigVersion  int64      `json:"config_version"`
	AssignedProbes int        `json:"assigned_probes"`
}

// CollectorGroupFilter narrows the list; empty fields do not filter.
type CollectorGroupFilter struct {
	ClientID string
	SiteID   string
}

// CollectorGroupUpdate is a PATCH; nil leaves a field, an empty
// AnchorProbeID removes the anchor probe.
type CollectorGroupUpdate struct {
	Name             *string
	Mode             *string
	LivenessTimeoutS *int
	AnchorProbeID    *string
}

// CollectorGroupMemberInput is the body of POST /{id}/members.
type CollectorGroupMemberInput struct {
	CollectorID string `json:"collector_id"`
	Priority    *int   `json:"priority"`
	Weight      *int   `json:"weight"`
}

// CollectorGroupRepository stores collector groups; PGRepository and
// MemoryRepository implement it with the same rules. Every call runs in
// the caller's tenant scope (RLS).
type CollectorGroupRepository interface {
	ListCollectorGroups(ctx context.Context, orgID string, f CollectorGroupFilter, page api.PaginationParams) ([]CollectorGroup, int, error)
	GetCollectorGroup(ctx context.Context, orgID, id string) (*CollectorGroup, error)
	CreateCollectorGroup(ctx context.Context, g *CollectorGroup) (*CollectorGroup, error)
	UpdateCollectorGroup(ctx context.Context, orgID, id string, u *CollectorGroupUpdate) (*CollectorGroup, error)
	DeleteCollectorGroup(ctx context.Context, orgID, id string) error
	// AddCollectorGroupMember adds the collector or moves it from its group
	// (or changes priority and weight of a member).
	AddCollectorGroupMember(ctx context.Context, orgID, groupID string, in *CollectorGroupMemberInput) (*CollectorGroup, error)
	RemoveCollectorGroupMember(ctx context.Context, orgID, groupID, collectorID string) error
}

// Errors of the collector groups; the handler maps them.
var (
	// ErrCollectorGroupNotFound: missing or invisible group (404).
	ErrCollectorGroupNotFound = errors.New("collector group not found")
	// ErrCollectorGroupMemberNotFound: the collector is no member (404).
	ErrCollectorGroupMemberNotFound = errors.New("collector is no member of the collector group")
	// ErrLastMemberWithProbes: the last member of a group with probes
	// stays (409).
	ErrLastMemberWithProbes = &api.ConflictError{Field: "collector_id",
		Message: "the last member of a collector group with probes cannot leave it"}
	// errConcurrentMemberMove: a concurrent request moved the collector
	// while this one waited for the locks (409, repeat the request).
	errConcurrentMemberMove = &api.ConflictError{Field: "collector_id",
		Message: "the collector was moved concurrently; repeat the request"}
	errCollectorOfOtherClient = &api.FieldError{Field: "collector_id", Status: http.StatusUnprocessableEntity,
		Message: "collector not found or not a collector of the group's client"}
	errAnchorProbe = &api.FieldError{Field: "anchor_probe_id", Status: http.StatusUnprocessableEntity,
		Message: "anchor probe not found or not a probe of the group's client"}
	errGroupClient = &api.FieldError{Field: "client_id", Status: http.StatusUnprocessableEntity, Message: "client not found"}
	errGroupSite   = &api.FieldError{Field: "site_id", Status: http.StatusUnprocessableEntity,
		Message: "site not found or not a site of the group's client"}
)

// CollectorGroupInUseError refuses the deletion of a group with probes
// (409 with their number).
type CollectorGroupInUseError struct{ Probes int }

// validation: a fixed text with the number of probes.
func (e *CollectorGroupInUseError) Error() string {
	return fmt.Sprintf("%d probes run in the collector group; move or delete them first", e.Probes)
}

// validateCollectorGroup checks name, mode and liveness timeout.
func validateCollectorGroup(name, mode string, liveness int) error {
	switch {
	case strings.TrimSpace(name) == "" || len(name) > maxCollectorGroupName:
		return &api.FieldError{Field: "name", Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("name must not be blank and at most %d characters", maxCollectorGroupName)}
	case mode != CollectorGroupDistributed && mode != CollectorGroupFailover:
		return &api.FieldError{Field: "mode", Status: http.StatusUnprocessableEntity, Message: "mode must be distributed or failover"}
	case liveness < MinLivenessTimeoutS || liveness > MaxLivenessTimeoutS:
		return &api.FieldError{Field: "liveness_timeout_s", Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("liveness_timeout_s must be between %d and %d", MinLivenessTimeoutS, MaxLivenessTimeoutS)}
	}
	return nil
}

// collectorGroupRepo returns the repository of the collector groups, nil
// when the repository has none.
func (h *Handler) collectorGroupRepo(w http.ResponseWriter, r *http.Request) (CollectorGroupRepository, string, bool) {
	orgID := tenant.FromContext(r.Context()).OrganizationID
	if orgID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return nil, "", false
	}
	repo, ok := h.repo.(CollectorGroupRepository)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "collector groups are not available")
		return nil, "", false
	}
	return repo, orgID, true
}

// RegisterCollectorGroupRoutes mounts the collector group routes.
func (h *Handler) RegisterCollectorGroupRoutes(r chi.Router) {
	r.Get("/api/v1/collector-groups", h.listCollectorGroups)
	r.Post("/api/v1/collector-groups", h.createCollectorGroup)
	r.Get("/api/v1/collector-groups/{id}", h.getCollectorGroup)
	r.Patch("/api/v1/collector-groups/{id}", h.updateCollectorGroup)
	r.Delete("/api/v1/collector-groups/{id}", h.deleteCollectorGroup)
	r.Post("/api/v1/collector-groups/{id}/members", h.addCollectorGroupMember)
	r.Delete("/api/v1/collector-groups/{id}/members/{collector_id}", h.removeCollectorGroupMember)
}

// writeCollectorGroupError maps the errors of the collector groups.
func writeCollectorGroupError(w http.ResponseWriter, err error) {
	var inUse *CollectorGroupInUseError
	switch {
	case errors.Is(err, ErrCollectorGroupNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "collector group not found")
	case errors.Is(err, ErrCollectorGroupMemberNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "collector is no member of the collector group")
	case errors.As(err, &inUse):
		api.WriteProblemBody(w, http.StatusConflict, struct {
			api.FieldProblem
			Probes int `json:"probes"`
		}{
			FieldProblem: api.FieldProblem{ProblemDetail: api.ProblemDetail{Type: "urn:reticora:problem:conflict", Title: "Conflict",
				Status: http.StatusConflict, Detail: inUse.Error()}, Field: "id"},
			Probes: inUse.Probes,
		})
	default:
		api.WriteRepoError(w, err)
	}
}

func (h *Handler) listCollectorGroups(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.collectorGroupRepo(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, total, err := repo.ListCollectorGroups(r.Context(), orgID, CollectorGroupFilter{ClientID: q.Get("client_id"), SiteID: q.Get("site_id")}, page)
	if err != nil {
		writeCollectorGroupError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[CollectorGroup]{Data: items, Total: total, Limit: page.Limit,
		Offset: page.Offset, HasMore: page.Offset+len(items) < total})
}

func (h *Handler) getCollectorGroup(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.collectorGroupRepo(w, r)
	if !ok {
		return
	}
	g, err := repo.GetCollectorGroup(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeCollectorGroupError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, g)
}

// collectorGroupCreate is the body of POST /collector-groups.
type collectorGroupCreate struct {
	Name             string `json:"name"`
	ClientID         string `json:"client_id"`
	SiteID           string `json:"site_id"`
	Mode             string `json:"mode"`
	LivenessTimeoutS *int   `json:"liveness_timeout_s"`
	AnchorProbeID    string `json:"anchor_probe_id"`
}

func (h *Handler) createCollectorGroup(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.collectorGroupRepo(w, r)
	if !ok {
		return
	}
	var req collectorGroupCreate
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	g := &CollectorGroup{OrganizationID: orgID, Name: strings.TrimSpace(req.Name), ClientID: req.ClientID, SiteID: req.SiteID,
		Mode: req.Mode, LivenessTimeoutS: DefaultLivenessTimeoutS, AnchorProbeID: req.AnchorProbeID}
	if g.Mode == "" {
		g.Mode = CollectorGroupDistributed
	}
	if req.LivenessTimeoutS != nil {
		g.LivenessTimeoutS = *req.LivenessTimeoutS
	}
	if err := validateCollectorGroup(g.Name, g.Mode, g.LivenessTimeoutS); err != nil {
		writeCollectorGroupError(w, err)
		return
	}
	for _, ref := range []struct {
		id  string
		err error
	}{{g.ClientID, errGroupClient}, {g.SiteID, errGroupSite}, {g.AnchorProbeID, errAnchorProbe}} {
		if ref.id != "" && !api.IsUUID(ref.id) {
			writeCollectorGroupError(w, ref.err)
			return
		}
	}
	created, err := repo.CreateCollectorGroup(r.Context(), g)
	if err != nil {
		writeCollectorGroupError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, created)
}

func (h *Handler) updateCollectorGroup(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.collectorGroupRepo(w, r)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) == 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body: name at least one field to change")
		return
	}
	u, err := parseCollectorGroupUpdate(body)
	if err != nil {
		writeCollectorGroupError(w, err)
		return
	}
	g, err := repo.UpdateCollectorGroup(r.Context(), orgID, chi.URLParam(r, "id"), u)
	if err != nil {
		writeCollectorGroupError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, g)
}

// parseCollectorGroupUpdate reads a PATCH; the values are checked here,
// the anchor probe in the repository.
func parseCollectorGroupUpdate(body map[string]json.RawMessage) (*CollectorGroupUpdate, error) {
	u := &CollectorGroupUpdate{}
	field := func(name, message string) error {
		return &api.FieldError{Field: name, Status: http.StatusUnprocessableEntity, Message: message}
	}
	for name, raw := range body {
		switch name {
		case "name":
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, field(name, "name must be a string")
			}
			s = strings.TrimSpace(s)
			u.Name = &s
		case "mode":
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, field(name, "mode must be distributed or failover")
			}
			u.Mode = &s
		case "liveness_timeout_s":
			var n int
			if err := json.Unmarshal(raw, &n); err != nil {
				return nil, field(name, "liveness_timeout_s must be an integer")
			}
			u.LivenessTimeoutS = &n
		case "anchor_probe_id":
			s := ""
			if string(bytes.TrimSpace(raw)) != "null" {
				if err := json.Unmarshal(raw, &s); err != nil || (s != "" && !api.IsUUID(s)) {
					return nil, errAnchorProbe
				}
			}
			u.AnchorProbeID = &s
		default:
			return nil, field(name, "only name, mode, liveness_timeout_s and anchor_probe_id can be changed")
		}
	}
	return u, nil
}

// apply changes g by u and checks the result.
func (u *CollectorGroupUpdate) apply(g *CollectorGroup) error {
	if u.Name != nil {
		g.Name = *u.Name
	}
	if u.Mode != nil {
		g.Mode = *u.Mode
	}
	if u.LivenessTimeoutS != nil {
		g.LivenessTimeoutS = *u.LivenessTimeoutS
	}
	if u.AnchorProbeID != nil {
		g.AnchorProbeID = *u.AnchorProbeID
	}
	return validateCollectorGroup(g.Name, g.Mode, g.LivenessTimeoutS)
}

func (h *Handler) deleteCollectorGroup(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.collectorGroupRepo(w, r)
	if !ok {
		return
	}
	if err := repo.DeleteCollectorGroup(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeCollectorGroupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) addCollectorGroupMember(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.collectorGroupRepo(w, r)
	if !ok {
		return
	}
	var in CollectorGroupMemberInput
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	if !api.IsUUID(in.CollectorID) {
		writeCollectorGroupError(w, errCollectorOfOtherClient)
		return
	}
	if in.Weight != nil && *in.Weight < 1 {
		writeCollectorGroupError(w, &api.FieldError{Field: "weight", Status: http.StatusUnprocessableEntity, Message: "weight must be at least 1"})
		return
	}
	g, err := repo.AddCollectorGroupMember(r.Context(), orgID, chi.URLParam(r, "id"), &in)
	if err != nil {
		writeCollectorGroupError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, g)
}

func (h *Handler) removeCollectorGroupMember(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.collectorGroupRepo(w, r)
	if !ok {
		return
	}
	if err := repo.RemoveCollectorGroupMember(r.Context(), orgID, chi.URLParam(r, "id"), chi.URLParam(r, "collector_id")); err != nil {
		writeCollectorGroupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- memory (--no-db) -------------------------------------------------------

// GroupProbes answers the memory store what the probe table answers on
// PostgreSQL: the client of a probe (anchor probe) and the number of
// probes of a group (deletion, last member). Without one there are no
// probes.
type GroupProbes interface {
	ProbeClient(ctx context.Context, orgID, probeID string) (clientID string, ok bool, err error)
	CountGroupProbes(ctx context.Context, orgID, groupID string) (int, error)
}

// memCollectorGroups is the collector group store of the memory
// repository.
type memCollectorGroups struct {
	groups map[string]*CollectorGroup
	// members by collector id.
	members map[string]*CollectorGroupMember
	probes  GroupProbes
	seq     int
}

// GroupMirror is implemented by a probe source that keeps its own copy of
// the collector groups (probe.MemoryRepository): the memory store tells it
// every group it creates and deletes, so that a probe of --no-db finds its
// collector group. The calls run under the store's lock (lock order
// discovery before probe; the probe store never calls back).
type GroupMirror interface {
	AddCollectorGroup(orgID, id, clientID string)
	RemoveCollectorGroup(id string)
}

// mirrorAdd and mirrorRemove tell the probe source of a group.
func (s *memCollectorGroups) mirrorAdd(g *CollectorGroup) {
	if m, ok := s.probes.(GroupMirror); ok {
		m.AddCollectorGroup(g.OrganizationID, g.ID, g.ClientID)
	}
}

func (s *memCollectorGroups) mirrorRemove(id string) {
	if m, ok := s.probes.(GroupMirror); ok {
		m.RemoveCollectorGroup(id)
	}
}

// WithGroupProbes sets the probe source of the memory collector groups.
func (r *MemoryRepository) WithGroupProbes(p GroupProbes) *MemoryRepository {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.cg()
	s.probes = p
	for _, g := range s.groups {
		s.mirrorAdd(g)
	}
	return r
}

// cg returns the collector group store; the lock is held.
func (r *MemoryRepository) cg() *memCollectorGroups {
	if r.collectorGroups == nil {
		r.collectorGroups = &memCollectorGroups{groups: map[string]*CollectorGroup{}, members: map[string]*CollectorGroupMember{}}
	}
	return r.collectorGroups
}

// addDefaultCollectorGroup gives a new collector a group of its own, as
// the trigger collector_default_group does; the lock is held.
func (r *MemoryRepository) addDefaultCollectorGroup(c *Collector) {
	s := r.cg()
	s.seq++
	name := strings.TrimSpace(c.Name)
	if len(name) > maxCollectorGroupName {
		name = strings.TrimSpace(name[:maxCollectorGroupName])
	}
	if name == "" {
		name = "collector " + c.ID
	}
	now := time.Now().UTC()
	g := &CollectorGroup{ID: fmt.Sprintf("c0110000-0000-4000-8000-%012d", s.seq), OrganizationID: c.OrganizationID,
		ClientID: c.ClientID, SiteID: c.SiteID, Name: name, Mode: CollectorGroupDistributed,
		LivenessTimeoutS: DefaultLivenessTimeoutS, Status: "reachable", StatusChangedAt: now, CreatedAt: now, UpdatedAt: now}
	s.groups[g.ID] = g
	s.mirrorAdd(g)
	s.members[c.ID] = &CollectorGroupMember{CollectorID: c.ID, GroupID: g.ID, Weight: DefaultMemberWeight, MemberStatus: "healthy"}
}

// memGroupScope is the client and site scope of a memory call.
type memGroupScope struct {
	clients, sites database.ScopeSet
	scoped         bool
}

func groupScopeOf(ctx context.Context) memGroupScope {
	s, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return memGroupScope{}
	}
	return memGroupScope{clients: s.Clients, sites: s.Sites, scoped: true}
}

func inScope(set database.ScopeSet, id string) bool {
	return set.IsAll() || (id != "" && slices.Contains(set.IDs(), id))
}

func (s *memGroupScope) reads(clientID, siteID string) bool {
	return !s.scoped || ((clientID == "" || inScope(s.clients, clientID)) && (siteID == "" || inScope(s.sites, siteID)))
}

func (s *memGroupScope) writes(clientID, siteID string) bool {
	return !s.scoped || (inScope(s.clients, clientID) && inScope(s.sites, siteID))
}

// visibleGroup returns a copy of the group with its members.
func (r *MemoryRepository) visibleGroup(ctx context.Context, orgID, id string) (*CollectorGroup, bool) {
	s := r.cg()
	g, ok := s.groups[id]
	sc := groupScopeOf(ctx)
	if !ok || g.OrganizationID != orgID || !sc.reads(g.ClientID, g.SiteID) {
		return nil, false
	}
	out := *g
	out.Members = []CollectorGroupMember{}
	for _, m := range s.members {
		if m.GroupID == id {
			out.Members = append(out.Members, *m)
		}
	}
	sort.Slice(out.Members, func(i, j int) bool {
		if out.Members[i].Priority != out.Members[j].Priority {
			return out.Members[i].Priority < out.Members[j].Priority
		}
		return out.Members[i].CollectorID < out.Members[j].CollectorID
	})
	return &out, true
}

// ListCollectorGroups implements CollectorGroupRepository.
func (r *MemoryRepository) ListCollectorGroups(ctx context.Context, orgID string, f CollectorGroupFilter, page api.PaginationParams) ([]CollectorGroup, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := []CollectorGroup{}
	for id := range r.cg().groups {
		g, ok := r.visibleGroup(ctx, orgID, id)
		if !ok || (f.ClientID != "" && g.ClientID != f.ClientID) || (f.SiteID != "" && g.SiteID != f.SiteID) {
			continue
		}
		all = append(all, *g)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].ID < all[j].ID
	})
	start := min(page.Offset, len(all))
	end := min(start+page.Limit, len(all))
	return all[start:end], len(all), nil
}

// GetCollectorGroup implements CollectorGroupRepository.
func (r *MemoryRepository) GetCollectorGroup(ctx context.Context, orgID, id string) (*CollectorGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.visibleGroup(ctx, orgID, id)
	if !ok {
		return nil, ErrCollectorGroupNotFound
	}
	return g, nil
}

// memCheckAnchor checks an anchor probe against the probe source.
func (r *MemoryRepository) memCheckAnchor(ctx context.Context, orgID, clientID, probeID string) error {
	if probeID == "" {
		return nil
	}
	p := r.cg().probes
	if p == nil {
		return errAnchorProbe
	}
	probeClient, ok, err := p.ProbeClient(ctx, orgID, probeID)
	if err != nil {
		return err
	}
	if !ok || (clientID != "" && probeClient != clientID) {
		return errAnchorProbe
	}
	return nil
}

func (r *MemoryRepository) memProbeCount(ctx context.Context, orgID, groupID string) (int, error) {
	if p := r.cg().probes; p != nil {
		// Counted org-wide like groupProbeCount: a probe outside the
		// caller's client or site scope still runs in the group.
		user := ""
		if s, ok := database.TenantScopeFromContext(ctx); ok {
			user = s.UserID
		}
		wide := database.OrgWideScope(orgID, user)
		return p.CountGroupProbes(database.ContextWithTenantScope(ctx, &wide), orgID, groupID)
	}
	return 0, nil
}

// CreateCollectorGroup implements CollectorGroupRepository.
func (r *MemoryRepository) CreateCollectorGroup(ctx context.Context, in *CollectorGroup) (*CollectorGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sc := groupScopeOf(ctx)
	if in.ClientID != "" && !sc.reads(in.ClientID, "") {
		return nil, errGroupClient
	}
	if err := r.memCheckAnchor(ctx, in.OrganizationID, in.ClientID, in.AnchorProbeID); err != nil {
		return nil, err
	}
	if !sc.writes(in.ClientID, in.SiteID) {
		return nil, errMemRowSecurity
	}
	s := r.cg()
	s.seq++
	now := time.Now().UTC()
	g := *in
	g.ID = fmt.Sprintf("c0120000-0000-4000-8000-%012d", s.seq)
	g.Status, g.StatusChangedAt, g.CreatedAt, g.UpdatedAt = "reachable", now, now, now
	g.Members = nil
	s.groups[g.ID] = &g
	s.mirrorAdd(&g)
	out, _ := r.visibleGroup(ctx, g.OrganizationID, g.ID)
	return out, nil
}

// errMemRowSecurity is the refusal of a write outside the scope, as row
// level security reports it (403).
var errMemRowSecurity = &pgconn.PgError{Code: "42501", Message: "new row violates row-level security policy for table \"collector_group\""}

// writableGroup returns the stored group when the caller writes it.
func (r *MemoryRepository) writableGroup(ctx context.Context, orgID, id string) (*CollectorGroup, error) {
	g, ok := r.cg().groups[id]
	sc := groupScopeOf(ctx)
	if !ok || g.OrganizationID != orgID || !sc.reads(g.ClientID, g.SiteID) || !sc.writes(g.ClientID, g.SiteID) {
		return nil, ErrCollectorGroupNotFound
	}
	return g, nil
}

// UpdateCollectorGroup implements CollectorGroupRepository.
func (r *MemoryRepository) UpdateCollectorGroup(ctx context.Context, orgID, id string, u *CollectorGroupUpdate) (*CollectorGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, err := r.writableGroup(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	next := *stored
	if werr := u.apply(&next); werr != nil {
		return nil, werr
	}
	if u.AnchorProbeID != nil {
		if werr := r.memCheckAnchor(ctx, orgID, next.ClientID, next.AnchorProbeID); werr != nil {
			return nil, werr
		}
	}
	next.UpdatedAt = time.Now().UTC()
	*stored = next
	out, _ := r.visibleGroup(ctx, orgID, id)
	return out, nil
}

// DeleteCollectorGroup implements CollectorGroupRepository.
func (r *MemoryRepository) DeleteCollectorGroup(ctx context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.writableGroup(ctx, orgID, id); err != nil {
		return err
	}
	n, err := r.memProbeCount(ctx, orgID, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return &CollectorGroupInUseError{Probes: n}
	}
	s := r.cg()
	delete(s.groups, id)
	s.mirrorRemove(id)
	for cid, m := range s.members {
		if m.GroupID == id {
			delete(s.members, cid)
		}
	}
	return nil
}

// memLeaveAllowed is leaveAllowed of the memory store.
func (r *MemoryRepository) memLeaveAllowed(ctx context.Context, orgID, groupID string) error {
	members := 0
	for _, m := range r.cg().members {
		if m.GroupID == groupID {
			members++
		}
	}
	if members > 1 {
		return nil
	}
	n, err := r.memProbeCount(ctx, orgID, groupID)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrLastMemberWithProbes
	}
	return nil
}

// AddCollectorGroupMember implements CollectorGroupRepository: the move
// changes the stored member only when every check passed (one step).
func (r *MemoryRepository) AddCollectorGroupMember(ctx context.Context, orgID, groupID string, in *CollectorGroupMemberInput) (*CollectorGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, err := r.writableGroup(ctx, orgID, groupID)
	if err != nil {
		return nil, err
	}
	c, ok := r.collectors[in.CollectorID]
	sc := groupScopeOf(ctx)
	if !ok || c.OrganizationID != orgID || !sc.reads(c.ClientID, c.SiteID) || c.ClientID != g.ClientID {
		return nil, errCollectorOfOtherClient
	}
	s := r.cg()
	next := CollectorGroupMember{CollectorID: in.CollectorID, GroupID: groupID, Weight: DefaultMemberWeight, MemberStatus: "healthy"}
	if current, member := s.members[in.CollectorID]; member {
		if current.GroupID == groupID {
			next = *current
		} else {
			old, oldOK := s.groups[current.GroupID]
			if !oldOK || !sc.writes(old.ClientID, old.SiteID) {
				return nil, errCollectorOfOtherClient
			}
			if werr := r.memLeaveAllowed(ctx, orgID, current.GroupID); werr != nil {
				return nil, werr
			}
			next.MemberStatus, next.LastAliveAt, next.ClockOffsetMS = current.MemberStatus, current.LastAliveAt, current.ClockOffsetMS
			next.CapacityHint, next.ConfigVersion = current.CapacityHint, current.ConfigVersion
		}
	}
	if in.Priority != nil {
		next.Priority = *in.Priority
	}
	if in.Weight != nil {
		next.Weight = *in.Weight
	}
	s.members[in.CollectorID] = &next
	out, _ := r.visibleGroup(ctx, orgID, groupID)
	return out, nil
}

// RemoveCollectorGroupMember implements CollectorGroupRepository.
func (r *MemoryRepository) RemoveCollectorGroupMember(ctx context.Context, orgID, groupID, collectorID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.writableGroup(ctx, orgID, groupID); err != nil {
		return err
	}
	s := r.cg()
	m, ok := s.members[collectorID]
	if !ok || m.GroupID != groupID {
		return ErrCollectorGroupMemberNotFound
	}
	if err := r.memLeaveAllowed(ctx, orgID, groupID); err != nil {
		return err
	}
	delete(s.members, collectorID)
	return nil
}
