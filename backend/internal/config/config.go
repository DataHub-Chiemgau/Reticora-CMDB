// Package config provides configuration loading for Reticora services.
// All configuration is read exclusively from environment variables with
// the RETICORA_ prefix.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Environments accepted in RETICORA_ENVIRONMENT. Only development keeps the
// built-in defaults for backing services; staging and production require them
// to be configured explicitly (fail-closed start, OPS-01).
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Config holds the application configuration.
type Config struct {
	// Server
	Port        string
	Environment string
	LogLevel    slog.Level

	// Database
	DatabaseURL string

	// Messaging
	NATSUrl string
	// IngestLanes is the number of parallel ingest lanes per organization
	// (RETICORA_INGEST_LANES, default 4, 1-MaxIngestLanes; NFR-03, WP-334,
	// E-56). 1 processes the ingest of an organization serially as before.
	// Every instance must use the same value.
	IngestLanes int

	// Cache / Rate-Limiting / Idempotency
	RedisURL string

	// Object Storage (S3-compatible / MinIO)
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3UseSSL    bool
	// BlobDir backs export storage in --no-db development mode.
	BlobDir string

	// Auth / OIDC
	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	// OIDCCACertFile optionally points at a PEM bundle that is trusted in
	// addition to the system roots when the server talks to the OIDC issuer
	// (discovery, JWKS and token exchange). Deployments that terminate TLS
	// with a private or not-yet-issued certificate would otherwise fail the
	// token exchange with an x509 verification error.
	OIDCCACertFile string
	SessionKeyPath string // path to RS256 private key PEM for session JWTs
	// SessionPreviousKeyPaths lists PEM files (comma separated) of previous
	// session signing keys whose tokens stay valid during a key rotation
	// (SEC-06); new tokens are signed with SessionKeyPath only.
	SessionPreviousKeyPaths string

	// Operator path /admin (SEC-07): RETICORA_OPERATOR_TOKEN is the
	// break-glass token (empty disables it, at least 32 characters
	// otherwise); operators are members of OperatorGroup who logged in with
	// MFA (amr, or one of OperatorMFAACR as acr).
	OperatorToken        string
	OperatorGroup        string
	OperatorMFAACR       string
	AllowInsecureDevAuth bool // opt-in: accept session tokens without signature verification
	// Alertmanager bridge of the operator path (OPS-05, E-75):
	// RETICORA_OPS_ALERT_TOKEN is the bearer token Alertmanager sends (at
	// least 32 characters, not the break-glass token; surrounding white
	// space is dropped as Alertmanager drops it from its credentials file);
	// RETICORA_OPS_ALERT_ORG_ID is the operator organization whose
	// notification channels deliver the alerts. Without the organization the
	// bridge answers 404 and Alertmanager delivers by e-mail only.
	OpsAlertToken string
	OpsAlertOrgID string

	// Entitlements
	DefaultPlan string // plan provisioned for the --no-db demo organization
	// DefaultProvisionRole names the standard role assigned to a user on first
	// OIDC login; empty assigns no role (admins assign explicitly).
	DefaultProvisionRole   string
	EntitlementEnforcement bool // when false, feature gating is reported but not enforced

	// Encryption
	MasterKey string // 32-byte base64-encoded master key for envelope encryption
	// MasterKeyPrevious lists the master keys being rotated out
	// (comma-separated): their data keys stay readable until the rewrap job
	// has moved them to MasterKey (SEC-06).
	MasterKeyPrevious string

	// Collector NATS endpoint (COL-08, WP-146): the edge NATS server that
	// collectors reach on 4222 with mutual TLS. PKIDir receives its CA
	// bundle, server certificate (for the host names in NATSEdgeHosts) and
	// generated configuration; the authorization callout connects to
	// NATSEdgeURL. Its keys are derived from the derivation root of the
	// installation (crypto.LoadDerivationRoot), not from the master key.
	PKIDir        string
	NATSEdgeHosts string
	NATSEdgeURL   string

	// Observability
	OTelEndpoint string
	// MetricsTenantLabel enables the organization_id label on HTTP request
	// metrics. It multiplies the series count by the number of tenants, so it
	// is opt-in for bounded-tenant deployments.
	MetricsTenantLabel bool
	// PprofAddr is the loopback address of the profiling listener
	// (RETICORA_PPROF_ADDR, e.g. 127.0.0.1:6060; NFR-02, WP-438): the CPU
	// profile of a load run. Empty (the default) starts no listener; the
	// start check allows it only in development, on a loopback IP and
	// without RETICORA_EGRESS_ALLOW_PRIVATE.
	PprofAddr string

	// EgressAllowPrivate permits webhook and connector destinations and own
	// SMTP servers in private networks and on other SMTP ports (dedicated,
	// on-premises and air-gapped installations); cloud metadata endpoints
	// stay blocked (SEC-08). Default off.
	EgressAllowPrivate bool

	// Self-signup (TLC-01, E-33). SignupVerifyURL is the page the
	// verification link opens; the Keycloak admin client creates the first
	// org_admin's account. Without them, or without an e-mail channel
	// (WP-137), signup answers 503.
	SignupVerifyURL           string
	KeycloakAdminClientID     string
	KeycloakAdminClientSecret string
	// SignupCaptchaKey signs the ALTCHA challenges (at least 32 bytes, the
	// same on every instance); SignupBlockedDomains/SignupAllowedDomains
	// extend the disposable-domain lists (E-33).
	SignupCaptchaKey     string
	SignupBlockedDomains string
	SignupAllowedDomains string
	TrialPlan            string // ENT-07: standard
	TrialDays            int    // ENT-07: 30
	TrialMaxCIs          int    // ENT-07: 100

	// InvitationAcceptURL is the page the link of an invitation mail opens
	// (TLC-03, NTF-05); the link adds ?token=<token>. Without it, issuing and
	// renewing invitations answers 503. Development defaults to
	// DefaultInvitationAcceptURL.
	InvitationAcceptURL string

	// PreAuthRateLimitRPM is the budget of authentication attempts per
	// client IP and minute (AUT-10, default 20).
	PreAuthRateLimitRPM int
	// TrustedProxies lists the reverse proxies (CIDRs) whose X-Real-IP
	// header names the client for IP-based limits.
	TrustedProxies string

	// Rate Limiting
	RateLimitRPM int // requests per minute per API key or session (default 600, API-04)
	// RateLimitOrgRPM is the budget of all requests of an organization per
	// minute (RETICORA_RATE_LIMIT_ORG_RPM, default 6000, API-04).
	RateLimitOrgRPM int

	// Central SMTP server of the installation (TEC-13): e-mail of the
	// central_smtp channels and of the self-signup. Empty host: none.
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPTLS      string // starttls (default), tls or none

	// Retention (NFR-06, AUD-09): how long the retention runs keep each
	// kind of data. The defaults are the minimums; a shorter value is a
	// start error.
	RetentionAuditYears     int // audit_log, 10 years
	RetentionHistoryDays    int // ci_change, entity_change, location_change, 3 years
	RetentionSoftDeleteDays int // soft-deleted CIs, 90 days
	RetentionReviewItemDays int // resolved review items, 1 year

	// Search
	SearchBackend      string
	OpenSearchURL      string
	OpenSearchUsername string
	OpenSearchPassword string
	OpenSearchIndex    string

	// AI / LLM
	LLMBaseURL        string
	LLMAPIKey         string
	LLMChatModel      string
	LLMEmbeddingModel string
	// AirGapped disables every external AI call (AI-02, air-gapped profile).
	AirGapped bool
	// Modules lists the enabled late modules (RETICORA_MODULES, WP-214):
	// empty switches every module with a phase tag from P2 or [A] off (the
	// G1 default after E-03), "all" switches them all on.
	Modules string

	// workers holds the operator switch of every registered background
	// worker (OPS-02, see WorkerNames).
	workers map[string]bool

	// explicit records the RETICORA_* variables that were set; invalid
	// lists variables whose value could not be parsed; warnings are the
	// settings the server accepts but reports at start (Warnings).
	explicit map[string]bool
	invalid  []string
	warnings []string
}

