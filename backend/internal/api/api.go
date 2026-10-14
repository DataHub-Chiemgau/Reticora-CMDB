// Package api provides shared API types and helpers for the Reticora REST API.
package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// DefaultPageLimit is used when a request does not specify a limit.
const DefaultPageLimit = 50

// MaxPageLimit caps the page size a client may request (API-02); a larger
// limit is capped to it.
const MaxPageLimit = 200

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

// ParsePagination extracts pagination parameters from the request query
// (API-02): limit (default 50, at most 200; a larger value is capped),
// offset and the signed cursor. An offset cursor sets Offset; a keyset
// cursor is returned in Cursor. A malformed or forged cursor is reported
// through PaginationParams.CursorError so callers can reject the request
// instead of silently falling back to the first page.
func ParsePagination(r *http.Request) PaginationParams {
	p := PaginationParams{Limit: DefaultPageLimit, Offset: 0}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.Limit = min(n, MaxPageLimit)
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
		if cursor.IsOffset() {
			p.Offset = cursor.Offset
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

// ProblemDetail implements RFC 7807. TraceID is filled by WriteProblemBody
// from the request id of the response (API-03).
type ProblemDetail struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	TraceID  string `json:"trace_id,omitempty"`
}

// ProblemContentType is the media type of every error response (API-03).
const ProblemContentType = "application/problem+json"

// RequestIDHeader carries the request id the RequestID middleware assigns;
// it is the trace_id of a problem.
const RequestIDHeader = "X-Request-ID"

// CompleteList is the ListResponse of a list that is always returned in
// full (a small catalog, a sub-collection, a tree): one page, no
// next_cursor.
func CompleteList[T any](items []T) ListResponse[T] {
	if items == nil {
		items = []T{}
	}
	return ListResponse[T]{Data: items, Total: len(items), Limit: len(items)}
}

// pagedList is implemented by ListResponse: it fills next_cursor when the
// handler did not set one.
type pagedList interface {
	withNextCursor() any
}

// withNextCursor sets an offset cursor for the next page when the handler
// reports more rows (HasMore) but issued no keyset cursor, so every list
// answers with next_cursor (API-02).
func (l ListResponse[T]) withNextCursor() any {
	if l.NextCursor != "" {
		return l
	}
	if l.HasMore && len(l.Data) > 0 {
		l.NextCursor = EncodeOffsetCursor(l.Offset + len(l.Data))
	}
	return l
}

// WriteJSON writes a JSON response. An error status (4xx/5xx) goes through
// WriteProblemBody, so every error carries the problem media type and a
// trace_id; a ListResponse gets its next_cursor.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	if status >= http.StatusBadRequest {
		WriteProblemBody(w, status, v)
		return
	}
	if list, ok := v.(pagedList); ok {
		v = list.withNextCursor()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteProblemBody is the one writer of error responses (API-03): body is a
// ProblemDetail or a struct embedding one (extension members such as
// violations). It sets application/problem+json and adds the trace_id —
// the request id of the response (X-Request-ID, set by the RequestID
// middleware); without one a fresh id is assigned and returned in the
// header, so the client can always quote it.
func WriteProblemBody(w http.ResponseWriter, status int, body any) {
	traceID := w.Header().Get(RequestIDHeader)
	if traceID == "" {
		traceID = newTraceID()
		w.Header().Set(RequestIDHeader, traceID)
	}
	payload := withTraceID(body, traceID)
	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// withTraceID encodes body and sets its trace_id member unless the body
// carries one already; a body that is not a JSON object is kept as is.
func withTraceID(body any, traceID string) []byte {
	raw, err := json.Marshal(body)
	if err != nil {
		raw, _ = json.Marshal(ProblemDetail{Type: "https://reticora.io/problems/500", Title: "Internal Server Error", Status: http.StatusInternalServerError})
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		return append(raw, '\n')
	}
	if existing, ok := members["trace_id"]; !ok || bytes.Equal(existing, []byte(`""`)) {
		members["trace_id"], _ = json.Marshal(traceID)
	}
	out, _ := json.Marshal(members)
	return append(out, '\n')
}

func newTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// WriteError writes an RFC 7807 problem detail.
func WriteError(w http.ResponseWriter, status int, title, detail string) {
	WriteProblemBody(w, status, ProblemDetail{
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
	WriteProblemBody(w, status, ProblemDetail{Type: problemType, Title: title, Status: status, Detail: detail})
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
	WriteProblemBody(w, http.StatusPreconditionFailed, ProblemDetail{
		Type:   "urn:reticora:problem:precondition-failed",
		Title:  "Precondition Failed",
		Status: http.StatusPreconditionFailed,
		Detail: detail,
	})
}
