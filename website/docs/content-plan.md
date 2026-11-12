# Inhaltsplan – Website für Reticora CMDB

| | |
|---|---|
| Stand | 2026-11-12 |
| Status | **Entwurf – Freigabe ausstehend** |

## 1. Regeln für Inhalte

1. **Präsens, ohne Roadmap.** Alle Funktionen des Paketumfangs stehen als
   verfügbar da. Keine Phasenkürzel (P1–P5), kein „bald“, kein „geplant“
   (Vorgabe des Auftraggebers, `project-brief.md` W-E02).
2. **Prüfung vor dem Go-live.** Jede Aussage im Claim-Register (§7) wird vor
   dem Go-live gegen den ausgelieferten Stand geprüft. Was dann nicht stimmt,
   wird gestrichen oder geändert.
3. **Keine erfundenen Zahlen, Kunden, Zitate, Siegel.** Zielwerte aus
   Planungsdokumenten sind keine Zusagen und stehen nur mit Freigabe auf der
   Website.
4. **Begriffe der App.** Kunde (nicht „Client“ oder „Mandant“ im Fließtext),
   Standort, CI, Review, Collector, Scope.
5. **Screenshots sind Platzhalter**, bis die Bilder aus der Demo-Organisation
   vorliegen (§8).

## 2. Sitemap

```
/                              Startseite
/produkt/
  discovery/                   Auslesen: Collector, Protokolle, Ablauf
  datenqualitaet/              Identität, Quellränge, Provenienz, Review-Inbox
  topologie-impact/            Topologie, Rack, Ausfallsimulation
  verwaltung/                  Assets, Lager, Inventur, Dokumente, Verträge
  betrieb/                     Tickets, SLAs, Monitoring, Workflows
  compliance/                  Audit, Compliance, Befunde, IGA
  api/                         REST-API, Webhooks, Export, Import
/geraete/                      Unterstützte Geräte und Protokolle
/loesungen/
  isp/                         Für Netzbetreiber
  msp/                         Für Managed-Service-Provider
/sicherheit/                   Sicherheit, Datenschutz, Betriebsmodelle
/preise/                       Pakete, Add-ons, Fragen
/kontakt/                      Kontakt und Demo-Anfrage (Formular)
/kontakt/danke/                Bestätigung (noindex)
/impressum/                    Rechtstext vom Auftraggeber
/datenschutz/                  Rechtstext vom Auftraggeber
/agb/                          Rechtstext vom Auftraggeber
/subprozessoren/               Liste vom Auftraggeber
/barrierefreiheit/             Stand der Barrierefreiheit, Kontakt für Hinweise
/404
/en/…                          englische Fassung, gleiche Struktur (F-08)
```

„30 Tage testen“ ist ein Link zur Registrierung in der App (F-02) und keine
eigene Seite.

| Seite | Zweck | Hauptrolle | Handlungsaufruf | Start |
|---|---|---|---|---|
| Startseite | Produkt in einer Minute verstehen | alle | Testen, Demo | Pflicht |
| Discovery | zeigen, wie der Bestand entsteht | Techniker | Testen | Pflicht |
| Datenqualität | zeigen, warum die Daten stimmen | Techniker | Testen | Pflicht |
| Topologie und Impact | Zusammenhänge und Ausfallfolgen | Netzbetrieb | Testen | Pflicht |
| Verwaltung | Assets, Lager, Inventur, Dokumente | Bestandsverwaltung | Demo | Pflicht |
| Betrieb | Tickets, Monitoring, Workflows | Service-Desk | Demo | Pflicht |
| Compliance | Audit, Befunde, IGA | Compliance | Demo | Pflicht |
| API | Integration bewerten | Techniker | Doku, Testen | Pflicht |
| Geräte | „Wird mein Gerät erkannt?“ | Techniker | Testen | Pflicht |
| Lösungen ISP / MSP | Nutzen je Geschäftsmodell | Entscheider | Demo | Pflicht |
| Sicherheit | Erstprüfung bestehen | Sicherheit, Einkauf | Kontakt | Pflicht |
| Preise | Paket wählen | Entscheider | Testen, Demo | Pflicht |
| Kontakt | Anfrage stellen | Entscheider | Absenden | Pflicht |
| Rechtsseiten | Pflichtangaben | alle | – | Pflicht |
| Englisch | internationale Interessenten | alle | wie oben | offen (F-08) |

Nicht zum Start: Blog, Changelog, Karriere, Partnerseite, Vergleichsseiten,
Newsletter. Jede dieser Seiten bringt eigene Pflichten mit
(`compliance-matrix.md`).

## 3. Navigation

- **Kopfzeile:** Produkt (Menü mit den sieben Produktseiten), Geräte,
  Lösungen, Sicherheit, Preise; rechts „Anmelden“ (Link zur App), „Demo
  anfragen“, „30 Tage testen“.
