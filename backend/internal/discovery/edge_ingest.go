package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The collectors' data path over the edge (COL-08): an enrolled collector
// delivers its records, heartbeats and metric samples over its mutually
// authenticated NATS channel. The edge grants a collector only
// collector.<org>.<id>.>, so the organization and the collector are those
// its certificate was authorized for; the HTTP ingest stays for API clients
// with a session or API key.
const (
	IngestSuffix    = ".ingest"
	HeartbeatSuffix = ".heartbeat"
	// MetricsSuffix carries the collector's metric samples (sweep
	// reachability, UPS values, SNMP polls and traps; MON-02).
	MetricsSuffix = ".metrics"
	// edgeDataQueue spreads the requests over the server instances: each
	// is processed once.
	edgeDataQueue = "reticora-edge-data"
)

// License states of the discovery feature as the collector reads them
// (ENT-07): "expired" pauses scanning, "active" resumes it.
const (
	LicenseActive      = "active"
	LicenseExpired     = "expired"
	LicenseNotEntitled = "not_entitled"
)

// EdgeLicense reports an organization's discovery license for the edge path:
// its state and, for a refusal, the problem type the collector reacts to.
type EdgeLicense interface {
	DiscoveryLicense(ctx context.Context, orgID string) (state, problemType string, err error)
}

// WithEdgeLicense checks the discovery license on the edge data path, as the
// entitlement middleware does on the HTTP ingest.
func (h *Handler) WithEdgeLicense(license EdgeLicense) *Handler {
	h.license = license
	return h
}

// EdgeReply answers an ingest or heartbeat request of a collector.
type EdgeReply struct {
	// Status is the HTTP status the same request would have on the API.
	Status int `json:"status"`
	// Type is the problem type of a refusal (license expired, not entitled).
	Type            string              `json:"type,omitempty"`
	Error           string              `json:"error,omitempty"`
	LicenseStatus   string              `json:"license_status,omitempty"`
	CollectorStatus string              `json:"collector_status,omitempty"`
	Result          *BulkIngestResponse `json:"result,omitempty"`
	// Ingested and Unassigned answer accepted metric samples: those stored
	// with their CI and those without a CI (POST /monitoring/metrics).
	Ingested   *int `json:"ingested,omitempty"`
	Unassigned *int `json:"unassigned,omitempty"`
}

// MetricsIngester stores the metric samples of an organization like
// POST /api/v1/monitoring/metrics (monitoring.Handler.IngestPayload): it
// assigns them to the CIs the scope of ctx sees by their target label and
// counts those without a CI; a body without valid metrics is a
// *monitoring.InvalidMetricsError.
type MetricsIngester interface {
	IngestPayload(ctx context.Context, orgID string, data []byte) (monitoring.IngestResult, error)
}

// FeatureStatus reports the entitlement of a feature (entitlement.Service).
type FeatureStatus interface {
	Status(ctx context.Context, orgID, featureKey string) (entitlement.Entitlement, entitlement.Status, error)
}

// WithEdgeMetrics answers the collectors' metric samples on the edge
// (.metrics, MON-02) with metrics, the ingest of POST /monitoring/metrics;
// features checks the monitoring entitlement as the entitlement middleware
// does on that route (nil: every organization is entitled). Without it the
// subject is not served and the collectors keep their samples.
func (h *Handler) WithEdgeMetrics(metrics MetricsIngester, features FeatureStatus) *Handler {
	h.metrics, h.features = metrics, features
	return h
}

// edgeLicense returns the license state; without a checker every
// organization counts as licensed.
func (h *Handler) edgeLicense(ctx context.Context, orgID string) (state, problemType string, err error) {
	if h.license == nil {
		return LicenseActive, "", nil
	}
	return h.license.DiscoveryLicense(ctx, orgID)
}

// checkCollector fails for a collector that may not deliver over the edge:
// api.ErrNotFound for an unknown collector, ErrCollectorDisabled for a
// disabled one (COL-04). The edge grants the subject tree when the
// collector connects; a collector disabled later still holds it.
func (h *Handler) checkCollector(ctx context.Context, orgID, collectorID string) error {
	status, err := h.repo.CollectorStatus(ctx, orgID, collectorID)
	if err != nil {
		return err
	}
	if status == CollectorDisabled {
		return ErrCollectorDisabled
	}
	return nil
}

// edgeRefusal answers a request of a collector checkCollector refused:
// 403 for a disabled, 404 for an unknown collector.
func edgeRefusal(err error) EdgeReply {
	switch {
	case errors.Is(err, ErrCollectorDisabled):
		return EdgeReply{Status: http.StatusForbidden, Error: err.Error()}
	case errors.Is(err, api.ErrNotFound):
		return EdgeReply{Status: http.StatusNotFound, Error: "collector not found"}
	}
	return EdgeReply{Status: http.StatusInternalServerError, Error: "collector lookup failed"}
}

