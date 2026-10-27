# Abnahme ABN-01 – Protokoll G1

WP-212 (`g1-abnahme`). ABN-01 legt die Abnahmeszenarien fest
(`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md`, Abschnitt 45). Für
G1 zählen die Szenarien (d) und (e). Beide sind [B] und laufen als
automatisierte Protokolle gegen eingefrorene Daten. Die Daten liegen in
`docs/acceptance/artefakte/`.

| Teil | Szenario | Tag | Status G1 | Nachweis |
|---|---|---|---|---|
| (a) | CMDB-Szenario, 34 Schritte, aus V §65 | [Q] | offen | Erklärung des Auftraggebers vom 2026-10-15 (B4), Nachweis ausstehend: Originaltext nachzureichen (E-02, A-02; WP-457) |
| (b) | System-Szenario, 20 Schritte, aus V §66 | [Q] | offen | Erklärung des Auftraggebers vom 2026-10-15 (B4), Nachweis ausstehend: Originaltext nachzureichen (E-02, A-02; WP-457) |
| (c) | Metamodell-Szenario | [P2] | nicht G1 | Phase 2 |
| (d) | Onboarding-Szenario | [B] | **PASS** | `frontend/e2e/onboarding.spec.ts`, Lauf auf der frischen Installation (`docs/acceptance/g1-installation.md`); Schritt 7 seit WP-456 verschärft, bestanden in install-smoke Run 40258372749 (2026-10-26) |
| (e) | Konflikt-Szenario | [B] | **PASS** | `frontend/e2e-real/abn-01-konflikt.spec.ts` (Real-Stack, install-smoke Run 40258372749, bestanden) und `frontend/e2e/conflict.spec.ts` (gemockt), beide mit `artefakte/abn-01-e-konflikt.json` |

## (a) und (b): v2-Texte nach E-02

ABN-01 verlangt, dass beide Szenarien „unverändert“ aus V §65–66 übernommen und
vor G1 eingefroren werden. Die v2-Texte hat der Auftraggeber noch nicht
geliefert (E-02, offen). Nach der Übergangsregel aus E-02 werden keine
Schritte erfunden. Sobald die Texte vorliegen, kommen sie unverändert als
`abn-01-a-cmdb.md` und `abn-01-b-system.md` in dieses Verzeichnis. Jeder
Schritt wird dann einem Test oder einer manuellen Prüfung zugeordnet. Bis
dahin bleibt der Punkt ein Gate-Blocker (B4 in `docs/acceptance/g1-gate.md`).

Nachtrag 2026-10-15: Der Auftraggeber hat B4 am 2026-10-15 für erledigt
erklärt. Die Texte liegen im Repository weiterhin nicht vor; (a) und (b) sind
durch diese Erklärung abgenommen, nicht durch einen Test. Nachzureichen sind
die Texte in `docs/spec/`; danach kommen sie wie oben beschrieben unverändert
in dieses Verzeichnis, und jeder Schritt wird zugeordnet.

Nachtrag 2026-10-25 (WP-451, E-76): Die Erklärung des Auftraggebers ersetzt
den Nachweis nicht. (a) und (b) stehen auf „offen“ mit der Begründung
„Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend“; ABN-01
steht in `docs/acceptance/g1-gate.md` auf „offen“ und in
`docs/acceptance/g1-ausnahmen.csv`, bis die Szenariotexte eingefroren und
Schritt für Schritt protokolliert sind (WP-457). (d) und (e) bleiben durch
Tests belegt.

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

| # | Schritt | Erwartung | Ergebnis |
|---|---|---|---|
| 1 | Self-Signup | Bestätigungs-Mail, Organisation mit Trial | bestanden |
| 2 | Login, Dashboard | Dashboard der neuen Organisation | bestanden |
| 3 | Collector-Enrollment | Einmal-Code, Zertifikat, Collector online | bestanden |
| 4 | Scope bestätigen | Scope aktiv | bestanden |
| 5 | 30 simulierte Geräte | 30 CIs sichtbar | bestanden |
| 6 | Review auflösen | Review geschlossen, Ergebnis am CI | bestanden |
| 7 | Impact abfragen | betroffene CIs zum gewählten CI: nach der Beziehung `sim-srv-01` → `sim-nas-01` nennt der Impact von `sim-nas-01` `sim-srv-01` (API und Ausfallsimulation); ein CI ohne Beziehung hat keinen Impact, mit Hinweis in der UI | bestanden (install-smoke Run 40258372749; frühere Fassung in Lauf #122 bestanden) |
| 8 | Export laden | Exportdatei mit den CIs, geladen über die signierte `download_url` des Exportauftrags | frühere Fassung (Download über `/api/v1/export/cis`) in Lauf #122 bestanden; Fassung WP-468 steht aus (install-smoke) |

Lauf: [Install Smoke Test #122](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/38512314064),
Job `install-cloud-smoke (ubuntu-24.04)`, Schritt „Run the onboarding E2E
suite“, success, 2026-10-14.

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

**Gemockter Lauf (Regression, Freigabe vom 2026-10-14):**
`frontend/e2e/conflict.spec.ts` spielt dieselben Daten mit gemocktem Backend
ab. Der Test läuft in allen Browser-Projekten des CI-Jobs `frontend`:
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
| Auftraggeber | – | 2026-10-15 | G1 freigegeben; (a) und (b) laut Erklärung erledigt, Texte nachzureichen (E-02) |
