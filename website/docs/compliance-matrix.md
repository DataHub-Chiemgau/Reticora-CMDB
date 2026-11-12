# Compliance-Matrix – Website für Reticora CMDB

| | |
|---|---|
| Stand der Sichtung | 2026-11-12 |
| Status | **Erste Risikoeinschätzung – nicht rechtlich geprüft, nichts freigegeben** |
| Gesichtet von | Claude (KI-Assistent), ohne Zugriff auf aktuelle Gesetzesdatenbanken in dieser Sitzung |

**Dieses Dokument ist keine Rechtsberatung.** Es sammelt die Anforderungen,
die für eine B2B-Website in Deutschland und der EU typischerweise zu prüfen
sind, und ordnet sie dem Projekt zu. Fundstellen und Einschätzungen muss eine
fachkundige Person prüfen. Die Website gilt erst als geprüft, wenn in jeder
Zeile eine benannte Person die Freigabe eingetragen hat. Rechtstexte liefert
der Auftraggeber; hier werden keine formuliert.

Spalte „Prüfer“: Bis F-14 (`project-brief.md` §8) geklärt ist, steht dort die
Rolle. Spalte „Freigabe“: `offen`, `in Prüfung`, `freigegeben (Name, Datum)`.

## 1. Erste Risikoeinschätzung

| Rang | Risiko | Warum | Maßnahme |
|---|---|---|---|
| 1 | **Irreführende Werbung** (UWG §5): Funktionen werden als verfügbar dargestellt, bevor sie es sind; Zielwerte lesen sich wie Zusagen | Vorgabe „als fertig darstellen“; Sicherheits- und Leistungsangaben im Repository sind teils Ziele oder nur erklärt | Claim-Register mit Pflichtprüfung vor Go-live (W-01, W-02) |
| 2 | **Fehlende Anbieterangaben und Rechtstexte** | Firma und Anschrift liegen vor (W-E05); Geschäftsführung, Register, USt-IdNr. und Kontaktangaben fehlen (F-01); Impressums- und Datenschutzfehler sind abmahnfähig | Go-live-Sperre, bis die Angaben und Texte vom Auftraggeber vorliegen (I-01, D-02) |
| 3 | **B2B-Beschränkung nicht belastbar** | Der Auftraggeber hat ausschließlich B2B bestätigt (W-E08). BFSG, PAngV und §§312j/k BGB entfallen aber nur, wenn die Beschränkung für Besucher erkennbar ist und in der Registrierung durchgesetzt wird (F-05) | deutlicher B2B-Hinweis, Nettopreise mit Zusatz, Prüfung des Signups der App (B-01 bis B-04) |
| 4 | **Auftragsverarbeitung und Subprozessoren** | AV-Vertrag mit Hetzner ungeklärt (F-09); Subprozessorenliste der SaaS im Repository leer | AVV abschließen, Liste vom Betreiber (D-05, D-10) |
| 5 | **Lead-Daten** | Formular verarbeitet personenbezogene Daten; Speicherdauer, Empfänger und Postfach sind offen (F-06) | Datensparsamkeit, Löschfrist, Verfahren dokumentieren (D-03, D-07, D-08) |
| 6 | **Drittanfragen und Endgerätezugriff** | jede eingebettete Schrift, Karte, Video oder Messung löst Einwilligungs- und Transferfragen aus | null Drittanfragen als Testkriterium (T-01, D-09) |
| 7 | **E-Mail-Werbung** | Werbung per E-Mail braucht auch im B2B die vorherige ausdrückliche Einwilligung | kein Newsletter zum Start; Antworten nur auf Anfragen (W-04) |

## 2. Anbieterkennzeichnung und Medienrecht

