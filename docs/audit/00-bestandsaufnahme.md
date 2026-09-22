# Teil 0 – Bestandsaufnahme

## Stand

ABGESCHLOSSEN – statische Bestandsaufnahme und angeforderte Go-Prüfläufe dokumentiert; Backend-Gesamtprüfung wegen fehlender Offline-Abhängigkeit N/P, Einzelprüfung der Anforderungen folgt in Teilen 1–9.
Prüfdatum: 2026-09-22; Produktstand: `b0f0ee2302e3af5afa41fbeaf738687fe1492504`. Pfade sind relativ zu `/home/runner/work/Reticora-CMDB/Reticora-CMDB/`. Dies ist eine Quellenbestandsaufnahme, keine v3-Konformitäts- oder Gate-Abnahme.
Dokumentprüfung: Änderungsumfang ausschließlich vier neue Markdown-Dateien unter `docs/audit/` und `docs/spec/`; Secrets-Scan ohne Treffer, unabhängiges statisches Gegenlesen ohne Befund. `parallel_validation` wurde aufgerufen: automatischer Code-Review wegen nicht verfügbarem Modell nicht ausführbar; CodeQL bei reinen Dokumentänderungen übersprungen. Dies ersetzt keine Produkt-Sicherheitsprüfung.

## 1. Repo-Landkarte

| Pfad | Zweck / Iststand |
|---|---|
| `.github/workflows/` | Drei Workflow-Dateien für CI, Installations-Smoke-Test und Restore-Test. |
| `api/` | REST-Vertrag `api/openapi.yaml`; Generator-Konfiguration im Root `oapi-codegen.yaml`. |
| `backend/` | Go-API, Domänen, SQL-Migrationen und fünf ausführbare Programme unter `backend/cmd/`. |
| `collector/` | Eigenes Go-Modul für Discovery-CLI, Protokoll-Plugins und Geräteprofile. |
| `edgecore/` | Eigenes Go-Modul für gemeinsame Edge-Funktionen; kein `cmd/` oder `internal/`. |
| `frontend/` | React-SPA, TypeScript, Vite, Komponenten, Seiten und Browser-/Unit-Tests. |
| `deploy/` | Compose, Kustomize, Keycloak-Realm, Terraform, Grafana-Dashboards und Monitoring-Regeln. |
| `docs/` | Bestehende Architektur-/Betriebsdokumente; neu ausschließlich `docs/audit/` und `docs/spec/`. |
| `tests/` | Shell-Tests des Cloud-Installers in `tests/install-cloud-helpers.test.sh`. |
| `.git/` | Lokale Versionsverwaltungsmetadaten, kein Produktmodul. |
| `go.work`, `Makefile` | Workspace aus drei Modulen; Build-, Test-, Lint-, Generierungs- und Migrationsziele. |
| `install.sh`, `install-cloud.sh`, `install-vm.sh` | Dispatcher (`install.sh:233–234`) für zentralen Compose-Stack bzw. VM/Collector-Installation. |
| `docker-compose.yml`, `deploy/docker-compose/docker-compose.yml` | Root: lokale Infrastrukturdienste; Deploy: vollständiger Stack samt Server/Frontend; Source-Build-Override in `deploy/docker-compose/docker-compose.override.yml`. |
| `deploy/k8s/base/`, `deploy/k8s/overlays/{staging,prod}/` | Deployment, Service, ServiceAccount, HPA; optionale Komponenten `base/components/{backup,opensearch}/` und Kustomize-Overlays. |
| `server`, `.gitignore` | Eingechecktes Root-Binary (nicht als Buildbeleg verwendet) sowie Ignore-Regeln. |

### Go-Module und Pakete

Alle drei Modulpfade beginnen mit `github.com/DataHub-Chiemgau/Reticora-CMDB/`: `backend/go.mod` → `backend`, `collector/go.mod` → `collector`, `edgecore/go.mod` → `edgecore`; jeweils `go 1.25.0`. `go.work` bindet alle drei ein; lokale `replace`-Einträge verknüpfen Backend → Collector → Edgecore.

| Kommando-Paket | Zweck |
|---|---|
| `backend/cmd/server/` | `main` verdrahtet Konfiguration, Persistenz, Auth, Hintergrundarbeiter und HTTP-Server. |
| `backend/cmd/collector/` | `main` ist der Kompatibilitäts-Wrapper für `collectorcmd.Main`. |
| `backend/cmd/agent/` | `main` startet Endpoint-Telemetrie und Heartbeats, direkt per HTTP/mTLS oder Collector-Relay. |
| `backend/cmd/audit-seed/` | `main` erzeugt eine Organisation und Audit-Testeinträge für Restore-Prüfungen. |
| `backend/cmd/audit-verify/` | `main` verifiziert die Audit-Hashkette einer Organisation. |
| `collector/cmd/collector/` | `main` ist der kanonische Collector-Einstieg und ruft `collectorcmd.Main` auf. |

