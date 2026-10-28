// Package webhook provides webhook subscription management for Reticora CMDB.
package webhook

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Subscription represents a webhook subscription.
type Subscription struct {
	ID             string            `json:"id"`
	OrganizationID string            `json:"organization_id"`
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	Secret         string            `json:"-"` // never expose in API responses
	Events         []string          `json:"events"`
	IsActive       bool              `json:"is_active"`
	Headers        map[string]string `json:"headers,omitempty"`
	// ServiceAccountID binds the subscription to a service account: it then
	// receives only events whose object the account may read (RBA-08).
	ServiceAccountID string `json:"service_account_id,omitempty"`
	// PreviousSecret stays valid until PreviousSecretExpiresAt after a
	// rotation (SEC-06): deliveries are signed with both.
	PreviousSecret          string     `json:"-"`
	PreviousSecretExpiresAt *time.Time `json:"previous_secret_expires_at,omitempty"`
	CreatedAt               string     `json:"created_at"`
	UpdatedAt               string     `json:"updated_at"`
	// Scope is the scope snapshot of the subscription; nil for one without.
	Scope *Scope `json:"-"`
}

// Scope is the scope snapshot of a subscription (TEN-09, E-98): the client,
// site and team scope its creator had when creating it. A nil dimension is
// the whole organization, an empty one no id. A subscription without
// snapshot (from before migration 000155) belongs to the whole
// organization.
type Scope struct {
	Clients []string
	Sites   []string
	Teams   []string
}

// ScopeOf returns the snapshot of a tenant scope.
func ScopeOf(s *database.TenantScope) *Scope {
	set := func(d database.ScopeSet) []string {
		if d.IsAll() {
			return nil
		}
		if ids := d.IDs(); ids != nil {
			return ids
		}
		return []string{}
	}
	return &Scope{Clients: set(s.Clients), Sites: set(s.Sites), Teams: set(s.Teams)}
}

// orgWide reports whether the snapshot grants the whole organization: no
// snapshot, or none of its dimensions restricted.
func (s *Scope) orgWide() bool {
	return s == nil || (s.Clients == nil && s.Sites == nil && s.Teams == nil)
}

// containsID reports whether ids holds id, ignoring case.
func containsID(ids []string, id string) bool {
	return slices.ContainsFunc(ids, func(v string) bool { return strings.EqualFold(v, id) })
}

// VisibleTo reports whether a principal with the tenant scope s sees and
// manages the subscription, the rule of webhook_sub_isolation (migration
// 000155): the subscription belongs to the organization of s, and in every
// dimension s is org-wide or the snapshot lies completely in s. A
// subscription without snapshot is visible to org-wide principals only
// (E-09).
func (sub *Subscription) VisibleTo(s *database.TenantScope) bool {
	if s == nil || !strings.EqualFold(sub.OrganizationID, s.OrgID) {
		return false
	}
	snapshot := sub.Scope
	if snapshot == nil {
		snapshot = &Scope{}
	}
	within := func(ids []string, set database.ScopeSet) bool {
		if set.IsAll() {
			return true
		}
		if ids == nil {
			return false
		}
		allowed := set.IDs()
		for _, id := range ids {
			if !containsID(allowed, id) {
				return false
			}
		}
		return true
	}
	return within(snapshot.Clients, s.Clients) && within(snapshot.Sites, s.Sites) && within(snapshot.Teams, s.Teams)
}

// visibleIn reports whether the subscription is visible in ctx: a request
// (a tenant scope in ctx) sees the subscriptions its scope covers
// (VisibleTo), a call without scope (dispatcher, system paths) every one.
func visibleIn(ctx context.Context, sub *Subscription) bool {
	scope, ok := database.TenantScopeFromContext(ctx)
	return !ok || sub.VisibleTo(&scope)
}

// SecretOverlap is how long a rotated secret stays valid (SEC-06).
const SecretOverlap = 24 * time.Hour

// SecretRotator replaces a subscription's secret and keeps the old one
// valid until validUntil.
type SecretRotator interface {
	RotateSecret(ctx context.Context, orgID, id, secret string, validUntil time.Time) (*Subscription, error)
}

// RotateSecret implements SecretRotator.
func (r *MemoryRepository) RotateSecret(_ context.Context, orgID, id, secret string, validUntil time.Time) (*Subscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sub, ok := r.items[id]
	if !ok || sub.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	until := validUntil.UTC()
	sub.PreviousSecret, sub.PreviousSecretExpiresAt, sub.Secret = sub.Secret, &until, secret
	cp := *sub
	return &cp, nil
}

