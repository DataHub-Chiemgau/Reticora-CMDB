package audit

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// ListEntries runs the repository path of GET /api/v1/audit for the
// integration tests of package audit_test (WP-536), which check the error
// the tenant transaction returns; the handler maps it to a generic 500.
func (h *Handler) ListEntries(ctx context.Context, orgID string) ([]Entry, error) {
	entries, _, err := h.list(ctx, orgID, &ListFilter{}, api.PaginationParams{Limit: 100})
	return entries, err
}
