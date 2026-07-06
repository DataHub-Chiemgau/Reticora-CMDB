// Package main provides the entry point for the Reticora Collector.
// The Collector runs in customer networks and handles Discovery, Provisioning and Agent-Relay.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
)

type collectorConfig struct {
	ServerURL         string
	OrganizationID    string
	CollectorID       string
	ScanSubnets       []string
	Protocols         []string
	DiscoveryInterval time.Duration
	HeartbeatInterval time.Duration
}

func main() {
	cfg := config.Load()
	collectorCfg := loadCollectorConfig()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	slog.Info("starting collector", "environment", cfg.Environment)

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	_ = tlsConfig

	natsURL := cfg.NATSUrl
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}
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

		for _, protocol := range cfg.Protocols {
			slog.Debug("protocol plugin pending implementation", "protocol", protocol)
			// TODO: invoke protocol-specific discovery plugins (SNMP, SSH, WMI, VMware, ...)
		}

		slog.Info("discovery cycle completed", "duration_ms", time.Since(cycleStart).Milliseconds())

		select {
		case <-ctx.Done():
			slog.Info("discovery loop stopped")
			return
		case <-ticker.C:
		}
	}
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
	<-ctx.Done()
}

func loadCollectorConfig() collectorConfig {
	return collectorConfig{
		ServerURL:         envOrDefault("RETICORA_SERVER_URL", "http://localhost:8080"),
		OrganizationID:    os.Getenv("RETICORA_ORGANIZATION_ID"),
		CollectorID:       os.Getenv("RETICORA_COLLECTOR_ID"),
		ScanSubnets:       csvEnvOrDefault("RETICORA_SCAN_SUBNETS", []string{"127.0.0.1/32"}),
		Protocols:         csvEnvOrDefault("RETICORA_DISCOVERY_PROTOCOLS", []string{"snmp", "ssh"}),
		DiscoveryInterval: durationEnvOrDefault("RETICORA_DISCOVERY_INTERVAL", 15*time.Minute),
		HeartbeatInterval: durationEnvOrDefault("RETICORA_HEARTBEAT_INTERVAL", time.Minute),
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
