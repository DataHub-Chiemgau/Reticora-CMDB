# Projektbrief – Website für Reticora CMDB

| | |
|---|---|
| Stand | 2026-11-12 |
| Phase | 1–3: Anforderungen, Informationsarchitektur, Art Directions |
| Status | **Entwurf – Freigabe durch den Auftraggeber ausstehend** |
| Geltung | Lebendes Dokument. Jede Entscheidung wird hier oder im verlinkten Dokument nachgetragen. |

Bis zur Design-Freigabe (Phase 4) entsteht kein Website-Code. Pfade in den
Website-Dokumenten sind relativ zu `website/`; Verweise auf das Produkt nennen
den Pfad ab Repository-Wurzel.

## 1. Auftrag

Vom Auftraggeber vorgegeben (2026-11-12):

- Eine Marketing-Website für Reticora CMDB, B2B, Deutschland und EU.
- Lauffähig auf einem **Hetzner Webhosting L** (Shared Hosting).
- Stil **„Technical Precision“**, passend zur tatsächlichen Software.
- **Platzhalter** an allen Stellen, an denen Screenshots stehen sollen.
- Funktionen werden **als fertig dargestellt**, auch wenn sie es heute noch
  nicht sind: Die Website geht erst nach dem Produkt-Launch online.
- Technik: Astro mit TypeScript, Tailwind CSS mit eigenen Design-Tokens,
  statische Generierung, wenig Client-JavaScript, lokale Schriften, getrenntes
  Backend für Lead-Erfassung, Playwright und axe-core.
- Vorgehen in acht Phasen mit Freigaben; drei Art Directions vor der
  Umsetzung; Compliance-Matrix und Vendor-Register werden gepflegt.

Annahmen von Claude (bitte widersprechen, wenn falsch):

- Die Website liegt als eigenes Teilprojekt in `website/` in diesem
  Repository und berührt Produktcode und Produkt-CI nicht.
- „Technical Precision“ ist die Stilklammer. Die drei Art Directions sind drei
  verschiedene Auslegungen dieser Klammer, keine drei beliebigen Stile.
- Hauptsprache ist Deutsch in der Sie-Form, wie in der App
  (`frontend/src/i18n/de-DE.json`: 178 Texte mit „Sie“, 8 mit „du“).

## 2. Produkt in Kürze

Alle Angaben stammen aus dem Repository; die Quelle steht dabei.

| Thema | Fakt | Quelle |
|---|---|---|
| Produkt | Mandantenfähige SaaS-CMDB mit EU-Hosting; ein Collector im Kundennetz liest Netzwerk, Server, Strom und Storage automatisch aus | `README.md` §1 |
| Zielgruppe | ISPs und auf Netzwerke spezialisierte MSPs | `README.md` §1, `frontend/DESIGN.md` §1 |
| Rollen in der App | Techniker, Service-Desk, Bestandsverwaltung, Compliance/Administration | `frontend/DESIGN.md` §1 |
| Betriebsmodelle | SaaS (EU) mit On-Prem-Collector; Dedicated Instance (Enterprise); Air-Gapped optional | `docs/betrieb/referenzmodell.md` |
| Verbindung | Collector baut nur ausgehende Verbindungen auf (HTTPS 443, NATS-mTLS 4222) | `README.md` §2.1 |
| Kernversprechen | Jedes Feld ist erklärbar (Quelle, Wert, Zeitpunkt, Auswahlgrund); manuelle Werte werden nie überschrieben; unklare Fälle landen in der Review-Inbox | `README.md` §1.1, §5.3 |
| Pakete | Essential, Standard, Pro, Enterprise; Add-ons IGA und KI | `README.md` §14 |
| Test | Self-Signup, 30 Tage Standard-Umfang mit 100 CIs, danach Essential-Limits | `README.md` §14, Katalog ENT-07 |
| Registrierung | Felder: Organisation, E-Mail, Name; Wegwerf-Adressen werden abgelehnt | `frontend/src/i18n/de-DE.json` (`onboarding.signup.*`) |
| Marke | Bildmarke Fadenkreuz („Reticle“), Wortmarke „Reticora“, Akzent Petrol, Neutral Graphit, Schrift Geist und Geist Mono | `frontend/src/components/shell/Brand.tsx`, `frontend/DESIGN.md` §4 |
| Oberfläche | Deutsch und Englisch, hell und dunkel, Ziel WCAG 2.2 AA | `frontend/DESIGN.md` §7 |

## 3. Zielgruppe

