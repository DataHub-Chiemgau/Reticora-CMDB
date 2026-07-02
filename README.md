# Reticora-CMDB

Reticora ist eine **mandantenfähige SaaS-CMDB**, die Netzwerk-, Server-, Strom- und Storage-Infrastruktur **automatisch ausliest**, in ein konsistentes Datenmodell überführt und sowohl **menschenlesbar** (intuitive Web-UI) als auch **maschinenlesbar** (REST-API, Webhooks, Export) bereitstellt. Zielgruppe sind ISPs und auf Netzwerke spezialisierte MSPs.

## 2. Architekturüberblick

### 2.1 Topologie: Collector + Cloud (Hybrid)

Reticora nutzt ein **Probe/Collector-Muster**:

- Im Kundennetz läuft ein Collector, der lokal scannt, pollt und normalisiert.
- Daten werden **ausgehend per mTLS** in die Reticora Cloud (EU) übertragen.
- Der Collector übernimmt Discovery, aktive Provisionierung (On-Prem-Connectors) und Agent-Relay.
- Ein Endpoint-Agent liefert Telemetrie für Server/Clients.
- Cloud-Verzeichnisse (Entra ID, Google Workspace, M365) werden direkt durch die IGA-Engine angebunden.

### 2.2 Logische Schichten

| Schicht | Verantwortung | Bausteine |
|---|---|---|
| **Edge** | Auslesen, aktive Provisionierung, Agent-Relay, lokale Credential-Haltung | Collector (Go), Endpoint-Agent (Go), Plugins |
| **Ingest** | Authentifizierter Empfang, Backpressure, Entkopplung | mTLS-Gateway, NATS JetStream |
| **Domain/Core** | CMDB-Logik, Reconciliation, Beziehungen, Management-Module | Modularer Go-Monolith |
| **Daten** | Persistenz, Zeitreihen, Graph, Volltext, Objekte | PostgreSQL, TimescaleDB, Apache AGE, OpenSearch, S3 |
| **Delivery** | Bereitstellung für Mensch und Maschine | REST-API, GraphQL-BFF, Webhooks, Export, Web-UI |
| **Querschnitt** | Mandantenfähigkeit, Auth, IGA, Entitlements, Audit, KI | RLS, OIDC, IGA-Engine, Entitlement-Service, Audit-Hashchain |

### 2.3 Modularer Monolith statt verfrühter Microservices

Start mit einem deploybaren **Go-Monolithen** mit strikt getrennten Bounded Contexts, internen Events und eigenen Schema-Namespaces. Spätere Extraktionskandidaten:

- Discovery-/Agent-Ingest,
- Monitoring/Time-Series,
- IGA-/Reconciliation-Engine,
- Webhook-/Notification-Dispatch.

## 3. Technologie-Stack

| Bereich | Wahl | Begründung |
|---|---|---|
| Collector | **Go** | Single-Binary, gute Nebenläufigkeit, kleiner Footprint |
| Endpoint-Agent | **Go** | Gemeinsamer Core mit Collector, Cross-Platform |
| Backend | **Go** | Konsistenz, Durchsatz, Wartbarkeit |
| Frontend | **React + TypeScript** | Interaktive UIs, großes Ökosystem |
| UI-Basis | Radix UI + Tailwind + TanStack Query + Zustand | Design-System + klare State-Trennung |
| Topologie-Viz | Sigma.js/Cytoscape.js + React Flow | Große Graphen + editierbare Flows |
| Rack-Viz | Custom SVG/Canvas | Pixelgenaue HE-Darstellung |
| Primär-DB | PostgreSQL 16+ | Integrität + JSONB-Flexibilität |
| Graph | Apache AGE | openCypher in PostgreSQL |
| Zeitreihen | TimescaleDB (optional VictoriaMetrics) | Monitoring-/Verbrauchsdaten |
| Volltext | Postgres FTS, später OpenSearch | Schlank starten, skalierbar |
| Objekt-Storage | S3-kompatibel | Dokumente/Anhänge/Export |
| Event-Bus | NATS JetStream | Streaming + interner Event-Bus |
| Cache | Redis | Sessions, Rate-Limit, Idempotenz |
| API-Spez | OpenAPI 3.1 | Spec-first, SDK-freundlich |
| Identity/Provisioning | SCIM 2.0 + Connector-Framework | Aktive Provisionierung/JML |
| Infra | Kubernetes, Terraform, OTel, Prometheus, Grafana, Loki | Standardbetrieb + Observability |
| Hosting | EU/Deutschland | Datenresidenz/DSGVO |

