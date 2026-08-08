// Package main is the canonical entry point for the Reticora Collector
// binary. All logic lives in the collectorcmd package.
package main

import "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/collectorcmd"

func main() {
	collectorcmd.Main()
}