| Rolle | Was sie auf der Website sucht | Was sie überzeugt |
|---|---|---|
| **Netzwerktechniker / Engineer** (nutzt das Produkt täglich) | Liest es meine Geräte aus? Wie kommt der Collector ins Netz? Kann ich den Daten trauen? | Geräteliste, Protokolle, Ablauf der Discovery, Provenienz je Feld, API |
| **Technische Leitung / Geschäftsführung eines MSP** (entscheidet) | Trennt es meine Kunden sauber? Was kostet es? Wie schnell bin ich produktiv? | Mandantenmodell, Preise, Test ohne Vertriebsgespräch |
| **Leitung Netzbetrieb eines ISP** (entscheidet, größerer Bestand) | Skaliert es? Impact bei Ausfällen? Dedicated Instance? | Topologie und Impact, Enterprise-Umfang, direkter Kontakt |
| **Informationssicherheit / Datenschutz / Einkauf** (prüft) | Wo liegen die Daten? Wer verarbeitet sie? Wie wird isoliert und protokolliert? | Sicherheitsseite, Subprozessoren, AVV, Audit-Kette |
| **Service-Desk und Bestandsverwaltung** (nutzen Module) | Tickets, SLAs, Assets, Lager, Inventur | Modulseiten mit echten Abläufen |

## 4. Ziele und Erfolgskriterien

1. Ein Techniker versteht in unter einer Minute, was Reticora ausliest und
   warum die Daten belastbar sind.
2. Zwei klare Wege zum Kontakt: **„30 Tage testen“** (Self-Signup in der App)
   und **„Demo anfragen“** (Formular).
3. Eine Sicherheits- und Datenschutzseite, die eine Erstprüfung durch
   Informationssicherheit und Einkauf ohne Rückfrage übersteht.
4. Keine Aussage ohne Beleg: Jede Produktaussage steht im Claim-Register
   (`content-plan.md` §7) mit Quelle.
5. Die Seite lädt ohne Anfragen an Dritte, ohne Cookies und ohne
   Einwilligungsbanner.

Messbar in den Quality Gates (Phase 7): Budgets aus §7, axe ohne Befunde,
Netzwerktest mit null Drittanfragen, alle Claims geprüft.

## 5. Rahmenbedingungen

- **Hosting:** Hetzner Webhosting L. Die Architektur setzt nur voraus, was
  Shared Hosting sicher bietet: Apache mit `.htaccess`, PHP 8, MariaDB,
  Postfach mit SMTP, Cronjobs, TLS-Zertifikat, Upload per SFTP/FTPS. Es wird
  **kein Node.js auf dem Server** gebraucht. Ob Tarif L SSH bietet, ließ sich
  aus dieser Sitzung nicht prüfen (`www.hetzner.com` ist im Netzwerk der
  Sitzung nicht freigegeben, Drittquellen widersprechen sich); der Deploy
  funktioniert mit und ohne SSH (F-09).
- **Launch-Reihenfolge:** erst Produkt, dann Website. Inhalte stehen im
  Präsens, ohne Phasen- oder Roadmap-Hinweise. Vor dem Go-live wird jede
  Aussage gegen den tatsächlich ausgelieferten Stand geprüft (UWG §5, siehe
  `compliance-matrix.md` W-01).
- **Screenshots:** Platzhalter mit fester ID, Seitenverhältnis und
  Bildunterschrift; die echten Bilder kommen später aus der Demo-Organisation
  bzw. dem Simulationsmodus der App, nie aus Kundendaten.
- **Rechtstexte:** Impressum, Datenschutzerklärung, AGB und AVV liefert der
  Auftraggeber bzw. seine Rechtsberatung. Claude erfindet keine Rechtstexte
  und erklärt die Website nicht für rechtskonform.

## 6. Vorgeschlagene Architektur

```
Browser ──HTTPS──▶ Hetzner Webhosting L (Apache)
                    ├── /                statische Dateien aus `astro build`
                    ├── /.htaccess       HTTPS, Security-Header, CSP, Caching, 404
                    └── /api/lead.php    PHP-8-Endpunkt (einziger dynamischer Pfad)
                           ├── prüft Eingaben, Honeypot, Zeitfalle, Rate-Limit
                           ├── schreibt in MariaDB (Lead, befristet)
                           └── sendet per SMTP an das Vertriebspostfach
                    Cronjob: löscht abgelaufene Leads und Rate-Limit-Einträge

CTA „30 Tage testen“ ──Link──▶ Self-Signup der Reticora-App (eigene Domain, F-02)
```

