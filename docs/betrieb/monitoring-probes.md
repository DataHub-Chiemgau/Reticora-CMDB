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

## BFD-Sessions (`bfd`)

Standard nach PRT-08: Intervall 60 s (30 s–24 h), Timeout 5 s (1–30 s),
Down-Count 3, Up-Count 1, höchstens 1000 Sessions je Prüfung, Zugangsdaten
aus dem Namespace `snmp`. Parameter: genau eines von `sessions` (1–1000
Nachbaradressen, IPv4 oder IPv6) und `all: true`; beide zugleich oder
keines ist ein Feldfehler auf `params.sessions`.

Quelle ist `bfdSessTable` der BFD-STD-MIB (RFC 7331, `mib-2 222`:
`bfdSessState`, `bfdSessDiag`, `bfdSessDstAddrType`/`bfdSessDstAddr`).
Weicht ein Hersteller ab, liefert der Profilabschnitt `monitor.bfd` die
Tabelle (logische Spalten `state`, `diag`, `addr_type`, `addr` mit
Enum-Abbildung); `cisco.yaml` bildet CISCO-IETF-BFD-MIB ab. Ist die
Profiltabelle leer, gilt der Kern; antwortet das Gerät auf keine der
beiden Tabellen, ist das Ergebnis `error`/`unsupported` (`unknown`).

Ergebnis: Eine erwartete Session, die fehlt oder nicht `up` ist, ergibt
`fail` mit `session_down`; mit `all: true` gilt `adminDown` nicht als
Fehler. SNMP-Timeout ergibt `fail`/`timeout`, ein abgewiesener
SNMPv3-Zugang `error`/`auth_failed`. Mehr als 1000 Zeilen brechen den Walk
ab (`probe_bfd_truncated` = 1); fehlende Zeilen dahinter erzeugen keinen
Zustand (`probe_bfd_sessions_unseen` zählt die erwarteten Sessions hinter
dem Abbruch). Liegt keine erwartete Session in den gelesenen Zeilen, ist
das Ergebnis `error`/`response_too_large` (`unknown`), nie `ok`. Mehrere
Sessions zu einem Nachbarn (Micro-BFD auf LAG-Mitgliedern, eine Session je
VRF) zählen mit der schlechtesten: Ist eine davon nicht `up`, gilt der
Nachbar als nicht `up`. Link-Local-Nachbarn (`ipv6z`) werden ohne Zone
verglichen, `fe80::1%eth0` in `sessions` trifft also `fe80::1`. Werte je
Nachbar mit Label `peer` (Nachbaradresse, bei mehreren Sessions die
schlechteste):
`probe_bfd_session_state{peer}` (Enum-Wert, `up` = 4) und
`probe_bfd_diag{peer,diag}` (Diagnose-Code, Label `diag` mit dem Namen,
z. B. `controlDetectionTimeExpired`), dazu `probe_bfd_sessions` und
`probe_bfd_sessions_down`.

## Optische Pegel (`optical`)

Standard nach PRT-09: Intervall 300 s (60 s–24 h), Timeout 15 s (1–60 s),
Down-Count 3, Up-Count 1, höchstens 512 Transceiver je Prüfung,
Zugangsdaten aus dem Namespace `snmp`. Parameter: genau eines von
`interfaces` (1–512 Einträge, ifName oder ifIndex) und `all: true`
(Feldfehler `params.interfaces`); Schwellen `rx_warn_dbm` (−20),
`rx_crit_dbm` (−25), optional `tx_warn_dbm`/`tx_crit_dbm`; eine
kritische Schwelle muss unter ihrer Warnschwelle liegen. Geprüft werden die
wirksamen Werte wie im Collector (`thresholds` vor `params` vor dem
Standard); der Feldfehler nennt das Feld der kritischen Schwelle
(`thresholds.rx_crit_dbm` bzw. `params.rx_crit_dbm`).

