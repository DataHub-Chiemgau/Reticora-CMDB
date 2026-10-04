package search

import (
	"context"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// HybridBackend serves queries from a remote backend while using PostgreSQL as
// the authoritative reindex source.
type HybridBackend struct {
	Remote Backend
	Source *PGRepository
}

func (b *HybridBackend) Ping(ctx context.Context) error { return b.Remote.Ping(ctx) }
func (b *HybridBackend) IndexDocument(ctx context.Context, doc Document) error {
	return b.Remote.IndexDocument(ctx, doc)
}
func (b *HybridBackend) Delete(ctx context.Context, orgID, entityType, entityID string) error {
	return b.Remote.Delete(ctx, orgID, entityType, entityID)
}
func (b *HybridBackend) Query(ctx context.Context, q Query) (Result, error) {
	return b.Remote.Query(ctx, q)
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
