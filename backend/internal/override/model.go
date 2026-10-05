// Package override implements per-field provenance and controlled manual
// overrides (spec §13). Every discovered field retains its source metadata;
// manual overrides are protected from silent discovery overwrites, and the
// effective value resolves through the tenant's source-priority policy.
package override

import (
	"encoding/json"
	"time"
)

func jsonMarshal(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// FieldValue carries the provenance of one CI field (spec §13).
type FieldValue struct {
	ID               string     `json:"id"`
	OrganizationID   string     `json:"organization_id"`
	CIID             string     `json:"ci_id"`
	FieldName        string     `json:"field_name"`
	DiscoveredValue  any        `json:"discovered_value,omitempty"`
	DiscoveredSource string     `json:"discovered_source,omitempty"`
	DiscoveredAt     *time.Time `json:"discovered_at,omitempty"`
	OverrideValue    any        `json:"override_value,omitempty"`
	OverrideAuthor   string     `json:"override_author,omitempty"`
	OverrideReason   string     `json:"override_reason,omitempty"`
	OverrideAt       *time.Time `json:"override_at,omitempty"`
	Protected        bool       `json:"protected"`
	// EffectiveValue resolves through the source-priority policy: a protected
	// override always wins; otherwise the highest-trust discovered value.
	EffectiveValue any `json:"effective_value,omitempty"`
	// Diverged is true when discovered and effective values differ — the UI
	// surfaces this as a conflict badge.
	Diverged  bool      `json:"diverged"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SetOverrideRequest is the payload for setting a manual override.
type SetOverrideRequest struct {
	Value     any    `json:"value"`
	Reason    string `json:"reason"`
	Protected *bool  `json:"protected,omitempty"`
}

// SourcePolicy is the per-organization source-priority policy.
type SourcePolicy struct {
	ID             string   `json:"id"`
	OrganizationID string   `json:"organization_id"`
	Name           string   `json:"name"`
	Priorities     []string `json:"priorities"`
	IsDefault      bool     `json:"is_default"`
}

// DefaultPriorities is the built-in source-priority ordering (highest first),
// mirroring the discovery sourceTrust ranking with manual_override on top.
var DefaultPriorities = []string{
	"manual_override", "ipmi", "redfish", "api", "agent",
	"wmi", "ssh", "snmp", "sweep", "manual", "import",
}

// ResolveEffective computes the effective value of a field: a manual
// override, protected or not, before the observed value. Manual ranks 100
// in REC-03 and stays 100 under a per-attribute policy (REC-04), so the
// policy cannot rank a source above it; the central write decision and the
// CI view use the same rule (OVR-01, CI-10).
func ResolveEffective(fv *FieldValue, _ []string) any {
	if fv.OverrideAt != nil || fv.OverrideValue != nil {
		return fv.OverrideValue
	}
	return fv.DiscoveredValue
}

// IsDiverged reports whether discovered and effective values differ.
func IsDiverged(fv *FieldValue, priorities []string) bool {
	if fv.DiscoveredValue == nil {
		return false
	}
	effective := ResolveEffective(fv, priorities)
	return !valuesEqual(fv.DiscoveredValue, effective)
}

func valuesEqual(a, b any) bool {
	return fmtSprint(a) == fmtSprint(b)
}

func fmtSprint(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		raw, err := jsonMarshal(v)
		if err != nil {
			return ""
		}
		return raw
	}
}
