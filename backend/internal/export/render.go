package export

import (
	"context"
	"fmt"
	"io"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// renderBatchSize bounds how many rows an export job holds in memory at once.
const renderBatchSize = 500

// RenderFormat writes the tenant's CIs in the given format to out and returns
// the number of rows written. It streams keyset-paginated batches so a large
// tenant export never buffers the whole result set in memory. The exported
// row shape is identical to the streaming endpoint.
func RenderFormat(ctx context.Context, ciRepo ci.Repository, orgID string, format string, filters JobFilters, out io.Writer) (int, error) {
	filter := ci.FilterParams{
		Status:   filters.Status,
		TypeID:   filters.CITypeID,
		ClientID: filters.ClientID,
		SortBy:   "created_at",
		SortDir:  "asc",
	}

	writer := newFormatWriter(format, out)
	writer.begin()

	written := 0
	var cursor *api.Cursor
	for {
		items, _, err := ciRepo.List(ctx, orgID, filter, api.PaginationParams{Limit: renderBatchSize, Cursor: cursor})
		if err != nil {
			return written, fmt.Errorf("export query: %w", err)
		}
		for _, item := range items {
			writer.row(item)
			written++
		}
		if err := writer.flush(); err != nil {
			return written, err
		}
		if len(items) < renderBatchSize {
			break
		}
		next := ci.NextCursor(items, filter, renderBatchSize)
		if next == "" {
			break
		}
		decoded, err := api.DecodeCursor(next)
		if err != nil {
			return written, fmt.Errorf("decode export cursor: %w", err)
		}
		cursor = &decoded
	}
	writer.end()
	if err := writer.flush(); err != nil {
		return written, err
	}
	return written, nil
}
