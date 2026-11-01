package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// Unique attributes in the ingest (MET-15, WP-142, F-432). A manual write of
// a value of a unique attribute another active CI holds is refused with 409
// (ci.CheckUnique); the ingest cannot answer a person with it. As with a
// value the field metadata rejects (CI-03, dropInvalidObserved) and an
// identity value another CI holds (withoutTakenIdentity), the value is not
// stored: it is counted in invalid_values and logged with the attribute's
// key, the rest of the record is stored, and the other records of the batch
// go on. The check is the one of ci.Service, also while the unique index
// does not exist yet (DB-04); the index guards concurrent writes.

// CITypeUniqueLookup resolves the type names of a batch like
// CITypeBatchLookup and, in the same statement, the keys of the unique
// attributes of each type, global ones included (NFR-03: no statement per
// record for an organization without unique attributes). The discovery PG
// repository implements it.
type CITypeUniqueLookup interface {
	LookupCITypeIDsWithUnique(ctx context.Context, orgID string, namesOrKeys []string) (types map[string]string, unique map[string][]string, err error)
}

// lookupFrameTypes resolves the type names of a frame with the keys of the
// unique attributes per type id where the repository can (CITypeUniqueLookup),
// else as lookupTypes with unique nil.
func (h *Handler) lookupFrameTypes(ctx context.Context, orgID string, names []string) (types map[string]string, unique map[string][]string, err error) {
	if lookup, ok := h.repo.(CITypeUniqueLookup); ok {
		if types, unique, err = lookup.LookupCITypeIDsWithUnique(ctx, orgID, names); err != nil {
			return nil, nil, fmt.Errorf("lookup ci types: %w", err)
		}
		return types, unique, nil
	}
	types, err = h.lookupTypes(ctx, orgID, names)
	return types, nil, err
}

// mayHoldUnique reports whether values may carry a value of a unique
// attribute of CI type typeID: false only when the frame read the unique
// attributes of the type and values carries none of them.
func (f *batchFrame) mayHoldUnique(typeID string, values map[string]any) bool {
	keys, known := f.uniqueKeys[typeID]
	if !known {
		return len(values) > 0
	}
	for _, key := range keys {
		if v, ok := values[key]; ok && v != nil {
			return true
		}
	}
	return false
}

// takenUnique returns the keys of the attributes in values whose value
// another active CI holds in the attribute's scope (ci.CheckUnique), for CI
// ciID ("" for a new one) of type typeID and client clientID, and logs each.
func (h *Handler) takenUnique(ctx context.Context, frame *batchFrame, orgID, typeID, clientID, ciID string, values map[string]any) ([]string, error) {
	if !frame.mayHoldUnique(typeID, values) {
		return nil, nil
	}
	err := ci.CheckUnique(ctx, h.ciRepo, orgID, typeID, clientID, ciID, values)
	var taken *ci.UniqueError
	if !errors.As(err, &taken) {
		return nil, err
	}
	for _, key := range taken.Fields {
		slog.WarnContext(ctx, "discovery value not stored: another CI holds this value of the unique attribute",
			"event", "ingest.unique.dropped", "organization_id", orgID, "ci_type_id", typeID, "field", key)
	}
	return taken.Fields, nil
}

// withoutTakenUnique removes from a new device the values of its unique
// attributes another active CI holds and returns how many it removed. The
// attribute map is replaced, not changed: callers share it.
func (h *Handler) withoutTakenUnique(ctx context.Context, frame *batchFrame, orgID, typeID string, item *IngestItem) (int, error) {
	taken, err := h.takenUnique(ctx, frame, orgID, typeID, item.ClientID, "", discoveredAttributes(item))
	if err != nil || len(taken) == 0 {
		return 0, err
	}
	item.Attributes = maps.Clone(item.Attributes)
	for _, key := range taken {
		switch key {
		case "fingerprint":
			item.Fingerprint = nil
		case "raw_data":
			item.RawData = nil
		default:
			delete(item.Attributes, key)
		}
	}
	return len(taken), nil
}

// withoutTakenUniqueWrites removes from the discovered attributes and the
// writes of known CI matched the values of unique attributes another active
// CI holds and returns how many it removed. Only values that differ from
// the CI's are checked: the CI keeps its own.
func (h *Handler) withoutTakenUniqueWrites(ctx context.Context, frame *batchFrame, orgID string, matched *ci.Item,
	attributes, writes map[string]any) (int, error) {
	changed := map[string]any{}
	for _, values := range []map[string]any{attributes, writes} {
		for key, v := range values {
			if current, ok := matched.Attributes[key]; !ok || !reflect.DeepEqual(current, v) {
				changed[key] = v
			}
		}
	}
	if len(changed) == 0 {
		return 0, nil
	}
	taken, err := h.takenUnique(ctx, frame, orgID, matched.CITypeID, matched.ClientID, matched.ID, changed)
	for _, key := range taken {
		delete(attributes, key)
		delete(writes, key)
	}
	return len(taken), err
}
