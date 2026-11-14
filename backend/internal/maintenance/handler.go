package maintenance

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for the maintenance window module.
type Handler struct {
	repo Repository
	// finder computes the recipients over the configuration graph
	// (WP-1034); without it a notification reaches the clients of the
	// window's own CIs and the impact preview answers 501.
	finder *RecipientFinder
}

// NewHandler creates a new maintenance handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithAnalyzer lets the handler find the recipients of a window over the
// graph impact of its CIs (topology.Analyzer, MGT-04).
func (h *Handler) WithAnalyzer(analyzer ImpactAnalyzer) *Handler {
	if analyzer != nil {
		h.finder = NewRecipientFinder(h.repo, analyzer)
	}
	return h
}

// RegisterRoutes registers maintenance routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/maintenance-windows", h.List)
	r.Post("/api/v1/maintenance-windows", h.Create)
	r.Get("/api/v1/maintenance-windows/{id}", h.Get)
	r.Patch("/api/v1/maintenance-windows/{id}", h.Update)
	r.Delete("/api/v1/maintenance-windows/{id}", h.Delete)
	r.Get("/api/v1/maintenance-windows/{id}/impact", h.Impact)
	r.Post("/api/v1/maintenance-windows/{id}/notify", h.Notify)
	r.Get("/api/v1/maintenance-windows/{id}/notifications", h.ListNotifications)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, FilterParams{Status: r.URL.Query().Get("status")}, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Window]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CreateWindowRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "title is required")
		return
	}
	startsAt, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "starts_at must be RFC3339")
		return
	}
	endsAt, err := time.Parse(time.RFC3339, req.EndsAt)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "ends_at must be RFC3339")
		return
	}
	if !endsAt.After(startsAt) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "ends_at must be after starts_at")
		return
	}
	// A window without CIs is org-wide; a principal restricted in the client
	// or site scope may not create one (E-09).
	if scope, ok := database.TenantScopeFromContext(r.Context()); ok && len(req.CIIDs) == 0 &&
		(!scope.Clients.IsAll() || !scope.Sites.IsAll()) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "the operation is not permitted")
		return
	}
	win := &Window{
		OrganizationID: t.OrganizationID,
		Title:          req.Title,
		Description:    req.Description,
		StartsAt:       startsAt,
		EndsAt:         endsAt,
		Status:         "scheduled",
		CreatedBy:      t.UserID,
		CIIDs:          req.CIIDs,
	}
	if err := h.repo.Create(r.Context(), win); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, win)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateWindowRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Status != nil {
		switch *req.Status {
		case "scheduled", "in_progress", "completed", "cancelled":
		default:
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid status")
			return
		}
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// recipientOptions reads include_heuristic and categories of the query.
func recipientOptions(r *http.Request) (RecipientOptions, error) {
	q := r.URL.Query()
	opts := RecipientOptions{}
	if v := q.Get("include_heuristic"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return opts, &api.FieldError{Field: "include_heuristic", Message: "must be true or false", Status: http.StatusUnprocessableEntity}
		}
		opts.IncludeHeuristic = b
	}
	for _, c := range strings.Split(q.Get("categories"), ",") {
		if c = strings.TrimSpace(c); c != "" {
			opts.Categories = append(opts.Categories, c)
		}
	}
	if err := opts.Validate(); err != nil {
		return opts, err
	}
	return opts, nil
}

// Impact handles GET /api/v1/maintenance-windows/{id}/impact: the clients,
// sites and services the window affects over the graph (MGT-04, WP-1034),
// in the scope of the caller.
func (h *Handler) Impact(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.finder == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "the impact preview needs the configuration graph")
		return
	}
	opts, err := recipientOptions(r)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	preview, err := h.finder.Recipients(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), opts)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, preview)
}

// Notify handles POST /api/v1/maintenance-windows/{id}/notify: it finds the
// affected clients over the graph (include_heuristic, categories) and
// announces the window to each in one transaction (maintenance.scheduled);
// the notification chain delivers it to the client's contacts of
// contact_roles and its users with maintenance:read. The statuses follow
// the deliveries.
func (h *Handler) Notify(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req NotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	opts := RecipientOptions{IncludeHeuristic: req.IncludeHeuristic, Categories: req.Categories}
	if err := opts.Validate(); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	var plan *NotifyPlan
	if h.finder != nil {
		preview, err := h.finder.Recipients(r.Context(), t.OrganizationID, id, opts)
		if err != nil {
			api.WriteRepoError(w, err)
			return
		}
		plan = &NotifyPlan{Clients: preview.Clients, ContactRoles: req.ContactRoles}
	}
	notifications, err := h.repo.NotifyClients(r.Context(), t.OrganizationID, id, plan)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	// notified counts the notifications a channel delivered; queued those
	// still waiting for a delivery (pending) or failed.
	sent := 0
	for i := range notifications {
		if notifications[i].Status == NotificationSent {
			sent++
		}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"notified": sent, "queued": len(notifications) - sent, "notifications": api.NonNilSlice(notifications),
	})
}

// ListNotifications handles GET /api/v1/maintenance-windows/{id}/notifications.
func (h *Handler) ListNotifications(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	notifications, err := h.repo.ListNotifications(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": api.NonNilSlice(notifications)})
}
