# ADR 0002: Speicher der Probe-Ergebnisse (TimescaleDB): Volumen, Chunk-Größe, Kompression, Idempotenz und Aggregationsstufen

- Status: angenommen (Spike WP-943, 2026-11-12)
- Anforderungen: PRB-06 ([P3]), NFR-11 ([P3]), MON-01 ([B], P3-Anteil),
  NFR-06 ([B], Probe-Daten über Aggregate und Zustandshistorie)
- Entscheidungen: E-171 (Speicherengine und Tabellenmodell, Option 1),
  E-172 (Rohdatenaufbewahrung und Aggregationsstufen, Option 1)
- Umsetzung: WP-945 (Hypertable `probe_result`, View `probe_result_v`,
  Kompression, Retention der Rohdaten), WP-972 (Continuous Aggregates),
  WP-974 (Retention von Zustandswechseln und Tagesverfügbarkeit)
- Nachweis: `backend/internal/monitoring/probe_storage_spike_integration_test.go`
  (`TestProbeStorageSpikeWriteRate`, `TestProbeStorageSpikeCompressionRatio`,
  `TestProbeStorageSpikeOnConflictOnCompressedChunk`,
  `TestProbeStorageSpikeHierarchicalAggregates`,
  `TestProbeStorageSpikeAppRoleHasNoDirectAccess`)

## Kontext

NFR-11 legt die Prüflast fest: 353 Prüfergebnisse/s dauerhaft (30,5 Mio. am
Tag), Auslegung mit Reserve Faktor 3 (Ingest ≥ 1.060/s) und eine
Nachsynchronisationsspitze von 5.000 Ergebnissen/s (PRB-07). PRB-06 verlangt
Rohergebnisse 14 Tage, Aggregate 5 min 90 Tage, 1 h und 1 d je 400 Tage,
Zustandswechsel und Tagesverfügbarkeit 400 Tage und Kompression der Rohdaten
ab 1 Tag; Volumen, Chunk-Größe und Schreibrate sind je Tabelle zu
dokumentieren.

Der Metrikspeicher `metric_sample` ist dafür nicht ausgelegt: TEXT-Name und
JSONB-Labels je Zeile, kein Idempotenzschlüssel, nur ein Stundenaggregat
(Migrationen 000020, 000031, 000174). E-171 hat deshalb eine eigene, schmale
Hypertable `probe_result` mit Zugriff über eine Security-Barrier-View nach
ADR 0001 entschieden, E-172 die Stufen. Offen waren die Messwerte und zwei
Fragen zu TimescaleDB 2.31 (im Betrieb seit E-96): Wirkt `ON CONFLICT DO
NOTHING` auf einem Unique-Constraint auch in komprimierten Chunks? Lassen
sich hierarchische Continuous Aggregates (5 min → 1 h → 1 d) in einer
Migration mit `WITH NO DATA` anlegen?

## Untersuchung

### Umgebung und Aussagekraft

Lokale Messung am 2026-11-12 auf einem Apple-Silicon-Rechner mit Docker
Desktop, Image
`timescale/timescaledb:2.31.0-pg16@sha256:322ae364eca3321cf2a48f1ef31213b87843413b3cf8feeebf54e999a26bb731`
(der gepinnte `TIMESCALE_IMAGE` nach E-96; PostgreSQL 16.15,
TimescaleDB 2.31.0), Standardkonfiguration des Testcontainers. Auf derselben
Datenbankinstanz liefen gleichzeitig Testläufe anderer Arbeitsstränge. Die
Werte sind deshalb **Richtwerte** einer Entwicklerumgebung, keine Messung
auf der Referenzhardware. Der Nachweis unter der Last aus NFR-11 folgt im
Lasttest (WP-1083, WP-1084, Lasttest G3); der Spike-Test enthält die
Abbruchgrenze von 5.000 Zeilen/s für den CI-Postgres.

Der Prototyp ist die Hypertable `probe_result_spike` mit dem Schema aus
E-171 in einem eigenen Schema:

