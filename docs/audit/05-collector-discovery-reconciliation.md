## Zusammenfassung

Prüfstand 2026-09-22, unveränderter Produktstand wie Teil 4; Dokumentationsbasis `b9e3779a526c169550cc1d7491c31b4ad003dd24`. Ausschließlich dieser Bericht wird angelegt; Empfehlungen nicht umgesetzt.

Zwischenstand: DIS-01–10 geprüft; Collector/Reconciliation/Overrides sowie abschließende Statuszählung und G1-Liste folgen. Fehlende v2-Teiltexte werden nicht rekonstruiert.

## Ergebnis je Anforderung

Bewertung gemäß `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/README.md:13–15`: PASS nur vollständig und testbelegt; spätere nicht implementierte Funktionen OFFEN, vorhandene Teilfunktionen PARTIAL bzw. bei Widerspruch ABWEICHEND. S ≤0,5 Tag, M 0,5–2 Tage, L >2 Tage/aufzuteilen. Quelltext und Tests werden getrennt von tatsächlich ausgeführten Nachweisen ausgewiesen.

| ID | Tag | Status | Evidenz | Tests | Befund (1–2 Sätze) | Severity | Aufwand |
|---|---|---|---|---|---|---|---|
| DIS-01 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/sweep/sweep.go:14–21,44–143`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/snmp/snmp.go:144–165` | Sweep-/SNMP-Sockettests vorhanden; kein ICMP-/ARP-/256-Grenznachweis. | Sweep verwendet TCP mit Default 64, ohne harte Obergrenze 256, nicht ICMP/ARP; Ports 623/5985/8006 fehlen im Default, zusätzliche 3389/8080/8443. Reverse-DNS und separates UDP161-Probing vorhanden. | High | L |
| DIS-02 | [B], Profilliste (V) | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/profiles.go:19–40,54–72,98–159`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:264–282,795`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:635–666` | Profile/OUI-/OID-Tests vorhanden; kein vollständiger Klassifikations-/Fallbackdurchstich. | 21 JSON-Profile statt YAML; kein zentraler SNMP→Redfish→Banner-Ablauf und kein System-Template-Mapping. Unknown-Type-Review existiert, aber nicht zugleich garantierter generic_device-Fallback; konkrete Vorschlagsprofile separat abgeglichen. | High | L |
| DIS-03 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/plugin.go:7–45`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:264–282` | Compile-Time-Pluginimplementierungen und Plugin-Tests; kein DeviceRecord-Vertragstest. | Interface ist Name/Discover/Collect mit Result statt Name/Probe/Collect mit DeviceRecord. Hauptschleife ruft nur Discover, nicht die reichhaltigen Collect-Pfade auf. | High | M |
| DIS-04 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/snmp/snmp.go:152–153,194–223`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/ssh/ssh.go:93–155`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/` | Protokolltests mit lokalen Mockgeräten vorhanden; keine echte SNMPv3-/SSH-Authentifizierungsintegration. | Acht Protokollplugins bestehen, SNMP ist tatsächlich v2c und SSH nur Banner/Command-Metadaten ohne Befehlsausführung. Redfish/WMI/NAS/IPMI/Strom haben Teilimplementierungen; weiterer „wie v2“-Umfang mangels Wortlaut N/P, nicht als vollständig bestanden angenommen. | High | L |
| DIS-05 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/plugin.go:7–38`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:355–395`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:38–79` | Uploadtest mockt HTTP-Erfolg; kein Collector→echter Backend-Decoder-Vertrag. | DeviceRecord mit record_id/observed_at/VRF/Scope/Client/Site und strukturierten identity/classification/system/neighbors/power fehlt. Collector sendet ein Result-Array, Server erwartet collector_id/items mit anderem Feldvertrag. | High | L |
| DIS-06 | [P3], Zusatzprotokolle [O] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/snmp/trap.go:13–83`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:557–569` | Trap-Parser-/Receiver-Tests vorhanden; kein echter Collector→Monitoring→UI-Durchstich. | SNMPv1/v2c-Trapreceiver ist implementiert und optional konfigurierbar; Events werden als Monitoringdaten hochgeladen. Fehlende optionale mDNS/SSDP/CDP/NetFlow/sFlow/STP-Funktionen sind keine Pflichtabweichung. | Medium | M |
| DIS-07 | [P4], IGA [A] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/model.go:7–8`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/connector.go:27–70` | IGA-Connector-Mocks; keine Intune-/vCenter-/AD-Discovery-/Rangtests. | IGA-Registry mit SCIM/Relay besteht als Add-on-Teilfunktion; generisches Discovery-Connectorframework samt genannten Integrationen/Provenienzrang nicht gefunden. Fehlender P4-Discoveryteil OFFEN, nicht G1-FAIL. | Medium | L |
| DIS-08 | [Q] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:275–296,463–501`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/DiscoveryPage.tsx:112–187` | Kein End-to-End-Test für einen Probe-/Uploadfehler bis UI und Fehlerzähler. | Strukturierte Fehlerlogs und generische UI-APIfehler/Collectorstatus vorhanden; kein vollständiges Discovery-Fehlerzustandsmodell samt spezifischen Metriken und Job-/Scopefehlerdarstellung. Ein ausgefallenes Ziel wird vielfach nur übersprungen. | Medium | M |
| DIS-09 | [B] | FAIL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/jobs.go`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000008_discovery.up.sql`; Backend-/OpenAPI-Suche ohne discovery_scope/scope_suggested | DiscoveryJobsCRUD ist kein Scope-/Enrollmentvorschlagstest. | discovery_scope-Modell mit Client/Site/VRF/Zeitplan/Credentials/Plugins/Laufergebnis und CRUD fehlt. Vorhandene Jobs und lokale ScanSubnet-Konfiguration ersetzen weder Scopes noch scope_suggested nach Enrollment. | High | L |
| DIS-10 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:790–804,874–884` | Konfigurationstests, kein Scope-/Org-/Minimum-5-Minuten-Nachweis. | Ein Discovery-Ticker mit Default 15 min für alle Protokolle statt Sweep 24 h und getrennter Polls; Heartbeat 60 s stimmt. Metrikpolling default aus, keine Scope-/Orgsteuerung und keine Mindestintervallprüfung. | High | M |

