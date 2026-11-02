# G1-Nachweis Installation

WP-211 (GATE-03, INS-01). GATE-03 verlangt für G1 einen Install-Smoke-Test
auf beiden Plattformen. Nach E-27 sind das die Referenzplattformen aus CH22:
Ubuntu 22.04/24.04 und Debian 12. Debian 12 läuft im Workflow als Container
auf einem Ubuntu-24.04-Runner.

## Release-Kandidat

Release-Kandidat für die erneute Entscheidung über G1 ist der Stand von
`main` nach dem Merge aller Korrektur-WPs mit dem Tag `v1.0.0-rc.<n>`
(WP-595, `t5-rc-evidence`). `install-smoke.yml` läuft auf jedem Push nach
`main` ohne Pfadfilter und auf jedem Tag `v*`. `tests/rep01-structure.test.sh
--final` prüft (Fall `install_evidence_current`, F-122), dass diese Tabelle
den letzten Commit nennt, der das Produkt geändert hat (Code, Deployment,
Installer; Test- und Audit-Pfade ausgenommen), oder einen späteren.

| Punkt | Wert |
|---|---|
| Tag | `v1.0.0-rc.<n>`: RC-Lauf ausstehend (WP-595) |
| Commit | RC-Lauf ausstehend (WP-595) |
| Workflow | `.github/workflows/install-smoke.yml` („Install Smoke Test“), Push des Tags |
| Lauf | RC-Lauf ausstehend (WP-595) |
| Datum | RC-Lauf ausstehend (WP-595) |
| Ergebnis | RC-Lauf ausstehend (WP-595) |

Jobs des Laufs auf dem Release-Kandidaten:

| Job | Prüfung | Ergebnis |
|---|---|---|
| `installer-script-tests` | Installer-Hilfsfunktionen, Preflight, Backup-Konfiguration, NATS-mTLS, Signaturprüfung | RC-Lauf ausstehend (WP-595) |
| `install-cloud-smoke (ubuntu-22.04)` | Installation, Dienste healthy, Backup-Kette, `/health`, SPA, Keycloak | RC-Lauf ausstehend (WP-595) |
| `install-cloud-smoke (ubuntu-24.04)` | wie oben, dazu Onboarding-E2E | RC-Lauf ausstehend (WP-595) |
| `install-cloud-smoke (debian-12)` | wie oben, Container `debian:12` | RC-Lauf ausstehend (WP-595) |
| `e2e-real` (chromium, edge, firefox, webkit, mobile-chrome, mobile-safari) | Real-Stack-Suite und Modul-Lauf `maintenance.spec.ts` je Browserprojekt gegen die Erwartungsliste | RC-Lauf ausstehend (WP-595) |
| `e2e-real-ratchet` | Ratchet über alle sechs Projekte; zulässig sind nur Zeilen der nach E-60 zurückgestellten Befunde | RC-Lauf ausstehend (WP-595) |
| `tls-domain-pebble` | Installation mit Domäne und ACME (Pebble) | RC-Lauf ausstehend (WP-595) |
| `reinstall` | erneute Installation über eine bestehende | RC-Lauf ausstehend (WP-595) |
| `release-mode` | Standardpfad mit den signierten Release-Artefakten des Tags (WP-388, E-69) | RC-Lauf ausstehend (WP-595) |

Release-Hinweis (F-212, nach E-60 zurückgestellt): Die Datenkorrektur der
Migration 000086 benennt die Add-on-Zeilen nur um. Installationen eines
Zwischenbuilds vor 000086 können deshalb implizit aus dem Tarif
provisionierte Zeilen der Add-ons `iga` und `ai` behalten. Im G1-Release
sind diese Module nach E-03 abgeschaltet (WP-214); die Zeilen sind dort
wirkungslos. Frische Installationen des Release-Kandidaten sind nicht
betroffen. Die Bereinigung folgt mit der Aktivierung der Add-on-Module IGA
und KI (Phase 2, `docs/acceptance/g1-gate.md`, Abschnitt „Ergebnis“, E-60).

### Erstnachweis (WP-211)

