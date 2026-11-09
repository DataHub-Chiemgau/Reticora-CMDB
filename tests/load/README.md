# Lasttest (NFR-10)

k6-Szenarien (E-31) für die Budgets aus NFR-02 und NFR-03 bei der Seed-Größe aus
NFR-10 (SIM-01 skaliert auf 10.000 Objekte). Die Schwellen der Skripte sind die
Budgets: Wird ein Budget verfehlt, schlägt der Lauf fehl.

| Skript | Prüft | Budget (p95) |
|---|---|---|
| `api.js` `list` | `GET /cis?limit=50` | < 500 ms |
| `api.js` `list_name_filter` | Namensfilter der CI-Liste `GET /cis?search=…` (`name ILIKE`; bis WP-437 `search`) | < 500 ms |
| `api.js` `global_search` | globale Suche der UI (Befehlspalette) `GET /search?q=<Servername>&limit=5` über den nach dem Seed neu aufgebauten Index; der Treffer muss das CI sein | < 500 ms |
| `api.js` `attr_filter` | Filter auf das durchsuchbare JSONB-Attribut des Seeds `GET /cis?filter[attributes.cost_center]=CC-…` (200 CIs je Wert) | < 500 ms |
| `api.js` `relationships` | Beziehungen je CI `GET /cis/{id}/relationships` (Detailansicht), 500 CIs aus fünf über die Organisation verteilten Seiten | < 500 ms |
| `api.js` `filter` | indizierte Filter `filter[status]`, `filter[hostname]` | < 500 ms |
| `api.js` `detail` | `GET /cis/{id}` | < 300 ms |
| `api.js` `rack` | `GET /racks/{id}/layout` | < 300 ms |
| `api.js` `topology` | Topologie des Core-Switches, Tiefe 1 (> 2.000 Knoten) | < 3 s |
| `api.js` `impact` | Impact des Core-Switches, Tiefe 20 (ganze Organisation), kompakte Antwort der Topologie-Ansicht (`fields=…`, ≤ 250 Byte je betroffenem CI) | < 2 s |
| `api.js` `impact_full` | derselbe Impact in der vollständigen Antwort (alle Felder und Pfade) | < 2 s |
| `api.js` `blast_radius` | Blast Radius des Core-Switches mit Service-Impact-Kette `GET /cis/{id}/blast-radius` (WP-297); der Seed hat keine Services, gemessen werden Zeit und Größe der Antwort (`blast_radius_bytes`), die SPOF-Last weist `TestServiceImpactWorstCase` nach | < 2 s |
| `api.js` `bulk` | Bulk-Statuswechsel von 500 CIs bis Job-Ende | < 30 s |
| `ingest.js` `load` | `POST /ingest/bulk` mit `RATE` Records/s für `DURATION_S`; Verzögerung Record → sichtbar (Probe-Records während der Last, danach bis alle Records sichtbar sind) | < 30 s |
| `ingest.js` `spool` | Spool-Flush eines Collectors (COL-05): 10.000 Records als nacheinander gesendete Uploads (`POST /discovery/ingest`, gzip); Abbau-Rate ab Sendeende, Dauer bis alle sichtbar | ≥ 200/s, < 5 min |
| `ingest.js` `rediscover` | Wiederentdeckung der geseedeten Server (F-118): `RATE` SNMP-artige Records/s mit primärer MAC, Interface-MACs und LLDP-Nachbar über `POST /ingest/bulk`; Verzögerung Record → gesehen (`last_seen_at`), kein neues CI, kein fehlender Record | < 30 s, 0 neue CIs, 0 fehlend |

`global_search`, `attr_filter` und `relationships` laufen mit je 2 VUs in
der Mischlast mit (wie `topology` und `impact`); die gleichzeitige Last des
Laufs wächst damit um etwa ein Fünftel (32 auf 38 VUs).

Der Job `nfr01-volume` von `load.yml` führt `api.js` mit denselben
Schwellen auf den Volumina von NFR-01 aus (Abschnitt „NFR-01-Volumina“).
Die Listen, Suche, Filter, Beziehungssuche und Bulk-Operationen der Phase 2
misst `phase2.js` im Job `phase2` (Abschnitt „Phase 2“), die
Massen-Schreibjobs der Phase 2 parallel zum Ingest `writes.js` in den Jobs
`import`, `template` und `computed` (Abschnitt „Massen-Schreibjobs parallel
zum Ingest“).

**Fehlgeschlagene Anfragen (F-388, WP-659):** Kein Lauf ist mit einer
fehlgeschlagenen Anfrage grün. Jedes Skript hat `http_req_failed: rate==0`
für den ganzen Lauf (mit `setup`) und je Szenario
(`http_req_failed{scenario:<name>}`; `api.js` alle vierzehn Szenarien,
`ingest.js` `ingest` und `probe` bzw. `spool`). Als fehlgeschlagen zählt
k6 jede Antwort außerhalb 200–399 und jede Anfrage ohne Antwort (Status 0),
auch die Abfragen der Sichtbarkeit und `/readyz`. `api.js` verlangt
zusätzlich, dass jeder der drei Bulk-Jobs gelingt (`bulk_jobs_succeeded:
count==3`, `checks{scenario:bulk}: rate==1`); `bulk_500_duration` erfasst
nur gelungene Bulk-Jobs, eine abgewiesene Bulk-Anfrage (wenige ms) zählt
also nicht als schneller Bulk. `checks: rate>0.99` bleibt für die
inhaltlichen Prüfungen der Antworten. `tests/load/lib.js` (`failed`)
schreibt jede fehlgeschlagene Anfrage mit Name, Status, Methode, Pfad und
Anfang der Antwort auf die k6-Konsole; `load.yml` legt sie als
`console-<job>.log` ab, `tests/load/failed-requests.sh` zählt sie je Name
und Status für die Zusammenfassung des Jobs und `failed-<job>.txt`.
Selbsttest: `tests/load/thresholds.test.sh` (k6 `inspect` aller Skripte und
`api.js` gegen eine Attrappe der API: abgewiesene oder gescheiterte
Bulk-Jobs und eine einzelne 503 machen den Lauf rot), im Job `prepare`.

## Ablauf

