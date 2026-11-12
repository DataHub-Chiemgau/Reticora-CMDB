KATALOGAUSZUG (Ergänzungen des Auftraggebers vom 2026-11-09, Phase 3)

Vorbemerkungen

Quelle: Anforderungen des Auftraggebers vom 2026-11-09 zu Paketen, Monitoring, Alarmierung, IPAM, Zugangsdaten, ADMP, Compliance und Workflows. Die Ergänzungen gelten für Phase 3 (Gate G3) und präzisieren bestehende Katalog-IDs, ohne deren Text zu ändern; wo sie eine bestehende ID berühren, ist das an der jeweiligen Zeile vermerkt.

Zusammenlegungen: Die Probe-Typgrenzen sind Bestandteil von PRT-01, die Auslegung der Prüflast ist Bestandteil von NFR-11, die Eskalation ist Bestandteil der bestehenden NTF-02 (siehe Hinweis unter Alarmierung). Diese Inhalte haben keine eigenen IDs.

Tags: Test-, Last-, Gate- und Abnahmepflichten dieser Ergänzung (PRT-21, NFR-11, GATE-06, ABN-04) tragen [P3], weil sie erst ab G3 gelten. Ein [Q] ohne Phasenangabe gilt nach der Legende (00-grundlagen.md) bereits ab G1.

Abgrenzung zu Phase-1-Werten: Das Minimum von 5 min aus DIS-10 gilt nur für Discovery-Polls (SNMP-/Redfish-Poll eines Discovery-Scopes), nicht für Monitoring-Probes; für Probes gelten die Typgrenzen (PRT-01) und das Paketkontingent min_probe_interval_s (PKG-04). Das Monitoring-Lebenszeichen (PRB-10) ersetzt nicht den Heartbeat und Offline-Status nach COL-05.

Pakete und Limits (PKG, Ergänzung zu Abschnitt 8)

PKG-01 [P3] Paket je Mandant: Jede Organisation hat genau ein Paket (essential, standard, pro, enterprise) in organization.plan als führende Angabe. Jede Entitlement-Zeile trägt ihre Herkunft (plan, addon, manual). GET /api/v1/entitlements liefert je Schlüssel Paket, Herkunft, Freischaltung, Limits und Ablauf. Zeilen mit Herkunft plan entsprechen nach jeder Paketoperation genau der Paketmatrix (PKG-08).

PKG-02 [P3] Paketwechsel: Ein Operator wechselt das Paket einer Organisation in einer Transaktion (Up- und Downgrade, PUT /api/v1/admin/orgs/{id}/plan). Funktionen des neuen Pakets werden freigeschaltet, Paketfunktionen außerhalb des neuen Pakets gesperrt (enabled=false); Add-ons und manuelle Freischaltungen bleiben unverändert. Es werden keine Daten gelöscht (ENT-08). Die Standardlimits des neuen Pakets werden gesetzt, gespeicherte Abweichungen je Organisation bleiben. Der Wechsel steht mit Vorher/Nachher in operator_audit, erzeugt das Ereignis entitlement.changed, benachrichtigt die org_admins und wirkt ohne Cache-Verzögerung.

PKG-03 [P3] Add-ons: Add-ons (iga, ai sowie die in PKG-08 als Add-on geführten Funktionen) werden je Organisation unabhängig vom Paket gebucht und gekündigt (POST/DELETE /api/v1/admin/orgs/{id}/addons/{key}), mit Laufzeit valid_until und Quelle. Ein Paketwechsel ändert gebuchte Add-ons nicht. Ein Schlüssel, der kein Add-on ist, ist nicht buchbar (422).

PKG-04 [P3] Limits je Paket: Jedes Paket hat Standardwerte für max_cis, max_collectors, max_users, max_api_keys, max_storage_mb und die Monitoring-Kontingente max_monitored_targets, max_probes_per_minute und min_probe_interval_s. Der Operator überschreibt sie je Organisation, optional mit Kappungen je Probetyp (Schlüssel aus PRT-01). Die Prüfung ist unter Parallelität exakt (kein Überschreiten durch gleichzeitige Anlage). Überschreitung eines Kontingents ergibt 403 entitlement-limit, ein Intervall unter min_probe_interval_s 422 mit Feldfehler.

PKG-05 [P3] Verbrauch: Mandant (entitlement:read) und Operator sehen je Limit aktuellen Verbrauch, Grenze und Herkunft der Grenze (Paket oder Abweichung); für das Monitoring zusätzlich die Zahl der Prüfziele und die konfigurierten Prüfungen pro Minute.

PKG-06 [P3] Mandantenoberfläche „Paket und Add-ons“: Paket, enthaltene und gesperrte Funktionen im Paketvergleich, gebuchte Add-ons mit Laufzeit, Limits mit Verbrauch und Ablaufdaten. Hinweisseiten gesperrter Funktionen verlinken darauf. Kauf oder Wechsel durch den Mandanten ist nicht Bestandteil.

PKG-07 [P3] Operator-Oberfläche: Liste der Organisationen mit Paket und Verbrauch, Paketwechsel, Add-on-Buchung und -Kündigung, Limits und Verlauf aus operator_audit; nur mit Operator-Sitzung nach SEC-07, jede Aktion auditiert.

PKG-08 [P3] (V) Paketmatrix Phase 3: Die Funktionen der Phase 3 sind Feature-Schlüsseln und Paketen zugeordnet: Grundmonitoring, Netzprotokoll- und Zugangsnetz-Monitoring, Monitoring-Analytik (SLA, Prognosen), IPAM/ASN, Tresor-Anbindung für Zugangsdaten, SMS/Pager, Workflows, Compliance/Findings, Compliance-Automation und ADMP. Jeder P3-Pfad und jede P3-Teilfunktion ist über genau einen dieser Schlüssel gegatet. Die Matrix präzisiert ENT-06 für Phase 3. Abweichend von ENT-06 (dort monitoring nur Enterprise) ist Monitoring ab Standard mit den Kernfunktionen verfügbar (Erreichbarkeitsprüfungen ICMP, TCP, HTTP/HTTPS und DNS, Zustandswechsel, Benachrichtigung, Root-Cause-Unterdrückung, Flapping, Wartungsfenster, Verfügbarkeitshistorie); Schwellwertregeln, Handlungsempfehlungen und Netzprotokoll-Probes beginnen ab Pro, Zugangsnetz und Analytik ab Enterprise (Beschluss des Auftraggebers vom 2026-11-09).

PKG-09 [P3] Wirkung von Sperren auf das Monitoring: Fällt durch Paketwechsel, Kündigung oder Ablauf eine Monitoring-Funktion weg, werden deren Probes pausiert, nicht gelöscht, und nach erneuter Freischaltung mit unveränderter Konfiguration fortgesetzt. Überschreitet die Organisation ein Kontingent, wird nur die Neuanlage gesperrt (ENT-08). Die org_admins werden benachrichtigt.

