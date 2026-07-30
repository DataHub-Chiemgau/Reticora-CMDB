// Package config provides configuration loading for Reticora services.
// All configuration is read exclusively from environment variables with
// the RETICORA_ prefix.
package config

import (
	"log/slog"
	"os"
	"strconv"
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

	// Auth / OIDC
	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	SessionKeyPath   string // path to RS256 private key PEM for session JWTs

	// Entitlements
	DefaultPlan            string // plan applied to tenants without entitlement rows
	EntitlementEnforcement bool   // when false, feature gating is reported but not enforced

	// Encryption
	MasterKey string // 32-byte base64-encoded master key for envelope encryption

	// Observability
	OTelEndpoint string

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
}

// Load reads configuration from environment variables with RETICORA_ prefix
// and sensible defaults.
func Load() *Config {
	cfg := &Config{
		Port:        envOrDefault("RETICORA_PORT", "8080"),
		Environment: envOrDefault("RETICORA_ENVIRONMENT", "development"),

		DatabaseURL: envOrDefault("RETICORA_DATABASE_URL", "postgres://localhost:5432/reticora?sslmode=disable"),
		NATSUrl:     envOrDefault("RETICORA_NATS_URL", "nats://localhost:4222"),
		RedisURL:    envOrDefault("RETICORA_REDIS_URL", "redis://localhost:6379"),

		S3Endpoint:  envOrDefault("RETICORA_S3_ENDPOINT", "localhost:9000"),
		S3Bucket:    envOrDefault("RETICORA_S3_BUCKET", "reticora"),
		S3AccessKey: envOrDefault("RETICORA_S3_ACCESS_KEY", "reticora"),
		S3SecretKey: envOrDefault("RETICORA_S3_SECRET_KEY", "reticora_dev"),
		S3UseSSL:    envOrDefault("RETICORA_S3_USE_SSL", "false") == "true",

		OIDCIssuerURL:    envOrDefault("RETICORA_OIDC_ISSUER_URL", "http://localhost:8180/realms/reticora"),
		OIDCClientID:     envOrDefault("RETICORA_OIDC_CLIENT_ID", "reticora-app"),
		OIDCClientSecret: envOrDefault("RETICORA_OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:  envOrDefault("RETICORA_OIDC_REDIRECT_URL", ""),
		SessionKeyPath:   envOrDefault("RETICORA_SESSION_KEY_PATH", ""),

		DefaultPlan:            envOrDefault("RETICORA_DEFAULT_PLAN", "essential"),
		EntitlementEnforcement: envOrDefault("RETICORA_ENTITLEMENT_ENFORCEMENT", "true") != "false",

		MasterKey: envOrDefault("RETICORA_MASTER_KEY", ""),

		OTelEndpoint: envOrDefault("RETICORA_OTEL_ENDPOINT", ""),
		RateLimitRPM: envOrDefaultInt("RETICORA_RATE_LIMIT_RPM", 600),

		SearchBackend:      envOrDefault("RETICORA_SEARCH_BACKEND", "postgres"),
		OpenSearchURL:      envOrDefault("RETICORA_OPENSEARCH_URL", ""),
		OpenSearchUsername: envOrDefault("RETICORA_OPENSEARCH_USERNAME", ""),
		OpenSearchPassword: envOrDefault("RETICORA_OPENSEARCH_PASSWORD", ""),
		OpenSearchIndex:    envOrDefault("RETICORA_OPENSEARCH_INDEX", "reticora-search"),

		LLMBaseURL:        envOrDefault("RETICORA_LLM_BASE_URL", ""),
		LLMAPIKey:         envOrDefault("RETICORA_LLM_API_KEY", ""),
		LLMChatModel:      envOrDefault("RETICORA_LLM_CHAT_MODEL", ""),
		LLMEmbeddingModel: envOrDefault("RETICORA_LLM_EMBEDDING_MODEL", ""),

		LogLevel: slog.LevelInfo,
	}

	if cfg.Environment == "development" {
		cfg.LogLevel = slog.LevelDebug
	}

	return cfg
}

func envOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func envOrDefaultInt(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultValue
}