## 4. Mandantenfähigkeit & Tenant-Modell

Hierarchie:

```text
Organisation (Mandant = MSP/ISP)
└── Kunde/Client
    └── Standort (Site) → Gebäude → Raum → Rack → CI
```

- Isolationsstrategie: **Shared DB, Shared Schema** mit `organization_id` auf jeder Tabelle.
- Erzwingung über PostgreSQL **RLS**.
- Optional `client_id`-Scoping.
- Enterprise-Optionen: Dedicated Schema oder Dedicated Instance.

## 5. Discovery-Engine

### 5.1 Protokolle je Geräteklasse

- Netzwerksweep: ICMP, ARP, Port-Scan, mDNS/SSDP/LLDP/CDP.
- Netzwerkgeräte: SNMP v2c/v3, SSH/CLI, optional NetFlow/sFlow, Traps.
- Server: Redfish, IPMI, WMI/WinRM, SSH.
- Strom: SNMP, NUT, Hersteller-APIs.
- NAS/Storage: SNMP, APIs (Synology/QNAP/TrueNAS), SMB/NFS-Enumeration.

### 5.2 Ablauf

1. Sweep + Polling.
2. Rohdaten über SNMP/SSH/Redfish/API.
3. Fingerprinting/Normalisierung ins kanonische CI-Modell.
4. Batch-Upload komprimiert via mTLS (offline: lokaler Puffer).
5. Reconciliation, Upsert, Diff, Ableitung von Beziehungen.

### 5.3 Identity-Resolution & Reconciliation

Priorisierte Identity-Keys:
1. Seriennummer,
2. Chassis-/Hardware-UUID,
3. MAC-Adresse(n),
4. Management-IP + sysObjectID,
5. Hostname/FQDN.

Konfliktlösung nach Quellenvertrauen + Aktualität; Unklarheiten in Review-Queue.

### 5.4 Normalisierung & Klassifikation

Datengetriebenes Mapping (Profile je Hersteller/Modell), OUI-Lookup, sysObjectID-Tabelle, Banner-Grabbing.

### 5.5 Topologie-Ableitung

L2 aus LLDP/CDP + FDB, L3 aus ARP/Routing, STP für Pfade. Ergebnis: automatische `connected_to`-Beziehungen.

### 5.6 Credential-Handling

Credentials liegen im Kundennetz auf dem Collector, verschlüsselt. Optional zentrale Verwaltung via Envelope-Encryption (KMS/Vault).

## 6. Datenmodell

Konventionen:

- UUID-PKs (`gen_random_uuid()`),
- `organization_id`/`client_id` je Mandantentabelle,
- `created_at`/`updated_at` als `timestamptz`,
- heterogene Attribute in `JSONB`.

Hybrid-Modell: häufige Felder als typisierte Spalten, gerätespezifisches in `JSONB`.

### 6.1 Management-Layer (Auszug)

- `asset`, `assignment` (kaufmännische + Lifecycle-Sicht),
- `app_user`, `team`, `role`, `permission`,
- `contact`, `ci_contact`,
- `document`, `document_link`,
- `ticket`, `ticket_comment`, `sla`,
- `stocktake`, `stock_scan`,
- `workflow_def`, `workflow_run`, `form_def`, `form_submission`,
- `compliance_rule`, `compliance_result`,
- `webhook_subscription`, `webhook_delivery`,
- `entitlement`.

## 7. API-Design

Grundsätze:

- OpenAPI 3.1, REST unter `/api/v1`.
- OIDC/OAuth2, API-Keys/Service-Tokens, JWT-Scopes.
- Cursor-Pagination, Filter/Sort/Sparse-Fields.
- Fehler via RFC 7807 (`application/problem+json`).
- Idempotenz via `Idempotency-Key`.
- Versionierung per Major in URL.

Zusätzlich:

- Collector-Bulk-Ingest mit Reconciliation/Upsert/Diff.
- Signierte Webhooks (HMAC-SHA256) mit Retry/Backoff.
- Internes GraphQL-BFF für UI.
- Asynchroner Export (CSV/DATEV) mit signierter Download-URL.