Probes: Definition, Verteilung, Zustand, Datenhaltung (PRB, Ergänzung zu Abschnitt 28)

PRB-01 [P3] Probe-Definition und Override-Hierarchie: Probes werden zentral im Server gepflegt als probe(organization, client, site, ci, collector_group, type, target, params, enabled, parent_probe). Jeder Parameter (u. a. Intervall, Timeout, Down-Count, Up-Count, Schwellen) ist auf den Ebenen Organisation → Kunde → Gerätegruppe → Gerät (CI) → Probe überschreibbar; die spezifischste gesetzte Ebene gewinnt, ohne Wert gilt der Typstandard aus PRT-01. Gerätegruppe ist derselbe Gruppenbegriff wie in CRD-04. Die API liefert je Probe die effektiven Werte und je Feld die Herkunftsebene. Werte außerhalb der Typgrenzen (PRT-01) oder des Paketkontingents (PKG-04) werden beim Schreiben auf jeder Ebene mit 422 und Feldfehler abgewiesen. Die Standardwerte des ICMP-Pings stehen in PRT-02.

PRB-03 [P3] Collector-Gruppen: Probes laufen ausschließlich auf Collectoren im Kundennetz. Jede Probe gehört genau einer Collector-Gruppe mit mindestens einem Mitglied; der Server weist jede Probe genau einem gesunden Mitglied zu. Fällt ein Mitglied aus (PRB-10), übernehmen die übrigen dessen Probes innerhalb von 60 s ab dem letzten Lebenszeichen. Ist kein Mitglied erreichbar, gilt das als Netzausfall: alle Probes der Gruppe wechseln nach unknown mit Grund collector_unreachable, es entsteht genau ein Root-Ereignis collector_group.unreachable und kein down-Ereignis einzelner Probes. Gemeldet wird der Ausfall wie ein Netzausfall. Im Protokoll steht die Zeit mit Grund collector_unreachable getrennt; für die Zeit, für die der Collector nach der Wiederverbindung gepufferte Ergebnisse nachliefert (PRB-07), gilt der gemessene Zustand (Beschluss des Auftraggebers vom 2026-11-09).

PRB-04 [P3] Jitter: Der Collector verteilt Prüfungen deterministisch über das Intervall mit Startphase = Hash(probe_id) mod Intervall. Die Phase bleibt über Neustarts und Collector-Wechsel stabil. Bei mindestens 100 Probes gleichen Intervalls starten in keiner Sekunde mehr als das 1,5-Fache des Mittelwerts.

PRB-05 [P3] Zustandsmaschine: Der Collector führt je Probe die Zustände up, degraded, down und unknown. down gilt nach Down-Count aufeinanderfolgenden Fehlschlägen, up nach Up-Count aufeinanderfolgenden Erfolgen, degraded bei Erfolg mit verletzter Schwelle (z. B. RTT, Verlust), unknown, wenn die Probe nicht aussagefähig ist (Grund u. a. collector_unreachable, unsupported, limit_exceeded, auth_failed). Zustandswechsel werden ohne Batch-Wartezeit übertragen, p95 < 5 s bei bestehender Verbindung. Messwerte gehen gebündelt (höchstens 10 s oder 1.000 Ergebnisse je Bündel). Jeder Zustandswechsel trägt Sequenznummer, Epoche, Grund, Zeitstempel und letztes Ergebnis.

PRB-06 [P3] Datenhaltung: Rohergebnisse 14 Tage; Aggregate 5 min 90 Tage, 1 h 400 Tage, 1 d 400 Tage; Zustandswechsel und tägliche Verfügbarkeit je Probe 400 Tage. Kompression der Rohdaten ab 1 Tag. Die Verfügbarkeit wird aus Zustandsintervallen sekundengenau berechnet, nicht aus Stichproben. Rohergebnisse von Probes unterliegen dieser Datenhaltung, nicht der Retention von metric_sample (MON-01). Volumen, Chunk-Größe und Schreibrate sind je Tabelle dokumentiert und unter der Last aus NFR-11 gemessen.

PRB-07 [P3] Pufferung und Nachsynchronisation: Der Collector puffert Ergebnisse und Zustandswechsel persistent mindestens 24 h (vgl. NFR-04). Ein Neustart verliert nichts. Nach Wiederherstellung der Verbindung wird in Sequenzreihenfolge synchronisiert, Zustandswechsel vor Messwerten. Eine Doppelübertragung erzeugt weder Doppelzeilen noch doppelte Ereignisse. Nachgereichte Zustandswechsel sind als late markiert und lösen für bereits beendete Zustände keine Benachrichtigung aus. Bei Pufferüberlauf werden zuerst Messwerte verdichtet; Zustandswechsel werden innerhalb von 24 h nie verworfen. Jeder Verlust wird gezählt und gemeldet.

PRB-09 [P3] Zeit und Reihenfolge: Zeitstempel sind UTC-Zeitpunkte der Messung am Collector (observed_at). Die Reihenfolge ergibt sich aus Epoche und Sequenznummer je Collector, nicht aus der Uhrzeit. Der Server misst die Uhrabweichung je Collector über das Lebenszeichen (PRB-10). Ab 2 s Abweichung wird gewarnt (collector.clock_skew); ab 60 s gilt das Mitglied als degraded, und seine Zeitstempel werden um die gemessene Abweichung korrigiert und als korrigiert markiert.

PRB-10 [P3] Lebenszeichen: Jeder Collector sendet für das Monitoring alle 10 s ein Lebenszeichen mit Uhrzeit, Konfigurationsversion, Lastzahlen und Pufferstand. Ein Gruppenmitglied gilt nach dem Liveness-Timeout seiner Gruppe (Standard 30 s, konfigurierbar 20–300 s) als ausgefallen. Der Heartbeat und der Offline-Status nach 5 min aus COL-05 bleiben davon unberührt.

PRB-11 [P3] Konfigurationsverteilung: Probe-Konfigurationen werden versioniert an den zuständigen Collector verteilt: Abruf über NATS, Änderungshinweis per Push, lokale Sicherung für einen Neustart ohne Server. Ein Collector prüft ausschließlich die ihm mit gültiger Epoche zugewiesenen Probes. Ergebnisse mit veralteter Epoche werden gespeichert, entscheiden aber keinen Zustand (Split-Brain-Schutz).

Probetypen (PRT, Ergänzung zu Abschnitt 28)

