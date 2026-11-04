# Betriebsreferenz: Betriebsmodelle und Datenresidenz

Anforderungen: PRI-11 ([B], Betriebsmodelle nach CH8), NFR-09 ([B],
Datenresidenz und Subprozessoren). Umsetzung: WP-105.

## Betriebsmodelle

| Modell | Status | Inhalt | Gate |
|---|---|---|---|
| **SaaS EU mit On-Prem-Collector** | Referenzmodell (CH8) | Zentrale Plattform (Server, Frontend, PostgreSQL/TimescaleDB, Redis, NATS, Objektspeicher, Keycloak, optional OpenSearch) in einer EU-Region; Collector als VM im Kundennetz (`install.sh --vm`), Verbindung über mTLS nach außen | G1-relevant |
| **Dedicated Instance** | Enterprise-Option (CH8) | Gleicher Stack wie SaaS, eigene Instanz je Kunde (Compose oder Kubernetes, `deploy/k8s`); eigenes Schema bzw. eigene Instanz mit eigenem Schlüssel ist TEN-07 ([P4]) | gleiche Anforderungen wie SaaS |
| **Air-Gapped On-Prem** | optional ([O], INS-08) | Offline-Bundle, interne CA, kein ACME, kein KI-Add-on | **nicht G1-gegatet**: Nach CH8 darf kein Aufwand für Air-Gapped die Phase-1-Gates gefährden. Ein fehlendes Air-Gapped-Profil ist kein G1-Befund. |

Alle Anforderungen gelten für alle Modelle, sofern der Katalog sie nicht als
„nur SaaS“ oder „nur Air-Gapped“ kennzeichnet (PRI-11).

## Uploads und Malware-Scan (SEC-09, E-42)

Dokumente und Importdateien (Module `documents` und `import`, im
Standard abgeschaltet) werden vor dem ersten Download mit ClamAV geprüft
(Jobart `document.scan`, WP-734). Der Dienst `clamd` ist optional wie die
Module; ohne erreichbaren clamd bleiben neue Versionen `pending_scan`
(fail-closed), der Sweep `document.scan_sweep` scannt sie nach, sobald clamd
wieder antwortet. Außerhalb von `development` startet der Server mit dem
Modul `documents` nur mit `RETICORA_CLAMD_ADDR` und der Job-Queue
(`RETICORA_NATS_URL`).

| Profil | clamd | Signaturen |
|---|---|---|
| Entwicklung (Root-`docker-compose.yml`) | Dienst `clamd`, Port `localhost:3310` (Default von `RETICORA_CLAMD_ADDR` in `development`) | freshclam im Container |
| Compose-Betriebsstack (`deploy/docker-compose`) | Dienst `clamd` mit Profil `documents` (`COMPOSE_PROFILES=documents` in `.env`); der Server hat `RETICORA_CLAMD_ADDR=clamd:3310`; Port nicht veröffentlicht | freshclam im Container, Volume `clamdata` |
| Kubernetes (`deploy/k8s`) | Komponente `base/components/clamav` (Deployment und Service `clamd`, setzt `RETICORA_CLAMD_ADDR` am Server), eingebunden im Overlay `prod` | freshclam im Container, PVC `clamd-signatures` |
| Air-Gapped On-Prem | wie Compose bzw. Kubernetes | kein freshclam (`RETICORA_CLAMAV_NO_FRESHCLAMD=true` in `.env` bzw. `CLAMAV_NO_FRESHCLAMD=true` per Overlay); Offline-Signaturbundle |

**Offline-Signaturbundle (Air-Gapped):** Auf einem Rechner mit Internetzugang
lädt `freshclam --datadir=<verzeichnis>` (oder `cvdupdate`) die Dateien
`main.cvd`, `daily.cvd` und `bytecode.cvd`. Das Verzeichnis wird mit
`sha256sum` dokumentiert, auf das Zielsystem übertragen und in das
Signatur-Volume kopiert; anschließend clamd neu starten. Compose: `docker
compose cp <verzeichnis>/. clamd:/var/lib/clamav/`; das Volume `clamdata`
erhält beim ersten Start die Signaturen, die das Image mitbringt. Kubernetes:
Das PVC `clamd-signatures` ist anfangs leer, und ohne `main.cvd` versucht der
Container einen ersten Download. Das Bundle wird deshalb vor dem ersten Start
in das PVC kopiert (z. B. über einen Hilfs-Pod mit dem PVC und `kubectl cp`),
spätere Bundles mit `kubectl cp` in den Pod `clamd`. Ohne Aktualisierung
veralten die Signaturen; das Bundle sollte mindestens wöchentlich erneuert
werden.

