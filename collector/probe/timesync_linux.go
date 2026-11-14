//go:build linux

package probe

import (
	"syscall"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// staUnsync is STA_UNSYNC of <linux/timex.h>: the clock is not synchronized.
const staUnsync = 0x0040

// systemTimeSync reads the synchronization of the clock with adjtimex
// (modes 0: read only).
func systemTimeSync() string {
	var tx syscall.Timex
	if _, err := syscall.Adjtimex(&tx); err != nil {
		return probewire.TimeUnknown
	}
	if tx.Status&staUnsync != 0 {
		return probewire.TimeUnsynchronized
	}
	return probewire.TimeSynchronized
}