```sh
# 1. Datenbank migrieren und den Last-Seed mit API-Schlüssel anlegen (mit
#    dem durchsuchbaren Attribut cost_center, seinen Werten und seinem Index)
migrate -path backend/migrations -database "$DATABASE_URL" up
go run ./backend/cmd/seed -database-url "$DATABASE_URL" -objects 10000 -api-key-file /tmp/load.key

# 2. Server starten. Das Rate-Limit (600 Anfragen/min je Principal, API-04)
#    würde den Lastlauf drosseln und wird für den Lauf angehoben.
#    RETICORA_MODULES=P2 ist die Release-Konfiguration G2 (alle Module der
#    Phase P2); ohne sie sind die Phase-2-Routen nicht registriert.
RETICORA_RATE_LIMIT_RPM=1000000 RETICORA_RATE_LIMIT_ORG_RPM=1000000 RETICORA_MODULES=P2 ./server

# 3. Suchindex neu aufbauen (Job search.reindex, 202 mit job_id: bis
#    "succeeded" abwarten) und die Statistik aktualisieren (ohne ANALYZE
#    hängen die Pläne davon ab, wann Autovacuum den Seed erreicht)
curl -fsS -X POST -H "X-API-Key: $(cat /tmp/load.key)" http://localhost:8080/api/v1/search/reindex
curl -fsS -H "X-API-Key: $(cat /tmp/load.key)" http://localhost:8080/api/v1/jobs/<job_id>
psql "$DATABASE_URL" -c 'ANALYZE'

# 4. Ein Szenario je Lauf, danach die Integritätsprüfung (jede Prüfung 0);
#    vor dem nächsten Szenario Schritt 1 bis 3 mit frischer Datenbank
#    wiederholen
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) tests/load/api.js
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) tests/load/ingest.js
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) -e SCENARIO=spool tests/load/ingest.js
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) -e SCENARIO=rediscover tests/load/ingest.js
psql "$DATABASE_URL" -At -F '|' -f tests/integrity-check.sql
```

Parameter: `DURATION`, `VUS` und `OBJECTS` (api.js; Seed-Größe der
Demo-Organisation, Standard 10.000, daraus die erwartete Zahl betroffener CIs
der Impact-Szenarien und die Seiten der Beziehungssuche), `ATTR_KEY` und
`ATTR_VALUES` (api.js; Attribut des Seeds und Zahl seiner Werte, Standard
`cost_center` und 50); `SCENARIO` (`load`, `spool` oder `rediscover`),
`RATE`, `BATCH`, `DURATION_S` (Standard 60 s, bei `rediscover` 45 s),
`PROBE_EVERY_S`, `VISIBLE_BUDGET_S`, `SPOOL_RECORDS`, `SPOOL_BATCH`,
`SPOOL_BUDGET_S`, `TARGET`, `WAIT_S` und `OBJECTS` (`rediscover`) (ingest.js);
`SEED_PREFIX` und `SEED_OCTET` (lib.js; Demo-Organisation des Seeds,
Standard `demo` und 10); `API_KEY` (lib.js, Schlüssel von org_admin aus
`-api-key-file`) und `API_KEY_RESTRICTED` (lib.js, Schlüssel des
Rollennutzers `viewer` aus einem zweiten Seed-Lauf mit `-api-key-role
viewer`; `params(name, { key: 'restricted' })` wählt ihn, fehlt er, bricht
der Lauf mit `fail` ab). Die Parameter von `phase2.js` stehen im Abschnitt
„Phase 2“.

### NFR-01-Volumina (WP-439)

NFR-01 nennt je Organisation 50.000 Interfaces, 200.000 IP-Adressen und
100.000 Beziehungen sowie 500 Organisationen je SaaS-Instanz (Phase 1). Der
Seed erzeugt sie mit vier Optionen für die Demo-Organisation; acme und die
kleinen Organisationen behalten den SIM-01-Datenbestand:

| Option | Standard | Grenzen | NFR-01 bei 10.000 Objekten | Wirkung |
|---|---|---|---|---|
| `-interfaces-per-ci` | 1 | 1–10 | 5 (50.000) | `eth0` (Management-Interface wie bisher) und `eth1` bis `eth<n-1>` je CI |
| `-ips-per-ci` | 1 | 1–40 | 20 (200.000) | die Management-Adresse auf `eth0`, die übrigen reihum auf den Interfaces, eindeutig im Subnetz „Server networks“ `100.64.0.0/10` der Standard-VRF |
| `-relationships-per-ci` | 0 | 0 oder 2–20 | 10 (100.000) | füllt die SIM-01-Beziehungen (LLDP, Strom, Uplink, etwa 2 je CI) mit `depends_on` zwischen Servern auf `n · objects` auf; Switches und PDUs bleiben unberührt, der Impact des Core-Switches umfasst weiter alle CIs außer ihm und den PDUs (`api.js`) |
| `-orgs` | 0 | 0–5.000 | 500 | kleine Organisationen `org-0001` bis `org-<n>` mit dem SIM-01-Datenbestand von acme (40 CIs, Attribut mit Index, Collector, Reviews, Webhook) |

Alle Zeilen haben deterministische IDs; `eth0` und die Management-Adresse
behalten die IDs des SIM-01-Seeds, sodass `ingest.js` (`rediscover`) auch
gegen einen Volumen-Seed passt. Ein zweiter Lauf ändert nichts.
`backend/cmd/seed/seed_integration_test.go` (`TestSeedNFR01Volumes`,
`TestSeedOptionsAreBounded`) prüft Mengen, Verteilung, Determinismus und
Grenzen.

```sh
go run ./backend/cmd/seed -database-url "$DATABASE_URL" -objects 10000 \
  -interfaces-per-ci 5 -ips-per-ci 20 -relationships-per-ci 10 -orgs 500 \
  -api-key-file /tmp/load.key
```

Lokal (geteilter Host mit 4 CPUs, TimescaleDB 2.31.0) dauerte dieser Seed
5 min 40 s, davon etwa 1 min 24 s für die Demo-Organisation und der Rest für
die 500 kleinen Organisationen (je eine Transaktion, Suchindex und
Attributindex); der Seed mit 10.000 Objekten ohne Volumina 15 s. Der Job
`nfr01-volume` (unten) misst die Seed-Dauer auf dem Runner
(`seed-nfr01-volume.txt`).

### Phase-2-Last-Seed (WP-821, ABN-02)

NFR-01 zählt Objekte als CI + Asset; die Budgets gelten bei 10.000 Objekten
je Organisation, 50.000 sind Headroom ohne Budgetzusage (E-17). Drei
Optionen skalieren die Phase-2-Objekte der Demo-Organisation; acme und die
kleinen Organisationen behalten ihre Mengen:

| Option | Standard (SIM-01) | Grenzen | Wirkung |
|---|---|---|---|
| `-assets` | 60 | 60 bis 50.000 − `-objects` | weitere Assets reihum in den Bins des Lagers hq, ohne CI-Verknüpfung, mit den AST-01-Feldern, Lieferant und Vertrag wie die Lager-Assets aus WP-819 |
| `-consumables` | 20 | 20–50.000 | weitere Mengenartikel mit je einer `receipt`-Buchung über den Bewegungsdienst (`stock_level.on_hand` = Summe der Buchungen) |
| `-tickets` | 10 | 10–50.000 | weitere Tickets (reihum `new`, `open`, `in_progress`, `waiting` und die vier Prioritäten) auf [B]-CIs, Fristen aus der Kalenderberechnung des SLA-Pakets im Seed-Kalender |

