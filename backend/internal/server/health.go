package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RequiredExtensions are the PostgreSQL extensions the migrations install and
// the server relies on (TEC-06).
var RequiredExtensions = []string{"pgcrypto", "btree_gist", "timescaledb"}

// readinessTimeout bounds every single check so a hanging dependency turns
// into "unavailable" instead of a hanging probe.
const readinessTimeout = 2 * time.Second

// readinessDetailTimeout bounds every detail. A detail is informational, so
// it must not hold up the answer: the probe of the orchestrator times out
// after one second by default, and a slow answer would take the instance out
// of the load balancer although every check passed.
const readinessDetailTimeout = 300 * time.Millisecond

// ReadinessCheck is one dependency that /readyz verifies (OPS-01). Check
// returns nil when the dependency is usable. Detail optionally reports
// operational figures under details.<Name> (the ingest backlog per lane,
// WP-334) within readinessDetailTimeout, or "unavailable"; a check with only
// a Detail is informational: it is not listed in checks and never changes
// the status, and a slow Detail never delays the answer by more than
// readinessDetailTimeout. Address is the host:port the check connects to,
// without credentials; the log line of a failing check names it.
type ReadinessCheck struct {
	Name    string
	Address string
	Check   func(ctx context.Context) error
	Detail  func(ctx context.Context) (any, error)
}

type readinessResponse struct {
	Status  string            `json:"status"`
	Checks  map[string]string `json:"checks"`
	Details map[string]any    `json:"details,omitempty"`
}

// readinessHandler runs all checks in parallel and answers 200 when every
// check passes and 503 otherwise. The endpoint is unauthenticated, so the
// response only names the failing dependency; the error goes to the log,
// once per change of a check's status (readinessLog). Details carry no
// tenant data.
func readinessHandler(checks []ReadinessCheck) http.HandlerFunc {
	transitions := &readinessLog{failing: map[string]bool{}}
	return func(w http.ResponseWriter, r *http.Request) {
		resp := readinessResponse{Status: "ready", Checks: make(map[string]string, len(checks))}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, c := range checks {
			if c.Detail != nil {
				wg.Add(1)
				go func(c ReadinessCheck) {
					defer wg.Done()
					detail := readinessDetail(r.Context(), c)
					mu.Lock()
					defer mu.Unlock()
					if resp.Details == nil {
						resp.Details = map[string]any{}
					}
					resp.Details[c.Name] = detail
				}(c)
			}
			if c.Check == nil {
				continue
			}
			wg.Add(1)
			go func(c ReadinessCheck) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
				defer cancel()
				err := c.Check(ctx)
				transitions.record(&c, err)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					resp.Checks[c.Name] = "unavailable"
					resp.Status = "not_ready"
					return
				}
				resp.Checks[c.Name] = "ok"
			}(c)
		}
		wg.Wait()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if resp.Status != "ready" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// readinessLog remembers which checks failed on the last probe. /readyz is
// probed every few seconds, so a failing dependency is logged once when its
// check turns unavailable — with name, address and error, which the
// installer shows from the log tail when the server does not become healthy
// — and once when it recovers, not on every probe.
type readinessLog struct {
	mu      sync.Mutex
	failing map[string]bool
}

// record logs the result err of c when the status of c changed; a check
// starts as ok.
func (l *readinessLog) record(c *ReadinessCheck, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if failing := err != nil; failing == l.failing[c.Name] {
		return
	}
	l.failing[c.Name] = err != nil
	if err != nil {
		slog.Warn("readiness check failed", "check", c.Name, "address", c.Address, "error", err)
		return
	}
	slog.Info("readiness check recovered", "check", c.Name, "address", c.Address)
}

// readinessDetail returns the detail of c, or "unavailable" when it fails or
// takes longer than readinessDetailTimeout; a Detail that outlasts its
// context finishes in the background. A failure is logged as a warning,
// except a detail that has no value yet (discovery.ErrNoBacklogSample: the
// ingest worker has not sampled since the start): that state is expected,
// and the endpoint is unauthenticated and probed every few seconds.
func readinessDetail(parent context.Context, c ReadinessCheck) any {
	ctx, cancel := context.WithTimeout(parent, readinessDetailTimeout)
	defer cancel()
	type result struct {
		detail any
		err    error
	}
	done := make(chan result, 1)
	go func() {
		detail, err := c.Detail(ctx)
		done <- result{detail, err}
	}()
	var err error
	select {
	case res := <-done:
		if res.err == nil {
			return res.detail
		}
		err = res.err
	case <-ctx.Done():
		err = ctx.Err()
	}
	if errors.Is(err, discovery.ErrNoBacklogSample) {
		slog.Debug("readiness detail not available yet", "check", c.Name, "error", err)
	} else {
		slog.Warn("readiness detail failed", "check", c.Name, "error", err)
	}
	return map[string]string{"error": "unavailable"}
}