**Alle 72 Go-Pakete unter `backend/internal/`** (Pfadpräfix gilt für jede Zeile; Verzeichnisse ohne eigene Go-Dateien sind keine zusätzlichen Pakete):

| Paketpfad unter `backend/internal/` | Zweck |
|---|---|
| `agent/` | Verwaltet Endpoint-Agenten, Richtlinien und Telemetrie-Ingest. |
| `ai/` | Enthält Provider-Anbindung, Konversationen, Retrieval und CI-Chunk-Indexierung. |
| `api/` | Definiert HTTP-Fehler, DB-Fehlerabbildung, Pagination und Cursor. |
| `api/generated/` | Ist derzeit nur ein Paketplatzhalter (`doc.go`), ohne generierte Go-Modelle. |
| `api/speccheck/` | Liest dokumentierte OpenAPI-Operationen für den Routenabgleich. |
| `asset/` | Verwaltet Assets einschließlich Labels und CI-/Kompositionsbezug. |
| `assignment/` | Verwaltet Ausgaben und Rückgaben zugewiesener Objekte. |
| `audit/` | Schreibt und verifiziert persistente, verkettete Audit-Einträge. |
| `cache/` | Bietet austauschbare Memory-/Redis-Stores. |
| `ci/` | Verwaltet CIs mit Validierung, Limits und Indexierungs-Wrappern. |
| `citype/` | Verwaltet CI-Typen, Attribute, Templates und Feldauflösung. |
| `compliance/` | Evaluiert Compliance-Regeln und erzeugt Berichte. |
| `composition/` | Verwaltet Parent-/Child-Zuordnungen von Assets und CIs. |
| `config/` | Liest die Serverkonfiguration aus Umgebungsvariablen. |
| `consumable/` | Verwaltet Verbrauchsmaterial und Bestandsbewegungen. |
| `contact/` | Verwaltet Kontakte und CI-Kontaktzuordnungen. |
| `credential/` | Verwaltet verschlüsselte Discovery-Zugangsdaten. |
| `database/` | Stellt pgx-Pools, Rollenprüfung und tenantgebundene Transaktionen bereit. |
| `desk/` | Verwaltet Arbeitsplätze und Buchungen. |
| `discovery/` | Verarbeitet Collector-Registrierung, Enrollment, Ingest, Identitätsabgleich, Jobs und Review-Items. |
| `disposal/` | Verwaltet Entsorgungsnachweise. |
| `document/` | Verwaltet Dokumentmetadaten, Verknüpfungen und Inhalte. |
| `entitlement/` | Berechnet Plan-/Featurefreigaben und HTTP-Entitlement-Gates. |
| `export/` | Rendert direkte Exporte und verarbeitet persistente Exportjobs. |
| `fieldmeta/` | Beschreibt Feldmetadaten für API/UI. |
| `form/` | Verwaltet Formularversionen und validiert Einreichungen. |
| `graphqlbff/` | Implementiert einen eigenen Query-Parser und vier BFF-Resolver. |
| `history/` | Liest die objektbezogene Änderungshistorie. |
| `identity/` | Implementiert OIDC, signierte Sitzungen, API-Keys, Principals und Permission-Typen. |
| `iga/` | Enthält Connector-, JML-, Provisionierungs-, Review-, Reconciliation- und SCIM-Funktionen. |
| `ipam/` | Verwaltet Interfaces, Subnetze, IP-Adressen und Kabel. |
| `keymgmt/` | Verwaltet physische Schlüssel und deren Ausgaben, nicht den kryptographischen Schlüsselspeicher. |
| `lifecycle/` | Verwaltet Zustandsdefinitionen und führt Lifecycle-Transitionen aus. |
| `location/` | Verwaltet aufgezeichnete Asset-Positionen. |
| `locationnode/` | Verwaltet den zusätzlichen hierarchischen Location-Knotenbestand. |
| `maintenance/` | Verwaltet Wartungsfenster, CI-Zuordnungen und Benachrichtigungsdatensätze. |
| `middleware/` | Implementiert HTTP-Querschnittsfunktionen wie Auth, Tenant-Kontext, Limits und Idempotenz. |
| `monitoring/` | Speichert Metriken und wertet Alarmregeln aus. |
| `movement/` | Verwaltet Asset-Bewegungen und Mengeninventar. |
| `observability/` | Initialisiert OpenTelemetry für den laufenden Server. |
| `order/` | Verwaltet interne Bestellungen und Positionen. |
| `override/` | Verwaltet Feldprovenienz, Overrides und Quellprioritätsregeln. |
| `permission/` | Definiert den Anwendungskatalog und verwaltet Rollen-/Permission-Zuordnungen. |
| `platform/audit/` | Bietet eine separate speicherbasierte Audit-Writer-/Reader-Abstraktion. |
| `platform/blob/` | Abstrahiert dateibasierten und S3-Objektspeicher. |
| `platform/crypto/` | Implementiert Envelope-Verschlüsselung. |
| `platform/db/` | Bietet einen weiteren pgx-Pool und `WithTenant` mit optionaler User-ID. |
| `platform/events/` | Definiert Event-Publisher, Noop- und NATS-Adapter. |
| `platform/httpx/` | Bietet HTTP-Hilfen für Problemantworten. |
| `platform/redis/` | Kapselt die Redis-Verbindung. |
| `platform/telemetry/` | Bietet eine weitere OpenTelemetry-Initialisierung. |
| `privacy/` | Verwaltet Retention und Lösch-/Anonymisierungsabläufe für Kontakte und Nutzer. |
| `rack/` | Verwaltet Rackbelegungen. |
| `relationship/` | Verwaltet CI-Beziehungen. |
| `relationshiptype/` | Verwaltet den erweiterbaren Beziehungstypkatalog. |
| `reservation/` | Verwaltet Reservierungen, Verfügbarkeit und Ablaufbereinigung. |
| `savedview/` | Verwaltet gespeicherte Ansichten und JSON-Filterabfragen. |
| `search/` | Bietet PostgreSQL-, OpenSearch- und Hybrid-Suche mit CI-Indexierung. |
| `security/` | Verwaltet Sicherheitsfindings. |
| `server/` | Verdrahtet Repositories, Domänenhandler, Routen und Route-Permissions. |
| `sla/` | Verwaltet SLA-Regeln und berechnet Fristen mit Kalendern. |
| `stocktake/` | Verwaltet Inventuren, Scans, Differenzen und Abschlusskorrekturen. |
| `tenant/` | Hält den Tenant-Kontext einschließlich Client-Scope. |
| `tenant/rls/` | Bietet zusätzliche `database/sql`-Hilfen für RLS-Kontext und Transaktionen. |
| `tenantapi/` | Verwaltet Clients, Sites, Buildings und Rooms. |
| `ticket/` | Verwaltet Tickets und Kommentare. |
| `topology/` | Berechnet Graphansichten und Impact-/Blast-Radius-Abfragen. |
| `training/` | Verwaltet Schulungen und Nutzerzuweisungen. |
| `user/` | Verwaltet Nutzer, Teams, Rollen, Einladungen und API-Keys. |
| `webhook/` | Verwaltet Subscriptions, Auslieferungen, Retries und Dead Letters. |
| `wire/` | Definiert gemeinsame Ingest-Payload-Datentypen. |
| `workflow/` | Verwaltet Definitionen/Runs/Schritte und führt Aktionen sowie Genehmigungen aus. |