// MaskedHeaderValue replaces every custom header value in API responses.
const MaskedHeaderValue = "***"

// redacted returns the subscription as the API shows it: custom header values
// often carry authentication (Authorization, API keys), so they are
// write-only and masked; only the header names are returned (SEC-01).
// Subscriber URLs often carry a token in path, query or userinfo, so a
// caller without webhook:manage sees only scheme and host (F-202).
func (s *Subscription) redacted(ctx context.Context) Subscription {
	out := *s
	if len(s.Headers) > 0 {
		out.Headers = make(map[string]string, len(s.Headers))
		for name := range s.Headers {
			out.Headers[name] = MaskedHeaderValue
		}
	}
	if !mayManage(ctx) {
		out.URL = urlOrigin(s.URL)
	}
	return out
}

// mayManage reports whether the caller holds webhook:manage; a request
// without an authenticated principal does not.
func mayManage(ctx context.Context) bool {
	principal, ok := identity.PrincipalFromContext(ctx)
	return ok && principal.Has(identity.PermWebhookManage)
}

// urlOrigin returns scheme and host of raw, without userinfo, path, query
// and fragment; a value that is no absolute URL is shown empty.
func urlOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// urlInText finds the URLs in a delivery error: the HTTP client names the
// full subscriber URL (`Post "https://…": …`).
var urlInText = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)

// redactError reduces every URL in a delivery error to scheme and host for
// a caller without webhook:manage (F-202).
func redactError(ctx context.Context, text string) string {
	if text == "" || mayManage(ctx) {
		return text
	}
	return urlInText.ReplaceAllStringFunc(text, urlOrigin)
}

// CreateRequest is the payload for creating a webhook subscription.
type CreateRequest struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Secret  string            `json:"secret"`
	Events  []string          `json:"events"`
	Headers map[string]string `json:"headers,omitempty"`
	// ServiceAccountID optionally binds the subscription (RBA-08).
	ServiceAccountID string `json:"service_account_id,omitempty"`
}

// TestEvent is the event name used by the webhook test endpoint. It is
// deliberately not part of ValidEvents so it cannot be subscribed to.
const TestEvent = "webhook.test"

// ValidEvents lists allowed webhook events.
var ValidEvents = map[string]bool{
	"ci.created":           true,
	"ci.updated":           true,
	"ci.deleted":           true,
	"ci.status_changed":    true,
	"relationship.created": true,
	"relationship.deleted": true,
	"discovery.completed":  true,
	// Enterprise CMDB + asset/inventory extension (spec §20).
	"ci_type.created":               true,
	"ci_type.updated":               true,
	"ci_type.deactivated":           true,
	"ci_attribute.changed":          true,
	"lifecycle.transitioned":        true,
	"asset.movement":                true,
	"inventory.movement":            true,
	"reservation.created":           true,
	"reservation.released":          true,
	"composition.created":           true,
	"composition.updated":           true,
	"composition.deleted":           true,
	"override.changed":              true,
	"reconciliation.policy_changed": true,
	// The domain events of EVT-02 (WP-136); ci.updated and
	// relationship.deleted are their former names.
	"ci.changed":           true,
	"ci.restored":          true,
	"ci.merged":            true,
	"ci.offline":           true,
	"ci.online":            true,
	"relationship.removed": true,
	"collector.enrolled":   true,
	"collector.offline":    true,
	"review.created":       true,
	"review.resolved":      true,
	"export.completed":     true,
	"job.failed":           true,
	"entitlement.warning":  true,
	"entitlement.expired":  true,
	"invitation.created":   true,
	"certificate.expiring": true,
}

// withheldKeys are the data keys of an event that webhook deliveries never
// carry: the link of invitation.created holds the acceptance token, with
// which a webhook receiver could accept the invitation itself (NTF-05,
// F-041). Only the invitee gets it, by mail.
var withheldKeys = map[string][]string{
	events.InvitationCreated: {"link", "token"},
}

// redactPayload returns the webhook body of event: body without the
// withheld keys of the event. ok is false when such a body is not a JSON
// object; it is then not delivered.
func redactPayload(event string, body []byte) (redacted []byte, ok bool) {
	keys := withheldKeys[event]
	if len(keys) == 0 {
		return body, true
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, false
	}
	for _, k := range keys {
		delete(data, k)
	}
	out, err := json.Marshal(data)
	if err != nil {
		return nil, false
	}
	return out, true
}

