package discovery

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/wire"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
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
	// Refresh are the attributes a re-confirmation of the stored edge
	// sets; a nil value removes one (WP-498). Nil leaves them unchanged.
	Refresh map[string]any `json:"-"`
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
// neighbor, with at most four MACs (more is a trunk) and to CIs no LLDP/CDP
// neighbor of the device names; ARP entries only enrich, they give no edge.
// resolve maps (ip, mac, hostname) to a CI id; suppressed skips (source,
// target, type) edges a user removed (REL-03). The result is deduplicated,
// keeping the highest confidence of an edge, and free of self-loops. It does
// not depend on the order of rels: the FDB entries are derived after every
// other neighbor (F-269: an FDB entry listed before the LLDP neighbor of the
// same CI gave that edge 0.6, and the stored value flipped between ingests).
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

	// Every other neighbor first, the FDB entries last.
	order := make([]int, 0, len(rels))
	for i := range rels {
		if neighborProtocol(&rels[i]) != "fdb" {
			order = append(order, i)
		}
	}
	for i := range rels {
		if neighborProtocol(&rels[i]) == "fdb" {
			order = append(order, i)
		}
	}

	// edgeAt is the index of an edge in derived; lldpPeers are the CIs an
	// LLDP/CDP neighbor of the device names.
	edgeAt := make(map[string]int)
	lldpPeers := make(map[string]bool)
	var derived []DerivedRelationship
	var unknown []UnknownRelationship
	for _, i := range order {
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
		switch protocol {
		case "lldp", "cdp":
			lldpPeers[targetID] = true
		case "fdb":
			if lldpPeers[targetID] {
				continue // the LLDP/CDP neighbor is the edge of the pair
			}
		}
		if suppressed != nil && known && suppressed(sourceCIID, targetID, relType) {
			continue
		}
		if !known {
			unknown = append(unknown, UnknownRelationship{SourceCIID: sourceCIID, TargetCIID: targetID, TypeKey: rel.TypeKey, Neighbor: *rel})
			continue
		}
		if confidence == 0 {
			confidence = defaultConfidence
		}
		key := sourceCIID + "|" + targetID + "|" + relType
		if at, dup := edgeAt[key]; dup {
			derived[at].Confidence = max(derived[at].Confidence, confidence)
			continue
		}
		edgeAt[key] = len(derived)
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

// macSeparators removes the separators of the usual MAC notations; one
// Replacer for every call (building one per call was a few percent of the
// server's CPU during a re-discovery, WP-608).
var macSeparators = strings.NewReplacer(":", "", "-", "", ".", "")

func normalizeMAC(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = macSeparators.Replace(value)
	return value
}

// hasRelationships reports whether a resolved item of the batch reports
// relationships; without one the suppressions are not read.
func hasRelationships(items []IngestItem, resolvedCIID []string) bool {
	for i := range items {
		if resolvedCIID[i] != "" && len(items[i].Relationships) > 0 {
			return true
		}
	}
	return false
}

// deriveTopology resolves and upserts topology relationships for a bulk ingest
// batch, returning the number of relationships created. When the
// suppressions cannot be loaded no edge is written (fail-closed, REL-03).
func (h *Handler) deriveTopology(ctx context.Context, orgID string, items []IngestItem, resolvedCIID []string, existing []ci.Item) int {
	if h.relRepo == nil || !hasRelationships(items, resolvedCIID) {
		return 0
	}

	index := BuildNeighborIndex(existing)
	suppressed, err := h.suppressionChecker(ctx, orgID)
	if err != nil {
		slog.Error("topology skipped: suppressions not loaded", "event", "discovery.topology.failed", "organization_id", orgID, "error", err)
		return 0
	}

	derived := make([][]DerivedRelationship, len(items))
	unknown := make([][]UnknownRelationship, len(items))
	for i, item := range items {
		srcID := resolvedCIID[i]
		if srcID == "" || len(item.Relationships) == 0 {
			continue
		}
		derived[i], unknown[i] = DeriveTopology(srcID, item.Relationships, index.Resolve, suppressed)
	}
	return h.writeTopology(ctx, orgID, derived, unknown, true)
}

// writeTopology writes the derived edges and opens the reviews of the
// unknown keys, device by device, and returns the number of new edges.
// The edges seen again are read with one statement and, with confirm,
// re-confirmed with one (WP-608, NFR-03); every other edge takes the path
// of one edge, which also finds an edge created earlier in the batch.
func (h *Handler) writeTopology(ctx context.Context, orgID string, derived [][]DerivedRelationship, unknown [][]UnknownRelationship, confirm bool) int {
	var keys []EdgeKey
	for i := range derived {
		for _, d := range derived[i] {
			keys = append(keys, d.key())
		}
	}
	known := h.discoveryEdges(ctx, orgID, keys)
	var confirmations []relationship.Confirmation
	created := 0
	for i := range derived {
		for _, d := range derived[i] {
			if e, ok := known[d.key()]; ok {
				if confirm && relationship.MayChange(RelationshipSource, e.Source) {
					confidence := d.Confidence
					confirmations = append(confirmations, relationship.Confirmation{ID: e.ID, Confidence: &confidence})
				}
				continue
			}
			if h.upsertRelationship(ctx, orgID, d) {
				created++
			}
		}
	}
	for i := range unknown {
		for j := range unknown[i] {
			h.reviewUnknownRelationship(ctx, orgID, &unknown[i][j])
		}
	}
	h.confirmEdges(ctx, orgID, confirmations)
	return created
}

// StoredNeighbor is an LLDP/CDP neighbor a device reported, stored with its
// CI (raw_data.neighbors, F-374).
type StoredNeighbor struct {
	Protocol       string `json:"protocol"`
	LocalInterface string `json:"local_interface,omitempty"`
	RemoteIP       string `json:"remote_ip,omitempty"`
	RemoteMAC      string `json:"remote_mac,omitempty"`
	RemoteHostname string `json:"remote_hostname,omitempty"`
}

// storedNeighbors returns the LLDP/CDP neighbors of a record as they are
// stored: trimmed, without duplicates and sorted, so that an unchanged
// report leaves raw_data unchanged whatever order the collector lists them
// in (AUD-03). FDB and ARP entries are not stored.
func storedNeighbors(neighbors []plugins.DeviceNeighbor) []StoredNeighbor {
	var out []StoredNeighbor
	seen := map[StoredNeighbor]bool{}
	for i := range neighbors {
		n := StoredNeighbor{
			Protocol:       strings.ToLower(strings.TrimSpace(neighbors[i].Protocol)),
			LocalInterface: strings.TrimSpace(neighbors[i].LocalInterface),
			RemoteIP:       strings.TrimSpace(neighbors[i].RemoteIP),
			RemoteMAC:      strings.TrimSpace(neighbors[i].RemoteMAC),
			RemoteHostname: strings.TrimSpace(neighbors[i].RemoteHostname),
		}
		if (n.Protocol != "lldp" && n.Protocol != "cdp") || (n.RemoteIP == "" && n.RemoteMAC == "" && n.RemoteHostname == "") || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b StoredNeighbor) int {
		return cmp.Or(cmp.Compare(a.LocalInterface, b.LocalInterface), cmp.Compare(a.Protocol, b.Protocol),
			cmp.Compare(a.RemoteIP, b.RemoteIP), cmp.Compare(a.RemoteMAC, b.RemoteMAC), cmp.Compare(a.RemoteHostname, b.RemoteHostname))
	})
	return out
}

