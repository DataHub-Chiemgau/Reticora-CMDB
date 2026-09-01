// Package collectorcmd provides the entry point for the Reticora Collector.
// The Collector runs in customer networks and handles Discovery, Provisioning
// and Agent-Relay. The logic lives in this importable package so both the
// canonical collector/cmd/collector binary and the backend module's
// compatibility shim (backend/cmd/collector) can invoke it.
package collectorcmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/ipmi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/nas"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/power"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/redfish"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/ssh"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/sweep"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/wmi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/transport"
)

type collectorConfig struct {
	ServerURL         string
	OrganizationID    string
	CollectorID       string
	ScanSubnets       []string
	Protocols         []string
	DiscoveryInterval time.Duration
	HeartbeatInterval time.Duration
	Credentials       map[string]string
	// SpoolDir is the disk buffer location for discovery results collected
	// while the backend is unreachable (offline operation per spec §5.2).
	SpoolDir string
	// SpoolMaxBytes caps the total spool size (oldest messages dropped
	// first); 0 = unlimited (Epic D4 hardening).
	SpoolMaxBytes int64
	// SpoolMaxAge drops buffered messages older than this on enqueue;
	// 0 = keep forever (Epic D4 hardening).
	SpoolMaxAge time.Duration
	// TrapListenAddr enables the SNMP trap receiver when set (e.g. ":162").
	// Traps are normalized into metric events and sent to the monitoring
	// ingest so existing alert rules can fire on them (spec §9).
	TrapListenAddr string
	// MetricsInterval enables metric polling when > 0: the configured SNMP
	// poll metrics are collected from the discovered targets every interval
	// and uploaded to the monitoring ingest (Epic E: Collector-Polling).
	MetricsInterval time.Duration
	// PollMetrics lists the numeric OIDs polled on every SNMP target. The
	// default covers the standard IF-MIB interface counters of ifIndex 1.
	PollMetrics []snmp.PollMetric
	// TLS identity material. When CertPEM/KeyPEM are set (directly, via
	// CertFile/KeyFile, or via the keystore at CredentialsPath) the upload
	// and heartbeat clients authenticate with mTLS.
	TLSCertPEM      []byte
	TLSKeyPEM       []byte
	TLSCAPEM        []byte
	TLSCertFile     string
	TLSKeyFile      string
	TLSCAFile       string
	TLSServerName   string
	CredentialsPath string
}

// pluginRegistry maps protocol names to plugin instances.
var pluginRegistry = map[string]plugins.Plugin{
	"sweep":   sweep.New(),
	"snmp":    snmp.New(),
	"ssh":     ssh.New(),
	"wmi":     wmi.New(),
	"ipmi":    ipmi.New(),
	"redfish": redfish.New(),
	"power":   power.New(),
	"nas":     nas.New(),
}