Die Mengen über SIM-01 entstehen mengenbasiert mit deterministischen IDs
(`<prefix>demo/asset/<n>`, `…/consumable/<n>`, `…/ticket/<n>`); die
Phase-2-Objekte ändern den Graphen des Core-Switches nicht (`IMPACT_ALL =
OBJECTS - 3`). Ein nicht gesetztes `-assets` zählt nicht gegen die Grenze,
`-objects 50000` allein bleibt zulässig. Unabhängig von den Optionen
schreibt der Seed für jedes [B]-CI der Demo-Organisation `hostname`,
`serial_number` und `management_ip` als Feldwerte der Quelle `snmp` in
`ci_field_value` (gleich den Spalten, `observed_at` = `-at`) und für jedes
20. CI einen geschützten Override auf `hostname` mit dem Spaltenwert; damit
misst der Override-Abgleich der Knotenlesung gegen Provenienzzeilen
(`docs/acceptance/lasttest-g1.md`, offener Punkt 10). `-api-key-role` stellt
den Lasttest-Schlüssel für den Nutzer einer Standardrolle aus
(`load-test-<role>`), damit Feldrechte und Layouts mitgemessen werden.
Beispiel mit 5.000 CIs und 5.000 Assets (10.000 Objekte nach E-17):

```sh
go run ./backend/cmd/seed -database-url "$DATABASE_URL" -objects 5000 \
  -assets 5000 -consumables 2000 -tickets 5000 -api-key-role engineer \
  -api-key-file /tmp/load-engineer.key
```

Lokal (ohne TimescaleDB, ohne Last) dauerte dieser Seed etwa 25 s. Die
Mengen des Phase-2-Lastlaufs legen WP-822 (Standardeingaben in `load.yml`,
Abschnitt „Phase 2“) und WP-327 (Headroom-Läufe) fest. `TestSeedScaled` und
`TestSeedOptionsAreBounded` in `backend/cmd/seed/seed_integration_test.go`
prüfen Mengen, Buchungsinvariante, Feldwerte, Beziehungszahl, Grenzen und
den zweiten Lauf.

### Phase 2 (WP-822, ABN-02)

`phase2.js` misst die Listen, Suche, Filter, Beziehungssuche und
Bulk-Operationen der Phase 2 gegen den Phase-2-Last-Seed; jede Anfrageart
ist ein eigenes Szenario mit gleichnamigem Name-Tag und dem Budget aus
NFR-02 als Schwelle:

| Skript | Prüft | Budget (p95) |
|---|---|---|
| `phase2.js` `asset_list` | `GET /assets?limit=50` mit `total` (K23) | < 500 ms |
| `phase2.js` `asset_list_restricted` | dieselbe Liste mit dem Schlüssel des Rollennutzers `viewer` (`API_KEY_RESTRICTED`); Check: keine Zeile enthält `purchase_price` oder `cost_center` (Feldrechte-Grants aus WP-819, Projektion aus WP-271) | < 500 ms |
| `phase2.js` `ticket_list` | `GET /tickets?limit=50` mit `total` (mit der Sichtbarkeitsprüfung der Tickets) | < 500 ms |
| `phase2.js` `consumable_list` | `GET /consumables?limit=50` mit `total` | < 500 ms |
| `phase2.js` `search_fields` | globale Suche `GET /search?q=…&limit=5` über die Felder aus WP-308: Inventarnummer (Stichprobe der Asset-Liste), Management-IP und MAC von `eth0` (zufällige Seed-CIs, `lib.js` `seedDevice`), den Tag `loadphase2` und das Custom-Feld `cost_center`; Check: mindestens ein Treffer | < 500 ms |
| `phase2.js` `ci_attr_filter` | `GET /cis?limit=50` im Wechsel mit dem Bereich `filter[attributes.cost_center][gte]=CC-…&…[lte]=CC-…` (drei Werte; den Gleichheitsfilter misst `attr_filter` in `api.js`) und mit dem berechneten Attribut `filter[attributes.warranty_days_left][lte]=30` (WP-718) | < 500 ms |
| `phase2.js` `ci_tag_filter` | `GET /cis?limit=50&filter[tags][in]=loadphase2,archived` (WP-281; 500 CIs mit dem Tag) | < 500 ms |
| `phase2.js` `ast_asset_filter` | `POST /search/query?limit=50` im Wechsel: Asset-Abfrage mit `where` auf `manufacturer` und `cost_center`, CI-Abfrage mit `where` auf `asset.manufacturer` und dem berechneten Attribut `attributes.warranty_days_left` (WP-309) | < 500 ms |
| `phase2.js` `view_presets` | `filter_spec` (mit `sort` und `columns`) der Systemviews `warranty_expiring`, `servers_without_monitoring`, `my_devices_ci` und `my_devices_asset` aus `GET /saved-views/presets` über `POST /search/query?limit=50` (WP-307) | < 500 ms |
| `phase2.js` `relation_search` | `POST /search/query?limit=50` mit `downstream_of` oder `upstream_of` (Tiefe `MaxRelationshipDepth` = 5) am Core-Switch `sw-00001` (Worst Case: `upstream_of` erreicht etwa die Hälfte der CIs) und am Server `srv-04242` | < 500 ms |
| `phase2.js` `bulk_assets` | `POST /assets/bulk` `action=set_fields` (`notes`) auf 500 Assets bis Job-Ende über `GET /jobs/{id}` (WP-781), `bulk_assets_500_duration` | < 30 s |
| `phase2.js` `bulk_cis_ops` | `POST /cis/bulk` `action=add_tags` (je Job ein anderer Tag `loadbulk<n>`) auf 500 CIs bis Job-Ende (WP-304), `bulk_cis_ops_500_duration` | < 30 s |

Die zehn Lese-Szenarien laufen mit je `VUS` (Standard 3) VUs für
`DURATION`, die Bulk-Szenarien mit einer VU und drei Jobs nacheinander;
den Statuswechsel per Bulk misst weiter `api.js` (`bulk`). Wie in `api.js`
gilt `http_req_failed: rate==0` für den Lauf und je Szenario, jeder
Bulk-Job muss gelingen (`bulk_assets_jobs_succeeded` und
`bulk_cis_ops_jobs_succeeded` `count==3`, `checks{scenario:bulk_assets}`
und `checks{scenario:bulk_cis_ops}` `rate==1`), die Bulk-Dauern erfassen nur
gelungene Jobs, `checks: rate>0.99`. `setup()` bricht mit Hinweis auf
`RETICORA_MODULES` ab, wenn `/assets`, `/tickets` oder `/consumables` nicht
200 liefert, ebenso ohne `API_KEY_RESTRICTED`, mit weniger als 500 Assets
oder 1.000 CIs oder wenn die Suche für einen der Feldtypen nichts findet
(Suchindex nicht neu aufgebaut). Es sammelt die IDs über `next_cursor`, legt
die Tags `loadphase2` und `loadbulk1` bis `loadbulk3` im Tag-Katalog an,
soweit sie fehlen (der Seed hat keine Tags), und setzt `loadphase2` per
Bulk `add_tags` auf die ersten 500 CIs nach Name; `bulk_cis_ops` schreibt
die nächsten 500. GraphQL misst der Lauf nicht: Der BFF lehnt API-Schlüssel
ab.

Parameter: `DURATION`, `VUS`, `OBJECTS` (CIs des Seeds, für die
Stichproben von IP und MAC), `ATTR_KEY` und `ATTR_VALUES` wie in `api.js`,
`API_KEY` und `API_KEY_RESTRICTED`.