// NeighborKeys are the normalized identifiers of new CIs a stored
// neighbor may name: management IPs, MACs (normalizeMAC) and hostnames
// or FQDNs, as BuildNeighborIndex indexes them.
type NeighborKeys struct {
	IPs, MACs, Hostnames []string
}

// NeighborReporter is a CI with the neighbors stored from its last report.
type NeighborReporter struct {
	ID        string
	Neighbors []StoredNeighbor
}

// NeighborReporterFinder lists the CIs of an organization whose stored
// neighbors name one of the keys (PostgreSQL, F-374).
type NeighborReporterFinder interface {
	NeighborReporters(ctx context.Context, orgID string, keys *NeighborKeys) ([]NeighborReporter, error)
}

// deriveNeighborsOf writes the LLDP/CDP edges other devices reported to the
// CIs a batch created (created, with their ids createdCIID), from the
// neighbors stored on the reporting CIs. A reporting device processed
// before its neighbor's CI existed (another lane, an earlier batch) found
// no CI to connect to (relationships-suppression.spec.ts R01: 4 of 7 edges
// of sim-access-01 after the first cycle). Each side derives after its own
// commit, so whichever of the two derives last sees the other and every
// edge is written in the cycle that reports both, as derivePowerFeedsOf
// does for powered_by (WP-498). The neighbors resolve as in deriveTopology
// (TOP-01 confidence, REL-03 suppressions fail-closed); only the edges to
// the new CIs are written, edges that exist are left as they are (the
// reporter's own report re-confirms them), and the CIs processed in this
// batch were derived with their own report.
func (h *Handler) deriveNeighborsOf(ctx context.Context, orgID string, created []IngestItem, createdCIID, processedCIID []string) int {
	if h.relRepo == nil || len(createdCIID) == 0 {
		return 0
	}
	finder, ok := h.repo.(NeighborReporterFinder)
	if !ok {
		return 0
	}
	newIDs := map[string]bool{}
	for _, id := range createdCIID {
		newIDs[id] = true
	}
	ips, macs, hosts := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var keys NeighborKeys
	add := func(set map[string]bool, list *[]string, key string) {
		if key != "" && !set[key] {
			set[key] = true
			*list = append(*list, key)
		}
	}
	for i := range created {
		add(ips, &keys.IPs, normalizeKey(created[i].ManagementIP))
		add(macs, &keys.MACs, normalizeMAC(created[i].PrimaryMAC))
		add(hosts, &keys.Hostnames, normalizeKey(created[i].Hostname))
		add(hosts, &keys.Hostnames, normalizeKey(created[i].FQDN))
	}
	if len(keys.IPs)+len(keys.MACs)+len(keys.Hostnames) == 0 {
		return 0
	}
	reporters, err := finder.NeighborReporters(ctx, orgID, &keys)
	if err != nil {
		slog.Warn("topology of new CIs not derived: stored neighbors not loaded", "event", "discovery.topology.neighbors_failed",
			"organization_id", orgID, "error", err)
		return 0
	}
	processed := map[string]bool{}
	for _, id := range processedCIID {
		processed[id] = true
	}
	var sources []string
	var items []IngestItem
	for _, r := range reporters {
		if processed[r.ID] {
			continue
		}
		var rels []wire.IngestRelationship
		for _, n := range r.Neighbors {
			if !ips[normalizeKey(n.RemoteIP)] && !macs[normalizeMAC(n.RemoteMAC)] && !hosts[normalizeKey(n.RemoteHostname)] {
				continue
			}
			rels = append(rels, wire.IngestRelationship{TargetIP: n.RemoteIP, TargetMAC: n.RemoteMAC, TargetHostname: n.RemoteHostname,
				TypeKey: "connected_to", Protocol: n.Protocol, LocalInterface: n.LocalInterface})
		}
		if len(rels) > 0 {
			sources = append(sources, r.ID)
			items = append(items, IngestItem{Relationships: rels})
		}
	}
	if len(items) == 0 {
		return 0
	}
	suppressed, err := h.suppressionChecker(ctx, orgID)
	if err != nil {
		slog.Error("topology of new CIs skipped: suppressions not loaded", "event", "discovery.topology.failed", "organization_id", orgID, "error", err)
		return 0
	}
	// The neighbors resolve against every CI they name, not only the new
	// ones: a neighbor whose IP names an older CI stays that CI's edge.
	index := BuildNeighborIndex(h.neighborCandidates(ctx, orgID, items))
	derived := make([][]DerivedRelationship, len(items))
	unknown := make([][]UnknownRelationship, len(items))
	for i := range items {
		all, odd := DeriveTopology(sources[i], items[i].Relationships, index.Resolve, suppressed)
		for _, d := range all {
			if newIDs[d.TargetCIID] {
				derived[i] = append(derived[i], d)
			}
		}
		for j := range odd {
			if newIDs[odd[j].TargetCIID] {
				unknown[i] = append(unknown[i], odd[j])
			}
		}
	}
	return h.writeTopology(ctx, orgID, derived, unknown, false)
}

