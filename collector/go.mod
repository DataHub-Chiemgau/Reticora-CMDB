module github.com/DataHub-Chiemgau/Reticora-CMDB/collector

go 1.26.0

replace github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore => ../edgecore

require (
	github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore v0.0.0-00010101000000-000000000000
	github.com/nats-io/nats.go v1.54.0
	golang.org/x/net v0.60.0
)

require (
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)