Ablauf (Schritte 1 bis 3 oben mit dem Phase-2-Last-Seed und
`RETICORA_MODULES=P2`):

```sh
# Seed mit den Mengen des Jobs phase2 (Standardeingaben von load.yml) und
# zweiter Lauf mit denselben Optionen für den Schlüssel von viewer: er
# ändert keine Zeile und stellt load-test-viewer neben load-test aus
go run ./backend/cmd/seed -database-url "$DATABASE_URL" -objects 10000 \
  -assets 10000 -tickets 10000 -consumables 10000 -api-key-file /tmp/load.key
go run ./backend/cmd/seed -database-url "$DATABASE_URL" -objects 10000 \
  -assets 10000 -tickets 10000 -consumables 10000 -api-key-role viewer \
  -api-key-file /tmp/load-viewer.key
RETICORA_RATE_LIMIT_RPM=1000000 RETICORA_RATE_LIMIT_ORG_RPM=1000000 RETICORA_MODULES=P2 ./server
# Suchindex neu aufbauen (Job abwarten), ANALYZE, dann
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) \
  -e API_KEY_RESTRICTED=$(cat /tmp/load-viewer.key) -e OBJECTS=10000 tests/load/phase2.js
psql "$DATABASE_URL" -At -F '|' -f tests/integrity-check.sql
```

Mit 10.000 CIs und 10.000 Assets misst der Lauf 20.000 Objekte nach NFR-01,
konservativ über der Messbasis von E-17 (10.000 Objekte); die
Zusammensetzung steht im Bericht von WP-327. Gegen einen lokalen Server
(ohne TimescaleDB und NATS, 5.000 CIs, je 1.000 Assets, Tickets und
Mengenartikel) lieferten `setup()` und jede Szenariofunktion nur
200/201/202, kein Check scheiterte; die Messung selbst ist der Job
`phase2`.

### Massen-Schreibjobs parallel zum Ingest (WP-823, ABN-02, NFR-03)

`writes.js` misst die Massen-Schreibjobs der Phase 2, während `ingest.js`
(`SCENARIO=load`, 200 Records/s) parallel läuft. Das Szenario wählt
`SCENARIO` wie in `ingest.js`; je Lauf gibt es genau ein k6-Szenario. Die
Vorbereitung ist ein eigener k6-Lauf mit `PHASE=prepare` (nicht gemessen,
vor dem Start des Ingest), die Messung der Lauf ohne `PHASE` (`measure`).
Die Dauern sind Messwerte ohne Budget (Bericht von WP-327); eine Dauer
zählt nur, wenn alle Jobs des Laufs gelungen sind.

| Skript | Prüft | Messwert |
|---|---|---|
| `writes.js` `import` | Vorbereitung: Importprofil `load-writes` (`POST /import-profiles`, Entität `ci`, Format `json`, Identität `serial_number`). Gemessen: `IMPORT_ROWS` (10.000) neue Server-Zeilen über `POST /imports` mit `rows` und `profile_id` in Aufrufen zu höchstens 5.000 Zeilen (E-49), Modus `create_missing`, bis beide Jobs geendet haben (`GET /jobs/{id}`); danach dieselben Zeilen (Identität über die Seriennummer, anderes `model`, damit jedes CI geschrieben wird) mit neuem `Idempotency-Key` und `mode=update_existing` ohne manuelles `ANALYZE` (Punkt 8 von `lasttest-g1.md`). Checks: jeder Job `succeeded`, `GET /imports/{id}` meldet jede Zeile gelesen und keine fehlerhaft; `import_jobs_succeeded` `count==4` | `import_10000_duration` (Ziel < 5 min), `import_update_duration` |
| `writes.js` `template` | Vorbereitung: globales Attribut `load_template_tier`, Template `load_writes` des Typs `server` mit Version 1 (das Attribut) und Version 2 (das Attribut mit Default `migrated`), beide veröffentlicht, und Version 1 auf jedem Server des Seeds (`srv-*`, `PUT /cis/{id}/templates`, `ASSIGN_PARALLEL` gleichzeitig). Gemessen: `POST /templates/{id}/migrations` von Version 1 auf 2 mit `where` auf diese CIs bis Job-Ende; `totals.migrated` aus `GET /jobs/{id}/result` muss jeder Server des Seeds sein (`template_migrated`); `template_jobs_succeeded` `count==1` | `template_migrate_duration` |
| `writes.js` `computed` | Gemessen: `POST /attribute-definitions` legt das globale berechnete Attribut `load_asset_age_days` an (Tage vom Kaufdatum des verknüpften Assets bis heute, wie `warranty_days_left`), danach läuft der Job `attribute.computed_backfill` über alle CIs bis Job-Ende (`GET /jobs?kind=attribute.computed_backfill`, der Job der Definition); `computed_jobs_succeeded` `count==1`. Die Vorbereitung prüft nur, dass das Attribut noch nicht existiert | `computed_backfill_duration` |

Abweichend vom Plan ordnet die Vorbereitung von `template` die Templates mit
`PUT /cis/{id}/templates` zu: `POST /cis/bulk` mit `change_template`
(WP-780) migriert oder ersetzt nur Templates, die ein CI schon hält, und die
Templates des Seeds gehören zu den abgeleiteten Typen `blade_chassis` und
`blade_server`, außerhalb der Typkette von `server`. Der Typ `server` ist
ein Systemtyp ohne Attribute der Organisation, das Attribut des Templates
ist daher global. Der Dateiimport (CSV/XLSX) über Dokumente bräuchte clamd
und wird nicht gemessen; der Import über JSON-Zeilen legt die Zeilen im
Objektspeicher ab (WP-303), die Jobs starten deshalb MinIO.

Wie in den anderen Skripten gilt `http_req_failed: rate==0` für den Lauf
und das Szenario, dazu `checks: rate==1`. Der parallele Ingest behält die
Schwellen von `ingest.js` (`ingest_visible_delay` < 30 s,
`ingest_records_missing` = 0, NFR-03); der Job scheitert, wenn einer der
beiden Läufe scheitert. Jeder Ingest-Record legt ein neues CI an: 120 s
ergeben 24.000 CIs, mit Seed und Import etwa 44.000 CIs (300 s ergäben mit
Seed, Import und Assets mehr als 80.000 Objekte, weit über dem Headroom von
50.000 nach NFR-01). Dauert der Schreibjob länger als der Ingest, steht das
als Messwert im Bericht von WP-327.

Parameter: `SCENARIO` (`import`, `template`, `computed`), `PHASE`
(`prepare` oder `measure`), `OBJECTS` (CIs des Seeds; Server sind CI 5 bis
`OBJECTS`), `IMPORT_ROWS` (10.000), `ASSIGN_PARALLEL` (20), `WAIT_S`
(1.800, Wartezeit je Job), `RUN_ID` (Präfix der Importzeilen), `API_KEY`.

