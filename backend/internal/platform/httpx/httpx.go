// Package httpx provides HTTP utilities for the Reticora platform.
package httpx

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// ProblemDetail represents an RFC 7807 problem response.
type ProblemDetail = api.ProblemDetail

// RespondProblem writes an RFC 7807 problem detail response through the one
// problem writer (api.WriteProblemBody), which adds the trace_id.
func RespondProblem(w http.ResponseWriter, p *ProblemDetail) {
	api.WriteProblemBody(w, p.Status, p)
}
