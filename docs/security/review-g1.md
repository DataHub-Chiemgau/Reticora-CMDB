# Internes Sicherheitsreview Phase 1 (G1)

WP-209 (SEC-11, GATE-03). Prüfrahmen: OWASP ASVS Level 2 nach SEC-11.
Grundlagen:

- Threat-Model `docs/security/threat-model.md`
- Befundliste des Audits `docs/audit/befunde.csv`
- Implementierungsplan und Traceability `docs/traceability.csv`
- Scan-Jobs nach `docs/security/abhaengigkeitsscans.md`
- Lastnachweis `docs/acceptance/lasttest-g1.md`

Prüfstand: Commit `dbbfded`, also die G1-Nachbesserung (WP-332 bis WP-335)
auf `02ebb76`, dazu die zwei Korrekturen aus dem Gesamtreview des
integrierten Stands (`58863fa`, `dea65b7`) und die Nachträge zu seinen nicht
blockierenden Befunden vom 2026-10-15 (`3f2bfd5`, `458a96d`, `111bdc4`,
`f354c5a`, `0f165cc`; Abschnitt „G1-Nachbesserung“).
Die Erstfassung prüfte `main` nach dem Lasttest (PR #60) zuzüglich WP-208
(Kanalmatrix, Migration 000136). Der Stand ist lokal geprüft; der CI-Lauf auf
dem Release-Kandidaten steht aus.

## Ergebnis

- **Audit-Befunde:** Von 225 Critical/High-Befunden sind alle G1-Anteile
  geschlossen oder warten nur noch auf ihren Gate-Nachweis (WP-210 bis
  WP-213).
  - 29 High-Befunde betreffen ausschließlich Spätphasen-Module. Sie sind nach
    E-03 zurückgestellt; die Module sind im G1-Release über WP-214
    abgeschaltet.
  - Bei 15 weiteren Befunden ist der G1-Anteil geschlossen, ihr
    Spätphasen-Anteil ist ebenfalls zurückgestellt.
  - NFR-02, NFR-03 und NFR-10 verfehlte der Erstlauf des Lasttests (WP-206).
    Der Wiederholungslauf nach der G1-Nachbesserung belegt sie.
- **Neue Befunde:** Das Review ergab sieben neue Befunde (SR-01 bis SR-07).
  Die G1-Nachbesserung behebt zwei davon und hinterlässt drei Restrisiken
  (SR-08 bis SR-10). Das Gesamtreview fand zusätzlich einen vorbestehenden
  Fehler (SR-11). Stand der elf Befunde (unten):
  - Behoben:
    - drei High (mandantenübergreifende Client-Verweise; Discovery
      überschreibt einen gleichzeitig gesetzten manuellen Wert;
      Ingest-Durchsatz NFR-03)
    - ein Medium (Impact im Worst Case, NFR-02)
  - Offen:
    - zwei Medium
    - vier Low (drei Restrisiken der G1-Nachbesserung, ein vorbestehender
      Fehler in der Decorator-Kette des CI-Repositorys)
    - ein Info (Referenzhost des Lasttests)
- **G1-Nachbesserung:** Die sicherheitsrelevanten Änderungen von WP-332 bis
  WP-335 sind mit Tests belegt (Abschnitt „G1-Nachbesserung“ unten). Die
  Schwächen, die die Reviews der WPs und das Gesamtreview des integrierten
  Stands fanden, sind behoben, darunter eine Injektion bei fehlkonfigurierter
  Client-Kodierung, unbegrenzte Advisory-Locks, die die Lock-Tabelle des
  Clusters füllen konnten, und Identitätssperren im Lock-Raum der
  Scheduler-Sperre (behoben in `58863fa`). Die nicht blockierenden Befunde
  des Gesamtreviews sind am 2026-10-15 nachgetragen, darunter fehlende
  Identitätssperren mit einer Spur über mehrere Instanzen; offen bleibt nur
  der vorbestehende SR-11.
- **Scans:** govulncheck, npm audit und Trivy sind ohne blockierende Funde, und
  es gibt keine Ausnahmen. Die G1-Nachbesserung ändert keine Abhängigkeiten;
  der Scan-Lauf auf dem Release-Kandidaten steht aus.
- **Pentest:** Der externe Pentest (WP-210, E-28) steht aus.

**Freigabe des Sicherheitsreviews:** Es ist noch nicht freigegeben. Unter den
Befunden SR-01 bis SR-11 ist kein High und kein Critical mehr offen; SR-02 ist
mit WP-332 bis WP-334 behoben. Das Ausstiegskriterium „keine offenen
High/Critical“ ist erst mit dem Pentest-Nachweis (WP-210, B3) erfüllt.

Für GATE-03 steht außerdem die Anerkennung der Referenzumgebung des
Lasttests aus (E-58, SR-06). Den Lasttest belegen der lokale Nachweislauf auf
Commit `dbbfded` und der grüne `load.yml`-Lauf 38665599979 auf Commit
`918a832`; nach dem Merge wiederholt der nächtliche Lauf ihn auf dem
Release-Kandidaten. G1 ist weiter nicht bestanden: Offen sind B3
(Pentest), B4 (v2-Texte) und B6 (Subprozessorenliste, NFR-09). B5 (E-17) ist
ebenfalls offen; gemessen ist mit 10.000 Objekten
(`docs/acceptance/g1-gate.md`).

## Audit-Befunde Critical/High

| Severity | geschlossen | G1-Anteil geschlossen, Rest zurückgestellt (E-03) | zurückgestellt (E-03) | Gate-Nachweis ausstehend | offen |
|---|---:|---:|---:|---:|---:|
| Critical | 41 | 8 | 0 | 0 | 0 |
| High | 132 | 7 | 29 | 8 | 0 |

Die Spalten bedeuten:

- **geschlossen:** Alle WPs, die dem Befund im Plan zugeordnet sind, sind
  gemergt oder im Prüfstand enthalten; der Nachweis steht in
  `docs/traceability.csv`.
- **Gate-Nachweis ausstehend:** Der Befund ist bis auf ein Gate-WP geschlossen.
  Das betrifft REP-01, INS-01, SEC-11, GLO-11, DOD-01, GATE-02, GATE-03 und
  ABN-01, die WP-209 bis WP-213 belegen. SEC-11 belegt dieses Dokument.
- **zurückgestellt (E-03):** Die Phasen-Tags liegen ab P2 bzw. bei [A]. Die
  Module sind im G1-Release abgeschaltet, ihre Routen sind nicht registriert,
  und ihre Worker laufen nicht (`backend/internal/server/modules_test.go`).

Die vollständige Liste steht im Anhang.

NFR-02, NFR-03 und NFR-10 verfehlte der Erstlauf des Lasttests (WP-206;
SR-02, SR-05). Der Plan ordnet ihnen und ABN-02 zusätzlich die
G1-Nachbesserung zu (WP-332 bis WP-335); der Anhang führt sie mit diesen WPs.
Der Wiederholungslauf auf Commit `dbbfded` belegt sie, der grüne
`load.yml`-Lauf 38665599979 auf Commit `918a832` bestätigt sie
(`docs/acceptance/lasttest-g1.md`). Die Anerkennung der Referenzumgebung
(E-58) steht aus.

## Scanergebnisse (SEC-11, E-31)

Lauf: GitHub Actions, Run 38510025931 (PR #60, Stand vor WP-208), 2026-10-14.

| Job | Gegenstand | Ergebnis |
|---|---|---|
| govulncheck (backend, collector, edgecore) | Go-Abhängigkeiten, erreichbare Schwachstellen | grün, keine erreichbaren Funde |
| npm-audit | Produktionsabhängigkeiten des Frontends | grün, keine High/Critical |
| image-scan (backend, collector, frontend) | Container-Images (Trivy, Critical/High mit Fix) | grün |

`security/scan-exceptions.txt` enthält keine Ausnahmen. Die Berichte liegen als
Artefakte des Laufs vor (30 Tage).

Die G1-Nachbesserung (WP-332 bis WP-335) ändert weder `go.mod`/`go.sum` noch
`package.json`/`package-lock.json` noch die Dockerfiles. Die Scan-Jobs laufen
im CI-Lauf des Release-Kandidaten erneut; dieser Lauf steht aus.

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
| SR-02 | High | **behoben** (WP-332 bis WP-334) | Ingest-Durchsatz im Erstlauf (WP-206) 58 statt 200 Records/s je Instanz (NFR-03). Unter Dauerlast staute sich die Queue, die Verzögerung stieg über 30 s (Verfügbarkeit). Datenverlust trat nicht auf, die Queue ist persistent. | G1-Nachbesserung: eine Transaktion je Record (WP-332), Batch-Schreibpfad für neue Geräte (WP-333), parallele Spuren mit Identitätssperre (WP-334; E-56 offen, umgesetzt ist die Empfehlung). Wiederholungslauf auf Commit `dbbfded` mit 4 Spuren (Standard): Abbau ab Sendeende 233 Records/s, Verzögerung Record → sichtbar höchstens 3,1 s bei 200 Records/s Angebot, Spool-Flush von 10.000 Records in 35,9 s, kein fehlender Record (`docs/acceptance/lasttest-g1.md`). Mit einer Spur (`RETICORA_INGEST_LANES=1`) 182 Records/s, das verfehlt NFR-03. Reserve: SR-08; Referenzumgebung: SR-06. |
| SR-05 | Medium | **behoben** (WP-335) | Impact im Worst Case (ganze Organisation, 10.000 CIs) im Erstlauf unter Parallellast p95 2,39 s statt < 2 s; Antwort ca. 5 MB. | Adaptive Kantenlesung, schlanke Knotenprojektion, `hops` und `via` je CI. Die Oberfläche fragt die kompakte Form über den Sparse-Fieldset `fields=` ab (API-02); die Standardantwort ist nur additiv erweitert (E-57, vorläufig entschieden). Wiederholungslauf auf Commit `dbbfded`: p95 477 ms (kompakte Form) und 546 ms (Standardform mit Pfaden); kompakte Antwort 2,39 MB (239 Byte je betroffenem CI). Referenzumgebung: SR-06. |
| SR-03 | Medium | offen | Von 194 Fremdschlüsseln zwischen Mandantentabellen enthalten 178 die Organisation nicht. Die Anwendung prüft referenzierte Objekte unter RLS; die Kanalmatrix belegt das für 13 Schreibpfade (Standorte, Racks, Mounts, CIs, Kanten, Kontakte, Subnetze, Scopes, Rollen). Die Datenbank allein verhindert fremde Verweise nicht (Defense in Depth). | Folge-WP: zusammengesetzte Fremdschlüssel nach dem Muster von 000136, priorisiert nach Schreibpfaden. |
| SR-04 | Medium | offen | Ausdrucksindizes mit nicht-leakproof Funktionen sind unter RLS für die App-Rolle unbenutzbar. `idx_ci_fts` (`to_tsvector`) führt zu linearem Aufwand bei der Volltextsuche (Verfügbarkeit bei großen Beständen). Für Hostname und FQDN ist das mit 000134 behoben. Dieselbe Ursache hat der Abgleich über Fingerprint-MACs im Ingest (`jsonb`-Containment): Er liest je Batch alle CIs der Organisation (WP-333; vorher je Record mit MACs). | Folge-WP: gespeicherte `tsvector`-Spalte mit Index; Katalogtest gegen neue Ausdrucksindizes auf RLS-Tabellen; für Fingerprint-MACs eine Seitentabelle mit B-Baum-Index (Migration). |
| SR-08 | Low | offen | Die Reserve bei NFR-03 ist knapp (Verfügbarkeit). Mit Spuren begrenzt der Abschnitt unter den Advisory-Locks von Audit-Kette und Event-Outbox je Organisation den Durchsatz. Darin berechnet der Trigger der Outbox-Nummerierung (Migration 000120, `event_outbox_number()`) je Zeile `max(sequence)` über die Events der Organisation; mit veralteter Statistik aggregiert der Plan über alle Events. Ebenso wählen die Batch-Kandidatenabfragen (WP-333) nach großen Importen bis zur nächsten Analyse Pläne über die ganze Organisation. Frühere Läufe desselben Codes lagen bei 210–233 Records/s. | Folge-WP mit Migration: Zähler je Organisation oder `ORDER BY sequence DESC LIMIT 1`; das verkürzt den Lock-Abschnitt von etwa 200 ms auf etwa 20 ms je Batch. `ANALYZE` nach Massenimport oder robuste Abfrageform. |
| SR-09 | Low | offen | `max_cis` (ENT-03) hat mit Spuren zwei kleine Überschreitungsfenster. `AdoptUnlicensed` (Anlage offener `unlicensed_ci`-Einträge, wenn das Limit steigt) nimmt die `max_cis`-Sperre nicht und kann gleichzeitig mit dem Ingest anlegen (vorbestehend). Drain-Spuren nach dem Verringern der Spurzahl können das Limit je um höchstens 100 überschreiten. | Folge-WP: `AdoptUnlicensed` nimmt die `max_cis`-Sperre der Organisation und zählt in der Transaktion neu wie der Ingest (WP-334). Drain-Spuren sind in `docs/betrieb/ingest-spuren.md` beschrieben. |
| SR-10 | Low | offen | Ein Record mit mehr als 256 Identitätsschlüsseln (Stack oder Chassis mit vielen Port-MACs, Record mit Tausenden MACs) nimmt die Identitätssperre seiner Organisation exklusiv. Für die Dauer seiner Transaktion warten alle sperrenden Ingest-Transaktionen dieser Organisation. Andere Organisationen warten nur, soweit ihre Records im selben Spur-Batch dahinter stehen. Der DeviceRecord-Vertrag begrenzt die Zahl der MACs und Interfaces nicht. Die Lock-Tabelle des Clusters bleibt geschützt (Abschnitt „G1-Nachbesserung“). | Folge-WP: Obergrenze für MACs und Interfaces je Record (Änderung des DeviceRecord-Vertrags, API-01) oder konfigurierbare Sperrgrenze. |
| SR-11 | Low | offen | Vorbestehend (schon auf `02ebb76`, nicht durch die G1-Nachbesserung), gefunden im Gesamtreview: Der Indexing-Decorator des CI-Repositorys (`ci.NewIndexingRepository`, `backend/internal/ci/indexing_repository.go`) reicht die optionalen Schnittstellen `Restore` und `UniqueConflicts` nicht durch. `main.go` legt ihn in der Produktion immer um das CI-Repository. Folgen: Das Auflösen eines Reviews `resurrected_device` mit `merge` antwortet in der Produktion 501 („this repository cannot reactivate CIs“); „reaktivieren“ aus CI-13 ist damit in der Produktionsverdrahtung nicht nutzbar. `resurrect_test.go` nutzt das Repository ohne Decorator und sieht das nicht. Die Vorabprüfung eindeutiger Attribute (MET-15) entfällt; den Konflikt fängt nur der Unique-Index der Datenbank (DB-04). Mandantentrennung und Eindeutigkeit bleiben gewahrt; das Wiederherstellen per `POST /cis/{id}/restore` (CI-05) ist nicht betroffen. | Folge-WP außerhalb der G1-Nachbesserung: `Restore` (weiterreichen und neu indizieren) und `UniqueConflicts` (weiterreichen) im Decorator ergänzen; ein Test baut die Kette aus `main.go` (PostgreSQL, Such- und KI-Decorator) und prüft jede optionale Schnittstelle, die Aufrufer erwarten (`ciRestorer`, `UniqueChecker`, `BatchCreator`, `NodeReader`, `BatchReader`, `TypeChanger`, `Merger`, `Restorer`). |
| SR-06 | Info | offen | Der Lasttest lief nicht auf einem dedizierten Referenzhost. Auch der Wiederholungslauf auf Commit `dbbfded` lief auf einem Host mit 4 vCPU und 15 GB RAM, den sich PostgreSQL, NATS, Redis, Server und k6 teilten (Größenklasse des Mindestsizings nach INS-07). | E-58 (offen, Aufgabe A-14): Der Auftraggeber entscheidet mit dem Product Owner, ob dieser Lauf als G1-Nachweis für GATE-03 gilt. Empfohlen ist die Anerkennung mit Auflagen, darunter mindestens ein grüner `load.yml`-Lauf auf dem Commit des Release-Kandidaten. Diese Auflage ist für Commit `918a832` erfüllt: Seit `458a96d` läuft jedes Szenario in `load.yml` als eigener Job mit frischem Seed und `ANALYZE`, und Run 38665599979 auf dem Standard-Runner `ubuntu-latest` (4 CPUs, 15,61 GB RAM) hält alle Schwellen ein, zweiter Nachweis neben dem lokalen Lauf (`docs/acceptance/lasttest-g1.md`). Bis zur Entscheidung bleibt SR-06 offen. Nächtlicher Lauf `load.yml`, nach dem Merge auf dem Release-Kandidaten; dedizierter Referenzhost vor GA. |

## G1-Nachbesserung (WP-332 bis WP-335)

Die G1-Nachbesserung behebt SR-02 und SR-05 (`docs/plan/implementierungsplan.md`,
Abschnitt „G1-Nachbesserung (NFR-02, NFR-03)“). Sie ändert den
Datenbank-Einstieg aller Mandantentransaktionen (`WithTenant`), den
Ingest-Pfad und die Impact-API. Geprüft ist Commit `dbbfded` mit diesen
Commits auf `02ebb76`, dazu die Korrekturen aus dem Gesamtreview:

- WP-332: `d2daeed`, Nachtrag `82110bf`
- WP-333: `8320a12`
- WP-334: `25f0c79`, Nachbesserung `55aa9f3`; Korrekturen aus dem
  Gesamtreview `58863fa` und `dea65b7`; Nachträge zu seinen nicht
  blockierenden Befunden `3f2bfd5`, `458a96d`, `111bdc4`, `f354c5a` und
  `0f165cc`
- WP-335: `a8f5912`, Nachtrag `dbbfded`

Jedes WP hatte drei unabhängige Reviews: Transaktionen und Nebenläufigkeit,
Sicherheit und Mandantentrennung, Akzeptanzkriterien mit echten Testläufen.
Blockierende Befunde gab es nur bei WP-334; sie sind in `55aa9f3` behoben.
Die übrigen Befunde sind in den Nachträgen behoben oder als offene Punkte
festgehalten.

Ein Gesamtreview des integrierten Stands `dbbfded` fand drei weitere
blockierende Befunde zu WP-334. Sie sind behoben:

- **Lock-Raum (Sicherheit):** Die Identitäts- und `max_cis`-Sperren waren
  Ein-Argument-Sperren mit FNV-1a-Schlüsseln aus Werten des Mandanten, im
  selben Raum wie die Sperre des Scheduler-Leiters. FNV-1a lässt sich
  umkehren; eine gewählte Seriennummer hätte eine Spur für alle Mandanten
  ohne Zeitgrenze blockieren können. Behoben in `58863fa` (Zeile
  „Identitätssperre“ unten).
- **`/readyz` (Betrieb):** Das Ingest-Detail wartete bis zu 2 s auf
  JetStream. Stockte JetStream, lief die Kubernetes-Probe (Standard 1 s) ab,
  und alle Pods verließen den Service, obwohl jeder Check bestand. Behoben
  in `dea65b7` (Zeile „Rückstand je Spur“ unten).
- **Rollback (Betrieb):** Der beschriebene Rückfall auf ein Release vor
  WP-334 ließ den Rückstand der Spur 0 ungelesen verfallen (nachgestellt mit
  NATS 2.10.29). Korrigiert ist die Betriebsdoku
  (`docs/betrieb/ingest-spuren.md`, „Rollback auf ein Release vor WP-334“);
  der Code ist unverändert.

Die nicht blockierenden Befunde des Gesamtreviews sind am 2026-10-15
nachgetragen:

- **Sperren mit einer Spur (Sicherheit, Betrieb):** Identitätssperre,
  `max_cis`-Sperre und Sofort-Wiederholung hingen an mehr als einer Spur. Mit
  einer Spur konsumiert eine Instanz aber weiter Spur 0, den Altconsumer und
  alle Drain-Spuren, jeweils mit eigenem `MaxAckPending`, und andere Instanzen
  tun dasselbe. Beim Rückfall auf eine Spur und bei rollenden Änderungen der
  Spurzahl konnten zwei Instanzen ein Gerät doppelt anlegen (PRI-10) und
  `max_cis` überschreiten (ENT-03); die Betriebsdoku behauptete das
  Gegenteil. Nachgestellt gegen den alten Stand: zwei Instanzen mit einer
  Spur legten je Runde 2 CIs an, vier Instanzen bei `max_cis` 300 legten 550
  an. Behoben in `3f2bfd5`: Jede Transaktion eines Records aus der Queue
  nimmt die Sperren, unabhängig von der Spurzahl (Zeilen „Identitätssperre“
  und „`max_cis`“ unten).
- **Speicherbetrieb (Integration):** Mit `--no-db` und NATS liefen die
  Spuren einer Organisation parallel, obwohl das Speicher-Repository nicht
  sperren kann; ein Gerät konnte zwei CIs bekommen. PostgreSQL war nicht
  betroffen. Behoben in `3f2bfd5`: Parallel laufen die Spuren nur mit einem
  Repository, das sperren kann; sonst verarbeitet ein Prozess jede
  Organisation seriell.
- **`/readyz` ohne Anmeldung (Sicherheit):** Jede Anfrage listete die
  JetStream-Consumer, also ein NATS-API-Aufruf mit bis zu 2 s Wartezeit auf
  dem Server je Anfrage. Behoben in `111bdc4`: Das Detail ist die letzte
  30-s-Stichprobe des Ingest-Workers. Seit `0f165cc` melden es nur Instanzen
  mit Ingest-Worker; auf API-Instanzen schrieb sonst jede Probe eine
  WARN-Zeile (Zeile „Rückstand je Spur“ unten).
- **Lastnachweis (Betrieb):** `load.yml` führte die Szenarien nacheinander
  gegen einen Seed aus, ohne neuen Seed und ohne `ANALYZE`, und `ingest.js`
  ließ eine einzelne abgelehnte Anfrage durch. Behoben in `458a96d`: ein Job
  je Szenario mit frischem Seed und `ANALYZE`, Schwellen
  `http_req_failed{name:ingest}` und `http_req_failed{name:spool}` = 0;
  `f354c5a` nennt im Spool-Log das Alter der Stichprobe. Der Standard-Runner
  von GitHub trägt den Lauf: Run 38665599979 auf Commit `918a832` ist grün
  (`docs/acceptance/lasttest-g1.md`).
- **Betriebsdoku:** Der Altconsumer `reticora-ingest` bleibt dauerhaft; die
  beschriebene Entfernung wirkte nicht, weil der Server ihn wieder anlegt.
  Die Lock-Tabelle ist nach einer Formel zu bemessen, die das HPA-Maximum
  von 10 Replikaten abdeckt (`docs/betrieb/ingest-spuren.md`).
- **Vorbestehend:** Der Indexing-Decorator reicht `Restore` und
  `UniqueConflicts` nicht durch (SR-11, Folge-WP außerhalb der
  G1-Nachbesserung).

### Änderungen und Nachweise

| Änderung | Bewertung | Nachweis |
|---|---|---|
| **Transaktionsbeginn in einem Roundtrip** (WP-332): `WithTenant` sendet `BEGIN` und die fünf `set_config` der Mandanten-GUCs als eine Anweisung. Die Werte stehen als Literale im Text, nicht mehr als Bind-Parameter. | Organisations- und Scope-IDs sind validierte UUIDs. Die Benutzerkennung (Subject des Principals) steht als `E'…'`-Literal; Backslash und Hochkomma werden verdoppelt (nie `\'`), das gilt unter jeder Einstellung von `standard_conforming_strings` und `backslash_quote`. `Validate` lehnt NUL-Bytes und ungültiges UTF-8 ab. Die Verdopplung ist nur in UTF-8 sicher: Jeder Pool setzt `client_encoding=UTF8` als Startparameter (vor DSN und Rollen-Defaults) und verweigert Verbindungen mit anderer Kodierung. Diese Lücke fand das Review; sie bestand nur in den Zwischenständen ab `d2daeed`, setzte eine fehlkonfigurierte DSN voraus (z. B. `client_encoding=SJIS` mit `backslash_quote=on`) und ist mit `82110bf` geschlossen. Rest: Die GUC-Werte erscheinen im Anweisungstext (`pg_stat_activity`, Serverlog bei Anweisungsprotokollierung). Es sind Kennungen, keine Geheimnisse. | `TestBeginTenantSQLQuotesValues` (feindliche Werte `'`, `\`, `;`, `--`, `\'`, Zeilenumbruch, eingeschleuste `set_config`), `TestTenantScopeValidate` (NUL, ungültiges UTF-8), `TestWithTenantQuotesHostileUserID` (feindliche Benutzerkennungen kommen unverändert in `app.user_id` an, `app.org_id` bleibt, RLS isoliert weiter; auch mit `standard_conforming_strings=off` und mit einer DSN, die `client_encoding=SJIS` verlangt). Gegenprobe mit dem `pool.go` vor `82110bf`: Die SJIS-Variante setzte `app.org_id` auf eine fremde Organisation. |
| **Beitritt zur Kontext-Transaktion** (WP-332): Die Repository-Aufrufe eines Records laufen in einer Transaktion; `WithTenant` und `WithRequestTenant` treten einer Transaktion im Kontext bei. | Beitritt nur, wenn `WithTenant` die Transaktion geöffnet hat, sie offen ist und der Scope identisch ist (Organisation, Benutzer, Client-, Site- und Team-Scope). Sonst scheitert der Aufruf, ohne seine Funktion auszuführen (`ErrTxNotJoinable`, `ErrTxDone`, `ErrTxScopeMismatch`; fail closed). `WithSystem`, `WithOperator` und `WithOrganizationSetup` treten nie bei. Ein Kontext mit eigenem Scope behält ihn; ein engerer Scope wird nie auf den der Transaktion erweitert (Review-Befund, behoben in `82110bf`). Ein verschluckter Anweisungsfehler rollt die ganze Transaktion zurück (`ErrTxAborted`). | `TestContextTxFailsClosed`, `TestTenantScopeSameAs`, `TestContextTxJoin`; der Architekturtest `tenantguard_test.go` (WP-041) ist unverändert grün. |
| **Externe Aufrufe und Benachrichtigungen nach dem Commit** (WP-332, WP-333): `database.AfterCommit`. | Review-Benachrichtigungen, das Log `review.opened`, der Remote-Index (OpenSearch), Einwilligungsprüfung und Embedding der KI sowie Zähler und Log von `unlicensed_ci` laufen erst nach dem Commit, auch für die Batch-Transaktion; ein Rollback verwirft sie. Ein zurückgerollter Record kündigt also nichts an und gibt keine Daten an externe Dienste. Keine Transaktion wartet unter den Sperren der Organisation auf einen externen Dienst. Die KI-Einwilligung gilt zum Zeitpunkt des Commits. Ohne Kontext-Transaktion (REST-Pfad) bleibt alles wie bisher. Logs zu zurückgerollten Reviews und die Einwilligungsprüfung in der Transaktion fand das Review (behoben in `82110bf`). | `TestDiscoveryConcurrency` (Rollback-Runden für Record und Batch: kein Review angekündigt, im Einzelpfad genau eins nach dem Commit), `TestIngestKeepsPrefixOfFailedBatch` (kein `review.opened` für den zurückgerollten Record), `TestHybridIndexesRemoteAfterCommit`, `TestCIChunkIndexerEmbedsAfterCommit` (keine Einwilligungsprüfung in der Transaktion, verweigerte Einwilligung), `TestContextTxJoin`. |
| **Audit-Kette im Batch und am Transaktionsende** (WP-333, WP-334) | Die neuen Geräte eines Batches schreiben ihre Audit-Einträge mit einer Anweisung unter dem Ketten-Lock der Organisation, verkettet in fester Reihenfolge mit lückenloser `seq` (AUD-01). Audit- und Outbox-Einträge des Ingests stehen am Ende der Transaktion (`database.BeforeCommit`); die Kette bleibt in Commit-Reihenfolge. Ein per Batch angelegtes CI hat dieselben Events, dieselbe Historie, Provenienz und dieselben Suchdokumente wie bei der Einzelanlage. | `TestIngestStatementBudget` (`audit.Verify` nach jedem Batch; Audit und Outbox zuletzt in jeder Record- und Batch-Transaktion), `TestCreateMany/equal_to_the_single_creation` (einschließlich `seq` im Audit-Log), `TestIngestBatchMatchesRecordPath`, `TestBeforeCommit`. |
| **Identitätssperre, begrenzt je Transaktion** (WP-334, E-56 offen) | Jede Record- und Batch-Transaktion eines Records aus der Queue nimmt als erste Anweisung transaktionsgebundene Advisory-Locks, seit `3f2bfd5` unabhängig von der Spurzahl (vorher nur mit mehr als einer Spur): die Identitätssperre der Organisation geteilt, dann die Identitätsschlüssel exklusiv und aufsteigend (deadlockfrei). Schlüssel sind Seriennummer, Hardware-UUID, jede MAC und die Management-IP je VRF. Das verhindert doppelte CIs über Spuren (PRI-10) und dass eine ältere Beobachtung eine neuere überschreibt (REC-05). Eigener Lock-Raum (seit `58863fa`): Die Sperren nutzen die Zwei-Argument-Form mit eigenen Klassen (Organisation `0x52496f72`, Schlüssel `0x52496b79`, `max_cis` `0x52436e74`), verschieden von Audit-Kette, Outbox und Publisher; ihr Lock-Tag kann keiner Ein-Argument-Sperre gleichen (Scheduler-Leiter, Jobs, Location-Baum, Operator-Audit). Der Schlüssel in der Klasse sind die ersten 4 Byte des SHA-256 von Organisation und Wert; eine gezielte Kollision kostet etwa 2^32 Hashes und serialisiert nur zwei Ingest-Transaktionen. Jede Sperranweisung wartet höchstens 20 s (`lock_timeout`, in derselben Anweisung zurückgesetzt; andere Wartezeiten der Transaktion bleiben unverändert); danach wird der Record mit Pause wiederholt (55P03), statt eine Spur aller Mandanten anzuhalten. Befund des Gesamtreviews: Vorher waren es Ein-Argument-Sperren mit umkehrbaren FNV-1a-Schlüsseln im Raum der Scheduler-Sperre, ohne Wartegrenze. Review-Befund, behoben in `55aa9f3`: Ohne Obergrenze hielt eine Transaktion bis zu 20.003 Advisory-Locks. Jeder belegt einen Eintrag der Lock-Tabelle des ganzen PostgreSQL-Clusters; ist sie voll, scheitern Transaktionen aller Mandanten und Dienste mit „out of shared memory“ (53200). Jetzt sperrt eine Transaktion höchstens 256 Schlüssel. Batch-Fenster enden vor der Grenze; ein Record darüber nimmt nur die Sperre der Organisation exklusiv. Eine Instanz hält höchstens 6 × 257 = 1.542 Identitätssperren; 53200 und 55P03 werden bis zu dreimal nach 100, 200 und 300 ms wiederholt. Die Lock-Tabelle ist nach `max_locks_per_transaction` × (`max_connections` + `max_prepared_transactions`) ≥ Ingest-Instanzen × 1.542 + Reserve zu bemessen; beim HPA-Maximum von 10 Replikaten und `max_connections` = 100 sind das `max_locks_per_transaction` = 256, alternativ eigene Ingest-Instanzen (`RETICORA_WORKER_INGEST_ENABLED`, `docs/betrieb/ingest-spuren.md`). Ohne Sperre bleiben nur der Ingest ohne NATS (in der Anfrage, wie vor WP-334) und der Speicherbetrieb (`--no-db`), der jede Organisation seriell verarbeitet. Befund des Gesamtreviews, behoben in `3f2bfd5`: Mit einer Spur nahm eine Instanz keine Sperre, obwohl andere Instanzen, Altconsumer und Drain-Spuren dieselbe Organisation gleichzeitig verarbeiten. Rest: SR-10. | `TestIdentityLocksBounded`, `TestQueuedRecordsTakeLocks` (Sperre und Wiederholung für Records aus der Queue auch mit einer Spur, nicht über HTTP ohne Queue), `TestNoDBLanesKeepOneDevice` (Speicherbetrieb mit 1 und 4 Spuren: je 1 CI), `TestIngestLanesIdentityLock` (ohne Sperre 2 CIs, mit Sperre 1; `one_lane_from_the_queue`: zwei Instanzen mit einer Spur, über die Queue je Runde 1 CI, über HTTP ohne Queue 2; `bounded_per_transaction`: höchstens 256 Locks je Transaktion gemessen, der Record mit 20.000 MACs hält genau eine exklusive Sperre, kein 53200), `TestDiscoveryConcurrency` (Doppel-Ingest über Spuren, drei Fälle, je genau 1 CI). Gegenprobe ohne Grenze: 20.003 Locks in einer Transaktion. `TestIngestLockSpace` (Klassen disjunkt, nur Zwei-Argument-Form, `lock_timeout` gesetzt und zurückgesetzt), `TestIngestLanesIdentityLock/own_lock_space` (die Scheduler-Sperre und Ein-Argument-Sperren mit denselben Bits blockieren nicht) und `/lock_timeout` (gegen einen hängenden Halter scheitert der Record nach Batch- und vier Record-Versuchen mit 55P03, bei 300 ms Grenze nach etwa 2,1 s statt unbegrenzt; danach entsteht genau 1 CI). Gegenproben: Mit dem Code vor `58863fa` scheitert `own_lock_space`, mit `lock_timeout` „0“ scheitert `lock_timeout`; mit der Bedingung vor `3f2bfd5` (nur mehr als eine Spur) scheitern `TestQueuedRecordsTakeLocks`, `one_lane_from_the_queue` (2 CIs in 5 von 5 Runden) und `TestNoDBLanesKeepOneDevice`. |
| **`max_cis` exakt mit Spuren** (WP-334, ENT-03) | Für Records aus der Queue (seit `3f2bfd5` auch mit einer Spur) und mit gespeichertem Limit nimmt jede anlegende Transaktion die `max_cis`-Sperre der Organisation (eigene Klasse, Wartegrenze 20 s wie die Identitätssperre) und zählt in der Transaktion neu: exklusiv nahe am Limit, sonst geteilt. Reicht der Spielraum unter der geteilten Sperre nicht, beginnt die Transaktion exklusiv neu. Ohne Limit gibt es keine Sperre. Rest: SR-09. | `TestIngestLanesMaxCIs` (Limit 150 bzw. 500, 4 Spuren gleichzeitig: genau 150 bzw. 500 CIs, der Rest als `unlicensed_ci`; vier Instanzen mit einer Spur aus der Queue, Limit 300: genau 300 CIs). Gegenproben: ohne Neuzählung 400 bzw. 800 CIs, mit der Bedingung vor `3f2bfd5` 550 statt 300. |
| **NATS-Subjects und Consumer** (WP-334) | Der Server veröffentlicht Records unter `ingest.<org>.<spur>.<collector>`; je Spur liest ein Consumer `reticora-ingest-lane-<n>`. Der Altconsumer `reticora-ingest` wird auf die alten Subjects `ingest.*.*` eingeschränkt und bleibt dauerhaft bestehen (fehlt er, legt der Server ihn wieder an); wartende Nachrichten bleiben erhalten. Die Collector-Berechtigungen sind unverändert (`natsedge.go` unverändert): Ein Collector publiziert und abonniert nur unter `collector.<org>.<collector>.>` und in seinem Inbox-Präfix, `ingest.>` erreicht er nicht. Organisation und Scope einer Nachricht setzt weiter der Server aus der Authentisierung; die Spur berechnet der Consumer aus dem Record, nicht aus dem Subject. Rolling Upgrade: Eine Instanz eines Releases vor WP-334 setzt den Altconsumer beim Start ungefiltert zurück. Bis neue Instanzen den Filter wiederherstellen (höchstens 30 s), verarbeitet sie Spur-Nachrichten, die in dieser Zeit veröffentlicht werden, ohne Identitäts- und `max_cis`-Sperre; ältere Spur-Nachrichten sieht sie nicht. Rollback: Ein Release vor WP-334 liest Nachrichten, die schon in Spur-Subjects liegen, nicht; bei einem Rolling Rollback verfielen sie nach 48 h (Befund des Gesamtreviews, nachgestellt mit NATS 2.10.29). Verlustfrei ist nur der Ablauf mit gestoppter Annahme, leerer Queue und gelöschten Spur-Consumern (`docs/betrieb/ingest-spuren.md`, „Rollback auf ein Release vor WP-334“). | `TestIngestLanes` (Upgrade vom ungefilterten Altconsumer mit wartenden Nachrichten, Subjects für HTTP und Edge, Reihenfolge je Gerät, Drain, Wiederherstellen des Filters), `natsedge_integration_test.go` (T10, unverändert). |
| **Rückstand je Spur in `/readyz`** (WP-334, OPS-05) | `/readyz` ist ohne Anmeldung erreichbar und zeigt unter `details.ingest` die Spurzahl und den Rückstand je Spur, summiert über alle Organisationen, ohne Mandanten-IDs. Die Summe lieferte `/metrics` auf demselben Port schon vorher ohne Anmeldung (`reticora_worker_backlog`), je Spur steht sie dort jetzt als `reticora_ingest_lane_backlog`. Ein Fehler erscheint nur als `{"error":"unavailable"}`, ohne Fehlertext in der Antwort. Die Details ändern den Status nie. OpenAPI `ReadinessResponse` ist additiv erweitert. Seit `dea65b7` (Befund des Gesamtreviews) laufen die Details getrennt von den Checks mit einer eigenen Grenze von 300 ms; ein langsames oder hängendes Detail meldet `unavailable` und hält die Antwort nicht auf. Vorher wartete `/readyz` bis zu 2 s auf JetStream, die Kubernetes-Probe (Standard 1 s) lief ab, und alle Pods verließen den Service. Die Readiness-Probe wartet jetzt 3 s (`deploy/k8s/base/deployment.yaml`). Seit `111bdc4` (Befund des Gesamtreviews) ist das Detail die letzte Stichprobe, die der Ingest-Worker alle 30 s ohnehin für die Metriken nimmt, mit ihrem Zeitpunkt (`sampled_at`, OpenAPI additiv). Eine Anfrage ruft NATS nicht mehr auf; vorher löste jede unauthentifizierte Anfrage eine Auflistung der JetStream-Consumer aus. Seit `0f165cc` melden nur Instanzen mit Ingest-Worker das Detail; vor der ersten Stichprobe steht dort `unavailable`, ohne WARN-Zeile. | `TestReadyzDetails` (Details nicht in `checks`, Fehlertext nicht in der Antwort, Status unverändert), `TestReadyzSlowDetail` (ein Detail, das auf seinen Kontext wartet oder ihn ignoriert: Antwort unter 500 ms, Status `ready`, Detail `unavailable`), `TestReadinessDetailFromSample` (eine Stichprobe und 50 Details: eine Auflistung der Consumer), `TestIngestLanes` (echter NATS: 20 Details lösen keine JetStream-Anfrage aus), `TestReadyzDetailNotSampledIsQuiet`. |
| **Impact und Topologie** (WP-335, IMP-07) | Kanten werden ohne Join auf `ci` gelesen; die RLS der Kanten bleibt. Die erreichten CIs prüft eine Abfrage unter demselben Scope. Eine Kante zählt nur, wenn beide Enden sichtbar und nicht gelöscht sind (fail closed, auch wenn ein CI nach dem Lesen der Kanten gelöscht oder aus dem Scope verschoben wurde). Erreicht eine Ebene mehr als 1.000 CIs, liest die Traversierung alle Kanten und die sichtbaren CIs der Organisation und prüft genauso. `ListNodes` projiziert unter RLS mit Override-Overlay; ab 2.000 IDs liest es die sichtbaren CIs der Organisation und filtert in Go. `fields=` (API-02) lehnt unbekannte Felder mit 422 ab. Die teuerste Analyse (T20) wird billiger: Die Zahl der Anweisungen hängt nur noch von der Tiefe ab, nicht von der Größe des Graphen, und die kompakte Antwort ist 2,39 MB statt 5,0 MB groß. | `TestTraversalStaysInsideScope` (Ebenen- und Organisationslesung; ohne Sichtbarkeitsfilter der Organisationslesung rot), `TestListNodesProjection` (gelöschte CIs und CIs außerhalb des Scopes fehlen, auch bei Organisationslesung), `TestImpactFields`, `TestTopologyQueryBudget`, `TestImpactWorstCase`. |

### Prüfungen auf Commit `dbbfded`

- `go build`, `gofmt`, `go vet ./...` und golangci-lint v2.14.0
  (Konfiguration des Repositorys): ohne Befund. `go test ./...`: grün.
- Datenbank-Tests auf einer frischen Datenbank mit NATS und Redis: 89 Pakete,
  1.885 Tests, zwei Fehlschläge in `internal/permission` (Sortierung des
  Katalogs). Ursache ist die Kollation der lokalen Datenbank (en_US, glibc);
  das CI-Image (Alpine/musl) sortiert wie C, und mit C.UTF-8 ist das Paket
  grün. `internal/permission` und die Migrationen ändert die Nachbesserung
  nicht. `TestMigrationsRoundtrip` läuft nur in der CI, nicht auf dem
  geteilten lokalen Cluster.
- Frontend: format:check, lint, typecheck, vitest (47 Dateien, 249 Tests),
  build und generate:api:check sind grün.
- Lastnachweis: `tests/load/ingest.js` zählt fehlende Records als Fehler
  (Schwelle `ingest_records_missing` = 0). Vorher bestand k6 auch mit
  fehlenden Records; das fand das Review von WP-334 (behoben in `55aa9f3`).
- Korrekturen aus dem Gesamtreview (`58863fa`, `dea65b7`): `gofmt`,
  `go vet ./...` und golangci-lint ohne Befund, `go test ./...` grün. Die
  Datenbank-Tests der Pakete `discovery`, `database`, `ci`, `topology`,
  `relationship`, `server`, `tenant`, `tenant/rls`, `override` und `jobs`
  sind grün; `kustomize build` von base, staging und prod gelingt. Eine
  Kontrollmessung des Szenarios `spool` mit 4 Spuren auf diesem Stand ergab
  243 Records/s ab Sendeende (Wiederholungslauf: 233) ohne fehlenden Record;
  die Werte des Wiederholungslaufs bleiben gültig
  (`docs/acceptance/lasttest-g1.md`).
- Nachträge vom 2026-10-15 (Stand `111bdc4`): `gofmt`, `go vet ./...` und
  golangci-lint ohne Befund, `go test ./...` grün. Die Datenbank-Tests der
  Pakete `discovery`, `database`, `ci`, `topology`, `relationship`, `server`,
  `tenant` (mit `rls`), `override`, `middleware` und `api` sind grün,
  ebenso `tests/rep01-structure.test.sh`, `generate:api:check` und der
  Typecheck des Frontends. Die neuen Tests scheitern gegen den Code vor der
  Korrektur (Gegenproben). Kontrollmessung `spool`: 4 Spuren 232–255
  Records/s, 1 Spur mit Sperren 177 Records/s, kein fehlender Record.
- Lastlauf auf GitHub: `load.yml` auf Commit `918a832` (Run 38665599979)
  ist grün, alle k6-Schwellen eingehalten; damit ist die Auflage von E-58
  für diesen Commit erfüllt. Nach dem Merge wiederholt der nächtliche Lauf
  ihn auf dem Release-Kandidaten.
- Ausstehend: CI-Lauf auf dem Release-Kandidaten (mit den Scan-Jobs und
  `TestMigrationsRoundtrip`).

## Externer Pentest

WP-210: Nach E-28 beauftragt der Auftraggeber den Pentest (Aufgabe A-07).
Bericht und Nachtest werden in `docs/security/pentest-g1.md` zusammengefasst.
Ohne diesen Nachweis ist GATE-03 offen.

Die G1-Nachbesserung ändert den Datenbank-Einstieg der Mandantentransaktionen,
den Ingest-Pfad (Spuren, Subjects) und die Impact-API. Alle drei liegen im
vorgeschlagenen Umfang. Der Pentest sollte deshalb den Stand nach WP-332 bis
WP-335 prüfen; beginnt er früher, braucht er einen Nachtest für Ingest und
Impact.

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
| ABN-02 | [Q] | High | geschlossen | WP-204, WP-206, WP-335 |
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
| NFR-02 | [B] | High | geschlossen | WP-177, WP-206, WP-335 |
| NFR-03 | [B]; Teilumfang [P5] | High | geschlossen | WP-085, WP-154, WP-206, WP-332, WP-333, WP-334 |
| NFR-05 | [B] | High | geschlossen | WP-100, WP-101 |
| NFR-06 | [B] | High | geschlossen | WP-135 |
| NFR-07 | [B] | High | geschlossen | WP-193 |
| NFR-09 | [B] | High | geschlossen | WP-105 |
| NFR-10 | [Q] | High | geschlossen | WP-203, WP-204, WP-206, WP-334 |
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
