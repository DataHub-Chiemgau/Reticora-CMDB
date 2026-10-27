# Firewall-Matrix: Collector, Agent und SaaS-Plattform

Anforderungen: COL-08 ([B], Inbound-Ports der SaaS-Seite, Firewall-Dokumentation
für Kunden), PRI-07 (Hybrid-Konnektivität: nur ausgehend zu festen
Hostnamen). Umsetzung: WP-146; Portbindung und Kanäle an Deployment und Code
angeglichen: WP-431.

## Grundsatz

Collector und Agent im Kundennetz bauen **jede** Verbindung selbst auf, und
zwar ausschließlich **ausgehend** zu festen Hostnamen der Plattform. Die
SaaS-Seite öffnet keine Verbindung in Kundennetze; das Kundennetz braucht
keine eingehende Freigabe. Lokale Discovery-Probes (ICMP, ARP, SNMP, SSH,
Redfish …) bleiben im Kundennetz.

## Kundenseite: ausgehende Freigaben

| Quelle | Ziel (Hostname) | Port/Protokoll | TLS | Zweck |
|---|---|---|---|---|
| Collector, Agent | `<RETICORA_TLS_DOMAIN>` (z. B. `cmdb.example.com`) | 443/TCP (HTTPS) | TLS 1.2+, Serverzertifikat der Plattform (Let's Encrypt) | Enrollment mit Einmal-Code (24 h, `POST /api/v1/collectors/enroll`); Updates (Manifeste und Binaries unter `RETICORA_UPDATE_URL`, nur mit Auto-Updates; Host nach Wahl des Betreibers, z. B. die Plattform oder ein Spiegel der GitHub-Releases, `docs/betrieb/collector-updates.md`) |
| Collector | `<RETICORA_TLS_DOMAIN>` | 4222/TCP (NATS) | **mTLS**, TLS-Handshake zuerst; Serverzertifikat von der Collector-CA (Trust-Bundle aus dem Enrollment), Client-Zertifikat des Collectors (CN = Collector-ID, 90 Tage) | Datenkanal des Collectors unter `collector.<org>.<collector-id>.>`: Heartbeat (`.heartbeat`), Ingest der Discovery-Ergebnisse (`.ingest`), Metriken (`.metrics`), Credential-Bundles (`.credentials.request`), Scopes (`.scopes.request`, `.scopes.result`, `.subnets.report`), Discovery-Jobs (`.jobs.request`, `.jobs.status`) und Zertifikatserneuerung ab 60 % der Laufzeit (`.certificate.renew`) |
| Collector | Discovery-Ziele im eigenen Netz | ICMP, ARP, 22, 80, 443, 161/UDP, 623, 5985, 8006 | – | Lokale Discovery (DIS-01); verlässt das Kundennetz nicht |

Der Collector leitet den NATS-Endpunkt aus der Server-URL des Enrollments ab
(Host der URL, Port 4222, `tls://`; abweichend `RETICORA_NATS_URL`). Heartbeat,
Ingest und Metriken laufen nur über 4222: Die HTTP-API authentisiert keinen
Collector, ein Heartbeat oder Ingest über 443 wird mit 401 abgewiesen.
Ohne 4222 liefert ein enrollter Collector also nichts; Discovery-Ergebnisse
warten im Spool, Metriken im Puffer.

Nicht benötigt: eingehende Verbindungen in das Kundennetz, VPN, Portweiterleitungen.
Ein Proxy für HTTPS ist möglich; für NATS (4222) muss die Verbindung direkt
(oder per TCP-Durchleitung ohne TLS-Aufbruch) zulässig sein, weil der
Collector sich mit seinem Client-Zertifikat ausweist.

## Plattformseite: eingehende Ports

Installation mit Domain (`RETICORA_TLS_DOMAIN`, Produktionsbetrieb):

| Port | Dienst | Erreichbar von | Absicherung |
|---|---|---|---|
| 443/TCP | Frontend (nginx): Web-UI, API (`/api/`, `/bff/`), Keycloak-Anmeldung (`/realms/`, `/resources/`), Collector-Enrollment | Internet | TLS; Anmeldung über OIDC bzw. API-Schlüssel |
| 80/TCP | nginx | Internet | nur ACME-Challenge und Weiterleitung auf 443 |
| 4222/TCP | `nats-edge` (Collector-Endpunkt) | Internet | mTLS gegen die Collector-CA; jede Verbindung wird vom Server autorisiert (Callout): gesperrte Zertifikate und deaktivierte Collectors werden abgewiesen, ein Collector darf nur `collector.<org>.<collector-id>.>` und sein eigenes Antwort-Postfach nutzen |

Sonst ist nichts von außen erreichbar. REST-API (`RETICORA_SERVER_PORT`,
8080) und Keycloak samt Admin-Konsole (`RETICORA_KEYCLOAK_PORT`, 8180) sprechen
HTTP ohne TLS und sind nur auf `127.0.0.1` veröffentlicht; die Admin-Konsole
erreicht man auf dem Host (`http://127.0.0.1:8180/admin/`, von außen per
SSH-Tunnel). Ebenso nur intern bzw. auf `127.0.0.1`: PostgreSQL, Redis,
interne NATS mit Leaf-Node-Port 7422, MinIO, Keycloak-Datenbank, OpenSearch
(`deploy/docker-compose/docker-compose.yml`).

### Ausnahme: IP-Installation ohne Domain

Ohne `RETICORA_TLS_DOMAIN` (nur HTTP, in der Regel unter einer IP-Adresse
oder `localhost`) liegt der OIDC-Issuer auf dem Keycloak-Port der Host-IP
(`http://<host-ip>:8180/realms/reticora`). Browser und Server-Container
erreichen ihn nur, wenn der Port auf allen Schnittstellen veröffentlicht ist.
`install-cloud.sh` setzt dafür das Opt-in `RETICORA_PUBLISH_DIRECT_PORTS=true`
und daraus `RETICORA_DIRECT_PORTS_BIND=0.0.0.0` in `.env` und warnt vor der
Bestätigung der Einstellungen:

```text
! Installation without a TLS domain: the REST API (port 8080) and Keycloak including its admin console (port 8180) are published on all interfaces without TLS (RETICORA_PUBLISH_DIRECT_PORTS=true; the OIDC issuer of an IP installation is served on the Keycloak port of the host IP). Allow both ports only from trusted networks in the host firewall, or install with a TLS domain (docs/betrieb/firewall-matrix.md).
```

Die Übersicht vor dem Start nennt die Bindung („API and Keycloak: bound to
0.0.0.0“), die Zusammenfassung am Ende wiederholt den Hinweis („⚠ Direct
ports“). Offen sind dann (443 bleibt veröffentlicht, nginx bedient ihn ohne
Domain nicht):

| Port | Dienst | Absicherung |
|---|---|---|
| `RETICORA_FRONTEND_PORT` (3000)/TCP | Frontend (nginx), Web-UI und API | **kein TLS**; Anmeldung über OIDC bzw. API-Schlüssel |
| `RETICORA_SERVER_PORT` (8080)/TCP | REST-API direkt | **kein TLS**; Anmeldung über OIDC bzw. API-Schlüssel |
| `RETICORA_KEYCLOAK_PORT` (8180)/TCP | Keycloak inkl. Admin-Konsole | **kein TLS**; Keycloak-Anmeldung |
| 4222/TCP | `nats-edge` | mTLS wie oben |

Eine IP-Installation ist nicht für den Betrieb am Internet gedacht: Die
Host-Firewall soll 3000, 8080 und 8180 nur aus vertrauenswürdigen Netzen
zulassen. Für den Produktionsbetrieb mit einer Domain installieren; dann
setzt der Installer `RETICORA_PUBLISH_DIRECT_PORTS=false` und
`RETICORA_DIRECT_PORTS_BIND=127.0.0.1` (bei jedem Lauf aus
`RETICORA_TLS_DOMAIN` abgeleitet, ein Wert in `.env` wird nicht übernommen)
und warnt, wenn `RETICORA_OIDC_ISSUER_URL` noch den Keycloak-Port nennt
(richtig: `https://<domain>/realms/reticora`). Compose selbst liest nur
`RETICORA_DIRECT_PORTS_BIND`; fehlt der Wert (z. B. `.env` von vor WP-431,
Start ohne Installer), gilt `127.0.0.1`. Eine bestehende IP-Installation
erhält das Opt-in erst mit dem nächsten Lauf von `install-cloud.sh`; startet
sie nach dem Update ohne diesen Lauf, sind 8080/8180 nur auf `127.0.0.1` und
die Anmeldung über `http://<host-ip>:8180` scheitert. Mit `0.0.0.0` sind die
Ports auf allen IPv4-Schnittstellen veröffentlicht.

## Zertifikate und Sperre

- Die Collector-CA liegt verschlüsselt in der Datenbank (`collector_ca`); der
  Server schreibt Trust-Bundle, Serverzertifikat von `nats-edge` (Hostnamen aus
  `RETICORA_NATS_EDGE_HOSTS`) und die generierte `edge.conf` in das Volume
  `collector-pki` (Worker `nats_edge`, täglich; das Serverzertifikat wird ab
  60 % der Laufzeit erneuert, `nats-edge` übernimmt es beim nächsten Neustart).
- Ein CA-Wechsel hält zwei Vertrauensanker bis zum Stilllegen der alten CA;
  Collectors erneuern ihr Zertifikat ab 60 % der Laufzeit selbst, über ihren
  mTLS-Kanal auf 4222 (`collector.<org>.<collector-id>.certificate.renew`)
  mit Signatur des aktuellen Schlüssels; eine Erneuerung über die HTTP-API
  gibt es nicht (`docs/betrieb/collector-ca.md`).
- Deaktivieren eines Collectors widerruft seine Zertifikate (CRL unter
  `GET /api/v1/collectors/pki`); der Callout lässt ihn nicht mehr zu. Bestehende
  Verbindungen trennt der Worker `nats_edge` binnen Sekunden: Er meldet sich im
  Systemkonto `SYS` des Endpunkts an (Nutzer `reticora-edge-sys`, Passwort aus
  der Ableitungswurzel der Installation abgeleitet, `docs/ops/workers.md`,
  internes Client-Zertifikat), sucht die
  Verbindungen des Collectors (`CONNZ`) und schließt sie (`KICK`); ein
  Fehlschlag wird als `discovery.collector.kick_failed` protokolliert und
  wiederholt. Spätestens enden sie mit dem Ablauf der NATS-Berechtigung
  (15 min); danach durchläuft jede Verbindung den Callout erneut.
- Der Endpunkt liest `edge.conf` nur beim Start: `install-cloud.sh` erzeugt
  `nats-edge` bei jedem Lauf nach dem Server neu
  (`up -d --force-recreate nats-edge`).

## Prüfung

`tests/nats-mtls.test.sh` startet `nats-edge` mit der ausgelieferten
Konfiguration und prüft: ohne Client-Zertifikat und mit einem Zertifikat einer
fremden CA kein TLS-Aufbau; mit Zertifikat der Collector-CA TLS-Aufbau. Die
Autorisierung (eigene/fremde Subjects, gesperrte Collectors) prüft
`backend/internal/discovery/natsedge_integration_test.go`.

`tests/audit/compose-port-policy.test.sh` (CI-Job `installer-script-tests`)
prüft die veröffentlichten Ports mit `docker compose config` auf der `.env`,
die `install-cloud.sh` schreibt: mit Domain nur 80/443/4222 auf allen
Schnittstellen, 8080/8180 auf `127.0.0.1`, 3000 nicht öffentlich; ohne
Domain 8080/8180 nur mit dem Opt-in auf `0.0.0.0`, samt Warnung des
Installers; ohne die Bindung des Installers (Compose-Standard) 8080/8180 auf
`127.0.0.1`. Die Trennung eines deaktivierten Collectors ohne Neustart und die
Abweisung nach dem Neustart prüft der Real-Stack-Test D08 in
`frontend/e2e-real/discovery-override.spec.ts` (CI-Job `install-cloud-smoke`).
