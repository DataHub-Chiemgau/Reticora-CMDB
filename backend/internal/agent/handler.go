package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// MetricStore is the metric ingest surface the telemetry handler writes to.
type MetricStore interface {
	Ingest(ctx context.Context, metrics []monitoring.Metric) error
}

// CIUpserter reconciles the endpoint into a CI (source "agent").
type CIUpserter interface {
	List(ctx context.Context, orgID string, filter ci.FilterParams, page api.PaginationParams) ([]ci.Item, int, error)
	Create(ctx context.Context, item *ci.Item) error
	Update(ctx context.Context, orgID, id string, req ci.UpdateRequest) (*ci.Item, error)
}

// CITypeResolver resolves a CI type key (server/client) to its UUID.
type CITypeResolver interface {
	LookupCITypeID(ctx context.Context, orgID, nameOrID string) (string, error)
}

// FindingRecorder records a security finding for a CI (patch posture feed).
type FindingRecorder interface {
	Create(ctx context.Context, f *security.Finding) error
}

// Handler provides HTTP handlers for the endpoint-agent surface.
type Handler struct {
	repo        Repository
	metrics     MetricStore
	cis         CIUpserter
	typeResolve CITypeResolver
	findings    FindingRecorder
	tokens      TokenIssuer
}

// TokenIssuer signs the agent credential (implemented by
// identity.SessionIssuer).
type TokenIssuer interface {
	Issue(claims identity.SessionClaims) (string, error)
}

// AgentTokenLifetime is the validity of an agent credential; an agent is
// re-enrolled to renew it, and the kill-switch disables it at any time.
const AgentTokenLifetime = 365 * 24 * time.Hour

// agentSubjectPrefix marks the session subject of an agent credential.
const agentSubjectPrefix = "agent:"

// AgentSubject is the session subject of the agent's credential.
func AgentSubject(agentID string) string { return agentSubjectPrefix + agentID }

// WithAgentTokens makes enrollment return a signed agent credential.
func (h *Handler) WithAgentTokens(issuer TokenIssuer) *Handler {
	h.tokens = issuer
	return h
}

// issueAgentToken signs the credential of an enrolled agent: subject
// agent:<agent_id>, permission agent:ingest only, scope the agent's client.
func (h *Handler) issueAgentToken(a *Agent) (string, error) {
	scope := identity.Scope{
		Clients: identity.ScopeSet{All: true},
		Sites:   identity.ScopeSet{All: true},
		Teams:   identity.ScopeSet{All: true},
	}
	if a.ClientID != "" {
		scope.Clients = identity.ScopeSet{IDs: []string{a.ClientID}}
	}
	now := time.Now().UTC()
	return h.tokens.Issue(identity.SessionClaims{
		Subject:        AgentSubject(a.AgentID),
		OrganizationID: a.OrganizationID,
		ClientScope:    scope.LegacyClientScope(),
		Permissions:    []identity.Permission{identity.PermAgentIngest},
		Scope:          &scope,
		IssuedAt:       now,
		ExpiresAt:      now.Add(AgentTokenLifetime),
	})
}

// NewHandler creates a new agent handler. metrics, cis and findings may be
// nil in --no-db smoke tests; telemetry then only registers heartbeats.
func NewHandler(repo Repository, metrics MetricStore, cis CIUpserter, typeResolve ...CITypeResolver) *Handler {
	h := &Handler{repo: repo, metrics: metrics, cis: cis}
	if len(typeResolve) > 0 {
		h.typeResolve = typeResolve[0]
	}
	return h
}

// WithFindings attaches the patch-posture finding recorder (E5).
func (h *Handler) WithFindings(f FindingRecorder) *Handler {
	h.findings = f
	return h
}

// RegisterRoutes registers agent routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/agents", h.List)
	r.Post("/api/v1/agents/enroll", h.Enroll)
	r.Post("/api/v1/agents/enrollment-tokens", h.CreateEnrollmentToken)
	r.Post("/api/v1/agents/{id}/site", h.ConfirmSite)
	r.Post("/api/v1/agents/telemetry", h.IngestTelemetry)
	r.Post("/api/v1/agents/{id}/heartbeat", h.Heartbeat)
	r.Patch("/api/v1/agents/{id}/policy", h.UpdatePolicy)
	r.Post("/api/v1/agents/{id}/disable", h.Disable)
	r.Post("/api/v1/agents/{id}/enable", h.Enable)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Agent]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Enroll handles POST /api/v1/agents/enroll — registers an endpoint agent