| ID | Rechtsquelle (Sichtung) | Anwendbarkeit und Begründung | Umsetzung | Test / Nachweis | Prüfer | Freigabe |
|---|---|---|---|---|---|---|
| I-01 | DDG §5 Abs. 1 (2026-11-12) | **anwendbar:** geschäftsmäßiger, in der Regel entgeltlicher digitaler Dienst | Seite `/impressum/` mit Angaben vom Auftraggeber (F-01); Link in der Fußzeile jeder Seite | Playwright: Link auf jeder Seite, Ziel antwortet mit 200; Sichtprüfung der Angaben | Recht | offen |
| I-02 | DDG §5: „leicht erkennbar, unmittelbar erreichbar, ständig verfügbar“ (2026-11-12) | **anwendbar** | Bezeichnung „Impressum“, ein Klick von jeder Seite, auch mobil und auf der 404-Seite | Playwright je Viewport | Recht | offen |
| I-03 | DDG §6 (2026-11-12) | **anwendbar:** Der Beta-Rabatt wird beworben (W-E07). Kommerzielle Kommunikation und die Bedingungen von Preisnachlässen müssen klar erkennbar und leicht zugänglich sein | Bedingungen des Beta-Rabatts direkt bei den Preisen: Höhe, Dauer, Buchungszeitraum (`content-plan.md` §6.1, F-04) | Sichtprüfung der Preisseite in beiden Sprachen | Recht | offen |
| I-04 | MStV §18 Abs. 2 (2026-11-12) | **zum Start voraussichtlich nicht anwendbar:** kein journalistisch-redaktionelles Angebot; wird relevant mit Blog oder Magazin | kein Blog zum Start; bei Einführung Verantwortliche Person nennen | Sitemap-Prüfung | Recht | offen |
| I-05 | §35a GmbHG; der Anbieter ist eine UG (haftungsbeschränkt) (2026-11-12) | **anwendbar auf E-Mails** des Formulars an Anfragende (Geschäftsbrief) | Pflichtangaben in der Signatur der Bestätigungs-E-Mail (F-01) | Test-E-Mail im Protokoll | Recht | offen |

## 3. Datenschutz

| ID | Rechtsquelle (Sichtung) | Anwendbarkeit und Begründung | Umsetzung | Test / Nachweis | Prüfer | Freigabe |
|---|---|---|---|---|---|---|
| D-01 | DSGVO Art. 5 Abs. 1 lit. c, Art. 25 (2026-11-12) | **anwendbar:** Formular und Server-Logs verarbeiten personenbezogene Daten | Formular mit fünf Feldern (`content-plan.md` §9); keine Messung; keine Profile | Feldliste im Test; Review | Datenschutz | offen |
| D-02 | DSGVO Art. 12–14 (2026-11-12) | **anwendbar** | Seite `/datenschutz/` mit Text vom Auftraggeber; Hinweis am Formular mit Link | Playwright: Link auf jeder Seite und am Formular | Datenschutz, Recht | offen |
| D-03 | DSGVO Art. 6 Abs. 1 lit. b oder f (2026-11-12) | **anwendbar:** Rechtsgrundlage für Anfragen und Logs ist festzulegen | Festlegung durch Auftraggeber; kein Einwilligungs-Häkchen, sofern nicht nötig | Eintrag im Verarbeitungsverzeichnis | Datenschutz | offen |
| D-04 | DSGVO Art. 30 (2026-11-12) | **anwendbar** (Umfang je nach Unternehmensgröße zu prüfen) | Verarbeitungen „Website-Betrieb“ und „Kontaktanfragen“ ins Verzeichnis des Auftraggebers | Auszug | Datenschutz | offen |
| D-05 | DSGVO Art. 28 (2026-11-12) | **anwendbar:** Hetzner verarbeitet im Auftrag (Webserver, Datenbank, Postfach) | AV-Vertrag mit Hetzner abschließen und ablegen (F-09) | Vertragsnachweis | Datenschutz | offen |
| D-06 | DSGVO Art. 32 (2026-11-12) | **anwendbar** | TLS überall, Security-Header, serverseitige Prüfung, Zugangsdaten außerhalb des Webroots, Zugriff auf Leads beschränkt | Header-Test, Sicherheitsreview Phase 7 | Sicherheit | offen |
| D-07 | DSGVO Art. 5 Abs. 1 lit. e, Art. 17 (2026-11-12) | **anwendbar** | Löschfrist für Leads und Rate-Limit-Einträge, Cronjob (`vendor-register.md` §5); Frist legt der Auftraggeber fest | Test des Löschskripts; Protokoll | Datenschutz | offen |
| D-08 | DSGVO Art. 15–21 (2026-11-12) | **anwendbar** | Kontaktweg und internes Verfahren für Auskunft und Löschung | Verfahrensbeschreibung | Datenschutz | offen |
| D-09 | DSGVO Art. 44 ff. (2026-11-12) | **zu vermeiden:** keine Übermittlung in Drittländer zur Laufzeit geplant | keine Dritt-Skripte, Schriften, Karten, Videos, Captchas; Postfach ohne Weiterleitung in Drittländer (F-06) | Netzwerktest T-01; Vendor-Register | Datenschutz | offen |
| D-10 | DSGVO Art. 28 Abs. 2–4, Verweis der Website auf die SaaS (2026-11-12) | **anwendbar auf die Aussage** „Subprozessoren“ und „EU-Hosting“ | Seite `/subprozessoren/` nur mit Liste des Betreibers; `docs/legal/subprozessoren.md` ist derzeit leer | Abgleich mit Betreiberliste | Datenschutz | offen |
| D-11 | DSGVO Art. 37, BDSG §38 (2026-11-12) | **zu klären** (F-17): Benennungspflicht hängt u. a. von der Zahl der Personen ab, die ständig personenbezogene Daten automatisiert verarbeiten | Angabe in der Datenschutzerklärung, falls benannt | Erklärung des Auftraggebers | Datenschutz | offen |
| D-12 | DSGVO Art. 6 Abs. 1 lit. f; Server-Logs (2026-11-12) | **anwendbar:** Zugriffslogs des Hosters enthalten IP-Adressen | Einstellungen zu Anonymisierung und Aufbewahrung im Hetzner-Konto prüfen (F-09) | Bildschirmfoto der Einstellung | Datenschutz | offen |
| D-13 | DSGVO Art. 33, 34 (2026-11-12) | **anwendbar** | Meldeweg bei Datenpannen des Website-Backends im Betriebshandbuch | Verfahrensbeschreibung | Datenschutz, Sicherheit | offen |
| D-14 | LG München I, Urteil vom 20.01.2022 – 3 O 17493/20 (2026-11-12) | **einschlägig als Maßstab:** Einbindung von Schriften über Server Dritter überträgt die IP-Adresse | Schriften selbst gehostet | Netzwerktest T-01 | Datenschutz | offen |

