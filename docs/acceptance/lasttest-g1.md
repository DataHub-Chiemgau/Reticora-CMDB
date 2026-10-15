# Lasttest G1 (NFR-10)

Nachweis zu NFR-01, NFR-02, NFR-03 und ABN-02 vor Gate G1. Der Erstlauf
(WP-206) verfehlte NFR-02 beim Impact im Worst Case und NFR-03 beim
Ingest-Durchsatz (Blocker B2 und B1 in `docs/acceptance/g1-gate.md`). Die
G1-Nachbesserung (WP-332 bis WP-335, `docs/plan/implementierungsplan.md`)
behebt beides; der Wiederholungslauf auf Commit `dbbfded` (Nachweislauf für
G1) belegt das. Danach kamen zwei Korrekturen aus dem Gesamtreview hinzu
(`58863fa`, `dea65b7`) und am 2026-10-15 die Nachträge zu seinen nicht
blockierenden Befunden (`3f2bfd5`, `458a96d`, `111bdc4`, `f354c5a`,
`0f165cc`). Kontrollmessungen auf beiden Ständen bestätigen die Werte
(Abschnitt „Ergebnis NFR-03“). Auf GitHub hält der Workflow `load.yml` auf
Commit `918a832` alle Schwellen ein (Abschnitt „GitHub-Lauf“). Am 2026-10-15
hat der Auftraggeber beide Läufe als G1-Nachweis anerkannt (E-58) und das
Skalierungsziel bestätigt (E-17). Der Erstlauf steht unten als historischer
Abschnitt.

Die Szenarien, ihre Schwellen und der Ablauf stehen in
`tests/load/README.md`. Der Workflow `.github/workflows/load.yml` wiederholt
den Lauf manuell und nächtlich. Seit `458a96d` läuft jedes Szenario als
eigener Job (`api.js`, `ingest.js` mit dem Szenario `load`, `ingest.js` mit
dem Szenario `spool`), jeweils mit frischer Datenbank, Seed, `ANALYZE` und
4 Spuren, wie es das Nachweisprotokoll verlangt. Vorher liefen die drei
nacheinander gegen einen Seed, ohne neuen Seed und ohne `ANALYZE`. Jeder Job
legt seine k6-Zusammenfassung als eigenes Artefakt ab (`load-results-api`,
`load-results-ingest`, `load-results-ingest-spool`).

## Umgebung und Datenbestand

- **Seed:** `cmd/seed -objects 10000` (SIM-01 skaliert auf 10.000 Objekte,
  WP-203). Die Demo-Org hat 10.000 CIs mit je einem Interface und einer
  Adresse sowie 19.993 Kanten (LLDP zum Core-Switch, Strom zur PDU) und 2.000
  Metriken. Der Core-Switch `sw-00001` trägt etwa die Hälfte aller CIs, sein
  Impact umfasst die ganze Organisation (Worst Case). Vor jedem Szenario wird
  neu geseedet, denn der Rückstand eines Laufs verfälscht den nächsten.
- **Skalierungsziel:** 10.000 Objekte nach CH15 (NFR-01). E-17 ist am
  2026-10-15 entschieden (Option 1, B5 geschlossen): Die Budgets gelten bei
  10.000 Objekten, Headroom 50.000 ohne Budgetzusage; gemessen ist mit
  10.000 Objekten.
  Seed und Szenarien sind parametrisiert (`OBJECTS` in `api.js`) und bis
  50.000 Objekte nutzbar.
- **Referenzumgebung:** Der Wiederholungslauf lief auf einem Host mit 4 vCPU
  und 15 GB RAM. Datenbank, Queue, Server und k6 teilen sich die Kerne; die
  Werte sind deshalb konservativ. Der Host entspricht der Mindestgrößenklasse
  nach INS-07 (4 vCPU), ist aber kein dedizierter Referenzhost (SR-06).

  Der Auftraggeber hat diesen Lauf am 2026-10-15 als G1-Nachweis für GATE-03
  anerkannt (E-58, Option 1, Aufgabe A-14 erledigt). Die Anerkennung gilt mit
  diesen Auflagen:
  - Protokoll mit Commit, CPU und RAM, Konfiguration und k6-Ergebnissen
    (Abschnitt „Wiederholungslauf“)
  - NFR-02 auf frischem Seed vor dem Ingest gemessen
  - alle Schwellen eingehalten
  - mindestens ein grüner `load.yml`-Lauf auf dem Commit des
    Release-Kandidaten (für Commit `918a832` erfüllt, Abschnitt
    „GitHub-Lauf“; nach dem Merge wiederholt ihn der nächtliche Lauf auf
    `main`)
  - dedizierter Referenzhost vor GA (SR-06)

  **GitHub-Runner:** Der Workflow `load.yml` lief auf dem Standard-Runner
  `ubuntu-latest` (4 CPUs) grün; alle k6-Schwellen sind eingehalten
  (Abschnitt „GitHub-Lauf (Run 38665599979, Commit `918a832`)“). Damit
  halten zwei unabhängige Umgebungen mit 4 vCPU jede Schwelle ein.

## Wiederholungslauf (Commit `dbbfded`)

| Punkt | Wert |
|---|---|
| Commit | `dbbfded33363896010dd8ad84297eb6e3004bf37` (WP-332 bis WP-335 auf `02ebb76`) |
| Datum | 2026-10-14, etwa 22:25–22:40 UTC |
| Host | 4 vCPU (Intel Xeon @ 2,10 GHz), 15 GB RAM; allein auf dem Host |
| Komponenten | PostgreSQL 16.15 mit TimescaleDB 2.17.2 (aus den Quellen gebaut), NATS 2.10.29, Redis 7, Server (ein Prozess) und k6 v0.54.0 auf demselben Host |
| Datenbestand | `cmd/seed -objects 10000`, vor jedem Szenario neu geseedet |
| Konfiguration | Rate-Limit angehoben (siehe README); `RETICORA_INGEST_LANES=4` (Standard) für `api.js` und für NFR-03, zur Information zusätzlich `RETICORA_INGEST_LANES=1` |
| Szenarien | `api.js` (60 s, alle Szenarien parallel, 5 VUs je Lesepfad, 2 VUs für Topologie und je Impact-Form); `ingest.js` Szenario `load` und Szenario `spool`, je mit 4 und mit 1 Spur |
| Ergebnis | Mit 4 Spuren alle Schwellen eingehalten. Mit 1 Spur verfehlt nur das Szenario `spool` die Abbau-Rate. |

