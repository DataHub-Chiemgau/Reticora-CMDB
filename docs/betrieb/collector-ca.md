# Collector-CA und Zertifikatserneuerung

Anforderungen: COL-04 ([B]), COL-02 ([B]). Umsetzung: WP-145, WP-146, WP-375
(Befund F-012), WP-430 (Befund F-102).

## Zertifikate

- Die interne Collector-CA liegt verschlüsselt (Master-Key) in `collector_ca`.
  Die aktive CA signiert; nach einem CA-Wechsel bleibt die vorherige bis zum
  Stilllegen Vertrauensanker und stellt bis dahin das Serverzertifikat von
  `nats-edge` aus (siehe [CA-Wechsel](#ca-wechsel-roll-over-und-stilllegung)).
- Beim Enrollment (`POST /api/v1/collectors/enroll` mit CSR) erhält der
  Collector ein Client-Zertifikat: CN = Collector-ID, O = Organisations-ID,
  90 Tage. Der private Schlüssel verlässt den Collector nicht. Jede Ausstellung
  steht in `collector_certificate` (Seriennummer, Fingerprint, Laufzeit,
  Sperre).
- Deaktivieren eines Collectors sperrt alle seine Zertifikate (CRL unter
  `GET /api/v1/collectors/pki`); der Callout von `nats-edge` weist sie ab.

## Erneuerung über den NATS-Kanal

Ab 60 % der Laufzeit erneuert der Collector sein Zertifikat über seinen
mTLS-Kanal (`nats-edge`, Port 4222). Über die API gibt es keine Erneuerung;
`POST /api/v1/collectors/{id}/certificate` ist entfernt (F-012).

Die Anfrage geht als Request/Reply auf `collector.<org>.<id>.certificate.renew`,
die Antwort kommt im eigenen Postfach (`_INBOX.<id>.>`). Inhalt (JSON):

| Feld | Inhalt |
|---|---|
| `csr` | PEM-CSR des neuen Schlüssels; Subject CN = Collector-ID, O = Organisations-ID |
| `certificate_pem` | aktuelles Zertifikat des Collectors |
| `timestamp` | Zeitpunkt der Anfrage (RFC 3339) |
| `signature` | Signatur mit dem aktuellen privaten Schlüssel (Base64) über `reticora-collector-certificate-renewal`, Subject, `timestamp` und `csr`, getrennt durch je einen Zeilenvorschub (`\n`); ECDSA: SHA-256, ASN.1 DER; RSA: SHA-256, PKCS #1 v1.5; Ed25519: die Nachricht selbst |

Der Server beantwortet die Anfragen im Worker `credential_bundles` über eine
Queue-Gruppe: jede Anfrage beantwortet genau eine Instanz. Er prüft:

1. das Zertifikat: Kette zur Collector-CA, Client-Authentifizierung, nicht
   gesperrt, Collector bekannt und nicht deaktiviert; CN und O des Zertifikats
   sind `<id>` und `<org>` des Subjects;
2. die Signatur gegen den öffentlichen Schlüssel dieses Zertifikats. Das
   mitgesendete Zertifikat ist öffentlich und allein kein Identitätsnachweis,
   das interne NATS authentifiziert niemanden;
3. den Zeitstempel: höchstens 5 Minuten Abweichung von der Serverzeit;
4. den CSR: CN = `<id>`, O = `<org>`, gültige Signatur, Schlüssel mindestens
   P-256 bzw. RSA 2048;
5. die Fälligkeit: das vorgelegte Zertifikat hat 60 % seiner Laufzeit
   erreicht. Ein Zertifikat, das in den letzten 10 Minuten schon erneuert
   wurde, erhält keinen zweiten Nachfolger (wiederholte, mitgeschnittene
   Anfrage).

Die Antwort enthält `certificate_pem` (90 Tage), `ca_bundle_pem` (aktive und
ggf. vorherige CA), `serial`, `fingerprint`, `not_before` und `not_after`, oder
`error`:

| `error` | Bedeutung |
|---|---|
| `invalid_request` | Subject oder JSON ungültig, ein Feld fehlt |
| `not_authorized` | Zertifikat nicht von der Collector-CA, gesperrt, eines anderen Collectors oder eines deaktivierten Collectors; Signatur nicht mit dessen Schlüssel |
| `timestamp_out_of_window` | Zeitstempel mehr als 5 Minuten von der Serverzeit entfernt |
| `invalid_csr` | CSR ungültig, schwacher Schlüssel, CN oder O nennen einen anderen Collector bzw. eine andere Organisation |
| `renewal_not_due` | 60 % der Laufzeit noch nicht erreicht oder gerade erneuert |
| `renewal_unavailable` | Serverfehler; später wiederholen |

Jede Ausstellung wird auditiert: `audit_log.action = 'collector.certificate_issued'`,
Ressource ist der Collector, `changes` enthält `serial`, `predecessor_serial`,
`fingerprint` und `not_after`.

### Vorgänger

Das vorgelegte Zertifikat (Vorgänger) bleibt gültig, bis sich der Collector
erstmals mit dem Nachfolger am Endpunkt anmeldet. Dann sperrt der Callout alle
älteren, nicht gesperrten Zertifikate des Collectors
(`revocation_reason = 'superseded'`, CRL). Meldet er sich nie mit dem Nachfolger
an, sperrt der Worker `nats_edge` (stündlich) die Vorgänger spätestens 7 Tage
nach Ausstellung des Nachfolgers.

## CA-Wechsel (Roll-over) und Stilllegung

Ein CA-Wechsel, etwa nach dem Verdacht auf Kompromittierung des CA-Schlüssels,
läuft in zwei Schritten ohne Ausfall der Collector und ohne Neustart von
Server oder `nats-edge` (COL-04, F-102):

1. `rollover`: Eine neue CA wird aktiv und signiert ab dann alle
   Collector-Zertifikate. Die bisherige bleibt als vorherige CA
   Vertrauensanker. `ca.pem` von `nats-edge` enthält beide Anker, das
   Serverzertifikat von `nats-edge` stellt weiter die vorherige CA aus. So
   vertrauen Collector, die vor dem Wechsel registriert wurden (ihr Bundle
   enthält nur die vorherige CA), dem Endpunkt weiter, und der Endpunkt
   vertraut den Zertifikaten der neuen CA.
2. Übergangsphase: Jeder Collector erhält mit seiner nächsten Erneuerung
   (ab 60 % der Laufzeit, also spätestens nach 54 Tagen) ein Zertifikat der
   neuen CA und das Bundle mit beiden Ankern; sobald er sich damit anmeldet,
   wird sein Vorgänger gesperrt.
3. `retire`: Die vorherige CA wird stillgelegt (`collector_ca.state =
   'retired'`). `ca.pem` enthält nur noch die aktive CA, das Serverzertifikat
   stellt die aktive CA aus.

### Bedienung

Der Befehl läuft im Server-Container, mit dessen Konfiguration
(`RETICORA_DATABASE_URL`, `RETICORA_MASTER_KEY`, `RETICORA_PKI_DIR`,
`RETICORA_NATS_EDGE_URL`, `RETICORA_NATS_EDGE_HOSTS`, `RETICORA_NATS_URL`):

```sh
# Docker Compose
docker compose -f deploy/docker-compose/docker-compose.yml exec server server collector-ca status
# Kubernetes
kubectl exec deploy/reticora-server -- server collector-ca status
```

| Befehl | Wirkung |
|---|---|
| `server collector-ca status` | aktive und vorherige CAs (Subject, SHA-256, Erstellung, Ablauf), Aussteller des Serverzertifikats von `nats-edge`, Zahl der aktiven Collector mit einem Zertifikat einer vorherigen CA |
| `server collector-ca rollover` | neue CA aktiv, bisherige vorherige CA |
| `server collector-ca retire` | vorherige CAs stilllegen; verweigert (Exit-Code 1, mit Anzahl), solange aktive Collector ein Zertifikat einer vorherigen CA haben |
| `server collector-ca retire --force` | stilllegen trotz solcher Collector; sie können sich danach nicht mehr verbinden |

Exit-Codes: 0 ausgeführt, 1 fehlgeschlagen oder verweigert, 2 falscher Aufruf.

Ist der Schlüssel der bisherigen CA kompromittiert, bietet die
Übergangsphase keinen Schutz: Bis zum Stilllegen vertrauen Endpunkt,
Callout und Erneuerung ihr weiter, und wer ihren Schlüssel hat, kann
Zertifikate für jeden bekannten Collector ausstellen und damit auch dessen
Credential-Bundle abrufen. Dann direkt nach `rollover` mit
`retire --force` stilllegen, die übergangenen Collector neu registrieren und
die Credentials rotieren wie unter [Sperre](#sperre) beschrieben.

Als „Collector mit einem Zertifikat einer vorherigen CA“ zählt jeder
Collector mit einem nicht gesperrten, nicht abgelaufenen Eintrag in
`collector_certificate`, der vor der aktiven CA ausgestellt wurde. Ein
deaktivierter Collector zählt nicht (seine Zertifikate sind gesperrt), ein
erneuerter zählt, bis er sich mit dem Nachfolger angemeldet hat oder sein
Vorgänger nach 7 Tagen gesperrt wurde. Welche Collector das sind, zeigt
(psql als Eigentümer der Datenbank):

<!-- query: previous-ca-collectors -->
```sql
SELECT cc.organization_id, cc.collector_id, c.name, c.status, c.last_heartbeat_at,
       max(cc.not_after) AS not_after
FROM collector_certificate cc
JOIN collector c ON c.id = cc.collector_id
WHERE cc.revoked_at IS NULL AND cc.not_after > now()
  AND cc.created_at < (SELECT created_at FROM collector_ca WHERE state = 'active')
GROUP BY cc.organization_id, cc.collector_id, c.name, c.status, c.last_heartbeat_at
ORDER BY cc.organization_id, c.name;
```

Collector, die nicht mehr laufen, werden deaktiviert
(`PATCH /api/v1/collectors/{id}` mit `{"status": "disabled"}`); dann ist
`retire` ohne `--force` möglich. Nach `retire --force` werden die
übergangenen Collector mit einem neuen Enrollment-Code neu registriert.

### Ablauf eines Wechsels

`rollover` und `retire` laufen in einer Transaktion unter einer exklusiven
Sperre der CA (Advisory-Lock-Klasse `RCAS`). Ausstellungen von
Collector-Zertifikaten (Registrierung, Erneuerung) halten dieselbe Sperre
geteilt: Während des Wechsels wird nichts signiert, und jedes Zertifikat der
bisherigen CA wurde vor der neuen CA in `collector_certificate`
eingetragen. Vor dem Commit

1. schreibt der Befehl `ca.pem`, `server.pem`, `server-key.pem` und
   `edge.conf` für den neuen Zustand nach `RETICORA_PKI_DIR` (unter der
   Sperre des Exports, auf die die Exporte der Repliken warten),
2. lässt `nats-edge` die Dateien neu laden: Anmeldung mit dem SYS-Login
   (`reticora-edge-sys`, WP-381), `$SYS.REQ.SERVER.PING`, dann
   `$SYS.REQ.SERVER.<id>.RELOAD` an jeden Server,
3. prüft, dass `nats-edge` geladen hat: eine Verbindung mit einem
   Client-Zertifikat der aktiven CA wird angenommen, und das Serverzertifikat
   ist eines der vertrauten CAs,
4. schreibt den Audit-Eintrag in `operator_audit`.

Erst danach wird der Wechsel festgeschrieben: Die erste Signatur der neuen
CA folgt, wenn `nats-edge` den neuen Anker bereits geladen hat. Schlägt ein
Schritt fehl, wird nichts festgeschrieben, die Dateien werden aus dem
bestehenden Zustand neu geschrieben und der Befehl endet mit Exit-Code 1.

Danach kündigt der Befehl den Wechsel auf dem internen NATS an
(`discovery.collector_ca.changed`). Jede Replik liest daraufhin den Zustand
der CA neu, ihr Worker `nats_edge` exportiert die Dateien und lässt
`nats-edge` geänderte Dateien neu laden. Die Ankündigung trägt keinen
Zustand, nur die Version von `collector_ca`, einen Zeitstempel und einen MAC
mit einem aus der Ableitungswurzel der Installation abgeleiteten Schlüssel
(unabhängig vom Master-Key, `docs/ops/workers.md`); ohne gültigen MAC oder
älter als 5 Minuten wird sie verworfen. Collector können sie nicht senden
(sie dürfen nur unter `collector.<org>.<id>.>` veröffentlichen). Geht die
Ankündigung verloren, prüft jede Replik vor jeder Signatur, jedem Export und
jedem Callout die Version von `collector_ca` und liest den Zustand bei einer
Änderung neu.

Beim Neuladen liest `nats-edge` auch seine Konten neu ein und trennt dabei
die bestehenden Collector-Verbindungen. Die Collector verbinden sich sofort
wieder (der Callout lässt sie erneut zu); Daten gehen nicht verloren, der
Spool wiederholt unbestätigte Anfragen. Dasselbe gilt, wenn der Worker
`nats_edge` geänderte Dateien nachladen lässt, etwa ein erneuertes
Serverzertifikat.

Ohne `RETICORA_PKI_DIR` und `RETICORA_NATS_EDGE_URL` (Installation ohne
`nats-edge`) ändert der Befehl nur `collector_ca` und warnt; ist nur eine der
beiden gesetzt, bricht er ab.

### Audit

Jeder ausgeführte Wechsel steht in `operator_audit` (Hash-Kette,
`/api/v1/admin/audit`): `action` = `collector_ca.rolled_over` bzw.
`collector_ca.retired`, `operator_id` = `server collector-ca@<Host>`,
`operator_kind` = `break_glass` (der Befehl läuft mit den Zugangsdaten des
Servers ohne persönliche Operator-Identität), `details` mit den
SHA-256-Fingerprints der aktiven (`active`), vorherigen (`previous`) und
stillgelegten (`retired`) CAs, dem Aussteller des Serverzertifikats
(`edge_issuer`) und `edge_reloaded`. Bei `retire` zusätzlich `force` und
`overridden_collectors` (Zahl der übergangenen Collector). Ein verweigertes
`retire` ändert nichts und wird nicht auditiert.

### Wenn das Neuladen scheitert: Neustart von nats-edge

Lehnt `nats-edge` den SYS-Login ab (eine `nats-edge`-Instanz, die seit dem
Upgrade auf WP-381 nicht neu gestartet wurde, kennt ihn noch nicht) oder ist
es nicht erreichbar, endet `rollover` bzw. `retire` mit Exit-Code 1 ohne
Änderung. Dann:

1. `nats-edge` neu starten; es lädt die Dateien aus dem PKI-Volume, darunter
   `edge.conf` mit dem SYS-Login:
   `docker compose -f deploy/docker-compose/docker-compose.yml restart nats-edge`
   bzw. `kubectl rollout restart deploy/reticora-nats-edge`.
   Die Collector verbinden sich danach neu.
2. Den Befehl wiederholen.

## Upgrade: vor dem Fix über HTTP ausgestellte Collector-Zertifikate

Vor WP-375 erneuerte kein Collector sein Zertifikat selbst (F-026). Jede
Ausstellung nach der Registrierung lief deshalb über
`POST /api/v1/collectors/{id}/certificate`. Diese Route stellte jedem Principal
mit `discovery:ingest` (Sitzung oder API-Key) ein Zertifikat mit beliebiger
Collector-ID für einen eigenen Schlüssel aus. Damit ließ sich das
Credential-Bundle dieses Collectors öffnen (F-012). Alle solchen Ausstellungen
sind nach dem Upgrade als kompromittiert zu behandeln.

### Erkennung

Stichtag `upgrade_at` ist der Zeitpunkt des Upgrades. Die Abfragen laufen mit
psql als Eigentümer der Datenbank (ohne RLS), zum Beispiel nach
`\set upgrade_at '2026-10-21 06:00:00+00'`.

(a) Audit-Einträge der Route: jede erfolgreiche Anfrage schrieb einen Eintrag
mit Akteur, Zeitpunkt und Collector-ID.

<!-- query: audit -->
```sql
SELECT organization_id, timestamp, actor_type, actor_id, resource_id AS collector_id,
       changes->>'status' AS status
FROM audit_log
WHERE action = 'collectors.certificate'
  AND changes->>'route' = 'POST /api/v1/collectors/{id}/certificate'
  AND timestamp < :'upgrade_at'
ORDER BY organization_id, timestamp;
```

(b) Ausstellungsdaten: jede vor dem Upgrade angelegte Zeile von
`collector_certificate` außer der bei der Registrierung ausgestellten. Das
Registrierungszertifikat entsteht in derselben Transaktion wie die
`collector`-Zeile und hat deshalb denselben `created_at`. Collectors, die ein
Administrator ohne Enrollment angelegt hat, haben kein Registrierungszertifikat;
jede ihrer Zeilen zählt. (b) findet die Ausstellungen auch dann, wenn
Audit-Einträge fehlen oder nach der Aufbewahrungsfrist gelöscht sind.

<!-- query: certificates -->
```sql
SELECT cc.organization_id, cc.collector_id, c.name AS collector, cc.serial,
       cc.created_at AS issued_at, cc.revoked_at, cc.not_after
FROM collector_certificate cc
JOIN collector c ON c.id = cc.collector_id
WHERE cc.created_at < :'upgrade_at'
  AND cc.created_at <> c.created_at
ORDER BY cc.organization_id, cc.collector_id, cc.created_at;
```

`backend/internal/discovery/renew_edge_integration_test.go`
(`TestCollectorCAUpgradeDetection`) führt beide Abfragen aus dieser Datei aus.

### Sperre

Für jeden gefundenen Collector:

1. Collector deaktivieren (`PATCH /api/v1/collectors/{id}` mit
   `{"status": "disabled"}`). Das sperrt alle seine Zertifikate (CRL), der
   Endpunkt weist sie ab.
2. Den Collector mit einem neuen Enrollment-Code neu registrieren; er erhält
   eine neue Collector-ID und ein neues Zertifikat.
3. Die Credentials rotieren, die das Bundle des Collectors enthielt: die
   seiner Discovery-Scopes, ohne Scopes die seines Clients und die
   organisationsweiten. Wer ein solches Zertifikat besaß, konnte sie
   entschlüsseln.
