## Zusammenfassung

Prüfdatum 2026-10-01; Produktstand `abda3bff7d03b91826df635c2b80fc2c31e95005`. Teil 08, nicht Teil 09; Vorbereitung README, Bestandsaufnahme und Ist-Schema vorhanden.
Zwischenstand: 3/50 IDs bewertet; Status-/Tagzähler und priorisierte Befunde folgen nach Abschluss.
Keine Produkt-, Migrations- oder Teständerungen. Tests ausschließlich mit gesperrtem Download; fehlende Werkzeuge werden nicht installiert.
Ungeprüft: GLO-10, MON-01–04, UI-01–18, NFR-01–10, TST-01–04, DOD-01, SEQ-01, GATE-01–05, ABN-01–03.

## Ergebnis je Anforderung als Tabelle

Soll: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md`; GLO-10/11 zusätzlich aus `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md:99–101`. Fehlender v2-Wortlaut wird nicht durch vermutete Einzelverträge ersetzt; spätere Teilpflichten bleiben bei ihrer Phase.

| ID | Tag | Status | Evidenz | Tests | Befund (1–2 Sätze) | Severity | Aufwand |
|---|---|---|---|---|---|---|---|
| GLO-11 | [Q] | FAIL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/README.md:352`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/.github/workflows/ci.yml:12–215`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/Makefile:10–119`; Pfadkontrolle docs/traceability.csv: fehlt | Kein Traceability-Check in den Workflowdefinitionen; fehlende Datei kann nicht auf vier Spalten geprüft werden. | README behauptet ID→Epic/Test-Rückverfolgbarkeit, aber Datei und CI-Prüfung fehlen. 0/261 bekannte verpflichtend getaggte IDs haben einen Eintrag; unbekannte IDs aus Teil 03 sind nicht im Nenner. | High | L |
| SIM-01 | [B]; Teilumfang [P2] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000054_default_organization.up.sql:21–23`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/deploy/keycloak/realm-reticora.json:122–127`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/audit-seed/main.go:21–22,37–61`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/Makefile:112–114` | Einzelne Testfixtures, kein Test des verlangten Demo-/Skalierungsseeds; seed.sql fehlt. | Default-Org, anderer Demo-Login und Audit-Restore-Seed vorhanden, nicht der geforderte Zwei-Org-/Client-/40-CI-/2.000-Metriken-Datensatz mit Beziehungen, Scopes, Reviews, Webhook und VRF. make seed maskiert die fehlende Datei; 10.000-Objekt-Skalierungsseed fehlt. | High | L |
| SIM-02 | [B] | FAIL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/config/config.go:13–83 Config,85 Load`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:49`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:96 Main,778 loadCollectorConfig`; Suche RETICORA_SIMULATE/Simulation in Backend/Collector/Deploy ohne Produktionsfund | Kein Simulator-/30-Geräte-Ingest-Test gefunden; vorhandene Memory-Fixtures sind kein echter Ingest. | RETICORA_SIMULATE wird nicht ausgewertet; kein deterministischer 30-Geräte-Generator am echten Ingest-Pfad nachgewiesen. | High | L |

## Befunde im Detail

### GLO-11 — High: fehlende Traceability-Datei und Durchsetzung

Die in README:352 behauptete CSV existiert nicht, ebenso wenig ein entsprechender Check in den vorhandenen Workflows/Makefile; das ist keine vorhandene leere CSV. Die 224 bekannten [B]/[Pn]/[A]-IDs der bisherigen Berichte und 37 neue solche IDs aus Teil 08 ergeben 261 eindeutige IDs; GLO-10 ist jetzt eine echte Anforderungszeile, nicht zusätzlich doppelt zu zählen. Die 13 ausschließlich [Q]-IDs dieses Teils sind nicht in diesem speziellen CI-Abdeckungsnenner, bleiben aber gemäß GLO-11 dokumentationspflichtig. Fehlender Teil 03 verhindert eine Vollkatalogquote.

**Empfehlung, nicht umgesetzt:** IDs mit Tag/Epic/Testdatei erfassen, echte Testart und Ausführungsgrenzen kenntlich machen und Vollständigkeit gegen den Katalog in CI prüfen; neue Features erst mit ihrem Eintrag zulassen.

### SIM-01/02 — High: Demo-Ansätze sind kein Abnahmedatensatz oder Simulator

Migration 54 legt lediglich „Reticora Demo“ an; das separate Audit-Seed-Kommando legt standardmäßig eine Enterprise-Restore-Org mit fünf Audit-Einträgen an. Damit sind weder die geforderten Baseline-Objekte noch die P2-Ergänzungen oder der skalierte Lasttest-Datensatz erzeugt. Makefile:114 verschluckt sogar das Scheitern am fehlenden seed.sql. Konfiguration und Einstiegspunkte besitzen keinen RETICORA_SIMULATE-Schalter.

**Empfehlung, nicht umgesetzt:** Reproduzierbaren Seed mit allen ausdrücklich genannten Beständen und skalierbarer Variante sowie einen deterministischen Simulator über den echten autorisierten Ingest schaffen; Anzahl, Beziehungen, Isolation, Wiederholbarkeit und Revieweffekte durch Integrationstests abnehmen. Keine bloßen direkten CI-DB-Inserts als SIM-02 ausgeben.

## Offene Fragen

- Vollständiger v2-Katalog fehlt weiterhin; Suche im Dokumentbestand und Bestandsaufnahme:210–225 liefern keinen Originaltext. UI-01–10 und ABN-01(a/b) dürfen nicht als rekonstruierte Einzelverträge gelten.
- Bestätigungsbedürftige Entscheidungen bleiben Vorschläge; GATE-02 verwendet SOLL/WARN, obwohl das vorgegebene Statusschema keinen WARN-Status definiert.
- Externe Betriebs-, Browser-, Last- und Sicherheitsnachweise sind nicht durch Quellcode oder Mocks ersetzt.

## Stand

**UNVOLLSTÄNDIG – fortsetzen ab ID GLO-10.**

### Tatsächliche Prüfläufe dieser Fortsetzung

Go 1.25.14 aus `/opt/hostedtoolcache/go/1.25.14/x64/bin/go`; durchgehend `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly`, TEST_DATABASE_URL nicht gesetzt. Keine Dienste gestartet, keine neuen Abhängigkeiten oder Toolchains installiert/nachgeladen. Anders als beim dokumentierten Teil-09-Vorfall wurden die Schutzflags hier nicht entfernt; dessen historische Abweichung wird nicht nachträglich aufgehoben.

| Lauf | Ergebnis | Grenze |
|---|---|---|
| Backend: `go test -short -count=1` für internal/monitoring, config, discovery, topology, override, api, audit, rack, citype, platform/crypto, webhook, entitlement, server | Exit 1: 11 Pakete Setupfehler wegen fehlender Offline-Modulauflösung (`chi/v5@v5.3.1`); config und platform/crypto erfolgreich | Keine fehlgeschlagene Fachassertion bewiesen; insbesondere Monitoring-/Servertests nicht ausgeführt |
| Backend: `go build ./internal/monitoring ./internal/config ./internal/discovery ./internal/topology` | Exit 1, gleiche Offlineblockade | Kein erfolgreicher Gesamt-/Bereichsbuild |
| Collector: `go test -short -count=1 ./...` | Exit 0, zehn Testpakete erfolgreich, zwei ohne Testdateien | Protokoll-/Plugin-Komponententests, kein echter vollständiger Ingest bis Dashboard |
| Edgecore: `go test -short -count=1 ./...` | Exit 0, fünf Testpakete erfolgreich | Lokale Puffer-/Enrollment-/Keystore-/Transport-/Update-Tests, keine 24h-/Last-/Plattformabnahme |
| Frontend / golangci-lint | Nicht ausgeführt: node_modules bzw. Linter nicht vorhanden | Keine Installation; kein Vitest-/Playwright-/axe-/Lint-/Browser-PASS |

Temporäre Rohlogs: `/tmp/reticora-audit-08/backend-tests.log`, `/tmp/reticora-audit-08/backend-build.log`, `/tmp/reticora-audit-08/collector-tests.log`, `/tmp/reticora-audit-08/edgecore-tests.log`. Diese Pfade sind keine dauerhaften Repo-Artefakte; Ergebnisse und Grenzen stehen deshalb hier.

### GitHub-Actions-Nachweis, getrennt vom lokalen Lauf

GitHub-MCP-Abfrage vom 2026-10-01: letzter aufgelisteter `ci.yml`-Lauf [35772333628](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/35772333628), 2026-09-22, main `895336da05c2547b0078b262d89a9a6b42d0b7b7`, Gesamtstatus failure. Das ist kein Lauf auf dem aktuellen Auditcommit; erfolgreiche „Running Copilot cloud agent“-Läufe sind keine Produkt-CI-Abnahme.

- Backendjob 106896848433: Build, Race-/Coverage-Tests und Vet erfolgreich; Kustomizejob 106896848629 ebenfalls erfolgreich.
- Migrationsjob 106896848083: Up/Down/Up erfolgreich, anschließend PostgreSQL-Integrationstestschritt fehlgeschlagen; die beiden nachgelagerten RLS-Assertions übersprungen. Die gelesenen Logenden erklären nicht belastbar den konkreten Assertionfehler, deshalb keine Ursachenbehauptung aus Container-Nebenmeldungen.
- lint-backend 106896848447: `latest` installierte golangci-lint v2.13.2; Aufruf scheitert mit „unsupported version of the configuration: ""“, Exit 3. Kein durchgelaufener Lint.
- Frontendjob 106896848597: ESLint erfolgreich, Prettier meldet 18 Dateien und Exit 1; API-Generierungscheck, Typecheck, Vitest und Build danach übersprungen. E2E-Job 106897043774 übersprungen.
- Keine Wiederholung oder Änderung an Workflows/Produktdateien veranlasst. Vorhandene Checks sind nicht gleichbedeutend mit grünen Ausführungen oder vollständiger Testauswahl.
