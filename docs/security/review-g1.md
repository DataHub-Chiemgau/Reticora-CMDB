# Internes Sicherheitsreview Phase 1 (G1)

WP-209 (SEC-11, GATE-03). Prüfrahmen: OWASP ASVS Level 2 nach SEC-11.
Grundlagen:

- Threat-Model `docs/security/threat-model.md`
- Befundliste des Audits `docs/audit/befunde.csv`
- Implementierungsplan und Traceability `docs/traceability.csv`
- Scan-Jobs nach `docs/security/abhaengigkeitsscans.md`

Prüfstand: `main` nach dem Lasttest (PR #60) zuzüglich WP-208 (Kanalmatrix,
Migration 000136).

## Ergebnis

- **Audit-Befunde:** Von 225 Critical/High-Befunden sind alle G1-Anteile
  geschlossen oder warten nur noch auf ihren Gate-Nachweis (WP-210 bis
  WP-213).
  - 29 High-Befunde betreffen ausschließlich Spätphasen-Module. Sie sind nach
    E-03 zurückgestellt; die Module sind im G1-Release über WP-214
    abgeschaltet.
  - Bei 15 weiteren Befunden ist der G1-Anteil geschlossen, ihr
    Spätphasen-Anteil ist ebenfalls zurückgestellt.
- **Neue Befunde:** Das Review ergibt sieben neue Befunde (unten).
  - Behoben: zwei High (mandantenübergreifende Client-Verweise; Discovery
    überschreibt einen gleichzeitig gesetzten manuellen Wert).
  - Offen:
    - ein High (Ingest-Durchsatz NFR-03, Verfügbarkeit)
    - drei Medium
    - ein Info
- **Scans:** govulncheck, npm audit und Trivy sind ohne blockierende Funde, und
  es gibt keine Ausnahmen.
- **Pentest:** Der externe Pentest (WP-210, E-28) steht aus.

**Freigabe des Sicherheitsreviews:** Es ist noch nicht freigegeben. Das
Ausstiegskriterium „keine offenen High/Critical“ ist erst mit dem Folge-WP zu
SR-02 und dem Pentest-Nachweis erfüllt.

## Audit-Befunde Critical/High

| Severity | geschlossen | G1-Anteil geschlossen, Rest zurückgestellt (E-03) | zurückgestellt (E-03) | Gate-Nachweis ausstehend | offen |
|---|---:|---:|---:|---:|---:|
| Critical | 41 | 8 | 0 | 0 | 0 |
| High | 132 | 7 | 29 | 8 | 0 |

Die Spalten bedeuten:

- **geschlossen:** Alle WPs, die dem Befund im Plan zugeordnet sind, sind
  gemergt; der Nachweis steht in `docs/traceability.csv`.
- **Gate-Nachweis ausstehend:** Der Befund ist bis auf ein Gate-WP geschlossen.
  Das betrifft REP-01, INS-01, SEC-11, GLO-11, DOD-01, GATE-02, GATE-03 und
  ABN-01, die WP-209 bis WP-213 belegen. SEC-11 belegt dieses Dokument.
- **zurückgestellt (E-03):** Die Phasen-Tags liegen ab P2 bzw. bei [A]. Die
  Module sind im G1-Release abgeschaltet, ihre Routen sind nicht registriert,
  und ihre Worker laufen nicht (`backend/internal/server/modules_test.go`).

Die vollständige Liste steht im Anhang.

## Scanergebnisse (SEC-11, E-31)

