# Ingest-Spuren (`RETICORA_INGEST_LANES`)

Anforderungen: NFR-03 ([B], Ingest-Durchsatz je Instanz), REC-01, REC-02,
REC-05, ENT-03, OPS-05. Umsetzung: WP-334, Entscheidung E-56 (Option 1 mit
Identitätssperre).

## Zweck

Der Ingest einer Organisation lief bis WP-333 streng seriell: ein Consumer,
ein Batch zugleich, ein Mutex je Organisation. Der Durchsatz einer
Organisation war damit der Kehrwert der Zeit je Record. Mit Spuren verteilt
der Server die Records einer Organisation auf mehrere JetStream-Consumer, die
nebeneinander arbeiten.

## Einstellung

| Variable | Standard | Werte | Hinweis |
|---|---|---|---|
| `RETICORA_INGEST_LANES` | `4` | `1` … `16` | Auf **allen** Instanzen gleich setzen. Ein Wert außerhalb 1 … 16 verhindert den Start. |

- `1` ist die serielle Verarbeitung der Releases vor WP-334 (Rückfall, Option 3
  von E-56 per Konfiguration): eine Spur, ein Mutex je Organisation, keine
  Identitätssperre, kein `max_cis`-Lock, keine Sofort-Wiederholung.
- Größenordnung: etwa 4 Spuren je Ingest-Instanz, soweit die Datenbank es
  trägt. Die Parallelität ist **clusterweit** etwa die Spurzahl, nicht
  Spurzahl × Instanzen: `MaxAckPending` (100) gilt je Spur über alle
  Instanzen. Mehr Instanzen erhöhen den Ingest-Durchsatz daher nur mit mehr
  Spuren.
- Je Instanz arbeiten höchstens `max(1, 25/4)` = 6 Batches gleichzeitig
  (ein Viertel des Datenbank-Pools); weitere Spuren warten. Die API behält so
  ihre Verbindungen.
- NATS ab Version 2.10 (Filter eines bestehenden Consumers ändern).

## Funktionsweise

- **Subject:** `ingest.<org>.<spur>.<collector>` (`api` ohne Collector). Die
  Releases davor veröffentlichten `ingest.<org>.<collector>`.
- **Spur eines Records:** FNV-1a (32 Bit) des stärksten Identitätsschlüssels
  modulo `RETICORA_INGEST_LANES`, in der Reihenfolge der Identitätsauflösung
  (REC-02): nutzbare Seriennummer (ohne Platzhalter, Groß-/Kleinschreibung
  egal), nutzbare Hardware-UUID (ohne Klon-UUIDs), MAC (Primär-MAC zuerst;
  lokal verwaltete und VRRP/HSRP/CARP-MACs zählen nicht), Management-IP.
  Records ohne solchen Schlüssel laufen in Spur 0; der Hostname ist kein
  Schlüssel (CH29).
- **Consumer:** `reticora-ingest-lane-<n>` mit Filter `ingest.*.<n>.*`,
  `MaxAckPending` 100, `AckWait` 2 min, `MaxDeliver` 5. Je Spur und
  Organisation verarbeitet eine Instanz einen Batch zugleich (Mutex je
  Organisation und Spur).
- **Collector-Edge:** Records über den mTLS-NATS-Kanal (COL-08) laufen über
  dieselbe Queue und dieselben Subjects.

## Garantien (E-56)

- **Reihenfolge:** Innerhalb einer Spur gilt die Eingangsreihenfolge über
  Batches und `observed_at` innerhalb eines Batches. Records eines Geräts mit
  demselben stärksten Schlüssel bleiben in einer Spur. Zwischen Spuren gibt es
  keine Reihenfolge.
- **Identitätssperre:** Records, die einen Identitätsschlüssel teilen
  (Seriennummer, Hardware-UUID, jede MAC einschließlich Fingerprint- und
  Interface-MACs, Management-IP je VRF), werden nie gleichzeitig verarbeitet,
  auch nicht über Spuren oder Instanzen hinweg. Jede Record- und
  Batch-Transaktion nimmt dazu als erste Anweisung transaktionsgebundene
  Advisory-Locks auf diese Schlüssel, sortiert. Die Identitätsauflösung sieht
  damit, was eine serielle Verarbeitung gesehen hätte: ein Gerät wird nicht
  zu zwei CIs (PRI-10), auch wenn sich seine Records nur über
  nicht eindeutige Merkmale (IP und sysObjectID, Interface-MACs) erkennen.
- **Feldwerte:** REC-03/REC-05 entscheiden unabhängig von der
  Verarbeitungsreihenfolge; eine ältere Beobachtung überschreibt keine neuere.
  Rest: `raw_data` und der Fingerprint-Merge folgen der Verarbeitungsreihenfolge,
  wenn die Records eines Geräts in verschiedenen Spuren liegen.
- **Topologie:** Kanten zu einem Nachbarn, den eine andere Spur im selben
  Moment neu anlegt, entstehen spätestens im nächsten Discovery-Zyklus.
- **max_cis (ENT-03):** Mit mehr als einer Spur zählt jede anlegende
  Transaktion die CIs unter einer Sperre der Organisation neu: nahe am Limit
  exklusiv (exakt), sonst geteilt. Ausnahme: Während Drain-Spuren (siehe
  unten) Rückstand haben, kann jede von ihnen das Limit um höchstens 100
  überschreiten.
