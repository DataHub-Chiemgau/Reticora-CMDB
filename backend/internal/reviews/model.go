// Package reviews is the review queue of reconciliation (REC-08, REP-01): the
// ten review types, their resolution actions merge | new | dismiss | accept,
// deduplication per object and type, escalation of items open longer than
// 14 days and the REST resource review-items (API-05).
//
// The domain effect of a resolution (merging into a CI, creating one,
// reactivating a deleted one, applying values) belongs to the package that
// opened the item; it registers an Effect per type with the Service.
package reviews

import (
	"context"
	"errors"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// The ten review types of REC-08.
const (
	KindUnclassifiedDevice = "unclassified_device"
	KindAmbiguousIdentity  = "ambiguous_identity"
	KindConflictingValues  = "conflicting_values"
	KindProbableDuplicate  = "probable_duplicate"
	// KindOverrideConflict: a source reported a value that differs from a
	// manual override (REC-12); one open item per CI field.
	KindOverrideConflict  = "override_conflict"
	KindResurrectedDevice = "resurrected_device"
	KindUnlicensedCI      = "unlicensed_ci"
	KindScopeSuggested    = "scope_suggested"
	// KindUnknownRelationship is a neighbor reported with a relationship
	// key the topology does not know (TOP-01).
	KindUnknownRelationship = "unknown_relationship"
	// KindSerialMismatch: the observed serial number of a CI differs from
	// the serial number of its asset (DB-05, AST-04); opened by the
	// database, at most one open item per CI.
	KindSerialMismatch = "serial_mismatch"
	KindDuplicateIP    = "duplicate_ip"
)

// Kinds lists the review types in REC-08 order.
var Kinds = []string{
	KindUnclassifiedDevice, KindAmbiguousIdentity, KindConflictingValues, KindProbableDuplicate, KindOverrideConflict,
	KindResurrectedDevice, KindUnlicensedCI, KindScopeSuggested, KindSerialMismatch, KindDuplicateIP, KindUnknownRelationship,
}

// Statuses of a review item.
const (
	StatusOpen      = "open"
	StatusResolved  = "resolved"
	StatusDismissed = "dismissed"
)

// Resolution actions of REC-08.
const (
	ActionMerge   = "merge"
	ActionNew     = "new"
	ActionDismiss = "dismiss"
	ActionAccept  = "accept"
)

// AllowedActions are the actions that resolve an item of each type:
//
//   - merge folds the observation into a candidate CI (for a resurrected
//     device: reactivates it; for a probable duplicate: removes the new CI);
//   - new creates a CI from the observation;
//   - accept applies what the item proposes (the observed values, the
//     suggested scope);
//   - dismiss closes the item without a change.
var AllowedActions = map[string][]string{
	KindUnclassifiedDevice:  {ActionNew, ActionDismiss},
	KindAmbiguousIdentity:   {ActionMerge, ActionNew, ActionDismiss},
	KindConflictingValues:   {ActionAccept, ActionDismiss},
	KindProbableDuplicate:   {ActionMerge, ActionDismiss},
	KindOverrideConflict:    {ActionAccept, ActionDismiss},
	KindResurrectedDevice:   {ActionMerge, ActionNew, ActionDismiss},
	KindUnlicensedCI:        {ActionNew, ActionDismiss},
	KindScopeSuggested:      {ActionAccept, ActionDismiss},
	KindSerialMismatch:      {ActionDismiss},
	KindDuplicateIP:         {ActionDismiss},
	KindUnknownRelationship: {ActionAccept, ActionDismiss},
}

// Allowed reports whether action resolves an item of kind.
func Allowed(kind, action string) bool {
	for _, a := range AllowedActions[kind] {
		if a == action {
			return true
		}
	}
	return false
}

// EscalationAge is how long an item may stay open before the dashboard
// escalates it (REC-08).
const EscalationAge = 14 * 24 * time.Hour

// Item is one review item.
type Item struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	Kind           string         `json:"kind"`
	Status         string         `json:"status"`
	Payload        map[string]any `json:"payload"`
	CandidateCIIDs []string       `json:"candidate_ci_ids,omitempty"`
	// ObjectKey identifies the object the item is about; one item per
	// object and type is open at a time (REC-08).
	ObjectKey  string `json:"object_key,omitempty"`
	Action     string `json:"action,omitempty"`
	ResolvedBy string `json:"resolved_by,omitempty"`
	ResolvedAt string `json:"resolved_at,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	// Escalated marks items open longer than EscalationAge.
	Escalated bool   `json:"escalated"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// Deduplicated is set by CreateReviewItem when an open item of the same
	// object and type already existed; Item then describes that item.
	Deduplicated bool `json:"-"`
	// ClientID and SiteID scope an item without candidate CIs: the producer
	// sets the client and site of the device, collector or subnet it is
	// about. With candidates the database derives both from the candidate
	// CIs (migration 000140). Not part of the API.
	ClientID string `json:"-"`
	SiteID   string `json:"-"`
}

// markEscalated sets Escalated from the creation time.
func (i *Item) markEscalated(now time.Time) {
	created, err := time.Parse(time.RFC3339Nano, i.CreatedAt)
	i.Escalated = err == nil && i.Status == StatusOpen && now.Sub(created) > EscalationAge
}

// Filter narrows a listing.
type Filter struct {
	Status string
	Kind   string
	// ObjectKey lists the items of one object.
	ObjectKey string
	// Escalated lists only open items older than EscalationAge.
	Escalated bool
}

// ResolveRequest is the request body for resolving an item
// (ReviewItemResolveRequest). It names no resolver: a body with resolved_by
// is refused as an unknown field (400).
type ResolveRequest struct {
	Action string `json:"action"`
	CIID   string `json:"ci_id,omitempty"`
	Note   string `json:"resolution,omitempty"`
}

// Resolution is the resolution of an item as the service carries it out.
type Resolution struct {
	Action string `json:"action"`
	CIID   string `json:"ci_id,omitempty"`
	// ResolvedBy is the principal (user or service account) that resolves
	// the item: the handler takes it from the request, never from the body
	// (F-091). It is stored as resolved_by and is the audit actor; empty
	// for the system (adoption after max_cis was raised).
	ResolvedBy string `json:"-"`
	Note       string `json:"resolution,omitempty"`
	// Status is the terminal status to persist; derived from Action.
	Status string `json:"-"`
}

// Repository stores review items.
type Repository interface {
	// CreateReviewItem opens an item; with an ObjectKey it returns the open
	// item of the same object and type instead (Deduplicated).
	CreateReviewItem(ctx context.Context, item *Item) error
	ListReviewItems(ctx context.Context, orgID string, filter Filter, page api.PaginationParams) ([]Item, int, error)
	GetReviewItem(ctx context.Context, orgID, id string) (*Item, error)
	ResolveReviewItem(ctx context.Context, orgID, id string, resolution *Resolution) (*Item, error)
}

// Errors of a resolution; the handler maps them to HTTP statuses.
var (
	ErrNotFound        = errors.New("review item not found")
	ErrAlreadyResolved = errors.New("review item already resolved")
)

// Error is a refused resolution with its HTTP status (400, 403, 409, 422).
type Error struct {
	Status  int
	Message string
	// Problem is an RFC 7807 problem type, when the refusal has one.
	Problem string
}

func (e *Error) Error() string { return e.Message }

// ProblemType implements api.TypedProblem when Problem is set.
func (e *Error) ProblemType() string { return e.Problem }