Ablauf (Schritte 1 bis 3 oben mit dem Phase-2-Last-Seed, `RETICORA_MODULES=P2`
und Objektspeicher, z. B. MinIO mit `RETICORA_S3_ENDPOINT=localhost:9000`,
`RETICORA_S3_ACCESS_KEY` und `RETICORA_S3_SECRET_KEY`), je Szenario mit
frischer Datenbank:

```sh
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) -e OBJECTS=10000 \
  -e SCENARIO=import -e PHASE=prepare tests/load/writes.js
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) -e OBJECTS=10000 \
  -e SCENARIO=load -e DURATION_S=120 --summary-export import-ingest.json tests/load/ingest.js &
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) -e OBJECTS=10000 \
  -e SCENARIO=import --summary-export import.json tests/load/writes.js
wait
psql "$DATABASE_URL" -At -F '|' -f tests/integrity-check.sql
```

Gegen einen lokalen Server (ohne TimescaleDB und NATS, Objektspeicher als
S3-Attrappe, 3.000 CIs, 1.000 Assets) liefen Vorbereitung und Messung jedes
Szenarios über eine k6-Attrappe (curl) ohne fehlgeschlagene Anfrage oder
Check, `import` auch mit parallelem Ingest; die Messung selbst sind die Jobs
`import`, `template` und `computed`.

### Workflow

`.github/workflows/load.yml` (manuell und nächtlich) führt diese Schritte aus:
`api.js`, `ingest.js` (`load`), `ingest.js` (`spool`), `ingest.js`
(`rediscover`), `api.js` auf den NFR-01-Volumina, `phase2.js` und
`writes.js` als getrennte Jobs einer Matrix (`api`, `ingest`, `ingest-spool`,
`ingest-rediscover`, `nfr01-volume`, `api-large-28000`, `api-large-50000`,
`phase2`, `import`, `template`, `computed`), jeder mit frischer Datenbank, Seed, neu aufgebautem Suchindex
(der Schritt wartet, bis der Job `search.reindex` gelungen ist),
`ANALYZE` und einem Server mit 4 Spuren (Standard) und
`RETICORA_MODULES=P2` (Release-Konfiguration G2: alle Module der Phase P2
aus `LateModules`, nach E-105 einschließlich `metamodel` und `import`; WP-822).
Jeder Job seedet `objects` CIs und die Phase-2-Mengen `assets`, `tickets` und
`consumables` (WP-821); nur `api-large-50000` behält die SIM-01-Mengen, weil
der Seed höchstens 50.000 CIs und Assets zusammen anlegt. `OBJECTS` bleibt
die CI-Zahl: `api` läuft mit unveränderten Schwellen gegen den erweiterten
Seed, die Phase-2-Objekte hängen nicht am Graphen des Core-Switches. Der Job
`phase2` seedet ein zweites Mal mit denselben Optionen und `-api-key-role
viewer` und übergibt diesen Schlüssel als `API_KEY_RESTRICTED`. Der Schritt
„Seeded volumes (NFR-01)“ zählt nach dem Seed CIs, Interfaces, Adressen und
Beziehungen der Demo-Organisation, die Organisationen und die Assets,
Tickets und Mengenartikel der Demo-Organisation (`volumes-<job>.txt`); im
Job `nfr01-volume` scheitert er unter 10.000, 50.000, 200.000, 100.000 bzw.
500. Die Jobs `import`, `template` und `computed` (WP-823) starten zusätzlich
MinIO und den Server mit `RETICORA_S3_*`, führen im k6-Schritt erst die
Vorbereitung (`PHASE=prepare`) und dann `ingest.js` (`load`,
`INGEST_DURATION_S` = 120 s) im Hintergrund neben `writes.js` aus; der
Schritt scheitert, wenn einer der Läufe scheitert. Danach zählt „Size of the
organization at the end“ CIs und Assets der Demo-Organisation
(`size-<job>.txt`). Nach dem k6-Lauf prüft ein eigener
Schritt die Daten mit `tests/integrity-check.sql`; eine Verletzung lässt den
Job scheitern (ABN-03, F-281). Eingaben:

- `objects` (`OBJECTS`) und `duration` (`DURATION`, `api.js`, `phase2.js`);
- `assets`, `tickets` und `consumables`: die Phase-2-Mengen des Seeds
  (`-assets`, `-tickets`, `-consumables`, Standard je 10.000; WP-822);
- `scenarios`: die Jobs, kommagetrennt, oder `all` (Standard);
- `server_ref`: Commit, Tag oder Branch, auch mit Elternsuffix (`<sha>^`),
  aus dem Server, Seed und Migrationen gebaut werden (Standard: der
  Lauf-Commit). `tests/load/*` und `tests/integrity-check.sql` stammen immer
  aus dem Lauf-Commit. Der Job `prepare` löst die Referenz mit
  `tests/load/resolve-server-ref.sh` auf (Selbsttest
  `tests/load/resolve-server-ref.test.sh`); eine ungültige Referenz bricht den
  Lauf dort mit einer Fehlermeldung ab, bevor ein Job baut oder seedet. Der
  Seed eines Commits vor WP-437 legt das Attribut `cost_center` nicht an;
  `api.js` ist mit einem solchen `server_ref` nicht aussagekräftig, der
  Vergleich läuft nur mit `ingest-rediscover`. Der Seed eines Commits vor
  WP-439 kennt die Volumen-Optionen nicht; `nfr01-volume` scheitert damit
  im Schritt „Seed“. Vor WP-821 kennt der Seed `-assets`, `-tickets` und
  `-consumables` nicht, vor WP-325 `-api-key-role` nicht; jeder Job außer
  `api-large-50000` bzw. der Job `phase2` scheitert damit im Schritt
  „Seed“.

Jeder Job lädt seine Ergebnisse als eigenes Artefakt hoch
(`load-results-api`, `load-results-ingest`, `load-results-ingest-spool`,
`load-results-ingest-rediscover`, `load-results-nfr01-volume`,
`load-results-api-large-28000`, `load-results-api-large-50000`,
`load-results-phase2`, `load-results-import`, `load-results-template`,
`load-results-computed`); der Job `archive` übernimmt jeden ausgewählten Job
ins Archiv. Die Artefakte enthalten die
k6-Zusammenfassung (`<job>.json`, `<job>.txt`), die Integritätsprüfung
(`integrity-<job>.txt`), die Datenbankkosten (`statements-<job>.txt`), die
Seed-Dauer (`seed-<job>.txt`), den Datenbestand nach dem Seed
(`volumes-<job>.txt`), das Laufprotokoll (`run-<job>.txt`: Lauf- und
Server-Commit, Lauf-URL, Skript, Szenario,
`objects`, `duration`, Seed-Optionen, Phase-2-Mengen, Module, Spurzahl, Rate-Limits, Versionen von
k6, Go, PostgreSQL und TimescaleDB, Container-Images, vollständiges `lscpu`,
`/proc/meminfo` und `free -h`), das Ergebnis des Jobs und seiner Schritte
(`result-<job>.txt`), `/readyz`, das Server-Log, die k6-Konsole
(`console-<job>.log`) und die fehlgeschlagenen Anfragen je Name und Status
(`failed-<job>.txt`, auch in der Zusammenfassung des Jobs); die `api.js`-Jobs
zusätzlich die Pläne langsamer Statements und das CPU-Profil des Servers
(siehe Ursachenanalyse NFR-02), die Jobs von `writes.js` zusätzlich die
Zusammenfassung, Ausgabe, Konsole und fehlgeschlagenen Anfragen des
parallelen Ingest (`<job>-ingest.json`, `<job>-ingest.txt`,
`console-<job>-ingest.log`, `failed-<job>-ingest.txt`), Ausgabe und Konsole
der Vorbereitung (`<job>-prepare.txt`, `console-<job>-prepare.log`; deren
fehlgeschlagene Anfragen zählen in `failed-<job>.txt` mit) und die Größe der
Organisation am Jobende (`size-<job>.txt`). Das Ergebnis des G1-Laufs steht in
`docs/acceptance/lasttest-g1.md`.

