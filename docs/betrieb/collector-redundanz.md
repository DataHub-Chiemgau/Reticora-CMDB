# Collector-Gruppen: Redundanz, Liveness und Failover

Anforderungen: PRB-03, PRB-10, PRB-11 ([P3]); Entscheidungen E-174 und
E-176. Gilt für den Server (System-Job `probe.assign` im Worker
`probe_assign`, Modul `monitoring`) und für Collectoren ab der Probe-Engine
(Lebenszeichen `.probe.alive`, WP-957).

Jede Probe gehört genau einer Collector-Gruppe. Der Server weist jede aktive
Probe genau einem gesunden Mitglied der Gruppe zu (`probe_assignment`) und
vergibt bei jeder Änderung eine neue Epoche (Fencing-Token). Diese Seite
beschreibt, wie der Server ausgefallene Mitglieder erkennt, wie die Probes
umziehen und welche Grenzfälle gelten.

## Modi der Gruppe

- **`distributed`** (Standard): Die Probes verteilen sich über gewichtetes
  Rendezvous-Hashing (`hash(probe_id, collector_id)` und `weight` des
  Mitglieds) auf die gesunden Mitglieder. Fällt ein Mitglied aus, ziehen nur
  seine Probes um; kehrt es zurück oder kommt ein Mitglied hinzu, wechseln
  nur die Probes, deren höchster Hash auf dieses Mitglied fällt (minimale
  Bewegung).
- **`failover`**: Alle Probes laufen auf dem gesunden Mitglied mit der
  höchsten `priority` (bei Gleichstand die kleinere Collector-ID). Fällt es
  aus, übernimmt das nächste; kehrt es zurück, gehen alle Probes wieder an
  es.

Gesund im Sinne der Zuweisung sind Mitglieder mit `member_status`
`healthy` oder `degraded` (Uhrabweichung über 60 s, WP-962; ein solches
Mitglied bleibt zuweisbar). Der COL-05-Status des Collectors (Heartbeat,
`offline` nach 5 min) entscheidet nicht über die Zuweisung.

## Lebenszeichen und Timeout

- Jeder Collector sendet für das Monitoring alle 10 s ein Lebenszeichen
  `.probe.alive` mit Uhrzeit, Konfigurationsversion (`config_version`),
  Lastzahlen und Pufferstand. Der Server schreibt die Empfangszeit als
  `collector_group_member.last_alive_at` (nie zurück).
- Der Liveness-Timeout ist eine Einstellung der Gruppe
  (`collector_group.liveness_timeout`, Standard 30 s, 20–300 s; API
  `liveness_timeout_s`).
- Ein Mitglied gilt als ausgefallen (`member_status = down`), wenn
  `jetzt − last_alive_at > liveness_timeout`. Das prüft der System-Job
  `probe.assign` bei jedem Lauf vor der Zuweisung (Liveness-Sweep).
- Ein ausgefallenes Mitglied wird mit dem zweiten Lebenszeichen innerhalb
  eines Timeouts wieder `healthy` (bzw. `degraded`, wenn die Uhr weiter um
  mehr als 60 s abweicht). Das zweite Lebenszeichen löst die Zuweisung der
  Gruppe sofort aus.
- Ein Mitglied ohne jedes Lebenszeichen (`last_alive_at` leer, etwa ein
  Collector ohne Probe-Engine oder vor dem ersten Start) prüft der Sweep
  nicht, und es bekommt keine Probes zugewiesen, auch wenn sein
  `member_status` noch der Standardwert `healthy` ist. Sein erstes
  Lebenszeichen macht es zuweisbar und löst die Zuweisung der Gruppe sofort
  aus.
- Der Heartbeat und der Offline-Status nach 5 min aus COL-05 bleiben davon
  unberührt.

## Takt des System-Jobs

`probe.assign` läuft alle 5 s (`RETICORA_PROBE_ASSIGN_TICK`, 1–30 s; nur
auf der führenden Replik des Schedulers, Schalter
`RETICORA_WORKER_PROBE_ASSIGN_ENABLED`). Je Lauf:

1. Liveness-Sweep je Organisation: Mitglieder über dem Timeout werden in
   der Transaktion, die ihre Gruppe unter der Sperre
   `probe_assign:<gruppe>` neu zuweist, auf `down` gesetzt. Ihre Probes
   erhalten das neue Mitglied mit Epoche + 1, die `config_version` der
   betroffenen Collectoren steigt, und nach dem Commit geht der
   Änderungshinweis `.probes.changed` an diese Collectoren.
2. Je übernommenem Mitglied ein Ereignis `collector_group.failover`
   (`collector_group_id`, `client_id`, `site_id`, `failed_collector_id`,
   `moved_probes`) über die Outbox in derselben Transaktion.
3. Zuweisung aller Gruppen wie bisher (neue, geänderte, deaktivierte
   Probes).

## Zeitkette bis zur Übernahme

Mit Standardwerten (Lebenszeichen alle 10 s, Timeout 30 s, Takt 5 s):

| Schritt | spätestens nach dem letzten Lebenszeichen von A |
|---|---|
| Timeout überschritten | 30 s |
| nächster Lauf von `probe.assign` setzt A `down` und weist neu zu | 35 s |
| Hinweis `.probes.changed` an B, B ruft den Satz ab | ≈ 35 s |
| nächstes Lebenszeichen von B trägt die neue `config_version` | 45 s |

Die Übernahme ist damit spätestens 60 s nach dem letzten Lebenszeichen von
A wirksam (PRB-03). Die erste Prüfung einer übernommenen Probe durch B folgt
in ihrer unveränderten Phase (WP-951), also spätestens ein Probe-Intervall
nach der Übernahme. Ein längerer Timeout verlängert die Kette um die
Differenz.

## Grenzfälle

**Split-Brain.** Ist A nur vom Server getrennt und prüft weiter, puffert er
seine Ergebnisse und Zustandswechsel mit der alten Epoche. Nach der
Wiederverbindung landen sie nur in der Historie (`stale = true`, kein
Ereignis `probe.state_changed`); der Zustand `probe_state` stammt
ausschließlich von B, dem Inhaber der gültigen Epoche (PRB-11). A übernimmt
beim nächsten Abruf den neuen Zuweisungssatz und entfernt die entzogenen
Probes; für sie entstehen keine Ergebnisse mehr.

**Rückkehr.** Mit dem zweiten Lebenszeichen innerhalb eines Timeouts ist A
wieder gesund, die Gruppe wird sofort neu zugewiesen. Im Modus
`distributed` wechseln nur die Probes, deren höchster Hash auf A fällt; im
Modus `failover` gehen alle Probes an das gesunde Mitglied mit der höchsten
Priorität, also zurück an A, wenn es die höchste hat. Die Rückkehr erzeugt
kein Ereignis `collector_group.failover`.

**Ausfall aller Mitglieder.** Bleibt kein gesundes Mitglied, ändert der Sweep
die Zuweisungen nicht und schreibt kein Ereignis `collector_group.failover`.
Diesen Fall behandelt die Netzdown-Semantik (WP-964): alle Probes der Gruppe
`unknown` mit Grund `collector_unreachable` und genau ein Root-Ereignis
`collector_group.unreachable`.

## Diagnose

- `GET /api/v1/collector-groups/{id}` zeigt je Mitglied `member_status`,
  `last_alive_at` und `config_version`.
- Log `collector group member down: no monitoring alive message within the
  liveness timeout` (mit `moved_probes`) und `collector group member alive
  again`.
- Ereignisse `collector_group.failover` im Ereignisstrom bzw. als Webhook.
