// Package relationship provides the CI relationship domain for Reticora CMDB.
package relationship

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
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
	// SourceClientID, SourceSiteID, TargetClientID and TargetSiteID are the
	// scope of both ends (migration 000061, empty for an org-wide end,
	// 000142): the field permissions of the edge are evaluated for both
	// (RBA-07, E-140, WP-770). The PostgreSQL repository reads them; the
	// memory repository knows no client and no site.
	SourceClientID string `json:"-"`
	SourceSiteID   string `json:"-"`
	TargetClientID string `json:"-"`
	TargetSiteID   string `json:"-"`
	// checkTypeLists makes the PostgreSQL repository check the CI types of
	// the ends against allowed_source_types and allowed_target_types of the
	// relationship type, subtypes included (ErrSourceCITypeNotAllowed,
	// ErrTargetCITypeNotAllowed), and the cardinality of the type
	// (ErrCardinalityExceeded, REL-06).
	// POST /relationships sets it (E-95), as does the accept of an
	// unknown_relationship item (CheckTypeLists); the discovery ingest does
	// not.
	checkTypeLists bool
}

// CheckTypeLists makes Create check the CI types of the ends against the
// type lists of the relationship type, as POST /relationships does (E-95):
// for an edge a person decides on outside this package, the accept of an
// unknown_relationship item (TOP-01, F-424).
func (r *Relationship) CheckTypeLists() { r.checkTypeLists = true }

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
	// Check, when set, is called with the edge as stored before the write,
	// in the transaction and under the lock of the write (the memory
	// repository: under its lock); an error refuses the write and leaves
	// the edge unchanged. The handler sets it for the field permissions
	// (RBA-07, WP-770).
	Check func(ctx context.Context, current *Relationship) error `json:"-"`
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
	"monitored_by":      true,
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
	// REL-06 seeds (WP-294, migration 000202).
	"belongs_to": true,
}

// ValidSources lists the origin vocabulary for relationships (spec §12).
var ValidSources = map[string]bool{
	"manual": true, "discovery": true, "agent": true, "import": true,
	"api": true, "integration": true, "workflow": true, "rule": true,
}

// SystemSources are the sources reserved to their system paths (REL-08,
// F-271): discovery edges come from the discovery ingest, agent edges from
// the agent path. POST /relationships refuses them with 422 on source; a
// user edge stored as discovery would need force to be deleted and would be
// removed by the cleanup of unconfirmed discovery edges.
var SystemSources = map[string]bool{"discovery": true, "agent": true}

// ErrSystemSource refuses a system source on POST /relationships (F-271).
var ErrSystemSource = errors.New("discovery and agent are reserved to the discovery and agent paths")

// ProvenanceAttribute is the attribute that records actor, actor type,
// source and time of an edge (REL-08). Create sets it; POST and PATCH
// refuse it (ErrProvenanceReserved, F-270, REL-07) and the repositories
// keep the stored one.
const ProvenanceAttribute = "provenance"

// ErrProvenanceReserved refuses attributes.provenance on POST and PATCH
// (F-270, REL-07).
var ErrProvenanceReserved = errors.New("provenance is recorded by the system and cannot be changed")

// ErrForceRequired refuses deleting a discovery edge without force=true
// (REL-04): discovery would find it again, so the deletion also records a
// suppression.
var ErrForceRequired = errors.New("a discovery edge is deleted only with force=true; discovery then no longer recreates it")

// ErrSuppressionType refuses the forced deletion of a discovery edge whose
// type has no relationship_type row (REL-03, F-272): without the row no
// suppression can be stored and discovery would recreate the edge, so it
// stays. Delete answers 422 on rel_type.
var ErrSuppressionType = errors.New("the relationship type has no catalogue entry, so the deletion cannot be recorded as suppression")

// ErrSourceCINotFound and ErrTargetCINotFound refuse an edge whose source or
// target CI the caller cannot see: a CI of another organization, of a client
// or site outside the scope, an unknown id (F-145) or a soft-deleted CI
// (F-424). Create answers 422 on
// source_ci_id or target_ci_id, the same for each of them, so the answer does
// not tell a foreign CI from an unknown one. A repository joins both when
// neither endpoint is visible.
var (
	ErrSourceCINotFound = errors.New("source CI not found")
	ErrTargetCINotFound = errors.New("target CI not found")
)

// ErrSourceCITypeNotAllowed and ErrTargetCITypeNotAllowed refuse an edge
// whose visible source or target CI has a type outside allowed_source_types
// or allowed_target_types of the relationship type; an empty list allows
// every type (REL-01, E-95, F-313). Create answers 422 on source_ci_id or
// target_ci_id, a repository joins both.
var (
	ErrSourceCITypeNotAllowed = errors.New("the type of the source CI is not in allowed_source_types of the relationship type")
	ErrTargetCITypeNotAllowed = errors.New("the type of the target CI is not in allowed_target_types of the relationship type")
)

// ErrCardinalityExceeded refuses an edge that would exceed the cardinality
// of its relationship type (REL-06, WP-294): one_to_one allows the source
// one outgoing and the target one incoming edge of the type, one_to_many
// the target one incoming, many_to_one the source one outgoing. Create
// answers 422 on rel_type.
var ErrCardinalityExceeded = errors.New("the edge would exceed the cardinality of the relationship type")

// PolicyDeleter deletes an edge by the source policy (REL-03, REL-04): a
// discovery edge only with force, then suppressed for its CIs and type.
type PolicyDeleter interface {
	DeleteWithPolicy(ctx context.Context, orgID, id string, opts DeleteOptions) error
}

// DeleteOptions are the options of DeleteWithPolicy: Force deletes a
// discovery edge; its suppression records the actor and the reason
// (F-273).
type DeleteOptions struct {
	Force bool
	// Reason is the reason given in the confirmation; empty stores none.
	Reason string
	// Actor names who deletes the edge (created_by of the suppression and
	// actor of its audit entry).
	Actor Actor
}

