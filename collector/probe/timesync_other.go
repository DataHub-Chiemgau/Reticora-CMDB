//go:build !linux

package probe

import "github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"

// systemTimeSync is unknown outside Linux (no adjtimex).
func systemTimeSync() string { return probewire.TimeUnknown }
