# Lasttest G2 (NFR-02, NFR-03, ABN-02)

Nachweis zu NFR-02 (Phase-2-Listen und Regression der G1-Budgets), NFR-03
(G1-Regression des Ingest und paralleler Ingest der Massen-Schreibjobs) und
ABN-02 vor Gate G2, nach dem Protokoll von E-58 mit der Ergänzung vom
2026-10-26 (Median über drei Läufe). Gliederung wie
`docs/acceptance/lasttest-g1.md` seit WP-439.

**Stand 2026-11-09: Gerüst, Nachweis ausstehend.** Dieses Dokument legt
Aufbau, Werkzeuge und die Zuordnung von ABN-02 aus dem Code fest. Alle
Messwerte, Lauf-IDs, Commits, Archive und Bewertungen sind **ausstehend**;
keine Zahl in diesem Dokument ist ein Messwert des G2-Nachweises. Die
Akzeptanzkriterien von WP-327 (`g2-perf`) sind damit nicht erfüllt.

Werkzeuge: `.github/workflows/load.yml` (ein Job je Szenario),
`tests/load/archive-run.sh` (Archiv `load-archive-<Lauf-ID>`),
`tests/load/evaluate-runs.sh` (Bewertung nach dem Median, Jobsatz G2 als
Standard) und `.github/workflows/load-evidence.yml`; Ablauf und Übernahme
der Archive in `tests/load/README.md`.

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
- Ob die Läufe nach PR #148 grün sind, belegen erst die Nachweisläufe
  (Abschnitt „Läufe“). Ausstehend.

## Messumgebung und Ablauf (E-58)

- **Umgebung:** GitHub Actions, Standard-Runner `ubuntu-latest` mit 4 CPUs
  (E-58, Option 1; dedizierter Referenzhost vor GA, SR-06). PostgreSQL mit
  TimescaleDB, NATS mit JetStream, Redis, MinIO (Jobs `import`, `template`,
  `computed`), Server und k6 teilen sich den Runner.
- **Release-Kandidat G2:** Commit ausstehend (Zeile „Server, Seed und
  Migrationen“ in `ergebnis.md`, in allen drei Läufen derselbe).
- **Konfiguration:** `RETICORA_MODULES` = `P2` (Release-Konfiguration G2,
  WP-830), `RETICORA_INGEST_LANES` = 4, `RETICORA_SEARCH_BACKEND` =
  `postgres` (Standard der Konfiguration; `load.yml` setzt die Variable
  nicht, `run-<job>.txt` nennt sie bisher nicht), Rate-Limit 1.000.000 Anfragen/min je Principal und je
  Organisation, `duration` 2 min für `api.js` und `phase2.js`,
  `INGEST_DURATION_S` = 120 s für den Ingest neben `writes.js`. Werte je
  Job aus `run-<job>.txt`: ausstehend.
- **Seed-Eingaben:** `objects` 10.000 (CIs), `assets`, `tickets` und
  `consumables` je 10.000 (Standardeingaben von `load.yml`, WP-822); dazu
  die Feldwerte in `ci_field_value` (WP-821). Zusammensetzung nach dem Seed
  (`volumes-<job>.txt`):

  | Job | CIs | Assets | Tickets | Mengenartikel | Feldwerte (`ci_field_value`) | Beziehungen | Organisationen |
  |---|---:|---:|---:|---:|---:|---:|---:|
  | `api` | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend |
  | `phase2` | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend |
  | `nfr01-volume` | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend |
  | `import`, `template`, `computed` (Größe am Jobende, `size-<job>.txt`) | ausstehend | ausstehend | – | – | – | – | – |

- **Ablauf je Job:** frische Datenbank, Migrationen, Seed, neu aufgebauter
  Suchindex, `ANALYZE`, Server mit 4 Spuren und den P2-Modulen, ein
  k6-Szenario (bei den Schreibjobs Vorbereitung, dann `writes.js` und
  `ingest.js` parallel), danach `tests/integrity-check.sql` (jede Prüfung 0)
  und die Datenbankkosten aus `pg_stat_statements`.
