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
| `api.js` `bulk` | Bulk-Statuswechsel von 500 CIs bis Job-Ende | < 30 s |
| `ingest.js` `load` | `POST /ingest/bulk` mit `RATE` Records/s für `DURATION_S`; Verzögerung Record → sichtbar (Probe-Records während der Last, danach bis alle Records sichtbar sind) | < 30 s |
| `ingest.js` `spool` | Spool-Flush eines Collectors (COL-05): 10.000 Records als nacheinander gesendete Uploads (`POST /discovery/ingest`, gzip); Abbau-Rate ab Sendeende, Dauer bis alle sichtbar | ≥ 200/s, < 5 min |
| `ingest.js` `rediscover` | Wiederentdeckung der geseedeten Server (F-118): `RATE` SNMP-artige Records/s mit primärer MAC, Interface-MACs und LLDP-Nachbar über `POST /ingest/bulk`; Verzögerung Record → gesehen (`last_seen_at`), kein neues CI, kein fehlender Record | < 30 s, 0 neue CIs, 0 fehlend |

`global_search`, `attr_filter` und `relationships` laufen mit je 2 VUs in
der Mischlast mit (wie `topology` und `impact`); die gleichzeitige Last des
Laufs wächst damit um etwa ein Fünftel (32 auf 38 VUs).

## Ablauf

```sh
# 1. Datenbank migrieren und den Last-Seed mit API-Schlüssel anlegen (mit
#    dem durchsuchbaren Attribut cost_center, seinen Werten und seinem Index)
migrate -path backend/migrations -database "$DATABASE_URL" up
go run ./backend/cmd/seed -database-url "$DATABASE_URL" -objects 10000 -api-key-file /tmp/load.key

# 2. Server starten. Das Rate-Limit (600 Anfragen/min je Principal, API-04)
#    würde den Lastlauf drosseln und wird für den Lauf angehoben.
RETICORA_RATE_LIMIT_RPM=1000000 RETICORA_RATE_LIMIT_ORG_RPM=1000000 ./server

# 3. Suchindex neu aufbauen und die Statistik aktualisieren (ohne ANALYZE
#    hängen die Pläne davon ab, wann Autovacuum den Seed erreicht)
curl -fsS -X POST -H "X-API-Key: $(cat /tmp/load.key)" http://localhost:8080/api/v1/search/reindex
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
Standard `demo` und 10).

### Workflow

`.github/workflows/load.yml` (manuell und nächtlich) führt diese Schritte aus:
`api.js`, `ingest.js` (`load`), `ingest.js` (`spool`) und `ingest.js`
(`rediscover`) als getrennte Jobs einer Matrix (`api`, `ingest`,
`ingest-spool`, `ingest-rediscover`), jeder mit frischer Datenbank, Seed,
neu aufgebautem Suchindex, `ANALYZE` und einem Server mit 4 Spuren
(Standard). Nach dem k6-Lauf prüft ein eigener Schritt die Daten mit
`tests/integrity-check.sql`; eine Verletzung lässt den Job scheitern
(ABN-03, F-281). Eingaben:

- `objects` (`OBJECTS`) und `duration` (`DURATION`, `api.js`);
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
  Vergleich läuft nur mit `ingest-rediscover`.

Jeder Job lädt seine Ergebnisse als eigenes Artefakt hoch
(`load-results-api`, `load-results-ingest`, `load-results-ingest-spool`,
`load-results-ingest-rediscover`): die k6-Zusammenfassung (`<job>.json`,
`<job>.txt`), die Integritätsprüfung (`integrity-<job>.txt`), die
Datenbankkosten (`statements-<job>.txt`), Commit, `server_ref`, CPU und RAM
(`run-<job>.txt`), `/readyz` und das Server-Log. Das Ergebnis des G1-Laufs
steht in `docs/acceptance/lasttest-g1.md`.

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
Laufbeginn gesehen wurden. `ingest_new_cis` ist die Zahl der CIs nach dem
Lauf minus der Zahl davor.

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
  (`rediscover`: das CI wurde nach dem Senden gesehen, siehe oben).
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