PRT-01 [P3] Typkatalog und Typgrenzen: Jeder Monitoring-Probetyp steht mit Schlüssel, Parameterschema (JSON-Schema), Standardwerten, Systemgrenzen (Mindestintervall, Standard- und Höchst-Timeout, Timeout ≤ Intervall, maximale Parallelität je Ziel und je Collector, maximale Rate je Collector, maximale Probes je Ziel, Payload- und Zeilengrenzen), benötigten Credential-Namespaces, benötigten Collector-Fähigkeiten (PRT-18) und Status (planned|available) in api/probe-types.json; die Typschlüssel sind dort einmal verbindlich festgelegt und werden von Paketmatrix (PKG-08) und Kontingenten (PKG-04) nur referenziert. Der Katalog umfasst die Typen aus PRT-02 bis PRT-17 sowie Redfish-Health für MON-03. Typen mit Status planned sind nicht anlegbar (422). Server (Anlage/Änderung, 422 mit Feldfehler) und Collector (Ablehnung vor Ausführung) prüfen gegen denselben Vertrag; ein Konformitätstest auf beiden Seiten schlägt bei Abweichung fehl. Der Collector setzt Parallelität und Rate je Typ zur Laufzeit durch; eine Überschreitung ergibt unknown mit Grund limit_exceeded und erhöht einen Zähler je Typ. Kaufmännische Kontingente (Probes gesamt und je Typ, Mindestintervall) kommen aus den Entitlement-Limits (PKG-04) und können die Typgrenzen nur verschärfen.

PRT-02 [P3] ICMP-Ping: Standardwerte Intervall 30 s, Timeout 10 s, Down-Count 5, Up-Count 1; über die Hierarchie aus PRB-01 überschreibbar in den Grenzen Intervall 10 s–1 h, Timeout 1–10 s und < Intervall, Down-Count 1–20, Pakete je Prüfung 1–5, Payload 0–1472 Byte. IPv4 und IPv6 über einen gemeinsamen ICMP-Socket je Adressfamilie und Collector. Metriken: RTT min/avg/max, Verlustquote, Jitter. Optionale Schwellen für RTT und Verlust führen zu degraded. Das Poll-Minimum von 5 min aus DIS-10 gilt für diesen Typ nicht.

PRT-03 [P3] TCP-Connect: Verbindungsaufbau zu Host:Port mit optionalem Banner-Abgleich (Lesen ≤ 1 KiB; gesendet wird nichts außer einem konfigurierten Sendestring ≤ 256 Byte). Ursachen refused und timeout werden getrennt gemeldet. Metrik connect_seconds. Standard Intervall 60 s, Timeout 5 s; Grenzen Intervall ≥ 10 s, Timeout ≤ 30 s.

PRT-04 [P3] HTTP/HTTPS: nur Methoden GET und HEAD. Erwartete Statuscodes, Body-Regex über höchstens 64 KiB (Systemgrenze 1 MiB), höchstens 5 Redirects, TLS-Prüfung nach TLS-Scopes, Restlaufzeit des Zertifikats als Metrik, Phasenzeiten (DNS, Connect, TLS, TTFB, gesamt). Authentisierung über Credential-Art http (Basic oder Bearer, PRT-22). Standard Intervall 60 s, Timeout 10 s; Grenzen Intervall ≥ 30 s, Timeout ≤ 60 s.

PRT-05 [P3] SNMP-Status: Erreichbarkeit über sysUpTime mit Reboot-Erkennung und bis zu 20 OID-Prüfungen mit Operator (eq, ne, lt, gt, in, regex) und Folgezustand (degraded|down). Ein Authentisierungsfehler ergibt unknown mit Grund auth_failed, nicht down. SNMP v2c/v3 mit Zugangsdaten aus dem Namespace snmp.

PRT-06 [P3] SNMP-Interfaces: je ausgewähltem Interface (Namensmuster, ifIndex-Liste, nur admin-up; höchstens 1024 je Probe) Oper-/Admin-Status, Raten in/out (bit/s) mit Wrap- und Discontinuity-Behandlung, Auslastung in %, Fehler- und Discard-Raten. Oper-down bei admin-up eines als kritisch markierten Interfaces führt zu down, sonst zu degraded.

PRT-07 [P3] Routing-Protokolle: BGP-Peers (BGP4-MIB, herstellerspezifische BGP4-V2-Tabellen über Profile, alternativ gNMI/OpenConfig), OSPF-/OSPFv3-Nachbarn und IS-IS-Adjazenzen mit Sessionzustand, Established-/Up-Zeit, Prefix-Zählern (soweit verfügbar) und Erkennung von Session-Resets. Erwartete Peers/Nachbarn sind konfigurierbar; ein fehlender oder nicht etablierter erwarteter Peer führt zu down.

PRT-08 [P3] BFD-Status: Sessions aus BFD-STD-MIB (RFC 7331) bzw. Herstellerprofil mit Zustand und Diagnose-Code; erwartete Sessions konfigurierbar; eine erwartete Session, die nicht up ist, führt zu down.

PRT-09 [P3] Optical Monitoring (DOM/DDM): je Transceiver Rx-/Tx-Pegel (dBm), Temperatur, Bias-Strom und Spannung, zugeordnet zum Interface. Schwellen aus den Gerätealarmschwellen oder je Probe parametriert; Überschreitung einer Warnschwelle führt zu degraded, einer Alarmschwelle oder LOS zu down. Generischer Kern über ENTITY-SENSOR-MIB, Herstellererweiterungen über Profile (PRT-20).

PRT-10 [P3] PPPoE/BNG: aktive Subscriber-Sessions je BNG gesamt und je Zugangsart/Interface nach Herstellerprofil. Ein Einbruch gegenüber dem Vorwert um mehr als einen parametrierbaren Anteil (Standard 20 %) führt zu degraded bzw. – ab einer zweiten parametrierbaren Schwelle – zu down. Ein aktiver PPPoE-Logintest ist nur Bestandteil, wenn der Auftraggeber ihn bestätigt.

PRT-11 [P3] PON/OLT/ONU: je OLT und PON-Port Anzahl ONUs online/offline; je ONU Zustandsänderungen, Rx-Pegel an ONU und OLT, letzte Down-Ursache (u. a. LOS, Dying Gasp) und Seriennummer als Label für die Zuordnung zum Anschluss. Generischer Kern mit Herstellerprofilen (PRT-20); höchstens 8192 ONUs je OLT.

PRT-12 [P3] DNS-Abfrage: Abfrage an einen konfigurierten Server über UDP oder TCP mit qname, qtype (A, AAAA, CNAME, MX, NS, SOA, TXT, PTR, SRV, CAA), erwartetem RCODE, optional erwarteten Antwortinhalten und AD-Bit; SOA-Serial als Metrik. Antwortgrenze 4096 Byte (UDP) bzw. 64 KiB (TCP).

