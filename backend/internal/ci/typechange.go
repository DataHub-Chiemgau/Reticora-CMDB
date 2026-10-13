package ci

import (
	"context"
	"errors"
	"sort"
)

// Reclassification of CIs (CI-11): discovery may raise generic_device to a
// specific type, never the other way round and never between specific
// types; every other type change is manual. A change records ci_change
// type_change, the audit entry ci.type_changed and the event ci.changed
// (changed field ci_type_id), checks the required attributes of the target
// type as findings instead of blocking, and keeps relationships, mounts,
// the asset link and the history (the CI row stays the same).

// Modes of a type change.
const (
	TypeChangeManual    = "manual"
	TypeChangeDiscovery = "discovery"
)

// GenericDeviceType is the key of the type of unclassified devices.
const GenericDeviceType = "generic_device"

// ErrReclassificationRefused: discovery may only raise generic_device to a
// specific type (CI-11).
var ErrReclassificationRefused = errors.New("discovery may only raise generic_device to a specific type")

// ErrUnknownType: the target type does not exist for the organization.
var ErrUnknownType = errors.New("unknown CI type")

// TypeFinding is a required attribute of the target type the CI does not
// carry; it is reported, the change is not blocked.
type TypeFinding struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// TypeChangeResult is the outcome of a type change.
type TypeChangeResult struct {
	CI       *Item         `json:"ci"`
	Findings []TypeFinding `json:"findings"`
	// Changed is false when the CI already had the type.
	Changed bool `json:"changed"`
}

// TypeChanger changes the type of a CI (CI-11).
type TypeChanger interface {
	ChangeType(ctx context.Context, orgID, ciID, typeID, mode string) (*TypeChangeResult, error)
}

// DiscoveryMayReclassify reports whether discovery may change a CI of type
// fromKey to toKey: only generic_device to a specific type.
func DiscoveryMayReclassify(fromKey, toKey string) bool {
	return fromKey == GenericDeviceType && toKey != "" && toKey != GenericDeviceType
}

// MissingRequired returns a finding for every required attribute key that
// attrs does not carry (nil or empty string count as missing).
func MissingRequired(required []string, attrs map[string]any) []TypeFinding {
	findings := []TypeFinding{}
	keys := append([]string(nil), required...)
	sort.Strings(keys)
	for _, key := range keys {
		v, ok := attrs[key]
		if s, isString := v.(string); ok && v != nil && (!isString || s != "") {
			continue
		}
		findings = append(findings, TypeFinding{Field: key, Message: "required by the new type and not set"})
	}
	return findings
}

// ChangeType changes the type of a CI (manual mode).
func (s *Service) ChangeType(ctx context.Context, orgID, ciID, typeID string) (*TypeChangeResult, error) {
	changer, ok := s.repo.(TypeChanger)
	if !ok {
		return nil, errors.New("type change is not supported by this repository")
	}
	return changer.ChangeType(ctx, orgID, ciID, typeID, TypeChangeManual)
}
