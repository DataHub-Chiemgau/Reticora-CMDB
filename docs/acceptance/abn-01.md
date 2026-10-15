# Abnahme ABN-01 – Protokoll G1

WP-212 (`g1-abnahme`). ABN-01 legt die Abnahmeszenarien fest
(`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md`, Abschnitt 45). Für
G1 zählen die Szenarien (d) und (e). Beide sind [B] und laufen als
automatisierte Protokolle gegen eingefrorene Daten. Die Daten liegen in
`docs/acceptance/artefakte/`.

| Teil | Szenario | Tag | Status G1 | Nachweis |
|---|---|---|---|---|
| (a) | CMDB-Szenario, 34 Schritte, aus V §65 | [Q] | PASS (Erklärung des Auftraggebers) | Abnahme durch Erklärung des Auftraggebers vom 2026-10-15 (B4); Originaltext nachzureichen (E-02, A-02) |
| (b) | System-Szenario, 20 Schritte, aus V §66 | [Q] | PASS (Erklärung des Auftraggebers) | Abnahme durch Erklärung des Auftraggebers vom 2026-10-15 (B4); Originaltext nachzureichen (E-02, A-02) |
| (c) | Metamodell-Szenario | [P2] | nicht G1 | Phase 2 |
| (d) | Onboarding-Szenario | [B] | **PASS** | `frontend/e2e/onboarding.spec.ts`, Lauf auf der frischen Installation (`docs/acceptance/g1-installation.md`) |
| (e) | Konflikt-Szenario | [B] | **PASS** | `frontend/e2e/conflict.spec.ts` mit `artefakte/abn-01-e-konflikt.json` |

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

| # | Schritt | Erwartung | Ergebnis |
|---|---|---|---|
| 1 | Self-Signup | Bestätigungs-Mail, Organisation mit Trial | bestanden |
| 2 | Login, Dashboard | Dashboard der neuen Organisation | bestanden |
| 3 | Collector-Enrollment | Einmal-Code, Zertifikat, Collector online | bestanden |
| 4 | Scope bestätigen | Scope aktiv | bestanden |
| 5 | 30 simulierte Geräte | 30 CIs sichtbar | bestanden |
| 6 | Review auflösen | Review geschlossen, Ergebnis am CI | bestanden |
| 7 | Impact abfragen | betroffene CIs zum gewählten CI | bestanden |
| 8 | Export laden | Exportdatei mit den CIs | bestanden |

Lauf: [Install Smoke Test #122](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/38512314064),
Job `install-cloud-smoke (ubuntu-24.04)`, Schritt „Run the onboarding E2E
suite“, success, 2026-10-14.

## (e) Konflikt-Szenario

**Ablauf:** Override setzen, abweichende Discovery, override_conflict sichtbar,
accept und dismiss.

**Umgebung:** Playwright gegen die SPA mit gemocktem Backend. Der Test läuft in
allen Browser-Projekten des CI-Jobs `frontend`: Chromium, Edge, Firefox,
WebKit, Mobile Chrome und Mobile Safari.

**Eingefrorene Daten:** `artefakte/abn-01-e-konflikt.json`. Der Test liest die
Datei direkt, sodass Protokoll und Test dieselben Daten verwenden. Die Datei
legt fest:

- CI `srv-abn-01`, entdecktes Modell `R-1U` (SNMP)
- Override `R-2U-manual`, Begründung „Abnahme ABN-01“
- zwei `override_conflict`-Reviews: `srv-abn-01`/model mit beobachtetem Wert
  `R-3U` (accept) und `srv-abn-02`/os_name (dismiss)

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
