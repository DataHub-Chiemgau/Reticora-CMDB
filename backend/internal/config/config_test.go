package config

import (
	"os"
	"strings"
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

// clearEnv blanks every variable Validate looks at; an empty value counts as
// unset.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range append(append([]string{
		"RETICORA_ENVIRONMENT", "RETICORA_RATE_LIMIT_RPM", "RETICORA_METRICS_TENANT_LABEL", "RETICORA_NATS_URL",
	}, requiredOutsideDevelopment...), s3Keys...) {
		t.Setenv(key, "")
	}
}

func setAll(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

var productionEnv = map[string]string{
	"RETICORA_ENVIRONMENT":     EnvProduction,
	"RETICORA_DATABASE_URL":    "postgres://db/reticora",
	"RETICORA_REDIS_URL":       "redis://redis:6379",
	"RETICORA_MASTER_KEY":      "key",
	"RETICORA_OIDC_ISSUER_URL": "https://id.example/realms/reticora",
}

func TestValidateDevelopmentDefaults(t *testing.T) {
	clearEnv(t)
	cfg := Load()
	if err := cfg.Validate(true); err != nil {
		t.Fatalf("development defaults: %v", err)
	}
	if !cfg.S3Configured() {
		t.Fatal("development keeps the local object storage defaults")
	}
}

// TestValidateProductionRequiresExplicitServices: OPS-01 – no silent
// localhost defaults outside development; all gaps are reported at once.
func TestValidateProductionRequiresExplicitServices(t *testing.T) {
	clearEnv(t)
	t.Setenv("RETICORA_ENVIRONMENT", EnvStaging)
	err := Load().Validate(false)
	if err == nil {
		t.Fatal("staging without explicit services must not start")
	}
	for _, key := range requiredOutsideDevelopment {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not name %s", err, key)
		}
	}
}

func TestValidateProductionComplete(t *testing.T) {
	clearEnv(t)
	setAll(t, productionEnv)
	cfg := Load()
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("complete production config: %v", err)
	}
	if cfg.S3Configured() || cfg.S3AccessKey != "" || cfg.S3SecretKey != "" {
		t.Fatal("production must not use the development object storage defaults")
	}
	if cfg.IsSet("RETICORA_NATS_URL") {
		t.Fatal("NATS was not configured")
	}
}

func TestValidateProductionPartialObjectStorage(t *testing.T) {
	clearEnv(t)
	setAll(t, productionEnv)
	t.Setenv("RETICORA_S3_ENDPOINT", "minio:9000")
	err := Load().Validate(false)
	if err == nil || !strings.Contains(err.Error(), "RETICORA_S3_ACCESS_KEY") || !strings.Contains(err.Error(), "RETICORA_S3_SECRET_KEY") {
		t.Fatalf("partial S3 config: err = %v", err)
	}
	t.Setenv("RETICORA_S3_ACCESS_KEY", "a")
	t.Setenv("RETICORA_S3_SECRET_KEY", "b")
	cfg := Load()
	if err := cfg.Validate(false); err != nil || !cfg.S3Configured() {
		t.Fatalf("complete S3 config: err = %v, configured = %v", err, cfg.S3Configured())
	}
}

func TestValidateRejectsInvalidSettings(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		noDB bool
		want string
	}{
		{"no-db outside development", productionEnv, true, "--no-db"},
		{"unknown environment", map[string]string{"RETICORA_ENVIRONMENT": "prod"}, false, `"prod"`},
		{"unparsable integer", map[string]string{"RETICORA_RATE_LIMIT_RPM": "many"}, false, "RETICORA_RATE_LIMIT_RPM has an invalid value"},
		{"non-positive rate limit", map[string]string{"RETICORA_RATE_LIMIT_RPM": "0"}, false, "must be positive"},
		{"unparsable bool", map[string]string{"RETICORA_METRICS_TENANT_LABEL": "maybe"}, false, "RETICORA_METRICS_TENANT_LABEL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			setAll(t, tc.env)
			err := Load().Validate(tc.noDB)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