// EdgeKey is the source, target and type of a discovery edge.
type EdgeKey struct {
	SourceCIID, TargetCIID, RelType string
}

// FoundEdge is the oldest edge of an EdgeKey: its id and source.
type FoundEdge struct {
	ID, Source string
}

func (d *DerivedRelationship) key() EdgeKey {
	return EdgeKey{SourceCIID: d.SourceCIID, TargetCIID: d.TargetCIID, RelType: d.RelType}
}

// edgesFinder looks the edges of several keys up with one statement
// (PostgreSQL, WP-608).
type edgesFinder interface {
	DiscoveryEdges(ctx context.Context, orgID string, keys []EdgeKey) (map[EdgeKey]FoundEdge, error)
}

// discoveryEdges returns the existing edges of keys, read at once; nil when
// the repository cannot or the read failed, then every edge takes the path
// of one edge (upsertRelationship).
func (h *Handler) discoveryEdges(ctx context.Context, orgID string, keys []EdgeKey) map[EdgeKey]FoundEdge {
	finder, ok := h.repo.(edgesFinder)
	if !ok || len(keys) == 0 {
		return nil
	}
	found, err := finder.DiscoveryEdges(ctx, orgID, keys)
	if err != nil {
		slog.Warn("discovery edges not read at once", "organization_id", orgID, "error", err)
		return nil
	}
	return found
}