// Actor is who changes a suppression: the user id (empty for a principal
// without user) and the actor type of the audit entry (user, api_key).
type Actor struct {
	ID   string
	Type string
}

// MaxSuppressionReason is the longest reason a suppression stores, in
// characters (migration 000178).
const MaxSuppressionReason = 1000

// Audit actions of the suppression lifecycle (F-273): the forced deletion of
// a discovery edge creates a suppression, DELETE
// /relationship-suppressions/{id} lifts it.
const (
	ActionSuppressionCreated = "relationship.suppression_created"
	ActionSuppressionLifted  = "relationship.suppression_lifted"
)

// Suppression is a removed discovery edge that discovery does not create
// again (REL-03): its CIs and type, who removed it, why and when (F-273).
type Suppression struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	SourceCIID     string `json:"source_ci_id"`
	TargetCIID     string `json:"target_ci_id"`
	RelType        string `json:"rel_type"`
	Reason         string `json:"reason,omitempty"`
	CreatedBy      string `json:"created_by,omitempty"`
	CreatedAt      string `json:"created_at"`
}

// SuppressionStore lists and lifts suppressions (F-273). Both see only the
// suppressions whose source and target CI the caller sees.
type SuppressionStore interface {
	// ListSuppressions returns the suppressions of the organization, with
	// ciID set those whose source or target is that CI, newest first.
	ListSuppressions(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Suppression, int, error)
	// LiftSuppression deletes a suppression, so the next discovery cycle
	// creates its edge again; api.ErrNotFound when the caller does not
	// find it.
	LiftSuppression(ctx context.Context, orgID, id string, actor Actor) error
}

// MemoryRepository is an in-memory relationship store.
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Relationship
	// suppressions are the suppressions of forced deletions (REL-03).
	suppressions map[string]*Suppression
	seq          int
	// visible decides which CIs a traversal may pass through (IMP-07).
	visible NodeVisibility
}