```sql
CREATE TABLE probe_result_spike (
    time timestamptz NOT NULL, organization_id uuid NOT NULL,
    probe_id uuid NOT NULL, collector_id uuid NOT NULL, seq bigint NOT NULL,
    status smallint NOT NULL,   -- 0 = ok, 1 = fail, 2 = error
    code smallint NOT NULL DEFAULT 0, rtt_ms real NULL,
    loss smallint NULL,         -- Promille
    value double precision NULL,
    CONSTRAINT probe_result_spike_key UNIQUE (probe_id, collector_id, seq, time));
SELECT create_hypertable('probe_result_spike', 'time', chunk_time_interval => INTERVAL '6 hours');
CREATE INDEX probe_result_spike_org_time ON probe_result_spike (organization_id, time DESC);
ALTER TABLE probe_result_spike SET (timescaledb.compress,
    timescaledb.compress_segmentby = 'organization_id, probe_id',
    timescaledb.compress_orderby = 'time DESC, collector_id, seq');
```

Dazu die View nach Migration 000071 (`security_barrier`, Organisation als
Sub-Select, `WITH CASCADED CHECK OPTION`) mit `SELECT, INSERT` für
`reticora_app`. Der Test entfernt alle Objekte im Cleanup und prüft, dass
kein TimescaleDB-Job zurückbleibt.

### Ergebnis 1: Schreibrate und WAL

`TestProbeStorageSpikeWriteRate`: Die App-Rolle schreibt über die View
1 Mio. synthetische Zeilen (10.000 Probes, je 100 Ergebnisse im Abstand von
30 s) per `INSERT … SELECT FROM unnest(…)` in Blöcken zu 1.000 Zeilen, je
Block eine Mandantentransaktion, eine Sitzung.

| Messgröße | Wert |
|---|---:|
| Zeilen | 1.000.000 |
| Dauer | 10,0 s |
| Schreibrate | ≈ 100.000 Zeilen/s |
| WAL je Zeile | 459 B (Lauf mit gleichzeitiger Kompression, siehe unten: 575 B) |
| Heap je Zeile (unkomprimiert) | 117 B |
| Indizes je Zeile (Unique-Schlüssel, `(organization_id, time)`, Zeitindex) | 96 B |
| gesamt je Zeile | 213 B |

Die Schreibrate liegt um den Faktor 94 über der Auslegung (1.060/s) und um
den Faktor 20 über der Nachsynchronisationsspitze (5.000/s).

Beobachtung: Ein erster Lauf, in dem eine Kompressions-Policy auf demselben
Prototyp aktiv war und der Datenbereich zwei Tage zurücklag, kam nur auf
2.983 Zeilen/s. Wahrscheinliche Ursache ist der Policy-Job, der den gerade
beschriebenen Chunk komprimierte; einzeln nachgestellt wurde das nicht. Der
Messlauf schreibt deshalb wie der Betrieb in die aktuellen, unkomprimierten
Chunks. Folge für den Betrieb: Laufende Daten dürfen nicht in Chunks fallen,
die gerade komprimiert werden (siehe Entscheidung 5).

### Ergebnis 2: Zeilenbreite und Kompression

`TestProbeStorageSpikeCompressionRatio`: ein 6-h-Chunk mit der Dichte des
Betriebs, 2.000 Probes mit je 720 Ergebnissen (alle 30 s), 2 % Fehlschläge,
RTT 1–41 ms mit Zufallsanteil.

| Messgröße | Wert |
|---|---:|
| Zeilen | 1.440.000 |
| Heap je Zeile | 117,0 B |
| Unique-Schlüssel `(probe_id, collector_id, seq, time)` je Zeile | 114,7 B |
| Standard-Zeitindex von `create_hypertable` je Zeile | 10,3 B |
| Index `(organization_id, time DESC)` je Zeile | 10,9 B |
| gesamt je Zeile unkomprimiert | 253,0 B |
| gesamt je Zeile komprimiert (`compress_chunk`) | 8,4 B |
| Kompressionsfaktor | 30,3 |

