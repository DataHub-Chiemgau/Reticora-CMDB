# Lasttest G1 (NFR-10)

Nachweis zu NFR-01, NFR-02, NFR-03 und ABN-02 vor Gate G1. Stand
2026-10-26 (WP-439, `t4-load-report`): Der Bericht folgt den
Akzeptanzkriterien von WP-439. Messbasis ist der Workflow `load.yml` auf der
nach E-58 anerkannten Umgebung (GitHub-Standard-Runner mit 4 CPUs); die
Messbasis der Budgets sind 10.000 Objekte je Organisation (E-17).

Nachtrag 2026-10-26 (WP-612, `k3n-load-evidence`): Nach den K3-Nachträgen
WP-607 bis WP-611 liegen drei weitere vollständige Läufe auf `main` vor
(Commit `3910aae`). Der Workflow `load-evidence.yml` (Run 40320307297) hat
sie aus ihren Archiven nach E-58 bewertet: alle Budgets im Median gehalten,
kein Job in zwei Läufen rot (Abschnitt „Nachweisläufe nach den
K3-Nachträgen“). Damit sind AK 3 von WP-439 und die Offenen Punkte 1 und 11
erledigt.

Nachtrag 2026-10-28 (WP-658, `k7n-preauth-concurrency`, F-387): Lauf
40713089267 auf `e5c2d86` hatte im Job `api` 19 von 17.879 und im Job
`nfr01-volume` 23 von 13.920 fehlgeschlagene Anfragen, darunter alle drei
Bulk-Anfragen (429 des Pre-Auth-Limiters, `tests/load` und `load.yml`
unverändert seit `3910aae`). Ursache: Seit WP-462 hielt jede Anfrage mit
gültigem API-Key während der Schlüsselprüfung eine Einheit des
Pre-Auth-Budgets ihrer IP (20/min); mehr als 20 gleichzeitige Prüfungen von
k6 (eine IP) erhielten 429. Seit WP-658 wartet eine solche Anfrage auf eine
frei werdende Einheit; 429 gibt es erst, wenn Fehlversuche das Budget
verbraucht haben. Lokal (Server von WP-658, `load.yml`-Einstellungen, Seed
10.000 Objekte, `api.js` 60 s): 0 von 4.542 Anfragen fehlgeschlagen, kein
429 (vorher auf demselben Bestand 32 fehlgeschlagene Checks, 26 × 429). Der
Wiederholungslauf von `load.yml` auf GitHub **steht aus**.

**Quelle der Werte:** Die Werte der Läufe 1 bis 3 stammen aus den
k6-Zusammenfassungen und Protokollzeilen in den Job-Logs der Läufe
40269518264, 40270929166 und 40271818796 (Abschnitt „Läufe“). Das Archiv je
Lauf (Artefakt `load-archive-<Lauf-ID>`) hat der Job `archive` in allen
drei Läufen gebaut; nach `docs/acceptance/artefakte/lasttest/<Lauf-ID>/`
übernommen ist es noch nicht, weil die Arbeitsumgebung den Blob-Speicher
der GitHub-Artefakte nicht erreicht (Übernahme siehe `tests/load/README.md`,
Abschnitt „Archiv eines Laufs“). Werte, die nur im Archiv stehen (`lscpu`,
`/proc/meminfo`), sind als „Archiv steht aus“ markiert und **nicht
belegt**. Alle anderen Zahlen stammen aus den genannten GitHub-Läufen
(Lauf-ID angegeben) oder, als historisch gekennzeichnet, aus früheren
Nachweisläufen. Die Nachweisläufe 40316485305, 40316488267 und 40316491988
(WP-612) sind zusätzlich in GitHub Actions aus ihren Archiven bewertet
(`load-evidence.yml`, Run 40320307297, Artefakt
`load-evidence-40320307297`); ihre Mediane stammen aus dieser Bewertung,
die Einzelwerte aus den Job-Logs. Fett: Schwelle verfehlt.

## Stand der Akzeptanzkriterien (WP-439)

| AK | Inhalt | Stand | Nachweis |
|---|---|---|---|
| 1 | Seed-Optionen für die NFR-01-Volumina, deterministisch und begrenzt | erfüllt; Tests lokal grün (TimescaleDB 2.31.0); in den drei Läufen erzeugt der Seed die Volumina vollständig | `backend/cmd/seed` (`-interfaces-per-ci`, `-ips-per-ci`, `-relationships-per-ci`, `-orgs`); `seed_integration_test.go`: `TestSeedNFR01Volumes` (Mengen, Verteilung, Determinismus, zweiter Lauf), `TestSeedOptionsAreBounded` (Grenzen); Abschnitt „NFR-01-Volumina“ |
| 2 | `load.yml`-Job `nfr01-volume` mit `api.js` und den NFR-02-Schwellen | erfüllt; der Job läuft in allen Läufen. Vor den K3-Nachträgen war er in drei Läufen **rot** (Punkt 11); nach WP-607 bis WP-611 hält er jedes Budget im Median und ist in einem von drei Läufen rot | `.github/workflows/load.yml` (Matrix-Eintrag `nfr01-volume`, Schritt „Seeded volumes (NFR-01)“) |
| 3 | Drei aufeinanderfolgende vollständige `load.yml`-Läufe mit exit 0, archiviert; seit der Ergänzung zu E-58 vom 2026-10-26 bewertet nach dem Median der drei Läufe | **erfüllt** (nach der Ergänzung zu E-58, WP-612) | Drei vollständige Läufe auf `3910aae` (Abschnitt „Nachweisläufe nach den K3-Nachträgen“); `load-evidence.yml` (Run 40320307297) bewertet ihre Archive mit `tests/load/evaluate-runs.sh`: alle Budgets gehalten, Exit 0. Die drei Läufe davor (`8ca7f4b`) verfehlten `ingest-rediscover` und `nfr01-volume`. Die Archive liegen als Artefakte der Läufe vor; ins Repository übernommen sind sie noch nicht (Retest WP-204) |
| 4 | Bericht: p95 je Budget aus den drei Läufen, 200 Records/s und Max-Drain gegenüber 1.000 Records/s [P5], NFR-01-Volumina gemessen, Umfangskorrektur SR-04, offene Punkte mit Folge-WP oder Entscheidung | Werte der drei Läufe eingetragen; CPU und RAM je Lauf stehen nur im Archiv (**steht aus**); sechs offene Punkte haben noch keine Folge-WP-Nummer (Vorschlag an den Auftraggeber); die Punkte 1 und 11 sind mit WP-607 bis WP-612 (K3-Nachträge) erledigt | dieses Dokument |
| 5 | Retests F-279, WP-206, WP-204 | WP-206 und F-118 **bestanden**; F-279 und WP-204 **stehen aus** (Übernahme der Archive) | Abschnitt „Retests“ |

## Messumgebung und Ablauf (E-58)

- **Umgebung:** GitHub Actions, Standard-Runner `ubuntu-latest` mit 4 CPUs;
  PostgreSQL mit TimescaleDB (gepinntes Image aus `load.yml`), NATS mit
  JetStream, Redis, der Server (ein Prozess) und k6 0.54.0 teilen sich den
  Runner, die Werte sind also konservativ. Der Auftraggeber hat diese
  Umgebung am 2026-10-15 als G1-Nachweis anerkannt (E-58, Option 1); ein
  dedizierter Referenzhost folgt vor GA (SR-06).
- **Konfiguration:** 4 Ingest-Spuren (`RETICORA_INGEST_LANES`, Standard),
  Rate-Limit für den Lauf auf 1.000.000 Anfragen/min angehoben (je Principal
  und je Organisation), `duration` 2 min für `api.js`.
