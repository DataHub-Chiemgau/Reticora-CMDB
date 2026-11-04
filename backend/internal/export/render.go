package export

import (
	"context"
	"fmt"
	"io"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// renderBatchSize bounds how many rows an export job holds in memory at once.
const renderBatchSize = 500

// RenderFormat writes the tenant's CIs in the given format to out and returns
// the number of rows written, projected by the field policy of the request
// in ctx (permission.PolicyFrom: without a principal unrestricted, with a
// principal its resolved policy or an error). The export job renders with
// RenderProjected and the policy of its creator.
func RenderFormat(ctx context.Context, ciRepo ci.Repository, orgID, format string, filters JobFilters, out io.Writer) (int, error) {
	policy, err := permission.PolicyFrom(ctx)
	if err != nil {
		return 0, fmt.Errorf("export field permissions: %w", err)
	}
	written, _, err := RenderProjected(ctx, ciRepo, orgID, format, filters, policy, out)
	return written, err
}

// RenderProjected writes the tenant's CIs in the given format to out,
// projected by policy (RBA-07, WP-729), and returns the number of rows and
// the manifest of the file. It streams keyset-paginated batches so a large
// tenant export never buffers the whole result set in memory. The exported
// row shape is identical to the streaming endpoint. A nil policy reads no
// field with field permissions (fail-closed).
func RenderProjected(ctx context.Context, ciRepo ci.Repository, orgID, format string, filters JobFilters,
	policy *permission.FieldPolicy, out io.Writer) (int, Manifest, error) {
	filter := &ci.FilterParams{
		Status:   filters.Status,
		TypeID:   filters.CITypeID,
		ClientID: filters.ClientID,
		SortBy:   "created_at",
		SortDir:  "asc",
	}

	proj := newProjection(format, policy, typeFilter(filters.CITypeID))
	writer := newFormatWriter(format, out, proj)
	writer.begin()

	written := 0
	var cursor *api.Cursor
	for {
		items, _, err := ciRepo.List(ctx, orgID, filter, api.PaginationParams{Limit: renderBatchSize, Cursor: cursor})
		if err != nil {
			return written, proj.manifest, fmt.Errorf("export query: %w", err)
		}
		for i := range items {
			if rowErr := writer.row(&items[i]); rowErr != nil {
				return written, proj.manifest, fmt.Errorf("export row: %w", rowErr)
			}
			written++
		}
		if flushErr := writer.flush(); flushErr != nil {
			return written, proj.manifest, flushErr
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
			return written, proj.manifest, fmt.Errorf("decode export cursor: %w", err)
		}
		cursor = &decoded
	}
	writer.end()
	if err := writer.flush(); err != nil {
		return written, proj.manifest, err
	}
	return written, proj.manifest, nil
}
