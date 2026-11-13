package security

import (
	"path"
	"strings"
)

// The product mapping of CIs (SEC-02, WP-1121, E-236 option 2): a CI is
// mapped to a vendor and products of the vulnerability catalog by its
// sys_object_id prefix or by manufacturer and model (glob patterns,
// case-insensitive). The rows of cpe_mapping of an organization go before
// the system mapping of this file; within the organization the lowest
// priority wins.

// Match kinds of a product mapping.
const (
	MatchManufacturerModel = "manufacturer_model"
	MatchSysObjectID       = "sys_object_id"
)

// Version fields of a product mapping.
const (
	VersionFieldFirmware = "firmware_version"
	VersionFieldOS       = "os_version"
)

// ProductMapping is one mapping of CIs to a vendor and its products
// (cpe_mapping, or a row of the system mapping).
type ProductMapping struct {
	ID                  string   `json:"id,omitempty"`
	MatchKind           string   `json:"match_kind"`
	ManufacturerPattern string   `json:"manufacturer_pattern,omitempty"`
	ModelPattern        string   `json:"model_pattern,omitempty"`
	SysObjectIDPrefix   string   `json:"sys_object_id_prefix,omitempty"`
	Vendor              string   `json:"vendor"`
	Products            []string `json:"products"`
	VersionField        string   `json:"version_field"`
	VersionScheme       string   `json:"version_scheme"`
	Priority            int      `json:"priority"`
	// System marks a row of the system mapping.
	System bool `json:"system,omitempty"`
}

// CIProduct is what the match reads of a CI.
type CIProduct struct {
	ID              string
	Manufacturer    string
	Model           string
	SysObjectID     string
	FirmwareVersion string
	OSVersion       string
}

// Matches reports whether the mapping applies to the CI.
func (m *ProductMapping) Matches(c *CIProduct) bool {
	switch m.MatchKind {
	case MatchSysObjectID:
		prefix := strings.TrimPrefix(strings.TrimSpace(m.SysObjectIDPrefix), ".")
		oid := strings.TrimPrefix(strings.TrimSpace(c.SysObjectID), ".")
		return prefix != "" && oid != "" && (oid == prefix || strings.HasPrefix(oid, prefix+"."))
	case MatchManufacturerModel:
		if strings.TrimSpace(c.Manufacturer) == "" {
			return false
		}
		if !glob(m.ManufacturerPattern, c.Manufacturer) {
			return false
		}
		return m.ModelPattern == "" || glob(m.ModelPattern, c.Model)
	}
	return false
}

// Version is the version of the CI the mapping compares: version_field, an
// empty firmware version falls back to the OS version.
func (m *ProductMapping) Version(c *CIProduct) string {
	if m.VersionField == VersionFieldOS {
		return strings.TrimSpace(c.OSVersion)
	}
	if v := strings.TrimSpace(c.FirmwareVersion); v != "" {
		return v
	}
	return strings.TrimSpace(c.OSVersion)
}

// glob matches s against pattern (path.Match, * and ?), case-insensitive.
func glob(pattern, s string) bool {
	ok, err := path.Match(strings.ToLower(strings.TrimSpace(pattern)), strings.ToLower(strings.TrimSpace(s)))
	return err == nil && ok
}