// Load reads configuration from environment variables with RETICORA_ prefix
// and sensible defaults.
func Load() *Config {
	l := &loader{set: map[string]bool{}}
	cfg := &Config{
		Port:        l.str("RETICORA_PORT", "8080"),
		Environment: l.str("RETICORA_ENVIRONMENT", "development"),

		DatabaseURL: l.str("RETICORA_DATABASE_URL", "postgres://localhost:5432/reticora?sslmode=disable"),
		NATSUrl:     l.str("RETICORA_NATS_URL", "nats://localhost:4222"),
		IngestLanes: l.integer("RETICORA_INGEST_LANES", DefaultIngestLanes),
		RedisURL:    l.str("RETICORA_REDIS_URL", "redis://localhost:6379"),

		S3Endpoint:  l.str("RETICORA_S3_ENDPOINT", "localhost:9000"),
		S3Bucket:    l.str("RETICORA_S3_BUCKET", "reticora"),
		S3AccessKey: l.str("RETICORA_S3_ACCESS_KEY", "reticora"),
		S3SecretKey: l.str("RETICORA_S3_SECRET_KEY", "reticora_dev"),
		S3UseSSL:    l.str("RETICORA_S3_USE_SSL", "false") == "true",
		// BlobDir backs export storage in --no-db development mode; in
		// database mode the S3 settings above are used instead.
		BlobDir: l.str("RETICORA_BLOB_DIR", ""),

		OIDCIssuerURL:           l.str("RETICORA_OIDC_ISSUER_URL", "http://localhost:8180/realms/reticora"),
		OIDCClientID:            l.str("RETICORA_OIDC_CLIENT_ID", "reticora-app"),
		OIDCClientSecret:        l.str("RETICORA_OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:         l.str("RETICORA_OIDC_REDIRECT_URL", ""),
		OIDCCACertFile:          l.str("RETICORA_OIDC_CA_CERT_FILE", ""),
		SessionKeyPath:          l.str("RETICORA_SESSION_KEY_PATH", ""),
		SessionPreviousKeyPaths: l.str("RETICORA_SESSION_PREVIOUS_KEY_PATHS", ""),
		OperatorToken:           l.str("RETICORA_OPERATOR_TOKEN", ""),
		OperatorGroup:           l.str("RETICORA_OPERATOR_GROUP", "operators"),
		OperatorMFAACR:          l.str("RETICORA_OPERATOR_MFA_ACR", "2"),
		AllowInsecureDevAuth:    l.str("RETICORA_ALLOW_INSECURE_DEV_AUTH", "false") == "true",
		OpsAlertToken:           strings.TrimSpace(l.str("RETICORA_OPS_ALERT_TOKEN", "")),
		OpsAlertOrgID:           strings.TrimSpace(l.str("RETICORA_OPS_ALERT_ORG_ID", "")),

		DefaultPlan: l.str("RETICORA_DEFAULT_PLAN", "essential"),

		SignupVerifyURL:           l.str("RETICORA_SIGNUP_VERIFY_URL", ""),
		KeycloakAdminClientID:     l.str("RETICORA_KEYCLOAK_ADMIN_CLIENT_ID", ""),
		KeycloakAdminClientSecret: l.str("RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET", ""),
		SignupCaptchaKey:          l.str("RETICORA_SIGNUP_CAPTCHA_KEY", ""),
		SignupBlockedDomains:      l.str("RETICORA_SIGNUP_BLOCKED_DOMAINS", ""),
		SignupAllowedDomains:      l.str("RETICORA_SIGNUP_ALLOWED_DOMAINS", ""),
		TrialPlan:                 l.str("RETICORA_TRIAL_PLAN", "standard"),
		TrialDays:                 l.integer("RETICORA_TRIAL_DAYS", 30),
		TrialMaxCIs:               l.integer("RETICORA_TRIAL_MAX_CIS", 100),
		InvitationAcceptURL:       l.str("RETICORA_INVITATION_ACCEPT_URL", ""),
		DefaultProvisionRole:      l.str("RETICORA_DEFAULT_PROVISION_ROLE", "viewer"),
		EntitlementEnforcement:    l.str("RETICORA_ENTITLEMENT_ENFORCEMENT", "true") != "false",

		MasterKey:         l.str("RETICORA_MASTER_KEY", ""),
		MasterKeyPrevious: l.str("RETICORA_MASTER_KEY_PREVIOUS", ""),

		PKIDir:        l.str("RETICORA_PKI_DIR", ""),
		NATSEdgeHosts: l.str("RETICORA_NATS_EDGE_HOSTS", ""),
		NATSEdgeURL:   l.str("RETICORA_NATS_EDGE_URL", ""),

		OTelEndpoint: l.str("RETICORA_OTEL_ENDPOINT", ""),
		// Opt-in: the organization_id label multiplies the HTTP metric series
		// by the tenant count. Default off; enable for bounded-tenant setups.
		MetricsTenantLabel: l.boolean("RETICORA_METRICS_TENANT_LABEL", false),
		PprofAddr:          strings.TrimSpace(l.str("RETICORA_PPROF_ADDR", "")),
		RateLimitRPM:       l.integer("RETICORA_RATE_LIMIT_RPM", 600),
		RateLimitOrgRPM:    l.integer("RETICORA_RATE_LIMIT_ORG_RPM", 6000),

		SMTPHost:     l.str("RETICORA_SMTP_HOST", ""),
		SMTPPort:     l.integer("RETICORA_SMTP_PORT", 587),
		SMTPUsername: l.str("RETICORA_SMTP_USERNAME", ""),
		SMTPPassword: l.str("RETICORA_SMTP_PASSWORD", ""),
		SMTPFrom:     l.str("RETICORA_SMTP_FROM", ""),
		SMTPTLS:      l.str("RETICORA_SMTP_TLS", "starttls"),

		RetentionAuditYears:     l.integer("RETICORA_RETENTION_AUDIT_YEARS", MinRetentionAuditYears),
		RetentionHistoryDays:    l.integer("RETICORA_RETENTION_HISTORY_DAYS", MinRetentionHistoryDays),
		RetentionSoftDeleteDays: l.integer("RETICORA_RETENTION_SOFT_DELETE_DAYS", MinRetentionSoftDeleteDays),
		RetentionReviewItemDays: l.integer("RETICORA_RETENTION_REVIEW_ITEM_DAYS", MinRetentionReviewItemDays),
		EgressAllowPrivate:      l.boolean("RETICORA_EGRESS_ALLOW_PRIVATE", false),
		PreAuthRateLimitRPM:     l.integer("RETICORA_PREAUTH_RATE_LIMIT_RPM", 20),
		TrustedProxies:          l.str("RETICORA_TRUSTED_PROXIES", "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"),

		SearchBackend:      l.str("RETICORA_SEARCH_BACKEND", "postgres"),
		OpenSearchURL:      l.str("RETICORA_OPENSEARCH_URL", ""),
		OpenSearchUsername: l.str("RETICORA_OPENSEARCH_USERNAME", ""),
		OpenSearchPassword: l.str("RETICORA_OPENSEARCH_PASSWORD", ""),
		OpenSearchIndex:    l.str("RETICORA_OPENSEARCH_INDEX", "reticora-search"),

		LLMBaseURL:        l.str("RETICORA_LLM_BASE_URL", ""),
		LLMAPIKey:         l.str("RETICORA_LLM_API_KEY", ""),
		LLMChatModel:      l.str("RETICORA_LLM_CHAT_MODEL", ""),
		LLMEmbeddingModel: l.str("RETICORA_LLM_EMBEDDING_MODEL", ""),
		AirGapped:         l.boolean("RETICORA_AIR_GAPPED", false),
		Modules:           l.str("RETICORA_MODULES", ""),

		LogLevel: slog.LevelInfo,
	}

	cfg.workers = make(map[string]bool, len(WorkerNames))
	for _, name := range WorkerNames {
		cfg.workers[name] = l.boolean(WorkerEnvKey(name), true)
	}
	cfg.warnings = l.checkWorkerSwitches(cfg.workers)
	if cfg.OpsAlertToken != "" && cfg.OpsAlertOrgID == "" {
		cfg.warnings = append(cfg.warnings, "RETICORA_OPS_ALERT_TOKEN is set without RETICORA_OPS_ALERT_ORG_ID: "+
			"the Alertmanager bridge answers 404 (E-75)")
	}

	cfg.explicit = l.set
	cfg.invalid = l.invalid

	// Compose derives the accept URL from RETICORA_PUBLIC_BASE_URL, which may
	// end with a slash: https://host//invitations/accept becomes
	// https://host/invitations/accept.
	cfg.InvitationAcceptURL = collapseLeadingSlashes(cfg.InvitationAcceptURL)

	if cfg.Environment == EnvDevelopment {
		cfg.LogLevel = slog.LevelDebug
		if cfg.InvitationAcceptURL == "" {
			cfg.InvitationAcceptURL = DefaultInvitationAcceptURL
		}
	} else if !cfg.anySet(s3Keys...) {
		// Outside development the local MinIO defaults are never used
		// silently: without explicit settings object storage is disabled.
		cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey = "", "", ""
	}

	return cfg
}

// DefaultInvitationAcceptURL is the accept page of the development frontend
// (Vite on port 5173).
const DefaultInvitationAcceptURL = "http://localhost:5173/invitations/accept"

// collapseLeadingSlashes replaces a run of slashes at the start of the path
// of raw with one; any other value stays as it is.
func collapseLeadingSlashes(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.HasPrefix(u.Path, "//") {
		return raw
	}
	u.Path, u.RawPath = "/"+strings.TrimLeft(u.Path, "/"), ""
	return u.String()
}

// absoluteHTTPURL reports whether raw is an absolute http or https URL with
// a host and without user info.
func absoluteHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "http" || scheme == "https"
}

