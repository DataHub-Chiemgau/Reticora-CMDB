## Fehlende Teilberichte – Gesamtprüfung unvollständig

**Noch nachholen:** Teil 03 (Datenmodell/Standorte/Metamodell). Diese Datei fehlt im geprüften Checkout; der Teil muss erstellt beziehungsweise seine bereits vorhandenen Ergebnisse bereitgestellt werden:

- `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/03-datenmodell-standorte-metamodell.md`

Vorhanden und jeweils als VOLLSTÄNDIG bezeichnet: Teile 01, 02, 04, 05, 06, 07, **08 und 09**. Das bezeichnet dort abgeschlossene Bearbeitung, nicht Konformität oder erfolgreiche Integrationsnachweise. Der früher als „08 erneut“ nachgereichte Text war tatsächlich Teil 09; dessen 46 IDs bleiben konsolidiert. Der nun vorliegende eigenständige Teil 08 ergänzt 50 IDs zu Frontend/Monitoring/NFR/Tests/Gates/Traceability. Insbesondere Teil 06 deckt nur seinen ausdrücklich gelieferten Umfang mit gemeinsamen Prüfblöcken und überwiegend unbekannten Tags ab. Der fehlende Teil 03 wird weder als PASS noch pauschal als Produkt-FAIL gezählt; seine unbekannte ID-Menge wird nicht erfunden.

## Management-Zusammenfassung

Stand 2026-10-01; Referenzstand `abda3bff7d03b91826df635c2b80fc2c31e95005` wie Teil 08, ohne Produktänderung gegenüber der vorigen Konsolidierung `b7e747a6181cad553e529a2a4d7aa7d1582a7743` und der ursprünglichen Prüfkopie `d8f35e05f891d7c950f9e19824221a095807493c`.
**Gesamtabdeckung unvollständig:** Nur Teil 03 fehlt; acht vorhandene Teile ergeben **329 eindeutige IDs** (279 unverändert, neu 50 aus Teil 08).
**Tagbasierte Vergleichsquote im verfügbaren Korpus: 0/184 [B]-oder-[Q]-IDs = 0 % PASS**; keine isolierte G1-Pflichtmenge, keine Vollkatalogquote und nicht „0 % Produktcode vorhanden“. Insbesondere GATE-04 gehört zu GA; weitere Prozess-/Qualitätspflichten sind phasenabhängig.
Nach Widerspruchsbereinigung: **49 Critical und 176 High** über alle Phasen, gezählt je ID, nicht je unabhängiger Ursache; Teil 08 ergänzt zwei Critical, 32 High, elf Medium und fünf N/P ohne Defekt-Severity.
Davon betreffen **27 Critical und 123 High** explizite [B]/[Q]-IDs; spätere/ungeklärte Tags sind getrennt ausgewiesen.
**G1 nicht freigabefähig:** Isolation, Autorisierung, manuelle Datenhoheit und zuverlässige Verarbeitung haben belegte Lücken; erforderliche Abnahmen fehlen.
Lasttest, vollständige Isolation, Pentest und Install-Smoke für beide Plattformen sind nicht vollständig nachgewiesen.
**261 bekannte [B]/[Pn]/[A]-IDs ohne Traceability-Eintrag**: 224 bisherige Quell-IDs plus 37 aus Teil 08. GLO-10 ist jetzt eine echte Quell-/CSV-Zeile, kein zusätzlicher synthetischer Eintrag; die frühere 225er-Menge steigt deshalb um 36.
**39 N/P-IDs**, wegen fehlendem Originaltext beziehungsweise Prozess-/Abnahmenachweisen; CH16-Vaultvorschlag wird nicht als beschlossene G1-Pflicht ausgegeben.
Priorität: Isolation/RLS → Auth/Authz → Datenmodell/Migrationen → Ingest/Reconciliation → API/Jobs → Frontend; anschließend unabhängige Gate-Nachweise.
Diese Ergänzung der Konsolidierung ändert ausschließlich Befund-CSV und Gesamtbericht auf Grundlage des neuen Teil-08-Berichts; keine Produktiv-, Migrations-, Test- oder Dependencydateien. Teil 08 dokumentiert strikt offline ausgeführte, teilweise setupblockierte Tests. Die historische Testausführung zu Teil 09 wich dagegen vom Offline-/Installationsverbot ab; grüne Folgeläufe sind kein konformer Offline-Nachweis (Stand unten).

## Datengrundlage und Zählregeln

Erst Zusammenfassung und Stand, anschließend die Ergebnistabellen der vorhandenen Teile gelesen. Quelltexte werden nur zur Klärung berichtsübergreifender Widersprüche erneut geprüft. Historische Testläufe bleiben als solche gekennzeichnet; Teil-08-Läufe und die dort beobachtete historische Produkt-CI sind von Teil-09-Läufen getrennt und überschreiben keine früheren Testgrenzen. Die Konsolidierung selbst prüft Dokumentstruktur und Zählungen, nicht das Produkt oder einen Pentest.

| Teil | Berichtsstatus | Einzel-IDs / Tabellenblöcke | Quelle |
|---|---|---|---|
| 01 | VOLLSTÄNDIG, v2-Lücken | 53 / 53 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/01-installation-stack.md:30–84,216–222` |
| 02 | VOLLSTÄNDIG, v2-/Laufzeitlücken | 36 / 36 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/02-mandanten-auth-entitlements.md:32–69,494–498` |
| 03 | FEHLT | nicht bestimmbar | Fehlender Pfad oben |
| 04 | VOLLSTÄNDIG, v2-/Laufzeitlücken | 52 / 52 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/04-ci-netz-rack-beziehungen-impact.md:27–80,261–267` |
| 05 | VOLLSTÄNDIG, Laufzeit-/Teiltextlücken | 37 / 37 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/05-collector-discovery-reconciliation.md:30–68,330–332` |
| 06 | VOLLSTÄNDIG nur für eingeschränkten Auftrag | 16 / 12 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/06-audit-sicherheit-events.md:16–33,208–210` |
| 07 | VOLLSTÄNDIG, Laufzeit-/Teiltextlücken | 39 / 39 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/07-api-suche-jobs-lebenszyklus.md:24–64,269–273` |
| 08 | VOLLSTÄNDIG, gemeinsame UI-Kriterien, v2-/Prozess-/Laufzeitlücken | 50 / 50 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/08-frontend-monitoring-nfr-tests.md:28–79,179–276` |
| 09 | VOLLSTÄNDIG, gemeinsame IGA-Kriterien, v2-/Laufzeitlücken | 46 / 46 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/09-module-phase2plus.md:27–74,164–216` |

Die CSV hat eine Zeile pro **Anforderungs-ID**, nicht pro Ursache oder Prüftabellenblock. Gemeinsam bewertete AUD-01/02, AUD-05/07/09 und EVT-01/02 werden für die angeforderten Einzelzeilen aufgeteilt und als gemeinsame Bewertung gekennzeichnet. **IGA-01–04 [A]** erhalten vier ID-Bewertungen ausschließlich für denselben ausdrücklich gelieferten Gruppenvertrag, keine erfundenen v2-Einzelverträge; Details bleiben N/P. Ebenso erben **UI-01–10 [B]** ausschließlich die gemeinsamen gelieferten UI-Kriterien, nicht zehn rekonstruierte Einzelverträge oder unabhängige Fehler; **UI-06** besitzt zusätzlich eine explizite, als High bewertete Erweiterung. MGT-05/06 teilen den Desk-Scopefehler, AST-01/MGT-11 den Asset-Scopefehler; UI-18/NFR-07 dieselbe Browser-/Accessibility-Ursache. `CH16` ist eine Entscheidung und keine zusätzliche Anforderungszeile. Mehrere IDs können dieselbe Ursache betreffen; Critical-/High-Zähler und Aufwand sind deshalb keine Anzahl beziehungsweise Summe unabhängiger Schwachstellen.

`abschnitt` bezeichnet den **Auditteil** (01–09), nicht eine aus Präfixen geschätzte Katalogabschnittsnummer. `bericht` verweist absolut auf die Quellzeile; `evidenz` enthält die dort genannten Belege. Nicht bekannte Tags bleiben unbekannt. [B]/[Q]-Quote zählt jede ID mit explizitem [B] oder [Q] genau einmal; gemischte Phasen-/Vorschlagsgruppen werden gesondert erläutert und nicht heimlich als Baseline eingerechnet.

## Statistik

### Abschnitt × Status: [B] oder [Q]

Explizite Tagzugehörigkeit der ID, Mehrfachzugehörigkeit nur einmal gezählt. 166 IDs tragen [B], 19 [Q]; TEC-06 trägt beides, daher **184**, nicht 185 (bisher 141, neu 43). ENT-06 trägt die gemischte Vorschlagsgruppe `[B–P4], [A]`, kein einzelnes `[B]`: separat geführt. Bei Zuordnung auch dieser ID zu B ergäbe sich **0/185**, unverändert 0 %. ABN-01 ist eine [Q]-ID; ihre [B]/[P2]-Teilszenarien erzeugen keine weiteren IDs oder zusätzlichen [B]-Haupttag. SEC-05 erhält aus CH16(V) keinen erfundenen [B]-Tag.

Die 0/184 sind eine **tagbasierte Vergleichsquote, keine isolierte G1-Pflichtmenge**: insbesondere GATE-04 [Q] gehört zu GA, weitere Prozess-/Qualitätspflichten einschließlich SEQ-01 sind phasenabhängig. Daraus wird weder eine neue G1-Nennerzahl geschätzt noch ein zusätzlicher G1-Blocker wegen GA abgeleitet.

| Auditteil | PASS | PARTIAL | ABWEICHEND | FAIL | OFFEN | N/P | Summe |
|---|---:|---:|---:|---:|---:|---:|---:|
| 01 | 0 | 7 | 11 | 2 | 0 | 0 | 20 |
| 02 | 0 | 4 | 21 | 1 | 0 | 2 | 28 |
| 03 | — | — | — | — | — | — | Bericht fehlt |
| 04 | 0 | 6 | 24 | 6 | 0 | 1 | 37 |
| 05 | 0 | 3 | 23 | 3 | 0 | 0 | 29 |
| 06 | — | — | — | — | — | — | Tags unbestätigt |
| 07 | 0 | 7 | 12 | 8 | 0 | 0 | 27 |
| 08 | 0 | 22 | 10 | 6 | 0 | 5 | 43 |
| 09 | 0 | 0 | 0 | 0 | 0 | 0 | 0 (keine [B]/[Q]-ID) |
| **Bekannte Gesamtmenge** | **0** | **49** | **101** | **26** | **0** | **8** | **184** |

Keine WARN-Kategorie im vorliegenden Bewertungsschema; vorhandene PARTIAL/ABWEICHEND werden nicht nachträglich zu erlaubten SOLL-WARN umgedeutet. Die Phasengeltung unbekannter Tags muss geklärt werden.

### Spätere Phasen/Add-ons: vorhanden/offen

„Vorhanden“ bedeutet hier **Implementierung nachgewiesen, aber PARTIAL/ABWEICHEND**, nicht erfüllt oder releasbar. Die disjunkten Haupttaggruppen verhindern Mehrfachzählung; spätere Teilpflichten einer [B]-ID sind im Befund enthalten, nicht noch einmal als ganze ID gezählt.

| Haupttaggruppe | PASS | Vorhanden, nicht PASS | OFFEN | Summe |
|---|---:|---:|---:|---:|
| [P2] | 0 | 37 | 8 | 45 |
| [P3] | 0 | 9 | 5 | 14 |
| [P4] | 0 | 14 | 8 | 22 |
| [P5] | 0 | 2 | 1 | 3 |
| [A] führend (COL-07, IGA-01–04, AI-01/02; Relay auch P4) | 0 | 7 | 0 | 7 |
| Mehrphasig P2–P4/[A] (RBA-05, API-06, UI-14) | 0 | 3 | 0 | 3 |
| (V), B–P4/[A] (ENT-06; Befund nur zu CH14 verbindlich) | 0 | 1 | 0 | 1 |
| **Gesamt dieser Gruppen** | **0** | **73** | **22** | **95** |

