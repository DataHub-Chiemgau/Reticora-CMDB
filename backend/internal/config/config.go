// Package config provides configuration loading for Reticora services.
// All configuration is read exclusively from environment variables with
// the RETICORA_ prefix.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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
	// ProbeStreamMaxBytes bounds the JetStream stream RETICORA_PROBE that
	// takes the collectors' probe state changes and results
	// (RETICORA_PROBE_STREAM_MAX_BYTES, default 5 GiB; E-173, WP-959).
	ProbeStreamMaxBytes int64
	// ProbeEdgeWorkers is the number of handlers per instance that answer
	// the probe edge subjects in parallel (RETICORA_PROBE_EDGE_WORKERS,
	// default and minimum 8; NFR-11, WP-959).
	ProbeEdgeWorkers int

	// Cache / Rate-Limiting / Idempotency
	RedisURL string

	// Object Storage (S3-compatible / MinIO)
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3UseSSL    bool
	// S3PublicURL is the base URL under which browsers reach the object
	// store for signed downloads (exports, documents), e.g.
	// https://cmdb.example.com/objects behind the frontend nginx. Empty
	// keeps the internal endpoint in the URLs (F-376).
	S3PublicURL string
	// BlobDir backs export storage in --no-db development mode.
	BlobDir string

	// Auth / OIDC
	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	// OIDCCACertFile optionally points at a PEM bundle that is trusted in
	// addition to the system roots when the server talks to the OIDC issuer
	// (discovery, JWKS and token exchange, and the Keycloak admin client of
	// signup, invitations and MFA binding). Deployments that terminate TLS
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
	// verification link opens (the SPA's /onboarding; the link adds
	// ?token=<token>); the Keycloak admin client creates the first
	// org_admin's account. Without them, or without the central SMTP server
	// (WP-137), signup answers 503. Development defaults to
	// DefaultSignupVerifyURL.
	SignupVerifyURL           string
	KeycloakAdminClientID     string
	KeycloakAdminClientSecret string
	// KeycloakInternalURL optionally is the base URL (http(s)://host[:port])
	// under which the server reaches Keycloak directly, e.g.
	// http://keycloak:8080 in the compose network. The Keycloak admin client
	// then sends its requests there instead of to the public issuer host,
	// whose reverse proxy does not expose /admin/ (WP-646).
	KeycloakInternalURL string
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
	// RetentionWorkflowRunDays keeps ended workflow runs with their steps
	// (WFL-02, E-241: 400 days by default, at least 30; WP-1093).
	RetentionWorkflowRunDays int

	// Retention of the event stream RETICORA_EVENTS (EVT-01, NFR-06): the
	// age and size its messages are kept; published outbox rows keep their
	// data as long. An age below MinEventsMaxAge is a start error.
	EventsMaxAge   time.Duration
	EventsMaxBytes int64

	// ProbeAssignTick is the interval of the system job probe.assign
	// (RETICORA_PROBE_ASSIGN_TICK, default 5 s, 1–30 s; PRB-03, PRB-10,
	// WP-963): every tick sweeps the members without monitoring alive
	// message and assigns the collector groups.
	ProbeAssignTick time.Duration

	// ADMPCacheTTLDays is the TTL of a fetched ADMP entry (ADM-03, E-243,
	// WP-929): admp.expire removes it with its personal surrogates after
	// it (RETICORA_ADMP_CACHE_TTL_DAYS, 1 to 3650, default 30).
	ADMPCacheTTLDays int

	// Vulnerability feeds (SEC-02, WP-1118): the CSAF providers
	// (RETICORA_VULNFEED_CSAF_PROVIDERS, comma separated https URLs of
	// provider-metadata.json or aggregator.json; default the CSAF provider
	// of the BSI) and the interval of vulnfeed.csaf
	// (RETICORA_VULNFEED_CSAF_INTERVAL, default 6 h). In the air-gapped
	// profile the import runs only with an explicitly set list (E-235).
	VulnfeedCSAFProviders []string
	VulnfeedCSAFInterval  time.Duration
	// NVD 2.0 and CISA KEV (WP-1119): the API key of the installation
	// (RETICORA_VULNFEED_NVD_API_KEY, deployment secret, only the header
	// apiKey), the interval of vulnfeed.nvd (RETICORA_VULNFEED_NVD_INTERVAL,
	// default 2 h) and the KEV catalog (RETICORA_VULNFEED_KEV_URL, default
	// the CISA feed). Not in the air-gapped profile (VulnfeedNVDEnabled).
	VulnfeedNVDAPIKey   string
	VulnfeedNVDInterval time.Duration
	VulnfeedKEVURL      string
	// VulnfeedBundlePublicKey replaces the embedded public key of the
	// offline bundles (RETICORA_VULNFEED_BUNDLE_PUBLIC_KEY, base64 Ed25519,
	// WP-1120); empty keeps vulnfeed.DefaultBundlePublicKey.
	VulnfeedBundlePublicKey string

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
	// empty is the release default G2 (WP-830), every module with phase P2
	// on and the modules of P3, P4 and the add-ons [A] off (E-03), like the
	// phase value P2; "all" switches them all on, the phase value P3 the
	// modules of P2 and P3 (release G3, WP-863), a comma list of module
	// keys only the modules named (server.ParseModules).
	Modules string

	// Documents holds the upload limits of the document module (SEC-09,
	// WP-268).
	Documents DocumentSettings

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

		ProbeStreamMaxBytes: l.int64("RETICORA_PROBE_STREAM_MAX_BYTES", DefaultProbeStreamMaxBytes),
		ProbeEdgeWorkers:    l.integer("RETICORA_PROBE_EDGE_WORKERS", MinProbeEdgeWorkers),
		RedisURL:            l.str("RETICORA_REDIS_URL", "redis://localhost:6379"),

		S3Endpoint:  l.str("RETICORA_S3_ENDPOINT", "localhost:9000"),
		S3Bucket:    l.str("RETICORA_S3_BUCKET", "reticora"),
		S3AccessKey: l.str("RETICORA_S3_ACCESS_KEY", "reticora"),
		S3SecretKey: l.str("RETICORA_S3_SECRET_KEY", "reticora_dev"),
		S3UseSSL:    l.str("RETICORA_S3_USE_SSL", "false") == "true",
		S3PublicURL: l.str("RETICORA_S3_PUBLIC_URL", ""),
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
		KeycloakInternalURL:       l.str("RETICORA_KEYCLOAK_INTERNAL_URL", ""),
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

		RetentionAuditYears:      l.integer("RETICORA_RETENTION_AUDIT_YEARS", MinRetentionAuditYears),
		RetentionHistoryDays:     l.integer("RETICORA_RETENTION_HISTORY_DAYS", MinRetentionHistoryDays),
		RetentionSoftDeleteDays:  l.integer("RETICORA_RETENTION_SOFT_DELETE_DAYS", MinRetentionSoftDeleteDays),
		RetentionReviewItemDays:  l.integer("RETICORA_RETENTION_REVIEW_ITEM_DAYS", MinRetentionReviewItemDays),
		RetentionWorkflowRunDays: l.integer("RETICORA_RETENTION_WORKFLOW_RUN_DAYS", DefaultRetentionWorkflowRunDays),
		EventsMaxAge:             l.duration("RETICORA_EVENTS_MAX_AGE", DefaultEventsMaxAge),
		EventsMaxBytes:           l.int64("RETICORA_EVENTS_MAX_BYTES", DefaultEventsMaxBytes),
		ProbeAssignTick:          l.duration("RETICORA_PROBE_ASSIGN_TICK", DefaultProbeAssignTick),
		ADMPCacheTTLDays:         l.integer("RETICORA_ADMP_CACHE_TTL_DAYS", DefaultADMPCacheTTLDays),
		VulnfeedCSAFProviders:    splitList(l.str("RETICORA_VULNFEED_CSAF_PROVIDERS", "")),
		VulnfeedCSAFInterval:     l.duration("RETICORA_VULNFEED_CSAF_INTERVAL", DefaultVulnfeedCSAFInterval),
		VulnfeedNVDAPIKey:        strings.TrimSpace(l.str("RETICORA_VULNFEED_NVD_API_KEY", "")),
		VulnfeedNVDInterval:      l.duration("RETICORA_VULNFEED_NVD_INTERVAL", DefaultVulnfeedNVDInterval),
		VulnfeedKEVURL:           strings.TrimSpace(l.str("RETICORA_VULNFEED_KEV_URL", "")),
		VulnfeedBundlePublicKey:  strings.TrimSpace(l.str("RETICORA_VULNFEED_BUNDLE_PUBLIC_KEY", "")),
		EgressAllowPrivate:       l.boolean("RETICORA_EGRESS_ALLOW_PRIVATE", false),
		PreAuthRateLimitRPM:      l.integer("RETICORA_PREAUTH_RATE_LIMIT_RPM", 20),
		TrustedProxies:           l.str("RETICORA_TRUSTED_PROXIES", "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"),

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

		Documents: DocumentSettings{
			MaxUploadMB:       l.integer("RETICORA_DOCUMENT_MAX_UPLOAD_MB", DefaultDocumentMaxUploadMB),
			UploadConcurrency: l.integer("RETICORA_DOCUMENT_UPLOAD_CONCURRENCY", DefaultDocumentUploadConcurrency),
			UploadTimeout:     l.duration("RETICORA_DOCUMENT_UPLOAD_TIMEOUT", DefaultDocumentUploadTimeout),
			ClamdAddr:         l.str("RETICORA_CLAMD_ADDR", ""),
			ClamdTimeout:      l.duration("RETICORA_CLAMD_TIMEOUT", DefaultClamdTimeout),
		},

		LogLevel: slog.LevelInfo,
	}

	cfg.workers = make(map[string]bool, len(WorkerNames))
	for _, name := range WorkerNames {
		cfg.workers[name] = l.boolean(WorkerEnvKey(name), true)
	}
	cfg.warnings = l.checkWorkerSwitches(cfg.workers)
	cfg.warnings = append(cfg.warnings, moduleAliasWarnings(cfg.Modules)...)
	if cfg.OpsAlertToken != "" && cfg.OpsAlertOrgID == "" {
		cfg.warnings = append(cfg.warnings, "RETICORA_OPS_ALERT_TOKEN is set without RETICORA_OPS_ALERT_ORG_ID: "+
			"the Alertmanager bridge answers 404 (E-75)")
	}
	// Outside production the server may run without the NATS ingest queue,
	// but then it ingests in the request without identity locks (F-282);
	// production refuses that in Validate.
	if !l.set["RETICORA_NATS_URL"] && cfg.Environment != EnvProduction {
		cfg.warnings = append(cfg.warnings, natsIngestWarning)
	}

	cfg.explicit = l.set
	cfg.invalid = l.invalid

	// Compose derives the accept and the verify URL from
	// RETICORA_PUBLIC_BASE_URL, which may end with a slash:
	// https://host//invitations/accept becomes
	// https://host/invitations/accept, https://host//onboarding becomes
	// https://host/onboarding.
	cfg.InvitationAcceptURL = collapseLeadingSlashes(cfg.InvitationAcceptURL)
	cfg.SignupVerifyURL = collapseLeadingSlashes(cfg.SignupVerifyURL)

	if cfg.Environment == EnvDevelopment {
		cfg.LogLevel = slog.LevelDebug
		if cfg.Documents.ClamdAddr == "" {
			cfg.Documents.ClamdAddr = DefaultClamdAddr
		}
		if cfg.InvitationAcceptURL == "" {
			cfg.InvitationAcceptURL = DefaultInvitationAcceptURL
		}
		if cfg.SignupVerifyURL == "" {
			cfg.SignupVerifyURL = DefaultSignupVerifyURL
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

// DefaultSignupVerifyURL is the onboarding page of the development frontend
// (Vite on port 5173), which redeems the verification token of a signup.
const DefaultSignupVerifyURL = "http://localhost:5173/onboarding"

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

// smtpLocalhost reports whether Go's net/smtp sends a login to host over an
// unencrypted connection: only to these literal names (smtp.PlainAuth).
func smtpLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Minimum retention periods of NFR-06 and AUD-09.
const (
	MinRetentionAuditYears     = 10
	MinRetentionHistoryDays    = 3 * 365
	MinRetentionSoftDeleteDays = 90
	MinRetentionReviewItemDays = 365
	// DefaultRetentionWorkflowRunDays and MinRetentionWorkflowRunDays bound
	// the retention of ended workflow runs (E-241, WP-1093).
	DefaultRetentionWorkflowRunDays = 400
	MinRetentionWorkflowRunDays     = 30
)

// DefaultADMPCacheTTLDays and MaxADMPCacheTTLDays bound the TTL of the
// ADMP cache (E-243, WP-929).
const (
	DefaultADMPCacheTTLDays = 30
	MaxADMPCacheTTLDays     = 3650
)

// Retention of the event stream RETICORA_EVENTS (EVT-01): 30 days and
// 10 GB by default; the events are kept at least 30 days (NFR-06).
const (
	DefaultEventsMaxAge = 30 * 24 * time.Hour
	// DefaultProbeAssignTick, MinProbeAssignTick and MaxProbeAssignTick
	// bound RETICORA_PROBE_ASSIGN_TICK (WP-963): above 30 s the failover
	// of a member could exceed 60 s (PRB-03).
	DefaultProbeAssignTick = 5 * time.Second
	MinProbeAssignTick     = time.Second
	MaxProbeAssignTick     = 30 * time.Second
	MinEventsMaxAge        = 30 * 24 * time.Hour
	DefaultEventsMaxBytes  = int64(10) << 30
)

// Ingest lanes per organization (RETICORA_INGEST_LANES, WP-334): the
// default and the largest value (discovery.MaxIngestLanes). Every lane may
// hold a database connection; the server bounds the batches in work to a
// quarter of its pool whatever the value.
const (
	DefaultIngestLanes = 4
	MaxIngestLanes     = 16
)

// Probe edge (WP-959): the byte limit of the stream RETICORA_PROBE
// (probe.DefaultStreamMaxBytes) and the bounds of the handler pool per
// instance (probe.DefaultEdgeWorkers).
const (
	DefaultProbeStreamMaxBytes = int64(5) << 30
	MinProbeEdgeWorkers        = 8
	MaxProbeEdgeWorkers        = 256
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
	// WorkerHealth recomputes ci.health from the monitoring samples every
	// minute (DB-05, MON-02); unlike the alert evaluator (monitoring, module
	// alerting) it runs in the G1 default configuration (F-087).
	WorkerHealth            = "health"
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
	// WorkerComputedRefresh runs as the system job
	// attribute.computed_refresh of the scheduler: the hourly refresh of
	// computed attributes (JOB-02 [P2], WP-718).
	WorkerComputedRefresh = "computed_refresh"
	// WorkerProbeAssign runs as the system job probe.assign of the
	// scheduler: the assignment of the probes to the members of their
	// collector group every 5 s (RETICORA_PROBE_ASSIGN_TICK), after the
	// liveness sweep of the members (PRB-03, PRB-10, PRB-11, WP-941,
	// WP-963; module monitoring).
	WorkerProbeAssign = "probe_assign"
	// WorkerProbeIngest consumes the probe result batches of the stream
	// RETICORA_PROBE and writes them to probe_result (NFR-11, PRB-07,
	// WP-960; module monitoring).
	WorkerProbeIngest = "probe_ingest"
	// WorkerWorkflow runs the workflows (OPS-02 [P3], WP-1114; module
	// workflows): the durable consumer workflow-triggers of the event
	// triggers and the consumer of the job kind workflow.run. Switched off,
	// the instance still queues runs (jobs.Queue.Declare), another instance
	// executes them.
	WorkerWorkflow = "workflow"
	// WorkerIPAMScanApply runs as the system job ipam.scan.apply of the
	// scheduler: every minute the probe states of the subnet scan become
	// address states, last_seen_at and new hosts (IPM-11, WP-1063; module
	// ipam).
	WorkerIPAMScanApply = "ipam_scan_apply"
)

// WorkerNames lists every registered worker.
var WorkerNames = []string{
	WorkerWebhookDispatcher,
	WorkerExport,
	WorkerIngest,
	WorkerMonitoring,
	WorkerHealth,
	WorkerScheduler,
	WorkerJobs,
	WorkerEvents,
	WorkerNotifier,
	WorkerNATSEdge,
	WorkerCredentialBundles,
	WorkerDiscoveryScopes,
	WorkerOfflineDetector,
	WorkerComputedRefresh,
	WorkerProbeAssign,
	WorkerProbeIngest,
	WorkerWorkflow,
	WorkerIPAMScanApply,
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

// natsIngestWarning is the start warning without RETICORA_NATS_URL outside
// production: the identity locks of the ingest (E-56) exist only on the NATS
// queue path, so several instances without NATS can duplicate devices with a
// weak identity (F-282).
const natsIngestWarning = "RETICORA_NATS_URL is not set: the discovery ingest runs in the request without identity locks; " +
	"several instances can duplicate devices with a weak identity, so run one instance or configure NATS (docs/betrieb/ingest-spuren.md)"

// DefaultVulnfeedCSAFInterval is the default of
// RETICORA_VULNFEED_CSAF_INTERVAL (WP-1118).
const DefaultVulnfeedCSAFInterval = 6 * time.Hour

// DefaultVulnfeedNVDInterval is the default of
// RETICORA_VULNFEED_NVD_INTERVAL (WP-1119).
const DefaultVulnfeedNVDInterval = 2 * time.Hour

// VulnfeedNVDEnabled reports whether the NVD and KEV import may run: never
// in the air-gapped profile (only the offline bundle, WP-1120).
func (c *Config) VulnfeedNVDEnabled() bool { return !c.AirGapped }

// MinVulnfeedInterval is the shortest interval of a vulnerability feed.
const MinVulnfeedInterval = 5 * time.Minute

// VulnfeedCSAFExplicit reports whether RETICORA_VULNFEED_CSAF_PROVIDERS
// was set explicitly (the air-gapped profile runs the CSAF import only
// then, E-235).
func (c *Config) VulnfeedCSAFExplicit() bool { return c.explicit["RETICORA_VULNFEED_CSAF_PROVIDERS"] }

// vulnfeedErrors refuses a CSAF provider without HTTPS and an interval
// below MinVulnfeedInterval (WP-1118 AK4).
func (c *Config) vulnfeedErrors() []error {
	var errs []error
	for _, p := range c.VulnfeedCSAFProviders {
		u, err := url.Parse(p)
		if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil {
			errs = append(errs, fmt.Errorf("RETICORA_VULNFEED_CSAF_PROVIDERS: %q is no absolute https URL with a host", p))
		}
	}
	if c.VulnfeedKEVURL != "" {
		u, err := url.Parse(c.VulnfeedKEVURL)
		if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil {
			errs = append(errs, fmt.Errorf("RETICORA_VULNFEED_KEV_URL is no absolute https URL with a host"))
		}
	}
	if c.VulnfeedNVDInterval < MinVulnfeedInterval {
		errs = append(errs, fmt.Errorf("RETICORA_VULNFEED_NVD_INTERVAL must be at least %s, got %s", MinVulnfeedInterval, c.VulnfeedNVDInterval))
	}
	if c.VulnfeedCSAFInterval < MinVulnfeedInterval {
		errs = append(errs, fmt.Errorf("RETICORA_VULNFEED_CSAF_INTERVAL must be at least %s, got %s", MinVulnfeedInterval, c.VulnfeedCSAFInterval))
	}
	return errs
}

// splitList splits a comma separated list and drops empty entries.
func splitList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

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
	// The verification mail of the self-signup carries this link, with the
	// same rules.
	if c.SignupVerifyURL != "" && !absoluteHTTPURL(c.SignupVerifyURL) {
		errs = append(errs, fmt.Errorf("RETICORA_SIGNUP_VERIFY_URL must be an absolute http or https URL with a host and without user info"))
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
		case c.SMTPTLS == "none" && c.SMTPUsername != "" && !smtpLocalhost(c.SMTPHost):
			// Go's net/smtp sends no login over an unencrypted connection
			// to another host: every e-mail would fail (notify.SMTPConfig).
			errs = append(errs, fmt.Errorf("RETICORA_SMTP_USERNAME needs RETICORA_SMTP_TLS=starttls or tls: no login is sent unencrypted to %s", c.SMTPHost))
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
		{"RETICORA_RETENTION_WORKFLOW_RUN_DAYS", c.RetentionWorkflowRunDays, MinRetentionWorkflowRunDays},
	} {
		if r.value < r.min {
			errs = append(errs, fmt.Errorf("%s must be at least %d (NFR-06), got %d", r.key, r.min, r.value))
		}
	}
	if c.ProbeAssignTick < MinProbeAssignTick || c.ProbeAssignTick > MaxProbeAssignTick {
		errs = append(errs, fmt.Errorf("RETICORA_PROBE_ASSIGN_TICK must be between %s and %s, got %s", MinProbeAssignTick, MaxProbeAssignTick, c.ProbeAssignTick))
	}
	if c.EventsMaxAge < MinEventsMaxAge {
		errs = append(errs, fmt.Errorf("RETICORA_EVENTS_MAX_AGE must be at least 30d (NFR-06), got %s", c.EventsMaxAge))
	}
	if c.EventsMaxBytes <= 0 {
		errs = append(errs, fmt.Errorf("RETICORA_EVENTS_MAX_BYTES must be positive, got %d", c.EventsMaxBytes))
	}
	if c.ADMPCacheTTLDays < 1 || c.ADMPCacheTTLDays > MaxADMPCacheTTLDays {
		errs = append(errs, fmt.Errorf("RETICORA_ADMP_CACHE_TTL_DAYS must be between 1 and %d, got %d", MaxADMPCacheTTLDays, c.ADMPCacheTTLDays))
	}
	errs = append(errs, c.vulnfeedErrors()...)
	if c.RateLimitOrgRPM < c.RateLimitRPM {
		errs = append(errs, fmt.Errorf("RETICORA_RATE_LIMIT_ORG_RPM (%d) must not be below RETICORA_RATE_LIMIT_RPM (%d)", c.RateLimitOrgRPM, c.RateLimitRPM))
	}
	if c.IngestLanes < 1 || c.IngestLanes > MaxIngestLanes {
		errs = append(errs, fmt.Errorf("RETICORA_INGEST_LANES must be between 1 and %d, got %d", MaxIngestLanes, c.IngestLanes))
	}
	if c.ProbeStreamMaxBytes <= 0 {
		errs = append(errs, fmt.Errorf("RETICORA_PROBE_STREAM_MAX_BYTES must be positive, got %d", c.ProbeStreamMaxBytes))
	}
	if c.ProbeEdgeWorkers < MinProbeEdgeWorkers || c.ProbeEdgeWorkers > MaxProbeEdgeWorkers {
		errs = append(errs, fmt.Errorf("RETICORA_PROBE_EDGE_WORKERS must be between %d and %d, got %d",
			MinProbeEdgeWorkers, MaxProbeEdgeWorkers, c.ProbeEdgeWorkers))
	}
	if c.RateLimitRPM <= 0 {
		errs = append(errs, fmt.Errorf("RETICORA_RATE_LIMIT_RPM must be positive, got %d", c.RateLimitRPM))
	}
	errs = append(errs, c.Documents.errors()...)
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
	// Production runs the ingest only over the NATS queue, the path with the
	// identity locks (E-56); without it two instances could duplicate devices
	// with a weak identity (F-282).
	if c.Environment == EnvProduction && !c.explicit["RETICORA_NATS_URL"] {
		errs = append(errs, fmt.Errorf("RETICORA_ENVIRONMENT=%s requires RETICORA_NATS_URL: without the NATS ingest queue "+
			"the HTTP ingest takes no identity locks and several instances can duplicate devices (docs/betrieb/ingest-spuren.md)", EnvProduction))
	}
	errs = append(errs, c.documentScanErrors()...)
	return errors.Join(errs...)
}

// Defaults of the document uploads (SEC-09, WP-268).
const (
	DefaultDocumentMaxUploadMB       = 50
	DefaultDocumentUploadConcurrency = 4
	DefaultDocumentUploadTimeout     = 5 * time.Minute
)

// Defaults of the malware scanner (SEC-09, WP-734): the clamd address
// applies in development only, outside it RETICORA_CLAMD_ADDR is required
// with the module documents, which the release default G2 includes
// (WP-853).
const (
	DefaultClamdAddr    = "localhost:3310"
	DefaultClamdTimeout = 60 * time.Second
)

// DocumentSettings are the settings of the document module (SEC-09):
// RETICORA_DOCUMENT_MAX_UPLOAD_MB is the largest body of one upload in MiB
// (default 50), RETICORA_DOCUMENT_UPLOAD_CONCURRENCY the uploads one
// instance runs at the same time (default 4, beyond that 503), and
// RETICORA_DOCUMENT_UPLOAD_TIMEOUT the read and write deadline of an upload
// request (default 5 min) in place of the global 15 s (WP-268).
// RETICORA_CLAMD_ADDR (host:port) and RETICORA_CLAMD_TIMEOUT (default 60 s)
// are the clamd of the malware scan (WP-734).
type DocumentSettings struct {
	MaxUploadMB       int
	UploadConcurrency int
	UploadTimeout     time.Duration
	ClamdAddr         string
	ClamdTimeout      time.Duration
}

// MaxUploadBytes is MaxUploadMB in bytes.
func (d DocumentSettings) MaxUploadBytes() int64 {
	return int64(d.MaxUploadMB) << 20
}

// errors rejects values of zero or below (fail-closed, OPS-01).
func (d DocumentSettings) errors() []error {
	var errs []error
	if d.MaxUploadMB <= 0 || int64(d.MaxUploadMB) > math.MaxInt64>>20 {
		errs = append(errs, fmt.Errorf("RETICORA_DOCUMENT_MAX_UPLOAD_MB must be positive, got %d", d.MaxUploadMB))
	}
	if d.UploadConcurrency <= 0 {
		errs = append(errs, fmt.Errorf("RETICORA_DOCUMENT_UPLOAD_CONCURRENCY must be positive, got %d", d.UploadConcurrency))
	}
	if d.UploadTimeout <= 0 {
		errs = append(errs, fmt.Errorf("RETICORA_DOCUMENT_UPLOAD_TIMEOUT must be positive, got %s", d.UploadTimeout))
	}
	if d.ClamdAddr != "" && !hostPort(d.ClamdAddr) {
		errs = append(errs, fmt.Errorf("RETICORA_CLAMD_ADDR=%q is not host:port", d.ClamdAddr))
	}
	if d.ClamdTimeout <= 0 {
		errs = append(errs, fmt.Errorf("RETICORA_CLAMD_TIMEOUT must be positive, got %s", d.ClamdTimeout))
	}
	return errs
}

// hostPort reports whether addr is host:port with a host and a port from 1
// to 65535.
func hostPort(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || strings.ContainsAny(host, "/ ") {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// ModuleAliasAlerting is the former key of the module monitoring
// (WP-863, E-160); server.ParseModules accepts it, Load reports it.
const ModuleAliasAlerting = "alerting"

// moduleAliasWarnings reports the former module keys in RETICORA_MODULES.
func moduleAliasWarnings(spec string) []string {
	var warnings []string
	for _, key := range strings.Split(spec, ",") {
		if strings.TrimSpace(key) == ModuleAliasAlerting {
			warnings = append(warnings, "RETICORA_MODULES: module alerting is deprecated: use monitoring")
		}
	}
	return warnings
}

// documentsEnabled reports whether RETICORA_MODULES switches the module
// documents on, read like server.ParseModules: the empty release default
// G2 (WP-830), "all", the phase values P2 and P3 or the key documents. Since
// WP-853 (SR-57) the empty default counts as well: a deployment that leaves
// RETICORA_MODULES unset runs documents and needs clamd, otherwise every
// upload would stay pending_scan without a hint at the start.
func (c *Config) documentsEnabled() bool {
	spec := strings.TrimSpace(c.Modules)
	if spec == "" || strings.EqualFold(spec, "all") {
		return true
	}
	for _, key := range strings.Split(spec, ",") {
		switch strings.TrimSpace(key) {
		case "documents", "P2", "P3":
			return true
		}
	}
	return false
}

// documentScanErrors is the fail-closed start check of the malware scan
// (OPS-01, E-42): outside development the module documents needs an
// explicit clamd (RETICORA_CLAMD_ADDR) and the job queue
// (RETICORA_NATS_URL); without either every upload would stay locked.
func (c *Config) documentScanErrors() []error {
	if c.IsDevelopment() || !c.documentsEnabled() {
		return nil
	}
	var missing []string
	for _, key := range []string{"RETICORA_CLAMD_ADDR", "RETICORA_NATS_URL"} {
		if !c.explicit[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []error{fmt.Errorf("RETICORA_ENVIRONMENT=%s with the module documents requires %s: "+
		"the malware scan of uploads runs on clamd over the job queue (SEC-09)", c.Environment, strings.Join(missing, ", "))}
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

func (l *loader) int64(key string, defaultValue int64) int64 {
	v := l.str(key, "")
	if v == "" {
		return defaultValue
	}
	i, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.invalid = append(l.invalid, key)
		return defaultValue
	}
	return i
}

// duration reads a Go duration ("720h") or a whole number of days ("30d").
func (l *loader) duration(key string, defaultValue time.Duration) time.Duration {
	v := l.str(key, "")
	if v == "" {
		return defaultValue
	}
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 || int64(n) > math.MaxInt64/int64(24*time.Hour) {
			l.invalid = append(l.invalid, key)
			return defaultValue
		}
		return time.Duration(n) * 24 * time.Hour
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.invalid = append(l.invalid, key)
		return defaultValue
	}
	return d
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