Kern ist `entPhySensorTable` der ENTITY-SENSOR-MIB (RFC 3433, `mib-2 99`):
Wert = `entPhySensorValue` × 10^(3 × (`entPhySensorScale` − 9)) ×
10^(−`entPhySensorPrecision`); Sensoren mit `entPhySensorOperStatus` ≠
`ok` liefern keinen Wert und zählen als `probe_optical_sensors_unavailable`.
Leistung in Watt rechnet der Collector in dBm um (`10 · log10(mW)`). Die
Zuordnung zum Interface geht vom Sensor über `entPhysicalContainedIn`
höchstens 4 Ebenen aufwärts bis zum Eintrag, dessen
`entAliasMappingIdentifier` auf ein `ifIndex` zeigt; Rx/Tx unterscheidet
das Muster `patterns` (Standard `rx|receive`, `tx|transmit` auf
`entPhysicalName`/`entPhysicalDescr`), Temperatur, Bias und Spannung der
Sensortyp. Ein Leistungssensor ohne Interface zählt als
`probe_optical_sensors_unmapped` und geht in keinen Zustand ein. Das Profil
`cisco` (`monitor.optical`) liest CISCO-ENTITY-SENSOR-MIB mit Typ dBm und
den Geräteschwellen aus `entSensorThresholdTable` (minor → Warnung,
major/critical → Alarm). Geräteschwellen rechnet der Collector wie den
Sensorwert um (Watt in dBm, Ampere in mA).

Schwellen (E-186): Geräteschwellen, sofern das Profil sie für den Sensor
liefert, sonst die Parameter. Warnschwelle → `ok` mit `threshold_exceeded`
(`degraded` über die Zustandsmaschine), Alarmschwelle → `fail` mit
`threshold_exceeded`, Rx unter −40 dBm oder ohne Licht → `fail` mit `los`
und `probe_optical_los{ifIndex,ifName}` = 1. Antwortet keine Sensortabelle,
ist das Ergebnis `error`/`unsupported`.

Werte je Interface (Labels `ifIndex`, `ifName`): `probe_optical_rx_dbm`,
`probe_optical_tx_dbm`, `probe_optical_temperature_celsius`,
`probe_optical_bias_ma`, `probe_optical_voltage_volts`; dazu
`probe_optical_interfaces`, `probe_optical_interfaces_not_found` (gelistete
Interfaces ohne Transceiver) und bei mehr als 512 Transceivern
`probe_optical_truncated` = 1 (die übrigen erzeugen keinen Zustand).
`probe_optical_truncated` = 1 steht auch, wenn ein Walk der Entity-Tabellen
(`entPhysicalTable`, `entAliasMappingTable`, `ifName`) oder der
Schwellentabelle an seiner Grenze (16384 Zeilen) abbricht; die Schwellen
des zuletzt gelesenen Sensors bleiben dann unberücksichtigt, er wird nach
den Parametern bewertet.

Werte mit Labels tragen die Labels im Schlüssel von `Result.values` in der
Form des Prometheus-Formats (`name{label="wert",…}`, Labels sortiert;
`probewire.ValueKey`/`ParseValueKey`).

## SNMP-Status (`snmp_status`)

Standard nach PRT-05: Intervall 60 s (30 s–24 h), Timeout 5 s (1–15 s),
Down-Count 3, Up-Count 1, Zugangsdaten aus dem Namespace `snmp` (SNMPv2c
mit Community oder SNMPv3 authPriv). Der Typ liegt im Unterpaket
`collector/probe/snmpstatus`. Parameter: `checks` (höchstens 20 Einträge
`{oid, op, value, severity}`), `port` (161; ein Port im Ziel hat Vorrang),
`timeout_s` (Timeout einer Anfrage, 1–15 s; ohne Angabe der
Probe-Timeout geteilt durch die Versuche) und `retries` (0–2, Standard 0;
alle Versuche liegen im Probe-Timeout).

Ein GET von `sysUpTime.0` und den OIDs der Prüfungen bestimmt die
Erreichbarkeit. Ist `sysUpTime` kleiner als der Vorwert derselben Probe
(Vorwertspeicher `collector/probe/rate`, WP-994), meldet die Probe
`probe_snmp_reboot` = 1; ein Reboot allein ändert den Zustand nicht und
gilt zugleich als Discontinuity für die Zählerraten der Probe. Werte:
`probe_snmp_reboot` (0/1), `probe_snmp_uptime_seconds` und
`probe_snmp_response_seconds`.