- **Fußzeile:** alle Seiten, Impressum, Datenschutz, AGB, Subprozessoren,
  Barrierefreiheit, Sprache. Impressum und Datenschutz sind von jeder Seite
  mit einem Klick erreichbar.

## 4. Wege zum Abschluss

| Weg | Wer | Verlauf | Ziel |
|---|---|---|---|
| A Selbst testen | Techniker | Startseite → Discovery oder Geräte → „30 Tage testen“ | Registrierung in der App (Organisation, E-Mail, Name) |
| B Entscheiden | Leitung MSP | Startseite → Lösungen MSP → Preise → „Demo anfragen“ | Formular |
| C Prüfen | Sicherheit, Einkauf | Sicherheit → Subprozessoren → Kontakt | Formular, Thema „Sicherheit und Datenschutz“ |
| D Großer Bestand | Netzbetrieb ISP | Lösungen ISP → Preise (Enterprise) → Kontakt | Formular, Thema „Enterprise“ |

Der Test ist der erste Handlungsaufruf, die Demo der zweite. Auf Seiten für
Entscheider und Prüfer steht die Demo bzw. der Kontakt vorn.

## 5. Startseite

| Nr. | Abschnitt | Aussage | Bild |
|---|---|---|---|
| 1 | Einstieg | „Die CMDB, die Ihr Netz selbst ausliest und jeden Wert belegt.“ Ein Satz dazu, für wen und wie. Zwei Handlungsaufrufe. | S-02 CI-Detail mit Herkunft eines Feldes |
| 2 | So entsteht der Bestand | Ablauf in fünf Schritten: Collector verbinden, Scope bestätigen, auslesen, abgleichen, prüfen. Die Reihenfolge ist die des Onboardings der App. | Schema Collector → Cloud |
| 3 | Daten, denen Sie trauen | Quelle, Zeitpunkt und Grund je Feld; manuell gewinnt; Review-Inbox statt stiller Überschreibung | S-03 Review-Inbox |
| 4 | Zusammenhänge | Topologie aus LLDP, Rack höheneinheitengenau, Ausfallsimulation mit Redundanz | S-04, S-05, S-06 |
| 5 | Für mehrere Kunden | Organisation → Kunde → Standort; getrennt bis in die Datenbank; Rollen mit Geltungsbereich | S-18 Arbeitsbereich |
| 6 | Auf demselben Bestand | Assets, Lager, Inventur, Tickets, Monitoring, Workflows, Compliance als Verzeichnis mit je einem Satz, keine Kartenreihe | – |
| 7 | Für Maschinen | REST-API, Webhooks, Export, mit einem echten Aufrufbeispiel | Code-Beispiel |
| 8 | Sicherheit in Kürze | nur ausgehende Verbindung, EU, verschlüsselte Zugangsdaten, Audit-Kette; Link zur Sicherheitsseite | Schema Verbindungsrichtung |
| 9 | Geräte | Auszug der erkannten Hersteller als Textliste; Link zur Geräteseite | – |
| 10 | Pakete | vier Pakete in einer Zeile je Paket; Link zur Preisseite | – |
| 11 | Abschluss | Test: 30 Tage, Standard-Umfang, 100 CIs. Daneben Demo. | – |

## 6. Übrige Seiten in Stichpunkten

- **Discovery:** Protokolle (ICMP, ARP, SNMP v2c/v3, SSH, Redfish, IPMI, WMI);
  Intervalle (Sweep 24 h, Poll 15 min); Verhalten ohne Verbindung (Puffer bis
  24 h); Auslieferung des Collectors (Docker, OVA, Windows-Installer);
  Zugangsdaten nur im Speicher des Collectors. Bilder S-07, S-08.
- **Datenqualität:** die fünf Identitätsschlüssel in ihrer Reihenfolge; die
  Tabelle der Quellränge; drei Beispiele für Review-Fälle; Zusammenführen und
  Rückgängigmachen. Bilder S-02, S-03.
- **Topologie und Impact:** Kanten mit Herkunft und Verlässlichkeit;
  Stromkette Gerät → PDU → USV → Quelle; Mehrfachausfall für Wartungsfenster;
  Rack mit Vorder- und Rückseite. Bilder S-04, S-05, S-06.
- **Verwaltung:** Asset und CI getrennt und verknüpft; Zuweisung mit
  Unterschrift; Lagerbewegungen; Inventur mit Barcode; Dokumente und
  Verträge; DATEV-Export. Bilder S-09, S-10.
- **Betrieb:** Tickets mit SLA und Geschäftskalender; Monitoring und
  Alarmregeln; Workflows und Formulare; Wartungsfenster. Bilder S-11, S-12.
