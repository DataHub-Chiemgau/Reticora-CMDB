package ticket

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// SLAHooks is implemented by the SLA package to keep ticket clocks in sync.
type SLAHooks interface {
	ApplyForTicket(ctx context.Context, orgID string, t *Ticket) error
	MarkFirstResponse(ctx context.Context, orgID, ticketID string, at time.Time) error
	MarkResolved(ctx context.Context, orgID, ticketID string, at time.Time) error
}

// AttachmentReader reads the documents linked to an object
// (document.Repository): the attachments of a ticket are its documents
// linked with entity_type ticket (WP-765).
type AttachmentReader interface {
	GetLinksForEntity(ctx context.Context, orgID, entityType, entityID string, page api.PaginationParams) ([]document.Document, int, error)
}

// Handler provides HTTP handlers for ticket endpoints.
type Handler struct {
	svc  *Service
	repo Repository
	sla  SLAHooks
	docs AttachmentReader
}

// NewHandler creates a new ticket handler; tickets and comments are created
// through svc only (WP-766), reads and changes go to its repository.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc, repo: svc.repo, sla: svc.sla}
}

// WithDocuments gives the handler the document repository the attachments
// of a ticket are read from (WP-765); without it the attachments answer 503.
func (h *Handler) WithDocuments(docs AttachmentReader) *Handler {
	h.docs = docs
	return h
}

// RegisterRoutes registers ticket routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/tickets", h.List)
	r.Post("/api/v1/tickets", h.Create)
	r.Get("/api/v1/tickets/{id}", h.Get)
	r.Patch("/api/v1/tickets/{id}", h.Update)
	r.Delete("/api/v1/tickets/{id}", h.Delete)
	r.Post("/api/v1/tickets/{id}/comments", h.AddComment)
	r.Get("/api/v1/tickets/{id}/comments", h.ListComments)
	r.Get("/api/v1/tickets/{id}/links", h.ListLinks)
	r.Post("/api/v1/tickets/{id}/links", h.CreateLink)
	r.Delete("/api/v1/tickets/{id}/links/{link_id}", h.DeleteLink)
	r.Get("/api/v1/tickets/{id}/attachments", h.ListAttachments)
}

// parseTagFilter splits the filter tags (comma separated) into keys.
func parseTagFilter(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return NormalizeTags(strings.Split(raw, ","))
}

// List handles GET /api/v1/tickets
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	filter := FilterParams{
		Status:     r.URL.Query().Get("status"),
		Priority:   r.URL.Query().Get("priority"),
		Category:   r.URL.Query().Get("category"),
		AssigneeID: r.URL.Query().Get("assignee_id"),
		TeamID:     r.URL.Query().Get("team_id"),
		Tags:       parseTagFilter(r.URL.Query().Get("tags")),
		Search:     r.URL.Query().Get("search"),
		SortBy:     r.URL.Query().Get("sort_by"),
		SortDir:    r.URL.Query().Get("sort_dir"),
	}
	if filter.TeamID == TeamMine {
		// The teams of the principal (E-143); without a user no team.
		filter.TeamID, filter.TeamMemberOf = "", t.UserID
		if !api.IsUUID(t.UserID) {
			filter.TeamMemberOf = noUser
		}
	}

	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if filterErr := permission.CheckListFilter(policy, permission.KindTicket, nil, listFieldPaths(&filter)); filterErr != nil {
		api.WriteFieldError(w, filterErr)
		return
	}

	items, total, err := h.repo.List(r.Context(), t.OrganizationID, &filter, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	hasMore := page.Offset+page.Limit < total
	if page.Cursor != nil {
		hasMore = len(items) == page.Limit
	}
	next := NextCursor(items, &filter, page.Limit)
	if !permission.NeedsProjection(policy, permission.KindTicket) {
		api.WriteJSON(w, http.StatusOK, api.ListResponse[Ticket]{
			Data:       items,
			Total:      total,
			Limit:      page.Limit,
			Offset:     page.Offset,
			HasMore:    hasMore,
			NextCursor: next,
		})
		return
	}
	data := make([]any, len(items))
	for i := range items {
		if data[i], err = project(policy, &items[i]); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[any]{Data: data, Total: total, Limit: page.Limit,
		Offset: page.Offset, HasMore: hasMore, NextCursor: next})
}