// Repository defines persistence operations for webhooks.
type Repository interface {
	List(ctx context.Context, orgID string, page api.PaginationParams) ([]Subscription, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Subscription, error)
	Create(ctx context.Context, sub *Subscription) error
	Delete(ctx context.Context, orgID, id string) error
	ListByEvent(ctx context.Context, orgID, event string) ([]Subscription, error)
}

// MemoryRepository is an in-memory webhook store. Like the PostgreSQL
// repository it shows a request only the subscriptions in its scope
// (visibleIn).
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Subscription
	seq   int
}

// NewMemoryRepository creates a new in-memory webhook repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: make(map[string]*Subscription)}
}

func (r *MemoryRepository) List(ctx context.Context, orgID string, page api.PaginationParams) ([]Subscription, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Subscription
	for _, sub := range r.items {
		if sub.OrganizationID != orgID || !visibleIn(ctx, sub) {
			continue
		}
		result = append(result, *sub)
	}

	total := len(result)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return result[start:end], total, nil
}

func (r *MemoryRepository) GetByID(ctx context.Context, orgID, id string) (*Subscription, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sub, ok := r.items[id]
	if !ok || sub.OrganizationID != orgID || !visibleIn(ctx, sub) {
		return nil, fmt.Errorf("not found")
	}
	return sub, nil
}

func (r *MemoryRepository) Create(_ context.Context, sub *Subscription) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	sub.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	sub.CreatedAt = now
	sub.UpdatedAt = now
	r.items[sub.ID] = sub
	return nil
}

func (r *MemoryRepository) Delete(ctx context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	sub, ok := r.items[id]
	if !ok || sub.OrganizationID != orgID || !visibleIn(ctx, sub) {
		return fmt.Errorf("not found")
	}
	delete(r.items, id)
	return nil
}

// Deactivate switches a subscription off (WHK-01).
func (r *MemoryRepository) Deactivate(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	sub, ok := r.items[id]
	if !ok || sub.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	sub.IsActive = false
	return nil
}

func (r *MemoryRepository) ListByEvent(ctx context.Context, orgID, event string) ([]Subscription, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Subscription
	for _, sub := range r.items {
		if sub.OrganizationID != orgID || !sub.IsActive || !visibleIn(ctx, sub) {
			continue
		}
		for _, e := range sub.Events {
			if e == event {
				result = append(result, *sub)
				break
			}
		}
	}
	return result, nil
}

// TestDeliverer performs a single synchronous delivery for the test endpoint.
type TestDeliverer interface {
	DeliverOnce(sub Subscription, event string, payload any) (Delivery, error)
}

// DeliveryLister exposes the durable delivery history of a subscription.
type DeliveryLister interface {
	ListDeliveries(ctx context.Context, orgID, subscriptionID string, page api.PaginationParams) ([]DeliveryRecord, int, error)
}

// DeliveryRetrier queues a failed or dead delivery again (WHK-01).
type DeliveryRetrier interface {
	Retry(ctx context.Context, orgID, subscriptionID, deliveryID string) (DeliveryRecord, error)
}

// DeadLetterLister exposes the dead-letter queue of a tenant.
type DeadLetterLister interface {
	ListDeadLetters(ctx context.Context, orgID string, page api.PaginationParams) ([]DeadLetter, int, error)
}

// Handler provides HTTP handlers for webhook endpoints.
type Handler struct {
	repo        Repository
	deliverer   TestDeliverer
	deliveries  DeliveryLister
	deadLetters DeadLetterLister
	retrier     DeliveryRetrier
	// egress validates subscriber URLs when they are stored (SEC-08).
	egress egress.Options
}

// WithEgress sets the destination policy subscriber URLs are validated
// against; the default blocks every internal destination.
func (h *Handler) WithEgress(opts egress.Options) *Handler {
	h.egress = opts
	return h
}

// NewHandler creates a new webhook handler. An optional TestDeliverer enables
// POST /api/v1/webhooks/{id}/test.
func NewHandler(repo Repository, deliverer ...TestDeliverer) *Handler {
	h := &Handler{repo: repo}
	if len(deliverer) > 0 {
		h.deliverer = deliverer[0]
		if lister, ok := deliverer[0].(DeliveryLister); ok {
			h.deliveries = lister
		}
		if lister, ok := deliverer[0].(DeadLetterLister); ok {
			h.deadLetters = lister
		}
		if retrier, ok := deliverer[0].(DeliveryRetrier); ok {
			h.retrier = retrier
		}
	}
	return h
}

