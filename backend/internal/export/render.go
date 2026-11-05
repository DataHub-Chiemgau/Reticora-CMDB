package export

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

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
func RenderFormat(ctx context.Context, ciRepo ci.Repository, orgID, format string, filters JobFilters, out io.Writer) (int, error) { //nolint:gocritic // hugeParam: the filters of the job row, a copy since filters.ids (WP-780)
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
func RenderProjected(ctx context.Context, ciRepo ci.Repository, orgID, format string, filters JobFilters, //nolint:gocritic // hugeParam: the filters of the job row, a copy since filters.ids (WP-780)
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
	if len(filters.IDs) > 0 {
		return renderSelection(ctx, ciRepo, orgID, filters.IDs, writer, proj)
	}

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

// errNoBatchReader fails an export of a selection of ids whose CI
// repository cannot read by ids (fail-closed, WP-780).
var errNoBatchReader = errors.New("export of a selection of ids: the CI repository cannot read CIs by id")

// renderSelection writes the CIs of a selection of ids (filters.ids,
// WP-780): one read through ci.BatchReader under the scope of ctx, so
// deleted and invisible ids are missing, sorted by created_at and id like
// the filtered export.
func renderSelection(ctx context.Context, ciRepo ci.Repository, orgID string, ids []string, writer *rowWriter,
	proj *projection) (int, Manifest, error) {
	reader, ok := ciRepo.(ci.BatchReader)
	if !ok {
		return 0, proj.manifest, errNoBatchReader
	}
	items, err := reader.GetByIDs(ctx, orgID, ids)
	if err != nil {
		return 0, proj.manifest, fmt.Errorf("export query: %w", err)
	}
	slices.SortFunc(items, func(a, b ci.Item) int {
		return cmp.Or(createdAt(a.CreatedAt).Compare(createdAt(b.CreatedAt)), cmp.Compare(a.ID, b.ID))
	})
	written := 0
	for i := range items {
		if rowErr := writer.row(&items[i]); rowErr != nil {
			return written, proj.manifest, fmt.Errorf("export row: %w", rowErr)
		}
		written++
	}
	writer.end()
	if err := writer.flush(); err != nil {
		return written, proj.manifest, err
	}
	return written, proj.manifest, nil
}

// createdAt parses the creation time of a CI (RFC 3339); an unreadable one
// sorts first.
func createdAt(value string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return at
}
