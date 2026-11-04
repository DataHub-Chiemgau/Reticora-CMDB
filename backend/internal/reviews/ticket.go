package reviews

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Tickets from review items (TKT-01, WP-291): POST
// /api/v1/review-items/{id}/ticket opens a ticket for an open item through
// the ticket service, linked to the item, its candidate CIs and (for
// serial_mismatch) the asset; GET /api/v1/review-items and /{id} name the
// tickets of each item. The package does not import package ticket: the
// server wires a TicketCreator over ticket.Service, only while the module
// ticketing is on.

// TicketCategory is the category of a ticket from a review item.
const TicketCategory = "task"

// TicketPriorities is the priority of a ticket per review type; priority
// in the request takes precedence. A type missing here gets medium.
var TicketPriorities = map[string]string{
	KindAmbiguousIdentity:   "high",
	KindProbableDuplicate:   "high",
	KindResurrectedDevice:   "high",
	KindDuplicateIP:         "high",
	KindSerialMismatch:      "high",
	KindUnlicensedCI:        "high",
	KindConflictingValues:   "medium",
	KindOverrideConflict:    "medium",
	KindUnclassifiedDevice:  "medium",
	KindUnknownRelationship: "medium",
	KindScopeSuggested:      "low",
}

// ticketPriority returns the priority of a ticket for an item of kind.
func ticketPriority(kind string) string {
	if p, ok := TicketPriorities[kind]; ok {
		return p
	}
	return "medium"
}

// TicketRequest is the optional body of POST /api/v1/review-items/{id}/ticket
// (ReviewItemTicketRequest). The ticket service validates the values (422).
type TicketRequest struct {
	Priority   string `json:"priority,omitempty"`
	AssigneeID string `json:"assignee_id,omitempty"`
	TeamID     string `json:"team_id,omitempty"`
}

// fields lists the request fields the body sets (for the field
// permissions of the ticket).
func (r *TicketRequest) fields() []string {
	var out []string
	for _, f := range []struct{ name, value string }{
		{"priority", r.Priority}, {"assignee_id", r.AssigneeID}, {"team_id", r.TeamID},
	} {
		if f.value != "" {
			out = append(out, f.name)
		}
	}
	return out
}

// TicketRef is a ticket linked to a review item, as GET names it.
type TicketRef struct {
	ID           string `json:"id"`
	TicketNumber int    `json:"ticket_number"`
	Status       string `json:"status"`
}

// NewTicket is the ticket a review item opens.
type NewTicket struct {
	OrganizationID string
	// ReviewItemID is the item: the ticket is linked to it exclusively (at
	// most one ticket that is not closed) and it is the origin of the
	// audit entry ticket.create.
	ReviewItemID   string
	Title          string
	Description    string
	Priority       string
	Category       string
	AssigneeID     string
	TeamID         string
	RelatedCIID    string
	RelatedAssetID string
	// CIIDs are linked as ci, AssetID as asset.
	CIIDs   []string
	AssetID string
	// RequestFields are the fields the request body set.
	RequestFields []string
}

// TicketCreator is the ticket side of the review queue: the server
// implements it over ticket.Service.
type TicketCreator interface {
	// CreateTicket creates the ticket and its links in one transaction and
	// returns the created ticket as the API answers it. An open ticket of
	// the item is a *Error with status 409.
	CreateTicket(ctx context.Context, t *NewTicket) (any, error)
	// LinkedTickets returns the tickets of the items the caller may see,
	// with one query; nil without ticket:read or without the entitlement.
	LinkedTickets(ctx context.Context, orgID string, itemIDs []string) (map[string][]TicketRef, error)
	// CIName returns the name of a CI for the title of a ticket.
	CIName(ctx context.Context, orgID, ciID string) (string, error)
}

// WithTickets attaches the ticket side (WP-291); without it items open no
// tickets and name none.
func (s *Service) WithTickets(tickets TicketCreator) *Service {
	s.tickets = tickets
	return s
}