## 4. Endgerätezugriff und Einwilligung

| ID | Rechtsquelle (Sichtung) | Anwendbarkeit und Begründung | Umsetzung | Test / Nachweis | Prüfer | Freigabe |
|---|---|---|---|---|---|---|
| T-01 | TDDDG §25 Abs. 1 (2026-11-12) | **anwendbar dem Grunde nach;** Einwilligung nötig für jedes nicht unbedingt erforderliche Speichern oder Auslesen im Endgerät | zum Start: keine Cookies, kein Web Storage, keine Messung, keine Drittinhalte. Dann ist kein Einwilligungsbanner nötig. | Playwright-Netzwerktest: null fremde Hosts, null Cookies, leerer Web Storage auf jeder Seite | Datenschutz | offen |
| T-02 | TDDDG §25 Abs. 2 Nr. 2 (2026-11-12) | **nur falls** später eine vom Nutzer gewählte Einstellung gespeichert wird (Sprache, Darstellung) | zum Start nicht speichern: Sprache über URL, Darstellung über Systemeinstellung | Test wie T-01 | Datenschutz | offen |
| T-03 | TDDDG §25, DSGVO Art. 7 (2026-11-12) | **nur falls** Reichweitenmessung eingeführt wird (F-13) | vor Einführung: Verfahren wählen, Zeile neu bewerten, Vendor-Register ergänzen | – | Datenschutz | offen |

## 5. Wettbewerbs- und Werberecht

