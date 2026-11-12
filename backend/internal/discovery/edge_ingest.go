package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
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
	// ProbeStateSuffix, ProbeResultsSuffix and ProbeAliveSuffix carry the
	// monitoring probes' state changes, result batches and alive messages
	// (PRB-05, E-173, WP-959); served once WithEdgeProbes set a handler.
	ProbeStateSuffix   = ".probe.state"
	ProbeResultsSuffix = ".probe.results"
	ProbeAliveSuffix   = ".probe.alive"
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

// EdgeProbes takes the monitoring probe messages of a collector the edge
// dispatcher checked (probe.Edge, WP-959): it answers with a reply the
// dispatcher sends as JSON. Workers is the number of handlers per instance
// that serve the probe subjects in parallel (RETICORA_PROBE_EDGE_WORKERS).
type EdgeProbes interface {
	ProbeState(ctx context.Context, orgID, collectorID string, data []byte) any
	ProbeResults(ctx context.Context, orgID, collectorID string, data []byte) any
	ProbeAlive(ctx context.Context, orgID, collectorID string, data []byte) any
	Workers() int
}

// WithEdgeProbes serves the probe subjects .probe.state, .probe.results and
// .probe.alive with probes (E-173). Without it the subjects are not served
// and the collectors keep their probe messages in the spool.
func (h *Handler) WithEdgeProbes(probes EdgeProbes) *Handler {
	h.probes = probes
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
	received := len(items) + body.identityRejected
	items, rejected := admitObservedAt(items, time.Now().UTC())
	// The records of a client-bound collector are processed in its client
	// (F-406/F-407, DIS-05): its scope carries them to the consumer, a
	// record without client_id takes the collector's client, one naming
	// another client, a site or a discovery scope outside it is rejected
	// on its own (WP-481).
	ctx, col, err := h.collectorIngestContext(ctx, orgID, collectorID)
	if err != nil {
		return edgeRefusal(err)
	}
	items, outside := h.bindRecordsToCollector(ctx, orgID, col, items)
	var resp BulkIngestResponse
	if len(items) > 0 {
		resp, err = h.dispatch(ctx, newIngestMessages(ctx, tenant.FromContext(ctx), collectorID, items))
	}
	// Records that fail are listed in the reply; only when no record was
	// accepted does the request fail as a whole (WP-481).
	resp.RejectedIdentityLimit = body.identityRejected
	if err = resp.finish(received, rejected, withRecordErrors(err, outside)); err != nil {
		var se *collectorScopeError
		if errors.As(err, &se) {
			// Every record lies outside the collector's client: refused for
			// good, the collector does not send them again.
			return EdgeReply{Status: http.StatusForbidden, Error: se.Error(), Result: &resp}
		}
		slog.Error("edge ingest failed", "organization_id", orgID, "collector_id", collectorID, "error", err)
		return EdgeReply{Status: http.StatusInternalServerError, Error: "ingest failed"}
	}
	return EdgeReply{Status: http.StatusAccepted, LicenseStatus: state, Result: &resp}
}

// collectorScopeError rejects a record of a client-bound collector whose
// field names a client, site or discovery scope outside the collector's
// client (F-407): 403, permanent.
type collectorScopeError struct{ field string }

func (e *collectorScopeError) Error() string {
	return e.field + ": outside the client of the collector"
}

// SiteClientReader returns the client of a site visible in the scope of
// ctx; the PostgreSQL repository implements it.
type SiteClientReader interface {
	SiteClient(ctx context.Context, orgID, siteID string) (string, error)
}