Collector außerhalb `internal/`: `collector/collectorcmd/` steuert Discovery, Upload, Spool, Heartbeat, Trap-Empfang, Metriken und Agent-Relay (`collectorcmd.go`). `collector/plugins/` definiert die Plugin-Schnittstelle; `collector/plugins/{sweep,snmp,ssh,redfish,ipmi,wmi,nas,power}/` implementieren jeweils Netzsuche, SNMP, SSH, Redfish, IPMI, WMI, NAS- und Stromgeräteabfragen. `collector/profiles/` lädt und klassifiziert eingebettete JSON-Profile aus `collector/profiles/data/` (`profiles.go:15–55`); kein `collector/internal/`.
Edgecore: `edgecore/buffer/` persistiert den Offline-Spool; `edgecore/enrollment/` behandelt PKI/Enrollment; `edgecore/keystore/` speichert Schlüsselmaterial; `edgecore/transport/` kapselt HTTP-/Dial-mTLS; `edgecore/update/` verarbeitet signierte Updates. Der Agent selbst liegt in `backend/cmd/agent/main.go` (`runTelemetryLoop`, `collectAndSend`), seine API unter `backend/internal/agent/`, nicht in einem eigenen Agent-Go-Modul.

### Frontend

`frontend/package.json`: React/TypeScript-SPA, Entwicklung `vite`, Build `tsc -b && vite build`; `frontend/vite.config.ts` setzt Alias `@` auf `src` und `/api`-Proxy auf Port 8080. Einstiegspunkte: `frontend/index.html`, `frontend/src/main.tsx`, `frontend/src/App.tsx`.
`frontend/src/api/` enthält HTTP-Client und generierte OpenAPI-Typen (`api/generated/schema.d.ts`); `auth/` OIDC und Sitzungen; `components/{ui,cmdb,form,graph,rack}/` die Bausteine; `pages/` und `pages/auth/` Fachseiten; `hooks/`, `stores/`, `lib/` gemeinsame Logik.
`frontend/src/i18n/{index.ts,de-DE.json,en-US.json}` enthält Initialisierung und beide Sprachkataloge; `frontend/src/test/setup.ts`, `frontend/e2e/` und `frontend/src/**/*.test.{ts,tsx}` enthalten Testinfrastruktur/-fälle. Containerbetrieb: `frontend/Dockerfile`, `frontend/nginx.conf`, `frontend/keycloak-proxy.conf`.

