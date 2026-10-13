package reviews

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// Effect carries out the domain side of a resolution. It may complete
// res.Note. When it persists the resolution itself (in its own
// transaction) it returns resolved = true.
type Effect func(ctx context.Context, item *Item, res *Resolution) (resolved bool, err error)

// Notifier is told about every newly opened item (NTF-05). The notification
// module attaches here once it exists (WP-140).
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
// (REC-08). It reports whether a new item was opened.
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
	slog.InfoContext(ctx, "review item opened", "event", "review.opened", "organization_id", item.OrganizationID,
		"review_item_id", item.ID, "kind", item.Kind)
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
// status follows the effect (JOB-04).
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
		_ = s.jobs.Mirror(ctx, item.OrganizationID, "review_item", item.ID, jobs.StatusFailed, err.Error())
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