Die k6-Zusammenfassungen und Serverlogs dieses lokalen Laufs sind nicht
versioniert; die Werte unten stammen aus ihnen. Als Artefakt archiviert sind
die k6-Zusammenfassungen des GitHub-Laufs (Abschnitt „GitHub-Lauf“,
aufbewahrt bis 2027-01-13).

## Ergebnis NFR-02 (p95, 60 s, 10.000 Objekte)

| Budget | Szenario | p95 | Budget | Ergebnis |
|---|---|---:|---:|---|
| Listen | `GET /cis?limit=50` | 115 ms | < 500 ms | erfüllt |
| Suche | `GET /cis?search=…` | 98 ms | < 500 ms | erfüllt |
| Indizierte Attributfilter | `filter[status]`, `filter[hostname]` | 81 ms | < 500 ms | erfüllt |
| CI-Detail | `GET /cis/{id}` | 66 ms | < 300 ms | erfüllt |
| Rack-Layout | `GET /racks/{id}/layout` | 79 ms | < 300 ms | erfüllt |
| Topologie 2.000 Knoten | Core-Switch, Tiefe 1 (5.000 Knoten) | 255 ms | < 3 s | erfüllt |
| Impact Tiefe 20 (kompakt, Oberfläche) | Core-Switch, `fields=…` (9.997 betroffene CIs) | 477 ms | < 2 s | erfüllt |
| Impact Tiefe 20 (Standardform mit Pfaden) | Core-Switch | 546 ms | < 2 s | erfüllt |
| Bulk 500 IDs | Statuswechsel bis Job-Ende | 4,0 s | < 30 s | erfüllt |

Checks: 100 % (7.038 von 7.038), 0 fehlgeschlagene Anfragen. Der Impact ist
der Worst Case: Der Core-Switch fällt aus, betroffen ist die ganze
Organisation.

- **Zwei Antwortformen (E-57):** Die Standardantwort von `GET /impact/{ci_id}`
  und `GET /impact` ändert sich nur additiv (API-01). Sie behält `path` und
  bekommt `hops`, `via` und optional `relay`. Die kompakte Form ist der
  Sparse-Fieldset `fields=` (API-02); die Topologie-Ansicht fragt
  `fields=name,ci_type,status,heuristic,hops,via,relay` an. In der OpenAPI
  hat die Antwort mit `fields` ein eigenes Schema (`ImpactAnalysisFields`,
  `ImpactNodeFields`). Beide Formen sind budgetiert (`impact` und
  `impact_full` in `api.js`). E-57 ist am 2026-10-15 entschieden (Option A,
  umgesetzt in WP-335; Aufgabe A-13 erledigt).
- **Einzelanfrage (WP-335, 11 Aufrufe per curl):** Die kompakte Form braucht
  im Median 190 ms (173–220 ms) für 2,39 MB, das sind 239 Byte je betroffenem
  CI. Die Standardform mit Pfaden braucht im Median 184 ms (172–217 ms) für
  5,8 MB. Vorher waren es 593 ms und 5,0 MB.
- **Messbasis:** Der Seed schreibt keine Feldwerte (`ci_field_value`). In
  einer per Discovery befüllten Organisation kostet der Override-Abgleich der
  Knotenlesung mehr (Offene Punkte, Punkt 11).

## Ergebnis NFR-03 (Ingest)

| Messgröße | 4 Spuren (Standard, Gate) | 1 Spur (Information) | Budget | Ergebnis (4 Spuren) |
|---|---:|---:|---:|---|
| Szenario `load`: 200 Records/s für 60 s angeboten, angenommen | 12.000, 0 fehlend | 12.000, 0 fehlend | – | – |
| Verzögerung Record → sichtbar (max / p95 der Proben) | 3,1 s / 3,0 s | 12,3 s / 9,9 s | < 30 s | erfüllt |
| `POST /ingest/bulk` p95 | 17 ms | 13 ms | – | – |
| Szenario `load`: Rückstand max. (`/readyz`) | 761 | 2.389 | – | – |
| Szenario `spool`: Abbau ab Sendeende | **233 Records/s** | 182 Records/s (verfehlt) | ≥ 200/s | erfüllt |
| Spool inklusive Senden | 278 Records/s | 189 Records/s | – | – |
| Spool-Flush 10.000 Records (erstes Senden bis alles sichtbar) | 35,9 s | 53,0 s | < 5 min | erfüllt |
| fehlende Records | 0 | 0 | 0 | erfüllt |

- **Gate-Konfiguration:** 4 Spuren sind der Standard (`RETICORA_INGEST_LANES`,
  `docs/betrieb/ingest-spuren.md`) und die Konfiguration von `load.yml`. Damit
  sind alle drei Budgets von NFR-03 erfüllt: Abbau-Rate ≥ 200 Records/s je
  Instanz, Verzögerung < 30 s bei 200 Records/s Angebot und Spool-Flush von
  10.000 Records < 5 min.
- **Eine Spur:** `RETICORA_INGEST_LANES=1` ist der Rückfall auf die serielle
  Verarbeitung (Option 3 von E-56 per Konfiguration). Er hält Verzögerung und
  Spool-Flush, verfehlt aber die Abbau-Rate von 200 Records/s. Seit `3f2bfd5`
  nehmen Records aus der Queue auch mit einer Spur die Identitäts- und
  `max_cis`-Sperre (Kontrollmessung unten).
- **Messgrößen:** Die Abbau-Rate ist im Szenario `spool` budgetiert. Der
  Collector liefert seinen Spool nacheinander aus (COL-05); das Szenario sendet
  10.000 Records als Uploads zu 64 Records (gzip, `POST /discovery/ingest`),
  jeden nach der Antwort des vorigen. Gemessen wird ab Sendeende: Was dann noch
  nicht sichtbar ist, geteilt durch die Zeit, bis alles sichtbar ist. Der Wert
  inklusive Senden steht zur Information daneben. Im Szenario `load` bietet k6
  200 Records/s für 60 s an (Batches zu 20). Probe-Records messen die
  Verzögerung während der Last und danach, bis jeder Record sichtbar ist.
