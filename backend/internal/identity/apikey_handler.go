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
	// List returns every key of the organization (quota count).
	List(ctx context.Context, orgID string) ([]StoredAPIKey, error)
	// ListPage returns a page newest first on (created_at, id), at most
	// one key more than the limit, and the number of keys (API-02).
	ListPage(ctx context.Context, orgID string, page api.PaginationParams) ([]StoredAPIKey, int, error)
	Get(ctx context.Context, orgID, id string) (*StoredAPIKey, error)
	Revoke(ctx context.Context, orgID, id string) error
	Rotate(ctx context.Context, orgID, id string, overlapUntil time.Time) (*RotatedAPIKey, error)
}

// QuotaGuard enforces a licensed quota before a resource is created
// (ENT-03): current is the number of resources the organization holds; over
// the limit it returns an error with LimitExceeded (403 entitlement-limit).
// The entitlement service implements it.
type QuotaGuard interface {
	AllowCreate(ctx context.Context, orgID, quota string, current int64) error
}

// QuotaMaxAPIKeys is the quota of the active API keys of an organization
// (entitlement.LimitMaxAPIKeys; the entitlement package imports this one).
const QuotaMaxAPIKeys = "max_api_keys"

// ServiceAccountKeyResolver resolves the service account a key is bound to
// (RBA-08): its grants and whether it exists in the organization and is
// active.
type ServiceAccountKeyResolver interface {
	ServiceAccountAccessResolver
	ServiceAccountState(ctx context.Context, orgID, id string) (exists, active bool, err error)
}

// APIKeyHandler serves /api/v1/api-keys (AUT-04, API-05). Every route needs
// apikey:manage (authz). The plaintext of a key is returned only by create
// and rotate. Rotating and revoking a foreign key and binding a key to a
// service account need the key's rights in its holder's scope
// (GrantCeiling); without the resolvers only the owner rotates and revokes
// and no key is bound (fail-closed).
type APIKeyHandler struct {
	keys            APIKeyManager
	owners          OwnerAccessResolver
	serviceAccounts ServiceAccountKeyResolver
	quota           QuotaGuard
}

// NewAPIKeyHandler creates the handler.
func NewAPIKeyHandler(keys APIKeyManager) *APIKeyHandler {
	return &APIKeyHandler{keys: keys}
}

// WithOwnerAccess resolves the current rights of the users owning keys.
func (h *APIKeyHandler) WithOwnerAccess(owners OwnerAccessResolver) *APIKeyHandler {
	h.owners = owners
	return h
}

// WithServiceAccountAccess resolves the service accounts keys are bound to.
func (h *APIKeyHandler) WithServiceAccountAccess(r ServiceAccountKeyResolver) *APIKeyHandler {
	h.serviceAccounts = r
	return h
}

// WithQuota enforces max_api_keys on create (ENT-03); a rotation replaces
// a key and is no new one.
func (h *APIKeyHandler) WithQuota(q QuotaGuard) *APIKeyHandler {
	h.quota = q
	return h
}