## 2. Zentrale Einstiegspunkte

| Oberfläche | Pfad und Symbol / relevante Grenze |
|---|---|
| Router / Registrierung | `backend/internal/server/router.go:185` `NewRouter`, `registrar.RegisterRoutes`; `backend/internal/server/authz.go:297` `authorizingRouter` umschließt Domänenregistrierung. Repository-Verdrahtung: `backend/internal/server/repositories.go` `PostgresRepositories`/`MemoryRepositories`. |
| Middleware-Kette | `backend/cmd/server/main.go:307–319` `middleware.Chain`: RequestID → Recovery → Logger → SecurityHeaders → OpenAPIValidation → AuthWithAPIKeys → TenantMiddleware → httpMetrics → Entitlement → RateLimiterWithStore → IdempotencyWithStore → Router/Route-Authz. |
| Route → Permission | `backend/internal/server/authz.go:181,264` `PermissionForRoute`, `AuthorizeRoute`; Ressourcenpräfix plus HTTP-Methode und Ausnahmen für öffentliche Pfade/Agent-Telemetrie, unbekannte API-Zuordnungen fail-closed. Kein Paket `internal/authz/`. |
| Tenant / RLS | `backend/internal/database/pool.go:153` `WithTenant` setzt transaktionslokal `app.org_id` und optional `app.client_scope`; `NewPool`/`VerifyRLSEnforced` prüfen die effektive Rolle. Weitere DB-Varianten: `backend/internal/platform/db/db.go:41`, `backend/internal/tenant/rls/rls.go:60`; `backend/internal/tenant/tenant.go:18` `WithTenant` setzt dagegen nur den Go-Kontext. |
| Migrationen | `backend/migrations/000001_tenant_model.up.sql` bis `000057_composition_acyclic.up.sql`; 57 Paare, vollständige Tabellen-/Policy-Inventur in `docs/audit/00-schema-ist.md`. Ausführung über `Makefile:86–98`. |
| Ingest-Konsument | HTTP-Handler `backend/internal/discovery/discovery.go:415,612` `RegisterRoutes`/`BulkIngest`: `/api/v1/ingest/bulk` und `/api/v1/discovery/ingest`; Collector-Aufruf in `collector/collectorcmd/collectorcmd.go:377` `postPayload`. Kein NATS-Ingest-Subscriber in den untersuchten Backend-/Collector-/Edgecore-Quellen gefunden; `backend/internal/platform/events/nats.go` ist ein Publisher-Adapter. |
| Identity-Resolution / Reconciliation | `backend/internal/discovery/reconciliation.go:73,141` `Reconcile`/`ShouldApplyAttribute`, `backend/internal/discovery/discovery.go:874` `applySourceTrust`; Topologieableitung `backend/internal/discovery/topology.go:79` `DeriveRelationships`. |
| Review-Items / Overrides | `backend/internal/discovery/review.go:28,230,258` `ReviewItem`, `ListReviewItems`, `ResolveReviewItem`; PG-Persistenz `backend/internal/discovery/pg_repository.go`; Provenienz-Port `discovery.go:283` `ProvenanceRecorder`, Adapter `backend/internal/server/report_adapters.go` `overrideProvenance`, Speicher `backend/internal/override/`. |
| Jobs / Scheduler | `backend/internal/export/worker.go:128` `JobWorker.Run` (Start `backend/cmd/server/main.go:266–273`); `backend/internal/monitoring/evaluator.go:55` `Evaluator.Run`; `backend/internal/reservation/sweeper.go` `Sweeper.Run`. Collector-Ticker: `collector/collectorcmd/collectorcmd.go:251` `runDiscoveryLoop`; Discovery-Job-API: `backend/internal/discovery/jobs.go` `CreateJob`/`ListJobs`; kein zentraler Universal-Scheduler identifiziert. |
| Audit-Runner / Outbox | Vorhanden: `backend/internal/audit/audit.go:123,136,202` `AcquireAdvisoryLock`, `PGRecorder.Record`, `Verify` (Schreiben innerhalb der Aufrufertransaktion); `backend/cmd/audit-verify/main.go` `main`. Keine Audit-Outbox-Tabelle oder separater Audit-Runner in `backend/migrations/` bzw. `backend/{cmd,internal}/` gefunden; README-Soll nicht mit diesem Schreibweg gleichsetzen. |
| Webhook-Dispatcher | `backend/internal/webhook/dispatcher.go:88,140,309` `NewDispatcher`, `Dispatch`, `ProcessDue`; `backend/internal/webhook/pg_delivery_store.go` persistiert Delivery-/Dead-Letter-Zustand; Start in `backend/cmd/server/main.go:234`. |
| Notifier | `backend/internal/monitoring/monitoring.go:80` `Notifier.NotifyAlert`; `backend/internal/monitoring/evaluator.go:36` `NewEvaluator` ersetzt nil durch slog-Logging. Genau dieser Default ist in `backend/cmd/server/main.go:280` verdrahtet, kein dort angeschlossener E-Mail-Provider. |
| Entitlement-Middleware | `backend/internal/entitlement/middleware.go:54,71` `RequiredFeature`, `Service.Middleware`; Initialisierung und Enforce-Schalter in `backend/cmd/server/main.go:227–232`. |
| GraphQL-Schema / Resolver | `backend/internal/graphqlbff/graphqlbff.go:22–35,146–175,234–266` `Schema`, `QueryResolver`, `registerQueries`, `resolveCIs`, `resolveCI`, `resolveRelationships`, `resolveCurrentUser`; Schema als Go-Registry, keine separate SDL-Datei im Paket/`api/`. |
| REST / i18n | `api/openapi.yaml`; Typgenerierung in `frontend/package.json` `generate:api`; `frontend/src/i18n/index.ts` und `de-DE.json`/`en-US.json` für Anzeigen. |
| Seed / Simulation | `backend/migrations/000054_default_organization.up.sql` sät Demo-Org; Typ-/Rollen-Seeds siehe Schema-Bericht; `backend/cmd/audit-seed/main.go` `main` ist nur Audit-Fixture. `Makefile:112–114` referenziert das **fehlende** `backend/migrations/seed.sql`; kein 30-Geräte-Ingest-Simulationsmodus in `backend/`, `collector/`, `edgecore/` gefunden (nicht mit Topologie-Impact-Simulation verwechseln). |