// bindRecordsToCollector binds the records of a client-bound collector to
// its client (F-407, DIS-05): a record without client_id takes the
// collector's client and, for a collector bound to a site, its site; a
// record naming another client or site, a site of another client or a
// discovery scope the collector's client does not see (or of another
// client) is removed and returned as a permanent record error, the others
// go on. A lookup that fails for another reason fails the record
// transiently. The records of an organization-wide collector stay as they
// are (the client comes from their scope).
func (h *Handler) bindRecordsToCollector(ctx context.Context, orgID string, col *Collector, items []IngestItem) ([]IngestItem, []RecordError) {
	if col == nil || col.ClientID == "" {
		return items, nil
	}
	sites, _ := h.repo.(SiteClientReader)
	scopes, _ := h.repo.(ScopeRepository)
	siteClient := map[string]string{}
	scopeClient := map[string]string{}
	var failed []RecordError
	admitted := items[:0]
	for i := range items {
		item := items[i]
		err := h.bindRecord(ctx, orgID, col, &item, sites, scopes, siteClient, scopeClient)
		if err != nil {
			var se *collectorScopeError
			if errors.As(err, &se) {
				slog.Warn("ingest record rejected: outside the client of its collector",
					"event", "ingest.record.outside_collector", "organization_id", orgID, "collector_id", col.ID,
					"record_id", item.RecordID, "field", se.field)
			}
			failed = append(failed, newRecordError(&item, err))
			continue
		}
		admitted = append(admitted, item)
	}
	return admitted, failed
}

// bindRecord applies bindRecordsToCollector to one record; the maps cache
// the lookups of a request.
func (h *Handler) bindRecord(ctx context.Context, orgID string, col *Collector, item *IngestItem, sites SiteClientReader,
	scopes ScopeRepository, siteClient, scopeClient map[string]string) error {
	switch {
	case item.ClientID == "":
		item.ClientID = col.ClientID
	case !strings.EqualFold(item.ClientID, col.ClientID):
		return &collectorScopeError{field: "client_id"}
	}
	switch {
	case col.SiteID != "" && item.SiteID == "":
		item.SiteID = col.SiteID
	case col.SiteID != "" && !strings.EqualFold(item.SiteID, col.SiteID):
		return &collectorScopeError{field: "site_id"}
	case col.SiteID == "" && item.SiteID != "":
		client, ok := siteClient[item.SiteID]
		if !ok {
			if sites == nil || !api.IsUUID(item.SiteID) {
				return &collectorScopeError{field: "site_id"}
			}
			var err error
			if client, err = sites.SiteClient(ctx, orgID, item.SiteID); err != nil && !isNotFound(err) {
				return err
			}
			siteClient[item.SiteID] = client
		}
		if !strings.EqualFold(client, col.ClientID) {
			return &collectorScopeError{field: "site_id"}
		}
	}
	if item.ScopeID == "" {
		return nil
	}
	client, ok := scopeClient[item.ScopeID]
	if !ok {
		if scopes == nil {
			return &collectorScopeError{field: "scope_id"}
		}
		s, err := scopes.GetScope(ctx, orgID, item.ScopeID)
		switch {
		case isNotFound(err):
			client = "-"
		case err != nil:
			return err
		default:
			client = s.ClientID
		}
		scopeClient[item.ScopeID] = client
	}
	// A scope the collector's client does not see, or one of another
	// client; an organization-wide scope of the collector stays valid.
	if client == "-" || (client != "" && !strings.EqualFold(client, col.ClientID)) {
		return &collectorScopeError{field: "scope_id"}
	}
	return nil
}

// withRecordErrors adds the records refused before the dispatch to its
// error: after the records the dispatch reports, so a transient failure of
// those still decides the answer when nothing was accepted. A failure of
// the dispatch as a whole stays as it is.
func withRecordErrors(err error, refused []RecordError) error {
	if len(refused) == 0 {
		return err
	}
	if err == nil {
		return &IngestErrors{Records: refused}
	}
	var ie *IngestErrors
	if errors.As(err, &ie) && ie.Rest == nil {
		ie.Records = append(ie.Records, refused...)
		return ie
	}
	return err
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
	recordVaultReport(req.Vault)
	reply := EdgeReply{Status: http.StatusNoContent, CollectorStatus: status}
	if state, _, lerr := h.edgeLicense(ctx, orgID); lerr == nil && state != LicenseNotEntitled {
		reply.LicenseStatus = state
	}
	return reply
}

