package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrInvalidCursor is returned when a client supplies a cursor that cannot be
// decoded, whose signature does not verify, or that does not match the
// requested sort order.
var ErrInvalidCursor = errors.New("invalid cursor")

// Cursor is the decoded form of an opaque pagination cursor (API-02). A
// keyset cursor pins the sort order it was created with plus the (sort
// value, id) tuple of the last row of the previous page, so paging stays
// stable while rows are inserted or deleted concurrently. Lists without a
// keyset order use an offset cursor (Offset set, no ID); ParsePagination
// turns it into PaginationParams.Offset.
type Cursor struct {
	Sort   string `json:"s,omitempty"`
	Dir    string `json:"d,omitempty"`
	Value  string `json:"v,omitempty"`
	ID     string `json:"i,omitempty"`
	Offset int    `json:"o,omitempty"`
}

// IsOffset reports whether c is an offset cursor rather than a keyset one.
func (c Cursor) IsOffset() bool { return c.ID == "" && c.Offset > 0 }

// cursorKey signs cursors (HMAC-SHA256). The server sets it from the master
// key so every replica accepts the cursors of the others; until then a
// random per-process key is used.
var cursorKey = struct {
	sync.RWMutex
	key []byte
}{key: randomCursorKey()}

func randomCursorKey() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}

// SetCursorKey sets the key cursors are signed and verified with. A key
// shorter than 16 bytes is refused.
func SetCursorKey(key []byte) error {
	if len(key) < 16 {
		return errors.New("cursor key must be at least 16 bytes")
	}
	cursorKey.Lock()
	cursorKey.key = append([]byte(nil), key...)
	cursorKey.Unlock()
	return nil
}

// cursorMACLen is the length of the truncated HMAC in a cursor token.
const cursorMACLen = 16

func cursorMAC(payload []byte) []byte {
	cursorKey.RLock()
	mac := hmac.New(sha256.New, cursorKey.key)
	cursorKey.RUnlock()
	mac.Write(payload)
	return mac.Sum(nil)[:cursorMACLen]
}

// EncodeCursor serialises a cursor into an opaque, URL-safe token: the
// base64url payload and its HMAC, separated by a dot. Clients cannot forge
// or alter a cursor (API-02).
func EncodeCursor(c Cursor) string {
	payload, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(cursorMAC(payload))
}

// EncodeOffsetCursor returns the cursor of the page starting at offset.
func EncodeOffsetCursor(offset int) string {
	return EncodeCursor(Cursor{Offset: offset})
}

// DecodeCursor verifies and parses an opaque cursor token.
func DecodeCursor(raw string) (Cursor, error) {
	body, sig, ok := strings.Cut(strings.TrimSpace(raw), ".")
	if !ok {
		return Cursor{}, fmt.Errorf("%w: not signed", ErrInvalidCursor)
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: not base64url", ErrInvalidCursor)
	}
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(mac, cursorMAC(payload)) {
		return Cursor{}, fmt.Errorf("%w: signature does not verify", ErrInvalidCursor)
	}
	var c Cursor
	if err := json.Unmarshal(payload, &c); err != nil {
		return Cursor{}, fmt.Errorf("%w: malformed payload", ErrInvalidCursor)
	}
	if c.ID == "" && c.Offset <= 0 {
		return Cursor{}, fmt.Errorf("%w: missing id", ErrInvalidCursor)
	}
	if c.Offset < 0 {
		return Cursor{}, fmt.Errorf("%w: negative offset", ErrInvalidCursor)
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
