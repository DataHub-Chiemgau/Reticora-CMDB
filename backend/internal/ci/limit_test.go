package ci

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type stubLimitGuard struct {
	max int64
}

type stubLimitError struct {
	limit   int64
	current int64
}

func (e *stubLimitError) Error() string {
	return fmt.Sprintf("limit %d reached (%d in use)", e.limit, e.current)
}

func (e *stubLimitError) LimitExceeded() bool { return true }

func (g stubLimitGuard) AllowCreate(_ context.Context, _, _ string, current int64) error {
	if current >= g.max {
		return &stubLimitError{limit: g.max, current: current}
	}
	return nil
}

func TestServiceCreateEnforcesEntitlementLimit(t *testing.T) {
	svc := NewServiceWithLimits(NewMemoryRepository(), stubLimitGuard{max: 1})
	ctx := context.Background()

	if err := svc.Create(ctx, &Item{OrganizationID: "org-1", Name: "ci-1", CITypeID: "type-1"}); err != nil {
		t.Fatalf("expected the first CI to be created: %v", err)
	}

	err := svc.Create(ctx, &Item{OrganizationID: "org-1", Name: "ci-2", CITypeID: "type-1"})
	if err == nil {
		t.Fatal("expected the licensed CI limit to be enforced")
	}
	if !isEntitlementError(err) {
		t.Fatalf("expected an entitlement error, got %v", err)
	}

	var limitErr *stubLimitError
	if !errors.As(err, &limitErr) || limitErr.current != 1 {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServiceCreateWithoutGuardIsUnlimited(t *testing.T) {
	svc := NewService(NewMemoryRepository())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := svc.Create(ctx, &Item{OrganizationID: "org-1", Name: fmt.Sprintf("ci-%d", i), CITypeID: "type-1"}); err != nil {
			t.Fatalf("expected creation without a limit guard to succeed: %v", err)
		}
	}
}