// EdgeMetrics stores the metric samples a collector sent over the edge like
// POST /api/v1/monitoring/metrics (MON-02): 403 with the problem type of
// the entitlement middleware without the monitoring feature, 404 for a
// sample whose ci_id names a CI outside the collector's scope (nothing of
// the batch is stored), otherwise 202 with the stored and the unassigned
// samples. The samples are assigned in the collector's scope; the samples
// of an unknown or disabled collector are refused.
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
	var unknown *monitoring.UnknownCIError
	switch {
	case errors.As(err, &invalid):
		return EdgeReply{Status: http.StatusBadRequest, Error: invalid.Error()}
	case errors.As(err, &unknown):
		// A sample naming a CI outside the collector's scope refuses the
		// batch as on the API (404, nothing stored, F-394).
		return EdgeReply{Status: http.StatusNotFound, Error: "ci_id: " + unknown.Error()}
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
// binds the records of a collector to its client (collectorIngestContext).
func (h *Handler) collectorScopeContext(ctx context.Context, orgID, collectorID string) (context.Context, error) {
	ctx, _, err := h.collectorContext(ctx, orgID, collectorID, true)
	return ctx, err
}

// collectorIngestContext returns ctx with the scope the records of the
// collector are processed in (F-407): its client, so they create and
// update only CIs of that client (RLS, ResolveIdentity) and review items
// carry it; the site dimension stays organization-wide, since a discovered
// CI takes its site from a location, not from the record (a site scope
// would refuse every new CI). A collector without a client keeps the
// organization-wide scope. It also returns the collector.
func (h *Handler) collectorIngestContext(ctx context.Context, orgID, collectorID string) (context.Context, *Collector, error) {
	return h.collectorContext(ctx, orgID, collectorID, false)
}

// collectorContext reads the collector and returns ctx with its client
// scope and, with sites, its site scope as the request scope.
func (h *Handler) collectorContext(ctx context.Context, orgID, collectorID string, sites bool) (context.Context, *Collector, error) {
	repo, ok := h.repo.(ScopeRepository)
	if !ok {
		return nil, nil, errors.New("collector scope unavailable")
	}
	col, err := repo.ScopeCollector(ctx, orgID, collectorID)
	if err != nil {
		return nil, nil, err
	}
	scope := database.OrgWideScope(orgID, "")
	if col.ClientID != "" {
		scope.Clients = database.ScopeIDs(col.ClientID)
	}
	if sites && col.SiteID != "" {
		scope.Sites = database.ScopeIDs(col.SiteID)
	}
	ctx = database.ContextWithTenantScope(ctx, &scope)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: orgID, ClientID: col.ClientID}), col, nil
}

// edgeHandler answers an edge data request of a checked collector; the
// reply is sent as JSON.
type edgeHandler func(ctx context.Context, orgID, collectorID string, data []byte) any

// edgeReplying adapts a handler answering with an EdgeReply.
func edgeReplying(handle func(ctx context.Context, orgID, collectorID string, data []byte) EdgeReply) edgeHandler {
	return func(ctx context.Context, orgID, collectorID string, data []byte) any {
		return handle(ctx, orgID, collectorID, data)
	}
}

// probeSuffixes are the suffixes the probe handler pool serves.
var probeSuffixes = []string{ProbeStateSuffix, ProbeResultsSuffix, ProbeAliveSuffix}

// edgeHandlers are the handlers of the edge data subjects by suffix; they
// run only for a collector the dispatcher checked. .metrics is served once
// the metrics ingest is set (WithEdgeMetrics), the probe suffixes once a
// probe handler is set (WithEdgeProbes).
func (h *Handler) edgeHandlers() map[string]edgeHandler {
	handlers := map[string]edgeHandler{
		IngestSuffix:    edgeReplying(h.edgeIngest),
		HeartbeatSuffix: edgeReplying(h.EdgeHeartbeat),
	}
	if h.metrics != nil {
		handlers[MetricsSuffix] = edgeReplying(h.edgeMetrics)
	}
	if h.probes != nil {
		handlers[ProbeStateSuffix] = h.probes.ProbeState
		handlers[ProbeResultsSuffix] = h.probes.ProbeResults
		handlers[ProbeAliveSuffix] = h.probes.ProbeAlive
	}
	return handlers
}

