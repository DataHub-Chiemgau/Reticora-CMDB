package httpx

import (
	"context"
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"regexp"
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
// are written: values of secret-named attributes are replaced, also the
// secret-named keys and fields of map and struct values; in every string
// value the password of URLs (connection strings) and the values of
// key=value pairs with a secret key and of "authorization:" are masked. It
// is the central logger of the server (cmd/server).
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
		// Errors and Stringers are rendered as text: that text is
		// redacted. Maps, structs, slices and pointers to them are walked:
		// their secret-named keys and fields and their strings are redacted
		// (F-202). A value without secrets is logged unchanged.
		if clean, changed := redactValue(reflect.ValueOf(value.Any()), 0); changed {
			return slog.Any(a.Key, clean)
		}
	}
	return slog.Attr{Key: a.Key, Value: value}
}

// maxRedactDepth bounds the walk of nested values; a deeper container is
// replaced as a whole, so a cycle or a deep secret never reaches the log.
const maxRedactDepth = 16

// redactValue walks v and reports whether it holds a secret: a map key or
// struct field with a secret name, or a string redactString changes. Only
// then is the returned value used: maps and structs become
// map[string]any (struct fields named as encoding/json names them, also
// unexported ones, which the text handler prints), slices []any, with
// secrets replaced and everything else as it was.
func redactValue(v reflect.Value, depth int) (any, bool) {
	if !v.IsValid() {
		return nil, false
	}
	if v.CanInterface() {
		switch x := v.Interface().(type) {
		case error:
			// Both handlers print an error as its text.
			text := x.Error()
			return redactString(text), redactString(text) != text
		case json.Marshaler, encoding.TextMarshaler:
			// The type renders itself (time.Time, netip.Addr, …); only a
			// text form it also offers is checked.
			if str, ok := x.(fmt.Stringer); ok {
				text := str.String()
				return redactString(text), redactString(text) != text
			}
			return nil, false
		case fmt.Stringer:
			// The text handler prints the text, the JSON handler the
			// fields: a secret in either is redacted.
			if text := x.String(); redactString(text) != text {
				return redactString(text), true
			}
		}
	}
	switch v.Kind() { //nolint:exhaustive // other kinds hold no secrets
	case reflect.String:
		if text := v.String(); redactString(text) != text {
			return redactString(text), true
		}
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, false
		}
		if depth >= maxRedactDepth {
			return RedactedValue, true
		}
		return redactValue(v.Elem(), depth+1)
	case reflect.Map:
		if depth >= maxRedactDepth {
			return RedactedValue, true
		}
		out := make(map[string]any, v.Len())
		changed := false
		iter := v.MapRange()
		for iter.Next() {
			key := fmt.Sprint(iter.Key())
			value, secret := redactMember(key, iter.Value(), depth)
			changed = changed || secret
			out[key] = value
		}
		if changed {
			return out, true
		}
	case reflect.Struct:
		if depth >= maxRedactDepth {
			return RedactedValue, true
		}
		out := make(map[string]any, v.NumField())
		changed := false
		for i := range v.NumField() {
			field := v.Type().Field(i)
			name, skip := fieldName(&field)
			if skip {
				continue
			}
			value, secret := redactMember(name, v.Field(i), depth)
			changed = changed || secret
			out[name] = value
		}
		if changed {
			return out, true
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return nil, false
		}
		if depth >= maxRedactDepth {
			return RedactedValue, true
		}
		out := make([]any, v.Len())
		changed := false
		for i := range v.Len() {
			value, secret := redactMember("", v.Index(i), depth)
			changed = changed || secret
			out[i] = value
		}
		if changed {
			return out, true
		}
	}
	return nil, false
}

// redactMember returns the shown value of a map entry, struct field or
// slice element and whether it held a secret.
func redactMember(key string, v reflect.Value, depth int) (any, bool) {
	if key != "" && IsSecretKey(key) {
		return RedactedValue, true
	}
	if clean, changed := redactValue(v, depth+1); changed {
		return clean, true
	}
	if v.CanInterface() {
		return v.Interface(), false
	}
	// An unexported field cannot be handed on; fmt prints what it holds.
	return fmt.Sprint(v), false
}

// fieldName names a struct field as encoding/json does; skip is true for
// a field encoding/json leaves out by its tag.
func fieldName(f *reflect.StructField) (name string, skip bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", true
	}
	if name, _, _ = strings.Cut(tag, ","); name != "" {
		return name, false
	}
	return f.Name, false
}

// secretPair finds key=value pairs, e.g. in a query string or a
// connection string: the value runs to the next blank, quote, "&", "," or
// ";". The key decides (IsSecretKey) whether the value is masked.
var secretPair = regexp.MustCompile(`([A-Za-z0-9_.-]+)=("[^"]*"|'[^']*'|[^\s"'&,;]*)`)

// authorizationValue finds an authorization header value with its scheme
// ("Authorization: Bearer x", "proxy-authorization=Basic y").
var authorizationValue = regexp.MustCompile(`(?i)(authorization"?\s*[:=]\s*"?)(?:(?:bearer|basic|digest|negotiate|token|apikey)\s+)?[^\s"',;]+`)

// redactString masks secrets in s: the values of authorization headers and
// of key=value pairs with a secret key (password=, token=, secret=, …) and
// the password of every URL with userinfo, e.g. a connection string in an
// error message.
func redactString(s string) string {
	if strings.Contains(s, "uthorization") || strings.Contains(s, "UTHORIZATION") {
		s = authorizationValue.ReplaceAllString(s, "${1}"+RedactedValue)
	}
	if strings.Contains(s, "=") {
		s = secretPair.ReplaceAllStringFunc(s, func(pair string) string {
			key, value, _ := strings.Cut(pair, "=")
			if value == "" || value == RedactedValue || !IsSecretKey(key) {
				return pair
			}
			return key + "=" + RedactedValue
		})
	}
	return redactURLPasswords(s)
}

// redactURLPasswords masks the password of every URL with userinfo in s.
func redactURLPasswords(s string) string {
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
