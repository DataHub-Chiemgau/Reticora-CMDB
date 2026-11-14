# AVV-Anlage: Technische und organisatorische Maßnahmen (SEC-05 [P3])

> **Status (2026-11-14, WP-1159, [E-242](../plan/phase3/entscheidungen.md#e-242)):**
> Diese Anlage zur Auftragsverarbeitungsvereinbarung (AVV, Art. 28 DSGVO)
> beschreibt, was Reticora CMDB als Software und der SaaS-Betrieb technisch
> leisten, und verweist für jede Maßnahme auf ihren Nachweis im Repository.
> Die **Rechtsprüfung** der AVV und dieser Anlage liegt beim Auftraggeber;
> diese Datei ist keine Rechtsberatung. Zertifizierungen (z. B. ISO 27001,
> C5) sind nicht Gegenstand dieser Anlage.

## 1. Gegenstand der Verarbeitung

Reticora CMDB verwaltet für eine Organisation (den Verantwortlichen) deren
IT-Bestand: Configuration Items und ihre Beziehungen, Assets, Standorte,
Tickets, Dokumente, IPAM, Monitoring, Alarmierung und die Anbindung an
ADMP. Der Betreiber verarbeitet die Daten im Auftrag der Organisation nur zur
Erbringung dieser Leistung.

Betriebsmodelle (PRI-11, [`docs/betrieb/referenzmodell.md`](../betrieb/referenzmodell.md)):

| Betriebsmodell | Verantwortlich für Standort und Betrieb |
|---|---|
| SaaS (EU) mit Collector beim Kunden | Betreiber; alle Datenorte in der EU (NFR-09) |
| Dedicated Instance | Betreiber der Instanz |
| Air-Gapped On-Prem | Betreiber der Instanz (in der Regel der Kunde) |

## 2. Kategorien der Daten und der betroffenen Personen

| Kategorie | Beispiele | Ablage |
|---|---|---|
| Nutzer der Organisation | E-Mail, Anzeigename, Sprache, Mobilnummer (WP-1140), Rollen, Sitzungen | `app_user`; Mobilnummer nur als Vault-Subjekt |
| Kontakte | Name, E-Mail, Telefon, Rolle | nur im PII-Vault (`pii_subject`, AUD-01) |
| Anschlussinhaber aus ADMP | Name, Firma, Anschrift von Privatanschlüssen | nur im PII-Vault (WP-1076, E-243) |
| Personenbezogene Werte der Historie | Werte in Audit, CI-Historie, Ereignissen | nur als Surrogat `{"$pii": …}` (SEC-05) |
| Technische Bestandsdaten | CIs, Netze, Messwerte, Probes | Tabellen der Organisation unter RLS |
| Zugangsdaten | SNMP, SSH, API-Tokens, Provider-Konten | verschlüsselt (`credential`, COL-02) |

Betroffene Personen: Mitarbeitende und Dienstleister der Organisation,
Ansprechpartner ihrer Kunden, Anschlussinhaber aus ADMP.

## 3. Technische und organisatorische Maßnahmen

| Maßnahme | Umsetzung | Nachweis |
|---|---|---|
| Verschlüsselung von Zugangsdaten (COL-02) | Envelope-Verschlüsselung: Daten unter einem DEK je Organisation, DEK mit dem Master-Key gewrappt; Rotation; Klartext nur im Speicher | `backend/internal/credential/`, `backend/internal/platform/crypto/`, `docs/security/threat-model.md` |
| PII-Vault (SEC-05) | Personenbezogene Daten verschlüsselt in `pii_subject` unter dem Org-DEK; alle anderen Tabellen nur mit Surrogat; Blind-Index für die Suche | `backend/internal/pii/` (`pii_integration_test.go`, `surrogate_integration_test.go`) |
| Mandantentrennung (TEN-03) | PostgreSQL Row Level Security (FORCE) auf jeder Mandantentabelle, Anwendungsrolle ohne BYPASSRLS, zusammengesetzte Fremdschlüssel mit `organization_id` | `backend/internal/tenant/rls/` (`TestRLSCatalog`), `backend/internal/tenant/rls/channel_matrix_integration_test.go` |
| Löschung einer Organisation (TLC-06) | Antrag mit MFA und Slug, 30 Tage Karenz (nur Lesen), danach Purge aller Tabellen, des Vaults (Crypto-Shredding: zuerst `org_dek`), der Objekte, des Suchindex, des Event-Streams und der Caches; Löschprotokoll per E-Mail und im `operator_audit` | WP-1152 bis WP-1156: `backend/internal/tenantapi/deletion_integration_test.go`, `purge_integration_test.go`, `purge_external_test.go`, `purge_protocol_test.go`; Prüfskript `tests/org-purge-check.sql` |
| Vollexport (TLC-05, Art. 20) | Export aller Tabellen und Dokumente mit MFA; personenbezogene Daten entsiegelt ([E-165](../plan/phase3/entscheidungen.md#e-165)); 24 h abrufbar, signierte URLs höchstens 15 min, danach gelöscht | WP-1149 bis WP-1151: `backend/internal/export/` |
| Betroffenenrechte (Art. 15, 17) | Auskunft über alle Surrogat-Referenzen; Löschung durch Crypto-Shredding oder Pseudonymisierung; historische Daten legen gelöschte PII nicht offen (CH16) | WP-1158: `backend/internal/privacy/subject_integration_test.go`; WP-1159: `backend/internal/pii/history_redaction_integration_test.go` |
| Audit (AUD-04) | Hash-verkettetes `audit_log` je Organisation, personenbezogene Werte nur als Surrogat, Prüfung mit `audit-verify`; Betreiberzugriffe in eigener Kette `operator_audit` | `backend/internal/audit/`, `backend/cmd/audit-verify/`, `backend/internal/operator/audit_integration_test.go` |
| Zugangskontrolle | OIDC mit MFA für Betreiber, Rollen und Rechte je Client/Site/Team (RBA), Sitzungswiderruf | `backend/internal/identity/`, `backend/internal/server/authz.go`, `backend/internal/server/testdata/route_permissions.txt` |
| Ausgehende Verbindungen (SEC-08) | Egress-Policy für Webhooks, SMTP, Provider und Tresore; keine internen Ziele | `backend/internal/platform/egress/` |
| Datenresidenz (NFR-09) | Alle SaaS-Datenorte in der EU, durch Terraform-Variablen erzwungen und im CI-Job `terraform` geprüft | [`docs/betrieb/referenzmodell.md`](../betrieb/referenzmodell.md), `deploy/terraform/variables.tf` |
| Verfügbarkeit und Wiederherstellung | Basisbackups, WAL-Archiv, Restore-Test | `docs/backup-dr.md` |

## 4. Unterauftragsverarbeiter

Die Liste steht in [`docs/legal/subprozessoren.md`](../legal/subprozessoren.md).
Sie ist leer, solange der Betreiber keinen Dienstleister einsetzt
([E-35](../plan/entscheidungen.md#e-35), [E-103](../plan/phase2/entscheidungen.md#e-103)).
SMS-, Pager- und ADMP-Provider sind kundeneigene Konten der Organisation und
keine Unterauftragsverarbeiter des Betreibers
([E-242](../plan/phase3/entscheidungen.md#e-242)). Für Provider mit Sitz
außerhalb der EU ist ein Drittlandhinweis in der Kanalkonfiguration geplant
(kommt mit WP-1142 und WP-1148; diese Anlage nennt ihn erst nach deren
Abschluss als umgesetzte Maßnahme). Der Kanalumfang folgt [E-163](../plan/phase3/entscheidungen.md#e-163).

## 5. Löschung und Rückgabe nach Vertragsende

Die Organisation exportiert ihre Daten (Vollexport) und beantragt die
Löschung; nach der Karenz von 30 Tagen sind alle Daten aus Datenbank,
Objektspeicher, Suchindex, Event-Stream und Caches gelöscht und die
personenbezogenen Daten durch das Löschen des Organisationsschlüssels
unwiederbringlich. Der Antragsteller erhält das Löschprotokoll mit
Zeitpunkt, Umfang und Hashes per E-Mail. Backups enthalten die Daten bis zum
Ende ihrer Aufbewahrung und werden danach überschrieben.
