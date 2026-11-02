# ADR 0001: Mandantentrennung für Metriken (TimescaleDB) über Security-Barrier-Views

- Status: angenommen (Spike WP-039, 2026-10-01)
- Anforderungen: TEC-06 ([B], [Q] Spike), MON-01 ([B])
- Umsetzung: WP-040 (Migration und Repository-Umstellung), WP-350
  (Client-/Site-Scope in den Views, Punkt 4; Entscheidung E-63)
- Nachweis: `backend/internal/monitoring/timescale_rls_spike_integration_test.go`,
  `backend/internal/monitoring/audit_t1_metric_scope_integration_test.go`

## Kontext

TEC-06 verlangt einen Spike, der RLS und FORCE RLS auf Hypertables, komprimierten
Chunks und Continuous Aggregates nachweist; bei Lücken sind Security-Barrier-Views
der Fallback. MON-01 verlangt für `metric_sample` 1-Tages-Chunks, Kompression nach
7 Tagen, Retention 400 Tage, das Continuous Aggregate `metric_sample_1h` und RLS.

Heute hat `metric_sample` weder RLS noch FORCE RLS (Migration 000020). Die
Mandantentrennung liegt allein im Repository (`organization_id` in jeder
Abfrage). `metric_sample_1h` ist ungeschützt. Der RLS-Katalogtest führt die
Hypertable als bekannte Lücke für WP-040.

## Untersuchung

Umgebung: PostgreSQL 16 mit TimescaleDB 2.17.2, beim Spike die Version des
CI-Images (der Compose-Stack nutzte den gleitenden Tag `latest-pg16`).
Nachtrag 2026-10-21 (E-96, WP-389): CI, Compose-Stack und `reticora-postgres`
nutzen jetzt
`timescale/timescaledb:2.31.0-pg16@sha256:322ae364eca3321cf2a48f1ef31213b87843413b3cf8feeebf54e999a26bb731`
(PostgreSQL 16.15). Die Nachweistests im Kopf sind darauf grün; die Zeilen 1
bis 3 von Ergebnis 1 prüft `TestTimescaleRefusesRLSWithCompression` mit
denselben Fehlertexten, die Zeilen 4 und 5 wurden nicht erneut geprüft.

Geprüft wurde mit der App-Rolle `reticora_app` (ohne Superuser, ohne
BYPASSRLS) über `database.WithTenant`. Ein Superuser umgeht RLS auch mit FORCE
und taugt nicht als Nachweis.

### Ergebnis 1: RLS ist mit Kompression und Continuous Aggregates unvereinbar

| Reihenfolge | Operation | Ergebnis |
|---|---|---|
| RLS zuerst | `ALTER TABLE … SET (timescaledb.compress …)` | `compression cannot be used on table with row security` |
| RLS zuerst | `CREATE MATERIALIZED VIEW … WITH (timescaledb.continuous)` | `cannot create continuous aggregate on hypertable with row security` |
| Kompression zuerst | `ALTER TABLE … ENABLE ROW LEVEL SECURITY` | `operation not supported on hypertables that have compression enabled` |
| Continuous Aggregate | `ENABLE ROW LEVEL SECURITY` auf View bzw. Materialisierungstabelle | `cannot be performed on relation` / `operation not supported on materialization tables` |
| RLS ohne Kompression | `UPDATE` als `reticora_app` | `variable not found in subplan target list` (TimescaleDB-Fehler) |

RLS auf `metric_sample` ist nur ohne Kompression und ohne `metric_sample_1h`
möglich. Damit wären die Pflichtanteile von MON-01 nicht erfüllbar. Variante A
(RLS + FORCE auf der Hypertable) scheidet aus.

### Ergebnis 2: Security-Barrier-Views trennen Mandanten vollständig

Variante B: Die App-Rolle hat keine Rechte auf `metric_sample` und
`metric_sample_1h`. Sie liest und schreibt ausschließlich über Views mit
`security_barrier` und dem Org-Prädikat:

```sql
CREATE VIEW metric_sample_v WITH (security_barrier) AS
  SELECT * FROM metric_sample
  WHERE organization_id = (SELECT current_setting('app.org_id')::uuid)
  WITH CASCADED CHECK OPTION;
```

Belegt durch den Integrationstest:

- Rohdaten, auch aus komprimierten Chunks, liefern nur Zeilen der eigenen Org.
- Die View auf das Continuous Aggregate liefert nur Zeilen der eigenen Org.
- Direkter Zugriff auf Hypertable und Aggregat endet mit `permission denied`.
  Neue Chunks übernehmen die Rechte der Hypertable. Ohne Grant auf die
  Hypertable hat die App-Rolle auch auf die Chunks keine Rechte.
- Eine billige, „undichte“ Nutzerfunktion im `WHERE` sieht keine fremde Zeile.
  Die Barriere wird vor Nutzerprädikaten ausgewertet.
- `INSERT` über die View für eine fremde Org scheitert an der CHECK OPTION.
  Eigene Zeilen, auch in komprimierte Zeiträume, werden angenommen.

`DELETE` über eine View auf die Hypertable löst denselben TimescaleDB-Fehler
aus wie `UPDATE` unter RLS. Die App braucht kein `DELETE`, weil die Retention
als Systemjob läuft. Die View erhält deshalb nur `SELECT` und `INSERT`.

### Messwerte

Lokale Messung mit 400 000 Samples, 20 Orgs, 1-Tages-Chunks, Chunks älter als
7 Tage komprimiert, je Abfrage zweimal gemessen:

| Abfrage (eine Org) | direkt mit Org-Filter | View, Prädikat `current_setting()` | View, Prädikat als Sub-Select |
|---|---:|---:|---:|
| letzte 3 Tage | 4,4–7,9 ms | 24–38 ms | 4,8–8,0 ms |
| gesamter Zeitraum | 10–15 ms | 65–80 ms | 20–23 ms |

Ein direkter Aufruf von `current_setting()` im View-Prädikat verhindert das
Segment-Pruning und kostet den Faktor 5–6. Als Sub-Select (InitPlan) wird der Wert
einmal ausgewertet. Die Laufzeit liegt dann im Bereich der direkten Abfrage.

### Messwerte Client-/Site-Scope (WP-350)

Lokale Messung (TimescaleDB 2.17.2, PostgreSQL 16, 4 Kerne): eine Org mit
zwei Clients und 10 000 CIs (je 5000), 30 Tage stündliche Samples je CI
(7,2 Mio. Zeilen), Chunks älter als 7 Tage komprimiert, Aggregat
aktualisiert. Gemessen wurde `PGMetricStore.Query` über den Laufzeit-Login
(`scopetest.AppDSN`), je Pfad 100 Abfragen auf wechselnde CIs (ohne CI-Filter
20) nach drei Aufwärmläufen, zwei Läufe vor und zwei nach Migration 000143.
Angegeben ist p95 in ms (Bereich der beiden Läufe):

| Abfrage | Principal | vor 000143 | nach 000143 |
|---|---|---:|---:|
| roh, 24 h, ein CI | Client 1 | 37–39 | 38–39 |
| 5 min, 7 Tage, ein CI | Client 1 | 40–41 | 38–39 |
| 1 h (Aggregat), 30 Tage, ein CI | Client 1 | 38–41 | 43 |
| roh, 24 h, ein CI | org-weit | 26 | 20–28 |
| 5 min, 7 Tage, ein CI | org-weit | 26 | 24–26 |
| 1 h (Aggregat), 30 Tage, ein CI | org-weit | 27 | 27–29 |
| roh, 2 h, ohne CI-Filter | Client 1 | 41–58 | 103–125 |
| 1 h (Aggregat), 30 Tage, ohne CI-Filter | Client 1 | 9 850–10 130 | 12 070–12 160 |
| 1 h (Aggregat), 30 Tage, ohne CI-Filter | org-weit | 10 790–10 900 | 15 550–15 760 |

