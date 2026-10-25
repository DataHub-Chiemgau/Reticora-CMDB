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
		"RETICORA_ENVIRONMENT", "RETICORA_RATE_LIMIT_RPM", "RETICORA_RATE_LIMIT_ORG_RPM", "RETICORA_METRICS_TENANT_LABEL", "RETICORA_NATS_URL",
		"RETICORA_INGEST_LANES", "RETICORA_INVITATION_ACCEPT_URL",
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
		{"org rate limit below the credential limit", map[string]string{"RETICORA_RATE_LIMIT_ORG_RPM": "100"}, false, "must not be below"},
		{"smtp without sender", map[string]string{"RETICORA_SMTP_HOST": "mail.example"}, false, "RETICORA_SMTP_FROM is required"},
		{"smtp with an unknown tls mode", map[string]string{"RETICORA_SMTP_HOST": "mail.example", "RETICORA_SMTP_FROM": "noreply@example.org",
			"RETICORA_SMTP_TLS": "ssl"}, false, "RETICORA_SMTP_TLS"},
		{"audit retention below NFR-06", map[string]string{"RETICORA_RETENTION_AUDIT_YEARS": "9"}, false, "RETICORA_RETENTION_AUDIT_YEARS must be at least 10"},
		{"history retention below NFR-06", map[string]string{"RETICORA_RETENTION_HISTORY_DAYS": "30"}, false, "RETICORA_RETENTION_HISTORY_DAYS must be at least 1095"},
		{"soft-delete retention below NFR-06", map[string]string{"RETICORA_RETENTION_SOFT_DELETE_DAYS": "7"}, false, "at least 90"},
		{"review retention below AUD-09", map[string]string{"RETICORA_RETENTION_REVIEW_ITEM_DAYS": "30"}, false, "at least 365"},
		{"unparsable bool", map[string]string{"RETICORA_METRICS_TENANT_LABEL": "maybe"}, false, "RETICORA_METRICS_TENANT_LABEL"},
		{"no ingest lane", map[string]string{"RETICORA_INGEST_LANES": "0"}, false, "RETICORA_INGEST_LANES must be between 1 and 16, got 0"},
		{"too many ingest lanes", map[string]string{"RETICORA_INGEST_LANES": "17"}, false, "RETICORA_INGEST_LANES must be between 1 and 16, got 17"},
		{"unparsable ingest lanes", map[string]string{"RETICORA_INGEST_LANES": "x"}, false, "RETICORA_INGEST_LANES has an invalid value"},
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

// TestInvitationAcceptURL covers WP-421 (TLC-03, NTF-05, F-041): the link
// of the invitation mail. Development defaults to the Vite accept page;
// outside development there is no default (issue and renew answer 503).
// http is valid in production, the HTTP default path of the installer runs
// production on http://<host>; a doubled slash after the host (base URL with
// a trailing slash) is collapsed. A URL without host, a relative URL,
// another scheme and user info fail the start check.
func TestInvitationAcceptURL(t *testing.T) {
	clearEnv(t)
	if cfg := Load(); cfg.InvitationAcceptURL != "http://localhost:5173/invitations/accept" || cfg.Validate(true) != nil {
		t.Errorf("development default: %q, %v", cfg.InvitationAcceptURL, cfg.Validate(true))
	}
	setAll(t, productionEnv)
	if cfg := Load(); cfg.InvitationAcceptURL != "" || cfg.Validate(false) != nil {
		t.Errorf("production without accept URL: %q, %v; want none and a valid configuration", cfg.InvitationAcceptURL, cfg.Validate(false))
	}
	for _, c := range []struct{ in, want string }{
		{"http://192.0.2.10/invitations/accept", "http://192.0.2.10/invitations/accept"},
		{"http://192.0.2.10:3000/invitations/accept", "http://192.0.2.10:3000/invitations/accept"},
		{"https://cmdb.example.com/invitations/accept", "https://cmdb.example.com/invitations/accept"},
		{"https://cmdb.example.com//invitations/accept", "https://cmdb.example.com/invitations/accept"},
	} {
		t.Setenv("RETICORA_INVITATION_ACCEPT_URL", c.in)
		cfg := Load()
		if err := cfg.Validate(false); err != nil || cfg.InvitationAcceptURL != c.want {
			t.Errorf("production with %q: %q, %v; want %q and a valid configuration", c.in, cfg.InvitationAcceptURL, err, c.want)
		}
	}
	for _, env := range []string{EnvProduction, EnvDevelopment} {
		t.Setenv("RETICORA_ENVIRONMENT", env)
		for _, bad := range []string{"https:///invitations/accept", "/invitations/accept", "invitations/accept",
			"ftp://cmdb.example.com/invitations/accept", "https://user:secret@cmdb.example.com/invitations/accept"} {
			t.Setenv("RETICORA_INVITATION_ACCEPT_URL", bad)
			err := Load().Validate(false)
			if err == nil || !strings.Contains(err.Error(), "RETICORA_INVITATION_ACCEPT_URL") {
				t.Errorf("%s with %q: %v, want an error naming RETICORA_INVITATION_ACCEPT_URL", env, bad, err)
			} else if strings.Contains(err.Error(), "secret") {
				t.Errorf("the start check repeats the value: %v", err)
			}
		}
	}
}

