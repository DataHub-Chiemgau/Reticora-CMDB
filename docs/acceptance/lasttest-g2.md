# Lasttest G2 (NFR-02, NFR-03, ABN-02)

Nachweis zu NFR-02 (Phase-2-Listen und Regression der G1-Budgets), NFR-03
(G1-Regression des Ingest und paralleler Ingest der Massen-Schreibjobs) und
ABN-02 vor Gate G2, nach dem Protokoll von E-58 mit der Ergänzung vom
2026-10-26 (Median über drei Läufe). Gliederung wie
`docs/acceptance/lasttest-g1.md` seit WP-439.

**Stand 2026-11-09 (WP-327, `g2-perf`): Nachweis geführt.** Drei
vollständige `load.yml`-Läufe auf dem Server-Commit `69e6668d` (main), alle
neun Jobs des Jobsatzes G2 in allen drei Läufen grün, bewertet mit
`load-evidence.yml` (Lauf 42683419968): „Bewertung nach E-58 (Median über
drei Läufe): alle Budgets gehalten“, Exit 0. Maßgeblich ist der vorläufige
Budgetsatz G2 (E-146). Gegen die Budgets von NFR-02 (Satz G1) verfehlen im
Median 14 Latenz- und Dauerbudgets ihre Schwelle (Abschnitt „NFR-02“); für
jedes nennt der Abschnitt „Offene Punkte“ einen Folge-WP-Vorschlag.

**Budgetsätze (E-146, Entscheidung des Auftraggebers vom 2026-11-09,
vorläufig, Korrektur bei Bedarf):** Die p95-Schwellen von `api.js` und
`phase2.js` stehen in `tests/load/budgets.json` in zwei Sätzen: G1 (Budgets
aus NFR-02, unverändert) und G2 (vorläufige Schwellen nach Lauf
42649690603, am 2026-11-09 gegen die Laufvarianz revidiert nach Lauf
42661932847: höchster p95 der Läufe 42619469662, 42649690603 und
42661932847 × 1,5, aufgerundet, für alle Metriken). `load.yml` läuft
standardmäßig mit G2 (`BUDGET_GATE`), und `evaluate-runs.sh --gate G2` bzw.
`load-evidence.yml` mit `gate` = `G2` bewertet gegen dieselben G2-Werte. Die
Tabellen im Abschnitt „NFR-02“ nennen beide Werte; maßgeblich für den
G2-Nachweis ist die Spalte „Schwelle G2“. Fehlerschwellen und NFR-03 sind in
beiden Sätzen gleich.

Werkzeuge: `.github/workflows/load.yml` (ein Job je Szenario),
`tests/load/archive-run.sh` (Archiv `load-archive-<Lauf-ID>`),
`tests/load/evaluate-runs.sh` (Bewertung nach dem Median, Jobsatz G2 als
Standard; Selbsttest `tests/load/evaluate-runs.test.sh`, 136 Fälle grün) und
`.github/workflows/load-evidence.yml`; Ablauf und Übernahme der Archive in
`tests/load/README.md`.

**Quelle der Werte:** Mediane, Einzelwerte je Lauf und Urteile stammen aus
der Bewertung von `load-evidence.yml` (Lauf
[42683419968](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/42683419968),
Job-Log und Artefakt `load-evidence-42683419968` mit `bewertung.md`), die
die drei Archive mit `tests/load/evaluate-runs.sh --gate G2` auswertet.
Seed-Dauer, Datenbestand, Integritätsprüfung, die Messgrößen der
Schreibjobs ohne Schwelle und die Headroom-Werte stammen aus den
k6-Zusammenfassungen und Protokollzeilen der Job-Logs der genannten Läufe.
Die Archive `load-archive-<Lauf-ID>` liegen als Artefakte der Läufe in
GitHub (Aufbewahrungsfrist der Artefakte); nach
`docs/acceptance/artefakte/lasttest/<Lauf-ID>/` übernommen sind sie nicht,
weil die Arbeitsumgebung den Blob-Speicher der GitHub-Artefakte nicht
erreicht (Download mit `gh run download` abgewiesen, wie schon bei G1).
Werte, die nur im Archiv stehen (`lscpu`, `/proc/meminfo`, Versionen aus
`run-<job>.txt`), sind als „Archiv steht aus“ markiert und **nicht belegt**.
`load-evidence.yml` hat die Archive in GitHub Actions geladen und ihre
Prüfsummen beim Download geprüft; die Bewertung beruht damit auf den
vollständigen Archiven.

## Bekannte Vorgeschichte

- Die nächtlichen Läufe von `load.yml` (`scenarios=all`) waren vom
  2026-11-03 bis 2026-11-09 rot.
