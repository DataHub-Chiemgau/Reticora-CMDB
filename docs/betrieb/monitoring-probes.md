# Monitoring: Probetypen und ihre Grenzen

Das Register der Monitoring-Probetypen ist der Vertrag
[`api/probe-types.json`](../../api/probe-types.json) (PRT-01, E-154). Server
und Collector prüfen jede Probe gegen dieselbe Datei; das Paket
`edgecore/probewire` bettet eine Kopie ein (`go generate ./probewire` in
`edgecore/`) und stellt `Types()`, `Lookup(key)` und `Validate(spec)` bereit.
Ein Konformitätstest hält Kopie, Vertrag und die Tabelle unten gleich.

## Drei Ebenen von Grenzen

1. **Systemgrenzen je Typ** (dieses Register): Mindest- und Höchstintervall,
   Timeout-Grenzen, Parallelität je Ziel und je Collector, Rate je
   Collector, Probes je Ziel, Payload- und Zeilengrenzen. Sie schützen
   Zielgeräte und Collector und sind nicht verhandelbar.
2. **Paketgrenzen je Organisation** (Entitlement, PKG-04): Ziele gesamt,
   Prüfungen pro Minute, Mindestintervall und optional `max_targets_<typ>`.
   `<typ>` ist immer ein Schlüssel dieses Registers; Langformen wie
   `max_targets_icmp_ping` werden mit 422 auf `limits` abgewiesen.
   Paketgrenzen können die Systemgrenzen nur verschärfen.
3. **Werte der Probe** (PRB-01): Standardwerte des Typs, überschreibbar je
   Organisation, Kunde, Monitoring-Gruppe, CI und Probe innerhalb beider
   Grenzen.

## Regeln eines Eintrags

- `min_interval_s ≤ default_interval_s ≤ max_interval_s`,
  `min_timeout_s ≤ default_timeout_s ≤ max_timeout_s`,
  `default_timeout_s < default_interval_s`; alle Grenzen sind größer 0.
- Je Probe gilt `timeout < interval` (PRT-02; erfüllt PRT-01 „Timeout ≤
  Intervall“). Bei `icmp` mit Intervall 10 s ist der Timeout daher höchstens
  9 s.
- Down- und Up-Count liegen je Probe zwischen 1 und 20.
- Parameter folgen dem JSON-Schema `params` des Eintrags; unbekannte
  Parameter sind ein Feldfehler (`params.<name>`).
- Typen mit Status `planned` sind nicht anlegbar (422 auf `type`). Die
  Typ-Arbeitspakete setzen ihren Typ auf `available`.

## ICMP-Ping (`icmp`)

Standard nach PRT-02 und E-177: Intervall 30 s (10 s–1 h), Timeout 10 s
(1–10 s), Down-Count 5 (1–20), Up-Count 1. Parameter: `count` 3 Echo-Requests
je Prüfung (1–5), `spacing_ms` 1000, `payload_bytes` 56 (0–1472),
`rtt_warn_ms` ohne Standard, `loss_warn` 0,5. Mehrere Pakete je Prüfung
liefern Verlust und RTT-Streuung, ohne die Prüfrate zu erhöhen.

## TCP-Connect (`tcp`)

Standard nach PRT-03: Intervall 60 s (10 s–24 h), Timeout 5 s (1–30 s, je
Probe unter dem Intervall), Down-Count 3, Up-Count 1, höchstens 2
gleichzeitige Prüfungen je Ziel. Parameter: `port` 1–65535 (Pflicht),
`send` (optional, höchstens 256 Byte; `\r`, `\n`, `\t` und `\\` werden
dekodiert), `expect` (optional, RE2-Ausdruck mit höchstens 1024 Zeichen),
`family` `auto|v4|v6` (Standard `auto`), Schwelle `rtt_warn_ms` für den
Verbindungsaufbau ohne Standard.

Ablauf: Verbindungsaufbau; nur bei gesetztem `send` wird dieser String
gesendet, nur bei gesetztem `expect` wird die Antwort gelesen, höchstens
1 KiB oder bis zum Timeout. Ohne `send` und `expect` schließt der Collector
die Verbindung sofort nach dem Aufbau und sendet nichts.

| Ergebnis | Bedeutung | Zustand |
|---|---|---|
| `ok` | Aufbau gelungen (und `expect` getroffen) | `up`; `degraded`, wenn die Aufbauzeit `rtt_warn_ms` überschreitet |
| `fail`/`refused` | Gegenstelle lehnt ab (RST) | zählt auf `down` |
| `fail`/`timeout` | kein Aufbau bzw. keine Antwort bis zum Timeout | zählt auf `down` |
| `fail`/`unreachable` | ICMP unreachable (Host oder Netz) | zählt auf `down` |
| `fail`/`expect_mismatch` | Antwort gelesen, `expect` nicht getroffen | zählt auf `down` (verletzter Sollwert, E-177) |
| `error`/`config` | ungültige Parameter (Feld im Label `field`) | `unknown` |

Messwert: Die Aufbauzeit steht als `rtt_ms` im Ergebnis und heißt als
Metrik `probe_tcp_connect_seconds`.

## Typtabelle

Zeiten in Sekunden, Rate in Prüfungen je Sekunde. Die Werte sind Startwerte
nach E-154; WP-1023 misst die Raten nach.