// TestIngestLanesSetting covers WP-334 (NFR-03, E-56): four ingest lanes by
// default, one (the serial processing) and sixteen are accepted.
func TestIngestLanesSetting(t *testing.T) {
	clearEnv(t)
	if cfg := Load(); cfg.IngestLanes != 4 || cfg.Validate(true) != nil {
		t.Fatalf("default ingest lanes: %d, %v", cfg.IngestLanes, cfg.Validate(true))
	}
	for _, v := range []string{"1", "16"} {
		t.Setenv("RETICORA_INGEST_LANES", v)
		if cfg := Load(); cfg.Validate(true) != nil || cfg.IngestLanes != map[string]int{"1": 1, "16": 16}[v] {
			t.Errorf("RETICORA_INGEST_LANES=%s: %d, %v", v, cfg.IngestLanes, cfg.Validate(true))
		}
	}
}

// TestMaskDSN covers WP-047 (SEC-01): the password of URL and key=value
// connection strings never survives masking.
func TestMaskDSN(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"postgres://reticora:s3cret@db:5432/reticora?sslmode=disable", "postgres://reticora:***@db:5432/reticora?sslmode=disable"},
		{"postgresql://user:p%40ss@db/x", "postgresql://user:***@db/x"},
		{"postgres://db/x?user=app&password=s3cret&sslmode=require", "postgres://db/x?user=app&password=***&sslmode=require"},
		{"host=db port=5432 user=app password=s3cret dbname=x", "host=db port=5432 user=app password=*** dbname=x"},
		{"host=db PASSWORD='s3c ret' dbname=x", "host=db PASSWORD=*** dbname=x"},
		{"redis://:s3cret@cache:6379/0", "redis://:***@cache:6379/0"},
		{"postgres://app@db/x", "postgres://app@db/x"},
		{"host=db dbname=x", "host=db dbname=x"},
	} {
		got := MaskDSN(c.in)
		if got != c.want {
			t.Errorf("MaskDSN(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "s3c") || strings.Contains(got, "p%40ss") {
			t.Errorf("MaskDSN(%q) leaks the password: %q", c.in, got)
		}
	}
}

