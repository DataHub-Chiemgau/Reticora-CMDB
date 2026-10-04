// Package main provides the entry point for the Reticora Edge Agent.
// The agent runs on-premises at customer sites and communicates with the
// Reticora Cloud backend via mTLS-secured connections.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/transport"
)

const agentVersion = "0.1.0"

type agentConfig struct {
	CollectorRelay string
	ServerURL      string
	OrganizationID string
	AgentID        string
	Hostname       string
	Interval       time.Duration
	// mTLS material loaded from the enrollment keystore; when present the
	// agent authenticates with its client certificate instead of headers.
	CertPEM []byte
	KeyPEM  []byte
	CAPEM   []byte
	// CredentialsPath is the keystore written by edge enrollment.
	CredentialsPath string
	TLSServerName   string
	// Token is the agent credential returned by the enrollment
	// (RETICORA_AGENT_TOKEN or the file RETICORA_AGENT_TOKEN_FILE). Every
	// report carries it; without it the agent sends nothing (AGT-03).
	Token string
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
	cfg := agentConfig{
		CollectorRelay:  os.Getenv("RETICORA_COLLECTOR_RELAY"),
		ServerURL:       os.Getenv("RETICORA_SERVER_URL"),
		OrganizationID:  os.Getenv("RETICORA_ORGANIZATION_ID"),
		AgentID:         envOrDefault("RETICORA_AGENT_ID", hostname),
		Hostname:        hostname,
		Interval:        durationEnvOrDefault("RETICORA_AGENT_INTERVAL", 60*time.Second),
		CredentialsPath: os.Getenv("RETICORA_CREDENTIALS_PATH"),
		TLSServerName:   os.Getenv("RETICORA_TLS_SERVER_NAME"),
		Token:           strings.TrimSpace(os.Getenv("RETICORA_AGENT_TOKEN")),
	}
	if path := os.Getenv("RETICORA_AGENT_TOKEN_FILE"); cfg.Token == "" && path != "" {
		if data, err := os.ReadFile(path); err == nil {
			cfg.Token = strings.TrimSpace(string(data))
		} else {
			slog.Error("failed to read agent token", "path", path, "error", err)
		}
	}
	if path := os.Getenv("RETICORA_TLS_CA_FILE"); len(cfg.CAPEM) == 0 && path != "" {
		if data, err := os.ReadFile(path); err == nil {
			cfg.CAPEM = data
		} else {
			slog.Error("failed to read CA bundle", "path", path, "error", err)
		}
	}

	// Load the enrolled client identity from the keystore (edge enrollment
	// wrote it); mTLS replaces the insecure org/agent headers.
	if cfg.CredentialsPath != "" {
		creds, err := (&keystore.FileStore{Path: cfg.CredentialsPath}).Load(context.Background())
		if err == nil {
			cfg.CertPEM = []byte(creds.ClientCertificatePEM)
			cfg.KeyPEM = []byte(creds.ClientPrivateKeyPEM)
			cfg.CAPEM = []byte(creds.CertificateAuthority)
		} else if !errors.Is(err, keystore.ErrNotFound) {
			slog.Warn("failed to load agent credentials", "path", cfg.CredentialsPath, "error", err)
		}
	}
	return cfg
}

// tlsConfig is the client TLS configuration of both channels: the enrolled
// CA (or the system roots) and, when enrolled, the client certificate. There
// is no plaintext fallback (AGT-03).
func tlsConfig(cfg agentConfig) (*tls.Config, error) {
	conf := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.TLSServerName}
	if len(cfg.CAPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CAPEM) {
			return nil, errors.New("CA bundle contains no certificate")
		}
		conf.RootCAs = pool
	}
	if len(cfg.CertPEM) > 0 && len(cfg.KeyPEM) > 0 {
		cert, err := tls.X509KeyPair(cfg.CertPEM, cfg.KeyPEM)
		if err != nil {
			return nil, fmt.Errorf("client certificate: %w", err)
		}
		conf.Certificates = []tls.Certificate{cert}
	}
	return conf, nil
}

