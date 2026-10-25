// Package relationship provides the CI relationship domain for Reticora CMDB.
package relationship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Relationship represents a directed edge between two CIs.
type Relationship struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	SourceCIID     string         `json:"source_ci_id"`
	TargetCIID     string         `json:"target_ci_id"`
	RelType        string         `json:"rel_type"`
	Attributes     map[string]any `json:"attributes"`
	Source         string         `json:"source"`
	// Provenance and verification metadata (spec §12).
	Confidence        *float64 `json:"confidence,omitempty"`
	FirstSeenAt       string   `json:"first_seen_at,omitempty"`
	LastSeenAt        string   `json:"last_seen_at,omitempty"`
	VerificationState string   `json:"verification_state,omitempty"`
	SourceSystem      string   `json:"source_system,omitempty"`
	Notes             string   `json:"notes,omitempty"`
	// SourceInterfaceID and TargetInterfaceID are the port endpoints of
	// the edge: interfaces of its source and target CI (REL-02).
	SourceInterfaceID string `json:"source_interface_id,omitempty"`
	TargetInterfaceID string `json:"target_interface_id,omitempty"`
	// LastConfirmedAt is the last time the source of the edge confirmed
	// it; VerifiedBy/VerifiedAt name who verified it and when (REL-02).
	LastConfirmedAt string `json:"last_confirmed_at,omitempty"`
	VerifiedBy      string `json:"verified_by,omitempty"`
	VerifiedAt      string `json:"verified_at,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	// Projected marks a structural relationship that is not stored but
	// derived (DB-05, CH27): contains from ci.parent_ci_id, mounted_in from
	// rack_mount, located_in from ci.location_id. It cannot be changed
	// through the relationship API.
	Projected bool `json:"projected,omitempty"`
	// TargetLocationID is the target of mounted_in (the rack) and located_in
	// (the location); TargetCIID is empty for them.
	TargetLocationID string `json:"target_location_id,omitempty"`
}

// StructuralRelTypes are projected, never stored (REL-01, CH27).
var StructuralRelTypes = map[string]bool{
	"contains": true, "contained_by": true, "parent_of": true, "child_of": true,
	"mounted_in": true, "located_in": true,
}

// ProjectionSource is the source of a projected relationship.
const ProjectionSource = "projection"

// CreateRequest is the payload for creating a relationship.
type CreateRequest struct {
	SourceCIID        string         `json:"source_ci_id"`
	TargetCIID        string         `json:"target_ci_id"`
	RelType           string         `json:"rel_type"`
	Attributes        map[string]any `json:"attributes,omitempty"`
	Source            string         `json:"source,omitempty"`
	Confidence        *float64       `json:"confidence,omitempty"`
	VerificationState string         `json:"verification_state,omitempty"`
	SourceSystem      string         `json:"source_system,omitempty"`
	Notes             string         `json:"notes,omitempty"`
	SourceInterfaceID string         `json:"source_interface_id,omitempty"`
	TargetInterfaceID string         `json:"target_interface_id,omitempty"`
}

// UpdateRequest is the payload for editing a relationship's provenance and
// verification metadata (spec §13: manual relationships can be created,
// edited, verified and removed). The endpoints of the edge and its type are
// immutable — rewiring is expressed by deleting and recreating the edge so the
// graph history stays truthful.
type UpdateRequest struct {
	Attributes        map[string]any `json:"attributes,omitempty"`
	Confidence        *float64       `json:"confidence,omitempty"`
	VerificationState *string        `json:"verification_state,omitempty"`
	SourceSystem      *string        `json:"source_system,omitempty"`
	Notes             *string        `json:"notes,omitempty"`
	// ActorID is the user who changes the edge; a change of the
	// verification state records it as verified_by (set by the handler).
	ActorID string `json:"-"`
}

// ValidVerificationStates lists the verification vocabulary for relationships.
var ValidVerificationStates = map[string]bool{
	"unverified": true, "verified": true, "disputed": true, "stale": true,
}

// ValidRelTypes lists the built-in relationship types. The relationship_type
// metadata table (migration 000055) is the authoritative catalogue; this map
// remains as the fallback for repositories without metadata access and keeps
// every type that was ever valid (additive).
var ValidRelTypes = map[string]bool{
	"connected_to":      true,
	"hosted_on":         true,
	"depends_on":        true,
	"member_of":         true,
	"member_of_cluster": true,
	"powers":            true,
	"powered_by":        true,
	"runs_on":           true,
	"mounted_in":        true,
	"uplink_to":         true,
	"stores":            true,
	"monitors":          true,
	"backs_up":          true,
	// Extended standard catalogue (spec §11).
	"backed_up_by": true,
	"managed_by":   true,
	"manages":      true,
	"assigned_to":  true,
	"contains":     true,
	"contained_by": true,
	"parent_of":    true,
	"child_of":     true,
	"located_in":   true,
	"uses":         true,
	"used_by":      true,
}

// ValidSources lists the origin vocabulary for relationships (spec §12).
var ValidSources = map[string]bool{
	"manual": true, "discovery": true, "agent": true, "import": true,
	"api": true, "integration": true, "workflow": true, "rule": true,
}

// ErrForceRequired refuses deleting a discovery edge without force=true
// (REL-04): discovery would find it again, so the deletion also records a
// suppression.
var ErrForceRequired = errors.New("a discovery edge is deleted only with force=true; discovery then no longer recreates it")

// ErrSourceCINotFound and ErrTargetCINotFound refuse an edge whose source or
// target CI the caller cannot see: a CI of another organization, of a client
// or site outside the scope, or an unknown id (F-145). Create answers 422 on
// source_ci_id or target_ci_id, the same for each of them, so the answer does
// not tell a foreign CI from an unknown one. A repository joins both when
// neither endpoint is visible.
var (
	ErrSourceCINotFound = errors.New("source CI not found")
	ErrTargetCINotFound = errors.New("target CI not found")
)

// PolicyDeleter deletes an edge by the source policy (REL-03, REL-04): a
// discovery edge only with force, then suppressed for its CIs and type.
type PolicyDeleter interface {
	DeleteWithPolicy(ctx context.Context, orgID, id string, force bool) error
}

// DeleteWithPolicy implements PolicyDeleter (without suppression storage).
func (r *MemoryRepository) DeleteWithPolicy(ctx context.Context, orgID, id string, force bool) error {
	r.mu.RLock()
	rel, ok := r.items[id]
	r.mu.RUnlock()
	if ok && rel.OrganizationID == orgID && rel.Source == "discovery" && !force {
		return ErrForceRequired
	}
	return r.Delete(ctx, orgID, id)
}

// Confirmer re-confirms a discovery edge seen again (TOP-02).
type Confirmer interface {
	Confirm(ctx context.Context, orgID, id string, confidence *float64) error
}

// Confirm implements Confirmer.
func (r *MemoryRepository) Confirm(_ context.Context, orgID, id string, confidence *float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rel, ok := r.items[id]
	if !ok || rel.OrganizationID != orgID || rel.Source != "discovery" {
		return fmt.Errorf("not found")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rel.LastSeenAt, rel.LastConfirmedAt = now, now
	if confidence != nil {
		c := *confidence
		rel.Confidence = &c
	}
	return nil
}

// Repository defines persistence operations for relationships.
type Repository interface {
	List(ctx context.Context, orgID string, ciID string, page api.PaginationParams) ([]Relationship, int, error)
	Create(ctx context.Context, rel *Relationship) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Relationship, error)
	Delete(ctx context.Context, orgID, id string) error
}

// Traverser walks the configuration graph from a root CI up to maxDepth hops
// away, following relationships in both directions. maxDepth is clamped to the
// given bounds and maxNodes caps the number of distinct CIs returned.
type Traverser interface {
	TraverseFrom(ctx context.Context, orgID, rootCIID string, maxDepth, maxNodes int) ([]Relationship, error)
}

// EdgeReader reads the edges among a set of CIs at once, with a number of
// statements that does not grow with the set, so the topology without a
// root reads its edges without one query per node (NFR-02, ABN-02). The
// memory repository does not implement it; the topology then lists the
// edges of each CI.
type EdgeReader interface {
	// EdgesAmong returns the edges the topology shows (the stored
	// relationships and the projected containment) whose source and target
	// are both among ids and are visible, not deleted CIs under the
	// caller's scope, ordered by id.
	EdgesAmong(ctx context.Context, orgID string, ids []string) ([]Relationship, error)
}

// MemoryRepository is an in-memory relationship store.
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Relationship
	seq   int
	// visible decides which CIs a traversal may pass through (IMP-07).
	visible NodeVisibility
}

// NodeVisibility returns which of ids are visible, not deleted CIs of the
// organization under the caller's scope (IMP-07).
type NodeVisibility func(ctx context.Context, orgID string, ids []string) (map[string]bool, error)

// WithNodeVisibility makes TraverseFrom pass only through visible, not
// deleted CIs, like the PostgreSQL repository; without it every endpoint
// counts as visible.
func (r *MemoryRepository) WithNodeVisibility(visible NodeVisibility) *MemoryRepository {
	r.mu.Lock()
	r.visible = visible
	r.mu.Unlock()
	return r
}

// NewMemoryRepository creates a new in-memory relationship repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: make(map[string]*Relationship)}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, ciID string, page api.PaginationParams) ([]Relationship, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Relationship
	for _, rel := range r.items {
		if rel.OrganizationID != orgID {
			continue
		}
		if ciID != "" && rel.SourceCIID != ciID && rel.TargetCIID != ciID {
			continue
		}
		result = append(result, *rel)
	}

	total := len(result)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return result[start:end], total, nil
}

func (r *MemoryRepository) Create(_ context.Context, rel *Relationship) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	rel.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	rel.CreatedAt = now
	rel.UpdatedAt = now
	rel.LastConfirmedAt = now
	if rel.VerificationState == "verified" {
		rel.VerifiedAt = now
	}
	r.items[rel.ID] = rel
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateRequest) (*Relationship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rel, ok := r.items[id]
	if !ok || rel.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if req.Attributes != nil {
		if rel.Attributes == nil {
			rel.Attributes = make(map[string]any)
		}
		for k, v := range req.Attributes {
			rel.Attributes[k] = v
		}
	}
	if req.Confidence != nil {
		rel.Confidence = req.Confidence
	}
	if req.VerificationState != nil {
		rel.VerificationState = *req.VerificationState
		rel.VerifiedBy, rel.VerifiedAt = "", ""
		if rel.VerificationState == "verified" {
			rel.VerifiedBy, rel.VerifiedAt = req.ActorID, time.Now().UTC().Format(time.RFC3339Nano)
		}
	}
	if req.SourceSystem != nil {
		rel.SourceSystem = *req.SourceSystem
	}
	if req.Notes != nil {
		rel.Notes = *req.Notes
	}
	rel.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	out := *rel
	return &out, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rel, ok := r.items[id]
	if !ok || rel.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.items, id)
	return nil
}

// TraverseFrom walks the in-memory graph from rootCIID with the semantics of
// SelectTraversal, like the PostgreSQL repository. The memory repository
// has no CI table: with a NodeVisibility it keeps the edges between visible,
// not deleted CIs (IMP-07), without one every endpoint counts as visible.
func (r *MemoryRepository) TraverseFrom(ctx context.Context, orgID, rootCIID string, maxDepth, maxNodes int) ([]Relationship, error) {
	r.mu.RLock()
	edges := make([]Relationship, 0, len(r.items))
	for _, rel := range r.items {
		if rel.OrganizationID == orgID {
			edges = append(edges, *rel)
		}
	}
	visibility := r.visible
	r.mu.RUnlock()

	if visibility != nil && len(edges) > 0 {
		seen := map[string]bool{}
		var ids []string
		for i := range edges {
			for _, id := range []string{edges[i].SourceCIID, edges[i].TargetCIID} {
				if id != "" && !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		visible, err := visibility(ctx, orgID, ids)
		if err != nil {
			return nil, err
		}
		edges = visibleEdges(edges, relationshipEnds, visible)
	}
	return SelectTraversal(rootCIID, edges, maxDepth, maxNodes), nil
}

func relationshipEnds(rel *Relationship) (source, target string) {
	return rel.SourceCIID, rel.TargetCIID
}

// visibleEdges keeps the edges whose source and target are both in visible,
// so a walk over the result never passes through an invisible or deleted CI
// (IMP-07). An endpoint missing from visible is invisible (fail closed). It
// filters edges in place.
func visibleEdges[E any](edges []E, ends func(*E) (source, target string), visible map[string]bool) []E {
	kept := edges[:0]
	for i := range edges {
		if source, target := ends(&edges[i]); visible[source] && visible[target] {
			kept = append(kept, edges[i])
		}
	}
	return kept
}

// MaxTraversalNodes is the node limit of every traversal (topology, impact):
// the PostgreSQL and the memory repository receive the same value.
const MaxTraversalNodes = 10000

// SelectTraversal defines the result of TraverseFrom on a set of edges whose
// endpoints are all visible: every node gets its hop distance from rootCIID
// (at most maxDepth); the maxNodes nodes with the smallest distance (ties by
// id) are kept, and the result holds each edge between kept nodes that leaves
// a node closer than maxDepth, ordered by id.
func SelectTraversal(rootCIID string, edges []Relationship, maxDepth, maxNodes int) []Relationship {
	kept := selectEdges(rootCIID, edges, relationshipEnds, maxDepth, maxNodes)
	slices.SortFunc(kept, func(a, b int) int { return strings.Compare(edges[a].ID, edges[b].ID) })
	result := make([]Relationship, 0, len(kept))
	for _, i := range kept {
		result = append(result, edges[i])
	}
	return result
}

// selectEdges returns the indexes of the edges SelectTraversal keeps; ends
// reads the source and target of an edge.
func selectEdges[E any](rootCIID string, edges []E, ends func(*E) (source, target string), maxDepth, maxNodes int) []int {
	if maxDepth < 1 || maxNodes < 1 || rootCIID == "" {
		return nil
	}
	// Adjacency index: each node expands its own edges only (NFR-02).
	incident := make(map[string][]int, len(edges))
	for i := range edges {
		source, target := ends(&edges[i])
		incident[source] = append(incident[source], i)
		if target != source {
			incident[target] = append(incident[target], i)
		}
	}
	depth := map[string]int{rootCIID: 0}
	frontier := []string{rootCIID}
	for d := 1; d <= maxDepth && len(frontier) > 0; d++ {
		var next []string
		for _, id := range frontier {
			for _, i := range incident[id] {
				source, target := ends(&edges[i])
				neighbor := target
				if neighbor == id {
					neighbor = source
				}
				if _, seen := depth[neighbor]; seen || neighbor == "" {
					continue
				}
				depth[neighbor] = d
				next = append(next, neighbor)
			}
		}
		frontier = next
	}

	// Beyond the node limit only the nodes closest to the root are kept.
	kept := func(id string) bool { _, ok := depth[id]; return ok }
	if len(depth) > maxNodes {
		ids := make([]string, 0, len(depth))
		for id := range depth {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			if depth[ids[i]] != depth[ids[j]] {
				return depth[ids[i]] < depth[ids[j]]
			}
			return ids[i] < ids[j]
		})
		limited := make(map[string]bool, maxNodes)
		for _, id := range ids[:maxNodes] {
			limited[id] = true
		}
		kept = func(id string) bool { return limited[id] }
	}
	var result []int
	for i := range edges {
		source, target := ends(&edges[i])
		if !kept(source) || !kept(target) {
			continue
		}
		if min(depth[source], depth[target]) >= maxDepth {
			continue
		}
		result = append(result, i)
	}
	return result
}

// TypeChecker validates a rel_type against the relationship_type metadata
// catalogue when available. Implementations return (true, nil) for known keys
// and (false, nil) for unknown ones.
type TypeChecker interface {
	Exists(ctx context.Context, orgID, key string) (bool, error)
}

// Handler provides HTTP handlers for relationship endpoints.
type Handler struct {
	repo  Repository
	types TypeChecker
}

// NewHandler creates a new relationship handler. An optional TypeChecker
// enables metadata-driven rel_type validation; without one the built-in
// ValidRelTypes catalogue is enforced.
func NewHandler(repo Repository, checkers ...TypeChecker) *Handler {
	h := &Handler{repo: repo}
	if len(checkers) > 0 {
		h.types = checkers[0]
	}
	return h
}

// RegisterRoutes registers relationship routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/relationships", h.List)
	r.Get("/api/v1/cis/{id}/relationships", h.List)
	r.Post("/api/v1/relationships", h.Create)
	r.Patch("/api/v1/relationships/{id}", h.Update)
	r.Delete("/api/v1/relationships/{id}", h.Delete)
}

// List handles GET /api/v1/cis/{id}/relationships
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	ciID := chi.URLParam(r, "id")
	page := api.ParsePagination(r)

	rels, total, err := h.repo.List(r.Context(), t.OrganizationID, ciID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Relationship]{
		Data:    rels,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Create handles POST /api/v1/relationships
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.SourceCIID == "" || req.TargetCIID == "" || req.RelType == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "source_ci_id, target_ci_id, and rel_type are required")
		return
	}
	if StructuralRelTypes[req.RelType] {
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity",
			"rel_type "+req.RelType+" is projected and cannot be stored: containment is the CI's parent_ci_id, "+
				"mounted_in follows from the rack mount, located_in from the CI's location")
		return
	}
	if !h.validRelType(r, t.OrganizationID, req.RelType) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid rel_type")
		return
	}
	// The seven sources of REL-08; rule is accepted as workflow (E-01).
	source, validSource := CanonicalSource(req.Source)
	if !validSource {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid source")
		return
	}
	req.Source = source
	if req.Confidence != nil && (*req.Confidence < 0 || *req.Confidence > 1) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "confidence must be between 0 and 1")
		return
	}

	rel := &Relationship{
		OrganizationID: t.OrganizationID,
		SourceCIID:     req.SourceCIID,
		TargetCIID:     req.TargetCIID,
		RelType:        req.RelType,
		Attributes:     req.Attributes,
		Source:         req.Source,
		Confidence:     req.Confidence,
		SourceSystem:   req.SourceSystem,
		Notes:          req.Notes,

		SourceInterfaceID: req.SourceInterfaceID,
		TargetInterfaceID: req.TargetInterfaceID,
	}
	if rel.Attributes == nil {
		rel.Attributes = make(map[string]any)
	}
	if rel.Source == "" {
		rel.Source = "manual"
	}
	// Actor and time of the edge (REL-08).
	actor, actorType := t.UserID, "user"
	if actor == "" {
		actor, actorType = "api", "api_key"
	}
	rel.Attributes["provenance"] = Provenance(actor, actorType, rel.Source, time.Now())
	if rel.Source == "manual" {
		// Manual relationships are born verified; they must never be removed
		// silently because discovery does not detect them (spec §12).
		rel.VerificationState = "verified"
	} else {
		rel.VerificationState = req.VerificationState
	}
	if rel.VerificationState == "" {
		// Mirror the column default so the response reports the state that is
		// actually persisted rather than an empty string.
		rel.VerificationState = "unverified"
	}
	if !ValidVerificationStates[rel.VerificationState] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid verification_state")
		return
	}
	if rel.VerificationState == "verified" {
		rel.VerifiedBy = t.UserID
	}

	if err := h.repo.Create(r.Context(), rel); err != nil {
		if writeEndpointError(w, err) {
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, rel)
}

// violation is one rejected field of a 422 (ViolationProblem).
type violation struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

// writeEndpointError answers an edge with an endpoint the caller cannot see
// (ErrSourceCINotFound, ErrTargetCINotFound) with 422 and a violation per
// such endpoint (F-145). It reports whether err was one.
func writeEndpointError(w http.ResponseWriter, err error) bool {
	var violations []violation
	var details []string
	for _, end := range []struct {
		field string
		err   error
	}{{"source_ci_id", ErrSourceCINotFound}, {"target_ci_id", ErrTargetCINotFound}} {
		if errors.Is(err, end.err) {
			violations = append(violations, violation{Field: end.field, Detail: end.err.Error()})
			details = append(details, end.field+": "+end.err.Error())
		}
	}
	if len(violations) == 0 {
		return false
	}
	api.WriteProblemBody(w, http.StatusUnprocessableEntity, struct {
		api.ProblemDetail
		Violations []violation `json:"violations"`
	}{
		ProblemDetail: api.ProblemDetail{Type: "https://reticora.io/problems/422", Title: "Unprocessable Entity",
			Status: http.StatusUnprocessableEntity, Detail: strings.Join(details, "; ")},
		Violations: violations,
	})
	return true
}

// validRelType resolves the rel_type against the metadata catalogue when
// available, falling back to the built-in list.
func (h *Handler) validRelType(r *http.Request, orgID, relType string) bool {
	if h.types != nil {
		ok, err := h.types.Exists(r.Context(), orgID, relType)
		if err == nil {
			return ok
		}
	}
	return ValidRelTypes[relType]
}

// Update handles PATCH /api/v1/relationships/{id}. It lets an authorized user
// edit and verify an existing relationship (spec §13) without recreating it.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	if req.Confidence != nil && (*req.Confidence < 0 || *req.Confidence > 1) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "confidence must be between 0 and 1")
		return
	}
	if req.VerificationState != nil && !ValidVerificationStates[*req.VerificationState] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid verification_state")
		return
	}

	req.ActorID = t.UserID
	rel, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			api.WriteError(w, http.StatusNotFound, "Not Found", "relationship not found")
			return
		}
		if api.WriteDBError(w, err) {
			return
		}
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "could not update relationship")
		return
	}

	api.WriteJSON(w, http.StatusOK, rel)
}

// Delete handles DELETE /api/v1/relationships/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	var err error
	if deleter, ok := h.repo.(PolicyDeleter); ok {
		// A discovery edge goes only with force and is then suppressed
		// (REL-03, REL-04); manual edges are deleted as asked.
		err = deleter.DeleteWithPolicy(r.Context(), t.OrganizationID, id, r.URL.Query().Get("force") == "true")
	} else {
		err = h.repo.Delete(r.Context(), t.OrganizationID, id)
	}
	if errors.Is(err, ErrForceRequired) {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "relationship not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
