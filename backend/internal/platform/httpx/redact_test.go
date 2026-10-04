package httpx

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestRedactingHandlerRemovesSecrets covers WP-047 (SEC-01): secret-named
// attributes, also inside groups and on derived loggers, and passwords of
// connection URLs in values and messages never reach the log output.
func TestRedactingHandlerRemovesSecrets(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(&buf, nil)))

	logger.With("client_secret", "cs-42").WithGroup("req").Info("connect postgres://app:pw-1@db:5432/x failed",
		"password", "pw-2",
		"Authorization", "Bearer tok-3",
		"X-API-Key", "key-4",
		slog.Group("oidc", slog.String("refresh_token", "rt-5"), slog.String("issuer", "https://idp")),
		"url", "postgres://app:pw-6@db/x?sslmode=disable",
		"error", errors.New("dial redis://:pw-7@cache:6379 refused"),
		"user", "alice",
	)
	out := buf.String()
	for _, secret := range []string{"cs-42", "pw-1", "pw-2", "tok-3", "key-4", "rt-5", "pw-6", "pw-7"} {
		if strings.Contains(out, secret) {
			t.Errorf("log output contains %q: %s", secret, out)
		}
	}
	for _, kept := range []string{"alice", "https://idp", "app:***@db", RedactedValue} {
		if !strings.Contains(out, kept) {
			t.Errorf("log output lost %q: %s", kept, out)
		}
	}
}

func TestIsSecretKey(t *testing.T) {
	for key, want := range map[string]bool{
		"password": true, "DB_PASSWORD": true, "api-key": true, "X-Api-Key": true,
		"access_token": true, "session_key": true, "user": false, "url": false, "org_id": false,
	} {
		if got := IsSecretKey(key); got != want {
			t.Errorf("IsSecretKey(%q) = %v, want %v", key, got, want)
		}
	}
}
