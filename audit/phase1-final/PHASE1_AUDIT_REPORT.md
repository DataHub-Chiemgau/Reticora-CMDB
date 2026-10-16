# Phase-1-Abnahmeaudit (WP-001 – WP-213) – Bericht

> **Prüfstand: IN ARBEIT** (fortlaufend aktualisiert). Dieser Abschnitt hält den Stand fest, damit der
> Prüfstand nach Kontextverlust rekonstruierbar bleibt. Endgültige Bewertung folgt in den Abschnitten unten.

## 0. Prüfumgebung und Baseline (ausgeführt am 2026-10-15, Commit `3a9669a`)

| Prüfung | Kommando / Umgebung | Ergebnis | Evidenz |
|---|---|---|---|
| Backend, alle PostgreSQL-Tests (wie CI-Job `migrations`) | `gotestsum -- -count=1 -p 1 ./...` gegen TimescaleDB 2.17.2-pg16 + NATS JetStream + Redis 7 | 1896 Tests (inkl. Subtests) PASS, 0 FAIL, 0 SKIP | `evidence/results-backend.tsv` |
| Collector / Edgecore | `go test -race ./...` | 273 / 76 PASS | `evidence/results-collector.tsv`, `evidence/results-edgecore.tsv` |
| golangci-lint v2.14.0 (gepinnte Version) | backend, collector, edgecore mit `backend/.golangci.yml` | 0 Befunde | `evidence/lint-*.log` |
| Frontend-Kette | `format:check`, `lint`, `generate:api:check`, `typecheck`, `test`, `build` | alle Exit 0; Vitest 47 Dateien / 249 Tests PASS | `evidence/results-frontend-vitest.tsv` |
| Playwright (Mock-Backend, wie CI: `dist` auf :4173) | Projekte `chromium`, `mobile-chrome` | 50/50 PASS | `evidence/e2e-mocked.log` |
| Playwright Real-Stack Onboarding (`onboarding.spec.ts`) | echter Server (production), Keycloak 24, PG/NATS/NATS-Edge (mTLS)/Redis/MinIO/Mailpit, Collector-Binary | 6/6 PASS | `evidence/e2e-onboarding-real.log` |
| Migrationen | `migrate up` → `down -all` → `up` (138 Migrationen) | OK | `evidence/migrate.log` |
| RLS-Zusicherungen aus CI | keine Legacy-GUC-Policies, RLS überall FORCE | 0 / 0 Abweichungen | Kommandoausgabe (Bericht §5) |
| `make seed` (SIM-01) | gegen migrierte DB | OK | `evidence/seed.log` |
| Shell-Tests | install-cloud-helpers 36/36, install-smoke-health 12/12, install-preflight 29/29, backup-config 26/26, nats-mtls 3/3, simulate-ingest 4/4, rep01-structure 47/47 | alle PASS | `evidence/sh-*.log`, `evidence/rep01.log` |
| Installer `./install.sh --cloud --non-interactive` | Docker-Build der Images | **NICHT TESTBAR**: Image-Build scheitert an der Netzwerkrichtlinie der Prüfumgebung (`dl-cdn.alpinelinux.org`, `quay.io` → 403) | `evidence/install-cloud-blocked.log` |
| Firefox / WebKit / Edge (Playwright) | Browser nicht installierbar (Netzwerkrichtlinie) | **NICHT TESTBAR** lokal | – |

Ersatz für den blockierten Installer: Der reale Stack wurde aus denselben Artefakten manuell aufgebaut
(Server-Binary aus `backend/cmd/server` im Modus `production`, Produktions-Build des Frontends über `vite preview`
mit `/api`-Proxy, Keycloak 24.0 mit `deploy/keycloak/realm-reticora-dev.json`, interner NATS mit
`deploy/docker-compose/nats.conf`, NATS-Edge mit `deploy/docker-compose/nats-edge.conf` und vom Server erzeugter PKI).

## Prüfstand (Zwischenstand 2026-10-16)

- **Erledigt:** Einzelprüfung aller 213 WPs in 27 Batches, jeweils Auditor + unabhängiger adversarialer Verifizierer
  (213/213 verifiziert). Zwischenergebnis: BESTANDEN 72, TEILWEISE BESTANDEN 100, FEHLGESCHLAGEN 39, NICHT TESTBAR 2
  (`PHASE1_WP_AUDIT_MATRIX.md`, Rohdaten `data/verified-verdicts.json`).
- **Erledigt:** Real-Stack-E2E-Suite `frontend/e2e-real/` (6 Prozessketten, 79 Tests: 56 bestanden, 23 fehlgeschlagen;
  25 Produktbefunde, davon 23 adversarial bestätigt, 2 „Spezifikation unklar“).
- **Erledigt:** Lastlauf (k6 0.54.0, Seed 10.000): Lauf 1 unter Fremdlast – `list`/`search` über Budget; Lauf 2 auf
  ruhiger Umgebung – alle 10 API-Budgets eingehalten; Ingest und Spool in beiden Läufen eingehalten (`evidence/load/`).
- **In Arbeit:** Defekt- und Regressionstests je WP-Gruppe (T1–T7) mit unabhängigem Review; danach Abschlusssynthese
  (Statusabgleich mit E2E- und Testevidenz, eindeutige Defekt-IDs) und Quality-Gate-Entscheidung.
- Prüfumgebung: Nach einem Neustart mit weniger freiem Speicher wurde JetStream lokal mit `max_file_store: 60G`
  (Scratch-Kopie von `deploy/docker-compose/nats.conf`) betrieben; keine Repository-Änderung.