| Punkt | Wert |
|---|---|
| Commit | `1b6671fa0cc4ae7d945e0e32704915d4fc3722b1` (`main`, nach PR #61 „Sicherheitsreview: WP-208 – WP-210“) |
| Workflow | `.github/workflows/install-smoke.yml` („Install Smoke Test“), manuell ausgelöst |
| Lauf | [#122, Run 38512314064](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/38512314064) |
| Datum | 2026-10-14, 04:37–04:43 UTC |
| Ergebnis | **success**, alle vier Jobs grün |

Der Gate-PR (WP-211 bis WP-213) ändert Backend und Frontend (WHK-01,
AUD-02). Der Workflow lief deshalb ein zweites Mal auf dem Stand des PRs:
[#123, Run 38513566388](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/38513566388),
Commit `37752e1`, 2026-10-14 04:51–04:58 UTC. Auch dieser Lauf ist
**success**, mit denselben vier Jobs und der Onboarding-E2E. Danach ändert sich
nur Dokumentation. Ändern sich Installer, Compose-Stack oder Images, wird der
Workflow erneut ausgelöst und diese Tabelle aktualisiert.

## Ergebnisse je Plattform

Erstnachweis (WP-211, Run 38512314064); die Jobs des Laufs auf dem
Release-Kandidaten stehen im Abschnitt „Release-Kandidat“.

| Job | Plattform | Installation (nicht interaktiv) | Dienste healthy, Backup-Kette, `/health`, SPA, Keycloak | Onboarding-E2E |
|---|---|---|---|---|
| `install-cloud-smoke (ubuntu-22.04)` | Ubuntu 22.04, nativ | success (4 min 10 s) | success | – |
| `install-cloud-smoke (ubuntu-24.04)` | Ubuntu 24.04, nativ | success (3 min 44 s) | success | **success** (57 s) |
| `install-cloud-smoke (debian-12)` | Debian 12, Container `debian:12` auf ubuntu-24.04 | success (5 min 4 s) | success | – |
| `installer-script-tests` | ubuntu-latest | Installer-Hilfsfunktionen, Preflight (Plattform, Docker ≥ 24, Compose v2, Sizing), Backup-Konfiguration, NATS-mTLS (COL-08) | – | – |

Die Onboarding-E2E (`frontend/e2e/onboarding.spec.ts`, WP-200) läuft ohne
Mocks gegen die frische Installation. Eine native Plattform genügt dafür, denn
die Installation selbst prüft jede Plattform. Die Suite umfasst:

- Self-Signup mit Bestätigungs-Mail und Trial
- Login und Dashboard
- Collector-Enrollment und Scope-Bestätigung
- 30 simulierte Geräte
- ein aufgelöstes Review
- eine Impact-Abfrage
- einen Export
- die Fehlerbilder aus INS-04

Das ist zugleich der Nachweis für ABN-01 (d), siehe `docs/acceptance/abn-01.md`.

## Prüfschritte des Smoke-Tests

1. `install-cloud.sh` nicht interaktiv mit Testdomäne. Die Preflight-Prüfung
   der Plattform ist Teil des Installers (WP-093). Der geprüfte, noch nicht
   veröffentlichte Stand wird mit `--build-from-source` gebaut; den
   Standardpfad mit den signierten Release-Artefakten prüft auf Tags `v*` der
   Job `release-mode` (WP-388, E-69).
2. Alle erwarteten Compose-Dienste laufen und sind healthy
   (`tests/install-smoke-health.sh`).
3. Die Backup-Kette ist eingerichtet: pgBackRest-Stanza und erstes Backup.
4. `/health` des Backends, Auslieferung der SPA, Keycloak-Realm erreichbar.
5. Auf Ubuntu 24.04 zusätzlich: Onboarding-Vorbereitung
   (`frontend/e2e/fixtures/setup-onboarding.sh`: Mail-Catcher,
   Signup-Konfiguration, Collector-Build), dann die Onboarding-Suite.
6. Aufräumen. Bei einem Fehler legt der Workflow die Logs der Dienste und den
   Playwright-Bericht als Artefakt ab.

## Status

- **INS-01:** PASS. Die Installation läuft auf allen Referenzplattformen.
- **GATE-03, Teil Install-Smoke:** PASS.