Operatoren einer Prüfung: `eq` und `ne` (numerisch, wenn beide Seiten
Zahlen sind, sonst als Text), `lt` und `gt` (nur Zahlen; `value` muss eine
Zahl sein, eine Textantwort verletzt die Prüfung), `in` (kommagetrennte
Liste) und `regex` (RE2, höchstens 256 Zeichen). `severity` ist `down`
(Standard) oder `degraded`. Eine verletzte Prüfung mit `down` ergibt
`fail` mit `check_failed`, mit `degraded` `ok` mit `check_failed`
(`degraded`); eine OID, die der Agent nicht kennt (noSuchObject,
noSuchInstance), ergibt `ok` mit `no_such_object` (`degraded`). Das Label
`oid` nennt die auslösende Prüfung. Ein abgewiesener SNMPv3-Zugang
(usmStats-Report: unbekannter Nutzer, falscher Digest,
Entschlüsselungsfehler) ergibt `error`/`auth_failed` (`unknown`, nie
`down`, E-177); keine Antwort, auch bei falscher SNMPv2c-Community, ergibt
`fail`/`timeout`. Antwortet der Agent mit einem error-status (etwa
`tooBig` bei zu kleiner Nachrichtengröße oder `genErr`), fehlen sysUpTime
und die Prüfwerte: Das ergibt `fail` mit `check_failed`, das Label
`error_status` nennt den Status (gilt für alle SNMP-Probetypen). Der
Collector sendet nur GET.

## SNMP-Interfaces (`snmp_if`)

Standard nach PRT-06: Intervall 300 s (60 s–24 h), Timeout 10 s (1–30 s),
Down-Count 3, Up-Count 1, höchstens 1024 Interfaces je Prüfung (je
Interface 12 Spalten), Zugangsdaten aus dem Namespace `snmp`. Der Typ
liegt im Unterpaket `collector/probe/snmpif`. Gelesen werden aus der
IF-MIB `ifOperStatus`, `ifAdminStatus`, `ifName`, `ifAlias`,
`ifHighSpeed`, `ifHCInOctets`/`ifHCOutOctets`, `ifInErrors`,
`ifOutErrors`, `ifInDiscards`, `ifOutDiscards` und
`ifCounterDiscontinuityTime`; Interfaces ohne ifXTable-Zähler nutzen
`ifInOctets`/`ifOutOctets` (32 Bit) und `ifSpeed`.

Parameter: `name_regex` (auf `ifName`), `alias_regex` (auf `ifAlias`),
`if_index[]`, `only_admin_up` (Standard `true`), `max_interfaces`
(Standard 256, höchstens 1024), `critical[]` (ifName-Liste; kritische
Interfaces sind immer ausgewählt), `util_warn` und `util_crit` (Anteil
0–1, `util_crit` über `util_warn`) und `errors_warn` (Fehler je Sekunde,
ein- und ausgehend zusammen). Alle angegebenen Filter müssen passen; die
Auswahl filtert vor der Ratenberechnung. Schwellen in `thresholds` haben
Vorrang vor `params` (E-186).

Raten rechnet der Collector aus dem Vorwertspeicher der Probe (E-185,
WP-994): Wrap bei 32 bzw. 64 Bit, keine Rate nach einer geänderten
`ifCounterDiscontinuityTime` oder einem Rücksprung von `sysUpTime`; eine
Rate über `ifHighSpeed` × 1,1 wird als unplausibel verworfen. Die erste
Prüfung liefert nur Status, keine Raten. Werte je Interface (Labels
`ifIndex`, `ifName`): `probe_if_oper_status`, `probe_if_admin_status`,
`probe_if_in_bps`, `probe_if_out_bps`, `probe_if_util_ratio` (höhere der
beiden Richtungen), `probe_if_errors_per_s`, `probe_if_discards_per_s`;
dazu `probe_if_interfaces` (ausgewählte Interfaces).

