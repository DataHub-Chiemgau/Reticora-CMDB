# Collector: Verbindung zur Cloud und Diagnose

Anforderungen: COL-08 ([B]), INS-06 ([B]), DIS-09 ([B]), COL-05 ([B]),
MON-02 ([B]). Gilt für den Collector (`collector/collectorcmd`) und den
Installer `install-vm.sh`.

Diese Seite beschreibt, über welchen Weg der Collector liefert, welche
Zertifikatsnamen er am NATS-Endpunkt annimmt, wie er sich vor und nach dem
Enrollment verhält und welche Log-Ereignisse erklären, warum in der Cloud
nichts ankommt. Ports und Freigaben: [Firewall-Matrix](firewall-matrix.md).

## Der einzige Lieferweg: mTLS-NATS auf Port 4222

Records, Heartbeats und Messwerte gehen ausschließlich über die
mTLS-Verbindung des Collectors zum NATS-Endpunkt `nats-edge`:

- Endpunkt: `tls://<Host der Server-URL>:4222`; `RETICORA_NATS_URL`
  überschreibt ihn. Die Server-URL ist `RETICORA_SERVER_URL` oder, wenn
  leer, die beim Enrollment gespeicherte URL.
- Die HTTP-API authentisiert keinen Collector. Ohne verbundenen Endpunkt
  antwortet sie auf Ingest und Heartbeat mit 401; die Discovery-Ergebnisse
  bleiben im Spool und gehen über den Endpunkt, sobald er verbunden ist.
- 4222/TCP muss im Kundennetz ausgehend und auf dem Cloud-Host eingehend
  offen sein (Host-Firewall, Security-Group). `install-vm.sh` prüft das am
  Ende mit einer TCP-Verbindung und warnt deutlich, wenn sie nicht zustande
  kommt.

## Servername des Edge-Zertifikats

Der Collector prüft das Serverzertifikat von `nats-edge` selbst, mit der
vollständigen Kette:

1. Vertrauensanker ist nur das CA-Bundle aus dem Enrollment (die private
   Collector-CA), nie die System-CAs.
2. Das Zertifikat muss zur Laufzeit gültig sein und ausdrücklich für
   Serverauthentifizierung ausgestellt sein (Extended Key Usage
   `serverAuth`).
3. Es muss für **einen** dieser Namen gültig sein: den Host, den der
   Collector anwählt (Name oder IP-Adresse), `reticora-collector-nats` oder
   `nats-edge`.

Die festen Namen machen den Collector unabhängig davon, unter welcher
Adresse er die Cloud erreicht (IP-Installation, interner Name, NAT,
DNS-Alias). `reticora-collector-nats` trägt jedes Edge-Zertifikat neben den
Hosts aus `RETICORA_NATS_EDGE_HOSTS`; `nats-edge` steht auf jedem Zertifikat
des Compose-Stacks, sodass ein aktualisierter Collector auch mit einem
älteren Server ohne Neuausstellung arbeitet.

Sicherheit: Die Collector-CA stellt `serverAuth` nur für den eigenen
Endpunkt aus (`CA.IssueServer` und der Edge-Export in
`backend/internal/discovery/ca.go` und `natsedge.go`, CN
`reticora-collector-nats`). Collector-Zertifikate (`CA.Sign`) und die
internen Dienstzertifikate (`CA.IssueInternalClient`) tragen nur
`clientAuth`. Ein Zertifikat mit Kette zur Collector-CA und `serverAuth`
ist daher immer das Edge-Zertifikat der Plattform; kein Collector kann sich
als Server ausgeben.

`RETICORA_NATS_TLS_SERVER_NAME` (optional, in `collector.env`) ersetzt die
Regel durch **genau** diesen Namen; das Zertifikat muss ihn tragen. Der Name
wird auch als SNI gesendet. `RETICORA_TLS_SERVER_NAME` gilt dagegen nur für
HTTPS und nicht für den NATS-Endpunkt.

## Enrollment und Identität

