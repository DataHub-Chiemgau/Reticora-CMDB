## Fehlende Teilberichte – Gesamtprüfung unvollständig

**Zuerst nachholen:** Teil 03 (Datenmodell/Standorte/Metamodell), Teil 08 (Frontend/Monitoring/NFR/Tests/Gates/Traceability) und Teil 09 (Phase-2+-Module). Die Dateien fehlen im geprüften Checkout; diese Teile müssen erstellt beziehungsweise ihre bereits vorhandenen Ergebnisse bereitgestellt werden:

- `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/03-datenmodell-standorte-metamodell.md`
- `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/08-frontend-monitoring-nfr-tests.md`
- `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/09-module-phase2plus.md`

Vorhanden und jeweils als VOLLSTÄNDIG bezeichnet: Teile 01, 02, 04, 05, 06, 07. Das bezeichnet dort abgeschlossene Bearbeitung, nicht Konformität oder erfolgreiche Integrationsnachweise. Insbesondere Teil 06 deckt nur seinen ausdrücklich gelieferten Umfang mit gemeinsamen Prüfblöcken und überwiegend unbekannten Tags ab. Fehlende Teile werden weder als PASS noch pauschal als Produkt-FAIL gezählt; ihre unbekannten ID-Mengen werden nicht erfunden.

## Management-Zusammenfassung

Stand 2026-10-01; Prüfkopie `d8f35e05f891d7c950f9e19824221a095807493c`.
**Gesamtabdeckung unvollständig:** Teile 03, 08 und 09 fehlen; sechs vorhandene Teile ergeben **233 eindeutige IDs**.
**Nachgewiesener Phase-1-Erfüllungsgrad im verfügbaren Korpus: 0/141 [B]-oder-[Q]-IDs = 0 % PASS**; keine Vollkatalogquote und nicht „0 % Produktcode vorhanden“.
Nach Widerspruchsbereinigung: **30 Critical und 123 High** über alle Phasen, gezählt je ID, nicht je unabhängiger Ursache.
Davon betreffen **25 Critical und 97 High** explizite [B]/[Q]-IDs; spätere/ungeklärte Tags sind getrennt ausgewiesen.
**G1 nicht freigabefähig:** Isolation, Autorisierung, manuelle Datenhoheit und zuverlässige Verarbeitung haben belegte Lücken; erforderliche Abnahmen fehlen.
Lasttest, vollständige Isolation, Pentest und Install-Smoke für beide Plattformen sind nicht vollständig nachgewiesen.
**180 bekannte [B]/[Pn]/[A]-IDs ohne Traceability-Eintrag**: 179 aus Teilberichten plus GLO-10 aus den Grundlagen; weiterer Umfang unbekannt.
**34 N/P-IDs**, überwiegend wegen fehlendem v2-Text; CH16-Vaultvorschlag wird nicht als beschlossene G1-Pflicht ausgegeben.
Priorität: Isolation/RLS → Auth/Authz → Datenmodell/Migrationen → Ingest/Reconciliation → API/Jobs → Frontend; anschließend unabhängige Gate-Nachweise.
Geändert wurden ausschließlich die beiden angeforderten Konsolidierungsdokumente, keine Produktivdateien, Migrationen oder Tests.

## Datengrundlage und Zählregeln

Erst Zusammenfassung und Stand, anschließend die Ergebnistabellen der vorhandenen Teile gelesen. Quelltexte werden nur zur Klärung berichtsübergreifender Widersprüche erneut geprüft. Historische Testläufe bleiben als solche gekennzeichnet; diese Konsolidierung ist kein neuer Produkt-/Pentest.

| Teil | Berichtsstatus | Einzel-IDs / Tabellenblöcke | Quelle |
|---|---|---|---|
| 01 | VOLLSTÄNDIG, v2-Lücken | 53 / 53 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/01-installation-stack.md:30–84,216–222` |
| 02 | VOLLSTÄNDIG, v2-/Laufzeitlücken | 36 / 36 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/02-mandanten-auth-entitlements.md:32–69,494–498` |
| 03 | FEHLT | nicht bestimmbar | Fehlender Pfad oben |
| 04 | VOLLSTÄNDIG, v2-/Laufzeitlücken | 52 / 52 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/04-ci-netz-rack-beziehungen-impact.md:27–80,261–267` |
| 05 | VOLLSTÄNDIG, Laufzeit-/Teiltextlücken | 37 / 37 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/05-collector-discovery-reconciliation.md:30–68,330–332` |
| 06 | VOLLSTÄNDIG nur für eingeschränkten Auftrag | 16 / 12 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/06-audit-sicherheit-events.md:16–33,208–210` |
| 07 | VOLLSTÄNDIG, Laufzeit-/Teiltextlücken | 39 / 39 | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/07-api-suche-jobs-lebenszyklus.md:24–64,269–273` |
| 08 | FEHLT | nicht bestimmbar | Fehlender Pfad oben |
| 09 | FEHLT | nicht bestimmbar | Fehlender Pfad oben |

Die CSV hat eine Zeile pro **Anforderungs-ID**, nicht pro Ursache oder Prüftabellenblock. Gemeinsam bewertete AUD-01/02, AUD-05/07/09 und EVT-01/02 werden für die angeforderten Einzelzeilen aufgeteilt und als gemeinsame Bewertung gekennzeichnet. `CH16` ist eine Entscheidung und keine zusätzliche Anforderungszeile. Mehrere IDs können dieselbe Ursache betreffen; Critical-/High-Zähler sind deshalb keine Anzahl unabhängiger Schwachstellen.