Zustand: Ein kritisches Interface, das admin-up und oper-down ist (down,
unknown, notPresent, lowerLayerDown; nicht testing oder dormant), oder
ein kritisches Interface, das bei vollständigem Walk fehlt, ergibt `fail`
mit `check_failed` (Label `ifName`). Ein anderes ausgewähltes Interface
admin-up und oper-down ergibt `ok` mit `check_failed`, eine Auslastung
über `util_warn` oder Fehler über `errors_warn` `ok` mit
`threshold_exceeded` (`degraded`), eine Auslastung über `util_crit`
`fail` mit `threshold_exceeded`. Admin-down-Interfaces gehen in keine
Bewertung ein. Erreicht der Walk `max_interfaces`, meldet die Probe
`probe_if_truncated` = 1; aus nicht gelesenen Zeilen entsteht kein
Zustand, ein kritisches Interface dahinter ergibt `ok` mit `check_failed`
(`probe_if_critical_unseen`). Ein Ergebnis muss in eine Uplink-Nachricht
(256 KiB) passen: Die Werte je Interface enden bei einem Budget von
192 KiB (`probe.MaxResultValueBytes`, bei langen `ifName` etwa ab 400
Interfaces); weitere Interfaces liefern dann keine Werte, die Probe meldet
ebenfalls `probe_if_truncated` = 1, bewertet den Zustand aber weiter über
alle gelesenen Zeilen. SNMP-Timeout → `fail`/`timeout`,
abgewiesener SNMPv3-Zugang → `error`/`auth_failed`.

## Redfish (`redfish`)

Standard nach MON-03: Intervall 300 s (60 s–24 h), Timeout 15 s (1–60 s),
Down-Count 3, Up-Count 1, Zugangsdaten (Basic-Auth) aus dem Namespace
`redfish`. Parameter: `port` (443), `system_id` (leer: erstes Mitglied von
`Systems`), `chassis_ids` (höchstens 8; leer: alle Chassis), `temp_warn`
und `temp_crit` in °C. Ids sind reine Redfish-Ids (Buchstaben, Ziffern,
`.`, `_`, `-`), sonst Feldfehler `params.system_id` bzw.
`params.chassis_ids[i]`; `temp_crit` muss über `temp_warn` liegen.

Der Collector sendet nur GET-Anfragen: Service-Root, `Systems/{id}`
(`Status.Health`, `PowerState`) und je Chassis zuerst die Netzteile, dann
Temperaturen und Lüfter. Verlinkt das Chassis die älteren Ressourcen
`Power` und `Thermal`, werden diese gelesen (je ein Abruf, nur `Thermal`
trägt die Geräteschwellen), sonst `PowerSubsystem/PowerSupplies` und
`ThermalSubsystem` (`ThermalMetrics`, `Fans`). Je Prüfung höchstens 20
Ressourcenabrufe (darüber `probe_redfish_truncated` = 1 und mindestens `ok`
mit Warncode `check_failed`, Label `truncated`; ausgewertet wird das
Gelesene), je
Ressource höchstens 1 MiB (größere Antworten werden verworfen, nie
teilweise gelesen: `error`/`response_too_large`). Die TLS-Prüfung ist die
der Discovery: Systemwurzeln oder die Ausnahmen aus
`RETICORA_REDFISH_TLS_SCOPES`; ein nicht vertrauenswürdiges Zertifikat
ergibt `error`/`tls_untrusted`, die Zugangsdaten werden nicht gesendet.
Links des Geräts werden nur als Pfad auf demselben Ziel verfolgt; ein
Link auf ein anderes Ziel ergibt `error`/`unsupported`.

Ergebnis: Health oder ein Netzteil `Critical` → `fail` mit
`check_failed`, `Warning` → `ok` mit `check_failed` (`degraded`). Ein
Temperaturwert über `UpperThresholdCritical` des Geräts → `fail` mit
`threshold_exceeded`, über `UpperThresholdNonCritical` → `ok` mit
`threshold_exceeded`; `temp_warn`/`temp_crit` ersetzen die jeweilige
Geräteschwelle (`thresholds` vor `params`, E-186). `ThermalMetrics` führt
keine Geräteschwellen; dort gelten nur die Parameter. 401/403 ergeben
`error`/`auth_failed` (`unknown`, nie `down`), ein Timeout
`fail`/`timeout`, ein anderer HTTP-Status `fail`/`status_mismatch`. Das
Label `sensor` bzw. `psu` am Ergebnis nennt den auslösenden Sensor.

Werte: `probe_redfish_health` (0 OK, 1 Warning, 2 Critical),
`probe_redfish_temperature_celsius{sensor}`, `probe_redfish_fan_rpm{fan}`,
`probe_redfish_psu_input_watts{psu}` und `probe_redfish_psu_state{psu}`
(0/1/2); Namen als `<Chassis-Id>/<Sensor>`. Die `ci_id` setzt die
Metrik-Brücke aus der Probe (WP-976).