### Tests und Nachweisgrenzen

Bestehende Offline-Tests/Builds beauftragt, Ergebnisse folgen. CI-Run [35731844252](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/35731844252), Produktstand `b0f0ee2302e3af5afa41fbeaf738687fe1492504`, erneut über Actions-MCP abgefragt: failure. Logs des Migrationsjobs [106759104594](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/35731844252/job/106759104594) zeigen TestMigrationsApplied/TenantIsolationRLS/ClientScopeRLS mit URL-Parsingfehler nach erfolgreichem up/down/up; kein grüner vollständiger DB-Nachweis. Keine CI ausgelöst.

## Befunde im Detail

### DIS-01/03/04 — High: Probe-/Pluginvertrag und reale Collect-Nutzung weichen ab

**Beschreibung/Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/sweep/sweep.go:14–21,44–143` begrenzt standardmäßig auf 64 gleichzeitige TCP-Probes, aber frei gesetzte größere Concurrency wird nicht auf 256 gedeckelt. Keine ICMP-/ARP-Probes; Reverse-DNS ist vorhanden. TCPPorts ist konfigurierbar, der ausgelieferte Satz enthält nicht alle geforderten Ports. UDP161 wird durch das separate SNMPplugin verarbeitet, nicht als vollständiger Sweepvertrag.

`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/plugin.go:40–45` hat Discover statt Probe und Result statt DeviceRecord. Hauptschleife `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:264–282` ruft ausschließlich Discover auf. Deshalb ist die Existenz von Collect kein Nachweis des produktiven Inventarisierungswegs.

| Protokoll | Tatsächlich belegte Teilfunktion / Grenze |
|---|---|
| SNMP | v2c-GET; Collect liest System-/Entity-MIB und Profile. Kein v3-Authentifizierungs-/Privacyweg trotz Paketkommentar (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/snmp/snmp.go:152–153,194–248`). |
| SSH | Banner und Reverse-DNS; Kommandos nur als Metadaten, keine authentifizierte Ausführung (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/ssh/ssh.go:93–155`). |
| Redfish | Eigenes HTTP-Plugin mit Discover/Collect und Mocktests (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/redfish/redfish.go:57,164`). Kein produktiver vollständiger Gerätekatalognachweis. |
| IPMI | RMCP-Erkennung/Auth-Capabilities, nicht vollständiges Chassisinventar (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/ipmi/ipmi.go:99–155`). Best-Effort-Wortlaut nicht als Vollinventarpflicht erweitert. |
| WMI | WS-Man Win32-Abfragen über Collect (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/wmi/wmi.go:136–198`); Mockantworten, kein Nachweis aller v2-Details. |
| NAS | Synology/QNAP/TrueNAS-Erkennung und unterschiedliche API-Teilpfade (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/nas/nas.go:119–154,205–363`). Pluginunterstützung ist kein benanntes YAML-Geräteprofil. |
| Strom | UPS/PDU-Erkennung, APC-/Eaton-OIDs; keine vollständige Outlet-/Feedableitung (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/power/power.go:126–195`). |