`abschnitt` bezeichnet den **Auditteil** (01–09), nicht eine aus Präfixen geschätzte Katalogabschnittsnummer. `bericht` verweist absolut auf die Quellzeile; `evidenz` enthält die dort genannten Belege. Nicht bekannte Tags bleiben unbekannt. [B]/[Q]-Quote zählt jede ID mit explizitem [B] oder [Q] genau einmal; gemischte Phasen-/Vorschlagsgruppen werden gesondert erläutert und nicht heimlich als Baseline eingerechnet.

## Statistik

### Abschnitt × Status: [B] oder [Q]

Explizite Tagzugehörigkeit, Mehrfachzugehörigkeit nur einmal gezählt. 136 IDs tragen [B], sechs [Q]; TEC-06 trägt beides, daher **141**, nicht 142. ENT-06 trägt die gemischte Vorschlagsgruppe `[B–P4], [A]`, kein einzelnes `[B]`: separat geführt. Bei Zuordnung auch dieser ID zu B ergäbe sich **0/142**, unverändert 0 %. SEC-05 erhält aus CH16(V) keinen erfundenen [B]-Tag.

| Auditteil | PASS | PARTIAL | ABWEICHEND | FAIL | OFFEN | N/P | Summe |
|---|---:|---:|---:|---:|---:|---:|---:|
| 01 | 0 | 7 | 11 | 2 | 0 | 0 | 20 |
| 02 | 0 | 4 | 21 | 1 | 0 | 2 | 28 |
| 03 | — | — | — | — | — | — | Bericht fehlt |
| 04 | 0 | 6 | 24 | 6 | 0 | 1 | 37 |
| 05 | 0 | 3 | 23 | 3 | 0 | 0 | 29 |
| 06 | — | — | — | — | — | — | Tags unbestätigt |
| 07 | 0 | 7 | 12 | 8 | 0 | 0 | 27 |
| 08 | — | — | — | — | — | — | Bericht fehlt |
| 09 | — | — | — | — | — | — | Bericht fehlt |
| **Bekannte Gesamtmenge** | **0** | **27** | **91** | **20** | **0** | **3** | **141** |

Keine WARN-Kategorie im vorliegenden Bewertungsschema; vorhandene PARTIAL/ABWEICHEND werden nicht nachträglich zu erlaubten SOLL-WARN umgedeutet. Die Phasengeltung unbekannter Tags muss geklärt werden.

### Spätere Phasen/Add-ons: vorhanden/offen

„Vorhanden“ bedeutet hier **Implementierung nachgewiesen, aber PARTIAL/ABWEICHEND**, nicht erfüllt oder releasbar. Die disjunkten Haupttaggruppen verhindern Mehrfachzählung; spätere Teilpflichten einer [B]-ID sind im Befund enthalten, nicht noch einmal als ganze ID gezählt.

| Haupttaggruppe | PASS | Vorhanden, nicht PASS | OFFEN | Summe |
|---|---:|---:|---:|---:|
| [P2] | 0 | 15 | 7 | 22 |
| [P3] | 0 | 2 | 5 | 7 |
| [P4] | 0 | 3 | 6 | 9 |
| [P5] | 0 | 1 | 0 | 1 |
| [A] führend (COL-07, Relay auch P4) | 0 | 1 | 0 | 1 |
| Mehrphasig P2–P4/[A] (RBA-05, API-06) | 0 | 2 | 0 | 2 |
| (V), B–P4/[A] (ENT-06; Befund nur zu CH14 verbindlich) | 0 | 1 | 0 | 1 |
| **Gesamt dieser Gruppen** | **0** | **25** | **18** | **43** |

Auch diese Übersicht ist **keine vollständige P2–P5-/GA-Abnahme**, insbesondere ohne Teil 09. Ungetaggte Teil-06-IDs werden nicht als spätere Phase einsortiert. [O] als Haupttag betrifft TEC-16/INS-08 (beide OFFEN); optionale Unteranteile anderer IDs erzeugen keine Extrazeilen.

### Alle verfügbaren Einzel-IDs nach Korrektur

| PASS | PARTIAL | ABWEICHEND | FAIL | OFFEN | N/P | Summe |
|---:|---:|---:|---:|---:|---:|---:|
| 0 | 46 | 112 | 21 | 20 | 34 | **233** |

Severity: **30 Critical, 123 High, 23 Medium, 3 Low, 54 ohne Defekt-Severity**. Die ursprünglichen 31 Critical/125 High werden nicht unkritisch addiert: SEC-05 sowie AUD-01/02 sind wegen unbestätigter Vaultpflicht normativ korrigiert (K1/K2). Unveränderte technische Risiken und ursprüngliche Ratings bleiben ausdrücklich erhalten. Vollständige Status-/Tagwerte stehen in der CSV.

## Gate-Check G1

Maßstab sind GATE-01/02/03/05 aus dem aktuellen Auftrag; fehlender Teil 08 wird nicht durch erfundene dortige Ergebnisse ersetzt.

