package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Lists that were read in full before F-215 (API keys, invitations, service
// accounts, attribute definitions, the operator's organization list) page
// newest first on (created_at, id) (API-02). Their keyset cursor pins this
// order.
const (
	newestFirstSort = "created_at"
	newestFirstDir  = "desc"
)

// newestFirstPosition returns the (created_at, id) position of a keyset
// cursor of a newest-first list. A cursor issued for another order, or one
// whose position is no timestamp and UUID, is ErrInvalidCursor.
// validation: fixed texts wrapping ErrInvalidCursor (API-03).
func newestFirstPosition(c Cursor) (time.Time, string, error) {
	if !c.Matches(newestFirstSort, newestFirstDir) {
		return time.Time{}, "", fmt.Errorf("%w: sort order changed", ErrInvalidCursor)
	}
	at, err := time.Parse(time.RFC3339Nano, c.Value)
	if err != nil || !IsUUID(c.ID) {
		return time.Time{}, "", fmt.Errorf("%w: malformed position", ErrInvalidCursor)
	}
	return at, c.ID, nil
}

// ParseNewestFirstPage is ParsePagination for a list paged newest first on
// (created_at, id): limit (default 50, at most 200), offset, offset cursors
// and the keyset cursors NewestFirstPage issues. A malformed or forged
// cursor, or a keyset cursor of another order, is answered with 400 and ok
// false.
func ParseNewestFirstPage(w http.ResponseWriter, r *http.Request) (page PaginationParams, ok bool) {
	page = ParsePagination(r)
	err := page.CursorError
	if err == nil && page.Cursor != nil {
		_, _, err = newestFirstPosition(*page.Cursor)
	}
	if err != nil {
		WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return page, false
	}
	return page, true
}

// NewestFirstSQL renders the paging part of a query that reads a list
// newest first on (created_at, id) of one table: where is the continuation
// predicate after the row of a keyset cursor ("TRUE" without one), tail is
// ORDER BY created_at DESC, id DESC LIMIT … with the OFFSET of an offset
// page. The query reads one row more than the page, so NewestFirstPage
// knows whether more rows follow. args belong to the placeholders from
// argPos on.
func NewestFirstSQL(page PaginationParams, argPos int) (where, tail string, args []any, err error) {
	where = "TRUE"
	if page.Cursor != nil {
		at, id, posErr := newestFirstPosition(*page.Cursor)
		if posErr != nil {
			return "", "", nil, posErr
		}
		where = KeysetClause(newestFirstSort, "timestamptz", newestFirstDir, argPos)
		args = append(args, at.UTC().Format(time.RFC3339Nano), id)
		argPos += 2
	}
	tail = fmt.Sprintf("ORDER BY created_at DESC, id DESC LIMIT $%d", argPos)
	args = append(args, page.Limit+1)
	if page.Cursor == nil && page.Offset > 0 {
		tail += fmt.Sprintf(" OFFSET $%d", argPos+1)
		args = append(args, page.Offset)
	}
	return where, tail, args, nil
}

// NewestFirstSlice pages an in-memory list like NewestFirstSQL: the items
// after the keyset cursor or the offset, newest first on (created_at, id),
// at most one more than the limit, and the number of items. key returns
// created_at and id of an item.
func NewestFirstSlice[T any](items []T, page PaginationParams, key func(*T) (time.Time, string)) (rows []T, total int, err error) {
	sorted := slices.Clone(items)
	slices.SortStableFunc(sorted, func(a, b T) int {
		at, aid := key(&a)
		bt, bid := key(&b)
		if c := bt.Compare(at); c != 0 {
			return c
		}
		return strings.Compare(bid, aid)
	})
	start := min(page.Offset, len(sorted))
	if page.Cursor != nil {
		after, afterID, posErr := newestFirstPosition(*page.Cursor)
		if posErr != nil {
			return nil, 0, posErr
		}
		start = len(sorted)
		for i := range sorted {
			at, id := key(&sorted[i])
			if at.Before(after) || (at.Equal(after) && id < afterID) {
				start = i
				break
			}
		}
	}
	end := min(start+max(page.Limit, 0)+1, len(sorted))
	return sorted[start:end], len(sorted), nil
}

// NewestFirstPage is the ListResponse of the rows NewestFirstSQL or
// NewestFirstSlice read (at most one more than the limit) and the total of
// the list: when more rows follow, the page ends at the limit, has_more is
// set and next_cursor is the keyset cursor after its last row. key returns
// created_at and id of a row.
func NewestFirstPage[T any](rows []T, total int, page PaginationParams, key func(*T) (time.Time, string)) ListResponse[T] {
	list := ListResponse[T]{Data: rows, Total: total, Limit: page.Limit, Offset: page.Offset}
	if page.Cursor != nil {
		list.Offset = 0
	}
	if page.Limit > 0 && len(rows) > page.Limit {
		list.Data = rows[:page.Limit]
		at, id := key(&list.Data[page.Limit-1])
		list.HasMore = true
		list.NextCursor = EncodeCursor(Cursor{Sort: newestFirstSort, Dir: newestFirstDir, Value: at.UTC().Format(time.RFC3339Nano), ID: id})
	}
	return list
}