- **Jobs des Nachweises** (Jobsatz G2 von `evaluate-runs.sh`): `api`,
  `ingest`, `ingest-spool`, `ingest-rediscover`, `nfr01-volume` (G1-Regression
  mit unveränderten Schwellen gegen den erweiterten Seed), `phase2`
  (WP-822), `import`, `template`, `computed` (WP-823). `scenarios=all`
  startet zusätzlich `api-large-28000` und `api-large-50000` (WP-839); die
  Bewertung führt sie mit.
- **Lokaler Lauf (4 vCPU)** nach `tests/load/README.md` als Ergänzung wie in
  G1: ausstehend.

| Lauf | CPU (`lscpu`) | RAM (`MemTotal`) |
|---|---|---|
| Lauf 1 | ausstehend | ausstehend |
| Lauf 2 | ausstehend | ausstehend |
| Lauf 3 | ausstehend | ausstehend |

## Läufe

Drei vollständige Läufe von `load.yml` per `workflow_dispatch` mit
`scenarios=all` auf dem Release-Kandidaten, jeweils im ersten Versuch.

| Lauf | Lauf-ID | Commit (Lauf / Server) | `api` | `ingest` | `ingest-spool` | `ingest-rediscover` | `nfr01-volume` | `phase2` | `import` | `template` | `computed` | Vollständig |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| L1 | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend |
| L2 | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend |
| L3 | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend |

Archive: `load-archive-<Lauf-ID>` je Lauf, übernommen nach
`docs/acceptance/artefakte/lasttest/<Lauf-ID>/`: ausstehend.

### Bewertung nach E-58 (Median über drei Läufe)

Ein Budget gilt als gehalten, wenn der Median seiner Messwerte aus den drei
Läufen im Budget liegt; ein Job ist verfehlt, wenn er in mindestens zwei
Läufen rot ist. In den Jobs `import`, `template` und `computed` gelten
zusätzlich die Schwellen des parallelen Ingest (`<job>-ingest.json`).
Bewertet mit `load-evidence.yml` (Eingabe `gate` = `G2`).

- Lauf von `load-evidence.yml`: ausstehend.
- Artefakt `load-evidence-<Lauf-ID>` (`bewertung.md`): ausstehend.
- Letzte Zeile der Bewertung: ausstehend.

| Job | rot in Läufen | Urteil | Budgets im Median |
|---|---|---|---|
| `api` | ausstehend | ausstehend | ausstehend |
| `ingest` | ausstehend | ausstehend | ausstehend |
| `ingest-spool` | ausstehend | ausstehend | ausstehend |
| `ingest-rediscover` | ausstehend | ausstehend | ausstehend |
| `nfr01-volume` | ausstehend | ausstehend | ausstehend |
| `phase2` | ausstehend | ausstehend | ausstehend |
| `import` (mit parallelem Ingest) | ausstehend | ausstehend | ausstehend |
| `template` (mit parallelem Ingest) | ausstehend | ausstehend | ausstehend |
| `computed` (mit parallelem Ingest) | ausstehend | ausstehend | ausstehend |
| `api-large-28000`, `api-large-50000` | ausstehend | ausstehend | ausstehend |

## NFR-02: p95 je Budget

Werte in ms, Bulk in s; Median über die drei Läufe aus der Bewertung.

### G1-Regression (`api.js`, Job `api`, 10.000 CIs mit erweitertem Seed)