// serveEdgeRequest answers one edge data request: a request of an unknown
// or disabled collector is refused before its handler runs (COL-04).
func (h *Handler) serveEdgeRequest(ctx context.Context, msg *nats.Msg, suffix string, handle edgeHandler) {
	var reply any = EdgeReply{Status: http.StatusBadRequest, Error: "invalid subject"}
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
}

// ServeCollectorEdge answers the ingest, heartbeat, metrics and probe
// requests of the collectors on nc until ctx ends. A request of an unknown
// or disabled collector is refused before its handler runs, whatever the
// suffix, so nothing of it is persisted (COL-04). nats.go calls the
// callbacks of a subscription one after another; the probe subjects
// therefore run on a pool of EdgeProbes.Workers handlers per instance
// (ChanQueueSubscribe, NFR-11).
func (h *Handler) ServeCollectorEdge(ctx context.Context, nc *nats.Conn) error {
	handlers := h.edgeHandlers()
	subs := make([]*nats.Subscription, 0, len(handlers))
	defer func() {
		for _, sub := range subs {
			_ = sub.Unsubscribe()
		}
	}()
	var probeMsgs chan *nats.Msg
	var probeSubs []*nats.Subscription
	if h.probes != nil {
		workers := max(h.probes.Workers(), 1)
		probeMsgs = make(chan *nats.Msg, probePending)
		for range workers {
			go h.serveProbeMessages(ctx, probeMsgs, handlers)
		}
	}
	for suffix, handle := range handlers {
		subject := "collector.*.*" + suffix
		if slices.Contains(probeSuffixes, suffix) {
			sub, err := nc.ChanQueueSubscribe(subject, edgeDataQueue, probeMsgs)
			if err != nil {
				return fmt.Errorf("subscribe %s: %w", suffix, err)
			}
			subs = append(subs, sub)
			probeSubs = append(probeSubs, sub)
			continue
		}
		sub, err := nc.QueueSubscribe(subject, edgeDataQueue, func(msg *nats.Msg) {
			h.serveEdgeRequest(ctx, msg, suffix, handle)
		})
		if err != nil {
			return fmt.Errorf("subscribe %s: %w", suffix, err)
		}
		subs = append(subs, sub)
	}
	if len(probeSubs) > 0 {
		watchProbeDrops(ctx, probeSubs)
		return nil
	}
	<-ctx.Done()
	return nil
}

// probePending is the buffer of the probe handler pool, as many messages
// as a callback subscription keeps pending by default: nats.go hands a
// channel subscription its messages without blocking and drops one the
// full channel cannot take (slow consumer), and the queue group does not
// send it to another instance. The collector repeats a dropped request
// only after its request timeout, so the buffer is sized for the resync
// burst of NFR-11, not for the pool.
const probePending = nats.DefaultSubPendingMsgsLimit

// probeDropCheck is how often watchProbeDrops looks for dropped messages.
const probeDropCheck = 10 * time.Second

// watchProbeDrops logs the probe messages a full buffer dropped (slow
// consumer) until ctx ends; the collectors repeat them after their
// request timeout.
func watchProbeDrops(ctx context.Context, subs []*nats.Subscription) {
	ticker := time.NewTicker(probeDropCheck)
	defer ticker.Stop()
	reported := make([]int, len(subs))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i, sub := range subs {
				dropped, err := sub.Dropped()
				if err != nil || dropped <= reported[i] {
					continue
				}
				slog.Warn("probe edge dropped messages: handler pool overloaded (slow consumer)",
					"subject", sub.Subject, "dropped", dropped-reported[i], "pending_limit", probePending)
				reported[i] = dropped
			}
		}
	}
}

// serveProbeMessages is one handler of the probe pool: it answers the
// messages of the probe subjects until ctx ends.
func (h *Handler) serveProbeMessages(ctx context.Context, msgs <-chan *nats.Msg, handlers map[string]edgeHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-msgs:
			for _, suffix := range probeSuffixes {
				if strings.HasSuffix(msg.Subject, suffix) {
					h.serveEdgeRequest(ctx, msg, suffix, handlers[suffix])
					break
				}
			}
		}
	}
}