- **Start vor dem Enrollment:** Ohne Credentials-Datei
  (`/var/lib/reticora-collector/credentials.json`) liefert der Collector
  nichts. Er meldet das einmal (`collector.enroll.pending`) und prüft alle
  30 s, ob die Datei da ist. Sobald `reticora-collector enroll` sie
  geschrieben hat, startet er die NATS-Sitzung und übernimmt Server-URL,
  Organisation und Collector-ID (`collector.enroll.detected`,
  `collector.identity.adopted`). Für den Lieferweg ist kein Neustart nötig;
  `systemctl restart reticora-collector` wendet die Identität sofort an.
- **Identität aus dem Zertifikat:** Mit Credentials-Datei gelten
  Organisation (O) und Collector-ID (CN) des Zertifikats. Abweichende Werte
  in `RETICORA_ORGANIZATION_ID` oder `RETICORA_COLLECTOR_ID` (ältere
  Installer schrieben `reticora-demo` und den Hostnamen) werden ignoriert und
  als Warnung gemeldet (`collector.identity.mismatch`). Ausnahme: ein eigenes
  Client-Zertifikat (`RETICORA_TLS_CLIENT_CERT_FILE`); dann gelten die Werte
  aus der Umgebung.
- **Server-URL:** `RETICORA_SERVER_URL` hat weiter Vorrang vor der URL des
  Enrollments. Weicht ihr Host ab, warnt der Collector
  (`collector.identity.mismatch`), denn der NATS-Endpunkt folgt
  `RETICORA_SERVER_URL`.
- **Code ohne Prozessliste:** `reticora-collector enroll --server <url>`
  liest den Einmal-Code aus `RETICORA_ENROLL_TOKEN`, wenn `--code` fehlt.
  So erscheint er nicht in der Prozessliste; `install-vm.sh` enrollt auf
  diese Weise.

### install-vm.sh

- Organisation und Collector-ID bleiben leer (sie kommen aus dem
  Enrollment). Die Platzhalter älterer Installer (`https://cmdb.example.com`,
  `reticora-demo`, der Hostname als Collector-ID) werden bei einem erneuten
  Lauf nicht übernommen.
- Die Cloud-URL ist die URL, unter der die Cloud im Browser geöffnet wird
  (das `--server` des Enroll-Befehls). Leer: die URL aus dem Enrollment.
- Der Installer fragt optional nach dem Einmal-Code der Discovery-Seite
  (verdeckte Eingabe, nicht interaktiv: `RETICORA_ENROLL_CODE`). Mit Code
  enrollt er vor dem (Neu-)Start des Dienstes; im Docker-Modus läuft das
  Enrollment im Image, danach wird der Container neu gestartet. Der Code
  wird nie in eine Datei geschrieben, nie auf einer Kommandozeile übergeben
  und nicht ausgegeben. Schlägt das Enrollment fehl, warnt der Installer und
  nennt in der Zusammenfassung den Befehl für später.
- Ein bereits enrollter Collector wird nicht erneut enrollt.
- Im Docker-Modus gehört das Spool-Verzeichnis dem Benutzer des Images
  (`65534:65534`, Modus 700); ein vorhandenes Verzeichnis außerhalb des
  Standardpfads wird nicht übernommen, der Installer sagt, was zu tun ist.
- Im Docker-Modus bindet der Installer die Dateien aus `collector.env`
  (`RETICORA_TLS_CA_FILE`, `RETICORA_TLS_CLIENT_CERT_FILE`,
  `RETICORA_TLS_CLIENT_KEY_FILE`), die außerhalb der ohnehin eingebundenen
  Verzeichnisse liegen, schreibgeschützt unter demselben Pfad ein. So liest
  das Enrollment im Image die private Cloud-CA (`--ca-file`). Ein relativer
  Pfad oder eine Datei, die der Image-Benutzer nicht lesen kann, ergibt eine
  Warnung.
- Nachweis: `tests/install-vm-enroll.test.sh`.

## Discovery-Scopes und Vorschläge