| Budget (NFR-02) | k6-Szenario (`name`) | Schwelle | L1 | L2 | L3 | Median |
|---|---|---:|---:|---:|---:|---:|
| Listen | `list` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Listen (sortiert nach Name, E-104) | `list_sorted` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Suche (Namensfilter der Liste) | `list_name_filter` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Suche (globale Suche der UI) | `global_search` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Indizierte Attributfilter | `filter` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Attributfilter (JSONB `cost_center`) | `attr_filter` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Beziehungen eines CI | `relationships` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| CI-Detail | `detail` | < 300 | ausstehend | ausstehend | ausstehend | ausstehend |
| Rack-Layout | `rack` | < 300 | ausstehend | ausstehend | ausstehend | ausstehend |
| Topologie 2.000 Knoten | `topology` | < 3.000 | ausstehend | ausstehend | ausstehend | ausstehend |
| Impact Tiefe 20 (kompakt) | `impact` | < 2.000 | ausstehend | ausstehend | ausstehend | ausstehend |
| Impact Tiefe 20 (Standardform) | `impact_full` | < 2.000 | ausstehend | ausstehend | ausstehend | ausstehend |
| Blast Radius | `blast_radius` | < 2.000 | ausstehend | ausstehend | ausstehend | ausstehend |
| Bulk 500 IDs (Status) | `bulk_500_duration` | < 30 s | ausstehend | ausstehend | ausstehend | ausstehend |
| Bulk 500 IDs (`set_fields`) | `bulk_fields_500_duration` | < 30 s | ausstehend | ausstehend | ausstehend | ausstehend |
| Checks / fehlgeschlagene Anfragen | `checks`, `http_req_failed` | > 99 % / 0 | ausstehend | ausstehend | ausstehend | ausstehend |

### Phase 2 (`phase2.js`, Job `phase2`)

| Budget (NFR-02) | k6-Szenario (`name`) | Schwelle | L1 | L2 | L3 | Median |
|---|---|---:|---:|---:|---:|---:|
| Listen (Asset-Liste mit `total`, K23) | `asset_list` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Listen (Asset-Liste mit Feldrechten von viewer) | `asset_list_restricted` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Listen (Ticketliste mit `total`) | `ticket_list` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Listen (Mengenartikel mit `total`) | `consumable_list` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Suche (Felder aus WP-308) | `search_fields` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Attributfilter (Bereich `cost_center`, `warranty_days_left`) | `ci_attr_filter` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Tag-Filter der CI-Liste | `ci_tag_filter` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Filter auf Asset-Felder (`POST /search/query`) | `ast_asset_filter` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Systemsichten (WP-307) | `view_presets` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Beziehungssuche (`upstream_of`/`downstream_of`, Tiefe 5) | `relation_search` | < 500 | ausstehend | ausstehend | ausstehend | ausstehend |
| Bulk 500 IDs (Assets, `set_fields`) | `bulk_assets_500_duration` | < 30 s | ausstehend | ausstehend | ausstehend | ausstehend |
| Bulk 500 IDs (CIs, `add_tags`) | `bulk_cis_ops_500_duration` | < 30 s | ausstehend | ausstehend | ausstehend | ausstehend |
| Checks / fehlgeschlagene Anfragen | `checks`, `http_req_failed` | > 99 % / 0 | ausstehend | ausstehend | ausstehend | ausstehend |

### Massen-Schreibjobs (`writes.js`, Messgrößen ohne Budget)

| Job | Messgröße | Ziel | L1 | L2 | L3 | Median |
|---|---|---|---:|---:|---:|---:|
| `import` | `import_10000_duration` | < 5 min (Ziel, keine Schwelle) | ausstehend | ausstehend | ausstehend | ausstehend |
| `import` | `import_update_duration` (zweiter Lauf ohne manuelles `ANALYZE`) | – | ausstehend | ausstehend | ausstehend | ausstehend |
| `template` | `template_migrate_duration`, `template_migrated` | – | ausstehend | ausstehend | ausstehend | ausstehend |
| `computed` | `computed_backfill_duration` | – | ausstehend | ausstehend | ausstehend | ausstehend |