// httpClientFor returns the TLS client of the direct channel: mTLS when
// enrolled material is available, otherwise server-authenticated TLS.
func httpClientFor(cfg agentConfig) (*http.Client, error) {
	if len(cfg.CertPEM) > 0 && len(cfg.KeyPEM) > 0 {
		mtlsTransport, err := transport.NewMTLS(transport.Config{
			ServerName:    cfg.TLSServerName,
			RootCAsPEM:    cfg.CAPEM,
			ClientCertPEM: cfg.CertPEM,
			ClientKeyPEM:  cfg.KeyPEM,
			Timeout:       15 * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("mTLS setup: %w", err)
		}
		return mtlsTransport.HTTPClient(), nil
	}
	conf, err := tlsConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: conf, Proxy: http.ProxyFromEnvironment}}, nil
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

// buildTelemetry assembles the report in the backend's contract
// (agent.TelemetryPayload, POST /api/v1/agents/telemetry).
func buildTelemetry(cfg agentConfig) agent.TelemetryPayload {
	return agent.TelemetryPayload{
		AgentID:     cfg.AgentID,
		Hostname:    cfg.Hostname,
		Version:     agentVersion,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Metrics:     collectMetrics(),
		SystemInfo:  collectSystemInfo(),
		IPAddress:   outboundIP(),
		CollectedAt: time.Now().UTC(),
	}
}

// collectAndSend sends the telemetry through the collector relay (preferred)
// or directly to the backend; both channels are TLS only.
func collectAndSend(ctx context.Context, cfg agentConfig) {
	if cfg.Token == "" {
		slog.Error("no agent credential configured (RETICORA_AGENT_TOKEN or RETICORA_AGENT_TOKEN_FILE); telemetry not sent")
		return
	}
	payload := buildTelemetry(cfg)
	if cfg.CollectorRelay != "" {
		err := sendViaRelay(ctx, cfg, payload)
		if err == nil {
			return
		}
		if cfg.ServerURL == "" {
			slog.Error("telemetry send via relay failed", "error", err)
			return
		}
		slog.Warn("relay send failed, trying direct", "error", err)
	}
	if err := sendDirect(ctx, cfg, payload); err != nil {
		slog.Error("telemetry send failed", "error", err)
	}
}

// sendViaRelay sends one relay message over TLS to the collector relay and
// waits for its acknowledgement.
func sendViaRelay(ctx context.Context, cfg agentConfig, payload agent.TelemetryPayload) error {
	conf, err := tlsConfig(cfg)
	if err != nil {
		return err
	}
	if conf.ServerName == "" {
		host, _, splitErr := net.SplitHostPort(cfg.CollectorRelay)
		if splitErr != nil {
			return fmt.Errorf("relay address: %w", splitErr)
		}
		conf.ServerName = host
	}
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: conf}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.CollectorRelay)
	if err != nil {
		return fmt.Errorf("connect to relay: %w", err)
	}
	defer conn.Close()

	data, err := json.Marshal(agent.RelayMessage{Token: cfg.Token, Telemetry: payload})
	if err != nil {
		return fmt.Errorf("marshal relay message: %w", err)
	}
	if err = conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}
	if _, err = conn.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write to relay: %w", err)
	}
	ack, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read relay ack: %w", err)
	}
	if !strings.Contains(ack, `"ok"`) {
		return fmt.Errorf("relay rejected the telemetry: %s", strings.TrimSpace(ack))
	}
	return nil
}

// sendDirect posts the telemetry to the backend over HTTPS with the agent
// credential. A plain http:// server URL is refused.
func sendDirect(ctx context.Context, cfg agentConfig, payload agent.TelemetryPayload) error {
	if cfg.ServerURL == "" {
		return fmt.Errorf("no server URL configured")
	}
	u, err := url.Parse(cfg.ServerURL)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("server URL %q is not https; the agent never sends telemetry unencrypted", cfg.ServerURL)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telemetry: %w", err)
	}
	endpoint := strings.TrimRight(cfg.ServerURL, "/") + "/api/v1/agents/telemetry"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	client, err := httpClientFor(cfg)
	if err != nil {
		return err
	}
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

// outboundIP is the local address of the default route, the agent's network
// fingerprint for the site suggestion (AGT-06). No packet is sent.
func outboundIP() string {
	conn, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return ""
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

// collectMetrics gathers basic system metrics as numbers (the backend
// stores them as time series).
func collectMetrics() map[string]float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return map[string]float64{
		"goroutines": float64(runtime.NumGoroutine()),
		"heap_alloc": float64(m.HeapAlloc),
		"sys_memory": float64(m.Sys),
		"num_gc":     float64(m.NumGC),
		"num_cpu":    float64(runtime.NumCPU()),
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
