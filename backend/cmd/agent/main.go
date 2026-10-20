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
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
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
	// SpoolDir keeps the reports that could not be sent until the next
	// successful send (RETICORA_AGENT_SPOOL_DIR, default in the user cache
	// directory); empty disables the spool.
	SpoolDir string
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
		SpoolDir:        os.Getenv("RETICORA_AGENT_SPOOL_DIR"),
	}
	if cfg.SpoolDir == "" {
		if dir, err := os.UserCacheDir(); err == nil {
			cfg.SpoolDir = filepath.Join(dir, "reticora-agent", "spool")
		} else {
			slog.Warn("no spool directory; unsent telemetry is not kept", "error", err)
		}
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

// maxRedirects is the number of redirects the direct channel follows.
const maxRedirects = 3

// checkRedirect is the redirect policy of the direct channel: a redirect to
// any scheme but https is refused, so the credential never leaves TLS
// (AGT-03, F-064); at most maxRedirects are followed, and the Authorization
// header is dropped once a redirect leaves the host of the request.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("redirect to %s refused: the agent follows https redirects only", req.URL.Redacted())
	}
	if len(via) > maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	// Once any hop left the original host the header stays dropped, also
	// on a redirect back.
	for _, hop := range via[1:] {
		if hop.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
	}
	if req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}
	return nil
}

// httpClientFor returns the TLS client of the direct channel: mTLS when
// enrolled material is available, otherwise server-authenticated TLS. Both
// follow redirects only under checkRedirect.
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
		client := mtlsTransport.HTTPClient()
		client.CheckRedirect = checkRedirect
		return client, nil
	}
	conf, err := tlsConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: checkRedirect,
		Transport: &http.Transport{TLSClientConfig: conf, Proxy: http.ProxyFromEnvironment}}, nil
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
// or directly to the backend; both channels are TLS only. A report that
// cannot be sent is logged and spooled; after a successful send the spool
// is delivered, oldest report first.
func collectAndSend(ctx context.Context, cfg agentConfig) {
	if cfg.Token == "" {
		slog.Error("no agent credential configured (RETICORA_AGENT_TOKEN or RETICORA_AGENT_TOKEN_FILE); telemetry not sent")
		return
	}
	payload := buildTelemetry(cfg)
	if err := deliver(ctx, cfg, payload); err != nil {
		slog.Error("telemetry send failed, report spooled", "error", err)
		spoolTelemetry(ctx, cfg, payload)
		return
	}
	flushSpool(ctx, cfg)
}

// deliver sends one report through the relay or, without relay or when
// the relay fails, directly.
func deliver(ctx context.Context, cfg agentConfig, payload agent.TelemetryPayload) error {
	if cfg.CollectorRelay != "" {
		err := sendViaRelay(ctx, cfg, payload)
		if err == nil || cfg.ServerURL == "" {
			return err
		}
		slog.Warn("relay send failed, trying direct", "error", err)
	}
	return sendDirect(ctx, cfg, payload)
}

// Spool limits: the buffer keeps unsent reports for at least 24 hours
// (buffer.DefaultMinRetention) and at most a week, within maxSpoolBytes.
const (
	maxSpoolBytes = 32 << 20
	maxSpoolAge   = 7 * 24 * time.Hour
	spoolTopic    = "agent.telemetry"
)

// spoolFor returns the spool of the agent, or nil when none is configured.
func spoolFor(cfg agentConfig) *buffer.DiskBuffer {
	if cfg.SpoolDir == "" {
		return nil
	}
	return &buffer.DiskBuffer{Dir: cfg.SpoolDir, MaxBytes: maxSpoolBytes, MaxAge: maxSpoolAge,
		OnDrop: func(l buffer.Loss) {
			slog.Warn("telemetry spool dropped reports", "messages", l.Messages, "bytes", l.Bytes, "reason", l.Reason)
		}}
}

// spoolTelemetry keeps an unsent report for the next successful send.
func spoolTelemetry(ctx context.Context, cfg agentConfig, payload agent.TelemetryPayload) {
	spool := spoolFor(cfg)
	if spool == nil {
		slog.Warn("no telemetry spool configured; report dropped")
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("spool telemetry", "error", err)
		return
	}
	if _, err = spool.Enqueue(ctx, buffer.Message{Topic: spoolTopic, Payload: data, CreatedAt: payload.CollectedAt}); err != nil {
		slog.Error("spool telemetry", "dir", cfg.SpoolDir, "error", err)
	}
}

// flushSpool delivers the spooled reports, oldest first, until the spool is
// empty or a send fails (the rest stays for the next run). A report the
// backend rejects as invalid is dropped so it cannot block the spool.
func flushSpool(ctx context.Context, cfg agentConfig) {
	spool := spoolFor(cfg)
	if spool == nil {
		return
	}
	for {
		batch, err := spool.PeekBatch(ctx, 20)
		if err != nil {
			slog.Error("read telemetry spool", "dir", cfg.SpoolDir, "error", err)
			return
		}
		if len(batch) == 0 {
			return
		}
		for _, msg := range batch {
			var payload agent.TelemetryPayload
			if err = json.Unmarshal(msg.Payload, &payload); err == nil {
				err = deliver(ctx, cfg, payload)
			}
			var rejected *rejectedError
			switch {
			case err == nil:
			case errors.As(err, &rejected) && rejected.permanent():
				slog.Warn("spooled report rejected, dropped", "id", msg.ID, "error", err)
			case errors.As(err, new(*json.SyntaxError)), errors.As(err, new(*json.UnmarshalTypeError)):
				slog.Warn("spooled report unreadable, dropped", "id", msg.ID, "error", err)
			default:
				slog.Warn("spooled telemetry not sent, kept", "pending_from", msg.ID, "error", err)
				return
			}
			if err = spool.Ack(ctx, []string{msg.ID}); err != nil {
				slog.Error("acknowledge spooled telemetry", "id", msg.ID, "error", err)
				return
			}
		}
	}
}

// rejectedError is a telemetry report the backend answered with an error
// status.
type rejectedError struct{ status int }

func (e *rejectedError) Error() string { return fmt.Sprintf("telemetry rejected: status %d", e.status) }

// permanent reports whether sending the same report again cannot succeed.
func (e *rejectedError) permanent() bool {
	switch e.status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
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
		return &rejectedError{status: resp.StatusCode}
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