// DDLGuardEventTrigger is the event trigger of migration 000078 that
// restricts the application roles to index DDL (CH19). The update of the
// TimescaleDB extension disables it for the duration of the update
// (scripts/ops/timescaledb-update.sh, WP-604); an aborted update could leave
// it disabled.
const DDLGuardEventTrigger = "reticora_restrict_app_ddl"

// DatabaseReadiness checks that PostgreSQL answers, that the given
// extensions are installed and that DDLGuardEventTrigger exists and is
// enabled (OPS-01, WP-604). Both are catalog lookups. The first check — the
// start check of cmd/server — also logs a warning for every given extension
// that is not at the default version of its installed library: after an
// image update TimescaleDB keeps its version until
// scripts/ops/timescaledb-update.sh raises it. That comparison reads the
// extension control files of the server, so it is not repeated per probe,
// and the versions go to the log only, not to the unauthenticated /readyz.
func DatabaseReadiness(pool *pgxpool.Pool, extensions []string) ReadinessCheck {
	var versionsCompared atomic.Bool
	conn := pool.Config().ConnConfig
	address := net.JoinHostPort(conn.Host, strconv.Itoa(int(conn.Port)))
	return ReadinessCheck{Name: "database", Address: address, Check: func(ctx context.Context) error {
		var (
			missing []string
			trigger *string
		)
		err := pool.QueryRow(ctx, `
			SELECT ARRAY(SELECT e FROM unnest($1::text[]) AS e
			              WHERE NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = e)
			              ORDER BY e),
			       (SELECT evtenabled::text FROM pg_event_trigger WHERE evtname = $2)`,
			extensions, DDLGuardEventTrigger).Scan(&missing, &trigger)
		if err != nil {
			return fmt.Errorf("query database: %w", err)
		}
		if versionsCompared.CompareAndSwap(false, true) {
			warnOutdatedExtensions(ctx, pool, extensions)
		}
		if len(missing) > 0 {
			return fmt.Errorf("missing PostgreSQL extensions: %s", strings.Join(missing, ", "))
		}
		switch {
		case trigger == nil:
			return fmt.Errorf("event trigger %s is missing; apply the migrations", DDLGuardEventTrigger)
		// O: enabled, A: always; D (disabled) and R (replica only) do not
		// fire in the sessions of the application.
		case *trigger != "O" && *trigger != "A":
			return fmt.Errorf("event trigger %s is not enabled (evtenabled %s); "+
				"run scripts/ops/timescaledb-update.sh or, as the bootstrap superuser, ALTER EVENT TRIGGER %s ENABLE",
				DDLGuardEventTrigger, *trigger, DDLGuardEventTrigger)
		}
		return nil
	}}
}

