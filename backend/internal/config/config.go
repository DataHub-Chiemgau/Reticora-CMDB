// Package config provides configuration loading for Reticora services.
// All configuration is read exclusively from environment variables with
// the RETICORA_ prefix.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
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

	// Entitlements
	DefaultPlan string // plan applied to tenants without entitlement rows
	// DefaultProvisionRole names the standard role assigned to a user on first
	// OIDC login; empty assigns no role (admins assign explicitly).
	DefaultProvisionRole   string
	EntitlementEnforcement bool // when false, feature gating is reported but not enforced

	// Encryption
	MasterKey string // 32-byte base64-encoded master key for envelope encryption

	// Observability
	OTelEndpoint string
	// MetricsTenantLabel enables the organization_id label on HTTP request
	// metrics. It multiplies the series count by the number of tenants, so it
	// is opt-in for bounded-tenant deployments.
	MetricsTenantLabel bool

	// EgressAllowPrivate permits webhook and connector destinations in
	// private networks (on-premises installations); cloud metadata endpoints
	// stay blocked (SEC-08). Default off.
	EgressAllowPrivate bool

	// PreAuthRateLimitRPM is the budget of authentication attempts per
	// client IP and minute (AUT-10, default 20).
	PreAuthRateLimitRPM int
	// TrustedProxies lists the reverse proxies (CIDRs) whose X-Real-IP
	// header names the client for IP-based limits.
	TrustedProxies string

	// Rate Limiting
	RateLimitRPM int // requests per minute per key/user (default 600)

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

	// explicit records the RETICORA_* variables that were set; invalid
	// lists variables whose value could not be parsed.
	explicit map[string]bool
	invalid  []string
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

		DefaultPlan:            l.str("RETICORA_DEFAULT_PLAN", "essential"),
		DefaultProvisionRole:   l.str("RETICORA_DEFAULT_PROVISION_ROLE", "viewer"),
		EntitlementEnforcement: l.str("RETICORA_ENTITLEMENT_ENFORCEMENT", "true") != "false",

		MasterKey: l.str("RETICORA_MASTER_KEY", ""),

		OTelEndpoint: l.str("RETICORA_OTEL_ENDPOINT", ""),
		// Opt-in: the organization_id label multiplies the HTTP metric series
		// by the tenant count. Default off; enable for bounded-tenant setups.
		MetricsTenantLabel:  l.boolean("RETICORA_METRICS_TENANT_LABEL", false),
		RateLimitRPM:        l.integer("RETICORA_RATE_LIMIT_RPM", 600),
		EgressAllowPrivate:  l.boolean("RETICORA_EGRESS_ALLOW_PRIVATE", false),
		PreAuthRateLimitRPM: l.integer("RETICORA_PREAUTH_RATE_LIMIT_RPM", 20),
		TrustedProxies:      l.str("RETICORA_TRUSTED_PROXIES", "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"),

		SearchBackend:      l.str("RETICORA_SEARCH_BACKEND", "postgres"),
		OpenSearchURL:      l.str("RETICORA_OPENSEARCH_URL", ""),
		OpenSearchUsername: l.str("RETICORA_OPENSEARCH_USERNAME", ""),
		OpenSearchPassword: l.str("RETICORA_OPENSEARCH_PASSWORD", ""),
		OpenSearchIndex:    l.str("RETICORA_OPENSEARCH_INDEX", "reticora-search"),

		LLMBaseURL:        l.str("RETICORA_LLM_BASE_URL", ""),
		LLMAPIKey:         l.str("RETICORA_LLM_API_KEY", ""),
		LLMChatModel:      l.str("RETICORA_LLM_CHAT_MODEL", ""),
		LLMEmbeddingModel: l.str("RETICORA_LLM_EMBEDDING_MODEL", ""),

		LogLevel: slog.LevelInfo,
	}

	cfg.explicit = l.set
	cfg.invalid = l.invalid

	if cfg.Environment == EnvDevelopment {
		cfg.LogLevel = slog.LevelDebug
	} else if !cfg.anySet(s3Keys...) {
		// Outside development the local MinIO defaults are never used
		// silently: without explicit settings object storage is disabled.
		cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey = "", "", ""
	}

	return cfg
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
	if c.RateLimitRPM <= 0 {
		errs = append(errs, fmt.Errorf("RETICORA_RATE_LIMIT_RPM must be positive, got %d", c.RateLimitRPM))
	}
	if !c.IsDevelopment() {
		if noDB {
			errs = append(errs, fmt.Errorf("--no-db is only allowed with RETICORA_ENVIRONMENT=%s", EnvDevelopment))
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
