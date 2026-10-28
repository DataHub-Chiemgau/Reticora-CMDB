package ci

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Reclassification of CIs (CI-11): discovery may raise generic_device to a
// specific type, never the other way round and never between specific
// types; every other type change is manual. A change records ci_change
// type_changed, the audit entry ci.type_changed and the event ci.changed
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

// Violations of ci_type_id (CI-01, TEN-09; F-144, F-016): a CI has an active
// system type or an active type of its organization, named by its ID. An
// unknown ID and a private type of another organization get the same answer,
// so the answer does not tell whether a foreign type exists.
const (
	ciTypeNotAnID     = "not the ID (UUID) of a CI type"
	ciTypeNotFound    = "CI type not found"
	ciTypeDeactivated = "the CI type is deactivated"
)

// ciTypeViolation is the 422 on ci_type_id with detail.
func ciTypeViolation(detail string) error {
	return &ValidationError{Violations: []Violation{{Field: "ci_type_id", Detail: detail}}}
}

// checkCITypeID refuses a ci_type_id that is no UUID, such as the key
// "server", before it reaches a query (422 instead of the 400 of the failed
// cast).
func checkCITypeID(id string) error {
	if !api.IsUUID(id) {
		return ciTypeViolation(ciTypeNotAnID)
	}
	return nil
}

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

// ChangeType changes the type of a CI (manual mode). The target is the
// writer's choice like the type of a new CI: an ID that is no UUID is
// refused here, the repository reads the type in the writing transaction.
func (s *Service) ChangeType(ctx context.Context, orgID, ciID, typeID string) (*TypeChangeResult, error) {
	changer, ok := s.repo.(TypeChanger)
	if !ok {
		return nil, errors.New("type change is not supported by this repository")
	}
	if err := checkCITypeID(typeID); err != nil {
		return nil, err
	}
	return changer.ChangeType(ctx, orgID, ciID, typeID, TypeChangeManual)
}

// RecordOrigin is the ingest record a change comes from (AUD-03): the
// collector that sent it, the time of the observation and the record id,
// which becomes the correlation id of the change.
type RecordOrigin struct {
	CollectorID string
	ObservedAt  time.Time
	RecordID    string
}

// RecordUpdate is everything one ingest record changes on a known CI: the
// field update and, with TypeID, the reclassification of a generic_device
// to that specific type (CI-11, discovery mode).
type RecordUpdate struct {
	Update UpdateRequest
	TypeID string
	Origin RecordOrigin
}

// RecordUpdateResult is the CI after a record update. Changed reports a
// historized change (a sighting that only moves last_seen_at has none),
// TypeChanged the reclassification.
type RecordUpdateResult struct {
	CI          *Item
	Changed     bool
	TypeChanged bool
}

// RecordUpdater is the transaction variant of the update for the ingest
// (AUD-03, CI-11): the field update and the reclassification of a record in
// one transaction, one change set with the record's collector, source,
// observation time and correlation id, and one event ci.changed, followed
// by ci.online when the record brings an unknown CI back to active (REC-09).
// A failure after the field update also rolls back the type change.
type RecordUpdater interface {
	UpdateRecord(ctx context.Context, orgID, id string, rec *RecordUpdate) (*RecordUpdateResult, error)
}

// UpdateRecord applies rec to CI id through repo: in one transaction where
// repo is a RecordUpdater, else as an update followed by the type change
// (TypeChanger), each with its own change set and event. A reclassification
// discovery may not make leaves the type as it is.
func UpdateRecord(ctx context.Context, repo Repository, orgID, id string, rec *RecordUpdate) (*RecordUpdateResult, error) {
	if updater, ok := repo.(RecordUpdater); ok {
		return updater.UpdateRecord(ctx, orgID, id, rec)
	}
	item, err := repo.Update(ctx, orgID, id, rec.Update)
	if err != nil {
		return nil, err
	}
	// Without the change set the update cannot tell a sighting from a change.
	result := &RecordUpdateResult{CI: item, Changed: true}
	changer, ok := repo.(TypeChanger)
	if rec.TypeID == "" || rec.TypeID == item.CITypeID || !ok {
		return result, nil
	}
	changed, err := changer.ChangeType(ctx, orgID, id, rec.TypeID, TypeChangeDiscovery)
	switch {
	case errors.Is(err, ErrReclassificationRefused), errors.Is(err, ErrUnknownType):
		return result, nil
	case err != nil:
		return nil, err
	}
	result.CI, result.TypeChanged = changed.CI, changed.Changed
	return result, nil
}