- **Konflikte zwischen Spuren:** Ein Deadlock (40P01), ein
  Serialisierungsfehler (40001) oder ein Unique-Index auf Seriennummer,
  Hardware-UUID oder Primär-MAC, den eine andere Spur zuerst belegt hat,
  wiederholt den Record sofort in einer neuen Transaktion (höchstens dreimal,
  Metrik `reticora_ingest_record_retries_total`). Die Queue sieht diese
  Wiederholungen nicht; sie zählen nicht gegen `MaxDeliver`.

## Upgrade und Altconsumer

Der Consumer der Releases vor WP-334 (`reticora-ingest`, bisher ohne Filter)
bleibt bestehen. Beim Start setzt der Server seinen Filter auf `ingest.*.*`,
bevor er Spur-Consumer anlegt (ein WorkQueue-Stream verlangt disjunkte
Filter). Nachrichten, die beim Upgrade noch im Stream liegen, bleiben erhalten
und werden über diesen Consumer verarbeitet (`/readyz`: Spur `legacy`).

- Startet während eines Rolling Upgrades eine Instanz des alten Releases neu,
  setzt sie den Consumer wieder ohne Filter. NATS 2.10 nimmt das an; der alte
  Server verarbeitet dann auch Spur-Nachrichten. Neue Instanzen stellen den
  Filter innerhalb von 30 s wieder her (Log
  `ingest.legacy_filter_restored`).
- Der Server löscht `reticora-ingest` nie. Entfernen erst, wenn kein
  unterstütztes Release mehr dreiteilige Subjects veröffentlicht und die Spur
  `legacy` leer ist:
  `nats consumer rm RETICORA_INGEST reticora-ingest`. Danach ist ein Rollback
  auf ein Release vor WP-334 nicht mehr möglich.

## Spurzahl ändern

- **Erhöhen:** neue Spuren entstehen beim Start; Nachrichten in den alten
  Spuren werden weiter verarbeitet.
- **Verringern:** Spur-Consumer mit einer Nummer ≥ `RETICORA_INGEST_LANES`
  bleiben bestehen und werden von jeder Ingest-Instanz geleert
  (Drain-Spuren, Log `ingest.lane.drain`). Sobald alle Instanzen die neue
  Zahl haben und die Spur in `/readyz` den Rückstand 0 zeigt, kann sie
  entfernt werden: `nats consumer rm RETICORA_INGEST reticora-ingest-lane-<n>`.
  Laufende Instanzen beenden die Drain-Spur dann ohne Fehler
  (Log `ingest.lane.removed`).
- Beim Wechsel kann ein Gerät für kurze Zeit in zwei Spuren Nachrichten haben;
  die Identitätssperre und REC-03 halten das Ergebnis richtig.

## Rückfall auf eine Spur

1. `RETICORA_INGEST_LANES=1` auf allen Instanzen setzen und neu starten.
2. Warten, bis `/readyz` für die Spuren `1` … `n-1` den Rückstand 0 zeigt
   (`details.ingest.backlog`).
3. Erst danach ein Rollback auf ein Release vor WP-334 durchführen. Ein
   Release vor WP-334 kann Nachrichten in Spur-Subjects zwar über den
   ungefilterten Altconsumer lesen, dieser Weg hängt aber von einer
   Validierungslücke von NATS 2.10 ab und ist nicht zugesichert.

## Rückstand beobachten

- `/readyz` (ohne Anmeldung) zeigt unter `details.ingest` die Spurzahl und den
  Rückstand je Spur (wartende plus unbestätigte Nachrichten), summiert über
  alle Organisationen; das ändert den Status nie:

  ```json
  {"status":"ready","checks":{"database":"ok","nats":"ok","redis":"ok"},
   "details":{"ingest":{"lanes":4,"backlog":{"0":12,"1":0,"2":7,"3":0,"legacy":0},"total":19}}}
  ```

- Metriken (alle 30 s):

  | Metrik | Bedeutung |
  |---|---|
  | `reticora_ingest_lanes` | Spurzahl dieser Instanz |
  | `reticora_ingest_lane_backlog{lane}` | Rückstand je Spur (`legacy` = Altconsumer) |
  | `reticora_ingest_lane_records_total{lane}` | bestätigte Nachrichten je Spur |
  | `reticora_ingest_record_retries_total{reason}` | sofortige Wiederholungen (`deadlock`, `serialization`, `identity`, `near_limit`) |
  | `reticora_worker_backlog{worker="ingest"}` | Summe aller Spuren (bestehende Alerts) |

- Eine einzelne hängende Spur fällt in der Summe kaum auf: steigt
  `reticora_ingest_lane_backlog` einer Spur über 15 min, die Logs der Instanz
  prüfen (`ingest message redelivered`, `ingest message given up`).

## Kosten je Transaktion

Mit mehr als einer Spur kommt je Record- und Batch-Transaktion eine Anweisung
hinzu (die Identitätssperre): ein Batch von 20 neuen Geräten braucht 11 statt
10 Anweisungen, ein neuer Record einzeln 13 statt 12. Updates bekannter Geräte
lesen ihre Kandidaten unter der Sperre neu (2 Anweisungen mehr). Organisationen
mit `max_cis` zählen je anlegender Transaktion einmal neu (2 Anweisungen).

## Lastnachweis

`tests/load/ingest.js` (Szenarien `load` und `spool`, siehe
`tests/load/README.md`).