- **Vollständigkeit:** Seit WP-334 (`55aa9f3`) verlangen beide Szenarien
  `ingest_records_missing` = 0, das Szenario `load` zusätzlich
  `dropped_iterations` = 0. k6 scheitert also, sobald ein Record nie sichtbar
  wird oder k6 die Rate nicht voll anbietet. Vorher bestand der Lauf auch mit
  fehlenden Records. Seit `458a96d` scheitert k6 außerdem an jeder abgelehnten
  Anfrage (`http_req_failed{name:ingest}` und `http_req_failed{name:spool}`
  = 0): Im Szenario `load` startet k6 bis zu eine Iteration mehr als
  angeboten, sodass eine einzelne abgelehnte Anfrage weder als fehlender
  Record noch an der Schwelle von `checks` auffiel.
- **Rückstand (`/readyz`):** Seit `111bdc4` liefert `/readyz` die letzte
  30-s-Stichprobe des Ingest-Workers (`sampled_at`) statt einer Abfrage je
  Anfrage. `ingest_backlog_peak` ist damit gröber; die Werte der Tabelle
  stammen aus der Abfrage je Anfrage. `ingest.js` wertet „Queue leer“ nur mit
  einer Stichprobe aus, die nach dem letzten Anstieg der sichtbaren Records
  genommen wurde, und das Spool-Log nennt das Alter der Stichprobe
  (`f354c5a`).
- **Reserve:** Die Reserve ist auf diesem 4-vCPU-Host knapp. Frühere Läufe in
  WP-334 lagen bei 210–233 Records/s (ein Lauf nach der Nachbesserung
  `55aa9f3` bei 246 Records/s); die Streuung zwischen Läufen liegt bei etwa
  ±10 %. Die verbleibende Grenze ist der Audit-/Outbox-Sperrabschnitt (nächster
  Abschnitt).
- **Kontrollmessung nach dem Gesamtreview:** `58863fa` legt die Identitäts-
  und `max_cis`-Sperren in eigene Lock-Klassen und begrenzt ihre Wartezeit auf
  20 s; `dea65b7` begrenzt das Ingest-Detail von `/readyz` auf 300 ms. Die
  Zahl der Anweisungen je Transaktion bleibt gleich. Das Szenario `spool` mit
  4 Spuren auf dem Stand dieser Korrekturen (frischer Seed mit 10.000
  Objekten, kein anderer Lastlauf auf demselben Host): Abbau ab Sendeende 243 Records/s, inklusive Senden
  287 Records/s, Spool-Flush 34,9 s, kein fehlender Record. Das liegt in der
  Streuung des Wiederholungslaufs; seine Werte bleiben gültig.
- **Kontrollmessung nach den Review-Nachträgen (2026-10-15):** `3f2bfd5`
  lässt jede Transaktion eines Records aus der Queue die Sperren nehmen, auch
  mit einer Spur; `111bdc4` liest das `/readyz`-Detail aus der Stichprobe.
  Szenario `spool` auf dem Stand `111bdc4`, je Lauf frisch geseedet, allein
  auf dem Host. Danach änderten sich nur das Spool-Log von `ingest.js`
  (`f354c5a`) sowie Registrierung und Log des `/readyz`-Details (`0f165cc`),
  nicht der Ingest-Pfad.
  - 4 Spuren: 232–255 Records/s ab Sendeende in drei Läufen, Spool-Flush
    33,1–36,3 s; mit `ANALYZE` nach dem Seed 219 und 232,5 Records/s.
  - 1 Spur, jetzt mit Sperren: 177 Records/s (ohne Sperre im
    Wiederholungslauf 182, in früheren Läufen 161–169).
  - kein fehlender Record

  Die zusätzliche Sperranweisung je Transaktion kostet mit einer Spur
  messbar keinen Durchsatz, und `ANALYZE` nach dem Seed ändert ihn nicht
  nachweisbar (Streuung etwa ±10 %). Die Werte des Wiederholungslaufs
  bleiben gültig.
- **Reihenfolge (E-56):** E-56 ist am 2026-10-15 entschieden (Option 1 mit
  Identitätssperre, Aufgabe A-12 erledigt). WP-334 setzt sie auf die
  Anweisung vom 2026-10-14 um.
  Die Spur folgt dem stärksten Identitätsschlüssel in REC-02-Reihenfolge
  (Seriennummer, Hardware-UUID, MAC, IP). Jede Ingest-Transaktion eines
  Records aus der Queue sperrt die normalisierten Identitätsschlüssel ihrer
  Records (über 256 Schlüssel die ganze Organisation), seit `3f2bfd5`
  unabhängig von der Spurzahl, vorher nur mit mehr als einer Spur.
  Reihenfolge und Dedupe je Gerät sichert diese Identitätssperre, nicht die
  Spur allein: Records eines Geräts mit verschiedenen stärksten Schlüsseln
  können in verschiedenen Spuren laufen. Sperrform und Wartegrenze stehen in
  `docs/betrieb/ingest-spuren.md`.

## GitHub-Lauf (Run 38665599979, Commit `918a832`)

Der Workflow `load.yml` lief am 2026-10-15 auf GitHub, manuell gestartet auf
dem Branch der G1-Nachbesserung.

| Punkt | Wert |
|---|---|
| Lauf | GitHub Actions, [Run 38665599979](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/38665599979) (`workflow_dispatch`) |
| Commit | `918a832`: Code-Stand der G1-Nachbesserung mit allen Nachträgen; spätere Commits ändern nur Dokumentation |
| Eingaben | `objects` = 10000, `duration` = 2m |
| Runner | `ubuntu-latest`, 4 CPUs, 15,61 GB RAM |
| Komponenten | PostgreSQL mit TimescaleDB (Image `timescale/timescaledb:2.17.2-pg16`, mit `timescaledb-tune` eingestellt), NATS, Redis, Server und k6 auf demselben Runner |
| Ablauf | je Szenario ein eigener Job mit frischer Datenbank, Seed (`cmd/seed -objects 10000`) und `ANALYZE`; Server mit 4 Spuren (Standard) und angehobenem Rate-Limit |
| Ergebnis | alle drei Jobs grün, alle k6-Schwellen eingehalten |
| Artefakte | k6-Zusammenfassungen in `load-results-api`, `load-results-ingest` und `load-results-ingest-spool`, aufbewahrt bis 2027-01-13 |