Die Abfragen der API auf ein CI (`GET /cis/{id}/metrics`) bleiben im
Messrauschen. Abfragen ohne CI-Filter über alle CIs eines Clients werden
teurer. Die Rohdaten-Abfrage über zwei Stunden (10 000 Zeilen) braucht etwa
die doppelte Zeit für den Hash-Lookup in `ci`. Die Aggregat-Abfrage über
30 Tage liest 7,2 Mio. Zeilen. Ihre Laufzeit wird vom Scan bestimmt. Mit dem
generischen Plan der vorbereiteten Anweisung (pgx) kostet das zusätzliche
Prädikat je Zeile 10–40 %. Mit einem Custom-Plan (psql, Literale) war die neue
View schneller als die alte (5,0–6,5 s gegenüber 10,6–12,0 s). Diese
Abfrageform bietet die Monitoring-Seite an (`GET /api/v1/monitoring/metrics`
ohne `ci_id`). Die CI-Ansicht fragt je CI ab. Wird die Abfrage ohne CI-Filter
produktiv zeitkritisch, bleibt die Denormalisierung (E-63 Option 2) der
Ausweg.

## Entscheidung

1. `metric_sample` und `metric_sample_1h` behalten Kompression, Retention und
   Continuous Aggregate nach MON-01 und erhalten **kein** RLS (Ausnahme vom
   RLS-Katalog wegen technischer Unvereinbarkeit, siehe Ergebnis 1).
2. Die Mandantentrennung erfolgt über Security-Barrier-Views mit dem
   Org-Prädikat als Sub-Select (`(SELECT current_setting('app.org_id')::uuid)`)
   und `WITH CASCADED CHECK OPTION` für Rohdaten.
3. `reticora_app` erhält Rechte nur auf die Views: `SELECT, INSERT` auf die
   Rohdaten-View und `SELECT` auf die Aggregat-View. Die Rechte auf
   Hypertable, Aggregat und bestehenden Chunks (Grant aus Migration 000056)
   werden entzogen. Retention, Kompression und Refresh
   laufen als TimescaleDB-Jobs des Eigentümers.
4. Client- und Site-Scope (TEN-05) sind weitere Barrier-Prädikate derselben
   Views (Migration 000143, WP-350, Entscheidung E-63 Option 1): eine Zeile
   mit CI ist nur sichtbar, wenn das CI in der Org existiert und sein Client
   und Standort im Scope der Transaktion liegen (NULL = org-weit, sichtbar,
   wie die ci-Policy). Das Prädikat ist ein Sub-Select über `ci` und nennt
   `app.client_scope`/`app.site_scope` explizit. Es hängt damit nicht davon
   ab, ob die RLS von `ci` für den View-Eigentümer greift. Die GUCs werden als
   Sub-Selects (InitPlan) gelesen. Eine org-weite Transaktion überspringt den
   CI-Lookup. Die `CASCADED CHECK OPTION` der Rohdaten-View weist ein
   `INSERT` für ein CI außerhalb des Scopes ab. `metric_sample` erhält keine
   denormalisierten `client_id`/`site_id`: Verschiebt sich ein CI, gilt die
   neue Sichtbarkeit sofort, und komprimierte Chunks werden nicht
   umgeschrieben. Damit gilt WP-025 AK1 für `metric_sample` als erfüllt.

## Folgen

- WP-040 legt die Views per Migration an, entzieht die Direktrechte, stellt
  `monitoring/pg_store.go` auf die Views um und ersetzt die Katalog-Lücken für
  `metric_sample` durch eine dokumentierte View-Ausnahme mit eigener Prüfung.
- Der Spike-Test bleibt bestehen. Hebt ein TimescaleDB-Upgrade die
  Einschränkungen auf, schlägt `TestTimescaleRefusesRLSWithCompression` fehl.
  Dann ist diese ADR neu zu bewerten.
- Abfragen auf Metriken müssen über die Views laufen. Für Systemjobs, die über
  alle Orgs aggregieren, gilt der Systempfad (`WithSystem`, E-08).
