package discovery

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/wire"
)

// RelationshipSource marks derived topology edges as originating from discovery.
const RelationshipSource = "discovery"

// Confidence of discovered edges (TOP-01): LLDP/CDP 1.0, FDB 0.6 (only
// where no LLDP/CDP neighbor exists); other reports without a value 0.5.
const (
	defaultConfidence = 0.5
	lldpConfidence    = 1.0
	fdbConfidence     = 0.6
	// trunkMACs: a port with more than this many FDB MACs is a trunk; its
	// MACs give no edges.
	trunkMACs = 4
)

// DerivedRelationship is a topology edge derived from discovery ingest data.
type DerivedRelationship struct {
	SourceCIID string  `json:"source_ci_id"`
	TargetCIID string  `json:"target_ci_id"`
	RelType    string  `json:"rel_type"`
	Confidence float64 `json:"confidence"`
	// Attributes describe the edge (outlet, feed, redundancy).
	Attributes map[string]any `json:"attributes,omitempty"`
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

// UnknownRelationship is a neighbor reported with a relationship key the
// topology does not know; it becomes a review item instead of an edge.
type UnknownRelationship struct {
	SourceCIID string
	TargetCIID string
	TypeKey    string
	Neighbor   wire.IngestRelationship
}

// neighborProtocol returns how a neighbor was seen: the reported protocol
// or a protocol named as type key.
func neighborProtocol(rel *wire.IngestRelationship) string {
	if p := strings.ToLower(strings.TrimSpace(rel.Protocol)); p != "" {
		return p
	}
	switch k := strings.ToLower(strings.TrimSpace(rel.TypeKey)); k {
	case "lldp", "cdp", "fdb", "arp":
		return k
	}
	return ""
}

// DeriveRelationships returns the topology edges of a device's neighbors;
// see DeriveTopology.
func DeriveRelationships(
	sourceCIID string,
	rels []wire.IngestRelationship,
	resolve func(ip, mac, hostname string) (string, bool),
	suppressed func(sourceID, targetID, relType string) bool,
) []DerivedRelationship {
	derived, _ := DeriveTopology(sourceCIID, rels, resolve, suppressed)
	return derived
}

// DeriveTopology resolves the neighbors of a device to CI ids and returns
// the edges to upsert and the neighbors with an unknown relationship key
// (TOP-01): LLDP/CDP neighbors are connected_to with confidence 1.0; FDB
// entries give connected_to with 0.6 only on ports without an LLDP/CDP
// neighbor and with at most four MACs (more is a trunk); ARP entries only
// enrich, they give no edge. resolve maps (ip, mac, hostname) to a CI id;
// suppressed skips (source, target, type) edges a user removed (REL-03).
// The result is deduplicated and
// free of self-loops.
func DeriveTopology(
	sourceCIID string,
	rels []wire.IngestRelationship,
	resolve func(ip, mac, hostname string) (string, bool),
	suppressed func(sourceID, targetID, relType string) bool,
) ([]DerivedRelationship, []UnknownRelationship) {
	if sourceCIID == "" || resolve == nil {
		return nil, nil
	}

	// Ports with an LLDP/CDP neighbor and the FDB MACs per port.
	lldpPorts := map[string]bool{}
	anyLLDP := false
	fdbMACs := map[string]map[string]bool{}
	for i := range rels {
		port := normalizeKey(rels[i].LocalInterface)
		switch neighborProtocol(&rels[i]) {
		case "lldp", "cdp":
			lldpPorts[port] = true
			anyLLDP = true
		case "fdb":
			if fdbMACs[port] == nil {
				fdbMACs[port] = map[string]bool{}
			}
			fdbMACs[port][normalizeMAC(rels[i].TargetMAC)] = true
		}
	}

	seen := make(map[string]struct{})
	var derived []DerivedRelationship
	var unknown []UnknownRelationship
	for i := range rels {
		rel := &rels[i]
		protocol := neighborProtocol(rel)
		port := normalizeKey(rel.LocalInterface)
		confidence := rel.Confidence
		switch protocol {
		case "arp":
			continue // enrichment only
		case "fdb":
			if lldpPorts[port] || (port == "" && anyLLDP) || len(fdbMACs[port]) > trunkMACs {
				continue
			}
			confidence = fdbConfidence
		case "lldp", "cdp":
			confidence = lldpConfidence
		}
		relType, known := normalizeRelType(rel.TypeKey)
		targetID, ok := resolve(rel.TargetIP, rel.TargetMAC, rel.TargetHostname)
		if !ok || targetID == "" || targetID == sourceCIID {
			continue
		}
		if suppressed != nil && known && suppressed(sourceCIID, targetID, relType) {
			continue
		}
		if !known {
			unknown = append(unknown, UnknownRelationship{SourceCIID: sourceCIID, TargetCIID: targetID, TypeKey: rel.TypeKey, Neighbor: *rel})
			continue
		}
		key := sourceCIID + "|" + targetID + "|" + relType
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
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
	return derived, unknown
}

// normalizeRelType maps collector-provided relationship keys onto the
// supported discovery topology relationship types; known is false for keys
// the topology does not know (TOP-01: they become a review, not an edge).
func normalizeRelType(typeKey string) (relType string, known bool) {
	switch strings.ToLower(strings.TrimSpace(typeKey)) {
	case "powered_by", "powers", "power":
		return "powered_by", true
	case "", "connected_to", "connected", "neighbor", "lldp", "cdp", "fdb":
		return "connected_to", true
	}
	return "", false
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
// batch, returning the number of relationships created. When the
// suppressions cannot be loaded no edge is written (fail-closed, REL-03).
func (h *Handler) deriveTopology(ctx context.Context, orgID string, items []IngestItem, resolvedCIID []string, existing []ci.Item) int {
	if h.relRepo == nil {
		return 0
	}

	index := BuildNeighborIndex(existing)
	suppressed, err := h.suppressionChecker(ctx, orgID)
	if err != nil {
		slog.Error("topology skipped: suppressions not loaded", "event", "discovery.topology.failed", "organization_id", orgID, "error", err)
		return 0
	}

	created := 0
	for i, item := range items {
		srcID := resolvedCIID[i]
		if srcID == "" || len(item.Relationships) == 0 {
			continue
		}
		derived, unknown := DeriveTopology(srcID, item.Relationships, index.Resolve, suppressed)
		for _, d := range derived {
			if h.upsertRelationship(ctx, orgID, d) {
				created++
			}
		}
		for i := range unknown {
			h.reviewUnknownRelationship(ctx, orgID, &unknown[i])
		}
	}
	return created
}

// suppressionChecker returns a predicate that reports whether an edge
// (source, target, type) is suppressed, in either direction. It returns
// nil without suppression data (e.g. the in-memory repository) and an error
// when the suppressions cannot be loaded.
func (h *Handler) suppressionChecker(ctx context.Context, orgID string) (func(sourceID, targetID, relType string) bool, error) {
	provider, ok := h.repo.(suppressionProvider)
	if !ok {
		return nil, nil
	}
	edges, err := provider.SuppressedPairs(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if len(edges) == 0 {
		return nil, nil
	}
	return func(sourceID, targetID, relType string) bool {
		return edges[sourceID+"|"+targetID+"|"+relType] || edges[targetID+"|"+sourceID+"|"+relType]
	}, nil
}

// suppressionProvider is optionally implemented by repositories that persist a
// relationship_suppression table; the keys are source|target|type.
type suppressionProvider interface {
	SuppressedPairs(ctx context.Context, orgID string) (map[string]bool, error)
}

// edgeFinder looks an edge up by its endpoints and type (PostgreSQL); other
// repositories list the edges of the source.
type edgeFinder interface {
	DiscoveryEdge(ctx context.Context, orgID, sourceID, targetID, relType string) (id, source string, found bool, err error)
}

// upsertRelationship creates a discovery edge, or re-confirms an existing
// one (last_seen_at, last_confirmed_at, confidence; TOP-02). An edge of
// another source between the same CIs stays untouched (source policy,
// REL-08). It reports whether a new edge was created.
func (h *Handler) upsertRelationship(ctx context.Context, orgID string, d DerivedRelationship) bool {
	confidence := d.Confidence
	if finder, ok := h.repo.(edgeFinder); ok {
		id, source, found, err := finder.DiscoveryEdge(ctx, orgID, d.SourceCIID, d.TargetCIID, d.RelType)
		if err == nil && found {
			if confirmer, ok := h.relRepo.(relationship.Confirmer); ok && relationship.MayChange(RelationshipSource, source) {
				if cerr := confirmer.Confirm(ctx, orgID, id, &confidence); cerr != nil {
					slog.Warn("re-confirm discovery edge", "relationship_id", id, "error", cerr)
				}
			}
			return false
		}
	} else if existing, _, err := h.relRepo.List(ctx, orgID, d.SourceCIID, api.PaginationParams{Limit: 10000, Offset: 0}); err == nil {
		for i := range existing {
			rel := &existing[i]
			if rel.SourceCIID != d.SourceCIID || rel.TargetCIID != d.TargetCIID || rel.RelType != d.RelType {
				continue
			}
			if confirmer, ok := h.relRepo.(relationship.Confirmer); ok && relationship.MayChange(RelationshipSource, rel.Source) {
				if cerr := confirmer.Confirm(ctx, orgID, rel.ID, &confidence); cerr != nil {
					slog.Warn("re-confirm discovery edge", "relationship_id", rel.ID, "error", cerr)
				}
			}
			return false
		}
	}
	rel := &relationship.Relationship{
		OrganizationID: orgID,
		SourceCIID:     d.SourceCIID,
		TargetCIID:     d.TargetCIID,
		RelType:        d.RelType,
		Source:         RelationshipSource,
		Attributes:     edgeAttributes(d.Attributes),
		Confidence:     &confidence,
	}
	if err := h.relRepo.Create(ctx, rel); err != nil {
		return false
	}
	return true
}

// ReviewKindUnknownRelationship is the review of a neighbor reported with
// an unknown relationship key (TOP-01).
const ReviewKindUnknownRelationship = reviews.KindUnknownRelationship

// reviewUnknownRelationship opens the review of an unknown key; a
// dismissed key of the pair stays dismissed.
func (h *Handler) reviewUnknownRelationship(ctx context.Context, orgID string, u *UnknownRelationship) {
	_, err := h.ReviewService().OpenUnlessDismissed(ctx, &ReviewItem{
		OrganizationID: orgID,
		Kind:           ReviewKindUnknownRelationship,
		Status:         ReviewStatusOpen,
		ObjectKey:      "rel:" + u.SourceCIID + ":" + u.TargetCIID,
		CandidateCIIDs: []string{u.SourceCIID, u.TargetCIID},
		Payload: map[string]any{
			"source_ci_id":    u.SourceCIID,
			"target_ci_id":    u.TargetCIID,
			"type_key":        u.TypeKey,
			"protocol":        u.Neighbor.Protocol,
			"local_interface": u.Neighbor.LocalInterface,
			"target_ip":       u.Neighbor.TargetIP,
			"target_mac":      u.Neighbor.TargetMAC,
		},
	}, "type_key")
	if err != nil {
		slog.Error("open unknown_relationship review", "organization_id", orgID, "error", err)
	}
}

// resolveUnknownRelationship: accept records the neighbor as connected_to
// (confidence 0.5); dismiss keeps the edge away.
func (h *Handler) resolveUnknownRelationship(ctx context.Context, item *ReviewItem, res *Resolution) (bool, error) {
	if res.Action != reviews.ActionAccept || h.relRepo == nil {
		return false, nil
	}
	d := DerivedRelationship{SourceCIID: payloadString(item.Payload, "source_ci_id"), TargetCIID: payloadString(item.Payload, "target_ci_id"),
		RelType: "connected_to", Confidence: defaultConfidence}
	if d.SourceCIID == "" || d.TargetCIID == "" {
		return false, refused(http.StatusUnprocessableEntity, "the item names no CIs")
	}
	h.upsertRelationship(ctx, item.OrganizationID, d)
	res.Note = "recorded as connected_to"
	return false, nil
}

// edgeAttributes are the derived attributes plus the provenance.
func edgeAttributes(derived map[string]any) map[string]any {
	out := map[string]any{"provenance": relationship.Provenance("discovery", "system", RelationshipSource, time.Now())}
	for k, v := range derived {
		out[k] = v
	}
	return out
}