- Ursachen und Korrekturen:
  - PR #147: globale Suche mit Feldrechten ohne Vollscan der
    eingeschränkten Typen, Reindex-Batch ohne Nested-Loop-Anti-Join beim
    Löschen veralteter Feldzeilen, Zählung der sichtbaren Records im
    Ingest-Lastlauf über die gedeckelte Gesamtzahl von `GET /cis` hinaus
    (E-104, `tests/load/count.js`).
  - PR #148: Systemsicht `servers_without_monitoring` (`view_presets`) ohne
    Typbaum und Messwert-Vollscan je CI; Listen-Indizes 000276 bis 000279
    (`idx_ci_list_sort` mit `owner_team_id`, Ticket-, Asset- und
    Mengenartikel-Liste); Hostname- und FQDN-Filter der CI-Liste über die
    Kleinschreibungsspalte; eingeschränkte Suche liest die Zusatzfeldzeilen
    einmal je Suche.
  - Lastregression Runde 4 (PR #150, `47c9f76a`, `b5bf2342`):
    `upstream_of`/`downstream_of` je Arbeitstabellenzeile über den
    Kantenindex (`relation_search`); Bulk schreibt je CI in einer
    Transaktion statt sechs (`bulk_500`, `bulk_fields_500`).
- Lauf 42619469662 (`fc59cd22`): zusätzlich echte Fehler in `phase2`
  (`relation_search` mit EOF-Abbrüchen, `http_req_failed` 0,10 %), behoben
  durch die Beziehungssuche mit LATERAL/OFFSET 0; Bulk seitdem in einer
  Transaktion je CI.
- Lauf 42649690603 (`6d019370`): rot in `api`, `nfr01-volume`,
  `api-large-28000` und `phase2` nur durch p95-Latenz- und
  Bulk-Dauerschwellen (`http_req_failed` 0 %, `checks` 100 % in allen
  Jobs). Entscheidung des Auftraggebers vom 2026-11-09: vorläufiger
  Budgetsatz G2 (E-146, Abschnitt „NFR-02“).
- Lauf 42661932847 (`e3c1aa7d`): rot in `api` und `nfr01-volume` nur durch
  p95-Schwellen von G2 (`http_req_failed` 0 %, `checks` 100 %); dieselben
  Szenarien schwanken zwischen den Läufen auf dem 4-vCPU-Runner stark
  (z. B. `blast_radius` in `nfr01-volume` 1,67 s bzw. 3,29 s). Revision von
  E-146 vom 2026-11-09 (Laufvarianz): G2 für alle Metriken aus dem höchsten
  p95 der drei Läufe × 1,5.
- Nachweisläufe auf `69e6668d` (Merge von PR #152 mit der Revision von
  E-146): alle Jobs grün (Abschnitt „Läufe“).

## Messumgebung und Ablauf (E-58)

- **Umgebung:** GitHub Actions, Standard-Runner `ubuntu-latest` (E-58,
  Option 1; dedizierter Referenzhost vor GA, SR-06), Runner-Image
  `ubuntu-26.04` Version `20261101.186.1` in allen Jobs der Nachweisläufe,
  Azure in wechselnden Regionen (centralus, westus2, eastus, northcentralus
  u. a.). PostgreSQL 16 mit TimescaleDB (`timescale/timescaledb:2.31.0-pg16`,
  per Digest gepinnt, `pg_stat_statements` und `auto_explain`), NATS 2.10 mit
  JetStream, Redis, MinIO (Jobs `import`, `template`, `computed`), k6 0.54.0;
  Server und k6 teilen sich den Runner, die Werte sind konservativ. Jeder Job
  läuft auf einem eigenen Runner.
- **Release-Kandidat G2:** Server, Seed und Migrationen aus
  `69e6668d93ef85d0e28d5eb271672b81a167a7dc` (main, Merge von PR #152) in
  allen drei Läufen; Lauf-Commit derselbe (`workflow_dispatch` auf `main`,
  `server_ref` leer). `ci.yml` auf demselben Commit (Lauf 42675826333) ist
  grün.
- **Konfiguration:** `RETICORA_MODULES` = `P2` (Release-Konfiguration G2,
  WP-830), `RETICORA_INGEST_LANES` = 4, `RETICORA_SEARCH_BACKEND` =
  `postgres` (Standard von `backend/internal/config/config.go`; `load.yml`
  setzt die Variable nicht), `BUDGET_GATE` = `G2`, Rate-Limit 1.000.000
  Anfragen/min je Principal und je Organisation, `duration` 2 min für
  `api.js` und `phase2.js`, `INGEST_DURATION_S` = 120 s für den Ingest neben
  `writes.js`, `LATENCY_BUDGETS` `all` (`api`, `nfr01-volume`), `lists`
  (`api-large-28000`), `none` (`api-large-50000`). Werte aus der Umgebung
  der Schritte im Job-Log; `run-<job>.txt` im Archiv.
- **Seed-Eingaben:** `objects` 10.000 (CIs), `assets`, `tickets` und
  `consumables` je 10.000 (Standardeingaben von `load.yml`, WP-822; in
  `api-large-50000` die SIM-01-Mengen); `nfr01-volume` zusätzlich
  `-interfaces-per-ci 5 -ips-per-ci 20 -relationships-per-ci 10 -orgs 500`.
  Feldwerte in `ci_field_value` (WP-821, `ensureFieldValues` in
  `backend/cmd/seed/seed_assets.go`): je CI der Demo-Organisation die drei
  erkannten Felder `hostname`, `serial_number` und `management_ip` der
  Quelle `snmp`, also 30.000 Feldzeilen bei 10.000 CIs, dazu ein Override
  auf `hostname` an jedem 20. CI (500). `volumes-<job>.txt` zählt
  `ci_field_value` nicht; die Zahl folgt aus dem Seed-Code, nicht aus einer
  Messung. Zusammensetzung nach dem Seed (`volumes-<job>.txt`, im Job-Log;
  in allen drei Läufen gleich, Seed deterministisch):

  | Job | CIs (Demo) | Assets | Tickets | Mengenartikel | Feldwerte (`ci_field_value`, aus dem Seed-Code) | Interfaces | Adressen | Beziehungen | Organisationen | Seed-Dauer L1 / L2 / L3 (s) |
  |---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
  | `api` | 10.010 | 10.000 | 10.000 | 10.000 | 30.000 (500 Overrides) | 10.000 | 10.000 | 19.993 | 3 | 156 / 150 / 207 |
  | `phase2` | 10.010 | 10.000 | 10.000 | 10.000 | 30.000 (500 Overrides) | 10.000 | 10.000 | 19.993 | 3 | 207 / 185 / 255 (+ Schlüssel viewer 12 / 8 / 12) |
  | `nfr01-volume` | 10.010 | 10.000 | 10.000 | 10.000 | 30.000 (500 Overrides) | 50.000 | 200.000 | 100.000 | 503 | 266 / 322 / 384 |
  | `ingest`, `ingest-spool`, `ingest-rediscover` | 10.010 | 10.000 | 10.000 | 10.000 | 30.000 (500 Overrides) | 10.000 | 10.000 | 19.993 | 3 | 108 bis 236 |
  | `api-large-28000` | 28.010 | 10.000 | 10.000 | 10.000 | – | 28.000 | 28.000 | 55.993 | 3 | 199 / 151 / 236 |
  | `api-large-50000` (SIM-01-Mengen) | 50.010 | 60 | 10 | 20 | – | 50.000 | 50.000 | 99.993 | 3 | 112 / 240 / 106 |
  | `import`, `template`, `computed` (nach dem Seed wie `api`; Größe am Jobende, `size-<job>.txt`) | `import` 44.054, `template` 34.034 bzw. 34.054, `computed` 34.054 | 10.000 | 10.000 | 10.000 | 30.000 vor dem Lauf | – | – | – | 3 | 128 bis 320 |

- **Ablauf je Job:** frische Datenbank, Migrationen, Seed, neu aufgebauter
  Suchindex, `ANALYZE`, Server mit 4 Spuren und den P2-Modulen, ein
  k6-Szenario (bei den Schreibjobs Vorbereitung, dann `writes.js` und
  `ingest.js` parallel), danach `tests/integrity-check.sql` und die
  Datenbankkosten aus `pg_stat_statements`. Integritätsprüfung nach jedem
  der 33 Jobs der Nachweisläufe und der 4 Jobs der Headroom-Läufe:
  „0 violations in 35 checks“. Fehlgeschlagene Anfragen: keine in jedem
  Job.
- **Jobs des Nachweises** (Jobsatz G2 von `evaluate-runs.sh`): `api`,
  `ingest`, `ingest-spool`, `ingest-rediscover`, `nfr01-volume`
  (G1-Regression gegen den erweiterten Seed, Budgetsatz G2 nach E-146),
  `phase2` (WP-822), `import`, `template`, `computed` (WP-823).
  `scenarios=all` startet zusätzlich `api-large-28000` und
  `api-large-50000` (WP-839); die Bewertung führt sie mit.
- **Lokaler Lauf (4 vCPU)** nach `tests/load/README.md` als Ergänzung wie in
  G1: **nicht durchgeführt** (Abschnitt „Offene Punkte“).

| Lauf | CPU (`lscpu`) | RAM (`MemTotal`) |
|---|---|---|
| Lauf 1 | Archiv steht aus | Archiv steht aus |
| Lauf 2 | Archiv steht aus | Archiv steht aus |
| Lauf 3 | Archiv steht aus | Archiv steht aus |

`lscpu` und `MemTotal` schreibt `load.yml` je Job nur nach
`run-<job>.txt` des Archivs, nicht ins Job-Log; belegt ist im Job-Log nur
das Runner-Image. Dass die Runner verschieden schnell sind, zeigen die Läufe
selbst: Bei gleichem Commit liegt etwa `relationships` im Job `api`
zwischen 420,9 und 898,3 ms, `computed_backfill_duration` zwischen 29 und
98 s.

## Läufe

Drei vollständige Läufe von `load.yml` per `workflow_dispatch` mit
`scenarios=all`, `objects` 10.000, `duration` 2 min und `budget_gate` G2
auf `main`, gleichzeitig gestartet am 2026-11-09 um 18:39 UTC, jeweils im
ersten Versuch. Der Job `archive` meldet in jedem Lauf „missing: 0, not
green: 0“ für die elf Jobs.

| Lauf | Lauf-ID | Commit (Lauf / Server) | `api` | `ingest` | `ingest-spool` | `ingest-rediscover` | `nfr01-volume` | `phase2` | `import` | `template` | `computed` | `api-large-28000` / `-50000` | Vollständig |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| L1 | [42675878596](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/42675878596) | `69e6668d` / `69e6668d` | grün | grün | grün | grün | grün | grün | grün | grün | grün | grün / grün | ja |
| L2 | [42675885412](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/42675885412) | `69e6668d` / `69e6668d` | grün | grün | grün | grün | grün | grün | grün | grün | grün | grün / grün | ja |
| L3 | [42675891357](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/42675891357) | `69e6668d` / `69e6668d` | grün | grün | grün | grün | grün | grün | grün | grün | grün | grün / grün | ja |

Archive (Artefakte der Läufe, in GitHub; nicht nach
`docs/acceptance/artefakte/lasttest/<Lauf-ID>/` übernommen, siehe „Quelle
der Werte“):

| Lauf | Artefakt | Artefakt-ID | Größe (Byte) | SHA-256 des Zip (Upload) |
|---|---|---:|---:|---|
| L1 | `load-archive-42675878596` | 13702840498 | 1.760.193 | `46ff037c72cf52fc9dc98f03a811e515c2d790165a6f160f4a736351ce43a35f` |
| L2 | `load-archive-42675885412` | 13702770859 | 2.684.402 | `7cf8a5baf8f61cf220e1343ed4c80bffaf376a017a17fd99c4d507dd3e799048` |
| L3 | `load-archive-42675891357` | 13701638764 | 2.171.352 | `72528a828309f7add8f8d266285c88de6ff2cadff69fda5bdffa4f1d6917a097` |

### Bewertung nach E-58 (Median über drei Läufe)

Ein Budget gilt als gehalten, wenn der Median seiner Messwerte aus den drei
Läufen im Budget liegt; ein Job ist verfehlt, wenn er in mindestens zwei
Läufen rot ist. In den Jobs `import`, `template` und `computed` gelten
zusätzlich die Schwellen des parallelen Ingest (`<job>-ingest.json`).
Bewertet mit `load-evidence.yml` (Eingabe `gate` = `G2`).

- Lauf von `load-evidence.yml`:
  [42683419968](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/42683419968)
  (`workflow_dispatch` auf `main`, `run_1` = 42675878596, `run_2` =
  42675885412, `run_3` = 42675891357, `gate` = `G2`), grün,
  `evaluate-runs.sh exit code: 0`.
- Voraussetzungen: „Voraussetzungen erfüllt“ (derselbe Server-Commit
  `69e6668d93ef85d0e28d5eb271672b81a167a7dc` in allen drei Läufen, jeder der
  neun Jobs in jedem Lauf); Latenz- und Dauerbudgets aus Satz G2 von
  `tests/load/budgets.json`.
- Artefakt `load-evidence-42683419968` (`bewertung.md`, Artefakt-ID
  13703333074).
- Letzte Zeile der Bewertung: „Bewertung nach E-58 (Median über drei
  Läufe): alle Budgets gehalten“.

| Job | rot in Läufen | Urteil | Budgets im Median |
|---|---|---|---|
| `api` | 0 von 3 | **gehalten** | 36 von 36 |
| `ingest` | 0 von 3 | **gehalten** | 9 von 9 |
| `ingest-spool` | 0 von 3 | **gehalten** | 8 von 8 |
| `ingest-rediscover` | 0 von 3 | **gehalten** | 10 von 10 |
| `nfr01-volume` | 0 von 3 | **gehalten** | 36 von 36 |
| `phase2` | 0 von 3 | **gehalten** | 30 von 30 |
| `import` (mit parallelem Ingest) | 0 von 3 | **gehalten** | 13 von 13 |
| `template` (mit parallelem Ingest) | 0 von 3 | **gehalten** | 13 von 13 |
| `computed` (mit parallelem Ingest) | 0 von 3 | **gehalten** | 13 von 13 |
| `api-large-28000`, `api-large-50000` | 0 von 3 | **gehalten** | 24 von 24 bzw. 21 von 21 (nur `list`, `list_sorted`, `filter` mit Latenzschwelle bzw. keine; Fehlerschwellen) |

## NFR-02: p95 je Budget

Werte in ms, Bulk in s; Einzelwerte und Median aus der Bewertung von
`load-evidence.yml` (Lauf 42683419968). Schwelle G1: Budget aus NFR-02
(Satz G1 von `tests/load/budgets.json`). Schwelle G2: vorläufige Schwelle
nach E-146 (Satz G2, Standard von `load.yml` und `evaluate-runs.sh`);
Herleitung (Messwert, Lauf-ID) in `docs/plan/phase2/entscheidungen.md`,
E-146. Spalte „G1“: Median innerhalb der G1-Schwelle (ja/**nein**); fett:
gegen G1 verfehlt. Gegen G2 ist jedes Budget gehalten.

### G1-Regression (`api.js`, Job `api`, 10.000 CIs mit erweitertem Seed)

| Budget (NFR-02) | k6-Szenario (`name`) | Schwelle G1 | Schwelle G2 | L1 | L2 | L3 | Median | G1 |
|---|---|---:|---:|---:|---:|---:|---:|---|
| Listen | `list` | < 500 | < 1.500 | 459,1 | 410,8 | 640,2 | 459,1 | ja |
| Listen (sortiert nach Name, E-104) | `list_sorted` | < 500 | < 1.500 | 460,0 | 409,1 | 656,6 | 460,0 | ja |
| Suche (Namensfilter der Liste) | `list_name_filter` | < 500 | < 1.500 | 576,3 | 542,6 | 856,0 | **576,3** | **nein** |
| Suche (globale Suche der UI) | `global_search` | < 500 | < 2.500 | 1.168,7 | 1.108,6 | 1.609,2 | **1.168,7** | **nein** |
| Indizierte Attributfilter | `filter` | < 500 | < 900 | 285,7 | 237,7 | 414,4 | 285,7 | ja |
| Attributfilter (JSONB `cost_center`) | `attr_filter` | < 500 | < 800 | 314,9 | 290,4 | 480,7 | 314,9 | ja |
| Beziehungen eines CI | `relationships` | < 500 | < 1.500 | 463,0 | 420,9 | 898,3 | 463,0 | ja |
| CI-Detail | `detail` | < 300 | < 800 | 286,4 | 229,0 | 411,7 | 286,4 | ja |
| Rack-Layout | `rack` | < 300 | < 600 | 238,2 | 197,3 | 351,5 | 238,2 | ja |
| Topologie 2.000 Knoten | `topology` | < 3.000 | < 3.500 | 1.384,4 | 1.452,8 | 1.885,1 | 1.452,8 | ja |
| Impact Tiefe 20 (kompakt) | `impact` | < 2.000 | < 4.500 | 1.493,7 | 1.399,0 | 2.086,1 | 1.493,7 | ja |
| Impact Tiefe 20 (Standardform) | `impact_full` | < 2.000 | < 5.000 | 1.635,3 | 1.453,9 | 2.250,5 | 1.635,3 | ja |
| Blast Radius | `blast_radius` | < 2.000 | < 5.000 | 1.564,4 | 1.384,2 | 2.212,6 | 1.564,4 | ja |
| Bulk 500 IDs (Status) | `bulk_500_duration` | < 30 s | < 95 s | 57,46 | 49,82 | 58,59 | **57,46** | **nein** |
| Bulk 500 IDs (`set_fields`) | `bulk_fields_500_duration` | < 30 s | < 115 s | 61,73 | 53,68 | 72,95 | **61,73** | **nein** |
| Checks / fehlgeschlagene Anfragen | `checks`, `http_req_failed` | > 99 % / 0 | > 99 % / 0 | 100 % / 0 | 100 % / 0 | 100 % / 0 | 100 % / 0 | ja |

Jeder der drei Bulk-Jobs je Art gelingt in jedem Lauf
(`bulk_jobs_succeeded`, `bulk_fields_jobs_succeeded` je 3). Gegenüber den
G1-Nachweisläufen N1 bis N3 (`3910aae`, Median `api`: `list` 166,5,
`list_name_filter` 299,5, `global_search` 349,9, `topology` 766,0, `impact`
1.148,2, `bulk_500` 15,14 s) sind alle Pfade langsamer; der Seed ist um
Assets, Tickets, Mengenartikel und Feldwerte erweitert, und die Runner
streuen stark (L3 liegt in jedem Szenario über L1 und L2).

### G1-Regression auf den Listen über 10.000 Objekten (Job `api-large-28000`, 28.000 CIs)

`LATENCY_BUDGETS` = `lists`: Latenzschwellen nur für `list`,
`list_sorted` und `filter` (WP-839); nach E-17 gelten die Budgets bei 10.000
Objekten, die Werte über 10.000 zeigen die Reserve.

| Budget | k6-Szenario | Schwelle G1 | Schwelle G2 | L1 | L2 | L3 | Median | G1 |
|---|---|---:|---:|---:|---:|---:|---:|---|
| Listen | `list` | < 500 | < 1.500 | 680,5 | 551,7 | 636,7 | **636,7** | **nein** |
| Listen (sortiert nach Name, `total` gedeckelt nach E-104) | `list_sorted` | < 500 | < 1.500 | 662,7 | 586,7 | 614,5 | **614,5** | **nein** |
| Indizierte Attributfilter | `filter` | < 500 | < 900 | 403,8 | 314,6 | 361,5 | 361,5 | ja |

`api-large-50000` (50.000 CIs, `LATENCY_BUDGETS` = `none`): keine
Latenzschwelle; `http_req_failed` 0 und `checks` 100 % in allen drei Läufen,
die Bulk-Jobs gelingen (3 von 3). k6 exportiert ohne Schwelle keinen p95 je
Szenario; Messwerte je Liste über 10.000 Objekten liefern die
Headroom-Läufe.

### Phase 2 (`phase2.js`, Job `phase2`)

| Budget (NFR-02) | k6-Szenario (`name`) | Schwelle G1 | Schwelle G2 | L1 | L2 | L3 | Median | G1 |
|---|---|---:|---:|---:|---:|---:|---:|---|
| Listen (Asset-Liste mit `total`, K23) | `asset_list` | < 500 | < 700 | 536,2 | 299,1 | 526,1 | **526,1** | **nein** |
| Listen (Asset-Liste mit Feldrechten von viewer) | `asset_list_restricted` | < 500 | < 700 | 502,9 | 310,2 | 510,4 | **502,9** | **nein** |
| Listen (Ticketliste mit `total`) | `ticket_list` | < 500 | < 1.500 | 973,8 | 568,2 | 878,2 | **878,2** | **nein** |
| Listen (Mengenartikel mit `total`) | `consumable_list` | < 500 | < 500 | 251,2 | 150,8 | 266,3 | 251,2 | ja |
| Suche (Felder aus WP-308) | `search_fields` | < 500 | < 2.000 | 1.512,9 | 895,0 | 1.423,7 | **1.423,7** | **nein** |
| Attributfilter (Bereich `cost_center`, `warranty_days_left`) | `ci_attr_filter` | < 500 | < 800 | 581,2 | 354,9 | 563,2 | **563,2** | **nein** |
| Tag-Filter der CI-Liste | `ci_tag_filter` | < 500 | < 700 | 480,1 | 281,9 | 487,9 | 480,1 | ja |
| Filter auf Asset-Felder (`POST /search/query`) | `ast_asset_filter` | < 500 | < 800 | 511,9 | 299,5 | 535,4 | **511,9** | **nein** |
| Systemsichten (WP-307) | `view_presets` | < 500 | < 3.000 | 2.499,5 | 1.312,3 | 2.476,1 | **2.476,1** | **nein** |
| Beziehungssuche (`upstream_of`/`downstream_of`, Tiefe 5) | `relation_search` | < 500 | < 1.500 | 1.434,5 | 740,5 | 1.323,1 | **1.323,1** | **nein** |
| Bulk 500 IDs (Assets, `set_fields`) | `bulk_assets_500_duration` | < 30 s | < 30 s | 9,76 | 8,23 | 16,01 | 9,76 | ja |
| Bulk 500 IDs (CIs, `add_tags`) | `bulk_cis_ops_500_duration` | < 30 s | < 40 s | 26,63 | 27,05 | 32,80 | 27,05 | ja |
| Checks / fehlgeschlagene Anfragen | `checks`, `http_req_failed` | > 99 % / 0 | > 99 % / 0 | 100 % / 0 | 100 % / 0 | 100 % / 0 | 100 % / 0 | ja |

Die Bulk-Jobs gelingen in jedem Lauf (`bulk_assets_jobs_succeeded`,
`bulk_cis_ops_jobs_succeeded` je 3; `checks{scenario:bulk_*}` 100 %).
`asset_list_restricted` hält G1 in L2 und verfehlt es knapp im Median
(502,9 ms).

### Massen-Schreibjobs (`writes.js`, Messgrößen ohne Budget)

Werte aus den k6-Zusammenfassungen der Job-Logs (k6 zeigt über einer Minute
ganze Sekunden).

| Job | Messgröße | Ziel | L1 | L2 | L3 | Median |
|---|---|---|---:|---:|---:|---:|
| `import` | `import_10000_duration` | < 5 min (Ziel, keine Schwelle) | 4 min 0 s | 2 min 51 s | 4 min 9 s | 4 min 0 s |
| `import` | `import_update_duration` (zweiter Lauf ohne manuelles `ANALYZE`) | – | 4 min 8 s | 2 min 55 s | 4 min 23 s | 4 min 8 s |
| `template` | `template_migrate_duration` | – | 3 min 59 s | 4 min 7 s | 3 min 0 s | 3 min 59 s |
| `template` | `template_migrated` | – | 9.996 | 9.996 | 9.996 | 9.996 |
| `computed` | `computed_backfill_duration` | – | 1 min 38 s | 28,96 s | 36,26 s | 36,26 s |

Schwellen der Schreibjobs, im Median gehalten (Bewertung oben):
`import_jobs_succeeded` `count==4` (4 / 4 / 4), `template_jobs_succeeded`
und `computed_jobs_succeeded` `count==1` (je 1 / 1 / 1), `checks`
`rate==1`, `http_req_failed` `rate==0` (je Lauf und Szenario). Der Import
von 10.000 Zeilen bleibt in jedem Lauf unter dem Ziel von 5 min.

## NFR-03

Schwellen aus `ingest.js`: `ingest_visible_delay` `max<30000`,
`ingest_records_missing` `count==0`, `http_req_duration{name:ingest}`
`p(95)<1000`, `dropped_iterations` `count==0`, `checks` `rate>0.99`,
`http_req_failed` `rate==0`; `spool` zusätzlich `ingest_spool_flush`
`max<300000` und `ingest_drain_records_per_s` `value>=200`;
`rediscover` zusätzlich `ingest_new_cis` `count==0`. Verzögerung als
Maximum je Lauf, Spalten L1 / L2 / L3 → Median.

| Job | Szenario | Verzögerung max (s) | fehlend | weitere Schwellen (Median) | Median gehalten |
|---|---|---|---:|---|---|
| `ingest` | `load`, 200 Records/s | 1,77 / 3,24 / 2,24 → 2,24 | 0 | `POST /ingest/bulk` p95 27,1 ms; 0 verworfene Iterationen | ja (9 von 9) |
| `ingest-spool` | `spool`, 10.000 Records | – | 0 | Flush 32,16 / 29,11 / 15,00 → 29,11 s; Abbau 331,4 / 348,3 / 690,4 → 348,3 Records/s; p95 72,2 ms | ja (8 von 8) |
| `ingest-rediscover` | `rediscover`, 200 Records/s | 19,93 / 23,65 / 8,80 → 19,93 | 0 | neue CIs 0; p95 30,9 ms | ja (10 von 10) |
| `import` | paralleler Ingest (`import-ingest.json`, 120 s) | 2,42 / 1,80 / 2,51 → 2,42 | 0 | p95 26,2 ms; 24.020 Records angenommen je Lauf | ja |
| `template` | paralleler Ingest (`template-ingest.json`, 120 s) | 4,32 / 3,88 / 3,60 → 3,88 | 0 | p95 42,0 ms; 24.000 bis 24.020 Records | ja |
| `computed` | paralleler Ingest (`computed-ingest.json`, 120 s) | 3,24 / 2,88 / 2,35 → 2,88 | 0 | p95 37,9 ms; 24.020 Records | ja |

Fingerprint-MACs (WP-434, `ci_identity_mac`, 000161): auf dem
Phase-2-Stand bestätigt. `ingest`, `ingest-spool` und `ingest-rediscover`
halten jede Schwelle in jedem Lauf; die Wiederentdeckung legt kein neues CI
an (`ingest_new_cis` 0), die Identitätssuche findet also jedes Gerät
wieder. Die Reserve der Wiederentdeckung ist gegenüber G1 kleiner: Median
der Verzögerung 19,93 s (bis 23,65 s) gegen 2,35 s in den
G1-Nachweisläufen N1 bis N3 (Abschnitt „Offene Punkte“).

Max-Drain gegenüber 1.000 Records/s [P5]: Abbau ab Sendeende im Szenario
`spool` 331,4 bis 690,4 Records/s, Median 348,3 Records/s; kein G2-Budget
(Schwelle 200 Records/s gehalten). Die 1.000 Records/s der Phase 5 erreicht
der Ingest auf 4 CPUs nicht (G1, Offener Punkt 2).

## NFR-01-Volumina

Job `nfr01-volume`: `api.js` mit den Schwellen des Jobs `api` (Budgetsatz
G2 nach E-146, G1 mit `--gate G1` auswertbar) auf 10.000 CIs mit 50.000
Interfaces, 200.000 Adressen, 100.000 Beziehungen und 500 Organisationen,
dazu die Phase-2-Mengen der Eingaben. Ersetzt die Headroom-Läufe nicht.
Der Schritt „Seeded volumes (NFR-01)“ prüft die Mengen und war in jedem
Lauf grün.

| Kenngröße | Soll | L1 | L2 | L3 |
|---|---:|---:|---:|---:|
| CIs der Demo-Organisation | 10.000 | 10.010 | 10.010 | 10.010 |
| Interfaces | 50.000 | 50.000 | 50.000 | 50.000 |
| Adressen | 200.000 | 200.000 | 200.000 | 200.000 |
| Beziehungen | 100.000 | 100.000 | 100.000 | 100.000 |
| Organisationen | 500 | 503 | 503 | 503 |
| Assets / Tickets / Mengenartikel | je 10.000 | je 10.000 | je 10.000 | je 10.000 |

Budgets im Median (Schwellen wie Job `api`): **gehalten**, 36 von 36 gegen
G2, rot in 0 von 3 Läufen. p95 in ms (Bulk in s), L1 / L2 / L3 → Median:

| k6-Szenario | Schwelle G1 | Schwelle G2 | Werte | Median | G1 |
|---|---:|---:|---|---:|---|
| `list` | < 500 | < 1.500 | 407,7 / 518,2 / 447,9 | 447,9 | ja |
| `list_sorted` | < 500 | < 1.500 | 293,5 / 395,5 / 458,5 | 395,5 | ja |
| `list_name_filter` | < 500 | < 1.500 | 347,1 / 429,9 / 532,7 | 429,9 | ja |
| `global_search` | < 500 | < 2.500 | 767,2 / 899,3 / 1.132,1 | **899,3** | **nein** |
| `filter` | < 500 | < 900 | 181,7 / 232,1 / 288,9 | 232,1 | ja |
| `attr_filter` | < 500 | < 800 | 204,8 / 284,4 / 337,8 | 284,4 | ja |
| `relationships` | < 500 | < 1.500 | 263,6 / 318,2 / 413,2 | 318,2 | ja |
| `detail` | < 300 | < 800 | 178,1 / 234,2 / 283,0 | 234,2 | ja |
| `rack` | < 300 | < 600 | 147,8 / 194,3 / 235,4 | 194,3 | ja |
| `topology` | < 3.000 | < 3.500 | 982,3 / 1.211,6 / 1.411,1 | 1.211,6 | ja |
| `impact` | < 2.000 | < 4.500 | 1.472,9 / 1.759,1 / 2.288,7 | 1.759,1 | ja |
| `impact_full` | < 2.000 | < 5.000 | 1.589,9 / 1.754,7 / 2.268,2 | 1.754,7 | ja |
| `blast_radius` | < 2.000 | < 5.000 | 1.583,6 / 1.831,7 / 2.169,3 | 1.831,7 | ja |
| `bulk_500_duration` | < 30 s | < 95 s | 30,89 / 27,40 / 46,32 | **30,89** | **nein** |
| `bulk_fields_500_duration` | < 30 s | < 115 s | 30,93 / 30,54 / 43,21 | **30,93** | **nein** |
| `checks` / `http_req_failed` | > 99 % / 0 | > 99 % / 0 | 100 % / 0 in jedem Lauf | 100 % / 0 | ja |

## ABN-02

ABN-02 [Q]: „Performance-Sanity gegen NFR-02 für Listen, Suche,
Attributfilter, Beziehungssuche, Topologie, Impact, Bulk; Prüfung auf N+1
und fehlende Indizes.“ Jeder Teil hat ein k6-Szenario (`name`-Tag,
Schwelle; p95 aus den Läufen oben) und, wo vorhanden, einen
Query-Budget-Test (Statementzahl, Plan). Die Tests laufen bei jedem PR gegen
PostgreSQL (`ci.yml`); Zeitgrenzen nur mit `RETICORA_PERF_BUDGET=1` auf dem
4-vCPU-Host.

**Ergebnis auf dem Release-Kandidaten:** `ci.yml` auf `69e6668d` (Lauf
[42675826333](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/42675826333),
Job `migrations` 129356795765, PostgreSQL mit TimescaleDB, ohne
`RETICORA_PERF_BUDGET`) ist grün; jeder Query-Budget-Test der Tabelle unten
ist bestanden (`PASS`), keiner übersprungen (kein `SKIP` in den Paketen
`ci`, `asset`, `ticket`, `consumable`, `savedview`, `graphqlbff`,
`relationship`, `search`, `topology`, `rack`, `discovery`): `TestCIQueryBudget`,
`TestCIListBudget`, `TestCINodeReadBudget`, `TestAssetQueryBudget`,
`TestTicketQueryBudget`, `TestConsumableListQueryBudget`,
`TestSavedViewQueryBudget`, `TestWarrantyDaysLeftIndexKeepsResults`,
`TestAssetAggregateQueryBudget`, `TestRelationshipListPlanBudget`,
`TestRestrictedSearchQueryBudget`, `TestRestrictedSearchFieldRowBudget`,
`TestReindexFieldRowsQueryBudget`, `TestTopologyQueryBudget`,
`TestImpactWorstCase`, `TestImpactLeanAgainstPostgres`,
`TestMultiImpactAgainstPostgres`, `TestServiceImpactWorstCase`,
`TestRackLayoutAgainstPostgres`, `TestIngestStatementBudget`. Die Tests
protokollieren ihre Zeiten mit der Grenze „budget 500ms with
RETICORA_PERF_BUDGET=1“ (z. B. Asset-Liste mit 200 Zeilen bestes von fünf
7,1 ms). Ein Lauf mit `RETICORA_PERF_BUDGET=1` auf dem 4-vCPU-Host steht aus
(Abschnitt „Offene Punkte“).

### Zuordnung

Spalte „Schwelle (p95)“: G1 (NFR-02) / G2 (E-146); ein einzelner Wert gilt in
beiden Budgetsätzen. Spalte „Median“: Median der drei Nachweisläufe aus dem
Abschnitt „NFR-02“ (Job `api` bzw. `phase2`).

| Teil von ABN-02 / NFR-02 | k6-Szenario (`name`-Tag, Skript) | Schwelle (p95) G1 / G2 (E-146) | Median | Query-Budget-Test (Statementzahl, Plan) |
|---|---|---:|---:|---|
| Listen: CI-Liste | `list`, `list_sorted` (`api.js`) | < 500 ms / < 1.500 ms | 459,1 / 460,0 ms | `TestCIQueryBudget` Fall `list` (höchstens 6 Statements für 10 und 200 Zeilen); `TestCIListBudget` (feste Statementzahl, Seite über `idx_ci_list_sort` ohne Sortierung der Organisation) |
| Listen: Asset-Liste, auch mit Feldrechte-Projektion | `asset_list`, `asset_list_restricted` (`phase2.js`) | < 500 ms / < 700 ms | **526,1** / **502,9** ms | `TestAssetQueryBudget` (Liste höchstens 5 Statements, über den Handler mit Projektion `listBudget + 11`; Standardseite über `idx_asset_list_sort`, 000278) |
| Listen: Ticketliste | `ticket_list` (`phase2.js`) | < 500 ms / < 1.500 ms | **878,2** ms | `TestTicketQueryBudget` (höchstens 5 Statements; Standardseite über `idx_ticket_list_sort`, 000277; Sichtbarkeit über `ci_pkey`/`ci_id_organization_key` und `asset_pkey`/`asset_id_organization_key`) |
| Listen: Mengenartikel | `consumable_list` (`phase2.js`) | < 500 ms | 251,2 ms | `TestConsumableListQueryBudget` (Seite über `idx_consumable_list_name`, 000279) |
| Suche: Namensfilter der CI-Liste | `list_name_filter` (`api.js`) | < 500 ms / < 1.500 ms | **576,3** ms | `TestCIQueryBudget` Fälle `name filter (search=)`, `name filter (filter[name]=)` |
| Suche: globale Suche | `global_search` (`api.js`), `search_fields` (`phase2.js`) | < 500 ms / `global_search` < 2.500 ms, `search_fields` < 2.000 ms | **1.168,7** / **1.423,7** ms | `TestRestrictedSearchQueryBudget`, `TestRestrictedSearchFieldRowBudget` (eingeschränkte Suche, Zusatzfeldzeilen einmal je Suche), `TestReindexFieldRowsQueryBudget`; Volltext nach E-133 (Option 3) als dokumentierter linearer Scan ohne Planprüfung |
| Attributfilter: indizierte Filter | `filter` (`api.js`) | < 500 ms / < 900 ms | 285,7 ms | `TestCIQueryBudget` Fall `hostname filter of the list (filter[hostname]=)` (`idx_ci_hostname_lc`) |
| Attributfilter: JSONB-Gleichheit | `attr_filter` (`api.js`) | < 500 ms / < 800 ms | 314,9 ms | `TestCIQueryBudget` Fall `attribute filter` (Index je Schlüssel, E-73 (Korrekturplan), 000162/000164) |
| Attributfilter: Bereich und `warranty_days_left` | `ci_attr_filter` (`phase2.js`) | < 500 ms / < 800 ms | **563,2** ms | `TestSavedViewQueryBudget` (`warranty_days_left` über `idx_ci_warranty_days_left`, 000274), `TestWarrantyDaysLeftIndexKeepsResults`; Bewertung unten |
| Attributfilter: Tags | `ci_tag_filter` (`phase2.js`) | < 500 ms / < 700 ms | 480,1 ms | `TestCIQueryBudget` Fall `tag filter (filter[tags][in])` (`idx_ci_tag_org_tag`) |
| Attributfilter: Asset-Felder und Systemsichten | `ast_asset_filter`, `view_presets` (`phase2.js`) | < 500 ms / `ast_asset_filter` < 800 ms, `view_presets` < 3.000 ms | **511,9** / **2.476,1** ms | `TestSavedViewQueryBudget` (Filter-AST mit Asset-Feldern auf Assets und auf CIs über das verknüpfte Asset, höchstens 4 Statements für 10 und 200 Zeilen; Systemsicht `servers_without_monitoring`) |
| Beziehungssuche | `relation_search` (`phase2.js`); Beziehungen eines CI `relationships` (`api.js`) | < 500 ms / < 1.500 ms | **1.323,1** / 463,0 ms | `TestSavedViewQueryBudget` (`upstream_of` über `idx_ci_rel_org_target`, `downstream_of` über `idx_ci_rel_org_source`/`uq_ci_relationship_edge`, Tiefe 5); `TestRelationshipListPlanBudget` |
| Topologie | `topology` (`api.js`) | < 3.000 ms / < 3.500 ms | 1.452,8 ms | `TestTopologyQueryBudget` (höchstens 40 Statements, unabhängig von der Graphgröße; `seqScan`-Prüfung) |
| Impact, kompakte Form | `impact` (`api.js`) | < 2.000 ms / < 4.500 ms | 1.493,7 ms | `TestImpactWorstCase` (höchstens 250 Byte je betroffenem CI, Statementzahl wie bei 3.000 Objekten, Override-Abgleich über `idx_ci_field_value_override`), `TestImpactLeanAgainstPostgres` |
| Impact, Standardform | `impact_full` (`api.js`) | < 2.000 ms / < 5.000 ms | 1.635,3 ms | `TestImpactWorstCase` (Pfade aus `via`/`relay`); Mehrfach-Impact `TestMultiImpactAgainstPostgres` |
| Impact, Blast Radius | `blast_radius` (`api.js`) | < 2.000 ms / < 5.000 ms | 1.564,4 ms | `TestTopologyQueryBudget` (Fall `blast-radius`), `TestServiceImpactWorstCase` |
| Bulk | `bulk_500_duration`, `bulk_fields_500_duration` (`api.js`); `bulk_assets_500_duration`, `bulk_cis_ops_500_duration` (`phase2.js`) | < 30 s / `bulk_500_duration` < 95 s, `bulk_fields_500_duration` < 115 s, `bulk_assets_500_duration` < 30 s, `bulk_cis_ops_500_duration` < 40 s | **57,46** / **61,73** / 9,76 / 27,05 s | kein Query-Budget-Test; nur k6 (jeder Bulk-Job muss gelingen: `*_jobs_succeeded` `count==3`, in jedem Lauf erfüllt) |
| Massen-Schreibjobs neben dem Ingest | `import`, `template`, `computed` (`writes.js`) | ohne Budget (Messgrößen) | Abschnitt „Massen-Schreibjobs“ | `TestAssetQueryBudget` Fälle `import duplicate search by inventory number` (`idx_asset_tag_org`) und `import duplicate search by normalized serial number` (`idx_asset_org_serial_norm`); Ingest `TestIngestStatementBudget` |
| GraphQL-Aggregat (N+1) | – | – | – | `TestAssetAggregateQueryBudget` (`asset(id) { cis children movements documents }`, gleiche Statementzahl für 10 und 100) |
| NFR-02 CI-Detail | `detail` (`api.js`) | < 300 ms / < 800 ms | 286,4 ms | `TestCIQueryBudget` Fall `detail` (höchstens 5 Statements, `ci_pkey`/`ci_id_organization_key`) |
| NFR-02 Rack-Layout | `rack` (`api.js`) | < 300 ms / < 600 ms | 238,2 ms | kein Query-Budget-Test; Funktion `TestRackLayoutAgainstPostgres`; Messung nur k6 |

**Plan von `warranty_days_left` zusammen mit `ci_attr_filter`:**
`TestSavedViewQueryBudget` belegt auf `69e6668d` per `EXPLAIN ANALYZE` als
App-Rolle unter RLS: „attribute filter warranty_days_left <= 30 (count):
100 rows read: Aggregate > Result > Index Scan using
idx_ci_warranty_days_left on ci“, ohne den Index „10000 rows read: … Seq
Scan on ci“. Der Filter ist also indexfähig und liest nur die Treffer. Der
p95 von `ci_attr_filter` (Median 563,2 ms, G2 < 800 ms gehalten, G1
verfehlt) entsteht damit nicht im Filter auf `warranty_days_left`, sondern
in Zählung, Seite und Projektion der CI-Liste unter Last (dieselbe
Größenordnung wie `list` mit 459,1 ms und `ci_tag_filter` mit 480,1 ms im
selben Lauf); Folge-WP-Vorschlag im Abschnitt „Offene Punkte“.

N+1 und fehlende Indizes: Jeder Query-Budget-Test oben misst die
Statementzahl über einen pgx-Tracer an einer Kopie des App-Pools und prüft
den Plan per `EXPLAIN (ANALYZE, FORMAT JSON)` als App-Rolle unter RLS
(Helfer `backend/internal/database/scopetest/querybudget.go`, WP-824). Alle
bestehen auf dem Release-Kandidaten ohne `t.Skip`. Das Statement-Profil des
Jobs `api` (L1, `pg_stat_statements`) zeigt kein N+1-Muster; die teuersten
Anweisungen sind der Namensfilter der CI-Liste (`name ILIKE … OR hostname
ILIKE …`, 982 Aufrufe, bis 495 ms), die Advisory-Sperre der Bulk-Schreiber
(`pg_advisory_xact_lock`, 441 Aufrufe, zusammen 169 s, bis 2,2 s), der
Kantenwalk von Topologie und Impact (611 Aufrufe, bis 508 ms) und die
gedeckelte Zählung der CI-Liste (`SELECT COUNT(*) FROM (… LIMIT 10001)`,
741 Aufrufe, zusammen 110 s).

## Headroom (E-17, ohne Budgetzusage)

Jobs `api` und `phase2` einzeln per `workflow_dispatch` mit
`scenarios=api,phase2` (Aufruf in `tests/load/README.md`, Abschnitt
„Headroom nach E-17“) auf demselben Server-Commit `69e6668d`, gestartet mit
den Nachweisläufen. Grenze des Seeds: `objects + assets` ≤ 50.000.
Verfehlte Schwellen sind hier Messwerte, kein Gate; der Lauf bei 50.000
Objekten ist deshalb in GitHub rot (k6 Exit 99 in `api` und `phase2`, nur
durch p95-Schwellen von G2), ohne dass eine Anfrage fehlschlägt.

| Lauf | Lauf-ID | `objects` | `assets` | `tickets` | `consumables` | Objekte (CI + Asset) | Ergebnis in GitHub |
|---|---|---:|---:|---:|---:|---:|---|
| Headroom 25.000 | [42675897697](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/42675897697) | 12.500 | 12.500 | 10.000 | 10.000 | 25.000 | grün (beide Jobs) |
| Headroom 50.000 | [42675902852](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/42675902852) | 25.000 | 25.000 | 10.000 | 10.000 | 50.000 | rot (Messwerte über G2, siehe unten) |

Datenbestand nach dem Seed (Demo-Organisation): 12.510 CIs, 12.500 Assets,
12.500 Interfaces und Adressen, 24.993 Beziehungen bzw. 25.010 CIs, 25.000
Assets, 25.000 Interfaces und Adressen, 49.993 Beziehungen; je 10.000
Tickets und Mengenartikel. Archive `load-archive-42675897697` (Artefakt-ID
13702932153, SHA-256 des Zip
`017d158e07b8a11525c19d4feda119d17e4199d7f8b1d5be0c8b80d3f9be693d`) und
`load-archive-42675902852` (Artefakt-ID 13703710931, SHA-256
`9db67034d46032aeebfd7dd7b9d9738a787071f2b39cc5faacbdfe04a8ace6be`), in
GitHub; Werte aus den k6-Zusammenfassungen der Job-Logs.

p95 in ms (Bulk in s); in Klammern der Median der Nachweisläufe bei 10.000
Objekten zum Vergleich. Einzelläufe auf je eigenem Runner: Der Runner des
Laufs bei 25.000 Objekten war schneller als die Runner der Nachweisläufe,
die Werte bei 25.000 liegen deshalb teils unter denen bei 10.000.

| Messgröße | Szenario | 25.000 Objekte | 50.000 Objekte | Schwelle G2 / G1 |
|---|---|---:|---:|---|
| `GET /api/v1/cis` p95 (`total` nach E-104, gedeckelt) | `list` | 326,6 | 890,4 | < 1.500 / < 500 |
| `GET /api/v1/cis?sort_by=name` p95 (Check „list sorted by name has total after E-104“ bestanden) | `list_sorted` | 336,1 | 867,7 | < 1.500 / < 500 |
| `GET /api/v1/cis` Namensfilter | `list_name_filter` | 413,8 | **1.780** (über G2) | < 1.500 / < 500 |
| `GET /api/v1/search` p95 (`total` nach E-104) | `global_search` | 766,0 | **2.810** (über G2) | < 2.500 / < 500 |
| Suche in Feldern (WP-308) | `search_fields` | 1.550 | **2.660** (über G2) | < 2.000 / < 500 |
| `GET /api/v1/assets` p95 (Zählung je Anfrage) | `asset_list` | 503,8 | **1.070** (über G2) | < 700 / < 500 |
| `GET /api/v1/assets` mit Feldrechten (viewer) | `asset_list_restricted` | 531,4 | **1.090** (über G2) | < 700 / < 500 |
| `GET /api/v1/tickets` p95 (Zählung je Anfrage) | `ticket_list` | 1.000 | **1.830** (über G2) | < 1.500 / < 500 |
| `GET /api/v1/consumables` p95 (Zählung je Anfrage) | `consumable_list` | 228,1 | 391,0 | < 500 / < 500 |
| Attributfilter, Tags, Asset-Felder | `ci_attr_filter`, `ci_tag_filter`, `ast_asset_filter` | 584,2 / 454,6 / 487,9 | **1.200** / **734,6** / **929,2** (über G2) | < 800 / < 700 / < 800 |
| Systemsichten, Beziehungssuche | `view_presets`, `relation_search` | 2.700 / 1.360 | **6.760** / **4.910** (über G2) | < 3.000 / < 1.500 |
| `ListNodes` (Topologie, Impact) | `topology`, `impact`, `impact_full`, `blast_radius` | 944,2 / 1.060 / 1.080 / 1.050 | 3.310 / 3.910 / 4.110 / 4.400 | < 3.500 / 4.500 / 5.000 / 5.000 (G1 3.000 / 2.000) |
| Detail, Rack, Filter, Beziehungen eines CI | `detail`, `rack`, `filter`, `relationships` | 150,1 / 131,5 / 153,6 / 354,4 | 541,3 / 433,8 / 564,5 / 1.050 | < 800 / 600 / 900 / 1.500 |
| Bulk 500 IDs (Status, `set_fields`) | `bulk_500_duration`, `bulk_fields_500_duration` | 26,17 / 28,68 s | 52,59 / 45,70 s | < 95 / 115 s |
| Bulk 500 IDs (Assets, CIs) | `bulk_assets_500_duration`, `bulk_cis_ops_500_duration` | 9,39 / 25,39 s | 12,33 / 31,98 s | < 30 / 40 s |
| Fehlerfreiheit | `http_req_failed`, `checks` | 0 von 13.756 bzw. 7.525 Anfragen; Checks 100 % (15.948 bzw. 13.486) | 0 von 6.844 bzw. 4.811 Anfragen; Checks 100 % (7.405 bzw. 8.341) | 0 / > 99 % |

Bei 50.000 Objekten ist jede Funktion fehlerfrei (E-17: funktionsfähig ohne
Budgetzusage); jeder Bulk-Job gelingt, jede inhaltliche Prüfung besteht,
auch Impact „covers the organization“ und „<= 250 B per impacted CI“ und
die Form von `total` nach E-104. Über den G2-Schwellen liegen in `api`
`global_search` und `list_name_filter`, in `phase2` neun der zehn
Lesepfade (alle außer `consumable_list`). Die Zählung je Anfrage der
Asset- und Ticketliste wächst mit der Organisation (`asset_list` 526 →
1.070 ms, `ticket_list` 878 → 1.830 ms von 10.000 auf 50.000 Objekte bei
gleich vielen Tickets), die Mengenartikel-Liste bleibt unter 500 ms.
Topologie und Impact bleiben bei 50.000 Objekten unter den G2-Schwellen,
überschreiten aber die G1-Budgets (Kappung des Walks bei 10.000 Knoten,
WP-840, `truncated: true`).

## Bewertung für G2

- **NFR-02:** **nachgewiesen gegen den vorläufigen Budgetsatz G2**
  (E-146): Median der drei Läufe hält jedes Budget der G1-Regression
  (`api`, `nfr01-volume`, `api-large-28000`) und jedes Phase-2-Budget
  (`phase2`), keine fehlgeschlagene Anfrage. Gegen die Budgets von NFR-02
  (Satz G1) verfehlt im Median: `list_name_filter`, `global_search`,
  `bulk_500_duration`, `bulk_fields_500_duration` (Job `api`;
  `global_search` und beide Bulk-Dauern auch in `nfr01-volume`), `list`
  und `list_sorted` bei 28.000 CIs, `asset_list`, `asset_list_restricted`,
  `ticket_list`, `search_fields`, `ci_attr_filter`, `ast_asset_filter`,
  `view_presets` und `relation_search` (Job `phase2`). Die Abweichung ist
  durch E-146 vorläufig hingenommen; Folge-WP-Vorschläge im Abschnitt
  „Offene Punkte“.
- **NFR-03:** **gehalten** in `ingest`, `ingest-spool`,
  `ingest-rediscover` und im parallelen Ingest der Schreibjobs `import`,
  `template` und `computed` (jede Schwelle im Median und in jedem Lauf;
  0 fehlende Records). Max-Drain [P5] 348,3 Records/s im Median, kein
  G2-Budget.
- **NFR-01-Volumina:** **erzeugt und gemessen** (10.010 CIs, 50.000
  Interfaces, 200.000 Adressen, 100.000 Beziehungen, 503 Organisationen in
  jedem Lauf); NFR-02 auf den Volumina gegen G2 gehalten, gegen G1 siehe
  oben.
- **ABN-02:** **erfüllt**: Jeder Teil hat ein k6-Szenario mit p95 und
  Schwelle und, wo vorhanden, einen Query-Budget-Test; alle Query-Budget-
  Tests bestehen auf dem Release-Kandidaten ohne `t.Skip` (ohne
  Zeitgrenze, `ci.yml`). Der Lauf mit `RETICORA_PERF_BUDGET=1` auf dem
  4-vCPU-Host steht aus.
- **Headroom (E-17):** **funktionsfähig** bei 25.000 und 50.000 Objekten
  (0 fehlgeschlagene Anfragen, Checks 100 %), ohne Budgetzusage; bei 50.000
  Objekten liegen elf Lesepfade über den G2-Schwellen.
- **Gesamturteil:** Der Nachweis nach E-58 ist geführt: drei vollständige
  Läufe desselben Server-Commits `69e6668d`, bewertet durch
  `load-evidence.yml` (Lauf 42683419968) mit „Bewertung nach E-58 (Median
  über drei Läufe): alle Budgets gehalten“. Er gilt für den vorläufigen
  Budgetsatz G2 (E-146); NFR-02 im Wortlaut (Satz G1) ist für 14 Budgets
  nicht erreicht.

## Offene Punkte

Bezug: die Offenen Punkte von `docs/acceptance/lasttest-g1.md`
(Nummerierung seit WP-439), bezogen auf die Phase-2-Pfade, und jedes gegen
G1 verfehlte Budget. Für jeden Punkt ohne WP und jedes verfehlte Budget
nennt dieser Abschnitt einen Folge-WP-Vorschlag; die Korrektur gehört nicht
zu WP-327. „Folge-WP: Vorschlag“ heißt: noch kein WP angelegt, Vorschlag an
den Auftraggeber, die Nummer wird hier nachgetragen.

### Punkte aus lasttest-g1.md

| Punkt (lasttest-g1.md) | Korrektur | Nachweis auf den Phase-2-Pfaden | Wert | Folge-WP |
|---|---|---|---|---|
| 4: Listen und Suche über etwa 25.000 Objekte | WP-839 (E-104) | Headroom-Läufe: `GET /cis`, `GET /search` mit gedeckeltem `total`; Asset-, Ticket- und Mengenartikel-Liste mit Zählung je Anfrage; `api-large-28000` | `list` 326,6 / 890,4 ms und `global_search` 766 / 2.810 ms bei 25.000 / 50.000 Objekten; `list` Median 636,7 ms bei 28.000 CIs; `asset_list` 503,8 / 1.070 ms, `ticket_list` 1.000 / 1.830 ms, `consumable_list` 228,1 / 391,0 ms; Form von `total` geprüft | Vorschläge V-1 (Suche), V-4 (Zählung der P2-Listen) |
| 5: `ListNodes`-Schwelle (`nodeOrgReadAbove`) | WP-839 | Headroom-Läufe, `topology` und `impact` bei 25.000 und 50.000 Objekten | `topology` 944 / 3.310 ms, `impact` 1.060 / 3.910 ms (G2 gehalten, G1 bei 50.000 überschritten; ohne Budgetzusage) | Vorschlag V-3 (Graph-Cache), vor GA |
| 5: Mehrfach-Impact | WP-840 | `impact`, `impact_full` im Job `api` gegen den Seed mit Feldwerten; `TestMultiImpactAgainstPostgres` grün | Median 1.493,7 / 1.635,3 ms (G1 gehalten), L3 2.086,1 / 2.250,5 ms | keiner nötig; Reserve über V-3 |
| 7: Outbox-Nummerierung | WP-834 | `ingest_visible_delay` des parallelen Ingest in `import`, `template` und `computed` (Kontrollmessung) | Median 2,42 / 3,88 / 2,88 s (Schwelle < 30 s); Abbau in `ingest-spool` 348,3 Records/s im Median, unter dem Kriterium der Kontrollmessung WP-834 (≥ 414,7 Records/s aus WP-612) | Vorschlag V-7 (Ingest-Durchsatz, mit G1-Punkt 2) |
| 8: Statistik nach Massenimport (`database.AnalyzeAfterBulk`) | WP-834 | `import_update_duration` des zweiten Importlaufs ohne manuelles `ANALYZE` | Median 4 min 8 s gegen 4 min 0 s des ersten Laufs (+3 %); in jedem Lauf +2 bis +6 %, kein Planbruch erkennbar | keiner nötig |
| 10: Override-Abgleich der Knotenlesung | WP-840 | Topologie und Impact im Job `api` gegen den Seed mit Feldwerten in `ci_field_value` (500 Overrides) | `topology` 1.452,8 ms, `impact` 1.493,7 ms im Median (G1 gehalten); `TestImpactWorstCase` nutzt `idx_ci_field_value_override` | Vorschlag V-5 (Override-Projektion der Listen) |

Erledigt ohne neuen Nachweis: Fingerprint-MACs (früher Punkt 8, WP-434,
`ci_identity_mac`, 000161); bestätigt durch `ingest`, `ingest-spool` und
`ingest-rediscover` auf dem Phase-2-Stand (Abschnitt „NFR-03“, 0 neue CIs
in der Wiederentdeckung).

### Gegen G1 verfehlte Budgets (Median)

Grundlage sind die Statement-Profile und Pläne der Läufe
(`statements-<job>.txt`, `slow-plans-<job>.txt`, im Job-Log auszugsweise)
und die Befunde der Lastregression Runde 4 (PR #150). Keiner der Fälle ist
ein N+1 oder ein fehlender Index im Sinn der Query-Budget-Tests; alle
bestehen.

| Budget (Median, Job) | Schwelle G1 | Befund | Folge-WP (Vorschlag) |
|---|---:|---|---|
| `global_search` 1.168,7 ms (`api`), 899,3 ms (`nfr01-volume`); `search_fields` 1.423,7 ms (`phase2`) | < 500 ms | Volltext nach E-133 (Option 3) als linearer Scan über `search_document` mit `ts_rank` und gedeckelter Zählung `COUNT(*) OVER()`; mit Feldrechten zusätzlich die Zusatzfeldzeilen (`search_document_field`). Wächst linear mit der Organisation (2.810 ms bei 50.000 Objekten). | **V-1 `g2-search-rework`:** Umbau der globalen Suche: Kandidaten über einen RLS-fähigen Index (LEAKPROOF-Hülle nach dem Muster von `reticora_attr_text`, E-73 (Korrekturplan); Neubewertung von E-133), Rangfolge nur über die Kandidaten, Zählung getrennt und gedeckelt; Feldsuche (`search_fields`) im selben Umbau. Entscheidung zu E-133 nötig. |
| `list_name_filter` 576,3 ms (`api`) | < 500 ms | `name ILIKE $1 OR hostname ILIKE $1 OR host(management_ip) …` liest die Organisation (982 Aufrufe, bis 495 ms je Ausführung in L1); `ILIKE` ist nicht leakproof und unter RLS ohne Index. 1.780 ms bei 50.000 Objekten. | **V-2 `g2-name-trgm`:** Trigramm-Index (`pg_trgm`, GIN) auf den kleingeschriebenen Namens- und Hostnamenspalten mit leakproof Vergleichsfunktion wie bei Hostname/FQDN (`idx_ci_hostname_lc`); Plan-Nachweis im Fall `name filter` von `TestCIQueryBudget`. |
| `bulk_500_duration` 57,46 s, `bulk_fields_500_duration` 61,73 s (`api`); 30,89 / 30,93 s (`nfr01-volume`) | < 30 s | Seit Runde 4 eine Transaktion je CI; jede nimmt die Audit-/Outbox-Sperre der Organisation (`pg_advisory_xact_lock`, 441 Aufrufe, zusammen 169 s, bis 2,2 s Wartezeit in L1) und konkurriert mit den Lesern der Mischlast. | **V-6 `g2-bulk-batch`:** Bulk in Blöcken (z. B. 50 CIs je Transaktion) mit einer Sperre, einem Audit- und Outbox-Schreibvorgang je Block; Statementbudget je Block als Test. |
| `list` 636,7 ms, `list_sorted` 614,5 ms (`api-large-28000`) | < 500 ms | Gedeckelte Zählung nach E-104 liest bis 10.001 Zeilen je Anfrage (`SELECT COUNT(*) FROM (… LIMIT 10001)`, 741 Aufrufe, zusammen 110 s im Job `api`); bei 10.000 CIs knapp im Budget (459,1 ms), darüber nicht. | **V-4 `g2-list-count`** (mit den P2-Listen unten) |
| `asset_list` 526,1 ms, `asset_list_restricted` 502,9 ms, `ticket_list` 878,2 ms (`phase2`) | < 500 ms | Exakte Zählung je Anfrage (`SELECT COUNT(*)`, E-104 nur für `/cis` und `/search`); die Ticketliste zählt zusätzlich mit der Sichtbarkeit über `ci` und `asset`. Wächst mit der Organisation (`ticket_list` 1.830 ms bei 50.000 Objekten). | **V-4 `g2-list-count`:** Zählung der CI-, Asset- und Ticketliste ohne Lesung über die Organisation je Anfrage: gedeckelte Zählung nach E-104 auch für `/assets` und `/tickets` (Erweiterung von E-104, Entscheidung nötig) oder Zähler je Organisation und Filterklasse; für die Ticketliste die Sichtbarkeitsprüfung als Semi-Join über `idx_ticket_list_sort`. |
| `ci_attr_filter` 563,2 ms, `ast_asset_filter` 511,9 ms (`phase2`) | < 500 ms | Filter selbst indexfähig (`idx_ci_warranty_days_left`, Asset-Felder über das verknüpfte Asset, `TestSavedViewQueryBudget`); die Zeit liegt in Zählung und Projektion der Liste. | **V-4** (Zählung) und **V-5** (Projektion) |
| `asset_list_restricted` 502,9 ms (`phase2`); Feldrechte in Listen | < 500 ms | Projektion der Feldrechte und Overrides je Zeile über den Handler (`listBudget + 11` Statements in `TestAssetQueryBudget`); der Override-Abgleich liest `ci_field_value` je Anfrage nach. | **V-5 `g2-override-projection`:** Override-Werte und Feldrechte-Maske je Typ einmal je Anfrage bzw. als materialisierte Spalte projizieren statt je Seite nachzulesen; Statementbudget ohne Zuschlag für die Projektion. |
| `view_presets` 2.476,1 ms (`phase2`) | < 500 ms | Systemsichten (z. B. `servers_without_monitoring`) als Filter-AST mit Unterabfragen über Messwerte und Typbaum; seit PR #148 ohne Vollscan je CI, aber 6.760 ms bei 50.000 Objekten. | **V-8 `g2-view-presets`:** Systemsichten als vorberechnete Mengen (Typbaum je Organisation zwischengespeichert, Monitoring-Zuordnung als Semi-Join über einen Index) mit Plan-Nachweis je Sicht in `TestSavedViewQueryBudget`. |
| `relation_search` 1.323,1 ms (`phase2`) | < 500 ms | `upstream_of`/`downstream_of` bis Tiefe 5 seit Runde 4 je Arbeitstabellenzeile über den Kantenindex; rekursiver Walk je Anfrage, 4.910 ms bei 50.000 Objekten. | **V-3 `g2-graph-cache`:** Topologie-/Impact-Graph-Cache je Organisation (Kantenmenge im Speicher, invalidiert über die Outbox-Events der Beziehungen), genutzt von Topologie, Impact, Blast Radius und der Beziehungssuche; Statementbudget unabhängig von der Tiefe. Erhöht zugleich die Reserve von `impact`/`impact_full` (L3 über 2.000 ms) und `topology` bei 50.000 Objekten. |

### Weitere Punkte

- **Wiederentdeckung (NFR-03):** Median der Verzögerung 19,93 s (bis
  23,65 s) gegen 2,35 s in den G1-Nachweisläufen; die Schwelle von 30 s
  hält, die Reserve ist klein. Vorschlag **V-7 `g2-ingest-throughput`**:
  Statement-Profil der Wiederentdeckung auf dem Phase-2-Stand
  (Feldwerte in `ci_field_value`, Auto-Link WP-741, Konfliktprüfung
  WP-772/WP-300), zusammen mit G1-Punkt 2 (Max-Drain [P5]) und dem
  Kriterium der Kontrollmessung WP-834 (Abbau 348,3 statt ≥ 414,7
  Records/s).
- **CPU und RAM je Lauf (E-58):** stehen nur in `run-<job>.txt` der Archive;
  die Archive sind nicht ins Repository übernommen (Blob-Speicher der
  Artefakte aus der Arbeitsumgebung nicht erreichbar). Übernahme nach
  `tests/load/README.md`, Abschnitt „Archiv eines Laufs“, innerhalb der
  Aufbewahrungsfrist der Artefakte; danach die Tabelle „CPU/RAM“ füllen und
  `tests/load/evaluate-runs.sh` lokal auf die drei Verzeichnisse anwenden
  (Ergebnis muss dem von Lauf 42683419968 gleichen). Kein Folge-WP, Retest.
- **Lokaler Lauf (4 vCPU) und Query-Budget-Tests mit
  `RETICORA_PERF_BUDGET=1`:** nicht durchgeführt (keine 4-vCPU-Umgebung mit
  TimescaleDB in der Arbeitsumgebung). Ergänzung wie in G1, nicht Teil der
  Bewertung nach E-58; Retest auf dem Referenzhost vor GA (SR-06).
- **Budgetsatz G2 (E-146):** vorläufig. Mit jeder umgesetzten
  Folge-WP-Korrektur wird der G2-Wert der betroffenen Metrik in
  `tests/load/budgets.json` und E-146 Richtung G1 gesenkt; Ziel bleibt
  NFR-02 (Satz G1) auf dem Referenzhost vor GA.