| Kriterium | Ergebnis | Begründung / benötigter Nachweis |
|---|---|---|
| GATE-01: kein offenes Critical im Gate | nicht erfüllt | Bereits explizite Baselinebefunde zur Isolation, Authentifizierung und manuellen Datenhoheit offen, etwa TEN-04/05, AUT-01/02, CI-04/10, OVR-01/02 und API-04/07. |
| GATE-02: sämtliche Phasen- und Qualitätsanforderungen PASS | nicht erfüllt | Keine vorhandene [B]/[Q]-ID PASS; N/P ist ebenfalls kein PASS. Drei Berichte fehlen, DOD-/SEQ-/GLO-11-Gesamtabdeckung aus Teil 08 nicht vorhanden. |
| GATE-03: Lasttest NFR-10 bestanden | nicht erfüllt (Nachweis fehlt) | Kein vorgelegter Lasttestnachweis, NFR-Bericht 08 fehlt. Lastprofil und CH15-Zielwerte müssen verbindlich bestätigt und messbar geprüft werden. |
| GATE-03: Pentest ohne offene High/Critical | nicht erfüllt | Vorhandene statische Sicherheitsbefunde nicht behoben; kein freigabefähiger Pentestbericht nachgewiesen. Statisches Audit ist kein Pentest. |
| GATE-03: Isolationstest TEN-09 vollständig | nicht erfüllt | TEN-09 PARTIAL: keine vollständige Site-/Team-/Schreib-/Kanalabdeckung; vorhandene Integrationstests sind nicht durchgängig ausgeführt. Quelle Teil 02:42. |
| GATE-03: Install-Smoke auf beiden Plattformen | nicht erfüllt | INS-01 PARTIAL: Workflow nur ubuntu-latest, kein belegter zweiter Zielplattformlauf; Tests mit gemocktem IdP ersetzen Install→Login nicht (INS-04). |
| GATE-05: [O] beeinflusst kein Gate | erfüllt als Bewertungsregel | TEC-16/INS-08 und optionale Zusatzprotokolle/Textsyntax/Air-Gapped-Teile werden aus der Pflichtquote und allen G1-Blockern ausgeschlossen. Das ist eine Regel der Konsolidierung, kein erfundener Produkt-PASS. |

Ein Gate wird nicht dadurch grün, dass seine Kriterien mangels Bericht unbewertet sind. Die P2–P5-/[A]-Unvollständigkeit löst für sich kein G1-Fehlen aus; bereits implementierte Isolation-/Authfehler müssen für die im Release tatsächlich aktivierten Kanäle dennoch behoben oder nachweislich ausgeschlossen werden. GATE-04 betrifft GA, nicht G1.

## Invarianten PRI-10

Wortlaut: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md:82–91`. Ein Gegenbeispiel genügt für „verletzt“, wenige positive Pfade nicht für „eingehalten“. Die Nummerierung unten bezeichnet die neun Aufzählungspunkte, keine neuen Katalog-IDs.

| Nr. / Invariante | Bewertung | Betreffende Befunde / Abgrenzung |
|---|---|---|
| 1. Ein Gerät → ein CI; kein Auto-Merge ohne belastbare Identität | verletzt | REC-02: Hostname-Automatch, unzureichende Serial-/MAC-/Client-/VRF-Qualifikation; CI-13: gelöschte Geräte können neu angelegt werden; REC-11: separater Agentmatcher. Keine Garantie durch bloße Existenz eines Matchers. |
| 2. Asset/CI getrennt und komponierbar, Asset führend für kaufmännische Daten/Serial/Standort/Lifecycle | verletzt | EXP-01 (DATEV-Teil): DATEV verwendet CI-Serial und freie CI-Kauf-/Standortattribute statt Assetdaten; RCK-03 übernimmt Standort nicht nach DB-05. Gegenbeleg direkt in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/export.go:194–204`. Vollständige Asset-/Kompositionsprüfung mangels Teile 03/09 offen. |
| 3. Genau ein kanonischer Speicherort; Kopien abgeleitet und read-only | verletzt | CI-10/OVR-01: getrennte Feldwert-/CI-Sicht ohne konsistenten effektiven Wert; EXP-01: kaufmännische CI-Attributkopien. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/pg_repository.go:330–353` lässt Serial-/Standort-/Attributänderungen zu. Keine pauschale Behauptung, jeder Doppelwert sei bereits ein Verstoß. |
| 4. Manuell gewinnt; Automation zerstört nie still manuelle Daten | verletzt | CI-04/CI-10, REC-03/05/12, OVR-01/02, API-07: fehlende Autooverrides, manual20 statt100, fail-open Schutzabfrage und direkter Workflow-Schreibpfad. Vorhandene Historie heilt den Verlust des wirksamen manuellen Werts nicht. |
| 5. Kein stilles Verwerfen; unklare Fälle → Review | verletzt | OPS-06/COL-05: unbestätigte Spooldaten durch Alter-/Größenbereinigung gelöscht; REC-06/08: Eingangskonflikt kann verloren gehen, unvollständiger Reviewprozess. CH21(V) bestätigt allein keine Lizenz-Reviewpflicht; Teil 02:450 bewertet den unlicensed_ci-Prozess jedoch als eigenständige ENT-03-Einzelpflicht, die davon getrennt bestehen bleibt. |
| 6. Historisierung aller Bewegungen/Änderungen mit observed_at | verletzt | GLO-12, AUD-03, REC-07, RCK-03, REL-09: Server- statt Quellzeit, falsche Granularität, fehlende Änderungsereignisse; JOB-04: State-at verwendet created_at (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/history/pg_repository.go:91–97`). Ein bloßes History-API ist kein Quellzeitnachweis. |
| 7. Server-/DB-Isolation einschließlich Client/Site/Team | verletzt | TEN-02/04/05/06, RBA-03, IMP-07, TEC-06/12, GQL-04, SRC-01/03/04, EXP-01; bei Dokumenten zusätzlich Cross-Org-Blobpfad laut TEN-06. Org-RLS allein genügt nicht. |
| 8. Server-seitige Entitlements | verletzt | ENT-02/03/04/05/06/07/08, API-04/06, GQL-04: Ingest-/Export-Aliasse umgehen Gates, tenantseitige Selbstfreigabe, Add-ons implizit im Plan und falsche Ablaufwirkung. CH14/CH21 sind verbindlich, vorgeschlagene Paketwerte nicht. |
| 9. Erweiterungen ergänzen, ersetzen die Architektur nicht | unklar | Wiederverwendete Ports/Repositories stehen separatem Agentmatcher (REC-11), rohem Workflow-Schreibpfad (OVR-02) und uneinheitlicher Rechteverdrahtung (RBA-05) gegenüber. Diese konkreten Verletzungen sind oben erfasst; ohne Teile 03/09 kein vollständiges Architektururteil über sämtliche Erweiterungen. |

