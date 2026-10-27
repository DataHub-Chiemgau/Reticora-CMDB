package discovery

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Power chain (IMP-09): device → PDU → UPS (→ source). The outlet names a
// PDU or a UPS outlet group reports ("web01 PSU1", "pdu-a") name what they
// feed; each mapped outlet becomes a powered_by edge from the fed CI to the
// power device. A CI fed by more than one power device has redundant feeds:
// its powered_by edges carry redundant = true and their feed, and the
// impact postfilter (IMP-04, powered_by is a redundant type) counts it as
// affected only when all feeds failed. An outlet switched off feeds
// nothing: its edge stays, it does not count for redundant (E-85 option A).

// outletConfidence is the confidence of an outlet-mapped edge: the mapping
// is typed in by people.
const outletConfidence = 0.8

// DevicePower and DeviceOutlet are the power part of the DeviceRecord.
type (
	DevicePower  = plugins.DevicePower
	DeviceOutlet = plugins.DeviceOutlet
)

// OutletFeed is one outlet mapped to the CI it feeds.
type OutletFeed struct {
	PowerCIID string
	FedCIID   string
	Outlet    string
	Feed      string
	State     string
}

var (
	feedSuffix = regexp.MustCompile(`(?i)[\s_\-.:/]*\(?\b(psu|ps|pwr|power|feed|netzteil|nt|input|in)[\s_\-]*([0-9]+|[ab])\)?$`)
	sideSuffix = regexp.MustCompile(`(?i)[\s_\-.:/]+\(?([ab])\)?$`)
	numSuffix  = regexp.MustCompile(`(?i)[\s_\-]+\(?#?(\d+)\)?$`)
)

// ParseOutletName splits an outlet name into the name of the fed device
// and its feed ("web01 PSU2" → web01, psu2; "db01-B" → db01, b).
func ParseOutletName(name string) (device, feed string) {
	n := strings.TrimSpace(name)
	switch {
	case feedSuffix.MatchString(n):
		m := feedSuffix.FindStringSubmatch(n)
		return strings.TrimSpace(n[:len(n)-len(m[0])]), strings.ToLower(m[1] + m[2])
	case sideSuffix.MatchString(n):
		m := sideSuffix.FindStringSubmatch(n)
		return strings.TrimSpace(n[:len(n)-len(m[0])]), strings.ToLower(m[1])
	case numSuffix.MatchString(n):
		m := numSuffix.FindStringSubmatch(n)
		return strings.TrimSpace(n[:len(n)-len(m[0])]), m[1]
	}
	return n, ""
}

// outletsOf returns the outlets of a power device item.
func outletsOf(item *IngestItem) []DeviceOutlet {
	raw, ok := item.RawData["power"]
	if !ok || raw == nil {
		return nil
	}
	var p DevicePower
	b, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(b, &p) != nil {
		return nil
	}
	return p.Outlets
}

// PowerFeeds maps the outlets of the power devices of a batch to the CIs
// they feed; resolve finds the CI of a device name.
func PowerFeeds(items []IngestItem, resolvedCIID []string, resolve func(name string) (string, bool)) []OutletFeed {
	var feeds []OutletFeed
	for i := range items {
		powerID := resolvedCIID[i]
		if powerID == "" {
			continue
		}
		for _, o := range outletsOf(&items[i]) {
			// The whole outlet name first ("PDU-A" is a device), then the
			// name without its feed suffix ("web01 PSU1").
			device, feed := ParseOutletName(o.Name)
			fed, ok := resolve(strings.TrimSpace(o.Name))
			if ok {
				feed = ""
			} else if device != "" {
				fed, ok = resolve(device)
			}
			if !ok || fed == "" || fed == powerID {
				continue
			}
			feeds = append(feeds, OutletFeed{PowerCIID: powerID, FedCIID: fed, Outlet: o.Name, Feed: feed, State: o.State})
		}
	}
	return feeds
}