// Minimum retention periods of NFR-06 and AUD-09.
const (
	MinRetentionAuditYears     = 10
	MinRetentionHistoryDays    = 3 * 365
	MinRetentionSoftDeleteDays = 90
	MinRetentionReviewItemDays = 365
)

// Ingest lanes per organization (RETICORA_INGEST_LANES, WP-334): the
// default and the largest value (discovery.MaxIngestLanes). Every lane may
// hold a database connection; the server bounds the batches in work to a
// quarter of its pool whatever the value.
const (
	DefaultIngestLanes = 4
	MaxIngestLanes     = 16
)

// Background workers with an independent operator switch (OPS-02). The
// convention: worker <name> is switched by RETICORA_WORKER_<NAME>_ENABLED
// (true/false, default true); a disabled worker is not started in this
// process, so each worker can run on dedicated instances. A new worker adds
// its name here and registers under it in the server's worker set.
const (
	WorkerWebhookDispatcher = "webhook_dispatcher"
	WorkerExport            = "export"
	WorkerIngest            = "ingest"
	WorkerMonitoring        = "monitoring"
	WorkerScheduler         = "scheduler"
	WorkerJobs              = "jobs"
	WorkerEvents            = "events"
	WorkerNotifier          = "notifier"
	WorkerNATSEdge          = "nats_edge"
	WorkerCredentialBundles = "credential_bundles"
	WorkerDiscoveryScopes   = "discovery_scopes"
	// WorkerOfflineDetector runs as the system job ci.offline_detector of
	// the scheduler (REC-09, LCY-05, F-289).
	WorkerOfflineDetector = "offline_detector"
)