Die beiden Läufe ergeben 213–253 B je Zeile unkomprimiert; der Unterschied
liegt im Unique-Schlüssel (Füllgrad je nach Einfügereihenfolge). E-171 hatte
≈ 150 B geschätzt; gemessen ist die Zeile um 40–70 % breiter. Der
Kompressionsfaktor liegt über der Annahme von E-172 (10–15). Die Messdaten
sind synthetisch; reale RTT-Streuung komprimiert schlechter. Die Rechnung
unten führt deshalb neben dem Messwert 8,4 B die konservative Annahme
25 B je Zeile (Faktor 10).

Nach der Kompression liefert die View alle 720 Ergebnisse einer Probe aus
dem komprimierten Chunk.

### Ergebnis 3: Idempotenz auf komprimierten Chunks

`TestProbeStorageSpikeOnConflictOnCompressedChunk`: 1.000 Zeilen in einen
Chunk, `compress_chunk`, danach als App-Rolle über die View:

- derselbe Block mit `ON CONFLICT DO NOTHING`: 0 Zeilen eingefügt, kein
  Fehler, Zeilenzahl unverändert 1.000 (16 ms);
- 500 Dubletten und 500 neue Zeilen: genau 500 eingefügt (19 ms);
- derselbe Block ohne `ON CONFLICT`: Fehler 23505, der Schlüssel hält.

Der Chunk bleibt dabei komprimiert. Ergänzend von Hand geprüft (nicht im
Test festgehalten): Auch ohne explizites `compress_orderby` lässt sich die
Kompression mit dem Unique-Schlüssel einschalten, und die Doppelzustellung
bleibt ohne Wirkung.

### Ergebnis 4: Hierarchische Continuous Aggregates

`TestProbeStorageSpikeHierarchicalAggregates`: 20 Probes, alle 10 min über
drei Tage und stündlich 97–100 Tage zurück.

- Die drei Stufen `…_5m` (auf der Hypertable), `…_1h` (auf `…_5m`) und `…_1d`
  (auf `…_1h`) lassen sich in **einer Transaktion** mit `WITH NO DATA`
  anlegen und danach mit `refresh_continuous_aggregate` (außerhalb einer
  Transaktion, in der Reihenfolge der Stufen) füllen.
- Jede Stufe speichert Summen statt Mittelwerten: `total`, `ok`, `rtt_sum`,
  `rtt_n`, `rtt_min`, `rtt_max`. Die RTT-Summe ist `double precision`
  (`sum(rtt_ms::float8)`, in den oberen Stufen `sum(rtt_sum)`): Eine
  Summe über die `real`-Spalte bleibt `real`, und bei gebrochenen RTTs
  weicht schon ein 1-d-Bucket mit 2.880 Werten um 2,5·10⁻⁶ vom direkten
  Mittel ab, bei 1 Mio. Werten um 6,5·10⁻⁴. Mit gebrochenen synthetischen
  RTTs stimmen Anzahl, Erfolgsquote, Mittel (`rtt_sum / rtt_n`, Toleranz
  10⁻⁶), Minimum und Maximum der RTT in allen 10.100 (5 min), 2.900 (1 h)
  und 140 (1 d) Buckets mit der direkten Aggregation der Rohdaten überein.
  Ein Mittel der Mittel wäre falsch, sobald Buckets unterschiedlich viele
  Ergebnisse haben.
- Eine Retention-Policy auf dem 5-min-Aggregat (`add_retention_policy`,
  90 Tage, per `run_job` ausgeführt) löscht die Buckets älter als 90 Tage;
  die 1-h-Stufe behält diese Tage.

### Ergebnis 5: Kein Direktzugriff der App-Rolle