Schwellen der Schreibjobs: `<job>_jobs_succeeded` (`import` `count==4`,
`template` und `computed` `count==1`), `checks` `rate==1`,
`http_req_failed` `rate==0`. Werte: ausstehend.

## NFR-03

Schwellen aus `ingest.js`: `ingest_visible_delay` `max<30000`,
`ingest_records_missing` `count==0`, `http_req_duration{name:ingest}`
`p(95)<1000`, `dropped_iterations` `count==0`, `checks` `rate>0.99`,
`http_req_failed` `rate==0`; `spool` zusätzlich `ingest_spool_flush`
`max<300000` und `ingest_drain_records_per_s` `value>=200`;
`rediscover` zusätzlich `ingest_new_cis` `count==0`.

| Job | Szenario | Verzögerung max (s) | fehlend | weitere Schwellen | Median gehalten |
|---|---|---:|---:|---|---|
| `ingest` | `load`, 200 Records/s | ausstehend | ausstehend | ausstehend | ausstehend |
| `ingest-spool` | `spool`, 10.000 Records | ausstehend | ausstehend | Flush und Abbau: ausstehend | ausstehend |
| `ingest-rediscover` | `rediscover`, 200 Records/s | ausstehend | ausstehend | neue CIs: ausstehend | ausstehend |
| `import` | paralleler Ingest (`import-ingest.json`, 120 s) | ausstehend | ausstehend | ausstehend | ausstehend |
| `template` | paralleler Ingest (`template-ingest.json`, 120 s) | ausstehend | ausstehend | ausstehend | ausstehend |
| `computed` | paralleler Ingest (`computed-ingest.json`, 120 s) | ausstehend | ausstehend | ausstehend | ausstehend |

Fingerprint-MACs (WP-434, `ci_identity_mac`, 000161): Bestätigung auf dem
Phase-2-Stand durch `ingest`, `ingest-spool` und `ingest-rediscover`:
ausstehend. Max-Drain gegenüber 1.000 Records/s [P5]: Messwert aus
`ingest-spool`, kein G2-Budget; ausstehend.

## NFR-01-Volumina

Job `nfr01-volume`: `api.js` mit den G1-Schwellen auf 10.000 CIs mit 50.000
Interfaces, 200.000 Adressen, 100.000 Beziehungen und 500 Organisationen,
dazu die Phase-2-Mengen der Eingaben. Ersetzt die Headroom-Läufe nicht.

| Kenngröße | Soll | L1 | L2 | L3 |
|---|---:|---:|---:|---:|
| CIs der Demo-Organisation | 10.000 | ausstehend | ausstehend | ausstehend |
| Interfaces | 50.000 | ausstehend | ausstehend | ausstehend |
| Adressen | 200.000 | ausstehend | ausstehend | ausstehend |
| Beziehungen | 100.000 | ausstehend | ausstehend | ausstehend |
| Organisationen | 500 | ausstehend | ausstehend | ausstehend |

Budgets im Median (Schwellen wie Job `api`): ausstehend.

## ABN-02

ABN-02 [Q]: „Performance-Sanity gegen NFR-02 für Listen, Suche,
Attributfilter, Beziehungssuche, Topologie, Impact, Bulk; Prüfung auf N+1
und fehlende Indizes.“ Jeder Teil hat ein k6-Szenario (`name`-Tag,
Schwelle; p95 aus den Läufen oben) und, wo vorhanden, einen
Query-Budget-Test (Statementzahl, Plan). Die Tests laufen bei jedem PR gegen
PostgreSQL (`ci.yml`); Zeitgrenzen nur mit `RETICORA_PERF_BUDGET=1` auf dem
4-vCPU-Host (Ergebnis dort: ausstehend). Die p95-Werte stehen im Abschnitt
„NFR-02“ und sind ausstehend.

### Zuordnung

