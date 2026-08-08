// Package main is a thin compatibility shim. The canonical collector entry
// point lives in the collector module at collector/cmd/collector; this shim
// keeps `go build ./cmd/collector` inside the backend module working and
// forwards to the same implementation.
package main

import (
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/collectorcmd"
)

func main() {
	collectorcmd.Main()
}
