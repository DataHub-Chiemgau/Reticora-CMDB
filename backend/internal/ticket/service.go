package ticket

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Default values of a new ticket (WP-290).
const (
	DefaultPriority = "medium"
	DefaultCategory = "incident"
)

// Target names an object a ticket is linked to: an entity type of
// ticket_link and the object's id (WP-764).
type Target struct {
	EntityType string
	EntityID   string
}

// CreateOptions are the options of Service.Create (WP-766).
type CreateOptions struct {
	// Exclusive: at most one ticket that is not closed is linked to the
	// target; a second creation fails with *ErrOpenTicketExists. The
	// ticket is linked to the target.
	Exclusive *Target
	// Origin: the ticket was created for this object (e.g. a review item);
	// one audit entry ticket.create with the origin replaces the route
	// audit.
	Origin *Target
}

// LinkedTicket is a ticket linked to an object: id, number and status.
type LinkedTicket struct {
	ID           string `json:"id"`
	TicketNumber int    `json:"ticket_number"`
	Status       string `json:"status"`
}

// ErrOpenTicketExists: a ticket that is not closed is already linked to the
// exclusive target of a creation (WP-766); errors.As yields its id and
// number.
type ErrOpenTicketExists struct {
	ID           string
	TicketNumber int
}

func (e *ErrOpenTicketExists) Error() string {
	return fmt.Sprintf("open ticket #%d (%s) already exists for the object", e.TicketNumber, e.ID)
}

// Service is the only way tickets and their comments are created (TKT-01,
// WP-766): POST /api/v1/tickets, POST /api/v1/tickets/{id}/comments and
// the tickets of review items. It applies the defaults of WP-290, creates
// a ticket with its links in one transaction and keeps the SLA clocks.
type Service struct {
	repo Repository
	sla  SLAHooks
}

// NewService creates the ticket service; hooks may be nil (no SLA).
func NewService(repo Repository, hooks SLAHooks) *Service {
	return &Service{repo: repo, sla: hooks}
}

// Repository returns the repository of the service.
func (s *Service) Repository() Repository { return s.repo }

// prepare applies the defaults of a new ticket and validates it: a title
// is required (400), the priority must be one of the list (422), status ""
// becomes new.
func prepare(t *Ticket) error {
	if t.Title == "" {
		return &api.FieldError{Field: "title", Message: "title is required", Status: http.StatusBadRequest}
	}
	if t.Priority == "" {
		t.Priority = DefaultPriority
	}
	if !ValidPriority(t.Priority) {
		return &api.FieldError{Field: "priority", Message: "must be one of low, medium, high, critical", Status: http.StatusUnprocessableEntity}
	}
	if t.Category == "" {
		t.Category = DefaultCategory
	}
	if t.Status == "" {
		t.Status = StatusNew
	}
	if t.Tags == nil {
		t.Tags = []string{}
	}
	return nil
}

// checkTarget validates the entity type (422 on field) and id (404) of a
// link target.
func checkTarget(field string, target Target) error {
	if !ValidLinkEntityType(target.EntityType) {
		return &api.FieldError{Field: field, Status: http.StatusUnprocessableEntity,
			Message: "must be one of " + strings.Join(LinkEntityTypes, ", ")}
	}
	if !api.IsUUID(target.EntityID) {
		return ErrNotFound
	}
	return nil
}