// with a single-use enrollment token (AGT-06). The token binds the agent to
// its client and, unless it is a roaming token, its site; for roaming agents
// a site is suggested from the network fingerprint.
func (h *Handler) Enroll(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req EnrollRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.AgentID) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "agent_id is required")
		return
	}
	if strings.TrimSpace(req.EnrollmentToken) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "enrollment_token is required")
		return
	}
	a := &Agent{
		OrganizationID: t.OrganizationID,
		AgentID:        req.AgentID,
		Hostname:       req.Hostname,
		Version:        req.Version,
		OS:             req.OS,
		Arch:           req.Arch,
		Policy:         DefaultPolicy(),
	}
	if err := h.repo.Enroll(r.Context(), a, hashEnrollmentToken(req.EnrollmentToken), req.IPAddress); err != nil {
		if errors.Is(err, ErrInvalidEnrollmentToken) {
			api.WriteError(w, http.StatusForbidden, "Forbidden", err.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	if h.tokens != nil {
		token, err := h.issueAgentToken(a)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "Internal Error", "issue agent credential")
			return
		}
		a.Token = token
	}
	api.WriteJSON(w, http.StatusCreated, a)
}

// maxEnrollmentTokenTTL caps the lifetime of an enrollment token.
const maxEnrollmentTokenTTL = 30 * 24 * time.Hour

// CreateEnrollmentToken handles POST /api/v1/agents/enrollment-tokens. The
// secret is returned once and stored only as a hash.
func (h *Handler) CreateEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CreateEnrollmentTokenRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.ClientID) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "client_id is required")
		return
	}
	ttl := 24 * time.Hour
	if req.TTLHours > 0 {
		ttl = time.Duration(req.TTLHours) * time.Hour
	}
	if ttl > maxEnrollmentTokenTTL {
		ttl = maxEnrollmentTokenTTL
	}
	secret, err := newEnrollmentSecret()
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "could not create a token")
		return
	}
	tok := &EnrollmentToken{
		ClientID:    req.ClientID,
		SiteID:      req.SiteID,
		Description: req.Description,
		ExpiresAt:   time.Now().UTC().Add(ttl),
	}
	if err := h.repo.CreateEnrollmentToken(r.Context(), t.OrganizationID, tok, hashEnrollmentToken(secret)); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	tok.Token = secret
	api.WriteJSON(w, http.StatusCreated, tok)
}

// ConfirmSite handles POST /api/v1/agents/{id}/site — the manual confirmation
// of a roaming agent's site (AGT-06).
func (h *Handler) ConfirmSite(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req ConfirmSiteRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.SiteID) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "site_id is required")
		return
	}
	a, err := h.repo.ConfirmSite(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req.SiteID)
	if err != nil {
		if errors.Is(err, ErrSiteNotOfClient) {
			api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, a)
}

// newEnrollmentSecret returns a random token secret.
func newEnrollmentSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "agt_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashEnrollmentToken is the stored form of a token secret.
func hashEnrollmentToken(secret string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return hex.EncodeToString(sum[:])
}

// IngestTelemetry handles POST /api/v1/agents/telemetry. It reconciles the
// endpoint into a CI (source "agent") and writes the health metrics into the
// time-series store. The software inventory lands in the CI attributes for
// patch posture (§9.4).
func (h *Handler) IngestTelemetry(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var payload TelemetryPayload
	if err := api.ReadJSON(r, &payload); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(payload.AgentID) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "agent_id is required")
		return
	}
	// An agent credential reports for its own agent only.
	if principal, ok := identity.PrincipalFromContext(r.Context()); ok && strings.HasPrefix(principal.Subject, agentSubjectPrefix) &&
		principal.Subject != AgentSubject(payload.AgentID) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "agent credential does not match agent_id")
		return
	}

	// Resolve the registered agent and its linked CI.
	ag, err := h.repo.GetByAgentID(r.Context(), t.OrganizationID, payload.AgentID)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "agent not enrolled")
		return
	}
	if ag.Status == "disabled" {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "agent is disabled (kill-switch)")
		return
	}

	ciID, err := h.reconcileCI(r.Context(), t.OrganizationID, ag, payload)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if ciID != "" && ag.CIID != ciID {
		_ = h.repo.SetCI(r.Context(), t.OrganizationID, ag.AgentID, ciID)
	}
	// A roaming device reports where it is; a differing site is only
	// suggested and needs a manual confirmation (AGT-06).
	if strings.TrimSpace(payload.IPAddress) != "" && payload.IPAddress != ag.NetworkFingerprint {
		if err := h.repo.SuggestSite(r.Context(), t.OrganizationID, ag.AgentID, payload.IPAddress); err != nil {
			slog.Warn("agent site suggestion failed", "agent", ag.AgentID, "error", err)
		}
	}
	_ = h.repo.Heartbeat(r.Context(), t.OrganizationID, ag.AgentID)

	// Patch posture (§9.4): software inventory entries carrying a known-vulnerable
	// version marker produce a security finding attached to the endpoint CI.
	if h.findings != nil && ciID != "" && ag.Policy.InventoryEnabled {
		h.recordFindings(r.Context(), t.OrganizationID, ciID, payload.Software)
	}

	// Write health metrics into the time-series store.
	if h.metrics != nil && len(payload.Metrics) > 0 && ag.Policy.MetricsEnabled {
		now := time.Now().UTC()
		if !payload.CollectedAt.IsZero() {
			now = payload.CollectedAt.UTC()
		}
		metrics := make([]monitoring.Metric, 0, len(payload.Metrics))
		for name, value := range payload.Metrics {
			metrics = append(metrics, monitoring.Metric{
				OrgID:     t.OrganizationID,
				CIID:      ciID,
				Name:      name,
				Value:     value,
				Timestamp: now,
				Labels:    map[string]string{"agent_id": ag.AgentID, "hostname": ag.Hostname},
			})
		}
		if err := h.metrics.Ingest(r.Context(), metrics); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}

	api.WriteJSON(w, http.StatusAccepted, map[string]any{"ci_id": ciID, "metrics_ingested": len(payload.Metrics)})
}

