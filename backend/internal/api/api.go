// Package api provides shared API types and helpers for the Reticora REST API.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// DefaultPageLimit is used when a request does not specify a limit.
const DefaultPageLimit = 50

// MaxPageLimit caps the page size a client may request.
const MaxPageLimit = 100

// PaginationParams holds pagination parameters. Both the legacy
// limit/offset form and the cursor (keyset) form are supported; when Cursor is
// set, Offset is ignored by repositories that implement keyset pagination.
type PaginationParams struct {
	Limit  int
	Offset int
	// Cursor is the decoded cursor when the request carried a valid one.
	Cursor *Cursor
	// CursorError is set when the request carried a cursor that could not be
	// decoded. Handlers should answer with 400 in that case.
	CursorError error
}

// ParsePagination extracts pagination parameters from the request query. A
// malformed cursor is reported through PaginationParams.CursorError so callers
// can reject the request instead of silently falling back to offset paging.
func ParsePagination(r *http.Request) PaginationParams {
	p := PaginationParams{Limit: DefaultPageLimit, Offset: 0}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= MaxPageLimit {
			p.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.Offset = n
		}
	}
	if v := r.URL.Query().Get("cursor"); v != "" {
		cursor, err := DecodeCursor(v)
		if err != nil {
			p.CursorError = err
			return p
		}
		p.Cursor = &cursor
	}
	return p
}

// ListResponse is a generic paginated response wrapper. NextCursor is only set
// for endpoints that support keyset pagination and when more rows follow.
type ListResponse[T any] struct {
	Data       []T    `json:"data"`
	Total      int    `json:"total"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// ProblemDetail implements RFC 7807.
type ProblemDetail struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// WriteJSON writes a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteError writes an RFC 7807 problem detail.
func WriteError(w http.ResponseWriter, status int, title, detail string) {
	WriteJSON(w, status, ProblemDetail{
		Type:   fmt.Sprintf("https://reticora.io/problems/%d", status),
		Title:  title,
		Status: status,
		Detail: detail,
	})
}

// ReadJSON decodes JSON from request body.
func ReadJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
