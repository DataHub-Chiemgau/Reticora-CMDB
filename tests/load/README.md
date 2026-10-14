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
| `ingest.js` | `POST /ingest/bulk` mit `RATE` Records/s; Abbau-Rate der Queue und Verzögerung Record → sichtbar | ≥ 200/s, < 30 s |

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
```

Parameter: `DURATION` und `VUS` (api.js), `RATE`, `BATCH`, `DURATION_S` und
`TARGET` (ingest.js). Der Workflow `.github/workflows/load.yml` (manuell und
nächtlich) führt diese Schritte aus und lädt die k6-Zusammenfassungen als
Artefakt hoch. Das Ergebnis des G1-Laufs steht in
`docs/acceptance/lasttest-g1.md`.

Ergänzend prüfen die Integrationstests `backend/internal/ci/querybudget_integration_test.go`
und `backend/internal/topology/querybudget_integration_test.go` die Zahl der
Statements je Anfrage (kein N+1) und per `EXPLAIN` die Indexnutzung zentraler
Abfragen (ABN-02).
