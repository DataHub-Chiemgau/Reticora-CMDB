# Phase-1-Abnahmeaudit WP-001 – WP-213 – Abschlussbericht

| | |
|---|---|
| Prüfgegenstand | Reticora CMDB, Phase 1, Work Packages WP-001 bis WP-213 (`docs/plan/implementierungsplan.md`) |
| Geprüfter Produktstand | Commit `3a9669a` (G1-Nachbesserung, PR #64); der Audit-Branch `claude/ultracode-phase1-acceptance-jtfhsq` fügt **ausschließlich** Tests, Testkonfiguration und Audit-Artefakte hinzu (keine bestehende Datei geändert oder gelöscht, kein Produktionscode) |
| Prüfzeitraum | 2026-10-15 bis 2026-10-18 |
| Artefakte | dieser Bericht · `PHASE1_WP_AUDIT_MATRIX.md` (213 Einzelprüfungen) · `PHASE1_FINDINGS.md` (353 Defekte) · `evidence/` · `data/` |
| **Ergebnis** | **NICHT ABNAHMEFÄHIG – NO-GO für Phase 2** |

## 1. Management Summary

**Frage: Sind WP-001 bis WP-213 nachweislich vollständig, korrekt, zuverlässig und aus Endnutzersicht funktionsfähig umgesetzt, sodass Phase 2 beginnen darf?**

**Antwort: Nein.** Von 213 WPs sind 64 nachweislich vollständig erfüllt (30 %), 102 nur teilweise (48 %) und 47 verletzen beobachtet verbindliche Anforderungen (22 %). Es bestehen **16 P0- und 30 P1-Defekte** mit reproduzierter Ursache, darunter:

- **Zentraler Nutzerprozess blockiert:** Die manuelle CI-Anlage über die Oberfläche scheitert für jeden CI-Typ (F-016). Werte mit Anführungszeichen oder Zeilenumbrüchen brechen jeden auditierten Schreibvorgang ab (F-045). Jede Sitzung endet spätestens nach Ablauf des Access-Tokens, weil der SPA-Refresh immer mit 400 abgewiesen wird (F-018).
- **Mandantentrennung innerhalb einer Organisation durchbrochen** (Client-/Site-Scope): Compliance-Ergebnisse fremder Clients werden gelesen und überschrieben (F-001), Racks fremder Sites gelesen und gelöscht (F-002), Review-Items (F-003) und geteilte Dokumente/Wartungsfenster (F-004) leaken oder werden fremd gelöscht, Site-Nutzer sehen fremde Clients (F-014).
- **Rechteausweitung:** API-Key-Übernahme und -Ausweitung (F-006, F-007), Service-Account-Rollen ohne Rechteprüfung (F-008), fehlende Grant-Obergrenze für Einladungen/Rollen (F-015), MFA für Admins nicht erzwungen (F-005).
- **Geheimnisse im Klartext:** WMI- und NAS-Plugins senden Zugangsdaten unverschlüsselt bzw. ungeprüft (F-010, F-011); Collector-Zertifikate lassen sich für fremde Collector-Identitäten ausstellen (F-012); Credentials gelten scope- und clientübergreifend (F-013).
- **Betrieb/Installation:** Der TLS-/Domain-Installationspfad bricht ab (F-033 – F-035), K8s-Manifeste starten nicht (F-036), Collector verlieren nach 90 Tagen die Konnektivität (F-026).
- **Gate-Nachweise unzutreffend:** Der externe Pentest-Bericht fehlt (F-039); Gate-Checkliste und Sicherheitsreview melden „kein offenes Critical“ und CI-13 als PASS, obwohl reproduzierte Gegenbefunde vorliegen (F-038, F-040).

**Was nachweislich funktioniert** (Real-Stack, UI → API → Datenbank → UI): Self-Signup mit Verifikationsmail, Login über Keycloak (PKCE), Dashboard, Collector-Enrollment mit simulierter Flotte, Review, Impact und Export (`onboarding.spec.ts`, 6/6); die **Isolation zwischen Organisationen** hielt in allen 13 Real-Stack-Isolationstests (Lesen, Ändern, Löschen, Suche, Export, Audit, GraphQL, Header-Manipulation); Standortbaum, Rack-Layout und Mounts, Discovery mit Override-Schutz, Bulk-Jobs und Export in weiten Teilen. Die Lastbudgets NFR-02/03 werden auf ruhiger 4-Kern-Umgebung eingehalten. Die vorhandene Testbasis ist breit und grün (2021 Backend-Tests auf HEAD).

**Funktionaler Reifegrad:** Das Fundament (WithTenant, RLS, Katalogprüfung, Migrationen, Ingest-Pipeline) ist weitgehend tragfähig. Die Lücken liegen an Querschnittsgrenzen (Client-/Site-Scope außerhalb der CI-Kerntabellen, Grant-Pfade, Collector-Credential-Routing), an UI-Integrationen, die nur gegen ein gemocktes Backend getestet waren, und an Betriebs-/Installationspfaden. Der Stand ist **nicht abnahmefähig**; für eine Wiederholungsprüfung ist ein gezielter Korrekturzyklus der P0/P1-Defekte nötig (Abschnitt 9).

## 2. Phase-2-Quality-Gate

| Kriterium (Auftrag, Abschnitt 6) | Befund | Erfüllt |
|---|---|---|
| Alle verbindlichen Anforderungen ausreichend verifiziert | 149 von 213 WPs nicht BESTANDEN | nein |
| Kritische E2E-Prozesse bestanden | CI-Anlage per UI, Sitzungsverlängerung, Rollenentzug, Einladung, Standort-Löschregel scheitern im Real-Stack | nein |
| Keine abnahmeverhindernden Defekte | 16 × P0, 30 × P1 (Ursache jeweils belegt) | nein |
| Keine Nachweislücken | Pentest-Bericht fehlt; paketierter Installer, Nicht-Chromium-Browser und GitHub-CI-Läufe in der Prüfumgebung nicht prüfbar (Abschnitt 8) | nein |

**Einstufung: NICHT ABNAHMEFÄHIG.** Wesentliche Anforderungen und kritische Nutzerprozesse sind fehlgeschlagen. Nach der GO/NO-GO-Regel ist **Phase 2 nicht freizugeben (NO-GO)**. Eine bedingte Freigabe kommt nicht in Betracht, weil P0/P1-Defekte offen sind; eine Risikoakzeptanz für P0/P1 ist nach dem Auftrag nicht vorgesehen.

Hinweis zur bestehenden G1-Dokumentation: `docs/acceptance/g1-gate.md` weist G1 auf Basis einer Freigabe des Auftraggebers vom 2026-10-15 als bestanden aus. Dieses Audit kommt zu einem anderen Ergebnis; insbesondere sind dort zentrale Prämissen („kein offenes Critical“, CI-13 PASS, TEN-09 vollständig) durch reproduzierte Befunde widerlegt (F-002, F-037, F-038, F-040).

## 3. Statuszählung aller 213 WPs

| Status | Anzahl | Anteil |
|---|---:|---:|
| BESTANDEN | 64 | 30,0 % |
| TEILWEISE BESTANDEN | 102 | 47,9 % |
| FEHLGESCHLAGEN | 47 | 22,1 % |
| NICHT IMPLEMENTIERT | 0 | 0 % |
| NICHT TESTBAR | 0 | 0 % |
| UNKLAR | 0 | 0 % |
| **Summe** | **213** | 100 % |

| Meilenstein | WPs | BESTANDEN | TEILWEISE | FEHLGESCHLAGEN |
|---|---:|---:|---:|---:|
| M0.0 Werkzeugkette und Prüfrahmen | 7 | 4 | 3 | 0 |
| M0.1a Mandantentrennung | 34 | 12 | 11 | 11 |
| M0.1b Authentisierung und Autorisierung | 10 | 3 | 5 | 2 |
| M0.1c Migrationen und Standortmodell | 5 | 3 | 1 | 1 |
| M0.1d Overrides, Reconciliation-Schutz | 7 | 1 | 4 | 2 |
| M0.2a DB-Rollenvertrag und RLS-Matrix | 2 | 0 | 2 | 0 |
| M0.2b Tokens, API-Keys, Rollen, Operator | 11 | 3 | 3 | 5 |
| M0.2c Kanonisches Datenmodell, VLAN/VRF | 6 | 0 | 5 | 1 |
| M0.2d Ingest und Reconciliation | 9 | 4 | 3 | 2 |
| Epic A – Installation und Betrieb | 15 | 3 | 8 | 4 |
| Epic B – Fundament | 36 | 14 | 20 | 2 |
| Epic D – Collector und Discovery | 20 | 5 | 8 | 7 |
| Epic C – API, Kernfunktionen, UI | 40 | 11 | 22 | 7 |
| Lasttest (NFR-10) | 5 | 1 | 4 | 0 |
| Sicherheitsreview | 3 | 0 | 1 | 2 |
| Gate G1 | 3 | 0 | 2 | 1 |

Kein WP ist NICHT TESTBAR eingestuft: Wo ein Teil technisch blockiert war (z. B. paketierte Installation, Nicht-Chromium-Browser), war der übrige Teil geprüft; das WP ist dann TEILWEISE BESTANDEN mit benannter Blockade im Feld „Offene Nachweise“ der Matrix.

## 4. Defekte

| Priorität | Anzahl | Bedeutung |
|---|---:|---|
| P0 | 16 | Sicherheitsrisiko, schwerer Datenverlust oder zentraler Prozess blockiert |
| P1 | 30 | wesentliche Funktion fehlt, ist falsch oder unzuverlässig |
| P2 | 133 | relevante funktionale, Validierungs- oder UX-Einschränkung |
| P3 | 174 | geringfügiger Qualitätsmangel |
| **Summe** | **353** | eindeutige Defekte nach Deduplizierung über alle Prüfquellen; 334 mit belegter Ursache, 19 als Hypothese gekennzeichnet |

### 4.1 P0-Defekte (abnahmeverhindernd)

| ID | Defekt | Betroffene WPs |
|---|---|---|
| F-001 | Compliance-Auswertung eines client-gescopten Principals liefert und überschreibt Ergebnisse anderer Clients | WP-020 |
| F-002 | Site-Scope-Bypass im Rack-Kanal: site-fremde Racks lesbar und löschbar | WP-014, WP-208, WP-209, WP-213 |
| F-003 | Review-Items ohne Kandidaten-CIs leaken über Clients | WP-013, WP-025, WP-208 |
| F-004 | Geteilte Dokumente/Wartungsfenster: teilgescopter Principal ändert/löscht sie, FK-Kaskade entfernt Links anderer Clients | WP-019, WP-020, WP-028 |
| F-005 | MFA für org_admins aus Signup, Einladung, Operator-Pfad und Gruppe reticora-admin nicht erzwungen | WP-044 |
| F-006 | API-Key-Rotation/-Widerruf durch Nicht-Owner: Rechteausweitung und Übernahme fremder Keys | WP-067 |
| F-007 | API-Key-Bindung an Service-Account ohne Scope-Prüfung (client-gebundener Ersteller erhält org-weiten Key) | WP-067, WP-069 |
| F-008 | Service-Account-Rollenzuweisung ohne Rechte-/Scope-Prüfung | WP-069 |
| F-009 | Webhook-Filter liefert Events anderer Clients an client-gebundene Service-Accounts | WP-069 |
| F-010 | WMI-Plugin sendet Credentials im Klartext (HTTP Basic, HTTPS ohne Zertifikatsprüfung) | WP-048, WP-152 |
| F-011 | NAS-Plugin sendet Konto und Passwort in der URL über HTTP bzw. ungeprüftes HTTPS | WP-152 |
| F-012 | Zertifikatserneuerung nicht an den Collector gebunden: fremde Collector-Identität ausstellbar | WP-145, WP-147 |
| F-013 | Collector ignoriert credential_ids je Scope; Credentials gelten über Client-Grenzen | WP-147, WP-153 |
| F-014 | Site-Grant setzt Client-Scope auf NULL: Site-Nutzer lesen Kontakte, Credentials, Assets fremder Clients | WP-109 |
| F-015 | Keine Grant-Obergrenze: client-gescopter user:manage-Inhaber vergibt org-weite org_admin-Rechte | WP-114 |
| F-016 | CI-Dialog sendet hart kodierten Typ-Key statt Typ-ID: keine manuelle CI-Anlage über die UI möglich | WP-046, WP-117, WP-119, WP-120, WP-191, WP-194, WP-201, WP-239 |

### 4.2 P1-Defekte

| ID | Defekt | Betroffene WPs |
|---|---|---|
| F-017 | Rollenzuweisung nicht entziehbar (kein DELETE /role-assignments/{id}) | WP-009, WP-046, WP-066, WP-168 |
| F-018 | SPA-Refresh (POST /auth/refresh ohne Body) vom OpenAPI-Validator immer mit 400 abgewiesen – Abmeldung bei Token-Ablauf | WP-009, WP-050, WP-066 |
| F-019 | Abgewiesene Requests einer Credential leeren das Org-Ratelimit-Budget | WP-166 |
| F-020 | Bulk-Statusänderung ist kein manueller Schreibvorgang (kein Override, keine Version, unter Override still verworfen, „ok“ gemeldet) | WP-057, WP-061, WP-181 |
| F-021 | Override setzen/aufheben erhöht ci.version nicht (Lost Update) | WP-061 |
| F-022 | Review-Auflösung „new“ bei resurrected_device erzeugt org-weites CI ohne Identität | WP-087 |
| F-023 | Löschen von Site/Building/Room kaskadiert Kindstandorte statt 409 (LOC-11) – hochgestuft von P2 | WP-055 |
| F-024 | Site-Löschung kaskadiert VLANs/Enrollment-Tokens und entzieht Zuordnungen – hochgestuft von P2 | WP-055 |
| F-025 | Deaktivierter Collector liefert weiter (offene NATS-Verbindung, Kanäle ohne Statusprüfung) | WP-145, WP-153 |
| F-026 | Keine automatische Zertifikatserneuerung; Collector verlieren nach 90 Tagen die Konnektivität | WP-145 |
| F-027 | Falsche Credential-Auswahl im Collector-Bundle | WP-147, WP-153, WP-156 |
| F-028 | WMI-Credentials erreichen das WMI-Plugin nie; SSH-Credentials gehen an WMI-/NAS-Ziele | WP-152 |
| F-029 | WMI Collect meldet Erfolg bei 401/Transportfehler | WP-152, WP-155 |
| F-030 | Jedes gesweepte Gerät erhält ein falsches, wiederkehrendes unclassified_device-Review | WP-151 |
| F-031 | scope_suggested umgeht discovery:manage und ist clientübergreifend sichtbar/verwerfbar | WP-153, WP-156 |
| F-032 | Ausgelieferter Collector liefert keine LLDP-/CDP-/FDB-Nachbarn | WP-158 |
| F-033 | certbot kann das Zertifikat neben dem Bootstrap-Platzhalter nicht ablegen | WP-094 |
| F-034 | certbot-Schlüssel/-Kette für den nginx-Nutzer unlesbar | WP-094 |
| F-035 | OIDC-Issuer-Prüfung vor dem Frontend-Start: Domain-/TLS-Installationen brechen ab | WP-095 |
| F-036 | K8s-Manifeste ohne RETICORA_OIDC_ISSUER_URL: Server startet nicht | WP-097, WP-099 |
| F-037 | CI-13-Reaktivierung über die Produktions-Decorator-Kette liefert 501 (SR-11) | WP-087, WP-213 |
| F-038 | Gate-Checkliste führt CI-13 als PASS trotz offenem Defekt SR-11 | WP-213 |
| F-039 | Kein Pentest-Bericht und keine Befundliste im Repository | WP-210 |
| F-040 | Review und Gate melden TEN-09 vollständig/„kein offenes Critical“ trotz reproduziertem Rack-Bypass | WP-209, WP-213 |
| F-041 | Einladungen versenden keine E-Mail (invitation.created wird nie erzeugt) | WP-114, WP-140 |
| F-042 | Manuell verifizierte Port-Kanten werden bei Interface-Entfernung durch Discovery still gelöscht | WP-128 |
| F-043 | EVT-02-Ereignistypen (relationship.*, collector.enrolled, review.resolved) werden nie erzeugt | WP-136 |
| F-044 | Entitlement-/Zertifikatsablauf: Jobläufe nicht eingeplant, Events nie erzeugt | WP-131, WP-136, WP-140 |
| F-045 | Werte mit Anführungszeichen/Steuerzeichen brechen jeden auditierten Write (audit_entry_hash) | WP-117, WP-120, WP-132, WP-133 |
| F-046 | Collector-Metrik-Uploads ohne Credential: 401, Metriken erreichen den Server nie | WP-190 |

P2 und P3 siehe `PHASE1_FINDINGS.md` (jeweils mit Reproduktion, Soll/Ist, Nutzerwirkung, Ursache, Empfehlung und verbindlichem Retest).

## 5. Testausführung und Ergebnisse

Alle Ergebnisse stammen aus tatsächlichen Läufen in der Prüfumgebung; nichts ist simuliert. Rohdaten unter `evidence/`.

### 5.1 Baseline auf `3a9669a` (vor Testergänzung)

| Prüfung | Ergebnis | Evidenz |
|---|---|---|
| Backend, alle PostgreSQL-Tests wie CI-Job `migrations` (TimescaleDB 2.17.2-pg16, NATS JetStream, Redis) | 1896 bestanden, 0 Fehler, 0 übersprungen | `evidence/results-backend.tsv` |
| Collector / Edgecore (`go test -race`) | 273 / 76 bestanden | `evidence/results-collector.tsv`, `results-edgecore.tsv` |
| golangci-lint v2.14.0 (gepinnt) | 0 Befunde (mit `new-from-rev`-Baseline; ohne Baseline 429 Altbefunde, siehe WP-001) | `evidence/lint-*.log` |
| Frontend: format, lint, API-Client-Abgleich, Typecheck, Vitest, Build | alle grün; Vitest 249/249 | `evidence/results-frontend-vitest.tsv` |
| Playwright gegen gemocktes Backend (chromium, mobile-chrome) | 50/50 | `evidence/e2e-mocked.log` |
| Playwright Real-Stack Onboarding | 6/6 | `evidence/e2e-onboarding-real.log` |
| Shell-Tests (Installer-Helfer, Preflight, Backup, NATS-mTLS, Simulate-Ingest, Gate-Struktur) | 36/36, 12/12, 29/29, 26/26, 3/3, 4/4, 47/47 | `evidence/sh-*.log`, `evidence/rep01.log` |
| Migrationen up → down -all → up (138) | ok | `evidence/migrate.log` |

### 5.2 Finale Regression auf dem Audit-Branch

| Prüfung | Ergebnis | Evidenz |
|---|---|---|
| Backend, vollständige PostgreSQL-Suite auf privatem Einzel-DB-Cluster (wie CI), inkl. der neuen Regressionstests | **2021 bestanden, 0 Fehler** | `evidence/final-regression/` |
| Collector / Edgecore | bestanden | `evidence/final-regression/summary.txt` |
| Frontend Vitest (Standardlauf) | 48 Dateien, 252 Tests bestanden | `evidence/audit-tests/verify-*.tsv` |
| Real-Stack-E2E-Suite `frontend/e2e-real/` (82 Tests) | **56 bestanden, 26 fehlgeschlagen** – jeder Fehlschlag kodiert einen bestätigten Defekt | `evidence/final-regression/e2e-real-HEAD.log` |
| Defekttests (opt-in) | 116 Go-, 9 Shell-, 11 Frontend-Defekttests schlagen erwartungsgemäß an ihrer Anforderungsassertion fehl | `evidence/audit-tests/` |

### 5.3 E2E-Ergebnisse (Real-Stack, ohne Mocks)

| Prozesskette | Tests | Bestanden | Fehlgeschlagen (Defekt) |
|---|---:|---:|---:|
| Mandantentrennung zwischen Organisationen (`tenant-isolation.spec.ts`) | 17 | 13 | 4 (F-016, F-145/T14, F-161/T15, F-078/T17) |
| Rollen, Einladung, Admission, Session (`roles-authz.spec.ts`) | 18 | 11 | 7 (F-041, F-017, F-018 [2 Tests], F-016, F-062, F-201) |
| CI-Lebenszyklus (`ci-lifecycle.spec.ts`) | 18 | 9 | 9 (u. a. F-016, F-045, F-077, F-078, F-080) |
| Standorte und Racks (`locations-racks.spec.ts`) | 10 | 7 | 3 (F-023, F-302, Mount-Historie) |
| Discovery, Override-Schutz, Review (`discovery-override.spec.ts`) | 8 | 7 | 1 (F-100) |
| Bulk, Jobs, Saved Views, Export, Webhooks (`bulk-jobs-export.spec.ts`) | 10 | 8 | 2 (F-062, F-327) |
| Smoke | 1 | 1 | 0 |
| Onboarding (bestehende Suite, `e2e/onboarding.spec.ts`) | 6 | 6 | 0 |

Erkenntnis aus dem Vergleich: Mehrere Defekte (CI-Anlage, Audit-Viewer, Export-Buttons, Sitzungsverlängerung) waren von der gemockten E2E-Suite verdeckt, weil die Mocks die Fehlannahmen des Frontends übernehmen.

### 5.4 Integrations-, Sicherheits- und Lastergebnisse

- **Sicherheit:** 8 der 16 P0 betreffen Autorisierung/Mandantentrennung innerhalb einer Organisation, 4 die Behandlung von Geheimnissen/Identitäten (Collector), 1 MFA. Die Organisationsgrenze selbst (RLS auf `app.org_id`, Header-Spoofing, IDOR, GraphQL) hielt in allen Real-Stack- und Integrationstests.
- **Datenintegrität:** stille Datenverluste durch Kaskaden (F-023, F-024, F-042, F-004), Lost Updates (F-021), still verworfene Massenänderungen (F-020), abgebrochene Writes bei Sonderzeichen (F-045).
- **Last (k6 0.54.0, Seed 10.000 Objekte, Nachbau von `load.yml`):** Lauf 2 auf ruhiger Umgebung: alle 10 API-Budgets eingehalten (list p95 450 ms bei 500 ms Budget, search 348 ms, impact 1135 ms, bulk 500 CIs 12,7 s); Ingest p95 43 ms, Sichtbarkeit ≤ 2,3 s; Spool 415 Records/s, 0 verloren. Lauf 1 unter Fremdlast (Load-Average bis 12 auf 4 Kernen) verfehlte list (717 ms) und search (581 ms) – geringe Reserve von `list` (≈ 10 %). `evidence/load/`.
- **Testrobustheit:** `TestCIQueryBudget` (Indexnachweis NFR-02) ist umgebungsabhängig: grün auf dem Einzel-DB-Cluster, rot nach Container-Neustarts auf identischem Code (`evidence/audit-tests/querybudget-flake.txt`). Der Migrations-Roundtrip-Test ist auf geteilten Clustern unbrauchbar und kann dort eine Test-DB halb zurückmigriert hinterlassen (WP-006, P3).

## 6. Vorhandene und neu erstellte Tests

**Vorhanden (Baseline):** 1896 Backend-, 349 Collector/Edgecore-, 249 Vitest-, 56 Playwright- und 157 Shell-Testfälle. Lücke der vorhandenen Basis: Browser-Abläufe nur gegen ein gemocktes Backend; Client-/Site-Scope außerhalb der CI-Kerntabellen und Grant-Pfade kaum negativ getestet; Collector-Credential-Routing nur mit Fakes.

**Neu (Audit, alle versioniert):**

| Art | Umfang | Ort | Ausführung |
|---|---|---|---|
| Real-Stack-E2E (UI → API → DB) | 7 Specs, 82 Tests | `frontend/e2e-real/` | `npx playwright test -c e2e-real/playwright.real.config.ts` (Stack siehe `frontend/e2e-real/README.md`) |
| Go-Defekttests (schlagen bis zur Behebung fehl) | 116 Tests in 62 Dateien | `*_auditdefect_test.go` (Build-Tag `auditdefect`) | `go test -tags auditdefect -run <Test> <Paket>` |
| Go-Regressionstests (grün) | 36 Tests in 33 Dateien | `backend/…/*_test.go` mit Präfix `audit_t*`/`t2_` | regulärer CI-Lauf |
| Shell-Defekttests | 9 | `tests/audit/*.test.sh` | `bash tests/audit/<name>.test.sh` |
| Frontend-Defekttests | 11 Tests in 8 Dateien | `frontend/src/**/*.auditdefect.tsx` | `npx vitest run -c vitest.auditdefect.config.ts` |
| Frontend-Regressionstests (grün) | 3 | `frontend/src/pages/CITypeAdminPage.flags.test.tsx` | `npm test` |

Defekttests sind bewusst von den Standardläufen getrennt (Build-Tag, eigene Vitest-Konfiguration, eigenes Playwright-Config): Sie kodieren die Anforderung, nicht das beobachtete Verhalten, und schlagen fehl, bis der Befund behoben ist. Ihre Assertions wurden nicht an Fehlverhalten angepasst. Unabhängige Prüfung: Jeder Go-Defekttest wurde auf frischer Datenbank einzeln ausgeführt und scheitert an einer Anforderungsassertion (nicht an Setup-Fehlern); alle berührten Pakete laufen ohne Tag grün; `go vet` mit und ohne Tag, `gofmt`, `golangci-lint` ohne Befund (`evidence/audit-tests/verify-*.tsv`, Skript `verify_audit_tests.sh`).

**Verbleibende Abdeckungslücken** (je WP im Feld „Offene Nachweise“ der Matrix): Client-/Site-gescopte Nutzer im Real-Stack-Login; Teamscopes (nicht zuweisbar, F-049); Webhook-Zustellung und Benachrichtigungskanäle End-to-End; paketierter Installer und ACME-Pfad auf echter Domain; Nicht-Chromium-Browser; mehrere P2/P3-Befunde sind dokumentiert, aber nicht durch eigene Tests kodiert.

## 7. Methode

1. **Inventarisierung:** 213 WP-Blöcke aus dem Implementierungsplan extrahiert (Anforderungen, Akzeptanzkriterien, Tests, Traceability, Abhängigkeiten), Commits je WP zugeordnet, Spezifikationstexte des Katalogs v3 herangezogen.
2. **Einzelprüfung:** 27 risikobasierte Batches; je Batch ein Auditor (Kriterium → Implementierung → ausgeführte Tests → Testinhalt → Prüfebenen UX/API/Daten/E2E/Robustheit → Platzhalter/Mocks/TODOs/fehlende Verdrahtung) und ein unabhängiger adversarialer Verifizierer, der jedes BESTANDEN zu widerlegen und jeden Defekt zu entkräften versuchte (eigene Proben, eigene Datenbank).
3. **Real-Stack-E2E:** echter Server (Production-Modus), Keycloak 24 mit dem Repo-Realm, NATS mit mTLS-Edge und vom Server erzeugter PKI, Mailpit, MinIO, Produktions-Build des Frontends; sechs Prozessketten, jeder gemeldete Defekt adversarial nachgeprüft.
4. **Testergänzung:** Defekt- und Regressionstests je WP-Gruppe in isolierten Worktrees; unabhängige mechanische Prüfung jedes Tests.
5. **Abschlusssynthese:** je Gruppe finaler Status nach verbindlichen Regeln (Abschnitt „Statusregeln“ der Matrix), deduplizierte Defekte, danach erneute adversariale Prüfung jedes BESTANDEN und jeder Statusänderung; gruppenübergreifende Deduplizierung und Konsistenzprüfung (kein BESTANDEN mit zugerechnetem P0–P2-Defekt).

Statusverlauf: Gegenüber der verifizierten Einzelprüfung änderten sich 17 Status, 16 davon Herabstufungen wegen bestätigter Defekte; kein WP wurde ohne zusätzliche Evidenz auf BESTANDEN gehoben.

## 8. Prüfumgebung, Einschränkungen und Abweichungen

- **Paketierter Installer nicht ausführbar:** `./install.sh --cloud` scheitert beim Image-Build an der Netzwerkrichtlinie der Prüfumgebung (`dl-cdn.alpinelinux.org`, `quay.io` → 403; `evidence/install-cloud-blocked.log`). Ersatz: derselbe Stack aus denselben Artefakten manuell aufgebaut (Abschnitt 7). Installationsbezogene WPs sind deshalb nur über Skript-Tests, Proben und Code belegt.
- **Browser:** nur Chromium/mobile-Chrome lokal ausführbar; Firefox, WebKit, Edge nicht installierbar.
- **Nicht beobachtbar:** GitHub-Actions-Läufe (CI-Ergebnisse, `install-smoke.yml`, `load.yml`), externer Pentest, govulncheck (vuln.go.dev gesperrt) und Trivy.
- **Testumgebung:** JetStream nach einem Neustart mit weniger freiem Speicher lokal mit `max_file_store: 60G` betrieben (Scratch-Kopie der Repo-Konfiguration, keine Repo-Änderung). Die Prüfmaschine hatte 4 Kerne; Lastläufe unter Fremdlast sind gekennzeichnet.
- **Verifikationsstufe:** Für die Gruppe T3 (WP-052–063, 077–091, 171–178) konnte die adversariale Prüfung der Synthese wegen eines Nutzungslimits nicht als eigener Agentenlauf erfolgen. Ersatz durch die Auditleitung: Abgleich jeder Statusänderung (einzige: WP-172 Herabstufung mit belegtem Defekt) und jedes BESTANDEN (alle bereits adversarial verifiziert) sowie die automatische Konsistenzprüfung. Die Review-Agenten der Testwelle wurden ebenfalls durch die mechanische Prüfung aller Tests ersetzt (Abschnitt 6).
- **Evidenzpfade:** Verweise der Form `SP/…` in Matrix und Befundliste beziehen sich auf den Arbeitsbereich des Audits; die textuellen Logs daraus liegen als `evidence/scratch-work-logs.tgz` bei (ohne Dateien mit Test-Tokens). Alle Befunde sind über die versionierten Tests und Retest-Befehle reproduzierbar.
- **Abgrenzung:** Geprüft wurden genau WP-001 bis WP-213. Weitere G1-Pflicht-WPs laut Plan – WP-214 (Feature-Gates), WP-240 – WP-242 (PII-Vault), WP-332 – WP-335 (G1-Nachbesserung) – sind nicht Gegenstand dieses Audits und gesondert abzunehmen; ihre Wirkung ist nur mittelbar über Abhängigkeiten berücksichtigt.

## 9. NO-GO: Blocker, notwendige Korrekturen und verbindliche Retest-Kriterien

### 9.1 Blocker (vor jeder Freigabe zu beheben)

1. **Mandantentrennung innerhalb der Organisation** (F-001 – F-004, F-009, F-013, F-014): Client-/Site-Prädikate für alle objektbezogenen Modul- und Kanalpfade (Compliance, Wartung, Dokumente, Review-Items, Racks, Webhook-Filter), Site-Grant mit Client-Bezug, Credential-Routing je Scope.
2. **Rechteausweitung** (F-005 – F-008, F-015, F-017): durchgängige Regel „nie mehr Rechte oder breiteren Scope vergeben als man selbst hält“ für Einladungen, Rollen, Service-Accounts, API-Keys; Owner-Prüfung bei Key-Rotation/-Widerruf; MFA für alle Admin-Pfade; Rollenentzug.
3. **Zentrale Nutzerprozesse** (F-016, F-018, F-045, F-020, F-021): CI-Dialog auf Metamodell-Typen umstellen; bodylose POSTs im Validator zulassen und Refresh-Fluss der SPA korrigieren; audit_entry_hash auf kanonisches JSON umstellen; Bulk als manuellen Schreibvorgang mit Override und Version behandeln; Version bei Override-Änderungen erhöhen.
4. **Collector-Sicherheit** (F-010 – F-013, F-025 – F-029): TLS mit Zertifikatsprüfung für WMI/NAS, Credentials je Protokoll und Scope, Zertifikatserneuerung an die Collector-Identität binden und automatisieren, deaktivierte Collector sofort trennen.
5. **Datenverlust** (F-023, F-024, F-042, F-004): LOC-11-Löschregel (409 bei Kindern/Abhängigkeiten), keine Kaskaden auf manuelle Daten.
6. **Betrieb/Installation** (F-033 – F-036, F-026): ACME-/TLS-Pfad und Reihenfolge der Issuer-Prüfung im Installer, Pflichtvariablen in den K8s-Manifesten.
7. **Gate-Nachweise** (F-037 – F-040): Pentest-Bericht mit Befundliste ablegen (A-07); Gate-Checkliste und Sicherheitsreview gegen die Befunde dieses Audits korrigieren.

### 9.2 Fehlende Tests (vor Retest zu ergänzen)

- Real-Stack-Login client- und site-gescopter Nutzer mit Lese-/Schreibgrenzen über UI und API (Ergänzung zu `tenant-isolation.spec.ts`).
- Installationstest des paketierten Stacks auf allen Referenzplattformen (`install-smoke.yml`) einschließlich ACME auf echter Domain.
- Browser-Matrix aller sechs Playwright-Projekte in CI für die Real-Stack-Suite.
- Stabiler NFR-02-Indexnachweis (`TestCIQueryBudget` ohne Abhängigkeit von Cluster-Statistik) und Migrations-Roundtrip ohne clusterweite Rollen.

### 9.3 Verbindliche Retest-Kriterien

1. Für **jeden P0- und P1-Defekt**: der in `PHASE1_FINDINGS.md` genannte Retest ist grün; die kodierenden Defekttests (`-tags auditdefect`, `tests/audit/`, `vitest.auditdefect.config.ts`, `e2e-real`) bestehen und werden in die regulären Suiten überführt (Tag bzw. Sonderkonfiguration entfernen).
2. Vollständige Regression grün: Backend-PostgreSQL-Suite, Collector, Edgecore, Frontend-Kette, gemockte Playwright-Suite in allen sechs Browserprojekten, **Real-Stack-Suite `frontend/e2e-real/` vollständig (82/82)** und `onboarding.spec.ts` gegen eine frisch installierte Instanz.
3. `install-smoke.yml` auf allen drei Plattformen und `load.yml` auf der Referenzumgebung grün (Nachweis als CI-Lauf-ID im Bericht).
4. Pentest-Bericht im Repository ohne offenes Critical/High; Gate-Checkliste ohne widersprochene PASS-Einträge.
5. P2-Defekte behoben oder je Defekt mit dokumentierter Risikoakzeptanz des verantwortlichen Entscheiders.
6. Wiederholungsprüfung aller 149 nicht bestandenen WPs gegen ihre „Offenen Nachweise“ (Matrix); erst danach erneute Gate-Entscheidung.

## 10. Vollständigkeitscheck

| Prüfpunkt | Ergebnis |
|---|---|
| Einzelprüfungen WP-001 – WP-213 | 213/213 in der Matrix, jede mit Status, Soll/Ist, Tests, Evidenz, Abweichung, Priorität, Abhängigkeiten, Prüfebenen und Retest |
| Adversariale Verifikation | 27/27 Batches; Synthese-Prüfung 6/7 Gruppen als eigener Lauf, T3 durch die Auditleitung (Abschnitt 8) |
| Ungeprüft als BESTANDEN markierte WPs | keine (automatische Prüfung: kein BESTANDEN mit zugerechnetem P0–P2-Defekt) |
| Defekte | 353 mit eindeutiger ID, Priorität, betroffenen WPs, Reproduktion, Soll/Ist, Nutzerwirkung, Ursache, Empfehlung, Retest |
| Kritische Testfälle | Mandantentrennung, Rollen/Authz, CI-Lebenszyklus, Standorte/Racks, Discovery/Override, Bulk/Jobs/Export real ausgeführt; alle P0/P1 durch Tests kodiert oder mit Retest-Befehl belegt |
| Produktionscode | unverändert (nur Ergänzungen von Tests, Testkonfiguration, Audit-Artefakten) |
