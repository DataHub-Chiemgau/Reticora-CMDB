package notify

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Center bundles the stores of the notification API.
type Center struct {
	Channels    Repository
	Inbox       InboxStore
	Preferences PreferenceStore
	Deliveries  DeliveryStore
}

// NewMemoryCenter creates a center in memory (tests, --no-db).
func NewMemoryCenter() *Center {
	store := NewMemoryStore()
	return &Center{Channels: NewMemoryRepository(), Inbox: NewMemoryInbox(), Preferences: store, Deliveries: store}
}

// Handler serves the notification center of the signed-in user and the
// channel administration (NTF-04, API-05).
type Handler struct{ c *Center }

// NewHandler creates the handler.
func NewHandler(c *Center) *Handler { return &Handler{c: c} }

// RegisterRoutes registers the routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/notifications", h.List)
	r.Post("/api/v1/notifications/read-all", h.MarkAllRead)
	r.Post("/api/v1/notifications/{id}/read", h.MarkRead)
	r.Get("/api/v1/notifications/preferences", h.GetPreferences)
	r.Put("/api/v1/notifications/preferences", h.PutPreferences)
	r.Get("/api/v1/notification-channels", h.ListChannels)
	r.Post("/api/v1/notification-channels", h.CreateChannel)
	r.Get("/api/v1/notification-channels/{id}", h.GetChannel)
	r.Patch("/api/v1/notification-channels/{id}", h.UpdateChannel)
	r.Delete("/api/v1/notification-channels/{id}", h.DeleteChannel)
	r.Get("/api/v1/notification-deliveries", h.ListDeliveries)
}

func user(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" || t.UserID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "the notification center needs a signed-in user")
		return t, false
	}
	return t, true
}

func org(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return t, false
	}
	return t, true
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
	case errors.Is(err, ErrConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// List handles GET /api/v1/notifications: the user's in-app notifications,
// newest first; unread=true limits to the unread ones.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t, ok := user(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.c.Inbox.List(r.Context(), t.OrganizationID, t.UserID, r.URL.Query().Get("unread") == "true", page.Limit, page.Offset)
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []InAppNotification{}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[InAppNotification]{Data: items, Total: total, Limit: page.Limit,
		Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}

// MarkRead handles POST /api/v1/notifications/{id}/read.
func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	t, ok := user(w, r)
	if !ok {
		return
	}
	if err := h.c.Inbox.MarkRead(r.Context(), t.OrganizationID, t.UserID, chi.URLParam(r, "id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MarkAllRead handles POST /api/v1/notifications/read-all.
func (h *Handler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	t, ok := user(w, r)
	if !ok {
		return
	}
	n, err := h.c.Inbox.MarkAllRead(r.Context(), t.OrganizationID, t.UserID)
	if err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]int{"marked": n})
}

// GetPreferences handles GET /api/v1/notifications/preferences.
func (h *Handler) GetPreferences(w http.ResponseWriter, r *http.Request) {
	t, ok := user(w, r)
	if !ok {
		return
	}
	p, err := h.c.Preferences.Preference(r.Context(), t.OrganizationID, t.UserID)
	if err != nil {
		writeError(w, err)
		return
	}
	if p == nil {
		p = &Preference{}
		_ = p.Validate()
	}
	api.WriteJSON(w, http.StatusOK, p)
}

// PutPreferences handles PUT /api/v1/notifications/preferences.
func (h *Handler) PutPreferences(w http.ResponseWriter, r *http.Request) {
	t, ok := user(w, r)
	if !ok {
		return
	}
	var p Preference
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	if err := p.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := h.c.Preferences.SetPreference(r.Context(), t.OrganizationID, t.UserID, &p); err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, p)
}

// secretPlaceholder replaces secrets in responses; sent back unchanged it
// keeps the stored secret.
const secretPlaceholder = "********"

// secretKeys are the configuration keys that are never returned.
var secretKeys = []string{"password", "secret"}

// maskURL shows a URL as scheme and host only: path and query of an
// incoming webhook carry its token, user info a password (F-154).
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return secretPlaceholder
	}
	return u.Scheme + "://" + u.Host + "/" + secretPlaceholder
}