## 3. Technologie-Ist

| Bereich | Nachweisbarer Stand / Quelle |
|---|---|
| Go | Sprach-/Modulvorgabe **1.25.0** in `go.work`, `backend/go.mod`, `collector/go.mod`, `edgecore/go.mod`; `.github/workflows/ci.yml` nutzt Go 1.25. Lokale Prüfläufe mit vorinstalliertem Go **1.25.14**. |
| Backend-Bibliotheken | `backend/go.mod`: chi **5.3.1**, pgx **5.10.0**, go-redis **9.21.0**, Prometheus-Client **1.24.1**, OpenTelemetry **1.44.0**. |
| PostgreSQL / Extensions | Beide Compose-Dateien: `timescale/timescaledb:latest-pg16` (PG16, Timescale-Patchstand nicht fixiert); Deploy-Keycloak-DB: `postgres:16-alpine`. CI/Restore fixieren `2.17.2-pg16`. Migrationen aktivieren `pgcrypto` (`000001_tenant_model.up.sql`, erneut `000039_audit_canonical_hash.up.sql`), `timescaledb` (`000020_metrics.up.sql`), `btree_gist` (`000021_spec_alignment.up.sql`), jeweils unter `backend/migrations/`. |
| Messaging | Beide Compose-Dateien: `nats:2.10-alpine` mit `--jetstream`; `backend/internal/platform/events/nats.go` enthält `NATSPublisher` mit injizierbarem `Conn` und lokalem Puffer. Image-/Adapterexistenz belegt keinen produktiven Ingest-Konsumenten. |
| Cache | Beide Compose-Dateien: `redis:7-alpine`; Adapter in `backend/internal/cache/redis.go` und `backend/internal/platform/redis/`. |
| Auth | Beide Compose-Dateien: `quay.io/keycloak/keycloak:24.0`; Realm `deploy/keycloak/realm-reticora.json`. Root-Compose nutzt `start-dev`/`dev-mem`, Deploy-Compose separate PG16-Datenbank. |
| Objektspeicher | Beide Compose-Dateien: `minio/minio:latest` (nicht versioniert); S3-/Blob-Abstraktion unter `backend/internal/platform/blob/`. |
| Suche / Betrieb | `deploy/docker-compose/docker-compose.yml`: OpenSearch **2.13.0**, Certbot **2.11.0**; `deploy/{grafana,monitoring,terraform}/` für Dashboards, SLO-Regeln, Infrastruktur. |
| Frontend-Kern | Lockstand aus `frontend/package-lock.json`: React/React DOM **19.2.7**, TypeScript **5.7.3**, Vite **6.4.3**, React Router **6.30.4**, TanStack Query **5.101.2**, Zustand **5.0.14**. |
| Frontend-UI | Derselbe Lockstand: i18next **24.2.3**, react-i18next **15.7.4**, Sigma **3.0.3**, Graphology **0.25.4**, React Hook Form **7.81.0**, Zod **4.4.3**, Tailwind **3.4.19**; außerdem Radix UI und Lucide (`frontend/package.json`). |
| Frontend-Qualität | Lockstand: Vitest **3.2.7**, Playwright **1.62.0**, ESLint **9.39.4**; CI nutzt Node **20** (`.github/workflows/ci.yml`). |