## Critical- und High-Befunde nach Fundament

Die folgenden Gruppen nennen sämtliche betreffenden Einzel-IDs; Mehrfachursachen sind nicht als voneinander unabhängige Schwachstellen zu verstehen. Vollständige Status-, Phasen-, Aufwand- und Quellbelege stehen ID-genau in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/befunde.csv`. „Teil/Zeile“ verweist auf den absoluten Quellberichtspfad aus der Datengrundlage. Innerhalb jeder Severity gilt die verlangte Fundament-Reihenfolge; Stack/Installation/Betrieb ist der Datenmodell-/Infrastrukturbasis zugeordnet, sofern nicht unmittelbar Auth, Ingest oder Jobs betroffen sind.

### Critical

| Fundament | IDs | Kernbefund | Teil/Zeile |
|---|---|---|---|
| Isolation/RLS | TEC-06 | Metriken/Aggregate ohne RLS oder sicheren Fallback | 01:59 |
| Isolation/RLS | TEC-12 | Implementierte OpenSearch-Suche ohne vollständigen Nutzerscope; P5, kein pauschaler G1-Fehlumfang | 01:61 |
| Isolation/RLS | TEN-02, TEN-04, TEN-05, TEN-06 | Fehlende denormalisierte Scopes/Schreibschranken, inkonsistente Tenanttransaktionen, zusätzlicher Dokument-Blobzugriff | 02:35,37–39 |
| Isolation/RLS | IMP-07 | Traversierung vor Sichtbarkeitsprüfung; unsichtbare Zwischenknoten | 04:76 |
| Isolation/RLS | SRC-01, SRC-03, SRC-04 | Org-only Suche/CTEs und fehlendes Entitätsleserecht im strukturierten Suchpfad | 07:39,41–42 |
| Isolation/RLS | EXP-01 | Scopeverlust im Exportworker; Jobzugriff nur orggebunden | 07:47 |
| Auth/Authz | AUT-01, AUT-02, TLC-04 | Login reaktiviert gesperrte Nutzer; Refresh wiederholt alte Rechte ohne wirksamen Widerruf | 02:44–45; 07:52 |
| Auth/Authz | AUT-09 | Produktiver Demo-Admin und fehlende zwingende Authentifizierungspolicies | 02:52 |
| Auth/Authz | RBA-02, RBA-03, RBA-04 | Rollen-/Scopeauflösung inkonsistent; falsche Aktionsrechte trotz vollständig gemappter Routen | 02:55–57 |
| Auth/Authz | COL-02, SEC-01 | Klartext-Credential-/Webhook-Headerzugriff; Redfish ohne Zertifikatsprüfung | 05:35; 06:26 |
| Auth/Authz | SEC-08 | Ausgehende Webhook-/SCIM-Ziele ohne hinreichende private/Metadaten-/Redirect-Zielprüfung | 06:30 |
| Auth/Authz | API-04 | Nichtatomare orgweite Idempotenzantwort vor Aktionsrechten; fehlende Rate-Buckets | 07:29 |
| Auth/Authz | GQL-04 | ci:write statt Resolverleserechten; volle Objekte ohne Feldprojektion | 07:38 |
| Ingest/Reconciliation | CI-04, CI-10, OVR-01, OVR-02, API-07 | Fehlende Autooverrides/einheitliche effektive Werte, fail-open Schutzprüfung, Workflowumgehung; keine CI-Konkurrenzkontrolle | 04:33,39; 05:66–67; 07:32 |
| Ingest/Reconciliation | OPS-06, COL-05 | Unbestätigte Spooldaten werden ohne fachliche Verlustmeldung gelöscht | 01:84; 05:38 |

Keine zusätzlich als Critical bewertete reine Frontend-ID liegt vor; Kanal-/Jobbefunde stehen entsprechend ihrer eigentlichen Isolation-/Auth-/Datenhoheitsursache oben.

### High

| Fundament | IDs | Kernbefund | Teil/Zeile |
|---|---|---|---|
| Isolation/RLS | TEN-03 | Effektiver DDL-/FORCE-Vertrag nicht vollständig belegt | 02:36 |
| Isolation/RLS | TEN-09 | Unvollständige echte Org-/Client-/Site-/Team-/Schreib-/Kanalmatrix | 02:42 |
| Auth/Authz | AUT-04, AUT-08, AUT-10 | Key-Rechteschnitt/Rotation, SCIM-Deprovisionierung und Pre-Auth-Ratelimits unvollständig | 02:47,51,53 |
| Auth/Authz | RBA-01, RBA-05, RBA-06, RBA-08 | Fehlende/abweichende Aktionsrechte, umgangene Spezialrechte und Service-Account-Autorität | 02:54,58–59,61 |
| Auth/Authz | ENT-01, ENT-02, ENT-03 | Feature-/Limit-/Herkunftsmodell und tatsächliche Enforcementpfade stimmen nicht | 02:62–64 |
| Auth/Authz | ENT-04, ENT-05 | Tenant-Selbstfreigabe und fehlende Export-/Ingest-/BFF-Gates | 02:65–66 |
| Auth/Authz | ENT-06, API-06 | Verbindliche Add-on-Trennung CH14 durch implizite Planfreigabe verletzt, unabhängig von Paketvorschlägen | 02:67; 07:31 |
| Auth/Authz | ENT-07, ENT-08 | Lizenzablauf sperrt falsche Funktionen; Downgrade/Bestands-/Ingestvertrag nicht vollständig | 02:68–69 |
| Auth/Authz | SEC-07 | Operator-/Break-Glass-/MFA-/separater Auditpfad fehlt | 06:29 |
| Auth/Authz | INS-02 | Issuer-Erreichbarkeit/-Richtigkeit und Brokerkonfiguration nicht vollständig geprüft | 01:72 |
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
| API/Jobs | TEC-13, TEC-14, OPS-05 | Notifier loggt; gemeinsame zuverlässige Worker-/Scheduler-/Fehlersicht fehlt | 01:62–63,83 |
| API/Jobs | API-05, API-08, API-09 | Ressourcen-/CI-Aktionen, serverseitiger Bulkvertrag und generische Job-API fehlen | 07:30,33–34 |
| API/Jobs | GQL-01, GQL-02, SEC-10 | Eigenparser/BFF-Vertrag, Resolver/Dataloader und Ausführungsgrenzen unvollständig | 07:35–36; 06:31 |
| API/Jobs | TLC-01, TLC-02, TLC-03, INS-04 | Keine vollständige Signup-/Operator-/Einladungs-/Install→Login-Kette | 07:49–51; 01:74 |
| API/Jobs | NTF-01, NTF-03, NTF-04, NTF-05 | SMTP/Provider, Empfänger/Vorlagen/i18n, Zustellungscenter und Baseline-Ereignisketten fehlen | 07:56,58–60 |
| API/Jobs | JOB-01, JOB-02, JOB-03, JOB-04 | Generische persistente Jobs, Recovery, Scheduler, Cancel/Revert und verpflichtende Hintergrundpfade fehlen | 07:61–64 |
| API/Jobs | EVT-01, EVT-02 | NATS-Helfer ohne vollständige produktive Stream-/Publisherverdrahtung | 06:33 |
| API/Jobs | IMP-01, IMP-02, IMP-03, IMP-04, IMP-05, IMP-08 | Falsche Ausfallrichtung/Netzheuristik, fehlende Redundanz-/Pfad-/Mehrfachausfallsemantik | 04:70–74,77 |
| Frontend | IMP-06 | Typfilter falsch; graphischer Gesamtumfang ohne verlangte 2.000er-Abweisung | 04:75 |
| Frontend | BLK-01 | Promise.all-Einzelrequests statt serverseitigem Bulkjob und Einzel-ID-Fehlerbericht | 07:45 |

## Querschnittsmuster

| Muster | Betroffene IDs | Gemeinsamer Befund |
|---|---|---|
| Kontext endet an Kanal-/Transaktionsgrenzen | TEN-04/05/06, TEN-09, RBA-03, TEC-06/12, IMP-07, GQL-04, SRC-01/03/04, EXP-01, JOB-01 | **Nicht** pauschal fehlendes WITH CHECK: überwiegend vorhanden, jedoch Org-only; Client nur selektiv, Site/Team fehlen. Backgroundjobs und Suchprojektionen verlieren Aufruferscopes. |
| Mapping-/Bausteinexistenz mit korrekter Durchsetzung verwechselt | RBA-01/02/04/05/06/08, AUT-04, ENT-02/03/04/05, GQL-04, TLC-04 | Fail-closed bei unbekannter Route verhindert keine falsch zugeordnete bekannte Aktion; Key-/Session-/Objekt-/Feldrechte sind nicht durchgängig dieselbe Autorität. |
| Manual-/Quellzeitvertrag nicht zentral durchgesetzt | GLO-12/13, CI-04/10/13, REC-01/02/03/05/06/07/08/12, OVR-01/02/03, API-07 | Parallelmodelle, fehlende reservierte Namespaces, Serverzeit, falsche Ränge und fehlende Konfliktreviews; Review-"Merge" ist nicht CI-Merge. |
| Historie/Audit/Events fehlen am tatsächlichen Schreibpfad | AUD-03/04/05/07/09, RCK-03, REL-09, REC-07, EVT-01/02, API-03 | ci_change, entity_change, Audit, NATS und Webhooks bilden keine geschlossene Ereigniskette; Request-/Correlation-ID und Auftragstransaktion fehlen teils. |
| Einzelworker statt verlässlichem Job-/Zustellungsvertrag | TEC-13/14, OPS-02/05/06, COL-05, BLK-01, API-08/09, NTF-01/03/04/05, JOB-01/02/03/04 | Crash-Recovery/Single-Runner/Cancel fehlen; Log/Statusflag ist kein Versand. Claim-Sperre ersetzt weder Scheduler-Leader noch persistentes Catch-up. |
| Datenmodell und konsumierender Vertrag divergieren | TEN-10, NET-01/02/03/05/08/09, CI-01/03, RCK-01/02, REL-01/02, LCY-02/03, SRC-01, EXP-01 | Fehlende VRFs/FKs/Enums, andere Routen-/Fehlercodes, ignorierte Felder und Suchtypen außerhalb DB-CHECK; vorhandene Tabellen reichen nicht. |
| Tests beweisen Ersatzverhalten statt Soll | TEN-09, RBA-04, CI-01, RCK-01/02, IMP-01/03, DIS-05, REC-03/05, TOP-01, GQL-01/02/04, SRC-01/03/04, EXP-01, TLC-04 | Memory-/SQL-String-/Happy-Path-Tests beziehungsweise Tests falscher Konstanten ersetzen keine echte PostgreSQL-/Kanal-/Negativprüfung (TST-04). Setupblockierte Tests sind weder bestanden noch fachlich fehlgeschlagene Assertions. |
| UI und i18n nicht durchgängig angebunden | REC-10, DIS-08, IMP-06, SRC-03, VIE-02, BLK-01, NTF-03/04, JOB-03 | Fehlende Builder/Freigaben/Feldbegründungen/Jobcenter, falsche Typfilter und Sammelfehler; NTF-03 belegt fehlende Nachrichten-i18n. Kein pauschales „gesamte Frontend-i18n fehlt“ ohne Teil 08. |
| Installation/Betrieb nicht als vollständige Kette nachgewiesen | TEC-09/15, INS-01/02/03/04/05/06, OPS-01/03/04, SEC-11, TLC-01 | Plattform-/TLS-/Issuer-/Login-/Enrollment-/Backup-Verträge brechen an Übergängen; Healthcheck oder vorhandener Workflow bedeutet keinen erfolgreich geprüften Releasepfad. |

## Traceability

Die verlangte `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/traceability.csv` fehlt. Schon Teil 0 dokumentiert das Fehlen (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-bestandsaufnahme.md:210–225`); die Pfadkontrolle wurde wiederholt. Eine Audit-CSV ersetzt weder Epic-/Testdateizuordnung noch CI-Durchsetzung nach GLO-11. Dateizeilen/-inhalt sind mangels Datei nicht prüfbar, nicht als „vorhandene leere CSV“ beschrieben.