// urlPattern finds URLs in texts such as a delivery's last error.
var urlPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>]+`)

// maskURLs masks every URL in text like maskURL.
func maskURLs(text string) string { return urlPattern.ReplaceAllStringFunc(text, maskURL) }

func redact(ch *Channel) *Channel {
	out := *ch
	out.Config = map[string]any{}
	for k, v := range ch.Config {
		out.Config[k] = v
	}
	for _, k := range secretKeys {
		if v, ok := out.Config[k].(string); ok && v != "" {
			out.Config[k] = secretPlaceholder
		}
	}
	if v, ok := out.Config["url"].(string); ok && v != "" && ch.Provider == ProviderWebhook {
		out.Config["url"] = maskURL(v)
	}
	return &out
}

// ListChannels handles GET /api/v1/notification-channels.
func (h *Handler) ListChannels(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	channels, err := h.c.Channels.List(r.Context(), t.OrganizationID)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]*Channel, len(channels))
	for i := range channels {
		out[i] = redact(&channels[i])
	}
	api.WriteJSON(w, http.StatusOK, api.CompleteList(out))
}

// channelRequest is the body of create and update.
type channelRequest struct {
	Name     *string        `json:"name"`
	Kind     *string        `json:"kind"`
	Provider *string        `json:"provider"`
	Config   map[string]any `json:"config"`
	Enabled  *bool          `json:"enabled"`
}

// CreateChannel handles POST /api/v1/notification-channels.
func (h *Handler) CreateChannel(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	var req channelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	ch := &Channel{OrganizationID: t.OrganizationID, Config: req.Config, Enabled: true}
	if req.Name != nil {
		ch.Name = *req.Name
	}
	if req.Kind != nil {
		ch.Kind = *req.Kind
	}
	if req.Provider != nil {
		ch.Provider = *req.Provider
	}
	if req.Enabled != nil {
		ch.Enabled = *req.Enabled
	}
	for _, k := range secretKeys {
		if ch.Config[k] == secretPlaceholder {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", k+" must be set")
			return
		}
	}
	if err := h.c.Channels.Create(r.Context(), ch); err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, redact(ch))
}

// GetChannel handles GET /api/v1/notification-channels/{id}.
func (h *Handler) GetChannel(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	ch, err := h.c.Channels.Get(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, redact(ch))
}

// UpdateChannel handles PATCH /api/v1/notification-channels/{id}: given
// fields replace the stored ones; a configuration replaces the stored one,
// a secret sent back as the placeholder and a webhook url sent back masked
// keep their value.
func (h *Handler) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	ch, err := h.c.Channels.Get(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, err)
		return
	}
	var req channelRequest
	if err = json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	if req.Name != nil {
		ch.Name = *req.Name
	}
	if req.Kind != nil || req.Provider != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "kind and provider cannot change; create a new channel")
		return
	}
	if req.Enabled != nil {
		ch.Enabled = *req.Enabled
	}
	if req.Config != nil {
		for _, k := range secretKeys {
			if req.Config[k] == secretPlaceholder {
				req.Config[k] = ch.Config[k]
			}
		}
		if stored, _ := ch.Config["url"].(string); stored != "" && ch.Provider == ProviderWebhook && req.Config["url"] == maskURL(stored) {
			req.Config["url"] = stored
		}
		ch.Config = req.Config
	}
	if err = h.c.Channels.Update(r.Context(), ch); err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, redact(ch))
}

// DeleteChannel handles DELETE /api/v1/notification-channels/{id}.
func (h *Handler) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.c.Channels.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deliveryView is a delivery in the list: whom, what and how it went, but
// not the content. Body and data hold links such as an invitation's, URLs
// in subject and error are masked (F-154).
type deliveryView struct {
	ID             string      `json:"id"`
	OrganizationID string      `json:"organization_id"`
	ChannelID      string      `json:"channel_id"`
	Recipient      Recipient   `json:"recipient"`
	Recipients     []Recipient `json:"recipients,omitempty"`
	Category       string      `json:"category"`
	Critical       bool        `json:"critical"`
	Subject        string      `json:"subject"`
	DedupKey       string      `json:"dedup_key,omitempty"`
	Status         string      `json:"status"`
	Attempts       int         `json:"attempts"`
	MaxAttempts    int         `json:"max_attempts"`
	NextAttemptAt  time.Time   `json:"next_attempt_at"`
	LastError      string      `json:"last_error,omitempty"`
	ProviderID     string      `json:"provider_id,omitempty"`
	SentAt         *time.Time  `json:"sent_at,omitempty"`
}

func viewDelivery(d *QueuedDelivery) deliveryView {
	v := deliveryView{ID: d.ID, OrganizationID: d.OrganizationID, ChannelID: d.ChannelID, Recipient: d.Recipient,
		Recipients: d.Recipients, Category: d.Category, Critical: d.Critical, Subject: maskURLs(d.Subject),
		DedupKey: d.DedupKey, Status: d.Status, Attempts: d.Attempts, MaxAttempts: d.MaxAttempts,
		NextAttemptAt: d.NextAttemptAt, LastError: maskURLs(d.LastError), ProviderID: d.ProviderID, SentAt: d.SentAt}
	if strings.HasPrefix(d.RecipientKey, "mail:") && v.Recipient.Email == "" {
		v.Recipient.Email = strings.TrimPrefix(d.RecipientKey, "mail:")
	}
	return v
}

// ListDeliveries handles GET /api/v1/notification-deliveries: the latest
// deliveries of the organization with their status (NTF-04).
func (h *Handler) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	// One row more than the page tells whether another page follows.
	items, err := h.c.Deliveries.List(r.Context(), t.OrganizationID, page.Limit+1, page.Offset)
	if err != nil {
		writeError(w, err)
		return
	}
	hasMore := len(items) > page.Limit
	if hasMore {
		items = items[:page.Limit]
	}
	out := make([]deliveryView, len(items))
	for i := range items {
		out[i] = viewDelivery(&items[i])
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[deliveryView]{Data: out, Limit: page.Limit, Offset: page.Offset, HasMore: hasMore})
}
