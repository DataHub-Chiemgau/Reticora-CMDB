# Gate G1 – Checkliste

WP-213 (`g1-gate`). Die Checkliste prüft die Gate-Regeln GATE-01 bis GATE-05
(`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md`, Abschnitt „Gates“)
für Phase 1. Die Nachweise sind Tests und die Traceability
(`docs/traceability.csv`).

Prüfungen:

- `backend/internal/traceability` (`TestTraceabilityFile`) blockiert jede
  [B]/[Q]-ID ohne Traceability-Zeile (E-30).
- `tests/rep01-structure.test.sh` prüft:
  - die REP-01-Struktur
  - die Abschnitte dieser Checkliste
  - offene Critical-Befunde
  - die Begründung jeder N/P- und WARN-Zeile
  - [O]-IDs
  - die Workflows
- Beide laufen in jedem PR (CI-Jobs `traceability` und `gate`).

Stand: 2026-10-14, Release-Kandidat `main` nach PR „Gate G1: WP-211 – WP-213“.

## Ergebnis

**G1 ist nicht bestanden.** Strukturell ist das Gate vollständig:

- Jede [B]/[Q]-ID hat einen Nachweis.
- Die Spätphasen-Module sind abgeschaltet.
- Es gibt kein offenes Critical.

Diese Punkte blockieren:

| # | Blocker | Regel | Verantwortlich | Weg |
|---|---|---|---|---|
| B1 | Ingest-Durchsatz 58 statt 200 Records/s je Instanz (NFR-03, Befund SR-02, High) | GATE-02, GATE-03 (Lasttest bestanden), Sicherheitsreview ohne offene High | Auftragnehmer | WP-332 bis WP-334 (`docs/plan/implementierungsplan.md`, G1-Nachbesserung; E-56 offen) |
| B2 | Impact im Worst Case unter Parallellast p95 2,39 s statt < 2 s (NFR-02, SR-05) | GATE-02 | Auftragnehmer | WP-335 (`docs/plan/implementierungsplan.md`, G1-Nachbesserung); Referenzhost bleibt SR-06 |
| B3 | Externer Pentest ohne offene High/Critical (NFR-08 nach E-28) | GATE-03 | Auftraggeber (E-28, A-07) | `docs/security/pentest-g1.md` |
| B4 | v2-Originaltexte fehlen: AUT-05, AUT-06, NET-04, ABN-01 (a)/(b) | GATE-02 | Auftraggeber (E-02, A-02) | Texte liefern, danach WPs nach Plan |
| B5 | Skalierungsziel unbestätigt (E-17) | – (kein eigener Gate-Punkt; gemessen mit 10.000 Objekten nach CH15) | Auftraggeber | Bestätigung oder neuer Zielwert, dann Nachmessung |

B1 bis B4 müssen erledigt sein, bevor G1 freigegeben wird. Ist B5 nicht
bestätigt, gilt der Lasttest mit 10.000 Objekten.

## GATE-01 – Kein offenes Critical

| Quelle | Critical offen | Nachweis |
|---|---:|---|
| Audit-Befunde (225 Critical/High) | 0 | `docs/security/review-g1.md`, Abschnitt „Audit-Befunde Critical/High“: 41 geschlossen, 8 mit G1-Anteil geschlossen |
| Neue Befunde des Sicherheitsreviews (SR-01 bis SR-07) | 0 | `docs/security/review-g1.md`: kein Critical |
| Abhängigkeits- und Image-Scans | 0 | govulncheck, npm audit, Trivy blockierend in CI |
| Externer Pentest | ausstehend | `docs/security/pentest-g1.md` |

**Status:** PASS für den internen Stand. `tests/rep01-structure.test.sh`
schlägt fehl, sobald eine Critical-Zeile in Review oder Pentest nicht
„behoben“ ist. Der Pentest kann neue Befunde bringen (B3).

## GATE-02 – Alle Anforderungen der Phase sind PASS

Für G1 gilt das für alle IDs mit Tag [B] und für die [Q]-Punkte. Für
gemischte Tags wie „[B], Teilumfang [P2]“ zählt der [B]-Anteil.

- 220 IDs sind betroffen: 219 mit Tag im Katalog und SEC-05, dessen Tag im
  Katalog fehlt und in der Traceability als [B] geführt ist. Jede hat eine
  Zeile in `docs/traceability.csv`; die Prüfung blockiert, sobald eine fehlt.
