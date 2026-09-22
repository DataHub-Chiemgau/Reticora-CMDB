KATALOGAUSZUG (Abschnitte 3, 4, 39)
3. Technologie-Stack

TEC-01 bis TEC-05, TEC-07, TEC-08, TEC-10, TEC-11 unverändert.

TEC-06 [B] PostgreSQL 16 mit timescaledb, pgcrypto, btree_gist, ltree; Apache AGE optional und in Managed-Postgres nicht vorausgesetzt. Alle Graph-, Metrik- und RLS-Funktionen bestehen ohne AGE. [Q] Spike in Epic A: RLS + FORCE RLS auf Hypertables, komprimierten Chunks und Continuous Aggregates nachweisen; bei Lücken Security-Barrier-Views als Fallback, festgehalten als ADR.

TEC-09 [B] Installation (CH4, CH22): install.sh für Ubuntu 22.04/24.04 LTS und Debian 12; Nginx mit TLS-Terminierung; TLS-Modul selbstsigniert/ACME; Keycloak-Bootstrap; Mindest-Sizing dokumentiert (INS-07).

TEC-12 [P5] OpenSearch-Produktivbetrieb; Umschaltkriterium NFR-02 (p95 Suche &gt; 1 s bei Auslegungsgröße trotz Indizes). Der Suchindex wird ausschließlich über den Server ausgeliefert und trägt Org-, Client-, Site- und Feldrechte-Filter (RBA-07). VictoriaMetrics als Skalierungsoption.

TEC-13 [B] Benachrichtigungen: SMTP (Phase 1); Provider-Abstraktion für SMS und Pager (Phase 3); Abschnitt 41.

TEC-14 [B] Job-Framework und Scheduler als Teil des Monolithen (Abschnitt 42); Single-Runner über Postgres-Advisory-Locks bzw. NATS-Durable-Consumer.

TEC-15 [B] Signierte Artefakte: Container-Images, Collector- und Agent-Pakete werden signiert (Sigstore/cosign); SBOM pro Release; Signaturprüfung vor Installation und Auto-Update.

TEC-16 [O] Air-Gapped-Profil: Offline-Bundle (Images, Installer, Profile, OUI-Tabelle, Update-Bundle); kein ACME, kein Auto-Update aus dem Internet, kein KI-Add-on; kundeneigener SMTP.

4. Monorepo &amp; Struktur

REP-01 [B] Verzeichnisse wie v2; zusätzlich internal/{notify,jobs,locations,pii,reviews,rules}; api/rules.schema.json; docs/acceptance, docs/decisions, docs/schema-baseline.md.

REP-02 bis REP-05 unverändert.

39. Installation &amp; Betrieb
Installation

INS-01 [Q] Install-Smoke-Test in der CI in frischer Ubuntu-24.04- und Debian-12-Umgebung mit Health-Checks aller Container (Epic A1).

INS-02 [B] Keycloak-Bootstrap-Validierung (Realm, Health, Issuer, Broker-Konfiguration); klare Fehlermeldung statt 502 (Epic A2).

INS-03 [B] TLS-Modul mit Nginx-Rendering und Kettenvalidierung (Epic A3).

INS-04 [Q] E2E Install → Signup/Login → Dashboard; abgedeckt: „failed to fetch", localhost-Redirect, crypto.digest (Epic A4).

INS-05 [B] Idempotenter Installer mit Passwort-Rotation (Epic A5); Troubleshooting-Dokumentation (Epic A6).

INS-06 [B] Zero-Config-Onboarding: Collector anschließen, Enrollment-Code eingeben, vorgeschlagene Scopes bestätigen, Infrastruktur erscheint (DIS-09).

INS-07 [B] Zielplattformen (CH22): Ubuntu 22.04/24.04 LTS, Debian 12; Docker Engine ≥ 24 und Compose v2; Mindest-Sizing für ≤ 10.000 Objekte: 4 vCPU, 8 GB RAM, 100 GB SSD; Sizing-Tabelle bis 50.000 Objekte in docs/ops/sizing.md.

INS-08 [O] Air-Gapped-Profil (TEC-16): Offline-Bundle mit Prüfsummen und Signaturen, interne CA/eigenes Zertifikat, Update-Bundle mit install.sh --offline-update, kein KI-Add-on, kein ACME, OUI-/Profil-Updates per Bundle.

Betrieb

OPS-01 [B] Health-/Readiness-Endpunkte für DB, Extensions, NATS, Redis, S3, SMTP; Konfigurationsvalidierung beim Start.

OPS-02 [B] Abschaltbare Worker: Webhook-Dispatcher, Export, Offline-Detector, Ingest, Audit-Runner, Job-Runner, Scheduler, Notifier; [P3] zusätzlich Monitoring, Workflow; [A] IGA.

OPS-03 [B] Kubernetes mit Kustomize-Overlays für Staging/Prod, Limits, HPA (Epic H1, Single-Runner-Garantie nach JOB-02); [P5] Terraform, GitOps, Postgres-HA.

OPS-04 [B] Backup: WAL-Archiv + tägliches Basisbackup (RPO 15 min, RTO 4 h, NFR-05); wöchentlicher Restore-Test in Staging; S3-Versionierung. [P5] PITR mit nächtlichem Restore-Test, Read-Replicas, Partitionierung.

OPS-05 [B] Mandantenbewusste Observability mit SLOs (NFR-04); Fehler von Workern, Collector, Discovery, Webhooks, Reconciliation, Jobs und Benachrichtigungen sind sichtbar; Alarmierung des Betriebs über NTF-Kanäle.

OPS-06 [Q] Ausfalltests für DB, NATS, Redis, S3, SMTP, Collector offline, Timeout, Webhook-Ziel: Retries, Backpressure, kein stiller Datenverlust.