// allowNewKey checks max_api_keys against the organization's active keys
// (not revoked, not expired) and writes the refusal.
func (h *APIKeyHandler) allowNewKey(w http.ResponseWriter, r *http.Request, orgID string) bool {
	if h.quota == nil {
		return true
	}
	keys, err := h.keys.List(r.Context(), orgID)
	if err != nil {
		api.WriteRepoError(w, err)
		return false
	}
	now := time.Now()
	var active int64
	for i := range keys {
		if keys[i].RevokedAt == nil && (keys[i].ExpiresAt == nil || keys[i].ExpiresAt.After(now)) {
			active++
		}
	}
	if err = h.quota.AllowCreate(r.Context(), orgID, QuotaMaxAPIKeys, active); err != nil {
		if !api.WriteEntitlementError(w, err) {
			api.WriteRepoError(w, err)
		}
		return false
	}
	return true
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
	// ServiceAccountID is set for keys bound to a service account (RBA-08).
	ServiceAccountID *string    `json:"service_account_id,omitempty"`
	RotatedFrom      *string    `json:"rotated_from,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
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
		OwnerID: k.CreatedBy, ServiceAccountID: k.ServiceAccountID, RotatedFrom: k.RotatedFrom, ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
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
		// ServiceAccountID binds the key to a service account (RBA-08).
		ServiceAccountID *string `json:"service_account_id"`
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
	if req.ServiceAccountID != nil && strings.TrimSpace(*req.ServiceAccountID) == "" {
		req.ServiceAccountID = nil
	}
	if req.ServiceAccountID != nil && !h.authorizeBinding(w, r, &p, *req.ServiceAccountID, req.Permissions) {
		return
	}
	if !h.allowNewKey(w, r, p.OrganizationID) {
		return
	}
	generated, err := GenerateAPIKey(req.Environment)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
		return
	}
	created, err := h.keys.Create(r.Context(), &StoredAPIKey{
		OrganizationID: p.OrganizationID, Name: req.Name, KeyHash: generated.KeyHash, KeyPrefix: generated.KeyPrefix,
		Environment: generated.Environment, Permissions: req.Permissions, CreatedBy: p.Subject, ExpiresAt: req.ExpiresAt,
		ServiceAccountID: req.ServiceAccountID,
	})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, CreatedAPIKey{APIKeyView: viewOf(created), Key: generated.Plaintext})
}

// List returns a page of the organization's keys without secrets, newest
// first on (created_at, id): limit (default 50, at most 200) and the signed
// cursor of API-02 (F-215).
func (h *APIKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	keys, total, err := h.keys.ListPage(r.Context(), p.OrganizationID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	views := make([]APIKeyView, len(keys))
	for i := range keys {
		views[i] = viewOf(&keys[i])
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(views, total, page, func(v *APIKeyView) (time.Time, string) {
		return v.CreatedAt, v.ID
	}))
}

// Revoke ends a key at once.
func (h *APIKeyHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}
	id := chi.URLParam(r, "id")
	if !h.authorizeKeyChange(w, r, &p, id) {
		return
	}
	if err := h.keys.Revoke(r.Context(), p.OrganizationID, id); err != nil {
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
	id := chi.URLParam(r, "id")
	if !h.authorizeKeyChange(w, r, &p, id) {
		return
	}
	rotated, err := h.keys.Rotate(r.Context(), p.OrganizationID, id, time.Now().UTC().Add(overlap))
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]any{
		"key":      CreatedAPIKey{APIKeyView: viewOf(rotated.Created), Key: rotated.Plaintext},
		"previous": viewOf(rotated.Previous),
	})
}

// authorizeBinding allows binding a key with perms to a service account only
// if the account exists in the organization (404 otherwise) and the caller
// holds what the key would grant, the intersection of perms and the
// account's grants, in the account's scope (RBA-08, F-007). The rights of an
// inactive account are unknown until it is reactivated, so perms are then
// required org-wide.
func (h *APIKeyHandler) authorizeBinding(w http.ResponseWriter, r *http.Request, p *Principal, serviceAccountID string, perms []Permission) bool {
	if h.serviceAccounts == nil {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "API keys cannot be bound to service accounts here")
		return false
	}
	exists, active, err := h.serviceAccounts.ServiceAccountState(r.Context(), p.OrganizationID, serviceAccountID)
	if err != nil {
		api.WriteRepoError(w, err)
		return false
	}
	if !exists {
		api.WriteError(w, http.StatusNotFound, "Not Found", "service account not found")
		return false
	}
	target := []Grant{OrgWideGrant(perms)}
	if active {
		grants, grantErr := h.serviceAccounts.ServiceAccountGrants(r.Context(), p.OrganizationID, serviceAccountID)
		if grantErr != nil {
			api.WriteRepoError(w, grantErr)
			return false
		}
		target = restrictGrants(grants, perms)
	}
	if missing := GrantCeiling(p, target); len(missing) > 0 {
		api.WriteError(w, http.StatusForbidden, "Forbidden",
			"binding the key to the service account would grant "+joinPermissions(missing)+" beyond the caller's scope")
		return false
	}
	return true
}

// authorizeKeyChange allows rotating or revoking the key id only to its
// owner (the creating user of a key not bound to a service account) or to a
// caller holding every key permission in at least the scope in which the
// key's holder (owner or service account) holds it (AUT-04, F-006). A
// rotation hands out the successor's plaintext, so anything less would let
// the caller act with the holder's rights. A foreign or unknown key is 404.
func (h *APIKeyHandler) authorizeKeyChange(w http.ResponseWriter, r *http.Request, p *Principal, id string) bool {
	key, err := h.keys.Get(r.Context(), p.OrganizationID, id)
	if err != nil {
		writeAPIKeyError(w, err)
		return false
	}
	if key.ServiceAccountID == nil && key.CreatedBy == p.Subject {
		return true
	}
	grants, resolved, err := h.holderGrants(r.Context(), key)
	if err != nil {
		api.WriteRepoError(w, err)
		return false
	}
	if !resolved {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "only the owner may rotate or revoke this API key")
		return false
	}
	missing := GrantCeiling(p, restrictGrants(grants, key.Permissions))
	for _, perm := range key.Permissions {
		// A permission the holder lost may return with its roles; the
		// caller must hold it as well.
		if !p.Has(perm) && !containsPermission(missing, perm) {
			missing = append(missing, perm)
		}
	}
	if len(missing) > 0 {
		api.WriteError(w, http.StatusForbidden, "Forbidden",
			"rotating or revoking this API key requires "+joinPermissions(missing)+" in the scope of its holder")
		return false
	}
	return true
}

// holderGrants returns the grants of the key's holder; resolved is false
// when the handler has no resolver for it.
func (h *APIKeyHandler) holderGrants(ctx context.Context, key *StoredAPIKey) (grants []Grant, resolved bool, err error) {
	if key.ServiceAccountID == nil {
		if h.owners == nil {
			return nil, false, nil
		}
		grants, err = h.owners.AccessGrants(ctx, key.OrganizationID, key.CreatedBy)
		return grants, err == nil, err
	}
	if h.serviceAccounts == nil {
		return nil, false, nil
	}
	exists, active, err := h.serviceAccounts.ServiceAccountState(ctx, key.OrganizationID, *key.ServiceAccountID)
	if err != nil {
		return nil, false, err
	}
	if !exists || !active {
		// Unknown rights: the key's permissions are required org-wide.
		return []Grant{OrgWideGrant(key.Permissions)}, true, nil
	}
	grants, err = h.serviceAccounts.ServiceAccountGrants(ctx, key.OrganizationID, *key.ServiceAccountID)
	return grants, err == nil, err
}

// restrictGrants limits every grant to perms: what a key with perms acts
// with when its holder has the grants.
func restrictGrants(grants []Grant, perms []Permission) []Grant {
	out := make([]Grant, 0, len(grants))
	for _, g := range grants {
		if held := intersectPermissions(perms, g.Permissions); len(held) > 0 {
			g.Permissions = held
			out = append(out, g)
		}
	}
	return out
}

func containsPermission(perms []Permission, p Permission) bool {
	for _, q := range perms {
		if q == p {
			return true
		}
	}
	return false
}

func joinPermissions(perms []Permission) string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}
	return strings.Join(out, ", ")
}

func writeAPIKeyError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrAPIKeyNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "API key not found")
		return
	}
	api.WriteRepoError(w, err)
}