**Empfohlene Korrektur:** Verbindlichen Probe→Klassifikation→Collect-Vertrag herstellen, geforderte Port-/Probeauswahl mit harter Maximalparallelität, echte SSH-MVP-/SNMPv3-Pfade und den gelieferten Best-Effort-Umfang prüfen. Ende-zu-Ende testen, dass Collect-Ergebnisse tatsächlich den Server erreichen; v2-Details vor darüber hinausgehender Abnahme nachliefern.

### DIS-02 — High: Profilbestand ist keine vollständige Klassifikationspipeline

**Beschreibung/Dateien:** Hauptschleife läuft standardmäßig sweep,snmp,ssh, nicht SNMP→Redfish→Banner (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:264–282,795`). Embedded Profile sind **21 Einträge in 14 JSON-Dateien**, nicht YAML; Registry bietet OID-Präfix- und eingebettete OUI-Zuordnung sowie Register/LoadJSON, aber kein geforderter System-Template-Vertrag (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/profiles.go:19–40,54–159`). SNMP-applyProfile ergänzt nur Hersteller/Modell/Attribute, nicht CIType oder eine zentrale Plugin-/Templateentscheidung (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/snmp/snmp.go:91–108`); nur Collect ruft es auf, nicht der produktive Discover-Loop. Unknown-Type wird im Backend als unclassified_device gehalten statt garantiert zugleich generic_device anzulegen (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:635–666`).

**Exakter Abgleich der gelieferten Vorschlagsliste:** „vorhanden“ bezeichnet deklarierte Profile, nicht getestete Hardwareabdeckung. Die Liste hat 23 Gruppen, nicht exakt 20 Geräte; Varianten zählen nicht still als getestete Modelle.

| Sollprofilgruppe (DIS-02, V) | Vorhandenes Profil / genauer Pfad | Abgrenzung |
|---|---|---|
| Cisco Catalyst 9200/9300 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/cisco.json:3,28` cisco-ios-snmp/ssh | Nur generisches IOS/switch; konkrete Modelle nicht ausgewiesen. |
| Cisco ISR/ASR | Derselbe absolute Cisco-Pfad oben | Kein eigener Routerfamilien-Eintrag; IOS-Profil setzt switch. |
| Cisco ASA/Firepower | Derselbe absolute Cisco-Pfad oben | Kein Firewallprofil. |
| Aruba CX 6xxx | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/hpe.json:22` hpe-procurve-snmp | Kein CX-Eintrag; ProCurve ist nicht CX. |
| Aruba/HPE ProCurve | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/hpe.json:22` | ProCurve-Familie vorhanden. |
| Ubiquiti UniFi Switch/AP | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/ubiquiti.json:3` | UniFi als access_point; Switch nicht getrennt klassifiziert. |
| MikroTik RouterOS | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/mikrotik.json:3,30` | RouterOS SNMP plus „api“-benannter SSH/CLI-Eintrag. |
| FortiGate | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/fortinet.json:3` | FortiGate-Profil vorhanden. |
| OPNsense/pfSense | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/` | Kein Profil. |
| Dell PowerEdge/iDRAC | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/dell.json:3` | iDRAC-Familie, keine einzelne PowerEdge-Modellabnahme. |
| HPE ProLiant/iLO | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/hpe.json:3` | iLO-Familie vorhanden. |
| Lenovo ThinkSystem/XCC | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/lenovo.json:3` | XClarity-Controller-Profil vorhanden. |
| Supermicro/BMC | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/supermicro.json:3,21` | BMC-SNMP/Redfish vorhanden. |
| VMware ESXi | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/` | Kein Profil. |
| Proxmox VE | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/` | Kein Profil. |
| Synology DSM | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/synology.json:3` | DSM-SNMP vorhanden. |
| QNAP | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/` | Kein Profil; NAS-Plugin ist gesonderte Teilunterstützung. |
| TrueNAS | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/` | Kein Profil; NAS-Plugin ist gesonderte Teilunterstützung. |
| APC Smart-UPS/NMC | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/apc.json:3` | NMC/UPS-Profil, kein separater Smart-UPS-Modellnachweis. |
| Eaton UPS | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/` | Kein Profil; Power-Plugin enthält Eaton-OIDs. |
| APC/Raritan PDU | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/apc.json:3` | Nur UPS-Profil, kein PDU-/Raritan-Eintrag. |
| Windows Server/WMI | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/` | Kein Profil; WMI-Plugin separat. |
| Linux/SSH | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/generic.json:27` | Generisches Linux-SSH-Profil vorhanden, keine echte SSH-Ausführung. |

Zusätzliche, nicht geforderte Familien: Arista EOS, Juniper JunOS (SNMP/SSH), Dell OS10, NetApp ONTAP, generisches SNMP; Quellen `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/arista.json:3`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/juniper.json:3,26`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/dell.json:22`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/netapp.json:3`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/profiles/data/generic.json:3`. Sie kompensieren keine fehlende Sollfamilie.

**Empfohlene Korrektur:** Zentrale deterministische Klassifikationskette und Profile mit Typ/Hersteller/Modell/Plugin/System-Template, nachvollziehbarer OUI-Aktualisierung und Unknown-Fallback herstellen. **Profilliste vor konkreter Familienumsetzung bestätigen (CH30/V); fehlende Vorschlagsmodelle allein sperren G1 nicht.** Pipeline-/Fallbackvertrag ist unabhängig davon Baseline. Tests mit Modellfixtures statt bloßem JSON-Parseerfolg.

### DIS-05 — High: Collector und Ingest besitzen keinen gemeinsamen DeviceRecord

**Beschreibung/Dateien:** Result hat ungetaggte Go-Felder CIType/Name/IP/MAC/... und Interfaces ohne IP-Liste (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/plugins/plugin.go:7–38`). uploadResults serialisiert direkt []Result und komprimiert es für /discovery/ingest (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:355–395`). Backend-BulkIngest erwartet ein Objekt collector_id/items mit IngestItem fingerprint/raw_data/ci_type_name/... (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:38–79,619–625`). Selbst nach Transport-/Authentifizierungsfragen bleibt dieser JSON-Vertrag inkompatibel.

