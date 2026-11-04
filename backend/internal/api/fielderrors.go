package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// FieldError rejects the value of one request field.
type FieldError struct {
	Field   string
	Message string
	// Code names the failing rule in machine-readable form, e.g. the code
	// of a composition conflict (MET-16); empty when there is none.
	Code string
	// Status is the HTTP status; zero means 400.
	Status int
}

// validation: the field and its message; handler_errors_test checks where a
// message is built.
func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// ConflictError reports a unique field whose value is already taken (409).
type ConflictError struct {
	Field   string
	Message string
}

// validation: the field and its message, as FieldError.
func (e *ConflictError) Error() string { return e.Field + ": " + e.Message }

// ProblemTypeForbidden is the problem type of a write or filter refused by
// field permissions (RBA-07, WP-265); the value of httpx.TypeForbidden,
// declared here because httpx imports api. The route permission answers
// 403 with https://reticora.io/problems/403 (WriteError) instead.
const ProblemTypeForbidden = "urn:reticora:problem:forbidden"

// FieldForbiddenError refuses a request on fields the principal may not
// read or write (field permissions, RBA-07): 403 with the DTO paths of the
// fields (hostname, attributes.cost_center), never their values.
type FieldForbiddenError struct {
	Fields []string
}

// validation: a fixed rule text with the DTO paths of the refused fields.
func (e *FieldForbiddenError) Error() string {
	return "field permissions do not allow this request on: " + strings.Join(e.Fields, ", ")
}

// FieldForbiddenProblem is the RFC 7807 body of a *FieldForbiddenError.
type FieldForbiddenProblem struct {
	ProblemDetail
	Fields []string `json:"fields"`
}

// FieldProblem is an RFC 7807 problem that names the request field.
type FieldProblem struct {
	ProblemDetail
	Field string `json:"field"`
	// Code is FieldError.Code; omitted when empty.
	Code string `json:"code,omitempty"`
}

// Violation names a field of a problem and what is wrong with its value.
type Violation struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

// ViolationsConflict is a conflict on fields whose values another resource
// holds, e.g. the unique attributes of a CI (MET-15).
type ViolationsConflict interface {
	error
	ConflictViolations() []Violation
}

// WriteFieldError answers a *FieldError (400 unless it carries another
// status) or a *ConflictError (409) with the field named, a
// ViolationsConflict (409 with violations) or a *FieldForbiddenError (403
// with fields), and reports whether err was one of them.
func WriteFieldError(w http.ResponseWriter, err error) bool {
	var ff *FieldForbiddenError
	if errors.As(err, &ff) {
		fields := ff.Fields
		if fields == nil {
			fields = []string{}
		}
		WriteProblemBody(w, http.StatusForbidden, FieldForbiddenProblem{
			ProblemDetail: ProblemDetail{Type: ProblemTypeForbidden, Title: "Forbidden",
				Status: http.StatusForbidden, Detail: ff.Error()},
			Fields: fields,
		})
		return true
	}
	var vc ViolationsConflict
	if errors.As(err, &vc) {
		WriteProblemBody(w, http.StatusConflict, struct {
			ProblemDetail
			Violations []Violation `json:"violations"`
		}{
			ProblemDetail: ProblemDetail{Type: "urn:reticora:problem:conflict", Title: "Conflict",
				Status: http.StatusConflict, Detail: vc.Error()},
			Violations: vc.ConflictViolations(),
		})
		return true
	}
	var fe *FieldError
	if errors.As(err, &fe) {
		status := fe.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		WriteProblemBody(w, status, FieldProblem{
			ProblemDetail: ProblemDetail{Type: "urn:reticora:problem:invalid-field", Title: http.StatusText(status),
				Status: status, Detail: fe.Error()},
			Field: fe.Field,
			Code:  fe.Code,
		})
		return true
	}
	var ce *ConflictError
	if errors.As(err, &ce) {
		WriteProblemBody(w, http.StatusConflict, FieldProblem{
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
