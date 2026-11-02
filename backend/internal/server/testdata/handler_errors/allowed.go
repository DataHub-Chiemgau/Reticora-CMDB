package handlererrors

import (
	"errors"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Nothing in this file is reported.

// errTaken is a sentinel with a fixed text.
var errTaken = errors.New("the name is already taken")

// notFound are sentinels the handlers report by their own text.
var notFound = []error{errors.New("item not found"), errTaken}

// readJSON writes the decode error of the body.
func readJSON(w http.ResponseWriter, r *http.Request) {
	var req item
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
	}
}

// validateName checks a name.
// validation: fixed texts naming the rule.
func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	return nil
}

// validated writes the error of a validation function.
func validated(w http.ResponseWriter, r *http.Request) {
	if err := validateName(r.URL.Query().Get("name")); err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
	}
}

// sentinel writes the text of the sentinel, the rest sanitized.
func (h *handler) sentinel(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Create(r.Context(), "x")
	switch {
	case errors.Is(err, errTaken):
		api.WriteError(w, http.StatusConflict, "Conflict", errTaken.Error())
	case err != nil:
		api.WriteRepoError(w, err)
	}
}

// sentinelList writes the sentinel of a literal list.
func (h *handler) sentinelList(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Create(r.Context(), "x")
	for _, missing := range notFound {
		if errors.Is(err, missing) {
			api.WriteError(w, http.StatusNotFound, "Not Found", missing.Error())
			return
		}
	}
}

// ruleError is a typed rule error with a fixed text.
type ruleError struct{ Rule string }

// validation: a fixed text naming the rule.
func (e *ruleError) Error() string { return "the rule " + e.Rule + " refuses the request" }

// typedRule writes the text of the typed rule error.
func (h *handler) typedRule(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Create(r.Context(), "x")
	var rule *ruleError
	if errors.As(err, &rule) {
		api.WriteError(w, http.StatusConflict, "Conflict", rule.Error())
	}
}

// shadowed writes the ReadJSON error of the inner block, not the
// repository's error of the outer one.
func (h *handler) shadowed(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Create(r.Context(), "x")
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	var req item
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
	}
}

// cursor writes the cursor error of api.ParsePagination.
func cursor(w http.ResponseWriter, r *http.Request) {
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
	}
}

// fail is a writer of the package: its detail reaches the response, so
// its callers are checked.
func fail(w http.ResponseWriter, detail string) {
	api.WriteError(w, http.StatusBadRequest, "Bad Request", detail)
}

// failFixed calls the writer with a fixed text.
func failFixed(w http.ResponseWriter) {
	fail(w, "the request is invalid")
}
