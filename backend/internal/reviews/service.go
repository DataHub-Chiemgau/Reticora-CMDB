package reviews

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// Effect carries out the domain side of a resolution. It may complete
// res.Note. When it persists the resolution itself (in its own
// transaction) it returns resolved = true.
type Effect func(ctx context.Context, item *Item, res *Resolution) (resolved bool, err error)

// Notifier is told about every newly opened item. It is an in-process hook
// only: review.created (NTF-05, EVT-01) is written by the repository in the
// transaction that inserts the item (PGRepository.CreateReviewItem, WP-549),
// so a notifier must not write the event a second time.
type Notifier interface {
	ReviewOpened(ctx context.Context, item *Item)
}

// Service is the review queue.
type Service struct {
	repo     Repository
	effects  map[string]Effect
	notifier Notifier
	// jobs records every merge resolution as a job (JOB-04).
	jobs jobs.Tracker
}

// MergeJobKind is the job kind of a review merge (JOB-04).
const MergeJobKind = "review.merge"

// WithJobs records the merge resolutions as jobs of kind review.merge with
// the review item as reference (JOB-04).
func (s *Service) WithJobs(tracker jobs.Tracker) *Service {
	s.jobs = tracker
	return s
}

// NewService returns the queue over repo.
func NewService(repo Repository) *Service {
	return &Service{repo: repo, effects: map[string]Effect{}}
}

// WithEffect registers the effect of resolving items of kind.
func (s *Service) WithEffect(kind string, effect Effect) *Service {
	s.effects[kind] = effect
	return s
}

// WithNotifier attaches the notification hook.
func (s *Service) WithNotifier(n Notifier) *Service {
	s.notifier = n
	return s
}

// Open opens an item; an open item of the same object and type is reused
// (REC-08). It reports whether a new item was opened; the PostgreSQL
// repository has then written review.created with it.
func (s *Service) Open(ctx context.Context, item *Item) (bool, error) {
	if err := s.repo.CreateReviewItem(ctx, item); err != nil {
		return false, err
	}
	if item.Deduplicated {
		return false, nil
	}
	if s.notifier != nil {
		s.notifier.ReviewOpened(ctx, item)
	}
	// Inside a transaction in the context (the ingest's record transaction)
	// the event is logged once the item has committed: a rolled back item
	// never existed.
	orgID, id, kind := item.OrganizationID, item.ID, item.Kind
	database.AfterCommit(ctx, func(ctx context.Context) {
		slog.InfoContext(ctx, "review item opened", "event", "review.opened", "organization_id", orgID,
			"review_item_id", id, "kind", kind)
	})
	return true, nil
}

// OpenUnlessDismissed opens an item unless the latest dismissed item of the
// same object and type reported the same value under valueKey: a dismissal
// suppresses that value until it changes (REC-12). It reports whether a new
// item was opened.
func (s *Service) OpenUnlessDismissed(ctx context.Context, item *Item, valueKey string) (bool, error) {
	if item.ObjectKey != "" {
		dismissed, _, err := s.repo.ListReviewItems(ctx, item.OrganizationID,
			Filter{Kind: item.Kind, ObjectKey: item.ObjectKey, Status: StatusDismissed}, api.PaginationParams{Limit: 1})
		if err != nil {
			return false, err
		}
		if len(dismissed) > 0 && sameValue(dismissed[0].Payload[valueKey], item.Payload[valueKey]) {
			return false, nil
		}
	}
	return s.Open(ctx, item)
}

// OverrideConflict is a value an automation source reported for a field
// under a manual override (REC-12).
type OverrideConflict struct {
	OrganizationID string
	CIID           string
	Field          string
	// Value is the reported value, Source its source and Rank the REC-03
	// rank of the source.
	Value  any
	Source string
	Rank   int
	// Previous is the value reported for the field before this report (the
	// CI's _observed as read before the report was recorded); HasPrevious
	// is false when none is known.
	Previous    any
	HasPrevious bool
}

