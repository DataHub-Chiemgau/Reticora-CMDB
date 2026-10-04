package desk_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/desk"
)

// TestConcurrentBookingsDoNotOverlap books one desk for the same window from
// many goroutines at once (MGT-05, WP-029). The exclusion constraint of
// migration 000065 lets exactly one booking through; all others fail with
// ErrAlreadyBooked. Adjacent windows stay bookable.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestConcurrentBookingsDoNotOverlap(t *testing.T) {
	f := scopetest.Seed(t, "49")
	room := f.Room(t, f.Client1, "booking-room")
	user := f.AppUser(t, f.OrgA, "booking-user")
	repo := desk.NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)
	d := &desk.Desk{OrganizationID: f.OrgA, RoomID: room, Name: "hot-desk", Status: "available", Attributes: map[string]any{}}
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("create desk: %v", err)
	}

	start := time.Now().UTC().Truncate(time.Second).Add(24 * time.Hour)
	const workers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		booked   int
		rejected int
		other    []error
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(offset time.Duration) {
			defer wg.Done()
			// The windows differ but all overlap the first hour.
			_, err := repo.Book(ctx, &desk.Booking{OrganizationID: f.OrgA, DeskID: d.ID, UserID: user,
				StartsAt: start.Add(offset), EndsAt: start.Add(time.Hour + offset)})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				booked++
			case errors.Is(err, desk.ErrAlreadyBooked):
				rejected++
			default:
				other = append(other, err)
			}
		}(time.Duration(i) * time.Minute)
	}
	wg.Wait()
	if booked != 1 || rejected != workers-1 || len(other) != 0 {
		t.Fatalf("booked=%d rejected=%d other=%v, want 1/%d/none", booked, rejected, other, workers-1)
	}

	// The window right after the longest booked one is free.
	if _, err := repo.Book(ctx, &desk.Booking{OrganizationID: f.OrgA, DeskID: d.ID, UserID: user,
		StartsAt: start.Add(2 * time.Hour), EndsAt: start.Add(3 * time.Hour)}); err != nil {
		t.Errorf("adjacent booking: %v", err)
	}
}