## 8. Usability, UX-Architektur & Visualisierung

Leitlinien:

- Zero-Config-Onboarding,
- Progressive Disclosure,
- Sinnvolle Defaults,
- Tastatur-First + Command-Palette,
- schnelle wahrgenommene Performance,
- Fehlervermeidung + Undo,
- Bulk-Komfort,
- Konsistentes Design-System.

Fundament: Radix + Tailwind + TanStack Query + Zustand, A11y/WCAG, i18n (Deutsch zuerst), Dark Mode ab Phase 0.

Visualisierungen:

- Rack-Ansicht (SVG, HE-genau, Drilldown),
- Topologie/Abhängigkeiten (WebGL-Graph, Filter, Ausfallsimulation),
- Raumpläne mit Overlays,
- GIS/Kartenansicht,
- Rollenbasierte Dashboards.

## 9. Monitoring, Agent & Zeitreihen

- TimescaleDB für Metriken, Continuous Aggregates, optional VictoriaMetrics.
- Collector-Polling + Trap/Event-Verarbeitung.
- Eigener Endpoint-Agent (Windows/Linux/macOS), ausgehend-only via mTLS, bevorzugt Relay über Collector.
- Signierte Pakete, abgesicherter Self-Update-Kanal, Heartbeat, zentrale Policies, Kill-Switch.
- Firmware-/Softwarestände gegen CVE-/Versionsfeeds.

## 10. Workflow-Builder & Formulare

- `form_def`/`form_submission` (JSON-Schema-basiert).
- `workflow_def`/`workflow_run` (Trigger, Bedingungen, Aktionen).
- Einsatz für Übergaben, Inventur, Genehmigungen, Wartungsbenachrichtigungen.

## 11. Berechtigungen, Sicherheit & Compliance

- **RBAC + ABAC** mit Scopes über Mandant/Kunde/Standort.
- SSO via OIDC/SAML, SCIM inbound/outbound.
- TLS überall, Encryption-at-Rest, Envelope-Encryption für Credentials.
- Audit-Hashchain, Compliance-Score pro CI-Typ.
- DSGVO: Datenminimierung, Lösch-/Exportworkflows, PII-Vault mit Surrogat-IDs.
- ISO27001/NIS2/KRITIS-Report-Generierung.

### 11.6 IGA (aktive Provisionierung)

- Connector-Architektur (On-Prem via Collector, Cloud via Backend).
- JML-Lifecycle (Joiner/Mover/Leaver).
- Access-Requests mit Genehmigungsworkflows.
- Access-Reviews/Recertification.
- Reconciliation + Drift-Erkennung inkl. Remediation.

## 12. KI-Funktionen

LLM-Aufrufe über Provider-API mit RAG auf mandantengescopten Daten und strikter Berechtigungs-/Daten-Governance.

## 13. Entitlement-/Lizenzsystem

Feature-Flags mit Limits pro Mandant auf einer Codebasis:

- Essential,
- Standard,
- Pro,
- Spezialisierung/Enterprise (u. a. RFID, GIS, Endpoint-Agent, IGA).

## 14. Betrieb, Deployment & Skalierung

- Kubernetes in EU-Region, NATS, Redis, Postgres-HA, S3.
- IaC mit Terraform, GitOps-Deployments.
- Collector als Docker/OVA/Windows-Installer.
- Agent-Rollout via MSI/DEB/RPM/PKG (GPO/Intune/MDM/Skript).
- OTel/Prometheus/Grafana/Loki, mandantenbewusste Observability.
- Skalierung über Partitionierung, Read-Replicas, spätere Service-Extraktion.
- Backup/DR mit PITR + Restore-Tests.

## 15. Phasenplan / Roadmap

- **Phase 0:** Fundament (Mandantenmodell, RLS, Entitlements, UX-Foundation, CI/CD).
- **Phase 1:** Discovery + CMDB-Kern inkl. Visualisierung, REST/Webhooks/Export.
- **Phase 2:** Verwaltung (Inventar, Zuweisungen, DMS, Tickets Essential, Rollen).
- **Phase 3:** Standard-Add-ons.
- **Phase 4:** Pro-Add-ons.
- **Phase 5:** Spezialisierung, Analyse & Enterprise.