// WorkerNames lists every registered worker.
var WorkerNames = []string{
	WorkerWebhookDispatcher,
	WorkerExport,
	WorkerIngest,
	WorkerMonitoring,
	WorkerScheduler,
	WorkerJobs,
	WorkerEvents,
	WorkerNotifier,
	WorkerNATSEdge,
	WorkerCredentialBundles,
	WorkerDiscoveryScopes,
	WorkerOfflineDetector,
}

// OfflineDetectorAlias is the former switch of the offline detector (WP-157).
// It still applies while RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED is not set
// and is reported as deprecated (F-289).
const OfflineDetectorAlias = "RETICORA_OFFLINE_DETECTOR_ENABLED"

// WorkerEnvKey returns the switch variable of a worker.
func WorkerEnvKey(name string) string {
	return "RETICORA_WORKER_" + strings.ToUpper(name) + "_ENABLED"
}

// Warnings lists the settings the server accepts but reports at start: a
// deprecated switch, a worker switch without a registered worker (OPS-02).
func (c *Config) Warnings() []string { return append([]string(nil), c.warnings...) }

// IsWorker reports whether name is a registered worker.
func IsWorker(name string) bool {
	for _, n := range WorkerNames {
		if n == name {
			return true
		}
	}
	return false
}

// WorkerEnabled reports the switch of a registered worker; unknown names
// are never enabled.
func (c *Config) WorkerEnabled(name string) bool {
	return c.workers[name]
}

