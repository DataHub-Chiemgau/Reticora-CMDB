# Reticora-CMDB

## 1. Produktüberblick

Reticora ist eine **mandantenfähige SaaS-CMDB (EU-Hosting)** für ISPs und auf Netzwerke spezialisierte MSPs. Ein Collector im Kundennetz liest Netzwerk-, Server-, Strom- und Storage-Infrastruktur **automatisch aus**. Die Cloud überführt die Beobachtungen per Identity-Resolution und Reconciliation in ein konsistentes Datenmodell. Sie stellt es **menschenlesbar** (Web-UI mit Rack-, Topologie- und Impact-Ansichten) und **maschinenlesbar** (REST-API, Webhooks, Export) bereit. Ab Phase 2 kommen Asset-, Lager-, Inventur-, Dokumenten- und Ticket-Module hinzu, ab Phase 3 Monitoring, Workflows und Compliance. IGA und KI sind zubuchbare Add-ons.

### 1.1 Leitprinzipien

- Ein Gerät wird nie zu mehreren CIs; kein Auto-Merge ohne belastbaren Identitätsbeweis.
- Asset und CI sind getrennt, aber komponierbar; das Asset ist Hoheit für kaufmännische Daten, Seriennummer, Standort und physischen Lifecycle.
- Jede Information hat genau einen kanonischen Speicherort; alle anderen Vorkommen sind abgeleitet und read-only.
- **Manuell gewinnt:** Manuelle Werte werden nie automatisch überschrieben. Automation verwirft nie still Daten; unklare Fälle werden Review-Items.
- Änderungen werden historisiert; Zeitbezug ist `observed_at` der Quelle, nicht die Ankunftszeit.
- Isolation und Entitlements werden server- und DB-seitig erzwungen, einschließlich Client-, Site- und Team-Scope.

## 2. Architekturüberblick

### 2.1 Topologie & Betriebsmodelle

- **Referenzmodell:** SaaS (EU) mit On-Prem-Collector. Dedicated Instance ist Enterprise-Option (P4). Air-Gapped-On-Prem ist optional, mit eingeschränktem Umfang (ohne KI, ACME, Auto-Update).
- Collector und Agent kommunizieren **ausschließlich ausgehend** (HTTPS 443, NATS-mTLS 4222) zu festen Hostnamen. Die Cloud öffnet nie Verbindungen ins Kundennetz.
- Der Collector übernimmt Sweep, Polling, Klassifikation und Normalisierung zu DeviceRecords. Bei Verbindungsverlust puffert er bis 24 h und aktualisiert sich nur mit signierten Paketen.
- Der Endpoint-Agent (P4) sendet direkt oder per Relay über den Collector. IGA-Connectoren (Add-on) laufen als separater Prozess mit eigenem Enrollment und Least-Privilege-Konten.

### 2.2 Logische Schichten

| Schicht | Verantwortung | Bausteine |
|---|---|---|
| **Edge** | Auslesen, Klassifikation, Offline-Spool, Credential-Nutzung im Speicher | Collector (Go), Plugins + YAML-Profile, Endpoint-Agent (Go, P4) |
| **Ingest** | Authentifizierter Empfang, Idempotenz, Ordering, Backpressure | mTLS-NATS, JetStream (`ingest.>`) |
| **Domain/Core** | Reconciliation, Metamodell, Beziehungen/Impact, Reviews, Module | Modularer Go-Monolith inkl. Job-Framework und Scheduler |
| **Daten** | Persistenz, Zeitreihen, Graph, Volltext, Objekte | PostgreSQL 16 (+ TimescaleDB, ltree, btree_gist, pgcrypto), S3, Redis; AGE optional; OpenSearch ab P5 |
| **Delivery** | Bereitstellung für Mensch und Maschine | REST `/api/v1`, GraphQL-BFF (nur UI), Webhooks, Export, Web-UI |
| **Querschnitt** | Isolation, Identität, Rechte, Lizenzen, Nachvollziehbarkeit | RLS mit Scopes, Keycloak/OIDC, RBAC/ABAC, Entitlements, Audit-Hashchain, PII-Vault, Benachrichtigungen |

