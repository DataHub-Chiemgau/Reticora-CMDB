package user

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for user, team, and role endpoints.
type Handler struct {
	repo Repository
}

// NewHandler creates a new user/team/role handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers user/team/role routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	// Users
	r.Get("/api/v1/users", h.ListUsers)
	r.Post("/api/v1/users", h.CreateUser)
	r.Get("/api/v1/users/{id}", h.GetUser)
	r.Patch("/api/v1/users/{id}", h.UpdateUser)
	r.Delete("/api/v1/users/{id}", h.DeleteUser)
	r.Get("/api/v1/users/{id}/roles", h.ListUserRoles)

	// Teams
	r.Get("/api/v1/teams", h.ListTeams)
	r.Post("/api/v1/teams", h.CreateTeam)
	r.Get("/api/v1/teams/{id}", h.GetTeam)
	r.Patch("/api/v1/teams/{id}", h.UpdateTeam)
	r.Delete("/api/v1/teams/{id}", h.DeleteTeam)
	r.Get("/api/v1/teams/{id}/members", h.ListTeamMembers)
	r.Post("/api/v1/teams/{id}/members", h.AddTeamMember)
	r.Delete("/api/v1/teams/{teamId}/members/{userId}", h.RemoveTeamMember)

	// Custom Roles
	r.Get("/api/v1/roles", h.ListRoles)
	r.Post("/api/v1/roles", h.CreateRole)
	r.Get("/api/v1/roles/{id}", h.GetRole)
	r.Patch("/api/v1/roles/{id}", h.UpdateRole)
	r.Delete("/api/v1/roles/{id}", h.DeleteRole)
	r.Post("/api/v1/roles/assign", h.AssignRole)
}

// --- User handlers ---

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	search := r.URL.Query().Get("search")

	items, total, err := h.repo.ListUsers(t.OrganizationID, search, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[User]{
		Data:    items,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	item, err := h.repo.GetUser(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "user not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req CreateUserRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.Email == "" || req.DisplayName == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "email and display_name are required")
		return
	}

	status := req.Status
	if status == "" {
		status = "active"
	}

	u := &User{
		OrganizationID: t.OrganizationID,
		Email:          req.Email,
		DisplayName:    req.DisplayName,
		Status:         status,
		ExternalID:     req.ExternalID,
	}

	if err := h.repo.CreateUser(u); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, u)
}

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	var req UpdateUserRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	item, err := h.repo.UpdateUser(t.OrganizationID, id, req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "user not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.DeleteUser(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "user not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListUserRoles(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	userID := chi.URLParam(r, "id")
	roles, err := h.repo.ListUserRoles(t.OrganizationID, userID)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, roles)
}

// --- Team handlers ---

func (h *Handler) ListTeams(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	search := r.URL.Query().Get("search")

	items, total, err := h.repo.ListTeams(t.OrganizationID, search, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Team]{
		Data:    items,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) GetTeam(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	item, err := h.repo.GetTeam(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "team not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) CreateTeam(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req CreateTeamRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}

	team := &Team{
		OrganizationID: t.OrganizationID,
		Name:           req.Name,
		Description:    req.Description,
		LeadID:         req.LeadID,
	}

	if err := h.repo.CreateTeam(team); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, team)
}

func (h *Handler) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	var req UpdateTeamRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	item, err := h.repo.UpdateTeam(t.OrganizationID, id, req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "team not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.DeleteTeam(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "team not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListTeamMembers(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	teamID := chi.URLParam(r, "id")
	members, err := h.repo.ListTeamMembers(t.OrganizationID, teamID)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, members)
}

func (h *Handler) AddTeamMember(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	teamID := chi.URLParam(r, "id")
	var req AddMemberRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.UserID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "user_id is required")
		return
	}

	roleInTeam := req.RoleInTeam
	if roleInTeam == "" {
		roleInTeam = "member"
	}

	member := &TeamMember{
		TeamID:     teamID,
		UserID:     req.UserID,
		RoleInTeam: roleInTeam,
	}

	if err := h.repo.AddTeamMember(t.OrganizationID, member); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, member)
}

func (h *Handler) RemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	teamID := chi.URLParam(r, "teamId")
	userID := chi.URLParam(r, "userId")

	if err := h.repo.RemoveTeamMember(t.OrganizationID, teamID, userID); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Role handlers ---

func (h *Handler) ListRoles(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	items, total, err := h.repo.ListRoles(t.OrganizationID, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[CustomRole]{
		Data:    items,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) GetRole(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	item, err := h.repo.GetRole(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "role not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) CreateRole(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req CreateRoleRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}

	role := &CustomRole{
		OrganizationID: t.OrganizationID,
		Name:           req.Name,
		Description:    req.Description,
		Permissions:    req.Permissions,
	}
	if role.Permissions == nil {
		role.Permissions = []string{}
	}

	if err := h.repo.CreateRole(role); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, role)
}

func (h *Handler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	var req UpdateRoleRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	item, err := h.repo.UpdateRole(t.OrganizationID, id, req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.DeleteRole(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) AssignRole(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req AssignRoleRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.UserID == "" || req.CustomRoleID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "user_id and custom_role_id are required")
		return
	}

	scopeType := req.ScopeType
	if scopeType == "" {
		scopeType = "organization"
	}

	assignment := &UserRoleAssignment{
		UserID:       req.UserID,
		CustomRoleID: req.CustomRoleID,
		ScopeType:    scopeType,
		ScopeID:      req.ScopeID,
		GrantedBy:    t.UserID,
	}

	if err := h.repo.AssignRole(t.OrganizationID, assignment); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, assignment)
}
