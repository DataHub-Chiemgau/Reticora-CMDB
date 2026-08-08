package ci

import (
	"context"
	"log/slog"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// indexingRepository decorates a Repository with best-effort search-index
// maintenance: every successful create, update and delete is mirrored into
// the tenant search index. Index failures are logged and never fail the
// mutation itself — the index can always be rebuilt with
// POST /api/v1/search/reindex.
//
// The decorator lives at the repository boundary so that every write path
// (REST handler, collector bulk ingest, workflow executor) keeps the index
// in sync, not only the CI service.
type indexingRepository struct {
	Repository
	indexer Indexer
}

// NewIndexingRepository wraps repo so CI mutations are mirrored into the
// search index. A nil indexer returns the repository unchanged.
func NewIndexingRepository(repo Repository, indexer Indexer) Repository {
	if indexer == nil {
		return repo
	}
	return &indexingRepository{Repository: repo, indexer: indexer}
}

func (r *indexingRepository) Create(ctx context.Context, item *Item) error {
	if err := r.Repository.Create(ctx, item); err != nil {
		return err
	}
	r.index(ctx, item)
	return nil
}

func (r *indexingRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) {
	item, err := r.Repository.Update(ctx, orgID, id, req)
	if err != nil {
		return nil, err
	}
	r.index(ctx, item)
	return item, nil
}

func (r *indexingRepository) Delete(ctx context.Context, orgID, id string) error {
	if err := r.Repository.Delete(ctx, orgID, id); err != nil {
		return err
	}
	if err := r.indexer.DeleteDocument(ctx, orgID, EntityTypeCI, id); err != nil {
		slog.WarnContext(ctx, "search index delete failed; rebuild via POST /api/v1/search/reindex", "ci_id", id, "error", err)
	}
	return nil
}

// ListChanges forwards change-history reads when the wrapped repository
// supports them (used by ci.Service.ListChanges).
func (r *indexingRepository) ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error) {
	reader, ok := r.Repository.(ChangeReader)
	if !ok {
		return []Change{}, 0, nil
	}
	return reader.ListChanges(ctx, orgID, ciID, page)
}

func (r *indexingRepository) index(ctx context.Context, item *Item) {
	if item == nil {
		return
	}
	doc := IndexDocumentFor(item)
	if doc.OrganizationID == "" || doc.EntityID == "" {
		return
	}
	if err := r.indexer.IndexDocument(ctx, doc); err != nil {
		slog.WarnContext(ctx, "search index update failed; rebuild via POST /api/v1/search/reindex", "ci_id", item.ID, "error", err)
	}
}