// warnOutdatedExtensions logs a warning for every extension of extensions
// whose installed version differs from the default version of the library
// the server ships.
func warnOutdatedExtensions(ctx context.Context, pool *pgxpool.Pool, extensions []string) {
	rows, err := pool.Query(ctx, `
		SELECT e.extname::text, e.extversion, a.default_version
		  FROM pg_extension e JOIN pg_available_extensions a ON a.name = e.extname
		 WHERE e.extname = ANY($1::text[]) AND e.extversion IS DISTINCT FROM a.default_version
		 ORDER BY e.extname`, extensions)
	if err != nil {
		slog.Warn("could not compare the PostgreSQL extension versions", "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name, version, defaultVersion string
		if err := rows.Scan(&name, &version, &defaultVersion); err != nil {
			slog.Warn("could not compare the PostgreSQL extension versions", "error", err)
			return
		}
		slog.Warn("PostgreSQL extension is not at the version of its installed library; "+
			"update it with scripts/ops/timescaledb-update.sh (TimescaleDB) or ALTER EXTENSION <name> UPDATE",
			"extension", name, "version", version, "default_version", defaultVersion)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("could not compare the PostgreSQL extension versions", "error", err)
	}
}

// TCPReadiness checks that a TCP connection to address (host:port) can be
// opened. It serves dependencies without a cheap protocol-level probe here
// (object storage endpoint, NATS).
func TCPReadiness(name, address string) ReadinessCheck {
	return ReadinessCheck{Name: name, Address: address, Check: func(ctx context.Context) error {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", address)
		if err != nil {
			return err
		}
		return conn.Close()
	}}
}

// ObjectStorageReadiness checks the S3 endpoint (RETICORA_S3_ENDPOINT) at
// the host:port its client connects to (ObjectStorageAddress).
func ObjectStorageReadiness(endpoint string, useSSL bool) (ReadinessCheck, error) {
	address, err := ObjectStorageAddress(endpoint, useSSL)
	if err != nil {
		return ReadinessCheck{}, err
	}
	return TCPReadiness("object_storage", address), nil
}

// ObjectStorageAddress returns the host:port blob.NewS3Store connects to for
// endpoint (F-127): the scheme of a URL (http://, https://) is removed and
// decides TLS, otherwise useSSL (RETICORA_S3_USE_SSL) does; without a port
// the default port of the scheme applies, 443 or 80. User information of a
// URL is dropped, and errors do not repeat the endpoint, so neither carries
// credentials.
func ObjectStorageAddress(endpoint string, useSSL bool) (string, error) {
	if strings.TrimSpace(endpoint) == "" {
		return "", errors.New("object storage endpoint is empty")
	}
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return "", fmt.Errorf("object storage endpoint is no valid URL: %w", urlCause(err))
		}
		if u.Host == "" {
			return "", errors.New("object storage endpoint has no host")
		}
		endpoint = u.Host
		switch u.Scheme {
		case "https":
			useSSL = true
		case "http":
			useSSL = false
		}
	}
	scheme, port := "http", "80"
	if useSSL {
		scheme, port = "https", "443"
	}
	// The S3 client builds its request URLs from scheme and endpoint; the
	// HTTP transport dials their host with the default port of the scheme.
	u, err := url.Parse(scheme + "://" + strings.TrimRight(endpoint, "/"))
	if err != nil {
		return "", fmt.Errorf("object storage endpoint is no valid host[:port]: %w", urlCause(err))
	}
	if u.Hostname() == "" {
		return "", errors.New("object storage endpoint has no host")
	}
	if u.Port() != "" {
		port = u.Port()
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

// urlCause strips the URL that url.Parse repeats in its error.
func urlCause(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// smtpReadinessReuse is how long the result of an SMTP probe answers further
// probes. /readyz is unauthenticated and not rate-limited: without it every
// request would open a connection to the relay, and a relay that throttles
// or blocks a client for a burst of connections would take every instance
// out of the load balancer. The orchestrators probe every 5 s or less often,
// so they always get a fresh result.
const smtpReadinessReuse = time.Second

// SMTPReadiness checks the central SMTP server (RETICORA_SMTP_*, TEC-13)
// within the readiness timeout: connection, 220 greeting and EHLO in the
// configured TLS mode — implicit TLS for tls, an offered and completed
// STARTTLS for starttls, as the sender requires them. It sends no mail and
// does not authenticate, so it needs no credentials. One probe runs at a
// time, and its result answers the probes of the next smtpReadinessReuse.
func SMTPReadiness(host string, port int, tlsMode string) ReadinessCheck {
	return smtpReadiness(host, port, tlsMode, nil)
}

// smtpReadiness is SMTPReadiness; roots replaces the system roots for the
// certificate of the server (tests).
func smtpReadiness(host string, port int, tlsMode string, roots *x509.CertPool) ReadinessCheck {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: roots}
	var (
		mu      sync.Mutex
		probed  time.Time
		lastErr error
	)
	return ReadinessCheck{Name: "smtp", Address: address, Check: func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if !probed.IsZero() && time.Since(probed) < smtpReadinessReuse {
			return lastErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		err := probeSMTP(ctx, address, host, tlsMode, tlsConfig)
		// A probe whose caller went away says nothing about the server.
		if !errors.Is(ctx.Err(), context.Canceled) {
			probed, lastErr = time.Now(), err
		}
		return err
	}}
}

// probeSMTP opens one SMTP session to address in tlsMode and ends it after
// EHLO (and STARTTLS).
func probeSMTP(ctx context.Context, address, host, tlsMode string, tlsConfig *tls.Config) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if tlsMode == notify.SMTPTLS {
		tlsConn := tls.Client(conn, tlsConfig)
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp TLS: %w", err)
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer client.Close()
	if err = client.Hello("localhost"); err != nil {
		return fmt.Errorf("smtp EHLO: %w", err)
	}
	if tlsMode == notify.SMTPStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("smtp server offers no STARTTLS")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("smtp STARTTLS: %w", err)
		}
	}
	// The server was usable; a failed goodbye does not change that.
	_ = client.Quit()
	return nil
}