`TestProbeStorageSpikeAppRoleHasNoDirectAccess` sucht die internen
Relationen wie Migration 000071 (`:38–75`): Hypertables, Chunks über
`pg_inherits`, die Materialisierung und die Partial- und Direct-View des
Aggregats. Abweichend von TimescaleDB 2.17 gibt es in 2.31 **keine
komprimierte Hypertable** mehr (`compressed_hypertable_id` ist leer). Die
komprimierten Daten liegen je Chunk in einer eigenen Relation
`_timescaledb_internal.<chunk>_compressed`, die von nichts erbt; die Suche
von 000071 findet sie nicht. Der Test findet sie über den Namen.

Beim Komprimieren übernimmt TimescaleDB 2.31 die ACL der Hypertable auf
`<chunk>_compressed`, und ein späteres `GRANT` oder `REVOKE` auf die
Hypertable wirkt dort weiter. `probe_result` liegt in `public`; dort
vergeben die Standardrechte aus 000056 beim Anlegen `ALL` an `reticora_app`.
Der Test stellt das nach: Er gibt die Hypertable vor `compress_chunk` für
die App-Rolle frei; danach tragen alle komprimierten Relationen den Grant,
nach `REVOKE ALL` auf die Hypertable allein keine mehr, und ein danach
komprimierter Chunk erbt keinen Grant. (Eine leere ACL sähe man nur in einem
Schema ohne Standardrechte, etwa im Spike-Schema `p3_probe_spike`.) Die
Schutzbedingung ist deshalb: Der `REVOKE` auf die Hypertable steht vor jeder
Kompression und wird nie zurückgenommen. Zusätzlich vergibt der Test im
ungünstigsten Fall alle Rechte auf jede gefundene Relation, entzieht sie wie
000071 und prüft dann `SELECT` (und für Tabellen `DELETE`) als App-Rolle:
Jeder Zugriff auf die 28 Relationen (2 Hypertables, 13 Chunks,
10 komprimierte Relationen, Aggregat, Partial- und Direct-View) endet mit
`permission denied`. Die View liefert weiter nur die Zeilen der eigenen
Organisation, auch aus komprimierten Chunks.

## Volumenrechnung

Grundlage: 353 Ergebnisse/s × 86.400 s = 30.499.200 ≈ **30,5 Mio. Zeilen
am Tag**, 10.600 Probes bei 30 s (E-168), gemessene Zeilenbreite 213–253 B
unkomprimiert und 8,4 B komprimiert (konservativ 25 B).

| Größe | bei 353/s | bei 1.060/s (Reserve Faktor 3) |
|---|---:|---:|
| Rohdaten unkomprimiert je Tag | 6,5–7,7 GB | 19,5–23,2 GB |
| Rohdaten komprimiert je Tag (gemessen / konservativ) | 0,26 / 0,76 GB | 0,77 / 2,3 GB |
| Zeilen je 6-h-Chunk | 7,62 Mio. | 22,9 Mio. |
| 6-h-Chunk unkomprimiert | 1,62–1,93 GB | 4,9–5,8 GB |
| Zeilen je 3-h-Chunk | 3,81 Mio. | 11,4 Mio. |
| 3-h-Chunk unkomprimiert | 0,81–0,96 GB | 2,4–2,9 GB |
| WAL | 584–731 MB/h (14–17,5 GB/Tag) | 1,75–2,2 GB/h |

Nachsynchronisation 5.000 Zeilen/s: WAL 2,3–2,9 MB/s (8,3–10,4 GB/h),
gemessen sind lokal ≈ 100.000 Zeilen/s.

**Chunk-Größe:** Ziel ist ein unkomprimierter Chunk einschließlich Indizes
unter 25 % von `shared_buffers`; Referenz sind 4 GB (`docs/ops/sizing.md`,
Zeile „≤ 25.000“), also 1 GB. Der 6-h-Chunk aus E-171 liegt mit 1,62–1,93 GB
darüber, der 3-h-Chunk mit 0,81–0,96 GB darunter (Entscheidung 2). Bei
Reserve Faktor 3 braucht auch der 3-h-Chunk 12 GB `shared_buffers`; diese
Last ist eine Reserve, kein Dauerbetrieb.

**Speicher je Stufe (E-172) im eingeschwungenen Zustand bei 353/s:**

