package ci

import (
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// filterColumns maps the filterable CI fields (API-02) onto their SQL
// expression and kind. Only expressions from this map are interpolated
// into SQL.
var filterColumns = map[string]struct{ expr, kind string }{
	"name":             {"name", api.KindText},
	"status":           {"status", api.KindText},
	"hostname":         {"hostname", api.KindText},
	"fqdn":             {"fqdn", api.KindText},
	"serial_number":    {"serial_number", api.KindText},
	"manufacturer":     {"manufacturer", api.KindText},
	"model":            {"model", api.KindText},
	"os_name":          {"os_name", api.KindText},
	"os_version":       {"os_version", api.KindText},
	"firmware_version": {"firmware_version", api.KindText},
	"management_ip":    {"host(management_ip)", api.KindText},
	"primary_mac":      {"primary_mac::text", api.KindText},
	"discovery_source": {"discovery_source", api.KindText},
	"lifecycle_state":  {"lifecycle_state", api.KindText},
	"health":           {"health", api.KindText},
	"ci_type_id":       {"ci_type_id", api.KindUUID},
	"client_id":        {"client_id", api.KindUUID},
	"site_id":          {"site_id", api.KindUUID},
	"room_id":          {"room_id", api.KindUUID},
	"location_id":      {"location_id", api.KindUUID},
	"parent_ci_id":     {"parent_ci_id", api.KindUUID},
	"asset_id":         {"asset_id", api.KindUUID},
	"is_manual":        {"is_manual", api.KindBool},
	"created_at":       {"created_at", api.KindTime},
	"updated_at":       {"updated_at", api.KindTime},
	"first_seen_at":    {"first_seen_at", api.KindTime},
	"last_seen_at":     {"last_seen_at", api.KindTime},
}

// ListFields is the whitelist of the CI list (API-02): filterable fields,
// sortable fields (sortColumnCasts) and the selectable response fields.
var ListFields = func() api.ListFields {
	spec := api.ListFields{Filterable: map[string]string{}, Attributes: true}
	for field, c := range filterColumns {
		spec.Filterable[field] = c.kind
	}
	spec.Sortable = []string{"created_at", "updated_at", "name", "status"}
	t := reflect.TypeFor[Item]()
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			spec.Selectable = append(spec.Selectable, name)
		}
	}
	return spec
}()

// fieldValue returns the value of a filterable field (or an attribute) of
// an item as the memory repository compares it.
func fieldValue(item *Item, field string) string {
	if key, ok := strings.CutPrefix(field, api.AttributePrefix); ok {
		v, present := item.Attributes[key]
		if !present || v == nil {
			return ""
		}
		switch x := v.(type) {
		case string:
			return x
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(x)
		}
		return ""
	}
	at := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	switch field {
	case "name":
		return item.Name
	case "status":
		return item.Status
	case "hostname":
		return item.Hostname
	case "fqdn":
		return item.FQDN
	case "serial_number":
		return item.SerialNumber
	case "manufacturer":
		return item.Manufacturer
	case "model":
		return item.Model
	case "os_name":
		return item.OSName
	case "os_version":
		return item.OSVersion
	case "firmware_version":
		return item.FirmwareVersion
	case "management_ip":
		return item.ManagementIP
	case "primary_mac":
		return item.PrimaryMAC
	case "discovery_source":
		return item.DiscoverySource
	case "lifecycle_state":
		return item.LifecycleState
	case "health":
		return item.Health
	case "ci_type_id":
		return item.CITypeID
	case "client_id":
		return item.ClientID
	case "site_id":
		return item.SiteID
	case "room_id":
		return item.RoomID
	case "location_id":
		return item.LocationID
	case "parent_ci_id":
		return item.ParentCIID
	case "asset_id":
		return item.AssetID
	case "is_manual":
		return strconv.FormatBool(item.IsManual)
	case "created_at":
		return item.CreatedAt
	case "updated_at":
		return item.UpdatedAt
	case "first_seen_at":
		return at(item.FirstSeenAt)
	case "last_seen_at":
		return at(item.LastSeenAt)
	}
	return ""
}
