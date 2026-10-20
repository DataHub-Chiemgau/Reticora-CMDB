package operator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler serves /api/v1/admin.
type Handler struct {
	cfg      Config
	oidc     *identity.OIDCProvider
	sessions *identity.SessionIssuer
	audit    *Auditor
	pool     *pgxpool.Pool
	// entitlements is written here only (ENT-04).
	entitlements *entitlement.Service
	// orgs creates organizations (TLC-02).
	orgs OrgCreator
}

// NewHandler creates the operator handler. pool serves the organization
// listing (system read); oidc and sessions the operator login.
func NewHandler(cfg Config, oidc *identity.OIDCProvider, sessions *identity.SessionIssuer, audit *Auditor, pool *pgxpool.Pool) *Handler {
	return &Handler{cfg: cfg, oidc: oidc, sessions: sessions, audit: audit, pool: pool}
}

// RegisterRoutes registers the operator routes. Everything except the login
// runs behind Middleware.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/admin/auth/callback", h.Callback)
	r.With(h.Middleware).Get("/api/v1/admin/me", h.Me)
	r.With(h.Middleware).Get("/api/v1/admin/orgs", h.ListOrgs)
	r.With(h.Middleware).Post("/api/v1/admin/orgs", h.CreateOrg)
	r.With(h.Middleware).Get("/api/v1/admin/audit", h.ListAudit)
	r.With(h.Middleware).Get("/api/v1/admin/audit/verify", h.VerifyAudit)
	r.With(h.Middleware).Get("/api/v1/admin/orgs/{id}/entitlements", h.ListEntitlements)
	r.With(h.Middleware).Post("/api/v1/admin/orgs/{id}/entitlements", h.GrantEntitlement)
}

var errNotOperator = errors.New("operator: login is not an operator with MFA")

// Callback exchanges an OIDC authorization code (PKCE) for an operator
// session. Only members of the operator group who logged in with a second
// factor get one (SEC-07, CH26); every attempt is audited.
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code         string `json:"code"`
		State        string `json:"state"`
		CodeVerifier string `json:"code_verifier"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.State) == "" || strings.TrimSpace(req.CodeVerifier) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "code, state and code_verifier are required")
		return
	}
	if h.oidc == nil || h.sessions == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "operator login is not configured")
		return
	}
	tokens, err := h.oidc.ExchangeCodeWithVerifier(r.Context(), req.Code, req.CodeVerifier)
	if err != nil {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "authorization code exchange failed")
		return
	}
	id, err := h.oidc.ValidateIDToken(r.Context(), tokens.IDToken)
	if err != nil {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "invalid ID token")
		return
	}
	token, expires, err := h.issue(id)
	if err != nil {
		status, detail := http.StatusForbidden, errNotOperator.Error()
		if !errors.Is(err, errNotOperator) {
			status, detail = http.StatusInternalServerError, "operator session could not be issued"
		}
		// The audit names the failed condition and the acr of the login;
		// the response stays generic.
		h.record(r.Context(), &AuditEntry{OperatorID: id.Subject, OperatorKind: KindOIDC, Action: "login",
			Status: status, Details: map[string]any{"reason": err.Error(), "acr": id.ACR, "remote": r.RemoteAddr}})
		api.WriteError(w, status, http.StatusText(status), detail)
		return
	}
	h.record(r.Context(), &AuditEntry{OperatorID: id.Subject, OperatorKind: KindOIDC, Action: "login",
		Status: http.StatusOK, Details: map[string]any{"remote": r.RemoteAddr, "email": id.Email, "acr": id.ACR}})
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": expires.Format(time.RFC3339),
		"operator":   Identity{ID: id.Subject, Name: id.Name, Email: id.Email, Kind: KindOIDC},
	})
}

// issue signs an operator session for a verified operator login: a member
// of the operator group (claim groups without full path, realm group
// operators) whose login used a second factor (amr, or acr 2 of the realm
// flow browser-mfa, requested with acr_values=2).
func (h *Handler) issue(id *identity.IDTokenClaims) (string, time.Time, error) {
	if !slices.Contains(id.Groups, h.cfg.group()) {
		return "", time.Time{}, fmt.Errorf("%w: not a member of the group %q", errNotOperator, h.cfg.group())
	}
	if !h.cfg.MFASatisfied(id.AMR, id.ACR) {
		return "", time.Time{}, fmt.Errorf("%w: no second factor (acr %q)", errNotOperator, id.ACR)
	}
	now := time.Now().UTC()
	claims := identity.SessionClaims{
		Subject: id.Subject, Name: id.Name, Email: id.Email, Operator: true,
		Permissions: []identity.Permission{}, IssuedAt: now, ExpiresAt: now.Add(SessionLifetime),
	}
	jti, err := newTokenID()
	if err != nil {
		return "", time.Time{}, err
	}
	claims.ID = jti
	token, err := h.sessions.Issue(claims)
	return token, claims.ExpiresAt, err
}

// Me returns the operator of the request.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	op, _ := FromContext(r.Context())
	api.WriteJSON(w, http.StatusOK, op)
}

// OrgSummary is an organization as the operator listing shows it.
type OrgSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Plan      string    `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
}

// ListOrgs lists every organization (system read, E-08).
func (h *Handler) ListOrgs(w http.ResponseWriter, r *http.Request) {
	orgs := []OrgSummary{}
	err := database.WithSystem(r.Context(), h.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, name, slug, plan, created_at FROM organization ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o OrgSummary
			if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.Plan, &o.CreatedAt); err != nil {
				return err
			}
			orgs = append(orgs, o)
		}
		return rows.Err()
	})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[OrgSummary]{Data: orgs, Total: len(orgs), Limit: len(orgs)})
}

// ListAudit lists operator_audit, newest first.
func (h *Handler) ListAudit(w http.ResponseWriter, r *http.Request) {
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	entries, total, err := h.audit.List(r.Context(), page.Limit, page.Offset)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if entries == nil {
		entries = []AuditEntry{}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[AuditEntry]{Data: entries, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+len(entries) < total})
}

// VerifyAudit recomputes the operator audit hash chain.
func (h *Handler) VerifyAudit(w http.ResponseWriter, r *http.Request) {
	result, err := h.audit.Verify(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, result)
}
