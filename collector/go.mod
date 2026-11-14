module github.com/DataHub-Chiemgau/Reticora-CMDB/collector

go 1.26.0

replace github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore => ../edgecore

require (
	github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore v0.0.0-00010101000000-000000000000
	github.com/gosnmp/gosnmp v1.47.0
	github.com/nats-io/jwt/v2 v2.7.4
	github.com/nats-io/nats.go v1.54.0
	github.com/nats-io/nkeys v0.4.16
	github.com/openconfig/gnmi v0.14.1
	golang.org/x/crypto v0.59.0
	golang.org/x/net v0.62.0
	google.golang.org/grpc v1.84.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

// Graph-Pins (Update-Plan Upd-03, 2026-11-13): Diese Module stehen nur im
// Modulgraphen (go.mod von google.golang.org/grpc v1.84.0), kein Paket davon
// wird gebaut ("go mod why -m": main module does not need module). Die
// versionierten Replaces heben genau die verwundbaren Graph-Versionen auf die
// gefixten an, damit Scanner über "go list -m all" sauber bleiben. Sie greifen
// nur für exakt diese Version und werden wirkungslos, sobald grpc höher zieht;
// bei jedem grpc-Update prüfen (OSV über "go list -m all") und ggf. anpassen.
//   go-jose/v4 v4.1.4: GHSA-mgcc-cww5-fp2q (high), GHSA-4w3g-jfj2-cw2q,
//     GHSA-vvm8-gf78-28g4, GHSA-c67m-wr4h-2ccg, GHSA-v6h2-qj3f-2h6v, ... (Fix 4.1.7)
//   otel v1.44.0: GO-2026-6505/GHSA-8wmf-6v46-5gfg, GHSA-q7gw-24fr-5p69,
//     GHSA-rrfc-gjm9-cgrj (Fix 1.47.0)
replace (
	github.com/go-jose/go-jose/v4 v4.1.4 => github.com/go-jose/go-jose/v4 v4.1.9
	go.opentelemetry.io/otel v1.44.0 => go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/metric v1.44.0 => go.opentelemetry.io/otel/metric v1.47.0
	go.opentelemetry.io/otel/sdk v1.44.0 => go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/sdk/metric v1.44.0 => go.opentelemetry.io/otel/sdk/metric v1.47.0
	go.opentelemetry.io/otel/trace v1.44.0 => go.opentelemetry.io/otel/trace v1.47.0
)