- Der Collector entdeckt nur, was der Server ihm als aktivierte Scopes
  schickt. Eine leere Liste bedeutet: nichts wird entdeckt. Das meldet er
  einmal je Wechsel als Warnung (`collector.scopes.empty`).
- Nach dem Verbindungsaufbau meldet er die Subnetze seiner Schnittstellen
  **und** die konfigurierten `RETICORA_SCAN_SUBNETS` als Vorschläge
  (`scope_suggested`); ausgenommen sind Loopback, Link-Local, Multicast und
  einzelne Hosts. Die Vorschläge werden auf der Discovery-Seite bestätigt;
  ein bestätigter Scope erreicht den Collector innerhalb von 15 Minuten oder
  beim nächsten Start.
- Liegt ein Discovery-Job vor („Jetzt ausführen“), während der Collector
  keinen Scope kennt, holt er die Scopes vorher einmal neu. Ohne offenen Job
  entsteht keine zusätzliche Anfrage.
- Unverändert: Vor der ersten Antwort des Servers nach einem Start scannt
  der Collector einmal `RETICORA_SCAN_SUBNETS`.

## Große Discovery-Läufe

- Die Records eines Laufs gehen in Payloads, die die maximale
  Nachrichtengröße des Endpunkts nicht überschreiten (`max_payload` des
  verbundenen Endpunkts, NATS-Standard 1 MiB; ohne Verbindung 512 KiB), in
  ihrer Reihenfolge.
- Ein gespoolter Batch, den der Endpunkt als zu groß ablehnt, wird in Teile
  zerlegt (`collector.spool.split`). Die Teile behalten die Quellzeit des
  Batches und gehen vor allen späteren Batches (COL-05); der Server
  dedupliziert über `record_id`.
- Ein einzelner Record, der größer als die maximale Nachrichtengröße des
  verbundenen Endpunkts ist, kommt in die Quarantäne des Spools und zählt
  als verloren (`collector.record.oversized`, im Heartbeat gemeldet). Er
  blockiert den Spool nicht.
- Ohne Verbindung ist die Grenze des Endpunkts unbekannt: 512 KiB ist dann
  nur die Packgröße. Ein größerer Record wartet als eigene Payload im Spool;
  erst wenn der verbundene Endpunkt ihn ablehnt, kommt er in die Quarantäne.
- Zu große Payloads gehen nie über HTTP.

## Monitoring nicht lizenziert

Enthält der Plan der Organisation kein Monitoring, lehnt der Server
Messwerte mit 403 (`feature-not-entitled`) ab. Der Collector meldet das
einmal als Warnung (`collector.metrics.not_licensed`), sendet danach keine
Messwerte mehr und pollt keine SNMP-Messwerte. Stündlich prüft ein einzelner
Batch, ob Monitoring inzwischen lizenziert ist; dann laufen die Messwerte
wieder (`collector.metrics.resumed`). Ein Neustart prüft sofort. Diese
Messwerte zählen nicht als Verlust. Discovery und Inventar sind nicht
betroffen.

## Log-Ereignisse

