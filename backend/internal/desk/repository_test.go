package desk

import (
	"context"
	"testing"
	"time"
)

func TestDeskBookingOverlapRejected(t *testing.T) {
	repo := NewMemoryRepository()
	d := &Desk{OrganizationID: "org-1", Name: "Desk 1"}
	if err := repo.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	b1 := &Booking{OrganizationID: "org-1", DeskID: d.ID, UserID: "u1", StartsAt: now, EndsAt: now.Add(2 * time.Hour)}
	if _, err := repo.Book(context.Background(), b1); err != nil {
		t.Fatal(err)
	}
	// overlap rejected
	b2 := &Booking{OrganizationID: "org-1", DeskID: d.ID, UserID: "u2", StartsAt: now.Add(time.Hour), EndsAt: now.Add(3 * time.Hour)}
	if _, err := repo.Book(context.Background(), b2); err == nil {
		t.Fatal("expected overlap rejection")
	}
	// non-overlapping ok
	b3 := &Booking{OrganizationID: "org-1", DeskID: d.ID, UserID: "u2", StartsAt: now.Add(3 * time.Hour), EndsAt: now.Add(4 * time.Hour)}
	if _, err := repo.Book(context.Background(), b3); err != nil {
		t.Fatalf("expected non-overlap booking, got %v", err)
	}
	// cancel frees the slot
	if _, err := repo.CancelBooking(context.Background(), "org-1", b1.ID); err != nil {
		t.Fatal(err)
	}
	b4 := &Booking{OrganizationID: "org-1", DeskID: d.ID, UserID: "u2", StartsAt: now.Add(time.Hour), EndsAt: now.Add(90 * time.Minute)}
	if _, err := repo.Book(context.Background(), b4); err != nil {
		t.Fatalf("expected booking after cancel, got %v", err)
	}
}
