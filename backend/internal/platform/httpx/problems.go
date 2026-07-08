package httpx

import "net/http"

// Problem type URIs as defined in the spec.
const (
	TypeValidationError   = "urn:reticora:problem:validation-error"
	TypeUnauthorized      = "urn:reticora:problem:unauthorized"
	TypeForbidden         = "urn:reticora:problem:forbidden"
	TypeEntitlementLimit  = "urn:reticora:problem:entitlement-limit"
	TypeNotFound          = "urn:reticora:problem:not-found"
	TypeConflict          = "urn:reticora:problem:conflict"
	TypeIdempotencyMismatch = "urn:reticora:problem:idempotency-mismatch"
	TypeRateLimited       = "urn:reticora:problem:rate-limited"
	TypeInternal          = "urn:reticora:problem:internal"
)

// ValidationError returns a 400 validation problem.
func ValidationError(w http.ResponseWriter, r *http.Request, detail string) {
	RespondProblem(w, ProblemDetail{
		Type:     TypeValidationError,
		Title:    "Validation Error",
		Status:   http.StatusBadRequest,
		Detail:   detail,
		Instance: r.URL.Path,
		TraceID:  r.Header.Get("X-Request-ID"),
	})
}

// Unauthorized returns a 401 problem.
func Unauthorized(w http.ResponseWriter, r *http.Request, detail string) {
	RespondProblem(w, ProblemDetail{
		Type:     TypeUnauthorized,
		Title:    "Unauthorized",
		Status:   http.StatusUnauthorized,
		Detail:   detail,
		Instance: r.URL.Path,
		TraceID:  r.Header.Get("X-Request-ID"),
	})
}

// Forbidden returns a 403 problem.
func Forbidden(w http.ResponseWriter, r *http.Request, detail string) {
	RespondProblem(w, ProblemDetail{
		Type:     TypeForbidden,
		Title:    "Forbidden",
		Status:   http.StatusForbidden,
		Detail:   detail,
		Instance: r.URL.Path,
		TraceID:  r.Header.Get("X-Request-ID"),
	})
}

// EntitlementLimit returns a 403 entitlement-limit problem.
func EntitlementLimit(w http.ResponseWriter, r *http.Request, detail string) {
	RespondProblem(w, ProblemDetail{
		Type:     TypeEntitlementLimit,
		Title:    "Entitlement Limit Exceeded",
		Status:   http.StatusForbidden,
		Detail:   detail,
		Instance: r.URL.Path,
		TraceID:  r.Header.Get("X-Request-ID"),
	})
}

// NotFound returns a 404 problem.
func NotFound(w http.ResponseWriter, r *http.Request, detail string) {
	RespondProblem(w, ProblemDetail{
		Type:     TypeNotFound,
		Title:    "Not Found",
		Status:   http.StatusNotFound,
		Detail:   detail,
		Instance: r.URL.Path,
		TraceID:  r.Header.Get("X-Request-ID"),
	})
}

// Conflict returns a 409 problem.
func Conflict(w http.ResponseWriter, r *http.Request, detail string) {
	RespondProblem(w, ProblemDetail{
		Type:     TypeConflict,
		Title:    "Conflict",
		Status:   http.StatusConflict,
		Detail:   detail,
		Instance: r.URL.Path,
		TraceID:  r.Header.Get("X-Request-ID"),
	})
}

// IdempotencyMismatch returns a 409 idempotency-mismatch problem.
func IdempotencyMismatch(w http.ResponseWriter, r *http.Request, detail string) {
	RespondProblem(w, ProblemDetail{
		Type:     TypeIdempotencyMismatch,
		Title:    "Idempotency Mismatch",
		Status:   http.StatusConflict,
		Detail:   detail,
		Instance: r.URL.Path,
		TraceID:  r.Header.Get("X-Request-ID"),
	})
}

// RateLimited returns a 429 problem.
func RateLimited(w http.ResponseWriter, r *http.Request, detail string) {
	RespondProblem(w, ProblemDetail{
		Type:     TypeRateLimited,
		Title:    "Rate Limited",
		Status:   http.StatusTooManyRequests,
		Detail:   detail,
		Instance: r.URL.Path,
		TraceID:  r.Header.Get("X-Request-ID"),
	})
}

// Internal returns a 500 problem. Detail should not expose internals to clients.
func Internal(w http.ResponseWriter, r *http.Request, detail string) {
	RespondProblem(w, ProblemDetail{
		Type:     TypeInternal,
		Title:    "Internal Server Error",
		Status:   http.StatusInternalServerError,
		Detail:   detail,
		Instance: r.URL.Path,
		TraceID:  r.Header.Get("X-Request-ID"),
	})
}
