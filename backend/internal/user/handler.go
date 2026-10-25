package user

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
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

// SessionRevoker ends every session of a user issued up to the given time
// (implemented by identity.SessionRevocations).
type SessionRevoker interface {
	RevokeUser(ctx context.Context, userID string, at time.Time) error
}

// Handler provides HTTP handlers for user, team, and role endpoints.
type Handler struct {
	repo        Repository
	contacts    ContactLister
	tickets     TicketActivityLister
	assignments AssignmentLister
	sessions    SessionRevoker
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

// WithSessionRevoker makes deactivation and anonymization end the sessions
// and refresh tokens of the user immediately (TLC-04, AUT-02).
func (h *Handler) WithSessionRevoker(sessions SessionRevoker) *Handler {
	h.sessions = sessions
	return h
}

// revokeSessions blacklists the sessions of the user.
func (h *Handler) revokeSessions(ctx context.Context, userID string) error {
	if h.sessions == nil {
		return nil
	}
	return h.sessions.RevokeUser(ctx, userID, time.Now().UTC())
}

// sessionRevocation blacklists the sessions of the user as the
// DeactivationHook, inside the deactivation transaction once the user was
// found in the organization: a failure rolls the deactivation back, and a
// request for a user of another organization writes nothing. err keeps the
// failure so the handler can answer 503 instead of 404.
type sessionRevocation struct {
	h      *Handler
	userID string
	err    error
}

func (s *sessionRevocation) hook(ctx context.Context, _ pgx.Tx) error {
	s.err = s.h.revokeSessions(ctx, s.userID)
	return s.err
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
	// role-assignments is the catalog path (API-05); roles/assign and
	// users/{id}/roles stay until their sunset (API-01).
	r.Get("/api/v1/role-assignments", h.ListRoleAssignments)
	r.Post("/api/v1/role-assignments", h.AssignRole)
	r.Delete("/api/v1/role-assignments/{id}", h.RevokeRoleAssignment)
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

	deactivate := req.Status != nil && !statusIsActive(*req.Status)
	revocation := &sessionRevocation{h: h, userID: id}
	item, err := h.repo.UpdateUserTx(r.Context(), t.OrganizationID, id, req, revocation.hook)
	if revocation.err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "sessions could not be revoked; the user was not deactivated")
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "user not found")
		return
	}
	// Second revocation after the commit: covers tokens refreshed between
	// the first one and the commit of the status change.
	if deactivate {
		if err := h.revokeSessions(r.Context(), id); err != nil {
			slog.Warn("second session revocation after deactivation failed", "user_id", id, "error", err)
		}
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
		switch {
		case errors.Is(err, ErrUserOwnsOpenObjects), errors.Is(err, ErrUserReferenced):
			api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		default:
			api.WriteError(w, http.StatusNotFound, "Not Found", "user not found")
		}
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
	revocation := &sessionRevocation{h: h, userID: id}
	u, err := h.repo.AnonymizeUserTx(r.Context(), t.OrganizationID, id, revocation.hook)
	if revocation.err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "sessions could not be revoked; the user was not anonymized")
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "user not found")
		return
	}
	if err := h.revokeSessions(r.Context(), id); err != nil {
		slog.Warn("second session revocation after anonymization failed", "user_id", id, "error", err)
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

// ListRoleAssignments handles GET /api/v1/role-assignments?user_id=….
func (h *Handler) ListRoleAssignments(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "user_id", Message: "is required"})
		return
	}
	roles, err := h.repo.ListUserRoles(r.Context(), t.OrganizationID, userID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.CompleteList(roles))
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

	id, ok := api.ParseUUIDParam(w, r, "id", "team not found")
	if !ok {
		return
	}
	item, err := h.repo.GetTeam(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeTeamError(w, r, "team not found", err)
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
		writeTeamError(w, r, "team not found", err)
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

	id, ok := api.ParseUUIDParam(w, r, "id", "team not found")
	if !ok {
		return
	}
	var req UpdateTeamRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	item, err := h.repo.UpdateTeam(r.Context(), t.OrganizationID, id, req)
	if err != nil {
		writeTeamError(w, r, "team not found", err)
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

	id, ok := api.ParseUUIDParam(w, r, "id", "team not found")
	if !ok {
		return
	}
	if err := h.repo.DeleteTeam(r.Context(), t.OrganizationID, id); err != nil {
		writeTeamError(w, r, "team not found", err)
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

	teamID, ok := api.ParseUUIDParam(w, r, "id", "team not found")
	if !ok {
		return
	}
	members, err := h.repo.ListTeamMembers(r.Context(), t.OrganizationID, teamID)
	if err != nil {
		writeTeamError(w, r, "team not found", err)
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

	teamID, ok := api.ParseUUIDParam(w, r, "id", "team not found")
	if !ok {
		return
	}
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
		writeTeamError(w, r, "team not found", err)
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

	teamID, ok := api.ParseUUIDParam(w, r, "teamId", "team not found")
	if !ok {
		return
	}
	userID, ok := api.ParseUUIDParam(w, r, "userId", "team member not found")
	if !ok {
		return
	}

	if err := h.repo.RemoveTeamMember(r.Context(), t.OrganizationID, teamID, userID); err != nil {
		writeTeamError(w, r, "team not found", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// writeTeamError answers a failed team, team member or role request: an
// object that is unknown, of another organization or outside the caller's
// team scope is 404 with notFound (a member: "team member not found"), a
// duplicate member 409, a system role 404, a user or lead outside the
// organization the 422 field error (TEN-04). Database errors go through
// api.WriteRepoError (23505 is 409 without the constraint name, F-047);
// their text goes to the log only.
func writeTeamError(w http.ResponseWriter, r *http.Request, notFound string, err error) {
	switch {
	case errors.Is(err, errMemberNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "team member not found")
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", notFound)
	case errors.Is(err, ErrConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrConflict.Error())
	case errors.Is(err, ErrSystemRole):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrSystemRole.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "team or role request failed", "error", err)
		api.WriteRepoError(w, err)
	}
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

	id, ok := api.ParseUUIDParam(w, r, "id", "role not found")
	if !ok {
		return
	}
	item, err := h.repo.GetRole(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeTeamError(w, r, "role not found", err)
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

	id, ok := api.ParseUUIDParam(w, r, "id", "role not found")
	if !ok {
		return
	}
	var req UpdateRoleRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "changing a role requires an authenticated principal")
		return
	}
	item, err := h.repo.UpdateRole(r.Context(), t.OrganizationID, id, &p, req)
	var widening *RoleWideningError
	if errors.As(err, &widening) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", widening.Error())
		return
	}
	if err != nil {
		writeTeamError(w, r, "role not found", err)
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

	id, ok := api.ParseUUIDParam(w, r, "id", "role not found")
	if !ok {
		return
	}
	if err := h.repo.DeleteRole(r.Context(), t.OrganizationID, id); err != nil {
		writeTeamError(w, r, "role not found", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AssignRole handles POST /api/v1/role-assignments and the sunset path POST
// /api/v1/roles/assign. The caller must hold role:manage and every permission
// of the role in a scope covering the assignment's scope (grant ceiling, 403);
// the check runs before anything is stored and before the MFA binding of an
// org_admin grant.
func (h *Handler) AssignRole(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "assigning roles requires an authenticated principal")
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

	assignment := &UserRoleAssignment{
		UserID:       req.UserID,
		CustomRoleID: req.CustomRoleID,
		RoleID:       req.RoleID,
		ScopeType:    req.ScopeType,
		ScopeID:      req.ScopeID,
		GrantedBy:    t.UserID,
	}

	if err := h.repo.AssignRole(r.Context(), t.OrganizationID, &p, assignment); err != nil {
		writeAssignmentError(w, r, "user_id", req.UserID, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, assignment)
}

// RevokeRoleAssignment handles DELETE /api/v1/role-assignments/{id} (API-05,
// AUT-02, F-017): it removes a standard or custom role assignment of the
// organization. The caller must hold role:manage and every permission of the
// role in a scope covering the assignment's scope, as for assigning it (grant
// ceiling, 403); an unknown id or one of another organization is 404. The
// user loses the rights at the next refresh.
func (h *Handler) RevokeRoleAssignment(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "revoking roles requires an authenticated principal")
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.repo.RevokeRoleAssignment(r.Context(), t.OrganizationID, &p, id); err != nil {
		writeAssignmentError(w, r, "assignment_id", id, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAssignmentError answers a refused role assignment or revocation: 403
// beyond the grant ceiling, 404 for a role, user, team or assignment outside
// the organization, 422 for an invalid scope, 503 when an org_admin cannot
// be bound to MFA. Other failures are sanitized (api.WriteRepoError);
// repository and database texts go to the log only, with the attribute key
// and value naming the request's user or assignment.
func writeAssignmentError(w http.ResponseWriter, r *http.Request, key, value string, err error) {
	var ceiling *GrantCeilingError
	switch {
	case errors.As(err, &ceiling):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ceiling.Error())
	case errors.Is(err, errRoleNotFound), errors.Is(err, errUserNotFound), errors.Is(err, errTeamNotFound),
		errors.Is(err, errAssignmentNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
	case errors.Is(err, ErrMFAUnavailable):
		// No org_admin without MFA (WP-358); the cause names the
		// admin API and goes to the log only.
		slog.ErrorContext(r.Context(), "org_admin assignment without enforceable MFA", key, value, "error", err)
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", ErrMFAUnavailable.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.ErrorContext(r.Context(), "role assignment failed", key, value, "error", err)
		api.WriteRepoError(w, err)
	}
}
