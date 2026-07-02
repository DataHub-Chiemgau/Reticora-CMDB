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

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	cfg := &Config{
		Port:         envOrDefault("PORT", "8080"),
		DatabaseURL:  envOrDefault("DATABASE_URL", "postgres://localhost:5432/reticora?sslmode=disable"),
		NATSUrl:      envOrDefault("NATS_URL", "nats://localhost:4222"),
		RedisURL:     envOrDefault("REDIS_URL", "redis://localhost:6379"),
		OTelEndpoint: envOrDefault("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		Environment:  envOrDefault("ENVIRONMENT", "development"),
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