Auch diese Übersicht ist **keine vollständige P2–P5-/GA-Abnahme**: Teile 08/09 sind vollständig bearbeitet, aber benannte Teiltext-/Laufzeitnachweise und Teil 03 fehlen. Die sieben neuen IDs sind MON-03/04, UI-11/12/13/14/15. Ungetaggte Teil-06-IDs werden nicht als spätere Phase einsortiert. [O] als Haupttag betrifft TEC-16/INS-08/TKT-03 (alle OFFEN); optionale Unteranteile anderer IDs erzeugen keine Extrazeilen. Gemischte Teilpflichten in INV-02/03, TKT-01 und WFL-04 bleiben im führenden P2-/P3-Haupttag gezählt, nicht zusätzlich in P4/[A]; spätere Teilpflichten von NFR-01/03/04, SIM-01, TST-01/03 und ABN-01 erzeugen ebenfalls keine zusätzlichen Gruppen-IDs.

### Alle verfügbaren Einzel-IDs nach Korrektur

| PASS | PARTIAL | ABWEICHEND | FAIL | OFFEN | N/P | Summe |
|---:|---:|---:|---:|---:|---:|---:|
| 0 | 79 | 159 | 27 | 25 | 39 | **329** |

Severity: **49 Critical, 176 High, 37 Medium, 3 Low, 64 ohne Defekt-Severity**. Im ursprünglichen 233-ID-Korpus wurden die 31 Critical/125 High durch K1/K2 auf 30/123 korrigiert: SEC-05 sowie AUD-01/02 wegen unbestätigter Vaultpflicht. Diese Korrekturen bleiben unverändert; Teil 09 brachte 17 Critical/21 High, Teil 08 nun zwei Critical/32 High hinzu. Das sind insbesondere keine vier unabhängigen IGA-Ursachen oder zehn unabhängigen UI-v2-Defekte. Teil 08 ergänzt 28 PARTIAL, elf ABWEICHEND, sechs FAIL und fünf N/P. Unveränderte technische Risiken und ursprüngliche Ratings bleiben ausdrücklich erhalten. Vollständige Status-/Tagwerte stehen in der CSV.

## Gate-Check G1

Maßstab sind die jetzt einzeln bewerteten GATE-01–05 aus Teil 08. **Anforderungsstatus und aktuelle Freigabeempfehlung sind verschieden:** GATE-01/04/05 bleiben wegen fehlender Prozess-/Freigabeartefakte N/P; das erlaubt keinen positiven G1-Entscheid trotz belegter Critical- und Nicht-PASS-Befunde.

| Kriterium | Ergebnis | Begründung / benötigter Nachweis |
|---|---|---|
| GATE-01: kein offenes Critical im Gate | aktuelle G1-Voraussetzung nicht erfüllt; Prozess-ID N/P | Explizite Baselinebefunde etwa TEN-04/05, AUT-01/02, CI-04/10, OVR-01/02 und API-04/07 sowie MON-01/NFR-04 offen. Historische Gatefreigabe/Bereinigung nicht belegt (08:72). |
| GATE-02: sämtliche Phasen- und Qualitätsanforderungen PASS | nicht erfüllt; ID FAIL | Keine der 184 vorhandenen [B]/[Q]-IDs PASS; N/P ist ebenfalls kein PASS. Nur Teil 03 fehlt; DOD-01 PARTIAL, SEQ-01 N/P und GLO-11 FAIL sind jetzt echte Ergebniszeilen (08:70–73). |
| GATE-03: Lasttest NFR-10 bestanden | nicht erfüllt (Nachweis fehlt) | NFR-10 FAIL wegen fehlender Lasttest-/Seed-/Ergebnisartefakte, kein behaupteter gescheiterter Lastlauf. Explizite NFR-01–03-Ziele und skalierter SIM-01-Seed sind verbindlich und messbar zu prüfen; CH15 bleibt separat bestätigungsbedürftig (08:54–56,63–64). |
| GATE-03: Pentest ohne offene High/Critical | nicht erfüllt | Vorhandene statische Sicherheitsbefunde nicht behoben; kein freigabefähiger Pentestbericht nachgewiesen. Statisches Audit ist kein Pentest. |
| GATE-03: Isolationstest TEN-09 vollständig | nicht erfüllt | TEN-09 PARTIAL: keine vollständige Site-/Team-/Schreib-/Kanalabdeckung; vorhandene Integrationstests sind nicht durchgängig ausgeführt. Quelle Teil 02:42. |
| GATE-03: Install-Smoke auf beiden Plattformen | nicht erfüllt | INS-01 PARTIAL: Workflow nur ubuntu-latest, kein belegter zweiter Zielplattformlauf; Tests mit gemocktem IdP ersetzen Install→Login nicht (INS-04). |
| GATE-05: [O] beeinflusst kein Gate | Prozess-ID N/P; kein Audit-PASS | TEC-16/INS-08/TKT-03 und optionale Zusatzprotokolle/Textsyntax/Air-Gapped-Teile werden hier aus Pflichtquote und G1-Blockern ausgeschlossen. TKT-03/CH17 bleibt klärungsbedürftig. Die eigene Bewertungsregel beweist weder historische Gateentscheidungen noch eine getestete Repository-Prozessgarantie (08:76). |

Ein Gate wird nicht dadurch grün, dass seine Kriterien mangels Bericht unbewertet sind. Die P2–P5-/[A]-Unvollständigkeit löst für sich kein G1-Fehlen aus; bereits implementierte Isolation-/Auth-/Datenhoheitsfehler müssen für die im Release tatsächlich aktivierten Kanäle dennoch behoben oder nachweislich ausgeschlossen werden. Teil 09 bestätigt solche aktiven Modul-, Agent-, Workflow-, Blob- und AI-Pfade sowie eine destruktive Migrationsbereinigung (AST-05), nicht zusätzliche G1-FAILs allein wegen fehlender späterer Module. GATE-04 betrifft GA, nicht G1.

### Verbindliche NFR-Ziele und fehlende Messungen

Der nun vorliegende Katalogteil `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:34–52` ersetzt frühere Annahmen über unbekannte Teil-08-Ziele: NFR-01 fordert 10.000 Objekte (CI + Asset) pro Org typisch, 50.000 Headroom, 50.000 Interfaces, 200.000 IPs, 100.000 Beziehungen sowie 500 SaaS-Orgs in P1/5.000 in P5. NFR-02 nennt acht p95-Budgets bei 10.000 Objekten: Listen und Suche je <500 ms, CI-Detail <300 ms, Topologie mit 2.000 Knoten <3 s, Impact mit **Tiefe 20** <2 s, indizierte Attributfilter <500 ms, Bulk mit 500 IDs <30 s, Rack-Layout <300 ms. **Impact-Tiefe ist kein GraphQL-Limit; K3 bleibt unverändert.**

NFR-03 fordert 200/1.000 DeviceRecords/s in P1/P5, Sichtbarkeit <30 s und Flush von 10.000 Records <5 min; NFR-04 99,5/99,9 % SaaS-Verfügbarkeit und verlustfreie 24h-Pufferung. NFR-05 verlangt RPO 15 min/RTO 4 h und versionierte/replizierte Dokumente; NFR-06 Audit zehn Jahre, Historie/Snapshots drei Jahre, Events/Logs 30 Tage, Backups 35 Tage, Metriken 400 Tage und Soft-Delete 90 Tage. NFR-07/UI-18 verlangen aktuelle/Vorversion von Chrome, Edge, Firefox, Safari, mobiles Safari/Chrome und WCAG 2.1 AA; NFR-09 EU-Residenz einschließlich Backups/Logs/Suche und Subprozessoren-Dokumentation. Diese expliziten Sollwerte sind bekannt, **ihre vollständige Mess-/Betriebsabnahme bleibt offen**. NFR-08 bleibt wegen des fehlenden SEC-11-Originalvertrags N/P; frühere Auditinterpretationen ersetzen ihn nicht.

## Invarianten PRI-10

Wortlaut: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md:82–91`; ergänzender aktiver Bereichsabgleich aus Teil 08: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/08-frontend-monitoring-nfr-tests.md:183–197`. Ein Gegenbeispiel genügt für „verletzt“, wenige positive Pfade nicht für „eingehalten“. Die Nummerierung unten bezeichnet die neun Aufzählungspunkte, keine neuen Katalog-IDs.

