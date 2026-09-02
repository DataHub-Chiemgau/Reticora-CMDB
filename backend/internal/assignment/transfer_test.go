package assignment

import (
	"context"
	"testing"
)

func TestTransferAtomicityInvalidSuccessorLeavesOriginalActive(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	orig := &Assignment{OrganizationID: "org", AssetID: "asset-1", AssignedTo: "alice", Status: "active", AssignmentType: "permanent"}
	if err := repo.Create(ctx, orig); err != nil {
		t.Fatal(err)
	}

	err := repo.Transfer(ctx, "org", orig.ID, &Assignment{OrganizationID: "org", AssetID: "asset-1", Status: "active"})
	if err == nil {
		t.Fatal("expected an error for a successor without an assignee")
	}
	got, err := repo.GetByID(ctx, "org", orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "active" {
		t.Fatalf("original assignment must stay active after failed transfer, got %q", got.Status)
	}
}

func TestTransferSuccess(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	orig := &Assignment{OrganizationID: "org", AssetID: "asset-1", AssignedTo: "alice", Status: "active", AssignmentType: "permanent"}
	if err := repo.Create(ctx, orig); err != nil {
		t.Fatal(err)
	}
	successor := &Assignment{OrganizationID: "org", AssetID: "asset-1", AssignedTo: "bob", Status: "active", AssignmentType: "transfer"}
	if err := repo.Transfer(ctx, "org", orig.ID, successor); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByID(ctx, "org", orig.ID)
	if got.Status != "transferred" {
		t.Fatalf("original must be transferred, got %q", got.Status)
	}
	if successor.ID == "" {
		t.Fatal("successor must be persisted")
	}
}

func TestTransferCrossTenantRejected(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	orig := &Assignment{OrganizationID: "org-a", AssetID: "asset-1", AssignedTo: "alice", Status: "active"}
	if err := repo.Create(ctx, orig); err != nil {
		t.Fatal(err)
	}
	err := repo.Transfer(ctx, "org-b", orig.ID, &Assignment{AssignedTo: "bob", Status: "active"})
	if err == nil {
		t.Fatal("cross-tenant transfer must fail")
	}
}
