// Package main provides the entry point for the Reticora Edge Agent.
// The agent runs on-premises at customer sites and communicates with the
// Reticora Cloud backend via mTLS-secured connections.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const agentVersion = "0.1.0"

type agentConfig struct {
	CollectorRelay string
	ServerURL      string
	OrganizationID string
	AgentID        string
	Hostname       string
	Interval       time.Duration
}

// TelemetryPayload is the data the agent reports on each cycle.
type TelemetryPayload struct {
	AgentID        string            `json:"agent_id"`
	OrganizationID string            `json:"organization_id"`
	Hostname       string            `json:"hostname"`
	Version        string            `json:"version"`
	OS             string            `json:"os"`
	Arch           string            `json:"arch"`
	Timestamp      time.Time         `json:"timestamp"`
	Metrics        map[string]any    `json:"metrics,omitempty"`
	SystemInfo     map[string]string `json:"system_info,omitempty"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := loadAgentConfig()
	slog.Info("starting reticora agent", "version", agentVersion, "hostname", cfg.Hostname, "os", runtime.GOOS, "arch", runtime.GOARCH)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runTelemetryLoop(ctx, cfg)
	go runHeartbeat(ctx, cfg)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("received signal, shutting down", "signal", sig)
	case <-ctx.Done():
	}

	cancel()
	fmt.Println("agent stopped")
}

func loadAgentConfig() agentConfig {
	hostname, _ := os.Hostname()
	return agentConfig{
		CollectorRelay: envOrDefault("RETICORA_COLLECTOR_RELAY", "localhost:9443"),
		ServerURL:      envOrDefault("RETICORA_SERVER_URL", "http://localhost:8080"),
		OrganizationID: os.Getenv("RETICORA_ORGANIZATION_ID"),
		AgentID:        envOrDefault("RETICORA_AGENT_ID", hostname),
		Hostname:       hostname,
		Interval:       durationEnvOrDefault("RETICORA_AGENT_INTERVAL", 60*time.Second),
	}
}

// runTelemetryLoop collects and sends system telemetry at configured intervals.
func runTelemetryLoop(ctx context.Context, cfg agentConfig) {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		collectAndSend(ctx, cfg)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// collectAndSend gathers telemetry and sends it to the collector relay or directly to backend.
func collectAndSend(ctx context.Context, cfg agentConfig) {
	payload := TelemetryPayload{
		AgentID:        cfg.AgentID,
		OrganizationID: cfg.OrganizationID,
		Hostname:       cfg.Hostname,
		Version:        agentVersion,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		Timestamp:      time.Now().UTC(),
		Metrics:        collectMetrics(),
		SystemInfo:     collectSystemInfo(),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal telemetry", "error", err)
		return
	}

	// Try collector relay first (preferred path), fall back to direct HTTP
	if cfg.CollectorRelay != "" {
		if err := sendViaRelay(ctx, cfg.CollectorRelay, data); err != nil {
			slog.Debug("relay send failed, trying direct", "error", err)
			if err := sendDirect(ctx, cfg, data); err != nil {
				slog.Error("telemetry send failed", "error", err)
			}
		}
	} else {
		if err := sendDirect(ctx, cfg, data); err != nil {
			slog.Error("telemetry send failed", "error", err)
		}
	}
}

// sendViaRelay sends telemetry over a TCP connection to the collector relay.
func sendViaRelay(ctx context.Context, addr string, data []byte) error {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to relay: %w", err)
	}
	defer conn.Close()

	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write to relay: %w", err)
	}

	// Read acknowledgement
	buf := make([]byte, 256)
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read relay ack: %w", err)
	}
	slog.Debug("relay ack received", "response", string(buf[:n]))
	return nil
}

// sendDirect sends telemetry directly to the backend via HTTP POST.
func sendDirect(ctx context.Context, cfg agentConfig, data []byte) error {
	if cfg.ServerURL == "" {
		return fmt.Errorf("no server URL configured")
	}

	endpoint := strings.TrimRight(cfg.ServerURL, "/") + "/api/v1/agents/telemetry"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.OrganizationID != "" {
		req.Header.Set("X-Organization-ID", cfg.OrganizationID)
	}
	req.Header.Set("X-Agent-ID", cfg.AgentID)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send telemetry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("telemetry rejected: status %d", resp.StatusCode)
	}
	return nil
}

// runHeartbeat sends periodic heartbeat signals.
func runHeartbeat(ctx context.Context, cfg agentConfig) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			slog.Debug("heartbeat", "agent_id", cfg.AgentID)
		}
	}
}

// collectMetrics gathers basic system metrics.
func collectMetrics() map[string]any {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return map[string]any{
		"goroutines": runtime.NumGoroutine(),
		"heap_alloc": m.HeapAlloc,
		"sys_memory": m.Sys,
		"num_gc":     m.NumGC,
		"num_cpu":    runtime.NumCPU(),
	}
}

// collectSystemInfo gathers basic system identification.
func collectSystemInfo() map[string]string {
	hostname, _ := os.Hostname()
	return map[string]string{
		"hostname": hostname,
		"os":       runtime.GOOS,
		"arch":     runtime.GOARCH,
		"go":       runtime.Version(),
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationEnvOrDefault(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return d
}