| Teil von ABN-02 / NFR-02 | k6-Szenario (`name`-Tag, Skript) | Schwelle (p95) | Query-Budget-Test (Statementzahl, Plan) |
|---|---|---:|---|
| Listen: CI-Liste | `list`, `list_sorted` (`api.js`) | < 500 ms | `TestCIQueryBudget` Fall `list` (höchstens 6 Statements für 10 und 200 Zeilen); `TestCIListBudget` (feste Statementzahl, Seite über `idx_ci_list_sort` ohne Sortierung der Organisation) |
| Listen: Asset-Liste, auch mit Feldrechte-Projektion | `asset_list`, `asset_list_restricted` (`phase2.js`) | < 500 ms | `TestAssetQueryBudget` (Liste höchstens 5 Statements, über den Handler mit Projektion `listBudget + 11`; Standardseite über `idx_asset_list_sort`, 000278) |
| Listen: Ticketliste | `ticket_list` (`phase2.js`) | < 500 ms | `TestTicketQueryBudget` (höchstens 5 Statements; Standardseite über `idx_ticket_list_sort`, 000277; Sichtbarkeit über `ci_pkey`/`ci_id_organization_key` und `asset_pkey`/`asset_id_organization_key`) |
| Listen: Mengenartikel | `consumable_list` (`phase2.js`) | < 500 ms | `TestConsumableListQueryBudget` (Seite über `idx_consumable_list_name`, 000279) |
| Suche: Namensfilter der CI-Liste | `list_name_filter` (`api.js`) | < 500 ms | `TestCIQueryBudget` Fälle `name filter (search=)`, `name filter (filter[name]=)` |
| Suche: globale Suche | `global_search` (`api.js`), `search_fields` (`phase2.js`) | < 500 ms | `TestRestrictedSearchQueryBudget`, `TestRestrictedSearchFieldRowBudget` (eingeschränkte Suche, Zusatzfeldzeilen einmal je Suche), `TestReindexFieldRowsQueryBudget`; Volltext nach E-133 (Option 3) als dokumentierter linearer Scan ohne Planprüfung |
| Attributfilter: indizierte Filter | `filter` (`api.js`) | < 500 ms | `TestCIQueryBudget` Fall `hostname filter of the list (filter[hostname]=)` (`idx_ci_hostname_lc`) |
| Attributfilter: JSONB-Gleichheit | `attr_filter` (`api.js`) | < 500 ms | `TestCIQueryBudget` Fall `attribute filter` (Index je Schlüssel, E-73 (Korrekturplan), 000162/000164) |
| Attributfilter: Bereich und `warranty_days_left` | `ci_attr_filter` (`phase2.js`) | < 500 ms | `TestSavedViewQueryBudget` (`warranty_days_left` über `idx_ci_warranty_days_left`, 000274), `TestWarrantyDaysLeftIndexKeepsResults`; Bewertung des Plans zusammen mit dem p95 von `ci_attr_filter`: ausstehend |
| Attributfilter: Tags | `ci_tag_filter` (`phase2.js`) | < 500 ms | `TestCIQueryBudget` Fall `tag filter (filter[tags][in])` (`idx_ci_tag_org_tag`) |
| Attributfilter: Asset-Felder und Systemsichten | `ast_asset_filter`, `view_presets` (`phase2.js`) | < 500 ms | `TestSavedViewQueryBudget` (Filter-AST mit Asset-Feldern auf Assets und auf CIs über das verknüpfte Asset, höchstens 4 Statements für 10 und 200 Zeilen; Systemsicht `servers_without_monitoring`) |
| Beziehungssuche | `relation_search` (`phase2.js`); Beziehungen eines CI `relationships` (`api.js`) | < 500 ms | `TestSavedViewQueryBudget` (`upstream_of` über `idx_ci_rel_org_target`, `downstream_of` über `idx_ci_rel_org_source`/`uq_ci_relationship_edge`, Tiefe 5); `TestRelationshipListPlanBudget` |
| Topologie | `topology` (`api.js`) | < 3.000 ms | `TestTopologyQueryBudget` (höchstens 40 Statements, unabhängig von der Graphgröße; `seqScan`-Prüfung) |
| Impact, kompakte Form | `impact` (`api.js`) | < 2.000 ms | `TestImpactWorstCase` (höchstens 250 Byte je betroffenem CI, Statementzahl wie bei 3.000 Objekten, Override-Abgleich über `idx_ci_field_value_override`), `TestImpactLeanAgainstPostgres` |
| Impact, Standardform | `impact_full` (`api.js`) | < 2.000 ms | `TestImpactWorstCase` (Pfade aus `via`/`relay`); Mehrfach-Impact `TestMultiImpactAgainstPostgres` |
| Impact, Blast Radius | `blast_radius` (`api.js`) | < 2.000 ms | `TestTopologyQueryBudget` (Fall `blast-radius`), `TestServiceImpactWorstCase` |
| Bulk | `bulk_500_duration`, `bulk_fields_500_duration` (`api.js`); `bulk_assets_500_duration`, `bulk_cis_ops_500_duration` (`phase2.js`) | < 30 s | kein Query-Budget-Test; nur k6 (jeder Bulk-Job muss gelingen: `*_jobs_succeeded` `count==3`) |
| Massen-Schreibjobs neben dem Ingest | `import`, `template`, `computed` (`writes.js`) | ohne Budget (Messgrößen) | `TestAssetQueryBudget` Fälle `import duplicate search by inventory number` (`idx_asset_tag_org`) und `import duplicate search by normalized serial number` (`idx_asset_org_serial_norm`); Ingest `TestIngestStatementBudget` |
| GraphQL-Aggregat (N+1) | – | – | `TestAssetAggregateQueryBudget` (`asset(id) { cis children movements documents }`, gleiche Statementzahl für 10 und 100) |
| NFR-02 CI-Detail | `detail` (`api.js`) | < 300 ms | `TestCIQueryBudget` Fall `detail` (höchstens 5 Statements, `ci_pkey`/`ci_id_organization_key`) |
| NFR-02 Rack-Layout | `rack` (`api.js`) | < 300 ms | kein Query-Budget-Test; Funktion `TestRackLayoutAgainstPostgres`; Messung nur k6 |