## OSPF-Nachbarn (`ospf`) und IS-IS-Adjazenzen (`isis`)

Standard nach PRT-07: Intervall 60 s (60 s–24 h), Timeout 10 s (1–30 s),
Down-Count 3, Up-Count 1, höchstens 1000 Zeilen je Prüfung, Zugangsdaten
aus dem Namespace `snmp`. Beide Typen liegen im Unterpaket
`collector/probe/routing`.

`ospf` liest `ospfNbrTable` der OSPF-MIB (RFC 4750, `1.3.6.1.2.1.14.10.1`:
`ospfNbrRtrId`, `ospfNbrState`, `ospfNbrEvents`) und `ospfv3NbrTable` der
OSPFV3-MIB (RFC 5643, `1.3.6.1.2.1.191.1.9.1`: `ospfv3NbrState`,
`ospfv3NbrEvents`; die Router-ID ist der letzte Teil des Index).
Parameter: `neighbors` (höchstens 1000 Einträge `{router_id, version}`,
Router-ID in IPv4-Schreibweise, `version` 2 oder 3, ohne Angabe beide),
`min_neighbors` und `max_neighbors` (Standard und Höchstwert 1000;
`min_neighbors` ≤ `max_neighbors`). Beide Angaben sind kombinierbar; ohne
beide meldet die Probe nur Werte. Ein erwarteter Nachbar, der fehlt oder
nicht `full` (8) ist, ergibt `fail` mit `check_failed` und Label
`router_id`; weniger `full`-Nachbarn als `min_neighbors` ergeben `ok` mit
`check_failed` (`degraded`). Mehrere Zeilen eines Nachbarn (je Interface)
zählen mit der schlechtesten. Auf Broadcast-Segmenten bleiben Nachbarn,
die weder DR noch BDR sind, regulär in `twoWay`; sie gehören nur in
`neighbors`, wenn sie `full` sein müssen. Werte je Nachbar (Labels
`router_id`, `version`): `probe_ospf_neighbor_state` (Rohwert 1–8) und
`probe_ospf_neighbor_events_total` (Zählerstand `ospfNbrEvents`, ohne
Einfluss auf den Zustand); dazu `probe_ospf_neighbors` und
`probe_ospf_neighbors_full`.

`isis` liest `isisISAdjTable` der ISIS-MIB (RFC 4444,
`1.3.6.1.2.1.138.1.6.1.1`: `isisISAdjState` mit `up` = 3,
`isisISAdjNeighSysID`, `isisISAdjUsage`). Parameter: `adjacencies`
(höchstens 1000 Einträge `{system_id, level}`, System-ID aus sechs
Oktetten wie `1921.6800.1001`, `level` 1 oder 2; eine L1L2-Adjazenz dient
beiden), `min_adjacencies`, `max_adjacencies` (wie bei `ospf`). Eine
erwartete Adjazenz, die fehlt oder nicht `up` ist, ergibt `fail` mit
`check_failed` und Label `system_id`; weniger Adjazenzen `up` als
`min_adjacencies` ergeben `ok` mit `check_failed`. Werte:
`probe_isis_adjacency_state{system_id,level}` (Level `L1`, `L2`, `L1L2`),
`probe_isis_adjacencies`, `probe_isis_adjacencies_up`.

Mehr Zeilen als `max_neighbors` bzw. `max_adjacencies` brechen den Walk ab
(`probe_ospf_truncated` bzw. `probe_isis_truncated` = 1): erwartete
Einträge dahinter erzeugen keinen Zustand (`…_unseen`), die
Mindestanzahl wird dann nicht bewertet; liegt kein erwarteter Eintrag in
den gelesenen Zeilen, ist das Ergebnis `error`/`response_too_large`.
Herstellerabweichungen beschreibt der Profilabschnitt `monitor.ospf`
(Tabellen `neighbors` und `neighbors_v3`, Spalten `router_id`, `state`,
`events`, Enum `state` auf `full` oder `up`) bzw. `monitor.isis` (Tabelle
`adjacencies`, Spalten `state`, `sys_id`, `usage`, Enum `state` auf
`up`); eine leere Profiltabelle fällt auf die Standard-MIB zurück. Ein
SNMP-Timeout ergibt `fail`/`timeout`, ein abgewiesener SNMPv3-Zugang
`error`/`auth_failed`.