## 4. Tests und Qualität

### Testdateien (nicht Testfälle)

Zählbasis: eingecheckte `*_test.go` sowie die Include-Muster aus `frontend/vitest.config.ts:16` und `frontend/playwright.config.ts:4`. Kategorien sind **disjunkt**, keine vorgetäuschte Coverage: Integration zuerst (acht unten genannte DB-Dateien), danach `*repository_test.go`, danach `*handler*_test.go`, übrige Go-Dateien als Unit/sonstige. Manche Unit-Dateien enthalten ebenfalls HTTP-Handler-Tests.

| Art | `backend/` | `collector/` | `edgecore/` | `frontend/` |
|---|---:|---:|---:|---:|
| Unit / sonstige Go-Tests | 64 | 11 | 5 | – |
| Repository (ohne DB-Integration) | 11 | 0 | 0 | – |
| Handler (Dateinamenkategorie) | 16 | 0 | 0 | – |
| PostgreSQL-Integration | 8 | 0 | 0 | – |
| Playwright (`frontend/e2e/*.spec.ts`) | – | – | – | 3 |
| Vitest (`frontend/src/**/*.test.{ts,tsx}`) | – | – | – | 33 |
| Summe | 99 | 11 | 5 | 36 |

Go-Tests liegen paketnah unter `backend/internal/`, zusätzlich `backend/cmd/server/main_test.go`, `collector/{collectorcmd,plugins,profiles}/` und `edgecore/{buffer,enrollment,keystore,transport,update}/`; insgesamt **115 Go-Testdateien**. Zusätzlich **1 Shell-Testdatei**: `tests/install-cloud-helpers.test.sh`.
Die acht DB-Testdateien: `backend/internal/database/{integration,rls_isolation,traversal_dlq}_test.go`, `backend/internal/audit/pg_roundtrip_integration_test.go`, `backend/internal/composition/cycle_integration_test.go`, `backend/internal/relationship/pg_repository_integration_test.go`, `backend/internal/stocktake/pg_repository_test.go`, `backend/internal/tenant/rls/isolation_integration_test.go`.
Sie benötigen `TEST_DATABASE_URL` und ein migriertes PostgreSQL; der Skip erfolgt über diese Variable, **nicht** als allgemeine Garantie durch `-short` (z. B. `backend/internal/database/integration_test.go:15–24`). Audit-/RLS-Fixtures nutzen privilegierte Maintenance-Verbindungen und sessionspezifisch `session_replication_role = replica` (`backend/internal/audit/pg_roundtrip_integration_test.go:35–60`, `backend/internal/tenant/rls/isolation_integration_test.go:46–74`).

### CI-Workflows (Definition, kein Nachweis eines grünen Remote-Laufs)

| Workflow | Jobs und tatsächliche Schritte |
|---|---|
| `.github/workflows/ci.yml` | `backend`: Download, Build, `go test -race -coverprofile=coverage.out ./...`, Vet, Coverage-Artefakt; `migrations`: Timescale PG16, migrate up/down/up, `go test -run 'Integration\|PGRepository\|Migrations\|RLS' ./...`, RLS-Variablen-/FORCE-Abfragen; `lint-backend`: golangci-lint-action v8 (`latest`); `frontend`: npm ci, ESLint, Prettier, `generate:api:check`, Typecheck, Vitest, Build; `e2e`: statischer Build/Serve und Playwright Chromium; `k8s-manifests`: Kustomize-Build für Base, Staging, Prod. |
| `.github/workflows/install-smoke.yml` | `install-cloud-smoke`: `./install.sh --cloud --non-interactive`, Container-Health, Backend `/healthz`, Frontend, Keycloak-OIDC, Cleanup; pfadgefiltert auf Installer/Deploy/Dockerfiles/Workflow. |
| `.github/workflows/restore-test.yml` | `restore-test`: täglich 03:00 UTC bzw. manuell; Migrationen, `audit-seed`, `audit-verify`, pg_dump/pg_restore, Zeilenzählung und erneute Hashkettenprüfung. |

