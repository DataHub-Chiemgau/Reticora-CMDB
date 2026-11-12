# Technische Spezifikation – Website für Reticora CMDB

| | |
|---|---|
| Stand | 2026-11-12 |
| Status | **Entwurf zur Freigabe.** Erst nach Freigabe dieses Dokuments und von `design-system.md` entsteht ein Umsetzungsplan, danach Code. |
| Geltung | beschreibt, was gebaut wird und wie es geprüft wird. Inhalte, Gestaltung und Rechtliches stehen in den sechs Dokumenten unter `website/docs/`; diese Spezifikation verweist darauf und wiederholt sie nicht. |

## 1. Ziel und Abgrenzung

Gebaut wird die Marketing-Website `www.reticora.de` in Deutsch und Englisch:
statische Seiten, ein Kontaktformular mit kleinem Server-Teil, lauffähig auf
Hetzner Webhosting L.

Zum Umfang gehören die Seiten aus `content-plan.md` §2, die Bausteine aus
`design-system.md` §5, das Lead-Backend, Tests, CI und ein von Hand
freigegebener Deploy.

Nicht zum Umfang gehören: Änderungen an der App (Signup, Sprache per URL,
B2B-Abfrage; siehe F-20, F-21), Rechtstexte, echte Screenshots, Blog,
Newsletter, Reichweitenmessung, CRM-Anbindung.

## 2. Aufbau im Repository

```
website/
├── docs/                     Dokumente (diese Spezifikation unter specs/)
├── astro.config.ts           Ausgabe static, i18n, Sitemap
├── package.json              Skripte: dev, build, check, test, test:e2e
├── src/
│   ├── styles/tokens.css     Tokens beider Themen (aus design-system.md §3)
│   ├── styles/base.css       Grundstile, Schriften, Fokus
│   ├── layouts/              Leitstand.astro, Dokument.astro
│   ├── components/           ein Ordner je Baustein aus design-system.md §5
│   ├── islands/              impact.ts (Ausfallsimulation), copy.ts, form.ts
│   ├── content/              Seiteninhalte je Sprache (Markdown mit Frontmatter)
│   ├── data/                 site.ts, links.ts, pricing.ts, devices.ts,
│   │                         claims.ts, screenshots.ts, topology.ts
│   ├── i18n/                 de.ts, en.ts (Texte der Bausteine), routes.ts
│   └── pages/                Routen für Deutsch und /en/
├── public/                   .htaccess, fonts/, favicon, robots.txt,
│                             .well-known/security.txt
├── backend/
│   ├── public/api/lead.php   einziger Einstiegspunkt
│   ├── src/                  Validator, RateLimiter, LeadRepository, Mailer
│   ├── config/config.example.php
│   ├── migrations/001_init.sql
│   ├── bin/purge.php         Löschlauf für den Cronjob
│   └── tests/                PHPUnit
└── tests/                    Playwright: pages, a11y, privacy, forms, budget
```

Jede Einheit hat genau eine Aufgabe und eine schmale Schnittstelle:

| Einheit | Aufgabe | hängt ab von |
|---|---|---|
| `data/*.ts` | typisierte Fakten an genau einer Stelle (Preise, Links, Claims, Screenshots, Geräte) | nichts |
| `components/*` | Darstellung eines Bausteins | Tokens, `i18n` |
| `content/*` | Text der Seiten | Bausteine über Namen |
| `islands/impact.ts` | rechnet die Ausfallsimulation im Browser | Markup von `TopologyStage` |
| `backend/src/Validator` | prüft Eingaben, kennt weder Datenbank noch Mail | nichts |
| `backend/src/RateLimiter` | zählt Anfragen je Herkunft | Datenbank |
| `backend/src/LeadRepository` | speichert und löscht Anfragen | Datenbank |
| `backend/src/Mailer` | sendet zwei E-Mails | SMTP |

## 3. Seiten, Sprachen, Adressen

- Deutsch ohne Präfix, Englisch unter `/en/`. Jede Seite gibt es in beiden
  Sprachen; ein Test bricht ab, wenn ein Gegenstück fehlt.
- Englische Adressen sind übersetzt (`/en/pricing/`, `/en/security/`);
  `i18n/routes.ts` ordnet die Paare zu und speist `hreflang` und die
  Sprachwahl.
