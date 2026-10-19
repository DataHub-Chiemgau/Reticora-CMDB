package search

import (
	"context"
	"encoding/json"
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

// IndexDocument writes the document to the PostgreSQL index and sends it to
// the remote index. In a context transaction the PostgreSQL write runs under
// a savepoint, so its failure does not abort the transaction, and the HTTP
// call waits for the commit, so the transaction never holds its locks while
// the remote index answers (NFR-03); a remote failure is then logged, the
// index can be rebuilt with POST /api/v1/search/reindex.
func (b *HybridBackend) IndexDocument(ctx context.Context, doc Document) error {
	pgErr := database.Savepoint(ctx, func(ctx context.Context) error { return b.Source.IndexDocument(ctx, doc) })
	if !database.InTx(ctx) {
		return errors.Join(pgErr, b.Remote.IndexDocument(ctx, doc))
	}
	database.AfterCommit(ctx, func(ctx context.Context) {
		if err := b.Remote.IndexDocument(ctx, doc); err != nil {
			slog.WarnContext(ctx, "remote search index update failed; rebuild via POST /api/v1/search/reindex",
				"entity_id", doc.EntityID, "error", err)
		}
	})
	return pgErr
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
func (b *HybridBackend) Query(ctx context.Context, q Query) (Result, error) {
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

// ReindexPlacement implements rack.Indexer: it copies the PostgreSQL
// documents of the placed CI and, for a CI bound to an asset, of the asset
// and all its CIs to the remote index. Their client and site are current
// there (migration 000067). Like ReindexTenant it reads organization-wide:
// the CIs of the asset may lie outside the caller's scope, and nothing read
// here is returned to the caller.
func (b *HybridBackend) ReindexPlacement(ctx context.Context, orgID, ciID string) error {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return database.ErrNoTenantScope
	}
	if !strings.EqualFold(scope.OrgID, orgID) {
		return database.ErrTenantMismatch
	}
	orgWide := database.OrgWideScope(scope.OrgID, scope.UserID)
	var docs []Document
	err := database.WithTenant(ctx, b.Source.pool, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH placed AS (SELECT id, asset_id FROM ci WHERE organization_id = $1 AND id::text = $2)
			SELECT id::text, organization_id::text, entity_type, entity_id::text, title, summary, url, metadata,
			       COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), updated_at
			FROM search_document
			WHERE organization_id = $1 AND (
			      (entity_type = 'ci' AND entity_id IN (SELECT id FROM placed
			                                            UNION SELECT c.id FROM ci c JOIN placed p ON c.asset_id = p.asset_id
			                                            WHERE c.organization_id = $1))
			   OR (entity_type = 'asset' AND entity_id IN (SELECT asset_id FROM placed)))`, orgID, ciID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Document
			var meta []byte
			if err := rows.Scan(&d.ID, &d.OrganizationID, &d.EntityType, &d.EntityID, &d.Title, &d.Summary, &d.URL, &meta,
				&d.ClientID, &d.SiteID, &d.UpdatedAt); err != nil {
				return err
			}
			if err := json.Unmarshal(meta, &d.Metadata); err != nil {
				return fmt.Errorf("unmarshal document metadata for %s: %w", d.ID, err)
			}
			docs = append(docs, d)
		}
		return rows.Err()
	})
	if err != nil {
		return fmt.Errorf("read placed search documents: %w", err)
	}
	var errs []error
	for i := range docs {
		if err := b.Remote.IndexDocument(ctx, docs[i]); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", docs[i].EntityType, docs[i].EntityID, err))
		}
	}
	return errors.Join(errs...)
}

// ReindexTenant rebuilds the PostgreSQL index and copies it to the remote
// index. Like the PostgreSQL rebuild, the copy covers the whole organization
// even when a restricted administrator triggers it; each document carries its
// client and site, on which every remote query filters.
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
	orgWide := database.OrgWideScope(scope.OrgID, scope.UserID)
	ctx = database.ContextWithTenantScope(ctx, &orgWide)
	indexed := 0
	for offset := 0; ; offset += 100 {
		res, err := b.Source.Query(ctx, Query{OrganizationID: orgID, Limit: 100, Offset: offset})
		if err != nil {
			return ReindexResult{Indexed: indexed}, err
		}
		for _, hit := range res.Data {
			if err := b.Remote.IndexDocument(ctx, hit.Document); err != nil {
				return ReindexResult{Indexed: indexed}, err
			}
			indexed++
		}
		if !res.HasMore {
			break
		}
	}
	return ReindexResult{Indexed: indexed}, nil
}