## TR-069 über den ACS (`tr069`)

Standard nach PRT-15 und E-154: Intervall 900 s (300 s–24 h), Timeout 30 s
(1–60 s), Parallelität 1 je Ziel, 5 Prüfungen/s je Collector, höchstens
10.000 Geräte je Prüfung, Zugangsdaten aus dem Namespace `acs`
(`username`/`password` als Basic-Auth oder `token` als Bearer). Reticora
ist kein ACS (E-192): Der Collector liest nur aus dem vorhandenen ACS des
Kunden über dessen Northbound-API; Referenzadapter ist GenieACS-NBI.

Nur lesend: Der HTTP-Client des Adapters entsteht ausschließlich über
`newReadOnlyClient` (`collector/probe/tr069/acs.go`). Er folgt keinen
Redirects (Authorization erreicht keine andere URL) und weist jede Methode
außer `GET` vor dem Netzzugriff mit `ErrWriteMethod` ab; das Interface
`Reader` hat nur die lesende Methode `ListDevices`. Connection Requests,
Tasks und Presets gibt es nicht.

Parameter: `acs_url` (Pflicht, http/https ohne Benutzerangabe, Query oder
Fragment), `selector` (`serial`, `oui`, `product_class`, `tag`; exakte
Treffer, freie Query-Texte gibt es nicht), `parameters` (höchstens 20
Einträge `{path, warn, crit, direction}`, `direction` `above` oder
`below`), `inform_interval_s` (Pflicht, 60 s–7 Tage) und `stale_factor`
(Standard 3, 1–10). Zugangsdaten gehen nur über HTTPS: Eine `http://`-URL
mit Zugangsdaten ergibt `error`/`unsupported` mit Label
`field=params.acs_url`.

Abfrage (GenieACS-NBI): `GET <acs_url>/devices/?query=<JSON>&projection=
_id,_lastInform,_deviceId,<Parameter>&skip=<n>&limit=100`; die Query
entsteht mit JSON-Kodierung aus dem Selektor (`_deviceId._SerialNumber`,
`_deviceId._OUI`, `_deviceId._ProductClass`, `_tags`), Parameterwerte
liest der Adapter als `<Pfad>._value`. Je Seite höchstens 1 MiB
(`error`/`response_too_large`). Nach 10.000 Geräten enden die
Seitenabrufe; war die letzte Seite voll, steht `probe_tr069_truncated` = 1.

Bewertung: Ein Gerät ist veraltet (`inform_stale`), wenn sein letzter
Inform älter als `inform_interval_s` × `stale_factor` ist oder fehlt.
Bei genau einem ausgewählten Gerät ergeben veraltet `fail`/`inform_stale`
und eine `crit`-Schwelle `fail`/`threshold_exceeded`, eine `warn`-Schwelle
`ok` mit `threshold_exceeded`. Bei mehreren Geräten ergibt „alle
veraltet“ `fail`/`inform_stale`, ein veraltetes Gerät `ok` mit
`inform_stale`, eine verletzte Schwelle `ok` mit `threshold_exceeded`. Kein
Gerät gefunden ergibt `ok` mit `no_devices`. Die Labels `serial` und
`device_id` (Geräte-ID des ACS; Seriennummern sind nur je OUI und
Produktklasse eindeutig) nennen das auslösende Gerät. 401/403 ergeben `error`/`auth_failed`, ein nicht
vertrauenswürdiges Zertifikat `error`/`tls` (beide `unknown`), ein ACS
außer Reichweite `fail`/`unreachable` bzw. `fail`/`timeout`, ein anderer
HTTP-Status (auch ein nicht verfolgter Redirect) `fail`/`status_mismatch`.

Werte: `probe_tr069_devices`, `probe_tr069_devices_stale`,
`probe_tr069_last_inform_age_seconds{device_id,serial}` und die numerischen
Parameter als `probe_tr069_parameter{device_id,parameter,serial}`.

TR-369/USP ist nicht Teil dieses Typs (WP-1019, bedingt nach A-63).

## RPKI-RTR (`rpki_rtr`)

