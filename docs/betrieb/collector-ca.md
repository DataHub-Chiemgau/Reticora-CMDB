# Collector-CA und Zertifikatserneuerung

Anforderungen: COL-04 ([B]), COL-02 ([B]). Umsetzung: WP-145, WP-146, WP-375
(Befund F-012).

## Zertifikate

- Die interne Collector-CA liegt verschlüsselt (Master-Key) in `collector_ca`.
  Die aktive CA signiert; nach einem CA-Wechsel bleibt die vorherige bis zum
  Stilllegen Vertrauensanker.
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
