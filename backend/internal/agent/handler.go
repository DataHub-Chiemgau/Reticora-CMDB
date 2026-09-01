package agent

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
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

// Handler provides HTTP handlers for the endpoint-agent surface.
type Handler struct {
	repo        Repository
	metrics     MetricStore
	cis         CIUpserter
	typeResolve CITypeResolver
}

// NewHandler creates a new agent handler. metrics and cis may be nil in
// --no-db smoke tests; telemetry then only registers heartbeats.
func NewHandler(repo Repository, metrics MetricStore, cis CIUpserter, typeResolve ...CITypeResolver) *Handler {
	h := &Handler{repo: repo, metrics: metrics, cis: cis}
	if len(typeResolve) > 0 {
		h.typeResolve = typeResolve[0]
	}
	return h
}

// RegisterRoutes registers agent routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/agents", h.List)
	r.Post("/api/v1/agents/enroll", h.Enroll)
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
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Agent]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Enroll handles POST /api/v1/agents/enroll — registers an endpoint agent
// (after the edge enrollment authenticated it) and returns its policy.
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
	a := &Agent{
		OrganizationID: t.OrganizationID,
		AgentID:        req.AgentID,
		Hostname:       req.Hostname,
		Version:        req.Version,
		OS:             req.OS,
		Arch:           req.Arch,
		Policy:         DefaultPolicy(),
	}
	if err := h.repo.Register(r.Context(), a); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, a)
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
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	if ciID != "" && ag.CIID != ciID {
		_ = h.repo.SetCI(r.Context(), t.OrganizationID, ag.AgentID, ciID)
	}
	_ = h.repo.Heartbeat(r.Context(), t.OrganizationID, ag.AgentID)

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
			api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
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