// Variables that staging and production must set explicitly instead of
// falling back to the localhost defaults.
var requiredOutsideDevelopment = []string{
	"RETICORA_DATABASE_URL",
	"RETICORA_REDIS_URL",
	"RETICORA_MASTER_KEY",
	"RETICORA_OIDC_ISSUER_URL",
}

var s3Keys = []string{"RETICORA_S3_ENDPOINT", "RETICORA_S3_ACCESS_KEY", "RETICORA_S3_SECRET_KEY"}

// IsDevelopment reports whether the local development defaults apply.
func (c *Config) IsDevelopment() bool { return c.Environment == EnvDevelopment }

// IsSet reports whether the RETICORA_* variable was set explicitly.
func (c *Config) IsSet(key string) bool { return c.explicit[key] }

func (c *Config) anySet(keys ...string) bool {
	for _, k := range keys {
		if c.explicit[k] {
			return true
		}
	}
	return false
}

// S3Configured reports whether object storage is configured.
func (c *Config) S3Configured() bool { return c.S3Endpoint != "" }

// Validate is the fail-closed start check (OPS-01): it reports every missing
// or invalid setting at once. noDB is the --no-db flag, which is only allowed
// in development.
func (c *Config) Validate(noDB bool) error {
	var errs []error
	switch c.Environment {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("RETICORA_ENVIRONMENT=%q is not one of %s, %s, %s",
			c.Environment, EnvDevelopment, EnvStaging, EnvProduction))
	}
	for _, key := range c.invalid {
		errs = append(errs, fmt.Errorf("%s has an invalid value", key))
	}
	if c.OperatorToken != "" && len(c.OperatorToken) < 32 {
		errs = append(errs, fmt.Errorf("RETICORA_OPERATOR_TOKEN must have at least 32 characters"))
	}
	errs = append(errs, c.opsAlertErrors()...)
	errs = append(errs, c.pprofErrors()...)
	switch c.TrialPlan {
	case "essential", "standard", "pro", "enterprise":
	default:
		errs = append(errs, fmt.Errorf("RETICORA_TRIAL_PLAN=%q is not one of essential, standard, pro, enterprise", c.TrialPlan))
	}
	if c.SignupCaptchaKey != "" && len(c.SignupCaptchaKey) < 32 {
		errs = append(errs, fmt.Errorf("RETICORA_SIGNUP_CAPTCHA_KEY must have at least 32 characters"))
	}
	// The invitation mail carries this link. http is allowed in every
	// environment: the HTTP default path of the installer runs production
	// on http://<host>.
	if c.InvitationAcceptURL != "" && !absoluteHTTPURL(c.InvitationAcceptURL) {
		errs = append(errs, fmt.Errorf("RETICORA_INVITATION_ACCEPT_URL must be an absolute http or https URL with a host and without user info"))
	}
	if c.TrialDays <= 0 {
		errs = append(errs, fmt.Errorf("RETICORA_TRIAL_DAYS must be positive, got %d", c.TrialDays))
	}
	if c.TrialMaxCIs < 0 {
		errs = append(errs, fmt.Errorf("RETICORA_TRIAL_MAX_CIS must not be negative, got %d", c.TrialMaxCIs))
	}
	if c.SMTPHost != "" {
		switch {
		case c.SMTPFrom == "":
			errs = append(errs, fmt.Errorf("RETICORA_SMTP_FROM is required with RETICORA_SMTP_HOST"))
		case c.SMTPTLS != "starttls" && c.SMTPTLS != "tls" && c.SMTPTLS != "none":
			errs = append(errs, fmt.Errorf("RETICORA_SMTP_TLS=%q is not one of starttls, tls, none", c.SMTPTLS))
		case c.SMTPPort < 1 || c.SMTPPort > 65535:
			errs = append(errs, fmt.Errorf("RETICORA_SMTP_PORT=%d is out of range", c.SMTPPort))
		}
	}
	for _, r := range []struct {
		key        string
		value, min int
	}{
		{"RETICORA_RETENTION_AUDIT_YEARS", c.RetentionAuditYears, MinRetentionAuditYears},
		{"RETICORA_RETENTION_HISTORY_DAYS", c.RetentionHistoryDays, MinRetentionHistoryDays},
		{"RETICORA_RETENTION_SOFT_DELETE_DAYS", c.RetentionSoftDeleteDays, MinRetentionSoftDeleteDays},
		{"RETICORA_RETENTION_REVIEW_ITEM_DAYS", c.RetentionReviewItemDays, MinRetentionReviewItemDays},
	} {
		if r.value < r.min {
			errs = append(errs, fmt.Errorf("%s must be at least %d (NFR-06), got %d", r.key, r.min, r.value))
		}
	}
	if c.RateLimitOrgRPM < c.RateLimitRPM {
		errs = append(errs, fmt.Errorf("RETICORA_RATE_LIMIT_ORG_RPM (%d) must not be below RETICORA_RATE_LIMIT_RPM (%d)", c.RateLimitOrgRPM, c.RateLimitRPM))
	}
	if c.IngestLanes < 1 || c.IngestLanes > MaxIngestLanes {
		errs = append(errs, fmt.Errorf("RETICORA_INGEST_LANES must be between 1 and %d, got %d", MaxIngestLanes, c.IngestLanes))
	}
	if c.RateLimitRPM <= 0 {
		errs = append(errs, fmt.Errorf("RETICORA_RATE_LIMIT_RPM must be positive, got %d", c.RateLimitRPM))
	}
	if !c.IsDevelopment() {
		// The development login (unsigned session tokens) exists only in
		// development (AUT-03).
		if c.AllowInsecureDevAuth {
			errs = append(errs, fmt.Errorf("RETICORA_ALLOW_INSECURE_DEV_AUTH is only allowed with RETICORA_ENVIRONMENT=%s, not %s (AUT-03)",
				EnvDevelopment, c.Environment))
		}
		if noDB {
			errs = append(errs, fmt.Errorf("--no-db is only allowed with RETICORA_ENVIRONMENT=%s", EnvDevelopment))
		}
		// A default role org_admin makes first logins org_admins whose
		// account the server binds to MFA through the Keycloak admin client
		// (AUT-09, E-65 option 1, WP-367); without it each of these logins
		// would fail.
		if c.DefaultProvisionRole == "org_admin" && (c.KeycloakAdminClientID == "" || c.KeycloakAdminClientSecret == "") {
			errs = append(errs, fmt.Errorf("RETICORA_DEFAULT_PROVISION_ROLE=org_admin requires the Keycloak admin client "+
				"(RETICORA_KEYCLOAK_ADMIN_CLIENT_ID, RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET) outside %s: "+
				"the second factor of a new org_admin could not be enforced (AUT-09)", EnvDevelopment))
		}
		var missing []string
		for _, key := range requiredOutsideDevelopment {
			if !c.explicit[key] {
				missing = append(missing, key)
			}
		}
		if c.anySet(s3Keys...) {
			for _, key := range s3Keys {
				if !c.explicit[key] {
					missing = append(missing, key)
				}
			}
		}
		if len(missing) > 0 {
			errs = append(errs, fmt.Errorf("RETICORA_ENVIRONMENT=%s requires %s", c.Environment, strings.Join(missing, ", ")))
		}
	}
	return errors.Join(errs...)
}