// OpenOverrideConflict opens the override_conflict item of a field for
// every automation source, discovery, workflow and agent alike (REC-12): one
// open item per CI and field, the payload carries the source, its rank and
// the reported value. A dismissal suppresses its value only until the source
// reports another one (F-248): no item is opened when the newest item of the
// field was dismissed with this value and the report before this one had
// the same value; after another value in between (or an accepted item) the
// value opens a new item. Inside a transaction it runs under a savepoint, so
// a failed item leaves the caller's transaction usable. It reports whether a
// new item was opened.
func (s *Service) OpenOverrideConflict(ctx context.Context, c *OverrideConflict) (bool, error) {
	item := &Item{
		OrganizationID: c.OrganizationID,
		Kind:           KindOverrideConflict,
		Status:         StatusOpen,
		Payload: map[string]any{"ci_id": c.CIID, "field": c.Field, "observed_value": c.Value,
			"source": c.Source, "rank": c.Rank},
		CandidateCIIDs: []string{c.CIID},
		ObjectKey:      "ci:" + c.CIID + "/" + c.Field,
	}
	var opened bool
	err := database.Savepoint(ctx, func(ctx context.Context) error {
		newest, _, err := s.repo.ListReviewItems(ctx, item.OrganizationID,
			Filter{Kind: item.Kind, ObjectKey: item.ObjectKey}, api.PaginationParams{Limit: 1})
		if err != nil {
			return err
		}
		if len(newest) > 0 && newest[0].Status == StatusDismissed && sameValue(newest[0].Payload["observed_value"], c.Value) &&
			(!c.HasPrevious || sameValue(c.Previous, c.Value)) {
			return nil
		}
		opened, err = s.Open(ctx, item)
		return err
	})
	if err != nil {
		return false, err
	}
	return opened, nil
}

func sameValue(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// List lists items.
func (s *Service) List(ctx context.Context, orgID string, filter Filter, page api.PaginationParams) ([]Item, int, error) {
	return s.repo.ListReviewItems(ctx, orgID, filter, page)
}

// Get returns one item.
func (s *Service) Get(ctx context.Context, orgID, id string) (*Item, error) {
	item, err := s.repo.GetReviewItem(ctx, orgID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return item, nil
}

// legacyActions are the action names of the deprecated discovery endpoint.
var legacyActions = map[string]string{"create": ActionNew}

// Resolve resolves an open item with one of the actions its type allows
// (REC-08) and the effect registered for the type.
func (s *Service) Resolve(ctx context.Context, orgID, id string, res *Resolution) (*Item, error) {
	item, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if item.Status != StatusOpen {
		return nil, ErrAlreadyResolved
	}
	if action, ok := legacyActions[res.Action]; ok {
		res.Action = action
	}
	if !Allowed(item.Kind, res.Action) {
		return nil, &Error{Status: http.StatusUnprocessableEntity,
			Message: "a " + item.Kind + " item is resolved by " + joinActions(AllowedActions[item.Kind])}
	}
	res.Status = StatusResolved
	if res.Action == ActionDismiss {
		res.Status = StatusDismissed
	}
	effect, ok := s.effects[item.Kind]
	if !ok && res.Action != ActionDismiss {
		return nil, &Error{Status: http.StatusUnprocessableEntity, Message: "no " + res.Action + " is available for " + item.Kind + " items yet"}
	}
	resolved := false
	if ok {
		if resolved, err = s.runEffect(ctx, effect, item, res); err != nil {
			return nil, err
		}
	}
	if res.Note == "" {
		res.Note = res.Action
	}
	if resolved {
		return s.Get(ctx, orgID, id)
	}
	out, err := s.repo.ResolveReviewItem(ctx, orgID, id, res)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "review item resolved", "event", "review.resolved", "organization_id", orgID,
		"review_item_id", id, "kind", item.Kind, "action", res.Action)
	return out, nil
}

// runEffect runs the effect of a resolution; a merge runs as a job whose
// status follows the effect (JOB-04). The merge runs in the request and the
// review.merge job mirrors it (E-89, transitional rule: option 1); the
// merge of a probable_duplicate item is the ci.merge job of the CI-12
// merge, whose status the review.merge job takes. There is one job per item:
// a retry after a failed merge runs that job again, failed → running with
// one more attempt and without the old error, and it ends succeeded or
// failed with the retry (F-330).
func (s *Service) runEffect(ctx context.Context, effect Effect, item *Item, res *Resolution) (bool, error) {
	if s.jobs == nil || res.Action != ActionMerge {
		return effect(ctx, item, res)
	}
	actor := res.ResolvedBy
	if err := s.jobs.Track(ctx, item.OrganizationID, MergeJobKind, "review_item", item.ID, actor); err != nil {
		return false, err
	}
	_ = s.jobs.Mirror(ctx, item.OrganizationID, "review_item", item.ID, jobs.StatusRunning, "")
	resolved, err := effect(ctx, item, res)
	if err != nil {
		// The job shows a refusal as it is, any other error sanitized; the
		// raw error goes to the log (F-372).
		_ = jobs.MirrorFailed(ctx, s.jobs, item.OrganizationID, "review_item", item.ID, err)
		return false, err
	}
	_ = s.jobs.Mirror(ctx, item.OrganizationID, "review_item", item.ID, jobs.StatusSucceeded, "")
	return resolved, nil
}

func joinActions(actions []string) string {
	out := ""
	for i, a := range actions {
		switch {
		case i == 0:
		case i == len(actions)-1:
			out += " or "
		default:
			out += ", "
		}
		out += a
	}
	return out
}