PRT-13 [P3] DHCP-Test (DHCPv4): Test ohne Lease-Verbrauch (DHCPINFORM oder DISCOVER/OFFER ohne REQUEST; Relay-Modus mit giaddr für entfernte Segmente). Metriken: Antwortzeit; angebotene Adresse als Label. Fehlt dem Collector die nötige Port-Fähigkeit (PRT-18), ist der Zustand unknown mit Grund unsupported. Grenzen: Intervall ≥ 60 s, höchstens 1 Test/s je Collector.

PRT-14 [P3] TWAMP/STAMP: Session-Sender nach RFC 8762 (unauthenticated), kompatibel zu TWAMP-Light-Reflektoren. 1–100 Testpakete im Abstand ≥ 10 ms, DSCP konfigurierbar, Paketgröße bis 1472 Byte. Metriken: RTT min/avg/max, Jitter, Verlust; Einwegverzögerung nur bei gemeldeter Zeitsynchronisation (PRT-18). TWAMP-Control ist nur Bestandteil, wenn der Auftraggeber den Umfang bestätigt.

PRT-15 [P3] TR-069/TR-369: Der Collector liest Gerätezustände ausschließlich aus einem vorhandenen ACS bzw. USP-Controller über dessen Northbound-API und nur mit lesenden Aufrufen (letzter Inform, ausgewählte Parameter). Ein Test weist nach, dass der Adapter keine schreibende Methode aufrufen kann. Ein letzter Inform älter als das n-Fache des Inform-Intervalls (n parametrierbar) führt zu down. Credential-Art acs (PRT-22).

PRT-16 [P3] gNMI/OpenConfig: Get bzw. Subscribe ONCE über TLS (Pflicht) mit Benutzer/Passwort und/oder Client-Zertifikat (Credential-Art gnmi, PRT-22) auf konfigurierte bzw. profilgegebene OpenConfig-Pfade (höchstens 32 Pfade, Nachrichten ≤ 4 MiB, höchstens 10.000 Updates je Antwort). Werte werden als Metriken abgebildet. BGP- und Optik-Probes (PRT-07, PRT-09) können gNMI als Quelle wählen.

PRT-17 [P3] RPKI: RTR-Client (RFC 8210, Rückfall RFC 6810) zu einem Validator im Kundennetz mit Erreichbarkeit, Serial, VRP-Anzahl v4/v6 und Alter der Daten; ROA-Validierung konfigurierter Präfix/Origin-AS-Paare nach RFC 6811 (valid, invalid, notfound). invalid führt zu down, ein veralteter Cache zu degraded. Höchstens 2 Mio. VRPs je Validator, eine RTR-Session je Validator und Collector.

PRT-18 [P3] Fähigkeitsmeldung: Der Collector meldet im Lebenszeichen die verfügbaren Probetypen und Fähigkeiten (ICMP unprivilegiert/raw/keine, IPv6, bindbare privilegierte UDP-Ports, Zeitsynchronisation). Eine Probe, deren Fähigkeit fehlt, wird als unknown mit Grund unsupported gemeldet, nie als down. Der Server berücksichtigt die Meldung bei der Zuweisung an Gruppenmitglieder (PRB-03).

PRT-19 [P3] Durchsatznachweis je Collector: Ein Collector führt den Referenz-Typmix (Anteile nach Auftraggeber, vorläufig 80 % ICMP, 10 % TCP/HTTP, 10 % SNMP) mit mindestens 353 Probes/s über 30 min gegen Simulatoren aus, ohne dass mehr als 1 % der Probes ihren Fälligkeitszeitpunkt um mehr als 1 s verfehlen. CPU, Speicher, Goroutinen und Dateideskriptoren sind in docs/ops/sizing.md dokumentiert. Der Wert ist ein Referenzwert je Collector; die Gesamtlast der Instanz regelt NFR-11.

PRT-20 [P3] Herstellerprofile für Monitoring: Herstellerabhängige Probetypen (Routing-v2-Tabellen, BFD, Optik, BNG, PON) lesen Tabellen, Indexarten, Enum-Abbildungen, Skalierungen und Schwellwert-OIDs aus dem Profilabschnitt monitor. Neue oder geänderte Profile wirken über RETICORA_PROFILES_DIR ohne Release; ungültige Profile werden beim Laden abgewiesen.

PRT-21 [P3] Testpflicht je Probetyp: Unit-Tests gegen einen Simulator/Fake (fakesnmp mit Walk-Fixtures, httptest, fakedns, fakedhcp, fakestamp, fakegnmi, fakertr, Fake-ACS) für up, down, degraded, unknown und deren Gründe sowie ein Limits-Test (Ablehnung außerhalb der Grenzen, Payload-/Zeilenkappung, Parallelität je Ziel).

PRT-22 [P3] Credential-Arten für Probes: http (username/password oder token), gnmi (username/password, client_cert/client_key, ca) und acs (username/password oder token) nach COL-02. Übergabe nur im Bundle, Haltung nur im Collector-Speicher, je Art ein eigener Namespace ohne Alias. Bindung pro Gerät oder Tresor-Referenz nach CRD-01 bis CRD-04.

Alarmierung (ALR, Ergänzung zu Abschnitt 28 und 41)

ALR-01 [P3] Alarmmodell: Ein Alarm ist eine Instanz je Quelle (Regel, Probe, Collector, Konfiguration, Kapazität, SLA) und Serie (CI + Labels) mit Deduplizierungsschlüssel. Zustände pending, open, acknowledged, resolved, dazu die Unterdrückungsgründe maintenance, symptom und flapping. Jeder Zustandswechsel wird append-only mit Akteur und Grund protokolliert. Ein erneutes Auslösen desselben Schlüssels erzeugt keinen zweiten offenen Alarm, sondern erhöht den Zähler. Ack und Un-Ack sind Nutzeraktionen mit Audit. Alarme und Zustandswechsel werden 400 Tage aufbewahrt.

ALR-02 [P3] Zeitnahe Übertragung: Ein Zustandswechsel einer Probe (PRB-05) erzeugt oder schließt den zugehörigen Alarm innerhalb von 10 s (p95) nach Eingang im Backend. Die Benachrichtigung zur Wurzelursache wird nach Ablauf des Korrelationsfensters (Standard 30 s, je Org einstellbar 0–300 s) eingereiht. Die Ereignisse alarm.opened, alarm.acknowledged, alarm.resolved, incident.opened und incident.resolved stehen über Outbox, Webhooks und Benachrichtigungen bereit; diese Namen gelten für alle Ergänzungen dieses Auszugs.

ALR-03 [P3] Root-Cause-Korrelation über die Abhängigkeitstopologie: Ist ein CI down und sind abhängige CIs über sichere Impact-Pfade (IMP-04/05, Redundanz berücksichtigt) nicht erreichbar, werden deren Alarme als Symptom (Grund symptom) einer Störung (incident) mit dem CI als Wurzel zugeordnet und nicht einzeln benachrichtigt. Das Ergebnis ist unabhängig von der Eingangsreihenfolge (Kind vor Eltern). Ist kein Mitglied einer Collector-Gruppe erreichbar (PRB-03), entsteht eine Störung mit Ursache collector_unreachable; die Probes der Gruppe werden wie bei einem Netzausfall als Symptome geführt.

