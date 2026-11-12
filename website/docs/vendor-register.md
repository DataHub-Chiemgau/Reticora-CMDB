# Vendor-Register – Website für Reticora CMDB

| | |
|---|---|
| Stand | 2026-11-12 |
| Status | **Entwurf – Verträge und Standorte sind vom Auftraggeber zu bestätigen** |

Erfasst sind alle Dienstleister und Werkzeuge, die für die Website Daten von
Besuchern verarbeiten oder bei Bau und Auslieferung beteiligt sind. Die
Subprozessoren der Reticora-SaaS gehören nicht hierher; ihre Liste führt
`docs/legal/subprozessoren.md` (derzeit ohne Einträge).

## 1. Zur Laufzeit (verarbeiten Daten von Besuchern)

| Anbieter | Leistung | Daten | Ort | Vertrag | Drittland | Status |
|---|---|---|---|---|---|---|
| **Hetzner Online GmbH**, Gunzenhausen | Webhosting L: Webserver, PHP, MariaDB, Postfach und SMTP, Zugriffslogs, Backups | IP-Adresse und Anfragedaten in Logs; Formularinhalte (Name, E-Mail, Unternehmen, Anliegen, Nachricht) | Deutschland (laut Anbieter; im Konto zu bestätigen) | AV-Vertrag nach Art. 28 DSGVO: **offen** (F-09) | nein | offen |
| Domain-Registrar und DNS | Namensauflösung | keine Inhaltsdaten | unbekannt (F-02) | – | unbekannt | offen |
| Empfängerpostfach des Vertriebs | Eingang der Anfragen | Formularinhalte | unbekannt (F-06). Liegt das Postfach bei einem anderen Anbieter oder wird weitergeleitet, kommt dieser Anbieter hier hinzu. | offen | zu prüfen | offen |

Mehr ist zur Laufzeit nicht vorgesehen.

## 2. Bewusst nicht eingesetzt

| Kategorie | Entscheidung zum Start | Folge bei späterer Einführung |
|---|---|---|
| Reichweitenmessung, Tag-Manager | keine (F-13) | Neubewertung TDDDG §25, neuer Eintrag hier |
| Schriften von Drittservern | keine; Schriften liegen auf dem eigenen Webspace | – |
| CDN | keines | AV-Vertrag, Standort |
| Captcha eines Dritten | keines; Köderfeld, Zeitfalle, Rate-Limit, bei Bedarf selbst gehostetes ALTCHA | – |
| Eingebettete Videos, Karten, Social-Media-Inhalte | keine | Einwilligung vor dem Laden |
| Chat-Widget, Terminbuchung | keine | AV-Vertrag, Einwilligung |
| CRM, Newsletter-Dienst | keiner (F-06, F-07) | AV-Vertrag, Double-Opt-in, UWG §7 |

## 3. Bau und Auslieferung (keine Besucherdaten)

| Anbieter / Werkzeug | Zweck | Daten | Hinweis |
|---|---|---|---|
| GitHub (Repository, Actions) | Quelltext, Tests, Build, Deploy | Quelltext, Deploy-Zugang als Secret | keine Leads, keine Logs, keine Kundendaten im Repository oder in CI-Läufen |
| npm-Registry | Abhängigkeiten zum Build | keine personenbezogenen | Lockfile, Prüfung im CI |
| Claude Code (Anthropic) | Entwicklungshilfe | Quelltext und Dokumente dieses Projekts | keine echten Kundendaten, keine Leads, keine Zugangsdaten in Sitzungen |

## 4. Schriften und Lizenzen

Welche Schriften eingesetzt werden, hängt von der gewählten Art Direction ab
(`design-system.md`). Alle Kandidaten stehen unter der SIL Open Font License
und dürfen selbst gehostet werden. Die Lizenztexte werden mit ausgeliefert.

| Richtung | Schriften | Lizenz |
|---|---|---|
| A | Geist, Geist Mono | SIL OFL 1.1 |
| B | Archivo, JetBrains Mono | SIL OFL 1.1 |
| C | IBM Plex Serif, Sans, Mono | SIL OFL 1.1 |

Vor der Auslieferung wird die Lizenz jeder eingebundenen Datei im Build
geprüft.

## 5. Aufbewahrung und Löschung (Vorschlag)

Die Fristen legt der Auftraggeber fest. Die Werte sind Vorschläge für das
Gespräch mit der Datenschutzberatung.

| Daten | Ort | Vorschlag | Verfahren |
|---|---|---|---|
| Kontaktanfragen | MariaDB beim Hoster | Löschung 90 Tage nach Eingang, sofern kein Vertragsverhältnis entsteht | täglicher Cronjob, Protokoll ohne Personenbezug |
| Rate-Limit-Einträge | MariaDB | gesalzener Hash statt IP-Adresse, Löschung nach 24 Stunden | derselbe Cronjob |
| E-Mails im Vertriebspostfach | Postfach | nach interner Regel des Auftraggebers | vom Auftraggeber zu beschreiben |
| Zugriffslogs | Hoster | kürzeste einstellbare Frist, IP-Adressen gekürzt, falls einstellbar | Einstellung im Hetzner-Konto (F-09) |
| Backups des Hosters | Hoster | Frist des Hosters; gelöschte Leads verschwinden mit Ablauf der Backups | in der Datenschutzerklärung berücksichtigen |

## 6. Anfragen an fremde Hosts

Soll-Zustand zur Laufzeit: **keine**. Der Netzwerktest in Phase 7 lädt jede
Seite und bricht ab, sobald eine Anfrage an einen anderen Host als die eigene
Domain geht, ein Cookie gesetzt wird oder Web Storage beschrieben wird.

Einzige gewollte Ausnahme sind Links, denen der Besucher selbst folgt: zur
App (Registrierung, Anmeldung) und gegebenenfalls zur Dokumentation (F-02,
F-15).

## 7. Offene Punkte

- AV-Vertrag mit Hetzner abschließen und ablegen (F-09).
- Standort und Anbieter des Vertriebspostfachs benennen (F-06).
- Registrar und DNS-Anbieter benennen (F-02).
- Subprozessorenliste der SaaS vom Betreiber beschaffen, bevor die Seite
  `/subprozessoren/` Inhalt bekommt (F-10).
