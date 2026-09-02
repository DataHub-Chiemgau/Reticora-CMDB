package ci

import (
	"context"
	"errors"
	"testing"
)

type stubEffectiveSource struct {
	values map[string]any
	err    error
}

func (s stubEffectiveSource) EffectiveValues(context.Context, string, string) (map[string]any, error) {
	return s.values, s.err
}

func TestGetByIDSurfacesEffectiveAttributes(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo).WithEffectiveValues(stubEffectiveSource{
		values: map[string]any{"ram_gb": float64(64)},
	})
	ctx := context.Background()

	item := &Item{OrganizationID: "org", Name: "srv", CITypeID: "type", Attributes: map[string]any{"ram_gb": float64(32), "cpu": "x"}}
	if err := svc.Create(ctx, item); err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetByID(ctx, "org", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attributes["ram_gb"] != float64(32) {
		t.Fatalf("raw attributes must stay untouched, got %v", got.Attributes["ram_gb"])
	}
	if got.EffectiveAttributes["ram_gb"] != float64(64) {
		t.Fatalf("effective override value must surface, got %v", got.EffectiveAttributes["ram_gb"])
	}
	if got.EffectiveAttributes["cpu"] != "x" {
		t.Fatalf("non-overridden attributes must carry over, got %v", got.EffectiveAttributes["cpu"])
	}
}

func TestGetByIDToleratesEffectiveSourceFailure(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo).WithEffectiveValues(stubEffectiveSource{err: errors.New("boom")})
	ctx := context.Background()

	item := &Item{OrganizationID: "org", Name: "srv", CITypeID: "type"}
	if err := svc.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetByID(ctx, "org", item.ID)
	if err != nil {
		t.Fatalf("read must not fail on provenance errors: %v", err)
	}
	if got.EffectiveAttributes != nil {
		t.Fatal("effective attributes must be omitted on lookup failure")
	}
}

func TestGetByIDWithoutSourceOmitsEffectiveAttributes(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	item := &Item{OrganizationID: "org", Name: "srv", CITypeID: "type"}
	if err := svc.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetByID(ctx, "org", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectiveAttributes != nil {
		t.Fatal("no effective attributes expected without a source")
	}
}