`ci.yml` enthält keinen separaten Collector-/Edgecore-Testjob und keinen Traceability-CSV-Job; `Makefile` bietet dagegen `test-collector`. Der Namensfilter des Migrationsjobs ist nicht gleichbedeutend mit Ausführung jeder DB-Testfunktion; `.github/workflows/restore-test.yml` prüft zusätzlich die Audit-Kette.
Lint-Konfiguration: `backend/.golangci.yml` (`readonly`, 5 min; u. a. errcheck, govet, staticcheck, gofmt, goimports, revive; Ausnahmen für Tests/cmd); keine Collector-Konfiguration, `Makefile:69–71` maskiert dort Linterfehler. `frontend/eslint.config.js`: JS-/TS-Empfehlungen, React-Hooks; `frontend/.prettierrc.json`, `frontend/.prettierignore`: Formatregeln/Ausschlüsse. `frontend/playwright.config.ts` definiert Chromium **und** Firefox, CI führt nur Chromium aus.

### Ausgeführte Go-Prüfläufe

Am 2026-09-22 jeweils im Modulverzeichnis `backend/`, `collector/`, `edgecore/`: **`go build ./...`** und **`go test -short ./...`**. Verwendet wurde `/opt/hostedtoolcache/go/1.25.14/x64/bin/go` (`go1.25.14 linux/amd64`) statt des zu alten `/usr/bin/go` (1.24.13).
Umgebung: `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly`, `TEST_DATABASE_URL` nicht gesetzt. Damit keine Abhängigkeits-/Toolchain-Installation und keine externe Testdatenbank; keine Dienste gestartet. Root ist nur Workspace (`go.work`), die Prüfkommandos gehören in die drei Module.

| Modulpfad | Build | Short-Test | Aussage |
|---|---|---|---|
| `backend/` | **ROT**, Exit 1 | **ROT**, Exit 1 | Offline-Modulauflösung blockiert: `github.com/go-chi/chi/v5@v5.3.1: module lookup disabled by GOPROXY=off`; 64 Pakete `[setup failed]`, 5 Testpakete grün, 8 ohne Testdateien. Kein belegter fehlgeschlagener fachlicher Test; Backend-Gesamtfunktion **N/P** unter dieser Umgebung. |
| `collector/` | **GRÜN**, Exit 0 | **GRÜN**, Exit 0 | 10 Testpakete erfolgreich, `collector/cmd/collector` und `collector/plugins` ohne Testdateien. |
| `edgecore/` | **GRÜN**, Exit 0 | **GRÜN**, Exit 0 | Alle 5 Testpakete erfolgreich. |

Backend-Setupfehler betreffen `backend/cmd/{audit-seed,audit-verify,server}` und unter `backend/internal/`: `agent`, `ai`, `api`, `asset`, `assignment`, `audit`, `cache`, `ci`, `citype`, `compliance`, `composition`, `consumable`, `contact`, `credential`, `database`, `desk`, `discovery`, `disposal`, `document`, `entitlement`, `export`, `form`, `graphqlbff`, `history`, `identity`, `iga`, `ipam`, `keymgmt`, `lifecycle`, `location`, `locationnode`, `maintenance`, `middleware`, `monitoring`, `movement`, `observability`, `order`, `override`, `permission`, `platform/db`, `platform/redis`, `platform/telemetry`, `privacy`, `rack`, `relationship`, `relationshiptype`, `reservation`, `savedview`, `search`, `security`, `server`, `sla`, `stocktake`, `tenant/rls`, `tenantapi`, `ticket`, `topology`, `training`, `user`, `webhook`, `workflow`.
Erfolgreich im Backend: `backend/internal/{api/speccheck,config,fieldmeta,platform/crypto,tenant}`. Rohlogs lagen während der Session unter `/tmp/reticora-audit-00/{backend,collector,edgecore}-{build,test}.log` (temporär, kein dauerhafter Repository-Beleg); Befund und Exitcodes sind hier gesichert. Frontend-, Migrations-, externe Integrations-, Installations- und Lasttests wurden nicht ausgeführt; kein Ergebnis wird dafür behauptet.

## 5. Vorhandene Dokumente und Anforderungsquellen