// Main runs the collector until an interrupt or termination signal arrives.
// It is the entry point shared by collector/cmd/collector and the backend
// module's compatibility shim.
func Main() {
	collectorCfg := loadCollectorConfig()

	environment := envOrDefault("RETICORA_ENVIRONMENT", "development")
	logLevel := slog.LevelInfo
	if environment == "development" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	slog.Info("starting collector", "environment", environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	uploader, err := newUploader(ctx, collectorCfg)
	if err != nil {
		slog.Error("collector upload setup failed", "error", err)
		os.Exit(1)
	}

	natsURL := envOrDefault("RETICORA_NATS_URL", "nats://localhost:4222")
	slog.Info("collector configured",
		"nats_url", natsURL,
		"server_url", collectorCfg.ServerURL,
		"scan_subnets", collectorCfg.ScanSubnets,
		"protocols", collectorCfg.Protocols,
		"discovery_interval", collectorCfg.DiscoveryInterval.String(),
		"heartbeat_interval", collectorCfg.HeartbeatInterval.String(),
		"mtls", uploader.mtls,
		"spool_dir", collectorCfg.SpoolDir,
	)

	go runDiscoveryLoop(ctx, collectorCfg, uploader)
	go runHeartbeatLoop(ctx, collectorCfg, uploader)
	go runAgentRelay(ctx, uploader)
	if collectorCfg.TrapListenAddr != "" {
		go runTrapReceiver(ctx, collectorCfg, uploader)
	}
	if collectorCfg.MetricsInterval > 0 && len(collectorCfg.PollMetrics) > 0 {
		go runMetricsLoop(ctx, collectorCfg, uploader)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("collector shutting down")
	cancel()
}

// spoolTopic is the buffer topic under which discovery result batches are
// persisted while the backend is unreachable.
const spoolTopic = "discovery-results"

// uploader delivers discovery results and heartbeats to the backend. When
// client-certificate material is configured it uses the mTLS transport from
// edgecore; otherwise it falls back to plain HTTPS/HTTP so local development
// keeps working. Failed uploads are spooled to the on-disk buffer and flushed
// once the backend is reachable again (spec §5.2: "Batch-Upload komprimiert
// via mTLS (offline: lokaler Puffer)").
type uploader struct {
	cfg    collectorConfig
	client *http.Client
	mtls   bool
	spool  *buffer.DiskBuffer
}

// newUploader builds the upload path: mTLS when certificate material is
// available, plus the disk spool used for offline buffering.
func newUploader(ctx context.Context, cfg collectorConfig) (*uploader, error) {
	u := &uploader{cfg: cfg}
	if cfg.SpoolDir != "" {
		u.spool = &buffer.DiskBuffer{Dir: cfg.SpoolDir, MaxBytes: cfg.SpoolMaxBytes, MaxAge: cfg.SpoolMaxAge}
	}

	certPEM, keyPEM, caPEM, err := loadTLSMaterial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if len(certPEM) > 0 && len(keyPEM) > 0 {
		mtlsTransport, err := transport.NewMTLS(transport.Config{
			ServerName:    cfg.TLSServerName,
			RootCAsPEM:    caPEM,
			ClientCertPEM: certPEM,
			ClientKeyPEM:  keyPEM,
			Timeout:       30 * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("mTLS setup: %w", err)
		}
		u.client = mtlsTransport.HTTPClient()
		u.mtls = true
	} else {
		slog.Warn("no collector client certificate configured; uploads use plain TLS/HTTP without client authentication",
			"hint", "set RETICORA_TLS_CLIENT_CERT_FILE/RETICORA_TLS_CLIENT_KEY_FILE or RETICORA_CREDENTIALS_PATH")
		u.client = &http.Client{Timeout: 30 * time.Second}
	}
	return u, nil
}

// loadTLSMaterial resolves the collector's client identity from PEM env
// variables, PEM files, or the keystore written by enrollment.
func loadTLSMaterial(ctx context.Context, cfg collectorConfig) (certPEM, keyPEM, caPEM []byte, err error) {
	certPEM, keyPEM, caPEM = cfg.TLSCertPEM, cfg.TLSKeyPEM, cfg.TLSCAPEM

	readIfEmpty := func(current []byte, path string) ([]byte, error) {
		if len(current) > 0 || path == "" {
			return current, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read TLS material from %s: %w", path, err)
		}
		return data, nil
	}

	if certPEM, err = readIfEmpty(certPEM, cfg.TLSCertFile); err != nil {
		return nil, nil, nil, err
	}
	if keyPEM, err = readIfEmpty(keyPEM, cfg.TLSKeyFile); err != nil {
		return nil, nil, nil, err
	}
	if caPEM, err = readIfEmpty(caPEM, cfg.TLSCAFile); err != nil {
		return nil, nil, nil, err
	}

	if (len(certPEM) == 0 || len(keyPEM) == 0) && cfg.CredentialsPath != "" {
		creds, loadErr := (&keystore.FileStore{Path: cfg.CredentialsPath}).Load(ctx)
		switch {
		case loadErr == nil:
			if len(certPEM) == 0 {
				certPEM = []byte(creds.ClientCertificatePEM)
			}
			if len(keyPEM) == 0 {
				keyPEM = []byte(creds.ClientPrivateKeyPEM)
			}
			if len(caPEM) == 0 {
				caPEM = []byte(creds.CertificateAuthority)
			}
		case errors.Is(loadErr, keystore.ErrNotFound):
			// Not enrolled yet; plain client remains active.
		default:
			return nil, nil, nil, fmt.Errorf("load collector credentials: %w", loadErr)
		}
	}

	if len(certPEM) == 0 && len(keyPEM) == 0 {
		return nil, nil, caPEM, nil
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, nil, nil, fmt.Errorf("collector TLS configuration is incomplete: client certificate and key must both be provided")
	}
	return certPEM, keyPEM, caPEM, nil
}

func runDiscoveryLoop(ctx context.Context, cfg collectorConfig, up *uploader) {
	ticker := time.NewTicker(cfg.DiscoveryInterval)
	defer ticker.Stop()

	for {
		cycleStart := time.Now()
		slog.Info("discovery cycle started", "scan_subnets", cfg.ScanSubnets, "protocols", cfg.Protocols)

		var allResults []plugins.Result

		// Expand subnets to individual targets for discovery
		targets := expandSubnets(cfg.ScanSubnets)

		for _, protocol := range cfg.Protocols {
			if ctx.Err() != nil {
				break
			}

			plug, ok := pluginRegistry[protocol]
			if !ok {
				slog.Warn("unknown protocol plugin, skipping", "protocol", protocol)
				continue
			}

			slog.Info("running discovery plugin", "protocol", protocol, "targets", len(targets))
			results, err := plug.Discover(ctx, targets, cfg.Credentials)
			if err != nil {
				slog.Error("discovery plugin error", "protocol", protocol, "error", err)
				continue
			}
			slog.Info("discovery plugin completed", "protocol", protocol, "results", len(results))
			allResults = append(allResults, results...)
		}

		// Upload results to the backend in a compressed batch, then flush any
		// results spooled while the backend was unreachable.
		if len(allResults) > 0 {
			if err := up.uploadResults(ctx, allResults); err != nil {
				slog.Error("failed to upload discovery results", "error", err, "count", len(allResults))
			} else {
				slog.Info("discovery results uploaded", "count", len(allResults))
			}
		}
		up.flushSpool(ctx)

		slog.Info("discovery cycle completed", "duration_ms", time.Since(cycleStart).Milliseconds(), "total_results", len(allResults))

		select {
		case <-ctx.Done():
			slog.Info("discovery loop stopped")
			return
		case <-ticker.C:
		}
	}
}

// expandSubnets converts CIDR notations to individual IP targets.
const maxSubnetExpansionTargets = 4096

func expandSubnets(subnets []string) []string {
	var targets []string
	for _, subnet := range subnets {
		_, ipNet, err := net.ParseCIDR(subnet)
		if err != nil {
			// Treat as single host
			targets = append(targets, subnet)
			continue
		}

		maskOnes, _ := ipNet.Mask.Size()
		var subnetTargets []string
		for ip := ipNet.IP.Mask(ipNet.Mask); ipNet.Contains(ip); incrementIP(ip) {
			subnetTargets = append(subnetTargets, ip.String())
			// Safety limit to avoid expanding huge ranges
			if len(subnetTargets) > maxSubnetExpansionTargets {
				slog.Warn("subnet expansion limit reached", "subnet", subnet, "limit", maxSubnetExpansionTargets)
				break
			}
		}

		// Remove network and broadcast addresses for /30 and larger prefixes
		// For /31 and /32 all addresses are usable (point-to-point or host routes)
		if maskOnes <= 30 && len(subnetTargets) > 2 {
			subnetTargets = subnetTargets[1 : len(subnetTargets)-1]
		}

		targets = append(targets, subnetTargets...)
	}
	return targets
}

func incrementIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// uploadResults sends discovery results to the backend via compressed JSON
// POST. When the upload fails and a spool directory is configured, the batch
// is persisted to the disk buffer and delivered by flushSpool once the
// backend is reachable again.
func (u *uploader) uploadResults(ctx context.Context, results []plugins.Result) error {
	cfg := u.cfg
	if cfg.ServerURL == "" || cfg.OrganizationID == "" || cfg.CollectorID == "" {
		slog.Warn("skipping upload due to incomplete configuration")
		return nil
	}

	payload, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}

	if err := u.postPayload(ctx, payload); err != nil {
		if spoolErr := u.spoolResults(ctx, payload); spoolErr != nil {
			return fmt.Errorf("upload failed (%v) and spooling failed: %w", err, spoolErr)
		}
		return err
	}
	return nil
}

// postPayload compresses and POSTs a single JSON payload to the ingest endpoint.
func (u *uploader) postPayload(ctx context.Context, payload []byte) error {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(payload); err != nil {
		return fmt.Errorf("compress results: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("finalize compression: %w", err)
	}

	endpoint := strings.TrimRight(u.cfg.ServerURL, "/") + "/api/v1/discovery/ingest"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return fmt.Errorf("build upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-Organization-ID", u.cfg.OrganizationID)
	req.Header.Set("X-Collector-ID", u.cfg.CollectorID)

	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("upload request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("upload failed with status %d", resp.StatusCode)
	}
	return nil
}

// postAgentTelemetry forwards an endpoint-agent telemetry payload to the
// backend's agent surface (not the discovery ingest used for device results).
func (u *uploader) postAgentTelemetry(ctx context.Context, payload []byte) error {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(payload); err != nil {
		return fmt.Errorf("compress telemetry: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("finalize compression: %w", err)
	}

	endpoint := strings.TrimRight(u.cfg.ServerURL, "/") + "/api/v1/agents/telemetry"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return fmt.Errorf("build telemetry request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("telemetry upload request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("telemetry upload failed with status %d", resp.StatusCode)
	}
	return nil
}

// spoolResults persists an undeliverable batch to the disk buffer.
func (u *uploader) spoolResults(ctx context.Context, payload []byte) error {
	if u.spool == nil {
		return nil
	}
	id, err := u.spool.Enqueue(ctx, buffer.Message{Topic: spoolTopic, Payload: payload})
	if err != nil {
		return err
	}
	slog.Info("discovery results spooled for later delivery", "message_id", id)
	u.logSpoolStats(ctx)
	return nil
}

// logSpoolStats emits the current spool occupancy as a structured log record
// so operators can alert on a growing/ageing spool (Epic D4: Metriken).
func (u *uploader) logSpoolStats(ctx context.Context) {
	if u.spool == nil {
		return
	}
	stats, err := u.spool.Stats(ctx)
	if err != nil {
		slog.Warn("spool stats unavailable", "error", err)
		return
	}
	slog.Info("spool stats",
		"spool_messages", stats.Messages,
		"spool_bytes", stats.Bytes,
		"spool_oldest_age", stats.OldestAge.String(),
		"spool_max_bytes", u.cfg.SpoolMaxBytes,
		"spool_max_age", u.cfg.SpoolMaxAge.String(),
	)
}

// flushSpool delivers buffered batches in oldest-first order. Delivery stops
// at the first failure so spooled batches are never reordered or dropped.
func (u *uploader) flushSpool(ctx context.Context) {
	if u.spool == nil || u.cfg.ServerURL == "" {
		return
	}
	msgs, err := u.spool.PeekBatch(ctx, 16)
	if err != nil {
		slog.Error("read upload spool failed", "error", err)
		return
	}
	for _, msg := range msgs {
		if ctx.Err() != nil {
			return
		}
		if msg.Topic != spoolTopic {
			continue
		}
		if err := u.postPayload(ctx, msg.Payload); err != nil {
			slog.Warn("spool flush postponed; backend still unreachable", "message_id", msg.ID, "error", err)
			return
		}
		if err := u.spool.Ack(ctx, []string{msg.ID}); err != nil {
			slog.Error("spool ack failed", "message_id", msg.ID, "error", err)
			return
		}
		slog.Info("spooled discovery results delivered", "message_id", msg.ID)
	}
	if len(msgs) > 0 {
		u.logSpoolStats(ctx)
	}
}

func runHeartbeatLoop(ctx context.Context, cfg collectorConfig, up *uploader) {
	ticker := time.NewTicker(cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		postHeartbeat(ctx, up.client, cfg)
		select {
		case <-ctx.Done():
			slog.Info("heartbeat loop stopped")
			return
		case <-ticker.C:
		}
	}
}

func postHeartbeat(ctx context.Context, client *http.Client, cfg collectorConfig) {
	if cfg.ServerURL == "" || cfg.OrganizationID == "" || cfg.CollectorID == "" {
		slog.Warn("skipping heartbeat due to incomplete configuration",
			"server_url", cfg.ServerURL,
			"organization_id", cfg.OrganizationID,
			"collector_id", cfg.CollectorID,
		)
		return
	}

	endpoint := strings.TrimRight(cfg.ServerURL, "/") + "/api/v1/collectors/" + cfg.CollectorID + "/heartbeat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		slog.Error("build heartbeat request failed", "error", err)
		return
	}
	req.Header.Set("X-Organization-ID", cfg.OrganizationID)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("heartbeat failed", "error", err, "endpoint", endpoint)
		return
	}
	defer resp.Body.Close()

	slog.Info("heartbeat completed", "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
}

// runTrapReceiver receives SNMP traps and forwards them as metric events to
// the monitoring ingest, so the existing alert rules can fire on traps (e.g.
// "any linkDown trap → critical alert"). Failed uploads are logged and
// dropped: traps are fire-and-forget events, spooling them like discovery
// results would delay alerts past the point where they are useful.
func runTrapReceiver(ctx context.Context, cfg collectorConfig, up *uploader) {
	rcv := &snmp.TrapReceiver{
		ListenAddr: cfg.TrapListenAddr,
		Community:  cfg.Credentials["community"],
		Sink: func(ctx context.Context, ev snmp.TrapEvent) {
			slog.Info("snmp trap received", "source", ev.SourceIP, "trap_oid", ev.TrapOID)
			if err := up.uploadTrapEvent(ctx, ev); err != nil {
				slog.Warn("trap event upload failed", "source", ev.SourceIP, "trap_oid", ev.TrapOID, "error", err)
			}
		},
	}
	if err := rcv.Run(ctx); err != nil {
		slog.Error("snmp trap receiver stopped", "error", err)
	}
}

// runMetricsLoop polls the configured numeric SNMP metrics on every scan
// target and uploads the samples to the monitoring ingest (Epic E:
// Collector-Polling). Polling runs independently of discovery so metric
// intervals can be much shorter than discovery cycles.
func runMetricsLoop(ctx context.Context, cfg collectorConfig, up *uploader) {
	ticker := time.NewTicker(cfg.MetricsInterval)
	defer ticker.Stop()

	poller, ok := pluginRegistry["snmp"].(*snmp.Plugin)
	if !ok {
		slog.Error("snmp plugin unavailable; metric polling disabled")
		return
	}

	for {
		targets := expandSubnets(cfg.ScanSubnets)
		samples := make([]plugins.MetricSample, 0, len(targets)*len(cfg.PollMetrics))
		for _, target := range targets {
			if ctx.Err() != nil {
				break
			}
			polled, err := poller.Poll(ctx, target, cfg.Credentials, cfg.PollMetrics)
			if err != nil {
				slog.Debug("metric poll failed", "target", target, "error", err)
				continue
			}
			samples = append(samples, polled...)
		}
		if len(samples) > 0 {
			if err := up.uploadMetricSamples(ctx, samples); err != nil {
				slog.Warn("metric sample upload failed", "count", len(samples), "error", err)
			} else {
				slog.Info("metric samples uploaded", "count", len(samples))
			}
		}

		select {
		case <-ctx.Done():
			slog.Info("metrics loop stopped")
			return
		case <-ticker.C:
		}
	}
}

// uploadMetricSamples converts collector metric samples into monitoring
// ingest payloads and POSTs them in one batch.
func (u *uploader) uploadMetricSamples(ctx context.Context, samples []plugins.MetricSample) error {
	if u.cfg.ServerURL == "" || u.cfg.OrganizationID == "" || u.cfg.CollectorID == "" {
		return nil
	}
	metrics := make([]map[string]any, 0, len(samples))
	now := time.Now().UTC()
	for _, sample := range samples {
		labels := map[string]string{"collector_id": u.cfg.CollectorID, "kind": "snmp_poll"}
		for k, v := range sample.Labels {
			labels[k] = v
		}
		metrics = append(metrics, map[string]any{
			"name":      sample.Name,
			"value":     sample.Value,
			"labels":    labels,
			"timestamp": now.Format(time.RFC3339Nano),
		})
	}
	payload, err := json.Marshal(metrics)
	if err != nil {
		return fmt.Errorf("marshal metric samples: %w", err)
	}
	return u.postMetricsPayload(ctx, payload)
}

// postMetricsPayload POSTs a monitoring ingest payload (shared by trap
// events and polled metrics).
func (u *uploader) postMetricsPayload(ctx context.Context, payload []byte) error {
	endpoint := strings.TrimRight(u.cfg.ServerURL, "/") + "/api/v1/monitoring/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build metrics upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Organization-ID", u.cfg.OrganizationID)
	req.Header.Set("X-Collector-ID", u.cfg.CollectorID)

	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("metrics upload request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("metrics upload failed with status %d", resp.StatusCode)
	}
	return nil
}

// uploadTrapEvent maps a normalized trap onto a monitoring metric sample and
// POSTs it to the ingest endpoint. The metric name is stable so alert rules
// can target it; trap-specific context travels in the labels.
func (u *uploader) uploadTrapEvent(ctx context.Context, ev snmp.TrapEvent) error {
	if u.cfg.ServerURL == "" || u.cfg.OrganizationID == "" || u.cfg.CollectorID == "" {
		return nil
	}
	labels := map[string]string{
		"source_ip":    ev.SourceIP,
		"collector_id": u.cfg.CollectorID,
		"kind":         "snmp_trap",
	}
	if ev.TrapOID != "" {
		labels["trap_oid"] = ev.TrapOID
	}
	for k, v := range ev.Variables {
		if len(labels) >= 16 { // keep the label set bounded
			break
		}
		labels["var_"+strings.ReplaceAll(k, ".", "_")] = v
	}
	payload, err := json.Marshal([]map[string]any{{
		"name":      "snmp_trap_received",
		"value":     1,
		"labels":    labels,
		"timestamp": ev.Received.UTC().Format(time.RFC3339Nano),
	}})
	if err != nil {
		return fmt.Errorf("marshal trap event: %w", err)
	}
	return u.postMetricsPayload(ctx, payload)
}

func runAgentRelay(ctx context.Context, up *uploader) {
	slog.Info("agent relay started")

	// The agent relay accepts incoming connections from endpoint agents
	// and forwards telemetry/results to the backend.
	listener, err := net.Listen("tcp", ":9443")
	if err != nil {
		slog.Error("agent relay listen failed", "error", err)
		return
	}
	defer listener.Close()

	slog.Info("agent relay listening", "addr", listener.Addr().String())

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("agent relay stopped")
				return
			}
			slog.Error("agent relay accept error", "error", err)
			continue
		}
		go handleAgentConnection(ctx, up, conn)
	}
}

