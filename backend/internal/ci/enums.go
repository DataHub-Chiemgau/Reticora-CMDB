package ci

// Status values a CI may hold. These constants are the single source of truth
// for the Go layer and are asserted against the `ci_status_check` database
// constraint by TestStatusesMatchDatabaseConstraint.
const (
	StatusActive         = "active"
	StatusInactive       = "inactive"
	StatusMaintenance    = "maintenance"
	StatusDecommissioned = "decommissioned"
	StatusUnknown        = "unknown"
)

// Statuses returns every valid CI status.
func Statuses() []string {
	return []string{
		StatusActive,
		StatusInactive,
		StatusMaintenance,
		StatusDecommissioned,
		StatusUnknown,
	}
}

// IsValidStatus reports whether the given value is a valid CI status.
func IsValidStatus(value string) bool {
	for _, s := range Statuses() {
		if s == value {
			return true
		}
	}
	return false
}

// Discovery source values a CI may carry (CI-01). Mirrors the
// `ci_discovery_source_check` database constraint (migration 000108).
const (
	SourceSNMP    = "snmp"
	SourceSSH     = "ssh"
	SourceRedfish = "redfish"
	SourceIPMI    = "ipmi"
	SourceWMI     = "wmi"
	SourceAPI     = "api"
	SourceAgent   = "agent"
	SourceSweep   = "sweep"
	SourceManual  = "manual"
	// SourceImport marks a CI created by a user's bulk import.
	SourceImport = "import"
)

// DiscoverySources returns every valid CI discovery source.
func DiscoverySources() []string {
	return []string{
		SourceSNMP,
		SourceSSH,
		SourceRedfish,
		SourceIPMI,
		SourceWMI,
		SourceAPI,
		SourceAgent,
		SourceSweep,
		SourceManual,
		SourceImport,
	}
}

// IsValidDiscoverySource reports whether the given value is a valid discovery source.
func IsValidDiscoverySource(value string) bool {
	for _, s := range DiscoverySources() {
		if s == value {
			return true
		}
	}
	return false
}

// IsManualSource reports whether a source is a person's write: manual
// entry or import. CIs created from it are manual (is_manual).
func IsManualSource(value string) bool {
	return value == SourceManual || value == SourceImport
}
