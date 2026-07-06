// Package api provides shared API types and helpers for the Reticora REST API.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// PaginationParams holds cursor-based pagination parameters.
type PaginationParams struct {
	Limit  int
	Offset int
}

// ParsePagination extracts pagination parameters from request query.
func ParsePagination(r *http.Request) PaginationParams {
	p := PaginationParams{Limit: 50, Offset: 0}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			p.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.Offset = n
		}
	}
	return p
}

// ListResponse is a generic paginated response wrapper.
type ListResponse[T any] struct {
	Data       []T  `json:"data"`
	Total      int  `json:"total"`
	Limit      int  `json:"limit"`
	Offset     int  `json:"offset"`
	HasMore    bool `json:"has_more"`
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