<!-- probe-types:begin -->
| Schlüssel | Status | Intervall Std. / Min. / Max. (s) | Timeout Std. / Min. / Max. (s) | Down / Up | Parallel je Ziel | Parallel je Collector | Rate je Collector (/s) | Probes je Ziel | Payload (Bytes) | Zeilen | Namespaces | Fähigkeiten |
|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---|---|
| `icmp` | available | 30 / 10 / 3600 | 10 / 1 / 10 | 5 / 1 | 1 | 256 | 1000 | 4 | 1472 | 1 | – | icmp |
| `tcp` | available | 60 / 10 / 86400 | 5 / 1 / 30 | 3 / 1 | 2 | 128 | 500 | 32 | 1024 | 1 | – | – |
| `http` | planned | 60 / 30 / 86400 | 10 / 1 / 60 | 3 / 1 | 2 | 64 | 100 | 32 | 1048576 | 1 | http | – |
| `snmp_status` | planned | 60 / 30 / 86400 | 5 / 1 / 15 | 3 / 1 | 1 | 32 | 200 | 4 | 65536 | 20 | snmp | – |
| `snmp_if` | planned | 300 / 60 / 86400 | 10 / 1 / 60 | 3 / 1 | 1 | 16 | 50 | 4 | 1048576 | 1024 | snmp | – |
| `snmp_custom` | planned | 300 / 60 / 86400 | 10 / 1 / 60 | 3 / 1 | 1 | 16 | 100 | 8 | 65536 | 256 | snmp | – |
| `redfish` | planned | 300 / 60 / 86400 | 15 / 1 / 60 | 3 / 1 | 1 | 8 | 20 | 2 | 1048576 | 256 | redfish | – |
| `bgp` | planned | 60 / 30 / 86400 | 10 / 1 / 60 | 3 / 1 | 1 | 16 | 50 | 2 | 1048576 | 4096 | snmp, gnmi | – |
| `ospf` | planned | 60 / 30 / 86400 | 10 / 1 / 60 | 3 / 1 | 1 | 16 | 50 | 2 | 1048576 | 1024 | snmp, gnmi | – |
| `isis` | planned | 60 / 30 / 86400 | 10 / 1 / 60 | 3 / 1 | 1 | 16 | 50 | 2 | 1048576 | 1024 | snmp, gnmi | – |
| `bfd` | planned | 30 / 10 / 86400 | 5 / 1 / 15 | 3 / 1 | 1 | 16 | 50 | 2 | 1048576 | 1024 | snmp | – |
| `optical` | planned | 300 / 60 / 86400 | 15 / 1 / 60 | 3 / 1 | 1 | 8 | 20 | 2 | 1048576 | 1024 | snmp, gnmi | – |
| `bng` | planned | 60 / 30 / 86400 | 10 / 1 / 30 | 3 / 1 | 1 | 8 | 20 | 2 | 1048576 | 1024 | snmp | – |
| `pon` | planned | 300 / 120 / 86400 | 60 / 1 / 120 | 3 / 1 | 1 | 4 | 5 | 2 | 4194304 | 8192 | snmp | – |
| `dns` | planned | 60 / 10 / 86400 | 5 / 1 / 15 | 3 / 1 | 4 | 64 | 200 | 16 | 65536 | 1 | – | – |
| `dhcp` | planned | 300 / 60 / 86400 | 10 / 1 / 30 | 3 / 1 | 1 | 1 | 1 | 1 | 4096 | 1 | – | privileged_udp_ports |
| `stamp` | planned | 60 / 10 / 86400 | 5 / 1 / 30 | 3 / 1 | 1 | 16 | 20 | 4 | 1472 | 1 | – | – |
| `twamp_control` | planned | 60 / 10 / 86400 | 5 / 1 / 30 | 3 / 1 | 1 | 16 | 20 | 4 | 1472 | 1 | – | – |
| `gnmi` | planned | 60 / 10 / 86400 | 10 / 1 / 60 | 3 / 1 | 1 | 16 | 50 | 4 | 4194304 | 10000 | gnmi | – |
| `tr069` | planned | 900 / 300 / 86400 | 30 / 1 / 60 | 3 / 1 | 1 | 4 | 5 | 1 | 1048576 | 256 | acs | – |
| `usp` | planned | 900 / 300 / 86400 | 30 / 1 / 60 | 3 / 1 | 1 | 4 | 5 | 1 | 1048576 | 256 | acs | – |
| `rpki_rtr` | planned | 300 / 60 / 86400 | 30 / 1 / 120 | 3 / 1 | 1 | 4 | 1 | 1 | 67108864 | 2000000 | – | – |
| `rpki_roa` | planned | 300 / 60 / 86400 | 1 / 1 / 5 | 3 / 1 | 1 | 64 | 100 | 100 | 65536 | 100 | – | – |
<!-- probe-types:end -->

Payload- und Zeilengrenzen (`max_payload_bytes`, `max_rows`) stehen in den
Spalten „Payload (Bytes)“ und „Zeilen“; die Parameterschemas stehen nur im
Vertrag. `rpki_roa` hat nach E-154 keine
eigene Parallelitätsgrenze je Ziel; das Register führt 1, weil jede Grenze
größer 0 sein muss.
