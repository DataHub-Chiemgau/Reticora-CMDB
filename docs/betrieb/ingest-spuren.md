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

- `1` ist die serielle Verarbeitung je Instanz (Rückfall, Option 3 von E-56
  per Konfiguration): eine Spur, ein Mutex je Organisation. Identitätssperre,
  `max_cis`-Sperre und Sofort-Wiederholung bleiben für jeden Record aus der
  Queue aktiv, denn andere Instanzen, der Altconsumer und Drain-Spuren können
  dieselbe Organisation gleichzeitig verarbeiten (eine Anweisung mehr je
  Transaktion, siehe „Kosten je Transaktion“).
- Ohne NATS (`RETICORA_NATS_URL` leer) verarbeitet der Server den Ingest in
  der Anfrage, seriell je Organisation und Prozess, wie vor WP-334 ohne
  Sperren. Mehrere Instanzen ohne NATS sperren sich dabei nicht gegenseitig
  (vorbestehend); in der Produktion deshalb mit NATS betreiben.
- Im Speicherbetrieb (`--no-db` mit NATS) kann das Repository nicht sperren.
  Ein Prozess verarbeitet dann jede Organisation seriell, unabhängig von
  `RETICORA_INGEST_LANES`; die Subjects bleiben je Spur.
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
  Organisation und Spur; mit einer Spur und im Speicherbetrieb ein Mutex je
  Organisation für alle Consumer).
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
  Batch-Transaktion eines Records aus der Queue nimmt dazu als erste
  Anweisung transaktionsgebundene Advisory-Locks: die Identitätssperre der
  Organisation geteilt, dann die Schlüssel, sortiert. Die Identitätsauflösung
  sieht damit, was eine serielle Verarbeitung gesehen hätte: ein Gerät wird
  nicht zu zwei CIs (PRI-10), auch wenn sich seine Records nur über nicht
  eindeutige Merkmale (IP und sysObjectID, Interface-MACs) erkennen.
- **Unabhängig von der Spurzahl:** Auch mit einer Spur konsumiert jede
  Instanz Spur 0, den Altconsumer und alle Drain-Spuren, jeweils mit eigenem
  `MaxAckPending`, und andere Instanzen tun dasselbe. Die Sperren gelten
  deshalb für jeden Record aus der Queue, mit einer Spur wie mit mehreren,
  auch wenn Instanzen mit verschiedener Spurzahl nebeneinander laufen.
- **Sperrform:** Identitäts- und `max_cis`-Sperren nutzen die
  Zwei-Argument-Form `pg_advisory_xact_lock(klasse, schlüssel)` mit eigenen
  Klassen: Identitätssperre der Organisation `0x52496f72` (1380544370),
  Identitätsschlüssel `0x52496b79` (1380543353), `max_cis` `0x52436e74`
  (1380150900). Sie unterscheiden sich von den Klassen von Audit-Kette,
  Outbox und Publisher und können keiner Ein-Argument-Sperre gleichen (etwa
  der Sperre des Scheduler-Leiters). Der Schlüssel in der Klasse sind die
  ersten 4 Byte des SHA-256 der Organisation und des Werts; eine gezielte
  Kollision serialisiert nur zwei Ingest-Transaktionen.
- **Wartegrenze:** Jede Sperranweisung wartet höchstens 20 s
  (`lock_timeout`, in derselben Anweisung gesetzt und zurückgesetzt; andere
  Wartezeiten der Transaktion bleiben unverändert). Danach scheitert die
  Transaktion mit 55P03 und wird wiederholt (siehe „Konflikte zwischen
  Spuren“). Auch fünf solche Wartezeiten einer Zustellung (Batch und vier
  Record-Versuche) enden innerhalb von `AckWait` (2 min).
- **Obergrenze der Sperren:** Eine Transaktion sperrt höchstens 256
  Schlüssel. Ein Batch endet vor dem Record, der die Grenze überschreiten
  würde (z. B. 5 Switches mit 48 Ports, je 51 Schlüssel); ein Record mit mehr
  als 256 Schlüsseln (Stack oder Chassis mit vielen Port-MACs, Record mit
  Tausenden MACs) nimmt stattdessen die Identitätssperre der Organisation
  **exklusiv** und läuft damit allein gegen alle anderen sperrenden
  Transaktionen der Organisation, über alle Spuren und Instanzen. Grund:
  Advisory-Locks belegen je einen Eintrag der gemeinsamen Lock-Tabelle des
  PostgreSQL-Clusters (siehe „Kosten je Transaktion“).
