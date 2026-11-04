package ci

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// filterColumns maps the filterable CI fields (API-02) onto their SQL
// expression and kind. Only expressions from this map are interpolated
// into SQL. The owner (WP-752) compares with = and = ANY, both strict, so
// the partial indexes idx_ci_owner_user/idx_ci_owner_team (WP-280) carry
// the filter.
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
	"owner_user_id":    {"owner_user_id", api.KindUUID},
	"owner_team_id":    {"owner_team_id", api.KindUUID},
	"is_manual":        {"is_manual", api.KindBool},
	"created_at":       {"created_at", api.KindTime},
	"updated_at":       {"updated_at", api.KindTime},
	"first_seen_at":    {"first_seen_at", api.KindTime},
	"last_seen_at":     {"last_seen_at", api.KindTime},
}

// TagsField is the set field of the CI tags (WP-754): filter[tags] with
// exact, in or all compares the keys of the tags (ci_tag), outside
// filterColumns.
const TagsField = "tags"

// ListFields is the whitelist of the CI list (API-02): filterable fields,
// sortable fields (sortColumnCasts) and the selectable response fields.
var ListFields = func() api.ListFields {
	spec := api.ListFields{Filterable: map[string]string{TagsField: api.KindSet}, Attributes: true}
	for field, c := range filterColumns {
		spec.Filterable[field] = c.kind
	}
	spec.Sortable = []string{"created_at", "updated_at", "name", "status", "owner_user_id", "owner_team_id"}
	t := reflect.TypeFor[Item]()
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			spec.Selectable = append(spec.Selectable, name)
		}
	}
	return spec
}()

// namedTypes returns the CI types a list query restricts to (WP-566): the
// parameter ci_type_id and filter[ci_type_id] with exact or in, in lower
// case, one group per parameter. Each type stands for its subtree (MET-06,
// WP-708): a group names the union of its subtrees, several groups
// intersect (PGRepository.typeScope). named is false when none names a
// type; the attribute filters then follow the definitions of every type.
func (p *FilterParams) namedTypes() (groups [][]string, named bool) {
	add := func(values []string) {
		set := make([]string, 0, len(values))
		for _, v := range values {
			if v = strings.ToLower(strings.TrimSpace(v)); !slices.Contains(set, v) {
				set = append(set, v)
			}
		}
		groups = append(groups, set)
	}
	if p.TypeID != "" {
		add([]string{p.TypeID})
	}
	for _, f := range p.Filters {
		if f.Field == "ci_type_id" && (f.Op == api.OpExact || f.Op == api.OpIn) {
			add(f.Values)
		}
	}
	return groups, len(groups) > 0
}

// tagFilterKeys returns the tag keys of a filter[tags] in lower case and
// without duplicates: tag keys are lower case (TagKey), and all counts the
// distinct keys.
func tagFilterKeys(f api.Filter) []string {
	keys := make([]string, 0, len(f.Values))
	for _, v := range f.Values {
		if v = strings.ToLower(v); !slices.Contains(keys, v) {
			keys = append(keys, v)
		}
	}
	return keys
}

// tagsPredicate renders filter[tags] as a predicate on the CI row with the
// tag IDs as parameter argPos (WP-754): exact and in need one of the tags,
// all every one. ids maps the keys of the organization's tags to their IDs
// (tagIDsOf); an unknown key matches no tag, so all with one matches no CI.
// The IDs are a parameter, not a join with tag on the key: then the planner
// estimates the CIs of the tags from the statistics of ci_tag.tag_id and
// reads the CIs of a rare tag through idx_ci_tag_org_tag, where with the
// join it estimated the average tag (ci_tag rows / distinct tags), walked
// the list in sort order and probed every CI of the organization
// (TestCIQueryBudget; as for the types, typeScope).
func tagsPredicate(f api.Filter, ids map[string]string, argPos int) (clause string, args []any) {
	keys := tagFilterKeys(f)
	known := make([]string, 0, len(keys))
	for _, key := range keys {
		if id, ok := ids[key]; ok {
			known = append(known, id)
		}
	}
	if len(known) == 0 || (f.Op == api.OpAll && len(known) < len(keys)) {
		return "false", nil
	}
	if f.Op == api.OpAll {
		// (ci_id, tag_id) is unique: the count is the number of the tags
		// the CI holds.
		return fmt.Sprintf("(SELECT count(*) FROM ci_tag ct WHERE ct.ci_id = ci.id AND ct.tag_id = ANY($%[1]d::uuid[]))"+
			" = cardinality($%[1]d::uuid[])", argPos), []any{known}
	}
	return fmt.Sprintf("EXISTS (SELECT 1 FROM ci_tag ct WHERE ct.ci_id = ci.id AND ct.tag_id = ANY($%d::uuid[]))", argPos), []any{known}
}

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
	case "owner_user_id":
		return item.OwnerUserID
	case "owner_team_id":
		return item.OwnerTeamID
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
