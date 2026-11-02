package fieldmeta

import (
	"context"
	"sync/atomic"
)

// Reference targets of MET-11/MET-12: the kinds of entity a reference-typed
// attribute (reference and the alias types user, team, location, ci_ref,
// asset_ref, contract_ref) or a file attribute (document) stores the id of.
const (
	TargetUser     = "user"
	TargetTeam     = "team"
	TargetLocation = "location"
	TargetCI       = "ci"
	TargetAsset    = "asset"
	TargetContract = "contract"
	// TargetDocument is the target of the type file; it is no valid
	// reference_target.
	TargetDocument = "document"
)

// Modules of the late features a reference target belongs to (WP-214,
// E-03); the keys are those of server.LateModules.
const (
	moduleInventory = "inventory"
	moduleDocuments = "documents"
)

// referenceTarget is one entry of the target registry.
type referenceTarget struct {
	// module is the late module the target needs; empty for a target of
	// the core (G1).
	module string
	// resolvable: the existence check (ReferenceLookup) knows the target.
	// contract stays known but unavailable until WP-269 registers it.
	resolvable bool
}

// referenceRegistry lists every reference target with its availability.
var referenceRegistry = map[string]referenceTarget{
	TargetUser:     {resolvable: true},
	TargetTeam:     {resolvable: true},
	TargetLocation: {resolvable: true},
	TargetCI:       {resolvable: true},
	TargetAsset:    {module: moduleInventory, resolvable: true},
	TargetContract: {},
	TargetDocument: {module: moduleDocuments, resolvable: true},
}

// ReferenceTargets lists the valid reference_target values of a
// reference-typed field (document, the target of file, is none).
var ReferenceTargets = map[string]bool{
	TargetUser: true, TargetTeam: true, TargetLocation: true,
	TargetCI: true, TargetAsset: true, TargetContract: true,
}

// aliasTargets are the targets the alias types name.
var aliasTargets = map[string]string{
	TypeUser: TargetUser, TypeTeam: TargetTeam, TypeLocation: TargetLocation,
	TypeCIRef: TargetCI, TypeAssetRef: TargetAsset, TypeContractRef: TargetContract,
}

// ReferenceTargetOf returns the target of a reference-typed or file
// definition, "" for any other type: an alias type names its target, file
// stores a document, reference without reference_target refers to a CI (as
// reticora_reference_target, migration 000107).
func ReferenceTargetOf(def *FieldDefinition) string {
	if def.DataType == TypeFile {
		return TargetDocument
	}
	if CanonicalType(def.DataType) != TypeReference {
		return ""
	}
	if target, ok := aliasTargets[def.DataType]; ok {
		return target
	}
	if def.ReferenceTarget == "" {
		return TargetCI
	}
	return def.ReferenceTarget
}

// moduleSwitch is the switch of the late modules set at startup; nil
// enables every module (development and tests, like a nil
// server.ModuleSet).
var moduleSwitch atomic.Pointer[func(module string) bool]

// SetReferenceModules sets which late modules are on (E-03, WP-214):
// cmd/server sets it once at startup from the ModuleSet of
// server.ParseModules. nil enables every module.
func SetReferenceModules(enabled func(module string) bool) {
	if enabled == nil {
		moduleSwitch.Store(nil)
		return
	}
	moduleSwitch.Store(&enabled)
}

// ReferenceTargetAvailable reports whether a target can be used: it is
// known, the existence check resolves it and its module is on.
func ReferenceTargetAvailable(target string) bool {
	entry, ok := referenceRegistry[target]
	if !ok || !entry.resolvable {
		return false
	}
	if entry.module == "" {
		return true
	}
	enabled := moduleSwitch.Load()
	return enabled == nil || (*enabled)(entry.module)
}

// ReferenceLookup is the existence check of reference values (MET-12): for
// one target, one query for all ids of a write. It returns the ids among
// ids the caller sees (row level security of its scope) and that count as
// present, each with its client_id ("" for an org-wide target). An id that
// is not returned does not exist for the caller.
type ReferenceLookup interface {
	LookupReferences(ctx context.Context, orgID, target string, ids []string) (map[string]string, error)
}
