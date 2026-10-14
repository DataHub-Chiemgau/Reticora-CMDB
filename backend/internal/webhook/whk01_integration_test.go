package webhook_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// TestWHK01AgainstPostgres covers the stores of WHK-01 against PostgreSQL:
// dead deliveries are counted per subscription and window, a subscription
// is deactivated in its own tenant context, and a dead delivery is queued
// again (pending, fresh budget, out of the dead-letter queue) while a
// pending one cannot be retried.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestWHK01AgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "cd")
	ctx := f.OrgCtx(f.OrgA)
	bg := context.Background()
	repo := webhook.NewPGRepository(f.App)
	store := webhook.NewPGDeliveryStore(f.App)
	sub := &webhook.Subscription{OrganizationID: f.OrgA, Name: "whk01", URL: "https://whk01.example.invalid/hook", Secret: "s",
		Events: []string{"ci.created"}, IsActive: true}
	if err := repo.Create(ctx, sub); err != nil {
		t.Fatal(err)
	}
	var dead []webhook.DeliveryRecord
	for i := 0; i < 3; i++ {
		rec, err := store.Enqueue(ctx, webhook.DeliveryRecord{OrganizationID: f.OrgA, SubscriptionID: sub.ID, Event: "ci.created",
			Payload: []byte(`{"id":"ci"}`), MaxAttempts: 6})
		if err != nil {
			t.Fatal(err)
		}
		rec.Attempt, rec.Error, rec.Status = 6, "unexpected status 502", webhook.StatusDead
		if err := store.MoveToDeadLetter(bg, rec); err != nil {
			t.Fatal(err)
		}
		dead = append(dead, rec)
	}
	if n, err := store.CountDeadSince(bg, f.OrgA, sub.ID, time.Now().Add(-24*time.Hour)); err != nil || n != 3 {
		t.Fatalf("dead within 24 h: %d (err %v), want 3", n, err)
	}
	if n, err := store.CountDeadSince(bg, f.OrgA, sub.ID, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("dead after the window: %d (err %v), want 0", n, err)
	}

	if err := repo.Deactivate(bg, f.OrgA, sub.ID); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, f.OrgA, sub.ID)
	if err != nil || got.IsActive {
		t.Fatalf("after Deactivate: %+v (err %v)", got, err)
	}
	if matching, lerr := repo.ListByEvent(ctx, f.OrgA, "ci.created"); lerr != nil || len(matching) != 0 {
		t.Fatalf("deactivated subscription still receives events: %+v (err %v)", matching, lerr)
	}

	rec, err := store.Requeue(ctx, f.OrgA, sub.ID, dead[0].ID, time.Now())
	if err != nil || rec.Status != webhook.StatusPending || rec.Attempt != 0 {
		t.Fatalf("requeue: %+v (err %v)", rec, err)
	}
	if n, _ := store.CountDeadSince(bg, f.OrgA, sub.ID, time.Now().Add(-24*time.Hour)); n != 2 {
		t.Errorf("dead letters after the retry: %d, want 2", n)
	}
	if _, err := store.Requeue(ctx, f.OrgA, sub.ID, dead[0].ID, time.Now()); !errors.Is(err, webhook.ErrNotRetryable) {
		t.Errorf("requeue of a pending delivery: %v, want ErrNotRetryable", err)
	}
}
