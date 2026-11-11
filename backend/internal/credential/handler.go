package credential

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for credential management.
type Handler struct {
	svc *Service
	// pool writes credential.rotated into the outbox in the transaction of
	// a replaced secret (WP-889); nil (--no-db) writes no event.
	pool *pgxpool.Pool
	// vaultJobs and vaultQueue run the connection tests vault.test
	// (WP-896): on the job queue when there is one, else in this process;
	// without either the test route answers 503.
	vaultJobs  VaultTestJobs
	vaultQueue VaultTestQueue
}

// WithEvents lets PUT /credentials/{id}/secret append credential.rotated
// to the outbox of pool.
func (h *Handler) WithEvents(pool *pgxpool.Pool) *Handler {
	h.pool = pool
	return h
}

// NewHandler creates a new credential HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts credential routes on the given router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/v1/credentials", func(r chi.Router) {
		r.Post("/", h.create)
		r.Get("/", h.list)
		r.Post("/rotate-keys", h.rotateKeys)
		r.Get("/{id}", h.get)
		r.Patch("/{id}", h.update)
		r.Put("/{id}/secret", h.replaceSecret)
		// There is deliberately no decrypt route: plaintext secrets never
		// leave the server through the API (SEC-01, COL-02). They are only
		// decrypted in server memory for delivery to collectors and
		// connectors (Service.Decrypt).
		r.Delete("/{id}", h.delete)
	})
	h.registerBindingRoutes(r)
	h.registerVaultRoutes(r)
}

