package httpx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
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

// leakConfig is a struct value as a log call hands it over: a secret in a
// field, in a nested map, in an unexported field and in a key=value string.
type leakConfig struct {
	Host     string            `json:"host"`
	Password string            `json:"password"`
	APIKey   string            `json:"api_key"`
	Options  map[string]string `json:"options"`
	DSN      string            `json:"dsn"`
	token    string
}

// TestRedactingHandlerRedactsMapsStructsAndKeyValues covers F-202 (SEC-01,
// WP-569): map and struct attributes are redacted recursively by their
// secret-named keys and fields, strings with password=, token=, secret= or
// "authorization:" are masked, in the JSON and the text handler.
func TestRedactingHandlerRedactsMapsStructsAndKeyValues(t *testing.T) {
	leaks := []any{
		"map", map[string]any{"user": "alice", "password": "MAPLEAK-1",
			"nested": map[string]string{"client_secret": "MAPLEAK-2", "region": "eu"},
			"list":   []map[string]string{{"access_token": "MAPLEAK-3"}}},
		"struct", leakConfig{Host: "db.internal", Password: "STRUCTLEAK-1", APIKey: "STRUCTLEAK-2",
			Options: map[string]string{"Authorization": "Bearer STRUCTLEAK-3", "mode": "fast"},
			DSN:     "host=db user=app password=STRUCTLEAK-4 sslmode=require", token: "STRUCTLEAK-5"},
		"struct_ptr", &leakConfig{Host: "db2.internal", Password: "STRUCTLEAK-6"},
		"query", "GET /cb?code=1&token=KVLEAK-1&state=x",
		"line", `retry password=KVLEAK-2 secret="KVLEAK-3" user=bob api-key=KVLEAK-4`,
		"header", "Authorization: Bearer KVLEAK-5",
		"json", `{"authorization":"Basic KVLEAK-6","user":"carol"}`,
	}
	secrets := []string{"MAPLEAK-1", "MAPLEAK-2", "MAPLEAK-3", "STRUCTLEAK-1", "STRUCTLEAK-2", "STRUCTLEAK-3",
		"STRUCTLEAK-4", "STRUCTLEAK-5", "STRUCTLEAK-6", "KVLEAK-1", "KVLEAK-2", "KVLEAK-3", "KVLEAK-4", "KVLEAK-5",
		"KVLEAK-6", "KVLEAK-7"}
	kept := []string{"alice", "eu", "db.internal", "db2.internal", "fast", "host=db user=app", "sslmode=require",
		"code=1", "state=x", "user=bob", "carol", "user=dave", "Authorization: " + RedactedValue}

	for name, newHandler := range logHandlers {
		var buf bytes.Buffer
		logger := slog.New(NewRedactingHandler(newHandler(&buf)))
		logger.Info("login failed for user=dave token=KVLEAK-7", leaks...)
		out := buf.String()
		for _, secret := range secrets {
			if strings.Contains(out, secret) {
				t.Errorf("%s: log output contains %q: %s", name, secret, out)
			}
		}
		for _, k := range kept {
			if !strings.Contains(out, k) {
				t.Errorf("%s: log output lost %q: %s", name, k, out)
			}
		}
	}
}

// logHandlers are the two handlers the server wraps (cmd/server).
var logHandlers = map[string]func(*bytes.Buffer) slog.Handler{
	"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
}

// harmlessConfig has no secret-named field and no secret string.
type harmlessConfig struct {
	Host    string            `json:"host"`
	Port    int               `json:"port"`
	Labels  map[string]string `json:"labels"`
	Started time.Time         `json:"started"`
}

// TestRedactingHandlerKeepsHarmlessValues covers the other half of F-202:
// maps, structs and strings without secrets are logged exactly as without
// the redaction, in the JSON and the text handler.
func TestRedactingHandlerKeepsHarmlessValues(t *testing.T) {
	args := []any{
		"map", map[string]any{"user": "alice", "count": 3, "tags": []string{"a", "b"}},
		"struct", harmlessConfig{Host: "db", Port: 5432, Labels: map[string]string{"env": "prod"},
			Started: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)},
		"ptr", &harmlessConfig{Host: "cache"},
		"line", "user=bob mode=fast url=https://example.com/a?b=c",
		"note", "verify token: expired; key rotation pending",
		"error", errors.New("dial tcp 10.0.0.1:5432: connection refused"),
		"payload", []byte("raw"),
	}
	for name, newHandler := range logHandlers {
		var plain, redacted bytes.Buffer
		record := func(h slog.Handler) {
			r := slog.NewRecord(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), slog.LevelInfo, "harmless user=eve", 0)
			r.Add(args...)
			if err := h.Handle(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		}
		record(newHandler(&plain))
		record(NewRedactingHandler(newHandler(&redacted)))
		if plain.String() != redacted.String() {
			t.Errorf("%s: harmless values changed:\nwant %s\ngot  %s", name, plain.String(), redacted.String())
		}
	}
}
