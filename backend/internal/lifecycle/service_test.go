package lifecycle

import (
	"context"
	"strings"
	"testing"
)

// fakeStateStore is an in-memory StateStore for unit tests.
type fakeStateStore struct {
	states map[string]string
}

func newFakeStateStore() *fakeStateStore {
	return &fakeStateStore{states: map[string]string{}}
}

func (f *fakeStateStore) key(orgID, entityType, entityID string) string {
	return orgID + "/" + entityType + "/" + entityID
}

func (f *fakeStateStore) CurrentState(_ context.Context, orgID, entityType, entityID string) (string, error) {
	return f.states[f.key(orgID, entityType, entityID)], nil
}

func (f *fakeStateStore) SetState(_ context.Context, orgID, entityType, entityID, state string) error {
	f.states[f.key(orgID, entityType, entityID)] = state
	return nil
}

func newTestService(t *testing.T) (*Service, *fakeStateStore) {
	t.Helper()
	repo := NewMemoryRepository()
	states := newFakeStateStore()
	return NewService(repo, states, nil), states
}

func TestDefinitionForKeyLoadsStatesAndTransitions(t *testing.T) {
	svc, _ := newTestService(t)
	def, err := svc.DefinitionForKey(context.Background(), "org-1", "physical_asset")
	if err != nil {
		t.Fatalf("DefinitionForKey: %v", err)
	}
	if len(def.States) == 0 {
		t.Fatal("definition must include states (regression: transitions failed with 'unknown lifecycle state')")
	}
	if len(def.Transitions) == 0 {
		t.Fatal("definition must include transitions")
	}
}

func TestDefinitionForKeyPrefersTenantScoped(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SeedDefinition(Definition{
		Key:            "physical_asset",
		Name:           "Tenant Override",
		OrganizationID: "org-1",
		AppliesTo:      "asset",
		States:         []State{{Key: "custom", IsInitial: true}},
	})
	svc := NewService(repo, newFakeStateStore(), nil)

	def, err := svc.DefinitionForKey(context.Background(), "org-1", "physical_asset")
	if err != nil {
		t.Fatalf("DefinitionForKey: %v", err)
	}
	if def.Name != "Tenant Override" {
		t.Fatalf("expected tenant-scoped definition, got %q", def.Name)
	}

	// A different tenant still resolves the global system definition.
	other, err := svc.DefinitionForKey(context.Background(), "org-2", "physical_asset")
	if err != nil {
		t.Fatalf("DefinitionForKey other org: %v", err)
	}
	if other.Name == "Tenant Override" {
		t.Fatal("other tenant must not see org-1's definition")
	}
}

func TestDefinitionForKeyNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.DefinitionForKey(context.Background(), "org-1", "does-not-exist"); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestTransitionFullPhysicalAssetChain(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	steps := []struct {
		to      string
		context map[string]any
	}{
		{"ordered", nil},
		{"received", nil},
		{"in_stock", map[string]any{"storage_location": "warehouse-1"}},
		{"reserved", nil},
		{"preparing", nil},
		{"deployed", map[string]any{"deployment_location": "rack-1"}},
		{"repair", nil},
		{"in_stock", map[string]any{"storage_location": "warehouse-1"}},
		{"retired", nil},
		{"disposed", map[string]any{"disposal_date": "2026-01-01", "disposal_record": "rec-1"}},
	}
	for _, step := range steps {
		res, err := svc.Transition(ctx, "org-1", "asset", "asset-1", "physical_asset",
			TransitionRequest{ToState: step.to, Context: step.context})
		if err != nil {
			t.Fatalf("transition to %q: %v", step.to, err)
		}
		if res.ToState != step.to {
			t.Fatalf("expected ToState %q, got %q", step.to, res.ToState)
		}
	}
}

func TestTransitionRejectsMissingRequiredFields(t *testing.T) {
	svc, states := newTestService(t)
	ctx := context.Background()
	_ = states.SetState(ctx, "org-1", "asset", "asset-1", "received")

	_, err := svc.Transition(ctx, "org-1", "asset", "asset-1", "physical_asset",
		TransitionRequest{ToState: "in_stock"})
	if err == nil || !strings.Contains(err.Error(), "storage_location") {
		t.Fatalf("expected required-field error for storage_location, got %v", err)
	}
}

func TestTransitionRejectsInvalidEdge(t *testing.T) {
	svc, states := newTestService(t)
	ctx := context.Background()
	_ = states.SetState(ctx, "org-1", "asset", "asset-1", "disposed")

	_, err := svc.Transition(ctx, "org-1", "asset", "asset-1", "physical_asset",
		TransitionRequest{ToState: "deployed", Context: map[string]any{"deployment_location": "x"}})
	if err == nil || !strings.Contains(err.Error(), "no lifecycle transition") {
		t.Fatalf("expected invalid-edge error, got %v", err)
	}
}

func TestTransitionInitialStateRules(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// A fresh entity may not jump into a non-initial state without a
	// from-any transition.
	_, err := svc.Transition(ctx, "org-1", "asset", "fresh", "physical_asset",
		TransitionRequest{ToState: "repair"})
	if err == nil {
		t.Fatal("expected error entering non-initial state on fresh entity")
	}

	// Entering the initial state is allowed.
	if _, err := svc.Transition(ctx, "org-1", "asset", "fresh", "physical_asset",
		TransitionRequest{ToState: "ordered"}); err != nil {
		t.Fatalf("initial state transition: %v", err)
	}
}

func TestTransitionRequiresToState(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Transition(context.Background(), "org-1", "asset", "a", "physical_asset",
		TransitionRequest{}); err == nil {
		t.Fatal("expected error for missing to_state")
	}
}