// DeleteWithPolicy implements PolicyDeleter; a forced deletion of a
// discovery edge stores its suppression in memory.
func (r *MemoryRepository) DeleteWithPolicy(ctx context.Context, orgID, id string, opts DeleteOptions) error {
	r.mu.RLock()
	rel, ok := r.items[id]
	var edge Relationship
	if ok {
		edge = *rel
	}
	r.mu.RUnlock()
	if ok && edge.OrganizationID == orgID && edge.Source == "discovery" && !opts.Force {
		return ErrForceRequired
	}
	if err := r.Delete(ctx, orgID, id); err != nil {
		return err
	}
	if edge.Source != "discovery" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.suppressions {
		if s.OrganizationID == orgID && s.SourceCIID == edge.SourceCIID && s.TargetCIID == edge.TargetCIID && s.RelType == edge.RelType {
			return nil
		}
	}
	r.seq++
	s := &Suppression{ID: fmt.Sprintf("%08d-0000-4000-8000-%012d", r.seq, r.seq), OrganizationID: orgID,
		SourceCIID: edge.SourceCIID, TargetCIID: edge.TargetCIID, RelType: edge.RelType, Reason: opts.Reason,
		CreatedBy: opts.Actor.ID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	r.suppressions[s.ID] = s
	return nil
}

// ListSuppressions implements SuppressionStore.
func (r *MemoryRepository) ListSuppressions(_ context.Context, orgID, ciID string, page api.PaginationParams) ([]Suppression, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Suppression, 0)
	for _, s := range r.suppressions {
		if s.OrganizationID == orgID && (ciID == "" || s.SourceCIID == ciID || s.TargetCIID == ciID) {
			items = append(items, *s)
		}
	}
	slices.SortFunc(items, func(a, b Suppression) int {
		return cmp.Or(strings.Compare(b.CreatedAt, a.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}

// LiftSuppression implements SuppressionStore.
func (r *MemoryRepository) LiftSuppression(_ context.Context, orgID, id string, _ Actor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.suppressions[id]
	if !ok || s.OrganizationID != orgID {
		return api.ErrNotFound
	}
	delete(r.suppressions, id)
	return nil
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

// BatchConfirmer re-confirms several discovery edges seen again with one
// statement, each as Confirm does (TOP-02; WP-608, NFR-03).
type BatchConfirmer interface {
	ConfirmMany(ctx context.Context, orgID string, confirmations []Confirmation) error
}

// Confirmation is the re-confirmation of one discovery edge: its id and
// the reported confidence (nil keeps the stored one).
type Confirmation struct {
	ID         string
	Confidence *float64
	// Attributes are merged into the stored attributes of the edge, so
	// that what discovery reports again (an outlet switched off) does not
	// stay at its first value (WP-498); a nil value removes the attribute.
	// Nil leaves the attributes unchanged.
	Attributes map[string]any
}

// mergeAttributes merges patch into attrs as a confirmation does: a nil
// value removes the attribute.
func mergeAttributes(attrs, patch map[string]any) map[string]any {
	if patch == nil {
		return attrs
	}
	out := make(map[string]any, len(attrs)+len(patch))
	for k, v := range attrs {
		out[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

// ConfirmMany implements BatchConfirmer.
func (r *MemoryRepository) ConfirmMany(ctx context.Context, orgID string, confirmations []Confirmation) error {
	for _, c := range confirmations {
		r.mu.Lock()
		rel, ok := r.items[c.ID]
		if ok && rel.OrganizationID == orgID && rel.Source == "discovery" {
			rel.Attributes = mergeAttributes(rel.Attributes, c.Attributes)
		}
		r.mu.Unlock()
		if ok {
			// Gone edges and edges of other sources stay unchanged, as in
			// the PostgreSQL repository.
			_ = r.Confirm(ctx, orgID, c.ID, c.Confidence)
		}
	}
	return nil
}

// RedundancyMarker recomputes attributes.redundant of the discovery edges
// of a type (powered_by, IMP-09) per source CI from every stored discovery
// edge of that type, not only from those a batch reported (WP-498, F-112):
// a source fed by more than one target has redundant feeds. An edge whose
// attributes.outlet_state is "off" stays an edge but feeds nothing (E-85
// option A). Edges of other sources are neither counted nor changed
// (REL-08).
type RedundancyMarker interface {
	MarkRedundant(ctx context.Context, orgID, relType string, sourceCIIDs []string) error
}

// liveFeed reports whether a stored edge counts as a feed (E-85 option A).
func liveFeed(attrs map[string]any) bool {
	state, _ := attrs["outlet_state"].(string)
	return state != "off"
}

// MarkRedundant implements RedundancyMarker.
func (r *MemoryRepository) MarkRedundant(_ context.Context, orgID, relType string, sourceCIIDs []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	sources := map[string]bool{}
	for _, id := range sourceCIIDs {
		sources[id] = true
	}
	feeds := map[string]map[string]bool{}
	var edges []*Relationship
	for _, rel := range r.items {
		if rel.OrganizationID != orgID || rel.RelType != relType || rel.Source != "discovery" || !sources[rel.SourceCIID] {
			continue
		}
		edges = append(edges, rel)
		if feeds[rel.SourceCIID] == nil {
			feeds[rel.SourceCIID] = map[string]bool{}
		}
		if liveFeed(rel.Attributes) {
			feeds[rel.SourceCIID][rel.TargetCIID] = true
		}
	}
	for _, rel := range edges {
		redundant := len(feeds[rel.SourceCIID]) > 1
		if rel.Attributes["redundant"] == redundant {
			continue
		}
		rel.Attributes = mergeAttributes(rel.Attributes, map[string]any{"redundant": redundant})
		rel.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return nil
}

// ListFilter holds the documented filters of GET /api/v1/relationships
// (API-01, F-402): CIID keeps the edges with the CI at either end,
// SourceCIID and TargetCIID those with the CI at that end, RelType those
// of the type. The filters combine; an empty field does not filter.
type ListFilter struct {
	CIID       string
	SourceCIID string
	TargetCIID string
	RelType    string
}

// Repository defines persistence operations for relationships.
type Repository interface {
	// List returns the relationships of the organization, of one CI when
	// ciID is set: ListFiltered with ListFilter{CIID: ciID}.
	List(ctx context.Context, orgID string, ciID string, page api.PaginationParams) ([]Relationship, int, error)
	ListFiltered(ctx context.Context, orgID string, filter ListFilter, page api.PaginationParams) ([]Relationship, int, error)
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

// Walker walks like Traverser and returns the walk with its CIs as numbers
// (NFR-02, WP-611): topology and impact work on the numbers instead of
// the id strings. The PostgreSQL repository implements it; the memory
// repository does not.
type Walker interface {
	WalkFrom(ctx context.Context, orgID, rootCIID string, maxDepth, maxNodes int) (*Walk, error)
	// WalkFromMany walks from several distinct roots at once (IMP-05,
	// WP-840): one walk whose hop distance is the distance to the nearest
	// root, so an edge several roots share is read once and the statements
	// do not grow with the number of roots. Without the node limit it
	// keeps the union of the walks from each root: a CI within maxDepth of
	// a root, and an edge between two kept CIs that leaves a CI closer than
	// maxDepth to some root. maxNodes bounds the whole walk (at least the
	// roots are kept); Walk.Truncated reports a cut. The visibility is that
	// of WalkFrom (IMP-07).
	WalkFromMany(ctx context.Context, orgID string, rootCIIDs []string, maxDepth, maxNodes int) (*Walk, error)
}

// Walk is the result of a walk: the edges TraverseFrom returns with the
// numbers of their CIs.
type Walk struct {
	// CIs holds the id of each CI the walk keeps by number: the roots
	// first, in the order they were given, then the CIs in the order the
	// walk reaches them. A root the caller does not see keeps its number
	// but no edge.
	CIs []string
	// Edges are the kept edges, ordered by id.
	Edges []WalkEdge
	// Truncated is set when the node limit cut the walk: a kept CI may
	// then lack edges to CIs within the depth (IMP-04 reads them again).
	Truncated bool
}

// WalkEdge is an edge of a Walk; SourceCI and TargetCI are numbers of
// Walk.CIs, Source is the origin of the edge (Relationship.Source).
type WalkEdge struct {
	ID                 string
	SourceCI, TargetCI int32
	RelType            string
	Source             string
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

// IncidentEdgeReader reads the stored edges of some types that touch a set
// of CIs, whatever the depth of a walk (IMP-04, F-160): the impact reads
// every provider of a redundant type of the dependents it reached, also
// those beyond its depth, in one statement. Both the PostgreSQL and the
// memory repository implement it.
type IncidentEdgeReader interface {
	// EdgesOf returns the stored relationships of one of relTypes whose
	// source or target is among ciIDs and whose source and target are
	// both visible, not deleted CIs under the caller's scope, ordered by
	// id.
	EdgesOf(ctx context.Context, orgID string, ciIDs, relTypes []string) ([]Relationship, error)
}

// HiddenEdgeReader reads the stored edges of some types that touch a set of
// CIs and that the caller's client or site scope hides (IMP-04, IMP-07,
// F-420): the impact counts a provider of a redundant type in another
// client or site as a working supply of its dependent, as it does for an
// org-wide caller, without learning which CI it is. The PostgreSQL
// repository implements it; without it the impact sees only what the
// caller sees.
type HiddenEdgeReader interface {
	// HiddenEdgesOf returns the stored relationships of one of relTypes
	// whose source or target is among ciIDs, whose source and target are
	// not deleted CIs of the organization, and which EdgesOf does not
	// return to the caller. They carry neither an id nor a source; an end
	// the caller does not see is empty. Nothing is returned to a caller
	// without a client and site restriction.
	HiddenEdgesOf(ctx context.Context, orgID string, ciIDs, relTypes []string) ([]Relationship, error)
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
	return &MemoryRepository{items: make(map[string]*Relationship), suppressions: make(map[string]*Suppression)}
}

func (r *MemoryRepository) List(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Relationship, int, error) {
	return r.ListFiltered(ctx, orgID, ListFilter{CIID: ciID}, page)
}

func (r *MemoryRepository) ListFiltered(_ context.Context, orgID string, filter ListFilter, page api.PaginationParams) ([]Relationship, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Relationship
	for _, rel := range r.items {
		if rel.OrganizationID != orgID {
			continue
		}
		if filter.CIID != "" && rel.SourceCIID != filter.CIID && rel.TargetCIID != filter.CIID {
			continue
		}
		if (filter.SourceCIID != "" && rel.SourceCIID != filter.SourceCIID) ||
			(filter.TargetCIID != "" && rel.TargetCIID != filter.TargetCIID) ||
			(filter.RelType != "" && rel.RelType != filter.RelType) {
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

func (r *MemoryRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Relationship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rel, ok := r.items[id]
	if !ok || rel.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if req.Check != nil {
		current := *rel
		current.Attributes = maps.Clone(rel.Attributes)
		if err := req.Check(ctx, &current); err != nil {
			return nil, err
		}
	}
	if req.Attributes != nil {
		if rel.Attributes == nil {
			rel.Attributes = make(map[string]any)
		}
		for k, v := range req.Attributes {
			// The stored provenance stays (F-270).
			if k != ProvenanceAttribute {
				rel.Attributes[k] = v
			}
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
		return api.ErrNotFound
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

// EdgesOf implements IncidentEdgeReader on the in-memory edges; with a
// NodeVisibility only edges between visible, not deleted CIs count, as in
// TraverseFrom.
func (r *MemoryRepository) EdgesOf(ctx context.Context, orgID string, ciIDs, relTypes []string) ([]Relationship, error) {
	edges := make([]Relationship, 0)
	if len(ciIDs) == 0 || len(relTypes) == 0 {
		return edges, nil
	}
	r.mu.RLock()
	for _, rel := range r.items {
		if rel.OrganizationID == orgID && slices.Contains(relTypes, rel.RelType) &&
			(slices.Contains(ciIDs, rel.SourceCIID) || slices.Contains(ciIDs, rel.TargetCIID)) {
			edges = append(edges, *rel)
		}
	}
	visibility := r.visible
	r.mu.RUnlock()

	if visibility != nil && len(edges) > 0 {
		var ids []string
		for i := range edges {
			ids = append(ids, edges[i].SourceCIID, edges[i].TargetCIID)
		}
		visible, err := visibility(ctx, orgID, ids)
		if err != nil {
			return nil, err
		}
		edges = visibleEdges(edges, relationshipEnds, visible)
	}
	slices.SortFunc(edges, func(a, b Relationship) int { return strings.Compare(a.ID, b.ID) })
	return edges, nil
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
	r.Get("/api/v1/relationships/{id}/changes", h.ListChanges)
	r.Get("/api/v1/relationship-suppressions", h.ListSuppressions)
	r.Delete("/api/v1/relationship-suppressions/{id}", h.LiftSuppression)
}

// List handles GET /api/v1/cis/{id}/relationships, the edges of the CI, and
// GET /api/v1/relationships with its documented filters ci_id,
// source_ci_id, target_ci_id and rel_type (API-01, F-402). A CI filter
// that is no UUID and a rel_type the catalogue does not know are 400; an
// unknown or invisible CI lists nothing. The edges stay those of the
// caller's tenant scope.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	filter := ListFilter{CIID: chi.URLParam(r, "id")}
	if filter.CIID == "" {
		var ok bool
		if filter, ok = h.listFilter(w, r, t.OrganizationID); !ok {
			return
		}
	}
	page := api.ParsePagination(r)

	rels, total, err := h.repo.ListFiltered(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	// The fields of each edge as the principal may read them (RBA-07).
	if rels, err = ProjectEdges(r.Context(), t.OrganizationID, rels, h.types); err != nil {
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

// listFilter reads the query filters of GET /api/v1/relationships; on an
// invalid one it answers 400 and reports false.
func (h *Handler) listFilter(w http.ResponseWriter, r *http.Request, orgID string) (ListFilter, bool) {
	q := r.URL.Query()
	filter := ListFilter{CIID: q.Get("ci_id"), SourceCIID: q.Get("source_ci_id"), TargetCIID: q.Get("target_ci_id"), RelType: q.Get("rel_type")}
	for _, f := range []struct{ name, value string }{
		{"ci_id", filter.CIID}, {"source_ci_id", filter.SourceCIID}, {"target_ci_id", filter.TargetCIID},
	} {
		if f.value != "" && !api.IsUUID(f.value) {
			api.WriteFieldError(w, &api.FieldError{Field: f.name, Message: "must be a UUID"})
			return ListFilter{}, false
		}
	}
	// The catalogue holds the projected types (contains, mounted_in,
	// located_in) as well, so every listed type can be filtered.
	if filter.RelType != "" && (api.RejectNUL("rel_type", filter.RelType) != nil || !h.validRelType(r.Context(), orgID, filter.RelType)) {
		api.WriteFieldError(w, &api.FieldError{Field: "rel_type", Message: "must be a relationship type of the catalogue"})
		return ListFilter{}, false
	}
	return filter, true
}

// Create handles POST /api/v1/relationships: the edge of the request as a
// manual write of the caller (CreateManual), answered with the fields the
// caller may read.
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
	// Actor and time of the edge (REL-08).
	actor := Actor{ID: t.UserID, Type: "user"}
	if actor.ID == "" {
		actor = Actor{ID: "api", Type: "api_key"}
	}
	rel, rights, err := h.createManual(r.Context(), t.OrganizationID, actor, &req)
	var refused *RequestError
	switch {
	case err == nil:
	case errors.As(err, &refused):
		if len(refused.violations) > 0 {
			writeViolations(w, refused.violations...)
		} else {
			api.WriteError(w, refused.Status, http.StatusText(refused.Status), refused.Detail)
		}
		return
	case writeSchemaError(w, err), writeEndpointError(w, err):
		return
	default:
		api.WriteRepoError(w, err)
		return
	}

	out := *rel
	if rights != nil {
		if err = rights.project(r.Context(), &out); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusCreated, &out)
}

// RequestError refuses a create request before the write (CreateManual):
// the status with its detail (400, 422 for a projected type), or a 422 with
// the rejected fields.
type RequestError struct {
	Status     int
	Detail     string
	violations []violation
}

// validation: the fixed texts of the checks of CreateManual.
func (e *RequestError) Error() string {
	if len(e.violations) == 0 {
		return e.Detail
	}
	details := make([]string, 0, len(e.violations))
	for _, v := range e.violations {
		details = append(details, v.Field+": "+v.Detail)
	}
	return strings.Join(details, "; ")
}

// Fields are the rejected fields of a 422 with violations.
func (e *RequestError) Fields() []string {
	fields := make([]string, 0, len(e.violations))
	for _, v := range e.violations {
		fields = append(fields, v.Field)
	}
	return fields
}

// refuse is a RequestError with status and detail.
func refuse(status int, detail string) *RequestError {
	return &RequestError{Status: status, Detail: detail}
}

// refuseFields is a 422 RequestError with the rejected fields.
func refuseFields(violations ...violation) *RequestError {
	return &RequestError{Status: http.StatusUnprocessableEntity, violations: violations}
}

// errStructuralRelType names the projected types, which no edge stores.
const errStructuralRelType = " is projected and cannot be stored: containment is the CI's parent_ci_id, " +
	"mounted_in follows from the rack mount, located_in from the CI's location"

// ManualRelType checks a rel_type for a manual edge: a projected type
// (StructuralRelTypes) is a 422, a type the catalogue lacks (validRelType)
// a 400, both as *RequestError.
func (h *Handler) ManualRelType(ctx context.Context, orgID, relType string) error {
	if StructuralRelTypes[relType] {
		return refuse(http.StatusUnprocessableEntity, "rel_type "+relType+errStructuralRelType)
	}
	if !h.validRelType(ctx, orgID, relType) {
		return refuse(http.StatusBadRequest, "invalid rel_type")
	}
	return nil
}

// CreateManual creates the edge of req as a manual write of actor in orgID
// (POST /api/v1/relationships and the bulk add_relationship of the CIs,
// WP-780): the checks of the request form (*RequestError: 400, a projected
// type 422, reserved attributes 422 with the fields), the catalogue of the
// type, the provenance with the actor, source manual and verification
// state verified for a manual edge, the field permissions of the principal
// in ctx (RBA-07) and the schema of the type (REL-07). Errors of the
// repository (an endpoint the caller cannot see, a type the relationship
// type does not allow, the cardinality, an existing edge) pass unchanged.
// It returns the stored edge, not projected.
func (h *Handler) CreateManual(ctx context.Context, orgID string, actor Actor, req CreateRequest) (*Relationship, error) { //nolint:gocritic // a request value like the handler decodes
	rel, _, err := h.createManual(ctx, orgID, actor, &req)
	return rel, err
}

// createManual is CreateManual; it also returns the field permissions of
// the edge (nil without), with which Create projects its answer.
func (h *Handler) createManual(ctx context.Context, orgID string, actor Actor, req *CreateRequest) (*Relationship, *edgeRights, error) {
	if req.SourceCIID == "" || req.TargetCIID == "" || req.RelType == "" {
		return nil, nil, refuse(http.StatusBadRequest, "source_ci_id, target_ci_id, and rel_type are required")
	}
	if err := h.ManualRelType(ctx, orgID, req.RelType); err != nil {
		return nil, nil, err
	}
	// The seven sources of REL-08; rule is accepted as workflow (E-01).
	source, validSource := CanonicalSource(req.Source)
	if !validSource {
		return nil, nil, refuse(http.StatusBadRequest, "invalid source")
	}
	if SystemSources[source] {
		// Discovery and agent edges come only from their paths (F-271).
		return nil, nil, refuseFields(violation{Field: "source", Detail: ErrSystemSource.Error()})
	}
	req.Source = source
	if req.Confidence != nil && (*req.Confidence < 0 || *req.Confidence > 1) {
		return nil, nil, refuse(http.StatusBadRequest, "confidence must be between 0 and 1")
	}

	rel := &Relationship{
		OrganizationID: orgID,
		SourceCIID:     req.SourceCIID,
		TargetCIID:     req.TargetCIID,
		RelType:        req.RelType,
		// A copy: req.Attributes stays the attributes of the request.
		Attributes:   maps.Clone(req.Attributes),
		Source:       req.Source,
		Confidence:   req.Confidence,
		SourceSystem: req.SourceSystem,
		Notes:        req.Notes,

		SourceInterfaceID: req.SourceInterfaceID,
		TargetInterfaceID: req.TargetInterfaceID,

		checkTypeLists: true,
	}
	if rel.Attributes == nil {
		rel.Attributes = make(map[string]any)
	}
	if rel.Source == "" {
		rel.Source = "manual"
	}
	// Actor and time of the edge (REL-08).
	rel.Attributes["provenance"] = Provenance(actor.ID, actor.Type, rel.Source, time.Now())
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
		return nil, nil, refuse(http.StatusBadRequest, "invalid verification_state")
	}
	if rel.VerificationState == "verified" && actor.Type == "user" {
		rel.VerifiedBy = actor.ID
	}
	// Reserved attribute keys (422, REL-07): provenance is recorded by the
	// server, a field name of the edge is no attribute.
	if violations := reservedViolations(req.Attributes); len(violations) > 0 {
		return nil, nil, refuseFields(violations...)
	}

	// The fields of the request against the field permissions (RBA-07):
	// after the checks of the request form (400), before the write.
	rights, err := newEdgeRights(ctx, orgID, h.types)
	if err != nil {
		return nil, nil, err
	}
	if rights != nil {
		if checkErr := h.checkCreate(ctx, rights, req, rel); checkErr != nil {
			return nil, nil, checkErr
		}
	}
	// The attributes against the schema of the type (422, REL-07), after
	// the field permissions; a schema that cannot be read is 500.
	if schemaErr := h.checkMetadata(ctx, orgID, rel.RelType, req.Attributes, req.Attributes, nil); schemaErr != nil {
		return nil, nil, schemaErr
	}
	if createErr := h.repo.Create(ctx, rel); createErr != nil {
		return nil, nil, createErr
	}
	return rel, rights, nil
}

// checkCreate checks the fields of a POST /relationships against the field
// permissions of the new edge (RBA-07, WP-770): its type and the scope of
// both CIs. An end the caller cannot see skips the check; Create then
// answers 422 on it.
func (h *Handler) checkCreate(ctx context.Context, rights *edgeRights, req *CreateRequest, rel *Relationship) error {
	if reader, ok := h.repo.(ScopeReader); ok {
		if !api.IsUUID(rel.SourceCIID) || !api.IsUUID(rel.TargetCIID) {
			return nil
		}
		scopes, err := reader.EndpointScopes(ctx, rel.OrganizationID, []string{rel.SourceCIID, rel.TargetCIID})
		if err != nil {
			return err
		}
		source, sourceOK := scopes[rel.SourceCIID]
		target, targetOK := scopes[rel.TargetCIID]
		if !sourceOK || !targetOK {
			return nil
		}
		rel.SourceClientID, rel.SourceSiteID = source.ClientID, source.SiteID
		rel.TargetClientID, rel.TargetSiteID = target.ClientID, target.SiteID
	}
	typeID, err := rights.typeID(ctx, rel.RelType)
	if err != nil {
		return err
	}
	var writes []fieldWrite
	for k, v := range req.Attributes {
		// The server records the provenance (REL-08).
		if k != ProvenanceAttribute {
			writes = append(writes, fieldWrite{key: permission.NamespaceAttributes + "." + k, changed: !emptyValue(v)})
		}
	}
	if req.Notes != "" {
		writes = append(writes, fieldWrite{key: fieldNotes, changed: true})
	}
	if req.Confidence != nil {
		writes = append(writes, fieldWrite{key: fieldConfidence, changed: true})
	}
	return rights.checkWrite(typeID, rel, writes)
}

// violation is one rejected field of a 422 (ViolationProblem).
type violation struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

// writeEndpointError answers an edge with an endpoint the caller cannot see
// (ErrSourceCINotFound, ErrTargetCINotFound, F-145) or of a type the
// relationship type does not allow (ErrSourceCITypeNotAllowed,
// ErrTargetCITypeNotAllowed, F-313) with 422 and a violation per such
// endpoint, an edge beyond the cardinality of the type
// (ErrCardinalityExceeded, REL-06) with 422 on rel_type. It reports whether
// err was one.
func writeEndpointError(w http.ResponseWriter, err error) bool {
	var violations []violation
	for _, end := range []struct {
		field string
		err   error
	}{
		{"source_ci_id", ErrSourceCINotFound}, {"source_ci_id", ErrSourceCITypeNotAllowed},
		{"target_ci_id", ErrTargetCINotFound}, {"target_ci_id", ErrTargetCITypeNotAllowed},
		{"rel_type", ErrCardinalityExceeded},
	} {
		if errors.Is(err, end.err) {
			violations = append(violations, violation{Field: end.field, Detail: end.err.Error()})
		}
	}
	if len(violations) == 0 {
		return false
	}
	writeViolations(w, violations...)
	return true
}

// writeViolations answers 422 with the rejected fields (ViolationProblem).
func writeViolations(w http.ResponseWriter, violations ...violation) {
	details := make([]string, 0, len(violations))
	for _, v := range violations {
		details = append(details, v.Field+": "+v.Detail)
	}
	api.WriteProblemBody(w, http.StatusUnprocessableEntity, struct {
		api.ProblemDetail
		Violations []violation `json:"violations"`
	}{
		ProblemDetail: api.ProblemDetail{Type: "https://reticora.io/problems/422", Title: "Unprocessable Entity",
			Status: http.StatusUnprocessableEntity, Detail: strings.Join(details, "; ")},
		Violations: violations,
	})
}

// validRelType resolves the rel_type against the metadata catalogue when
// available, falling back to the built-in list.
func (h *Handler) validRelType(ctx context.Context, orgID, relType string) bool {
	if h.types != nil {
		ok, err := h.types.Exists(ctx, orgID, relType)
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
	if violations := reservedViolations(req.Attributes); len(violations) > 0 {
		// Actor and time of the edge are recorded by the system (F-270), a
		// field name of the edge is no attribute (REL-07).
		writeViolations(w, violations...)
		return
	}

	req.ActorID = t.UserID
	rights, err := newEdgeRights(r.Context(), t.OrganizationID, h.types)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	// Only a PATCH with attributes checks the schema: a PATCH of the
	// verification state keeps working on an edge that a later schema
	// change made invalid (REL-07).
	checkSchema := req.Attributes != nil && h.hasSchemaSource()
	if rights != nil || checkSchema {
		// The repository calls it with the stored edge under the lock of
		// the write: the field permissions (RBA-07, WP-770), then the
		// attributes after the merge against the schema of the type, read
		// in the same transaction (REL-07).
		req.Check = func(ctx context.Context, current *Relationship) error {
			if rights != nil {
				if checkErr := rights.checkUpdate(ctx, &req, current); checkErr != nil {
					return checkErr
				}
			}
			if !checkSchema {
				return nil
			}
			return h.checkMetadata(ctx, t.OrganizationID, current.RelType,
				mergedAttributes(current.Attributes, req.Attributes), req.Attributes, current.Attributes)
		}
	}
	rel, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		if writeSchemaError(w, err) {
			return
		}
		var forbidden *api.FieldForbiddenError
		if errors.As(err, &forbidden) {
			api.WriteFieldError(w, forbidden)
			return
		}
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
	if rights != nil {
		if err = rights.project(r.Context(), rel); err != nil {
			api.WriteRepoError(w, err)
			return
		}
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

	// A malformed id names no edge (404) and never reaches SQL.
	id, ok := api.ParseUUIDParam(w, r, "id", "relationship not found")
	if !ok {
		return
	}
	// The reason of a suppression (F-273): optional for the API, the UI
	// asks for it in its confirmation.
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	if utf8.RuneCountInString(reason) > MaxSuppressionReason || strings.ContainsRune(reason, 0) {
		writeViolations(w, violation{Field: "reason", Detail: errReason.Error()})
		return
	}
	var err error
	if deleter, ok := h.repo.(PolicyDeleter); ok {
		// A discovery edge goes only with force and is then suppressed
		// (REL-03, REL-04); manual edges are deleted as asked.
		err = deleter.DeleteWithPolicy(r.Context(), t.OrganizationID, id, DeleteOptions{
			Force: r.URL.Query().Get("force") == "true", Reason: reason, Actor: requestActor(r)})
	} else {
		err = h.repo.Delete(r.Context(), t.OrganizationID, id)
	}
	// Only an unknown or invisible edge is 404; a refused suppression names
	// the type, other failures go through WriteRepoError (F-272).
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrForceRequired):
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, ErrSuppressionType):
		writeViolations(w, violation{Field: "rel_type", Detail: ErrSuppressionType.Error()})
	case errors.Is(err, api.ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "relationship not found")
	default:
		api.WriteRepoError(w, err)
	}
}

// errReason refuses a suppression reason that is too long or holds a NUL
// character (422 on reason).
var errReason = fmt.Errorf("the reason has at most %d characters and no NUL character", MaxSuppressionReason)

// requestActor names the principal of the request as the actor of a
// suppression change: its user id (none when the subject is no UUID) and
// whether it is a user session or an API key.
func requestActor(r *http.Request) Actor {
	actor := Actor{ID: tenant.FromContext(r.Context()).UserID, Type: "user"}
	if p, ok := identity.PrincipalFromContext(r.Context()); ok && p.Type == identity.PrincipalTypeAPIKey {
		actor.Type = "api_key"
	}
	if !api.IsUUID(actor.ID) {
		actor.ID = ""
	}
	return actor
}

// ListSuppressions handles GET /api/v1/relationship-suppressions: the
// suppressions the caller sees (both CIs visible), with ci_id those of one
// CI, newest first (F-273).
func (h *Handler) ListSuppressions(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	ciID := r.URL.Query().Get("ci_id")
	if ciID != "" && !api.IsUUID(ciID) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "ci_id must be a UUID")
		return
	}
	items, total := []Suppression{}, 0
	if store, ok := h.repo.(SuppressionStore); ok {
		var err error
		if items, total, err = store.ListSuppressions(r.Context(), t.OrganizationID, ciID, page); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Suppression]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// LiftSuppression handles DELETE /api/v1/relationship-suppressions/{id}: the
// suppression is deleted and audited (relationship.suppression_lifted), so
// the next discovery cycle creates the edge again (F-273). 404 for a
// suppression the caller does not find (unknown, another organization, a CI
// outside the scope, malformed id).
func (h *Handler) LiftSuppression(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", "suppression not found")
	if !ok {
		return
	}
	err := api.ErrNotFound
	if store, ok := h.repo.(SuppressionStore); ok {
		err = store.LiftSuppression(r.Context(), t.OrganizationID, id, requestActor(r))
	}
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, api.ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "suppression not found")
	default:
		api.WriteRepoError(w, err)
	}
}

// Field permissions of edges (RBA-07, E-43, E-140, WP-770): the fields
// attributes.<key>, core.notes and core.confidence of an edge resolve by
// FieldPolicy.Level with the object kind relationship, the id of its
// relationship type and the scope of each end; the lower level of both
// ends counts. Every other member of Relationship stays; RLS and the scope
// decide whether the edge is visible. Grants of the object kind ci with a
// relationship type do not act on edges (E-140).

// The core fields of an edge the projection covers.
const (
	fieldNotes      = permission.NamespaceCore + ".notes"
	fieldConfidence = permission.NamespaceCore + ".confidence"
)

// orgWideEnd stands for the missing client or site of an edge end (an
// org-wide CI, 000142): only role assignments without a client or site
// scope cover it, so only their grants count there (E-140).
const orgWideEnd = "\x00org-wide"

// EndpointScope is the client and site of a CI at an end of an edge, empty
// without one.
type EndpointScope struct {
	ClientID string
	SiteID   string
}

// ScopeReader reads the client and site of the visible, not deleted CIs
// among ids under the caller's scope; an invisible CI is missing from the
// map. The PostgreSQL repository implements it.
type ScopeReader interface {
	EndpointScopes(ctx context.Context, orgID string, ids []string) (map[string]EndpointScope, error)
}

// TypeResolver resolves a relationship type key as the caller sees it: the
// organization's row before the system row (relationshiptype.Repository).
type TypeResolver interface {
	GetByKey(ctx context.Context, orgID, key string) (*relationshiptype.Type, error)
}

// edgeRights evaluates the field permissions of edges for one request.
type edgeRights struct {
	orgID  string
	policy *permission.FieldPolicy
	types  TypeChecker
	// typeIDs caches the relationship type id per key, resolved once per
	// request.
	typeIDs map[string]string
}

// newEdgeRights returns the field permissions of the request for edges,
// nil when the policy does not restrict the object kind relationship
// (no grant and no sensitive field): the answers stay unchanged.
func newEdgeRights(ctx context.Context, orgID string, types TypeChecker) (*edgeRights, error) {
	policy, err := permission.PolicyFrom(ctx)
	if err != nil {
		return nil, err
	}
	if !policy.Restricted(permission.KindRelationship) {
		return nil, nil //nolint:nilnil // nil means no restriction
	}
	return &edgeRights{orgID: orgID, policy: policy, types: types, typeIDs: map[string]string{}}, nil
}

// typeID returns the id of the relationship type of key, empty without a
// catalogue row (only grants without relationship type apply then).
func (e *edgeRights) typeID(ctx context.Context, key string) (string, error) {
	if id, ok := e.typeIDs[key]; ok {
		return id, nil
	}
	id := ""
	if resolver, ok := e.types.(TypeResolver); ok {
		typ, err := resolver.GetByKey(ctx, e.orgID, key)
		switch {
		case err == nil && typ != nil:
			id = typ.ID
		case err == nil, errors.Is(err, relationshiptype.ErrNotFound):
		default:
			return "", err
		}
	}
	e.typeIDs[key] = id
	return id, nil
}

// endRef is the object reference of one end of the edge.
func endRef(id, clientID, siteID string) permission.ObjectRef {
	if clientID == "" {
		clientID = orgWideEnd
	}
	if siteID == "" {
		siteID = orgWideEnd
	}
	return permission.ObjectRef{ID: id, ClientID: clientID, SiteID: siteID}
}

// level returns the level of the field of the edge: the lower level of
// both ends (E-140).
func (e *edgeRights) level(typeID, fieldKey string, rel *Relationship) permission.Level {
	source := e.policy.Level(permission.KindRelationship, typeID, fieldKey, endRef(rel.ID, rel.SourceClientID, rel.SourceSiteID))
	if !source.Readable() {
		return permission.LevelNone
	}
	target := e.policy.Level(permission.KindRelationship, typeID, fieldKey, endRef(rel.ID, rel.TargetClientID, rel.TargetSiteID))
	switch {
	case !target.Readable():
		return permission.LevelNone
	case !source.Editable() || !target.Editable():
		return permission.LevelRead
	}
	return permission.LevelWrite
}

// project removes the fields of the edge the principal may not read: the
// attribute keys from attributes (the object stays, {} at least), notes
// and confidence from the edge. A projected structural edge carries none
// of them and stays. The attributes are replaced, not changed in place.
func (e *edgeRights) project(ctx context.Context, rel *Relationship) error {
	if rel.Projected {
		return nil
	}
	typeID, err := e.typeID(ctx, rel.RelType)
	if err != nil {
		return err
	}
	attrs := make(map[string]any, len(rel.Attributes))
	for k, v := range rel.Attributes {
		if e.level(typeID, permission.NamespaceAttributes+"."+k, rel).Readable() {
			attrs[k] = v
		}
	}
	rel.Attributes = attrs
	if rel.Notes != "" && !e.level(typeID, fieldNotes, rel).Readable() {
		rel.Notes = ""
	}
	if rel.Confidence != nil && !e.level(typeID, fieldConfidence, rel).Readable() {
		rel.Confidence = nil
	}
	return nil
}

// ProjectEdges returns the edges with the fields the principal of ctx may
// read (RBA-07, WP-770): GET /relationships, GET /cis/{id}/relationships
// and the include relationships of GET /cis/{id}. types resolves the
// relationship types (a TypeResolver; nil or another TypeChecker leaves
// only grants without relationship type). Without a restriction of the
// object kind relationship the edges are returned unchanged.
func ProjectEdges(ctx context.Context, orgID string, rels []Relationship, types TypeChecker) ([]Relationship, error) {
	rights, err := newEdgeRights(ctx, orgID, types)
	if err != nil || rights == nil || len(rels) == 0 {
		return rels, err
	}
	out := make([]Relationship, len(rels))
	copy(out, rels)
	for i := range out {
		if projectErr := rights.project(ctx, &out[i]); projectErr != nil {
			return nil, projectErr
		}
	}
	return out, nil
}

// fieldWrite is a field of a write request: its field key and whether its
// value differs from the stored one (for a new edge: is not empty).
type fieldWrite struct {
	key     string
	changed bool
}

// checkWrite applies the write rule of WP-265 to the fields of a request:
// a field the principal may not read is refused whatever its value, a
// readable field that is not editable when its value changes. The refusal
// is an *api.FieldForbiddenError with the sorted DTO paths
// (attributes.<key>, notes, confidence).
func (e *edgeRights) checkWrite(typeID string, rel *Relationship, writes []fieldWrite) error {
	var refused []string
	for _, w := range writes {
		level := e.level(typeID, w.key, rel)
		if !level.Readable() || (w.changed && !level.Editable()) {
			refused = append(refused, permission.DTOPath(w.key))
		}
	}
	if len(refused) == 0 {
		return nil
	}
	sort.Strings(refused)
	return &api.FieldForbiddenError{Fields: refused}
}

// checkUpdate checks the fields of a PATCH /relationships/{id} against the
// stored edge (RBA-07, WP-770).
func (e *edgeRights) checkUpdate(ctx context.Context, req *UpdateRequest, current *Relationship) error {
	typeID, err := e.typeID(ctx, current.RelType)
	if err != nil {
		return err
	}
	var writes []fieldWrite
	for k, v := range req.Attributes {
		if k == ProvenanceAttribute {
			continue
		}
		stored, ok := current.Attributes[k]
		writes = append(writes, fieldWrite{key: permission.NamespaceAttributes + "." + k, changed: !ok || !reflect.DeepEqual(stored, v)})
	}
	if req.Notes != nil {
		writes = append(writes, fieldWrite{key: fieldNotes, changed: *req.Notes != current.Notes})
	}
	if req.Confidence != nil {
		writes = append(writes, fieldWrite{key: fieldConfidence, changed: current.Confidence == nil || *current.Confidence != *req.Confidence})
	}
	return e.checkWrite(typeID, current, writes)
}

// emptyValue reports whether an attribute value of a new edge is empty
// (null or an empty string).
func emptyValue(v any) bool {
	s, isString := v.(string)
	return v == nil || (isString && s == "")
}
