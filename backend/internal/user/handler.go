package user

import (
	"context"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/go-chi/chi/v5"
)

// ContactLister gathers every contact record belonging to a person for the
// GDPR data export (implemented by contact.Repository).
type ContactLister interface {
	ListByEmail(ctx context.Context, orgID, email string) ([]contact.Contact, error)
}

// TicketActivityLister gathers the tickets a person reported or is assigned
// to for the GDPR data export (implemented by ticket.Repository).
type TicketActivityLister interface {
	List(ctx context.Context, orgID string, filter ticket.FilterParams, page api.PaginationParams) ([]ticket.Ticket, int, error)
}

// AssignmentLister gathers the assignments a person received or issued for
// the GDPR data export (implemented by assignment.Repository).
type AssignmentLister interface {
	List(ctx context.Context, orgID string, filter assignment.FilterParams, page api.PaginationParams) ([]assignment.Assignment, int, error)
}

// Handler provides HTTP handlers for user, team, and role endpoints.
type Handler struct {
	repo        Repository
	contacts    ContactLister
	tickets     TicketActivityLister
	assignments AssignmentLister
}

// NewHandler creates a new user/team/role handler. contacts may be nil; the
// GDPR data export then returns an empty contact list instead of failing.
func NewHandler(repo Repository, contacts ...ContactLister) *Handler {
	h := &Handler{repo: repo}
	if len(contacts) > 0 {
		h.contacts = contacts[0]
	}
	return h
}

// WithPrivacySources attaches the ticket and assignment repositories so the
// GDPR data export covers the full processing footprint (Art. 15) instead of
// only the account and contact records.
func (h *Handler) WithPrivacySources(tickets TicketActivityLister, assignments AssignmentLister) *Handler {
	h.tickets = tickets
	h.assignments = assignments
	return h
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
	// Privacy (GDPR Art. 15 / 17)
	r.Get("/api/v1/users/{id}/data-export", h.ExportData)
	r.Post("/api/v1/users/{id}/anonymize", h.Anonymize)

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

	items, total, err := h.repo.ListUsers(r.Context(), t.OrganizationID, search, page)
	if err != nil {
		api.WriteRepoError(w, err)
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
	item, err := h.repo.GetUser(r.Context(), t.OrganizationID, id)
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

	if err := h.repo.CreateUser(r.Context(), u); err != nil {
		api.WriteRepoError(w, err)
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

	item, err := h.repo.UpdateUser(r.Context(), t.OrganizationID, id, req)
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
	if err := h.repo.DeleteUser(r.Context(), t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "user not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ExportData handles GET /api/v1/users/{id}/data-export (GDPR Art. 15).
// Returns the full processing footprint of the user: the account record,
// every contact record carrying the user's e-mail address, the user's custom
// role assignments, and the tickets and assignments the user reported,
// received or issued.
func (h *Handler) ExportData(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	u, err := h.repo.GetUser(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "user not found")
		return
	}

	contacts := []contact.Contact{}
	if h.contacts != nil && u.Email != "" {
		contacts, err = h.contacts.ListByEmail(r.Context(), t.OrganizationID, u.Email)
		if err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}

	roles, err := h.repo.ListUserRoles(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if roles == nil {
		roles = []UserRoleAssignment{}
	}

	all := api.PaginationParams{Limit: 1000}

	reported := []ticket.Ticket{}
	assignedTickets := []ticket.Ticket{}
	if h.tickets != nil {
		if reported, _, err = h.tickets.List(r.Context(), t.OrganizationID, ticket.FilterParams{ReporterID: id}, all); err != nil {
			api.WriteRepoError(w, err)
			return
		}
		if assignedTickets, _, err = h.tickets.List(r.Context(), t.OrganizationID, ticket.FilterParams{AssigneeID: id}, all); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}

	received := []assignment.Assignment{}
	issued := []assignment.Assignment{}
	if h.assignments != nil {
		if received, _, err = h.assignments.List(r.Context(), t.OrganizationID, assignment.FilterParams{AssignedTo: id}, all); err != nil {
			api.WriteRepoError(w, err)
			return
		}
		if issued, _, err = h.assignments.List(r.Context(), t.OrganizationID, assignment.FilterParams{AssignedBy: id}, all); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}

	api.WriteJSON(w, http.StatusOK, map[string]any{
		"user":        u,
		"contacts":    contacts,
		"roles":       roles,
		"tickets":     map[string]any{"reported": reported, "assigned": assignedTickets},
		"assignments": map[string]any{"received": received, "issued": issued},
		"exported_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// Anonymize handles POST /api/v1/users/{id}/anonymize (GDPR Art. 17).
// Personal data is replaced by surrogate values and the account is
// deactivated; the row stays so referential integrity and audit survive.
func (h *Handler) Anonymize(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	// Self-anonymization would lock the operator out mid-request and orphan
	// the session; require a second account to do it.
	if id == t.UserID {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "you cannot anonymize your own account")
		return
	}
	u, err := h.repo.AnonymizeUser(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "user not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, u)
}

func (h *Handler) ListUserRoles(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	userID := chi.URLParam(r, "id")
	roles, err := h.repo.ListUserRoles(r.Context(), t.OrganizationID, userID)
	if err != nil {
		api.WriteRepoError(w, err)
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

	items, total, err := h.repo.ListTeams(r.Context(), t.OrganizationID, search, page)
	if err != nil {
		api.WriteRepoError(w, err)
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
	item, err := h.repo.GetTeam(r.Context(), t.OrganizationID, id)
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

	if err := h.repo.CreateTeam(r.Context(), team); err != nil {
		api.WriteRepoError(w, err)
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

	item, err := h.repo.UpdateTeam(r.Context(), t.OrganizationID, id, req)
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
	if err := h.repo.DeleteTeam(r.Context(), t.OrganizationID, id); err != nil {
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
	members, err := h.repo.ListTeamMembers(r.Context(), t.OrganizationID, teamID)
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

	if err := h.repo.AddTeamMember(r.Context(), t.OrganizationID, member); err != nil {
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

	if err := h.repo.RemoveTeamMember(r.Context(), t.OrganizationID, teamID, userID); err != nil {
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
	items, total, err := h.repo.ListRoles(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteRepoError(w, err)
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
	item, err := h.repo.GetRole(r.Context(), t.OrganizationID, id)
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

	if err := h.repo.CreateRole(r.Context(), role); err != nil {
		api.WriteRepoError(w, err)
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

	item, err := h.repo.UpdateRole(r.Context(), t.OrganizationID, id, req)
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
	if err := h.repo.DeleteRole(r.Context(), t.OrganizationID, id); err != nil {
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

	if req.UserID == "" || (req.CustomRoleID == "" && req.RoleID == "") {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "user_id and one of custom_role_id or role_id are required")
		return
	}
	if req.CustomRoleID != "" && req.RoleID != "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "custom_role_id and role_id are mutually exclusive")
		return
	}

	scopeType := req.ScopeType
	if scopeType == "" {
		scopeType = "organization"
	}

	assignment := &UserRoleAssignment{
		UserID:       req.UserID,
		CustomRoleID: req.CustomRoleID,
		RoleID:       req.RoleID,
		ScopeType:    scopeType,
		ScopeID:      req.ScopeID,
		GrantedBy:    t.UserID,
	}

	if err := h.repo.AssignRole(r.Context(), t.OrganizationID, assignment); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, assignment)
}