- **Compliance:** Audit-Protokoll mit Hash-Kette und Prüfwerkzeug;
  Compliance-Regeln und Befunde; Berichte; IGA als Add-on. Bilder S-13, S-14.
- **API:** Grundsätze (OpenAPI, additive Änderungen, Fehlerformat, Limits);
  Webhooks mit Signatur; Export und Import; API-Schlüssel. Bild S-15.
- **Geräte:** Tabelle aus den Klassifikationsprofilen (`collector/profiles`),
  filterbar nach Kategorie; Hinweis, was mit unbekannten Geräten geschieht
  (`generic_device` und Review).
- **Lösungen ISP / MSP:** je drei bis vier Arbeitssituationen aus dem Alltag
  und wie Reticora sie löst; keine erfundenen Fallstudien.
- **Sicherheit:** Betriebsmodelle; Verbindungsrichtung und Ports;
  Mandantentrennung; Verschlüsselung der Zugangsdaten; Identität und MFA;
  Audit; Datenschutz (PII-Tresor, Löschung); Datenstandort;
  Subprozessoren; Meldeweg für Sicherheitslücken.
- **Preise:** Paketmatrix aus einer Datendatei; Add-ons; Test; Fragen (Was
  zählt als CI? Was passiert über dem Limit? Was passiert nach Ablauf?).
  Antworten stehen in `README.md` §14.
- **Kontakt:** Formular (§9), daneben direkte Kontaktdaten (F-01).

## 7. Claim-Register

Status „Beleg“ nennt die Quelle im Repository. „Vor Go-live“ nennt, was
zusätzlich geklärt sein muss. Kein Eintrag gilt als geprüft, bevor eine
benannte Person ihn abgehakt hat (F-14).

| ID | Aussage | Beleg | Vor Go-live |
|---|---|---|---|
| C-01 | Automatische Erfassung über SNMP v2c/v3, SSH, Redfish, IPMI/WMI, NAS- und Strom-Plugins | `README.md` §5.1 | Stand der Auslieferung |
| C-02 | Collector baut nur ausgehende Verbindungen auf (443, 4222) | `README.md` §2.1, `docs/betrieb/firewall-matrix.md` | – |
| C-03 | Herkunft je Feld: Quelle, Wert, Zeitpunkt, Auswahlgrund | `README.md` §5.3 | – |
| C-04 | Manuelle Werte werden nie automatisch überschrieben | `README.md` §1.1 | – |
| C-05 | Kein automatisches Zusammenführen allein über den Hostnamen | `README.md` §5.3 | – |
| C-06 | Mandantentrennung in der Datenbank (Row-Level Security) | `README.md` §4, `docs/decisions/0001-timescale-rls.md` | – |
| C-07 | Daten, Backups, Logs und Suchindex in der EU | `README.md` §3 | **Region und Anbieter vom Betreiber bestätigen (F-10)** |
| C-08 | Liste erkannter Hersteller und Geräteklassen | `README.md` §5.4, `collector/profiles` | mit Profilstand abgleichen |
| C-09 | Ausfallsimulation mit Redundanz und Mehrfachausfall | `README.md` §7 | – |
| C-10 | Rack-Ansicht höheneinheitengenau | `README.md` §9 | – |
| C-11 | REST-API nach OpenAPI, signierte Webhooks, Export | `README.md` §8 | öffentliche Doku vorhanden? (F-15) |
| C-12 | Audit-Protokoll mit Hash-Kette, prüfbar | `README.md` §12 | – |
| C-13 | Zugangsdaten verschlüsselt (AES-256-GCM), Klartext nur im Speicher des Collectors | `README.md` §5.6 | – |
| C-14 | Personenbezogene Daten in einem eigenen Tresor; Löschung durch Vernichten des Schlüssels | `README.md` §12 | – |
| C-15 | Signierte Release-Artefakte und SBOM | `README.md` §3, §15 | – |
| C-16 | Externer Penetrationstest | nur Erklärung des Auftraggebers, kein Bericht im Repository (`docs/security/pentest-g1.md`) | **nur mit Nachweis nennen (F-10)** |
| C-17 | Verfügbarkeit, RPO, RTO | Zielwerte in `README.md` §15 | **nur als vertragliche Zusage nennen (F-10)** |
| C-18 | Antwortzeiten und Durchsatz | Zielwerte, Lastschwellen G2 vorläufig (`docs/acceptance/lasttest-g2.md`) | **nicht nennen ohne Freigabe** |
| C-19 | Berichte für ISO 27001, NIS2, KRITIS | `README.md` §12 | Formulierung: Berichtsfunktion, keine Zertifizierung des Anbieters, keine Garantie der Konformität des Kunden |
| C-20 | KI-Add-on: Verarbeitung in der EU oder selbst betrieben, kein Training mit Kundendaten, Einschalten je Organisation | `README.md` §13 | Anbieter und Region bestätigen |
| C-21 | Paketumfang, Limits, Preise | `README.md` §14, dort „noch zu bestätigen“ | **F-04** |
| C-22 | Test: 30 Tage, Standard-Umfang, 100 CIs | `README.md` §14, Katalog ENT-07 | – |
| C-23 | Barrierefreiheit der App | Ziel WCAG 2.2 AA (`frontend/DESIGN.md` §7) | als Ziel formulieren, nicht als Konformität |
| C-24 | Funktionen späterer Produktphasen (Monitoring-Alarme, Workflows, Compliance-Befunde, Endpoint-Agent, SAML/SCIM, Reseller, RFID, GIS, Mobil) | `README.md` §14, §17 | **gegen Launch-Umfang prüfen (F-03)** |
| C-25 | Dedicated Instance und Air-Gapped-Betrieb | `docs/betrieb/referenzmodell.md` | Angebot bestätigen |