N+1 und fehlende Indizes: Jeder Query-Budget-Test oben misst die
Statementzahl über einen pgx-Tracer an einer Kopie des App-Pools und prüft
den Plan per `EXPLAIN (ANALYZE, FORMAT JSON)` als App-Rolle unter RLS
(Helfer `backend/internal/database/scopetest/querybudget.go`, WP-824).
Ergebnis der Tests auf dem Release-Kandidaten, mit und ohne
`RETICORA_PERF_BUDGET=1`: ausstehend. Fälle mit `t.Skip` und Verweis auf
ein Folge-WP: ausstehend (Abschnitt „Offene Punkte“).

## Headroom (E-17, ohne Budgetzusage)

Jobs `api` und `phase2` einzeln per `workflow_dispatch` mit
`scenarios=api,phase2` (Aufruf in `tests/load/README.md`, Abschnitt
„Headroom nach E-17“). Grenze des Seeds: `objects + assets` ≤ 50.000.
Verfehlte Schwellen sind hier Messwerte, kein Gate.

| Lauf | Lauf-ID | `objects` | `assets` | `tickets` | `consumables` | Objekte (CI + Asset) |
|---|---|---:|---:|---:|---:|---:|
| Headroom 25.000 | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | 25.000 |
| Headroom 50.000 | ausstehend | ausstehend | ausstehend | ausstehend | ausstehend | 50.000 |