### 2.3 Modularer Monolith

Reticora ist ein deploybarer Go-Monolith mit getrennten Bounded Contexts (u. a. `internal/{notify,jobs,locations,pii,reviews,rules}`) und internem Event-Bus über NATS. Die Worker sind einzeln abschaltbar: Ingest, Webhook-Dispatcher, Export, Offline-Detector, Audit-Runner, Job-Runner, Scheduler, Notifier. Systemjobs laufen mit Single-Runner-Garantie über Postgres-Advisory-Locks und sind damit HPA-sicher.

## 3. Technologie-Stack

| Bereich | Wahl | Hinweis |
|---|---|---|
| Collector / Agent | **Go** | Signierte Artefakte (Sigstore/cosign), SBOM pro Release |
| Backend | **Go** | Modularer Monolith, gqlgen für das BFF |
| Frontend | **React + TypeScript** | WCAG 2.1 AA, i18n DE/EN |
| Primär-DB | **PostgreSQL 16** | timescaledb, pgcrypto, btree_gist, ltree |
| Graph | Rekursive CTEs | Apache AGE optional, nie vorausgesetzt |
| Zeitreihen | TimescaleDB | VictoriaMetrics als Skalierungsoption |
| Volltext | Postgres FTS | OpenSearch ab P5, wenn p95 Suche > 1 s |
| Objekt-Storage | S3-kompatibel | Presigned URLs, Versionierung |
| Event-Bus | NATS JetStream | Ingest, Events, Job-Queue |
| Cache | Redis | Session-Blacklist, Rate-Limits, Entitlement-Cache |
| Identity | Keycloak | Ein Realm, Brokering OIDC/SAML pro Org, MFA |
| API-Spez | OpenAPI | Parity-Test; Regelschema in `api/rules.schema.json` |
| Installation | `install.sh`, Docker ≥ 24, Compose v2, Nginx | Ubuntu 22.04/24.04 LTS, Debian 12 |
| Betrieb | Kubernetes + Kustomize | Terraform, GitOps, Postgres-HA ab P5 |
| Hosting | EU | Daten, Backups, Logs und Suchindex in der EU |

## 4. Mandantenfähigkeit & Tenant-Modell

Hierarchie:

- Reseller (P4) → Organisation (Mandant) → Client (`client_id NULL` = org-weit) → Location-Baum → CI / Asset
- Location-Baum (Tabelle `location` mit `ltree`-Pfad):
  - Betriebszweig: Site → Building → Room → Rack
  - Lagerzweig (P2): Site → Warehouse → Zone → Shelf → Bin

Regeln:

- **Shared DB, Shared Schema:** `organization_id NOT NULL` auf jeder Mandantentabelle, darunter `client_id`, bei Standortbezug denormalisiert `site_id`.
- **RLS (ENABLE + FORCE)** auf allen Mandantentabellen. Pro Request setzt `WithTenant` `app.org_id` sowie mengenwertige Client-, Site- und Team-Scopes.
- `USING` und `WITH CHECK` prüfen dieselben Bedingungen. Ein client-gescopter Nutzer kann daher keine org-weiten Zeilen anlegen.
- Die DB-Rolle hat weder Superuser noch BYPASSRLS, aber DDL-Recht auf den Anwendungsschemata für Laufzeit-Indizes.
- VRFs liegen auf Org-Ebene, mit Default-VRF `global`. Subnetze sind eindeutig pro (org, vrf, cidr).
- Mandantenlebenszyklus:
  - Self-Signup mit E-Mail-Verifikation und Trial
  - Anlage durch Operator oder (P4) Reseller
  - Einladungen 7 Tage gültig, Deaktivierung wirkt sofort
  - Vollexport und Org-Löschung mit 30 Tagen Karenz ab P3
- Enterprise: Dedicated Schema oder Instance mit eigenem Schlüssel (P4).

## 5. Discovery-Engine

