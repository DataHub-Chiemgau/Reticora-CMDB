package reviews_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
)

// TestTST01ReviewResolution is the review part of the TST-01 matrix
// (WP-202) against PostgreSQL:
//   - every review type accepts exactly its actions of REC-08; any other
//     action is refused with 422 and leaves the item open;
//   - a resolved item cannot be resolved twice (409);
//   - a failing effect leaves the item open, a succeeding one resolves it;
//   - a dismissal suppresses the same value until it changes (REC-12).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTST01ReviewResolution(t *testing.T) {
	f := scopetest.Seed(t, "c0")
	ctx := f.OrgCtx(f.OrgA)
	repo := reviews.NewPGRepository(f.App)
	failing := errors.New("effect failed")
	failNext := false
	svc := reviews.NewService(repo)
	for _, kind := range reviews.Kinds {
		svc.WithEffect(kind, func(context.Context, *reviews.Item, *reviews.Resolution) (bool, error) {
			if failNext {
				return false, failing
			}
			return false, nil
		})
	}
	ci := f.CI(t, f.OrgA, f.Client1, "tst01-review")
	n := 0
	open := func(t *testing.T, kind string) *reviews.Item {
		t.Helper()
		n++
		// One CI per item: serial_mismatch allows one open item per CI.
		subject := f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("tst01-review-%d", n))
		item := &reviews.Item{OrganizationID: f.OrgA, Kind: kind, ObjectKey: fmt.Sprintf("tst01:%d", n),
			CandidateCIIDs: []string{subject}, Payload: map[string]any{"ci_id": subject}}
		if created, err := svc.Open(ctx, item); err != nil || !created {
			t.Fatalf("open %s: %v (created %v)", kind, err, created)
		}
		return item
	}
	status := func(t *testing.T, id string) string {
		t.Helper()
		item, err := svc.Get(ctx, f.OrgA, id)
		if err != nil {
			t.Fatal(err)
		}
		return item.Status
	}

	for _, kind := range reviews.Kinds {
		for _, action := range []string{reviews.ActionMerge, reviews.ActionNew, reviews.ActionAccept, reviews.ActionDismiss} {
			t.Run(fmt.Sprintf("%s/%s", kind, action), func(t *testing.T) {
				item := open(t, kind)
				_, err := svc.Resolve(ctx, f.OrgA, item.ID, &reviews.Resolution{Action: action, CIID: ci, ResolvedBy: f.User})
				if !reviews.Allowed(kind, action) {
					var refused *reviews.Error
					if !errors.As(err, &refused) || refused.Status != http.StatusUnprocessableEntity {
						t.Fatalf("not allowed: %v", err)
					}
					if got := status(t, item.ID); got != reviews.StatusOpen {
						t.Errorf("refused action changed the status to %s", got)
					}
					return
				}
				if err != nil {
					t.Fatalf("allowed: %v", err)
				}
				want := reviews.StatusResolved
				if action == reviews.ActionDismiss {
					want = reviews.StatusDismissed
				}
				if got := status(t, item.ID); got != want {
					t.Errorf("status %s, want %s", got, want)
				}
			})
		}
	}

	t.Run("a resolved item is not resolved twice", func(t *testing.T) {
		item := open(t, reviews.KindDuplicateIP)
		if _, err := svc.Resolve(ctx, f.OrgA, item.ID, &reviews.Resolution{Action: reviews.ActionDismiss}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Resolve(ctx, f.OrgA, item.ID, &reviews.Resolution{Action: reviews.ActionDismiss}); !errors.Is(err, reviews.ErrAlreadyResolved) {
			t.Errorf("second resolution: %v", err)
		}
	})

	t.Run("a failing effect leaves the item open", func(t *testing.T) {
		item := open(t, reviews.KindConflictingValues)
		failNext = true
		_, err := svc.Resolve(ctx, f.OrgA, item.ID, &reviews.Resolution{Action: reviews.ActionAccept})
		failNext = false
		if !errors.Is(err, failing) {
			t.Fatalf("resolve: %v", err)
		}
		if got := status(t, item.ID); got != reviews.StatusOpen {
			t.Errorf("status %s after a failed effect", got)
		}
	})

	t.Run("a dismissal suppresses the same value until it changes", func(t *testing.T) {
		key := "tst01:dismissed"
		first := &reviews.Item{OrganizationID: f.OrgA, Kind: reviews.KindOverrideConflict, ObjectKey: key,
			Payload: map[string]any{"ci_id": ci, "observed_value": "v1"}}
		if created, err := svc.OpenUnlessDismissed(ctx, first, "observed_value"); err != nil || !created {
			t.Fatalf("first: %v %v", created, err)
		}
		if _, err := svc.Resolve(ctx, f.OrgA, first.ID, &reviews.Resolution{Action: reviews.ActionDismiss}); err != nil {
			t.Fatal(err)
		}
		same := &reviews.Item{OrganizationID: f.OrgA, Kind: reviews.KindOverrideConflict, ObjectKey: key,
			Payload: map[string]any{"ci_id": ci, "observed_value": "v1"}}
		if created, err := svc.OpenUnlessDismissed(ctx, same, "observed_value"); err != nil || created {
			t.Errorf("same value after dismissal: created %v, %v", created, err)
		}
		changed := &reviews.Item{OrganizationID: f.OrgA, Kind: reviews.KindOverrideConflict, ObjectKey: key,
			Payload: map[string]any{"ci_id": ci, "observed_value": "v2"}}
		if created, err := svc.OpenUnlessDismissed(ctx, changed, "observed_value"); err != nil || !created {
			t.Errorf("changed value after dismissal: created %v, %v", created, err)
		}
	})
}
