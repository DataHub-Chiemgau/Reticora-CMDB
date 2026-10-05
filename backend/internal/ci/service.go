package ci

import (
	"context"
	"strings"

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

// LimitFeatureKey is the entitlement feature whose limit caps the number of CIs
// an organization may store.
const LimitFeatureKey = "cmdb"

// ChangeReader defines persistence operations for CI change history.
type ChangeReader interface {
	ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error)
}

// LimitGuard enforces licensed resource limits before a new CI is created.
// It is implemented by the entitlement service.
type LimitGuard interface {
	AllowCreate(ctx context.Context, orgID, featureKey string, current int64) error
}

// Indexer keeps the tenant search index in sync with CI mutations. It is
// implemented by the search backends (Postgres FTS, OpenSearch, hybrid).
// Indexing is best-effort: failures are logged and never fail the underlying
// CI mutation, because the index can always be rebuilt with
// POST /api/v1/search/reindex.
type Indexer interface {
	IndexDocument(ctx context.Context, doc Document) error
	DeleteDocument(ctx context.Context, orgID, entityType, entityID string) error
}

// Document is the search-index representation of a CI. It mirrors
// search.Document; the ci package declares its own copy so it does not
// depend on the search package (search already depends on shared api types,
// and keeping the dependency direction ci-free avoids an import cycle).
type Document struct {
	OrganizationID string
	EntityType     string
	EntityID       string
	Title          string
	Summary        string
	URL            string
	Metadata       map[string]string
	ClientID       string
	SiteID         string
}

// EntityTypeCI is the search entity type under which CIs are indexed.
const EntityTypeCI = "ci"

// IndexDocumentFor builds the search document for a CI, matching the field
// selection the reindex path uses in search.PGRepository.ReindexTenant.
func IndexDocumentFor(item *Item) Document {
	return Document{
		OrganizationID: item.OrganizationID,
		EntityType:     EntityTypeCI,
		EntityID:       item.ID,
		Title:          item.Name,
		Summary:        ciSummary(item),
		URL:            "/cmdb/" + item.ID,
		ClientID:       item.ClientID,
		SiteID:         item.SiteID,
	}
}

func ciSummary(item *Item) string {
	parts := []string{item.Hostname, item.Manufacturer, item.Model, item.SerialNumber, item.OSName, item.OSVersion}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// Service coordinates CI persistence with mutation semantics. Audit-log and
// ci_change rows are written by the repository inside the same transaction as
// the mutation, so history can never diverge from the stored data.
type Service struct {
	repo   Repository
	limit  LimitGuard
	fields FieldResolver
}

// NewService creates a CI service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// NewServiceWithLimits creates a CI service that enforces the tenant's licensed
// CI limit on every create.
func NewServiceWithLimits(repo Repository, limit LimitGuard) *Service {
	return &Service{repo: repo, limit: limit}
}

// WithFieldResolver enables server-side validation of CI attributes against the
// global, CI-type and instance field metadata. Without a resolver the service
// keeps its previous pass-through behaviour, which keeps the constructor
// signatures stable for callers that do not manage field metadata.
func (s *Service) WithFieldResolver(fields FieldResolver) *Service {
	s.fields = fields
	return s
}

// validate checks the effective attributes of a CI against its field metadata.
func (s *Service) validate(ctx context.Context, orgID, ciTypeID, ciID string, existing, patch map[string]any) error {
	if s.fields == nil {
		return nil
	}
	defs, err := s.fields.ResolveFields(ctx, orgID, ciTypeID, ciID)
	if err != nil {
		return err
	}
	return validateAttributes(defs, MergePatch(existing, patch), patch, existing)
}

// List returns paginated CIs filtered by the given parameters.
func (s *Service) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
	return s.repo.List(ctx, orgID, filter, page)
}

// GetByID retrieves a single CI by ID.
func (s *Service) GetByID(ctx context.Context, orgID, id string) (*Item, error) {
	return s.repo.GetByID(ctx, orgID, id)
}

// Create inserts a new CI after verifying the tenant's licensed CI limit.
func (s *Service) Create(ctx context.Context, item *Item) error {
	if s.limit != nil {
		_, total, err := s.repo.List(ctx, item.OrganizationID, FilterParams{}, api.PaginationParams{Limit: 1})
		if err != nil {
			return err
		}
		if err := s.limit.AllowCreate(ctx, item.OrganizationID, LimitFeatureKey, int64(total)); err != nil {
			return err
		}
	}
	if err := s.validate(ctx, item.OrganizationID, item.CITypeID, "", nil, item.Attributes); err != nil {
		return err
	}
	return s.repo.Create(ctx, item)
}

// Update modifies an existing CI. The repository loads the prior state inside
// the mutation transaction so the recorded diff cannot race with concurrent
// writers.
func (s *Service) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) {
	if s.fields != nil {
		before, err := s.repo.GetByID(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		if err := s.validate(ctx, orgID, before.CITypeID, id, before.Attributes, req.Attributes); err != nil {
			return nil, err
		}
	}
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