| Stufe | Aufbewahrung | Zeilen gesamt | Speicher | Grundlage |
|---|---:|---:|---:|---|
| Roh | 14 Tage | 427 Mio. | 10,7–18,6 GB | gemessen: unkomprimiert 1 Tag plus ein Chunk (1,125 Tage × 6,5–7,7 GB), 13 Tage komprimiert (0,26–0,76 GB je Tag) |
| 5 min | 90 Tage | 275 Mio. | ≈ 33 GB unkomprimiert, ≈ 2,2–3,3 GB komprimiert | Rechnung E-172 (≈ 120 B je Aggregatzeile), nicht gemessen |
| 1 h | 400 Tage | 102 Mio. | ≈ 12,2 GB unkomprimiert, ≈ 0,8–1,2 GB komprimiert | Rechnung E-172, nicht gemessen |
| 1 d | 400 Tage | 4,24 Mio. | ≈ 0,5 GB | Rechnung E-172, nicht gemessen |
| Zustandswechsel | 400 Tage | ≤ 42 Mio. (obere Annahme 10 je Probe und Tag) | ≤ 16 GB | Rechnung: ≈ 120 B Heap plus ≈ 250 B für die sechs Indizes aus WP-944; E-172 nannte ohne Indizes 6,4 GB |
| Tagesverfügbarkeit | 400 Tage | 4,24 Mio. | ≈ 0,4–0,6 GB | Rechnung |

Summe mit komprimierten Aggregaten: ≈ 15–40 GB bei 353/s (die Spanne
kommt vor allem aus der Zahl der Zustandswechsel), ≈ 45–120 GB bei
1.060/s. Die Aggregate brauchen dafür eine Kompressions-Policy (WP-972);
ohne sie wächst allein die 5-min-Stufe auf ≈ 33 GB (bei Faktor 3 ≈ 100 GB).

## Entscheidung

1. **E-171 bestätigt:** Die Probe-Ergebnisse liegen in der eigenen,
   schmalen Hypertable `probe_result` mit dem Schema oben. Die App-Rolle
   erreicht sie nur über die Security-Barrier-View `probe_result_v`
   (`SELECT, INSERT`, Muster 000071). Der Idempotenzschlüssel ist
   `UNIQUE (probe_id, collector_id, seq, time)`; der Ingest schreibt per
   `INSERT … SELECT FROM unnest(…) ON CONFLICT DO NOTHING` in Blöcken
   (WP-960). Das wirkt auch auf komprimierten Chunks.
2. **Chunk-Intervall korrigiert: 3 h statt 6 h.** Gemessen ist eine
   unkomprimierte Zeile 213–253 B breit (E-171 nahm ≈ 150 B an). Ein
   6-h-Chunk läge bei 1,62–1,93 GB und damit über 25 % von 4 GB
   `shared_buffers`; ein 3-h-Chunk liegt bei 0,81–0,96 GB. WP-945 übernimmt
   `chunk_time_interval => INTERVAL '3 hours'` (dort AK2).
3. **Kompressionsbeginn bestätigt: 1 Tag** (`add_compression_policy(…,
   compress_after => INTERVAL '1 day')`). **`segmentby` bestätigt:**
   `organization_id, probe_id` (Faktor 30 im Spike). `orderby`:
   `time DESC, collector_id, seq`, so wie im Spike festgehalten; damit liegt
   der Idempotenzschlüssel vollständig in `segmentby` und `orderby`.
4. **Indizes:** `create_hypertable(…, create_default_indexes => false)`
   statt des Standard-Zeitindex, dazu `(organization_id, time DESC)` und
   `(probe_id, time DESC)`. Konvention 22 verlangt Indizes auf Hypertables,
   die mit `organization_id` oder `probe_id` beginnen. Abfragen je Probe
   (WP-975) brauchen den zweiten. Seine Größe ist nicht gemessen und wird
   wie beim gemessenen Index `(organization_id, time)` auf ≈ 11 B je Zeile
   geschätzt.