// systemMappings is the system mapping of the most common vendors of the
// target customers (ISPs and MSPs with network devices). Vendor and product
// names are those of the CSAF and NVD catalogs (normalized by
// vulnfeed.Normalize); several product names let both sources hit.
var systemMappings = []ProductMapping{
	// Cisco: NX-OS (Nexus) first, whose model names (N9K-C93180YC-EX,
	// N9K-C9504) contain the Catalyst pattern c9nnn as well; then IOS-XE
	// (Catalyst 9000, ISR 4000, ASR 1000) and IOS.
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "cisco*", ModelPattern: "*nexus*", Vendor: "cisco",
		Products: []string{"nx-os", "nx-os_software", "cisco_nx-os_software"}, VersionScheme: SchemeCiscoNXOS},
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "cisco*", ModelPattern: "n[3579]k*", Vendor: "cisco",
		Products: []string{"nx-os", "nx-os_software", "cisco_nx-os_software"}, VersionScheme: SchemeCiscoNXOS},
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "cisco*", ModelPattern: "*c9[0-9][0-9][0-9]*", Vendor: "cisco",
		Products: []string{"ios_xe", "ios_xe_software", "cisco_ios_xe_software"}, VersionScheme: SchemeCiscoIOSXE},
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "cisco*", ModelPattern: "*isr4*", Vendor: "cisco",
		Products: []string{"ios_xe", "ios_xe_software", "cisco_ios_xe_software"}, VersionScheme: SchemeCiscoIOSXE},
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "cisco*", ModelPattern: "*asr1*", Vendor: "cisco",
		Products: []string{"ios_xe", "ios_xe_software", "cisco_ios_xe_software"}, VersionScheme: SchemeCiscoIOSXE},
	{MatchKind: MatchSysObjectID, SysObjectIDPrefix: "1.3.6.1.4.1.9.12.3.1.3", Vendor: "cisco",
		Products: []string{"nx-os", "nx-os_software", "cisco_nx-os_software"}, VersionScheme: SchemeCiscoNXOS},
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "cisco*", Vendor: "cisco",
		Products: []string{"ios", "ios_software", "cisco_ios_software"}, VersionScheme: SchemeCiscoIOS},
	{MatchKind: MatchSysObjectID, SysObjectIDPrefix: "1.3.6.1.4.1.9", Vendor: "cisco",
		Products: []string{"ios", "ios_software", "cisco_ios_software"}, VersionScheme: SchemeCiscoIOS},
	// Juniper Junos.
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "juniper*", Vendor: "juniper", Products: []string{"junos", "junos_os"},
		VersionScheme: SchemeJunos},
	{MatchKind: MatchSysObjectID, SysObjectIDPrefix: "1.3.6.1.4.1.2636", Vendor: "juniper", Products: []string{"junos", "junos_os"},
		VersionScheme: SchemeJunos},
	// Nokia SR OS (Alcatel-Lucent 7750/7450/7950).
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "nokia*", Vendor: "nokia", Products: []string{"sr_os", "service_router_operating_system"},
		VersionScheme: SchemeGeneric},
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "alcatel*", Vendor: "nokia", Products: []string{"sr_os", "service_router_operating_system"},
		VersionScheme: SchemeGeneric},
	{MatchKind: MatchSysObjectID, SysObjectIDPrefix: "1.3.6.1.4.1.6527", Vendor: "nokia", Products: []string{"sr_os", "service_router_operating_system"},
		VersionScheme: SchemeGeneric},
	// Huawei VRP.
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "huawei*", Vendor: "huawei", Products: []string{"vrp", "versatile_routing_platform"},
		VersionScheme: SchemeGeneric},
	{MatchKind: MatchSysObjectID, SysObjectIDPrefix: "1.3.6.1.4.1.2011", Vendor: "huawei", Products: []string{"vrp", "versatile_routing_platform"},
		VersionScheme: SchemeGeneric},
	// MikroTik RouterOS.
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "mikrotik*", Vendor: "mikrotik", Products: []string{"routeros"}, //nolint:misspell // RouterOS is the product of MikroTik
		VersionScheme: SchemeGeneric},
	{MatchKind: MatchSysObjectID, SysObjectIDPrefix: "1.3.6.1.4.1.14988", Vendor: "mikrotik", Products: []string{"routeros"}, //nolint:misspell // RouterOS is the product of MikroTik
		VersionScheme: SchemeGeneric},
	// Zyxel (firmware of the USG/ATP/XGS families).
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "zyxel*", Vendor: "zyxel", Products: []string{"zld", "firmware", "zyxel_firmware"},
		VersionScheme: SchemeGeneric},
	{MatchKind: MatchSysObjectID, SysObjectIDPrefix: "1.3.6.1.4.1.890", Vendor: "zyxel", Products: []string{"zld", "firmware", "zyxel_firmware"},
		VersionScheme: SchemeGeneric},
	// AVM FRITZ!OS.
	{MatchKind: MatchManufacturerModel, ManufacturerPattern: "avm*", Vendor: "avm", Products: []string{"fritz!os", "fritzos", "fritz!box_firmware"},
		VersionScheme: SchemeGeneric},
	{MatchKind: MatchSysObjectID, SysObjectIDPrefix: "1.3.6.1.4.1.872", Vendor: "avm", Products: []string{"fritz!os", "fritzos", "fritz!box_firmware"},
		VersionScheme: SchemeGeneric},
}

// SystemMappings returns a copy of the system mapping, in the order it is
// tried (the more specific rows first).
func SystemMappings() []ProductMapping {
	out := make([]ProductMapping, len(systemMappings))
	for i := range systemMappings {
		out[i] = systemMappings[i]
		out[i].System = true
		if out[i].VersionField == "" {
			out[i].VersionField = VersionFieldFirmware
		}
		out[i].Products = append([]string(nil), systemMappings[i].Products...)
	}
	return out
}

// ResolveMapping returns the mapping of the CI: the first matching row of
// the organization (ordered by priority), else the first matching row of
// the system mapping; nil without one.
func ResolveMapping(org, system []ProductMapping, c *CIProduct) *ProductMapping {
	for i := range org {
		if org[i].Matches(c) {
			return &org[i]
		}
	}
	for i := range system {
		if system[i].Matches(c) {
			return &system[i]
		}
	}
	return nil
}