- **Feldwerte:** REC-03/REC-05 entscheiden unabhängig von der
  Verarbeitungsreihenfolge; eine ältere Beobachtung überschreibt keine neuere.
  Rest: `raw_data` und der Fingerprint-Merge folgen der Verarbeitungsreihenfolge,
  wenn die Records eines Geräts in verschiedenen Spuren liegen.
- **Topologie:** Kanten zu einem Nachbarn, den eine andere Spur im selben
  Moment neu anlegt, entstehen spätestens im nächsten Discovery-Zyklus.
- **max_cis (ENT-03):** Für Records aus der Queue zählt jede anlegende
  Transaktion die CIs unter einer Sperre der Organisation neu: nahe am Limit
  exklusiv (exakt), sonst geteilt. Ausnahme: Während Drain-Spuren (siehe
  unten) Rückstand haben, kann jede von ihnen das Limit um höchstens 100
  überschreiten.
- **Konflikte zwischen Spuren:** Ein Deadlock (40P01), ein
  Serialisierungsfehler (40001) oder ein Unique-Index auf Seriennummer,
  Hardware-UUID oder Primär-MAC, den eine andere Spur oder Instanz zuerst
  belegt hat, wiederholt einen Record aus der Queue (mit einer Spur wie mit
  mehreren) sofort in einer neuen Transaktion (höchstens dreimal,
  Metrik `reticora_ingest_record_retries_total`). „out of shared memory“
  (53200, meist eine volle Lock-Tabelle) und eine überschrittene Wartegrenze
  der Sperren (55P03) wiederholen ihn ebenso, aber nach 100, 200 und 300 ms.
  Die Queue sieht diese Wiederholungen nicht; sie zählen nicht gegen
  `MaxDeliver`.

## Upgrade und Altconsumer

Der Consumer der Releases vor WP-334 (`reticora-ingest`, bisher ohne Filter)
bleibt dauerhaft bestehen. Beim Start setzt der Server seinen Filter auf
`ingest.*.*`, bevor er Spur-Consumer anlegt (ein WorkQueue-Stream verlangt
disjunkte Filter). Nachrichten, die beim Upgrade noch im Stream liegen, bleiben
erhalten und werden über diesen Consumer verarbeitet (`/readyz`: Spur
`legacy`); danach läuft er leer.

- Startet während eines Rolling Upgrades eine Instanz des alten Releases neu,
  setzt sie den Consumer wieder ohne Filter (NATS 2.10 nimmt das an). Sie
  sieht dann nur Nachrichten, die danach veröffentlicht werden; neue
  Instanzen stellen den Filter innerhalb von 30 s wieder her (Log
  `ingest.legacy_filter_restored`) und verarbeiten die Spuren weiter.
- Der Server führt `reticora-ingest` wie eine eigene Spur: Fehlt er, legt
  jede laufende Instanz ihn wieder an, beim nächsten Abruf (Log
  `ingest.lane.recreated`) und spätestens nach 30 s (Log
  `ingest.legacy_filter_restored`). Ihn zu löschen ist deshalb nicht
  vorgesehen und wirkt nicht, solange eine Instanz dieses Releases läuft.
  Leer kostet er je Ingest-Instanz einen Abruf pro Sekunde.
- Beim Rollback auf ein Release vor WP-334 übernimmt dieses ihn wieder
  (siehe „Rollback auf ein Release vor WP-334“).

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
- Beim Wechsel kann ein Gerät für kurze Zeit in zwei Spuren Nachrichten haben,
  und Instanzen mit alter und neuer Spurzahl laufen nebeneinander. Weil jede
  Transaktion eines Records aus der Queue die Sperren nimmt, auch auf
  Instanzen mit einer Spur, halten die Identitätssperre und REC-03 das
  Ergebnis richtig, auch beim Wechsel von oder auf `1`.

## Rückfall auf eine Spur

