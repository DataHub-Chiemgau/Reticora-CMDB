# Phase-1-WP-Auditmatrix (WP-001 – WP-213)

Abnahmeaudit Phase 1, Prüfstand Commit `3a9669a` zuzüglich der im Audit ergänzten Tests (Branch `claude/ultracode-phase1-acceptance-jtfhsq`). Jede Zeile ist eine einzeln nachvollziehbare Prüfung: Einzelprüfung durch einen Auditor, unabhängige adversariale Verifikation, Abgleich mit Real-Stack-E2E-Evidenz und der Defekttest-Welle, Abschlusssynthese mit erneuter adversarialer Prüfung. Defekt-IDs verweisen auf `PHASE1_FINDINGS.md`.

Nachweisquellen: `evidence/` (Baseline-Läufe, Lastläufe, Prüfung der Audit-Tests), `frontend/e2e-real/` (Real-Stack-E2E), `*_auditdefect_test.go` / `tests/audit/` / `*.auditdefect.tsx` (Defekttests, schlagen bis zur Behebung fehl), `data/` (Rohdaten der Einzelprüfungen und der Synthese).

## Statusregeln

- **BESTANDEN:** jedes verbindliche Akzeptanzkriterium ist durch ausgeführte Evidenz belegt (Tests, die liefen, bestanden und das Kriterium gegen echten Code/DB prüfen; gemockte Browsertests belegen nie UI → API → DB) und kein bestätigter P0–P2-Defekt verletzt ein Kriterium dieses WP. Code-Review allein genügt nie. Verknüpfte Befunde außerhalb der Kriterien sind als „verknüpft“ ausgewiesen.
- **TEILWEISE BESTANDEN:** nur ein Teil der Kriterien ist belegt oder ein bestätigter Defekt betrifft nur einen Teil.
- **FEHLGESCHLAGEN:** ein bestätigter Defekt verletzt ein verbindliches Kriterium (beobachtet, reproduziert).
- **NICHT IMPLEMENTIERT / NICHT TESTBAR / UNKLAR:** Funktion fehlt / konkrete technische Blockade / Spezifikation oder Evidenz unzureichend.
- **Priorität des WP:** höchste Priorität der zugerechneten Abweichungen (P0 Sicherheitsrisiko, schwerer Datenverlust oder zentraler Prozess blockiert; P1 wesentliche Funktion fehlt/falsch/unzuverlässig; P2 relevante Einschränkung; P3 geringfügig), sonst „-“.

## Statuszählung

| Status | Anzahl |
|---|---:|
| BESTANDEN | 64 |
| TEILWEISE BESTANDEN | 102 |
| FEHLGESCHLAGEN | 47 |
| NICHT IMPLEMENTIERT | 0 |
| NICHT TESTBAR | 0 |
| UNKLAR | 0 |
| **Summe** | **213** |

## Status je Meilenstein

| Meilenstein | WPs | BESTANDEN | TEILWEISE BESTANDEN | FEHLGESCHLAGEN | NICHT IMPLEMENTIERT | NICHT TESTBAR | UNKLAR |
|---|---:|---:|---:|---:|---:|---:|---:|
| M0.0 Werkzeugkette und Prüfrahmen | 7 | 4 | 3 | 0 | 0 | 0 | 0 |
| M0.1a Mandantentrennung (RLS/Scopes, WithTenant) | 34 | 12 | 11 | 11 | 0 | 0 | 0 |
| M0.1b Authentisierung und Autorisierung | 10 | 3 | 5 | 2 | 0 | 0 | 0 |
| M0.1c Migrationen und Standortmodell | 5 | 3 | 1 | 1 | 0 | 0 | 0 |
| M0.1d Overrides, Reconciliation-Schutz, Datenverlust | 7 | 1 | 4 | 2 | 0 | 0 | 0 |
| M0.2a DB-Rollenvertrag und RLS-Matrix | 2 | 0 | 2 | 0 | 0 | 0 | 0 |
| M0.2b Tokens, API-Keys, Rollen, Operator, Entitlements | 11 | 3 | 3 | 5 | 0 | 0 | 0 |
| M0.2c Kanonisches Datenmodell (DB-05), VLAN/VRF | 6 | 0 | 5 | 1 | 0 | 0 | 0 |
| M0.2d Ingest und Reconciliation | 9 | 4 | 3 | 2 | 0 | 0 | 0 |
| Epic A – Installation und Betrieb | 15 | 3 | 8 | 4 | 0 | 0 | 0 |
| Epic B – Fundament | 36 | 14 | 20 | 2 | 0 | 0 | 0 |
| Epic D – Collector und Discovery | 20 | 5 | 8 | 7 | 0 | 0 | 0 |
| Epic C – API, Kernfunktionen, UI | 40 | 11 | 22 | 7 | 0 | 0 | 0 |
| Lasttest (NFR-10) | 5 | 1 | 4 | 0 | 0 | 0 | 0 |
| Sicherheitsreview | 3 | 0 | 1 | 2 | 0 | 0 | 0 |
| Gate G1 | 3 | 0 | 2 | 1 | 0 | 0 | 0 |

## Übersicht

| WP-ID | Anforderung/Nutzerziel | Status | Prio | Defekte | Abhängigkeiten |
|---|---|---|---|---|---|
| [WP-001](#wp-001) | Backend-Lint auf golangci-lint v2 lauffähig machen: golangci-lint v2 läuft für alle drei Go-Module grün und ist in CI und Makefile identisch gepinnt, damit jedes WP die DoD-01-Aussage 'keine … | TEILWEISE BESTANDEN | P2 | F-123 | keine |
| [WP-002](#wp-002) | Frontend-Pipeline grün: Prettier, Typecheck, Vitest, Build: Die Frontend-CI-Kette (format:check, lint, typecheck, Vitest, build) läuft nacheinander grün und bricht bei jedem Fehler ab; Formatierung e… | BESTANDEN | - | - | keine |
| [WP-003](#wp-003) | Vollständige Testauswahl in CI (alle DB-Tests, Collector, Edgecore): CI führt alle PostgreSQL-Tests ohne Namensfilter aus und zeigt ihre Anzahl im Log; Collector und Edgecore werden mit Race-Detector getestet. | BESTANDEN | P3 | verknüpft: F-283 | WP-001 |
| [WP-004](#wp-004) | Abhängigkeits- und Container-Scans in CI: Jeder PR scannt Go-Module (govulncheck), Frontend-npm-Abhängigkeiten und alle gebauten Container-Images (Trivy); Blockierschwellen folgen S… | TEILWEISE BESTANDEN | P2 | F-124, F-284 | WP-003; E-01, E-31; WP-100 (führte das postgres-Image ein) |
| [WP-005](#wp-005) | Traceability-Datei, CI-Prüfung und PR-Vorlage (GLO-11/DOD-01): Jede Anforderungs-ID ist über docs/traceability.csv auf Epic, Testdatei und WP rückführbar; ein Go-Prüfprogramm validiert die Datei auf jed… | TEILWEISE BESTANDEN | P2 | F-121, F-285, F-286 | WP-003; E-30, E-06; WP-213 (Gate-Regel [B]/[Q]) |
| [WP-006](#wp-006) | Migrations-Roundtrip mit Schemavergleich, vollständige Down-Migration…: Jede Migration hat ein funktionierendes up/down-Paar; ein Integrationstest beweist up → down-all → up mit identischem Schema und dass jedes… | BESTANDEN | P3 | verknüpft: F-287 | WP-003 |
| [WP-007](#wp-007) | RLS-Katalogtest mit verwalteter Ausnahmeliste: Ein Katalogtest erzwingt für jede Mandantentabelle ENABLE und FORCE RLS, Policies für alle Kommandos mit USING und WITH CHECK sowie Client-… | BESTANDEN | - | - | WP-006; WP-022, WP-023, WP-024, WP-040, WP-065 (schlossen a… |
| [WP-008](#wp-008) | Zentrale Mandantentransaktion database.WithTenant mit vollständigem S…: Jede mandantenbezogene DB-Transaktion setzt app.org_id, app.user_id, app.client_scope, app.site_scope und app.team_scope transaktionslokal … | BESTANDEN | - | - | WP-007; Basis für WP-009..WP-022 und alle WithTenant-Nutzer… |
| [WP-009](#wp-009) | Autoritative Scope-Ableitung in Session und Principal: Effektive Rechte und Client-/Site-/Team-Scope stammen aus DB-Rollenzuweisungen (RBA-03-Vereinigung), werden in Session und Principal getrag… | TEILWEISE BESTANDEN | P2 | F-049, F-181 | WP-008; E-11 (Team-Semantik, vorläufig); WP-029 (Team-RLS-P… |
| [WP-010](#wp-010) | WithTenant-Migration: CMDB-Kern I: Alle DB-Zugriffe von ci, citype, relationship, relationshiptype und topology laufen über WithTenant mit Principal-Scope; client-gescopte Nu… | FEHLGESCHLAGEN | P2 | F-047, F-145, F-161 | WP-009, WP-041, WP-208 (Kanalmatrix), API-03-Fehlervertrag;… |
| [WP-011](#wp-011) | WithTenant-Migration: CMDB-Kern II: lifecycle, override, history, contact, credential und privacy greifen nur über WithTenant mit Principal-Scope auf die DB zu; Client-Isolati… | FEHLGESCHLAGEN | P2 | F-047 | WP-009, WP-041, WP-208. |
| [WP-012](#wp-012) | WithTenant-Migration: Identität und Rechte: user, permission, entitlement, identity (API-Key-Store), security, middleware (Idempotenz) und api nutzen WithTenant mit Principal-Scope; A… | FEHLGESCHLAGEN | P2 | F-047 | WP-009, WP-041, WP-067, WP-208. |
| [WP-013](#wp-013) | WithTenant-Migration: Discovery und IPAM: Discovery und IPAM laufen über WithTenant mit Principal-Scope; client-gescopte Nutzer sehen/ändern keine fremden Netz-/Discovery-Daten; Feh… | TEILWEISE BESTANDEN | P0 | F-003 | WP-009, WP-041, WP-208. |
| [WP-014](#wp-014) | WithTenant-Migration: Standorte: rack, tenantapi, locationnode (jetzt locations) und location laufen über WithTenant mit Principal-Scope; Objekte ohne eigene Client-Spalte … | FEHLGESCHLAGEN | P0 | F-002, F-047, F-180 | WP-009, WP-041, WP-053, WP-208; DB-seitiger Site-Scope für … |
| [WP-015](#wp-015) | WithTenant-Migration: Suche, Sichten, Export (Requestpfade): Such-, Saved-View- (inkl. Query-Engine) und Export-Requestpfade laufen über WithTenant mit Principal-Scope; client-gescopte Nutzer erhalten… | FEHLGESCHLAGEN | P2 | F-047 | WP-009, WP-041, WP-208. |
| [WP-016](#wp-016) | WithTenant-Migration: Webhook, Audit, Monitoring (Requestpfade): Webhook-, Audit- und Monitoring-Requestpfade laufen über WithTenant mit Principal-Scope; client-gescopte Principals lesen/schreiben keine f… | FEHLGESCHLAGEN | P2 | F-047, F-052, F-183 | WP-009, WP-041, WP-208; WP-190 (MetricNames), WP-040. |
| [WP-017](#wp-017) | WithTenant-Migration: Asset I (asset, assignment, reservation, compos…: Jeder DB-Zugriff von asset, assignment, reservation und composition läuft in WithTenant mit Principal-Scope; client-gescopte Nutzer sehen/ä… | FEHLGESCHLAGEN | P2 | F-047 | WP-009, WP-041, WP-023, WP-025. |
| [WP-018](#wp-018) | WithTenant-Migration: Asset II / Lager (movement, stocktake, consumab…: Lager-Module greifen nur über WithTenant mit Principal-Scope auf die DB zu; client-gescopte Nutzer sehen/ändern keine fremden Lagerobjekte;… | FEHLGESCHLAGEN | P2 | F-047 | WP-009, WP-041, WP-023. |
| [WP-019](#wp-019) | WithTenant-Migration: Module I (document, desk, keymgmt, training): Dokumente, Desks, Schlüssel und Trainings werden nur in WithTenant mit Principal-Scope gelesen/geschrieben; client-gescopte Nutzer sehen/än… | TEILWEISE BESTANDEN | P0 | F-004, F-048 | WP-009, WP-041, WP-023, WP-025/WP-028 (document_link-Scope,… |
| [WP-020](#wp-020) | WithTenant-Migration: Module II (ticket, sla, maintenance, compliance): Tickets, SLAs, Wartungsfenster und Compliance-Daten nur über WithTenant mit Principal-Scope; client-gescopte Nutzer sehen/ändern nur eigene… | FEHLGESCHLAGEN | P0 | F-001, F-004, F-047, F-048 | WP-009, WP-041, WP-023, WP-028/029 (Scope-Spalten maintenan… |
| [WP-021](#wp-021) | WithTenant-Migration: Module III (form, workflow, agent, ai, iga): Formulare, Workflows, Endpoint-Agents, AI-Chunks und IGA-Daten nur über WithTenant mit Principal-Scope; client-gescopte Nutzer sehen/ändern… | FEHLGESCHLAGEN | P2 | F-047, F-056 | WP-009, WP-041, WP-023. |
| [WP-022](#wp-022) | Systemworker mandantenweise; Systemausnahmen (app.system) eingrenzen: Hintergrund-Worker finden Arbeit nur unter app.system und ändern Daten je Organisation in WithTenant; System-Policies sind SELECT-only; Wor… | BESTANDEN | P3 | verknüpft: F-184 | WP-015, WP-016, WP-017, WP-041, WP-103 (Worker-Metriken), W… |
| [WP-023](#wp-023) | Client-Policies für neun Tabellen und NULL-Client-Schreibverbot (Quer…: Tabellen mit client_id erzwingen den Client-Scope in USING und WITH CHECK; Principals ohne org-weiten Scope dürfen keine Zeilen mit client_… | BESTANDEN | - | - | WP-007, WP-008; WP-065 (Matrix). |
| [WP-024](#wp-024) | Globale Katalogzeilen schreibgeschützt; Tabellen ohne Org-Spalte klär…: Globale Katalogzeilen (organization_id NULL) sind lesbar, aber nicht durch die App-Rolle schreibbar; Tabellen ohne Org-Spalte sind nach E-1… | BESTANDEN | - | - | WP-007; WP-065. |
| [WP-025](#wp-025) | Client/Site in CI-abhängige Kindtabellen denormalisieren (Querschnitt…: Kindzeilen eines CI (Interfaces, IPs, Beziehungen, Kontakte, Historie, Feldwerte, Findings, Discovery-Ergebnisse, Rack-Mounts, Review-Items… | TEILWEISE BESTANDEN | P0 | F-003, F-050, F-051, F-145, F-182 | WP-023, WP-010, WP-013, WP-027; reviews (WP-088/089, Gruppe… |
| [WP-026](#wp-026) | Kanonischer Location-Baum: Tabelle location mit ltree, Elternmatrix, …: Ein kanonischer Standortbaum (site > building > room > rack; site > warehouse > zone > shelf > bin) mit ltree-Pfad; DB erzwingt Elternmatri… | BESTANDEN | - | - | WP-023; Basis für WP-027, WP-029, WP-032, WP-053/074. |
| [WP-027](#wp-027) | Site-Scope in allen Policies mit site_id (CH25): Ein auf Sites beschränkter Principal sieht/schreibt in jeder Tabelle mit eigener oder denormalisierter site_id nur Zeilen seiner Sites; die… | BESTANDEN | P2 | verknüpft: F-053 | WP-025, WP-026; WP-028/029/032; Eigentümer-WP bzw. PO-Entsc… |
| [WP-028](#wp-028) | Modultabellen mit Objektbezug erben Bereichsrechte (Dokumente, Ticket…: Dokumente, Tickets, Ticket-Kommentare, Wartungsfenster und Schlüsselzuweisungen erben client/site des referenzierten CI/Assets/Tickets/Schl… | FEHLGESCHLAGEN | P0 | F-004, F-048, F-185, F-186 | WP-027, WP-019, WP-020; WP-214 (Modul-Gates begrenzen G1-Ex… |
| [WP-029](#wp-029) | Team-, Raum- und Personenbezug: Desks, Buchungen, Trainings, Ticket-T…: Team-gescopte Principals sehen/schreiben nur Tickets ihrer Teams und Trainingszuweisungen ihrer Teammitglieder, geschnitten mit Client/Site… | BESTANDEN | - | - | WP-028, WP-026, E-11 (vorläufig); Wirksamkeit hängt an L-T1… |
| [WP-030](#wp-030) | Dokument-Blobs: serverseitige Storage-Keys mit Org-Präfix: Ein Dokument referenziert nur Blobs der eigenen Organisation; Storage-Keys werden serverseitig unter org/<org_id>/ erzeugt; Downloads prüfe… | TEILWEISE BESTANDEN | P3 | F-187, F-188 | WP-019, WP-028, WP-214 (Modul-Gate). |
| [WP-031](#wp-031) | Topologie-Traversal mit Sichtbarkeit im CTE: Topologie- und Impact-Traversals laufen nur über sichtbare, nicht gelöschte CIs; unsichtbare/gelöschte Zwischenknoten verbinden nie sichtba… | BESTANDEN | P3 | verknüpft: F-161 | WP-025 (Kanten-Scope, Zwei-Endpunkt-Policy); WP-206/WP-335 … |
| [WP-032](#wp-032) | PG-Volltextsuche mit Client-/Site-Scope und Treffer-Leserecht: Die Suche liefert nur Treffer, die der Aufrufer sehen darf: search_document trägt abgeleitete client/site seiner Entität unter RLS, und jed… | BESTANDEN | - | - | WP-027, WP-015; WP-074; L-T1-11 (Assets ohne site_id) wirkt… |
| [WP-033](#wp-033) | OpenSearch-Pfad: Client-/Site-Filter und Feldrechte: Nutzer einer Organisation mit optionalem OpenSearch-Backend erhalten Treffer nur innerhalb ihres aktuellen Client-/Site-Scopes; der Scope s… | TEILWEISE BESTANDEN | P2 | F-054 | WP-032, E-03, E-09, Rack-Platzierung (RCK-02). |
| [WP-034](#wp-034) | Strukturierte Suche und Beziehungssuche: Leserecht, Scope, Tiefe 5: Nutzer führen strukturierte Filterabfragen und Saved Views inkl. Upstream-/Downstream-Walks aus; Ergebnisse respektieren das Leserecht des … | TEILWEISE BESTANDEN | P2 | F-055 | WP-027, WP-015, WP-187, WP-045. |
| [WP-035](#wp-035) | Export-Jobs mit Ersteller-Scope ausführen und abrufen: Ein Nutzer startet einen asynchronen Export; der Worker rendert mit exakt dem Client/Site/Team-Scope des Erstellers, und nur der Ersteller … | TEILWEISE BESTANDEN | P2 | F-059 | WP-022, WP-027, WP-030, JOB-01/JOB-03 (generisches Job-Mode… |
| [WP-036](#wp-036) | GraphQL-BFF: Leserechte je Resolver, Feldprojektion, Entitlement: Der GraphQL-BFF gibt nichts über RBAC-Rechte, Entitlements und RLS-Scope des Aufrufers hinaus preis, liefert nur selektierte Felder und leh… | BESTANDEN | - | - | WP-027, WP-184/185, WP-072. Hinweis: rackLayout (WP-185) er… |
| [WP-037](#wp-037) | RAG-Chunks mit Quellobjekt-Scope; keine Antwort ohne Treffer: Der AI-Assistent antwortet nur aus Chunks lesbarer Quellobjekte (Client/Site-RLS plus Leserecht); ohne zulässige Quellen kein Provider-Aufr… | BESTANDEN | - | - | WP-027, WP-021, WP-076, E-03 (AI-Add-on in G1 aus). |
| [WP-038](#wp-038) | Agent-Registrierung mit Enrollment-Token und Client/Site-Bindung: Endpoint-Agents registrieren sich nur mit gültigem Einmal-Token, das Client und optional Site bindet; Roaming-Geräte behalten ihren Client,… | BESTANDEN | P3 | verknüpft: F-189 | WP-027, WP-021, WP-051, E-03. |
| [WP-039](#wp-039) | Spike TEC-06: RLS auf Hypertable, komprimierten Chunks und Continuous…: Eine evidenzbasierte Entscheidung, wie Mandantenisolation für TimescaleDB-Metriken (Hypertable, komprimierte Chunks, CAgg) erreicht wird, a… | TEILWEISE BESTANDEN | P2 | F-051 | WP-007; Grundlage für WP-040, WP-190. |
| [WP-040](#wp-040) | Metrik-RLS, 1-Tages-Chunks und korrektes Aggregat: Metriken mit 1-Tages-Chunks, Kompression, Retention und Stundenaggregat; Nutzer lesen nur Metriken ihrer Organisation und ihres Scopes; Lan… | TEILWEISE BESTANDEN | P2 | F-051, F-052 | WP-039, WP-016, WP-190 (MetricNames, Gruppe T7), WP-065. |
| [WP-041](#wp-041) | Architekturtest: kein Datenbankzugriff am Mandantenkontext vorbei: Ein dauerhafter automatischer Guard in go test ./... und CI verhindert DB-Zugriffe außerhalb WithTenant/WithSystem; nur dokumentierte Syste… | TEILWEISE BESTANDEN | P3 | F-190, F-191 | WP-010…WP-022, E-08. |
| [WP-042](#wp-042) | Deaktivierung wirksam: Login/Refresh prüfen Status, Sessions und API-…: Ein Admin deaktiviert einen Benutzer; Sessions, Refresh-Cookies und API-Keys enden sofort, ein neuer Login reaktiviert nicht, und Owner off… | TEILWEISE BESTANDEN | P2 | F-060, F-195, F-196 | WP-012; WP-066/WP-050 (SPA-Refresh L-T2-18 verhindert produ… |
| [WP-043](#wp-043) | Org-Zuordnung über Nutzerattribut und zugelassenen Erstlogin: Die Organisation stammt aus dem Keycloak-Attribut organization_id; ein Erstlogin legt nur bei Zulassung (Einladung/Signup) einen Benutzer a… | TEILWEISE BESTANDEN | P3 | F-197, F-198 | WP-042, WP-044 (Realm), TLC-01, TLC-03 |
| [WP-044](#wp-044) | Kein Demo-Administrator im Produktivimport; Passwort-/Lockout-Policy: Produktive Installation ohne bekannten Demo-Admin; individueller Admin mit temporärem Passwort; Passwort-, Lockout- und MFA-Pflicht für Adm… | FEHLGESCHLAGEN | P0 | F-005, F-061 | WP-043, WP-046, TLC-01/02/03; install-smoke (nicht lokal au… |
| [WP-045](#wp-045) | Korrekte Aktionszuordnung im Authz-Mapping inkl. verschachtelter Spez…: Jede API-Operation verlangt das semantisch richtige Recht; Spezialaktionen sind nicht über breitere Elternrechte erreichbar. | BESTANDEN | P3 | verknüpft: F-062, F-199 | WP-009, WP-046, WP-047 |
| [WP-046](#wp-046) | Ein Rollenmodell: OIDC-Rollen auf DB-Rollen abbilden; Viewer ohne Cre…: Session-Rechte stammen nur aus DB-Rollen; IdP-Gruppen werden fest auf Seed-Rollen abgebildet; Rollenmatrix nach RBA-02; Viewer ohne Credent… | BESTANDEN | P3 | verknüpft: F-017, F-062, F-200, F-201 | WP-045, WP-044, WP-009, WP-168; E-13 (vorläufig) |
| [WP-047](#wp-047) | Keine Klartext-Geheimnisse an Menschen: Decrypt-Pfad, Webhook-Header,…: Keine API-Antwort oder Logzeile zeigt Klartext-Geheimnisse. | BESTANDEN | P3 | verknüpft: F-202 | WP-045, WP-147 |
| [WP-048](#wp-048) | Redfish: Zertifikatsprüfung und protokollgetrennte Zugangsdaten: Der Collector sendet Zugangsdaten nur an geprüfte Endpunkte und nur die des passenden Protokolls. | FEHLGESCHLAGEN | P0 | F-010 | WP-003, WP-147, WP-150 (T4: WMI-Transport) |
| [WP-049](#wp-049) | SSRF-Schutz: gemeinsamer Egress-Client für Webhooks und SCIM: Ausgehende Aufrufe an Nutzer-URLs (Webhooks, IGA/SCIM) erreichen keine internen Ziele; URLs werden auch beim Speichern geprüft. | TEILWEISE BESTANDEN | P2 | F-062, F-203, F-204, F-205 | WP-022; notify NTF-04; Webhook-UI (WP-191/WP-192) |
| [WP-050](#wp-050) | Middleware-Reihenfolge, Pre-Auth-Limit und atomare, principalgebunden…: Auth-Versuche werden pro IP vor der Prüfung begrenzt; Validierung und Idempotenz laufen nach Authz; Idempotenzschlüssel sind principal- und… | TEILWEISE BESTANDEN | P1 | F-018, F-063, F-206 | WP-012, WP-066 (Cookie-Refresh 2041fcb), WP-166 |
| [WP-051](#wp-051) | Agent-Kanal: TLS ohne Fallback, Authentifizierung, korrekter Payload/…: Der Endpoint-Agent meldet Telemetrie nur über TLS mit seinem Enrollment-Geheimnis an den Agent-Telemetrie-Endpunkt. | TEILWEISE BESTANDEN | P2 | F-064, F-207 | WP-038, WP-214 (Modul agents in G1 aus) |
| [WP-052](#wp-052) | Migration 000056: Quarantäne statt Löschen inkonsistenter Composition…: Betreiber verlieren bei der FK-Härtung durch 000056 keine Daten; inkonsistente Zeilen landen in einer RLS-geschützten Quarantäne, Altinstal… | BESTANDEN | P3 | verknüpft: F-224 | WP-006 |
| [WP-053](#wp-053) | Location-API auf kanonischen Baum umstellen; BuildTree-Fehler beheben: Nutzer pflegen einen kanonischen Standortbaum über /api/v1/locations und die LocationTreePage mit vollständigem Baum, Feldfehlern und CRUD … | TEILWEISE BESTANDEN | P2 | F-079, F-225, F-226 | WP-026; WP-055 (409-Regeln) |
| [WP-054](#wp-054) | CI-/Asset-Standortbezüge auf location; Site/Room im CI ableiten (DB-0…: CI- und Asset-Standort zeigen auf den kanonischen Baum; site/room werden abgeleitet und sind nicht direkt schreibbar; Migration verlustfrei… | BESTANDEN | - | - | WP-053; WP-055 |
| [WP-055](#wp-055) | Löschregeln 409 und Umzug mit location_change, Audit und Pfadaktualis…: Standorte mit CIs, Assets, Mounts oder Kindern sind über keinen Löschpfad löschbar (409 mit Arten); Umzug aktualisiert Pfade atomar mit loc… | FEHLGESCHLAGEN | P2 | F-023, F-024, F-227 | WP-054; tenantapi-, IPAM-, Discovery-Scope-WPs |
| [WP-056](#wp-056) | Instanzattribute: Discovery-Schutz und Audit: Instanzattribute werden nie von Discovery überschrieben; Änderungen an Instanzdefinitionen werden auditiert. | BESTANDEN | - | - | WP-045, WP-013, WP-091 |
| [WP-057](#wp-057) | Manuelle Schreibpfade erzeugen Overrides; RFC-7396-Merge-Patch: Manuelle CI-Änderungen folgen JSON Merge Patch (null löscht) und jedes manuell geänderte Feld wird geschützter Override – auf allen manuell… | FEHLGESCHLAGEN | P1 | F-020, F-080, F-081, F-228, F-229 | WP-010, WP-011; WP-181, WP-058, WP-061 |
| [WP-058](#wp-058) | Zentrale fail-closed Schreibentscheidung für Automationsquellen mit R…: Jede automatisierte Feldschreibung wird zentral nach REC-03 entschieden, überschreibt nie Overrides (Review statt Überschreiben) und schlie… | BESTANDEN | P3 | verknüpft: F-230 | WP-057; E-32 |
| [WP-059](#wp-059) | Workflow set_ci_field und Agent-Updates über die zentrale Entscheidung: Workflow (Rang 92) und Agent (Rang 85) schreiben über die zentrale Entscheidung mit Provenienz, überschreiben keine Overrides und erzeugen … | TEILWEISE BESTANDEN | P2 | F-083, F-231 | WP-058, WP-021, WP-173, WP-089 |
| [WP-060](#wp-060) | Effektive CI-Sicht konsistent; Override-Clear synchronisiert: Jede CI-Lesesicht zeigt den effektiven Wert; Override-Aufheben stellt den beobachteten Wert atomar und auditiert wieder her, auch sichtbar … | TEILWEISE BESTANDEN | P2 | F-100, F-232 | WP-058; WP-061 (Versionierung der Override-Schreibvorgänge,… |
| [WP-061](#wp-061) | Optimistische Nebenläufigkeit: CI-Version, ETag/If-Match, 409/412: Zwei Bearbeiter überschreiben sich nicht still; Rang≥92-Schreibvorgänge erhöhen die Version, veraltetes If-Match → 409/412, UI sendet If-Ma… | FEHLGESCHLAGEN | P1 | F-020, F-021, F-082, F-228 | WP-060, Bulk-WP (WP-181), CI-Detail |
| [WP-062](#wp-062) | Spool ohne stillen Verlust: 24-h-Pufferung, Backpressure, Verlustmeld…: Der Collector puffert unbestätigte Ergebnisse ≥24 h, drosselt statt zu löschen, meldet jeden Verlust an den Server und liefert in Quellzeit… | TEILWEISE BESTANDEN | P2 | F-084, F-085, F-233, F-234 | WP-003, WP-104, WP-143 |
| [WP-063](#wp-063) | Wartungsankündigung nicht ohne Versand als sent markieren: Eine Wartungsankündigung behauptet keine Zustellung; Status bleibt pending bis zur Zustellung, die UI zeigt den echten Status. | TEILWEISE BESTANDEN | P3 | F-235, F-236 | WP-020, WP-028; Epic B Benachrichtigungskette |
| [WP-064](#wp-064) | DDL-Vertrag CH19 für die App-Rolle; FORCE für alle Mandantentabellen: Die App-Rolle kann Laufzeit-Indizes (CH19/DB-04) ohne SUPERUSER/BYPASSRLS anlegen; jede Mandantentabelle erzwingt RLS auch für den Owner; d… | TEILWEISE BESTANDEN | P2 | F-057, F-192, F-193 | WP-027; WP-208; Installer-WPs (install-cloud.sh/Compose, Gr… |
| [WP-065](#wp-065) | RLS-Testmatrix: Org/Client/Site/Team × CRUD × NULL; Ausnahmeliste leer: Ein automatisierter, tabellengetriebener Test belegt, dass jede Mandantentabelle Org-, Client-, Site- und Team-Scope (inkl. kombinierter Sc… | TEILWEISE BESTANDEN | P2 | F-354 | WP-029, WP-064, WP-040, WP-032, WP-037, WP-038, WP-055, WP-… |
| [WP-066](#wp-066) | Session-Token nach AUT-02: 15 min, Claims, kid, Refresh-Cookie, Redis…: 15-min-RS256-Token mit AUT-02-Claims und kid; Nutzer bleiben über ein rotierendes HttpOnly-Refresh-Cookie angemeldet; Widerruf über Redis. | FEHLGESCHLAGEN | P1 | F-017, F-018, F-065, F-066 | WP-042, WP-050, WP-009 |
| [WP-067](#wp-067) | API-Keys nach AUT-04: Präfixe, Owner-Rechteschnitt, Verwaltung, Rotat…: rk_live_/rk_test_-Keys mit Schnittmenge aus Key- und Owner-Rechten; anlegen, listen, widerrufen, rotieren; Lookup ohne Tenant-Kontext nur z… | FEHLGESCHLAGEN | P0 | F-006, F-007 | WP-066, WP-069, RBA-03 |
| [WP-068](#wp-068) | Permission-Katalog: neun fehlende Schlüssel, location:* statt site:*: Katalog (SQL/Go) enthält exakt die RBA-01-Schlüssel und die Authz nutzt sie. | FEHLGESCHLAGEN | P2 | F-067 | WP-046, RBA-03 |
| [WP-069](#wp-069) | Service-Accounts mit Rollen und Scopes; Webhook-Subscriptions daran b…: Service-Accounts mit Rollen/Scopes; gebundene Keys und Subscriptions erhalten nur Ereignisse, die der Account lesen darf. | FEHLGESCHLAGEN | P0 | F-007, F-008, F-009 | WP-067, WP-136, RBA-03 |
| [WP-070](#wp-070) | Operator-Pfad /admin mit Operator-Token und eigener Audit-Hashkette: Operatoren erreichen /api/v1/admin/* nur mit personalisierter MFA-Identität oder Break-Glass-Token; jede Aktion in operator_audit; Break-Gl… | TEILWEISE BESTANDEN | P2 | F-068, F-069, F-208 | WP-066; ENT-04 (WP-074) |
| [WP-071](#wp-071) | Entitlement-Modell und Featureschlüssel nach ENT-01/ENT-02: Entitlements je Org mit limits, valid_until, source und den ENT-02-Schlüsseln; cmdb_core immer aktiv; Daten übernommen. | BESTANDEN | P3 | verknüpft: F-209 | WP-012; Basis WP-072/073; Upgrade-Folge in L-T2-36 (WP-072) |
| [WP-072](#wp-072) | Entitlement-Gates lückenlos: Export, Ingest-Alias, GraphQL; kein Defa…: Jeder gegatete REST-/GraphQL-Pfad prüft das passende Feature; fehlende Zeile = nicht berechtigt; nach Ablauf stoppt nur Discovery/Ingest; B… | FEHLGESCHLAGEN | P2 | F-070, F-071, F-210 | WP-071, WP-036, COL-08 |
| [WP-073](#wp-073) | Limits bei Ingest: unlicensed_ci statt Umgehung; Downgrade ohne Daten…: Über max_cis entdeckte Geräte werden als unlicensed_ci gehalten; Limit-Erhöhung übernimmt sie; Downgrade löscht nichts; Quoten in REST-Schr… | TEILWEISE BESTANDEN | P2 | F-072 | WP-072, WP-058, WP-070 |
| [WP-074](#wp-074) | Entitlement-Schreiben nur über Operator (admin-entitlements): Tenants lesen Entitlements nur; nur der Operator schreibt unter /admin/orgs/{id}/entitlements, auditiert. | BESTANDEN | P3 | verknüpft: F-208, F-211 | WP-070, WP-071, WP-046 |
| [WP-075](#wp-075) | CH14: IGA und KI nur als separate Add-on-Entitlements: Kein Plan enthält IGA/KI implizit; nur eigene Entitlement-Zeilen schalten frei, sonst 403. | BESTANDEN | P3 | verknüpft: F-212 | WP-071, WP-072, WP-214 |
| [WP-076](#wp-076) | KI-Embeddings nur mit Org-Opt-in und Entitlement; Air-Gap-Abschaltung: CI-Daten erreichen den Embedding-Provider nur mit Opt-in und ai-Add-on; Air-Gap schaltet jeden externen KI-Aufruf ab. | TEILWEISE BESTANDEN | P2 | F-073, F-074 | WP-075, WP-037, WP-214 |
| [WP-077](#wp-077) | DB-05: Asset-Hoheit für Seriennummer, Standort, physischen Lifecycle …: Bei Asset-Verknüpfung ist das Asset kanonisch; das CI zeigt die Daten read-only, spiegelt den Standort, direkte Schreibversuche → 422, Seri… | TEILWEISE BESTANDEN | P3 | F-237, F-238, F-239 | WP-054, WP-060, WP-078 |
| [WP-078](#wp-078) | DB-05/RCK-03: Rack-Platzierung setzt Standort; Historie und Audit: Mount/Umpositionieren setzt den Standort auf das Rack, Unmount auf den Raum; jede Änderung mit ci_change und Audit in derselben Transaktion. | TEILWEISE BESTANDEN | P3 | F-240, F-241 | WP-077, WP-054, WP-079, WP-175 |
| [WP-079](#wp-079) | DB-05: Enthaltensein über ci.parent_ci_id; Strukturbeziehungen nur pr…: Physisches Enthaltensein wird einmal als parent_ci_id mit Zyklenschutz gespeichert; strukturelle Beziehungen sind nur Projektionen (422), T… | TEILWEISE BESTANDEN | P2 | F-086, F-242, F-243, F-244 | WP-078, WP-031, WP-127/WP-176, TEN-05 |
| [WP-080](#wp-080) | DB-05: Software-Lifecycle nur für Typen mit Lifecycle-Modell; Health …: lifecycle_state nur bei Typen mit Lifecycle-Modell; ci.health read-only und aus Monitoring berechnet. | FEHLGESCHLAGEN | P2 | F-087 | WP-040, WP-077, WP-214, MON-02 |
| [WP-081](#wp-081) | VLAN-Katalog (NET-08) und VLAN-Bezüge von Subnetz und Interface: VLANs als Mandantenkatalog mit Eindeutigkeit nach NET-08 und RLS; Subnetze/Interfaces referenzieren per FK, Altwerte migriert. | TEILWEISE BESTANDEN | P2 | F-088 | WP-027, WP-013, WP-082 |
| [WP-082](#wp-082) | VRF-Modell (CH10): Default-VRF je Org, Subnetz-/IP-Eindeutigkeit pro …: Jede Org hat genau eine Default-VRF 'global'; VRFs nach TEN-05 geschützt, Subnetz/IP-Eindeutigkeit je VRF, Schreiben mit vrf:manage. | TEILWEISE BESTANDEN | P2 | F-089 | WP-081, WP-065, WP-125 |
| [WP-083](#wp-083) | DeviceRecord-Vertrag zwischen Collector und Server: Collector und Server teilen einen versionierten DIS-05-Vertrag; alter Vertrag nur übergangsweise mit Deprecation-Header. | BESTANDEN | - | - | WP-062, WP-013 |
| [WP-084](#wp-084) | Quellzeit observed_at: Zukunftsgrenze, Ordnung, feldweise Provenienz: Abgleich nach Quellzeit; Zukunfts-Records abgewiesen und gezählt; feldweise Provenienz mit Rang und observed_at. | BESTANDEN | P3 | verknüpft: F-245 | WP-083, WP-058 |
| [WP-085](#wp-085) | Ingest-Pipeline: ingest.>-Konsument, record_id-Idempotenz 48 h, Ordnu…: Records laufen über den NATS-Konsumenten im Mandantenkontext, jede record_id einmal in 48 h, seriell je CI; jeder Record wird verarbeitet. | TEILWEISE BESTANDEN | P2 | F-090 | WP-084, WP-333/334 |
| [WP-086](#wp-086) | Identitätsabgleich: Qualifikationen, Blocklisten, Clientbindung, Host…: Geräte werden nach REC-02 im Client über den gesamten Bestand abgeglichen; Hostname nur als Review. | BESTANDEN | P3 | verknüpft: F-246 | WP-085, WP-082 |
| [WP-087](#wp-087) | Soft-gelöschte CIs im Abgleich: resurrected_device statt Duplikat: Ein wieder auftauchendes gelöschtes Gerät wird kein Duplikat; Review mit Reaktivieren oder neuem CI (CI-13). | FEHLGESCHLAGEN | P1 | F-022 | WP-086, WP-088 |
| [WP-088](#wp-088) | Review-Modell: zehn Typen, Aktionen merge/new/dismiss/accept, review:…: Eine Review-Queue mit REC-08-Typen und Aktionen unter review:resolve, dedupliziert, Eskalation >14 Tage im Dashboard, Benachrichtigung. | TEILWEISE BESTANDEN | P2 | F-091, F-247 | WP-087, WP-196, WP-140 |
| [WP-089](#wp-089) | Konfliktwerte erhalten; override_conflict-Lebenszyklus (accept/dismis…: Reviewer sehen unveränderte Eingangswerte; ein override_conflict je Feld; accept übernimmt, dismiss unterdrückt bis zur nächsten Änderung. | TEILWEISE BESTANDEN | P3 | F-231, F-248 | WP-088, WP-084, WP-196 |
| [WP-090](#wp-090) | last_seen_at ohne Historie; unknown→active; Interface/IP-Entfernung n…: Sichtungen aktualisieren last_seen_at ohne Historie, holen unknown-CIs zurück, entfernen fehlende Interfaces/IPs nach drei Läufen mit Histo… | BESTANDEN | P3 | verknüpft: F-249 | WP-085, WP-171 |
| [WP-091](#wp-091) | Reservierte Namensräume und CI-10-Speichermodell: Die vier reservierten Namen sind auf keinem Schreibpfad als Attributschlüssel/Feldname nutzbar; CI-10-Speichermodell, Altdaten migriert. | FEHLGESCHLAGEN | P2 | F-092 | WP-089, WP-056 |
| [WP-092](#wp-092) | Install-Smoke-Matrix Ubuntu 22.04/24.04 und Debian 12; strikte Health…: CI belegt auf jeder CH22-Plattform, dass der Cloud-Installer einen vollständig gesunden Stack hochfährt; die Health-Auswertung scheitert be… | TEILWEISE BESTANDEN | P3 | F-288 | WP-003; speist WP-211/WP-213; TLS-Domain-Smoke-Variante nöt… |
| [WP-093](#wp-093) | Installer-Vorprüfung: OS/Release, Docker >= 24, Compose v2, Sizing-Ta…: Der Installer verweigert nicht unterstützte Plattformen und alte Docker-/Compose-Versionen mit klarer Supportmeldung und warnt bei Untersch… | BESTANDEN | - | - | WP-092; E-17 (Skalierungsziel bestätigt) |
| [WP-094](#wp-094) | TLS/ACME im Installer: Erkennung, Kettenprüfung, funktionsfähiger Sta…: Mit TLS-Domain liefert der Installer zuerst ein Bootstrap-Zertifikat, bezieht dann ein vertrauenswürdiges Let's-Encrypt-Zertifikat, prüft d… | FEHLGESCHLAGEN | P1 | F-033, F-034, F-139 | WP-092 (keine TLS-Smoke-Variante); WP-095 (Domain-Pfad bric… |
| [WP-095](#wp-095) | Installer: Issuer-Prüfung extern und containerseitig: Der Installer prüft, dass der konfigurierte OIDC-Issuer vom Host und aus dem Backend-Container erreichbar ist und genau den konfigurierten … | FEHLGESCHLAGEN | P1 | F-035 | WP-094 (TLS-Pfad); WP-097 (server nutzt config.Load für -ch… |
| [WP-096](#wp-096) | Wiederholte Installation: Secrets/Keycloak-DB synchron, .env erhalten: Ein erneuter Installerlauf (auch mit rotierten Passwörtern, INS-05) hält Datenbanken und Keycloak über TCP verifiziert konsistent zur .env … | TEILWEISE BESTANDEN | P2 | F-125 | WP-095; vollständiger Installationspfad lokal blockiert (Ne… |
| [WP-097](#wp-097) | Readiness und fail-closed Startvalidierung: /readyz meldet den Zustand jeder konfigurierten Abhängigkeit (DB mit Extensions, NATS, Redis, S3, SMTP) mit 503 bei Ausfall; der Server sta… | FEHLGESCHLAGEN | P1 | F-036, F-126, F-127 | WP-003; betrifft WP-099 und alle K8s-Deployment-WPs; WP-095… |
| [WP-098](#wp-098) | Unabhängige Betriebsschalter für alle Worker (OPS-02): Betreiber können jeden Hintergrund-Worker unabhängig ein- und ausschalten (Konvention RETICORA_WORKER_<NAME>_ENABLED); ein deaktivierter Wo… | BESTANDEN | P3 | verknüpft: F-289 | WP-097, WP-022; WP-157 (Offline-Detector), WP-132 (Audit-Ru… |
| [WP-099](#wp-099) | Kubernetes: Pflicht-Master-Key injizieren: Kubernetes-Deployments (base, staging, prod) injizieren den Pflicht-Master-Key aus einem Secret, Pods starten nur mit Key, die kustomize-Bu… | TEILWEISE BESTANDEN | P1 | F-036 | WP-097 (führte den Pflicht-Issuer ein; Behebung dort); WP-1… |
| [WP-100](#wp-100) | Backup-Kette: WAL-Archivierung und Basisbackups, S3-Versionierung: Betreiber erhalten eine funktionierende Backup-Kette: kontinuierliches WAL-Archiv plus tägliches pgBackRest-Basisbackup in Compose und K8s,… | TEILWEISE BESTANDEN | P2 | F-128, F-129, F-290, F-291 | WP-099 (K8s), WP-101 (Restore-Nachweis), WP-105 (Terraform-… |
| [WP-101](#wp-101) | Restore-Nachweis RPO 15 min / RTO 4 h: Betreiber erhalten einen gemessenen PITR-Restore-Nachweis (RPO 15 min / RTO 4 h) als Artefakt und in der Doku; der Workflow scheitert bei G… | TEILWEISE BESTANDEN | P2 | F-130, F-292 | WP-100 (pgBackRest-Image/Compose); Netzwerkzugang zu Alpine… |
| [WP-102](#wp-102) | Signierte Releases: cosign, SBOM, verpflichtende Signaturprüfung im U…: Betreiber und Collectoren installieren und aktualisieren nur Release-Artefakte, die keyless vom Release-Workflow des Projekts signiert sind… | TEILWEISE BESTANDEN | P2 | F-131, F-293, F-294 | GitHub-Actions-OIDC (Fulcio/Rekor); WP-104 (outage-Gating);… |
| [WP-103](#wp-103) | Betriebsbeobachtung: Worker-Fehlermetriken und Alarmregeln: Betreiber sehen Lauf-, Fehler- und Rückstaumetriken jedes Hintergrund-Workers und werden bei Ausfall oder Rückstau über die in deploy/monit… | FEHLGESCHLAGEN | P2 | F-132, F-133, F-134, F-295 | WP-137 (NTF, geliefert 6da3c58), WP-130/146/147/153 (später… |
| [WP-104](#wp-104) | Ausfall- und Backpressure-Tests aller Dienste (OPS-06): Das Betriebsteam erhält den Nachweis, dass ein Ausfall von PG, NATS, Redis, S3, SMTP oder der Server-Erreichbarkeit für den Collector keine… | TEILWEISE BESTANDEN | P2 | F-135, F-136, F-296 | WP-137 (SMTP), WP-062 (Spool), WP-097 (readyz), Installatio… |
| [WP-105](#wp-105) | Betriebsreferenz EU/Dedicated, Air-Gap-Abgrenzung, Subprozessoren: Kunden und Betreiber erhalten ein dokumentiertes Referenzbetriebsmodell, eine vom Betreiber gelieferte Subprozessorenliste und Infrastruktu… | TEILWEISE BESTANDEN | P2 | F-120, F-137 | extern: Betreiberlieferung der Subprozessorenliste (E-35, A… |
| [WP-106](#wp-106) | make seed reparieren (fehlende seed.sql): Entwickler und CI führen make seed aus, erhalten die SIM-01-Seed-Daten über den Seed-Befehl aus WP-203, und jeder Seed-Fehler lässt den Bef… | BESTANDEN | - | - | WP-203 (cmd/seed), WP-003 (CI-migrations-Job) |
| [WP-107](#wp-107) | Organisation, Client, Site: Zeitzone/Währung/Locale, Eindeutigkeiten,…: Org hat validierte IANA-Zeitzone, ISO-4217-Währung und BCP-47-Locale (GET/PATCH /org); Client-Namen eindeutig je Org, Site-Namen je (Org, C… | TEILWEISE BESTANDEN | P3 | F-302 | WP-055; Location-Tree-WPs (WP-026/WP-053) besitzen location… |
| [WP-108](#wp-108) | Building/Room/Rack: Eindeutigkeiten und height_units: Building-/Room-/Rack-Namen eindeutig je Elternebene; Rackhöhe height_units 1–60 (Default 42) in DB und API, explizite 0 → 422 bei POST und … | TEILWEISE BESTANDEN | P2 | F-140 | WP-107; WP-126 (RCK-01 Mount-Prüfung) |
| [WP-109](#wp-109) | Durchgängige Hierarchie Org → Client → Site mit Scopewirkung belegen: Die Hierarchie Org → Client → Site → Location → CI/Asset ist eine Struktur, deren Scope auf jeder Ebene wirkt; ein Site-gebundener Nutzer s… | FEHLGESCHLAGEN | P0 | F-014, F-141 | WP-065, RBA-03-Scopeaggregation, TEN-02 |
| [WP-110](#wp-110) | Dev-Login nur außerhalb production: Unsignierte Dev-Tokens nur in development; Start mit RETICORA_ALLOW_INSECURE_DEV_AUTH in staging/production wird verweigert. | BESTANDEN | - | - | WP-066 |
| [WP-111](#wp-111) | Self-Signup: Org, Slug, Default-VRF, erster org_admin, Trial, Verifik…: Interessent registriert sich, bestätigt den Mail-Link und setzt ein Passwort; Org, Slug, Default-VRF, org_admin, Trial-Entitlements und ope… | BESTANDEN | P3 | verknüpft: F-303 | WP-082, WP-070, WP-071 |
| [WP-112](#wp-112) | Self-Signup-Schutz: Captcha, Pre-Auth-Limit, Wegwerf-Domain-Blocklist: Signup ist durch Pre-Auth-Limit, ALTCHA, Blocklist und max. 5 Signups/IP/h geschützt; Ablehnungen als RFC 7807. | BESTANDEN | P3 | verknüpft: F-304 | WP-111, WP-050 |
| [WP-113](#wp-113) | Operator legt Organisationen mit Plan und Limits an: Operator legt über /admin/orgs Org mit Plan und Limits an (Signup-Kette ohne Verifikation, operator_audit). | BESTANDEN | - | - | WP-111, WP-074, WP-070 |
| [WP-114](#wp-114) | Einladungen: Ausgabe, Annahme, Erneuerung; 7 Tage, atomar einmalig: Admin lädt mit Rolle/Scope ein (7 Tage, einmalig); Eingeladener nimmt an und erhält app_user mit Rolle/Scope; Erneuerung möglich; Annahme h… | TEILWEISE BESTANDEN | P0 | F-015, F-041, F-142, F-305 | WP-043, WP-050, WP-140, WP-109 |
| [WP-115](#wp-115) | CI-Typen: i18n-Labels, is_observable, schema_version; logische Seed-T…: CI-Typen haben de/en-Labels, is_observable und schema_version; genau 17 physische und 6 logische Seeds, keine ausgeschlossenen Typen. | BESTANDEN | - | - | WP-024, WP-116, WP-119 |
| [WP-116](#wp-116) | attribute_definition als einzige Quelle; generiertes read-only attrib…: Attribute zentral in attribute_definition (MET-10), attribute_schema generiert und nicht schreibbar, Ressource /api/v1/attribute-definition… | TEILWEISE BESTANDEN | P3 | F-306, F-307 | WP-115, WP-091, WP-119, Phase-2 WP-256 |
| [WP-117](#wp-117) | Serverseitige Validierung aller Phase-1-Datentypen inkl. typisierter …: Jeder CI-Wert (manuell oder Discovery) wird serverseitig gegen MET-11 und Feldregeln geprüft; manuelle Anlage mit Name, Typ, Attributen fun… | FEHLGESCHLAGEN | P0 | F-016, F-045, F-143 | WP-116, WP-120, WP-239 |
| [WP-118](#wp-118) | Regel-AST: api/rules.schema.json, Go-Auswertung, TS-Vorschau, gemeins…: Eine Regelsprache (required/pattern/range/enum), verbindlich in Go, Vorschau in TS, gemeinsame Konformanzfälle. | BESTANDEN | - | - | WP-117, WP-116 |
| [WP-119](#wp-119) | Schema-Komposition Basis + Typkette, Materialisierung, schema_version…: Effektives Schema nach MET-16 komponiert, materialisiert und versioniert; CI speichert schema_version; neue Typen ohne Release nutzbar (MET… | TEILWEISE BESTANDEN | P0 | F-016, F-308, F-309 | WP-118, WP-116, WP-120, WP-239 |
| [WP-120](#wp-120) | CI-Felder und Create-Pfad nach CI-01: CI hat alle CI-01-Felder; manuelle Anlage übernimmt alle Felder und setzt is_manual. | TEILWEISE BESTANDEN | P0 | F-016, F-045, F-144 | WP-080, WP-117, WP-239 |
| [WP-121](#wp-121) | CI-Indizes nach CI-02: CI-Abfragen sind durch die CI-02-Indizes gestützt; Migration ohne lange Sperren (dokumentiert). | BESTANDEN | - | - | WP-079 |
| [WP-122](#wp-122) | Nachweis Trennung Status/Lifecycle (LCY-01): ci.status und lifecycle_state sind getrennte Zustandsräume; Änderung des einen ändert nie den anderen. | BESTANDEN | - | - | WP-080 |
| [WP-123](#wp-123) | Interfacemodell nach NET-01: Netzwerk-Interfaces haben exakt die NET-01-Felder, Name eindeutig je CI, (org, mac)-Index. | BESTANDEN | P3 | verknüpft: F-310, F-311 | WP-082, WP-090 |
| [WP-124](#wp-124) | IP-Adressen nach NET-03 (Typ, VRF in Teilindizes): IP-Adressen tragen type statt status, Datenübernahme korrekt, Eindeutigkeit je Org/VRF/Adresse/Interface. | BESTANDEN | P3 | verknüpft: F-312 | WP-123, WP-082 |
| [WP-125](#wp-125) | REST-Ressourcen vrfs und vlans: VRF (vrf:manage) und VLAN (ipam:write) per REST-CRUD mit Authz, OpenAPI und Frontend-Typen. | BESTANDEN | - | - | WP-082, WP-081, WP-045 |
| [WP-126](#wp-126) | Rack-Belegung: 409 und Sperre für Kompositionskinder: Belegte, kollidierende oder zu große Positionen → 409; Face-aware-Exclusion in DB; Kompositionskinder nicht separat montierbar. | BESTANDEN | - | - | WP-078, WP-079, WP-175; Shrink-Pfad der Rackhöhe ist WP-108… |
| [WP-127](#wp-127) | Beziehungstypen: impact_direction, redundant, Kategorien; connected_t…: Beziehungstypen tragen den REL-01-Vertrag; Seeds korrigiert; Strukturschlüssel nicht speicherbar. | BESTANDEN | P3 | verknüpft: F-242, F-313 | WP-079, WP-176, WP-160 |
| [WP-128](#wp-128) | Beziehungskanten: Interface-Endpunkte, Bestätigung, Verifikation, Con…: Kanten nach REL-02 mit Interface-Endpunkten, Verifikation, Confidence-Spalte mit CHECK, Eindeutigkeit inkl. Interfaces; Port-Kanten bleiben… | TEILWEISE BESTANDEN | P1 | F-042, F-145, F-314 | WP-127, WP-123, WP-090, WP-160; WP-010/WP-163/WP-208 (500-D… |
| [WP-129](#wp-129) | Generisches Jobmodell backend/internal/jobs: Eine generische, mandantengetrennte Jobtabelle, in der auch Export- und Discovery-Jobs erscheinen (Job Center, /api/v1/jobs). | TEILWEISE BESTANDEN | P2 | F-146 | WP-065, WP-182, WP-130 |
| [WP-130](#wp-130) | NATS-Work-Queue für Jobs; Webhook-Erstversand-Claim; Export-Wiederauf…: Jobs laufen über JetStream-Work-Queue mit Ack/Redelivery; Erstversand von Webhooks mit Claim; Exporte setzen nach Neustart fort. | TEILWEISE BESTANDEN | P2 | F-147, F-315 | WP-129, WP-098, WP-182/183 |
| [WP-131](#wp-131) | Scheduler mit Single-Runner (Leader-Lock): Gemeinsamer Scheduler führt die JOB-02-Systemläufe aus; genau eine Replika führt per Advisory-Lock; abschaltbar (OPS-02). | TEILWEISE BESTANDEN | P1 | F-044 | WP-130, WP-135, WP-140, WP-136 |
| [WP-132](#wp-132) | Audit-Hashkette je Org mit Sequenz und Prüfwerkzeug: Jede Org hat eine manipulationssichere Kette mit lückenloser Sequenz; audit-verify meldet die erste Bruchstelle; keine Gabelung bei Nebenlä… | TEILWEISE BESTANDEN | P1 | F-045, F-316 | WP-065, WP-133, WP-135 |
| [WP-133](#wp-133) | Audit-Felder und lückenlose Erfassung aller Mutationen: Jede Mutation schreibt lückenlos einen Audit-Datensatz mit Pflichtfeldern inkl. Vorher/Nachher; Abfrage über GET /audit-log; Retention selb… | TEILWEISE BESTANDEN | P1 | F-045, F-148, F-149 | WP-132, WP-045, WP-134, WP-135, Audit-Viewer-WP (Epic F4) |
| [WP-134](#wp-134) | Metamodell-Export und Audit von Metamodell-Änderungen: Admins exportieren das Metamodell als JSON; jede Metamodell-Änderung ist mit Request-/Correlation-ID auditiert. | BESTANDEN | - | - | WP-116, WP-133 |
| [WP-135](#wp-135) | Aufbewahrung und Löschläufe (NFR-06): Daten werden genau so lange wie NFR-06 verlangt aufbewahrt und dann automatisch gelöscht; Audit-Löschung erhält die Kette. | TEILWEISE BESTANDEN | P2 | F-150, F-317, F-318 | WP-131, WP-132, WP-084 |
| [WP-136](#wp-136) | Domain-Events mit Outbox und stabilem Event-Vertrag: Jede Domänenänderung erzeugt genau ein Event in derselben Transaktion; zuverlässige Zustellung an NATS und Webhooks mit stabilem Vertrag. | TEILWEISE BESTANDEN | P1 | F-043, F-044, F-151, F-319, F-320, F-321 | WP-130, WP-140, Webhook-WPs |
| [WP-137](#wp-137) | Benachrichtigungskanäle (E-Mail, In-App, Webhook) und internal/notify: Organisation definiert Kanäle (zentrales/eigenes SMTP, In-App, Webhook) mit verschlüsselter Konfiguration. | TEILWEISE BESTANDEN | P2 | F-152, F-153, F-154 | WP-136, WP-047, WP-139, WP-140 |
| [WP-138](#wp-138) | Empfängerauflösung nach Rolle/Scope (NTF-03): Benachrichtigungen erreichen genau die Berechtigten: Nutzer nach Rolle/Team/Abo, CI- und Client-Kontakte, nur mit Scope-Berechtigung. | TEILWEISE BESTANDEN | P2 | F-155, F-322 | WP-137, WP-046, WP-140 |
| [WP-139](#wp-139) | Zustellung mit Retry/Deduplizierung; REST notifications und notificat…: Zuverlässige Zustellung (Retry, Dedup, Status, Ruhezeiten, Opt-out, Digest); Inbox und Kanäle per REST. | TEILWEISE BESTANDEN | P2 | F-154, F-156, F-157 | WP-137, WP-138, WP-140 |
| [WP-140](#wp-140) | Die acht Phase-1-Benachrichtigungsereignisse (NTF-05): Jedes der acht Ereignisse benachrichtigt die richtige Zielgruppe über Outbox → Notifier. | TEILWEISE BESTANDEN | P1 | F-041, F-044, F-323, F-324 | WP-139, WP-136, WP-114, WP-131 |
| [WP-141](#wp-141) | Schlüsselrotation (SEC-06): Master-Key und Org-DEKs ohne Datenverlust und Downtime rotieren; Schlüsselversion je Ciphertext; alter Schlüssel stillgelegt. | TEILWEISE BESTANDEN | P2 | F-158 | WP-131, WP-047, COL-08 |
| [WP-142](#wp-142) | Attribut-DDL-Job: searchable/unique-Flags, CREATE INDEX CONCURRENTLY …: Admins markieren Attribute als searchable/unique; Job baut Index nebenläufig; Server weist Duplikate ab, auch parallel. | TEILWEISE BESTANDEN | P2 | F-159, F-325 | WP-116, WP-130, WP-064 |
| [WP-143](#wp-143) | Collector-Statusmodell, Site-Bindung, Offline-Übergang nach 5 min: Collectors haben die Status pending/active/offline/disabled/paused nach COL-01. Ein Scheduler setzt sie nach 5 min ohne Heartbeat offline u… | BESTANDEN | P3 | verknüpft: F-256, F-257 | WP-131, WP-072 (ENT-07), WP-145 |
| [WP-144](#wp-144) | Enrollment-Token: 24 h, atomarer Einmalverbrauch: Ein Betreiber erzeugt einen Einmalcode mit 24 h Gültigkeit; auch bei parallelem Einsatz entsteht genau ein Collector. | BESTANDEN | - | - | WP-143, WP-145 |
| [WP-145](#wp-145) | Collector-Zertifikate: CSR, CN-Bindung, Erneuerung, Dual-Trust, Sperre: Ein Collector meldet sich per CSR an und erhält ein 90-Tage-Zertifikat mit CN = Collector-ID. Ab 60 % der Laufzeit erneuert er es automatis… | FEHLGESCHLAGEN | P0 | F-012, F-025, F-026, F-101, F-102, F-258 | WP-144, WP-146, WP-147, WP-143 |
| [WP-146](#wp-146) | NATS-mTLS-Endpunkt und Firewall-Matrix: Collectors verbinden sich ausgehend per mTLS mit 4222 und bleiben auf ihre eigenen Subjects beschränkt. Kunden erhalten eine korrekte Firew… | TEILWEISE BESTANDEN | P2 | F-103, F-104, F-259 | WP-145, WP-147, WP-153, Install-WPs |
| [WP-147](#wp-147) | Verschlüsseltes Credential-Bundle über NATS: Der Server liefert jedem Collector nur die benötigten Credentials, verschlüsselt auf den Collector-Schlüssel und über mTLS-NATS. Der Collec… | TEILWEISE BESTANDEN | P0 | F-012, F-013, F-027 | WP-145, WP-146, WP-153, WP-047 |
| [WP-148](#wp-148) | Signierte Collector-Auto-Updates mit Kanal und Rollback: Collectors aktualisieren sich nur mit signierten Artefakten ihres Kanals und rollen nach einem Fehlstart zurück. | TEILWEISE BESTANDEN | P3 | F-260 | WP-102, WP-145, install-vm.sh |
| [WP-149](#wp-149) | Sweep: ICMP/ARP, Parallelität ≤ 256, Default-Ports: Der Sweep findet Hosts per ICMP, ARP und TCP auf genau 22/80/443/623/5985/8006, mit höchstens 256 parallelen Probes und dokumentierter Capa… | BESTANDEN | P3 | verknüpft: F-261, F-262 | WP-083, WP-150, WP-153 |
| [WP-150](#wp-150) | Plugin-Interface Name/Probe/Collect mit DeviceRecord; Collect-Pfade a…: Alle acht Protokoll-Plugins erfüllen DIS-03, und die Hauptschleife ruft für jeden Probe-Treffer Collect auf. | BESTANDEN | - | - | WP-149; Protokolltiefe in WP-152 |
| [WP-151](#wp-151) | Klassifikation SNMP → Redfish → Banner, YAML-Profile, generic_device-…: Geräte werden über die Kette SNMP → Redfish → Banner mit YAML-Profilen klassifiziert. Nur Geräte ohne Profiltreffer werden generic_device m… | FEHLGESCHLAGEN | P1 | F-030, F-263 | WP-150, WP-149, Reviews-Framework |
| [WP-152](#wp-152) | Protokolltiefe Phase 1: SNMPv3, SSH-Befehle, Redfish/WMI/IPMI vervoll…: Discovery fragt Geräte per SNMPv3 authPriv, SSH-Allowlist sowie Redfish/WMI/IPMI ab und verwendet die konfigurierten Credentials, ohne sie … | FEHLGESCHLAGEN | P0 | F-010, F-011, F-028, F-029, F-105 | WP-048, WP-147, WP-155, E-02 |
| [WP-153](#wp-153) | Discovery-Scopes: Modell, CRUD, scope_suggested nach Enrollment: Admins definieren Scopes per REST/UI unter discovery:manage. Collectors erhalten sie über NATS und scannen jeden Scope mit seinen Einstellu… | FEHLGESCHLAGEN | P0 | F-013, F-025, F-027, F-031, F-106, F-107, F-108, F-264, F-265, F-267 | WP-146, WP-147, WP-082, WP-156, WP-154 |
| [WP-154](#wp-154) | Getrennte Intervalle: Sweep 24 h, Polls je Protokoll, Mindestinterval…: Sweeps laufen nach dem Scope-Zeitplan, Polls im eigenen Intervall (mindestens 5 min), und der Spool wird kontinuierlich geleert. | TEILWEISE BESTANDEN | P2 | F-109, F-266 | WP-153, edgecore buffer, WP-146 |
| [WP-155](#wp-155) | Discovery-Fehlerzustände, Metriken, Job-/Scopefehler in UI: Bei Discovery-Fehlern sehen Nutzer je Ziel, Scope und Job Ursache, Protokoll und Ziel in der UI. Betreiber erhalten Metriken und Logs. | TEILWEISE BESTANDEN | P1 | F-029, F-105, F-258, F-267 | WP-153, WP-154, WP-152 |
| [WP-156](#wp-156) | Collector-CLI `enroll --server --code`; Scope-Vorschlag bestätigen: Der Betreiber führt den in der UI angezeigten Befehl aus, die Identität landet im einheitlichen Pfad, und er bestätigt die Scope-Vorschläge… | TEILWEISE BESTANDEN | P1 | F-027, F-031, F-110, F-265 | WP-153, WP-200, install-vm.sh, Dockerfile |
| [WP-157](#wp-157) | CI-Offline-Detektor: last_seen_at > 3 x Scopeintervall -> unknown, ci…: Beobachtbare, entdeckte CIs werden nach mehr als 3× Scope-Intervall ohne Sichtung unknown und lösen ci.offline aus. Der Lauf ist schaltbar … | FEHLGESCHLAGEN | P2 | F-108, F-268 | WP-153, WP-136, WP-120, WP-115, WP-159 |
| [WP-158](#wp-158) | Topologie: Confidence LLDP 1,0 / FDB 0,6, Trunkfilter, Confidence-Spa…: Entdeckte Nachbarn werden zu connected_to-Kanten mit der Confidence nach TOP-01. Unbekannte Schlüssel werden zum Review. | TEILWEISE BESTANDEN | P1 | F-032, F-269 | WP-128, WP-085, WP-088, Collector-Protokoll (DIS-04/05) |
| [WP-159](#wp-159) | Wiederbeobachtung frischt Kanten auf; Cleanup nach max(7 d, 5 x Inter…: Erneute Sichtung frischt Discovery-Kanten auf. Unbestätigte Kanten werden nach max(7 d, 5× Intervall) mit Event entfernt. Eine Quellen-Poli… | FEHLGESCHLAGEN | P2 | F-108, F-111, F-270, F-271 | WP-158, WP-153, WP-136, WP-160, WP-157 |
| [WP-160](#wp-160) | Suppression: Loeschung manueller Kanten, Typbezug, fail-closed; Disco…: Ein Nutzer entfernt eine falsch entdeckte Kante; Discovery erzeugt sie nicht neu. Manuelle Kanten bleiben unberührt, und beim Ladefehler de… | TEILWEISE BESTANDEN | P3 | F-145, F-271, F-272, F-273, F-274 | WP-159, WP-158, WP-161, WP-127/128, WP-045 |
| [WP-161](#wp-161) | Versorgungskette: PDU/USV, redundante Einspeisung, Outlet-Mapping: Aus den Outlet-Namen von PDU und USV entstehen powered_by-Kanten mit korrekter Redundanzkennung für IMP-04. | FEHLGESCHLAGEN | P2 | F-112, F-275 | WP-150, WP-127, WP-179, WP-160, WP-334 |
| [WP-162](#wp-162) | RETICORA_SIMULATE: deterministischer 30-Geraete-Generator am echten I…: Mit RETICORA_SIMULATE=true entsteht deterministisch eine 30-Geräte-Flotte über den echten Ingest; in production ist das gesperrt. | BESTANDEN | P3 | verknüpft: F-276 | WP-150, WP-085, WP-146, WP-161 |
| [WP-163](#wp-163) | RFC-7807 durchgängig: problem+json, trace_id, einheitlicher 429-Writer: Jeder API-Fehler ist application/problem+json mit trace_id = Request-ID; 429 nutzt denselben Writer. | BESTANDEN | P3 | verknüpft: F-145, F-206, F-213 | WP-050, WP-061; WP-128, WP-031 (Statuscodes) |
| [WP-164](#wp-164) | Cursor-Pagination: signiert, max. 200, durchgängig: Jeder Listenendpunkt liefert {data, next_cursor} mit HMAC-Cursor; limit 50/max 200. | TEILWEISE BESTANDEN | P2 | F-075, F-214, F-215 | WP-163 |
| [WP-165](#wp-165) | Listenfilter filter[field]-Operatoren, Sortier-Whitelist, fields, q: Gemeinsamer Parser mit Whitelist aus Feldmetadaten; unbekannte Felder 422; JSONB nur für searchable. | TEILWEISE BESTANDEN | P3 | F-216 | WP-164, WP-116 |
| [WP-166](#wp-166) | Ratelimits: Session-Bucket, 6.000/min-Org-Bucket, X-RateLimit-Header: 600/min je Key/Session, 6.000/min je Org in Redis, Header auf allen Antworten; ein Client kann andere nicht aushungern. | FEHLGESCHLAGEN | P1 | F-019, F-076, F-217 | WP-163, WP-050, AUT-02. Lastlauf: Lauf 2 alle API-Budgets o… |
| [WP-167](#wp-167) | API-Lebenszyklus: Deprecation/Sunset-Header, No-DB-Profil-Parität: Deprecated Operationen tragen Deprecation/Sunset/Successor (≥12 Monate); beide Profile bedienen genau die dokumentierten Operationen. | BESTANDEN | P3 | verknüpft: F-218, F-219, F-220 | WP-163, WP-168, WP-189 |
| [WP-168](#wp-168) | Abweichende REST-Pfade auf Katalogvertrag umstellen (zehn Ressourcen): Die zehn Ressourcen laufen unter API-05-Pfaden; alte Pfade mit Deprecation; Frontend nutzt neue Pfade. | TEILWEISE BESTANDEN | P2 | F-017, F-077, F-078, F-218 | WP-167, WP-088, WP-116, WP-129, WP-189 |
| [WP-169](#wp-169) | CI-include (Beziehungen, Kontakte, Asset) und Restore-Endpunkt: CI mit eingebetteten Beziehungen/Kontakten/Asset abrufen und gelöschte CIs innerhalb der Aufbewahrung wiederherstellen. | TEILWEISE BESTANDEN | P3 | F-221, F-222, F-223 | WP-168, WP-170, WP-135 |
| [WP-170](#wp-170) | CI-Löschen: decommissioned, Traversal-Stopp, 90-Tage-Hard-Delete mit …: Löschen setzt deleted_at/decommissioned, löst Asset, Lifecycle bleibt; Traversal überspringt; nach 90 Tagen Hard-Delete inkl. IPs. | BESTANDEN | - | - | WP-169 (L-T2-55), WP-131, WP-031, WP-135 |
| [WP-171](#wp-171) | Einheitliche CI-Historie mit Standort/Zeit; Audit-Abfrage: Ein Operator sieht eine chronologische CI-Historie (Änderungen, Audit, location_change, Overrides) mit Zeit und Standort auf der Detailseit… | TEILWEISE BESTANDEN | P2 | F-093, F-094, F-095, F-237 | WP-055, WP-133, WP-046/E-13, WP-090 |
| [WP-172](#wp-172) | CI-Typwechsel: Discovery-Upgrade generic_device → spezifisch, manuell…: Discovery hebt generic_device auf einen spezifischen Typ; manueller Wechsel mit Pflichtfeld-Befunden, Historie (type_changed) und Audit. | TEILWEISE BESTANDEN | P2 | F-095, F-250 | WP-119, WP-136, WP-151, WP-171 |
| [WP-173](#wp-173) | CI-Merge: Referenzübernahme, Override-Konflikte, Tombstone/301, Snaps…: Duplikate werden zusammengeführt: Referenzen wandern, Konflikte werden Reviews, Opfer liefert 301, Snapshot 30 Tage, parallele Merges 409. | TEILWEISE BESTANDEN | P2 | F-096, F-251 | WP-170, WP-089, WP-129 |
| [WP-174](#wp-174) | CI-Unmerge innerhalb der Snapshot-Frist: Innerhalb von 30 Tagen wird ein falscher Merge rückgängig gemacht; danach 410. | TEILWEISE BESTANDEN | P2 | F-097 | WP-173 |
| [WP-175](#wp-175) | Rack-API: GET layout, PUT mounts/{ci_id} mit Fehlervertrag: Rack-Belegung über GET layout lesen, CIs per PUT montieren/verschieben mit 409; alte Routen deprecated; Frontend umgestellt. | TEILWEISE BESTANDEN | P3 | F-252, F-253 | WP-126, WP-167, WP-078 |
| [WP-176](#wp-176) | Impact entlang impact_direction; connected_to als Heuristik: Impact folgt impact_direction; connected_to nur als gekennzeichnete Netzheuristik; falsche Tests korrigiert. | TEILWEISE BESTANDEN | P2 | F-098, F-099, F-254 | WP-127, WP-031 |
| [WP-177](#wp-177) | Impact: 20 Hops, Pfade mit Typfolge, Kürzestpfad, Kategorien, projizi…: Impact bis 20 Hops mit kürzestem Pfad und Typfolge, Kategorienfilter, projiziertem Enthaltensein, ohne N+1. | BESTANDEN | P3 | verknüpft: F-255 | WP-176, WP-079, WP-206/WP-335 |
| [WP-178](#wp-178) | Nachweis assetfreie CIs in Historie und Impact: Logische CIs ohne Asset nehmen an Beziehungen, Suche, Topologie, Historie und Impact teil – per Integrationstest belegt. | BESTANDEN | - | - | WP-171, WP-177 |
| [WP-179](#wp-179) | Redundanz-Postfilter in Go: In der Impact-Analyse gilt ein redundant versorgtes CI (powered_by, member_of_cluster) nur dann als betroffen, wenn alle seine Versorgungen… | FEHLGESCHLAGEN | P2 | F-160 | WP-177 (Pfade), WP-161 (Power); die WP-180-Endpunkte erben … |
| [WP-180](#wp-180) | Mehrfachausfall /impact?ci_ids= und /impact/{ci_id}; 422 über 2.000: Mehrfachausfälle werden über /impact?ci_ids= und /impact/{ci_id} gemeinsam analysiert; /topology ohne Root antwortet über 2.000 Knoten mit … | TEILWEISE BESTANDEN | P2 | F-160, F-161, F-326 | WP-179 (Redundanzdefekt), WP-168 (Pfade); WP-195 nutzt die … |
| [WP-181](#wp-181) | Bulk-API: bis 500 IDs, ab 100 als Job, Ergebnis je ID: Statusänderungen für bis zu 500 CIs in einem Request, ab 100 IDs als Job (202); Ergebnis je ID; die UI nutzt die Bulk-API und zeigt Einzelf… | FEHLGESCHLAGEN | P1 | F-020, F-162, F-327, F-328 | WP-129/WP-130 (Jobmodell), WP-163 (Fehler); Cancel-Semantik… |
| [WP-182](#wp-182) | Generische Job-API /api/v1/jobs und Jobmetriken: Generische Job-API mit Liste, Detail, Cancel, Revert (E-25) und Ergebnis sowie Jobmetriken (Dauer, Fehler, Rückstau, Alter des ältesten que… | FEHLGESCHLAGEN | P2 | F-162, F-163, F-329 | WP-181 (ci.bulk-Runner), WP-183 (RunDetached), WP-130 (Queu… |
| [WP-183](#wp-183) | Lange Vorgänge als Jobs: Review-Merge, State-Snapshot: Review-Merge und State-Snapshot laufen als Jobs; der Snapshot liefert den rekonstruierten Zustand im Umfang nach JOB-04 (E-25). | TEILWEISE BESTANDEN | P2 | F-164, F-330, F-331 | WP-182 (Job-API), WP-173 (CI-Merge), WP-171/AUD-08 (Histori… |
| [WP-184](#wp-184) | GraphQL-BFF mit gqlgen unter /bff/graphql, nur Session, Tiefen-/Kompl…: GraphQL-BFF mit gqlgen unter /bff/graphql, ausschließlich Session-Auth (keine API-Keys), mit SEC-10-Limits (Tiefe 10, Komplexitätsbudget, 1… | BESTANDEN | P3 | verknüpft: F-332 | WP-036 (gql-authz); WP-185 baut darauf auf. |
| [WP-185](#wp-185) | Fehlende Resolver mit Dataloader: Die GQL-02-Resolver topology, rackLayout, searchEverything, reviewInbox und jobs sind mit Dataloader (ohne N+1) und nur lesend verfügbar. | BESTANDEN | P3 | verknüpft: F-333 | WP-184, WP-182, WP-180. |
| [WP-186](#wp-186) | Suche: Locations und Kontakte als Treffer: Locations und Kontakte werden laufend indexiert und als Suchtreffer nur mit location:read bzw. contact:read und im Scope des Aufrufers geli… | BESTANDEN | P3 | verknüpft: F-334 | WP-032 (search-pg), WP-053 (loc-api), WP-242 (PII-Vault). |
| [WP-187](#wp-187) | Filter-AST AND/OR/NOT (Server), Grundlage für Sichten: Serverseitiger Filter-AST mit AND/OR/NOT und Vergleichsoperatoren (inkl. Attribut- und Asset-Feldern), validiert und per Whitelist in SQL ü… | TEILWEISE BESTANDEN | P2 | F-165, F-179 | WP-165, WP-034; Grundlage für WP-188 (filter_spec.where) un… |
| [WP-188](#wp-188) | Gespeicherte Sichten: sort, columns, shared_with_roles, Zugriff privat: Gespeicherte Sichten mit VIE-01-Feldern (sort, columns, is_shared, shared_with_roles, eigene Tabelle); fremde private Sicht per GET → 404; … | TEILWEISE BESTANDEN | P2 | F-166, F-335, F-336, F-337 | WP-187 (where-AST); VIE-02 [P2] baut darauf auf. |
| [WP-189](#wp-189) | Export-Format: CSV mit BOM/Semikolon, Feldmanifest, Pfad exports: Export über POST /exports als Job (202), CSV UTF-8 mit BOM und Semikolon, Presign-Download 15 min, nur sichtbare Daten, Feldmanifest der au… | BESTANDEN | P3 | verknüpft: F-338 | WP-035 (Export-Scope), WP-168 (Pfade), Blob/MinIO-Konfigura… |
| [WP-190](#wp-190) | CI-Metriken /cis/{id}/metrics mit Detail-Linie; ci_id in Uploads: Per-CI-Metriken über GET /cis/{id}/metrics mit den MON-02-Basismetriken (Sweep, SNMP-Interface-Zähler, UPS/PDU), Collector-Uploads mit ci_i… | FEHLGESCHLAGEN | P1 | F-046, F-167, F-168, F-339 | WP-040 (Metrik-RLS-Views), WP-150 (Power-Plugin), DIS-10 SN… |
| [WP-191](#wp-191) | UI-Basis: Scope-Bedienung (Org/Client/Site) im Layout: Ein Scope-Umschalter (Org/Client/Site) im Layout; Listen und Detailseiten berücksichtigen den gewählten Scope; nur zugängliche Scopes sind … | TEILWEISE BESTANDEN | P2 | F-077, F-078, F-169, F-340 | WP-065/WP-109 (Backend-RLS Client/Site-Scope); E-02 offen (… |
| [WP-192](#wp-192) | UI-Basis: Tastaturbedienung und Fokusführung gemeinsamer Bausteine: Modal, Select, Tabellen und Command-Palette sind vollständig tastaturbedienbar (Fokusfalle, Escape, Pfeiltasten, Fokusrückgabe), mit Tests … | TEILWEISE BESTANDEN | P2 | F-077, F-078, F-170, F-171 | WP-002 (Frontend-CI); WP-193 (axe) ergänzt; E-02 offen. |
| [WP-193](#wp-193) | Browsermatrix und WCAG 2.1 AA (axe), Raumpositionierung ohne Drag-and…: Playwright-Browsermatrix nach NFR-07 (Chromium/Firefox/WebKit, Edge-Kanal, Mobil) mit axe-AA-Prüfung aller Hauptseiten in CI ohne Verstöße;… | FEHLGESCHLAGEN | P2 | F-172, F-173, F-341, F-342 | WP-002 (Frontend-CI), WP-192 (Tastaturbausteine); i18n-Teil… |
| [WP-194](#wp-194) | i18n: englische Enumwerte, Statusbeschriftungen, Metamodell-Labels al…: Persistierte Enums sind englisch, alle Statuswerte werden über den i18n-Katalog angezeigt, die UI rendert Metamodell-Labels aus den WP-115-… | FEHLGESCHLAGEN | P1 | F-016, F-174, F-175, F-343 | WP-115 (LocalizedText display_name), WP-143 (Collector-Stat… |
| [WP-195](#wp-195) | Topologie-UI: Startdialog über 2.000 Knoten, Simulation mit mehreren …: Die Topologie-UI zeigt über 2.000 Knoten einen Startdialog mit Standort-/Typauswahl und erlaubt eine Ausfallsimulation mit Mehrfachauswahl … | TEILWEISE BESTANDEN | P2 | F-161, F-326, F-344 | WP-180 (Mehrfach-Impact, 2.000-Grenze), TEN-09 (T15). |
| [WP-196](#wp-196) | Review-Inbox mit Diff, Einzel- und Bulk-Auflösung: Eine Review-Inbox mit Filter, Diff-Ansicht, Einzelaktionen merge/new/dismiss/accept und Bulk-Auflösung. | TEILWEISE BESTANDEN | P2 | F-176, F-345, F-346 | WP-088/089 (Review-Modell und -Effekte, Eigentümer des new-… |
| [WP-197](#wp-197) | Benachrichtigungs- und Jobcenter: Ein Benachrichtigungscenter (gelesen/ungelesen) und ein übergreifendes Jobcenter (Fortschritt, Cancel, Ergebnis). | TEILWEISE BESTANDEN | P2 | F-162, F-347, F-348 | WP-182 (Mirror-/Cancel-Semantik), WP-189 (Export-Worker), W… |
| [WP-198](#wp-198) | Provenienz-Tooltip mit Zeit und Auswahlbegründung für alle typisierte…: Die API liefert Provenienz für alle typisierten Felder; ein Tooltip zeigt Quelle, Zeit, Rang bzw. Override-Grund. | BESTANDEN | P3 | verknüpft: F-349, F-350 | WP-060, WP-084, Override-Repository. |
| [WP-199](#wp-199) | Onboarding-Flow: Registrierung → Verifikation → Scope-Bestätigung → e…: Ein geführter Onboarding-Flow mit Fortschrittsanzeige über Signup, Verifikation, Collector-Enrollment und Scope-Bestätigung bis zu den erst… | TEILWEISE BESTANDEN | P3 | F-351 | WP-111/112, WP-156, WP-200 (Suite); Real-Stack (Keycloak, M… |
| [WP-200](#wp-200) | E2E: Install → Signup/Login → Dashboard; Onboarding-Fehlerbilder: Eine Playwright-Suite gegen eine frisch installierte Instanz (install-smoke) inkl. INS-04-Fehlerbilder, die die ABN-01-[B]-Onboarding-Szena… | TEILWEISE BESTANDEN | P2 | F-177, F-352, F-353 | WP-199, WP-092, WP-112; paketierte Installation braucht Net… |
| [WP-201](#wp-201) | E2E-Kernabläufe: CI, Review, Drag/Undo, Topologie, Export, Palette: Playwright-Specs je Kernablauf (CI, Review, Drag/Undo, Topologie, Export, Palette), die in der Browsermatrix aus WP-193 laufen; P2-Abläufe … | TEILWEISE BESTANDEN | P1 | F-016, F-077, F-178 | WP-193 (Matrix), WP-195/196/189 (Seiten); CI-Formular-Defek… |
| [WP-202](#wp-202) | TST-01-Pflichtfälle schließen (Querschnitt): Je TST-01-Pflichtfall existiert ein benannter Test, das Mapping steht in docs/traceability.csv, fehlende Fälle aus Vor-WPs sind ergänzt. | BESTANDEN | - | - | WP-084/086/087/089, WP-158, WP-136, WP-118, WP-005. |
| [WP-203](#wp-203) | Demo-/Testdatensatz nach SIM-01 und skalierbarer Last-Seed: Ein deterministischer Seed erzeugt den SIM-01-Demodatensatz (demo und acme, nutzbar als admin@demo.local), skaliert bis 50.000 CIs und läuf… | TEILWEISE BESTANDEN | P2 | F-113, F-277, F-278 | WP-106, WP-153, WP-088, WP-082, WP-054, Keycloak-Dev-Realm |
| [WP-204](#wp-204) | Lasttest-Harness und Ergebnisartefakte (NFR-10): Wiederholbare k6-Szenarien für die Budgets aus NFR-01/02/03, ein load.yml-Workflow mit Artefakten, N+1-Budgets und EXPLAIN-Prüfungen nach A… | TEILWEISE BESTANDEN | P2 | F-114, F-115, F-116, F-277, F-280 | WP-203, WP-142, SR-04, WP-206 |
| [WP-205](#wp-205) | Reconciliation über 10.000 CIs hinaus (seitenweise Identitätssuche): Die Reconciliation findet das passende CI unabhängig von der Inventargröße, indexbasiert und seitenweise. | TEILWEISE BESTANDEN | P2 | F-114, F-118 | WP-086, WP-203, SR-04, WP-333/334, WP-206 |
| [WP-206](#wp-206) | Lastlauf und Nachweis NFR-01/02/03 inkl. Budgetkorrekturen: Ein Lastlauf auf der Referenzumgebung mit Bericht (p95 je Budget, 200/1.000 rec/s); verfehlte Budgets führen zu Folge-WPs. | TEILWEISE BESTANDEN | P2 | F-116, F-117, F-118, F-279, F-280 | WP-204, WP-205, WP-154, WP-332..335, E-17, E-58, SR-06 |
| [WP-207](#wp-207) | Konkurrenzmatrix: Discovery während Edit, Doppel-Ingest, paralleler M…: Integrationstests mit echten parallelen Transaktionen für jeden [B]-Fall aus ABN-03, danach ein Integritätsprüfskript. | BESTANDEN | P3 | verknüpft: F-281, F-282 | WP-061, WP-085, WP-173, WP-332..334, WP-054, E-03 |
| [WP-208](#wp-208) | Kanal-Isolationsmatrix vollständig (TEN-09, TST-02): Mandantentrennung über alle Zugriffskanäle (REST, GraphQL, Suche, Export, Webhook, Notification, Job, Review, Location, Ingest) für alle Sc… | FEHLGESCHLAGEN | P0 | F-002, F-003, F-047, F-058, F-059, F-145, F-161, F-180, F-194 | WP-065, WP-185, WP-189, WP-140, WP-145, WP-157, WP-131, WP-… |
| [WP-209](#wp-209) | Internes Sicherheitsreview Phase 1: Vor G1 liegt ein dokumentiertes internes Sicherheitsreview vor: Threat-Model-Abgleich, Prüfung aller Critical/High-Befunde auf Schließung m… | TEILWEISE BESTANDEN | P1 | F-002, F-040, F-119, F-122, F-297 | WP-208, WP-206, WP-102, WP-166, WP-184, WP-004; WP-210 (Pen… |
| [WP-210](#wp-210) | Externer Pentest und Nachweis ohne offene Critical/High: Ein externer Pentest ist durchgeführt, Zusammenfassung und Befundliste sind abgelegt, jeder Befund hat ein WP, und es sind keine Critical/H… | FEHLGESCHLAGEN | P1 | F-039, F-120, F-300 | WP-209; externe Aufgabe A-07 (Auftraggeber), E-28 |
| [WP-211](#wp-211) | G1-Nachweis Installation auf beiden Referenzplattformen: Für den Release-Kandidaten belegen grüne Läufe von install-smoke (Ubuntu 22.04/24.04, Debian 12) und der Onboarding-E2E ohne Mocks, dass da… | TEILWEISE BESTANDEN | P2 | F-122 | WP-092, WP-093, WP-200; CI-Lauf install-smoke.yml auf dem R… |
| [WP-212](#wp-212) | Abnahmeszenarien ABN-01 [B] mit eingefrorenen Artefakten: Die Abnahmeszenarien Onboarding (d) und Konflikt (e) sind als Protokolle mit eingefrorenen Daten nachvollziehbar bestanden; (a) und (b) fol… | TEILWEISE BESTANDEN | P2 | F-120, F-138, F-298, F-299 | WP-201, WP-200, WP-089; E-02 (offen, v2-Texte durch Auftrag… |
| [WP-213](#wp-213) | G1-Gateprüfung: Traceability blockierend, REP-01-Struktur, Gate-Check…: Die G1-Gateprüfung ist automatisiert und prüfbar: Traceability blockiert fehlende [B]/[Q]-IDs, die REP-01-Struktur ist getestet, eine Check… | FEHLGESCHLAGEN | P1 | F-002, F-037, F-038, F-040, F-120, F-121, F-122, F-300, F-301 | WP-210 (Pentest), WP-211 (RC-Installationsnachweis), WP-212… |

## Einzelprüfungen

### M0.0 Werkzeugkette und Prüfrahmen

<a id="wp-001"></a>
#### WP-001 – Backend-Lint auf golangci-lint v2 lauffähig machen

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** TST-04
- **Anforderung/Nutzerziel:** golangci-lint v2 läuft für alle drei Go-Module grün und ist in CI und Makefile identisch gepinnt, damit jedes WP die DoD-01-Aussage 'keine Lint-Befunde' belegen kann.
- **Implementierung:** backend/.golangci.yml:1 (version '2'), :12-24 Linter, :39-54 Ausnahmen, :59-63 Baseline-Kommentar ('~580 findings … follow-up work package'), :64 new-from-rev 257e6fa, :66-71 Formatter gofmt/goimports; .github/workflows/ci.yml:233-262 Lint-Job (Matrix :239, GOLANGCI_LINT_VERSION v2.14.0 :242, fetch-depth 0 :248, Pin-Gleichheitsprüfung :255); Makefile:72 (v2.14.0), :79-83 check-golangci-lint. Commit cd23a05, gemergt als PR #40 (8db32ed).
- **Test(s):** Kein automatisierter Test. Baseline: SP/runs/lint-backend.log, lint-collector.log, lint-edgecore.log je '0 issues.' (v2.14.0, Repo-Config). Verifier: edgecore mit Repo-Config 0 issues; Config-Kopie ohne new-from-rev (SP/work/verify-B01/nobaseline.yml): backend 429 issues in 164 Dateien, exit 1 (SP/work/verify-B01/lint-backend-nobaseline.txt), edgecore 6 issues, collector 37 (Auditor); gofmt -l backend collector edgecore: 12 Dateien. Mechanische Verifikation der Testwelle: golangci-lint backend/collector/edgecore 0 issues. Defekttest-Welle: nicht kodifiziert (Plan-/Doku-Mangel ohne Laufzeit-Assertion).
- **Soll/Ist:** SOLL: v2-Schema mit erhaltenen Lintern, identischer Pin, grüner Lint für alle go.work-Module; new-from-rev-Baseline nur zusammen mit dokumentiertem Folge-WP. IST: Migration, Pin und Modulmatrix korrekt und lokal grün – aber nur dank Baseline. Das Folge-WP zum Abbau der 472 Altbefunde existiert weder im Phase-1- noch im Phase-2-Plan, die Altlast bleibt dauerhaft unsichtbar.
- **Evidenz:** SP/runs/lint-{backend,collector,edgecore}.log ('0 issues.'); SP/work/verify-B01/lint-backend-nobaseline.txt ('429 issues', exit 1); SP/work/verify-B01/lint-edgecore-nobaseline.txt ('6 issues'); gofmt -l → 12 Dateien; grep in docs/plan/implementierungsplan.md und docs/plan/phase2/* nach Lint-Folge-WP: kein Treffer; v1→v2-Linter-Mapping gegen git show cd23a05^:backend/.golangci.yml geprüft.
- **Abweichung:** AK4 teilweise verletzt: Baseline new-from-rev gesetzt, Folge-WP nicht angelegt (L-T5-13); DoD-01 'Lint ohne Befunde' gilt nur für seit 257e6fa geänderten Code.
- **Prüfebenen:** UX: N/A (CI-Werkzeug); API: N/A; Daten: N/A; E2E: OK (Lint-Kette lokal mit v2.14.0 und Repo-Config reproduziert, 0 issues in allen 3 Modulen); Robustheit: LÜCKE (472 verdeckte Altbefunde, u. a. 8 ungeprüfte Fehler wie json.Unmarshal in internal/agent/pg_repository.go:38 und internal/desk/pg_repository.go:42, 22 ineffassign, 7 unused, 12 nicht gofmt-formatierte Dateien)
- **Abhängigkeiten:** keine
- **Defekte:** F-123 (P2) Lint-Baseline (new-from-rev) verdeckt 472 Altbefunde; das versprochene Folge-WP wurde nie angelegt
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Folge-WP im Plan für den Abbau der 472 Befunde (danach new-from-rev entfernen) ODER Lint ohne Baseline meldet 0 issues in allen 3 Modulen; empfohlen zusätzlich ein Toolchain-Vertragstest (v2-Schema, Linter-Obermenge, Pin-Gleichheit ci.yml/Makefile, jedes go.work-Modul in der Matrix) mit Negativfällen.

<a id="wp-002"></a>
#### WP-002 – Frontend-Pipeline grün: Prettier, Typecheck, Vitest, Build

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** TST-04
- **Anforderung/Nutzerziel:** Die Frontend-CI-Kette (format:check, lint, typecheck, Vitest, build) läuft nacheinander grün und bricht bei jedem Fehler ab; Formatierung erfolgt rein mechanisch.
- **Implementierung:** .github/workflows/ci.yml:264-301 (Frontend-Job: format:check → lint → generate:api:check → typecheck → test → build, Kommentar 'any failing step fails the job'); frontend/package.json Skripte build :8, lint :11, format:check :13, typecheck :14, test :15; frontend/.prettierignore (nur node_modules, dist, coverage, *.min.js, test-results, playwright-report). Commit 6d75a36 (18 Dateien formatiert, CI-Schritt auf npm run format:check umgestellt).
- **Test(s):** Baseline HEAD 3a9669a: npm run format:check ('All matched files use Prettier code style!'), lint, generate:api:check, typecheck, build ('built in 5.09s') je exit 0 (SP/runs/fe-*.log); Vitest 249/249 pass (SP/runs/results-frontend.json). Verifier-Probe prettier 3.9.6: 18/18 Dateien aus 6d75a36 byte-identisch zur Prettier-Ausgabe der Vorversion (SP/work/verify-B01/wp002: mech=18, nonmech=0). Mechanische Verifikation der Testwelle: prettier/eslint/tsc 0/0/0, Vitest 48 Dateien / 252 Tests pass. Keine neuen Tests der Welle T5.
- **Soll/Ist:** SOLL: grüne, sequenzielle, fail-fast Frontend-Kette mit ausschließlich mechanischer Formatierung. IST: erfüllt; belegt durch die lokal ausgeführte Kette auf HEAD und die wiederholte Prettier-Identitätsprobe.
- **Evidenz:** SP/runs/fe-*.log (alle exit 0); SP/runs/results-frontend.json (numPassedTests 249, numFailedTests 0); SP/work/verify-B01/wp002 (mech=18, nonmech=0); ci.yml:264-301 ohne '|| true', continue-on-error oder set +e.
- **Abweichung:** -
- **Prüfebenen:** UX: N/A (CI-Werkzeug); API: N/A; Daten: N/A; E2E: OK (gesamte Kette auf HEAD ausgeführt, jeder Schritt exit 0); Robustheit: OK (keine Soft-Fail-Konstrukte; generate:api:check schützt zusätzlich gegen OpenAPI-Drift)
- **Abhängigkeiten:** keine
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung optional: Strukturtest der Schrittfolge und Soft-Fail-Freiheit (tests/rep01-structure.test.sh mit PyYAML, Negativfall). Der GitHub-Lauf selbst ist von hier nicht einsehbar; Evidenz ist die lokale Reproduktion.

<a id="wp-003"></a>
#### WP-003 – Vollständige Testauswahl in CI (alle DB-Tests, Collector, Edgecore)

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** TST-04, TST-02
- **Anforderung/Nutzerziel:** CI führt alle PostgreSQL-Tests ohne Namensfilter aus und zeigt ihre Anzahl im Log; Collector und Edgecore werden mit Race-Detector getestet.
- **Implementierung:** .github/workflows/ci.yml:74-192 migrations-Job (:148 gotestsum -- -count=1 -p 1 ./... mit TEST_DATABASE_URL/TEST_NATS_URL/TEST_REDIS_URL; Zusammenfassung :150-167, Fail nur auf 'TEST_DATABASE_URL not set' :164); ci.yml:46-72 go-modules-Job (Matrix [collector, edgecore] :51, go test -race ./... :72); Makefile:58-64 test-db, test-collector, test-edgecore; backend/internal/database/scopetest/scopetest.go:43 (Skip-Meldung). Commit b0a1b32.
- **Test(s):** Baseline: SP/runs/results-backend.tsv 1896 pass / 0 fail / 0 skip; collector 273 pass, edgecore 76 pass. Verifier: test-db.json 0 Test-Level-Skips/-Fails (11 Paket-Skips ohne Testdateien); SP/work/audit-B01/db-tests.txt 798 Top-Level-Tests in 61 Paketen; edgecore go test -race 5/5 Pakete ok (Verifier), collector -race 13 Pakete ok (SP/work/audit-B01/collector-race.txt); Probe SP/work/verify-B01/wp003/summary.sh (wörtliche CI-Zusammenfassung, synthetischer Skip 'TEST_NATS_URL not set' → skipped=1, exit 0).
- **Soll/Ist:** SOLL: vollständige DB-Testauswahl mit sichtbarer Anzahl, race-getestete Collector/Edgecore-Module, Reparatur oder WP-referenzierter Skip neu fehlschlagender Tests. IST: erfüllt; der Skip-Wächter der Zusammenfassung ist enger als nötig (P3, außerhalb der AK).
- **Evidenz:** SP/runs/results-backend.tsv (1896 pass); test-db.json (0 Test-Level-Skips/-Fails); edgecore -race 5/5 (Verifier), collector -race 13 ok (Auditor); SP/work/verify-B01/wp003/summary.sh (exit 0 bei NATS-Skip); ci.yml:46-72, :143-167.
- **Abweichung:** P3 außerhalb der verbindlichen Kriterien: CI-Zusammenfassung scheitert nur an einer bestimmten Skip-Meldung (L-T5-30).
- **Prüfebenen:** UX: N/A; API: N/A; Daten: OK (alle DB-Pakete gegen migrierte TimescaleDB mit NATS und Redis); E2E: OK (CI-migrations-Job lokal mit gotestsum -p 1 reproduziert); Robustheit: LÜCKE P3 (Skips 'TEST_REDIS_URL not set', 'TEST_NATS_URL not set', 'TEST_DATABASE_URL and TEST_REDIS_URL required…' würden den Job grün lassen; per Probe reproduziert)
- **Abhängigkeiten:** WP-001
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-283 (P3) CI-Zusammenfassung des DB-Jobs scheitert nur an einer bestimmten Skip-Meldung
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Empfohlen: Strukturtest (kein -run im DB-Job, go-modules-Matrix = go.work-Module ohne backend, -race vorhanden) und breiterer Skip-Wächter mit eigenem Shell-Test.

<a id="wp-004"></a>
#### WP-004 – Abhängigkeits- und Container-Scans in CI

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** TST-04, SEC-11
- **Anforderung/Nutzerziel:** Jeder PR scannt Go-Module (govulncheck), Frontend-npm-Abhängigkeiten und alle gebauten Container-Images (Trivy); Blockierschwellen folgen SEC-11, Berichte werden hochgeladen.
- **Implementierung:** .github/workflows/ci.yml:370-413 govulncheck (Matrix backend/collector/edgecore :375, v1.8.0, Auswertung :393-405 nur erreichbare Funde, Upload :407-413); :415-453 npm-audit (--omit=dev :429, Auswertung :432-445 prod High/Critical, Upload :447-453); :455-507 image-scan (Matrix backend/collector/frontend :461-469, trivy 0.75.0 --severity CRITICAL,HIGH --ignore-unfixed --ignorefile security/scan-exceptions.txt --exit-code 1 :489, Upload :499-507); security/scan-exceptions.txt; docs/security/abhaengigkeitsscans.md; release.yml:117-129 baut 4 Images inkl. reticora-postgres. Commit a8fea01.
- **Test(s):** Kein automatisierter Test. Verifier: echtes npm audit auf Scratch-Kopie von package.json/package-lock.json (registry.npmjs.org per noProxy erreichbar): --omit=dev critical 0 / high 0 / moderate 2; alle Abhängigkeiten critical 2 / high 10; CI-Auswertung wörtlich auf die echten Berichte → exit 0 (SP/work/verify-B01/wp004/evalnpm.sh, npm-audit-prod.json, npm-audit-all.json). govulncheck-Auswertung wörtlich mit 5 synthetischen Fällen korrekt (SP/work/verify-B01/wp004/go/eval.sh). Echter govulncheck-Lauf unmöglich (vuln.go.dev 'Forbidden'/CONNECT 403, SP/work/audit-B27/govuln-backend.err), kein Trivy/Image-Build. Defekttest-Welle T5: tests/audit/ops-config.test.sh Fall image_scan_coverage fails-as-expected ('not scanned: reticora-postgres'), in dieser Synthese erneut ausgeführt (SP/work/synth-T5/ops-config.log).
- **Soll/Ist:** SOLL: Go-, npm- und Container-Scans auf jedem PR für alle gebauten Images, SEC-11-Schwellen, hochgeladene Artefakte. IST: Jobs und Doku vorhanden; npm audit End-to-End grün; Go-Auswertungslogik korrekt; das ausgelieferte Image reticora-postgres liegt außerhalb der Scan-Matrix; kein echter govulncheck- oder Trivy-Lauf beobachtbar; SLA-Deckel für Ausnahmen nicht maschinell erzwungen.
- **Evidenz:** SP/work/verify-B01/wp004/npm-audit-prod.json, npm-audit-all.json, evalnpm.sh (exit 0); SP/work/verify-B01/wp004/go/eval.sh (5 Fälle); Vergleich release.yml:117-129 ↔ ci.yml:455-470; ops-config image_scan_coverage FAIL; curl vuln.go.dev → 403.
- **Abweichung:** AK3 teilweise verletzt: reticora-postgres wird nie per Trivy gescannt (L-T5-14). AK4: SLA-Deckel nicht erzwungen, Trivy-Eintrag ohne exp: wirkt unbegrenzt (L-T5-31). AK1/AK3 nicht real ausführbar (Netzwerkrichtlinie).
- **Prüfebenen:** UX: N/A; API: N/A; Daten: N/A; E2E: LÜCKE (npm audit real ausgeführt und grün; govulncheck und Trivy lokal blockiert – vuln.go.dev 403, kein Image-Build – nur Auswertungslogik synthetisch geprüft); Robustheit: FEHLER (postgres-Image ungescannt; Ausnahme-Ablauf ohne SLA-Deckel, exp:2099-01-01 akzeptiert; 2 Critical/10 High der Dev-Toolchain nur berichtet, by design)
- **Abhängigkeiten:** WP-003; E-01, E-31; WP-100 (führte das postgres-Image ein)
- **Defekte:** F-124 (P2) Ausgeliefertes Image reticora-postgres wird nie auf Schwachstellen gescannt, F-284 (P3) Ablauf von Scan-Ausnahmen wird nicht gegen den SEC-11-SLA-Deckel geprüft; Trivy behandelt Einträge ohne exp: als dauerhaft
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** (1) postgres in der image-scan-Matrix und Strukturtest Release-Matrix ⊆ Scan-Matrix (ops-config image_scan_coverage grün); (2) Shell-Test der Scan-Auswertung und des Ausnahmeformats (exp/since/sev, SLA 7/30 Tage) grün; (3) CI-Run-Artefakte govulncheck ×3 und Trivy ×4 auf dem Release-Kandidaten, grün oder mit SLA-konformen Ausnahmen.

<a id="wp-005"></a>
#### WP-005 – Traceability-Datei, CI-Prüfung und PR-Vorlage (GLO-11/DOD-01)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** GLO-11, DOD-01
- **Anforderung/Nutzerziel:** Jede Anforderungs-ID ist über docs/traceability.csv auf Epic, Testdatei und WP rückführbar; ein Go-Prüfprogramm validiert die Datei auf jedem PR; die PR-Vorlage enthält die DoD-01-Checkliste.
- **Implementierung:** backend/internal/traceability/traceability.go:333-369 Validate (testdatei-Prüfung :363-368 nur Pfadform und os.Stat), :399 MissingForGate, :418-428 WorkPackagesIn (nur wörtliche Tokens); traceability_test.go:260-315 TestTraceabilityFile (Gate-Blockade :296-300, [Pn]/[A] nur mit TRACEABILITY_REQUIRE_COMPLETE=1 :301-310); .github/workflows/ci.yml:194-218 traceability-Job (WP-Extraktion :205-214); .github/pull_request_template.md; README.md:352; docs/traceability.csv (471 Zeilen). Commit 2d3d699; 5 g1-gate.md-Zeilen nachträglich in fa76b16.
- **Test(s):** Verifier: go test -count=1 -v ./internal/traceability/ 12/12 PASS (TestTraceabilityFile: 'gate G1: 219 of 219 [B]/[Q] IDs traced', '471 rows … 79 without a row'); Baseline results-backend.tsv 12 pass. Overlay-Probes SP/work/verify-B01/wp005/: ohne die 5 g1-gate.md-Zeilen FAIL 214/219 ('AUT-05 AUT-06 GATE-04 NET-04 NFR-08'); mit README.md und docs/ als testdatei PASS. TRACEABILITY_WPS='WP-212 WP-333 WP-334 WP-001..WP-007' PASS. Defekttest-Welle T5 (Build-Tag auditdefect): TestAuditT5ValidateRejectsNonTestEvidence und TestAuditT5GateIDsAreNotTracedByTheChecklistItself fails-as-expected, mechanisch bestätigt (traceability_evidence_auditdefect_test.go:54 bzw. :106), Kontrolle mit echter Testdatei PASS.
- **Soll/Ist:** SOLL: jede Zeile verweist auf eine existierende Testdatei; das Prüfprogramm erzwingt GLO-11 (mindestens eine Testart) und blockiert vollständig zu G1. IST: Prüfprogramm, CI-Job und PR-Vorlage funktionieren und sind gut unit-getestet; das Prüfprogramm akzeptiert aber beliebige existierende Pfade (Markdown, Verzeichnisse, README), und 5 [B]/[Q]-IDs gelten nur über Verweise auf die Gate-Checkliste (Erklärungen) als 'traced'. Ohne diese Zeilen scheitert das eigene G1-Gate.
- **Evidenz:** go test ./internal/traceability 12/12 PASS; Overlay-Probes trace-nogate.csv (FAIL 214/219) und trace-readme.csv (PASS); Defekttests traceability_evidence_auditdefect_test.go rot; git log -S → fa76b16; docs/acceptance/g1-gate.md:163-167 ('PASS (Erklärung des Auftraggebers)'); .github/pull_request_template.md (10 DoD-Punkte gelesen).
- **Abweichung:** AK1 und AK2 teilweise verletzt: Nicht-Test-Pfade werden als testdatei akzeptiert, G1-Vollständigkeit beruht auf 5 Selbstverweisen (L-T5-11). P3: README überzeichnet Umfang (L-T5-32); WP-Bereiche in PR-Titeln werden nicht expandiert (L-T5-33).
- **Prüfebenen:** UX: N/A; API: N/A; Daten: OK (Format, IDs, Tags, Epics, WPs gegen Katalog und Plan validiert, 471 Zeilen, 0 Fehler); E2E: OK (TestTraceabilityFile auf den echten Dateien, CI-WP-Extraktion lokal nachgestellt); Robustheit: FEHLER (beliebiger existierender Pfad zählt als testdatei; G1-Zahl 219/219 enthält 5 ungetestete IDs; Bereiche 'WP-211 – WP-213' liefern nur Endpunkte)
- **Abhängigkeiten:** WP-003; E-30, E-06; WP-213 (Gate-Regel [B]/[Q])
- **Defekte:** F-121 (P2) Traceability-Prüfung akzeptiert jeden existierenden Pfad als 'testdatei'; G1-Vollständigkeit 219/219 nur über Selbstverweise auf die Gate-Checkliste, F-285 (P3) README überzeichnet G1-Blockadeumfang und Vollständigkeit der Traceability, F-286 (P3) WP-Bereiche in PR-Titeln werden vom Traceability-Job nicht expandiert
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** (1) Validate erzwingt Testdatei-Muster plus explizite Allow-List der 21 planvorgeschriebenen Nachweisdokumente (TestAuditT5ValidateRejectsNonTestEvidence grün); (2) die 5 g1-gate.md-Zeilen durch Testdateien ersetzt oder als erklärungsbasiert markiert und aus der 'traced'-Zahl ausgenommen (TestAuditT5GateIDsAreNotTracedByTheChecklistItself grün); (3) README-Formulierung korrigiert; optional Bereichsexpansion in WorkPackagesIn mit Unit-Test.

<a id="wp-006"></a>
#### WP-006 – Migrations-Roundtrip mit Schemavergleich, vollständige Down-Migrationen, Schema-Baseline

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** DB-02, REP-01
- **Anforderung/Nutzerziel:** Jede Migration hat ein funktionierendes up/down-Paar; ein Integrationstest beweist up → down-all → up mit identischem Schema und dass jedes down den Vorzustand herstellt; docs/schema-baseline.md ist gegen das migrierte Schema geprüft.
- **Implementierung:** backend/internal/database/migrations_roundtrip_integration_test.go: loadMigrations :40-62, migrator :71-135, TestMigrationsRoundtrip :143-196, knownDownDeviations :233-240 (nur 000056, FORCE RLS), TestSchemaBaselineMatchesMigratedSchema :271-303, schemaDump :305-406; 000021_spec_alignment.down.sql, 000055_cmdb_extensions.down.sql vervollständigt; docs/schema-baseline.md (Stand 000138 :8, Konvention :43-53); Makefile migrate-roundtrip/schema-baseline; ci.yml CLI up/down -all/up :117-124. Commit 385832f.
- **Test(s):** Baseline results-backend.tsv: pass TestMigrationsRoundtrip (78.43s), TestSchemaBaselineMatchesMigratedSchema, TestMigrationsAreSorted, TestMigrationsApplied; SP/runs/migrate.log CLI up/down -all/up (138 Migrationen) OK. Verifier auf privatem Cluster (timescaledb 2.17.2-pg16): Roundtrip PASS 63.21s (SP/work/verify-B01/wp006/roundtrip-clean.log); Sabotageprobe (000138 up + probe_col) FAIL mit genau einem Diff 'column ci_contact.probe_col' (roundtrip-probe.log); Shared-Cluster-Probe FAIL 2BP01 bei 000056 down (roundtrip-shared.log). 138 *.up.sql = 138 *.down.sql.
- **Soll/Ist:** SOLL: vollständige up/down-Paare, Roundtrip mit Schemagleichheit, geprüfte Schema-Baseline, Konvention für Folge-WPs. IST: vollständig erfüllt, unabhängig reproduziert inklusive Negativ-Selbsttest per Sabotageprobe; einzige tolerierte Abweichung (000056 behält FORCE RLS) ist gewollt.
- **Evidenz:** results-backend.tsv (TestMigrationsRoundtrip pass); SP/work/verify-B01/wp006/roundtrip-clean.log (PASS), roundtrip-probe.log (ein erwarteter Diff), roundtrip-shared.log (2BP01); SP/runs/migrate.log.
- **Abweichung:** P3 außerhalb der AK: Roundtrip scheitert auf geteilten Clustern mit zweiter migrierter Reticora-DB (L-T5-34). Beobachtung ohne Befund: einige ältere up-Migrationen nachträglich editiert (z. B. 000056 durch WP-052, 000071 durch 9529f22), widerspricht der Konvention in schema-baseline.md.
- **Prüfebenen:** UX: N/A; API: N/A; Daten: OK (Dump vergleicht alle public-Objektklassen inkl. RLS-Flags, Policies, Grants; Reversibilität je Migration für 138 Migrationen belegt); E2E: OK (CLI- und Go-Roundtrip ausgeführt, Go-Roundtrip unabhängig auf privatem Cluster); Robustheit: LÜCKE P3 (clusterweites DROP ROLE reticora_app bei 000056 down; schemaDump ignoriert Nicht-public-Schemata und TimescaleDB-Katalog)
- **Abhängigkeiten:** WP-003
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-287 (P3) Roundtrip-Test scheitert auf geteilten PostgreSQL-Clustern (clusterweite Rolle reticora_app wird gelöscht)
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Empfohlen: permanenter Negativ-Selbsttest (Sabotage in t.TempDir), TimescaleDB-Katalog im Dump, Behebung der Shared-Cluster-Einschränkung.

<a id="wp-007"></a>
#### WP-007 – RLS-Katalogtest mit verwalteter Ausnahmeliste

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** TEN-05, TEN-03, TEN-09
- **Anforderung/Nutzerziel:** Ein Katalogtest erzwingt für jede Mandantentabelle ENABLE und FORCE RLS, Policies für alle Kommandos mit USING und WITH CHECK sowie Client-/Site-Scope-Prädikate; bekannte Lücken stehen in einer Liste, die nur schrumpfen darf.
- **Implementierung:** backend/internal/tenant/rls/known_gaps.go: Regeln :10-60, ViewProtected/GlobalCatalog/OperatorTables :62-85, KnownGapsBaselineMigration=57 :105, KnownGaps leer :120 (initial 31 Einträge mit WP); catalog_integration_test.go: usesGUC :81, TestRLSCatalog :88-140 (unlisted :130, closed :134), TestKnownGapsList :142-177, TestKnownGapsDocumented :179-210, violations :221-298, enforces :323-347, loadCatalog :374-451. Ergänzend TestRLSMatrix (matrix_integration_test.go:113, WP-065). Commit e0b18fa.
- **Test(s):** Baseline results-backend.tsv: pass TestRLSCatalog, TestKnownGapsList, TestKnownGapsDocumented, TestRLSMatrix (123 Tabellen-Subtests). Verifier auf audit_db_05 (SP/work/verify-B01/wp007/): positive Läufe PASS; Probe-Tabelle verify_b01_weak → TestRLSCatalog FAIL mit client-scope, rls-forced, site-scope, system-write, with-check (catalog-probe.txt); verify_b01_fake (OR-aufgeweitetes Prädikat) von TestRLSCatalog nicht erkannt, TestRLSMatrix FAIL 'row outside the scope is visible'/'deleted' (matrix-probe.txt); known_gaps.go-Overlay → 'known RLS gaps are closed … ci: rls-forced' bzw. 'WP-999 is not in …'/'not created by a migration up to 000057'; Wiederholung nach Drop PASS.
- **Soll/Ist:** SOLL: Katalogtest erzwingt RLS-Struktur, Ausnahmeliste darf nur schrumpfen. IST: vollständig implementiert; Positiv-, Negativ-, Closed-Gap- und New-Table-Verhalten per ausgeführten Läufen gegen echte migrierte DB belegt (Auditor und Verifier unabhängig).
- **Evidenz:** results-backend.tsv (4 Tests pass); SP/work/verify-B01/wp007/catalog-probe.txt, matrix-probe.txt; known_gaps.go-Overlay-Läufe; alle 242 public-Policies gelten für {public}; reticora_app ohne SELECT auf _timescaledb_internal._materialized_hypertable_3.
- **Abweichung:** -
- **Prüfebenen:** UX: N/A; API: N/A; Daten: OK (alle public-Mandantentabellen gegen 15 Regeln geprüft); E2E: N/A (struktureller Katalogtest; Verhaltensmatrix gehört zu WP-065); Robustheit: OK mit Einschränkung (Prädikaterkennung per Substring; OR-aufgeweitetes Prädikat wird nur gemeinsam mit TestRLSMatrix erkannt; Negativpfade nur per Ad-hoc-Probe ausgeübt). Hinweis: Der Rack-Site-Scope-Bypass (Gruppe T1, WP-208/WP-027) beruht auf fehlender site_id-Spalte an Racks und liegt außerhalb von AK2 (gilt nur für Tabellen mit site_id).
- **Abhängigkeiten:** WP-006; WP-022, WP-023, WP-024, WP-040, WP-065 (schlossen alle Lücken)
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Empfohlen: permanente Negativ-Unit-Tests von violations() und compareGaps/validateGaps sowie ein Integrations-Negativtest (inkl. OR-aufgeweitetem Prädikat), damit die Garantie nicht von Ad-hoc-Probes abhängt.

### M0.1a Mandantentrennung (RLS/Scopes, WithTenant)

<a id="wp-008"></a>
#### WP-008 – Zentrale Mandantentransaktion database.WithTenant mit vollständigem Scope

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** TEN-04, TEN-06
- **Anforderung/Nutzerziel:** Jede mandantenbezogene DB-Transaktion setzt app.org_id, app.user_id, app.client_scope, app.site_scope und app.team_scope transaktionslokal über eine validierte API; leere Scopes sind fail-closed, org-weit nur per explizitem Marker (E-08).
- **Implementierung:** backend/internal/database/pool.go:212-250 (WithTenant: Validate, joinTx, BeginTx), :258-264 (beginTenantSQL, 5 GUCs per set_config(...,true) im BEGIN-Roundtrip), :291-311 (WithSystem read-only); backend/internal/database/tenant_scope.go (ScopeSet, gucValue, TenantScope.Validate, WithRequestTenant mit ErrNoTenantScope/ErrTenantMismatch); backend/internal/middleware/auth.go:229-251 (TenantMiddleware), :259-279 (TenantScopeFor); backend/internal/database/tenantguard_allowlist.go (4 dokumentierte Systempfade); platform/db entfernt, tenant/rls/rls.go nur Paketdoku.
- **Test(s):** Baseline PASS (SP/runs/results-backend.tsv): internal/database TestWithTenantSetsAllGUCs, TestWithTenantResetsGUCsOnPooledConnections, TestWithTenantScopesRows, TestWithTenantRejectsIncompleteScope, TestWithTenantRejectsIncompleteScopeBeforeConnecting, TestTenantScopeValidate, TestScopeSetGUCValue, TestGUCNamesMatchCatalog, TestBeginTenantSQLQuotesValues, TestWithTenantQuotesHostileUserID, TestContextTxJoin, TestContextTxFailsClosed, TestNoDatabaseAccessOutsideTenantContext; internal/middleware TestTenantMiddlewareFromJWT, TestTenantScopeFor, TestTenantScopeForResolvedScope, TestTenantMiddlewareRejectsHeaderOnlyRequests, TestHeaderSpoofingDoesNotChangeTenant; internal/tenant/rls TestSitePoliciesRestrictSiteScopedPrincipals, TestTeamRoomAndPersonScope. Verifier-Probe TestVerifyB02EmptySiteTeamScopeRows PASS. Neu (Defekttest-Welle, Commit 0e02557): TestAuditT1SessionTokenScopeChain (backend/internal/tenant/rls/audit_t1_token_chain_integration_test.go) PASS. Real-Stack tenant-isolation.spec.ts T05, T09 PASS.
- **Soll/Ist:** SOLL: eine validierte WithTenant-API mit den 5 TEN-04-GUCs, fail-closed bei unvollständigem/leerem Scope, Altpfade entfernt, Middleware liefert den Scope, kein GUC-Leck über Pool-Verbindungen. IST: vollständig umgesetzt; alle 5 Kriterien durch ausgeführte DB-Integrationstests belegt. Die zuvor offene echte Tokenkette für client-gescopte Nutzer (Refresh mit PG-Resolver -> signiertes Token -> AuthMiddleware -> TenantMiddleware -> Router -> RLS) ist jetzt durch TestAuditT1SessionTokenScopeChain belegt; die Org-Dimension zusätzlich Real-Stack (T05/T09).
- **Evidenz:** SP/runs/results-backend.tsv (alle genannten Tests pass); SP/work/verify-B02/database-empty-scope-probe.log (leere Site-Menge -> 0 Buildings, leere/fremde Team-Menge -> 0 Team-Tickets, team-lose Zeile sichtbar); SP/work/t1-tests/untagged.log ('--- PASS: TestAuditT1SessionTokenScopeChain'); mechanische Verifikation SP/verify-tests-run2/results.tsv 'untagged backend/internal/tenant/rls (all) PASS'; SP/work/tenant-iso-final.log T05/T09 ✓ (13 passed).
- **Abweichung:** -
- **Prüfebenen:** UX: N/A (Backend-Infrastruktur). API: OK – 401 ohne Principal, Header-Spoofing wirkungslos (Unit + Real-Stack T09). Daten: OK – GUCs is_local, Zeileneffekt für Client/Site/Team auf echten Policies. E2E: OK – Real-Stack T05/T09 (Org-Dimension) und echte Client-Scope-Tokenkette per TestAuditT1SessionTokenScopeChain. Robustheit: OK – Quoting feindlicher User-IDs, UUID-Validierung, Scope-Mismatch beim Join, abgebrochene Tx erkannt.
- **Abhängigkeiten:** WP-007; Basis für WP-009..WP-022 und alle WithTenant-Nutzer; Guard WP-041 (Guard-Lücken siehe L-T1-29).
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Probe TestVerifyB02EmptySiteTeamScopeRows als Repo-Test committen (leere Site-/Team-Menge -> 0 Zeilen, team-lose Zeile sichtbar, nicht schreibbar).

<a id="wp-009"></a>
#### WP-009 – Autoritative Scope-Ableitung in Session und Principal

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** RBA-03, TEN-04
- **Anforderung/Nutzerziel:** Effektive Rechte und Client-/Site-/Team-Scope stammen aus DB-Rollenzuweisungen (RBA-03-Vereinigung), werden in Session und Principal getragen und in den WithTenant-Scope übersetzt; ein reiner Client-Nutzer erhält nie org-weiten Zugriff.
- **Implementierung:** backend/internal/permission/pg_repository.go:165-227 (AccessGrants aus role_assignment.scope_client_id/scope_site_id und user_custom_role), :230-265 (RoleGrants für IdP-Gruppen, nur org-weit); backend/internal/identity/principal.go:155-221 (ResolveAccess, aggregateScope, unionDimension); backend/internal/identity/handler.go:463-493 (applyAccess, genutzt von Callback :445 und Refresh :243); backend/internal/server/router.go:318-320; backend/internal/identity/types.go:263-411 (cls/sts/tms); backend/internal/middleware/auth.go:259-279; backend/internal/identity/apikey.go:217-243 (API-Keys mit Inhaber-Grants, cmd/server/main.go:640).
- **Test(s):** Baseline PASS: permission TestAccessGrantsResolveScopesFromRoleAssignments, TestClientOnlyUserSeesOnlyAssignedClient (echte PG-Kette); identity TestResolveAccessUnitesScopesPerDimension, TestResolveAccessKeepsNarrowerPermissionScopes, TestLegacyClientScopeFailsClosed, TestRefreshReadsRoleAssignmentsFromDatabase, TestRefreshFailsWhenRoleAssignmentsCannotBeRead, TestCallbackCombinesGroupsAndRoleAssignments (Fake-Resolver), TestDeactivationEndsAccess; middleware TestTenantScopeForResolvedScope. Verifier-Probe TestVerifyB02RefreshScopeFromDB PASS. Welle (Commit 0e02557): TestAuditT1SessionTokenScopeChain PASS (Refresh mit permission.PGRepository: client-1 / org-weit / verengt / ohne Zuweisung / Org B / unsigniertes Token); Defekttest TestAuditT1TeamScopedAssignment FAILS_AS_EXPECTED. Real-Stack roles-authz.spec.ts 07 und 11 PASS, 13 FAIL (ext D3), 14 FAIL (ext D2) (SP/work/verify-roles-authz/final-run.log).
- **Soll/Ist:** SOLL: role_assignment/Custom-Rollen mit scope_kind org|client|site|team, je Dimension vereinigt, in Session/Principal getragen, bei Login und Refresh neu gelesen; Client-Nutzer nie org-weit. IST: Client-/Site-/Org-Ableitung inkl. Verengung beim Refresh auf echter DB belegt (jetzt auch committed: TestAuditT1SessionTokenScopeChain). Team-Scope ist nicht zuweisbar (Teams immer All, bestätigt durch Defekttest). Client- plus Site-Grant schneidet Dimensionen (Spezifikationsambiguität). Real-Stack: Rollenentzug unmöglich (ext D3) und SPA-Refresh scheitert (ext D2).
- **Evidenz:** psql audit_db_06 (role_assignment ohne Team-Spalte, user_custom_role CHECK scope_type IN organization/client/site); SP/work/verify-B02/identity-refresh-probe.log (1/2/1/0 sichtbare Clients); SP/verify-tests-run2/defect-TestAuditT1TeamScopedAssignment.log ('POST /role-assignments with scope_type team answered 404 unsupported scope_type team'; 'resolved team scope ... {All:false IDs:[]} (grants [])'); SP/work/t1-tests/untagged.log PASS TestAuditT1SessionTokenScopeChain; identity/principal_scope_test.go:56-62.
- **Abweichung:** AK1 teilweise: Team-Dimension von RBA-03 nicht aus der DB ableitbar (L-T1-07, P2). Gemischte Client+Site-Grants werden geschnitten statt vereinigt (L-T1-19, P3, PO-Klärung). Externe Real-Stack-Defekte D2/D3 betreffen den Nutzerprozess (Refresh, Entzug).
- **Prüfebenen:** UX: N/A (keine UI im WP). API: OK – Login/Refresh scheitern ohne ungescopte Session, wenn die Auflösung fehlschlägt; Real-Stack-Refresh der SPA scheitert jedoch (ext D2). Daten: LÜCKE – Team-Zuweisung nicht speicherbar. E2E: LÜCKE – kein Real-Stack-Login eines client-gescopten Nutzers (roles-authz deckt nur org-weite Rollenwechsel). Robustheit: OK – fail-closed ohne Grants; Semantikfrage gemischter Grants (L-T1-19).
- **Abhängigkeiten:** WP-008; E-11 (Team-Semantik, vorläufig); WP-029 (Team-RLS-Prädikat, produktiv wirkungslos solange L-T1-07 offen); WP-046/WP-066 (ext D2/D3, Gruppe T2).
- **Defekte:** F-049 (P2) Team-Scope kann nie vergeben werden: Rollenzuweisungen ohne Team-Dimension, app.team_scope für reale Nutzer immer 'alle', F-181 (P3) Client-Grant kombiniert mit Site-Grant schneidet die Dimensionen und verengt den Zugriff unter die Vereinigung der Rollen
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** (1) Team-Scope zuweisbar (Schema + AccessGrants) und TestAuditT1TeamScopedAssignment grün – oder PO-Entscheidung E-11, die Team-Zuweisungen aus Phase 1 nimmt; (2) PO-Entscheidung zu gemischten Client+Site-Grants, per Integrationstest fixiert; (3) Callback mit Fake-OIDC + permission.NewPGRepository (cls=[C1], sts=null); (4) Real-Stack-Login eines client-gescopten Nutzers (Liste nur C1, C2-URL not-found).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – defekte: „["L-T1-07","L-T1-19","ext:D2 SPA session refresh always fails: POST /auth/refresh without Content-Type is rejected with 400 by the OpenAPI validator","ext:D3 A role assignment cannot be removed: no DELETE /role-assignments/{id} and no other revoke path"]“ → „["L-T1-07","L-T1-19"]“. D2 ist laut e2e_defects 'unverified' und darf nicht als bestätigter Defekt geführt werden. D3 (bestätigt, P1) verletzt kein WP-009-AK; Eigentümer sind WP-046/WP-066 (T2). In defekte gelistet, müsste WP-009 nach der Regel 'WP-Priorität = höchste Priorität seiner Abweichungen' P1 statt P2 tragen. Die Referenz bleibt in abhaengigkeiten. Im Ebenen- bzw. Abweichungstext ist D2 als unbestätigt zu kennzeichnen ('Real-Stack-Refresh der SPA scheitert' ist nicht verifiziert).

<a id="wp-010"></a>
#### WP-010 – WithTenant-Migration: CMDB-Kern I

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** TEN-06
- **Anforderung/Nutzerziel:** Alle DB-Zugriffe von ci, citype, relationship, relationshiptype und topology laufen über WithTenant mit Principal-Scope; client-gescopte Nutzer sehen/ändern keine fremden CMDB-Objekte; Fehler geben keine DB-Interna preis.
- **Implementierung:** Commit 74c1ee0: WithRequestTenant in ci, citype, relationship, relationshiptype; topology über ci/relationship-Repos; Systempfade citype/ddl_job.go:186 (allowgelistet), relationship/cleanup.go:158. Fehlerpfade: relationshiptype/handler.go writeError (Default 409 err.Error()); citype/handler.go:320-324 (WriteDBError, sonst 409 err.Error()); relationship/pg_repository.go:221-222 (fmt.Errorf('not found')) -> relationship.go:586-587 WriteRepoError -> 500; topology/topology.go:273-276 (buildFromRootViaTraversal) -> :133-140 (GetTopology WriteRepoError) -> 500.
- **Test(s):** Baseline PASS: ci TestCIRepositoryClientScope, citype TestCITypeRepositoryScope, relationship TestRelationshipRepositoryClientScope, relationshiptype TestRelationshipTypeRepositoryScope, topology TestTraversalStaysInsideScope, database TestNoDatabaseAccessOutsideTenantContext, tenant/rls TestChannelMatrix. Verifier TestVerifyB02RouterProbe. Welle (Commit 0e02557): TestAuditT1TopologyChannelInvisibleRoots PASS (verweigerte Kanten nicht gespeichert, neighbors/impact 404); TestAuditT1RepositoryErrorsAreSanitized FAILS_AS_EXPECTED (Subtests WP-010_relationship_type_duplicate, WP-010_ci_type_with_a_NUL_byte); TestAuditT1ScopeRefusalsAreNot5xx FAILS_AS_EXPECTED. Real-Stack tenant-isolation.spec.ts T02, T03, T05, T06 PASS; T14, T15, T16 FAIL.
- **Soll/Ist:** SOLL: vollständige WithTenant-Migration, sanitisierte Repository-Fehler, unverändertes Verhalten, Isolationstest je Paket, Guard-Einträge erledigt. IST: Migration, Guard und Isolation belegt (Repo-Tests, Router-Probe, Real-Stack Org-Dimension T02–T06). AK2 beobachtet verletzt: relationship-types (Duplikat) und ci-types (NUL) liefern SQLSTATE/Constraint-Namen. Zusätzlich 500 statt 404/422 bei Topologie-Wurzel außerhalb des Scopes (L-T1-17) und bei Beziehung zu fremdem/unbekanntem CI (ext T14).
- **Evidenz:** SP/work/verify-B02/router-probe.log ('rel-type dup #2' -> 409 '... idx_relationship_type_org_key (SQLSTATE 23505)', 'ci-type create name NUL' -> 409 '... (SQLSTATE 22021)', 'topology root=ci2 as client1' -> 500); SP/verify-tests-run2/defect-TestAuditT1RepositoryErrorsAreSanitized.log (beide WP-010-Subtests FAIL); SP/verify-tests-run2/defect-TestAuditT1ScopeRefusalsAreNot5xx.log ('POST /api/v1/relationships as client1 answered 500', 'GET /api/v1/topology?root_ci_id=... as client1 answered 500'); SP/work/tenant-iso-final.log T14/T15/T16 ✘.
- **Abweichung:** AK2 verletzt (L-T1-05). Topologie-Wurzel außerhalb Scope -> 500 (L-T1-17, P3). POST /relationships zu fremdem CI -> 500 (ext-Defekt T14, Eigentümer WP-128). UI-CI-Anlage scheitert (ext-Defekt T16, Eigentümer WP-117/WP-201) – verhindert die UI->DB-Isolationsprüfung in T16, ist aber nicht T1-ursächlich.
- **Prüfebenen:** UX: N/A (Backend-Migration; UI-Anlage durch ext-Defekt T16 blockiert). API: FEHLER – pgx-Text bei relationship-types/ci-types; 500 bei Fremd-/Unbekannt-Referenzen. Daten: OK – RLS WITH CHECK verhindert Cross-Client-Create/-Move; 0 Kanten gespeichert (TestAuditT1TopologyChannelInvisibleRoots, T14-DB-Teil). E2E: FEHLER – Real-Stack T14/T15/T16 rot, T02–T06 grün. Robustheit: LÜCKE – TestChannelMatrix wertet 500 als korrekte Verweigerung.
- **Abhängigkeiten:** WP-009, WP-041, WP-208 (Kanalmatrix), API-03-Fehlervertrag; extern WP-128 (Relationship-500), WP-117/WP-201 (CI-Formular).
- **Defekte:** F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück, F-145 (P2) POST /relationships mit CI einer anderen Organisation oder unbekannter CI-ID antwortet 500 statt 404/422, F-161 (P2) GET /topology mit unbekannter oder fremder root_ci_id antwortet 500 statt 404
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final FEHLGESCHLAGEN; TEILWEISE BESTANDEN -> FEHLGESCHLAGEN: Das verbindliche AK2 'Repository-Fehler werden weiterhin über api.WriteRepoError gemeldet (keine pgx-Details an Clients)' ist durch den bestätigten P2-Defekt L-T1-05 beobachtet verletzt (Kriteriumsurteil 'no'; Router-Probe und FAILS_AS_EXPECTED der WP-010-Subtests). Nach Statusregel (bestätigter Defekt verletzt ein Kriterium vollständig) FEHLGESCHLAGEN; vereinheitlicht mit WP-017/018/020/021, die für denselben Defekt bereits FEHLGESCHLAGEN waren.
- **Offene Nachweise / Retest:** WP-010-Subtests von TestAuditT1RepositoryErrorsAreSanitized grün (409 generisch bzw. 400/422 ohne SQLSTATE); TestAuditT1ScopeRefusalsAreNot5xx grün; Real-Stack T14/T15 grün; TestChannelMatrix mit exakten 404/422 statt 'nicht 2xx'; client-gescopter Real-Stack-Lauf (Liste nur C1, Beziehungsformular mit C2-CI -> Client-Fehler).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – defekte: „["L-T1-05","L-T1-17","ext:POST /relationships with a CI of another organization (or any unknown CI id) answers 500 Internal Error instead of 404/422","ext:CI creation form ('CI erstellen') sends the type key 'server' as ci_type_id; every manual CI creation through the UI fails with 400"]“ → „["L-T1-05","L-T1-17","ext:POST /relationships with a CI of another organization (or any unknown CI id) answers 500 Internal Error instead of 404/422"]“. Der CI-Formular-Defekt ist in T2/T7 bestätigt und hat P1 (D4). Die Synthese nennt ihn selbst 'nicht T1-ursächlich' (Eigentümer WP-117/WP-201). In defekte gelistet, wäre die WP-Priorität nach Regel P1 statt P2. Als Blocker von T16 gehört er in abweichung bzw. abhaengigkeiten, nicht in defekte.

<a id="wp-011"></a>
#### WP-011 – WithTenant-Migration: CMDB-Kern II

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** TEN-06
- **Anforderung/Nutzerziel:** lifecycle, override, history, contact, credential und privacy greifen nur über WithTenant mit Principal-Scope auf die DB zu; Client-Isolation hält, Fehler bleiben sanitisiert.
- **Implementierung:** Commit d029136: WithRequestTenant in lifecycle, override, history, contact, credential, privacy; Systempfade lifecycle/offline.go:126 (je Org), credential/pg_repository.go:323/398 (DEK-Rotation). Fehlerpfade: lifecycle/handler.go Create (409 err.Error()), :165-167 (422 err.Error()); override/handler.go:176-178 (400 err.Error()); privacy/handler.go:68-71 (PutRetention), :87-90 (RunErasure); contact nutzt WriteDBError/WriteRepoError.
- **Test(s):** Baseline PASS: TestContactRepositoryClientScope, TestCredentialRepositoryClientScope, TestHistoryRepositoryClientScope, TestLifecycleRepositoryScope, TestOverrideRepositoryClientScope, TestPrivacyRepositoryScope, TestNoDatabaseAccessOutsideTenantContext, TestChannelMatrix (/contacts). Verifier TestVerifyB02RouterProbe. Welle: TestAuditT1RepositoryErrorsAreSanitized FAILS_AS_EXPECTED (WP-011_lifecycle_definition_duplicate, _lifecycle_definition_invalid_applies_to, _source_policy_with_a_NUL_byte, _privacy_retention_days_beyond_int4); TestAuditT1CIChildTableDerivation PASS (u.a. ci_change, ci_contact). Real-Stack T05, T06, T13 PASS.
- **Soll/Ist:** SOLL: vollständige Migration, sanitisierte Fehler, Isolationstests. IST: Migration und Client-Isolation belegt (contacts, credentials, history, overrides, CI-Lifecycle-State; Router-Probe: Client-1-Transition auf C2-CI -> 404). AK2 beobachtet verletzt: lifecycle-definitions (23505/23514 als 409), reconciliation/source-policy (22021), privacy/retention (pgx-int4-Encoder-Text mit OID).
- **Evidenz:** SP/work/verify-B02/router-probe.log ('lifecycle def dup #2', 'lifecycle def bad applies_to', 'override source-policy name NUL', 'privacy retention_days 3e9', 'lifecycle transition ci2 as client1' -> 404); SP/verify-tests-run2/defect-TestAuditT1RepositoryErrorsAreSanitized.log (4 WP-011-Subtests FAIL); SP/work/tenant-iso-final.log T05/T06/T13 ✓.
- **Abweichung:** AK2 verletzt (L-T1-05). Router-Isolation für lifecycle-transitions/history/credentials nur teilweise getestet (Evidenzlücke, kein Defekt).
- **Prüfebenen:** UX: N/A (Backend-Migration). API: FEHLER – pg/pgx-Texte, 409 für Validierungsfehler. Daten: OK – Client-Isolation auf PG belegt. E2E: LÜCKE – Real-Stack T05/T06/T13 nur Org-Dimension, kein client-gescopter Real-Login. Robustheit: LÜCKE – applies_to/retention_days nicht validiert.
- **Abhängigkeiten:** WP-009, WP-041, WP-208.
- **Defekte:** F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final FEHLGESCHLAGEN; TEILWEISE BESTANDEN -> FEHLGESCHLAGEN: AK2 (keine pgx-Details an Clients) durch bestätigten Defekt L-T1-05 beobachtet verletzt (Kriteriumsurteil 'no'; Router-Probe + 4 FAIL-Subtests WP-011_*). Statusregel ⇒ FEHLGESCHLAGEN; einheitlich mit WP-017/018/020/021.
- **Offene Nachweise / Retest:** WP-011-Subtests von TestAuditT1RepositoryErrorsAreSanitized grün (409 generisch, 422 Feldfehler für applies_to, 400 Feldfehler für retention_days/NUL); Router-Test Client-1 gegen C2-History (geseedet) und C2-Credentials (404, Zeile unverändert).

<a id="wp-012"></a>
#### WP-012 – WithTenant-Migration: Identität und Rechte

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** TEN-06, AUT-04
- **Anforderung/Nutzerziel:** user, permission, entitlement, identity (API-Key-Store), security, middleware (Idempotenz) und api nutzen WithTenant mit Principal-Scope; API-Keys werden mandantensicher gespeichert und genutzt; Fehler sind sanitisiert.
- **Implementierung:** Commit ea30652: WithRequestTenant in user, permission, entitlement, identity (apikey_store.go List/Revoke/Rotate; MarkUsed :249 org-weit auf Key-Org), security. Pre-Principal-Pfade user/pg_repository.go:1038, user/service_account.go:354, permission/pg_repository.go:170/235, entitlement.go:393. Fehlerpfade user/handler.go:532-533 und weitere Team-/Rollen-Handler (404 err.Error()), permission/handler.go:95-98 (400 err.Error()).
- **Test(s):** Baseline PASS: TestUserRepositoryScope, TestPermissionRepositoryScope, TestEntitlementRepositoryScope, TestSecurityRepositoryClientScope, TestAPIKeyStoreScope (nur Save + MarkUsed), TestIdempotencyScopedByTenant, TestNoDatabaseAccessOutsideTenantContext, TestChannelMatrix (/role-assignments). Verifier-Router-Probe. Welle: TestAuditT1RepositoryErrorsAreSanitized FAILS_AS_EXPECTED (6 Subtests WP-012_*). Real-Stack T05 (B mit eigenem API-Key) PASS.
- **Soll/Ist:** SOLL: vollständige Migration, sanitisierte Fehler, Isolationstest je Paket. IST: Migration/Guard ok; Org-Isolation und Client-Isolation (security) belegt. AK2 beobachtet verletzt in user/permission (7 Pfade, u.a. Duplikat-Team-Mitglied als 404 mit Constraint-Namen, ungültige UUIDs mit SQLSTATE 22P02). AK4 teilweise: API-Key-Store List/Revoke/Rotate ohne Test.
- **Evidenz:** SP/work/verify-B02/router-probe.log ('team members not-a-uuid', 'team add member #2 (dup)' -> 404 'team_member_team_id_user_id_key (SQLSTATE 23505)', 'role-assign role not-a-uuid', 'role perms not-a-uuid', 'role patch/delete not-a-uuid'); SP/verify-tests-run2/defect-TestAuditT1RepositoryErrorsAreSanitized.log (6 WP-012-Subtests FAIL).
- **Abweichung:** AK2 verletzt (L-T1-05). AK4 teilweise (API-Key-Store List/Revoke/Rotate ungetestet – Evidenzlücke). Beobachtung: POST /role-assignments akzeptiert dieselbe Zuweisung doppelt (201/201) – an Gruppe T2 (RBA) weitergegeben.
- **Prüfebenen:** UX: N/A (Backend). API: FEHLER – pgx-Texte, 404 für Duplikate/ungültige Eingaben. Daten: OK – Org-Isolation, Keys mit Ersteller-Scope, MarkUsed auf Key-Org begrenzt. E2E: LÜCKE – roles-authz.spec.ts deckt Rollenwechsel, keine client-gescopten Zuweisungen. Robustheit: LÜCKE – UUID-Parameter erreichen SQL unvalidiert.
- **Abhängigkeiten:** WP-009, WP-041, WP-067, WP-208.
- **Defekte:** F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final FEHLGESCHLAGEN; TEILWEISE BESTANDEN -> FEHLGESCHLAGEN: AK2 durch bestätigten Defekt L-T1-05 beobachtet verletzt (Kriteriumsurteil 'no'; 6 FAIL-Subtests WP-012_*). Statusregel ⇒ FEHLGESCHLAGEN; einheitlich mit WP-017/018/020/021.
- **Offene Nachweise / Retest:** WP-012-Subtests grün (404/400 generisch, Duplikat 409 ohne Constraint-Namen); scopetest für apikey_store List/Revoke/Rotate (Org-B-Scope -> ErrTenantMismatch/not found, Zeile unverändert; ohne Scope -> ErrNoTenantScope).

<a id="wp-013"></a>
#### WP-013 – WithTenant-Migration: Discovery und IPAM

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P0 · **Anforderungen:** TEN-06
- **Anforderung/Nutzerziel:** Discovery und IPAM laufen über WithTenant mit Principal-Scope; client-gescopte Nutzer sehen/ändern keine fremden Netz-/Discovery-Daten; Fehler sind sanitisiert.
- **Implementierung:** Commit 34b84ba: WithRequestTenant in discovery und ipam; dokumentierte System-/Collector-Pfade discovery/pg_repository.go:629/647, ca.go (WithOperator/WithSystem), collector_status.go:159/186, natsedge.go:120/444/469, scope_handler.go:336. Fehler: ipam/handler.go:69-82 writeRepoError (Sentinels mit festen Texten, sonst api.WriteRepoError); discovery ca.go:463-471, discovery.go:942-949.
- **Test(s):** Baseline PASS: TestDiscoveryRepositoryClientScope, TestIPAMRepositoryClientScope (18 Assertions), TestDiscoveryScopes, TestNoDatabaseAccessOutsideTenantContext, TestChannelMatrix (subnets, discovery-scopes, review-items, ingest). Auditor-Probes ipam_probe_test.go/discovery_probe_test.go und Verifier-Router-Probe (keine SQLSTATE). Real-Stack T03/T08 (Review-Inbox, Jobs; Org-Dimension) PASS. Keine Wellen-Tests für dieses WP.
- **Soll/Ist:** SOLL: vollständige Migration, sanitisierte Fehler, Isolationstests, Guard erledigt. IST: erfüllt; belegt durch Integrationstests, Guard und zwei ausgeführte Sanitisierungs-Probes.
- **Evidenz:** SP/runs/results-backend.tsv (pass); SP/work/verify-B02/router-probe.log ('subnet create name NUL' -> 500 generisch, 'subnet get not-a-uuid' -> 404 'subnet not found', 'vrf/collector/discovery scope NUL name' generisch); SP/work/audit-B02/probe/ipam_probe_test.go, discovery_probe_test.go; SP/work/tenant-iso-final.log T03/T08 ✓.
- **Abweichung:** AK4 teilweise verletzt: Review-Items ohne Kandidaten-CIs (unclassified_device, scope_suggested; Erzeuger im Discovery-Paket) sind für client-gescopte Principals über das Discovery-Repository und die von discovery registrierten /review-items-Routen lesbar und verwerfbar (L-T1-03; Prädikat aus 34b84ba vakuum-wahr bei leerer candidate_ci_ids).
- **Prüfebenen:** UX: N/A (Backend). API: OK – sanitisierte Fehler (NUL ergibt generisches 500 statt 400, querschnittliche ClassifyDBError-Lücke ohne Leck). Daten: OK – Client-Isolation für Collectoren, Jobs, Review-Items mit Kandidaten, Subnetze, Adressen, Interfaces. E2E: LÜCKE – Real-Stack nur Org-Dimension (T03/T08). Robustheit: OK – ungültige UUIDs/FKs generisch 4xx, fail-closed ohne Scope; Rest-Risiko Credential-Bundle des Collector-Edge org-weit (Testlücke).
- **Abhängigkeiten:** WP-009, WP-041, WP-208.
- **Defekte:** F-003 (P0) Review-Items ohne Kandidaten-CIs leaken über Clients; review_item ohne DB-seitigen Client/Site-Scope
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final TEILWEISE BESTANDEN; BESTANDEN -> TEILWEISE BESTANDEN: Der bestätigte Defekt L-T1-03 (Defekttest TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope FAILS_AS_EXPECTED über den vollständigen PG-Router) verletzt AK4 im Teilfall der kandidatenlosen Review-Items, die das Discovery-Paket erzeugt und über sein Repository ausliefert. Collectoren, Jobs, Subnetze und Interfaces bleiben belegt isoliert, daher nur teilweise.
- **Offene Nachweise / Retest:** TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope und TestAuditT1ReviewItemRowsCarryClientScope grün; TestDiscoveryRepositoryClientScope um ein kandidatenloses Item eines Client-2-Collectors erweitern (Liste leer, Resolve abgewiesen, Status open). Härtung: Sanitisierungs-Probe als Router-Regressionstest; SQLSTATE 22021 auf 400 abbilden; Client-gescopte Collector-Verwaltung über Router testen.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – status: „BESTANDEN“ → „TEILWEISE BESTANDEN“. AK4 ('Je Paket ... ein client-gescopter Principal sieht und ändert keine Objekte eines anderen Clients') ist durch den bestätigten Defekt L-T1-03 teilweise verletzt. Die Review-Queue gehört zum Discovery-Repository: discovery.PGRepository bettet reviews.PGRepository ein (discovery/pg_repository.go:42-51), und der Discovery-Handler registriert die Routen /review-items und /discovery/review-items (discovery.go:718-719). Das fail-open-Prädikat 'Review-Items über alle Kandidaten-CIs' stammt aus dem WP-013-Commit 34b84ba (reviewVisible in discovery/pg_repository.go, später nach reviews verschoben). Die geleakten Items vom Typ unclassified_device und scope_suggested erzeugt das Discovery-Paket selbst ohne Kandidaten. Defekttest TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope (FAILS_AS_EXPECTED): Client 1 liest Gerätename, Seriennummer, CIDR und Collector von Client 2 und verwirft die Items (200, Status dismissed). Der WP-013-Test TestDiscoveryRepositoryClientScope prüft nur ein Item mit Kandidaten; die Synthese schreibt selbst 'Review-Items mit Kandidaten'. Der Fall ist gleich gelagert wie WP-019 (BESTANDEN -> TEILWEISE wegen L-T1-04 im Teilfall).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „-“ → „P0“. Höchste Priorität der Abweichungen: L-T1-03 (auf P0 korrigiert, siehe defekt_korrekturen). Bleibt L-T1-03 auf P1, dann P1.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – defekte: „[]“ → „["L-T1-03"]“. Der bestätigte Defekt L-T1-03 liegt im Repository und in den Routen dieses WPs, und das Prädikat wurde mit 34b84ba eingeführt.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „-“ → „AK4 teilweise verletzt: Review-Items ohne Kandidaten-CIs (unclassified_device, scope_suggested; Erzeuger im Discovery-Paket) sind für client-gescopte Principals über das Discovery-Repository und die von discovery registrierten /review-items-Routen lesbar und verwerfbar (L-T1-03; Prädikat aus 34b84ba vakuum-wahr bei leerer candidate_ci_ids).“. Folgt aus der Statuskorrektur.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – status_aenderung: „-“ → „BESTANDEN -> TEILWEISE BESTANDEN: Der bestätigte Defekt L-T1-03 (Defekttest TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope FAILS_AS_EXPECTED über den vollständigen PG-Router) verletzt AK4 im Teilfall der kandidatenlosen Review-Items, die das Discovery-Paket erzeugt und über sein Repository ausliefert. Collectoren, Jobs, Subnetze und Interfaces bleiben belegt isoliert, daher nur teilweise.“. Statusregel: Ein bestätigter Defekt, der einen Teil eines verbindlichen Kriteriums verletzt, ergibt TEILWEISE BESTANDEN.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – offene_nachweise: „Keine für BESTANDEN. Härtung: Sanitisierungs-Probe als Router-Regressionstest; SQLSTATE 22021 auf 400 abbilden; Client-gescopte Collector-Verwaltung über Router testen.“ → „TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope und TestAuditT1ReviewItemRowsCarryClientScope grün; TestDiscoveryRepositoryClientScope um ein kandidatenloses Item eines Client-2-Collectors erweitern (Liste leer, Resolve abgewiesen, Status open). Härtung: Sanitisierungs-Probe als Router-Regressionstest; SQLSTATE 22021 auf 400 abbilden; Client-gescopte Collector-Verwaltung über Router testen.“. Folgt aus der Statuskorrektur.

<a id="wp-014"></a>
#### WP-014 – WithTenant-Migration: Standorte

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** TEN-06
- **Anforderung/Nutzerziel:** rack, tenantapi, locationnode (jetzt locations) und location laufen über WithTenant mit Principal-Scope; Objekte ohne eigene Client-Spalte sind nur über sichtbare Eltern sichtbar/schreibbar; Fehler sind sanitisiert.
- **Implementierung:** Commit a05dfb9 (+ locations-Nachfolger WP-053, 2cb5115). rack/pg_repository.go:58-60 rackVisible (LEFT JOIN building), :247-252 ListMounts (EXISTS nur organization_id/id), :557-567 mountWriteError; rack/handler.go:65-77 writeRepoError (err.Error() für ErrValidation/ErrConflict); tenantapi WriteDBError/WriteRepoError; locations/pg_repository.go:281/284.
- **Test(s):** Baseline PASS: TestRackRepositoryClientScope, TestTenantRepositoryClientScope, TestLocationRepositoryClientScope, TestLocationTreeScope, TestLocationTreeMatrixAndDerivation, TestHierarchyScopeOnPostgreSQL, TestNoDatabaseAccessOutsideTenantContext, TestChannelMatrix. Verifier-Router-Probe. Welle: TestAuditT1LocationChannelBelowSites PASS (Buildings/Rooms; Racks für Client/Org/Mehrfach/Org B); TestAuditT1RepositoryErrorsAreSanitized FAILS_AS_EXPECTED (WP-014_rack_mount_with_an_invalid_ci_id, _unknown_ci_id, _of_a_client-2_CI_by_client_1); TestAuditT1RackChannelSiteScope FAILS_AS_EXPECTED. Real-Stack T01, T07, L01, L03, L06, L10 PASS.
- **Soll/Ist:** SOLL: vollständige Migration, sanitisierte Fehler, Isolationstests für rack/tenantapi/locationnode/location. IST: Migration und Client-Isolation belegt (inkl. Real-Stack-Location-Baum, Org-Dimension). AK2 beobachtet verletzt: Rack-Mount-Erstellung liefert SQLSTATE, FK-Constraint- und RLS-Policy-Text. Im WP-eigenen Rack-Prädikat zusätzlich Site-Scope-Bypass (fremde Racks lesbar und löschbar) und ein Mounts-Existenzorakel.
- **Evidenz:** SP/work/verify-B02/router-probe.log ('rack mount ci not-a-uuid' -> 400 '(SQLSTATE 22P02)', 'rack mount ci unknown' -> 400 'rack_mount_ci_id_fkey (SQLSTATE 23503)', 'rack mount ci2 as client1' -> 400 'row-level security policy for table rack_mount (SQLSTATE 42501)'); SP/verify-tests-run2/defect-TestAuditT1RackChannelSiteScope.log ('GET /racks as site2: rack t1-rack-one listed true', 'DELETE rack t1-rack-one as site2: 204', 'rack/location rows 0/1', '/mounts (t1-rack-two) as client1: 200, want 404'); defect-TestAuditT1RepositoryErrorsAreSanitized.log (3 WP-014-Subtests FAIL); SP/work/locrack/run-final.log L01/L03/L06/L10 ✓.
- **Abweichung:** AK2 verletzt (L-T1-05). Site-Scope-Bypass im Rack-Prädikat rackVisible (L-T1-02, P1) – kein wörtliches AK (AK4 nennt client-gescopte Principals), aber Fehler im von WP-014 migrierten Rack-Code; Mounts-Existenzorakel (L-T1-18, P3).
- **Prüfebenen:** UX: N/A (Backend; Racks werden in der UI über /racks gelistet, L-T1-02 daher UI-relevant). API: FEHLER – Mount-Fehler mit RLS-/Constraint-Text; site-fremde Racks lesbar/löschbar. Daten: FEHLER – Cross-Site-DELETE entfernt rack-Zeile, location-Eintrag verwaist. E2E: LÜCKE – Real-Stack T01/T07/L01–L10 grün, aber nur Org-Dimension; kein site-gescopter Real-Login. Robustheit: LÜCKE – ci_id ohne UUID-Validierung.
- **Abhängigkeiten:** WP-009, WP-041, WP-053, WP-208; DB-seitiger Site-Scope für rack/room fehlt (L-T1-11, WP-027).
- **Defekte:** F-002 (P0) Site-Scope-Bypass im Rack-Kanal: site-fremde Racks lesbar und löschbar, F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück, F-180 (P3) GET /api/v1/racks/{id}/mounts eines Racks außerhalb des Scopes antwortet 200 (leer) statt 404 (Existenzorakel)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final FEHLGESCHLAGEN; TEILWEISE BESTANDEN -> FEHLGESCHLAGEN: AK2 durch bestätigten Defekt L-T1-05 beobachtet verletzt (Kriteriumsurteil 'no'; 3 FAIL-Subtests WP-014_*). Statusregel ⇒ FEHLGESCHLAGEN (einheitlich mit WP-017/018/020/021). Priorität P2 -> P1 wegen des bestätigten Site-Scope-Bypass L-T1-02 im WP-eigenen rackVisible-Prädikat (Defekttest TestAuditT1RackChannelSiteScope).
- **Offene Nachweise / Retest:** WP-014-Subtests von TestAuditT1RepositoryErrorsAreSanitized grün; TestAuditT1RackChannelSiteScope grün (404 für Liste/Detail/Layout/Mounts/GraphQL/DELETE außerhalb des Scopes, rack/location 1/1); TestAuditT1LocationChannelBelowSites bleibt grün.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Folgt aus der Korrektur von L-T1-02 auf P0. Die Begründung in status_aenderung ('P2 -> P1') lautet damit 'P2 -> P0'.

<a id="wp-015"></a>
#### WP-015 – WithTenant-Migration: Suche, Sichten, Export (Requestpfade)

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** TEN-06
- **Anforderung/Nutzerziel:** Such-, Saved-View- (inkl. Query-Engine) und Export-Requestpfade laufen über WithTenant mit Principal-Scope; client-gescopte Nutzer erhalten keine fremden Treffer, Sichten oder Exportzeilen; Fehler sind sanitisiert.
- **Implementierung:** Commit 5cf39f2: WithRequestTenant in search, savedview, export; Reindex org-weit (search/pg_repository.go:212, hybrid.go:59, dokumentiert); Export-Worker-Scope export/pg_job_repository.go:42 (Systempfad), Ersteller-Snapshot export/job.go. Fehlerpfade savedview/handler.go:136-137 (409 err.Error()), :222-225 (400 err.Error()); savedview/filter.go:123-150 (::uuid ohne Validierung); search/handler.go:37-41 (400 err.Error()).
- **Test(s):** Baseline PASS: TestSearchRepositoryClientScope, TestSearchHitsRespectClientSiteAndReadPermission, TestSavedViewScope, TestStructuredSearchScopeDepthAndPermission, TestExportScope, TestExportJobRunsWithCreatorScope, TestNoDatabaseAccessOutsideTenantContext, TestChannelMatrix (search, export). Verifier-Router-Probe. Welle: TestAuditT1RepositoryErrorsAreSanitized FAILS_AS_EXPECTED (WP-015_saved_view_duplicate, _structured_search_with_an_invalid_upstream_of, _full-text_search_with_a_NUL_byte). Real-Stack T03, T13 (tenant-isolation), B03, V01, E01 (bulk-jobs-export) PASS.
- **Soll/Ist:** SOLL: vollständige Migration, sanitisierte Fehler, Isolationstests. IST: Migration und Isolation belegt (Repo-Tests; Real-Stack: Suche/Export/Saved View fremder Tenants unsichtbar, ungültiger Filter-AST 422). AK2 beobachtet verletzt bei Saved-View-Duplikat (23505), upstream_of (22P02) und Volltextsuche mit NUL (22021).
- **Evidenz:** SP/work/verify-B02/router-probe.log ('saved view dup #2', 'query upstream_of not-a-uuid', 'search q NUL'); SP/verify-tests-run2/defect-TestAuditT1RepositoryErrorsAreSanitized.log (3 WP-015-Subtests FAIL); SP/work/bulk-jobs-export/final.log B03/V01/E01 ✓; SP/work/tenant-iso-final.log T03/T13 ✓.
- **Abweichung:** AK2 verletzt (L-T1-05). Saved-View-Update maskiert Konflikte als 404 (Code, gleiche Ursache).
- **Prüfebenen:** UX: N/A (Backend). API: FEHLER – Saved-View-Create, Filter-Query und Volltextsuche leaken pg-Fehler. Daten: OK – Scope in Suche, Saved-View-Ausführung und Export (inkl. Ersteller-Snapshot; Real-Stack V01/E01). E2E: LÜCKE – Real-Stack nur Org-Dimension, kein client-gescopter Nutzer. Robustheit: LÜCKE – upstream_of/downstream_of/q nicht validiert.
- **Abhängigkeiten:** WP-009, WP-041, WP-208.
- **Defekte:** F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final FEHLGESCHLAGEN; TEILWEISE BESTANDEN -> FEHLGESCHLAGEN: AK2 durch bestätigten Defekt L-T1-05 beobachtet verletzt (Kriteriumsurteil 'no'; 3 FAIL-Subtests WP-015_*). Statusregel ⇒ FEHLGESCHLAGEN; einheitlich mit WP-017/018/020/021.
- **Offene Nachweise / Retest:** WP-015-Subtests grün (409 generisch, 422 Feldfehler upstream_of, 400 für q); Router-Test client-gescopter Saved-View-Ausführung (geteilte Sicht liefert nur C1-Zeilen).

<a id="wp-016"></a>
#### WP-016 – WithTenant-Migration: Webhook, Audit, Monitoring (Requestpfade)

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** TEN-06
- **Anforderung/Nutzerziel:** Webhook-, Audit- und Monitoring-Requestpfade laufen über WithTenant mit Principal-Scope; client-gescopte Principals lesen/schreiben keine fremden Metriken/Audit-Einträge; Fehler sind sanitisiert.
- **Implementierung:** Commit dfa9ecd: WithRequestTenant in webhook, audit, monitoring. Org-weite Pfade webhook/pg_delivery_store.go:33-35 (orgScope), webhook/pg_repository.go:277-293 RotateSecret (OrgWideScope(orgID,''), undokumentiert), audit/audit.go:379-385 Verify. Fehler monitoring/monitoring.go:585-588 (CreateAlert 409 err.Error()). Später ergänzt (WP-190, d80d173): monitoring/pg_store.go:230-236 MetricNames ohne Scope-Prädikat; monitoring.go:836-872 CIMetrics ohne CI-Sichtbarkeitsprüfung.
- **Test(s):** Baseline PASS: TestWebhookRepositoryScope, TestAuditScope, TestMonitoringStoreScope, TestNoDatabaseAccessOutsideTenantContext, TestChannelMatrix (/webhooks); Verifier-Probes TestVerifyB02RotateSecretWithoutScope, Router-Probe. Welle: FAILS_AS_EXPECTED TestAuditT1RepositoryErrorsAreSanitized (WP-016_alert_rule_with_a_NUL_byte, _duration_overflow), TestAuditT1CIMetricsEndpointScope; PASS TestAuditT1OrgWideChannelWrites (Webhook-Löschung durch Org B -> 404), TestAuditT1MetricReadPathsHonourScope. Real-Stack T03, T12 PASS.
- **Soll/Ist:** SOLL: vollständige Migration mit Request-Scope, sanitisierte Fehler, Isolationstests. IST: Migration und Isolationstests grün, Audit-Isolation real belegt (T12, Hash-Kette intakt). AK2 beobachtet verletzt (CreateAlert 409 mit SQLSTATE 22021 bzw. pgx-Scan-Text). AK1 teilweise (RotateSecret/Delivery-Requestpfade org-weit). Der Monitoring-Lesepfad GET /cis/{id}/metrics legt Serienamen fremder Client-CIs offen.
- **Evidenz:** SP/work/verify-B02/router-probe.log ('alert name NUL' -> 409 '(SQLSTATE 22021)', 'alert duration overflow' -> 409 'can't scan into dest[6]'); SP/work/verify-B02/webhook-probe.log (RotateSecret ohne Scope und mit Org-B-Scope erfolgreich); SP/verify-tests-run2/defect-TestAuditT1CIMetricsEndpointScope.log ('GET /cis/{client-2 CI}/metrics as client1: 200 and discloses the series name'); SP/work/tenant-iso-final.log T03/T12 ✓.
- **Abweichung:** AK2 verletzt (L-T1-05). AK1 teilweise: RotateSecret/Delivery-Requestpfade mit OrgWideScope (L-T1-21, P3). Metrik-Namen-Leck im Monitoring-Lesepfad (L-T1-10, P2; Ursache MetricNames aus WP-190).
- **Prüfebenen:** UX: N/A (Backend). API: FEHLER – CreateAlert 409 mit pg/pgx-Text; GET /cis/{fremd}/metrics 200 mit Serienamen. Daten: OK – Samples/Audit-Einträge in den Query-Pfaden per Client gefiltert (TestAuditT1MetricReadPathsHonourScope). E2E: LÜCKE – Real-Stack T03/T12 nur Org-Dimension. Robustheit: LÜCKE – RotateSecret nicht fail-closed, duration unbegrenzt.
- **Abhängigkeiten:** WP-009, WP-041, WP-208; WP-190 (MetricNames), WP-040.
- **Defekte:** F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück, F-052 (P2) Metrik-Serienamen eines fremden Client-CI über GET /api/v1/cis/{id}/metrics sichtbar, F-183 (P3) Webhook RotateSecret (und Delivery-Requestpfade) nutzen einen org-weiten Scope statt des Request-Scopes
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final FEHLGESCHLAGEN; TEILWEISE BESTANDEN -> FEHLGESCHLAGEN: AK2 durch bestätigten Defekt L-T1-05 beobachtet verletzt (Kriteriumsurteil 'no'; 2 FAIL-Subtests WP-016_*). Statusregel ⇒ FEHLGESCHLAGEN; einheitlich mit WP-017/018/020/021.
- **Offene Nachweise / Retest:** WP-016-Subtests grün (422 Feldfehler ohne SQLSTATE/pgx-Text); TestAuditT1CIMetricsEndpointScope grün; RotateSecret ohne Scope -> ErrNoTenantScope, mit Org-B-Scope -> ErrTenantMismatch (Probe als Repo-Test); Audit-Liste ohne Scope -> ErrNoTenantScope.

<a id="wp-017"></a>
#### WP-017 – WithTenant-Migration: Asset I (asset, assignment, reservation, composition)

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** TEN-06, AST-01, MGT-11
- **Anforderung/Nutzerziel:** Jeder DB-Zugriff von asset, assignment, reservation und composition läuft in WithTenant mit Principal-Scope; client-gescopte Nutzer sehen/ändern keine fremden Objekte; keine pgx-Details in API-Fehlern.
- **Implementierung:** Commit deaf5d3: asset/pg_repository.go:56,144,164,214,294; assignment/pg_repository.go:73-281; reservation/pg_repository.go:59-160 + availability.go:34; composition/pg_repository.go:61-254; reservation ExpireDue :196-197 (Systempfad WP-022). Fehlerlecks reservation/handler.go:161-163 (Create), :176-179 (Release); asset/handler.go:257-259 (rejectParentOwnedFields -> composition ParentOf, composition/pg_repository.go:229-254). deaf5d3 änderte nur composition/handler.go.
- **Test(s):** Baseline PASS: TestAssetRepositoryClientScope, TestAssignmentRepositoryClientScope, TestReservationRepositoryClientScope, TestCompositionRepositoryClientScope, TestParentOfCIIsTenantScoped, TestNoDatabaseAccessOutsideTenantContext; Verifier-Probes TestVerifyB03Reservation, TestVerifyB03Asset. Welle: TestAuditT1ModuleChannelsClientScope PASS (assets, reservations über Router: Listen, Detail 404, PATCH/DELETE/Release verweigert, Zeilen unverändert, Org B); TestAuditT1RepositoryErrorsAreSanitized FAILS_AS_EXPECTED (WP-017_reservation_of_an_unknown_asset, _release_of_an_invalid_reservation_id, _release_of_a_client-2_reservation_by_client_1, _PATCH_asset_with_an_invalid_id).
- **Soll/Ist:** SOLL: WithTenant mit Principal-Scope, sanitisierte Fehler, Isolationstest je Paket. IST: Migration und Isolation belegt (jetzt auch Router-Ebene). AK2 beobachtet verletzt: reservation Create/Release und asset PATCH liefern pgx-Texte mit Tabellen-/Constraint-Namen und falsche Status; Release einer unsichtbaren Reservation 409 statt 404.
- **Evidenz:** SP/work/verify-B03/probes-all.log (CREATE-FK 409 'reservation_asset_id_fkey (SQLSTATE 23503)', RELEASE-BADID 409 '(SQLSTATE 22P02)', PATCH /assets/not-a-uuid 409 'lookup parent composition ... 22P02'); SP/verify-tests-run2/defect-TestAuditT1RepositoryErrorsAreSanitized.log (4 WP-017-Subtests FAIL); SP/work/t1-tests/untagged.log PASS TestAuditT1ModuleChannelsClientScope/{assets,reservations}.
- **Abweichung:** AK2 verletzt in reservation/handler.go und asset/handler.go (L-T1-05, inkl. neuem Wellenbefund 'Release unsichtbarer Reservation 409 statt 404').
- **Prüfebenen:** UX: N/A (Backend; Client-Create ohne client_id erhält sanitisiertes 403). API: FEHLER – pgx-Texte, 409 für FK-/Syntaxfehler. Daten: OK – Isolation inkl. Router-Ebene, fremde Zeilen unverändert. E2E: LÜCKE – kein Real-Stack-Lauf mit client-gescoptem Nutzer. Robustheit: LÜCKE – UUID-Pfadparameter unvalidiert; Fremd-Updates assignment/composition ungetestet.
- **Abhängigkeiten:** WP-009, WP-041, WP-023, WP-025.
- **Defekte:** F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** WP-017-Subtests von TestAuditT1RepositoryErrorsAreSanitized grün (422/400/404 ohne SQLSTATE, Duplikat-Reservation weiter 409 mit Domänentext); Fremd-Update-Tests assignment Return/Transfer und composition Update (nicht kodiert, Welle 'not_codified' c).

<a id="wp-018"></a>
#### WP-018 – WithTenant-Migration: Asset II / Lager (movement, stocktake, consumable, order, disposal)

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** TEN-06
- **Anforderung/Nutzerziel:** Lager-Module greifen nur über WithTenant mit Principal-Scope auf die DB zu; client-gescopte Nutzer sehen/ändern keine fremden Lagerobjekte; Fehler sind sanitisiert.
- **Implementierung:** Commit a413ce0: movement/pg_repository.go:68-319, stocktake/pg_repository.go:87-473, consumable/pg_repository.go:42-215, order/pg_repository.go:86-287, disposal/pg_repository.go:58-112 mit WithRequestTenant. Fehlerlecks movement/handler.go:150-152 (Record, 400 err.Error()), :221-223 (CreateItem, 409 err.Error()); a413ce0 änderte movement/handler.go nicht.
- **Test(s):** Baseline PASS: TestMovementRepositoryClientScope, TestStocktakeRepositoryClientScope, TestConsumableRepositoryClientScope, TestOrderRepositoryClientScope, TestDisposalRepositoryClientScope, TestNoDatabaseAccessOutsideTenantContext; Verifier-Probe TestVerifyB03Movement. Welle: TestAuditT1ModuleChannelsClientScope PASS (consumables, inventory_items, stock_movements); TestAuditT1RepositoryErrorsAreSanitized FAILS_AS_EXPECTED (WP-018_stock_movement_of_an_unknown_asset, WP-018_inventory_item_of_an_unknown_client).
- **Soll/Ist:** SOLL: WithTenant überall, sanitisierte Fehler, Isolationstest je Paket. IST: Migration und Isolation grün (jetzt auch Router-Ebene); AK2 beobachtet verletzt: movement Record (400) und CreateItem (409) liefern Tabellen-/Constraint-Namen; Fremdobjekt-Versuch 400 statt 404.
- **Evidenz:** SP/work/verify-B03/probes-all.log (RECORD-FK 400 'asset_movement_asset_id_fkey (SQLSTATE 23503)', CREATEITEM-FK 409 'quantity_item_client_id_fkey', C1-RECORD-C2 400 'not found' + 0 Zeilen); SP/verify-tests-run2/defect-TestAuditT1RepositoryErrorsAreSanitized.log (2 WP-018-Subtests FAIL); SP/work/t1-tests/untagged.log PASS TestAuditT1ModuleChannelsClientScope.
- **Abweichung:** AK2 verletzt in movement/handler.go (L-T1-05).
- **Prüfebenen:** UX: N/A (Backend). API: FEHLER – pgx-Texte, 400/409 statt 422/404. Daten: OK – Isolation inkl. Router-Ebene; append-only Bewegungen unverändert. E2E: LÜCKE – kein Real-Stack-Lagerfluss. Robustheit: LÜCKE – Fremd-Update/Approve-Pfade (order, consumable, quantity item, stocktake Complete) ungetestet.
- **Abhängigkeiten:** WP-009, WP-041, WP-023.
- **Defekte:** F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** WP-018-Subtests grün (422 ohne SQLSTATE, Fremd-Bewegung 404); Fremd-Update-Tests für order/consumable/quantity item/stocktake-Korrekturen.

<a id="wp-019"></a>
#### WP-019 – WithTenant-Migration: Module I (document, desk, keymgmt, training)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P0 · **Anforderungen:** TEN-06, MGT-03
- **Anforderung/Nutzerziel:** Dokumente, Desks, Schlüssel und Trainings werden nur in WithTenant mit Principal-Scope gelesen/geschrieben; client-gescopte Nutzer sehen/ändern keine Objekte anderer Clients.
- **Implementierung:** Commit e30e036: document/pg_repository.go:103-395, desk/pg_repository.go:85-259, keymgmt/pg_repository.go:53-246, training/pg_repository.go:48-195 mit WithRequestTenant; Handler mit api.WriteRepoError (document/handler.go:104,186,291,371,389; desk/handler.go:49,94,140; keymgmt/handler.go:49,97,198; training/handler.go:45,91,137). Sichtbarkeit geteilter Dokumente: document/pg_repository.go:80-81 documentVisible ('kein Link oder ein sichtbarer Link'), Policy-Design aus WP-028 (000064/000079).
- **Test(s):** Baseline PASS: TestDocumentRepositoryClientScope, TestDeskRepositoryClientScope, TestKeyRepositoryClientScope, TestTrainingRepositoryScope, TestNoDatabaseAccessOutsideTenantContext; Verifier-Probes TestVerifyB03document/training/desk, TestAuditProbeKeymgmtRepoError, TestAuditProbeDeskRepoError (keine SQLSTATE; C1-PATCH-C2DOC 404, C1-PATCH-C2DESK 404). Welle: TestAuditT1DocumentWritesOutsideScope FAILS_AS_EXPECTED (über Document-Handler + PG).
- **Soll/Ist:** SOLL: WithTenant, sanitisierte Fehler, Isolationstest je Paket. IST: Migration vollständig; Sanitisierung in allen 4 Paketen per Probes belegt; Einzel-Client-Isolation belegt. Für geteilte (Client 1 + Client 2) und unverlinkte Dokumente ändert/löscht ein client-gescopter Principal jedoch Daten anderer Clients: PATCH 200, DELETE 204 mit Kaskaden-Löschung der Client-2-Links, Kapern org-weiter Dokumente.
- **Evidenz:** SP/work/verify-B03/probes-all.log (Sanitisierung, Fremd-Updates 404); SP/verify-tests-run2/defect-TestAuditT1DocumentWritesOutsideScope.log ('PATCH as client 1: 200', 'DELETE as client 1: 204', 'client-2 asset links after DELETE by client 1: 0, want 1').
- **Abweichung:** AK4 im Teilfall geteilter/unverlinkter Dokumente verletzt (L-T1-04 P1, L-T1-06 P2; Ursache im Policy-Design von WP-028). Desk-Book/Training-Assign mappen Nicht-Domänenfehler auf 404 (vorbestehend, kein Kriterium).
- **Prüfebenen:** UX: N/A (Backend; Modul in G1 aus). API: FEHLER – Document-Handler: Client-1 PATCH/DELETE eines geteilten Dokuments 200/204. Daten: FEHLER – FK-Kaskade löscht document_link-Zeilen von Client 2. E2E: LÜCKE – kein Router-/Real-E2E für desk/keymgmt/training mit client-gescoptem Principal. Robustheit: LÜCKE – Sanitisierung nur per Probe, keine Repo-Regressionstests.
- **Abhängigkeiten:** WP-009, WP-041, WP-023, WP-025/WP-028 (document_link-Scope, Policy-Design der Mehrobjekt-Zeilen); Modul in G1 per WP-214 abgeschaltet.
- **Defekte:** F-004 (P0) Geteilte Dokumente und Wartungsfenster: teilgescopter Principal ändert/löscht sie, FK-Kaskade entfernt Links anderer Clients, F-048 (P2) Unverlinkte (org-weite) Dokumente und Wartungsfenster durch client-gescopte Principals anleg-, änder-, lösch- und kaperbar (E-09)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation BESTANDEN → final TEILWEISE BESTANDEN; BESTANDEN -> TEILWEISE BESTANDEN: Der bestätigte P1-Defekt L-T1-04 (Defekttest TestAuditT1DocumentWritesOutsideScope über den Document-Handler FAILS_AS_EXPECTED) zeigt, dass ein client-gescopter Principal über ein geteiltes Dokument Link-Zeilen von Client 2 löscht; damit ist AK4 ('sieht und ändert keine Objekte eines anderen Clients') für das Paket document im Teilfall geteilter Mehrobjekt-Zeilen verletzt. BESTANDEN ist nach Regel ausgeschlossen; da Einzel-Client-Isolation und die Pakete desk/keymgmt/training belegt bleiben (nur Teil betroffen), TEILWEISE BESTANDEN.
- **Offene Nachweise / Retest:** TestAuditT1DocumentWritesOutsideScope grün (geteiltes Dokument: PATCH/DELETE 403/404, Client-2-Links intakt; org-weites Dokument nicht kaperbar; Positivkontrollen 200/204); Sanitisierungs-Probes als Repo-Regressionstests.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Folgt aus der Korrektur von L-T1-04 auf P0.

<a id="wp-020"></a>
#### WP-020 – WithTenant-Migration: Module II (ticket, sla, maintenance, compliance)

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** TEN-06, TKT-01, MGT-04
- **Anforderung/Nutzerziel:** Tickets, SLAs, Wartungsfenster und Compliance-Daten nur über WithTenant mit Principal-Scope; client-gescopte Nutzer sehen/ändern nur eigene Objekte; Fehler sanitisiert.
- **Implementierung:** Commit ea88c47: ticket/pg_repository.go:87-377, sla/pg_repository.go:36-251, maintenance/pg_repository.go:66-260, compliance/pg_repository.go:30-177 mit WithRequestTenant. compliance/evaluator.go:28-35 ersetzt den Request-Scope durch OrgWideScope; compliance/handler.go:129-134 liefert die vollständige EvaluationResponse; ReplaceResults pg_repository.go:160-172. maintenance/pg_repository.go:41-43 windowVisible (auch UPDATE :181, DELETE :200), FK ON DELETE CASCADE. Fehlerlecks compliance/handler.go:54,91,131,144,156,176; ticket/handler.go:250-252; sla/handler.go:143-146.
- **Test(s):** Baseline PASS: TestTicketRepositoryClientScope, TestSLARepositoryClientScope, TestMaintenanceRepositoryClientScope, TestMaintenanceWindowNotifyDerivesClients, TestNotifyQueuesInPostgres, TestComplianceScope (kodiert den Cross-Client-Write als erwartet, compliance_scope_integration_test.go:32-42), TestNoDatabaseAccessOutsideTenantContext; Probes TestVerifyB03Compliance/maintenance/ticket/sla. Welle: FAILS_AS_EXPECTED TestAuditT1ComplianceEvaluationStaysInCallerScope, TestAuditT1MaintenanceWindowWritesOutsideScope, TestAuditT1RepositoryErrorsAreSanitized (WP-020_compliance_rule_of_an_unknown_ci_type, _ticket_comment_of_an_invalid_ticket_id, _SLA_attach_with_an_invalid_sla_id); PASS TestAuditT1ModuleChannelsClientScope (tickets).
- **Soll/Ist:** SOLL: client-gescopte Principals sehen/ändern nur eigene Objekte, Fehler sanitisiert. IST: Ticket/SLA erfüllen das (jetzt auch Router-Ebene). Compliance-Auswertung läuft für client-gescopte Aufrufer org-weit, liefert Ergebnisse anderer Clients und überschreibt deren Zeilen (P0). Gemischte Wartungsfenster sind durch Client 1 umbenenn-, storno- und löschbar (Kaskade löscht Client-2-Links); org-weite Fenster durch client-gescopte Principals anlegbar. Mehrere Handler leaken pgx-Texte.
- **Evidenz:** SP/verify-tests-run2/defect-TestAuditT1ComplianceEvaluationStaysInCallerScope.log ('POST /compliance/evaluations as client 1 answered 200 and lists the client-2 CI'); defect-TestAuditT1MaintenanceWindowWritesOutsideScope.log ('PATCH as client 1: 200', 'DELETE as client 1: 204', 'client-2 link rows after DELETE by client 1: 0, want 1', 'POST an org-wide window (no CIs) as client 1: 201'); SP/work/verify-B03/probes-all.log (C1-EVALUATE foreignCI-in-body=true, rewritten=true; CREATE-RULE-FK 500 '(SQLSTATE 23503)').
- **Abweichung:** Cross-Client-Offenlegung und -Überschreibung über die Compliance-Auswertung (L-T1-01, P0; AK4 'no', AK1 'partial'). Cross-Client-Schreiben/Löschen gemischter Wartungsfenster (L-T1-04, P1). Org-weite Fenster durch client-gescopte Principals anlegbar (L-T1-06, P2). AK2 verletzt in compliance/ticket/sla (L-T1-05).
- **Prüfebenen:** UX: N/A (Backend). API: FEHLER – Compliance-POST liefert Fremd-Client-Ergebnisse; pgx-Texte in compliance/ticket/sla. Daten: FEHLER – fremde compliance_result-Zeilen gelöscht/neu angelegt; maintenance_window_ci von Client 2 per Kaskade gelöscht. E2E: LÜCKE – kein Real-Stack-Lauf mit client-gescoptem Nutzer. Robustheit: LÜCKE – Evaluator kappt auf 100 Regeln × 100 CIs, ReplaceResults löscht alle Org-Ergebnisse; UUIDs unvalidiert.
- **Abhängigkeiten:** WP-009, WP-041, WP-023, WP-028/029 (Scope-Spalten maintenance_window).
- **Defekte:** F-001 (P0) Compliance-Auswertung durch client-gescopten Principal liefert und überschreibt Ergebnisse anderer Clients, F-004 (P0) Geteilte Dokumente und Wartungsfenster: teilgescopter Principal ändert/löscht sie, FK-Kaskade entfernt Links anderer Clients, F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück, F-048 (P2) Unverlinkte (org-weite) Dokumente und Wartungsfenster durch client-gescopte Principals anleg-, änder-, lösch- und kaperbar (E-09)
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT1ComplianceEvaluationStaysInCallerScope, TestAuditT1MaintenanceWindowWritesOutsideScope und die WP-020-Subtests von TestAuditT1RepositoryErrorsAreSanitized grün; TestComplianceScope umgeschrieben (kein Cross-Client-Write); NotifyClients durch Client 1 reiht nur Client-1-Benachrichtigung ein (nicht kodiert).

<a id="wp-021"></a>
#### WP-021 – WithTenant-Migration: Module III (form, workflow, agent, ai, iga)

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** TEN-06, WFL-01
- **Anforderung/Nutzerziel:** Formulare, Workflows, Endpoint-Agents, AI-Chunks und IGA-Daten nur über WithTenant mit Principal-Scope; client-gescopte Nutzer sehen/ändern keine fremden Objekte; Fehler sanitisiert.
- **Implementierung:** Commit 035b3e6: form/pg_repository.go:50-219, workflow/pg_repository.go:27-261, agent/pg_repository.go:55-301, ai/pg_repository.go:32-163, iga/pg_repository.go:42-520 mit WithRequestTenant. Fehlerlecks form/handler.go:45,85,127,141,195; workflow/handler.go:44,81,150; iga/handler.go:142,270,287,322,371,460 (api.WriteError 500 err.Error()). Verschluckte Writes iga/handler.go:346 ('_ = h.repo.CreateTask'), :203,207,242,404,433,452,455. agent/ai-Handler sauber.
- **Test(s):** Baseline PASS: TestFormRepositoryClientScope, TestWorkflowRepositoryScope, TestAgentRepositoryClientScope, TestAIChunkScope, TestRAGChunksFollowSourceScope, TestIGARepositoryScope, TestNoDatabaseAccessOutsideTenantContext; Probes TestVerifyB03form/workflow/iga/igaSwallow. Welle: FAILS_AS_EXPECTED TestAuditT1RepositoryErrorsAreSanitized (WP-021_form_for_client_2_by_client_1, _workflow_runs_filtered_by_an_invalid_workflow_id, _IGA_access_request_for_an_unknown_subject), TestAuditT1ApprovalNeverLosesTheProvisioningTask.
- **Soll/Ist:** SOLL: WithTenant, sanitisierte Fehler, Isolationstests. IST: Migration fertig, Isolationstests grün (Form per Probe: C2-form_def 404). form/workflow/IGA beantworten Repository-Fehler mit 500 und RLS-/FK-/Syntaxtext; IGA verschluckt Schreibfehler (Genehmigung ohne Provisioning-Task); AI-Schreibseite ohne Isolationsnachweis.
- **Evidenz:** SP/work/verify-B03/probes-all.log (form C1-CREATE-FOREIGN-CLIENT 500 'row-level security policy for table form_def (SQLSTATE 42501)', 'APPROVE-WITH-FAILING-CREATETASK status=200 db_ar_status=approved provisioning_tasks=0'); SP/verify-tests-run2/defect-TestAuditT1RepositoryErrorsAreSanitized.log (3 WP-021-Subtests FAIL); defect-TestAuditT1ApprovalNeverLosesTheProvisioningTask.log ('approve answered 200 while the provisioning task could not be written; DB status approved, tasks 0').
- **Abweichung:** AK2 verletzt in form/workflow/IGA (L-T1-05). IGA ignoriert Repository-Schreibfehler (L-T1-14, P2). AK4 teilweise: AI-Schreibseite (UpsertChunk/DeleteChunks) ohne Client-Isolationsnachweis.
- **Prüfebenen:** UX: N/A (Backend; Client-Nutzer erhält beim Formular-Anlegen 500 statt 403). API: FEHLER – 500 mit RLS-/Policy-/Constraint-Namen. Daten: FEHLER – IGA-Genehmigung persistiert 'approved' ohne Task. E2E: LÜCKE – kein Router-/Real-E2E mit client-gescoptem Principal. Robustheit: LÜCKE – IGA-Atomarität, AI-Schreibseite ungetestet.
- **Abhängigkeiten:** WP-009, WP-041, WP-023.
- **Defekte:** F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück, F-056 (P2) IGA-Handler ignoriert Repository-Schreibfehler (Genehmigung ohne Provisioning-Task)
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** WP-021-Subtests grün (403 für RLS, 422 für FK, 400 für ungültige ids); TestAuditT1ApprovalNeverLosesTheProvisioningTask grün; AI-Schreibseiten-Isolationstest (Client 1 UpsertChunk/DeleteChunks für C2-CI abgewiesen).

<a id="wp-022"></a>
#### WP-022 – Systemworker mandantenweise; Systemausnahmen (app.system) eingrenzen

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** TEN-06, TEN-05, OPS-05, MON-04, AST-06
- **Anforderung/Nutzerziel:** Hintergrund-Worker finden Arbeit nur unter app.system und ändern Daten je Organisation in WithTenant; System-Policies sind SELECT-only; Worker-Fehler werden geloggt, gezählt und nicht verschluckt.
- **Implementierung:** Commit a18e86d: migrations/000060_restrict_system_policies.up.sql:17-43; database/pool.go:283-313 (WithSystem READ ONLY), :377-397 (OrganizationIDs); webhook/pg_delivery_store.go:127-165; export/pg_job_repository.go:41-45,137-170,189-300, export/worker.go:140-145; monitoring/pg_alert_store.go:159-198, evaluator.go:94-117,130-134; reservation/pg_repository.go:184-210, sweeper.go:28-44 (Scheduler cmd/server/main.go:478); jobs/scheduler.go:181-190; tenant/rls/known_gaps.go:120 (leer).
- **Test(s):** Baseline PASS: TestSystemWorkersPerOrganization, TestRLSCatalog, TestKnownGapsList, TestKnownGapsDocumented, TestRLSMatrix, TestMigrationsRoundtrip, TestWorkerMetricsAreRegisteredAndRecorded, TestSchedulerSingleRunnerAndCatchUp, TestLifecycleModelAndHealth; Probes TestAuditProbeAlertEvaluatorPerOrganization, TestAuditProbeWorkerFailuresCounted, TestVerifyB03SchedulerCountsSweeperFailure. Welle (Commit 0e02557): TestAuditT1AlertEvaluatorPerOrganization PASS (backend/internal/monitoring/audit_t1_alert_evaluator_integration_test.go: ListEnabled unter app.system, MarkFired je Org, feuert einmal, keine Worker-Fehler, app.system kann nicht schreiben).
- **Soll/Ist:** SOLL: Worker iterieren Orgs und schreiben je Org; app.system SELECT-only; Worker-Fehler sichtbar; Gaps entfernt. IST: alle 4 Kriterien ausgeführt belegt (committeter Worker-Test, Policy-Inspektion, Probes für Fehlermetriken); der Alert-Evaluator je Org ist jetzt durch einen committeten Regressionstest belegt. Rest: Webhook-Claiming-Schwäche (Codepfad, nicht reproduziert).
- **Evidenz:** psql audit_db_07: 11 app.system-Policies, alle cmd=SELECT; Probe-Ausgaben 'worker_errors_total{scheduler} before=0 after=1', beide Orgs notified; SP/work/t1-tests/untagged.log '--- PASS: TestAuditT1AlertEvaluatorPerOrganization' inkl. Subtest 'WithSystem_cannot_write'; mechanische Verifikation 'untagged backend/internal/monitoring (all) PASS'.
- **Abweichung:** P3 außerhalb der AK: Webhook-ClaimDue bricht bei Fehler einer Org ab, bereits geleaste Datensätze bleiben bis Lease-Ablauf liegen, keine faire Org-Reihenfolge (L-T1-22, nur Codepfad). Fehlermetrik-Kriterium nur per Probe belegt (kein Repo-Test).
- **Prüfebenen:** UX: N/A (keine UI). API: N/A (Worker). Daten: OK – System-Flag kann nicht schreiben; Schreiben je Org; Enrollment-Code höchstens einmal. E2E: LÜCKE – laufende Worker-Schleifen (Dispatcher, JobWorker, Evaluator-Ticker) nicht end-to-end gegen PG+NATS. Robustheit: LÜCKE – Webhook-Claim-Fehlerbehandlung (L-T1-22).
- **Abhängigkeiten:** WP-015, WP-016, WP-017, WP-041, WP-103 (Worker-Metriken), WP-131 (Scheduler).
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-184 (P3) Webhook-Retry verwirft bei Claim-Fehler einer Organisation bereits geleaste Datensätze; keine faire Org-Reihenfolge
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Fehlermetrik-Probes (Evaluator/Sweeper -> worker_errors_total +1) als Repo-Tests; Retest L-T1-22 per Integrationstest mit injiziertem Claim-Fehler einer Org.

<a id="wp-023"></a>
#### WP-023 – Client-Policies für neun Tabellen und NULL-Client-Schreibverbot (Querschnitt)

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** TEN-05, AST-01, MGT-11, MGT-03, WFL-01
- **Anforderung/Nutzerziel:** Tabellen mit client_id erzwingen den Client-Scope in USING und WITH CHECK; Principals ohne org-weiten Scope dürfen keine Zeilen mit client_id NULL schreiben (E-09).
- **Implementierung:** Commit bb6d660: migrations/000058_client_scope_policies.up.sql:17-53 (Schleife über 15 Tabellen), .down.sql; restriktive UPDATE/DELETE-Policies aus späteren Migrationen (z.B. asset_scoped_update/delete); tenant/rls/known_gaps.go (KnownGaps leer); docs/schema-baseline.md.
- **Test(s):** Baseline PASS: TestClientPoliciesEnforceClientScope (45 Subtests: 9 Tabellen × select/insert/update/delete), TestRLSMatrix (123 Einträge), TestRLSCatalog, TestKnownGapsList, TestKnownGapsDocumented, TestMigrationsRoundtrip. Verifier-pg_policies-Abfrage über 41 client_id-Tabellen. Real-Stack T07 PASS (B kann A's Location/Client/Parent nicht anhängen).
- **Soll/Ist:** SOLL: Client-Prädikate auf den 9 Tabellen, NULL-Client-Schreibverbot für gescopte Principals auf allen Client-Tabellen, Gaps entfernt, Tests je Tabelle. IST: erfüllt; belegt durch Integrations- und Matrixtests gegen PostgreSQL und Policy-Inspektion (0 permissive WITH CHECK akzeptieren NULL).
- **Evidenz:** SP/runs/results-backend.tsv (TestClientPoliciesEnforceClientScope 45 Subtests, TestRLSMatrix 123, TestRLSCatalog, TestMigrationsRoundtrip pass); psql audit_db_07 (41 client_id-Tabellen, perm_wc_null=0, restriktive update/delete auf allen nullable-client-Tabellen); SP/work/tenant-iso-final.log T07 ✓.
- **Abweichung:** -
- **Prüfebenen:** UX: N/A (Policy-WP; Probe: client-gescopter POST /assets ohne client_id -> sanitisiertes 403). API: N/A (keine Endpunktänderung). Daten: OK – DB-Erzwingung je Kommando und Scope inkl. NULL- und geteilter Zeilen. E2E: N/A (DB-WP; Real-Stack T07 bestätigt Wirkung für Org-Dimension). Robustheit: OK – Move-out und Claiming geteilter Zeilen abgewiesen; Minor: rejected()-Helfer akzeptiert jeden Fehler.
- **Abhängigkeiten:** WP-007, WP-008; WP-065 (Matrix).
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: 42501-Assertion im dedizierten Test; API-Verhalten client-gescopter Creates ohne client_id für /forms (siehe L-T1-05), /slas, /consumables.

<a id="wp-024"></a>
#### WP-024 – Globale Katalogzeilen schreibgeschützt; Tabellen ohne Org-Spalte klären

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** TEN-02, TEN-05
- **Anforderung/Nutzerziel:** Globale Katalogzeilen (organization_id NULL) sind lesbar, aber nicht durch die App-Rolle schreibbar; Tabellen ohne Org-Spalte sind nach E-10 klassifiziert.
- **Implementierung:** Commit 7e20577: migrations/000059_global_rows_readonly.up.sql:25-49 (Policies je Kommando für ci_type, lifecycle_definition/state/transition, relationship_type), :51-53 (REVOKE auf permission), :55-147 (organization_id per Trigger + Policies für attribute_definition, team_member, user_custom_role); tenant/rls/known_gaps.go GlobalCatalogTables=['permission'].
- **Test(s):** Baseline PASS: TestGlobalRowsAreReadOnly + 9 Subtests, TestRLSMatrix, TestRLSCatalog, TestMigrationsRoundtrip, TestRelationshipTypeRepositoryScope. Verifier-psql (Policies, has_table_privilege).
- **Soll/Ist:** SOLL: Policies je Kommando, globale Zeilen read-only, E-10-Klassifikation, Integrationstest. IST: erfüllt; PG-Integrations-, Katalog- und Matrixtests sowie Policy-/Rechte-Inspektion belegen alle Kriterien.
- **Evidenz:** SP/runs/results-backend.tsv (TestGlobalRowsAreReadOnly 9 Subtests pass); psql audit_db_07 (keine Schreib-Policy akzeptiert organization_id IS NULL; permission INSERT/UPDATE/DELETE f/f/f).
- **Abweichung:** -
- **Prüfebenen:** UX: N/A. API: LÜCKE – kein API-Test, dass PATCH/DELETE globaler CI-/Relationship-Typen 404/403 statt 500 liefert. Daten: OK – globale Zeilen unveränderlich, org-lose Kindtabellen gescopt. E2E: N/A (DB-WP). Robustheit: OK – Matrix + dedizierter Test; NULL-Org-INSERT für lifecycle_*/relationship_type nur über Matrix.
- **Abhängigkeiten:** WP-007; WP-065.
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: API-Test für globale Katalogeinträge; explizite NULL-Org-INSERT-Ablehnung für lifecycle_* und relationship_type.

<a id="wp-025"></a>
#### WP-025 – Client/Site in CI-abhängige Kindtabellen denormalisieren (Querschnitt TEN-02)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P0 · **Anforderungen:** TEN-02, TEN-05, IMP-07, SRC-01
- **Anforderung/Nutzerziel:** Kindzeilen eines CI (Interfaces, IPs, Beziehungen, Kontakte, Historie, Feldwerte, Findings, Discovery-Ergebnisse, Rack-Mounts, Review-Items, Metriken) tragen aus dem CI abgeleitete client_id/site_id; client-/site-gescopte Principals sehen/schreiben keine fremden Daten, die DB erzwingt das.
- **Implementierung:** backend/migrations/000061_denormalize_ci_scope.up.sql: Spalten :29-44, Ableitungstrigger :50-126, Propagation :131-173 (plpgsql unter RLS des Aufrufers), Backfill :179-190, Client-Policies :198-247; Site-Prädikat 000063:26-89; restriktive Schreib-Policies 000079. Nicht abgedeckt: review_item (keine client_id/site_id, Policy nur Org), Go-Prädikat reviews/pg_repository.go:29-32 (vakuum-wahr bei leeren candidate_ci_ids); metric_sample (Views nur Org, 000071). Relationship-Create relationship/pg_repository.go:188-217 -> 500.
- **Test(s):** Baseline PASS: TestCIChildTablesCarryClientScope, TestRLSCatalog, TestKnownGapsList, TestKnownGapsDocumented, 13 TestRLSMatrix-Subtests, TestChannelMatrix, TestIPAMRepositoryClientScope, TestMonitoringStoreScope; Probes probe_review_no_candidate.sql, probe_edge_stale_after_move.sql, probe_edge_stale_site.sql, TestB04VerifierProbe. Welle: TestAuditT1CIChildTableDerivation PASS (ci_change, ci_field_value, ci_instance_field_definition, security_finding, compliance_result, ci_contact: Ableitung, Spoof-Override, Fremd-Client-Ablehnung, Sichtbarkeit, Site-Folge); FAILS_AS_EXPECTED TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope, TestAuditT1ReviewItemRowsCarryClientScope, TestAuditT1EdgeScopeFollowsScopedMove, TestAuditT1MetricViewsHonourScope. Real-Stack L01, L06, L10 PASS (Ableitung/Propagation, org-weiter Nutzer).
- **Soll/Ist:** SOLL: alle CI-abhängigen Kindtabellen inkl. review_item und metric_sample tragen abgeleitete client_id/site_id, korrekt auch bei Verschiebungen, unter Client-/Site-Policies. IST: 12 Tabellen abgeleitet, backfilled und gepolicet; Insert-Ableitung für 6 weitere Kindtabellen jetzt per Regressionstest belegt; Real-Stack-Propagation bei Raum-Verschiebung (org-weit) korrekt. review_item und metric_sample ohne DB-Scope; Review-Items ohne Kandidaten leaken über die API (lesen und verwerfen); Kanten-Scope veraltet bei Verschiebung durch gescopten Principal.
- **Evidenz:** SP/verify-tests-run2/defect-TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope.log ('GET /review-items as client 1 discloses t1rev-c2-firewall of client 2'); defect-TestAuditT1ReviewItemRowsCarryClientScope.log ('selects 1 review_item rows about the client-2 CI, want 0'); defect-TestAuditT1EdgeScopeFollowsScopedMove.log ('edge source_client_id after the move = ...c1, want client 2'; 'observer clients 1+3 sees 1 edges naming the invisible CI X'); defect-TestAuditT1MetricViewsHonourScope.log; SP/work/t1-tests/untagged.log PASS TestAuditT1CIChildTableDerivation (6 Subtests); SP/work/locrack/run-final.log L01/L06/L10 ✓.
- **Abweichung:** AK1 teilweise: review_item und metric_sample nicht denormalisiert (L-T1-03 P1, L-T1-09 P2). AK2/AK3 teilweise: veralteter Kanten-Scope nach Verschiebung durch gescopten Principal (L-T1-08 P2). Beziehung zu sichtbarem org-weitem CI blockiert (L-T1-20 P3, PO-Klärung). POST /relationships mit Out-of-Scope-Endpunkt 500 (ext-Defekt T14).
- **Prüfebenen:** UX: N/A (Schema/RLS-WP). API: FEHLER – GET/resolve /review-items liefert/verwirft Client-2-Items; POST /relationships zu fremdem CI 500 (ext). Daten: FEHLER – review_item/metric_sample nur Org; veralteter Kanten-Scope. E2E: LÜCKE – Real-Stack L01/L06/L10 nur org-weiter Nutzer. Robustheit: LÜCKE – discovery_result, relationship_suppression, rack_mount nicht in der Ableitungs-Regressionstabelle; Backfill nur per Statement-Replay.
- **Abhängigkeiten:** WP-023, WP-010, WP-013, WP-027; reviews (WP-088/089, Gruppe T3); ADR 0001 (WP-039/040).
- **Defekte:** F-003 (P0) Review-Items ohne Kandidaten-CIs leaken über Clients; review_item ohne DB-seitigen Client/Site-Scope, F-050 (P2) Abgeleiteter Kanten-Scope veraltet, wenn ein client- oder site-gescopter Principal ein CI verschiebt, F-051 (P2) Metrik-Views nur org-begrenzt: metric_sample ohne Client-/Site-Scope auf DB-Ebene, F-145 (P2) POST /relationships mit CI einer anderen Organisation oder unbekannter CI-ID antwortet 500 statt 404/422, F-182 (P3) Client-gescopter Principal kann keine Beziehung zu einem sichtbaren org-weiten (client NULL) CI anlegen
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope, TestAuditT1ReviewItemRowsCarryClientScope, TestAuditT1EdgeScopeFollowsScopedMove, TestAuditT1MetricViewsHonourScope grün; TestAuditT1CIChildTableDerivation um discovery_result (matched/unmatched -> Collector-Client), relationship_suppression, rack_mount erweitert und grün; Backfill-Migrationstest 000061; PO-Entscheidung zu L-T1-20.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Folgt aus der Korrektur von L-T1-03 auf P0.

<a id="wp-026"></a>
#### WP-026 – Kanonischer Location-Baum: Tabelle location mit ltree, Elternmatrix, Zyklenschutz

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** LOC-10, TEN-02, TEC-06
- **Anforderung/Nutzerziel:** Ein kanonischer Standortbaum (site > building > room > rack; site > warehouse > zone > shelf > bin) mit ltree-Pfad; DB erzwingt Elternmatrix und Zyklenschutz, site/client werden aus dem Baum abgeleitet und per RLS geschützt.
- **Implementierung:** backend/migrations/000062_location_tree.up.sql: ltree :19, Tabelle :23-35, Elternmatrix :50-61, location_maintain :69-135 (Zyklus :121-124, Matrix :125-128, Ableitung :130-132), Propagation :142-153, Spezialistentabellen :160-215,337-339, Backfill :219-226/:233-272, 1:1-FKs :274-281, RLS ENABLE/FORCE :317-334; backend/internal/locations/{model.go,pg_repository.go}.
- **Test(s):** Baseline PASS: TestLocationTreeMatrixAndDerivation, TestLocationTreeMoveAndCycle, TestLocationTreeScope, TestLocationTreeUpdateAndDelete, TestMigration74MovesReferencesToLocation, TestRLSMatrix/location, TestRLSCatalog; Probe probe_location_backfill.sql (4/4 identisch). Real-Stack PASS: locations-racks.spec.ts L01 (UI-Baum site->building->room->rack, abgeleitete client/site/path, DB-Zeilen), L03 (Zyklus/Matrix 422 auf parent_id, nichts geändert), L10 (Raum-Verschiebung re-derived, location_change + Audit); tenant-isolation.spec.ts T01, T07.
- **Soll/Ist:** SOLL: kanonischer ltree-Baum mit DB-Matrix, Zyklenschutz, abgeleitetem Scope, 1:1-Spezialistentabellen mit Backfill, vollständiger RLS. IST: wie spezifiziert umgesetzt; alle 5 Kriterien ausgeführt belegt, zusätzlich UI->API->DB->UI über den Real-Stack.
- **Evidenz:** SP/runs/results-backend.tsv (pass); probe_location_backfill 'rows identical after backfill|4'; probe_location_orgwide 'a site needs a client'; SP/work/locrack/run-final.log L01/L03/L10 ✓; SP/work/tenant-iso-final.log T01/T07 ✓.
- **Abweichung:** -
- **Prüfebenen:** UX: OK – Real-Stack L01/L03 (Baum-UI, Dialoge bieten nur zulässige Kinder). API: OK – Triggerfehler -> ErrInvalidParent/ErrCycle/422 auf parent_id. Daten: OK – Ableitung, Propagation, Kaskade inkl. Spezialistenzeilen. E2E: OK – L01/L03/L10 UI->API->DB->UI. Robustheit: OK – Spoofing, fremde Eltern, Zyklus, Matrix, Org-Isolation; LÜCKE: nebenläufiges Umhängen und site-gescopte Writes ungetestet.
- **Abhängigkeiten:** WP-023; Basis für WP-027, WP-029, WP-032, WP-053/074.
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: 61->62-Backfill-Migrationstest; Nebenläufigkeitstest Advisory-Lock; site-gescopte Schreibablehnung.

<a id="wp-027"></a>
#### WP-027 – Site-Scope in allen Policies mit site_id (CH25)

- **Status:** BESTANDEN · **Priorität:** P2 · **Anforderungen:** TEN-05, TEN-04, TEC-12
- **Anforderung/Nutzerziel:** Ein auf Sites beschränkter Principal sieht/schreibt in jeder Tabelle mit eigener oder denormalisierter site_id nur Zeilen seiner Sites; die DB erzwingt das (CH25) mit E-09-NULL-Semantik.
- **Implementierung:** backend/migrations/000063_site_scope_policies.up.sql:26-89; location 000062:325-333; weitere Tabellen 000064/065/067/068-070/074; restriktive Schreib-Policies 000079; known_gaps.go:120 leer; RuleSiteScope catalog_integration_test.go:291-292.
- **Test(s):** Baseline PASS: TestSitePoliciesRestrictSiteScopedPrincipals, TestRLSCatalog, TestKnownGapsList, TestRLSMatrix, TestChannelMatrix; Verifier-Probes probe_site_policy_catalog.sql (34 Tabellen, USING/WITH CHECK je Policy), probe_site_scope_nosite_tables.sql. Welle: TestAuditT1SiteScopeOfLocatedTables FAILS_AS_EXPECTED (außerhalb der AK).
- **Soll/Ist:** SOLL: Site-Prädikat in USING und WITH CHECK aller Tabellen mit site_id, E-09-Semantik, Katalogprüfung. IST: erfüllt (alle Datentabellen mit *site_id: using_without_site=0, check_without_site=0). Außerhalb der AK: asset/room/rack (und cable) haben einen Standortbezug, aber keine site_id; site-gescopte Principals sehen sie DB-seitig und Assets auch über die API in allen Sites (TEN-02-Lücke ohne Eigentümer-WP).
- **Evidenz:** probe_site_policy_catalog.out (34 Zeilen, 0/0 für alle Datentabellen; Ausnahmen role_assignment/service_account_role = Grant-Metadaten); SP/verify-tests-run2/defect-TestAuditT1SiteScopeOfLocatedTables.log ('GET /assets as site1 lists the asset located in site 2'; 'site-1 principal selects the room/rack/asset of site 2 (1 rows), want 0').
- **Abweichung:** Kein AK verletzt. Außerhalb der AK: bestätigter P2-Defekt L-T1-11 (asset/room/rack ohne site_id; TEN-02/CH25), hier zur Nachverfolgung zugeordnet, da kein WP des Plans ihn abdeckt. Priorität folgt der Regel 'höchste Priorität der Abweichungen'.
- **Prüfebenen:** UX: N/A (Policy-WP). API: OK – TestChannelMatrix Site-Principal über REST/GraphQL/Suche/Export/Topologie; außerhalb AK: GET /assets site-übergreifend. Daten: OK für alle Tabellen mit site_id; LÜCKE außerhalb AK (asset/room/rack ohne site_id). E2E: N/A (keine UI im WP). Robustheit: OK – Out-of-Scope-Inserts, Moves, NULL-Site-Writes abgewiesen.
- **Abhängigkeiten:** WP-025, WP-026; WP-028/029/032; Eigentümer-WP bzw. PO-Entscheidung für L-T1-11 offen (WP-074 ergänzte asset.location_id ohne site_id).
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-053 (P2) Assets, Rooms und Racks haben einen Standortbezug, aber keine site_id; site-gescopte Principals sehen sie in allen Sites
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN (AK erfüllt). Für L-T1-11: TestAuditT1SiteScopeOfLocatedTables grün oder dokumentierte PO-Entscheidung 'Assets site-los'; Eigentümer-WP festlegen.

<a id="wp-028"></a>
#### WP-028 – Modultabellen mit Objektbezug erben Bereichsrechte (Dokumente, Tickets, Wartung, Schlüssel)

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** MGT-02, MGT-01, TKT-01, MGT-04, MGT-03
- **Anforderung/Nutzerziel:** Dokumente, Tickets, Ticket-Kommentare, Wartungsfenster und Schlüsselzuweisungen erben client/site des referenzierten CI/Assets/Tickets/Schlüssels; gescopte Principals sehen/ändern keine Moduldaten fremder Clients/Sites.
- **Implementierung:** backend/migrations/000064_module_object_scope.up.sql: abgeleitete Spalten :27-33, Trigger :37-102, Propagation :106-156, Zähler link_count/ci_count (SECURITY INVOKER) :160-193, Eltern-Guard :198-220, document/maintenance_window-Policies 'count = 0 OR EXISTS sichtbarer Link' mit WITH CHECK nur Org :275-287; restriktive *_scoped_update/delete nur Org (000079). Go: document/pg_repository.go:80-81 documentVisible, :326-337 Delete; maintenance/pg_repository.go:41-43. disposal_record unverändert (nur Org).
- **Test(s):** Baseline PASS: TestModuleTablesInheritObjectScope, 7 TestRLSMatrix-Subtests, TestRLSCatalog, TestTicketRepositoryClientScope, TestDocumentRepositoryClientScope, TestMaintenanceRepositoryClientScope, TestKeyRepositoryClientScope, TestDisposalRepositoryClientScope; Probes probe_doc_shared_delete.sql, probe_window_shared_delete.sql, probe_doc_link_count.sql, TestB04VerifierProbe P4. Welle: TestAuditT1DocumentWritesOutsideScope und TestAuditT1MaintenanceWindowWritesOutsideScope FAILS_AS_EXPECTED.
- **Soll/Ist:** SOLL: Modulzeilen erben client/site vom referenzierten Objekt; Schreibzugriffe außerhalb des Scopes werden abgewiesen. IST: Vererbung für Links/Tickets/Kommentare/Fenster-CIs/Schlüsselzuweisungen funktioniert. document/maintenance_window nutzen ein 'irgendein Link sichtbar'-Modell: über die echte API löscht ein teilgescopter Principal geteilte Dokumente/Fenster (Links anderer Clients kaskadieren weg), legt org-weite Dokumente/Fenster an, ändert und kapert sie; Zähler driften und sind manipulierbar; disposal_record bleibt org-only.
- **Evidenz:** SP/verify-tests-run2/defect-TestAuditT1DocumentWritesOutsideScope.log ('PATCH as client 1: 200', 'DELETE as client 1: 204', 'client-2 asset links after DELETE by client 1: 0, want 1'); defect-TestAuditT1MaintenanceWindowWritesOutsideScope.log ('client-2 link rows after DELETE by client 1: 0, want 1 (FK cascade removed another client's data)', 'POST an org-wide window (no CIs) as client 1: 201'); probe_doc_link_count.out ('A2 ... |0|2', 'B1 client-3 principal (no link) sees the c1+c2 document after tampering|1').
- **Abweichung:** AK2 verletzt (L-T1-04 P1, L-T1-06 P2). AK1 teilweise (document/maintenance_window ohne client_id/site_id, Zähler driften/manipulierbar: L-T1-24 P3; key_assignment nur Client). MGT-02 (disposal_record) laut Traceability geschlossen, aber DB-seitig nur Org (L-T1-25 P3).
- **Prüfebenen:** UX: N/A in G1 (Module per WP-214 abgeschaltet). API: FEHLER – Client-1 löscht geteiltes Dokument (204), ändert/kapert org-weite Dokumente. Daten: FEHLER – Kaskaden-Löschung fremder Links; Zähler driften/schreibbar. E2E: N/A in G1. Robustheit: LÜCKE – Site-Scope und Propagation auf Modultabellen ungetestet.
- **Abhängigkeiten:** WP-027, WP-019, WP-020; WP-214 (Modul-Gates begrenzen G1-Exposition); E-03 (M0-Scope-Fixes verbindlich).
- **Defekte:** F-004 (P0) Geteilte Dokumente und Wartungsfenster: teilgescopter Principal ändert/löscht sie, FK-Kaskade entfernt Links anderer Clients, F-048 (P2) Unverlinkte (org-weite) Dokumente und Wartungsfenster durch client-gescopte Principals anleg-, änder-, lösch- und kaperbar (E-09), F-185 (P3) link_count/ci_count driften unter RLS und sind direkt schreibbar; Dokumente können für alle unsichtbar oder fremden Clients sichtbar werden, F-186 (P3) Traceability meldet MGT-02 (Disposal) durch WP-028 geschlossen, disposal_record behält aber eine reine Org-Policy
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final FEHLGESCHLAGEN; TEILWEISE BESTANDEN -> FEHLGESCHLAGEN: AK2 'Schreibzugriffe auf Objekte außerhalb des Scopes werden abgewiesen' ist durch die bestätigten Defekte L-T1-04 (P1) und L-T1-06 (P2) beobachtet verletzt (Kriteriumsurteil des Verifiers 'no'; Defekttests TestAuditT1DocumentWritesOutsideScope und TestAuditT1MaintenanceWindowWritesOutsideScope FAILS_AS_EXPECTED über den vollständigen PG-Router). Statusregel ⇒ FEHLGESCHLAGEN.
- **Offene Nachweise / Retest:** Beide Defekttests grün inkl. Positivkontrollen; Zählerintegrität (Entfernen eigener Links -> link_count exakt, direktes UPDATE link_count abgewiesen; probe_doc_link_count.sql); disposal_record DB-gescopt oder Traceability korrigiert; Site-Scope- und Propagationstests für Modultabellen.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Folgt aus der Korrektur von L-T1-04 auf P0.

<a id="wp-029"></a>
#### WP-029 – Team-, Raum- und Personenbezug: Desks, Buchungen, Trainings, Ticket-Teams

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** MGT-05, MGT-06, MGT-07, TKT-01, TEN-05
- **Anforderung/Nutzerziel:** Team-gescopte Principals sehen/schreiben nur Tickets ihrer Teams und Trainingszuweisungen ihrer Teammitglieder, geschnitten mit Client/Site (E-11); Desks/Buchungen erben client/site vom Raum; die DB verhindert überlappende Buchungen auch nebenläufig.
- **Implementierung:** backend/migrations/000065_team_scope_policies.up.sql: Ticket-Policy client+site+team :23-42; desk/desk_booking aus Raum-Location :46-131; training_assignment über team_member EXISTS :135-146; Überlappungsbereinigung :150-171; EXCLUDE desk_booking_no_overlap :173-175; desk/pg_repository.go:222-241 (23P01 -> ErrAlreadyBooked), desk/handler.go:184-188 (409).
- **Test(s):** Baseline PASS: TestTeamRoomAndPersonScope (15 Lese-, 9 Schreibfälle), TestConcurrentBookingsDoNotOverlap (8 Goroutinen -> 1 gebucht/7 ErrAlreadyBooked), TestDeskRepositoryClientScope, TestTrainingRepositoryScope, TestTicketRepositoryClientScope, TestRLSCatalog, TestRLSMatrix/{ticket,desk,desk_booking,training_assignment}; Probe probe_ticket_comment_team.sql.
- **Soll/Ist:** SOLL: Team-Scope geschnitten mit Client/Site für Tickets, Trainingszuweisungen und Desks/Buchungen; DB-seitiger Überlappungsschutz; keine Katalog-Ausnahmen. IST: erfüllt und durch PG-Integrationstests inkl. echter Nebenläufigkeit belegt. Produktiv wirkungslos, solange Team-Scopes nicht zugewiesen werden können (L-T1-07, WP-009) – das betrifft kein AK dieses WPs. ticket_comment ohne DB-Team-Prädikat (außerhalb AK, Go-Pfad commentVisible schützt).
- **Evidenz:** SP/runs/results-backend.tsv (pass); pg_policies (Ticket-Team-Prädikat, training_assignment EXISTS team_member); probe_ticket_comment_team.out ('comments of team2 ticket (DB policy only)|1', 'team2 ticket|0').
- **Abweichung:** -
- **Prüfebenen:** UX: N/A in G1 (Module per WP-214 aus). API: OK – ErrAlreadyBooked -> 409; LÜCKE – kein HTTP-Test. Daten: OK – Ableitung aus Raum, Propagation, Exclusion-Constraint; außerhalb AK ticket_comment ohne DB-Team-Prädikat. E2E: N/A in G1. Robustheit: OK – 8 nebenläufige Buchungen; LÜCKE – Migrationsbereinigung bestehender Überlappungen ungetestet.
- **Abhängigkeiten:** WP-028, WP-026, E-11 (vorläufig); Wirksamkeit hängt an L-T1-07 (Team-Zuweisung, WP-009).
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Bei Änderung von E-11 neu bewerten; ticket_comment-DB-Team-Prädikat als Defense-in-Depth.

<a id="wp-030"></a>
#### WP-030 – Dokument-Blobs: serverseitige Storage-Keys mit Org-Präfix

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** MGT-01, TEN-06
- **Anforderung/Nutzerziel:** Ein Dokument referenziert nur Blobs der eigenen Organisation; Storage-Keys werden serverseitig unter org/<org_id>/ erzeugt; Downloads prüfen Präfix und Dokumentzugehörigkeit; Altdaten werden per DB-Constraint geprüft.
- **Implementierung:** backend/internal/platform/blob/blob.go:34-57 (OrgKey, CheckOrgKey); document/handler.go:168-176 (Request-Key ignoriert), :282, :310-323, :399-405 ownsStorageKey; pg_repository.go:206, :305; migrations/000066_document_storage_key_check.up.sql:10-27 (Bereinigung + NOTICE-Anzahl), :29-34 CHECK. Frontend verlangt weiterhin storage_key: frontend/src/pages/DocumentListPage.tsx:47,151-153; api/client.ts:948.
- **Test(s):** Baseline PASS: TestCheckOrgKey, TestOwnsStorageKey, TestCreateIgnoresRequestedStorageKey, TestStorageKeyRejectedOutsideOrganization (PG), TestDocumentRepositoryClientScope, TestUploadAndDownloadContent; Verifier-HTTP+PG-Probe TestB04BlobProbe (B1–B4); Probe probe_storage_key_cleanup.sql. Keine Wellen-Tests.
- **Soll/Ist:** SOLL: nur serverseitige Org-Präfix-Keys, Präfix- und Zugehörigkeitsprüfung, CHECK-Constraint, Altdaten geprüft und gemeldet. IST: Sicherheitskern umgesetzt und über den PG-HTTP-Pfad mit gescopten Principals belegt (B2 404, B3 200, B4 409). Altdaten-Report nur als Anzahl, Schlüssel irreversibel geleert; UI verlangt einen toten Storage-Key.
- **Evidenz:** SP/runs/results-backend.tsv (pass); probe_blob_http.out (B1 'key-prefix-ok=true', B2 'client1 download of client-2 document: 404', B4 '409'); probe_storage_key_cleanup ('NOTICE: cleared 1 document storage keys outside the organization prefix').
- **Abweichung:** AK3 teilweise: abweichende Keys nur als Anzahl gemeldet und irreversibel geleert (L-T1-26, P3). UI verlangt Storage-Key, kein Upload/Download (L-T1-27, P3).
- **Prüfebenen:** UX: LÜCKE – DocumentListPage verlangt ignoriertes Storage-Key-Feld, kein Upload/Download (Modul in G1 aus). API: OK – Key ignoriert, serverseitig erzeugt, Fremd-Download 404, falscher Key 409. Daten: OK – CHECK für jeden Schreiber; LÜCKE – Altdatenbericht. E2E: N/A in G1. Robustheit: OK – Traversal, leere Segmente, Backslash, fremde Org abgewiesen.
- **Abhängigkeiten:** WP-019, WP-028, WP-214 (Modul-Gate).
- **Defekte:** F-187 (P3) Migration 000066 meldet nicht konforme Storage-Keys nur als Anzahl und leert sie irreversibel, F-188 (P3) Dokument-Formular verlangt weiterhin einen 'Storage-Key', den die API ignoriert; kein Upload/Download in der UI
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Altdaten-Bereinigung meldet betroffene Dokumente einzeln (Migrationstest); PG-gestützter gescopter Download-Test committed; Frontend ohne storage_key (Vitest).

<a id="wp-031"></a>
#### WP-031 – Topologie-Traversal mit Sichtbarkeit im CTE

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** IMP-07, CI-05
- **Anforderung/Nutzerziel:** Topologie- und Impact-Traversals laufen nur über sichtbare, nicht gelöschte CIs; unsichtbare/gelöschte Zwischenknoten verbinden nie sichtbare Knoten; PG- und Memory-Implementierung teilen ein Knotenlimit.
- **Implementierung:** backend/internal/relationship/pg_repository.go:368-462 TraverseFrom (ebenenweises BFS unter RLS; Wurzelprüfung :375-381; visibleCIs :523-541; organizationEdges :485-503); relationship.go:376 MaxTraversalNodes, genutzt in topology.go:279/:352 und impact.go; Fehlerpfad topology.go:273-276 -> GetTopology :133-140 WriteRepoError -> 500.
- **Test(s):** Baseline PASS: TestTraversalStaysInsideScope (+ Subtests levels, organization_read), TestChannelMatrix (nur sichtbare Wurzeln). Verifier-HTTP-Probe TestB04VerifierProbe P2. Welle: TestAuditT1TopologyChannelInvisibleRoots PASS (neighbors/impact unsichtbarer Wurzeln 404); TestAuditT1ScopeRefusalsAreNot5xx FAILS_AS_EXPECTED (Topologie-Teil). Real-Stack T03, T06, L11 PASS; T15 FAIL.
- **Soll/Ist:** SOLL: Sichtbarkeit während des Walks, eine Knotenlimit-Konstante, Test für unsichtbare Zwischenknoten. IST: erfüllt (BFS statt rekursiver CTE, verhaltensgleich), belegt durch PG-Integrationstest in beiden Lesemodi, HTTP-Kanalmatrix und Real-Stack (T06: keine Nachbarn/Impact fremder CIs; L11: Impact überspringt gelöschtes CI). Außerhalb der AK: Topologie mit Out-of-Scope-/unbekannter Wurzel liefert 500 statt 404.
- **Evidenz:** SP/runs/results-backend.tsv (pass); probe_http.out ('P2 client1 GET topology root=foreign(client2): 500', 'P2 control ... root=own: 200'); SP/work/tenant-iso-final.log T15 ✘ ('GET /topology?root_ci_id={A} as B -> 500'), T03/T06 ✓; SP/work/ci-lifecycle/final.log L11 ✓.
- **Abweichung:** AK-Wortlaut 'Rekursive CTE': umgesetzt als RLS-gescoptes ebenenweises BFS (dokumentiert pg_repository.go:354-367), verhaltensgleich. Außerhalb der AK: P3-Defekt L-T1-17 (500 statt 404 für Out-of-Scope-Wurzel; eigener Real-Stack-Defekt T15). Priorität gegenüber dem verifizierten Urteil von '-' auf P3 korrigiert (Regel: höchste Priorität der Abweichungen).
- **Prüfebenen:** UX: N/A (Backend-Traversal; Topologie-UI Real-Stack T10 nur Org-Dimension). API: OK für sichtbare Wurzeln; FEHLER außerhalb AK – Out-of-Scope-Wurzel 500. Daten: OK – soft-gelöschte CIs ausgeschlossen. E2E: OK/LÜCKE – Real-Stack T06/L11 grün, T15 rot (500); kein gescopter UI-Lauf. Robustheit: OK – Zyklen, Knotenlimit, Bulk-Fallback; LÜCKE – unsichtbare/unbekannte Wurzel.
- **Abhängigkeiten:** WP-025 (Kanten-Scope, Zwei-Endpunkt-Policy); WP-206/WP-335 (CTE ersetzt); WP-163 (API-03, Gruppe T2).
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-161 (P2) GET /topology mit unbekannter oder fremder root_ci_id antwortet 500 statt 404
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. L-T1-17: Real-Stack T15 und Topologie-Assertions von TestAuditT1ScopeRefusalsAreNot5xx grün; e2e-real gescopte Topologie (Pfad über Client-2-CI ausgeblendet).

<a id="wp-032"></a>
#### WP-032 – PG-Volltextsuche mit Client-/Site-Scope und Treffer-Leserecht

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** SRC-01
- **Anforderung/Nutzerziel:** Die Suche liefert nur Treffer, die der Aufrufer sehen darf: search_document trägt abgeleitete client/site seiner Entität unter RLS, und jeder Treffer wird gegen das Leserecht seines Entitätstyps geprüft.
- **Implementierung:** backend/migrations/000067_search_document_scope.up.sql (Spalten, derive_search_document_scope, Propagation, Backfill, Client+Site-Policy), aktualisiert durch 000074 (liest location); search/pg_repository.go:91-99 hitVisible; handler.go:75-96 filterAllowed (fail-closed); model.go:68-77 ReadPermission.
- **Test(s):** Baseline PASS: TestSearchHitsRespectClientSiteAndReadPermission, TestLocationsAndContactsAreHitsAgainstPostgres, TestSearchRepositoryClientScope, TestRLSMatrix/search_document, TestChannelMatrix (Suchkanal je Scope-Art). Real-Stack PASS: tenant-isolation.spec.ts T03 (/search ohne A-Marker), T10 (Ctrl+K-Palette: B 0 Optionen), ci-lifecycle.spec.ts L09 (Listenfilter, /search, Palette; nach Umbenennung nur neuer Name).
- **Soll/Ist:** SOLL: Indexzeilen mit abgeleitetem client/site unter RLS, Leserecht je Treffer, Integrationsnachweis. IST: erfüllt; PG-Integrationstest, HTTP-Kanalmatrix und Real-Stack-Suche (Org-Dimension) belegen alle Kriterien. Site-gescopte Principals finden Assets aller Sites (Schema-Lücke L-T1-11, außerhalb dieses WPs).
- **Evidenz:** SP/runs/results-backend.tsv (pass); psql derive_search_document_scope liest location, 6 Propagationstrigger; SP/work/tenant-iso-final.log T03/T10 ✓; SP/work/ci-lifecycle/final.log L09 ✓.
- **Abweichung:** -
- **Prüfebenen:** UX: OK – Real-Stack L09/T10 (Suche, Palette). API: OK – HTTP-Suche je Scope-Art, Rechtefilter fail-closed. Daten: OK für ci/asset; LÜCKE – Ableitung für ticket/location/contact/reservation nicht asserted. E2E: OK – Real-Stack L09/T03/T10 (Org-Dimension); kein client-gescopter UI-Lauf. Robustheit: OK – veraltete Indexzeilen per hitVisible gefiltert, ohne User keine Treffer.
- **Abhängigkeiten:** WP-027, WP-015; WP-074; L-T1-11 (Assets ohne site_id) wirkt auf Site-gescopte Asset-Treffer.
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Ableitungs-/Propagationstests für ticket/location/contact/reservation; e2e-real client-gescopte Suche.

<a id="wp-033"></a>
#### WP-033 – OpenSearch-Pfad: Client-/Site-Filter und Feldrechte

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** TEC-12
- **Anforderung/Nutzerziel:** Nutzer einer Organisation mit optionalem OpenSearch-Backend erhalten Treffer nur innerhalb ihres aktuellen Client-/Site-Scopes; der Scope stammt aus dem serverseitigen Principal (TEC-12).
- **Implementierung:** Commit ea0f7ce: backend/internal/search/opensearch.go:157-210 buildOpenSearchQuery, :213-281 Query, :284-321 visibleTo; ci_indexer.go:59-71; ci/service.go:155-165; hybrid.go:25-36, :49-77 ReindexTenant; Remote-Schreiber nur ci.NewIndexingRepository (cmd/server/main.go:327; ci/indexing_repository.go:71-78) und Reindex; nicht reindiziert: rack/placement.go:59 (UPDATE ci SET location_id) und PG-Propagation 000067. k8s-Prod-Overlay aktiviert OpenSearch.
- **Test(s):** Baseline PASS: TestOpenSearchQueryFiltersOnScope, TestOpenSearchDropsHitsOutsideScope, TestCIIndexerCarriesScope, TestHybridIndexesRemoteAfterCommit (httptest-Fakes). Probes TestProbeB05HybridReindexCarriesScope, TestProbeV05OpenSearchStaleSiteAfterRackMount, TestProbeV05OpenSearchStaleSiteAfterLocationMove. Welle: TestAuditT1OpenSearchFollowsRackPlacement FAILS_AS_EXPECTED (backend/internal/search/audit_t1_opensearch_scope_auditdefect_test.go).
- **Soll/Ist:** SOLL: jede OpenSearch-Abfrage liefert nur Treffer im aktuellen Client-/Site-Scope. IST: Filter korrekt für den im Dokument gespeicherten Scope; dieser veraltet, wenn ein CI ohne CI-Repository-Schreibvorgang die Site wechselt (Rack-Mount). Site-beschränkte Nutzer finden dann weiterhin weggezogene CIs.
- **Evidenz:** Probe-Log audit_db_05 ('remote doc after move ...: site=<A>', 'OPENSEARCH site-A principal sees ... pv5-secret-ci'); SP/verify-tests-run2/defect-TestAuditT1OpenSearchFollowsRackPlacement.log ('remote document of the moved CI keeps site 85f6acfe-..., want site B 3a2cf2ba-...').
- **Abweichung:** AK1 teilweise: Site-Scope der Remote-Dokumente veraltet nach Rack-Platzierung (L-T1-12, P2). Feldrechte (RBA-07) nicht im AK. Echte OpenSearch-Semantik unverifiziert (Netzwerkpolicy).
- **Prüfebenen:** UX: N/A (keine OpenSearch-spezifische UI). API: OK – fail-closed ohne Scope/bei Org-Mismatch (Unit). Daten: FEHLER – client/site der Remote-Dokumente nicht nachgeführt. E2E: LÜCKE – kein echtes OpenSearch, kein Router-Test mit RETICORA_SEARCH_BACKEND=opensearch. Robustheit: LÜCKE – visibleTo prüft nur veraltete Dokumentfelder.
- **Abhängigkeiten:** WP-032, E-03, E-09, Rack-Platzierung (RCK-02).
- **Defekte:** F-054 (P2) OpenSearch-Dokumente behalten nach Rack-Platzierung eine veraltete Site; site-beschränkte Nutzer finden weggezogene CIs
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT1OpenSearchFollowsRackPlacement grün; zusätzlich UnmountCI und Client-Wechsel über den CI-Service gegen den Remote-Index (nicht kodiert); Hybrid-Reindex-Probe als Repo-Test; idealerweise CI-Integrationstest gegen echtes OpenSearch.

<a id="wp-034"></a>
#### WP-034 – Strukturierte Suche und Beziehungssuche: Leserecht, Scope, Tiefe 5

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** SRC-03, SRC-04
- **Anforderung/Nutzerziel:** Nutzer führen strukturierte Filterabfragen und Saved Views inkl. Upstream-/Downstream-Walks aus; Ergebnisse respektieren das Leserecht des Entitätstyps und den vollen Scope; Walks enden nach 5 Hops.
- **Implementierung:** Commit 168879e: savedview/handler.go:206-215, :233-247 (readPermissions, mayRead fail-closed); filter.go:123-152, :188-189 (MaxRelationshipDepth=5); query.go:28-52 (WithRequestTenant); Route POST /api/v1/search/query über server/authz.go:170 auf search:write gemappt; frontend/src/pages/SavedViewsPage.tsx:140-145 (ErrorState mit No-op-Retry).
- **Test(s):** Baseline PASS: TestStructuredSearchScopeDepthAndPermission (PG), TestCompileSQLRelationshipPredicates, TestSavedViewScope, TestEveryOperationRequiresItsExpectedPermission (pinnt search:write, route_permissions.txt:458); Vitest SavedViewsPage.test.tsx 4 PASS (gemockt). Probe TestProbeV05SearchQueryByRole. Welle: TestAuditT1StructuredSearchForReadRoles FAILS_AS_EXPECTED. Real-Stack bulk-jobs-export.spec.ts V01 PASS (Saved View mit Berechtigtem, Persistenz, Tenant-Privatheit, 422 für ungültigen AST).
- **Soll/Ist:** SOLL: Entity-Leserecht unter vollem Scope, Walks höchstens 5 Hops. IST: Tiefenbegrenzung und Client-Scope-Walk auf PG belegt, Handler prüft Leserecht, Saved-View-Fluss real belegt (V01). Die Route verlangt jedoch search:write, sodass Leserollen (viewer, client_technician) 403 erhalten; Site-Scope und Asset-Kind ungetestet.
- **Evidenz:** Probe-Log ('role=viewer POST /api/v1/search/query -> 403 missing required permission: search:write'); SP/verify-tests-run2/defect-TestAuditT1StructuredSearchForReadRoles.log ('viewer (holds ci:read) POST /search/query {entity_kind: ci}: 403 ... want 200'); SP/work/bulk-jobs-export/final.log V01 ✓.
- **Abweichung:** AK1 teilweise: Routenrecht search:write blockiert Leserollen (L-T1-13, P2); Site-/Asset-Scope ungetestet; UI-Retry No-op (Teil von L-T1-13).
- **Prüfebenen:** UX: LÜCKE – ErrorState ohne Rechtehinweis, Retry No-op. API: FEHLER – POST /search/query 403 für Leserollen. Daten: OK – WithRequestTenant, RLS, gelöschte CIs ausgeschlossen. E2E: LÜCKE – Real-Stack V01 nur mit berechtigtem Nutzer; kein Lauf durch Viewer/client-gescopten Nutzer. Robustheit: OK – Tiefenlimit mit Zyklusschutz, unbekannter Typ 400.
- **Abhängigkeiten:** WP-027, WP-015, WP-187, WP-045.
- **Defekte:** F-055 (P2) Strukturierte Suche (POST /api/v1/search/query) verlangt search:write; Leserollen erhalten 403, Saved Views nicht ausführbar
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT1StructuredSearchForReadRoles grün (viewer/client_technician 200, asset ohne asset:read 403, reindex weiter search:write); Site-Scope- und Asset-Kind-Integrationstest; Real-Stack-Saved-View durch Viewer.

<a id="wp-035"></a>
#### WP-035 – Export-Jobs mit Ersteller-Scope ausführen und abrufen

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** EXP-01, TEN-06
- **Anforderung/Nutzerziel:** Ein Nutzer startet einen asynchronen Export; der Worker rendert mit exakt dem Client/Site/Team-Scope des Erstellers, und nur der Ersteller sieht Job und Download-Link; ein client-gescopter Nutzer exportiert nie fremde Clients.
- **Implementierung:** Commit 8f790b0: Migration 000068_export_job_scope; export/job.go:54-76 SnapshotScope; job_handler.go:57-104 (CreateJob, :98 Tracker.Track), :108-149; pg_job_repository.go:24 ownJob; worker.go:137-145 (Rendering im Snapshot), :79-82 (Mirror in generische Jobs); jobs/handler.go:53-57 (/api/v1/jobs, nur Org-Filter); server/router.go:359.
- **Test(s):** Baseline PASS: TestExportJobRunsWithCreatorScope (PG, Worker, FileStore), TestListJobsTenantIsolation, TestGetJobNotFoundOtherTenant, TestJobLifecycleCompletesWithSignedURL, TestExpiredJobHidesDownloadURL, TestChannelMatrix (Export); Vitest ExportPage 2 PASS (gemockt); onboarding.spec.ts 6 PASS (Real-Stack). Probe TestProbeV05ExportJobsVisibleInGenericJobs. Real-Stack PASS: ci-lifecycle.spec.ts L10, bulk-jobs-export.spec.ts E01 (UI-Export, Datei = API-Daten des eigenen Tenants, Tenant B 404), tenant-isolation.spec.ts T03/T08.
- **Soll/Ist:** SOLL: Export mit Ersteller-Snapshot, Export-Job nur für Ersteller sichtbar. IST: Snapshot-Rendering auf PG belegt; Export-Routen nur für Ersteller; UI->API->DB-Export real belegt (E01/L10, Org-Dimension). Die generische Job-API spiegelt den Job mit reiner Org-Sichtbarkeit: andere Nutzer/Clients mit job:read sehen Existenz, Ersteller und Status (kein Inhalt, kein Link).
- **Evidenz:** Probe-Log audit_db_05 ('B (client-2) GET /export/jobs -> total 0', 'B (client-2) GET /jobs -> 200 ... kind export ... created_by ...', '/jobs/{id}/result -> 409'); SP/work/bulk-jobs-export/final.log E01 ✓; SP/work/ci-lifecycle/final.log L10 ✓.
- **Abweichung:** AK2 teilweise: Export-Job-Metadaten über /api/v1/jobs und Jobcenter für Nicht-Ersteller sichtbar (L-T1-23, P3). Site-/Team-Snapshot ungetestet.
- **Prüfebenen:** UX: OK/LÜCKE – Real-Stack E01/L10 (UI-Export, Download) grün; Jobcenter listet fremde Export-Jobs (Metadaten). API: OK für Export-Routen; LÜCKE – generische Job-API ohne Ersteller-Regel. Daten: OK – Snapshot persistiert. E2E: OK – E01/L10 (Org-Dimension); kein client-gescopter Real-Lauf. Robustheit: OK – Altjobs fail-closed.
- **Abhängigkeiten:** WP-022, WP-027, WP-030, JOB-01/JOB-03 (generisches Job-Modell).
- **Defekte:** F-059 (P2) In das generische Job-Modell gespiegelte Export-Jobs sind über GET /api/v1/jobs und /jobs/{id} für andere Nutzer und Clients sichtbar
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** API-Test: B (job:read, anderer Client) sieht A's Export-Job weder in GET /api/v1/jobs noch per /jobs/{id} (404); Site-/Team-Snapshot-Test; client-gescopter Real-Stack-Export.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P3“ → „P2“. Folgt aus der Korrektur von L-T1-23 auf P2.

<a id="wp-036"></a>
#### WP-036 – GraphQL-BFF: Leserechte je Resolver, Feldprojektion, Entitlement

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** GQL-04
- **Anforderung/Nutzerziel:** Der GraphQL-BFF gibt nichts über RBAC-Rechte, Entitlements und RLS-Scope des Aufrufers hinaus preis, liefert nur selektierte Felder und lehnt Mutationen ab (GQL-04).
- **Implementierung:** Commit f50f2a7; gqlgen e9ed81a/0537a0e: backend/internal/graphqlbff/resolver.go:85-98 authorize, :108-170 (cis/ci, relationships), :225-244 (Nested-Recheck); graphqlbff.go:154-170 (nur Sessions), :100-131 (POST-only, Limits); server/authz.go:332-337; router.go:441-442 (PG-Repos).
- **Test(s):** Baseline PASS: TestOnlySessionsReachTheBFF, TestResolversCheckTheirReadPermission, TestResolversCheckEntitlement, TestResponseContainsOnlySelectedFields, TestMutationsAreRefused, TestNewQueriesCheckTheirPermission, TestLimits, TestGraphQLRouteRequiresAuthenticationOnly, TestChannelMatrix (graphql, 6 Principal-Arten). Welle: TestAuditT1GraphQLFieldsHonourScope PASS (ci, relationships, topology, searchEverything, reviewInbox, jobs × org/client1/andere Org). Real-Stack T04 PASS.
- **Soll/Ist:** SOLL: kein Bypass von RBAC/Entitlements/RLS über den BFF, Projektion, keine Mutationen. IST: für die WP-036-Resolver (cis, ci, relationships, currentUser) erfüllt; belegt durch Unit-Tests, PG-Kanalmatrix und Real-Stack T04 (B's ci(id: A) null, keine A-Marker). Der ungereviewte Regressionstest deckt ci, relationships, topology, searchEverything, reviewInbox und jobs ab, aber nicht rackLayout und reviewInbox nur mit Kandidaten-Items. Die WP-185-Felder rackLayout und reviewInbox erben L-T1-02 bzw. L-T1-03 aus den Repositories (außerhalb der WP-036-AK).
- **Evidenz:** SP/runs/results-backend.tsv (pass); SP/work/t1-tests/untagged.log PASS TestAuditT1GraphQLFieldsHonourScope; SP/work/tenant-iso-final.log T04 ✓; psql ci_relationship_isolation (beide Endpunkte im Scope).
- **Abweichung:** -
- **Prüfebenen:** UX: N/A (kein UI-Konsument). API: OK – authentifiziert, nur Sessions, Recht und Entitlement je Resolver. Daten: OK – read-only, PG-gescopte Repos. E2E: OK – Real-Stack T04 (Org-Dimension) + Regressionstest Client-Scope. Robustheit: OK – fail-closed ohne Principal/Checker/Recht.
- **Abhängigkeiten:** WP-027, WP-184/185, WP-072. Hinweis: rackLayout (WP-185) erbt den Rack-Defekt L-T1-02 aus dem Rack-Repository; nicht Teil der WP-036-Resolver.
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Cross-Scope-Relationship mit Nested-CIs auf PG; Test mit echter Session-Middleware.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – soll_ist: „SOLL: kein Bypass von RBAC/Entitlements/RLS über den BFF, Projektion, keine Mutationen. IST: erfüllt; Unit-Tests, PG-Kanalmatrix, neuer Regressionstest über alle Query-Felder und Real-Stack T04 (B's ci(id: A) null, keine A-Marker).“ → „SOLL: kein Bypass von RBAC/Entitlements/RLS über den BFF, Projektion, keine Mutationen. IST: für die WP-036-Resolver (cis, ci, relationships, currentUser) erfüllt; belegt durch Unit-Tests, PG-Kanalmatrix und Real-Stack T04 (B's ci(id: A) null, keine A-Marker). Der ungereviewte Regressionstest deckt ci, relationships, topology, searchEverything, reviewInbox und jobs ab, aber nicht rackLayout und reviewInbox nur mit Kandidaten-Items. Die WP-185-Felder rackLayout und reviewInbox erben L-T1-02 bzw. L-T1-03 aus den Repositories (außerhalb der WP-036-AK).“. Der Regressionstest deckt nicht 'alle Query-Felder' ab. reviewInbox leakt nachweislich (L-T1-03, Defekttest: 'GraphQL reviewInbox as client 1 lists review items of client 2'). Die Resolver reviewInbox und rackLayout stammen aus WP-185 (Commit 0537a0e), daher keine Statuswirkung. Der Hinweis in abhaengigkeiten ist um 'reviewInbox erbt L-T1-03' zu ergänzen.

<a id="wp-037"></a>
#### WP-037 – RAG-Chunks mit Quellobjekt-Scope; keine Antwort ohne Treffer

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** AI-01
- **Anforderung/Nutzerziel:** Der AI-Assistent antwortet nur aus Chunks lesbarer Quellobjekte (Client/Site-RLS plus Leserecht); ohne zulässige Quellen kein Provider-Aufruf; Antworten zitieren ihre Quellen (AI-01).
- **Implementierung:** Commit 6a9f811: Migration 000069_ai_chunk_scope; ai/retrieval.go:38-120; ai/pg_repository.go:19-26 chunkVisible, :130, :140; handler.go:92-115 (noGroundingAnswer).
- **Test(s):** Baseline PASS: TestRAGChunksFollowSourceScope (PG), TestAskWithoutChunksSkipsProviderAndCitesNothing, TestRetrievalPermissionScoping u.a.; Vitest AssistantPage 1 PASS (gemockt); Probe probe_ai_site.sql.
- **Soll/Ist:** SOLL: Chunks wie ihre Quelle gescopt, Retrieval mit Leserecht, keine Antwort ohne zulässige Quellen, Zitate. IST: erfüllt; Client-Scope auf PG getestet, Site-Ableitung/-Policy per wiederholter SQL-Probe belegt.
- **Evidenz:** SP/runs/results-backend.tsv (pass); probe_ai_site ('derived|c1|S2', 'site1-scope sees|0', 'site2-scope sees|1').
- **Abweichung:** -
- **Prüfebenen:** UX: N/A (Add-on in G1 aus; AssistantPage nur gemockt). API: OK – fail-closed, 503 ohne Provider; Beobachtung: fremde conversation_id akzeptiert (kein Scope-Leck). Daten: OK – abgeleitete Spalten, Propagation, Policies. E2E: N/A (Modul in G1 aus). Robustheit: OK – Fremd-Org-Treffer verworfen.
- **Abhängigkeiten:** WP-027, WP-021, WP-076, E-03 (AI-Add-on in G1 aus).
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Site-/Ticket-Team-/Asset-Ableitung als Integrationstests; Ask mit fremder conversation_id -> 404.

<a id="wp-038"></a>
#### WP-038 – Agent-Registrierung mit Enrollment-Token und Client/Site-Bindung

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** AGT-06
- **Anforderung/Nutzerziel:** Endpoint-Agents registrieren sich nur mit gültigem Einmal-Token, das Client und optional Site bindet; Roaming-Geräte behalten ihren Client, erhalten einen Site-Vorschlag, ein Mensch bestätigt die Site (AGT-06).
- **Implementierung:** Commit a581aa3: Migration 000070_agent_enrollment_scope; agent/pg_repository.go:221-268 (Enroll verbraucht Token in derselben Tx; SuggestSite); handler.go:163-209, :215-256, :258ff ConfirmSite; Routen mit agent:manage; frontend agentApi.enroll (client.ts:2521-2529) ungenutzt und veraltet.
- **Test(s):** Baseline PASS: TestAgentEnrollmentBindsClientAndSite (PG), TestAgentRepositoryClientScope; Probe probe_agent_site.sql; Router-Probe (enroll verlangt agent:manage).
- **Soll/Ist:** SOLL: Token-gebundene Registrierung, gescopte Agents, Fingerprint-Vorschlag mit manueller Bestätigung. IST: umgesetzt und auf PG belegt; Site-Prädikat per wiederholter Probe belegt.
- **Evidenz:** SP/runs/results-backend.tsv (pass); enroll_integration_test.go:87-155 (Wiederverwendung 403, ohne Token 400, abgelaufen 403, Fremd-Client 403, nur Hash gespeichert); probe_agent_site ('site1-scope sees|ag-roam,ag-s1', RLS-Verstoß beim Verschieben nach S2).
- **Abweichung:** P3 außerhalb der AK: toter, veralteter Frontend-Client agentApi.enroll ohne enrollment_token (L-T1-28). Designhinweis: Enroll verlangt zusätzlich agent:manage.
- **Prüfebenen:** UX: N/A (keine UI im WP; P4-Modul in G1 aus). API: OK – 400/403/404-Pfade getestet. Daten: OK – Token-Hash, Einmalverbrauch in derselben Tx, Konsistenztrigger, Scope-Policies. E2E: N/A (kein Endpoint-Agent-Binary im Repo). Robustheit: OK – abgelaufen/wiederverwendet/fremd getestet; Nebenläufigkeit ungetestet.
- **Abhängigkeiten:** WP-027, WP-021, WP-051, E-03.
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-189 (P3) Frontend agentApi.enroll ist tot und veraltet (ohne enrollment_token)
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Site-Scope-Probe als Test, Nebenläufigkeitstest Einmal-Token, agentApi.enroll entfernen oder aus schema.d.ts typisieren.

<a id="wp-039"></a>
#### WP-039 – Spike TEC-06: RLS auf Hypertable, komprimierten Chunks und Continuous Aggregate

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** TEC-06, MON-01
- **Anforderung/Nutzerziel:** Eine evidenzbasierte Entscheidung, wie Mandantenisolation für TimescaleDB-Metriken (Hypertable, komprimierte Chunks, CAgg) erreicht wird, als ADR mit Integrationstest (TEC-06, MON-01).
- **Implementierung:** Commit 85f027c: docs/decisions/0001-timescale-rls.md (Entscheidung Punkte 1-4, Z. 84-99; Punkt 4 stellt Client/Site zurück); backend/internal/monitoring/timescale_rls_spike_integration_test.go:85-230.
- **Test(s):** Baseline PASS: TestTimescaleRefusesRLSWithCompression, TestSecurityBarrierViewsIsolateMetrics (5 Subtests). Welle: TestAuditT1MetricViewsHonourScope FAILS_AS_EXPECTED (Views ohne Client/Site-Begrenzung); TestAuditT1MetricReadPathsHonourScope PASS (Store-Lesepfade raw/5m/1h per Anwendungsprädikat gescopt).
- **Soll/Ist:** SOLL: Isolationsnachweis plus ADR, die eine org- und scope-begrenzte Variante wählt. IST: Org-Isolation belegt und entschieden; Client/Site bewusst zurückgestellt und Prädikaten je Abfrage überlassen – der Defekttest zeigt, dass ein Client-1-Principal Client-2-Samples direkt aus beiden Views liest; diese Fragilität ermöglichte das Metrik-Namen-Leck (L-T1-10).
- **Evidenz:** psql audit_db_05 pg_views (nur organization_id-Prädikat); SP/verify-tests-run2/defect-TestAuditT1MetricViewsHonourScope.log ('client1 selects 2 rows of the client-2 CI from metric_sample_v, want 0'); SP/work/t1-tests/untagged.log PASS TestAuditT1MetricReadPathsHonourScope.
- **Abweichung:** AK1 teilweise: gewählte Variante nur org-begrenzt, 'scope-begrenzt' weder entworfen noch getestet (L-T1-09). Priorität gegenüber dem verifizierten Urteil von P3 auf P2 angehoben, weil die Defekttest-Welle den ursachengleichen Defekt (Views org-only, auch WP-025-Befund P2) bestätigt hat.
- **Prüfebenen:** UX: N/A. API: N/A. Daten: LÜCKE – kein DB-seitiges Client/Site-Prädikat (Defekttest rot). E2E: N/A (Spike). Robustheit: OK – Leaky-Function- und CHECK-OPTION-Subtests, Canary für Timescale-Verhalten.
- **Abhängigkeiten:** WP-007; Grundlage für WP-040, WP-190.
- **Defekte:** F-051 (P2) Metrik-Views nur org-begrenzt: metric_sample ohne Client-/Site-Scope auf DB-Ebene
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** ADR um Scope-Entscheidung ergänzt; TestAuditT1MetricViewsHonourScope grün (beide Views, client- und site-gescopt), oder PO-akzeptierte, dokumentierte Ausnahme mit Test über alle Store-Lesepfade inkl. MetricNames.

<a id="wp-040"></a>
#### WP-040 – Metrik-RLS, 1-Tages-Chunks und korrektes Aggregat

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** MON-01, TEC-06
- **Anforderung/Nutzerziel:** Metriken mit 1-Tages-Chunks, Kompression, Retention und Stundenaggregat; Nutzer lesen nur Metriken ihrer Organisation und ihres Scopes; Langzeitabfragen nutzen metric_sample_1h mit korrekt gewichteten Mittelwerten.
- **Implementierung:** Commit 9b0d7f2: Migration 000071_metric_sample_rls (Security-Barrier-Views, entzogene Rechte, 1-Tages-Chunks); monitoring/pg_store.go:79-118 Ingest, :124-185 Lesepfade, :187-217 metricWhere (ci_id IN (SELECT id FROM ci)); pg_store.go:230-236 MetricNames (WP-190) ohne Prädikat; monitoring.go:836-872 CIMetrics ohne Sichtbarkeitsprüfung; known_gaps.go ViewProtectedTables.
- **Test(s):** Baseline PASS: TestMetricStoreUsesBarrierViews (PG), TestRLSCatalog, TestKnownGapsDocumented; Probe TestProbeV05CIMetricsFullRouter. Welle: PASS TestAuditT1MetricReadPathsHonourScope (raw/5m/1h × org/client-1/site-1 × mit/ohne CI-Filter, Org B); FAILS_AS_EXPECTED TestAuditT1CIMetricsEndpointScope, TestAuditT1MetricNamesHonourScope, TestAuditT1MetricViewsHonourScope.
- **Soll/Ist:** SOLL: mandantenisolierte Metrikablage mit 1-Tages-Chunks, korrektem Aggregat und Store auf Views. IST: Chunks, Aggregat, Org-Isolation und die Scope-Begrenzung der Query-Pfade sind belegt; DB-seitig fehlt Client/Site; MetricNames ohne Prädikat legt Serienamen fremder Client-CIs über den Produktionsrouter offen.
- **Evidenz:** Probe-Log ('client1 GET /cis/{foreign} -> 404', 'client1 GET /cis/{foreign}/metrics -> 200 ... pv5_secret_metric'); SP/verify-tests-run2/defect-TestAuditT1MetricNamesHonourScope.log ('client1 MetricNames(client-2 CI) = [t1_cpu t1_secret_series], want none'); defect-TestAuditT1CIMetricsEndpointScope.log; SP/work/t1-tests/untagged.log PASS TestAuditT1MetricReadPathsHonourScope.
- **Abweichung:** AK1 teilweise: RLS durch org-only Views ersetzt (ADR-begründet), Client/Site DB-seitig nicht erzwungen (L-T1-09). Cross-Client-Metrik-Namen-Leck über GET /cis/{id}/metrics (L-T1-10).
- **Prüfebenen:** UX: LÜCKE – kein Real-Stack-Test der CI-Detail-Metriken für gescopte Nutzer. API: FEHLER – GET /cis/{fremd}/metrics 200 mit Serienamen. Daten: OK – Org-Isolation, Grants, Chunk-Intervall, Query-Pfade gescopt; LÜCKE – kein DB-Client/Site-Scope. E2E: LÜCKE – kein UI->API->DB-Test mit gescoptem Nutzer. Robustheit: FEHLER – MetricNames ohne Scope-Prädikat, CIMetrics ohne Sichtbarkeitsprüfung.
- **Abhängigkeiten:** WP-039, WP-016, WP-190 (MetricNames, Gruppe T7), WP-065.
- **Defekte:** F-051 (P2) Metrik-Views nur org-begrenzt: metric_sample ohne Client-/Site-Scope auf DB-Ebene, F-052 (P2) Metrik-Serienamen eines fremden Client-CI über GET /api/v1/cis/{id}/metrics sichtbar
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT1CIMetricsEndpointScope, TestAuditT1MetricNamesHonourScope und TestAuditT1MetricViewsHonourScope grün (oder dokumentierte Ausnahme für die Views); Metrics-Kanal in TestChannelMatrix; Ingest-Ablehnung für Fremd-CI getestet.

<a id="wp-041"></a>
#### WP-041 – Architekturtest: kein Datenbankzugriff am Mandantenkontext vorbei

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** TEN-06
- **Anforderung/Nutzerziel:** Ein dauerhafter automatischer Guard in go test ./... und CI verhindert DB-Zugriffe außerhalb WithTenant/WithSystem; nur dokumentierte Systempfade dürfen den Pool direkt nutzen (TEN-06, E-08).
- **Implementierung:** Commit a249376: backend/internal/database/tenantguard_test.go (AST-Walk, Receiver-Namensheuristik /pool$/i, nur CallExpr-Selektoren); tenantguard_allowlist.go (4 kommentierte Einträge); database/pool.go:282-290 (behauptet Allowlist aller WithSystem-Aufrufer); CI .github/workflows/ci.yml:32, 72, 148.
- **Test(s):** Baseline PASS: TestNoDatabaseAccessOutsideTenantContext. Verifier-Scratch-Kopie mit internal/zzprobe/probe.go: nur die Kontrollzeile (r.pool.Exec) wird gemeldet; Alias, Method-Value und pgx.Connect nicht.
- **Soll/Ist:** SOLL: dauerhafter Guard mit kommentierter Freigabeliste der Systempfade inkl. WithSystem (E-08). IST: Guard existiert, läuft in CI und erkennt direkte Pool-Aufrufe per Namen; er ist umgehbar, und 24 WithSystem-Aufrufstellen in 19 Dateien sind ungeregelt.
- **Evidenz:** Scratch-Kopie: '--- FAIL ... tenantguard_test.go:48: internal/zzprobe/probe.go:28: direct database access pool.Exec' (nur Kontrolle), unveränderte Kopie 'ok'; grep: 24 database.WithSystem( in 19 Dateien.
- **Abweichung:** AK1 teilweise: Detektor per Alias/Method-Value/pgx.Connect umgehbar (L-T1-29, P3). AK2 teilweise: WithSystem-Systempfade ohne kommentierte Freigabeliste (L-T1-30, P3).
- **Prüfebenen:** UX: N/A. API: N/A. Daten: OK – aktuell kein direkter Zugriff außerhalb der Allowlist; LÜCKE – WithSystem ungeregelt. E2E: N/A. Robustheit: LÜCKE – namensbasierte Heuristik umgehbar, kein Fixture-Selbsttest.
- **Abhängigkeiten:** WP-010…WP-022, E-08.
- **Defekte:** F-190 (P3) Tenant-Guard erkennt Pool-Zugriffe über lokalen Alias, Method-Value oder direkte pgx-Verbindung nicht, F-191 (P3) WithSystem-Aufrufer sind entgegen E-08 und pool.go-Kommentar durch keine Freigabeliste geregelt
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Typbasierter Guard (go/types) mit Fixture-Selbsttest (Alias, Method-Value, eingebetteter Pool, pgx.Connect, sql.Open gemeldet; Tx in WithTenant nicht); kommentierte, geprüfte WithSystem-Allowlist oder formale Korrektur von E-08 und pool.go-Kommentar.

### M0.1b Authentisierung und Autorisierung

<a id="wp-042"></a>
#### WP-042 – Deaktivierung wirksam: Login/Refresh prüfen Status, Sessions und API-Keys widerrufen

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** TLC-04, AUT-02, AUT-01
- **Anforderung/Nutzerziel:** Ein Admin deaktiviert einen Benutzer; Sessions, Refresh-Cookies und API-Keys enden sofort, ein neuer Login reaktiviert nicht, und Owner offener Objekte sind nur mit Übergabe löschbar.
- **Implementierung:** backend/internal/user/handler.go:206-238 (UpdateUser; revokeSessions vor repo.UpdateUser :219-225), :241 DeleteUser; user/pg_repository.go:182-249 (FOR UPDATE, deactivateInTx :253), :289 openOwnershipQuery; identity/apikey_store.go:262-271 RevokeUserAPIKeys; identity/session.go:63-74 RevokeUser; server/router.go:285-291; cmd/server/main.go:176-178
- **Test(s):** Baseline PASS (results-backend.tsv): identity TestDeactivationEndsAccess (PG), TestCallbackRejectsDeactivatedUser, TestRefreshChecksUserStatus, TestValidateRejectsRevokedSessions, TestRefreshReadsRoleAssignmentsFromDatabase. Welle: TestT2DeactivationFullChain (Regression, PASS, nur mechanisch verifiziert), TestAuditT2ForeignDeactivationRevokesNothing (Defekt, FAILS_AS_EXPECTED, verify-tests-run2). Real-Stack roles-authz 10 PASS, 11 PASS.
- **Soll/Ist:** Soll: Deaktivierung beendet jeden Zugang sofort und atomar, ohne Wirkung außerhalb des Mandanten. Ist: Kriterien 1, 2 und 4 sind belegt. Kriterium 3 ist für den eigenen Mandanten belegt (PG-Test, Full-Chain-Probe mit Redis). Der Session-Widerruf ist aber nicht an die erfolgreiche, org-gebundene Deaktivierung gebunden (er läuft vor Lookup/Commit und außerhalb der Transaktion), sodass fremde Benutzer trotz 404 ausgeloggt werden (L-T2-01).
- **Evidenz:** results-backend.tsv PASS TestDeactivationEndsAccess u. a.; V/fullchain-deactivation.log (Bearer/X-API-Key 401 nach Deaktivierung, anderer User 200); V/xtenant-revoke.log ('status 404', 'blacklist entry for org B user written: true'); verify-tests-run2: TestAuditT2ForeignDeactivationRevokesNothing FAILS_AS_EXPECTED (t2_deactivation_tenant_auditdefect_test.go:104).
- **Abweichung:** L-T2-01 (P2): Widerruf vor Lookup/Commit verletzt Kriterium 3 ('in derselben Transaktion') und die Mandantentrennung. L-T2-02 (P3), L-T2-03 (P3) außerhalb der Kriterien.
- **Prüfebenen:** UX: LÜCKE (keine UI für Deaktivierung, L-T2-03). API: FEHLER (L-T2-01). Daten: OK (is_active, revoked_at, audit_log in PG). E2E: LÜCKE (kein UI→API→DB-Lauf; Full-Chain nur Probe/Regressionstest). Robustheit: FEHLER (wiederholbarer Fremd-Logout), sonst fail-closed OK.
- **Abhängigkeiten:** WP-012; WP-066/WP-050 (SPA-Refresh L-T2-18 verhindert produktiven Refresh, Handlerlogik selbst belegt); Redis-Store
- **Defekte:** F-060 (P2) Deaktivierung/Anonymisierung eines fremden Benutzers widerruft dessen Sessions trotz 404 (mandantenübergreifender Zwangs-Logout), F-195 (P3) Deaktivierung widerruft auch die vom Benutzer erzeugten Service-Account-API-Keys, F-196 (P3) Keine UI zum Deaktivieren, Reaktivieren oder Löschen von Benutzern
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT2ForeignDeactivationRevokesNothing PASS (ohne Tag-Fehlschlag) nach Fix; TestT2DeactivationFullChain weiter PASS mit inhaltlichem Review 'ok'; Entscheidung und Test zu Service-Account-Keys.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – soll_ist: „Soll: Deaktivierung beendet jeden Zugang sofort und atomar, ohne Wirkung außerhalb des Mandanten. Ist: alle vier Kriterien belegt (PG-Test, Full-Chain-Probe mit Redis); der Session-Widerruf erfolgt jedoch vor der org-gebundenen Prüfung und außerhalb der Transaktion, sodass fremde Benutzer trotz 404 ausgeloggt werden.“ → „Soll: Deaktivierung beendet jeden Zugang sofort und atomar, ohne Wirkung außerhalb des Mandanten. Ist: Kriterien 1, 2 und 4 sind belegt. Kriterium 3 ist für den eigenen Mandanten belegt (PG-Test, Full-Chain-Probe mit Redis). Der Session-Widerruf ist aber nicht an die erfolgreiche, org-gebundene Deaktivierung gebunden (er läuft vor Lookup/Commit und außerhalb der Transaktion), sodass fremde Benutzer trotz 404 ausgeloggt werden (L-T2-01).“. Innerer Widerspruch: 'alle vier Kriterien belegt' steht im Widerspruch zu abweichung ('L-T2-01 verletzt Kriterium 3') und zum Status TEILWEISE BESTANDEN.

<a id="wp-043"></a>
#### WP-043 – Org-Zuordnung über Nutzerattribut und zugelassenen Erstlogin

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** AUT-01, AUT-09, CH26
- **Anforderung/Nutzerziel:** Die Organisation stammt aus dem Keycloak-Attribut organization_id; ein Erstlogin legt nur bei Zulassung (Einladung/Signup) einen Benutzer an, sonst 403.
- **Implementierung:** backend/internal/identity/oidc.go:393 decodeOrganization; identity/handler.go:409, :552 (ErrFirstLoginNotPermitted→403); user/pg_repository.go:1057-1190 EnsureUser/resolveLogin (:1140-1144 FOR UPDATE, :1149-1156 Bootstrap-count ohne Lock); deploy/keycloak/realm-reticora.json (organization_id admin-only)
- **Test(s):** Baseline PASS: TestFirstLoginRequiresAdmission (PG), TestCallbackTakesOrganizationFromAttribute, TestValidateIDTokenReadsOrganizationAttribute, TestValidateIDToken*, PKCE-Tests. Real-Stack PASS: roles-authz 01, 10, 10b (SP/work/verify-roles-authz/final-run.log Z.12-13), tenant-isolation T09 (verify-tenant-iso/final.log:12), onboarding Test 5.
- **Soll/Ist:** Soll: Org nur aus dem Attribut, Erstlogin nur zugelassen. Ist: Kriterien 1 und 3 sowie Kriterium 2 im sequenziellen Fall sind auf dem Real-Stack positiv wie negativ belegt (roles-authz 10, 10b, T09). Bei parallelen Erstlogins in eine leere Org werden nicht zugelassene Benutzer angelegt (L-T2-05, reproduziert). Dazu kommt der rohe Fehlertext (L-T2-04).
- **Evidenz:** final-run.log '✓ 12 … 10 … refused (403); with an invitation it is admitted', '✓ 13 … 10b'; results-backend.tsv; V/bootstrap-race.log (3/1/4 Benutzer).
- **Abweichung:** L-T2-05 (P3, Bootstrap-Race, admin-kontrollierte Vorbedingung) verletzt Kriterium 2 im Nebenläufigkeitsfall. L-T2-04 (P3, roher Fehlertext) liegt außerhalb der Kriterien.
- **Prüfebenen:** UX: LÜCKE (roher englischer Text, L-T2-04). API: OK (401/403 real). Daten: OK (kein app_user bei Ablehnung). E2E: OK (01, 10, 10b, T09). Robustheit: LÜCKE (L-T2-05).
- **Abhängigkeiten:** WP-042, WP-044 (Realm), TLC-01, TLC-03
- **Defekte:** F-197 (P3) Login-Abweisung zeigt rohen englischen Backend-Fehlertext, F-198 (P3) Parallele Erstlogins in eine leere Organisation werden alle zugelassen
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final TEILWEISE BESTANDEN; BESTANDEN → TEILWEISE BESTANDEN: Der bestätigte, reproduzierte Defekt L-T2-05 (verify-B06/bootstrap-race.log: 3/1/4 app_user bei 4 parallelen Erstlogins ohne Einladung, erwartet 1; pg_repository.go:1149-1156 count(*) ohne Sperre) verletzt Kriterium 2 für die nicht zugelassenen parallelen Logins. Kriterien 1 und 3 bleiben real belegt.
- **Offene Nachweise / Retest:** Für BESTANDEN: Bootstrap-Zulassung serialisiert und Integrationstest PASS (N parallele EnsureUser auf leere Org → genau 1 app_user, übrige ErrFirstLoginNotPermitted, 20 Wiederholungen). Härtung: lokalisierte Meldung, KC-Account-API-Test (organization_id nicht änderbar).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – status: „BESTANDEN“ → „TEILWEISE BESTANDEN“. L-T2-05 ist bestätigt und reproduziert (work/verify-B06/bootstrap-race.log). 4 parallele EnsureUser-Erstlogins ohne Einladung in eine leere Org ergeben in 3 Läufen 3/1/4 app_user statt 1. Ursache: pg_repository.go:1149-1156 zählt mit count(*) ohne Sperre. Damit ist Kriterium 2 verletzt ('Erstlogin legt einen Benutzer nur an, wenn Org und E-Mail zugelassen sind; sonst 403'): Nicht zugelassene parallele Logins erhalten einen Benutzer statt 403. Nach den Statusregeln darf nur ein P3-Mangel AUSSERHALB der Kriterien BESTANDEN begleiten. Die Synthese wendet diese Regel bei WP-049 an (P3 L-T2-14 trotz Laufzeitschutz ⇒ TEILWEISE). Die admin-kontrollierte Vorbedingung senkt die Priorität, verschiebt den Mangel aber nicht aus dem Kriterium. Die Kriterien 1 und 3 bleiben belegt (roles-authz 10/10b, results-backend.tsv).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – status_aenderung: „-“ → „BESTANDEN → TEILWEISE BESTANDEN: Der bestätigte, reproduzierte Defekt L-T2-05 (verify-B06/bootstrap-race.log: 3/1/4 app_user bei 4 parallelen Erstlogins ohne Einladung, erwartet 1; pg_repository.go:1149-1156 count(*) ohne Sperre) verletzt Kriterium 2 für die nicht zugelassenen parallelen Logins. Kriterien 1 und 3 bleiben real belegt.“. Folgeänderung der Statuskorrektur. Die Evidenz ist ausgeführt: Overlay-Probe mit 3 Läufen auf PG.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „P3 L-T2-04 (roher Fehlertext), P3 L-T2-05 (Bootstrap-Race, admin-kontrollierte Vorbedingung) – beide außerhalb des Kriterienkerns.“ → „L-T2-05 (P3, Bootstrap-Race, admin-kontrollierte Vorbedingung) verletzt Kriterium 2 im Nebenläufigkeitsfall. L-T2-04 (P3, roher Fehlertext) liegt außerhalb der Kriterien.“. L-T2-05 betrifft wörtlich die Zulassungsregel aus Kriterium 2 und liegt daher nicht außerhalb des Kriterienkerns.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – soll_ist: „Soll: Org nur aus dem Attribut, Erstlogin nur zugelassen. Ist: erfüllt und auf Real-Stack positiv wie negativ belegt; Rest-Mängel: roher Fehlertext, Bootstrap-Race.“ → „Soll: Org nur aus dem Attribut, Erstlogin nur zugelassen. Ist: Kriterien 1 und 3 sowie Kriterium 2 im sequenziellen Fall sind auf dem Real-Stack positiv wie negativ belegt (roles-authz 10, 10b, T09). Bei parallelen Erstlogins in eine leere Org werden nicht zugelassene Benutzer angelegt (L-T2-05, reproduziert). Dazu kommt der rohe Fehlertext (L-T2-04).“. Konsistenz mit dem korrigierten Status.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – offene_nachweise: „Keine für BESTANDEN; Härtung: Concurrency-Test Bootstrap, lokalisierte Meldung, KC-Account-API-Test (organization_id nicht änderbar).“ → „Für BESTANDEN: Bootstrap-Zulassung serialisiert und Integrationstest PASS (N parallele EnsureUser auf leere Org → genau 1 app_user, übrige ErrFirstLoginNotPermitted, 20 Wiederholungen). Härtung: lokalisierte Meldung, KC-Account-API-Test (organization_id nicht änderbar).“. Upgrade-Bedingung zum korrigierten Status.

<a id="wp-044"></a>
#### WP-044 – Kein Demo-Administrator im Produktivimport; Passwort-/Lockout-Policy

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** AUT-09, CH26
- **Anforderung/Nutzerziel:** Produktive Installation ohne bekannten Demo-Admin; individueller Admin mit temporärem Passwort; Passwort-, Lockout- und MFA-Pflicht für Admin-Rollen.
- **Implementierung:** deploy/keycloak/realm-reticora.json (users [], passwordPolicy, bruteForceProtected, browser-mfa mit mfa_required; Gruppe reticora-admin ohne realmRoles); install-cloud.sh:1510-1550 create_initial_admin; backend/internal/signup/keycloak.go:89-123 (keine Realm-Rolle); identity/oidc.go:54-57
- **Test(s):** sh-install-cloud-helpers.log 36/36 (statische/fake-kcadm-Prüfungen). Welle: TestAuditT2FirstOrgAdminAccountRequiresMFA, TestAuditT2RealmAdminGroupRequiresMFA (Defekt, FAILS_AS_EXPECTED). Real-Stack onboarding Test 5 PASS (Login nur mit Passwort = Beleg der Verletzung).
- **Soll/Ist:** Soll: kein Demo-Admin, individueller Admin, Policies inkl. MFA für Admin-Rollen. Ist: Demo-Admin entfernt; MFA nur für den Installer-Admin; Signup-/Einladungs-/Operator-/Gruppen-org_admins melden sich nur mit Passwort an (Real-Stack, Live-Keycloak).
- **Evidenz:** V/kc-signup-admin-mfa.txt (credential types ['password'], kein mfa_required); verify-tests-run2 t2_mfa_auditdefect_test.go:134/:168; sh-install-cloud-helpers.log 36/36.
- **Abweichung:** L-T2-06 (P0) verletzt Kriterium 3. Kriterium 2 ist nur mit fake kcadm belegt (Installationspfad durch Netzwerk-Policy blockiert). L-T2-07 (P2, Hypothese).
- **Prüfebenen:** UX: N/A. API: N/A. Daten: FEHLER (keine Sync DB-Rolle→KC-MFA). E2E: FEHLER (Signup-Admin ohne OTP). Robustheit: LÜCKE (Upgrade-Pfad, L-T2-07).
- **Abhängigkeiten:** WP-043, WP-046, TLC-01/02/03; install-smoke (nicht lokal ausführbar)
- **Defekte:** F-005 (P0) MFA für org_admins aus Signup, Einladung, Operator-Pfad und Gruppe reticora-admin nicht erzwungen, F-061 (P2) Bestehende Installationen behalten nach Upgrade den alten Realm ohne neue Policies
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** MFA-Defekttests PASS; Real-Stack-Test: Signup- und eingeladener org_admin müssen CONFIGURE_TOTP durchlaufen; Installer-Adminanlage gegen echten Keycloak 24; Upgrade-Shell-Test.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Folgt aus der Korrektur von L-T2-06 auf P0 (WP-Priorität = höchste Priorität der Abweichungen).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „L-T2-06 (P1) verletzt Kriterium 3; Kriterium 2 nur mit fake kcadm belegt (Installationspfad durch Netzwerk-Policy blockiert); L-T2-07 (P2, Hypothese).“ → „L-T2-06 (P0) verletzt Kriterium 3. Kriterium 2 ist nur mit fake kcadm belegt (Installationspfad durch Netzwerk-Policy blockiert). L-T2-07 (P2, Hypothese).“. Prioritätsangabe an die Defektkorrektur angepasst.

<a id="wp-045"></a>
#### WP-045 – Korrekte Aktionszuordnung im Authz-Mapping inkl. verschachtelter Spezialrechte

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** RBA-04, RBA-06, MET-14, CI-10
- **Anforderung/Nutzerziel:** Jede API-Operation verlangt das semantisch richtige Recht; Spezialaktionen sind nicht über breitere Elternrechte erreichbar.
- **Implementierung:** backend/internal/server/authz.go routeRules/PermissionForRoute/AuthorizeRoute; testdata/route_permissions.txt (505 Operationen); identity/types.go:120 PermCredentialDecrypt; Migration 000072
- **Test(s):** Baseline PASS: TestEveryOperationRequiresItsExpectedPermission, TestSpecialActionsRequireTheirOwnPermission, TestAuthorizationMiddlewareEnforcesPermissions, TestEveryRouteHasAPermissionMapping, TestRBA01KeysGateTheirActions. Real-Stack PASS: roles-authz 06, 07, 08, 09; FAIL 09c (UI-Feedback, L-T2-17).
- **Soll/Ist:** Soll: korrekte Rechte je Operation. Ist: per Golden-Tabelle, Enforcement-Tests, DB-Seed und Real-Stack (Viewer) belegt.
- **Evidenz:** results-backend.tsv; final-run.log '✓ 6 … 06', '✓ 7 … 07'; psql audit_db_06 Rollenmatrix.
- **Abweichung:** P3 L-T2-08 (toter Schlüssel credential:decrypt). Verknüpft, nicht zugerechnet: L-T2-17 (Webhook-UI-Feedback, Backend-403 korrekt).
- **Prüfebenen:** UX: N/A. API: OK. Daten: OK (Seed). E2E: OK (06 real, DB unverändert). Robustheit: OK (fail-closed).
- **Abhängigkeiten:** WP-009, WP-046, WP-047
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-062 (P2) Webhook-Seite verschluckt Serverablehnungen (400 Egress/Validierung, 403): keine Meldung, F-199 (P3) Permission credential:decrypt ist geseedet, steuert aber keine Route
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN; optional DB-Rollen-Integrationstest engineer/org_admin (approve/reconciliation).

<a id="wp-046"></a>
#### WP-046 – Ein Rollenmodell: OIDC-Rollen auf DB-Rollen abbilden; Viewer ohne Credential-Rechte

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** RBA-02
- **Anforderung/Nutzerziel:** Session-Rechte stammen nur aus DB-Rollen; IdP-Gruppen werden fest auf Seed-Rollen abgebildet; Rollenmatrix nach RBA-02; Viewer ohne Credential-Rechte.
- **Implementierung:** backend/internal/identity/rbac.go:69-81; identity/oidc.go:268 (nur groups); identity/handler.go applyAccess; permission/role_matrix.go; Migration 000073; user/pg_repository.go:594-596
- **Test(s):** Baseline PASS: TestStandardRolesMatchTheRoleMatrix (PG), TestIdPRolesMapToStandardRoles, TestSessionPermissionsComeFromDatabaseRoles. Real-Stack PASS: 01, 06, 07, 08, 09, 11; FAIL: 09b (L-T2-10), 09c (L-T2-17), 12 (ext D4), 13 (L-T2-11).
- **Soll/Ist:** Soll: ein DB-basiertes Rollenmodell nach RBA-02. Ist: alle drei Kriterien auf PG und Real-Stack belegt; Mängel liegen in Anzeige (09b) bzw. außerhalb der Kriterien.
- **Evidenz:** final-run.log '✓ 1 … 01', '✓ 14 … 11', ✘ 10/11/15/16 (09b, 09c, 12, 13); results-backend.tsv.
- **Abweichung:** P3 L-T2-09 (Realm-Rollen nicht gemappt, README), P3 L-T2-10 (Systemrollen als 'Custom'). Verknüpft, nicht zugerechnet (keine WP-046-Kriterie betroffen): L-T2-11 (P1, kein Rollenentzug), L-T2-17 (P2, Webhook-UI), ext D4.
- **Prüfebenen:** UX: LÜCKE (09b). API: OK. Daten: OK. E2E: OK (01/06/07/11). Robustheit: OK; kein Revoke-API (L-T2-11, kriterienfremd).
- **Abhängigkeiten:** WP-045, WP-044, WP-009, WP-168; E-13 (vorläufig)
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-017 (P1) Rollenzuweisung nicht entziehbar: kein DELETE /role-assignments/{id}, F-062 (P2) Webhook-Seite verschluckt Serverablehnungen (400 Egress/Validierung, 403): keine Meldung, F-200 (P3) Keycloak-Realm-Rollen erreichen das Backend nicht; README behauptet Mapping, F-201 (P3) Rollen-Tab zeigt die vier RBA-02-Systemrollen als 'Custom' (API: is_system=false), extern: D4 Creating a CI through the UI form fails with 400: the form sends a CI type key ('server') where the API expects the CI type id
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN; Härtung: 09b PASS, Realm-Rollen/README bereinigen, Credential-Route-Real-Stack-Test.

<a id="wp-047"></a>
#### WP-047 – Keine Klartext-Geheimnisse an Menschen: Decrypt-Pfad, Webhook-Header, Log-Redaktion

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** SEC-01, COL-02
- **Anforderung/Nutzerziel:** Keine API-Antwort oder Logzeile zeigt Klartext-Geheimnisse.
- **Implementierung:** backend/internal/credential/handler.go:29-32; credential/credential.go:52-62; webhook/webhook.go:65,70-80; platform/httpx/redact.go:31,75-110; config/config.go:552 MaskDSN; middleware/middleware.go:275-277
- **Test(s):** Baseline PASS: TestCredentialSecretsNeverLeaveTheAPI, TestHandler_HeadersAreWriteOnly, TestRedactingHandlerRemovesSecrets, TestIsSecretKey, TestMaskDSN; generate:api:check exit 0.
- **Soll/Ist:** Soll: keine Klartext-Geheimnisse an Menschen/Logs. Ist: für alle aktuellen Pfade erfüllt; Härtungslücken der Redaktion ohne aktuelle Aufrufstelle.
- **Evidenz:** results-backend.tsv; V/redact-gaps.log (MAPLEAK/STRUCTLEAK/KVLEAK leaked=true; URLSECRET/NAMEDSECRET leaked=false).
- **Abweichung:** P3 L-T2-12 (Map/Struct/key=value nicht redigiert; Webhook-URL für Viewer) – außerhalb aktueller Aufrufstellen.
- **Prüfebenen:** UX: OK. API: OK. Daten: LÜCKE (kein PG-Roundtrip maskierter Header). E2E: LÜCKE (kein Real-Stack-Logcheck). Robustheit: LÜCKE (L-T2-12).
- **Abhängigkeiten:** WP-045, WP-147
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-202 (P3) Log-Redaktion deckt Map/Struct/key=value nicht ab; Webhook-URLs für Viewer sichtbar
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN; Härtung: Redaktions-Negativtests, PG-Webhook-Roundtrip.

<a id="wp-048"></a>
#### WP-048 – Redfish: Zertifikatsprüfung und protokollgetrennte Zugangsdaten

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** COL-02
- **Anforderung/Nutzerziel:** Der Collector sendet Zugangsdaten nur an geprüfte Endpunkte und nur die des passenden Protokolls.
- **Implementierung:** collector/plugins/redfish/redfish.go:79-90,103,130; collector/plugins/plugin.go:71-78 (credentialAliases inkl. wmi→ssh, Commit 7c87220), :86; collector/collectorcmd/bundle.go:104-107; collector/plugins/wmi/wmi.go:42-48,157-163,209
- **Test(s):** Baseline PASS (results-collector.tsv): TestCollectRejectsUntrustedCertificateByDefault, TestCollectAcceptsScopedCAAndPin, TestCredentialsForSeparatesProtocols. Welle: TestAuditT2CredentialsArePerProtocol (FAILS_AS_EXPECTED); T4: TestAuditWMICredentialTransport, TestAuditCredentialKindRouting (FAILS_AS_EXPECTED).
- **Soll/Ist:** Soll: Redfish-TLS geprüft, Credentials strikt je Protokoll. Ist: Redfish erfüllt; der Alias wmi→ssh schickt das SSH-Konto per HTTP Basic im Klartext an WinRM-Ziele.
- **Evidenz:** V/wmi-e2e.log ('CredentialsFor(wmi) = map[password:ssh-pw username:root]', 'listener saw scheme=http basic-auth user="root"'); verify-tests-run2 t2_credentials_auditdefect_test.go:52.
- **Abweichung:** L-T2-13 (P0, Klartext-Offenlegung des SSH-Kontos) verletzt Kriterium 2.
- **Prüfebenen:** UX: N/A. API: N/A. Daten: FEHLER (wmi-Credentials ungenutzt). E2E: LÜCKE (kein Collector-Lauf gegen TLS-Redfish). Robustheit: FEHLER (Klartext-Basic).
- **Abhängigkeiten:** WP-003, WP-147, WP-150 (T4: WMI-Transport)
- **Defekte:** F-010 (P0) WMI-Plugin sendet Credentials im Klartext (HTTP Basic über http://ziel:5985; HTTPS-Modus mit InsecureSkipVerify)
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT2CredentialsArePerProtocol und T4-WMI-Transporttests PASS; Integrationslauf Redfish-TLS-Scope.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Folgt aus der Korrektur von L-T2-13 auf P0.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „L-T2-13 (P1) verletzt Kriterium 2.“ → „L-T2-13 (P0, Klartext-Offenlegung des SSH-Kontos) verletzt Kriterium 2.“. Prioritätsangabe an die Defektkorrektur angepasst.

<a id="wp-049"></a>
#### WP-049 – SSRF-Schutz: gemeinsamer Egress-Client für Webhooks und SCIM

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** SEC-08
- **Anforderung/Nutzerziel:** Ausgehende Aufrufe an Nutzer-URLs (Webhooks, IGA/SCIM) erreichen keine internen Ziele; URLs werden auch beim Speichern geprüft.
- **Implementierung:** backend/internal/platform/egress/egress.go:86,116,146; webhook/webhook.go, dispatcher.go; iga/connector.go:48-73 (ValidateBaseURL :65); iga/handler.go:137 (Create validiert), :147-166 (UpdateConnector ohne Validierung); server/router.go:404,435; notify/channel.go:90-92; frontend/src/pages/WebhooksPage.tsx:51-82
- **Test(s):** Baseline PASS: TestAllowed, TestValidateURL, TestClientBlocksInternalDestinations, TestClientPinsTheCheckedAddress, TestClientRedirects, TestHandler_RejectsInternalDestinations, TestDispatcherDefaultClientBlocksInternalDestinations. Welle: TestT2IGAEgressCreateValidation, TestT2IGAEgressRuntimeBlocksLoopback (Regression PASS, mechanisch verifiziert); TestAuditT2IGAUpdateValidatesBaseURL (Defekt, FAILS_AS_EXPECTED). Real-Stack: W02 PASS, W01 FAIL (UI).
- **Soll/Ist:** Soll: SSRF-Schutz zur Laufzeit und beim Speichern für Webhook und IGA. Ist: Laufzeitschutz und Webhook-Speicherprüfung belegt (W02 real); der IGA-Update-Pfad speichert interne/HTTP-URLs ungeprüft; die UI zeigt die Ablehnung nicht.
- **Evidenz:** bulk-jobs-export W02 ✓ (verify-bulk-jobs-export/final.log:13), W01 ✘ (:12); verify-tests-run2 t2_egress_auditdefect_test.go:51 ('update … https://169.254.169.254/scim/v2: 200, want 400'); Code iga/handler.go:147-166; V/iga-egress-rerun.log, V/egress-obfuscated.log.
- **Abweichung:** L-T2-14 (P3) verletzt Kriterium 2 auf dem IGA-Update-Pfad; L-T2-17 (P2) Ablehnung beim Speichern für Nutzer unsichtbar; L-T2-15, L-T2-16 (P3, Härtung).
- **Prüfebenen:** UX: FEHLER (W01: keine Meldung, L-T2-17). API: FEHLER (IGA-PATCH ohne Validierung), Webhook OK. Daten: N/A. E2E: OK für Webhook-Ablehnung (W02), FEHLER UI (W01). Robustheit: LÜCKE (Übergangspräfixe, Notify-Kanal).
- **Abhängigkeiten:** WP-022; notify NTF-04; Webhook-UI (WP-191/WP-192)
- **Defekte:** F-062 (P2) Webhook-Seite verschluckt Serverablehnungen (400 Egress/Validierung, 403): keine Meldung, F-203 (P3) IGA-Connector-Update speichert base_url ohne SSRF-/https-Validierung, F-204 (P3) Notification-Webhook-Kanal-URL beim Speichern nicht gegen Egress-Policy geprüft, F-205 (P3) IPv6-Übergangspräfixe (6to4, Teredo, IPv4-kompatibel) mit internem IPv4 erlaubt
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final TEILWEISE BESTANDEN; BESTANDEN → TEILWEISE BESTANDEN: neuer bestätigter Defekt L-T2-14 (Defekttest TestAuditT2IGAUpdateValidatesBaseURL schlägt reproduzierbar fehl; Code iga/handler.go:147-166 ohne ValidateBaseURL) verletzt Kriterium 2 'URL-Validierung auch beim Speichern' für den IGA-SCIM-Connector; zusätzlich Real-Stack W01: Speicherablehnung dem Nutzer nicht erklärt (L-T2-17, P2).
- **Offene Nachweise / Retest:** TestAuditT2IGAUpdateValidatesBaseURL PASS; W01 PASS; Regressionstests mit inhaltlichem Review 'ok'.

<a id="wp-050"></a>
#### WP-050 – Middleware-Reihenfolge, Pre-Auth-Limit und atomare, principalgebundene Idempotenz

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** API-04, AUT-10
- **Anforderung/Nutzerziel:** Auth-Versuche werden pro IP vor der Prüfung begrenzt; Validierung und Idempotenz laufen nach Authz; Idempotenzschlüssel sind principal- und routengebunden und atomar.
- **Implementierung:** backend/cmd/server/main.go:446-448,658-677; server/authz.go:547-553; middleware/openapi.go:76-127, :233, :301 ('POST /api/v1/auth/refresh' bodyJSON); middleware/ratelimit.go:242-305; middleware/idempotency.go:40-90,118; frontend/src/auth/session.ts:16-31
- **Test(s):** Baseline PASS: TestPreAuthLimitCountsFailedAuthentication, TestClientIPTrustsOnlyConfiguredProxies, TestIdempotencyIsAtomicAndPrincipalBound, TestIdempotencyScopedByTenant, TestRouteMiddlewareRunsAfterAuthorization. Real-Stack: ci-lifecycle L03b PASS (parallele POSTs mit einem Key → 1 CI, Redis); roles-authz 14 FAIL. Welle (FAILS_AS_EXPECTED): TestAuditT2RefreshPassesOpenAPIValidation, TestAuditT2ValidatorRegistryMatchesOpenAPIRequestBodies, TestAuditT2SuccessfulRefreshDoesNotConsumePreAuthBudget, TestAuditT2CookieOnlyRefreshThroughProductionChain.
- **Soll/Ist:** Soll: Pre-Auth → Auth → Authz → Validierung → Idempotenz → Handler. Ist: Reihenfolge und Idempotenz belegt (auch real, L03b); die Validierungsstufe blockiert jeden SPA-Refresh, erfolgreiche Refreshes verbrauchen das Pre-Auth-Budget.
- **Evidenz:** V/live-refresh-contenttype.txt, V/refresh-validation.log; final-run.log:269; verify-ci-lifecycle/run2.log:7 (L03b ✓); verify-tests-run2 t2_auth_ratelimit_auditdefect_test.go:76/:155/:197.
- **Abweichung:** L-T2-18 (P1) betrifft Kriterium 1 (Validierungsstufe der Produktionskette); L-T2-19 (P2); L-T2-20 (P3).
- **Prüfebenen:** UX: FEHLER (Abmeldung nach 15 min). API: FEHLER (Refresh 400). Daten: N/A. E2E: FEHLER (14), OK Idempotenz (L03b). Robustheit: LÜCKE (nicht-atomare Pre-Auth-Prüfung, L-T2-19).
- **Abhängigkeiten:** WP-012, WP-066 (Cookie-Refresh 2041fcb), WP-166
- **Defekte:** F-018 (P1) OpenAPI-Validator weist jeden SPA-Refresh (POST /auth/refresh ohne Body/Content-Type) mit 400 ab; gleiche Abweichung bei weiteren bodylosen POSTs, F-063 (P2) Erfolgreiche Refreshes verbrauchen das Pre-Auth-Budget je IP; SPA meldet bei 429 ab, F-206 (P3) Idempotenz-Replay liefert gespeicherte Fehler wörtlich (alte trace_id; 5xx 24 h festgeschrieben)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Validator-Defekttests und TestAuditT2CookieOnlyRefreshThroughProductionChain PASS; roles-authz 14/14b PASS; Pre-Auth-Budget-Test PASS; Integrationstest der main.go-Kette.

<a id="wp-051"></a>
#### WP-051 – Agent-Kanal: TLS ohne Fallback, Authentifizierung, korrekter Payload/Endpunkt

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** AGT-03
- **Anforderung/Nutzerziel:** Der Endpoint-Agent meldet Telemetrie nur über TLS mit seinem Enrollment-Geheimnis an den Agent-Telemetrie-Endpunkt.
- **Implementierung:** backend/internal/agent/handler.go:66-102,319-323; backend/cmd/agent/main.go:126-141,147-165 (kein CheckRedirect),225,266-298; collector/collectorcmd/collectorcmd.go:1167-1195
- **Test(s):** Baseline PASS: TestEnrollmentIssuesAgentCredential, TestTelemetryRequiresTheMatchingAgentCredential, collectorcmd TestAgentRelayIsTLSOnlyAndForwardsTheAgentCredential, TestAgentRelayNeedsACertificate. Welle: TestT2AgentDirectChannelIsTLSOnly, TestT2AgentRelayChannelIsTLSOnly (Regression PASS, mechanisch); TestAuditT2AgentRefusesRedirectToPlainHTTP (FAILS_AS_EXPECTED).
- **Soll/Ist:** Soll: nur TLS, kein HTTP-Fallback. Ist: Klartext beim Verbindungsaufbau abgewiesen; bei 302/307 auf http:// folgt der Agent und sendet den Bearer-Token im Klartext.
- **Evidenz:** V/agent-probes.log ('redirect 302 https->http … plaintext server saw Authorization="Bearer secret-agent-token"'); verify-tests-run2 t2_redirect_auditdefect_test.go:55/:58.
- **Abweichung:** L-T2-21 (P2) verletzt Kriterium 1 'kein HTTP-Fallback' (nicht-standardmäßige Vorbedingung); L-T2-22 (P3).
- **Prüfebenen:** UX: N/A. API: OK (Handlerebene). Daten: LÜCKE (kein Test über echte AuthMiddleware+PG). E2E: LÜCKE (kein Agent→Relay→Backend-Lauf). Robustheit: FEHLER (Redirect-Downgrade).
- **Abhängigkeiten:** WP-038, WP-214 (Modul agents in G1 aus)
- **Defekte:** F-064 (P2) Agent folgt https→http-Redirects und sendet Bearer-Token im Klartext, F-207 (P3) Kompiliertes Agent-Binary im Repository
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT2AgentRefusesRedirectToPlainHTTP PASS; Integrationstest Agent-Token durch AuthMiddleware; Binary aus Repo entfernt.

### M0.1c Migrationen und Standortmodell

<a id="wp-052"></a>
#### WP-052 – Migration 000056: Quarantäne statt Löschen inkonsistenter Composition-Zeilen

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** AST-05
- **Anforderung/Nutzerziel:** Betreiber verlieren bei der FK-Härtung durch 000056 keine Daten; inkonsistente Zeilen landen in einer RLS-geschützten Quarantäne, Altinstallationen erhalten Release-Hinweis und Runbook.
- **Implementierung:** backend/migrations/000056_rls_enforcement.up.sql:137-185 (migration_quarantine ENABLE+FORCE RLS, DELETE…RETURNING→INSERT), down.sql:12-22; docs/backup-dr.md:189-238
- **Test(s):** TestMigration56QuarantinesInconsistentRows (internal/composition, Baseline pass, echte up/down-SQL); migrate up/down-all/up OK; Verifier-Probe probe-052-down-legacy.sql (reproduziert Fehler)
- **Soll/Ist:** Soll: Quarantäne, Release-Hinweis, Runbook, Test. Ist: alles vorhanden und getestet; down-Pfad scheitert nur auf Altinstallationen ohne Quarantänetabelle (P3).
- **Evidenz:** results-backend.tsv pass TestMigration56QuarantinesInconsistentRows; RLS-Flags true/true (audit_db_07); Legacy-down-Probe ERROR relation "migration_quarantine" does not exist
- **Abweichung:** P3 L-T3-28: down auf Altinstallationen nicht abgesichert, backup-dr.md:209 verspricht zu viel (außerhalb der Kriterien).
- **Prüfebenen:** UX N/A; API N/A; Daten OK (Quarantäne mit RLS, down stellt wieder her); E2E N/A; Robustheit LÜCKE (Legacy-down, kein dedizierter RLS-Isolationstest)
- **Abhängigkeiten:** WP-006
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-224 (P3) 000056-down scheitert auf Altinstallationen ohne migration_quarantine; backup-dr.md:209 verspricht Rollback
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Für P3-Bereinigung: geschützter down-Abschnitt + Migrationstest ohne Tabelle; korrigierter Satz in backup-dr.md.

<a id="wp-053"></a>
#### WP-053 – Location-API auf kanonischen Baum umstellen; BuildTree-Fehler beheben

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** LOC-10, LOC-07
- **Anforderung/Nutzerziel:** Nutzer pflegen einen kanonischen Standortbaum über /api/v1/locations und die LocationTreePage mit vollständigem Baum, Feldfehlern und CRUD auf allen Ebenen (LOC-07).
- **Implementierung:** backend/internal/locations/tree.go:17-33, handler.go:24-31,146-191, pg_repository.go; server/repositories.go:186; frontend/src/pages/LocationTreePage.tsx:41-260; api/cmdbHooks.ts:164-183 (kein Update-Hook)
- **Test(s):** Baseline pass: TestBuildTreeKeepsAllChildren, TestHandlerTreeAndFieldProblems, TestLocationTree* (PG); Vitest LocationTreePage (Mock). Real-Stack pass: locations-racks L01, L03, L04; tenant-isolation T01, T07.
- **Soll/Ist:** Die 4 Kriterien sind Ende-zu-Ende auf echtem Stack belegt; LOC-07 (vom WP geschlossen) Update/Verschieben fehlt im UI.
- **Evidenz:** Real-Stack L01/L03/L04 pass (SP/work/locrack/run-final.log, verify-locrack/final.log); grep ohne useUpdateLocation; probe-hist.log history/location → 400
- **Abweichung:** P2 L-T3-06 kein Umbenennen/Verschieben im UI; P3 L-T3-29 Löschen ohne Bestätigung/409 ohne Arten; P3 L-T3-30 History-Whitelist location_node.
- **Prüfebenen:** UX LÜCKE (kein Rename/Move, keine Löschbestätigung); API OK (422/409 über echten Stack), LÜCKE AuthZ-Test viewer 403; Daten OK; E2E OK (L01/L03/L04); Robustheit OK
- **Abhängigkeiten:** WP-026; WP-055 (409-Regeln)
- **Defekte:** F-079 (P2) Standortbaum-UI ohne Umbenennen/Verschieben (LOC-07 CRUD unvollständig), F-225 (P3) Standort-Löschen im Baum ohne Bestätigung; 409 ohne Abhängigkeitsarten, F-226 (P3) History-Endpunkt akzeptiert entfernten Typ location_node und weist location ab
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Rename/Move-UI mit Vitest + e2e-real (oder dokumentierte Entscheidung LOC-07-Update API-only); AuthZ-Test viewer → 403 auf POST/PATCH/DELETE /locations.

<a id="wp-054"></a>
#### WP-054 – CI-/Asset-Standortbezüge auf location; Site/Room im CI ableiten (DB-05)

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** LOC-10, DB-05
- **Anforderung/Nutzerziel:** CI- und Asset-Standort zeigen auf den kanonischen Baum; site/room werden abgeleitet und sind nicht direkt schreibbar; Migration verlustfrei und umkehrbar.
- **Implementierung:** backend/migrations/000074_ci_asset_location_fk.up.sql:30-317 (FK-Umstellung, ci_derive_location), 000088:66-68; ci/model.go:18-23,124; ci/pg_repository.go:1376
- **Test(s):** Baseline pass: TestCILocationDerivation, TestMigration74MovesReferencesToLocation (echte Migrations-SQL); Verifier-Probe C1-C3 (POST /cis); Real-Stack pass: L06, L10, T07
- **Soll/Ist:** Alle drei Kriterien implementiert und durch PG-Tests, Probe und Real-Stack belegt.
- **Evidenz:** results-backend.tsv pass beider Tests; pg_constraint confdeltype 'r'; to_regclass('location_node') NULL; L06/L10 pass
- **Abweichung:** -
- **Prüfebenen:** UX N/A; API OK (site_id → 400, fremder Client → 422); Daten OK (Trigger, Migration up/down); E2E OK (L06, L10); Robustheit OK
- **Abhängigkeiten:** WP-053; WP-055
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Empfohlen: Create-Pfad-Probe als Repo-Test committen.

<a id="wp-055"></a>
#### WP-055 – Löschregeln 409 und Umzug mit location_change, Audit und Pfadaktualisierung

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** LOC-11
- **Anforderung/Nutzerziel:** Standorte mit CIs, Assets, Mounts oder Kindern sind über keinen Löschpfad löschbar (409 mit Arten); Umzug aktualisiert Pfade atomar mit location_change und Audit.
- **Implementierung:** backend/migrations/000075_location_delete_rules.up.sql:12-77; locations/dependencies.go:46-97; tenantapi/pg_repository.go:283-305 (CheckDeletable subtree=true); locations/pg_repository.go:148-171
- **Test(s):** Baseline pass: TestLocationMoveRecordsChangeAndAudit, TestLocationDeleteRefusedWithDependencies (move_integration_test.go:202-204 kodiert Kaskade). Real-Stack: L05 FAIL, L04/L09/L10 pass. Defekttests fails-as-expected: TestAuditT3LocationDeleteRefusesChildren, TestAuditT3SiteDeleteKeepsNetworkObjects.
- **Soll/Ist:** RESTRICT-FKs, 409 für Objektreferenzen, Umzug mit Historie und RLS erfüllt; DELETE /sites|/buildings|/rooms mit Kindern → 204 mit Kaskade (LOC-11 verletzt), Site-Löschung entfernt VLANs.
- **Evidenz:** L05 'Expected: 409 Received: 204' (zwei Läufe); probe-loc.log A0 409 vs A1/B1/C1 204; Defekttest-Ausgabe '0 of 8 nodes left'
- **Abweichung:** P2 L-T3-04 Kaskade statt 409; P2 L-T3-05 VLAN/Subnetz/Collector/Scope-Verlust; P3 L-T3-31 location_change kaskadiert.
- **Prüfebenen:** UX N/A (Baum nutzt /locations, L04 OK); API FEHLER (Fach-Endpunkte 204); Daten FEHLER (stille Kaskade); E2E FEHLER (L05); Robustheit OK (RLS, FK-Backstop)
- **Abhängigkeiten:** WP-054; tenantapi-, IPAM-, Discovery-Scope-WPs
- **Defekte:** F-023 (P1) DELETE /sites|/buildings|/rooms/{id} löscht Standorte mit Kindern (204, Kaskade) statt 409 (LOC-11), F-024 (P1) Site-Löschung kaskadiert VLANs/Enrollment-Tokens und entzieht Subnetzen, Collectoren, Discovery-Scopes, ci_merge still die Site, F-227 (P3) location_change-Historie wird mit dem Standort gelöscht (ON DELETE CASCADE)
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT3LocationDeleteRefusesChildren und TestAuditT3SiteDeleteKeepsNetworkObjects PASS; Real-Stack L05 grün; move_integration_test.go angepasst.

<a id="wp-056"></a>
#### WP-056 – Instanzattribute: Discovery-Schutz und Audit

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** MET-14
- **Anforderung/Nutzerziel:** Instanzattribute werden nie von Discovery überschrieben; Änderungen an Instanzdefinitionen werden auditiert.
- **Implementierung:** backend/internal/discovery/discovery.go:516-551,1466-1473; reconciliation.go:275-300; server/router.go:360-363,382; citype/pg_repository.go:608-611
- **Test(s):** Baseline pass: TestIngestNeverWritesInstanceFields, TestIngestWithholdsAttributesWithoutDefinitions, TestWithoutInstanceFields, TestInstanceFieldChangesAreAudited (PG); Verifier-PG-Probe probe-inst.log
- **Soll/Ist:** Erfüllt auf allen Ingest-Pfaden (eine Funktion), produktiv verdrahtet, PG-belegt.
- **Evidenz:** probe-inst.log: 202 protected_instance_fields=1, _instance.rack_label='manual R7'
- **Abweichung:** -
- **Prüfebenen:** UX N/A; API OK (LÜCKE AuthZ-Test); Daten OK; E2E N/A; Robustheit OK (fail-closed unit)
- **Abhängigkeiten:** WP-045, WP-013, WP-091
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Empfohlen: PG-Ingest-Regressionstest und NATS-Pfadtest committen. Hinweis: L-T3-19 (WP-091) kann _instance über den Override-Endpunkt zerstören.

### M0.1d Overrides, Reconciliation-Schutz, Datenverlust

<a id="wp-057"></a>
#### WP-057 – Manuelle Schreibpfade erzeugen Overrides; RFC-7396-Merge-Patch

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** CI-04, OVR-01
- **Anforderung/Nutzerziel:** Manuelle CI-Änderungen folgen JSON Merge Patch (null löscht) und jedes manuell geänderte Feld wird geschützter Override – auf allen manuellen Pfaden.
- **Implementierung:** backend/internal/ci/mergepatch.go:9-43; ci/handler.go:471-516; ci/pg_repository.go:783-886,1452-1478; ci/bulk.go:142-160 (ohne Manual); frontend/src/pages/CIFormModal.tsx:46-49,156-164
- **Test(s):** Baseline pass: TestMergePatchRFC7396, TestManualPatchCreatesOverridesDiscoveryKeeps (PG). Real-Stack: L04/D03/D04 pass; L05, L05b, L12b FAIL. Defekttests fails-as-expected: TestAuditT3BulkStatusIsManualWrite, TestAuditT3MergePatchNullClearsTypedField.
- **Soll/Ist:** PATCH-Attribute und Einzel-PATCH-Override korrekt; null auf typisierten Feldern ignoriert, Bulk-Status ohne Override/still verworfen, UI kann Felder nicht leeren.
- **Evidenz:** probe-ci.log PROBE1/2/3b; L05 '9.9.9' nach Reload; L05b '1.2.3'; Defekttest-Ausgaben
- **Abweichung:** P1 L-T3-01; P2 L-T3-07, L-T3-08; P3 L-T3-32, L-T3-33.
- **Prüfebenen:** UX FEHLER (Leeren unmöglich, L05; Bulk meldet Erfolg); API FEHLER (null ignoriert, Bulk ok bei No-op, 404-Mapping); Daten FEHLER (Bulk ohne Override); E2E FEHLER (L05/L05b/L12b); Robustheit LÜCKE
- **Abhängigkeiten:** WP-010, WP-011; WP-181, WP-058, WP-061
- **Defekte:** F-020 (P1) Manuelle Massen-Statusänderung (POST /cis/bulk set_status) ist kein manueller Schreibvorgang: kein Override, keine Versionserhöhung, unter Status-Override still verworfen und als ok gemeldet, F-080 (P2) PATCH /cis/{id} ignoriert null auf typisierten Feldern (200, unverändert) – nicht RFC 7396, F-081 (P2) Leeren eines optionalen Felds im CI-Bearbeiten-Dialog wird stillschweigend nicht gespeichert, F-228 (P3) Bearbeiten-Dialog sendet alle Felder: veralteter Editor erhält 409 trotz Änderung nur eines unberührten Felds, F-229 (P3) CI-Update-Handler bildet alle unerwarteten Fehler auf 404 'CI not found' ab
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT3BulkStatusIsManualWrite und TestAuditT3MergePatchNullClearsTypedField PASS; Real-Stack L05, L05b, L12b grün; Vitest CIFormModal-Payload.

<a id="wp-058"></a>
#### WP-058 – Zentrale fail-closed Schreibentscheidung für Automationsquellen mit Rangtabelle

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** REC-03, OVR-01, CI-10, API-07, REC-12
- **Anforderung/Nutzerziel:** Jede automatisierte Feldschreibung wird zentral nach REC-03 entschieden, überschreibt nie Overrides (Review statt Überschreiben) und schließt bei Lesefehlern fail-closed.
- **Implementierung:** backend/internal/override/decide.go:18-35,84-160; discovery/discovery.go:594-630,1380-1412; Migration 000076; server/router.go:600
- **Test(s):** Baseline pass: TestSourceRanksFollowREC03, TestDecideAutomatedWrite, TestDecisionFailuresAreCounted, TestIngestQueuesOverrideConflictOnce, TestManualPatchCreatesOverridesDiscoveryKeeps (PG). Real-Stack pass: D02-D06.
- **Soll/Ist:** Alle vier Kriterien erfüllt und ausgeführt belegt; nur Altliste der source-policy-API widerspricht REC-03 (ohne Wirkung).
- **Evidenz:** results-backend.tsv; discovery-override D04/D05/D06 pass (zwei Läufe); probe-policy.log
- **Abweichung:** P3 L-T3-34 source-policy-API mit Altreihenfolge (außerhalb Kriterien).
- **Prüfebenen:** UX N/A (Inbox in D04-D06 OK); API LÜCKE (source-policy P3); Daten OK; E2E OK (D04-D06); Robustheit OK (fail-closed unit), LÜCKE PG-fail-closed-Test
- **Abhängigkeiten:** WP-057; E-32
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-230 (P3) source-policy-API liefert Altreihenfolge im Widerspruch zu REC-03
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Für P3: DefaultPriorities aus SourceRanks ableiten + Test.

<a id="wp-059"></a>
#### WP-059 – Workflow set_ci_field und Agent-Updates über die zentrale Entscheidung

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** OVR-02, WFL-02, AGT-05
- **Anforderung/Nutzerziel:** Workflow (Rang 92) und Agent (Rang 85) schreiben über die zentrale Entscheidung mit Provenienz, überschreiben keine Overrides und erzeugen keine Duplikate (OVR-02).
- **Implementierung:** backend/internal/workflow/executor.go:295-341; override/decide.go:177-205; agent/handler.go:391-536 (anchorCI :453-466); ci/merge.go:239-267
- **Test(s):** Baseline pass (Memory): TestSetCIFieldRespectsOverrides, TestAgentUpdateUsesAnchorAndCentralDecision. Neu: TestAuditT3AgentTelemetryRankAndOverridesPG pass (final regression); Defekttest TestAuditT3AgentTelemetryFollowsMergedCI fails-as-expected. Verifier-PG-Probes wfprobe/agentprobe.
- **Soll/Ist:** Ränge, Entscheidung, Provenienz und Override-Schutz auf PG belegt; nach Merge des verknüpften CIs erzeugt der Agent ein Duplikat (OVR-02 verletzt).
- **Evidenz:** agentprobe 'activeCIsWithHostname=2'; Defekttest '1 before …, 2 after'; wfprobe 'workflow/92, version 1->2'
- **Abweichung:** P2 L-T3-10 Duplikat nach Merge; P3 L-T3-35 kein override_conflict-Review aus Workflow/Agent.
- **Prüfebenen:** UX N/A; API OK; Daten OK (PG-Probes); E2E LÜCKE (kein Real-Stack-Lauf); Robustheit FEHLER (Duplikat nach Merge, transiente Fehler = 'gone')
- **Abhängigkeiten:** WP-058, WP-021, WP-173, WP-089
- **Defekte:** F-083 (P2) Agent-Telemetrie legt nach Merge/Löschung des verknüpften CIs ein Duplikat an, F-231 (P3) Override-Konflikte aus Workflow und Agent erzeugen kein override_conflict-Review (REC-12)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT3AgentTelemetryFollowsMergedCI PASS; PG-Repo-Test für set_ci_field; REC-12 für Automationsquellen umgesetzt oder formell ausgegrenzt.

<a id="wp-060"></a>
#### WP-060 – Effektive CI-Sicht konsistent; Override-Clear synchronisiert

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** OVR-01, CI-10
- **Anforderung/Nutzerziel:** Jede CI-Lesesicht zeigt den effektiven Wert; Override-Aufheben stellt den beobachteten Wert atomar und auditiert wieder her, auch sichtbar im UI.
- **Implementierung:** backend/internal/ci/pg_repository.go:173-357,1513ff; override/pg_repository.go:323-450; override/handler.go:111-124; frontend/src/api/cmdbHooks.ts:275-301
- **Test(s):** Baseline pass: TestEffectiveViewAndOverrideClear (PG), TestOverrideRepositoryClientScope. Real-Stack: D02/D03/D04 pass, D07 FAIL (weiche UI-Assertion; harte API/DB/Audit-Assertionen pass).
- **Soll/Ist:** Backend/API erfüllen alle drei Kriterien; der Nutzerprozess ‚Override aufheben‘ zeigt im CI-Detail bis Reload den entfernten Wert.
- **Evidenz:** D07 'Expected: "FortiGate-100F" Received: "Audit-FW-…"' (full.log, d07.log); ovrprobe 404
- **Abweichung:** P2 L-T3-27 UI-Cache nach Clear veraltet; P3 L-T3-36 DELETE → 404 bei internen Fehlern.
- **Prüfebenen:** UX FEHLER (widersprüchliche Werte nach Clear); API OK, LÜCKE 404-Mapping; Daten OK; E2E FEHLER (D07); Robustheit LÜCKE (Fehler maskiert)
- **Abhängigkeiten:** WP-058; WP-061 (Versionierung der Override-Schreibvorgänge, L-T3-02)
- **Defekte:** F-100 (P2) CI-Detail zeigt nach „Override aufheben“ bis zum Reload weiter den entfernten Override-Wert, F-232 (P3) DELETE /cis/{id}/fields/{name}/override bildet jeden Fehler auf 404 ab
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** useClearOverride invalidiert ['ci',id]/['cis'] mit Vitest; Real-Stack D07 vollständig grün.

<a id="wp-061"></a>
#### WP-061 – Optimistische Nebenläufigkeit: CI-Version, ETag/If-Match, 409/412

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** API-07, API-03
- **Anforderung/Nutzerziel:** Zwei Bearbeiter überschreiben sich nicht still; Rang≥92-Schreibvorgänge erhöhen die Version, veraltetes If-Match → 409/412, UI sendet If-Match.
- **Implementierung:** backend/migrations/000077_ci_version; ci/pg_repository.go:852-856,1567-1599; ci/handler.go:486; frontend/src/api/client.ts:499-507; CIDetailPage.tsx:231 (ohne version); override/pg_repository.go syncCIField; ci/bulk.go:152
- **Test(s):** Baseline pass: TestOptimisticConcurrency (PG). Real-Stack: L04/L12/L13 pass, L12b FAIL. Defekttests fails-as-expected: TestAuditT3OverrideWritesRaiseVersion, TestAuditT3BulkStatusIsManualWrite.
- **Soll/Ist:** PATCH/Workflow korrekt; Override set/clear und Bulk-Status erhöhen die Version nicht → veralteter PATCH überschreibt (Lost Update); Instanzfelder ohne If-Match.
- **Evidenz:** ciprobe PROBE B-E ('ETag="1" … stale PATCH 200'); bulk_probe 'version 1->1'; Defekttest-Ausgaben
- **Abweichung:** P1 L-T3-01, L-T3-02; P2 L-T3-09; P3 L-T3-32. Wortlaut '412 für veraltetes If-Match' vs. API-07 (409 feldgranular) nicht als Defekt gewertet.
- **Prüfebenen:** UX LÜCKE (Instanzfelder ohne If-Match, Formular sendet alle Felder); API FEHLER (Override/Bulk umgehen Version); Daten FEHLER; E2E FEHLER (L12b), OK L12/L13; Robustheit FEHLER (Lost Update)
- **Abhängigkeiten:** WP-060, Bulk-WP (WP-181), CI-Detail
- **Defekte:** F-020 (P1) Manuelle Massen-Statusänderung (POST /cis/bulk set_status) ist kein manueller Schreibvorgang: kein Override, keine Versionserhöhung, unter Status-Override still verworfen und als ok gemeldet, F-021 (P1) Override setzen/aufheben erhöht ci.version nicht: veralteter If-Match-PATCH überschreibt den neueren geschützten Override, F-082 (P2) Instanzattribut-Bearbeitung im CI-Detail sendet kein If-Match, F-228 (P3) Bearbeiten-Dialog sendet alle Felder: veralteter Editor erhält 409 trotz Änderung nur eines unberührten Felds
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT3OverrideWritesRaiseVersion und TestAuditT3BulkStatusIsManualWrite PASS; Vitest If-Match für InstanceFieldsSection; Real-Stack L12b + Override-vs-Edit-Szenario grün.

<a id="wp-062"></a>
#### WP-062 – Spool ohne stillen Verlust: 24-h-Pufferung, Backpressure, Verlustmeldung, Reihenfolge

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** COL-05, OPS-06, NFR-04
- **Anforderung/Nutzerziel:** Der Collector puffert unbestätigte Ergebnisse ≥24 h, drosselt statt zu löschen, meldet jeden Verlust an den Server und liefert in Quellzeit-Reihenfolge ohne Batch-Limit.
- **Implementierung:** edgecore/buffer/buffer.go:103-277; collector/collectorcmd/collectorcmd.go:384-405,644-722,810-880,939-955; backend/internal/discovery/discovery.go:907-962
- **Test(s):** Baseline pass: TestDiskBufferMaxAgeKeepsMessagesFor24Hours, TestDiskBufferSizeLimitAppliesBackpressure, TestPeekBatchOrdersBySourceTime, TestUploadRecords_BacklogFirstAndUnboundedFlush, TestSpoolFlushDeliversTenThousandRecords. Defekttests fails-as-expected: TestAuditT3SpoolLossSurvivesRestart, TestAuditT3CorruptHeadDoesNotBlockSpool. Last: Spool 415 rec/s, 0 missing.
- **Soll/Ist:** 24 h, Backpressure, Reihenfolge, unbegrenzter Flush erfüllt; Verlustmeldung nicht restart-fest und lückenhaft, Head-of-Line-Blockade.
- **Evidenz:** spoolprobe 'after restart dropped=0'; colprobe 'rejected=5 delivered=0 spoolLen=3'; Defekttest-Ausgaben
- **Abweichung:** P2 L-T3-11, L-T3-12; P3 L-T3-37, L-T3-38.
- **Prüfebenen:** UX N/A; API OK (Heartbeat-Report), LÜCKE (Server persistiert nicht); Daten FEHLER (Zähler nur im Speicher, nicht-atomare Writes); E2E LÜCKE (kein Ausfalltest gegen echtes Backend); Robustheit FEHLER (HOL-Blockade)
- **Abhängigkeiten:** WP-003, WP-104, WP-143
- **Defekte:** F-084 (P2) Spool-Verlustzähler nicht persistiert: Verluste vor Neustart erreichen den Server nie, F-085 (P2) Head-of-Line-Blockade im Spool: korrupte Datei oder dauerhaft abgewiesener Batch stoppt jede Zustellung, F-233 (P3) Gespoolte Agent-Telemetrie bei 4xx/unlesbar ohne Verlustzähler verworfen, F-234 (P3) Server protokolliert Spool-Verlustmeldungen nur; Metrik-Samples/Traps ungezählt verworfen
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Beide edgecore-Defekttests PASS; Collector-Poison-Test (400/202, 503); Repo-Test Backpressure-Pause.

<a id="wp-063"></a>
#### WP-063 – Wartungsankündigung nicht ohne Versand als sent markieren

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** NFR-04, MGT-04, NTF-05
- **Anforderung/Nutzerziel:** Eine Wartungsankündigung behauptet keine Zustellung; Status bleibt pending bis zur Zustellung, die UI zeigt den echten Status.
- **Implementierung:** backend/internal/maintenance/pg_repository.go:240-252; handler.go:152-176; frontend/src/pages/MaintenancePage.tsx:23-29,60-106
- **Test(s):** Baseline pass: TestNotifyQueuesWithoutClaimingDelivery, TestNotifyQueuesInPostgres (PG). Verifier: maintprobe (PG), Vitest-Probe (Mock). Kein Repo-Vitest, kein e2e-real.
- **Soll/Ist:** Backend-Kriterium PG-belegt; UI-Statustext nur mit Mock und nur transient belegt, queued-Pfad aus der UI unerreichbar.
- **Evidenz:** maintprobe 'clients=2 total rows=4'; Vitest-Probe '2 Benachrichtigungen vorgemerkt – Versand ausstehend' (Mock)
- **Abweichung:** P3 L-T3-39 Duplikate; P3 L-T3-40 UI ohne CI-Zuordnung/gespeicherten Status.
- **Prüfebenen:** UX LÜCKE (transient, keine CI-Auswahl); API OK, LÜCKE 404-Mapping; Daten OK (pending), FEHLER gering (Duplikate); E2E LÜCKE; Robustheit LÜCKE
- **Abhängigkeiten:** WP-020, WP-028; Epic B Benachrichtigungskette
- **Defekte:** F-235 (P3) Wiederholtes „Kunden benachrichtigen“ erzeugt doppelte pending-Benachrichtigungen, F-236 (P3) Wartungs-UI kann keine CIs zuordnen und zeigt den gespeicherten Benachrichtigungsstatus nie
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Repo-Vitest MaintenancePage-Status; Real-Stack-Lauf UI→API→DB mit pending in DB und UI.

### M0.2a DB-Rollenvertrag und RLS-Matrix

<a id="wp-064"></a>
#### WP-064 – DDL-Vertrag CH19 für die App-Rolle; FORCE für alle Mandantentabellen

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** TEN-03, DB-04
- **Anforderung/Nutzerziel:** Die App-Rolle kann Laufzeit-Indizes (CH19/DB-04) ohne SUPERUSER/BYPASSRLS anlegen; jede Mandantentabelle erzwingt RLS auch für den Owner; der Server startet nicht, wenn die effektiven DB-Rechte vom Vertrag abweichen.
- **Implementierung:** backend/migrations/000078_app_role_ddl_contract.up.sql (reticora_owner NOLOGIN NOSUPERUSER NOBYPASSRLS, GRANT reticora_owner TO reticora_app WITH INHERIT FALSE SET TRUE, ENABLE+FORCE, Ownership, Event-Trigger) + down; backend/internal/database/role_check.go:42-43 (Erreichbarkeit nur ab current_user), :107-130 VerifyRoleContract; pool.go:61-99 NewPool (Login als DSN-User, AfterConnect SET ROLE reticora_app); cmd/server/main.go:207-211; deploy/docker-compose/docker-compose.yml:47-49 (POSTGRES_USER=reticora = Superuser), :287.
- **Test(s):** Baseline PASS: TestForceRowLevelSecurityBindsOwner, TestRuntimeIndexDDL, TestRoleContractStartupCheck (akzeptiert in :144 den Superuser-Login), TestPoolRefusesRLSBypassingRole, TestRLSCatalog, TestAttributeIndexJob; Probes reset_role_probe.sql, truncate_probe.sql. Welle: TestAuditT1StartupRefusesEscalatableLogin FAILS_AS_EXPECTED (backend/internal/database/audit_t1_role_contract_auditdefect_test.go); TestAuditT1DedicatedLoginRolePassesContract PASS (dedizierter LOGIN NOSUPERUSER NOBYPASSRLS besteht, RESET ROLE ohne Superuser).
- **Soll/Ist:** SOLL (TEN-03/CH19): App-DB-Rolle ohne SUPERUSER/BYPASSRLS, DDL nur über Owner-Rolle, FORCE überall, Startprüfung verweigert jede Abweichung der effektiven Rechte. IST: Rollendesign, FORCE und Owner-Bindung korrekt und getestet; ein dedizierter Nicht-Superuser-Login funktioniert (Positivtest). Die paketierte Compose-Installation meldet den Server jedoch als Superuser an, und die Startprüfung prüft nur current_user – jede App-Session ist ein RESET ROLE vom Superuser entfernt. Owner-TRUNCATE umgeht FORCE RLS. Die Down-Migration 000056 droppt die clusterweite Rolle reticora_app.
- **Evidenz:** reset_role_probe ('after SET ROLE|reticora_app|reticora|f|t', 'after RESET ROLE|reticora|t', 'org rows visible after RESET ROLE|1'); truncate_probe (Owner-TRUNCATE -> 0 Zeilen beider Orgs); SP/verify-tests-run2/defect-TestAuditT1StartupRefusesEscalatableLogin.log ('NewPool accepted a session whose login role regains SUPERUSER/BYPASSRLS via RESET ROLE'); SP/work/t1-tests/untagged.log PASS TestAuditT1DedicatedLoginRolePassesContract; Wellenlog zu TestMigrationsRoundtrip ('apply 000056_rls_enforcement.down.sql: ERROR: role reticora_app cannot be dropped ... (SQLSTATE 2BP01)').
- **Abweichung:** AK3 teilweise: Startprüfung ignoriert session_user, paketierte Installation mit Superuser-Login (L-T1-15, P2). Owner-TRUNCATE ignoriert FORCE RLS (L-T1-31, P3). Down-Migration 000056 droppt clusterweite Rolle (L-T1-32, P3, Betriebs-/Testinfrastruktur).
- **Prüfebenen:** UX: N/A. API: N/A (Startverhalten; TestPoolRefusesRLSBypassingRole). Daten: OK – Ownership, FORCE, Event-Trigger; LÜCKE – Owner-TRUNCATE. E2E: NICHT TESTBAR – paketierte Installation lokal nicht baubar (Docker-Build durch Netzwerkpolicy blockiert, 403 dl-cdn.alpinelinux.org/quay.io); Compose-Konfiguration gelesen. Robustheit: FEHLER – RESET ROLE stellt Superuser wieder her, Startprüfung bemerkt es nicht.
- **Abhängigkeiten:** WP-027; WP-208; Installer-WPs (install-cloud.sh/Compose, Gruppe T5); citype-Laufzeit-DDL.
- **Defekte:** F-057 (P2) Server meldet sich in der paketierten Installation als SUPERUSER an; Rollenprüfung prüft nur current_user, RESET ROLE umgeht RLS, F-192 (P3) reticora_owner (per SET ROLE aus reticora_app erreichbar) kann Mandantentabellen trotz FORCE RLS organisationsübergreifend per TRUNCATE leeren, F-193 (P3) Down-Migration 000056 droppt die clusterweite Rolle reticora_app und scheitert, wenn andere Datenbanken des Clusters Grants an sie halten
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT1StartupRefusesEscalatableLogin grün (Superuser-Login + SET ROLE -> RoleContractError mit session_user); Installer/k8s provisionieren Nicht-Superuser-Runtime-Login (install-smoke-Assertion, lokal NICHT TESTBAR); TRUNCATE-Guard mit Test; Down-Migration 000056 ohne clusterweiten DROP ROLE.

<a id="wp-065"></a>
#### WP-065 – RLS-Testmatrix: Org/Client/Site/Team × CRUD × NULL; Ausnahmeliste leer

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** TEN-09, TST-02
- **Anforderung/Nutzerziel:** Ein automatisierter, tabellengetriebener Test belegt, dass jede Mandantentabelle Org-, Client-, Site- und Team-Scope (inkl. kombinierter Scopes und NULL-Werte) für SELECT/INSERT/UPDATE/DELETE der App-Rolle erzwingt, mit leerer Ausnahmeliste, im CI-DB-Job.
- **Implementierung:** backend/internal/tenant/rls/matrix_integration_test.go:113-160 TestRLSMatrix (6 Principals), :200-260 run, :262-327 checkPrincipal, :331-407 checkUpdate; fixtures_test.go:86-130 loadMatrixTables; known_gaps.go:120 KnownGaps leer, :65/:71/:80 dokumentierte Ausnahmen; .github/workflows/ci.yml:126-166.
- **Test(s):** Baseline PASS: TestRLSMatrix (124 Zeilen: Parent + 123 Tabellen-Subtests), TestRLSCatalog, TestKnownGapsList, TestKnownGapsDocumented, TestModuleTablesInheritObjectScope, TestTeamRoomAndPersonScope, TestSitePoliciesRestrictSiteScopedPrincipals, TestClientPoliciesEnforceClientScope. Verifier-Strict-Variante (NULL-Lesesemantik nach TEN-05 gepinnt) 'ok 10.5 s', 0 Verstöße.
- **Soll/Ist:** SOLL: Matrix über alle Mandantentabellen × alle Scope-Arten × CRUD × NULL. IST: ausgeführte PG-Matrix über 123 Tabellen, leere Gap-Liste, im CI-DB-Job verdrahtet. Für die 3 vererbten Tabellen document, maintenance_window und training_assignment werden gescopte Principals jedoch übersprungen (matrix_integration_test.go:255), und genau dort besteht der DB-seitige Cross-Client-Schreib-/Löschdefekt L-T1-04/L-T1-06 unentdeckt
- **Evidenz:** SP/runs/results-backend.tsv (124× pass TestRLSMatrix); SP/work/verify-B08/rlsprobe/strict.log ('row in scope is invisible' = 0); psql vererbte Tabellen document, maintenance_window, training_assignment (restriktive Policies).
- **Abweichung:** -
- **Prüfebenen:** UX: N/A. API: N/A (Kanalmatrix ist WP-208). Daten: OK – echte Policies mit echter App-Rolle je Kommando (Seeding mit session_replication_role=replica). E2E: N/A. Robustheit: OK – Cross-Org, Out-of-Scope, Move-out, NULL-/Global-Claiming; LÜCKE – NULL-Lesesemantik im Repo-Test nur 'maybe', vererbte Tabellen für gescopte Principals übersprungen.
- **Abhängigkeiten:** WP-029, WP-064, WP-040, WP-032, WP-037, WP-038, WP-055, WP-035; WP-208 (Kanalmatrix).
- **Defekte:** F-354 (P2) RLS-Matrix (TestRLSMatrix) überspringt client-, site- und team-gescopte Principals auf den vererbten Tabellen document, maintenance_window und training_assignment und verdeckt damit F-004/F-048
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final TEILWEISE BESTANDEN; BESTANDEN -> TEILWEISE BESTANDEN: AK1 ist für die vererbten Tabellen document, maintenance_window und training_assignment nicht belegt, weil die Matrix gescopte Principals überspringt. Die Ersatztests decken UPDATE/DELETE geteilter oder unverlinkter Zeilen nicht ab, wie die bestätigten Defekte L-T1-04/L-T1-06 zeigen (DB-Probe als App-Rolle: UPDATE 1/DELETE 1 durch Client 1). Neuer Defekt L-T1-34 (P2).
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: NULL-Lesesemantik im Repo-Test pinnen; gescopte Principals auf vererbten Tabellen mit aktiven Triggern; Katalogregel für fehlende Scope-Spalten an Tabellen mit Standort-/CI-Bezug.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – status: „BESTANDEN“ → „TEILWEISE BESTANDEN“. AK1 verlangt eine Matrix über alle Mandantentabellen × Org/Client/Site/Team/Mehrfach × CRUD × NULL. matrix_integration_test.go:255 ('if tbl.inherited && p.scoped() { continue }') überspringt jedoch alle gescopten Principals für document, maintenance_window und training_assignment. Für diese 3 Tabellen ist AK1 damit nicht durch die Matrix belegt. Das Verifier-Argument 'durch andere Tests abgedeckt' ist widerlegt: TestModuleTablesInheritObjectScope prüft nur Lesen und Link-Inserts. Dieselbe Policy lässt den bestätigten DB-Defekt L-T1-04/L-T1-06 zu. Wiederholte rollback-Probe probe_doc_shared_delete.sql auf audit_db_08 als reticora_app mit app.client_scope=c1: 'UPDATE 1', 'DELETE 1', 'client2 asset links 0' (/tmp/claude-0/-home-user-Reticora-CMDB/fbd8a132-b968-552a-84eb-515fd3e8f807/scratchpad/review-t1/probe_doc_shared_delete.out). Die Matrix ist trotzdem grün, das Kriterium also nur teilweise erfüllt.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „-“ → „P2“. Neue Abweichung L-T1-34 (P2), analog zu L-T1-16 (Kanalmatrix-Lücke, die einen realen Defekt verdeckt, P2).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – defekte: „[]“ → „["L-T1-34"]“. Neuer Defekt, siehe defekt_korrekturen L-T1-34.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – soll_ist: „SOLL und IST stimmen überein: erschöpfende, ausgeführte PG-Matrix über alle 123 Mandantentabellen“ → „SOLL: Matrix über alle Mandantentabellen × alle Scope-Arten × CRUD × NULL. IST: ausgeführte PG-Matrix über 123 Tabellen, leere Gap-Liste, im CI-DB-Job verdrahtet. Für die 3 vererbten Tabellen document, maintenance_window und training_assignment werden gescopte Principals jedoch übersprungen (matrix_integration_test.go:255), und genau dort besteht der DB-seitige Cross-Client-Schreib-/Löschdefekt L-T1-04/L-T1-06 unentdeckt“. Die Aussage 'SOLL und IST stimmen überein' und 'erschöpfend' ist für 3 Tabellen falsch (siehe Statuskorrektur).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – status_aenderung: „-“ → „BESTANDEN -> TEILWEISE BESTANDEN: AK1 ist für die vererbten Tabellen document, maintenance_window und training_assignment nicht belegt, weil die Matrix gescopte Principals überspringt. Die Ersatztests decken UPDATE/DELETE geteilter oder unverlinkter Zeilen nicht ab, wie die bestätigten Defekte L-T1-04/L-T1-06 zeigen (DB-Probe als App-Rolle: UPDATE 1/DELETE 1 durch Client 1). Neuer Defekt L-T1-34 (P2).“. Statusregel: nicht jedes verbindliche Kriterium ist durch ausgeführte Evidenz belegt.

### M0.2b Tokens, API-Keys, Rollen, Operator, Entitlements

<a id="wp-066"></a>
#### WP-066 – Session-Token nach AUT-02: 15 min, Claims, kid, Refresh-Cookie, Redis-Widerruf

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** AUT-02, SEC-06
- **Anforderung/Nutzerziel:** 15-min-RS256-Token mit AUT-02-Claims und kid; Nutzer bleiben über ein rotierendes HttpOnly-Refresh-Cookie angemeldet; Widerruf über Redis.
- **Implementierung:** backend/internal/identity/session.go:27; identity/handler.go:200-268 (Refresh, Logout, Cookie); cmd/server/main.go:153-180,447; middleware/openapi.go:233,301; frontend/src/auth/session.ts:16-45, api/client.ts:392-410, auth/authStore.ts:29-71
- **Test(s):** Baseline PASS: TestSessionTokenFollowsAUT02, TestRefreshRotatesAndDetectsReuse, TestLogoutRevokesSession, TestRevocationInRedis, TestSigningKeyRotationOverlap u. a.; Vitest (gemockt) PASS. Real-Stack: 11 PASS (nur mit JSON-Body), 13 FAIL, 14 FAIL, 14b FAIL. Welle: TestT2AuditLogoutThroughProductionChain (Regression PASS); Defekt FAILS_AS_EXPECTED: TestAuditT2CookieOnlyRefreshThroughProductionChain, TestAuditT2RefreshPassesOpenAPIValidation, session.auditdefect.ts, bootRefresh.auditdefect.tsx.
- **Soll/Ist:** Soll: Token-Modell, Cookie-Rotation, Redis-Widerruf, SPA nutzt Cookie-Refresh. Ist: Handlerebene korrekt; der cookie-only Refresh wird vom Validator abgewiesen, abgelaufene Sessions werden beim Boot nicht erneuert, parallele 401 widerrufen die Familie.
- **Evidenz:** SP/work/verify-B09/refresh-curl.log; final-run.log:269 (14), :300-323 (14b); verify-tests-run2 frontend-defect 'expected 3 to be 1', 'expected +0 to be 1'.
- **Abweichung:** L-T2-18 (P1) verletzt Kriterien 2 und 3; L-T2-23, L-T2-24 (P2) verletzen Kriterium 3. Verknüpft, nicht zugerechnet: L-T2-11 (Rollenentzug).
- **Prüfebenen:** UX: FEHLER (Abmeldung nach 15 min, kein Boot-Refresh). API: FEHLER (Refresh 400). Daten: OK (Redis-Blacklist, 2 Replikas). E2E: FEHLER (14, 14b). Robustheit: FEHLER (parallele Refreshes, L-T2-23).
- **Abhängigkeiten:** WP-042, WP-050, WP-009
- **Defekte:** F-017 (P1) Rollenzuweisung nicht entziehbar: kein DELETE /role-assignments/{id}, F-018 (P1) OpenAPI-Validator weist jeden SPA-Refresh (POST /auth/refresh ohne Body/Content-Type) mit 400 ab; gleiche Abweichung bei weiteren bodylosen POSTs, F-065 (P2) Parallele 401 lösen parallele Refreshes aus, Reuse-Erkennung widerruft die Familie, F-066 (P2) Abgelaufene gespeicherte Session führt ohne Refresh-Versuch zu /login
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Router-Refresh-/Logout-Test über Produktionskette PASS; roles-authz 14/14b PASS; Vitest Single-Flight und Boot-Refresh PASS.

<a id="wp-067"></a>
#### WP-067 – API-Keys nach AUT-04: Präfixe, Owner-Rechteschnitt, Verwaltung, Rotation

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** AUT-04, API-05
- **Anforderung/Nutzerziel:** rk_live_/rk_test_-Keys mit Schnittmenge aus Key- und Owner-Rechten; anlegen, listen, widerrufen, rotieren; Lookup ohne Tenant-Kontext nur zur Identifikation.
- **Implementierung:** backend/internal/identity/apikey.go:16-28,169-251; apikey_store.go:180-196,208-245; apikey_handler.go:102-163,145-157,184-229; RLS api_key_system_select
- **Test(s):** Baseline PASS: TestAPIKeyLifecycle, TestAPIKeyStoreScope, TestAPIKeyAuthenticationPopulatesPrincipal. Real-Stack PASS: tenant-isolation T05, roles-authz 06/07. Welle: TestT2AuditAPIKeysThroughProductionChain (Regression PASS); TestAuditT2APIKeyRotateRevokeOnlyByEntitledCaller (FAILS_AS_EXPECTED).
- **Soll/Ist:** Soll: Rechte-Schnittmenge, sichere Verwaltung. Ist: Format, Speicherung, Validierungs-Schnittmenge und Lookup belegt; Rotation/Widerruf ohne Owner-/Rechteprüfung → Übernahme fremder Admin-Keys.
- **Evidenz:** SP/work/verify-B09/probe-v09.log (V1b 201, V1c owner=admin org-wide, V1e 204); verify-tests-run2 t2_apikey_authz_auditdefect_test.go:171; verify-tenant-iso/final.log:8 (T05 ✓).
- **Abweichung:** L-T2-25 (P0, Rechteausweitung/Übernahme fremder Admin-Keys) verletzt Kriterium 2. L-T2-26 (P0, gebundene Keys, Cross-Client) ist ebenfalls betroffen.
- **Prüfebenen:** UX: N/A. API: FEHLER (Rotate/Revoke). Daten: OK. E2E: OK teilweise (Anlage/Nutzung real). Robustheit: FEHLER.
- **Abhängigkeiten:** WP-066, WP-069, RBA-03
- **Defekte:** F-006 (P0) API-Key-Rotation/-Widerruf durch Nicht-Owner: Rechteausweitung und Übernahme fremder Keys, F-007 (P0) API-Key-Bindung an Service-Account ohne Scope-Prüfung: client-gebundener Ersteller erhält org-weiten Key
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT2APIKeyRotateRevokeOnlyByEntitledCaller und TestAuditT2KeyBindingNeverBroadensCreatorScope PASS.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Folgt aus der Korrektur von L-T2-25 und L-T2-26 auf P0.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „L-T2-25 (P1) verletzt Kriterium 2; L-T2-26 (P1, gebundene Keys) ebenfalls betroffen.“ → „L-T2-25 (P0, Rechteausweitung/Übernahme fremder Admin-Keys) verletzt Kriterium 2. L-T2-26 (P0, gebundene Keys, Cross-Client) ist ebenfalls betroffen.“. Prioritätsangaben an die Defektkorrekturen angepasst.

<a id="wp-068"></a>
#### WP-068 – Permission-Katalog: neun fehlende Schlüssel, location:* statt site:*

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** RBA-01
- **Anforderung/Nutzerziel:** Katalog (SQL/Go) enthält exakt die RBA-01-Schlüssel und die Authz nutzt sie.
- **Implementierung:** migrations/000081_permission_catalog_rba01.up.sql; permission/catalog.go:72,95; identity/types.go:19,107; server/authz.go:80-81,139-140,185
- **Test(s):** Baseline PASS: TestCatalogueHoldsTheRBA01Keys, TestSQLCatalogueEqualsGoCatalogue, TestStandardRolesMatchTheRoleMatrix, TestRBA01KeysGateTheirActions. Welle: TestAuditT2RBA01KeysGateCITypesAndNotificationChannels (FAILS_AS_EXPECTED).
- **Soll/Ist:** Soll: Authz nutzt exakt RBA-01. Ist: Katalog vollständig, aber citype:manage und notification:manage sind wirkungslos; ci_type:manage-Duplikat bleibt.
- **Evidenz:** SP/work/verify-B09/probe-v09-rba01.log; verify-tests-run2 t2_auth_chain_auditdefect_test.go:103.
- **Abweichung:** L-T2-27 (P2) verletzt Kriterien 1 und 2.
- **Prüfebenen:** UX: N/A. API: FEHLER. Daten: OK (Migration). E2E: LÜCKE (Custom-Rolle). Robustheit: LÜCKE (kein Key-Usage-Guard).
- **Abhängigkeiten:** WP-046, RBA-03
- **Defekte:** F-067 (P2) Authz ignoriert die RBA-01-Schlüssel citype:manage und notification:manage
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT2RBA01KeysGateCITypesAndNotificationChannels PASS; Guard-Test je RBA-01-Schlüssel.

<a id="wp-069"></a>
#### WP-069 – Service-Accounts mit Rollen und Scopes; Webhook-Subscriptions daran binden

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** RBA-08, API-05
- **Anforderung/Nutzerziel:** Service-Accounts mit Rollen/Scopes; gebundene Keys und Subscriptions erhalten nur Ereignisse, die der Account lesen darf.
- **Implementierung:** Migration 000082; user/service_account.go:77-97,318-346,349-400; user/service_account_handler.go; webhook/dispatcher.go:105-150,256-268; identity/apikey_handler.go:145-157; route_permissions.txt:455,503
- **Test(s):** Baseline PASS: TestServiceAccounts (PG), TestDispatcherFiltersBoundSubscriptions (fake). Welle: TestT2ServiceAccountTenantIsolation (Regression PASS); FAILS_AS_EXPECTED: TestAuditT2KeyBindingNeverBroadensCreatorScope, TestAuditT2ServiceAccountRolesWithinCallerRights, TestAuditT2ServiceAccountRoleAssignmentNeedsRoleManage, TestAuditT2BoundSubscriptionGetsOnlyReadableClientEvents.
- **Soll/Ist:** Soll: SA wie Benutzer, Ereignisfilter nach Leserechten. Ist: Modell/REST funktionieren; Filter leakt scope-lose Events; Bindung und SA-Rollenzuweisung erlauben Rechte-/Scope-Eskalation.
- **Evidenz:** probe-v09.log (V2a 201, V4b 200, V3 ci.merged=1); verify-tests-run2 (t2_apikey_authz_auditdefect_test.go:250/:323, t2_auth_chain_auditdefect_test.go:137, t2_bound_filter_auditdefect_test.go:130).
- **Abweichung:** L-T2-29 (P0, Cross-Client-Exposition) verletzt Kriterium 2 (Filter). L-T2-26 und L-T2-28 (P0) betreffen Rechte- und Scope-Eskalation.
- **Prüfebenen:** UX: N/A. API: FEHLER. Daten: OK (FORCE RLS, FKs). E2E: LÜCKE (kein Outbox→Publisher→HTTP-Test). Robustheit: FEHLER (Filter).
- **Abhängigkeiten:** WP-067, WP-136, RBA-03
- **Defekte:** F-007 (P0) API-Key-Bindung an Service-Account ohne Scope-Prüfung: client-gebundener Ersteller erhält org-weiten Key, F-008 (P0) Service-Account-Rollenzuweisung ohne Rechte-/Scope-Prüfung; Route verlangt nur user:manage, F-009 (P0) Webhook-Filter liefert merge-/relationship-Events anderer Clients an client-gebundene Service-Accounts
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Alle vier Defekttests PASS; Dispatcher-Integration mit echten Payloads.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Folgt aus der Korrektur von L-T2-26, L-T2-28 und L-T2-29 auf P0.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „L-T2-29 (P2) verletzt Kriterium 2 (Filter); L-T2-26, L-T2-28 (P1) Eskalation.“ → „L-T2-29 (P0, Cross-Client-Exposition) verletzt Kriterium 2 (Filter). L-T2-26 und L-T2-28 (P0) betreffen Rechte- und Scope-Eskalation.“. Prioritätsangaben an die Defektkorrekturen angepasst.

<a id="wp-070"></a>
#### WP-070 – Operator-Pfad /admin mit Operator-Token und eigener Audit-Hashkette

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** SEC-07, API-05
- **Anforderung/Nutzerziel:** Operatoren erreichen /api/v1/admin/* nur mit personalisierter MFA-Identität oder Break-Glass-Token; jede Aktion in operator_audit; Break-Glass alarmiert.
- **Implementierung:** backend/internal/operator/auth.go:16-21,33-60,84-121,135-160; operator/handler.go:57-121; identity/oidc.go:269-331; Migration 000083; deploy/keycloak/realm-reticora*.json; deploy/monitoring/*
- **Test(s):** Baseline PASS: TestOperatorLoginRequiresGroupAndMFA (fake IdP), TestOperatorPathAdmitsOperatorsOnly, TestOperatorAuditChain (PG), TestEntryHashCoversEveryField. Real-Stack: admin/me 401; operator_audit-Zeilen 202-204 verkettet. Welle: tests/audit/t2-break-glass-alert.test.sh (FAILS).
- **Soll/Ist:** Soll: personalisierte MFA-Operatoren, alarmiertes Break-Glass, Hashkette. Ist: Pfadisolation und Kette belegt; OIDC-Operatorlogin mit ausgeliefertem Realm unmöglich; Break-Glass nur Log+Zähler.
- **Evidenz:** SP/work/verify-B09/kc/idtoken-claims.txt (kein acr/amr); grep deploy/ ohne operator_break_glass; verify-tests-run2 shell FAILS; results-backend.tsv.
- **Abweichung:** L-T2-30, L-T2-31 (P2) betreffen Kriterien 1 und 2 teilweise; L-T2-32 (P3, Audit nicht atomar).
- **Prüfebenen:** UX: N/A. API: OK. Daten: OK (Hashkette). E2E: FEHLER (OIDC-Operatorlogin). Robustheit: LÜCKE (keine Alarmregel, Audit nicht atomar).
- **Abhängigkeiten:** WP-066; ENT-04 (WP-074)
- **Defekte:** F-068 (P2) Break-Glass-'Alarm' nur Logzeile und Zähler; keine Alarmregel ausgeliefert, F-069 (P2) Personalisierter Operator-OIDC-Login mit ausgeliefertem Realm unmöglich (kein acr/amr, keine Gruppe operators), F-208 (P3) Operator-Aktion kann ohne operator_audit-Eintrag wirksam werden (Audit nicht atomar)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Break-Glass-Shelltest PASS + promtool; e2e-real Operator-Login mit OTP PASS; Audit-Atomizitätstest.

<a id="wp-071"></a>
#### WP-071 – Entitlement-Modell und Featureschlüssel nach ENT-01/ENT-02

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** ENT-01, ENT-02
- **Anforderung/Nutzerziel:** Entitlements je Org mit limits, valid_until, source und den ENT-02-Schlüsseln; cmdb_core immer aktiv; Daten übernommen.
- **Implementierung:** migrations/000084_entitlement_model.up.sql; entitlement/entitlement.go:35-46,78-92,200-219; entitlement/handler.go; middleware.go; frontend/src/hooks/useEntitlements.ts:74-99
- **Test(s):** Baseline PASS: TestEntitlementModel (PG), TestPhase1FeaturesAndQuotas, TestRequiredFeatureMapping, TestProvisionedPlanGrantsOnlyPlanFeatures, TestHandlerGrantListAndCheck; Vitest ENT-02-Schlüssel PASS.
- **Soll/Ist:** Soll: Schema, Schlüssel, Konvertierung, Gates. Ist: erfüllt (PG-Tests, ausgeführte Migrations-SQL); Grant akzeptiert unbekannte Schlüssel.
- **Evidenz:** SP/work/verify-B09/m084-probe.log (Probe-SQL == Migrations-SQL); probe-v09-ent.log E3; Live-Constraints audit_db_05.
- **Abweichung:** P3 L-T2-33 (keine Allowlist beim Grant).
- **Prüfebenen:** UX: OK. API: OK. Daten: OK. E2E: OK (Signup-Zeilen). Robustheit: LÜCKE (L-T2-33).
- **Abhängigkeiten:** WP-012; Basis WP-072/073; Upgrade-Folge in L-T2-36 (WP-072)
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-209 (P3) Entitlement-Grant akzeptiert beliebige Feature- und Quota-Schlüssel
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN; dauerhafter Migrations-Konvertierungstest empfohlen.

<a id="wp-072"></a>
#### WP-072 – Entitlement-Gates lückenlos: Export, Ingest-Alias, GraphQL; kein DefaultPlan-Fallback; Ablauf nach CH21

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** ENT-05, ENT-07, ENT-03, API-04
- **Anforderung/Nutzerziel:** Jeder gegatete REST-/GraphQL-Pfad prüft das passende Feature; fehlende Zeile = nicht berechtigt; nach Ablauf stoppt nur Discovery/Ingest; Banner und Warnungen vor Ablauf.
- **Implementierung:** entitlement/middleware.go:17-171 (keine /racks,/mounts,/rooms); entitlement.go:283-284,368-385,485-513; graphqlbff/resolver.go:85-100,275; cmd/server/main.go:337-343,707-727; events/envelope.go:59 (kein Emitter)
- **Test(s):** Baseline PASS: TestEntitlementEnforcement (PG+Redis, Stub-Handler), TestMiddlewareBlocksUnentitledModule, TestDisabledAndExpiredEntitlementsDeny, TestLicenseExpiryPausesDiscovery, TestEdgeRefusals. Welle: TestT2EntitlementGatesWithoutRows, TestT2EntitlementGatesExpiredDiscovery (Regression PASS); TestAuditT2EntitlementRackViewGated, TestAuditT2EntitlementAPIAccessGatesAPIKeys (FAILS_AS_EXPECTED).
- **Soll/Ist:** Soll: vollständige Durchsetzung, kein Fallback, CH21 mit Banner/Warnungen. Ist: Export/Ingest/Topologie-Gates, kein Fallback, Ablauf und Cache belegt; rack_view (REST+GraphQL), api_access, notifications_email nicht durchgesetzt; ENT-07-Warnungen fehlen.
- **Evidenz:** probe-v09-ent.log E1/E2; verify-tests-run2 t2_entitlement_auditdefect_test.go:51/:104 ('GraphQL rackLayout without rack_view: 200'); grep EntitlementWarning ohne Emitter.
- **Abweichung:** L-T2-34 (P2) verletzt Kriterium 1 (GraphQL rackLayout); L-T2-35 (P2, Funktion fehlt); L-T2-36 (P3).
- **Prüfebenen:** UX: FEHLER (UI blendet aus, API liefert; kein Banner). API: FEHLER. Daten: LÜCKE (Backfill). E2E: LÜCKE (kein realer Ablauf-Lauf). Robustheit: LÜCKE (TTL nicht assertiert).
- **Abhängigkeiten:** WP-071, WP-036, COL-08
- **Defekte:** F-070 (P2) rack_view, api_access und notifications_email serverseitig nicht durchgesetzt (REST und GraphQL), F-071 (P2) ENT-07-Ablaufwarnungen fehlen (Banner, 14/7/1-Tage-Benachrichtigung), F-210 (P3) Entfernen des DefaultPlan-Fallbacks ohne Backfill lässt Orgs mit Teilzeilen ohne Planfeatures
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Beide Gate-Defekttests PASS; notifications_email-Test; ENT-07-Job+Banner mit Test; Router-Gate-Tests mit Review 'ok'.

<a id="wp-073"></a>
#### WP-073 – Limits bei Ingest: unlicensed_ci statt Umgehung; Downgrade ohne Datenverlust

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** ENT-03, ENT-08
- **Anforderung/Nutzerziel:** Über max_cis entdeckte Geräte werden als unlicensed_ci gehalten; Limit-Erhöhung übernimmt sie; Downgrade löscht nichts; Quoten in REST-Schreibpfaden.
- **Implementierung:** Migration 000085; discovery/unlicensed.go:28-60,198-266,300-380; discovery/review.go; edge_ingest.go:74-104; ci/service.go:268-279 (nur max_cis)
- **Test(s):** Baseline PASS: TestUnlicensedCIs (PG), TestAllowCreateEnforcesLimit. Welle: TestT2EdgeIngestUnlicensed (Regression PASS); TestAuditT2EntitlementQuotasEnforced (FAILS_AS_EXPECTED).
- **Soll/Ist:** Soll: ENT-03/08 geschlossen. Ist: unlicensed-Fluss für max_cis vollständig auf PG belegt; max_users/max_api_keys/max_collectors wirkungslos; Benachrichtigung an org_admin nur im Code nachvollzogen.
- **Evidenz:** results-backend.tsv TestUnlicensedCIs; probe-v09-ent.log E4 ([201 201 201]); verify-tests-run2 t2_entitlement_auditdefect_test.go:133.
- **Abweichung:** L-T2-37 (P2): ENT-03-Limitprüfung, die dieses WP schließt, nur für max_cis.
- **Prüfebenen:** UX: LÜCKE (nur gemockte Inbox-Tests). API: OK für max_cis. Daten: OK. E2E: LÜCKE (kein Real-Fluss). Robustheit: FEHLER (Quoten), Race count-then-insert.
- **Abhängigkeiten:** WP-072, WP-058, WP-070
- **Defekte:** F-072 (P2) Nur max_cis durchgesetzt; max_users, max_api_keys, max_collectors wirkungslos
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT2EntitlementQuotasEnforced PASS + max_collectors-Test; org_admin-Benachrichtigung assertiert.

<a id="wp-074"></a>
#### WP-074 – Entitlement-Schreiben nur über Operator (admin-entitlements)

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** ENT-04, API-05
- **Anforderung/Nutzerziel:** Tenants lesen Entitlements nur; nur der Operator schreibt unter /admin/orgs/{id}/entitlements, auditiert.
- **Implementierung:** entitlement/handler.go:23-29; operator/handler.go:48-49; operator/entitlements.go:70-118; operator/auth.go:156-160; permission/catalog.go:59
- **Test(s):** Baseline PASS: TestEntitlementsAreWrittenByOperatorsOnly, TestOperatorEntitlementAudit (PG), TestEveryOperationRequiresItsExpectedPermission. Real-Binary-Probe SPV/server_all.log (405/401/200 + operator_audit-Zeile).
- **Soll/Ist:** Soll: nur Operator schreibt, org_admin liest, jede Änderung auditiert. Ist: auf realer Kette belegt; Audit nicht atomar, toter Schlüssel entitlement:manage.
- **Evidenz:** SPV/server_all.log (POST/PUT 405, Tenant 401, Break-Glass 200, 'entitlement.grant|200|ai'); psql audit_db_13.
- **Abweichung:** P3 L-T2-32 (nur bei Audit-Insert-Fehler), P3 L-T2-38 (toter Schlüssel, keine Route).
- **Prüfebenen:** UX: N/A. API: OK. Daten: OK, LÜCKE toter Schlüssel. E2E: LÜCKE (kein Keycloak-Operatorlogin). Robustheit: LÜCKE (L-T2-32).
- **Abhängigkeiten:** WP-070, WP-071, WP-046
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-208 (P3) Operator-Aktion kann ohne operator_audit-Eintrag wirksam werden (Audit nicht atomar), F-211 (P3) org_admin behält den toten Schlüssel entitlement:manage
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN; Atomizitätstest, Revoke/Guard für entitlement:manage.

<a id="wp-075"></a>
#### WP-075 – CH14: IGA und KI nur als separate Add-on-Entitlements

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** ENT-06, API-06, IGA-01, IGA-02, IGA-03, IGA-04, AI-02
- **Anforderung/Nutzerziel:** Kein Plan enthält IGA/KI implizit; nur eigene Entitlement-Zeilen schalten frei, sonst 403.
- **Implementierung:** entitlement/entitlement.go:66-76,107-120,418-431; middleware.go:56-58; cmd/server/main.go:675; config.go:242; migrations/000086_addon_entitlements.up.sql
- **Test(s):** Baseline PASS: TestAddonsNeedTheirOwnEntitlement, TestPhase1FeaturesAndQuotas, TestEntitlementModel (PG). Real-Binary-Probe (Enterprise-Org: iga/ai 403, nach Grant ai 503 air-gapped, iga 403).
- **Soll/Ist:** Soll: Add-ons nur separat. Ist: erfüllt (Unit + reale PG-Kette).
- **Evidenz:** SPV/server_all.log; git show 3092583 (planFeatures).
- **Abweichung:** P3 L-T2-39 (Datenkorrektur 000086 unvollständig, nur Zwischenbuilds).
- **Prüfebenen:** UX: OK. API: OK (real). Daten: LÜCKE (L-T2-39). E2E: OK auf API-Ebene. Robustheit: OK.
- **Abhängigkeiten:** WP-071, WP-072, WP-214
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-212 (P3) Datenkorrektur 000086 behält implizit provisionierte Add-on-Zeilen
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN; Full-Chain-Gating-Test committen.

<a id="wp-076"></a>
#### WP-076 – KI-Embeddings nur mit Org-Opt-in und Entitlement; Air-Gap-Abschaltung

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** AI-02
- **Anforderung/Nutzerziel:** CI-Daten erreichen den Embedding-Provider nur mit Opt-in und ai-Add-on; Air-Gap schaltet jeden externen KI-Aufruf ab.
- **Implementierung:** migrations/000087_ai_org_optin; ai/ci_indexer.go:23-70; ai/pg_repository.go:159-167; ai/provider.go:34-41; cmd/server/main.go:328-356; ai/handler.go:60-110, retrieval.go:71-72 (ohne Consent)
- **Test(s):** Baseline PASS: TestCIChunkIndexerEmbedsOnlyWithOptInAndAddon, TestAIOptInIsPerOrganization (PG), TestCIChunkIndexerEmbedsAfterCommit (PG). Real-Binary-Probes (fake LLM). Welle: TestAuditT2AIAskRequiresOrgOptIn, TestAuditT2AIOptInSettableThroughAPI (FAILS_AS_EXPECTED).
- **Soll/Ist:** Soll: keine externe KI-Verarbeitung ohne Opt-in. Ist: drei Kriterien belegt; Ask-Pfad sendet ohne Opt-in Daten an den Provider; Opt-in nur per SQL setzbar.
- **Evidenz:** SPV/server_g1.log (air_gapped=true), SPV/fakellm/calls.log; verify-tests-run2 t2_ai_optin_auditdefect_test.go:141/:223.
- **Abweichung:** L-T2-40, L-T2-41 (P2): AI-02 (von WP-076 geschlossen) verletzt.
- **Prüfebenen:** UX: LÜCKE (kein Opt-in-Schalter). API: FEHLER (Ask). Daten: OK. E2E: OK für Indexer (Probe). Robustheit: OK.
- **Abhängigkeiten:** WP-075, WP-037, WP-214
- **Defekte:** F-073 (P2) KI-Ask-Pfad sendet Mandantendaten ohne Org-Opt-in an den Provider, F-074 (P2) Org-KI-Opt-in über keine API/UI/Operator-Schnittstelle setzbar
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Beide Defekttests PASS; Wiring-/Air-Gap-Tests committen.

### M0.2c Kanonisches Datenmodell (DB-05), VLAN/VRF

<a id="wp-077"></a>
#### WP-077 – DB-05: Asset-Hoheit für Seriennummer, Standort, physischen Lifecycle und Inventardaten

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** DB-05, AST-04
- **Anforderung/Nutzerziel:** Bei Asset-Verknüpfung ist das Asset kanonisch; das CI zeigt die Daten read-only, spiegelt den Standort, direkte Schreibversuche → 422, Serien-Abweichung → serial_mismatch.
- **Implementierung:** backend/migrations/000088_ci_asset_canonical.up.sql:12-100; ci/service.go:289-363; api/openapi.yaml:12972-12990; frontend/src/pages/CIDetailPage.tsx:139-182; CIFormModal.tsx:150,228
- **Test(s):** Baseline pass: TestAssetIsCanonicalForLinkedCIs (PG); Real-Binary-HTTP-Probe; Overlay-Probe. Kein Vitest zur Asset-Karte.
- **Soll/Ist:** Backend vollständig (PG + HTTP Real-Binary); UI-Teil von Kriterium 1 ohne Evidenz und mit Mängeln; Verknüpfung mit standortlosem Asset löscht Standort.
- **Evidenz:** server_all3.log PATCH location 422, serial 422, serial_mismatch open=1; Overlay 'after=||'
- **Abweichung:** P3 L-T3-41, L-T3-42, L-T3-43.
- **Prüfebenen:** UX LÜCKE (UUID, fehlende Felder, Serial editierbar); API OK; Daten LÜCKE (stiller Standortverlust); E2E LÜCKE; Robustheit OK teilweise
- **Abhängigkeiten:** WP-054, WP-060, WP-078
- **Defekte:** F-237 (P3) CI-Detailseite zeigt Standort/Site/Raum als rohe UUIDs und Zeitstempel unformatiert, F-238 (P3) Asset-Karte ohne RFID/Kaufdatum/Rechnungsnummer; Seriennummer bei verknüpftem CI editierbar (422 erst nach Speichern), F-239 (P3) Verknüpfen mit standortlosem Asset löscht still Standort, Site und Raum des CI
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Vitest Asset-Karte/Formular; committeter PG-HTTP-422-Test; Entscheidung + Test für standortloses Asset; e2e-real CI-Detail.

<a id="wp-078"></a>
#### WP-078 – DB-05/RCK-03: Rack-Platzierung setzt Standort; Historie und Audit

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** RCK-03, DB-05
- **Anforderung/Nutzerziel:** Mount/Umpositionieren setzt den Standort auf das Rack, Unmount auf den Raum; jede Änderung mit ci_change und Audit in derselben Transaktion.
- **Implementierung:** backend/internal/rack/placement.go:29-111; rack/pg_repository.go:278-308; 000088:71-81 trg_asset_propagate_location
- **Test(s):** Baseline pass: TestMountSetsLocationWithHistory (PG), TestMountFitAndOverlap, TestRackTenantIsolation. Real-Stack: L06 pass, L08 funktional pass (Fail nur mounts:null, L-T3-57). Overlay-Probe (Atomarität, Linked-Unmount, Colocation).
- **Soll/Ist:** Hauptpfade inkl. Real-Stack belegt; Standortänderung von Geschwister-CIs ohne Historie (Kriterium 2 'jede Änderung'), keine Versionserhöhung.
- **Evidenz:** L06 pass; L08 ci_change/Audit/History-Assertionen pass; Probe 'y.ci_change(location)=0 y.audit=0', 'version before=1 after=1'
- **Abweichung:** P3 L-T3-44 (innerhalb Kriterium 2), P3 L-T3-45.
- **Prüfebenen:** UX OK (L06 Rack-Seite nach Reload); API OK; Daten LÜCKE (Geschwister-CIs, Version); E2E OK (L06/L08 funktional); Robustheit OK (Probe), LÜCKE committete Tests
- **Abhängigkeiten:** WP-077, WP-054, WP-079, WP-175
- **Defekte:** F-240 (P3) Standortänderung von Geschwister-CIs eines gemeinsamen Assets ohne ci_change/Audit, F-241 (P3) Rack-Platzierung ändert ci.location_id ohne Versionserhöhung (ETag)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Fix + PG-Test Geschwister-CI-Historie; committete Tests Atomarität/Linked-Unmount/Colocation.

<a id="wp-079"></a>
#### WP-079 – DB-05: Enthaltensein über ci.parent_ci_id; Strukturbeziehungen nur projiziert

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** DB-05, REL-01, AST-05
- **Anforderung/Nutzerziel:** Physisches Enthaltensein wird einmal als parent_ci_id mit Zyklenschutz gespeichert; strukturelle Beziehungen sind nur Projektionen (422), Topologie/Impact lesen sie.
- **Implementierung:** backend/migrations/000089_ci_parent_projection.up.sql:14-83; relationship/relationship.go:539; relationship/pg_repository.go:74-122,354ff; savedview/filter.go:116-121; frontend CIRelationshipsCard.tsx:34-47
- **Test(s):** Baseline pass: TestStructureIsProjected, TestMigration89MovesContainment (PG). Defekttest TestAuditT3ParentMustBeVisibleToScopedPrincipal fails-as-expected. Probes mig89-cycle, savedview.
- **Soll/Ist:** Drei Kriterien im Backend PG-belegt; Client-übergreifender Parent möglich (P2), UI bietet nicht speicherbare Typen, Migration ohne Zyklusschutz, Saved Views ignorieren Projektion.
- **Evidenz:** Defekttest 'stored parent_ci_id = …1004'; probe_savedview 'projection 1, filter 0'; mig89 '2-cycle persisted'
- **Abweichung:** P2 L-T3-13; P3 L-T3-46, L-T3-47, L-T3-48.
- **Prüfebenen:** UX FEHLER gering (tote Typoptionen); API OK; Daten FEHLER (Cross-Client-Parent), LÜCKE (Legacy-Zyklen, Saved Views); E2E LÜCKE; Robustheit LÜCKE (Nebenläufigkeit Zyklus)
- **Abhängigkeiten:** WP-078, WP-031, WP-127/WP-176, TEN-05
- **Defekte:** F-086 (P2) Client-gescopter Principal kann parent_ci_id auf ein CI eines anderen Clients setzen, F-242 (P3) Beziehungsformular bietet strukturelle Typen mounted_in/located_in an (immer 422), F-243 (P3) Migration 000089 übernimmt zyklische Alt-contains-Zeilen als parent_ci_id-Zyklus, F-244 (P3) Saved-View-Beziehungsfilter sehen projiziertes Enthaltensein nicht
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT3ParentMustBeVisibleToScopedPrincipal PASS; Vitest Typoptionen; Migrations-Zyklusschutz; Saved-View-PG-Test.

<a id="wp-080"></a>
#### WP-080 – DB-05: Software-Lifecycle nur für Typen mit Lifecycle-Modell; Health persistiert und berechnet

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** DB-05
- **Anforderung/Nutzerziel:** lifecycle_state nur bei Typen mit Lifecycle-Modell; ci.health read-only und aus Monitoring berechnet.
- **Implementierung:** backend/migrations/000090_ci_health_lifecycle.up.sql:10-102; lifecycle/handler.go:157; cmd/server/main.go:606-619; server/modules.go:47-49
- **Test(s):** Baseline pass: TestLifecycleModelAndHealth (PG), TestG1DefaultSwitchesLateModulesOff. Real-Binary-Läufe G1/alerting. Defekttest TestAuditT3HealthRefreshRunsInG1 fails-as-expected.
- **Soll/Ist:** Lifecycle-Teil erfüllt (PG + HTTP 422); Health wird in der G1-Standardkonfiguration nie berechnet.
- **Evidenz:** SPV/server_g1.log 'worker disabled … monitoring', health=unknown nach 75 s; Kontrolle alerting → ok
- **Abweichung:** P2 L-T3-14.
- **Prüfebenen:** UX LÜCKE (Badge immer unknown, ungetestet); API OK; Daten FEHLER (health nie geschrieben); E2E FEHLER (Real-Binary); Robustheit LÜCKE
- **Abhängigkeiten:** WP-040, WP-077, WP-214, MON-02
- **Defekte:** F-087 (P2) ci.health wird in der G1-Standardkonfiguration nie berechnet (immer 'unknown')
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT3HealthRefreshRunsInG1 PASS + PG-Assertion health='ok'; HTTP-422-Lifecycle-Test; Vitest Health-Badge.

<a id="wp-081"></a>
#### WP-081 – VLAN-Katalog (NET-08) und VLAN-Bezüge von Subnetz und Interface

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** NET-08, DB-05, NET-01, NET-02
- **Anforderung/Nutzerziel:** VLANs als Mandantenkatalog mit Eindeutigkeit nach NET-08 und RLS; Subnetze/Interfaces referenzieren per FK, Altwerte migriert.
- **Implementierung:** backend/migrations/000091_vlan_catalog.up.sql:11-108; ipam/vlan.go, vlan_handler.go; ipam/pg_repository.go:135-138,172,428,481
- **Test(s):** Baseline pass: TestVLANCatalog, TestMigration91ConvertsVLANNumbers (PG), TestVLANResourceCRUD. Defekttest TestAuditT3VLANReferenceMustBeVisible fails-as-expected.
- **Soll/Ist:** Tabelle, Indizes, RLS und Migration belegt; Referenzen auf VLANs fremder Clients möglich.
- **Evidenz:** probe_vlan_xclient_v.sql; Defekttest 'CreateSubnet with the client-2 VLAN: err=<nil>'
- **Abweichung:** P2 L-T3-15.
- **Prüfebenen:** UX N/A; API OK; Daten FEHLER (Cross-Client-Referenz); E2E N/A; Robustheit LÜCKE (PG-HTTP-403/404)
- **Abhängigkeiten:** WP-027, WP-013, WP-082
- **Defekte:** F-088 (P2) Subnetze und Interfaces können VLANs eines anderen Clients referenzieren
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT3VLANReferenceMustBeVisible PASS; RLS-Schreibpfadtest; PG-HTTP-Test /vlans.

<a id="wp-082"></a>
#### WP-082 – VRF-Modell (CH10): Default-VRF je Org, Subnetz-/IP-Eindeutigkeit pro VRF

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** TEN-10, NET-02, NET-03
- **Anforderung/Nutzerziel:** Jede Org hat genau eine Default-VRF 'global'; VRFs nach TEN-05 geschützt, Subnetz/IP-Eindeutigkeit je VRF, Schreiben mit vrf:manage.
- **Implementierung:** backend/migrations/000092_vrf_model.up.sql:13-135 (nur vrf_isolation); ipam/vrf.go:93-123; permission/catalog.go:53; authz.go:88,137
- **Test(s):** Baseline pass: TestVRFModel (PG), TestVRFResourceCRUD, TestIPAddressesFollowNET03, Authz-Subtests. Defekttest TestAuditT3VRFWritesNeedOrgScope fails-as-expected.
- **Soll/Ist:** Modell, Default, Backfill, Eindeutigkeit, Permission belegt; RLS ohne TEN-05-Client-Bedingung: Client-Nutzer ändern org-weite VRFs.
- **Evidenz:** pg_policies nur vrf_isolation; Defekttest 'UpdateVRF of the default VRF … renamed-by-client1'
- **Abweichung:** P2 L-T3-16 (Teil von Kriterium 1).
- **Prüfebenen:** UX N/A; API OK, LÜCKE Client-Scope; Daten FEHLER (TEN-05); E2E N/A; Robustheit LÜCKE
- **Abhängigkeiten:** WP-081, WP-065, WP-125
- **Defekte:** F-089 (P2) Client-gescopte Principals können org-weite VRFs anlegen, umbenennen (auch 'global') und löschen
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT3VRFWritesNeedOrgScope PASS mit OrgCtx-Positivfall.

### M0.2d Ingest und Reconciliation

<a id="wp-083"></a>
#### WP-083 – DeviceRecord-Vertrag zwischen Collector und Server

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** DIS-05, GLO-12
- **Anforderung/Nutzerziel:** Collector und Server teilen einen versionierten DIS-05-Vertrag; alter Vertrag nur übergangsweise mit Deprecation-Header.
- **Implementierung:** api/devicerecord.schema.json; collector/plugins/devicerecord.go:16,155; backend/internal/discovery/devicerecord.go:19-128; api/openapi.yaml:1674-1725
- **Test(s):** Pass: TestCollectorOutputConformsToDeviceRecordSchema, TestDeviceRecordGoTypeMatchesSchema, TestIngestAcceptsDeviceRecords, TestIngestToWebhook (PG). Verifier-Full-Router-Probe. Real-Stack D01 pass (echter Collector → 30 CIs in PG).
- **Soll/Ist:** Alle drei Kriterien erfüllt und ausgeführt belegt.
- **Evidenz:** server-full.log 202/Deprecation/422/403; discovery.log 413; D01 pass
- **Abweichung:** -
- **Prüfebenen:** UX N/A; API OK; Daten OK; E2E OK (D01); Robustheit LÜCKE (Server prüft client_id/scope_id nicht; OpenAPI-Middleware puffert Rohbodies ohne Limit – Codebeobachtung)
- **Abhängigkeiten:** WP-062, WP-013
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Empfohlen: 413-Test über Full Router, Scope/Client-Konsistenztest.

<a id="wp-084"></a>
#### WP-084 – Quellzeit observed_at: Zukunftsgrenze, Ordnung, feldweise Provenienz

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** GLO-12, REC-05
- **Anforderung/Nutzerziel:** Abgleich nach Quellzeit; Zukunfts-Records abgewiesen und gezählt; feldweise Provenienz mit Rang und observed_at.
- **Implementierung:** backend/internal/discovery/reconciliation.go:17-48; unlicensed.go:48-53,118-119; override/decide.go; Migration 000093, 000099
- **Test(s):** Pass: TestIngestComparesSourceTimes (PG), TestAdmitObservedAt, TestDecideComparesObservedAtPerField, TestProvenanceAgainstPostgres. Real-Stack D02 pass.
- **Soll/Ist:** Drei Kriterien PG-belegt; last_seen_at/first_seen_at noch aus Ankunftszeit (GLO-12-Rest, außerhalb Kriterien).
- **Evidenz:** results-backend.tsv; discovery.log 'last_seen_at age=0s status=active'
- **Abweichung:** P3 L-T3-49 (außerhalb der Kriterien).
- **Prüfebenen:** UX N/A; API OK; Daten OK, LÜCKE gering (last_seen_at); E2E OK (D02); Robustheit OK
- **Abhängigkeiten:** WP-083, WP-058
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-245 (P3) last_seen_at/first_seen_at aus Ankunftszeit statt observed_at (GLO-12-Rest)
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Für vollständige GLO-12-Schließung: PG-Test last_seen_at/first_seen_at aus observed_at.

<a id="wp-085"></a>
#### WP-085 – Ingest-Pipeline: ingest.>-Konsument, record_id-Idempotenz 48 h, Ordnung je CI

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** REC-01, NFR-03
- **Anforderung/Nutzerziel:** Records laufen über den NATS-Konsumenten im Mandantenkontext, jede record_id einmal in 48 h, seriell je CI; jeder Record wird verarbeitet.
- **Implementierung:** backend/internal/discovery/consumer.go:102-201,934-993; discovery.go:1000-1080; Migration 000094
- **Test(s):** Pass: TestIngestRecordLedger (NATS+PG), TestIngestLanes, TestIngestPipelineDedupesAndOrders, TestConsumeBatchAcksSettledRecords. Real-Stack D01/D08 pass. Last: ingest p95 43 ms. Defekttest TestAuditT3PoisonRecordDoesNotTakeValidRecordsDown fails-as-expected.
- **Soll/Ist:** Konsument, Ledger, Ordnung belegt; ein fehlerhafter Record verwirft gültige Records derselben Spur.
- **Evidenz:** discovery.log 'GOOD-E2-2 … term; CI rows=0'; Defekttest-Ausgabe
- **Abweichung:** P2 L-T3-17 (Teil von Kriterium 1).
- **Prüfebenen:** UX N/A; API OK; Daten FEHLER (Verlust nach 202); E2E OK (D01/D08); Robustheit FEHLER (Poison-Record)
- **Abhängigkeiten:** WP-084, WP-333/334
- **Defekte:** F-090 (P2) Ein fehlschlagender Record verwirft (Queue) bzw. weist ab (HTTP) die gültigen Records derselben Gruppe/Spur
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT3PoisonRecordDoesNotTakeValidRecordsDown PASS.

<a id="wp-086"></a>
#### WP-086 – Identitätsabgleich: Qualifikationen, Blocklisten, Clientbindung, Hostname → Review, voller Suchhorizont

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** REC-02, NFR-01, CH29
- **Anforderung/Nutzerziel:** Geräte werden nach REC-02 im Client über den gesamten Bestand abgeglichen; Hostname nur als Review.
- **Implementierung:** backend/internal/discovery/identity.go:86-182; pg_repository.go:912-961
- **Test(s):** Pass: TestResolveIdentity, TestIdentityOverWholeInventory (PG, 10.050 CIs), TestIdentityHitsMatchSingle; Verifier-PG-Probes (VRF, ambiguous MAC, Platzhalter).
- **Soll/Ist:** Drei Kriterien belegt; Groß-/Kleinschreibung der Seriennummer inkonsistent (nicht in REC-02 gefordert).
- **Evidenz:** discovery-identity.log; discovery-serial.log 'live CIs by serial (any case)=2'
- **Abweichung:** P3 L-T3-50 (außerhalb der Kriterien).
- **Prüfebenen:** UX N/A; API OK; Daten LÜCKE gering (Serial-Case); E2E N/A; Robustheit OK
- **Abhängigkeiten:** WP-085, WP-082
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-246 (P3) Seriennummer: ResolveIdentity case-insensitiv, PG-Kandidatensuche case-sensitiv
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Für P3: Normalisierung + PG-Test; Repo-Tests für ambiguous_identity/VRF.

<a id="wp-087"></a>
#### WP-087 – Soft-gelöschte CIs im Abgleich: resurrected_device statt Duplikat

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** CI-13
- **Anforderung/Nutzerziel:** Ein wieder auftauchendes gelöschtes Gerät wird kein Duplikat; Review mit Reaktivieren oder neuem CI (CI-13).
- **Implementierung:** backend/internal/discovery/identity.go:182,838-886; discovery/review.go:164-187 ActionNew
- **Test(s):** Pass: TestDeletedDeviceSeenAgainIsReviewed, TestResurrectedDeviceIsReactivatedOrNew (PG). Defekttest TestAuditT3ResurrectedNewKeepsClientAndIdentity fails-as-expected.
- **Soll/Ist:** Erkennung/Reaktivierung erfüllt; Auflösung „neues CI“ (CI-13) erzeugt CI ohne Client/Identität: fremde Sichtbarkeit, Endlosschleife.
- **Evidenz:** discovery.log 'client_id=<NULL> primary_mac=<NULL>', 'readable by a client-2 principal: err=<nil>'; Defekttest-Ausgabe
- **Abweichung:** P1 L-T3-03.
- **Prüfebenen:** UX LÜCKE (Inbox nur Mock); API FEHLER ('new'); Daten FEHLER (Cross-Client); E2E LÜCKE; Robustheit FEHLER (Review-Schleife)
- **Abhängigkeiten:** WP-086, WP-088
- **Defekte:** F-022 (P1) Auflösung „new“ bei resurrected_device erzeugt org-weites CI ohne Identität: fremde Client-Sichtbarkeit und endlose Review-Schleife
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT3ResurrectedNewKeepsClientAndIdentity PASS; Client-Isolationstest der Auflösung.

<a id="wp-088"></a>
#### WP-088 – Review-Modell: zehn Typen, Aktionen merge/new/dismiss/accept, review:resolve, Eskalation

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** REC-08, API-05
- **Anforderung/Nutzerziel:** Eine Review-Queue mit REC-08-Typen und Aktionen unter review:resolve, dedupliziert, Eskalation >14 Tage im Dashboard, Benachrichtigung.
- **Implementierung:** backend/internal/reviews/model.go:142, service.go:89-101, handler.go:29-35,101-103, pg_repository.go:161-190; Migration 000097; frontend DashboardPage.tsx (ohne Eskalation)
- **Test(s):** Pass: TestReviewModel, TestReviewRepositoryOnPostgreSQL, TestTST01ReviewResolution; neu TestAuditT3ReviewResolveRequiresPermission pass (final regression). Real-Stack D05/D06 pass. Defekttest TestAuditT3ResolvedByIsThePrincipal fails-as-expected.
- **Soll/Ist:** Modell, Dedup, Pfad, Hook und 403 belegt; resolved_by fälschbar, Dashboard-Eskalation fehlt.
- **Evidenz:** server-full.log 403 und 'stored resolved_by=…1002'; D05 resolved_by=user; grep DashboardPage
- **Abweichung:** P2 L-T3-18; P3 L-T3-51 (Kriterium 3 teilweise).
- **Prüfebenen:** UX LÜCKE (keine Dashboard-Eskalation); API FEHLER (resolved_by-Spoof), OK 403; Daten FEHLER (Audit-Actor); E2E OK (D05/D06); Robustheit FEHLER (Attribution)
- **Abhängigkeiten:** WP-087, WP-196, WP-140
- **Defekte:** F-091 (P2) resolved_by und unlicensed_ci-Audit-Actor per Request-Body fälschbar, F-247 (P3) REC-08-Eskalation (>14 Tage) wird im Dashboard nicht angezeigt
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT3ResolvedByIsThePrincipal PASS; Dashboard-Widget mit Vitest/e2e-real oder formelle Umverteilung.

<a id="wp-089"></a>
#### WP-089 – Konfliktwerte erhalten; override_conflict-Lebenszyklus (accept/dismiss)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** REC-06, REC-12
- **Anforderung/Nutzerziel:** Reviewer sehen unveränderte Eingangswerte; ein override_conflict je Feld; accept übernimmt, dismiss unterdrückt bis zur nächsten Änderung.
- **Implementierung:** backend/internal/discovery/discovery.go:607-631,1388-1410; reviews/service.go:89-101; discovery/review.go resolveOverrideConflict
- **Test(s):** Pass: TestOverrideConflictOnPostgreSQL, TestOverrideConflictLifecycle, TestConflictingValuesKeepTheReportedValue. Real-Stack pass: D04, D05, D06, T03, T08. Verifier-Probe Suppression S1/S2.
- **Soll/Ist:** Direkte Abfolge auf PG und Real-Stack belegt; Unterdrückung endet nicht bei zwischenzeitlichem Wertwechsel; Workflow/Agent erzeugen keine Konflikt-Reviews.
- **Evidenz:** reviews.log 'S1 … reviews=0', Kontrolle seen-9 reviews=1; D05/D06 pass
- **Abweichung:** P3 L-T3-52 (innerhalb Kriterium 2); P3 L-T3-35.
- **Prüfebenen:** UX OK (D04-D06 echte Inbox); API OK; Daten FEHLER gering (Suppression); E2E OK; Robustheit FEHLER gering
- **Abhängigkeiten:** WP-088, WP-084, WP-196
- **Defekte:** F-231 (P3) Override-Konflikte aus Workflow und Agent erzeugen kein override_conflict-Review (REC-12), F-248 (P3) Verworfener override_conflict-Wert bleibt nach Wertwechsel unterdrückt
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** PG-Test S1/S2 (neues Item nach Wertwechsel) plus direkter Wiederholungsfall unterdrückt.

<a id="wp-090"></a>
#### WP-090 – last_seen_at ohne Historie; unknown→active; Interface/IP-Entfernung nach drei Fehlbeobachtungen

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** REC-07, AUD-03, NET-01
- **Anforderung/Nutzerziel:** Sichtungen aktualisieren last_seen_at ohne Historie, holen unknown-CIs zurück, entfernen fehlende Interfaces/IPs nach drei Läufen mit Historie.
- **Implementierung:** backend/internal/discovery/discovery.go:1413-1420; reconciliation.go syncInterfaces; ipam/pg_repository.go:578-650; Migration 000098
- **Test(s):** Pass: TestInterfacesMissedThreeTimesAreRemoved (PG), TestSightingUpdatesLastSeenAndInterfaces; Verifier-Probe Reset; Real-Stack D04 pass.
- **Soll/Ist:** Alle drei Kriterien PG-belegt; manuelle Einträge werden mitgelöscht (Spec unklar, außerhalb Kriterien).
- **Evidenz:** miss_count_integration_test.go:88-139; discovery.log 'reserved … rows left=0'
- **Abweichung:** P3 L-T3-53 (Produktentscheidung offen).
- **Prüfebenen:** UX N/A; API N/A; Daten OK, Risiko manuelle Einträge; E2E N/A; Robustheit OK
- **Abhängigkeiten:** WP-085, WP-171
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-249 (P3) Manuell gepflegte IPs (reserved/static) und Interfaces an entdeckten CIs nach drei Läufen gelöscht
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Reset-Repo-Test; Entscheidung + Test zu reserved/static-Einträgen.

<a id="wp-091"></a>
#### WP-091 – Reservierte Namensräume und CI-10-Speichermodell

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** GLO-13, CI-10
- **Anforderung/Nutzerziel:** Die vier reservierten Namen sind auf keinem Schreibpfad als Attributschlüssel/Feldname nutzbar; CI-10-Speichermodell, Altdaten migriert.
- **Implementierung:** backend/internal/ci/validation.go:155-244; override/pg_repository.go:327,613; override/handler.go:67-127; Migration 000099
- **Test(s):** Pass: TestReservedNamespaces, TestAttributeNamespacesOnPostgreSQL; Verifier mig099-Probe. Defekttest TestAuditT3OverrideRefusesReservedNames fails-as-expected.
- **Soll/Ist:** Create/PATCH/Ingest, Speichermodell und Migration korrekt; Override-Endpunkt akzeptiert reservierte Namen und zerstört _instance.
- **Evidenz:** server-full.log 'PUT override _instance: 200', 'DELETE … _instance: "x"'; Defekttest-Ausgabe
- **Abweichung:** P2 L-T3-19.
- **Prüfebenen:** UX N/A; API FEHLER (Override-Endpunkt); Daten FEHLER (Instanzwerte verloren); E2E N/A; Robustheit LÜCKE
- **Abhängigkeiten:** WP-089, WP-056
- **Defekte:** F-092 (P2) Override-Endpunkt akzeptiert reservierte Namensräume; _instance-Werte dauerhaft zerstört
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT3OverrideRefusesReservedNames PASS; DB-Constraint; Repo-Datentest 000099.

### Epic A – Installation und Betrieb

<a id="wp-092"></a>
#### WP-092 – Install-Smoke-Matrix Ubuntu 22.04/24.04 und Debian 12; strikte Health-Auswertung

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** INS-01, GATE-03, TEC-09
- **Anforderung/Nutzerziel:** CI belegt auf jeder CH22-Plattform, dass der Cloud-Installer einen vollständig gesunden Stack hochfährt; die Health-Auswertung scheitert bei Compose-/JSON-Fehlern und fehlenden/beendeten/ungesunden Containern aller erwarteten Dienste.
- **Implementierung:** .github/workflows/install-smoke.yml:8-41 (push tags 'v*' :13, workflow_dispatch :41), :73-119 Matrix (ubuntu-22.04/24.04 nativ, debian-12 per docker run debian:12 nach E-27), :121-124 strikter Health-Schritt; tests/install-smoke-health.sh:37-50 (erwartete Dienste = compose config --services, certbot nur mit TLS-Domain), :52-62 (ps-/JSON-Fehler), :66-82 (je Dienst missing / state!=running / health!=healthy → FAIL; nur 'first' Container je Dienst, :67).
- **Test(s):** tests/install-smoke-health.test.sh 12/12 ok (SP/runs/sh-install-smoke-health.log, Docker gestubbt). Verifier-Real-Compose-Probe SP/work/verify-B12/health/run.sh → run.log: unhealthy rc=1, restarting rc=1, skaliert web=2 mit beendetem Replikat → 'OK' rc=0 (Fehlpass), beendeter One-off-Container neben laufendem Dienst → rc=0 (korrekt). Auditor-Real-Compose-Probe (missing/exited/broken compose rc=1, healthy rc=0) konsistent, nicht protokolliert. Matrixläufe nur in docs/acceptance/g1-installation.md:14-32 behauptet (Runs 38512314064/38513566388); lokaler Install blockiert (SP/runs/install.log INSTALL_EXIT=1). Keine Tests der Welle T5.
- **Soll/Ist:** SOLL: 3-Plattform-Matrix mit strikter Health-Auswertung aller erwarteten Dienste, Trigger für Tags und workflow_dispatch ohne Pfadfilter. IST: Matrix und striktes Skript implementiert; das Skript arbeitet gegen echtes Docker Compose 5.6.0 korrekt bis auf den Skalierungs-Randfall. Ausgeführte Matrixevidenz existiert nur als CI-Behauptung für einen Commit, dessen Installerdateien HEAD entsprechen; lokal durch Netzwerkrichtlinie blockiert.
- **Evidenz:** SP/work/verify-B12/health/run.log; SP/runs/sh-install-smoke-health.log 12/12; install-smoke.yml:13, :41, :79-87, :121-124; git log 1b6671fa..HEAD -- install*.sh deploy/docker-compose backend/Dockerfile frontend/Dockerfile tests/install-smoke-health.sh .github/workflows/install-smoke.yml → leer.
- **Abweichung:** AK1/AK2 (Matrixlauf, Trigger) nur per Konfiguration und unbeobachtbarer CI-Behauptung belegt (technische Blockade: Image-Build durch Netzwerkrichtlinie, dl-cdn.alpinelinux.org/quay.io 403). AK3-Randfall P3: beendetes Replikat eines skalierten Dienstes wird nicht erkannt (L-T5-35). CI deckt den TLS-Domain-Pfad nicht ab und ist daher blind für L-T5-01 bis L-T5-03.
- **Prüfebenen:** UX: N/A (CI/CLI; SERVICE/STATE/HEALTH-Tabelle lesbar); API: N/A; Daten: N/A; E2E: LÜCKE (voller Installer je Plattform nur CI-behauptet; lokal am Image-Build blockiert; keine TLS-Domain-Variante); Robustheit: OK mit Randfall (unhealthy, restarting, missing, exited, broken compose scheitern real; skaliertes Replikat exited → Fehlpass, :67)
- **Abhängigkeiten:** WP-003; speist WP-211/WP-213; TLS-Domain-Smoke-Variante nötig für WP-094/WP-095
- **Defekte:** F-288 (P3) Health-Prüfung wertet nur den ersten Container je Dienst aus; beendetes Replikat eines skalierten Dienstes besteht
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Einsehbare Artefakte eines install-smoke-Laufs mit allen drei Matrixjobs grün auf dem auditierten Commit (oder reproduzierbarer lokaler Lauf, sobald der Image-Build möglich ist); docker-gestützter Health-Skript-Test im Repo inkl. Skalierungsfall; empfohlen TLS-Domain/Pebble-Matrixeintrag.

<a id="wp-093"></a>
#### WP-093 – Installer-Vorprüfung: OS/Release, Docker >= 24, Compose v2, Sizing-Tabelle

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** INS-07, TEC-09
- **Anforderung/Nutzerziel:** Der Installer verweigert nicht unterstützte Plattformen und alte Docker-/Compose-Versionen mit klarer Supportmeldung und warnt bei Unterschreitung der dokumentierten Dimensionierung; eine Sizing-Tabelle bis 50.000 Objekte ist dokumentiert.
- **Implementierung:** install-preflight.sh:15-51 (Plattformen, RETICORA_ALLOW_UNSUPPORTED_PLATFORM), :55-87 (Docker Engine >= 24 über Server.Version, Compose-Major >= 2), :89-131 (Sizing-Warnungen ohne Abbruch, 4 vCPU / 8 GiB / 100 GiB); install.sh:150-174 und :255 (Plattformprüfung vor Download); install-cloud.sh:142-151 (Docker nur über get.docker.com), :205-206, :486-497 (preflight); install-vm.sh:346-375; docs/ops/sizing.md (≤10k/25k/50k); docs/troubleshooting-installation.md:21-29.
- **Test(s):** tests/install-preflight.test.sh 29/29 ok (SP/runs/sh-install-preflight.log): 8 Plattformfälle, Override, fehlendes os-release, Verweigerung vor Download, Docker 23 abgelehnt / 24, 27 akzeptiert / unbekannt abgelehnt, Compose v1 abgelehnt / v2 akzeptiert, Sizing-Warnungen, Doku, nur apt, Preflight-Verdrahtung. Realer Installerlauf SP/runs/install.log:1-7 ('Platform ubuntu:24.04 is supported', 'Docker Engine 29.8.2', 'Docker Compose 5.6.0', Plattenwarnung '23 GiB … minimum is 100 GiB SSD', Installer läuft weiter). Verifier wiederholte Auditor-Probe SP/work/audit-B12/probe-vm/run.sh: centos:9 rc=1 mit Supportmeldung, debian:12 rc=0 mit 3 Sizing-Warnungen. Keine Tests der Welle T5.
- **Soll/Ist:** SOLL: OS-/Docker-/Compose-Prüfung, Sizing-Tabelle und Warnung. IST: implementiert, unit-getestet und in einem realen Installerlauf sowie einer realen install-vm-Preflight-Probe beobachtet.
- **Evidenz:** SP/runs/sh-install-preflight.log 29/29; SP/runs/install.log:1-7; SP/work/audit-B12/probe-vm/run.sh (Ausgabe identisch wiederholt); docs/ops/sizing.md entspricht INS-07.
- **Abweichung:** -
- **Prüfebenen:** UX: OK (Meldungen nennen unterstützte Plattformen, Mindestwerte und Doku; Override dokumentiert); API: N/A; Daten: N/A; E2E: OK (realer Installer passierte Preflight auf ubuntu:24.04; install-vm-Docker-Zweig und Alt-Docker-Negativfall nur gestubbt); Robustheit: OK (altes Docker, Compose v1, unbekannte Versionen, nicht unterstütztes oder fehlendes os-release brechen ab)
- **Abhängigkeiten:** WP-092; E-17 (Skalierungsziel bestätigt)
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Negativ-Verdrahtungstest von check_docker_engine/check_compose_v2 innerhalb von install-cloud.sh preflight() und im install-vm-Docker-Zweig.

<a id="wp-094"></a>
#### WP-094 – TLS/ACME im Installer: Erkennung, Kettenprüfung, funktionsfähiger Standardpfad

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** INS-03, TEC-09
- **Anforderung/Nutzerziel:** Mit TLS-Domain liefert der Installer zuerst ein Bootstrap-Zertifikat, bezieht dann ein vertrauenswürdiges Let's-Encrypt-Zertifikat, prüft dessen Kette, liefert es aus und hält es erneuert; ohne Domain wird keine TLS-Konfiguration eingebunden und der HTTP-Standardpfad funktioniert.
- **Implementierung:** install-cloud.sh:784-790 tls_cert_is_trusted (openssl verify -untrusted), :796-798 tls_cert_is_selfsigned, :803-809 tls_template_source, :815-863 bootstrap_tls_cert (Platzhalter direkt in letsencrypt/live/<domain>/, chmod 644 :862), :876-913 validate_tls_material, :923-961 issue_tls_certificate (compose run --rm --no-deps --entrypoint certbot certbot certonly --webroot, :942-946), Reihenfolge :1657-1661; deploy/docker-compose/docker-compose.yml:370 (frontend user: nginx), :382 (letsencrypt :ro), :388, :415-426 (certbot-renew-Schleife ohne Deploy-Hook); frontend/docker-entrypoint.d/25-reticora-tls-optional.sh; nginx-tls.conf.template:47-48.
- **Test(s):** tests/install-cloud-helpers.test.sh TLS-Fälle ok (36/36, SP/runs/sh-install-cloud-helpers.log), ACME-Test ersetzt certbot aber durch Dateikopie (:199-204). Verifier-Probe SP/work/verify-B12/certbot/cli_probe.py → cli_probe.log mit echter certbot-2.11.0-CLI (nur Account/Order gepatcht): Lauf 1 'CertStorageError: live directory exists', Order verbraucht, renewal-conf 0 Byte; Lauf 2 Lineage -0001, live/<domain>/fullchain.pem bleibt selbstsigniert (openssl verify error 18). uid-101-Leseprobe: Bootstrap-privkey READ_OK, certbot-Dateien 'Permission denied'. Defekttest-Welle T5: tests/audit/install-cloud-acme.test.sh fails-as-expected (Kontrolle 'failed challenge keeps the placeholder' ok, 3 FAIL); in dieser Synthese erneut ausgeführt: '1 passed, 3 failed' (SP/work/synth-T5/acme.log). Hinweis: der Test modelliert certbot-2.11.0-Speichersemantik und Dateimodi, die echte CLI-Evidenz liefert cli_probe.log.
- **Soll/Ist:** SOLL: funktionsfähiger ACME-Pfad mit Kettenprüfung; ohne Domain kein TLS-Template. IST: Kettenprüfung und Template-Auswahl funktionieren. Die ACME-Ausstellung scheitert am Speicherschritt von certbot (vorbelegtes live-Verzeichnis); ausgestellte Dateien wären für nginx (uid 101) nicht lesbar; Erneuerungen werden nie neu geladen. Wiederholungslauf bricht ab.
- **Evidenz:** SP/work/verify-B12/certbot/cli_probe.py und cli_probe.log (Läufe 1 und 2); uid-101-Leseprobe; SP/work/audit-B12/probe-certbot/probe.py (new_lineage direkt); SP/work/synth-T5/acme.log; install-cloud.sh:815-863, :923-961; docker-compose.yml:370, :382, :415-426.
- **Abweichung:** AK2 und AK3 verletzt: ACME-Pfad bei Erstinstallation nicht funktionsfähig, Wiederholung bricht ab (L-T5-01); latent unlesbares Schlüsselmaterial für nginx (L-T5-02); kein Reload nach Erneuerung (L-T5-29, Ursache nicht belegt). Repo-Test maskiert alles per certbot-Stub.
- **Prüfebenen:** UX: LÜCKE (gescheiterte Ausstellung nur als Warnung, Platzhalter bleibt – Browserwarnungen; Wiederholung stirbt in validate_tls_material); API: N/A; Daten: FEHLER (leere renewal/<domain>.conf, verwaiste -0001-Lineage; Schlüsselmaterial 0700/0600 root für uid 101 unlesbar); E2E: FEHLER (Let's-Encrypt-Zertifikat kann weder abgelegt noch ausgeliefert werden – echte certbot-CLI-Probe; HTTP-Standardpfad nur CI-dokumentiert); Robustheit: FEHLER (jede Wiederholung verbraucht eine ACME-Order; kein nginx-Reload nach certbot renew)
- **Abhängigkeiten:** WP-092 (keine TLS-Smoke-Variante); WP-095 (Domain-Pfad bricht noch früher ab); WP-200/WP-211 (Installationsnachweis nur HTTP/IP)
- **Defekte:** F-033 (P1) certbot certonly kann das Let's-Encrypt-Zertifikat nicht ablegen, weil der Bootstrap-Platzhalter letsencrypt/live/<domain>/ belegt, F-034 (P1) Von certbot ausgestellter Schlüssel und Kette sind für den unprivilegierten nginx-Nutzer (uid 101) des Frontends unlesbar, F-139 (P2) Erneuerte Zertifikate werden nie geladen: kein nginx-Reload nach 'certbot renew'
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Behebung von Lineage-/Platzhalter-Behandlung, Dateirechten (Deploy-Hook) und Reload; danach Pebble-Integrationstest (Erstinstallation, Wiederholung ohne neue Order/-0001, erzwungene Erneuerung, Frontend-Neustart als uid 101) mit vertrauenswürdiger Kette aus live/<domain>/; ./tests/audit/install-cloud-acme.test.sh 4/4 ok; archivierte Evidenz für den HTTP-Standardpfad.

<a id="wp-095"></a>
#### WP-095 – Installer: Issuer-Prüfung extern und containerseitig

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** INS-02
- **Anforderung/Nutzerziel:** Der Installer prüft, dass der konfigurierte OIDC-Issuer vom Host und aus dem Backend-Container erreichbar ist und genau den konfigurierten Wert ankündigt, ohne unterstützte Installationen zu brechen; Fehler liefern eine verständliche Meldung mit Verweis auf die Troubleshooting-Doku.
- **Implementierung:** install-cloud.sh:1057-1105 validate_keycloak_bootstrap (Loopback-Ablehnung, lokale Discovery, check_issuer_from_host :1103, check_issuer_from_container :1104), :1140-1159 Host-Prüfung, :1164-1172 Container-Prüfung (compose run … server -check-oidc-issuer); start_stack: up -d keycloak → validate_keycloak_bootstrap (:1385) → create_initial_admin → up -d server frontend (:1389); Default-Issuer ${RETICORA_PUBLIC_BASE_URL}/realms/reticora (:603); /realms/-Proxy frontend/nginx.conf:111, nginx-tls.conf.template:86; backend/internal/identity/oidc.go:437-465 CheckIssuer; docs/troubleshooting-installation.md:110-146.
- **Test(s):** results-backend.tsv: pass backend/internal/identity TestCheckIssuer; install-cloud-helpers 8 'issuer:'-Fälle ok (curl/compose gestubbt); Auditor-Binärproben SP/work/audit-B12/probe-issuer_ok.log, probe-issuer_wrong_realm.log ('HTTP 404'), probe-issuer_down.log ('connection refused'); Verifier-Reihenfolgeprobe SP/work/verify-B12/issuer/order.sh (Harness byte-identisch zu HEAD ohne main): start_stack rc=1, keine 'up -d server frontend'. Defekttest-Welle T5: tests/audit/install-cloud-issuer-order.test.sh fails-as-expected; in dieser Synthese erneut: '2 passed, 1 failed' (SP/work/synth-T5/issuer-order.log; Kontrollen 'unreachable domain issuer aborts with the hint' und 'IP issuer is still checked' ok).
- **Soll/Ist:** SOLL: strikte Issuer-Prüfung von Host und Container, die unterstützte Installationen nicht bricht. IST: Prüflogik korrekt, aber vor dem Frontend-Start platziert; Issuer, die über das Reticora-Frontend bedient werden (Installer-Default und dokumentierte Form für Domains), können nie bestehen – jede Domain-Installation und Wiederholung bricht ab.
- **Evidenz:** SP/work/verify-B12/issuer/order.sh, order.out, order.log; SP/work/audit-B12/probe-issuer*.log; results-backend.tsv TestCheckIssuer pass; SP/work/synth-T5/issuer-order.log; install-cloud.sh:603, :1103-1104, :1385, :1389; docs/troubleshooting-installation.md:132-133.
- **Abweichung:** AK1 verletzt für den Default-/dokumentierten Domain-Issuer https://<domain>/realms/reticora (L-T5-03); Container-Prüfung lokal nur per Stub belegt. AK2 (Fehlermeldung mit Doku-Hinweis) erfüllt.
- **Prüfebenen:** UX: OK (klare Meldungen mit Abhilfe, Loopback-Issuer mit Host-IP-Vorschlag abgelehnt); API: OK (server -check-oidc-issuer exit 0/1 gegen echten Keycloak; CheckIssuer unit-getestet); Daten: N/A; E2E: FEHLER (Domain-Installation bricht an der Host-Prüfung ab, Issuer-Endpunkt startet später :1385 vs :1389; IP-Pfad nur CI-dokumentiert); Robustheit: OK für unreachable/404/anderer Issuer/503/Trailing-Slash, FEHLER bei der Reihenfolge
- **Abhängigkeiten:** WP-094 (TLS-Pfad); WP-097 (server nutzt config.Load für -check-oidc-issuer); CI install-smoke deckt nur den IP-Issuer ab
- **Defekte:** F-035 (P1) OIDC-Issuer-Prüfung läuft vor dem Start des Frontends (Issuer-Proxy), daher brechen Domain-/TLS-Installationen ab
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Strikte Issuer-Prüfungen nach dem Frontend-Start (oder proxyunabhängig); ./tests/audit/install-cloud-issuer-order.test.sh 3/3 ok; install-smoke-TLS-Domain-Variante läuft durch und protokolliert beide erfolgreichen Issuer-Prüfungen ('OIDC issuer is reachable from the backend container').

<a id="wp-096"></a>
#### WP-096 – Wiederholte Installation: Secrets/Keycloak-DB synchron, .env erhalten

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** INS-05
- **Anforderung/Nutzerziel:** Ein erneuter Installerlauf (auch mit rotierten Passwörtern, INS-05) hält Datenbanken und Keycloak über TCP verifiziert konsistent zur .env und führt die .env zusammen, statt Betreiberergänzungen wie RETICORA_HTTP_BIND zu verlieren.
- **Implementierung:** install-cloud.sh:397-462 write_env, :468-483 merge_env_extras, :1272-1276 db_password_matches (psql -h <service> über TCP), :1285-1288 sync_db_password (postgres/reticora, keycloak-db/keycloak), :1291-1351 sync_role_password (temporäre trust-hba-Instanz, ALTER ROLE, TCP-Nachprüfung), :1510-1516 create_initial_admin (kcadm-Login mit KEYCLOAK_ADMIN_PASSWORD aus .env), configure_realm; deploy/docker-compose/docker-compose.yml:217 'start --import-realm'; docs/troubleshooting-installation.md:45, :152, :224-236.
- **Test(s):** install-cloud-helpers 're-run:' 5/5 ok (Merge, TCP-Probe, beide DBs, Reset-Ablauf; Docker gestubbt), test_env_merge_keeps_extra_settings auf echtem write_env/merge_env_extras. Verifier-Real-PG-Probe SP/work/verify-B12/pwsync/run.sh (timescale 2.17.2-pg16): TCP altes PW rc=0 / neues rc=2; Socket mit FALSCHEM PW rc=0; sync_role_password → '✔ Authentication of keycloak verified over TCP'; danach neu rc=0 / alt rc=2. Keycloak-24-Probe SP/work/verify-B12/kcrot/run.log: 'Realm rot already exists. Import skipped', kcadm-Login neues Admin-PW rc=1 / altes rc=0, Client-Secret bleibt SECRET_A. Defekttest-Welle: nicht kodifiziert (benötigt echten Keycloak und Installer-Wiederholung).
- **Soll/Ist:** SOLL: idempotente Wiederholung mit Passwort-Rotation (INS-05, von WP-096 'geschlossen'), DB-Passwort-Sync, .env-Merge. IST: Die expliziten AK (TCP-Probe, Keycloak-DB-Sync, .env-Merge) funktionieren gegen echten Code und echte PostgreSQL. Rotation von Keycloak-Admin-Passwort und OIDC-Client-Secret (und jede Realm-Änderung) wird beim Wiederholungslauf nicht angewendet; ein vollständiger zweiter Installerlauf ist nicht belegt.
- **Evidenz:** SP/work/verify-B12/pwsync/run.sh-Ausgabe; SP/work/verify-B12/kcrot/run.log; install-cloud-helpers 're-run:' 5/5; install-cloud.sh:1272-1351, :468-483, :1510-1516; docker-compose.yml:217-224.
- **Abweichung:** INS-05-Rotation nur für die zwei DB-Rollen umgesetzt: rotiertes KEYCLOAK_ADMIN_PASSWORD lässt create_initial_admin abbrechen, rotiertes Client-Secret/Redirect-URL erreicht Keycloak nie (L-T5-15). Kein End-to-End-Nachweis einer Wiederholungsinstallation (Image-Build blockiert).
- **Prüfebenen:** UX: LÜCKE (Doku verspricht, dass ein Wiederholungslauf Realm-Einstellungen repariert; wirkungslos); API: N/A; Daten: OK für DB-Rollen (echte PG), FEHLER für Keycloak-Admin-Passwort und Client-Secret (Keycloak-24-Probe); E2E: LÜCKE (kein vollständiger zweiter Installerlauf; CI läuft einmal); Robustheit: OK (falsches Passwort über TCP abgelehnt, Passwort nie auf Kommandozeilen); ungetestet: Reset der Haupt-postgres (reticora-postgres-Image)
- **Abhängigkeiten:** WP-095; vollständiger Installationspfad lokal blockiert (Netzwerkrichtlinie)
- **Defekte:** F-125 (P2) Installer-Wiederholung wendet rotiertes Keycloak-Admin-Passwort, OIDC-Client-Secret und andere Realm-Änderungen nicht auf einen bestehenden Keycloak an
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** CI- oder Real-Stack-Wiederholungsjob grün (rotierte DB-Passwörter, RETICORA_HTTP_BIND erhalten, healthy, Login und /readyz 200, dritter Lauf idempotent); Keycloak-Secret-/Realm-Rotation implementiert (oder Rotationsumfang und Doku korrigiert) mit bestandenem Keycloak-24-Integrationstest (neues Admin-PW, Secret = .env, redirectUris aktualisiert, altes Secret 401).

<a id="wp-097"></a>
#### WP-097 – Readiness und fail-closed Startvalidierung

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** OPS-01
- **Anforderung/Nutzerziel:** /readyz meldet den Zustand jeder konfigurierten Abhängigkeit (DB mit Extensions, NATS, Redis, S3, SMTP) mit 503 bei Ausfall; der Server startet nicht bei fehlender Pflichtkonfiguration oder unerreichbaren Pflichtdiensten; Compose/K8s nutzen /readyz und starten.
- **Implementierung:** backend/internal/server/health.go:56-104 readinessHandler, :144-159 DatabaseReadiness, :164-173 TCPReadiness (wählt die Adresse wörtlich); backend/cmd/server/main.go:157-170 Redis fatal, :214-220 DB plus Extensions, :232-245 S3 (TCPReadiness('object_storage', cfg.S3Endpoint) :237), :250-262 NATS (hostPort mit Default 4222), kein SMTP-Check (centralSMTP :871-885 nur Sender); backend/internal/platform/blob/blob.go:168-186 (NewS3Store akzeptiert http(s)://host[:port]); backend/internal/config/config.go:382-387 requiredOutsideDevelopment (Commit 75aa419); api/openapi.yaml:110-137; docker-compose.yml:353-355; deploy/k8s/base/deployment.yaml:93-105.
- **Test(s):** results-backend.tsv pass (internal/server): TestReadyzAllChecksPass, TestReadyzFailingCheckIs503, TestReadyzHangingCheckTimesOut, TestReadyzWithoutChecks, TestReadyzDetails, TestReadyzDetailNotSampledIsQuiet, TestReadyzSlowDetail, TestHealthzStaysLiveness, TestTCPReadiness, TestDatabaseReadiness (echte PG), TestRoutesAndSpecificationAreInParity; internal/config TestValidate* (15 Subtests). Neu (Welle T5, Regression, ungetaggt): TestAuditT5ServerStartupFailClosed (backend/cmd/server/startup_failclosed_integration_test.go) PASS 11/11 im Schreiber-Lauf (9 fail-closed-Fälle exit 1 mit spezifischer Meldung, vollständige Konfiguration /readyz 200, S3-host:port geprüft); mechanische Verifikation: Paket backend/cmd/server untagged PASS (Hinweis: Test überspringt ohne TEST_DATABASE_URL/TEST_REDIS_URL, :135-137). Defekttests (auditdefect): TestAuditT5ReadinessCoversSMTP und TestAuditT5ReadinessObjectStorageURLEndpoint fails-as-expected (mechanisch bestätigt; Kontrolle host:port 200); TestAuditT5K8sRenderedEnvSatisfiesStartCheck in dieser Synthese mit kustomize v5.8.1 erneut ausgeführt: 3/3 Subtests FAIL auf der Assertion (SP/work/synth-T5/k8s-env.log). Verifier: Live-/readyz ohne smtp-Schlüssel bei konfiguriertem SMTP; SP/work/verify-B12/k8s-env-{staging,production}.log; probe-s3-*.log.
- **Soll/Ist:** SOLL: vollständige Readiness inkl. SMTP; fail-closed Start; Deployments nutzen /readyz und starten. IST: fail-closed Start funktioniert (jetzt durch Repo-Regressionstest belegt). Readiness prüft SMTP nicht und wählt URL-förmige/portlose S3-Endpunkte falsch (dauerhaft 503). Der neu verpflichtende OIDC-Issuer macht die ausgelieferten K8s-Manifeste unstartbar.
- **Evidenz:** TestAuditT5ServerStartupFailClosed PASS 11/11; SP/work/synth-T5/k8s-env.log (CrashLoopBackOff-Assertion base/staging/prod); SP/work/verify-B12/probe-s3-localhost_9000.log (200) vs probe-s3-http___localhost_9000.log (503, 'too many colons in address'); Live-curl localhost:8080/readyz ohne smtp mit SP/stack/server.env:21-24; git log -S config.go → 75aa419.
- **Abweichung:** AK1 verletzt: kein SMTP-Readiness-Check (L-T5-16), S3-Readiness falsch für URL-/portlose Endpunkte (L-T5-17). AK3 verletzt: K8s-Manifeste ohne Pflicht-RETICORA_OIDC_ISSUER_URL, Pods beenden sich vor jeder Probe (L-T5-04). AK2 erfüllt.
- **Prüfebenen:** UX: N/A; API: FEHLER (kein SMTP-Check; URL-förmiger S3-Endpunkt → dauerhaft 503; sonst 503 benennt nur die Abhängigkeit, Timeout je Check); Daten: OK (fehlende PostgreSQL-Extensions erkannt und beim Start fatal); E2E: FEHLER (K8s base/staging/prod beenden sich beim Start; URL-förmiger S3-Endpunkt hält den Compose-Server unhealthy, Frontend startet wegen depends_on service_healthy nie); Robustheit: OK fail-closed für Config/Redis/DB/S3/NATS (Repo-Test), realer Redis-Ausfall → 503 bei /healthz 200; NATS/S3 nur TCP
- **Abhängigkeiten:** WP-003; betrifft WP-099 und alle K8s-Deployment-WPs; WP-095 nutzt config.Load für -check-oidc-issuer
- **Defekte:** F-036 (P1) K8s-Manifeste liefern das durch WP-097 verpflichtende RETICORA_OIDC_ISSUER_URL nicht; Server beendet sich in base, staging und prod beim Start, F-126 (P2) /readyz prüft SMTP nicht, obwohl SMTP konfiguriert ist, F-127 (P2) object_storage-Readiness wählt das rohe RETICORA_S3_ENDPOINT; URL-förmige oder portlose Endpunkte, die der S3-Client akzeptiert, halten /readyz dauerhaft auf 503
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** SMTP-Readiness implementiert (TestAuditT5ReadinessCoversSMTP grün: checks.smtp ok, 503 bei geschlossenem Port); S3-Adressnormalisierung (TestAuditT5ReadinessObjectStorageURLEndpoint grün); K8s-Manifeste mit allen Pflichtschlüsseln (TestAuditT5K8sRenderedEnvSatisfiesStartCheck grün) plus CI-Assertion; optional Ausfall-Integrationstest je Abhängigkeit (NATS/S3/DB) mit 503 und Wiederanlauf ohne Prozessneustart.

<a id="wp-098"></a>
#### WP-098 – Unabhängige Betriebsschalter für alle Worker (OPS-02)

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** OPS-02
- **Anforderung/Nutzerziel:** Betreiber können jeden Hintergrund-Worker unabhängig ein- und ausschalten (Konvention RETICORA_WORKER_<NAME>_ENABLED); ein deaktivierter Worker startet nicht.
- **Implementierung:** backend/internal/config/config.go:326-378 (Konvention, WorkerNames ×11, WorkerEnvKey, WorkerEnabled), :289-291 (nur WorkerNames-Schlüssel gelesen); backend/internal/server/workers.go StartWorkers; backend/cmd/server/main.go:521-616 (alle 11 Worker über server.Worker{Name: config.Worker*}, ein StartWorkers-Aufruf); Offline-Detector (WP-157) als Scheduler-Systemjob hinter RETICORA_OFFLINE_DETECTOR_ENABLED (config.go:178-180, :284; main.go:496-499); docker-compose.yml:320-332 und deploy/k8s/base/deployment.yaml:60-80 (11 Schalter); docs/ops/workers.md; docs/README.md:890.
- **Test(s):** results-backend.tsv pass: internal/server TestDisabledWorkerDoesNotStart (Export-Laufzahl 0, Ingest läuft, Started()/Disabled(), unbekannt/doppelt/ohne Run abgelehnt), TestEveryRegisteredWorkerHasASwitch, TestWorkerMetricsAreRegisteredAndRecorded; internal/config TestWorkerSwitches. Verifier mit HEAD-Binary (audit_db_15, Redis db 15, Wegwerf-nats:2.10-alpine -js): alle 11 Schalter false → 11× 'worker disabled', reticora_worker_enabled=0 für alle, 0 ERROR, /readyz 200 (SP/work/verify-B12/probe-workers-all-off.log); Offline-Detector true → scheduler_run-Zeile ci.offline_detector, false → keine, Konventionsname RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED=false → läuft trotzdem ohne Warnung (probe-offline-{true,false,convention}.log). Keine Tests der Welle T5.
- **Soll/Ist:** SOLL: ein unabhängiger Schalter je Worker nach einer Konvention, Registry, Test 'deaktivierter Worker startet nicht'. IST: implementiert und für jeden registrierten Worker mit dem echten Binary belegt. Der später ergänzte Offline-Detector hat einen funktionierenden Schalter außerhalb der Konvention (P3).
- **Evidenz:** SP/work/verify-B12/probe-workers-all-off.log; SP/work/verify-B12/probe-offline-{true,false,convention}.log mit scheduler_run-Abfragen; results-backend.tsv (4 Tests pass); SP/work/audit-B12/probe-workers.log.
- **Abweichung:** P3 (begleitend, Schalter existiert und wirkt): Offline-Detector-Schalter umgeht die WP-098-Konvention, ist in Compose/K8s nicht durchgereicht, docs/ops/workers.md:113 nennt den ignorierten Konventionsnamen (L-T5-36).
- **Prüfebenen:** UX: N/A (Betreiber-Env und Doku); API: OK (reticora_worker_enabled spiegelt alle 11 Schalter, echtes Binary); Daten: OK (Offline-Detector-Schalter über scheduler_run belegt); E2E: OK (alle 11 Worker plus Offline-Detector mit echtem Binary); Robustheit: OK (ungültiger Wert scheitert in Validate; unbekannter/doppelter Worker abgelehnt), LÜCKE (Konventionsname für Offline-Detector stillschweigend ignoriert)
- **Abhängigkeiten:** WP-097, WP-022; WP-157 (Offline-Detector), WP-132 (Audit-Runner), WP-214 (Modul-Gating)
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-289 (P3) Offline-Detector-Schalter umgeht die Worker-Konvention; dokumentierter Konventionsname wird stillschweigend ignoriert, Schalter in Compose/K8s nicht setzbar
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Paritätstest Compose/K8s ↔ WorkerEnvKey, Angleichung des Offline-Detector-Schalters an die Konvention, Warnung bei unbekannten RETICORA_WORKER_*_ENABLED.

<a id="wp-099"></a>
#### WP-099 – Kubernetes: Pflicht-Master-Key injizieren

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** OPS-03
- **Anforderung/Nutzerziel:** Kubernetes-Deployments (base, staging, prod) injizieren den Pflicht-Master-Key aus einem Secret, Pods starten nur mit Key, die kustomize-Builds bleiben in CI grün und die ausgerollten Pods starten tatsächlich.
- **Implementierung:** deploy/k8s/base/deployment.yaml:48-56 (RETICORA_MASTER_KEY aus secretKeyRef reticora-secrets/master-key, optional: false); deploy/k8s/overlays/staging/kustomization.yaml:47-54 (wiederholt, da env-Liste ersetzt); prod erbt base; deploy/README.md:239-259; .github/workflows/ci.yml:544-574 k8s-manifests (kustomize v5.8.1 + master-key-grep-Assertion); config.go:382-387, main.go:99-104.
- **Test(s):** CI-Job k8s-manifests nicht einsehbar. Verifier mit kustomize v5.8.1: base/staging/prod rc=0, CI-Assertion-Replay OK ×3 mit optional:false (SP/work/verify-B12/k8s-*.yaml, k8s-*.err); Binary: RETICORA_MASTER_KEY fehlend/leer → exit 1 'requires RETICORA_MASTER_KEY', '   ' und 'not-base64-key' → exit 1 'invalid base64 master key' (SP/work/verify-B12/mk.log); gerenderte Env → exit 1 'requires RETICORA_OIDC_ISSUER_URL' (k8s-env-*.log). Defekttest-Welle T5: TestAuditT5K8sRenderedEnvSatisfiesStartCheck (wps WP-097, WP-099) – in dieser Synthese 3/3 Subtests FAIL (SP/work/synth-T5/k8s-env.log).
- **Soll/Ist:** SOLL: K8s-Pods erhalten den Pflicht-Master-Key, starten ohne ihn nicht, Builds grün. IST: Injektion und CI-Assertion funktionieren, fehlender/leerer/ungültiger Key verhindert den Serverstart. Der zugehörige Nutzerprozess (funktionierender K8s-Rollout) scheitert dennoch, weil dieselben Manifeste den Pflicht-OIDC-Issuer nicht liefern (Ursache in WP-097).
- **Evidenz:** SP/work/verify-B12/k8s-{base,staging,prod}.yaml plus CI-Assertion-Replay; SP/work/verify-B12/k8s-env-{staging,production}.log; SP/work/verify-B12/mk.log; SP/work/audit-B12/probe-master_key_missing.log; SP/work/synth-T5/k8s-env.log.
- **Abweichung:** Die engen AK sind erfüllt, der K8s-Rollout bleibt aber unstartbar (L-T5-04, gemeinsame Ursache mit WP-097). Laufzeitverhalten auf einem Cluster (CreateContainerConfigError bei fehlendem Secret-Key) nicht beobachtet.
- **Prüfebenen:** UX: N/A; API: N/A; Daten: OK (Master-Key nur aus Secret; leer/ungültig abgelehnt); E2E: FEHLER (kein Clusterlauf; gerenderte Env ohne RETICORA_OIDC_ISSUER_URL → Pod beendet sich trotz Master-Key); Robustheit: OK (fehlend/leer/ungültig abgelehnt; CI-Assertion erkennt fehlende Referenz)
- **Abhängigkeiten:** WP-097 (führte den Pflicht-Issuer ein; Behebung dort); WP-131
- **Defekte:** F-036 (P1) K8s-Manifeste liefern das durch WP-097 verpflichtende RETICORA_OIDC_ISSUER_URL nicht; Server beendet sich in base, staging und prod beim Start
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** K8s-Manifeste mit vollständiger Pflichtkonfiguration (WP-097-Fix) und CI-Pflichtschlüssel-Assertion (TestAuditT5K8sRenderedEnvSatisfiesStartCheck grün); kind-Smoke: Pod Ready mit Master-Key, CreateContainerConfigError ohne Key, leerer Wert → exit 1.

<a id="wp-100"></a>
#### WP-100 – Backup-Kette: WAL-Archivierung und Basisbackups, S3-Versionierung

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** OPS-04, NFR-05
- **Anforderung/Nutzerziel:** Betreiber erhalten eine funktionierende Backup-Kette: kontinuierliches WAL-Archiv plus tägliches pgBackRest-Basisbackup in Compose und K8s, 35 Tage Aufbewahrung, versionierter und replizierter Objektspeicher.
- **Implementierung:** deploy/docker-compose/docker-compose.yml:28-70 (archive_mode=on, archive_command pgbackrest archive-push, archive_timeout=300), :76-106 backup-Dienst; deploy/docker-compose/postgres/Dockerfile:7-30 (pgBackRest 2.56.0 aus Tarball auf TIMESCALE_IMAGE=latest-pg16); pgbackrest.conf (repo1 aes-256-cbc, retention 35); reticora-backup.sh:38-72; deploy/k8s/base/components/backup/ (pgbackrest-configmap, täglicher Full-CronJob, pg_dump-CronJob; WAL nur als Kommentar pgbackrest-configmap.yaml:1-9); backend/internal/platform/blob/blob.go:265-301 EnableVersioning, backend/cmd/server/main.go:794-812 setupObjectStorage; deploy/terraform/main.tf:34-56 (check 'backup_replication'), variables.tf:99-129; docs/backup-dr.md:22-124.
- **Test(s):** SP/runs/sh-backup-config.log 26/26 (statische Greps, Scheduler/Health gegen pgbackrest-Stub; tests/backup-config.test.sh:74 greppt nur den check-Block); results-backend.tsv pass TestEnableVersioningSendsASignedPutBucketVersioning (httptest). Verifier: MinIO-Overlay-Probe PASS (SP/work/verify-B13/minio-probe.log: Status Enabled, versions=2, deleteMarkers=1); terraform 1.9.8 (terraform-run.log, tf-plan-prod-noreplica.log, tf-plan-prod-same.log): backup_retention_days=30 exit 1, production ohne Replik exit 0 mit 'Warning: Check block assertion failed', Replik == Backup-Region exit 0; kustomize prod (k8s-prod.yaml): 0 Treffer archive_mode/archive-push, staging ohne Backup; scheduler-hour-busybox.log (HOUR=08/09 'arithmetic syntax error'); Image-Build blockiert (pg-image-build.log, pg-image-build2.log). Defekttest-Welle T5 (ops-config.test.sh): k8s_wal_archive FAIL, terraform_replica_missing/terraform_replica_same FAIL, Kontrolle terraform_replica_eu ok – in dieser Synthese erneut ausgeführt (SP/work/synth-T5/ops-config.log).
- **Soll/Ist:** SOLL: WAL-Archiv plus tägliches Basisbackup in Compose UND K8s, versionierter und replizierter Bucket, 35 Tage, Doku. IST: Compose vollständig konfiguriert, aber hier nicht ausführbar; K8s rendert keine WAL-Archivierung; Versionierung nur für App-Buckets bewiesen; Replikation weder implementiert noch erzwungen (Terraform-Check nur Warnung, ausgeführt). Aufbewahrung und Doku erfüllt.
- **Evidenz:** SP/work/verify-B13/{minio-probe.log, terraform-run.log, tf-plan-prod-noreplica.log, tf-plan-prod-same.log, k8s-prod.yaml, k8s-staging.yaml, scheduler-hour-busybox.log, pg-image-build.log, pg-image-build2.log}; SP/runs/sh-backup-config.log; SP/work/synth-T5/ops-config.log.
- **Abweichung:** AK1 teilweise verletzt: K8s ohne WAL-Archivierung (L-T5-18); Backup-Bucket-Versionierung/Replikation nicht erzwungen, Terraform nur Warnung (L-T5-19). P3: Scheduler stirbt bei HOUR=08/09 (L-T5-37); pgBackRest-Tarball ungeprüft, Basisimage ungepinnt (L-T5-38). Reale Compose-Kette wegen Netzwerkrichtlinie unbelegt.
- **Prüfebenen:** UX: N/A (Betreiberkonfiguration); API: OK (setupObjectStorage aktiviert Versionierung beim Start; gegen MinIO per Overlay-Probe belegt, Verdrahtung selbst ungetestet); Daten: LÜCKE/FEHLER (kein ausgeführter Nachweis für WAL-Archiv oder Basisbackup; Backup-Bucket weder versioniert noch repliziert; App-Bucket-Versionierung belegt); E2E: LÜCKE (echte postgres+backup-Kette hier blockiert, CI-Lauf nicht einsehbar); Robustheit: FEHLER (Scheduler 08/09; 24/abc stillschweigend Mitternacht; keine Negativtests für falsches Cipher, unerreichbares repo2, gescheitertes archive-push)
- **Abhängigkeiten:** WP-099 (K8s), WP-101 (Restore-Nachweis), WP-105 (Terraform-Variablen), Netzwerkzugang für den Image-Build
- **Defekte:** F-128 (P2) K8s-Backup-Komponente provisioniert keine WAL-Archivierung, F-129 (P2) Versionierung/Replikation des Backup-Buckets nicht erzwungen; Terraform-Regel 'production requires replica' ist nur eine Warnung, F-290 (P3) Backup-Scheduler reticora-backup.sh stürzt bei RETICORA_BACKUP_HOUR=08/09 ab und plant ungültige Werte stillschweigend auf Mitternacht, F-291 (P3) pgBackRest-Tarball ohne Prüfsumme; Basisimage ungepinnt (auch im signierten Release-Image)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** (1) realer Compose-Backup-Ketten-Test oder einsehbarer CI-Lauf: archived_count steigt nach pg_switch_wal(), Full-Backup vorhanden, Health OK, falsches Cipher scheitert; (2) K8s-WAL-Archivierung provisioniert oder verbindlicher Managed-PITR-Pfad geprüft (ops-config k8s_wal_archive grün); (3) Terraform-Plan production ohne Replik bzw. mit Replik == Backup-Region scheitert (terraform_replica_* grün) und Backup-Bucket-Versionierung erzwungen; (4) MinIO-Integrationstest setupObjectStorage; (5) Scheduler-Validierung 0..23 mit Test.

<a id="wp-101"></a>
#### WP-101 – Restore-Nachweis RPO 15 min / RTO 4 h

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** NFR-05, OPS-04
- **Anforderung/Nutzerziel:** Betreiber erhalten einen gemessenen PITR-Restore-Nachweis (RPO 15 min / RTO 4 h) als Artefakt und in der Doku; der Workflow scheitert bei Grenzwertüberschreitung.
- **Implementierung:** tests/restore-pitr.sh:35-39 (RPO 900 s, RTO 14400 s, WRITE_SECONDS Default 420), :137-151 Szenario 1 PITR-Exaktheit, :153-170 Szenario 2 Datenvolumenverlust (rpo = last_commit - restored_last), :172-186 Archivierung nach Restore, Bericht, Exit-Code; .github/workflows/restore-test.yml (pitr-Job nightly, Artefakt restore-pitr-report); docs/backup-dr.md:126-161 (Messtabelle :150-157).
- **Test(s):** Kein Test der Grenzwertlogik, kein Baseline-Eintrag. Lokaler Lauf unmöglich: pgBackRest-Image-Build scheitert ohne und mit Proxy+CA-Workaround (SP/work/verify-B13/pg-image-build.log 'DNS lookup error', pg-image-build2.log 'dl-cdn.alpinelinux.org … Permission denied'; Host-curl CONNECT 403 für Alpine-CDN und GitHub-Tarball). CI-pitr-Lauf nicht einsehbar. Defekttest-Welle T5: ops-config.test.sh Fall restore_rpo_window fails-as-expected ('WRITE_SECONDS=420s <= RPO_LIMIT=900s'), in dieser Synthese erneut ausgeführt (SP/work/synth-T5/ops-config.log).
- **Soll/Ist:** SOLL: gemessener PITR-Nachweis, der bei Grenzwertverletzung scheitert. IST: Skript und Workflow strukturell vorhanden, hier wegen echter Netzwerkblockade nicht ausführbar; die RPO-Grenzwertprüfung kann mit den Defaults nicht scheitern; die dokumentierten Messwerte stammen aus einer anderen Parametrisierung (30 s statt 420 s Schreibfenster).
- **Evidenz:** Blocker: SP/work/verify-B13/pg-image-build.log, pg-image-build2.log; Code: tests/restore-pitr.sh:39, :152-169, :186; docs/backup-dr.md:150-157 (RPO 52 s bei 30 s Schreibfenster als bestanden geführt – kein Schreibphasen-Datensatz wiederhergestellt); ops-config restore_rpo_window FAIL.
- **Abweichung:** AK2 teilweise verletzt: RPO-Grenzwert kann mit Defaults nicht verletzt werden (L-T5-20). AK1 wegen Netzwerkrichtlinie nicht ausführbar (NICHT TESTBAR für diesen Teil). P3: Doku-Messwerte aus Nicht-Default-Lauf auf Spielzeugdatenmenge (L-T5-39).
- **Prüfebenen:** UX: N/A; API: N/A; Daten: LÜCKE (PITR-Exaktheit implementiert, nie einsehbar ausgeführt); E2E: LÜCKE (lokal blockiert, reproduziert; CI-Lauf nicht einsehbar); Robustheit: FEHLER (RPO-Assertion nicht diskriminierend; kein Negativlauf mit fehlendem WAL-Segment, archive_timeout > 900 s oder falschem Cipher; RTO nur auf 50k-Zeilen-Spielzeug-DB)
- **Abhängigkeiten:** WP-100 (pgBackRest-Image/Compose); Netzwerkzugang zu Alpine-CDN und GitHub für den Image-Build; GitHub Actions für einen einsehbaren Lauf
- **Defekte:** F-130 (P2) RPO-Grenzwertprüfung in restore-pitr.sh kann mit Default-Parametern nicht scheitern, F-292 (P3) Dokumentierte Restore-Messwerte stammen aus einem Nicht-Default-Lauf (30 s) auf Spielzeugdatenmenge
- **Statusverlauf:** Einzelprüfung NICHT TESTBAR → Verifikation NICHT TESTBAR → final TEILWEISE BESTANDEN; NICHT TESTBAR → TEILWEISE BESTANDEN. Begründung: Nach den verbindlichen Statusregeln führt ein bestätigter Defekt, der ein verbindliches Kriterium verletzt, zu FEHLGESCHLAGEN bzw. TEILWEISE BESTANDEN – ein reines NICHT TESTBAR ist damit ausgeschlossen. L-T5-20 (P2, Ursache belegt durch tests/restore-pitr.sh:39 und :157-169, durch den eigenen dokumentierten Lauf docs/backup-dr.md:150-157 und den roten Defekttest restore_rpo_window) verletzt AK2 für den RPO-Grenzwert. Nur dieser Teil ist betroffen (RTO- und Exaktheitsverletzungen würden laut :186 den Workflow scheitern lassen), AK1 bleibt wegen der Netzwerkblockade unbelegt – daher TEILWEISE BESTANDEN statt FEHLGESCHLAGEN.
- **Offene Nachweise / Retest:** RPO-Prüfung diskriminierend gemacht (Schreibfenster > Grenzwert oder RPO <= archive_timeout + Marge; ops-config restore_rpo_window grün) mit Negativlauf archive_timeout=1200 → exit 1; einsehbarer restore-pitr-Lauf (CI-Artefakt oder lokal, sobald Alpine-CDN/GitHub erreichbar) mit Defaults: exakter PITR, RPO <= 900 s, RTO <= 14400 s, Archivierung nach Restore; Doku-Tabelle aus diesem Artefakt aktualisiert.

<a id="wp-102"></a>
#### WP-102 – Signierte Releases: cosign, SBOM, verpflichtende Signaturprüfung im Updater

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** TEC-15, COL-06
- **Anforderung/Nutzerziel:** Betreiber und Collectoren installieren und aktualisieren nur Release-Artefakte, die keyless vom Release-Workflow des Projekts signiert sind, mit SBOM je Release (TEC-15, COL-06).
- **Implementierung:** .github/workflows/release.yml (Trigger Tag v*, outage-Job als Voraussetzung :23-28, :108; binaries-Job mit syft-SBOM, cosign sign-blob --bundle, verify-blob, Updater-Manifeste; images-Job :113-169 für server, collector, frontend, postgres nach ghcr.io/datahub-chiemgau mit SBOM, cosign sign/attest/verify); edgecore/update/update.go:117-162 (Apply verlangt Verifier und SignatureBundleURL, ErrUnsigned; Verify vor dem Tausch), :249-296 CosignVerifier (verankerte Identity-Regexp auf release.yml@refs/tags/v…, GitHub-OIDC-Issuer); collector/collectorcmd/update.go:28 (einziger Produktivaufrufer). Installpfade install.sh, install-cloud.sh, install-vm.sh ohne cosign (install-vm.sh:487-498, :550-556 laden Quellen/Go-Toolchain ungeprüft).
- **Test(s):** results-edgecore.tsv pass: TestApplyRefusesUnsignedAndWronglySignedReleases (5 Subtests: unsigned_release, no_verifier, bundle_not_found, wrong_signature, valid_signature), TestCosignVerifierPinsTheReleaseIdentity (exakte argv inkl. Regexp/Issuer), TestFailedStartsRollBackTheUpdate, TestConfirmKeepsTheUpdate, TestHTTPSourceReadsTheChannelManifest – Verifier ist immer Fake bzw. gestubbte run-Funktion, kein echtes cosign. release.yml nicht ausführbar (GitHub-OIDC). Defekttest-Welle T5: ops-config.test.sh install_signature_install-vm und install_signature_install-cloud FAIL ('no cosign verify'), in dieser Synthese erneut ausgeführt.
- **Soll/Ist:** SOLL: signiertes Release mit SBOMs, Signaturprüfung vor Installation und beim Auto-Update. IST: Ablehnungslogik des Updaters unit-belegt und an CosignVerifier verdrahtet; Release-Pipeline vorhanden, aber nie beobachtet ausgeführt; keine Signaturprüfung vor der Installation (TEC-15, von WP-102 'geschlossen'); K8s-Overlays referenzieren nicht die veröffentlichten signierten Images.
- **Evidenz:** results-edgecore.tsv (alle edgecore/update-Tests pass); update.go:117-162, :249-296; collectorcmd/update.go:28; grep -rln cosign (nur release.yml, edgecore/update, collectorcmd/update.go, Doku); install-vm.sh:487-498, :550-556; SP/work/verify-B13/k8s-prod.yaml (reticora-cmdb/server:1.0.0, reticora-cmdb/postgres:1.0.0); SP/work/synth-T5/ops-config.log.
- **Abweichung:** AK1 unbelegt (Release-Workflow nur außerhalb erreichbarer Umgebung ausführbar). TEC-15 'Signaturprüfung vor Installation' fehlt (L-T5-21). P3: K8s-Overlays referenzieren unveröffentlichte Imagenamen (L-T5-40); Doku nennt 3 statt 4 Images (L-T5-41). AK2 nur mit Fake-Verifier belegt.
- **Prüfebenen:** UX: N/A (Doku docs/README.md:927-958, docs/betrieb/collector-updates.md); API: OK (Apply fail-closed: Verifier und Bundle Pflicht, Verify vor Tausch – unit-belegt); Daten: OK (atomarer Tausch mit Rollback, TestFailedStartsRollBackTheUpdate/TestConfirmKeepsTheUpdate); E2E: LÜCKE (kein Manifest → echtes cosign verify → Installpfad; Release-Workflow nie beobachtet); Robustheit: FEHLER (kein Echt-cosign-Test mit manipuliertem Artefakt/falscher Identität; Installation ohne Signaturprüfung)
- **Abhängigkeiten:** GitHub-Actions-OIDC (Fulcio/Rekor); WP-104 (outage-Gating); WP-099 (K8s-Overlays); WP-148 (Collector-Kanäle)
- **Defekte:** F-131 (P2) Keine Signaturprüfung vor der Installation (TEC-15 'Signaturprüfung vor Installation'), F-293 (P3) K8s-Overlays referenzieren Images, die das Release nie veröffentlicht, F-294 (P3) Release-Doku nennt drei Images, der Workflow baut und signiert vier
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** (1) einsehbarer (Pre-)Release-Lauf mit SBOMs, Bundles, Image-Signaturen, Attestierungen und Selbstverifikation; (2) Echt-cosign-Integrationstest (manipuliertes Artefakt/falsche Identität abgelehnt, echtes akzeptiert); (3) Signaturprüfung vor Installation mit Negativtest (ops-config install_signature_* grün); (4) K8s-Overlays auf die veröffentlichten ghcr.io-Images.

<a id="wp-103"></a>
#### WP-103 – Betriebsbeobachtung: Worker-Fehlermetriken und Alarmregeln

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** OPS-05
- **Anforderung/Nutzerziel:** Betreiber sehen Lauf-, Fehler- und Rückstaumetriken jedes Hintergrund-Workers und werden bei Ausfall oder Rückstau über die in deploy/monitoring konfigurierte Kette und – nach WP-137 – über NTF-Kanäle alarmiert (OPS-05).
- **Implementierung:** backend/internal/observability/workers.go (reticora_worker_{errors_total,runs_total,last_success_timestamp_seconds,enabled,up,backlog}, ObserveWorkerRun :61-66); backend/internal/server/workers.go StartWorkers; ObserveWorkerRun-Aufrufer (7 Worker): webhook/dispatcher.go:506/510, jobs/scheduler.go:191, monitoring/evaluator.go:96, notify/notifier.go:316, discovery/consumer.go:829/854, export/worker.go:216, events/publisher.go:97; ohne Metriken: jobs, nats_edge, credential_bundles, discovery_scopes (main.go:582-593 nur slog.Error); Backlog nur main.go:525, :545, :604; deploy/monitoring/worker-rules.yaml (PrometheusRule-CRD, nirgends referenziert); deploy/monitoring/alertmanager.yaml (kein globales SMTP, Platzhalter, kein NTF); deploy/grafana/dashboard-overview.json (3 Worker-Panels).
- **Test(s):** results-backend.tsv pass (internal/server): TestWorkerMetricsAreRegisteredAndRecorded (Fake-Worker rufen ObserveWorkerRun direkt), TestWorkerAlertRulesCoverTheWorkerMetrics (String-Contains), TestDisabledWorkerDoesNotStart, TestEveryRegisteredWorkerHasASwitch. Verifier: amtool v0.27.0 check-config → 'FAILED: no global SMTP smarthost set', exit 1 (SP/work/verify-B13/amtool-check.log); promtool v2.54.1 check rules auf extrahiertem .spec SUCCESS (promtool-check-plain.log), auf der CRD-Datei FAIL (promtool-check.log); promtool test rules SUCCESS (SP/work/verify-B13/prom/worker-rules.test.yaml: alle 5 Alarme feuern positiv, nicht negativ; NoSuccess feuert nicht ohne last_success-Serie); Overlay-Probe zz_vb13_jobs_metrics_probe_test.go (TestQueueRetriesAndRecoversLostWorkers 5 Subtests PASS, 'jobs worker: runs_total delta=0 errors_total delta=0', jobs-metrics-probe.log). Defekttest-Welle T5: ops-config alertmanager_smtp und alertmanager_amtool FAIL (erneut ausgeführt). Nicht kodifiziert: fehlende Metriken der 4 Worker, NTF-Anbindung.
- **Soll/Ist:** SOLL: jeder Worker exportiert Lauf-, Fehler- und Rückstaumetriken; Regeln existieren; Betreiber werden über die konfigurierte Kette und NTF alarmiert. IST: Regeln gültig und feuern korrekt (ausgeführt); 4 von 11 Workern ohne Lauf-/Fehlermetriken (für jobs zur Laufzeit belegt); die ausgelieferte Alertmanager-Kette wird von Alertmanagers Validator abgelehnt; NTF nicht angebunden.
- **Evidenz:** SP/work/verify-B13/{amtool-check.log, promtool-check-plain.log, promtool-test-rules.log, prom/worker-rules.test.yaml, jobs-metrics-probe.log, zz_vb13_jobs_metrics_probe_test.go}; results-backend.tsv Worker-Tests; config.go:346-358; main.go:566-604; SP/work/synth-T5/ops-config.log.
- **Abweichung:** AK1 verletzt: 4 Worker ohne Metrikvertrag, Backlog nur für 3 von 11 (L-T5-23). AK2 verletzt: alertmanager.yaml wird abgelehnt (L-T5-22), NTF trotz geliefertem WP-137 nicht angebunden (L-T5-24). P3: NoSuccess blind für nie erfolgreiche Worker (L-T5-42). Regeln nicht deploybar ohne Operator-CRD und nirgends verdrahtet.
- **Prüfebenen:** UX: N/A (nur Grafana-JSON, 3 Worker-Panels, gültig); API: LÜCKE (/metrics für instrumentierte Worker belegt; 4 Worker außerhalb des Vertrags, Laufzeitprobe für jobs); Daten: N/A (nur Metriken); E2E: FEHLER (Worker-Fehler → Metrik → Regel → Alertmanager → Betreiber: letzter Schritt bricht, Konfiguration abgelehnt; Fehler von jobs, nats_edge, credential_bundles, discovery_scopes erreichen die Regelebene nie); Robustheit: LÜCKE (Fehler in Bundles/Scopes nur geloggt; NoSuccess-Blindstelle per promtool belegt; kein amtool/promtool in CI)
- **Abhängigkeiten:** WP-137 (NTF, geliefert 6da3c58), WP-130/146/147/153 (spätere Worker), WP-098
- **Defekte:** F-132 (P2) alertmanager.yaml wird von Alertmanagers Konfigurationsprüfung abgelehnt (kein globaler SMTP-Smarthost), F-133 (P2) Vier registrierte Worker exportieren keine Lauf-/Fehler-/Last-Success-Metriken; Worker-Backlog nur für 3 von 11, F-134 (P2) Betreiberalarmierung nicht an NTF-Kanäle angebunden, obwohl WP-137 geliefert ist, F-295 (P3) ReticoraWorkerNoSuccess kann für einen seit Prozessstart nie erfolgreichen Worker nicht feuern
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** (1) alle config.WorkerNames-Worker emittieren Lauf- und Fehlermetriken (und Backlog, wo queue-basiert), belegt durch registry-weiten Test; (2) amtool check-config und promtool check/test rules in CI grün (ops-config alertmanager_* grün); (3) NTF-Bridge implementiert und getestet oder formale Zurückstellungsentscheidung.

<a id="wp-104"></a>
#### WP-104 – Ausfall- und Backpressure-Tests aller Dienste (OPS-06)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** OPS-06
- **Anforderung/Nutzerziel:** Das Betriebsteam erhält den Nachweis, dass ein Ausfall von PG, NATS, Redis, S3, SMTP oder der Server-Erreichbarkeit für den Collector keinen stillen Datenverlust verursacht, Backpressure anwendet und wieder anläuft; der Nachweis läuft nightly und vor jedem Release.
- **Implementierung:** tests/outage/run.sh:39-76 dependency_outage (psql-Markerzeile, compose stop, /readyz 503 mit Check unavailable, Server lebt ohne Neustart, Wiederanlauf, Marker vorhanden), :81-104 component_scenarios (go test collector-Spool, edgecore-Buffer, Webhook-Retry), :112-118 smtp hart N/A ('no SMTP delivery exists yet (TEC-13)'); Readiness-Checks health.go:145, main.go:163, :237, :257; .github/workflows/outage.yml (cron '17 2 * * *', workflow_call, ./install.sh --cloud, dann run.sh); release.yml (binaries und images need outage); middleware/ratelimit.go (Probes ausgenommen).
- **Test(s):** Auditor-Lauf SP/work/audit-B13/outage-report.md (Komponenten + smtp: 3 PASS, 1 N/A). Baseline pass: collector TestUploadRecords_FailureSpoolsAndFlushDelivers, TestHeartbeatReportsSpoolBackpressureAndLoss; edgecore/buffer TestDiskBufferSizeLimitAppliesBackpressure, TestDiskBufferMaxAgeKeepsMessagesFor24Hours, TestPersistenceAcrossBufferInstances; webhook TestDispatcherPersistsAndRetriesFailedDelivery, TestDispatcherMarksDeliveryFailedAfterMaxAttempts; middleware TestRateLimiterExemptsOperationalProbesDuringARedisOutage; server TestReadyzFailingCheckIs503; notify TestNotifierDeliversWithRetryAndDedup. Abhängigkeitsszenarien brauchen ./install.sh --cloud (Image-Build blockiert, SP/work/verify-B13/pg-image-build2.log); CI-Lauf nicht einsehbar. Defekttest-Welle T5: ops-config outage_smtp FAIL (erneut ausgeführt). Nicht kodifiziert: schwache No-Loss-Assertion.
- **Soll/Ist:** SOLL: Compose-Suite mit 6 Ausfallarten und Assertions zu kein stiller Verlust, Backpressure, Wiederanlauf; nightly und vor Releases. IST: 4 Abhängigkeitsszenarien, Komponententests und Release-Gating vorhanden; SMTP fehlt trotz gelieferter SMTP-Zustellung; Collector-Szenario nur komponentenbasiert; No-Loss-Assertion schwach; Abhängigkeitsteil hier unbelegt.
- **Evidenz:** tests/outage/run.sh:39-118; git log: smtp.go 6da3c58 (2026-10-13) nach run.sh bec175d (2026-10-12); results-*.tsv Komponententests pass; SP/work/audit-B13/outage-report.md; Blocker SP/work/verify-B13/pg-image-build2.log; SP/work/synth-T5/ops-config.log.
- **Abweichung:** AK1 teilweise verletzt: SMTP-Ausfallszenario hart N/A (L-T5-25); 'kein stiller Datenverlust' nur per vorab geschriebener Markerzeile (L-T5-26). P3: Collector-Szenario nicht Compose-basiert (L-T5-43). AK2 nur statisch belegt.
- **Prüfebenen:** UX: N/A; API: OK/LÜCKE (/readyz 503 je Abhängigkeit und Ratelimiter-Ausnahme unit-belegt; Client-API-Verhalten im Ausfall nicht assertiert); Daten: LÜCKE (keine Last während des Ausfalls; nicht geprüft, dass im Ausfall angenommene Schreibvorgänge danach verarbeitet werden); E2E: LÜCKE (Abhängigkeitsausfälle nie hier ausgeführt, CI nicht einsehbar; Collector-Szenario nicht gegen echten Server); Robustheit: FEHLER (kein SMTP-Ausfall; object_storage nur TCP-Check)
- **Abhängigkeiten:** WP-137 (SMTP), WP-062 (Spool), WP-097 (readyz), Installationspfad (hier blockiert)
- **Defekte:** F-135 (P2) SMTP-Ausfallszenario wird als N/A gemeldet, obwohl SMTP-Zustellung existiert, F-136 (P2) 'Kein stiller Datenverlust' wird nur über eine vor dem Ausfall geschriebene psql-Markerzeile assertiert, F-296 (P3) Collector-Szenario 'Server unerreichbar' ist nur komponentenbasiert, nicht Compose-basiert
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** (1) einsehbarer vollständiger Suite-Lauf mit PASS für postgres, nats, redis, minio; (2) SMTP-Szenario implementiert und grün (ops-config outage_smtp grün, Zustellstatus pending→sent, genau 1 Mail); (3) lastbasierte No-Silent-Loss-Assertions (accepted == persisted); (4) Compose-Collector-Offline-Szenario oder ausdrückliche Entscheidung für Komponentennachweis.

<a id="wp-105"></a>
#### WP-105 – Betriebsreferenz EU/Dedicated, Air-Gap-Abgrenzung, Subprozessoren

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** PRI-11, NFR-09
- **Anforderung/Nutzerziel:** Kunden und Betreiber erhalten ein dokumentiertes Referenzbetriebsmodell, eine vom Betreiber gelieferte Subprozessorenliste und Infrastrukturvariablen, die EU-Regionen für alle Datenorte erzwingen (PRI-11, NFR-09).
- **Implementierung:** docs/betrieb/referenzmodell.md:6-15 (Betriebsmodelle, Air-Gap '[O] … nicht G1-gegatet'), :17-44 (Datenresidenz, Subprozessor-Referenz); docs/legal/subprozessoren.md ('Status: offen', nur Platzhalterzeile); deploy/terraform/variables.tf:34-48 (EU-Regionsliste AWS/GCP/Azure/Hetzner/STACKIT, London/Zürich/EWR ausgeschlossen), :52-129 (Validierungen region, backup_region, log_region, search_region, backup_retention_days, backup_replica_region); main.tf:12-30; .github/workflows/ci.yml:509-541 terraform-Job; docs/plan/entscheidungen.md:959-975 (E-35 offen, Erklärung vom 2026-10-15).
- **Test(s):** Verifier: CI-terraform-Schritte lokal mit terraform 1.9.8 (offizielles Zip, sha256 OK) auf Scratch-Kopie (SP/work/verify-B13/terraform-run.log, tf-plan-eu.log): fmt -check exit 0, init exit 0, validate 'Success!', EU-Plan exit 0; region=us-east-1, eu-west-2, eu-central-2, europe-west6; backup_region=us-west-2; log_region=eastus; search_region=switzerlandnorth; backup_replica_region=us-east-1; backup_retention_days=30 → alle exit 1 'must be an EU region (NFR-09 data residency)'. Defekttest-Welle T5: g1-gate-evidence.test.sh Fall subprocessors FAIL ('| – | *vom Betreiber zu liefern (A-11)* |'), in dieser Synthese erneut ausgeführt (SP/work/synth-T5/g1-gate-evidence.log).
- **Soll/Ist:** SOLL: Referenzmodell-Doku, vom Betreiber gelieferte Subprozessorenliste, EU-erzwingende Terraform-Variablen. IST: Referenzmodell vollständig; Terraform-EU-Erzwingung per Ausführung belegt; Subprozessorenliste leer (externe Lieferung E-35/A-11 offen), NFR-09-Gate-PASS beruht nur auf Erklärung.
- **Evidenz:** SP/work/verify-B13/terraform-run.log, tf-plan-eu.log; docs/betrieb/referenzmodell.md:6-44; docs/legal/subprozessoren.md; docs/plan/entscheidungen.md:959-975; variables.tf:34-129; SP/work/synth-T5/g1-gate-evidence.log (subprocessors, pass_by_declaration).
- **Abweichung:** AK2 nicht erfüllt: Subprozessorenliste fehlt (L-T5-27); NFR-09 steht im Gate nur per Erklärung auf PASS (L-T5-10). Einschränkung AK3: Modul ohne Ressourcen, Erzwingung nur auf Variablen-/Output-Ebene; Produktions-Replikat nur Warnung (L-T5-19, WP-100).
- **Prüfebenen:** UX: N/A (Doku und IaC); API: N/A; Daten: LÜCKE (Residenz nur auf Variablenebene eines ressourcenlosen Moduls; RETICORA_OTEL_ENDPOINT 'EU-Ziele' nur dokumentiert); E2E: N/A; Robustheit: OK (alle Nicht-EU-Werte abgelehnt, ausgeführt), LÜCKE (production ohne Replik nur Warnung)
- **Abhängigkeiten:** extern: Betreiberlieferung der Subprozessorenliste (E-35, A-11); WP-100 (backup_*-Variablen)
- **Defekte:** F-120 (P2) Gate-Checkliste und Abnahmeprotokoll führen Anforderungen allein per Erklärung des Auftraggebers als PASS, F-137 (P2) Subprozessorenliste leer; externe Lieferung (E-35/A-11) fehlt
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** (1) vom Betreiber gelieferte, vollständige Subprozessorenliste mit Region und AVV-Status je Dienst committed, E-35 entschieden (g1-gate-evidence subprocessors grün); NFR-09 bis dahin nicht als PASS im Gate; (2) optional Terraform-Regionsprüfungen als tftest committed.

<a id="wp-106"></a>
#### WP-106 – make seed reparieren (fehlende seed.sql)

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** SIM-01
- **Anforderung/Nutzerziel:** Entwickler und CI führen make seed aus, erhalten die SIM-01-Seed-Daten über den Seed-Befehl aus WP-203, und jeder Seed-Fehler lässt den Befehl scheitern.
- **Implementierung:** Makefile:155-160 (seed: cd backend && go run ./cmd/seed -database-url $(DATABASE_URL) -objects $(SEED_OBJECTS) -metrics $(SEED_METRICS), ohne Maskierung); backend/cmd/seed/main.go:19-67 (exit 2 bei fehlender DSN, falschem -at, Connect-Fehler; exit 1 bei Run-Fehler); seed.go ON CONFLICT DO NOTHING (idempotent); .github/workflows/ci.yml:185-189 'Seed the database (make seed)' im migrations-Job (DATABASE_URL :105).
- **Test(s):** SP/runs/seed.log make seed OK; results-backend.tsv pass backend/cmd/seed TestSeedSIM01 (3.15 s; SIM-01-Mengen je Org, deterministische IDs, zweiter Lauf, keine orgübergreifenden Beziehungen), TestSeedOptionsAreBounded. Auditor SP/work/audit-B13/seed-{neg,pos1,pos2}.log (nicht existente DB → make exit 2; zwei positive Läufe exit 0 mit identischen Org-IDs). Verifier gegen audit_db_16: SP/work/verify-B13/seed-readonly.log (default_transaction_read_only=on → 'cannot execute INSERT in a read-only transaction', make exit 2), seed-badflag.log (SEED_OBJECTS=abc → exit 2), seed-range.log (SEED_OBJECTS=5 → 'objects must be between 40 and 50000', exit 2). Keine Tests der Welle T5.
- **Soll/Ist:** SOLL: make seed scheitert bei Fehlern, ruft den WP-203-Seed auf, läuft als CI-Schritt. IST: erfüllt, mit ausgeführten Positiv-, Negativ- und Idempotenznachweisen.
- **Evidenz:** SP/runs/seed.log; results-backend.tsv (TestSeedSIM01, TestSeedOptionsAreBounded pass); SP/work/audit-B13/seed-*.log; SP/work/verify-B13/seed-readonly.log, seed-badflag.log, seed-range.log (alle EXIT 2).
- **Abweichung:** -
- **Prüfebenen:** UX: N/A (CLI/Makefile-Ziel); API: N/A; Daten: OK (Seed-Daten persistiert, Wiederholung idempotent mit gleichen IDs, TestSeedSIM01 prüft Mengen und Isolation); E2E: OK (make → go run cmd/seed → PostgreSQL, reale Läufe); Robustheit: OK (jede geprüfte Fehlerklasse liefert non-zero exit)
- **Abhängigkeiten:** WP-203 (cmd/seed), WP-003 (CI-migrations-Job)
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Optional tests/make-seed.test.sh gegen Wiedereinführung einer Fehlermaskierung.

### Epic B – Fundament

<a id="wp-107"></a>
#### WP-107 – Organisation, Client, Site: Zeitzone/Währung/Locale, Eindeutigkeiten, external_ref

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** LOC-01, LOC-02, LOC-03, TEN-01, API-05
- **Anforderung/Nutzerziel:** Org hat validierte IANA-Zeitzone, ISO-4217-Währung und BCP-47-Locale (GET/PATCH /org); Client-Namen eindeutig je Org, Site-Namen je (Org, Client); Konflikte liefern 409 mit Feld.
- **Implementierung:** backend/migrations/000101_org_client_site_fields.up.sql:8-41; backend/internal/tenantapi/validate.go:22-56, handler.go:24-25,59-73,89-145, pg_repository.go:34-48; UI-Pfad backend/internal/locations/handler.go:146-163 (writeError ohne Unique-Mapping)
- **Test(s):** results-backend.tsv pass tenantapi TestOrgClientSiteFieldsOnPostgreSQL, TestOrganizationFields, TestClientAndSiteUniqueness, TestClientCRUD, TestTenantIsolation; Real-Stack ci-lifecycle L13 pass (PATCH /org 200), roles-authz 06 pass (Viewer PATCH /org 403); locations-racks L02 FAIL nur an Soft-Assertion Zeile 434 (POST /locations 409 ohne field); Probe SP/work/verify-B14/pw-org.log
- **Soll/Ist:** Soll: validierte Felder, /org GET/PATCH, 409 mit Feldbezug. Ist: Felder, Validierung, /org bis Real-Stack belegt; 409 mit field bei /clients und /sites, nicht bei /api/v1/locations (einziger UI-Anlagepfad).
- **Evidenz:** SP/work/locrack/run-final.log L02 (409 {detail:'a record with the same unique value already exists'} ohne field); POST /sites 409 field=name pass; SP/work/verify-B14/probe-rerun.log TestB14ProbeLocationTreeDuplicateNames
- **Abweichung:** Kriterium 3 nur teilweise (Tree-API ohne Feldbezug, P3). E2E-Verifikation stuft die Bindung des Kriteriums an /locations als spec-unklar ein (PO-Entscheid); bei Einschränkung auf tenantapi-Endpunkte wäre BESTANDEN mit P3-Abweichung möglich. Keine UI für Org-/Client-Felder (außerhalb Dateiliste).
- **Prüfebenen:** UX: LÜCKE – Dialog bleibt mit Alert offen, Name-Feld nicht markiert; API: OK – /org 400 je Feld real belegt; Daten: OK – Constraints, Migrations-Rename; E2E: OK/LÜCKE – L02 real, gültiger tz/currency-PATCH nur auf PG; Robustheit: LÜCKE – geo_lat/geo_lon ohne Bereichsprüfung
- **Abhängigkeiten:** WP-055; Location-Tree-WPs (WP-026/WP-053) besitzen locations/handler.go
- **Defekte:** F-302 (P3) POST /locations beantwortet doppelte Namen mit generischem 409 ohne Feldbezug
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** L-T6-epic-b-29 behoben und L02 ohne Soft-Fehler grün, oder PO-Entscheid, dass Kriterium 3 nur tenantapi-Endpunkte bindet; Integrationstest 409 field=name für site/building/room/rack über /locations.

<a id="wp-108"></a>
#### WP-108 – Building/Room/Rack: Eindeutigkeiten und height_units

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** LOC-04, LOC-05, LOC-06
- **Anforderung/Nutzerziel:** Building-/Room-/Rack-Namen eindeutig je Elternebene; Rackhöhe height_units 1–60 (Default 42) in DB und API, explizite 0 → 422 bei POST und PATCH.
- **Implementierung:** backend/migrations/000102_location_uniques_height_units.up.sql:7-22; backend/internal/rack/handler.go:83,87,113,174; rack/pg_repository.go:36-40, :143 UpdateRack (kein Abgleich mit Mounts); frontend/src/pages/RackPage.tsx:96,136
- **Test(s):** results-backend.tsv pass TestLocationUniquesAndHeightUnitsOnPostgreSQL, TestRackCRUD, TestRackRulesAgainstPostgres; Real-Stack locations-racks L01 pass, L02 (alle LOC-04..06/height-Assertions pass); Welle: TestAuditT6RackForeignClientPatchIsNotFound PASS (Review ok, rack untagged PASS), TestAuditDefectRackHeightNotBelowMounts fails-as-expected
- **Soll/Ist:** Soll: Uniques und height_units 1–60/42, 0 → 422. Ist: bis Real-Stack erfüllt; aber PATCH height_units speichert Höhen unterhalb belegter HE (Mounts außerhalb des Racks, RCK-01-Invariante verletzt).
- **Evidenz:** SP/work/locrack/run-final.log L01/L02; WT height_auditdefect_test.go:88 'PATCH height_units=39 = 200 (stored 39), want 409'; SP/work/review-T6-epic-b/defect-run.log:10
- **Abweichung:** Bestätigter P2-Defekt L-T6-epic-b-08 im height_units-API-Pfad dieses WPs.
- **Prüfebenen:** UX: OK – RackPage zeigt '(42 HE)' real; API: FEHLER – PATCH akzeptiert Höhe unter Mounts; Daten: FEHLER – inkonsistente Rack/Mount-Daten; E2E: OK – L01/L02 real; Robustheit: LÜCKE – kein Konsistenzcheck
- **Abhängigkeiten:** WP-107; WP-126 (RCK-01 Mount-Prüfung)
- **Defekte:** F-140 (P2) PATCH height_units akzeptiert Höhe unterhalb bestehender Mounts
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final TEILWEISE BESTANDEN; BESTANDEN → TEILWEISE BESTANDEN: Der Verifier ließ BESTANDEN mit P2-Abweichung stehen; nach den verbindlichen Statusregeln darf nur ein P3-Mangel BESTANDEN begleiten. Der bestätigte P2 (Defekttest schlägt erwartungsgemäß fehl) betrifft die API-Seite von Kriterium 1 (height_units in der API nimmt inkonsistente Werte an).
- **Offene Nachweise / Retest:** TestAuditDefectRackHeightNotBelowMounts grün; Real-Stack-Schritt Shrink → 409, DB unverändert.

<a id="wp-109"></a>
#### WP-109 – Durchgängige Hierarchie Org → Client → Site mit Scopewirkung belegen

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** TEN-01
- **Anforderung/Nutzerziel:** Die Hierarchie Org → Client → Site → Location → CI/Asset ist eine Struktur, deren Scope auf jeder Ebene wirkt; ein Site-gebundener Nutzer sieht nur seinen Teil.
- **Implementierung:** backend/internal/locations/hierarchy_integration_test.go:26-113; permission/pg_repository.go:164-230 AccessGrants; identity/principal.go:196-232 unionDimension; middleware/auth.go:259-283 TenantScopeFor; asset ohne site_id
- **Test(s):** results-backend.tsv pass TestHierarchyScopeOnPostgreSQL (kodiert Leck als erwartet, Z.105-106); Real-Stack L01/L10 pass (nur Baum); Welle TestAuditDefectSiteGrantConfinedToClientAndSite fails-as-expected
- **Soll/Ist:** Soll: belegte Scopewirkung über die ganze Hierarchie. Ist: nur für Location-Knoten und platzierte CIs; Site-Grant setzt Client-Scope auf NULL, Site-Nutzer lesen Kontakte, Credentials, Assets und unplatzierte CIs fremder Clients.
- **Evidenz:** SP/work/verify-B14/probe-sitescope-chain.log ('resolved scope clients={All:true} sites=[site1]', 'contact client2=true asset client2@site2=true'); WT sitescope_auditdefect_test.go:136,167; SP/work/review-T6-epic-b/defect-run.log:39
- **Abweichung:** Kriterium 1 verletzt (beobachtet): Site-Scope nicht transitiv zum Client, Assets ohne Site-Scope (RBA-03, TEN-02).
- **Prüfebenen:** UX: N/A; API: N/A (Test-WP); Daten: FEHLER – Cross-Client-Lesen; E2E: LÜCKE – keine Real-Session eines Site-Nutzers; Robustheit: FEHLER – einziger Site-Test kodiert das Leck
- **Abhängigkeiten:** WP-065, RBA-03-Scopeaggregation, TEN-02
- **Defekte:** F-014 (P0) Site-Grant setzt Client-Scope auf NULL: Site-Nutzer lesen Kontakte, Credentials, Assets und unplatzierte CIs fremder Clients, F-141 (P2) asset/quantity_item ohne denormalisierte site_id: Site-Scope greift nicht für platzierte Assets
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Defekttest grün, TestHierarchyScopeOnPostgreSQL auf asset2:false umgestellt, e2e-real Site-Session ohne Fremddaten.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Der zugeordnete L-T6-epic-b-01 wird P0 (Cross-Client-Exposition, TEN-09 'Critical'; siehe defekt_korrekturen). WP-Priorität = höchste Abweichung.

<a id="wp-110"></a>
#### WP-110 – Dev-Login nur außerhalb production

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** AUT-03
- **Anforderung/Nutzerziel:** Unsignierte Dev-Tokens nur in development; Start mit RETICORA_ALLOW_INSECURE_DEV_AUTH in staging/production wird verweigert.
- **Implementierung:** backend/internal/config/config.go:474-479; middleware/auth.go:78-92,112-115; cmd/server/main.go:426-432
- **Test(s):** results-backend.tsv pass TestDevAuthOnlyInDevelopment, TestDevelopmentLoginOnlyInDevelopment (4 Subtests); Realbinary SP/work/verify-B14/out-devauth-staging.log, out-devauth-production.log; SP/work/audit-B14/probe-unsigned-token.log
- **Soll/Ist:** Erfüllt: Startverweigerung real (exit 1 mit AUT-03-Meldung), unsigniertes Token 401 im Real-Stack.
- **Evidenz:** exit 1 'RETICORA_ALLOW_INSECURE_DEV_AUTH is only allowed with RETICORA_ENVIRONMENT=development'; HTTP 401 alg:none
- **Abweichung:** -
- **Prüfebenen:** UX: N/A; API: OK – 401; Daten: N/A; E2E: OK – Realbinary/Real-Stack; Robustheit: OK – doppelte Absicherung
- **Abhängigkeiten:** WP-066
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-111"></a>
#### WP-111 – Self-Signup: Org, Slug, Default-VRF, erster org_admin, Trial, Verifikation

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** API-05, TLC-01
- **Anforderung/Nutzerziel:** Interessent registriert sich, bestätigt den Mail-Link und setzt ein Passwort; Org, Slug, Default-VRF, org_admin, Trial-Entitlements und operator_audit entstehen atomar.
- **Implementierung:** backend/internal/signup/handler.go:35-111, service.go:193,235,294,493, pg_repository.go:46,173-215; cmd/server/main.go:887-912; frontend/src/pages/OnboardingPage.tsx:93-200
- **Test(s):** results-backend.tsv pass TestSignupCreatesOrganizationAtomically, TestVerifyCreatesAccountForTheNewOrganization, TestVerifyDeletesTheAccountWhenTheTransactionFails, TestTrialEntitlementsFollowENT07 u.a.; SP/runs/e2e-onboarding-real.log Test 5 pass
- **Soll/Ist:** Erfüllt und real belegt (199 Signups mit je 1 VRF, 1 org_admin, Entitlements). Randfehler: Slug-Erschöpfung → 500.
- **Evidenz:** e2e-onboarding-real.log Test 5; Verifier-SQL 199|199|199|199; SP/work/verify-B14/probe-rerun.log TestB14ProbeSlugExhaustion
- **Abweichung:** P3 L-T6-epic-b-30 außerhalb der Kriterien; Post-Trial-Limits UNKLAR (E-34 vorläufig).
- **Prüfebenen:** UX: OK – Real-E2E; API: OK – RFC 7807; Daten: OK – atomar; E2E: OK; Robustheit: LÜCKE – Slug-Kappung 20, paralleles Verify ungetestet
- **Abhängigkeiten:** WP-082, WP-070, WP-071
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-303 (P3) Signup-Verifikation scheitert mit 500, wenn 20 Slug-Kandidaten belegt sind
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-112"></a>
#### WP-112 – Self-Signup-Schutz: Captcha, Pre-Auth-Limit, Wegwerf-Domain-Blocklist

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** TLC-01
- **Anforderung/Nutzerziel:** Signup ist durch Pre-Auth-Limit, ALTCHA, Blocklist und max. 5 Signups/IP/h geschützt; Ablehnungen als RFC 7807.
- **Implementierung:** backend/internal/signup/protect.go:34,113,170,187,270,291; middleware/ratelimit.go:211-253; main.go:942-962
- **Test(s):** results-backend.tsv pass 7 Protect-Tests (TestAltchaSolutionIsVerifiedOnce, TestProtectionLimitsSignupsPerIP, TestSignupHandlerProtectionAnswersRFC7807 …); Real-Stack-Probes SP/work/audit-B14/probe-preauth-signup.log, probe-altcha-blocklist.log, probe-altcha-iplimit.log
- **Soll/Ist:** Erfüllt und real belegt; Stundenlimit ist festes Uhrzeitfenster.
- **Evidenz:** '600 400 / 15 429'; Replay 400 altcha; mailinator 422 email; 5×202 dann 429 Retry-After 3600; SP/work/verify-B14/probe-signup-window.log (10 in 61 s)
- **Abweichung:** P3 L-T6-epic-b-31 (Festfenster); kein Regressionstest bindet /signup* an den Pre-Auth-Limiter.
- **Prüfebenen:** UX: OK; API: OK – 400/422/429; Daten: OK; E2E: OK – Real-Probes; Robustheit: LÜCKE – Festfenster
- **Abhängigkeiten:** WP-111, WP-050
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-304 (P3) Signup-Limit je IP als festes Uhrzeit-Stundenfenster: 10 Signups in 61 s möglich
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-113"></a>
#### WP-113 – Operator legt Organisationen mit Plan und Limits an

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** TLC-02
- **Anforderung/Nutzerziel:** Operator legt über /admin/orgs Org mit Plan und Limits an (Signup-Kette ohne Verifikation, operator_audit).
- **Implementierung:** backend/internal/operator/orgs.go:14-84, handler.go:44-45; signup/service.go:325-370; server/router.go:263-272
- **Test(s):** results-backend.tsv pass TestOperatorCreatesOrganizations, TestOperatorCreatesOrganizationAtomically, TestOperatorCreationRunsTheSignupChain, TestOperatorCreationValidates; SP/work/verify-B14/probe-operator-http.log
- **Soll/Ist:** Erfüllt: HTTP→Middleware→Signup-Kette→PG mit allen Artefakten; 401/409/400 korrekt.
- **Evidenz:** probe-operator-http.log 'org plan=pro defaultVRF=1 org_admins=1 cmdb_core.max_cis=5000 operator_audit org.create=1'; Real-Stack 401×2
- **Abweichung:** -
- **Prüfebenen:** UX: N/A; API: OK; Daten: OK – atomar; E2E: LÜCKE – keine echte Operator-Session (nicht konfiguriert); Robustheit: OK
- **Abhängigkeiten:** WP-111, WP-074, WP-070
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-114"></a>
#### WP-114 – Einladungen: Ausgabe, Annahme, Erneuerung; 7 Tage, atomar einmalig

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P0 · **Anforderungen:** API-05, TLC-03
- **Anforderung/Nutzerziel:** Admin lädt mit Rolle/Scope ein (7 Tage, einmalig); Eingeladener nimmt an und erhält app_user mit Rolle/Scope; Erneuerung möglich; Annahme hinter Pre-Auth-Limit.
- **Implementierung:** backend/internal/user/invitation.go:31,130,203-232,235-262 (keine Grant-Obergrenze),287,348; user/handler.go:712 AssignRole; invitation_handler.go:91; middleware/ratelimit.go:247; kein Frontend (UserManagementPage.tsx read-only)
- **Test(s):** results-backend.tsv pass TestInvitationsAgainstPostgres; Real-Stack roles-authz 01–04, 06, 07, 10 pass, 05 FAIL; Welle fails-as-expected: TestAuditDefectInvitationGrantCeiling, TestAuditDefectRoleAssignmentGrantCeiling, TestAuditDefectInvitationCreatedEvent, UserManagementPage.auditdefect.tsx
- **Soll/Ist:** Beide Kriterien real belegt. Nutzerprozess unvollständig (keine Mail, keine UI/Accept-Seite); Privilegieneskalation über fehlende Grant-Obergrenze.
- **Evidenz:** SP/roles-authz-run5.log; SP/work/audit-B14/probe-preauth-accept.log ('600 404 / 15 429'); SP/work/verify-B14/probe-invite-ceiling.log ('issues org_admin/org invitation: 201'); WT invitation_auditdefect_test.go:150,211,279
- **Abweichung:** Bestätigte P1-Eskalation, P1 fehlende Einladungsmail, P2 fehlende UI, P3 Rolle/Scope-Mismatch.
- **Prüfebenen:** UX: FEHLER – keine Einladungs-UI; API: FEHLER – keine Grant-Obergrenze; Daten: OK – Hash, atomar, Isolation; E2E: FEHLER – Test 05; Robustheit: LÜCKE – Rolle/Scope-Mismatch
- **Abhängigkeiten:** WP-043, WP-050, WP-140, WP-109
- **Defekte:** F-015 (P0) Keine Grant-Obergrenze: client-gescopter user:manage-Inhaber erzeugt org-weite org_admin-Einladungen und -Rollenzuweisungen, F-041 (P1) Einladungen versenden keine E-Mail: invitation.created (NTF-05 'Einladung') wird nie erzeugt, F-142 (P2) Keine UI zum Ausstellen, Erneuern oder Annehmen von Einladungen, F-305 (P3) Einladung akzeptiert Client-Rolle mit Org-Scope und erzeugt Nutzer ohne wirksame Rechte
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Grant-Ceiling-Defekttests grün; Einladungs-Event-Test und roles-authz 05 grün; UI-Defekttest grün plus e2e-real UI-Einladung; Pre-Auth-Regressionstest für /invitations/accept.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Der zugeordnete L-T6-epic-b-02 wird P0 (Privilegieneskalation von Client-Scope auf org-weiten org_admin; siehe defekt_korrekturen). WP-Priorität = höchste Abweichung.

<a id="wp-115"></a>
#### WP-115 – CI-Typen: i18n-Labels, is_observable, schema_version; logische Seed-Typen

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** MET-01, MET-02, MET-03
- **Anforderung/Nutzerziel:** CI-Typen haben de/en-Labels, is_observable und schema_version; genau 17 physische und 6 logische Seeds, keine ausgeschlossenen Typen.
- **Implementierung:** backend/migrations/000104_ci_type_fields_seeds.up.sql:15-80; citype/model.go:25-48,139-169; citype/templates.go:216; lifecycle/offline.go:143; frontend/src/pages/CITypeAdminPage.tsx:56,165-180
- **Test(s):** results-backend.tsv pass TestSystemTypeSeedsAndFields, TestNewTypeWithoutReleaseAgainstPostgres, TestAttributeDefinitionsAgainstPostgres, TestOfflineDetector; Real-Stack-Probe SP/work/verify-B14/pw-citypes.log pass
- **Soll/Ist:** Erfüllt, inkl. UI-Anzeige gegen echte API.
- **Evidenz:** psql audit_db_13: 17 physisch/6 logisch; seed_integration_test.go:84-89 (missing/surplus); pw-citypes.log 1 passed
- **Abweichung:** -
- **Prüfebenen:** UX: OK – Real-Probe; API: OK; Daten: OK; E2E: OK – Lesepfad; Robustheit: OK
- **Abhängigkeiten:** WP-024, WP-116, WP-119
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-116"></a>
#### WP-116 – attribute_definition als einzige Quelle; generiertes read-only attribute_schema

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** DB-03, MET-10, API-05
- **Anforderung/Nutzerziel:** Attribute zentral in attribute_definition (MET-10), attribute_schema generiert und nicht schreibbar, Ressource /api/v1/attribute-definitions.
- **Implementierung:** backend/migrations/000105_attribute_definition.up.sql:19-238; citype/handler.go:63-67; citype/attribute_definition.go:44-71,142-146; citype/resolver.go:52-62 (ci_instance_field_definition bleibt)
- **Test(s):** results-backend.tsv pass TestAttributeDefinitionsAgainstPostgres, Route-Permission-Tests; Welle TestAuditT6AttributeDefinitionsAuthzAndIsolation PASS (Review ok; schließt Authz- und Cross-Tenant-Lücke); SP/work/verify-B15/probe-attrdef.log
- **Soll/Ist:** Tabelle, MET-10-Spalten, Generator, Schreibschutz, Ressource belegt. Instanzdefinitionen nicht übernommen; validation nicht MET-45-AST.
- **Evidenz:** attrdef_integration_test.go:60-77,136-146; probe-attrdef.log (AST 400 'unknown field op'); SP/work/review-T6-epic-b/defect-run.log:134-141
- **Abweichung:** Kriterium 1 nur teilweise (P3 ×2).
- **Prüfebenen:** UX: LÜCKE – Feldadmin nur gemockt; API: OK – Authz/Isolation jetzt per Router-Test; Daten: LÜCKE – zweite Definitionsquelle; E2E: LÜCKE – kein e2e-real; Robustheit: OK
- **Abhängigkeiten:** WP-115, WP-091, WP-119, Phase-2 WP-256
- **Defekte:** F-306 (P3) Instanzdefinitionen nicht in attribute_definition übernommen (zweite Definitionsquelle), F-307 (P3) attribute_definition.validation akzeptiert den MET-45-Regel-AST nicht; OpenAPI beschreibt Form nicht
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** E-nn-Entscheid oder Migration der Instanzdefinitionen; AST-Annahme oder dokumentierter Legacy-Entscheid mit OpenAPI-Beschreibung.

<a id="wp-117"></a>
#### WP-117 – Serverseitige Validierung aller Phase-1-Datentypen inkl. typisierter CI-Felder

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** MET-11, CI-03
- **Anforderung/Nutzerziel:** Jeder CI-Wert (manuell oder Discovery) wird serverseitig gegen MET-11 und Feldregeln geprüft; manuelle Anlage mit Name, Typ, Attributen funktioniert (CI-03).
- **Implementierung:** backend/internal/fieldmeta/fieldmeta.go:88-113,446-560; ci/validation.go:261-268,318-383; discovery/reconciliation.go:353-393, devicerecord.go:83-120; frontend/src/pages/CIFormModal.tsx:30,146
- **Test(s):** results-backend.tsv pass TestPhase1TypesFollowMET11, TestValidateValuePerMET11Type, TestTypedColumnsTakePartInValidation, TestIngestValidatesObservedValues; Real-Stack ci-lifecycle L02 pass (Formular-/IP-422), L01 FAIL, L14 FAIL, tenant-isolation T16 FAIL; Welle TestAuditDefectDiscoveryTypedValuesValidated, CIFormModal.auditdefect.tsx fails-as-expected
- **Soll/Ist:** Typen und Typed-Column-Regeln für manuelle API-Writes korrekt; Discovery speichert MET-11-ungültige Werte oder verwirft ganze Batches; manuelle Anlage über UI unmöglich.
- **Evidenz:** SP/work/verify-B15/probe-discovery-text.log, probe-discovery-mixed.log; WT typedvalues_auditdefect_test.go:112,125,164; SP/work/ci-lifecycle/final.log:24-40 (400)
- **Abweichung:** Kriterium 3 verletzt (beobachtet); CI-03 in der UI blockiert (P0, L-T6-epic-b-04). Zusätzlich bestätigt L-T6-epic-b-12 (P1): Werte des MET-11-Typs multiline mit Zeilenumbruch und Texte mit Anführungszeichen scheitern an audit_entry_hash (22P02 → 400). Das berührt Kriterium 1 teilweise.
- **Prüfebenen:** UX: FEHLER – CI-Dialog 400; API: FEHLER – Ingest ohne Typed-Validierung; Daten: FEHLER – mehrzeilige hostname/fqdn persistiert; E2E: FEHLER – L01/T16; Robustheit: FEHLER – ein Feld kippt Batch
- **Abhängigkeiten:** WP-116, WP-120, WP-239
- **Defekte:** F-016 (P0) CI-Dialog 'CI erstellen' nutzt hart kodierte Typ-Keys: sendet 'server' als ci_type_id (400) und bietet keine Laufzeit-/Org-Typen, F-045 (P1) Werte mit Anführungszeichen oder Steuerzeichen (Zeilenumbruch, Tab) brechen jeden auditierten Write mit Wert-Snapshot (CI-Create/-Update/-Delete/-Restore, Metamodell) mit 400 ab: audit_entry_hash castet den JSON-Text nach bytea, F-143 (P2) Discovery überspringt Datentypvalidierung typisierter CI-Spalten: ungültige Werte persistiert oder ganze Batches verworfen
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Discovery-Defekttest grün inkl. Queue-Poison-Test; CIFormModal-Defekttest und e2e-real L01/T16/L14 grün.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Der zugeordnete L-T6-epic-b-04 wird P0 (zentraler Prozess blockiert; siehe defekt_korrekturen).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „Kriterium 3 verletzt (beobachtet); CI-03 in der UI blockiert.“ → „Kriterium 3 verletzt (beobachtet); CI-03 in der UI blockiert (P0, L-T6-epic-b-04). Zusätzlich bestätigt L-T6-epic-b-12 (P1): Werte des MET-11-Typs multiline mit Zeilenumbruch und Texte mit Anführungszeichen scheitern an audit_entry_hash (22P02 → 400). Das berührt Kriterium 1 teilweise.“. Die bestätigte Ursache von L-12 betrifft den MET-11-Typ multiline: Ein LF im CI-Snapshot bricht den Audit-Insert ab (verify-L12-bytea.log, Fall 'nested CI snapshot with description newline'). DynamicForm.tsx:104 rendert multiline als mehrzeilige Eingabe.

<a id="wp-118"></a>
#### WP-118 – Regel-AST: api/rules.schema.json, Go-Auswertung, TS-Vorschau, gemeinsame Konformanzfälle

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** MET-45, REP-01
- **Anforderung/Nutzerziel:** Eine Regelsprache (required/pattern/range/enum), verbindlich in Go, Vorschau in TS, gemeinsame Konformanzfälle.
- **Implementierung:** api/rules.schema.json; api/rules.conformance.json (39+12); backend/internal/rules/rules.go; fieldmeta.go:446-459; frontend/src/rules/rules.ts; frontend/src/lib/fieldmeta.ts:215,234
- **Test(s):** results-backend.tsv 53 pass internal/rules (TestConformance, TestSchemaMatchesOperators); results-frontend.json rules.test.ts 52/52, fieldmeta.test.ts 23/23
- **Soll/Ist:** Erfüllt; beide Läufer nutzen identische Fälle.
- **Evidenz:** rules_test.go:27,43-75; rules.test.ts:6-39
- **Abweichung:** -
- **Prüfebenen:** UX: LÜCKE – kein Komponententest DynamicForm (nicht verbindlich); API: N/A; Daten: N/A; E2E: N/A; Robustheit: OK
- **Abhängigkeiten:** WP-117, WP-116
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-119"></a>
#### WP-119 – Schema-Komposition Basis + Typkette, Materialisierung, schema_version im CI

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P0 · **Anforderungen:** MET-16, MET-04
- **Anforderung/Nutzerziel:** Effektives Schema nach MET-16 komponiert, materialisiert und versioniert; CI speichert schema_version; neue Typen ohne Release nutzbar (MET-04).
- **Implementierung:** backend/internal/citype/compose.go:34, resolver.go:27-62; backend/migrations/000107_ci_schema_version.up.sql:311-324; frontend/src/pages/CIFormModal.tsx:30,66-75
- **Test(s):** results-backend.tsv pass TestComposeRules, TestComposeConflicts, TestResolveFieldsComposesLayers, TestNewTypeWithoutReleaseAgainstPostgres; SP/work/verify-B15/schema-version-typed.log; Real-Stack ci-lifecycle L01 FAIL; Welle CIFormModal.auditdefect.tsx ('offers the org type created at runtime') fails-as-expected
- **Soll/Ist:** Komposition/Materialisierung auf PG belegt; neue Typen im UI nicht wählbar (hart kodierte Liste); schema_version unvollständig.
- **Evidenz:** new_type_integration_test.go:71-172; schema-version-typed.log (CI v2 bei Typ v4); CIFormModal.auditdefect.tsx AssertionError 'Audit Custom PDU' nicht angeboten
- **Abweichung:** MET-04 in der UI nicht erfüllt; Priorität P2→P1, weil der MET-04-Befund mit dem P1-CIFormModal-Defekt (gleiche Ursache) zusammengeführt wurde.
- **Prüfebenen:** UX: FEHLER – Typliste kodiert; API: OK – 422 bei Konflikt; Daten: LÜCKE – org-global/Typed-Writes; E2E: FEHLER – L01; Robustheit: OK
- **Abhängigkeiten:** WP-118, WP-116, WP-120, WP-239
- **Defekte:** F-016 (P0) CI-Dialog 'CI erstellen' nutzt hart kodierte Typ-Keys: sendet 'server' als ci_type_id (400) und bietet keine Laufzeit-/Org-Typen, F-308 (P3) Org-globale Attributdefinitionen werden erzwungen, aber nicht materialisiert/versioniert, F-309 (P3) ci.schema_version wird bei reinen Typed-Column-Writes nicht aktualisiert
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** CIFormModal-Defekttest grün und e2e-real 'neuer Typ → CI im Dialog'; Entscheid/Test org-global-Schicht; schema_version bei Typed-Writes.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Die WP-Priorität ergibt sich hier aus dem zusammengeführten L-T6-epic-b-04, der P0 wird.

<a id="wp-120"></a>
#### WP-120 – CI-Felder und Create-Pfad nach CI-01

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P0 · **Anforderungen:** CI-01
- **Anforderung/Nutzerziel:** CI hat alle CI-01-Felder; manuelle Anlage übernimmt alle Felder und setzt is_manual.
- **Implementierung:** backend/migrations/000108_ci_fields_ci01.up.sql; ci/model.go:95-117; ci/handler.go:400-444; ci_ci_type_id_fkey einspaltig; frontend/src/pages/CIFormModal.tsx:30,146
- **Test(s):** results-backend.tsv pass TestCreateStoresTheFieldsOfCI01, TestHandler_CreateTakesTheFieldsOfCI01; Real-Stack ci-lifecycle L01/L03a/L14 FAIL, roles-authz 12 FAIL; Welle TestAuditDefectForeignPrivateCITypeRejected, CIFormModal.auditdefect.tsx fails-as-expected
- **Soll/Ist:** API-Create-Pfad erfüllt CI-01 auf PG; UI-Anlage scheitert immer (400); fremder privater CI-Typ wird akzeptiert.
- **Evidenz:** create_fields_integration_test.go:26-105; SP/work/verify-B15/probe-ci-ui-payload.log (UUID 201, 'server' 400); WT audit_t6_router_auditdefect_test.go:351 (201 statt 422)
- **Abweichung:** Kriterium 2 nur für die API erfüllt (P0 UI, L-T6-epic-b-04), P2 Cross-Org-Typreferenz (L-T6-epic-b-13), P1 (bestätigt) Anführungszeichen und Steuerzeichen (L-T6-epic-b-12).
- **Prüfebenen:** UX: FEHLER – Dialog 400, Felder fehlen; API: FEHLER – fremder Typ 201; Daten: FEHLER – Cross-Org-Referenz; E2E: FEHLER – L01; Robustheit: FEHLER – Existenzorakel
- **Abhängigkeiten:** WP-080, WP-117, WP-239
- **Defekte:** F-016 (P0) CI-Dialog 'CI erstellen' nutzt hart kodierte Typ-Keys: sendet 'server' als ci_type_id (400) und bietet keine Laufzeit-/Org-Typen, F-045 (P1) Werte mit Anführungszeichen oder Steuerzeichen (Zeilenumbruch, Tab) brechen jeden auditierten Write mit Wert-Snapshot (CI-Create/-Update/-Delete/-Restore, Metamodell) mit 400 ab: audit_entry_hash castet den JSON-Text nach bytea, F-144 (P2) POST /cis akzeptiert privaten CI-Typ einer fremden Organisation
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** e2e-real L01/L03a/L14 und roles-authz 12 grün; CIFormModal- und ForeignPrivateCIType-Defekttests grün.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Der zugeordnete L-T6-epic-b-04 wird P0.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „Kriterium 2 nur für API erfüllt (P1 UI), P2 Cross-Org-Typreferenz, P2 (unbestätigt) Anführungszeichen.“ → „Kriterium 2 nur für die API erfüllt (P0 UI, L-T6-epic-b-04), P2 Cross-Org-Typreferenz (L-T6-epic-b-13), P1 (bestätigt) Anführungszeichen und Steuerzeichen (L-T6-epic-b-12).“. Folgt aus den Prioritätskorrekturen von L-04 und L-12 sowie der Bestätigung von L-12.

<a id="wp-121"></a>
#### WP-121 – CI-Indizes nach CI-02

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** CI-02
- **Anforderung/Nutzerziel:** CI-Abfragen sind durch die CI-02-Indizes gestützt; Migration ohne lange Sperren (dokumentiert).
- **Implementierung:** backend/migrations/000109_ci_indexes.up.sql:14-22; docs/schema-baseline.md:55-75
- **Test(s):** results-backend.tsv pass TestCIIndexesFollowCI02 (Regex auf indexdef); Re-Apply 000109 auf audit_db_14 idempotent
- **Soll/Ist:** Erfüllt.
- **Evidenz:** indexes_integration_test.go; psql pg_indexes
- **Abweichung:** -
- **Prüfebenen:** UX: N/A; API: N/A; Daten: OK; E2E: N/A; Robustheit: OK – IF NOT EXISTS
- **Abhängigkeiten:** WP-079
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-122"></a>
#### WP-122 – Nachweis Trennung Status/Lifecycle (LCY-01)

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** LCY-01
- **Anforderung/Nutzerziel:** ci.status und lifecycle_state sind getrennte Zustandsräume; Änderung des einen ändert nie den anderen.
- **Implementierung:** backend/internal/lifecycle/service.go:35-46; server/router.go:446; lifecycle/offline.go:171-178
- **Test(s):** results-backend.tsv pass TestStatusAndLifecycleAreSeparate (direkte SQL-Lesungen)
- **Soll/Ist:** Erfüllt.
- **Evidenz:** status_separation_integration_test.go
- **Abweichung:** -
- **Prüfebenen:** UX: N/A; API: LÜCKE – nicht über HTTP (nicht verbindlich); Daten: OK; E2E: N/A; Robustheit: OK
- **Abhängigkeiten:** WP-080
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-123"></a>
#### WP-123 – Interfacemodell nach NET-01

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** NET-01
- **Anforderung/Nutzerziel:** Netzwerk-Interfaces haben exakt die NET-01-Felder, Name eindeutig je CI, (org, mac)-Index.
- **Implementierung:** backend/migrations/000110_interface_net01.up.sql:7-20; ipam/pg_repository.go:424-437; ipam/handler.go:458-463; api/dberrors.go:83-88
- **Test(s):** results-backend.tsv pass TestInterfaceModelFollowsNET01, TestInterfaceCRUD, TestIPAMRepositoryClientScope; SP/work/verify-B15/net01-dup.log, probe-ipam-xtenant.log
- **Soll/Ist:** Kriterium erfüllt; zwei P3-Randfehler.
- **Evidenz:** interface_integration_test.go:26-138; net01-dup.log ('could not create unique index'); probe-ipam-xtenant.log (POST fremdes CI 500)
- **Abweichung:** P3 L-T6-epic-b-37, L-T6-epic-b-38 außerhalb des Kriteriums.
- **Prüfebenen:** UX: N/A; API: OK (500 statt 404 minor); Daten: OK; E2E: N/A; Robustheit: LÜCKE – Dedupe-Kollision
- **Abhängigkeiten:** WP-082, WP-090
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-310 (P3) Migration 000110 bricht ab, wenn ein suffigierter Duplikatname bereits existiert, F-311 (P3) Interface-Anlage auf fremdem CI antwortet 500 statt 404
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-124"></a>
#### WP-124 – IP-Adressen nach NET-03 (Typ, VRF in Teilindizes)

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** NET-03
- **Anforderung/Nutzerziel:** IP-Adressen tragen type statt status, Datenübernahme korrekt, Eindeutigkeit je Org/VRF/Adresse/Interface.
- **Implementierung:** backend/migrations/000111_ip_address_net03.up.sql:9-21; ipam/pg_repository.go:557-609; ipam/handler.go:509-519
- **Test(s):** results-backend.tsv pass TestIPAddressesFollowNET03 (+/data_transfer), TestVRFModel; SP/work/verify-B15/net03-uniq.log, probe-ipam-delete.log
- **Soll/Ist:** Erfüllt; P3-Interaktion ON DELETE SET NULL/Teilindex.
- **Evidenz:** ip_integration_test.go:28-161; probe-ipam-delete.log (DELETE 404, GET 200)
- **Abweichung:** P3 L-T6-epic-b-39 außerhalb des Kriteriums.
- **Prüfebenen:** UX: N/A; API: OK; Daten: OK; E2E: N/A; Robustheit: LÜCKE – Interface-Löschung
- **Abhängigkeiten:** WP-123, WP-082
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-312 (P3) Interface-Löschung scheitert (irreführende 404), wenn dieselbe Adresse ungebunden im selben VRF existiert
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-125"></a>
#### WP-125 – REST-Ressourcen vrfs und vlans

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** NET-05, API-05
- **Anforderung/Nutzerziel:** VRF (vrf:manage) und VLAN (ipam:write) per REST-CRUD mit Authz, OpenAPI und Frontend-Typen.
- **Implementierung:** backend/internal/ipam/vrf_handler.go, vlan_handler.go, handler.go:47-74; server/authz.go:87-88,137; api/openapi.yaml:6546-6701; frontend/src/api/generated/schema.d.ts:3425-3486
- **Test(s):** results-backend.tsv pass TestVRFResourceCRUD, TestVLANResourceCRUD, TestVRFModel, TestVLANCatalog, TestAuthorizationMiddlewareEnforcesPermissions (vrfs/vlans), TestRoutesAndSpecificationAreInParity; SP/work/verify-B16/probe/run-main.log
- **Soll/Ist:** Erfüllt.
- **Evidenz:** run-main.log 'POST /vrfs ipam:write only -> 403', 'rows 2/2'; generate:api:check exit 0
- **Abweichung:** -
- **Prüfebenen:** UX: N/A; API: OK; Daten: OK; E2E: N/A; Robustheit: OK
- **Abhängigkeiten:** WP-082, WP-081, WP-045
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-126"></a>
#### WP-126 – Rack-Belegung: 409 und Sperre für Kompositionskinder

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** RCK-01
- **Anforderung/Nutzerziel:** Belegte, kollidierende oder zu große Positionen → 409; Face-aware-Exclusion in DB; Kompositionskinder nicht separat montierbar.
- **Implementierung:** backend/migrations/000112_rack_mount_rules.up.sql; rack/repository.go:58-75,249; rack/pg_repository.go:278-389,558-566
- **Test(s):** results-backend.tsv pass TestRackRulesAgainstPostgres, TestTST01FaceAwareExclusion (9/9); Real-Stack locations-racks L07 pass; SP/work/verify-B16/probe/run-main.log
- **Soll/Ist:** Erfüllt bis Real-Stack.
- **Evidenz:** SP/work/verify-locrack/final.log L07; run-main.log 'PUT m2 U2 both -> 409'
- **Abweichung:** -
- **Prüfebenen:** UX: N/A; API: OK; Daten: OK; E2E: OK – L07; Robustheit: OK
- **Abhängigkeiten:** WP-078, WP-079, WP-175; Shrink-Pfad der Rackhöhe ist WP-108 (L-T6-epic-b-08)
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-127"></a>
#### WP-127 – Beziehungstypen: impact_direction, redundant, Kategorien; connected_to ungerichtet

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** REL-01
- **Anforderung/Nutzerziel:** Beziehungstypen tragen den REL-01-Vertrag; Seeds korrigiert; Strukturschlüssel nicht speicherbar.
- **Implementierung:** backend/migrations/000113_relationship_type_rel01.up.sql; relationshiptype/model.go:29-31,80-90; relationship/relationship.go:539-548; frontend/src/components/cmdb/CIRelationshipsCard.tsx:34-47
- **Test(s):** results-backend.tsv pass TestRelationshipTypeSeedsFollowREL01, TestRelationshipTypeRepositoryScope, Middleware-403-Subtests; Real-Stack ci-lifecycle L08 pass; SP/work/verify-B16/probe/run-main.log
- **Soll/Ist:** Kriterien erfüllt; UI-Typliste kodiert, allowed_*-Felder wirkungslos (außerhalb AK).
- **Evidenz:** run-main.log 'key mounted_in -> 400', 'rel_type=mounted_in -> 422'
- **Abweichung:** P3 ×2 außerhalb der Kriterien.
- **Prüfebenen:** UX: LÜCKE – kodierte Typliste; API: OK; Daten: OK; E2E: N/A; Robustheit: OK
- **Abhängigkeiten:** WP-079, WP-176, WP-160
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-242 (P3) Beziehungsformular bietet strukturelle Typen mounted_in/located_in an (immer 422), F-313 (P3) allowed_source_types/allowed_target_types/cardinality werden gespeichert, aber nie durchgesetzt
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-128"></a>
#### WP-128 – Beziehungskanten: Interface-Endpunkte, Bestätigung, Verifikation, Confidence-Spalte, Indizes

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** REL-02
- **Anforderung/Nutzerziel:** Kanten nach REL-02 mit Interface-Endpunkten, Verifikation, Confidence-Spalte mit CHECK, Eindeutigkeit inkl. Interfaces; Port-Kanten bleiben verlässlich.
- **Implementierung:** backend/migrations/000114_relationship_rel02.up.sql (FKs ON DELETE CASCADE); relationship/pg_repository.go:157-231 (Z.222 fmt.Errorf('not found')); ipam/pg_repository.go:630-686 missAndRemove
- **Test(s):** results-backend.tsv pass TestRelationshipEdgesFollowREL02 (+/data_transfer); Real-Stack T02, T11, L08 pass, T14 FAIL (500); Welle TestAuditDefectManualEdgeSurvivesInterfaceSync fails-as-expected
- **Soll/Ist:** Schema-Vertrag belegt; Kaskade löscht manuelle verifizierte Kanten bei Discovery-Interface-Entfernung (REL-05); fremde/unbekannte CI → 500.
- **Evidenz:** SP/work/verify-B16/probe/run-TestB16VManualEdgeInterfaceSync.log ('manual edge rows=0'); WT audit_t6_router_auditdefect_test.go:89; tenant-isolation T14 '…: 500'
- **Abweichung:** P1 Kaskade durch eigene FK-Definition; P2 500 statt 4xx; P3 (unbestätigt) Dialog nach Doppelklick.
- **Prüfebenen:** UX: LÜCKE – generische 500-Meldung im Dialog; API: FEHLER – 500; Daten: FEHLER – stiller Kantenverlust; E2E: OK/FEHLER – T02/L08 pass, T14 fail; Robustheit: FEHLER – Kaskade
- **Abhängigkeiten:** WP-127, WP-123, WP-090, WP-160; WP-010/WP-163/WP-208 (500-Defekt)
- **Defekte:** F-042 (P1) Manuelle verifizierte Port-Kanten werden bei Discovery-Interface-Entfernung per ON DELETE CASCADE still gelöscht, F-145 (P2) POST /relationships mit CI einer anderen Organisation oder unbekannter CI-ID antwortet 500 statt 404/422, F-314 (P3) Beziehungsdialog bleibt nach Doppelklick trotz erfolgreicher Anlage mit Konfliktfehler offen (intermittierend)
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** ManualEdge-Defekttest grün; T14 grün (404/422); L08 stabil ohne Soft-Fehler.

<a id="wp-129"></a>
#### WP-129 – Generisches Jobmodell backend/internal/jobs

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** JOB-01, REP-01
- **Anforderung/Nutzerziel:** Eine generische, mandantengetrennte Jobtabelle, in der auch Export- und Discovery-Jobs erscheinen (Job Center, /api/v1/jobs).
- **Implementierung:** backend/migrations/000115_job.up.sql; jobs/pg_repository.go:61,321, service.go:261,273; export/job_handler.go:96-100; discovery/jobs.go:196-209 (WithJobTracker ohne Aufrufer); server/router.go:360-387
- **Test(s):** results-backend.tsv pass TestJobsAgainstPostgres, TestJobLifecycle, TestTrackerMirrorsSpecialisedJobs; Real-Stack E01 und J01 pass; Welle TestAuditDefectDiscoveryJobInGenericJobModel fails-as-expected
- **Soll/Ist:** Tabelle, RLS, Service, Export-Adapter real belegt; Discovery-Adapter nicht verdrahtet.
- **Evidenz:** SP/work/verify-B16/probe/run-main.log ('job rows … 0'); WT audit_t6_router_auditdefect_test.go:273
- **Abweichung:** Teilkriterium Discovery-Adapter verletzt (P2).
- **Prüfebenen:** UX: LÜCKE – Discovery-Jobs fehlen im Job Center; API: FEHLER – fehlende Discovery-Jobs; Daten: OK; E2E: OK Export/Bulk, FEHLER Discovery; Robustheit: LÜCKE – Tracker-Fehler nur geloggt
- **Abhängigkeiten:** WP-065, WP-182, WP-130
- **Defekte:** F-146 (P2) Discovery-Jobs werden nicht im generischen Jobmodell registriert (Adapter nicht verdrahtet)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Defekttest grün inkl. Spiegelung bis succeeded und Org-B 404.

<a id="wp-130"></a>
#### WP-130 – NATS-Work-Queue für Jobs; Webhook-Erstversand-Claim; Export-Wiederaufnahme

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** TEC-14
- **Anforderung/Nutzerziel:** Jobs laufen über JetStream-Work-Queue mit Ack/Redelivery; Erstversand von Webhooks mit Claim; Exporte setzen nach Neustart fort.
- **Implementierung:** backend/internal/jobs/queue.go:89-326 (keine registrierten Kinds); cmd/server/main.go:268-300,599-600; ci/bulk.go:113-125; webhook/dispatcher.go:316-331; export/worker.go:185-214, pg_job_repository.go:270
- **Test(s):** results-backend.tsv pass TestQueueOverNATS, TestQueueRunsAJobOnce, TestQueueRetriesAndRecoversLostWorkers, TestDispatcherClaimsTheFirstAttempt, TestExportJobRunsWithCreatorScope; SP/work/verify-B16/probe/run-webhook.log, run-main.log
- **Soll/Ist:** Framework, Claim, Webhook-Lease (PG-Probe) und Export-Wiederaufnahme vorhanden; produktiv nutzt kein Job die Queue, Bulk/Snapshot laufen als Goroutinen ohne Reaper.
- **Evidenz:** run-main.log 'bulk job … final status without NATS: succeeded'; grep: jobs.Queue nur main.go:270, kein Handle/Submit/ReapTimeouts
- **Abweichung:** Kriterium 1 nur teilweise (P2); P3-Hypothese Export-Reclaim.
- **Prüfebenen:** UX: N/A; API: N/A; Daten: OK – Claims; E2E: LÜCKE – kein Job über NATS; Robustheit: FEHLER – Jobs bleiben nach Crash 'running'
- **Abhängigkeiten:** WP-129, WP-098, WP-182/183
- **Defekte:** F-147 (P2) Kein Produktionsjob nutzt die NATS-Work-Queue; Bulk/Snapshot laufen als Goroutinen und bleiben nach Crash 'running', F-315 (P3) Export-Stale-Reclaim kann lange Exporte doppelt ausführen
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Wiring-Test registrierter Kinds und Crash-Recovery-Test grün; PG-Zwei-Dispatcher-Test committen.

<a id="wp-131"></a>
#### WP-131 – Scheduler mit Single-Runner (Leader-Lock)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** JOB-02, OPS-03
- **Anforderung/Nutzerziel:** Gemeinsamer Scheduler führt die JOB-02-Systemläufe aus; genau eine Replika führt per Advisory-Lock; abschaltbar (OPS-02).
- **Implementierung:** backend/internal/jobs/scheduler.go:113-381, cron.go; migrations/000116_scheduler_run; cmd/server/main.go:468-532 (keine Entitlement-/Zertifikatsläufe)
- **Test(s):** results-backend.tsv pass TestSchedulerReplicasAgainstPostgres, TestSchedulerSingleRunnerAndCatchUp, TestSpecNext, TestEveryRegisteredWorkerHasASwitch, TestDisabledWorkerDoesNotStart
- **Soll/Ist:** Scheduler, Single-Runner und Schalter belegt; JOB-02-Läufe Entitlement-Warnungen und Zertifikatserneuerung fehlen.
- **Evidenz:** main.go:476-515 systemJobs; grep entitlement.warning/certificate.expiring nur Deklarationen
- **Abweichung:** Kriterium 1 (registrierte Läufe) nur teilweise; Priorität P2→P1 durch Zusammenführung mit den P1-Befunden von WP-136/WP-140 (gleiche Ursache).
- **Prüfebenen:** UX: N/A; API: N/A; Daten: OK; E2E: N/A; Robustheit: OK – Leaderwechsel getestet, LÜCKE – Registrierung ungetestet
- **Abhängigkeiten:** WP-130, WP-135, WP-140, WP-136
- **Defekte:** F-044 (P1) Entitlement-Warnung/-Ablauf und Zertifikatsablauf: JOB-02-Läufe nicht eingeplant, Events entitlement.warning/expired und certificate.expiring nie erzeugt
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Entitlement-/Zertifikatsläufe implementiert, PG-Tests und Wiring-Test der Kinds grün.

<a id="wp-132"></a>
#### WP-132 – Audit-Hashkette je Org mit Sequenz und Prüfwerkzeug

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** AUD-04
- **Anforderung/Nutzerziel:** Jede Org hat eine manipulationssichere Kette mit lückenloser Sequenz; audit-verify meldet die erste Bruchstelle; keine Gabelung bei Nebenläufigkeit.
- **Implementierung:** backend/migrations/000117_audit_chain_per_org.up.sql:55, 000118_audit_fields.up.sql:39-53 (audit_entry_hash, Cast ::bytea); audit/audit.go:379-430; cmd/audit-verify/main.go:23-84
- **Test(s):** results-backend.tsv pass TestAuditChainPerOrganizationPG, TestPGRecorderChainRoundtripPG, TestChainAfterRetention, TestRunReportsEveryOrganization; Real-Stack T03, T12, L06 pass; ci-lifecycle L14 FAIL (Wert mit Anführungszeichen 400); SP/work/verify-B16/cli-probe.log
- **Soll/Ist:** Alle Kriterien durch PG-Tests, Realbinary-CLI und Real-Stack (T12) belegt.
- **Evidenz:** cli-probe.log ('intact (4 entries)' exit 0; 'hash mismatch' exit 1; 'sequence gap' exit 1; exit 2)
- **Abweichung:** P1 L-T6-epic-b-12 (bestätigt): audit_entry_hash castet den kanonischen JSON-Text per ::bytea. Einträge mit Anführungszeichen oder Steuerzeichen werden nicht gekettet (22P02, Rollback der fachlichen Schreibtransaktion). Einträge mit Backslash werden nicht über das kanonische JSON gehasht (AUD-04-Formel verletzt). P3 L-T6-epic-b-44 (synchrone Kette statt Outbox, dokumentiert).
- **Prüfebenen:** Robustheit: FEHLER – die Kettenfunktion lehnt Anführungszeichen und Steuerzeichen ab und hasht Backslash-Werte nicht kanonisch (L-T6-epic-b-12, bestätigt)
- **Abhängigkeiten:** WP-065, WP-133, WP-135
- **Defekte:** F-045 (P1) Werte mit Anführungszeichen oder Steuerzeichen (Zeilenumbruch, Tab) brechen jeden auditierten Write mit Wert-Snapshot (CI-Create/-Update/-Delete/-Restore, Metamodell) mit 400 ab: audit_entry_hash castet den JSON-Text nach bytea, F-316 (P3) Audit-Kette synchron unter Org-Lock bis COMMIT statt asynchroner Outbox mit Single-Runner (AUD-04)
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final TEILWEISE BESTANDEN; BESTANDEN → TEILWEISE BESTANDEN: L-T6-epic-b-12 in der adversarialen Prüfung reproduziert. audit_entry_hash (000117:55 dieses WPs, 000118:53) wirft für Anführungszeichen, LF und TAB 'invalid input syntax for type bytea'. Bei Backslash weicht der Hash vom kanonischen JSON ab (SP/work/review-T6-epic-b/verify-L12-bytea.log). Ein bestätigter P1 im eigenen Kettencode des WPs; Kriterium 1 ist nur teilweise erfüllt.
- **Offene Nachweise / Retest:** Fix von L-T6-epic-b-12 (convert_to(…,'UTF8') statt ::bytea, Bestandsketten per Migration neu verketten) mit PG-Test: Einträge mit Anführungszeichen, LF, TAB und Backslash werden gekettet, audit.Verify ist intakt, und der DB-Hash entspricht audit.ComputeHash. Danach e2e-real ci-lifecycle L14 grün.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – status: „BESTANDEN“ → „TEILWEISE BESTANDEN“. L-T6-epic-b-12 ist in dieser Prüfung bestätigt (siehe defekt_korrekturen L-12). Den fehlerhaften Hash hat dieses WP selbst eingeführt: Migration 000117_audit_chain_per_org.up.sql:55 ('|| '}')::bytea'), 000118:53 übernimmt ihn. Belegt: Jeder Kettenkandidat, dessen kanonisches JSON ein Anführungszeichen oder ein Steuerzeichen (LF, TAB) enthält, bricht mit 22P02 ab, und die fachliche Transaktion rollt zurück. Bei Backslash-Werten hasht die Funktion 14 statt 15 Bytes (same_digest=f). Das ist nicht SHA-256(kanonisches JSON) nach AUD-04. Beleg: /tmp/claude-0/-home-user-Reticora-CMDB/fbd8a132-b968-552a-84eb-515fd3e8f807/scratchpad/work/review-T6-epic-b/verify-L12-bytea.log. Damit gilt Kriterium 1 ('je Org eigene Kette') nur für Einträge ohne diese Zeichen. Die Synthese hat diesen Fall selbst festgelegt ('bei Bestätigung → TEILWEISE BESTANDEN'). Nach ihrer für WP-108 angewandten Regel darf nur ein P3-Mangel BESTANDEN begleiten.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P3“ → „P1“. Höchste Abweichung ist jetzt der bestätigte L-T6-epic-b-12, neu P1 (siehe defekt_korrekturen).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – status_aenderung: „-“ → „BESTANDEN → TEILWEISE BESTANDEN: L-T6-epic-b-12 in der adversarialen Prüfung reproduziert. audit_entry_hash (000117:55 dieses WPs, 000118:53) wirft für Anführungszeichen, LF und TAB 'invalid input syntax for type bytea'. Bei Backslash weicht der Hash vom kanonischen JSON ab (SP/work/review-T6-epic-b/verify-L12-bytea.log). Ein bestätigter P1 im eigenen Kettencode des WPs; Kriterium 1 ist nur teilweise erfüllt.“. Die Statusänderung braucht nach der Synthesevorgabe eine Begründung. Die Evidenz ist der ausgeführte SQL-Lauf mit dem wörtlich extrahierten Funktionskörper auf PostgreSQL 16.15 + pgcrypto sowie der fehlgeschlagene Real-Stack-Test ci-lifecycle L14 (SP/work/ci-lifecycle/final.log:380,396).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „P3 L-T6-epic-b-44 (synchrone Kette statt Outbox, dokumentiert). Zusätzlich zugeordnet, aber UNBESTÄTIGT: L-T6-epic-b-12 (P2, Werte mit '"' scheitern am ::bytea-Cast in audit_entry_hash). Die Zuordnung hängt an der nicht verifizierten Ursache und begründet daher keine Statusänderung; bei Bestätigung → TEILWEISE BESTANDEN/P2.“ → „P1 L-T6-epic-b-12 (bestätigt): audit_entry_hash castet den kanonischen JSON-Text per ::bytea. Einträge mit Anführungszeichen oder Steuerzeichen werden nicht gekettet (22P02, Rollback der fachlichen Schreibtransaktion). Einträge mit Backslash werden nicht über das kanonische JSON gehasht (AUD-04-Formel verletzt). P3 L-T6-epic-b-44 (synchrone Kette statt Outbox, dokumentiert).“. Nach der Ursachenverifikation ist die Abweichung bestätigt und nicht mehr hypothetisch.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – ebenen: „Robustheit: OK, offen L-T6-epic-b-12“ → „Robustheit: FEHLER – die Kettenfunktion lehnt Anführungszeichen und Steuerzeichen ab und hasht Backslash-Werte nicht kanonisch (L-T6-epic-b-12, bestätigt)“. Folgt aus der bestätigten Ursache von L-T6-epic-b-12.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – offene_nachweise: „Verifikation von L-T6-epic-b-12 (Ursache audit_entry_hash) und nach Fix e2e-real L14 grün.“ → „Fix von L-T6-epic-b-12 (convert_to(…,'UTF8') statt ::bytea, Bestandsketten per Migration neu verketten) mit PG-Test: Einträge mit Anführungszeichen, LF, TAB und Backslash werden gekettet, audit.Verify ist intakt, und der DB-Hash entspricht audit.ComputeHash. Danach e2e-real ci-lifecycle L14 grün.“. Die Verifikation ist erledigt; offen ist nur der Fix-Nachweis.

<a id="wp-133"></a>
#### WP-133 – Audit-Felder und lückenlose Erfassung aller Mutationen

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** AUD-05, AUD-09
- **Anforderung/Nutzerziel:** Jede Mutation schreibt lückenlos einen Audit-Datensatz mit Pflichtfeldern inkl. Vorher/Nachher; Abfrage über GET /audit-log; Retention selbstauditiert.
- **Implementierung:** backend/migrations/000118_audit_fields.up.sql; audit/audit.go:32-50,546-613; middleware/middleware.go:88-142 (nach Response, eigene Tx, nur Feldnamen); server/router.go:612-633; frontend/src/api/client.ts:2085-2101, AuditPage.tsx:113-118
- **Test(s):** results-backend.tsv pass TestEveryWriteRouteIsAudited, TestWriteRequestsWriteAuditEntries, TestAuditFieldsAndFiltersPG, TestRetentionRunsAgainstPostgres; Real-Stack L06, L11, B01 pass, L07 und T17 FAIL; Welle TestAuditDefectRouteAuditStoresOldAndNewValues, TestAuditDefectRouteAuditFailureLeavesNoUnauditedMutation, AuditPage.auditdefect.tsx fails-as-expected
- **Soll/Ist:** Pflichtfelder, Architekturtest, API-Filter und Retention belegt; Route-Audit ohne Alt/Neu-Werte und nicht lückenlos; Audit-Viewer zeigt 'Invalid Date'/'—'.
- **Evidenz:** SP/work/verify-B16/probe/run-TestB16VAuditFailure.log ('vrf row persisted=1, audit rows=0'); run-main.log seq 3 changes={route,fields:[name]}; WT audit_t6_router_auditdefect_test.go:200,228
- **Abweichung:** P2 ×2 (eigene, L-17/L-18), P2 Viewer (ext), P1 bestätigt L-T6-epic-b-12 (000118 ist Migration dieses WPs). AK-Verweis 'Felder nach AUD-05' teils UNKLAR.
- **Prüfebenen:** UX: FEHLER – Viewer (ext); API: OK – Filter, audit:read; Daten: FEHLER – Lücke bei Audit-Fehler, keine Alt/Neu-Werte; E2E: FEHLER – L07/T17; Robustheit: FEHLER – best effort
- **Abhängigkeiten:** WP-132, WP-045, WP-134, WP-135, Audit-Viewer-WP (Epic F4)
- **Defekte:** F-045 (P1) Werte mit Anführungszeichen oder Steuerzeichen (Zeilenumbruch, Tab) brechen jeden auditierten Write mit Wert-Snapshot (CI-Create/-Update/-Delete/-Restore, Metamodell) mit 400 ab: audit_entry_hash castet den JSON-Text nach bytea, F-148 (P2) Route-Audit speichert nur Feldnamen, keine Vorher-/Nachher-Werte, F-149 (P2) Route-Audit nach der Response in separater Transaktion; Fehler nur geloggt (nicht lückenlos), extern: Audit page reads fields the API does not return (created_at/entity_type/entity_id vs timestamp/resource_type/resource_id): time shows 'Invalid Date', object column shows '—', extern: The audit viewer shows 'Invalid Date' as the time and '—' as the object of every entry: the UI reads fields the API does not return
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Route-Audit-Defekttests grün; AuditPage-Defekttest und e2e-real L07/T17 grün (ext); PG-Full-Router-Audit-Test.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P2“ → „P1“. L-T6-epic-b-12 ist WP-133 zugeordnet (Migration 000118:39-53 dieses WPs definiert audit_entry_hash mit ::bytea neu), jetzt bestätigt und P1. WP-Priorität = höchste Abweichung.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „P2 ×2 (eigene), P2 Viewer (ext), P2 unbestätigt L-T6-epic-b-12 (000118 ist Migration dieses WPs). AK-Verweis 'Felder nach AUD-05' teils UNKLAR.“ → „P2 ×2 (eigene, L-17/L-18), P2 Viewer (ext), P1 bestätigt L-T6-epic-b-12 (000118 ist Migration dieses WPs). AK-Verweis 'Felder nach AUD-05' teils UNKLAR.“. Folgt aus der Bestätigung und Höherstufung von L-12.

<a id="wp-134"></a>
#### WP-134 – Metamodell-Export und Audit von Metamodell-Änderungen

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** MET-54, AUD-07
- **Anforderung/Nutzerziel:** Admins exportieren das Metamodell als JSON; jede Metamodell-Änderung ist mit Request-/Correlation-ID auditiert.
- **Implementierung:** backend/internal/citype/handler.go:46,245-260, export.go:30-57, pg_repository.go:860,895; middleware/middleware.go:88-100; server/authz.go:488-519
- **Test(s):** results-backend.tsv pass TestMetamodelExportAndAudit (Re-Run audit_db_16 PASS), TestEveryWriteRouteIsAudited, TestWriteRequestsWriteAuditEntries, TestAuditFieldsAndFiltersPG; Real-Stack L06 pass
- **Soll/Ist:** Erfüllt (11 Audit-Zeilen mit Vorher/Nachher, Kette intakt, Export org-getrennt).
- **Evidenz:** TestMetamodelExportAndAudit-Body; Verifier-Re-Run 0.52s
- **Abweichung:** -
- **Prüfebenen:** UX: N/A; API: OK; Daten: OK – Audit in Mutations-Tx; E2E: LÜCKE – kein Full-Router-PG-Lauf (nicht verbindlich); Robustheit: LÜCKE – globale Feldrouten
- **Abhängigkeiten:** WP-116, WP-133
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** -

<a id="wp-135"></a>
#### WP-135 – Aufbewahrung und Löschläufe (NFR-06)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** NFR-06
- **Anforderung/Nutzerziel:** Daten werden genau so lange wie NFR-06 verlangt aufbewahrt und dann automatisch gelöscht; Audit-Löschung erhält die Kette.
- **Implementierung:** backend/internal/jobs/retention.go:73-167; migrations/000119; config.go:265-268,451-462; events/publisher.go:205-234; deploy/docker-compose/docker-compose.yml (kein logging)
- **Test(s):** results-backend.tsv pass TestRetentionRunsAgainstPostgres, TestChainAfterRetention, Config-Subtests; sh-backup-config 26/26; Welle tests/audit/log-retention.test.sh FAILS (erwartet)
- **Soll/Ist:** DB-Datenarten und kettenerhaltende Audit-Löschung belegt; Log-Retention fehlt, Metrik-Rollups und Outbox-Anker ohne Ablauf.
- **Evidenz:** log-retention.test.sh 'FAIL NFR-06 logs 30 days: … server'; psql timescaledb_information.jobs nur 1001 für metric_sample
- **Abweichung:** Kriterium 1 und 2 nur teilweise (P2 Logs, P3 ×2).
- **Prüfebenen:** UX: N/A; API: N/A; Daten: LÜCKE – Rollups/Outbox; E2E: N/A; Robustheit: LÜCKE – location_change nur Probe
- **Abhängigkeiten:** WP-131, WP-132, WP-084
- **Defekte:** F-150 (P2) NFR-06-Log-Retention (30 Tage) in keiner Auslieferung konfiguriert, F-317 (P3) Continuous Aggregate metric_sample_1h ohne Retention-Policy (400 Tage), F-318 (P3) Outbox-Cleanup behält das neueste Event jeder Entität dauerhaft
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** log-retention.test.sh grün; Migrationstest Retention metric_sample_1h; Repo-Test location_change.

<a id="wp-136"></a>
#### WP-136 – Domain-Events mit Outbox und stabilem Event-Vertrag

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** EVT-01, EVT-02
- **Anforderung/Nutzerziel:** Jede Domänenänderung erzeugt genau ein Event in derselben Transaktion; zuverlässige Zustellung an NATS und Webhooks mit stabilem Vertrag.
- **Implementierung:** backend/migrations/000120_event_outbox; events/outbox.go:33,165-175, publisher.go:65-234, envelope.go:38-72; webhook/dispatcher.go:256-337; server/repositories.go:142
- **Test(s):** results-backend.tsv pass TestOutboxAgainstPostgres, TestEnvelopeMatchesSchema, TestTST01AuditChainAndOutbox, TestDispatcherConsumesOutboxEvents, TestRLSMatrix/event_outbox; Welle TestAuditDefectDomainEventsEmitted, TestAuditDefectWebhookConsumerKeepsEventsOnTransientErrors fails-as-expected
- **Soll/Ist:** Outbox, Nummerierung, Publisher, Schema solide; 4 von 16 EVT-02-Typen ohne Produzent; Webhook-Consumer verliert Events; einige Produzenten außerhalb der Mutations-Tx.
- **Evidenz:** WT audit_t6_router_auditdefect_test.go:115,137,157,179; WT consume_auditdefect_test.go:165,178; SP/work/verify-B17/probes-backend.log
- **Abweichung:** P1 fehlende Produzenten (zwei Defekte), P2 Event-Verlust, P3 ×3.
- **Prüfebenen:** UX: N/A; API: LÜCKE – keine Event-ID für Empfänger; Daten: OK – Nummerierung; E2E: LÜCKE – kein Real-Webhook-Lauf; Robustheit: FEHLER – verlustbehafteter Hand-off
- **Abhängigkeiten:** WP-130, WP-140, Webhook-WPs
- **Defekte:** F-043 (P1) EVT-02-Typen relationship.created, relationship.removed (API), collector.enrolled, review.resolved werden nie erzeugt, F-044 (P1) Entitlement-Warnung/-Ablauf und Zertifikatsablauf: JOB-02-Läufe nicht eingeplant, Events entitlement.warning/expired und certificate.expiring nie erzeugt, F-151 (P2) Webhook-Consumer verwirft Events bei transienten Fehlern, Outbox markiert sie als veröffentlicht, F-319 (P3) Domain-Events nach Commit in separater Transaktion (review.created, export.completed, job.failed, webhook.dead), F-320 (P3) Webhook-Empfänger erhalten keine Event-ID/entity_seq; Redeliveries tragen neue Delivery-ID, F-321 (P3) Stream-Retention nicht konfigurierbar (EVT-01 '30 Tage / 10 GB … konfigurierbar')
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Beide Defekttests grün; Produzententests je EVT-02-Typ; Post-Commit-Fault-Injection-Test.

<a id="wp-137"></a>
#### WP-137 – Benachrichtigungskanäle (E-Mail, In-App, Webhook) und internal/notify

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** NTF-01, TEC-13, REP-01
- **Anforderung/Nutzerziel:** Organisation definiert Kanäle (zentrales/eigenes SMTP, In-App, Webhook) mit verschlüsselter Konfiguration.
- **Implementierung:** backend/internal/notify/channel.go:63-211, smtp.go:41-200 (plain net.Dialer), inapp.go:100-140, handler.go:171-185 (redact ohne url, Platzhalter-PATCH); server/authz.go:80-81,139-140
- **Test(s):** results-backend.tsv pass TestChannelValidation, TestSealer, TestEmailOverSMTP, TestChannelsAgainstPostgres, TestNotificationAPI; Real-Stack N01 pass; Welle TestAuditT6NotificationChannelsAndInbox PASS (Review ok); fails-as-expected TestAuditDefectSMTPSecretNotReleasedToNewHost, TestAuditDefectSMTPChannelCloudMetadataBlocked, TestAuditDefectNotificationAdministrationNeedsNotificationManage
- **Soll/Ist:** Kanalmodell, Provider und Verschlüsselung at rest belegt; Secret-Umleitung, SMTP-Egress-Bypass, unredigierte Webhook-Token für Viewer.
- **Evidenz:** WT channel_secret_auditdefect_test.go:114,136; audit_t6_router_auditdefect_test.go:299,311; SP/work/verify-B17/probes-backend.log ('AUTH received by redirected host')
- **Abweichung:** Kriterium 2 (Secrets) nur teilweise; P2 ×3.
- **Prüfebenen:** UX: LÜCKE – keine Kanal-UI; API: FEHLER – Secret-Umleitung, Egress; Daten: OK – DEK je Zeile; E2E: OK – N01 In-App; Robustheit: LÜCKE – TLS-Prüfung ungetestet
- **Abhängigkeiten:** WP-136, WP-047, WP-139, WP-140
- **Defekte:** F-152 (P2) Gespeichertes SMTP-Passwort wird nach PATCH mit neuem Host an diesen gesendet, F-153 (P2) Eigener SMTP-Kanal umgeht die Egress-Policy (interne Ziele, Cloud-Metadaten), F-154 (P2) Benachrichtigungs-Administration unter webhook:read statt notification:manage; Webhook-URL-Token unredigiert für Viewer/Engineer lesbar
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Drei Defekttests grün; Egress-Test auch für private/Loopback-Ziele im SaaS-Modus.

<a id="wp-138"></a>
#### WP-138 – Empfängerauflösung nach Rolle/Scope (NTF-03)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** NTF-03
- **Anforderung/Nutzerziel:** Benachrichtigungen erreichen genau die Berechtigten: Nutzer nach Rolle/Team/Abo, CI- und Client-Kontakte, nur mit Scope-Berechtigung.
- **Implementierung:** backend/internal/notify/recipients.go:18-212 (kein Client-Kontakt), permission/covers.go, notify/subscriptions.go:35-160
- **Test(s):** results-backend.tsv pass TestRecipientsAgainstPostgres, TestCovers, TestPhaseOneEventsNotify
- **Soll/Ist:** Rollen, Teams, Nutzer, CI-Kontakte, Scope-Filter auf PG belegt; Client-Kontakte, Abos, i18n, Branding fehlen.
- **Evidenz:** recipients.go:18-47; psql contact.client_id vorhanden
- **Abweichung:** Kriterium teilweise nicht implementiert (P2, P3).
- **Prüfebenen:** UX: N/A; API: N/A; Daten: OK – RLS; E2E: LÜCKE – Produktions-Audiences nutzen Rollen/Teams nicht; Robustheit: LÜCKE – Site-Scope ungetestet
- **Abhängigkeiten:** WP-137, WP-046, WP-140
- **Defekte:** F-155 (P2) Client-Kontakte sind keine Empfängerquelle (NTF-03), F-322 (P3) Keine persistierten Abonnements, keine Empfänger-i18n, kein Org-Branding
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Client-Kontakt-Audience mit PG-Test; Site-Scope-Test; Abos/i18n/Branding oder Planverschiebung.

<a id="wp-139"></a>
#### WP-139 – Zustellung mit Retry/Deduplizierung; REST notifications und notification-channels

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** NTF-04, API-05
- **Anforderung/Nutzerziel:** Zuverlässige Zustellung (Retry, Dedup, Status, Ruhezeiten, Opt-out, Digest); Inbox und Kanäle per REST.
- **Implementierung:** backend/internal/notify/notifier.go:157-291, pg_notification.go, handler.go; server/authz.go:80-81,139-140 (webhook:read); permission/role_matrix.go:42,54
- **Test(s):** results-backend.tsv pass TestNotifierDeliversWithRetryAndDedup, TestNotificationsAgainstPostgres, TestNotificationAPI; Real-Stack N01, roles-authz 06 pass; Welle PASS (Review ok): TestAuditT6NotifierRetryBackoffAndQuietHoursOnPostgres, TestAuditT6NotificationChannelsAndInbox; fails-as-expected TestAuditDefectNotificationAdministrationNeedsNotificationManage
- **Soll/Ist:** Retry/Backoff/Dedup jetzt auf PG im Repo belegt, REST-Inbox und Kanal-CRUD über Router+PG; Lesezugriff zu breit, Digest fehlt, kein Default-Kanal.
- **Evidenz:** SP/work/review-T6-epic-b/defect-run.log (TestAuditT6… PASS); WT audit_t6_router_auditdefect_test.go:299; SP/work/verify-B17/probes-backend.log ('no channel: deliveries=0')
- **Abweichung:** Kriterium 2 nur teilweise (Authz P2); NTF-04 Digest und Default-Kanal fehlen (P2).
- **Prüfebenen:** UX: OK/LÜCKE – Center real, keine Kanal-/Präferenz-UI; API: FEHLER – Viewer liest Zustellungen; Daten: OK – Dedup, RLS; E2E: OK – N01; Robustheit: OK – PG-Retry
- **Abhängigkeiten:** WP-137, WP-138, WP-140
- **Defekte:** F-154 (P2) Benachrichtigungs-Administration unter webhook:read statt notification:manage; Webhook-URL-Token unredigiert für Viewer/Engineer lesbar, F-156 (P2) NTF-04-Digest nicht implementiert, F-157 (P2) Neue Organisationen erhalten keine Benachrichtigungen (kein Default-Kanal, keine Kanal-UI)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Authz-Defekttest grün; Digest und Default-Kanal mit Tests.

<a id="wp-140"></a>
#### WP-140 – Die acht Phase-1-Benachrichtigungsereignisse (NTF-05)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** NTF-05, OPS-05
- **Anforderung/Nutzerziel:** Jedes der acht Ereignisse benachrichtigt die richtige Zielgruppe über Outbox → Notifier.
- **Implementierung:** backend/internal/notify/subscriptions.go:35-166; Produzenten router.go:676-681, discovery/collector_status.go:234, export/worker.go:170, jobs/service.go:191, webhook/dispatcher.go:611; keine für invitation.created, entitlement.*, certificate.expiring
- **Test(s):** results-backend.tsv pass TestPhaseOneEventsNotify (synthetisch), TestDiscoveryConcurrency, TestCollectorStatusModel, TestDeadDeliveryIsReported; Real-Stack N01 pass, roles-authz 05 FAIL; Welle TestAuditDefectInvitationCreatedEvent fails-as-expected
- **Soll/Ist:** Subscription-Schicht komplett, 4 Produzenten belegt; 4 Ereignisse werden nie erzeugt.
- **Evidenz:** SP/roles-authz-run5.log Test 05 'Received: null'; SP/work/verify-B17/probes-backend.log TestB17ProbeInvitationEvent; WT invitation_auditdefect_test.go:279,307
- **Abweichung:** 4 von 8 Ereignissen ohne Produzent (P1 ×2), P3 ×2 unbestätigt.
- **Prüfebenen:** UX: OK/LÜCKE – nur mit Kanal; API: N/A; Daten: OK – Dedup; E2E: FEHLER – Einladungsmail; Robustheit: LÜCKE – Merge-Reviews, Client-Scope
- **Abhängigkeiten:** WP-139, WP-136, WP-114, WP-131
- **Defekte:** F-041 (P1) Einladungen versenden keine E-Mail: invitation.created (NTF-05 'Einladung') wird nie erzeugt, F-044 (P1) Entitlement-Warnung/-Ablauf und Zertifikatsablauf: JOB-02-Läufe nicht eingeplant, Events entitlement.warning/expired und certificate.expiring nie erzeugt, F-323 (P3) review.created erreicht client-gescopte Reviewer nicht, F-324 (P3) Review-Items außerhalb reviews.Service (Merge-Konflikte) benachrichtigen nicht
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Produzententests je fehlendem Ereignis grün; roles-authz 05 grün; WP-139-Authz-Fix vor Einladungslink in Zustellungen.

<a id="wp-141"></a>
#### WP-141 – Schlüsselrotation (SEC-06)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** SEC-06
- **Anforderung/Nutzerziel:** Master-Key und Org-DEKs ohne Datenverlust und Downtime rotieren; Schlüsselversion je Ciphertext; alter Schlüssel stillgelegt.
- **Implementierung:** backend/internal/platform/crypto/crypto.go:43-126 (Derive nur aktueller Key :109-113); jobs/rotation.go:73-217; discovery/natsedge.go:176-190,302-316; deploy nats-edge ohne Reload
- **Test(s):** results-backend.tsv pass TestKeyRotationAgainstPostgres, TestMasterKeyRotation, TestSigningKeyRotationOverlap, TestRotatedSecretSignsDuringOverlap; SP/work/verify-B17/probes-backend.log
- **Soll/Ist:** Drei AK erfüllt; SEC-06 'ohne Downtime' verletzt: abgeleitete NATS-Edge-Secrets und Cursor-Key ändern sich.
- **Evidenz:** probes-backend.log TestB17ProbeDerivedSecretsAcrossRotation ('issuer stable: false', 'signature does not verify')
- **Abweichung:** P2 (Requirement-Abschluss SEC-06).
- **Prüfebenen:** UX: N/A; API: OK; Daten: OK – Rewrap; E2E: LÜCKE – Rotation mit laufendem Collector; Robustheit: FEHLER – abgeleitete Secrets
- **Abhängigkeiten:** WP-131, WP-047, COL-08
- **Defekte:** F-158 (P2) Master-Key-Rotation ändert abgeleitete Secrets (NATS-Edge-Issuer/-Passwort, Cursor-Key); nats-edge lädt nicht neu
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Unit-Test stabile abgeleitete Secrets; e2e-real Rotation ohne nats-edge-Neustart; Repo-Tests CA-Rewrap und RotateOrgDEK-Atomizität.

<a id="wp-142"></a>
#### WP-142 – Attribut-DDL-Job: searchable/unique-Flags, CREATE INDEX CONCURRENTLY idx_attr_…, serverseitige Eindeutigkeit

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** DB-04, MET-15
- **Anforderung/Nutzerziel:** Admins markieren Attribute als searchable/unique; Job baut Index nebenläufig; Server weist Duplikate ab, auch parallel.
- **Implementierung:** backend/internal/citype/ddl_job.go:22-263, attribute_definition.go:547,564; ci/service.go:186-209; frontend/src/pages/CITypeAdminPage.tsx:195-198,283-293,349-380
- **Test(s):** results-backend.tsv pass TestAttributeIndexJob, TestUniqueAttributes; Welle CITypeAdminPage.flags.test.tsx 3/3 PASS (Review ok, vitest default 48/252), CITypeAdminPage.auditdefect.tsx fails-as-expected
- **Soll/Ist:** Backend vollständig auf PG belegt; UI ignoriert fehlgeschlagenes Flag-Update still, Flags nur bei Neuanlage.
- **Evidenz:** CITypeAdminPage.auditdefect.tsx 'Unable to find … CIs already share a value'; SP/work/verify-B17/probes-frontend.log
- **Abweichung:** Nutzerprozess Flag-Setzen in der WP-eigenen UI fehlerhaft (P2), P3 Bearbeitung bestehender Flags.
- **Prüfebenen:** UX: FEHLER – stiller 409; API: OK – Service/Repo; Daten: OK – Teil-Unique-Index; E2E: LÜCKE – kein e2e-real; Robustheit: OK – parallele Writes
- **Abhängigkeiten:** WP-116, WP-130, WP-064
- **Defekte:** F-159 (P2) CI-Typ-Admin ignoriert fehlgeschlagenes searchable/unique-Update still, F-325 (P3) Flags bestehender Attribute in der UI nicht änderbar
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Defekttest grün; e2e-real Unique-Flag und Duplikatkonflikt; HTTP-409-Test.

### Epic D – Collector und Discovery

<a id="wp-143"></a>
#### WP-143 – Collector-Statusmodell, Site-Bindung, Offline-Übergang nach 5 min

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** COL-01, COL-05
- **Anforderung/Nutzerziel:** Collectors haben die Status pending/active/offline/disabled/paused nach COL-01. Ein Scheduler setzt sie nach 5 min ohne Heartbeat offline und bei entzogener Lizenz paused, die UI zeigt die Status an.
- **Implementierung:** backend/migrations/000127_collector_status.up.sql:13-51; backend/internal/discovery/collector_status.go:22-40, 92-113, 143-148, 156-245 (Early-Return :170-174); pg_repository.go:113-221 (:190 not found → 500); backend/cmd/server/main.go:486-493; frontend/src/pages/DiscoveryPage.tsx:151-166, 359-367
- **Test(s):** PASS: TestCollectorStatusModel (internal/discovery, PG), TestHandler_Heartbeat, TestEdgeHeartbeat (results-backend.tsv; HEAD-Lauf final/test-db.json PASS). PASS: i18n catalog.test.ts (fünf Status). Real-Stack PASS: discovery-override.spec.ts D01 (Badge 'Aktiv'), D08 (Badge 'Deaktiviert').
- **Soll/Ist:** Soll: COL-01-Statusmenge, offline nach 5 min, paused bei Lizenzentzug, Anzeige in der UI. Ist: alle drei Kriterien durch PG-Test und Real-Stack belegt; im echten Scheduler gingen 3 Collector 5:02–6:00 min nach dem letzten Heartbeat offline (SP/stack/server.log:2179). Es bleiben zwei P3-Robustheitsmängel.
- **Evidenz:** results-backend.tsv pass TestCollectorStatusModel; SP/work/discovery-override/final.log (D01, D08 passed); SP/work/verify-B18/real-stack-evidence.txt; probe-b18-all.log.
- **Abweichung:** P3: Der Statuslauf bricht beim ersten Org-Fehler für alle folgenden Orgs ab (L-T4-31). PATCH auf eine fremde oder unbekannte Collector-ID liefert 500 (L-T4-32). Die Verdrahtung paused↔Entitlement in main.go:489-492 ist nur per Code-Review geprüft.
- **Prüfebenen:** UX OK (Real-Stack-Badges); API OK mit P3 (PATCH 500); Daten OK (Migration, Trigger, RLS); E2E OK (D01/D08, Scheduler offline); Robustheit LÜCKE (Abbruch bei Org-Fehler)
- **Abhängigkeiten:** WP-131, WP-072 (ENT-07), WP-145
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-256 (P3) Der Collector-Statuslauf bricht beim ersten Org-Fehler für alle restlichen Orgs ab, F-257 (P3) PATCH /collectors/{id} antwortet bei unbekanntem oder fremdem Collector mit 500 statt 404
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Multi-Org-Test (Licensed-Fehler in Org 1 → Org 2 geht offline), API-Test PATCH fremde ID → 404, Rendertest für pending/offline/paused.

<a id="wp-144"></a>
#### WP-144 – Enrollment-Token: 24 h, atomarer Einmalverbrauch

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** COL-04
- **Anforderung/Nutzerziel:** Ein Betreiber erzeugt einen Einmalcode mit 24 h Gültigkeit; auch bei parallelem Einsatz entsteht genau ein Collector.
- **Implementierung:** backend/internal/discovery/discovery.go:783-894; pg_repository.go:626-689 (UPDATE … WHERE used_at IS NULL AND expires_at > now() RETURNING)
- **Test(s):** PASS: TestEnrollmentCodeIsConsumedOnce (12 parallele HTTP-Anfragen → genau 1 Collector, 24 h aus der DB), TestEnrollCodeDefaultsAndSingleUse, TestEnrollCollectorFlow (results-backend.tsv, HEAD-Lauf PASS). Real-Stack PASS: D01 (zweites Enroll mit demselben Code scheitert, kein zweiter Collector), e2e-onboarding-real.log Test 6.
- **Soll/Ist:** Soll und Ist stimmen überein: 24 h gültig, atomarer Verbrauch, Paralleltest mit genau einer Registrierung.
- **Evidenz:** results-backend.tsv; SP/work/discovery-override/final.log D01; SP/runs/e2e-onboarding-real.log.
- **Abweichung:** -
- **Prüfebenen:** UX OK; API OK (einheitliche 401); Daten OK (nur Hash gespeichert, Rollback getestet); E2E OK (D01); Robustheit OK (Nebenläufigkeit, Ablauf)
- **Abhängigkeiten:** WP-143, WP-145
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine. Optional: API-Test der TTL-Kappung.

<a id="wp-145"></a>
#### WP-145 – Collector-Zertifikate: CSR, CN-Bindung, Erneuerung, Dual-Trust, Sperre

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** COL-04
- **Anforderung/Nutzerziel:** Ein Collector meldet sich per CSR an und erhält ein 90-Tage-Zertifikat mit CN = Collector-ID. Ab 60 % der Laufzeit erneuert er es automatisch, ein CA-Wechsel läuft mit zwei Trust-Ankern, und disabled sperrt Zertifikat und NATS und trennt die Verbindungen.
- **Implementierung:** backend/internal/discovery/ca.go:99-261 (Rollover nur in Tests aufgerufen), 292-403, 442-475 (RenewCertificate ohne Bindung an den Aufrufer); pg_repository.go:211-218, 708-737; natsedge.go:91-153, 303-333; edge_ingest.go:74-101 (kein Statuscheck); edgecore/enrollment/collector.go:95-150 (Renew ohne Aufrufer); collector/collectorcmd/natsconn.go:73, 83, 101-107
- **Test(s):** PASS: TestCollectorPKI, TestNeedsRenewal, TestCollectorNATSEndpointAuthorization (mit echtem nats-server), TestCollectorEnrollAndRenew. Real-Stack PASS: D01 (Fingerprint, 90 Tage), D08 (Neustart des disabled Collectors wird abgewiesen). Defekttests FAIL wie erwartet: TestAuditDisabledCollectorConnectionCut, TestAuditEdgeIngestRefusesDisabledOrUnknownCollector, TestAuditDisabledCollectorScopeChannelRefused, TestAuditCollectorRenewsCertificateAt60Percent, TestAuditCertificateRenewalBoundToCollector, TestAuditCollectorEnrollsFromEnvironment, TestAuditCARolloverKeepsEnrolledCollectorTrust.
- **Soll/Ist:** Soll: COL-04 vollständig. Ist: Ausstellung, CN-Bindung, CRL und Abweisung NEUER Verbindungen funktionieren. Verletzt sind: Trennung bestehender Verbindungen bei disabled, automatische Erneuerung, Bindung der Erneuerung an den Collector, CA-Roll-over ohne Ausfall und der Env-Bootstrap.
- **Evidenz:** SP/verify-tests-run2/defect-*.log (7 Defekttests FAILS_AS_EXPECTED, z. B. collector_pki_auditdefect_test.go:343 '5 s after disable … CONNECTED … publish delivered'); SP/work/verify-B18/probe-b18-all.log; D08 passed.
- **Abweichung:** Kriterium 2 ist verletzt (disabled trennt nicht, Dual-Trust bricht). Kriterium 1 ist teilweise verletzt (keine Auto-Erneuerung). Sicherheitsdefekt: Erneuerung ohne Identitätsnachweis. Env-Bootstrap fehlt.
- **Prüfebenen:** UX N/A; API FEHLER (Erneuerung für fremden Schlüssel: 201); Daten OK; E2E LÜCKE (nur Neustartpfad D08); Robustheit FEHLER (Verlust der Konnektivität nach 90 Tagen, Roll-over bricht Trust)
- **Abhängigkeiten:** WP-144, WP-146, WP-147, WP-143
- **Defekte:** F-012 (P0) Erneuerungsendpunkt ist nicht an den Collector gebunden: ein Principal mit discovery:ingest stellt sich ein Collector-Zertifikat für den eigenen Schlüssel aus, F-025 (P1) Deaktivierter Collector liefert weiter: offene NATS-Verbindung bleibt bestehen, EdgeIngest, scopes.result und subnets.report ohne Statusprüfung, F-026 (P1) Keine automatische Zertifikatserneuerung; Collector verlieren 90 Tage nach dem Enrollment die Konnektivität, F-101 (P2) Bootstrap über RETICORA_ENROLL_TOKEN / RETICORA_CLOUD_URL nicht implementiert, F-102 (P2) CA-Roll-over ist nicht auslösbar und bricht danach das Vertrauen enrollter Collector in das Edge-Serverzertifikat, F-258 (P3) Der Collector protokolliert 'collector NATS endpoint connected over mTLS', obwohl der Endpunkt ihn abgewiesen hat (disabled/revoked)
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Die 7 Defekttests bestehen nach den Fixes (go test -tags auditdefect). Real-Stack-Variante von D08 ohne Neustart: kein Ingest mehr innerhalb von 10 s. Roll-over-Integrationstest mit echtem nats-server.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Die WP-Priorität ist die höchste Priorität der zugeordneten Defekte. L-T4-05 ist WP-145 zugeordnet und nach Definition P0 (siehe defekt_korrekturen L-T4-05). Der Status FEHLGESCHLAGEN bleibt.

<a id="wp-146"></a>
#### WP-146 – NATS-mTLS-Endpunkt und Firewall-Matrix

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** COL-08, PRI-07
- **Anforderung/Nutzerziel:** Collectors verbinden sich ausgehend per mTLS mit 4222 und bleiben auf ihre eigenen Subjects beschränkt. Kunden erhalten eine korrekte Firewall-Matrix.
- **Implementierung:** backend/internal/discovery/natsedge.go:91-153, 245-295, 303-333 (feste .tmp-Namen, Schlüssel 0644), 362-388; deploy/docker-compose/nats-edge.conf; docker-compose.yml:233, 343; docs/betrieb/firewall-matrix.md
- **Test(s):** PASS: TestCollectorNATSEndpointAuthorization (echter nats-server), tests/nats-mtls.test.sh 3/3, TestHeartbeatOverTheEdge, TestEdgeRefusals. Defekttests FAIL wie erwartet: TestAuditConcurrentEdgePKIExportConsistent (56–58 von 60 Exporten scheitern, Schlüssel 0644), tests/audit/compose-port-policy.test.sh (3 ok, 2 FAIL: 8180, 8080 auf 0.0.0.0).
- **Soll/Ist:** Kriterium 1 erfüllt: mTLS-Endpunkt, Callout und Subject-Trennung, belegt per Real-Stack und TLS-Probe von 192.0.2.2:4222. Kriterium 2 nur teilweise: Die Matrix lässt die Ports 8080/8180/3000 aus und beschreibt Collector-Kanäle, die es nicht gibt. Der PKI-Export bei mehreren Replikas ist nicht race-frei.
- **Evidenz:** SP/work/verify-B18/rerun-disable-natsedge.log; real-stack-evidence.txt; compose-ports.txt; SP/verify-tests-run2/sh-compose-port-policy.test.sh.log; defect-TestAuditConcurrentEdgePKIExportConsistent.log.
- **Abweichung:** COL-08: Compose veröffentlicht API und Keycloak auf allen Interfaces (P2). Race im PKI-Export (P2). Die Matrix ist ungenau (P3). Der echte Server-Teil des CI-Integrationstests wird ohne TEST_NATS_SERVER_BIN still übersprungen.
- **Prüfebenen:** UX N/A; API OK; Daten LÜCKE (Schlüsseldatei 0644); E2E OK mit LÜCKE (install-cloud.sh nicht ausführbar: Netzwerk-Policy); Robustheit FEHLER (Race im Multi-Replica-Export)
- **Abhängigkeiten:** WP-145, WP-147, WP-153, Install-WPs
- **Defekte:** F-103 (P2) Compose veröffentlicht REST-API (8080), Keycloak (8180) und Frontend (3000) auf allen Interfaces, entgegen Firewall-Matrix und COL-08, F-104 (P2) Paralleler Edge-PKI-Export mehrerer Server-Replikas hinterlässt ein nicht passendes oder fehlendes Server-Schlüsselpaar; Schlüsseldatei 0644, F-259 (P3) Die Firewall-Matrix beschreibt Collector-Verkehr, den es nicht gibt (Selbsterneuerung, Heartbeat und Ingest über HTTPS)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** compose-port-policy.test.sh und TestAuditConcurrentEdgePKIExportConsistent bestehen. Matrix korrigiert. Der natsedge-Test läuft in der CI mit echtem nats-server (t.Skip statt t.Log) und einem Cross-Org-Fall.

<a id="wp-147"></a>
#### WP-147 – Verschlüsseltes Credential-Bundle über NATS

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P0 · **Anforderungen:** COL-02
- **Anforderung/Nutzerziel:** Der Server liefert jedem Collector nur die benötigten Credentials, verschlüsselt auf den Collector-Schlüssel und über mTLS-NATS. Der Collector hält den Klartext nur im Speicher.
- **Implementierung:** backend/internal/credential/bundle.go:60-105, 161-171, 225-266; backend/internal/discovery/natsedge.go:432-480 (BundleGate, :446-458 Auswahl); collector/collectorcmd/bundle.go:105-152 (erstes Credential je Protokoll), scopes.go:38, collectorcmd.go:449
- **Test(s):** PASS: TestSealBundle, TestBundleSelectsScopeCredentials, TestCollectorNATSEndpointAuthorization, TestCredentialBundle. Defekttests FAIL wie erwartet: TestAuditBundleCredentialSelection (collector_pki_auditdefect_test.go:416), TestAuditPerScopeCredentials (discovery_auditdefect_test.go:326 'c1-secret' an ein Ziel von Client 2), TestAuditCertificateRenewalBoundToCollector.
- **Soll/Ist:** Kryptografie, Transport, Zertifikatsprüfung und Audit funktionieren. Verletzt ist 'nur Scope-relevante Credentials': ohne aktive Scopes gehen alle Credentials an den Collector, ein Scope ohne Credentials leert das Bundle, und der Collector ignoriert die credential_ids je Scope. Über die ungebundene Erneuerung (WP-145) erhält ein Dritter das Bundle auf seinen eigenen Schlüssel. Dass der Klartext nur im Speicher liegt, ist nur per Code-Review geprüft.
- **Evidenz:** SP/verify-tests-run2/defect-TestAuditBundleCredentialSelection.log, defect-TestAuditPerScopeCredentials.log; SP/work/verify-B18/probe-scopecreds.log; real-stack-evidence.txt (43 Zustellungen, alle mit credential_ids []).
- **Abweichung:** Priorität gegenüber der Verifikation P2 → P0: Nach der Deduplizierung treffen die Sicherheitsdefekte L-T4-05 und L-T4-07 (P0) sowie L-T4-06 (P1) direkt das Kriterium 1.
- **Prüfebenen:** UX N/A; API OK (disabled/revoked wird abgewiesen); Daten FEHLER (Auswahlregel); E2E LÜCKE (nie ein nicht-leeres Bundle end-to-end); Robustheit FEHLER (Geheimnisse gehen an fremde Mandanten)
- **Abhängigkeiten:** WP-145, WP-146, WP-153, WP-047
- **Defekte:** F-012 (P0) Erneuerungsendpunkt ist nicht an den Collector gebunden: ein Principal mit discovery:ingest stellt sich ein Collector-Zertifikat für den eigenen Schlüssel aus, F-013 (P0) Collector ignoriert credential_ids je Scope: das erste Credential je Protokoll gilt für alle Scopes (auch über Client-Grenzen), F-027 (P1) Falsche Credential-Auswahl im Bundle: ohne aktive Scopes alle Client- und Org-Credentials, ein Scope ohne credential_ids leert das Bundle
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditBundleCredentialSelection, TestAuditPerScopeCredentials und TestAuditCertificateRenewalBoundToCollector bestehen. Unit-Test 'Klartext nur im Speicher' (kein Treffer auf Disk oder in Logs). Real-E2E mit einem nicht-leeren Bundle, das für einen Scan verwendet wird.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. WP-147 sind L-T4-05 und L-T4-07 zugeordnet, beide nach Definition P0 (siehe defekt_korrekturen). Beide verletzen Kriterium 1 direkt: Das Bundle wird für den Schlüssel eines Dritten versiegelt bzw. die Geheimnisse eines Clients gehen an Ziele eines anderen Clients. Der Status TEILWEISE BESTANDEN bleibt.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „Priorität gegenüber der Verifikation P2 → P1: Nach der Deduplizierung treffen die P1-Defekte L-T4-05, L-T4-06 und L-T4-07 direkt das Kriterium 1.“ → „Priorität gegenüber der Verifikation P2 → P0: Nach der Deduplizierung treffen die Sicherheitsdefekte L-T4-05 und L-T4-07 (P0) sowie L-T4-06 (P1) direkt das Kriterium 1.“. Der Text muss zur korrigierten Priorität von L-T4-05 und L-T4-07 passen.

<a id="wp-148"></a>
#### WP-148 – Signierte Collector-Auto-Updates mit Kanal und Rollback

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** COL-06
- **Anforderung/Nutzerziel:** Collectors aktualisieren sich nur mit signierten Artefakten ihres Kanals und rollen nach einem Fehlstart zurück.
- **Implementierung:** edgecore/update/update.go:56-296 (Signatur vor Ausführung, Identität gepinnt :250-251); channel.go:41-52; collector/collectorcmd/update.go:25-32, collectorcmd.go:162-231; install-vm.sh:597-614 (ohne cosign)
- **Test(s):** PASS (results-edgecore.tsv/results-collector.tsv, HEAD-Lauf exit 0): TestApplyRefusesUnsignedAndWronglySignedReleases, TestCosignVerifierPinsTheReleaseIdentity, TestChannelAccepts, TestCheckHonoursChannelVersionAndRollback, TestFailedStartsRollBackTheUpdate, TestUpdateLoopWaitsForAWorkingStartAndRestartsIntoTheUpdate, TestStartupUpdateConfirmsAndRollsBack. Die Testwelle hat nichts kodifiziert (End-to-End-Selbstupdate zu aufwendig).
- **Soll/Ist:** Alle Bausteine sind einzeln belegt. Die Zusammensetzung im laufenden Collector (Main, newUpdateChecker) und eine echte cosign-Prüfung sind nicht ausgeführt.
- **Evidenz:** results-edgecore.tsv, results-collector.tsv; Code-Review collectorcmd.go:162-231.
- **Abweichung:** Verdrahtung und echte Signaturprüfung sind nicht ausgeführt belegt. cosign wird vom VM-Installer nicht bereitgestellt (P3).
- **Prüfebenen:** UX N/A; API OK (fail-closed); Daten OK (atomarer Tausch); E2E LÜCKE (kein echtes Selbstupdate); Robustheit LÜCKE (cosign fehlt auf der VM)
- **Abhängigkeiten:** WP-102, WP-145, install-vm.sh
- **Defekte:** F-260 (P3) Auto-Update braucht cosign auf dem Host, den der VM-Installer nicht bereitstellt
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Shell-Test des Selbstupdates mit zwei gebauten Versionen: positiv, falsche Signatur, falscher Kanal, Rollback. Unit-Test für newUpdateChecker. Echte cosign-Prüfung in der CI.

<a id="wp-149"></a>
#### WP-149 – Sweep: ICMP/ARP, Parallelität ≤ 256, Default-Ports

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** DIS-01
- **Anforderung/Nutzerziel:** Der Sweep findet Hosts per ICMP, ARP und TCP auf genau 22/80/443/623/5985/8006, mit höchstens 256 parallelen Probes und dokumentierter Capability.
- **Implementierung:** collector/plugins/sweep/sweep.go:19-28, 58-183 (Semaphore im Goroutine-Körper :92-145); icmp.go:16-108; arp.go:21-56; collector/Dockerfile:20-27; install-vm.sh:611
- **Test(s):** PASS: TestNewDefaults, TestDiscoverCapsParallelProbesAt256, TestDiscoverFindsHostsByICMPAndARP, TestDiscoverWithoutICMPCapabilityFallsBackToTCP, TestParseARPTable, TestICMPPingerUsesTheAvailableSocket, TestSweepConcurrencyIsCappedAt256, TestCollectorOutputConformsToDeviceRecordSchema. Verifier: Sweep auf einem realen Segment (11/20 Hosts per Raw-ICMP mit MAC aus /proc/net/arp).
- **Soll/Ist:** Alle vier Teilkriterien sind durch ausgeführte Tests und einen Lauf auf einem realen Segment belegt.
- **Evidenz:** results-collector.tsv; SP/work/verify-B18/probe-sweep-real.log, probe-sweep-goroutines.log.
- **Abweichung:** P3, außerhalb der Kriterien: veralteter Capability-Kommentar in icmp.go; eine Goroutine je Ziel vor der Semaphore (Spitze 63.356).
- **Prüfebenen:** UX N/A; API N/A; Daten OK; E2E OK mit LÜCKE (kein ARP-only-Host im realen Segment); Robustheit OK mit P3 (Goroutinen)
- **Abhängigkeiten:** WP-083, WP-150, WP-153
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-261 (P3) Widersprüchliche Capability-Doku (icmp.go vs. Dockerfile), F-262 (P3) Der Sweep startet je Ziel eine Goroutine, bevor die Semaphore mit 256 Plätzen belegt wird
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Container-Test mit unprivilegiertem ICMP, netns-ARP-only-Test, Goroutine-Bound-Test.

<a id="wp-150"></a>
#### WP-150 – Plugin-Interface Name/Probe/Collect mit DeviceRecord; Collect-Pfade aufrufen

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** DIS-03, DIS-04
- **Anforderung/Nutzerziel:** Alle acht Protokoll-Plugins erfüllen DIS-03, und die Hauptschleife ruft für jeden Probe-Treffer Collect auf.
- **Implementierung:** collector/plugins/plugin.go:45-69; Interface-Asserts in den 8 Plugins; collector/collectorcmd/collectorcmd.go:126-135, 419-597
- **Test(s):** PASS: TestDiscoveryProbesThenCollects, TestEveryPluginImplementsProbeCollect, TestPollsTargetTheSweptHosts, TestCollectorOutputConformsToDeviceRecordSchema und die Collect-Tests je Plugin (results-collector.tsv; HEAD-Lauf exit 0).
- **Soll/Ist:** Erfüllt: Vertrag zur Compile-Zeit erzwungen; die Schleife ruft Probe → Collect auf; der DeviceRecord ist schemakonform.
- **Evidenz:** results-collector.tsv; Testkörper collectorcmd_test.go:627-688.
- **Abweichung:** -
- **Prüfebenen:** UX N/A; API OK; Daten OK (Schema); E2E OK (eingeschränkt, Real-Stack nur mit simulate); Robustheit OK (Fallback + runError; WMI-Spezifika siehe WP-152)
- **Abhängigkeiten:** WP-149; Protokolltiefe in WP-152
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine. Optional: Zyklustest mit echten snmp-/redfish-Plugins gegen Fakes.

<a id="wp-151"></a>
#### WP-151 – Klassifikation SNMP → Redfish → Banner, YAML-Profile, generic_device-Fallback

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** DIS-02, CH30
- **Anforderung/Nutzerziel:** Geräte werden über die Kette SNMP → Redfish → Banner mit YAML-Profilen klassifiziert. Nur Geräte ohne Profiltreffer werden generic_device mit einem unclassified_device-Review.
- **Implementierung:** collector/profiles/classify.go:137-283; collector/profiles/data/*.yaml (20 Dateien, 36 Profile); collector/plugins/sweep/sweep.go:249-262 (Record ohne Evidenz); backend/internal/discovery/discovery.go:1238-1256 (Review vor der Identitätsauflösung, Open statt OpenUnlessDismissed); review.go:103-105
- **Test(s):** PASS: TestProfileListCoversCH30, TestClassificationChain, TestUnmatchedDeviceFallsBackToGenericDevice, TestEmbeddedProfileData, TestTST01Reconciliation/21_…. Defekttests FAIL wie erwartet: TestAuditUnclassifiedReviewOnlyForUnclassifiedDevices (ingest_auditdefect_test.go:162/:178), TestAuditClassifiedDeviceNotUploadedAsFallback (discovery_auditdefect_test.go:502).
- **Soll/Ist:** Profile, Kette und CH30-Abdeckung sind erfüllt. Verletzt ist der Fallback 'nur bei fehlendem Profiltreffer': Jeder gesweepte Host erhält ein unclassified_device-Review, auch wenn SNMP ihn klassifiziert hat, und das Review öffnet sich nach dem Verwerfen erneut.
- **Evidenz:** SP/verify-tests-run2/defect-TestAuditUnclassifiedReviewOnlyForUnclassifiedDevices.log, defect-TestAuditClassifiedDeviceNotUploadedAsFallback.log; SP/work/verify-B19 classify.json-Probe.
- **Abweichung:** Kriterium 'Fallback generic_device plus Review' ist verletzt (P1). system_template ist ohne Konsumenten (P3).
- **Prüfebenen:** UX FEHLER (falsche Reviews im Eingang); API LÜCKE (Review ohne Prüfung des CI-Typs); Daten OK (CI-Typ korrekt); E2E LÜCKE (Real-Stack nur simulate); Robustheit FEHLER (Reviews kehren täglich wieder)
- **Abhängigkeiten:** WP-150, WP-149, Reviews-Framework
- **Defekte:** F-030 (P1) Jedes gesweepte Gerät erhält ein (wiederkehrendes) falsches unclassified_device-Review, auch wenn SNMP, SSH oder Redfish es klassifiziert haben, F-263 (P3) Das Profilfeld systemTemplate ist ein totes Label; das Backend wertet es nie aus
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Beide Defekttests bestehen. Die vorhandenen Profil-/Kettentests bleiben grün. Auto-Resolve nach späterer SNMP-Klassifikation getestet.

<a id="wp-152"></a>
#### WP-152 – Protokolltiefe Phase 1: SNMPv3, SSH-Befehle, Redfish/WMI/IPMI vervollständigen

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** DIS-04
- **Anforderung/Nutzerziel:** Discovery fragt Geräte per SNMPv3 authPriv, SSH-Allowlist sowie Redfish/WMI/IPMI ab und verwendet die konfigurierten Credentials, ohne sie preiszugeben.
- **Implementierung:** collector/plugins/snmp/v3.go; ssh/exec.go:54-215; redfish/redfish.go:153-200, 311; wmi/wmi.go:42 (UseHTTPS=false), :47 (InsecureSkipVerify), :155-162, :204-255 (Fehler werden verschluckt); nas/nas.go:47-60, 214-217, 257; collector/plugins/plugin.go:74-78 (Aliasse wmi/nas → ssh); collector/collectorcmd/bundle.go:105-108
- **Test(s):** PASS: TestSNMPv3AuthPrivRoundTrip, TestSNMPv3RequiresAuthPriv, TestCollectRunsAllowlistedCommands, TestChangedHostKeyIsRefused, die Redfish-TLS-Tests, TestCollectParsesWSManResponses (Fixture), TestCollectReadsSystemGUID. Defekttests FAIL wie erwartet: TestAuditWMICredentialTransport, TestAuditNASCredentialTransport, TestAuditCredentialKindRouting, TestAuditWMIFailuresAreErrors, TestAuditWMIAuthFailureReachesScopeRun, TestAuditSNMPv3AuthFailureReported.
- **Soll/Ist:** SNMPv3, SSH und Redfish sind belegt, IPMI als Best Effort. WMI ist verletzt: Das Plugin nutzt das SSH-Geheimnis statt des wmi-Credentials, sendet es im Klartext (Basic über http:5985) und meldet bei 401 Erfolg mit leeren Feldern. NAS sendet das Passwort in der URL über http:5000 bzw. an ungeprüftes TLS.
- **Evidenz:** SP/verify-tests-run2/defect-TestAuditWMICredentialTransport.log (wmi_auditdefect_test.go:131), defect-TestAuditNASCredentialTransport.log (nas_auditdefect_test.go:103), defect-TestAuditCredentialKindRouting.log, defect-TestAuditWMIFailuresAreErrors.log; SP/work/verify-B19-Probes.
- **Abweichung:** Kriterium 'Redfish/WMI/IPMI liefern DeviceRecord-Felder' ist für WMI verletzt. Zwei P0-Sicherheitsdefekte (Klartext-Credentials bei WMI und NAS). 'Wie v2' bleibt bis E-02 außen vor.
- **Prüfebenen:** UX N/A; API FEHLER (Credential-Namespaces inkonsistent); Daten FEHLER (WMI-Record 'erfolgreich' ohne Daten); E2E LÜCKE (keine realen Geräte); Robustheit FEHLER (Klartext, verschluckte Fehler)
- **Abhängigkeiten:** WP-048, WP-147, WP-155, E-02
- **Defekte:** F-010 (P0) WMI-Plugin sendet Credentials im Klartext (HTTP Basic über http://ziel:5985; HTTPS-Modus mit InsecureSkipVerify), F-011 (P0) NAS-Plugin sendet Synology-Konto und Passwort in der URL über http://ziel:5000 und an ungeprüftes HTTPS (5001), F-028 (P1) Credentials der Art 'wmi' erreichen das WMI-Plugin nie; SSH-Credentials gehen an WMI- und NAS-Ziele, F-029 (P1) WMI Collect meldet Erfolg mit leeren Feldern bei 401, Transportfehler oder protokollkonformer WinRM-Antwort; der Scope-Lauf zeigt ok, F-105 (P2) SNMPv3-Credential-Fehler unsichtbar: usmStats-Reports werden als 'kein Gerät' behandelt
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Die 6 Defekttests bestehen nach den Fixes. Die Fixture-Tests bleiben grün. Ein Vertragstest von jeder Credential-Art zum Plugin besteht.

<a id="wp-153"></a>
#### WP-153 – Discovery-Scopes: Modell, CRUD, scope_suggested nach Enrollment

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** DIS-09, API-05
- **Anforderung/Nutzerziel:** Admins definieren Scopes per REST/UI unter discovery:manage. Collectors erhalten sie über NATS und scannen jeden Scope mit seinen Einstellungen. Lokale Subnetze erscheinen nach dem Enrollment als scope_suggested.
- **Implementierung:** backend/migrations/000129_discovery_scope.up.sql; backend/internal/discovery/scope.go:39, 185-280 (Zonen akzeptiert :269-280), 418-431 (nur Org geprüft); scope_handler.go:193-458; natsedge.go:446-458; reviews/handler.go:89-110; collector/collectorcmd/scopes.go:182, 203-230; collectorcmd.go:449, 502-521 (Kappung bei 4096); frontend/src/pages/DiscoveryPage.tsx:209-357
- **Test(s):** PASS: TestDiscoveryScopes (PG), TestScopeREST, die authz-Subtests, TestDiscoveryFollowsTheServerScopes. Neuer Regressionstest PASS: TestAuditScopeChannelOverNATS (scope_channel_nats_audit_test.go, 6 Subtests; mechanische Prüfung und HEAD-Lauf PASS). Defekttests FAIL wie erwartet: TestAuditBundleCredentialSelection, TestAuditPerScopeCredentials, TestAuditScopeSuggestedAuthzAndClientIsolation, TestAuditScopeExpansionCompleteOrReported, TestAuditZonedScopeTargetRefused, TestAuditDisabledCollectorScopeChannelRefused.
- **Soll/Ist:** Modell, RLS, REST, NATS-Zustellung und Vorschläge funktionieren. Die Credentials je Scope funktionieren nicht: Der Collector ignoriert sie, und ein Scope ohne Credentials leert das Bundle. Das Annehmen eines Vorschlags umgeht discovery:manage und wirkt über Client-Grenzen. Große Scopes werden still gekappt.
- **Evidenz:** SP/verify-tests-run2/defect-*.log der 6 Tests; SP/work/verify-B19 pg/nats/collector-Probes; final/test-db.json PASS TestAuditScopeChannelOverNATS.
- **Abweichung:** Kriterien 'REST mit discovery:manage' und 'Collector erhält und scannt Scopes' sind teilweise verletzt (P1/P2). Dem UI-Formular fehlen Credentials/VRF/Site/Plugins (P2). Weitere P3-Mängel.
- **Prüfebenen:** UX LÜCKE (Formular minimal, keine Fehleranzeige); API FEHLER (scope_suggested ohne discovery:manage); Daten FEHLER (Bundle leer, Kappung); E2E LÜCKE (Real-Stack-Collector im simulate-Modus); Robustheit FEHLER (mandantenübergreifende Credentials/Vorschläge)
- **Abhängigkeiten:** WP-146, WP-147, WP-082, WP-156, WP-154
- **Defekte:** F-013 (P0) Collector ignoriert credential_ids je Scope: das erste Credential je Protokoll gilt für alle Scopes (auch über Client-Grenzen), F-025 (P1) Deaktivierter Collector liefert weiter: offene NATS-Verbindung bleibt bestehen, EdgeIngest, scopes.result und subnets.report ohne Statusprüfung, F-027 (P1) Falsche Credential-Auswahl im Bundle: ohne aktive Scopes alle Client- und Org-Credentials, ein Scope ohne credential_ids leert das Bundle, F-031 (P1) scope_suggested umgeht discovery:manage und ist über Client-Grenzen sichtbar und verwerfbar, F-106 (P2) Scopes mit mehr als ~4096 Adressen werden vom Collector still gekappt, F-107 (P2) Scope-Formular kann Credentials, VRF, Site, Plugins und Namen nicht setzen, F-108 (P2) Zoniertes IPv6-Scope-Ziel wird akzeptiert; Offline-Detektor und Kanten-Cleanup brechen für alle danach sortierten Orgs ab, F-264 (P3) Scope-Credentials werden nur auf die Org geprüft: ein Credential von Client 2 lässt sich an einen Scope von Client 1 binden, F-265 (P3) Docker-/Bridge-Netze des Collector-Hosts werden als Scopes vorgeschlagen, F-267 (P3) Discovery-Metriken undokumentiert; Scope-Aktionen ohne Fehlerrückmeldung, Löschen ohne Bestätigung
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Die 6 Defekttests bestehen. Ein e2e-real-Test für Scope-CRUD und das Annehmen eines Vorschlags mit nicht-simuliertem Collector besteht. Ein UI-Test der neuen Formularfelder besteht.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. L-T4-07 ist WP-153 zugeordnet und nach Definition P0, denn Credentials gehen über Client-Grenzen (siehe defekt_korrekturen L-T4-07). Der Status FEHLGESCHLAGEN bleibt.

<a id="wp-154"></a>
#### WP-154 – Getrennte Intervalle: Sweep 24 h, Polls je Protokoll, Mindestintervalle; Spool-Flush

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** DIS-10, NFR-03
- **Anforderung/Nutzerziel:** Sweeps laufen nach dem Scope-Zeitplan, Polls im eigenen Intervall (mindestens 5 min), und der Spool wird kontinuierlich geleert.
- **Implementierung:** collector/collectorcmd/schedule.go:21-230 (ungesehene Schlüssel sofort fällig :95-104); scopes.go:203-262; collectorcmd.go:385-414 (neuer Scheduler je Start :396); backend/internal/discovery/scope.go:55-98, 289-307; scope_handler.go:289 (kein next_run_at)
- **Test(s):** PASS: TestIntervalsHaveDefaultsAndMinimums, TestSchedulerRunsSweepAndPollsSeparately, TestCronSchedule, TestSpoolFlushDeliversTenThousandRecords, TestDiscoveryIntervalsFromSettings. Regressionstest PASS: TestAuditScopeChannelOverNATS (Intervalle über NATS geklemmt). Defekttest FAIL wie erwartet: TestAuditScopeScheduleSurvivesRestart (discovery_auditdefect_test.go:405). Lastlauf: Spool 415 rec/s, Flush 28,5 s, 0 fehlend.
- **Soll/Ist:** Mindestwerte und kontinuierlicher Flush sind belegt. Intervalle je Scope sind teilweise verletzt: Cron- und Intervall-Scopes laufen bei jedem Start sofort.
- **Evidenz:** results-collector.tsv; SP/verify-tests-run2/defect-TestAuditScopeScheduleSurvivesRestart.log; audit/phase1-final/evidence/load/k6-ingest-spool-run1.txt.
- **Abweichung:** Scope-Zeitpläne werden nach Start und Neustart ignoriert (P2). Polls nur je Org, Cron in UTC (P3).
- **Prüfebenen:** UX N/A; API OK (Intervalle über NATS); Daten LÜCKE (Scheduler nur im Speicher, kein next_run_at); E2E OK für den Spool (Lastlauf); Robustheit FEHLER (Scan beim Neustart)
- **Abhängigkeiten:** WP-153, edgecore buffer, WP-146
- **Defekte:** F-109 (P2) Scope-Zeitpläne (Cron/Intervall) werden beim Collector-Start ignoriert: jeder Scope läuft sofort und nach jedem Neustart, F-266 (P3) Poll-Intervalle nur je Org und Protokoll einstellbar, nicht je Scope; Cron wird in UTC ausgewertet
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditScopeScheduleSurvivesRestart besteht. Entscheidung zu Poll-Intervallen je Scope und zur Cron-Zeitzone.

<a id="wp-155"></a>
#### WP-155 – Discovery-Fehlerzustände, Metriken, Job-/Scopefehler in UI

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** DIS-08
- **Anforderung/Nutzerziel:** Bei Discovery-Fehlern sehen Nutzer je Ziel, Scope und Job Ursache, Protokoll und Ziel in der UI. Betreiber erhalten Metriken und Logs.
- **Implementierung:** backend/internal/discovery/errors.go:14-204; scope_handler.go:369-391; backend/internal/server/router.go:603; collector/collectorcmd/scopes.go:285-331; frontend/src/pages/DiscoveryPage.tsx:168-203; stille Pfade wmi.go:204-230 und der SNMPv3-Probe
- **Test(s):** PASS: TestClassifyCause, TestRunResultStatesAndMetrics, TestScopeHealthAfterRun, TestDiscoveryProbesThenCollects, Vitest DiscoveryPage DIS-08 (Mock). Regressionstest PASS: TestAuditScopeChannelOverNATS (scopes.result → Health in PG). Defekttests FAIL wie erwartet: TestAuditWMIAuthFailureReachesScopeRun, TestAuditWMIFailuresAreErrors, TestAuditSNMPv3AuthFailureReported.
- **Soll/Ist:** Fehlermodell, Persistenz, NATS-Meldung und Metriken sind belegt. Die UI ist nur gegen Mocks belegt. Credential-Fehler bei WMI und SNMPv3 bleiben unsichtbar, die Scopes zeigen ok.
- **Evidenz:** results-backend.tsv; SP/verify-tests-run2/defect-TestAuditWMIAuthFailureReachesScopeRun.log ('records=1 error_count=0'), defect-TestAuditSNMPv3AuthFailureReported.log; final/test-db.json PASS TestAuditScopeChannelOverNATS.
- **Abweichung:** Das Kriterium 'Fehlerzustand mit Ursache' ist für WMI und SNMPv3 verletzt, die UI ohne e2e-real-Beleg. Priorität gegenüber der Verifikation P2 → P1: Der WMI-Teil hat dieselbe Ursache wie der P1-Defekt L-T4-09 (wsmanEnumerate verschluckt Fehler).
- **Prüfebenen:** UX LÜCKE (nur Mock, keine Fehleranzeige für Aktionen); API OK; Daten OK (Health persistiert); E2E LÜCKE (kein realer Fehlerlauf); Robustheit FEHLER (Auth-Fehler unsichtbar)
- **Abhängigkeiten:** WP-153, WP-154, WP-152
- **Defekte:** F-029 (P1) WMI Collect meldet Erfolg mit leeren Feldern bei 401, Transportfehler oder protokollkonformer WinRM-Antwort; der Scope-Lauf zeigt ok, F-105 (P2) SNMPv3-Credential-Fehler unsichtbar: usmStats-Reports werden als 'kein Gerät' behandelt, F-258 (P3) Der Collector protokolliert 'collector NATS endpoint connected over mTLS', obwohl der Endpunkt ihn abgewiesen hat (disabled/revoked), F-267 (P3) Discovery-Metriken undokumentiert; Scope-Aktionen ohne Fehlerrückmeldung, Löschen ohne Bestätigung
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Die drei Defekttests bestehen. Ein e2e-real-Test zeigt Badge und Ursache aus einem real gemeldeten Lauf. Ein Test der /metrics-Exposition besteht. Metriken dokumentiert.

<a id="wp-156"></a>
#### WP-156 – Collector-CLI `enroll --server --code`; Scope-Vorschlag bestätigen

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** INS-06
- **Anforderung/Nutzerziel:** Der Betreiber führt den in der UI angezeigten Befehl aus, die Identität landet im einheitlichen Pfad, und er bestätigt die Scope-Vorschläge in der UI.
- **Implementierung:** collector/collectorcmd/cli.go:27-139; edgecore/enrollment/enrollment.go:18 (/var/lib/…); install-vm.sh:456 (/opt/…/credentials.json), :607 (Binary 'collector'); collector/Dockerfile:19,31; frontend/src/pages/DiscoveryPage.tsx:49-51, 209-252; OnboardingPage.tsx:250-300
- **Test(s):** PASS: TestEnrollCommand, TestCollectorEnrollAndRenew, Vitest 'confirms a suggested scope' (Mock). Real-Stack PASS: D01 (UI-Code → echtes Enroll exit 0), e2e-onboarding-real.log Test 6 (explizites --credentials). Defekttest FAIL wie erwartet: tests/audit/collector-install-cli.test.sh (3 ok, 3 FAIL).
- **Soll/Ist:** Das CLI-Enroll funktioniert. Der 'einheitliche Konfigurationspfad' ist auf installierten Collectors verletzt: Binary-Name und Pfad passen nicht zum UI-Befehl. Die reale Bestätigung eines Vorschlags ist nicht geprüft und leert dabei das Credential-Bundle.
- **Evidenz:** SP/verify-tests-run2/sh-collector-install-cli.test.sh.log; SP/work/discovery-override/final.log D01; results-collector.tsv.
- **Abweichung:** Kriterium 1 ist teilweise verletzt (P2), Kriterium 2 nur per Mock und mit dem Bundle-Nebeneffekt. Priorität gegenüber der Verifikation P2 → P1: Der zugeordnete P1-Defekt L-T4-06 (Bestätigung erzeugt einen Scope ohne Credentials und leert das Bundle) gehört zu den verifizierten Abweichungen dieses WPs.
- **Prüfebenen:** UX LÜCKE (Befehl passt nicht zur Installation); API LÜCKE (discovery:manage umgangen, L-T4-16); Daten FEHLER (Bundle wird geleert); E2E LÜCKE (Bestätigung nicht geprüft); Robustheit OK (Exit-Codes)
- **Abhängigkeiten:** WP-153, WP-200, install-vm.sh, Dockerfile
- **Defekte:** F-027 (P1) Falsche Credential-Auswahl im Bundle: ohne aktive Scopes alle Client- und Org-Credentials, ein Scope ohne credential_ids leert das Bundle, F-031 (P1) scope_suggested umgeht discovery:manage und ist über Client-Grenzen sichtbar und verwerfbar, F-110 (P2) Installierter Collector passt nicht zum Enroll-Befehl der UI (Binary-Name und Credentials-Pfad abweichend), F-265 (P3) Docker-/Bridge-Netze des Collector-Hosts werden als Scopes vorgeschlagen
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** collector-install-cli.test.sh besteht. Ein e2e-real-Test bestätigt einen Vorschlag, prüft den Scope und das Bundle mit Credentials. TestAuditBundleCredentialSelection besteht.

<a id="wp-157"></a>
#### WP-157 – CI-Offline-Detektor: last_seen_at > 3 x Scopeintervall -> unknown, ci.offline

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** REC-09, LCY-05
- **Anforderung/Nutzerziel:** Beobachtbare, entdeckte CIs werden nach mehr als 3× Scope-Intervall ohne Sichtung unknown und lösen ci.offline aus. Der Lauf ist schaltbar und gilt für jeden Mandanten.
- **Implementierung:** backend/internal/lifecycle/offline.go:55-60, 68-91 (Early-Return :82-86), 101-198 (ungeschützte inet-Casts :144-147); backend/internal/config/config.go:178-180, 284; backend/cmd/server/main.go:494-499; backend/internal/discovery/scope.go:269-280
- **Test(s):** PASS: TestOfflineDetector (PG). Regressionstest PASS: TestAuditOfflineDetectorSwitchAndSchedules (offline_switch_audit_test.go; Schalter, '@every 5m'). Defekttests FAIL wie erwartet: TestAuditZonedScopeTargetRefused (zone_isolation_auditdefect_test.go:75 POST → 201), TestAuditBadScopeIsolated (:133 die CI von Org B bleibt active).
- **Soll/Ist:** Die Logik je Org ist korrekt, inklusive Grenzen und Ausnahmen, und der Job läuft im Real-Stack. Ein per API angelegter zonierter IPv6-Scope stoppt den Lauf aber für alle Orgs, die danach sortiert sind.
- **Evidenz:** SP/verify-tests-run2/defect-TestAuditBadScopeIsolated.log, defect-TestAuditZonedScopeTargetRefused.log; SP/work/verify-B20/out/zone.txt, realstack.txt; final/test-db.json PASS TestAuditOfflineDetectorSwitchAndSchedules.
- **Abweichung:** Kriterium 1 ist mandantenübergreifend verletzt (P2). ci.online wird nie gesendet (P3). Ob main.go den Job bei ausgeschaltetem Schalter tatsächlich nicht registriert, ist nicht testbar ohne Produktionsänderung.
- **Prüfebenen:** UX N/A (ci.offline in der UI nicht abonnierbar); API FEHLER (Zonen-Target akzeptiert); Daten OK; E2E OK (teilweise: Scheduler läuft, kein unknown beobachtet); Robustheit FEHLER (keine Fehlerisolation je Org)
- **Abhängigkeiten:** WP-153, WP-136, WP-120, WP-115, WP-159
- **Defekte:** F-108 (P2) Zoniertes IPv6-Scope-Ziel wird akzeptiert; Offline-Detektor und Kanten-Cleanup brechen für alle danach sortierten Orgs ab, F-268 (P3) ci.online ist dokumentiert, wird bei der Rückkehr eines CIs aber praktisch nie gesendet
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditZonedScopeTargetRefused und TestAuditBadScopeIsolated bestehen. Ein buildSystemJobs-Test für Schalter ein und aus. Entscheidung und Test zu ci.online.

<a id="wp-158"></a>
#### WP-158 – Topologie: Confidence LLDP 1,0 / FDB 0,6, Trunkfilter, Confidence-Spalte

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** TOP-01
- **Anforderung/Nutzerziel:** Entdeckte Nachbarn werden zu connected_to-Kanten mit der Confidence nach TOP-01. Unbekannte Schlüssel werden zum Review.
- **Implementierung:** backend/internal/discovery/topology.go:20-29, 131-206 (First-Wins-Dedup :190-194), 313-396; collector/plugins/snmp (keine LLDP-/BRIDGE-MIB-Abfrage)
- **Test(s):** PASS: TestTopologyConfidence (PG), TestDeriveTopologyRules. Regressionstest PASS: TestAuditDiscoveryResightingThroughIngest. Defekttest FAIL wie erwartet: TestAuditSNMPCollectsNeighbors (neighbors_auditdefect_test.go:101/:104). Real-Stack: 257 LLDP-Kanten mit 1.0 (simulate).
- **Soll/Ist:** Die Regeln sind für kanonische Eingaben erfüllt. Ist dieselbe MAC zuerst per FDB gemeldet, wird die Confidence 0,6 und kippt bei erneutem Ingest. Der ausgelieferte Collector liefert gar keine LLDP-/FDB-Nachbarn.
- **Evidenz:** SP/work/verify-B20/out/probe-summary.txt; SP/verify-tests-run2/defect-TestAuditSNMPCollectsNeighbors.log; realstack.txt.
- **Abweichung:** Die Testwelle ordnet diesen Defekt (L-T4-22, P1: wesentliche Funktion fehlt im Produktiv-Collector) WP-158 zu, daher Priorität gegenüber der Verifikation P3 → P1.
- **Prüfebenen:** UX LÜCKE (Confidence nicht angezeigt); API OK; Daten OK mit P3 (Kippen); E2E OK mit LÜCKE (nur simulate liefert Nachbarn); Robustheit FEHLER P3 (Reihenfolge)
- **Abhängigkeiten:** WP-128, WP-085, WP-088, Collector-Protokoll (DIS-04/05)
- **Defekte:** F-032 (P1) Ausgelieferter Collector liefert keine LLDP-/CDP-/FDB-Nachbarn (TOP-01-Eingang fehlt in Produktion), F-269 (P3) LLDP-Nachbar wird mit FDB-Confidence 0,6 gespeichert, wenn ein FDB-Eintrag vorher gemeldet wird; der Wert kippt zwischen Ingests
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Test der Reihenfolgeunabhängigkeit (Unit und PG-Re-Ingest) besteht. TestAuditSNMPCollectsNeighbors besteht. Integrationstest des unknown_relationship-Review-Lebenszyklus.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P2“ → „P1“. L-T4-22 ist WP-158 zugeordnet und nach Definition P1, weil eine wesentliche Funktion fehlt (siehe defekt_korrekturen L-T4-22). Der Status TEILWEISE BESTANDEN bleibt, denn L-T4-22 verletzt kein WP-158-Kriterium. Diese Kriterien betreffen die Ableitung der Confidence aus gemeldeten Nachbarn.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „Die Testwelle ordnet diesen Defekt WP-158 zu, daher Priorität gegenüber der Verifikation P3 → P2.“ → „Die Testwelle ordnet diesen Defekt (L-T4-22, P1: wesentliche Funktion fehlt im Produktiv-Collector) WP-158 zu, daher Priorität gegenüber der Verifikation P3 → P1.“. Der Text muss zur korrigierten Priorität von L-T4-22 passen.

<a id="wp-159"></a>
#### WP-159 – Wiederbeobachtung frischt Kanten auf; Cleanup nach max(7 d, 5 x Intervall); Quellen-Policy

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** TOP-02, REL-08
- **Anforderung/Nutzerziel:** Erneute Sichtung frischt Discovery-Kanten auf. Unbestätigte Kanten werden nach max(7 d, 5× Intervall) mit Event entfernt. Eine Quellen-Policy hält Akteur und Zeit fest.
- **Implementierung:** backend/internal/relationship/cleanup.go:18-69, 105-141 (Early-Return :132-136), 156-233 (inet-Casts :180-183), 237-252 (jedes Nicht-Duration-Intervall = 24 h); pg_repository.go:242-245 (PATCH merged provenance), 629-638
- **Test(s):** PASS: TestDiscoveryEdgeCleanup, TestSourcePolicy. Regressionstests PASS: TestAuditCleanupDeadlinePerScopeKind (host/range/disabled, Grenze, stale_after), TestAuditDiscoveryResightingThroughIngest, TestAuditOfflineDetectorSwitchAndSchedules. Defekttests FAIL wie erwartet: TestAuditCleanupCronScopeDeadline (cleanup_cron_auditdefect_test.go:93), TestAuditBadScopeIsolated (:144).
- **Soll/Ist:** Auffrischen und Duration-Scopes funktionieren. Cron-Scopes gelten als 24 h, Kanten wöchentlicher Scopes werden daher nach 7 d gelöscht. Ein zonierter Scope stoppt den Cleanup für folgende Orgs. Die Provenienz ist per PATCH fälschbar.
- **Evidenz:** SP/verify-tests-run2/defect-TestAuditCleanupCronScopeDeadline.log, defect-TestAuditBadScopeIsolated.log; SP/work/verify-B20/out/probe-summary.txt; final/test-db.json PASS der 3 Regressionstests.
- **Abweichung:** Kriterium 'Cleanup nach Frist' ist verletzt (P2 Cron, P2 Mandanten-Abbruch). Quellen-Policy teilweise (P3 Provenienz, P3 source=discovery durch Nutzer).
- **Prüfebenen:** UX N/A; API LÜCKE (PATCH schützt Provenienz nicht); Daten FEHLER (vorzeitiges Löschen); E2E OK (teilweise: Job läuft im Real-Stack); Robustheit FEHLER (keine Fehlerisolation je Org)
- **Abhängigkeiten:** WP-158, WP-153, WP-136, WP-160, WP-157
- **Defekte:** F-108 (P2) Zoniertes IPv6-Scope-Ziel wird akzeptiert; Offline-Detektor und Kanten-Cleanup brechen für alle danach sortierten Orgs ab, F-111 (P2) Cleanup behandelt Cron-Zeitpläne als 24 h; Kanten wöchentlicher Scopes werden nach 7 Tagen gelöscht, F-270 (P3) Die Provenienz einer Kante (Akteur, Zeit) ist per PATCH attributes überschreibbar, F-271 (P3) POST /relationships erlaubt Nutzern Kanten mit source 'discovery'
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditCleanupCronScopeDeadline und TestAuditBadScopeIsolated bestehen. Ein API-Test zum Provenienzschutz besteht.

<a id="wp-160"></a>
#### WP-160 – Suppression: Loeschung manueller Kanten, Typbezug, fail-closed; Discovery ueberschreibt manuell nicht

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** REL-03, REL-04, REL-05
- **Anforderung/Nutzerziel:** Ein Nutzer entfernt eine falsch entdeckte Kante; Discovery erzeugt sie nicht neu. Manuelle Kanten bleiben unberührt, und beim Ladefehler der Suppressions bricht der Abgleich ab.
- **Implementierung:** backend/internal/relationship/relationship.go:522-600, 668-693; pg_repository.go:309-352; backend/internal/discovery/topology.go:245-323; power.go:170-174 (fail-closed ungetestet); frontend/src/components/cmdb/CIRelationshipsCard.tsx:165-185
- **Test(s):** PASS: TestSuppression (PG), TestTopologyFailsClosed, TestDiscoveryEdgeCleanup, Vitest CIRelationshipsCard (Mock). Regressionstest PASS: TestAuditRelationshipDeleteAuthzThroughRouter (relationship_delete_authz_audit_test.go: viewer 403, fremde Org 404, client-scoped 404, Kontrolle 409/204). Real-Stack tenant-isolation T02 PASS, T14 FAIL (500).
- **Soll/Ist:** Kriterium 1 und 3 sind erfüllt; den Widerspruch zu force löst E-01 zugunsten von REL-04. Kriterium 2 nur teilweise: Fail-closed der Versorgungskette ist nicht ausgeführt. Der Nutzerablauf ist nur per Mock belegt.
- **Evidenz:** results-backend.tsv; final/test-db.json PASS TestAuditRelationshipDeleteAuthzThroughRouter; SP/work/verify-B20/out/probe-summary.txt, xpost.txt; SP/work/tenant-iso-final.log.
- **Abweichung:** Für Kriterium 2 fehlt ein Teilbeleg (Versorgungskette). Fünf P3-Mängel: Suppression ohne Bestätigung und ohne Rücknahme, source=discovery durch Nutzer, POST quer über Mandanten → 500, DELETE → pauschal 404, WP-Text widerspricht REL-04.
- **Prüfebenen:** UX LÜCKE (nur Mock, keine Bestätigung); API OK mit P3 (500/404); Daten OK; E2E LÜCKE (kein realer Ablauf Entfernen → Suppression → Re-Ingest); Robustheit LÜCKE (Fail-closed der Versorgungskette ungetestet; Authz jetzt per Router-Test belegt)
- **Abhängigkeiten:** WP-159, WP-158, WP-161, WP-127/128, WP-045
- **Defekte:** F-145 (P2) POST /relationships mit CI einer anderen Organisation oder unbekannter CI-ID antwortet 500 statt 404/422, F-271 (P3) POST /relationships erlaubt Nutzern Kanten mit source 'discovery', F-272 (P3) DELETE /relationships bildet alle Fehler außer ErrForceRequired auf 404 ab, F-273 (P3) Eine Suppression ist irreversibel und entsteht mit einem einzigen Klick, F-274 (P3) Das Akzeptanzkriterium von WP-160 widerspricht REL-04 (wer force braucht)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Fail-closed-Test für derivePowerChain. e2e-real: Entfernen und Unterdrücken, danach kein Neuanlegen.

<a id="wp-161"></a>
#### WP-161 – Versorgungskette: PDU/USV, redundante Einspeisung, Outlet-Mapping

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** IMP-09
- **Anforderung/Nutzerziel:** Aus den Outlet-Namen von PDU und USV entstehen powered_by-Kanten mit korrekter Redundanzkennung für IMP-04.
- **Implementierung:** collector/plugins/power/outlets.go:28-78; backend/internal/discovery/power.go:49-207 (Redundanz je Batch :112-150); topology.go:313-323 (nur Confirm, Attribute nie aktualisiert); lanes.go (4 Lanes)
- **Test(s):** PASS: TestCollectReadsOutletMapping (nur APC rPDU2), TestParseOutletName, TestPowerChain (Speicher), TestSimulatedFleetIngest. Defekttest FAIL wie erwartet: TestAuditPowerRedundancyIndependentOfBatchOrder (power_chain_auditdefect_test.go:112: redundant=true auf 0 von 16).
- **Soll/Ist:** Die Kanten entstehen (34). Die Redundanzkennung, Kern des Kriteriums, ist im Standardpfad (4 Lanes) falsch und nicht deterministisch: Im Onboarding-Org des Real-Stacks sind 0 von 32 korrekt.
- **Evidenz:** SP/verify-tests-run2/defect-TestAuditPowerRedundancyIndependentOfBatchOrder.log; SP/work/verify-B20/out/realstack.txt, probe-summary.txt.
- **Abweichung:** Kriterium (Redundanzkennzeichnung) ist verletzt (P2). Nur eine Hersteller-Tabelle getestet. Ausgeschaltete Outlets zählen als Einspeisung (P3, Spezifikation offen).
- **Prüfebenen:** UX N/A; API FEHLER (falsches attributes.redundant); Daten FEHLER; E2E FEHLER (Real-Stack falsch); Robustheit LÜCKE (Tabellen von Raritan/APC sPDU/USV ungetestet)
- **Abhängigkeiten:** WP-150, WP-127, WP-179, WP-160, WP-334
- **Defekte:** F-112 (P2) Redundanzflag von powered_by hängt von Batch- und Lane-Reihenfolge ab und wird nie korrigiert (im Real-Stack falsch), F-275 (P3) Ausgeschaltete Outlets zählen als Stromeinspeisung (Redundanz)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditPowerRedundancyIndependentOfBatchOrder besteht. Real-Stack 32/32 redundant=true, stabil über zwei Zyklen. Unit-Tests der Hersteller-Tabellen.

<a id="wp-162"></a>
#### WP-162 – RETICORA_SIMULATE: deterministischer 30-Geraete-Generator am echten Ingest

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** SIM-02
- **Anforderung/Nutzerziel:** Mit RETICORA_SIMULATE=true entsteht deterministisch eine 30-Geräte-Flotte über den echten Ingest; in production ist das gesperrt.
- **Implementierung:** collector/plugins/simulate/simulate.go:19-222; collector/collectorcmd/collectorcmd.go:149, 157-160, 1337-1350; install-vm.sh:338
- **Test(s):** PASS: TestGenerateIsDeterministic, TestFleetShape, TestPluginProbeAndCollect, TestSimulationRefusedInProduction, TestSimulationUploadsTheFleet, TestSimulatedFleetIngest (PG: genau 30), simulate-ingest.test.sh 4/4. Real-Stack PASS: e2e-onboarding Test 6, D01 (genau 30 sim-*-CIs).
- **Soll/Ist:** Erfüllt; der ausgelieferte Installer setzt production.
- **Evidenz:** results-collector.tsv, results-backend.tsv, sh-simulate-ingest.log, SP/work/discovery-override/final.log, SP/work/verify-B20/out/sim-*.log.
- **Abweichung:** P3-Härtung, außerhalb der ausgelieferten Pfade: Die Sperre greift nicht, wenn RETICORA_ENVIRONMENT fehlt oder falsch geschrieben ist.
- **Prüfebenen:** UX OK; API OK; Daten OK (30, idempotent); E2E OK; Robustheit LÜCKE P3 (fail-open)
- **Abhängigkeiten:** WP-150, WP-085, WP-146, WP-161
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-276 (P3) Die Simulationssperre hängt an einem exakt gesetzten RETICORA_ENVIRONMENT (fail-open)
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Shell-Test mit leerem/'prod'/'staging'-Environment.

### Epic C – API, Kernfunktionen, UI

<a id="wp-163"></a>
#### WP-163 – RFC-7807 durchgängig: problem+json, trace_id, einheitlicher 429-Writer

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** API-03
- **Anforderung/Nutzerziel:** Jeder API-Fehler ist application/problem+json mit trace_id = Request-ID; 429 nutzt denselben Writer.
- **Implementierung:** backend/internal/api/api.go:80-217; platform/httpx/httpx.go:21-26; middleware/ratelimit.go:79,85,279-289; middleware.go:47-59,303; server/router.go:303-308
- **Test(s):** Baseline PASS: TestEveryErrorIsAProblemWithTraceID (9 Codes), TestProblemWithoutRequestIDGetsOne, TestRateLimitAndRecoveryAnswerWithTheProblemWriter; apiError.test.ts 6/6. Real-Stack PASS: ci-lifecycle L02 (422 mit trace_id), L13 (409/412 problem+json); tenant-isolation T14/T15 FAIL (500 statt 404, ext).
- **Soll/Ist:** Soll: ein Writer, problem+json mit trace_id. Ist: erfüllt (Writer-Test, PG-Router-Probe vrun2, Real-Stack 401/409/412/422).
- **Evidenz:** vrun2.log (7 Produktionspfade, trace_id == X-Request-ID); headers.log; verify-ci-lifecycle/run2.log:5,20; verify-tenant-iso/final.log:17-18.
- **Abweichung:** P3 L-T2-42, P3 L-T2-20. Ext (nicht zugerechnet, Handler-Fehlklassifikation WP-128/WP-031): T14 500, T15 500.
- **Prüfebenen:** UX: OK. API: OK. Daten: N/A. E2E: OK (L02, L13), ext FEHLER T14/T15. Robustheit: LÜCKE (Replay-trace_id).
- **Abhängigkeiten:** WP-050, WP-061; WP-128, WP-031 (Statuscodes)
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-145 (P2) POST /relationships mit CI einer anderen Organisation oder unbekannter CI-ID antwortet 500 statt 404/422, F-206 (P3) Idempotenz-Replay liefert gespeicherte Fehler wörtlich (alte trace_id; 5xx 24 h festgeschrieben), F-213 (P3) OpenAPI deklariert application/json für eine Fehlerantwort (forms submissions 400)
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN; Full-Chain-Statuscode-Test, Spec-Medientyp-Test.

<a id="wp-164"></a>
#### WP-164 – Cursor-Pagination: signiert, max. 200, durchgängig

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** API-02
- **Anforderung/Nutzerziel:** Jeder Listenendpunkt liefert {data, next_cursor} mit HMAC-Cursor; limit 50/max 200.
- **Implementierung:** backend/internal/api/cursor.go:39-115; api.go:16-20,42-68,117-125; middleware/openapi.go:80-85; credential/handler.go:88-105; ipam/pg_repository.go:252; server/pagination_test.go:91
- **Test(s):** Baseline PASS: TestCursorIsSigned, TestCursorRoundTrip, TestParsePagination, TestEveryListIsCursorPaginated (blind für Arrays), TestForgedCursorIsRejectedOnEveryRoute. Welle: TestAuditT2ListsAreCursorPages, TestAuditT2ContractHasNoBareArrayLists (FAILS_AS_EXPECTED).
- **Soll/Ist:** Soll: alle Listen paginiert. Ist: Signatur, Cap, Keyset belegt; 9 Operationen nackte Arrays (limit ignoriert), leere IPAM-Listen data:null.
- **Evidenz:** vrun2.log (/permissions?limit=1 → 103); headers.log A/B/F/G; verify-tests-run2 t2_pagination_auditdefect_test.go:69/:121.
- **Abweichung:** L-T2-43 (P2) verletzt Kriterium 'alle Listenendpunkte'; L-T2-44 (P3); L-T2-45 (P3, unbelegt).
- **Prüfebenen:** UX: N/A. API: FEHLER. Daten: OK (Keyset). E2E: LÜCKE (Real-Stack bestätigt Mängel). Robustheit: OK Cursor, LÜCKE nil-Slices.
- **Abhängigkeiten:** WP-163
- **Defekte:** F-075 (P2) Neun Listenoperationen als nackte Arrays ohne {data, next_cursor}; limit ignoriert, F-214 (P3) Leere IPAM-Listen liefern "data": null, F-215 (P3) CompleteList-Endpunkte ignorieren limit/max 200
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Beide Defekttests PASS; Runtime-Listentest (data==[]); PG-Keyset-Test.

<a id="wp-165"></a>
#### WP-165 – Listenfilter filter[field]-Operatoren, Sortier-Whitelist, fields, q

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** API-02
- **Anforderung/Nutzerziel:** Gemeinsamer Parser mit Whitelist aus Feldmetadaten; unbekannte Felder 422; JSONB nur für searchable.
- **Implementierung:** backend/internal/api/listquery.go:86-289; ci/listquery.go:14-61; ci/handler.go:88-134; ci/pg_repository.go:155-171,210-230
- **Test(s):** Baseline PASS: TestParseListQuery, TestParseListQueryErrors, TestFilterSQLAndMatch, TestSelectFields, TestListFiltersAgainstPostgres (PG). Real-Stack L09 PASS (Filter, q).
- **Soll/Ist:** Soll: API-02-Parser, Metadaten-Whitelist. Ist: auf PG belegt; Searchability pro Key org-weit → nicht-searchable Werte filterbar, gültige typisierte Filter 400.
- **Evidenz:** vrun2.log TestVB21FilterNonSearchableViaCollision; auditor run3.log; verify-ci-lifecycle/run2.log:14.
- **Abweichung:** L-T2-46 (P3) verletzt die JSONB-searchable-Regel teilweise.
- **Prüfebenen:** UX: N/A. API: OK, FEHLER Randfall. Daten: LÜCKE. E2E: OK (L09). Robustheit: OK (parametrisiert).
- **Abhängigkeiten:** WP-164, WP-116
- **Defekte:** F-216 (P3) Searchability/Typ von Attributfiltern org-weit pro Key aufgelöst
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** PG-Kollisionstest PASS; erweiterter PG-Filtertest.

<a id="wp-166"></a>
#### WP-166 – Ratelimits: Session-Bucket, 6.000/min-Org-Bucket, X-RateLimit-Header

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** API-04
- **Anforderung/Nutzerziel:** 600/min je Key/Session, 6.000/min je Org in Redis, Header auf allen Antworten; ein Client kann andere nicht aushungern.
- **Implementierung:** backend/internal/middleware/ratelimit.go:20-23,61-143 (:123-138),165-177,262-300; middleware/auth.go:148,170-171; identity/handler.go:247,304-309; cmd/server/main.go:157-169,667-676
- **Test(s):** Baseline PASS (nur Memory-Store): TestRateLimitPerCredential, TestRateLimitPerOrganization, TestRateLimitHeadersAndDefaults, TestRateLimitFailsClosed. Welle: TestAuditT2RefusedCredentialTrafficKeepsOrgBudget, TestAuditT2SessionBudgetSurvivesTokenRefresh (FAILS_AS_EXPECTED).
- **Soll/Ist:** Soll: isolierte Budgets, Header überall. Ist: Redis-Buckets und Header auf durchgelassenen Antworten; abgewiesene Requests leeren das Org-Budget, Session-Budget je Token, keine Header vor dem Limiter.
- **Evidenz:** vrun1.log (TestVB21OrgDrainRedis [429 429 429]); verify-tests-run2 t2_auth_ratelimit_auditdefect_test.go:271/:377; headers.log E.
- **Abweichung:** L-T2-47 (P1), L-T2-48 (P2), L-T2-49 (P3) verletzen das Kriterium.
- **Prüfebenen:** UX: N/A. API: FEHLER. Daten: OK (Redis geteilt). E2E: OK/LÜCKE (Header real). Robustheit: FEHLER (Org-Lockout).
- **Abhängigkeiten:** WP-163, WP-050, AUT-02. Lastlauf: Lauf 2 alle API-Budgets ok (kein Kriterium dieses WPs).
- **Defekte:** F-019 (P1) Abgewiesene Requests einer Credential leeren das Org-Budget (eine Integration sperrt die ganze Org), F-076 (P2) Session-Bucket je Access-Token, Reset bei jedem Refresh, F-217 (P3) X-RateLimit-Header und Retry-After fehlen bei vor dem Limiter abgewiesenen Antworten
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Beide Defekttests PASS (Memory+Redis); Header-Kettentest.

<a id="wp-167"></a>
#### WP-167 – API-Lebenszyklus: Deprecation/Sunset-Header, No-DB-Profil-Parität

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** API-01
- **Anforderung/Nutzerziel:** Deprecated Operationen tragen Deprecation/Sunset/Successor (≥12 Monate); beide Profile bedienen genau die dokumentierten Operationen.
- **Implementierung:** backend/internal/middleware/openapi.go:40-56,86,136-150,152-230,364-386; backend/Dockerfile:29-30; server/router.go:469-478
- **Test(s):** Baseline PASS: TestDeprecatedOperationsCarryLifecycleHeaders, TestDeprecationsNeedTwelveMonthsNotice, TestRoutesAndSpecificationAreInParity.
- **Soll/Ist:** Soll: Lifecycle-Header aus OpenAPI, Profilparität. Ist: mit geladener Spec erfüllt (Tests, PG-Router); ohne Spec still keine Header (nativer Real-Stack, keine unterstützte Bereitstellung; Image liefert Spec).
- **Evidenz:** vrun2.log (/export/cis Deprecation @1791936000, Sunset 2027-10-15); auditor run4.log; headers.log/paths.log (nativ ohne Header); install-vm.sh betrifft nur den Collector.
- **Abweichung:** P3 L-T2-50, L-T2-51, L-T2-52 (außerhalb des Kriterienkerns).
- **Prüfebenen:** UX: N/A. API: OK in-process. Daten: N/A. E2E: LÜCKE (Image nicht lokal baubar). Robustheit: FEHLER P3 (stiller Verlust).
- **Abhängigkeiten:** WP-163, WP-168, WP-189
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-218 (P3) Deprecation-Header still deaktiviert, wenn die Spec-Datei fehlt, F-219 (P3) Successor-Link mit unexpandierten URI-Templates, F-220 (P3) Keine Lifecycle-Header auf 401/403 deprecated Operationen
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN; e2e-real mit gesetzter Spec, Spec-missing-Test.

<a id="wp-168"></a>
#### WP-168 – Abweichende REST-Pfade auf Katalogvertrag umstellen (zehn Ressourcen)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** API-05
- **Anforderung/Nutzerziel:** Die zehn Ressourcen laufen unter API-05-Pfaden; alte Pfade mit Deprecation; Frontend nutzt neue Pfade.
- **Implementierung:** ipam/handler.go:33; export/job_handler.go:47; user/handler.go:119-120,386-404; identity/handler.go:117; router.go:469-478; frontend/src/api/client.ts:529-531,576-579,695-702,1357,2002-2008,2113-2116; pages/CIListPage.tsx:118-123
- **Test(s):** Baseline PASS: TestCatalogResourcesAreServed, TestRoutesAndSpecificationAreInParity u. a.; Vitest (gemockt). e2e-onboarding-real 6/6. Welle: deprecatedPaths.auditdefect.ts (FAILS_AS_EXPECTED).
- **Soll/Ist:** Soll: Katalogpfade, Frontend auf neuen Pfaden. Ist: Backend vollständig, Frontend real bestätigt; CI-Listen-Export nutzt deprecated /export/cis als Link → 401.
- **Evidenz:** Verifier Real-Browser run.log; paths.log; verify-tests-run2 frontend-defect deprecatedPaths.auditdefect.ts:92.
- **Abweichung:** L-T2-53 (P2) verletzt Kriterium 2. Verknüpft, nicht zugerechnet: L-T2-54 (Audit-Felder, gleicher Handler für alten/neuen Pfad), L-T2-50, L-T2-11.
- **Prüfebenen:** UX: FEHLER (Export 401). API: OK. Daten: OK. E2E: OK teilweise. Robustheit: OK.
- **Abhängigkeiten:** WP-167, WP-088, WP-116, WP-129, WP-189
- **Defekte:** F-017 (P1) Rollenzuweisung nicht entziehbar: kein DELETE /role-assignments/{id}, F-077 (P2) CI-Listen-Export nutzt deprecated /api/v1/export/cis als Link → 401 im Browser, F-078 (P2) Audit-Seite liest Felder, die die API nicht liefert ('Invalid Date', Objekt '—'), F-218 (P3) Deprecation-Header still deaktiviert, wenn die Spec-Datei fehlt
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** deprecatedPaths.auditdefect.ts PASS; e2e-real CI-Export-Download PASS.

<a id="wp-169"></a>
#### WP-169 – CI-include (Beziehungen, Kontakte, Asset) und Restore-Endpunkt

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** API-05, CI-05
- **Anforderung/Nutzerziel:** CI mit eingebetteten Beziehungen/Kontakten/Asset abrufen und gelöschte CIs innerhalb der Aufbewahrung wiederherstellen.
- **Implementierung:** ci/handler.go:186-250; ci/service.go:96-98,367-373; ci/pg_repository.go:992-1060; server/router.go:648-662; api/openapi.yaml:805-833; config.go:267,313
- **Test(s):** Baseline PASS: TestHandler_IncludeAndRestore, TestRestoreAgainstPostgres, TestDeleteDecommissionsAndHardDeletes. Welle: TestT2CIIncludeWiring (Regression PASS, mechanisch). Real-Stack PASS: T05, L11.
- **Soll/Ist:** Soll: include und Restore innerhalb der (konfigurierbaren) Retention. Ist: include und Restore bei Default belegt (auch real); Restore-Fenster fest 90 d.
- **Evidenz:** vrun2.log TestVB21RestoreEdges (a)-(c); verify-ci-lifecycle/run2.log:17 (L11 ✓); verify-tenant-iso/final.log:8 (T05 ✓).
- **Abweichung:** L-T2-55 (P3) verletzt 'innerhalb der Aufbewahrung' bei konfigurierter Retention; L-T2-56, L-T2-57 (P3).
- **Prüfebenen:** UX: N/A. API: OK, LÜCKE 409 undokumentiert. Daten: OK. E2E: OK (L11, T05). Robustheit: LÜCKE.
- **Abhängigkeiten:** WP-168, WP-170, WP-135
- **Defekte:** F-221 (P3) Restore-Fenster fest 90 Tage statt konfigurierter Retention, F-222 (P3) Restore-Konflikt 409 nicht in OpenAPI dokumentiert, F-223 (P3) include kappt eingebettete Sammlungen still bei 200
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Restore-Fenster aus Konfiguration mit Test; 409 dokumentiert; Restore-Edge-Tests.

<a id="wp-170"></a>
#### WP-170 – CI-Löschen: decommissioned, Traversal-Stopp, 90-Tage-Hard-Delete mit IP-Bereinigung

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** CI-05, LCY-01
- **Anforderung/Nutzerziel:** Löschen setzt deleted_at/decommissioned, löst Asset, Lifecycle bleibt; Traversal überspringt; nach 90 Tagen Hard-Delete inkl. IPs.
- **Implementierung:** ci/pg_repository.go:937-981; jobs/harddelete.go:16-40; jobs/retention.go:71-78,114-142; cmd/server/main.go:485-486; topology.go:344-347; relationship/pg_repository.go:490-535
- **Test(s):** Baseline PASS: TestDeleteDecommissionsAndHardDeletes (PG), TestRetentionRunsAgainstPostgres, TestTraversalStaysInsideScope (PG), TestImpactSkipsDeletedCIs. Real-Stack PASS: L11, T05.
- **Soll/Ist:** Soll und Ist übereinstimmend; alle Kriterien auf PG und Real-Stack belegt.
- **Evidenz:** verify-ci-lifecycle/run2.log:17 (L11 ✓: decommissioned, Impact/Suche ohne CI, Restore); vrun3.log (Lifecycle erhalten); FK-Abfrage audit_db_23; scheduler_run retention.soft_deleted.
- **Abweichung:** -
- **Prüfebenen:** UX: N/A (keine UI-Löschaktion). API: OK. Daten: OK. E2E: OK (L11). Robustheit: OK; LÜCKE exakte 90-Tage-Grenze.
- **Abhängigkeiten:** WP-169 (L-T2-55), WP-131, WP-031, WP-135
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN; Härtung: Grenzwerttest, Schedule-Registrierungstest.

<a id="wp-171"></a>
#### WP-171 – Einheitliche CI-Historie mit Standort/Zeit; Audit-Abfrage

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** CI-06, AUD-03
- **Anforderung/Nutzerziel:** Ein Operator sieht eine chronologische CI-Historie (Änderungen, Audit, location_change, Overrides) mit Zeit und Standort auf der Detailseite.
- **Implementierung:** backend/internal/history/pg_repository.go:63-148; history/handler.go:41-131; route_permissions.txt:154; migrations/000073:18-22; frontend CIDetailSections.tsx:568-610, CIDetailPage.tsx:146-149,238
- **Test(s):** Pass: TestUnifiedCIHistory, TestHistoryRepositoryClientScope; neu TestAuditT3HistoryIncludesOverrides pass. Real-Stack: L06, L11, B01, J01 pass; L08 funktional pass; L07 FAIL (ext). Defekttest TestAuditT3HistoryIncludesLocationChange fails-as-expected.
- **Soll/Ist:** ci_change/entity_change/Audit/Overrides vereint; location_change fehlt, Historie nur für org_admin, rohe UUIDs, AUD-03-Schema nicht umgesetzt.
- **Evidenz:** probe-history.log '0 → 0'; L06/L08 History mit Zeit/Site; psql audit:read nur org_admin; \d ci_change
- **Abweichung:** P2 L-T3-20, L-T3-21, L-T3-22; P3 L-T3-41; ext Audit-Viewer 'Invalid Date' (T7).
- **Prüfebenen:** UX LÜCKE (UUIDs, Fehlertext für Nicht-Admins); API OK (400/401/403); Daten LÜCKE (location_change, AUD-03); E2E OK teilweise (L06/L08 als Admin), FEHLER L07 (ext); Robustheit OK
- **Abhängigkeiten:** WP-055, WP-133, WP-046/E-13, WP-090
- **Defekte:** F-093 (P2) Einheitliche CI-Historie enthält keine location_change-Einträge, F-094 (P2) CI-Historie für alle Standardrollen außer org_admin unbenutzbar (audit:read), F-095 (P2) AUD-03-ci_change-Schema und -Vokabular nicht umgesetzt (change_kind, field_changes, Quellen-/Request-Korrelation, eine ci_change je Record; 'type_change' statt 'type_changed'), F-237 (P3) CI-Detailseite zeigt Standort/Site/Raum als rohe UUIDs und Zeitstempel unformatiert, extern: The audit viewer shows 'Invalid Date' as the time and '—' as the object of every entry: the UI reads fields the API does not return
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT3HistoryIncludesLocationChange PASS; Rollenvertrag + API-Test; AUD-03 umgesetzt oder per Entscheidung eingegrenzt; lesbare Standortanzeige.

<a id="wp-172"></a>
#### WP-172 – CI-Typwechsel: Discovery-Upgrade generic_device → spezifisch, manueller Wechsel

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** CI-11
- **Anforderung/Nutzerziel:** Discovery hebt generic_device auf einen spezifischen Typ; manueller Wechsel mit Pflichtfeld-Befunden, Historie (type_changed) und Audit.
- **Implementierung:** backend/internal/ci/typechange.go:53-82; ci/pg_repository.go:1727-1787; discovery/discovery.go:1473-1486,1890-1912; ci/handler.go:256-283
- **Test(s):** Pass: TestTypeChangeAgainstPostgres, TestChangeTypeHandler, TestDiscoveryMayReclassify, TestDiscoveryRaisesGenericDevice (Memory), Authz-Subtest; Verifier-PG-Probes probe-discovery*.log.
- **Soll/Ist:** Service, Befunde, Audit, Event ci.changed und Discovery-Pfad belegt; ci_change schreibt 'type_change' statt geforderter 'type_changed' (Teil von L-T3-22); Raise in separater Transaktion.
- **Evidenz:** probe-discovery.log 'type_change=1 audit ci.type_changed=1'; psql chk_ci_change_type erlaubt 'type_change'
- **Abweichung:** P2 L-T3-22 (WP-Anteil: Vokabular type_change); P3 L-T3-54.
- **Prüfebenen:** UX N/A; API OK; Daten LÜCKE (Vokabular, mehrere ci_change je Record); E2E OK (Ingest→PG-Probe); Robustheit OK
- **Abhängigkeiten:** WP-119, WP-136, WP-151, WP-171
- **Defekte:** F-095 (P2) AUD-03-ci_change-Schema und -Vokabular nicht umgesetzt (change_kind, field_changes, Quellen-/Request-Korrelation, eine ci_change je Record; 'type_change' statt 'type_changed'), F-250 (P3) Discovery-Reklassifizierung läuft in separater Transaktion nach dem Record-Update
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation BESTANDEN → final TEILWEISE BESTANDEN; BESTANDEN → TEILWEISE BESTANDEN: Kriterium 1 nennt type_changed (CI-11 'ci_change type_changed'), implementiert ist 'type_change'. Das ist Teil des bestätigten P2-Defekts L-T3-22, und die verbindliche Regel lässt BESTANDEN nicht zu. Der Discovery-Nachweis beruht zudem nur auf Probes und einem Memory-Test.
- **Offene Nachweise / Retest:** Vokabular angeglichen oder per Entscheidung festgelegt; Discovery-PG-Probe und Event-Assertion als Repo-Tests.

<a id="wp-173"></a>
#### WP-173 – CI-Merge: Referenzübernahme, Override-Konflikte, Tombstone/301, Snapshot

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** CI-12
- **Anforderung/Nutzerziel:** Duplikate werden zusammengeführt: Referenzen wandern, Konflikte werden Reviews, Opfer liefert 301, Snapshot 30 Tage, parallele Merges 409.
- **Implementierung:** backend/internal/ci/merge.go:35-441,582-597; ci/handler.go:157-165,287-323; Migration 000131; discovery/review.go:118-146
- **Test(s):** Pass: TestMergeAgainstPostgres, TestParallelMerge, TestJobAPIRevert (Memory); Verifier-HTTP-Probe [200 409]×10. Defekttest TestAuditT3ProbableDuplicateMergeIsCIMerge fails-as-expected.
- **Soll/Ist:** POST /merge erfüllt Semantik und 409; Job nur synchron gespiegelt; einziger UI-Merge (Review) macht Soft-Delete statt CI-12.
- **Evidenz:** probe-ci.log 'map[[200 409]:10]'; probe-review.log 'merged_into="" ci_merge rows=0'
- **Abweichung:** P2 L-T3-23; P3 L-T3-55; Kriterium 1 'als Job' nur synchron/probe-belegt.
- **Prüfebenen:** UX LÜCKE (Review-Merge = Löschen); API OK; Daten LÜCKE (Referenzen bleiben am Duplikat, keine Retention); E2E LÜCKE; Robustheit OK
- **Abhängigkeiten:** WP-170, WP-089, WP-129
- **Defekte:** F-096 (P2) Review-Auflösung merge bei probable_duplicate löscht weich statt CI-12-Merge, F-251 (P3) ci_merge.job_id nie gesetzt; abgelaufene Snapshots/Tombstones nie bereinigt
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT3ProbableDuplicateMergeIsCIMerge PASS; Repo-Test ci.merge-Job-Lebenszyklus.

<a id="wp-174"></a>
#### WP-174 – CI-Unmerge innerhalb der Snapshot-Frist

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** CI-12
- **Anforderung/Nutzerziel:** Innerhalb von 30 Tagen wird ein falscher Merge rückgängig gemacht; danach 410.
- **Implementierung:** backend/internal/ci/merge.go:614-795; ci/handler.go:325-353
- **Test(s):** Pass: TestUnmergeAgainstPostgres, Authz-Subtest; Verifier-Probe TestVerifyB22UnmergeRestore. Defekttest TestAuditT3UnmergeKeepsLaterSurvivorEdits fails-as-expected.
- **Soll/Ist:** Wiederherstellung und 410 belegt; spätere Survivor-Änderungen werden still überschrieben.
- **Evidenz:** probe-ci.log equal(before,after)=true; Defekttest 'survivor name after unmerge … want the later edit'
- **Abweichung:** P2 L-T3-24.
- **Prüfebenen:** UX N/A (API-only); API OK; Daten FEHLER (stiller Verlust späterer Änderungen); E2E LÜCKE; Robustheit OK
- **Abhängigkeiten:** WP-173
- **Defekte:** F-097 (P2) Unmerge überschreibt nach dem Merge vorgenommene Survivor-Änderungen still
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Entschiedener Vertrag + TestAuditT3UnmergeKeepsLaterSurvivorEdits PASS.

<a id="wp-175"></a>
#### WP-175 – Rack-API: GET layout, PUT mounts/{ci_id} mit Fehlervertrag

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** RCK-02
- **Anforderung/Nutzerziel:** Rack-Belegung über GET layout lesen, CIs per PUT montieren/verschieben mit 409; alte Routen deprecated; Frontend umgestellt.
- **Implementierung:** backend/internal/rack/handler.go:34-36,233-345; rack/pg_repository.go:325-389; api/openapi.yaml:5795-5943,12252-12259; frontend/src/api/hooks.ts:241-266; RackPage.tsx:44,62
- **Test(s):** Pass: TestLayoutAndPutMount (Memory), TestRackRulesAgainstPostgres, TestDeprecatedOperationsCarryLifecycleHeaders (generisch); Real-Stack L06, L07 pass, L08 FAIL (mounts:null); Verifier-Probes.
- **Soll/Ist:** Endpunkte und 409 über echten Stack, Frontend liest layout (L06); leeres Rack liefert mounts:null entgegen OpenAPI; Deprecation-Header nur generisch belegt.
- **Evidenz:** L06 'Position 10 · 2 HE · front' nach Reload; L07 409-Fälle; L08 'mounts = null'
- **Abweichung:** P3 L-T3-57 (Vertrag von Kriterium 1); P3 L-T3-56.
- **Prüfebenen:** UX OK (L06 Lesesicht), LÜCKE (kein Mount-UI, N+1); API FEHLER gering (null statt []); Daten OK; E2E OK (L06/L07), FEHLER L08; Robustheit OK (Race 201/409)
- **Abhängigkeiten:** WP-126, WP-167, WP-078
- **Defekte:** F-252 (P3) Rack-Seite lädt jedes montierte CI einzeln (1+N); putMount-Client ungenutzt, F-253 (P3) GET /racks/{id}/layout liefert "mounts": null für leeres Rack (OpenAPI: Pflicht-Array)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** mounts=[] behoben und Real-Stack L08 grün; Repo-PG-Test neue Routen; Deprecation-Header für Rack-Pfade gezielt geprüft.

<a id="wp-176"></a>
#### WP-176 – Impact entlang impact_direction; connected_to als Heuristik

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** IMP-01, IMP-03
- **Anforderung/Nutzerziel:** Impact folgt impact_direction; connected_to nur als gekennzeichnete Netzheuristik; falsche Tests korrigiert.
- **Implementierung:** backend/internal/topology/impact.go:95-146 GetBlastRadius, :151-176 walkDirected, :223-410, :560-672; frontend/src/pages/TopologyPage.tsx:80-153; CIDetailSections.tsx:417-470
- **Test(s):** Pass: TestImpactDirectionAgainstPostgres, TestComputeImpactConnectedToIsHeuristic; Vitest TopologyPage (kodiert altes Verhalten); Real-Stack ci-lifecycle L08 pass. Defekttest TestAuditT3BlastRadiusMarksHeuristicCIs fails-as-expected.
- **Soll/Ist:** /impact korrekt und markiert; Blast Radius ohne Heuristik-Kennzeichen; Frontend-BFS und sein Test nicht korrigiert.
- **Evidenz:** probe-topo.log 'affected_count=2 … (no flag)'; probe-frontend-outage.log 'pdu fails -> []'
- **Abweichung:** P2 L-T3-25; P3 L-T3-58 (Kriterium 2); P2 L-T3-26 außerhalb des WP-Umfangs (statusneutral, WP-031).
- **Prüfebenen:** UX LÜCKE (Marker nie gerendert, inverse Fallback-Menge); API FEHLER (blast-radius); Daten OK; E2E OK (L08 Richtung), LÜCKE Marker; Robustheit OK
- **Abhängigkeiten:** WP-127, WP-031
- **Defekte:** F-098 (P2) Blast Radius zählt nur über connected_to erreichte CIs ohne Heuristik-Kennzeichen als betroffen, F-099 (P2) GET /cis/{id}/dependencies nutzt rohe Kantenrichtung, invers zu impact_direction (außerhalb WP-176, IMP-07/WP-031), F-254 (P3) Client-seitige Ausfallsimulation folgt roher source→target-Richtung; ihr Vitest kodiert das alte Verhalten
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditT3BlastRadiusMarksHeuristicCIs PASS; korrigierter TopologyPage-Vitest.

<a id="wp-177"></a>
#### WP-177 – Impact: 20 Hops, Pfade mit Typfolge, Kürzestpfad, Kategorien, projizierte physical-Kanten

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** IMP-02, NFR-02
- **Anforderung/Nutzerziel:** Impact bis 20 Hops mit kürzestem Pfad und Typfolge, Kategorienfilter, projiziertem Enthaltensein, ohne N+1.
- **Implementierung:** backend/internal/topology/topology.go:27-28,264-402,443-455; impact.go:324-432,563-566; relationship/pg_repository.go:93-99,353-450
- **Test(s):** Pass: TestImpactPathsAgainstPostgres (21-Hop-Kette, Zähler nodes=1), TestComputeImpactPathsAndCategories, TestImpactReadsNodesInOneQuery; Real-Stack ci-lifecycle L08 pass; Lastlauf 2 impact 1135 ms / impact_full 1258 ms.
- **Soll/Ist:** Verhalten aller Kriterien PG-belegt; Technik Ebenen-BFS statt rekursivem CTE (dokumentierte WP-206-Korrektur, lasttest-g1.md:526).
- **Evidenz:** impact_integration_test.go:148-173; results-backend.tsv; Lastlauf 2
- **Abweichung:** P3 L-T3-59 (Technikabweichung ohne E-Eintrag, depth=abc still).
- **Prüfebenen:** UX N/A; API OK; Daten OK; E2E N/A (L08 stützt); Robustheit OK
- **Abhängigkeiten:** WP-176, WP-079, WP-206/WP-335
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-255 (P3) Impact-Traversierung per Ebenen-BFS statt rekursivem CTE ohne formale Entscheidung; ungültiger depth-Parameter still akzeptiert
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Formelle Entscheidung zur CTE→BFS-Abweichung; Statement-Zähler für Kanten (optional).

<a id="wp-178"></a>
#### WP-178 – Nachweis assetfreie CIs in Historie und Impact

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** CI-09
- **Anforderung/Nutzerziel:** Logische CIs ohne Asset nehmen an Beziehungen, Suche, Topologie, Historie und Impact teil – per Integrationstest belegt.
- **Implementierung:** backend/internal/ci/assetfree_integration_test.go:29-105 (Test-only-WP)
- **Test(s):** Pass: TestAssetFreeCIAgainstPostgres (0,69 s); Verifier-Probe TestVerifyB22AssetFreeLogical (Typ application, is_logical).
- **Soll/Ist:** Erfüllt und gegen PG ausgeführt.
- **Evidenz:** results-backend.tsv pass; probe-ci.log 'history HTTP 200 total=4 … search hit=true'
- **Abweichung:** -
- **Prüfebenen:** UX N/A; API OK; Daten OK; E2E N/A; Robustheit OK
- **Abhängigkeiten:** WP-171, WP-177
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Optional: Repo-Test mit logischem Typ und expliziter Historien-Assertion.

<a id="wp-179"></a>
#### WP-179 – Redundanz-Postfilter in Go

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** IMP-04
- **Anforderung/Nutzerziel:** In der Impact-Analyse gilt ein redundant versorgtes CI (powered_by, member_of_cluster) nur dann als betroffen, wenn alle seine Versorgungen ausgefallen sind; die findSPOFs-Heuristik ist ersetzt.
- **Implementierung:** backend/internal/topology/redundancy.go:37-55 redundantProviders (nutzt nur Kanten des geladenen Graphen), :72-128 downClosure, :134-161 redundancyFindings; impact.go:560-616 writeImpact (buildFromRoot(ctx, orgID, id, depth) :591, computeImpact :614), :95-145 GetBlastRadius (maxDepth 20, topology.go:28); relationship/pg_repository.go:368-428 TraverseFrom (tiefenbegrenzt). findSPOFs entfernt (grep in backend/frontend ohne Treffer). UI: CIDetailSections.tsx:444-453; topologyApi.impactMany sendet keine depth (client.ts:697-703). Produktionscode seit Baseline 3a9669a unverändert (git diff 3a9669a HEAD: nur Test-/Evidenzdateien).
- **Test(s):** Bestehend, PASS in Baseline (SP/runs/results-backend.tsv) und im finalen Regressionslauf auf HEAD (SP/final/test-db.log, 2021 Tests, gotestsum exit=0): internal/topology TestRedundancyPostfilter (7 Subtests), TestRedundancyFindings, TestImpactRulesUseOrganizationTypes, TestMultiImpactAgainstPostgres. redundancy_test.go:13-77 arbeitet mit vollständigen Kantenlisten und kann den Tiefenfehler nicht erkennen. Verifier-Probe f1 TestAuditProbeRedundancyDepthBoundary (audit_db_21): /impact/pdu-a → [] | ?depth=1 → [server] | /impact?ci_ids=pdu-a&depth=1 → [server] | /topology/cis/pdu-a/impact?depth=1 → [server] | depth=2 → []. Defekttest-Welle: kein Test (nicht kodiert).
- **Soll/Ist:** Soll: Ein Abhängiger redundanten Typs fällt nur aus, wenn alle Versorger ausgefallen sind; ein Versorger außerhalb der Analyse hält ihn oben. Ist: Versorger werden nur aus dem tiefenbegrenzten Teilgraphen gesammelt; eine alternative Versorgung einen Hop jenseits der Tiefe ist unsichtbar, der Abhängige wird als sicher betroffen gemeldet.
- **Evidenz:** Probe f1 (Re-Run audit_db_21) reproduziert die IMP-04-Verletzung auf drei öffentlichen Endpunkten (HTTP 200, impacted=[server] bei depth=1, obwohl pdu-b versorgt). Baseline-/Final-Tests grün nur bei Default-Tiefe 20.
- **Abweichung:** IMP-04 bei tiefenbegrenzter Analyse beobachtet verletzt (L-T7-ui-04, P2); depth ist dokumentierter OpenAPI-Parameter (1-20). Die UI ist nicht betroffen (sendet keine depth).
- **Prüfebenen:** UX: LÜCKE – SPOF-Liste im CI-Detail nur gemockt getestet, UI nutzt Default-Tiefe. API: FEHLER – /impact/{id}, /impact?ci_ids, /topology/cis/{id}/impact mit depth=1 falsch positiv (Probe f1). Daten: OK – redundant-Flag aus relationship_type inkl. Org-Overlay (impact.go:211-243). E2E: LÜCKE – kein Real-Stack-Lauf des Redundanzergebnisses. Robustheit: FEHLER – Tiefenlimit/MaxTraversalNodes machen Versorger unsichtbar, Ergebnis falsch statt 'unbekannt'.
- **Abhängigkeiten:** WP-177 (Pfade), WP-161 (Power); die WP-180-Endpunkte erben den Defekt.
- **Defekte:** F-160 (P2) Redundanz-Postfilter meldet ein redundant versorgtes CI als betroffen, wenn die Tiefe die alternative Versorgung abschneidet
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Fix der Versorgerermittlung an der Tiefengrenze; committeter PG-Regressionstest (pdu-a/pdu-b/server/vm, ups→pdu-a): /impact/{pdu-a}?depth=1, /impact?ci_ids={pdu-a}&depth=1, /topology/cis/{pdu-a}/impact?depth=1, /impact/{ups}?depth=2 ohne server/vm, Negativfall ci_ids=pdu-a,pdu-b&depth=1 mit server; Blast-Radius-HTTP-Test auf PG (SPOF/protected, 404 für fremden Client).

<a id="wp-180"></a>
#### WP-180 – Mehrfachausfall /impact?ci_ids= und /impact/{ci_id}; 422 über 2.000

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** IMP-05, IMP-06
- **Anforderung/Nutzerziel:** Mehrfachausfälle werden über /impact?ci_ids= und /impact/{ci_id} gemeinsam analysiert; /topology ohne Root antwortet über 2.000 Knoten mit 422; die UI filtert Typen per CI-Typ-ID.
- **Implementierung:** backend/internal/topology/topology.go:80-92 Routen, :26 MaxTopologyNodes=2000, :212-223 buildFull (422 mit Hinweis root_ci_id/site_id/client_id/ci_type), :120-140 GetTopology; impact.go:434 MaxImpactCIs=100, :447-479 GetMultiImpact, :560-672 writeImpact; route_permissions.txt:164-165; api/openapi.yaml:5027,5080. Frontend TopologyPage.tsx:111-136 (Optionen value=type.id, 422 → Startdialog), :145-153 useImpactMany mit stillem Fallback computeOutageImpact (:80-94); hooks.ts:214-225; client.ts:697-703.
- **Test(s):** PASS (Baseline + finaler Lauf SP/final/test-db.log): internal/topology TestMultiImpactAgainstPostgres (gemeinsame Analyse, Summary, Pfade, 400/404/422, 2001+1 CIs → 422, ci_type=<id> → 1 Knoten), TestImpactCatalogPath, TestImpactFields, TestImpactFieldsMatchSpec. Vitest (fetch gestubbt, SP/runs/results-frontend.json): TopologyPage 'filters by CI type id', 'asks for a filter when the topology is too large', 'opens a start dialog with site and type above 2,000 CIs (UI-06)', 'simulates several failing CIs jointly (IMP-05)' PASS; e2e-mocked topology.spec.ts PASS (chromium, mobile-chrome; Mock). Probe f1 (audit_db_21): 101 IDs → 422 'at most 100 CIs per analysis', 100 → Limit passiert; /topology?root_ci_id=<unbekannt|OrgB-CI> → 500. Real-Stack tenant-isolation.spec.ts T06 PASS (B liest weder Impact noch Blast-Radius von A, /impact?ci_ids=B,A → 404), T15 FAIL (500). Lastlauf k6 Lauf 2: impact p95 1135 ms, impact_full 1258 ms innerhalb Budget.
- **Soll/Ist:** Soll: gemeinsame Analyse-Endpunkte, 422 über 2.000, UI-Typfilter per ID. Ist: Backend implementiert und auf PG getestet; UI nur gegen Mocks belegt; Impact-Panel fällt bei API-Fehler still auf eine richtungsnaive Client-Berechnung zurück; /topology mit unbekannter/fremder Root → 500; Redundanzergebnis bei kleiner depth falsch (geerbt).
- **Evidenz:** Backend-Kriterien mit ausgeführter PG-Evidenz (TestMultiImpactAgainstPostgres, Probe f1 101 → 422). UI-Kriterium nur mit Mock-Evidenz (Vitest, gemocktes Playwright). Probe f1 und Real-Stack T15 reproduzieren die 500 bei falscher Root.
- **Abweichung:** UI-Typfilter-Kriterium nur mit Mocks belegt (kein UI→API→DB); geerbter Tiefen-/Redundanzdefekt L-T7-ui-04 (P2) auf den WP-180-Endpunkten; /topology mit unbekannter/fremder Root → 500 (L-T7-ui-05, P2); stiller Client-Fallback bei /impact-Fehler (L-T7-ui-25, P3, Hypothese).
- **Prüfebenen:** UX: LÜCKE – Typfilter, 422-Startdialog und gemeinsame Simulation nur gemockt; stiller Client-Fallback bei /impact-Fehler. API: FEHLER – 400/404/422 und 101 IDs → 422 korrekt, aber unbekannte/fremde root_ci_id → 500 und geerbter Tiefendefekt. Daten: OK – RLS-gescopte Repositories (TestTraversalStaysInsideScope, TestChannelMatrix, Real-Stack T06). E2E: LÜCKE – kein e2e-real der Topologieseite; T06 PASS, T15 FAIL. Robustheit: FEHLER – 500 bei falscher Root, Tiefen-/Redundanzfehler.
- **Abhängigkeiten:** WP-179 (Redundanzdefekt), WP-168 (Pfade); WP-195 nutzt die Endpunkte.
- **Defekte:** F-160 (P2) Redundanz-Postfilter meldet ein redundant versorgtes CI als betroffen, wenn die Tiefe die alternative Versorgung abschneidet, F-161 (P2) GET /topology mit unbekannter oder fremder root_ci_id antwortet 500 statt 404, F-326 (P3) Topologie-Simulation zeigt bei /impact-Fehler still eine richtungsnaive Client-Berechnung
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** e2e-real Topologie-Spec (Typ-ID-Filter, 422-Startdialog, gemeinsame Simulation = Backend-Antwort /impact?ci_ids); Fix L-T7-ui-04 mit PG-Test; API-Tests ci_ids 100/101 und Cross-Tenant/Client; /topology mit falscher Root → 404 und T15 grün; Vitest für den /impact-Fehlerzustand.

<a id="wp-181"></a>
#### WP-181 – Bulk-API: bis 500 IDs, ab 100 als Job, Ergebnis je ID

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** API-08, BLK-01
- **Anforderung/Nutzerziel:** Statusänderungen für bis zu 500 CIs in einem Request, ab 100 IDs als Job (202); Ergebnis je ID; die UI nutzt die Bulk-API und zeigt Einzelfehler.
- **Implementierung:** backend/internal/ci/bulk.go:74-100 validateBulk (keine ID-Formatprüfung), :105-121 Bulk (sync < 100, sonst Enqueue + Goroutine), :124-134 runBulkJob (kein Cancel-Check), :142-169 runBulk → s.Update(ctx, orgID, id, UpdateRequest{Status: &status}) OHNE Manual (:152), :173-192 bulkError (Default 500); ci/pg_repository.go:783-789 (Manual==nil → dropOverriddenFields), :845-856 (ohne SET-Klausel 'before' zurück; Version nur bei Manual/Authoritative), :1606-1638; handler.go:486 (PATCH setzt Manual); server/router.go:339,351,355; route_permissions.txt:368. UI CIListPage.tsx:34 limit=25, :46-54 verwirft unsichtbare Auswahl, :73-110 applyBulkStatus mit Fehlerliste.
- **Test(s):** PASS (Baseline + final): internal/ci TestBulkAgainstPostgres, TestBulkStatusReportsEachID, TestBulkLimits, TestBulkFromHundredIDsIsAJob; Vitest CIListPage (2, gemockt). Real-Stack frontend/e2e-real/bulk-jobs-export.spec.ts (SP/work/verify-bulk-jobs-export/final.log): B01 PASS (ein POST /cis/bulk, Persistenz, Historie/Audit), B02 PASS (gelöschtes CI → 404 im Bericht und in der UI-Fehlerliste), B03 PASS (0/501 IDs, falscher Status/Aktion, fremder Tenant), J01 PASS (100 IDs → 202, Jobcenter-Ergebnis 98/2), B04 FAIL (fehlerhafte ID → Item-Code 500). Defekttest-Welle (Commit c4027bc, mechanisch verifiziert SP/verify-tests-t357/results.tsv): TestAuditT7BulkReportMatchesStoredStatus (3 Subtests) FAILS_AS_EXPECTED ('bulk report says ok/200 but ci.status = maintenance, want inactive' für Sync- und Job-Pfad; 'version after bulk set_status = 1, want 2', PATCH mit Vor-Bulk-If-Match '1' = 200), TestAuditT7BulkJobCancelStopsWork FAILS_AS_EXPECTED ('DELETE /api/v1/jobs/{id} -> 200 with 4 of 500 CIs changed … the job still changed all 500 CIs'). Probe f2 (audit_db_21) identisch. Lastlauf k6 Lauf 2: bulk500 12,7 s innerhalb Budget.
- **Soll/Ist:** Soll: jede angefragte ID wird geändert oder mit Fehler gemeldet; die Bulk-Änderung eines Nutzers ist ein manueller Schreibvorgang (API-07/OVR-01). Ist: IDs mit geschütztem Status-Override werden übersprungen, aber ok/200 gemeldet; geänderte CIs behalten Version und erhalten keinen Override; fehlerhafte IDs → 500; Cancel eines laufenden Jobs lässt alle Änderungen zu und verwirft den Bericht; UI auf 25 sichtbare Zeilen begrenzt.
- **Evidenz:** Defekttest TestAuditT7BulkReportMatchesStoredStatus und Probe f2 reproduzieren den Falsch-Erfolgsbericht und fehlende Version/Override; Real-Stack B04 reproduziert die 500; B01/B02/B03/J01 belegen den übrigen Ablauf UI→API→DB.
- **Abweichung:** Kriterium 'Ergebnis je ID' beobachtet verletzt: Partial-Failure-Report unzuverlässig und Bulk kein manueller Schreibvorgang (L-T7-ui-01, P1); Cancel wirkungslos (L-T7-ui-06, P2); fehlerhafte ID → 500 (L-T7-ui-26, P3); UI auf 25 IDs begrenzt (L-T7-ui-27, P3, Hypothese).
- **Prüfebenen:** UX: LÜCKE – Sync-Pfad real belegt (B01/B02), aber max. 25 IDs, Job-Pfad aus der Liste unerreichbar. API: FEHLER – falsches ok für überschriebene CIs, fehlerhafte ID → 500, kein manueller Schreibvorgang, DELETE stoppt laufenden Job nicht. Daten: FEHLER – Statusänderung bei überschriebenen CIs still nicht persistiert, Bericht bei Cancel verloren. E2E: LÜCKE – B01/B02/B03/J01 PASS, B04 FAIL; kein E2E für überschriebene CIs oder Cancel. Robustheit: FEHLER – keine Cancel-Behandlung, In-Process-Goroutine, ID-Format unvalidiert.
- **Abhängigkeiten:** WP-129/WP-130 (Jobmodell), WP-163 (Fehler); Cancel-Semantik gemeinsam mit WP-182; gleicher Ursachenkern wie T3-Defekttest TestAuditT3BulkStatusIsManualWrite.
- **Defekte:** F-020 (P1) Manuelle Massen-Statusänderung (POST /cis/bulk set_status) ist kein manueller Schreibvorgang: kein Override, keine Versionserhöhung, unter Status-Override still verworfen und als ok gemeldet, F-162 (P2) Job-Cancel stoppt laufende Arbeit nicht (ci.bulk, Export): Status 'cancelled', aber alle Änderungen werden ausgeführt bzw. der Export fertiggestellt, Teilbericht geht verloren, F-327 (P3) Bulk-Bericht meldet eine fehlerhafte CI-ID mit Code 500 'the CI could not be updated' statt der 400 der Einzeländerung, F-328 (P3) UI-Bulk auf 25 sichtbare Zeilen begrenzt; der Job-Pfad (≥ 100 IDs) ist aus der CI-Liste unerreichbar
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT7BulkReportMatchesStoredStatus und TestAuditT7BulkJobCancelStopsWork PASS (go test -tags auditdefect, ./internal/ci/); Real-Stack B04 PASS; API-Grenz-/Isolationstests (99 sync, 100/500 Job, 501 → 422, fremde Org/Client → Item 404, ohne ci:write → 403); e2e-real B01 erweitert um ein vorher manuell bearbeitetes CI.

<a id="wp-182"></a>
#### WP-182 – Generische Job-API /api/v1/jobs und Jobmetriken

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** API-09, JOB-03
- **Anforderung/Nutzerziel:** Generische Job-API mit Liste, Detail, Cancel, Revert (E-25) und Ergebnis sowie Jobmetriken (Dauer, Fehler, Rückstau, Alter des ältesten queued Jobs).
- **Implementierung:** backend/internal/jobs/handler.go:52-58 Routen, :69-89 List, :99-121 Get, :125-143 Result (nur orgOf, kein Client-Scope), :147-157 mayControl (Ersteller oder Kind-Permission, scope-blind), :161-183 Cancel, :187-213 Revert; service.go:200-209 Cancel = reine Statustransition; metrics.go:44-98 Backlog-Collector, :101-111 RegisterMetrics; server/router.go:332-356 Verdrahtung, :599 RegisterMetrics; RLS job_isolation/job_system_select nur organization_id; route_permissions.txt:28,174-176,426; api/openapi.yaml:1904-2009.
- **Test(s):** PASS (Baseline + final): internal/jobs TestJobAPIListGetResult, TestJobAPICancel, TestJobAPIRevert, TestJobMetrics (Memory-Repo/Stub-Reverter), TestJobsAgainstPostgres, TestTrackerMirrorsSpecialisedJobs, TestJobRetryTimeoutAndCancel; tenant/rls TestChannelMatrix (GET /api/v1/jobs prüft nur HTTP 200). Probes (audit_db_21): f6 Revert (Fremder 403, Ersteller 200, zweiter Revert 409 'not found'); f2 HTTP DELETE auf laufendem ci.bulk-Job → 200 'cancelled', Arbeit läuft weiter, Ergebnis verloren; f8 TestVerifyProbeJobClientScope (Client-2 liest 100/100 Client-1-CI-IDs aus /result); f9 TestVerifyProbeBacklogMetricsPG (reticora_job_backlog=2, oldest_queued_age_seconds>0). Real-Stack bulk-jobs-export.spec.ts J01 PASS (Liste, Statusfilter, Ergebnis, Cross-Org 404, Cancel/Revert eines fertigen Jobs 409, Dauerzähler bedingt) und E01 PASS. Defekttest-Welle: TestAuditT7BulkJobCancelStopsWork und TestAuditT7JobAPIHonoursClientScope FAILS_AS_EXPECTED (SP/verify-tests-t357/results.tsv).
- **Soll/Ist:** Soll: GET, DELETE (Cancel), Revert, Ergebnis und Metriken im Scope des Aufrufers. Ist: alle Endpunkte vorhanden, Metriken auf PG korrekt; Cancel eines laufenden Jobs ist nur ein Statuswechsel (Arbeit läuft weiter, Ergebnis verworfen); Jobdaten über Clients derselben Org sichtbar.
- **Evidenz:** Defekttests und Probe f2 reproduzieren den wirkungslosen Cancel über das echte HTTP-DELETE; Defekttest und Probe f8 reproduzieren die Cross-Client-Offenlegung; übrige Operationen mit ausgeführter Evidenz (Unit, PG-Probes f6/f9, Real-Stack J01).
- **Abweichung:** JOB-03-Cancel für laufende Jobs beobachtet wirkungslos (L-T7-ui-06, P2); Job-API ignoriert Client-Scope (L-T7-ui-07, P2, TEN-09-Kanal 'job'); revertierter Job bleibt succeeded (L-T7-ui-28, P3).
- **Prüfebenen:** UX: N/A – API-WP (Jobcenter-Cancel erbt den Defekt, siehe WP-197). API: FEHLER – DELETE auf laufendem Job meldet Erfolg ohne Stopp; Jobs/Ergebnisse ignorieren Client-Scope; Rechte und 409 sonst korrekt. Daten: FEHLER – Client-2-Principal liest Parameter/Ergebnisse von Client 1; revertierter Job bleibt succeeded. E2E: LÜCKE – J01 PASS; kein E2E für Abbruch laufender Jobs. Robustheit: FEHLER – nach Cancel scheitert Succeed, Ergebnis verloren; mayControl ignoriert Scope.
- **Abhängigkeiten:** WP-181 (ci.bulk-Runner), WP-183 (RunDetached), WP-130 (Queue), WP-197 (Jobcenter), WP-208 (Kanalmatrix).
- **Defekte:** F-162 (P2) Job-Cancel stoppt laufende Arbeit nicht (ci.bulk, Export): Status 'cancelled', aber alle Änderungen werden ausgeführt bzw. der Export fertiggestellt, Teilbericht geht verloren, F-163 (P2) Job-API ignoriert den Client-Scope: ein client-beschränkter Principal listet und liest Jobs und Ergebnisse anderer Clients, F-329 (P3) Revertierter Job bleibt 'succeeded'; zweiter Revert liefert 409 mit Detail 'not found'
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Cancel erreicht die Runner bzw. 409 für laufende nicht unterbrechbare Arten (TestAuditT7BulkJobCancelStopsWork PASS); Client-Scope-Filter (TestAuditT7JobAPIHonoursClientScope PASS, TestChannelMatrix mit Inhaltsprüfung des Job-Kanals); Revert- und Metrik-Probes f6/f9 als Repo-Tests.

<a id="wp-183"></a>
#### WP-183 – Lange Vorgänge als Jobs: Review-Merge, State-Snapshot

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** JOB-04
- **Anforderung/Nutzerziel:** Review-Merge und State-Snapshot laufen als Jobs; der Snapshot liefert den rekonstruierten Zustand im Umfang nach JOB-04 (E-25).
- **Implementierung:** backend/internal/reviews/service.go:36 MergeJobKind, :172-192 runEffect (Track + Mirror running/failed/succeeded um den synchronen Effekt im Request); discovery/review.go:48-77 (WithJobs, WithReviewJobs); server/router.go:384-387 (vor Routenregistrierung :405); jobs/service.go:328-353 RunDetached (In-Process-Goroutine); history/handler.go:55-108 CreateSnapshot (202 + Location, TrailUpTo → Replay); history/pg_repository.go:150-171 TrailUpTo liest nur entity_change, :37-60 Record ohne produktiven Aufrufer, kein Trigger schreibt entity_change.
- **Test(s):** PASS (Baseline + final): internal/reviews TestLongOperationsRunAsJobsAgainstPostgres – nutzt einen Stub-Merge-Effekt; Snapshot-Teil fügt ci_change ein, prüft aber nur result.entity_id (merge_job_integration_test.go:121-123). Probes (audit_db_21): f7 TestAuditProbeSnapshotContent (Umbenennung via ci.Service.Update Manual → ci_change=1, entity_change=0; Snapshot-Job succeeded mit fields={} jetzt, bei T-2h, für Client-2 und für unbekannte ID); f3 TestAuditProbeReviewMergeRetry (Retry erfolgreich, Job bleibt failed 'transient failure'). psql: SELECT count(*) FROM entity_change → 0. Defekttest-Welle: kein Test (nicht kodiert).
- **Soll/Ist:** Soll: Merge und Snapshot laufen als Jobs mit aussagekräftigem Ergebnis. Ist: beide erzeugen Jobs; der Merge läuft weiter synchron mit gespiegeltem Job, dessen Status nach Retry falsch sein kann; der Snapshot-Job liefert für jedes CI einen leeren Zustand, weil die Replay-Quelle nie befüllt wird.
- **Evidenz:** Jobanlage auf PG belegt (Test, Probe f7); Probes f3/f7 zeigen falschen Jobstatus nach Retry und leere Snapshot-Ergebnisse; der bestehende Test verdeckt das leere Ergebnis.
- **Abweichung:** Snapshot liefert immer leeren Zustand (L-T7-ui-08, P2) – verletzt den Teil 'Snapshot-Umfang'; da JOB-04/E-25 den Inhalt nicht weiter definieren (Replay-Inhalt AUD-08 [P2]) und die Job-Kriterien erfüllt sind, nur ein Teil betroffen ⇒ TEILWEISE statt FEHLGESCHLAGEN. Merge nur gespiegelt (L-T7-ui-30, P3); Job nach Merge-Retry failed (L-T7-ui-29, P3).
- **Prüfebenen:** UX: N/A – keine Snapshot-UI, Review-Inbox unverändert. API: LÜCKE – Snapshot unbekannter/fremder Entität 'succeeded' mit leerem Zustand statt 404. Daten: FEHLER – Snapshot-Inhalt leer (falsche Datenquelle), Jobstatus nach Merge-Retry falsch. E2E: LÜCKE – kein Real-Stack-Lauf für Merge-Job oder Snapshot. Robustheit: LÜCKE – RunDetached in-process ohne Cancel, Verlust bei Neustart bis zum Timeout-Reaper.
- **Abhängigkeiten:** WP-182 (Job-API), WP-173 (CI-Merge), WP-171/AUD-08 (Historien-Replay, Quelltabelle).
- **Defekte:** F-164 (P2) State-Snapshot-Job (und GET /cis/{id}/state) liefert immer einen leeren Zustand, F-330 (P3) Wiederholter Review-Merge lässt seinen Job im Status failed, F-331 (P3) Review-Merge läuft synchron im Request; der Job spiegelt ihn nur
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Snapshot replayt die reale Änderungsspur, Inhaltstest (alter/neuer Name je Zeitpunkt, 404/failed für unbekannte ID, Client-Scope); Retry-Status korrigiert und getestet; Merge-Job-Test über echten Router und Discovery-Effekt; dokumentierte Entscheidung zu synchronem Merge + Mirror.

<a id="wp-184"></a>
#### WP-184 – GraphQL-BFF mit gqlgen unter /bff/graphql, nur Session, Tiefen-/Komplexitätslimit

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** GQL-01, SEC-10
- **Anforderung/Nutzerziel:** GraphQL-BFF mit gqlgen unter /bff/graphql, ausschließlich Session-Auth (keine API-Keys), mit SEC-10-Limits (Tiefe 10, Komplexitätsbudget, 10 s Deadline) und portierten Resolvern inkl. WP-036-Rechteprüfungen.
- **Implementierung:** backend/internal/graphqlbff/graphqlbff.go:34-42 (Path /bff/graphql, MaxDepth 10, MaxComplexity 5000, Deadline 10 s), :99-130 NewHandler (nur POST, FixedComplexityLimit, depthLimit), :152-154 Route, :159-176 ServeHTTP (ohne Principal 401, Type≠user 403, WithTimeout); middleware/auth.go:147 (X-API-Key → api_key), :168 (JWT → user); resolver.go:85-181 (cis, ci, relationships, currentUser mit gleichen Rechten/Entitlements wie e9ed81a^); /api/v1/graphql entfernt; frontend/src/api/client.ts:425-446 bffQuery ohne Aufrufer.
- **Test(s):** PASS (Baseline + final): internal/graphqlbff TestOnlySessionsReachTheBFF (graphqlbff_test.go:98-112: 401 ohne Principal, 403 api_key, 200 Session), TestLimits (:114-143: Tiefe 11 abgewiesen, Tiefe 6 akzeptiert, 3×100-CI-Seiten über Budget, Deadline mit blockierendem Repo), TestResolversCheckTheirReadPermission, TestResolversCheckEntitlement, TestResponseContainsOnlySelectedFields, TestMutationsAreRefused; tenant/rls TestChannelMatrix (graphql-Kanal über server.NewRouter auf PG, Client/Site/Team/Fremd-Org); middleware TestAPIKeyAuthenticationPopulatesPrincipal. Real-Stack tenant-isolation.spec.ts T04 PASS (echter Keycloak-Session-JWT durch die echte Middleware; B sieht in 9 Queries nichts von A).
- **Soll/Ist:** Soll: gqlgen-BFF unter /bff/graphql, nur Session, SEC-10-Limits, WP-036-Prüfungen. Ist: wie spezifiziert umgesetzt und durch Unit-Tests auf echtem BFF-Code, Full-Router-PG-Kanalmatrix und Real-Stack-Sessiontest belegt; die UI nutzt den BFF noch nicht.
- **Evidenz:** Alle Kriterien durch ausgeführte Tests gegen echten BFF-Code, den Full-Router auf PostgreSQL (TestChannelMatrix) und den Real-Stack-Sessionpfad (T04) belegt; Verifier konnte das Urteil nicht widerlegen.
- **Abweichung:** P3 L-T7-ui-31 (BFF-Client ohne UI-Verwender) außerhalb der Kriterien.
- **Prüfebenen:** UX: N/A – kein UI-Verwender. API: OK – 401/403, nur POST, Limits, fail-closed. Daten: OK – RLS-gescopte Repositories, TestChannelMatrix und T04 ohne Cross-Client-/Cross-Org-Leck. E2E: OK – T04 mit echtem Session-JWT; API-Key → 403 nur auf Handler-/Middleware-Ebene belegt. Robustheit: OK – Negativtests für Tiefe, Komplexität und Deadline; exakte Grenzen (10/11, 5000/5001) ungetestet.
- **Abhängigkeiten:** WP-036 (gql-authz); WP-185 baut darauf auf.
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-332 (P3) BFF-Client bffQuery hat keinen Verwender in der UI
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Bereits BESTANDEN. Bestätigend empfohlen: Full-Router-Auth-Test inkl. X-API-Key → 403, GET → 404/405; Grenzwerttests Tiefe 10/11 und Komplexität 5000/5001.

<a id="wp-185"></a>
#### WP-185 – Fehlende Resolver mit Dataloader

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** GQL-02
- **Anforderung/Nutzerziel:** Die GQL-02-Resolver topology, rackLayout, searchEverything, reviewInbox und jobs sind mit Dataloader (ohne N+1) und nur lesend verfügbar.
- **Implementierung:** backend/internal/graphqlbff/schema.graphqls:10-29 (nur Query), :164-172 Job-Typ (ohne params/result); resolver.go:246-272 Topology, :274-300 RackLayout, :302-326 SearchEverything (Leserecht je Treffer), :328-349 ReviewInbox, :351-369 Jobs, :218-244 prime/loadCI; dataloader.go:23-127 (Request-Cache, Batch über ci.BatchReader.GetByIDs); graphqlbff.go:110 Topologie-Komplexität child*maxPage; server/report_adapters.go:235-241; router.go:441-442.
- **Test(s):** PASS (Baseline + final): internal/graphqlbff TestNestedCIsUseTheDataloader, TestNewQueries, TestNewQueriesCheckTheirPermission, TestMutationsAreRefused (Memory-Repos). Verifier-Probe f5 TestAuditProbeBFFAgainstPostgres (audit_db_21, PG-Repos mit N+1-Zählern, Client-2-Negative): relationships/topology/rackLayout/reviewInbox je 1 Batch-Read, searchEverything total=1; Client 2 erhält [], 'not found', null, total 0, []. Memory-Probe TestVerifyProbeTopologyComplexity: 1500 Knoten ohne Komplexitätsfehler. Real-Stack T04 PASS.
- **Soll/Ist:** Soll: GQL-02-Queries mit Dataloader, nur lesend. Ist: umgesetzt und auf Fakes, auf PostgreSQL (Probe inkl. Client-Scope-Negativen) und auf dem Real-Stack (Org-Isolation) belegt; Topologie-Komplexität wird zu niedrig angesetzt.
- **Evidenz:** Repo-Unit-Tests grün; Probe f5 führt alle Queries gegen PG-Repositories mit N+1-Zählern und Client-2-Negativen aus; Real-Stack T04 grün; Widerlegungsversuch des Verifiers erfolglos.
- **Abweichung:** P3 L-T7-ui-32 (Topologie-Komplexität unterschätzt) außerhalb der Kriterien; PG-Nachweis liegt nur als nicht committete Verifier-Probe vor.
- **Prüfebenen:** UX: N/A – kein UI-Verwender. API: OK – Permission/Entitlement je Query, 422 → BAD_USER_INPUT, null für unsichtbares Rack. Daten: OK – RLS-gescopte Repos, Client-2-Negative auf PG (f5); jobs-Query org-weit nur mit Metadaten. E2E: OK – T04 (Isolation) PASS; keine UI-Nutzung. Robustheit: LÜCKE – Topologie-Komplexität unterschätzt (P3), nur die 10-s-Deadline begrenzt.
- **Abhängigkeiten:** WP-184, WP-182, WP-180.
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-333 (P3) Topology-Komplexität des BFF unterschätzt große Graphen
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Bereits BESTANDEN. Empfohlen: Probe f5 (inkl. Client-2-Negativen) und Komplexitätstest als Repo-Tests committen.

<a id="wp-186"></a>
#### WP-186 – Suche: Locations und Kontakte als Treffer

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** SRC-01
- **Anforderung/Nutzerziel:** Locations und Kontakte werden laufend indexiert und als Suchtreffer nur mit location:read bzw. contact:read und im Scope des Aufrufers geliefert.
- **Implementierung:** backend/migrations/000132_search_entity_kinds.up.sql:11-57 (Trigger location_search_index/contact_search_index für INSERT/UPDATE/DELETE + Backfill; url '/locations' und '/contacts' :39-40); search/model.go:68-77 ReadPermission; search/pg_repository.go:91-105 hitVisible/contactTokenMatch; search/handler.go:37-50, :75-97 filterAllowed (fail-closed). UI CommandPalette.tsx:338-345 (window.location.assign(hit.url)); App.tsx ohne /contacts-Route (Catch-all :360 → CIListPage).
- **Test(s):** PASS (Baseline + final): internal/search TestLocationsAndContactsAreHitsAgainstPostgres (Treffer nach Insert, Rename, Delete; kein Treffer für anderen Client), TestLocationsAndContactsAreSearchable, TestSearchHitsRespectClientSiteAndReadPermission; internal/contact TestContactsThroughTheVault; tenant/rls TestChannelMatrix (search); Migration up/down/up OK. Probe f4 TestAuditProbeLocationContactHits (audit_db_21, PII-Vault, HTTP-Handler): mit/ohne location:read bzw. contact:read Treffer/keine, Client 2 → 0 Treffer, keine Klartext-PII; Post-Filter-nach-LIMIT nicht reproduzierbar. Real-Stack tenant-isolation T01 und T13 PASS (keine Fremdtreffer über /search).
- **Soll/Ist:** Soll: Locations und Kontakte indexiert und mit Leserecht und Scope abfragbar. Ist: erfüllt und auf PG belegt (committete Tests plus Probe mit Permission- und Client-Scope-Negativen); Kontakttreffer führen in der UI auf eine nicht existente Route.
- **Evidenz:** Jeder Kriteriumsteil mit ausgeführter PG-Evidenz inkl. HTTP-Permission-Filter, Client-2-Negativen und Vault-Pfad (Tests + Probe f4).
- **Abweichung:** P3 L-T7-ui-33 (Kontakttreffer → nicht existente Route /contacts) außerhalb des Kriteriums; das WP verspricht keinen UI-Ablauf (alle WP-Dateien Backend).
- **Prüfebenen:** UX: LÜCKE – Kontakttreffer → /contacts (nicht existent), Location-Treffer öffnet nur den Baum. API: OK – Permission-Filter je Treffertyp, Scope via RLS + hitVisible. Daten: OK – Index folgt Insert/Update/Delete, Client/Site abgeleitet, keine Klartext-PII mit Vault. E2E: LÜCKE – Palette-Suche nur gemockt; real nur Negativ-Isolation (T01, T13). Robustheit: OK – Post-Filter-nach-LIMIT nicht reproduzierbar.
- **Abhängigkeiten:** WP-032 (search-pg), WP-053 (loc-api), WP-242 (PII-Vault).
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-334 (P3) Kontakt-Suchtreffer navigieren auf /contacts, eine nicht existente Route
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Bereits BESTANDEN. Empfohlen: Probe f4 inkl. Client-2-Negativen als Test committen; nach URL-Fix e2e-real Suche → Navigation.

<a id="wp-187"></a>
#### WP-187 – Filter-AST AND/OR/NOT (Server), Grundlage für Sichten

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** SRC-03
- **Anforderung/Nutzerziel:** Serverseitiger Filter-AST mit AND/OR/NOT und Vergleichsoperatoren (inkl. Attribut- und Asset-Feldern), validiert und per Whitelist in SQL übersetzt; Grundlage für gespeicherte Sichten.
- **Implementierung:** backend/internal/savedview/filter.go:208-216 Expr, :248-271 astFields-Whitelist, :295-340 Validate, :342-364 fieldKind (number nach JSON-Werttyp :347), :366-395 validateComparison (nur Go-Typ, in-Elemente ungeprüft), :397-438 CompileSQL, :459-487 comparison ((attributes->>key)::numeric für jede Zeile), :491-498 likePattern; query.go:32-90 (WithRequestTenant/RLS, ORDER BY name); handler.go:191-232 (POST /api/v1/search/query; 422 :216-220; jeder Engine-Fehler → 400 mit err.Error() :224).
- **Test(s):** PASS (Baseline + final): internal/savedview TestFilterASTCompilesAndOrNot, TestFilterASTValidation, TestFilterASTAgainstPostgres (nur homogene numerische ram_gb), TestStructuredSearchScopeDepthAndPermission (flache Filter), TestSavedViewsVIE01AgainstPostgres. Real-Stack bulk-jobs-export.spec.ts V01 PASS (422 filter_spec.where). Verifier-Probe zz_verify_b24_sv_test.go (audit_db_22): attributes.ram_gb gte 16 bei Werten 64/32/'16GB' → 400 22P02; client_id 'not-a-uuid' → 400 22P02; created_at 2020 → 400 22007; Client-1-OR-AST → total 1; Feld password → 422. Defekttest-Welle: kein Test (nicht kodiert).
- **Soll/Ist:** Soll: serverseitig validierter AST, Übersetzung über Whitelist. Ist: Struktur und Whitelist funktionieren und sind auf PG belegt; Wertvalidierung fehlt; numerische Vergleiche auf dynamischen Attributen scheitern für die ganze Organisation, sobald ein CI unter dem Schlüssel einen nicht-numerischen Wert speichert.
- **Evidenz:** Probe auf audit_db_22 reproduziert 400 'invalid input syntax for type numeric: 16GB (SQLSTATE 22P02)' sowie 22P02/22007 für ungültige Werte; Struktur-/Whitelist-/Scope-Teile durch PG-Tests und V01 belegt.
- **Abweichung:** L-T7-ui-09 (P2), L-T7-ui-34 (P2)
- **Prüfebenen:** UX: N/A – kein AST-UI in Phase 1 (WP-236). API: LÜCKE – Strukturfehler 422 korrekt, Wert-/Castfehler 400 mit PostgreSQL-Text und SQLSTATE. Daten: OK – WithRequestTenant/RLS (Client-1-Scope total 1), gelöschte CIs ausgeschlossen. E2E: LÜCKE – real nur der 422-Pfad (V01). Robustheit: FEHLER – numerischer Attributvergleich bricht auf heterogenen Daten die ganze Abfrage ab, Ergebnis von der PG-Auswertungsreihenfolge abhängig.
- **Abhängigkeiten:** WP-165, WP-034; Grundlage für WP-188 (filter_spec.where) und WP-236.
- **Defekte:** F-165 (P2) Numerischer AST-Vergleich auf einem dynamischen Attribut bricht die ganze Abfrage ab, sobald ein sichtbares CI einen nicht-numerischen Wert unter dem Schlüssel speichert, F-179 (P2) Filter-AST validiert Wertformate nicht; DB-Fehlertext mit SQLSTATE wird in 400-Antworten offengelegt
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Abgesicherte numerische Casts und Wertvalidierung je Feldart (→ 422 ohne SQL-Text); PG-Test mit gemischten Typen, HTTP-Negativvertrag (kein 'SQLSTATE' im Body), committeter AST-Client-Scope-Test.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – abweichung: „L-T7-ui-09 (P2), L-T7-ui-34 (P3)“ → „L-T7-ui-09 (P2), L-T7-ui-34 (P2)“. Folgt aus der Prioritätskorrektur von L-T7-ui-34 (siehe defekt_korrekturen). Status TEILWEISE BESTANDEN und WP-Priorität P2 bleiben gleich.

<a id="wp-188"></a>
#### WP-188 – Gespeicherte Sichten: sort, columns, shared_with_roles, Zugriff privat

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** VIE-01
- **Anforderung/Nutzerziel:** Gespeicherte Sichten mit VIE-01-Feldern (sort, columns, is_shared, shared_with_roles, eigene Tabelle); fremde private Sicht per GET → 404; Freigabe an Rollen.
- **Implementierung:** backend/migrations/000133_saved_view_vie01.up/down.sql; savedview/model.go:9-41 (UpsertRequest.Shared bool mit omitempty :40); view_rules.go:15-62 (nur UUID-Format der Rollen); pg_repository.go:24-26 visibleTo, :67-86 GetByID, :112-169 Update (immer 'shared = $n' :153; WHERE shared OR owner :157), :171-187 Delete; handler.go:94-185; frontend SavedViewsPage.tsx:45-51 (sendet nur name/entity_kind/filter_spec), :96-113; api/cmdb.ts:689-711 (savedViewApi.update unbenutzt).
- **Test(s):** PASS (Baseline + final): internal/savedview TestSavedViewsVIE01AgainstPostgres, TestSavedViewScope; Vitest SavedViewsPage (4, Stubs). Real-Stack bulk-jobs-export.spec.ts V01 PASS (UI speichern/Reload/erneut ausführen/löschen, persistierter Datensatz, Cross-Tenant GET/PATCH/DELETE 404). Probe zz_verify_b24_sv_test.go (audit_db_22): PATCH nur name → shared=false (DB), anderes Mitglied 404; Nicht-Owner PATCH/DELETE org-geteilt 200/204, DB rows 0; Rollenmitglied GET 200/DELETE 404; unbekannte Rollen-UUID → 201. Defekttest-Welle: Regressionstest TestAuditT7SavedViewEditDeleteRights (backend/internal/savedview/audit_t7_savedview_rights_test.go:91) PASS im finalen Lauf (SP/final/test-db.log, 1,68 s) und im untagged-Paketlauf (mechanische Verifikation ok) – belegt Edit-/Delete-Rechte nach dokumentiertem Vertrag; der P2-Defekt 'PATCH ohne shared' ist nicht kodiert.
- **Soll/Ist:** Soll: VIE-01-Felder, private Sichten geschützt, Rollenfreigabe. Ist: Felder, 404-Regel, Rollen-Lesezugriff und der reale UI-Speichern/Löschen-Ablauf sind belegt; ein PATCH nur mit Namen hebt die Freigabe still auf; Edit-Rechte auf geteilte Sichten nur dokumentiert, nicht entschieden.
- **Evidenz:** Probe reproduziert DB-Zustand shared=false nach Name-PATCH; Felder und 404 durch PG-Test und V01 belegt; Regressionstest für Edit-/Delete-Rechte grün.
- **Abweichung:** Kriterium 'Rollenfreigabe' nur teilweise: PATCH ohne shared hebt die Freigabe still auf (L-T7-ui-10, P2); P3: L-T7-ui-35 (Edit-Rechte org-geteilter Sichten, Entscheidung offen), L-T7-ui-36 (sort/columns wirkungslos), L-T7-ui-37 (Rollen-IDs ungeprüft).
- **Prüfebenen:** UX: LÜCKE – UI zeigt nur Badges, kann sort/columns/Freigabe nicht setzen; Löschknopf für fremde org-geteilte Sichten. API: FEHLER – PATCH setzt shared zurück; Edit/Delete für jeden saved_view:write-Inhaber (dokumentiert). Daten: FEHLER – stille Änderung shared=false; Rollen-IDs ungeprüft gespeichert. E2E: OK – V01 belegt Speichern/Reload/Löschen und Cross-Tenant-Isolation (Rollenfreigabe nicht E2E). Robustheit: LÜCKE – kein Negativtest für PATCH-Teilupdates.
- **Abhängigkeiten:** WP-187 (where-AST); VIE-02 [P2] baut darauf auf.
- **Defekte:** F-166 (P2) PATCH ohne 'shared' hebt die Org-Freigabe einer gespeicherten Sicht still auf, F-335 (P3) Jeder Inhaber von saved_view:write kann fremde org-geteilte Sichten umbenennen, entsperren oder löschen, F-336 (P3) sort und columns gespeicherter Sichten werden persistiert, aber nicht angewendet; die UI kann sie und die Freigabe nicht setzen, F-337 (P3) shared_with_roles akzeptiert Rollen-IDs, die in der Organisation nicht existieren
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** PATCH mit *bool für Shared und PG-Partial-Update-Test (shared bleibt true, anderes Mitglied GET 200); Entscheidung zu Edit-/Delete-Rechten samt angepasstem TestAuditT7SavedViewEditDeleteRights; Rollen-ID-Prüfung (422); e2e-real Rollenfreigabe.

<a id="wp-189"></a>
#### WP-189 – Export-Format: CSV mit BOM/Semikolon, Feldmanifest, Pfad exports

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** EXP-01
- **Anforderung/Nutzerziel:** Export über POST /exports als Job (202), CSV UTF-8 mit BOM und Semikolon, Presign-Download 15 min, nur sichtbare Daten, Feldmanifest der ausgelassenen Felder; DATEV nicht Bestandteil.
- **Implementierung:** backend/internal/export/export.go:160-175 (csv.Comma ';'), :177-189 (BOM + Header), :191-206 row (Werte unverändert), :270 utf8BOM, :272-275 csvColumns, :278-331 Manifest/ManifestFor, :78 X-Export-Omitted-Fields; job_handler.go:18-23,45-51,155 (/api/v1/exports, Legacy /export/jobs); server/authz.go:270-271; route_permissions.txt:147-148,401; platform/blob/blob.go:29 (Presign 15 min); frontend ExportPage.tsx:106-113.
- **Test(s):** PASS (Baseline + final): internal/export TestCSVFormatAndManifest, TestJobRendersCorrectCSV, TestJobResponseCarriesManifest, TestExportJobRunsWithCreatorScope (PG), TestExportScope, TestJobLifecycleCompletesWithSignedURL, TestListJobsTenantIsolation, TestGetJobNotFoundOtherTenant; Vitest ExportPage (2). Real-Stack bulk-jobs-export.spec.ts E01 PASS (SP/work/verify-bulk-jobs-export/final.log: 202, completed, X-Amz-Expires=900, BOM EF BB BF, Header = manifest.fields, Zeilen = API-Daten inkl. 'Müller; Rack 7', UI 'N Felder nicht enthalten', Cross-Tenant 404), N01 PASS, ci-lifecycle L10 PASS, Onboarding-Suite #6 PASS. Probe zz_verify_b24_csv_test.go: Formelwerte unverändert exportiert.
- **Soll/Ist:** Soll: CSV mit BOM und Semikolon, Manifest, Ressource exports. Ist: vollständig umgesetzt und Ende-zu-Ende auf dem Real-Stack belegt; nur die Härtung gegen CSV-Formel-Injection fehlt.
- **Evidenz:** E01/L10 belegen UI → API → Worker → MinIO → Download → Dateiinhalt; Unit- und PG-Tests belegen Format, Manifest und Ersteller-Scope.
- **Abweichung:** P3 L-T7-ui-38 (CSV-Injection) außerhalb von EXP-01. Geprüft, aber nicht zugeordnet: Real-Stack-Defekt L-T7-ui-23 (Export-Buttons der CI-Liste, 401) – Ursache CIListPage.tsx:119/122 (keine WP-189-Datei), EXP-01 über /exports durch E01/L10 erfüllt; daher kein WP-189-Kriterium verletzt.
- **Prüfebenen:** UX: OK – E01 bedient die Exportseite real (Format, Erzeugen, Jobzeile mit Zeilenzahl, Hinweis ausgelassener Felder, Download). API: OK – 202-Job, GET /exports/{id} mit Manifest, Presign 900 s, export:run/job:read, Tenant-404. Daten: OK – Worker im Ersteller-Scope (PG), Datei = API-Daten, keine Fremddaten. E2E: OK – E01 und L10 Ende-zu-Ende. Robustheit: LÜCKE – keine CSV-Formel-Neutralisierung (P3); Quoting von ';' und Anführungszeichen belegt.
- **Abhängigkeiten:** WP-035 (Export-Scope), WP-168 (Pfade), Blob/MinIO-Konfiguration.
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-338 (P3) CSV-Export neutralisiert keine Tabellenkalkulationsformeln (CSV-Injection)
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Bereits BESTANDEN. Empfohlen: CSV-Injection-Neutralisierung mit Unittest; e2e-real Exportberechtigung (Viewer ohne export:run, client-beschränkter Export).

<a id="wp-190"></a>
#### WP-190 – CI-Metriken /cis/{id}/metrics mit Detail-Linie; ci_id in Uploads

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** MON-02
- **Anforderung/Nutzerziel:** Per-CI-Metriken über GET /cis/{id}/metrics mit den MON-02-Basismetriken (Sweep, SNMP-Interface-Zähler, UPS/PDU), Collector-Uploads mit ci_id-Zuordnung und Metriklinie im CI-Detail.
- **Implementierung:** backend/internal/monitoring/monitoring.go:478 (GET /api/v1/cis/{id}/metrics), :507-550 IngestMetrics + assignCIs, :800-822, :836-878 CIMetrics (keine CI-Sichtbarkeitsprüfung); pg_store.go:187 metricWhere (ci-RLS-Prädikat), :230-247 MetricNames (ohne Prädikat); server/report_adapters.go:245-257 ciAddressResolver; collector/collectorcmd/collectorcmd.go:459, :464-466 + 1588-1622 (nur UPS-MIB), :1068-1115 uploadMetricSamples/postMetricsPayload (HTTP-POST ohne Authorization, nur X-Organization-ID/X-Collector-ID), :1625-1632 uploadSamples (nur Log); edge.go:22-23 (nur .ingest/.heartbeat); middleware/auth.go:295-331 (Bearer für jeden /api/-Pfad); frontend CIDetailSections.tsx:500-559, CIDetailPage.tsx:237.
- **Test(s):** PASS (Baseline + final): internal/monitoring TestIngestAssignsCollectorSamplesByTarget, TestIngestWithoutResolverDropsSamplesWithoutCI (Memory-Store, Fake-Resolver); collector TestReachabilitySamplesReportLostHosts, TestSchedulerRunsSweepAndPollsSeparately (unauthentisierter httptest-Server); Vitest CIMetricsSection (3, gemockt). Kein PG-Test der Route, kein Real-Stack-Test. Probe zz_verify_b24_mon_test.go (audit_db_22): eigenes CI 200 mit Punkten; Client-2-CI als Client 1 → 200 mit Serie sweep_reachable; nicht existente UUID → 200 []; Ingest org-weit 202 ingested 3/unassigned 1. Real-Stack-curl → 401 'missing bearer token'; realer Collector-Log /tmp/reticora-e2e-discovery-SuPITb/collector.log: HTTP 401, Edge 204. Defekttest-Welle: TestAuditT7MetricSamplesReachTheServerAuthenticated FAILS_AS_EXPECTED ('accepted=0 refused(401)=1').
- **Soll/Ist:** Soll: Per-CI-Endpunkt mit Basismetriken, CI-zugeordnete Collector-Uploads, Linie im Detail. Ist: Endpunkt, serverseitige Zuordnung und UI-Linie existieren, aber die Collector-Uploads werden mit 401 abgewiesen – in einer realen Installation erreicht nie eine Basismetrik ein CI; zusätzlich Scope-Leck der Metriknamen und fehlende PDU-Werte.
- **Evidenz:** Defekttest, Real-Stack-curl und realer Collector-Log belegen den 401; Probe auf audit_db_22 belegt das Scope-Leck; grep belegt fehlende PDU-OIDs.
- **Abweichung:** Kriterium 'Collector-Uploads mit ci_id' beobachtet verletzt (L-T7-ui-02, P1); Endpunkt mit Scope-Leck/200 für unsichtbare CIs (L-T7-ui-11, P2); PDU-Werte fehlen (L-T7-ui-12, P2); Interface-Zähler nur ifIndex 1, opt-in (L-T7-ui-39, P3); Detail-Linie nur gemockt.
- **Prüfebenen:** UX: LÜCKE – Linie nur auf Klick, rohe Seriennamen ohne Einheit; real praktisch immer leer. API: FEHLER – Collector-Upload 401; /cis/{id}/metrics leakt Namen über Client-Scope und antwortet 200 für unsichtbare/nicht existente CIs. Daten: FEHLER – in einer realen Installation werden keine Collector-Samples gespeichert. E2E: FEHLER – Kette Collector → Server unterbrochen (curl 401, Collector-Log). Robustheit: LÜCKE – Upload-Fehler nur geloggt, kein Spool/Retry.
- **Abhängigkeiten:** WP-040 (Metrik-RLS-Views), WP-150 (Power-Plugin), DIS-10 SNMP-Poll, COL-08 NATS-Edge (WP-144/145), WP-214 (FeatureMonitoring).
- **Defekte:** F-046 (P1) Collector-Metrik-Uploads (Sweep, UPS, SNMP-Poll) werden ohne Credential gesendet, mit 401 abgewiesen und erreichen den Server nie, F-167 (P2) GET /cis/{id}/metrics zeigt Metriknamen von CIs außerhalb des Client-Scopes und antwortet 200 für unsichtbare oder nicht existente CIs, F-168 (P2) PDU-Messwerte werden nicht erhoben (nur UPS-MIB), F-339 (P3) Standard-Interface-Zähler nur für ifIndex 1 und nur mit RETICORA_METRICS_INTERVAL
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT7MetricSamplesReachTheServerAuthenticated PASS; e2e-real Collector → Server → CI-Detail (metric_sample mit ci_id, Linie sichtbar); PG-Routentest mit 404 für unsichtbare/nicht existente CIs; PDU-Werte (oder dokumentierte Entscheidung) mit Unittest.

<a id="wp-191"></a>
#### WP-191 – UI-Basis: Scope-Bedienung (Org/Client/Site) im Layout

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** UI-01, UI-02, UI-03, UI-04, UI-05, UI-07, UI-08, UI-09, UI-10
- **Anforderung/Nutzerziel:** Ein Scope-Umschalter (Org/Client/Site) im Layout; Listen und Detailseiten berücksichtigen den gewählten Scope; nur zugängliche Scopes sind wählbar; keine Funktionen über die genannten Bausteine hinaus (E-02).
- **Implementierung:** frontend/src/components/ScopeSwitcher.tsx (Org aus /org, Clients via clientApi.list limit 200 api/cmdb.ts:311, Sites limit 200, verwirft unbekannte gespeicherte Auswahl); stores/scope.ts (zustand + localStorage 'reticora-scope'); api/client.ts:364-371 SCOPED_LISTS (/cis, /topology, /assets, /contacts, /sites), :373-387 withScope (nur GET, Aufruferparameter gewinnen), :389; App.tsx:227. Backend liest client_id/site_id: ci/handler.go:98-99, topology.go:133-134, asset/handler.go:114, contact/handler.go:50, tenantapi/handler.go:275.
- **Test(s):** PASS: Vitest ScopeSwitcher.test.tsx 5/5 (fetch-Stubs; kodiert withScope('/cis/abc') unverändert); internal/tenantapi TestTenantRepositoryClientScope (PG, Baseline + final). Kein Spec in frontend/e2e oder frontend/e2e-real nutzt den Umschalter (grep 'Alle Kunden', 'Arbeitsbereich', 'reticora-scope' ohne Treffer). Real-Stack-Specs mit WP-191-Zuordnung: tenant-isolation T10 PASS (UI zeigt B nichts von A – Mandantentrennung, nicht der Umschalter), ci-lifecycle L09 PASS, L07 FAIL (Audit-Viewer), L10b FAIL (Export-Buttons), roles-authz Test 12 FAIL (CI-Formular 400), bulk-jobs-export W01 FAIL (Webhooks-Seite).
- **Soll/Ist:** Soll: Scope-Umschalter, Listen und Detailseiten folgen dem Scope, nur zugängliche Scopes. Ist: Umschalter und Listeneinschränkung in Unit-Tests belegt; Detailseiten ignorieren den Scope bewusst; keine E2E-Evidenz.
- **Evidenz:** results-frontend.json 5/5 ScopeSwitcher.test.tsx; TestTenantRepositoryClientScope PASS; client.ts:364-387; grep frontend/e2e und e2e-real ohne Umschalter-Nutzung.
- **Abweichung:** Kriterium 'Detailseiten berücksichtigen den Scope' nicht erfüllt (L-T7-ui-13, P2, Designentscheidung); Listen nur Vitest-belegt, mehrere Listen ungescoped; P3 L-T7-ui-40 (Hypothese). Zugeordnete UI-01..10-Defekte außerhalb der WP-191-Kriterien (Scope-Bedienung): L-T7-ui-23, L-T7-ui-24 (je P2) sowie ext D4 (P1), Audit-Seite (P2), Webhooks-Seite (P2) – sie bestimmen die WP-Priorität nicht.
- **Prüfebenen:** UX: LÜCKE – kein Hinweis auf aktiven Scope in Seiten/Details; 200er-Grenze der Client-/Site-Listen. API: OK – Listen-Handler akzeptieren client_id/site_id; /clients und /sites RLS-beschränkt (PG). Daten: N/A – Auswahl je Browser in localStorage (by design). E2E: LÜCKE – keinerlei Playwright-Abdeckung (Mock oder real) des Umschalters. Robustheit: LÜCKE – > 200 Clients verwerfen eine gültige Auswahl (Hypothese).
- **Abhängigkeiten:** WP-065/WP-109 (Backend-RLS Client/Site-Scope); E-02 offen (v2-Texte UI-01..10).
- **Defekte:** F-077 (P2) CI-Listen-Export nutzt deprecated /api/v1/export/cis als Link → 401 im Browser, F-078 (P2) Audit-Seite liest Felder, die die API nicht liefert ('Invalid Date', Objekt '—'), F-169 (P2) Detailseiten ignorieren den gewählten Client-/Site-Scope, F-340 (P3) Client-/Site-Optionen des Scope-Umschalters auf 200 begrenzt; eine gültige gespeicherte Auswahl jenseits davon wird verworfen, extern: D4 Creating a CI through the UI form fails with 400: the form sends a CI type key ('server') where the API expects the CI type id, extern: Webhooks page silently swallows the server's refusal (egress protection / validation errors): no error message, the form stays as it was
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Detailseitenverhalten entscheiden, umsetzen und testen (Vitest + Playwright); e2e-real: client-beschränkter Nutzer sieht nur zugängliche Clients, Auswahl schränkt /cmdb über die echte API ein (Request mit client_id), Site-Reset bei Clientwechsel, Persistenz nach Reload.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – defekte: „["L-T7-ui-13","L-T7-ui-23","L-T7-ui-24","L-T7-ui-40","ext:Audit page reads fields the API does not return (created_at/entity_type/entity_id vs timestamp/resource_type/resource_id): time shows 'Invalid Date', object column shows '—'","ext:D4 Creating a CI through the UI form fails with 400: the form sends a CI type key ('server') where the API expects the CI type id","ext:Webhooks page silently swallows the server's refusal (egress protection / validation errors): no error message, the form stays as it was"]“ → „["L-T7-ui-13","L-T7-ui-23","L-T7-ui-24","L-T7-ui-40","ext:D4 Creating a CI through the UI form fails with 400: the form sends a CI type key ('server') where the API expects the CI type id","ext:Webhooks page silently swallows the server's refusal (egress protection / validation errors): no error message, the form stays as it was"]“. Doppelter Eintrag. Der ext-Defekt zur Audit-Seite (tenant-isolation, owned=false) ist laut der Synthese selbst identisch mit L-T7-ui-24 ('Identisch mit dem Real-Stack-Defekt aus tenant-isolation.spec.ts (owned=false, WP-168)'), und L-T7-ui-24 steht schon in derselben Liste. Mit 'Audit-Seite (P2)' im Feld abweichung ist damit L-T7-ui-24 gemeint. WP-Status und -Priorität ändern sich nicht.

<a id="wp-192"></a>
#### WP-192 – UI-Basis: Tastaturbedienung und Fokusführung gemeinsamer Bausteine

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** UI-01, UI-02, UI-03, UI-04, UI-05, UI-07, UI-08, UI-09, UI-10
- **Anforderung/Nutzerziel:** Modal, Select, Tabellen und Command-Palette sind vollständig tastaturbedienbar (Fokusfalle, Escape, Pfeiltasten, Fokusrückgabe), mit Tests je Baustein.
- **Implementierung:** frontend/src/components/ui/Modal.tsx:20-40 (Radix Dialog, returnFocusRef in onOpenAutoFocus/onCloseAutoFocus); ui/Select.tsx (natives select, label, aria-invalid/-describedby); CommandPalette.tsx:390-430 (ArrowUp/Down, Home/End, Enter, Escape), :433-496 (Dialog.Root ohne Dialog.Trigger und ohne onCloseAutoFocus); kein gemeinsamer Tabellenbaustein, CIListPage.tsx:269-290 (Zeilen-onClick, Link, Checkbox).
- **Test(s):** PASS: Vitest keyboard.test.tsx (3: Modal Trap/Escape/Restore, Close-Button, Select nur fireEvent.change), CommandPalette.test.tsx (3); e2e-mocked palette.spec.ts (chromium, mobile-chrome; Mock). Kein Tabellen-Tastaturtest. Verifier-Playwright-Probe VP/pw/v-kbd.spec.ts (built dist :4189, gemockte API): Palette Fokus nach Escape = BODY; Tabellenzeilen tabIndex -1 mit je 2 Fokuszielen, ArrowDown ohne Wirkung, Enter auf Link öffnet Detail. Real-Stack ci-lifecycle L09 PASS (Ctrl+K → Detail).
- **Soll/Ist:** Soll: vier Bausteine vollständig tastaturbedienbar, je mit Test. Ist: Modal vollständig; Palette per Tastatur bedienbar, verliert aber beim Schließen den Fokus; Select schwach getestet; Tabellen ohne Pfeiltasten und ohne Test.
- **Evidenz:** Probe-Ausgaben 'PALETTE focus … after=BODY', 'ROWS tabIndex … [-1::2,-1::2]', 'ARROWDOWN focus now on: core-sw-01'; Modal-Kriterium durch keyboard.test.tsx belegt.
- **Abweichung:** Kriterium Tabellen (Pfeiltasten + Test) nicht erfüllt (L-T7-ui-14, P2); Palette ohne Fokusrückgabe (L-T7-ui-15, P2); Select-Test simuliert keine Tasten. Zugeordnete UI-01..10-Defekte außerhalb der Tastaturkriterien: L-T7-ui-23, L-T7-ui-24, ext Webhooks-Seite (je P2).
- **Prüfebenen:** UX: FEHLER – keine Pfeiltasten in Tabellen, Palette schließt mit Fokus auf <body> (reproduziert). API: N/A. Daten: N/A. E2E: LÜCKE – nur jsdom und gemockte palette.spec; kein Real-Browser-Spec für Modal/Select/Tabellen. Robustheit: LÜCKE – keine Tests für verschachtelte Dialoge, Escape in Eingaben, Shift+Tab.
- **Abhängigkeiten:** WP-002 (Frontend-CI); WP-193 (axe) ergänzt; E-02 offen.
- **Defekte:** F-077 (P2) CI-Listen-Export nutzt deprecated /api/v1/export/cis als Link → 401 im Browser, F-078 (P2) Audit-Seite liest Felder, die die API nicht liefert ('Invalid Date', Objekt '—'), F-170 (P2) Tabellen ohne Pfeiltasten-Bedienung, ohne Tabellenbaustein und ohne Test, F-171 (P2) Command-Palette gibt den Fokus nach dem Schließen nicht zurück (Fokus landet auf <body>), extern: Webhooks page silently swallows the server's refusal (egress protection / validation errors): no error message, the form stays as it was
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Tabellen-Tastaturmuster mit Vitest- und Playwright-Test; Fokusrückgabe der Palette mit Test (activeElement ≠ body); Real-Browser-Keyboard-Spec für alle vier Bausteine in allen Projekten.

<a id="wp-193"></a>
#### WP-193 – Browsermatrix und WCAG 2.1 AA (axe), Raumpositionierung ohne Drag-and-drop

- **Status:** FEHLGESCHLAGEN · **Priorität:** P2 · **Anforderungen:** UI-18, NFR-07
- **Anforderung/Nutzerziel:** Playwright-Browsermatrix nach NFR-07 (Chromium/Firefox/WebKit, Edge-Kanal, Mobil) mit axe-AA-Prüfung aller Hauptseiten in CI ohne Verstöße; Raumpositionierung alternativ per Tastatur/Formular.
- **Implementierung:** frontend/playwright.config.ts:23-54 (6 Projekte; Kommentar :23-25: Vorversionen nicht abgedeckt); .github/workflows/ci.yml:315-363 (Matrix je Projekt inkl. a11y.spec.ts); frontend/e2e/a11y.spec.ts:23-35 (MAIN_PAGES: 11 Routen + Login, leere Mocks, nur hell; App.tsx hat ~40 Routen); RoomPlanPage.tsx:113-139 (keyboardMove, Ganz-Layout-PATCH), :141-148 Koordinatenformular, :231 fokussierbare Marker, :273; backend tenantapi/model.go:155-170, pg_repository.go:630-678 (UpdateRoom ohne Bereichsprüfung).
- **Test(s):** PASS: e2e-mocked a11y.spec.ts 12/12 chromium + 12/12 mobile-chrome; firefox/webkit/edge/mobile-safari lokal nicht ausführbar (Browser-Download durch Netzwerk-Policy blockiert), CI-Ergebnisse nicht beobachtbar; Vitest RoomPlanPage.test.tsx (6). Real-Stack locations-racks.spec.ts L06 (Anzeige im Raumplan, keine Tastaturpositionierung). Verifier-axe-Probe VP/pw/v-axe.spec.ts: 6 color-contrast-Verstöße (5× 2,53:1 hell auf /assets, /stocktake, /assignments, /documents, /tickets; 3,88:1 dunkel CI-Detail). Raum-Probe zz_verify_b24_room_test.go (audit_db_22): {0.6,0.5} persistiert, x=5,y=-3 gespeichert, Client-1 auf Client-2-Raum → not found.
- **Soll/Ist:** Soll: volle Browsermatrix inkl. Vorversionen, axe-AA-sauber auf allen Hauptseiten, Tastatur/Formular-Positionierung. Ist: Matrix konfiguriert, aber nur Chromium belegt, Vorversionen fehlen; axe-Abdeckung partiell mit realen AA-Verstößen auf nicht abgedeckten Seiten und im Dark Mode; Raumpositionierung umgesetzt und auf Repository-Ebene persistent, ohne serverseitige Bereichsprüfung.
- **Evidenz:** axe-Re-Run auf dem gebauten dist reproduziert alle sechs Verstöße; Raum-Probe belegt Persistenz/RLS und fehlende Validierung.
- **Abweichung:** Kriterium 'axe ohne AA-Verstöße' beobachtet verletzt (L-T7-ui-16, P2); Matrix ohne Vorversionen und Nicht-Chromium unbelegt (L-T7-ui-17, P2; Ausführung hier NICHT TESTBAR wegen Netzwerk-Policy); P3 L-T7-ui-41, L-T7-ui-42 (Hypothese).
- **Prüfebenen:** UX: FEHLER – AA-Kontrastverstöße auf Hauptlistenseiten (hell) und dem CI-Detail-Zurücklink (dunkel), reproduziert. API: LÜCKE – PATCH /rooms/{id} akzeptiert Koordinaten außerhalb 0..1. Daten: OK – Layout persistiert, RLS blockt fremde Räume (Probe, nicht committet). E2E: LÜCKE – a11y und Raumplan nur gemockt in Chromium-Projekten; L06 nur Anzeige. Robustheit: LÜCKE – Ganz-Layout-PATCH aus veraltetem Zustand (Hypothese).
- **Abhängigkeiten:** WP-002 (Frontend-CI), WP-192 (Tastaturbausteine); i18n-Teil UI-18 in WP-194; Browserinstallation durch Sandbox-Netzwerk-Policy blockiert.
- **Defekte:** F-172 (P2) WCAG-2.1-AA-Kontrastverstöße auf Hauptseiten, die a11y.spec.ts nicht abdeckt, F-173 (P2) Browsermatrix deckt keine Vorversionen ab; Nicht-Chromium-Engines unbelegt, F-341 (P3) Raum-Layout-Koordinaten werden serverseitig nicht validiert, F-342 (P3) Tastaturverschiebung im Raumplan patcht das ganze Layout aus veraltetem Zustand
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Kontrast behoben und a11y.spec.ts auf alle Hauptrouten inkl. /cmdb/:id mit Daten und Dark Mode erweitert, grün in allen sechs Projekten (CI-Artefakt); Vorversions-Job oder PO-Entscheidung; Layout-Bereichsvalidierung mit PG/API-Test; e2e-real Tastaturpositionierung.

<a id="wp-194"></a>
#### WP-194 – i18n: englische Enumwerte, Statusbeschriftungen, Metamodell-Labels als Sprachmaps

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** GLO-10, UI-18
- **Anforderung/Nutzerziel:** Persistierte Enums sind englisch, alle Statuswerte werden über den i18n-Katalog angezeigt, die UI rendert Metamodell-Labels aus den WP-115-Sprachmaps, eine Lint-Regel verhindert Literale.
- **Implementierung:** frontend/src/stores/dashboard.ts (DASHBOARD_ROLES, parseDashboardRole für Legacy technik/einkauf); i18n/de-DE.json + en-US.json; StocktakeListPage/DiscoveryPage/DashboardPage/CITypeAdminPage via t()/localizedText; eslint.config.js:22-40 (no-restricted-syntax für 4 Dateien); i18n/catalog.test.ts. Nicht abgedeckt: pages/CIFormModal.tsx:30, :66-75 (hart kodierte Typschlüssel/-Labels), CIDetailPage.tsx:114, :136 (ci_type_id-UUID), lib/fieldmeta.ts:241-255 (englische Meldungen), rohe Status-Badges u. a. AssetListPage.tsx:255, TicketListPage.tsx:151, OrderListPage.tsx:87, KeyListPage.tsx:102, SecurityPage.tsx:143, RoomPlanPage.tsx:273.
- **Test(s):** PASS: Vitest i18n/catalog.test.ts (10), DashboardPage.test.tsx (4); Lint exit 0. Verifier-Probe VP/pw/v-i18n.spec.ts (de, built dist, gemockte API): Zeile 'INV-1 Laptop 1 laptop in_stock — Löschen'; CI-Detail zeigt Typ-UUID, kein de-Label. psql audit_db_22: keine deutschen Enumwerte/CHECKs. Defekttest-Welle (frontend/src/pages/CITypeLabels.auditdefect.tsx, mechanisch verifiziert): 'labels the type options with display_name in the UI language (WP-194 P2)' und 'shows the localized type label on the CI detail page, not the type UUID' failed as expected. Real-Stack ci-lifecycle L01 FAIL (Formular sendet Typschlüssel → 400).
- **Soll/Ist:** Soll: englische Enum-Schlüssel, alle Statuswerte über den Katalog, Metamodell-Labels aus Sprachmaps, Lint-Schutz. Ist: die 4 genannten Seiten sind sauber und die DB enthält keine deutschen Enums, aber viele Seiten rendern rohe Statusschlüssel, CI-Formular und -Detail ignorieren die Label-Maps, die Lint-Regel ist eng.
- **Evidenz:** Probe reproduziert rohes 'in_stock' und Typ-UUID im Detail; Defekttests schlagen erwartungsgemäß fehl; 6 Rohrender-Stellen im Code stichprobenartig bestätigt.
- **Abweichung:** Kriterium 'alle Statuswerte über Katalog' beobachtet verletzt (L-T7-ui-18, P2); 'UI rendert i18n-Maps' nur teilweise: CI-Formular mit hart kodierter Typliste (L-T7-ui-03, P1 – gleiche Ursache wie die blockierte CI-Anlage, daher WP-Priorität durch Zusammenführung P2 → P1) und CI-Detail mit Typ-UUID (L-T7-ui-19, P2); Lint-Regel nur 4 Dateien; P3 L-T7-ui-43. Die geplante Migration war unnötig (keine deutschen Enums persistiert).
- **Prüfebenen:** UX: FEHLER – rohe Enum-Schlüssel (in_stock) und Typ-UUID in deutscher UI, Formular mit fester Typliste. API: N/A – Anzeigeschicht (display_name in der API vorhanden). Daten: OK – keine deutschen persistierten Enums (psql), Legacy-localStorage gemappt. E2E: LÜCKE – kein Sprachwechsel gegen reale API-Daten; L01 zeigt den Folgeschaden (400). Robustheit: LÜCKE – Katalogtest 5 Enum-Gruppen, kein Scan auf Rohrendering, Lint 4 Dateien.
- **Abhängigkeiten:** WP-115 (LocalizedText display_name), WP-143 (Collector-Status); überschneidet WP-193/UI-18 und WP-117/WP-120 (CI-Formular, T6).
- **Defekte:** F-016 (P0) CI-Dialog 'CI erstellen' nutzt hart kodierte Typ-Keys: sendet 'server' als ci_type_id (400) und bietet keine Laufzeit-/Org-Typen, F-174 (P2) Status-/Enum-Schlüssel werden roh statt über den i18n-Katalog angezeigt, F-175 (P2) CI-Detail zeigt die Typ-UUID statt der lokalisierten Typbezeichnung, F-343 (P3) Validierungsmeldungen des DynamicForm sind englische Literale
- **Statusverlauf:** Einzelprüfung FEHLGESCHLAGEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** Alle Statuswerte über den Katalog mit erweitertem Katalogtest und Source-Scan; CITypeLabels.auditdefect.tsx alle drei Tests PASS; Lint-Regel auf src/pages/**; e2e-real Sprachwechsel gegen reale API-Daten.

<a id="wp-195"></a>
#### WP-195 – Topologie-UI: Startdialog über 2.000 Knoten, Simulation mit mehreren Ausfall-IDs

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** UI-06
- **Anforderung/Nutzerziel:** Die Topologie-UI zeigt über 2.000 Knoten einen Startdialog mit Standort-/Typauswahl und erlaubt eine Ausfallsimulation mit Mehrfachauswahl (IMP-05 über WP-180).
- **Implementierung:** frontend/src/pages/TopologyPage.tsx:129-131 (422 → tooLarge), :258-263 und :371-418 TopologyStartDialog (Site-/Typ-Select, Submit bis Auswahl gesperrt), :96-102 und :181-186 (?simulate=a,b), :145-153 (stiller BFS-Fallback), :341 (Knoten-Badge mit roher ci_type); hooks.ts:214-225 useImpactMany. Backend topology.go:26, :212-224, :120-142, :270-277 (GetByID-Fehler durchgereicht → 500); impact.go:434, :445-472.
- **Test(s):** PASS: Vitest TopologyPage.test.tsx 17/17 (fetch gestubbt, TopologyGraph gemockt; Startdialog nur Standortpfad :191-235); internal/topology TestMultiImpactAgainstPostgres (Baseline + final); e2e-mocked topology.spec.ts (chromium, mobile-chrome). Real-Stack: e2e-onboarding-real.log #6 (Simulation mit genau einer ID), tenant-isolation T10 PASS (Rendering), T15 FAIL (500). Verifier-Probe TestProbeV25TopologyLimit (audit_db_23): 2000 → 200 mit 2000 Knoten, 2001 → 422, Root bei 2001 → 200, site_id → 10 Knoten, client_id → 11, ungültige Filter → 400. TestProbeB25TopologyForeignRoot: fremde/zufällige Root → 500. Lastlauf k6 Lauf 2: topology p95 682 ms innerhalb Budget.
- **Soll/Ist:** Soll: Startdialog über 2.000 Knoten und Simulation mit mehreren Ausfall-IDs. Ist: implementiert und verdrahtet; Backend-Grenze und Filter auf PostgreSQL belegt, UI nur mit Mocks; Fokus auf eine veraltete/fremde Root ergibt HTTP 500.
- **Evidenz:** Serverseite vollständig auf PG belegt (Probe, Test); UI-Dialog (nur Standortpfad) und Mehrfachsimulation nur mit Stubs/Mocks; kein realer UI→API→DB-Lauf des Dialogs oder einer 2-CI-Simulation (grep e2e-real/onboarding.spec).
- **Abweichung:** UI-Ablauf (Startdialog, Mehrfachsimulation) nur mit Mocks belegt; /topology mit unbekannter/fremder Root → 500 (L-T7-ui-05, P2); P3: L-T7-ui-44 (Typ-UUID in Knotenliste), L-T7-ui-25 (stiller Fallback, Hypothese).
- **Prüfebenen:** UX: LÜCKE – Dialog beschriftet und bis zur Auswahl gesperrt, aber Knoten-Badge mit Typ-UUID und still ersetzte Impact-Fehler; Startdialog nicht im axe-Scan. API: FEHLER – /topology mit fremder/unbekannter Root → 500; Grenze und Filter korrekt (Probe). Daten: OK – Limit serverseitig, Site-/Client-/Typ-Einschränkung auf PG belegt. E2E: LÜCKE – Dialog und Mehrfachsimulation nie UI→API→DB. Robustheit: LÜCKE – Impact > 100 CIs (422) vom Fallback verschluckt.
- **Abhängigkeiten:** WP-180 (Mehrfach-Impact, 2.000-Grenze), TEN-09 (T15).
- **Defekte:** F-161 (P2) GET /topology mit unbekannter oder fremder root_ci_id antwortet 500 statt 404, F-326 (P3) Topologie-Simulation zeigt bei /impact-Fehler still eine richtungsnaive Client-Berechnung, F-344 (P3) Topologie-Knotenliste zeigt die CI-Typ-UUID statt des Typnamens
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** e2e-real: Startdialog bei > 2.000 CIs (Site- und Typpfad) und 2-CI-Simulation = /impact-Antwort; /topology mit unbekannter/fremder Root → 404 (Integrationstest + T15 grün); Grenz-Probe als Repo-Integrationstest.

<a id="wp-196"></a>
#### WP-196 – Review-Inbox mit Diff, Einzel- und Bulk-Auflösung

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** UI-16
- **Anforderung/Nutzerziel:** Eine Review-Inbox mit Filter, Diff-Ansicht, Einzelaktionen merge/new/dismiss/accept und Bulk-Auflösung.
- **Implementierung:** frontend/src/pages/ReviewInboxPage.tsx:57-73 reviewDiff, :82-88 Bulk-Aktionen (ohne merge), :140-175 Filter, :177-210 Bulk, :278-355 Detail/Diff/Kandidatenauswahl (rohe IDs :290-296); hooks/useReviews.ts:22-34 REVIEW_ACTIONS, :61-67 limit=100, :69-74 POST /review-items/{id}/resolve; Backend discovery/discovery.go:719, discovery/review.go:48-60, :118-212 resolveCIReview (new: CITypeID = payload ci_type_name :172); reviews/service.go (409 bereits aufgelöst, 422 unzulässige Aktion).
- **Test(s):** PASS: Vitest ReviewInboxPage.test.tsx 7/7 (Stubs); internal/reviews TestTST01ReviewResolution (PG, Effekte gestubbt), TestReviewRepositoryOnPostgreSQL; internal/discovery TestResolveReviewItemMerge, TestResolveReviewItemCreate (Memory), TestResurrectedDeviceIsReactivatedOrNew; e2e-mocked review.spec.ts. Real-Stack discovery-override.spec.ts D04, D05, D06 PASS in zwei Läufen (SP/work/discovery-override/final.log, SP/work/verify-discovery-override/full.log: Typfilter, echter Diff, accept/dismiss mit Persistenz und Wirkung im nächsten Lauf, Bulk-accept '2 erledigt, 0 fehlgeschlagen.'); Onboarding #6 (dismiss); tenant-isolation T08/T10. Verifier-Probe TestProbeV25ReviewEffects (PG, audit_db_23): merge → 200 (Kandidat management_ip gesetzt), new ambiguous_identity → 200, new unclassified_device → 400, Item bleibt open.
- **Soll/Ist:** Soll: Inbox mit Filter, Diff, merge/new/dismiss/accept und Bulk. Ist: Filter, Diff, accept, dismiss und Bulk Ende-zu-Ende real belegt; merge im Backend (PG-Probe) funktionsfähig, aber nicht über die UI belegt; new funktioniert für ambiguous_identity, scheitert für unclassified_device.
- **Evidenz:** Real-Stack D04/D05/D06 (zwei Läufe); PG-Probe mit Ausgaben 'merge -> 200', 'new (unclassified_device, unknown type) -> 400', 'after: item status=open'.
- **Abweichung:** Aktion 'new' für unclassified_device schlägt immer fehl (L-T7-ui-20, P2); merge und new nicht über die reale UI belegt; P3: L-T7-ui-45 (100er-Grenze), L-T7-ui-46 (Kandidaten-UUIDs).
- **Prüfebenen:** UX: LÜCKE – Kandidaten als UUIDs, 100er-Limit ohne Paging, kryptischer 400-Text; /reviews nicht im axe-Scan. API: FEHLER – 'new' für unclassified_device scheitert immer; Routen, Authz, 409/422 korrekt (D05). Daten: OK – accept/dismiss/Bulk real persistiert; merge/new-Effekte per PG-Probe. E2E: LÜCKE – accept/dismiss/Bulk/Filter/Diff real; merge und new ohne Real-UI-Lauf. Robustheit: LÜCKE – Doppelauflösung real abgewiesen; select-all nur über die ersten 100.
- **Abhängigkeiten:** WP-088/089 (Review-Modell und -Effekte, Eigentümer des new-Defekts), WP-168; Real-Stack für e2e-real.
- **Defekte:** F-176 (P2) Review-Inbox: 'Neu anlegen' (action new) für unclassified_device scheitert immer; Items mit unbekanntem Typ erhalten ein kryptisches 400 und bleiben offen, F-345 (P3) Review-Inbox auf 100 Einträge ohne Paging begrenzt; 'Alle offenen auswählen' erfasst nur die geladene Seite, F-346 (P3) Merge-Kandidatenauswahl der Review-Inbox listet rohe CI-UUIDs
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** e2e-real merge in gewählten Kandidaten und new mit DB-Assertions und 409/403-Negativen; 'new' für unclassified_device gelingt oder wird nicht angeboten (PG-Integrationstest).

<a id="wp-197"></a>
#### WP-197 – Benachrichtigungs- und Jobcenter

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** UI-16
- **Anforderung/Nutzerziel:** Ein Benachrichtigungscenter (gelesen/ungelesen) und ein übergreifendes Jobcenter (Fortschritt, Cancel, Ergebnis).
- **Implementierung:** frontend/src/components/NotificationCenter.tsx:15-118 (Zähler, Panel, einzeln/alle gelesen; kein isError-Zweig :22-23, :72-76); hooks/useNotifications.ts:18-44; pages/JobCenterPage.tsx:33-164 (Filter, Fortschrittsbalken, Cancel für queued/running :119-128, 'Ergebnis' für jeden succeeded :129-133, Cancel-Fehleralarm :72-75); hooks/useJobs.ts:35-64. Backend notify/handler.go; jobs/handler.go:127-140 (result 409 ohne Ergebnis), :161-180; jobs/service.go:200-209 (Cancel nur Statuswechsel), :273-317 (Mirror setzt nie Result); server/router.go:335, :359 (exportWorker.Tracker = jobService); export/worker.go:134-176 liest den generischen Jobstatus nie.
- **Test(s):** PASS: Vitest NotificationCenter.test.tsx 2/2, JobCenterPage.test.tsx 3/3 (Stubs); internal/notify TestNotificationsAgainstPostgres, TestNotificationAPI; internal/jobs TestJobAPIListGetResult, TestJobAPICancel, TestJobAPIRevert (Memory), TestJobsAgainstPostgres (Baseline + final). Real-Stack bulk-jobs-export.spec.ts J01 und N01 PASS in zwei Läufen (SP/work/bulk-jobs-export/final.log, SP/work/verify-bulk-jobs-export/final.log: Jobzeile bis 'Erfolgreich', aria-valuenow=100, Statusfilter, Ergebnisdialog 98/2, Persistenz, fertiger Job DELETE 409, Cross-Tenant 404; Glockenlabel = API-Unread, 'Als gelesen markieren' 204, read_at persistiert, Tenant-Isolation). Probes (audit_db_23): Export-Cancel 'generic job status=cancelled; export job status=completed'; gespiegelter Job GET /result → 409.
- **Soll/Ist:** Soll: Benachrichtigungscenter mit gelesen/ungelesen; Jobcenter mit Fortschritt, Cancel und Ergebnis. Ist: Benachrichtigungscenter Ende-zu-Ende belegt; Jobcenter für native Jobs (Fortschritt, Filter, Ergebnis) real belegt; Cancel für gespiegelte Exportjobs wirkungslos, 'Ergebnis' bei gespiegelten Arten → 409; UI-Cancel real unbelegt.
- **Evidenz:** J01/N01 real (zwei Läufe) belegen das Benachrichtigungskriterium vollständig und Fortschritt/Ergebnis; Probes reproduzieren den kosmetischen Cancel und die 409.
- **Abweichung:** Kriterium Jobcenter nur teilweise: Cancel stoppt Export bzw. laufende Jobs nicht (L-T7-ui-06, P2), UI-Cancel real nicht belegt; P3: L-T7-ui-47 ('Ergebnis' → 409), L-T7-ui-48 (Ladefehler als 'keine Benachrichtigungen').
- **Prüfebenen:** UX: LÜCKE – Benachrichtigungspanel ohne Fehlerzustand, Jobarten unübersetzt ('ci.bulk'), 'Ergebnis' für ergebnislose Jobs. API: FEHLER – DELETE auf Exportjob 'cancelled', während der Export fertig wird; /result 409 für gespiegelte Jobs. Daten: LÜCKE – generischer Job und export_job divergieren nach Cancel; Lesestatus persistiert (N01). E2E: LÜCKE – Benachrichtigungen und Jobcenter (Fortschritt/Filter/Ergebnis) real; UI-Cancel und 'Alle gelesen' nicht. Robustheit: LÜCKE – 409/404 real belegt; 403-Cancel via UI und Ladefehler ungetestet.
- **Abhängigkeiten:** WP-182 (Mirror-/Cancel-Semantik), WP-189 (Export-Worker), WP-139 (Benachrichtigungen).
- **Defekte:** F-162 (P2) Job-Cancel stoppt laufende Arbeit nicht (ci.bulk, Export): Status 'cancelled', aber alle Änderungen werden ausgeführt bzw. der Export fertiggestellt, Teilbericht geht verloren, F-347 (P3) Jobcenter bietet 'Ergebnis' für jeden erfolgreichen Job an, gespiegelte Jobs haben aber nie ein Ergebnis (409), F-348 (P3) Benachrichtigungspanel verbirgt Ladefehler als 'keine Benachrichtigungen'
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Cancel stoppt gespiegelte Exportjobs oder wird für sie ausgeblendet (PG-Integrationstest); 'Ergebnis' nur bei vorhandenem Ergebnis (Vitest); e2e-real UI-Cancel (Bulk und Export) inkl. 403-Negativfall.

<a id="wp-198"></a>
#### WP-198 – Provenienz-Tooltip mit Zeit und Auswahlbegründung für alle typisierten Felder

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** REC-10
- **Anforderung/Nutzerziel:** Die API liefert Provenienz für alle typisierten Felder; ein Tooltip zeigt Quelle, Zeit, Rang bzw. Override-Grund.
- **Implementierung:** backend/internal/ci/provenance.go:58-77 (14 typisierte Felder), :87-106 (GET /cis/{id}/provenance, fremde Org 404), :111-150 BuildProvenance, :152-172 fromRecord (Grund override/source_rank, Rang, Zeiten, Autor, Grund, überdeckter Wert); ci/handler.go:65; server/router.go:651; OpenAPI CIFieldProvenance. Frontend components/cmdb/ProvenanceTooltip.tsx:18-79 (Hover/Fokus, role=tooltip, Escape; Autor roh :66), CIDetailSections.tsx:219-305 (ohne Fehlerzweig), api/cmdbHooks.ts:266-272.
- **Test(s):** PASS: internal/ci TestProvenanceAgainstPostgres (PG: beobachtete, Override- und Record-Gründe, leere Felder ausgelassen, Cross-Org 404; Baseline + final); Vitest ProvenanceTooltip.test.tsx 4/4. Real-Stack discovery-override.spec.ts D02 und D03 PASS in zwei Läufen (SP/work/discovery-override/final.log, SP/work/verify-discovery-override/full.log): Provenienz aller belegten typisierten Felder mit Quelle, Rang > 0 und Zeit; Tooltip zeigt 'Höchstrangige Quelle', Quelle, Rang, Beobachtet, Erfasst bzw. 'Manueller Override', Begründung, Autor, Überdeckt; Persistenz nach Reload; tenant-isolation T05 PASS.
- **Soll/Ist:** Soll: Provenienz aller typisierten Felder per API, Tooltip mit Quelle, Zeit, Rang/Override-Grund. Ist: erfüllt und auf PostgreSQL sowie im realen Browser gegen den Real-Stack inkl. Tenant-Isolation belegt; nur kleinere UX-Abweichungen.
- **Evidenz:** Beide Kriterien durch PG-Test und Real-Stack D02/D03 (UI→API→DB, je zwei Läufe) belegt; kein P0–P2-Defekt berührt die Kriterien.
- **Abweichung:** P3 L-T7-ui-49 (Autor als User-ID), L-T7-ui-50 ('keine Daten' bei API-Fehler) außerhalb der Kriterien; Asset-/Attribut-Provenienzpfade ungetestet (niedrig).
- **Prüfebenen:** UX: OK – Info-Button je Feld, Tooltip per Hover/Fokus, aria-describedby, Escape; real in D02/D03 (P3: Autor als UUID, kein Fehlerzustand). API: OK – vollständige Liste, 404 fremd/unbekannt (PG + T05), 422 für strukturelles Override. Daten: OK – Vorrang auf PG korrekt, Override-Autor/-Zeit/-Grund persistiert und auditiert. E2E: OK – UI→API→DB für beobachtete und Override-Werte (D02, D03). Robustheit: OK – Cross-Tenant 404, Pflichtgrund, strukturelle Ablehnung; Asset-/Attributpfad ungetestet.
- **Abhängigkeiten:** WP-060, WP-084, Override-Repository.
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-349 (P3) Override-Autor wird im Provenienz-Tooltip als rohe User-ID gezeigt, F-350 (P3) Provenienz-Abschnitt zeigt bei API-Fehler 'Noch keine Provenienz-Daten'
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Bereits BESTANDEN (Urteil des Verifiers TEILWEISE → BESTANDEN wird übernommen). Herabstufen nur, falls D02, D03 oder TestProvenanceAgainstPostgres künftig scheitern.

<a id="wp-199"></a>
#### WP-199 – Onboarding-Flow: Registrierung → Verifikation → Scope-Bestätigung → erste CIs

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P3 · **Anforderungen:** UI-17
- **Anforderung/Nutzerziel:** Ein geführter Onboarding-Flow mit Fortschrittsanzeige über Signup, Verifikation, Collector-Enrollment und Scope-Bestätigung bis zu den ersten CIs.
- **Implementierung:** frontend/src/pages/OnboardingPage.tsx:19-43 (5 Schritte), :50-92 (Fortschrittsbalken + Schrittliste), :94-151 (Signup mit ALTCHA), :153-214 (Verifikation, 410/404), :216-248 (Enrollment-Code), :250-299 (Scope-Vorschläge: Bestätigen/Verwerfen/Weiter), :301-335 (10-s-Polling; scopesDone = lokales Skip oder keine offenen Vorschläge mit CIs > 0); Route App.tsx:301-309; kein Link auf /onboarding (nur App.tsx:303, OnboardingPage.tsx:175; LoginPage ohne Signup-Link).
- **Test(s):** PASS: Vitest OnboardingPage.test.tsx 7/7 (Stubs); internal/signup (19 Tests); internal/discovery TestDiscoveryScopes (PG; Bestätigen von scope_suggested legt Scope an). Real-Stack SP/runs/e2e-onboarding-real.log 6/6 PASS: #5 Signup, Mailpit-Verifikation, Trial, Login, Dashboard; #6 Enrollment-Code aus der UI, echter Collector, Fortschritt verlässt Schritt 2, ≥ 30 CIs, Abschlusstext. Scope-Schritt in onboarding.spec.ts:243-255 nur bedingt geklickt, ohne Assertion, obwohl der Collector 'suggested:3' meldete.
- **Soll/Ist:** Soll: geführter 5-Schritt-Flow mit Fortschritt. Ist: umgesetzt und weitgehend auf dem Real-Stack belegt; die Scope-Bestätigung ist im realen E2E optional und unbelegt; es gibt keinen sichtbaren Einstieg zum Signup.
- **Evidenz:** e2e-onboarding-real.log '6 passed (34.8s)'; Scope-Bestätigung nur über Vitest (POST-Body) und TestDiscoveryScopes (PG) belegt.
- **Abweichung:** Teil 'Scope-Bestätigung' des Kriteriums ohne reale UI-Evidenz (Testlücke; Ursache ist die Spec-Schwäche L-T7-ui-21 der WP-200-Suite, kein Produktdefekt dieses WPs); kein In-App-Einstieg (L-T7-ui-51, P3).
- **Prüfebenen:** UX: LÜCKE – kein In-App-Einstieg zu Signup/Onboarding, Skip-Status nur lokal. API: OK – signup, verify, enrollment-codes, Scope-Vorschläge/resolve, cis und collectors real genutzt. Daten: OK – Org und ≥ 30 CIs real persistiert; Scope-Anlage nur auf PG, nicht über diese UI. E2E: LÜCKE – Kette weitgehend real, Scope-Schritt unbelegt. Robustheit: LÜCKE – keine realen Negativpfade (wiederverwendeter/abgelaufener Link, doppelte E-Mail).
- **Abhängigkeiten:** WP-111/112, WP-156, WP-200 (Suite); Real-Stack (Keycloak, Mailpit, Collector).
- **Defekte:** F-351 (P3) Kein In-App-Einstieg zu Self-Signup bzw. Onboarding
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Realer Onboarding-E2E bestätigt einen Scope deterministisch über die UI und prüft per GET /discovery-scopes und /review-items?kind=scope_suggested sowie Fortschritt 3 → 4 → 5; Signup-Einstiegslink.

<a id="wp-200"></a>
#### WP-200 – E2E: Install → Signup/Login → Dashboard; Onboarding-Fehlerbilder

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** INS-04, TST-03, ABN-01
- **Anforderung/Nutzerziel:** Eine Playwright-Suite gegen eine frisch installierte Instanz (install-smoke) inkl. INS-04-Fehlerbilder, die die ABN-01-[B]-Onboarding-Szenarien abdeckt.
- **Implementierung:** frontend/e2e/onboarding.spec.ts:124-330; frontend/e2e/fixtures/playwright.install.config.ts; fixtures/setup-onboarding.sh:88-97 (BASE_URL=http://localhost:<port>, E2E_INSECURE_URL, E2E_ISSUER_URL); .github/workflows/install-smoke.yml:164-199 (Setup und Suite nach der Installation).
- **Test(s):** SP/runs/e2e-onboarding-real.log 6/6 PASS gegen nativen Real-Stack (Production-Server, Keycloak 24, Mailpit, echter Collector). Paketierte Installation hier nicht ausführbar: SP/runs/install.log 'Image build failed', INSTALL_EXIT=1 (dl-cdn.alpinelinux.org/quay.io 403). Ergänzend real: ci-lifecycle L10 und bulk-jobs-export E01 PASS (Job-Datei-Download, außerhalb dieser Suite).
- **Soll/Ist:** Soll: install-smoke-Suite mit INS-04-Fehlerbildern und ABN-01(d). Ist: Suite vorhanden und 6/6 grün auf nativem Real-Stack; Nachweis gegen die paketierte Installation fehlt hier; Scope-Schritt, Job-Datei-Download und Localhost-Prüfung schwach.
- **Evidenz:** e2e-onboarding-real.log '6 passed (34.8s)'; install.log INSTALL_EXIT=1; Code-Stellen onboarding.spec.ts:135-146, 243-255, 302-328; setup-onboarding.sh:89-93.
- **Abweichung:** Kriterium 1 gegen die paketierte install-smoke-Instanz hier NICHT TESTBAR (Netzwerk-Policy), nur nativer Real-Stack belegt; Kriterium 2 teilweise: Schritt 'Scope bestätigen' optional (L-T7-ui-21, P2); P3: L-T7-ui-52 (Localhost-Redirect nicht diskriminiert), L-T7-ui-53 ('Export laden' über synchronen Endpunkt).
- **Prüfebenen:** UX: OK – Fehlerbildtexte in realem Chromium geprüft. API: OK – reale API über den Frontend-Origin. Daten: OK – Org angelegt, ≥ 30 CIs, Review-Zähler sinkt, Export row_count ≥ 30. E2E: LÜCKE – nicht gegen die paketierte install-smoke-Instanz; zwei ABN-01(d)-Schritte schwach geprüft. Robustheit: LÜCKE – Localhost-Redirect nicht diskriminiert; nur Chromium.
- **Abhängigkeiten:** WP-199, WP-092, WP-112; paketierte Installation braucht Netzwerkzugang (nur CI).
- **Defekte:** F-177 (P2) Onboarding-Abnahmesuite: Schritt 'Scope bestätigen' (ABN-01 d) ist optional und unbelegt, F-352 (P3) INS-04-Fehlerbild 'Localhost-Redirect' wird in der Onboarding-Suite nicht diskriminiert, F-353 (P3) Onboarding-Schritt 'Export laden' prüft den synchronen CSV-Endpunkt statt der Datei des Exportjobs
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** CI-Artefakt der Suite gegen frisch paketierte Installation (install-smoke, 0 Retries); deterministische Scope-Bestätigung und Job-Datei-Download in der Suite; Test über nicht-localhost-TLS-Origin.

<a id="wp-201"></a>
#### WP-201 – E2E-Kernabläufe: CI, Review, Drag/Undo, Topologie, Export, Palette

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** TST-03
- **Anforderung/Nutzerziel:** Playwright-Specs je Kernablauf (CI, Review, Drag/Undo, Topologie, Export, Palette), die in der Browsermatrix aus WP-193 laufen; P2-Abläufe nicht Bestandteil.
- **Implementierung:** frontend/e2e/ci.spec.ts:21-75 (prüft im POST-Body nur name und discovery_source :70), review.spec.ts, rack-drag.spec.ts, topology.spec.ts, export.spec.ts, palette.spec.ts; permissiver Mock frontend/e2e/support/backend.ts:72-79 (unbekannte /api/v1-Route → 200); Matrix playwright.config.ts:26-55, .github/workflows/ci.yml:310-363. Produktcode im Test: frontend/src/pages/CIFormModal.tsx:30, :66-74 (hart kodierte Typschlüssel als ci_type_id).
- **Test(s):** PASS: SP/runs/e2e-mocked.log 50/50 (chromium, mobile-chrome); firefox/webkit/msedge/mobile-safari nicht ausführbar. Real-Stack-Gegenprobe: ci-lifecycle.spec.ts L01 FAIL ('POST /cis sent by the CMDB form: 400', SP/work/ci-lifecycle/final.log und SP/work/verify-ci-lifecycle/single-L01.log; Trace-Body ci_type_id 'server'), L03a FAIL (durch L01 blockiert), L05 FAIL (Feld leeren nicht gespeichert, ext), L10b FAIL (401), L02/L04/L08/L09/L10 PASS; discovery-override D05/D06 PASS; bulk-jobs-export E01 PASS; tenant-isolation T10 PASS. Kein realer Drag/Undo- und kein realer Mehrfachausfall-Lauf. Defekttest-Welle: CITypeLabels.auditdefect.tsx:113 'posts the CI type UUID from GET /ci-types when the user picks "Server" (WP-201 P1)' failed as expected (mechanisch verifiziert).
- **Soll/Ist:** Soll: Specs je Ablauf, die die Kernabläufe im Browser in der vollen Matrix verifizieren. Ist: Specs vorhanden und grün in 2 von 6 Projekten gegen einen permissiven Mock; mindestens ein Kernablauf (CI-Anlage per Formular) ist real defekt, während seine Spec grün bleibt – falsche Sicherheit.
- **Evidenz:** e2e-mocked '50 passed'; Real-Stack L01 zweimal 400; L10b 401; reale Abdeckung für Review (D05/D06), Exportseite (L10, E01), Palette (L09) bestätigt.
- **Abweichung:** Kriterium 'Specs je Ablauf' nur teilweise: CI-Ablauf real defekt bei grüner Mock-Spec (L-T7-ui-03, P1; maskiert durch L-T7-ui-22, P2); Export-Buttons der CI-Liste tot (L-T7-ui-23, P2); Matrix nur 2/6 Projekte belegt; ext: CI-Formular L01 (P1, gleiche Ursache wie L-T7-ui-03), Feld leeren L05 (P2), Beziehungsdialog L08 (P3, unverifiziert).
- **Prüfebenen:** UX: OK – reale Browserinteraktion (chromium, mobile-chrome) mit Mockdaten. API: FEHLER – Mocks nicht vertragsgeprüft; ci_type_id-Schlüssel/UUID-Fehler grün im Mock, 400 real; unbekannte Routen → 200. Daten: N/A für Mock-Specs; Real-Gegenprobe: CI-Anlage per Formular persistiert nicht. E2E: LÜCKE – Specs nur UI; reale Äquivalente für Review, Exportseite, Palette; CI-Anlage FAIL; Drag/Undo und Mehrfachausfall ohne Real-Lauf. Robustheit: LÜCKE – keine Fehlerpfad-Specs; 4/6 Browserprojekte nicht ausgeführt.
- **Abhängigkeiten:** WP-193 (Matrix), WP-195/196/189 (Seiten); CI-Formular-Defekt gehört zum CI-UI-WP (WP-117/WP-120, T6); Firefox/WebKit/Edge brauchen netzwerkfähigen CI-Runner.
- **Defekte:** F-016 (P0) CI-Dialog 'CI erstellen' nutzt hart kodierte Typ-Keys: sendet 'server' als ci_type_id (400) und bietet keine Laufzeit-/Org-Typen, F-077 (P2) CI-Listen-Export nutzt deprecated /api/v1/export/cis als Link → 401 im Browser, F-178 (P2) Permissives Mock-Backend der E2E-Suite (unbekannte Routen → 200, keine Vertragsprüfung) maskiert UI→API-Verdrahtungsfehler, extern: Clearing an optional field in the CI edit form is silently not saved, extern: Relationship dialog after a double click can stay open with a conflict error although the relationship was created (intermittent)
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** CI-Formular sendet echte Typ-IDs: Real-Stack L01 und vertragsvalidierte ci.spec.ts grün; strikte/OpenAPI-validierte Mocks oder Real-Stack-Varianten für CI, Review-merge, Drag/Undo-Persistenz, Topologie-Mehrfachausfall und beide Exportpfade (inkl. L10b); CI-Artefakt mit allen 6 Browserprojekten grün.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – defekte: „["L-T7-ui-03","L-T7-ui-22","L-T7-ui-23","ext:The CMDB 'CI erstellen' form cannot create any CI: it sends the type key ('server') as ci_type_id instead of the type's UUID, and the server answers 400","ext:Clearing an optional field in the CI edit form is silently not saved","ext:Relationship dialog after a double click can stay open with a conflict error although the relationship was created (intermittent)"]“ → „["L-T7-ui-03","L-T7-ui-22","L-T7-ui-23","ext:Clearing an optional field in the CI edit form is silently not saved","ext:Relationship dialog after a double click can stay open with a conflict error although the relationship was created (intermittent)"]“. Doppelter Eintrag. Die Reproduktion von L-T7-ui-03 ist genau dieser Real-Stack-Lauf ci-lifecycle L01 (owned=false). Die Synthese nennt ihn selbst 'gleiche Ursache wie L-T7-ui-03' und führt in L-T7-ui-03.ursache die Identität auf. Derselbe P1-Defekt steht also zweimal in der Liste von WP-201. Status TEILWEISE BESTANDEN und Priorität P1 bleiben.

<a id="wp-202"></a>
#### WP-202 – TST-01-Pflichtfälle schließen (Querschnitt)

- **Status:** BESTANDEN · **Priorität:** - · **Anforderungen:** TST-01
- **Anforderung/Nutzerziel:** Je TST-01-Pflichtfall existiert ein benannter Test, das Mapping steht in docs/traceability.csv, fehlende Fälle aus Vor-WPs sind ergänzt.
- **Implementierung:** backend/internal/discovery/tst01_integration_test.go:34-294 (21 Subtests über echtes POST /api/v1/discovery/ingest auf PG mit DB-Assertions), events/tst01_integration_test.go:20-104, rack/tst01_integration_test.go:18-71, reviews/tst01_integration_test.go:23-134, discovery/topology_integration_test.go:28-115, rules/rules_test.go:25-36 (api/rules.conformance.json, 39 Fälle; frontend src/rules/rules.test.ts liest dieselbe Datei), api/cursor_test.go, platform/crypto/crypto_test.go, export/export_test.go; Mapping docs/traceability.csv:419-427; Commit c54bd37.
- **Test(s):** PASS (Baseline + finaler Lauf SP/final/test-db.log): TestTST01Reconciliation mit 21/21 Subtests, TestTST01AuditChainAndOutbox 4/4 (atomar, Manipulationserkennung via audit.Verify), TestTST01FaceAwareExclusion 9/9, TestTST01ReviewResolution, TestTopologyConfidence (len(edges)==2, Trunk/ARP ohne Kante), TestConformance und TestSchemaMatchesOperators, TestCursorRoundTrip/IsSigned/DecodeCursorRejectsGarbage, TestEncryptDecrypt/Decrypt_Tampered/MasterKeyRotation, TestExportCSV, TestCSVFormatAndManifest; Vitest rules.test.ts PASS.
- **Soll/Ist:** Soll: benannter Test je TST-01-[B]-Pflichtfall mit Traceability-Mapping. Ist: erfüllt und auf PostgreSQL ausgeführt; [P2]-Teile nicht Bestandteil.
- **Evidenz:** Alle gemappten Tests liefen und bestanden; Testkörper gelesen (DB-Zählungen/-Felder, exakte Kanten, Kettenverifikation, gemeinsame Konformitätsdatei für Go und TS); Mapping traceability.csv:419-427.
- **Abweichung:** -
- **Prüfebenen:** UX: N/A. API: OK – Reconciliation-Fälle über den echten Ingest-Handler. Daten: OK – Assertions direkt auf PostgreSQL. E2E: N/A – Test-WP. Robustheit: OK – Negativfälle (Blocklisten, veraltete/zukünftige Beobachtungen, verweigerte Mutation, Manipulationserkennung, fremde Client-Bindung).
- **Abhängigkeiten:** WP-084/086/087/089, WP-158, WP-136, WP-118, WP-005.
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** -
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Bereits BESTANDEN. Herabstufen nur, falls ein gemappter TST-01-Test entfernt, übersprungen oder rot wird; empfohlen: Traceability-Lint für den TST-01-Fallkatalog.

### Lasttest (NFR-10)

<a id="wp-203"></a>
#### WP-203 – Demo-/Testdatensatz nach SIM-01 und skalierbarer Last-Seed

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** SIM-01, NFR-10
- **Anforderung/Nutzerziel:** Ein deterministischer Seed erzeugt den SIM-01-Demodatensatz (demo und acme, nutzbar als admin@demo.local), skaliert bis 50.000 CIs und läuft über make seed.
- **Implementierung:** backend/cmd/seed/seed.go:38, 90-399 (CIs ohne location_id :216-247); backend/cmd/seed/main.go:19-67; Makefile:147-160; Trigger ci_derive_location (WP-054); deploy/keycloak/realm-reticora-dev.json:258-285
- **Test(s):** PASS: TestSeedSIM01, TestSeedOptionsAreBounded; make seed OK (SP/runs/seed.log); Lastläufe mit 10k Seed. Defekttest FAIL wie erwartet: tests/audit/demo-realm-users.test.sh (1 ok, 2 FAIL: admin@demo.local und admin@acme.local fehlen).
- **Soll/Ist:** Determinismus, Skalierung und make seed sind belegt. Nicht nutzbar ist der Datensatz als Demo: Der Demo-Admin kann sich nicht anmelden, die CIs fehlen im Suchindex und haben keinen Standort.
- **Evidenz:** results-backend.tsv; SP/verify-tests-run2/sh-demo-realm-users.test.sh.log; psql-Prüfung audit_db_24 (0 von 10.120 CIs mit site/room/location, 0 CI-Suchdokumente).
- **Abweichung:** Kriterium 1 ist teilweise verletzt (Login P2; Suche und Standort P3). Webhook- und Review-Payloads sind Platzhalter.
- **Prüfebenen:** UX FEHLER (kein Login für die Demo-Org); API OK (API-Key-Rotation); Daten LÜCKE (kein Standort, kein Suchindex); E2E LÜCKE (UI erreicht die Seed-Daten nicht); Robustheit OK (Grenzen, Idempotenz)
- **Abhängigkeiten:** WP-106, WP-153, WP-088, WP-082, WP-054, Keycloak-Dev-Realm
- **Defekte:** F-113 (P2) Der SIM-01-Demo-Admin admin@demo.local kann sich nicht anmelden; die geseedete Demo-Org ist über die UI nicht erreichbar, F-277 (P3) Der Seed umgeht den Suchindex: geseedete CIs findet die globale Suche nicht, F-278 (P3) Geseedete CIs haben keinen Standort: site_id und room_id setzt der Trigger ci_derive_location auf NULL
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** demo-realm-users.test.sh besteht. Ein e2e-real-Login als admin@demo.local besteht. TestSeedSIM01 prüft location_id und die Suchdokumente. Ein Seed-Test mit 10.000 Objekten und integrity-check = 0.

<a id="wp-204"></a>
#### WP-204 – Lasttest-Harness und Ergebnisartefakte (NFR-10)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** NFR-10, ABN-02
- **Anforderung/Nutzerziel:** Wiederholbare k6-Szenarien für die Budgets aus NFR-01/02/03, ein load.yml-Workflow mit Artefakten, N+1-Budgets und EXPLAIN-Prüfungen nach ABN-02.
- **Implementierung:** tests/load/api.js:15-149 (search = GET /cis?search=), ingest.js:118-127; .github/workflows/load.yml:89-140; backend/internal/ci/querybudget_integration_test.go:55-168 (planUsesSeqScan); citype/ddl_job.go:65-89
- **Test(s):** PASS: TestCIQueryBudget (baseline, HEAD-Lauf 9,88 s; FAIL im Verifikationslauf am 18.10.), TestTopologyQueryBudget, TestImpactWorstCase, TestIngestStatementBudget, TestIdentityHitsMatchSingle. k6: Lauf 1 exit 99 (list/search), Lauf 2 alle Schwellen ✓. Defekttest FAIL wie erwartet: TestAuditIdentitySearchWithMACsUsesIndexes.
- **Soll/Ist:** Harness und Workflow existieren und laufen, die N+1-Budgets sind solide. Gemessen werden aber Ersatzgrößen: Suche und Attributfilter über Proxys, Beziehungssuche fehlt. Die EXPLAIN-Prüfung ist vakuös; der Attribut-Index wird unter RLS nicht genutzt.
- **Evidenz:** audit/phase1-final/evidence/load/k6-api-run1.txt, k6-api-run2.txt; SP/work/verify-B26/probe.txt, probe-testform.txt; audit/phase1-final/evidence/audit-tests/querybudget-flake.txt.
- **Abweichung:** Beide Kriterien sind teilweise verletzt: Szenarien unvollständig, EXPLAIN-Prüfung vakuös und instabil. Der Lauf von load.yml selbst ist hier nicht beobachtbar.
- **Prüfebenen:** UX N/A; API OK (0 fehlgeschlagene Requests); Daten LÜCKE (Seed ohne Attribute und Suchdokumente); E2E LÜCKE (GitHub-Lauf extern); Robustheit FEHLER (EXPLAIN-Prüfung erkennt fehlende Indexnutzung nicht)
- **Abhängigkeiten:** WP-203, WP-142, SR-04, WP-206
- **Defekte:** F-114 (P2) JSONB-Prädikate unter RLS nicht indexfähig: Attribut-Ausdrucksindex ungenutzt, Identitätssuche mit MACs als org-weiter Seq Scan, F-115 (P2) Die EXPLAIN-Prüfung nach ABN-02 in TestCIQueryBudget ist vakuös: sie akzeptiert einen Org-Index-Scan über die ganze Org und prüft kein Produkt-SQL, F-116 (P2) Das Suchbudget nach NFR-02 wird am Namensfilter der CI-Liste gemessen statt an der globalen Suche; Szenarien für Attributfilter und Beziehungssuche fehlen, F-277 (P3) Der Seed umgeht den Suchindex: geseedete CIs findet die globale Suche nicht, F-280 (P3) TestCIQueryBudget ist planner- und umgebungsabhängig instabil (falscher Fehlschlag 'sequential scan')
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** k6-Szenarien für globale Suche, JSONB-Attributfilter und Beziehungssuche bestehen wiederholt. Die EXPLAIN-Tests laufen auf Produkt-SQL und prüfen Indexnamen. Ein archivierter load.yml-Lauf.

<a id="wp-205"></a>
#### WP-205 – Reconciliation über 10.000 CIs hinaus (seitenweise Identitätssuche)

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** NFR-01
- **Anforderung/Nutzerziel:** Die Reconciliation findet das passende CI unabhängig von der Inventargröße, indexbasiert und seitenweise.
- **Implementierung:** backend/internal/discovery/pg_repository.go:801-946 (identityFilter mit attributes @> ANY je MAC); identity.go:99-105, 727-768; discovery.go:1120-1123, 1266-1273; lanes.go:114-116
- **Test(s):** PASS: TestReconciliationBeyondTenThousandCIs (10.500), TestIdentityOverWholeInventory (10.050, Handler), TestIdentityHitsMatchSingle. Defekttest FAIL wie erwartet: TestAuditIdentitySearchWithMACsUsesIndexes (identity_plan_auditdefect_test.go:156 'Seq Scan on ci').
- **Soll/Ist:** Seitenweise und ohne Obergrenze ist belegt. 'Indexbasiert' ist für Records mit MACs verletzt: ein org-weiter Seq Scan unter RLS, auf dem Queue-Pfad je bekanntem Gerät.
- **Evidenz:** results-backend.tsv; SP/verify-tests-run2/defect-TestAuditIdentitySearchWithMACsUsesIndexes.log; SP/work/verify-B26/probe.txt (P5 Seq Scan 6,28 ms vs P6 BitmapOr 0,16 ms).
- **Abweichung:** Das Kriterium 'indexbasiert' ist für den dominanten Produktionsfall verletzt (P2). Die Doku (SR-04, lasttest-g1.md) untertreibt den Umfang.
- **Prüfebenen:** UX N/A; API OK; Daten LÜCKE (Org-Scan); E2E LÜCKE (kein Re-Discovery-Test auf dem Queue-Pfad); Robustheit LÜCKE (Indexnutzung nicht geprüft)
- **Abhängigkeiten:** WP-086, WP-203, SR-04, WP-333/334, WP-206
- **Defekte:** F-114 (P2) JSONB-Prädikate unter RLS nicht indexfähig: Attribut-Ausdrucksindex ungenutzt, Identitätssuche mit MACs als org-weiter Seq Scan, F-118 (P2) Der Nachweis zu NFR-03 ist nicht repräsentativ: Ingest nur mit neuen Geräten ohne MACs und Interfaces, keine Wiederentdeckung
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** TestAuditIdentitySearchWithMACsUsesIndexes besteht, oder eine E-Entscheidung akzeptiert die Ausnahme mit korrigiertem Umfang. Ein Re-Discovery-Test auf dem Queue-Pfad über 10k besteht.

<a id="wp-206"></a>
#### WP-206 – Lastlauf und Nachweis NFR-01/02/03 inkl. Budgetkorrekturen

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** NFR-10, NFR-01, NFR-02, NFR-03, ABN-02, CH15
- **Anforderung/Nutzerziel:** Ein Lastlauf auf der Referenzumgebung mit Bericht (p95 je Budget, 200/1.000 rec/s); verfehlte Budgets führen zu Folge-WPs.
- **Implementierung:** docs/acceptance/lasttest-g1.md:65-99, 208-263, 535-586; tests/load/*; Entscheidungen E-17, E-58 (entscheidungen.md:21)
- **Test(s):** Kein Repo-Test. k6 0.54.0, Seed 10k: Lauf 1 (Konkurrenzlast, load1 bis 12,7) api exit 99, list p95 717 ms ✗, search 581 ms ✗, übrige ✓; Ingest ✓ (12.000/0 fehlend, max 2,25 s); Spool ✓ (415 rec/s, 28,5 s). Replikat der Verifikation: list 646 ms ✗, search 534 ms ✗. Lauf 2 (ruhiger Host): alle 10 API-Budgets ✓ (list 450, search 348, topology 682, impact 1135, impact_full 1258 ms, bulk500 12,7 s).
- **Soll/Ist:** Der Bericht liegt vor, und E-58 akzeptiert 4-vCPU-Läufe für G1. List und search liegen in 2 von 3 Läufen über dem Budget, in Lauf 2 knapp darunter (450 ms). NFR-02 ist damit nicht robust belegt. 1.000 rec/s und die NFR-01-Volumina fehlen im Bericht; es gibt keinen dedizierten Referenzhost.
- **Evidenz:** audit/phase1-final/evidence/load/k6-api-run1.txt, k6-api-run2.txt, k6-ingest-run1.txt, k6-ingest-spool-run1.txt, run1.log, run2.log; SP/work/verify-B26/api-1.txt.
- **Abweichung:** Kriterium 1 teilweise (Referenzhost, 1.000/s, Volumina, nicht versionierte Artefakte); Kriterium 2 teilweise (kein Folge-WP für list/search).
- **Prüfebenen:** UX N/A; API OK; Daten OK (Seed + ANALYZE); E2E LÜCKE (Budget nicht reproduzierbar stabil); Robustheit LÜCKE (Ingest nur mit neuen Geräten ohne MACs)
- **Abhängigkeiten:** WP-204, WP-205, WP-154, WP-332..335, E-17, E-58, SR-06
- **Defekte:** F-116 (P2) Das Suchbudget nach NFR-02 wird am Namensfilter der CI-Liste gemessen statt an der globalen Suche; Szenarien für Attributfilter und Beziehungssuche fehlen, F-117 (P2) Die p95-Budgets nach NFR-02 für list und search werden nicht robust eingehalten (2 von 3 Läufen auf der akzeptierten 4-vCPU-Klasse verfehlt), F-118 (P2) Der Nachweis zu NFR-03 ist nicht repräsentativ: Ingest nur mit neuen Geräten ohne MACs und Interfaces, keine Wiederentdeckung, F-279 (P3) Der Lastbericht lässt 1.000 Records/s und die Volumina aus NFR-01 aus, F-280 (P3) TestCIQueryBudget ist planner- und umgebungsabhängig instabil (falscher Fehlschlag 'sequential scan')
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Drei saubere Läufe auf dem Referenzhost mit exit 0 und archivierten Artefakten. Szenario 'rediscover' mit MACs bei 200 rec/s. Bericht nennt 1.000/s und NFR-01. Folge-WPs mit ID.

<a id="wp-207"></a>
#### WP-207 – Konkurrenzmatrix: Discovery während Edit, Doppel-Ingest, paralleler Merge, Reservierung

- **Status:** BESTANDEN · **Priorität:** P3 · **Anforderungen:** ABN-03
- **Anforderung/Nutzerziel:** Integrationstests mit echten parallelen Transaktionen für jeden [B]-Fall aus ABN-03, danach ein Integritätsprüfskript.
- **Implementierung:** backend/internal/ci/concurrency_integration_test.go:30-276; backend/internal/discovery/concurrency_integration_test.go:66-448; lanes_integration_test.go:189-260; tests/integrity-check.sql; backend/cmd/server/main.go:251
- **Test(s):** PASS (Baseline und HEAD-Lauf): TestParallelMerge, TestAutomatedWriteKeepsOverride, TestOptimisticConcurrency, TestDiscoveryConcurrency, TestIngestLanesIdentityLock.
- **Soll/Ist:** Erfüllt mit ausgeführter Evidenz. Die Restmängel betreffen die Abdeckung des Skripts und einen dokumentierten Deployment-Hinweis (NATS).
- **Evidenz:** results-backend.tsv; final/test-db.json; SP/work/verify-B26/xtenant-probe3.txt, xtenant-probe4.txt.
- **Abweichung:** P3, außerhalb der verbindlichen Kriterien: integrity-check.sql mit vakuösen Duplikatprüfungen und ohne Cross-Tenant-Prüfung CI→location/type, läuft nicht nach load.yml. HTTP-Ingest ohne NATS nimmt keine Identitätssperren.
- **Prüfebenen:** UX N/A; API OK; Daten OK; E2E LÜCKE (kein Test mit zwei Server-Prozessen); Robustheit LÜCKE P3 (Skriptabdeckung)
- **Abhängigkeiten:** WP-061, WP-085, WP-173, WP-332..334, WP-054, E-03
- **Verknüpfte Befunde (verletzen kein verbindliches Kriterium dieses WP, siehe Abweichung):** F-281 (P3) integrity-check.sql: vakuöse Duplikatprüfungen, keine Prüfung schwacher Identitäten, keine Cross-Tenant-Prüfung CI→location/type, läuft nicht nach dem Lastlauf, F-282 (P3) Ohne RETICORA_NATS_URL nimmt der HTTP-Ingest keine Identitätssperren; zwei Instanzen können Geräte mit schwacher Identität duplizieren
- **Statusverlauf:** Einzelprüfung BESTANDEN → Verifikation BESTANDEN → final BESTANDEN
- **Offene Nachweise / Retest:** Keine für BESTANDEN. Härtung: Negativ-Selbsttest des Skripts, Integritätsschritt in load.yml, Produktionsschutz ohne RETICORA_NATS_URL.

### Sicherheitsreview

<a id="wp-208"></a>
#### WP-208 – Kanal-Isolationsmatrix vollständig (TEN-09, TST-02)

- **Status:** FEHLGESCHLAGEN · **Priorität:** P0 · **Anforderungen:** TEN-09, TST-02
- **Anforderung/Nutzerziel:** Mandantentrennung über alle Zugriffskanäle (REST, GraphQL, Suche, Export, Webhook, Notification, Job, Review, Location, Ingest) für alle Scope-Arten beim Lesen und Schreiben nachweisen; TST-02-Szenarien als Integrationstests gegen echte Dienste.
- **Implementierung:** backend/internal/tenant/rls/channel_matrix_integration_test.go:118 TestChannelMatrix (Lesen :240-265, Detail/Topologie/Impact :267-292, Sites :294-309, org-weite Kanäle nur GET :311-345, Schreiben :347-387, Fremdverweise :389-437, Ingest :439-457), :477 TestClientReferencesCarryOrganization; matrix_fixtures.go:16 rls.Channels (ungenutzt); backend/internal/server/e2e_scenarios_integration_test.go:39 TestIngestToWebhook, :149 TestTST02ScenarioCoverage; Defektstellen backend/internal/rack/pg_repository.go:58-60 (rackVisible), :247-252 (ListMounts), topology/topology.go:273-276.
- **Test(s):** Baseline PASS: TestChannelMatrix, TestClientReferencesCarryOrganization, TestRLSMatrix, TestIngestToWebhook, TestTST02ScenarioCoverage und TST-02-Zieltests (TestIdempotencyIsAtomicAndPrincipalBound, TestCollectorNATSEndpointAuthorization, TestCollectorPKI, TestNeedsRenewal, TestMultiImpactAgainstPostgres, TestUnlicensedCIs, TestEntitlementEnforcement, TestOfflineDetector, TestSchedulerReplicasAgainstPostgres, TestEmailOverSMTP, TestAttributeIndexJob); Probes TestVerifyB27RackSiteScope, TestVerifyB27Matrix, TestVerifyB27NegativeStatus, TestVerifyB27TeamScope. Welle (Commit 0e02557) PASS: TestAuditT1OrgWideChannelWrites, TestAuditT1LocationChannelBelowSites, TestAuditT1TopologyChannelInvisibleRoots, TestAuditT1GraphQLFieldsHonourScope; FAILS_AS_EXPECTED: TestAuditT1RackChannelSiteScope, TestAuditT1ScopeRefusalsAreNot5xx, TestAuditT1RepositoryErrorsAreSanitized. Real-Stack tenant-isolation.spec.ts T01–T13 PASS, T14/T15/T16 FAIL.
- **Soll/Ist:** die Welle ergänzt org-weite Schreibkanäle (Webhooks, Jobs cancel/revert, Review-Resolve nur für Items mit Kandidaten, Notifications), die GraphQL-Felder ci, relationships, topology, searchEverything, reviewInbox (nur Items mit Kandidaten) und jobs (rackLayout nur im roten Defekttest), Locations unterhalb Sites (Client/Org) und unsichtbare Wurzeln als grüne Regressionstests; Review-Kanal (L-T1-03) und Job-Kanal (L-T1-23) leaken clientübergreifend
- **Evidenz:** SP/work/verify-B27/rack-site-scope.log (site2: GET /racks enthält site1-Rack, Detail/Layout/GraphQL 200, DELETE 204, rows left 0, location rows 1); SP/verify-tests-run2/defect-TestAuditT1RackChannelSiteScope.log; defect-TestAuditT1ScopeRefusalsAreNot5xx.log; SP/work/t1-tests/untagged.log (4 Regressionstests PASS); SP/work/tenant-iso-final.log ('3 failed, 13 passed').
- **Abweichung:** AK1 'no': TEN-09 verletzt durch Cross-Site-Lese-/Löschpfad im Rack-Kanal (L-T1-02, P1); Matrix-Lücken (L-T1-16, P2); 500 statt 404 bei Topologie-Wurzel (L-T1-17, P3) und Fremdkante (ext T14); Mounts-Existenzorakel (L-T1-18, P3); Team-Mitglied-Schreiben mit SQL-Text im 404 (Teil von L-T1-05); TST-02-Meta-Zuordnung auf Unit-Test (L-T1-33, P3). UI-CI-Anlage (ext T16) blockiert den UI->DB-Isolationsschritt.
- **Prüfebenen:** UX: LÜCKE – Rack-Leck UI-relevant (api/client.ts:741 /racks); Real-Stack T10 belegt UI-Isolation nur für Org. API: FEHLER – Site-Bypass im Rack-Kanal; 500 bei Topologie/Fremdkante. Daten: FEHLER – Cross-Site-DELETE entfernt rack, location verwaist. E2E: FEHLER – Real-Stack T14/T15/T16 rot (T01–T13 grün). Robustheit: LÜCKE – Matrix akzeptiert jeden Nicht-2xx-Code; Team-/Ingest-Scope-Arten unvollständig.
- **Abhängigkeiten:** WP-065, WP-185, WP-189, WP-140, WP-145, WP-157, WP-131, WP-180, WP-073; Rack-Repository WP-014; Gate GATE-03 (WP-213).
- **Defekte:** F-002 (P0) Site-Scope-Bypass im Rack-Kanal: site-fremde Racks lesbar und löschbar, F-003 (P0) Review-Items ohne Kandidaten-CIs leaken über Clients; review_item ohne DB-seitigen Client/Site-Scope, F-047 (P2) Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück, F-058 (P2) Kanalmatrix deckt Locations unterhalb von Sites, org-weite Schreibpfade, GraphQL-Felder außer cis und exakte Negativstatus nicht ab, F-059 (P2) In das generische Job-Modell gespiegelte Export-Jobs sind über GET /api/v1/jobs und /jobs/{id} für andere Nutzer und Clients sichtbar, F-145 (P2) POST /relationships mit CI einer anderen Organisation oder unbekannter CI-ID antwortet 500 statt 404/422, F-161 (P2) GET /topology mit unbekannter oder fremder root_ci_id antwortet 500 statt 404, F-180 (P3) GET /api/v1/racks/{id}/mounts eines Racks außerhalb des Scopes antwortet 200 (leer) statt 404 (Existenzorakel), F-194 (P3) TST-02-Meta-Test ordnet die Zertifikatserneuerung einem Unit-Test zu und prüft nur Funktionsnamen, extern: CI creation form ('CI erstellen') sends the type key 'server' as ci_type_id; every manual CI creation through the UI fails with 400
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** TestAuditT1RackChannelSiteScope und TestAuditT1ScopeRefusalsAreNot5xx grün; TestChannelMatrix tabellengetrieben aus rls.Channels um Racks/Rooms/Buildings/Mounts/rackLayout × alle Scopes, org-weite Schreibpfade, alle GraphQL-Felder und exakte 404/422 erweitert und grün; Real-Stack T14/T15/T16 grün; TST-02-Zuordnung auf TestCollectorPKI.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Folgt aus L-T1-02 und L-T1-03 (beide auf P0 korrigiert).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – defekte: „["L-T1-02","L-T1-05","L-T1-16","L-T1-17","L-T1-18","L-T1-33","ext:POST /relationships with a CI of another organization (or any unknown CI id) answers 500 Internal Error instead of 404/422","ext:CI creation form ('CI erstellen') sends the type key 'server' as ci_type_id; every manual CI creation through the UI fails with 400"]“ → „["L-T1-02","L-T1-03","L-T1-05","L-T1-16","L-T1-17","L-T1-18","L-T1-23","L-T1-33","ext:POST /relationships with a CI of another organization (or any unknown CI id) answers 500 Internal Error instead of 404/422","ext:CI creation form ('CI erstellen') sends the type key 'server' as ci_type_id; every manual CI creation through the UI fails with 400"]“. WP-208 AK1 nennt ausdrücklich die Kanäle Review und Job; TEN-09 nennt Review-Items und Jobs. L-T1-03 ist eine Cross-Client-Exposition im Review-Kanal (REST /review-items, Resolve, GraphQL reviewInbox), L-T1-23 eine im Job-Kanal (GET /api/v1/jobs, /jobs/{id}). Beide sind bestätigt und gehören zur Kanalmatrix-Verletzung wie L-T1-02 im Rack-Kanal. Die Abweichung ist entsprechend zu ergänzen: 'Cross-Client-Lese-/Verwerfpfad im Review-Kanal (L-T1-03, P0); Job-Kanal legt Export-Job-Metadaten clientübergreifend offen (L-T1-23, P2)'.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – soll_ist: „die Welle ergänzt org-weite Schreibkanäle (Webhooks, Jobs cancel/revert, Review-Resolve, Notifications), alle GraphQL-Query-Felder, Locations unterhalb Sites (Client/Org) und unsichtbare Wurzeln als grüne Regressionstests“ → „die Welle ergänzt org-weite Schreibkanäle (Webhooks, Jobs cancel/revert, Review-Resolve nur für Items mit Kandidaten, Notifications), die GraphQL-Felder ci, relationships, topology, searchEverything, reviewInbox (nur Items mit Kandidaten) und jobs (rackLayout nur im roten Defekttest), Locations unterhalb Sites (Client/Org) und unsichtbare Wurzeln als grüne Regressionstests; Review-Kanal (L-T1-03) und Job-Kanal (L-T1-23) leaken clientübergreifend“. 'alle GraphQL-Query-Felder' ist falsch: audit_t1_graphql_scope_integration_test.go deckt rackLayout nicht ab (Kommentar Z. 12) und seedet für reviewInbox nur ein Item mit Kandidat (Z. 42). Genau diese Felder leaken (L-T1-02, L-T1-03). Auch der grüne Review-Resolve-Test widerspricht L-T1-03 nur scheinbar, weil er Items mit Kandidaten verwendet.

<a id="wp-209"></a>
#### WP-209 – Internes Sicherheitsreview Phase 1

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P1 · **Anforderungen:** GATE-03, SEC-11
- **Anforderung/Nutzerziel:** Vor G1 liegt ein dokumentiertes internes Sicherheitsreview vor: Threat-Model-Abgleich, Prüfung aller Critical/High-Befunde auf Schließung mit WP-Verweis, Scanergebnisse und neue Befunde mit Severity.
- **Implementierung:** docs/security/review-g1.md (Ergebnis :22-66, Scanergebnisse :98-113 mit Run 38510025931 :100, RC-Lauf 'steht aus' :60/:113, ASVS :115-130, SR-01..SR-11 :132-146 mit SR-11 Low :145, Anhang 225 Critical/High); docs/security/threat-model.md (STRIDE, T1–T20); docs/security/abhaengigkeitsscans.md; CI-Jobs govulncheck :370, npm-audit :415, image-scan :455 in .github/workflows/ci.yml.
- **Test(s):** Keine automatisierten Tests (Review-Dokument). Verifier: Python-Abgleich docs/audit/befunde.csv (Critical/High) ↔ Anhang 225/225, fehlend [] überzählig []; npm audit (SP/work/verify-B01/wp004) prod critical 0/high 0, alle critical 2 (tinypool, vitest)/high 10; curl vuln.go.dev und dl-cdn.alpinelinux.org 403; SR-11-Probe mit Kontrollgruppe (SP/work/verify-B27/sr11-verify.log: bare-pg Restore true/200, main.go-Kette 501); Rack-Site-Scope-Probe (SP/work/verify-B27/rack-site-scope.log). Defekttest-Welle T5: TestAuditT5CIReactivationThroughProductionDecoratorChain (fails-as-expected, mechanisch bestätigt, Kontrolle bare-pg PASS); g1-gate-evidence known_defect_SR-11 (2 Assertions FAIL: CI-13 PASS, severity Low) und rack_site_scope FAIL – in dieser Synthese erneut ausgeführt (SP/work/synth-T5/g1-gate-evidence.log).
- **Soll/Ist:** SOLL: dokumentiertes Review mit Threat-Model-Abgleich, Prüfung aller Critical/High, Scanergebnissen und vollständigen neuen Befunden mit Severity. IST: alle Abschnitte vorhanden, Befundabdeckung vollständig (225/225). Scans stammen aus einem älteren Lauf, RC-Lauf steht aus. SR-11 entgegen dem eigenen Schema (docs/audit/README.md:14) als Low eingestuft, obwohl CI-13 [B] in der Produktion 501 liefert; ein reproduzierbarer Site-Scope-Bypass bei Racks fehlt, die Kanalmatrix-Abdeckung (V4, SR-03) ist überzeichnet.
- **Evidenz:** Python-Abgleich 225/225; review-g1.md:60, :100, :113, :145 und SR-03-Zeile; docs/audit/README.md:14; npm-audit-all.json/-prod.json; curl 403; SP/work/verify-B27/sr11-verify.log, rack-site-scope.log; SP/work/synth-T5/g1-gate-evidence.log.
- **Abweichung:** AK2 teilweise verletzt: SR-11 unterbewertet (L-T5-08), Rack-Site-Scope-Bypass nicht als Befund geführt und Matrixabdeckung überzeichnet (L-T5-09; Produktdefekt ext. T1/WP-208). AK1 teilweise: kein Scan-Nachweis auf dem RC (L-T5-12); P3: Dev-Critical nicht bewertet (L-T5-44). Mittelbar betroffen vom Produktdefekt SR-11 (L-T5-05).
- **Prüfebenen:** UX: N/A (Dokument-WP); API: N/A (Dokument-WP; stichprobenhaft verifiziert: SR-11 per Router-Probe mit Kontrollgruppe, Rack-Site-Scope per Probe); Daten: N/A; E2E: N/A; Robustheit: LÜCKE (Scan-Nachweise nicht für den RC, lokal nur npm reproduzierbar; SR-11 unterbewertet; Scope-Isolationsdefekt vom Review nicht gefunden)
- **Abhängigkeiten:** WP-208, WP-206, WP-102, WP-166, WP-184, WP-004; WP-210 (Pentest); WP-087 (SR-11), WP-025/WP-027 (Rack-Scope)
- **Defekte:** F-002 (P0) Site-Scope-Bypass im Rack-Kanal: site-fremde Racks lesbar und löschbar, F-040 (P1) Review und Gate führen TEN-09-Isolation als vollständig und 'kein offenes Critical', obwohl ein Site-Scope-Bypass im Rack-Kanal reproduziert ist, F-119 (P2) Internes Review stuft SR-11 als 'Low' ein, obwohl CI-13 [B] in der Produktionsverdrahtung 501 liefert, F-122 (P2) Kein CI-, Install-Smoke- oder Scan-Lauf auf dem aktuellen Release-Kandidaten dokumentiert; G1-Nachweise beziehen sich auf ältere Commits, F-297 (P3) Internes Review bewertet die Critical/High-Advisories der Dev-Abhängigkeiten nicht
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Scan-Lauf auf dem RC grün und mit Run-ID in review-g1.md; SR-11 behoben und per main.go-Kettentest belegt (TestAuditT5CIReactivationThroughProductionDecoratorChain grün) oder als High bewertet und im Gate als Abweichung geführt (known_defect_SR-11 grün); Rack-Site-Scope-Defekt als Befund aufgenommen und behoben (WP-208-Test grün, rack_site_scope grün); Dev-Critical bewertet.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P2“ → „P1“. Folgt aus der Korrektur von L-T5-09 auf P1. L-T5-09 ist WP-209 zugeordnet, und die WP-Priorität ist die höchste Priorität ihrer Abweichungen. Alternative: L-T5-09 aufteilen in einen Gate-Defekt (P1, nur WP-213) und einen Review-Defekt (P2, WP-209, fehlender Befund und überzeichnete Kanalmatrix). Dann bleibt WP-209 bei P2.

<a id="wp-210"></a>
#### WP-210 – Externer Pentest und Nachweis ohne offene Critical/High

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** GATE-03
- **Anforderung/Nutzerziel:** Ein externer Pentest ist durchgeführt, Zusammenfassung und Befundliste sind abgelegt, jeder Befund hat ein WP, und es sind keine Critical/High mehr offen (GATE-03).
- **Implementierung:** docs/security/pentest-g1.md: Status :6-27 (alle Punkte 'laut Erklärung des Auftraggebers vom 2026-10-15; nachzureichen', :8-13 'Diese Datei enthält keine Angaben aus dem Pentest'), vorgeschlagener Umfang :29-50, Ablage :52-57; keine PT-Befundzeile (grep -c 'PT-' = 0); docs/acceptance/g1-gate.md:65 (B3) und GATE-03-Tabelle (PASS per Erklärung).
- **Test(s):** Keine. tests/rep01-structure.test.sh:57-62 prüft nur Zeilen mit exakt 'Critical' und ist bei leerer Pentest-Liste trivial grün (SP/runs/rep01.log 47/47); Verifier: '| PT-01 | High | offen |' bleibt grün (SP/work/verify-B27/rep01-negative.log). Defekttest-Welle T5: g1-gate-evidence.test.sh pentest_summary, pentest_findings, pentest_field ×3 (Dienstleister, Zeitraum|Termin, Nachtest) FAIL; pentest_open ok nur mangels PT-Zeilen – in dieser Synthese erneut ausgeführt (SP/work/synth-T5/g1-gate-evidence.log).
- **Soll/Ist:** SOLL: abgelegte Zusammenfassung mit Befundliste, Folge-WPs und Nachtest ohne offene Critical/High; Abschluss erst danach. IST: nur eine Erklärung des Auftraggebers, alle Angaben 'nachzureichen'; GATE-03 steht dennoch auf PASS. Interne Audit-Probes belegen zudem offene Defekte, die nach eigenem Schema High/Critical wären (SR-11, Rack-Site-Scope).
- **Evidenz:** docs/security/pentest-g1.md:8-27 (grep PT- = 0); docs/acceptance/g1-gate.md:65 und GATE-03-Tabelle; SP/work/verify-B27/rep01-negative.log; SP/work/synth-T5/g1-gate-evidence.log (pentest_* rot).
- **Abweichung:** Einziges AK verletzt: Bericht nicht abgelegt, keine Befundliste, kein Nachtest, Abschluss per Erklärung (L-T5-07, L-T5-10). P3: rep01-Prüfung erkennt offene High/Formatvarianten nicht (L-T5-47).
- **Prüfebenen:** UX: N/A; API: N/A; Daten: N/A; E2E: N/A; Robustheit: FEHLER (kein Pentest-Nachweis; GATE-01/03-Prüfung erkennt offene High und Critical in anderer Schreibweise nicht, reproduziert)
- **Abhängigkeiten:** WP-209; externe Aufgabe A-07 (Auftraggeber), E-28
- **Defekte:** F-039 (P1) Kein Pentest-Bericht und keine Befundliste im Repository (Lieferung A-07 fehlt), F-120 (P2) Gate-Checkliste und Abnahmeprotokoll führen Anforderungen allein per Erklärung des Auftraggebers als PASS, F-300 (P3) rep01-GATE-01-Prüfung erkennt Formatvarianten, Anhangzeilen und offene High-Befunde nicht
- **Statusverlauf:** Einzelprüfung NICHT TESTBAR → Verifikation NICHT TESTBAR → final FEHLGESCHLAGEN; NICHT TESTBAR → FEHLGESCHLAGEN. Begründung: Die externe Abhängigkeit (Lieferung A-07) blockiert die Behebung, nicht die Prüfung. Das einzige AK verlangt 'Bericht abgelegt' und 'Abschluss erst ohne offene Critical/High'; pentest-g1.md:8-27 belegt nachprüfbar das Gegenteil (alle Angaben 'nachzureichen', 0 PT-Zeilen), während GATE-03 in g1-gate.md auf PASS steht. Der bestätigte P1-Defekt L-T5-07 ist durch ausgeführte Defekttests (g1-gate-evidence pentest_* rot, in dieser Synthese wiederholt) als beobachtete Verletzung reproduziert; nach den Statusregeln ⇒ FEHLGESCHLAGEN.
- **Offene Nachweise / Retest:** Pentest-Zusammenfassung mit Dienstleister, Zeitraum, Umfang, Befundliste (ID, Severity, Status, Folge-WP) und Nachtest-Ergebnis abgelegt, kein Critical/High offen (g1-gate-evidence pentest_* grün); erweiterte rep01-Prüfung (GATE-03, High+Critical normalisiert) grün; GATE-03 bis dahin nicht als PASS.

### Gate G1

<a id="wp-211"></a>
#### WP-211 – G1-Nachweis Installation auf beiden Referenzplattformen

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** GATE-03, INS-01
- **Anforderung/Nutzerziel:** Für den Release-Kandidaten belegen grüne Läufe von install-smoke (Ubuntu 22.04/24.04, Debian 12) und der Onboarding-E2E ohne Mocks, dass das Produkt auf den Referenzplattformen installierbar und nutzbar ist.
- **Implementierung:** docs/acceptance/g1-installation.md:9-21 (RC 1b6671f, Run 38512314064; Nachlauf 37752e1, Run 38513566388); .github/workflows/install-smoke.yml:7-41 (Pfadfilter für push/PR ohne backend/** und frontend/src/**; Tags v* und workflow_dispatch ungefiltert); frontend/e2e/onboarding.spec.ts; frontend/e2e/fixtures/setup-onboarding.sh.
- **Test(s):** SP/runs/e2e-onboarding-real.log: 6/6 pass gegen den echten nativen Stack auf HEAD (u. a. 'self-signup, verification, trial, login and dashboard' 4.7s; 'collector, scopes, 30 simulated devices, review, impact and export' 25.3s; einmal transient 'upload failed with status 401', Test grün). SP/runs/install.log INSTALL_EXIT=1 (apk 'exit code: 99'). Shell-Tests install-cloud-helpers 36/36, install-preflight 29/29, install-smoke-health 12/12. CI-Runs 38512314064/38513566388 nicht einsehbar. Defekttest-Welle T5: g1-gate-evidence install_evidence_current FAIL ('last product change: 3a9669a …; commits named in g1-installation.md: 1b6671f… 37752e1'), in dieser Synthese erneut ausgeführt.
- **Soll/Ist:** SOLL: grüne install-smoke-Matrix und Onboarding-E2E auf dem Release-Kandidaten, dokumentiert. IST: grüne Läufe für einen älteren Kandidaten dokumentiert; danach änderten sich 68 Backend-/Frontend-Dateien (+12086/-808), aber keine Installer-, Compose- oder Migrationsdateien (nur deploy/k8s/base/deployment.yaml). Paketierte Installation lokal nicht ausführbar; Onboarding-E2E auf HEAD gegen den nativen Stack grün.
- **Evidenz:** SP/runs/e2e-onboarding-real.log (6 passed); SP/runs/install.log (INSTALL_EXIT=1); curl dl-cdn.alpinelinux.org 403; git diff --shortstat fa76b16 3a9669a -- backend frontend (68 Dateien); git tag leer; git log -- docs/acceptance/g1-installation.md (nur fa76b16); SP/work/synth-T5/g1-gate-evidence.log.
- **Abweichung:** AK teilweise verletzt: Installationsnachweis gilt nicht für den aktuellen RC (L-T5-12). Paketierte Installation lokal nicht prüfbar (Netzwerkrichtlinie).
- **Prüfebenen:** UX: OK (Onboarding-UI real durchlaufen: Signup mit Mail-Catcher, Dashboard, Collector, Review; INS-04-Fehlerbilder in Tests 3 und 4); API: OK (Same-Origin-API und PKCE-Redirect real geprüft); Daten: OK (30 simulierte Geräte als CIs sichtbar, Export >= 30 Zeilen); E2E: LÜCKE (UI → API → DB auf nativem Stack real belegt, paketierte Installation mit Images/Compose/nginx für den RC nicht); Robustheit: LÜCKE (Preflight-Negativfälle per Shell-Test; Plattformmatrix für den RC unbelegt)
- **Abhängigkeiten:** WP-092, WP-093, WP-200; CI-Lauf install-smoke.yml auf dem RC (extern beobachtbar)
- **Defekte:** F-122 (P2) Kein CI-, Install-Smoke- oder Scan-Lauf auf dem aktuellen Release-Kandidaten dokumentiert; G1-Nachweise beziehen sich auf ältere Commits
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** Grüner install-smoke-Lauf (3 Plattformen + Onboarding-E2E) auf dem aktuellen RC-Commit mit Run-ID in g1-installation.md, für Auditoren einsehbar (g1-gate-evidence install_evidence_current grün); lokal wegen Netzwerkrichtlinie nicht möglich.

<a id="wp-212"></a>
#### WP-212 – Abnahmeszenarien ABN-01 [B] mit eingefrorenen Artefakten

- **Status:** TEILWEISE BESTANDEN · **Priorität:** P2 · **Anforderungen:** ABN-01
- **Anforderung/Nutzerziel:** Die Abnahmeszenarien Onboarding (d) und Konflikt (e) sind als Protokolle mit eingefrorenen Daten nachvollziehbar bestanden; (a) und (b) folgen E-02.
- **Implementierung:** docs/acceptance/abn-01.md (Status :9-15, (a)/(b) per Erklärung :11-12, (d) :44-67, (e) :69-102 mit 'eingespielt' :90, Freigabe :104ff); docs/acceptance/artefakte/{abn-01-e-konflikt.json, SHA256SUMS}; frontend/e2e/conflict.spec.ts (mockBackend :80, Artefaktdatei :35); frontend/e2e/onboarding.spec.ts (echter Stack; Impact-Assertion :292-297); backend/internal/reviews/conflict_integration_test.go TestOverrideConflictOnPostgreSQL; tests/rep01-structure.test.sh:94-97 (Prüfsummen).
- **Test(s):** SP/runs/e2e-mocked.log: conflict.spec.ts pass [chromium], [mobile-chrome] (gemocktes Backend; Firefox/WebKit/Edge lokal nicht ausführbar). SP/runs/e2e-onboarding-real.log 6/6 pass. results-backend.tsv pass: reviews TestOverrideConflictOnPostgreSQL, TestOverrideConflictLifecycle, TestConflictingValuesKeepTheReportedValue; ci TestAutomatedWriteKeepsOverride; discovery TestDiscoveryConcurrency. sha256sum -c docs/acceptance/artefakte/SHA256SUMS 4/4 OK. Real-Stack-Audit-Spec frontend/e2e-real/discovery-override.spec.ts D03–D05 pass (SP/work/verify-discovery-override/full.log; D07 FAIL = Fremdbefund WP-060/Gruppe T3, nicht Teil von (e)). Defekttest-Welle T5: g1-gate-evidence abn01_texts FAIL (abn-01-a-cmdb.md, abn-01-b-system.md fehlen), pass_by_declaration FAIL – in dieser Synthese erneut ausgeführt.
- **Soll/Ist:** SOLL: Abnahmeprotokolle (d) und (e) mit eingefrorenen Daten nachweislich bestanden; (a)/(b) nach E-02 übernommen. IST: (d) real bestanden und eingefroren (schwache Impact-Assertion). Funktion von (e) per Real-Stack-Audit-Spec belegt, das eingefrorene Protokoll stützt sich aber auf Mocks. (a)/(b) nur per Erklärung abgenommen, Texte fehlen.
- **Evidenz:** SP/runs/e2e-mocked.log; SP/runs/e2e-onboarding-real.log 6/6; results-backend.tsv (Konflikttests pass); sha256sum -c 4/4 OK; SP/work/verify-discovery-override/full.log (D03–D06, D08 pass); abn-01.md:11-12, :74, :90; SP/work/synth-T5/g1-gate-evidence.log.
- **Abweichung:** AK teilweise verletzt: (a)/(b) nicht nachgewiesen, Texte fehlen (L-T5-28), Status PASS per Erklärung (L-T5-10). P3: Protokoll (e) referenziert nur den gemockten Lauf (L-T5-45); Impact-Schritt in (d) ohne inhaltliche Assertion (L-T5-46).
- **Prüfebenen:** UX: OK (Override-Dialog, Konfliktanzeige, Übernehmen/Verwerfen auf echtem Stack bedient, D03–D05; Onboarding real); API: OK (PUT override, GET provenance, POST resolve real genutzt, zweites Auflösen 4xx); Daten: OK (accept/dismiss nach Reload und erneuter Discovery per API belegt, zusätzlich gegen PG); E2E: LÜCKE (für (e) real belegt, aber außerhalb des eingefrorenen Protokolls; (d) nicht auf paketierter Installation); Robustheit: LÜCKE (kein UI-Negativfall ohne review:resolve; Impact ohne inhaltliche Assertion; Firefox/WebKit/Edge unverifiziert)
- **Abhängigkeiten:** WP-201, WP-200, WP-089; E-02 (offen, v2-Texte durch Auftraggeber); WP-211 (paketierte Installation)
- **Defekte:** F-120 (P2) Gate-Checkliste und Abnahmeprotokoll führen Anforderungen allein per Erklärung des Auftraggebers als PASS, F-138 (P2) ABN-01 (a)/(b): v2-Originaltexte fehlen und sind nicht eingefroren (E-02/A-02 offen), F-298 (P3) ABN-01 (e)-Abnahmeprotokoll stützt sich auf gemockten Lauf; Real-Stack-Nachweis nicht referenziert, F-299 (P3) Onboarding-Impact-Schritt prüft keine betroffenen CIs
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation TEILWEISE BESTANDEN → final TEILWEISE BESTANDEN
- **Offene Nachweise / Retest:** (a)/(b): v2-Texte eingefroren (in SHA256SUMS) und jeder Schritt mit Ergebnis protokolliert, oder ausdrücklich außerhalb von PASS geführt (abn01_texts grün); (e): Protokoll referenziert grünen Real-Stack-Lauf (D05 einbinden, Artefaktdaten nutzen, Prüfsumme neu); (d): Impact-Assertion impacted.length >= 1 mit erwarteter ID.

<a id="wp-213"></a>
#### WP-213 – G1-Gateprüfung: Traceability blockierend, REP-01-Struktur, Gate-Checkliste

- **Status:** FEHLGESCHLAGEN · **Priorität:** P1 · **Anforderungen:** DOD-01, GATE-01, GATE-02, GATE-05, GLO-11, REP-01, SEQ-01
- **Anforderung/Nutzerziel:** Die G1-Gateprüfung ist automatisiert und prüfbar: Traceability blockiert fehlende [B]/[Q]-IDs, die REP-01-Struktur ist getestet, eine Checkliste belegt GATE-01/02/05 und SEQ-01 mit dem Status jeder ID, Spätphasen-Module sind nachweislich abgeschaltet.
- **Implementierung:** backend/internal/traceability/traceability.go (MissingForGate, RequiredForGate/gateTag); traceability_test.go:260 TestTraceabilityFile (Gate-Fehler :296-300, Validate nur Existenz :285-288); .github/workflows/ci.yml (Jobs traceability und gate ohne continue-on-error); tests/rep01-structure.test.sh (REP-01 :36-48, GATE-01 :57-62 nur '(SR|PT)-nn | Critical |', GATE-02 :64-79, GATE-05 :81-85, SEQ-01 :87-92, ABN-01 :94-97, DOD-01/GLO-11 :99-107); docs/acceptance/g1-gate.md (Ergebnis :38-62 'G1 … bestanden', :55 'Es gibt kein offenes Critical', :30-31 RC-CI-Lauf steht aus, :154-170 PASS per Erklärung, :333 CI-13 PASS); backend/internal/server/modules.go; backend/cmd/server/main.go:355 (KI-Decorator vor ParseModules :404), :412 WorkerAllowed, :477 inventory-Sweeper.
- **Test(s):** results-backend.tsv pass: traceability TestTraceabilityFile, TestRequiredForGateSelectsBaselineAndQualityTags, TestMissingListsRequiredIDsWithoutRow; server TestG1DefaultSwitchesLateModulesOff, TestLateRoutesAreMarkedInSpecification, TestModulesEndpointListsTheReleaseModules; SP/runs/rep01.log 47/47. Verifier (SP/work/verify-B27/): Traceability ohne REP-01/TEN-09 FAIL 217/219, TEN-09 nur auf g1-gate.md PASS 219/219 (trace-negative.log); rep01 mit High/**Critical**/Kritisch/critical 47/0, nur 'Critical' 46/1 (rep01-negative.log); Route-Walk 313 Routen, 0 aus Spätphasen-Modulen, 'all' 517/204 (route-walk.log); sr11-verify.log; ai-chunk.log; rack-site-scope.log. Defekttest-Welle T5: TestAuditT5CIReactivationThroughProductionDecoratorChain, TestAuditT5GateIDsAreNotTracedByTheChecklistItself (fails-as-expected, mechanisch bestätigt); g1-gate-evidence.test.sh rep01_* (7 FAIL, 4 Kontrollen ok), known_defect_SR-11, rack_site_scope, pass_by_declaration, install_evidence_current FAIL – in dieser Synthese erneut: '5 passed, 20 failed' (SP/work/synth-T5/g1-gate-evidence.log); Sanity: rep01-structure.test.sh weiterhin 47/47.
- **Soll/Ist:** SOLL: blockierende Traceability, REP-01-Strukturtest, belastbare Gate-Checkliste mit PASS-Nachweis je ID per Test/Traceability, nachgewiesene Abschaltung der Spätphasen-Module. IST: Traceability-Blockade, REP-01-Test und Modulabschaltung funktionieren (reproduziert). Die Checkliste erklärt G1 aber für bestanden, obwohl eine [B]-Funktion (CI-13) in der Produktion defekt ist und ein Scope-Isolationsdefekt (TEN-09) offen ist; weitere PASS beruhen nur auf Erklärungen oder Selbstverweisen, der RC-CI-Lauf steht aus.
- **Evidenz:** SP/work/verify-B27/sr11-verify.log (bare-pg 200, main.go 501, CI gelöscht, Review open, GET 404); rack-site-scope.log; trace-negative.log; rep01-negative.log; route-walk.log; ai-chunk.log; g1-gate.md:30-31, :38-62, :154-170, :333; docs/audit/README.md:14; results-backend.tsv; SP/runs/rep01.log; SP/work/synth-T5/g1-gate-evidence.log.
- **Abweichung:** AK3 verletzt (GATE-02): CI-13 [B] PASS trotz Produktionsdefekt (L-T5-05, L-T5-06); 'kein offenes Critical'/TEN-09 vollständig trotz Rack-Bypass (L-T5-09); PASS per Erklärung (L-T5-10), selbstreferenzielle Traceability (L-T5-11), RC-CI-Lauf ausstehend (L-T5-12). P3: rep01-GATE-01-Prüfung textuell umgehbar (L-T5-47); KI-Chunk-Indexer in G1 aktiv (L-T5-48). AK1, AK2, AK4 erfüllt (mit Einschränkung AK1: Selbstverweise erfüllen die Blockade).
- **Prüfebenen:** UX: N/A (Gate-Prüfung ohne UI); API: OK (Route-Gate über authorizingRouter authz.go:550, Route-Walk 313/0); Daten: N/A (Beobachtung: ai_chunk-Schreibzugriffe in G1, P3); E2E: N/A; Robustheit: FEHLER (Checkliste meldet 'bestanden' trotz reproduzierter Defekte in [B]/[Q]-Pfaden; GATE-01 textuell umgehbar; Traceability akzeptiert Selbstverweise)
- **Abhängigkeiten:** WP-210 (Pentest), WP-211 (RC-Installationsnachweis), WP-212, WP-214, WP-087 (CI-13/SR-11), WP-208 (TEN-09/Racks), WP-005; E-02/E-35 (Lieferungen des Auftraggebers)
- **Defekte:** F-002 (P0) Site-Scope-Bypass im Rack-Kanal: site-fremde Racks lesbar und löschbar, F-037 (P1) CI-13-Reaktivierung über die Produktions-Decorator-Kette aus main.go liefert 501; gelöschtes, wiedergesehenes Gerät bleibt gelöscht (SR-11), F-038 (P1) Gate-Checkliste führt CI-13 [B] als PASS trotz offenem und reproduziertem Defekt SR-11, F-040 (P1) Review und Gate führen TEN-09-Isolation als vollständig und 'kein offenes Critical', obwohl ein Site-Scope-Bypass im Rack-Kanal reproduziert ist, F-120 (P2) Gate-Checkliste und Abnahmeprotokoll führen Anforderungen allein per Erklärung des Auftraggebers als PASS, F-121 (P2) Traceability-Prüfung akzeptiert jeden existierenden Pfad als 'testdatei'; G1-Vollständigkeit 219/219 nur über Selbstverweise auf die Gate-Checkliste, F-122 (P2) Kein CI-, Install-Smoke- oder Scan-Lauf auf dem aktuellen Release-Kandidaten dokumentiert; G1-Nachweise beziehen sich auf ältere Commits, F-300 (P3) rep01-GATE-01-Prüfung erkennt Formatvarianten, Anhangzeilen und offene High-Befunde nicht, F-301 (P3) KI-Chunk-Indexer (Add-on 'ai') ist in der G1-Voreinstellung aktiv
- **Statusverlauf:** Einzelprüfung TEILWEISE BESTANDEN → Verifikation FEHLGESCHLAGEN → final FEHLGESCHLAGEN
- **Offene Nachweise / Retest:** SR-11 behoben und per main.go-Kettentest belegt (TestAuditT5CIReactivationThroughProductionDecoratorChain grün) oder CI-13 im Gate auf FAIL und Gate-Ergebnis angepasst; Rack-Site-Scope-Defekt behoben (WP-208-Test grün) bzw. im Gate geführt; PASS per Erklärung durch Nachweise ersetzt oder außerhalb von PASS geführt; RC-CI-Lauf und RC-Install-Smoke dokumentiert; g1-gate-evidence.test.sh vollständig grün; erweiterte rep01-Prüfungen (Severity-Normalisierung, GATE-03 High, Abgleich Befund ↔ Status) und Traceability-Regel 'mindestens eine Testart' umgesetzt und grün.

