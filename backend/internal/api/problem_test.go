package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// TestEveryErrorIsAProblemWithTraceID covers API-03: every error status the
// API uses is written as application/problem+json with the trace_id of the
// request (X-Request-ID of the response), whichever helper writes it.
func TestEveryErrorIsAProblemWithTraceID(t *testing.T) {
	writers := map[int]func(w http.ResponseWriter){
		http.StatusBadRequest:   func(w http.ResponseWriter) { api.WriteError(w, http.StatusBadRequest, "Bad Request", "bad") },
		http.StatusUnauthorized: func(w http.ResponseWriter) { api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "no token") },
		http.StatusForbidden: func(w http.ResponseWriter) {
			api.WriteProblem(w, http.StatusForbidden, api.ProblemFeatureNotEntitled, "Forbidden", "plan")
		},
		http.StatusNotFound: func(w http.ResponseWriter) { api.WriteError(w, http.StatusNotFound, "Not Found", "gone") },
		http.StatusConflict: func(w http.ResponseWriter) {
			api.WriteFieldError(w, &api.ConflictError{Field: "name", Message: "taken"})
		},
		http.StatusPreconditionFailed: func(w http.ResponseWriter) { api.WritePreconditionFailed(w, "If-Match required") },
		http.StatusUnprocessableEntity: func(w http.ResponseWriter) {
			// A handler that answers with its own struct through WriteJSON.
			api.WriteJSON(w, http.StatusUnprocessableEntity, struct {
				api.ProblemDetail
				Violations []string `json:"violations"`
			}{api.ProblemDetail{Type: "https://reticora.io/problems/422", Title: "Unprocessable Entity", Status: 422}, []string{"x"}})
		},
		http.StatusTooManyRequests: func(w http.ResponseWriter) {
			api.WriteProblem(w, http.StatusTooManyRequests, "urn:reticora:problem:rate-limited", "Rate Limited", "slow down")
		},
		http.StatusInternalServerError: func(w http.ResponseWriter) {
			api.WriteTypedProblem(w, http.StatusInternalServerError, typedErr{})
		},
	}
	for status, write := range writers {
		w := httptest.NewRecorder()
		w.Header().Set(api.RequestIDHeader, "req-123")
		write(w)
		if w.Code != status {
			t.Errorf("%d: status %d", status, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != api.ProblemContentType {
			t.Errorf("%d: Content-Type %q", status, ct)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		if body["trace_id"] != "req-123" || body["status"] != float64(status) || body["type"] == "" || body["title"] == "" {
			t.Errorf("%d: body %v", status, body)
		}
	}
}

type typedErr struct{}

func (typedErr) Error() string       { return "boom" }
func (typedErr) ProblemType() string { return "https://reticora.io/problems/500" }

// TestProblemWithoutRequestIDGetsOne: outside the middleware chain the
// writer assigns a trace id and returns it in X-Request-ID.
func TestProblemWithoutRequestIDGetsOne(t *testing.T) {
	w := httptest.NewRecorder()
	api.WriteError(w, http.StatusNotFound, "Not Found", "gone")
	id := w.Header().Get(api.RequestIDHeader)
	if len(id) != 32 || !strings.Contains(w.Body.String(), `"trace_id":"`+id+`"`) {
		t.Errorf("trace id %q, body %s", id, w.Body.String())
	}
}

// TestSuccessStaysJSON: only errors use the problem media type.
func TestSuccessStaysJSON(t *testing.T) {
	w := httptest.NewRecorder()
	api.WriteJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
	if ct := w.Header().Get("Content-Type"); ct != "application/json" || strings.Contains(w.Body.String(), "trace_id") {
		t.Errorf("success: %q %s", ct, w.Body.String())
	}
}
