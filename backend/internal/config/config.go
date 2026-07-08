// Package config provides configuration loading for Reticora services.
package config

import (
	"log/slog"
	"os"
)

// Config holds the application configuration.
type Config struct {
	Port         string
	DatabaseURL  string
	NATSUrl      string
	RedisURL     string
	OTelEndpoint string
	Environment  string
	LogLevel     slog.Level
}

// Load reads configuration from environment variables with RETICORA_ prefix
// and sensible defaults.
func Load() *Config {
	cfg := &Config{
		Port:         envOrDefault("RETICORA_PORT", "8080"),
		DatabaseURL:  envOrDefault("RETICORA_DATABASE_URL", "postgres://localhost:5432/reticora?sslmode=disable"),
		NATSUrl:      envOrDefault("RETICORA_NATS_URL", "nats://localhost:4222"),
		RedisURL:     envOrDefault("RETICORA_REDIS_URL", "redis://localhost:6379"),
		OTelEndpoint: envOrDefault("RETICORA_OTEL_ENDPOINT", ""),
		Environment:  envOrDefault("RETICORA_ENVIRONMENT", "development"),
		LogLevel:     slog.LevelInfo,
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
