package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// HybridBackend serves queries from a remote backend while using PostgreSQL as
// the authoritative source: every document is written to search_document as
// well, whose client and site the triggers of migration 000067 keep current
// when an entity moves, and every remote hit is checked against it under the
// request's tenant scope (TEC-12, WP-346).
type HybridBackend struct {
	Remote Backend
	Source *PGRepository
}

func (b *HybridBackend) Ping(ctx context.Context) error { return b.Remote.Ping(ctx) }

// FullDocumentIndexer is a remote index that takes the full documents of
// the PostgreSQL index, with field rows and identifiers (SRC-02, WP-787);
// OpenSearchBackend implements it. A remote index without it gets the
// documents alone.
type FullDocumentIndexer interface {
	IndexFullDocument(ctx context.Context, doc *FullDocument) error
}

// IndexDocument writes the document to the PostgreSQL index and sends its
// full document to the remote index, like IndexDocuments.
func (b *HybridBackend) IndexDocument(ctx context.Context, in *Document) error {
	return b.IndexDocuments(ctx, []Document{*in})
}

// remoteCopyKey is the AfterCommitBatch key of the documents of a
// transaction that go to the remote index of the backend.
type remoteCopyKey struct{ backend *HybridBackend }

// remoteCopyItem is a document of a transaction to copy to the remote index.
type remoteCopyItem struct {
	orgID string
	ref   DocumentRef
}

// IndexDocuments writes the documents of one organization to the PostgreSQL
// index and then sends their full documents to the remote index: read back
// from PostgreSQL (FullDocuments) with the field rows of both sources and
// the identifiers, so the rows the triggers keep reach the remote index as
// well (SRC-02, WP-787). In a context transaction the PostgreSQL write runs
// under a savepoint, so its failure does not abort the transaction, and the
// read and the HTTP calls wait for the commit, so the transaction never
// holds its locks while the remote index answers (NFR-03, K20); all
// documents a transaction writes are read with one statement then
// (AfterCommitBatch). A remote failure is then logged, the index can be
// rebuilt with POST /api/v1/search/reindex.
func (b *HybridBackend) IndexDocuments(ctx context.Context, in []Document) error {
	if len(in) == 0 {
		return nil
	}
	docs := append([]Document(nil), in...)
	pgErr := database.Savepoint(ctx, func(ctx context.Context) error { return b.Source.IndexDocuments(ctx, docs) })
	orgID := docs[0].OrganizationID
	refs := make([]DocumentRef, len(docs))
	for i := range docs {
		refs[i] = DocumentRef{EntityType: docs[i].EntityType, EntityID: docs[i].EntityID}
	}
	if !database.InTx(ctx) {
		return errors.Join(pgErr, b.copyToRemote(ctx, orgID, refs))
	}
	for i := range refs {
		database.AfterCommitBatch(ctx, remoteCopyKey{b}, remoteCopyItem{orgID: orgID, ref: refs[i]}, func(ctx context.Context, items []any) {
			byOrg := map[string][]DocumentRef{}
			var orgs []string
			for _, item := range items {
				it, ok := item.(remoteCopyItem)
				if !ok {
					continue
				}
				if _, seen := byOrg[it.orgID]; !seen {
					orgs = append(orgs, it.orgID)
				}
				byOrg[it.orgID] = append(byOrg[it.orgID], it.ref)
			}
			for _, org := range orgs {
				if err := b.copyToRemote(ctx, org, byOrg[org]); err != nil {
					slog.WarnContext(ctx, "remote search index update failed; rebuild via POST /api/v1/search/reindex",
						"documents", len(byOrg[org]), "error", err)
				}
			}
		})
	}
	return pgErr
}

// copyToRemote reads the full documents of the entities from PostgreSQL
// with one statement and sends them to the remote index.
func (b *HybridBackend) copyToRemote(ctx context.Context, orgID string, refs []DocumentRef) error {
	docs, err := b.Source.FullDocuments(ctx, orgID, dedupeRefs(refs))
	if err != nil {
		return err
	}
	return b.sendFull(ctx, docs)
}

// dedupeRefs drops repeated entities, keeping the first.
func dedupeRefs(refs []DocumentRef) []DocumentRef {
	seen := make(map[string]bool, len(refs))
	out := refs[:0:0]
	for _, ref := range refs {
		if k := hitKey(ref.EntityType, ref.EntityID); !seen[k] {
			seen[k] = true
			out = append(out, ref)
		}
	}
	return out
}

// sendFull sends the full documents to the remote index, the documents
// alone to a remote index that is no FullDocumentIndexer. Every document is
// sent; the errors are joined.
func (b *HybridBackend) sendFull(ctx context.Context, docs []FullDocument) error {
	full, _ := b.Remote.(FullDocumentIndexer)
	var errs []error
	for i := range docs {
		var err error
		if full != nil {
			err = full.IndexFullDocument(ctx, &docs[i])
		} else {
			err = b.Remote.IndexDocument(ctx, &docs[i].Document)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", docs[i].EntityType, docs[i].EntityID, err))
		}
	}
	return errors.Join(errs...)
}

// Delete removes the document from both indexes.
func (b *HybridBackend) Delete(ctx context.Context, orgID, entityType, entityID string) error {
	pgErr := database.Savepoint(ctx, func(ctx context.Context) error { return b.Source.Delete(ctx, orgID, entityType, entityID) })
	return errors.Join(pgErr, b.Remote.Delete(ctx, orgID, entityType, entityID))
}

