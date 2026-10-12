package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// FieldError rejects the value of one request field.
type FieldError struct {
	Field   string
	Message string
	// Status is the HTTP status; zero means 400.
	Status int
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// ConflictError reports a unique field whose value is already taken (409).
type ConflictError struct {
	Field   string
	Message string
}

func (e *ConflictError) Error() string { return e.Field + ": " + e.Message }

// FieldProblem is an RFC 7807 problem that names the request field.
type FieldProblem struct {
	ProblemDetail
	Field string `json:"field"`
}

// WriteFieldError answers a *FieldError (400 unless it carries another
// status) or a *ConflictError (409) with the field named, and reports
// whether err was one of them.
func WriteFieldError(w http.ResponseWriter, err error) bool {
	var fe *FieldError
	if errors.As(err, &fe) {
		status := fe.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		WriteJSON(w, status, FieldProblem{
			ProblemDetail: ProblemDetail{Type: "urn:reticora:problem:invalid-field", Title: http.StatusText(status),
				Status: status, Detail: fe.Error()},
			Field: fe.Field,
		})
		return true
	}
	var ce *ConflictError
	if errors.As(err, &ce) {
		WriteJSON(w, http.StatusConflict, FieldProblem{
			ProblemDetail: ProblemDetail{Type: "urn:reticora:problem:conflict", Title: "Conflict",
				Status: http.StatusConflict, Detail: ce.Error()},
			Field: ce.Field,
		})
		return true
	}
	return false
}

// FieldDBError maps a unique, check or foreign-key violation of a
// constraint listed in fields onto the request field it protects: unique →
// *ConflictError, check and foreign key → *FieldError. Other errors pass
// through unchanged.
func FieldDBError(err error, fields map[string]string) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	field, known := fields[pgErr.ConstraintName]
	if !known {
		return err
	}
	switch pgErr.Code {
	case sqlStateUniqueViolation:
		return &ConflictError{Field: field, Message: "the value is already taken"}
	case sqlStateCheckViolation:
		return &FieldError{Field: field, Message: "the value is not allowed"}
	case sqlStateForeignKeyViolation:
		return &FieldError{Field: field, Message: "the referenced record does not exist"}
	}
	return err
}
