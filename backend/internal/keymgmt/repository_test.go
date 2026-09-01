package keymgmt

import (
	"context"
	"testing"
)

func TestKeyIssueReturnCycle(t *testing.T) {
	repo := NewMemoryRepository()
	item := &Item{OrganizationID: "org-1", Name: "Server room key"}
	if err := repo.Create(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	// issue
	updated, err := repo.Issue(context.Background(), &Assignment{OrganizationID: "org-1", KeyItemID: item.ID, AssignedTo: "u1"})
	if err != nil || updated.Status != "issued" {
		t.Fatalf("issue: %+v err=%v", updated, err)
	}
	// double issue must fail
	if _, err := repo.Issue(context.Background(), &Assignment{OrganizationID: "org-1", KeyItemID: item.ID, AssignedTo: "u2"}); err == nil {
		t.Fatal("expected not available error")
	}
	// return
	updated, err = repo.Return(context.Background(), "org-1", item.ID)
	if err != nil || updated.Status != "available" {
		t.Fatalf("return: %+v err=%v", updated, err)
	}
	// assignment history
	assignments, _ := repo.ListAssignments(context.Background(), "org-1", item.ID)
	if len(assignments) != 1 || assignments[0].ReturnedAt == nil {
		t.Fatalf("expected 1 returned assignment, got %+v", assignments)
	}
}