ALR-04 [P3] Flapping Detection: Erreicht eine Serie N Zustandswechsel innerhalb des Fensters W (Standard 5 in 15 min, je Org und Regel einstellbar), wird der Alarm als flapping markiert. Es folgt genau eine Benachrichtigung; weitere Wechsel benachrichtigen nicht. Den Zustand verlässt die Serie erst nach einer stabilen Phase H ohne Wechsel (Hysterese, Standard 15 min).

ALR-05 [P3] Threshold Monitoring (präzisiert MON-04): Schwellwertregeln je Metrik mit Geltungsbereich (CI, CI-Typ inkl. Untertypen, Client, Label-Selektor). Die Bedingung ist eine MET-45-AST mit Stufen warning und critical, Aggregation (last, avg, max, min, rate) über ein Fenster, Mindestdauer und Rückkehrhysterese (eigener Clear-Schwellwert). Ausgewertet wird jede Serie (CI + Labels) einzeln.

ALR-06 [P3] SLA Monitoring: Verfügbarkeitsziele (Prozent, Periode Monat/Quartal/Jahr) je CI, Dienst (business_service/application), Anschluss oder Client. Die Verfügbarkeit wird aus der Zustandshistorie (PRB-06) berechnet. Zeiten in Wartungsfenstern sind ausgenommen; unknown wird nach Org-Einstellung als verfügbar, nicht verfügbar oder ausgenommen gewertet; die Zeit mit Grund collector_unreachable wird getrennt ausgewiesen und durch nachgelieferte Messdaten ersetzt (PRB-03). Berichte decken bis 400 Tage ab, mit CSV-Export. Ist ein je Ziel festgelegter Anteil des Fehlerbudgets verbraucht, entsteht ein Alarm.

ALR-07 [P3] Konfigurationsänderungserkennung: Der Collector holt die Running-Config über SSH (Zugangsdaten nach CRD-01 bis CRD-04), maskiert Geheimnisse vor dem Versand und übermittelt sie nur bei geändertem SHA-256-Hash. Trigger sind ein Zeitplan, SNMP-Änderungsindikatoren oder gNMI. Das Backend speichert Versionen (Retention 400 Tage), bildet Diffs, erzeugt das Ereignis ci.config_changed und bewertet Änderungen im Zeitfenster vor einer Störung als Ursachenkandidaten.

ALR-08 [P3] Kapazitätsprognosen: Für Interface-Auslastung und andere Zählermetriken wird täglich ein Trend aus Stundenaggregaten berechnet (lineare Regression über 30 und 90 Tage, optional mit Wochensaisonalität). Ergebnis sind Steigung, Bestimmtheitsmaß und der prognostizierte Zeitpunkt der Schwellwertüberschreitung mit Prognoseintervall. Liegt der Zeitpunkt im Horizont (Standard 30 Tage), entsteht ein Alarm. Das Verfahren ist deterministisch und dokumentiert.

ALR-09 [P3] Handlungsempfehlung je Störung, regelbasiert und mit Begründung. Sie enthält: (a) betroffene Anschlüsse aus Topologie und ADMP-Anschlussdaten (ADM-08) mit Netzbetreiber, Vertrags-ID, Adresse und Anschlussinhaber (ADM-06), sofern ADMP gebucht ist; (b) betroffene Dienste und Clients; (c) die wahrscheinliche Ursache mit Ursachenklasse, CI, Konfidenz (0–100) und Begründungszeilen; (d) vorhandene offene Tickets an Wurzel, betroffenen CIs oder Störung über einen Ticket-Dedupe-Schlüssel, zur Verknüpfung statt Doppel-Ticket; (e) noch funktionierende redundante Verbindungen und Provider mit aktuellem Zustand; (f) nächste Schritte aus einem Playbook-Katalog je Ursachenklasse, je Org anpassbar, ergänzt um Empfehlungen aus Workflows (WFL-07). Ein ML-Modell ohne nachvollziehbare Begründung wird nicht eingesetzt.

ALR-10 [P3] Störungsansicht im UI: Liste offener und kürzlich gelöster Störungen mit Wurzel, Ursache, Zahl betroffener Anschlüsse und Dienste sowie Ticketstatus. Die Detailansicht zeigt die Handlungsempfehlung (ALR-09), eine Zeitleiste der Zustandswechsel, die Symptom-Alarme, einen Link zur Impact-Ansicht und die Aktionen Ack, Ticket anlegen oder verknüpfen sowie Ursache manuell festlegen. Bedienbar per Tastatur, i18n de/en.

ALR-11 [P3] Alarmunterdrückung in Wartungsfenstern (MGT-04): Während eines aktiven Fensters werden Alarme der Fenster-CIs und der sicher betroffenen CIs (IMP-05) mit Grund maintenance erfasst, aber nicht benachrichtigt und nicht als SLA-Verletzung gezählt. Ist ein Alarm bei Fensterende noch offen, wird er benachrichtigt (je Fenster abschaltbar).

ALR-12 [P3] Alarmflutbegrenzung: Benachrichtigungen werden je Störung gebündelt (eine Benachrichtigung mit Anzahl betroffener CIs, Anschlüsse und Dienste statt einer je Symptom). Über ein Org-Limit von Alarmbenachrichtigungen pro Minute hinaus folgt eine Sammelbenachrichtigung. Benachrichtigungen kritischer Wurzelalarme werden nie verworfen.

Hinweis zu NTF-02 (Abschnitt 41, unverändert): Die Eskalation der Alarmierung ist Bestandteil von NTF-02 und hat keine eigene ID. Präzisierung des Auftraggebers: Eine Eskalationsrichtlinie je Org, Schweregrad und optional Client legt Stufen fest (Verzögerung, Zielgruppe nach NTF-03). Ohne Ack innerhalb der Verzögerung folgt die nächste Stufe; Ack oder Auflösung stoppen die Kette. SMS- und Pager-Kanäle docken als Kanäle an dieselbe Kette an. Nach SEQ-01 entsteht die Eskalation mit Epic F; bis dahin benachrichtigt die Alarmierung ohne Eskalationsstufen.

IPAM und ASN (IPM, Ergänzung zu Abschnitt 14)

IPM-01 [P3] ipam_section(org, client nullable, name unique pro Org, description, strict_mode bool, show_vlan, show_vrf); jede Organisation erhält die Default-Section „default“ (Trigger und Backfill); subnet.section_id NOT NULL; CRUD /api/v1/ipam-sections; eine Section mit Subnetzen ist nicht löschbar (409).