Lauf: GitHub Actions, Run 38510025931 (PR #60, Stand vor WP-208), 2026-10-14.

| Job | Gegenstand | Ergebnis |
|---|---|---|
| govulncheck (backend, collector, edgecore) | Go-Abhängigkeiten, erreichbare Schwachstellen | grün, keine erreichbaren Funde |
| npm-audit | Produktionsabhängigkeiten des Frontends | grün, keine High/Critical |
| image-scan (backend, collector, frontend) | Container-Images (Trivy, Critical/High mit Fix) | grün |

`security/scan-exceptions.txt` enthält keine Ausnahmen. Die Berichte liegen als
Artefakte des Laufs vor (30 Tage).

## ASVS Level 2 – Abgleich je Kapitel

| Kapitel | Umsetzung in G1 | Nachweis |
|---|---|---|
| V1 Architektur | Threat-Model, Mandantentrennung in der DB (RLS FORCE), ein DB-Einstieg `WithTenant` | `docs/security/threat-model.md`, `backend/internal/tenant/rls/` |
| V2 Authentisierung | OIDC mit PKCE über Keycloak, MFA für Operatoren, API-Schlüssel mit Hash und Ablauf | T1, T2, T15 |
| V3 Sessions | RS256-JWT mit 15 min, Refresh-Rotation, Widerruf über Redis | T1 |
| V4 Zugriffskontrolle | Routen-Rechte-Tabelle (fail closed), Scopes je Client/Site/Team, Kanalmatrix | T6, T7, `backend/internal/server/authz_test.go` |
| V5 Validierung | OpenAPI-Validierung der Anfragen, typisierte Fehler (RFC 7807) | `backend/internal/middleware/` (Validation) |
| V7 Logging | Audit-Kette, Redaktion personenbezogener Daten | T16, T17 |
| V8 Datenschutz | PII-Vault, Envelope-Encryption der Credentials | T17 |
| V9 Kommunikation | TLS überall, mTLS für Collector | T10 |
| V10 Schadcode/Lieferkette | Scans blockierend, signierte Releases | T19 |
| V12 Dateien | Uploads (SEC-09) gehören zu P2; Dokumente sind im G1-Release abgeschaltet | WP-214 |
| V13 API | Rate-Limits, Idempotenz, GraphQL-Limits, keine Introspection in production | T3, T4, T5 |
| V14 Konfiguration | Fail-closed-Start außerhalb development, Feature-Gates je Modul | `backend/internal/config/`, WP-214 |

## Neue Befunde

| ID | Severity | Status | Befund | Maßnahme |
|---|---|---|---|---|
| SR-01 | High | **behoben** (WP-208) | Mandantenübergreifende Client-Verweise: Fremdschlüssel auf `client` prüfen nur die ID und laufen ohne RLS. Organisation B konnte Standorte, CIs, Kontakte und Subnetze mit einem Client von Organisation A anlegen. | Migration 000136: alle 21 Verweise auf `client` mit Organisation; Kanalmatrix und `TestClientReferencesCarryOrganization` verhindern Regressionen. |
| SR-07 | High | **behoben** (WP-207-Nachtrag) | Discovery während einer manuellen Änderung: Die zentrale Schreibentscheidung lief vor der Schreibtransaktion. Ein Override, das dazwischen committet wurde, schützte den Wert nicht, und `ci.model` trug den Discovery-Wert trotz geschütztem Override (gefunden von `TestDiscoveryConcurrency`). | Das CI-Repository entfernt Felder mit Override aus automatisierten Schreibvorgängen unter der Zeilensperre (`dropOverriddenFields`); deterministischer Test `TestAutomatedWriteKeepsOverride`. |
| SR-02 | High | offen | Ingest-Durchsatz 58 statt 200 Records/s je Instanz (NFR-03). Unter Dauerlast staut sich die Queue, die Verzögerung steigt über 30 s (Verfügbarkeit). Datenverlust tritt nicht auf, die Queue ist persistent. | WP-332 bis WP-334 (G1-Nachbesserung): eine Transaktion je Record, Batch-Schreibpfad, parallele Spuren je Identitätsschlüssel (`docs/acceptance/lasttest-g1.md`, E-56). |
| SR-03 | Medium | offen | Von 194 Fremdschlüsseln zwischen Mandantentabellen enthalten 178 die Organisation nicht. Die Anwendung prüft referenzierte Objekte unter RLS; die Kanalmatrix belegt das für 13 Schreibpfade (Standorte, Racks, Mounts, CIs, Kanten, Kontakte, Subnetze, Scopes, Rollen). Die Datenbank allein verhindert fremde Verweise nicht (Defense in Depth). | Folge-WP: zusammengesetzte Fremdschlüssel nach dem Muster von 000136, priorisiert nach Schreibpfaden. |
| SR-04 | Medium | offen | Ausdrucksindizes mit nicht-leakproof Funktionen sind unter RLS für die App-Rolle unbenutzbar. `idx_ci_fts` (`to_tsvector`) führt zu linearem Aufwand bei der Volltextsuche (Verfügbarkeit bei großen Beständen). Für Hostname und FQDN ist das mit 000134 behoben. | Folge-WP: gespeicherte `tsvector`-Spalte mit Index; Katalogtest gegen neue Ausdrucksindizes auf RLS-Tabellen. |
| SR-05 | Medium | offen | Impact im Worst Case (ganze Organisation, 10.000 CIs) unter Parallellast p95 2,39 s statt < 2 s; Antwort ca. 5 MB. | WP-335 (G1-Nachbesserung): adaptive Kantenlesung, schlanke Knotenprojektion, Pfade nur auf Anfrage; Messung auf dem Referenzhost bleibt SR-06. |
| SR-06 | Info | offen | Der Lasttest lief nicht auf einem dedizierten Referenzhost. | Nächtlicher Lauf `load.yml`; Referenzhost vor GA. |

## Externer Pentest

WP-210: Nach E-28 beauftragt der Auftraggeber den Pentest (Aufgabe A-07).
Bericht und Nachtest werden in `docs/security/pentest-g1.md` zusammengefasst.
Ohne diesen Nachweis ist GATE-03 offen.

## Anhang: Critical/High-Befunde des Audits

| ID | Tag | Severity | Status | WPs |
|---|---|---|---|---|
| AGT-03 | [P4] | Critical | geschlossen | WP-051 |
| AGT-06 | [P4] | Critical | geschlossen | WP-038 |
| AI-01 | [A] | Critical | geschlossen | WP-037 |
| API-04 | [B] | Critical | geschlossen | WP-050, WP-072, WP-166 |
| API-07 | [B] | Critical | geschlossen | WP-058, WP-061 |
| AUT-01 | [B] | Critical | geschlossen | WP-042, WP-043 |
| AUT-02 | [B] | Critical | geschlossen | WP-042, WP-066 |
| AUT-09 | [B] | Critical | geschlossen | WP-043, WP-044 |
| CI-04 | [B] | Critical | geschlossen | WP-057 |
| CI-10 | [B] | Critical | geschlossen | WP-045, WP-058, WP-060, WP-091 |
| COL-02 | [B] | Critical | geschlossen | WP-047, WP-048, WP-147 |
| COL-05 | [B] | Critical | geschlossen | WP-062, WP-143 |
| EXP-01 | [B]; DATEV [P2] | Critical | geschlossen | WP-035, WP-189 |
| GQL-04 | [B] | Critical | geschlossen | WP-036 |
| IMP-07 | [B] | Critical | geschlossen | WP-025, WP-031 |
| MGT-01 | [P2] | Critical | geschlossen | WP-028, WP-030 |
| MGT-03 | [P4] | Critical | geschlossen | WP-019, WP-023, WP-028 |
| MGT-05 | [P4] | Critical | geschlossen | WP-029 |
| MGT-06 | [P4] | Critical | geschlossen | WP-029 |
| MGT-07 | [P4] | Critical | geschlossen | WP-029 |
| MGT-11 | [P5] | Critical | geschlossen | WP-017, WP-023 |
| MON-01 | [B] | Critical | geschlossen | WP-039, WP-040 |
| NFR-04 | [B]; Teilumfang [P5] | Critical | geschlossen | WP-062, WP-063 |
| OPS-06 | [Q] | Critical | geschlossen | WP-062, WP-104 |
| OVR-01 | [B] | Critical | geschlossen | WP-057, WP-058, WP-060 |
| OVR-02 | [B] | Critical | geschlossen | WP-059 |
| RBA-02 | [B] | Critical | geschlossen | WP-046 |
| RBA-03 | [B] | Critical | geschlossen | WP-009 |
| RBA-04 | [B] | Critical | geschlossen | WP-045 |
| SEC-01 | nicht angegeben | Critical | geschlossen | WP-047 |
| SEC-08 | nicht angegeben | Critical | geschlossen | WP-049 |
| SRC-01 | [B] | Critical | geschlossen | WP-025, WP-032, WP-186 |
| SRC-04 | [P2] | Critical | geschlossen | WP-034 |
| TEC-06 | [B]; [Q] Spike | Critical | geschlossen | WP-026, WP-039, WP-040 |
| TEC-12 | [P5] | Critical | geschlossen | WP-027, WP-033 |
| TEN-02 | [B] | Critical | geschlossen | WP-024, WP-025, WP-026 |
| TEN-04 | [B] | Critical | geschlossen | WP-008, WP-009, WP-027 |
| TEN-05 | [B] | Critical | geschlossen | WP-007, WP-022, WP-023, WP-024, WP-025, WP-027, WP-029 |
| TEN-06 | [B] | Critical | geschlossen | WP-008, WP-010, WP-011, WP-012, WP-013, WP-014, WP-015, WP-016, WP-017, WP-018, WP-019, WP-020, WP-021, WP-022, WP-030, WP-035, WP-041 |
| TKT-01 | [P2]; Pro [P4]; Monitoring/Findings/Automationen [P3] | Critical | geschlossen | WP-020, WP-028, WP-029 |
| TLC-04 | [B] | Critical | geschlossen | WP-042 |
| AGT-05 | [P4] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-059, WP-220 |
| AST-01 | [P2] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-017, WP-023, WP-222 |
| AST-05 | [P2] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-052, WP-079, WP-222 |
| MGT-02 | [P2] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-028, WP-224 |
| MGT-04 | [P3] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-020, WP-028, WP-063, WP-228 |
| SRC-03 | [P2]; Textsyntax [O] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-034, WP-187, WP-236 |
| WFL-01 | [P3] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-021, WP-023, WP-231 |
| WFL-02 | [P3] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-059, WP-231 |
| ABN-02 | [Q] | High | geschlossen | WP-204, WP-206 |
| ABN-03 | [Q]; jeweilige Phase | High | geschlossen | WP-207 |
| AI-02 | [A] | High | geschlossen | WP-075, WP-076 |
| API-05 | [B] | High | geschlossen | WP-067, WP-069, WP-070, WP-074, WP-088, WP-107, WP-111, WP-114, WP-116, WP-125, WP-139, WP-153, WP-168, WP-169 |
| API-06 | [P2]/[P3]/[P4]/[A] | High | geschlossen | WP-075, WP-214 |
| API-08 | [B] | High | geschlossen | WP-181 |
| API-09 | [B] | High | geschlossen | WP-182 |
| AUD-03 | nicht angegeben | High | geschlossen | WP-090, WP-171 |
| AUD-04 | nicht angegeben | High | geschlossen | WP-132, WP-241 |
| AUD-05 | nicht angegeben | High | geschlossen | WP-133 |
| AUD-07 | nicht angegeben | High | geschlossen | WP-134 |
| AUD-09 | nicht angegeben | High | geschlossen | WP-133 |
| AUT-04 | [B] | High | geschlossen | WP-012, WP-067 |
| AUT-10 | [B] | High | geschlossen | WP-050 |
| BLK-01 | [B] | High | geschlossen | WP-181 |
| CI-01 | [B], Teilfelder [P2]/[P3] | High | geschlossen | WP-120 |
| CI-03 | [B] | High | geschlossen | WP-117 |
| CI-05 | [B] | High | geschlossen | WP-031, WP-169, WP-170 |
| CI-11 | [B] | High | geschlossen | WP-172 |
| CI-12 | [B] | High | geschlossen | WP-173, WP-174 |
| CI-13 | [B] | High | geschlossen | WP-087 |
| COL-01 | [B] | High | geschlossen | WP-143 |
| COL-04 | [B] | High | geschlossen | WP-144, WP-145 |
| COL-06 | [B], Air-Gapped [O] | High | geschlossen | WP-102, WP-148 |
| COL-08 | [B] | High | geschlossen | WP-146 |
| DIS-01 | [B] | High | geschlossen | WP-149 |
| DIS-02 | [B], Profilliste (V) | High | geschlossen | WP-151 |
| DIS-03 | [B] | High | geschlossen | WP-150 |
| DIS-04 | [B] | High | geschlossen | WP-150, WP-152 |
| DIS-05 | [B] | High | geschlossen | WP-083 |
| DIS-09 | [B] | High | geschlossen | WP-153 |
| DIS-10 | [B] | High | geschlossen | WP-154 |
| ENT-01 | [B] | High | geschlossen | WP-071 |
| ENT-02 | [B] | High | geschlossen | WP-071 |
| ENT-03 | [B] | High | geschlossen | WP-072, WP-073 |
| ENT-04 | [B] | High | geschlossen | WP-074 |
| ENT-05 | [B] | High | geschlossen | WP-072 |
| ENT-06 | (V), [B–P4], [A] | High | geschlossen | WP-075 |
| ENT-07 | [B] | High | geschlossen | WP-072 |
| ENT-08 | [B] | High | geschlossen | WP-073 |
| EVT-01 | nicht angegeben | High | geschlossen | WP-136 |
| EVT-02 | nicht angegeben | High | geschlossen | WP-136 |
| GLO-12 | [B] | High | geschlossen | WP-083, WP-084 |
| GLO-13 | [B] | High | geschlossen | WP-091 |
| GQL-01 | [B] | High | geschlossen | WP-184 |
| GQL-02 | [B] | High | geschlossen | WP-185 |
| IMP-01 | [B] | High | geschlossen | WP-176 |
| IMP-02 | [B] | High | geschlossen | WP-177 |
| IMP-03 | [B] | High | geschlossen | WP-176 |
| IMP-04 | [B] | High | geschlossen | WP-179 |
| IMP-05 | [B] | High | geschlossen | WP-180 |
| IMP-06 | [B] | High | geschlossen | WP-180 |
| IMP-09 | [B] | High | geschlossen | WP-161 |
| INS-02 | [B] | High | geschlossen | WP-095 |
| INS-03 | [B] | High | geschlossen | WP-094 |
| INS-04 | [Q] | High | geschlossen | WP-200 |
| INS-05 | [B] | High | geschlossen | WP-096 |
| INS-06 | [B] | High | geschlossen | WP-156 |
| JOB-01 | [B] | High | geschlossen | WP-129 |
| JOB-02 | [B]; Zusätze [P2], [P3] | High | geschlossen | WP-131 |
| JOB-03 | [B] | High | geschlossen | WP-182 |
| JOB-04 | [B] | High | geschlossen | WP-183 |
| LCY-05 | [B] | High | geschlossen | WP-157 |
| MON-02 | [B] | High | geschlossen | WP-190 |
| NET-01 | [B] | High | geschlossen | WP-081, WP-090, WP-123 |
| NET-02 | [B] | High | geschlossen | WP-081, WP-082 |
| NET-03 | [B] | High | geschlossen | WP-082, WP-124 |
| NET-05 | [B] | High | geschlossen | WP-125 |
| NET-08 | [B] | High | geschlossen | WP-081 |
| NFR-01 | [B]; Teilumfang [P5] | High | geschlossen | WP-086, WP-205, WP-206 |
| NFR-02 | [B] | High | geschlossen | WP-177, WP-206 |
| NFR-03 | [B]; Teilumfang [P5] | High | geschlossen | WP-085, WP-154, WP-206 |
| NFR-05 | [B] | High | geschlossen | WP-100, WP-101 |
| NFR-06 | [B] | High | geschlossen | WP-135 |
| NFR-07 | [B] | High | geschlossen | WP-193 |
| NFR-09 | [B] | High | geschlossen | WP-105 |
| NFR-10 | [Q] | High | geschlossen | WP-203, WP-204, WP-206 |
| NTF-01 | [B] | High | geschlossen | WP-137 |
| NTF-03 | [B] | High | geschlossen | WP-138 |
| NTF-04 | [B] | High | geschlossen | WP-139 |
| NTF-05 | [B]; Zusätze [P2], [P3] | High | geschlossen | WP-063, WP-140 |
| OPS-01 | [B] | High | geschlossen | WP-097 |
| OPS-02 | [B]; [P3]; [A] | High | geschlossen | WP-098 |
| OPS-03 | [B]; [P5] | High | geschlossen | WP-099, WP-131 |
| OPS-04 | [B]; [P5] | High | geschlossen | WP-100, WP-101 |
| OPS-05 | [B] | High | geschlossen | WP-022, WP-103, WP-140 |
| PRI-07 | Grundsatz (ohne Tag) | High | geschlossen | WP-146 |
| RBA-01 | [B] | High | geschlossen | WP-068 |
| RBA-05 | [P2–P4], [A] | High | geschlossen | WP-214 |
| RBA-06 | [P2] | High | geschlossen | WP-045 |
| RBA-08 | [B] | High | geschlossen | WP-069 |
| RCK-01 | [B] | High | geschlossen | WP-126 |
| RCK-02 | [B] | High | geschlossen | WP-175 |
| RCK-03 | [B] | High | geschlossen | WP-078 |
| REC-01 | [B] | High | geschlossen | WP-085 |
| REC-02 | [B] | High | geschlossen | WP-086 |
| REC-03 | [B] | High | geschlossen | WP-058 |
| REC-05 | [B] | High | geschlossen | WP-084 |
| REC-06 | [B] | High | geschlossen | WP-089 |
| REC-07 | [B] | High | geschlossen | WP-090 |
| REC-08 | [B] | High | geschlossen | WP-088 |
| REC-09 | [B] | High | geschlossen | WP-157 |
| REC-12 | [B] | High | geschlossen | WP-058, WP-089 |
| REL-01 | [B] | High | geschlossen | WP-079, WP-127 |
| REL-02 | [B] | High | geschlossen | WP-128 |
| REL-03 | [B] | High | geschlossen | WP-160 |
| REL-04 | [B] | High | geschlossen | WP-160 |
| SEC-06 | nicht angegeben | High | geschlossen | WP-066, WP-141 |
| SEC-07 | nicht angegeben | High | geschlossen | WP-070 |
| SEC-10 | nicht angegeben | High | geschlossen | WP-184 |
| SIM-01 | [B]; Teilumfang [P2] | High | geschlossen | WP-106, WP-203 |
| SIM-02 | [B] | High | geschlossen | WP-162 |
| TEC-09 | [B] | High | geschlossen | WP-092, WP-093, WP-094 |
| TEC-13 | [B] | High | geschlossen | WP-137 |
| TEC-14 | [B] | High | geschlossen | WP-130 |
| TEC-15 | [B] | High | geschlossen | WP-102 |
| TEN-03 | [B] | High | geschlossen | WP-007, WP-064 |
| TEN-09 | [Q] | High | geschlossen | WP-007, WP-065, WP-208 |
| TEN-10 | [B] | High | geschlossen | WP-082 |
| TLC-01 | [B] | High | geschlossen | WP-111, WP-112 |
| TLC-02 | [B]; Reseller [P4] | High | geschlossen | WP-113 |
| TLC-03 | [B] | High | geschlossen | WP-114 |
| TOP-01 | [B] | High | geschlossen | WP-158 |
| TOP-02 | [B] | High | geschlossen | WP-159 |
| TST-01 | [B]; Teilumfang [P2] | High | geschlossen | WP-202 |
| TST-02 | [B] | High | geschlossen | WP-003, WP-065, WP-208 |
| TST-03 | [B]; Teilumfang [P2] | High | geschlossen | WP-200, WP-201 |
| TST-04 | [Q] | High | geschlossen | WP-001, WP-002, WP-003, WP-004 |
| UI-06 | [B] | High | geschlossen | WP-195 |
| UI-16 | [B] | High | geschlossen | WP-196, WP-197 |
| UI-17 | [B] | High | geschlossen | WP-199 |
| UI-18 | [B] | High | geschlossen | WP-193, WP-194 |
| ABN-01 | [Q] | High | geschlossen bis auf Gate-Nachweis | WP-200, WP-212 |
| AGT-01 | [P4] | High | verschoben (E-03) | WP-230 |
| AGT-02 | [P4] | High | verschoben (E-03) | WP-230 |
| AGT-04 | [P4] | High | verschoben (E-03) | WP-230 |
| AST-02 | [P2] | High | verschoben (E-03) | WP-223 |
| AST-04 | [P2] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-077, WP-222 |
| AST-06 | [P2] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-022, WP-223 |
| AST-07 | [P2] | High | verschoben (E-03) | WP-224 |
| AUT-08 | [P4] | High | verschoben (E-03) | WP-233 |
| COL-07 | [A], Relay [P4] | High | verschoben (E-03) | WP-219 |
| DOD-01 | [Q] | High | geschlossen bis auf Gate-Nachweis | WP-005, WP-213 |
| GATE-02 | [Q] | High | G1-Gate | WP-213 |
| GATE-03 | [Q] | High | geschlossen bis auf Gate-Nachweis | WP-092, WP-209, WP-210, WP-211 |
| GLO-11 | [Q] | High | geschlossen bis auf Gate-Nachweis | WP-005, WP-213 |
| IGA-01 | [A] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-075, WP-232 |
| IGA-02 | [A] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-075, WP-232 |
| IGA-03 | [A] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-075, WP-232 |
| IGA-04 | [A] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-075, WP-232 |
| IMP-08 | [P2] | High | verschoben (E-03) | WP-218 |
| INS-01 | [Q] | High | geschlossen bis auf Gate-Nachweis | WP-092, WP-211 |
| INV-01 | [P2] | High | verschoben (E-03) | WP-227 |
| INV-03 | [P2]; RFID/Direct-Access [P4] | High | verschoben (E-03) | WP-227 |
| LCY-02 | [P2] | High | verschoben (E-03) | WP-215 |
| LCY-03 | [P2] | High | verschoben (E-03) | WP-215 |
| MON-03 | [P3] | High | verschoben (E-03) | WP-234 |
| MON-04 | [P3] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-022, WP-234 |
| NET-09 | [P2] | High | verschoben (E-03) | WP-216 |
| OVR-03 | [P2] | High | verschoben (E-03) | WP-221 |
| REC-04 | [P3] | High | verschoben (E-03) | WP-220 |
| REC-11 | [P4] | High | verschoben (E-03) | WP-220 |
| REL-09 | [P2] | High | verschoben (E-03) | WP-217 |
| REP-01 | [B] | High | geschlossen bis auf Gate-Nachweis | WP-006, WP-118, WP-129, WP-137, WP-213 |
| SEC-11 | nicht angegeben | High | geschlossen bis auf Gate-Nachweis | WP-004, WP-209 |
| STK-01 | [P2] | High | verschoben (E-03) | WP-225 |
| STK-02 | [P2] | High | verschoben (E-03) | WP-225 |
| STK-03 | [P2] | High | verschoben (E-03) | WP-225 |
| STK-04 | [P2] | High | verschoben (E-03) | WP-224 |
| STK-05 | [P2] | High | verschoben (E-03) | WP-226 |
| TKT-02 | [P2] | High | verschoben (E-03) | WP-229 |
| UI-11 | [P2] | High | verschoben (E-03) | WP-236 |
| UI-13 | [P4] | High | verschoben (E-03) | WP-237 |
| UI-14 | [P2–P4, A] | High | verschoben (E-03) | WP-237 |
| UI-15 | [P2] | High | verschoben (E-03) | WP-235 |
| WFL-04 | [P3]; delegierte Inventur [P4]; IGA [A] | High | verschoben (E-03) | WP-231 |
