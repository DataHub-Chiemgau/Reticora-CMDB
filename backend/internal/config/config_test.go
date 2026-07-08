package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.Port != "8080" {
		t.Errorf("expected default port 8080, got %s", cfg.Port)
	}
	if cfg.Environment != "development" {
		t.Errorf("expected default environment development, got %s", cfg.Environment)
	}
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("RETICORA_PORT", "9090")
	os.Setenv("RETICORA_ENVIRONMENT", "production")
	defer os.Unsetenv("RETICORA_PORT")
	defer os.Unsetenv("RETICORA_ENVIRONMENT")

	cfg := Load()
	if cfg.Port != "9090" {
		t.Errorf("expected port 9090, got %s", cfg.Port)
	}
	if cfg.Environment != "production" {
		t.Errorf("expected environment production, got %s", cfg.Environment)
	}
}