| Job | Messgröße | Ergebnis | Budget |
|---|---|---:|---:|
| `api` (`api.js`, 2 min, alle Szenarien parallel) | Listen p95 | 311 ms | < 500 ms |
| | Suche p95 | 246 ms | < 500 ms |
| | Indizierte Attributfilter p95 | 87 ms | < 500 ms |
| | CI-Detail p95 | 55 ms | < 300 ms |
| | Rack-Layout p95 | 72 ms | < 300 ms |
| | Topologie 2.000 Knoten p95 | 460 ms | < 3 s |
| | Impact Tiefe 20 (kompakt, Oberfläche) p95 | 852 ms | < 2 s |
| | Impact Tiefe 20 (Standardform mit Pfaden) p95 | 882 ms | < 2 s |
| | Bulk 500 IDs p95 | 8,4 s | < 30 s |
| | Checks | 12.549 von 12.549, 0 fehlgeschlagene Anfragen | – |
| `ingest` (`ingest.js`, Szenario `load`: 200 Records/s für 60 s) | angenommen / fehlend | 12.000 / 0 | 0 fehlend |
| | Verzögerung Record → sichtbar (max / p95 der Proben) | 1,84 s / 1,68 s | < 30 s |
| | `POST /ingest/bulk` p95 | 25,6 ms | – |
| | Rückstand max. (`/readyz`, 30-s-Stichprobe) | 212 | – |
| `ingest-spool` (`ingest.js`, Szenario `spool`: 10.000 Records) | Spool-Flush (erstes Senden bis alles sichtbar) | 15,1 s | < 5 min |
| | Abbau ab Sendeende / inklusive Senden | 798 / 663 Records/s | ≥ 200/s |
| | fehlende Records | 0 | 0 |

- **Ingest schneller als auf dem Nachweishost:** Mit dem Batch-Schreibpfad
  ist der Standard-Runner beim Ingest schneller als der lokale Nachweishost
  (Abbau ab Sendeende 798 statt 233 Records/s, Spool-Flush 15,1 statt
  35,9 s, Verzögerung höchstens 1,84 statt 3,1 s). Vor der G1-Nachbesserung
  war es umgekehrt: Der alte Code war von den Commits je Record (fsync)
  bestimmt, und dabei erreichte der Runner nur etwa die Hälfte des
  Nachweishosts. Der letzte `load.yml`-Lauf auf diesem Code (Run 38514977761
  auf Commit `fa76b16`, dem Vorgänger von `02ebb76` mit identischem
  `backend/` und `tests/load/`) maß 38,47 Records/s und eine Verzögerung von
  4 min 11 s; der Nachweishost maß auf demselben Code 74 Records/s und 102 s
  (Basis, Abschnitt „Budgetkorrekturen der G1-Nachbesserung“). Die daraus
  abgeleitete Sorge, der Standard-Runner trage den Lauf nicht, ist mit
  diesem Lauf erledigt.
- **Lesepfade:** Die p95-Werte von `api.js` liegen über denen des lokalen
  Laufs (der Lauf dauerte 2 min statt 60 s), alle unter ihrem Budget.
- **Auflage von E-58:** Die Auflage „mindestens ein grüner `load.yml`-Lauf“
  ist für Commit `918a832` erfüllt. Nach dem Merge auf `main` wiederholt der
  nächtliche Lauf von `load.yml` den Nachweis auf dem Release-Kandidaten.
  Die Entscheidung E-58 ist am 2026-10-15 gefallen: Der Auftraggeber erkennt
  die Referenzumgebung als G1-Nachweis an (Option 1, Aufgabe A-14).

## Verbleibende Grenze: Audit-/Outbox-Sperrabschnitt

Die Analyse stammt aus den Messläufen von WP-334 auf demselben Host (Szenario
`spool`, 4 Spuren, Stichproben aus `pg_stat_activity`, `pg_locks` und
`auto_explain`). Der Wiederholungslauf selbst hat kein Profil erhoben.

1. **Erster Lauf mit Spuren:** 133 Records/s ab Sendeende. 62 % der
   Wait-Events waren `Lock/advisory`; die nicht gewährten Locks betrafen fast
   nur den Ketten-Lock des Audit-Logs (311 Proben, Outbox-Lock 4). Korrektur:
   Audit-Einträge und Events laufen in der Ingest-Transaktion zuletzt vor dem
   COMMIT (`database.BeforeCommit`).
2. **Nach dieser Korrektur:** Advisory-Wartezeit 4 %, Durchsatz unverändert
   (125 Records/s). PostgreSQL lag bei etwa 317 % CPU, der Server bei 16 %.
   Engpass war das `INSERT INTO ci_field_value` der Batch-Provenienz (im Mittel
   2,98 s). Seine Sichtbarkeitsprüfung als Semi-Join verglich jeden Wert mit
   jedem CI der Organisation, weil die Statistik älter als Seed und Ingest war.
   Korrektur: Sichtbarkeit als materialisierte CTE einmal je Statement.
3. **Endstand:** 213–233 Records/s. Die Grenze ist jetzt der Abschnitt unter
   dem Audit-Lock am Ende jeder Transaktion. 52 % der aktiven Proben waren
   `Lock/advisory` auf dem Audit-Lock (Szenario `load`: 12 %, mit 1 Spur 0 %);
   nach der Nachbesserung `55aa9f3` waren es 40 %, keine davon auf
   Identitätssperren. Die Halter des Locks rechnen in etwa 90 % der Proben im
   INSERT von Outbox oder Audit-Log:
   - `Insert on event_outbox` kostet 160–255 ms je 100 Zeilen, davon etwa
     155 ms der Trigger `trg_event_outbox_number`; isoliert sind es etwa
     10 ms.
   - Der Trigger (Migration 000120, `event_outbox_number()`) bestimmt je Zeile
     `max(sequence)` über die Events der Organisation. Mit einer Statistik aus
     der Zeit der leeren Outbox aggregiert der Plan über alle Events, statt den
     höchsten Schlüssel zu lesen.
   - Das INSERT in `audit_log` selbst braucht etwa 6 ms; der Rest ist
     Wartezeit auf den Lock.