### Archiv eines Laufs (WP-439, E-58)

Der Job `archive` läuft nach allen Lastjobs, auch wenn einer rot ist, und
legt mit `tests/load/archive-run.sh` (Selbsttest
`tests/load/archive-run.test.sh` im Job `prepare`) ein Artefakt
`load-archive-<Lauf-ID>` an. Es enthält genau das Verzeichnis
`<Lauf-ID>/`, das unter `docs/acceptance/artefakte/lasttest/` archiviert
wird:

- `<Lauf-ID>/<job>/` je ausgewähltem Job: `<job>.json` (k6
  `--summary-export`), `<job>.txt` (k6-Ausgabe ohne die Fortschrittszeilen),
  `run-<job>.txt`, `result-<job>.txt`, `seed-<job>.txt`, `volumes-<job>.txt`,
  `integrity-<job>.txt`, `statements-<job>.txt` und, soweit vorhanden,
  `slow-plans-<job>.txt`, `cpu-<job>.txt`, `readyz-<job>.json`,
  `failed-<job>.txt` und für die Jobs von `writes.js` `<job>-ingest.json`,
  `<job>-ingest.txt` (ohne Fortschrittszeilen), `failed-<job>-ingest.txt`
  und `size-<job>.txt` (WP-823). Server-
  und PostgreSQL-Logs und das binäre CPU-Profil bleiben in den
  Job-Artefakten des Laufs.
- `<Lauf-ID>/ergebnis.md`: Lauf (Link, Versuch, Ereignis, Branch), Lauf- und
  Server-Commit, Eingaben, ob jeder ausgewählte Job ein Artefakt hat
  („Vollständig“) und ob alle grün sind, je Job Status (Seed, Volumina, k6,
  Integritätsprüfung), CPU, RAM, Konfiguration, Datenbestand, die
  fehlgeschlagenen Anfragen je Name und Status und jede k6-Schwelle mit
  Messwert und Ergebnis; für die Jobs von `writes.js` auch die
  fehlgeschlagenen Anfragen, Schwellen und Messgrößen des parallelen Ingest
  und die Größe am Jobende. Dieselbe Übersicht steht in der
  Zusammenfassung des Laufs auf GitHub.
- `<Lauf-ID>/SHA256SUMS`: Prüfsummen aller Dateien des Verzeichnisses.

Übernahme in das Repository (GitHub CLI, angemeldet; die Lauf-ID steht in
der URL des Laufs):

```sh
run=<Lauf-ID>
gh run download "$run" -R DataHub-Chiemgau/Reticora-CMDB -n "load-archive-$run" \
  -D docs/acceptance/artefakte/lasttest
(cd "docs/acceptance/artefakte/lasttest/$run" && sha256sum -c SHA256SUMS)
```

Ohne GitHub CLI: Artefakt `load-archive-<Lauf-ID>` auf der Seite des Laufs
laden und nach `docs/acceptance/artefakte/lasttest/` entpacken. Danach die
Werte aus `ergebnis.md` in `docs/acceptance/lasttest-g1.md` übernehmen
(Platzhalter `⟨LAUF-n…⟩`). Als „vollständiger Lauf“ zählt ein Lauf mit
`scenarios=all` im ersten Versuch, dessen `ergebnis.md` „Vollständig: ja“
meldet; „exit 0“ heißt „Ergebnis: alle Jobs grün“. Für den G1-Nachweis gilt
statt „drei Läufe mit exit 0“ die Bewertung über drei Läufe (nächster
Abschnitt).

### Bewertung über drei Läufe (WP-439, E-58)

Nachtrag des Auftraggebers zu E-58 vom 2026-10-26: Der Lastnachweis für G1
(WP-439, AK 3) wird über die drei Läufe des Nachweises mit dem Median
bewertet; das ersetzt „drei aufeinanderfolgende Läufe mit exit 0“. Jeder
Lauf wird weiter wie oben archiviert.

- **Budget:** Eine k6-Schwelle gilt als gehalten, wenn der Median ihrer
  Messwerte über die drei Läufe sie einhält. Messwert eines Laufs ist der
  Wert, den seine k6-Zusammenfassung (`<job>.json`) für die Kenngröße der
  Schwelle meldet: bei `p(95)<500` der p95 des Laufs, bei `max<…`,
  `count==…` und `value>=…` Maximum, Zähler bzw. Wert; `rate` ist bei einer
  Rate-Metrik (`checks`, `http_req_failed`) ihr Anteil, bei einem Zähler
  seine Rate je Sekunde. Zeiten in ms.
- **Job:** Ein Job gilt als gescheitert, wenn er in mindestens zwei der drei
  Läufe rot ist (`result-<job>.txt` ohne `status: success`). Ein Job ohne
  Artefakt zählt in seinem Lauf als rot.
- **Fehlender Messwert** (Job ohne Artefakt oder ohne k6-Zusammenfassung,
  Kenngröße nicht exportiert): zählt als verfehlt. Der Median ist dann der
  schlechtere der beiden anderen Werte; fehlen zwei, gibt es keinen Median
  und die Schwelle gilt als verfehlt.

`tests/load/evaluate-runs.sh` (Selbsttest `tests/load/evaluate-runs.test.sh`
im Job `prepare`) wendet die Regel auf genau drei Archive an:

```sh
a=docs/acceptance/artefakte/lasttest
tests/load/evaluate-runs.sh "$a/<Lauf-ID-1>" "$a/<Lauf-ID-2>" "$a/<Lauf-ID-3>"
```

**Voraussetzungen des Nachweises** (WP-206/WP-612 „Drei vollständige
Läufe“, NFR-10, F-417): Die drei Läufe nennen in `ergebnis.md` unter
„Server, Seed und Migrationen“ denselben Commit, und jeder Lauf hat jeden
der fünf Jobs `api`, `ingest`, `ingest-spool`, `ingest-rediscover` und
`nfr01-volume` (in der Zeile „Jobs“ genannt und mit seinem Verzeichnis im
Archiv, also `scenarios=all`). Sonst sind die Läufe **nicht bewertbar**:
Die letzte Zeile lautet „Bewertung nach E-58 (Median über drei Läufe):
nicht bewertbar: <Gründe>“ (verschiedene oder fehlende Server-Commits mit
Lauf-ID und Commit, je fehlendem Job die Läufe ohne ihn), jeder Grund steht
zusätzlich auf stderr, Exit 1. Die Tabellen und Mediane stehen trotzdem im
Bericht.

