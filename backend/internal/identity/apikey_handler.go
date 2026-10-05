package identity

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/go-chi/chi/v5"
)

// APIKeyRotationOverlap is how long a rotated key stays valid by default; a
// rotation may ask for 0 up to MaxAPIKeyRotationOverlap.
const (
	APIKeyRotationOverlap    = 24 * time.Hour
	MaxAPIKeyRotationOverlap = 7 * 24 * time.Hour
)

// APIKeyManager is the persistence of the api-keys resource.
type APIKeyManager interface {
	Create(ctx context.Context, key *StoredAPIKey) (*StoredAPIKey, error)
	List(ctx context.Context, orgID string) ([]StoredAPIKey, error)
	Revoke(ctx context.Context, orgID, id string) error
	Rotate(ctx context.Context, orgID, id string, overlapUntil time.Time) (*RotatedAPIKey, error)
}

// APIKeyHandler serves /api/v1/api-keys (AUT-04, API-05). Every route needs
// apikey:manage (authz). The plaintext of a key is returned only by create
// and rotate.
type APIKeyHandler struct {
	keys APIKeyManager
}

// NewAPIKeyHandler creates the handler.
func NewAPIKeyHandler(keys APIKeyManager) *APIKeyHandler {
	return &APIKeyHandler{keys: keys}
}

// RegisterRoutes registers the api-keys routes.
func (h *APIKeyHandler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/api-keys", h.Create)
	r.Get("/api/v1/api-keys", h.List)
	r.Delete("/api/v1/api-keys/{id}", h.Revoke)
	r.Post("/api/v1/api-keys/{id}/rotate", h.Rotate)
}

// APIKeyView is an API key without secret and hash.
type APIKeyView struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	KeyPrefix   string       `json:"key_prefix"`
	Environment string       `json:"environment"`
	Permissions []Permission `json:"permissions"`
	OwnerID     string       `json:"owner_id"`
	RotatedFrom *string      `json:"rotated_from,omitempty"`
	ExpiresAt   *time.Time   `json:"expires_at,omitempty"`
	RevokedAt   *time.Time   `json:"revoked_at,omitempty"`
	LastUsedAt  *time.Time   `json:"last_used_at,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
}

// CreatedAPIKey is a new key with its plaintext, shown exactly once.
type CreatedAPIKey struct {
	APIKeyView
	Key string `json:"key"`
}

func viewOf(k *StoredAPIKey) APIKeyView {
	perms := k.Permissions
	if perms == nil {
		perms = []Permission{}
	}
	return APIKeyView{
		ID: k.ID, Name: k.Name, KeyPrefix: k.KeyPrefix, Environment: k.Environment, Permissions: perms,
		OwnerID: k.CreatedBy, RotatedFrom: k.RotatedFrom, ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
		LastUsedAt: k.LastUsedAt, CreatedAt: k.CreatedAt,
	}
}

// userPrincipal returns the calling user. Keys are created and rotated by
// users only, so a key can never mint another key.
func userPrincipal(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return Principal{}, false
	}
	if p.Type != PrincipalTypeUser {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "API keys are managed by users only")
		return Principal{}, false
	}
	return p, true
}

// Create issues a key for the calling user. The key's permissions must be a
// subset of the caller's; at use the key holds the intersection with its
// owner's then current rights (AUT-04).
func (h *APIKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := userPrincipal(w, r)
	if !ok {
		return
	}
	var req struct {
		Name        string       `json:"name"`
		Permissions []Permission `json:"permissions"`
		ExpiresAt   *time.Time   `json:"expires_at"`
		Environment string       `json:"environment"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	switch {
	case req.Name == "":
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", "name is required")
		return
	case len(req.Permissions) == 0:
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", "permissions are required")
		return
	case req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()):
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", "expires_at must be in the future")
		return
	}
	known := map[Permission]bool{}
	for _, perm := range AllPermissions() {
		known[perm] = true
	}
	for _, perm := range req.Permissions {
		if !known[perm] {
			api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", "unknown permission "+string(perm))
			return
		}
		if !p.Has(perm) {
			api.WriteError(w, http.StatusForbidden, "Forbidden", "a key cannot grant the permission "+string(perm)+" the caller does not hold")
			return
		}
	}
	generated, err := GenerateAPIKey(req.Environment)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
		return
	}
	created, err := h.keys.Create(r.Context(), &StoredAPIKey{
		OrganizationID: p.OrganizationID, Name: req.Name, KeyHash: generated.KeyHash, KeyPrefix: generated.KeyPrefix,
		Environment: generated.Environment, Permissions: req.Permissions, CreatedBy: p.Subject, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, CreatedAPIKey{APIKeyView: viewOf(created), Key: generated.Plaintext})
}

// List returns the organization's keys without secrets.
func (h *APIKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}
	keys, err := h.keys.List(r.Context(), p.OrganizationID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	views := make([]APIKeyView, len(keys))
	for i := range keys {
		views[i] = viewOf(&keys[i])
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[APIKeyView]{Data: views, Total: len(views), Limit: len(views)})
}

// Revoke ends a key at once.
func (h *APIKeyHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}
	if err := h.keys.Revoke(r.Context(), p.OrganizationID, chi.URLParam(r, "id")); err != nil {
		writeAPIKeyError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Rotate issues a successor key; the old key stays valid for the overlap.
func (h *APIKeyHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	p, ok := userPrincipal(w, r)
	if !ok {
		return
	}
	var req struct {
		OverlapSeconds *int64 `json:"overlap_seconds"`
	}
	if r.ContentLength != 0 {
		if err := api.ReadJSON(r, &req); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
	}
	overlap := APIKeyRotationOverlap
	if req.OverlapSeconds != nil {
		overlap = time.Duration(*req.OverlapSeconds) * time.Second
		if overlap < 0 || overlap > MaxAPIKeyRotationOverlap {
			api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", "overlap_seconds must be between 0 and 604800")
			return
		}
	}
	rotated, err := h.keys.Rotate(r.Context(), p.OrganizationID, chi.URLParam(r, "id"), time.Now().UTC().Add(overlap))
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]any{
		"key":      CreatedAPIKey{APIKeyView: viewOf(rotated.Created), Key: rotated.Plaintext},
		"previous": viewOf(rotated.Previous),
	})
}

func writeAPIKeyError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrAPIKeyNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "API key not found")
		return
	}
	api.WriteRepoError(w, err)
}