### 5.1 Protokolle

- **Sweep:** ICMP (max. 256 parallel), ARP, optionale Ports (22/80/443/623/5985/8006), SNMP, Reverse-DNS.
- **Geräte:** SNMP v2c/v3, SSH, Redfish, IPMI/WMI (Best Effort), NAS- und Strom-Plugins.
- **P3:** SNMP-Traps, mDNS, SSDP, CDP, NetFlow, sFlow, STP.
- **P4:** Connectoren (Intune, vCenter, Active Directory, Hersteller-APIs).

### 5.2 Ablauf

1. **Scope:** Discovery-Scopes definieren CIDR, Host oder Range samt VRF, Zeitplan, Credentials und Plugins. Zero-Config: Nach dem Enrollment schlägt der Collector seine lokalen Subnetze vor.
2. **Sweep und Poll:** Default Sweep 24 h, SNMP/Redfish-Poll 15 min (min. 5 min).
3. **Klassifikation:** Reihenfolge SNMP → Redfish → Banner, dazu OUI-Tabelle und YAML-Profile (Typ, Hersteller, Modell, Plugin, Template). Ohne Treffer entsteht `generic_device` plus Review.
4. **Übertragung:** DeviceRecord mit `observed_at` über mTLS-NATS; offline im Spool, Flush in Originalreihenfolge.
5. **Ingest:** idempotent über `record_id` (48 h), seriell pro CI, dann Reconciliation. Pro Record entstehen genau eine `ci_change` und ein Event.

### 5.3 Identity-Resolution & Reconciliation

Priorisierte Identity-Keys innerhalb desselben Clients:

