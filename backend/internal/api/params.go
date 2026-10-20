package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// IsUUID reports whether s is a UUID in the canonical 8-4-4-4-12 hex form
// that PostgreSQL accepts for ::uuid.
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
				return false
			}
		}
	}
	return true
}

// ParseUUIDParam returns the path parameter name of r when it is a UUID.
// Otherwise it answers the request and reports false: 400 when notFound is
// empty, else 404 with notFound as detail, for handlers whose unknown ids
// already answer 404 (a malformed id names no resource either). The id never
// reaches SQL, so no driver or SQLSTATE text can reach the client (API-03).
func ParseUUIDParam(w http.ResponseWriter, r *http.Request, name, notFound string) (string, bool) {
	id := chi.URLParam(r, name)
	if IsUUID(id) {
		return id, true
	}
	if notFound != "" {
		WriteError(w, http.StatusNotFound, "Not Found", notFound)
	} else {
		WriteError(w, http.StatusBadRequest, "Bad Request", "the path parameter "+name+" must be a UUID")
	}
	return "", false
}

// RequireUUID checks the UUID of the request body field field: it returns a
// 422 *FieldError when value is no UUID (an empty value included), for
// WriteFieldError.
func RequireUUID(field, value string) error {
	if IsUUID(value) {
		return nil
	}
	return &FieldError{Field: field, Message: "must be a UUID", Status: http.StatusUnprocessableEntity}
}

// RejectNUL returns a 400 *FieldError when one of values contains a NUL
// character: PostgreSQL text cannot store it (SQLSTATE 22021, jsonb 22P05).
func RejectNUL(field string, values ...string) error {
	for _, v := range values {
		if strings.ContainsRune(v, 0) {
			return &FieldError{Field: field, Message: "must not contain NUL characters"}
		}
	}
	return nil
}
