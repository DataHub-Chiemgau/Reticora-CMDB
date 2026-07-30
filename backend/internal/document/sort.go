package document

import (
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// sortColumnCasts maps the sortable columns to their PostgreSQL type. Only
// columns in this map may be interpolated into SQL.
var sortColumnCasts = map[string]string{
	"title":      "text",
	"category":   "text",
	"created_at": "timestamptz",
	"updated_at": "timestamptz",
}

// NormalizeSort maps the requested sort onto the supported column/direction
// pair. Unknown values fall back to the stable default ordering.
func NormalizeSort(filter FilterParams) (column, direction string) {
	column = "created_at"
	if _, ok := sortColumnCasts[filter.SortBy]; ok {
		column = filter.SortBy
	}
	direction = "desc"
	if strings.EqualFold(filter.SortDir, "asc") {
		direction = "asc"
	}
	return column, direction
}

// sortValue returns the value of the sort column for one item, formatted the
// same way for the in-memory and the PostgreSQL repository so cursors are
// interchangeable.
func sortValue(item Document, column string) string {
	switch column {
	case "title":
		return item.Title
	case "category":
		return item.Category
	case "updated_at":
		return item.UpdatedAt.UTC().Format(time.RFC3339Nano)
	default:
		return item.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
}

// NextCursor returns the opaque cursor pointing after the last returned item,
// or an empty string when the page was not full (i.e. no more rows follow).
func NextCursor(items []Document, filter FilterParams, limit int) string {
	if len(items) == 0 || len(items) < limit {
		return ""
	}
	column, direction := NormalizeSort(filter)
	last := items[len(items)-1]
	return api.EncodeCursor(api.Cursor{
		Sort:  column,
		Dir:   direction,
		Value: sortValue(last, column),
		ID:    last.ID,
	})
}