1. Seriennummer (Blocklist für Platzhalter wie „To be filled by O.E.M."),
2. Hardware-UUID (Blocklist bekannter Klon-UUIDs; virtuelle Typen brauchen zusätzlich MAC oder Hostname),
3. eindeutige MAC-Schnittmenge (ohne lokal administrierte und VRRP/HSRP/CARP-MACs),
4. Management-IP + sysObjectID in derselben VRF,
5. Hostname/FQDN – **nie Auto-Merge**, nur Review `probable_duplicate`.

Quellränge bestimmen, welcher Wert übernommen wird:

| Quelle | Rang |
|---|---|
| manual / Override | 100 |
| import | 95 |
| workflow | 92 |
| redfish | 90 |
| agent | 85 |
| snmp | 80 |
| api, integration | 75 |
| wmi | 70 |
| ssh, nas | 60 |
| sweep | 20 |

Weitere Regeln:

- Ein Wert wird nur übernommen, wenn sein `observed_at` neuer ist als der gespeicherte.
- Overrides bleiben immer erhalten; weicht eine Quelle ab, entsteht `override_conflict`.
- Jedes Feld ist erklärbar: Quelle, Wert, Zeitpunkt, Auswahlgrund.
- Unklare Fälle landen in der **Review-Inbox**, u. a. `unclassified_device`, `ambiguous_identity`, `conflicting_values`, `resurrected_device`, `unlicensed_ci`, `scope_suggested`, `serial_mismatch`.
- CIs ohne Sichtung seit 3× Scope-Intervall werden `unknown`. Das gilt nur für beobachtbare, nicht manuelle CIs.

### 5.4 Klassifikationsprofile (Phase 1)

- **Netzwerk:** Cisco Catalyst/ISR/ASR/ASA/Firepower, Aruba CX und ProCurve, Ubiquiti UniFi, MikroTik, FortiGate, OPNsense/pfSense.
- **Server/Virtualisierung:** Dell iDRAC, HPE iLO, Lenovo XCC, Supermicro BMC, VMware ESXi, Proxmox VE, Windows Server (WMI), Linux (SSH).
- **Storage:** Synology, QNAP, TrueNAS.
- **Strom:** APC/Eaton-USV, APC/Raritan-PDU.

### 5.5 Topologie-Ableitung

- LLDP erzeugt `connected_to` mit Confidence 1.0. FDB wird nur ohne LLDP genutzt (0.6); mehr als 4 MACs gelten als Trunk. ARP dient nur zur Anreicherung.
- Unbestätigte Discovery-Kanten verschwinden nach max(7 Tage, 5× Scope-Intervall).
- Manuell entfernte Kanten werden per Suppression nicht neu angelegt. Manuelle Kanten löscht Discovery nie.

### 5.6 Collector & Credentials

- **Enrollment:** Einmal-Token (24 h) und CSR, interne CA. Zertifikate gelten 90 Tage und erneuern sich ab 60 % Laufzeit automatisch. Gesperrt wird über Widerruf und NATS-Account.
- **Betrieb:** Heartbeat 60 s, offline nach 5 min. Auto-Update nur signiert (Kanäle stable/beta, Rollback). Auslieferung als Docker, OVA oder Windows-Installer.
- **Credentials:** serverseitig per **Envelope-Encryption** (Org-DEK, AES-256-GCM). Die Übergabe an den Collector läuft über mTLS-NATS, zusätzlich auf den Collector-Schlüssel verschlüsselt. Klartext existiert nur im Speicher, nie auf Disk, nie in API oder Logs.
- Zentrale KMS/Vault-Anbindung ab P4.

## 6. Datenmodell

Konventionen:

- UUID-PKs, `timestamptz` in UTC, `text` + CHECK statt Enums.
- Englische `snake_case`-Schlüssel; Anzeige über i18n.
- Fortlaufende Migrationen mit up/down-Roundtrip.
- Hybrid-Modell: typisierte Spalten für häufige Filterfelder, dynamische Fachattribute in `ci.attributes` (JSONB), kein EAV.
- Durchsuchbare und eindeutige Attribute erhalten zur Laufzeit per Job partielle Expression-Indizes (max. 50 pro Org).

### 6.1 Kanonische Speicherorte

| Information | Kanonisch | Abgeleitet |
|---|---|---|
| Seriennummer, Standort, physischer Lifecycle, Kaufdaten, Owner | `asset` | CI zeigt read-only; Abweichung → Review |
| Standort ohne Asset | `ci.location_id` | `site_id`/`room_id` denormalisiert |
| Rack-Platzierung / Chassis / Standort-Enthaltensein | `rack_mount` / `ci.parent_ci_id` / `location.parent_id` | `mounted_in` / `contains` / `located_in` werden projiziert |
| Operativer Status / Health / Software-Lifecycle | `ci.status` / `ci.health` (berechnet) / `ci.lifecycle_state` | – |
| VLAN, Subnetz, Vertrag | eigene Tabellen | **keine** CI-Typen |
| Installierte Software | `installed_software` | CI-Typ `software` nur für Produkte |
| Personen- und Kontaktdaten | PII-Vault | überall sonst nur Surrogat-IDs |

### 6.2 CI-Kern

- Ein CI hat Typ, Client, Location, optionalen Asset-Link sowie Hardware-, Netz- und Systemfelder. Status: `active|inactive|maintenance|decommissioned|unknown`.
- Namensräume in `attributes`: `_overrides`, `_observed`, `_provenance`, `_instance`. Die API liefert pro Feld den effektiven, beobachteten und überschriebenen Wert.
- **Soft-Delete:** Restore innerhalb 90 Tagen möglich.
- **Merge:** hängt alle Bezüge um, das alte CI leitet per 301 weiter; Unmerge innerhalb 30 Tagen.
- **Reklassifikation:** Discovery darf nur `generic_device` auf einen spezifischen Typ heben.

### 6.3 Metamodell

- Objekttypen und zentrale Attributdefinitionen pro Org sind ohne Migration nutzbar.
- Seed-Typen physisch (switch, router, firewall, server, pdu, ups, nas …) und logisch (business_service, application, database, cluster, cloud_resource, software).
- Das effektive Schema entsteht per Komposition mit festen Konfliktregeln: System-Basis → Typkette → Attribute Sets → Templates → Instanzattribute.
- **Regelsprache als JSON-AST** (Klick-Builder in der UI, keine Text-DSL). Sie dient für Validierung, konditionale Felder, Workflows, Compliance und Suche. Referenzimplementierung in Go und TypeScript mit gemeinsamem Konformitätstestset.
- Ab P2: Typvererbung, Attribute Sets, versionierte Templates, Regel-Engine, berechnete Attribute, Form Builder, Draft → Test → Publish.

### 6.4 Netzwerk, Rack & Beziehungen

- Netzwerktabellen: `network_interface`, `subnet`, `ip_address`, `vlan`, `cable`, `vrf`. IP-Reservierung und Duplikaterkennung ab P2.
- `rack_mount` mit Exclusion-Constraint, das die Seite berücksichtigt (front/rear/both), und Höhenprüfung.
- Beziehungstypen tragen Kategorie und **Impact-Semantik** (`impact_direction`, `redundant`). Kanten haben Provenienz, Confidence und Verifikation. Eigene Typen ab P2.

### 6.5 Management-Layer (ab P2, Auszug)

- `asset` mit Komposition, Reservierung und Zuweisung,
- `stock_movement` als einzige unveränderliche Bewegungshistorie, `consumable`, `internal_order`,
- `stocktake`, `stock_scan`, Barcode und Etikettendruck,
- `contract`, `document`, `document_link`, `disposal_record`,
- `ticket` mit SLA und Business-Calendar,
- `workflow_def`, `workflow_run`, `form_def`, `form_submission` (P3),
- `compliance_rule`, `compliance_result`, Findings (P3).

## 7. Graph, Topologie & Impact

- **Impact:** per rekursivem CTE (Tiefe ≤ 20, kürzester Pfad, Kategorienfilter). Die Richtung kommt aus `impact_direction`. `connected_to` zählt nur als Heuristik zweiter Klasse.
- **Redundanz:** `powered_by` und `member_of_cluster` betreffen ein CI erst, wenn alle Quellen ausgefallen sind.
- **Mehrfachausfall:** `/impact?ci_ids=…` für Wartungsfenster und Redundanzprüfung.
- **Strommodell:** Gerät → PDU → USV → Quelle.
- **Topologie:** ohne Root max. 2.000 Knoten.
- **Ab P2:** Service-Impact-Kette mit Blast Radius und SPOFs; Subnetze und VLANs als projizierte Knoten.

## 8. API-Design

Grundsätze:

- REST unter `/api/v1`, vollständig in OpenAPI beschrieben (Parity-Test). Änderungen nur additiv; Deprecation mit Sunset-Header und ≥ 12 Monaten Vorlauf.
- **Auth:** OIDC + PKCE und RS256-Session-JWT (15 min) mit Scope-Claims. API-Keys (`rk_live_…`, gehasht) gehören Nutzern oder Service-Accounts.
- **Listen:** HMAC-signierte Cursor-Pagination (Default 50, max. 200), Filter, Sortier-Whitelist, Sparse Fields, Volltext `q`.
- **Fehler und Konsistenz:** RFC 7807 mit `trace_id`, Idempotenz 24 h. Optimistische Nebenläufigkeit per ETag; Discovery ändert die Version nicht.
- **Rate-Limits:** 20/min pro IP vor der Auth, 600/min pro Key/Session, 6.000/min pro Org.
- **Bulk:** bis 500 IDs, ab 100 IDs asynchron als Job mit Partial-Failure-Report; Undo ab P2.

Zusätzlich:

- Events über NATS (`events.<org_id>.<type>`), at-least-once, Reihenfolge pro Entität.
- Signierte Webhooks (HMAC-SHA256) mit Backoff von 30 s bis 6 h, Dead-Letter und automatischer Deaktivierung.
- Internes GraphQL-BFF nur für die UI (Tiefe 10, Komplexitätsbudget, keine Mutationen).
- Asynchroner CSV-Export (UTF-8 mit BOM, Semikolon, nur sichtbare Felder plus Manifest) mit 15-min-Download-URL.
- DATEV-Export und Import mit Mapping und Dry Run ab P2.

## 9. Usability, UX & Visualisierung

Leitlinien:

- Zero-Config-Onboarding mit Fortschrittsanzeige: Registrierung → Verifikation → erster Collector → Scope bestätigen → erste CIs.
- Review-Inbox mit Diff-Ansicht und Bulk-Auflösung, Jobcenter, Benachrichtigungscenter.
- Provenienz-Tooltip pro Feld, Command-Palette, Tastaturbedienung, Skeletons und Undo.
- Progressive Disclosure und geführte Anlage ohne Metamodell-Begriffe (P2).
- WCAG 2.1 AA, DE/EN, aktuelle Browser inkl. Mobil.

Visualisierungen:

- Rack-Ansicht (SVG, HE-genau),
- Topologie, ab 2.000 Knoten mit vorgeschaltetem Standort-/Typfilter,
- Impact-Simulation mit Mehrfachauswahl,
- Metrik-Linien in der CI-Detailansicht,
- ab P4: rollenbasierte Dashboards, Mobil/Offline mit Scan, GPS und Unterschrift, Raumpläne, Heatmaps, GIS.

## 10. Monitoring, Agent & Zeitreihen

- **Metriken:** TimescaleDB-Hypertable mit RLS, 1-Tages-Chunks, Kompression nach 7 Tagen, 400 Tage Retention, stündliches Continuous Aggregate.
- **Phase 1:** Erreichbarkeit, Interface-Zähler, USV- und PDU-Werte.
- **P3:** konfigurierbares Polling und Alerting-Regeln (JSON-AST), die Tickets, Webhooks oder Benachrichtigungen auslösen. Health wird aus Alerts und Findings berechnet.
- **Endpoint-Agent (P4)** für Windows, Linux und macOS:
  - erfasst Hardware, Software (`installed_software`) und Patch-Stand,
  - sendet per mTLS direkt oder per Relay,
  - signierte Pakete und Self-Update, Heartbeat 5 min, Kill-Switch.
- CVE- und Versionsabgleich mit Findings pro CI (P3).

## 11. Workflows & Formulare (P3)

- `form_def`/`form_submission`: versioniert, JSON-Schema, bedingte Logik per JSON-AST.
- `workflow_def`/`workflow_run`:
  - Trigger: Event, manuell oder Zeitplan
  - Aktionen: Ticket, Webhook, Benachrichtigung, Feld setzen, Genehmigung
  - idempotent mit Retries
- Schreibaktionen tragen Provenienz `workflow` (Rang 92) und respektieren Overrides.
- Einsatz für Übergaben mit Unterschrift, Bestellgenehmigungen, Wartungsbenachrichtigungen, delegierte Inventur (P4).

## 12. Berechtigungen, Sicherheit & Compliance

- **RBAC + ABAC:**
  - Systemrollen `org_admin`, `engineer`, `viewer`, `client_technician`.
  - Custom-Rollen mit Scopes auf Org, Client, Site oder Team.
  - Jede Route ist fail-closed gemappt.
  - Feldrechte je Attribut und Rolle ab P2, wirksam in REST, GraphQL, Suche, Export, Webhooks und KI.
- **Identität:** Keycloak mit einem Realm; Kunden-IdPs per Brokering pro Org. MFA ist Pflicht für `org_admin` und Operatoren. SAML-SSO und SCIM 2.0 ab P4.
- **Schlüssel:** JWT-Rotation alle 90 Tage, Master-Key-Rewrap ohne Downtime, Org-DEK jährlich. Webhook-Secrets und API-Keys rotieren mit Überlappung.
- **Operatoren:** personalisierte Identitäten mit MFA. Jede Nutzung des Break-Glass-Tokens löst einen Alarm aus. Eigenes `operator_audit` mit Hash-Chain.
- **Egress-Schutz** für Webhook-, Import- und Connector-URLs: private und Metadata-Bereiche gesperrt, DNS-Rebinding-Schutz, Redirect-Limit.
- **Audit:**
  - Hash-Chain pro Org über Outbox und Single-Runner, Prüfung per CLI `audit-verify`.
  - `ci_change` als Feldhistorie.
  - Retention: Audit-Log 10 Jahre, Historie 3 Jahre.
- **DSGVO:** PII-Vault mit Surrogat-IDs ab Phase 1. Gelöscht wird per Crypto-Shredding bei intakter Hash-Kette. Auskunfts- und Löschworkflows ab P3.
- **Compliance (P3):** Compliance-Regeln und Score pro CI, Reports für ISO 27001, NIS2, KRITIS.
- **Prüfrahmen:** OWASP ASVS L2, Pentest vor G1 und GA, CVE-Fix-SLA 7 Tage (Critical) bzw. 30 Tage (High).

### 12.1 IGA (Add-on)

- Eigenes Entitlement und eigenes Gate, zubuchbar zu jedem Paket.
- Connector-Framework; schreibende Connectoren laufen als separater Prozess mit eigenem Enrollment und Least-Privilege.
- JML-Lifecycle, Access-Requests mit Genehmigung, Access-Reviews, Drift-Erkennung.
- Remediation nur mit Vier-Augen-Freigabe.

## 13. KI-Funktionen (Add-on)

- LLM über Provider-API mit RAG auf mandantengescopten Daten.
- Chunks werden nach den Feldrechten des fragenden Nutzers gefiltert; Antworten müssen zitieren.
- EU-Verarbeitung oder Self-Hosting, kein Training auf Kundendaten, Opt-in pro Org.
- PII-Surrogate werden vor dem Prompt nicht aufgelöst.
- Im Air-Gapped-Profil deaktiviert.

## 14. Entitlement-/Lizenzsystem

Feature-Keys mit Limits pro Mandant auf einer Codebasis, serverseitig in REST und GraphQL durchgesetzt:

| Paket | Umfang (Auszug) | max_cis / Collectors / Nutzer |
|---|---|---|
| **Essential** | CMDB-Kern, Discovery, Topologie, Rack, Export, Webhooks, API; ab P2 Assets, Dokumente, Import, Ticketing Essential | 500 / 2 / 5 |
| **Standard** | + Ticketing Standard, Signatur, Lager, Inventur, Barcode, DATEV | 2.500 / 5 / 25 |
| **Pro** | + SMS/Pager, Workflows, Compliance-Findings, Ticketing Pro, Disposition, erweitertes Audit | 10.000 / 20 / 100 |
| **Enterprise** | + Monitoring, Reseller, RFID, GIS, Endpoint-Agent, Schlüssel, Schulung, Dedicated Instance | 50.000 / ∞ / ∞ |
| **Add-ons** | IGA, KI – zu jedem Paket zubuchbar | – |

Paketmatrix und Limits sind noch zu bestätigen.

Regeln:

- Nach Lizenzablauf stoppt nur Discovery/Ingest (Collector `paused`); alles andere bleibt nutzbar. Warnungen gehen 14, 7 und 1 Tag vorher raus.
- Über `max_cis` werden neue Discovery-CIs nicht verworfen, sondern als Review `unlicensed_ci` gehalten.
- Downgrades löschen nie Daten.
- Trial: 30 Tage Standard-Umfang mit 100 CIs.

## 15. Betrieb, Deployment & Skalierung

- **Installation:** idempotentes `install.sh` mit Keycloak-Bootstrap-Validierung, TLS-Modul (selbstsigniert/ACME) und Passwort-Rotation.
- **Mindest-Sizing** bis 10.000 Objekte: 4 vCPU, 8 GB RAM, 100 GB SSD.
- **Kubernetes** mit Kustomize-Overlays und HPA. Ab P5: Terraform, GitOps, Postgres-HA, PITR, Read-Replicas, Partitionierung.
- **Observability:** Health-/Readiness-Endpunkte für alle Abhängigkeiten, mandantenbewusst mit SLOs.
- **Backup:** WAL-Archiv plus tägliches Basisbackup, RPO 15 min, RTO 4 h, wöchentlicher Restore-Test.
- **Jobs** (Export, Import, Bulk, Merge, Index-Anlage …) mit Jobcenter, Retry, Cancel und Metriken.
- **Benachrichtigungen** per E-Mail, SMS/Pager ab P3; mit Ruhezeiten, Digest und Deduplizierung.
- **Air-Gapped-Profil** (optional) mit signiertem Offline-Bundle und Offline-Updates.

Zielwerte:

| Kennzahl | Phase 1 | Phase 5 |
|---|---|---|
| Objekte pro Org | 10.000 (Headroom 50.000) | – |
| Orgs pro Instanz | 500 | 5.000 |
| Ingest | 200 Records/s, sichtbar < 30 s | 1.000 Records/s |
| Verfügbarkeit | 99,5 % | 99,9 % |
| Latenz p95 | Listen/Suche < 500 ms, CI-Detail < 300 ms, Topologie (2.000 Knoten) < 3 s, Impact < 2 s | – |

## 16. Qualitätssicherung

- **Traceability:** Jede Anforderung ist über [`docs/traceability.csv`](docs/traceability.csv) (`id;tag;epic;testdatei;wp`) auf Epic, Testdatei und Arbeitspaket rückführbar. Der CI-Job `traceability` (`backend/internal/traceability`) prüft Format, bekannte IDs, vorhandene Testdateien und die im Implementierungsplan vorgesehenen Zeilen der WPs eines PRs; die Vollständigkeit aller [B]/[Pn]/[A]-IDs wird berichtet und ist ab G1 blockierend (E-30).
- **Definition of Done pro PR:** OpenAPI, RLS, Audit/Events, i18n, Tests, Lint, Doku, Traceability. Arbeitspakete umfassen 0,5–2 Tage; die Checkliste steht in der [PR-Vorlage](.github/pull_request_template.md).
- **Tests:**
  - Unit, u. a. ≥ 20 Reconciliation-Fälle
  - Integration gegen echte Dienste (RLS inkl. Scopes, mTLS, Ingest bis Webhook)
  - Playwright-E2E
  - Lasttest und Ausfalltests
  - Isolationstest über alle Zugriffspfade
- **Seed und Simulation:** Demo-Org und Isolations-Org; Simulationsmodus mit 30 deterministischen Geräten über den echten Ingest.
- **Abnahmeszenarien:** CMDB, System, Onboarding, Override-Konflikt, Metamodell (P2).

## 17. Phasenplan / Roadmap

Jede Phase ist eigenständig auslieferbar und hat ein eigenes Gate: kein offenes Critical, alle Anforderungen der Phase erfüllt.

- **Phase 1 – Baseline (G1):** Epic A Installation/Betrieb → Epic B Fundament (Mandanten, Auth, Entitlements, Datenmodell) parallel zu Epic D (Collector, Discovery, Reconciliation) → Epic C (Kernfunktionen, API, UI) → Lasttest → Sicherheitsreview/Pentest.
- **Phase 2 – Verwaltung (G2):** Metamodell (Vererbung, Templates, Regeln, Form Builder) → Verträge, Assets, Lager, Inventur, Dokumente → Ticketing Essential/Standard → Import, Bulk, Views; dazu Feldrechte.
- **Phase 3 – Betrieb & Compliance (G3):** Monitoring und Alerting → Workflows → Compliance, Findings, DSGVO-Workflows, SMS/Pager, Wartungsfenster.
- **Phase 4 – Erweiterung (G4):** Endpoint-Agent, Mobil/Offline, SAML/SCIM, Reseller, KMS, Dedicated Instance, Management-Module (Schlüssel, Desk-Booking, Schulung, Disposition, RFID, GIS).
- **Phase 5 – Skalierung (G5):** OpenSearch, Terraform/GitOps, Postgres-HA, PITR, Servicekosten-Analyse, Cloud-Inventar.
- **Add-ons IGA und KI (GA):** jeweils nach G2, unabhängig voneinander.
