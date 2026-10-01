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