| Baustein | Wahl | Begründung |
|---|---|---|
| Generator | Astro, TypeScript strict, Ausgabe `static` | reines HTML, läuft auf jedem Webspace |
| Styles | Tailwind CSS mit Tokens als CSS-Variablen (`src/styles/tokens.css`) | dieselbe Token-Logik wie die App (`canvas`, `panel`, `line`, `fg`, `accent`) |
| Inhalte | Astro Content Collections (Markdown) plus typisierte Datendateien für Preise, Geräteprofile, Claims, Screenshot-Manifest | Inhalte ohne Code-Änderung pflegbar; Preise und Claims an genau einer Stelle |
| Sprachen | Astro-i18n-Routing: Deutsch ohne Präfix, Englisch unter `/en/` | passend zu DE/EN der App |
| Client-JS | Standard: keines. Inseln nur für mobile Navigation, Formular-Komfort und höchstens eine Produktdemo | Budget §7 |
| Schriften | selbst gehostet (WOFF2, Teilmenge Latin), `font-display: swap` | keine Drittanfrage, DSGVO |
| Bilder | `astro:assets` (AVIF/WebP, feste Maße); Komponente `ScreenshotSlot` mit Manifest | kein Layout-Sprung; Austausch der Platzhalter ohne Layout-Arbeit |
| Lead-Backend | PHP 8 ohne Framework in `backend/`, Konfiguration außerhalb des Webroots | läuft nativ auf dem Webhosting; getrennt deploybar |
| Spam-Schutz | Honeypot, Zeitfalle, Rate-Limit; bei Bedarf selbst gehostetes ALTCHA (wie im Signup der App) | kein Dritt-Captcha |
| Tests | Playwright (Responsive, Formulare), `@axe-core/playwright`, Netzwerktest auf Drittanfragen und Cookies, Lighthouse-Budget, Link- und HTML-Prüfung | Quality Gates Phase 7 |
| CI | eigener Workflow mit Pfadfilter `website/**` | Produkt-CI bleibt unberührt |
| Deploy | manuell freigegebener Workflow; Upload per rsync/SSH oder SFTP; Zugangsdaten nur als geschützte Secrets | Phase 8 verlangt menschliche Freigabe |

Geplante Struktur:

```
website/
├── docs/        diese Dokumente
├── src/         pages, layouts, components, content, data, styles
├── public/      .htaccess, Schriften, Favicon, security.txt
├── backend/     PHP-Lead-Endpunkt, Migration, Cron-Skript
└── tests/       Playwright: responsive, a11y, privacy, forms
```

## 7. Qualitätsziele (Budgets)

| Ziel | Wert |
|---|---|
| Anfragen an Dritte zur Laufzeit | 0 |
| Cookies und Web Storage ohne Nutzeraktion | 0 |
| JavaScript je Seite (komprimiert) | ≤ 20 KB, Startseite mit Demo ≤ 35 KB |
| HTML + CSS + JS + Schriften, ohne Bilder (komprimiert) | ≤ 200 KB |
| LCP / CLS / INP (Labor, Mittelklasse-Mobilgerät, 4G) | ≤ 2,0 s / ≤ 0,05 / ≤ 200 ms |
| Lighthouse Performance, Accessibility, Best Practices, SEO | je ≥ 95 |
| Barrierefreiheit | WCAG 2.2 AA als Ziel; axe ohne Befunde plus manuelle Prüfung |
| Funktion ohne JavaScript | alle Inhalte lesbar, Formular sendet |

## 8. Fehlende Geschäftsfakten

Ohne diese Angaben lassen sich die genannten Teile nicht fertigstellen. Bis
zur Klärung gilt die Annahme in der dritten Spalte.

