// Package collectorcmd provides the entry point for the Reticora Collector.
// The Collector runs in customer networks and handles Discovery, Provisioning
// and Agent-Relay. The logic lives in this importable package so both the
// canonical collector/cmd/collector binary and the backend module's
// compatibility shim (backend/cmd/collector) can invoke it.
package collectorcmd

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/ipmi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/nas"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/power"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/redfish"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/simulate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/ssh"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/sweep"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/wmi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/transport"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/update"
)

type collectorConfig struct {
	ServerURL         string
	OrganizationID    string
	CollectorID       string
	ScanSubnets       []string
	Protocols         []string
	// Simulate replaces discovery by the deterministic 30-device fleet of
	// plugins/simulate (RETICORA_SIMULATE, SIM-02); refused in production.
	Simulate bool
	// DiscoveryInterval is the poll interval of the configured subnets
	// (RETICORA_DISCOVERY_INTERVAL, default 15 min, at least 5 min);
	// SweepInterval their sweep interval (default 24 h, DIS-10). Scopes
	// bring their own schedule and the organization's poll intervals.
	DiscoveryInterval time.Duration
	SweepInterval     time.Duration
	// ScheduleTick is how often due discovery is checked (default 1 min).
	ScheduleTick time.Duration
	// SpoolFlushInterval is how often the spool is delivered (default 30 s).
	SpoolFlushInterval time.Duration
	HeartbeatInterval  time.Duration
	// Credentials are the locally configured credentials (RETICORA_*),
	// namespaced by protocol ("ssh.username", "redfish.password",
	// "snmp.community"); plugins receive only their own namespace through
	// plugins.CredentialsFor (COL-02). They apply to the configured subnets
	// (fallback pass, metric polling, traps), never to a server scope: a
	// scope uses the bundle credentials of its credential_ids.
	Credentials map[string]string
	// RedfishTLSScopes and WMITLSScopes are the per-network TLS exceptions
	// of the Redfish and WMI plugins (RETICORA_REDFISH_TLS_SCOPES,
	// RETICORA_WMI_TLS_SCOPES); outside them certificates are verified
	// against the system roots.
	RedfishTLSScopes []plugins.TLSScope
	WMITLSScopes     []plugins.TLSScope
	// SpoolDir is the disk buffer location for discovery results collected
	// while the backend is unreachable (offline operation per spec §5.2).
	SpoolDir string
	// SpoolMaxBytes caps the total spool size; 0 = unlimited. At 90 % the
	// collector pauses discovery (backpressure); messages younger than 24 h
	// are never dropped (NFR-04, COL-05).
	SpoolMaxBytes int64
	// SpoolMaxAge drops buffered messages older than this on enqueue, but
	// never before 24 h; 0 = keep forever.
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
	// AgentRelayAddr, AgentRelayCertFile and AgentRelayKeyFile configure the
	// agent relay (AGT-03). It runs over TLS only: without a server
	// certificate the relay stays off.
	AgentRelayAddr     string
	AgentRelayCertFile string
	AgentRelayKeyFile  string
	// SweepConcurrency is the number of parallel sweep probes, capped at
	// sweep.MaxConcurrency (DIS-01).
	SweepConcurrency int
	// UpdateURL enables signed auto-updates (COL-06) from the release
	// manifests below <UpdateURL>/<UpdateChannel>/; empty disables them.
	UpdateURL      string
	UpdateChannel  string
	UpdateInterval time.Duration
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
	if handled, code := runCommand(context.Background(), os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	collectorCfg := loadCollectorConfig()
	pluginRegistry["redfish"] = redfish.New(redfish.WithTLSScopes(collectorCfg.RedfishTLSScopes...))
	pluginRegistry["wmi"] = wmi.New(wmi.WithTLSScopes(collectorCfg.WMITLSScopes...))
	pluginRegistry["sweep"] = newSweep(&collectorCfg)
	loadProfileDir(os.Getenv("RETICORA_PROFILES_DIR"))

	environment := envOrDefault("RETICORA_ENVIRONMENT", "development")
	logLevel := slog.LevelInfo
	if environment == "development" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	slog.Info("starting collector", "environment", environment, "version", Version)
	if err := applySimulation(&collectorCfg, environment); err != nil {
		slog.Error("collector simulation refused", "event", "collector.simulate.refused", "error", err)
		os.Exit(1)
	}

	// A pending update is confirmed by the first successful heartbeat;
	// after repeated failed starts it is rolled back (COL-06).
	confirmUpdate := func() {}
	execPath, err := executablePath()
	if err == nil {
		confirmUpdate, err = startupUpdate(execPath)
		if errors.Is(err, update.ErrRolledBack) {
			os.Exit(1)
		}
		if err != nil {
			slog.Error("pending collector update", "error", err)
		}
	}
	confirmed := make(chan struct{})
	var confirmedOnce sync.Once
	onHeartbeat := func() {
		confirmUpdate()
		confirmedOnce.Do(func() { close(confirmed) })
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	uploader, err := newUploader(ctx, collectorCfg)
	if err != nil {
		slog.Error("collector upload setup failed", "error", err)
		os.Exit(1)
	}

	slog.Info("collector configured",
		"nats_url", natsEndpoint(collectorCfg.ServerURL),
		"server_url", collectorCfg.ServerURL,
		"scan_subnets", collectorCfg.ScanSubnets,
		"protocols", collectorCfg.Protocols,
		"poll_interval", collectorCfg.DiscoveryInterval.String(),
		"sweep_interval", collectorCfg.SweepInterval.String(),
		"heartbeat_interval", collectorCfg.HeartbeatInterval.String(),
		"mtls", uploader.mtls,
		"spool_dir", collectorCfg.SpoolDir,
		"sweep_concurrency", collectorCfg.SweepConcurrency,
		"sweep_icmp", sweepICMPCapability(),
	)

	go runDiscoveryLoop(ctx, collectorCfg, uploader)
	// The spool is delivered continuously, not only after discovery (NFR-03).
	go runSpoolFlusher(ctx, uploader, collectorCfg.SpoolFlushInterval)
	go runHeartbeatLoop(ctx, collectorCfg, uploader, onHeartbeat)
	restart := make(chan string, 1)
	if collectorCfg.UpdateURL != "" {
		go runUpdateLoop(ctx, collectorCfg.UpdateInterval, newUpdateChecker(&collectorCfg), confirmed, restart)
	}
	go runAgentRelay(ctx, collectorCfg, uploader)
	// The collector's mTLS channel to the NATS endpoint (COL-08).
	go runNATS(ctx, collectorCfg)
	if collectorCfg.TrapListenAddr != "" {
		go runTrapReceiver(ctx, collectorCfg, uploader)
	}
	if collectorCfg.MetricsInterval > 0 && len(collectorCfg.PollMetrics) > 0 {
		go runMetricsLoop(ctx, collectorCfg, uploader)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
		slog.Info("collector shutting down")
	case version := <-restart:
		// The service manager (Restart=always) starts the new binary.
		slog.Info("collector restarting into the update", "version", version)
	}
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
	// paused is set while the discovery license of the organization is
	// expired (CH21, ENT-07): the collector neither scans nor spools.
	paused atomic.Bool
	// flushMu serializes spool deliveries; flushNow wakes the flusher.
	flushMu  sync.Mutex
	flushNow chan struct{}
}

// LicenseStatusHeader carries the discovery license status on heartbeat
// responses ("active" or "expired").
const LicenseStatusHeader = "Reticora-License-Status"

// problemLicenseExpired is the problem type of an ingest refused after the
// license expired (CH21).
const problemLicenseExpired = "https://reticora.io/problems/license-expired"

// errLicenseExpired: the backend refused the ingest because the discovery
// license expired. The batch is not spooled; it would never be accepted.
var errLicenseExpired = errors.New("discovery license expired")

// setLicenseStatus pauses or resumes discovery after a heartbeat or a refused
// ingest; every change is logged.
func (u *uploader) setLicenseStatus(status string) {
	paused := status == "expired"
	if u.paused.Swap(paused) == paused {
		return
	}
	if paused {
		slog.Warn("discovery paused: the discovery license expired; no scans and no spooling until it is renewed",
			"event", "collector.license.paused")
	} else {
		slog.Info("discovery resumed: the discovery license is active", "event", "collector.license.resumed")
	}
}

// newUploader builds the upload path: mTLS when certificate material is
// available, plus the disk spool used for offline buffering.
func newUploader(ctx context.Context, cfg collectorConfig) (*uploader, error) {
	u := &uploader{cfg: cfg, flushNow: make(chan struct{}, 1)}
	if cfg.SpoolDir != "" {
		u.spool = &buffer.DiskBuffer{
			Dir: cfg.SpoolDir, MaxBytes: cfg.SpoolMaxBytes, MaxAge: cfg.SpoolMaxAge,
			// Every loss is an event in the collector log and is reported to
			// the server with the next heartbeat (Stats.Dropped*).
			OnDrop: func(l buffer.Loss) {
				slog.Error("spool data lost",
					"event", "collector.spool.data_lost",
					"reason", l.Reason, "messages", l.Messages, "bytes", l.Bytes)
			},
		}
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

// runDiscoveryLoop runs what is due every tick (DIS-10): the sweeps on
// their schedule, the polls on their interval. A paused collector (expired
// license) or a saturated spool scans nothing.
func runDiscoveryLoop(ctx context.Context, cfg collectorConfig, up *uploader) {
	tick := cfg.ScheduleTick
	if tick <= 0 {
		tick = defaultScheduleTick
		if cfg.DiscoveryInterval > 0 && cfg.DiscoveryInterval < tick {
			tick = cfg.DiscoveryInterval
		}
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	sched, hosts := newScheduler(), &hostCache{}

	for {
		switch {
		case up.paused.Load():
			// CH21: after the license expired the collector does not scan;
			// the heartbeat resumes it once the license is renewed.
		case up.backpressure(ctx):
			slog.Warn("discovery paused: spool is saturated and the backend is unreachable",
				"event", "collector.spool.backpressure")
		default:
			runDueDiscovery(ctx, &cfg, up, sched, hosts, time.Now())
		}
		select {
		case <-ctx.Done():
			slog.Info("discovery loop stopped")
			return
		case <-ticker.C:
		}
	}
}

// runDueDiscovery runs the due protocols of every pass, uploads the
// records and reports the scope runs; it returns the number of records.
func runDueDiscovery(ctx context.Context, cfg *collectorConfig, up *uploader, sched *scheduler, hosts *hostCache, now time.Time) int {
	var records []plugins.DeviceRecord
	type scopeOutcome struct {
		scope   *collectorScope
		records int
		errs    []runError
		next    time.Time
	}
	var outcomes []scopeOutcome

	// One pass per discovery scope (DIS-09), or the configured subnets.
	for _, pass := range discoveryPasses(cfg) {
		due := pass.dueProtocols(sched, now)
		if len(due) == 0 {
			continue
		}
		slog.Info("discovery started", "pass", pass.key, "protocols", due)
		// Only the credentials of this pass (COL-02, DIS-09); every plugin
		// receives the namespace of its protocol.
		passCreds := credentialsFor(cfg, pass.scope)
		var passRecords []plugins.DeviceRecord
		var errs []runError
		for _, protocol := range due {
			if ctx.Err() != nil {
				break
			}
			plug, ok := pluginRegistry[protocol]
			if !ok {
				slog.Warn("unknown protocol plugin, skipping", "protocol", protocol)
				continue
			}
			targets := pass.targetsFor(protocol, hosts)
			slog.Info("running discovery plugin", "protocol", protocol, "targets", len(targets))
			creds := plugins.CredentialsFor(protocol, passCreds)
			hits, err := plug.Probe(ctx, targets, creds)
			if err != nil {
				slog.Error("discovery plugin error", "event", "discovery.plugin.failed", "protocol", protocol, "error", err)
				errs = append(errs, runError{Protocol: protocol, Message: err.Error()})
				continue
			}
			if protocol == "sweep" {
				reachable := hitTargets(hits)
				// The sweep is the reachability series of the CIs (MON-02).
				uploadSamples(ctx, up, reachabilitySamples(hosts.get(pass.key), reachable))
				hosts.set(pass.key, reachable)
			}
			collected, failed := collectHits(ctx, protocol, plug, hits, creds)
			errs = append(errs, failed...)
			if protocol == "power" {
				// UPS and PDU readings of the power devices (MON-02).
				uploadSamples(ctx, up, pollPowerValues(ctx, hits, plugins.CredentialsFor("snmp", passCreds)))
			}
			slog.Info("discovery plugin completed", "protocol", protocol, "probed", len(hits), "records", len(collected))
			passRecords = append(passRecords, collected...)
		}
		tagRecords(passRecords, pass.scope)
		records = append(records, passRecords...)
		outcomes = append(outcomes, scopeOutcome{scope: pass.scope, records: len(passRecords), errs: errs, next: pass.nextRun(sched)})
	}
	if len(outcomes) == 0 {
		return 0
	}
	records = deliverableRecords(ctx, records)
	// Upload the records in a compressed batch; the spool flusher delivers
	// what could not be sent.
	var uploadErr *runError
	if len(records) > 0 {
		if err := up.uploadRecordsAt(ctx, records, now); err != nil {
			slog.Error("failed to upload discovery records", "event", "discovery.upload.failed", "error", err, "count", len(records))
			uploadErr = &runError{Protocol: "upload", Cause: "upload_failed", Message: err.Error()}
		} else {
			slog.Info("discovery records uploaded", "count", len(records))
		}
	}
	for _, o := range outcomes {
		errs := o.errs
		if uploadErr != nil && o.records > 0 {
			errs = append(errs, *uploadErr)
		}
		reportScopeRun(ctx, o.scope, now, o.records, errs, o.next)
	}
	slog.Info("discovery completed", "duration_ms", time.Since(now).Milliseconds(), "total_records", len(records))
	return len(records)
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

// collectParallelism bounds the parallel Collect calls of one plugin.
const collectParallelism = 16

// collectHits runs Collect for every target that answered the probe
// (DIS-03) and returns its DeviceRecords and the targets whose collection
// failed (DIS-08). When Collect fails, the probe hit is reported instead,
// so a reachable device is never lost.
func collectHits(ctx context.Context, protocol string, plug plugins.Plugin, hits []plugins.Result, creds map[string]string) ([]plugins.DeviceRecord, []runError) {
	out := make([]plugins.DeviceRecord, len(hits))
	failures := make([]*runError, len(hits))
	sem := make(chan struct{}, collectParallelism)
	var wg sync.WaitGroup
	for i := range hits {
		hit := &hits[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			probedAt := time.Now()
			rec, err := plug.Collect(ctx, hit.IP, creds)
			if err != nil || rec == nil {
				if err != nil {
					slog.Warn("discovery target failed; reporting the probe result", "event", "discovery.target.failed",
						"protocol", protocol, "target", hit.IP, "error", err)
					failures[i] = &runError{Protocol: protocol, Target: hit.IP, Message: err.Error()}
				}
				out[i] = plugins.RecordFromResult(protocol, hit, probedAt)
				return
			}
			if rec.Identity.PrimaryMAC == "" {
				rec.Identity.PrimaryMAC = hit.MAC
			}
			out[i] = *rec
		}()
	}
	wg.Wait()
	// One classification chain for every record (DIS-02).
	for i := range out {
		classifier.ClassifyRecord(&out[i])
	}
	var errs []runError
	for _, f := range failures {
		if f != nil {
			errs = append(errs, *f)
		}
	}
	return out, errs
}

// classifier holds the shipped profiles plus those of RETICORA_PROFILES_DIR.
var classifier = profiles.Default()

// loadProfileDir adds the operator's profiles (updated OUI prefixes and
// device profiles) to the classifier.
func loadProfileDir(dir string) {
	if dir == "" {
		return
	}
	if err := classifier.LoadDir(dir); err != nil {
		slog.Error("loading device profiles failed; using the shipped profiles", "dir", dir, "error", err)
		return
	}
	slog.Info("device profiles loaded", "dir", dir, "profiles", len(classifier.List()))
}

// deliverableRecords resolves the address of targets configured by name and
// drops records that break the DeviceRecord contract (DIS-05); the server
// would reject them.
func deliverableRecords(ctx context.Context, records []plugins.DeviceRecord) []plugins.DeviceRecord {
	out := records[:0]
	for _, rec := range records {
		if rec.TargetIP == "" && rec.Identity.Hostname != "" {
			if addrs, err := net.DefaultResolver.LookupHost(ctx, rec.Identity.Hostname); err == nil && len(addrs) > 0 {
				rec.TargetIP = addrs[0]
			}
		}
		if violations := rec.Validate(); len(violations) > 0 {
			slog.Warn("dropping device record that breaks the DeviceRecord contract",
				"event", "collector.record.invalid", "source", rec.Source, "target", rec.TargetIP,
				"violations", fmt.Sprint(violations))
			continue
		}
		out = append(out, rec)
	}
	return out
}

// uploadRecords uploads records collected now; see uploadRecordsAt.
func (u *uploader) uploadRecords(ctx context.Context, records []plugins.DeviceRecord) error {
	return u.uploadRecordsAt(ctx, records, time.Now())
}

// uploadRecordsAt sends the DeviceRecords of a discovery cycle that started at
// sourceTime to the backend via compressed JSON POST. While older batches
// wait in the spool the new batch is queued behind them, so the backend
// receives data in source-time order (COL-05). When the upload fails and a
// spool directory is configured, the batch is persisted and delivered by
// flushSpool once the backend is reachable again.
func (u *uploader) uploadRecordsAt(ctx context.Context, records []plugins.DeviceRecord, sourceTime time.Time) error {
	cfg := u.cfg
	if cfg.ServerURL == "" || cfg.OrganizationID == "" || cfg.CollectorID == "" {
		slog.Warn("skipping upload due to incomplete configuration")
		return nil
	}

	if u.paused.Load() {
		return errLicenseExpired
	}
	payload, err := json.Marshal(plugins.DeviceRecordBatch{Records: records})
	if err != nil {
		return fmt.Errorf("marshal records: %w", err)
	}

	if u.spool != nil {
		if backlog, lenErr := u.spool.Len(ctx); lenErr == nil && backlog > 0 {
			if spoolErr := u.spoolResults(ctx, payload, sourceTime); spoolErr != nil {
				return fmt.Errorf("queue behind spool backlog: %w", spoolErr)
			}
			u.flushSpool(ctx)
			return nil
		}
	}

	if err := u.postPayload(ctx, payload); err != nil {
		if errors.Is(err, errLicenseExpired) {
			return err
		}
		if spoolErr := u.spoolResults(ctx, payload, sourceTime); spoolErr != nil {
			return fmt.Errorf("upload failed (%v) and spooling failed: %w", err, spoolErr)
		}
		return err
	}
	return nil
}

// postPayload delivers a single JSON payload: over the edge when the
// collector holds its NATS channel, else compressed to the HTTP ingest.
func (u *uploader) postPayload(ctx context.Context, payload []byte) error {
	if err := u.postViaEdge(ctx, payload); !errors.Is(err, errNoEdge) {
		return err
	}
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

	if resp.StatusCode == http.StatusForbidden {
		var problem struct {
			Type string `json:"type"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&problem) == nil && problem.Type == problemLicenseExpired {
			u.setLicenseStatus("expired")
			return errLicenseExpired
		}
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("upload failed with status %d", resp.StatusCode)
	}
	return nil
}

// postAgentTelemetry forwards an endpoint-agent telemetry payload to the
// backend's agent surface (not the discovery ingest used for device results).
func (u *uploader) postAgentTelemetry(ctx context.Context, token string, payload []byte) error {
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
	// The agent's own credential authenticates the telemetry (AGT-03).
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("telemetry upload request: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("telemetry upload failed with status %d", resp.StatusCode)
	case resp.StatusCode >= 400:
		return fmt.Errorf("%w: status %d", errTelemetryRejected, resp.StatusCode)
	}
	return nil
}

// errTelemetryRejected marks a 4xx answer: the backend refused the agent
// credential or payload, so the telemetry is not spooled for a retry.
var errTelemetryRejected = errors.New("agent telemetry rejected")

// spoolResults persists an undeliverable batch to the disk buffer. A full
// spool returns buffer.ErrSpoolFull; the loss is counted and reported by the
// buffer, and discovery pauses through backpressure.
func (u *uploader) spoolResults(ctx context.Context, payload []byte, sourceTime time.Time) error {
	if u.spool == nil {
		return nil
	}
	id, err := u.spool.Enqueue(ctx, buffer.Message{Topic: spoolTopic, Payload: payload, CreatedAt: sourceTime.UTC()})
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

// spoolFlushBatch is the number of messages read per spool pass; the flush
// continues with further passes until the spool is empty (no per-cycle cap).
const spoolFlushBatch = 64

// flushSpool delivers buffered batches in source-time order until the spool is
// empty. Delivery stops at the first failure so spooled batches are never
// reordered or dropped.
func (u *uploader) flushSpool(ctx context.Context) {
	if u.spool == nil || u.cfg.ServerURL == "" || u.paused.Load() {
		return
	}
	u.flushMu.Lock()
	defer u.flushMu.Unlock()
	delivered := 0
	defer func() {
		if delivered > 0 {
			u.logSpoolStats(ctx)
		}
	}()
	for {
		msgs, err := u.spool.PeekBatch(ctx, spoolFlushBatch)
		if err != nil {
			slog.Error("read upload spool failed", "error", err)
			return
		}
		progressed := false
		for _, msg := range msgs {
			if ctx.Err() != nil {
				return
			}
			var deliver func() error
			switch msg.Topic {
			case spoolTopic:
				deliver = func() error { return u.postPayload(ctx, msg.Payload) }
			case agentTelemetryTopic:
				deliver = func() error { return u.replayAgentTelemetry(ctx, msg.Payload) }
			default:
				continue
			}
			if err := deliver(); err != nil {
				if errors.Is(err, errLicenseExpired) {
					// The spooled batches stay until the license is renewed.
					slog.Warn("spool flush postponed; discovery license expired", "message_id", msg.ID)
					return
				}
				slog.Warn("spool flush postponed; backend still unreachable", "message_id", msg.ID, "error", err)
				return
			}
			if err := u.spool.Ack(ctx, []string{msg.ID}); err != nil {
				slog.Error("spool ack failed", "message_id", msg.ID, "error", err)
				return
			}
			delivered++
			progressed = true
			slog.Info("spooled discovery results delivered", "message_id", msg.ID)
		}
		if len(msgs) < spoolFlushBatch || !progressed {
			return
		}
	}
}

// replayAgentTelemetry delivers a spooled relay message to the agent
// telemetry endpoint. A message the backend rejects (4xx) is dropped instead
// of blocking the spool.
func (u *uploader) replayAgentTelemetry(ctx context.Context, message []byte) error {
	var msg relayMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		slog.Error("spooled agent telemetry unreadable; dropped", "error", err)
		return nil
	}
	err := u.postAgentTelemetry(ctx, msg.Token, msg.Telemetry)
	if errors.Is(err, errTelemetryRejected) {
		slog.Warn("spooled agent telemetry rejected; dropped", "error", err)
		return nil
	}
	return err
}

// backpressure reports whether collection has to pause because the spool is
// saturated (COL-05: throttle instead of dropping data).
func (u *uploader) backpressure(ctx context.Context) bool {
	if u.spool == nil {
		return false
	}
	saturated, err := u.spool.Saturated(ctx)
	if err != nil {
		slog.Warn("spool saturation unknown", "error", err)
		return false
	}
	return saturated
}

// runHeartbeatLoop sends heartbeats; onSuccess runs after every heartbeat
// the server accepted.
func runHeartbeatLoop(ctx context.Context, cfg collectorConfig, up *uploader, onSuccess func()) {
	ticker := time.NewTicker(cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		status, ok := postHeartbeat(ctx, up.client, cfg, up.spoolReport(ctx))
		if status != "" {
			up.setLicenseStatus(status)
		}
		if ok {
			// The backend is reachable: deliver the spool now.
			up.requestFlush()
			if onSuccess != nil {
				onSuccess()
			}
		}
		select {
		case <-ctx.Done():
			slog.Info("heartbeat loop stopped")
			return
		case <-ticker.C:
		}
	}
}

// spoolReport is the spool state sent with every heartbeat so the server sees
// backlog, backpressure and losses (NFR-04, OPS-06).
type spoolReport struct {
	Messages         int   `json:"messages"`
	Bytes            int64 `json:"bytes"`
	OldestAgeSeconds int64 `json:"oldest_age_seconds"`
	DroppedMessages  int64 `json:"dropped_messages"`
	DroppedBytes     int64 `json:"dropped_bytes"`
	Backpressure     bool  `json:"backpressure"`
}

type heartbeatBody struct {
	Spool *spoolReport `json:"spool,omitempty"`
}

func (u *uploader) spoolReport(ctx context.Context) *spoolReport {
	if u.spool == nil {
		return nil
	}
	stats, err := u.spool.Stats(ctx)
	if err != nil {
		return nil
	}
	return &spoolReport{
		Messages:         stats.Messages,
		Bytes:            stats.Bytes,
		OldestAgeSeconds: int64(stats.OldestAge.Seconds()),
		DroppedMessages:  stats.DroppedMessages,
		DroppedBytes:     stats.DroppedBytes,
		Backpressure:     u.backpressure(ctx),
	}
}

// postHeartbeat sends a heartbeat and returns the discovery license status of
// the response ("" when unknown) and whether the server accepted it.
func postHeartbeat(ctx context.Context, client *http.Client, cfg collectorConfig, spool *spoolReport) (status string, ok bool) {
	if cfg.ServerURL == "" || cfg.OrganizationID == "" || cfg.CollectorID == "" {
		slog.Warn("skipping heartbeat due to incomplete configuration",
			"server_url", cfg.ServerURL,
			"organization_id", cfg.OrganizationID,
			"collector_id", cfg.CollectorID,
		)
		return "", false
	}

	endpoint := strings.TrimRight(cfg.ServerURL, "/") + "/api/v1/collectors/" + cfg.CollectorID + "/heartbeat"
	body, err := json.Marshal(heartbeatBody{Spool: spool})
	if err != nil {
		slog.Error("encode heartbeat failed", "error", err)
		return "", false
	}
	if license, ok, handled := heartbeatViaEdge(ctx, body); handled {
		return license, ok
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		slog.Error("build heartbeat request failed", "error", err)
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Organization-ID", cfg.OrganizationID)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("heartbeat failed", "error", err, "endpoint", endpoint)
		return "", false
	}
	defer resp.Body.Close()

	slog.Info("heartbeat completed", "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	if resp.StatusCode >= 300 {
		return "", false
	}
	return resp.Header.Get(LicenseStatusHeader), true
}

// runTrapReceiver receives SNMP traps and forwards them as metric events to
// the monitoring ingest, so the existing alert rules can fire on traps (e.g.
// "any linkDown trap → critical alert"). Failed uploads are logged and
// dropped: traps are fire-and-forget events, spooling them like discovery
// results would delay alerts past the point where they are useful.
func runTrapReceiver(ctx context.Context, cfg collectorConfig, up *uploader) {
	if err := newTrapReceiver(cfg, up).Run(ctx); err != nil {
		slog.Error("snmp trap receiver stopped", "error", err)
	}
}

// newTrapReceiver builds the trap receiver. Traps arrive from any device,
// so only the locally configured community applies, never one of a server
// scope (COL-02).
func newTrapReceiver(cfg collectorConfig, up *uploader) *snmp.TrapReceiver {
	return &snmp.TrapReceiver{
		ListenAddr: cfg.TrapListenAddr,
		Community:  plugins.CredentialsFor("snmp", cfg.Credentials)["community"],
		Sink: func(ctx context.Context, ev snmp.TrapEvent) {
			slog.Info("snmp trap received", "source", ev.SourceIP, "trap_oid", ev.TrapOID)
			if err := up.uploadTrapEvent(ctx, ev); err != nil {
				slog.Warn("trap event upload failed", "source", ev.SourceIP, "trap_oid", ev.TrapOID, "error", err)
			}
		},
	}
}

// metricPoller polls numeric SNMP metrics of a target (snmp.Plugin).
type metricPoller interface {
	Poll(ctx context.Context, target string, creds map[string]string, metrics []snmp.PollMetric) ([]plugins.MetricSample, error)
}

// runMetricsLoop polls the configured numeric SNMP metrics on every scan
// target and uploads the samples to the monitoring ingest (Epic E:
// Collector-Polling). Polling runs independently of discovery so metric
// intervals can be much shorter than discovery cycles. The scan targets are
// the configured subnets, so only the locally configured credentials apply,
// never those of a server scope (COL-02).
func runMetricsLoop(ctx context.Context, cfg collectorConfig, up *uploader) {
	ticker := time.NewTicker(cfg.MetricsInterval)
	defer ticker.Stop()

	poller, ok := pluginRegistry["snmp"].(metricPoller)
	if !ok {
		slog.Error("snmp plugin unavailable; metric polling disabled")
		return
	}

	creds := plugins.CredentialsFor("snmp", cfg.Credentials)
	for {
		targets := expandSubnets(cfg.ScanSubnets)
		samples := make([]plugins.MetricSample, 0, len(targets)*len(cfg.PollMetrics))
		for _, target := range targets {
			if ctx.Err() != nil {
				break
			}
			polled, err := poller.Poll(ctx, target, creds, cfg.PollMetrics)
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

// agentTelemetryTopic is the spool topic of relayed agent telemetry; it is
// replayed to the agent telemetry endpoint, never to discovery (AGT-03).
const agentTelemetryTopic = "agent-telemetry"

// maxRelayMessage bounds one relay message.
const maxRelayMessage = 1 << 20

// relayMessage mirrors agent.RelayMessage of the backend: the agent's bearer
// credential and its telemetry in the backend contract.
type relayMessage struct {
	Token     string          `json:"token"`
	Telemetry json.RawMessage `json:"telemetry"`
}

// agentRelayTLS loads the relay's server certificate. Without one the relay
// does not start: agents talk TLS only (AGT-03).
func agentRelayTLS(cfg collectorConfig) (*tls.Config, error) {
	if cfg.AgentRelayCertFile == "" || cfg.AgentRelayKeyFile == "" {
		return nil, errors.New("RETICORA_AGENT_RELAY_TLS_CERT_FILE and RETICORA_AGENT_RELAY_TLS_KEY_FILE are not set")
	}
	cert, err := tls.LoadX509KeyPair(cfg.AgentRelayCertFile, cfg.AgentRelayKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load agent relay certificate: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}

// runAgentRelay accepts endpoint agents over TLS and forwards their telemetry
// to the backend's agent telemetry endpoint.
func runAgentRelay(ctx context.Context, cfg collectorConfig, up *uploader) {
	tlsConf, err := agentRelayTLS(cfg)
	if err != nil {
		slog.Warn("agent relay disabled", "reason", err)
		return
	}
	listener, err := tls.Listen("tcp", cfg.AgentRelayAddr, tlsConf)
	if err != nil {
		slog.Error("agent relay listen failed", "error", err)
		return
	}
	serveAgentRelay(ctx, listener, up)
}

// serveAgentRelay serves agent connections on listener until ctx ends.
func serveAgentRelay(ctx context.Context, listener net.Listener, up *uploader) {
	defer listener.Close()
	slog.Info("agent relay listening (TLS)", "addr", listener.Addr().String())

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

// handleAgentConnection reads one relay message and forwards the telemetry
// with the agent's credential, falling back to the disk spool when the cloud
// is unreachable. The agent gets an ack only once the telemetry is durably
// accepted (uploaded or spooled).
func handleAgentConnection(ctx context.Context, up *uploader, conn net.Conn) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()

	if err := conn.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		slog.Error("set deadline failed", "error", err)
		return
	}
	line, err := bufio.NewReader(io.LimitReader(conn, maxRelayMessage)).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		slog.Debug("agent read error", "remote", remoteAddr, "error", err)
		return
	}
	var msg relayMessage
	if err := json.Unmarshal(line, &msg); err != nil || strings.TrimSpace(msg.Token) == "" || len(msg.Telemetry) == 0 {
		slog.Warn("agent relay message rejected", "remote", remoteAddr)
		_, _ = conn.Write([]byte(`{"status":"error","reason":"invalid message"}` + "\n"))
		return
	}

	if up != nil {
		if err := up.postAgentTelemetry(ctx, msg.Token, msg.Telemetry); err != nil {
			if errors.Is(err, errTelemetryRejected) {
				// The backend refused the credential or the payload;
				// spooling would only replay the refusal.
				slog.Warn("agent telemetry rejected", "remote", remoteAddr, "error", err)
				_, _ = conn.Write([]byte(`{"status":"error","reason":"rejected"}` + "\n"))
				return
			}
			slog.Warn("agent telemetry upload failed, spooling", "remote", remoteAddr, "error", err)
			if serr := up.spoolAgentTelemetry(ctx, line); serr != nil {
				slog.Error("agent telemetry spool failed", "remote", remoteAddr, "error", serr)
				_, _ = conn.Write([]byte(`{"status":"error"}` + "\n"))
				return
			}
		}
	}
	_, _ = conn.Write([]byte(`{"status":"ok"}` + "\n"))
}

// spoolAgentTelemetry persists an undeliverable relay message under the agent
// telemetry topic.
func (u *uploader) spoolAgentTelemetry(ctx context.Context, message []byte) error {
	if u.spool == nil {
		return errors.New("no spool configured")
	}
	_, err := u.spool.Enqueue(ctx, buffer.Message{Topic: agentTelemetryTopic, Payload: message, CreatedAt: time.Now().UTC()})
	return err
}

func loadCollectorConfig() collectorConfig {
	creds := credentialsFromEnv(os.Getenv)
	updateURL := strings.TrimSpace(os.Getenv("RETICORA_UPDATE_URL"))
	channel, channelOK := updateChannelFromEnv(os.Getenv("RETICORA_UPDATE_CHANNEL"))
	if !channelOK {
		updateURL = ""
	}

	cfg := collectorConfig{
		ServerURL:          envOrDefault("RETICORA_SERVER_URL", "http://localhost:8080"),
		OrganizationID:     os.Getenv("RETICORA_ORGANIZATION_ID"),
		CollectorID:        os.Getenv("RETICORA_COLLECTOR_ID"),
		ScanSubnets:        csvEnvOrDefault("RETICORA_SCAN_SUBNETS", []string{"127.0.0.1/32"}),
		Protocols:          csvEnvOrDefault("RETICORA_DISCOVERY_PROTOCOLS", []string{"sweep", "snmp", "ssh"}),
		Simulate:           boolEnv("RETICORA_SIMULATE"),
		DiscoveryInterval:  max(durationEnvOrDefault("RETICORA_DISCOVERY_INTERVAL", defaultPollInterval), minPollInterval),
		SweepInterval:      max(durationEnvOrDefault("RETICORA_SWEEP_INTERVAL", defaultSweepInterval), minPollInterval),
		SpoolFlushInterval: durationEnvOrDefault("RETICORA_SPOOL_FLUSH_INTERVAL", defaultSpoolFlush),
		HeartbeatInterval:  durationEnvOrDefault("RETICORA_HEARTBEAT_INTERVAL", time.Minute),
		Credentials:        creds,
		RedfishTLSScopes:   tlsScopesFromEnv("RETICORA_REDFISH_TLS_SCOPES"),
		WMITLSScopes:       tlsScopesFromEnv("RETICORA_WMI_TLS_SCOPES"),
		SpoolDir:           envOrDefault("RETICORA_SPOOL_DIR", "/var/lib/reticora-collector/spool"),
		SpoolMaxBytes:      int64EnvOrDefault("RETICORA_SPOOL_MAX_BYTES", 1<<30), // 1 GiB default cap
		SpoolMaxAge:        durationEnvOrDefault("RETICORA_SPOOL_MAX_AGE", 72*time.Hour),
		TrapListenAddr:     os.Getenv("RETICORA_SNMP_TRAP_LISTEN"),
		MetricsInterval:    durationEnvOrDefault("RETICORA_METRICS_INTERVAL", 0),
		PollMetrics:        pollMetricsFromEnv(os.Getenv("RETICORA_SNMP_POLL_METRICS")),
		TLSCertPEM:         []byte(os.Getenv("RETICORA_TLS_CLIENT_CERT")),
		TLSKeyPEM:          []byte(os.Getenv("RETICORA_TLS_CLIENT_KEY")),
		TLSCAPEM:           []byte(os.Getenv("RETICORA_TLS_CA")),
		TLSCertFile:        os.Getenv("RETICORA_TLS_CLIENT_CERT_FILE"),
		TLSKeyFile:         os.Getenv("RETICORA_TLS_CLIENT_KEY_FILE"),
		TLSCAFile:          os.Getenv("RETICORA_TLS_CA_FILE"),
		TLSServerName:      os.Getenv("RETICORA_TLS_SERVER_NAME"),
		AgentRelayAddr:     envOrDefault("RETICORA_AGENT_RELAY_ADDR", ":9443"),
		AgentRelayCertFile: os.Getenv("RETICORA_AGENT_RELAY_TLS_CERT_FILE"),
		AgentRelayKeyFile:  os.Getenv("RETICORA_AGENT_RELAY_TLS_KEY_FILE"),
		CredentialsPath:    credentialsPath(),
		SweepConcurrency:   sweepConcurrencyFromEnv(os.Getenv("RETICORA_SWEEP_CONCURRENCY")),
		UpdateURL:          updateURL,
		UpdateChannel:      channel,
		UpdateInterval:     durationEnvOrDefault("RETICORA_UPDATE_INTERVAL", 6*time.Hour),
	}
	// After `enroll` the credentials file holds server URL and identity
	// (INS-06); the environment still wins.
	if err := applyEnrolledIdentity(context.Background(), &cfg, strings.TrimSpace(os.Getenv("RETICORA_SERVER_URL")) != ""); err != nil && !errors.Is(err, keystore.ErrNotFound) {
		slog.Warn("enrolled identity not applied", "path", cfg.CredentialsPath, "error", err)
	}
	return cfg
}

// errSimulateInProduction: RETICORA_SIMULATE is refused in production.
var errSimulateInProduction = errors.New("RETICORA_SIMULATE cannot be enabled in production")

// applySimulation switches the collector to the simulated fleet when
// RETICORA_SIMULATE is set (SIM-02): the only protocol is "simulate" on the
// fleet's subnet, and the records take the real upload and ingest path.
// Production refuses it.
func applySimulation(cfg *collectorConfig, environment string) error {
	if !cfg.Simulate {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(environment), "production") {
		return errSimulateInProduction
	}
	pluginRegistry["simulate"] = simulate.New()
	cfg.Protocols = []string{"simulate"}
	cfg.ScanSubnets = []string{simulate.Subnet}
	slog.Warn("collector runs the simulated device fleet", "event", "collector.simulate.enabled",
		"devices", simulate.DeviceCount, "subnet", simulate.Subnet)
	return nil
}

// boolEnv reports whether the variable is set to a true value.
func boolEnv(key string) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return err == nil && v
}

// newSweep builds the sweep plugin: ICMP, ARP and the DIS-01 ports with the
// configured parallelism.
func newSweep(cfg *collectorConfig) *sweep.Plugin {
	p := sweep.New()
	p.Concurrency = cfg.SweepConcurrency
	return p
}

// sweepICMPCapability names the ICMP socket the sweep can use (logged at
// start so a missing CAP_NET_RAW / ping_group_range is visible).
func sweepICMPCapability() string {
	if c := sweep.ICMPCapability(); c != "" {
		return c
	}
	return "unavailable (TCP and ARP only)"
}

// sweepConcurrencyFromEnv parses RETICORA_SWEEP_CONCURRENCY; values above
// sweep.MaxConcurrency are capped, invalid ones use the maximum.
func sweepConcurrencyFromEnv(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		if strings.TrimSpace(value) != "" {
			slog.Warn("invalid RETICORA_SWEEP_CONCURRENCY; using the maximum", "value", value, "max", sweep.MaxConcurrency)
		}
		return sweep.MaxConcurrency
	}
	if n > sweep.MaxConcurrency {
		slog.Warn("RETICORA_SWEEP_CONCURRENCY above the hard limit; capped", "value", n, "max", sweep.MaxConcurrency)
		return sweep.MaxConcurrency
	}
	return n
}

// credentialEnv maps the credential environment variables to their protocol
// namespace (see plugins.CredentialsFor). Redfish has its own account: the SSH
// account is never sent to BMCs.
var credentialEnv = []struct{ env, key string }{
	{"RETICORA_SNMP_COMMUNITY", "snmp.community"},
	{"RETICORA_SSH_USERNAME", "ssh.username"},
	{"RETICORA_SSH_PASSWORD", "ssh.password"},
	{"RETICORA_REDFISH_USERNAME", "redfish.username"},
	{"RETICORA_REDFISH_PASSWORD", "redfish.password"},
	{"RETICORA_SSH_PRIVATE_KEY", "ssh.private_key"},
	{"RETICORA_SSH_KEY_PASSPHRASE", "ssh.passphrase"},
	// SNMPv3 authPriv (DIS-04); a configured user switches SNMP to v3.
	{"RETICORA_SNMP_V3_USERNAME", "snmp.username"},
	{"RETICORA_SNMP_V3_AUTH_PROTOCOL", "snmp.auth_protocol"},
	{"RETICORA_SNMP_V3_AUTH_PASSWORD", "snmp.auth_password"},
	{"RETICORA_SNMP_V3_PRIV_PROTOCOL", "snmp.priv_protocol"},
	{"RETICORA_SNMP_V3_PRIV_PASSWORD", "snmp.priv_password"},
}

func credentialsFromEnv(getenv func(string) string) map[string]string {
	creds := make(map[string]string)
	for _, c := range credentialEnv {
		if value := getenv(c.env); value != "" {
			creds[c.key] = value
		}
	}
	return creds
}

// tlsScopesFromEnv reads the TLS scopes of a plugin from the variable name.
func tlsScopesFromEnv(name string) []plugins.TLSScope {
	scopes, err := parseTLSScopes(os.Getenv(name), os.ReadFile)
	if err != nil {
		// Fail closed: without the exception the default verification
		// applies and unverifiable devices are not contacted.
		slog.Error("invalid "+name+"; using default certificate verification", "error", err)
		return nil
	}
	return scopes
}

// parseTLSScopes parses RETICORA_REDFISH_TLS_SCOPES and
// RETICORA_WMI_TLS_SCOPES: entries separated by ";" in the form
// "<cidr>=<rule>[,<rule>...]" with the rules "ca:<path to PEM bundle>" and
// "pin:<hex SHA-256 of the leaf certificate's SubjectPublicKeyInfo>", e.g.
// "10.0.10.0/24=ca:/etc/reticora/bmc-ca.pem;10.0.20.5/32=pin:9f86d0…".
func parseTLSScopes(value string, readFile func(string) ([]byte, error)) ([]plugins.TLSScope, error) {
	var scopes []plugins.TLSScope
	for _, entry := range strings.Split(value, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		cidr, rules, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("scope %q: missing \"=\"", entry)
		}
		_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			return nil, fmt.Errorf("scope %q: %w", entry, err)
		}
		scope := plugins.TLSScope{Network: network}
		for _, rule := range strings.Split(rules, ",") {
			kind, arg, _ := strings.Cut(strings.TrimSpace(rule), ":")
			switch kind {
			case "ca":
				pem, readErr := readFile(arg)
				if readErr != nil {
					return nil, fmt.Errorf("scope %q: read CA bundle: %w", entry, readErr)
				}
				if scope.RootCAs == nil {
					scope.RootCAs = x509.NewCertPool()
				}
				if !scope.RootCAs.AppendCertsFromPEM(pem) {
					return nil, fmt.Errorf("scope %q: CA bundle %s contains no certificate", entry, arg)
				}
			case "pin":
				pin, hexErr := hex.DecodeString(strings.ReplaceAll(arg, ":", ""))
				if hexErr != nil || len(pin) != 32 {
					return nil, fmt.Errorf("scope %q: pin must be a hex SHA-256 hash", entry)
				}
				scope.PinsSHA256 = append(scope.PinsSHA256, pin)
			default:
				return nil, fmt.Errorf("scope %q: unknown rule %q (want ca: or pin:)", entry, kind)
			}
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
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

// Metric names of the baseline series (MON-02).
const (
	metricSweepReachable = "sweep_reachable"
)

// reachabilitySamples are the sweep results as a series: 1 for every
// reachable host, 0 for a host of the previous sweep that no longer
// answers. The server assigns them to CIs by the target label.
func reachabilitySamples(previous, reachable []string) []plugins.MetricSample {
	up := make(map[string]bool, len(reachable))
	samples := make([]plugins.MetricSample, 0, len(reachable)+len(previous))
	for _, host := range reachable {
		up[host] = true
		samples = append(samples, plugins.MetricSample{Name: metricSweepReachable, Value: 1,
			Labels: map[string]string{"target": host, "kind": "sweep"}})
	}
	for _, host := range previous {
		if !up[host] {
			samples = append(samples, plugins.MetricSample{Name: metricSweepReachable, Value: 0,
				Labels: map[string]string{"target": host, "kind": "sweep"}})
		}
	}
	return samples
}

// upsPollMetrics are the UPS-MIB readings of a power device (RFC 1628);
// the names are those the power plugin announces.
var upsPollMetrics = []snmp.PollMetric{
	{Name: "ups_battery_status", OID: "1.3.6.1.2.1.33.1.2.1.0"},
	{Name: "ups_battery_runtime_seconds", OID: "1.3.6.1.2.1.33.1.2.3.0"},
	{Name: "ups_battery_charge_percent", OID: "1.3.6.1.2.1.33.1.2.4.0"},
	{Name: "ups_output_load_percent", OID: "1.3.6.1.2.1.33.1.4.4.1.5.1"},
}

// pollPowerValues reads the UPS-MIB values of the power devices the power
// plugin found with the SNMP credentials of their pass; the runtime is
// reported in minutes and converted.
func pollPowerValues(ctx context.Context, hits []plugins.Result, creds map[string]string) []plugins.MetricSample {
	poller, ok := pluginRegistry["snmp"].(metricPoller)
	if !ok {
		return nil
	}
	var samples []plugins.MetricSample
	for i := range hits {
		if hits[i].IP == "" || ctx.Err() != nil {
			continue
		}
		polled, err := poller.Poll(ctx, hits[i].IP, creds, upsPollMetrics)
		if err != nil {
			slog.Debug("power value poll failed", "target", hits[i].IP, "error", err)
			continue
		}
		for j := range polled {
			polled[j].Labels["kind"] = "power"
			if polled[j].Name == "ups_battery_runtime_seconds" {
				polled[j].Value *= 60
			}
		}
		samples = append(samples, polled...)
	}
	return samples
}

// uploadSamples sends metric samples; a failure is logged, the next run
// sends fresh values.
func uploadSamples(ctx context.Context, up *uploader, samples []plugins.MetricSample) {
	if len(samples) == 0 || up == nil {
		return
	}
	if err := up.uploadMetricSamples(ctx, samples); err != nil {
		slog.Warn("metric sample upload failed", "count", len(samples), "error", err)
	}
}