| Gesuchter Pfad / Quelle | Befund im geprüften Stand |
|---|---|
| `docs/schema-baseline.md` | **Nicht vorhanden**; Schema wird für dieses Audit neu aus `backend/migrations/` abgeleitet. |
| `docs/traceability.csv` | **Nicht vorhanden**; Zeilenzahl und Spalten **N/P**, nicht „0 Zeilen“. `README.md:352` behauptet eine Traceability-Datei/CI-Prüfung, ohne entsprechendes Artefakt im Checkout. |
| `docs/decisions/` | **Nicht vorhanden**; keine ADR-Dateien im Dokumentbestand gefunden. CH8–CH30 stehen nun wortgetreu in `docs/spec/katalog-v3/00-grundlagen.md`. |
| `docs/acceptance/` | **Nicht vorhanden**; Abnahmeszenarien werden lediglich in `README.md` §16 erwähnt. |
| `docs/ops/sizing.md` | **Nicht vorhanden**; numerische Zielwerte stehen in `README.md` §15, kein gemessener Sizing-Bericht. |
| v2-Katalog / vollständiger v3-Katalog | Kein entsprechendes Dokument unter den eingecheckten Dokumentdateien gefunden. `docs/spec/katalog-v3/00-grundlagen.md` enthält nur den in diesem Auftrag gelieferten Block; PRI-01–06/08/09 und GLO-01–09 bleiben mangels v2-Wortlaut ungeprüft. |
| `README.md` | Deutsche Produkt-/Anforderungsübersicht mit Sollsprache und Phasenplan (§17), aber kein vollständig ID-basierter v2-/v3-Katalog; nicht als Implementierungsbeweis verwenden. |
| `docs/README.md` | Architektur, Migrationen, Implementierungs-/Betriebshinweise; teilweise abweichend vom Code (z. B. React 18 in §Technology Stack gegenüber React 19 im Lockfile). |
| `docs/backup-dr.md`, `docs/opensearch.md`, `docs/troubleshooting-installation.md` | Backup/Restore, optionale Suche und Installer-Fehlerbehebung. |
| `deploy/README.md`, `deploy/keycloak/README.md`, `collector/profiles/README.md` | Deployment, Realm-Konfiguration und Discovery-Profilformat. |

Die Folgeteile benötigen zusätzlich den vollständigen ID-basierten Anforderungskatalog einschließlich v2-Referenzen. Aus den vorhandenen Übersichten werden keine fehlenden Anforderungen erfunden.

## 6. Evidenzbasierte Epic-Einschätzung

Zuordnung A–D nach `README.md` §17; E–H werden in `docs/README.md`/Betriebsdokumenten nur punktuell benannt, kein vollständiger Epic-Katalog liegt vor. Folgende Aussagen bewerten Implementierungsindizien, nicht die Erfüllung aller v3-IDs.

| Epic | Einschätzung (ohne Gate-Freigabe) |
|---|---|
| A – Installation | Installer, vollständiger Compose-Stack, Kustomize-Overlays und ein Installations-Smoke-Workflow sind vorhanden (`install*.sh`, `deploy/`, `.github/workflows/install-smoke.yml`); Installation/Betrieb wurden hier nicht ausgeführt. |
| B – Mandanten/Datenmodell/API | Breite produktive Verdrahtung ist in `backend/cmd/server/main.go`, `backend/internal/{database,identity,entitlement,server}/` und `backend/migrations/` belegt; der Schema-Bericht weist aber keine Site-/Team-Scope-Policies nach und die Backend-Gesamtprüfung ist lokal blockiert. |
| C – Frontend/Graph | React-Seiten und Graphbausteine sowie Topologie-/Impact-Code und vier GraphQL-Resolver bestehen (`frontend/src/{pages,components/graph}/`, `backend/internal/{topology,graphqlbff}/`); Frontend-/Browser-Tests wurden hier nicht ausgeführt, daher keine UX-/Gate-Abnahme. |
| D – Collector/Discovery/Reconciliation | Plugins, HTTP-Ingest, Reconciliation und Review-Persistenz sind vorhanden (`collector/`, `backend/internal/discovery/`); Collector-/Edgecore-Short-Tests sind grün, ein echter Ende-zu-Ende-Ingest mit Backend/DB und der behauptete Simulationsmodus sind damit nicht belegt. |
| E – Monitoring/Agent | `docs/README.md` benennt Metrikpolling/Alerting als Epic E; Code existiert in `backend/internal/monitoring/`, `backend/cmd/agent/main.go`, `collector/collectorcmd/collectorcmd.go`, der verdrahtete Notifier protokolliert nur. |
| F – spätere Fachmodule/Add-ons (Zuordnung offen) | Eine verbindliche F-Abgrenzung fehlt im vorhandenen Katalogbestand; späterer Fachcode ist jedoch in `backend/internal/{asset,stocktake,ticket,workflow,compliance,iga,ai}/` vorhanden und daher nicht pauschal „OFFEN, noch kein Code“. |
| G – Suche/Skalierung | Epic G2 ist in `docs/opensearch.md`/`docs/README.md` benannt; OpenSearch-/Hybrid-Suche und CI-Indexierung bestehen in `backend/internal/search/` und `backend/cmd/server/main.go:189–217`, ohne hier ausgeführten Last-/Skalierungsnachweis. |
| H – Betrieb/Qualität | Backup-/Restore-Abläufe, Kustomize/Terraform und Observability bestehen (`docs/backup-dr.md`, `.github/workflows/restore-test.yml`, `deploy/{k8s,terraform,monitoring}/`, `backend/internal/observability/`); Traceability, Abnahme- und Sizing-Artefakte fehlen an den angefragten Pfaden. |
