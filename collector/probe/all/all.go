// Package all binds the probe types of the subpackages of collector/probe
// into the collector (E-183, WP-956 AK6). A type with its own scope lives in
// collector/probe/<package>/ and registers its executor in an init function;
// its work package adds a blank import of that package here. The collector
// (collector/collectorcmd) imports this package once, so every subpackage
// added later is registered without a change there. Simple types live flat
// in collector/probe and need no entry.
package all

import (
	// redfish: health, temperatures, fans and power supplies (WP-998).
	_ "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/redfish"
	// snmp_status: reachability, reboot and OID checks (WP-995).
	_ "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/snmpstatus"
	// snmp_if: interfaces with selection, rates and utilization (WP-996).
	_ "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/snmpif"
	// snmp_custom: configured OIDs with kind, scale and thresholds (WP-997).
	_ "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/snmpcustom"
	// ospf and isis over the standard MIBs (WP-1002), bgp over BGP4-MIB and
	// the BGP4-V2 profiles (WP-1001).
	_ "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/routing"
	// tr069: read-only ACS adapter, GenieACS NBI (WP-1018).
	_ "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/tr069"
	// rpki_rtr: RTR client with the shared VRP cache per validator
	// (WP-1020).
	_ "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rpki"
)
