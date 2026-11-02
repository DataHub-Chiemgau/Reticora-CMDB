// Package handlererrors holds the fixtures of TestHandlerErrorRuleFixtures
// (WP-408). Every function of this file writes the text of a repository
// error into a response once, and the rule reports it once.
package handlererrors

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

type item struct{ Name string }

type repository interface {
	Create(ctx context.Context, name string) (*item, error)
}

type handler struct{ repo repository }

// repositoryError writes the text of the repository's error (F-047).
func (h *handler) repositoryError(w http.ResponseWriter, r *http.Request) {
	if _, err := h.repo.Create(r.Context(), "x"); err != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
	}
}

// alias hides the repository's error behind another name in the same
// block.
func (h *handler) alias(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Create(r.Context(), "x")
	if err != nil {
		e := err
		api.WriteError(w, http.StatusConflict, "Conflict", e.Error())
	}
}

// formatted formats the repository's error into the detail.
func (h *handler) formatted(w http.ResponseWriter, r *http.Request) {
	if _, err := h.repo.Create(r.Context(), "x"); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", fmt.Sprintf("create failed: %v", err))
	}
}

// stringVariable passes the text through a local variable.
func (h *handler) stringVariable(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Create(r.Context(), "x")
	if err != nil {
		msg := err.Error()
		api.WriteError(w, http.StatusConflict, "Conflict", "create: "+msg)
	}
}

// fieldMessage writes the text as the message of a field error.
func (h *handler) fieldMessage(w http.ResponseWriter, r *http.Request) {
	if _, err := h.repo.Create(r.Context(), "x"); err != nil {
		api.WriteFieldError(w, &api.FieldError{Field: "name", Message: err.Error()})
	}
}

// fieldMessageUnkeyed writes the text into a field error without keys.
func (h *handler) fieldMessageUnkeyed(w http.ResponseWriter, r *http.Request) {
	if _, err := h.repo.Create(r.Context(), "x"); err != nil {
		api.WriteFieldError(w, &api.FieldError{"name", err.Error(), http.StatusUnprocessableEntity})
	}
}

// driverField writes the message of the PostgreSQL error.
func (h *handler) driverField(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Create(r.Context(), "x")
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		api.WriteError(w, http.StatusConflict, "Conflict", pgErr.Message)
	}
}

// writeAny writes whatever error it gets: a parameter has no origin.
func writeAny(w http.ResponseWriter, err error) {
	api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
}

// localWriter passes the text to a writer of the package (fail).
func (h *handler) localWriter(w http.ResponseWriter, r *http.Request) {
	if _, err := h.repo.Create(r.Context(), "x"); err != nil {
		fail(w, err.Error())
	}
}

// reassigned replaces the ReadJSON error by the repository's error in a
// nested block.
func (h *handler) reassigned(w http.ResponseWriter, r *http.Request) {
	var req item
	err := api.ReadJSON(r, &req)
	if err == nil {
		_, err = h.repo.Create(r.Context(), req.Name)
	}
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
	}
}

// reassignedInLoop writes, in the second round, the repository's error of
// the first.
func (h *handler) reassignedInLoop(w http.ResponseWriter, r *http.Request) {
	var req item
	err := api.ReadJSON(r, &req)
	for _, name := range []string{"a", "b"} {
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		_, err = h.repo.Create(r.Context(), name)
	}
}