`RETICORA_INGEST_LANES=1` auf allen Instanzen setzen und neu starten. Die
Spuren `1` … `n-1` werden als Drain-Spuren geleert; es geht nichts
verloren. Solange Drain-Spuren oder der Altconsumer Rückstand haben,
verarbeiten mehrere Consumer dieselbe Organisation gleichzeitig; die
Identitäts- und `max_cis`-Sperre bleiben dafür auch mit einer Spur aktiv
(siehe „Garantien“).

## Rollback auf ein Release vor WP-334

Ein Release vor WP-334 liest Nachrichten, die schon in Spur-Subjects
(`ingest.<org>.<spur>.<collector>`) liegen, nicht: Sein ungefilterter
Consumer beginnt hinter ihnen, und solange eine neue Instanz läuft,
veröffentlicht sie weiter in Spur 0. Was beim Rollback in einer Spur liegt,
verfällt nach 48 h (die Collectors haben dafür 202 erhalten und senden nicht
erneut). Ein Rolling Rollback ist deshalb nicht verlustfrei. Ohne Verlust
geht es so:

1. `RETICORA_INGEST_LANES=1` auf allen Instanzen setzen und neu starten.
2. Annahme stoppen: Collector-Edge trennen (die Collectors puffern im
   Spool) und `POST /api/v1/ingest/bulk` sowie
   `POST /api/v1/discovery/ingest` am Ingress sperren.
3. Warten, bis `/readyz` einer Instanz mit Ingest-Worker
   `details.ingest.total` = 0 zeigt (alle Spuren einschließlich `0` und
   `legacy`), in einer Stichprobe, deren `sampled_at` nach dem Stopp der
   Annahme liegt.
4. Alle Instanzen des neuen Releases beenden (Strategie `Recreate`, kein
   Rolling Update).
5. Die leeren Spur-Consumer löschen, für jedes n aus
   `nats consumer ls RETICORA_INGEST`:
   `nats consumer rm RETICORA_INGEST reticora-ingest-lane-<n>`. Sonst lehnt
   NATS jede Neuanlage des ungefilterten Consumers `reticora-ingest` ab
   („multiple non-filtered consumers not allowed on workqueue stream“),
   etwa nach der Wiederherstellung eines NATS-Clusters, und das alte
   Release startet nicht. `reticora-ingest` bleibt stehen; das alte Release
   setzt ihn beim Start wieder ohne Filter.
6. Das alte Release starten.
7. Annahme wieder öffnen; die Collectors senden ihren Spool.

## Rückstand beobachten

- `/readyz` (ohne Anmeldung) zeigt unter `details.ingest` die Spurzahl und den
  Rückstand je Spur (wartende plus unbestätigte Nachrichten), summiert über
  alle Organisationen; das ändert den Status nie. Die Werte sind die letzte
  Stichprobe des Ingest-Workers (alle 30 s, Zeitpunkt in `sampled_at`); eine
  Anfrage an `/readyz` fragt NATS nicht ab:

  ```json
  {"status":"ready","checks":{"database":"ok","nats":"ok","redis":"ok"},
   "details":{"ingest":{"lanes":4,"backlog":{"0":12,"1":0,"2":7,"3":0,"legacy":0},"total":19,
    "sampled_at":"2026-10-15T08:30:00.125Z"}}}
  ```

  - Nur Instanzen mit Ingest-Worker (`RETICORA_WORKER_INGEST_ENABLED`,
    Standard `true`) melden das Detail. Auf API-Instanzen ohne Ingest-Worker
    fehlt `details.ingest`; den Rückstand dort an einer Ingest-Instanz oder
    über die Metriken unten lesen.
  - Vor der ersten Stichprobe nach dem Start steht dort
    `{"error":"unavailable"}`. Schlägt eine Stichprobe fehl, bleibt die
    vorige mit ihrem Zeitpunkt stehen (Log `worker backlog probe failed`);
    ein altes `sampled_at` zeigt das an.
  - Das Detail ist auf 300 ms begrenzt, unabhängig von den Checks (2 s), und
    `/readyz` antwortet auch dann sofort. Die Readiness-Probe in
    `deploy/k8s/base/deployment.yaml` wartet 3 s (`timeoutSeconds`), länger
    als die Check-Grenze.