| Ereignis | Stufe | Bedeutung | Abhilfe |
|---|---|---|---|
| `collector.nats.unreachable` | WARN beim ersten Mal; ERROR bei Zertifikat oder Abweisung; ERROR-Wiederholung höchstens alle 10 min mit `failing_since` und `attempts` | Der NATS-Endpunkt ist nicht verbunden. `hint` nennt die Ursache | siehe die Zeilen darunter |
| ↳ `hint`: „the edge certificate does not name <host>“ | ERROR | Das Edge-Zertifikat nennt den angewählten Host nicht (und trägt keinen der festen Namen) | Server aktualisieren (`RETICORA_NATS_EDGE_HOSTS`, `install-cloud.sh` erneut ausführen) oder `RETICORA_NATS_TLS_SERVER_NAME` setzen |
| ↳ `hint`: „not signed by the collector CA of this enrollment“ | ERROR | Der Collector wurde bei einem anderen Server oder mit einer anderen CA enrollt | neu enrollen |
| ↳ `hint`: „<host>:4222/TCP is not reachable“ | WARN, wiederholt ERROR | Verbindung abgewiesen oder Zeitüberschreitung | 4222/TCP freigeben (ausgehend im Kundennetz, eingehend auf dem Cloud-Host) |
| ↳ `hint`: „the name <host> does not resolve“ | WARN | DNS-Fehler | DNS, `RETICORA_SERVER_URL`, `RETICORA_NATS_URL` prüfen |
| ↳ Meldung „collector NATS endpoint refused“ | ERROR | Der Endpunkt weist den Collector ab (deaktiviert, Zertifikat gesperrt, Callout nicht erreichbar) | Collector auf der Discovery-Seite prüfen, ggf. neu enrollen |
| `collector.heartbeat.refused` | ERROR | Der Heartbeat wurde abgewiesen (`transport` `http`: 401, weil die HTTP-API keinen Collector authentisiert; `edge`: der Server weist den Collector ab) | Edge-Verbindung herstellen (siehe oben) bzw. Collector prüfen |
| `collector.upload.unauthenticated` | WARN, einmal je Edge-Ausfall | Der HTTP-Ingest antwortete 401; die Records warten im Spool | Edge-Verbindung herstellen |
| `collector.scopes.empty` | WARN, einmal je Wechsel | Kein Discovery-Scope: nichts wird entdeckt | Scope anlegen oder Vorschlag auf der Discovery-Seite bestätigen |
| `collector.enroll.pending` | WARN, einmal | Nicht enrollt, nichts wird geliefert | Enroll-Befehl der Discovery-Seite ausführen |
| `collector.enroll.detected`, `collector.identity.adopted` | INFO | Credentials-Datei nach dem Start erschienen, Identität übernommen | – |
| `collector.identity.mismatch` | WARN | `RETICORA_ORGANIZATION_ID`, `RETICORA_COLLECTOR_ID` oder der Host von `RETICORA_SERVER_URL` weichen vom Enrollment ab | Wert in `collector.env` leeren bzw. angleichen |
| `collector.record.oversized` | ERROR | Record größer als die maximale Nachrichtengröße; in Quarantäne, als verloren gezählt | Scope bzw. Plugin prüfen; `max_payload` des Endpunkts |
| `collector.spool.split` | INFO | Zu großer gespoolter Batch in Teile zerlegt | – |
| `collector.metrics.not_licensed` | WARN, einmal | Monitoring nicht im Plan; keine Messwerte | Plan erweitern (optional) |
| `collector.metrics.resumed` | INFO | Monitoring lizenziert, Messwerte laufen wieder | – |

Erfolg zeigt `collector NATS endpoint connected over mTLS` und
`heartbeat completed` mit `transport` `edge`.

## Diagnose

```bash
# Ereignisse des Collectors (systemd)
journalctl -u reticora-collector --since -1h \
  | grep -E 'collector\.(nats|heartbeat|upload|scopes|enroll|identity|record|metrics)|connected over mTLS'
# Erreichbarkeit des Endpunkts vom Collector aus
nc -vz <cloud-host> 4222
# Namen auf dem Edge-Zertifikat
openssl s_client -connect <cloud-host>:4222 </dev/null 2>/dev/null | openssl x509 -noout -ext subjectAltName
```

Nachweise: `collector/collectorcmd/edge_tls_test.go`
(Namensregel mit echten Zertifikaten, Wiederholung und Hinweise der
Verbindungsmeldungen, Start vor dem Enrollment), `edge_test.go` (Aufteilung
großer Läufe und gespoolter Batches, Quarantäne einzelner Records erst
nach Ablehnung durch den Endpunkt, 401 des HTTP-Wegs), `collectorcmd_test.go` (Heartbeat-Abweisung, unvollständige
Konfiguration, Identität aus dem Zertifikat, Scope-Vorschläge, leere
Scope-Liste), `metrics_upload_test.go` (Monitoring nicht lizenziert),
`discovery_scope_test.go` (Scopes vor einem Job neu holen, braucht
`TEST_NATS_SERVER_BIN` oder `TEST_NATS_URL`).