// PowerEdges turns outlet feeds into powered_by edges; feeds of one CI
// from different power devices are marked redundant. An outlet that is
// switched off (outlet_state=off) gives its edge but feeds nothing (E-85
// option A). The flag is what the batch shows; derivePowerChain then
// recomputes it from every stored edge (WP-498).
func PowerEdges(feeds []OutletFeed) []DerivedRelationship {
	sources := map[string]map[string]bool{}
	for _, f := range feeds {
		if sources[f.FedCIID] == nil {
			sources[f.FedCIID] = map[string]bool{}
		}
		if f.State != "off" {
			sources[f.FedCIID][f.PowerCIID] = true
		}
	}
	seen := map[string]bool{}
	var out []DerivedRelationship
	for _, f := range feeds {
		key := f.FedCIID + "|" + f.PowerCIID
		if seen[key] {
			continue
		}
		seen[key] = true
		attrs := map[string]any{"outlet": f.Outlet, "redundant": len(sources[f.FedCIID]) > 1}
		// A re-confirmation refreshes what the outlet reports; a feed or
		// state the outlet no longer reports is removed (nil).
		refresh := map[string]any{"outlet": f.Outlet, "feed": nil, "outlet_state": nil}
		if f.Feed != "" {
			attrs["feed"], refresh["feed"] = f.Feed, f.Feed
		}
		if f.State != "" {
			attrs["outlet_state"], refresh["outlet_state"] = f.State, f.State
		}
		out = append(out, DerivedRelationship{SourceCIID: f.FedCIID, TargetCIID: f.PowerCIID, RelType: "powered_by",
			Confidence: outletConfidence, Attributes: attrs, Refresh: refresh})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].SourceCIID+out[i].TargetCIID < out[j].SourceCIID+out[j].TargetCIID
	})
	return out
}

// derivePowerChain writes the powered_by edges of the outlet mappings of a
// batch and returns the number of new edges. When the suppressions cannot
// be loaded no edge is written (fail-closed, REL-03). Afterwards the
// redundancy of every CI the batch feeds is recomputed from all its stored
// discovery edges and set on each of them, so that it depends neither on
// the batch nor on the lane a PDU arrives in (WP-498, F-112).
func (h *Handler) derivePowerChain(ctx context.Context, orgID string, items []IngestItem, resolvedCIID []string) int {
	if h.relRepo == nil {
		return 0
	}
	var names []string
	for i := range items {
		for _, o := range outletsOf(&items[i]) {
			names = append(names, strings.ToLower(strings.TrimSpace(o.Name)))
			if device, _ := ParseOutletName(o.Name); device != "" {
				names = append(names, strings.ToLower(device))
			}
		}
	}
	if len(names) == 0 {
		return 0
	}
	return h.writePowerFeeds(ctx, orgID, items, resolvedCIID, names, nil)
}

// StoredPowerDevice is a CI whose stored report holds outlets
// (raw_data.power.outlets).
type StoredPowerDevice struct {
	ID      string
	Outlets []DeviceOutlet
}

// PowerDeviceFinder lists the CIs of an organization whose stored report
// holds outlets (IMP-09).
type PowerDeviceFinder interface {
	PowerDevices(ctx context.Context, orgID string) ([]StoredPowerDevice, error)
}

// derivePowerFeedsOf writes the powered_by edges of the CIs a batch created
// (fed, with their ids fedCIID) from the outlets stored on the power
// devices processed before them. Only the power device reports the outlet
// mapping: when the collector's batch is spread over the ingest lanes, or
// the PDU came in an earlier batch, the PDU's derivation ran before the CI
// it feeds existed and found nothing to feed (discovery-power.spec.ts P01:
// 13 of 32 edges after the first cycle). Each side derives after its own
// commit, so whichever of the two derives last sees the other and every
// feed is written in the cycle that reports both. Only the feeds of the new
// CIs are written, resolved as the power device's own derivation resolves
// them.
func (h *Handler) derivePowerFeedsOf(ctx context.Context, orgID string, fed []IngestItem, fedCIID []string) int {
	if h.relRepo == nil || len(fedCIID) == 0 {
		return 0
	}
	finder, ok := h.repo.(PowerDeviceFinder)
	if !ok {
		return 0
	}
	fedIDs := map[string]bool{}
	for _, id := range fedCIID {
		fedIDs[id] = true
	}
	fedNames := map[string]bool{}
	for i := range fed {
		for _, n := range []string{fed[i].Name, fed[i].Hostname, fed[i].FQDN} {
			if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
				fedNames[n] = true
			}
		}
	}
	if len(fedNames) == 0 {
		return 0
	}
	devices, err := finder.PowerDevices(ctx, orgID)
	if err != nil {
		slog.Warn("power feeds of new CIs not derived: power devices not loaded", "event", "discovery.power.feeds_failed",
			"organization_id", orgID, "error", err)
		return 0
	}
	var items []IngestItem
	var powerIDs, names []string
	for _, d := range devices {
		// A power device created by this batch was derived with its report.
		if fedIDs[d.ID] {
			continue
		}
		matched := false
		for _, o := range d.Outlets {
			full := strings.ToLower(strings.TrimSpace(o.Name))
			device, _ := ParseOutletName(o.Name)
			device = strings.ToLower(device)
			if !fedNames[full] && (device == "" || !fedNames[device]) {
				continue
			}
			// Both names, so that the outlet resolves as in
			// derivePowerChain: the whole name first, then the device.
			names = append(names, full)
			if device != "" {
				names = append(names, device)
			}
			matched = true
		}
		if matched {
			items = append(items, IngestItem{RawData: map[string]any{"power": &DevicePower{Outlets: d.Outlets}}})
			powerIDs = append(powerIDs, d.ID)
		}
	}
	if len(items) == 0 {
		return 0
	}
	return h.writePowerFeeds(ctx, orgID, items, powerIDs, names, func(f *OutletFeed) bool { return fedIDs[f.FedCIID] })
}

