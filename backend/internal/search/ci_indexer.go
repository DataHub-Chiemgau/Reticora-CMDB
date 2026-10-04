package search

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// CIIndexer adapts a search Backend to the ci.Indexer interface so the CI
// service can keep the tenant search index in sync on every mutation without
// importing the search package (which would create an import cycle through
// the shared api types).
type CIIndexer struct {
	Backend Backend
}

// NewCIIndexer wraps a search backend for use with ci.NewIndexingRepository.
// A nil backend returns nil so the repository is used undecorated.
func NewCIIndexer(backend Backend) *CIIndexer {
	if backend == nil {
		return nil
	}
	return &CIIndexer{Backend: backend}
}

// IndexDocument upserts a CI document into the search index. Client and site
// of the CI travel along so remote indexes can filter on them (TEC-12).
func (a *CIIndexer) IndexDocument(ctx context.Context, doc ci.Document) error {
	return a.Backend.IndexDocument(ctx, Document{
		OrganizationID: doc.OrganizationID,
		EntityType:     doc.EntityType,
		EntityID:       doc.EntityID,
		Title:          doc.Title,
		Summary:        doc.Summary,
		URL:            doc.URL,
		Metadata:       doc.Metadata,
		ClientID:       doc.ClientID,
		SiteID:         doc.SiteID,
	})
}

// DeleteDocument removes a CI document from the search index.
func (a *CIIndexer) DeleteDocument(ctx context.Context, orgID, entityType, entityID string) error {
	return a.Backend.Delete(ctx, orgID, entityType, entityID)
}