// uuidValue matches a UUID in its canonical text form.
var uuidValue = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// opsAlertErrors checks the Alertmanager bridge (OPS-05, E-75): the
// operator organization is a UUID and needs the token; the token has at
// least 32 characters and is not the break-glass token, whose every use is
// an alarm and which must not sit in Alertmanager's secrets.
func (c *Config) opsAlertErrors() []error {
	var errs []error
	if c.OpsAlertToken != "" && len(c.OpsAlertToken) < 32 {
		errs = append(errs, fmt.Errorf("RETICORA_OPS_ALERT_TOKEN must have at least 32 characters"))
	}
	if c.OpsAlertToken != "" && c.OpsAlertToken == c.OperatorToken {
		errs = append(errs, fmt.Errorf("RETICORA_OPS_ALERT_TOKEN must differ from RETICORA_OPERATOR_TOKEN"))
	}
	if c.OpsAlertOrgID != "" {
		if !uuidValue.MatchString(c.OpsAlertOrgID) {
			errs = append(errs, fmt.Errorf("RETICORA_OPS_ALERT_ORG_ID must be the UUID of the operator organization"))
		}
		if c.OpsAlertToken == "" {
			errs = append(errs, fmt.Errorf("RETICORA_OPS_ALERT_ORG_ID requires RETICORA_OPS_ALERT_TOKEN"))
		}
	}
	return errs
}

