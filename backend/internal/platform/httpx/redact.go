package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// RedactedValue replaces secret attribute values in log records.
const RedactedValue = "[REDACTED]"

// secretKeyParts are substrings of attribute keys whose values are secrets
// (SEC-01: no secrets in logs). Keys are compared lower-cased with "-"
// normalized to "_".
var secretKeyParts = []string{
	"password", "passwd", "secret", "token", "authorization", "api_key", "apikey",
	"private_key", "cookie", "master_key", "session_key",
}

// RedactingHandler wraps a slog.Handler and removes secrets before records
// are written: values of secret-named attributes are replaced, and the
// password of URLs (connection strings) in any string value is masked. It is
// the central logger of the server (cmd/server).
type RedactingHandler struct {
	next slog.Handler
}

// NewRedactingHandler returns a handler that redacts secrets for next.
func NewRedactingHandler(next slog.Handler) *RedactingHandler {
	return &RedactingHandler{next: next}
}

// Enabled reports whether next handles the level.
func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle redacts the record's attributes and passes it on.
func (h *RedactingHandler) Handle(ctx context.Context, record slog.Record) error { //nolint:gocritic // signature of slog.Handler
	clean := slog.NewRecord(record.Time, record.Level, redactString(record.Message), record.PC)
	record.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, clean)
}

// WithAttrs redacts attributes bound to a derived logger.
func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		clean = append(clean, redactAttr(a))
	}
	return &RedactingHandler{next: h.next.WithAttrs(clean)}
}

// WithGroup opens a group on the wrapped handler.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{next: h.next.WithGroup(name)}
}

// IsSecretKey reports whether an attribute key names a secret.
func IsSecretKey(key string) bool {
	k := strings.ReplaceAll(strings.ToLower(key), "-", "_")
	for _, part := range secretKeyParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

func redactAttr(a slog.Attr) slog.Attr {
	value := a.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		group := value.Group()
		clean := make([]slog.Attr, 0, len(group))
		for _, member := range group {
			clean = append(clean, redactAttr(member))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	}
	if IsSecretKey(a.Key) {
		return slog.String(a.Key, RedactedValue)
	}
	switch value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, redactString(value.String()))
	case slog.KindAny:
		// Errors and Stringers are rendered as text; redact that text when
		// it carries a connection URL with a password.
		var text string
		switch v := value.Any().(type) {
		case error:
			text = v.Error()
		case fmt.Stringer:
			text = v.String()
		default:
			return slog.Attr{Key: a.Key, Value: value}
		}
		if clean := redactString(text); clean != text {
			return slog.String(a.Key, clean)
		}
	}
	return slog.Attr{Key: a.Key, Value: value}
}

// redactString masks the password of every URL with userinfo in s, e.g. a
// connection string in an error message.
func redactString(s string) string {
	if !strings.Contains(s, "://") || !strings.Contains(s, "@") {
		return s
	}
	fields := strings.Fields(s)
	changed := false
	for i, field := range fields {
		trimmed := strings.Trim(field, `"'(),;`)
		u, err := url.Parse(trimmed)
		if err != nil || u.User == nil {
			continue
		}
		if _, hasPassword := u.User.Password(); !hasPassword {
			continue
		}
		u.User = url.UserPassword(u.User.Username(), "***")
		fields[i] = strings.Replace(field, trimmed, strings.ReplaceAll(u.String(), "%2A%2A%2A", "***"), 1)
		changed = true
	}
	if !changed {
		return s
	}
	return strings.Join(fields, " ")
}