- **Ablauf je Job:** frische Datenbank, Migrationen, Seed, neu aufgebauter
  Suchindex, `ANALYZE`, Server, ein k6-Szenario, danach
  `tests/integrity-check.sql` (jede Prüfung 0) und die Datenbankkosten aus
  `pg_stat_statements`. Jobs: `api` (NFR-02), `ingest` (NFR-03, neue Geräte
  mit 200 Records/s), `ingest-spool` (Spool-Flush und Abbau),
  `ingest-rediscover` (Wiederentdeckung mit 200 Records/s, F-118) und
  `nfr01-volume` (NFR-02 auf den NFR-01-Volumina).
- **Protokoll je Job (E-58):** `run-<job>.txt` mit Lauf- und Server-Commit,
  `lscpu`, `/proc/meminfo`, `free -h`, Spurzahl, Rate-Limits, Seed-Optionen,
  Versionen und Container-Images; `result-<job>.txt` mit dem Ergebnis des
  Jobs und seiner Schritte; Seed-Dauer und Datenbestand nach dem Seed
  (`seed-<job>.txt`, `volumes-<job>.txt`); die k6-Zusammenfassungen
  `<job>.json` und `<job>.txt`. Der Job `archive` fasst alles in
  `ergebnis.md` zusammen.
- **Fehlgeschlagene Anfragen (F-388, WP-659):** Ein Job ist nur grün, wenn
  keine Anfrage fehlschlägt: `http_req_failed` `rate==0` für den ganzen Lauf
  und je k6-Szenario (`http_req_failed{scenario:<name>}`), in `api.js` und
  `ingest.js`; fehlgeschlagen ist jede Antwort außerhalb 200–399 und jede
  Anfrage ohne Antwort, auch Abfragen und `/readyz`. Eine Toleranz sieht
  E-58 nicht vor („Alle Schwellen sind im Nachweislauf eingehalten“); die
  Schwelle ist deshalb 0. Jeder der drei Bulk-Jobs von `api.js` muss
  gelingen (`bulk_jobs_succeeded` `count==3`, `checks{scenario:bulk}`
  `rate==1`), und `bulk_500_duration` zählt nur gelungene Bulk-Jobs.
  `checks` `rate>0.99` gilt weiter nur für die inhaltlichen Prüfungen der
  Antworten. Jede fehlgeschlagene Anfrage steht mit Name und Status in der
  Zusammenfassung des Jobs, in `failed-<job>.txt` und in `ergebnis.md`, mit
  Methode, Pfad und Anfang der Antwort in `console-<job>.log`.
  Bis WP-659 hatte `api.js` keine Schwelle auf `http_req_failed`, und
  `bulk_500_duration` zählte auch abgewiesene Bulk-Anfragen (1 bis 15 ms):
  Lauf 40713089267 auf `e5c2d86` blieb im Job `api` grün, obwohl 19 von
  17.879 Anfragen und alle 3 Bulk-Anfragen fehlschlugen (429 des
  Pre-Auth-Limits, F-387). Läufe vor WP-659 sind deshalb nur mit
  `http_req_failed` 0 in ihrer k6-Zusammenfassung (Tabelle NFR-02, Zeile
  „Checks / fehlgeschlagene Anfragen“) als fehlerfrei belegt.

| Lauf | CPU (`lscpu`) | RAM (`MemTotal`) |
|---|---|---|
| Lauf 1 | Archiv steht aus | Archiv steht aus |
| Lauf 2 | Archiv steht aus | Archiv steht aus |
| Lauf 3 | Archiv steht aus | Archiv steht aus |

Jeder Job läuft auf einem eigenen Runner; `lscpu` und `MemTotal` stehen je
Job in `run-<job>.txt` des Archivs und nicht im Job-Log. Dass die Runner
verschieden schnell sind, zeigen die Läufe selbst: Bei gleichem Commit
schwankt die Datenbankzeit je Record der Wiederentdeckung zwischen 6,4 und
18,0 ms, der Abbau im Szenario `spool` zwischen 388 und 963 Records/s
(Abschnitt „NFR-03“).

## Läufe

### Nachweisläufe nach den K3-Nachträgen (WP-612)