**179 von 179 bekannten prüfpflichtig getaggten IDs aus den Teilberichten ohne Eintrag, zuzüglich GLO-10 [B] aus den Grundlagen = mindestens 180 fehlende Zuordnungen.** GLO-10 erhält dadurch keine erfundene Ergebniszeile. Nicht mitgezählt: 30 ungetaggte v2-IDs, PRI-07, die 16 Teil-06-IDs ohne bestätigtes Tag sowie reine [Q]/[O]-IDs. [Q] bleibt gleichwohl Qualitätsmaßstab nach GLO-11/DOD-01; TEC-06 zählt wegen zusätzlichem [B] zur verpflichtenden CI-Menge.

Vollständige Liste der bekannten fehlenden Einträge:

| Herkunft | Anzahl | IDs |
|---|---:|---|
| Teil 01 | 18 | PRI-11, TEC-06, TEC-09, TEC-12, TEC-13, TEC-14, TEC-15, REP-01, INS-02, INS-03, INS-05, INS-06, INS-07, OPS-01, OPS-02, OPS-03, OPS-04, OPS-05 |
| Teil 02 | 35 | TEN-01, TEN-02, TEN-03, TEN-04, TEN-05, TEN-06, TEN-07, TEN-08, TEN-10, AUT-01, AUT-02, AUT-03, AUT-04, AUT-05, AUT-06, AUT-07, AUT-08, AUT-09, AUT-10, RBA-01, RBA-02, RBA-03, RBA-04, RBA-05, RBA-06, RBA-07, RBA-08, ENT-01, ENT-02, ENT-03, ENT-04, ENT-05, ENT-06, ENT-07, ENT-08 |
| Teil 04 | 52 | GLO-13, CI-01, CI-02, CI-03, CI-04, CI-05, CI-06, CI-07, CI-08, CI-09, CI-10, CI-11, CI-12, CI-13, LCY-01, LCY-02, LCY-03, LCY-04, LCY-05, LCY-06, NET-01, NET-02, NET-03, NET-04, NET-05, NET-06, NET-07, NET-08, NET-09, RCK-01, RCK-02, RCK-03, REL-01, REL-02, REL-03, REL-04, REL-05, REL-06, REL-07, REL-08, REL-09, IMP-01, IMP-02, IMP-03, IMP-04, IMP-05, IMP-06, IMP-07, IMP-08, IMP-09, IMP-10, IMP-11 |
| Teil 05 | 35 | GLO-12, COL-01, COL-02, COL-03, COL-04, COL-05, COL-06, COL-07, COL-08, DIS-01, DIS-02, DIS-03, DIS-04, DIS-05, DIS-06, DIS-07, DIS-09, DIS-10, REC-01, REC-02, REC-03, REC-04, REC-05, REC-06, REC-07, REC-08, REC-09, REC-10, REC-11, REC-12, TOP-01, TOP-02, OVR-01, OVR-02, OVR-03 |
| Teil 07 | 39 | API-01, API-02, API-03, API-04, API-05, API-06, API-07, API-08, API-09, GQL-01, GQL-02, GQL-03, GQL-04, SRC-01, SRC-02, SRC-03, SRC-04, VIE-01, VIE-02, BLK-01, BLK-02, EXP-01, IMP-IO-01, TLC-01, TLC-02, TLC-03, TLC-04, TLC-05, TLC-06, TLC-07, NTF-01, NTF-02, NTF-03, NTF-04, NTF-05, JOB-01, JOB-02, JOB-03, JOB-04 |
| Grundlagen, ohne Ergebniszeile | 1 | GLO-10 |
| **Bekannte Gesamtmenge** | **180** | Keine doppelte ID; zusätzliche IDs aus fehlenden Teilen nicht bestimmbar |

