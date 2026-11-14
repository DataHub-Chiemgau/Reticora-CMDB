package api

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
)

// Read-only grace period of an organization deletion (TLC-06, WP-1153).

// ProblemOrgPendingDeletion is the problem type of a write refused during
// the grace period (423).
const ProblemOrgPendingDeletion = "urn:reticora:problem:org-pending-deletion"

// ErrOrgPendingDeletion refuses a write (a new job) of an organization
// whose deletion is requested or whose purge runs; WriteRepoError answers
// it with 423 org-pending-deletion.
var ErrOrgPendingDeletion = errors.New("the organization is pending deletion; no new job can be started")

// OrgPendingDeletionSignal records that a write of the request was refused
// because of the grace period, so the route middleware answers 423 even
// when the handler maps the error to another status (its cache of the
// deletion state was older than the database).
type OrgPendingDeletionSignal struct{ refused atomic.Bool }

type orgPendingDeletionKey struct{}

// WithOrgPendingDeletionSignal returns ctx with a new signal.
func WithOrgPendingDeletionSignal(ctx context.Context) (context.Context, *OrgPendingDeletionSignal) {
	s := &OrgPendingDeletionSignal{}
	return context.WithValue(ctx, orgPendingDeletionKey{}, s), s
}

// SignalOrgPendingDeletion marks the signal of ctx, if any.
func SignalOrgPendingDeletion(ctx context.Context) {
	if s, ok := ctx.Value(orgPendingDeletionKey{}).(*OrgPendingDeletionSignal); ok {
		s.refused.Store(true)
	}
}

// Refused reports whether a write of the request was refused.
func (s *OrgPendingDeletionSignal) Refused() bool { return s != nil && s.refused.Load() }

// writeOrgPendingDeletion answers 423 org-pending-deletion without due_at
// (the route middleware adds it when it knows it).
func writeOrgPendingDeletion(w http.ResponseWriter) {
	WriteProblem(w, http.StatusLocked, ProblemOrgPendingDeletion, "Organization pending deletion",
		"the organization is read-only until its deletion: only the cancel and the export are possible")
}