// handleAgentConnection processes a single agent connection, reading telemetry data
// and forwarding it to the backend through the collector's (mTLS) upload path.
func handleAgentConnection(ctx context.Context, up *uploader, conn net.Conn) {
	defer conn.Close()

	remoteAddr := conn.RemoteAddr().String()
	slog.Debug("agent connected", "remote", remoteAddr)

	// Set read deadline to prevent stale connections
	if err := conn.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		slog.Error("set deadline failed", "error", err)
		return
	}

	// Read the agent telemetry payload (JSON newline-delimited)
	buf := make([]byte, 65536)
	n, err := conn.Read(buf)
	if err != nil {
		slog.Debug("agent read error", "remote", remoteAddr, "error", err)
		return
	}

	slog.Debug("agent data received", "remote", remoteAddr, "bytes", n)

	// Forward the telemetry payload to the backend's agent-telemetry endpoint
	// via the collector's (mTLS-authenticated) upload path, falling back to the
	// disk spool when the cloud is unreachable. The agent gets an ack only once
	// the payload is durably accepted (uploaded or spooled).
	payload := buf[:n]
	if up != nil {
		if err := up.postAgentTelemetry(ctx, payload); err != nil {
			slog.Warn("agent telemetry upload failed, spooling", "remote", remoteAddr, "error", err)
			if serr := up.spoolResults(ctx, payload); serr != nil {
				slog.Error("agent telemetry spool failed", "remote", remoteAddr, "error", serr)
				_, _ = conn.Write([]byte(`{"status":"error"}` + "\n"))
				return
			}
		}
	}

	// Acknowledge receipt
	_, _ = conn.Write([]byte(`{"status":"ok"}` + "\n"))
}