IPM-02 [P3] Hierarchische Subnetze: subnet.parent_id wird beim Anlegen, Ändern und Löschen aus dem engsten umschließenden Subnetz derselben VRF berechnet (Master/Nested); is_pool kennzeichnet Container ohne eigene Hostadressen; im strict_mode der Section ist Überlappung ohne echtes Enthaltensein 409; GET /subnets?parent_id= und GET /subnets/{id}/children; Löschen hängt Kinder an den Elternknoten um.

IPM-03 [P3] Belegung und Freiraum: GET /subnets/{id}/usage liefert Gesamtzahl, belegt je Typ und Zustand, frei und Prozent (Pools: Abdeckung durch Kinder); GET /subnets/{id}/free-addresses und /free-subnets?prefix_length= liefern die nächsten freien Adressen bzw. Teilnetze; POST /subnets/{id}/allocate und /allocate-subnet vergeben die nächste freie Adresse bzw. das nächste freie Teilnetz atomar unter einer Sperre je Subnetz.

IPM-04 [P3] Adresszustand und Gateway: ip_address.state active|offline|unknown mit state_changed_at, getrennt von type (NET-03 bleibt Provenienz/Zweck); ip_address.is_gateway; das Gateway eines Subnetzes muss im Subnetz liegen (422) und wird als Adresse mit is_gateway geführt; Zustandsabbildung phpIPAM Used→active, Offline→offline, Reserved→type reserved, DHCP→type dhcp.

IPM-05 [P3] Nameserver-Sets: nameserver_set(org, name, servers inet[], description); subnet.nameserver_set_id optional; effektive DNS-Server eines Subnetzes = eigene dns_servers, sonst die des Sets; CRUD /api/v1/nameserver-sets.

IPM-06 [P3] VLAN-Domänen: vlan_domain(org, name, description) mit Zuordnung zu Standorten; vlan.domain_id optional; ein VLAN mit Domäne ist eindeutig je (Domäne, vlan_id), ohne Domäne gilt die bestehende Eindeutigkeit unverändert; CRUD /api/v1/vlan-domains.

IPM-07 [P3] VRF-Attribute: route_distinguisher (eindeutig je Org, Format ASN:nn oder IP:nn), import_targets und export_targets, Zuordnung zu Sections; die Default-VRF (CH10) bleibt unveränderlich.

IPM-08 [P3] Änderungsverlauf: ipam_change (append-only; org, object_kind subnet|ip_address|vlan|vrf|section, object_id, actor_id als Surrogat, source manual|discovery|scan|import|allocation, change_type, field_name, old_value, new_value, changed_at) für alle Schreibpfade einschließlich Discovery-Sync; GET /subnets/{id}/history und /ips/{id}/history.

IPM-09 [P3] Import und Export: CSV-Import von Subnetzen und Adressen als Job mit Trockenlauf, Fehlerbericht je Zeile und phpIPAM-kompatiblen Spaltennamen; CSV-Export von Subnetzen und Adressen über den Exportjob mit Feldrechte-Filter.

IPM-10 [P3] Berechtigungen je Section: ipam_section_permission(section, team, level read|write); hat eine Section Einträge, lesen bzw. schreiben ihre Subnetze und Adressen nur Principals mit passendem Team, zusätzlich zu ipam:read/ipam:write; Durchsetzung über RLS, nachgewiesen im Kanalmatrix-Test.

IPM-11 [P3] Subnetz-Scan: subnet.scan_enabled, scan_interval, discover_new_hosts und collector_group_id (Redundanz über die Collector-Gruppe nach PRB-03). Der Scan ist eine ICMP-Probe der Probe-Engine (PRB, PRT-02) im Kundennetz; er setzt ip_address.state und last_seen_at, mit discover_new_hosts legt er unbekannte antwortende Adressen als ip_address mit type discovered und origin scan an und setzt Adressen nach einer konfigurierbaren Frist ohne Antwort auf offline. Abgrenzung zu DIS-01: Der Subnetz-Scan legt keine CIs an und durchläuft nicht Ingest und Reconciliation; der Discovery-Sweep nach DIS-01 bleibt unverändert und ist die einzige Quelle für Geräteerkennung. Für den Scan gelten die Grenzen von PRT-01/PRT-02 und die Kontingente nach PKG-04, nicht die Parallelität aus DIS-01.

IPM-12 [P3] DNS-Abgleich: PTR-Auflösung je Adresse über den Collector (ptr_name, ptr_checked_at); eine Abweichung von dns_name wird markiert und gelistet; Section-Einstellung ptr_mode off|report|adopt.

IPM-13 [P3] Geräte ↔ IP: ip_address.ci_id (gleich dem CI des Interfaces, wenn gesetzt) und role primary|secondary|vip|anycast|loopback|management; ci.management_ip und die Adresse mit role management werden in derselben Transaktion konsistent gehalten; GET /cis/{id}/addresses; Anzeige der Adressen am CI und des Geräts und Interfaces an der Adresse.

IPM-14 [P3] Automatische Übernahme: Adressen ohne subnet_id erhalten das engste Subnetz ihrer VRF (longest prefix), auch bei Anlage oder Löschung von Subnetzen; ip_address.origin manual|discovery|scan|import und pinned; Discovery überschreibt keine gepinnte oder manuelle Bindung; sieht Discovery eine gepinnte Adresse an einem anderen Gerät, entsteht ein Review ip_binding_conflict.

IPM-15 [P3] NAT (optional, Umfang nach Entscheidung zur Phase): nat_translation(org, kind static|source|destination|pat, device_ci, inside, outside, Ports, description); CRUD und Anzeige an den beteiligten Adressen.

IPM-16 [P3] ASN-Verwaltung: asn(org, number 1–4294967295 eindeutig je Org, name, kind own|customer|peer|upstream|ixp, client nullable, rir, description) und asn_range(org, name, start, end, purpose, client nullable); POST /asn-ranges/{id}/allocate vergibt die nächste freie ASN atomar; subnet.origin_asn_id verknüpft Präfixe mit ihrem Origin-AS.

IPM-17 [P3] BGP-Peerings: bgp_peering(org, device_ci, vrf, local_asn, remote_asn, local_address, remote_address, expected_state, description) als Soll-Inventar; der beobachtete Sitzungszustand aus der BGP-Probe (PRT-07) wird über (Gerät, VRF, Remote-Adresse) zugeordnet und angezeigt; beobachtete Sitzungen ohne Soll-Eintrag erzeugen ein Review bgp_peer_unknown.

IPM-18 [P3] RPKI: Validierungsstatus valid|invalid|not_found|unknown je (Präfix, Origin-ASN) aus der RPKI-Probe (PRT-17) mit checked_at und Quelle; invalid wird als Ereignis an die Alarmierung übergeben und an Subnetz und ASN angezeigt.