| Nr. / Invariante | Bewertung | Betreffende Befunde / Abgrenzung |
|---|---|---|
| 1. Ein Gerät → ein CI; kein Auto-Merge ohne belastbare Identität | verletzt | REC-02: Hostname-Automatch, unzureichende Serial-/MAC-/Client-/VRF-Qualifikation; CI-13: gelöschte Geräte können neu angelegt werden; REC-11/AGT-05: separater Agentmatcher verwendet den ersten Name-/Hostnametreffer statt bestehender CI-Verknüpfung. AST-03 belegt keine vollständige physische Dublettenfreiheit. NFR-01 ergänzt den begrenzten 10.000-CI-Suchhorizont; TST-01 zeigt Tests des bestehenden Hostnamematchings statt vollständiger Sollfallmatrix. Keine Garantie durch bloße Existenz eines Matchers. |
| 2. Asset/CI getrennt und komponierbar, Asset führend für kaufmännische Daten/Serial/Standort/Lifecycle | verletzt | EXP-01 (DATEV-Teil): DATEV verwendet CI-Serial und freie CI-Kauf-/Standortattribute statt Assetdaten; RCK-03 übernimmt Standort nicht nach DB-05. Gegenbeleg direkt in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/export.go:194–204`. Teil 09 ergänzt AST-01/04/05: fehlende kanonische Felder/Kindmodi, CI-Serial weiterhin schreibbar; vollständiger DB-/Standortvertrag aus Teil 03 bleibt offen. |
| 3. Genau ein kanonischer Speicherort; Kopien abgeleitet und read-only | verletzt | CI-10/OVR-01: getrennte Feldwert-/CI-Sicht ohne konsistenten effektiven Wert; EXP-01: kaufmännische CI-Attributkopien. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/pg_repository.go:330–353` lässt Serial-/Standort-/Attributänderungen zu. AST-02/07/STK-04 ergänzen parallele schreibbare Assignments und zwei Ledger; INV-01 schreibt Assets direkt statt über Bewegungen. Keine pauschale Behauptung, jeder Doppelwert sei bereits ein Verstoß. |
| 4. Manuell gewinnt; Automation zerstört nie still manuelle Daten | verletzt | CI-04/CI-10, REC-03/05/12, OVR-01/02, API-07: fehlende Autooverrides, manual20 statt100, fail-open Schutzabfrage und direkter Workflow-Schreibpfad. AGT-05/WFL-02 umgehen die zentrale Provenienz-/Overrideentscheidung; agent70 statt85, kein workflow92. Vorhandene Historie heilt den Verlust des wirksamen manuellen Werts nicht. |
| 5. Kein stilles Verwerfen; unklare Fälle → Review | verletzt | OPS-06/COL-05/NFR-04: unbestätigte Spooldaten durch Alter-/Größenbereinigung gelöscht, bei 1 GiB auch innerhalb der verlangten 24 Stunden; REC-06/08: Eingangskonflikt kann verloren gehen, unvollständiger Reviewprozess. UI-16 ergänzt die fehlende Review-Inbox, nicht einen zusätzlichen nachgewiesenen Datenverlust. AST-05 belegt direktes Löschen inkonsistenter Composition-Zeilen vor FK-Härtung, ohne Quarantäne; Produktionsbetroffenheit N/P. AGT-03 hat fehlerhafte ACK-/Replaypfade. CH21(V) bestätigt allein keine Lizenz-Reviewpflicht; Teil 02:450 bewertet den unlicensed_ci-Prozess jedoch als eigenständige ENT-03-Einzelpflicht, die davon getrennt bestehen bleibt. |
| 6. Historisierung aller Bewegungen/Änderungen mit observed_at | verletzt | GLO-12, AUD-03, REC-07, RCK-03, REL-09: Server- statt Quellzeit, falsche Granularität, fehlende Änderungsereignisse; JOB-04: State-at verwendet created_at (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/history/pg_repository.go:91–97`). AST-02/07, STK-04/05, INV-01 und MGT-02 ergänzen nichtatomare/ignorierte Bewegungen und veränderbare Historie. TST-01 bestätigt die fehlende vollständige observed_at-/Outbox-Sollfallmatrix; NFR-06 betrifft daneben explizite Aufbewahrungsfristen, nicht den Beweis tatsächlich verlorener Historie. Ein bloßes History-API ist kein Quellzeitnachweis. |
| 7. Server-/DB-Isolation einschließlich Client/Site/Team | verletzt | TEN-02/04/05/06, RBA-03, IMP-07, TEC-06/12, GQL-04, SRC-01/03/04, EXP-01; zusätzlich AST-01/MGT-11, MGT-02/03/04/05/06/07, TKT-01, AGT-06, WFL-01/02 und AI-01. MON-01 bestätigt fehlende RLS auch am Metrikpfad, TST-02 die unvollständige Site-/Team-/Schreibmatrix. TEN-06/MGT-01 betreffen denselben zusätzlichen Cross-Org-Blobpfad. ENABLE/FORCE und Org-USING/WITH CHECK sind in den übrigen genannten Modulen vorhanden, aber keine vollständigen Bereichs-/Feldrechte; orgweite Konfigurationen werden nicht pauschal clientbezogen verlangt. Keine behauptete Live-Exfiltration. |
| 8. Server-seitige Entitlements | verletzt | ENT-02/03/04/05/06/07/08, API-04/06, GQL-04: Ingest-/Export-Aliasse umgehen Gates, tenantseitige Selbstfreigabe, Add-ons implizit im Plan und falsche Ablaufwirkung. IGA-01–04 bestätigen denselben gemeinsamen Add-on-Mangel; AI-02 indexiert ohne Org-Opt-in/Entitlement am tatsächlichen Providerpfad. CH14/CH21 sind verbindlich, vorgeschlagene Paketwerte nicht; tatsächlicher Verarbeitungsort/Nichttraining bleibt ohne Betriebs-/Vertragsbelege N/P. |
| 9. Erweiterungen ergänzen, ersetzen die Architektur nicht | unklar als Gesamturteil | Wiederverwendete Ports/Repositories stehen separatem Agentmatcher (REC-11/AGT-05), rohem Workflow-Schreibpfad (OVR-02/WFL-02) und uneinheitlicher Rechteverdrahtung (RBA-05) gegenüber. MON-04 ergänzt feste Operatoren statt gemeinsamer Regel-AST. Teil 09:168–181 bestätigt erweiterbare Entitlement-Keys, aber fehlende kanonische Assetspalten/Eventsequenzen. Diese konkreten Verletzungen sind oben erfasst; AST-05 belegt einen destruktiven Pfad, nicht unvermeidlichen Datenverlust durch jede spätere Erweiterung. Ohne Teil 03 und weitere Detail-/Laufzeitnachweise kein vollständiges Architektururteil. |

## Critical- und High-Befunde nach Fundament

Die folgenden Gruppen nennen sämtliche betreffenden Einzel-IDs; Mehrfachursachen sind nicht als voneinander unabhängige Schwachstellen zu verstehen. Vollständige Status-, Phasen-, Aufwand- und Quellbelege stehen ID-genau in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/befunde.csv`. „Teil/Zeile“ verweist auf den absoluten Quellberichtspfad aus der Datengrundlage. Innerhalb jeder Severity gilt die verlangte Fundament-Reihenfolge; Stack/Installation/Betrieb ist der Datenmodell-/Infrastrukturbasis zugeordnet, sofern nicht unmittelbar Auth, Ingest oder Jobs betroffen sind.

### Critical

| Fundament | IDs | Kernbefund | Teil/Zeile |
|---|---|---|---|
| Isolation/RLS | TEC-06 | Metriken/Aggregate ohne RLS oder sicheren Fallback | 01:59 |
| Isolation/RLS | MON-01 | Metriktabellen ohne RLS/USING/WITH CHECK; gleicher Isolationskomplex wie TEC-06, zusätzlich falscher Stunden-Viewname | 08:32 |
| Isolation/RLS | TEC-12 | Implementierte OpenSearch-Suche ohne vollständigen Nutzerscope; P5, kein pauschaler G1-Fehlumfang | 01:61 |
| Isolation/RLS | TEN-02, TEN-04, TEN-05, TEN-06 | Fehlende denormalisierte Scopes/Schreibschranken, inkonsistente Tenanttransaktionen, zusätzlicher Dokument-Blobzugriff | 02:35,37–39 |
| Isolation/RLS | IMP-07 | Traversierung vor Sichtbarkeitsprüfung; unsichtbare Zwischenknoten | 04:76 |
| Isolation/RLS | SRC-01, SRC-03, SRC-04 | Org-only Suche/CTEs und fehlendes Entitätsleserecht im strukturierten Suchpfad | 07:39,41–42 |
| Isolation/RLS | EXP-01 | Scopeverlust im Exportworker; Jobzugriff nur orggebunden | 07:47 |
| Isolation/RLS | AST-01, MGT-11 | Asset-/Cloud-Assetpfad nur orggesichert; dieselbe Scopeursache, zwei ID-Bewertungen | 09:29,54 |
| Isolation/RLS | MGT-01 | Frei gesetzter StorageKey kann fremdes Org-Blob referenzieren; Metadatenbesitz genügt nicht | 09:44 |
| Isolation/RLS | MGT-02, MGT-03, MGT-04, MGT-05, MGT-06, MGT-07, TKT-01 | Objekt-/Personen-/Raum-/Teambezüge ohne geerbte Bereichsrechte; Desk-Scopeursache für zwei IDs | 09:45–50,56 |
| Isolation/RLS | AGT-06, WFL-01, AI-01 | Enrollment/Formscope und RAG-Quellobjekt-/Feldrechte fehlen trotz Org-RLS | 09:64–65,73 |
| Auth/Authz | AUT-01, AUT-02, TLC-04 | Login reaktiviert gesperrte Nutzer; Refresh wiederholt alte Rechte ohne wirksamen Widerruf | 02:44–45; 07:52 |
| Auth/Authz | AUT-09 | Produktiver Demo-Admin und fehlende zwingende Authentifizierungspolicies | 02:52 |
| Auth/Authz | RBA-02, RBA-03, RBA-04 | Rollen-/Scopeauflösung inkonsistent; falsche Aktionsrechte trotz vollständig gemappter Routen | 02:55–57 |
| Auth/Authz | COL-02, SEC-01 | Klartext-Credential-/Webhook-Headerzugriff; Redfish ohne Zertifikatsprüfung | 05:35; 06:26 |
| Auth/Authz | SEC-08 | Ausgehende Webhook-/SCIM-Ziele ohne hinreichende private/Metadaten-/Redirect-Zielprüfung | 06:30 |
| Auth/Authz | API-04 | Nichtatomare orgweite Idempotenzantwort vor Aktionsrechten; fehlende Rate-Buckets | 07:29 |
| Auth/Authz | GQL-04 | ci:write statt Resolverleserechten; volle Objekte ohne Feldprojektion | 07:38 |
| Auth/Authz | AGT-03 | Klartext-Relay/HTTP-Fallback und nicht interoperabler Auth-/Payload-/Replayvertrag; kein behaupteter Auth-Bypass | 09:61 |
| Datenmodell/Migrationen | AST-05 | Migration 56 löscht inkonsistente Composition-Zeilen vor FK-Härtung ohne Quarantäne; tatsächliche Produktionsbetroffenheit N/P | 09:33,81 |
| Ingest/Reconciliation | CI-04, CI-10, OVR-01, OVR-02, API-07 | Fehlende Autooverrides/einheitliche effektive Werte, fail-open Schutzprüfung, Workflowumgehung; keine CI-Konkurrenzkontrolle | 04:33,39; 05:66–67; 07:32 |
| Ingest/Reconciliation | OPS-06, COL-05 | Unbestätigte Spooldaten werden ohne fachliche Verlustmeldung gelöscht | 01:84; 05:38 |
| Ingest/Reconciliation | NFR-04 | Gleicher Spoolverlust bei 1-GiB-Pruning auch innerhalb 24 Stunden; Wartungsankündigung als sent ohne Versand, reale Uptime N/P | 08:57 |
| Ingest/Reconciliation | AGT-05, WFL-02 | Agent-/Workflow-Schreibpfade umgehen zentrale Identitäts-, Provenienz-, Override- und CI-Serialisierungsregeln | 09:63,66 |

Keine zusätzlich als Critical bewertete reine Frontend-ID liegt vor; Kanal-/Jobbefunde stehen entsprechend ihrer eigentlichen Isolation-/Auth-/Datenhoheitsursache oben.

### High

| Fundament | IDs | Kernbefund | Teil/Zeile |
|---|---|---|---|
| Isolation/RLS | TEN-03 | Effektiver DDL-/FORCE-Vertrag nicht vollständig belegt | 02:36 |
| Isolation/RLS | TEN-09 | Unvollständige echte Org-/Client-/Site-/Team-/Schreib-/Kanalmatrix | 02:42 |
| Isolation/RLS | TST-02 | Echte Org-/Client-/mTLS-Teiltests, aber keine vollständige Site-/Team-/Schreib-/Dienstekette oder DB-Testauswahl | 08:67 |
| Auth/Authz | AUT-04, AUT-08, AUT-10 | Key-Rechteschnitt/Rotation, SCIM-Deprovisionierung und Pre-Auth-Ratelimits unvollständig | 02:47,51,53 |
| Auth/Authz | RBA-01, RBA-05, RBA-06, RBA-08 | Fehlende/abweichende Aktionsrechte, umgangene Spezialrechte und Service-Account-Autorität | 02:54,58–59,61 |
| Auth/Authz | ENT-01, ENT-02, ENT-03 | Feature-/Limit-/Herkunftsmodell und tatsächliche Enforcementpfade stimmen nicht | 02:62–64 |
| Auth/Authz | ENT-04, ENT-05 | Tenant-Selbstfreigabe und fehlende Export-/Ingest-/BFF-Gates | 02:65–66 |
| Auth/Authz | ENT-06, API-06 | Verbindliche Add-on-Trennung CH14 durch implizite Planfreigabe verletzt, unabhängig von Paketvorschlägen | 02:67; 07:31 |
| Auth/Authz | ENT-07, ENT-08 | Lizenzablauf sperrt falsche Funktionen; Downgrade/Bestands-/Ingestvertrag nicht vollständig | 02:68–69 |
| Auth/Authz | SEC-07 | Operator-/Break-Glass-/MFA-/separater Auditpfad fehlt | 06:29 |
| Auth/Authz | INS-02 | Issuer-Erreichbarkeit/-Richtigkeit und Brokerkonfiguration nicht vollständig geprüft | 01:72 |
| Auth/Authz | IGA-01, IGA-02, IGA-03, IGA-04 | Gemeinsamer expliziter Gruppenvertrag: keine erzwungene zweite Freigabe, implizites Enterprise-Add-on, kein produktiver RunDue; vier Ratings, keine vier unabhängigen Ursachen | 09:69–72 |
| Auth/Authz | AI-02 | Providerindexierung ohne Org-Opt-in/Add-on-Prüfung und Air-Gap-Abschaltung; tatsächliche EU-Verarbeitung/Nichttraining nicht belegt | 09:74 |
| Datenmodell/Migrationen | TEC-09, INS-01, INS-03 | Kein vollständiger OS-/Smoke-/TLS-/ACME-Installationsvertrag | 01:60,71,73 |
| Datenmodell/Migrationen | INS-05 | Socket-Passwortprobe/Keycloak-DB-Abgleich und Konfigurationserhalt problematisch | 01:75 |
| Datenmodell/Migrationen | TEC-15, SEC-11 | Signatur-/SBOM-Releaseprozess und vollständige Go-/Container-Scans fehlen; npm-Audit existiert | 01:64; 06:32 |
| Datenmodell/Migrationen | REP-01 | Expliziter Monorepo-Ergänzungsvertrag nicht vollständig umgesetzt | 01:66 |
| Datenmodell/Migrationen | OPS-01, OPS-02, OPS-03, OPS-04 | Statische Healthchecks, fehlende unabhängige Betriebsschalter/Master-Key-Injektion/Backup-/Recovery-Nachweise | 01:79–82 |
| Datenmodell/Migrationen | SEC-06 | Rotation nicht vollständig automatisiert; nichtatomare DEK-Updates gefährden Lesbarkeit | 06:28 |
| Datenmodell/Migrationen | CI-01, CI-03 | Fehlende/ignorierte CI-Felder und widersprüchlicher Pflichtfeldkontext | 04:30,32 |
| Datenmodell/Migrationen | CI-05, CI-12 | Softdelete-/Restore-/Merge-/Unmerge-/Referenzvertrag fehlt oder ist abweichend | 04:34,41 |
| Datenmodell/Migrationen | TEN-10, NET-01, NET-02, NET-03, NET-05, NET-08, NET-09 | VRF/VLAN-/Interface-/IP-/Reservierungsmodell, Eindeutigkeit und CRUD unvollständig | 02:43; 04:49–51,53,56–57 |
| Datenmodell/Migrationen | LCY-02, LCY-03 | PG-Transition ohne geladene States; Kontextwerte statt belastbarer Pflichtreferenzen | 04:44–45 |
| Datenmodell/Migrationen | RCK-01, RCK-02, RCK-03 | both-/Kollisions-/Routenvertrag abweichend; Standort-/Historienkopplung fehlt | 04:58–60 |
| Datenmodell/Migrationen | REL-01, REL-02, REL-09 | Typ-/Interface-/Impact-/Constraintvertrag sowie Änderungshistorie unvollständig | 04:61–62,69 |
| Datenmodell/Migrationen | AUD-03, AUD-04 | Historiengranularität/Feldbenennung und Hash-/Commit-/Outboxvertrag abweichend; genauer Soll-Enumvergleich N/P | 06:23–24 |
| Datenmodell/Migrationen | AUD-05, AUD-07, AUD-09 | Request-/Correlation-ID und selbst auditierte Retentionjobs fehlen | 06:25 |
| Datenmodell/Migrationen | AST-02, AST-04, AST-06, AST-07 | Nichtatomare Zuweisung/Reservierung, fehlender kanonischer Asset-/CI-Vertrag und parallele Historien | 09:30,32,34–35 |
| Datenmodell/Migrationen | STK-02, STK-03, STK-04, INV-01 | Globale statt Binsalden, Überreservierung/falsche Buchungssemantik, veränderbares Ledger und Inventur ohne unveränderlichen Snapshotabschluss | 09:37–39,41 |
| Datenmodell/Migrationen | AGT-01, AGT-04 | Keine vollständige signierte Zielplattformdistribution oder produktive Heartbeat-/Policy-/Updatekette | 09:59,62 |
| Datenmodell/Migrationen | NFR-05, NFR-06, NFR-09 | RPO/RTO-/S3-Replikations- und Retention-Matrix unvollständig; Subprozessoren-Dokumentation fehlt, tatsächliche Residenz N/P | 08:58–59,62 |
| Datenmodell/Migrationen | SIM-01 | Demo-/Audit-Ansätze statt vollständigem Zwei-Org-/Scope-/Skalierungsseed; make seed maskiert fehlende Datei | 08:64 |
| Datenmodell/Migrationen | GLO-11, TST-04, DOD-01, GATE-02, GATE-03 | Fehlende Traceability-/DoD-/PASS-/Abnahmedurchsetzung; historische CI scheitert an Linterconfig/Prettier, kein vollständiger Freigabenachweis | 08:31,69–70,73–74 |
| Datenmodell/Migrationen | ABN-03 | Fehlende echte Konkurrenz-/Integritätsmatrix; Go-race und sequenzielle Teiltests ersetzen keine parallele DB-Abnahme | 08:79 |
| Ingest/Reconciliation | PRI-07, COL-08 | Verbindlicher Outbound-/TLS-/Hostname-/Portvertrag nicht durchgängig nachgewiesen | 05:32,41 |
| Ingest/Reconciliation | GLO-12, GLO-13 | Quellzeit/Zukunftsabwehr und Ablehnung reservierter Attributschlüssel fehlen | 05:33; 04:29 |
| Ingest/Reconciliation | COL-01, COL-04, INS-06 | Collectorstatus/-Zertifikat-/Enrollmentvertrag und UI→CLI→API-Durchstich abweichend | 05:34,37; 01:76 |
| Ingest/Reconciliation | COL-06, COL-07 | Update-/Rollback-/Paketvertrag und Agent-Relay-Replay/Writer-Trennung unvollständig | 05:39–40 |
| Ingest/Reconciliation | DIS-01, DIS-02, DIS-03, DIS-04, DIS-05 | Sweep-/Klassifikations-/Plugin-/Protokoll-/Wire-Verträge divergieren | 05:42–46 |
| Ingest/Reconciliation | DIS-09, DIS-10 | Scopes samt Vorschlägen fehlen; Intervall-/Pollingsteuerung abweichend | 05:50–51 |
| Ingest/Reconciliation | REC-01, REC-02, REC-03, REC-04, REC-05 | Kein vollständiger Konsument/Replayvertrag; Matching, Rang und feldweise Quellzeitentscheidung falsch | 05:52–56 |
| Ingest/Reconciliation | REC-06, REC-07, REC-08, REC-09 | Konfliktinformation, Sichtungs-/Review-/Offlinezustandsmodell unvollständig | 05:57–60 |
| Ingest/Reconciliation | REC-11, REC-12, OVR-03 | Getrennte Agentidentität, fehlender Overridekonfliktprozess und ungeschützte Bulk-/Templatepfade | 05:62–63,68 |
| Ingest/Reconciliation | CI-11, CI-13, LCY-05 | Reklassifikation fehlt; Tombstones/CI-Offlinedetektion werden nicht korrekt behandelt | 04:40,42,47 |
| Ingest/Reconciliation | TOP-01, TOP-02, REL-03, REL-04 | Confidence-/Suppression-/Wiedersichtungs-/Cleanupregeln abweichend | 05:64–65; 04:63–64 |
| Ingest/Reconciliation | IMP-09 | Keine vollständige Erzeugung von Versorgungs-/Redundanz-/Outletketten | 04:78 |
| Ingest/Reconciliation | AGT-02 | Prozessmetriken statt vollständigem Inventar, Software im falschen Speichervertrag; Remote-Actions fehlen | 09:60 |
| Ingest/Reconciliation | MON-02, MON-03 | Fehlende CI-ID im SNMP-Upload, Power-Platzhalter und keine geschlossene Poll→Persistenz→Detailchart-Kette; P3-Redfish/Schwellwerte fehlen | 08:33–34 |
| Ingest/Reconciliation | NFR-01, NFR-03 | Identitätssuche nur erste 10.000 CIs, Flush höchstens 16 Batches pro 15-Minuten-Zyklus; Kapazität/Durchsatz ungemessen | 08:54,56 |
| Ingest/Reconciliation | SIM-02, TST-01 | Kein deterministischer Simulator am echten Ingest; vollständige Reconcile-/Provenienz-/Topologie-/Outbox-Sollfallmatrix fehlt | 08:65–66 |
| API/Jobs | TEC-13, TEC-14, OPS-05 | Notifier loggt; gemeinsame zuverlässige Worker-/Scheduler-/Fehlersicht fehlt | 01:62–63,83 |
| API/Jobs | API-05, API-08, API-09 | Ressourcen-/CI-Aktionen, serverseitiger Bulkvertrag und generische Job-API fehlen | 07:30,33–34 |
| API/Jobs | GQL-01, GQL-02, SEC-10 | Eigenparser/BFF-Vertrag, Resolver/Dataloader und Ausführungsgrenzen unvollständig | 07:35–36; 06:31 |
| API/Jobs | TLC-01, TLC-02, TLC-03, INS-04 | Keine vollständige Signup-/Operator-/Einladungs-/Install→Login-Kette | 07:49–51; 01:74 |
| API/Jobs | NTF-01, NTF-03, NTF-04, NTF-05 | SMTP/Provider, Empfänger/Vorlagen/i18n, Zustellungscenter und Baseline-Ereignisketten fehlen | 07:56,58–60 |
| API/Jobs | JOB-01, JOB-02, JOB-03, JOB-04 | Generische persistente Jobs, Recovery, Scheduler, Cancel/Revert und verpflichtende Hintergrundpfade fehlen | 07:61–64 |
| API/Jobs | EVT-01, EVT-02 | NATS-Helfer ohne vollständige produktive Stream-/Publisherverdrahtung | 06:33 |
| API/Jobs | IMP-01, IMP-02, IMP-03, IMP-04, IMP-05, IMP-08 | Falsche Ausfallrichtung/Netzheuristik, fehlende Redundanz-/Pfad-/Mehrfachausfallsemantik | 04:70–74,77 |
| API/Jobs | STK-01, STK-05 | Mindestbestandsfarbe ohne Zustellung; receipt bereits bei Bestellposition statt geprüftem Wareneingang | 09:36,40 |
| API/Jobs | TKT-02, WFL-04 | Fester UTC-Kalender statt Client-/Orgvertrag; fehlende fachliche Workflowketten und Definitions-ID als Run-ID | 09:57,68 |
| API/Jobs | MON-04 | Feste Schwellen statt Regel-AST, orgweite statt CI-bezogener Samples, PG-Updates ohne Tenantkontext und Notifier nur Log | 08:35 |
| API/Jobs | NFR-02, NFR-10, ABN-02 | Impact-Tiefe 10 statt20, N+1-Risiken und fehlende p95-/Plan-/Skalierungs-/Lasttestartefakte; keine erfundenen Messwerte | 08:55,63,78 |
| Frontend | IMP-06 | Typfilter falsch; graphischer Gesamtumfang ohne verlangte 2.000er-Abweisung | 04:75 |
| Frontend | BLK-01 | Promise.all-Einzelrequests statt serverseitigem Bulkjob und Einzel-ID-Fehlerbericht | 07:45 |
| Frontend | INV-03 | Dekorative Balken statt scannerfähigem Code, fehlende org-eindeutige Vergabe/Lookup/Kamera/PDF-Vorlagen | 09:43 |
| Frontend | UI-06 | Explizite Ergänzung ohne Startdialog/Standort-/Typfilter bei >2.000 Knoten und ohne Mehrfachausfallauswahl | 08:41 |
| Frontend | UI-11, UI-13, UI-14, UI-15 | Unvollständige Builder-/Publikations-/Mobil-/Offline-/IGA-/Scanabläufe; technische IDs statt vollständig angebundener Alltagsaktionen | 08:46,48–50 |
| Frontend | UI-16, UI-17 | Review-/Benachrichtigungs-/Jobcenter und durchgängiger Self-Signup-/Onboardingflow fehlen | 08:51–52 |
| Frontend | UI-18, NFR-07 | Gemeinsame Browser-/WCAG-/Tastatur-Abnahmelücke; UI-18 zusätzlich Metamodell-Sprachmaps statt einzelner Strings | 08:53,60 |
| Frontend | TST-03, ABN-01 | Login-/Titel-Mocks statt vollständiger Baseline-/P2-E2E-Abnahme; Originalschritte fehlen, explizite neue Szenarien nicht vollständig abgedeckt | 08:68,77 |

## Querschnittsmuster

| Muster | Betroffene IDs | Gemeinsamer Befund |
|---|---|---|
| Kontext endet an Kanal-/Transaktionsgrenzen | TEN-04/05/06, TEN-09, RBA-03, TEC-06/12, IMP-07, GQL-04, SRC-01/03/04, EXP-01, JOB-01, AST-01, MGT-01/02/03/04/05/06/07/11, TKT-01, AGT-06, WFL-01/02, AI-01, MON-01/04, TST-02 | **Nicht** pauschal fehlendes WITH CHECK: überwiegend vorhanden, jedoch Org-only; Client nur selektiv, Site/Team fehlen. Metriken sind die ausdrücklich belegte Ausnahme ohne RLS; Alertzustandsupdates verlieren den Tenantkontext. Backgroundjobs und Such-/RAG-Projektionen verlieren Aufruferscopes; Objekt-FKs oder Metadatenbesitz beweisen keine Bereichs-/Blobrechte. |
| Mapping-/Bausteinexistenz mit korrekter Durchsetzung verwechselt | RBA-01/02/04/05/06/08, AUT-04, ENT-02/03/04/05, GQL-04, TLC-04, IGA-01/02/03/04, AI-02 | Fail-closed bei unbekannter Route verhindert keine falsch zugeordnete bekannte Aktion; Key-/Session-/Objekt-/Feldrechte sind nicht durchgängig dieselbe Autorität. Gespeicherte Entscheider ersetzen keine zweite Identität; HTTP-Add-on-Gates sichern nicht den Providerindexer. |
| Manual-/Quellzeitvertrag nicht zentral durchgesetzt | GLO-12/13, CI-04/10/13, REC-01/02/03/05/06/07/08/12, OVR-01/02/03, API-07, AGT-05, WFL-02 | Parallelmodelle, fehlende reservierte Namespaces, Serverzeit, falsche Ränge und fehlende Konfliktreviews; Review-"Merge" ist nicht CI-Merge. Agent/Workflow umgehen dieselbe verbindliche Override-/Provenienzentscheidung. |
| Historie/Audit/Events fehlen am tatsächlichen Schreibpfad | AUD-03/04/05/07/09, RCK-03, REL-09, REC-07, EVT-01/02, API-03, AST-02/07, STK-04/05, INV-01, MGT-02, WFL-01/02 | ci_change, entity_change, Audit, NATS und Webhooks bilden keine geschlossene Ereigniskette; Request-/Correlation-ID und Auftragstransaktion fehlen teils. Ledger/Assignments konkurrieren, Bewegungsfehler werden ignoriert und mutable Formdefinitionen verändern historischen Kontext. |
| Einzelworker statt verlässlichem Job-/Zustellungsvertrag | TEC-13/14, OPS-02/05/06, COL-05, BLK-01, API-08/09, NTF-01/03/04/05, JOB-01/02/03/04, AST-06, STK-01, MGT-04, WFL-02/04, IGA-01/02/03/04 | Crash-Recovery/Single-Runner/Cancel fehlen; Log/Statusflag oder Warnfarbe ist kein Versand. Claim-Sperre ersetzt weder Scheduler-Leader noch persistentes Catch-up; RunDue ohne Aufrufer und Definitions-ID statt Run-ID sind keine Workflowausführung. |
| Datenmodell und konsumierender Vertrag divergieren | TEN-10, NET-01/02/03/05/08/09, CI-01/03, RCK-01/02, REL-01/02, LCY-02/03, SRC-01, EXP-01, AST-01/04/05, STK-02/03, AGT-02, TKT-02 | Fehlende VRFs/FKs/Enums, andere Routen-/Fehlercodes, ignorierte Felder und Suchtypen außerhalb DB-CHECK; vorhandene Tabellen reichen nicht. Asset-/CI-Kanonizität, Binsalden, Softwareinventar und Client-/Org-Kalender sind weitere konkrete Vertragsabweichungen. |
| Nichtatomare Prüfung und destruktive Korrektur | AST-02/05/06, STK-03/05, INV-01, MGT-05 | Getrennte Transfer-/Bewegungscommits, Überreservierung und SELECT-vor-INSERT-Rennen; Migration 56 löscht inkonsistente Composition ohne Quarantäne. Kein Beleg, dass jede fehlende Vorbereitung zwangsläufig Datenverlustmigration erfordert. |
| Tests beweisen Ersatzverhalten statt Soll | TEN-09, RBA-04, CI-01, RCK-01/02, IMP-01/03, DIS-05, REC-03/05, TOP-01, GQL-01/02/04, SRC-01/03/04, EXP-01, TLC-04, AST-05/07, STK-04, INV-01, MGT-05, TKT-02, AGT-03, IGA-01/02/03/04, AI-01, TST-01/02/03/04, ABN-01/02/03, NFR-04 | Memory-/SQL-String-/Happy-Path-Tests beziehungsweise Tests falscher Konstanten ersetzen keine echte PostgreSQL-/Kanal-/Negativprüfung. Asset-Append-only prüft nicht stock_movement, UTC-Wochenendtest keinen Orgkalender, Pruningtest keine Verlustfreiheit. Nur vier von 14 echten DB-Testfunktionen werden in CI selektiert; Race ersetzt keine parallele Transaktions-/Constraintmatrix. Setupblockierte Teil-08-Tests sind weder bestanden noch fachlich fehlgeschlagene Assertions; die historischen Teil-09-Nachladeläufe bleiben ein nicht konformer Offline-Nachweis. |
| UI und i18n nicht durchgängig angebunden | REC-10, DIS-08, IMP-06, SRC-03, VIE-02, BLK-01, NTF-03/04, JOB-03, INV-03, AGT-04, WFL-03/04, GLO-10, UI-01–18, NFR-07 | Fehlende Builder/Freigaben/Feldbegründungen/Center, falsche Typfilter und technische IDs; Barcodegrafik, Policyverwaltung oder Workflowliste beweisen keine reale Scanner-/Agent-/Fachkette. Teil 08 belegt 749/749 DE-/EN-Blattschlüssel, null gegenseitig fehlende und null fehlende einfache Literalschlüssel, aber nichtenglische persistierte Enums, technische Anzeigen/deutsche Literale und fehlende Metamodell-Sprachmaps. Keine pauschal fehlende i18n, keine zehn erfundenen UI-v2-Einzelfehler. Browser-/WCAG-Matrix bleibt offen. |
| Installation/Betrieb nicht als vollständige Kette nachgewiesen | TEC-09/15, INS-01/02/03/04/05/06, OPS-01/03/04, SEC-11, TLC-01, AGT-01/03/04/06, AI-02, NFR-05/06/09, GATE-03 | Plattform-/TLS-/Issuer-/Login-/Enrollment-/Backup-Verträge brechen an Übergängen; Healthcheck, Linux-ELF, konfigurierbarer EU-Provider oder vorhandener Workflow bedeutet keinen erfolgreich geprüften Release-/Betriebspfad. RPO/RTO-/Replikations-/Retention-Nachweise und Subprozessoren-Dokumentation fehlen; reale Residenz bleibt N/P. |
| Monitoring endet zwischen Payload, Persistenz und Wirkung | MON-01/02/03/04, TEC-06/13, NTF-01 | Fehlende ci_id verletzt NOT NULL, Stundenabfrage nutzt alten Viewnamen, Power liefert Platzhalter. Konfigurierbares Polling/Log-Notifier ersetzt weder echte Detailcharts noch CI-spezifische Schwellwert-/Health-/Versandkette. |
| Strukturelle Grenzen statt skalierter Sollnachweise | NFR-01/02/03/04/10, SIM-01/02, ABN-02, OPS-06, COL-05 | 10.000-CI-Suchhorizont, Impact-Tiefe zehn statt20, höchstens 16 Batches pro 15-Minuten-Zyklus und stilles 1-GiB-Spoolpruning widersprechen expliziten Zielen. Kein allgemeines DB-Speicherlimit oder gemessener Latenz-/Durchsatzfehler behauptet; vollständiger Seed/Simulator/Lastbericht fehlt. |
| Dokumentierte Regeln sind keine Freigabeartefakte | GLO-11, DOD-01, SEQ-01, GATE-01/02/03/04/05, NFR-10, TST-04 | Traceability-Datei/Check und vollständige PASS-/Gate-/Abnahmenachweise fehlen. Historische Produkt-CI ist rot; grüne Agentworkflows sind kein Produkt-Green. N/P der Prozesshistorie und Ausschluss von [O] im Audit sind kein PASS oder positive G1-Empfehlung. |

## Traceability

Die verlangte `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/traceability.csv` fehlt. Schon Teil 0 dokumentiert das Fehlen (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-bestandsaufnahme.md:210–225`); die Pfadkontrolle wurde wiederholt. Eine Audit-CSV ersetzt weder Epic-/Testdateizuordnung noch CI-Durchsetzung nach GLO-11. Dateizeilen/-inhalt sind mangels Datei nicht prüfbar, nicht als „vorhandene leere CSV“ beschrieben.

**261 von 261 bekannten [B]/[Pn]/[A]-Quell-IDs ohne Eintrag: 224 bisherige plus 37 aus Teil 08.** GLO-10 ist in diesen 37 eine echte Bericht-/CSV-Zeile und wird nicht zusätzlich gezählt. Die frühere 225er-Liste hatte GLO-10 bereits außerhalb der Ergebnistabellen enthalten; ihr Nettozuwachs beträgt daher **36**, nicht37. **TKT-03 [O] ist ausgeschlossen**. Nicht mitgezählt: 30 ungetaggte v2-IDs, PRI-07, die 16 Teil-06-IDs ohne bestätigtes Tag sowie reine [Q]/[O]-IDs. Die 13 führenden [Q]-IDs aus Teil 08 liegen außerhalb dieses speziellen CI-Abdeckungsnenners; [Q] bleibt gleichwohl dokumentationspflichtig und Qualitätsmaßstab nach GLO-11/DOD-01. **ABN-01 bleibt eine führende [Q]-ID:** ihre expliziten [B]/[P2]-Teilszenarien sind weder zusätzliche IDs noch ein zusätzlicher Traceability-Haupttag. TEC-06 zählt wegen zusätzlichem [B] zur verpflichtenden CI-Menge.

Vollständige Liste der bekannten fehlenden Einträge:

| Herkunft | Anzahl | IDs |
|---|---:|---|
| Teil 01 | 18 | PRI-11, TEC-06, TEC-09, TEC-12, TEC-13, TEC-14, TEC-15, REP-01, INS-02, INS-03, INS-05, INS-06, INS-07, OPS-01, OPS-02, OPS-03, OPS-04, OPS-05 |
| Teil 02 | 35 | TEN-01, TEN-02, TEN-03, TEN-04, TEN-05, TEN-06, TEN-07, TEN-08, TEN-10, AUT-01, AUT-02, AUT-03, AUT-04, AUT-05, AUT-06, AUT-07, AUT-08, AUT-09, AUT-10, RBA-01, RBA-02, RBA-03, RBA-04, RBA-05, RBA-06, RBA-07, RBA-08, ENT-01, ENT-02, ENT-03, ENT-04, ENT-05, ENT-06, ENT-07, ENT-08 |
| Teil 04 | 52 | GLO-13, CI-01, CI-02, CI-03, CI-04, CI-05, CI-06, CI-07, CI-08, CI-09, CI-10, CI-11, CI-12, CI-13, LCY-01, LCY-02, LCY-03, LCY-04, LCY-05, LCY-06, NET-01, NET-02, NET-03, NET-04, NET-05, NET-06, NET-07, NET-08, NET-09, RCK-01, RCK-02, RCK-03, REL-01, REL-02, REL-03, REL-04, REL-05, REL-06, REL-07, REL-08, REL-09, IMP-01, IMP-02, IMP-03, IMP-04, IMP-05, IMP-06, IMP-07, IMP-08, IMP-09, IMP-10, IMP-11 |
| Teil 05 | 35 | GLO-12, COL-01, COL-02, COL-03, COL-04, COL-05, COL-06, COL-07, COL-08, DIS-01, DIS-02, DIS-03, DIS-04, DIS-05, DIS-06, DIS-07, DIS-09, DIS-10, REC-01, REC-02, REC-03, REC-04, REC-05, REC-06, REC-07, REC-08, REC-09, REC-10, REC-11, REC-12, TOP-01, TOP-02, OVR-01, OVR-02, OVR-03 |
| Teil 07 | 39 | API-01, API-02, API-03, API-04, API-05, API-06, API-07, API-08, API-09, GQL-01, GQL-02, GQL-03, GQL-04, SRC-01, SRC-02, SRC-03, SRC-04, VIE-01, VIE-02, BLK-01, BLK-02, EXP-01, IMP-IO-01, TLC-01, TLC-02, TLC-03, TLC-04, TLC-05, TLC-06, TLC-07, NTF-01, NTF-02, NTF-03, NTF-04, NTF-05, JOB-01, JOB-02, JOB-03, JOB-04 |
| Teil 08 | 37 | GLO-10, MON-01, MON-02, MON-03, MON-04, UI-01, UI-02, UI-03, UI-04, UI-05, UI-06, UI-07, UI-08, UI-09, UI-10, UI-11, UI-12, UI-13, UI-14, UI-15, UI-16, UI-17, UI-18, NFR-01, NFR-02, NFR-03, NFR-04, NFR-05, NFR-06, NFR-07, NFR-08, NFR-09, SIM-01, SIM-02, TST-01, TST-02, TST-03 |
| Teil 09 | 45 | AST-01, AST-02, AST-03, AST-04, AST-05, AST-06, AST-07, STK-01, STK-02, STK-03, STK-04, STK-05, INV-01, INV-02, INV-03, MGT-01, MGT-02, MGT-03, MGT-04, MGT-05, MGT-06, MGT-07, MGT-08, MGT-09, MGT-10, MGT-11, MGT-12, TKT-01, TKT-02, AGT-01, AGT-02, AGT-03, AGT-04, AGT-05, AGT-06, WFL-01, WFL-02, WFL-03, WFL-04, IGA-01, IGA-02, IGA-03, IGA-04, AI-01, AI-02 |
| **Bekannte Gesamtmenge** | **261** | Keine doppelte ID; zusätzliche IDs aus Teil 03 nicht bestimmbar |

Für Teil 03 lässt sich keine vollständige Soll-ID-Menge ableiten; Teile 08/09 sind ID-genau berücksichtigt. Statische Suche nach Traceability-Durchsetzung in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/.github/workflows/` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/Makefile` findet keine entsprechende Prüfung; vorhandene Test-/Parity-Jobs ersetzen sie nicht. **GLO-11 ist jetzt eine echte [Q]-Ergebniszeile: FAIL/High** (08:31). Teil 08 hat den zuletzt aufgelisteten historischen Produkt-CI-Lauf 35772333628 geprüft: Gesamtstatus failure, nicht auf dem aktuellen Auditcommit; Details unten.

## N/P und offene Nachweise

Nach Bereinigung **39 vollständige N/P-Zeilen**: 38 Quell-N/P-IDs plus SEC-05 (K1); gegenüber der vorigen Konsolidierung fünf neue aus Teil 08.

| IDs | Grund | Zur Klärung benötigt |
|---|---|---|
| PRI-01, PRI-02, PRI-03, PRI-04, PRI-05, PRI-06, PRI-08, PRI-09 | Nur „unverändert aus v2“, kein Originalwortlaut/Tag | Verbindlicher v2-Text einschließlich Tags; Grundlagen:78 |
| GLO-01, GLO-02, GLO-03, GLO-04, GLO-05, GLO-06, GLO-07, GLO-08, GLO-09 | Nur „unverändert“ | v2-Regeln/Tags; Grundlagen:97 |
| TEC-01, TEC-02, TEC-03, TEC-04, TEC-05, TEC-07, TEC-08, TEC-10, TEC-11 | Stackvorgaben nicht aus vorhandenem Code rekonstruierbar | v2-Stackanforderungen/Tags; Teil 01:50–58 |
| REP-02, REP-03, REP-04, REP-05 | Strukturvertrag nur referenziert | Vollständiger v2-Strukturwortlaut/Tags; Teil 01:67–70 |
| AUT-05, AUT-06 | Originalauthentifizierungsanforderungen fehlen | v2-Text; [B]-Tag bereits bekannt; Teil 02:48–49 |
| NET-04 | cable „unverändert“, vorhandenes CRUD ersetzt keinen Sollfeldvertrag | v2-Kabelmodell/-Regeln; Teil 04:52 |
| SEC-05 | Spezifische Vaultpflicht und Phase nur aus CH16(V); ursprüngliche ABWEICHEND/Critical-Bewertung normativ nicht abgesichert | Beschlossener SEC-05-Originalvertrag, Bestätigung/Alternative zu CH16, PII-/Retention-/Löschkonzept; Teil 06:27 und K1 unten |
| NFR-08 | Ausschließlicher Verweis auf fehlenden SEC-11-Originalvertrag, frühere Auditinterpretation keine Ersatznorm | Verbindlicher SEC-11-Originalwortlaut einschließlich Sicherheits-/Abnahmekriterien; Teil 08:61 |
| SEQ-01 | Roadmap vorhanden, tatsächliche Umsetzungs-/Freigabereihenfolge unbekannt | Chronologische Epic-/Gate-Abnahmebelege; spätere Moduldateien allein beweisen keinen Verstoß; Teil 08:71 |
| GATE-01 | Tatsächliche Gatefreigabe/Bereinigung nicht prüfbar; belegte Critical-Befunde schließen positive G1-Empfehlung aus | Gatebezogener Critical-Bestand und dokumentierte Freigabe-/Bereinigungsentscheidungen; Teil 08:72 |
| GATE-04 | GA-Reviews nicht nachgewiesen, kein zusätzlicher G1-Blocker allein wegen Add-ons | Freigegebener Connector-Sicherheitsreview und KI-Provider-Prüfnachweis; Teil 08:75 |
| GATE-05 | Historische Gatebeeinflussung durch [O] nicht prüfbar; eigene Auditregel kein Prozess-PASS | Tatsächliche phasenbezogene Gateentscheidungen/Regressionstests mit optionaler Abgrenzung; Teil 08:76 |

„Grundlagen“ ist `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md`; „Teil“ verweist auf die absoluten Berichtspfade in der Datengrundlage. N/P ist kein Beleg für fehlende Implementation und kein zulässiger Ersatz für PASS.

**Nicht ganze IDs, sondern weiterhin N/P-Unteranteile:** REP-01/INS-/OPS-Betriebsnachweise (v2-Rest, reale Plattformen, EU-Lokation, Restore); TEN-09 (v2-Rest und vollständige RLS-Laufzeitmatrix); COL-02/DIS-04 (referenzierter Credential-/Protokollrest, physische Geräte); AUD-01/02 (exakte Kontaktrollen), AUD-03 (Soll-Enum), AUD-05/07/09 (v2-spezifische Fristdetails, soweit nicht durch die jetzt explizite NFR-06-Matrix festgelegt); API-01 (Releasehistorie/Sunset), API-03 (v2-Fehlertabelle); SEC-10/GQL-01 (Originalgrenzen), GQL-04 (vollständiger RBA-07-Feldrechtsvertrag), IMP-IO-01 (referenzierter SEC-09-Dateivertrag). Benötigt werden jeweils Originalanforderungen beziehungsweise echte Integrations-/Releaseartefakte, keine Rekonstruktion aus Mocks oder früheren Auditbehauptungen.

**Neue N/P-Unteranteile aus Teil 09, keine zusätzlichen vollständigen N/P-Zeilen:** IGA-01–04 (Original-v2-Einzelverträge jenseits des ausdrücklichen gemeinsamen Gruppenabsatzes), WFL-03 (unbekannte v2-Beispiele), MGT-01 (SEC-09-Detailtext), AST/WFL (nur referenzierte DB-05-/MET-45-Details), MGT-02 (konkrete BSI-/ISO-Norm/Abnahmekriterien), AI-02 (tatsächliche EU-/Self-Hosting-/Nichttraining-Verträge), AGT-01/03/04/06 (Zielplattform-/Enrollment-/Transportabnahme) sowie echte DB-/Scope-/Konkurrenz-/Serviceketten. AST-05 belegt den destruktiven Migrationspfad, nicht tatsächlich gelöschte Produktionsdaten. Keine fehlenden Originalpflichten aus vorhandenen Mocks rekonstruieren.

**Neue N/P-Unteranteile aus Teil 08, keine weiteren vollständigen N/P-Zeilen:** UI-01–10 (individuelle v2-Verträge, UI-06-Ergänzung ausdrücklich prüfbar), ABN-01 (34-/20-Schritt-Originale, nicht die expliziten neuen Szenarien), NFR-01–07/09 (tatsächliche Kapazitäts-/Latenz-/Durchsatz-/Uptime-/RPO-/RTO-/Retention-/Browser-/Residenznachweise) und DOD-01 (PR-Vollständigkeit, ein Paket pro PR, Paketdauer). Die Sollzahlen aus NFR-01–07 sind bekannt und werden nicht mehr als fehlender Teil-08-Vertrag bezeichnet.

Teil 03 hat eine **unbekannte fehlende ID-Menge**; das sind weder zusätzliche gezählte N/P-Zeilen noch Null-Anforderungen. In Teil 06 fehlen für 15 IDs explizite Tags, SEC-05 hat nur eine Phasenangabe aus CH16(V); auch bei PRI-07 ist kein Tag angegeben. Tags und einzelne AUD-/EVT-Verträge müssen nachgeliefert werden, gemeinsame Quellbewertung ist noch kein Einzeltest.

## Offene Katalogentscheidungen

Grundlage ist der Entscheidungswortlaut unter `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md:15–59`. Ein offener Vorschlag blockiert die **abschließende Spezifikation/Abnahme seines Details**, nicht automatisch G1; eindeutige verbindliche Gegenbeispiele bleiben unabhängig davon bestehen.

| Entscheidung | Offene Festlegung / betroffene Befunde |
|---|---|
| CH15 (S mit ausdrücklichem Bestätigungsvorbehalt) | Interpretation „1000-1000“ → 1.000–10.000/50.000-Headroom weiterhin bestätigen. **Die ausdrücklich gelieferten NFR-01-Kardinalitäten sind davon unabhängig verbindlich**, ebenso NFR-02/03 und der skalierte SIM-01-Seed für NFR-10. Fehlende Lastmessung ist kein unbekannter Sollwert und kein bestandener Lasttest. |
| CH30 / DIS-02 (V) | Verbindliche Top-20-Familien/Varianten und Abnahmedatensätze; Teil 05 nennt 23 Gruppen im Vorschlag. Die fehlende Klassifikationskette bleibt unabhängig davon ABWEICHEND. |
| ENT-06 (V) | Pakete, Limits, Trialwerte und Phasenmatrix bestätigen. **CH14-Add-on-Trennung ist bereits verbindlich**: ENT-06/API-06 bleiben deswegen High, nicht wegen nicht umgesetzter Vorschlagszahlen. |
| CH16 (V) | Vault-/Surrogatmodell, Phase-1-Umfang, Crypto-Shredding und Erhalt der Hashkette bestätigen; SEC-05 sowie AUD-01/02 benötigen Originalvertrag. Bereits persistierte Klartextattribute sind eine verifizierte Tatsache, aber der spezifische Architekturvorschlag ist nicht dadurch beschlossen. Die ausdrücklichen STK-04-/AI-02-Surrogatpflichten aus Teil 09 gelten separat; sie beschließen nicht den gesamten exklusiven Vaultentwurf und heben K1/K2 nicht auf. |
| TKT-03 [O] / CH17 | Der neue Einzeltext taggt das Providerinterface optional, CH17 nur konkrete Integrationen. Pflicht-/Tagabgrenzung bestätigen; TKT-03 bleibt bis dahin OFFEN ohne Gatewirkung und ohne verpflichtenden Traceability-Zähler. |
| CH21: Limitüberschreitung nur „(V, analog)“ | Den Analogievorschlag als Entscheidung bestätigen; **nicht** die separat berichtete ENT-03-Pflicht dadurch suspendieren: Teil 02:450 fordert unlicensed_ci ausdrücklich unabhängig von CH21(V). Zusätzliche Detail-/Retentionfestlegungen klären (ENT-03/08). Verbindlich bleibt außerdem: Ablauf stoppt ausschließlich Discovery/Ingest; andere Funktionen verfügbar (ENT-07/API-04). |
| CH26 (V) | Realm-/Org-Attribut-/Brokering-/MFA-Zielmodell bestätigen; AUT-01/09 und SEC-07 betreffen daneben konkrete bereits belegte Risiken. Vorschlagsstatus entschuldigt weder ein bekanntes Demo-Administratorkonto noch wirkungslose Deaktivierung. |
| CH27 (V) | Impact-Richtung/Strukturprojektion bestätigen; REL-01, IMP-01/02/03/04/10. Explizite Einzelanforderungen und falsche vorhandene Ausfallberechnung bleiben eigenständig zu bewerten. |
| CH28 (V) | Gemeinsamen Location-Baum/LOC-10 bestätigen und Teil 03 nachholen; CI-01, RCK-03, SRC-01. CH12-Assethoheit und Scope-Isolation sind keine optionalen Folgen dieser Entscheidung. |
| CH29 (V) | Hostname-Reviewdetails bestätigen; REC-02/CI-13. PRI-10 fordert bereits belastbaren Identitätsbeweis, daher kein bloßes Vorschlagsproblem. |
| NTF-02 (V-Provider), TLC-01/ENT-07 (V-Trialwerte) | Konkrete SMS-/Pager-Provider und Trialparameter festlegen; späterer Kanalumfang nicht G1-fällig. CH23-Signup und CH24-E-Mail-Baseline sind verbindlich. |

Zusätzlich fehlen bestätigte v2-Übernahmen und einzelne Querverweisverträge sowie dokumentierte Gate-Phasenzuordnung der ungetaggten Teil-06-IDs. DOD-01, SEQ-01, GATE-01–05 und GLO-10/11 sind jetzt echte Ergebniszeilen aus Teil 08. DOD-01 ist PARTIAL, GLO-11/GATE-02 FAIL, GATE-03 PARTIAL; SEQ-01/GATE-01/04/05 bleiben prozessbezogen N/P. Die spätere Auslieferungsreihenfolge verlangt zuerst belastbare Isolation/Auth/Datengrundlagen und den Ingestpfad; zusätzliche Moduloberflächen ersetzen G1-Nachweise nicht.

## Berichtswidersprüche und Konsolidierungskorrekturen

Keine Anforderungs-ID steht doppelt in den vorhandenen Ergebnistabellen. Widersprüche betreffen daher überwiegend gemeinsame Quellstellen, Prüfvoraussetzungen oder unterschiedliche Teile desselben Ablaufs. Der ursprüngliche Vergleich der Produktpfade zwischen `b3fe0390056cde41441efca8d870e644c531e1bd` und der Prüfkopie `d8f35e05f891d7c950f9e19824221a095807493c` ergab keine Änderungen; auch die Fortsetzungen über `b7e747a6181cad553e529a2a4d7aa7d1582a7743` zum aktuellen Referenzstand `abda3bff7d03b91826df635c2b80fc2c31e95005` ändern keinen Produktcode. Unterschiedliche Bewertungen oder zusätzliche Befunde sind kein belegter Produktfortschritt.

| Vermerk / IDs | Quelle → konsolidierte Bewertung | Nachprüfung und Begründung |
|---|---|---|
| **K1 – SEC-05** | ABWEICHEND/Critical → **N/P/–**; Phase unbestätigt | Teil 06:27 behandelt den Vaultvertrag als bestätigt, Teil 07:266,297 CH16 als Vorschlag. Grundlagen:31 nennt ausdrücklich **(V)**, separat beschlossener SEC-05-Originaltext fehlt. Technisch bleiben vollständige Snapshots/Attribute in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/pg_repository.go:293–296,390–409,611–631,673`, Klartext-JSONB in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/audit/audit.go:166–186` und begrenzte Löschsenken in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/privacy/privacy.go:124–152` belegt. Das beweist weder tatsächlich gespeicherte Produktions-PII noch unberechtigte Exfiltration oder einen beschlossenen Vaultzwang. **Bedingtes Datenschutzrisiko bleibt offen**, ursprüngliche Critical-Einstufung im CSV-Befund dokumentiert, nicht als bestätigter G1-Blocker gezählt. |
| **K2 – AUD-01, AUD-02** | ABWEICHEND/High → **PARTIAL/–** | Kontakt-/Linkmodell tatsächlich vorhanden: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000016_contacts.up.sql:4–26`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/contact/pg_repository.go:109–151`. Die gemeinsame High-Abweichung aus Teil 06:22 beruht auf derselben unbestätigten exklusiven Vaultpflicht. Exakte Sollrollen unbekannt, daher kein PASS; kein davon unabhängiger High-Defekt bewiesen. Originalratings und gemeinsame Prüfbasis bleiben in der CSV sichtbar. |
| **K3 – SEC-10; Abgleich GQL-01** | PARTIAL/High bleibt, numerisches Soll **N/P** | Teil 06:31 nennt Tiefe10/10s, Teil 07:35,266 erklärt SEC-10-Originalgrenzen als fehlend; der vorhandene Katalog verweist nur auf „Limits nach SEC-10“. Eigenparser und fehlende entsprechende Ausführungsgrenzen sind in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/graphqlbff/graphqlbff.go:142–198,321–371` belegt. **10/10s nicht als bestätigten Sollwert übernehmen**; CSV-Vermerk K3 korrigiert die normative Aussage, nicht den technischen Befund. |
| RBA-06 versus CI-10/OVR-01/API-07 | High versus Critical bleibt | Gemeinsame Override-Löschautorisierung ist bereits in Teil 04:128 als High-Unterbefund bezeichnet. Critical betrifft darüber hinaus stilles Überschreiben effektiver manueller Daten; keine automatische Hochstufung aller beteiligten IDs. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/authz.go:181–248` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:333–356,753–790`. |
| AUT-10 versus API-04 | High versus Critical bleibt | Ratelimitlücke allein ist nicht die zusätzliche orgweite Idempotenzantwort vor der Autorisierung; unterschiedliche Teilsachverhalte, keine widersprüchliche Ratingkorrektur. Teil 07:29 und Detail API-04. |
| JOB-01 versus EXP-01 | High versus Critical bleibt | Jobvertrag/Crash-Recovery ist nicht derselbe Befund wie Exportscopeverlust und zu breite Ergebnisberechtigung. Teil 07:47,61; kein Herunterstufen des Scopeproblems auf bloße Job-Unvollständigkeit. |
| API-08 versus BLK-01; API-09 versus JOB-03 | FAIL versus ABWEICHEND/PARTIAL bleibt | Generische Server-Bulk-/Job-API fehlt, UI-Fan-out/Fachjoblisten bestehen: unterschiedliche geprüfte Objekte. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/CIListPage.tsx:71–89`; Teil 07:33–34,45,63. |
| TEN-05/06 versus vorhandene WITH CHECK; Fail-closed-Router versus RBA-04 | Präzisierung, kein Statuswechsel | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000033_client_scope_rls.up.sql:64–73`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000030_search_ai.up.sql:65–67`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000035_export_job_formats.up.sql:13–22` haben Policies. Fehlende Scopeprädikate/NULL-/Systemausnahmen bleiben der konkrete Mangel. Vollständige Routenabdeckung ist nicht korrekte Aktionszuordnung. |

**Abgleich mit Teil 09:** TEN-06/MGT-01 betreffen denselben Blobpfad, OVR-02/WFL-02 denselben direkten Workflow-Schreibpfad und REC-11/AGT-05 denselben separaten Agentmatcher. AST-01/MGT-11 und MGT-05/06 teilen jeweils die genannte Scopeursache; vier IGA-Ratings erben nur die gemeinsame ausdrückliche Prüfbasis, nicht vier unabhängige Ursachen oder vier rekonstruierte v2-Verträge. STK-04 verlangt Nutzersurrogate, AI-02 deren Nichtauflösung im Prompt; diese eigenständigen Pflichten ersetzen den fehlenden SEC-05-/AUD-01/02-Originalvertrag nicht. **K1 bleibt N/P, K2 PARTIAL, K3 mit N/P der numerischen Grenzen.**

**Abgleich mit Teil 08:** MON-01/TEC-06 betreffen denselben Metrik-Isolationskomplex; NFR-04/OPS-06/COL-05 denselben stillen Spoolverlust. UI-06/IMP-06, UI-16/REC-10/JOB-03/NTF-04 und UI-17/TLC-01/INS-06 konkretisieren gemeinsame Bedien-/Integrationslücken; UI-18/NFR-07 teilen Browser-/Accessibility-Kriterien. UI-01–10 erben nur den ausdrücklichen Gruppenvertrag, UI-06 zusätzlich die eigene Erweiterung. **NFR-02-Tiefe20 gilt für Impact, nicht für SEC-10/GraphQL; K3 bleibt unverändert.** Die jetzt bekannte NFR-06-Retention-Matrix präzisiert Sollfristen, beschließt aber weder CH16(V) noch den fehlenden SEC-05-/AUD-01/02-Originalvertrag: **K1/K2 bleiben unverändert.** NFR-08 kann aus dem älteren SEC-11-Auditbefund keinen fehlenden Originalvertrag ableiten. GATE-01/04/05-Prozess-N/P ist weder Gate-PASS noch eine Entkräftung von GATE-02 FAIL.

Historische Teilberichte 01–07 und 09 bleiben unverändert; K1–K3 sind in der CSV am betroffenen ID-Befund markiert. **Alle 279 bisherigen CSV-Zeilen bleiben bytegetreu erhalten.** GLO-10 (ABWEICHEND/Medium) und GLO-11 (FAIL/High) werden als zwei der 50 tatsächlichen Teil-08-Quellzeilen importiert, keine synthetischen Extrazeilen. Der bisherige zusätzliche Traceability-Verweis auf GLO-10 entfällt zugunsten seiner regulären Teil-08-Zuordnung.

## Stand

**UNVOLLSTÄNDIG – Gesamtprüfung fortsetzen mit Teil 03. Teile 08 und 09 sind VOLLSTÄNDIG bearbeitet und konsolidiert.** Eine genaue erste fehlende ID ist ohne den ausstehenden Bericht und vollständige Katalogtexte nicht belastbar bestimmbar.

Die Konsolidierung der **329 vorhandenen Einzel-IDs** ist inhaltlich abgeschlossen; keine dieser IDs wurde ausgelassen. Die bisherigen 279 CSV-Zeilen einschließlich K1–K3 bleiben unverändert, die 50 Teil-08-Zeilen wurden ergänzt. Offen bleiben Teil 03, 39 N/P-IDs, benannte Teiltext-/Prozess-/Laufzeitnachweise und die Bestätigung offener Katalogentscheidungen. Nach Ergänzung CSV, Gesamtquoten, Traceability-Menge und Gates erneut konsolidieren; G1 bleibt bis zum Nachweis aller Kriterien gesperrt.

### Historischer Teststand der Teil-09-Fortsetzung

Die früheren Testbefunde werden **nicht** durch neue grüne Läufe überschrieben. Teil 09 dokumentiert die tatsächlichen Ausführungen und ihre Grenzen unter `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/09-module-phase2plus.md:187–203`:

- Initialer Offlineversuch setupblockiert (`module lookup disabled by GOPROXY=off`); dessen Log wurde vom Wiederholungslauf überschrieben, ursprünglicher Exitcode/vollständiges Erstlog nicht belastbar erhalten.
- Der delegierte Testausführer entfernte entgegen Auftrag die Offline-/Readonly-Vorgaben und lud **37 vorhandene Go-Modulversionen** nach (37 verschiedene Downloadzeilen, sieben beim Backendtest, 30 beim Build; unabhängig aus den erhaltenen Rohlogs bestätigt). Das verletzte das Installations-/Nachladeverbot; der Nutzer wurde informiert, weitere Läufe gestoppt. Keine neuen Dependency-/go.mod-/go.sum-/Lockfiledateien oder Produkt-/Teständerungen.
- Danach Backend: **18 von 21 Paketen `ok`**, reservation/movement/disposal ohne Tests; Agent-/Server-Build erfolgreich laut Ausführer. Wiederholte Agent-/Form-/Workflow-Detailfälle werden nicht als zusätzliche Pakete gezählt.
- Edgecore: fünf Pakete `ok`; Collector: acht Testfälle erfolgreich. Diese Komponententests liefen im abweichenden Kontext und beweisen weder einen konformen Offline-Gesamtlauf noch die Agent→Relay→Backend-Kette.
- Keine echte `TEST_DATABASE_URL`-/DB-/Serviceabnahme, keine Frontend-Testausführung und keine nachgewiesene Zielplattform-, Last-, Pentest- oder vollständige Isolationstestabnahme. Grüne Unit-/Memorytests sind kein PASS einer ganzen Anforderung; ein Setupfehler ist keine fehlgeschlagene fachliche Assertion.

### Testinventar und Offline-Läufe aus Teil 08

Quelle für das vollständige Inventar, die konkret nicht selektierten DB-Funktionen und tatsächlichen Läufe: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/08-frontend-monitoring-nfr-tests.md:218–258`. Die folgenden statischen Zählungen sind **keine Ausführungsergebnisse**:

| Bereich | Dateien | Fälle/Funktionen |
|---|---:|---:|
| Backend | 99 Go-Testdateien | 395 Top-Level-Testfunktionen |
| Collector | 11 Go-Testdateien | 123 Top-Level-Testfunktionen |
| Edgecore | 5 Go-Testdateien | 58 Top-Level-Testfunktionen |
| Vitest | 33 | 103 Deklarationen; 102 einfache und ein it.each mit zwölf Datensätzen, 114 statisch expandierte Fälle |
| Playwright | 3 | 5 Fälle: vier gemockte Loginfälle, ein Titel-Smoke |
| Installer-Helfer | 1 Shell-Testdatei | 7 aufgerufene Szenarien |

Insgesamt 115 Go-Testdateien/576 Top-Level-Funktionen, Untertests nicht erneut addiert. Überlappende Backend-Kategorien (58 NewMemory, 48 httptest, 16 handler, 13 repository im Dateinamen) sind nicht zu summieren. Discovery: sieben Dateien/33 Funktionen, darin acht direkte Reconcile-Testfunktionen/zehn Aufrufe; auch 17 angrenzende Funktionen sind kein Mindest-20-Sollfallnachweis. Der SNMP-Trap-Mikrobenchmark ist kein NFR-Lasttest.

Von **14 echten DB-Testfunktionen in acht Dateien** selektiert CI nur TestMigrationsApplied, TestTenantIsolationRLS, TestClientScopeRLS und TestPoolRefusesRLSBypassingRole, zusätzlich den statischen TestMigrationsAreSorted. Die übrigen zehn DB-Funktionen stehen einzeln in Teil 08:235–242. Im normalen Backendjob fehlt TEST_DATABASE_URL; ein separater Restore-Workflow ist keine vollständige PR-Matrix. Backend-`./...` deckt nicht automatisch Collector/Edgecore ab.

Teil 08 verwendete Go 1.25.14 mit **`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly`**, ohne TEST_DATABASE_URL, Dienststarts, Downloads oder Installationen. Diese Schutzflags wurden nicht entfernt; das hebt den historischen Teil-09-Vorfall nicht auf.

| Lokaler Teil-08-Lauf | Tatsächliches Ergebnis | Grenze |
|---|---|---|
| Backend: 13 gezielte Pakete, `go test -short -count=1` | Zwei Pakete erfolgreich (config, platform/crypto), elf Setupfehler; Exit 1 wegen fehlender Offline-Auflösung chi/v5@v5.3.1 | Kein fachlicher Assertionsfehler bewiesen; insbesondere Monitoring-/Servertests nicht ausgeführt |
| Backend: Build monitoring/config/discovery/topology | Exit 1, dieselbe Offlineblockade | Kein erfolgreicher Bereichs-/Gesamtbuild |
| Collector: `go test -short -count=1 ./...` | Exit 0; zehn Testpakete erfolgreich, zwei ohne Tests | Komponenten-, kein vollständiger Ingest→Dashboard-Nachweis |
| Edgecore: `go test -short -count=1 ./...` | Exit 0; fünf Testpakete erfolgreich | Keine 24h-/Last-/Plattformabnahme |
| Frontend / golangci-lint | Nicht ausgeführt; node_modules beziehungsweise Linter fehlen | Keine Installation und kein Vitest-/Playwright-/axe-/Browser-/Lint-PASS |

Die i18n-Inventur (Teil 08:199–216) zählt 749/749 DE-/EN-Blattschlüssel, je null gegenseitig fehlende sowie 689 verschiedene einfache `t(...)`-Literale an 969 Fundstellen, davon null im Katalog fehlend. Dynamische/Template-/Plural-/Kontextauflösung und Übersetzungsqualität sind damit nicht geprüft; technische Anzeigen/Enums und Metamodell-Sprachmaps bleiben konkrete Abweichungen.

### Beobachtete historische Produkt-CI aus Teil 08

GitHub-MCP-Abfrage vom 2026-10-01, Quelle Teil 08:260–268: letzter aufgelisteter `ci.yml`-Lauf [35772333628](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/35772333628) vom **2026-09-22**, main `895336da05c2547b0078b262d89a9a6b42d0b7b7`, Gesamtstatus **failure**. Kein Lauf auf dem aktuellen Auditcommit; erfolgreiche „Running Copilot cloud agent“-Workflows sind **keine Produkt-CI-Abnahme**.

- Backendjob 106896848433: Build, Race-/Coverage-Tests und Vet erfolgreich; Kustomizejob 106896848629 ebenfalls erfolgreich.
- Migrationsjob 106896848083: **Up/Down/Up erfolgreich**, danach PostgreSQL-Integrationstestschritt fehlgeschlagen; beide nachgelagerten RLS-Assertions übersprungen. Konkrete Assertionursache aus den gelesenen Logenden nicht belastbar ableitbar.
- lint-backend 106896848447: golangci-lint v2.13.2 über `latest`, Konfiguration mit „unsupported version of the configuration: ""“ abgewiesen, **Exit 3**; kein durchgelaufener Lint.
- Frontendjob 106896848597: ESLint erfolgreich, **Prettier 18 Dateien/Exit 1**; API-Generierungscheck, Typecheck, Vitest und Build anschließend übersprungen. E2E-Job 106897043774 übersprungen.
- Kein Workflow-Retry und keine Produkt-/Workflowänderung veranlasst. Jobexistenz, Teil-Green und grüne Agentworkflows ersetzen weder vollständige Testauswahl noch eine erfolgreiche Produktabnahme.

### Prüfung der Konsolidierung

- Quelltabellen nach Einzel-IDs aufgelöst: **329**, keine Dubletten; CH16 keine eigene Zeile. Die bisherigen 279 Zeilen samt K1/K2-Statuskorrekturen und K3-Sollwertpräzisierung bytegetreu erhalten; 50 neue Zeilen entsprechen der Teil-08-Quelltabelle einschließlich Evidenz, Befund, Tags und absoluten Berichtszeilen.
- Critical-/High-Inventar gegen die Einzel-ID-Menge abgeglichen: **49/176**, jede ID genau einmal in der jeweiligen priorisierten Tabelle und in der geforderten Fundament-Reihenfolge; neu zwei/32.
- Pfadkontrolle: Nur Teilbericht 03 und die Traceability-Datei fehlen; Teile 08 und 09 sind vorhanden.
- Katalogabgleich: **261** tatsächliche [B]/[Pn]/[A]-Quell-IDs = 224 bisherige +37 aus Teil 08. GLO-10 regulär enthalten, keine synthetische Extrazeile; GLO-11 echte FAIL/[Q]-Zeile außerhalb dieses speziellen Nenners, TKT-03[O] ausgeschlossen. 13 führende [Q]-IDs aus Teil 08 einschließlich ABN-01 nicht zusätzlich als [B]/[Pn]/[A] gezählt.
- CSV-Strukturkontrolle: UTF-8, Semikolon, exakte neun Kopfspalten, **329 eindeutige Datenzeilen** mit vollständiger Quell-ID-Abdeckung. Status-/Severitysummen, 184 B/Q-IDs, 95 spätere/Gemischtgruppen-IDs, 39 N/P-IDs und priorisierte/Traceability-Listen stimmen mit den Berichtstabellen überein.
- **Aktuelle unabhängige Teil-08-Quellenstichprobe:** Kein materieller Korrekturbedarf beim Abgleich von Katalog, Metrikmigrationen/-store, Spool-Pruning/-Flush, Reconciliation-Pagination, Topologie in Backend/UI/API, Wartungsbenachrichtigung und DB-Testdeklarationen/CI-Selektor. Bestätigt wurden insbesondere MON-01, NFR-01–04, UI-06 sowie vier selektierte echte DB-Testfunktionen plus statischem Migrationstest; keine erneuten Produktläufe/Installationen. Quelle: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/08-frontend-monitoring-nfr-tests.md:270–276`. Das ist keine automatisierte Abschlussreview oder Last-/Pentest-/Betriebsabnahme.
- **Historische unabhängige Teil-09-Quellenreview:** Eine zweite, nur lesende Stichprobe der Teil-09-Produktquellen (Dokumente, Management/Desk/Tickets, Workflows, AI, Cloud-Assets und Migrationen 33/50/56) ergab keinen materiellen Korrekturbedarf. Die Elternprüfung bestätigte zusätzlich 46 IDs, acht Spalten, fünf Hauptabschnitte, 19 Zusammenfassungszeilen und vorhandene zitierte Dateipfade; genaue Testsymbolfundstellen sind ergänzt. Quelle: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/09-module-phase2plus.md:207–216`. Das ist eine Quellen-/Dokumentreview, weder das historisch fehlende automatische Reviewwerkzeug noch eine DB-/Service-/Pentestabnahme.
- Die Ergänzung dieser beiden Konsolidierungsdateien wurde durch Dokumentparsing und `git diff --check` geprüft, ohne weitere Produktläufe/Installationen. Das ist keine Behauptung, in den Teilberichtsfortsetzungen seien keine Tests gelaufen: die abweichenden historischen Teil-09-Läufe und strikt offline begrenzten Teil-08-Läufe stehen getrennt oben. Unabhängige Abschlussvalidierung der aktuellen Ergänzung bleibt ein gesonderter Nachweis.

**Historischer Abschlussstand der ersten 233-ID-Konsolidierung (kein neuer Nachweis für die Ergänzung):** Damals 229 Quellzeilen unverändert und vier durch K1–K3 ausdrücklich korrigiert; Dokumentkonsolidierung ohne Produktivtests/Builds. Unabhängige Read-only-Quellen-/Dokumentreview mit drei eingearbeiteten Präzisierungen (JOB-04 statt GQL-03 bei State-at, eigenständige ENT-03-Pflicht gegenüber CH21(V), Feldbenennung statt unbekanntem AUD-03-Sollenum); keine weiteren materiellen Einwände gemeldet. Secret-Scans beider damaligen Ergebnisdateien und `git diff --check` ohne Befund, damals nur die beiden Konsolidierungsdateien geändert.

Die damalige parallele Validierung meldete im Wrapper „Success“, erklärte aber ausdrücklich, dass das automatische Reviewwerkzeug fehlte: **kein bestandener automatisierter Reviewnachweis**. CodeQL wurde wegen ausschließlich trivialer Dokumentänderungen übersprungen, nicht als ausgeführter Sicherheitstest ausgegeben. Diese historischen Prüfungen und die damalige unabhängige Review sind von der aktuellen Dokumentstrukturkontrolle und den oben genannten Teil-09-Testbeobachtungen getrennt.

**Historischer Abschluss der Teil-09-Fortsetzung:** Die Elternprüfung bestätigte erneut 279 eindeutige neunspaltige CSV-Zeilen, bytegetreu erhaltene 233 Altzeilen, 46 genaue Quellenimporte einschließlich Berichtszeilennummern, vollständige dublettenfreie Inventare mit 47 Critical/144 High, 225 Traceability-IDs und unverändert 141 [B]/[Q]-IDs. Der damalige Änderungsvergleich umfasste ausschließlich Teil-09-Katalogtext, Teil-09-Bericht und beide Konsolidierungsdokumente. Secret-Scan aller vier damaligen Dateien ohne Befund.

Die zur Teil-09-Fortsetzung erneut angeforderte parallele Validierung vom 2026-10-01 konnte das automatische Reviewwerkzeug mangels verfügbarem Modell wieder nicht ausführen, trotz „Success“ im Wrapper; **damals automatisierte Review weiterhin nicht nachgewiesen**. CodeQL wurde als Dokumentationsänderung übersprungen. Die damalige unabhängige Quellenstichprobe bleibt ein separater Nachweis; weder fehlende Produktabnahmen noch die dokumentierte Testausführungsabweichung sind dadurch erledigt.