- Der Status je ID steht im [Anhang](#anhang-status-je-bq-id).
- Abweichungen von PASS stehen in den beiden Tabellen unten.

### N/P und WARN

WARN ist nach GATE-02 nur für SOLL-Punkte zulässig (E-27). Phase 1 hat keine
SOLL-Punkte mit WARN. Diese IDs sind nicht beurteilbar oder gehören nicht zu
G1:

| ID | Status | Begründung |
|---|---|---|
| AUT-05 | N/P | Text „[B] unverändert“ verweist auf v2; die v2-Originaltexte fehlen (E-02, Aufgabe A-02). Nach der Übergangsregel werden keine Funktionen erfunden. Blocker B4. |
| AUT-06 | N/P | Wie AUT-05: Verweis auf v2 ohne Originaltext (E-02). Blocker B4. |
| NET-04 | N/P | „cable(...) unverändert“: Felder und Regeln stehen nur in v2 (E-02). Das bestehende Kabelmodell bleibt unverändert. Blocker B4. |
| NFR-08 | N/P | „Sicherheit nach SEC-11“: SEC-11 ist mit WP-209 belegt (`docs/security/review-g1.md`); der externe Pentest steht nach E-28 aus. Blocker B3. |
| GATE-04 | N/P | Gate-Regel für GA (COL-07, AI-02), nicht für G1. Die Add-ons sind nach E-03 abgeschaltet (WP-214). |

### FAIL und offen

| ID | Status | Begründung |
|---|---|---|
| NFR-02 | FAIL | Alle Budgets bis auf Impact im Worst Case unter Parallellast erfüllt (`docs/acceptance/lasttest-g1.md`). Blocker B2. |
| NFR-03 | FAIL | 58 statt 200 Records/s. Datenverlust tritt nicht auf. Blocker B1. |
| NFR-10 | FAIL | Der Lasttest lief, ist wegen NFR-03 aber nicht bestanden. Blocker B1. |
| GATE-03 | offen | Pentest ausstehend (B3), Lasttest nicht bestanden (B1). Isolationstest und Install-Smoke sind belegt (siehe GATE-03). |
| GATE-02 | FAIL | Folgt aus den Zeilen oben. |
| ABN-01 | PASS für (d) und (e) | Die Teile (a) und (b) sind N/P nach E-02 (B4); (c) ist [P2]. Siehe `docs/acceptance/abn-01.md`. |

### Spätphasen-Module (E-03)

Nach E-03 sind alle Module späterer Phasen im G1-Release abgeschaltet. Sie
gehören damit nicht zum Gate-Umfang. WP-214 (`m-gates`, PR #59) ist vor WP-213
gemergt.

- **Routen nicht registriert:** Ohne `RETICORA_MODULES` (Standard des
  Release) ist kein Spätphasen-Modul aktiv. `authorizingRouter` registriert
  keine Route unter den Präfixen dieser Module (`LateModules` in
  `backend/internal/server/modules.go`). Nachweis:
  `TestG1DefaultSwitchesLateModulesOff`. `TestLateRoutesAreMarkedInSpecification`
  prüft, dass diese Routen in `api/openapi.yaml` ein `x-phase` ab P2 tragen.
- **Worker aus:** Die Worker eines Moduls starten nur, wenn das Modul aktiv
  ist (`WorkerAllowed`).
- **Oberfläche:** Das Frontend blendet Navigation und Seiten über
  `/api/v1/modules` aus (`useModules`,
  `TestModulesEndpointListsTheReleaseModules`).

Die 29 Audit-High-Befunde in diesem Code sind nach E-03 auf WP-215 bis WP-239
verschoben (`docs/security/review-g1.md`).

## GATE-03 – Zusatzbedingungen G1

| Bedingung | Status | Nachweis |
|---|---|---|
| Lasttest (NFR-10) bestanden | FAIL | `docs/acceptance/lasttest-g1.md` (B1, B2) |
| Pentest ohne offene High/Critical | offen | `docs/security/pentest-g1.md` (B3) |
| Isolationstest (TEN-09) vollständig | PASS | `backend/internal/tenant/rls/channel_matrix_integration_test.go` (WP-208) |
| Install-Smoke auf beiden Plattformen | PASS | `docs/acceptance/g1-installation.md` (WP-211) |

## GATE-04 – GA

Diese Regel betrifft G1 nicht (siehe N/P).

## GATE-05 – Kein Gate wird durch [O]-Punkte beeinflusst

Die Gate-Prüfung wählt nur [B]- und [Q]-Tags aus (`RequiredForGate` in
`backend/internal/traceability/traceability.go`, Unit-Test
`TestRequiredForGateSelectsBaselineAndQualityTags`). [O]-IDs haben keine
G1-Zeile; `tests/rep01-structure.test.sh` prüft das. Die drei [O]-IDs des
Katalogs und die [O]-Anteile gemischter Tags (z. B. „[B], Air-Gapped [O]“)
gehen in keinen Blocker ein.

**Status:** PASS.

## SEQ-01 – Reihenfolge je Epic

SEQ-01 legt für Phase 1 fest: Epic A → Epic B ∥ Epic D → Epic C → Lasttest
(NFR-10) → Sicherheitsreview → G1.

Nach E-07 läuft M0 formal als Teil der Epics A, B und D. Jedes M0-WP trägt
sein fachliches Epic in der Spalte „Epic (Traceability)“. Die Merge-Folge auf
`main`:

| Schritt | Epic (Traceability) | PRs | gemergt | WPs |
|---|---|---|---|---|
| 1 | M0.0 Werkzeugkette (Epic A) | #40 – #43 | 2026-10-01 | WP-001 – WP-009 und Vorarbeiten der Runde 3 |
| 2 | M0.1/M0.2 Stabilisierung (Epics B, A, D je WP) | #44 – #51 | 2026-10-04 – 2026-10-12 | WP-010 – WP-091 |
| 3 | Epic A – Installation und Betrieb | #52 | 2026-10-12 | WP-093 – WP-104 |
| 4 | Epic B – Fundament | #53, #54, #55 | 2026-10-13 | WP-107 – WP-142, WP-240 – WP-242 |
| 4 | Epic D – Collector und Discovery (parallel zu B) | #56 | 2026-10-13 | WP-143 – WP-162 |
| 5 | Epic C – API, Kernfunktionen, UI | #58 | 2026-10-14 | WP-163 – WP-202 |
| – | M-S: WP-214 Feature-Gates (E-03, vor G1) | #59 | 2026-10-14 | WP-214 |
| 6 | Lasttest (NFR-10) | #60 | 2026-10-14 | WP-203 – WP-207 |
| 7 | Sicherheitsreview | #61 | 2026-10-14 | WP-208 – WP-210 |
| 8 | G1 | dieser PR | 2026-10-14 | WP-211 – WP-213 |

PR #57 (Planung Phase 2) enthält kein Arbeitspaket. Die einzige Abweichung von
SEQ-01 ist das Vorziehen von M0; E-07 erlaubt sie.

**Status:** PASS.

## DOD-01 und GLO-11

- **DOD-01:** Die PR-Vorlage `.github/pull_request_template.md` enthält die
  Definition of Done. Die CI liest die WPs eines PRs und verlangt deren
  Traceability-Zeilen.
- **GLO-11:** Seit diesem WP blockiert die Traceability-Prüfung vollständig
  für [B]/[Q] (E-30). Fehlende [Pn]/[A]-IDs späterer Phasen werden berichtet
  und blockieren mit `TRACEABILITY_REQUIRE_COMPLETE=1`, spätestens zum Gate
  der jeweiligen Phase.

**Status:** PASS.

## REP-01

`tests/rep01-structure.test.sh` prüft:

- `backend/internal/{notify,jobs,locations,pii,reviews,rules}`
- `api/rules.schema.json`
- `docs/acceptance`
- `docs/decisions` mit Index
- `docs/schema-baseline.md`

**Status:** PASS.

## Anhang: Status je [B]/[Q]-ID

Erzeugt aus `docs/traceability.csv`. PASS heißt: Der Nachweis liegt in den genannten Tests bzw. Dokumenten, und die CI ist auf dem Release-Kandidaten grün. Die Abweichungen sind oben begründet.

| ID | Tag | Status | Nachweis | WP |
|---|---|---|---|---|
| ABN-01 | [Q] | PASS (d, e) | `frontend/e2e/onboarding.spec.ts`, `frontend/e2e/conflict.spec.ts` | WP-200, WP-212 |
| ABN-02 | [Q] | PASS | `backend/internal/ci/querybudget_integration_test.go`, `tests/load/` | WP-204, WP-206 |
| ABN-03 | [Q], jeweilige Phase | PASS | `backend/internal/ci/concurrency_integration_test.go` | WP-207 |
| API-01 | [B] | PASS | `backend/internal/middleware/openapi_test.go` | WP-167 |
| API-02 | [B] | PASS | `backend/internal/api/cursor_test.go`, `backend/internal/api/listquery_test.go` | WP-164, WP-165 |
| API-03 | [B] | PASS | `backend/internal/ci/concurrency_integration_test.go`, `backend/internal/api/problem_test.go` | WP-061, WP-163 |
| API-04 | [B] | PASS | `backend/internal/middleware/middleware_test.go`, `backend/internal/entitlement/enforce_integration_test.go`, `backend/internal/middleware/ratelimit_test.go` | WP-050, WP-072, WP-166 |
| API-05 | [B] | PASS | `backend/internal/identity/apikey_integration_test.go`, `backend/internal/user/service_account_integration_test.go`, `backend/internal/operator/operator_test.go` (+11) | WP-067, WP-069, WP-070, WP-074 … |
| API-07 | [B] | PASS | `backend/internal/override/decide_test.go`, `backend/internal/ci/concurrency_integration_test.go` | WP-058, WP-061 |
| API-08 | [B] | PASS | `backend/internal/ci/bulk_test.go` | WP-181 |
| API-09 | [B] | PASS | `backend/internal/jobs/handler_test.go` | WP-182 |
| AUD-01 | [B] | PASS | `backend/internal/contact/pii_integration_test.go` | WP-242 |
| AUD-02 | [B] | PASS | `backend/internal/contact/handler_test.go` | WP-213 |
| AUD-03 | [B] | PASS | `backend/internal/discovery/last_seen_test.go`, `backend/internal/history/history_integration_test.go` | WP-090, WP-171 |
| AUD-04 | [B] | PASS | `backend/internal/audit/audit_test.go` | WP-132 |
| AUD-05 | [B] | PASS | `backend/internal/server/audit_coverage_test.go` | WP-133 |
| AUD-07 | [B] | PASS | `backend/internal/citype/export_audit_integration_test.go` | WP-134 |
| AUD-09 | [B] | PASS | `backend/internal/server/audit_coverage_test.go` | WP-133 |
| AUT-01 | [B] | PASS | `backend/internal/identity/deactivation_integration_test.go`, `backend/internal/identity/oidc_test.go` | WP-042, WP-043 |
| AUT-02 | [B] | PASS | `backend/internal/identity/deactivation_integration_test.go`, `backend/internal/identity/session_test.go` | WP-042, WP-066 |
| AUT-03 | [B] | PASS | `backend/internal/config/config_test.go` | WP-110 |
| AUT-04 | [B] | PASS | `backend/internal/identity/apikey_scope_integration_test.go`, `backend/internal/identity/apikey_integration_test.go` | WP-012, WP-067 |
| AUT-05 | [B] | N/P | `docs/acceptance/g1-gate.md` | WP-213 |
| AUT-06 | [B] | N/P | `docs/acceptance/g1-gate.md` | WP-213 |
| AUT-09 | [B] | PASS | `backend/internal/identity/oidc_test.go`, `tests/install-cloud-helpers.test.sh` | WP-043, WP-044 |
| AUT-10 | [B] | PASS | `backend/internal/middleware/middleware_test.go` | WP-050 |
| BLK-01 | [B] | PASS | `backend/internal/ci/bulk_test.go` | WP-181 |
| CI-01 | [B], Teilfelder [P2]/[P3] | PASS | `backend/internal/ci/handler_test.go` | WP-120 |
| CI-02 | [B] | PASS | `backend/internal/ci/indexes_integration_test.go` | WP-121 |
| CI-03 | [B] | PASS | `backend/internal/fieldmeta/fieldmeta_test.go` | WP-117 |
| CI-04 | [B] | PASS | `backend/internal/ci/mergepatch_test.go` | WP-057 |
| CI-05 | [B] | PASS | `backend/internal/topology/scope_integration_test.go`, `backend/internal/ci/handler_test.go`, `backend/internal/ci/delete_integration_test.go` | WP-031, WP-169, WP-170 |
| CI-06 | [B] | PASS | `backend/internal/history/history_integration_test.go` | WP-171 |
| CI-09 | [B] | PASS | `backend/internal/ci/assetfree_integration_test.go` | WP-178 |
| CI-10 | [B] | PASS | `backend/internal/server/authz_test.go`, `backend/internal/override/decide_test.go`, `backend/internal/ci/effective_view_integration_test.go` (+1) | WP-045, WP-058, WP-060, WP-091 |
| CI-11 | [B] | PASS | `backend/internal/ci/typechange_test.go` | WP-172 |
| CI-12 | [B] | PASS | `backend/internal/ci/merge_integration_test.go`, `backend/internal/ci/unmerge_integration_test.go` | WP-173, WP-174 |
| CI-13 | [B] | PASS | `backend/internal/discovery/resurrect_test.go` | WP-087 |
| COL-01 | [B] | PASS | `backend/internal/discovery/collector_status_integration_test.go` | WP-143 |
| COL-02 | [B] | PASS | `collector/plugins/redfish/redfish_test.go`, `backend/internal/credential/handler_test.go`, `backend/internal/credential/bundle_test.go` | WP-048, WP-047, WP-147 |
| COL-04 | [B] | PASS | `backend/internal/discovery/enroll_handler_test.go`, `backend/internal/discovery/ca_test.go` | WP-144, WP-145 |
| COL-05 | [B] | PASS | `edgecore/buffer/buffer_test.go`, `backend/internal/discovery/collector_status_integration_test.go` | WP-062, WP-143 |
| COL-06 | [B], Air-Gapped [O] | PASS | `edgecore/update/update_test.go` | WP-102, WP-148 |
| COL-08 | [B] | PASS | `tests/nats-mtls.test.sh` | WP-146 |
| DIS-01 | [B] | PASS | `collector/plugins/sweep/sweep_test.go` | WP-149 |
| DIS-02 | [B], Profilliste (V) | PASS | `collector/profiles/profiles_test.go` | WP-151 |
| DIS-03 | [B] | PASS | `collector/collectorcmd/collectorcmd_test.go` | WP-150 |
| DIS-04 | [B] | PASS | `collector/collectorcmd/collectorcmd_test.go`, `collector/plugins/snmp/snmp_test.go` | WP-150, WP-152 |
| DIS-05 | [B] | PASS | `backend/internal/discovery/devicerecord_test.go` | WP-083 |
| DIS-08 | [Q] | PASS | `backend/internal/discovery/errors_test.go` | WP-155 |
| DIS-09 | [B] | PASS | `backend/internal/discovery/scope_integration_test.go` | WP-153 |
| DIS-10 | [B] | PASS | `collector/collectorcmd/schedule_test.go` | WP-154 |
| DOD-01 | [Q] | PASS | `backend/internal/traceability/traceability_test.go`, `tests/rep01-structure.test.sh` | WP-005, WP-213 |
| ENT-01 | [B] | PASS | `backend/internal/entitlement/entitlement_test.go` | WP-071 |
| ENT-02 | [B] | PASS | `backend/internal/entitlement/entitlement_test.go` | WP-071 |
| ENT-03 | [B] | PASS | `backend/internal/entitlement/enforce_integration_test.go`, `backend/internal/entitlement/unlicensed_integration_test.go` | WP-072, WP-073 |
| ENT-04 | [B] | PASS | `backend/internal/operator/entitlements_test.go` | WP-074 |
| ENT-05 | [B] | PASS | `backend/internal/entitlement/enforce_integration_test.go` | WP-072 |
| ENT-06 | (V), [B–P4], [A] | PASS | `backend/internal/entitlement/addon_test.go` | WP-075 |
| ENT-07 | [B] | PASS | `backend/internal/entitlement/enforce_integration_test.go` | WP-072 |
| ENT-08 | [B] | PASS | `backend/internal/entitlement/unlicensed_integration_test.go` | WP-073 |
| EVT-01 | [B] | PASS | `backend/internal/events/outbox_integration_test.go` | WP-136 |
| EVT-02 | [B] | PASS | `backend/internal/events/outbox_integration_test.go` | WP-136 |
| EXP-01 | [B], DATEV [P2] | PASS | `backend/internal/export/scope_integration_test.go`, `backend/internal/export/export_test.go` | WP-035, WP-189 |
| GATE-01 | [Q] | PASS | `tests/rep01-structure.test.sh` | WP-213 |
| GATE-02 | [Q] | FAIL | `tests/rep01-structure.test.sh` | WP-213 |
| GATE-03 | [Q] | offen | `tests/install-smoke-health.test.sh`, `docs/security/review-g1.md`, `docs/security/pentest-g1.md` (+1) | WP-092, WP-209, WP-210, WP-211 |
| GATE-04 | [Q], Bezug GA | N/P | `docs/acceptance/g1-gate.md` | WP-213 |
| GATE-05 | [Q] | PASS | `tests/rep01-structure.test.sh` | WP-213 |
| GLO-10 | [B] | PASS | `frontend/src/i18n/catalog.test.ts` | WP-194 |
| GLO-11 | [Q] | PASS | `backend/internal/traceability/traceability_test.go`, `tests/rep01-structure.test.sh` | WP-005, WP-213 |
| GLO-12 | [B] | PASS | `backend/internal/discovery/devicerecord_test.go`, `backend/internal/override/decide_test.go` | WP-083, WP-084 |
| GLO-13 | [B] | PASS | `backend/internal/ci/validation_test.go` | WP-091 |
| GQL-01 | [B] | PASS | `backend/internal/graphqlbff/graphqlbff_test.go` | WP-184 |
| GQL-02 | [B] | PASS | `backend/internal/graphqlbff/resolver_test.go` | WP-185 |
| GQL-04 | [B] | PASS | `backend/internal/graphqlbff/graphqlbff_test.go` | WP-036 |
| IMP-01 | [B] | PASS | `backend/internal/topology/impact_test.go` | WP-176 |
| IMP-02 | [B] | PASS | `backend/internal/topology/impact_test.go` | WP-177 |
| IMP-03 | [B] | PASS | `backend/internal/topology/impact_test.go` | WP-176 |
| IMP-04 | [B] | PASS | `backend/internal/topology/redundancy_test.go` | WP-179 |
| IMP-05 | [B] | PASS | `backend/internal/topology/multi_integration_test.go` | WP-180 |
| IMP-06 | [B] | PASS | `backend/internal/topology/multi_integration_test.go` | WP-180 |
| IMP-07 | [B] | PASS | `backend/internal/tenant/rls/ci_children_integration_test.go`, `backend/internal/topology/scope_integration_test.go` | WP-025, WP-031 |
| IMP-09 | [B] | PASS | `backend/internal/discovery/power_test.go` | WP-161 |
| INS-01 | [Q] | PASS | `tests/install-smoke-health.test.sh`, `docs/acceptance/g1-installation.md` | WP-092, WP-211 |
| INS-02 | [B] | PASS | `tests/install-cloud-helpers.test.sh` | WP-095 |
| INS-03 | [B] | PASS | `tests/install-cloud-helpers.test.sh` | WP-094 |
| INS-04 | [Q] | PASS | `frontend/e2e/onboarding.spec.ts` | WP-200 |
| INS-05 | [B] | PASS | `tests/install-cloud-helpers.test.sh` | WP-096 |
| INS-06 | [B] | PASS | `collector/collectorcmd/collectorcmd_test.go` | WP-156 |
| INS-07 | [B] | PASS | `tests/install-preflight.test.sh` | WP-093 |
| JOB-01 | [B] | PASS | `backend/internal/jobs/service_test.go` | WP-129 |
| JOB-02 | [B], Zusätze [P2], [P3] | PASS | `backend/internal/jobs/scheduler_test.go` | WP-131 |
| JOB-03 | [B] | PASS | `backend/internal/jobs/handler_test.go` | WP-182 |
| JOB-04 | [B] | PASS | `backend/internal/reviews/merge_job_integration_test.go` | WP-183 |
| LCY-01 | [B] | PASS | `backend/internal/lifecycle/status_separation_integration_test.go`, `backend/internal/ci/delete_integration_test.go` | WP-122, WP-170 |
| LCY-05 | [B] | PASS | `backend/internal/lifecycle/offline_integration_test.go` | WP-157 |
| LOC-01 | B/G1 | PASS | `backend/internal/tenantapi/handler_test.go` | WP-107 |
| LOC-02 | B/G1 | PASS | `backend/internal/tenantapi/handler_test.go` | WP-107 |
| LOC-03 | B/G1 | PASS | `backend/internal/tenantapi/handler_test.go` | WP-107 |
| LOC-04 | B/G1 | PASS | `backend/internal/tenantapi/location_uniques_integration_test.go` | WP-108 |
| LOC-05 | B/G1 | PASS | `backend/internal/tenantapi/location_uniques_integration_test.go` | WP-108 |
| LOC-06 | B/G1 | PASS | `backend/internal/tenantapi/location_uniques_integration_test.go` | WP-108 |
| LOC-07 | B/G1 | PASS | `backend/internal/locations/tree_test.go` | WP-053 |
| LOC-10 | B/G1 | PASS | `backend/internal/locations/pg_repository_integration_test.go`, `backend/internal/locations/tree_test.go`, `backend/internal/ci/location_integration_test.go` | WP-026, WP-053, WP-054 |
| LOC-11 | B/G1 | PASS | `backend/internal/locations/move_integration_test.go` | WP-055 |
| MET-01 | B/G1, Teil P2/G2 | PASS | `backend/internal/citype/seed_integration_test.go` | WP-115 |
| MET-02 | B/G1 | PASS | `backend/internal/citype/seed_integration_test.go` | WP-115 |
| MET-03 | B/G1 | PASS | `backend/internal/citype/seed_integration_test.go` | WP-115 |
| MET-04 | B/G1 | PASS | `backend/internal/citype/compose_test.go` | WP-119 |
| MET-10 | B/G1, Teil P2/G2 | PASS | `backend/internal/citype/attrdef_integration_test.go` | WP-116 |
| MET-11 | B/G1, Teil P2/G2 | PASS | `backend/internal/fieldmeta/fieldmeta_test.go` | WP-117 |
| MET-15 | B/G1 | PASS | `backend/internal/citype/ddl_job_integration_test.go` | WP-142 |
| MET-16 | B/G1, Teil P2/G2 | PASS | `backend/internal/citype/compose_test.go` | WP-119 |
| MET-54 | B/G1, Teil P2/G2 | PASS | `backend/internal/citype/export_audit_integration_test.go` | WP-134 |
| MON-01 | [B] | PASS | `backend/internal/monitoring/timescale_rls_spike_integration_test.go`, `backend/internal/monitoring/pg_store_integration_test.go` | WP-039, WP-040 |
| MON-02 | [B] | PASS | `backend/internal/monitoring/monitoring_test.go` | WP-190 |
| NET-01 | [B] | PASS | `backend/internal/ipam/vlan_integration_test.go`, `backend/internal/discovery/last_seen_test.go`, `backend/internal/ipam/interface_integration_test.go` | WP-081, WP-090, WP-123 |
| NET-02 | [B] | PASS | `backend/internal/ipam/vlan_integration_test.go`, `backend/internal/ipam/vrf_integration_test.go` | WP-081, WP-082 |
| NET-03 | [B] | PASS | `backend/internal/ipam/vrf_integration_test.go`, `backend/internal/ipam/ip_integration_test.go` | WP-082, WP-124 |
| NET-04 | [B] | N/P | `docs/acceptance/g1-gate.md` | WP-213 |
| NET-05 | [B] | PASS | `backend/internal/ipam/handler_test.go` | WP-125 |
| NET-08 | [B] | PASS | `backend/internal/ipam/vlan_integration_test.go` | WP-081 |
| NFR-01 | [B], Teilumfang [P5] | PASS | `backend/internal/discovery/identity_test.go`, `backend/internal/discovery/scale_integration_test.go`, `tests/load/` | WP-086, WP-205, WP-206 |
| NFR-02 | [B] | FAIL | `backend/internal/topology/impact_test.go`, `tests/load/` | WP-177, WP-206 |
| NFR-03 | [B], Teilumfang [P5] | FAIL | `backend/internal/discovery/consumer_test.go`, `collector/collectorcmd/schedule_test.go`, `tests/load/` | WP-085, WP-154, WP-206 |
| NFR-04 | [B], Teilumfang [P5] | PASS | `edgecore/buffer/buffer_test.go`, `backend/internal/maintenance/status_test.go` | WP-062, WP-063 |
| NFR-05 | [B] | PASS | `tests/backup-config.test.sh`, `tests/restore-pitr.sh` | WP-100, WP-101 |
| NFR-06 | [B] | PASS | `backend/internal/jobs/retention_integration_test.go` | WP-135 |
| NFR-07 | [B] | PASS | `frontend/e2e/a11y.spec.ts` | WP-193 |
| NFR-08 | [B] | N/P | `docs/acceptance/g1-gate.md` | WP-213 |
| NFR-09 | [B] | PASS | `docs/betrieb/referenzmodell.md` | WP-105 |
| NFR-10 | [Q] | FAIL | `backend/cmd/seed/seed_integration_test.go`, `backend/internal/ci/querybudget_integration_test.go`, `tests/load/` | WP-203, WP-204, WP-206 |
| NTF-01 | [B] | PASS | `backend/internal/notify/channel_test.go` | WP-137 |
| NTF-03 | [B] | PASS | `backend/internal/notify/recipients_integration_test.go` | WP-138 |
| NTF-04 | [B] | PASS | `backend/internal/notify/notifier_test.go` | WP-139 |
| NTF-05 | [B], Zusätze [P2], [P3] | PASS | `backend/internal/maintenance/status_test.go`, `backend/internal/notify/events_integration_test.go` | WP-063, WP-140 |
| OPS-01 | [B] | PASS | `backend/internal/server/health_test.go` | WP-097 |
| OPS-02 | [B], [P3], [A] | PASS | `backend/internal/config/config_test.go` | WP-098 |
| OPS-03 | [B], [P5] | PASS | `.github/workflows/ci.yml`, `backend/internal/jobs/scheduler_test.go` | WP-099, WP-131 |
| OPS-04 | [B], [P5] | PASS | `tests/backup-config.test.sh`, `tests/restore-pitr.sh` | WP-100, WP-101 |
| OPS-05 | [B] | PASS | `backend/internal/tenant/rls/system_worker_integration_test.go`, `backend/internal/server/workers_test.go`, `backend/internal/notify/events_integration_test.go` | WP-022, WP-103, WP-140 |
| OPS-06 | [Q] | PASS | `edgecore/buffer/buffer_test.go`, `tests/outage/` | WP-062, WP-104 |
| OVR-01 | [B] | PASS | `backend/internal/ci/mergepatch_test.go`, `backend/internal/override/decide_test.go`, `backend/internal/ci/effective_view_integration_test.go` | WP-057, WP-058, WP-060 |
| OVR-02 | [B] | PASS | `backend/internal/workflow/executor_test.go` | WP-059 |
| PRI-11 | [B] | PASS | `docs/betrieb/referenzmodell.md` | WP-105 |
| RBA-01 | [B] | PASS | `backend/internal/server/authz_catalog_test.go` | WP-068 |
| RBA-02 | [B] | PASS | `backend/internal/permission/role_matrix_integration_test.go` | WP-046 |
| RBA-03 | [B] | PASS | `backend/internal/permission/scope_integration_test.go` | WP-009 |
| RBA-04 | [B] | PASS | `backend/internal/server/authz_test.go` | WP-045 |
| RBA-08 | [B] | PASS | `backend/internal/user/service_account_integration_test.go` | WP-069 |
| RCK-01 | [B] | PASS | `backend/internal/rack/rules_integration_test.go` | WP-126 |
| RCK-02 | [B] | PASS | `backend/internal/rack/handler_test.go` | WP-175 |
| RCK-03 | [B] | PASS | `backend/internal/rack/mount_location_integration_test.go` | WP-078 |
| REC-01 | [B] | PASS | `backend/internal/discovery/consumer_test.go` | WP-085 |
| REC-02 | [B] | PASS | `backend/internal/discovery/identity_test.go` | WP-086 |
| REC-03 | [B] | PASS | `backend/internal/override/decide_test.go` | WP-058 |
| REC-05 | [B] | PASS | `backend/internal/override/decide_test.go` | WP-084 |
| REC-06 | [B] | PASS | `backend/internal/reviews/conflict_test.go` | WP-089 |
| REC-07 | [B] | PASS | `backend/internal/discovery/last_seen_test.go` | WP-090 |
| REC-08 | [B] | PASS | `backend/internal/reviews/service_test.go` | WP-088 |
| REC-09 | [B] | PASS | `backend/internal/lifecycle/offline_integration_test.go` | WP-157 |
| REC-10 | [B] | PASS | `backend/internal/ci/provenance_integration_test.go` | WP-198 |
| REC-12 | [B] | PASS | `backend/internal/override/decide_test.go`, `backend/internal/reviews/conflict_test.go` | WP-058, WP-089 |
| REL-01 | [B] | PASS | `backend/internal/relationship/projection_integration_test.go`, `backend/internal/relationshiptype/seed_integration_test.go` | WP-079, WP-127 |
| REL-02 | [B] | PASS | `backend/internal/relationship/pg_repository_integration_test.go` | WP-128 |
| REL-03 | [B] | PASS | `backend/internal/relationship/suppression_integration_test.go` | WP-160 |
| REL-04 | [B] | PASS | `backend/internal/relationship/suppression_integration_test.go` | WP-160 |
| REL-05 | [B] | PASS | `backend/internal/relationship/suppression_integration_test.go` | WP-160 |
| REL-08 | [B] | PASS | `backend/internal/relationship/cleanup_integration_test.go` | WP-159 |
| REP-01 | [B] | PASS | `backend/internal/database/migrations_roundtrip_integration_test.go`, `backend/internal/rules/rules_test.go`, `backend/internal/jobs/service_test.go` (+2) | WP-006, WP-118, WP-129, WP-137 … |
| SEC-01 | [B] | PASS | `backend/internal/credential/handler_test.go` | WP-047 |
| SEC-05 | [B] | PASS | `backend/internal/pii/pii_integration_test.go`, `backend/internal/pii/surrogate_integration_test.go` | WP-240, WP-241 |
| SEC-06 | [B] | PASS | `backend/internal/identity/session_test.go`, `backend/internal/platform/crypto/crypto_test.go` | WP-066, WP-141 |
| SEC-07 | [B] | PASS | `backend/internal/operator/operator_test.go` | WP-070 |
| SEC-08 | [B] | PASS | `backend/internal/platform/egress/egress_test.go` | WP-049 |
| SEC-10 | [B] | PASS | `backend/internal/graphqlbff/graphqlbff_test.go` | WP-184 |
| SEC-11 | [Q] | PASS | `docs/security/abhaengigkeitsscans.md`, `docs/security/review-g1.md` | WP-004, WP-209 |
| SEQ-01 | [Q] | PASS | `tests/rep01-structure.test.sh` | WP-213 |
| SIM-01 | [B], Teilumfang [P2] | PASS | `.github/workflows/ci.yml`, `backend/cmd/seed/seed_integration_test.go` | WP-106, WP-203 |
| SIM-02 | [B] | PASS | `collector/plugins/simulate/simulate_test.go` | WP-162 |
| SRC-01 | [B] | PASS | `backend/internal/tenant/rls/ci_children_integration_test.go`, `backend/internal/search/scope_integration_test.go`, `backend/internal/search/search_test.go` | WP-025, WP-032, WP-186 |
| TEC-06 | [B], [Q] Spike | PASS | `backend/internal/monitoring/timescale_rls_spike_integration_test.go`, `backend/internal/locations/pg_repository_integration_test.go`, `backend/internal/monitoring/pg_store_integration_test.go` | WP-039, WP-026, WP-040 |
| TEC-09 | [B] | PASS | `tests/install-smoke-health.test.sh`, `tests/install-preflight.test.sh`, `tests/install-cloud-helpers.test.sh` | WP-092, WP-093, WP-094 |
| TEC-13 | [B] | PASS | `backend/internal/notify/channel_test.go` | WP-137 |
| TEC-14 | [B] | PASS | `backend/internal/jobs/queue_test.go` | WP-130 |
| TEC-15 | [B] | PASS | `edgecore/update/update_test.go` | WP-102 |
| TEN-01 | [B], Reseller [P4] | PASS | `backend/internal/tenantapi/handler_test.go`, `backend/internal/locations/hierarchy_integration_test.go` | WP-107, WP-109 |
| TEN-02 | [B] | PASS | `backend/internal/tenant/rls/global_rows_integration_test.go`, `backend/internal/tenant/rls/ci_children_integration_test.go`, `backend/internal/locations/pg_repository_integration_test.go` | WP-024, WP-025, WP-026 |
| TEN-03 | [B] | PASS | `backend/internal/tenant/rls/catalog_integration_test.go`, `backend/internal/database/role_check_integration_test.go` | WP-007, WP-064 |
| TEN-04 | [B] | PASS | `backend/internal/database/tenant_scope_test.go`, `backend/internal/permission/scope_integration_test.go`, `backend/internal/tenant/rls/site_policies_integration_test.go` | WP-008, WP-009, WP-027 |
| TEN-05 | [B] | PASS | `backend/internal/tenant/rls/catalog_integration_test.go`, `backend/internal/tenant/rls/client_policies_integration_test.go`, `backend/internal/tenant/rls/global_rows_integration_test.go` (+4) | WP-007, WP-023, WP-024, WP-022 … |
| TEN-06 | [B] | PASS | `backend/internal/database/tenant_scope_test.go`, `backend/internal/ci/ci_scope_integration_test.go`, `backend/internal/citype/citype_scope_integration_test.go` (+51) | WP-008, WP-010, WP-011, WP-012 … |
| TEN-09 | [Q] | PASS | `backend/internal/tenant/rls/catalog_integration_test.go`, `backend/internal/tenant/rls/matrix_integration_test.go`, `backend/internal/tenant/rls/channel_matrix_integration_test.go` | WP-007, WP-065, WP-208 |
| TEN-10 | [B] | PASS | `backend/internal/ipam/vrf_integration_test.go` | WP-082 |
| TLC-01 | [B] | PASS | `backend/internal/signup/service_test.go`, `backend/internal/signup/protect_test.go` | WP-111, WP-112 |
| TLC-02 | [B], Reseller [P4] | PASS | `backend/internal/operator/orgs_test.go` | WP-113 |
| TLC-03 | [B] | PASS | `backend/internal/user/invitation_integration_test.go` | WP-114 |
| TLC-04 | [B] | PASS | `backend/internal/identity/deactivation_integration_test.go` | WP-042 |
| TOP-01 | [B] | PASS | `backend/internal/discovery/topology_test.go` | WP-158 |
| TOP-02 | [B] | PASS | `backend/internal/relationship/cleanup_integration_test.go` | WP-159 |
| TST-01 | [B], Teilumfang [P2] | PASS | `backend/internal/discovery/tst01_integration_test.go`, `backend/internal/reviews/tst01_integration_test.go`, `backend/internal/events/tst01_integration_test.go` (+6) | WP-202 |
| TST-02 | [B] | PASS | `.github/workflows/ci.yml`, `backend/internal/tenant/rls/matrix_integration_test.go`, `backend/internal/tenant/rls/channel_matrix_integration_test.go` | WP-003, WP-065, WP-208 |
| TST-03 | [B], Teilumfang [P2] | PASS | `frontend/e2e/onboarding.spec.ts`, `frontend/e2e/ci.spec.ts` | WP-200, WP-201 |
| TST-04 | [Q] | PASS | `.github/workflows/ci.yml`, `docs/security/abhaengigkeitsscans.md` | WP-001, WP-002, WP-003, WP-004 |
| UI-01 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-02 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-03 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-04 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-05 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-06 | [B] | PASS | `frontend/src/pages/TopologyPage.test.tsx` | WP-195 |
| UI-07 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-08 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-09 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-10 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-16 | [B] | PASS | `frontend/src/pages/ReviewInboxPage.test.tsx`, `frontend/src/components/NotificationCenter.test.tsx` | WP-196, WP-197 |
| UI-17 | [B] | PASS | `frontend/src/pages/OnboardingPage.test.tsx` | WP-199 |
| UI-18 | [B] | PASS | `frontend/e2e/a11y.spec.ts`, `frontend/src/i18n/catalog.test.ts` | WP-193, WP-194 |
| VIE-01 | [B] | PASS | `backend/internal/savedview/savedview_integration_test.go` | WP-188 |
| WHK-01 | [B] | PASS | `backend/internal/webhook/whk01_test.go`, `backend/internal/webhook/whk01_integration_test.go` | WP-213 |