- Metriken (alle 30 s):

  | Metrik | Bedeutung |
  |---|---|
  | `reticora_ingest_lanes` | Spurzahl dieser Instanz |
  | `reticora_ingest_lane_backlog{lane}` | Rückstand je Spur (`legacy` = Altconsumer) |
  | `reticora_ingest_lane_records_total{lane}` | bestätigte Nachrichten je Spur |
  | `reticora_ingest_record_retries_total{reason}` | Wiederholungen im Prozess (`deadlock`, `serialization`, `identity`, `near_limit`, `out_of_memory`, `lock_timeout`) |
  | `reticora_worker_backlog{worker="ingest"}` | Summe aller Spuren (bestehende Alerts) |

- Eine einzelne hängende Spur fällt in der Summe kaum auf: steigt
  `reticora_ingest_lane_backlog` einer Spur über 15 min, die Logs der Instanz
  prüfen (`ingest message redelivered`, `ingest message given up`).

## Kosten je Transaktion

Für Records aus der Queue kommt je Record- und Batch-Transaktion eine
Anweisung hinzu (die Identitätssperre samt Wartegrenze), mit einer Spur wie
mit mehreren: ein Batch von 20 neuen Geräten braucht 11 statt 10
Anweisungen, ein neuer Record einzeln 13 statt 12. Updates bekannter Geräte
lesen ihre Kandidaten unter der Sperre neu (2 Anweisungen mehr).
Organisationen mit `max_cis` zählen je anlegender Transaktion einmal neu
(2 Anweisungen). Ohne NATS (Ingest in der Anfrage) bleibt es bei 10 und 12
Anweisungen.

### Lock-Tabelle bemessen

Jede Advisory-Sperre ist ein Eintrag der gemeinsamen Lock-Tabelle des
PostgreSQL-Clusters, die sich alle Datenbanken, Mandanten und Dienste
teilen; ist sie voll, scheitern beliebige Transaktionen mit „out of shared
memory“ (53200). Die Tabelle fasst `max_locks_per_transaction` ×
(`max_connections` + `max_prepared_transactions`) Einträge, mit den
PostgreSQL-Standardwerten 64 × (100 + 0) = 6.400.

Eine Ingest-Instanz hält höchstens 6 × 257 = 1.542 Identitätssperren
(6 Batches gleichzeitig, je höchstens 256 Schlüssel plus die Sperre der
Organisation), dazu je Transaktion höchstens eine `max_cis`-Sperre.
Ingest-Instanz ist jede Instanz mit Ingest-Worker
(`RETICORA_WORKER_INGEST_ENABLED`, Standard `true`). Bemessung:

```text
max_locks_per_transaction × (max_connections + max_prepared_transactions)
    ≥ Ingest-Instanzen × 1.542 + Reserve
```

Die Reserve deckt die übrigen Sperren aller Sitzungen (Tabellen, Indizes,
andere Advisory-Sperren). Mit Standardwerten reicht die Tabelle für
höchstens zwei Ingest-Instanzen mit Reserve.

- **Ausgelieferte Kubernetes-Konfiguration:** In `deploy/k8s/base/` laufen
  alle Worker in jedem Replikat, und der HPA (`hpa.yaml`) skaliert auf bis
  zu 10 Replikate: 10 × 1.542 = 15.420 Einträge. Bei `max_connections` = 100
  reichen dafür weder 64 (6.400) noch 128 (12.800);
  `max_locks_per_transaction` = 256 ergibt 25.600 Einträge und lässt gut
  10.000 Reserve. Die Einstellung wirkt erst nach einem Neustart von
  PostgreSQL.
- **Eigene Ingest-Instanzen:** Alternativ `RETICORA_WORKER_INGEST_ENABLED=false`
  auf den API-Instanzen setzen und den Ingest-Worker auf einer festen Zahl
  eigener Instanzen laufen lassen (`docs/ops/workers.md`). Die API
  veröffentlicht weiter in die Queue; die Zahl der Instanzen mit
  Identitätssperren hängt dann nicht mehr von der Skalierung der API ab.
  Mehr Ingest-Instanzen erhöhen den Durchsatz ohnehin nur mit mehr Spuren
  (siehe „Einstellung“).

## Lastnachweis

`tests/load/ingest.js` (Szenarien `load` und `spool`, siehe
`tests/load/README.md`). Der Workflow `.github/workflows/load.yml` führt jedes
Szenario als eigenen Job aus, mit frischer Datenbank, Seed, `ANALYZE` und
4 Spuren.
