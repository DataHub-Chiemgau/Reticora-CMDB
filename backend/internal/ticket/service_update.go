package ticket

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Update changes ticket id (TKT-01, WP-293): PATCH /api/v1/tickets/{id} and
// the ticket providers go through it. A status that is no value of the
// state machine and a priority that is no value of the list are 422 field
// errors; the repository checks the transition (ErrInvalidTransition) and
// writes ticket.updated (WP-290). The SLA hooks follow the change: a change
// to in_progress or waiting is the first response, resolved and closed
// additionally the resolution, a change to open resets the resolution
// clock (reopen, WP-769), and a new priority or object chooses the SLA
// again (WP-769). new→open is no first response. Each hook runs under a
// savepoint of a context transaction; a failed hook leaves the change in
// place.
func (s *Service) Update(ctx context.Context, orgID, id string, req *UpdateRequest) (*Ticket, error) {
	if req.Status != nil && !ValidStatus(*req.Status) {
		return nil, &api.FieldError{Field: "status", Message: "must be one of new, open, in_progress, waiting, resolved, closed", Status: http.StatusUnprocessableEntity}
	}
	if req.Priority != nil && !ValidPriority(*req.Priority) {
		return nil, &api.FieldError{Field: "priority", Message: "must be one of low, medium, high, critical", Status: http.StatusUnprocessableEntity}
	}
	reapply, err := s.slaReapplyNeeded(ctx, orgID, id, req)
	if err != nil {
		return nil, err
	}
	item, err := s.repo.Update(ctx, orgID, id, req)
	if err != nil {
		return nil, err
	}
	if s.sla == nil {
		return item, nil
	}
	now := time.Now().UTC()
	hook := func(name string, fn func(ctx context.Context) error) {
		if hookErr := database.Savepoint(ctx, fn); hookErr != nil && name != "" {
			slog.WarnContext(ctx, name, "ticket_id", id, "error", hookErr)
		}
	}
	if req.Status != nil {
		switch *req.Status {
		case StatusInProgress, StatusWaiting:
			hook("", func(ctx context.Context) error { return s.sla.MarkFirstResponse(ctx, orgID, id, now) })
		case StatusResolved, StatusClosed:
			hook("", func(ctx context.Context) error { return s.sla.MarkFirstResponse(ctx, orgID, id, now) })
			hook("", func(ctx context.Context) error { return s.sla.MarkResolved(ctx, orgID, id, now) })
		case StatusOpen:
			hook("ticket reopen: reset sla", func(ctx context.Context) error { return s.sla.MarkReopened(ctx, orgID, id, now) })
		}
	}
	if reapply {
		hook("ticket update: choose sla again", func(ctx context.Context) error { return s.sla.ReapplyForTicket(ctx, orgID, item) })
	}
	return item, nil
}

// slaReapplyNeeded reports whether the change sets another priority or
// object (related_ci_id, related_asset_id) of the ticket, so that its SLA
// is chosen again after the update (WP-769). Only then it reads the
// ticket; a ticket outside the scope is ErrNotFound.
func (s *Service) slaReapplyNeeded(ctx context.Context, orgID, id string, req *UpdateRequest) (bool, error) {
	if s.sla == nil || req.Priority == nil && req.RelatedCIID == nil && req.RelatedAssetID == nil {
		return false, nil
	}
	current, err := s.repo.GetByID(ctx, orgID, id)
	if err != nil {
		return false, err
	}
	changes := req.changes(current)
	return changes["priority"] || changes["related_ci_id"] || changes["related_asset_id"], nil
}

// CreateLink links the object of link to its ticket (ticket_link, WP-764):
// an unknown entity type is a 422 field error, a ticket or target that does
// not exist or lies outside the caller's scope ErrNotFound, a second link
// of the same object ErrLinkExists.
func (s *Service) CreateLink(ctx context.Context, link *Link) error {
	if !ValidLinkEntityType(link.EntityType) {
		return &api.FieldError{Field: "entity_type", Status: http.StatusUnprocessableEntity,
			Message: "must be one of " + strings.Join(LinkEntityTypes, ", ")}
	}
	if !api.IsUUID(link.TicketID) {
		return ErrNotFound
	}
	if !api.IsUUID(link.EntityID) {
		return ErrLinkTargetNotFound
	}
	return s.repo.CreateLink(ctx, link)
}
