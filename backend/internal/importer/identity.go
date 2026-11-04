package importer

import (
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// Targets of a CI row beside the typed columns of a CI (ciTextTargets) and
// its attributes (attributes.<key>).
const (
	// CITypeTarget is the CI type of the row: a name, key or UUID,
	// resolved like a collector's ci_type_name; CITypeIDTarget is its
	// alias.
	CITypeTarget   = "ci_type"
	CITypeIDTarget = "ci_type_id"
	// CIClientTarget is the client of the row: it binds the identity
	// resolution to the client's CIs (REC-02) and is the client of a new
	// CI.
	CIClientTarget = "client_id"
	// CIVRFTarget is the VRF of the management IP (REC-02); empty is the
	// organization's default VRF.
	CIVRFTarget = "vrf_id"
	// CILocationTarget is the location of the CI as a name path from the
	// site down (LocationResolver); CILocationKindTarget its kind, by
	// default the kind of the path's depth (site, building, room, rack).
	// CILocationIDTarget is the location as id.
	CILocationTarget     = "location"
	CILocationKindTarget = "location_kind"
	CILocationIDTarget   = "location_id"
	// CIObservedAtTarget dates the values of the row (RFC 3339 or
	// YYYY-MM-DD); without it they are dated with the start of the run.
	CIObservedAtTarget = "observed_at"
)

// ciTextTargets are the typed columns of a CI a row writes, by their name
// in the API and in the provenance of the field (override).
var ciTextTargets = []string{
	"name", "status", "manufacturer", "model", "serial_number", "hardware_uuid", "management_ip", "primary_mac",
	"hostname", "fqdn", "os_name", "os_version", "firmware_version", "sys_object_id", CILocationIDTarget,
}

// ingestItem maps a CI row onto the record the identity resolution of the
// ingest takes (REC-02): the resolved type id, serial number, hardware
// UUID, primary MAC, management IP, sysObjectID, VRF, host name, FQDN,
// client and name, with source import. It is the mapping only; the
// identity is resolved by discovery.IdentityResolver.
func ingestItem(row *MappedRow, typeID string) discovery.IngestItem {
	text := func(target string) string { return strings.TrimSpace(row.Text(target)) }
	return discovery.IngestItem{
		CITypeName:   typeID,
		Name:         text("name"),
		Manufacturer: text("manufacturer"),
		Model:        text("model"),
		SerialNumber: text("serial_number"),
		HardwareUUID: text("hardware_uuid"),
		PrimaryMAC:   text("primary_mac"),
		ManagementIP: text("management_ip"),
		SysObjectID:  text("sys_object_id"),
		VRFID:        text(CIVRFTarget),
		Hostname:     text("hostname"),
		FQDN:         text("fqdn"),
		ClientID:     text(CIClientTarget),
		Source:       ci.SourceImport,
	}
}