- Alle Adressen enden mit Schrägstrich. `.htaccess` leitet die Form ohne
  Schrägstrich und `http` dauerhaft um. Keine Umleitung nach
  Browser-Sprache.
- Kanonische Adresse, `hreflang` (de, en, x-default), `sitemap.xml`,
  `robots.txt`; `/kontakt/danke/` und `/en/contact/thanks/` sind `noindex`.
- Ziele außerhalb der Website stehen nur in `data/links.ts`:
  Registrierung `https://www.reticora.cloud/onboarding`, Anmeldung
  `https://www.reticora.cloud/login`, Demo `https://www.demo.reticora.cloud`.
- Die Seiten `/impressum/`, `/datenschutz/`, `/agb/`, `/subprozessoren/`
  nutzen das Layout `Dokument` und zeigen, solange der Text des Anbieters
  fehlt, einen deutlich gekennzeichneten Platzhalter. Der Build für den
  Go-live bricht ab, wenn ein Platzhalter übrig ist.

## 4. Inhalte und Daten

| Datei | Inhalt | Prüfung beim Build |
|---|---|---|
| `data/site.ts` | Firmierung, Anschrift, Kontakt | Pflichtfelder gefüllt (Go-live) |
| `data/pricing.ts` | Tarife, Preise, Beta-Rabatt, Zusatzkosten (`content-plan.md` §6.1) | Beta-Jahrespreis = Jahrespreis × 0,9 |
| `data/claims.ts` | Claim-Register (`content-plan.md` §7) mit Status `offen` oder `geprüft`, Name, Datum | jeder `Beleg` verweist auf einen Eintrag; Go-live nur mit `geprüft` |
| `data/screenshots.ts` | Manifest S-01 bis S-18 mit Motiv, Maß, Datei | Go-live nur ohne offenen Platzhalter |
| `data/devices.ts` | Geräteklassen aus `collector/profiles` | – |
| `data/topology.ts` | Knoten, Beziehungen und Ausgangszustand des Netzschemas | Ergebnis der Rechenregeln gegen die Tabelle in `design-system.md` §6.2 |

Zwei Build-Arten: `build` (Entwicklung, Platzhalter erlaubt, Seite trägt
`noindex`) und `build:release` (bricht bei offenem Claim, Platzhalter oder
fehlender Pflichtangabe ab).

## 5. Darstellung

- Tokens und Bausteine nach `design-system.md`. Tailwind liest die Tokens als
  CSS-Variablen; Bausteine nutzen nur semantische Namen.
- Schriften: zwei WOFF2-Dateien unter `public/fonts/`, auf die genutzten
  Zeichen gekürzt, `font-display: swap`, Archivo vorab geladen. Die
  Lizenztexte liegen daneben.
- JavaScript: Standard keines. Drei Inseln ohne Framework, jede lädt nur auf
  Seiten, die sie brauchen:

| Insel | Aufgabe | ohne JavaScript | Budget |
|---|---|---|---|
| `impact.ts` | Ausfallsimulation | Standbild im Ausgangszustand | 8 KB |
| `copy.ts` | Kopierknopf am Code | Knopf fehlt | 1 KB |
| `form.ts` | Senden ohne Seitenwechsel, Fehler am Feld | normales Absenden, Antwortseite | 3 KB |

- Bilder über `astro:assets` (AVIF und WebP, feste Maße). `ScreenshotSlot`
  liest das Manifest und zeigt Bild oder Platzhalter.
- Kein Inline-Skript und kein Inline-Stil im ausgelieferten HTML, damit die
  Content-Security-Policy ohne `unsafe-inline` auskommt (§8).

## 6. Lead-Backend

### 6.1 Vertrag

`POST /api/lead.php`, `Content-Type: application/x-www-form-urlencoded`.

| Feld | Regel |
|---|---|
| `name` | Pflicht, 1–200 Zeichen |
| `email` | Pflicht, gültige Adresse, höchstens 254 Zeichen |
| `company` | Pflicht, 1–200 Zeichen |
| `topic` | Pflicht, einer von `demo`, `enterprise`, `security`, `other` |
| `message` | freiwillig, höchstens 4000 Zeichen |
| `lang` | `de` oder `en` |
| `website` | Köderfeld, muss leer sein |
| `ts`, `sig` | Zeitstempel der Seite und dessen HMAC; Alter zwischen 3 Sekunden und 2 Stunden |

