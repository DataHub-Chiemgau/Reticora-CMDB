package pii

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
)

// RefKey marks a surrogate reference in stored JSON: {"$pii": "<id>"}
// stands for the value of a personal field (SEC-05). An empty id is a
// value that was redacted because no vault was configured.
const RefKey = "$pii"

// KindValue is a subject holding one value of a personal field written to
// the history (audit, ci_change, events).
const KindValue = "value"

// personalKeys are the field names whose string values are personal data:
// they are stored as surrogate references in audit changes, the CI change
// history and event data (SEC-05, AUD-04).
var personalKeys = map[string]bool{
	"email": true, "phone": true, "mobile": true, "display_name": true,
	"first_name": true, "last_name": true, "full_name": true,
	"contact_name": true, "contact_email": true, "contact_phone": true,
	"invitee_email": true, "user_email": true, "user_name": true,
	"signature": true, "checkout_signature": true, "return_signature": true,
}

// IsPersonalKey reports whether a field name holds personal data.
func IsPersonalKey(key string) bool { return personalKeys[strings.ToLower(key)] }

var defaultVault atomic.Pointer[Vault]

// SetDefault sets the vault of the history writers (audit, CI changes,
// events); nil leaves them redacting personal values instead.
func SetDefault(v *Vault) { defaultVault.Store(v) }

// Default is the vault of the history writers; nil when none is set.
func Default() *Vault { return defaultVault.Load() }

type anchorKey struct{}

// WithAnchor names the subject the personal values written in ctx belong
// to (a contact, a user): deleting it deletes their values in the history
// too (crypto-shredding, P3).
func WithAnchor(ctx context.Context, subjectID string) context.Context {
	return context.WithValue(ctx, anchorKey{}, subjectID)
}

func anchorFrom(ctx context.Context) string {
	s, _ := ctx.Value(anchorKey{}).(string)
	return s
}

// Generic turns v into its JSON form (maps, slices, strings, numbers).
func Generic(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		return nil, uerr
	}
	return out, nil
}

// SealTx replaces the personal values in data (in its JSON form) with
// surrogate references to new value subjects of the default vault, inside
// the writer's tenant transaction; without a vault the values are
// redacted. It returns the JSON form and whether anything was replaced.
func SealTx(ctx context.Context, tx pgx.Tx, orgID string, data any) (sealed any, changed bool, err error) {
	generic, err := Generic(data)
	if err != nil {
		return nil, false, fmt.Errorf("pii: encode: %w", err)
	}
	s := &sealer{ctx: ctx, tx: tx, orgID: orgID, vault: Default(), anchor: anchorFrom(ctx)}
	sealed, err = s.walk(generic, false)
	return sealed, s.changed, err
}

// SealFieldTx seals a historized field value: the whole value of a
// personal field (attributes.email), the personal keys inside a structured
// one (the CI of a create).
func SealFieldTx(ctx context.Context, tx pgx.Tx, orgID, fieldName string, value any) (sealed any, changed bool, err error) {
	if value == nil {
		return nil, false, nil
	}
	leaf := fieldName[strings.LastIndex(fieldName, ".")+1:]
	if str, ok := value.(string); ok && str != "" && IsPersonalKey(leaf) {
		ref, serr := SealValueTx(ctx, tx, orgID, str)
		return ref, serr == nil, serr
	}
	return SealTx(ctx, tx, orgID, value)
}

// PersonalKeys lists the personal field names, sorted.
func PersonalKeys() []string {
	keys := make([]string, 0, len(personalKeys))
	for k := range personalKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// KeyPattern is a POSIX regular expression that finds stored JSON with a
// personal key and a string value: the candidates of the backfill.
func KeyPattern() string {
	return `"(` + strings.Join(PersonalKeys(), "|") + `)": "`
}

// SealValueTx stores one personal value and returns its reference (the
// CI change history keeps a value per field).
func SealValueTx(ctx context.Context, tx pgx.Tx, orgID, value string) (map[string]any, error) {
	s := &sealer{ctx: ctx, tx: tx, orgID: orgID, vault: Default(), anchor: anchorFrom(ctx)}
	ref, err := s.seal(value)
	if err != nil {
		return nil, err
	}
	return ref, nil
}

type sealer struct {
	ctx     context.Context
	tx      pgx.Tx
	orgID   string
	vault   *Vault
	anchor  string
	changed bool
}

func (s *sealer) walk(v any, personal bool) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		if _, isRef := t[RefKey]; isRef && len(t) == 1 {
			return t, nil
		}
		for k, child := range t {
			out, err := s.walk(child, IsPersonalKey(k))
			if err != nil {
				return nil, err
			}
			t[k] = out
		}
		return t, nil
	case []any:
		for i, child := range t {
			out, err := s.walk(child, personal)
			if err != nil {
				return nil, err
			}
			t[i] = out
		}
		return t, nil
	case string:
		if !personal || t == "" {
			return t, nil
		}
		return s.seal(t)
	}
	return v, nil
}

func (s *sealer) seal(value string) (map[string]any, error) {
	s.changed = true
	if s.vault == nil {
		return map[string]any{RefKey: ""}, nil
	}
	id, err := s.vault.createTx(s.ctx, s.tx, s.orgID, KindValue, Data{"v": value}, s.anchor)
	if err != nil {
		return nil, err
	}
	return map[string]any{RefKey: id}, nil
}

// RevealTx replaces the surrogate references in data (a JSON form) with
// the values they stand for, for display and the API; a reference whose
// subject was deleted or redacted becomes null.
func RevealTx(ctx context.Context, tx pgx.Tx, orgID string, data any) (any, error) {
	var ids []string
	collectRefs(data, &ids)
	if len(ids) == 0 {
		return data, nil
	}
	subjects := map[string]*Subject{}
	if v := Default(); v != nil {
		var err error
		if subjects, err = v.ResolveTx(ctx, tx, orgID, ids); err != nil {
			return nil, err
		}
	}
	return replaceRefs(data, subjects), nil
}

// RefID returns the subject id of a reference and whether v is one.
func RefID(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	id, ok := m[RefKey].(string)
	return id, ok
}

func collectRefs(v any, ids *[]string) {
	if id, ok := RefID(v); ok {
		if id != "" {
			*ids = append(*ids, id)
		}
		return
	}
	switch t := v.(type) {
	case map[string]any:
		for _, child := range t {
			collectRefs(child, ids)
		}
	case []any:
		for _, child := range t {
			collectRefs(child, ids)
		}
	}
}

func replaceRefs(v any, subjects map[string]*Subject) any {
	if id, ok := RefID(v); ok {
		if s := subjects[id]; s != nil {
			return s.Data["v"]
		}
		return nil
	}
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			t[k] = replaceRefs(child, subjects)
		}
	case []any:
		for i, child := range t {
			t[i] = replaceRefs(child, subjects)
		}
	}
	return v
}
