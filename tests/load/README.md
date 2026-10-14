# Lasttest (NFR-10)

k6-Szenarien (E-31) für die Budgets aus NFR-02 und NFR-03 bei der Seed-Größe aus
NFR-10 (SIM-01 skaliert auf 10.000 Objekte). Die Schwellen der Skripte sind die
Budgets: Wird ein Budget verfehlt, schlägt der Lauf fehl.

| Skript | Prüft | Budget (p95) |
|---|---|---|
| `api.js` `list` | `GET /cis?limit=50` | < 500 ms |
| `api.js` `search` | `GET /cis?search=…` | < 500 ms |
| `api.js` `filter` | indizierte Filter `filter[status]`, `filter[hostname]` | < 500 ms |
| `api.js` `detail` | `GET /cis/{id}` | < 300 ms |
| `api.js` `rack` | `GET /racks/{id}/layout` | < 300 ms |
| `api.js` `topology` | Topologie des Core-Switches, Tiefe 1 (> 2.000 Knoten) | < 3 s |
| `api.js` `impact` | Impact des Core-Switches, Tiefe 20 (ganze Organisation) | < 2 s |
| `api.js` `bulk` | Bulk-Statuswechsel von 500 CIs bis Job-Ende | < 30 s |
| `ingest.js` `load` | `POST /ingest/bulk` mit `RATE` Records/s für `DURATION_S`; Verzögerung Record → sichtbar (Probe-Records während der Last, danach bis alle Records sichtbar sind) | < 30 s |
| `ingest.js` `spool` | Spool-Flush eines Collectors (COL-05): 10.000 Records als nacheinander gesendete Uploads (`POST /discovery/ingest`, gzip); Abbau-Rate ab Sendeende, Dauer bis alle sichtbar | ≥ 200/s, < 5 min |

## Ablauf

```sh
# 1. Datenbank migrieren und den Last-Seed mit API-Schlüssel anlegen
migrate -path backend/migrations -database "$DATABASE_URL" up
go run ./backend/cmd/seed -database-url "$DATABASE_URL" -objects 10000 -api-key-file /tmp/load.key

# 2. Server starten. Das Rate-Limit (600 Anfragen/min je Principal, API-04)
#    würde den Lastlauf drosseln und wird für den Lauf angehoben.
RETICORA_RATE_LIMIT_RPM=1000000 RETICORA_RATE_LIMIT_ORG_RPM=1000000 ./server

# 3. Szenarien
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) tests/load/api.js
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) tests/load/ingest.js
k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat /tmp/load.key) -e SCENARIO=spool tests/load/ingest.js
```

Parameter: `DURATION` und `VUS` (api.js); `SCENARIO` (`load` oder `spool`),
`RATE`, `BATCH`, `DURATION_S`, `PROBE_EVERY_S`, `VISIBLE_BUDGET_S`,
`SPOOL_RECORDS`, `SPOOL_BATCH`, `SPOOL_BUDGET_S`, `TARGET` und `WAIT_S`
(ingest.js). Der Workflow `.github/workflows/load.yml` (manuell und
nächtlich) führt diese Schritte aus und lädt die k6-Zusammenfassungen als
Artefakt hoch. Das Ergebnis des G1-Laufs steht in
`docs/acceptance/lasttest-g1.md`.

### Ingest (NFR-03, WP-334)

- **Messgrößen.** `ingest_visible_delay` (Probe-Records alle `PROBE_EVERY_S`
  während der Last und die Zeit vom Lastende, bis jeder Record des Laufs
  sichtbar ist), `ingest_drain_records_per_s` (verarbeitete Records ab
  Sendeende je Sekunde: was beim Sendeende noch nicht sichtbar war, geteilt
  durch die Zeit bis alle sichtbar sind; Budget nur im Szenario `spool`),
  `ingest_drain_inclusive_records_per_s` (alle Records durch die Zeit ab dem
  ersten Senden, zur Information), `ingest_spool_flush` (erstes Senden bis
  alles sichtbar), `ingest_backlog_peak` (größter Rückstand laut `/readyz`,
  `details.ingest.total`). Sichtbar heißt: `GET /cis?filter[serial_number][like]=<Lauf>*`
  zählt den Record.
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
  sich die Kerne; die Werte sind konservativ. Vor jedem Lauf neu seeden: der
  Rückstand eines Laufs verfälscht den nächsten.

Ergänzend prüfen die Integrationstests `backend/internal/ci/querybudget_integration_test.go`
und `backend/internal/topology/querybudget_integration_test.go` die Zahl der
Statements je Anfrage (kein N+1) und per `EXPLAIN` die Indexnutzung zentraler
Abfragen (ABN-02).