| ID | Rechtsquelle (Sichtung) | Anwendbarkeit und Begründung | Umsetzung | Test / Nachweis | Prüfer | Freigabe |
|---|---|---|---|---|---|---|
| W-01 | UWG §5, §5a (2026-11-12) | **anwendbar, auch im B2B:** Angaben über Merkmale der Leistung müssen zutreffen | Claim-Register (`content-plan.md` §7); Go-live-Sperre, bis jeder Eintrag gegen den ausgelieferten Stand geprüft ist | abgehaktes Register mit Name und Datum | Inhalt, Recht | offen |
| W-02 | UWG §5 (2026-11-12) | **anwendbar auf Sicherheits-, Verfügbarkeits- und Leistungsangaben** sowie auf Hinweise zu ISO 27001, NIS2, KRITIS | keine Nennung von Pentest, SLA-Werten, Zertifikaten ohne Nachweis (C-16 bis C-19); Berichtsfunktion nicht als Zertifizierung darstellen | Review der Sicherheitsseite | Recht, Sicherheit | offen |
| W-03 | UWG §6 (2026-11-12) | **zum Start nicht einschlägig:** keine vergleichende Werbung geplant | keine Wettbewerbernamen, keine Vergleichstabellen | Textsuche im Build | Inhalt | offen |
| W-04 | UWG §7 Abs. 2 Nr. 2, Abs. 3 (2026-11-12) | **anwendbar auf jede Werbe-E-Mail,** auch an Unternehmen | kein Newsletter; Formular dient nur der Antwort auf die Anfrage; keine Übernahme in Verteiler | Prozessbeschreibung; Formular ohne Werbe-Einwilligung | Recht | offen |
| W-05 | UWG §3a i. V. m. den Pflichten aus I-01, D-02 (2026-11-12) | **anwendbar:** Verstöße gegen Marktverhaltensregeln sind abmahnfähig | siehe I-01, D-02 | – | Recht | offen |
| W-06 | MarkenG §23 (2026-11-12) | **anwendbar:** Herstellernamen in der Geräteliste | Nennung nur als Text zur Beschreibung der Kompatibilität, keine Logos, kein Anschein einer Partnerschaft | Review der Geräteseite | Recht | offen |
| W-07 | UrhG; Lizenzen von Schriften, Symbolen, Bildern (2026-11-12) | **anwendbar** | Lizenzliste im Vendor-Register; nur eigene Zeichnungen und eigene Screenshots | Lizenzprüfung im Build | Inhalt | offen |

## 6. B2B-Abgrenzung und Vertragsschluss

| ID | Rechtsquelle (Sichtung) | Anwendbarkeit und Begründung | Umsetzung | Test / Nachweis | Prüfer | Freigabe |
|---|---|---|---|---|---|---|
| B-01 | BFSG §1 Abs. 3 Nr. 5, §2 Nr. 26, §3 Abs. 3 (2026-11-12) | **voraussichtlich nicht anwendbar, wenn** das Angebot sich ausschließlich an Unternehmer richtet: Das Gesetz erfasst Dienstleistungen im elektronischen Geschäftsverkehr im Hinblick auf Verbraucherverträge; zudem Ausnahme für Kleinstunternehmen. Der Auftraggeber hat ausschließlich B2B bestätigt (W-E08); die Beschränkung muss erkennbar und durchgesetzt sein (F-05). | B2B-Hinweis; unabhängig davon WCAG 2.2 AA als Qualitätsziel; keine Aussage „BFSG-konform“ | axe-Tests, manuelle Prüfung, Seite `/barrierefreiheit/` | Recht | offen |
| B-02 | PAngV §1, §3 (2026-11-12) | **voraussichtlich nicht anwendbar bei reinem B2B;** Preisangaben dürfen dennoch nicht irreführen | Preise als Nettopreise mit Zusatz „zzgl. USt.“ und Hinweis „Angebot nur für Unternehmer“ | Sichtprüfung der Preisseite | Recht | offen |
| B-03 | BGB §312i, Art. 246c EGBGB (2026-11-12) | **gilt auch im B2B, aber für den Bestellweg:** Der Vertrag wird in der App geschlossen, nicht auf der Website. Das Formular ist individuelle Kommunikation und keine Bestellung. | Website: AGB abrufbar und speicherbar verlinken; Bestellweg der App gesondert prüfen (außerhalb dieses Projekts) | Link-Test; Hinweis an Produktteam | Recht | offen |
| B-04 | BGB §312j, §312k (2026-11-12) | **nicht anwendbar bei reinem B2B** (Verbraucherverträge). B2B ist bestätigt (W-E08). Die Registrierung der App fragt derzeit weder die gewerbliche Nutzung ab noch lässt sie AGB annehmen (F-20); solange das so ist, stützt sich die Abgrenzung allein auf Hinweise. | B2B-Beschränkung im Signup der App belastbar machen | Prüfung des Signups | Recht | offen |
| B-05 | BGB §§305 ff. (2026-11-12) | **anwendbar:** AGB müssen einbezogen und abrufbar sein | Seite `/agb/` mit Text vom Auftraggeber (F-11) | Link-Test | Recht | offen |
| B-06 | VSBG §36; Verordnung (EU) 524/2013 (2026-11-12) | **voraussichtlich nicht anwendbar:** Verbraucherstreitbeilegung betrifft Verbraucherverträge; die OS-Plattform der EU wurde nach Kenntnis von Claude 2025 eingestellt. **Zu prüfen.** | kein OS-Link, kein Hinweis ohne anwaltliche Vorgabe | – | Recht | offen |