4. **Außerhalb des Abschnitts:** Auch die Batch-Kandidatenabfragen (WP-333)
   hängen von der Statistik ab: nach dem Seed im Mittel 300 ms je Batch,
   gelöschte Kandidaten 172 ms. Mit `ANALYZE` nach dem Seed verschwinden diese
   Pläne. Der Durchsatz bleibt dabei gleich (210 Records/s), denn er ist durch
   den Audit-/Outbox-Abschnitt begrenzt.

Ohne Migration lässt sich der Trigger nicht ändern; die G1-Nachbesserung
enthält keine Migration. Das Folge-WP (Offene Punkte, Punkt 5) verkürzt den
Sperrabschnitt voraussichtlich von etwa 200 ms auf etwa 20 ms je Batch.

## Budgetkorrekturen der G1-Nachbesserung

Statement-Zahlen nachher aus `TestIngestStatementBudget`, vorher aus dem
Statement-Profil des Plans; Laufzeiten aus den Messläufen der WPs auf
demselben 4-vCPU-Host.

### Ingest (WP-332 bis WP-334)

| Befund | Ursache | Korrektur | vorher | nachher |
|---|---|---|---:|---:|
| Transaktionen je neuem Record | Identitätssuche, Anlage, Provenienz und Suchindex in getrennten Transaktionen, jede mit eigenem Rahmen (`begin`, `set_config`, `commit`) | Eine Transaktion je Record, BEGIN mit `set_config` in einem Roundtrip; Rahmen und Ledger je Batch, der Ledger als ein `unnest`-Statement (WP-332, `d2daeed`, `82110bf`) | 24 Statements in 4–5 Transaktionen je Record, rund 10 je Batch | 12 Statements in einer Transaktion (Produktion 15); Rahmen 8 Statements in 2 Transaktionen je Batch |
| Anlage neuer Geräte | Kandidatenabfragen und INSERTs je Record | Batch-Kandidatenabfragen mit Trefferschlüssel; `ci.BatchCreator.CreateMany` schreibt `ci`, Historie, Audit, Outbox, Provenienz und Suchindex je Batch (WP-333, `8320a12`) | 12,40 Statements je Record (amortisiert) | 20 neue Geräte in einer Transaktion mit 10 Statements (Produktion 13), 0,90 je Record |
| Serialisierung je Organisation | Ein Mutex je Organisation, ein Batch zugleich | Spuren je stärkstem Identitätsschlüssel (Standard 4) mit Identitätssperre (WP-334, `25f0c79`) | 1 Spur: 182 Records/s | 4 Spuren: 233 Records/s (Abbau ab Sendeende) |
| Wartezeit auf den Audit-Lock | Ketten-Lock vom ersten Audit-Eintrag bis zum COMMIT gehalten | Audit und Outbox zuletzt vor dem COMMIT (`database.BeforeCommit`, WP-334) | 62 % `Lock/advisory` | 4 % |
| Batch-Provenienz | Sichtbarkeitsprüfung als Semi-Join je Wert, mit veralteter Statistik über die ganze Organisation | Sichtbarkeit als materialisierte CTE einmal je Statement (WP-334) | 2,98 s je Statement (Mittel), 125 Records/s | 213–233 Records/s |
| Identitätssperren je Transaktion | Sperre je Schlüssel aller Records eines Fensters, ohne Obergrenze (Gefahr für die Lock-Tabelle des Clusters) | Organisationssperre geteilt plus höchstens 256 Schlüssel, darüber nur die Organisationssperre exklusiv; Fenster enden an der Grenze (WP-334, `55aa9f3`) | bis 20.003 Advisory-Locks in einer Transaktion | höchstens 257 (gemessen 256), höchstens 1.542 je Instanz |

Verlauf je Schritt, 200 Records/s angeboten:

| Stand | Commit | Abbau-Rate | Verzögerung Record → sichtbar |
|---|---|---:|---:|
| Basis vor WP-332 | `02ebb76` | 74 Records/s¹ | 102 s |
| WP-332 | `d2daeed` | 101 Records/s¹ | 58,5 s |
| WP-333 | `8320a12` | 150 Records/s¹ | 19,9 s |
| WP-334, 4 Spuren | `25f0c79` | 213–233 Records/s² | max. 3,67 s |
| WP-334 nach der Nachbesserung, 4 Spuren | `55aa9f3` | 246 Records/s² | max. 6,5 s |
| Wiederholungslauf, 4 Spuren | `dbbfded` | 233 Records/s² | max. 3,1 s |
| Kontrollmessung nach dem Gesamtreview, 4 Spuren | `58863fa`, `dea65b7` | 243 Records/s² | nicht gemessen (nur `spool`) |
| Kontrollmessung nach den Review-Nachträgen, 4 Spuren | `111bdc4` | 232–255 Records/s² | nicht gemessen (nur `spool`) |

¹ Formel von `ingest.js` vor WP-334: angebotene Records geteilt durch die Zeit
vom Lastbeginn, bis der Probe-Record hinter der Last sichtbar ist. Der Wert
enthält das Senden und bleibt deshalb unter 200 Records/s. Die Verzögerung
ist die dieses Probe-Records.

² Abbau ab Sendeende im Szenario `spool`.

Die Basis ist auf demselben Host gemessen. Der Erstlauf (WP-206) ergab auf
seiner Umgebung 58 Records/s und 72 s (siehe unten).

### Impact und Topologie (WP-335)

WP-335 (`a8f5912`, Nachtrag `dbbfded`):