Bewertet werden die fünf Jobs und alle weiteren, die eines der drei
`ergebnis.md` nennt. Die Ausgabe (Markdown) nennt die drei Läufe (Link,
Server-Commit, Eingaben, Vollständigkeit und Ergebnis aus `ergebnis.md`),
die Voraussetzungen und ob sie erfüllt sind, je Job jede Schwelle mit dem
Messwert je Lauf, dem Median und „gehalten“ ja/nein, den Status des Jobs je
Lauf („rot in n von 3“) und das Urteil des Jobs, zuletzt die Zeile
„Bewertung nach E-58 (Median über drei Läufe): alle Budgets gehalten“ bzw.
„… verfehlt: <Liste>“ (verfehlte Schwellen und Jobs, die in zwei oder drei
Läufen rot sind) bzw. „… nicht bewertbar: …“. Exit 0 nur, wenn die
Voraussetzungen erfüllt sind, jede Schwelle jedes Jobs im Median gehalten
und kein Job in mindestens zwei Läufen rot ist, sonst 1; falsche Argumente
(nicht genau drei Archive, ein Verzeichnis ohne `ergebnis.md`, derselbe
Lauf zweimal, eine unlesbare k6-Zusammenfassung, kein Job) Exit 2.

**Teilbewertung** (`tests/load/evaluate-runs.sh --partial <Archiv-1>
<Archiv-2> <Archiv-3>`): eigener Modus zur Diagnose, **kein G1-Nachweis**.
Bewertet werden nur die Jobs, die einer der drei Läufe nennt oder als
Verzeichnis hat (etwa drei Läufe mit `scenarios=nfr01-volume`); ein Job, der
in einem Lauf fehlt, zählt dort als rot (Regel oben). Voraussetzung ist
auch hier derselbe Server-Commit in allen drei Läufen. Die letzte Zeile
beginnt mit „Teilbewertung nach E-58 (Median über drei Läufe, kein
G1-Nachweis)“, nie mit „Bewertung nach E-58“; Exit wie oben.
`load-evidence.yml` ruft das Skript immer ohne `--partial` auf.

In GitHub Actions macht das der Workflow `load-evidence.yml` (WP-612,
manuell, Eingaben `run_1` bis `run_3`: die Lauf-IDs der drei
`load.yml`-Läufe). Er lädt die Artefakte `load-archive-<Lauf-ID>` der drei
Läufe (`actions/download-artifact` mit `run-id`), ruft
`tests/load/evaluate-runs.sh` auf und schreibt die Bewertung in Job-Log,
Zusammenfassung und das Artefakt `load-evidence-<eigene Lauf-ID>`
(`bewertung.md`, auch bei verfehlten Budgets). Der Job endet wie das
Skript; eine Lauf-ID, die keine Zahl ist oder doppelt vorkommt, beendet ihn
vor dem Download mit Exit 2, ein Lauf ohne Archiv (abgelaufen oder ohne Job
`archive`) im Download-Schritt.

```sh
gh workflow run load-evidence.yml -R DataHub-Chiemgau/Reticora-CMDB \
  -f run_1=<Lauf-ID-1> -f run_2=<Lauf-ID-2> -f run_3=<Lauf-ID-3>
```

### Datenbankkosten je Record (F-118)

PostgreSQL läuft im Workflow mit `pg_stat_statements`; die Statistik wird
direkt vor dem k6-Lauf zurückgesetzt. Danach schreibt
`tests/load/statement-costs.sql` die 25 Statements mit der meisten
Ausführungszeit (Aufrufe je Record, mittlere Zeit, Puffer und Zeilen je
Aufruf, Kosten des generischen Plans als `reticora_app` unter RLS und die
Relationen, die er sequenziell liest) und die Summen je Record:
Ausführungszeit, Puffer und Plankosten (Aufrufe × Kosten der aufgeführten
Statements) geteilt durch `ingest_records_accepted`. Die Summen enthalten die
Abfragen, mit denen der Lauf die Sichtbarkeit prüft (`visible-poll`,
`probe-poll`); sie stehen als eigene Statements in der Liste. Bei `api.js`
(keine Records) bleibt es bei der Liste.

### Ursachenanalyse NFR-02 (F-117, WP-438)

Jeder `api.js`-Job zeichnet zusätzlich auf, was die Ursachenbestätigung von
F-117 braucht:

- **pg_stat_statements:** `statements-api.txt` (siehe oben) nennt je
  Statement auch die Gesamtzeit und ihren Anteil an der Ausführungszeit des
  Laufs (`total ms`, `% of time`), etwa Seite, `COUNT` und Overrides von
  `GET /cis` oder die Abfrage der globalen Suche.
- **auto_explain:** PostgreSQL lädt `auto_explain`; der Schritt vor k6
  schaltet es ein (`log_min_duration` 100 ms, mit tatsächlichen Zeilen und
  Puffern, ohne Zeitmessung je Planknoten), der Schritt nach k6 wieder aus.
  `postgres-api.log` enthält die Pläne, `slow-plans-api.txt`
  (`tests/load/slow-plans.sh`, Selbsttest `slow-plans.test.sh` im Job
  `prepare`) fasst sie je Statement zusammen (Ausführungen über 100 ms,
  Summe, längste) und zeigt den Plan der längsten Ausführung, z. B. ob die
  Liste den Index der Standardsortierung liest oder die Organisation
  sortiert und wo RLS-Prädikate Zeilen verwerfen.
- **CPU-Profil des Servers:** Der Server läuft mit
  `RETICORA_PPROF_ADDR=127.0.0.1:6060` (nur Loopback, nur in development;
  die API liefert keine Profile). Während k6 holt der Job das CPU-Profil
  von 10 s nach dem Start bis 10 s vor dem Ende von `DURATION`
  (`cpu-api.pprof`); `cpu-api.txt` (`tests/load/cpu-profile-report.sh`)
  zeigt die Funktionen mit der meisten CPU-Zeit und je Handler der
  Szenarien, wohin seine Zeit geht (Datenbank und Zeilen dekodieren,
  JSON-Serialisierung `api.WriteJSON`, ...).
  Lokal: `go tool pprof -top -cum cpu-api.pprof`.

Lokal ohne Workflow: PostgreSQL mit
`shared_preload_libraries=timescaledb,pg_stat_statements,auto_explain`
starten, den Server mit `RETICORA_PPROF_ADDR=127.0.0.1:6060`, während
`api.js` läuft
`curl -o cpu.pprof 'http://127.0.0.1:6060/debug/pprof/profile?seconds=60'`.

### Wiederentdeckung (F-118, WP-437)