| Messgröße | Szenario | 25.000 Objekte | 50.000 Objekte |
|---|---|---:|---:|
| `GET /api/v1/cis` p95 (`total` nach E-104, gedeckelt) | `list`, `list_sorted` | ausstehend | ausstehend |
| `GET /api/v1/search` p95 (`total` nach E-104) | `global_search` | ausstehend | ausstehend |
| `GET /api/v1/assets` p95 (Zählung je Anfrage) | `asset_list` | ausstehend | ausstehend |
| `GET /api/v1/tickets` p95 (Zählung je Anfrage) | `ticket_list` | ausstehend | ausstehend |
| `GET /api/v1/consumables` p95 (Zählung je Anfrage) | `consumable_list` | ausstehend | ausstehend |
| `ListNodes` (Topologie, Impact) | `topology`, `impact`, `impact_full` | ausstehend | ausstehend |
| Fehlerfreiheit | `http_req_failed`, `checks` | ausstehend | ausstehend |

## Bewertung für G2

- **NFR-02:** ausstehend (Median der drei Läufe, G1-Regression und
  Phase-2-Budgets).
- **NFR-03:** ausstehend (`ingest`, `ingest-spool`, `ingest-rediscover` und
  der parallele Ingest der Schreibjobs).
- **NFR-01-Volumina:** ausstehend.
- **ABN-02:** Zuordnung vollständig (Abschnitt „ABN-02“); Messwerte und
  Testergebnisse auf dem Release-Kandidaten ausstehend.
- **Headroom (E-17):** ausstehend, ohne Budgetzusage.
- **Gesamturteil:** ausstehend. Der Nachweis gilt erst mit drei
  vollständigen Läufen desselben Server-Commits, deren Bewertung durch
  `load-evidence.yml` mit „Bewertung nach E-58 (Median über drei Läufe):
  alle Budgets gehalten“ endet.

## Offene Punkte

Bezug: die Offenen Punkte von `docs/acceptance/lasttest-g1.md`
(Nummerierung seit WP-439), deren Wirkung auf die Phase-2-Pfade bisher kein
WP nachweist. Für jeden Punkt ohne WP und jedes verfehlte Budget nennt
dieser Abschnitt mit dem Nachweis einen Folge-WP-Vorschlag; die Korrektur
gehört nicht zu WP-327.

| Punkt (lasttest-g1.md) | Korrektur | Nachweis auf den Phase-2-Pfaden | Wert | Folge-WP |
|---|---|---|---|---|
| 4: Listen und Suche über etwa 25.000 Objekte | WP-839 (E-104) | Headroom-Läufe: `GET /cis`, `GET /search` mit gedeckeltem `total`; Asset-, Ticket- und Mengenartikel-Liste mit Zählung je Anfrage | ausstehend | ausstehend |
| 5: `ListNodes`-Schwelle (`nodeOrgReadAbove`) | WP-839 | Headroom-Läufe, `topology` und `impact` bei 25.000 und 50.000 Objekten | ausstehend | ausstehend |
| 5: Mehrfach-Impact | WP-840 | `impact`, `impact_full` im Job `api` gegen den Seed mit Feldwerten | ausstehend | ausstehend |
| 7: Outbox-Nummerierung | WP-834 | `ingest_visible_delay` des parallelen Ingest in `import`, `template` und `computed` (Kontrollmessung) | ausstehend | ausstehend |
| 8: Statistik nach Massenimport (`database.AnalyzeAfterBulk`) | WP-834 | `import_update_duration` des zweiten Importlaufs ohne manuelles `ANALYZE` | ausstehend | ausstehend |
| 10: Override-Abgleich der Knotenlesung | WP-840 | Topologie und Impact im Job `api` gegen den Seed mit Feldwerten in `ci_field_value` | ausstehend | ausstehend |

Erledigt ohne neuen Nachweis: Fingerprint-MACs (früher Punkt 8, WP-434,
`ci_identity_mac`, 000161); Bestätigung durch `ingest`, `ingest-spool` und
`ingest-rediscover` auf dem Phase-2-Stand ausstehend (Abschnitt „NFR-03“).

Verfehlte Budgets und Query-Budget-Fälle mit `t.Skip`: ausstehend.
