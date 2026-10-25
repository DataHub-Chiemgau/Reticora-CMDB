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
// in sync, not only the CI service. It offers every exported method of the
// PostgreSQL repository (except WithEvents): callers find the optional
// interfaces by type assertion, and a method the decorator hides is lost
// in production, where the repository is always decorated (SR-11, F-037).
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

// CreateMany implements BatchCreator: the wrapped repository creates the
// CIs in one batch where it can, else one by one (all or nothing only in a
// transaction of the context); the documents of the created CIs are indexed
// with one statement where the indexer can (NFR-03, WP-333). The decorator
// always offers the batch, so the outermost of several decorators keeps it.
func (r *indexingRepository) CreateMany(ctx context.Context, orgID string, items []*Item) (CreateManyResult, error) {
	var res CreateManyResult
	if creator, ok := r.Repository.(BatchCreator); ok {
		var err error
		if res, err = creator.CreateMany(ctx, orgID, items); err != nil {
			return res, err
		}
	} else {
		for _, item := range items {
			if err := r.Repository.Create(ctx, item); err != nil {
				return res, err
			}
		}
	}
	created := make([]*Item, 0, len(items))
	for _, item := range items {
		if item.ID != "" {
			created = append(created, item)
		}
	}
	r.indexMany(ctx, created)
	return res, nil
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

// reactivator reactivates a CI seen again after its deletion (CI-13); the
// discovery review handler asserts it.
type reactivator interface {
	Restore(ctx context.Context, orgID, id string) (*Item, error)
}

// Restore forwards the reactivation of a deleted CI (CI-13, merge of a
// resurrected_device review) and indexes the CI again.
func (r *indexingRepository) Restore(ctx context.Context, orgID, id string) (*Item, error) {
	restorer, ok := r.Repository.(reactivator)
	if !ok {
		return nil, errors.New("reactivation is not supported by this repository")
	}
	item, err := restorer.Restore(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	r.index(ctx, item)
	return item, nil
}

// UniqueConflicts forwards the check of the unique attributes (MET-15), so
// the service refuses a taken value before the write; without it no
// attribute is reported and only the unique index guards.
func (r *indexingRepository) UniqueConflicts(ctx context.Context, orgID, ciTypeID, clientID, ciID string, attrs map[string]any) ([]string, error) {
	checker, ok := r.Repository.(UniqueChecker)
	if !ok {
		return nil, nil
	}
	return checker.UniqueConflicts(ctx, orgID, ciTypeID, clientID, ciID, attrs)
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

// ListNodes forwards the graph projection (NFR-02); without it the nodes
// come from the batch read.
func (r *indexingRepository) ListNodes(ctx context.Context, orgID string, ids []string) ([]Node, error) {
	if reader, ok := r.Repository.(NodeReader); ok {
		return reader.ListNodes(ctx, orgID, ids)
	}
	items, err := r.GetByIDs(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	nodes := make([]Node, 0, len(items))
	for i := range items {
		nodes = append(nodes, NodeOf(&items[i]))
	}
	return nodes, nil
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

// indexMany indexes the documents of several CIs, with one call where the
// indexer takes a batch; like index it logs a failure.
func (r *indexingRepository) indexMany(ctx context.Context, items []*Item) {
	batch, ok := r.indexer.(BatchIndexer)
	if !ok {
		for _, item := range items {
			r.index(ctx, item)
		}
		return
	}
	docs := make([]Document, 0, len(items))
	for _, item := range items {
		if doc := IndexDocumentFor(item); doc.OrganizationID != "" && doc.EntityID != "" {
			docs = append(docs, doc)
		}
	}
	if len(docs) == 0 {
		return
	}
	if err := batch.IndexDocuments(ctx, docs); err != nil {
		slog.WarnContext(ctx, "search index update failed; rebuild via POST /api/v1/search/reindex", "cis", len(docs), "error", err)
	}
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
