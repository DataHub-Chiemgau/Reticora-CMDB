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
)
