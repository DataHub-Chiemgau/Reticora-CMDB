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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The collectors' data path over the edge (COL-08): an enrolled collector
// delivers its records and heartbeats over its mutually authenticated NATS
// channel. The edge grants a collector only collector.<org>.<id>.>, so the
// organization and the collector are those its certificate was authorized
// for; the HTTP ingest stays for API clients with a session or API key.
const (
	IngestSuffix    = ".ingest"
	HeartbeatSuffix = ".heartbeat"
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
	if err != nil {
		slog.Error("edge ingest failed", "organization_id", orgID, "collector_id", collectorID, "error", err)
		return EdgeReply{Status: http.StatusInternalServerError, Error: "ingest failed"}
	}
	resp.Received, resp.RejectedFuture = received, rejected
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
	if s := req.Spool; s != nil && (s.DroppedMessages > 0 || s.Backpressure) {
		slog.WarnContext(ctx, "collector reported spool data loss or backpressure", "event", "collector.spool.degraded",
			"organization_id", orgID, "collector_id", collectorID, "spool_messages", s.Messages,
			"dropped_messages", s.DroppedMessages, "backpressure", s.Backpressure)
	}
	reply := EdgeReply{Status: http.StatusNoContent, CollectorStatus: status}
	if state, _, lerr := h.edgeLicense(ctx, orgID); lerr == nil && state != LicenseNotEntitled {
		reply.LicenseStatus = state
	}
	return reply
}

// edgeHandlers are the handlers of the edge data subjects by suffix; they
// run only for a collector the dispatcher checked.
func (h *Handler) edgeHandlers() map[string]func(ctx context.Context, orgID, collectorID string, data []byte) EdgeReply {
	return map[string]func(ctx context.Context, orgID, collectorID string, data []byte) EdgeReply{
		IngestSuffix:    h.edgeIngest,
		HeartbeatSuffix: h.EdgeHeartbeat,
	}
}

// ServeCollectorEdge answers the ingest and heartbeat requests of the
// collectors on nc until ctx ends. A request of an unknown or disabled
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