Für Teile 03/08/09 lässt sich keine vollständige Soll-ID-Menge ableiten. Statische Suche nach Traceability-Durchsetzung in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/.github/workflows/` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/Makefile` findet keine entsprechende Prüfung; die vorhandenen Test-/Parity-Jobs ersetzen sie nicht. GLO-11-Abdeckung ist **nicht erfüllt**. Es wurde kein aktueller Remote-CI-Lauf als grün/rot bewertet.

## N/P und offene Nachweise

Nach Bereinigung **34 vollständige N/P-Zeilen**: 33 Quell-N/P-IDs plus SEC-05 (K1).

| IDs | Grund | Zur Klärung benötigt |
|---|---|---|
| PRI-01, PRI-02, PRI-03, PRI-04, PRI-05, PRI-06, PRI-08, PRI-09 | Nur „unverändert aus v2“, kein Originalwortlaut/Tag | Verbindlicher v2-Text einschließlich Tags; Grundlagen:78 |
| GLO-01, GLO-02, GLO-03, GLO-04, GLO-05, GLO-06, GLO-07, GLO-08, GLO-09 | Nur „unverändert“ | v2-Regeln/Tags; Grundlagen:97 |
| TEC-01, TEC-02, TEC-03, TEC-04, TEC-05, TEC-07, TEC-08, TEC-10, TEC-11 | Stackvorgaben nicht aus vorhandenem Code rekonstruierbar | v2-Stackanforderungen/Tags; Teil 01:50–58 |
| REP-02, REP-03, REP-04, REP-05 | Strukturvertrag nur referenziert | Vollständiger v2-Strukturwortlaut/Tags; Teil 01:67–70 |
| AUT-05, AUT-06 | Originalauthentifizierungsanforderungen fehlen | v2-Text; [B]-Tag bereits bekannt; Teil 02:48–49 |
| NET-04 | cable „unverändert“, vorhandenes CRUD ersetzt keinen Sollfeldvertrag | v2-Kabelmodell/-Regeln; Teil 04:52 |
| SEC-05 | Spezifische Vaultpflicht und Phase nur aus CH16(V); ursprüngliche ABWEICHEND/Critical-Bewertung normativ nicht abgesichert | Beschlossener SEC-05-Originalvertrag, Bestätigung/Alternative zu CH16, PII-/Retention-/Löschkonzept; Teil 06:27 und K1 unten |

