module github.com/DataHub-Chiemgau/Reticora-CMDB/backend

go 1.26.0

toolchain go1.26.11

replace github.com/DataHub-Chiemgau/Reticora-CMDB/collector => ../collector

replace github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore => ../edgecore

require (
	codeberg.org/go-pdf/fpdf v0.12.0
	github.com/99designs/gqlgen v0.17.93
	github.com/DataHub-Chiemgau/Reticora-CMDB/collector v0.0.0-00010101000000-000000000000
	github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore v0.0.0-00010101000000-000000000000
	github.com/ProtonMail/go-crypto v1.5.2
	github.com/boombuler/barcode v1.1.0
	github.com/go-chi/chi/v5 v5.5.0
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.13.0
	github.com/makiuchi-d/gozxing v0.1.1
	github.com/nats-io/jwt/v2 v2.7.4
	github.com/nats-io/nats.go v1.54.0
	github.com/nats-io/nkeys v0.4.16
	github.com/prometheus/client_golang v1.24.1
	github.com/redis/go-redis/v9 v9.21.0
	github.com/vektah/gqlparser/v2 v2.5.35
	github.com/xuri/excelize/v2 v2.11.1
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/sdk/metric v1.47.0
	golang.org/x/text v0.44.0
	golang.org/x/tools v0.52.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/agnivade/levenshtein v1.2.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/gosnmp/gosnmp v1.47.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.31.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/richardlehane/mscfb v1.0.9 // indirect
	github.com/richardlehane/msoleps v1.0.6 // indirect
	github.com/sosodev/duration v1.4.0 // indirect
	github.com/tiendc/go-deepcopy v1.7.2 // indirect
	github.com/xuri/efp v0.0.3-0.20260924042702-217b81952dec // indirect
	github.com/xuri/nfp v0.0.2-0.20250530014748-2ddeb826f9a9 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/image v0.48.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/grpc v1.84.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

// Graph-Pins (Update-Plan Upd-03, 2026-11-13): Diese Module stehen nur im
// Modulgraphen (go.mod von google.golang.org/grpc v1.84.0 bzw.
// golang.org/x/tools v0.52.0), kein Paket davon wird gebaut ("go mod why -m":
// main module does not need module). Die versionierten Replaces heben genau
// die verwundbaren Graph-Versionen auf die gefixten an, damit Scanner über
// "go list -m all" sauber bleiben. Sie greifen nur für exakt diese Version und
// werden wirkungslos, sobald grpc bzw. x/tools höher ziehen; bei jedem Update
// dieser Module prüfen (OSV über "go list -m all") und ggf. anpassen.
//   go-jose/v4 v4.1.4: GHSA-mgcc-cww5-fp2q (high), GHSA-4w3g-jfj2-cw2q,
//     GHSA-vvm8-gf78-28g4, GHSA-c67m-wr4h-2ccg, GHSA-v6h2-qj3f-2h6v, ... (Fix 4.1.7)
//   goldmark v1.4.13: GO-2026-5320 / CVE-2026-5160 (Fix 1.7.17)
replace (
	github.com/go-jose/go-jose/v4 v4.1.4 => github.com/go-jose/go-jose/v4 v4.1.9
	github.com/yuin/goldmark v1.4.13 => github.com/yuin/goldmark v1.8.7
)