// RegisterRoutes registers webhook routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/webhooks", h.List)
	r.Post("/api/v1/webhooks", h.Create)
	r.Get("/api/v1/webhooks/dead-letters", h.ListDeadLetters)
	r.Get("/api/v1/webhooks/{id}", h.Get)
	r.Delete("/api/v1/webhooks/{id}", h.Delete)
	r.Post("/api/v1/webhooks/{id}/test", h.Test)
	r.Post("/api/v1/webhooks/{id}/rotate-secret", h.RotateSecret)
	r.Get("/api/v1/webhooks/{id}/deliveries", h.ListDeliveries)
	r.Post("/api/v1/webhooks/{id}/deliveries/{delivery_id}/retry", h.RetryDelivery)
}

// tenantOf returns the tenant scope of the request or writes 401: every
// webhook route works in the organization and the scope of the caller
// (TEN-09); the tenant middleware attaches both.
func tenantOf(w http.ResponseWriter, r *http.Request) (database.TenantScope, bool) {
	scope, ok := database.TenantScopeFromContext(r.Context())
	if t := tenant.FromContext(r.Context()); !ok || t.OrganizationID == "" || !strings.EqualFold(scope.OrgID, t.OrganizationID) {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return database.TenantScope{}, false
	}
	return scope, true
}

// subscription loads the subscription of the path or writes 404, also for a
// subscription outside the caller's scope (VisibleTo, E-98), independent of
// the repository: every route with a subscription id answers as for an
// unknown one.
func (h *Handler) subscription(w http.ResponseWriter, r *http.Request, scope *database.TenantScope) (*Subscription, bool) {
	sub, err := h.repo.GetByID(r.Context(), scope.OrgID, chi.URLParam(r, "id"))
	if err != nil || !sub.VisibleTo(scope) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "webhook not found")
		return nil, false
	}
	return sub, true
}

// RetryDelivery handles POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/retry:
// a failed or dead delivery gets a new round of attempts (WHK-01).
func (h *Handler) RetryDelivery(w http.ResponseWriter, r *http.Request) {
	scope, ok := tenantOf(w, r)
	if !ok {
		return
	}
	if h.retrier == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "webhook delivery store is not configured")
		return
	}
	sub, found := h.subscription(w, r, &scope)
	if !found {
		return
	}
	rec, err := h.retrier.Retry(r.Context(), scope.OrgID, sub.ID, chi.URLParam(r, "delivery_id"))
	switch {
	case errors.Is(err, ErrNoDeliveryStore):
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", err.Error())
	case errors.Is(err, ErrNotRetryable):
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
	case err != nil:
		api.WriteRepoError(w, err)
	default:
		api.WriteJSON(w, http.StatusAccepted, rec)
	}
}

// RotateSecret handles POST /api/v1/webhooks/{id}/rotate-secret: a new
// secret (generated, or the one given) replaces the current one, which
// stays valid for SecretOverlap; the new secret is returned once.
func (h *Handler) RotateSecret(w http.ResponseWriter, r *http.Request) {
	scope, ok := tenantOf(w, r)
	if !ok {
		return
	}
	rotator, ok := h.repo.(SecretRotator)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "secret rotation is not available")
		return
	}
	var req struct {
		Secret string `json:"secret"`
	}
	if r.ContentLength > 0 {
		if err := api.ReadJSON(r, &req); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
	}
	if req.Secret == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			api.WriteError(w, http.StatusInternalServerError, "Internal Error", "generate secret")
			return
		}
		req.Secret = "whsec_" + hex.EncodeToString(raw)
	} else if len(req.Secret) < 16 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "secret must have at least 16 characters")
		return
	}
	// The repository rotates in the caller's scope (WP-562); the lookup
	// answers a subscription outside it with 404 first.
	current, found := h.subscription(w, r, &scope)
	if !found {
		return
	}
	sub, err := rotator.RotateSecret(r.Context(), scope.OrgID, current.ID, req.Secret, time.Now().Add(SecretOverlap))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "webhook not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"id": sub.ID, "secret": req.Secret,
		"previous_secret_expires_at": sub.PreviousSecretExpiresAt})
}

// Test handles POST /api/v1/webhooks/{id}/test by sending a single signed
// `webhook.test` payload to the subscription URL and returning the attempt.
func (h *Handler) Test(w http.ResponseWriter, r *http.Request) {
	scope, ok := tenantOf(w, r)
	if !ok {
		return
	}
	if h.deliverer == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "webhook dispatcher is not configured")
		return
	}

	sub, found := h.subscription(w, r, &scope)
	if !found {
		return
	}

	delivery, err := h.deliverer.DeliverOnce(*sub, TestEvent, map[string]any{
		"event":           TestEvent,
		"subscription_id": sub.ID,
		"organization_id": sub.OrganizationID,
		"sent_at":         time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, delivery)
}