**Größenlimits:** Das Uploadlimit setzt `RETICORA_DOCUMENT_MAX_UPLOAD_MB`
(Standard 50 MiB, WP-268). Davor stehen:

- nginx des Frontends: `client_max_body_size 51m` im `http`-Block von
  `frontend/nginx.conf` (Limit + 1 MiB), gilt auch für den TLS-Server. Wird
  `RETICORA_DOCUMENT_MAX_UPLOAD_MB` erhöht, muss der Wert mitwachsen (eigenes
  Frontend-Image oder vorgeschalteter Proxy), sonst antwortet nginx mit einem
  HTML-413, bevor der Server die Anfrage sieht.
- clamd: `StreamMaxLength` und `MaxFileSize` (Standard `51M`, Compose:
  `RETICORA_CLAMD_STREAM_MAX_LENGTH`) und `MaxScanSize` (`200M`,
  `RETICORA_CLAMD_MAX_SCAN_SIZE`) mindestens so groß wie das Uploadlimit. Ein
  größerer Stream wird von clamd abgewiesen und nie als `clean` gewertet.
- **Kubernetes-Ingress:** Das Repository liefert keinen Ingress
  (`deploy/k8s/base/service.yaml`: ClusterIP). Ein vorgeschalteter Ingress
  muss große Uploads durchlassen, für ingress-nginx z. B.:
  `nginx.ingress.kubernetes.io/proxy-body-size: "51m"` (Body-Size),
  `nginx.ingress.kubernetes.io/proxy-request-buffering: "on"` (der Body wird
  vor der Weitergabe vollständig gepuffert; `off` reicht ihn gestreamt weiter
  und spart Plattenplatz am Ingress) sowie die Timeouts
  `nginx.ingress.kubernetes.io/proxy-read-timeout` und
  `nginx.ingress.kubernetes.io/proxy-send-timeout` (Annotationen) und
  `client-body-timeout` (ConfigMap des Controllers) von mindestens `300`
  Sekunden, passend zu `RETICORA_DOCUMENT_UPLOAD_TIMEOUT` (Standard 5 min). Andere
  Ingress-Controller brauchen die entsprechenden Einstellungen.

Rückstau und Fehlerrate des Scans melden die Alarme
`ReticoraDocumentScanBacklog` und `ReticoraDocumentScanFailing`
(`deploy/monitoring/worker-rules.yaml`, `docs/ops/workers.md`); den
Speicherbedarf nennt [`docs/ops/sizing.md`](../ops/sizing.md).

## Datenresidenz (NFR-09)

Im SaaS-Betrieb liegen alle Kundendaten in der EU. Die Tabelle nennt jeden
Datenort und seine Sicherung:

| Datenort | Inhalt | Regel | Durchsetzung |
|---|---|---|---|
| Primärregion | PostgreSQL/TimescaleDB, Objektspeicher (S3/MinIO), Redis, NATS, Anwendung | EU-Region | Terraform `region` |
| Backups | Basisbackups, WAL-Archiv, logische Dumps (`docs/backup-dr.md`) | EU-Region, Standard: Primärregion | Terraform `backup_region` |
| Logs und Telemetrie | Anwendungs-, Audit- und Zugriffslogs, OTLP-Export | EU-Region, Standard: Primärregion | Terraform `log_region`; `RETICORA_OTEL_ENDPOINT` darf nur auf EU-Ziele zeigen |
| Suchindex | OpenSearch (optional, `docs/opensearch.md`) | EU-Region, Standard: Primärregion | Terraform `search_region` |
| Collector | Offline-Spool im Kundennetz | beim Kunden | Kundeninfrastruktur |

`deploy/terraform/variables.tf` lässt für alle vier Variablen nur Regionen
innerhalb der EU zu. London und Zürich sowie andere Standorte außerhalb der EU
sind bewusst ausgeschlossen. Der CI-Job `terraform` prüft, dass EU-Regionen
angenommen und Nicht-EU-Regionen abgewiesen werden. Neue Datenorte, etwa ein
externer Log-Dienst, brauchen eine eigene validierte Region-Variable und einen
Eintrag in der Subprozessorenliste.

„Klartext verlässt nie die EU“ gilt für die SaaS-Serverseite (COL-02). Im
Collector existieren Zugangsdaten nur im Speicher beim Kunden.

## Subprozessoren

Die Liste steht in [`docs/legal/subprozessoren.md`](../legal/subprozessoren.md).
Ihren Inhalt liefert der Betreiber (E-35, Aufgabe A-11). Bis zur Lieferung ist
NFR-09 nur für den technischen Teil (Datenresidenz) erfüllt.