// organizationID resolves the tenant from the authenticated request context.
// Credentials hold decryptable secret material, so the organization must never
// be taken from client-supplied input such as a header or the request body —
// that would let any authenticated caller read another tenant's secrets.
func organizationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := tenant.FromContext(r.Context()).OrganizationID
	if orgID == "" {
		writeError(w, http.StatusUnauthorized, "missing tenant context")
		return "", false
	}
	return orgID, true
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}

	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.OrganizationID = orgID

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.MaxAgeDays != nil && !MaxAgeDaysValid(*req.MaxAgeDays) {
		writeFieldError(w, "max_age_days", "max_age_days must be an integer from 1 to 3650")
		return
	}

	cred, err := h.svc.Create(r.Context(), req)
	if errors.Is(err, ErrInvalidKind) {
		api.WriteFieldError(w, &api.FieldError{Field: "kind", Message: "unknown credential kind", Status: http.StatusUnprocessableEntity})
		return
	}
	if err != nil {
		// A secret without the members of its kind (422 secret), a client outside the scope (403), an unknown client (422) or a
		// value the database refuses (400) through the sanitized
		// contract (API-03, F-390); anything else 500.
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, cred)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}

	// {data, next_cursor} with limit and the signed cursor (API-02, F-075).
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	creds, err := h.svc.List(r.Context(), orgID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.Page(creds, page))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")

	cred, err := h.svc.Get(r.Context(), orgID, id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, cred)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")

	// A bound credential is not deleted (WP-890): 409 with its bindings,
	// also without the foreign key of the database.
	if h.refuseBoundDeletion(w, r, orgID, id) {
		return
	}
	err := h.svc.Delete(r.Context(), orgID, id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if errors.Is(err, ErrCredentialIsVaultLogin) {
		api.WriteFieldError(w, &api.ConflictError{Field: "id",
			Message: "a vault connection logs in with the credential; change or delete the connection first"})
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// immutableFields are the members a PATCH may not send (WP-889): kind and
// scope define how the collectors use the secret, client_id is checked
// only when a scope is stored (discovery checkCredentials), and the
// secret is replaced only through PUT /credentials/{id}/secret.
var immutableFields = []string{"kind", "client_id", "scope", "secret", "source", "vault_ref", "vault_connection_id"}

// update handles PATCH /api/v1/credentials/{id}: name and max_age_days
// (null removes the limit); any other member is 422 with its field.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var u MetadataUpdate
	for field, raw := range body {
		switch field {
		case "name":
			var name string
			if err := json.Unmarshal(raw, &name); err != nil || strings.TrimSpace(name) == "" {
				writeFieldError(w, "name", "name must be a non-empty string")
				return
			}
			u.Name = &name
		case "max_age_days":
			u.SetMaxAge = true
			if string(raw) == "null" {
				continue
			}
			var days int
			if err := json.Unmarshal(raw, &days); err != nil || !MaxAgeDaysValid(days) {
				writeFieldError(w, "max_age_days", "max_age_days must be an integer from 1 to 3650 or null")
				return
			}
			u.MaxAgeDays = &days
		case "vault_path":
			var path string
			if err := json.Unmarshal(raw, &path); err != nil {
				writeFieldError(w, "vault_path", "vault_path must be a string")
				return
			}
			u.Ref.Path = &path
		case "vault_version":
			u.Ref.SetVersion = true
			if string(raw) == "null" {
				continue
			}
			var version int
			if err := json.Unmarshal(raw, &version); err != nil {
				writeFieldError(w, "vault_version", "vault_version must be an integer or null")
				return
			}
			u.Ref.Version = &version
		case "field_map":
			fieldMap := map[string]string{}
			if err := json.Unmarshal(raw, &fieldMap); err != nil {
				writeFieldError(w, "field_map", "field_map must map field names to vault field names")
				return
			}
			u.Ref.FieldMap = fieldMap
		default:
			if slices.Contains(immutableFields, field) {
				writeFieldError(w, field, field+" cannot be changed; the secret is replaced with PUT /credentials/{id}/secret")
				return
			}
			writeFieldError(w, field, "unknown field")
			return
		}
	}
	cred, err := h.svc.UpdateMetadata(r.Context(), orgID, chi.URLParam(r, "id"), u)
	h.writeCredential(w, cred, err)
}

// replaceSecret handles PUT /api/v1/credentials/{id}/secret (CRD-05,
// WP-889): the new secret is encrypted with the current DEK, secret_version
// rises and rotated_at is set in one transaction, which also appends
// credential.rotated as its last write. The answer carries the metadata,
// never the secret.
func (h *Handler) replaceSecret(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	var req struct {
		Secret map[string]any `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Secret) == 0 {
		writeFieldError(w, "secret", "secret must not be empty")
		return
	}
	cred, err := h.svc.ReplaceSecret(r.Context(), orgID, chi.URLParam(r, "id"), req.Secret, h.emitRotated(orgID))
	h.writeCredential(w, cred, err)
}

// emitRotated returns the in-transaction step of a replacement: it appends
// credential.rotated as the last write before the commit (Konvention 20).
func (h *Handler) emitRotated(orgID string) func(ctx context.Context, cred *Credential) error {
	if h.pool == nil {
		return nil
	}
	return func(ctx context.Context, cred *Credential) error {
		data := events.CredentialEventData{ID: cred.ID, ClientID: cred.ClientID, Kind: cred.Kind,
			SecretVersion: cred.SecretVersion, Source: "stored"}
		return database.WithRequestTenant(ctx, h.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			return database.BeforeCommit(ctx, func() error {
				_, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.CredentialRotated,
					EntityID: cred.ID, Data: data})
				return err
			})
		})
	}
}

// writeCredential answers a change of a credential: 404 for an unknown or
// foreign id, else the metadata.
func (h *Handler) writeCredential(w http.ResponseWriter, cred *Credential, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "credential not found")
	case errors.Is(err, ErrVaultRefHasNoSecret):
		api.WriteFieldError(w, &api.ConflictError{Field: "secret",
			Message: "the credential is a vault reference; change vault_path, vault_version or field_map instead"})
	case errors.Is(err, ErrUnsupported):
		writeError(w, http.StatusServiceUnavailable, "credential changes are not available")
	case err != nil:
		api.WriteRepoError(w, err)
	default:
		api.WriteJSON(w, http.StatusOK, cred)
	}
}

// writeFieldError answers 422 with the field.
func writeFieldError(w http.ResponseWriter, field, message string) {
	api.WriteFieldError(w, &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity})
}

// rotateKeys handles POST /api/v1/credentials/rotate-keys: the organization
// DEK is rotated and every stored credential ciphertext is re-encrypted.
// Rotating regularly limits the blast radius of a key compromise and is a
// standard ISO 27001 (A.10.1.2) / NIS2 cryptographic control.
func (h *Handler) rotateKeys(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}

	result, err := h.svc.RotateKeys(r.Context(), orgID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "no encryption key material exists for this tenant yet")
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, result)
}

// writeError writes an RFC 7807 problem response.
func writeError(w http.ResponseWriter, status int, detail string) {
	api.WriteProblemBody(w, status, api.ProblemDetail{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
}