// listFieldPaths are the DTO paths a ticket list reads besides the
// structural members: its filters, the search (title and description) and
// a sort on a field (WP-765).
func listFieldPaths(filter *FilterParams) []string {
	var paths []string
	for path, set := range map[string]bool{
		"status": filter.Status != "", "priority": filter.Priority != "", "category": filter.Category != "",
		"assignee_id": filter.AssigneeID != "", "reporter_id": filter.ReporterID != "",
		"team_id": filter.TeamID != "" || filter.TeamMemberOf != "",
		"tags":    len(filter.Tags) > 0, "description": filter.Search != "",
	} {
		if set {
			paths = append(paths, path)
		}
	}
	if column, _ := NormalizeSort(filter); column != "" {
		paths = append(paths, column)
	}
	return paths
}

// ticketRef is the object reference of a ticket for its field permissions:
// the ticket with its client, site and team.
func ticketRef(t *Ticket) permission.ObjectRef {
	return permission.ObjectRef{ID: t.ID, ClientID: t.ClientID, SiteID: t.SiteID, TeamID: t.TeamID}
}

// project returns the ticket as the field policy lets the principal read
// it (RBA-07, WP-765): the ticket itself without a restriction of the
// kind ticket.
func project(policy *permission.FieldPolicy, t *Ticket) (any, error) {
	return permission.ProjectValue(policy, permission.KindTicket, "", ticketRef(t), t)
}

// writeTicket answers the ticket projected for the principal of the
// request; a policy that cannot be resolved is 500 (fail-closed).
func writeTicket(w http.ResponseWriter, r *http.Request, status int, t *Ticket) {
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	body, err := project(policy, t)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, status, body)
}

// Get handles GET /api/v1/tickets/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeTicketError(w, r, err)
		return
	}

	writeTicket(w, r, http.StatusOK, item)
}

// TeamMine is the value of the filter team_id for the tickets of every
// team the principal is a member of (E-143).
const TeamMine = "mine"

// noUser is a user id no team member has: team_id=mine of a principal
// without user lists nothing.
const noUser = "00000000-0000-0000-0000-000000000000"

// validateEnums answers 422 when status or priority (nil: not given) is no
// value of the state machine or the priority list, before the repository,
// so PostgreSQL and the memory profile answer alike.
func validateEnums(w http.ResponseWriter, status, priority *string) bool {
	if status != nil && !ValidStatus(*status) {
		api.WriteFieldError(w, &api.FieldError{Field: "status", Message: "must be one of new, open, in_progress, waiting, resolved, closed", Status: http.StatusUnprocessableEntity})
		return false
	}
	if priority != nil && !ValidPriority(*priority) {
		api.WriteFieldError(w, &api.FieldError{Field: "priority", Message: "must be one of low, medium, high, critical", Status: http.StatusUnprocessableEntity})
		return false
	}
	return true
}

// writeTicketError answers a failed ticket request: a ticket that is
// unknown, of another organization or outside the caller's scope is 404
// (TEN-04), a status change the state machine refuses 409, an assignee,
// team, CI or asset that is no UUID or not visible 422 on its field. Database errors go through api.WriteRepoError; no table,
// constraint, policy or SQLSTATE text reaches the client (API-03, F-047),
// the log keeps it.
func writeTicketError(w http.ResponseWriter, r *http.Request, err error) {
	var open *ErrOpenTicketExists
	switch {
	case errors.As(err, &open):
		api.WriteError(w, http.StatusConflict, "Conflict", "an open ticket is already linked to the object")
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrNotFound.Error())
	case errors.Is(err, ErrInvalidTransition):
		detail := ErrInvalidTransition.Error()
		var te *TransitionError
		if errors.As(err, &te) && ValidStatus(te.To) && (te.From == "" || ValidStatus(te.From)) {
			detail = te.Error()
		}
		api.WriteError(w, http.StatusConflict, "Conflict", detail)
	case errors.Is(err, ErrLinkExists):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrLinkExists.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "ticket request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// Create handles POST /api/v1/tickets
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.Title == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "title is required")
		return
	}

	priority := req.Priority
	if priority == "" {
		priority = DefaultPriority
	}
	if !validateEnums(w, nil, &priority) {
		return
	}

	// The service applies the defaults (category incident, status new).
	ticket := &Ticket{
		OrganizationID: t.OrganizationID,
		Title:          req.Title,
		Description:    req.Description,
		Status:         StatusNew,
		Priority:       priority,
		Category:       req.Category,
		ReporterID:     t.UserID,
		AssigneeID:     req.AssigneeID,
		TeamID:         req.TeamID,
		RelatedCIID:    req.RelatedCIID,
		RelatedAssetID: req.RelatedAssetID,
		DueDate:        req.DueDate,
		Tags:           req.Tags,
	}
	// Every field the request sets must be editable (WP-765); the server
	// defaults of priority and category are no field of the request.
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if writeErr := permission.CheckWrite(policy, permission.KindTicket, "", permission.ObjectRef{TeamID: req.TeamID},
		req.sentFields()); writeErr != nil {
		api.WriteFieldError(w, writeErr)
		return
	}

	if err := h.svc.Create(r.Context(), ticket, nil, CreateOptions{}); err != nil {
		writeTicketError(w, r, err)
		return
	}

	writeTicket(w, r, http.StatusCreated, ticket)
}

