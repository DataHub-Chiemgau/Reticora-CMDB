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

	// Encryption
	MasterKey string // 32-byte base64-encoded master key for envelope encryption

	// Observability
	OTelEndpoint string

	// Rate Limiting
	RateLimitRPM int // requests per minute per key/user (default 600)
}

// Load reads configuration from environment variables with RETICORA_ prefix
// and sensible defaults.
func Load() *Config {
	cfg := &Config{
		Port:        envOrDefault("RETICORA_PORT", "8080"),
		Environment: envOrDefault("RETICORA_ENVIRONMENT", "development"),

		DatabaseURL: envOrDefault("RETICORA_DATABASE_URL", "******localhost:5432/reticora?sslmode=disable"),
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

		MasterKey: envOrDefault("RETICORA_MASTER_KEY", ""),

		OTelEndpoint: envOrDefault("RETICORA_OTEL_ENDPOINT", ""),
		RateLimitRPM: envOrDefaultInt("RETICORA_RATE_LIMIT_RPM", 600),

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
