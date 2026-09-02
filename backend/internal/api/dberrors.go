package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL SQLSTATE codes that map onto client-visible API errors.
const (
	sqlStateNotNullViolation    = "23502"
	sqlStateForeignKeyViolation = "23503"
	sqlStateUniqueViolation     = "23505"
	sqlStateCheckViolation      = "23514"
	sqlStateExclusionViolation  = "23P01"
	sqlStateInvalidTextRepr     = "22P02"
	sqlStateStringTooLong       = "22001"
	sqlStateNumericOutOfRange   = "22003"
	sqlStateInvalidDatetime     = "22007"
	sqlStateSerializationFail   = "40001"
	sqlStateDeadlock            = "40P01"
	sqlStateInsufficientPriv    = "42501"
)

// WriteDBError translates a PostgreSQL constraint or data error into an
// RFC 7807 response with an appropriate status code. It reports whether the
// error was recognised so callers can fall back to their own handling.
//
// Detail messages deliberately omit table, column and constraint identifiers:
// a failed write must not disclose the database schema to API clients.
func WriteDBError(w http.ResponseWriter, err error) bool {
	status, title, detail, ok := ClassifyDBError(err)
	if !ok {
		return false
	}
	WriteError(w, status, title, detail)
	return true
}

// ClassifyDBError maps a PostgreSQL error onto an HTTP status, title and a
// sanitized detail message. The final return value reports whether err was a
// recognised PostgreSQL error.
func ClassifyDBError(err error) (status int, title, detail string, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return 0, "", "", false
	}
	switch pgErr.Code {
	case sqlStateUniqueViolation:
		return http.StatusConflict, "Conflict",
			"a record with the same unique value already exists", true
	case sqlStateForeignKeyViolation:
		return http.StatusUnprocessableEntity, "Unprocessable Entity",
			"a referenced record does not exist or is still in use", true
	case sqlStateNotNullViolation:
		return http.StatusBadRequest, "Bad Request",
			"a required field is missing", true
	case sqlStateCheckViolation, sqlStateExclusionViolation:
		return http.StatusUnprocessableEntity, "Unprocessable Entity",
			"a field value is not allowed by the data model", true
	case sqlStateInvalidTextRepr, sqlStateStringTooLong, sqlStateNumericOutOfRange, sqlStateInvalidDatetime:
		return http.StatusBadRequest, "Bad Request",
			"a field value has an invalid format or is out of range", true
	case sqlStateSerializationFail, sqlStateDeadlock:
		return http.StatusConflict, "Conflict",
			"the request conflicted with a concurrent change, please retry", true
	case sqlStateInsufficientPriv:
		return http.StatusForbidden, "Forbidden",
			"the operation is not permitted", true
	default:
		return 0, "", "", false
	}
}

// WriteRepoError reports a repository failure to the client. Recognised
// PostgreSQL errors become a precise, sanitized RFC 7807 response; anything
// else becomes a generic 500.
//
// It exists so handlers never pass err.Error() to the client: raw pgx errors
// carry table, column and constraint names, and echoing them discloses the
// database schema to any caller able to trigger a failed write.
func WriteRepoError(w http.ResponseWriter, err error) {
	if WriteDBError(w, err) {
		return
	}
	WriteError(w, http.StatusInternalServerError, "Internal Error", "the request could not be completed")
}