// reconcileCI finds or creates the endpoint CI for the agent (source "agent")
// and stores the software inventory in the CI attributes.
func (h *Handler) reconcileCI(ctx context.Context, orgID string, ag *Agent, payload TelemetryPayload) (string, error) {
	if h.cis == nil {
		return "", nil
	}
	// Match an existing endpoint CI by hostname (the agent's stable identity).
	existing, _, err := h.cis.List(ctx, orgID, ci.FilterParams{Search: ag.Hostname}, api.PaginationParams{Limit: 50})
	if err != nil {
		return "", err
	}
	attrs := map[string]any{
		"agent_id":    ag.AgentID,
		"os":          payload.OS,
		"arch":        payload.Arch,
		"agent_version": payload.Version,
	}
	if payload.SystemInfo != nil {
		attrs["system_info"] = payload.SystemInfo
	}
	if len(payload.Software) > 0 && ag.Policy.InventoryEnabled {
		attrs["software_inventory"] = payload.Software
	}
	for _, item := range existing {
		if strings.EqualFold(item.Hostname, ag.Hostname) || strings.EqualFold(item.Name, ag.Hostname) {
			updated, err := h.cis.Update(ctx, orgID, item.ID, ci.UpdateRequest{Attributes: attrs})
			if err != nil {
				return "", err
			}
			return updated.ID, nil
		}
	}
	// No match: create the endpoint CI. The ci_type key resolves to the UUID
	// via the tenant-aware type lookup (global system types included).
	typeID := ""
	if h.typeResolve != nil {
		id, err := h.typeResolve.LookupCITypeID(ctx, orgID, endpointCITypeKey(payload.OS))
		if err == nil {
			typeID = id
		}
	}
	now := time.Now().UTC()
	item := &ci.Item{
		OrganizationID:  orgID,
		CITypeID:        typeID,
		Name:            ag.Hostname,
		Hostname:        ag.Hostname,
		Status:          "active",
		Attributes:      attrs,
		DiscoverySource: ci.SourceAgent,
		FirstSeenAt:     &now,
		LastSeenAt:      &now,
	}
	if err := h.cis.Create(ctx, item); err != nil {
		return "", err
	}
	return item.ID, nil
}

// recordFindings turns software inventory entries that are marked vulnerable
// (the feed matcher tags them with a "vulnerable" vendor flag) into security
// findings attached to the endpoint CI.
func (h *Handler) recordFindings(ctx context.Context, orgID, ciID string, software []SoftwareItem) {
	for _, sw := range software {
		if !isVulnerable(sw) {
			continue
		}
		_ = h.findings.Create(ctx, &security.Finding{
			OrganizationID:   orgID,
			CIID:             ciID,
			Kind:             "vulnerability",
			Severity:         "high",
			Title:            "Vulnerable software: " + sw.Name + " " + sw.Version,
			PackageName:      sw.Name,
			InstalledVersion: sw.Version,
			Reference:        sw.Vendor,
		})
	}
}

// isVulnerable reports whether an inventory entry is flagged vulnerable by the
// version/CVE feed (the feed matcher encodes this as vendor="vuln" or a
// "CVE-"-prefixed vendor field carrying the advisory reference).
func isVulnerable(sw SoftwareItem) bool {
	v := strings.ToLower(sw.Vendor)
	return v == "vuln" || strings.HasPrefix(strings.ToUpper(sw.Vendor), "CVE-")
}

// endpointCITypeKey maps the agent OS to the endpoint CI type key (server vs
// client); the resolver turns it into the canonical UUID.
func endpointCITypeKey(os string) string {
	if strings.Contains(strings.ToLower(os), "server") {
		return "server"
	}
	return "client"
}

// Heartbeat handles POST /api/v1/agents/{id}/heartbeat.
func (h *Handler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if err := h.repo.Heartbeat(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "agent not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdatePolicy handles PATCH /api/v1/agents/{id}/policy.
func (h *Handler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdatePolicyRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	a, err := h.repo.UpdatePolicy(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "agent not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, a)
}

// Disable handles POST /api/v1/agents/{id}/disable — the kill-switch.
func (h *Handler) Disable(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, "disabled")
}

// Enable handles POST /api/v1/agents/{id}/enable — re-enables a disabled agent.
func (h *Handler) Enable(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, "online")
}

func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request, status string) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	a, err := h.repo.SetStatus(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), status)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "agent not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, a)
}
