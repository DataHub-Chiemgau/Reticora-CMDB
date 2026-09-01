package training

import (
	"context"
	"testing"
)

func TestTrainingAssignComplete(t *testing.T) {
	repo := NewMemoryRepository()
	c := &Course{OrganizationID: "org-1", Title: "NIS2 Basics"}
	if err := repo.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	a, err := repo.Assign(context.Background(), &Assignment{OrganizationID: "org-1", TrainingID: c.ID, UserID: "u1"})
	if err != nil || a.Status != "assigned" {
		t.Fatalf("assign: %+v err=%v", a, err)
	}
	// duplicate assignment rejected
	if _, err := repo.Assign(context.Background(), &Assignment{OrganizationID: "org-1", TrainingID: c.ID, UserID: "u1"}); err == nil {
		t.Fatal("expected already assigned")
	}
	// complete with proof
	done, err := repo.Complete(context.Background(), "org-1", a.ID, "proof/nis2-u1.pdf")
	if err != nil || done.Status != "completed" || done.ProofObjectKey == "" {
		t.Fatalf("complete: %+v err=%v", done, err)
	}
}
