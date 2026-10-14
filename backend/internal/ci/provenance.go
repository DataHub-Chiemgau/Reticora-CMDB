package ci

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// The provenance of every typed field of a CI (REC-10): where the shown
// value comes from, when it was recorded and why it was chosen. Fields
// with a provenance row (observed or overridden) answer from it; the other
// typed fields come from the CI record itself.

// Why a field shows its value (REC-10).
const (
	// ReasonOverride: a manual override wins over every source (OVR-01).
	ReasonOverride = "override"
	// ReasonSourceRank: the observed value of the highest-ranked source
	// that reported it (REC-03).
	ReasonSourceRank = "source_rank"
	// ReasonAsset: the asset is canonical for the field (DB-05, AST-04).
	ReasonAsset = "asset"
	// ReasonRecord: the value of the CI record, written by its source.
	ReasonRecord = "record"
)

// ProvenanceSource reads the provenance rows of a CI (override.Repository).
type ProvenanceSource interface {
	ListForCI(ctx context.Context, orgID, ciID string) ([]override.FieldValue, error)
}

// FieldProvenance is the provenance of one field as the API answers it.
type FieldProvenance struct {
	Field  string `json:"field"`
	Value  any    `json:"value"`
	Source string `json:"source"`
	Rank   int    `json:"rank"`
	// Reason is why the value is shown: override, source_rank, asset or
	// record.
	Reason     string     `json:"reason"`
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	// The override, when there is one, and the observed value it covers.
	OverrideAuthor   string `json:"override_author,omitempty"`
	OverrideReason   string `json:"override_reason,omitempty"`
	Protected        bool   `json:"protected,omitempty"`
	DiscoveredValue  any    `json:"discovered_value,omitempty"`
	DiscoveredSource string `json:"discovered_source,omitempty"`
	Diverged         bool   `json:"diverged"`
}

// typedFields are the typed columns of a CI with their read of item.
var typedFields = []struct {
	name string
	read func(*Item) string
}{
	{"name", func(i *Item) string { return i.Name }},
	{"status", func(i *Item) string { return i.Status }},
	{"manufacturer", func(i *Item) string { return i.Manufacturer }},
	{"model", func(i *Item) string { return i.Model }},
	{"serial_number", func(i *Item) string { return i.SerialNumber }},
	{"hardware_uuid", func(i *Item) string { return i.HardwareUUID }},
	{"management_ip", func(i *Item) string { return i.ManagementIP }},
	{"primary_mac", func(i *Item) string { return i.PrimaryMAC }},
	{"hostname", func(i *Item) string { return i.Hostname }},
	{"fqdn", func(i *Item) string { return i.FQDN }},
	{"os_name", func(i *Item) string { return i.OSName }},
	{"os_version", func(i *Item) string { return i.OSVersion }},
	{"firmware_version", func(i *Item) string { return i.FirmwareVersion }},
	{"sys_object_id", func(i *Item) string { return i.SysObjectID }},
}

// WithProvenance attaches the provenance rows for GET /cis/{id}/provenance.
func (h *Handler) WithProvenance(source ProvenanceSource) *Handler {
	h.provenance = source
	return h
}

// Provenance handles GET /api/v1/cis/{id}/provenance (REC-10): one entry
// per typed field and attribute of the CI, sorted by field name.
func (h *Handler) Provenance(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.svc.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	}
	var records []override.FieldValue
	if h.provenance != nil {
		if records, err = h.provenance.ListForCI(r.Context(), t.OrganizationID, item.ID); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, api.CompleteList(BuildProvenance(item, records)))
}

// BuildProvenance merges the provenance rows of a CI with its typed fields
// and attributes: a field with a row answers from it, every other field
// with a value from the CI record (or the asset for the serial number).
func BuildProvenance(item *Item, records []override.FieldValue) []FieldProvenance {
	byField := make(map[string]FieldProvenance, len(records))
	for i := range records {
		byField[records[i].FieldName] = fromRecord(&records[i])
	}
	recordSource := item.DiscoverySource
	if recordSource == "" || item.IsManual {
		recordSource = "manual"
	}
	recordedAt := parseTime(item.UpdatedAt)
	add := func(field string, value any) {
		if _, ok := byField[field]; ok || value == nil || value == "" {
			return
		}
		byField[field] = FieldProvenance{
			Field: field, Value: value, Source: recordSource, Rank: override.SourceRank(recordSource),
			Reason: ReasonRecord, RecordedAt: recordedAt, ObservedAt: item.LastSeenAt,
		}
	}
	for _, f := range typedFields {
		if f.name == "serial_number" && item.Asset != nil && item.Asset.SerialNumber != "" {
			if _, ok := byField[f.name]; !ok {
				byField[f.name] = FieldProvenance{
					Field: f.name, Value: item.Asset.SerialNumber, Source: "asset", Reason: ReasonAsset,
				}
			}
			continue
		}
		add(f.name, f.read(item))
	}
	for key, value := range item.Attributes {
		add(key, value)
	}
	out := make([]FieldProvenance, 0, len(byField))
	for field := range byField {
		out = append(out, byField[field])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

func fromRecord(rec *override.FieldValue) FieldProvenance {
	p := FieldProvenance{
		Field: rec.FieldName, Value: rec.EffectiveValue, Diverged: rec.Diverged,
		DiscoveredValue: rec.DiscoveredValue, DiscoveredSource: rec.DiscoveredSource,
	}
	if rec.OverrideAt != nil || rec.OverrideValue != nil {
		p.Source, p.Reason, p.Rank = "manual_override", ReasonOverride, override.SourceRank("manual_override")
		p.RecordedAt, p.OverrideAuthor, p.OverrideReason, p.Protected = rec.OverrideAt, rec.OverrideAuthor, rec.OverrideReason, rec.Protected
		p.ObservedAt = rec.ObservedAt
		return p
	}
	p.Source, p.Reason, p.Rank = rec.DiscoveredSource, ReasonSourceRank, rec.DiscoveredRank
	if p.Rank == 0 {
		p.Rank = override.SourceRank(rec.DiscoveredSource)
	}
	p.RecordedAt, p.ObservedAt = rec.DiscoveredAt, rec.ObservedAt
	if p.Value == nil {
		p.Value = rec.DiscoveredValue
	}
	return p
}

func parseTime(value string) *time.Time {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &at
}