| Befund | Ursache | Korrektur | vorher | nachher |
|---|---|---|---:|---:|
| Kantenlesung | Jede Ebene liest die Kanten mit zwei Sichtbarkeits-Joins auf `ci` | Adaptive Kantenlesung (`relationship.TraverseFrom`): Ebenen ohne `JOIN ci`, Sichtbarkeit in Go (IMP-07, fail closed); ab einer Frontier von mehr als 1.000 CIs alle Kanten der Organisation in einer Abfrage | Ebene 0 des Core-Switches 52 ms (Ausführung) | 13 ms |
| Knotenlesung | `GetByIDs` liest volle Zeilen samt Attributen; ein Array mit 10.000 IDs kostet Senden, Parsen und Planen | `ci.NodeReader.ListNodes`: Projektion mit Override-Abgleich, ab 2.000 IDs Lesung der ganzen Organisation und Filter in Go | `ANY` über 9.999 IDs: 61 ms (Median) | Lesung der Organisation: 23 ms (Median) |
| Antwortumfang | Jeder betroffene CI trägt seinen vollständigen Pfad | `hops`, `via` und `relay` additiv; kompakte Form über `fields=` (API-02), Pfade nur auf Anfrage (E-57) | 5,0 MB | kompakt 2,39 MB (239 Byte je CI), Standardform 5,8 MB |
| Berechnung | Abwärts-Abschluss in jedem der zwei Durchläufe, Pfadkopien je Knoten | Ein Abwärts-Abschluss für beide Durchläufe (`downClosure`), Pfade nur bei Bedarf | – | – |
| Impact gesamt, Einzelanfrage | – | Summe der Impact-Korrekturen | 593 ms | 190 ms (Median, kompakt) |
| Impact unter Parallellast (p95) | – | wie oben | 2,44 s³ | 477 ms kompakt, 546 ms Standardform |
| Topologie Tiefe 1 unter Parallellast (p95) | – | Kanten- und Knotenlesung wie oben | 456 ms³ | 255 ms |

³ Basis `02ebb76` auf demselben Host (`api.js`, 60 s). Der Erstlauf ergab
2,39 s und 624 ms (siehe unten).

## ABN-02

Die Integrationstests laufen bei jedem PR gegen PostgreSQL
(`.github/workflows/ci.yml`). Sie zählen die Statements je Anfrage bzw.
Transaktion (kein N+1) und prüfen per `EXPLAIN` die Indexnutzung.

- **CI-Liste und CI-Detail**
  (`backend/internal/ci/querybudget_integration_test.go`): gleiche Zahl an
  Statements für 10 und 200 Zeilen, höchstens 6; CI-Detail höchstens 5.
- **Topologie und Impact**
  (`backend/internal/topology/querybudget_integration_test.go`,
  `TestTopologyQueryBudget`): Je Regime ist die Zahl der Statements
  unabhängig von der Graphgröße.
  - Hubs mit 20 und 400 Nachbarn (Lesung je Ebene) kosten gleich viel,
    ebenso Hubs mit 1.200 und 3.000 Nachbarn (Lesung der ganzen
    Organisation); höchstens 40 Statements.
  - Mit der Tiefe wächst die Zahl um höchstens zwei Statements je Ebene
    (Kanten, dann die erreichten CIs), nicht mit der Breite: 1 und 40 Ketten
    zu 20 Ebenen kosten gleich viel.
  - Gemessen in WP-335, einschließlich BEGIN, `set_config` und COMMIT: am
    Hub Topologie (Tiefe 2) 16 und Impact (Tiefe 20) 26 Statements; an den
    Ketten in Tiefe 20 Topologie 53 und Impact 63.
- **Impact im Worst Case** (`TestImpactWorstCase`, gleiche Datei, WP-335):
  Der Impact des Core-Switches bei 10.000 Objekten (Graph wie `cmd/seed`, mit
  Client und Site) erreicht 9.997 CIs.
  - Die kompakte Antwort kostet höchstens 250 Byte je betroffenem CI
    (gemessen 239).
  - Die Statements sind dieselben wie bei 3.000 Objekten (26).
  - Jeder Pfad der Standardantwort ist der aus `via` und `relay`
    rekonstruierte.
  - Die Zeit wird gemessen (bestes von 5: 145–164 ms in drei Läufen). Die
    Grenze von 250 ms erzwingt der Test nur mit `RETICORA_PERF_BUDGET=1`, weil
    Zeitgrenzen auf geteilten CI-Runnern unzuverlässig sind; `load.yml` führt
    keine Go-Tests aus.
- **Ingest je Record und Batch**
  (`backend/internal/discovery/ingest_budget_integration_test.go`,
  `TestIngestStatementBudget`, WP-332 bis WP-334), mit dem Schreibpfad des
  Servers (Audit, Outbox, Suchindex, Provenienz):
  - Record für Record: genau eine Transaktion je neuem Record mit höchstens 12
    Statements (mit den Produktionsextras höchstens 15). Der Rahmen eines
    Batches kostet höchstens 8 Statements in 2 Transaktionen, gleich für 5
    und 20 Records.
  - Batch: 20 neue Geräte in einer Transaktion mit höchstens 15 Statements,
    ohne eigenes Lock-Statement. Die Audit-Kette bleibt intakt
    (`audit.Verify`) und hält die Einträge lückenlos in Record-Reihenfolge.
  - Bekannte Geräte: eine Batch-Kandidatenabfrage, Update je Record.
  - Spuren und Queue: Mit mehr als einer Spur und für jeden Record aus der
    Queue nimmt jede Record- und Batch-Transaktion die Identitätssperre genau
    einmal als erstes Statement nach BEGIN; das Budget wächst um dieses eine
    Statement (gemessen: Batch 11 statt 10, Record 13 statt 12). Der Teiltest
    `queue_with_one_lane` (seit `3f2bfd5`) misst das für Records aus der
    Queue mit einer Spur; über HTTP ohne Queue bleibt es bei 10 und 12. Audit
    und Outbox stehen in jeder Record- und Batch-Transaktion zuletzt.
  - `TestIngestBatchMatchesRecordPath` vergleicht Batch- und Einzelpfad über
    den ganzen Ingest.
- **Batch-Anlage** (`backend/internal/ci/create_many_integration_test.go`,
  `TestCreateMany`, WP-333):
  - Ein im Batch angelegtes CI gleicht der Einzelanlage in Zeile, Historie,
    Audit-Eintrag, Event, Provenienz und Suchdokument.
  - Audit-Einträge und Events eines Batches sind lückenlos in der Reihenfolge
    der Items; die Zahl der Statements wächst nicht mit dem Batch.
  - Ein Unique-Konflikt wird mit seiner Position gemeldet und schreibt nichts;
    jeder andere Fehler schreibt für kein Item etwas.

Per `EXPLAIN` stellen die Tests außerdem sicher, dass die Identitäts-, Detail-
und Attributabfragen sowie die Kantenabfragen unter RLS keinen sequenziellen
Scan nutzen. Jeder Zweig der Batch-Kandidatenabfrage liest `ci` nur über
seinen Identitätsindex (`TestIdentityHitsMatchSingle`); Ausnahme ist der Zweig
der Fingerprint-MACs (Offene Punkte, Punkt 8). Die Batch-Provenienz liest `ci`
einmal je Statement (`TestRecordDiscoveredBatchReadsCIsOnce`).