// confirmEdges re-confirms the discovery edges a batch saw again: with one
// statement where the relationship store can, else, and when that fails,
// edge by edge as before.
func (h *Handler) confirmEdges(ctx context.Context, orgID string, confirmations []relationship.Confirmation) {
	if len(confirmations) == 0 {
		return
	}
	if batch, ok := h.relRepo.(relationship.BatchConfirmer); ok {
		err := batch.ConfirmMany(ctx, orgID, confirmations)
		if err == nil {
			return
		}
		slog.Warn("re-confirm discovery edges at once", "organization_id", orgID, "error", err)
	}
	confirmer, ok := h.relRepo.(relationship.Confirmer)
	if !ok {
		return
	}
	for _, c := range confirmations {
		if err := confirmer.Confirm(ctx, orgID, c.ID, c.Confidence); err != nil {
			slog.Warn("re-confirm discovery edge", "relationship_id", c.ID, "error", err)
		}
	}
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
			if relationship.MayChange(RelationshipSource, source) {
				h.reconfirm(ctx, orgID, id, &confidence, d.Refresh)
			}
			return false
		}
	} else if existing, _, err := h.relRepo.List(ctx, orgID, d.SourceCIID, api.PaginationParams{Limit: 10000, Offset: 0}); err == nil {
		for i := range existing {
			rel := &existing[i]
			if rel.SourceCIID != d.SourceCIID || rel.TargetCIID != d.TargetCIID || rel.RelType != d.RelType {
				continue
			}
			if relationship.MayChange(RelationshipSource, rel.Source) {
				h.reconfirm(ctx, orgID, rel.ID, &confidence, d.Refresh)
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

// reconfirm re-confirms one discovery edge; with refresh its attributes
// too, which only a BatchConfirmer can (WP-498).
func (h *Handler) reconfirm(ctx context.Context, orgID, id string, confidence *float64, refresh map[string]any) {
	if refresh != nil {
		h.confirmEdges(ctx, orgID, []relationship.Confirmation{{ID: id, Confidence: confidence, Attributes: refresh}})
		return
	}
	if confirmer, ok := h.relRepo.(relationship.Confirmer); ok {
		if err := confirmer.Confirm(ctx, orgID, id, confidence); err != nil {
			slog.Warn("re-confirm discovery edge", "relationship_id", id, "error", err)
		}
	}
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