IPM-19 [P3] IPAM-Oberfläche: Seite IPAM mit Sections, Subnetzbaum, Belegungsbalken, Adressliste und Adresskarte, Zuweisung der nächsten freien Adresse, Verlauf, VLANs mit Domänen, VRFs, Nameserver-Sets, ASNs und Peerings; CI-Detail mit Interfaces und Adressen.

IPM-20 [P3] Filter und Suche: Die Subnetzliste filtert nach Section, VRF, VLAN, Kunde, Standort, Elternknoten und contains=<Adresse>; die Adressliste nach Zustand, Typ, Herkunft, Kunde und CI; Subnetze und Adressen sind in der globalen Suche auffindbar (search_document, SR-04).

Zugangsdaten (CRD, Ergänzung zu Abschnitt 19)

CRD-01 [P3] credential_binding(org, credential_id, target_kind ci|device_group|subnet|probe, target_id, namespace, priority, client_id, site_id); CRUD /api/v1/credential-bindings und /cis/{id}/credentials mit credential:bind; ein Kunden-Credential ist nur an Ziele desselben Kunden bindbar (422).

CRD-02 [P3] Quelle einer Zugangsart: credential.source stored|vault_ref; stored = gerätespezifisches Geheimnis mit Envelope-Verschlüsselung (COL-02); vault_ref = Verweis (Tresorverbindung, Pfad, optional Version, Feldzuordnung) ohne gespeichertes Geheimnis; die API nimmt je Credential genau eine der beiden Formen an (sonst 422).

CRD-03 [P3] Tresorverbindung (Vorgriff auf COL-03 [P4], beschränkt auf das Lesen von Zugangsdaten; die zentrale KMS-Anbindung bleibt in COL-03 [P4]): vault_connection(org, client nullable, name, kind openbao_kv2|hashicorp_kv2, address, namespace, mount, auth_method approle|token, auth_credential_id, ca_pem, resolve_at collector|server, collector_ids); CRUD und Verbindungstest als Job mit vault:manage; die Tresor-Anmeldedaten sind selbst ein stored-Credential.

CRD-04 [P3] Auflösung: effektive Zugangsart je (Ziel, Namespace) in der Reihenfolge Probe > CI > Gerätegruppe > Subnetz > Scope > Kunde > Org; Tresor-Referenzen mit resolve_at collector löst erst der Collector im Speicher auf, nie auf Disk; mit resolve_at server nur der Server im Speicher; GET /cis/{id}/credentials/effective zeigt die Herkunft ohne Geheimnis. Gerätegruppe ist derselbe Gruppenbegriff wie in PRB-01.

CRD-05 [P3] Rotation: PUT /credentials/{id}/secret ersetzt das Geheimnis (secret_version, rotated_at) ohne Rückgabe; betroffene Collectoren laden ihr Bundle innerhalb von 60 s neu; Tresor-Werte werden nach TTL bzw. Version neu gelesen; Überschreiten von max_age_days löst eine Benachrichtigung aus; Ereignis credential.rotated.

CRD-06 [P3] Nutzung und Audit: credential_usage(org, credential_id, ci_id, last_success_at, last_failure_at, last_error_class) aus Collector-Meldungen ohne Geheimnis; jede Bindungs-, Quell- und Rotationsänderung wird auditiert; kein API-Feld, Log-, Audit- oder Ereigniseintrag enthält ein Geheimnis oder Tresor-Token (Canary-Test über alle Kanäle).

CRD-07 [P3] Oberfläche: CI-Detail „Zugangsdaten“ mit Auswahl gerätespezifisch (neu anlegen), Tresor-Referenz oder bestehende Zugangsart; effektive Zugangsart je Protokoll mit Herkunft und Nutzungsstatus; Verwaltung der Tresorverbindungen.

ADMP (ADM, Anschlussdaten, nur lesend)

Die konkreten ADMP-Endpunkte (ADM-04 bis ADM-07) werden erst nach Vorlage der ADMP-API-Dokumentation durch den Auftraggeber festgelegt; bis dahin werden keine Endpunkte angenommen. Stand 2026-11-12: Die Dokumentation liegt vor (`docs/plan/phase3/ADMP-API`, OpenAPI 3.1.0, ADMP API 9.14.0). Die verwendeten Endpunkte, die Anmeldung (`POST /V1/user/authentication`, Cookie `AuthToken`) und die Feldabbildung legt `docs/plan/phase3/implementierungsplan.md`, Meilenstein M3.14 (WP-1073 bis WP-1078), fest, die Auslegung `docs/plan/phase3/entscheidungen.md` E-248 bis E-255. Was die Dokumentation offenlässt, ist dort Aufgabe A-86.

ADM-01 [P3] admp_account(org, client nullable, name, base_url, credential_id, rate_limit_rps, timeout_ms, enabled, last_ok_at, last_error_class); CRUD /api/v1/admp/accounts mit admp:manage. Die Zugangsdaten liegen ausschließlich als credential nach CRD-02 vor (stored mit Envelope-Verschlüsselung nach COL-02 oder vault_ref), nie im Konto selbst; keine Antwort, kein Log und kein Audit-Eintrag enthält sie.

ADM-02 [P3] Strikt lesender Client: Das Paket admp kennt ausschließlich GET-Endpunkte und die Anmeldung. Der Transport lässt nur GET auf die Basis-URL und genau die Anmeldeanfrage an die konfigurierte Auth-URL zu und weist jede andere Anfrage vor dem Versand ab. Ein Test weist per Reflexion, AST-Prüfung und Mock-Server nach, dass POST, PUT, PATCH und DELETE technisch nicht erreichbar sind (null solche Anfragen am Mock-Server).

ADM-03 [P3] Betrieb: Token-Bucket je Konto (rate_limit_rps), Timeout, Retry mit Backoff nur für GET bei 429/5xx, Circuit-Breaker je Konto; Ergebnisse im Cache mit TTL; bei Fehler liefert die API den letzten Stand mit stale=true und fetched_at; Metriken je Konto und Fehlerklasse.

ADM-04 [P3] Dienst ↔ Port: Abfrage, welcher Dienst welchem Port zugeordnet ist; Speicherung als admp_service_port mit Verweis auf network_interface (Port des Geräts) und CI.

ADM-05 [P3] Diensteigenschaften eines Dienstes abfragen und mit fetched_at im Cache halten.

ADM-06 [P3] Anschlussinhaber abfragen; gespeichert wird nur ein pii_subject-Surrogat (SEC-05); Anzeige nur mit admp:holder_read; jeder Abruf wird auditiert; Lösch- und Auskunftsworkflows schließen den Cache ein.

ADM-07 [P3] Anschluss einzeln abrufen einschließlich Netzbetreiber, Vertrags-ID und Adresse; GET /api/v1/admp/connections/{id}, mit refresh=true als gezielter Neuabruf.