// ListDeliveries handles GET /api/v1/webhooks/{id}/deliveries and returns the
// durable delivery history including retry state.
func (h *Handler) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	scope, ok := tenantOf(w, r)
	if !ok {
		return
	}
	if h.deliveries == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "webhook delivery store is not configured")
		return
	}

	sub, found := h.subscription(w, r, &scope)
	if !found {
		return
	}

	page := api.ParsePagination(r)
	records, total, err := h.deliveries.ListDeliveries(r.Context(), scope.OrgID, sub.ID, page)
	if err != nil {
		if errors.Is(err, ErrNoDeliveryStore) {
			api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", err.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	for i := range records {
		records[i].Error = redactError(r.Context(), records[i].Error)
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[DeliveryRecord]{
		Data:    records,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// ListDeadLetters handles GET /api/v1/webhooks/dead-letters and returns the
// deliveries that exhausted their retry budget, newest first: those of the
// subscriptions in the caller's scope (E-98).
func (h *Handler) ListDeadLetters(w http.ResponseWriter, r *http.Request) {
	scope, ok := tenantOf(w, r)
	if !ok {
		return
	}
	if h.deadLetters == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "webhook delivery store is not configured")
		return
	}

	page := api.ParsePagination(r)
	letters, total, err := h.deadLetters.ListDeadLetters(r.Context(), scope.OrgID, page)
	if err != nil {
		if errors.Is(err, ErrNoDeliveryStore) {
			api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", err.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	for i := range letters {
		letters[i].LastError = redactError(r.Context(), letters[i].LastError)
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[DeadLetter]{
		Data:    letters,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// List handles GET /api/v1/webhooks: the subscriptions in the caller's
// scope.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	scope, ok := tenantOf(w, r)
	if !ok {
		return
	}

	page := api.ParsePagination(r)
	subs, total, err := h.repo.List(r.Context(), scope.OrgID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	shown := make([]Subscription, 0, len(subs))
	for i := range subs {
		shown = append(shown, subs[i].redacted(r.Context()))
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Subscription]{
		Data:    shown,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Get handles GET /api/v1/webhooks/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	scope, ok := tenantOf(w, r)
	if !ok {
		return
	}

	sub, found := h.subscription(w, r, &scope)
	if !found {
		return
	}

	api.WriteJSON(w, http.StatusOK, sub.redacted(r.Context()))
}

// errServiceAccountNotFound refuses binding a subscription to a service
// account its creator does not see: unknown, malformed or of another
// organization.
var errServiceAccountNotFound = &api.FieldError{Field: "service_account_id", Message: "service account not found",
	Status: http.StatusUnprocessableEntity}

// Create handles POST /api/v1/webhooks. The subscription stores the scope
// of its creator (ScopeOf, E-98): only principals whose scope covers it see
// and manage it, and it receives only the events whose objects lie in it. A
// bound service account must be visible to the creator; deliveries then
// need its read right as well, so the binding never widens the creator's
// scope.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	scope, ok := tenantOf(w, r)
	if !ok {
		return
	}

	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.Name == "" || req.URL == "" || req.Secret == "" || len(req.Events) == 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name, url, secret, and events are required")
		return
	}

	if err := h.egress.ValidateURL(req.URL); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	for _, e := range req.Events {
		if !ValidEvents[e] {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", fmt.Sprintf("invalid event: %s", e))
			return
		}
	}

	if req.ServiceAccountID != "" && !uuidPattern.MatchString(req.ServiceAccountID) {
		api.WriteFieldError(w, errServiceAccountNotFound)
		return
	}

	sub := &Subscription{
		OrganizationID:   scope.OrgID,
		Name:             req.Name,
		URL:              req.URL,
		Secret:           req.Secret,
		Events:           req.Events,
		IsActive:         true,
		Headers:          req.Headers,
		ServiceAccountID: req.ServiceAccountID,
		Scope:            ScopeOf(&scope),
	}

	if err := h.repo.Create(r.Context(), sub); err != nil {
		if !api.WriteFieldError(w, err) {
			api.WriteRepoError(w, err)
		}
		return
	}

	api.WriteJSON(w, http.StatusCreated, sub.redacted(r.Context()))
}

// Delete handles DELETE /api/v1/webhooks/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	scope, ok := tenantOf(w, r)
	if !ok {
		return
	}

	sub, found := h.subscription(w, r, &scope)
	if !found {
		return
	}
	if err := h.repo.Delete(r.Context(), scope.OrgID, sub.ID); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "webhook not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