„Grundlagen“ ist `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md`; „Teil“ verweist auf die absoluten Berichtspfade in der Datengrundlage. N/P ist kein Beleg für fehlende Implementation und kein zulässiger Ersatz für PASS.

**Nicht ganze IDs, sondern weiterhin N/P-Unteranteile:** REP-01/INS-/OPS-Betriebsnachweise (v2-Rest, reale Plattformen, EU-Lokation, Restore); TEN-09 (v2-Rest und vollständige RLS-Laufzeitmatrix); COL-02/DIS-04 (referenzierter Credential-/Protokollrest, physische Geräte); AUD-01/02 (exakte Kontaktrollen), AUD-03 (Soll-Enum), AUD-05/07/09 (Fristen); API-01 (Releasehistorie/Sunset), API-03 (v2-Fehlertabelle); SEC-10/GQL-01 (Originalgrenzen), GQL-04 (vollständiger RBA-07-Feldrechtsvertrag), IMP-IO-01 (referenzierter SEC-09-Dateivertrag). Benötigt werden jeweils Originalanforderungen beziehungsweise echte Integrations-/Releaseartefakte, keine Rekonstruktion aus Mocks oder früheren Auditbehauptungen.

Teile 03/08/09 haben **unbekannte fehlende ID-Mengen**; das sind weder zusätzliche gezählte N/P-Zeilen noch Null-Anforderungen. In Teil 06 fehlen für 15 IDs explizite Tags, SEC-05 hat nur eine Phasenangabe aus CH16(V); auch bei PRI-07 ist kein Tag angegeben. Tags und einzelne AUD-/EVT-Verträge müssen nachgeliefert werden, gemeinsame Quellbewertung ist noch kein Einzeltest.

## Offene Katalogentscheidungen

Grundlage ist der Entscheidungswortlaut unter `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md:15–59`. Ein offener Vorschlag blockiert die **abschließende Spezifikation/Abnahme seines Details**, nicht automatisch G1; eindeutige verbindliche Gegenbeispiele bleiben unabhängig davon bestehen.

| Entscheidung | Offene Festlegung / betroffene Befunde |
|---|---|
| CH15 (S mit ausdrücklichem Bestätigungsvorbehalt) | „1000-1000“ → 1.000–10.000/50.000-Headroom bestätigen; NFR-01/10 aus fehlendem Teil 08 mit prüfbarem Lastprofil vorlegen. Kein bestandener Lasttest aus angenommenen Zahlen. |
| CH30 / DIS-02 (V) | Verbindliche Top-20-Familien/Varianten und Abnahmedatensätze; Teil 05 nennt 23 Gruppen im Vorschlag. Die fehlende Klassifikationskette bleibt unabhängig davon ABWEICHEND. |
| ENT-06 (V) | Pakete, Limits, Trialwerte und Phasenmatrix bestätigen. **CH14-Add-on-Trennung ist bereits verbindlich**: ENT-06/API-06 bleiben deswegen High, nicht wegen nicht umgesetzter Vorschlagszahlen. |
| CH16 (V) | Vault-/Surrogatmodell, Phase-1-Umfang, Crypto-Shredding und Erhalt der Hashkette bestätigen; SEC-05 sowie AUD-01/02 benötigen Originalvertrag. Bereits persistierte Klartextattribute sind eine verifizierte Tatsache, aber der spezifische Architekturvorschlag ist nicht dadurch beschlossen. |
| CH21: Limitüberschreitung nur „(V, analog)“ | Den Analogievorschlag als Entscheidung bestätigen; **nicht** die separat berichtete ENT-03-Pflicht dadurch suspendieren: Teil 02:450 fordert unlicensed_ci ausdrücklich unabhängig von CH21(V). Zusätzliche Detail-/Retentionfestlegungen klären (ENT-03/08). Verbindlich bleibt außerdem: Ablauf stoppt ausschließlich Discovery/Ingest; andere Funktionen verfügbar (ENT-07/API-04). |
| CH26 (V) | Realm-/Org-Attribut-/Brokering-/MFA-Zielmodell bestätigen; AUT-01/09 und SEC-07 betreffen daneben konkrete bereits belegte Risiken. Vorschlagsstatus entschuldigt weder ein bekanntes Demo-Administratorkonto noch wirkungslose Deaktivierung. |
| CH27 (V) | Impact-Richtung/Strukturprojektion bestätigen; REL-01, IMP-01/02/03/04/10. Explizite Einzelanforderungen und falsche vorhandene Ausfallberechnung bleiben eigenständig zu bewerten. |
| CH28 (V) | Gemeinsamen Location-Baum/LOC-10 bestätigen und Teil 03 nachholen; CI-01, RCK-03, SRC-01. CH12-Assethoheit und Scope-Isolation sind keine optionalen Folgen dieser Entscheidung. |
| CH29 (V) | Hostname-Reviewdetails bestätigen; REC-02/CI-13. PRI-10 fordert bereits belastbaren Identitätsbeweis, daher kein bloßes Vorschlagsproblem. |
| NTF-02 (V-Provider), TLC-01/ENT-07 (V-Trialwerte) | Konkrete SMS-/Pager-Provider und Trialparameter festlegen; späterer Kanalumfang nicht G1-fällig. CH23-Signup und CH24-E-Mail-Baseline sind verbindlich. |