Drei Läufe per `workflow_dispatch` auf `main`, Commit `3910aae` (Merge von
PR #98 mit WP-607 bis WP-611), `scenarios=all`, `objects` 10.000,
`duration` 2 min, gleichzeitig gestartet, jeweils im ersten Versuch; jeder
Job läuft auf einem eigenen Runner. Integritätsprüfung nach jedem der 15
Jobs: 0 Verletzungen in 20 Prüfungen. Der Job `archive` hat in jedem Lauf
das Archiv gebaut (jeder Job hat ein Artefakt).

| Lauf | Lauf-ID | Commit (Lauf / Server) | `api` | `ingest` | `ingest-spool` | `ingest-rediscover` | `nfr01-volume` | Vollständig | exit 0 |
|---|---|---|---|---|---|---|---|---|---|
| N1 | [40316485305](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40316485305) | `3910aae` / `3910aae` | grün | grün | grün | grün | grün | ja | ja |
| N2 | [40316488267](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40316488267) | `3910aae` / `3910aae` | grün | grün | grün | grün | grün | ja | ja |
| N3 | [40316491988](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40316491988) | `3910aae` / `3910aae` | grün | grün | grün | grün | **rot** | ja | nein |

In N3 verfehlt `nfr01-volume` eine Schwelle: `impact` mit p95 2.094,5 ms
(Budget 2.000 ms); jede andere Schwelle hält.

Bewertung nach E-58 durch `load-evidence.yml` (Run [40320307297](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40320307297)) aus den Archiven
`load-archive-40316485305`, `load-archive-40316488267` und
`load-archive-40316491988` mit `tests/load/evaluate-runs.sh`. Ergebnis:
„Bewertung nach E-58 (Median über drei Läufe): alle Budgets gehalten“,
Exit 0. Mediane aus dieser Bewertung:

| Job | rot in Läufen | Urteil | Budgets im Median (p95 in ms; Bulk, Flush und Verzögerung in s) |
|---|---|---|---|
| `api` | 0 von 3 | **gehalten** (13 von 13) | list 166,5; list_name_filter 299,5; global_search 349,9; filter 125,4; attr_filter 120,1; relationships 260,6; detail 75,2; rack 105,3; topology 766,0; impact 1.148,2; impact_full 1.139,5; bulk_500 15,14 s; Checks 100 % |
| `ingest` | 0 von 3 | **gehalten** (6 von 6) | Verzögerung max 1,59 s; `POST /ingest/bulk` 22,5; 0 fehlend |
| `ingest-spool` | 0 von 3 | **gehalten** (6 von 6) | Spool-Flush 27,41 s; Abbau 414,7/s; 0 fehlend |
| `ingest-rediscover` | 0 von 3 | **gehalten** (7 von 7) | Verzögerung max 2,35 s (Budget < 30 s); 0 fehlend, 0 neue CIs |
| `nfr01-volume` | 1 von 3 (N3) | **gehalten** (13 von 13) | list 103,7; list_name_filter 132,8; global_search 206,6; filter 81,2; attr_filter 78,8; relationships 134,3; detail 56,1; rack 71,6; topology 432,1; impact 1.127,3; impact_full 1.028,7; bulk_500 13,46 s; Checks 100 % |

Gegenüber den Läufen vor den K3-Nachträgen (`8ca7f4b`, Abschnitt
„Bewertung nach E-58“): auf den NFR-01-Volumina `impact` 4.080 → 1.127 ms,
`impact_full` 3.990 → 1.029 ms, `relationships` 2.430 → 134 ms,
`global_search` 720,5 → 206,6 ms, `bulk_500` 38,63 → 13,46 s; die
Wiederentdeckung höchstens 55,60 → 2,35 s Verzögerung im Median (je Lauf
9,14 s, 2,35 s und 2,33 s).

### Archivierte Läufe (AK 3)

Stand vor den K3-Nachträgen (Commit `8ca7f4b`). Den Nachweis von AK 3
führen die Nachweisläufe im vorigen Abschnitt.

Als vollständig zählt ein Lauf mit `scenarios=all` (alle fünf Jobs) im
ersten Versuch, dessen `ergebnis.md` „Vollständig: ja“ meldet; exit 0 heißt
„Ergebnis: alle Jobs grün“.

| Lauf | Lauf-ID | Datum | Commit (Lauf / Server) | `api` | `ingest` | `ingest-spool` | `ingest-rediscover` | `nfr01-volume` | Vollständig | exit 0 |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | [40269518264](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40269518264) | 2026-10-26 | `8ca7f4b` / `8ca7f4b` | grün | grün | grün | grün | **rot** | ja | nein |
| 2 | [40270929166](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40270929166) | 2026-10-26 | `8ca7f4b` / `8ca7f4b` | **rot** | grün | grün | **rot** | **rot** | ja | nein |
| 3 | [40271818796](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40271818796) | 2026-10-26 | `8ca7f4b` / `8ca7f4b` | grün | grün | grün | **rot** | **rot** | ja | nein |

Die drei Läufe liefen nacheinander per `workflow_dispatch` mit
`scenarios=all`, `objects` 10.000 und `duration` 2 min auf dem Branch der
G1-Korrektur (Commit `8ca7f4b`, Stand von WP-439 und WP-459), jeweils im
ersten Versuch. Der Job `archive` meldet für jeden Lauf „missing: 0“ (alle
fünf Jobs vorhanden) und „not green: 1“, „3“ bzw. „2“. Integritätsprüfung
nach jedem der 15 Jobs: 0 Verletzungen in 20 Prüfungen.

Archiv: Artefakte `load-archive-40269518264`, `load-archive-40270929166` und
`load-archive-40271818796` der Läufe; die Übernahme nach
`docs/acceptance/artefakte/lasttest/<Lauf-ID>/` steht aus (Quelle der Werte,
oben). Kein Lauf endet mit exit 0; bewertet wird seit der Ergänzung zu
E-58 nach dem Median der drei Läufe (nächster Abschnitt).

### Bewertung nach E-58 (Median über drei Läufe)

Ergänzung zu E-58 vom 2026-10-26 (`docs/plan/entscheidungen.md`): Ein
Budget gilt als gehalten, wenn der Median seiner Messwerte aus den drei
Läufen im Budget liegt; ein Job gilt als verfehlt, wenn er in mindestens
zwei der drei Läufe rot ist. Für `http_req_failed` (`rate==0`) und
`bulk_jobs_succeeded` (`count==3`) gilt dieselbe Regel; seit WP-659
(F-388) ist ein Job mit einer fehlgeschlagenen Anfrage oder einem nicht
gelungenen Bulk-Job in seinem Lauf rot, und zwei solche Läufe verfehlen
den Job. Die Mediane unten sind aus den Werten der
Läufe 1 bis 3 in diesem Bericht gebildet (Quelle: Job-Logs);
`tests/load/evaluate-runs.sh` bildet sie aus den Archiven, sobald diese
übernommen sind. Fett: im Median verfehlt.

| Job | rot in Läufen | Bewertung | Budgets im Median (p95, ms; Bulk und Verzögerung in s) |
|---|---|---|---|
| `api` | 1 von 3 (Lauf 2) | **gehalten** | list 247,7; list_name_filter 380,4; global_search 485,2; filter 208,6; attr_filter 221,7; relationships 273,7; detail 163,0; rack 212,2; topology 1.050; impact 1.610; impact_full 1.710; bulk_500 19,35 s; Checks 100 % |
| `ingest` | 0 von 3 | **gehalten** | Verzögerung max 1,61 s; `POST /ingest/bulk` 26,4; 0 fehlend |
| `ingest-spool` | 0 von 3 | **gehalten** | Spool-Flush 15,74 s; Abbau 694,6/s; 0 fehlend |
| `ingest-rediscover` | 2 von 3 | **verfehlt** | Verzögerung max **55,60 s** (Budget < 30 s); 0 fehlend, 0 neue CIs |
| `nfr01-volume` | 3 von 3 | **verfehlt** | list 440,3; list_name_filter 495,9; **global_search 720,5**; filter 374,5; **attr_filter 529,8**; **relationships 2.430**; detail 296,9; **rack 419,0**; topology 1.880; **impact 4.080**; **impact_full 3.990**; **bulk_500 38,63 s** |

Damit hält NFR-02 bei 10.000 Objekten nach E-58; NFR-02 auf den
NFR-01-Volumina (Punkt 11) und NFR-03 für die Wiederentdeckung (Punkt 1)
sind verfehlt. Für diese Läufe war AK 3 nicht erfüllt; erfüllt ist es mit
den Nachweisläufen nach den K3-Nachträgen (WP-612).

### Vorlauf: Run 40257485919 (Commit `0f6b322`, nicht archiviert)

[Run 40257485919](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40257485919),
`workflow_dispatch` am 2026-10-26 auf dem Branch der G1-Korrektur,
`scenarios=all`, `objects` 10.000, `duration` 2 min, Server und Seed aus
dem Lauf-Commit `0f6b322`. Der Lauf ist älter als WP-439: Er hat weder den
Job `nfr01-volume` noch den Job `archive`; seine k6-Zusammenfassungen liegen
nur als Artefakte des Laufs auf GitHub (90 Tage) und zählen nicht zu den
drei archivierten Läufen. Die Werte unten stammen aus den k6-Zusammenfassungen
in den Job-Logs.

| Job | Ergebnis | Kernwerte |
|---|---|---|
| `api` | grün | alle Schwellen eingehalten (Tabelle NFR-02); 20.311 von 20.311 Checks, 0 fehlgeschlagene von 16.889 Anfragen |
| `ingest` | grün | 12.020 Records angenommen, 0 fehlend, keine verworfene Iteration; Verzögerung höchstens 1,74 s |
| `ingest-spool` | grün | Spool-Flush 30,7 s, Abbau ab Sendeende 328,3 Records/s, 0 fehlend |
| `ingest-rediscover` | **rot** | Verzögerung Record → gesehen höchstens 33,9 s (p95 31,0 s) gegen < 30 s; 0 fehlend, 0 neue CIs |

Integritätsprüfung nach jedem Job: 0 Verletzungen in 20 Prüfungen.

## NFR-02: p95 je Budget (AK 4)

`api.js`, alle Szenarien parallel (38 VUs), 2 min, 10.000 Objekte, frischer
Seed. Werte in ms, Bulk in s.

| Budget (NFR-02) | k6-Szenario | Schwelle | Vorlauf 40257485919 | Lauf 1 | Lauf 2 | Lauf 3 |
|---|---|---:|---:|---:|---:|---:|
| Listen | `list` (`GET /cis?limit=50`) | < 500 | 89,4 | 247,7 | **503,8** | 216,8 |
| Suche (Namensfilter der Liste) | `list_name_filter` | < 500 | 118,9 | 380,4 | **564,9** | 329,3 |
| Suche (globale Suche der UI) | `global_search` | < 500 | 130,4 | 485,2 | **563,8** | 417,8 |
| Indizierte Attributfilter | `filter` (`status`, `hostname`) | < 500 | 76,2 | 208,6 | 490,4 | 166,4 |
| Attributfilter (durchsuchbares JSONB-Attribut) | `attr_filter` | < 500 | 74,7 | 221,7 | 479,6 | 172,2 |
| Beziehungssuche (ABN-02) | `relationships` | < 500 | 94,4 | 273,7 | **500,6** | 230,8 |
| CI-Detail | `detail` | < 300 | 55,3 | 163,0 | **467,1** | 126,3 |
| Rack-Layout | `rack` | < 300 | 70,7 | 212,2 | **484,4** | 163,6 |
| Topologie 2.000 Knoten | `topology` (Core-Switch, Tiefe 1) | < 3.000 | 377,8 | 1.250 | 845,5 | 1.050 |
| Impact Tiefe 20 (kompakt, Oberfläche) | `impact` | < 2.000 | 704,2 | 1.830 | 1.370 | 1.610 |
| Impact Tiefe 20 (Standardform mit Pfaden) | `impact_full` | < 2.000 | 685,2 | 1.800 | 1.300 | 1.710 |
| Bulk 500 IDs | `bulk_500_duration` | < 30 s | 7,11 s | 20,49 s | 19,35 s | 17,11 s |
| Checks / fehlgeschlagene Anfragen | `checks`, `http_req_failed` | > 99 % / – (seit WP-659: 0 je Lauf und Szenario) | 100 % / 0 | 100 % (15.098 von 15.098) / 0 von 12.893 | 100 % (13.223 von 13.223) / 0 von 11.082 | 100 % (16.165 von 16.165) / 0 von 13.727 |

Im Vorlauf und in den Läufen 1 und 3 hält jedes Budget, mit wenig Abstand
bei `global_search` (485 bzw. 418 ms) und `impact` (1,83 bzw. 1,61 s). In
Lauf 2 verfehlen sechs Szenarien ihr Budget: `list`, `list_name_filter`,
`global_search`, `relationships`, `detail` und `rack`. Ihre Mediane liegen
dabei bei 60 bis 153 ms, nahe an Lauf 1 (41 bis 188 ms); nur p90 und p95
steigen auf 336 bis 565 ms, auch beim Einzelabruf `detail` über den
Primärschlüssel. Die teuren Szenarien `impact` und `topology` waren in
Lauf 2 schneller als in Lauf 1. Das Muster passt zu CPU-Konkurrenz auf dem
Runner, nicht zu einer einzelnen Abfrage. NFR-02 hält bei 10.000 Objekten
damit nicht in drei aufeinanderfolgenden Läufen, wohl aber im Median der
drei Läufe, nach dem seit der Ergänzung zu E-58 bewertet wird (Abschnitt
„Bewertung nach E-58“; Offene Punkte, Punkt 3).

## NFR-03: 200 Records/s und Max-Drain gegenüber 1.000 Records/s (AK 4)

### 200 Records/s je Instanz (Phase 1)

| Messgröße | Budget | Vorlauf 40257485919 | Lauf 1 | Lauf 2 | Lauf 3 |
|---|---:|---:|---:|---:|---:|
| `ingest`: 200 Records/s für 60 s angeboten, angenommen / fehlend | 0 fehlend | 12.020 / 0 | 12.020 / 0 | 12.020 / 0 | 12.000 / 0 |
| `ingest`: Verzögerung Record → sichtbar, max / p95 | < 30 s | 1,74 s / 1,57 s | 1,61 s / 1,59 s | 2,12 s / 1,80 s | 1,46 s / 1,35 s |
| `ingest`: `POST /ingest/bulk` p95 | < 1 s | 26,8 ms | 24,9 ms | 26,4 ms | 37,8 ms |
| `ingest-spool`: Spool-Flush 10.000 Records | < 5 min | 30,7 s | 28,72 s | 15,74 s | 11,43 s |
| `ingest-spool`: Abbau ab Sendeende (inklusive Senden) | ≥ 200/s | 328,3/s (326,1/s) | 387,8/s (348,1/s) | 694,6/s (635,0/s) | 963,0/s (874,2/s) |
| `ingest-rediscover`: 200 Records/s für 45 s, angenommen / fehlend / neue CIs | 0 / 0 | 9.005 / 0 / 0 | 9.009 / 0 / 0 | 9.004 / 0 / 0 | 9.004 / 0 / 0 |
| `ingest-rediscover`: Verzögerung Record → gesehen, max / p95 | < 30 s | **33,9 s / 31,0 s** | 5,14 s / 4,82 s | **55,60 s / 51,55 s** | **55,60 s / 50,95 s** |
| `ingest-rediscover`: Abbau ab Sendeende (inklusive Senden) | – | 65,9/s (114,1/s) | 146,9/s (179,6/s) | 51,5/s (89,5/s) | 62,4/s (89,5/s) |

- **Neue Geräte und Spool:** Mit 200 Records/s neuer Geräte und beim
  Spool-Flush hält NFR-03 im Vorlauf und in allen drei Läufen alle Budgets
  (in den drei Läufen Verzögerung höchstens 2,12 s, Spool-Flush höchstens
  28,72 s, 0 fehlend).
- **Wiederentdeckung:** Werden die geseedeten Server mit 200 Records/s
  wiederentdeckt (SNMP-artige Records mit primärer MAC, zwei Interfaces und
  LLDP-Nachbar, F-118), verarbeitet der Server in den Läufen 2 und 3 nur 52
  bis 62 Records/s ab Sendeende; die Verzögerung erreicht 51 s p95. Lauf 1
  bleibt mit 147 Records/s und 4,82 s p95 unter der Schwelle, bei
  6,4 statt 16 bis 18 ms Datenbankzeit je Record auf demselben Commit.
  **NFR-03 ist bei 200 Records/s für die Wiederentdeckung nicht
  erfüllt**; ob die Schwelle hält, hängt vom Runner ab (Offene Punkte,
  Punkt 1). Stand vor WP-608: In den Nachweisläufen N1 bis N3 hält die
  Wiederentdeckung die 30 s in jedem Lauf (höchstens 9,14 s, Tabelle unten).

Ursachenvergleich F-118 (Szenario `rediscover`, 200 Records/s angeboten,
9.000 Records; Werte aus den Artefakten bzw. Job-Logs der Läufe):

| Lauf | Server-Commit | Abbau ab Sendeende (inklusive) | Verzögerung p95 (max) | DB-Zeit je Record | Puffer je Record | Plankosten je Record | Statements je Record |
|---|---|---:|---:|---:|---:|---:|---:|
| 40256147034 | `c458f0a` (vor WP-434) | 31,6/s (41,3/s) | 2 min 35 s | 76,9 ms | 1.766,6 | 3.639,9 | – |
| 40256148903 | `7996d8d` (nach WP-434) | 77,6/s (95,2/s) | 45,1 s (49,6 s) | 14,9 ms | 898,7 | 733,1 | 57,8 |
| 40257485919 | `0f6b322` (Vorlauf) | 65,9/s (114,1/s) | 31,0 s (33,9 s) | 12,7 ms | 919,5 | 697,9 | 53,8 |
| 40269518264 (Lauf 1) | `8ca7f4b` | 146,9/s (179,6/s) | 4,82 s (5,14 s) | 6,4 ms | 839,8 | 691,5 | 48,6 |
| 40270929166 (Lauf 2) | `8ca7f4b` | 51,5/s (89,5/s) | 51,55 s (55,60 s) | 18,0 ms | 837,2 | 638,5 | 56,8 |
| 40271818796 (Lauf 3) | `8ca7f4b` | 62,4/s (89,5/s) | 50,95 s (55,60 s) | 15,7 ms | 872,1 | 708,4 | 57,7 |
| 40316485305 (N1) | `3910aae` (nach WP-608) | 89,4/s (166,4/s) | 9,04 s (9,14 s) | 9,6 ms | 501,2 | 66,5 | 31,3 |
| 40316488267 (N2) | `3910aae` (nach WP-608) | 253,1/s (195,0/s) | 2,11 s (2,35 s) | 4,2 ms | 402,0 | 64,6 | 29,1 |
| 40316491988 (N3) | `3910aae` (nach WP-608) | 190,7/s (192,6/s) | 2,23 s (2,33 s) | 4,2 ms | 409,2 | 64,0 | 29,2 |

WP-434 (Identitätssuche mit MACs über den Index) senkt Datenbankzeit und
Plankosten je Record auf etwa ein Fünftel und hebt den Abbau auf mehr als
das Doppelte; unter 30 s kommt die Wiederentdeckung damit nur auf einem
schnellen Runner. In den Läufen 1 bis 3 sind Puffer und Plankosten je
Record nahezu gleich (837 bis 872 bzw. 639 bis 708); die Datenbankzeit je
Record schwankt dagegen um den Faktor 2,8. Den Durchsatz anzuheben ist
nicht Teil von WP-439.

Nach WP-608 (Nachweisläufe N1 bis N3): 29 bis 31 Statements, 402 bis 501
Puffer und 4,2 bis 9,6 ms Datenbankzeit je Record; die Verzögerung bleibt in
allen drei Läufen unter 30 s (höchstens 9,14 s), 0 fehlende Records, 0 neue
CIs, Integritätsprüfung 0. Damit ist der Retest von F-118 bestanden.

**Ursache und Korrektur (WP-608, 2026-10-26).** Lokal nachgestellt wie im
Job `ingest-rediscover` (Seed mit 10.000 Objekten, Suchindex, `ANALYZE`,
Server mit 4 Spuren und NATS mit JetStream wie in `load.yml`, `ingest.js`
`SCENARIO=rediscover`; TimescaleDB 2.31.0 mit `pg_stat_statements` und
`track_planning`; Host mit 4 CPUs, mit anderen Arbeiten geteilt). Vor der
Korrektur je Record 53,1 Statements, 11,9 ms Ausführungs- und 2,3 ms
Planungszeit und 894 Puffer, wie in den Läufen 1 bis 3; Abbau ab Sendeende
66 Records/s, Verzögerung bis 34,3 s. Die drei größten Kostenposten je
Record:

1. **Eine Transaktion je bekanntem Gerät mit der seriellen Sperre der
   Organisation.** Jedes Update eines bekannten Geräts lief in einer
   eigenen Transaktion: Identitätssperren, die Kandidaten unter ihnen ein
   zweites Mal gelesen (3 Statements, 0,93 ms Planung), am Ende Audit-Kette
   und Outbox unter den Sperren der Organisation bis einschließlich COMMIT
   samt WAL-Flush. Warten auf die Audit-Sperre 1,85 ms je Record (15,5 %
   der Ausführungszeit), Audit- und Outbox-Insert zusammen 1,62 ms; dieser
   Abschnitt läuft über alle Spuren nacheinander. In einem Lauf des alten
   Codes (mit dem Index aus Punkt 3) mit zeitweise langsamem Datenträger
   (Stichproben: `IO:WALSync`, `LWLock:WALWrite`) stieg das Warten auf
   12,7 ms je Record und der Abbau fiel auf 10 Records/s: dieselbe Mechanik
   erklärt die Streuung der Datenbankzeit je Record zwischen den Läufen
   (6,4 bis 18,0 ms).
2. **Topologie in drei weiteren Transaktionen je Record.** Nachbar suchen,
   Kante suchen und Kante bestätigen liefen je Record in eigenen
   Transaktionen: 12 der 53 Statements (23 %), 0,78 ms Planung und ein
   weiteres COMMIT je Record, obwohl alle Records eines Fetches nur zwei
   Switches als Nachbarn melden.
3. **Adressabgleich ohne Index.** Die beiden Anweisungen des
   Schnittstellenabgleichs auf `ip_address` (Adresse bestätigen, Fehlbeobachtung
   zählen) lasen über `uq_ip_address_org_vrf_interface_address` die Einträge
   aller Adressen der Organisation (`vrf_id` steht vor `interface_id`):
   1,51 ms und 399 von 894 Puffern je Record (45 %), linear mit den Adressen
   der Organisation (NFR-01: 200.000).

Daneben bleibt `UPDATE ci` mit 2,4 ms je Record (20 %): kein HOT-Update,
weil sich `last_seen_at` und `attributes` ändern (28 Indexe), und bei der
ersten Wiederentdeckung der Abgleich von `ci_identity_mac`, weil neue MACs
hinzukommen (einzeln mit `EXPLAIN ANALYZE` als `reticora_app`: 3,7 ms, davon
2,4 ms der Trigger; ohne neue MACs 1,2 ms). Der Server lag bei 0,6 Kernen,
davon ein Viertel Systemaufrufe der Rundreisen und 3,8 % ein je Aufruf neu
gebauter `strings.Replacer` der MAC-Normalisierung.

Korrektur in WP-608, ohne Identitätssperre, `max_cis`-Sperre, Audit-Kette
oder Outbox-Reihenfolge zu lockern (E-56): Records aus der Queue
aktualisieren die bekannten Geräte in der Batch-Transaktion ihres Fensters
unter dessen Identitätssperren und mit dessen Kandidaten, Audit und Outbox
weiter zuletzt, ein COMMIT je Fenster; ein weiterer Record desselben Geräts
folgt wie bisher einzeln. Die Topologie eines Fetches sucht jeden Nachbarn
einmal und liest und bestätigt die Kanten mit je einem Statement. Migration
000165 legt `idx_ip_address_interface` an. Die MAC-Normalisierung nutzt einen
gemeinsamen `Replacer`. `TestRediscoverStatementBudget`
(`backend/internal/discovery/ingest_budget_integration_test.go`) begrenzt die
Statements je wiederentdecktem Record auf 22 (vorher 36,0, nachher 21,6 bei
der ersten und 17,6 bei der zweiten Wiederentdeckung) und prüft, dass das
Ergebnis dem Weg mit einer Transaktion je Record gleicht.

Lokal vorher und nachher auf demselben Host (abwechselnd, je frische
Datenbank; der Host war mit anderen Arbeiten belegt, Last 2 bis 9 auf
4 CPUs, die Zeiten sind daher nur relativ aussagekräftig):

| Lauf | Statements je Record | Ausführung je Record (davon Audit-Sperre) | Planung je Record | Puffer je Record | CPU PostgreSQL + Server je Record | Abbau ab Sendeende (inklusive) | Verzögerung max |
|---|---:|---:|---:|---:|---:|---:|---:|
| vorher 1 | 55,5 | 20,2 ms (3,1 ms) | 4,0 ms | 874 | 28,7 ms | 59,7/s (67,2/s) | **89,0 s** |
| nachher 1 | 30,6 | 11,8 ms (1,1 ms) | 0,6 ms | 428 | 14,7 ms | 113,8/s (134,7/s) | 21,8 s |
| vorher 2 | 55,6 | 19,5 ms (2,9 ms) | 4,3 ms | 925 | 26,9 ms | 27,1/s (69,4/s) | **84,8 s** |
| nachher 2 | 32,3 | 14,2 ms (1,5 ms) | 0,8 ms | 439 | 14,7 ms | 77,4/s (106,9/s) | **39,2 s** |
| vorher 3 | 54,8 | 14,2 ms (1,6 ms) | 3,0 ms | 865 | 26,0 ms | 62,9/s (94,1/s) | **50,7 s** |
| nachher 3 | 30,5 | 10,3 ms (0,9 ms) | 0,5 ms | 421 | 15,3 ms | – (155,4/s) | 13,0 s |

Die Statements je Record enthalten hier auch den Event-Publisher, die
Webhook-Zustellung und die Abfragen von k6; CPU ist die des Containers mit
PostgreSQL und die des Serverprozesses während des k6-Laufs. Im Lauf
„nachher 3“ waren zum Sendeende schon 8.827 von 9.008 Records sichtbar; der
Abbau ab Sendeende (181 Records) ist dort nicht aussagekräftig. Der Bedarf
an CPU je Record halbiert sich (26,0 bis 28,7 auf 14,7 bis 15,3 ms), der
Abbau verdoppelt sich etwa; auf dem belegten Host bleibt einer der drei
Läufe nachher über 30 s. Den Nachweis auf der Umgebung von E-58 führt
WP-612.

### Max-Drain gegenüber 1.000 Records/s [P5]

NFR-03 verlangt in Phase 5 1.000 Records/s je Instanz (Teilumfang [P5]).
Das ist kein G1-Budget; WP-439 misst es nicht als Budget, sondern stellt
den höchsten gemessenen Abbau daneben.

| Lauf | Commit | Abbau ab Sendeende, Szenario `spool` (Max-Drain) | Abbau Wiederentdeckung | gegenüber 1.000/s [P5] |
|---|---|---:|---:|---|
| 38665599979 (historisch) | `918a832` | 798/s | nicht gemessen | 80 % |
| 40257485919 (Vorlauf) | `0f6b322` | 328,3/s | 65,9/s | 33 % bzw. 7 % |
| 40269518264 (Lauf 1) | `8ca7f4b` | 387,8/s | 146,9/s | 39 % bzw. 15 % |
| 40270929166 (Lauf 2) | `8ca7f4b` | 694,6/s | 51,5/s | 69 % bzw. 5 % |
| 40271818796 (Lauf 3) | `8ca7f4b` | 963,0/s | 62,4/s | 96 % bzw. 6 % |

Eine Instanz auf 4 CPUs erreicht 1.000 Records/s in keinem Szenario; am
nächsten kommt der Spool-Abbau in Lauf 3 mit 963 Records/s. Der Abbau im
Szenario `spool` schwankt auf demselben Commit zwischen 388 und
963 Records/s; der frühere Rückgang von 798 (`918a832`) auf 328 Records/s
(`0f6b322`) ist damit nicht als Regression belegt, sondern liegt in der
Streuung der Runner. Das G1-Budget von 200 Records/s hält in jedem Lauf.
[P5]: Offene Punkte, Punkt 2.

## NFR-01-Volumina (AK 4)

NFR-01: 10.000 Objekte je Organisation typisch, Headroom 50.000; je
Organisation 50.000 Interfaces, 200.000 IP-Adressen, 100.000 Beziehungen;
500 Organisationen je SaaS-Instanz in Phase 1 (5.000 in Phase 5, [P5]). Der
Job `nfr01-volume` seedet sie (`cmd/seed -objects 10000 -interfaces-per-ci 5
-ips-per-ci 20 -relationships-per-ci 10 -orgs 500`), zählt sie nach dem Seed
(`volumes-nfr01-volume.txt`, der Job scheitert darunter) und misst `api.js`
mit den NFR-02-Schwellen darauf. Die Demo-Organisation trägt die Volumina,
dazu acme und 500 kleine Organisationen mit dem SIM-01-Datenbestand (je
40 CIs, durchsuchbares Attribut mit eigenem Index, Collector, Reviews,
Webhook). Die zusätzlichen Beziehungen sind `depends_on` zwischen Servern,
die zusätzlichen Adressen liegen eindeutig im Subnetz „Server networks“.

| Größe | NFR-01 | Lauf 1 | Lauf 2 | Lauf 3 |
|---|---:|---:|---:|---:|
| CIs der Demo-Organisation | 10.000 | 10.000 | 10.000 | 10.000 |
| Interfaces der Demo-Organisation | 50.000 | 50.000 | 50.000 | 50.000 |
| IP-Adressen der Demo-Organisation | 200.000 | 200.000 | 200.000 | 200.000 |
| Beziehungen der Demo-Organisation | 100.000 | 100.000 | 100.000 | 100.000 |
| Organisationen der Instanz | 500 | 503 | 503 | 503 |
| Dauer des Seeds (`seed-nfr01-volume.txt`) | – | 6 min 2 s | 6 min 12 s | 3 min 41 s |

p95 von `api.js` auf den NFR-01-Volumina (Job `nfr01-volume`, Werte in ms,
Bulk in s):

| Budget (NFR-02) | k6-Szenario | Schwelle | Lauf 1 | Lauf 2 | Lauf 3 |
|---|---|---:|---:|---:|---:|
| Listen | `list` | < 500 | 440,3 | 468,1 | 305,9 |
| Suche (Namensfilter der Liste) | `list_name_filter` | < 500 | 495,9 | **510,2** | 308,4 |
| Suche (globale Suche der UI) | `global_search` | < 500 | **720,5** | **749,6** | 398,1 |
| Indizierte Attributfilter | `filter` | < 500 | 374,5 | 396,2 | 256,7 |
| Attributfilter (JSONB) | `attr_filter` | < 500 | **529,8** | **570,2** | 345,0 |
| Beziehungssuche | `relationships` | < 500 | **2.430** | **2.650** | **1.070** |
| CI-Detail | `detail` | < 300 | 296,9 | **322,1** | 216,3 |
| Rack-Layout | `rack` | < 300 | **419,0** | **428,1** | 294,6 |
| Topologie 2.000 Knoten | `topology` | < 3.000 | 1.930 | 1.880 | 967,7 |
| Impact Tiefe 20 (kompakt) | `impact` | < 2.000 | **4.080** | **4.270** | **2.700** |
| Impact Tiefe 20 (Standardform) | `impact_full` | < 2.000 | **3.990** | **4.240** | **2.460** |
| Bulk 500 IDs | `bulk_500_duration` | < 30 s | **38,63 s** | **39,51 s** | 24,61 s |

- **Ergebnis:** Der Seed erzeugt die Volumina in allen drei Läufen
  vollständig (503 Organisationen: demo, acme, die Standardorganisation aus
  Migration 000054 und 500 kleine). Alle inhaltlichen Checks bestehen
  (9.918, 9.372 und 14.042), keine Anfrage schlägt fehl, die
  Integritätsprüfung meldet 0 Verletzungen.
  **NFR-02 hält auf den NFR-01-Volumina nicht:** In allen drei Läufen
  verfehlen `impact` (2,70 bis 4,27 s), `impact_full` (2,46 bis 4,24 s) und
  `relationships` (1,07 bis 2,65 s) ihr Budget deutlich; in den Läufen 1
  und 2 zusätzlich `bulk_500`, `global_search`, `attr_filter` und `rack`,
  in Lauf 2 auch `list_name_filter` und `detail`. Das Statement-Profil des
  Jobs (`statements-nfr01-volume.txt`, im Job-Log von Lauf 1) zeigt die
  Ursachen: Die Kantenabfrage für Impact und Topologie lädt je Aufruf alle
  100.000 Kanten der Organisation (im Mittel 1,19 s, sequenzieller Scan
  auf `rack_mount`); `COUNT` und Seite der Beziehungsliste eines CI lesen je
  Aufruf etwa 72.800 Puffer (im Mittel 498 bzw. 485 ms). Diese Abfragen
  belasten die CPU des Runners und verlängern so auch die übrigen
  Szenarien. Offene Punkte, Punkt 11.
- **Lokale Funktionsprobe (kein Nachweis):** Auf einem geteilten
  Entwicklungshost (4 CPUs, andere Lasten parallel) erzeugte der Seed die
  Volumina vollständig (10.000 CIs, 50.000 Interfaces, 200.000 Adressen,
  100.000 Beziehungen, 503 Organisationen) in 5 min 40 s; ein zweiter Lauf
  fügte nichts hinzu. Ein kurzer `api.js`-Lauf darauf bestand alle
  inhaltlichen Checks (2.225 von 2.225, Impact umfasst die Organisation);
  seine Latenzen sind auf diesem Host nicht aussagekräftig. Einzelne
  Impact-Anfragen des Core-Switches dauerten dort mit 100.000 Beziehungen
  etwa dreimal so lang wie auf dem Seed ohne Volumina; ob das Budget unter
  Mischlast hält, zeigt erst der Job `nfr01-volume`.
- **Headroom 50.000 Objekte:** Nach E-17 gilt er ohne Budgetzusage; Listen
  und Suche über etwa 25.000 Objekten sind eine bekannte Einschränkung
  (Offene Punkte, Punkt 4). Der Seed ist bis 50.000 Objekte nutzbar
  (`-objects`), `load.yml` misst mit 10.000.

## Umfangskorrektur SR-04 (AK 4)

SR-04 (`docs/security/review-g1.md`) beschreibt Ausdrucksindizes mit nicht
leakproof Funktionen, die unter RLS für die App-Rolle unbenutzbar sind. Zwei
der drei Klassen sind seit dem letzten Stand dieses Berichts behoben:

- **MAC-Klasse (behoben, WP-434, Migration 000161):** Die Identitätssuche
  über Fingerprint- und Interface-MACs liest die Seitentabelle
  `ci_identity_mac` über ihren Index statt aller CIs der Organisation. Der
  frühere offene Punkt „Fingerprint-MACs“ ist damit erledigt; das Szenario
  `rediscover` misst diesen Pfad (Tabelle F-118).
- **Attributklasse (behoben, WP-435, Migration 000162):** Der Attributfilter
  der CI-Liste nutzt `reticora_attr_text` (LEAKPROOF) in Index und Filter;
  das Szenario `attr_filter` misst ihn (74,7 ms p95 im Vorlauf).
- **Volltextsuche (offen):** `idx_ci_fts` (`to_tsvector`) bleibt unter RLS
  unbenutzbar; die globale Suche liest `search_document` der Organisation
  sequenziell (WP-438). Bei 10.000 Objekten hält sie das Budget (130,4 ms
  p95 im Vorlauf); für den Headroom siehe Offene Punkte, Punkt 6.

Die Lastgrenzen aus SR-04 betreffen damit nur noch die Volltextsuche.

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
seinen Identitätsindex (`TestIdentityHitsMatchSingle`); der Zweig der
Fingerprint-MACs liest seit WP-434 die Seitentabelle `ci_identity_mac` über
ihren Index (Abschnitt „Umfangskorrektur SR-04“). Die Batch-Provenienz liest
`ci` einmal je Statement (`TestRecordDiscoveredBatchReadsCIsOnce`).

## Bewertung (Stand 2026-10-26, nach WP-612)

- **NFR-02:** Bei 10.000 Objekten hält jedes Budget im Median der drei
  Läufe, vor und nach den K3-Nachträgen. Auf den NFR-01-Volumina verfehlten
  `impact`, `impact_full` und `relationships` vor den K3-Nachträgen ihr
  Budget in allen drei Läufen (Punkt 11); nach WP-607 bis WP-611 hält dort
  jedes Budget im Median (`impact` 1.127 ms, `relationships` 134 ms), der
  Job ist in einem von drei Läufen rot (`impact` 2.094,5 ms in N3). NFR-02
  ist nach E-58 **erfüllt**, auch auf den NFR-01-Volumina.
- **NFR-03:** Neue Geräte mit 200 Records/s, Spool-Flush und
  Wiederentdeckung halten nach WP-608 in allen drei Nachweisläufen jedes
  Budget (Verzögerung der Wiederentdeckung höchstens 9,14 s, 0 neue CIs).
  NFR-03 ist für G1 **erfüllt**. 1.000 Records/s (Phase 5, [P5]) ist kein
  G1-Budget: Der Spool-Abbau erreicht 1.092 Records/s in N1, aber nur 386
  und 415 Records/s in N2 und N3 (Punkt 2).
- **NFR-01:** Die Volumina sind in allen sechs Läufen mit `nfr01-volume`
  erzeugt und gezählt (10.000 CIs, 50.000 Interfaces, 200.000 Adressen,
  100.000 Beziehungen, 503 Organisationen); die Budgets von NFR-02 halten
  darauf nach den K3-Nachträgen im Median.
- **NFR-10:** Der Lasttest gegen NFR-01 bis NFR-03 läuft im Workflow
  `load.yml`, die Bewertung über drei Läufe in `load-evidence.yml`. Die
  Archive liegen als Artefakte der Läufe vor, im Repository noch nicht
  (Retest WP-204).
- **GATE-03 „Lasttest bestanden“:** belegt durch die Nachweisläufe N1 bis N3
  und ihre Bewertung nach E-58 (`load-evidence.yml`, Run 40320307297);
  `docs/acceptance/g1-gate.md` verweist darauf. Die Läufe auf dem
  Release-Kandidaten folgen mit WP-595.

## Retests (AK 5)

| Retest | Verlangt | Stand |
|---|---|---|
| F-279 | Doku-Review von `lasttest-g1.md` gegen die archivierten Artefakte | **steht aus**: Der Bericht nennt 1.000 Records/s als [P5] mit Max-Drain und die gemessenen NFR-01-Volumina mit den Werten der drei Läufe aus den Job-Logs; das Review gegen die Archive ist erst nach deren Übernahme möglich. |
| WP-206 | drei saubere Läufe archiviert | **bestanden** nach der Ergänzung zu E-58: Die Nachweisläufe N1 bis N3 halten jedes Budget im Median, kein Job ist in zwei Läufen rot (`load-evidence.yml`, Run 40320307297, aus den Archiven der Läufe). Die Läufe davor (`8ca7f4b`) waren nicht bestanden. Die Übernahme der Archive ins Repository ist Teil von WP-204. |
| F-118 | Wiederentdeckung mit 200 Records/s: Verzögerung < 30 s, 0 neue CIs, Integritätsprüfung 0 | **bestanden** in N1 bis N3 (höchstens 9,14 s, 0 neue CIs, 0 Verletzungen in 20 Prüfungen; Tabelle beim Ursachenvergleich F-118). |
| WP-204 | archivierter `load.yml`-Lauf | **steht aus**: Die Archive `load-archive-<Lauf-ID>` der Läufe sind gebaut (zuletzt N1 bis N3) und in GitHub Actions bewertet; die Übernahme nach `docs/acceptance/artefakte/lasttest/<Lauf-ID>/` steht aus, weil die Arbeitsumgebung den Blob-Speicher der Artefakte nicht erreicht. |

## Offene Punkte

Jeder Punkt nennt ein Folge-WP oder eine Entscheidung. „Folge-WP: offen“
heißt: Für den Punkt ist noch kein WP angelegt; er geht als Vorschlag an den
Auftraggeber, die Nummer wird hier nachgetragen.

1. **NFR-03, Wiederentdeckung mit 200 Records/s:** Verzögerung Record →
   gesehen 51,55 s bzw. 50,95 s p95 in den Läufen 2 und 3, 4,82 s in Lauf 1
   (31,0 s im Vorlauf, 45,1 s in Run 40256148903); Abbau 52–147 Records/s
   bei 49–58 Statements und 6,4–18,0 ms Datenbankzeit je Record
   (Ursachenvergleich F-118). Ursache untersucht und behoben in WP-608
   (`k3n-rediscover-throughput`, K3-Nachträge): eine Transaktion je
   bekanntem Gerät mit der seriellen Audit- und Outbox-Sperre der
   Organisation samt COMMIT, die Topologie in drei weiteren Transaktionen je
   Record und der Adressabgleich ohne Index (Absatz „Ursache und Korrektur
   (WP-608)“ beim Ursachenvergleich F-118). Lokal 31 statt 53 Statements
   und die halbe CPU je Record, Abbau etwa verdoppelt. **Erledigt** mit
   WP-612 (`k3n-load-evidence`): In den Nachweisläufen N1 bis N3 hält die
   Wiederentdeckung die 30 s in jedem Lauf (höchstens 9,14 s, 29 bis 31
   Statements je Record).
2. **NFR-03, 1.000 Records/s in Phase 5 [P5]:** Max-Drain 328–963
   Records/s auf 4 CPUs (Szenario `spool`); kein G1-Budget. Folge-WP:
   offen, Vorschlag an den Auftraggeber (Ingest-Durchsatz für Phase 5,
   zusammen mit Punkt 1).
3. **Dedizierter Referenzhost:** Entscheidung E-58 (Option 1): GitHub-Runner
   mit 4 CPUs als G1-Nachweis, Referenzhost vor GA (SR-06). Die drei Läufe
   zeigen die Grenze dieser Umgebung: Auf demselben Commit verfehlen in
   Lauf 2 sechs leichte Szenarien das p95-Budget von NFR-02 bei Medianen im
   Rahmen der anderen Läufe, und die Datenbankzeit je Record der
   Wiederentdeckung schwankt um den Faktor 2,8. Ein Nachweis „drei Läufe mit
   exit 0“ hängt damit auch vom zugeteilten Runner ab. Entscheidung:
   Ergänzung zu E-58 vom 2026-10-26 (Option A): Bewertung nach dem Median
   der drei Läufe, ein Job ist verfehlt, wenn er in mindestens zwei Läufen
   rot ist (Abschnitt „Bewertung nach E-58“, `tests/load/evaluate-runs.sh`).
   Kein offener Punkt mehr für die Bewertungsregel; der Referenzhost vor GA
   bleibt.
4. **Listen und Suche über etwa 25.000 Objekte (Headroom 50.000):**
   Entscheidung E-17 (Option 1): bekannte Einschränkung ohne Budgetzusage,
   Folge-WP vor GA. Die Zählung und der Index der Standardsortierung der
   CI-Liste sind mit WP-605 (`k1n-cilist-sort`) verbessert; gemessen ist
   über 10.000 Objekten nichts.
5. **Knotenlesung und Mehrfach-Impact:** `ListNodes` liest ab 2.000 IDs die
   ganze Organisation; `GET /impact?ci_ids=` traversiert je ausgefallenem
   CI. Entscheidung E-17: Schwelle für 50.000 Objekte neu bewerten, Folge-WP
   vor GA.
6. **Volltextsuche unter RLS (Rest von SR-04):** gespeicherte
   `tsvector`-Spalte mit Index statt `idx_ci_fts`; Katalogtest gegen neue
   Ausdrucksindizes auf RLS-Tabellen. Folge-WP: offen, Vorschlag an den
   Auftraggeber (Volltextsuche SR-04).
7. **Outbox-Nummerierung:** Der Trigger aus Migration 000120
   (`event_outbox_number()`) bestimmt `max(sequence)` je Zeile im
   Audit-/Outbox-Sperrabschnitt (historischer Abschnitt „Verbleibende
   Grenze“). Folge-WP: offen, Vorschlag an den Auftraggeber (Zähler je
   Organisation, Migration).
8. **Statistik nach Massenimport:** Nach großem Seed oder Onboarding planen
   die Batch-Kandidatenabfragen und der RLS-Organisationsfilter über die
   ganze Organisation, bis Autovacuum analysiert. `load.yml` führt `ANALYZE`
   nach dem Seed aus. Folge-WP: offen, Vorschlag an den Auftraggeber
   (`ANALYZE` nach großen Importen oder robuste Abfrageformen).
9. **`AdoptUnlicensed`:** Die Anlage beim Anheben von `max_cis` nimmt die
   `max_cis`-Sperre nicht; nebenläufig zum Ingest mit Spuren bleibt ein
   kleines Fenster für eine Überschreitung (vorbestehend). Folge-WP: offen,
   Vorschlag an den Auftraggeber (`max_cis`-Sperre bei der Übernahme).
10. **Override-Abgleich der Knotenlesung:** `ci_field_value` hat keinen
    Index für den Override-Abgleich der Organisationslesung; der Seed
    schreibt keine Feldwerte. Folge-WP: offen, Vorschlag an den
    Auftraggeber (partieller Index auf `ci_field_value (organization_id,
    ci_id)` für Overrides, Budget-Fixture mit Feldwerten; Migration).
11. **NFR-02 auf den NFR-01-Volumina:** Mit 100.000 Beziehungen je
    Organisation verfehlen `impact` (bis 4,27 s), `impact_full` (bis 4,24 s)
    und `relationships` (bis 2,65 s) ihr Budget in allen drei Läufen, auf
    langsamen Runnern zusätzlich `bulk_500`, `global_search`, `attr_filter`,
    `rack`, `list_name_filter` und `detail`. Ursachen laut Statement-Profil:
    Die Kantenabfrage für Impact und Topologie lädt alle Kanten der
    Organisation; `COUNT` und Seite der Beziehungsliste eines CI lesen je
    Aufruf etwa 72.800 Puffer für im Mittel 19 Zeilen. Die lokale
    Ursachenanalyse zu diesen Läufen nennt fünf Ursachen (U1 bis U5).
    Folge-WPs (K3-Nachträge, beauftragt am 2026-10-26):
    - WP-607 (`k3n-attr-index-key`): Attribut-Indexe je Schlüssel statt je
      Organisation (U1, Ergänzung zu E-73);
    - WP-609 (`k3n-relationships-arms`): Beziehungsliste (U2);
    - WP-610 (`k3n-hot-path`): `last_used_at` und JIT (U4, U5);
    - WP-611 (`k3n-impact-lean`): Impact-CPU (U3).

    **Erledigt** mit WP-612 (`k3n-load-evidence`): In den Nachweisläufen N1
    bis N3 hält `nfr01-volume` jedes Budget im Median (`impact` 1.127 ms,
    `impact_full` 1.029 ms, `relationships` 134 ms), rot in einem von drei
    Läufen (`impact` 2.094,5 ms in N3). Rest: Die Reserve von `impact` auf
    langsamen Runnern ist klein; der Referenzhost vor GA (Punkt 3, SR-06)
    misst sie ohne geteilte Runner.

Erledigt seit dem letzten Stand: NFR-03-Durchsatz neuer Geräte und
Spool-Flush (WP-332 bis WP-334), Impact unter Parallellast (WP-335),
Fingerprint-MACs (WP-434), Attributfilter unter RLS (WP-435),
`GET /cis` unter Mischlast (WP-438, WP-605; `list` p95 89,4 ms im
Vorlauf). Kein offener Punkt: Der
NATS-Consumer `reticora-ingest` bleibt dauerhaft bestehen (der Server legt
ihn wieder an); leer kostet er je Ingest-Instanz einen Abruf pro Sekunde
(`docs/betrieb/ingest-spuren.md`).

## Frühere Nachweisläufe (historisch)

Die folgenden Abschnitte dokumentieren die Läufe vom 2026-10-14/15, auf die
E-58 und `docs/acceptance/g1-gate.md` verweisen, und den Erstlauf. Ihre
k6-Zusammenfassungen sind nicht im Repository archiviert (lokal nicht
versioniert; die des GitHub-Laufs waren Artefakte des Laufs, aufbewahrt bis
2027-01-13). Für G1 nach der Korrektur gelten die Abschnitte oben.

### Umgebung und Datenbestand (Stand 2026-10-15)

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

### Wiederholungslauf (Commit `dbbfded`)

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

### Ergebnis NFR-02 im Wiederholungslauf (p95, 60 s, 10.000 Objekte)

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
  Knotenlesung mehr (Offene Punkte, Punkt 10).

### Ergebnis NFR-03 im Wiederholungslauf (Ingest)

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

### GitHub-Lauf (Run 38665599979, Commit `918a832`)

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

### Verbleibende Grenze: Audit-/Outbox-Sperrabschnitt

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
enthält keine Migration. Das Folge-WP (Offene Punkte, Punkt 7) verkürzt den
Sperrabschnitt voraussichtlich von etwa 200 ms auf etwa 20 ms je Batch.

### Budgetkorrekturen der G1-Nachbesserung

Statement-Zahlen nachher aus `TestIngestStatementBudget`, vorher aus dem
Statement-Profil des Plans; Laufzeiten aus den Messläufen der WPs auf
demselben 4-vCPU-Host.

#### Ingest (WP-332 bis WP-334)

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

#### Impact und Topologie (WP-335)

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

### Erstlauf (WP-206, historisch)

Der erste Lauf vor Gate G1 (WP-206) lief auf der Entwicklungsumgebung: 4 vCPU
und 15 GB RAM. Auf demselben Host liefen PostgreSQL 16 mit TimescaleDB 2.17
(Docker), Redis, NATS, der Server und k6.

- Der Server war ein einzelner Prozess.
- Das Rate-Limit war für den Lauf angehoben (siehe README).
- Die Lastszenarien liefen parallel, mit 5 VUs je Lesepfad und 2 VUs für
  Topologie und Impact.

Seine Befunde führten zu B1 und B2 und zur G1-Nachbesserung. Für das Gate
gelten die Werte des Wiederholungslaufs oben.

#### Ergebnis NFR-02 im Erstlauf (p95, 60 s, 10.000 Objekte)

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

#### Ergebnis NFR-03 im Erstlauf

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

#### Budgetkorrekturen in WP-206

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