// writePowerFeeds writes the powered_by edges of the outlets of items (the
// power devices powerIDs), resolving the outlet names names; keep, when
// set, selects the feeds that are written. When the suppressions cannot be
// loaded no edge is written (fail-closed, REL-03). It returns the number of
// new edges and recomputes the redundancy of every CI it feeds.
func (h *Handler) writePowerFeeds(ctx context.Context, orgID string, items []IngestItem, powerIDs, names []string, keep func(*OutletFeed) bool) int {
	suppressed, err := h.suppressionChecker(ctx, orgID)
	if err != nil {
		slog.Error("power chain skipped: suppressions not loaded", "event", "discovery.topology.failed", "organization_id", orgID, "error", err)
		return 0
	}
	byName := h.ciNameIndex(ctx, orgID, names)
	feeds := PowerFeeds(items, powerIDs, func(name string) (string, bool) {
		id, ok := byName[strings.ToLower(name)]
		return id, ok
	})
	if keep != nil {
		kept := feeds[:0]
		for i := range feeds {
			if keep(&feeds[i]) {
				kept = append(kept, feeds[i])
			}
		}
		feeds = kept
	}
	var edges []DerivedRelationship
	var keys []EdgeKey
	for _, d := range PowerEdges(feeds) {
		if suppressed != nil && suppressed(d.SourceCIID, d.TargetCIID, d.RelType) {
			continue
		}
		edges = append(edges, d)
		keys = append(keys, d.key())
	}
	// Edges seen again are re-confirmed with what their outlet reports now.
	known := h.discoveryEdges(ctx, orgID, keys)
	var confirmations []relationship.Confirmation
	var fed []string
	created := 0
	for _, d := range edges {
		fed = append(fed, d.SourceCIID)
		if e, ok := known[d.key()]; ok {
			if relationship.MayChange(RelationshipSource, e.Source) {
				confidence := d.Confidence
				confirmations = append(confirmations, relationship.Confirmation{ID: e.ID, Confidence: &confidence, Attributes: d.Refresh})
			}
			continue
		}
		if h.upsertRelationship(ctx, orgID, d) {
			created++
		}
	}
	h.confirmEdges(ctx, orgID, confirmations)
	if marker, ok := h.relRepo.(relationship.RedundancyMarker); ok && len(fed) > 0 {
		if err := marker.MarkRedundant(ctx, orgID, "powered_by", fed); err != nil {
			slog.Warn("power redundancy not recomputed", "event", "discovery.power.redundancy_failed", "organization_id", orgID, "error", err)
		}
	}
	return created
}

// ciNameIndex finds the CIs named by outlets: by hostname or FQDN through
// the identity index, otherwise by CI name.
func (h *Handler) ciNameIndex(ctx context.Context, orgID string, names []string) map[string]string {
	out := map[string]string{}
	finder, ok := h.repo.(IdentityCandidateFinder)
	if ok {
		for _, n := range names {
			ids, err := finder.IdentityCandidates(ctx, orgID, &IdentityQuery{Hostnames: []string{n}})
			if err == nil && len(ids) == 1 {
				out[n] = ids[0]
			}
		}
	}
	missing := false
	for _, n := range names {
		if _, found := out[n]; !found {
			missing = true
		}
	}
	if !missing || h.ciRepo == nil {
		return out
	}
	for _, n := range names {
		if _, found := out[n]; found {
			continue
		}
		items, _, err := h.ciRepo.List(ctx, orgID, ci.FilterParams{Search: n}, api.PaginationParams{Limit: 50})
		if err != nil {
			continue
		}
		var match string
		for i := range items {
			if strings.EqualFold(items[i].Name, n) || strings.EqualFold(items[i].Hostname, n) {
				if match != "" && match != items[i].ID {
					match = ""
					break
				}
				match = items[i].ID
			}
		}
		if match != "" {
			out[n] = match
		}
	}
	return out
}