// pprofErrors checks the profiling listener (RETICORA_PPROF_ADDR, WP-438).
// The profiles show the server's code paths and run CPU profiles on
// request, so nobody but the host may reach them: a loopback IP with a port,
// only in development (the load runs of load.yml), and not together with
// RETICORA_EGRESS_ALLOW_PRIVATE, which lets webhooks and connectors reach
// loopback addresses.
func (c *Config) pprofErrors() []error {
	if c.PprofAddr == "" {
		return nil
	}
	var errs []error
	host, port, err := net.SplitHostPort(c.PprofAddr)
	ip := net.ParseIP(host)
	n, perr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || perr != nil || n < 1 || n > 65535 {
		errs = append(errs, fmt.Errorf("RETICORA_PPROF_ADDR=%q must be a loopback IP with a port, e.g. 127.0.0.1:6060", c.PprofAddr))
	}
	if !c.IsDevelopment() {
		errs = append(errs, fmt.Errorf("RETICORA_PPROF_ADDR is only allowed with RETICORA_ENVIRONMENT=%s, not %s", EnvDevelopment, c.Environment))
	}
	if c.EgressAllowPrivate {
		errs = append(errs, fmt.Errorf("RETICORA_PPROF_ADDR must not be combined with RETICORA_EGRESS_ALLOW_PRIVATE=true: "+
			"webhooks and connectors could reach the profiles on the loopback address"))
	}
	return errs
}