| Nr. | Fehlender Fakt | Annahme bis zur Klärung | Blockiert |
|---|---|---|---|
| F-01 | **Anbieter:** Firma, Rechtsform, Anschrift, Vertretungsberechtigte, Register und Nummer, USt-IdNr., E-Mail, Telefon. Im Repository steht nur der GitHub-Name „DataHub-Chiemgau“. | Platzhalter im Impressum | Go-live (DDG §5) |
| F-02 | **Domains:** Website, App (Signup, Login), Dokumentation, Statusseite. Im Repository kommen `reticora.io` (Fehler-URIs) und `dev.reticora.cloud` (Entwicklung) vor. | Platzhalter-URLs in einer Konfigurationsdatei | CTA-Ziele, Canonical, Deploy |
| F-03 | **Launch-Umfang:** Welche Pakete und Module sind beim Go-live der Website buchbar? | gesamter Paketumfang aus `README.md` §14 wird als verfügbar dargestellt | Claim-Prüfung vor Go-live |
| F-04 | **Preise:** `README.md` §14 nennt Beta- und Endpreise und vermerkt „noch zu bestätigen“. Welche Preise zeigt die Website, bedeutet „19 €–190 €“ Monat und Jahr, netto? Mindestlaufzeit, Zahlungsweise, Zusatzkosten je CI und Monitoring-Service? | Preisseite mit Platzhaltern, Struktur nach README | Preisseite |
| F-05 | **Ausschließlich B2B?** Verkauft Reticora nur an Unternehmer (§14 BGB), und wie wird das im Signup sichergestellt? | ja, ausschließlich B2B | BFSG, PAngV, §§312j/k BGB |
| F-06 | **Vertriebsweg:** Wer beantwortet Anfragen, an welches Postfach, in welcher Frist? CRM vorhanden? | ein Postfach bei Hetzner, kein CRM | Formular, Vendor-Register |
| F-07 | **Formulare:** nur Kontakt/Demo, oder auch Newsletter, Partneranfrage, Rückruf? | nur Kontakt/Demo, kein Newsletter | Umfang Phase 6, UWG §7 |
| F-08 | **Sprachen zum Start:** nur Deutsch oder Deutsch und Englisch? | Deutsch zuerst, Englisch technisch vorbereitet | Umfang Phase 6 |
| F-09 | **Hosting-Konto:** PHP-Version, SSH ja/nein, `mod_headers`, Log-Anonymisierung, AV-Vertrag mit Hetzner abgeschlossen? | SFTP-Deploy, AVV offen | Deploy, DSGVO Art. 28 |
| F-10 | **Belege für Sicherheitsaussagen:** Pentest (Dienstleister, Datum, darf genannt werden?), Hosting-Region und Anbieter der SaaS, Subprozessorenliste (`docs/legal/subprozessoren.md` ist leer), zugesagte Verfügbarkeit (SLA) | keine Nennung von Pentest, SLA-Zahlen und Zertifikaten | Sicherheitsseite |
| F-11 | **Rechtstexte:** Wer liefert Impressum, Datenschutzerklärung, AGB, AVV, TOM? | Platzhalterseiten mit klarer Kennzeichnung | Go-live |
| F-12 | **Markenmaterial:** Logo-Dateien, Markenregistrierung „Reticora“, Vorgaben über `frontend/DESIGN.md` hinaus? | Bildmarke aus `Brand.tsx`, keine weiteren Vorgaben | Design-System |
| F-13 | **Reichweitenmessung:** keine, Server-Logs, oder selbst gehostet (z. B. Matomo ohne Cookies)? | keine Messung zum Start | TDDDG §25, Vendor-Register |
| F-14 | **Freigebende Personen:** je eine Person für Design, Inhalt, Recht, Datenschutz, Go-live | „Auftraggeber“ für alles | alle Freigaben |
| F-15 | **Öffentliche Dokumentation:** Gibt es zum Start eine Doku, API-Referenz, Statusseite, Changelog? | Links nur, wenn die Ziele existieren | Navigation, Footer |
| F-16 | **Referenzen:** Gibt es Kunden, die genannt werden dürfen? | keine Logos, keine Zitate | Startseite |
| F-17 | **Datenschutzbeauftragte Person:** benannt oder nicht erforderlich (§38 BDSG)? | offen | Datenschutzerklärung |

## 9. Phasen und Freigaben

| Phase | Inhalt | Stand |
|---|---|---|
| 1 | Anforderungen, Zielgruppe, Positionierung | Entwurf liegt vor (dieses Dokument, `brand-strategy.md`) |
| 2 | Informationsarchitektur, Inhaltsstrategie | Entwurf liegt vor (`content-plan.md`) |
| 3 | Drei Art Directions | Entwurf liegt vor (`brand-strategy.md` §6, `design-system.md`) |
| 4 | **Freigabe Design-System** | **wartet auf Auftraggeber** |
| 5 | Startseite | gesperrt bis Phase 4 |
| 6 | Übrige Seiten, Formulare | gesperrt |
| 7 | QA, Sicherheit, Datenschutz, Barrierefreiheit | gesperrt |
| 8 | Menschliche Freigabe, Deploy | gesperrt |

## 10. Entscheidungen

| Nr. | Datum | Entscheidung | Wer |
|---|---|---|---|
| W-E01 | 2026-11-12 | Website als Teilprojekt `website/` im Produkt-Repository | Annahme Claude, zu bestätigen |
| W-E02 | 2026-11-12 | Funktionen werden als verfügbar dargestellt; Prüfung der Claims vor Go-live | Auftraggeber |
| W-E03 | 2026-11-12 | Lead-Backend in PHP auf demselben Webhosting, kein Node.js zur Laufzeit | Vorschlag Claude, zu bestätigen |