`SCENARIO=rediscover` sendet für jeden Server des Seeds höchstens einen
Record: Seriennummer, Name, Management-IP und die MAC von `eth0` leitet
`lib.js` (`seedDevice`) wie `backend/cmd/seed` aus md5 ab; dazu eine
Chassis-MAC als primäre MAC, `eth1` mit einer zweiten MAC (beide global
verwaltet, REC-02 ignoriert die lokal verwalteten `02:…`-MACs des Seeds) und
den LLDP-Nachbarn auf `eth0`, den Switch, mit dem der Seed den Server
verbindet. Der Seed hält weder die primäre noch die zweite MAC; jede
Ausgangsversion des Seeds erhält damit dieselben Schreibvorgänge
(Fingerprint, `eth1`, bestätigte LLDP-Kante). Bei 200 Records/s und 45 s
nimmt die Last die Server 5 bis 9.004, die Probe-Records die Server von
`OBJECTS` abwärts; reichen die Server nicht, bricht das Skript vor dem Lauf
ab. `setup` prüft, dass die Records zum Seed passen, und zählt die CIs.
Gesehen heißt: `last_seen_at` des CIs (sekundengenau) liegt nach dem Senden;
gezählt werden die Server (`filter[name][like]=srv-*`), die seit dem
Laufbeginn gesehen wurden. `ingest_new_cis` ist die Zahl der CIs, die seit
dem Laufbeginn angelegt wurden (`filter[created_at][gte]`): Die Zahl aller
CIs der Organisation ist ab 10.000 gedeckelt (E-104) und wüchse nicht.

Ursachenbestätigung F-118 (Hypothese: die Identitätssuche mit MACs vor
WP-434 senkt den Durchsatz bei der Wiederentdeckung): `load.yml` per
workflow_dispatch zweimal mit `scenarios=ingest-rediscover` starten, einmal
mit `server_ref` = Elterncommit des Merges von WP-434
(`t4-identity-mac-index`), einmal ohne `server_ref` (der Lauf-Commit). Aus
den Artefakten `load-results-ingest-rediscover` gehören in den PR: die
Lauf-IDs, die Plankosten je Record (`statements-ingest-rediscover.txt`,
Summe und die Identitätsabfragen auf `ci` mit `seq scan on`), der Durchsatz
(`ingest_drain_records_per_s`, `ingest_drain_inclusive_records_per_s`) und
die Verzögerung (`ingest_visible_delay`).

### Ingest (NFR-03, WP-334)

- **Messgrößen.** `ingest_visible_delay` (Probe-Records alle `PROBE_EVERY_S`
  während der Last und die Zeit vom Lastende, bis jeder Record des Laufs
  sichtbar ist), `ingest_drain_records_per_s` (verarbeitete Records ab
  Sendeende je Sekunde: was beim Sendeende noch nicht sichtbar war, geteilt
  durch die Zeit bis alle sichtbar sind; Budget nur im Szenario `spool`),
  `ingest_drain_inclusive_records_per_s` (alle Records durch die Zeit ab dem
  ersten Senden, zur Information), `ingest_spool_flush` (erstes Senden bis
  alles sichtbar), `ingest_backlog_peak` (größter Rückstand laut `/readyz`,
  `details.ingest.total`; der Server liest ihn alle 30 s, `sampled_at`),
  `ingest_records_missing` (Records, die nie
  sichtbar werden), `ingest_records_accepted` (mit 202 angenommene Records
  des Szenarios, zur Information und als Nenner der Datenbankkosten),
  `ingest_new_cis` (`rediscover`: neue CIs, Budget 0). Sichtbar heißt:
  `GET /cis?filter[serial_number][like]=<Lauf>*` zählt den Record
  (`rediscover`: das CI wurde nach dem Senden gesehen, siehe oben). Seit
  WP-839 ist `total` ab 10.000 gedeckelt (E-104, `total_capped`); ein
  gedeckelter Zähler wird nach dem nächsten Zeichen der Seriennummer bzw.
  des Namens aufgeteilt, bis jeder Teil exakt zählt (`tests/load/count.js`,
  Selbsttest `count.test.sh` im Job `prepare`). Vorher blieb `load` mit
  12.000 Records bei 10.000 sichtbaren stehen (`ingest_records_missing` =
  2.000, Läufe 41566049639 und 41760522355).
- **Vollständigkeit.** Alle Szenarien scheitern, sobald ein Record fehlt
  (`ingest_records_missing` = 0): Ist die Queue leer (`/readyz`, eine
  Stichprobe, die nach dem letzten Anstieg genommen wurde) und wächst die
  Zahl sichtbarer Records nicht mehr, wartet der Lauf nicht weiter und
  zählt den Rest als fehlend (Dead Letter, als `unlicensed_ci` gehaltene oder
  verworfene Records). In den Szenarien `load` und `rediscover` muss k6 die
  Rate voll anbieten (`dropped_iterations` = 0); die Sollzahl ist
  `RATE · DURATION_S` (`rediscover`: plus die Probe-Records). Weil k6
  dabei eine Iteration mehr starten kann, fällt eine einzelne abgelehnte
  Anfrage nicht unbedingt als fehlender Record auf: Jede Anfrage muss
  deshalb angenommen werden (`http_req_failed{name:ingest}` bzw.
  `{name:spool}` = 0, also 202); schon eine abgelehnte lässt den Lauf
  scheitern.
- **Spool.** Der Collector liefert seinen Spool nacheinander aus; jede
  Spool-Nachricht ist der vollständige Upload eines Erfassungszyklus
  (`spoolFlushBatch` = 64 zählt Nachrichten je Lesedurchgang, nicht Records).
  Das Szenario sendet `SPOOL_RECORDS` Records in Uploads zu `SPOOL_BATCH`
  Records (gzip, wie der Collector), jeden nach der Antwort des vorigen, mit
  einem `observed_at` eine Stunde zurück.
- **HTTP statt Edge.** k6 nutzt `POST /ingest/bulk` bzw.
  `/discovery/ingest`. Der mTLS-NATS-Kanal der Collectors (COL-08) ruft
  denselben Handler und veröffentlicht in dieselbe Queue.
- **Spuren.** Der Server verteilt den Ingest auf `RETICORA_INGEST_LANES`
  Spuren (Standard 4; `docs/betrieb/ingest-spuren.md`). Für den Vergleich mit
  der seriellen Verarbeitung `RETICORA_INGEST_LANES=1` setzen.
- **Lokaler Lauf (4 vCPU).** PostgreSQL, Server, NATS, Redis und k6 teilen
  sich die Kerne; die Werte sind konservativ. Vor jedem Lauf neu seeden,
  den Suchindex neu aufbauen und `ANALYZE` ausführen, wie `load.yml`: der
  Rückstand eines Laufs
  verfälscht den nächsten, und ohne aktuelle Statistik wählt PostgreSQL für
  die Kandidatenabfragen Pläne über die ganze Organisation.

Ergänzend prüfen die Integrationstests `backend/internal/ci/querybudget_integration_test.go`
und `backend/internal/topology/querybudget_integration_test.go` die Zahl der
Statements je Anfrage (kein N+1) und per `EXPLAIN` die Indexnutzung zentraler
Abfragen (ABN-02).