## 7. Sicherheit des Website-Betriebs

Keine Rechtsquelle im engeren Sinn, aber Voraussetzung für D-06.

| ID | Maßstab (Sichtung) | Anwendbarkeit | Umsetzung | Test / Nachweis | Prüfer | Freigabe |
|---|---|---|---|---|---|---|
| S-01 | OWASP ASVS, Abschnitte zu Eingabeprüfung und Ausgabe (2026-11-12) | Lead-Endpunkt | Prüfung aller Felder auf dem Server; Längen; Ausgabe maskiert; vorbereitete SQL-Anweisungen; keine Header-Injektion in E-Mails | PHP-Tests, Playwright mit Angriffsmustern | Sicherheit | offen |
| S-02 | Rate-Limit (2026-11-12) | Lead-Endpunkt | Limit je Herkunft über gesalzenen Hash, Einträge 24 h | Test: Sperre nach n Anfragen | Sicherheit | offen |
| S-03 | Security-Header (2026-11-12) | alle Antworten | HSTS, CSP ohne `unsafe-inline` für Skripte, `X-Content-Type-Options`, `Referrer-Policy`, `Permissions-Policy`, `frame-ancestors` über `.htaccess` (F-09: `mod_headers`) | Header-Test im Playwright-Lauf | Sicherheit | offen |
| S-04 | Geheimnisse (2026-11-12) | Deploy, SMTP, Datenbank | nie im Repository; Konfiguration außerhalb des Webroots; Deploy-Zugang als geschütztes Secret | Secret-Scan im CI | Sicherheit | offen |
| S-05 | Weiterleitungen (2026-11-12) | Formular, Sprachwechsel | keine Ziel-URL aus Nutzereingaben; feste Ziele | Test | Sicherheit | offen |
| S-06 | Abhängigkeiten und Build (2026-11-12) | Build | Lockfile, `npm audit` im CI, Prüfung des Build-Ergebnisses auf fremde Hosts und Quelltext-Karten | CI-Lauf | Sicherheit | offen |
| S-07 | RFC 9116 (2026-11-12) | empfohlen | `/.well-known/security.txt` mit Meldeadresse (F-01) | Abruf-Test | Sicherheit | offen |
| S-08 | E-Mail-Authentifizierung (2026-11-12) | Versanddomain | SPF, DKIM, DMARC für die Absenderdomain | DNS-Prüfung | Sicherheit | offen |

## 8. Weitere Punkte zur Prüfung

| ID | Rechtsquelle (Sichtung) | Einschätzung | Umsetzung | Prüfer | Freigabe |
|---|---|---|---|---|---|
| X-01 | Verordnung (EU) 2024/1689 (KI-Verordnung), Art. 50 (2026-11-12) | **zu prüfen:** betrifft Aussagen zum KI-Add-on und KI-erzeugte Inhalte; Texte der Website werden von Menschen freigegeben | keine KI-erzeugten Bilder von Personen oder Ereignissen; Aussagen zum Add-on nach C-20 | Recht | offen |
| X-02 | Verordnung (EU) 2022/2065 (DSA) (2026-11-12) | **voraussichtlich nicht einschlägig:** keine Nutzerinhalte, kein Vermittlungsdienst | keine Kommentarfunktion | Recht | offen |
| X-03 | Kennzeichnung von Testdaten (2026-11-12) | Screenshots dürfen keine echten Kundendaten zeigen | nur Demo-Organisation und Simulationsmodus | Inhalt, Datenschutz | offen |

## 9. Go-live-Sperren

Die Website geht nicht online, solange eine dieser Zeilen offen ist:
I-01, I-02, D-02, D-05, D-07, T-01, W-01, W-02, B-05, S-01 bis S-04.
