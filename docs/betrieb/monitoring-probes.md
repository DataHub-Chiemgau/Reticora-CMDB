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
(1–10 s und unter dem Intervall), Down-Count 5 (1–20), Up-Count 1; alle
Werte sind je Probe überschreibbar (E-154). Das Poll-Minimum von 5 min aus
DIS-10 gilt für diesen Typ nicht. Parameter:

| Parameter | Bereich | Standard | Bedeutung |
|---|---|---|---|
| `count` | 1–5 | 3 | Echo-Requests je Prüfung |
| `size` | 0–1472 Byte | 56 | Payload je Request (über IPv6 höchstens 1452 Byte) |
| `spacing_ms` | 200–1000 | 1000 | Abstand der Requests |
| `family` | `auto`, `v4`, `v6` | `auto` | Adressfamilie; `auto` nimmt die Familie eines Adressliterals, bei Hostnamen zuerst IPv6, dann IPv4 |
| `rtt_warn_ms` | ≥ 1 | – | Schwelle der mittleren RTT für `degraded` |
| `loss_warn` | 0–1 | 0,5 | Schwelle der Verlustquote für `degraded` |

Die Prüfung weist feldbezogen ab (422 bzw. `error/config` im Collector):
Timeout ≥ Intervall, Timeout außerhalb 1–10 s, Intervall außerhalb 10 s–1 h,
`(count − 1) × spacing_ms` ≥ Timeout (Feld `params.spacing_ms`, sonst
`params.count`), `size` außerhalb 0–1472 und `family` `v6` bei einem
IPv4-Literal oder `v4` bei einem IPv6-Literal (`params.family`).

Ergebnis: `ok` bei mindestens einer Antwort, `fail` mit `timeout` bei 100 %
Verlust; `rtt_ms` ist der Mittelwert, `loss` die Verlustquote, `values`
enthält `rtt_min_ms`, `rtt_max_ms` und ab zwei Antworten `jitter_ms`
(mittlerer Betrag der Differenz aufeinanderfolgender RTTs). `degraded`
entsteht in der Zustandsmaschine über `rtt_warn_ms` und `loss_warn`. Ohne
ICMP-Socket der Adressfamilie liefert der Collector `error` mit
`icmp_unavailable` (Zustand `unknown`, nie `down`). Mehrere Pakete je
Prüfung liefern Verlust und RTT-Streuung, ohne die Prüfrate zu erhöhen.

Messwertnamen für die Metrikbrücke:

| Serie | Quelle |
|---|---|
| `probe_icmp_rtt_seconds{stat="min"}` | `rtt_min_ms` / 1000 |
| `probe_icmp_rtt_seconds{stat="avg"}` | `rtt_ms` / 1000 |
| `probe_icmp_rtt_seconds{stat="max"}` | `rtt_max_ms` / 1000 |
| `probe_icmp_loss_ratio` | `loss` |
| `probe_icmp_jitter_seconds` | `jitter_ms` / 1000 |

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

## DNS-Abfrage (`dns`)

Standard nach PRT-12 und A-64: Intervall 60 s (10 s–24 h), Timeout 5 s
(1–10 s, je Probe unter dem Intervall), Down-Count 3, Up-Count 1, höchstens
4 gleichzeitige Prüfungen je Ziel. Ziel der Probe ist der DNS-Server.
Parameter: `qname` (Pflicht; DNS-Name mit Labels von 1–63 Byte, `.` ist die
Wurzel), `qtype` `A|AAAA|CNAME|MX|NS|SOA|TXT|PTR|SRV|CAA`
(Standard `A`), `transport` `udp|tcp` (Standard `udp`), `port` (Standard
53), `rd` (Standard `true`), `expect_rcode` (Standard `NOERROR`),
`expect_contains[]` (höchstens 10 Texte, Groß-/Kleinschreibung egal),
`require_ad` (Standard `false`), `severity` `degraded|down` (Standard
`down`).

Ablauf: eine Abfrage mit zufälliger ID; über UDP mit EDNS0 (Puffer 4096
Byte) und Rückfall auf TCP bei gesetztem TC-Bit, über TCP mit
2-Byte-Längenpräfix. Antworten mit fremder ID oder Frage verwirft der
Collector bis zum Timeout. Antwortgrenze 4096 Byte (UDP) bzw. 64 KiB (TCP;
das Längenpräfix begrenzt eine Nachricht auf 65535 Byte, `max_payload_bytes`).
Der Collector prüft DNSSEC nicht selbst; `require_ad` wertet nur das AD-Bit
des Resolvers aus.

| Ergebnis | Bedeutung | Zustand |
|---|---|---|
| `ok` | RCODE wie erwartet, alle erwarteten Inhalte und ggf. das AD-Bit vorhanden | `up` |
| `ok`/`answer_mismatch` | Abweichung bei `severity` `degraded` | `degraded` |
| `fail`/`answer_mismatch` | abweichender RCODE, fehlender Inhalt oder fehlendes AD-Bit bei `severity` `down` | zählt auf `down` |
| `fail`/`timeout` | keine passende Antwort bis zum Timeout | zählt auf `down` |
| `fail`/`refused` | Verbindung abgelehnt oder abgebaut (TCP), ICMP Port unreachable (UDP) | zählt auf `down` |
| `fail`/`response_too_large` | Antwort über der Antwortgrenze verworfen | zählt auf `down` |
| `error`/`config` | ungültige Parameter (Feld im Label `field`) | `unknown` |

