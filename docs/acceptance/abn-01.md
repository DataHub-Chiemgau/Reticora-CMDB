# Abnahme ABN-01 – Protokoll G1 und G2

WP-212 (`g1-abnahme`), für G2 WP-330 (`g2-abnahme`). ABN-01 legt die
Abnahmeszenarien fest (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md`,
Abschnitt 45). Für G1 zählen die Szenarien (d) und (e). Beide sind [B] und
laufen als automatisierte Protokolle gegen eingefrorene Daten. Für G2 kommt
das Metamodell-Szenario (c) [P2] hinzu; es läuft nach E-136 als
Playwright-Ablauf gegen das gemockte Backend mit eingefrorenen Antworten, die
Wirkung im Backend belegen die Integrationstests der umsetzenden WPs. Die
Daten liegen in `docs/acceptance/artefakte/`.

| Teil | Szenario | Tag | Status G1 | Status G2 | Nachweis |
|---|---|---|---|---|---|
| (a) | CMDB-Szenario, 34 Schritte, aus V §65 | [Q] | offen | **PASS** durch die Erklärung des Auftraggebers vom 2026-10-15 (E-103, Beschluss vom 2026-10-28; löst E-76 (Korrekturplan) ab, keine Lieferung der v2-Texte) | Erklärung des Auftraggebers vom 2026-10-15 (B4); nach dem Beschluss vom 2026-10-28 Nachweis für G1 und G2 (E-103, E-02). Die Statuszelle G1 bleibt ohne PASS durch Erklärung (`docs/acceptance/g1-gate.md`, Ausnahme der Art `erklaerung` mit „entfällt (E-103)“); WP-457 entfällt |
| (b) | System-Szenario, 20 Schritte, aus V §66 | [Q] | offen | **PASS** durch die Erklärung des Auftraggebers vom 2026-10-15 (E-103, Beschluss vom 2026-10-28; löst E-76 (Korrekturplan) ab, keine Lieferung der v2-Texte) | Erklärung des Auftraggebers vom 2026-10-15 (B4); nach dem Beschluss vom 2026-10-28 Nachweis für G1 und G2 (E-103, E-02). Die Statuszelle G1 bleibt ohne PASS durch Erklärung (`docs/acceptance/g1-gate.md`, Ausnahme der Art `erklaerung` mit „entfällt (E-103)“); WP-457 entfällt |
| (c) | Metamodell-Szenario | [P2] | nicht G1 | bestanden im lokalen Lauf (chromium, mobile-chrome, 2026-11-08); Lauf im CI-Job `e2e` in allen sechs Projekten nach dem Merge nachzutragen; siehe [(c) Metamodell-Szenario](#c-metamodell-szenario) | `frontend/e2e/metamodel-scenario.spec.ts` (gemockt) mit `artefakte/abn-01-c-metamodell.json`; Backend-Nachweis je Schritt im Abschnitt (c) |
| (d) | Onboarding-Szenario | [B] | **PASS** | unverändert eingefrorener Stand von G1 (Prüfsummen in `artefakte/SHA256SUMS`); siehe [(d) Onboarding-Szenario](#d-onboarding-szenario) | `frontend/e2e/onboarding.spec.ts`, Lauf auf der frischen Installation (`docs/acceptance/g1-installation.md`); Schritt 7 seit WP-456 verschärft, bestanden in install-smoke Run 40258372749 (2026-10-26) |
| (e) | Konflikt-Szenario | [B] | **PASS** | unverändert eingefrorener Stand von G1 (Prüfsummen in `artefakte/SHA256SUMS`); siehe [(e) Konflikt-Szenario](#e-konflikt-szenario) | `frontend/e2e-real/abn-01-konflikt.spec.ts` (Real-Stack, install-smoke Run 40258372749, bestanden) und `frontend/e2e/conflict.spec.ts` (gemockt), beide mit `artefakte/abn-01-e-konflikt.json` |

## (a) und (b): v2-Texte nach E-02

ABN-01 verlangt, dass beide Szenarien „unverändert“ aus V §65–66 übernommen und
vor G1 eingefroren werden. Die v2-Texte hat der Auftraggeber nicht
geliefert. E-02 ist seit 2026-10-28 entschieden: Die Erklärung des
Auftraggebers vom 2026-10-15 ersetzt die Lieferung und gilt als Nachweis für
G1 und G2 (E-103). Nach der Regel aus E-02 werden keine Schritte erfunden;
`abn-01-a-cmdb.md` und `abn-01-b-system.md` gibt es nicht. B4 in
`docs/acceptance/g1-gate.md` ist durch die Erklärung nachgewiesen.

Nachtrag 2026-10-15: Der Auftraggeber hat B4 am 2026-10-15 für erledigt
erklärt. Die Texte liegen im Repository weiterhin nicht vor; (a) und (b) sind
durch diese Erklärung abgenommen, nicht durch einen Test. Damals sollten die
Texte in `docs/spec/` folgen; der Beschluss vom 2026-10-28 hat das aufgehoben
(E-103, Nachtrag vom 2026-11-09).

Nachtrag 2026-10-25 (WP-451, E-76): Die Erklärung des Auftraggebers ersetzt
den Nachweis nicht. (a) und (b) stehen auf „offen“; ABN-01 steht in
`docs/acceptance/g1-gate.md` auf „offen“ und in
`docs/acceptance/g1-ausnahmen.csv`. Die Lieferung der Szenariotexte (WP-457)
hat der Beschluss vom 2026-10-28 gestrichen (Nachtrag vom 2026-11-09). (d)
und (e) bleiben durch Tests belegt.

Nachtrag 2026-11-08 (WP-330, E-103): Nach dem Beschluss des Auftraggebers
vom 2026-10-28 gilt die Erklärung vom 2026-10-15 als Nachweis für G1 und G2;
das löst E-76 (Korrekturplan) in diesem Punkt ab, B4 ist gestrichen. Die
v2-Texte werden nicht nachgereicht und nicht abgeglichen. Für G2 stehen (a)
und (b) damit auf PASS; die Gate-Checkliste von G2 führt sie (WP-331).

Nachtrag 2026-11-09 (WP-831, E-103): Auch für G1 ist die Erklärung vom
2026-10-15 der Nachweis von (a) und (b); nichts wird nachgereicht, WP-457
entfällt. In der G1-Checkliste bleibt ABN-01 ohne PASS durch Erklärung auf
„offen“ (Regel `declaration` in `tests/rep01-structure.test.sh`); die Zeile
in `docs/acceptance/g1-ausnahmen.csv` nennt E-103 und die Lieferung
„entfällt (E-103)“. Das G1-Ergebnis ändert dieser Nachtrag nicht.

## (c) Metamodell-Szenario

WP-330 (`g2-abnahme`), für G2. (c) gehört nicht zu G1.

**Ablauf:** in der Reihenfolge des Katalogs
(`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:97`): Typ mit
Vererbung anlegen; Attribute Set zuordnen; zwei Templates kombinieren; Regel
im Klick-Builder definieren; Layout pro Rolle erstellen; Preview prüfen;
publizieren; Anlage durch einen normalen Nutzer; Template-Version migrieren,
Overrides bleiben erhalten. Die Schritte 1 bis 7 und 9 laufen als
`org_admin`, Schritt 8 als Nutzer mit der eingebauten Rolle `engineer`
(`permission.StandardRoles` in `backend/internal/permission/role_matrix.go`;
ohne `citype:manage`, `ci_attribute:manage` und `metamodel:publish`, E-106).

**Umgebung:** Nach E-136 nur der gemockte Teil des Musters von (e).
`frontend/e2e/metamodel-scenario.spec.ts` bedient die SPA und beantwortet
jede Anfrage unter `/api/v1` aus den eingefrorenen Daten (`mockBackend` in
`frontend/e2e/support/backend.ts`). Das strikte Mock-Backend (WP-530) prüft
jede Anfrage und jede Antwort samt Status gegen `api/openapi.yaml`
(`frontend/e2e/support/contract.ts`), ohne Eintrag in `KNOWN_DEVIATIONS`;
eine unbeantwortete Anfrage lässt den Test scheitern. `/me/permissions`,
`/entitlements` und `/modules` beantwortet die Spec selbst (Rechte der
jeweiligen Sitzung, Modul `metamodel` an). Die Sitzung setzt `signIn` mit
Name und Rechten des Nutzers (optionaler Parameter, WP-330). Locale
`de-DE` per `test.use` wie in `conflict.spec.ts` (E-99 (Korrekturplan)). Die
Spec läuft im CI-Job `e2e` (`.github/workflows/ci.yml`) in allen sechs
Projekten aus `frontend/playwright.config.ts`: chromium, edge, firefox,
webkit, mobile-chrome und mobile-safari. Anders als (e) hat (c) keinen Lauf
gegen eine echte Installation; die Wirkung im Backend belegen die
Integrationstests in der letzten Spalte der Tabelle.

**Eingefrorene Daten:** `artefakte/abn-01-c-metamodell.json`. Die Spec liest
die Datei direkt. Sie enthält:

- die Objekt-IDs mit ihrem Schlüssel nach der Funktion `ID` in
  `backend/cmd/seed/seed.go` (`md5(key)::uuid`, Präfix leer, Organisation
  `demo`). Organisation (`/org/demo`), Nutzer `admin` und `engineer`, Client
  `hq` und das Referenz-CI `bc-01-blade-1` (`demo/ci/blade/1/1`) tragen die
  IDs des Seeds von WP-325. Objekte, die der Seed über die Dienste anlegt
  (Typ `blade_server`, Sets, Template `blade_server_compute`, Rollen), und
  die Objekte des Szenarios erhalten ihre ID nach demselben Schema
  (`demo/<Art>/<Schlüssel>`, `quelle: schema`). Die Spec prüft jede ID gegen
  ihren Schlüssel;
- den Bestand: Typ `blade_server` („Blade-Server“, Untertyp von `server`) mit
  `blade_slot`, `blade_role` und `storage_pool`, die Sets `blade_power`
  („Stromversorgung“) und `blade_support` („Wartungsvertrag“, `blade_server`
  zugeordnet), die Templates `blade_server_compute` („Blade-Server
  (Compute)“) und `abn01_blade_storage` („Blade-Server (Storage)“) mit je
  zwei publizierten Versionen, die Rollen und das Referenz-CI;
- die API-Antworten je Schritt im Format von `api/openapi.yaml` und die
  erwarteten Anfragen (Methode, Pfad, Query, Body);
- Override-Feld `storage_pool`, Override-Wert `pool-abn01` und Begründung
  „Abnahme ABN-01 (c)“;
- die Template-Versionen vor und nach der Migration:
  `abn01_blade_storage` Version 1 (ohne Default) → Version 2 (Default
  `pool-a` für `storage_pool`, `8x5` für `support_level`), dazu die Berichte
  von Probelauf und Migration (`TemplateMigrationReport`, von der Spec gegen
  das Schema geprüft).

Screenshots kommen nicht ins Repository. Playwright schreibt sie nur bei
einem Fehlschlag (`screenshot: 'only-on-failure'`) nach
`frontend/test-results/` (per `.gitignore` ausgeschlossen); sie liegen nur im
Bericht des CI-Laufs (Artefakt `playwright-report-<Projekt>`).

**Festlegungen der Umsetzung:**

- Typ des Szenarios ab Schritt 3 ist `blade_server`. Die geführte Anlage
  bietet als Ausführung nur Templates genau des gewählten Typs an
  (`GET /api/v1/templates?ci_type_id=`); der in Schritt 1 angelegte Typ hat
  keine. Schritt 1 und 2 zeigen Vererbung und Set-Zuordnung am neuen Typ
  „Storage-Blade“ (Untertyp von „Blade-Server“).
- Schritt 3 kombiniert die beiden Templates am Referenz-CI über den
  Abschnitt „Templates“ der CI-Detailseite (WP-800): Position 1
  `blade_server_compute` Version 2, Position 2 `abn01_blade_storage`
  Version 1.
- Schritt 8: Die geführte Anlage (WP-317) wählt genau eine Ausführung, und
  `GET /api/v1/ci-types/{id}/form` nimmt genau ein `template_id`. Die Anlage
  sendet daher `POST /api/v1/cis` mit `templates` = [`abn01_blade_storage`];
  es greifen die Pflichtfelder und Defaults dieses Templates. Die Anlage mit
  beiden Templates und deren Pflichtfeldern belegt im Backend
  `template_assignment_integration_test.go` (Schritt 3).
- Schritt 9: Die CI-Detailseite liest die Herkunft über
  `GET /api/v1/cis/{id}/provenance`. `GET /api/v1/cis/{id}/fields` ruft die
  Spec nach der Migration im Browser selbst ab; die Antwort durchläuft die
  Vertragsprüfung.

| # | Schritt | Erwartung | Ergebnis | Backend-Nachweis |
|---|---|---|---|---|
| 1 | Typ mit Elterntyp anlegen | „Typ anlegen“ mit Elterntyp „Blade-Server“: `POST /api/v1/ci-types` mit `parent_type_id` (201); der Baum zeigt „Storage-Blade“ unter „Blade-Server“, das Detail „Elterntyp: Blade-Server“ und die Attribute als „geerbt von Blade-Server“ | bestanden (lokal) | `backend/internal/citype/inherit_integration_test.go` (WP-248) |
| 2 | Attribute Set zuordnen | „Stromversorgung“ zuordnen: `PUT /api/v1/ci-types/{id}/attribute-sets` mit `[{attribute_set_id, position: 1}]`; das Set steht in der Liste des Typs | bestanden (lokal) | `backend/internal/citype/attribute_set_integration_test.go` (WP-250) |
| 3 | Zwei Templates in definierter Reihenfolge kombinieren | Referenz-CI `bc-01-blade-1`, Abschnitt „Templates“: `PUT /api/v1/cis/{id}/templates` mit `If-Match: "1"` und `[{blade_server_compute, 2}, {abn01_blade_storage, 1}]`; Anzeige „Version 2 · Position 1“, „Version 1 · Position 2“ und „Auf Version 2 migrieren“ | bestanden (lokal) | `backend/internal/ci/template_assignment_integration_test.go` (Anlage mit zwei Templates, WP-710) und `backend/internal/citype/compose_test.go` (WP-252) |
| 4 | Regel im Klick-Builder definieren | Regelkatalog, „Regel anlegen“: Pflicht `blade_slot` auf „Blade-Server“, Bedingung `blade_role` = `storage` aus dem Klick-Builder, Meldung DE; `POST /api/v1/rules` (201), die Regel steht in der Tabelle | bestanden (lokal) | `backend/internal/ci/rules_eval_integration_test.go` (WP-257) |
| 5 | Layout pro Rolle erstellen | Draft „ABN-01 (c) Layout engineer“ anlegen und aktiv setzen; Form Builder von „Blade-Server“, Rolle `engineer`, Abschnittstitel „Storage und Wartung“: `POST /api/v1/layouts?draft=<id>` mit `role_id` (202, Draft-Item); „Im Draft … gespeichert“ | bestanden (lokal) | `backend/internal/citype/layout_integration_test.go` (Layout je Rolle, WP-259) |
| 6 | Preview prüfen | Vorschau mit Template „Blade-Server (Storage)“ als Rolle `engineer`: `GET /api/v1/ci-types/{id}/form` mit `template_id`, `role_id`, `preview=true` und `draft=<id>` (E-113); Hinweis „Die Vorschau zeigt den Stand des Drafts …“, Abschnitt „Storage und Wartung“, Defaults `pool-a` und `8x5`; keine schreibende Anfrage | bestanden (lokal) | `backend/internal/citype/form_integration_test.go` (aufgelöstes Formular und Vorschau ohne Schreibwirkung, WP-719) |
| 7 | Publizieren | Draft testen und publizieren: `POST …/metamodel/drafts/{id}/test` (202, Job `metamodel.test`), Status „Getestet“, `POST …/publish`, Status „Publiziert“ | bestanden (lokal) | `backend/internal/metamodel/publish_job_integration_test.go` (Test-Lauf, WP-721) und `backend/internal/metamodel/publish_integration_test.go` (Publish, WP-261) |
| 8 | Anlage durch einen normalen Nutzer | als `engineer` über „CI anlegen“: Kachel „Blade-Server“, Ausführung „Blade-Server (Storage)“, Abschnitt „Storage und Wartung“ (Layout aus 5), „Speicherpool“ Pflicht und vorbelegt, „Servicelevel“ vorbelegt; Rolle `storage` macht „Einschub“ zur Pflicht (Regel aus 4); keine Begriffe „Template“, „Attribut-Set“, „Attribute Set“ im Dialog; `POST /api/v1/cis` mit `templates` (201). `PATCH /api/v1/attribute-definitions/{id}` → 403 mit Problem-Body (`title`, `detail`), der Dialog meldet das fehlende Recht `ci_attribute:manage` | bestanden (lokal) | `backend/internal/permission/role_matrix_integration_test.go` (WP-245) |
| 9 | Template-Version migrieren, Overrides bleiben erhalten | Override auf `storage_pool` (`PUT /api/v1/cis/{id}/fields/storage_pool/override`, „geschützt“); „Auf Version 2 migrieren“: `POST /api/v1/templates/{id}/migrations` mit `dry_run: true`, dann wirksam (Job `template.migrate`); Bericht (`GET /api/v1/jobs/{id}/result`): CI `migrated`, Default `support_level`, `storage_pool` unter „Wegen Override übersprungen“, nicht unter `defaults`; danach Version 2 · Position 2, `storage_pool` „geschützt“ mit `pool-abn01`, `GET /api/v1/cis/{id}/fields` mit `protected: true` und unverändertem `override_value` | bestanden (lokal) | `backend/internal/ci/template_migration_integration_test.go` (WP-253), `backend/internal/ci/template_migration_job_integration_test.go` (WP-712; laut Traceability statt `backend/internal/citype/migrate_job_integration_test.go`) und `backend/internal/ci/bulk_override_integration_test.go` (WP-306) |

Lauf: lokaler Lauf am 2026-11-08 auf dem Stand von WP-330 (Projekte chromium
und mobile-chrome, `--retries=0`), bestanden; Firefox und WebKit waren lokal
nicht installiert. Der Lauf im CI-Job `e2e` in allen sechs Projekten
(chromium, edge, firefox, webkit, mobile-chrome, mobile-safari) mit Run-ID und
Datum wird nach dem Merge hier nachgetragen (datierter Vermerk); bis dahin
ist (c) für G2 nicht freigegeben.

Änderung nach dem Einfrieren, 2026-11-13 (WP-880, Nachtrag `p3-pkg-model`):
`GET /api/v1/entitlements` antwortet seit WP-880 mit `plan`,
`plan_changed_at` und `origin`. Der Mock in
`frontend/e2e/metamodel-scenario.spec.ts` baut die Antwort daher über den
gemeinsamen Helfer `entitlements()` aus `e2e/support/backend.ts` statt über
eine eigene Liste. Daten, Schritte und Erwartungen bleiben unverändert; die
Prüfsumme der Spec in `artefakte/SHA256SUMS` ist neu.

## (d) Onboarding-Szenario

**Ablauf:** Self-Signup, Collector-Enrollment, Scope bestätigen, 30 simulierte
Geräte, Review auflösen, Impact abfragen, Export laden.

**Umgebung:** Frische Installation mit `install-cloud.sh` auf Ubuntu 24.04 im
Workflow `install-smoke.yml`. Es gibt keine Mocks. Der Mail-Catcher fängt die
Bestätigungs-Mail ab, und der Collector läuft im Simulationsmodus mit festem
Gerätesatz.

**Eingefrorene Eingaben:**

- Signup mit E-Mail `admin-<Zeitstempel>@muster-e2e.de` und Passwort nach
  Richtlinie
- 30 simulierte Geräte (`SIMULATED_DEVICES`)
- Scope-Bestätigung des Collectors

Die Werte stehen in `frontend/e2e/onboarding.spec.ts` und
`frontend/e2e/fixtures/setup-onboarding.sh`. Die Prüfsummen des eingefrorenen
Stands stehen in `artefakte/SHA256SUMS`.

Änderung nach dem Einfrieren: WP-357 (`t2-mfa-accounts`, AUT-09, F-005).
Der erste org_admin eines Signups erhält die Realm-Rolle `mfa_required` und
richtet beim ersten Login OTP ein. `keycloakLogin` in
`frontend/e2e/onboarding.spec.ts` durchläuft dazu den OTP-Schritt
(`frontend/e2e/support/totp.ts`: Einrichtung beim ersten Login, Code bei
jedem weiteren). Eingaben und Schritte des Szenarios bleiben unverändert;
die Prüfsumme der Datei in `artefakte/SHA256SUMS` ist neu.

Änderung nach dem Einfrieren, 2026-10-25: WP-456 (`t5-abn01-de`, F-299).
Schritt 7 bestand auch mit leerer Impact-Liste, weil die simulierte Flotte
keine Beziehung garantiert. `frontend/e2e/onboarding.spec.ts` legt vor dem
Impact die Beziehung `sim-srv-01` → `sim-nas-01` (`depends_on`) an und
verlangt: Der Impact von `sim-nas-01` nennt mindestens ein CI, darunter
`sim-srv-01` über die Beziehung (nicht heuristisch), und die
Ausfallsimulation zeigt es. Negativfall: Ein CI ohne Beziehung hat einen
leeren Impact, und die Ausfallsimulation zeigt den Hinweis „Dieses CI hat
keine abhängigen CIs“. Die übrigen Eingaben und Schritte bleiben unverändert;
die Prüfsumme der Datei in `artefakte/SHA256SUMS` ist neu. Lauf #122 (unten)
belegt Schritt 7 in der früheren Fassung; die verschärfte Fassung bestand am
2026-10-26 in install-smoke Run 40258372749 (Job `install-cloud-smoke (ubuntu-24.04)`,
Commit `68fa184`, PR #94; 6 passed).

Änderung nach dem Einfrieren, 2026-10-26: Korrektur zu WP-456 (`t5-abn01-de`,
F-299). Der Negativfall las die Beziehungen des CI ohne Beziehung über
`GET /api/v1/relationships?ci_id=<id>`. Diese Liste kennt keinen Filter
`ci_id` und zählte alle Beziehungen der Organisation (install-smoke auf
PR #94: „Expected: 0, Received: 58“). Die Spec fragt jetzt
`GET /api/v1/cis/<id>/relationships` ab. Erwartung und übrige Schritte
bleiben unverändert; die Prüfsumme der Datei in `artefakte/SHA256SUMS` ist
neu.

Nachtrag 2026-11-01: WP-673 (`k7n-relationship-list-filters`, F-402).
`GET /api/v1/relationships` wendet die dokumentierten Filter `ci_id`,
`source_ci_id`, `target_ci_id` und `rel_type` jetzt an; das oben beschriebene
Symptom (alle Beziehungen der Organisation trotz `ci_id`) ist behoben. Die
eingefrorenen Artefakte bleiben unverändert: Der Negativfall fragt weiter
`GET /api/v1/cis/<id>/relationships` ab, die Prüfsummen in
`artefakte/SHA256SUMS` gelten unverändert.

Änderung nach dem Einfrieren, 2026-10-26: WP-468 (`t2-ci-export`, API-05,
F-077). Der Export-Schritt lud die Datei über den veralteten Pfad
`GET /api/v1/export/cis` (deprecated, Sunset 2027-10-15), nicht über den
Exportauftrag. `frontend/e2e/onboarding.spec.ts` lädt die Datei jetzt über die
signierte `download_url` des abgeschlossenen Auftrags (`GET /api/v1/exports/<id>`)
ohne Bearer-Token; die Prüfung (mehr Zeilen als simulierte Geräte) bleibt
gleich. Die URL nennt den Objektspeicher `minio:9000`; der Workflow
`install-smoke.yml` trägt `minio` vor dem Onboarding-Lauf in `/etc/hosts` ein
(`setup-onboarding.sh` bleibt unverändert). Eingaben und übrige Schritte
bleiben unverändert; die Prüfsumme der Datei in `artefakte/SHA256SUMS` ist
neu.

Änderung nach dem Einfrieren, 2026-10-28: WP-647 (`k5n-export-public-url`,
EXP-01, F-376). Die signierte `download_url` nennt jetzt den öffentlichen
Origin der Installation (`<Basis-URL>/objects/<Bucket>/<Schlüssel>?X-Amz-…`,
`RETICORA_S3_PUBLIC_URL`); das Frontend-nginx leitet diesen Pfad nur lesend
an den Objektspeicher weiter. Der Eintrag `127.0.0.1 minio` in `/etc/hosts`,
den `install-smoke.yml` vor dem Onboarding-Lauf setzte, entfällt.
`frontend/e2e/onboarding.spec.ts` und `setup-onboarding.sh` bleiben
unverändert (gleiche Prüfsummen): Die Seite lädt die Datei jetzt vom eigenen
Origin, die Prüfung mit manipulierter Signatur (403) bleibt.

Änderung nach dem Einfrieren, 2026-10-27: WP-529 (`t7-onboarding-suite`,
UI-17, F-177, F-351, F-353). Grund: Schritt 4 „Scope bestätigen“ wurde nur
geklickt, wenn ein Vorschlag sichtbar war, und nicht geprüft; ein defekter
Resolve hätte die Suite bestehen lassen. Schritt 8 prüfte die Datei nur über
die Zeilenzahl ohne Negativfall, und es gab keinen In-App-Einstieg zu Signup
und Onboarding. Änderungen in `frontend/e2e/onboarding.spec.ts`:

- Schritt 1: Die Registrierung beginnt über den Link „Konto anlegen“ der
  Anmeldeseite. Schritt 2: Das Dashboard der neuen Organisation zeigt
  „Einrichtung fortsetzen“ (Link nach `/onboarding`); Schritt 3 erreicht das
  Onboarding über diesen Link.
- Schritt 4 ist Pflicht: Die Spec wartet auf einen offenen Vorschlag
  (`GET /api/v1/review-items?kind=scope_suggested&status=open`), klickt
  „Bestätigen“ ohne Bedingung und verlangt: das Item ist `resolved` mit
  `action` `accept`, `GET /api/v1/discovery-scopes?collector_id=<id>` enthält
  den bestätigten Bereich, der Fortschritt geht 3 → 4 → 5 (`aria-valuenow`).
  Damit Stufe 4 sichtbar ist, liefert der Browser die CI-Zahl des Onboardings
  (`GET /api/v1/cis?limit=1`) bis zur Bestätigung als 0 (`page.route`); Stufe 5
  folgt aus der echten Zahl. Negativfall: Ein per `page.route` gebrochener
  Resolve-Endpunkt (500) hält den Fortschritt bei 3, die Seite zeigt die
  Meldung, das Item bleibt offen.
- Schritt 8: Der Browser lädt die Datei über die signierte `download_url` des
  Auftrags und zählt mindestens 30 Datenzeilen. Negativfall: Eine per
  `page.route` manipulierte Signatur beantwortet der Objektspeicher mit 403,
  und der Schritt schlägt fehl.

Die Eingriffe per `page.route` betreffen nur die Sicht des Browsers bzw. die
Negativfälle; Daten und Prüfungen der Positivfälle kommen vom installierten
Stack. Eingaben bleiben unverändert; die Prüfsumme der Datei in
`artefakte/SHA256SUMS` ist neu. `install-smoke.yml` lädt den Bericht der Suite
jetzt nach jedem Lauf hoch (Artefakt `onboarding-e2e`, `retries: 0`). Grüner
install-smoke-Lauf mit dieser Fassung: [Install Smoke Test #219](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40537085229)
(Commit `0d0f176`, PR #101), Job `install-cloud-smoke (ubuntu-24.04)`, Schritt
„Run the onboarding E2E suite“, success, 2026-10-27: 6 von 6 Tests bestanden,
darunter „self-signup, verification, trial, login and dashboard“ (Schritte 1
und 2) und „collector, scopes, 30 simulated devices, review, impact and
export“ (Schritte 3 bis 8).

Änderung nach dem Einfrieren, 2026-10-27: WP-558 (`t7-onboarding-tls-origin`,
INS-04, F-352). Grund: Das INS-04-Fehlerbild „Localhost-Redirect“ wurde nicht
erkannt. Die Suite lief nur über `http://localhost:<port>`, verglich den
Origin der `redirect_uri` mit diesem localhost-Origin und übersprang die
Issuer-Prüfung ohne `E2E_ISSUER_URL`. Änderungen:

- `frontend/e2e/onboarding.spec.ts`: Die Prüfung der Authorize-Weiterleitung
  verlangt `E2E_ISSUER_URL` und schlägt ohne die Variable fehl, statt zu
  überspringen. Ist `BASE_URL` kein Loopback-Origin, nennen Authorize-URL,
  `redirect_uri` und Issuer weder localhost noch eine Loopback-Adresse und
  haben den Origin von `BASE_URL`. Neu ist ein Selbsttest: `page.route`
  schreibt die `redirect_uri` der Authorize-Weiterleitung auf
  `http://localhost:1/auth/callback` um, und die Prüfung muss fehlschlagen.
  Die INS-04-Tests tragen das Tag `@ins-04`. Der Test zum unsicheren Origin
  trägt zusätzlich `@plain-http`, weil eine TLS-Installation HTTP auf HTTPS
  umleitet.
- `frontend/e2e/fixtures/setup-onboarding.sh`: Bei `RETICORA_TLS_DOMAIN` in
  der Env-Datei ist `BASE_URL` `https://<Domain>`. Das Skript prüft das
  Frontend dort und vertraut dabei der Wurzel aus `E2E_CA_CERT`. Es setzt
  `E2E_ISSUER_URL` auf den Issuer der Installation und bricht ab, wenn
  dieser fehlt. Ohne TLS-Domain bleibt `BASE_URL`
  `http://localhost:<RETICORA_FRONTEND_PORT>`.

Eingaben und die Schritte 1 bis 8 bleiben unverändert. Die Prüfsummen beider
Dateien in `artefakte/SHA256SUMS` sind neu. Der Job `tls-domain-pebble` in
`install-smoke.yml` behält die Installation von
`tests/install-acme-pebble.test.sh` mit dem Pebble-Zertifikat. Danach laufen
dort die INS-04-Tests (`--grep @ins-04 --grep-invert @plain-http`) über
`https://<Domain>`. Die Suite vertraut der Pebble-Wurzel über
`playwright.install.config.ts` (NSS-Datenbank, `NODE_EXTRA_CA_CERTS`) statt
über `ignoreHTTPSErrors`. Lauf-ID des Jobs `tls-domain-pebble`:
[Install Smoke Test 40613604691](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40613604691)
(Job 122571465336, Stand `372d442`, PR #102), grün; Nachtrag vom 2026-10-28.
Damit ist der Retest von F-352 bestanden.

Änderung nach dem Einfrieren, 2026-11-08 (Nachzug zu PR #137, Design-System):
Die Review-Inbox zeigt die Queue jetzt als Tabelle statt als Liste.
`frontend/e2e/onboarding.spec.ts` sucht das erste offene Review-Item daher
unter `main tbody tr button` statt `main ul li button`. Daten, Schritte und
Erwartungen bleiben unverändert; die Prüfsumme der Spec in
`artefakte/SHA256SUMS` ist neu. PR #137 hatte die Spec angepasst, die
Prüfsumme aber nicht nachgezogen (Job `gate` auf `main` rot).

Änderung nach dem Einfrieren, 2026-11-13 (WP-873, Nachtrag `p3-sr60-thirdparty-pins`):
Keycloak 26.8.2 verlangt für die Admin-REST-API den Client-Scope
`kc-admin-api`. `frontend/e2e/fixtures/setup-onboarding.sh` legt ihn für den
Admin-Client als Default-Scope an, wie `install-cloud.sh`. Daten, Schritte und
Erwartungen bleiben unverändert; die Prüfsumme der Fixture in
`artefakte/SHA256SUMS` ist neu.

| # | Schritt | Erwartung | Ergebnis |
|---|---|---|---|
| 1 | Self-Signup | über den Link „Konto anlegen“ der Anmeldeseite; Bestätigungs-Mail, Organisation mit Trial | bestanden (Lauf #219, Fassung WP-529; frühere Fassung in Lauf #122) |
| 2 | Login, Dashboard | Dashboard der neuen Organisation mit „Einrichtung fortsetzen“ | bestanden (Lauf #219, Fassung WP-529; frühere Fassung in Lauf #122) |
| 3 | Collector-Enrollment | Einmal-Code, Zertifikat, Collector online | bestanden |
| 4 | Scope bestätigen | Pflichtschritt: Vorschlag bestätigt, Item `resolved`/`accept`, Scope des Collectors angelegt, Fortschritt 3 → 4 → 5; gebrochener Resolve hält den Fortschritt bei 3 mit Meldung | bestanden (Lauf #219, Fassung WP-529 mit Negativfall; frühere bedingte Fassung in Lauf #122) |
| 5 | 30 simulierte Geräte | 30 CIs sichtbar | bestanden |
| 6 | Review auflösen | Review geschlossen, Ergebnis am CI | bestanden |
| 7 | Impact abfragen | betroffene CIs zum gewählten CI: nach der Beziehung `sim-srv-01` → `sim-nas-01` nennt der Impact von `sim-nas-01` `sim-srv-01` (API und Ausfallsimulation); ein CI ohne Beziehung hat keinen Impact, mit Hinweis in der UI | bestanden (install-smoke Run 40258372749; frühere Fassung in Lauf #122 bestanden) |
| 8 | Export laden | Exportdatei mit mindestens 30 Datenzeilen, im Browser geladen über die signierte `download_url` des Exportauftrags; manipulierte Signatur → 403, Schritt schlägt fehl | bestanden (Lauf #219, Fassungen WP-468 und WP-529 mit Negativfall; frühere Fassung über `/api/v1/export/cis` in Lauf #122) |

Läufe: [Install Smoke Test #219](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40537085229) (aktuelle Fassung, 2026-10-27) und
[Install Smoke Test #122](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/38512314064)
(Fassung vom 2026-10-14), jeweils Job `install-cloud-smoke (ubuntu-24.04)`,
Schritt „Run the onboarding E2E suite“, success.

## (e) Konflikt-Szenario

**Ablauf:** Override setzen, abweichende Discovery, override_conflict sichtbar,
accept und dismiss.

**Umgebung:** Real-Stack. `frontend/e2e-real/abn-01-konflikt.spec.ts` läuft
gegen die frisch installierte Instanz im Workflow `install-smoke.yml`, Job
`install-cloud-smoke (ubuntu-24.04)`, Schritt „Run the real-stack E2E suite
against the expected-failure list“ (WP-338). Es gibt keine Mocks: Die
Discovery-Datensätze laufen durch den Ingest des Stacks
(`POST /api/v1/ingest/bulk`, Abgleich), und jeder Schritt wird in der UI,
über die API und in der Datenbank (psql, nur lesend) geprüft.

**Eingefrorene Daten:** `artefakte/abn-01-e-konflikt.json`. Die Real-Stack-Spec
und der gemockte Lauf lesen die Datei direkt, sodass Protokoll und Tests
dieselben Daten verwenden. Die Datei legt fest:

- CI `srv-abn-01`, entdecktes Modell `R-1U` (SNMP)
- Override `R-2U-manual`, Begründung „Abnahme ABN-01“
- zwei `override_conflict`-Reviews: `srv-abn-01`/model mit beobachtetem Wert
  `R-3U` (accept) und `srv-abn-02`/os_name mit Override `Linux (manual)` und
  beobachtetem Wert `Linux 6.1` (dismiss)

Nach einem grünen Lauf schreibt die Spec die Datei aus dem Lauf neu (IDs,
Zeitpunkte, Run-ID); die Szenariowerte muss der Lauf unverändert
reproduzieren. Die Übernahme beschreibt `artefakte/README.md`.

| # | Schritt | Erwartung (UI, API, Datenbank) | Ergebnis |
|---|---|---|---|
| 0 | Erste Discovery | `srv-abn-01` mit Modell `R-1U` aus `snmp`, `srv-abn-02` ohne Betriebssystem (API, `ci_field_value`) | bestanden |
| 1 | CI-Detail, „Herkunft & Overrides“, Override setzen | Dialog: `PUT /cis/{id}/fields/model/override` 200; Feld „geschützt“, Override und Effektiv `R-2U-manual`, Discovery `R-1U`; Herkunft mit Autor und Begründung; `ci.model` und `override_value` in der Datenbank. Override `Linux (manual)` auf `srv-abn-02`/os_name über denselben Endpunkt | bestanden |
| 2 | Abweichende Discovery | Ingest meldet `R-3U` und `Linux 6.1`; beide Overrides bleiben, die Herkunft zeigt den gemeldeten Wert „abweichend“; je Feld genau ein offenes `override_conflict` mit dem gemeldeten Wert | bestanden |
| 3 | override_conflict sichtbar | Review-Inbox, Typ „Konflikt mit Override“: zwei Einträge; der Diff nennt `srv-abn-01`, beobachtet `R-3U`, im CI `R-2U-manual`, „abweichend“ | bestanden |
| 4 | accept | „Übernehmen“: Review `resolved` mit `accept` und Bearbeiter; Override entfernt, `srv-abn-01` hat das Modell `R-3U` | bestanden |
| 5 | dismiss | „Verwerfen“: Review `dismissed` mit `dismiss`; Override `Linux (manual)` bleibt; „Keine Review-Items“; beide Ergebnisse nach einem Reload | bestanden |

Lauf: install-smoke Run 40258372749 vom 2026-10-26 (Job `install-cloud-smoke (ubuntu-24.04)`,
Commit `68fa184`, PR #94), bestanden in 12,1 s. Die Spec verglich dabei die
Szenariowerte des Laufs mit `artefakte/abn-01-e-konflikt.json` (sie
stimmten überein) und schrieb die Datei des Laufs ins Testergebnis
(Artefakt `e2e-real`, 14 Tage aufbewahrt). Übernahme nach `artefakte/` und neue
Prüfsumme in `artefakte/SHA256SUMS`: ausstehend, weil das Artefakt aus der
Arbeitsumgebung nicht abrufbar war; bis dahin gilt die eingefrorene
Fassung.

Änderung nach dem Einfrieren, 2026-10-25: WP-456 (`t5-abn01-de`, F-298).
Das Protokoll stützte sich nur auf den gemockten Lauf unten. Neu ist die
Real-Stack-Spec `frontend/e2e-real/abn-01-konflikt.spec.ts` mit den
eingefrorenen Daten; sie steht mit eigener Prüfsumme in
`artefakte/SHA256SUMS`. `artefakte/abn-01-e-konflikt.json` und
`frontend/e2e/conflict.spec.ts` bleiben bis zum ersten grünen Real-Lauf
unverändert. Die allgemeine Real-Stack-Prüfung von Override und Konflikt mit
der simulierten Flotte bleibt `frontend/e2e-real/discovery-override.spec.ts`
(D03–D05); sie ist nicht eingefroren, weil mehrere Korrektur-WPs sie ändern.

Änderung nach dem Einfrieren, 2026-10-27: WP-530 (`t7-e2e-strict-mocks`,
TST-03, F-178). Der gemockte Lauf nutzt jetzt das strikte Mock-Backend:
Unbeantwortete Aufrufe schlagen fehl, und jeder Request und jede
Mock-Antwort wird gegen `api/openapi.yaml` geprüft.
`frontend/e2e/conflict.spec.ts` importiert dafür `test` aus
`e2e/support/backend.ts`, und die Mock-Antworten tragen die Pflichtfelder des
Vertrags: `is_manual` am CI, `organization_id` und `updated_at` an den
Reviews, die `FieldProvenance` als Antwort des Overrides. Die nicht
betrachteten Panels der CI-Detailseite stehen in der Allowlist der Spec.
`artefakte/abn-01-e-konflikt.json`, die Schritte und die Erwartungen bleiben
unverändert; die Prüfsumme der Spec in `artefakte/SHA256SUMS` ist neu.

Änderung nach dem Einfrieren, 2026-10-27 (Integration K4): Die Prüfung
„geschützt“ nach dem Speichern des Overrides sucht den Text jetzt exakt
(`getByText('geschützt', { exact: true })`). Ohne `exact` traf der Locator
auch den Hinweis des Abschnitts („Geschützte manuelle Overrides …“), der vor
dem Speichern sichtbar ist. Der Schritt galt dann als erledigt, bevor der
PUT ankam, oder schlug nach dem Speichern mit zwei Treffern fehl
(Strict Mode). Unter Last wurde das sichtbar. Daten, Schritte und
Erwartungen bleiben unverändert; die Prüfsumme der Spec in
`artefakte/SHA256SUMS` ist neu.

Änderung nach dem Einfrieren, 2026-11-08 (Nachzug zu PR #137, Design-System):
Die CI-Detailseite trägt den CI-Namen jetzt als Überschrift der Ebene 1, die
Review-Inbox zeigt die Queue als Tabelle und daneben den Vergleich des
geöffneten Items als zweite Tabelle. `frontend/e2e-real/abn-01-konflikt.spec.ts`
sucht deshalb die Überschrift der Ebene 1, die Einträge unter
`main tbody tr button` und die Vergleichstabelle über die Spalte
„Beobachtet“; die Status „Erledigt“/„Verworfen“ prüft sie an der Tabellenzeile.
Daten, Schritte und Erwartungen bleiben unverändert; die Prüfsumme der Spec in
`artefakte/SHA256SUMS` ist neu.

**Gemockter Lauf (Regression, Freigabe vom 2026-10-14):**
`frontend/e2e/conflict.spec.ts` spielt dieselben Daten mit gemocktem Backend
ab. Der Test läuft in allen Browser-Projekten des CI-Jobs `e2e`:
Chromium, Edge, Firefox, WebKit, Mobile Chrome und Mobile Safari.

| # | Schritt | Erwartung | Ergebnis |
|---|---|---|---|
| 1 | CI-Detail, „Herkunft & Overrides“, Override setzen | PUT auf `/cis/{id}/fields/model/override` mit Wert und Begründung; Feld „geschützt“ | bestanden |
| 2 | Abweichende Discovery | Die Herkunft liefert nach dem Override den abweichenden Discovery-Wert (`diverged`), und das Backend legt die Konflikt-Reviews an (Daten der Artefaktdatei) | eingespielt |
| 3 | override_conflict sichtbar | Review-Liste zeigt „Konflikt mit Override“ für `srv-abn-01` | bestanden |
| 4 | accept | `POST /review-items/conflict-accept/resolve` mit `action: accept`; Eintrag verschwindet | bestanden |
| 5 | dismiss | `POST /review-items/conflict-dismiss/resolve` mit `action: dismiss`; „Keine Review-Items“ | bestanden |

Die Wirkung im Backend belegen die Integrationstests:

- **accept:** Das CI übernimmt den beobachteten Wert.
- **dismiss:** Der Override bleibt.

Nachweis: `backend/internal/reviews/conflict_integration_test.go`, WP-089.
Den Schutz des Overrides bei gleichzeitiger Discovery belegt
`TestDiscoveryConcurrency` (ABN-03).

## Freigabe

| Rolle | Name | Datum | Ergebnis |
|---|---|---|---|
| Auftragnehmer | – | 2026-10-14 | (d) und (e) bestanden; (a) und (b) N/P (E-02) |
| Auftraggeber | – | 2026-10-15 | G1 freigegeben; (a) und (b) laut Erklärung erledigt (E-02); nach dem Beschluss vom 2026-10-28 ist die Erklärung der Nachweis für G1 und G2, die Texte werden nicht nachgereicht (E-103) |
| Auftragnehmer (G2) | – | ausstehend | G2: (c) nach dem grünen Lauf im CI-Job `e2e` in allen sechs Projekten; (a) und (b) PASS nach E-103; (d) und (e) unverändert eingefroren |
| Auftraggeber (G2) | – | ausstehend | G2-Freigabe von ABN-01 |
