// Package httpx provides HTTP utilities for the Reticora platform.
package httpx

import (
	"encoding/json"
	"net/http"
)

// RespondJSON marshals v as JSON and writes it to w with the given status code.
func RespondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// ProblemDetail represents an RFC 7807 problem response.
type ProblemDetail struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	TraceID  string `json:"trace_id,omitempty"`
}

// RespondProblem writes an RFC 7807 problem detail response.
func RespondProblem(w http.ResponseWriter, p ProblemDetail) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