Steuerzeichen und Zeilenumbrüche in einzeiligen Feldern werden abgelehnt.
Anfragen über 16 KB, mit fremder `Origin` oder anderer Methode werden
abgelehnt.

Antworten:

| Fall | mit `Accept: application/json` | sonst |
|---|---|---|
| angenommen | `200 {"ok":true}` | `303` nach `/kontakt/danke/` bzw. `/en/contact/thanks/` |
| Eingabefehler | `422 {"ok":false,"errors":{feld:code}}` | `303` zurück zum Formular mit Fehlercodes in der Adresse (keine Eingaben in der Adresse) |
| zu viele Anfragen | `429` mit `Retry-After` | Hinweisseite |
| Köderfeld gefüllt oder Zeitfalle | wie „angenommen“, ohne Speichern und Senden | wie „angenommen“ |
| Serverfehler | `500 {"ok":false}` | Hinweisseite mit direkter Kontaktadresse |

Weiterleitungsziele sind fest im Code; keine Adresse stammt aus der Anfrage.

### 6.2 Ablauf

1. Methode, Größe, `Origin` prüfen.
2. Rate-Limit: höchstens 5 Anfragen je Herkunft und Stunde. Gespeichert wird
   ein HMAC der IP-Adresse mit täglich wechselndem Schlüssel, nie die Adresse
   selbst.
3. Eingaben prüfen (`Validator`).
4. Anfrage in MariaDB speichern (`LeadRepository`, vorbereitete Anweisungen).
5. E-Mail an das Vertriebspostfach und Bestätigung an die anfragende Person
   senden (`Mailer`, SMTP mit Anmeldung; Kopfzeilen aus festen Werten;
   Pflichtangaben des Anbieters in der Signatur). Schlägt der Versand fehl,
   bleibt die Anfrage gespeichert und trägt den Vermerk „nicht zugestellt“;
   der Löschlauf meldet solche Einträge.
6. Antwort.

### 6.3 Daten und Löschung

Tabelle `lead` (ID, Zeit, Sprache, die fünf Formularfelder, Zustellstatus)
und Tabelle `rate_limit` (Hash, Zeitfenster, Zähler). `bin/purge.php` läuft
täglich als Cronjob: löscht Leads nach der vom Auftraggeber festgelegten
Frist (Vorschlag 90 Tage) und Rate-Limit-Einträge nach 24 Stunden. Das
Protokoll enthält nur Anzahlen.

### 6.4 Konfiguration und Geheimnisse

`config.php` liegt außerhalb des Webroots und enthält Datenbankzugang,
SMTP-Zugang, Empfängeradresse und den HMAC-Schlüssel. Im Repository liegt
nur `config.example.php`. Fehlerausgabe im Browser ist aus; Fehler gehen in
ein Protokoll ohne Formularinhalte.

## 7. Datenschutz

- Keine Anfragen an fremde Hosts, keine Cookies, kein Web Storage, keine
  Messung (`compliance-matrix.md` T-01, `vendor-register.md` §6).
- Thema und Sprache werden nicht gespeichert: Marketingseiten sind immer
  dunkel, die Sprache steht in der Adresse.
- Das Formular fragt fünf Angaben ab (`content-plan.md` §9).

## 8. Sicherheit

`.htaccess` setzt für alle Antworten:

| Kopfzeile | Wert |
|---|---|
| `Strict-Transport-Security` | `max-age=31536000` (ohne `preload`, bis alle Subdomains geklärt sind) |
| `Content-Security-Policy` | `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'` |
| `X-Content-Type-Options` | `nosniff` |
| `Referrer-Policy` | `strict-origin-when-cross-origin` |
| `Permissions-Policy` | alle Gerätefunktionen aus |
| `Cross-Origin-Opener-Policy` | `same-origin` |

Dazu: Verzeichnislisten aus, Zugriff auf `backend/` außer `public/api/`
gesperrt, Dateien mit Hash im Namen ein Jahr im Cache, HTML ohne Cache,
Komprimierung, eigene 404-Seite.

Abhängigkeiten: Lockfile, `npm audit` und Prüfung der PHP-Abhängigkeiten im
CI. Der Build wird auf fremde Hosts, Quelltext-Karten und Geheimnisse
durchsucht.

## 9. Fehlerfälle