// loader reads RETICORA_* variables and records which were set and which
// could not be parsed.
type loader struct {
	set     map[string]bool
	invalid []string
}

func (l *loader) str(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		l.set[key] = true
		return v
	}
	return defaultValue
}

func (l *loader) integer(key string, defaultValue int) int {
	v := l.str(key, "")
	if v == "" {
		return defaultValue
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		l.invalid = append(l.invalid, key)
		return defaultValue
	}
	return i
}

func (l *loader) boolean(key string, defaultValue bool) bool {
	v := l.str(key, "")
	if v == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.invalid = append(l.invalid, key)
		return defaultValue
	}
	return b
}

// checkWorkerSwitches applies the alias of the offline detector to workers
// and reports it, and reports every set RETICORA_WORKER_*_ENABLED that
// switches no registered worker: it has no effect (F-289).
func (l *loader) checkWorkerSwitches(workers map[string]bool) []string {
	var warnings []string
	if alias := l.boolean(OfflineDetectorAlias, true); l.set[OfflineDetectorAlias] {
		key := WorkerEnvKey(WorkerOfflineDetector)
		if l.set[key] {
			warnings = append(warnings, fmt.Sprintf("%s is deprecated and ignored: %s is set", OfflineDetectorAlias, key))
		} else {
			workers[WorkerOfflineDetector] = alias
			warnings = append(warnings, fmt.Sprintf("%s is deprecated: use %s", OfflineDetectorAlias, key))
		}
	}
	var unknown []string
	for _, kv := range os.Environ() {
		key, value, _ := strings.Cut(kv, "=")
		if value == "" || !strings.HasPrefix(key, "RETICORA_WORKER_") || !strings.HasSuffix(key, "_ENABLED") {
			continue
		}
		if name := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(key, "RETICORA_WORKER_"), "_ENABLED")); !IsWorker(name) ||
			WorkerEnvKey(name) != key {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		warnings = append(warnings, fmt.Sprintf("%s switches no registered worker and is ignored (workers: %s)",
			key, strings.Join(WorkerNames, ", ")))
	}
	return warnings
}

// dsnKeyValueSecret matches the password of a key=value connection string
// (libpq: password=secret, password='with spaces'), case-insensitively.
var dsnKeyValueSecret = regexp.MustCompile(`(?i)\b(password|passwd|pwd|sslpassword)\s*=\s*('(?:[^'\\]|\\.)*'|[^\s;&]*)`)

// MaskDSN hides the password of a connection string for logs (SEC-01). It
// covers the URL form (scheme://user:password@host/db, including a
// password=… query parameter) and the key=value form (host=… password=…),
// and never returns the input unchanged when it carries a password.
func MaskDSN(dsn string) string {
	masked := dsnKeyValueSecret.ReplaceAllString(dsn, "${1}=***")
	if u, err := url.Parse(masked); err == nil && u.Scheme != "" && u.Host != "" {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "***")
		}
		return strings.ReplaceAll(u.String(), "%2A%2A%2A", "***")
	}
	// Not a parseable URL: cut any userinfo password before the last "@".
	if scheme := strings.Index(masked, "://"); scheme >= 0 {
		rest := masked[scheme+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			if colon := strings.Index(rest[:at], ":"); colon >= 0 {
				return masked[:scheme+3] + rest[:colon+1] + "***" + rest[at:]
			}
		}
	}
	return masked
}
