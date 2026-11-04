package ticket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ExternalRef is the foreign reference of a ticket or ticket comment
// (TKT-03, CH17, WP-293): the key of the provider and the id of the object
// there. It is unique per organization and provider.
type ExternalRef struct {
	Provider string `json:"provider"`
	Ref      string `json:"ref"`
}

// ProviderInternal is the provider key of the internal ticket module.
const ProviderInternal = "internal"

// errExternalRefRequired: a reference needs its provider and its id.
var errExternalRefRequired = &api.FieldError{Field: "external_ref", Status: http.StatusUnprocessableEntity,
	Message: "provider and ref are required"}

// normalize trims the reference and validates that both parts are set.
func (r *ExternalRef) normalize() error {
	r.Provider, r.Ref = strings.TrimSpace(r.Provider), strings.TrimSpace(r.Ref)
	if r.Provider == "" || r.Ref == "" {
		return errExternalRefRequired
	}
	return nil
}

// Provider is the ticket provider interface (TKT-03 [O], CH17, E-47): the
// operations a ticket system offers the CMDB. Every method runs in the
// tenant scope of the caller (database.ContextWithTenantScope); without it
// the PostgreSQL profile fails with database.ErrNoTenantScope, with
// another organization with database.ErrTenantMismatch. Integrations of
// foreign systems (Jira, ServiceNow) implement it later and call their
// system only after the commit (database.AfterCommit).
type Provider interface {
	// Create creates ticket t; with ref the ticket carries the foreign
	// reference, a reference already taken in the organization is
	// *api.ConflictError.
	Create(ctx context.Context, t *Ticket, ref *ExternalRef) error
	// Update changes ticket ticketID of organization orgID.
	Update(ctx context.Context, orgID, ticketID string, req *UpdateRequest) (*Ticket, error)
	// Comment adds comment c to its ticket; with ref the comment carries
	// the foreign id, and a repeated call with the same reference adds no
	// second comment but returns the stored one.
	Comment(ctx context.Context, c *Comment, ref *ExternalRef) (*Comment, error)
	// Link links the object of link to its ticket.
	Link(ctx context.Context, link *Link) error
	// StatusSync sets the status of the ticket with the reference through
	// the state machine: an unknown reference is ErrNotFound, a refused
	// transition ErrInvalidTransition.
	StatusSync(ctx context.Context, orgID string, ref ExternalRef, status string) (*Ticket, error)
}

// InternalProvider is the reference provider: the internal ticket module.
// It bypasses no business logic: Create and Comment go through
// Service.Create and Service.AddComment (WP-766), Update and StatusSync
// through Service.Update, Link through Service.CreateLink (WP-764), so the
// defaults, the SLA hooks and ticket.updated (WP-290) run on every
// operation. On the repository it uses only the foreign reference methods.
// Each operation runs in one transaction (Repository.RunInTx). Outside a
// request of the route audit (no audit.RequestInfo in the context) it
// writes one audit entry per operation as the last write of that
// transaction; the memory profile (no pool) has no transaction and no
// audit.
type InternalProvider struct {
	svc  *Service
	pool *pgxpool.Pool
}

var _ Provider = (*InternalProvider)(nil)

// NewInternalProvider creates the internal provider over svc; pool is the
// database of the audit entries, nil in the memory profile.
func NewInternalProvider(svc *Service, pool *pgxpool.Pool) *InternalProvider {
	return &InternalProvider{svc: svc, pool: pool}
}

// providerAudit is what the audit entry of an operation names: the ticket,
// its foreign reference and ids, no contents.
type providerAudit struct {
	ticketID string
	ref      ExternalRef
	extra    map[string]any
}

// run runs op in one transaction of orgID; the audit entry of the
// operation op returns (nil: none) is the last write.
func (p *InternalProvider) run(ctx context.Context, orgID, operation string, op func(ctx context.Context) (*providerAudit, error)) error {
	return p.svc.repo.RunInTx(ctx, orgID, func(ctx context.Context) error {
		entry, err := op(ctx)
		if err != nil || entry == nil {
			return err
		}
		return p.record(ctx, orgID, operation, entry)
	})
}