5. **Schreibfenster:** Laufende Daten fallen in unkomprimierte Chunks. Mit
   Kompression ab 1 Tag (`compress_after` bezieht sich auf das Ende des
   Chunks) bleiben alle Daten der letzten 24 h unkomprimiert. Nachsynchronisierte
   Ergebnisse, die älter als 1 Tag sind (Spool bis 24 h, NFR-04), treffen
   komprimierte Chunks. Das funktioniert und ist idempotent, ist aber der
   langsamere Weg; der Lasttest (WP-1084) misst die
   Nachsynchronisationsspitze auch für diesen Fall.
6. **Aggregationsstufen nach E-172 bestätigt:** 5 min (90 Tage) → 1 h
   (400 Tage) → 1 d (400 Tage) als hierarchische Continuous Aggregates. Die
   Migration von WP-972 legt sie in einer Transaktion mit `WITH NO DATA`
   an. Jede Stufe speichert Summen (`total`, `ok`, `rtt_sum`, `rtt_n`,
   `rtt_min`, `rtt_max`, entsprechend für Verlust und Wert); Mittelwerte
   entstehen erst beim Lesen. Summen von `real`-Spalten (RTT, Wert) sind
   `double precision` (`sum(x::float8)`, oben `sum(x_sum)`), Zähler
   `bigint`. Die Retention der 5-min-Stufe läuft als
   TimescaleDB-Policy; Zustandswechsel und Tagesverfügbarkeit behält der Job
   aus WP-974 400 Tage. NFR-06 ist für Probe-Daten mit den Stufen 1 h und
   1 d, den Zustandswechseln und der Tagesverfügbarkeit erfüllt
   (Beschluss vom 2026-11-09 zu E-172); `metric_sample` behält MON-01.
7. **Rechteentzug auf TimescaleDB 2.31:** Die Revoke-Schleife von WP-945
   und WP-972 erfasst Hypertable, Chunks (über `pg_inherits`), die
   Materialisierungen und deren Chunks, die Partial- und Direct-Views und
   zusätzlich die Relationen `<chunk>_compressed` über ihren Namen. Eine
   komprimierte Hypertable gibt es in 2.31 nicht mehr. Die Relationen
   `<chunk>_compressed` erben die ACL der Hypertable, und `GRANT`/`REVOKE`
   auf die Hypertable wirken dort weiter. WP-945 führt deshalb `REVOKE ALL`
   auf die Hypertable in derselben Migration vor jeder Kompression aus und
   nimmt ihn nie zurück. Sein Katalogtest prüft auf jeder Relation
   einschließlich `<chunk>_compressed`
   `has_table_privilege('reticora_app', …) = false` (nicht eine leere ACL),
   so wie `TestProbeStorageSpikeAppRoleHasNoDirectAccess`.

## Folgen

- WP-945 legt `probe_result` mit 3-h-Chunks, Kompression ab 1 Tag,
  `segmentby organization_id, probe_id`, `orderby time DESC, collector_id,
  seq`, Retention 14 Tage und den Indizes aus Entscheidung 4 an. Ein Titel
  oder Text mit „6-h-Chunks“ gilt nach diesem ADR als 3 h (Konvention:
  WP-945 übernimmt die Werte des ADR).
- WP-972 legt die drei Stufen mit Summenspalten, Refresh- und
  Kompressions-Policies und der Retention der 5-min-Stufe an.
- `docs/ops/sizing.md` führt den Monitoring-Bedarf (Abschnitt
  „Monitoring“).
- Der WAL-Strom (14–17,5 GB am Tag bei 353/s, 42–53 GB bei 1.060/s) fließt
  in das WAL-Archiv (PITR) und dessen Aufbewahrung ein.
- Ein TimescaleDB-Upgrade, das die Ablage komprimierter Daten erneut ändert,
  macht `TestProbeStorageSpikeAppRoleHasNoDirectAccess` rot (zu wenige
  gefundene Relationen) und erfordert eine Prüfung dieses ADR.
