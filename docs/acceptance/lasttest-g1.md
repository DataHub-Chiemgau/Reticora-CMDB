# Lasttest G1 (NFR-10)

Nachweis zu NFR-01, NFR-02, NFR-03 und ABN-02 vor Gate G1 (WP-206). Die
Szenarien, ihre Schwellen und der Ablauf stehen in `tests/load/README.md`; der
Workflow `.github/workflows/load.yml` wiederholt den Lauf manuell und nächtlich
und legt die k6-Zusammenfassungen als Artefakt ab.

## Umgebung und Datenbestand

- **Seed:** `cmd/seed -objects 10000` (SIM-01 skaliert auf 10.000 Objekte,
  WP-203). Die Demo-Org hat 10.000 CIs mit je einem Interface und einer
  Adresse sowie 19.993 Kanten (LLDP zum Core-Switch, Strom zur PDU) und 2.000
  Metriken. Der Core-Switch `sw-00001` trägt etwa die Hälfte aller CIs, sein
  Impact umfasst die ganze Organisation (Worst Case).
- **Skalierungsziel:** 10.000 Objekte nach CH15 (NFR-01). E-17 (Bestätigung des
  Skalierungsziels) ist weiter offen; Seed und Szenarien sind parametrisiert
  und bis 50.000 Objekte nutzbar.
- **Referenzumgebung:** Für G1 lief der Test auf der Entwicklungsumgebung: 4
  vCPU und 15 GB RAM. Auf demselben Host liefen PostgreSQL 16 mit TimescaleDB
  2.17 (Docker), Redis, NATS, der Server und k6.
  - Der Server ist ein einzelner Prozess.
  - Das Rate-Limit ist für den Lauf angehoben (siehe README).
  - Die Lastszenarien laufen parallel, mit 5 VUs je Lesepfad und 2 VUs für
    Topologie und Impact.

  Ein dedizierter Referenzhost (getrennte Datenbank) steht für G1 nicht zur
  Verfügung. Die Werte sind deshalb konservativ, denn k6 und die Datenbank
  konkurrieren um dieselben 4 Kerne.

## Ergebnis NFR-02 (p95, 60 s, 10.000 Objekte)

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

Checks: 100 % (5.621 von 5.621). Der Impact-Wert ist der Worst Case: Die ganze
Organisation fällt aus, und die Antwort umfasst rund 5 MB. Er liegt nur unter
der gleichzeitigen Last aller Szenarien auf demselben 4-Kern-Host über dem
Budget. Ein einzelner Aufruf braucht 0,55–0,65 s.

## Ergebnis NFR-03 (Ingest)

| Messgröße | Ergebnis | Budget | Ergebnis |
|---|---:|---:|---|
| Annahme `POST /ingest/bulk` (20 Records je Anfrage), p95 | 15 ms | – | – |
| Abbau-Rate der Queue (neue Geräte) | 58 Records/s | 200 Records/s | **verfehlt** |
| Verzögerung Record → sichtbar bei 200 Records/s Angebot | 72 s (Rückstau) | < 30 s | **verfehlt** |
| Verzögerung Record → sichtbar bei 50 Records/s Angebot | 3,6 s | < 30 s | erfüllt |
| Spool-Flush 10.000 Records | nicht gemessen (Collector-seitig, COL-Spool) | < 5 min | offen |

Analyse (Statement-Profil des Servers während des Laufs):

- **Statements je Record:** Ein Record kostet rund 55 Statements in etwa 12
  getrennten Transaktionen. Darin enthalten sind:
  - Identitätssuche und gelöschte Kandidaten
  - Feldauflösung (`ResolveFields`)
  - CI-Anlage mit Audit, Historie und Outbox
  - Such- und KI-Index
  - Provenienz
  - Ledger
- **Engpass:** Die Datenbank ist dabei nur zu etwa einem Drittel ausgelastet;
  es dominieren die Roundtrips.
- **Serialisierung:** Die Verarbeitung je Organisation ist bewusst serialisiert
  (Mutex je Organisation, Grundlage der Identitäts-Deduplizierung), daher
  entspricht der Durchsatz einer Organisation dem Kehrwert der Zeit je Record.

Folge-WP (nicht in WP-206, siehe unten): Ingest-Pipeline mit wenigen
Transaktionen je Record und parallelen Spuren je Identitätsschlüssel.

## Budgetkorrekturen in WP-206

Der erste Lauf deckte Fehler auf, die in WP-206 behoben wurden:

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

## ABN-02

Die Integrationstests `backend/internal/ci/querybudget_integration_test.go` und
`backend/internal/topology/querybudget_integration_test.go` laufen bei jedem PR.
Sie prüfen drei Dinge:

- **CI-Liste:** gleiche Zahl an Statements für 10 und 200 Zeilen, höchstens 6.
- **CI-Detail:** höchstens 5 Statements.
- **Topologie und Impact:** gleiche Zahl an Statements für Hubs mit 20 und 400
  Nachbarn.

Per `EXPLAIN` stellen sie außerdem sicher, dass die Identitäts-, Detail- und
Attributabfragen sowie die Kantenabfragen unter RLS keinen sequenziellen Scan
nutzen.

## Offene Punkte (Folge-WPs)

1. **NFR-03 Durchsatz:** Ziel sind 200 Records/s je Instanz bei neuen Geräten.
   Gemessen sind 58/s, nötig sind weniger Transaktionen je Record und parallele
   Spuren innerhalb einer Organisation, ohne die Identitäts-Deduplizierung zu
   verletzen. Damit sinkt auch die Verzögerung unter Volllast unter 30 s.
2. **Impact unter Parallellast im Worst Case:** Der Antwortumfang (alle Pfade
   je betroffenem CI) und die Messung auf einem dedizierten Referenzhost stehen
   noch aus.
3. **Spool-Flush:** Für 10.000 Records im Collector-Spool fehlt ein eigenes
   Szenario.
4. **Listen über 10.000 Objekte:** Bei 28.000 CIs lag `GET /cis` bei p95 1,9 s
   (Gesamtzahl und Sortierung über die ganze Organisation). Das ist für den
   Headroom bis 50.000 Objekte (NFR-01) zu prüfen.