## Bewertung für G1

- **NFR-02:** erfüllt im Wiederholungslauf und im GitHub-Lauf, alle Budgets
  in beiden Impact-Formen. Blocker B2 ist mit WP-335 geschlossen.
- **NFR-03:** erfüllt mit 4 Spuren, lokal und auf GitHub. Blocker B1 ist mit
  WP-332 bis WP-334 geschlossen.
- **NFR-10:** Der Lasttest gegen NFR-01 bis NFR-03 mit 10.000 Objekten ist
  gelaufen und bestanden; das Ergebnis steht in diesem Dokument.
- **GATE-03 „Lasttest bestanden“:** erfüllt mit zwei Nachweisen: dem lokalen
  Wiederholungslauf auf Commit `dbbfded` und dem GitHub-Lauf 38665599979 auf
  Commit `918a832`. Der Auftraggeber hat beide am 2026-10-15 als G1-Nachweis
  anerkannt (E-58, Option 1); ein dedizierter Referenzhost folgt vor GA
  (SR-06). Die Auflage eines grünen
  `load.yml`-Laufs ist erfüllt; `load.yml` läuft auf GitHub manuell oder
  nächtlich auf dem Standardbranch und wiederholt den Lauf nach dem Merge auf
  dem Release-Kandidaten.
- **G1 insgesamt:** mit der Freigabe des Auftraggebers vom 2026-10-15
  bestanden. B5 ist mit E-17 geschlossen. B3 (Pentest, E-28, A-07), B4
  (v2-Texte, E-02, A-02) und B6 (NFR-09, Subprozessorenliste, E-35, A-11)
  sind laut Erklärung des Auftraggebers erledigt; ihre Nachweise sind
  nachzureichen, ebenso der grüne CI-Lauf auf PR #64 und der Merge nach
  `main`. Stand und Begründung stehen in `docs/acceptance/g1-gate.md`.

## Erstlauf (WP-206, historisch)

Der erste Lauf vor Gate G1 (WP-206) lief auf der Entwicklungsumgebung: 4 vCPU
und 15 GB RAM. Auf demselben Host liefen PostgreSQL 16 mit TimescaleDB 2.17
(Docker), Redis, NATS, der Server und k6.

- Der Server war ein einzelner Prozess.
- Das Rate-Limit war für den Lauf angehoben (siehe README).
- Die Lastszenarien liefen parallel, mit 5 VUs je Lesepfad und 2 VUs für
  Topologie und Impact.

Seine Befunde führten zu B1 und B2 und zur G1-Nachbesserung. Für das Gate
gelten die Werte des Wiederholungslaufs oben.

### Ergebnis NFR-02 im Erstlauf (p95, 60 s, 10.000 Objekte)

| Budget | Szenario | p95 | Budget | Ergebnis |
|---|---|---:|---:|---|
| Listen | `GET /cis?limit=50` | 307 ms | < 500 ms | erfüllt |
| Suche | `GET /cis?search=…` | 249 ms | < 500 ms | erfüllt |
| Indizierte Attributfilter | `filter[status]`, `filter[hostname]` | 103 ms | < 500 ms | erfüllt |
| CI-Detail | `GET /cis/{id}` | 86 ms | < 300 ms | erfüllt |
| Rack-Layout | `GET /racks/{id}/layout` | 104 ms | < 300 ms | erfüllt |
| Topologie 2.000 Knoten | Core-Switch, Tiefe 1 (5.000 Knoten) | 624 ms | < 3 s | erfüllt |
| Impact Tiefe 20 | Core-Switch (9.997 betroffene CIs) | 2,39 s | < 2 s | verfehlt unter Parallellast; einzeln 0,55–0,65 s |
| Bulk 500 IDs | Statuswechsel bis Job-Ende | 9,8 s | < 30 s | erfüllt |

Checks: 100 % (5.621 von 5.621). Der Impact-Wert war der Worst Case: Die ganze
Organisation fiel aus, und die Antwort umfasste rund 5 MB. Er lag nur unter
der gleichzeitigen Last aller Szenarien auf demselben 4-Kern-Host über dem
Budget. Ein einzelner Aufruf brauchte 0,55–0,65 s.

### Ergebnis NFR-03 im Erstlauf

| Messgröße | Ergebnis | Budget | Ergebnis |
|---|---:|---:|---|
| Annahme `POST /ingest/bulk` (20 Records je Anfrage), p95 | 15 ms | – | – |
| Abbau-Rate der Queue (neue Geräte) | 58 Records/s | 200 Records/s | **verfehlt** |
| Verzögerung Record → sichtbar bei 200 Records/s Angebot | 72 s (Rückstau) | < 30 s | **verfehlt** |
| Verzögerung Record → sichtbar bei 50 Records/s Angebot | 3,6 s | < 30 s | erfüllt |
| Spool-Flush 10.000 Records | im Erstlauf nicht gemessen (seit WP-334 Szenario `spool`) | < 5 min | – |

Analyse (Statement-Profil des Servers während des Laufs):

- **Statements je Record:** Ein Record kostete rund 55 Statements in etwa 12
  getrennten Transaktionen. Darin enthalten waren:
  - Identitätssuche und gelöschte Kandidaten
  - Feldauflösung (`ResolveFields`)
  - CI-Anlage mit Audit, Historie und Outbox
  - Such- und KI-Index
  - Provenienz
  - Ledger
- **Engpass:** Die Datenbank war dabei nur zu etwa einem Drittel ausgelastet;
  es dominierten die Roundtrips.
- **Serialisierung:** Die Verarbeitung je Organisation war bewusst serialisiert
  (Mutex je Organisation, Grundlage der Identitäts-Deduplizierung), daher
  entsprach der Durchsatz einer Organisation dem Kehrwert der Zeit je Record.

Daraus folgten WP-332 bis WP-334: wenige Transaktionen je Record,
Batch-Schreibpfad und parallele Spuren je Identitätsschlüssel (siehe
„Budgetkorrekturen der G1-Nachbesserung“).

### Budgetkorrekturen in WP-206

Der Erstlauf deckte Fehler auf, die in WP-206 behoben wurden:

| Befund | Ursache | Korrektur | vorher | nachher |
|---|---|---|---:|---:|
| Topologie Tiefe 2 (10.000 Knoten) | Rekursives CTE expandierte jeden Knoten auf jeder Tiefe neu | Breitensuche mit einer Abfrage je Ebene (`relationship.TraverseFrom`) | 4,4 s | 0,5 s |
| Impact Core-Switch (10.000 Knoten) | wie oben, zusätzlich O(V·E) in `computeImpact` und `downSet` | Inzidenzindex und Worklist | 84 s | 0,6 s |
| Bulk 500 IDs | 500 Änderungen streng nacheinander | 8 parallele Änderungen, Bericht in ID-Reihenfolge | 32 s | 7,7–9,8 s |
| Ingest: Identitätssuche | `lower(hostname)` ist nicht leakproof; unter RLS war `idx_ci_hostname` für die App-Rolle unbenutzbar, also ein Seq-Scan je Record | Gespeicherte Spalten `hostname_lc`/`fqdn_lc` mit Index (Migration 000134), Prädikat nur aus vorhandenen Identitätsteilen | 15 ms/Record, linear | Index (< 1 ms) |
| Ingest: Kandidaten | `GetByID` je Kandidat (N+1) | ein `GetByIDs` | – | – |
| Ingest: Provenienz | eine Transaktion je Feld (~10 je Record) | `RecordDiscoveredMany`: ein Statement je CI | – | – |
| Ingest: Projektion `ci_field_value` → `ci.attributes` | Zeilen-Trigger aktualisierte das CI je Feldwert (alle CI-Trigger je Feld) | Statement-Trigger mit Transition Tables (Migration 000135) | – | – |
| Ingest gesamt | – | Summe der Ingest-Korrekturen | 16 Records/s | 58 Records/s |

## Offene Punkte (Folge-WPs)

1. **NFR-03 Durchsatz:** erledigt mit WP-332 bis WP-334 (`d2daeed`,
   `8320a12`, `82110bf`, `25f0c79`, `55aa9f3`; Korrekturen aus dem
   Gesamtreview `58863fa`, `dea65b7`; Nachträge zu seinen nicht
   blockierenden Befunden `3f2bfd5`, `458a96d`, `111bdc4`, `f354c5a`,
   `0f165cc`). Mit 4 Spuren: Abbau
   233 Records/s ab Sendeende, Verzögerung höchstens 3,1 s. Die Reserve auf
   dem 4-vCPU-Host ist knapp (Punkt 5).
2. **Impact unter Parallellast im Worst Case:** erledigt mit WP-335
   (`a8f5912`, `dbbfded`): p95 477 ms in der kompakten Form, 546 ms in der
   Standardform. Die Messung auf einem dedizierten Referenzhost bleibt SR-06
   (vor GA); für G1 genügt der lokale Lauf nach E-58 (entschieden am
   2026-10-15).
3. **Spool-Flush:** erledigt mit WP-334: Szenario `spool` in `ingest.js` und
   `load.yml`, 10.000 Records in 35,9 s.
4. **Listen über 10.000 Objekte:** Bei 28.000 CIs lag `GET /cis` bei p95 1,9 s
   (Gesamtzahl per `COUNT(*)` je Listenanfrage und Sortierung über die ganze
   Organisation). Das ist für den Headroom bis 50.000 Objekte (NFR-01) zu
   prüfen. Nach E-17 (entschieden am 2026-10-15) ist das über etwa 25.000
   Objekten eine bekannte Einschränkung; ein Folge-WP vor GA behebt sie.
5. **Outbox-Nummerierung:** Der Trigger aus Migration 000120
   (`event_outbox_number()`) bestimmt `max(sequence)` je Zeile und liegt im
   Audit-/Outbox-Sperrabschnitt. Folge-WP mit Migration: Zähler je
   Organisation oder `ORDER BY sequence DESC LIMIT 1`. Das verkürzt den
   Sperrabschnitt voraussichtlich von etwa 200 ms auf etwa 20 ms je Batch und
   gibt NFR-03 mehr Reserve.
6. **Statistik nach Massenimport:** Nach großem Seed oder Onboarding erzeugen
   die Batch-Kandidatenabfragen und der RLS-Organisationsfilter Pläne über die
   ganze Organisation, bis Autovacuum analysiert. Folge-WP: `ANALYZE` nach
   großen Importen oder robuste Abfrageformen für die Batch-Kandidatenabfragen.
7. **Knotenlesung und Mehrfach-Impact:** `ListNodes` liest ab 2.000 IDs die
   ganze Organisation; die Schwelle ist mit E-17 (50.000 Objekte) neu zu
   bewerten. `GET /impact?ci_ids=` traversiert je ausgefallenem CI; die Kanten
   sollen innerhalb einer Anfrage wiederverwendet werden.
8. **Fingerprint-MACs:** Die Identitätssuche über Fingerprint-MACs liest
   weiter einmal je Batch alle CIs der Organisation, denn `jsonb`-Containment
   ist unter RLS nicht leakproof. Die Lösung braucht eine Migration.
9. **`AdoptUnlicensed`:** Die Anlage beim Anheben von `max_cis` nimmt die
   `max_cis`-Sperre nicht. Nebenläufig zum Ingest mit Spuren bleibt ein kleines
   Fenster für eine Überschreitung (vorbestehend).
10. **Altconsumer:** kein Folge-WP. Der NATS-Consumer `reticora-ingest`
    bleibt dauerhaft bestehen: Der Server legt ihn wieder an, sobald er
    fehlt, ein Entfernen ist nicht vorgesehen. Leer kostet er je
    Ingest-Instanz einen Abruf pro Sekunde (`docs/betrieb/ingest-spuren.md`).
11. **Override-Abgleich der Knotenlesung:** `ci_field_value` hat keinen Index
    für den Override-Abgleich der Organisationslesung, und der Seed schreibt
    keine Feldwerte. In einer per Discovery befüllten Organisation mit 10.000
    Objekten kostet der Abgleich laut Review 30–40 ms mehr; die Einzelanfrage
    im Worst Case läge dann voraussichtlich über 250 ms. Folge-WP mit
    Migration: partieller Index auf `ci_field_value (organization_id, ci_id)`
    für Overrides und ein Budget-Fixture mit Feldwerten.
