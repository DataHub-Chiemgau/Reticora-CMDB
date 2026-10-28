package discovery

import (
	"context"
	"log/slog"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
)

// systemTemplateAttribute is the attribute in which a collector profile
// names the system template of a device (collector/profiles, DIS-02) and in
// which the CI keeps the template key of the type catalog.
const systemTemplateAttribute = "system_template"

// profileTemplates maps the systemTemplate values of the collector profiles
// (collector/profiles/data/*.yaml) to the template keys of the type catalog
// (citype.Templates, F-263). The OS profiles (WMI, SSH) cannot tell
// hardware from a VM; physical_server is the server template whose
// server_kind covers both.
var profileTemplates = map[string]string{
	"server-bmc":     "physical_server",
	"windows-server": "physical_server",
	"linux-server":   "physical_server",
	"network-switch": "switch",
	"router":         "router",
	"firewall":       "firewall",
	"access-point":   "access_point",
	"ups":            "ups",
	"pdu":            "pdu",
	"hypervisor":     "hypervisor",
	"storage-array":  "storage",
	"nas":            "storage",
}

// catalogTemplateKeys are the template keys of the type catalog.
var catalogTemplateKeys = sync.OnceValue(func() map[string]bool {
	keys := map[string]bool{}
	for _, t := range citype.Templates() {
		keys[t.TemplateKey] = true
	}
	return keys
})

// SystemTemplateKey returns the catalog template key a profile's
// systemTemplate value stands for. A value that already is a catalog key
// (a profile from RETICORA_PROFILES_DIR, or a record mapped before) stays
// as it is; any other value maps to nothing.
func SystemTemplateKey(name string) (string, bool) {
	if key, ok := profileTemplates[name]; ok {
		return key, true
	}
	if catalogTemplateKeys()[name] {
		return name, true
	}
	return "", false
}

// mapSystemTemplates replaces the system_template attribute of each item
// with its catalog template key (DIS-02, F-263). An unknown name is logged
// as discovery.profile.unknown_template and the attribute removed: the CI
// stores no template the catalog does not know.
func mapSystemTemplates(ctx context.Context, orgID string, items []IngestItem) {
	for i := range items {
		value, ok := items[i].Attributes[systemTemplateAttribute]
		if !ok {
			continue
		}
		name, _ := value.(string)
		if key, known := SystemTemplateKey(name); known {
			items[i].Attributes[systemTemplateAttribute] = key
			continue
		}
		delete(items[i].Attributes, systemTemplateAttribute)
		slog.WarnContext(ctx, "discovery profile names an unknown system template; the attribute is not stored",
			"event", "discovery.profile.unknown_template", "organization_id", orgID,
			"record_id", items[i].RecordID, "system_template", value)
	}
}