ADM-08 [P3] Bereitstellung für Handlungsempfehlungen (ALR-09): betroffene Anschlüsse eines Geräts bzw. einer Menge von Interfaces mit Netzbetreiber, Vertrags-ID, Dienst und Redundanzhinweis über eine Go-Schnittstelle und GET /cis/{id}/admp-connections.

ADM-09 [P3] Oberfläche: ADMP-Konten in den Einstellungen; Anschlüsse und Dienste am Interface und am CI mit Stand (fetched_at, stale) und Neuabruf.

Compliance (CMP, Ergänzung zu Abschnitt 31)

CMP-01 [P3] Eine von der Konfigurationsänderungserkennung (ALR-07) gemeldete Änderung an einem CI erzeugt ein Finding der Art unauthorized_change (Severity nach Regel, Default medium), wenn zum Zeitpunkt der Änderung kein Wartungsfenster (MGT-04) für das CI oder einen übergeordneten Knoten aktiv ist. Eine Änderung im Wartungsfenster erzeugt kein Finding, wird aber am CI vermerkt. Das Finding fließt in den Compliance-Score ein und ist als Workflow-Trigger nutzbar.

CMP-02 [P3] Compliance-Regeln können den Compliance-Status aus Findings (höchste offene Severity, Anzahl offener Findings je Art) und den Lifecycle-Zustand als Eingabe verwenden. Der Score ist pro CI, CI-Typ, Client, Standort und Regelkategorie abrufbar und wird täglich als Snapshot für Trendauswertungen über mindestens 400 Tage gespeichert.

Workflows (WFL-05 bis WFL-08, Ergänzung zu Abschnitt 30)

WFL-05 [P3] Alarmzustandswechsel aus dem Monitoring (alarm.opened, alarm.acknowledged, alarm.resolved nach ALR-02) sind Workflow-Trigger mit Filter nach Severity, Probetyp, CI-Typ, Client und Standort (MET-45). Ein Alarm, den die Root-Cause-Korrelation (ALR-03) als Symptom unterdrückt, startet keinen Run; optional startet nur der Wurzelalarm. Zwischen Zustandswechsel und Run-Start liegen bei 10.000 Zustandswechseln pro Minute höchstens 30 s (p95). Pro Definition gilt ein konfigurierbares Ratenlimit (Default 60 Runs/min); Überschreitungen werden als Metrik und im Run-Protokoll ausgewiesen.

WFL-06 [P3] Die Aktion create_ticket hat einen Dedupe-Schlüssel (ci, alert_root_cause, finding oder ein frei gebildeter Schlüssel) und nutzt dieselbe Ticket-Deduplizierung wie die Alarmierung (ALR-09 d). Gibt es zum Schlüssel schon ein nicht geschlossenes Ticket, legt die Aktion kein neues an, sondern hängt einen Systemkommentar und einen Link an das bestehende Ticket und liefert dessen ID im Run-Kontext (ticket_existing=true). Für einen Alarm lässt sich lesend abfragen, ob ein Ticket existiert und welches. Das Auflösen des Wurzelalarms schreibt einen Systemkommentar; ob das Ticket dabei geschlossen wird, ist pro Definition einstellbar.

WFL-07 [P3] Workflow-Definitionen hängen über die Aktion attach_recommendation Handlungsempfehlungen (lokalisierte Runbook-Schritte mit optionalen Verweisen auf Dokumente) an einen Alarm, ein Finding oder ein Ticket. Der Auslöser wird mit MET-45-Bedingungen gewählt, etwa Ursache, Probetyp oder CI-Typ. Die Empfehlung liegt versioniert und auditierbar vor und erscheint als Teil (f) der Handlungsempfehlung nach ALR-09, zusammen mit betroffenen Anschlüssen, wahrscheinlicher Ursache, vorhandenem Ticket und noch funktionierender Redundanz.

WFL-08 [P3] Pro Paket gelten für die Workflow-Engine Limits: Anzahl aktiver Workflow-Definitionen, Runs pro Stunde und Formulardefinitionen. Die Defaults kommen aus dem Paket (Essential/Standard ohne Workflows, Pro und Enterprise mit Limits) und sind pro Mandant über Entitlement-Limits (PKG-04) überschreibbar. Eine Überschreitung lehnt neue Definitionen mit 403 und Grund ab. Runs über dem Limit werden verzögert statt verworfen und als Warnung (entitlement.warning) gemeldet.

Nichtfunktionale Anforderungen (Ergänzung zu Abschnitt 43)

NFR-11 [P3] Prüflast: Je SaaS-Instanz verarbeitet das Monitoring dauerhaft mindestens 353 Prüfergebnisse pro Sekunde (30,5 Mio. Prüfungen pro Tag) ohne Verlust, verteilt über mehrere Collectoren mit Jitter (PRB-04). Die Auslegung hat Reserve Faktor 3 (Ingest ≥ 1.060 Ergebnisse/s dauerhaft) und verkraftet eine Nachsynchronisationsspitze von 5.000 Ergebnissen/s (PRB-07). In allen drei Szenarien gilt: Zustandswechsel stehen p95 < 5 s nach Eingang beim Server als Ereignis bereit, p95 ≤ 10 s vom Collector-Zeitstempel bis zur Sichtbarkeit des Zustands, Alarme nach ALR-02. Die Budgets aus NFR-02 und NFR-03 bleiben unter dieser Last erfüllt. Nachweis im Lasttest vor G3 (Median über drei Läufe, Ergebnisse im Repo).

Gates und Abnahme (Ergänzung zu Abschnitt 44 und 45)

GATE-06 [P3] G3 zusätzlich: Lastnachweis NFR-11 bestanden; Ausfalltests für Collector-Ausfall (Behandlung als Netzausfall, PRB-03), Umschaltung auf redundante Collectoren sowie Pufferung und Nachsynchronisation nach Verbindungsabbruch (PRB-07) bestanden; ADMP-Test „nur GET“ (ADM-02) grün; Sicherheitsreview Phase 3 ohne offenes Critical oder High; Isolationstest (TEN-09) für alle P3-Kanäle vollständig.

ABN-04 [P3] Abnahmeszenario Monitoring (G3): In einer Referenztopologie mit redundanter Anbindung fällt ein Router aus. Reticora meldet einen Root-Cause-Alarm statt Folgealarmen (ALR-03), zeigt die betroffenen Anschlüsse (mit ADMP-Daten, wenn gebucht), die wahrscheinliche Ursache, ein vorhandenes Ticket und die noch funktionierende redundante Verbindung (ALR-09), eskaliert per SMS oder Pager bis zur Quittung (NTF-02) und zeigt nach Wiederherstellung die Verfügbarkeitshistorie (PRB-06). Ablauf, Daten und Protokoll liegen eingefroren in docs/acceptance/.
