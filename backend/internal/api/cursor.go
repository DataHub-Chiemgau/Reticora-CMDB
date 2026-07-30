package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidCursor is returned when a client supplies a cursor that cannot be
// decoded or that does not match the requested sort order.
var ErrInvalidCursor = errors.New("invalid cursor")

// Cursor is the decoded form of an opaque keyset-pagination cursor. It pins the
// sort order the cursor was created with plus the (sort value, id) tuple of the
// last row of the previous page, so paging stays stable while rows are inserted
// or deleted concurrently.
type Cursor struct {
	Sort  string `json:"s"`
	Dir   string `json:"d"`
	Value string `json:"v"`
	ID    string `json:"i"`
}

// EncodeCursor serialises a cursor into an opaque, URL-safe token.
func EncodeCursor(c Cursor) string {
	payload, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

// DecodeCursor parses an opaque cursor token.
func DecodeCursor(raw string) (Cursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: not base64url", ErrInvalidCursor)
	}
	var c Cursor
	if err := json.Unmarshal(payload, &c); err != nil {
		return Cursor{}, fmt.Errorf("%w: malformed payload", ErrInvalidCursor)
	}
	if c.ID == "" {
		return Cursor{}, fmt.Errorf("%w: missing id", ErrInvalidCursor)
	}
	return c, nil
}

// Matches reports whether the cursor was issued for the given sort order. A
// cursor that was created for a different ordering cannot be applied safely and
// must be rejected instead of silently returning wrong rows.
func (c Cursor) Matches(sort, dir string) bool {
	return strings.EqualFold(c.Sort, sort) && strings.EqualFold(c.Dir, dir)
}

// KeysetClause renders the SQL row-comparison predicate that continues a keyset
// scan after the row addressed by a cursor. The sort column is compared
// together with the primary key so rows with duplicate sort values are neither
// skipped nor returned twice. castType is the PostgreSQL type of the sort
// column (e.g. "timestamptz" or "text"); column and castType must never come
// from user input directly — repositories map them from a fixed allow-list.
func KeysetClause(column, castType, direction string, argPos int) string {
	comparison := "<"
	if strings.EqualFold(direction, "asc") {
		comparison = ">"
	}
	return fmt.Sprintf("(%s, id) %s ($%d::%s, $%d::uuid)", column, comparison, argPos, castType, argPos+1)
}

// KeysetCompare reports whether a row with the given (sort value, id) tuple
// comes after the cursor position for the requested direction. It is the
// in-memory equivalent of KeysetClause.
func KeysetCompare(value, id string, cursor Cursor, direction string) bool {
	if strings.EqualFold(direction, "asc") {
		return value > cursor.Value || (value == cursor.Value && id > cursor.ID)
	}
	return value < cursor.Value || (value == cursor.Value && id < cursor.ID)
}