// Update handles PATCH /api/v1/tickets/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if !validateEnums(w, req.Status, req.Priority) {
		return
	}
	if !h.checkUpdate(w, r, t.OrganizationID, id, &req) {
		return
	}

	item, err := h.repo.Update(r.Context(), t.OrganizationID, id, &req)
	if err != nil {
		writeTicketError(w, r, err)
		return
	}
	// SLA hooks: new→open is no first response, a reopen (to open) calls
	// none; resetting the resolution clock after a reopen is WP-292.
	if h.sla != nil && req.Status != nil {
		now := time.Now().UTC()
		switch *req.Status {
		case "in_progress", "waiting":
			_ = h.sla.MarkFirstResponse(r.Context(), t.OrganizationID, id, now)
		case "resolved", "closed":
			_ = h.sla.MarkFirstResponse(r.Context(), t.OrganizationID, id, now)
			_ = h.sla.MarkResolved(r.Context(), t.OrganizationID, id, now)
		}
	}

	writeTicket(w, r, http.StatusOK, item)
}

// checkUpdate checks a PATCH against the field permissions of the kind
// ticket (RBA-07, WP-765) and answers 403 with the fields: every sent
// field the principal may not read counts, whatever its value (no
// equality oracle), and every readable field whose value changes; each
// must be editable. Without a restriction of the kind it reads nothing.
// A ticket outside the scope is 404.
func (h *Handler) checkUpdate(w http.ResponseWriter, r *http.Request, orgID, id string, req *UpdateRequest) bool {
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return false
	}
	if !policy.Restricted(permission.KindTicket) {
		return true
	}
	current, err := h.repo.GetByID(r.Context(), orgID, id)
	if err != nil {
		writeTicketError(w, r, err)
		return false
	}
	ref := ticketRef(current)
	var changed []string
	for path, differs := range req.changes(current) {
		if differs || !permission.Readable(policy, permission.KindTicket, "", ref, path) {
			changed = append(changed, path)
		}
	}
	if writeErr := permission.CheckWrite(policy, permission.KindTicket, "", ref, changed); writeErr != nil {
		api.WriteFieldError(w, writeErr)
		return false
	}
	return true
}

// sentFields are the DTO paths a create request sets (WP-765).
func (req *CreateRequest) sentFields() []string {
	var paths []string
	for path, value := range map[string]string{
		"description": req.Description, "priority": req.Priority, "category": req.Category,
		"assignee_id": req.AssigneeID, "team_id": req.TeamID, "related_ci_id": req.RelatedCIID,
		"related_asset_id": req.RelatedAssetID, "due_date": req.DueDate,
	} {
		if value != "" {
			paths = append(paths, path)
		}
	}
	if len(NormalizeTags(req.Tags)) > 0 {
		paths = append(paths, "tags")
	}
	return paths
}

// changes maps each DTO path a PATCH sends to whether its value differs
// from the current ticket: ids compare case-insensitively, due_date as a
// point in time, tags as the normalized set.
func (req *UpdateRequest) changes(current *Ticket) map[string]bool {
	out := map[string]bool{}
	for _, f := range []struct {
		path      string
		value     *string
		current   string
		foldCase  bool
		timestamp bool
	}{
		{path: "title", value: req.Title, current: current.Title},
		{path: "description", value: req.Description, current: current.Description},
		{path: "status", value: req.Status, current: current.Status},
		{path: "priority", value: req.Priority, current: current.Priority},
		{path: "category", value: req.Category, current: current.Category},
		{path: "assignee_id", value: req.AssigneeID, current: current.AssigneeID, foldCase: true},
		{path: "team_id", value: req.TeamID, current: current.TeamID, foldCase: true},
		{path: "related_ci_id", value: req.RelatedCIID, current: current.RelatedCIID, foldCase: true},
		{path: "related_asset_id", value: req.RelatedAssetID, current: current.RelatedAssetID, foldCase: true},
		{path: "due_date", value: req.DueDate, current: current.DueDate, timestamp: true},
	} {
		if f.value == nil {
			continue
		}
		next := strings.TrimSpace(*f.value)
		switch {
		case f.foldCase:
			out[f.path] = !strings.EqualFold(next, f.current)
		case f.timestamp:
			out[f.path] = !sameInstant(next, f.current)
		default:
			out[f.path] = *f.value != f.current
		}
	}
	if req.Tags != nil {
		out["tags"] = !slices.Equal(NormalizeTags(req.Tags), NormalizeTags(current.Tags))
	}
	return out
}

