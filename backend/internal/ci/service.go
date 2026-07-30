package ci

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for CIs.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Item, error)
	Create(ctx context.Context, item *Item) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error)
	Delete(ctx context.Context, orgID, id string) error
}

// ChangeReader defines persistence operations for CI change history.
type ChangeReader interface {
	ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error)
}

// Service coordinates CI persistence with mutation semantics. Audit-log and
// ci_change rows are written by the repository inside the same transaction as
// the mutation, so history can never diverge from the stored data.
type Service struct {
	repo Repository
}

// NewService creates a CI service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// List returns paginated CIs filtered by the given parameters.
func (s *Service) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
	return s.repo.List(ctx, orgID, filter, page)
}

// GetByID retrieves a single CI by ID.
func (s *Service) GetByID(ctx context.Context, orgID, id string) (*Item, error) {
	return s.repo.GetByID(ctx, orgID, id)
}

// Create inserts a new CI.
func (s *Service) Create(ctx context.Context, item *Item) error {
	return s.repo.Create(ctx, item)
}

// Update modifies an existing CI. The repository loads the prior state inside
// the mutation transaction so the recorded diff cannot race with concurrent
// writers.
func (s *Service) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) {
	return s.repo.Update(ctx, orgID, id, req)
}

// Delete removes a CI after loading its prior state.
func (s *Service) Delete(ctx context.Context, orgID, id string) (*Item, error) {
	item, err := s.repo.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Delete(ctx, orgID, id); err != nil {
		return nil, err
	}
	return item, nil
}

// ListChanges returns paginated CI change history when supported by the repository.
func (s *Service) ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error) {
	reader, ok := s.repo.(ChangeReader)
	if !ok {
		return []Change{}, 0, nil
	}
	return reader.ListChanges(ctx, orgID, ciID, page)
}
