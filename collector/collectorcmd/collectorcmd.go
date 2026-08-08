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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
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

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	_ = tlsConfig

	natsURL := envOrDefault("RETICORA_NATS_URL", "nats://localhost:4222")
	slog.Info("collector configured",
		"nats_url", natsURL,
		"server_url", collectorCfg.ServerURL,
		"scan_subnets", collectorCfg.ScanSubnets,
		"protocols", collectorCfg.Protocols,
		"discovery_interval", collectorCfg.DiscoveryInterval.String(),
		"heartbeat_interval", collectorCfg.HeartbeatInterval.String(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runDiscoveryLoop(ctx, collectorCfg)
	go runHeartbeatLoop(ctx, collectorCfg)
	go runAgentRelay(ctx)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("collector shutting down")
	cancel()
}

func runDiscoveryLoop(ctx context.Context, cfg collectorConfig) {
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

		// Upload results to backend in a compressed batch
		if len(allResults) > 0 {
			if err := uploadResults(ctx, cfg, allResults); err != nil {
				slog.Error("failed to upload discovery results", "error", err, "count", len(allResults))
			} else {
				slog.Info("discovery results uploaded", "count", len(allResults))
			}
		}

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

// uploadResults sends discovery results to the backend via compressed JSON POST.
func uploadResults(ctx context.Context, cfg collectorConfig, results []plugins.Result) error {
	if cfg.ServerURL == "" || cfg.OrganizationID == "" || cfg.CollectorID == "" {
		slog.Warn("skipping upload due to incomplete configuration")
		return nil
	}

	payload, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}

	// Compress with gzip
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(payload); err != nil {
		return fmt.Errorf("compress results: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("finalize compression: %w", err)
	}

	endpoint := strings.TrimRight(cfg.ServerURL, "/") + "/api/v1/discovery/ingest"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return fmt.Errorf("build upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-Organization-ID", cfg.OrganizationID)
	req.Header.Set("X-Collector-ID", cfg.CollectorID)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("upload failed with status %d", resp.StatusCode)
	}
	return nil
}

func runHeartbeatLoop(ctx context.Context, cfg collectorConfig) {
	ticker := time.NewTicker(cfg.HeartbeatInterval)
	defer ticker.Stop()

	client := &http.Client{Timeout: 10 * time.Second}
	for {
		postHeartbeat(ctx, client, cfg)
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

func runAgentRelay(ctx context.Context) {
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
		go handleAgentConnection(ctx, conn)
	}
}

// handleAgentConnection processes a single agent connection, reading telemetry data.
func handleAgentConnection(ctx context.Context, conn net.Conn) {
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
	}
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

func (c collectorConfig) String() string {
	return fmt.Sprintf("server=%s collector=%s", c.ServerURL, c.CollectorID)
}