Zusätzlich fehlen bestätigte v2-Übernahmen und einzelne Querverweisverträge sowie dokumentierte Gate-Phasenzuordnung der ungetaggten Teil-06-IDs. DOD-01, SEQ-01, GATE-01–05 und GLO-11 werden aus der aktuellen Referenz als Bewertungsmaßstab verwendet, nicht als erfundene zusätzliche Ergebniszeilen aus dem fehlenden Teil 08. Die spätere Auslieferungsreihenfolge verlangt zuerst belastbare Isolation/Auth/Datengrundlagen und den Ingestpfad; zusätzliche Moduloberflächen ersetzen G1-Nachweise nicht.

## Berichtswidersprüche und Konsolidierungskorrekturen

Keine Anforderungs-ID steht doppelt in den vorhandenen Ergebnistabellen. Widersprüche betreffen daher überwiegend gemeinsame Quellstellen, Prüfvoraussetzungen oder unterschiedliche Teile desselben Ablaufs. Ein Vergleich der Produktpfade zwischen `b3fe0390056cde41441efca8d870e644c531e1bd` und der Prüfkopie ergab keine Änderungen; die unterschiedliche Bewertung wird nicht mit einem unbelegten Produktfortschritt erklärt.

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

Historische Teilberichte bleiben unverändert; K1–K3 sind in der CSV am betroffenen ID-Befund markiert. GLO-10/GLO-11 stehen nicht als Einzelzeilen in den vorhandenen Teilen; sie werden **nicht** zusätzlich in die 233 importiert. GLO-11 ist unabhängig davon über die fehlende Traceability-Datei und aktuelle Referenz zu bewerten, GLO-10 im fehlenden Teil 08 nachzuholen.

## Stand

**UNVOLLSTÄNDIG – Gesamtprüfung fortsetzen mit Teil 03, anschließend Teil 08 und Teil 09.** Eine genaue erste fehlende ID ist ohne diese Berichte und vollständige Katalogtexte nicht belastbar bestimmbar.

Die Konsolidierung der **233 vorhandenen Einzel-IDs** ist inhaltlich abgeschlossen; keine dieser IDs wurde ausgelassen. Offen bleiben die drei fehlenden Teilberichte, 34 N/P-IDs, benannte Teiltext-/Laufzeitnachweise und die Bestätigung offener Katalogentscheidungen. Nach Ergänzung CSV, Gesamtquoten, Traceability-Menge und Gates erneut konsolidieren; G1 bleibt bis zum Nachweis aller Kriterien gesperrt.

### Prüfung der Konsolidierung

- Quelltabellen nach Einzel-IDs aufgelöst: 233, keine Dubletten; CH16 keine eigene Zeile. Genau die gekennzeichneten K1/K2-Statuskorrekturen und K3-Sollwertpräzisierung.
- Critical-/High-Inventar gegen die Einzel-ID-Menge abgeglichen: 30/123, jede ID genau einmal in der jeweiligen priorisierten Tabelle.
- Pfadkontrolle: nur die ausdrücklich fehlenden drei Teilberichte und Traceability-Datei nicht vorhanden.
- Katalogabgleich: genau eine zusätzliche explizite [B]/[Pn]/[A]-ID außerhalb der Quelltabellen gefunden, GLO-10; in der Traceability-Liste ergänzt, nicht in der Befund-CSV erfunden.
- Keine neuen Produktivtests/Builds ausgeführt: ausschließlich Dokumentkonsolidierung, historische Testergebnisse nicht als neue bestandene Prüfungen ausgegeben.
- CSV-Strukturkontrolle abgeschlossen: UTF-8, Semikolon, exakte neun Kopfspalten, 233 eindeutige Datenzeilen mit Evidenz; vollständige Quell-ID-Abdeckung, 229 unveränderte und vier explizit korrigierte Zeilen. B/Q-Menge, 34 N/P-IDs, alle priorisierten IDs und 180 Traceability-Lücken stimmen mit den Berichtstabellen überein.
- Unabhängige Read-only-Quellen-/Dokumentreview durchgeführt; drei Textpräzisierungen eingearbeitet (JOB-04 statt GQL-03 beim State-at-Befund, eigenständige ENT-03-Pflicht gegenüber CH21(V), Feldbenennung statt unbekanntem AUD-03-Sollenum). Keine weiteren materiellen Einwände gemeldet.
- Secret-Scans beider Ergebnisdateien ohne Befund; `git diff --check` ohne Befund. Gegenüber der Prüfkopie ausschließlich `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/befunde.csv` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/99-gesamtbericht.md` geändert.
- Parallele Validierung aufgerufen: Wrapper meldet „Success“, erklärt aber ausdrücklich, dass das automatische Reviewwerkzeug fehlt. **Automatisierte Code-Review daher nicht verfügbar, kein bestandener Reviewnachweis daraus.** CodeQL wegen ausschließlich trivialer Dokumentänderungen übersprungen, nicht als ausgeführter Sicherheitstest ausgegeben. Die unabhängige Read-only-Review und Strukturkontrollen sind davon getrennte Nachweise.