// TestWorkerSwitches covers WP-098 (OPS-02): every registered worker is on
// by default and switched off independently by RETICORA_WORKER_<NAME>_ENABLED;
// an invalid value fails the start check; unknown workers are never enabled.
func TestWorkerSwitches(t *testing.T) {
	clearEnv(t)
	for _, name := range WorkerNames {
		t.Setenv(WorkerEnvKey(name), "")
	}
	cfg := Load()
	for _, name := range WorkerNames {
		if !cfg.WorkerEnabled(name) {
			t.Errorf("%s is off by default", name)
		}
	}
	if cfg.WorkerEnabled("unknown") || IsWorker("unknown") {
		t.Error("unknown worker enabled")
	}
	if WorkerEnvKey(WorkerWebhookDispatcher) != "RETICORA_WORKER_WEBHOOK_DISPATCHER_ENABLED" {
		t.Errorf("switch name: %s", WorkerEnvKey(WorkerWebhookDispatcher))
	}

	t.Setenv(WorkerEnvKey(WorkerExport), "false")
	cfg = Load()
	for _, name := range WorkerNames {
		if cfg.WorkerEnabled(name) != (name != WorkerExport) {
			t.Errorf("%s: enabled %v with only export switched off", name, cfg.WorkerEnabled(name))
		}
	}

	t.Setenv(WorkerEnvKey(WorkerIngest), "maybe")
	if err := Load().Validate(false); err == nil || !strings.Contains(err.Error(), WorkerEnvKey(WorkerIngest)) {
		t.Errorf("invalid switch: %v", err)
	}
}

// TestDefaultOrgAdminRoleRequiresAdminClient covers WP-367 (AUT-09, E-65
// option 1): outside development the default role org_admin needs the
// Keycloak admin client that binds a new org_admin to MFA; the start check
// names both settings. With the client, with another default role and in
// development the configuration starts.
func TestDefaultOrgAdminRoleRequiresAdminClient(t *testing.T) {
	clearEnv(t)
	t.Setenv("RETICORA_KEYCLOAK_ADMIN_CLIENT_ID", "")
	t.Setenv("RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET", "")
	t.Setenv("RETICORA_DEFAULT_PROVISION_ROLE", "org_admin")
	if err := Load().Validate(false); err != nil {
		t.Errorf("development with default role org_admin without admin client: %v", err)
	}
	for _, env := range []string{EnvStaging, EnvProduction} {
		setAll(t, productionEnv)
		t.Setenv("RETICORA_ENVIRONMENT", env)
		err := Load().Validate(false)
		if err == nil || !strings.Contains(err.Error(), "RETICORA_DEFAULT_PROVISION_ROLE") || !strings.Contains(err.Error(), "RETICORA_KEYCLOAK_ADMIN_CLIENT_ID") {
			t.Errorf("%s with default role org_admin without admin client: %v, want an error naming both settings", env, err)
		}
	}
	t.Setenv("RETICORA_KEYCLOAK_ADMIN_CLIENT_ID", "reticora-admin-client")
	if err := Load().Validate(false); err == nil || !strings.Contains(err.Error(), "RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET") {
		t.Errorf("admin client without secret: %v, want an error", err)
	}
	t.Setenv("RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET", "secret")
	if err := Load().Validate(false); err != nil {
		t.Errorf("default role org_admin with admin client: %v", err)
	}
	t.Setenv("RETICORA_KEYCLOAK_ADMIN_CLIENT_ID", "")
	t.Setenv("RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET", "")
	for _, role := range []string{"viewer", ""} {
		t.Setenv("RETICORA_DEFAULT_PROVISION_ROLE", role)
		if err := Load().Validate(false); err != nil {
			t.Errorf("default role %q without admin client: %v", role, err)
		}
	}
}

// TestDevAuthOnlyInDevelopment covers WP-110 (AUT-03): the start check
// refuses RETICORA_ALLOW_INSECURE_DEV_AUTH outside development.
func TestDevAuthOnlyInDevelopment(t *testing.T) {
	clearEnv(t)
	t.Setenv("RETICORA_ALLOW_INSECURE_DEV_AUTH", "true")
	if err := Load().Validate(false); err != nil {
		t.Errorf("development with the dev login: %v", err)
	}
	for _, env := range []string{EnvStaging, EnvProduction} {
		setAll(t, productionEnv)
		t.Setenv("RETICORA_ENVIRONMENT", env)
		err := Load().Validate(false)
		if err == nil || !strings.Contains(err.Error(), "RETICORA_ALLOW_INSECURE_DEV_AUTH") {
			t.Errorf("%s with the dev login: %v", env, err)
		}
	}
	t.Setenv("RETICORA_ALLOW_INSECURE_DEV_AUTH", "false")
	if err := Load().Validate(false); err != nil {
		t.Errorf("production without the dev login: %v", err)
	}
}
