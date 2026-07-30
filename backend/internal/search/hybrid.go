package search

// HybridBackend serves queries from a remote backend while using PostgreSQL as
// the authoritative reindex source.
type HybridBackend struct {
	Remote Backend
	Source *PGRepository
}

func (b *HybridBackend) Ping() error                      { return b.Remote.Ping() }
func (b *HybridBackend) IndexDocument(doc Document) error { return b.Remote.IndexDocument(doc) }
func (b *HybridBackend) Delete(orgID, entityType, entityID string) error {
	return b.Remote.Delete(orgID, entityType, entityID)
}
func (b *HybridBackend) Query(q Query) (Result, error) { return b.Remote.Query(q) }
func (b *HybridBackend) ReindexTenant(orgID string) (ReindexResult, error) {
	if _, err := b.Source.ReindexTenant(orgID); err != nil {
		return ReindexResult{}, err
	}
	indexed := 0
	for offset := 0; ; offset += 100 {
		res, err := b.Source.Query(Query{OrganizationID: orgID, Limit: 100, Offset: offset})
		if err != nil {
			return ReindexResult{Indexed: indexed}, err
		}
		for _, hit := range res.Data {
			if err := b.Remote.IndexDocument(hit.Document); err != nil {
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
