# Befunde der Korrektur G1 (ab F-355)

Gemeinsame Befundliste der Korrektur G1 nach Konvention 16 des Korrekturplans ([`implementierungsplan.md`](../../docs/plan/g1-korrektur/implementierungsplan.md#konventionen-für-alle-wps)): Defekte, die während der Umsetzung der Korrektur-WPs oder in der Wiederholungsprüfung (K7: WP-596 bis WP-599) neu gefunden werden. Die IDs setzen die Nummerierung von [`PHASE1_FINDINGS.md`](../phase1-final/PHASE1_FINDINGS.md) fort (F-354 ist dort vergeben). Format, Prioritäten und Begriffe wie dort: P0 Sicherheitsrisiko, schwerer Datenverlust oder zentraler Prozess blockiert; P1 wesentliche Funktion fehlt/falsch/unzuverlässig; P2 relevante funktionale, Validierungs- oder UX-Einschränkung; P3 geringfügiger Mangel. „Ursache belegt“ heißt: durch einen ausgeführten Test oder eine reproduzierte Probe nachgewiesen.

Die Befunde F-355 bis F-361 sind bei der Umsetzung von K4 (Funktions-, Validierungs- und Bedienmängel) gefunden worden; das Feld „Gefunden in“ nennt das K4-WP. Jeder Befund ist mit einem Defekttest kodiert, der auf dem Stand `d6e768c` rot ist, und erhält ein Korrektur-WP im Abschnitt [K4-Nachträge](../../docs/plan/g1-korrektur/implementierungsplan.md#k4-nachträge) (WP-630 bis WP-634).

Defekttests: Go-Defekttests tragen das Build-Tag `auditdefect` und stehen in `tests/audit/expected-failures.txt` (Ratchet, `tests/audit/run-auditdefect.sh`). Die a11y-Befunde sind in `frontend/e2e/a11y.spec.ts` (gemockter Playwright-Lauf, CI-Job `e2e`) mit `test.fail` und der Befund-ID markiert: Der markierte Fall muss scheitern, ein markierter Fall, der besteht, macht den Lauf rot. Diese Marken sind ihr eigener Ratchet; der Ratchet von WP-337 vergleicht nur die Arten `go`, `sh`, `vitest` und `e2e-real`, daher erhalten sie keine Zeile in `expected-failures.txt`.

## Zählung

| Priorität | Anzahl |
|---|---:|
| P0 | 0 |
| P1 | 0 |
| P2 | 7 |
| P3 | 0 |
| **Summe** | **7** |

## Übersicht

| ID | Prio | Titel | Gefunden in | Ursache | Kodierender Test | Korrektur-WP |
|---|---|---|---|---|---|---|
| [F-355](#f-355) | P2 | Graue Sekundärtexte ohne Dark-Variante (und `text-gray-400` im Inventar, helles Thema) unterschreiten den WCAG-AA-Kontrast | WP-522 | belegt | frontend/e2e/a11y.spec.ts (`test.fail` F-355, 12 Routen) | WP-630 |
| [F-356](#f-356) | P2 | Standortmarker der Karte sind Schaltflächen innerhalb des Kartenbilds (nested-interactive) | WP-522 | belegt | frontend/e2e/a11y.spec.ts (`test.fail` F-356, /map) | WP-631 |
| [F-357](#f-357) | P2 | Tabellen der Benutzer- und Berechtigungsseite scrollen auf dem Telefon seitlich, ohne fokussierbar zu sein | WP-522 | belegt | frontend/e2e/a11y.spec.ts (`test.fail` F-357, mobile) | WP-631 |
| [F-358](#f-358) | P2 | Ad-hoc angelegte Discovery-Jobs werden nie ausgeführt und bleiben dauerhaft pending/queued | WP-502 | belegt | backend/internal/discovery/k4_job_execution_auditdefect_test.go TestAuditK4DiscoveryJobIsExecuted | WP-632 |
| [F-359](#f-359) | P2 | Client-gebundener Inhaber von `user:manage` ändert Name, Zeitzone, Währung, Sprache und Einstellungen der Organisation | WP-463 | belegt | backend/internal/tenantapi/k4_org_fields_auditdefect_test.go TestAuditK4OrgFieldsRequireOrgWideUserManage | WP-633 |
| [F-360](#f-360) | P2 | Höhenprüfung des Racks übersieht Mounts, die der Aufrufer nicht sieht: gescopter Nutzer verkleinert ein Rack unter belegte HE | WP-500 | belegt | backend/internal/rack/k4_scoped_mounts_auditdefect_test.go TestAuditK4RackHeightCoversInvisibleMounts | WP-634 |
| [F-361](#f-361) | P2 | Rote Löschaktionen im Standortbaum ohne Dark-Variante unterschreiten den WCAG-AA-Kontrast | WP-522 (Nachprüfung nach WP-523) | belegt | frontend/e2e/a11y.spec.ts (`test.fail` F-361, /locations dunkel) | WP-630 |

<a id="f-355"></a>
### F-355 (P2) – Graue Sekundärtexte ohne Dark-Variante (und `text-gray-400` im Inventar, helles Thema) unterschreiten den WCAG-AA-Kontrast

- **Gefunden in:** WP-522 (`t7-a11y-contrast`) · **Betroffene Anforderungen:** UI-18, NFR-07 · **Komponente:** frontend/src/pages/OnboardingPage.tsx, InventoryPage.tsx, SavedViewsPage.tsx, CITypeAdminPage.tsx, ConsumableListPage.tsx, FindingsPage.tsx, JobCenterPage.tsx, FormsPage.tsx, WorkflowPage.tsx, CompliancePage.tsx, IGAPage.tsx, SecurityPage.tsx · **Quelle:** axe-Prüfung aller Routen mit Daten (WP-522)
- **Reproduktion:** `cd frontend && CI=1 BASE_URL=http://localhost:<port> npx playwright test e2e/a11y.spec.ts --project=chromium --project=mobile-chrome` (Sprache de-DE). Ohne die Marke F-355 scheitern im dunklen Thema /onboarding, /inventory, /saved-views, /ci-types, /consumables, /findings, /jobs, /forms, /workflows, /compliance, /iga und /security, im hellen Thema /inventory, jeweils mit `color-contrast (serious)`, z. B. `<p class="text-sm text-gray-500">Wählen Sie links eine Ansicht aus.</p>` (/saved-views, dunkel), `<span class="text-gray-400">a11y-move</span>` (/inventory, hell), Tabellenköpfe der Jobliste (/jobs, dunkel).
- **Soll:** UI-18/NFR-07: jede Seite besteht WCAG 2.1 AA in beiden Themen, Textkontrast mindestens 4,5:1.
- **Ist:** Sekundärtexte, Tab-Beschriftungen und Tabellenköpfe tragen `text-gray-500` ohne `dark:`-Variante; im Inventar trägt Text `text-gray-400` auf hellem Grund. axe meldet den Kontrast als zu gering.
- **Nutzerwirkung:** Hilfstexte, Zähler und Tabellenköpfe sind im dunklen Thema schwer lesbar; Nutzer mit eingeschränktem Sehvermögen verlieren Informationen.
- **Ursache (belegt):** Farbklassen ohne Dark-Variante; WP-522 hat die Kontraste nur in seinen eigenen Dateien korrigiert.
- **Empfehlung:** Sekundärtexte über eine gemeinsame Klasse (z. B. `text-gray-600 dark:text-gray-400`) setzen, `text-gray-400` auf hellem Grund ersetzen; Marke F-355 in `a11y.spec.ts` entfernen.
- **Retest (verbindlich):** `a11y.spec.ts` ohne die Marke F-355 in allen Browser-Projekten grün (CI-Job `e2e`).
- **Kodierender Test:** frontend/e2e/a11y.spec.ts, Marke `F_CONTRAST` (`test.fail`) an den genannten Routen; lokal ausgeführt auf `d6e768c` in chromium und mobile-chrome: alle markierten Fälle scheitern mit `color-contrast (serious)`.

<a id="f-356"></a>
### F-356 (P2) – Standortmarker der Karte sind Schaltflächen innerhalb des Kartenbilds (nested-interactive)

- **Gefunden in:** WP-522 (`t7-a11y-contrast`) · **Betroffene Anforderungen:** UI-18, NFR-07 · **Komponente:** frontend/src/pages/MapPage.tsx · **Quelle:** axe-Prüfung aller Routen mit Daten (WP-522)
- **Reproduktion:** wie F-355; ohne die Marke F-356 scheitert /map in beiden Themen und allen Projekten mit `nested-interactive (serious): .h-auto <svg viewBox="0 0 100 50" role="img" aria-label="Weltkarte mit Standortmarkern" …>`.
- **Soll:** WCAG 2.1 AA (4.1.2): Bedienelemente liegen nicht in einem Element, dessen Rolle keine interaktiven Kinder erlaubt.
- **Ist:** Das SVG trägt `role="img"`, die Standortmarker darin `role="button"` (MapPage.tsx:52). Screenreader behandeln das Bild als ein Element und bieten die Marker nicht an.
- **Nutzerwirkung:** Mit Screenreader sind die Standorte der Karte nicht erreichbar.
- **Ursache (belegt):** Rollenmodell des Kartenbilds.
- **Empfehlung:** Das SVG als `role="group"` (oder ohne Rolle) mit Beschriftung führen und die Marker als Schaltflächen darin lassen, oder die Marker zusätzlich als Liste neben der Karte anbieten; Marke F-356 entfernen.
- **Retest (verbindlich):** `a11y.spec.ts` ohne die Marke F-356 grün; die Marker sind per Tastatur erreichbar und öffnen die Standortdetails.
- **Kodierender Test:** frontend/e2e/a11y.spec.ts, Marke `F_MAP_MARKERS` an /map (hell und dunkel); lokal ausgeführt: scheitert in chromium und mobile-chrome.

<a id="f-357"></a>
### F-357 (P2) – Tabellen der Benutzer- und Berechtigungsseite scrollen auf dem Telefon seitlich, ohne fokussierbar zu sein

- **Gefunden in:** WP-522 (`t7-a11y-contrast`) · **Betroffene Anforderungen:** UI-18, NFR-07 · **Komponente:** frontend/src/pages/UserManagementPage.tsx (`overflow-x-auto`, :88, :138, :180, :464), frontend/src/pages/PermissionsPage.tsx (:45) · **Quelle:** axe-Prüfung aller Routen mit Daten (WP-522)
- **Reproduktion:** wie F-355 im Projekt mobile-chrome (Pixel 7) mit Sprache de-DE: /users und /permissions scheitern in beiden Themen mit `scrollable-region-focusable (serious): .overflow-x-auto <div class="overflow-x-auto">`. Mit en-US (Browsersprache von Playwright; die Oberfläche folgt seit WP-523 der Browsersprache, E-99) passen die kürzeren Beschriftungen in die Breite, die Tabelle scrollt nicht und axe meldet nichts. Der Fehler hängt also an der Sprache; `a11y.spec.ts` prüft deshalb fest in de-DE (`test.use({ locale: 'de-DE' })`).
- **Soll:** WCAG 2.1 AA (2.1.1): ein seitlich scrollbarer Bereich ist per Tastatur erreichbar und scrollbar.
- **Ist:** Der scrollende Container ist kein Fokusziel und hat keine Beschriftung; die Tabellen enthalten auf dem Telefon nicht in jeder Zeile fokussierbare Elemente, die den Bereich mitscrollen.
- **Nutzerwirkung:** Auf schmalen Bildschirmen sind rechte Spalten (Rollen, Aktionen) per Tastatur nicht erreichbar.
- **Ursache (belegt):** `overflow-x-auto`-Container ohne `tabIndex`, Rolle und Beschriftung.
- **Empfehlung:** Scrollcontainer als fokussierbare, beschriftete Region (`tabIndex={0}`, `role="region"`, `aria-label`) oder eine gemeinsame Tabellenhülle; die Prüfung zusätzlich in en-US ausführen, damit ein sprachabhängiges Layout nicht durchrutscht; Marke F-357 entfernen.
- **Retest (verbindlich):** `a11y.spec.ts` ohne die Marke F-357 in mobile-chrome und mobile-safari grün, in de-DE und en-US.
- **Kodierender Test:** frontend/e2e/a11y.spec.ts, Marke `F_SCROLL_REGION` (`knownMobile`) an /users und /permissions; lokal ausgeführt mit de-DE: scheitert in mobile-chrome (beide Themen); mit en-US (Stand `d6e768c` ohne Sprachfestlegung) bestanden die markierten Fälle („Expected to fail, but passed“).

<a id="f-358"></a>
### F-358 (P2) – Ad-hoc angelegte Discovery-Jobs werden nie ausgeführt und bleiben dauerhaft pending/queued

- **Gefunden in:** WP-502 (`t6-discovery-job-tracking`) · **Betroffene Anforderungen:** JOB-01, DIS-09 · **Komponente:** backend/internal/discovery/jobs.go (CreateJob), backend/internal/discovery/pg_repository.go (UpdateJobStatus), collector/collectorcmd · **Quelle:** Ursachen- und Umfangsbestätigung von WP-502 (alle Schreibpfade von `discovery_job.status`)
- **Reproduktion:** `POST /api/v1/discovery/jobs {"collector_id": "<id>", "job_type": "sweep"}` → 201, Status `pending`, im Job Center `discovery.sweep` mit `queued`. Kein Collector, Worker oder Edge-Handler liest offene Discovery-Jobs; `PGRepository.UpdateJobStatus`, der einzige Weg, einen Job weiterzuschalten, hat außerhalb von Tests keinen Aufrufer, und kein anderer Produktionscode schreibt `UPDATE discovery_job`. Der Job bleibt für immer `pending`/`queued`.
- **Soll:** Ein über die API angelegter Job wird ausgeführt (JOB-01: Status, Fortschritt und Ergebnis jedes Jobs), oder die API bietet das Anlegen nicht an.
- **Ist:** 201 und ein Job, der nie läuft; das Job Center zeigt ihn dauerhaft als wartend.
- **Nutzerwirkung:** Nutzer und Integrationen starten Scans, die nie stattfinden, ohne Fehlermeldung.
- **Ursache (belegt):** Für ad-hoc angelegte Discovery-Jobs gibt es keinen Ausführungspfad; die Collectoren arbeiten nur ihre Scopes nach Zeitplan ab (WP-502 hat das als neuen Befund gemeldet).
- **Empfehlung:** Entscheidung des Auftraggebers: entweder Ausführung über den Scope-Kanal des Collectors (Job zustellen, `running`/`completed`/`failed` über `UpdateJobStatus` mit Spiegelung, Zeitüberschreitung) oder Rücknahme von `POST /discovery/jobs` aus API und Spezifikation.
- **Retest (verbindlich):** `cd backend && go test -tags auditdefect -count=1 -run 'TestAuditK4DiscoveryJobIsExecuted' ./internal/discovery/` → PASS; bei Ausführung zusätzlich ein Integrationstest pending → running → completed mit einem simulierten Collector.
- **Kodierender Test:** backend/internal/discovery/k4_job_execution_auditdefect_test.go TestAuditK4DiscoveryJobIsExecuted (liest den Produktionscode von Backend und Collector; rot auf `d6e768c`: „POST /api/v1/discovery/jobs creates pending discovery jobs, but no production code of the backend or the collector moves a discovery job on …“).

<a id="f-359"></a>
### F-359 (P2) – Client-gebundener Inhaber von `user:manage` ändert Name, Zeitzone, Währung, Sprache und Einstellungen der Organisation

- **Gefunden in:** WP-463 (`t2-ai-optin-api`) · **Betroffene Anforderungen:** RBA-03, TEN-06, LOC-01 · **Komponente:** backend/internal/tenantapi/handler.go (UpdateOrganization), backend/internal/server/authz.go (Route `org` → `user:manage`) · **Quelle:** Umsetzung von WP-463 (dort als „Andere Felder bleiben für sie wie bisher schreibbar“ festgehalten)
- **Reproduktion:** Produktionsrouter über PostgreSQL, Principal mit `user:manage` und Scope Client 1: `PATCH /api/v1/org` mit `{"name": …}`, `{"timezone":"Pacific/Auckland"}`, `{"currency":"JPY"}`, `{"locale":"ja-JP"}` oder `{"settings":{…}}` → jeweils 200, die Organisationszeile ist geändert.
- **Soll:** Felder der Organisation gelten für alle Clients; nur ein org-weiter Inhaber von `user:manage` ändert sie (RBA-03, wie für `ai_opt_in` in WP-463). Ein gebundener Inhaber erhält 403 problem+json, die Zeile bleibt unverändert.
- **Ist:** 200 für jeden Inhaber von `user:manage`, gleich welcher Scope; nur `ai_opt_in` prüft den org-weiten Scope.
- **Nutzerwirkung:** Ein delegierter Kundenadmin (MSP-Modell) benennt die Organisation um und ändert Zeitzone, Währung und Sprache für alle anderen Clients.
- **Ursache (belegt):** Die Routenberechtigung prüft `user:manage` ohne Scope; `UpdateOrganization` ruft `holdsOrgWide` nur bei `ai_opt_in`. Der reguläre Test `TestOrganizationAIOptInRequiresOrgWideUserManage` (`tenantapi/handler_test.go:250`) kodiert das Fehlverhalten („PATCH without ai_opt_in → 200“ für gebundene Principals).
- **Empfehlung:** `holdsOrgWide(r, user:manage)` für jeden Schreibzugriff auf `PATCH /org`; den regulären Test umschreiben (Konvention 13).
- **Retest (verbindlich):** `cd backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -run 'TestAuditK4OrgFieldsRequireOrgWideUserManage' ./internal/tenantapi/` → PASS; org_admin weiter 200.
- **Kodierender Test:** backend/internal/tenantapi/k4_org_fields_auditdefect_test.go TestAuditK4OrgFieldsRequireOrgWideUserManage (rot auf `d6e768c`: `PATCH /api/v1/org {"name":"k4 renamed by a client admin"} by a holder of user:manage bound to client 1: 200 …; want 403`).

<a id="f-360"></a>
### F-360 (P2) – Höhenprüfung des Racks übersieht Mounts, die der Aufrufer nicht sieht: gescopter Nutzer verkleinert ein Rack unter belegte HE

- **Gefunden in:** WP-500 (`t6-location-conflicts`) · **Betroffene Anforderungen:** RCK-01, TEN-06 · **Komponente:** backend/internal/rack/pg_repository.go (`checkHeightCoversMounts`, `mountsForRackTx`) · **Quelle:** Umsetzung von WP-500
- **Reproduktion:** 42-HE-Rack in einem Raum von Client 1; ein org-weiter Principal montiert ein CI von Client 2 auf U40–41. Ein auf Client 1 beschränkter Principal sieht das Rack, aber nicht den Mount (`rack_mount_isolation` filtert nach dem Client des CI): `PATCH /api/v1/racks/{id} {"height_units":39}` → 200, gespeichert 39, der Mount liegt oberhalb des Racks.
- **Soll:** RCK-01: `position_u + height_u − 1 ≤ height_units` für jeden Mount, auch für Mounts, die der Aufrufer nicht sieht; 409 auf `height_units`, ohne Details des fremden Mounts preiszugeben.
- **Ist:** 200; die Prüfung zählt nur sichtbare Mounts. Das Montieren in belegte HE bleibt dagegen verweigert, weil die Ausschlussbedingung `rack_mount_no_overlap` nicht der Row-Level-Security unterliegt (im Test geprüft).
- **Nutzerwirkung:** Mounts anderer Clients liegen außerhalb ihres Racks; Rackansicht und Belegung sind falsch.
- **Ursache (belegt):** `checkHeightCoversMounts` liest `rack_mount` unter dem Scope des Aufrufers; eine DB-Regel für die Höhe gibt es nicht.
- **Empfehlung:** Höchste belegte HE über eine SECURITY-DEFINER-Funktion oder einen Constraint-Trigger auf `rack` prüfen, der alle Mounts sieht; Meldung ohne fremde CI-Daten.
- **Retest (verbindlich):** `cd backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -run 'TestAuditK4RackHeightCoversInvisibleMounts' ./internal/rack/` → PASS.
- **Kodierender Test:** backend/internal/rack/k4_scoped_mounts_auditdefect_test.go TestAuditK4RackHeightCoversInvisibleMounts (rot auf `d6e768c`: `client-1 PATCH height_units=39 of a rack with a client-2 mount at U40-41: 200, stored height 39, highest mounted unit 41; want 409 …`).

<a id="f-361"></a>
### F-361 (P2) – Rote Löschaktionen im Standortbaum ohne Dark-Variante unterschreiten den WCAG-AA-Kontrast

- **Gefunden in:** Nachprüfung der Marken von WP-522 (`t7-a11y-contrast`) nach WP-523 (`t7-i18n-labels`) · **Betroffene Anforderungen:** UI-18, NFR-07 · **Komponente:** frontend/src/pages/LocationTreePage.tsx:421 (`text-xs text-red-600 hover:underline`) · **Quelle:** axe-Prüfung aller Routen mit Daten
- **Reproduktion:** wie F-355; /locations im dunklen Thema scheitert mit `color-contrast (serious): … <button class="text-xs text-red-600 hover:underline">Löschen</button>` (chromium). WP-522 hatte die Route unter F-355 markiert, die Verletzung ist aber die rote Löschaktion, kein grauer Text. Auf dem Telefon in de-DE brechen die Zeilenaktionen um und axe bewertet ihren Kontrast nicht; in en-US (Stand `d6e768c` ohne Sprachfestlegung) scheitert auch mobile-chrome mit derselben Meldung.
- **Soll:** UI-18/NFR-07: Textkontrast mindestens 4,5:1 in beiden Themen.
- **Ist:** `text-red-600` auf dunklem Grund ohne `dark:`-Variante.
- **Nutzerwirkung:** Die Löschaktion ist im dunklen Thema schwer lesbar.
- **Ursache (belegt):** Farbklasse ohne Dark-Variante.
- **Empfehlung:** `dark:text-red-400` (oder die gemeinsame Gefahr-Klasse) für Textaktionen; Marke F-361 entfernen.
- **Retest (verbindlich):** `a11y.spec.ts` ohne die Marke F-361 in allen Browser-Projekten grün, in de-DE und en-US.
- **Kodierender Test:** frontend/e2e/a11y.spec.ts, Marke `F_DANGER_CONTRAST` an /locations (dunkel); lokal ausgeführt: scheitert in chromium.

## Geprüfte Kandidaten ohne neuen Befund

- **Idempotenz speichert 503 (WP-504/WP-505):** reproduziert, aber kein neuer Befund: [F-206](../phase1-final/PHASE1_FINDINGS.md#f-206) („5xx 24 h festgeschrieben; Retry nach 5xx erreicht Handler“) beschreibt denselben Defekt und wird von WP-569 (`t2-hardening-misc`, K6) geschlossen. Seit WP-504 tritt er im Normalbetrieb auf: Ist der Audit-Intent nicht speicherbar, antwortet die Route 503 mit `Retry-After: 5`, ohne den Handler auszuführen; die Idempotenz-Middleware liegt vor `AuditRoute` und speichert die 503-Antwort, sodass ein Retry mit demselben `Idempotency-Key` 24 Stunden lang die 503 wiederholt (Probe: erster Aufruf 503, Retry 503 mit `Idempotency-Replayed: true`, Handler 0-mal ausgeführt). Der Retest von WP-569 soll diesen Weg einschließen.
- **`scope_truncated` als `config_error` (WP-494/WP-496):** kein Defekt. `api/openapi.yaml` legt die Ursachen eines Lauffehlers als Aufzählung fest (`unreachable`, …, `config_error`, `upload_failed`, `unknown`), `scope_truncated` gehört nicht dazu. Der Server ordnet unbekannte Ursachen über `ClassifyCause` zu („not allowed“ → `config_error`); Protokoll `scope`, Ziel und Meldung („only the first 65536 were discovered“) bleiben erhalten. WP-496 dokumentiert die Abbildung in `docs/ops/discovery-metrics.md`, und der Alarm `ReticoraDiscoveryConfigErrors` deckt sie ab.
