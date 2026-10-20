# Firewall-Matrix: Collector, Agent und SaaS-Plattform

Anforderungen: COL-08 ([B], Inbound-Ports der SaaS-Seite, Firewall-Dokumentation
für Kunden), PRI-07 (Hybrid-Konnektivität: nur ausgehend zu festen
Hostnamen). Umsetzung: WP-146.

## Grundsatz

Collector und Agent im Kundennetz bauen **jede** Verbindung selbst auf, und
zwar ausschließlich **ausgehend** zu festen Hostnamen der Plattform. Die
SaaS-Seite öffnet keine Verbindung in Kundennetze; das Kundennetz braucht
keine eingehende Freigabe. Lokale Discovery-Probes (ICMP, ARP, SNMP, SSH,
Redfish …) bleiben im Kundennetz.

## Kundenseite: ausgehende Freigaben

| Quelle | Ziel (Hostname) | Port/Protokoll | TLS | Zweck |
|---|---|---|---|---|
| Collector, Agent | `<RETICORA_TLS_DOMAIN>` (z. B. `cmdb.example.com`) | 443/TCP (HTTPS) | TLS 1.2+, Serverzertifikat der Plattform (Let's Encrypt) | Enrollment mit Einmal-Code (24 h), Heartbeat, Ingest, Updates |
| Collector | `<RETICORA_TLS_DOMAIN>` | 4222/TCP (NATS) | **mTLS**, TLS-Handshake zuerst; Serverzertifikat von der Collector-CA (Trust-Bundle aus dem Enrollment), Client-Zertifikat des Collectors (CN = Collector-ID, 90 Tage) | Nachrichtenkanal des Collectors (Credential-Bundles, Zertifikatserneuerung, Steuerung) |
| Collector | Discovery-Ziele im eigenen Netz | ICMP, ARP, 22, 80, 443, 161/UDP, 623, 5985, 8006 | – | Lokale Discovery (DIS-01); verlässt das Kundennetz nicht |

Nicht benötigt: eingehende Verbindungen in das Kundennetz, VPN, Portweiterleitungen.
Ein Proxy für HTTPS ist möglich; für NATS (4222) muss die Verbindung direkt
(oder per TCP-Durchleitung ohne TLS-Aufbruch) zulässig sein, weil der
Collector sich mit seinem Client-Zertifikat ausweist.

## Plattformseite: eingehende Ports

| Port | Dienst | Erreichbar von | Absicherung |
|---|---|---|---|
| 443/TCP | Frontend (nginx), API | Internet | TLS; Anmeldung über OIDC bzw. API-Schlüssel |
| 80/TCP | nginx | Internet | nur ACME-Challenge und Weiterleitung auf 443 |
| 4222/TCP | `nats-edge` (Collector-Endpunkt) | Internet | mTLS gegen die Collector-CA; jede Verbindung wird vom Server autorisiert (Callout): gesperrte Zertifikate und deaktivierte Collectors werden abgewiesen, ein Collector darf nur `collector.<org>.<collector-id>.>` und sein eigenes Antwort-Postfach nutzen |

Alle übrigen Dienste (PostgreSQL, Redis, interne NATS mit Leaf-Node-Port
7422, MinIO, Keycloak-Datenbank, OpenSearch) sind nur intern bzw. auf
`127.0.0.1` erreichbar (`deploy/docker-compose/docker-compose.yml`).

## Zertifikate und Sperre

- Die Collector-CA liegt verschlüsselt in der Datenbank (`collector_ca`); der
  Server schreibt Trust-Bundle, Serverzertifikat von `nats-edge` (Hostnamen aus
  `RETICORA_NATS_EDGE_HOSTS`) und die generierte `edge.conf` in das Volume
  `collector-pki` (Worker `nats_edge`, täglich; das Serverzertifikat wird ab
  60 % der Laufzeit erneuert, `nats-edge` übernimmt es beim nächsten Neustart).
- Ein CA-Wechsel hält zwei Vertrauensanker bis zum Stilllegen der alten CA;
  Collectors erneuern ihr Zertifikat ab 60 % der Laufzeit selbst, über ihren
  mTLS-Kanal mit Signatur des aktuellen Schlüssels
  (`docs/betrieb/collector-ca.md`).
- Deaktivieren eines Collectors widerruft seine Zertifikate (CRL unter
  `GET /api/v1/collectors/pki`); der Callout lässt ihn nicht mehr zu. Bestehende
  Verbindungen trennt der Worker `nats_edge` binnen Sekunden: Er meldet sich im
  Systemkonto `SYS` des Endpunkts an (Nutzer `reticora-edge-sys`, Passwort aus
  dem Master-Key abgeleitet, internes Client-Zertifikat), sucht die
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
