package ci

import (
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// sortColumnCasts maps the sortable columns to the PostgreSQL type of their
// sort expression (sortExpr). Only columns in this map may be interpolated
// into SQL.
var sortColumnCasts = map[string]string{
	"created_at":    "timestamptz",
	"updated_at":    "timestamptz",
	"name":          "text",
	"status":        "text",
	"owner_user_id": "text",
	"owner_team_id": "text",
}

// nullableSort are the sortable columns that may be NULL: the owner
// (WP-752), sorted by its UUID, not by a display name (no PII join).
var nullableSort = map[string]bool{"owner_user_id": true, "owner_team_id": true}

// sortExpr returns the SQL expression a list orders and pages by. A
// nullable column sorts as text with the empty string for NULL and in byte
// order (COLLATE "C"), exactly like sortValue: the keyset row comparison
// (api.KeysetClause) is never NULL, so paging with a cursor neither skips
// nor repeats the CIs without the value, and the cursors of both
// repositories stay interchangeable. The text of a uuid is lower case, so
// its byte order is the order of the uuid.
func sortExpr(column string) string {
	if nullableSort[column] {
		return `COALESCE(ci.` + column + `::text, '') COLLATE "C"`
	}
	return "ci." + column
}

// NormalizeSort maps the requested sort onto the supported column/direction
// pair. Unknown values fall back to the stable default ordering.
func NormalizeSort(filter *FilterParams) (column, direction string) {
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
func sortValue(item *Item, column string) string {
	switch column {
	case "name":
		return item.Name
	case "status":
		return item.Status
	case "updated_at":
		return item.UpdatedAt
	case "owner_user_id":
		return strings.ToLower(item.OwnerUserID)
	case "owner_team_id":
		return strings.ToLower(item.OwnerTeamID)
	default:
		return item.CreatedAt
	}
}

// NextCursor returns the opaque cursor pointing after the last returned item,
// or an empty string when the page was not full (i.e. no more rows follow).
func NextCursor(items []Item, filter *FilterParams, limit int) string {
	if len(items) == 0 || len(items) < limit {
		return ""
	}
	column, direction := NormalizeSort(filter)
	last := &items[len(items)-1]
	return api.EncodeCursor(api.Cursor{
		Sort:  column,
		Dir:   direction,
		Value: sortValue(last, column),
		ID:    last.ID,
	})
}