## 8. Screenshot-Platzhalter

Alle Bilder entstehen später in der Demo-Organisation bzw. im
Simulationsmodus der App (`README.md` §16: 30 deterministische Geräte), helles
Thema, 1440 × 900 px in doppelter Auflösung, deutsche Oberfläche. Keine
Kundendaten.

| ID | Motiv | Seite der App | Verwendet auf |
|---|---|---|---|
| S-01 | CI-Liste mit Filter | `CIListPage` | Discovery |
| S-02 | CI-Detail mit Herkunft eines Feldes | `CIDetailPage` | Startseite, Datenqualität |
| S-03 | Review-Inbox mit Vergleichsansicht | `ReviewInboxPage` | Startseite, Datenqualität |
| S-04 | Topologie | `TopologyPage` | Startseite, Topologie |
| S-05 | Rack, Vorderseite | `RackPage` | Startseite, Topologie |
| S-06 | Ausfallsimulation | `TopologyPage` (Impact) | Startseite, Topologie |
| S-07 | Discovery: Scopes und Collector-Status | `DiscoveryPage` | Discovery |
| S-08 | Erste Schritte | `OnboardingPage` | Discovery |
| S-09 | Asset mit Zuweisung | `AssetDetailPage` | Verwaltung |
| S-10 | Inventur | `StocktakeDetailPage` | Verwaltung |
| S-11 | Ticket mit SLA | `TicketDetailPage` | Betrieb |
| S-12 | Monitoring | `MonitoringPage` | Betrieb |
| S-13 | Compliance und Befunde | `CompliancePage`, `FindingsPage` | Compliance |
| S-14 | Audit-Protokoll | `AuditPage` | Compliance, Sicherheit |
| S-15 | Webhooks und API-Schlüssel | `WebhooksPage` | API |
| S-16 | Dashboard | `DashboardPage` | Lösungen |
| S-17 | Berechtigungen | `PermissionsPage` | Sicherheit |
| S-18 | Arbeitsbereich Organisation → Kunde → Standort | Kopfzeile der App, `ClientsPage` | Startseite, Lösungen MSP |

## 9. Formular „Kontakt und Demo“

| Feld | Pflicht | Zweck |
|---|---|---|
| Name | ja | Anrede in der Antwort |
| Geschäftliche E-Mail-Adresse | ja | Antwort |
| Unternehmen | ja | Zuordnung, B2B-Bezug |
| Anliegen (Demo, Enterprise, Sicherheit und Datenschutz, Sonstiges) | ja | Zuteilung |
| Nachricht | nein | Inhalt der Anfrage |

- Kein Telefonfeld, keine Positionsbezeichnung, keine Größenabfrage: nicht
  erforderlich, um zu antworten.
- Kein Newsletter-Häkchen (F-07).
- Unter dem Formular ein kurzer Hinweis zur Verarbeitung mit Link zur
  Datenschutzerklärung (Text vom Auftraggeber).
- Das Formular sendet ohne JavaScript. Fehler stehen am Feld und in einer
  Zusammenfassung.
- Unsichtbares Köderfeld und Zeitfalle gegen Spam; kein Captcha eines
  Dritten.

## 10. Sprache und Auffindbarkeit

- Deutsch ist Standard, Englisch unter `/en/` mit `hreflang` in beide
  Richtungen. Inhalte werden zuerst deutsch freigegeben, dann übersetzt.
- Je Seite: eindeutiger Titel, Beschreibung, kanonische URL, Vorschaubild
  ohne Drittdienst, `sitemap.xml`, `robots.txt`.
- Suchbegriffe, um die die Seiten gebaut sind: CMDB für ISP, CMDB für MSP,
  Netzwerk-Discovery, Netzwerkdokumentation automatisch, Rack-Dokumentation,
  Impact-Analyse, CMDB EU-Hosting.
- Strukturierte Daten: Organisation und Produkt; Preise erst nach F-04.