// Query serves the remote hits that PostgreSQL confirms. The remote index
// filters on the client and site its documents carried when they were
// written; a move that no indexer saw (a location moved to another site, a
// write path without indexer) leaves them stale. Each hit is therefore
// looked up in search_document under the request's tenant scope, whose
// policy uses the current client and site, together with the entity's own
// policy (hitVisible); hits that PostgreSQL does not return are dropped, for
// every entity type.
//
// The remote index holds the field rows and identifiers (WP-787) but no
// field permissions (TEC-12 [P5]): with restricted field permissions
// PostgreSQL answers the query with its field rows (RBA-07, WP-266).
//
// Users (WP-788): PostgreSQL resolves the text to user ids
// (PGRepository.ResolveUsers), the remote index finds them in its
// identifiers.
func (b *HybridBackend) Query(ctx context.Context, in *Query) (Result, error) {
	if len(in.restrictedTypes()) > 0 {
		return b.Source.Query(ctx, in)
	}
	query := *in
	q := &query
	users, err := b.Source.ResolveUsers(ctx, q)
	if err != nil {
		return Result{}, err
	}
	q.userIDs = users
	res, err := b.Remote.Query(ctx, q)
	if err != nil || len(res.Data) == 0 {
		return res, err
	}
	visible, err := b.visibleHits(ctx, q.OrganizationID, res.Data)
	if err != nil {
		return Result{}, err
	}
	kept := res.Data[:0]
	for i := range res.Data {
		if visible[hitKey(res.Data[i].EntityType, res.Data[i].EntityID)] {
			kept = append(kept, res.Data[i])
		}
	}
	res.Total -= len(res.Data) - len(kept)
	res.Data = kept
	res.HasMore = res.Offset+len(res.Data) < res.Total
	return res, nil
}

// errHitCheck is returned when the remote hits could not be checked; no hit
// is served then.
var errHitCheck = errors.New("search hits could not be checked")

func hitKey(entityType, entityID string) string {
	return entityType + "/" + strings.ToLower(entityID)
}

// visibleHits returns the keys (hitKey) of the hits that search_document
// holds and that are visible under the tenant scope of ctx.
func (b *HybridBackend) visibleHits(ctx context.Context, orgID string, hits []Hit) (map[string]bool, error) {
	types, ids := make([]string, len(hits)), make([]string, len(hits))
	for i := range hits {
		types[i], ids[i] = hits[i].EntityType, strings.ToLower(hits[i].EntityID)
	}
	visible := map[string]bool{}
	err := database.WithRequestTenant(ctx, b.Source.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT search_document.entity_type, search_document.entity_id::text
			FROM search_document JOIN unnest($2::text[], $3::text[]) AS h(entity_type, entity_id)
			  ON search_document.entity_type = h.entity_type AND search_document.entity_id::text = h.entity_id
			WHERE search_document.organization_id = $1 AND `+hitVisible, orgID, types, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entityType, entityID string
			if err := rows.Scan(&entityType, &entityID); err != nil {
				return err
			}
			visible[hitKey(entityType, entityID)] = true
		}
		return rows.Err()
	})
	if err != nil {
		// The search handler answers with the error text: the database
		// error stays in the log.
		slog.ErrorContext(ctx, "checking remote search hits against PostgreSQL failed", "error", err)
		return nil, errHitCheck
	}
	return visible, nil
}

// ReindexPlacement implements rack.Indexer: it copies the full PostgreSQL
// documents of the placed CI and, for a CI bound to an asset, of the asset
// and all its CIs to the remote index, with their field rows and
// identifiers (WP-787). Their client and site are current there (migration
// 000067). Like ReindexTenant it reads organization-wide: the CIs of the
// asset may lie outside the caller's scope, and nothing read here is
// returned to the caller.
func (b *HybridBackend) ReindexPlacement(ctx context.Context, orgID, ciID string) error {
	docs, err := b.Source.placementDocuments(ctx, orgID, ciID)
	if err != nil {
		return fmt.Errorf("read placed search documents: %w", err)
	}
	return b.sendFull(ctx, docs)
}

// remoteCopyPage is the number of full documents one read of the copy of
// ReindexTenant takes.
const remoteCopyPage = 500

// ReindexTenant rebuilds the PostgreSQL index and copies it to the remote
// index: the full documents with their field rows and identifiers
// (WP-787), read page by page by id (FullDocumentPage). Like the
// PostgreSQL rebuild, the copy covers the whole organization even when a
// restricted administrator triggers it; each document carries its client
// and site, on which every remote query filters. The first remote error
// ends the copy; the documents sent before stay.
func (b *HybridBackend) ReindexTenant(ctx context.Context, orgID string) (ReindexResult, error) {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return ReindexResult{}, database.ErrNoTenantScope
	}
	if !strings.EqualFold(scope.OrgID, orgID) {
		return ReindexResult{}, database.ErrTenantMismatch
	}
	if _, err := b.Source.ReindexTenant(ctx, orgID); err != nil {
		return ReindexResult{}, err
	}
	indexed, after := 0, ""
	for {
		docs, err := b.Source.FullDocumentPage(ctx, orgID, after, remoteCopyPage)
		if err != nil {
			return ReindexResult{Indexed: indexed}, err
		}
		for i := range docs {
			if err := b.sendFull(ctx, docs[i:i+1]); err != nil {
				return ReindexResult{Indexed: indexed}, err
			}
			indexed++
		}
		if len(docs) < remoteCopyPage {
			return ReindexResult{Indexed: indexed}, nil
		}
		after = docs[len(docs)-1].ID
	}
}