// record writes the audit entry ticket.provider.<operation> when the route
// audit does not run (no request context) and the profile has a database.
func (p *InternalProvider) record(ctx context.Context, orgID, operation string, a *providerAudit) error {
	if p.pool == nil {
		return nil
	}
	if _, ok := audit.RequestInfoFrom(ctx); ok {
		return nil
	}
	actor := strings.TrimSpace(tenant.FromContext(ctx).UserID)
	actorType := "user"
	if actor == "" {
		actorType = "system"
	}
	changes := map[string]any{"operation": operation}
	if a.ref.Provider != "" {
		changes["external_provider"], changes["external_ref"] = a.ref.Provider, a.ref.Ref
	}
	for k, v := range a.extra {
		changes[k] = v
	}
	entry := audit.Entry{
		OrganizationID: orgID,
		ActorID:        actor,
		ActorType:      actorType,
		Action:         "ticket.provider." + operation,
		ResourceType:   "ticket",
		ResourceID:     a.ticketID,
		Changes:        changes,
	}
	return database.WithRequestTenant(ctx, p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		write := func() error {
			if _, err := audit.NewPGRecorder().Record(ctx, tx, entry); err != nil {
				return fmt.Errorf("record ticket provider %s: %w", operation, err)
			}
			return nil
		}
		if !database.InTx(ctx) {
			return write()
		}
		return database.BeforeCommit(ctx, write)
	})
}

// Create implements Provider.
func (p *InternalProvider) Create(ctx context.Context, t *Ticket, ref *ExternalRef) error {
	if ref != nil {
		if err := ref.normalize(); err != nil {
			return err
		}
		t.ExternalProvider, t.ExternalRef = ref.Provider, ref.Ref
	}
	return p.run(ctx, t.OrganizationID, "create", func(ctx context.Context) (*providerAudit, error) {
		if err := p.svc.Create(ctx, t, nil, CreateOptions{}); err != nil {
			return nil, err
		}
		return &providerAudit{ticketID: t.ID, ref: ExternalRef{Provider: t.ExternalProvider, Ref: t.ExternalRef},
			extra: map[string]any{"ticket_number": t.TicketNumber}}, nil
	})
}

// Update implements Provider.
func (p *InternalProvider) Update(ctx context.Context, orgID, ticketID string, req *UpdateRequest) (*Ticket, error) {
	var item *Ticket
	err := p.run(ctx, orgID, "update", func(ctx context.Context) (*providerAudit, error) {
		var err error
		if item, err = p.svc.Update(ctx, orgID, ticketID, req); err != nil {
			return nil, err
		}
		return &providerAudit{ticketID: item.ID, ref: ExternalRef{Provider: item.ExternalProvider, Ref: item.ExternalRef}}, nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

// Comment implements Provider. A reference already taken by a comment of
// another ticket is *api.ConflictError.
func (p *InternalProvider) Comment(ctx context.Context, c *Comment, ref *ExternalRef) (*Comment, error) {
	if ref != nil {
		if err := ref.normalize(); err != nil {
			return nil, err
		}
	}
	out := c
	err := p.run(ctx, c.OrganizationID, "comment", func(ctx context.Context) (*providerAudit, error) {
		if ref != nil {
			existing, err := p.svc.repo.GetCommentByExternalRef(ctx, c.OrganizationID, ref.Provider, ref.Ref)
			switch {
			case err == nil:
				if !strings.EqualFold(existing.TicketID, c.TicketID) {
					return nil, errExternalRefTaken
				}
				// Idempotent: the comment exists, nothing is written.
				out = existing
				return nil, nil
			case !errors.Is(err, ErrNotFound):
				return nil, err
			}
			c.ExternalProvider, c.ExternalRef = ref.Provider, ref.Ref
		}
		if err := p.svc.AddComment(ctx, c); err != nil {
			return nil, err
		}
		return &providerAudit{ticketID: c.TicketID, ref: ExternalRef{Provider: c.ExternalProvider, Ref: c.ExternalRef},
			extra: map[string]any{"comment_id": c.ID}}, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Link implements Provider: ticket and target must be visible under the
// caller's scope.
func (p *InternalProvider) Link(ctx context.Context, link *Link) error {
	return p.run(ctx, link.OrganizationID, "link", func(ctx context.Context) (*providerAudit, error) {
		if err := p.svc.CreateLink(ctx, link); err != nil {
			return nil, err
		}
		return &providerAudit{ticketID: link.TicketID, extra: map[string]any{
			"link_id": link.ID, "entity_type": link.EntityType, "entity_id": link.EntityID,
		}}, nil
	})
}

// StatusSync implements Provider.
func (p *InternalProvider) StatusSync(ctx context.Context, orgID string, ref ExternalRef, status string) (*Ticket, error) {
	if err := ref.normalize(); err != nil {
		return nil, err
	}
	var item *Ticket
	err := p.run(ctx, orgID, "status_sync", func(ctx context.Context) (*providerAudit, error) {
		current, err := p.svc.repo.GetByExternalRef(ctx, orgID, ref.Provider, ref.Ref)
		if err != nil {
			return nil, err
		}
		if item, err = p.svc.Update(ctx, orgID, current.ID, &UpdateRequest{Status: &status}); err != nil {
			return nil, err
		}
		return &providerAudit{ticketID: item.ID, ref: ref, extra: map[string]any{"status": item.Status}}, nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}
