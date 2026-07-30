package discovery

import (
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/wire"
)

// RelationshipSource marks derived topology edges as originating from discovery.
const RelationshipSource = "discovery"

// defaultConfidence is applied when a collector omits a confidence value.
const defaultConfidence = 0.5

// DerivedRelationship is a topology edge derived from discovery ingest data.
type DerivedRelationship struct {
	SourceCIID string  `json:"source_ci_id"`
	TargetCIID string  `json:"target_ci_id"`
	RelType    string  `json:"rel_type"`
	Confidence float64 `json:"confidence"`
}

// NeighborIndex resolves discovered neighbor identifiers to existing CI ids.
type NeighborIndex struct {
	byIP       map[string]string
	byMAC      map[string]string
	byHostname map[string]string
}

// BuildNeighborIndex indexes CIs by management IP, primary MAC, interface MACs,
// hostname and FQDN so neighbor references can be resolved to CI ids.
func BuildNeighborIndex(items []ci.Item) *NeighborIndex {
	idx := &NeighborIndex{
		byIP:       make(map[string]string),
		byMAC:      make(map[string]string),
		byHostname: make(map[string]string),
	}
	for _, item := range items {
		if key := normalizeKey(item.ManagementIP); key != "" {
			idx.byIP[key] = item.ID
		}
		if key := normalizeMAC(item.PrimaryMAC); key != "" {
			idx.byMAC[key] = item.ID
		}
		if key := normalizeKey(item.Hostname); key != "" {
			idx.byHostname[key] = item.ID
		}
		if key := normalizeKey(item.FQDN); key != "" {
			idx.byHostname[key] = item.ID
		}
	}
	return idx
}

// Resolve returns the CI id matching any of the supplied identifiers, preferring
// management IP, then MAC, then hostname.
func (idx *NeighborIndex) Resolve(ip, mac, hostname string) (string, bool) {
	if id, ok := idx.byIP[normalizeKey(ip)]; ok && ip != "" {
		return id, true
	}
	if id, ok := idx.byMAC[normalizeMAC(mac)]; ok && mac != "" {
		return id, true
	}
	if id, ok := idx.byHostname[normalizeKey(hostname)]; ok && hostname != "" {
		return id, true
	}
	return "", false
}

// DeriveRelationships resolves the neighbor identifiers on each discovered
// relationship to CI ids and returns the topology edges to upsert. resolve maps
// (ip, mac, hostname) to a CI id, returning ok=false when no CI matches.
// suppressed returns true for (source, target) pairs that must be skipped. Both
// callbacks may be nil. The result is deduplicated and free of self-loops.
func DeriveRelationships(
	sourceCIID string,
	rels []wire.IngestRelationship,
	resolve func(ip, mac, hostname string) (string, bool),
	suppressed func(sourceID, targetID string) bool,
) []DerivedRelationship {
	if sourceCIID == "" || resolve == nil {
		return nil
	}

	seen := make(map[string]struct{})
	var derived []DerivedRelationship
	for _, rel := range rels {
		targetID, ok := resolve(rel.TargetIP, rel.TargetMAC, rel.TargetHostname)
		if !ok || targetID == "" || targetID == sourceCIID {
			continue
		}
		relType := normalizeRelType(rel.TypeKey)
		if suppressed != nil && suppressed(sourceCIID, targetID) {
			continue
		}
		key := sourceCIID + "|" + targetID + "|" + relType
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		confidence := rel.Confidence
		if confidence == 0 {
			confidence = defaultConfidence
		}
		derived = append(derived, DerivedRelationship{
			SourceCIID: sourceCIID,
			TargetCIID: targetID,
			RelType:    relType,
			Confidence: confidence,
		})
	}
	return derived
}

// normalizeRelType maps collector-provided relationship keys onto the supported
// discovery topology relationship types.
func normalizeRelType(typeKey string) string {
	switch strings.ToLower(strings.TrimSpace(typeKey)) {
	case "powered_by", "powers", "power":
		return "powered_by"
	case "", "connected_to", "connected", "neighbor", "lldp", "cdp":
		return "connected_to"
	default:
		return "connected_to"
	}
}

func normalizeKey(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

func normalizeMAC(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.NewReplacer(":", "", "-", "", ".", "").Replace(value)
	return value
}

// deriveTopology resolves and upserts topology relationships for a bulk ingest
// batch, returning the number of relationships created.
func (h *Handler) deriveTopology(orgID string, items []IngestItem, resolvedCIID []string, existing []ci.Item) int {
	if h.relRepo == nil {
		return 0
	}

	index := BuildNeighborIndex(existing)
	suppressed := h.suppressionChecker(orgID)

	created := 0
	for i, item := range items {
		srcID := resolvedCIID[i]
		if srcID == "" || len(item.Relationships) == 0 {
			continue
		}
		for _, d := range DeriveRelationships(srcID, item.Relationships, index.Resolve, suppressed) {
			if h.upsertRelationship(orgID, d) {
				created++
			}
		}
	}
	return created
}

// suppressionChecker returns a predicate that reports whether a (source,target)
// pair is suppressed. Returns nil when the repository does not expose
// suppression data (e.g. the in-memory repository).
func (h *Handler) suppressionChecker(orgID string) func(sourceID, targetID string) bool {
	provider, ok := h.repo.(suppressionProvider)
	if !ok {
		return nil
	}
	pairs, err := provider.SuppressedPairs(orgID)
	if err != nil || len(pairs) == 0 {
		return nil
	}
	return func(sourceID, targetID string) bool {
		return pairs[sourceID+"|"+targetID] || pairs[targetID+"|"+sourceID]
	}
}

// suppressionProvider is optionally implemented by repositories that persist a
// relationship_suppression table.
type suppressionProvider interface {
	SuppressedPairs(orgID string) (map[string]bool, error)
}

// upsertRelationship creates a discovery-sourced relationship if an equivalent
// edge does not already exist. Returns true when a new edge was created.
func (h *Handler) upsertRelationship(orgID string, d DerivedRelationship) bool {
	existing, _, err := h.relRepo.List(orgID, d.SourceCIID, api.PaginationParams{Limit: 10000, Offset: 0})
	if err == nil {
		for _, rel := range existing {
			if rel.SourceCIID == d.SourceCIID && rel.TargetCIID == d.TargetCIID && rel.RelType == d.RelType {
				return false
			}
		}
	}
	rel := &relationship.Relationship{
		OrganizationID: orgID,
		SourceCIID:     d.SourceCIID,
		TargetCIID:     d.TargetCIID,
		RelType:        d.RelType,
		Source:         RelationshipSource,
		Attributes:     map[string]any{"confidence": d.Confidence},
	}
	if err := h.relRepo.Create(rel); err != nil {
		return false
	}
	return true
}