// Create creates ticket t with links in one transaction (Repository.RunInTx):
// with opts.Exclusive it first locks the target and fails with
// *ErrOpenTicketExists when a visible ticket that is not closed is linked
// to it; a link that fails (unknown or invisible target, duplicate) rolls
// back the ticket. The event ticket.updated of the creation and the links
// is written in the transaction. The SLA hook runs under a savepoint: when
// it fails (e.g. no policy for the priority) the ticket stays without SLA.
// With opts.Origin the audit entry ticket.create is the last write.
func (s *Service) Create(ctx context.Context, t *Ticket, links []Link, opts CreateOptions) error {
	if err := prepare(t); err != nil {
		return err
	}
	all := make([]Link, 0, len(links)+1)
	for i := range links {
		if err := checkTarget("entity_type", Target{EntityType: links[i].EntityType, EntityID: links[i].EntityID}); err != nil {
			return err
		}
		all = append(all, links[i])
	}
	if opts.Exclusive != nil {
		if err := checkTarget("entity_type", *opts.Exclusive); err != nil {
			return err
		}
		if !hasLink(all, *opts.Exclusive) {
			all = append(all, Link{EntityType: opts.Exclusive.EntityType, EntityID: opts.Exclusive.EntityID})
		}
	}
	if opts.Origin != nil {
		if err := checkTarget("entity_type", *opts.Origin); err != nil {
			return err
		}
	}
	err := s.repo.RunInTx(ctx, t.OrganizationID, func(ctx context.Context) error {
		if opts.Exclusive != nil {
			open, err := s.repo.LockOpenTicket(ctx, t.OrganizationID, *opts.Exclusive)
			if err != nil {
				return err
			}
			if open != nil {
				return &ErrOpenTicketExists{ID: open.ID, TicketNumber: open.TicketNumber}
			}
		}
		if err := s.repo.Create(ctx, t); err != nil {
			return err
		}
		for i := range all {
			all[i].OrganizationID, all[i].TicketID = t.OrganizationID, t.ID
			if err := s.repo.CreateLink(ctx, &all[i]); err != nil {
				return err
			}
		}
		if s.sla != nil {
			// A failed hook leaves the ticket without SLA, as before.
			_ = database.Savepoint(ctx, func(ctx context.Context) error {
				return s.sla.ApplyForTicket(ctx, t.OrganizationID, t)
			})
		}
		if opts.Origin != nil {
			return s.repo.RecordOrigin(ctx, t, *opts.Origin)
		}
		return nil
	})
	return err
}

// hasLink reports whether links names target.
func hasLink(links []Link, target Target) bool {
	for i := range links {
		if links[i].EntityType == target.EntityType && strings.EqualFold(links[i].EntityID, target.EntityID) {
			return true
		}
	}
	return false
}

// AddComment adds comment c of a user (author_type user unless set): an
// empty content is 400, a ticket that does not exist or is not visible
// ErrNotFound. A public comment of a user is the first response of the
// SLA (MarkFirstResponse); internal and system comments are not.
func (s *Service) AddComment(ctx context.Context, c *Comment) error {
	if c.Content == "" {
		return &api.FieldError{Field: "content", Message: "content is required", Status: http.StatusBadRequest}
	}
	if c.AuthorType == "" {
		c.AuthorType = AuthorUser
	}
	if c.AuthorType == AuthorSystem {
		c.AuthorID = ""
	}
	if err := s.repo.AddComment(ctx, c); err != nil {
		return err
	}
	if s.sla != nil && !c.IsInternal && c.AuthorType == AuthorUser {
		// Under a savepoint: in a context transaction (ticket.Provider,
		// WP-293) a failed hook must not abort it.
		_ = database.Savepoint(ctx, func(ctx context.Context) error {
			return s.sla.MarkFirstResponse(ctx, c.OrganizationID, c.TicketID, time.Now().UTC())
		})
	}
	return nil
}

// AddSystemComment adds an internal comment of the system (author_type
// system, no author_id) to ticket ticketID, e.g. when a review item is
// resolved without an actor (WP-767); it is no first response.
func (s *Service) AddSystemComment(ctx context.Context, orgID, ticketID, content string) (*Comment, error) {
	c := &Comment{OrganizationID: orgID, TicketID: ticketID, AuthorType: AuthorSystem, Content: content, IsInternal: true}
	if err := s.AddComment(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// LinkedTickets returns per id of entityType the tickets visible to the
// caller that are linked to it ({id, ticket_number, status}), with one
// query; an unknown entity type is a 422 field error.
func (s *Service) LinkedTickets(ctx context.Context, orgID, entityType string, ids []string) (map[string][]LinkedTicket, error) {
	if !ValidLinkEntityType(entityType) {
		return nil, &api.FieldError{Field: "entity_type", Status: http.StatusUnprocessableEntity,
			Message: "must be one of " + strings.Join(LinkEntityTypes, ", ")}
	}
	if len(ids) == 0 {
		return map[string][]LinkedTicket{}, nil
	}
	return s.repo.LinkedTickets(ctx, orgID, entityType, ids)
}