| Fall | Verhalten |
|---|---|
| Seite nicht gefunden | 404-Seite im Layout der Sprache mit Links zur Startseite, zu Kontakt, Impressum und Datenschutz |
| JavaScript aus | alle Inhalte lesbar, Menü und Beleg funktionieren, Formular sendet, Netzschema als Standbild |
| Schrift lädt nicht | Systemschrift aus der Ersatzliste; Layout bleibt stabil |
| Versand der E-Mail scheitert | Anfrage ist gespeichert; Besucher sieht die Bestätigung; Vermerk für den Betreiber |
| Datenbank nicht erreichbar | Hinweisseite mit direkter Kontaktadresse; nichts geht verloren, weil nichts angenommen wurde |
| Screenshot fehlt | Platzhalter in endgültiger Größe |

## 10. Tests

| Ebene | Werkzeug | prüft |
|---|---|---|
| Daten | Vitest | Preise (Beta-Rechnung), Sprachpaare, Claims, Rechenregeln des Netzschemas gegen `design-system.md` §6.2 |
| PHP | PHPUnit | Validator (Grenzwerte, Steuerzeichen, Kopfzeilen-Einschleusung), Rate-Limit, Löschlauf |
| Seiten | Playwright, 360 / 768 / 1280 px, Chromium, Firefox, WebKit | jede Seite lädt, eine `h1`, kein waagerechtes Scrollen, Pflichtlinks, Sprachwechsel |
| Barrierefreiheit | `@axe-core/playwright` auf jeder Seite in beiden Themen; Tastaturtests für Menü, Beleg, Ausfallsimulation, Formular | keine Befunde; dazu manuelle Prüfung nach Liste |
| Datenschutz | Playwright-Netzwerktest | null fremde Hosts, null Cookies, leerer Web Storage |
| Formular | Playwright gegen PHP und MariaDB im CI | Erfolg, jeder Feldfehler, Köderfeld, Zeitfalle, Rate-Limit, ohne JavaScript |
| Sicherheit | Playwright und Skript | Kopfzeilen, CSP ohne Verstöße in der Konsole, keine Geheimnisse im Build |
| Leistung | Lighthouse CI | Budgets aus `project-brief.md` §7 |
| Verweise und HTML | Link-Prüfer, HTML-Validator | keine toten Links, gültiges HTML |

Geschrieben wird testgetrieben: erst der Test, dann der Baustein.

## 11. CI und Deploy

- Workflow `website.yml` mit Pfadfilter `website/**`: Lint, Typprüfung,
  Tests, Build. Die Produkt-CI bleibt unberührt.
- Deploy ist ein eigener Workflow mit manuellem Start und Freigabe über eine
  geschützte Umgebung: `build:release`, dann Upload per rsync über SSH oder,
  falls der Tarif kein SSH bietet, per SFTP-Spiegelung. Zugangsdaten liegen
  nur als Secrets der Umgebung vor.
- Zuerst in ein Vorschauverzeichnis mit Zugangsschutz, nach Sichtprüfung auf
  die Hauptdomain. Rückweg: der vorige Build bleibt als Verzeichnis liegen.
- Datenbank-Migration und `config.php` werden einmalig von Hand eingerichtet
  und dokumentiert; der Deploy fasst sie nicht an.

## 12. Reihenfolge der Umsetzung

| Phase | Ergebnis | Freigabe |
|---|---|---|
| 5 | Grundgerüst, Tokens, Bausteine, Startseite deutsch, Tests dafür | Auftraggeber sieht die Startseite in der Vorschau |
| 6 | übrige Seiten, Englisch, Formular und Backend | Inhalte je Seite |
| 7 | vollständige Quality Gates, Sicherheits- und Datenschutzprüfung, manuelle Barrierefreiheitsprüfung | Prüfprotokoll |
| 8 | Rechtstexte, geprüfte Claims, Screenshots, Deploy | Go-live durch den Auftraggeber |

## 13. Annahmen und offene Punkte

- Hetzner Webhosting L bietet PHP 8, MariaDB, Cronjobs, `mod_headers` und
  `mod_rewrite`. Das wird gerade gegengeprüft; das Ergebnis fließt hier ein
  (F-09).
- Die App antwortet unter `https://www.reticora.cloud/onboarding` und
  `/login` (F-02).
- Offene Geschäftsfakten stehen in `project-brief.md` §8; sie blockieren
  erst die Phasen, die dort genannt sind.