Standard nach PRT-17 und E-154: Intervall 300 s (60 s–24 h), Timeout 30 s
(1–120 s), Parallelität 1 je Ziel, 1 Prüfung/s je Collector, höchstens
2.000.000 VRPs je Validator, keine Zugangsdaten. Parameter: `port`
(Standard 323; Validatoren verwenden auch 3323 oder 8282); Ziel ist die
Adresse des Validators. RTR über SSH oder TLS ist nicht Teil des Typs.

Der Collector implementiert RTR selbst (E-194, E-195, ohne neue
Bibliothek): Version 1 nach RFC 8210 mit Serial Notify, Serial Query,
Reset Query, Cache Response, IPv4/IPv6 Prefix, End of Data (Refresh,
Retry, Expire), Cache Reset und Error Report; Router-Key-PDUs werden
überlesen. Antwortet der Cache mit Error Report „Unsupported Protocol
Version“ in Version 0, baut der Collector die Session mit Version 0
(RFC 6810) neu auf und nimmt die Standardintervalle 3600/600/7200 s.
Intervalle aus End of Data werden auf die Bereiche aus RFC 8210 Abschnitt 6
begrenzt (Refresh 1–86400 s, Retry 1–7200 s, Expire 600–172800 s);
zwischen zwei Verbindungsversuchen liegt mindestens eine Sekunde. Eine
PDU über 64 KiB oder mit ungültiger Länge beendet die Session mit Error
Report „Corrupt Data“.

Je Validator (`host:port`) und Collector gibt es höchstens eine Session,
die alle `rpki_rtr`- und `rpki_roa`-Probes dieses Validators teilen. Die
erste Prüfung baut sie auf und wartet auf das erste End of Data; danach
folgt eine Serial Query im Refresh-Intervall und nach jedem Serial Notify,
nach Cache Reset oder einer neuen Session-ID eine Reset Query mit
vollständigem Neuaufbau. Verwendet zwei Refresh-Intervalle lang keine
Probe den Validator, endet die Session, auch wenn er nicht erreichbar ist
(dann im Retry-Takt geprüft). Die VRP-Tabelle liegt je
Adressfamilie sortiert im Speicher (12 bzw. 24 Byte je VRP); Ankündigungen
und Rücknahmen gelten erst mit End of Data. Mehr als 2.000.000 VRPs (nach
Anwendung des Deltas erneut geprüft; auch die gepufferten PDUs eines
Deltas sind begrenzt) verwerfen den unvollständigen Stand, der alte bleibt
(`error`/`vrp_limit`).

Ergebnis: Validator nicht erreichbar → `fail`/`refused`, `timeout` bzw.
`unreachable`; Daten älter als das Expire-Intervall → `ok` mit
`rpki_stale` (`degraded`); Error Report des Caches → `error`/`unsupported`
mit den Labels `error_code` (Zahl) und `error` (z. B.
`no_data_available`). Werte: `probe_rpki_rtr_up`, `probe_rpki_serial`,
`probe_rpki_vrps{af=ipv4|ipv6}`, `probe_rpki_data_age_seconds` (seit dem
letzten End of Data) und `probe_rpki_protocol_version`.

## Typtabelle

Zeiten in Sekunden, Rate in Prüfungen je Sekunde. Die Werte sind Startwerte
nach E-154; WP-1023 misst die Raten nach.