// EdgeIngest processes the records a collector sent over the edge like
// POST /api/v1/discovery/ingest; the records of an unknown or disabled
// collector are refused.
func (h *Handler) EdgeIngest(ctx context.Context, orgID, collectorID string, data []byte) EdgeReply {
	if err := h.checkCollector(ctx, orgID, collectorID); err != nil {
		return edgeRefusal(err)
	}
	return h.edgeIngest(ctx, orgID, collectorID, data)
}

// edgeIngest is EdgeIngest for a collector the caller checked.
func (h *Handler) edgeIngest(ctx context.Context, orgID, collectorID string, data []byte) EdgeReply {
	state, problem, err := h.edgeLicense(ctx, orgID)
	if err != nil {
		return EdgeReply{Status: http.StatusInternalServerError, Error: "entitlement lookup failed"}
	}
	if state != LicenseActive {
		slog.Warn("edge ingest refused", "organization_id", orgID, "collector_id", collectorID, "license", state)
		return EdgeReply{Status: http.StatusForbidden, Type: problem, LicenseStatus: state, Error: "discovery license " + state}
	}
	var body ingestBody
	if err = json.Unmarshal(data, &body); err != nil {
		return EdgeReply{Status: http.StatusBadRequest, Error: "invalid ingest body"}
	}
	items, status, msg := body.ingestItems()
	if status != 0 {
		return EdgeReply{Status: status, Error: msg}
	}
	received := len(items)
	items, rejected := admitObservedAt(items, time.Now().UTC())
	t := tenant.TenantInfo{OrganizationID: orgID}
	ctx = tenant.WithTenant(ctx, t)
	resp, err := h.dispatch(ctx, newIngestMessages(ctx, t, collectorID, items))
	// Records that fail are listed in the reply; only when no record was
	// accepted does the request fail as a whole (WP-481).
	if err = resp.finish(received, rejected, err); err != nil {
		slog.Error("edge ingest failed", "organization_id", orgID, "collector_id", collectorID, "error", err)
		return EdgeReply{Status: http.StatusInternalServerError, Error: "ingest failed"}
	}
	return EdgeReply{Status: http.StatusAccepted, LicenseStatus: state, Result: &resp}
}

// EdgeHeartbeat records a collector heartbeat sent over the edge like
// POST /api/v1/collectors/{id}/heartbeat; the repository refuses an unknown
// or disabled collector.
func (h *Handler) EdgeHeartbeat(ctx context.Context, orgID, collectorID string, data []byte) EdgeReply {
	var req CollectorHeartbeatRequest
	if len(data) > 0 {
		if err := json.Unmarshal(data, &req); err != nil {
			return EdgeReply{Status: http.StatusBadRequest, Error: "invalid heartbeat body"}
		}
	}
	status, err := h.repo.Heartbeat(ctx, orgID, collectorID)
	if err != nil {
		code := http.StatusNotFound
		if errors.Is(err, ErrCollectorDisabled) {
			code = http.StatusForbidden
		}
		return EdgeReply{Status: code, Error: err.Error()}
	}
	recordSpoolReport(ctx, orgID, collectorID, req.Spool)
	reply := EdgeReply{Status: http.StatusNoContent, CollectorStatus: status}
	if state, _, lerr := h.edgeLicense(ctx, orgID); lerr == nil && state != LicenseNotEntitled {
		reply.LicenseStatus = state
	}
	return reply
}

// EdgeMetrics stores the metric samples a collector sent over the edge like
// POST /api/v1/monitoring/metrics (MON-02): 403 with the problem type of
// the entitlement middleware without the monitoring feature, otherwise 202
// with the stored and the unassigned samples. The samples are assigned in
// the collector's scope; the samples of an unknown or disabled collector
// are refused.
func (h *Handler) EdgeMetrics(ctx context.Context, orgID, collectorID string, data []byte) EdgeReply {
	if err := h.checkCollector(ctx, orgID, collectorID); err != nil {
		return edgeRefusal(err)
	}
	return h.edgeMetrics(ctx, orgID, collectorID, data)
}