func loadCollectorConfig() collectorConfig {
	creds := make(map[string]string)
	if community := os.Getenv("RETICORA_SNMP_COMMUNITY"); community != "" {
		creds["community"] = community
	}
	if username := os.Getenv("RETICORA_SSH_USERNAME"); username != "" {
		creds["username"] = username
	}
	if password := os.Getenv("RETICORA_SSH_PASSWORD"); password != "" {
		creds["password"] = password
	}

	return collectorConfig{
		ServerURL:         envOrDefault("RETICORA_SERVER_URL", "http://localhost:8080"),
		OrganizationID:    os.Getenv("RETICORA_ORGANIZATION_ID"),
		CollectorID:       os.Getenv("RETICORA_COLLECTOR_ID"),
		ScanSubnets:       csvEnvOrDefault("RETICORA_SCAN_SUBNETS", []string{"127.0.0.1/32"}),
		Protocols:         csvEnvOrDefault("RETICORA_DISCOVERY_PROTOCOLS", []string{"sweep", "snmp", "ssh"}),
		DiscoveryInterval: durationEnvOrDefault("RETICORA_DISCOVERY_INTERVAL", 15*time.Minute),
		HeartbeatInterval: durationEnvOrDefault("RETICORA_HEARTBEAT_INTERVAL", time.Minute),
		Credentials:       creds,
		SpoolDir:          envOrDefault("RETICORA_SPOOL_DIR", "/var/lib/reticora-collector/spool"),
		SpoolMaxBytes:     int64EnvOrDefault("RETICORA_SPOOL_MAX_BYTES", 1<<30), // 1 GiB default cap
		SpoolMaxAge:       durationEnvOrDefault("RETICORA_SPOOL_MAX_AGE", 72*time.Hour),
		TrapListenAddr:    os.Getenv("RETICORA_SNMP_TRAP_LISTEN"),
		MetricsInterval:   durationEnvOrDefault("RETICORA_METRICS_INTERVAL", 0),
		PollMetrics:       pollMetricsFromEnv(os.Getenv("RETICORA_SNMP_POLL_METRICS")),
		TLSCertPEM:        []byte(os.Getenv("RETICORA_TLS_CLIENT_CERT")),
		TLSKeyPEM:         []byte(os.Getenv("RETICORA_TLS_CLIENT_KEY")),
		TLSCAPEM:          []byte(os.Getenv("RETICORA_TLS_CA")),
		TLSCertFile:       os.Getenv("RETICORA_TLS_CLIENT_CERT_FILE"),
		TLSKeyFile:        os.Getenv("RETICORA_TLS_CLIENT_KEY_FILE"),
		TLSCAFile:         os.Getenv("RETICORA_TLS_CA_FILE"),
		TLSServerName:     os.Getenv("RETICORA_TLS_SERVER_NAME"),
		CredentialsPath:   envOrDefault("RETICORA_CREDENTIALS_PATH", "/var/lib/reticora-collector/credentials.json"),
	}
}

