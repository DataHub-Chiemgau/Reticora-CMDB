package ci

import (
	"context"
	"errors"
	"log/slog"
	"time"

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

// RestoreDeleted forwards the restore (CI-05) and indexes the CI again.
func (r *indexingRepository) RestoreDeleted(ctx context.Context, orgID, id string, retention time.Duration) (*Item, error) {
	restorer, ok := r.Repository.(Restorer)
	if !ok {
		return nil, errors.New("restore is not supported by this repository")
	}
	item, err := restorer.RestoreDeleted(ctx, orgID, id, retention)
	if err != nil {
		return nil, err
	}
	r.index(ctx, item)
	return item, nil
}

// GetByIDs forwards the batch read (NFR-02); without it every CI is read
// on its own.
func (r *indexingRepository) GetByIDs(ctx context.Context, orgID string, ids []string) ([]Item, error) {
	if reader, ok := r.Repository.(BatchReader); ok {
		return reader.GetByIDs(ctx, orgID, ids)
	}
	items := []Item{}
	for _, id := range ids {
		if item, err := r.GetByID(ctx, orgID, id); err == nil {
			items = append(items, *item)
		}
	}
	return items, nil
}

// ChangeType forwards the type change (CI-11) and indexes the CI again.
func (r *indexingRepository) ChangeType(ctx context.Context, orgID, ciID, typeID, mode string) (*TypeChangeResult, error) {
	changer, ok := r.Repository.(TypeChanger)
	if !ok {
		return nil, errors.New("type change is not supported by this repository")
	}
	result, err := changer.ChangeType(ctx, orgID, ciID, typeID, mode)
	if err != nil {
		return nil, err
	}
	if result.Changed {
		r.index(ctx, result.CI)
	}
	return result, nil
}

// Merge forwards the merge (CI-12): the survivor is indexed again, the
// victim leaves the index.
func (r *indexingRepository) Merge(ctx context.Context, orgID, survivorID, victimID, mergeID string) (*MergeResult, error) {
	merger, ok := r.Repository.(Merger)
	if !ok {
		return nil, ErrMergeUnsupported
	}
	result, err := merger.Merge(ctx, orgID, survivorID, victimID, mergeID)
	if err != nil {
		return nil, err
	}
	r.index(ctx, result.Survivor)
	if derr := r.indexer.DeleteDocument(ctx, orgID, EntityTypeCI, victimID); derr != nil {
		slog.WarnContext(ctx, "search index delete failed; rebuild via POST /api/v1/search/reindex", "ci_id", victimID, "error", derr)
	}
	return result, nil
}

// Unmerge forwards the unmerge (CI-12) and indexes both CIs again.
func (r *indexingRepository) Unmerge(ctx context.Context, orgID, victimID string) (*UnmergeResult, error) {
	unmerger, ok := r.Repository.(Unmerger)
	if !ok {
		return nil, ErrMergeUnsupported
	}
	result, err := unmerger.Unmerge(ctx, orgID, victimID)
	if err != nil {
		return nil, err
	}
	r.index(ctx, result.Survivor)
	r.index(ctx, result.Victim)
	return result, nil
}

// MergeVictim forwards the merge lookup of the job revert (CI-12).
func (r *indexingRepository) MergeVictim(ctx context.Context, orgID, mergeID string) (string, error) {
	reader, ok := r.Repository.(mergeVictimReader)
	if !ok {
		return "", ErrMergeUnsupported
	}
	return reader.MergeVictim(ctx, orgID, mergeID)
}

// MergedInto forwards the tombstone lookup (CI-12).
func (r *indexingRepository) MergedInto(ctx context.Context, orgID, victimID string) (survivorID string, found bool, err error) {
	reader, ok := r.Repository.(TombstoneReader)
	if !ok {
		return "", false, nil
	}
	return reader.MergedInto(ctx, orgID, victimID)
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
