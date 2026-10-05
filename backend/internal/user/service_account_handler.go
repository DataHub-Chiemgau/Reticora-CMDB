package user

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// ServiceAccountHandler serves /api/v1/service-accounts (RBA-08, API-05).
// Reading needs user:read, every change user:manage (authz).
type ServiceAccountHandler struct {
	repo ServiceAccountRepository
}

// NewServiceAccountHandler creates the handler.
func NewServiceAccountHandler(repo ServiceAccountRepository) *ServiceAccountHandler {
	return &ServiceAccountHandler{repo: repo}
}

// RegisterRoutes registers the service-accounts routes.
func (h *ServiceAccountHandler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/service-accounts", h.List)
	r.Post("/api/v1/service-accounts", h.Create)
	r.Get("/api/v1/service-accounts/{id}", h.Get)
	r.Patch("/api/v1/service-accounts/{id}", h.Update)
	r.Delete("/api/v1/service-accounts/{id}", h.Delete)
	r.Put("/api/v1/service-accounts/{id}/roles", h.SetRoles)
}

func writeServiceAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrServiceAccountNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "service account not found")
	case errors.Is(err, ErrServiceAccountInUse):
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
	case err.Error() == "name is required", err.Error() == "unknown role in assignments":
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// List returns the organization's service accounts with their roles.
func (h *ServiceAccountHandler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	items, err := h.repo.ListServiceAccounts(r.Context(), t.OrganizationID)
	if err != nil {
		writeServiceAccountError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[ServiceAccount]{Data: items, Total: len(items), Limit: len(items)})
}

// Create adds a service account without roles.
func (h *ServiceAccountHandler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	sa := &ServiceAccount{OrganizationID: t.OrganizationID, Name: req.Name, Description: req.Description, CreatedBy: t.UserID}
	if err := h.repo.CreateServiceAccount(r.Context(), sa); err != nil {
		writeServiceAccountError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, sa)
}

// Get returns one service account.
func (h *ServiceAccountHandler) Get(w http.ResponseWriter, r *http.Request) {
	sa, err := h.repo.GetServiceAccount(r.Context(), tenant.FromContext(r.Context()).OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeServiceAccountError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, sa)
}

// Update changes name, description or the active flag. A deactivated
// account grants nothing: its keys and subscriptions lose every right.
func (h *ServiceAccountHandler) Update(w http.ResponseWriter, r *http.Request) {
	var req ServiceAccountUpdate
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	sa, err := h.repo.UpdateServiceAccount(r.Context(), tenant.FromContext(r.Context()).OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeServiceAccountError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, sa)
}

// Delete removes an account that no key or subscription is bound to.
func (h *ServiceAccountHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.DeleteServiceAccount(r.Context(), tenant.FromContext(r.Context()).OrganizationID, chi.URLParam(r, "id")); err != nil {
		writeServiceAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetRoles replaces the role assignments of an account.
func (h *ServiceAccountHandler) SetRoles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Roles []ServiceAccountRole `json:"roles"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	sa, err := h.repo.SetServiceAccountRoles(r.Context(), tenant.FromContext(r.Context()).OrganizationID, chi.URLParam(r, "id"), req.Roles)
	if err != nil {
		writeServiceAccountError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, sa)
}