// sameInstant reports whether two due dates (RFC 3339, empty: none) name
// the same point in time; values that do not parse compare as text.
func sameInstant(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ta.Equal(tb)
}

// Delete handles DELETE /api/v1/tickets/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id); err != nil {
		writeTicketError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AddComment handles POST /api/v1/tickets/{id}/comments
func (h *Handler) AddComment(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	// The ticket id reaches SQL only as a UUID (F-047).
	ticketID, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req CommentRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.Content == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "content is required")
		return
	}

	comment := &Comment{
		OrganizationID: t.OrganizationID,
		TicketID:       ticketID,
		AuthorID:       t.UserID,
		AuthorType:     AuthorUser,
		Content:        req.Content,
		IsInternal:     req.IsInternal,
	}

	if err := h.svc.AddComment(r.Context(), comment); err != nil {
		writeTicketError(w, r, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, comment)
}

// ListComments handles GET /api/v1/tickets/{id}/comments
func (h *Handler) ListComments(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	ticketID, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	page := api.ParsePagination(r)

	comments, total, err := h.repo.ListComments(r.Context(), t.OrganizationID, ticketID, page)
	if err != nil {
		writeTicketError(w, r, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Comment]{
		Data:    comments,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// ListLinks handles GET /api/v1/tickets/{id}/links (WP-764): the links in
// the order created_at, id, paged with limit and the keyset cursor.
func (h *Handler) ListLinks(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	ticketID, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	links, total, err := h.repo.ListLinks(r.Context(), t.OrganizationID, ticketID, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", api.ErrInvalidCursor.Error())
			return
		}
		writeTicketError(w, r, err)
		return
	}
	next := LinkNextCursor(links, page.Limit)
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Link]{
		Data:       links,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    next != "",
		NextCursor: next,
	})
}

// CreateLink handles POST /api/v1/tickets/{id}/links (WP-764): an unknown
// entity_type is 422, a ticket or target that does not exist or lies
// outside the caller's scope 404, a second link of the same object 409.
func (h *Handler) CreateLink(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	ticketID, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req LinkRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.EntityType == "" || req.EntityID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "entity_type and entity_id are required")
		return
	}
	if !ValidLinkEntityType(req.EntityType) {
		api.WriteFieldError(w, &api.FieldError{Field: "entity_type", Status: http.StatusUnprocessableEntity,
			Message: "must be one of " + strings.Join(LinkEntityTypes, ", ")})
		return
	}
	if !api.IsUUID(req.EntityID) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "linked object not found")
		return
	}
	link := &Link{OrganizationID: t.OrganizationID, TicketID: ticketID, EntityType: req.EntityType, EntityID: req.EntityID}
	if err := h.repo.CreateLink(r.Context(), link); err != nil {
		writeTicketError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, link)
}

// DeleteLink handles DELETE /api/v1/tickets/{id}/links/{link_id} (WP-764).
func (h *Handler) DeleteLink(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	ticketID, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	linkID, ok := api.ParseUUIDParam(w, r, "link_id", "ticket link not found")
	if !ok {
		return
	}
	if err := h.repo.DeleteLink(r.Context(), t.OrganizationID, ticketID, linkID); err != nil {
		writeTicketError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// attachmentEntityType is the entity_type of the document links of a ticket
// (document_link, links.go of the document package).
const attachmentEntityType = "ticket"

// ListAttachments handles GET /api/v1/tickets/{id}/attachments (TKT-01,
// WP-765): the documents linked to the ticket (document_link with
// entity_type ticket), newest first on (created_at, id), paged with limit
// (at most 200) and the keyset cursor; a forged cursor is 400. A ticket
// that does not exist or lies outside the caller's scope is 404; of the
// linked documents the caller sees those whose link it sees (RLS of
// document and document_link). Upload, scan and linking stay with
// /api/v1/documents.
func (h *Handler) ListAttachments(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	ticketID, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	if _, err := h.repo.GetByID(r.Context(), t.OrganizationID, ticketID); err != nil {
		writeTicketError(w, r, err)
		return
	}
	if h.docs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "ticket attachments are not available")
		return
	}
	docs, total, err := h.docs.GetLinksForEntity(r.Context(), t.OrganizationID, attachmentEntityType, ticketID, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", api.ErrInvalidCursor.Error())
			return
		}
		writeTicketError(w, r, err)
		return
	}
	if docs == nil {
		docs = []document.Document{}
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(docs, total, page, func(d *document.Document) (time.Time, string) {
		return d.CreatedAt, d.ID
	}))
}