// edgeMetrics is EdgeMetrics for a collector the caller checked.
func (h *Handler) edgeMetrics(ctx context.Context, orgID, collectorID string, data []byte) EdgeReply {
	if h.metrics == nil {
		return EdgeReply{Status: http.StatusServiceUnavailable, Error: "metrics ingest unavailable"}
	}
	if h.features != nil {
		_, status, err := h.features.Status(ctx, orgID, entitlement.FeatureMonitoring)
		if err != nil {
			return EdgeReply{Status: http.StatusInternalServerError, Error: "entitlement lookup failed"}
		}
		if status == entitlement.StatusNotEntitled {
			refusal := &entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureMonitoring}
			slog.Warn("edge metrics refused", "organization_id", orgID, "collector_id", collectorID, "feature", refusal.FeatureKey)
			return EdgeReply{Status: http.StatusForbidden, Type: refusal.ProblemType(), Error: refusal.Error()}
		}
	}
	ctx, err := h.collectorScopeContext(ctx, orgID, collectorID)
	if err != nil {
		return edgeRefusal(err)
	}
	result, err := h.metrics.IngestPayload(ctx, orgID, data)
	var invalid *monitoring.InvalidMetricsError
	switch {
	case errors.As(err, &invalid):
		return EdgeReply{Status: http.StatusBadRequest, Error: invalid.Error()}
	case err != nil:
		slog.Error("edge metrics ingest failed", "organization_id", orgID, "collector_id", collectorID, "error", err)
		return EdgeReply{Status: http.StatusInternalServerError, Error: "metrics ingest failed"}
	}
	return EdgeReply{Status: http.StatusAccepted, Ingested: &result.Ingested, Unassigned: &result.Unassigned}
}

// collectorScopeContext returns ctx with the tenant scope of the collector
// as the request scope: its client and its site, so the samples it sends
// are assigned only to CIs of that client or site; a collector without a
// client or site keeps the organization-wide scope. The discovery ingest
// binds the records of a collector to its client the same way
// (deviceReviewScope, ResolveIdentity).
func (h *Handler) collectorScopeContext(ctx context.Context, orgID, collectorID string) (context.Context, error) {
	repo, ok := h.repo.(ScopeRepository)
	if !ok {
		return nil, errors.New("collector scope unavailable")
	}
	col, err := repo.ScopeCollector(ctx, orgID, collectorID)
	if err != nil {
		return nil, err
	}
	scope := database.OrgWideScope(orgID, "")
	if col.ClientID != "" {
		scope.Clients = database.ScopeIDs(col.ClientID)
	}
	if col.SiteID != "" {
		scope.Sites = database.ScopeIDs(col.SiteID)
	}
	ctx = database.ContextWithTenantScope(ctx, &scope)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: orgID, ClientID: col.ClientID}), nil
}

// edgeHandlers are the handlers of the edge data subjects by suffix; they
// run only for a collector the dispatcher checked. .metrics is served once
// the metrics ingest is set (WithEdgeMetrics).
func (h *Handler) edgeHandlers() map[string]func(ctx context.Context, orgID, collectorID string, data []byte) EdgeReply {
	handlers := map[string]func(ctx context.Context, orgID, collectorID string, data []byte) EdgeReply{
		IngestSuffix:    h.edgeIngest,
		HeartbeatSuffix: h.EdgeHeartbeat,
	}
	if h.metrics != nil {
		handlers[MetricsSuffix] = h.edgeMetrics
	}
	return handlers
}

// ServeCollectorEdge answers the ingest, heartbeat and metrics requests of
// the collectors on nc until ctx ends. A request of an unknown or disabled
// collector is refused before its handler runs, whatever the suffix, so
// nothing of it is persisted (COL-04).
func (h *Handler) ServeCollectorEdge(ctx context.Context, nc *nats.Conn) error {
	handlers := h.edgeHandlers()
	subs := make([]*nats.Subscription, 0, len(handlers))
	for suffix, handle := range handlers {
		sub, err := nc.QueueSubscribe("collector.*.*"+suffix, edgeDataQueue, func(msg *nats.Msg) {
			reply := EdgeReply{Status: http.StatusBadRequest, Error: "invalid subject"}
			if orgID, collectorID, ok := subjectCollector(msg.Subject, suffix); ok {
				sctx := systemContext(ctx, orgID)
				if cerr := h.checkCollector(sctx, orgID, collectorID); cerr != nil {
					slog.Warn("collector edge request refused", "organization_id", orgID, "collector_id", collectorID,
						"suffix", suffix, "error", cerr)
					reply = edgeRefusal(cerr)
				} else {
					reply = handle(sctx, orgID, collectorID, msg.Data)
				}
			}
			raw, _ := json.Marshal(reply)
			if msg.Reply != "" {
				if rerr := msg.Respond(raw); rerr != nil {
					slog.Error("collector edge reply", "error", rerr)
				}
			}
		})
		if err != nil {
			return fmt.Errorf("subscribe %s: %w", suffix, err)
		}
		subs = append(subs, sub)
	}
	<-ctx.Done()
	for _, sub := range subs {
		_ = sub.Unsubscribe()
	}
	return nil
}