// CreateTicket opens a ticket for the open item id (WP-291): linked to the
// item, every candidate CI and, for serial_mismatch, the asset; the first
// candidate CI (else payload ci_id or matched_ci_id) is the related CI, so
// the ticket takes its client and site. The item stays open.
func (s *Service) CreateTicket(ctx context.Context, orgID, id string, req *TicketRequest) (any, error) {
	if s.tickets == nil {
		return nil, ErrNotFound
	}
	item, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if item.Status != StatusOpen {
		return nil, ErrAlreadyResolved
	}
	t := &NewTicket{
		OrganizationID: orgID,
		ReviewItemID:   item.ID,
		Description:    ticketDescription(item),
		Priority:       ticketPriority(item.Kind),
		Category:       TicketCategory,
		AssigneeID:     req.AssigneeID,
		TeamID:         req.TeamID,
		RequestFields:  req.fields(),
	}
	if req.Priority != "" {
		t.Priority = req.Priority
	}
	for _, ciID := range item.CandidateCIIDs {
		if api.IsUUID(ciID) && !containsFold(t.CIIDs, ciID) {
			t.CIIDs = append(t.CIIDs, ciID)
		}
	}
	switch {
	case len(t.CIIDs) > 0:
		t.RelatedCIID = t.CIIDs[0]
	case api.IsUUID(payloadText(item.Payload, "ci_id")):
		t.RelatedCIID = payloadText(item.Payload, "ci_id")
	case api.IsUUID(payloadText(item.Payload, "matched_ci_id")):
		t.RelatedCIID = payloadText(item.Payload, "matched_ci_id")
	}
	if t.RelatedCIID != "" && !containsFold(t.CIIDs, t.RelatedCIID) {
		t.CIIDs = append(t.CIIDs, t.RelatedCIID)
	}
	if item.Kind == KindSerialMismatch && api.IsUUID(payloadText(item.Payload, "asset_id")) {
		t.AssetID = payloadText(item.Payload, "asset_id")
		t.RelatedAssetID = t.AssetID
	}
	if t.Title, err = s.ticketTitle(ctx, orgID, item, t.RelatedCIID); err != nil {
		return nil, err
	}
	return s.tickets.CreateTicket(ctx, t)
}

// ticketTitle is "<kind>: <payload.name | payload.cidr | CI name>", else
// "<kind>: <object_key>" or the kind alone.
func (s *Service) ticketTitle(ctx context.Context, orgID string, item *Item, ciID string) (string, error) {
	subject := payloadText(item.Payload, "name")
	if subject == "" {
		subject = payloadText(item.Payload, "cidr")
	}
	if subject == "" && ciID != "" {
		name, err := s.tickets.CIName(ctx, orgID, ciID)
		if err != nil {
			return "", err
		}
		subject = name
	}
	if subject == "" {
		subject = item.ObjectKey
	}
	if subject == "" {
		return item.Kind, nil
	}
	return item.Kind + ": " + subject, nil
}

// omittedPayload are the payload fields a ticket description leaves out.
var omittedPayload = map[string]bool{"raw_data": true, "fingerprint": true}

// ticketDescription names the item, its object_key and its payload fields
// (sorted, without raw_data and fingerprint at any depth).
func ticketDescription(item *Item) string {
	var b strings.Builder
	b.WriteString("Review item " + item.ID + " (" + item.Kind + ")\n")
	if item.ObjectKey != "" {
		b.WriteString("object_key: " + item.ObjectKey + "\n")
	}
	keys := make([]string, 0, len(item.Payload))
	for k := range item.Payload {
		if !omittedPayload[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + ": " + payloadValue(stripPayload(item.Payload[k])) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// stripPayload removes the omitted fields from nested objects.
func stripPayload(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, inner := range value {
			if !omittedPayload[k] {
				out[k] = stripPayload(inner)
			}
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i := range value {
			out[i] = stripPayload(value[i])
		}
		return out
	}
	return v
}

// payloadValue writes a string as it is and any other value as JSON.
func payloadValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

func payloadText(payload map[string]any, key string) string {
	s, _ := payload[key].(string)
	return strings.TrimSpace(s)
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// attachTickets sets the tickets of the items with one query; without the
// ticket side, or when it names none (no ticket:read, no entitlement), the
// items carry no tickets.
func (s *Service) attachTickets(ctx context.Context, orgID string, items []Item) error {
	if s.tickets == nil || len(items) == 0 {
		return nil
	}
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	linked, err := s.tickets.LinkedTickets(ctx, orgID, ids)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Tickets = linked[items[i].ID]
	}
	return nil
}

// TicketConflict is the refusal of a second ticket for an item: 409 naming
// the open ticket.
func TicketConflict(id string, number int) error {
	return &Error{Status: http.StatusConflict,
		Message: "an open ticket is already linked to the review item: #" + strconv.Itoa(number) + " (" + id + ")"}
}