Messwerte in `values`: `probe_dns_response_seconds`, `probe_dns_rcode`,
`probe_dns_answer_count` und bei SOA `probe_dns_soa_serial`. Fehlende
erwartete Inhalte (und `AD`) nennt das Label `missing`.

## HTTP/HTTPS (`http`)

Standard nach PRT-04: Intervall 60 s (30 s–24 h), Timeout 10 s (1–60 s, je
Probe unter dem Intervall), Down-Count 3, Up-Count 1, höchstens 2
gleichzeitige Prüfungen je Ziel. Parameter: `url` (Pflicht, `http` oder
`https` mit Host, ohne Benutzerangabe), `method` `GET|HEAD` (Standard `GET`;
jede andere Methode weist die Validierung ab), `expected_status[]` (Codes
und Bereiche wie `"200"` oder `"200-299"`, Standard `200-399`),
`body_regex` (RE2, höchstens 1024 Zeichen, nur mit `GET`), `max_body`
(Standard 64 KiB, höchstens 1 MiB; der Rest wird ohne Fehler abgeschnitten),
`follow_redirects` 0–5 (Standard 0: eine Umleitung ist die Antwort),
`headers` (nur `Accept`, `User-Agent`, `Host`), `cert_warn_days` (Standard
21) und die Schwelle `rtt_warn_ms` (Gesamtzeit).

Authentisierung über ein Credential der Art `http` (Namespace `http`):
`username`/`password` als Basic, `token` als Bearer, ohne Credential
anonym. Ein Credential an einer `http://`-URL lehnt der Executor mit
`error`/`config` ab, bevor er etwas sendet (die Bindung ist zur
Validierung der Probe nicht bekannt). Der Executor löst den Namen selbst
auf, wählt die TLS-Prüfung über die TLS-Scopes des Collectors (CA-Bundle
oder SPKI-Pin des Netzes der Adresse, sonst die Systemwurzeln; der
Collector übergibt sie beim Start mit `probe.ConfigureHTTP` an den
registrierten Executor) und folgt
Umleitungen selbst: `Authorization` geht nur an denselben Ursprung
(Schema, Host, Port), ein Wechsel von `https` nach `http` mit Credential
ist `fail`/`redirect_limit`. Ein nicht vertrauenswürdiges Zertifikat
beendet die Prüfung im Handshake; es erreicht keine Anfrage und kein
Credential das Ziel.

| Ergebnis | Bedeutung | Zustand |
|---|---|---|
| `ok` | Status erwartet, `body_regex` getroffen | `up`; `degraded`, wenn die Gesamtzeit `rtt_warn_ms` überschreitet |
| `ok`/`cert_expiring` | Restlaufzeit des Zertifikats unter `cert_warn_days` | `degraded` |
| `fail`/`status_mismatch` | Status außerhalb `expected_status` | zählt auf `down` |
| `fail`/`body_mismatch` | `body_regex` im gelesenen Body nicht getroffen | zählt auf `down` (verletzter Sollwert, E-177) |
| `fail`/`redirect_limit` | mehr Umleitungen als `follow_redirects`, oder `https` → `http` mit Credential | zählt auf `down` |
| `fail`/`refused`, `timeout`, `unreachable` | Verbindungsfehler bzw. keine Antwort bis zum Timeout | zählt auf `down` |
| `error`/`tls_untrusted` | Zertifikat nicht vertrauenswürdig | `unknown` |
| `error`/`auth_failed` | 401 oder 403 (wenn nicht erwartet) | `unknown` |
| `error`/`config` | ungültige Parameter, unvollständiges Credential oder Credential an `http://` | `unknown` |

Messwerte: `rtt_ms` ist die Gesamtzeit; `values` enthält `dns_ms`,
`connect_ms`, `tls_ms`, `ttfb_ms`, `status_code` und bei HTTPS
`cert_expiry_s`. Für die Metrikbrücke heißen sie
`probe_http_phase_seconds{phase=dns|connect|tls|ttfb|total}`,
`probe_http_status_code` und `probe_http_cert_expiry_seconds`.

## Typtabelle

Zeiten in Sekunden, Rate in Prüfungen je Sekunde. Die Werte sind Startwerte
nach E-154; WP-1023 misst die Raten nach.

<!-- probe-types:begin -->
| Schlüssel | Status | Intervall Std. / Min. / Max. (s) | Timeout Std. / Min. / Max. (s) | Down / Up | Parallel je Ziel | Parallel je Collector | Rate je Collector (/s) | Probes je Ziel | Payload (Bytes) | Zeilen | Namespaces | Fähigkeiten |
|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---|---|
| `icmp` | available | 30 / 10 / 3600 | 10 / 1 / 10 | 5 / 1 | 1 | 256 | 1000 | 4 | 1472 | 1 | – | icmp |
| `tcp` | available | 60 / 10 / 86400 | 5 / 1 / 30 | 3 / 1 | 2 | 128 | 500 | 32 | 1024 | 1 | – | – |
| `http` | available | 60 / 30 / 86400 | 10 / 1 / 60 | 3 / 1 | 2 | 64 | 100 | 32 | 1048576 | 1 | http | – |
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
| `dns` | available | 60 / 10 / 86400 | 5 / 1 / 10 | 3 / 1 | 4 | 64 | 200 | 16 | 65535 | 1 | – | – |
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
