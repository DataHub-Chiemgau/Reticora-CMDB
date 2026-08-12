package privacy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

func fixture(t *testing.T) (*Service, *contact.MemoryRepository, *user.MemoryRepository) {
	t.Helper()
	repo := NewMemoryRepository()
	contacts := contact.NewMemoryRepository()
	users := user.NewMemoryRepository()
	return NewService(repo, contacts, users), contacts, users
}

func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }

func TestRetentionPolicyLifecycle(t *testing.T) {
	svc, _, _ := fixture(t)
	ctx := context.Background()

	if _, err := svc.GetPolicy(ctx, "org-1"); err != ErrNoPolicy {
		t.Fatalf("expected ErrNoPolicy, got %v", err)
	}

	policy, err := svc.Configure(ctx, "org-1", UpdatePolicyRequest{RetentionDays: intPtr(90), Mode: strPtr("delete")})
	if err != nil {
		t.Fatal(err)
	}
	if policy.RetentionDays != 90 || policy.Mode != "delete" {
		t.Fatalf("unexpected policy: %+v", policy)
	}

	// Partial update keeps the untouched fields.
	updated, err := svc.Configure(ctx, "org-1", UpdatePolicyRequest{RetentionDays: intPtr(30)})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RetentionDays != 30 || updated.Mode != "delete" {
		t.Fatalf("unexpected updated policy: %+v", updated)
	}

	if _, err := svc.Configure(ctx, "org-1", UpdatePolicyRequest{RetentionDays: intPtr(-1)}); err == nil {
		t.Fatal("negative retention must be rejected")
	}
	if _, err := svc.Configure(ctx, "org-1", UpdatePolicyRequest{Mode: strPtr("shred")}); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
}

func TestErasureRequiresPolicy(t *testing.T) {
	svc, _, _ := fixture(t)
	if _, err := svc.RunErasure(context.Background(), "org-1"); err != ErrNoPolicy {
		t.Fatalf("expected ErrNoPolicy, got %v", err)
	}
}

func TestErasureAnonymizesExpiredContactsAndUsers(t *testing.T) {
	svc, contacts, users := fixture(t)
	ctx := context.Background()

	if _, err := svc.Configure(ctx, "org-1", UpdatePolicyRequest{RetentionDays: intPtr(30)}); err != nil {
		t.Fatal(err)
	}

	// An expired contact (updated long ago) and a fresh one.
	expired := &contact.Contact{OrganizationID: "org-1", DisplayName: "Old Pete", Email: "pete@example.com", Phone: "123"}
	if err := contacts.Create(ctx, expired); err != nil {
		t.Fatal(err)
	}
	fresh := &contact.Contact{OrganizationID: "org-1", DisplayName: "New Nora", Email: "nora@example.com"}
	if err := contacts.Create(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	// Age the expired contact beyond the window.
	contacts.AgeForTesting(expired.ID, 60*24*time.Hour)

	// An expired inactive user and an active one.
	inactive := &user.User{OrganizationID: "org-1", Email: "former@example.com", DisplayName: "Former", Status: "inactive"}
	if err := users.CreateUser(ctx, inactive); err != nil {
		t.Fatal(err)
	}
	users.AgeForTesting(inactive.ID, 60*24*time.Hour)
	active := &user.User{OrganizationID: "org-1", Email: "current@example.com", DisplayName: "Current", Status: "active"}
	if err := users.CreateUser(ctx, active); err != nil {
		t.Fatal(err)
	}
	users.AgeForTesting(active.ID, 60*24*time.Hour)

	summary, err := svc.RunErasure(ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ContactsAffected != 1 {
		t.Fatalf("expected 1 erased contact, got %d", summary.ContactsAffected)
	}
	if summary.UsersAffected != 1 {
		t.Fatalf("expected 1 erased user, got %d", summary.UsersAffected)
	}

	got, err := contacts.GetByID(ctx, "org-1", expired.ID)
	if err != nil {
		t.Fatalf("expired contact must remain (anonymize mode): %v", err)
	}
	if strings.Contains(got.Email, "pete") || strings.Contains(got.DisplayName, "Pete") {
		t.Fatalf("contact was not anonymized: %+v", got)
	}
	if !strings.HasSuffix(got.Email, "@anonymized.invalid") {
		t.Fatalf("expected surrogate e-mail, got %q", got.Email)
	}

	gotUser, err := users.GetUser(ctx, "org-1", inactive.ID)
	if err != nil {
		t.Fatalf("inactive user must remain (anonymize mode): %v", err)
	}
	if strings.Contains(gotUser.Email, "former") {
		t.Fatalf("user was not anonymized: %+v", gotUser)
	}

	// Fresh contact and active user (despite being old) must be untouched.
	freshGot, err := contacts.GetByID(ctx, "org-1", fresh.ID)
	if err != nil || freshGot.Email != "nora@example.com" {
		t.Fatalf("fresh contact must be untouched: %+v, %v", freshGot, err)
	}
	activeGot, err := users.GetUser(ctx, "org-1", active.ID)
	if err != nil || activeGot.Email != "current@example.com" {
		t.Fatalf("active user must be untouched: %+v, %v", activeGot, err)
	}
}

func TestErasureDeleteModeRemovesRows(t *testing.T) {
	svc, contacts, users := fixture(t)
	ctx := context.Background()

	if _, err := svc.Configure(ctx, "org-1", UpdatePolicyRequest{RetentionDays: intPtr(30), Mode: strPtr("delete")}); err != nil {
		t.Fatal(err)
	}

	old := &contact.Contact{OrganizationID: "org-1", DisplayName: "Old", Email: "old@example.com"}
	if err := contacts.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	contacts.AgeForTesting(old.ID, 90*24*time.Hour)

	inactive := &user.User{OrganizationID: "org-1", Email: "gone@example.com", DisplayName: "Gone", Status: "inactive"}
	if err := users.CreateUser(ctx, inactive); err != nil {
		t.Fatal(err)
	}
	users.AgeForTesting(inactive.ID, 90*24*time.Hour)

	summary, err := svc.RunErasure(ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ContactsAffected != 1 || summary.UsersAffected != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if _, err := contacts.GetByID(ctx, "org-1", old.ID); err == nil {
		t.Fatal("deleted contact must be gone")
	}
	if _, err := users.GetUser(ctx, "org-1", inactive.ID); err == nil {
		t.Fatal("deleted user must be gone")
	}
}

func TestErasureWithKeepForeverPolicyIsRejected(t *testing.T) {
	svc, _, _ := fixture(t)
	ctx := context.Background()
	if _, err := svc.Configure(ctx, "org-1", UpdatePolicyRequest{RetentionDays: intPtr(0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunErasure(ctx, "org-1"); err == nil {
		t.Fatal("erasure with retention_days=0 must be rejected as a no-op safeguard")
	}
}