// defaultPollMetrics polls the IF-MIB interface counters of ifIndex 1 when
// no explicit list is configured.
var defaultPollMetrics = []snmp.PollMetric{
	{Name: "if_in_octets", OID: "1.3.6.1.2.1.2.2.1.10.1"},
	{Name: "if_out_octets", OID: "1.3.6.1.2.1.2.2.1.16.1"},
	{Name: "if_oper_status", OID: "1.3.6.1.2.1.2.2.1.8.1"},
}

// pollMetricsFromEnv parses RETICORA_SNMP_POLL_METRICS entries in the form
// "name=oid,name=oid" (e.g. "ifInOctets=1.3.6.1.2.1.2.2.1.10.1"). An empty
// value selects the defaults so metric polling works out of the box once
// RETICORA_METRICS_INTERVAL is set.
func pollMetricsFromEnv(value string) []snmp.PollMetric {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultPollMetrics
	}
	metrics := make([]snmp.PollMetric, 0)
	for _, entry := range strings.Split(value, ",") {
		name, oid, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(oid) == "" {
			slog.Warn("ignoring invalid poll metric entry", "entry", entry)
			continue
		}
		metrics = append(metrics, snmp.PollMetric{Name: strings.TrimSpace(name), OID: strings.TrimSpace(oid)})
	}
	if len(metrics) == 0 {
		return defaultPollMetrics
	}
	return metrics
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func csvEnvOrDefault(key string, fallback []string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}

func durationEnvOrDefault(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		slog.Warn("invalid duration configuration", "key", key, "value", value, "fallback", fallback.String(), "error", err)
		return fallback
	}
	return duration
}

// int64EnvOrDefault parses an integer byte limit from the environment;
// 0 disables the limit, negative values fall back to the default.
func int64EnvOrDefault(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		slog.Warn("invalid integer configuration", "key", key, "value", value, "fallback", fallback)
		return fallback
	}
	return parsed
}

func (c collectorConfig) String() string {
	return fmt.Sprintf("server=%s collector=%s", c.ServerURL, c.CollectorID)
}
