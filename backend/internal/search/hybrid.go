package search

import "context"

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
func (b *HybridBackend) ReindexTenant(ctx context.Context, orgID string) (ReindexResult, error) {
	if _, err := b.Source.ReindexTenant(ctx, orgID); err != nil {
		return ReindexResult{}, err
	}
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