record_id, observed_at, vrf_id, scope_id, client_id/site_id aus Scope sowie strukturierte identity/classification/system/neighbors/power fehlen; vorhandene Einzelwerte und freie Maps ersetzen diesen Vertrag nicht. Ein erfolgreicher Mock-HTTP-Upload testet weder Decoder noch Reconciliation des realen Servers.

**Empfohlene Korrektur:** Gemeinsamen versionierten DeviceRecord-Vertrag über Collector/Transport/Ingest mit vertrauenswürdigem Scopekontext, Beobachtungszeit und stabiler Record-ID herstellen; komplette Serialisierung/Kompression/Authentifizierung/Verarbeitung mit dem echten Server testen.

### DIS-09/10 — High: Keine Discovery-Scopes und keine getrennten Intervallverträge

**Beschreibung/Dateien:** Vorhandene discovery_job-Tabelle/CRUD (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000008_discovery.up.sql`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/jobs.go`) ist kein persistierter Scope mit kind/target/VRF/Zeitplan/Credential-/Pluginzuordnung/enabled/Runzeiten/Resultat. Keine automatische lokale Subnetzmeldung mit scope_suggested gefunden. Der lokale Discovery-Ticker läuft für alle Protokolle alle 15 Minuten; Metrikpolling standardmäßig aus, Heartbeat korrekt 60 Sekunden (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:790–804`). durationEnvOrDefault akzeptiert jede syntaktisch gültige Dauer, ohne Mindestwert fünf Minuten (:874–884).

**Empfohlene Korrektur:** Org-/Client-/Site-/Collector-gebundenen Scopekatalog mit discovery:manage-CRUD und Enrollmentvorschlagsreview ergänzen. Sweep24h/Poll15min/Heartbeat60s getrennt planen, Poll-Minimum5min prüfen und Scope-/Orgänderungen bis Collector und nachgelagerte Offline-/Kantenalterung testen.

## Offene Fragen

- PRI-07 besitzt im gelieferten Text keinen Tag; als eigener ungetaggter Grundsatz zählen, nicht still einen Katalogtag erfinden.
- Vollständige v2-Credentialtypen (COL-02) und zusätzliche Protokollkriterien (DIS-04) fehlen; gelieferte konkrete Kriterien dennoch bewertet.
- CH30 nennt Top-20, DIS-02 enthält 23 Gruppen mit weiteren Varianten; verbindliche Modell-/Familienliste und Abnahmekriterien bestätigen. Konkrete Liste ist V, kein zusätzlich erfundener Gateblocker.
- „Nie auf Disk“ bei COL-02 betrifft Discovery-/Zielcredentials; Umgang mit dauerhaft nötigem Collector-Identitätsschlüssel gegenüber COL-04 explizit abgrenzen.

## Stand

UNVOLLSTÄNDIG – fortsetzen ab ID PRI-07.
