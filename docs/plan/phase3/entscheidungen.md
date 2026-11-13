# Entscheidungen – Phase 3 (Monitoring, Netz und Automation, G3)

Stand: 2026-11-12 (Grundlage main `6d019370`; Anforderungen des Auftraggebers vom 2026-11-09; ADMP-API-Doku `docs/plan/phase3/ADMP-API` 9.14.0 vom 2026-11-12). Gehört zu [`implementierungsplan.md`](implementierungsplan.md) und [`abdeckung.csv`](abdeckung.csv) und setzt [`../entscheidungen.md`](../entscheidungen.md) (Phase 1), [`../g1-korrektur/entscheidungen.md`](../g1-korrektur/entscheidungen.md) (Korrekturplan) und [`../phase2/entscheidungen.md`](../phase2/entscheidungen.md) (Phase 2) fort. Die Empfehlung ist ein Vorschlag des Plans und keine neue Anforderung.

**Geltung der früheren Entscheidungen:** Alle Entscheidungen der Phase 1, des Korrekturplans und der Phase 2 sind entschieden (Beschluss des Auftraggebers vom 2026-10-28) und gelten in Phase 3 unverändert weiter. Diese Datei ändert keine von ihnen. Wo eine Phase-3-Entscheidung eine frühere ergänzt oder einschränkt (zum Beispiel [E-34](../entscheidungen.md#e-34) durch [E-151](#e-151) und [E-153](#e-153), [E-44](../phase2/entscheidungen.md#e-44) durch [E-153](#e-153) und [E-157](#e-157)), nennt sie die frühere ausdrücklich; ohne diese Nennung gilt die frühere Festlegung. Zitiert wird mit relativem Link, der Korrekturplan mit dem Zusatz „(Korrekturplan)“, zum Beispiel [E-59](../g1-korrektur/entscheidungen.md#e-59) (Korrekturplan). Alle Aufgaben A-01 bis A-53 sind erledigt (A-28 am 2026-11-09 mit WP-832).

**Nummern:** E-Nummern sind über alle vier Entscheidungsdateien eindeutig. Phase 1 belegt E-01 bis E-36 und E-56 bis E-58, der Korrekturplan E-59 bis E-102 und E-144, Phase 2 E-37 bis E-55, E-103 bis E-143 und E-145 bis E-147. Diese Datei führt E-148 bis E-255. A-Nummern sind über die Dateien der Phasen 1 bis 3 eindeutig: Phase 1 belegt A-01 bis A-14, Phase 2 A-15 bis A-54, diese Datei A-55 bis A-86. Neue Entscheidungen erhalten die nächste freie Nummer über alle vier Dateien, neue Aufgaben die nächste freie Nummer über die Dateien der Phasen 1 bis 3 (Stand dieser Fassung: **E-259** und **A-87**; E-245 und E-246 vom 2026-11-10 nach Empfehlung, E-247 vom 2026-11-12 durch den Auftraggeber, E-248 bis E-255 vom 2026-11-12 nach Empfehlung (ADMP-API-Doku), E-256 vom 2026-11-12 nach Empfehlung, E-257 und E-258 vom 2026-11-13 nach Empfehlung, A-85 und A-86 offen). **Nachtrag vom 2026-11-10:** Phase 2 hat am 2026-11-09 E-146, E-147 und A-54 selbst vergeben. Die Nummern dieser Datei sind deshalb um zwei (E) bzw. eins (A) verschoben; Inhalt und Status bleiben gleich.

**Status:** Alle Entscheidungen dieser Datei sind entschieden, E-148 bis E-244 am 2026-11-09, die späteren an dem Datum in ihrem Statusfeld. Jede nennt Schlüssel, Status, Kontext, Optionen, Empfehlung, Auswirkung, Quelle sowie betroffene IDs und WPs. Die WP-Blöcke des Plans nennen den Status im Feld „Entscheidungsbedarf“.

**Beschluss des Auftraggebers vom 2026-11-09:**

1. Phase 2 wird gerade abgeschlossen. Es gilt [E-148](#e-148) Option 2: M3.0 startet sofort, die Fach-Meilensteine beginnen nach dem GO von WP-331.
2. Monitoring ist ab Standard mit den Kernfunktionen verfügbar ([E-151](#e-151), Option 2 mit Präzisierung). Die erweiterten Funktionen beginnen ab Pro bzw. Enterprise.
3. Ein Collector-Ausfall wird wie ein Netzausfall gemeldet. Im Protokoll wird er getrennt ausgewiesen. Gewertet wird nach den nachgelieferten Daten ([E-181](#e-181), Option 5).
4. Die Datenhaltung folgt der fachlich sinnvollsten Lösung, [E-172](#e-172) Option 1.
5. Die ADMP-API-Dokumentation wird nachgeliefert. A-55 bleibt offen, bis dahin gilt A-84.
6. Fehlt eine Entscheidung, gilt immer die Empfehlung. Alle übrigen Entscheidungen sind danach entschieden; die Aufgaben sind im Abschnitt „Aufgaben“ entsprechend erledigt.

**Nachtrag vom 2026-11-12 (zu Punkt 5):** Die ADMP-API-Dokumentation liegt vor (`docs/plan/phase3/ADMP-API`, OpenAPI 3.1.0, „ADMP API“ 9.14.0). A-55 ist damit erledigt, A-84 gilt nur noch als Rückfallregel, falls M3.14 bis zum Gate nicht abgeschlossen ist. Die Auslegungsfragen der Doku sind als [E-248](#e-248) bis [E-255](#e-255) nach Empfehlung entschieden (Punkt 6). Fachfragen, die nur der Auftraggeber bzw. der ADMP-Betreiber beantworten kann, bündelt [A-86](#aufgaben); bis zur Antwort gelten die Empfehlungen. Vorgabe des Auftraggebers zur Anbindung: strikt nur lesend; der Client kennt ausschließlich die GET-Endpunkte und die Anmeldung; ein Test stellt sicher, dass POST, PATCH und DELETE technisch nicht erreichbar sind ([E-248](#e-248)).

## Aufgaben

Diese Aufgaben kann nur der Auftraggeber erledigen: Lieferungen, Bestätigungen und fachliche Vorgaben, die weder Code noch Katalog beantworten. Sie bündeln die offenen Fragen aus der Planung ohne Dubletten. Die Spalte „Blockierte WPs“ unterscheidet zwei Wirkungen:

- **gesperrt:** Das WP beginnt erst, wenn die Aufgabe erledigt ist, oder es ist bedingt („G3-Umfang: bedingt“) und entfällt ohne Antwort.
- **vorläufig:** Das WP läuft nach der Empfehlung der genannten Entscheidung. Weicht die Antwort ab, wird das WP vor Beginn angepasst oder erhält nach dem Merge ein Folge-WP.

Die Frist bezieht sich auf den Meilenstein des ersten betroffenen WPs. Eine überschrittene Frist wird mit neuem Termin fortgeführt, nicht gestrichen.

| Nr. | Entscheidung | Gegenstand | Verantwortlich | Blockierte WPs | Frist | Status |
|---|---|---|---|---|---|---|
| A-55 | [E-222](#e-222), [E-224](#e-224), [E-225](#e-225) | **ADMP-API-Dokumentation liefern.** System (Betreiber, Produkt, Version); Anmeldeverfahren (OAuth2 Client Credentials, Basic, API-Key oder mTLS) mit Methode und URL der Anmeldung; die GET-Endpunkte für Dienst ↔ Port, Diensteigenschaften, Anschluss und Anschlussinhaber mit Feldern; Ratenlimits, Paginierung, Fehlercodes; Merkmale, die einen Port eindeutig bezeichnen (Gerätename, Portbezeichnung, Line-ID, Zugangsnummer); fachliche Bedeutung von „Anschluss“ und sein Schlüssel zu den Topologie-CIs (Port-ID, Line-ID oder Vertrags-ID). Bis zur Lieferung bleiben die ADMP-Pfade auf `x-status: planned`, und das Gerüst aus M3.5 läuft gegen die Fake-Quelle aus WP-929. | Auftraggeber | gesperrt: WP-1073, WP-1074, WP-1075, WP-1076, WP-1077, WP-1078 (M3.14); vorläufig: WP-1176 (ABN-04-Schritt „betroffene Anschlüsse“ mit neutralem Ersatztext) | vor Beginn von M3.14 (WP-1073); spätestens vor Beginn von M3.20 (WP-1176), sonst steht ADMP in `docs/acceptance/g3-gate.md` (neu) als bekannte Einschränkung (WP-1177) | erledigt (2026-11-12): geliefert als `docs/plan/phase3/ADMP-API` (OpenAPI 3.1.0, ADMP API 9.14.0); Auslegung in [E-248](#e-248) bis [E-255](#e-255), M3.14 konkretisiert. Nicht in der Doku enthalten und offen als A-86: Ratenlimits, Fehlercodes außer 404, Token-Laufzeit, Antwortschemas von `accessnodepicker` und `dienstIds`, Port-Benennung, fachliche Bedeutung von „Anschluss“ über die Schemas hinaus |
| A-56 | [E-223](#e-223) | **ADMP-Zugang und Betrieb.** Ist ADMP aus dem Internet erreichbar (Abruf durch den Server) oder nur aus dem Kundennetz, über feste Quell-IP oder VPN (Abruf durch den Collector oder IP-Freigabe)? Gibt es ein Konto je Reticora-Organisation oder je Endkunde? Gibt es eine Testumgebung mit Testzugang für den Vertragstest? | Auftraggeber | vorläufig: WP-925, WP-926; gesperrt: Smoke-Test gegen die Testumgebung in WP-1077 | vor Beginn von M3.5 (WP-925); Testzugang vor Beginn von M3.14 (WP-1077) | teilweise erledigt (2026-11-09): Empfehlung gilt für WP-925 und WP-926; offen ist nur der Testzugang für den Smoke-Test in WP-1077. Nachtrag 2026-11-12: Die Doku (A-55) enthält weder Basis-URL (`servers`) noch Testzugang; beides bleibt offen |
| A-57 | [E-243](#e-243) | **ADMP-Datenschutz.** Aufbewahrungsdauer für Anschlussinhaber und Anschlussadresse im Zwischenspeicher (Vorschlag 30 Tage); gilt die Adresse eines Geschäftsanschlusses als personenbezogen; wer darf den Inhaber sehen (Vorschlag: nur mit `admp:holder_read`); dürfen Inhaber in Benachrichtigungen und Webhooks erscheinen oder nur in der Störungsansicht; ist der AVV mit dem ADMP-Betreiber geschlossen. | Auftraggeber/Datenschutz | vorläufig: WP-928, WP-930, WP-1044; gesperrt: WP-1075, WP-1076 (zusätzlich zu A-55) | vor Beginn von M3.5 (WP-928); AVV vor Beginn von M3.14 | erledigt (2026-11-09): Empfehlung von E-243 gilt (Punkt 6); WP-1075 und WP-1076 warten nur noch auf A-55 |
| A-58 | [E-151](#e-151) | **Paketmatrix der Phase 3 bestätigen (ENT-06, PKG-08).** Monitoring nur in Pro/Enterprise (`backend/internal/entitlement/entitlement.go:172`) oder schon ab Essential bzw. Standard (Ping-Monitoring); ADMP als Add-on oder Paketbestandteil; Probetypen je Paket; Paketzuordnung von Alarmierung, Root-Cause-Korrelation und Handlungsempfehlung, SLA-Monitoring, Konfigurationsänderungserkennung und Kapazitätsprognose (Paket oder Add-on); Paketzuordnung der IPAM-Erweiterungen unter dem bestehenden Merkmal `ipam` (Empfehlung: ab Standard, kein eigener Schlüssel), IPAM-Grundfunktion weiter in allen Paketen; Workflows in Standard oder erst ab Pro. | Auftraggeber/Vertrieb | vorläufig: WP-862, WP-865, WP-870, WP-883, WP-886, WP-915, WP-1062, WP-1160 | vor dem Merge von WP-862 (M3.0) | erledigt (2026-11-09): Monitoring ab Standard mit den Kernfunktionen, erweiterte Funktionen ab Pro bzw. Enterprise (Beschluss Punkt 2, siehe E-151) |
| A-59 | [E-153](#e-153), [E-154](#e-154) | **Kontingente und Limits je Paket und Probetyp.** Werte `max_monitored_targets`, `max_probes_per_minute` und `min_probe_interval_s` je Paket; Standardlimits `max_cis`, `max_collectors`, `max_users` aus ENT-06 für Bestandskunden; Systemgrenzen je Probetyp (Vorschlag ICMP-Mindestintervall 10 s, PON 120 s, TR-069 300 s; Timeout-Obergrenze, Parallelität und Rate je Collector, Vorschlagswerte in `docs/betrieb/monitoring-probes.md` (Typtabelle aus WP-934)) und ob Pakete sie weiter einschränken; Vorgaben aus Netzbetrieb oder Herstellern (Rate-Limits für SNMP/gNMI auf Routern); Workflow-Limits nach WFL-08 (aktive Definitionen, Runs je Stunde) für Pro und Enterprise. | Auftraggeber/Vertrieb, Netzbetrieb | vorläufig: WP-881, WP-885, WP-940, WP-969, WP-1022, WP-1023, WP-1112 | vor Beginn von M3.2 (WP-881); Typgrenzen vor Beginn von M3.6 (WP-940); Workflow-Limits vor Beginn von M3.16 (WP-1112) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-60 | [E-168](#e-168) | **Bezugsgröße und Mischung der Prüflast (NFR-11).** Gelten 353 Probes/s (30,5 Mio. Prüfungen/Tag) je SaaS-Instanz (Annahme des Plans), je Organisation oder je Collector; Verteilung auf Probetypen, Kunden und Collectoren (Typmix des Last-Seeds); Zahl der Collectoren je Kunde und Probes je Collector beim größten Kunden; Hardware eines Collectors (vCPU, RAM). Die Antwort löst den Widerspruch zwischen WP-958 (5.000 Ping-Probes à 30 s ≈ 167/s je Collector), WP-1023 (353/s je Collector) und WP-1162 (353/s über 20 Collectoren). Bis zur Antwort fahren WP-1023 und WP-1084 denselben vorläufigen Typmix nach [E-168](#e-168) (70/15/10/5); der PRT-19-Wert 80/10/10 läuft in WP-1023 nur als zusätzliche Variante. | Auftraggeber/Netzbetrieb | vorläufig: WP-958, WP-1023, WP-1083, WP-1084, WP-1162, WP-1163, WP-1167 | vor Beginn von M3.7 (WP-958) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-61 | [E-187](#e-187), [E-188](#e-188), [E-193](#e-193) | **Hersteller- und Plattformliste.** OLT-Hersteller und -Modelle (z. B. Huawei MA5800, ZTE C300/C600, Nokia, Adtran, Calix, DZS); BNG-Plattformen (z. B. Cisco ASR9k, Juniper MX, Nokia 7750, MikroTik); Plattformen für die Optikwerte; Router und Switches für gNMI/OpenConfig mit Software-Release, Port und Authentisierung (Passwort oder Client-Zertifikat). Dazu: Gerätealarmschwellen der Transceiver oder eigene Standardschwellen je Kunde (z. B. −20/−25 dBm); 400-Tage-Historie des Rx-Pegels je ONU oder Port-Aggregate plus Zustandswechsel je ONU. | Auftraggeber/Netzbetrieb | vorläufig: WP-1004, WP-1005, WP-1006, WP-1007, WP-1008, WP-1009, WP-1015, WP-1016, WP-1017 | vor Beginn von M3.10 (WP-1004) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-62 | [E-191](#e-191) | **TWAMP-Control bestätigen.** Welche Reflektoren sind im Einsatz (Hersteller; TWAMP-Light/STAMP oder volles TWAMP mit Control-Session)? Ohne Nennung von Reflektoren mit Control-Session bis zum Abschluss von M3.10 entfällt WP-1014; WP-1013 schließt PRT-14 dann mit dokumentierter Einschränkung (kein TWAMP-Control; Hinweis in `abdeckung.csv`, `abschluss_wp` WP-1013). WP-1177 führt PRT-14 dann im Abschnitt GATE-02 von `docs/acceptance/g3-gate.md` (neu) und in `docs/releases/phase3.md` (neu) als bekannte Einschränkung (siehe [WP-1177](implementierungsplan.md#wp-1177--g3-gateprüfung-traceability-bqp2p3-blockierend-g3-stufe-der-gate-prüfung-checkliste-g3-gatemd-release-notes-phase-3), M3.20); G3 ist dadurch nicht blockiert. | Auftraggeber/Netzbetrieb | gesperrt: WP-1014 (bedingt); vorläufig: WP-1013 | vor Beginn von M3.10 (WP-1013); spätestens vor Abschluss von M3.10 | erledigt (2026-11-09): Empfehlung gilt (Punkt 6). Ohne Nennung von Reflektoren mit Control-Session bis zum Abschluss von M3.10 entfällt WP-1014, WP-1013 schließt PRT-14 mit dokumentierter Einschränkung |
| A-63 | [E-192](#e-192) | **TR-069-ACS und TR-369-Controller nennen.** Welcher ACS (GenieACS, Axiros, Friendly, eigene Lösung) und welcher USP-Controller sind im Einsatz; gibt es eine dokumentierte lesende Northbound-API mit Testzugang? Ohne Nennung des USP-Controllers bis zum Abschluss von M3.10 entfällt WP-1019; WP-1018 schließt PRT-15 dann mit dokumentierter Einschränkung (kein USP; Hinweis in `abdeckung.csv`, `abschluss_wp` WP-1018). WP-1177 führt PRT-15 dann im Abschnitt GATE-02 von `docs/acceptance/g3-gate.md` (neu) und in `docs/releases/phase3.md` (neu) als bekannte Einschränkung (siehe [WP-1177](implementierungsplan.md#wp-1177--g3-gateprüfung-traceability-bqp2p3-blockierend-g3-stufe-der-gate-prüfung-checkliste-g3-gatemd-release-notes-phase-3), M3.20); G3 ist dadurch nicht blockiert. Nennt der Auftraggeber ein anderes ACS als GenieACS, braucht es ein zusätzliches Adapter-WP hinter dem Interface `Reader` (`collector/probe/tr069/acs.go`, neu mit WP-1018); dieses WP ist vor Beginn von M3.10 anzulegen. | Auftraggeber/Netzbetrieb | gesperrt: WP-1019 (bedingt); vorläufig: WP-1018 (Referenz GenieACS-NBI) | vor Beginn von M3.10 (WP-1018); spätestens vor Abschluss von M3.10 | erledigt (2026-11-09): Empfehlung gilt (Punkt 6). Ohne Nennung des USP-Controllers bis zum Abschluss von M3.10 entfällt WP-1019, WP-1018 schließt PRT-15 mit dokumentierter Einschränkung |
| A-64 | [E-189](#e-189), [E-190](#e-190), [E-194](#e-194) | **Fachvorgaben für Netzprotokoll- und Dienst-Probes.** RPKI: Validator im Kundennetz (Routinator, rpki-client/rtrtr, Fort, StayRTR), Port, RTR über SSH/TLS, Präfix/AS-Paare aus dem IPAM, eigener Validator oder öffentliche RTR-Server. DHCP: Test ohne Lease-Verbrauch (INFORM bzw. DISCOVER/OFFER, Relay-Modus) ausreichend, darf der Collector Port 67/68 binden (`CAP_NET_BIND_SERVICE`), ist DHCPv6 gefordert. PPPoE: aktiver Logintest (PADI/PADO oder vollständige Anmeldung mit Testzugang) oder Session-Monitoring am BNG. HTTP: GET und HEAD ausreichend oder Checks mit Request-Body (Spannung zu „nur lesend“). IPv6: Pflicht für alle Probetypen oder nur für ICMP, TCP, HTTP und DNS. | Auftraggeber/Netzbetrieb | vorläufig: WP-971, WP-1006, WP-1010, WP-1011, WP-1012, WP-1020, WP-1021, WP-1070 | vor Beginn von M3.7 (WP-971) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-65 | [E-172](#e-172), [E-174](#e-174), [E-175](#e-175), [E-176](#e-176), [E-177](#e-177), [E-178](#e-178), [E-180](#e-180), [E-182](#e-182) | **Parameter des Monitoring-Kerns bestätigen.** Down-Erkennung nach ca. 150 s (30 s × Down-Count 5) oder verkürztes Wiederholintervall nach dem ersten Fehlschlag (z. B. 10 s); Echo-Pakete je Prüfung (Vorschlag 3), Up-Count (Vorschlag 1), Schwellen für `degraded` (Paketverlust, RTT), IPv6-Ping; Bedeutung der Ebene „Gruppe“ in der Override-Hierarchie (Vorschlag: eigene Monitoring-Gruppe); Redundanz: eine Probe je Gruppe mit Übernahme oder Doppelprüfung aus zwei Collectoren; Netzdown-Schwelle ohne Lebenszeichen (Vorschlag 30 s) und Zuordnung des Root-Alarms (Standort, Kunde oder Collector-Gruppe); keine Benachrichtigung für nachgereichte, bereits beendete Ausfälle; Pufferdauer und Plattenbudget am Collector (24 h nach NFR-04 und ca. 2 GiB oder z. B. 72 h); NTP auf Collector-Hosts Pflicht, Toleranz (Vorschlag Warnung ab 2 s, Korrektur ab 60 s); automatische Ping-Probes für erkannte Geräte (Vorschlag Org-Einstellung, Standard aus); Rohdaten 14 Tage bei 5-min-Aggregaten 90 Tage und 1-h-/1-d-Aggregaten und Zustandshistorie 400 Tage, NFR-06 damit für Probe-Daten erfüllt. | Auftraggeber/Netzbetrieb | vorläufig: WP-935, WP-936, WP-937, WP-941, WP-946, WP-949, WP-953, WP-954, WP-962, WP-963, WP-964, WP-969, WP-972, WP-974 | vor Beginn von M3.6 (WP-935) | erledigt (2026-11-09): Empfehlung gilt (Punkt 4 und 6), Datenhaltung nach E-172 Option 1 |
| A-66 | [E-181](#e-181) | **Wertung von `unknown` in Verfügbarkeit und SLA (ALR-06).** Nach ALR-06 wird `unknown` (auch bei Collector-Ausfall) nach Org-Einstellung als verfügbar, nicht verfügbar oder ausgenommen gewertet: `alarm_settings.unknown_mode` ∈ `excluded` | `down` | `up` (WP-1030, ausgewertet in WP-1049). Welcher Standard gilt (Vorschlag `excluded`: aus der Messzeit herausgerechnet und getrennt ausgewiesen)? Gibt es vertragliche Vorgaben je Kunde? | Auftraggeber | vorläufig: WP-973, WP-1030, WP-1048, WP-1049, WP-1082 | vor Beginn von M3.8 (WP-973) | erledigt (2026-11-09): Meldung wie Netzausfall, im Protokoll getrennt ausgewiesen, Wertung nach den nachgelieferten Daten (Beschluss Punkt 3, siehe E-181) |
| A-67 | [E-163](#e-163), [E-198](#e-198), [E-205](#e-205) | **Korrelation, Eskalation und Tickets aus Monitoring.** Welche Korrelationswartezeit ist vertretbar (Vorschlag 30 s zusätzlich zur Erkennungszeit von ca. 150 s); was bedeutet „zeitnah“ in Sekunden; Standard-Eskalationsstufen und Ack-Fristen (z. B. 15 min bis Bereitschaft, 30 min bis Teamleitung); SMS/Pager schon in Epic E oder erst mit Epic F; Tickets aus Monitoring standardmäßig automatisch, für welche Schweregrade und Ursachenklassen; externes Ticketsystem (Jira, ServiceNow) vorgesehen. | Auftraggeber/Netzbetrieb | vorläufig: WP-1028, WP-1030, WP-1039, WP-1040, WP-1144, WP-1145 | vor Beginn von M3.11 (WP-1028) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-68 | [E-204](#e-204) | **Wartungsbenachrichtigung.** Nur Kunden sicher betroffener Anschlüsse oder auch heuristisch betroffene; Vorlaufzeiten (z. B. 72 h, 7 Tage, 24 h); Empfänger (Anschlussinhaber über ADMP, Client-Kontakte). | Auftraggeber | vorläufig: WP-1033, WP-1034, WP-1109 | vor Beginn von M3.11 (WP-1033) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-69 | [E-206](#e-206) | **Playbooks und Handlungsempfehlungen.** Gibt es Playbooks bzw. Störungsprozesse je Netzbetreiber (Meldewege, Formulare), die als System-Playbooks übernommen werden? Pflegen Kunden ihre Runbooks selbst über Workflow-Definitionen (WFL-07), oder liefert Reticora Standardempfehlungen je Prüftyp? | Auftraggeber | vorläufig: WP-1042, WP-1043, WP-1111 | vor Beginn von M3.11 (WP-1042) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-70 | [E-207](#e-207) | **Konfigurationsabruf und -änderung.** Gerätefamilien und Betriebssysteme für die Running-Config-Abfrage in Phase 3; Speicherung des maskierten Konfigurationstexts in der SaaS oder nur des Hashs; Standard-Severity einer Konfigurationsänderung außerhalb des Wartungsfensters (CMP-01) und ob sie zusätzlich alarmiert. | Auftraggeber/Netzbetrieb | vorläufig: WP-1052, WP-1053, WP-1054, WP-1056, WP-1124 | vor Beginn von M3.12 (WP-1052) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-71 | [E-208](#e-208) | **Kapazitätsprognose.** Standardschwelle und Prognosehorizont (z. B. 80 % Interface-Auslastung, 30 Tage); weitere Kapazitätsmetriken (PON-Port-Belegung, BNG-Sessions, IP-Pool-Auslastung aus dem IPAM). | Auftraggeber/Netzbetrieb | vorläufig: WP-1059, WP-1060, WP-1061 | vor Beginn von M3.12 (WP-1059) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-72 | [E-219](#e-219), [E-220](#e-220), [E-221](#e-221), [E-178](#e-178) | **Secret-Store und Tresor.** Meint „Die Zugangsdaten liegen im Secret-Store“ den internen verschlüsselten Zugangsdatenspeicher von Reticora oder einen externen Tresor, und welches Produkt (OpenBao, HashiCorp Vault, Azure Key Vault, AWS Secrets Manager); reichen OpenBao bzw. HashiCorp Vault KV v2 mit AppRole/Token, oder werden Cloud-Tresore schon in Phase 3 gebraucht (heute COL-03 [P4]); darf der Collector im Kundennetz direkt auf den Kundentresor zugreifen. Nicht mehr offen ist die Gerätegruppe: Sie ist nach PRB-01 und CRD-04 dieselbe statische Gruppe `monitoring_group` mit `monitoring_group_member` wie bei den Monitoring-Overrides (WP-935); [E-178](#e-178) ist daran angepasst, Tags sind keine Gerätegruppe. | Auftraggeber | vorläufig: WP-890, WP-891, WP-893, WP-894, WP-895, WP-896, WP-923 | vor Beginn von M3.3 (WP-890) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-73 | [E-217](#e-217), [E-218](#e-218) | **IPAM-Erweiterungen.** NAT in Phase 3 oder Phase 4; Übernahme aus phpIPAM per CSV-Import ausreichend oder zusätzlich über die phpIPAM-API; Subnetz-Scan legt neue Hosts automatisch als Adressen an (Vorschlag: je Subnetz, Standard aus) und Grenze für den Vollscan (Vorschlag /22 bei IPv4, IPv6 nur bekannte Adressen); BGP-Peerings als Soll-Inventar (Vorschlag) oder nur beobachtet. | Auftraggeber/Netzbetrieb | vorläufig: WP-920, WP-1062, WP-1063, WP-1066, WP-1068, WP-1069, WP-1071 | vor Beginn von M3.4 (WP-920) für den Import, sonst vor Beginn von M3.13 (WP-1062) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-74 | [E-234](#e-234), [E-235](#e-235), [E-236](#e-236) | **CVE-Quelle und Findings.** Kombination CSAF (BSI und Hersteller), NVD und CISA KEV bestätigen; vorrangige Hersteller für Versionsvergleich und Produktzuordnung; Schwachstellen-Bundles im Air-Gapped-Profil im Release-Rhythmus oder häufiger (z. B. wöchentlich); Findings ab einer Severity automatisch als Ticket (Vorschlag Standard aus) und Schwelle je Mandant oder je Client. | Auftraggeber | vorläufig: WP-1117, WP-1118, WP-1119, WP-1120, WP-1121, WP-1122, WP-1123 | vor Beginn von M3.17 (WP-1117) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-75 | [E-238](#e-238) | **Compliance-Vorlagen und Kontrollkatalog.** Mitgelieferte Regelvorlagen (z. B. Firmware nicht EOL, SNMPv3 statt v2c, Management-IP gesetzt, keine offenen kritischen Findings); Kontrollzuordnung zu ISO 27001 Annex A, NIS2 Art. 21 und § 8a BSIG; weitere Kataloge (B3S Telekommunikation, BSI IT-Grundschutz); nur eigene Kurztitel oder Lizenz für Normtexte (ISO 27001). | Auftraggeber/Compliance | vorläufig: WP-1128, WP-1129, WP-1130 | vor Beginn von M3.17 (WP-1128) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-76 | [E-230](#e-230), [E-240](#e-240) | **Workflow-Vorlagen und Genehmigungen.** Mitgelieferte Automationsvorlagen nach WFL-03 (Vorschlag: CI ohne Owner → Ticket; Garantie/Vertrag läuft ab → Benachrichtigung; CI decommissioned mit aktiven Beziehungen → Ticket; neues CI aus Discovery ohne Client → Review; kritisches Finding → Ticket); Schwellen und Genehmigerkreise der Bestellgenehmigung (z. B. ab 1.000 EUR zwei Genehmiger) und Fortbestand der direkten Genehmigungsroute; Unterschrift bei Übergaben auf dem Bildschirm oder fortgeschrittene/qualifizierte elektronische Signatur. Ohne Freigabe der Vorlagen entsteht nach [E-02](../entscheidungen.md#e-02) nur der Mechanismus. | Auftraggeber | vorläufig: WP-1098, WP-1099, WP-1106, WP-1107, WP-1108 | vor Beginn von M3.16 (WP-1098) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-77 | [E-241](#e-241) | **Aufbewahrung der Workflow-Runs.** 400 Tage wie die Verfügbarkeitshistorie oder eine andere Frist (z. B. 10 Jahre für Genehmigungsnachweise). | Auftraggeber/Datenschutz | vorläufig: WP-1092, WP-1093 | vor Beginn von M3.16 (WP-1092) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-78 | [E-163](#e-163) | **SMS- und Pager-Provider.** Bringt jeder Mandant seine Provider-Konten selbst ein, oder stellt der Betreiber sie zentral (Kosten, AVV mit Twilio, Sipgate, PagerDuty, Opsgenie, ilert); Sandbox-Zugänge für Tests; Quittung per SMS-Antwort gewünscht. | Auftraggeber/Betreiber | vorläufig: WP-1141, WP-1142, WP-1143, WP-1146, WP-1172 | vor Beginn von M3.18 (WP-1141) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-79 | [E-157](#e-157), [E-158](#e-158) | **Trial-Ende und Operator-Oberfläche.** Automatischer Wechsel auf Essential nach ENT-07 am Trial-Ende, abweichend von [E-44](../phase2/entscheidungen.md#e-44); grafische Operator-Verwaltung gewünscht; Billing-Anbindung (`source billing`) in Phase 3; Upgrade-Anfrage des Mandanten aus der Oberfläche. | Auftraggeber/Vertrieb | vorläufig: WP-884, WP-886, WP-887, WP-888 | vor Beginn von M3.2 (WP-884) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-80 | [E-165](#e-165), [E-166](#e-166) | **DSGVO-Workflows.** Darf das `audit_log` eines Mandanten nach der Karenz gelöscht werden, wenn vorher ein Vollexport angeboten wird; wer erhält das Löschprotokoll, wenn der Antragsteller das Unternehmen verlassen hat; enthält der Vollexport personenbezogene Daten im Klartext. | Auftraggeber/Datenschutz | vorläufig: WP-1149, WP-1150, WP-1151, WP-1152, WP-1153, WP-1154, WP-1155, WP-1156, WP-1157 | vor Beginn von M3.19 (WP-1149) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-81 | [E-148](#e-148), [E-167](#e-167) | **Startfreigabe und Folgebefunde.** Darf die Werkzeugkette M3.0 vor dem GO von WP-331 (G2) beginnen (Empfehlung: ja, Fach-Meilensteine erst nach dem GO); sollen SR-58 bis SR-63, der Rest von SR-03 und SR-10 vollständig in Phase 3 vor G3 behoben werden. | Auftraggeber | vorläufig: WP-860 bis WP-870 (Start vor dem GO von WP-331), WP-871 bis WP-879 | vor Beginn von M3.0 (WP-860); Folgebefunde vor Beginn von M3.1 (WP-871) | erledigt (2026-11-09): Phase 2 wird abgeschlossen; M3.0 startet sofort, Fach-Meilensteine nach dem GO von WP-331 (E-148 Option 2) |
| A-82 | [E-169](#e-169) | **GA-Termin und externer Pentest.** Liegt GA vor oder nach G3? Davon hängt ab, ob ein externer Pentest vor G3 nötig ist. | Auftraggeber | vorläufig: WP-1174, WP-1175, WP-1177 | vor Beginn von M3.20 (WP-1174) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-83 | [E-169](#e-169) | **Abnahmeszenario ABN-04 bestätigen.** Referenztopologie, erwartete Handlungsempfehlungen und Akzeptanzschwellen (z. B. Zeit bis zum Root-Cause-Alarm). | Auftraggeber/Netzbetrieb | vorläufig: WP-1161, WP-1176, WP-1177 | vor Beginn von M3.20 (WP-1161) | erledigt (2026-11-09): Empfehlung der genannten Entscheidungen gilt (Beschluss des Auftraggebers vom 2026-11-09, Punkt 6) |
| A-84 | [E-169](#e-169), [E-223](#e-223) | **G3 ohne ADMP-API-Dokumentation.** Ist G3 bestanden, wenn A-55 bis zum Gate nicht erledigt ist (ADMP-Endpunkte WP-1073–WP-1078 als bekannte Einschränkung nach GATE-02 bzw. Eintrag in `docs/acceptance/g3-ausnahmen.csv` (neu, analog `docs/acceptance/g1-ausnahmen.csv`)), und darf WP-1159 SEC-05 dann ohne WP-1076 schließen? Bis zur Antwort gilt: G3 ist nicht blockiert, die ADMP-Endpunkte stehen als bekannte Einschränkung, WP-1159 schließt SEC-05 und WP-1076 bleibt Teil. | Auftraggeber | vorläufig: WP-1159, WP-1166, WP-1176, WP-1177 | vor Beginn von M3.19 (WP-1159) | erledigt (2026-11-09): Empfehlung gilt (Punkt 5 und 6); G3 kann ohne ADMP-Endpunkte bestehen, M3.14 folgt nach Lieferung von A-55 |
| A-85 | [E-246](#e-246) | **Risikoakzeptanz für Critical-Befunde in Fremdimages ohne Hersteller-Fix.** Der Scan aller Fremdimages (WP-873, SR-60) findet Critical-Befunde in `pgsty/minio` (amqp091-go, 3×), `opensearchproject/opensearch` (Bouncy Castle, 2×) und `postgres:16.15-alpine` von keycloak-db (Go-Standardbibliothek in `gosu`, 1×; eine Umstellung auf das eigene Postgres-Image braucht Install- und Upgrade-Tests mit lauffähiger CI, Nachtrag 2026-11-10), für die es am 2026-11-10 kein neueres Image gibt. Ihre Ausnahmen in `security/scan-exceptions-thirdparty.txt` enden nach SEC-11 am 2026-11-17 und lassen sich nicht verlängern. Darf der Betrieb diese Images über die SLA hinaus mit den kompensierenden Maßnahmen (MinIO nur über den Lese-Proxy, OpenSearch optional und nur intern) weiterführen, bis der Hersteller liefert, oder sollen sie ersetzt werden (externer S3-Speicher, PostgreSQL-Suche als einziges Such-Backend)? Bis zur Antwort gilt die Empfehlung von E-246. **Nachtrag 2026-11-13 (Update-Plan Upd-01, keycloak-db):** Für keycloak-db durch Ersatz erledigt, keine Risikoakzeptanz mehr nötig: Der Dienst läuft auf dem eigenen Image `reticora-keycloak-db` (`deploy/docker-compose/keycloak-db`, Muster E-96: `postgres:16.16-alpine` per Digest, `gosu` 1.20 mit Go 1.26.11 neu gebaut), signiert in `release.yml` und gescannt in `image-scan`; Trivy 0.75.0 meldet 0 Critical/High (Hersteller-Image 1 Critical, 25 High). Der verlangte Upgrade-Nachweis liegt vor: Volumes von `postgres:16.15-alpine` und `16.16-alpine` laufen auf dem neuen Image mit Daten und Passwort weiter (`tests/keycloak-db-image.test.sh`, install-smoke Job `reinstall`). Die 26 keycloak-db-IDs sind aus `security/scan-exceptions-thirdparty.txt` entfernt. Offen bleibt A-85 für MinIO und OpenSearch. | Auftraggeber | vorläufig: WP-873 (Ausnahmen), Folge-WP nach E-246 | vor 2026-11-17 | offen (keycloak-db erledigt) |
| A-85 | [E-246](#e-246) | **Risikoakzeptanz für Critical-Befunde in Fremdimages ohne Hersteller-Fix.** Der Scan aller Fremdimages (WP-873, SR-60) findet Critical-Befunde in `pgsty/minio` (amqp091-go, 3×), `opensearchproject/opensearch` (Bouncy Castle, 2×) und `postgres:16.15-alpine` von keycloak-db (Go-Standardbibliothek in `gosu`, 1×; eine Umstellung auf das eigene Postgres-Image braucht Install- und Upgrade-Tests mit lauffähiger CI, Nachtrag 2026-11-10), für die es am 2026-11-10 kein neueres Image gibt. Ihre Ausnahmen in `security/scan-exceptions-thirdparty.txt` enden nach SEC-11 am 2026-11-17 und lassen sich nicht verlängern. Darf der Betrieb diese Images über die SLA hinaus mit den kompensierenden Maßnahmen (MinIO nur über den Lese-Proxy, OpenSearch optional und nur intern) weiterführen, bis der Hersteller liefert, oder sollen sie ersetzt werden (externer S3-Speicher, PostgreSQL-Suche als einziges Such-Backend)? Bis zur Antwort gilt die Empfehlung von E-246. Nachtrag 2026-11-13 (Update-Plan Upd-02): Für OpenSearch erledigt durch 3.9.0 ohne Critical-Befunde ([E-246](#e-246)); offen bleiben MinIO und keycloak-db. | Auftraggeber | vorläufig: WP-873 (Ausnahmen), Folge-WP nach E-246 | vor 2026-11-17 | offen |
| A-86 | [E-248](#e-248), [E-250](#e-250), [E-251](#e-251), [E-252](#e-252), [E-253](#e-253), [E-254](#e-254), [E-255](#e-255) | **Offene Fachfragen zur ADMP-API-Doku 9.14.0.** (1) Antwortschema von `GET /V1/infrastructure/accessnodepicker` (Annahme: `AccessNode[]` mit `modules[].ports[]`; enthält es `password`, `iv`, `username`?) und von `GET /V1/infrastructure/port/{portId}/dienstIds` (Annahme: Array von Zahlen; Antwort bei unbekanntem Port). (2) Port-Benennung: Entspricht `AccessNode.name` dem Hostnamen des Geräts, und wie heißen Interfaces bei `KEYMILE_SSH` und `ZYXEL_SNMP` aus `unitIndex` und `portIndex` (Vorschlag: Name endet auf `<unitIndex>/<portIndex>`, ohne Versatz)? (3) Liefert `GET /V1/anschluss/{id}` die Relationen `netzbetreiber` und `inhaber` mit, und liefert `GET /V1/dienst/{id}` `template`? Wenn nicht: Aufbau von `filters` und Element-Schema der Paginatoren (die Doku nennt `ChangeLog`). (4) Einheit der Zeitwerte (`created`, `removed`, `gebucht` … `abgeschaltet`; Vorschlag: Erkennung Sekunden/Millisekunden nach Betrag) und Bedeutung von `removed`. (5) Schlüsselkatalog der Diensteigenschaften; reichen die öffentlichen (`/properties/public`) für Bandbreite, Technologie und Status? (6) Token-Laufzeit, Cookie-Attribute und Status bei falschen Zugangsdaten bzw. abgelaufenem Cookie (Vorschlag: 401); Ratenlimits je Konto. (7) Technisches ADMP-Konto nur mit GET-Rechten (prüfbar über `AuthAuthorization.method`) und Basis-URL der Instanz (die Doku hat keinen `servers`-Eintrag). Bis zur Antwort gelten die Empfehlungen der genannten Entscheidungen; der Smoke-Test von WP-1077 liefert Teilantworten zu (1), (3), (4) und (6). | Auftraggeber/ADMP-Betreiber | vorläufig: WP-1073, WP-1074, WP-1075, WP-1076, WP-1077 | (1), (2) und (7) vor dem Merge von WP-1073; (3) bis (6) vor dem Merge von WP-1077 | offen |
<a id="e-148"></a>
## E-148 – Startfreigabe der Phase 3 gegenüber Gate G2

**Schlüssel:** `d-p3-start`

**Status:** entschieden (2026-11-09, Option 2; Beschluss Punkt 1: Phase 2 wird gerade abgeschlossen)
**Kontext:** G2 ist nicht abgeschlossen. WP-327 (`g2-perf`, Lastnachweis) und WP-331 (`g2-gate`) sind auf main `6d019370` nicht gemergt. `docs/acceptance/lasttest-g2.md` ist ein Gerüst: „Stand 2026-11-09: Gerüst, Nachweis ausstehend […] Die Akzeptanzkriterien von WP-327 (`g2-perf`) sind damit nicht erfüllt“ (`docs/acceptance/lasttest-g2.md:9–13`). Es gibt nur die Vorarbeit `1bebad94` (Auswertung um die Phase-2-Jobs erweitert). Phase 2 durfte erst nach dem GO von WP-600 (`k-gate`) beginnen (Konvention 2, `docs/plan/phase2/implementierungsplan.md:203`). Der Lastnachweis nach E-58 verlangt den Median dreier Läufe desselben Server-Commits ([E-58](../entscheidungen.md#e-58)). Jeder Merge in Produktivcode während dieser Läufe ändert den Release-Kandidaten G2 und kann archivierte Läufe entwerten.

**Frage:** Ab wann dürfen WPs der Phase 3 gemergt werden, solange WP-327 und WP-331 offen sind?

**Optionen:**

1. Phase 3 beginnt vollständig erst nach dem GO von WP-331. Folgen: kein Risiko für die G2-Nachweise; die Werkzeugkette (M3.0) liegt bis zum GO still und verschiebt alle Fach-Meilensteine um die Dauer der Gate-Läufe.
2. M3.0 startet sofort: Traceability, Register und Gerüstpfade mit `x-status: planned`, alles ohne Release-Wirkung. M3.0b (M3.1 Folgearbeiten), die Fach-Meilensteine und alles mit Wirkung auf den Release-Standard G2 beginnen erst nach dem GO von WP-331. Folgen: Vorlauf der Werkzeugkette ohne Wirkung auf G2; jedes M3.0-WP muss zeigen, dass der Release-Standard unverändert bleibt.
3. Alle Meilensteine starten sofort; G2-Nachweise werden bei Bedarf wiederholt. Folgen: schnellster Start; jeder Fach-Merge kann WP-327 und WP-331 zu neuen Läufen zwingen, und die Commit-Bindung des Medians nach E-58 geht verloren.

**Empfehlung:** Option 2. Die Werkzeugkette ändert weder den Release-Standard noch das Verhalten, weil P3-Module bis WP-1166 (`p3-release`) abgeschaltet bleiben und Gerüstoperationen nicht registriert werden. Sie verkürzt den Vorlauf. Fach-WPs würden den Release-Kandidaten G2 verändern und die Läufe von WP-327 und WP-331 entwerten. Prüfbar wird das über den Paritätstest der Routen: Ohne `RETICORA_MODULES` registriert der Server nach jedem M3.0-WP dieselben Routen wie vorher.

**Auswirkung:** M3.0 nennt diese Regel in der Meilensteineinleitung. Kein WP außerhalb von M3.0 hat eine Voraussetzung vor dem GO von WP-331. WP-1177 prüft im Gate, dass kein Fach-WP vor dem GO-Commit von WP-331 gemergt wurde.

**Quelle:** docs/acceptance/lasttest-g2.md:9–13; docs/plan/phase2/implementierungsplan.md:203 (Konvention 2); [E-58](../entscheidungen.md#e-58); Commit `1bebad94`

**Betroffene IDs:** GATE-02, GATE-06, SEQ-01

**Betroffene WPs:** WP-1177 (`p3-gate`)

<a id="e-149"></a>
## E-149 – Planstruktur, Nummernkreise und Traceability-Epics der Phase 3

**Schlüssel:** `d-p3-plan-structure`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Es gibt drei Pläne: Phase 1 (`docs/plan/implementierungsplan.md`), Korrekturplan (`docs/plan/g1-korrektur/implementierungsplan.md`) und Phase 2 (`docs/plan/phase2/implementierungsplan.md`). WP-Nummern sind über alle Pläne eindeutig. `MergePlans` lehnt doppelte Nummern ab (`backend/internal/traceability/traceability.go:440`), `ParsePlan` liest je Plan die Blöcke (:481). Die nächsten freien Nummern sind E-148 und A-55 (Kopf von `docs/plan/phase2/entscheidungen.md`). Die Traceability verlangt je WP ein „Epic (Traceability)“. Der Phase-2-Plan nutzt A, B, C und G2. SEQ-01 legt für Phase 3 fest: „Epic E (Monitoring) → Workflows → Epic F (Compliance, DSGVO-Workflows, SMS/Pager) → G3“ (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:79`). Die WP-Nummern WP-860 bis WP-1177 und die Meilensteinzuordnung sind zentral vergeben.

**Frage:** Wo steht der Plan der Phase 3, welche Nummernkreise nutzt er, und welche Epics tragen seine WPs?

**Optionen:**

1. Eigener Ordner `docs/plan/phase3/` mit `implementierungsplan.md`, `entscheidungen.md` und `abdeckung.csv` (mit Spalte `abschluss_wp`). Entscheidungen ab E-148, Aufgaben ab A-55. Epics nach SEQ-01 und Meilenstein: M3.6 bis M3.15 → E (Monitoring einschließlich Scan, ASN, ADMP-Endpunkte und Lastnachweis Epic E); M3.16 bis M3.19 → F (Workflows, Findings und Compliance, SMS/Pager, DSGVO-Workflows); alle übrigen (M3.0 bis M3.5 und M3.20: Werkzeugkette, Folgearbeiten, Pakete, Zugangsdaten, IPAM-Kern, ADMP-Gerüst, Qualität und Gate) → G3. Folgen: `ParsePlan` und `MergePlans` bleiben einheitlich; WP-860 erweitert `PlanFiles` und `CoverageFiles` um die dritte Phase.
2. Wie 1, aber ein einziges Epic P3 für alle WPs. Folgen: einfacher; die Reihenfolge nach SEQ-01 ist im Gate nicht mehr an den Epics nachweisbar.
3. Fortschreibung im Phase-2-Plan. Folgen: kein Codeumbau in der Traceability; der Phase-2-Plan wäre nach G2 nicht mehr eingefroren, und die Phasenprüfung (`CheckPhase2Consistency`) mischte zwei Gates.

**Empfehlung:** Option 1. Gleiches Muster wie Phase 2. Die Epics folgen SEQ-01, damit WP-1177 die Reihenfolge E vor F im Gate nachweisen kann. Workflows bilden kein eigenes Epic: SEQ-01 nennt sie ohne Epic-Buchstaben zwischen E und F. Sie stehen in M3.16 und tragen F, weil sie nach Epic E liegen und die DSGVO-Workflows in F auf ihnen aufbauen.

**Auswirkung:** WP-860 liest vier Pläne und drei Abdeckungen und akzeptiert vierstellige Nummern ab WP-1000 ([E-244](entscheidungen.md#e-244)). WP-861 passt PR-Vorlage, README und CI-Kommentar an. Die Kopfzeile jedes Blocks nennt Schlüssel, Meilenstein, Epic (Traceability), Aufwand und „G3-Umfang“.

**Quelle:** backend/internal/traceability/traceability.go:440 (`MergePlans`), :481 (`ParsePlan`); docs/plan/phase2/entscheidungen.md (Kopf, nächste freie Nummern); docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:75–79 (SEQ-01)

**Betroffene IDs:** DOD-01, GLO-11, SEQ-01

**Betroffene WPs:** WP-860 (`p3-trace`), WP-861 (`p3-trace-docs`)

<a id="e-150"></a>
## E-150 – Aufnahme der neuen Auftraggeber-Anforderungen vom 2026-11-09 in den Katalog

**Schlüssel:** `d-p3-catalog-ids`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** GLO-11 verlangt: „neue Anforderungen erhalten ID, Tag und Epic vor der Umsetzung“ (`docs/spec/katalog-v3/00-grundlagen.md:101`). Die Traceability liest den Katalog per Glob über `docs/spec/katalog-v3/*.md` (`backend/internal/traceability/traceability_test.go:831`). Befund- und Planverweise zitieren Katalogzeilen als Pfad:Zeile, so etwa die Phase-2-Abdeckung und `docs/audit/befunde.csv`. Der Auftraggeber hat am 2026-11-09 neue Anforderungen geliefert: Pakete (PKG), Probes (PRB), Probetypen (PRT), Alarmierung (ALR), IPAM (IPM), Zugangsdaten (CRD), ADMP (ADM), Compliance (CMP), Workflows (WFL-05 bis WFL-08), NFR-11, GATE-06 und ABN-04. Drei Inhalte sind zusammengelegt und haben keine eigene ID: „Die Probe-Typgrenzen sind Bestandteil von PRT-01, die Auslegung der Prüflast ist Bestandteil von NFR-11, die Eskalation ist Bestandteil der bestehenden NTF-02 (siehe Hinweis unter Alarmierung). Diese Inhalte haben keine eigenen IDs.“ (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:7`, neu).

**Frage:** Wie kommen die neuen IDs in den Katalog, ohne bestehende Verweise zu brechen?

**Optionen:**

1. Neue Katalogdatei `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu) mit allen neuen IDs, Tag, Text und Herkunft („Auftraggeber 2026-11-09“). Die zusammengelegten Inhalte (Typgrenzen, Prüflast, Eskalation) erhalten keine eigene ID; die Vorbemerkung (:7) und der Hinweis zu NTF-02 (:125) verweisen auf PRT-01, NFR-11 bzw. NTF-02. Eingebracht wird die Datei mit dem Planungs-PR. Folgen: Bestehende Pfad:Zeile-Verweise bleiben gültig, die Prüfung erkennt die IDs ohne Codeänderung.
2. Einfügen in die bestehenden Katalogdateien beim jeweiligen Thema. Folgen: thematisch geschlossen; alle Pfad:Zeile-Verweise hinter den Einfügestellen verschieben sich, und die Herkunft der Zeilen ist im Katalog nicht mehr datiert.
3. IDs nur im Plan und in der Abdeckung, nicht im Katalog. Folgen: verletzt GLO-11; `Validate` scheitert an IDs, die im Katalog fehlen.

**Empfehlung:** Option 1. Bestehende Verweise bleiben gültig, die Herkunft ist datiert, und die Prüfung erkennt die IDs ohne Codeänderung. Die zusammengelegten Inhalte werden nicht als eigene Anforderung geführt, damit keine ID ohne schließendes WP in der Abdeckung steht.

**Auswirkung:** WP-860 verlangt keine Änderung am Glob. Die Phase-3-Abdeckung führt 164 IDs, davon 97 neue [P3]-IDs und 16 [Q]-IDs; zusammengelegte Inhalte haben keine Zeile. Die Test-, Last-, Gate- und Abnahmepflichten PRT-21, NFR-11, GATE-06 und ABN-04 tragen [P3], nicht [Q] (:9). Plan-WPs zitieren die neuen IDs wörtlich im Format (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:<Zeile>`, neu), bis die Datei gemergt ist.

**Quelle:** docs/spec/katalog-v3/00-grundlagen.md:101 (GLO-11); backend/internal/traceability/traceability_test.go:831–834; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:7`, :9, :125 (neu); Lieferung des Auftraggebers vom 2026-11-09

**Betroffene IDs:** GLO-11, NFR-11, GATE-06, ABN-04, NTF-02, PRT-01, PKG-01 bis PKG-09, PRB-01, PRB-03 bis PRB-07, PRB-09 bis PRB-11, PRT-02 bis PRT-22, ALR-01 bis ALR-12, IPM-01 bis IPM-20, CRD-01 bis CRD-07, ADM-01 bis ADM-09, CMP-01, CMP-02, WFL-05 bis WFL-08

**Betroffene WPs:** WP-860 (`p3-trace`)

<a id="e-151"></a>
## E-151 – Paketzuordnung der P3-Funktionen (PKG-08, ENT-06)

**Schlüssel:** `d-p3-package-matrix` (eingearbeitet: `d-ipam-entitlement`)

**Status:** entschieden (2026-11-09, Option 2 mit Präzisierung nach Beschluss Punkt 2: `monitoring` ab Standard nur mit den Kernfunktionen)
**Kontext:** ENT-06 (V, mit E-34 bestätigt) ordnet „monitoring (Polling/Alerting)“ nur Enterprise zu, „notifications_sms_pager, workflows, compliance_findings“ ab Pro (`docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:105`, :107). Der Code führt `monitoring` bereits ab Pro (`backend/internal/entitlement/entitlement.go:172`, `planFeatures` :161–175). Add-ons sind nach CH14 kein Paketbestandteil (`docs/spec/katalog-v3/00-grundlagen.md:27`). Der Auftraggeber verlangt Ping-Monitoring als Standardfunktion, 16 Abfragetypen (Anforderung 9; im Register WP-934 23 Typschlüssel), IPAM/ASN, ADMP, Tresoranbindung, SLA und Prognosen. NET-05 (VRFs, VLANs, Subnetze, IPs) ist [B] und gehört zu `cmdb_core` (`docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:64`). Subnetz-Scan, PTR-Abgleich und ASN/BGP/RPKI erzeugen Prüflast über die Probe-Engine.

**Frage:** Welche Feature-Schlüssel tragen die P3-Funktionen, und welchem Paket gehören sie an?

**Optionen:**

1. ENT-06 wörtlich: ein Schlüssel `monitoring` nur in Enterprise für alle Probetypen und Analysen; IPAM, Tresor und ADMP ohne eigene Schlüssel im Kern bzw. in `monitoring`. Folgen: Ping-Monitoring ist keine Standardfunktion (widerspricht der Anforderung); ADMP wäre in jedem Enterprise-Paket ohne Vertrag mit dem Datenlieferanten enthalten; der Code müsste `monitoring` aus Pro entfernen.
2. Staffel:
   - `monitoring` ab Standard (Kernfunktionen): ICMP, TCP Connect, HTTP/HTTPS, DNS mit den typeigenen Grenzen für Laufzeit und Verlust; Zustandswechsel mit Benachrichtigung, Abhängigkeiten und Root-Cause-Korrelation (Störung mit Wurzel und betroffenen CIs), Flapping, Wartungsfenster, Verfügbarkeitshistorie 400 Tage.
   - `ipam` ab Standard: phpIPAM-Funktionen (Sections, Hierarchie, Freiraum, Verlauf, Import/Export), Subnetz-Scan, ASN, BGP-Peerings, NAT, Gerät ↔ IP. NET-05 bleibt `cmdb_core`.
   - `monitoring_network` ab Pro: SNMP-Status und -Interfaces, BGP/OSPF/IS-IS, BFD, Optik, DHCP-Test, TWAMP/STAMP, gNMI/OpenConfig, RPKI, Konfigurationsänderungserkennung, Redfish; dazu regelbasiertes Threshold Monitoring auf Metriken (ALR-05) und die Handlungsempfehlung je Störung (ALR-09: betroffene Anschlüsse, vorhandene Tickets, funktionierende Redundanz, nächste Schritte).
   - `credential_vault`, `notifications_sms_pager`, `workflows`, `compliance_findings` ab Pro.
   - `monitoring_access` (PPPoE/BNG, PON/OLT/ONU, TR-069/TR-369 USP), `monitoring_analytics` (SLA-Monitoring, Kapazitätsprognosen) und `compliance_automation` nur Enterprise.
   - `admp` als Add-on in jedem Paket (CH14).

   Folgen: zehn neue Schlüssel; Abweichung von ENT-06 (`monitoring` Enterprise → Standard) braucht die Bestätigung des Auftraggebers; Probetypen und Teilfunktionen brauchen eine Zuordnung zum Schlüssel (WP-865).
3. Monitoring vollständig als Add-on mit Kontingentstufen, Pakete unverändert. Folgen: Pakete bleiben wie ENT-06; die Root-Cause-Korrelation und das Grundmonitoring sind ohne Zukauf nicht verfügbar; jedes Add-on braucht eine eigene Kontingentlogik.
4. Aus `d-ipam-entitlement`: Sections, Hierarchie, Freiraum, Verlauf, Import/Export und UI in `cmdb_core`; Scan, PTR, ASN/BGP/RPKI und NAT unter einem eigenen Schlüssel `ipam_advanced`. Folgen: phpIPAM-Grundniveau in jedem Paket, auch in Essential ohne Monitoring; ein zusätzlicher Schlüssel, der nur in Paketen mit `monitoring` sinnvoll ist.

**Empfehlung:** Option 2. Sie bildet die Auftraggeber-Anforderungen als verkaufbare Stufen ab. Die Root-Cause-Unterdrückung (Anforderung 6) liegt in jedem Monitoring-Paket. ADMP ist eine externe, vertragsgebundene Anbindung und wird wie IGA und KI als Add-on geführt. Gegen Option 4: `ipam` liegt ab Standard und damit in jedem Paket, das auch `monitoring` hat. Ein eigener Schlüssel `ipam_advanced` würde nur Essential trennen, und Essential hat keine Collector-Prüflast für Scans. Die Prüflast der Scans zählt nach E-153 gegen die Monitoring-Kontingente und ist dort begrenzt. Die Abweichung von ENT-06 (`monitoring` Enterprise → Standard, `monitoring` aus Pro herausgelöst in drei Schlüssel) hat der Auftraggeber am 2026-11-09 bestätigt (Aufgabe A-58 erledigt), mit der Präzisierung unten.

**Beschluss vom 2026-11-09 (Punkt 2):** „Monitoring soll ab Standard mit nur essentiellen Funktionen verfügbar sein.“ Damit gilt Option 2 mit der oben genannten Präzisierung. `monitoring` umfasst in Standard nur die Kernfunktionen: Erreichbarkeit, Zustandswechsel, Benachrichtigung, Root-Cause-Unterdrückung von Folgealarmen, Flapping, Wartungsfenster und Verfügbarkeitshistorie. Regelbasierte Schwellwerte (ALR-05) und die Handlungsempfehlung (ALR-09) liegen in `monitoring_network` ab Pro. In Standard zeigt eine Störung Wurzel, Ursache und betroffene CIs, aber kein Empfehlungsobjekt. Die Oberfläche weist auf das Paket hin (PKG-06). Die Abweichung von ENT-06 ist damit vom Auftraggeber bestätigt (A-58 erledigt).

**Auswirkung:** WP-862 legt die Schlüssel und die Paketmatrix an (Datenmigration). WP-865 ordnet jedem Präfix, jeder Suffixregel, jedem der 23 Probetypen und jeder Teilfunktion genau einen Schlüssel zu; Schwellwertregeln und Handlungsempfehlung gehören zu `monitoring_network`. WP-985 wertet Schwellwertregeln nur mit `monitoring_network` aus, WP-1043 liefert die Empfehlung nur mit `monitoring_network`. Der Feature-Key heißt `ipam`; `ipam_advanced` aus Option 4 gibt es nicht, auch WP-870 nutzt `ipam`. WP-883 bucht `admp` als Add-on. WP-886, WP-915 und WP-1062 zeigen gesperrte Teile mit Verweis auf das Paket. WP-1160 legt je Paket eine Seed-Organisation an.

**Quelle:** docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:97–110 (ENT-06); docs/spec/katalog-v3/00-grundlagen.md:27 (CH14); docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:64 (NET-05); backend/internal/entitlement/entitlement.go:161–175 (`planFeatures`); `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:29` (neu, PKG-08); [E-34](../entscheidungen.md#e-34)

**Betroffene IDs:** CH14, ENT-05, ENT-06, NET-05, PKG-03, PKG-06, PKG-08, PRT-01, IPM-11, ADM-01

**Betroffene WPs:** WP-862 (`p3-entitlements`), WP-865 (`p3-feature-gates`), WP-870 (`p3-net-contract`), WP-883 (`p3-pkg-addons`), WP-886 (`p3-pkg-tenant-ui`), WP-915 (`p3-ipam-ui-tree`), WP-1062 (`p3-ipam-scan-config`), WP-1160 (`p3-seed-packages`)

<a id="e-152"></a>
## E-152 – ENT-06-Namen für bestehende P3-Schlüssel

**Schlüssel:** `d-p3-key-names`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Der Code nutzt `workflow_forms` und `compliance` (`backend/internal/entitlement/entitlement.go:91–92`), ENT-06 nennt `workflows` und `compliance_findings` (`docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:105`). Phase 2 hat Altschlüssel per Datenmigration umbenannt; die Altschlüssel antworten danach mit 422 (`backend/migrations/000190_entitlement_p2_keys.up.sql:32–49`, WP-244). Beide Schlüssel gaten heute nur Module mit Phase P3, die im Release-Standard G2 abgeschaltet sind (`backend/internal/server/modules.go:83–87`).

**Frage:** Werden die Code-Schlüssel auf die Katalognamen umbenannt?

**Optionen:**

1. Umbenennung mit Datenmigration (`workflow_forms` → `workflows`, `compliance` → `compliance_findings`) nach dem Muster 000190; Altschlüssel danach 422 wie `inventory` und `ticketing`. Folgen: eine Datenmigration auf `entitlement`; bestehende Zeilen behalten `enabled`, `limits`, `valid_until` und `source`.
2. Code-Namen beibehalten, ENT-06-Namen als Alias in der Dokumentation. Folgen: kein Migrationsaufwand; Katalog und Code sprechen dauerhaft verschiedene Namen, und Operator-API und Abrechnung müssten übersetzen.

**Empfehlung:** Option 1. Eine Quelle der Wahrheit (Katalog), Muster aus WP-244. Vor G3 sind die Schlüssel nur für abgeschaltete Module vergeben; die Wirkung auf Bestandsorganisationen ist gering.

**Auswirkung:** WP-862 enthält die Migration `backend/migrations/<nächste Nr.>_entitlement_p3_keys.up.sql` und `.down.sql` mit `docs/schema-baseline.md`. `Validate` lehnt die Altschlüssel ab.

**Quelle:** backend/internal/entitlement/entitlement.go:86–96; backend/migrations/000190_entitlement_p2_keys.up.sql; backend/internal/server/modules.go:82–90; docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:105

**Betroffene IDs:** ENT-06, PKG-08

**Betroffene WPs:** WP-862 (`p3-entitlements`)

<a id="e-153"></a>
## E-153 – Standardlimits je Paket und Werte der Monitoring-Kontingente

**Schlüssel:** `d-p3-quota-defaults`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2, mit `min_probe_interval_s` Standard 30 s)
**Kontext:** Nach E-34 wirken Quoten nur, wenn sie gespeichert sind; nur `max_storage_mb` hat nach E-44 eine Staffel (`backend/internal/entitlement/entitlement.go:132–139`, `storageDefaultMB`; `backend/internal/signup/service.go`, `planStorageMB`). `Quotas` ist eine flache Liste ohne Monitoring-Kontingente (`entitlement.go:142`). ENT-06 nennt als Default (V) `max_cis` 500 / 2.500 / 10.000 / 50.000, `max_collectors` 2 / 5 / 20 / unbegrenzt und `max_users` 5 / 25 / 100 / unbegrenzt (`docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:110`). PKG-04 verlangt Standardwerte je Paket für alle Quoten und die Monitoring-Kontingente `max_monitored_targets`, `max_probes_per_minute` und `min_probe_interval_s`. Die Gesamtlast nach NFR-11 beträgt 353 Prüfungen/s = 21.180/min. Der ICMP-Standard des Auftraggebers ist 30 s Intervall.

**Frage:** Erhält jedes Paket Standardwerte für alle Quoten, und welche Werte gelten für das Monitoring?

**Optionen:**

1. E-34 beibehalten: keine Standardwerte, der Operator speichert jede Grenze. Folgen: kein Migrationsaufwand; ohne Operatoreingriff ist Monitoring unbegrenzt, PKG-04 ist nicht erfüllt.
2. Standardwerte je Paket für alle Quoten, gespeichert bei Anlage und Paketwechsel (Herkunft `plan`); gespeicherte Abweichungen bleiben; Bestandsorganisationen erhalten Werte erst beim nächsten Paketwechsel. Monitoring (V, Standard / Pro / Enterprise): `max_monitored_targets` 500 / 5.000 / 25.000; `max_probes_per_minute` 1.000 / 10.000 / 30.000; `min_probe_interval_s` 60 / 30 / 10. Folgen: Grenzen wirken ohne Operatoreingriff; bestehende Organisationen werden nicht rückwirkend gesperrt.
3. Standardwerte nur für die neuen Monitoring-Kontingente. Folgen: Monitoring begrenzt; `max_cis`, `max_collectors` und `max_users` bleiben ohne Operatoreingriff unbegrenzt, PKG-04 ist nur teilweise erfüllt.

**Rechnung:** Bei 30 s Intervall erzeugt ein Ziel 2 Prüfungen/min. Standard: 500 Ziele × 2 = 1.000/min, passt. Pro: 5.000 × 2 = 10.000/min, passt. Enterprise: 25.000 × 2 = 50.000/min, also mehr als 30.000/min. In Enterprise begrenzt deshalb `max_probes_per_minute`: 25.000 Ziele sind nur mit einem mittleren Intervall ≥ 50 s erreichbar, mit 30 s Intervall 15.000 Ziele. 30.000/min deckt die Last von 21.180/min (353/s) einer einzelnen Organisation. Option 2 setzt in Standard `min_probe_interval_s` = 60 s. Damit wäre der verbindliche ICMP-Standard 30 s in Standard nicht anlegbar (422 nach PKG-04), obwohl ICMP nach E-151 ab Standard enthalten ist.

**Empfehlung:** Option 2 mit `min_probe_interval_s` Standard 30 s statt 60 s, also – / 30 / 30 / 10 (Essential / Standard / Pro / Enterprise; Essential ohne `monitoring`). „Limits je Paket“ ist Kern von Anforderung 13. Das Muster entspricht der Plandefault-Behandlung von `max_storage_mb` (WP-706) und vermeidet rückwirkende Sperren bei Bestandskunden. Standard 30 s ist zwingend, weil der ICMP-Standard des Auftraggebers in jedem Paket mit `monitoring` gelten muss. Die Last bleibt durch `max_probes_per_minute` (1.000) begrenzt. Die übrigen Werte sind Planannahmen (V) und vom Auftraggeber zu bestätigen (Aufgabe A-59 (Kontingente und Limits) erledigt (2026-11-09)). Dass in Enterprise `max_probes_per_minute` und nicht `max_monitored_targets` bindet, ist gewollt und steht in der Paketbeschreibung (WP-886).

**Auswirkung:** WP-881 legt `PlanQuotaDefaults` mit diesen Werten an. Die Entwurfsfassung von WP-881 (AK2) nennt für `min_probe_interval_s` noch 60 / 30 / 10 und ist auf – / 30 / 30 / 10 anzupassen. WP-885 zeigt Verbrauch und Grenze je Quote.

**Quelle:** backend/internal/entitlement/entitlement.go:117–153; backend/internal/signup/service.go (`planStorageMB`, `PlanStorageMB`); docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:110; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:21` (neu, PKG-04), :225 (neu, NFR-11), :57 (neu, PRT-02); [E-34](../entscheidungen.md#e-34); [E-44](../phase2/entscheidungen.md#e-44)

**Betroffene IDs:** ENT-03, ENT-06, PKG-04, PKG-05, NFR-11, PRT-02

**Betroffene WPs:** WP-881 (`p3-pkg-quotas`), WP-885 (`p3-pkg-usage`)

<a id="e-154"></a>
## E-154 – „Jeweils eigene Limits“ je Abfragetyp: kaufmännisch oder technisch

**Schlüssel:** `d-p3-probe-limits` (eingearbeitet: `d-pt-type-limits`)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2 mit der Grenzwerttabelle)
**Kontext:** Anforderung 9 verlangt eigene Limits je Abfragetyp, Anforderung 2 die Ping-Standards 30 s / 10 s / Down-Count 5 (PRT-02). Entitlement-Quoten sind eine flache Liste ohne Typbezug (`backend/internal/entitlement/entitlement.go:142`). PRT-01 verlangt je Typ „Systemgrenzen (Mindestintervall, Standard- und Höchst-Timeout, Timeout ≤ Intervall, maximale Parallelität je Ziel und je Collector, maximale Rate je Collector, maximale Probes je Ziel, Payload- und Zeilengrenzen)“ in `api/probe-types.json` (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:55`, neu), geprüft von Server und Collector gegen denselben Vertrag. Damit gibt es drei Ebenen: Systemgrenzen je Typ (PRT-01), Paketgrenzen je Mandant (PKG-04, E-153) und Werte der Probe (PRB-01). Das Register aus WP-934 führt 23 Typschlüssel (Konvention 30).

**Frage:** Sind die Limits je Typ kaufmännische Kontingente, technische Grenzen oder beides, und welche Startwerte gelten?

**Optionen:**

1. Alle Limits je Typ als Entitlement-Quoten (`max_targets_<typ>`, Intervall je Typ). Folgen: Der Operator pflegt je Organisation bis zu 23 × 3 Werte; technische Schutzgrenzen (Last auf SNMP-Agenten, ACS, OLT) wären verhandelbar.
2. Trennung: kaufmännische Kontingente im Entitlement (Ziele, Prüfungen/min, Mindestintervall, optionale Kappung `max_targets_<typ>`); technische Grenzen je Typ im Register `api/probe-types.json` (Mindestintervall, Timeout-Höchstwert, Parallelität je Ziel, Rate je Collector, Standardwerte; je Typ `min_interval_s`, `max_interval_s` (Standard 86400), `min_timeout_s`, `default_timeout_s` und `max_timeout_s`). Der Mandant überschreibt Standardwerte nur innerhalb beider Grenzen. Folgen: zwei Prüfungen, getrennt testbar; das Register ist Vertrag für Server und Collector.
3. Nur technische Grenzen, keine Kontingente. Folgen: Lastschutz ohne Paketbezug; widerspricht PKG-04.
4. Aus `d-pt-type-limits`: einheitliche Systemgrenzen für alle Typen (Minimum 30 s, Timeout ≤ 30 s). Folgen: einfach; bremst ICMP und BFD aus und gibt PON- und TR-069-Walks zur Überlast frei.

**Startwerte der Systemgrenzen (Option 2):** je Typ Intervall Standard/Minimum, Timeout Standard/Maximum, Parallelität je Ziel, Rate je Collector. Die Zeilen nennen die Schlüssel des Registers aus WP-934; Zeilen mit mehreren Schlüsseln gelten für jeden davon.

| Typ | Intervall | Timeout | Parallelität | Rate je Collector |
|---|---|---|---:|---:|
| icmp | 30 s / 10 s (Höchstwert 1 h) | 10 s / 10 s (Minimum 1 s) | 1 | 1.000/s |
| tcp | 60 s / 10 s | 5 s / 30 s | 2 | 500/s |
| http | 60 s / 30 s | 10 s / 60 s | 2 | 100/s |
| snmp_status | 60 s / 30 s | 5 s / 15 s | 1 | 200/s |
| snmp_if | 300 s / 60 s | 10 s / 60 s | 1 | 50/s |
| snmp_custom | 300 s / 60 s | 10 s / 60 s | 1 | 100/s |
| redfish | 300 s / 60 s | 15 s / 60 s | 1 | 20/s |
| bgp, ospf, isis | 60 s / 30 s | 10 s / 60 s | 1 | 50/s |
| bfd | 30 s / 10 s | 5 s / 15 s | 1 | 50/s |
| optical | 300 s / 60 s | 15 s / 60 s | 1 | 20/s |
| bng | 60 s / 30 s | 10 s / 30 s | 1 | 20/s |
| pon | 300 s / 120 s | 60 s / 120 s | 1 | 5/s |
| dns | 60 s / 10 s | 5 s / 15 s | 4 | 200/s |
| dhcp | 300 s / 60 s | 10 s / 30 s | 1 | 1/s |
| stamp, twamp_control | 60 s / 10 s | 5 s / 30 s | 1 | 20/s |
| tr069, usp | 900 s / 300 s | 30 s / 60 s | 1 | 5/s |
| gnmi | 60 s / 10 s | 10 s / 60 s | 1 | 50/s |
| rpki_rtr | 300 s / 60 s | 30 s / 120 s | 1 | 1/s |
| rpki_roa | 300 s / 60 s | 1 s / 5 s | – | 100/s |

Down-Count-Standard 5 für icmp (Up-Count 1), sonst 3; Bereich 1–20. Je Probe gilt timeout < interval (PRT-02; erfüllt PRT-01 „Timeout ≤ Intervall“). Bei icmp mit Minimum 10 s ist der Timeout deshalb höchstens 9 s, sobald das Intervall auf 10 s gesenkt wird. Für jeden Typ gilt `max_interval_s` 86400, soweit die Tabelle nichts anderes nennt. Es gelten nur die 23 Schlüssel des Registers aus WP-934 (Konvention 30); Langformen sind unzulässig.

**Empfehlung:** Option 2 mit dieser Tabelle. Kaufmännische Grenzen ändert nur der Operator (ENT-04), technische Grenzen sind Schutz für Zielgeräte und Collector und nicht verhandelbar. Beide Prüfungen bleiben getrennt testbar. Die Typen unterscheiden sich stark in der Last für Zielgeräte und Collector. Einheitliche Grenzen (Option 4) würden entweder die schnellen Typen ausbremsen oder die schweren zur Überlast freigeben. Kontrolle gegen die Gesamtlast: Ein einzelner Collector mit der ICMP-Rate 1.000/s trägt die gesamte Last von 353/s auch dann, wenn alle anderen Mitglieder einer Gruppe ausfallen. Die Werte sind Startwerte; WP-1023 (`p3-pt-bench`) und die Rückmeldung des Auftraggebers schärfen sie.

**Auswirkung:** WP-934 legt das Register an, WP-881 die Kontingente, WP-940 das Budget aus Zielen und Intervallen. WP-969 nutzt die Grenzen und Standards des ICMP-Eintrags aus WP-934 und führt keine eigenen Felder ein. WP-1022 prüft Server und Collector gegen dasselbe Register, WP-1023 misst die Raten je Typ.

**Quelle:** backend/internal/entitlement/entitlement.go:142; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:55` (neu, PRT-01), :35 (neu, PRB-01), :57 (neu, PRT-02), :21 (neu, PKG-04); docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:93 (ENT-04)

**Betroffene IDs:** ENT-04, PKG-04, PRB-01, PRT-01, PRT-02, PRT-19, PRT-21, NFR-11

**Betroffene WPs:** WP-881 (`p3-pkg-quotas`), WP-940 (`p3-pkg-probe-budget`), WP-969 (`p3-pt-icmp`), WP-1022 (`p3-pt-conformance`), WP-1023 (`p3-pt-bench`)

<a id="e-155"></a>
## E-155 – Datenmodell Paket, Add-on und manuelle Freischaltung

**Schlüssel:** `d-p3-entitlement-origin`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Entitlement-Zeilen tragen `plan`, `enabled`, `limits`, `valid_until` und `source` (`manual`, `selfsignup`, `billing`, `reseller`; `backend/internal/entitlement/entitlement.go:155–156`, Struktur `Entitlement` :196–207), sind je Organisation und Schlüssel eindeutig (`backend/migrations/000004_entitlements.up.sql:13`) und tragen keine Herkunft im Sinne von Paket, Add-on oder Einzelfreischaltung. Ein Paketwechsel kann deshalb Add-on-Zeilen und manuelle Freischaltungen nicht von Paketzeilen unterscheiden. Kein Schreibpfad im Backend ändert `organization.plan` nach der Anlage; die Upsert-Klausel in `backend/internal/entitlement/pg_repository.go:72` setzt nur `entitlement.plan`. PKG-01 verlangt `organization.plan` als führende Angabe und die Herkunft `plan`, `addon` oder `manual` je Zeile.

**Frage:** Wie wird unterschieden, welche Zeile aus dem Paket, aus einem Add-on oder aus einer manuellen Freischaltung stammt?

**Optionen:**

1. Spalte `entitlement.origin` (`plan` | `addon` | `manual`) mit Backfill; `organization.plan` führend, dazu `organization.plan_changed_at`. Folgen: eine Migration; Middleware, Cache, Operator-Grant und Ablauf je Zeile (`valid_until`) bleiben.
2. Eigene Tabelle `organization_addon`; Paketzeilen werden zur Laufzeit aus `organization.plan` abgeleitet. Folgen: Auswertung, Cache und Ablauf je Zeile werden umgebaut; zwei Quellen für dieselbe Frage „ist Funktion X frei“.
3. Keine Zeilen für Paketfunktionen, nur Abweichungen speichern. Folgen: weniger Zeilen; Ablauf und Limits je Paketfunktion lassen sich nicht mehr je Zeile speichern, die bewährte Auswertung entfällt.

**Empfehlung:** Option 1. Kleinste Änderung. Option 2 und 3 bauen die bewährte Auswertung und den Ablauf je Zeile um. Backfill-Regel: Schlüssel aus `Addons` → `addon`; Schlüssel, die `PlanFeatures(organization.plan)` enthält und deren Zeile weder `valid_until` noch Limits außerhalb der Paketwerte trägt → `plan`; alles andere → `manual`. Die Regel ist deterministisch und im Down-Skript umkehrbar (Spalte entfällt).

**Auswirkung:** WP-880 enthält die Migration `backend/migrations/<nächste Nr.>_entitlement_origin.up.sql` und `.down.sql` mit `docs/schema-baseline.md`. WP-882 ändert beim Paketwechsel nur Zeilen mit `origin = plan`.

**Quelle:** backend/internal/entitlement/entitlement.go:155–156, :196–207; backend/migrations/000004_entitlements.up.sql:13; backend/internal/entitlement/pg_repository.go:72; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:15` (neu, PKG-01), :17 (neu, PKG-02)

**Betroffene IDs:** ENT-01, ENT-04, PKG-01, PKG-02, PKG-03

**Betroffene WPs:** WP-880 (`p3-pkg-model`), WP-882 (`p3-pkg-plan-change`)

<a id="e-156"></a>
## E-156 – Wirkung von Downgrade, Kündigung und Ablauf auf laufende Prüfungen

**Schlüssel:** `d-p3-suspend`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** ENT-08: „keine Löschung von Daten; bei Überschreitung des neuen Limits nur Sperre für Neuanlage“ (`docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:114`). CH21: Nach Lizenzablauf „stoppt ausschließlich Discovery/Ingest“ (`docs/spec/katalog-v3/00-grundlagen.md:41`). Monitoring erzeugt anders als die übrigen Module laufende Last auf Collector und Server. Fällt ein Probetyp aus dem Paket, liefen seine Prüfungen sonst unbezahlt weiter. PKG-09 verlangt: Prüfungen einer weggefallenen Funktion pausieren, Kontingentüberschreitung sperrt nur die Neuanlage, org_admins werden benachrichtigt.

**Frage:** Was geschieht mit laufenden Prüfungen bei Downgrade, Kündigung oder Ablauf?

**Optionen:**

1. ENT-08 wörtlich: Alles läuft weiter, nur die Neuanlage ist gesperrt. Folgen: unbezahlte Dauerlast bis zum nächsten Eingriff; widerspricht PKG-09.
2. Prüfungen weggefallener Funktionen werden pausiert (Konfiguration und Historie bleiben) und nach erneuter Freischaltung fortgesetzt; bei Kontingentüberschreitung wird nur die Neuanlage gesperrt; Benachrichtigung der org_admins. Folgen: keine Datenverluste; Last folgt dem Paket.
3. Wie 2, zusätzlich pausieren überzählige Ziele (zuletzt angelegte zuerst). Folgen: harte Lastgrenze; die Auswahl ist für den Mandanten willkürlich und kann kritische Ziele treffen.

**Empfehlung:** Option 2. Sie überträgt CH21 sinngemäß auf das Monitoring: Die lastverursachende Funktion stoppt, alles andere bleibt. Es gibt keinen Datenverlust und keine willkürliche Auswahl von Zielen wie in Option 3.

**Auswirkung:** WP-882 löst beim Paketwechsel die Pausierung aus. WP-942 pausiert und setzt fort: Der Zuweisungssatz an den Collector enthält pausierte Probes nicht. Der Zustand pausierter Probes ist `unknown` mit Grund `paused`, und für die Pausendauer entsteht kein Alarm.

**Quelle:** docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:114 (ENT-08); docs/spec/katalog-v3/00-grundlagen.md:41 (CH21); `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:31` (neu, PKG-09)

**Betroffene IDs:** CH21, ENT-08, PKG-09

**Betroffene WPs:** WP-882 (`p3-pkg-plan-change`), WP-942 (`p3-pkg-suspend`)

<a id="e-157"></a>
## E-157 – Trial-Ende nach ENT-07 („danach Essential-Limits“)

**Schlüssel:** `d-p3-trial-end`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** ENT-07: „Trial (Self-Signup, V): 30 Tage Standard-Funktionsumfang mit max_cis 100, danach Essential-Limits“ (`docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:112`). Nach E-44 endet der Trial nur über `valid_until` einzelner Zeilen: `TrialEntitlements` setzt `valid_until` für Funktionen, die der Fallback-Plan nicht enthält, und speichert `max_cis` des Trials in `cmdb_core` (`backend/internal/signup/service.go:331–359`). `organization.plan` bleibt `standard`, `max_cis` bleibt 100.

**Frage:** Wechselt die Organisation am Trial-Ende auf das Fallback-Paket?

**Optionen:**

1. Systemjob `entitlement.trial_end` wechselt nach Ablauf per Paketwechsel (PKG-02) auf den Fallback-Plan (Herkunft `selfsignup`), mit der vorhandenen Vorwarnung 14/7/1 Tage und Audit. Folgen: `organization.plan`, Funktionen und Standardlimits entsprechen danach dem Fallback-Paket; Daten bleiben nach ENT-08.
2. Stand E-44 beibehalten. Folgen: kein Aufwand; `organization.plan` bleibt `standard`, die Limits sind keine Essential-Limits, ENT-07 ist nur teilweise erfüllt.

**Empfehlung:** Option 1. Sie nutzt den Paketwechsel aus PKG-02 und erfüllt ENT-07 wörtlich. Daten bleiben nach ENT-08 erhalten. Der Job ist idempotent: Eine Organisation, deren Paket der Operator während des Trials gewechselt hat (`plan_changed_at` nach Trial-Beginn), bleibt unverändert.

**Auswirkung:** WP-884 liefert den Systemjob unter Single-Runner, den Paketwechsel über den Dienst aus WP-882 und den Eintrag in `operator_audit` mit Akteur `system`.

**Quelle:** docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:112 (ENT-07); backend/internal/signup/service.go:331–359 (`TrialEntitlements`); [E-44](../phase2/entscheidungen.md#e-44)

**Betroffene IDs:** ENT-07, ENT-08, PKG-02

**Betroffene WPs:** WP-884 (`p3-pkg-trial-end`)

<a id="e-158"></a>
## E-158 – Form der Operator-Oberfläche (PKG-07)

**Schlüssel:** `d-p3-operator-ui`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Operatoren melden sich über `POST /api/v1/admin/auth/callback` an (`backend/internal/operator/handler.go:43`): OIDC, IdP-Gruppe `operators` (`backend/internal/operator/auth.go:39–48`), MFA über `amr`/`acr` (:41–43, :53) und eine Sitzung von 15 Minuten (`SessionLifetime`, :24). Die Mandanten-API lehnt Operator-Sitzungen ab (SEC-07, `docs/spec/katalog-v3/06-audit-sicherheit-events.md:36`). Das Frontend hat keinen Operator-Bereich; unter `frontend/src` gibt es keine Seite und keinen API-Client für `/api/v1/admin/*`. PKG-07 verlangt eine Operator-Oberfläche für Paket, Add-ons, Limits und Verbrauch.

**Frage:** In welcher Form entsteht die Operator-Oberfläche?

**Optionen:**

1. Operator-Bereich `/operator` in der bestehenden SPA mit eigener Sitzung, eigenem API-Client und eigenem Routenschutz. Folgen: Designsystem, i18n und Testwerkzeuge werden wiederverwendet; die SPA muss zwei Sitzungsarten getrennt halten.
2. Eigene kleine SPA (separater Build und Auslieferung). Folgen: harte Trennung; doppelte Pipeline, doppeltes Designsystem, eigener Auslieferungspfad im Helm/Compose.
3. Nur API plus Skripte/CLI. Folgen: kein UI-Aufwand; PKG-07 verlangt eine Oberfläche.

**Empfehlung:** Option 1. Designsystem, i18n und Tests werden wiederverwendet. Die Trennung der Sitzungen bleibt serverseitig erzwungen (SEC-07): Eine Mandantensitzung erhält auf `/api/v1/admin/*` 401, eine Operator-Sitzung auf der Mandanten-API ebenso. Der Client speichert beide Sitzungen getrennt und sendet nie beide.

**Auswirkung:** WP-887 liefert Anmeldung, Sitzung und Routenschutz, WP-888 die Seiten für Organisationen, Paket, Add-ons, Limits und Verlauf. WP-1174 prüft die Sitzungstrennung.

**Quelle:** backend/internal/operator/handler.go:43; backend/internal/operator/auth.go:23–53; backend/internal/server/authz.go:612; docs/spec/katalog-v3/06-audit-sicherheit-events.md:36 (SEC-07); `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:27` (neu, PKG-07)

**Betroffene IDs:** PKG-07, SEC-07, UI-14

**Betroffene WPs:** WP-887 (`p3-pkg-operator-auth-ui`), WP-888 (`p3-pkg-operator-orgs-ui`)

<a id="e-159"></a>
## E-159 – Rechteschlüssel P3 und Rollenmatrix

**Schlüssel:** `d-p3-permissions` (eingearbeitet: `d-net-permissions`, `d-finding-perm`)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** RBA-05 nennt für Phase 3 die Modulrechte „workflow, form, maintenance, compliance, finding“ (`docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:70`). Der Code führt für Findings `security:read`/`security:write` (`backend/internal/identity/types.go:79–80`), der Pfad heißt `/api/v1/security/findings`, das Modul `findings` (`backend/internal/server/modules.go:87`). Für Zugangsdaten gibt es `credential:read`/`credential:manage` (`identity/types.go:39–40`), für IPAM `ipam:read`/`ipam:write` (:85–86), für Monitoring `monitoring:read`/`monitoring:write` (:93–94). Es fehlen Rechte für die Quittung von Alarmen, ADMP, Eskalation, Org-Export, Org-Löschung, die Bindung von Zugangsdaten, die Tresorkonfiguration und die personenbezogenen Anschlussinhaberdaten aus ADMP. Phase 2 hat Rechte mit Übersetzung der Grants umbenannt (`backend/migrations/000192_permissions_p2.up.sql:1–25`).

**Frage:** Wie heißen die P3-Rechte, und welche Standardrolle erhält welches Recht?

**Optionen:**

1. `security:*` → `finding:*` ohne Alias, mit Übersetzung aller Grants in `role_permission`, `role.permissions`, `custom_role.permissions` und `api_key.permissions` (Muster 000192); Pfad `/api/v1/security/findings` bleibt. Neue Rechte: `alert:acknowledge`, `admp:read`, `escalation:manage`, `org:export`, `org:delete`, `workflow:approve` (WP-866) sowie `credential:bind`, `vault:manage`, `admp:holder_read`, `admp:manage` (WP-870). ASN, NAT, BGP-Peerings und Sections laufen über `ipam:read`/`ipam:write`. Matrix: `org_admin` alle; `engineer` `monitoring:*`, `ipam:*`, `maintenance:*`, `finding:*`, `alert:acknowledge`, `admp:read`, `credential:bind`; `client_technician` `monitoring:read` und `alert:acknowledge` im Client-Scope; Leserollen nur `:read` ohne `admp:read`; `org:export`, `org:delete`, `escalation:manage`, `workflow:approve`, `vault:manage`, `admp:holder_read` und `admp:manage` nur `org_admin` (`workflow:approve` an weitere Nutzer über Custom Roles). Folgen: RBA-05 wörtlich erfüllt; eine Datenmigration; die Module waren nie freigegeben, deshalb ist kein Alias nötig.
2. `security:*` behalten, nur neue Schlüssel ergänzen. Folgen: keine Migration; RBA-05 bleibt für Findings unerfüllt.
3. Aus `d-finding-perm`: Rechte und Pfad umbenennen (`/api/v1/findings`). Folgen: wörtlich am Katalog; zusätzlicher Pfadumbau und Abkündigung ohne fachlichen Gewinn (siehe E-160).
4. Aus `d-net-permissions`: eigene Rechte je Objektart (ASN, NAT, Section). Folgen: feinere Steuerung; mehr Rollenpflege ohne erkennbaren Bedarf.

**Empfehlung:** Option 1. RBA-05-konform ohne Umbau der API. `admp:read` und `admp:holder_read` schützen getrennt Anschlussdaten und personenbezogene Anschlussinhaberdaten ([E-243](entscheidungen.md#e-243)). `org:delete` bleibt beim `org_admin` mit MFA (TLC-06). Tresorkonfiguration und ADMP-Kontoverwaltung sind sicherheitsrelevant und erhalten deshalb eigene Rechte; für ASN, NAT und Sections genügen die IPAM-Rechte. `d-finding-perm` sah einen Alias für eine Version vor. Er entfällt, weil das Modul `findings` im Release-Standard nie registriert war (Phase P3) und Phase 2 nach demselben Muster ohne Alias ersetzt hat (000192).

**Auswirkung:** Neue Rechte und die Rollenmatrix registriert nur M3.0: WP-866 enthält die Migration `backend/migrations/<nächste Nr.>_permissions_p3.up.sql` und `.down.sql` mit `docs/schema-baseline.md` und registriert auch `workflow:approve`; WP-870 ergänzt die Netz- und ADMP-Rechte. WP-1098 registriert kein Recht selbst und nutzt `workflow:approve`. Die Zuordnung Route → Recht (`readPermissionFor`, `writePermissionOverrides`, `routeRules` in `backend/internal/server/authz.go`, `route_permissions.txt`, Audit-Scope und Snapshot-Register) liefert das Fach-WP, das die Route registriert, etwa WP-890 für `/api/v1/credential-bindings`, WP-893 für `/api/v1/vault-connections`, WP-947 für `/api/v1/collector-groups` (Lesen `monitoring:read`, Schreiben `collector:manage`). Festgelegte Zuordnungen: `GET /api/v1/vault-connections` (ohne Geheimnis) verlangt `credential:read`, Schreiben `vault:manage` (WP-893). Inhalt und Diff von Konfigurationsversionen verlangen `monitoring:read` und zusätzlich `credential:read` (WP-1056); ein eigenes Recht gibt es nicht. Die Quittung `POST /api/v1/monitoring/alarms/{id}/ack|unack` (WP-983) und `POST /api/v1/monitoring/incidents/{id}/ack` verlangt `alert:acknowledge`; WP-866 bindet das Recht an diese Pfade, nicht an `/monitoring/alerts/{id}/acknowledge`. Eskalationsrichtlinien verlangen zum Lesen und Schreiben `escalation:manage`. WP-1116 nutzt nur noch `finding:*`.

**Quelle:** docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:70 (RBA-05); backend/internal/identity/types.go:39–40, :79–80, :85–86, :93–94; backend/internal/server/modules.go:87; backend/migrations/000192_permissions_p2.up.sql:1–25; docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:66 (TLC-06)

**Betroffene IDs:** RBA-05, RBA-06, TLC-05, TLC-06, NTF-02, ALR-01, CRD-01, CRD-03, ADM-01

**Betroffene WPs:** WP-866 (`p3-permissions`), WP-870 (`p3-net-contract`), WP-983 (`p3-alr-api`), WP-1056 (`p3-cfg-diff-api`), WP-1098 (`p3-wf-approval`), WP-1116 (`p3-finding-model`)

<a id="e-160"></a>
## E-160 – Pfade und Modulschlüssel der P3-Module

**Schlüssel:** `d-p3-paths`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Bestehende P3-Module und Präfixe (`backend/internal/server/modules.go:82–90`): `maintenance` (`/api/v1/maintenance-windows`), `workflows` (`/api/v1/forms`, `/api/v1/form-submissions`, `/api/v1/workflows`, `/api/v1/workflow-runs`), `compliance` (`/api/v1/compliance`), `findings` (`/api/v1/security`) und `alerting` (`/api/v1/monitoring/alerts`, Worker `monitoring`). `/api/v1/monitoring/metrics` gehört zu MON-02 [B] und läuft im G1/G2-Release (Kommentar :88). Keines dieser P3-Module ist im Release-Standard G2 registriert (`ParseModules`, :124–131). API-06 verlangt REST für alle Module nach Phase (`docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:14`).

**Frage:** Bleiben die Bestandspfade, und welche Präfixe und Modulschlüssel erhalten die neuen Funktionen?

**Optionen:**

1. Bestehende Pfade bleiben. Neue Präfixe: `/api/v1/monitoring/{targets,probes,probe-types,profiles,groups,events,alarms,incidents,alarm-settings,alarm-playbooks,sla,forecasts}`, `/api/v1/cis/{id}/availability`, `/api/v1/collector-groups` (nicht unter `/monitoring`), `/api/v1/ipam/*`, `/api/v1/ipam-sections`, `/api/v1/asns`, `/api/v1/asn-ranges`, `/api/v1/bgp-peerings`, `/api/v1/nat-translations`, `/api/v1/nameserver-sets`, `/api/v1/vlan-domains`, `/api/v1/credential-bindings`, `/api/v1/vault-connections`, `/api/v1/admp/*` (nur `GET` außer der Kontoverwaltung `admp/accounts`), `/api/v1/escalation-policies`, `/api/v1/notification-escalations`, `/api/v1/org/exports`, `/api/v1/org/deletion`, `/api/v1/admin/orgs/{id}/{plan,addons,usage}`. Modul `alerting` wird `monitoring` (`alerting` bleibt als Alias in `RETICORA_MODULES`); neue Module `ipam` (einschließlich `/api/v1/nameserver-sets` und `/api/v1/vlan-domains`, Gate `ipam`), `admp`, `escalation`, `org_lifecycle`. Unterpfade bestehender Ressourcen laufen über Suffixregeln in `featureRules`: `/api/v1/cis` + `/availability` → `monitoring`; `/api/v1/cis` + `/credentials` → `monitoring` (CRD-01 `GET/PUT /api/v1/cis/{id}/credentials`, CRD-04 `/api/v1/cis/{id}/credentials/effective`; gleiche Wirkung wie `/api/v1/credential-bindings`); `/api/v1/cis` + `/addresses` → `ipam`; `/api/v1/subnets` + `/scan` → [`ipam`, `monitoring`]; `/api/v1/subnets` + `/rpki` → [`ipam`, `monitoring_network`]; `/api/v1/cis` + `/configs` → `monitoring_network`; `/api/v1/cis` + `/admp-connections` und `/api/v1/network-interfaces` + `/admp` → `admp`. Folgen: keine Abkündigungen; Modul- und Feature-Gate greifen je Präfix bzw. Suffix.
2. Umbenennung der Altpfade nach RBA-05 (`/api/v1/findings`) mit Abkündigung. Folgen: Pfadnamen wie Rechtenamen; Abkündigung mit `x-sunset` ≥ 12 Monate für Pfade, die nie freigegeben waren (E-25 verlangt sie nur für G1-Pfade).

**Empfehlung:** Option 1. Keine Abkündigungen für Module, die nie freigegeben waren. Die Präfixe sind die Grundlage für Modul- und Feature-Gate (WP-863, WP-865). Der Alias `alerting` verhindert, dass bestehende Installationen mit `RETICORA_MODULES=alerting` beim Start scheitern; `ParseModules` lehnt unbekannte Schlüssel ab (:129–130).

**Auswirkung:** WP-863 stellt das Modulregister um (`backend/internal/server/modules.go`, `LateModules`), WP-864 legt für dieselbe Liste das Pfadgerüst mit `x-status: planned` an, WP-865 die Feature-Gates je Präfix und die Suffixregeln (`featureRules` in `backend/internal/entitlement/middleware.go`). Alle Präfixe und Suffixregeln der P3-Pfade stehen ausschließlich in WP-863, WP-864 und WP-865 (Konvention 31); Domänen-WPs (u. a. WP-905, WP-906, WP-911, WP-930, WP-938, WP-983, WP-1056, WP-1062, WP-1070) ändern `modules.go` und `entitlement/middleware.go` nicht. Nicht verwendet werden `/api/v1/probes`, `/api/v1/alarms`, `/api/v1/incidents`, `/api/v1/alarm-settings`, `/api/v1/alarm-playbooks`, `/api/v1/availability-targets`, `/api/v1/capacity-forecasts` und `/api/v1/monitoring/collector-groups`.

**Quelle:** backend/internal/server/modules.go:82–90, :124–131; docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:14 (API-06); docs/plan/phase2/entscheidungen.md#e-38 (Muster Pfadinventar)

**Betroffene IDs:** API-06, OPS-02, RBA-05, PKG-08

**Betroffene WPs:** WP-863 (`p3-module-gates`), WP-864 (`p3-paths`), WP-865 (`p3-feature-gates`)

<a id="e-161"></a>
## E-161 – Ereignisse des Monitorings

**Schlüssel:** `d-p3-event-granularity`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** Bei 353 Prüfungen/s fallen 30,5 Mio. Prüfergebnisse je Tag an (NFR-11). EVT-01 begrenzt den Stream `RETICORA_EVENTS` auf „Retention 30 Tage / 10 GB pro Stream (konfigurierbar)“ (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:48`). Ereignisse laufen über die Outbox in derselben Transaktion wie die Fachänderung. ALR-02: „Die Ereignisse alarm.opened, alarm.acknowledged, alarm.resolved, incident.opened und incident.resolved stehen über Outbox, Webhooks und Benachrichtigungen bereit; diese Namen gelten für alle Ergänzungen dieses Auszugs“ (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:103`, neu). PRB-03 verlangt das Root-Ereignis `collector_group.unreachable` (:37), ALR-07 das Ereignis `ci.config_changed` (:113).

**Rechnung:** Ein Ereignis je Prüfergebnis mit 0,5–1 kB Envelope ergibt 30,5 Mio. × 0,5–1 kB = 15–30 GB/Tag. Damit ist die Grenze von 10 GB nach weniger als einem Tag erreicht, und die Outbox schreibt 353 Zeilen/s zusätzlich. Ein Ereignis je Zustandswechsel: 10.600 Probes (E-168) mit oberer Annahme 10 Wechseln je Probe und Tag ergeben 106.000 Ereignisse/Tag ≈ 1,2/s, bei 1 kB ≈ 106 MB/Tag, in 30 Tagen ≈ 3,2 GB. Das liegt innerhalb der 10 GB.

**Frage:** Wie fein sind die Ereignisse des Monitorings?

**Optionen:**

1. Ereignis je Prüfergebnis. Folgen: Rechnung oben; Stream und Outbox überlastet, Webhook-Abonnenten erhalten 353 Zustellungen/s.
2. Ereignis je Zustandswechsel einer Probe (`probe.state_changed`, mit `suppressed_by` und `root_incident_id` bei Folgeausfällen), je Alarm-Lebenszyklus (`alarm.opened`, `alarm.acknowledged`, `alarm.escalated`, `alarm.severity_changed`, `alarm.resolved`), je Störung (`incident.opened`, `incident.updated`, `incident.resolved`), für Collector-Gruppen (`collector_group.unreachable`, `collector_group.reachable`, `collector_group.failover`, `collector.clock_skew`) sowie `config.changed` (ausschließlich Änderungen an der Probe-Konfiguration), `ci.config_changed` (Gerätekonfiguration nach ALR-07), `monitoring.suspension_changed` (Sperrwirkung nach PKG-09), `capacity.forecast_exceeded` und `sla.violated`. Folgen: ≈ 1,2 Ereignisse/s; Rohdaten bleiben in der Zeitreihe.
3. Nur Alarm-Lebenszyklus. Folgen: minimale Last; Zustandswechsel ohne Alarm (etwa unterdrückte Symptome) sind für Webhook-Abonnenten unsichtbar, ALR-02 und PRB-03 sind nicht erfüllt.

**Empfehlung:** Option 2. Zustandswechsel sind selten und zeitnah zu übertragen (Anforderung 6). Webhook-Abonnenten brauchen sie, die Rohdaten bleiben in der Zeitreihe. Die Namen folgen ALR-02 und PRB-03. Die Modul-Ereignisse der Phase 3 (Wartung, Workflow, Finding, Eskalation, Paketwechsel, Org-Lebenszyklus) folgen derselben Regel: ein Ereignis je fachlichem Zustandswechsel.

**Auswirkung:** Registriert werden die Typen nur in WP-867, WP-868 und WP-870 (Konvention 32). WP-867 registriert die Monitoring-Typen einschließlich `ci.config_changed` (`EventReadPermission` `monitoring:read`) und `monitoring.suspension_changed` (`monitoring:read`), WP-868 die Modul-Typen (`notification.escalated`, `notification.acknowledged`, `entitlement.changed`, `org.*` u. a.) und entfernt `Reserved`, WP-870 die Zugangsdaten- und ADMP-Typen (u. a. `credential.rotated`, `credential.rotation_due`, `admp.connection.refreshed`). `reconciliation.policy_changed` wird kein Outbox-Typ; WP-1105 nutzt den bestehenden Dispatcher-Pfad (`backend/internal/webhook/webhook.go:248`). Nicht verwendet werden `alert.*`, `monitor.*`, `collector.failover` und ein Alias. Fach-WPs ändern `backend/internal/events/types.go`, `api/events.schema.json`, `backend/internal/webhook/scope.go`, das Enum `WebhookEvent` und `WebhooksPage.tsx` für neue Typen nicht. Die Monitoring-Abfrage `GET /api/v1/monitoring/events` liefert die Monitoring-Ereignisse aus `monitoring_event` (WP-979, WP-1079), nicht diese Domänenereignisse; diese laufen über Outbox, Webhooks und Benachrichtigungen. WP-1147 ordnet die Typen den Benachrichtigungskategorien zu.

Produzenten in `plannedProducers` (WP-867 AK3, WP-868 AK3); das produzierende WP führt `evt02_producers_integration_test.go` in seiner Dateiliste:

| Ereignistyp | Produzent |
|---|---|
| `probe.state_changed` | WP-961 |
| `alarm.opened`, `alarm.acknowledged`, `alarm.severity_changed`, `alarm.resolved` | WP-982 |
| `alarm.escalated` | WP-1146 |
| `incident.opened`, `incident.resolved` | WP-1028 |
| `incident.updated` | WP-1036 (manuelles Festlegen der Wurzel und Ack) |
| `collector_group.unreachable`, `collector_group.reachable` | WP-964 |
| `collector_group.failover` | WP-963 |
| `collector.clock_skew` | WP-962 |
| `config.changed` | WP-939 |
| `ci.config_changed` | WP-1054 |
| `monitoring.suspension_changed` | WP-942 |
| `capacity.forecast_exceeded` | WP-1060 |
| `sla.violated` | WP-1049 |
| `maintenance.scheduled` | WP-1034 |
| `maintenance.started`, `maintenance.ended` | WP-1033 |
| `workflow.run_*` | WP-1092 |
| `workflow.approval_*` | WP-1098 |
| `workflow.webhook` | WP-1096 |
| `finding.*` | WP-1116 |
| `notification.escalated`, `notification.acknowledged` | WP-1146 |
| `entitlement.changed` | WP-882 |
| `org.export_completed` | WP-1151 |
| `org.deletion_*` | WP-1152 |
| `credential.rotated` | WP-889 |
| `credential.rotation_due` | WP-897 |
| `admp.connection.refreshed` | WP-929 |

**Quelle:** docs/spec/katalog-v3/06-audit-sicherheit-events.md:48 (EVT-01); `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:225` (neu, NFR-11), :103 (neu, ALR-02), :37 (neu, PRB-03), :113 (neu, ALR-07), :31 (neu, PKG-09); backend/internal/webhook/webhook.go:248

**Betroffene IDs:** EVT-01, EVT-03, NTF-05, ALR-02, ALR-07, PRB-03, PKG-09, NFR-11, MON-04

**Betroffene WPs:** WP-867 (`p3-events-monitoring`), WP-868 (`p3-events-modules`), WP-870 (`p3-net-contract`), WP-1147 (`p3-ntf-categories`)

<a id="e-162"></a>
## E-162 – Betriebsform der Monitoring-Worker

**Schlüssel:** `d-p3-workers`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** OPS-02 verlangt abschaltbare Worker, „[P3] zusätzlich Monitoring, Workflow“ (`docs/spec/katalog-v3/01-installation-stack.md:49`). Jeder Worker hat heute den Schalter `RETICORA_WORKER_<NAME>_ENABLED` (`backend/cmd/server/workers.go:92–98`); `WorkerMonitoring` ist registriert (`backend/internal/config/config.go:474`). Die Prüflast von 353/s liegt deutlich über dem Ingest-Pfad nach NFR-03 (200 DeviceRecords/s, `docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:38`). Ausgeliefert werden ein Kubernetes-Deployment mit HPA (`deploy/k8s/base/deployment.yaml`, `deploy/k8s/base/hpa.yaml`) und ein Compose-Stack (`deploy/docker-compose/docker-compose.yml`).

**Frage:** Laufen die Monitoring-Worker im Serverprozess oder in einem eigenen Deployment-Profil?

**Optionen:**

1. Alle Worker im Serverprozess mit Schaltern. Folgen: keine neue Auslieferung; Lastspitzen der Prüfdaten treffen die API-Latenz (NFR-02), API und Monitoring skalieren nur gemeinsam.
2. Zusätzliches Deployment-Profil `monitoring`: Kubernetes-Deployment und Compose-Dienst desselben Images, nur Monitoring-Worker aktiv, eigener HPA; im Server-Profil sind die Monitoring-Worker aus. Folgen: ein zusätzliches Deployment; Prüflast skaliert unabhängig.

**Empfehlung:** Option 2. Sie nutzt die bestehende Schalterkonvention, skaliert die Prüflast unabhängig von der API und begrenzt die Wirkung von Lastspitzen auf NFR-02. Für kleine Installationen bleibt der Ein-Prozess-Betrieb möglich: Ohne das Profil laufen alle Worker im Server, wie heute.

**Auswirkung:** WP-869 bindet Worker an Module und liefert das Profil in Kubernetes und Compose. WP-1114 schaltet den Workflow-Worker einzeln ab. WP-1170 prüft beide Profile im Install- und k8s-Smoke.

**Quelle:** docs/spec/katalog-v3/01-installation-stack.md:49 (OPS-02); backend/cmd/server/workers.go:92–98; backend/internal/config/config.go:474; deploy/k8s/base/deployment.yaml; deploy/k8s/base/hpa.yaml; deploy/docker-compose/docker-compose.yml; docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:36, :38

**Betroffene IDs:** OPS-02, NFR-02, NFR-03, NFR-11, INS-01

**Betroffene WPs:** WP-869 (`p3-workers`), WP-1114 (`p3-ops02-workflow-worker`), WP-1170 (`p3-install`)

<a id="e-163"></a>
## E-163 – Umfang SMS/Pager, Eskalation und Quittungswege

**Schlüssel:** `d-p3-notify-providers` (eingearbeitet: `d-alr-escalation`)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2; Eskalation in M3.18, kanalneutral)
**Kontext:** NTF-02: „SMS und Pager über dieselbe Abstraktion (CH24); Referenz-Provider (V): SMS Twilio und Sipgate; Pager ilert, PagerDuty, Opsgenie sowie SMS-Pager-Gateway; Eskalationsketten mit Bestätigung“ (`docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:74`). Der Katalog vermerkt: „Hinweis zu NTF-02 (Abschnitt 41, unverändert): Die Eskalation der Alarmierung ist Bestandteil von NTF-02 und hat keine eigene ID.“ (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:125`, neu). SEQ-01 legt Epic E (Monitoring) vor Epic F (SMS/Pager). Die Alarm-WPs der Epic E liefern Ack und Un-Ack über API und UI (WP-982, WP-983, WP-991).

**Frage (a):** Welche Provider und welche Quittungswege liefert Phase 3?

**Optionen:**

1. Alle sechs Provider; Quittung in UI/API, über Rückruf der Pager-Provider (signiert) und per SMS-Antwort mit Einmalcode (Twilio/Sipgate eingehend). Folgen: vollständig; SMS-Antworten sind schwer zu authentisieren (Absender-Spoofing) und brauchen eingehende Endpunkte je SMS-Provider.
2. Alle sechs Provider; Quittung nur in UI/API und über signierte Rückrufe der Pager-Provider. Folgen: NTF-02 vollständig; keine eingehenden SMS.
3. Nur Twilio und PagerDuty in Phase 3. Folgen: weniger Aufwand; NTF-02 nur teilweise erfüllt.

**Frage (b), aus `d-alr-escalation`:** Entsteht die Eskalation schon in Epic E oder erst mit NTF-02?

1. Eskalation erst mit NTF-02 (M3.18, Epic F).
2. Eskalationsrichtlinie und Ack-Timer im Alarmmodul in Epic E; die Stufen nutzen vorhandene Kanäle (E-Mail, In-App, Webhook); NTF-02 ergänzt später Kanäle.
3. Eskalation nur über externe Pager-Dienste.

**Empfehlung:** (a) Option 2. Sie erfüllt NTF-02 vollständig. SMS-Antworten erweitern die Angriffsfläche, eingehende Rückrufe der Pager-Provider sind dagegen signiert und prüfbar. (b) Option 1, aber kanalneutral: NTF-02 verlangt die Eskalationskette mit Bestätigung ausdrücklich, und SEQ-01 legt sie damit in Epic F. Die Stufen adressieren Kanäle über die Provider-Abstraktion: E-Mail, In-App und Webhook ab dem ersten Tag, SMS und Pager mit WP-1141 bis WP-1143. Epic E liefert Quittung und das Ereignis `alarm.acknowledged`, auf denen die Eskalation aufsetzt. Option (b) 3 macht die Eskalation von einem Fremddienst abhängig.

**Auswirkung:** WP-1141 bis WP-1143 liefern die Provider, WP-1144 das Modell, WP-1145 den Eskalationslauf, WP-1146 die signierten Rückrufe, WP-1148 die Oberflächen. WP-1172 prüft den Ausfall eines Providers: Die Eskalation läuft mit der nächsten Stufe weiter.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:74 (NTF-02); docs/spec/katalog-v3/00-grundlagen.md:47 (CH24); docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:79 (SEQ-01); `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:125` (neu, Hinweis zu NTF-02)

**Betroffene IDs:** CH24, NTF-02, NTF-03, TEC-13, OPS-06, SEQ-01

**Betroffene WPs:** WP-1141 (`p3-ntf-sms`), WP-1142 (`p3-ntf-pager`), WP-1143 (`p3-ntf-gateway`), WP-1144 (`p3-ntf-escalation-model`), WP-1145 (`p3-ntf-escalation-engine`), WP-1146 (`p3-ntf-ack-inbound`), WP-1148 (`p3-ntf-ui`), WP-1172 (`p3-outage-notify`)

<a id="e-164"></a>
## E-164 – Mobilnummer für SMS-Empfänger

**Schlüssel:** `d-p3-user-mobile`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** `app_user` hat keine Telefonnummer (`backend/migrations/000005_users_roles.up.sql:3–15`; keine spätere Migration ergänzt eine). `personalKeys` kennt `phone` und `mobile` als personenbezogene Feldnamen (`backend/internal/pii/surrogate.go:26–31`). Kontakte führen Telefonnummern im PII-Vault (`backend/migrations/000126_contact_pii.up.sql:1–15`). NTF-03 löst Empfänger als Nutzer, Team und Kontaktrolle auf (`docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:76`). SEC-05 verlangt PII nur über Surrogate (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:32`).

**Frage:** Wo steht die Rufnummer eines SMS-Empfängers?

**Optionen:**

1. Mobilnummer am Nutzer als PII-Surrogat im Vault, im Profil selbst pflegbar, E.164-geprüft. Folgen: Eskalationsketten adressieren Nutzer und Teams direkt; eine Migration (`app_user.mobile_pii_subject_id`).
2. Nur Rufnummern an Kontakten und Kanälen. Folgen: keine Änderung am Nutzer; jede Person der Bereitschaft braucht zusätzlich einen Kontakt.
3. Rufnummern nur in der Kanalkonfiguration. Folgen: einfach; Rufnummern stehen im Klartext in der Konfiguration und verletzen SEC-05.

**Empfehlung:** Option 1. Eskalationsketten adressieren Nutzer und Teams (NTF-03), und SEC-05 verlangt PII nur über Surrogate. Muster ist der Kontakt-Vault aus 000126.

**Auswirkung:** WP-1140 enthält die Migration `backend/migrations/<nächste Nr.>_user_mobile.up.sql` und `.down.sql` mit `docs/schema-baseline.md`, die E.164-Prüfung und die Pflege im Profil sowie durch `user:manage`.

**Quelle:** backend/migrations/000005_users_roles.up.sql:3–15; backend/internal/pii/surrogate.go:23–31; backend/migrations/000126_contact_pii.up.sql:1–15; docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:76 (NTF-03); docs/spec/katalog-v3/06-audit-sicherheit-events.md:32 (SEC-05)

**Betroffene IDs:** NTF-02, NTF-03, SEC-05

**Betroffene WPs:** WP-1140 (`p3-ntf-user-mobile`)

<a id="e-165"></a>
## E-165 – Personenbezogene Daten im Vollexport (TLC-05)

**Schlüssel:** `d-p3-org-export-pii`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** TLC-05: „Vollexport der Org (alle Mandantentabellen als JSONL, Dokumente als ZIP) als Job; Download 24 h; für Portabilität und AVV“ (`docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:64`). Mandantendaten referenzieren Personen nur über Surrogate (SEC-05, `docs/spec/katalog-v3/06-audit-sicherheit-events.md:32`; CH16, `docs/spec/katalog-v3/00-grundlagen.md:31`). Ohne Vault-Inhalte sind Surrogate außerhalb von Reticora wertlos.

**Frage:** Enthält der Vollexport personenbezogene Werte im Klartext?

**Optionen:**

1. Export entsiegelt (Klartext an der Stelle der Surrogate) mit `org:export`, MFA-Sitzung und Audit; Download 24 h. Folgen: für den Mandanten ohne Reticora weiterverwendbar; die Exportdatei enthält PII und ist entsprechend zu schützen.
2. Surrogate plus separater, verschlüsselter Vault-Auszug. Folgen: Trennung bleibt; der Mandant braucht ein Werkzeug zum Zusammenführen, die Portabilität ist eingeschränkt.
3. Ohne personenbezogene Werte. Folgen: kein PII-Risiko; Export ist für Portabilität (Art. 20 DSGVO) unbrauchbar.

**Empfehlung:** Option 1. Nur Option 1 ist für den Mandanten ohne Reticora weiterverwendbar. Zugriff ist auf `org_admin` mit MFA begrenzt (E-159) und auditiert. Der Download-Link ist 24 h gültig und einmalig an die Sitzung des Antragstellers gebunden.

**Auswirkung:** WP-1149 bis WP-1151 liefern Job, Dokument-ZIP und API. WP-1158 und WP-1159 nennen den Export als Weg der Auskunft und weisen nach, dass gelöschte PII nicht im Export erscheint.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:64 (TLC-05); docs/spec/katalog-v3/06-audit-sicherheit-events.md:32 (SEC-05); docs/spec/katalog-v3/00-grundlagen.md:31 (CH16)

**Betroffene IDs:** TLC-05, SEC-05, CH16, RBA-05

**Betroffene WPs:** WP-1149 (`p3-tlc-export-job`), WP-1150 (`p3-tlc-export-docs`), WP-1151 (`p3-tlc-export-api`), WP-1158 (`p3-sec05-subject`), WP-1159 (`p3-sec05-residency`)

<a id="e-166"></a>
## E-166 – audit_log bei Org-Löschung gegenüber 10 Jahren Aufbewahrung (AUD-09, NFR-06)

**Schlüssel:** `d-p3-org-deletion-audit`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** TLC-06 verlangt nach 30 Tagen Karenz den „Hard-Delete inklusive S3, Vault und Suchindex“ und ein Löschprotokoll „(Zeitpunkt, Umfang, Hashes)“ an den Antragsteller und im `operator_audit` (`docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:66`). AUD-09 verlangt „audit_log 10 Jahre (konfigurierbar, mindestens gesetzliche Frist)“ (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:20`), NFR-06 ebenso (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:44`). `audit_log` ist append-only versiegelt (`backend/migrations/000188_audit_seal_append_only.up.sql`).

**Frage:** Wird `audit_log` bei der Org-Löschung gelöscht?

**Optionen:**

1. Alles löschen. Der Mandant erhält während der Karenz einen Vollexport einschließlich Audit (TLC-05); `operator_audit` behält das Löschprotokoll mit Hashes ohne Mandanten-PII. Folgen: TLC-06 wörtlich; die Aufbewahrungspflicht liegt beim Mandanten.
2. `audit_log` mit Crypto-Shredding der PII aufbewahren, alles andere löschen. Folgen: Audit bleibt beim Auftragsverarbeiter nach Vertragsende; Rechtsgrundlage dafür fehlt; die Organisation existiert als Rumpf weiter.
3. `audit_log` ungelöscht aufbewahren. Folgen: verletzt TLC-06 und SEC-05.

**Empfehlung:** Option 1. Die gesetzliche Aufbewahrung obliegt dem Verantwortlichen (Mandant); der Auftragsverarbeiter löscht nach Vertragsende. Der Export während der Karenz sichert die Pflichten des Mandanten. AUD-09 gilt für bestehende Organisationen weiter. Für das Löschen muss die Versiegelung aus 000188 eine Ausnahme für den Löschjob haben, die nur ganze Organisationen betrifft und selbst im `operator_audit` protokolliert ist.

**Auswirkung:** WP-1152 bis WP-1157 liefern Antrag, Karenz, Hard-Delete der Tabellen, Löschung in S3, Vault, Suchindex und NATS, das Löschprotokoll und die Oberfläche. Die Datenschutzdokumentation (AVV) nennt die Regel.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:64–66 (TLC-05, TLC-06); docs/spec/katalog-v3/06-audit-sicherheit-events.md:20 (AUD-09); docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:44 (NFR-06); backend/migrations/000188_audit_seal_append_only.up.sql

**Betroffene IDs:** AUD-09, NFR-06, TLC-05, TLC-06, SEC-05

**Betroffene WPs:** WP-1152 (`p3-tlc-delete-request`), WP-1153 (`p3-tlc-delete-readonly`), WP-1154 (`p3-tlc-delete-purge-db`), WP-1155 (`p3-tlc-delete-purge-external`), WP-1156 (`p3-tlc-delete-protocol`), WP-1157 (`p3-tlc-ui`)

<a id="e-167"></a>
## E-167 – Zuordnung der offenen G1/G2-Befunde zu Phase 3

**Schlüssel:** `d-p3-g2-followups`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Das Sicherheitsreview der Phase 2 führt als offen: SR-58, SR-59 und SR-60 (Medium) sowie SR-50, SR-61, SR-62 und SR-63 (Low) (`docs/security/review-phase2.md:313–321`). Die vorgeschlagenen Folge-WPs WP-854 und WP-855 sind „nicht, vor GA“ (:332–338). Aus G1 sind offen: SR-03 (Rest: 129 Verweise in `rls.ReferencesWithoutOrganization`, „Termin vor GA mit WPs einer späteren Phase (E-137, Option 2)“, :346) und SR-10 (Obergrenze für Identitätsschlüssel je Record, :351). Keiner dieser Befunde hat ein WP in einem gemergten Plan.

**Frage:** Welche offenen Befunde arbeitet Phase 3 ab, und was verlangt G3 dafür?

**Optionen:**

1. Alle als M3.1 (M3.0b) in Phase 3; G3 verlangt SR-58 bis SR-60 behoben, die übrigen behoben oder begründet. Folgen: neun WPs; alle bekannten Befunde vor GA geschlossen.
2. Nur SR-58 bis SR-60 in Phase 3, der Rest vor GA in einer späteren Phase. Folgen: weniger Aufwand; SR-03 wächst mit jeder neuen P3-Tabelle, die ohne zusammengesetzten Fremdschlüssel entsteht.

**Empfehlung:** Option 1. GA folgt nach SEQ-01 unabhängig von G3. Neue P3-Tabellen erhöhen die Zahl der Verweise (SR-03), wenn der Rest nicht vorher geschlossen ist, und die Prüflast verschärft SR-10. WP-877 leert `rls.ReferencesWithoutOrganization`; danach verlangt der Katalogtest für jede neue Tabelle den zusammengesetzten Fremdschlüssel (Konvention 19).

**Auswirkung:** WP-871 bis WP-879 in M3.1. WP-1175 übernimmt den Status in die Nachverfolgung des Reviews der Phase 3.

**Quelle:** docs/security/review-phase2.md:313–321, :326–338, :340–351; [E-137](../phase2/entscheidungen.md#e-137)

**Betroffene IDs:** SEC-11, TEN-09, GATE-01

**Betroffene WPs:** WP-871 (`p3-sr58-computed-fieldrights`), WP-872 (`p3-sr59-form-oracle`), WP-873 (`p3-sr60-thirdparty-pins`), WP-874 (`p3-sr61-62`), WP-875 (`p3-sr63-channel-rows`), WP-876 (`p3-sr03-fk-1`), WP-877 (`p3-sr03-fk-2`), WP-878 (`p3-sr10-identity-cap`), WP-879 (`p3-sr50-foreign-ref-422`)

<a id="e-168"></a>
## E-168 – Bezugsgröße und Messumgebung des Lastnachweises NFR-11

**Schlüssel:** `d-p3-load-basis`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** NFR-11: „Je SaaS-Instanz verarbeitet das Monitoring dauerhaft mindestens 353 Prüfergebnisse pro Sekunde (30,5 Mio. Prüfungen pro Tag) ohne Verlust, verteilt über mehrere Collectoren mit Jitter (PRB-04). Die Auslegung hat Reserve Faktor 3 (Ingest ≥ 1.060 Ergebnisse/s dauerhaft) und verkraftet eine Nachsynchronisationsspitze von 5.000 Ergebnissen/s (PRB-07). In allen drei Szenarien gilt: Zustandswechsel stehen p95 < 5 s nach Eingang beim Server als Ereignis bereit, p95 ≤ 10 s vom Collector-Zeitstempel bis zur Sichtbarkeit des Zustands, Alarme nach ALR-02. Die Budgets aus NFR-02 und NFR-03 bleiben unter dieser Last erfüllt. Nachweis im Lasttest vor G3 (Median über drei Läufe, Ergebnisse im Repo)“ (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:225`, neu). ALR-02 verlangt für Alarme p95 ≤ 10 s nach Eingang (:103). PRT-19 nennt für den Durchsatznachweis je Collector einen eigenen Referenz-Typmix: „vorläufig 80 % ICMP, 10 % TCP/HTTP, 10 % SNMP“ (:91). Die Mischung der Instanzlast ist offen. E-58 erkennt GitHub-Runner (4 vCPU) mit dem Median dreier Läufe als Messumgebung an; der dedizierte Referenzhost (SR-06) ist bis vor GA offen ([E-58](../entscheidungen.md#e-58), `docs/security/review-phase2.md:348`).

**Rechnung:** 353/s × 86.400 s = 30.499.200 ≈ 30,5 Mio. Prüfungen/Tag. Bei einheitlich 30 s Intervall sind das 353 × 30 ≈ 10.600 Prüfziele. 20 Collectoren tragen je 530 Ziele mit 17,65 Prüfungen/s. Mischung der Instanzlast 70 / 15 / 10 / 5 %: ICMP 247/s, TCP/HTTP 53/s, SNMP 35/s, übrige 18/s. Reserve Faktor 3: 1.060/s dauerhafter Ingest; Nachsynchronisation 5.000/s. Die Mischung gilt für Ziele und Rate gleichermaßen, weil der Last-Seed alle Typen mit 30 s Intervall anlegt (zulässig nach den Mindestintervallen aus E-154). Mit Standardintervallen je Typ wären für dieselbe Rate ≈ 13.800 Ziele nötig. Bei 3 Echo-Requests je ICMP-Prüfung (E-177) sendet ein Collector ≈ 37 ICMP-Pakete/s.

**Frage:** Worauf bezieht sich die Last, mit welcher Mischung und wo wird gemessen?

**Optionen:**

1. 353/s je Instanz über alle Organisationen; Instanzlast-Mischung 70 % ICMP, 15 % TCP/HTTP, 10 % SNMP, 5 % übrige; 20 simulierte Collectoren mit Jitter; drei Szenarien nach NFR-11 (353/s, 1.060/s dauerhaft, Nachsynchronisation 5.000/s); Messung wie E-58 (GitHub-Runner, Median dreier Läufe). Folgen: reproduzierbar auf der vorhandenen Messumgebung; die Mischung ist vom Auftraggeber zu bestätigen.
2. 353/s je Organisation. Folgen: bei mehreren großen Organisationen ein Vielfaches je Instanz; auf 4 vCPU nicht nachweisbar, und NFR-11 sagt „je Instanz“.
3. Messung auf dediziertem Referenzhost (SR-06). Folgen: aussagekräftigere Absolutwerte; der Host fehlt und ist nach E-58 erst vor GA vorgesehen.

**Empfehlung:** Option 1. Sie entspricht der Formulierung „je SaaS-Instanz“ und ist auf der vorhandenen Messumgebung reproduzierbar. Gemessen werden je Szenario: Zustandswechsel p95 < 5 s nach Eingang als Ereignis, p95 ≤ 10 s vom Collector-Zeitstempel bis zur Sichtbarkeit, Alarme nach ALR-02 p95 ≤ 10 s. Die Collector-Seite wird getrennt nachgewiesen: WP-1023 misst nach PRT-19, dass ein einzelner Collector 353/s gegen Simulatoren trägt, und nutzt dafür denselben vorläufigen Typmix 70/15/10/5 wie WP-1084; den PRT-19-Wert 80 % ICMP, 10 % TCP/HTTP, 10 % SNMP fährt WP-1023 als zusätzliche Variante. WP-958 bleibt beim ICMP-Budget. Damit kann ein verbleibendes Gruppenmitglied die gesamte Last einer Gruppe übernehmen. Beide Mischungen (70/15/10/5 und die PRT-19-Variante 80/10/10) sind vorläufig; WP-1023 und WP-1084 hängen an Aufgabe A-60 (Typmix).

**Auswirkung:** WP-1162 legt den Last-Seed (10.600 Ziele, 20 Collectoren, 30 s, Instanzlast-Mischung 70/15/10/5) an. WP-1084 weist die Last mit diesem Typmix nach, WP-1023 die Collector-Last mit demselben Typmix 70/15/10/5 und zusätzlich mit der PRT-19-Variante 80/10/10; WP-958 bleibt beim ICMP-Budget. WP-1023 und WP-1084 hängen an Aufgabe A-60 (Typmix). WP-1163 seedet `probe_result_1h` und `probe_result_1d` über 400 Tage und misst Historie und Speicherbedarf unter Last. WP-1167 fährt einen Dauerlauf ≥ 24 h mit 353/s realem Ingest (Ingest-Lag, Kompression, WAL, Speicher je Tag) und schreibt `docs/acceptance/lasttest-g3.md` (neu) nach E-58 mit G2-Regression.

**Quelle:** `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:225` (neu, NFR-11), :91 (neu, PRT-19), :103 (neu, ALR-02); [E-58](../entscheidungen.md#e-58); docs/security/review-phase2.md:348 (SR-06); docs/acceptance/lasttest-g2.md

**Betroffene IDs:** NFR-02, NFR-03, NFR-11, PRT-19, ALR-02, ABN-02, SIM-01, GATE-06

**Betroffene WPs:** WP-958 (`p3-coll-capacity`), WP-1023 (`p3-pt-bench`), WP-1084 (`p3-load-proof`), WP-1162 (`p3-seed-scale`), WP-1163 (`p3-load-history`), WP-1167 (`p3-perf`)

<a id="e-169"></a>
## E-169 – Zusatzkriterien für G3 (GATE-06) und externer Pentest

**Schlüssel:** `d-p3-gate-criteria`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** GATE-03 regelt nur G1 (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:89`), GATE-04 nur GA (:91). SEC-11 verlangt „Pentest vor G1 und GA“ (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:44`). GATE-06 (neu) verlangt für G3: Lastnachweis NFR-11, Ausfalltests (Collector-Ausfall als Netzdown, Umschaltung, Pufferung und Nachsynchronisation), ADMP-Test „nur GET“, Sicherheitsreview Phase 3 ohne offenes Critical oder High und Isolationstest der P3-Kanäle. Die P3-Angriffsfläche ist neu: Collector-Netz mit Prüfauftrag, signierte Rückrufe der Pager-Provider, ADMP, Tresoranbindung, Operator-Oberfläche, Org-Export und Org-Löschung.

**Frage:** Verlangt G3 zusätzlich einen externen Pentest?

**Optionen:**

1. GATE-06 wie vorgeschlagen; internes Review nach ASVS L2; kein externer Pentest für G3. Folgen: Gate ohne externe Abhängigkeit; die P3-Fläche wird extern erst mit dem Pentest vor GA geprüft.
2. Zusätzlich externer Pentest der P3-Angriffsfläche (Collector-Netz, Rückrufe, ADMP, Tresor) vor G3. Folgen: frühere externe Prüfung; Vergabe, Termin und Kosten liegen beim Auftraggeber und können G3 verzögern.

**Empfehlung:** Option 1. Konsistent mit SEC-11: Der externe Pentest vor GA deckt die P3-Fläche, wenn GA nach G3 liegt. Liegt GA vor G3, gilt Option 2. WP-1174 prüft die neuen Flächen gezielt (Rückruf-Signaturen, Sitzungstrennung, Export und Löschung mit MFA, Egress), WP-1175 dokumentiert das Review.

**Auswirkung:** WP-1174 bis WP-1177 setzen GATE-06 um; WP-1177 nimmt die Kriterien in die Checkliste `docs/acceptance/g3-gate.md` (neu) auf.

**Quelle:** docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:89–91 (GATE-03, GATE-04); docs/spec/katalog-v3/06-audit-sicherheit-events.md:44 (SEC-11); `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:229` (neu, GATE-06), :231 (neu, ABN-04)

**Betroffene IDs:** GATE-01, GATE-03, GATE-04, GATE-06, SEC-11, ABN-04

**Betroffene WPs:** WP-1174 (`p3-security-tests`), WP-1175 (`p3-security-review`), WP-1176 (`p3-abnahme`), WP-1177 (`p3-gate`)

<a id="e-170"></a>
## E-170 – Release-Standard G3

**Schlüssel:** `d-p3-release-default`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Seit WP-830 ist der leere Wert von `RETICORA_MODULES` der Release-Standard G2: „every module with phase P2 on and the modules of P3, P4 and the add-ons off, the same as the phase value P2“ (`backend/internal/server/modules.go:124–131`, `PhaseP2` :48). P3-Module sind heute nur einzeln per `RETICORA_MODULES` schaltbar. Die Wirkung je Organisation regeln unabhängig davon die Entitlements.

**Frage:** Schaltet der Release-Standard ab G3 die P3-Module ein?

**Optionen:**

1. Ab G3 schaltet der leere Wert P2- und P3-Module ein, P4 und [A] bleiben aus; neuer Phasenwert `P3` = P2 + P3. Folgen: P3-Funktionen ohne Betriebskonfiguration verfügbar, sofern das Paket sie enthält; Installationen mit expliziter Liste bleiben unverändert.
2. P3-Module bleiben Opt-in. Folgen: kein Verhaltenswechsel beim Update; jede Installation muss Monitoring und Workflows einzeln einschalten, obwohl die Pakete sie enthalten.

**Empfehlung:** Option 1. Gleiches Muster wie WP-830. Die Wirkung je Organisation regeln weiter die Entitlements. Der Wechsel erfolgt erst mit WP-1166 nach dem Lastnachweis; bis dahin bleibt der Release-Standard G2 (E-148).

**Auswirkung:** WP-863 führt den Phasenwert `P3` ein, WP-1166 schaltet den Standard um, WP-1170 prüft Install- und k8s-Smoke mit P3-Modulen.

**Quelle:** backend/internal/server/modules.go:48, :124–159; docs/plan/phase2/implementierungsplan.md (WP-830)

**Betroffene IDs:** API-06, OPS-02, RBA-05, INS-01

**Betroffene WPs:** WP-863 (`p3-module-gates`), WP-1166 (`p3-release`), WP-1170 (`p3-install`)

<a id="e-171"></a>
## E-171 – Speicherengine und Tabellenmodell der Probe-Ergebnisse

**Schlüssel:** `d-mon-probe-store`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Bei 353/s fallen 30,5 Mio. Ergebnisse/Tag an. `metric_sample` ist breit: `metric_name TEXT` und `labels JSONB` je Zeile, keine Idempotenz (`backend/migrations/000020_metrics.up.sql:6–13`); MON-01 verlangt dafür 400 Tage Rohdaten (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:4`). ADR 0001 belegt: RLS ist mit Kompression und Continuous Aggregates in TimescaleDB unvereinbar; die Mandantentrennung läuft deshalb über Security-Barrier-Views, die App-Rolle hat keine Rechte auf der Hypertable (`docs/decisions/0001-timescale-rls.md:37–60`). Konvention 5 und 19 verlangen für neue Tabellen RLS/FORCE RLS, truncate guard und zusammengesetzte Fremdschlüssel; eine komprimierte Hypertable kann das nach ADR 0001 nicht.

**Rechnung:** Schmale Zeile: `time` 8 B, `organization_id`, `probe_id`, `collector_id` je 16 B, `seq` 8 B, `status` und `code` je 2 B, `rtt_ms` 4 B, `loss` 2 B, `value` 8 B ≈ 82 B Nutzdaten plus 24 B Tupelkopf und Ausrichtung ≈ 110 B; Eindeutigkeitsindex ≈ 40–60 B. Zusammen ≈ 150 B/Zeile. 30,5 Mio. × 150 B ≈ 4,6 GB/Tag unkomprimiert. Ein 6-h-Chunk hat 7,6 Mio. Zeilen ≈ 1,15 GB einschließlich Index und passt bei 8 GB RAM in 25 % des Arbeitsspeichers (Richtwert TimescaleDB: aktiver Chunk mit Indizes ≤ 25 % RAM). Schreibrate: 353 Zeilen/s. Bei 20 Collectoren und Bündeln von höchstens 10 s (PRB-05) sind das ≈ 2 Bulk-Inserts/s mit je ≈ 177 Zeilen. Variante `metric_sample`: Name und JSONB-Labels mit `probe_id` ≈ 200–250 B je Zeile, und Status, RTT und Verlust brauchen drei Zeilen je Prüfung ≈ 600–750 B je Ergebnis, also ≈ 18–23 GB/Tag, Faktor 4–5 gegenüber dem schmalen Schema.

**Frage:** Wo und in welchem Schema werden die Probe-Ergebnisse gespeichert?

**Optionen:**

1. Eigene Hypertable `probe_result` (`time`, `organization_id`, `probe_id`, `collector_id`, `seq`, `status` mit 0 = ok, 1 = fail, 2 = error nach `probewire.Result.status`, `code`, `rtt_ms`, `loss`, `value`) in TimescaleDB; Chunks 6 h; Kompression ab 1 Tag (`segmentby organization_id, probe_id`); Zugriff wie ADR 0001 ausschließlich über die Security-Barrier-View `probe_result_v`; `UNIQUE (probe_id, collector_id, seq, time)` für Idempotenz (die Partitionsspalte `time` muss im eindeutigen Index stehen). Zustand separat in `probe_state` und `probe_state_change` als normale Tabellen mit RLS/FORCE RLS, truncate guard und zusammengesetzten Fremdschlüsseln. Folgen: eine Engine, bestehender Betrieb (Backup, Restore-Test, Isolation per View); `probe_result` ist wie `metric_sample` eine dokumentierte Ausnahme von Konvention 5 im RLS-Katalogtest.
2. `metric_sample` weiterverwenden (Metrikname `icmp_rtt` usw., Probe-ID in `labels`). Folgen: kein neues Schema; Faktor 4–5 mehr Volumen, keine Deduplizierung bei Nachsynchronisation, 400 Tage Rohdaten nach MON-01.
3. VictoriaMetrics oder ein anderer TSDB-Dienst. Folgen: effiziente Zeitreihen; zweite Datenbank mit eigenem Backup, eigener Mandantentrennung und eigenem Betrieb; widerspricht dem Betriebsmodell (TEC-12 sieht weitere Dienste erst in P5 vor).

**Empfehlung:** Option 1. Sie hält eine Engine und den bestehenden Betrieb. Durch Idempotenz und das schmale Schema ist sie um Faktor 4–5 kleiner als Option 2. Option 2 hat keine Deduplizierung und braucht JSONB-Labels je Zeile. Option 3 widerspricht dem Betriebsmodell. Die Chunk-Größe von 6 h hält einen Chunk bei ca. 1,2 GB. Der Spike in WP-943 (ADR 0002) bestätigt oder korrigiert Chunk-Größe und Zeilenbreite durch Messung.

**Auswirkung:** WP-943 dokumentiert Rechnung und Messung in `docs/decisions/0002-probe-store.md` (neu). WP-945 enthält die Migration `backend/migrations/<nächste Nr.>_probe_result.up.sql` und `.down.sql` mit `docs/schema-baseline.md` sowie den Eintrag der Ausnahme im RLS-Katalogtest. WP-960 schreibt per `unnest` mit `ON CONFLICT DO NOTHING` über `probe_result_v`.

**Quelle:** backend/migrations/000020_metrics.up.sql:6–21; docs/decisions/0001-timescale-rls.md:37–60; docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:4 (MON-01); docs/spec/katalog-v3/01-installation-stack.md:6 (TEC-06), :10 (TEC-12); `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:41` (neu, PRB-05), :43 (neu, PRB-06), :45 (neu, PRB-07)

**Betroffene IDs:** MON-01, NFR-06, NFR-11, PRB-05, PRB-06, PRB-07, TEC-06, TEC-12, TEN-09

**Betroffene WPs:** WP-943 (`p3-mon-storage-adr`), WP-945 (`p3-probe-schema-result`), WP-960 (`p3-probe-ingest-worker`)

<a id="e-172"></a>
## E-172 – Rohdatenaufbewahrung und Aggregationsstufen

**Schlüssel:** `d-mon-retention-tiers`

**Status:** entschieden (2026-11-09, Option 1; Beschluss Punkt 4)
**Kontext:** Der Auftraggeber verlangt 400 Tage Verfügbarkeitshistorie sowie definierte Rohdatenaufbewahrung und Aggregation. PRB-06: „Rohergebnisse 14 Tage; Aggregate 5 min 90 Tage, 1 h 400 Tage, 1 d 400 Tage; Zustandswechsel und tägliche Verfügbarkeit je Probe 400 Tage. Kompression der Rohdaten ab 1 Tag. Die Verfügbarkeit wird aus Zustandsintervallen sekundengenau berechnet, nicht aus Stichproben.“ (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:43`, neu). NFR-06 nennt „Metriken 400 Tage“ (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:44`), MON-01 400 Tage für `metric_sample` (:4). 400 Tage Rohdaten wären 30,5 Mio. × 400 ≈ 12,2 Mrd. Zeilen.

**Rechnung:** Grundlage 10.600 Probes (E-168), 150 B je Rohzeile (E-171), ≈ 120 B je Aggregatzeile (Anzahl, Erfolge, RTT min/avg/max, Verlust), Kompressionsfaktor 10–15 (Spike WP-943).

| Stufe | Zeilen/Tag | Aufbewahrung | Zeilen gesamt | Speicher |
|---|---:|---:|---:|---:|
| Roh | 30,5 Mio. | 14 T | 427 Mio. | 4,6 GB (heute, unkomprimiert) + 13 × 0,3–0,46 GB ≈ 8,5–10,6 GB |
| 5 min | 10.600 × 288 = 3,05 Mio. | 90 T | 275 Mio. | 33 GB unkomprimiert, ≈ 2,2–3,3 GB komprimiert |
| 1 h | 10.600 × 24 = 254.400 | 400 T | 102 Mio. | 12,2 GB unkomprimiert, ≈ 0,8–1,2 GB komprimiert |
| 1 d | 10.600 | 400 T | 4,24 Mio. | ≈ 0,5 GB |
| Zustandswechsel | ≤ 106.000 (obere Annahme 10 je Probe) | 400 T | ≤ 42 Mio. | ≤ 6,4 GB (normale Tabelle mit RLS) |
| Tagesverfügbarkeit | 10.600 | 400 T | 4,24 Mio. | ≈ 0,35 GB |

Summe Option 1 im eingeschwungenen Zustand ≈ 19–23 GB. Option 2: 12,2 Mrd. × 150 B ≈ 1,83 TB unkomprimiert, komprimiert (10–20×) ≈ 92–183 GB, dazu WAL und Backup im selben Verhältnis. Option 3 spart gegenüber Option 1 ≈ 2–3 GB Rohdaten und die 5-min-Stufe (≈ 3 GB).

**Frage:** Wie lange bleiben Rohdaten, und welche Aggregationsstufen gibt es?

**Optionen:**

1. Roh 14 T (komprimiert ab 1 T), 5 min 90 T, 1 h 400 T, 1 d 400 T; Zustandswechsel und Tagesverfügbarkeit 400 T. Folgen: Rechnung oben; Störungsanalysen mit Einzelwerten über 2 Wochen, Trend und Prognose über 400 Tage.
2. Roh 400 T wie `metric_sample`. Folgen: 92–183 GB Speicher plus WAL und Backup ohne fachlichen Mehrwert gegenüber Option 1.
3. Roh 7 T, nur 1 h und 1 d je 400 T. Folgen: kleiner; zu grob für die Analyse von Flapping und kurzen Ausfällen nach der ersten Woche.

**Empfehlung:** Option 1. Sie entspricht PRB-06 wörtlich. Störungsanalysen mit Einzelwerten sind über 2 Wochen möglich, Trendauswertung und Kapazitätsprognose über 400 Tage. Die Verfügbarkeit kommt exakt aus den Zustandswechseln, nicht aus Stichproben. Option 2 kostet ohne fachlichen Mehrwert eine Größenordnung mehr Speicher, Option 3 ist zu grob für Flapping-Analysen. Dass NFR-06 („Metriken 400 Tage“) für Probe-Ergebnisse mit den Aggregaten erfüllt ist, legt der Beschluss unten fest. Für `metric_sample` bleibt MON-01 unverändert.

**Beschluss vom 2026-11-09 (Punkt 4):** Option 1, die fachlich sinnvollste Lösung. NFR-06 („Metriken 400 Tage“) gilt damit als erfüllt: `metric_sample` behält nach MON-01 400 Tage Rohdaten für Metrik-Probes (SNMP, Redfish, Optik). Für Erreichbarkeitsprüfungen erfüllen die Stufen 1 h und 1 d sowie Zustandswechsel und Tagesverfügbarkeit die 400 Tage. Rohergebnisse bleiben 14 Tage.

**Auswirkung:** WP-943 dokumentiert die Stufen, WP-945 setzt die Retention der Rohdaten, WP-972 legt die Continuous Aggregates mit Refresh-, Kompressions- und Retention-Policies an, WP-974 die Retention-Jobs für Zustandswechsel und Tagesverfügbarkeit. WP-975 wählt die Stufe nach Zeitraum. WP-1084 misst Volumen und Schreibrate unter Last.

**Quelle:** `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:43` (neu, PRB-06); docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:4 (MON-01), :44 (NFR-06); docs/decisions/0001-timescale-rls.md:37–49

**Betroffene IDs:** MON-01, MON-03, NFR-06, NFR-11, PRB-06, PRB-07

**Betroffene WPs:** WP-943 (`p3-mon-storage-adr`), WP-945 (`p3-probe-schema-result`), WP-972 (`p3-probe-cagg`), WP-974 (`p3-probe-retention`), WP-975 (`p3-probe-query-api`), WP-1084 (`p3-load-proof`)

<a id="e-173"></a>
## E-173 – Übertragungsweg Collector → Server für Zustandswechsel und Messwerte

**Schlüssel:** `d-mon-transport`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Metriken laufen heute synchron per Request/Reply über core-NATS. `EdgeMetrics` schreibt im Callback (`backend/internal/discovery/edge_ingest.go:357–398`), `ServeCollectorEdge` beantwortet die Anfrage erst danach (:462–495). Es gibt keine Deduplizierung. Der Collector hält Metrik-Batches nur im Speicher und verwirft die ältesten bei Überlauf (`collector/collectorcmd/edge.go:149–157`). Records gehen dagegen über den JetStream-Stream `RETICORA_INGEST` mit Durable Consumern und explizitem Ack (`backend/internal/discovery/consumer.go:657–725`). Jeder Collector darf nur unter seinem Subject-Baum publizieren (`backend/internal/discovery/natsedge.go:71–74`, Rechte :238–239). COL-08 erlaubt eingehend nur 443 und 4222 (`docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:18`). `edgecore/buffer` ist ein persistenter Plattenpuffer, den der Collector bisher für Discovery-Uploads nutzt (`collector/collectorcmd/collectorcmd.go:288`). PRB-07 verlangt 24 h persistenten Puffer ohne Verlust und ohne Doppelzeilen.

**Rechnung:** 353 Ergebnisse/s; Bündel höchstens 10 s oder 1.000 Ergebnisse (PRB-05). Bei 20 Collectoren ≈ 2 Nachrichten/s mit je ≈ 177 Ergebnissen, dazu ≈ 1,2 Zustandswechsel/s (E-161) und 2 Lebenszeichen/s (E-176). Stream-Volumen bei ≈ 80 B je kodiertem Ergebnis: 30,5 Mio. × 80 B ≈ 2,4 GB/Tag. Der Stream ist eine Work-Queue und hält nur Unbestätigtes; Grenze 24 h bzw. 5 GB.

**Frage:** Auf welchem Weg gelangen Zustandswechsel und Messwerte verlustfrei und idempotent zum Server?

**Optionen:**

1. Edge-Request/Reply auf neuen Subjects `.probe.state`, `.probe.results`, `.probe.alive`. Der Server legt die Nachricht in den JetStream-Stream `RETICORA_PROBE` (`Nats-Msg-Id` = `collector:epoch:seq`) und antwortet erst nach dem PubAck. Durable Consumer schreiben gebündelt in die DB. Im Collector liegt ein persistenter Spool auf `edgecore/buffer` mit Sequenz. Folgen: vorhandener mTLS-Kanal und Subject-Autorisierung; Annahme und DB-Last entkoppelt; Ende-zu-Ende ohne Verlust.
2. Collector publiziert direkt in einen JetStream-Stream des Accounts `COLLECTORS` (JetStream-Domain am Edge). Folgen: ein Hop weniger; JetStream-Kontingente und Stream-Verwaltung je Collector-Account am Edge, Mandantentrennung im Stream statt im Server.
3. HTTP/gRPC-Streaming an die API. Folgen: kein NATS-Pfad; widerspricht COL-08 (zusätzlicher Port bei gRPC); HTTP-Uploads des Collectors kamen ohne Credential nie an (F-046, `docs/plan/g1-korrektur/abdeckung-befunde.csv:47`), seither läuft der Collector-Verkehr über den mTLS-Edge.

**Empfehlung:** Option 1. Sie nutzt den vorhandenen mTLS-Kanal und die Subject-Autorisierung und entkoppelt Annahme und DB-Last über JetStream wie bei den Records. Das Ack gilt erst nach persistenter Annahme. Collector-Spool und Server-Stream ergeben zusammen eine Ende-zu-Ende-Zustellung ohne Verlust. Die Deduplizierung greift zweifach: im Stream über `Nats-Msg-Id` innerhalb des Dedup-Fensters, in der DB über den eindeutigen Index aus E-171.

**Auswirkung:** WP-933 legt den Wire-Vertrag fest, WP-938 das OpenAPI-Gerüst (u. a. Schema `MonitoringEvent` nach WP-979), WP-948 die Verteilung an den Collector. WP-955 sendet, WP-959 nimmt am Edge an, WP-960 schreibt in die DB.

**Quelle:** backend/internal/discovery/edge_ingest.go:357–398, :462–495; backend/internal/discovery/consumer.go:657–725; backend/internal/discovery/natsedge.go:71–74, :238–239; collector/collectorcmd/edge.go:149–157; collector/collectorcmd/collectorcmd.go:288; edgecore/buffer/buffer.go; docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:18 (COL-08); `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:41` (neu, PRB-05), :45 (neu, PRB-07)

**Betroffene IDs:** COL-08, PRB-05, PRB-07, PRB-11, NFR-11

**Betroffene WPs:** WP-933 (`p3-probe-wire`), WP-938 (`p3-mon-api-contract`), WP-948 (`p3-probe-distribution`), WP-955 (`p3-coll-uplink`), WP-959 (`p3-probe-edge`), WP-960 (`p3-probe-ingest-worker`)

<a id="e-174"></a>
## E-174 – Redundanzmodell der Collector-Gruppen und Collector-Ausfall als Ursache

**Schlüssel:** `d-mon-redundancy` (eingearbeitet: `d-alr-collector-down`)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1, mit Gruppenmodus `failover` als Einstellung je Gruppe)
**Kontext:** Der Auftraggeber verlangt redundante Collectoren und die Behandlung eines Collector-Ausfalls wie einen Netzausfall. Eine Organisation kann heute mehrere Collectoren betreiben (Quote `max_collectors`), sie bilden aber keine Gruppe. PRB-03: Jede Probe gehört genau einer Collector-Gruppe; der Server weist sie genau einem gesunden Mitglied zu; Übernahme innerhalb von 60 s ab dem letzten Lebenszeichen; ist kein Mitglied erreichbar, wechseln alle Probes der Gruppe nach `unknown` mit Grund `collector_unreachable`, und es entsteht genau ein Root-Ereignis `collector_group.unreachable`. ALR-03: „Ist kein Mitglied einer Collector-Gruppe erreichbar (PRB-03), entsteht eine Störung mit Ursache collector_unreachable; die Probes der Gruppe werden wie bei einem Netzausfall als Symptome geführt.“ (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:105`, neu). COL-06 [B] nennt bereits „mehrere Collectors pro Standort“ (`docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:14`); die Gruppen bauen darauf auf. Split-Brain muss ausgeschlossen sein.

**Frage (a):** Wie verteilt eine Gruppe ihre Probes?

**Optionen:**

1. Verteilt aktiv/aktiv: jede Probe genau einem gesunden Mitglied per Rendezvous-Hashing (gewichtet nach Kapazität). Der Server ist alleiniger Schiedsrichter, je Zuweisung gibt es eine Epoche als Fencing-Token. Bei Ausfall werden nur die Probes des ausgefallenen Mitglieds umverteilt. Folgen: Last und Ausfallwirkung verteilt; keine Abstimmung zwischen Collectoren.
2. Aktiv/passiv: ein Mitglied prüft alles, der Standby übernimmt komplett. Folgen: einfach; der Standby ist ungenutzte Kapazität, und der Failover verschiebt die gesamte Last auf einmal.
3. Aktiv/aktiv doppelt: alle Mitglieder prüfen alles, der Zustand ergibt sich per Quorum. Folgen: kein Failover nötig; doppelte Prüflast (706/s) und doppeltes Volumen.

**Frage (b), aus `d-alr-collector-down`:** Wie wirkt ein Collector-Ausfall auf Checks und Alarme?

1. Alle Checks des Collectors werden `unknown`, kein Alarm.
2. Checks werden als nicht erreichbar mit Ursache `collector_unreachable` geführt; sobald kein redundantes Mitglied der Gruppe sie übernimmt, entsteht eine Störung mit Wurzel Collector-Gruppe bzw. Standort-Uplink.
3. Wie 2, aber Kinder einzeln alarmieren.

**Empfehlung:** (a) Option 1, Option 2 als Gruppenmodus `failover` für Kunden mit dediziertem Standby. Option 1 verteilt Last und Ausfallwirkung und braucht keine Abstimmung zwischen Collectoren, weil der Server entscheidet. Ein abgeschnittener Collector prüft weiter und puffert, seine Ergebnisse mit alter Epoche entscheiden aber keinen Zustand. Option 3 verdoppelt Prüflast und Volumen und bleibt als spätere Option je Probe offen. (b) Option 2 mit folgender Abbildung, die PRB-03 und ALR-03 zusammenführt: Auf Probe-Ebene gilt `unknown` mit Grund `collector_unreachable` (PRB-03; der Down-Count läuft nicht, es entsteht kein `down`-Ereignis einzelner Probes). Auf Alarm-Ebene entsteht genau eine Störung mit Ursache `collector_unreachable` und Wurzel Collector-Gruppe (ALR-03); die Alarme der betroffenen Probes sind ihre Symptome (Unterdrückungsgrund `symptom`) und werden nicht einzeln benachrichtigt. Das entspricht „wie Netzdown“ und verhindert eine Alarmflut. Wie `unknown` in der SLA zählt, regelt [E-181](entscheidungen.md#e-181).

**Auswirkung:** WP-936 legt Gruppen, Mitglieder und Zuweisungen an (RLS/FORCE RLS, truncate guard, zusammengesetzte Fremdschlüssel). WP-941 liefert die Zuweisungs-Engine mit Epoche, WP-947 die REST-API, WP-963 Liveness und Failover. WP-1027 führt `collector_unreachable` in `cause_class` (`topology`, `collector_unreachable`, `unknown`, ergänzt um `config_change` aus WP-1041), WP-1029 bildet die Störung mit Ursache `collector_unreachable`. WP-936, WP-947 und WP-1081 tragen COL-06 („mehrere Collectors pro Standort“) als Teil; WP-1081 zeigt die Gruppen, WP-1084 misst Failover und Netzdown unter Last.

**Quelle:** `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:37` (neu, PRB-03), :51 (neu, PRB-11), :105 (neu, ALR-03); docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:12 (COL-05), :14 (COL-06)

**Betroffene IDs:** COL-06, PRB-03, PRB-10, PRB-11, ALR-03, NFR-11, GATE-06

**Betroffene WPs:** WP-936 (`p3-collector-group-schema`), WP-941 (`p3-assign-engine`), WP-947 (`p3-collector-group-api`), WP-963 (`p3-group-liveness`), WP-1027 (`p3-inc-schema`), WP-1029 (`p3-rca-collector`), WP-1081 (`p3-ui-collector-groups`), WP-1084 (`p3-load-proof`)

<a id="e-175"></a>
## E-175 – Uhrzeit und Reihenfolge

**Schlüssel:** `d-mon-clock-order`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Eine Nachsynchronisation nach einem Ausfall liefert Daten mit Stunden Verspätung (PRB-07: Puffer mindestens 24 h). Mehrere Collectoren einer Gruppe haben unterschiedliche Uhren. Nach einem Failover meldet ein anderer Collector dieselbe Probe mit eigener Sequenz. PRB-05 verlangt je Zustandswechsel Sequenznummer, Epoche, Grund, Zeitstempel und letztes Ergebnis. Die Zustandsentscheidung muss unabhängig von der Eingangsreihenfolge eindeutig sein.

**Frage:** Was bestimmt Reihenfolge und Fachzeit eines Ergebnisses?

**Optionen:**

1. Reihenfolge über (Epoche, Sequenz je Collector); Fachzeit ist `observed_at` des Collectors. Die Uhrabweichung wird über das Lebenszeichen gemessen, ab 2 s gewarnt; ab 60 s gilt das Mitglied als `degraded`, und seine Zeitstempel werden korrigiert und markiert (PRB-09). Folgen: Historie bleibt zeitrichtig; Zustandsentscheidung je Probe eindeutig, weil je Epoche nur ein Collector zuständig ist.
2. Empfangszeit des Servers als Fachzeit. Folgen: einfach; nachgereichte Historie landet vollständig auf dem Sync-Zeitpunkt, Verfügbarkeit und SLA werden falsch.
3. Hybrid Logical Clock je Collector. Folgen: kausale Ordnung über Collectoren; kein Gewinn gegenüber (Epoche, Sequenz), weil der Server je Probe und Epoche nur einen zuständigen Collector hat.

**Empfehlung:** Option 1. Bei Option 2 wäre die nachgereichte Historie wertlos, und die Verfügbarkeit wäre falsch. Option 3 bringt keinen Gewinn gegenüber Sequenz plus Epoche. Für NTP-pflichtige Collector-Hosts sind 2 s Toleranz realistisch. Ab 60 s Abweichung korrigiert der Server `observed_at` um die gemessene Abweichung und markiert die Zeile; das Ereignis `collector.clock_skew` meldet den Zustand.

**Auswirkung:** WP-933 legt Epoche, Sequenz und `observed_at` im Wire-Vertrag fest, WP-944 die Spalten in `probe_state` und `probe_state_change`. WP-952 zählt im Collector, WP-961 wendet nach (Epoche, Sequenz) an, WP-962 misst und korrigiert die Uhrabweichung.

**Quelle:** `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:41` (neu, PRB-05), :45 (neu, PRB-07), :47 (neu, PRB-09), :51 (neu, PRB-11)

**Betroffene IDs:** PRB-05, PRB-07, PRB-09, PRB-11

**Betroffene WPs:** WP-933 (`p3-probe-wire`), WP-944 (`p3-probe-schema-state`), WP-952 (`p3-coll-statemachine`), WP-961 (`p3-probe-state-apply`), WP-962 (`p3-clock-skew`)

<a id="e-176"></a>
## E-176 – Ausfallerkennung eines Collectors für das Monitoring

**Schlüssel:** `d-mon-liveness`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** COL-05 setzt einen Collector erst nach 5 min ohne Heartbeat auf `offline` (`docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:12`; `OfflineAfter`, `backend/internal/discovery/collector_status.go:38–40`). Ein Gerät gilt nach Down-Count 5 bei 30 s Intervall als down. PRB-03 verlangt die Übernahme durch andere Mitglieder innerhalb von 60 s ab dem letzten Lebenszeichen, PRB-10 ein Lebenszeichen alle 10 s und einen Liveness-Timeout je Gruppe (Standard 30 s, 20–300 s) unabhängig von COL-05. Wird ein Collector-Ausfall langsamer erkannt als ein Gerätedown, meldet das Monitoring alle Geräte hinter dem ausgefallenen Collector als down (Fehlalarme statt Netzdown).

**Rechnung:** Gerätedown frühestens nach 4 × 30 s + 10 s Timeout = 130 s nach dem ersten Fehlschlag, mit Phasenlage bis 160 s. Collector-Ausfall mit Option 1: Lebenszeichen alle 10 s, Liveness-Timeout 30 s, Zuweisungs-Tick 10 s, also Erkennung ≤ 40 s. Neuzuweisung und Push an das übernehmende Mitglied (< 5 s) ergeben ≤ 45 s, innerhalb der 60 s aus PRB-03. Last: 20 Collectoren / 10 s = 2 Lebenszeichen/s.

**Frage:** Woran erkennt das Monitoring einen ausgefallenen Collector?

**Optionen:**

1. Eigenes Monitoring-Lebenszeichen alle 10 s; Liveness-Timeout je Gruppe (Standard 30 s); Zuweisungs-Tick 10 s. Folgen: Erkennung ≤ 40 s, sicher vor dem Gerätedown (≥ 130 s).
2. COL-05-Status (5 min) verwenden. Folgen: kein neuer Mechanismus; der Collector-Ausfall wird nach dem Gerätedown erkannt, jede Probe meldet zuerst `down`.
3. Lebenszeichen nur implizit über Ergebnis-Batches. Folgen: kein Zusatzverkehr; versagt bei Gruppen ohne Probes und bei Pufferstau (Batches bleiben aus, obwohl der Collector lebt).

**Empfehlung:** Option 1. Nur sie erkennt einen Collector-Ausfall (≤ 40 s) sicher vor einem Gerätedown (≥ 130 s) und hält damit die Netzdown-Semantik ein. Option 3 versagt bei Gruppen ohne Probes und bei Pufferstau. COL-05 bleibt für den Betriebsstatus (`offline`) unverändert. Der Liveness-Timeout je Gruppe ist nach PRB-10 im Bereich 20–300 s einstellbar. Die Übernahme in 60 s nach PRB-03 gilt für Timeouts bis 45 s (Timeout + 10 s Tick + 5 s Push ≤ 60 s). Längere Timeouts sind eine bewusste Einstellung des Mandanten, etwa für Standorte mit instabiler Anbindung; die Oberfläche (WP-1081) weist auf die verlängerte Übernahmezeit hin.

**Auswirkung:** WP-936 speichert den Liveness-Timeout je Gruppe, WP-957 sendet das Lebenszeichen, WP-963 bewertet die Mitglieder und verteilt um, WP-964 setzt die Netzdown-Semantik um.

**Quelle:** docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:12 (COL-05); backend/internal/discovery/collector_status.go:38–40; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:37` (neu, PRB-03), :49 (neu, PRB-10)

**Betroffene IDs:** COL-05, PRB-03, PRB-10, GATE-06

**Betroffene WPs:** WP-936 (`p3-collector-group-schema`), WP-957 (`p3-coll-alive`), WP-963 (`p3-group-liveness`), WP-964 (`p3-netdown`)

<a id="e-177"></a>
## E-177 – Ausführung einer Prüfung, Fehlerabbildung und Ort der Zustandsbewertung

**Schlüssel:** `d-mon-ping-semantics` (eingearbeitet: `d-pt-error-state`, `d-alr-state-source`)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1, alle Werte überschreibbar)
**Kontext:** PRT-02: „Standardwerte Intervall 30 s, Timeout 10 s, Down-Count 5, Up-Count 1; über die Hierarchie aus PRB-01 überschreibbar in den Grenzen Intervall 10 s–1 h, Timeout 1–10 s und < Intervall, Down-Count 1–20, Pakete je Prüfung 1–5, Payload 0–1472 Byte.“ (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:57`, neu). PRB-05 nennt als Gründe für `unknown` „u. a. collector_unreachable, unsupported, limit_exceeded, auth_failed“ (:41). Offen sind Pakete je Prüfung, `degraded`-Schwellen und ein verkürztes Wiederholintervall im Verdachtsfall. Die Collector-Plugins kennen heute die Fehlerursachen `auth_failed`, `unreachable` und `tls` (`collector/plugins/plugin.go:63–87`, `ErrorCause`). Für Root-Cause-Korrelation und gegen Alarmfluten muss feststehen, wann eine Probe `down` und wann `unknown` meldet. Die Prüfungen laufen dezentral im Collector (PRB-03); PRB-05 legt die Zustandsmaschine in den Collector.

**Rechnung:** Option 1: 3 Echo-Requests je Prüfung, bei 353 Prüfungen/s ≈ 1.060 Pakete/s über alle Collectoren, je Collector (20) ≈ 53 Pakete/s, bei ICMP-Anteil 70 % (E-168) ≈ 37 Pakete/s. Down-Erkennung: 130–160 s (E-176). Option 3: Ab dem ersten Fehlschlag 10 s Intervall; Erkennung ≈ 10 + 4 × 10 = 50 s nach dem ersten Fehlschlag bzw. ≈ 70 s mit Phasenlage. Für betroffene Probes verdreifacht sich die Last. PRT-02 verlangt Timeout < Intervall; Option 3 braucht deshalb im Verdachtsfall einen Timeout ≤ 9 s statt des Standards 10 s.

**Frage (a):** Wie läuft eine Ping-Prüfung ab, und wann kehrt sie nach `up` zurück?

**Optionen:**

1. 3 Echo-Requests (`count` 3) im Abstand von 1 s (`spacing_ms` 1000) innerhalb des Timeouts; Erfolg bei mindestens 1 Antwort; `degraded` bei Verlust ≥ `loss_warn` (Standard 0,5) oder RTT über `rtt_warn_ms` (ohne Standard); Up-Count 1; kein verkürztes Intervall. Folgen: Verlust und RTT-Streuung für Schwellwert- und SLA-Monitoring ohne höhere Prüfrate.
2. 1 Echo-Request je Prüfung, Up-Count 1. Folgen: minimale Last; ein einzelner verlorener Paket zählt als Fehlschlag, Verlust ist nicht messbar.
3. Wie 1, zusätzlich verkürztes Intervall 10 s ab dem ersten Fehlschlag. Folgen: Rechnung oben; ändert die geforderte Last und den vorgegebenen Timeout.

**Frage (b), aus `d-pt-error-state`:** Wie werden Fehler auf Zustände abgebildet?

1. `timeout`, `unreachable`, `refused` und verletzter Sollwert → `down`; Schwellwarnung und Teilausfall → `degraded`; `auth_failed`, `tls` (nicht vertrauenswürdig), `unsupported` (fehlende Fähigkeit, etwa `CAP_NET_RAW`, oder nicht umgesetzter Typ; nicht `not_supported`) und `config` (ungültige Parameter, fehlendes Credential) → `unknown` mit Ursache. Der Down-Count wirkt nur auf `down`.
2. Jeder Fehler → `down`.

**Frage (c), aus `d-alr-state-source`:** Wo entsteht der Zustand?

1. Der Collector bewertet Down-Count und Up-Count und sendet Zustandswechsel mit Sequenz sofort (gepuffert), Rohergebnisse gebündelt; der Server korreliert.
2. Der Server bewertet jedes Ergebnis.
3. Beides.

**Empfehlung:** (a) Option 1, alle Werte je Ebene innerhalb der Typgrenzen überschreibbar (E-178). Grenzen des ICMP-Eintrags im Register (WP-934): Intervall 30 s (10 s–1 h), Timeout 10 s (1–10 s, `max_timeout_s` 10), je Probe timeout < interval (PRT-02; erfüllt PRT-01 „≤“), Down-Count 5 (1–20), Up-Count 1, `count` 3, `spacing_ms` 1000, `rtt_warn_ms` ohne Standard, `loss_warn` Standard 0,5. Mehrere Pakete liefern Verlust und RTT-Streuung für Schwellwert- und SLA-Monitoring, ohne die Prüfrate zu erhöhen (Pakete ≠ Prüfungen). Option 3 würde die Down-Erkennung von ca. 150 s auf ca. 70 s verkürzen. Sie ändert aber die geforderte Last und den Timeout und braucht die Freigabe des Auftraggebers. (b) Option 1: Ein falsches Passwort oder fehlendes `CAP_NET_RAW` ist kein Ausfall des Ziels. Als `down` gemeldet, würde es Alarme und Tickets auslösen und die Korrelation verfälschen. Die Gründe der Probes stehen in `edgecore/probewire/reason.go` (WP-933) und heißen nach PRB-05, PRT-13 und PRT-18 `unsupported` (nicht `not_supported`) und `config`; `ErrorCause` der Discovery-Plugins bleibt unverändert. Die Abbildung gilt für alle Probetypen. Die Ableitung „ok mit Warncode → `degraded`“ (u. a. `cert_expiring`, `threshold_exceeded`) liegt einmal in der Zustandsmaschine von WP-952; Typ-WPs ändern `state.go` nicht, sie ergänzen nur ihre Codes in `reason.go`. `probewire.Result` hat kein Detailfeld; Zusatzinformation läuft über `values` bzw. Labels. Executoren bleiben zustandslos (WP-950 AK4); Vorwerte hält nur der Vorwertspeicher `collector/probe/rate` aus WP-994. (c) Option 1: Bei Option 2 müsste der Server 353 Ergebnisse/s in Reihenfolge bewerten. Option 1 übersteht Verbindungsabbrüche über den Puffer (Anforderung 8), und der Server konsumiert Zustandswechsel idempotent über (Probe, Epoche, Sequenz) nach E-175.

**Auswirkung:** WP-933 legt die Gründe in `edgecore/probewire/reason.go` fest (`unsupported`), WP-934 nimmt Grenzen und Standards ins Register auf (`min_interval_s`, `max_interval_s`, `min_timeout_s`, `default_timeout_s`, `max_timeout_s` je Typ), WP-937 löst effektive Werte auf. WP-952 bildet die Zustandsmaschine im Collector einschließlich der `degraded`-Ableitung, WP-953 den ICMP-Executor mit drei Echo-Requests, WP-966 meldet Fähigkeiten (`unsupported`). WP-969 nutzt die ICMP-Felder aus WP-934, ohne sie neu einzuführen. WP-969 bis WP-971, WP-995, WP-998, WP-1012 und WP-1018 wenden die Fehlerabbildung je Typ an, WP-1022 prüft sie typübergreifend. WP-986 überführt Zustandswechsel in Alarme.

**Quelle:** collector/plugins/plugin.go:63–87 (`ErrAuthFailed`, `ErrUnreachable`, `ErrorCause`); `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:35` (neu, PRB-01), :37 (neu, PRB-03), :41 (neu, PRB-05), :55 (neu, PRT-01), :57 (neu, PRT-02), :79 (neu, PRT-13), :89 (neu, PRT-18), :95 (neu, PRT-21)

**Betroffene IDs:** PRB-01, PRB-05, PRT-01, PRT-02, PRT-13, PRT-18, PRT-21, ALR-02, MON-04

**Betroffene WPs:** WP-933 (`p3-probe-wire`), WP-934 (`p3-probe-types`), WP-937 (`p3-probe-effective`), WP-952 (`p3-coll-statemachine`), WP-953 (`p3-coll-icmp`), WP-966 (`p3-pt-caps`), WP-969 (`p3-pt-icmp`), WP-970 (`p3-pt-tcp`), WP-971 (`p3-pt-http`), WP-986 (`p3-alr-check-consumer`), WP-995 (`p3-pt-snmp-status`), WP-998 (`p3-pt-redfish-poll`), WP-1012 (`p3-pt-dhcp`), WP-1018 (`p3-pt-tr069`), WP-1022 (`p3-pt-conformance`)

<a id="e-178"></a>
## E-178 – Ebene „Gruppe“ in der Override-Hierarchie und Gerätegruppe für Zugangsdaten

**Schlüssel:** `d-mon-override-group` (eingearbeitet: `d-device-group`)

**Status:** entschieden (2026-11-09, nach Empfehlung: (a) Option 1, (b) Gerätegruppe = `monitoring_group` nach PRB-01/CRD-04; dynamische Mitgliedschaft über Filter später)
**Kontext:** PRB-01 verlangt: „Jeder Parameter (u. a. Intervall, Timeout, Down-Count, Up-Count, Schwellen) ist auf den Ebenen Organisation → Kunde → Gerätegruppe → Gerät (CI) → Probe überschreibbar; die spezifischste gesetzte Ebene gewinnt […] Gerätegruppe ist derselbe Gruppenbegriff wie in CRD-04.“ (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:35`, neu). CRD-04 legt die Auflösung „Probe > CI > Gerätegruppe > Subnetz > Scope > Kunde > Org“ fest: „Gerätegruppe ist derselbe Gruppenbegriff wie in PRB-01.“ (:177). CRD-01 nennt `target_kind ci|device_group|subnet|probe` (:171). Eine Gruppierung von CIs gibt es heute weder für das Monitoring noch für Zugangsdaten. Tags (P2) und gespeicherte Ansichten bestehen. Die effektive Konfiguration bestimmt, was der Server an die Collectoren verteilt (WP-948).

**Frage (a):** Was ist die Ebene „Gruppe“ der Monitoring-Overrides?

**Optionen:**

1. Eigene Tabelle `monitoring_group` mit statischer Mitgliedsliste von CIs (`monitoring_group_member`); bei Mehrfachmitgliedschaft entscheidet die Gruppenpriorität. Folgen: deterministisch, auditierbar, schnell auflösbar; Pflege der Mitglieder durch den Mandanten.
2. CI-Typ als Gruppe. Folgen: keine Pflege; ein Typ mischt Geräte mit verschiedenen Anforderungen.
3. Gespeicherte Ansicht (`saved_view`) als dynamische Gruppe. Folgen: keine Pflege; jede CI-Änderung kann die effektive Konfiguration und damit die Verteilung an die Collectoren ändern.

**Frage (b), aus `d-device-group`:** Was ist eine Gerätegruppe für Zugangsdaten?

1. Tag.
2. Gespeicherte Ansicht (dynamisch).
3. Neue Tabelle `device_group`.

**Empfehlung:** (a) Option 1. Statische Gruppen sind deterministisch, auditierbar und schnell auflösbar. Bei dynamischen Filtern würde jede CI-Änderung die effektive Konfiguration und die Verteilung an die Collectoren ändern. Der CI-Typ als Kriterium ist über Gruppen nachbildbar. (b) Gerätegruppe = `monitoring_group` (wie (a)), nach Katalog PRB-01/CRD-04; keine der Optionen 1 bis 3. Der Katalog verlangt für Monitoring-Overrides und Zugangsdaten denselben Gruppenbegriff und hat Vorrang vor dieser Entscheidung. Eine eigene Tabelle `device_group` (Option 3) oder Tags (Option 1) ergäben zwei Gruppenbegriffe; gespeicherte Ansichten (Option 2) ändern sich durch Filter unbemerkt, was für Zugangsdaten riskant ist. Tags sind keine Gerätegruppe. Die statische Mitgliedschaft aus `monitoring_group_member` ist auch für Zugangsdaten stabil und auditierbar, die Pflege erfolgt an einer Stelle.

**Auswirkung:** WP-935 legt `monitoring_group` und `monitoring_group_member` an (RLS/FORCE RLS, truncate guard, zusammengesetzte Fremdschlüssel mit `organization_id`; `client_id` und `site_id` der Mitgliedstabelle per BEFORE-Trigger aus der Elternzeile; kein Eintrag in `rls.DerivedScopeTables`, weil die Tabelle `client_id` und `site_id` selbst trägt (`backend/internal/tenant/rls/known_gaps.go:88–93`)). WP-890 legt `credential_binding` mit `target_kind ci|device_group|subnet|probe` ohne `tag_id` an; die Ziele `device_group` und `probe` antworten bis WP-935 mit 422. WP-935 (setzt WP-890 voraus) ergänzt in seiner Migration `device_group_id` → `monitoring_group` und `probe_id` → `probe` mit zusammengesetzten Fremdschlüsseln, schaltet beide Ziele frei und liefert die Mitgliedschaftsquelle für die Auflösung. WP-891 und WP-892 sehen die Ebene Gerätegruppe über die Schnittstelle `credential.DeviceGroupSource` vor (bis WP-935 leer). CRD-01 und CRD-04 schließen deshalb mit WP-935. WP-937 löst die Hierarchie mit Herkunft auf, WP-946 liefert die REST-API, WP-1080 die Oberfläche der Gruppen einschließlich der Bindung von Zugangsdaten an Gruppen. WP-923 zeigt die effektive Zugangsart am CI, ohne Tag-Bindung.

**Quelle:** `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:35` (neu, PRB-01), :171 (neu, CRD-01), :177 (neu, CRD-04), :183 (neu, CRD-07)

**Betroffene IDs:** PRB-01, CRD-01, CRD-04, CRD-07

**Betroffene WPs:** WP-890 (`p3-cred-binding`), WP-891 (`p3-cred-resolve`), WP-892 (`p3-cred-collector-target`), WP-923 (`p3-cred-ui-binding`), WP-935 (`p3-probe-schema-def`), WP-937 (`p3-probe-effective`), WP-946 (`p3-mon-profile-api`), WP-1080 (`p3-ui-profiles`)

<a id="e-179"></a>
## E-179 – Ablage der Messwerte von Metrik-Probes (SNMP, Redfish)

**Schlüssel:** `d-mon-results-vs-metrics`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Probes wie `redfish` oder `snmp_status` liefern mehrere Werte je Prüfung (Temperaturen, Lüfter, Netzteile, OID-Werte). `probe_result` hat nach E-171 nur ein Feld `value`. `metric_sample` ist der Speicher von MON-02 und der Alert-Regeln (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:6`, :10). Heute ordnet der Ingest Collector-Metriken einem CI über die Adresse zu (`CIAddressResolver.ResolveCI` und `assignCIs`, `backend/internal/monitoring/monitoring.go:949–992`); eine Adresse, die zwei sichtbare CIs teilen, bleibt unzugeordnet.

**Frage:** Wohin gehen die Mehrfachwerte von Metrik-Probes?

**Optionen:**

1. Status, RTT und Hauptwert in `probe_result`; die Wertetabelle (`values`) geht über eine Brücke im Ingest-Worker nach `metric_sample` mit `ci_id` aus der Probe. Folgen: Alert-Regeln, Health und CI-Charts lesen weiter `metric_sample`; die Zuordnung über die Adresse entfällt für Probe-Daten.
2. Alles nach `probe_result` mit JSONB-Werten. Folgen: eine Tabelle; breite Zeilen für alle Typen, Alert-Regeln und Charts brauchen einen zweiten Lesepfad.
3. Alles nach `metric_sample`. Folgen: ein Speicher; keine Idempotenz, und Zustandslogik und Verfügbarkeit müssten aus breiten Zeilen rechnen.

**Empfehlung:** Option 1. Bestehende Alert-Regeln, Health und CI-Charts lesen weiter `metric_sample`. Die Zustands- und Verfügbarkeitslogik bleibt schmal und schnell. Die CI-Zuordnung über die Adresse entfällt für Probe-Daten, weil die Probe ihr CI kennt. Volumen: Nur Metrik-Probes schreiben nach `metric_sample`. Bei 10 % SNMP-Anteil (35/s, E-168) und z. B. 8 Werten je Prüfung sind das ≈ 280 Zeilen/s ≈ 24 Mio. Zeilen/Tag. WP-976 misst den Wert und nennt eine Obergrenze je Prüfung (PRT-01, Zeilengrenze).

**Auswirkung:** WP-976 liefert die Brücke mit Bulk-Insert und kündigt den Umweg über die Adresszuordnung für Probe-Daten ab.

**Quelle:** backend/migrations/000020_metrics.up.sql:6–13; backend/internal/monitoring/monitoring.go:949–992; docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:4–10 (MON-01 bis MON-04)

**Betroffene IDs:** MON-02, MON-03, MON-04, PRT-01

**Betroffene WPs:** WP-976 (`p3-mon-metric-path`)

<a id="e-180"></a>
## E-180 – Behandlung nachgereichter Zustandswechsel

**Schlüssel:** `d-mon-late-sync`

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Nach einem Collector- oder Verbindungsausfall kommen gepufferte Zustandswechsel bis zu 24 h verspätet an (PRB-07). PRB-07 verlangt: „Nachgereichte Zustandswechsel sind als late markiert und lösen für bereits beendete Zustände keine Benachrichtigung aus.“ (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:45`, neu); eine Doppelübertragung erzeugt weder Doppelzeilen noch doppelte Ereignisse. Alarmfluten nach der Wiederverbindung sind zu vermeiden.

**Frage:** Wie werden nachgereichte Zustandswechsel übernommen und gemeldet?

**Optionen:**

1. In die Historie übernehmen und als `late` markieren (`observed_at < received_at − 2 × Intervall`, mit korrigierter Uhrabweichung nach E-175). Ereignis mit `notify = false`, wenn der Zustand bis zur Ankunft schon wieder beendet ist, also ein jüngerer Wechsel derselben Probe folgt. Ein noch andauernder Zustand wird normal gemeldet. Folgen: Historie und SLA vollständig; keine Benachrichtigung über längst erledigte Ausfälle.
2. Alle nachgereichten Wechsel melden. Folgen: einfach; nach jedem Ausfall eine Flut von Benachrichtigungen über erledigte Zustände.
3. Nachgereichte Wechsel verwerfen. Folgen: keine Flut; Historie, Verfügbarkeit und SLA sind für die Ausfallzeit falsch, PRB-07 ist verletzt.

**Empfehlung:** Option 1. Historie und SLA bleiben vollständig, ohne Benachrichtigungen über längst erledigte Ausfälle. Ein noch bestehender `down` muss weiterhin alarmieren. Die Spool-Reihenfolge (Zustandswechsel vor Messwerten, PRB-07) sorgt dafür, dass der Server den jüngsten Wechsel einer Probe kennt, bevor er über `notify` entscheidet.

**Auswirkung:** WP-944 legt `late` und `notify` in `probe_state_change` an, WP-954 liefert den Spool mit getrennten Warteschlangen, WP-955 überträgt in Sequenzreihenfolge. WP-961 bestimmt `late` und `notify`, WP-964 behandelt die Rückkehr aus `collector_unreachable` mit den nachgereichten Wechseln.

**Quelle:** `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:41` (neu, PRB-05), :45 (neu, PRB-07)

**Betroffene IDs:** PRB-05, PRB-07, NFR-04, COL-05

**Betroffene WPs:** WP-944 (`p3-probe-schema-state`), WP-954 (`p3-coll-spool`), WP-955 (`p3-coll-uplink`), WP-961 (`p3-probe-state-apply`), WP-964 (`p3-netdown`)

<a id="e-181"></a>
## E-181 – Verfügbarkeitsberechnung und Wertung von unknown

**Status:** entschieden (2026-11-09, Option 5; Beschluss Punkt 3)
**Kontext:** PRB-06 verlangt eine Verfügbarkeitshistorie über 400 Tage, sekundengenau aus Zustandsintervallen und nicht aus Stichproben. ALR-06 verlangt Verfügbarkeitsziele ohne Wartungszeiten und eine Wertung von `unknown` „nach Org-Einstellung“. `unknown` entsteht bei Netzdown bzw. Collector-Ausfall (Grund `collector_unreachable`, WP-964), bei ausgefallener Elternprobe (`parent_down`, WP-965), bei Grenzverletzung (`limit_exceeded`) und bei fehlender Fähigkeit (`unsupported`). Der Auftraggeber behandelt einen Collector-Ausfall wie einen Netzdown (Vorgabe 4) und verlangt 400 Tage Historie (Vorgabe 7). Auf main 6d019370 gibt es keine Verfügbarkeitsberechnung: `ci.health` ist ein Momentwert (`backend/migrations/000090_ci_health_lifecycle.up.sql:44–46`). Rohsamples in `metric_sample` werden 400 Tage gehalten (`backend/migrations/000020_metrics.up.sql:35`), Probe-Rohergebnisse nach [E-172](entscheidungen.md#e-172) nur 14 Tage. Zusammengeführt sind die Entscheidungen `d-alr-sla-unknown` (Wertung von Collector-down) und `d-alr-sla-source` (Datenbasis).

**Optionen:**

1. Dreiwertige Org-Einstellung `alarm_settings.unknown_mode` ∈ `excluded | down | up` nach ALR-06 („als verfügbar, nicht verfügbar oder ausgenommen“), Standard `excluded` (WP-1030). Bei `excluded` gilt `percent = (up_s + degraded_s) / (Summe − unknown_s − maintenance_s)`; `unknown` wird aus der Messzeit herausgerechnet. Bei `down` zählt `unknown_s` im Nenner und als nicht verfügbar, bei `up` im Nenner und Zähler als verfügbar. Alle Zeitanteile (`up_s`, `degraded_s`, `down_s`, `unknown_s`, `maintenance_s`) werden getrennt ausgewiesen, zusammen mit dem angewandten `unknown_mode`. Datenbasis sind die Zustandsintervalle aus `probe_state_change` und der Tagesrollup `probe_availability_daily` (WP-973). Wartung und Unterdrückung gehen als Abzugsintervalle ein (WP-1033). Folge: ALR-06 („nach Org-Einstellung“) ist mit allen drei Wertungen erfüllt, und im Standard verschlechtert ein Netzdown die Quote nicht ohne Nachweis eines Ausfalls.
2. `unknown` zählt fest als nicht verfügbar. Folge: Ein Collector-Ausfall im Kundennetz senkt die Verfügbarkeit aller Ziele der Gruppe, auch wenn sie erreichbar waren. ALR-06 verlangt aber eine Org-Einstellung.
3. `unknown` zählt fest als verfügbar. Folge: Ein echter Ausfall hinter einem Netzdown wird verdeckt. Vorgabe 4 („wie Netzdown“) ist nicht abgebildet.
4. Org-Einstellung wie Option 1, aber mit Standard `down` (Empfehlung aus `d-alr-sla-unknown`). Folge: wie Option 2, solange der Mandant nichts ändert.
5. **Beschluss vom 2026-11-09:** Melden wie ein Netzausfall, im Protokoll getrennt, gewertet nach den nachgelieferten Daten.
   - **Meldung:** Fällt eine Collector-Gruppe aus, entsteht genau eine Störung „Netzausfall“ mit Ursache `collector_unreachable` und kritischem Wurzelalarm. Sie wird wie ein Netzausfall benachrichtigt, die Probes der Gruppe sind Symptome (WP-964, WP-1029).
   - **Protokoll:** Die Zeit mit Grund `collector_unreachable` steht als eigener Anteil `collector_unreachable_s` neben `up_s`, `degraded_s`, `down_s`, `unknown_s` (übrige Gründe) und `maintenance_s`. Das gilt in Tagesrollup, API, SLA-Bericht und Oberfläche.
   - **Wertung:** Liefert der Collector nach der Wiederverbindung gepufferte Ergebnisse für das Ausfallfenster (PRB-07), gilt für die gemessene Zeit der gemessene Zustand. Das erste nachgelieferte Ergebnis jeder Probe setzt ihn rückwirkend ab seinem Zeitstempel (Rekonstruktionswechsel, `late`, ohne Benachrichtigung), auch wenn der Collector keinen Wechsel gemeldet hat. Tagesverfügbarkeit und SLA-Werte der betroffenen Tage werden neu berechnet. Nur Zeit ohne nachgelieferte Messdaten (Collector selbst ausgefallen, Pufferverlust) bleibt `collector_unreachable_s` und wird nach `alarm_settings.unknown_mode` gewertet, Standard `excluded` (Empfehlung, Beschluss Punkt 6).
   - Folge: Eine reine Unterbrechung der Verbindung zwischen Collector und Server verfälscht die Verfügbarkeit nicht. Ein echter Ausfall des Collectors bleibt nachvollziehbar getrennt ausgewiesen.

Teilfrage Datenbasis (aus `d-alr-sla-source`): (a) Berechnung aus `metric_sample` bzw. `probe_result` oder (b) aus Zustandsintervallen mit täglichem Rollup. Variante (a) scheidet aus, siehe Rechnung.

**Rechnung:**

- Probe-Anzahl bei Volllast NFR-11: 30,5 Mio. Prüfungen/Tag ÷ 2.880 Prüfungen je Probe und Tag (30 s) ≈ 10.600 Probes.
- Variante (a): 400 Tage Rohergebnisse wären 30,5 Mio. × 400 ≈ 12,2 Mrd. Zeilen ([E-172](entscheidungen.md#e-172)). Gehalten werden nur 14 Tage. Das 5-min-Aggregat reicht 90 Tage, und seine Auflösung von 5 min ist nicht sekundengenau.
- Variante (b): `probe_availability_daily` erhält 10.600 Zeilen/Tag, in 400 Tagen 4,24 Mio. Zeilen. Je Zeile entfallen etwa 140 B auf das Tupel (5 UUIDs, Datum, 5 Ganzzahlen, Zeitstempel, Kopf) und etwa 80 B auf den Primärschlüssel `(probe_id, day)` und den Index `(organization_id, ci_id, day)`. Das sind etwa 220 B je Zeile, zusammen ≈ 0,9 GB. Geschrieben wird einmal täglich ein Batch von 10.600 Upserts je Instanz (WP-973 AK3), dazu der Nachlauf für `late`-Wechsel.
- Zustandswechsel: Bei durchschnittlich 4 Wechseln je Probe und Tag entstehen 42.400 Zeilen/Tag (≈ 0,5 Zeilen/s), in 400 Tagen ≈ 17 Mio. Zeilen. Bei etwa 250 B je Zeile mit Index sind das ≈ 4,2 GB.
- Eine Abfrage über 400 Tage liest je Probe 400 Tageszeilen; Ziel < 300 ms (WP-973 AK7).

**Beschluss:** Option 5 (2026-11-09, Punkt 3). Sie erweitert Option 1 um den getrennten Anteil `collector_unreachable_s` und den Vorrang der nachgelieferten Daten. Formel und Org-Einstellung aus Option 1 bleiben.

**Empfehlung:** Option 1 mit Datenbasis (b). Ein unbekannter Zustand ist kein nachgewiesener Ausfall. Die getrennt ausgewiesenen Anteile halten die Rechnung nachvollziehbar, und die dreiwertige Org-Einstellung erfüllt ALR-06 und erlaubt die Wertung `down` oder `up` dort, wo ein Vertrag sie verlangt. Die Zustandsintervalle sind sekundengenau, über 400 Tage billig und unabhängig von der Rohdatenaufbewahrung. Der Beschluss vom 2026-11-09 (Option 5) präzisiert die Vorgabe 4 (Aufgabe A-66 erledigt).

**Auswirkung:** WP-964 (Rekonstruktion aus dem ersten nachgelieferten Ergebnis), WP-1029 (Störung „Netzausfall“), WP-973 (Tabelle, Formel, Abfrage), WP-1030 (legt `alarm_settings.unknown_mode` an, Standard `excluded`), WP-1048 (Ziele ohne Wartung, Fehlerbudget-Alarm je Ziel über `budget_warn_percent`), WP-1049 (wertet `unknown_mode` in Berechnung und Fehlerbudget aus, Ausweis der angewandten Wertung), WP-1082 (Anzeige der Anteile, „–“ bei Nenner 0). Maßgebliche Probe eines CIs ist die `icmp`-Probe ohne `parent_probe_id`, sonst die schlechteste (WP-973 AK5).

**Quelle:** `backend/migrations/000020_metrics.up.sql:35`; `backend/migrations/000090_ci_health_lifecycle.up.sql:44–46`, `:63–102`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; PRB-06, ALR-06, ALR-11); Auftraggeber-Vorgaben 4 und 7 vom 2026-11-09.

**Betroffene IDs:** PRB-06, ALR-06, ALR-11, MGT-04, NFR-06, NFR-11

**Betroffene WPs:** WP-973 (`p3-availability`), WP-1030 (`p3-alr-settings`), WP-1048 (`p3-sla-avail-schema`), WP-1049 (`p3-sla-avail-calc`), WP-1082 (`p3-ui-availability`)

<a id="e-182"></a>
## E-182 – Automatische Ping-Probes für erkannte Geräte

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Die Erreichbarkeit eines CIs entsteht heute nur aus dem Sweep (`sweep_reachable`, Takt des Scopes, Standard 24 h, `collector/collectorcmd/schedule.go:24`). Die Daten für eine Automatik liegen vor: `ci.management_ip` (`backend/migrations/000002_ci_type_system.up.sql:38`), `ci_type.is_observable` (`backend/migrations/000104_ci_type_fields_seeds.up.sql:40–41`) und der entdeckende Collector in `ci_change.collector_id` (`backend/migrations/000167_ci_change_aud03.up.sql`). Die Verknüpfung von Management-IP und `ip_address` regelt [E-215](entscheidungen.md#e-215). Ohne Automatik legt der Mandant jede Ping-Probe von Hand an. Probe-Kontingente und Prüfbudget prüft WP-940.

**Optionen:**

1. Org-Einstellung `monitoring.auto_ping` in `organization.settings` (Standard aus). Ist sie an, legt ein Job für jedes aktive, beobachtbare CI mit Management-IP eine `icmp`-Probe in der Collector-Gruppe des entdeckenden Collectors an, idempotent über `(organization_id, ci_id, type, target)`, mit Herkunft `auto`, Sperrliste für gelöschte Auto-Probes und Prüfung über `AllowProbeChange` (WP-940). Folge: kein ungewollter Lastsprung, und für ISPs mit vielen Anschlüssen genügt ein Schalter.
2. Immer automatisch. Folge: Jede Organisation mit Discovery erhält ohne Zutun Dauerprüfungen; Kontingente laufen beim ersten Discovery-Lauf voll.
3. Nie automatisch. Folge: Bei Tausenden CPE-CIs ist die Anlage nur per Import oder API praktikabel.

**Rechnung:** 5.000 CIs mit Management-IP ergeben bei 30 s Intervall 5.000 × 2.880 = 14,4 Mio. Prüfungen/Tag (≈ 167/s). Das ist fast die Hälfte der Gesamtlast aus NFR-11 (353/s). Eine Automatik ohne Schalter würde diese Last mit dem ersten Discovery-Lauf auslösen.

**Empfehlung:** Option 1. Die Automatik bleibt unter Kontrolle von Kontingent und Prüfbudget und ist für große Bestände trotzdem nutzbar. Die Sperrliste verhindert, dass eine absichtlich gelöschte Auto-Probe beim nächsten Lauf wieder entsteht. Abschalten löscht keine Auto-Probes.

**Auswirkung:** WP-949 (Einstellung, Job `probe.autocreate`, Herkunftsspalte, Sperrliste `probe_autocreate_suppression`).

**Quelle:** `collector/collectorcmd/schedule.go:24`; `backend/migrations/000002_ci_type_system.up.sql:38`; `backend/migrations/000104_ci_type_fields_seeds.up.sql:40–41`; `docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:6` (MON-02).

**Betroffene IDs:** MON-02, PRB-01, PKG-04, NFR-11

**Betroffene WPs:** WP-949 (`p3-probe-autocreate`)

<a id="e-183"></a>
## E-183 – Ort und Schnittstelle der Probetypen im Collector

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** `plugins.Plugin` ist auf Discovery ausgelegt: `Probe` liefert Treffer für eine Zielliste, `Collect` einen `DeviceRecord` (`collector/plugins/plugin.go:47–61`). Probes brauchen dagegen je Einzelprüfung Parameter, Zustand, Ursache (Reason-Code) und Limits. Die einzige Dauerschleife pollt SNMP-Metriken ohne Probe-Bezug (`runMetricsLoop`, `collector/collectorcmd/collectorcmd.go:1481`). Wiederverwendbare Protokollbausteine sind heute unexportiert oder auf Discovery zugeschnitten: die SNMP-Sitzung `(*Plugin).request` und `walkTable` (`collector/plugins/snmp/poll.go:167`, `:212`), der Sweep-Ping, der je Aufruf einen eigenen Socket öffnet und nur IPv4 kann (`(*ICMPPinger).Ping`, `collector/plugins/sweep/icmp.go:64–73`), und der HTTP-Client `plugins.NewTLSClient` (`collector/plugins/tlsscope.go:80`). Der Entwurf verwies auf ein WP `p3-probe-model` mit Paket `collector/probes`; im Plan liegen Executor-Schnittstelle und Register bei WP-950 (Paket `collector/probe`), Typgrenzen bei WP-934 (`api/probe-types.json`, `edgecore/probewire`).

**Optionen:**

1. Eigenes Paket `collector/probe` mit Executor-Schnittstelle, Register, Scheduler und Zustandsmaschine (WP-950 bis WP-952); einfache Typen flach als `collector/probe/<typ>.go`, Typen mit eigenem Umfang als Unterpakete `collector/probe/<paket>/` (`snmpstatus`, `snmpif`, `snmpcustom`, `redfish`, `routing`, `tr069`, `rpki`), alle mit Registrierung per `init()`. Die Protokollbausteine werden exportiert und wiederverwendet: `snmp.Open` mit `Session.Get`/`WalkColumns` (WP-993), `plugins.NewTLSClient`; ICMP erhält einen eigenen Executor mit gemeinsamem Socket je Adressfamilie (WP-953). Ein Konformitätstest prüft alle Typen gegen denselben Vertrag (WP-1022). Folge: DIS-03 bleibt unverändert.
2. `plugins.Plugin` um eine Methode `Check` erweitern; jeder Probetyp wird ein Plugin in `collector/plugins`. Folge: Alle acht Discovery-Plugins brauchen eine Leer-Implementierung, und Discovery- und Monitoring-Tests vermischen sich.
3. Probes als Sonderfall des Polls in `runMetricsLoop`. Folge: kein Zustand, keine Ursache und keine Limits je Probe; Jitter je Probe (PRB-04) nicht abbildbar.

**Empfehlung:** Option 1. Discovery und Monitoring bleiben getrennt, DIS-03 bleibt unverändert, und der Konformitätstest deckt alle Probetypen ab. Weil die Typen die vorhandenen Bausteine aufrufen, entsteht keine zweite SNMP- oder HTTP-Implementierung. Der Sweep-Ping taugt nicht als Baustein für 353 Probes/s, weil er je Prüfung einen Socket öffnet; WP-953 ersetzt ihn für Probes durch einen gemeinsamen Socket mit Demultiplexing.

**Auswirkung:** Der Paketname ist `collector/probe` (nicht `collector/probes`), Register und `Lookup` liegen in `collector/probe/executor.go` (WP-950). Einfache Typen liegen flach als `collector/probe/<typ>.go` (`icmp`, `tcp`, `http`, `bfd`, `optical`, `bng`, `pon`, `dns`, `dhcp`, `stamp`, `twamp_control`, `gnmi`). Typen mit eigenem Umfang liegen in Unterpaketen `collector/probe/<paket>/`: `snmpstatus`, `snmpif`, `snmpcustom`, `redfish`, `routing` (`bgp`, `ospf`, `isis`, `bgp_gnmi`), `tr069` (`tr069`, `usp`) und `rpki` (`rpki_rtr`, `rpki_roa`). Alle Probetyp-WPs der Meilensteine M3.7 und M3.10 registrieren ihre Executoren per `init()` über `Register` aus WP-950. Die Unterpakete werden über Blank-Importe in `collector/probe/all/all.go` eingebunden; die Datei legt WP-956 zunächst ohne Importe an und importiert sie in `collector/collectorcmd`. Jedes Unterpaket-WP ändert `all.go` „(aus WP-956)“ und setzt WP-950 und WP-956 voraus. Hilfspakete ohne Registrierung (`collector/probe/rate`, `collector/probe/gnmi`, `collector/internal/dhcpv4`) importieren `collector/probe` nicht. Der Konformitätstest (WP-1022) und der Benchmark (WP-1023) liegen im externen Testpaket `probe_test`.

**Quelle:** `collector/plugins/plugin.go:47–61`; `collector/plugins/snmp/poll.go:167`, `:212`; `collector/plugins/sweep/icmp.go:64–73`; `collector/plugins/tlsscope.go:80`; `collector/collectorcmd/collectorcmd.go:1481`; `docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:28` (DIS-04).

**Betroffene IDs:** PRT-01, PRT-21, PRB-04, PRB-05, DIS-03

**Betroffene WPs:** WP-934 (`p3-probe-types`), WP-950 (`p3-coll-executor`), WP-951 (`p3-coll-scheduler`), WP-953 (`p3-coll-icmp`), WP-956 (`p3-coll-probe-config`), WP-993 (`p3-pt-snmp-table`), WP-1022 (`p3-pt-conformance`), WP-1023 (`p3-pt-bench`)

<a id="e-184"></a>
## E-184 – Geltung der DIS-10-Untergrenze von 5 min

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** DIS-10 setzt Standardintervalle für Sweep und Polls und ein Minimum von 5 min für Polls (`docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:40`). Der Collector setzt es als `minPollInterval` um (`collector/collectorcmd/schedule.go:26`, angewandt `:58–72`). Die Auftraggeber-Vorgabe 2 verlangt einen Ping im 30-s-Takt, MON-03 konfigurierbare Intervalle für SNMP und Redfish (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:8`), PRT-02 ein Mindestintervall von 10 s für ICMP.

**Optionen:**

1. DIS-10 gilt nur für Inventar-Polls der Discovery-Scopes. Monitoring-Probes haben eigene Untergrenzen je Typ im Typregister (WP-934, [E-154](entscheidungen.md#e-154)). Folge: DIS-10 bleibt wörtlich gültig; der Katalogtext von MON-03 erhält die Abgrenzung.
2. DIS-10 gilt auch für Probes, Ping ist eine Ausnahme. Folge: SNMP-Interface- oder Redfish-Probes könnten nicht unter 5 min laufen, obwohl MON-03 konfigurierbare Intervalle verlangt.
3. DIS-10 im Katalog auf 10 s für alle ändern. Folge: Vollerfassungen (Walk aller Tabellen, `DeviceRecord`) könnten im 10-s-Takt laufen und Zielgeräte überlasten.

**Rechnung:** Ein Inventar-Poll liest je Gerät mehrere Tabellen vollständig (Interfaces bis 256 Zeilen, `MaxPollInterfaces`, `collector/plugins/snmp/poll.go:103`). Eine `icmp`-Probe sendet 1–5 Pakete. Bei 30 s statt 5 min verzehnfacht sich die Last je Ziel; für eine Vollerfassung wären das bei 256 Interfaces und 3 Spalten etwa 768 Varbinds alle 30 s, für eine Ping-Probe ein Paket.

**Empfehlung:** Option 1. Inventar-Polls sind teure Vollerfassungen, Probes leichte Einzelprüfungen mit eigenen Grenzen (Mindestintervall, Timeout, Parallelität je Ziel). Die Grenzen je Typ schützen die Zielgeräte. Der Umgebungs-Poll (`RETICORA_SNMP_POLL_METRICS`) bleibt als Rückfall unter DIS-10 (WP-999).

**Auswirkung:** WP-999 dokumentiert die Abgrenzung in `docs/README.md`; WP-934 trägt die Mindestintervalle je Typ.

**Quelle:** `docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:40`; `collector/collectorcmd/schedule.go:26`, `:58–72`; `collector/plugins/snmp/poll.go:103`; `docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:8`.

**Betroffene IDs:** DIS-10, MON-03, PRT-01, PRT-02

**Betroffene WPs:** WP-999 (`p3-pt-mon03-migrate`)

<a id="e-185"></a>
## E-185 – Ratenberechnung für Zähler (Interfaces, snmp_custom counter)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Der Interface-Poll lädt Rohzähler hoch (`if_in_octets`, `if_out_octets`, `PollInterfaces`, `collector/plugins/snmp/poll.go:126–163`, Ausgabe `:156`). Eine Ratenberechnung, eine Wrap-Behandlung und eine Erkennung von Zählerrücksetzungen gibt es weder im Collector noch im Server. Schwellen, SLA und Kapazitätsprognose brauchen Raten und Auslastung. Zustandswechsel mit Schwellen sollen nach [E-186](entscheidungen.md#e-186) im Collector entstehen, auch bei getrennter Verbindung (PRB-07).

**Optionen:**

1. Der Collector rechnet Raten: Vorwert und Zeitpunkt je Serie im Speicher, Wrap für 32 und 64 Bit, Discontinuity über einen Rücksprung von `sysUpTime` bzw. eine Änderung von `ifCounterDiscontinuityTime`. Hochgeladen werden nur Raten; Rohzähler optional je Probe. Folge: Nach einem Collector-Neustart fehlt je Serie genau eine Rate.
2. Der Server rechnet aus Rohzählern (Continuous Aggregate). Folge: Schwellen auf Raten wirken erst im Server und bei getrennter Verbindung gar nicht; der genaue Abfragezeitpunkt geht durch die Bündelung verloren.
3. Beides immer. Folge: doppeltes Volumen.

**Rechnung:** Beispiel 1.000 Interfaces mit 6 Zählern (Oktette, Fehler und Discards je Richtung) im 5-min-Intervall: Option 1 lädt 1.000 × 6 × 288 = 1,73 Mio. Raten/Tag hoch, Option 3 3,46 Mio. Werte/Tag. Wrap: Ein 32-Bit-Oktettzähler läuft nach 2³² B ≈ 4,29 GB über; bei 100 Mbit/s nach etwa 343 s. Ab etwa 115 Mbit/s ist er bei 5 min Intervall mehrdeutig. Der Collector bevorzugt deshalb die 64-Bit-Zähler (`ifHCInOctets`, Rückfall wie heute `poll.go:139–142`) und verwirft eine Rate, wenn mehr als ein Überlauf möglich ist.

**Empfehlung:** Option 1. Der Collector kennt Abfragezeitpunkt und Vorwert genau, und die Zustandsableitung mit Schwellen läuft dort auch ohne Verbindung. Gegenüber Option 3 halbiert sich die Sample-Menge. Der Verlust einer Rate nach einem Neustart ist hinnehmbar.

**Auswirkung:** WP-994 (Ratenbibliothek, Vorwertspeicher `collector/probe/rate` im Engine-Kontext mit `Observe` für Zähler sowie `Previous`/`Swap` für Gauge- und Enum-Vorwerte und `sysUpTime`), WP-996 und WP-997 nutzen sie. Die Executoren bleiben zustandslos (WP-950 AK4).

**Quelle:** `collector/plugins/snmp/poll.go:126–163`, `:139–142`.

**Betroffene IDs:** PRT-06, MON-03, MON-04, ALR-08

**Betroffene WPs:** WP-994 (`p3-pt-counter-rates`), WP-996 (`p3-pt-snmp-if`), WP-997 (`p3-pt-snmp-custom`)

<a id="e-186"></a>
## E-186 – Wo typbezogene Schwellen den Probe-Zustand bestimmen

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** MON-03 verlangt Charts mit Schwellwerten (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:8`). PRB-05 verlangt degraded bei Erfolg mit verletzter Schwelle und Zustandswechsel ohne Batch-Wartezeit (p95 < 5 s). PRB-07 verlangt, dass Zustandswechsel auch bei getrennter Verbindung entstehen und gepuffert werden. Alerting-Regeln (MON-04, ALR-05) bleiben im Server; heute kennt eine Regel nur `gt`, `lt` und `eq` gegen einen Wert (`backend/internal/monitoring/evaluator.go:12–23`).

**Optionen:**

1. Schwellen (warn/crit) sind Teil der Probe-Parameter. Der Collector leitet daraus degraded bzw. einen Fehlschlag ab; der Server zeigt sie im Chart und nutzt den Zustand. Bedingungen über Zeitfenster oder mehrere Metriken (MET-45) bleiben Alarmregeln im Server ([E-201](entscheidungen.md#e-201)). Folge: eine Quelle für Schwellen je Probe.
2. Nur der Server wertet Schwellen aus. Folge: Ein Zustandswechsel wegen Schwellenverletzung entsteht erst nach Ingest, bei getrennter Verbindung gar nicht; die Zustandsmaschine im Collector kennt kein degraded aus Messwerten.

**Empfehlung:** Option 1. Zustandswechsel entstehen am Messort und werden gepuffert zeitnah übertragen. Die Schwellen für den Chart haben eine einzige Quelle (Probe-Parameter, WP-1085). Die Regel-Engine bleibt für übergreifende Bedingungen zuständig.

**Auswirkung:** Alle Typen mit Schwellen (`http`, `snmp_if`, `snmp_custom`, `redfish`, `optical`, `gnmi`) bilden warn → degraded und crit → Fehlschlag gleich ab. Die Typ-Executoren melden dafür `ok` mit Warncode (z. B. `threshold_exceeded`, `cert_expiring`) und ergänzen ihre Codes nur in `edgecore/probewire/reason.go`; die Ableitung „ok mit Warncode → degraded“ liegt einmal in der Zustandsmaschine von WP-952, Typ-WPs ändern `state.go` nicht. Fachliche Zusatzinformation läuft über `values` bzw. Labels, `probewire.Result` hat kein Detailfeld. WP-1085 zeichnet die Schwellen aus den effektiven Probe-Werten.

**Quelle:** `docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:8`, `:10`; `backend/internal/monitoring/evaluator.go:12–23`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; PRB-05, PRB-07).

**Betroffene IDs:** MON-03, MON-04, PRB-05, PRB-07, PRT-04, PRT-06, PRT-09, PRT-16

**Betroffene WPs:** WP-971 (`p3-pt-http`), WP-996 (`p3-pt-snmp-if`), WP-997 (`p3-pt-snmp-custom`), WP-998 (`p3-pt-redfish-poll`), WP-1004 (`p3-pt-optical`), WP-1016 (`p3-pt-gnmi`), WP-1085 (`p3-pt-mon03-charts`)

<a id="e-187"></a>
## E-187 – Quelle für herstellerabhängige Daten (Routing, BFD, Optik, BNG, PON)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Das SSH-Plugin führt nur eine feste Linux-Allowlist aus (`AllowedCommands`, `collector/plugins/ssh/exec.go:21–41`); der Befehlstext kommt nach DIS-04 nie vom Ziel, aus einem Profil oder vom Server. Die Profile führen zwar `sshCommands` (`collector/profiles/profiles.go:52`), kein Code liest das Feld. Hersteller-MIBs für BGP-v2, BFD, Optik, BNG und PON sind uneinheitlich. gNMI/OpenConfig gibt es nur auf neueren Plattformen. PRT-20 verlangt Herstellerprofile über `RETICORA_PROFILES_DIR` ohne Release.

**Optionen:**

1. SNMP mit Standard-MIBs als generischer Kern, Hersteller-MIBs über den Profilabschnitt `monitor` (WP-1000) und gNMI/OpenConfig als alternative Quelle für BGP und Optik (WP-1017). Keine Hersteller-CLI. Folge: Die installierte Basis ist abgedeckt, neue Plattformen kommen per Profil.
2. Zusätzlich Hersteller-CLI über SSH mit Parsern je Plattform und Allowlist je Profil. Folge: hoher Pflegeaufwand für Parser und eine neue Angriffsfläche, weil Befehle aus Profilen kämen.
3. Nur gNMI. Folge: Ältere OLTs und BNGs ohne gNMI fallen heraus.

**Empfehlung:** Option 1. Das Sicherheitsprinzip der festen Befehlsliste bleibt erhalten, und SNMP deckt die installierte Basis ab. Profile sind ohne Release erweiterbar. Option 2 kann bei Bedarf ein eigenes Vorhaben in P4 werden. Die feste zweite Allowlist für Running-Configs ([E-207](entscheidungen.md#e-207), WP-1053) ist davon getrennt: Ihre Befehle stehen im Code, nicht im Profil.

**Auswirkung:** WP-1000 (Profilabschnitt `monitor`), WP-1001 bis WP-1009 lesen nur SNMP, WP-1017 ergänzt gNMI für `bgp` und `optical`.

**Quelle:** `collector/plugins/ssh/exec.go:21–41`; `collector/profiles/profiles.go:52`; `docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:28` (DIS-04).

**Betroffene IDs:** PRT-07, PRT-08, PRT-09, PRT-10, PRT-11, PRT-16, PRT-20, DIS-04

**Betroffene WPs:** WP-1000 (`p3-pt-profile-monitor`), WP-1001 (`p3-pt-bgp`), WP-1002 (`p3-pt-ospf-isis`), WP-1003 (`p3-pt-bfd`), WP-1004 (`p3-pt-optical`), WP-1005 (`p3-pt-optical-vendors`), WP-1006 (`p3-pt-bng`), WP-1007 (`p3-pt-pon-core`), WP-1008 (`p3-pt-pon-vendor-a`), WP-1009 (`p3-pt-pon-vendor-b`), WP-1017 (`p3-pt-gnmi-sources`)

<a id="e-188"></a>
## E-188 – Metriktiefe für PON/ONU

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** PRT-11 verlangt je OLT und PON-Port die Anzahl ONUs online/offline und je ONU Zustand, Rx-Pegel, letzte Down-Ursache und Seriennummer, mit höchstens 8.192 ONUs je OLT. Werte-Tabellen von Probes gehen nach [E-179](entscheidungen.md#e-179) über die Brücke nach `metric_sample` (Labels als `jsonb`, Retention 400 Tage, `backend/migrations/000020_metrics.up.sql:6–13`, `:35`; Kompression nach 7 Tagen, `:28–32`). Das Interface-Polling ist heute auf 256 Zeilen begrenzt (`MaxPollInterfaces`, `collector/plugins/snmp/poll.go:103`).

**Optionen:**

1. Je PON-Port Aggregate als Metriken (`onu_online`, `onu_offline`, Rx-Pegel `min`/`avg`/`p05`). Je ONU nur Zustandswechsel und Down-Ursache als Ereignis; Rx-Pegel je ONU nur bei Verletzung der Schwelle und im ersten Lauf jeder UTC-Stunde als Stichprobe.
2. Alle Werte je ONU in jedem Intervall.
3. Nur Aggregate je Port.

**Rechnung (eine OLT, 8.192 ONUs, 128 Ports, Intervall 5 min, etwa 200 B je `metric_sample`-Zeile mit Labels und Index):**

- Option 2: 8.192 × 3 Werte × 288 = 7,08 Mio. Zeilen/Tag (≈ 1,4 GB/Tag unkomprimiert), in 400 Tagen 2,83 Mrd. Zeilen (≈ 570 GB unkomprimiert, bei Kompression 1:10 ≈ 57 GB). Schreibrate: 24.576 Zeilen je Lauf.
- Option 1: Port-Aggregate 128 × 5 × 288 = 184.320 Zeilen/Tag; stündliche Rx-Stichprobe 8.192 × 24 = 196.608 Zeilen/Tag; Schwellenverletzungen und ONU-Wechsel im Normalbetrieb wenige Hundert. Zusammen ≈ 0,38 Mio. Zeilen/Tag (≈ 76 MB/Tag), in 400 Tagen ≈ 152 Mio. Zeilen (≈ 30 GB unkomprimiert, ≈ 3 GB komprimiert). Das ist etwa Faktor 18 weniger als Option 2. Ohne Stundenstichprobe wären es Faktor 38.
- Option 3: 184.320 Zeilen/Tag, aber ohne Bezug zur einzelnen ONU.

**Empfehlung:** Option 1. Sie erhält die Information, die eine Handlungsempfehlung braucht (welche Anschlüsse betroffen sind, LOS gegenüber Dying Gasp), und senkt das Volumen um gut eine Größenordnung. Option 2 ist bei 400 Tagen Retention und mehreren OLTs je Mandant nicht tragbar (zehn OLTs: 28 Mrd. Zeilen). Option 3 verliert den Anschlussbezug. Die Angabe „zwei Größenordnungen“ aus dem Entwurf gilt nicht; mit der Stundenstichprobe ist es Faktor 18.

**Auswirkung:** WP-1007 (Ausgabe, Grenzen `max_rows` 8.192, Vorwertspeicher je ONU aus WP-994).

**Quelle:** `backend/migrations/000020_metrics.up.sql:6–13`, `:28–35`; `collector/plugins/snmp/poll.go:103`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; PRT-11).

**Betroffene IDs:** PRT-11, PRB-06, NFR-06

**Betroffene WPs:** WP-1007 (`p3-pt-pon-core`)

<a id="e-189"></a>
## E-189 – Aktiver PPPoE-Logintest

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** PRT-10 verweist für einen aktiven PPPoE-Logintest auf diese Entscheidung. Ein echter Test (PADI/PADO, LCP, Authentisierung, IPCP) braucht einen `AF_PACKET`-Socket mit `CAP_NET_RAW`, Host-Netz im Zugangssegment und einen Testzugang des Kunden. Das Collector-Image läuft als `nobody` (`collector/Dockerfile:40`).

**Optionen:**

1. In P3 nur Session-Monitoring am BNG über SNMP bzw. gNMI (aktive Sessions je BNG und Zugangsart, Einbruch um mehr als 20 % → degraded bzw. down). Kein aktiver Test in P3.
2. Nur ein PADI/PADO-Discovery-Test ohne Anmeldung, mit `CAP_NET_RAW`. Folge: zusätzliche Fähigkeit im Container und Host-Netz; prüft nur die Erreichbarkeit des Access Concentrators.
3. Vollständiger Logintest mit Testzugang. Folge: wie Option 2, zusätzlich Zugangsdaten eines Testanschlusses je Segment.

**Empfehlung:** Option 1. Das Session-Monitoring erkennt Massenausfälle am BNG zuverlässig und ohne Privilegien. Option 2 und 3 verlangen Rechte im Container und einen Testanschluss; sie werden nur umgesetzt, wenn der Auftraggeber sie ausdrücklich verlangt (Aufgabe A-64 (Fachvorgaben Netzprotokoll- und Dienst-Probes) erledigt (2026-11-09)).

**Auswirkung:** WP-1006 schließt PRT-10 mit Session-Monitoring; ein aktiver Test ist dort „Nicht Bestandteil“.

**Quelle:** `collector/Dockerfile:40`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; PRT-10).

**Betroffene IDs:** PRT-10, PRT-18

**Betroffene WPs:** WP-1006 (`p3-pt-bng`)

<a id="e-190"></a>
## E-190 – DHCP-Testverfahren und -Umfang

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** PRT-13 verlangt einen DHCPv4-Test ohne Lease-Verbrauch, einen Relay-Modus für entfernte Segmente und `unknown` (unsupported) ohne Port-Fähigkeit. Ein DHCP-Client bindet UDP 68, im Relay-Modus 67; beide sind privilegiert. Broadcast funktioniert nur im lokalen Segment. Der Collector läuft als `nobody` (`collector/Dockerfile:40`) und hat keine DHCP-Bibliothek (`collector/go.mod:7–16`).

**Optionen:**

1. DHCPv4 mit Modus `inform` (Unicast, kein Lease) und `discover` (DISCOVER/OFFER ohne REQUEST; Relay-Modus mit `giaddr`). Port-Bindung über `CAP_NET_BIND_SERVICE` oder `net.ipv4.ip_unprivileged_port_start`; ohne Fähigkeit `unknown`/`unsupported`. Eigene minimale Kodierung nach RFC 2131/2132 in `collector/internal/dhcpv4` (WP-1011).
2. Wie Option 1, aber mit der Bibliothek `insomniacslk/dhcp`. Folge: neue Abhängigkeit mit großem Umfang für wenige Optionen ([E-195](entscheidungen.md#e-195)).
3. Zusätzlich DHCPv6 (Solicit/Advertise, UDP 546/547). Folge: weiterer Kodierer und weitere Port-Freigabe; vom Auftraggeber nicht genannt.

**Empfehlung:** Option 1. Gebraucht werden nur die Optionen 53, 54, 55, 61 und 82 (lesend). Eine eigene Kodierung von etwa 300 Zeilen ist leichter zu prüfen als eine neue Abhängigkeit. DHCPv6 folgt als eigenes WP, sobald der Auftraggeber es bestätigt.

**Auswirkung:** WP-1011 (Kodierung, `fakedhcp`), WP-1012 (Probetyp), WP-1024 (Betriebsdoku zu Port-Fähigkeit und Firewall).

**Quelle:** `collector/Dockerfile:40`; `collector/go.mod:7–16`; `docs/betrieb/firewall-matrix.md`.

**Betroffene IDs:** PRT-13, PRT-18, PRT-21

**Betroffene WPs:** WP-1011 (`p3-pt-dhcp-codec`), WP-1012 (`p3-pt-dhcp`), WP-1024 (`p3-pt-docs`)

<a id="e-191"></a>
## E-191 – Umfang TWAMP/STAMP

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** STAMP (RFC 8762) und TWAMP-Light brauchen nur UDP ohne Steuerkanal. Volles TWAMP (RFC 5357) baut vorher eine Control-Session über TCP 862 auf. Im Collector gibt es weder TWAMP- noch STAMP-Code; der einzige aktive Messweg ist das ICMP-Echo des Sweeps (`collector/plugins/sweep/icmp.go:64–111`). Die Firewall-Matrix nennt Port 862 nicht (`docs/betrieb/firewall-matrix.md`). Das Typregister führt `twamp_control` als eigenen Schlüssel mit Status `planned` (WP-934).

**Optionen:**

1. In P3 STAMP-Sender (unauthenticated), kompatibel zu TWAMP-Light (WP-1013). TWAMP-Control nur als bedingtes WP-1014 nach Bestätigung durch den Auftraggeber.
2. STAMP und TWAMP-Control (unauthenticated) beide fest in P3. Folge: 1,5 PT und ein weiterer Port ohne bekannten Bedarf.
3. Zusätzlich authentisierte und verschlüsselte Modi. Folge: Schlüsselverwaltung je Reflektor.

**Empfehlung:** Option 1. Die meisten Router-Reflektoren sprechen TWAMP-Light bzw. STAMP. Der Steuerkanal ist nur bei bestimmten Geräten nötig; ob er gebraucht wird, hängt von den Reflektoren des Auftraggebers ab. Bis zur Bestätigung bleibt `twamp_control` `planned`.

**Auswirkung:** WP-1013 schließt PRT-14 nur zum Teil; WP-1014 ist bedingt. Die Bestätigung holt Aufgabe A-62 (TWAMP-Control) erledigt (2026-11-09) ein. Rückfall: Entfällt WP-1014 nach Aufgabe A-62 bis zum Abschluss von M3.10, schließt WP-1013 PRT-14 mit der dokumentierten Einschränkung „TWAMP-Control (RFC 5357) nicht unterstützt“. `docs/plan/phase3/abdeckung.csv` führt PRT-14 dann mit `WP-1013(S)` und `abschluss_wp` WP-1013; WP-1177 führt die Einschränkung in `docs/acceptance/g3-gate.md` unter GATE-02.

**Quelle:** `collector/plugins/sweep/icmp.go:64–111`; `docs/betrieb/firewall-matrix.md`; `collector/go.mod:7–16`.

**Betroffene IDs:** PRT-14, PRT-21

**Betroffene WPs:** WP-1013 (`p3-pt-stamp`), WP-1014 (`p3-pt-twamp-control`)

<a id="e-192"></a>
## E-192 – TR-069/TR-369: lesender Adapter oder eigener ACS

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** PRT-15 verlangt, dass der Collector Gerätezustände nur aus einem vorhandenen ACS bzw. USP-Controller über dessen Northbound-API und nur mit GET liest. Ein eigener ACS (CWMP über HTTP, Connection Requests, Datenmodell) oder USP-Controller (MTP MQTT/STOMP/WebSocket) wäre ein eigenes Produkt. Endgeräte melden sich nur bei einem ACS an. Der HTTP-Client des Collectors folgt keinen Redirects, lässt aber jede Methode zu (`plugins.NewTLSClient`, `collector/plugins/tlsscope.go:80`). Auf main 6d019370 gibt es keinen TR-069-Code.

**Optionen:**

1. Lesender Adapter auf den vorhandenen ACS/Controller des Kunden, nur GET; Referenzimplementierung GenieACS-NBI, weitere ACS je über ein eigenes Adapter-WP hinter `Reader`. USP erst nach Nennung des Controllers (WP-1019).
2. Eigener ACS/USP-Controller in Reticora. Folge: verdoppelt die Funktion des Kunden-ACS; zwei Systeme verwalten dieselben CPEs.
3. Kein TR-069/USP in P3. Folge: PRT-15 bleibt offen.

**Empfehlung:** Option 1. Sie liefert den Zustand der CPEs in Reticora ohne Eingriff in die Provisionierung des Kunden und folgt demselben Nur-Lese-Prinzip wie die ADMP-Anbindung ([E-224](entscheidungen.md#e-224)). Welche ACS und Controller anzubinden sind, nennt der Auftraggeber (Aufgabe A-63 (USP) erledigt (2026-11-09)).

**Auswirkung:** WP-1018 (Adapter, Nur-GET-Client, Test gegen schreibende Methoden), WP-1019 bedingt bis zur Nennung des USP-Controllers. Der Adapter in WP-1018 deckt nur GenieACS-NBI ab; nennt der Auftraggeber mit Aufgabe A-63 ein anderes ACS, braucht es ein eigenes Adapter-WP hinter `Reader` (`collector/probe/tr069/acs.go`, neu mit WP-1018). Rückfall: Entfällt WP-1019 nach Aufgabe A-63 (USP), schließt WP-1018 PRT-15 mit der dokumentierten Einschränkung „TR-369/USP nicht unterstützt“. `docs/plan/phase3/abdeckung.csv` führt PRT-15 dann mit `WP-1018(S)` und `abschluss_wp` WP-1018; WP-1177 führt die Einschränkung in `docs/acceptance/g3-gate.md` unter GATE-02.

**Quelle:** `collector/plugins/tlsscope.go:80`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; PRT-15).

**Betroffene IDs:** PRT-15, PRT-21

**Betroffene WPs:** WP-1018 (`p3-pt-tr069`), WP-1019 (`p3-pt-usp`)

<a id="e-193"></a>
## E-193 – gNMI-Betriebsart und neue Abhängigkeiten

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** gNMI erfordert gRPC und die OpenConfig-Protobufs (`github.com/openconfig/gnmi`, `google.golang.org/grpc`). Beide sind neu in `collector/go.mod` (direkt heute: edgecore, gosnmp, NATS-Pakete, `x/crypto`, `x/net`, `yaml.v3`; `collector/go.mod:7–16`) und vergrößern das Binary. Der Scheduler (WP-951) arbeitet zyklisch je Probe mit Jitter, Limits und Pufferung. PRT-16 verlangt Get bzw. Subscribe ONCE über TLS mit höchstens 32 Pfaden, Nachrichten ≤ 4 MiB und 10.000 Updates je Antwort.

**Optionen:**

1. Get bzw. Subscribe ONCE je Intervall im Scheduler-Modell; Rückfall auf ONCE, wenn das Gerät Get mit `Unimplemented` ablehnt. Streaming (SAMPLE/ON_CHANGE) später.
2. Dauerhafte Subscribe-STREAM-Sessions je Gerät mit eigenem Lebenszyklus. Folge: eigenes Verbindungsmanagement neben dem Scheduler, Limits und Zustandsmaschine passen nicht ohne Umbau.
3. Kein gNMI in P3. Folge: PRT-16 offen, BGP und Optik nur über SNMP.

**Empfehlung:** Option 1. Sie passt zu Zustandsmodell, Limits und Pufferung aller anderen Typen. ON_CHANGE wäre für Zustandswechsel schneller, braucht aber eigenes Verbindungsmanagement und kann als Folge-WP kommen. WP-1015 prüft Lizenz (Apache-2.0), SBOM-Eintrag und Binarygröße und trägt die Abhängigkeiten in `docs/security/abhaengigkeitsscans.md` ein ([E-195](entscheidungen.md#e-195)).

**Auswirkung:** WP-1015 (Client-Kern, Abhängigkeiten), WP-1016 (Probetyp), WP-1017 (Quelle für `bgp` und `optical`).

**Quelle:** `collector/go.mod:7–16`; `docs/security/abhaengigkeitsscans.md`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; PRT-16).

**Betroffene IDs:** PRT-07, PRT-09, PRT-16, PRT-21, SEC-11

**Betroffene WPs:** WP-1015 (`p3-pt-gnmi-core`), WP-1016 (`p3-pt-gnmi`), WP-1017 (`p3-pt-gnmi-sources`)

<a id="e-194"></a>
## E-194 – RPKI-Datenquelle

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** PRT-17 verlangt einen RTR-Client (RFC 8210, Rückfall RFC 6810) zu einem Validator im Kundennetz, ROA-Validierung nach RFC 6811, höchstens 2 Mio. VRPs je Validator und eine RTR-Session je Validator und Collector. Validatoren (Routinator, rpki-client mit rtrtr, Fort, StayRTR) bieten RTR über TCP 323 bzw. 8282; einige bieten zusätzlich eine eigene HTTP-API. Im Collector gibt es weder RPKI-Code noch -Bibliothek (`collector/go.mod:7–16`).

**Optionen:**

1. Eigener RTR-Client im Collector, VRP-Tabelle im Speicher und gemeinsam für alle ROA-Probes eines Validators.
2. HTTP-API des Validators (z. B. Routinator `/api/v1/validity`). Folge: an ein Produkt gebunden; andere Validatoren brauchen eigene Adapter.
3. Beides. Folge: doppelter Pflegeaufwand.

**Rechnung:** Bei kompakter Ablage (Präfix 16 B, Länge und Max-Länge je 1 B, ASN 4 B, Ausrichtung) belegt ein VRP etwa 32–48 B. Die Grenze von 2 Mio. VRPs ergibt 64–96 MB je Validator. Ein Cache je Probe statt je Validator vervielfacht das mit der Zahl der ROA-Probes (bei 10 Probes bis zu 960 MB); deshalb hält WP-1020 den Cache gemeinsam und mit eigenem Lebenszyklus neben dem Executor.

**Empfehlung:** Option 1. RTR ist bei allen Validatoren gleich und auch für Router der Standardweg; das Protokoll ist klein (etwa zehn PDU-Typen). Der Collector prüft damit dieselbe Sicht, die die Router des Kunden sehen.

**Auswirkung:** WP-1020 (RTR-Client, gemeinsamer VRP-Cache, `fakertr`), WP-1021 (ROA-Validierung).

**Quelle:** `collector/go.mod:7–16`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; PRT-17).

**Betroffene IDs:** PRT-17, PRT-21

**Betroffene WPs:** WP-1020 (`p3-pt-rpki-rtr`), WP-1021 (`p3-pt-rpki-roa`)

<a id="e-195"></a>
## E-195 – Politik für neue Bibliotheken im Collector

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Der Collector läuft im Kundennetz. Jede Abhängigkeit vergrößert Angriffsfläche und Update-Last (COL-06 signierte Auto-Updates, `docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:14`; SEC-11 Abhängigkeitsscans und CVE-Fix-SLA, `docs/spec/katalog-v3/06-audit-sicherheit-events.md:44`). Heute hat der Collector sechs direkte Fremdabhängigkeiten (`collector/go.mod:7–16`). `golang.org/x/net` ist bereits direkt eingebunden; das Paket `dns/dnsmessage` daraus wird noch nicht genutzt. Die Abhängigkeitsübersicht steht in `docs/security/abhaengigkeitsscans.md`.

**Optionen:**

1. Eigenimplementierung für kleine Protokolle (DNS über `x/net/dns/dnsmessage`, DHCPv4, STAMP, RTR); Fremdbibliothek nur für gNMI/gRPC. Jede neue Abhängigkeit erhält im einführenden WP Lizenzprüfung und SBOM-Eintrag.
2. Etablierte Bibliotheken für alle Protokolle (`miekg/dns`, `insomniacslk/dhcp`, gNMI). Folge: mehrere neue Module mit transitiven Abhängigkeiten, jede mit eigenem CVE-Fluss.

**Empfehlung:** Option 1. Die benötigten Teilmengen sind klein, gegen eigene Fakes testbar und frei von transitiven Abhängigkeiten. gRPC und Protobuf selbst zu implementieren wäre dagegen unverhältnismäßig.

**Auswirkung:** WP-1010 (DNS ohne neue Abhängigkeit), WP-1011 (DHCP-Kodierung), WP-1013 (STAMP), WP-1020 (RTR), WP-1015 (einzige neue Abhängigkeit gRPC/gNMI mit Eintrag in `docs/security/abhaengigkeitsscans.md`).

**Quelle:** `collector/go.mod:7–16`; `docs/security/abhaengigkeitsscans.md`; `docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:14`; `docs/spec/katalog-v3/06-audit-sicherheit-events.md:44`.

**Betroffene IDs:** COL-06, SEC-11, PRT-12, PRT-13, PRT-14, PRT-16, PRT-17, PRT-21

**Betroffene WPs:** WP-1010 (`p3-pt-dns`), WP-1011 (`p3-pt-dhcp-codec`), WP-1013 (`p3-pt-stamp`), WP-1015 (`p3-pt-gnmi-core`), WP-1020 (`p3-pt-rpki-rtr`)

**Nachtrag 2026-11-13 (Update-Plan Upd-03, nach Empfehlung):** Die bestehenden Fremdabhängigkeiten des Collectors bleiben dieselben, ihre Versionen werden wegen Hersteller-Advisories angehoben, die `govulncheck` nicht kennt (nur Repository-Advisories, nicht in vuln.go.dev oder OSV): gosnmp v1.43.2 → v1.47.0 (GHSA-w96x-5v55-24xx, critical, SNMPv3-Auth-Bypass und Remote-Panic im Polling), `golang.org/x/crypto` v0.58.0 → v0.59.0 (GO-2026-5932) mit `x/sys` v0.50.0 und `x/text` v0.44.0, `klauspost/compress` v1.20.0 → v1.20.4 (GO-2026-6865). Neue Bibliotheken kommen nicht hinzu. Die Abhängigkeiten, die gRPC nur im Modulgraphen nennt (`go-jose/v4` v4.1.4, `go.opentelemetry.io/otel*` v1.44.0), werden über versionierte `replace`-Direktiven in `collector/go.mod` auf gefixte Versionen gehoben (v4.1.9 bzw. v1.47.0); sie werden nicht gebaut und vergrößern das Binary nicht. Diese Graph-Pins sind bei jedem gRPC-Update mit einer OSV-Abfrage über `go list -m all` zu prüfen. Die Übersicht steht in `docs/security/abhaengigkeitsscans.md` („Nachtrag 2026-11-13: Go-Module mit Repository-Advisories“). Die Lücke im Gate für Repository-Advisories schließt Upd-04 (SEC-11).

<a id="e-196"></a>
## E-196 – Alarm- und Störungsmodell

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** Heute liegt der Alarmzustand nur einmal je Regel in `alert_rule.pending_since` und `last_fired_at` (`backend/migrations/000037_alert_rule.up.sql:21–22`); `alert_rule` hat nur eine Org-Policy. ALR-01 verlangt Instanzen je Quelle und Serie mit Dedup-Schlüssel, die Zustände pending, open, acknowledged und resolved, Unterdrückungsgründe, ein append-only-Protokoll und 400 Tage Aufbewahrung. ALR-03 verlangt die Gruppierung zu Störungen mit Wurzel.

**Optionen:**

1. `alert_rule` um Zustandsspalten je Serie erweitern (`jsonb`). Folge: kein Kunden-/Standort-Scope je Instanz, kein eindeutiger Dedup-Index, keine Historie.
2. Neue Tabellen `alarm` (Instanz), `alarm_transition` (append-only) und `incident` (Störung mit Wurzel, Ursache, Konfidenz); `alert_rule` bleibt Regeldefinition. Alle mit RLS, FORCE RLS, Truncate-Guard und Fremdschlüsseln mit `organization_id` (Konventionen 5 und 19).
3. Externe Alarm-Engine (Alertmanager). Folge: Mandantentrennung und Nachvollziehbarkeit außerhalb von RLS und Audit.

**Rechnung (Annahme 2.000 Alarme/Tag je Instanz, 4 Übergänge je Alarm):** 2.000 `alarm`-Zeilen/Tag, in 400 Tagen 0,8 Mio.; 8.000 `alarm_transition`-Zeilen/Tag, in 400 Tagen 3,2 Mio. Bei etwa 300 B je Zeile mit Indizes sind das zusammen ≈ 1,2 GB. Ohne Korrelation wären es bei einem Router-Ausfall mit 500 abhängigen CPEs 500 Alarme; mit `incident` bleibt eine benachrichtigte Störung.

**Empfehlung:** Option 2. Instanzen brauchen eigene RLS-Scopes (Kunde und Standort des CIs), einen eindeutigen Teilindex `(organization_id, dedup_key) WHERE state <> 'resolved'` und eine 400-Tage-Historie. Option 3 bricht Mandantentrennung und Nachvollziehbarkeit.

**Auswirkung:** WP-981 (Tabellen `alarm`, `alarm_transition`, 400-Tage-Aufbewahrung über den Systemjob `alarm.retention`), WP-982 (Lebenszyklus), WP-983 (API unter `/api/v1/monitoring/alarms`), WP-1027 (Tabellen `incident`, `incident_transition`; ergänzt `alarm.retention` um beide), WP-1154 (Trigger-Ausnahmen des Purgers für `alarm_transition` und `incident_transition`), WP-1113 (Folgeaktionen im Integrationstest).

**Quelle:** `backend/migrations/000037_alert_rule.up.sql:8–25`; `backend/migrations/000144_tenant_truncate_guard.up.sql:23`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; ALR-01, ALR-03).

**Betroffene IDs:** ALR-01, ALR-02, ALR-03, MON-04, NFR-06

**Betroffene WPs:** WP-981 (`p3-alr-schema`), WP-982 (`p3-alr-lifecycle`), WP-983 (`p3-alr-api`), WP-1027 (`p3-inc-schema`), WP-1113 (`p3-mon04-close`), WP-1154 (`p3-tlc-delete-purge-db`)

<a id="e-197"></a>
## E-197 – Verfahren der Root-Cause-Korrelation

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Die Auftraggeber-Vorgaben 6, 10 und 11 verlangen Root-Cause, Konfidenz und Begründung; die Abgrenzung schließt eine nicht nachvollziehbare ML-Blackbox aus. ALR-03 verlangt die Korrelation über sichere Impact-Pfade mit Redundanz, unabhängig von der Eingangsreihenfolge. Heute wertet der Evaluator jede Regel einzeln aus (`backend/internal/monitoring/evaluator.go:141–150`); sichere Ausfälle mit Redundanz berechnet nur der Topologie-Handler (`certainDown`, `backend/internal/topology/redundancy.go:276`).

**Optionen:**

1. Deterministische Topologie-Korrelation über sichere Impact-Pfade mit Redundanz, dazu gewichtete und offengelegte Ursachenfaktoren (Collector-Ausfall, Konfigurationsänderung, Interface-Down, Strom). Zeitliche Nähe geht nur als Faktor ein.
2. Statistische Ereigniskorrelation (Zeitfenster-Cluster). Folge: Ergebnis hängt von Eingangsreihenfolge und Fensterlage ab; Begründung nur „gleichzeitig“.
3. ML-Modell. Folge: von der Abgrenzung ausgeschlossen.

**Empfehlung:** Option 1. Sie ist reproduzierbar, mit Golden-Fällen testbar und erklärbar: Jede Konfidenz setzt sich aus benannten Faktoren zusammen und erscheint als Begründungszeile (ALR-09 c).

**Auswirkung:** WP-1028 (topologische Korrelation), WP-1041 (Ursachenbewertung mit Konfidenz), WP-1037 (betroffene Anschlüsse), WP-1043 (Handlungsempfehlung), WP-1057 (Konfigurationsänderung als Faktor).

**Quelle:** `backend/internal/monitoring/evaluator.go:141–150`; `backend/internal/topology/redundancy.go:276`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; ALR-03, ALR-09).

**Betroffene IDs:** ALR-03, ALR-07, ALR-09, IMP-04, IMP-05

**Betroffene WPs:** WP-1028 (`p3-rca-topology`), WP-1037 (`p3-inc-affected`), WP-1041 (`p3-rca-cause`), WP-1043 (`p3-inc-recommend`), WP-1057 (`p3-cfg-correlate`)

<a id="e-198"></a>
## E-198 – Korrelationsfenster vor der Benachrichtigung

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 3)
**Kontext:** Eltern- und Kind-Probes erkennen einen Ausfall unterschiedlich schnell (deterministischer Jitter nach PRB-04, Down-Count, Übertragung). Ohne Wartezeit gehen Kind-Alarme als Wurzel hinaus. ALR-02 verlangt das Einreihen der Wurzelbenachrichtigung nach Ablauf eines Korrelationsfensters (Standard 30 s, je Org 0–300 s). Heute benachrichtigt der Evaluator je Regel sofort (`backend/internal/monitoring/evaluator.go:221`).

**Optionen:**

1. Sofort benachrichtigen, Symptome nachträglich zusammenfassen. Folge: Bei einem Router-Ausfall gehen die Kind-Alarme vor dem Eltern-Alarm hinaus.
2. Festes Fenster 30 s. Folge: Kunden mit anderen Intervallen oder Down-Counts können nicht nachsteuern.
3. Fenster je Org einstellbar 0–300 s, Standard 30 s; Alarme an CIs ohne Upstream-Provider (Tiefe 1) ohne Wartezeit.

**Rechnung:** Bei 30 s Intervall, 10 s Timeout und Down-Count 5 meldet eine Probe `down` etwa 4 × 30 s + 10 s = 130 s nach dem ersten Fehlschlag. Eltern und Kind mit gleichen Werten unterscheiden sich nur in der Startphase (höchstens ein Intervall, 30 s) und der Übertragung (p95 < 5 s nach PRB-05). Ein Fenster von 30 s deckt die Phasendifferenz damit im Regelfall ab. Bei abweichenden Down-Counts wächst die Differenz um 30 s je Zählschritt; dafür ist der Wert bis 300 s einstellbar.

**Empfehlung:** Option 3. Mit Jitter erreicht der Elternzustand typischerweise innerhalb eines Intervalls das Backend. Der einstellbare Wert hält die Latenz nach ALR-02 unter Kontrolle, und CIs ohne Abhängigkeit warten nicht.

**Auswirkung:** WP-1028 (Port `SettleSource`, sofortige Entscheidung ohne Provider), WP-1030 (`settle_seconds` in den Org-Einstellungen).

**Quelle:** `backend/internal/monitoring/evaluator.go:221`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; ALR-02, PRB-04, PRB-05).

**Betroffene IDs:** ALR-02, ALR-03, PRB-04, PRB-05

**Betroffene WPs:** WP-1028 (`p3-rca-topology`), WP-1030 (`p3-alr-settings`)

<a id="e-199"></a>
## E-199 – Redundante Verbindungen modellieren

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Der Systemtyp `uplink_to` ist nicht redundant: Migration 000113 setzt `('uplink_to', true, 'network', 'source_depends_on_target', false)` (`backend/migrations/000113_relationship_type_rel01.up.sql:52`), der eingebaute Katalog denselben Wert (`backend/internal/relationshiptype/model.go:205`). IMP-04 kennt Redundanz bisher nur für `powered_by` und `member_of_cluster` (`docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:110`). Hat ein CPE zwei Uplinks und fällt einer aus, gilt es als sicher down; die „noch funktionierende redundante Verbindung“ (Auftraggeber-Vorgabe 10, ALR-09 e) ist nicht bestimmbar. Mit `redundant = true` fällt ein Abhängiger erst, wenn alle Provider des Typs ausgefallen sind (`certainDown`, `backend/internal/topology/redundancy.go:276`).

**Optionen:**

1. `uplink_to` auf `redundant = true` setzen. Bei einem Uplink ist das Ergebnis identisch, bei mehreren gilt „alle müssen ausfallen“.
2. Neuer Systemtyp `redundant_uplink_to`. Folge: Bestandskanten müssen umgetypt werden, Discovery muss entscheiden, welcher Typ entsteht.
3. Redundanzgruppen als eigenes Objekt (z. B. LAG mit Mindestanzahl). Folge: neues Modell, neue API, neue Oberfläche.

**Empfehlung:** Option 1. Die Semantik von `redundant` ist bei genau einem Uplink identisch mit der heutigen. Bestehende Impact-Ergebnisse ändern sich nur bei Mehrfach-Uplinks, und dort korrekt. `readFeeds` (`redundancy.go:69–86`) lädt Provider redundanter Typen jenseits der Tiefe nach; für Graphen mit `uplink_to` kommen bis zu zwei Abfragen hinzu, die WP-1026 gegen die Budgets aus Konvention 23 misst. Option 3 erst bei Bedarf.

**Auswirkung:** WP-1026 (Migration, Katalog, Seed-Test `TestRelationshipTypeSeedsFollowREL01`), WP-1038 (redundante Verbindungen je Störung).

**Quelle:** `backend/migrations/000113_relationship_type_rel01.up.sql:52`; `backend/internal/relationshiptype/model.go:205`; `backend/internal/topology/redundancy.go:69–86`, `:276`; `docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:110`.

**Betroffene IDs:** REL-01, IMP-04, IMP-05, ALR-03, ALR-09

**Betroffene WPs:** WP-1026 (`p3-topo-uplink-redundant`), WP-1038 (`p3-inc-redundancy`)

<a id="e-200"></a>
## E-200 – Flapping-Parameter

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** ALR-04 verlangt: N Zustandswechsel im Fenster W (Standard 5 in 15 min, je Org und Regel einstellbar) markieren den Alarm als flapping, genau eine Benachrichtigung, Austritt nach einer stabilen Phase H (Standard 15 min). Heute gibt es gegen Flattern nur die Mindestdauer je Regel (`backend/internal/monitoring/evaluator.go:214–216`).

**Optionen:**

1. Fest 5 Wechsel in 15 min, Austritt nach 15 min ohne Wechsel.
2. Standard wie Option 1, je Org und je Regel überschreibbar.
3. Gewichtete Flap-Rate wie Nagios (Prozent über 21 Prüfungen). Folge: schwer erklärbar; der Schwellenwert hängt vom Intervall ab.

**Rechnung:** Bei 30 s Intervall, Down-Count 5 und Up-Count 1 braucht ein vollständiger Zyklus down → up mindestens 6 Prüfungen (3 min). 5 Wechsel in 15 min (30 Prüfungen) entstehen damit nur bei echtem Flattern, nicht durch einzelne Paketverluste.

**Empfehlung:** Option 2. Einfach erklärbar, passt zu 30-s-Intervallen und erfüllt die Einstellbarkeit je Org und Regel aus ALR-04. Option 3 ist schwer nachvollziehbar.

**Auswirkung:** WP-1030 (Org-Standardwerte), WP-1031 (Erkennung mit Hysterese, Überschreibung je Regel). Eine Überschreibung je Probe ist nicht Bestandteil.

**Quelle:** `backend/internal/monitoring/evaluator.go:214–216`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; ALR-04).

**Betroffene IDs:** ALR-04, ALR-01

**Betroffene WPs:** WP-1030 (`p3-alr-settings`), WP-1031 (`p3-alr-flapping`)

<a id="e-201"></a>
## E-201 – Schwellwertsprache

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** Eine Regel kennt heute nur `gt`, `lt` und `eq` gegen einen Wert (`conditionMet`, `backend/internal/monitoring/evaluator.go:12–23`; CHECK in `backend/migrations/000037_alert_rule.up.sql:13`). ALR-05 verlangt eine MET-45-AST mit Stufen warning und critical, Aggregation (last, avg, max, min, rate) über ein Fenster, Mindestdauer und Rückkehrhysterese. CH18 verlangt JSON-AST mit Klick-Builder und keine Text-DSL (`docs/spec/katalog-v3/00-grundlagen.md:35`). Die Grammatik besteht mit Go- und TypeScript-Auswertung und gemeinsamem Konformitätstestset (`backend/internal/rules/rules.go`, `frontend/src/rules/rules.ts`, `api/rules.conformance.json`, geladen in `backend/internal/rules/rules_test.go:54`).

**Optionen:**

1. Eigene Felder `condition`, `threshold` beibehalten und um Stufen ergänzen. Folge: zweite Bedingungssprache neben MET-45; Aggregation und Hysterese als Sonderfelder.
2. MET-45-Bedingungs-AST über einen Metrikkontext (`value`, `avg`, `max`, `min`, `rate`) plus Spalten für Stufen, Fenster, Dauer und Clear-Bedingung.
3. PromQL-ähnliche Text-DSL. Folge: widerspricht CH18.

**Empfehlung:** Option 2. CH18 verlangt JSON-AST mit Klick-Builder, und die Go/TS-Konformität besteht schon. Altregeln mit `condition`/`threshold` bleiben lesbar und werden von WP-985 übernommen.

**Auswirkung:** WP-984 (Auswertung je Serie), WP-985 (AST, Stufen, Hysterese), WP-990 (Regel-Editor mit AST-Builder).

**Quelle:** `backend/internal/monitoring/evaluator.go:12–23`; `backend/migrations/000037_alert_rule.up.sql:13`; `backend/internal/rules/rules_test.go:54`; `frontend/src/rules/rules.ts`; `docs/spec/katalog-v3/00-grundlagen.md:35`; `docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:14` (MET-45).

**Betroffene IDs:** ALR-05, MET-45, MON-04, CH18

**Betroffene WPs:** WP-984 (`p3-alr-series-eval`), WP-985 (`p3-alr-threshold`), WP-990 (`p3-alr-ui-rules`)

<a id="e-202"></a>
## E-202 – Quelle der CI-Health (LCY-06)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** LCY-06 verlangt die Health aus Alerting (MON-04) und Findings (SEC-02/03), nicht manuell setzbar (`docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:52`). `ci_refresh_health` vergleicht heute das jüngste Rohsample je Metrik direkt mit `alert_rule.condition` und `threshold` und setzt `unknown` für CIs ohne Sample (`backend/migrations/000090_ci_health_lifecycle.up.sql:63–102`). Mindestdauer, Serien, Unterdrückung, Probe-Zustände und Findings spielen keine Rolle; AST-Regeln aus [E-201](entscheidungen.md#e-201) sieht die Funktion nicht.

**Optionen:**

1. Beibehalten. Folge: Health und Alarmansicht widersprechen sich (Hysterese, Wartung, Netzdown); AST-Regeln fehlen.
2. Health = höchste Schwere offener, nicht wartungsunterdrückter Alarme des CIs (`symptom` zählt höchstens als warning), dazu offene Findings; `unknown` ohne aktiven Check; bei Netzdown zählen Check-Alarme nicht.

**Empfehlung:** Option 2. Die Health ist konsistent mit der Alarmansicht, Hysterese und Flapping wirken auch auf sie, und Findings docken in WP-1139 an. Die Signatur von `ci_refresh_health` bleibt.

**Auswirkung:** WP-989 (Health aus Alarmen), WP-1139 (Findings, Anzeige der Ursache).

**Quelle:** `backend/migrations/000090_ci_health_lifecycle.up.sql:44–46`, `:63–102`; `docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:52`.

**Betroffene IDs:** LCY-06, MON-04, CI-01, SEC-02, SEC-03

**Betroffene WPs:** WP-989 (`p3-alr-health`), WP-1139 (`p3-health-findings`)

<a id="e-203"></a>
## E-203 – Graphanalyse für Worker

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** Impact, Redundanz und Dienstkette sind nur über `*topology.Handler` und eine HTTP-Anfrage erreichbar (z. B. `(*Handler).graphFromRoots`, `backend/internal/topology/topology.go:479`; `impactOf`, `backend/internal/topology/impact.go:423`; `(*Handler).readFeeds`, `backend/internal/topology/redundancy.go:69`). Korrelation (WP-1028) und Wartung (WP-1033, WP-1034) laufen als Worker ohne Anfrage im Org-weiten Scope. Die Budgets aus Konvention 23 (`TestImpactWorstCase`, `TestTopologyQueryBudget`) gelten für die Graphlogik.

**Optionen:**

1. Die Korrelation ruft die HTTP-API intern auf. Folge: Authentisierung, Serialisierung und Feldrechte für einen internen Aufruf; Fehlerbilder der HTTP-Schicht im Worker.
2. Eigener Analyzer-Dienst in `topology` ohne `net/http` (`topology.Analyzer`), den Handler und Worker nutzen.
3. Eigene vereinfachte Graphlogik im Alarmpaket. Folge: Abweichung von IMP-04/05, doppelte Pflege.

**Empfehlung:** Option 2. Eine Logik für Impact, Redundanz und Budgets (Konvention 23) und keine Doppelimplementierung. Der Handler delegiert an den Analyzer; die bestehenden HTTP-Tests bleiben der Nachweis.

**Auswirkung:** WP-1025 (Analyzer mit `Impact`, `Upstream`, `Roots`), Nutzer WP-1028, WP-1033, WP-1034, WP-1037, WP-1038.

**Quelle:** `backend/internal/topology/topology.go:479`; `backend/internal/topology/impact.go:423`; `backend/internal/topology/redundancy.go:69`; `docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:110–112`.

**Betroffene IDs:** IMP-04, IMP-05, ALR-03

**Betroffene WPs:** WP-1025 (`p3-topo-analyzer`)

<a id="e-204"></a>
## E-204 – Umfang der Wartungsunterdrückung

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** MGT-04 verlangt Wartungsfenster mit Benachrichtigung betroffener Kunden über den Graphen (`docs/spec/katalog-v3/09-module-phase2plus.md:46`). ALR-11 verlangt die Unterdrückung für Fenster-CIs und sicher betroffene CIs (IMP-05) und den Ausschluss aus der SLA-Zählung. Wartungsfenster (`maintenance_window`, `maintenance_window_ci`, `backend/migrations/000048_maintenance.up.sql`) sind heute nicht mit dem Monitoring verbunden. IMP-05 unterscheidet sichere und heuristische Betroffenheit.

**Optionen:**

1. Nur die Fenster-CIs. Folge: Alarmflut aus abhängigen CIs bei geplanten Arbeiten.
2. Fenster-CIs und sicher betroffene CIs (Impact `certain`); heuristisch betroffene CIs nur markieren (`details.maintenance_heuristic`) und normal benachrichtigen.
3. Alles einschließlich heuristisch betroffener CIs. Folge: echte Störungen hinter heuristischen Kanten werden verdeckt.

**Empfehlung:** Option 2. Sie verhindert Alarmfluten durch geplante Arbeiten, ohne über heuristische Kanten echte Störungen zu verdecken. Sie entspricht dem Wortlaut von ALR-11.

**Auswirkung:** WP-1033 (Unterdrückung, SLA-Ausnahme `maintenance_s`), WP-1034 (Empfänger über Graph-Impact, ersetzt WP-228).

**Quelle:** `backend/migrations/000048_maintenance.up.sql`; `docs/spec/katalog-v3/09-module-phase2plus.md:46`; `docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:112`.

**Betroffene IDs:** MGT-04, ALR-11, ALR-06, IMP-05, NTF-05

**Betroffene WPs:** WP-1033 (`p3-maint-suppress`), WP-1034 (`p3-maint-recipients`)

<a id="e-205"></a>
## E-205 – Automatische Tickets aus Monitoring

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** TKT-01 verlangt Tickets aus Monitoring in [P3] (`docs/spec/katalog-v3/09-module-phase2plus.md:66`). Die Auftraggeber-Vorgabe 10 verlangt die Angabe, ob bereits ein Ticket existiert (ALR-09 d). Der Ticket-Service legt mit `opts.Exclusive` höchstens ein nicht geschlossenes Ticket je Ziel an und sperrt das Ziel vorher (`backend/internal/ticket/service.go:113–176`; `LockOpenTicket`, `backend/internal/ticket/pg_repository.go:977`).

**Optionen:**

1. Immer automatisch je Störung. Folge: Ticketfluten bei kurzen Ausfällen und Flattern.
2. Aktion `ticket` je Regel bzw. Ursachenklasse konfigurierbar (Standard aus), sonst Vorschlag in der Störungsansicht; Dedup über das exklusive Ziel `incident` und vorhandene Tickets am Wurzel-CI.
3. Nie automatisch. Folge: TKT-01 [P3] nur als manuelle Anlage aus der Störung.

**Empfehlung:** Option 2. Sie vermeidet Ticketfluten und nutzt den vorhandenen Exklusiv-Mechanismus statt einer eigenen Sperrlogik.

**Auswirkung:** WP-1039 (vorhandene Tickets finden; Ticket-Link-Typen `alarm` und `incident`, nicht `alert`), WP-1040 (Anlage mit `reporter_id` = Nutzer, der die auslösende Regel bzw. Einstellung gespeichert hat, sonst `initiated_by`; kein System-Service-Account als Reporter; `close_on_resolve`), WP-1113 (Integrationstest der Folgeaktionen).

**Quelle:** `backend/internal/ticket/service.go:113–176`; `backend/internal/ticket/pg_repository.go:977`; `docs/spec/katalog-v3/09-module-phase2plus.md:66`.

**Betroffene IDs:** TKT-01, ALR-09, MON-04, WFL-06

**Betroffene WPs:** WP-1039 (`p3-inc-ticket-lookup`), WP-1040 (`p3-alr-ticket-create`), WP-1113 (`p3-mon04-close`)

<a id="e-206"></a>
## E-206 – Quelle der nächsten Schritte

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** ALR-09 (f) verlangt nächste Schritte aus einem Playbook-Katalog je Ursachenklasse. Lokalisierte Systemtexte führt das Backend heute nur für Benachrichtigungen als Go-Tabelle je Ereignistyp und Locale (`backend/internal/notify/templates.go`). Die Ursachenklassen definiert WP-1041.

**Optionen:**

1. Fest im Code. Folge: Kundenprozesse (Meldeweg an den Netzbetreiber, Vertrags-ID) sind nicht abbildbar.
2. System-Playbook-Katalog je Ursachenklasse (versioniert, `de-DE` und `en-US`) mit Platzhaltern und Ersatztexten, dazu Org-Overrides je Klasse.
3. Frei je Störung durch Nutzer. Folge: keine Vorschläge bei Eröffnung der Störung.

**Empfehlung:** Option 2. Nachvollziehbar und an Kundenprozesse anpassbar (z. B. „Störung bei {carrier} mit Vertrags-ID {contract_id} melden“). Fehlende Platzhalterwerte, etwa ADMP-Daten vor A-55, werden durch neutrale Ersatztexte gefüllt.

**Auswirkung:** WP-1042 (Katalog, Tabelle `alarm_playbook`), WP-1043 (Empfehlungsobjekt).

**Quelle:** `backend/internal/notify/templates.go`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; ALR-09).

**Betroffene IDs:** ALR-09

**Betroffene WPs:** WP-1042 (`p3-inc-playbook`), WP-1043 (`p3-inc-recommend`)

<a id="e-207"></a>
## E-207 – Speicherung von Konfigurationen

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** ALR-07 verlangt, dass der Collector die Running-Config per SSH holt, Geheimnisse vor dem Versand maskiert und nur bei geändertem SHA-256 sendet; das Backend speichert Versionen 400 Tage und bildet Diffs. Heute überträgt die Discovery nur `os_name`, `os_version` und `firmware_version` (`backend/internal/discovery/devicerecord.go:216–224`). Das SSH-Plugin begrenzt eine Ausgabe auf 64 KiB (`maxOutput`, `collector/plugins/ssh/exec.go:44`) und führt nur feste Befehle aus ([E-187](entscheidungen.md#e-187)). Konfigurationen enthalten Geheimnisse (Passwörter, Communities, Pre-Shared-Keys).

**Optionen:**

1. Nur Hash. Folge: Änderung erkennbar, aber kein Diff (ALR-07 verlangt Diffs).
2. Hash plus maskierter Volltext, gzip-komprimiert, Größenlimit 2 MiB unkomprimiert, Retention 400 Tage, in der Mandantentabelle `config_snapshot` mit RLS. Maskierung im Collector.
3. Volltext in S3. Folge: zweiter Speicher außerhalb von RLS, Backup und Restore-Test.

**Rechnung (Annahme 2.000 Geräte, mittlere Konfiguration 40 KiB, gzip 1:5, eine Änderung je Gerät und Woche):** ≈ 8 KiB je Version; 2.000 × 57 Wochen ≈ 114.000 Versionen in 400 Tagen ≈ 0,9 GB. Schreibrate: ≈ 290 Zeilen/Tag; der tägliche Zeitplanabruf ohne Änderung schreibt nichts. Obergrenze je Zeile 2 MiB (`CHECK` in WP-1052). Die jüngste Version je CI bleibt auch nach 400 Tagen erhalten.

**Empfehlung:** Option 2. Ein Diff braucht den Text. Die Maskierung im Collector verhindert, dass Geheimnisse die Kundenumgebung verlassen, und das Größenlimit begrenzt den Speicher. Eine reine Geheimnisänderung ist nach der Maskierung nicht erkennbar; das ist hinnehmbar.

**Auswirkung:** WP-1052 (Tabelle, Retention-Lauf), WP-1053 (Abruf über feste zweite Allowlist `ConfigCommands`, Maskierung, Hash), WP-1056 (Versionen und Diff).

**Quelle:** `backend/internal/discovery/devicerecord.go:216–224`; `collector/plugins/ssh/exec.go:21–44`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; ALR-07).

**Betroffene IDs:** ALR-07, SEC-01, NFR-06

**Betroffene WPs:** WP-1052 (`p3-cfg-schema`), WP-1053 (`p3-cfg-collect-ssh`), WP-1056 (`p3-cfg-diff-api`)

<a id="e-208"></a>
## E-208 – Prognoseverfahren

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** ALR-08 verlangt einen täglichen Trend aus Stundenaggregaten (lineare Regression über 30/90 Tage, optional mit Wochensaisonalität) mit Steigung, Bestimmtheitsmaß und prognostiziertem Überschreitungszeitpunkt mit Prognoseintervall; das Verfahren ist deterministisch und offengelegt. Im Backend gibt es keine Prognoselogik. Das Stundenaggregat `metric_sample_1h` gruppiert ohne Labels (`backend/migrations/000020_metrics.up.sql:41–54`), Interfaces eines Geräts fallen darin zusammen; WP-1060 legt deshalb ein eigenes Aggregat je Serie an.

**Optionen:**

1. Lineare Regression (kleinste Quadrate) auf dem Tageswert über 30 bzw. 90 Tage, Prognoseintervall aus den Residuen.
2. Option 1 plus Wochensaisonalität (Mittel der Residuen je Wochentag abziehen, neu anpassen).
3. Holt-Winters oder ARIMA. Folge: Parameterwahl und Initialisierung schwer offenzulegen, bei 90 Tagen Daten wenig Mehrwert.

**Rechnung:** Tageswert ist das Maximum der Stundenmittel eines UTC-Tages. Bei 10.000 Interface-Serien hält das Aggregat 24 Zeilen je Serie und Tag (240.000 Zeilen/Tag, bei 100 Tagen Retention 24 Mio. Zeilen). Der tägliche Lauf liest 10.000 × 90 = 0,9 Mio. Tageswerte; Ziel ≤ 90 s (WP-1060 AK6). Wochenprofil: 7 Werte je Serie, Voraussetzung mindestens zwei Werte je Wochentag.

**Empfehlung:** Option 2. Deterministisch, erklärbar und in Go ohne Abhängigkeit umsetzbar. Interface-Last hat ein ausgeprägtes Wochenmuster; ohne Bereinigung verbreitert es das Prognoseintervall. Option 3 bringt bei 90 Tagen Daten wenig und kostet Nachvollziehbarkeit.

**Auswirkung:** WP-1059 (Bibliothek `backend/internal/alarm/forecast`, Offenlegung in `docs/betrieb/kapazitaetsprognose.md`), WP-1060 (täglicher Job, Alarm im Horizont von 30 Tagen).

**Quelle:** `backend/migrations/000020_metrics.up.sql:41–54`; `docs/spec/katalog-v3/10-phase3-ergaenzungen.md` (neu; ALR-08).

**Betroffene IDs:** ALR-08

**Betroffene WPs:** WP-1059 (`p3-cap-lib`), WP-1060 (`p3-cap-job`)

<a id="e-209"></a>
## E-209 – Sections als eigene Tabelle oder über Tags

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** phpIPAM gliedert Subnetze in Sections mit eigenen Rechten und Einstellungen (`strict_mode`, Anzeige von VLAN/VRF). IPM-01 verlangt `ipam_section` mit Default-Section je Organisation und `subnet.section_id NOT NULL`, IPM-10 Rechte je Section über RLS. Reticora gliedert Subnetze heute nur nach Kunde, Standort, VRF und VLAN (`backend/internal/ipam/model.go`; Tabelle `backend/migrations/000015_network_ipam.up.sql:22–37`). Tags (P2) haben keine RLS-Bedeutung. Vorbild für ein Default-Objekt je Organisation ist die Default-VRF (Trigger `organization_default_vrf` und `subnet_default_vrf`, Löschschutz `vrf_protect_default`, `backend/migrations/000092_vrf_model.up.sql:36–96`).

**Optionen:**

1. Tabelle `ipam_section` mit Default-Section je Organisation (Trigger und Backfill), `subnet.section_id NOT NULL`.
2. Section als Tag an Subnetzen. Folge: keine eigene Policy, kein `strict_mode`, IPM-10 nicht über RLS durchsetzbar.
3. Keine Sections, Gliederung nur nach Kunde, Standort und VRF. Folge: IPM-01 und IPM-10 offen.

**Empfehlung:** Option 1. Rechte je Section (IPM-10, [E-214](entscheidungen.md#e-214)) und `strict_mode` brauchen ein Objekt mit eigener Policy. Die Default-Section nach dem Muster der Default-VRF macht die Migration bestehender Subnetze verlustfrei.

**Auswirkung:** WP-900 (Tabelle, Default-Section, CRUD).

**Quelle:** `backend/migrations/000015_network_ipam.up.sql:22–37`; `backend/migrations/000092_vrf_model.up.sql:36–96`; `backend/internal/ipam/model.go`.

**Betroffene IDs:** IPM-01, IPM-10

**Betroffene WPs:** WP-900 (`p3-ipam-section`)

<a id="e-210"></a>
## E-210 – Speicherung der Subnetzhierarchie

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** IPM-02 verlangt Master/Nested-Subnetze über CIDR-Enthaltensein, `parent_id` beim Anlegen, Ändern und Löschen sowie Umhängen der Kinder. Eindeutig ist heute nur `(organization_id, vrf_id, cidr)` (`subnet_org_vrf_cidr_key`, `backend/migrations/000092_vrf_model.up.sql:79`). Weder Handler noch Repository prüfen Überlappung (`CreateSubnet`, `UpdateSubnet`, `backend/internal/ipam/pg_repository.go:192`, `:207`). Die Inet-Operatoren `<<` und `>>=` sind nicht leakproof und unter RLS keine Indexbedingung (SR-04, Konvention 22 des Phase-2-Plans). Jede neue Liste hält p95 bei 10.000 Objekten (Konvention 23).

**Optionen:**

1. `parent_id` gespeichert, im Schreibpfad unter einer Advisory-Sperre je `(organization_id, vrf_id)` berechnet; Hierarchieabfragen über `parent_id`.
2. Hierarchie bei jedem Lesen rekursiv aus `cidr` berechnen. Folge: Containment-Vergleiche ohne Index unter RLS.
3. `ltree`-Pfad. Folge: Umhängen ändert die Pfade aller Nachfahren; `ltree`-Operatoren sind ebenfalls nicht leakproof.

**Rechnung:** Bei 10.000 Subnetzen einer VRF prüft Option 2 für jede Hierarchieabfrage jedes Subnetz gegen jedes andere ohne Index (bis 10⁸ Vergleiche bei einer Baumansicht). Option 1 liest Kinder über Gleichheit auf `parent_id` mit Index. Beim Schreiben sucht sie das engste umschließende Netz unter den Netzen derselben VRF (einmal je Anlage) und hängt bei Bedarf die neu enthaltenen Netze um.

**Empfehlung:** Option 1. Lesen bleibt unter RLS indexierbar (Fremdschlüsselspalte, Gleichheit), neu berechnet wird nur beim Schreiben. Die Sperre je VRF verhindert Wettläufe paralleler Anlagen. Der Selbstverweis braucht `UNIQUE (id, organization_id)` an `subnet` (Konvention 19); fehlt der Schlüssel, legt WP-901 ihn bedingt an (DO-Block, Name `subnet_id_organization_key`). Option 2 scheitert am p95-Budget.

**Auswirkung:** WP-901 (Hierarchie, Pools, `strict_mode`), WP-912 (Zuordnung von Adressen nach längstem Präfix).

**Quelle:** `backend/migrations/000092_vrf_model.up.sql:79`; `backend/internal/ipam/pg_repository.go:192`, `:207`; `docs/plan/phase2/implementierungsplan.md:226–227` (Konventionen 22 und 23).

**Betroffene IDs:** IPM-02, IPM-14, NET-02

**Betroffene WPs:** WP-901 (`p3-ipam-hierarchy`), WP-912 (`p3-ipam-auto-subnet`)

<a id="e-211"></a>
## E-211 – Adresszustand neben NET-03-Typ

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** NET-03 legt `type discovered|reserved|static|dhcp` fest (`docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:60`; CHECK in `backend/migrations/000111_ip_address_net03.up.sql:21`). phpIPAM unterscheidet zusätzlich Used und Offline, und der Subnetz-Scan (WP-1063) liefert die Erreichbarkeit. IPM-04 verlangt `state active|offline|unknown` getrennt von `type`.

**Optionen:**

1. Neue Spalte `state active|offline|unknown` mit `state_changed_at`; `type` bleibt.
2. `type` um `offline` erweitern. Folge: NET-03 ändert sich, G1/G2-Nachweise zu NET-03 werden ungültig; eine reservierte Adresse könnte nicht offline sein.
3. Zustand nur aus Monitoring-Metriken ableiten. Folge: Filter und Belegung (WP-903) brauchen eine Abfrage über Zeitreihen je Adresse.

**Empfehlung:** Option 1. NET-03 bleibt unverändert, G1 und G2 bleiben gültig. Zustand und Zweck sind fachlich verschieden: Eine reservierte Adresse kann online sein. Filter und Belegung brauchen eine Spalte. Backfill: `active` bei `last_seen_at` innerhalb von 7 Tagen, sonst `unknown`.

**Auswirkung:** WP-902 (Spalten, Gateway-Markierung), WP-903 (Belegung), WP-1063 (Übernahme der Scan-Ergebnisse).

**Quelle:** `backend/migrations/000111_ip_address_net03.up.sql:15–21`; `backend/migrations/000015_network_ipam.up.sql:48`; `docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:60`.

**Betroffene IDs:** IPM-04, IPM-03, IPM-11, NET-03

**Betroffene WPs:** WP-902 (`p3-ipam-address-state`), WP-903 (`p3-ipam-usage`), WP-1063 (`p3-ipam-scan-results`)

<a id="e-212"></a>
## E-212 – VLAN-Domänen und E-24

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** [E-24](../entscheidungen.md#e-24) macht ein VLAN je Standort bzw. je Organisation (ohne Standort) eindeutig, umgesetzt als zwei Teilindizes `vlan_site_number` und `vlan_org_number` (`backend/migrations/000091_vlan_catalog.up.sql:23–24`; NET-08, `docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:70`). phpIPAM kennt L2-Domänen über mehrere Standorte. IPM-06 verlangt `vlan_domain` mit optionaler Zuordnung; ohne Domäne gilt E-24 unverändert.

**Optionen:**

1. `vlan.domain_id` optional; mit Domäne eindeutig je `(domain_id, vlan_id)`, ohne Domäne gelten die Teilindizes aus E-24.
2. Domäne Pflicht, Backfill mit einer Domäne je Standort. Folge: Datenmigration aller VLANs; E-24 wird ersetzt.
3. Keine Domänen. Folge: IPM-06 offen, standortübergreifende L2-Netze nicht abbildbar.

**Empfehlung:** Option 1. Rückwärtskompatibel und ohne Datenmigration. Kunden mit standortübergreifenden L2-Netzen nutzen Domänen; die Teilindizes aus E-24 erhalten die Bedingung `domain_id IS NULL`.

**Auswirkung:** WP-906 (Tabelle `vlan_domain`, Standortzuordnung, Indizes).

**Quelle:** `backend/migrations/000091_vlan_catalog.up.sql:23–24`; `docs/plan/entscheidungen.md#e-24`; `docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:70`.

**Betroffene IDs:** IPM-06, NET-08

**Betroffene WPs:** WP-906 (`p3-ipam-vlan-domain`)

<a id="e-213"></a>
## E-213 – Quelle des IPAM-Änderungsverlaufs

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** IPM-08 verlangt `ipam_change` (append-only) für alle Schreibpfade einschließlich Discovery-Sync mit den Quellen `manual`, `discovery`, `scan`, `import` und `allocation`. Änderungen über die API stehen heute nur im Routen-Audit (Ressource `subnet`, `backend/internal/audit/scope.go:145`). Was `SyncDiscoveredInterfaces` an Adressen anlegt, verschiebt oder entfernt (`backend/internal/ipam/pg_repository.go:905`), ist je Adresse nicht abfragbar. `ci_change` betrifft nur CIs; `entity_change` ist org-weit ohne Kunden- und Standortspalten (`backend/migrations/000055_cmdb_extensions.up.sql:538`). Vorbild ist `relationship_change` (`backend/migrations/000261_relationship_change.up.sql:31`).

**Optionen:**

1. Eigene Tabelle `ipam_change` (append-only) nach dem Muster von `relationship_change`, geschrieben in der Fachtransaktion.
2. Verlauf aus `audit_log` filtern. Folge: Discovery und Scan schreiben kein Routen-Audit; Kunden-Principals sehen `audit_log` nicht nach Scope.
3. `ci_change` um IPAM-Objekte erweitern. Folge: Umbau eines CI-bezogenen Schemas (AUD-03) und seiner Replay-Logik (AUD-08).

**Rechnung (Annahme 50.000 Adressen, 5 % Zustands- oder Bindungsänderungen je Tag):** ≈ 2.500 Zeilen/Tag, in 400 Tagen ≈ 1 Mio. Zeilen (≈ 0,3 GB bei 300 B je Zeile mit Index). Die Retention ist nach AUD-09 einstellbar; Zustandswechsel aus dem Scan dominieren das Volumen.

**Empfehlung:** Option 1. Sie erfasst alle Quellen in der Fachtransaktion, braucht keinen Umbau des Audit-Schemas und trägt Kunden- und Standortspalten für RLS.

**Auswirkung:** WP-908 (Tabelle, Schreibpfade, `GET /subnets/{id}/history` und `/ips/{id}/history`).

**Nachtrag 2026-11-12 (WP-908, nach Empfehlung):** Neben `updated_at`, `created_at` und `address_host` protokolliert `ipam_change` auch die Beobachtungsbuchhaltung `last_seen_at` und `miss_count` nicht als Feldänderung. Jeder Discovery-Sync setzt `last_seen_at` jeder beobachteten Adresse neu; bei 50.000 Adressen und einem Lauf je Stunde wären das ≈ 1,2 Mio. Zeilen/Tag statt der oben gerechneten ≈ 2.500, ohne fachliche Aussage. Anlage, Verschiebung und Entfernung durch die Discovery bleiben als `create`, `update` und `delete` erfasst.

**Quelle:** `backend/internal/audit/scope.go:145`; `backend/internal/ipam/pg_repository.go:905`; `backend/migrations/000055_cmdb_extensions.up.sql:538`; `backend/migrations/000261_relationship_change.up.sql:31`; `docs/spec/katalog-v3/06-audit-sicherheit-events.md:20` (AUD-09).

**Betroffene IDs:** IPM-08, AUD-09

**Betroffene WPs:** WP-908 (`p3-ipam-history`)

<a id="e-214"></a>
## E-214 – Durchsetzung der Rechte je Section

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** IPM-10 verlangt Rechte je Section (`ipam_section_permission(section, team, level read|write)`) zusätzlich zu `ipam:read`/`ipam:write` und eine Durchsetzung über RLS mit Nachweis im Kanalmatrix-Test (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:147`, neu). Auf main 6d019370 gilt für `subnet` und `ip_address` nur die Organisations- und Kundengrenze: Die Tabellen entstehen in `backend/migrations/000015_network_ipam.up.sql:22` und `:39`; die Policy von `ip_address` ersetzt `000061_denormalize_ci_scope.up.sql:196–226` durch eine Prüfung auf `app.org_id` und `app.client_scope`. Die Section selbst (`ipam_section`, `subnet.section_id`) legt erst WP-900 an. Team-Prädikate gibt es bisher nur über `app.team_scope` (Muster `000065_team_scope_policies.up.sql:31–32`). Dieses GUC enthält die Teams team-beschränkter Rollenzuweisungen, für org-weite Principals „alle“ (`OrgWideScope`, `backend/internal/database/tenant_scope.go:126–134`); für API-Schlüssel kommen die Teams aus den Scopes des Schlüssels (`backend/internal/identity/apikey.go:413`). Für eine Section-Regel zählt aber die Teamzugehörigkeit (`team_member`, `000014_phase2_users_teams_roles.up.sql`) des Nutzers in `app.user_id` (`tenant_scope.go:22`). TEN-05 verlangt RLS auf allen Mandantentabellen (`docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:12`). SR-04 verbietet nicht-leakproof Ausdrucksindizes auf RLS-Tabellen (`docs/security/review-g1.md`).

**Optionen:**

1. Zusätzliche restriktive Policies (`AS RESTRICTIVE`) auf `ipam_section`, `subnet`, `ip_address` und `search_document` über die STABLE-Funktion `ipam_section_denied(level) RETURNS uuid[]`. Sie liefert die Liste der gesperrten Sections: Sections der Organisation mit mindestens einem Eintrag, aber ohne Eintrag passender Stufe für ein Team des Principals (`write` schließt `read` ein). Ist `app.system` gesetzt, ist die Liste leer. Die Teams kommen aus `team_member` für `app.user_id` bzw. aus den Team-Scopes eines API-Schlüssels; ein Principal ohne Team ist von jeder Section mit Einträgen ausgeschlossen (fail-closed). Die Policies prüfen `section_id <> ALL ((SELECT ipam_section_denied(…)))`; die Teilabfrage läuft als InitPlan einmal je Abfrage, nicht je Zeile. `ip_address` erhält `section_id` per BEFORE-Trigger aus dem Subnetz und zieht sie bei einem Section-Wechsel des Subnetzes nach (Muster `trg_ip_address_vrf`/`trg_subnet_propagate_vrf`, `000092_vrf_model.up.sql:107–128`), damit die Policy ohne Join auskommt. Folge: wirkt in REST, GraphQL, Export, Suche und Jobs gleich; eine Funktion und zwei Trigger mehr; Kanalmatrix-Test deckt die Section-Grenze ab.
2. Prüfung nur in der Anwendungsschicht (Handler und Repository). Folge: kein Migrationsaufwand, aber jeder Lesepfad (Export, Suche, GraphQL-BFF, Topologie, Jobs) muss die Prüfung selbst nachziehen; ein vergessener Pfad legt Adressen offen.
3. Sections als zusätzlicher Scope-GUC `app.section_scope`, gesetzt beim Aufbau des Tenant-Scope. Folge: konsistent zu Kunde/Standort/Team, aber `TenantScope` (`tenant_scope.go:116–122`), Gleichheitsprüfung, Query-Budgets und alle Scope-Tests ändern sich für ein einziges Modul; die Ableitung „Section ohne Einträge = frei“ müsste trotzdem in SQL stehen.

**Empfehlung:** Option 1. Sicherheitsrelevante Trennung gehört nach TEN-05 in RLS; Option 2 wäre bei Export, Suche und GraphQL lückenanfällig. Die Teamzugehörigkeit statt `app.team_scope` ist nötig, weil `app.team_scope` für org-weite Principals jede Section freigäbe. Die Policies vergleichen nur die Spalte `section_id` mit der einmal berechneten Liste und erfüllen damit SR-04; Indizes auf Ausdrücke entstehen nicht.

**Auswirkung:** WP-910 legt `ipam_section_permission` (RLS/FORCE RLS, truncate guard, zusammengesetzte Fremdschlüssel nach Konvention 5 und 19), die Funktion `ipam_section_denied(text)`, die restriktiven Policies auf `ipam_section`, `subnet`, `ip_address` und `search_document` sowie die Spalte `ip_address.section_id` mit Trigger und Backfill an und ergänzt die Kanalmatrix um den Kanal `ipam-section`. Mit Stufe `read` sind Subnetze und Adressen für UPDATE und DELETE nicht sichtbar (404); die Subnetzanlage in einer beschränkten Section verlangt `write`, sonst greift WITH CHECK (403).

**Quelle:** backend/migrations/000015_network_ipam.up.sql:22, :39; backend/migrations/000061_denormalize_ci_scope.up.sql:196–226; backend/migrations/000065_team_scope_policies.up.sql:31–32; backend/migrations/000092_vrf_model.up.sql:107–128; backend/internal/database/tenant_scope.go:22, :116–134; backend/internal/identity/apikey.go:413; docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:12; docs/security/review-g1.md (SR-04)

**Betroffene IDs:** IPM-10, TEN-05

**Betroffene WPs:** WP-910 (`p3-ipam-section-acl`)

<a id="e-215"></a>
## E-215 – Verknüpfung Management-IP ↔ ip_address

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** IPM-13 verlangt `ip_address.ci_id`, eine Rolle je Adresse und eine in derselben Transaktion konsistente Management-IP (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:153`, neu). `ci.management_ip` ist eine INET-Spalte am CI (`backend/migrations/000002_ci_type_system.up.sql:38`) mit Index für die Identitätssuche (`000023_rls_variable_unification.up.sql:100–101`). Sie ist Identitätsmerkmal und Teil der Identitätssperre beim Ingest (`identityLockKeys`, `backend/internal/discovery/lanes.go:260–265`; [E-56](../entscheidungen.md#e-56)). `ip_address` kennt kein CI, nur `interface_id` (`000015_network_ipam.up.sql:39–53`); Kunde und Standort leitet ein Trigger aus Interface bzw. Subnetz ab (`000061_denormalize_ci_scope.up.sql:30`, `:80–81`). Eine Management-Adresse ohne Interface ist damit keinem Gerät zuordenbar.

**Optionen:**

1. `ip_address.ci_id` (zusammengesetzter Fremdschlüssel mit Organisation) und `role primary|secondary|vip|anycast|loopback|management`. `ci.management_ip` bleibt führend; jeder Schreibpfad, der sie ändert, legt die Adresse mit Rolle `management` in derselben Transaktion an oder verschiebt sie. Ein Trigger erzwingt `ci_id` = CI des Interfaces, wenn `interface_id` gesetzt ist. Folge: Identitätslogik, Suche (WP-859) und Ingest bleiben unverändert; interface-lose Management-Adressen sind möglich.
2. `ci.management_ip` durch einen Fremdschlüssel `ci.management_ip_id → ip_address` ersetzen. Folge: eine Quelle der Wahrheit, aber Identitätssperre, Identitätsindex, Suche, Export, Import, GraphQL und Collector-Ingest müssen umgestellt werden; hohes Regressionsrisiko im Ingest.
3. Pseudo-Interface `mgmt0` je CI, an dem die Management-Adresse hängt. Folge: kein neues Feld an `ip_address`, aber künstliche Interfaces verfälschen Interface-Zählungen, REC-07-Fehlbeobachtungen und Topologie.

**Empfehlung:** Option 1. Sie ändert keine Identitätsregel und hält die Kunden- und Standortableitung (`located-scope`) bestehen, weil `ip_address` `client_id` und `site_id` bereits führt. Der Konsistenztrigger verhindert widersprüchliche Bindungen.

**Auswirkung:** WP-911 legt `ci_id`, `role` und den Konsistenztrigger an und liefert `GET /cis/{id}/addresses`. WP-913 synchronisiert `ci.management_ip` und die Adresse mit Rolle `management` in allen Schreibpfaden (manuell, Bulk, Import, Ingest) in derselben Transaktion; höchstens eine Adresse je CI trägt die Rolle `management` (partieller Unique-Index).

**Quelle:** backend/migrations/000002_ci_type_system.up.sql:38; backend/migrations/000023_rls_variable_unification.up.sql:100–101; backend/migrations/000015_network_ipam.up.sql:39–53; backend/migrations/000061_denormalize_ci_scope.up.sql:30, :80–81; backend/internal/discovery/lanes.go:260–265; docs/plan/entscheidungen.md#e-56

**Betroffene IDs:** IPM-13

**Betroffene WPs:** WP-911 (`p3-ipam-device-binding`), WP-913 (`p3-ipam-mgmt-sync`)

<a id="e-216"></a>
## E-216 – Override-Regel für Adressbindungen aus der Discovery

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** IPM-14 verlangt `ip_address.origin manual|discovery|scan|import` und `pinned`; Discovery überschreibt keine gepinnte oder manuelle Bindung, und eine gepinnte Adresse an einem anderen Gerät erzeugt ein Review `ip_binding_conflict` (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:155`, neu). `SyncDiscoveredInterfaces` (`backend/internal/ipam/pg_repository.go:905`) ordnet beobachtete Adressen nur über `interface_id` und Host zu und legt fehlende als `type = 'discovered'` an (`:957–967`). Geschützt sind nur die Typen `reserved`, `static` und `dhcp` vor dem Entfernen nach drei Fehlbeobachtungen (`:976–983`, [E-90](../g1-korrektur/entscheidungen.md#e-90)). Eine Kennzeichnung manueller Bindungen gibt es nicht. `override.DecideAutomatedWrite` (`backend/internal/override/decide.go:127`) entscheidet nur über CI-Felder. Konflikte mit Reservierungen meldet die Synchronisation bereits als Review `duplicate_ip` (`reservationConflicts`, WP-772, [E-53](../phase2/entscheidungen.md#e-53)).

**Optionen:**

1. `origin` und `pinned` je Adresse. Die Synchronisation ändert keine Adresse mit `pinned = true` oder `origin <> 'discovery'`; sieht sie eine solche Adresse an einem anderen Gerät, legt sie ein Review `ip_binding_conflict` an (Muster `duplicate_ip` aus WP-772). Folge: kleine Änderung an einer Abfrage und einem Review-Typ; prüfbar mit Integrationstests.
2. Eigene Override-Tabelle für Adressbindungen nach dem Muster der CI-Feld-Overrides. Folge: einheitliches Konzept, aber zweite Override-Infrastruktur mit Projektion, Review und UI für einen einzigen Fall.
3. Discovery gewinnt immer. Folge: kein Aufwand, verletzt aber IPM-14 und CH9 (manuell gewinnt).

**Empfehlung:** Option 1. Klein, prüfbar und auf dem Review-Muster aus WP-772 aufgebaut. Die Quellränge je Attribut (REC-04) kann WP-1105 als Nachfolger von WP-220 später darüberlegen, ohne das Modell zu ändern.

**Auswirkung:** WP-914 ergänzt `origin` (Backfill: `discovered` → `discovery`, sonst `manual`) und `pinned`, passt `SyncDiscoveredInterfaces` an und registriert den Review-Typ `ip_binding_conflict` (REC-08-Liste, Auflösung `accept`/`dismiss`).

**Quelle:** backend/internal/ipam/pg_repository.go:905, :957–967, :976–983; backend/internal/override/decide.go:127; docs/plan/g1-korrektur/entscheidungen.md#e-90; docs/plan/phase2/entscheidungen.md#e-53; docs/spec/katalog-v3/00-grundlagen.md:17 (CH9)

**Betroffene IDs:** IPM-14, REC-04

**Betroffene WPs:** WP-914 (`p3-ipam-discovery-override`)

<a id="e-217"></a>
## E-217 – ASN und Peerings als Tabellen oder CI-Typen

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** IPM-16 und IPM-17 verlangen eine ASN-Verwaltung mit Bereichen und atomarer Vergabe sowie BGP-Soll-Peerings, denen der beobachtete Sitzungszustand der BGP-Probe zugeordnet wird (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:159`, `:161`, neu). Das Datenmodell führt VLANs und Subnetze bewusst als Tabellen, nicht als CI-Typen (`docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:30–31`).

**Optionen:**

1. Tabellen `asn`, `asn_range` und `bgp_peering` im Paket `ipam`; beobachtete Sitzungszustände kommen als Probe-Ergebnisse und werden über (Gerät, VRF, Remote-Adresse) zugeordnet. Folge: konsistent zum IPAM-Modell; Vergabe per Sperre wie bei Adressen (WP-904).
2. CI-Typen `asn` und `bgp_session`. Folge: Metamodell, Historie und Beziehungen „gratis“, aber jede Zustandsänderung einer Sitzung landet in `ci_change`, und eine atomare Vergabe aus Bereichen ist mit CI-Attributen nicht abbildbar.
3. ASN nur als Attribut am Kunden. Folge: minimal, erfüllt aber weder Bereiche noch Peerings.

**Empfehlung:** Option 1. Bereiche und atomare Vergabe brauchen Tabellen. Beobachtete Sitzungszustände sind Monitoring-Ergebnisse mit eigener Aufbewahrung und gehören nicht in die CI-Historie.

**Auswirkung:** WP-1066 legt `asn` und `asn_range` an (RLS/FORCE RLS, truncate guard, zusammengesetzte Fremdschlüssel), WP-1068 `bgp_peering` mit Fremdschlüssel auf CI und VRF. Review `bgp_peer_unknown` für beobachtete Sitzungen ohne Soll-Eintrag.

**Quelle:** docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:30–31

**Betroffene IDs:** IPM-16, IPM-17

**Betroffene WPs:** WP-1066 (`p3-asn-model`), WP-1068 (`p3-asn-bgp-peering`)

<a id="e-218"></a>
## E-218 – NAT in Phase 3

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** IPM-15 ist im Katalog als optional gekennzeichnet („NAT (optional, Umfang nach Entscheidung zur Phase)“, `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:157`, neu). In phpIPAM ist NAT ein Zusatzmodul; der Auftraggeber nennt NAT in seinen Eckwerten nicht ausdrücklich. Kein anderes WP hängt von NAT ab.

**Optionen:**

1. NAT in Phase 3 als letztes IPAM-WP (WP-1071, 1,5 PT). Folge: phpIPAM-Funktionsumfang vollständig; streicht der Auftraggeber NAT, entfällt das WP ohne Folgen für andere WPs.
2. Nach Phase 4 verschieben. Folge: 1,5 PT weniger in G3; IPM-15 bleibt offen.

**Empfehlung:** Option 1. Geringer Aufwand ohne Abhängigkeiten. Das WP steht am Ende von M3.13, sodass eine Streichung durch den Auftraggeber keine Planfolgen hat.

**Auswirkung:** WP-1071 bleibt in M3.13. Bei Option 2 wird WP-1071 als entfallen geführt und IPM-15 in der Abdeckung auf P4 gesetzt.

**Quelle:** docs/spec/katalog-v3/10-phase3-ergaenzungen.md:157 (IPM-15, neu)

**Betroffene IDs:** IPM-15

**Betroffene WPs:** WP-1071 (`p3-ipam-nat`)

<a id="e-219"></a>
## E-219 – Auflösungsreihenfolge der Zugangsdaten

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** CRD-01 führt Bindungen an CI, Gerätegruppe, Subnetz und Probe ein (`target_kind ci|device_group|subnet|probe`); CRD-04 nennt die Reihenfolge Probe > CI > Gerätegruppe > Subnetz > Scope > Kunde > Org, wobei Gerätegruppe derselbe Gruppenbegriff wie in PRB-01 ist (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:171`, `:177`, neu). Gerätegruppe ist damit die statische Gruppe `monitoring_group` mit `monitoring_group_member` (WP-935); Tags sind keine Gerätegruppe. Auf main gilt nur die Scope-Ebene: Ein Scope ohne `credential_ids` nutzt die Credentials seines Kunden und die org-weiten ([E-67](../g1-korrektur/entscheidungen.md#e-67)). Das Bundle enthält genau die gelisteten Credentials (`BundleTarget.CredentialIDs`, `relevant`, `backend/internal/credential/bundle.go:149–165`); die Scopes eines Collectors ermittelt `CollectorScopes` (`backend/internal/discovery/scope_handler.go:379`). Eine Zuordnung je Ziel und Protokoll gibt es nicht.

**Optionen:**

1. Probe > CI > Gerätegruppe (`monitoring_group`) > Subnetz > Scope > Kunde > Org, je Namespace (z. B. `snmp`, `ssh`, `http`); innerhalb einer Ebene entscheidet `priority`, bei Gleichstand die ältere Bindung. Folge: deterministisch; E-67 bleibt als untere Ebenen erhalten; die Herkunft ist je Ziel ausweisbar.
2. Nur CI > Scope. Folge: einfach, aber Gerätegruppen und Subnetze (typisch bei ISPs mit Tausenden gleich konfigurierter CPEs) müssen je CI gepflegt werden.
3. Freie Regeln (MET-45-Bedingung je Bindung). Folge: maximal flexibel, aber nicht mehr eindeutig nachvollziehbar, welche Regel gewinnt; CRD-04 verlangt eine feste Reihenfolge.

**Empfehlung:** Option 1. Die spezifischste Bindung gewinnt wie in gängigen NMS. E-67 bleibt unverändert als Scope-, Kunden- und Org-Ebene. Die Herkunft je Ziel ist nachvollziehbar (`GET /cis/{id}/credentials/effective`, CRD-04).

**Auswirkung:** WP-890 legt `credential_binding` mit `target_kind ci|device_group|subnet|probe`, `namespace` und `priority` an (ohne `tag_id`); die Ziele `device_group` und `probe` antworten bis WP-935 mit 422. WP-935 ergänzt die Fremdschlüssel `device_group_id → monitoring_group` und `probe_id → probe` (zusammengesetzt mit Organisation), schaltet beide Ziele frei und liefert die Mitgliedschaftsquelle; CRD-01 und CRD-04 schließen mit WP-935. WP-891 löst je (Ziel, Namespace) auf, die Ebene Gerätegruppe über die Schnittstelle `credential.DeviceGroupSource` (bis WP-935 leer), und erweitert das Bundle um die Zuordnung Ziel → Credential, ohne die Auswahl nach E-67 zu ändern. Ein Kunden-Credential ist nur an Ziele desselben Kunden bindbar.

**Quelle:** backend/internal/credential/bundle.go:149–165; backend/internal/discovery/scope_handler.go:379; docs/plan/g1-korrektur/entscheidungen.md#e-67; docs/spec/katalog-v3/10-phase3-ergaenzungen.md:171, :177 (CRD-01, CRD-04, neu)

**Betroffene IDs:** CRD-01, CRD-04

**Betroffene WPs:** WP-890 (`p3-cred-binding`), WP-891 (`p3-cred-resolve`), WP-935 (`p3-probe-schema-def`)

<a id="e-220"></a>
## E-220 – Ort der Auflösung von Tresor-Referenzen

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 3)
**Kontext:** CRD-02 und CRD-03 führen Tresor-Referenzen (`vault_ref`) und Tresorverbindungen mit `resolve_at collector|server` ein; CRD-04 verlangt, dass der Collector Referenzen nur im Speicher auflöst, nie auf Disk (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:173–177`, neu). COL-02 erlaubt Klartext nur im Server-Speicher zum Zeitpunkt der Übergabe und im Collector-Speicher (`docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:6`). Der Tresor eines Kunden steht oft nur im Kundennetz; server-seitige Konnektoren wie ADMP ([E-223](#e-223)) brauchen ihre Geheimnisse auf dem Server. Das Bundle wird heute auf dem Server versiegelt (`SealBundle`, `backend/internal/credential/bundle.go:70`).

**Optionen:**

1. Immer im Collector. Folge: Der Server sieht nie Klartext aus dem Tresor; ADMP und andere Server-Konnektoren können keine Tresor-Referenzen nutzen.
2. Immer auf dem Server. Folge: Der Kundentresor muss aus der SaaS-Cloud erreichbar sein; Geräte-Zugangsdaten liegen zeitweise im Server-Speicher.
3. Je Tresorverbindung `resolve_at collector|server`, Default `collector`. Folge: zwei Auflösungswege, je einer im Collector (WP-895) und auf dem Server (WP-896), mit gemeinsamer Schnittstelle.

**Empfehlung:** Option 3. Geräte-Zugangsdaten verlassen den Kundentresor nur in Richtung Collector-Speicher; der Server sieht keinen Klartext. ADMP und andere Server-Konnektoren nutzen denselben Mechanismus server-seitig. Die Tresor-Anmeldedaten (AppRole) liegen als `stored`-Credential im Bundle.

**Auswirkung:** WP-893 legt `vault_connection` mit `resolve_at` an. WP-894 führt `credential.source stored|vault_ref`. WP-895 löst im Collector auf (Speichercache, kein Schreiben auf Disk). WP-896 löst auf dem Server nur für Verbindungen mit `resolve_at server` auf. Eine Referenz einer `collector`-Verbindung ist auf dem Server nicht auflösbar (Test).

**Quelle:** docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:6; backend/internal/credential/bundle.go:70

**Betroffene IDs:** CRD-02, CRD-03, CRD-04, COL-02

**Betroffene WPs:** WP-893 (`p3-vault-connection`), WP-894 (`p3-vault-reference`), WP-895 (`p3-vault-resolver-edge`), WP-896 (`p3-vault-resolver-server`)

<a id="e-221"></a>
## E-221 – Unterstützte Tresore in Phase 3

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** COL-03 nennt eine zentrale KMS/Vault-Anbindung ohne Produkt und ist [P4] (`docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:8`). CRD-03 nennt die Arten `openbao_kv2` und `hashicorp_kv2` mit `approle` und `token` (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:175`, neu). Der Collector-Kern (`edgecore/`) hat einen lokalen Schlüsselspeicher (`edgecore/keystore`), aber keinen Tresor-Client.

**Optionen:**

1. OpenBao und HashiCorp Vault KV v2 mit AppRole und Token. Folge: ein Protokoll für zwei Produkte; ein Client in `edgecore` und auf dem Server.
2. Zusätzlich Azure Key Vault und AWS Secrets Manager. Folge: drei weitere Protokolle und Authentifizierungsarten (Managed Identity, IAM), deutlich mehr Test- und Pflegeaufwand.
3. Generischer HTTP-Tresor (konfigurierbare URL und JSON-Pfad). Folge: flexibel, aber ohne Lease- und Versionssemantik; schwer sicher testbar.

**Empfehlung:** Option 1. Cloud-Tresore und KMS für den Master-Key bleiben COL-03 in P4. Eine Schnittstelle `Resolver` hält die Erweiterung offen.

**Auswirkung:** WP-893 begrenzt `kind` per CHECK auf `openbao_kv2` und `hashicorp_kv2`. WP-895 implementiert den lesenden KV-v2-Client hinter `Resolver`. CRD-03 und COL-03 schließen mit WP-896 (WP-893 Teil), COL-03 nur im Umfang dieser Entscheidung; KMS für den Master-Key bleibt P4.

**Quelle:** docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:8; edgecore/keystore

**Betroffene IDs:** CRD-03, COL-03

**Betroffene WPs:** WP-893 (`p3-vault-connection`), WP-895 (`p3-vault-resolver-edge`), WP-896 (`p3-vault-resolver-server`)

<a id="e-222"></a>
## E-222 – Datenmodell der ADMP-Daten

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** ADMP ist führendes Fremdsystem des Auftraggebers, nur lesend angebunden ([E-224](#e-224)), und liefert Anschlüsse, Dienste, Dienst-Port-Zuordnungen und Anschlussinhaber (ADM-03 bis ADM-08, `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:193–203`, neu). ADM-03 verlangt einen Cache mit TTL und bei Fehler den letzten Stand mit `stale=true`; ADM-04 die Speicherung als `admp_service_port` mit Verweis auf `network_interface`. Subnetze und VLANs sind bewusst Tabellen, keine CI-Typen (`docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:30–31`). Personenbezogene Werte dürfen nur als Surrogat gespeichert werden (Konvention 16, `docs/plan/phase2/implementierungsplan.md:220`).

Mengengerüst (Planannahme für einen großen ISP-Mandanten): 50.000 Anschlüsse, 60.000 Dienste, 80.000 Dienst-Port-Zuordnungen. Bei etwa 1 KB je Anschluss- bzw. Dienstzeile (mit `properties`) und 0,3 KB je Zuordnung ergeben sich rund 50 MB + 60 MB + 24 MB ≈ 0,14 GB zuzüglich Indizes, also unter 0,3 GB je Mandant.

**Optionen:**

1. Eigene Cache-Tabellen `admp_connection`, `admp_service` und `admp_service_port` mit `fetched_at`, `expires_at` und `stale`, verknüpft mit `network_interface` und CI. Folge: klare Trennung von CMDB-Provenienz und Fremddaten; Daten auch bei ADMP-Störung verfügbar.
2. CI-Typen „Anschluss“ und „Dienst“. Folge: Jede Auffrischung erzeugt `ci_change`-Einträge und Provenienz; Fremddaten vermischen sich mit dem führenden CMDB-Bestand; Löschung nach TTL widerspricht der CI-Historie.
3. Kein Speicher, nur Live-Abfrage. Folge: keine PII im Bestand, aber bei einer Alarmflut (z. B. 1.000 ausgefallene Geräte × 48 Ports = 48.000 Abfragen) braucht ein Konto mit 5 Anfragen/s rund 2,7 Stunden; bei ADMP-Störung fehlen die Daten genau im Ausfall.

**Empfehlung:** Option 1. Keine Vermischung mit der CMDB-Provenienz, Datensparsamkeit über TTL, und Handlungsempfehlungen im Ausfall (ADM-08) haben Daten auch bei ADMP-Störung. Option 3 verletzt bei Alarmfluten die Ratenlimits.

**Auswirkung:** WP-925 legt `admp_account` an, WP-928 die drei Cache-Tabellen (RLS/FORCE RLS, truncate guard, zusammengesetzte Fremdschlüssel), WP-1073 füllt `admp_service_port`. Der Personenbezug folgt [E-243](#e-243).

**Quelle:** docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:30–31; docs/plan/phase2/implementierungsplan.md:220 (Konvention 16)

**Betroffene IDs:** ADM-01, ADM-03, ADM-04, ADM-07, ADM-08

**Betroffene WPs:** WP-925 (`p3-admp-account`), WP-928 (`p3-admp-cache-model`), WP-1073 (`p3-admp-ep-port-service`)

<a id="e-223"></a>
## E-223 – Laufort des ADMP-Clients

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Unbekannt ist, ob ADMP öffentlich erreichbar ist oder nur aus dem Kundennetz; die API-Doku liefert der Auftraggeber erst mit Aufgabe A-55. Der Server hat einen ausgehenden HTTP-Client mit Zielprüfung für nutzerkonfigurierte URLs (`egress.Options.ValidateURL`, `egress.NewClient`, `backend/internal/platform/egress/egress.go:157`, `:241`), den der Webhook-Dispatcher nutzt (`backend/internal/webhook/dispatcher.go:298–300`). ADM-03 verlangt Token-Bucket, Retry und Circuit-Breaker je Konto.

**Optionen:**

1. Server über den egress-Client, Basis- und Auth-URL je Konto. Folge: Nur-Lese-Garantie und Ratenlimits an einer Stelle durchsetzbar; ADMP muss aus der EU-Cloud erreichbar sein.
2. Collector im Kundennetz. Folge: ADMP muss nicht öffentlich sein, aber Nur-Lese-Transport, Ratenbegrenzung und Cache-Schreibweg laufen über NATS; Ratenlimit je Konto ist bei mehreren Collectoren verteilt durchzusetzen.
3. Je Konto wählbar. Folge: beide Wege bauen und testen; doppelter Aufwand vor Kenntnis der Doku.

**Empfehlung:** Option 1. Einfachste Nur-Lese-Garantie an einer Stelle; Ratenlimits lassen sich zentral durchsetzen. Muss ADMP laut Doku aus dem Kundennetz erreicht werden, folgt ein Zusatz-WP für einen Collector-Transport hinter derselben Client-Schnittstelle.

**Auswirkung:** WP-926 baut den Client auf `egress.NewClient`; private Ziele bleiben gesperrt, solange das Konto sie nicht ausdrücklich erlaubt. WP-925 führt `base_url` und Auth-URL am Konto.

**Quelle:** backend/internal/platform/egress/egress.go:157, :241; backend/internal/webhook/dispatcher.go:298–300

**Betroffene IDs:** ADM-01, ADM-02, ADM-03

**Betroffene WPs:** WP-925 (`p3-admp-account`), WP-926 (`p3-admp-client-frame`)

<a id="e-224"></a>
## E-224 – Technischer Nachweis der Nur-Lese-Garantie

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Der Auftraggeber verlangt: Der ADMP-Client kennt nur GET-Endpunkte und die Anmeldung, und ein Test beweist, dass POST, PUT, PATCH und DELETE technisch nicht erreichbar sind (ADM-02, `docs/spec/katalog-v3/10-phase3-ergaenzungen.md:191`, neu). Die Anmeldung ist die einzige erlaubte Nicht-GET-Anfrage. Das Paket `admp` existiert auf main noch nicht.

**Optionen:**

1. Vier unabhängige Schichten: (a) die exportierte API des Pakets hat nur `Get…`/`List…`-Methoden (Reflexionstest); (b) ein RoundTripper lässt nur GET auf die Basis-URL und genau eine Anmeldeanfrage an die konfigurierte Auth-URL zu und weist alles andere vor dem Netz ab; (c) ein AST-Test findet `http.MethodPost`, `MethodPut`, `MethodPatch`, `MethodDelete` und entsprechende Literale nur in `auth.go`; (d) ein Mock-Server-Test zählt nach allen Client-Aufrufen null Schreibanfragen. Folge: Eine spätere Codeänderung bricht mindestens einen Test.
2. Nur Konvention und Review. Folge: kein Aufwand, aber kein technischer Nachweis; erfüllt ADM-02 nicht.
3. Nur ein vorgeschalteter Proxy mit Methodenfilter. Folge: betriebliche Absicherung, aber kein Test im Repository; der Proxy müsste die Anmeldung ausnehmen.

**Empfehlung:** Option 1. Mehrere unabhängige Schichten; die Anmeldung ist an eine feste URL gebunden und im Transport genau einmal je Token-Lebensdauer erlaubt.

**Auswirkung:** WP-926 liefert Transport und Anmeldung, WP-927 die Nachweistests. Jedes Endpunkt-WP aus M3.14 führt `TestMockServerSeesNoWriteRequests` als bestehenden Test mit.

**Quelle:** docs/spec/katalog-v3/10-phase3-ergaenzungen.md:191 (ADM-02, neu)

**Betroffene IDs:** ADM-02

**Betroffene WPs:** WP-926 (`p3-admp-client-frame`), WP-927 (`p3-admp-readonly-proof`)

<a id="e-225"></a>
## E-225 – Abrufstrategie ADMP

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Die Ratenlimits von ADMP sind bis zur Doku (A-55) unbekannt; das Konto führt `rate_limit_rps` (ADM-01). Personendaten sollen minimal gehalten werden (SEC-05, `docs/spec/katalog-v3/06-audit-sicherheit-events.md:32`). Für Handlungsempfehlungen im Ausfall (ADM-08) zählen nur Dienste, die mit einem Port verknüpft sind.

Rechnung (Mengengerüst aus [E-222](#e-222)): Bei 80.000 verknüpften Ports und 24 h TTL braucht die gezielte Auffrischung 80.000 Abrufe/Tag ≈ 0,93 Anfragen/s, bei einem Konto mit 5 Anfragen/s also rund 19 % des Budgets. Eine Vollspiegelung von 50.000 Anschlüssen und 60.000 Diensten mit Inhaber und Port-Zuordnung kostet mindestens 190.000 Abrufe je Durchlauf (≈ 10,6 h bei 5 Anfragen/s) und hält alle Inhaber dauerhaft im Bestand.

**Optionen:**

1. Abruf bei Bedarf (Job `admp.refresh`) mit TTL; periodische Auffrischung nur für Dienste mit verknüpftem Port; abgelaufene Einträge werden mit ihren PII-Subjects gelöscht. Folge: frische Daten zu Ausfallzeiten, begrenzte Last und begrenzter PII-Bestand.
2. Vollspiegelung aller Anschlüsse. Folge: alle Daten lokal, aber hohe Last und vollständiger PII-Bestand ohne Verwendungszweck.
3. Nur bei Bedarf, keine Auffrischung. Folge: geringste Last, aber im Ausfall veraltete oder fehlende Daten.

**Empfehlung:** Option 1. Die Daten zu Ausfallzeiten sind frisch, und Last sowie PII-Bestand bleiben begrenzt. TTL und Auffrischintervall sind je Konto einstellbar, sobald die Doku die Limits nennt.

**Auswirkung:** WP-929 liefert `admp.refresh`, die gezielte Auffrischung, `stale`-Kennzeichnung und `admp.expire` (stündlich, löscht abgelaufene Einträge samt PII-Subjects).

**Quelle:** docs/spec/katalog-v3/06-audit-sicherheit-events.md:32; docs/spec/katalog-v3/10-phase3-ergaenzungen.md:189, :193, :203 (ADM-01, ADM-03, ADM-08, neu)

**Betroffene IDs:** ADM-03, ADM-08, SEC-05

**Betroffene WPs:** WP-929 (`p3-admp-sync-jobs`)

<a id="e-226"></a>
## E-226 – Transport der Ereignis-Trigger für Workflows

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** WFL-02 verlangt idempotente Runs mit Retries über den Event-Bus (`docs/spec/katalog-v3/09-module-phase2plus.md:90`), EVT-01 idempotente Konsumenten in `entity_seq`-Reihenfolge (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:48`). Der Outbox-Publisher liefert je Organisation in Sequenzreihenfolge an den Stream und an In-Process-Konsumenten (`Consumer`, `backend/internal/events/publisher.go:20–23`); heute ist das nur der Webhook-Dispatcher (`Dispatcher.Consume`, `backend/internal/webhook/dispatcher.go:400`). Ein Fehler stoppt den Batch der Organisation (`publisher.go:34–38`). Durable JetStream-Consumer gibt es in der Discovery (`ingestConsumerConfig`, `backend/internal/discovery/consumer.go:716–722`) und in der Job-Queue (`backend/internal/jobs/queue.go:359–361`). WFL-05 verlangt bei 10.000 Alarmzustandswechseln pro Minute (≈ 167/s) höchstens 30 s p95 bis zum Run-Start (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:215`, neu).

Durchsatz auf main: Der Publisher liest je Organisation und Takt höchstens `Batch` = 100 Ereignisse (`publisher.go:28–29`, Abfrage `:151–153`) und taktet einmal je Sekunde (`Run`, `:88–111`), also höchstens 100 Ereignisse/s je Organisation. 10.000 Zustandswechsel in einer Minute stauen sich damit auf rund 100 s Veröffentlichungszeit, bevor ein Trigger-Konsument sie sieht; das verfehlt WFL-05 unabhängig von der gewählten Option.

**Optionen:**

1. Matcher als `events.Consumer` im Outbox-Publisher (In-Process, synchron zur Veröffentlichung). Folge: keine neue Infrastruktur, aber ein Fehler oder eine langsame Abfrage im Matcher hält die Webhook-Zustellung der Organisation an.
2. Eigener Durable JetStream-Consumer `workflow-triggers` auf `events.<org>.>`, der Runs idempotent anlegt (Unique: Organisation, Definitionsversion, Ereignis-ID); die Ausführung läuft als Job-Art `workflow.run`. Folge: Webhooks und Workflows sind entkoppelt; Wiederholung über JetStream-Redelivery.
3. Polling der Outbox-Tabelle durch einen Workflow-Worker. Folge: einfach, aber ein zweiter Leser der Outbox mit eigener Positionsverwaltung; konkurriert mit dem Cleanup.

**Empfehlung:** Option 2. Ein Fehler im Matcher blockiert keine Webhooks; die Option entspricht wörtlich „über den Event-Bus“ und nutzt die in Discovery und Jobs bewährten Muster. Die Idempotenz entsteht durch den Unique-Index statt durch Konsumentenzustand.

**Auswirkung:** WP-1088 führt die Job-Art `workflow.run` und den Unique-Index, WP-1090 den Consumer. WP-982 erweitert den Outbox-Publisher (`backend/internal/events/publisher.go`, `publisher_test.go`): Je Organisation und Takt werden so lange Batches veröffentlicht, bis die Outbox leer oder ein Zeitbudget von 900 ms erreicht ist; Nachweis ≥ 200 Ereignisse/s je Organisation. WP-1110 nutzt diesen Durchsatz für die Trigger-Art `alert` auf `alarm.opened`, `alarm.acknowledged` und `alarm.resolved` (WFL-05); der Lastlauf zu NFR-11 (WP-1167) weist ihn unter Last nach.

**Quelle:** backend/internal/events/publisher.go:20–23, :28–29, :34–38, :88–111, :151–153; backend/internal/webhook/dispatcher.go:400; backend/internal/discovery/consumer.go:716–722; backend/internal/jobs/queue.go:359–361; docs/spec/katalog-v3/09-module-phase2plus.md:90; docs/spec/katalog-v3/06-audit-sicherheit-events.md:48; docs/spec/katalog-v3/10-phase3-ergaenzungen.md:215 (WFL-05, neu)

**Betroffene IDs:** WFL-02, WFL-03, WFL-05, EVT-01, NFR-11

**Betroffene WPs:** WP-982 (`p3-alr-lifecycle`), WP-1088 (`p3-wf-runqueue`), WP-1090 (`p3-wf-trigger-event`), WP-1110 (`p3-wf-alert-trigger`), WP-1167 (`p3-perf`)

<a id="e-227"></a>
## E-227 – Serialisierung der Runs pro CI

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** WFL-02 und EVT-01 verlangen eine serielle Ausführung je Entität (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:48`). Die Job-Queue vergibt Jobs mit `FOR UPDATE SKIP LOCKED` nach `run_after` und `created_at` ohne Schlüsselserialisierung (`ClaimNext`, `backend/internal/jobs/pg_repository.go:291–302`). `workflow_run` hat keinen CI-Bezug und kennt den Zustand `waiting_approval` (`backend/migrations/000027_workflows_forms.up.sql:59–71`).

**Optionen:**

1. Advisory-Lock pro (Organisation, CI) während der Ausführung, Reihenfolge nach `created_at`. Folge: einfach, aber die Reihenfolge entscheidet, wer den Lock zuerst bekommt; ein wartender Worker blockiert einen Slot.
2. Spalte `serial_key` an `workflow_run`; der Claim nimmt nur den ältesten `pending`-Run eines Schlüssels, wenn kein Run desselben Schlüssels läuft, abgesichert mit `pg_advisory_xact_lock` auf den Schlüssel. Folge: Reihenfolge in der Datenbank nachweisbar, übersteht Neustarts.
3. NATS-Subject-Partitionierung nach CI wie bei den Ingest-Spuren. Folge: Reihenfolge durch Partitionierung, aber feste Spurenzahl, und Wiederholungen eines Runs blockieren die ganze Spur.

**Empfehlung:** Option 2. Die Reihenfolge ist in der Datenbank prüfbar und testbar. Runs ohne CI-Bezug (`serial_key` NULL) laufen parallel. Wartende Genehmigungen blockieren das CI nicht, weil `waiting_*` nicht als laufend gilt.

**Auswirkung:** WP-1089 ergänzt `serial_key`, den Index `(organization_id, serial_key, created_at) WHERE status = 'pending'` und den Claim; der Test belegt Reihenfolge und Nicht-Blockieren durch `waiting_approval`.

**Quelle:** backend/internal/jobs/pg_repository.go:291–302; backend/migrations/000027_workflows_forms.up.sql:59–71; docs/spec/katalog-v3/06-audit-sicherheit-events.md:48

**Betroffene IDs:** WFL-02, EVT-01

**Betroffene WPs:** WP-1089 (`p3-wf-serial`)

<a id="e-228"></a>
## E-228 – Versionierung von Workflow- und Formulardefinitionen

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** WFL-01 verlangt versionierte Formulare (`docs/spec/katalog-v3/09-module-phase2plus.md:88`); ein Run oder eine Einreichung muss an die Fassung gebunden sein, mit der er gestartet bzw. erfasst wurde (WP-231, `docs/plan/implementierungsplan.md:11159`). `form_def` und `workflow_def` sind veränderliche Zeilen ohne Version (`backend/migrations/000027_workflows_forms.up.sql:10–22`, `:44–56`); `form_submission.form_id` und `workflow_run.workflow_id` verweisen auf die veränderliche Zeile (`:29`, `:62`).

**Optionen:**

1. Unveränderliche Versionstabellen `workflow_def_version` und `form_def_version`; Run und Einreichung referenzieren `version_id`, die Definition zeigt auf die aktive Version. Folge: Bindung per Fremdschlüssel prüfbar; Versionen vergleichbar und gezielt aktivierbar.
2. JSON-Snapshot der Definition in jedem Run bzw. jeder Einreichung. Folge: keine neue Tabelle, aber jede Zeile trägt die Definition. Bei alarmgetriebenen Runs (Rechnung in [E-241](#e-241): bis 86.400 Runs/Tag je Definition am Ratenlimit) und etwa 4 KB Definition wären das bis zu 0,35 GB/Tag zusätzlich je Definition.
3. Versionsnummer ohne Historie (Optimistic Locking). Folge: verhindert nur parallele Änderungen; ein laufender Run sieht die geänderte Definition.

**Empfehlung:** Option 1. Eine Version wird einmal gespeichert und nicht dupliziert, ist im UI vergleichbar und gezielt aktivierbar. Option 2 bläht die Run-Tabelle bei hohen Alarmraten auf.

**Auswirkung:** WP-1086 legt `workflow_def_version` an und bindet `workflow_run`, WP-1100 legt `form_def_version` an und bindet `form_submission`. Backfill: je bestehender Definition eine Version 1. Neue Tabellen mit RLS/FORCE RLS, truncate guard und zusammengesetzten Fremdschlüsseln.

**Quelle:** backend/migrations/000027_workflows_forms.up.sql:10–22, :29, :44–56, :62; docs/spec/katalog-v3/09-module-phase2plus.md:88; docs/plan/implementierungsplan.md:11159 (WP-231)

**Betroffene IDs:** WFL-01, WFL-02, API-06

**Betroffene WPs:** WP-1086 (`p3-wf-version`), WP-1100 (`p3-form-version`)

<a id="e-229"></a>
## E-229 – Migration der Alt-Bedingungen in Workflow- und Compliance-Regeln

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1; Option 2 nur auf Anforderung des Auftraggebers)
**Kontext:** CH18 verlangt eine JSON-Regelsprache für Metamodell, Workflows, Compliance und Suche (MET-45, `docs/spec/katalog-v3/00-grundlagen.md:35`). Workflow- und Compliance-Regeln nutzen eigene Bedingungen `{field, op, value}` mit `eq`, `ne`, `exists`, `not_empty`, `contains` und `matches` (`eval`, `backend/internal/workflow/executor.go:357–378`; `backend/internal/compliance/evaluator.go:114–135`). Der MET-45-AST kennt `eq`, `ne`, `in`, `not_in`, Vergleiche, `contains`, `empty` und `not_empty`, aber kein `exists` und kein `matches`; `pattern` ist eine Wertregel, kein Bedingungsoperator (`backend/internal/rules/rules.go:29–53`). Schema und Konformanzfälle liegen in `api/rules.schema.json` und `api/rules.conformance.json`.

**Optionen:**

1. Datenmigration: `eq`, `ne`, `contains` und `not_empty` 1:1, `exists` → `not_empty`; Regeln mit `matches` werden deaktiviert und im Migrationsbericht genannt. Folge: eine Regelsprache; Regeln mit `matches` müssen neu formuliert werden.
2. Den MET-45-AST um den Bedingungsoperator `matches` erweitern (Go, TypeScript, `rules.schema.json`, Konformanzfälle). Folge: verlustfreie Migration, aber ein neuer Operator in beiden Sprachen und im Konformanzset; Regex in Bedingungen braucht eine Laufzeitgrenze.
3. Beide Sprachen parallel weiterführen. Folge: kein Migrationsaufwand, verletzt CH18.

**Empfehlung:** Option 1; Option 2 nur, wenn der Auftraggeber reguläre Ausdrücke in Bedingungen verlangt. Die Module sind in G2 abgeschaltet, Bestandsdaten gibt es höchstens in Testinstanzen.

**Auswirkung:** WP-1087 stellt Workflow-Bedingungen und Trigger-Filter um, WP-1125 die Compliance-Regeln; beide mit Migrationsbericht (Anzahl umgestellter und deaktivierter Regeln). Die Funktionen `eval` in Workflow und Compliance entfallen.

**Quelle:** backend/internal/workflow/executor.go:357–378; backend/internal/compliance/evaluator.go:114–135; backend/internal/rules/rules.go:29–53; api/rules.schema.json; api/rules.conformance.json; docs/spec/katalog-v3/00-grundlagen.md:35

**Betroffene IDs:** WFL-02, SEC-03, CMP-02, MET-45

**Betroffene WPs:** WP-1087 (`p3-wf-conditions`), WP-1125 (`p3-cmp-ast`)

<a id="e-230"></a>
## E-230 – Genehmigungsmodell

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** WFL-02 nennt die Genehmigung als Aktion (`docs/spec/katalog-v3/09-module-phase2plus.md:90`), WFL-04 Bestellgenehmigung, Übergabe und IGA-Genehmigungen (`:94`); IGA verlangt Vier-Augen. Heute wartet ein Run im Schritt `require_approval` (`backend/internal/workflow/executor.go:283–284`); die Entscheidung nimmt `POST /api/v1/workflow-runs/{id}/approval` an (`backend/internal/workflow/handler.go:28`) und prüft nur `approved`/`rejected` (`executor.go:112–150`). Das Recht leitet die Autorisierung aus `workflow:read` ab (`backend/internal/server/authz.go:112`, `:516–518`); entscheiden darf also jeder mit `workflow:write` (`backend/internal/identity/types.go:89–90`), auch der Initiator.

**Optionen:**

1. Ein Genehmiger mit neuem Recht `workflow:approve`. Folge: kleine Änderung; kein Vier-Augen-Prinzip, keine Frist.
2. Genehmigerkreis aus Nutzern, Teams und Rollen mit Quorum (n von m), Ausschluss des Initiators, Frist mit Aktion bei Ablauf (`reject` oder `escalate` an einen Ersatzkreis), eigene Tabelle `workflow_approval` je Entscheidung. Folge: deckt Vier-Augen und Fristen ab; mehrere Stufen entstehen durch mehrere `require_approval`-Schritte.
3. Mehrstufige Genehmigungsketten mit Bedingungen pro Stufe. Folge: zweite Ablaufsprache innerhalb der Workflow-Sprache; Mehraufwand ohne zusätzliche Katalogforderung.

**Empfehlung:** Option 2. Sie deckt Vier-Augen und Fristen ab, ohne eine zweite Ablaufsprache einzuführen. Das Recht `workflow:approve` registriert WP-866 (`p3-permissions`) mit der Rollenmatrix (nur `org_admin`, Vergabe über Custom Roles).

**Auswirkung:** WP-1098 legt `workflow_approval` (RLS/FORCE RLS, truncate guard, zusammengesetzte Fremdschlüssel), Quorum und Initiator-Ausschluss an und setzt für die Approval-Route `workflow:approve` in `writePermissionOverrides`; ein Recht registriert WP-1098 nicht selbst. WP-1099 liefert Frist und Eskalation, WP-1108 die Bestellgenehmigung nach WFL-04.

**Quelle:** backend/internal/workflow/executor.go:112–150, :283–284; backend/internal/workflow/handler.go:28; backend/internal/server/authz.go:112, :516–518; backend/internal/identity/types.go:89–90; docs/spec/katalog-v3/09-module-phase2plus.md:90, :94

**Betroffene IDs:** WFL-02, WFL-04, RBA-05, NTF-05

**Betroffene WPs:** WP-866 (`p3-permissions`), WP-1098 (`p3-wf-approval`), WP-1099 (`p3-wf-approval-timeout`), WP-1108 (`p3-wfl04-order`)

<a id="e-231"></a>
## E-231 – Ziel der Aktion send_webhook

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** Die Aktion `send_webhook` ruft `Dispatcher.Dispatch` direkt und vor dem Commit des Run-Schritts (`backend/internal/workflow/executor.go:272–277`; `backend/internal/webhook/dispatcher.go:372`). Rollt der Schritt zurück, ist der Webhook trotzdem verschickt. Die reguläre Zustellung über Subscriptions nutzt Signatur, Retries, Dead-Letter und den egress-Client (`dispatcher.go:298–300`); SEC-08 verlangt Egress-Schutz.

**Optionen:**

1. Freie URL in der Aktion mit Egress-Prüfung. Folge: flexibel, aber ein zweiter Zustellpfad mit eigenem Secret-, Retry- und Dead-Letter-Handling.
2. Ausschließlich über die Webhook-Subscriptions der Organisation: Die Aktion schreibt das Ereignis `workflow.webhook` in die Outbox; die Zustellung übernimmt der vorhandene Dispatcher. Folge: keine Wirkung vor dem Commit; ein Ziel muss als Subscription angelegt sein.

**Empfehlung:** Option 2. Secrets, Signatur, Retries, Dead-Letter und Egress-Schutz werden wiederverwendet; es gibt keinen zweiten Zustellpfad und keine Wirkung vor dem Commit.

**Auswirkung:** WP-1096 ersetzt den direkten Aufruf durch das Schreiben in die Outbox in der Schritt-Transaktion und ist Produzent von `workflow.webhook` (registriert in WP-868, `plannedProducers` → WP-1096; Produzentenfall in `evt02_producers_integration_test.go`).

**Quelle:** backend/internal/workflow/executor.go:272–277; backend/internal/webhook/dispatcher.go:298–300, :372

**Betroffene IDs:** WFL-02, SEC-08

**Betroffene WPs:** WP-1096 (`p3-wf-act-webhook`)

<a id="e-232"></a>
## E-232 – Wirkung der Lifecycle-Feldsperren (LCY-04)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** LCY-04 verlangt, dass Lifecycle-Zustände Felder über die Regel-Engine sperren (`docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:48`). Zu klären ist die Wirkung auf manuelle Schreibpfade, Import, Bulk, Workflow (Rang 92, `backend/internal/override/decide.go:24`) und Discovery-Beobachtungen. Der Regelkatalog kennt die Art `readonly` (`backend/migrations/000207_metamodel_rule.up.sql:35`; `RuleKindReadonly`, `backend/internal/citype/rule.go:27`); eine Regel greift, solange ihre Bedingung gilt, und weist eine geänderte eingehende Angabe ab (`catalogViolations`, `backend/internal/ci/validation.go:236–248`). Beobachtungen bleiben bei einem Override in `_observed` erhalten (OVR-01, `docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:80`).

**Optionen:**

1. Die Sperre gilt für manuelle, Import-, Bulk- und Workflow-Schreibpfade; Discovery-Beobachtungen landen wie bisher in `_observed` und lösen bei Abweichung ein Review aus. Umsetzung als `readonly`-Katalogregel mit Bedingung auf `lifecycle_state`. Folge: eine Sperrlogik; keine verlorenen Beobachtungen.
2. Die Sperre gilt auch für Discovery (Beobachtung verworfen). Folge: verletzt OVR-01 (stiller Verlust ist Critical).
3. Sperre nur in der UI. Folge: API, Import und Workflow umgehen die Sperre.

**Empfehlung:** Option 1. Sie entspricht der Semantik der vorhandenen `readonly`-Katalogregeln und verliert keine Beobachtungen. Eine zweite Sperrlogik entsteht nicht.

**Auswirkung:** WP-1103 liefert die Feldsperren als Katalogregeln mit Bedingung auf `lifecycle_state` und die Review-Auslösung für abweichende Beobachtungen. WP-1097 stellt sicher, dass `set_ci_field` und `transition_lifecycle` durch `catalogViolations` laufen.

**Quelle:** docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:48; docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:80; backend/migrations/000207_metamodel_rule.up.sql:35; backend/internal/citype/rule.go:27; backend/internal/ci/validation.go:236–248; backend/internal/override/decide.go:24

**Betroffene IDs:** LCY-04, OVR-01, WFL-02

**Betroffene WPs:** WP-1097 (`p3-wf-act-setfield`), WP-1103 (`p3-lcy-locks`)

<a id="e-233"></a>
## E-233 – Datenmodell der attributbezogenen Quellpriorität (REC-04)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** REC-04 verlangt eine Quellpriorität je Attribut, ausschließlich für Automationsquellen; `manual` bleibt 100 (`docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:56`). Die Ränge stehen fest im Code (`SourceRanks`, `backend/internal/override/decide.go:19–35`; REC-03, `:54` im Katalog). Die vorhandene `source_priority_policy` ist eine organisationsweite, frei sortierbare Liste ohne Attributbezug, in der auch `manual` und `manual_override` umsortierbar sind (`backend/migrations/000055_cmdb_extensions.up.sql:513–529`; `SourcePolicy`, `backend/internal/override/model.go:75–87`). Sie wird über `GET`/`PUT /api/v1/reconciliation/source-policy` gepflegt (`backend/internal/override/handler.go:150–151`), beeinflusst aber keine Schreibentscheidung. WP-220 (`docs/plan/implementierungsplan.md:10681`) war für REC-04 vorgesehen und wurde verschoben.

**Optionen:**

1. Neue Tabelle `source_rank_policy(organization_id, ci_type_id NULL, attribute_key, source, rank)` mit CHECK auf Automationsquellen und Rang 1–99; `DecideAutomatedWrite` liest den effektiven Rang (Typ vor global, sonst `SourceRanks`). `PUT /reconciliation/source-policy` entfällt (410), `GET` zeigt die effektive Tabelle. Folge: numerische Ränge wie REC-03; `manual` konstruktiv nicht konfigurierbar.
2. `source_priority_policy` um `attribute_key` erweitern und die Listenreihenfolge in Ränge umrechnen. Folge: weniger Schema, aber die Umrechnung von Reihenfolge in Ränge ist mehrdeutig, und `manual` bleibt umsortierbar.
3. Ränge pro Attribut nur in `attribute_definition`. Folge: keine Organisationsebene für Systemattribute; jede Typänderung berührt Ränge.

**Empfehlung:** Option 1. Numerische Ränge entsprechen REC-03; `manual` bleibt fest bei 100, weil die Quelle nicht zulässig ist. Die Vererbung über `ci_type_id NULL` ist eindeutig. Der Audit-Befund „manual umsortierbar“ entfällt konstruktiv.

**Auswirkung:** WP-1105 legt `source_rank_policy` an (RLS/FORCE RLS, truncate guard, zusammengesetzte Fremdschlüssel), stellt `DecideAutomatedWrite` um und kündigt die PUT-Route ab; die Rangsuche wird je Schreibvorgang einmal je (Typ, Attribut) gecacht. WP-1115 liefert die Oberfläche.

**Quelle:** backend/internal/override/decide.go:19–35; backend/migrations/000055_cmdb_extensions.up.sql:513–529; backend/internal/override/model.go:75–87; backend/internal/override/handler.go:150–151; docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:54, :56; docs/plan/implementierungsplan.md:10681 (WP-220)

**Betroffene IDs:** REC-03, REC-04

**Betroffene WPs:** WP-1105 (`p3-rec-attr-policy`), WP-1115 (`p3-rec-attr-policy-ui`)

<a id="e-234"></a>
## E-234 – CVE-Quelle für den Versionsabgleich (SEC-02)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 3)
**Kontext:** SEC-02 verlangt einen CVE- und Versionsabgleich mit Findings pro CI (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:26`). Die Kunden sind vor allem ISPs und MSPs mit Netzgeräten (Cisco, Juniper, Nokia, Huawei, MikroTik, Zyxel, AVM u. a.). Die Versionsangaben kommen aus `ci.firmware_version`, `manufacturer` und `model` (`backend/migrations/000002_ci_type_system.up.sql:35–36`, `:39`) sowie `os_name`, `os_version` und `sys_object_id` (`000021_spec_alignment.up.sql:17–19`). Findings speichert `security_finding` mit `kind vulnerability|outdated_firmware|…`, `installed_version` und `fixed_version` (`000053_security_findings.up.sql:6–22`).

Mengengerüst (Schätzung): Die NVD führt rund 3·10⁵ CVEs; bei etwa 2 KB je Eintrag mit CPE-Konfiguration sind das rund 0,6 GB global. Beschränkt auf die Hersteller mit Produktzuordnung ([E-236](#e-236), etwa 15 Netzhersteller) bleiben schätzungsweise 2–3·10⁴ CVEs, also unter 0,1 GB. Die Tabellen sind global (ohne `organization_id`), also einmal je Instanz.

**Optionen:**

1. NVD API 2.0 (CVE plus CPE-Match) allein. Folge: breite Abdeckung, aber CPE-Daten der NVD sind für viele Netzhersteller verspätet oder ungenau; Fixversionen fehlen oft.
2. CSAF 2.0 allein (BSI CERT-Bund als Provider plus Hersteller-Provider über Aggregator und `provider-metadata.json`). Folge: herstellergenaue Produktbäume und Fixversionen, aber Lücken bei Herstellern ohne CSAF.
3. CSAF 2.0 primär, NVD 2.0 ergänzend für Hersteller ohne CSAF; CISA KEV als Kennzeichen „aktiv ausgenutzt“. Folge: drei Importer, aber beste Abdeckung und Priorisierung.
4. Kommerzieller Feed. Folge: geringer Eigenaufwand, aber Lizenzkosten und Abhängigkeit; im Air-Gapped-Profil vertraglich zu klären.

**Empfehlung:** Option 3. CSAF liefert herstellergenaue Produktbäume und Fixversionen und kommt für deutsche KRITIS- und NIS2-Kunden vom BSI. NVD schließt die Lücken bei Herstellern ohne CSAF, KEV verbessert die Priorisierung. Alle drei Quellen sind frei nutzbar; ausgehend fließen keine Kundendaten.

**Auswirkung:** WP-1117 legt den globalen Speicher an (ohne `organization_id`, in der Ausnahmeliste `rls.SystemCatalogTables` mit RLS/FORCE RLS, Lesen in jedem Kontext, Schreiben nur über `database.WithSystemWrite`, Truncate-Guard), WP-1118 den CSAF-Import, WP-1119 NVD und KEV (NVD-API-Key aus `RETICORA_VULNFEED_NVD_API_KEY` als Deployment-Secret der Installation bis zur Klärung von Aufgabe A-72, Einhaltung des NVD-Ratenlimits).

**Quelle:** docs/spec/katalog-v3/06-audit-sicherheit-events.md:26; backend/migrations/000002_ci_type_system.up.sql:35–36, :39; backend/migrations/000021_spec_alignment.up.sql:17–19; backend/migrations/000053_security_findings.up.sql:6–22

**Betroffene IDs:** SEC-02

**Betroffene WPs:** WP-1117 (`p3-cve-store`), WP-1118 (`p3-cve-csaf`), WP-1119 (`p3-cve-nvd`)

<a id="e-235"></a>
## E-235 – CVE-Daten im Air-Gapped-Profil (TEC-16, INS-08)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2 mit Option 3)
**Kontext:** Im Air-Gapped-Profil gibt es keinen Internetzugang; Updates kommen als Bundle mit Prüfsummen und Signaturen über `install.sh --offline-update`, OUI- und Profil-Updates per Bundle (TEC-16, `docs/spec/katalog-v3/01-installation-stack.md:18`; INS-08, `:43`). Das Profil ist optional und darf keine Gates gefährden (CH8, `docs/spec/katalog-v3/00-grundlagen.md:15`). Die Quellen aus [E-234](#e-234) sind Internet-Dienste.

**Optionen:**

1. Kein CVE-Abgleich im Air-Gapped-Profil. Folge: kein Aufwand; SEC-02 ist in diesem Profil nicht erfüllt.
2. Signiertes Offline-Bundle im Format der Online-Synchronisation (Prüfsumme und Signatur wie das Update-Bundle), Import über eine Operator-Route oder `install.sh --offline-update`. Folge: gleicher Importpfad wie online; der Betreiber liefert Bundles.
3. Der Kunde betreibt einen eigenen CSAF-Spiegel im Netz; die Provider-URLs sind konfigurierbar. Folge: kein Bundle-Prozess, setzt aber einen Spiegel beim Kunden voraus.

**Empfehlung:** Option 2, zusätzlich Option 3 über konfigurierbare Provider-URLs. Das Muster der OUI- und Profil-Updates per Bundle (INS-08) wird wiederverwendet; Kunden mit eigenem CSAF-Spiegel brauchen keinen Bundle-Prozess.

**Auswirkung:** WP-1120 liefert Bundle-Format, Signaturprüfung und Import. Signiert wird das Bundle mit einer Ed25519-Signatur über `manifest.json` (`manifest.sig`), geprüft gegen den im Binary eingebetteten öffentlichen Schlüssel; als Release-Artefakt trägt es zusätzlich die cosign-Signatur aller Artefakte (`.github/workflows/release.yml:81–86`); die Provider-URLs aus WP-1118 sind konfigurierbar. Das Bundle trägt den Stand (`generated_at`); der Abgleich weist ein Alter über 30 Tage als Warnung aus.

**Quelle:** docs/spec/katalog-v3/01-installation-stack.md:18, :43; docs/spec/katalog-v3/00-grundlagen.md:15

**Betroffene IDs:** SEC-02, TEC-16, INS-08

**Betroffene WPs:** WP-1120 (`p3-cve-offline`)

<a id="e-236"></a>
## E-236 – Zuordnung CI → Produkt und Versionsvergleich

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** Hersteller- und Modellnamen aus SNMP und Discovery sind uneinheitlich (`ci.manufacturer`, `ci.model`, `backend/migrations/000002_ci_type_system.up.sql:35–36`), Versionsschemata herstellerspezifisch (IOS-XE `17.9.4a`, Junos `22.4R3-S2`). `ci.sys_object_id` (`000021_spec_alignment.up.sql:19`) identifiziert Hersteller und Plattform eindeutiger als Freitext. Ein Fehlalarm erzeugt ein Finding, das LCY-06 in die Health übernimmt (`docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:52`).

**Optionen:**

1. Nur exakter Abgleich auf CPE-Strings. Folge: kaum Treffer, weil Discovery keine CPE liefert.
2. Abbildungstabelle Hersteller/Modell bzw. `sys_object_id`-Präfix → CPE-vendor/product (Systemeinträge plus Überschreibung pro Organisation) und herstellerspezifische Versionsvergleicher mit generischem Fallback; nicht abbildbare CIs erscheinen in einer Liste „ohne Produktzuordnung“. Folge: deterministisch und prüfbar; Pflegeaufwand für die Abbildung.
3. Unscharfer Abgleich (Fuzzy) ohne Pflege. Folge: kein Pflegeaufwand, aber nicht reproduzierbare Fehlalarme und stille Lücken.

**Empfehlung:** Option 2. Sie ist deterministisch, prüfbar und erzeugt keine stillen Fehlalarme. Die Liste ohne Zuordnung macht Lücken sichtbar, statt falsche Sicherheit zu erzeugen.

**Auswirkung:** WP-1121 liefert Abbildung, Vergleicher (mindestens Cisco IOS/IOS-XE, Junos, generisch semver-ähnlich) und den Abgleich als Job; Findings mit `kind = 'vulnerability'` und `fixed_version`. Bei 10.000 CIs und etwa 20 Kandidaten-CVEs je zugeordnetem Produkt sind das rund 2·10⁵ Vergleiche je Lauf, also ein Job je Organisation, kein Request. WP-1122 liefert API und Oberfläche der Abbildung.

**Quelle:** backend/migrations/000002_ci_type_system.up.sql:35–36; backend/migrations/000021_spec_alignment.up.sql:19; docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:52

**Betroffene IDs:** SEC-02, LCY-06

**Betroffene WPs:** WP-1121 (`p3-cve-match`), WP-1122 (`p3-cve-mapping-ui`)

<a id="e-237"></a>
## E-237 – Auslösung der Compliance-Bewertung

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** SEC-03 verlangt ein Ergebnis pro CI und einen aggregierten Score (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:28`). Heute läuft die Bewertung synchron im Request `POST /api/v1/compliance/evaluations` (`backend/internal/compliance/handler.go:35`) und liest höchstens 100 Regeln und 100 CIs (`backend/internal/compliance/evaluator.go:40`, `:44`); größere Organisationen werden nur teilweise bewertet.

Rechnung (Planannahme): 50 aktive Regeln × 25.000 CIs = 1,25·10⁶ Ergebniszeilen je Volllauf. Bei 5.000 Upserts/s dauert der Volllauf rund 4 Minuten. Inkrementell kostet eine CI-Änderung 50 Upserts; bei 10.000 CI-Änderungen pro Stunde (Discovery-Spitze) sind das 5·10⁵ Upserts/h ≈ 140/s. Mit einer Bündelung je CI über 60 s (mehrere Änderungen desselben CI ergeben eine Bewertung) sinkt das deutlich.

**Optionen:**

1. Nur manueller Volllauf als Job. Folge: Score veraltet zwischen den Läufen.
2. Volllauf als Job (manuell und nächtlich) plus inkrementelle Neubewertung pro CI bei `ci.changed`, `lifecycle.changed` und `finding.*`, gebündelt je CI. Folge: Score zeitnah; der nächtliche Lauf fängt Regeländerungen und verpasste Ereignisse ab.
3. Nur inkrementell. Folge: Regeländerungen wirken erst bei der nächsten CI-Änderung.

**Empfehlung:** Option 2. Der Score bleibt zeitnah, ohne ständig voll zu rechnen; der nächtliche Lauf fängt Regeländerungen und verpasste Ereignisse ab.

**Auswirkung:** WP-1126 stellt die Bewertung auf einen Job (JOB-04) um, entfernt die Grenze von 100 durch seitenweises Lesen und ergänzt den inkrementellen Konsumenten mit Bündelung. WP-1127 schreibt den aggregierten Score je Lauf und Gruppe als Historie (eine Zeile je Organisation, Gruppe und Tag).

**Quelle:** backend/internal/compliance/handler.go:35; backend/internal/compliance/evaluator.go:31–44; docs/spec/katalog-v3/06-audit-sicherheit-events.md:28; docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:90 (JOB-04)

**Betroffene IDs:** SEC-03, CMP-02, JOB-04

**Betroffene WPs:** WP-1126 (`p3-cmp-eval-job`), WP-1127 (`p3-cmp-score`)

<a id="e-238"></a>
## E-238 – Kontrollkatalog für ISO 27001, NIS2, KRITIS

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** SEC-04 verlangt Reports für ISO 27001, NIS2 und KRITIS (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:30`). Der heutige Report kennt den Standard nur als Parameter (Default `iso27001`, `backend/internal/compliance/report.go:83–87`) ohne Kontrollen. Die Normtexte von ISO/IEC 27001 sind urheberrechtlich geschützt; NIS2 (Richtlinie (EU) 2022/2555, Art. 21) und § 8a BSIG sind frei zugänglich.

**Optionen:**

1. Globaler Systemkatalog nur mit Kontroll-IDs und eigenen Kurztiteln (ohne Normtext); Zuordnung Regel → Kontrolle pro Organisation. Folge: lizenzfrei; Auditoren können über die IDs referenzieren.
2. Vollständige Normtexte im Produkt. Folge: Lizenz der ISO-Texte nötig.
3. Kein Katalog, nur ein Kategorie-Label an der Regel. Folge: Report kann keine Abdeckung je Kontrolle ausweisen.

**Empfehlung:** Option 1. Lizenzfrei und für Auditoren ausreichend. Ob eigene Kontrollkataloge der Organisation (z. B. B3S, TISAX) möglich sein sollen, klärt der Auftraggeber; das Modell lässt sie über eine Spalte `organization_id NULL` zu, ohne dass P3 sie liefert.

**Auswirkung:** WP-1128 legt Katalog und Zuordnung an, WP-1129 weist im Report die Abdeckung je Kontrolle aus, WP-1130 ordnet die mitgelieferten Regelvorlagen Kontrollen zu.

**Quelle:** docs/spec/katalog-v3/06-audit-sicherheit-events.md:30; backend/internal/compliance/report.go:83–87

**Betroffene IDs:** SEC-04, SEC-03

**Betroffene WPs:** WP-1128 (`p3-cmp-controls`), WP-1129 (`p3-cmp-report`), WP-1130 (`p3-cmp-templates`)

<a id="e-239"></a>
## E-239 – Erzeugung und Ablage der Reports (SEC-04)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** SEC-04 verlangt PDF und CSV pro Standard (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:30`). Der heutige Report entsteht synchron in `GET /api/v1/compliance/report` (`backend/internal/compliance/handler.go:38`) aus höchstens 1.000 Ergebnissen (`backend/internal/compliance/report.go:89–95`). `github.com/go-pdf/fpdf` ist im Build (`backend/go.mod:14`, genutzt in `backend/internal/labels/render.go`). Dokumente liegen in S3 mit Presigned URLs (MGT-01, `docs/spec/katalog-v3/09-module-phase2plus.md:40`; `PresignedGetURL`, `backend/internal/platform/blob/blob.go:66`). Große Operationen laufen als Job (JOB-04, `docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:90`).

**Optionen:**

1. Synchron im Request. Folge: bei 1,25·10⁶ Ergebniszeilen ([E-237](#e-237)) nicht innerhalb der Request-Timeouts.
2. Job erzeugt PDF (fpdf) und CSV; Ablage als Dokument mit `document_link` auf die Organisation, Download per Presigned URL, Aufbewahrung nach den Dokumentregeln. Folge: revisionssicher und versioniert; keine neue Abhängigkeit.
3. HTML-zu-PDF über einen Headless-Browser. Folge: frei gestaltbare Layouts, aber eine Browser-Abhängigkeit im Server-Image und im Air-Gapped-Bundle.

**Empfehlung:** Option 2. Große Organisationen sprengen die Request-Timeouts; die Ablage als Dokument ist revisionssicher und versioniert; fpdf ist im Build und erfordert keinen Browser.

**Auswirkung:** WP-1129 liefert den Job, die Ablage und den Download; der Report zählt gegen `max_storage_mb` der Organisation.

**Quelle:** backend/internal/compliance/handler.go:38; backend/internal/compliance/report.go:89–95; backend/go.mod:14; backend/internal/labels/render.go; backend/internal/platform/blob/blob.go:66; docs/spec/katalog-v3/09-module-phase2plus.md:40; docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:90

**Betroffene IDs:** SEC-04, JOB-04, MGT-01

**Betroffene WPs:** WP-1129 (`p3-cmp-report`)

<a id="e-240"></a>
## E-240 – Umfang von WFL-03 („Beispiele wie v2“)

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** WFL-03 lautet „Automation-Regeln für CMDB-Objekte (Beispiele wie v2)“ (`docs/spec/katalog-v3/09-module-phase2plus.md:92`). Die v2-Texte liegen nicht vor und werden nach dem Beschluss vom 2026-10-28 nicht nachgereicht; verbindlich gilt: WPs setzen nur ausdrücklich genannte Bestandteile um und erfinden keine Funktionen ([E-02](../entscheidungen.md#e-02)). Der generische Mechanismus (Ereignis-Trigger auf CMDB-Objekte) entsteht ohnehin mit WP-1090.

**Optionen:**

1. WFL-03 nur als generischer Mechanismus ohne mitgelieferte Vorlagen. Folge: E-02 eingehalten; Kunden starten ohne Beispiele.
2. Vorlagenliste aus der Planung vorschlagen und mit dem Beschluss zu dieser Entscheidung vom Auftraggeber bestätigen lassen: CI ohne Owner → Ticket; Garantie oder Vertrag läuft ab → Benachrichtigung; CI `decommissioned` mit aktiven Beziehungen → Ticket; neues CI aus Discovery ohne Kunde → Review; kritisches Finding → Ticket. Folge: geringer Aufwand; erst nach Bestätigung zulässig.
3. Abwarten. Folge: WFL-03 bleibt offen, ohne dass Texte kommen.

**Empfehlung:** Option 2. Der Mechanismus entsteht in M3.16 ohnehin; Vorlagen sind billig, aber nach E-02 erst nach Freigabe zulässig. Bestätigt der Auftraggeber nur einen Teil, liefert WP-1106 nur diesen.

**Auswirkung:** WP-1106 liefert die bestätigten Vorlagen als inaktive Definitionen zum Übernehmen. WP-1130 liefert analog die Compliance-Regelvorlagen. Ohne Bestätigung gilt Option 1, und WP-1106 entfällt.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:92; docs/plan/entscheidungen.md#e-02

**Betroffene IDs:** WFL-03

**Betroffene WPs:** WP-1106 (`p3-wfl-templates`), WP-1130 (`p3-cmp-templates`)

<a id="e-241"></a>
## E-241 – Aufbewahrung von Workflow-Runs

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 2)
**Kontext:** Für `workflow_run` und `workflow_step` gibt es keine Aufbewahrungsregel (`backend/migrations/000027_workflows_forms.up.sql:59–80`); die Retention-Jobs kennen nur Audit, Historie, gelöschte Objekte und Review-Items (`RetentionPolicy`, `backend/internal/jobs/retention.go:30–36`). Metriken hält TimescaleDB 400 Tage (`retention.go:24–28`); der Auftraggeber verlangt 400 Tage Verfügbarkeitshistorie, und ALR-01 hält Alarme 400 Tage (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:101`, neu). WFL-05 macht Alarmzustandswechsel zu Triggern mit einem Ratenlimit von 60 Runs/min je Definition.

Rechnung (Planannahmen: 1 KB je Run mit Kontext, 3 Schritte zu je 0,6 KB, also 2,8 KB je Run):
- Obergrenze je Definition am Ratenlimit: 60 × 1.440 = 86.400 Runs/Tag ≈ 0,24 GB/Tag; über 400 Tage 3,5·10⁷ Runs ≈ 97 GB je Definition.
- Realistische Last: 30,5·10⁶ Prüfungen/Tag; bei 0,1 % Zustandswechseln sind das 30.500 Wechsel/Tag. Startet nur der Ursachenalarm (etwa 20 %), entstehen 6.100 Runs/Tag ≈ 17 MB/Tag; über 400 Tage 2,4·10⁶ Runs ≈ 6,8 GB.
- Schreibrate in der Spitze (10.000 Wechsel/min, WFL-05): bis 167 Run-Anlagen/s plus je Run 1 Status- und 3 Schritt-Schreibvorgänge.

**Optionen:**

1. Unbegrenzt. Folge: Wachstum ohne Grenze; bei der Obergrenze rund 88 GB je Definition und Jahr.
2. Runs und Schritte 400 Tage nach `finished_at` (gleich der Verfügbarkeitshistorie), danach Löschung durch den Retention-Job in Batches; Runs mit Genehmigung oder Unterschrift bleiben über die Audit-Einträge nachweisbar (Audit-Aufbewahrung). Folge: begrenzter Bestand (realistisch unter 7 GB), konsistent zur Alarmhistorie.
3. 90 Tage. Folge: rund 1,5 GB realistisch, aber Runs zu Alarmen sind kürzer verfügbar als die Alarme selbst.

**Empfehlung:** Option 2. Die Frist entspricht der Vorgabe des Auftraggebers zur Langzeithistorie; nachweispflichtige Entscheidungen bleiben über das Audit erhalten. Die Obergrenze je Definition begrenzen zusätzlich die Paketlimits nach WFL-08.

**Auswirkung:** WP-1093 ergänzt `RetentionPolicy` um Workflow-Runs (Index `(organization_id, finished_at)`), löscht in Batches je Organisation und auditiert den Lauf. WP-1092 schreibt die Audit-Einträge der Statuswechsel und Entscheidungen so, dass sie die Löschung der Runs überdauern.

**Quelle:** backend/migrations/000027_workflows_forms.up.sql:59–80; backend/internal/jobs/retention.go:24–36; docs/spec/katalog-v3/10-phase3-ergaenzungen.md:101, :215, :221, :225 (ALR-01, WFL-05, WFL-08, NFR-11, neu)

**Betroffene IDs:** WFL-01, WFL-02, WFL-05, NFR-06

**Betroffene WPs:** WP-1092 (`p3-wf-run-api`), WP-1093 (`p3-wfl-scope`)

<a id="e-242"></a>
## E-242 – EU-Residenz, Unterauftragsverarbeiter und Self-Hosting für SEC-05 [P3]

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Der P3-Anteil von SEC-05 verlangt neben Lösch- und Exportworkflows „EU-Residenz, AVV-fähig; historische Daten legen keine gelöschten PII offen (CH16)“ (`docs/spec/katalog-v3/06-audit-sicherheit-events.md:32`); NFR-09 [B] verlangt SaaS-Daten inklusive Backups, Logs und Suchindex in der EU (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:50`). Auf main bestehen:
- Datenorte und ihre Durchsetzung für SaaS (`docs/betrieb/referenzmodell.md:80–98`): Terraform-Variablen `region`, `backup_region`, `log_region` und `search_region` lassen nur EU-Regionen zu; neue Datenorte brauchen eine eigene validierte Region-Variable und einen Eintrag in der Subprozessorenliste (`:96–98`).
- Betriebsmodelle SaaS, Dedicated Instance und Air-Gapped On-Prem (`referenzmodell.md:8–13`).
- Die Subprozessorenliste ist leer; nach [E-35](../entscheidungen.md#e-35) und [E-103](../phase2/entscheidungen.md#e-103) gilt die Erklärung des Auftraggebers als Nachweis für G1 und G2 (`docs/legal/subprozessoren.md:3–10`).
- Das Paket `privacy` kennt nur Aufbewahrung und Löschung (`backend/internal/privacy/handler.go:23–26`); README nennt „EU-Hosting“ (`README.md:5`).

Phase 3 bringt neue Datenflüsse: ADMP-Cache mit Anschlussinhabern ([E-243](#e-243)), Tresorverbindungen (im Kundennetz, [E-220](#e-220)), CVE-Feeds (nur eingehend, [E-234](#e-234)) sowie SMS- und Pager-Provider (NTF-02 nennt Twilio, Sipgate, ilert, PagerDuty, Opsgenie; `docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:74`). Mehrere dieser Provider sitzen außerhalb der EU und verarbeiten Telefonnummern.

**Optionen:**

1. Nachweis im Repository: Die Tabelle der Datenorte in `docs/betrieb/referenzmodell.md` wird um die P3-Datenorte ergänzt (ADMP-Cache und Vault in PostgreSQL der Primärregion; Tresor beim Kunden; CVE-Feeds ohne ausgehende Kundendaten). SMS-, Pager- und ADMP-Zugänge sind kundeneigene Konten (Zugangsdaten im Secret-Store der Organisation); der Betreiber setzt keinen eigenen Dienstleister ein, sie werden also keine Unterauftragsverarbeiter des Betreibers. Kanäle zu Providern mit Sitz außerhalb der EU zeigen in der Konfiguration einen Drittlandhinweis. Eine AVV-Anlage mit TOM-Liste verweist auf die technischen Nachweise (Verschlüsselung, Löschung, Export). Für Dedicated und Air-Gapped liegt die Residenz in der Verantwortung des Betreibers der Instanz. Ein Integrationstest belegt CH16 (nach Crypto-Shredding nur Surrogat bzw. „gelöscht“ in Audit, `ci_change`, Ereignissen, ADMP-Cache und Benachrichtigungsverlauf). Folge: keine neue Laufzeitlogik außer dem Hinweis; die Subprozessorenliste bleibt nach E-35 leer, solange der Betreiber keinen Dienstleister einsetzt.
2. Wie Option 1, zusätzlich Laufzeitprüfung: Der Server verweigert ausgehende Verbindungen zu Zielen außerhalb der EU (Geo-IP auf dem egress-Client). Folge: technisch erzwungen, aber Geo-IP ist unzuverlässig (Anycast, CDNs) und bricht legitime Kundenkonten bei globalen Providern.
3. Nur Erklärung des Auftraggebers wie in G1 und G2 (E-103). Folge: kein Aufwand, aber kein Nachweis für den P3-Anteil und kein Test für CH16.

**Empfehlung:** Option 1. Die Durchsetzung für SaaS besteht bereits über Terraform und CI; P3 ergänzt die neuen Datenorte und belegt CH16 mit einem Test. Kundeneigene Provider-Konten halten den Betreiber aus der Unterauftragsverarbeitung heraus, ohne die Provider-Auswahl von NTF-02 einzuschränken. Option 2 erzeugt Fehlsperren ohne verlässlichen Schutz.

**Auswirkung:** WP-1159 ergänzt die Datenorte in `docs/betrieb/referenzmodell.md` statt einer zweiten Residenzdatei, legt die AVV-Anlage mit TOM-Liste an und liefert den CH16-Integrationstest. Eine zweite Residenzdatei (`docs/ops/datenresidenz.md`) entsteht nicht. Den Drittlandhinweis liefern WP-1142 (Feld `third_country` im Provider-Deskriptor aller sechs NTF-02-Provider, API und OpenAPI) und WP-1148 (Anzeige in der Kanalkonfiguration). Setzt der Betreiber eigene Dienstleister ein, sind sie in `docs/legal/subprozessoren.md` einzutragen; das ist eine Lieferung des Betreibers, keine Planarbeit.

**Quelle:** docs/spec/katalog-v3/06-audit-sicherheit-events.md:32; docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:50; docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:74; docs/betrieb/referenzmodell.md:8–13, :80–98; docs/legal/subprozessoren.md:3–10; backend/internal/privacy/handler.go:23–26; README.md:5; docs/plan/entscheidungen.md#e-35; docs/plan/phase2/entscheidungen.md#e-103

**Betroffene IDs:** SEC-05, NFR-09, NTF-02, CH16

**Betroffene WPs:** WP-1142 (`p3-ntf-pager`), WP-1148 (`p3-ntf-ui`), WP-1159 (`p3-sec05-residency`)

<a id="e-243"></a>
## E-243 – Anschlussinhaber aus ADMP als personenbezogene Daten: Vault-Speicherung oder nur flüchtige Anzeige

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** ADM-06 verlangt: Anschlussinhaber abfragen; gespeichert wird nur ein `pii_subject`-Surrogat; Anzeige nur mit `admp:holder_read`; jeder Abruf wird auditiert; Lösch- und Auskunftsworkflows schließen den Cache ein (`docs/spec/katalog-v3/10-phase3-ergaenzungen.md:199`, neu). ADM-07 verlangt die Anschlussadresse, die bei Privatanschlüssen ebenfalls personenbezogen ist. Bausteine auf main:
- `pii_subject` mit verschlüsseltem Payload unter dem Org-DEK; Löschen der Zeile macht den Inhalt unauflösbar (Crypto-Shredding, `backend/migrations/000124_pii_subject.up.sql:1–10`).
- Arten `user`, `contact`, `person` und `value` (`000125_pii_surrogates.up.sql:19–20`), Kind-Subjects über `parent_id … ON DELETE CASCADE` (`:17`).
- `pii.Vault.CreateTx`, `ResolveTx`, `UpdateTx` (`backend/internal/pii/vault.go:130`, `:157`, `:207`), `pii.DeleteTx` (`:232`); für Art `person` legt `createTx` Suchtoken an (`vault.go:149–150`), sodass eine Auskunft nach Name das Subject findet.
- Einzelwerte versiegelt `pii.SealValueTx` (`backend/internal/pii/surrogate.go:137`) mit Anker über `pii.WithAnchor` (`:51`).

Mengengerüst (aus [E-222](#e-222)/[E-225](#e-225)): Mit gezielter Auffrischung nur verknüpfter Ports liegen höchstens so viele Inhaber vor wie Anschlüsse mit verknüpftem Dienst, bei 50.000 Anschlüssen also höchstens 50.000 Subjects zu etwa 0,5 KB (≈ 25 MB) zuzüglich Suchtoken. Mit TTL-Ablauf (`admp.expire`) verschwinden sie samt Adresse.

**Optionen:**

1. Vault-Speicherung: Der Inhaber wird als `pii_subject` der Art `person` in der Upsert-Transaktion gespeichert; `admp_connection.holder_subject_id` verweist darauf. Die Adresse eines Privatanschlusses wird als Kind-Subject (`value`, Anker = Inhaber) gespeichert, die Geschäftsadresse als Klartext (`address_text`). Anzeige über `ResolveTx` mit `admp:holder_read` und Audit je Abruf; Ablauf über TTL und Löschung über `pii.DeleteTx` (Kaskade auf die Adresse). Folge: im ADMP-Ausfall verfügbar; Auskunft und Löschung über die Surrogat-Verweise; keine Migration der Arten-CHECK.
2. Nur flüchtige Anzeige: Der Inhaber wird bei jedem Aufruf der Holder-Route live aus ADMP gelesen und nicht gespeichert; der Cache führt nur `holder_present`. Folge: kein PII-Bestand und keine Auskunfts- oder Löschpflicht für den Cache, aber jede Anzeige kostet einen ADMP-Abruf, im ADMP-Ausfall fehlt der Inhaber genau dann, wenn Handlungsempfehlungen ihn brauchen; die Formulierung „gespeichert wird nur ein pii_subject-Surrogat“ in ADM-06 liefe ins Leere.
3. Eigene PII-Art `admp_holder` statt `person`. Folge: Trennung von Kontaktpersonen, aber eine Migration der CHECK `pii_subject_kind_check`, und die Suchtoken-Logik für Auskünfte müsste die neue Art einschließen.

**Empfehlung:** Option 1. Sie entspricht dem Wortlaut von ADM-06 und Konvention 16, nutzt die vorhandenen Vault-Bausteine ohne Schemaänderung an `pii_subject` und hält Inhaber und Privatadresse im Ausfall verfügbar. Der PII-Bestand bleibt durch die gezielte Auffrischung ([E-225](#e-225)) und den TTL-Ablauf begrenzt. Eine Geschäftsadresse ist kein Personenbezug und bleibt Klartext; die Unterscheidung Geschäfts-/Privatanschluss liefert erst die ADMP-Doku (A-55). Bis dahin gilt jede Adresse als privat.

**Auswirkung:** WP-928 legt `holder_subject_id`, `address_subject_id` und `address_text` mit `CHECK (address_subject_id IS NULL OR address_text IS NULL)` und die Kaskade per AFTER-DELETE-Trigger an. WP-930 liefert nur `holder_subject_id` und `holder_present`. WP-1075 versiegelt die Privatadresse als Kind-Subject. WP-1076 speichert den Inhaber mit Art `person`, liefert die Holder-Route mit Audit und belegt das Crypto-Shredding. Die Auskunfts- und Löschworkflows (WP-1158) finden ADMP-Inhaber über die beiden Verweise.

**Quelle:** backend/migrations/000124_pii_subject.up.sql:1–10; backend/migrations/000125_pii_surrogates.up.sql:17, :19–20; backend/internal/pii/vault.go:130, :149–150, :157, :207, :232; backend/internal/pii/surrogate.go:51, :137; docs/plan/phase2/implementierungsplan.md:220 (Konvention 16); docs/spec/katalog-v3/06-audit-sicherheit-events.md:32

**Betroffene IDs:** ADM-06, ADM-07, SEC-05

**Betroffene WPs:** WP-928 (`p3-admp-cache-model`), WP-930 (`p3-admp-api`), WP-1075 (`p3-admp-ep-connection`), WP-1076 (`p3-admp-ep-holder`)

<a id="e-244"></a>
## E-244 – WP-Nummern ab WP-1000: Traceability, ParsePlan und CI akzeptieren vier Ziffern

**Status:** entschieden (2026-11-09, nach Empfehlung: Option 1)
**Kontext:** Phase 3 vergibt WP-860 bis WP-1177. Konvention 26 des Phase-2-Plans schließt Suffixe aus, weil `ParsePlan`, die Zeilenprüfung und die CI genau drei Ziffern erwarten (`docs/plan/phase2/implementierungsplan.md:230`). Im Code:
- `wpPattern = ^WP-\d{3}$` prüft die Spalte `wp` von `docs/traceability.csv` (`backend/internal/traceability/traceability.go:58`).
- `wpHeading = ^### (WP-\d{3}) ` erkennt WP-Blöcke in `ParsePlan` (`:471`, `:481`).
- `wpReferences` erkennt Nummern und Bereiche „WP-332 – WP-335“ mit `\d{3}` ohne Wortgrenze (`:61`) und wertet sie in `:1006–1012` aus. „WP-1000“ würde dort als WP-100 gelesen, also als Verweis auf ein fremdes Phase-1-WP.
- `Phase2WorkPackages` sortiert WP-Nummern als Zeichenketten (`sort.Strings`, `:758–768`); eine entsprechende Liste für Phase 3 stellte „WP-1000“ vor „WP-860“.

**Optionen:**

1. Parser und CI auf drei bis vier Ziffern erweitern (`\d{3,4}`, in `wpReferences` mit Wortgrenze), numerisch sortieren; erstes WP von M3.0. Folge: eindeutige, fortlaufende Nummern; kleine Codeänderung mit Tests für die Grenzfälle WP-999/WP-1000 und Bereiche über die Grenze.
2. Phase 3 vergibt Lücken unter 1000 neu (entfallene Nummern). Folge: keine Codeänderung, aber nach Konvention 26 dürfen entfallene Nummern nicht wiederverwendet werden; es gibt nicht genug Lücken für 318 WPs.
3. Präfix `P3-nnn`. Folge: zweites Nummernschema in Traceability, PR-Vorlage und Plänen; alle Prüfungen müssen beide Schemata kennen.

**Empfehlung:** Option 1. Eindeutig und fortlaufend; die Codeänderung ist klein und wird als erstes WP gemergt, bevor eine vierstellige Nummer in Plan oder Traceability erscheint.

**Auswirkung:** WP-860 erweitert `wpPattern`, `wpHeading` und `wpReferences`, sortiert WP-Listen numerisch und ergänzt Tests (Bereich „WP-998 bis WP-1002“, keine Teiltreffer „WP-100“ in „WP-1000“). Konvention 26 des Phase-3-Plans nennt „drei oder vier Ziffern“.

**Quelle:** backend/internal/traceability/traceability.go:58, :61, :471, :481, :758–768, :1006–1012; docs/plan/phase2/implementierungsplan.md:230 (Konvention 26)

**Betroffene IDs:** GLO-11, DOD-01

**Betroffene WPs:** WP-860 (`p3-trace`)

## E-245 – React Router 6 ohne Fix für GHSA-wrjc-x8rr-h8h6 (SR-62)

**Status:** entschieden (2026-11-10, nach Empfehlung: Option 1; Beschluss vom 2026-11-09, Punkt 6)
**Kontext:** WP-874 AK4 verlangt die erste Version von `react-router-dom`, für die `npm audit --omit=dev` weder GHSA-jjmj-jmhj-qwj2 noch GHSA-wrjc-x8rr-h8h6 meldet, und schließt die Migration auf React Router 7 aus. Stand 2026-11-10: GHSA-jjmj-jmhj-qwj2 (Open Redirect, `react-router-dom` 6.30.2 bis 6.30.5) ist in 6.30.6 behoben. GHSA-wrjc-x8rr-h8h6 (Open Redirect über Backslash in `<Link>` und `useNavigate`, `react-router` ab 6.0.0 unter 7.18.0) hat in Version 6 keinen Fix; ebenso GHSA-337j-9hxr-rhxg (nur SSR-Hydration, im SPA-Bundle nicht genutzt). Beide sind Moderate und blockieren den Job `npm-audit` nicht (Schwelle High/Critical). Der einzige Navigationspfad mit Eingaben von außen ist das `returnTo` des Logins; WP-874 lässt dort nur lokale Pfade ohne `//` und Backslash zu (`safeReturnTo`, `frontend/src/auth/oidc.ts`).
**Optionen:**

1. `react-router-dom` 6.30.6 (schließt GHSA-jjmj-jmhj-qwj2); GHSA-wrjc-x8rr-h8h6 und GHSA-337j-9hxr-rhxg als Restrisiko in `docs/security/review-phase2.md` (SR-62) mit der Mitigation `safeReturnTo`; die Migration auf React Router 7 wird beim nächsten Planstand als eigenes WP vor dem G3-Gate eingeplant. Folge: AK4 gilt für GHSA-jjmj-jmhj-qwj2; WP-874 bleibt bei 0,5 PT.
2. Migration auf React Router 7.18 in WP-874. Folge: gegen „Nicht Bestandteil“ des WP; Umbau aller Routen-Importe und Tests, deutlich mehr als 0,5 PT, Risiko für das ganze Frontend in einem Sicherheits-WP.
3. Override von `react-router` auf einen eigenen Patch. Folge: Fork einer Kernbibliothek ohne Herstellerpflege.

**Empfehlung:** Option 1. Der ausnutzbare Weg (Navigation auf ein Ziel aus Eingaben von außen) ist durch `safeReturnTo` geschlossen; die Migration auf React Router 7 gehört in ein eigenes, testbares WP.

**Auswirkung:** WP-874 hebt `react-router-dom` auf 6.30.6, prüft `returnTo` nach AK3 und führt in `review-phase2.md` das Restrisiko. Die Migration auf React Router 7 wird beim nächsten Planstand eingeplant.

**Quelle:** frontend/package.json; npm audit vom 2026-11-10 (GHSA-wrjc-x8rr-h8h6: `>=6.0.0 <7.18.0`; GHSA-jjmj-jmhj-qwj2: `>=6.30.2 <=6.30.5`)

**Betroffene IDs:** – (Befund SR-62)

**Betroffene WPs:** WP-874 (`p3-sr61-62`)

**Nachtrag 2026-11-13 (Update-Plan Upd-05, nach Empfehlung):** Das Restrisiko ist beendet. Der Hersteller unterstützt React Router 6.x nicht mehr (SECURITY.md von remix-run/react-router: Sicherheitsupdates nur für 8.x und 7.x). Das Frontend nutzt jetzt `react-router` 7.18.5 (npm dist-tag `version-7`, veröffentlicht 2026-10-27, engines `node >=20.0.0`) statt `react-router-dom` 6.30.6; in Version 7 ist `react-router-dom` nur noch ein Re-Export, deshalb importieren alle 74 Dateien aus `react-router`. 7.18.5 liegt außerhalb aller veröffentlichten Bereiche, auch GHSA-wrjc-x8rr-h8h6 (CVE-2026-53669) und GHSA-337j-9hxr-rhxg (CVE-2026-53666), beide behoben ab 7.18.0; `npm audit` meldet keine Funde mehr. Verhalten wie in Version 6: `BrowserRouter` und die `MemoryRouter` der Tests setzen `useTransitions={false}`. Ohne diese Einstellung verpackt Version 7 jede Router-Aktualisierung in `React.startTransition`. Dann landete im Formular-Builder eine Eingabe nach der Rollenwahl im alten Formularstand und ging verloren (Playwright `metamodel-scenario.spec.ts` ABN-01 (c), reproduzierbar rot ohne die Einstellung). `relativeSplatPath` ist in Version 7 fest eingeschaltet. Den einzigen relativen Pfad (`<Navigate to="orgs">` im Index von `/operator/*`) löst Version 7 weiterhin auf `/operator/orgs` auf. `safeReturnTo` (`frontend/src/auth/oidc.ts`) bleibt als zweite Schutzschicht. Ein eigenes Migrations-WP entfällt, weil die Migration mit Upd-05 erledigt ist. Folgeschritt ist React Router 8 (engines `node >=22.22.0`) nach dem Node-Wechsel (Upd-10/Upd-11). SR-62 ist in `docs/security/review-phase2.md` ohne Restrisiko geschlossen.

## E-246 – Befunde der Fremdimages aus dem Scan nach SR-60

**Status:** entschieden (2026-11-10, nach Empfehlung: Option 1; Beschluss vom 2026-11-09, Punkt 6); Risikoakzeptanz über die SLA hinaus offen als [A-85](#aufgaben-an-den-auftraggeber)
**Kontext:** Seit WP-873 scannt `image-scan-thirdparty` alle neun Fremdimages des Betriebs. Sechs scheitern am ersten Lauf mit 90 IDs (8 Critical, 82 High), alle in Abhängigkeiten der Hersteller-Images: Go-Standardbibliothek und Go-Module (`gosu` im Image `postgres` von keycloak-db, Hilfsprogramme im TimescaleDB-Image des logischen Backups, MinIO), Java-Bibliotheken (OpenSearch, Keycloak), Python-Pakete (certbot) und OS-Pakete. Keines der Images hat am 2026-11-10 ein neueres Release. Die eigenen Release-Images sind sauber: `reticora-postgres` baut die Go-Helfer der TimescaleDB-Basis mit der gepinnten Toolchain neu (E-96). `security/scan-exceptions.txt` gilt global (govulncheck, npm audit, alle Trivy-Läufe); eine Ausnahme für eine ID der Go-Standardbibliothek würde dort auch die eigenen Images verdecken.
**Optionen:**

1. Eigene Ausnahmedatei `security/scan-exceptions-thirdparty.txt` nur für `image-scan-thirdparty`, gleiche Prüfung und SLA (High 30, Critical 7 Tage ab 2026-11-10). Ein Folge-WP vor dem 2026-11-17 ersetzt, was sich im Projekt beheben lässt: Der logische Backup-CronJob nutzt das eigene Image `reticora-cmdb/postgres` statt des rohen TimescaleDB-Images, keycloak-db ein Image mit neu gebauten Go-Helfern (Muster E-96). Nachtrag 2026-11-10: Das Folge-WP ist WP-1178 (Backup-CronJob); keycloak-db geht zu A-85, weil seine Umstellung Install- und Upgrade-Tests braucht, die ohne lauffähige CI nicht nachzuweisen sind. Für MinIO und OpenSearch ohne Hersteller-Fix entscheidet der Auftraggeber (A-85).
2. Ausnahmen in der globalen Datei. Folge: verdeckt dieselben IDs in den Scans der eigenen Images.
3. Scan nur für Images mit nicht vertrauenswürdigen Eingaben (Stand vor WP-873). Folge: SR-60 bleibt offen, AK3 von WP-873 unerfüllt.

**Empfehlung:** Option 1. Die Befunde werden sichtbar und mit SLA geführt, ohne die Prüfung der eigenen Images zu schwächen. Die Critical-Befunde im Backup und in keycloak-db sind mit eigenen Images behebbar.

**Auswirkung:** WP-873 legt `security/scan-exceptions-thirdparty.txt` an, `image-scan-thirdparty` liest sie zusammen mit der globalen Datei, `tests/ci-config.test.sh` prüft SLA und Geltungsbereich. Der nächste Planstand führt das Folge-WP für Backup-CronJob und keycloak-db vor dem 2026-11-17; MinIO und OpenSearch folgen A-85. Ohne Behebung oder Entscheidung scheitert der Scan ab dem 2026-11-18 wieder (gewollt, SEC-11).

**Quelle:** CI-Lauf 42881796932 (image-scan-thirdparty, 2026-11-10); security/scan-exceptions.txt; deploy/docker-compose/postgres/Dockerfile (E-96)

**Betroffene IDs:** SEC-11 [Q]

**Betroffene WPs:** WP-873 (`p3-sr60-thirdparty-pins`), Folge-WP nach E-246

**Nachtrag 2026-11-13 (Update-Plan Upd-01, Teil NATS):** NATS läuft auf `nats:2.14.10-alpine@sha256:311da47e2f31c26e99c623cd78d486a1e8ab039c49b32652770bb91a4f70512e` (Compose `nats` und `nats-edge`, `nats-edge.yaml`, CI-Services, `load.yml`, `tests/k8s/fixtures/nats.yaml`, Fallback in `tests/nats-mtls.test.sh`). Die CI lädt nats-server v2.14.10 (linux-amd64, SHA256 `464f118a8aecf293e405514c1ab2c197a5d9209e58e36a5d05ad9577e5ee9b99` laut `SHA256SUMS` des Releases). Damit sind die sieben Repository-Advisories des Herstellers für ≤ 2.14.9 behoben: GHSA-p849-62j6-rhc5, GHSA-r9gv-x84w-62mr und GHSA-fwrx-9r9q-h7p4 (Leafnode), GHSA-7mrx-vpq7-fhmp und GHSA-p29q-ccgc-hcg7 (Auth-Callout), GHSA-4fpp-8grw-j976 (Route-Pool) mit High sowie GHSA-rr37-7hx9-8xw5 mit Medium. NATS braucht weiterhin keine Ausnahme: Trivy 0.75.0 meldet für 2.14.10-alpine 0 Critical und 0 High. Für 2.14.9-alpine meldete Trivy ebenfalls 0, obwohl die sieben Advisories bestanden. Diese Advisories stehen nur im Hersteller-Repository und nicht in der globalen Advisory-Datenbank, deshalb erkennt `image-scan-thirdparty` sie nicht. Diese Lücke schließt Upd-04 mit einer eigenen Abfrage der Repository-Advisories. Der Minor-Sprung auf 2.15.3 folgt mit Upd-20.
**Nachtrag 2026-11-13 (Update-Plan Upd-01, MinIO → Silo, nach Empfehlung):** Die Annahme „kein neueres Image“ gilt für MinIO nicht mehr. Upstream `minio/minio` ist archiviert. Das GitHub-Repo `pgsty/minio` heißt jetzt `pgsty/silo` und wird gepflegt (letzter Push 2026-11-13). Eingefroren ist nur der Docker-Hub-Name `pgsty/minio`: Die Release-Notes von 20260806 nennen 20260804 als letztes Release unter diesem Namen. Der Objektspeicher läuft deshalb auf `pgsty/silo:RELEASE.2026-11-12T00-00-00Z@sha256:c20d112390b9a9ce8c11b8af95ad35bcaa62f4759663c09c936cd8d2b511f329`. Digest per Docker Hub und GitHub-Release geprüft, Build mit Go 1.27.4. Das Image steht in Dev- und Betriebs-Compose, im kind-Fixture, in `ci.yml` und `load.yml`; `tests/ci-config.test.sh` (`objectstorage_image_refs`) erzwingt überall dieselbe Referenz. Das Release behebt die serverseitigen Advisories SN-2026-020 und SN-2026-022 (Critical) sowie SN-2026-021, -023, -024 und -025 (High). Trivy 0.75.0 (`--severity CRITICAL,HIGH --ignore-unfixed`, DB vom 2026-11-13) findet für amd64 und arm64 keinen Befund (vorher 3 Critical und 28 High). Damit fallen die 31 MinIO-IDs aus `security/scan-exceptions-thirdparty.txt`: 20 Einträge sind gestrichen, 11 bleiben für keycloak-db. A-85 ist für MinIO durch Ersatz erledigt.

- **Distroless-Variante nicht genommen:** Sie hat weder Shell noch `mc`. Davon hängen der Healthcheck (`mc ready local`), `tests/backup-config.test.sh` (`docker exec … mc`) und `tests/k8s/kind-smoke.sh` (`sh -s` mit `mc` im Pod) ab. Ihr Scan ist ebenfalls ohne Befund. Ein Wechsel lohnt erst, wenn diese Schritte ohne Shell im Container laufen.
- **Lokal nachgewiesen:**
  - `objectstorage_integration_test.go` läuft gegen Silo: Buckets, Versionierung, Versionen und Delete-Marker.
  - Lese-Proxy-Vertrag: Ein signierter GET liefert 200. Unsignierter oder manipulierter GET, anonymer PUT und Bucket-Listing liefern 403.
  - Upgrade auf bestehenden Daten: Silo startet auf dem Volume von `pgsty/minio` 20260804. Versionierung, Versionen und Delete-Marker bleiben erhalten.
  - Die strengere Prüfung von Policy-JSON (400 bei unbekannten Schlüsseln) betrifft Reticora nicht, weil es keine Bucket- oder IAM-Policy setzt.
- **Restrisiko Bus-Faktor:** Silo ist ein Community-Fork, den eine einzelne Organisation (PGSTY) pflegt. Die Release-Folge ist zurzeit dicht, es gibt aber keinen Hersteller-Support und keine zugesagte Pflegedauer. Gegenmaßnahmen:
  - `image-scan-thirdparty` scannt das Image täglich mit SEC-11-SLA.
  - Reticora nutzt nur das S3-Protokoll (SigV4, Versionierung, signierte URLs). Ein Wechsel auf einen anderen S3-Speicher ist deshalb eine Konfigurationsfrage (`RETICORA_S3_*`).
  - Kommt 90 Tage kein Release oder bleibt ein Critical-Befund über die SLA hinaus offen, gilt die Ersatzoption von A-85: externer S3-Speicher.
**Nachtrag 2026-11-13 (Update-Plan Upd-01, keycloak-db):** Der keycloak-db-Teil des Folge-WP ist umgesetzt. keycloak-db läuft auf dem eigenen Image `reticora-keycloak-db` (`deploy/docker-compose/keycloak-db/Dockerfile`) nach dem Muster von E-96, das damit unverändert auf ein zweites eigenes Image angewandt wird: Basis `postgres:16.16-alpine@sha256:0adbf25a9e96…` (Digest des Multi-Arch-Index gegen Docker Hub geprüft), `gosu` in der Pseudo-Version `v0.0.0-20261025094355-29b939aa5bd4` (Tag 1.20, proxy.golang.org) mit `golang:1.26.11-alpine@sha256:ae52b5bc6ed0…` neu gebaut und gegen sum.golang.org geprüft. PostgreSQL-Major, `PGDATA` und UID 70 bleiben die des Herstellers. Das Image steht in der Release-Matrix von `release.yml` (Scan vor dem Push, Signatur, SBOM-Attestierung) und in der Matrix von `image-scan`; `image-scan-thirdparty` scannt keycloak-db nicht mehr. `install-cloud.sh` prüft es im Release-Modus mit cosign und pinnt es als `RETICORA_KEYCLOAK_DB_IMAGE`, der Quell-Build baut es über `docker-compose.override.yml`. Trivy 0.75.0 am 2026-11-13: neues Image 0 Critical/High, Hersteller-Image 1 Critical (CVE-2025-68121) und 25 High, alle in der Go-Standardbibliothek von `gosu` (Go 1.24.6). Die 26 IDs sind aus `security/scan-exceptions-thirdparty.txt` entfernt (15 Zeilen gestrichen, 11 gelten weiter nur für minio; 60 IDs, 5 Critical). Der Upgrade-Nachweis, an dem die Umstellung am 2026-11-10 hing (A-85), ist `tests/keycloak-db-image.test.sh` (install-smoke, Job `reinstall`): Volumes von `postgres:16.15-alpine` und `16.16-alpine` laufen mit Daten und Passwort auf dem neuen Image weiter, ein leeres Volume initialisiert sich; lokal zusätzlich über die Compose-Dateien (Volume `keycloakdb` von 16.15 auf das per Override gebaute Image) nachgestellt. `tests/ci-config.test.sh` (`keycloak_db_image_pin`) prüft Pins und Neubau. MinIO und OpenSearch folgen weiter A-85.
**Nachtrag 2026-11-13 (Update-Plan Upd-02, OpenSearch 2.19.7 → 3.9.0):** Die Annahme „kein neueres Image“ gilt für OpenSearch nicht mehr. `opensearchproject/opensearch:3.9.0@sha256:3b55f6b6249cfdb92b52ee59adc8a266203ba9ef9f5d82544fad6f1af2f3609e` (GitHub-Release 2026-09-29, Docker Hub 2026-10-26, Digest gegen Docker Hub und `docker pull` geprüft) ist das neueste Release; 3.10.0 ist noch in Vorbereitung (opensearch-build#6484 offen). 2.19.7 bekommt laut endoflife.date noch Patches; der Grund für den Sprung ist der Ablauf der Critical-Ausnahmen am 2026-11-17, nicht ein EOL. Umgesetzt in Compose, `deploy/k8s/base/components/opensearch/statefulset.yaml` und im CI-Service. 3.9.0 enthält kein Bouncy Castle mehr: Die Critical-Ausnahmen CVE-2025-14813 und CVE-2026-8763 sowie vier High-Ausnahmen (jetty-security, bcprov, jackson-databind 2.x) sind gestrichen. Für OpenSearch ist A-85 damit durch Ersatz erledigt; die Ersatzoption „PostgreSQL-Suche als einziges Backend“ ist nicht nötig. Trivy 0.75.0 meldet für 3.9.0 0 Critical und 19 High-IDs; keine davon ist in einem Hersteller-Image behoben. Sie bleiben nach SEC-11 befristet mit ihrem ursprünglichen `since:` (Ablauf 2026-12-10, rpm, sqlite-libs und das neue jsoup CVE-2026-75140 am 2026-12-13), eine Verlängerung ist ausgeschlossen. Liefert der Hersteller bis 2026-12-10 kein Image mit den Fixes (erwartet 3.10.0), gilt die Empfehlung: ein eigenes, abgeleitetes Image nach E-96 (OS-Pakete aktualisiert, nicht genutzte Plugins entfernt) oder bis dahin die Ersatzoption von A-85. Nebenbei behoben: Compose und K8s starteten den Container mit `plugins.security.disabled` ohne `DISABLE_INSTALL_DEMO_CONFIG`; der Demo-Installer des Images (seit 2.12) bricht dann ohne `OPENSEARCH_INITIAL_ADMIN_PASSWORD` ab, unter 2.19.7 wie unter 3.9.0 lokal nachgewiesen. Außerdem ignoriert OpenSearch `ES_JAVA_OPTS`; die Heap-Grenze steht jetzt in `OPENSEARCH_JAVA_OPTS`. Neuaufbau statt Snapshot-Übernahme: siehe `docs/opensearch.md`.

<a id="e-247"></a>
## E-247 – Bedeutung von „Überlappung“ und strict_mode in der Subnetzhierarchie (IPM-02)

**Status:** entschieden (2026-11-12, Auftraggeber: Option 1)
**Kontext:** AK3 von WP-901 und IPM-02 nennen „Überlappung ohne echtes Enthaltensein“ als Fall für 409 in Sections mit `strict_mode`. Zwei CIDR-Präfixe können sich aber nur überschneiden, wenn eines das andere enthält; wörtlich genommen greift die Regel nie. Die erste Umsetzung behandelte jede Schachtelung außerhalb eines Pools als Überlappung und machte damit Master/Nested-Hierarchien in strikten Sections unmöglich.
**Optionen:**

1. Echte Schachtelung ist in jeder Section erlaubt und ohne Warnung. Als Überlappung gilt ein doppelt genutzter Adressbereich: ein Elternsubnetz, das kein Pool ist, hält eigene Adressen innerhalb eines Kindes, oder ein Subnetz hält eigene Adressen innerhalb eines seiner Kinder (`subnet_overlap_partners`). In einer strikten Section ergibt das 409 `cidr`, sonst eine Warnung `overlap`. Kinder eines Pools sind immer zulässig; ein Subnetz mit Adressen wird kein Pool (409 `is_pool`).
2. Jede Schachtelung außerhalb von Pools ist Überlappung. Folge: keine Master/Nested-Hierarchie in strikten Sections.

**Empfehlung:** Option 1.

**Auswirkung:** WP-901 setzt Option 1 um (Migration 000300, `hierarchy.go`). Die Klasse „schneidet“ entfällt in AK6 (`Relation` kennt disjoint, equal, contains, within). Die Prüfung läuft beim Schreiben eines Subnetzes (cidr, vrf, is_pool, Section); eine spätere Adresse im Elternsubnetz prüft sie nicht.

**Quelle:** Rückmeldung des Auftraggebers vom 2026-11-12; backend/internal/ipam/hierarchy.go

**Betroffene IDs:** IPM-02

**Betroffene WPs:** WP-901 (`p3-ipam-hierarchy`)

<a id="e-248"></a>
## E-248 – Nur-Lese-Umfang des ADMP-Clients: Allowlist der Pfade, kein Logout, keine benannten Abfragen

**Status:** entschieden (2026-11-12, nach Empfehlung: Option 1)
**Kontext:** Die ADMP-API-Doku liegt vor (A-55, `docs/plan/phase3/ADMP-API`, OpenAPI 3.1.0, ADMP API 9.14.0). Sie enthält 46 Operationen: 23 GET, 10 POST, 8 PATCH und 5 DELETE. Die Vorgabe des Auftraggebers lautet: „Alles nur lesend niemals schreibend) strikt nur lesend: Der Client kennt ausschließlich die GET-Endpunkte und die Anmeldung. Ein Test stellt sicher, dass POST, PATCH und DELETE technisch nicht erreichbar sind.“ Die Anmeldung ist `POST /V1/user/authentication`. Zwei Nicht-GET-Operationen könnten als zulässig gelten. `DELETE /V1/user/authentication` beendet die Sitzung („Logout – deletes your authentication cookie“). `POST /V1/query/execute/{name}` führt eine benannte Abfrage mit freien Argumenten aus (`{argumentz: SerializeableData[]}`); welche Abfragen es gibt und ob sie schreiben, steht nicht in der Doku. Der Transport aus WP-926 lässt jeden GET unterhalb der `base_url` zu. Für ADM-04 bis ADM-07 genügen sieben GET-Pfade. Mehrere andere GETs liefern Daten ohne fachlichen Bezug: Admin-Logs, Prozess-Engine, Aktionskatalog, Berechtigungsbaum mit eingebetteten `AccessNode`-Zugangsdaten und `AuthUser`-LDAP-Daten.

**Optionen:**

1. Feste Allowlist im Transport: `POST /V1/user/authentication` und genau sieben GET-Pfadvorlagen (`GET /V1/user/authentication`, `/V1/infrastructure/accessnodepicker`, `/V1/infrastructure/port/{portId}/dienstIds`, `/V1/dienst/{id}`, `/V1/dienst/{id}/properties/public`, `/V1/anschluss/{id}`, `/V1/organization/byIds`). Alles andere wird vor dem Versand abgewiesen, auch Logout und `query/execute` und die 16 übrigen GETs. Folge: kleinste Angriffsfläche und Datensparsamkeit; ein neuer Endpunkt braucht einen Planeintrag und eine Erweiterung des Spec-Tests.
2. Jeder GET unterhalb der `base_url` plus Anmeldung (Stand WP-926). Folge: erfüllt den Wortlaut, aber der Client könnte Admin-Logs, Berechtigungen und Zugangsdaten der Netzelemente lesen, ohne dass ein Test das bemerkt.
3. Wie 2, zusätzlich Logout beim Herunterfahren und `query/execute` für lesende Abfragen. Folge: widerspricht der Vorgabe („ausschließlich die GET-Endpunkte und die Anmeldung“); der Charakter benannter Abfragen ist nicht prüfbar.

**Empfehlung:** Option 1. Sie setzt die Vorgabe wörtlich um und geht beim Lesen nicht über den Bedarf hinaus. Die Sitzung endet ohne Logout, indem das Token verworfen wird ([E-250](#e-250)). `GET /V1/user/authorization` (Selbstauskunft der Rechte) bleibt ebenfalls draußen. Dass das Dienstkonto nur GET-Rechte hat, stellt der Auftraggeber sicher (A-86).

**Auswirkung:** WP-1073 ergänzt den `readOnlyTransport` um die Pfad-Allowlist (`allowedGets` je Methodendatei, Fehlerklasse `path_not_allowed`) und beschränkt den `authTransport` auf `POST` an genau `base_url` + `/V1/user/authentication`; Methode und Pfad sind Konstanten in `auth.go`, `Config.AuthMethod` entfällt. WP-1074 und WP-1075 tragen ihre Pfade ein. WP-1077 prüft gegen eine Kopie der Doku, dass jede Nicht-GET-Methode auf jedem Doku-Pfad außer `POST /V1/user/authentication` (darunter die 22 tabu gesetzten Nicht-GET-Operationen und PUT) und die 16 GET außerhalb der Allowlist null Treffer am Mock erzeugen, und dass die Allowlist genau der Planliste entspricht.

**Quelle:** docs/plan/phase3/ADMP-API (paths, components.securitySchemes); docs/spec/katalog-v3/10-phase3-ergaenzungen.md:191 (ADM-02, neu); Vorgabe des Auftraggebers (WP-927, Problem)

**Betroffene IDs:** ADM-02, ADM-04, ADM-05, ADM-06, ADM-07

**Betroffene WPs:** WP-926 (`p3-admp-client-frame`, Nachtrag), WP-927 (`p3-admp-readonly-proof`, Nachtrag), WP-1073 (`p3-admp-ep-port-service`), WP-1074 (`p3-admp-ep-service-props`), WP-1075 (`p3-admp-ep-connection`), WP-1077 (`p3-admp-contract-test`)

<a id="e-249"></a>
## E-249 – Credential für die ADMP-Anmeldung: Benutzername und Passwort

**Status:** entschieden (2026-11-12, nach Empfehlung: Option 1)
**Kontext:** `POST /V1/user/authentication` verlangt `{username: string, password: string}` (beide Pflicht, `additionalProperties: false`). Nach WP-925 und Konvention 33 referenziert ein ADMP-Konto nur ein Credential der Art `api_token` mit Quelle `stored` oder `vault_ref`; eine Art `admp` gibt es nicht. `api_token` hat als einziges Pflichtfeld `token` (`KindFields`, `backend/internal/credential/vault_ref.go:61`). `checkFieldMap` lehnt in der Tresor-Feldabbildung jedes andere Feld ab (`vault_ref.go:80–97`). Die Oberfläche kennt für `api_token` nur `token` (`frontend/src/components/discovery/CredentialsSection.tsx:100`). Ein gespeichertes `api_token` nimmt dagegen beliebige Felder an, weil `credential.Service.Create` sie nicht prüft (WP-925, Problem). Arten mit `username` und `password` gibt es (`ssh_password`, `redfish`, `ipmi`, `wmi`, `nut`, `nas`), alle für Geräteprotokolle und an die Collector-Plugins gebunden (`binding.go:53`, `collector/collectorcmd/testdata/credential-kinds.json`).

**Optionen:**

1. `api_token` mit dem zusätzlichen optionalen Feld `username`, `token` trägt das Passwort. `KindOptionalFields` erlaubt `username` in der `field_map`, die Oberfläche zeigt das Feld optional. Folge: keine Migration, keine neue Art, WP-925 und WP-932 bleiben gültig. Die Benennung `token` für ein Passwort ist ungenau und wird in der Oberfläche erklärt.
2. Neue allgemeine Art `http_login` (`username`, `password`) mit Migration der CHECK `credential_kind_check`, Eintrag im Kind-Vertrag des Collectors, Oberfläche und Kontoprüfung in WP-925. Folge: sauber benannt, aber rund ein zusätzliches WP und Eingriffe in bereits gemergte bzw. laufende WPs (WP-925, WP-932).
3. Benutzername als Spalte am Konto, Passwort als `token`. Folge: Zugangsdaten lägen teilweise im Konto; das widerspricht ADM-01 („nie im Konto selbst“).

**Empfehlung:** Option 1. Sie hält die Zugangsdaten vollständig im Secret-Store (CRD-02), braucht keine Migration und keinen Neuzuschnitt. Bis WP-1078 lässt sich ein gespeichertes Credential mit `username` und `token` über die API anlegen.

**Auswirkung:** WP-1073 liest im `Authenticator` `username` und `token`; fehlt eines, gilt die Fehlerklasse `credential`. WP-1078 ergänzt `KindOptionalFields` in `vault_ref.go` und das optionale Feld in `CredentialsSection`. WP-1077 nutzt für den Smoke-Test ein solches Credential. Konvention 33 bleibt bestehen (Nachtrag im Plan).

**Quelle:** docs/plan/phase3/ADMP-API (`postV1UserAuthentication`); backend/internal/credential/vault_ref.go:53–64, :80–97; frontend/src/components/discovery/CredentialsSection.tsx:100

**Betroffene IDs:** ADM-01, ADM-02, CRD-02

**Betroffene WPs:** WP-925 (`p3-admp-account`, Nachtrag), WP-932 (`p3-admp-ui-accounts`, Nachtrag), WP-1073 (`p3-admp-ep-port-service`), WP-1077 (`p3-admp-contract-test`), WP-1078 (`p3-admp-ui-connections`)

<a id="e-250"></a>
## E-250 – ADMP-Sitzung: Cookie AuthToken, Ablauf, Erneuerung und Anmeldestatus

**Status:** entschieden (2026-11-12, nach Empfehlung: Option 1)
**Kontext:** Laut `components.securitySchemes.cookie` ist das Token ein JWT im Cookie `AuthToken`, „Obtained by logging in“. Die Anmeldung antwortet 200 `{message}` mit `Set-Cookie`. Nicht dokumentiert sind: Laufzeit des Tokens, Cookie-Attribute (`Max-Age`, `Expires`, `Path`, `Secure`, `HttpOnly`), ein Refresh-Endpunkt, der Status bei falschen Zugangsdaten oder abgelaufenem Cookie (in der ganzen Doku kommen nur 200, 204 und 404 vor) und ein globales oder operationsbezogenes `security`. Den Anmeldestatus liefert `GET /V1/user/authentication` (200 `AuthUser` mit LDAP-Daten des Dienstkontos). Die Doku hat keinen `servers`-Eintrag. WP-926 sieht `Authenticator.Token(ctx)` mit Cache bis Ablauf minus 60 s und einmaliger Erneuerung bei 401 vor.

**Optionen:**

1. Ablauf aus `Max-Age`/`Expires` des Cookies, sonst aus dem Claim `exp` des JWT (nur gelesen), sonst 10 min. Das Cookie wird ohne `CookieJar` nur an die `base_url` gesendet. 401 löst genau eine Neuanmeldung und eine Wiederholung aus, 403 gilt als `auth` ohne Neuanmeldung. Kein Logout. `GET /V1/user/authentication` dient als Statusprüfung je Konto zu Beginn von `admp.refresh_linked` (Erfolg setzt `last_ok_at`); nur `id` und `organizationId` werden dekodiert. `auth_url` leer bedeutet `base_url` + `/V1/user/authentication`; ein gesetztes `auth_url` muss genau gleich diesem Wert sein (gleiches Schema, gleicher Host, gleicher Port, gleiches Pfadpräfix ohne zusätzliche Segmente), sonst bricht die Anmeldung ohne Netzwerkzugriff ab, und die Konto-API weist den Wert mit 422 ab. So gehen Benutzername und Passwort nie an einen anderen Host. Folge: robust gegenüber fehlenden Angaben, wenige Anmeldungen.
2. Vor jedem Abruf neu anmelden. Folge: kein Ablaufproblem, aber doppelte Last und viele Anmeldungen im ADMP-Protokoll.
3. Vor jedem Abruf den Status per `GET /V1/user/authentication` prüfen. Folge: doppelte Anfragen; dass ADMP ungültige Cookies mit 401 beantwortet, bleibt trotzdem ungeprüft.

**Empfehlung:** Option 1. Sie nutzt nur dokumentierte Pfade, kommt ohne Logout aus und macht aus den fehlenden Angaben sichere Annahmen. Ein kurzer Ersatzablauf von 10 min begrenzt den Schaden, falls ADMP abgelaufene Cookies nicht mit 401 meldet. Der Smoke-Test (WP-1077) und A-86 bestätigen das Fehlerverhalten.

**Auswirkung:** WP-1073 setzt den `Authenticator` (mit Prüfung des Ursprungs von `auth_url` und den Konstanten `authMethod`/`authPath` in `auth.go`), die Cookie-Übergabe in `getJSON` und `GetSession` um; WP-1078 ergänzt die Prüfung von `auth_url` in der Konto-API; WP-1077 simuliert im Mock ein abgelaufenes Cookie (401) und prüft genau eine Neuanmeldung. Nachträge in WP-925, WP-926 und WP-932.

**Quelle:** docs/plan/phase3/ADMP-API (`components.securitySchemes.cookie`, `/V1/user/authentication`)

**Betroffene IDs:** ADM-02, ADM-03

**Betroffene WPs:** WP-925, WP-926, WP-932 (Nachträge), WP-1073 (`p3-admp-ep-port-service`), WP-1077 (`p3-admp-contract-test`), WP-1078 (`p3-admp-ui-connections`)

<a id="e-251"></a>
## E-251 – ADMP-Port-Kennung und Zuordnung zum network_interface (ADM-04)

**Status:** entschieden (2026-11-12, nach Empfehlung: Option 1); Port-Benennung offen als [A-86](#aufgaben)
**Kontext:** ADM-04 verlangt die Speicherung als `admp_service_port` mit Verweis auf `network_interface` und CI. Die Doku kennt die Zuordnung nur in Richtung Port → Dienste: `GET /V1/infrastructure/port/{portId}/dienstIds` („Returns all dienstIds“, ohne Antwortschema). `Dienst` und `DienstProperty` haben keinen Port-Verweis. Ein Port ist `AccessPort {id, moduleId, portIndex, extra}` unter `AccessModule {id, nodeId, unitIndex, technologiesAvailable}` unter `AccessNode {id, name, url, typeId}`; `AccessNodeType.protocol` ist `KEYMILE_SSH` oder `ZYXEL_SNMP`. Einen Interface-Namen, `ifIndex` oder eine MAC gibt es nicht. Das Inventar ist nur über `GET /V1/infrastructure/accessnodepicker` erreichbar (Antwort `200: {}` ohne Schema). Es enthält nach dem Schema auch `url`, `username`, `password` und `iv` der Netzelemente. In Reticora ist ein Interface je CI über `name` eindeutig; das Gerät hat `ci.hostname_lc` (nicht eindeutig).

**Optionen:**

1. `port_key` = `AccessPort.id`. Eine neue Tabelle `admp_access_port` (Port-Inventar je Konto) wird über `accessnodepicker` gefüllt, höchstens alle 24 h und bei Bedarf. Die Zuordnung: Gerät über `lower(AccessNode.name)` = `ci.hostname_lc`, sonst über das erste Hostnamen-Label, jeweils nur bei genau einem Treffer. Port über ein Interface dieses CIs, dessen Name auf `<unitIndex>/<portIndex>` endet. Mehrdeutig ergibt `none`, manuell bleibt manuell. Zugangsdaten der Netzelemente werden nicht dekodiert. Folge: Abruf je Interface ohne erneutes Inventar und ein Ergebnis auch für Ports ohne Dienst; dafür eine Migration in WP-1073.
2. Kein Inventar im Cache: Bei jedem Abruf eines Interfaces das ganze Inventar laden und im Speicher abgleichen. Folge: keine Migration, aber je Abruf ein großer GET mit Zugangsdaten der Netzelemente im Antwortkörper, und Ports ohne Dienst werden immer wieder abgefragt.
3. Nur manuelle Zuordnung (`matched_by = manual`) über eine neue Route. Folge: kein Regelrisiko, aber hoher Pflegeaufwand bei zehntausenden Ports, und ADM-08 bleibt ohne Pflege leer.

**Empfehlung:** Option 1. Sie nutzt nur dokumentierte Kennungen, hält das Inventar mit einem GET je Tag und Konto aktuell und macht ADM-08 auch für Ports ohne vorherigen Abruf möglich. Wie Interfaces der Hersteller (Keymile, Zyxel) heißen und ob die Indizes 0- oder 1-basiert sind, klärt A-86. Bis dahin gilt die Endungsregel ohne Versatz; nicht zugeordnete Ports stehen im Job-Ergebnis.

**Auswirkung:** WP-1073 legt `admp_access_port` (Migration, RLS wie `admp_service_port`) an, implementiert `ListAccessPorts`, `ListPortServiceIDs`, `match.go`, die Objektarten `inventory` und `port` sowie `ReplacePortServices`. WP-928 und WP-929 erhalten Nachträge. Eine Route für die manuelle Zuordnung ist nicht eingeplant.

**Quelle:** docs/plan/phase3/ADMP-API (`/V1/infrastructure/port/{portId}/dienstIds`, `/V1/infrastructure/accessnodepicker`, Schemas `AccessNode`, `AccessModule`, `AccessPort`, `Dienst`); backend/internal/ipam/model.go:102–125; backend/migrations/000110_interface_net01.up.sql:39; backend/migrations/000134_ci_identity_lowercase.up.sql:9–12

**Betroffene IDs:** ADM-04, ADM-08

**Betroffene WPs:** WP-928, WP-929, WP-931 (Nachträge), WP-1073 (`p3-admp-ep-port-service`), WP-1077 (`p3-admp-contract-test`)

<a id="e-252"></a>
## E-252 – Herkunft von Netzbetreiber, Vertrags-ID, Adresse und Adressform (ADM-07)

**Status:** entschieden (2026-11-12, nach Empfehlung: Option 1)
**Kontext:** ADM-07 verlangt Netzbetreiber, Vertrags-ID und Adresse. `Anschluss` (`GET /V1/anschluss/{id}`) hat `netzbetreiberId` (number), `vertragId` (string oder null), `strasse`, `hausnummer`, `hausnummerzusatz`, `plz` und `ort` (je string oder null), `created`, `removed` und die optionalen Relationen `netzbetreiber` (`Organization`), `inhaber[]`, `dienste[]` und `actions[]`. Einen Netzbetreibernamen hat der Anschluss nicht. Er steht in `Organization.name` (dazu `shortName`, `netzbetreiberNummer`) und ist über die eingebettete Relation oder `GET /V1/organization/byIds?id=…` erreichbar. `Organization` kann ihrerseits `authUsers` und `accessNodes` mit Zugangsdaten einbetten. Ob ADMP Relationen mitliefert, ist nicht dokumentiert. Geschäfts- und Privatanschluss unterscheidet nur `AnschlussInhaber.typ` (`Privat`, `Gewerblich`, `Genossenschaft`). Nach [E-243](#e-243) gilt eine Adresse ohne Unterscheidung als privat. Bei fehlendem Anschluss antwortet ADMP 404 `NamedError`.

**Optionen:**

1. `carrier` = `Organization.name`, aus `netzbetreiber` oder, wenn nicht eingebettet, aus `GET /V1/organization/byIds` mit genau einem `id` (je Job-Lauf zwischengespeichert). `shortName`, `netzbetreiberNummer` und `netzbetreiberId` kommen in `properties`. `contract_id` = `vertragId`. Die Adresse ist eine Zeile aus Straße, Hausnummer mit Zusatz, PLZ und Ort. Adressform: `Privat` bei einem Inhaber ergibt `private`, nur `Gewerblich`/`Genossenschaft` ergibt `business`, ohne Inhaberangabe gilt `private`. `created`/`removed` gehen als `created_at`/`removed_at` in `properties`. 404 löscht den Cache-Eintrag samt Subjects. Folge: alle Felder aus dokumentierten Pfaden, höchstens ein Zusatzabruf je Netzbetreiber.
2. Nur `netzbetreiberId` speichern, keinen Namen. Folge: ein Abruf weniger, aber die Handlungsempfehlung zeigt eine Zahl statt eines Netzbetreibers; ADM-07 wäre nur formal erfüllt.
3. `shortName` statt `name` als `carrier`. Folge: kürzer in Tabellen, aber nicht garantiert sprechend; der volle Name fehlte im Export.

**Empfehlung:** Option 1. Sie erfüllt ADM-07 vollständig und bleibt bei Personenbezug und Zugangsdaten datensparsam: Aus `Organization` werden nur vier Felder dekodiert. Bei 404 wird gelöscht statt `stale` markiert, weil ein gelöschter Anschluss keine PII im Cache rechtfertigt. Ein Anschluss mit `removed` ungleich null bleibt mit `removed_at` sichtbar, bis A-86 die Bedeutung klärt.

**Auswirkung:** WP-1075 setzt `GetConnection`, `GetOrganization`, die Abbildung und das Löschen bei 404 um und beschreibt `address_form` und `removed_at` additiv in `api/openapi.yaml`. WP-928, WP-930 und WP-931 erhalten Nachträge.

**Quelle:** docs/plan/phase3/ADMP-API (Schemas `Anschluss`, `Organization`, `AnschlussInhaber`, `NamedError`; `/V1/anschluss/{id}`, `/V1/organization/byIds`); [E-243](#e-243)

**Betroffene IDs:** ADM-07, ADM-08, SEC-05

**Betroffene WPs:** WP-928, WP-930, WP-931 (Nachträge), WP-1075 (`p3-admp-ep-connection`)

<a id="e-253"></a>
## E-253 – Diensteigenschaften, Lebenszyklus und Zeitwerte (ADM-05)

**Status:** entschieden (2026-11-12, nach Empfehlung: Option 1)
**Kontext:** `GET /V1/dienst/{id}/properties` und `GET /V1/dienst/{id}/properties/public` haben ein identisches Antwortschema: ein Array eines Inline-Schemas, das `DienstProperty` (`key`, `value`, `isPublic`, `title`, `description`, `orderIndex`, `timestamp`, `input`) entspricht und zusätzlich das Pflichtfeld `label` hat (nicht in `components.schemas.DienstProperty`, dort gilt `additionalProperties: false`). Dass `/public` nur Eigenschaften mit `isPublic = true` liefert, folgt nur aus dem Pfadnamen. Welche Schlüssel es gibt, ist nicht dokumentiert; typisierte Felder wie Bandbreite oder Technologie lassen sich daher nicht festlegen. `Dienst` hat feste Zeitfelder `gebucht`, `bereitstellung`, `bereitgestellt`, `gekuendigt`, `abschaltung` und `abgeschaltet`, jeweils `number` oder null, dazu `templateId` und optional `template`. Einen Status-Enum gibt es nicht. Alle Zeitwerte der Doku (auch `Anschluss.created`/`removed`) sind `number` ohne Einheit.

**Optionen:**

1. Nur `/properties/public`; Speicherung als `values.<key> = {value, label, order}`. Typisiert werden nur die Lebenszyklusdaten und `template_id`; der Status wird abgeleitet: `terminated`, wenn `abgeschaltet` gesetzt ist und nicht in der Zukunft liegt, sonst `cancelled` bei `gekuendigt`, `active` bei `bereitgestellt`, `ordered` bei `gebucht`, sonst `unknown`. Zeitwerte mit Betrag ab 10^11 gelten als Millisekunden, kleinere als Sekunden (eindeutig für alle Daten nach 1973), umgerechnet nach RFC 3339 UTC. Folge: datensparsam, keine erfundenen Felder.
2. `/properties` (alle Eigenschaften). Folge: mehr Information, aber interne Werte mit möglichem Personenbezug im Cache; Bedarf unbelegt.
3. Wie 1, aber feste Typfelder für vermutete Schlüssel (`bandwidth`, `technology`). Folge: Felder ohne Grundlage in der Doku.

**Empfehlung:** Option 1. Die öffentliche Variante genügt dem Wortlaut von ADM-05 und minimiert Personenbezug (SEC-05). Zeigt der Schlüsselkatalog (A-86), dass benötigte technische Werte nur intern vorliegen, wechselt WP-1074 vor Beginn auf `/properties`; dafür ändert sich nur ein Pfad der Allowlist. Die Zeitregel vermeidet eine Festlegung ohne Doku und ist für reale Daten eindeutig.

**Auswirkung:** WP-1073 speichert Lebenszyklus und Template in `admp_service.properties`, WP-1074 die Eigenschaften und die typisierten API-Felder (`status`, `booked_at`, `provisioning_planned_at`, `provisioned_at`, `cancelled_at`, `shutdown_planned_at`, `shut_down_at`, `template_id`, `properties`), WP-1075 `created_at`/`removed_at`, WP-1078 die Darstellung.

**Quelle:** docs/plan/phase3/ADMP-API (`getV1Dienst:idProperties`, `getV1Dienst:idPropertiesPublic`, Schemas `Dienst`, `DienstProperty`, `SerializeableData`)

**Betroffene IDs:** ADM-05, ADM-07, ADM-09, SEC-05

**Betroffene WPs:** WP-1073 (`p3-admp-ep-port-service`), WP-1074 (`p3-admp-ep-service-props`), WP-1075 (`p3-admp-ep-connection`), WP-1078 (`p3-admp-ui-connections`)

<a id="e-254"></a>
## E-254 – Anschlussinhaber aus ADMP: Quelle, mehrere Inhaber und Feldauswahl (ADM-06)

**Status:** entschieden (2026-11-12, nach Empfehlung: Option 1)
**Kontext:** Die Doku hat keinen Einzelabruf des Inhabers. Er steht als optionale Relation `inhaber[]` (`AnschlussInhaber`) in `GET /V1/anschluss/{id}`. Daneben gibt es `GET /V1/anschluss/inhaber/paginator` mit `rows`, `skip`, `first` (je Pflicht), `filters` (JSON-String), `sortField` und `sortOrder`. Der Aufbau von `filters` ist nicht dokumentiert, das Element-Schema ist als `ChangeLog` angegeben. `AnschlussInhaber` hat `typ`, `titel`, `name`, `vorname`, `geburtsdatum`, `registerinstanz`, `registernummer` und eine eigene Anschrift. Ein Anschluss kann mehrere Inhaber haben. [E-243](#e-243) legt die Vault-Speicherung als `person` fest; `admp_connection.holder_subject_id` verweist auf genau ein Subject, und `pii.Data` ist eine flache Abbildung von Zeichenketten.

**Optionen:**

1. Inhaber nur aus `inhaber[]` von `GET /V1/anschluss/{id}`, ohne Paginator. Alle Inhaber eines Anschlusses liegen in einem Subject der Art `person` mit den Schlüsseln `count` und `<n>.<feld>`. Gespeichert werden `typ`, `titel`, `vorname`, `name`, `registerinstanz`, `registernummer` und die Anschrift; `geburtsdatum` nicht. Fehlt die Relation, bleibt das Subject unverändert; eine leere Liste löscht es. Folge: kein zusätzlicher Pfad, ein Subject je Anschluss, Suchtoken über alle Werte (Auskunft findet jeden Inhaber).
2. Inhaber über den Paginator mit einem angenommenen Filter auf `anschlussId`. Folge: unabhängig von eingebetteten Relationen, aber Filtersyntax und Element-Schema wären erfunden.
3. Je Inhaber ein eigenes Subject (Kind-Subjects des ersten). Folge: feinere Löschung je Person, aber die Auflösung braucht mehrere Subjects und die Verankerung der Adresse ist mehrdeutig.

**Empfehlung:** Option 1. Sie nutzt nur dokumentierte Felder und keinen zusätzlichen Pfad. Das Geburtsdatum wird für die Störungsbearbeitung nicht gebraucht und entfällt (Datenminimierung). Mehrere Inhaber bleiben vollständig auffindbar. Liefert ADMP `inhaber` nicht eingebettet (A-86), folgt ein Nachtrags-WP für den Paginator, sobald dessen Filtersyntax bekannt ist.

**Auswirkung:** WP-1075 dekodiert `inhaber[].typ` für die Adressform, WP-1076 die übrigen Felder, speichert das Subject, liefert `holders` an der Holder-Route und `holder_count` an der Anschlussroute. WP-1078 zeigt mehrere Inhaber.

**Quelle:** docs/plan/phase3/ADMP-API (Schema `AnschlussInhaber`, `/V1/anschluss/{id}`, `/V1/anschluss/inhaber/paginator`); backend/internal/pii/vault.go:31, :130, :207; backend/internal/pii/tokens.go:60; [E-243](#e-243)

**Betroffene IDs:** ADM-06, SEC-05

**Betroffene WPs:** WP-1075 (`p3-admp-ep-connection`), WP-1076 (`p3-admp-ep-holder`), WP-1078 (`p3-admp-ui-connections`)

<a id="e-255"></a>
## E-255 – Vertragstest ohne Beispiele in der ADMP-Doku: Fixtures aus den Schemas

**Status:** entschieden (2026-11-12, nach Empfehlung: Option 1)
**Kontext:** WP-1077 sah einen Mock „aus den Doku-Beispielen“ vor. Die Doku enthält keine Beispiele; das einzige `example` ist `""` am Parameter `filters`. Zwei verwendete Antworten haben kein Schema (`accessnodepicker`, `port/{portId}/dienstIds`). Die Schemas nutzen nur `type` (einschließlich `null` und `integer`), `required`, `properties`, `additionalProperties`, `items`, `enum`, `anyOf` und `$ref` (auf `components.schemas` und eingebettete `$defs`). Daneben kommen die Annotationen `description`, `title`, `example` und `contentMediaType` vor, die nichts validieren. Einer der Verweise ist defekt (`#/components/schemas//schemas/object` zeigt auf das Schema mit dem Namen `/schemas/object`). `backend/go.mod` enthält keine JSON-Schema-Bibliothek.

**Optionen:**

1. Fixtures je `operationId` aus den Schemas von Hand bauen. Ein kleiner Prüfer im Testcode (`specschema_test.go`) prüft genau die genannten Validierungs-Schlüsselwörter, ignoriert die Annotationen und löst `$defs` als Verweisziel auf; ein unbekanntes Validierungs-Schlüsselwort lässt ihn scheitern. Eine unveränderte Kopie der Doku liegt in `readonly/testdata`, abgesichert über die SHA-256. Antworten ohne Schema werden gegen die angenommene Form geprüft und mit `assumed: A-86` gekennzeichnet. Folge: keine neue Abhängigkeit; eine geänderte Doku bricht den Test sichtbar.
2. Eine JSON-Schema-Bibliothek (Draft 2020-12) als Testabhängigkeit. Folge: vollständige Prüfung, aber eine neue Abhängigkeit mit Lizenz- und Scan-Aufwand für einen kleinen Schlüsselwortumfang.
3. Fixtures ohne Schemaprüfung. Folge: Abweichungen zwischen Mock und Doku blieben unbemerkt; der Vertragstest prüfte nur sich selbst.

**Empfehlung:** Option 1. Sie bindet den Mock nachweisbar an genau diese Doku, ohne neue Abhängigkeit. Die angenommenen Formen sind sichtbar gekennzeichnet und werden mit dem Smoke-Test bzw. A-86 bestätigt.

**Auswirkung:** WP-1073 bis WP-1076 legen ihre Fixtures in `readonly/testdata/admp_fixtures.json` an, WP-1077 liefert Kopie, Prüfer, Spec-Negativtest und Gesamtablauf.

**Quelle:** docs/plan/phase3/ADMP-API; backend/go.mod

**Betroffene IDs:** ADM-02

**Betroffene WPs:** WP-1073 (`p3-admp-ep-port-service`), WP-1074, WP-1075, WP-1076, WP-1077 (`p3-admp-contract-test`)

<a id="e-256"></a>
## E-256 – Section-Rechte nach Löschen des letzten berechtigten Teams (fail-closed)

**Status:** entschieden (2026-11-12, nach Empfehlung: Option 1); Umsetzung als Nachtrag zu WP-910
**Kontext:** Nach [E-214](#e-214) gilt eine Section als beschränkt, solange sie mindestens einen Eintrag in `ipam_section_permission` hat. WP-910 AK1 verlangt `ON DELETE CASCADE` vom Team auf `ipam_section_permission`. Löscht jemand das letzte berechtigte Team einer Section, verschwinden damit alle Einträge, und die Section ist ohne weitere Aktion für jeden Principal mit `ipam:read`/`ipam:write` offen (fail-open). Der Befund stammt aus der Prüfung von WP-910 am 2026-11-12.
**Optionen:**

1. Die Section erhält `acl_enabled boolean NOT NULL DEFAULT false`. Ein Trigger setzt das Flag beim ersten Eintrag in `ipam_section_permission`; `ipam_section_denied` sperrt jede Section mit `acl_enabled`, für deren Stufe der Principal keinen passenden Eintrag hat, auch wenn keine Einträge mehr bestehen. Die Freigabe ist eine ausdrückliche, auditierte Änderung (`PATCH /ipam-sections/{id}` mit `acl_enabled=false`, Recht `ipam:write` plus `write` auf die Section oder org-weiter Admin). Folge: fail-closed; eine Migration und ein Feld mehr.
2. Wie im Plan: CASCADE, die Section wird offen. Folge: Rechteverlust führt zu Sichtbarkeit, entgegen TEN-05.
3. `ON DELETE RESTRICT` für den letzten Eintrag einer Section: Das Team lässt sich erst löschen, wenn die Section anders berechtigt oder geöffnet ist. Folge: fail-closed, blockiert aber das Löschen von Teams mit 409 und betrifft die Teamverwaltung außerhalb von IPAM.

**Empfehlung:** Option 1. Sicherheitsrelevante Grenzen dürfen durch das Entfernen einer Berechtigung nicht weiter werden; die Freigabe bleibt eine bewusste Entscheidung mit Audit, ohne die Teamverwaltung zu blockieren.

**Auswirkung:** Nachtrag zu WP-910: Migration mit `ipam_section.acl_enabled`, Backfill (`true` für Sections mit Einträgen), Trigger, angepasste `ipam_section_denied`, PATCH-Feld, Tests „letztes Team gelöscht → Section bleibt gesperrt“ und „Freigabe nur ausdrücklich“ im Kanalmatrix-Test.

**Quelle:** Prüfbericht WP-910 (2026-11-12); backend/migrations/000320_ipam_section_permission.up.sql

**Betroffene IDs:** IPM-10, TEN-05

**Betroffene WPs:** WP-910 (`p3-ipam-section-acl`)

<a id="e-257"></a>
## E-257 – PostgreSQL 16.15 im eigenen Image reticora-postgres nach dem Release 16.16

**Status:** entschieden (2026-11-13, nach Empfehlung: Option 1)
**Kontext:** Seit 2026-11-13 meldet `image-scan (postgres)` 38 High-CVEs in PostgreSQL 16.15, behoben in 16.16 (Release 2026-11-12). Trivy sieht sie an den apk-Paketen `postgresql16`, `postgresql16-client`, `postgresql16-plpython3` und `libpq`. Der laufende Server ist aber ein Quell-Build 16.15 der Basis `timescale/timescaledb:2.31.0-pg16` unter `/usr/local`; ihn und die von pgBackRest, `psql` und `pg_dump` geladene `libpq` 16.15 (`/usr/local/lib`) sieht Trivy nicht. Ein `apk upgrade` der Pakete machte den Scan grün, ohne den Server zu ändern. Ein TimescaleDB-Image mit 16.16 gibt es am 2026-11-13 nicht (2.31.0-pg16 und 2.32.0-pg16 tragen PG_VERSION=16.15).
**Optionen:**

1. Befristete Ausnahmen in `security/scan-exceptions.txt` (since 2026-11-13, exp 2026-12-13 = SEC-11 für High) und Wechsel auf das erste TimescaleDB-pg16-Image mit PostgreSQL ≥ 16.16 (Version und Digest an allen Stellen nach E-96). Gibt es bis 2026-12-06 keines, baut ein Folge-WP PostgreSQL 16.16 aus dem Quelltext (sha256-geprüfter Tarball, Konfiguration wie `pg_config --configure`) über die Dateien in `/usr/local` und prüft im Build `postgres --version`.
2. Sofort PostgreSQL 16.16 aus dem Quelltext bauen. Folge: längerer Image-Build, eigener Pflegeaufwand für jede Minor-Version.
3. `apk upgrade` der Pakete. Folge: Scan grün, Server und Client-Bibliothek bleiben verwundbar (verdeckter Befund).

**Empfehlung:** Option 1. Der Scan zeigt das Risiko weiter an und schlägt mit Ablauf der Frist wieder fehl; der Hersteller-Fix kommt erfahrungsgemäß binnen Tagen. Option 3 ist ausgeschlossen, weil sie den Befund verdeckt.

**Auswirkung:** 38 Einträge in `security/scan-exceptions.txt` mit Verweis auf E-257; Nachtrag in `docs/security/abhaengigkeitsscans.md`. Prüfpunkt 2026-12-06: TimescaleDB-Image mit 16.16 pinnen oder Folge-WP nach Option 2 starten.

**Quelle:** CI-Lauf 43403501444 (Job 131811976341); Docker Hub timescale/timescaledb (2.31.0-pg16 sha256:322ae364…, 2.32.0-pg16 sha256:2f87efea…); Prüfbericht vom 2026-11-13 (ldd: pgBackRest lädt `/usr/local/lib/libpq.so.5` 16.15)

**Betroffene IDs:** SEC-11 [Q]

**Betroffene WPs:** WP-873 (Scan), Folge-WP nach Option 2 falls nötig


<a id="e-258"></a>
## E-258 – cosign v3: Bundle-Format, Mindestversion und Übergang der Collector-Hosts

**Status:** entschieden (2026-11-13, nach Empfehlung: Option 1)
**Kontext:** Release, Installer und Updater nutzen cosign v2.4.1 (Werkzeug nach [E-31](../entscheidungen.md#e-31)). Diese Version ist von GHSA-fx35-mq7g-6g98 betroffen (high, 2026-08-06). Der Advisory: „keyless verification using --certificate-identity and --certificate-oidc-issuer can be bypassed when a legacy JSON --bundle is supplied“. Genau so prüfen `install.sh`/`install-preflight.sh` (`cosign_verify_blob`) und `edgecore/update` (`CosignVerifier`). Ebenfalls betroffen ist v2.4.1 von GHSA-whqx-f9j3-ch6m (medium), GHSA-w6c6-c85g-mmv6 (medium) und GHSA-wfqv-66vq-46rm (low). Behoben ist alles in v3.1.3; die v2-Linie hat den Fix in v2.6.5. cosign v3 schreibt Sigstore-Bundles (v0.3) und legt Image-Signaturen als OCI-Referrer ab. Die `docker-reference` der Image-Signatur ist dann die signierte Referenz mit Digest. Das Release `v1.0.0-rc.1` ist mit Legacy-Bundles veröffentlicht. Collector-Hosts mit Auto-Updates haben cosign v2.4.1 als `/usr/local/bin/cosign`. cosign v2.4.1 liest die neuen Bundles nicht (lokal geprüft: `bundle does not contain cert for verification`).
**Optionen:**

1. cosign v3.1.4 überall (Release, CI, Installer), Signieren nur im neuen Format. Installer und Updater nutzen ein installiertes cosign erst ab v3.1.3 (`COSIGN_MIN_VERSION`, `update.MinCosignVersion`). Die Installer greifen sonst auf das gepinnte cosign zurück; der Updater lehnt jedes Update ab und warnt beim Start. Altinstallationen ziehen cosign einmalig nach (erneuter Lauf von `install-vm.sh` oder Tausch des Binaries). Bis dahin bleiben ihre Updates fail-closed abgelehnt.
2. Wie 1, aber jedes Release zusätzlich mit Legacy-Bundle für alte Collector-Hosts. Folge: Die Hosts würden weiter mit dem verwundbaren cosign prüfen; der angreifbare Prüfpfad bliebe für sie bestehen. Dazu kämen doppelte Bundles, Tests und Doku.
3. Zwischenschritt v2.6.5 (Fix ohne Formatwechsel). Folge: Die v2-Linie bekommt nur noch einzelne Sicherheitsfixes; der Formatwechsel bleibt offen und fällt später mit cosign v4 ohnehin an.

**Empfehlung:** Option 1. Sie schließt den Umgehungspfad für jeden Host, der aktualisiert. Alte Hosts installieren nichts Ungeprüftes (fail-closed nach TEC-15), auch nicht mit dem alten Collector-Binary. Der Übergang ist ein einmaliger, dokumentierter Schritt. Option 2 hielte genau die Lücke offen, die das Update schließt.

**Auswirkung:** Upd-06 (Update-Plan 2026-11-13): `release.yml`, `ci.yml` (`cosign-verify`), `install-smoke.yml`, `install.sh`/`install-preflight.sh`, `install-vm.sh` (gepinntes cosign für die Auto-Updates), `edgecore/update` und `collector/collectorcmd` (Mindestversion, Warnung `collector.update.cosign_outdated`). `sigstore/cosign-installer` v4.1.3 per Commit-SHA. Die Installer erkennen die Image-Signatur nach Repository und, falls die Referenz einen Digest trägt, nach dem signierten Digest. `release.yml` verlangt Bundles v0.3 und eine Image-Signatur, die das Image mit dem gepushten Digest nennt. Nachweis der Rückwärtskompatibilität: Fixture `edgecore/update/testdata/legacy-release-v1.0.0-rc.1` (echtes Legacy-Bundle), geprüft in `ci.yml` (`TestCosignVerifierWithLegacyReleaseBundle`) und in `tests/install-signature.test.sh` (echtes cosign). Betriebshinweis: `docs/betrieb/collector-updates.md`, Abschnitt „cosign v3“.

**Quelle:** `gh api repos/sigstore/cosign/security-advisories` (2026-11-13); Release v3.1.4 (2026-10-20, `cosign_checksums.txt`); Release-Notes v3.0.1, v3.1.1, v3.1.3; Update-Plan und Prüfbericht 2026-11-13 (Upd-06, Ergänzung B/C.8)

**Betroffene IDs:** TEC-15, COL-06, SEC-11 [Q]

**Betroffene WPs:** WP-387, WP-545, WP-546, WP-148 (Bestand); Upd-06