<!-- probe-types:begin -->
| Schlüssel | Status | Intervall Std. / Min. / Max. (s) | Timeout Std. / Min. / Max. (s) | Down / Up | Parallel je Ziel | Parallel je Collector | Rate je Collector (/s) | Probes je Ziel | Payload (Bytes) | Zeilen | Namespaces | Fähigkeiten |
|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---|---|
| `icmp` | available | 30 / 10 / 3600 | 10 / 1 / 10 | 5 / 1 | 1 | 256 | 1000 | 4 | 1472 | 1 | – | icmp |
| `tcp` | available | 60 / 10 / 86400 | 5 / 1 / 30 | 3 / 1 | 2 | 128 | 500 | 32 | 1024 | 1 | – | – |
| `http` | available | 60 / 30 / 86400 | 10 / 1 / 60 | 3 / 1 | 2 | 64 | 100 | 32 | 1048576 | 1 | http | – |
| `snmp_status` | available | 60 / 30 / 86400 | 5 / 1 / 15 | 3 / 1 | 1 | 32 | 200 | 4 | 65536 | 20 | snmp | – |
| `snmp_if` | available | 300 / 60 / 86400 | 10 / 1 / 30 | 3 / 1 | 1 | 16 | 50 | 4 | 1048576 | 1024 | snmp | – |
| `snmp_custom` | planned | 300 / 60 / 86400 | 10 / 1 / 60 | 3 / 1 | 1 | 16 | 100 | 8 | 65536 | 256 | snmp | – |
| `redfish` | available | 300 / 60 / 86400 | 15 / 1 / 60 | 3 / 1 | 1 | 8 | 20 | 2 | 1048576 | 256 | redfish | – |
| `bgp` | planned | 60 / 30 / 86400 | 10 / 1 / 60 | 3 / 1 | 1 | 16 | 50 | 2 | 1048576 | 4096 | snmp, gnmi | – |
| `ospf` | available | 60 / 60 / 86400 | 10 / 1 / 30 | 3 / 1 | 1 | 16 | 50 | 2 | 1048576 | 1000 | snmp | – |
| `isis` | available | 60 / 60 / 86400 | 10 / 1 / 30 | 3 / 1 | 1 | 16 | 50 | 2 | 1048576 | 1000 | snmp | – |
| `bfd` | available | 60 / 30 / 86400 | 5 / 1 / 30 | 3 / 1 | 1 | 16 | 50 | 2 | 1048576 | 1000 | snmp | – |
| `optical` | available | 300 / 60 / 86400 | 15 / 1 / 60 | 3 / 1 | 1 | 8 | 20 | 2 | 1048576 | 512 | snmp | – |
| `bng` | planned | 60 / 30 / 86400 | 10 / 1 / 30 | 3 / 1 | 1 | 8 | 20 | 2 | 1048576 | 1024 | snmp | – |
| `pon` | planned | 300 / 120 / 86400 | 60 / 1 / 120 | 3 / 1 | 1 | 4 | 5 | 2 | 4194304 | 8192 | snmp | – |
| `dns` | available | 60 / 10 / 86400 | 5 / 1 / 10 | 3 / 1 | 4 | 64 | 200 | 16 | 65535 | 1 | – | – |
| `dhcp` | planned | 300 / 60 / 86400 | 10 / 1 / 30 | 3 / 1 | 1 | 1 | 1 | 1 | 4096 | 1 | – | privileged_udp_ports |
| `stamp` | planned | 60 / 10 / 86400 | 5 / 1 / 30 | 3 / 1 | 1 | 16 | 20 | 4 | 1472 | 1 | – | – |
| `twamp_control` | planned | 60 / 10 / 86400 | 5 / 1 / 30 | 3 / 1 | 1 | 16 | 20 | 4 | 1472 | 1 | – | – |
| `gnmi` | planned | 60 / 10 / 86400 | 10 / 1 / 60 | 3 / 1 | 1 | 16 | 50 | 4 | 4194304 | 10000 | gnmi | – |
| `tr069` | available | 900 / 300 / 86400 | 30 / 1 / 60 | 3 / 1 | 1 | 4 | 5 | 1 | 1048576 | 10000 | acs | – |
| `usp` | planned | 900 / 300 / 86400 | 30 / 1 / 60 | 3 / 1 | 1 | 4 | 5 | 1 | 1048576 | 256 | acs | – |
| `rpki_rtr` | available | 300 / 60 / 86400 | 30 / 1 / 120 | 3 / 1 | 1 | 4 | 1 | 1 | 67108864 | 2000000 | – | – |
| `rpki_roa` | planned | 300 / 60 / 86400 | 1 / 1 / 5 | 3 / 1 | 1 | 64 | 100 | 100 | 65536 | 100 | – | – |
<!-- probe-types:end -->

Payload- und Zeilengrenzen (`max_payload_bytes`, `max_rows`) stehen in den
Spalten „Payload (Bytes)“ und „Zeilen“; die Parameterschemas stehen nur im
Vertrag. `rpki_roa` hat nach E-154 keine
eigene Parallelitätsgrenze je Ziel; das Register führt 1, weil jede Grenze
größer 0 sein muss.
