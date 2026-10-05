// Package api provides shared API types and helpers for the Reticora REST API.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// Problem types of the entitlement checks (ENT-03, CH21).
const (
	// ProblemEntitlementLimit: a quota (max_cis, ...) is exhausted.
	ProblemEntitlementLimit = "https://reticora.io/problems/entitlement-limit"
	// ProblemFeatureNotEntitled: the organization is not entitled to the feature.
	ProblemFeatureNotEntitled = "https://reticora.io/problems/feature-not-entitled"
	// ProblemLicenseExpired: the discovery license expired (CH21); only
	// discovery and ingest stop.
	ProblemLicenseExpired = "https://reticora.io/problems/license-expired"
)

// WriteProblem writes an RFC 7807 problem detail with its own type.
func WriteProblem(w http.ResponseWriter, status int, problemType, title, detail string) {
	WriteJSON(w, status, ProblemDetail{Type: problemType, Title: title, Status: status, Detail: detail})
}

// TypedProblem is an error that names its RFC 7807 problem type.
type TypedProblem interface {
	error
	ProblemType() string
}

// WriteTypedProblem writes err with its problem type when it names one and
// reports whether it did.
func WriteTypedProblem(w http.ResponseWriter, status int, err error) bool {
	var typed TypedProblem
	if !errors.As(err, &typed) {
		return false
	}
	WriteProblem(w, status, typed.ProblemType(), http.StatusText(status), typed.Error())
	return true
}

// ReadJSON decodes JSON from request body.
func ReadJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// IfMatchAny is the If-Match value "*": any current version matches.
const IfMatchAny int64 = 0

// ETag formats a resource version as a strong entity tag.
func ETag(version int64) string {
	return fmt.Sprintf("%q", strconv.FormatInt(version, 10))
}

// ParseIfMatch reads the If-Match header: ok is false when there is none;
// "*" yields IfMatchAny; a weak tag (W/) is accepted like a strong one. A
// value that is not a version is an error (412 for the caller).
func ParseIfMatch(r *http.Request) (version int64, ok bool, err error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, false, nil
	}
	if raw == "*" {
		return IfMatchAny, true, nil
	}
	raw = strings.TrimPrefix(raw, "W/")
	unquoted, uerr := strconv.Unquote(raw)
	if uerr != nil {
		unquoted = raw
	}
	version, err = strconv.ParseInt(unquoted, 10, 64)
	if err != nil || version < 1 {
		return 0, true, fmt.Errorf("If-Match %q is not a version", r.Header.Get("If-Match"))
	}
	return version, true, nil
}

// WritePreconditionFailed writes the 412 problem of API-03.
func WritePreconditionFailed(w http.ResponseWriter, detail string) {
	WriteJSON(w, http.StatusPreconditionFailed, ProblemDetail{
		Type:   "urn:reticora:problem:precondition-failed",
		Title:  "Precondition Failed",
		Status: http.StatusPreconditionFailed,
		Detail: detail,
	})
}
