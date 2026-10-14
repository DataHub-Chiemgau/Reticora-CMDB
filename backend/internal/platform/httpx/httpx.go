// Package httpx provides HTTP utilities for the Reticora platform.
package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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
type ProblemDetail = api.ProblemDetail

// RespondProblem writes an RFC 7807 problem detail response through the one
// problem writer (api.WriteProblemBody), which adds the trace_id.
func RespondProblem(w http.ResponseWriter, p ProblemDetail) {
	api.WriteProblemBody(w, p.Status, p)
}
