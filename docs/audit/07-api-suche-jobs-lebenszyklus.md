## Zusammenfassung

Prüfstand: `895336da05c2547b0078b262d89a9a6b42d0b7b7`, 2026-10-01. Reiner Dokumentationsauftrag; Produktivcode, Migrationen und Tests bleiben unverändert.
Zwischenstand: 8/39 IDs erfasst; die übrigen Prüfungen laufen. Kein G1-PASS ableitbar.

| Tag | PASS | PARTIAL | ABWEICHEND | FAIL | OFFEN | N/P |
|---|---:|---:|---:|---:|---:|---:|
| [B] | 0 | 3 | 2 | 1 | 0 | 0 |
| [P2] | 0 | 0 | 0 | 0 | 2 | 0 |

High/G1-Blocker bisher: API-09, BLK-01, JOB-01–04: kein generischer Jobvertrag, Bulk nur als parallele Einzelrequests, fehlender zentraler Scheduler und Wiederanlauf.
Offene Bearbeitung: API-01–08, GQL-01–04, SRC-01–04, VIE-01–02, EXP-01, TLC-01–07, NTF-01–05.
Featuretests und Server-Build sind durch eine offline fehlende Abhängigkeit blockiert; dies ist kein nachgewiesener fachlicher Testfehler. Details unter Stand.

## Ergebnis je Anforderung als Tabelle

| ID | Tag | Status | Evidenz | Tests | Befund (1–2 Sätze) | Severity | Aufwand |
|---|---|---|---|---|---|---|---|
| API-09 | [B] | FAIL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job_handler.go:40–44`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:429–431`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/router.go:NewRouter` | Export-/Discovery-Tests vorhanden, Ausführung blockiert; kein generischer Job-API-Test. | Nur `/api/v1/export/jobs` und `/api/v1/discovery/jobs`, kein `/api/v1/jobs` für die sechs verlangten Operationen. Spätere Import-/Template-/Org-Funktionen werden nicht zusätzlich als fehlende Phase-1-Fachmodule gewertet. | High | L |
| BLK-01 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/CIListPage.tsx:71–89`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/AssetListPage.tsx:90–108`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/handler.go:RegisterRoutes` | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/CIListPage.test.tsx:53` prüft gemockten Einzelfall; Frontend nicht ausführbar. | Bulk-Status existiert nur als `Promise.all` einzelner PATCH-Aufrufe; keine serverseitige 500/100-Grenze und kein Fehlerreport pro ID. Bei Teilfehlern zeigt die Oberfläche lediglich einen Sammelfehler, bereits erfolgreiche Änderungen bleiben bestehen. | High | L |
| BLK-02 | [P2] | OFFEN | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/CIListPage.tsx:71–89`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job_handler.go:40–44`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/history/model.go:Replay` | Keine Tests des geforderten Bulk-Change-Sets/Revert-Vertrags gefunden. | Der BLK-01-Status-Fan-out und Historien-Replay sind kein erweitertes Bulk-/Undo-Modul; kein entsprechender REST-Vertrag oder Change-Set-Speicher gefunden. Die fehlende generische Jobbasis muss für die spätere Umsetzung ergänzt werden, ohne belegte prinzipielle Architekturblockade. | – | – |
| IMP-IO-01 | [P2] | OFFEN | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:38–82,612`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job.go:20–43`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/router.go:NewRouter` | Kein Import-/Mapping-/Dry-Run-Test gefunden. | Discovery-Ingest ist kein CSV-/Excel-/API-Import mit Mapping-Profilen und Import-Provenienz; hierfür kein eigener Implementierungsbestand gefunden. Job-/Mapping-Strukturen fehlen, eine spätere additive Umsetzung ist nicht nachweislich blockiert. | – | – |
| JOB-01 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000017_export_jobs.up.sql:4–33`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000035_export_job_formats.up.sql:13–22`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job.go:9–43`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/worker.go:95–144`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/pg_job_repository.go:174–198` | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job_test.go` (MemoryRepository/FileStore), blockiert; kein Queue-/Crash-/RLS-Integrationstest. | Exportjobs sind persistent, aber Status, Felder und Org-Nullbarkeit entsprechen nicht `job`; Ausführung pollt Postgres statt NATS-Work-Queue. Retry/Backoff, kindbezogene Timeouts und Wiederaufnahme liegengebliebener `running`-Jobs fehlen; Ergebnis-RLS schützt nur die Organisation. | High | L |
| JOB-02 | [B]; Zusätze [P2], [P3] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:266–291`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/reservation/sweeper.go:25–44`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/workflow/executor.go:246`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/privacy/handler.go:RegisterRoutes` | Kein zentraler Scheduler-/HPA-/Catch-up-Test gefunden; vorhandene Worker-Tests blockiert. | Einzelne Ticker ersetzen keine vollständigen Cron-Systemjobdefinitionen; kein Advisory-Lock je Kind und kein persistentes, begrenztes Catch-up gefunden. Die acht Baseline-Systemjobs sind nicht als vollständiger Scheduler verdrahtet; die späteren Computed-/Workflow-Zeitpläne bleiben OFFEN. | High | L |
| JOB-03 | [B] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job_handler.go:40–44`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/ExportPage.tsx:ExportPage`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/middleware/metrics.go`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/App.tsx` | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/ExportPage.test.tsx` nur Stub-Requests; Job-API-Ausführung blockiert. | Export-/Discoverylisten sind vorhanden, aber die vier generischen Endpunkte einschließlich Cancel/Revert und ein allgemeines Jobcenter fehlen. Keine Jobmetriken für Dauer, Fehler, Rückstau und Alter des ältesten queued Jobs gefunden. | High | L |
| JOB-04 | [B] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:266–273`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/worker.go:95–117`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/review.go:286–310`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/history/handler.go:87–91`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/CIListPage.tsx:71–89` | Export-Worker-Tests vorhanden, offline blockiert; keine Tests der geforderten allgemeinen Jobdelegation. | Nur der gesonderte Exportweg läuft tatsächlich als persistenter Hintergrundjob; Bulk und Review-Merge laufen ohne Job, State-Snapshot wird synchron berechnet. Import, vollständiger CI-Merge, Template-Migration, Org-Export/-Löschung und Index-DDL haben keine solche Jobanbindung. | High | L |

## Befunde im Detail

### API-09, JOB-01, JOB-03 — High: kein einheitlicher Jobvertrag

**Beschreibung:** Der gesamte konkrete Bestand besteht aus Export-/Discoveryjobs sowie separaten Workflow-/IGA-Aufträgen, nicht aus einer universellen `job`-Tabelle. Bei `export_job` ist `organization_id NOT NULL`; Status sind `pending`, `running`, `completed`, `failed`, `expired` statt `queued`, `running`, `succeeded`, `failed`, `cancelled`. `kind`, `progress`, generische `params/result/error`, `idempotency_key`, Surrogat-`created_by` und `finished_at` fehlen unter dem verlangten Vertrag; vorhandene Filter, Ergebnisdatei, Fehlertext und Zeitstempel sind Teilleistungen, keine Gleichheit. Die Policy hat USING **und** WITH CHECK mit Org-Vergleich oder `app.system='on'`, jedoch keine Benutzer-/Client-/Site-/Teamgrenze.

**Betroffene Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000017_export_jobs.up.sql:4–33`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000035_export_job_formats.up.sql:13–26`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job.go:9–61`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job_handler.go:40–44`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:429–431`.

`ClaimPending` setzt `running` innerhalb der Claim-Transaktion und verwendet korrekt `FOR UPDATE SKIP LOCKED`, verhindert damit konkurrierendes Claiming **derselben Zeile**, aber keine parallelen Runner desselben Job-Kinds. Der Worker verarbeitet danach ohne Lease, Requeue oder Retry; ein Prozessabbruch nach Commit oder ein fehlgeschlagenes `CompleteJob` kann den Auftrag dauerhaft in `running` belassen. `fail` markiert endgültig `failed`. NATS existiert als Event-Publisher-Port (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/platform/events/nats.go:22–30,84–130`), nicht als Job-Work-Queue.

**Korrekturempfehlung (nicht umgesetzt):** Einheitlichen persistierten Jobvertrag, scopegebundene Ergebnisse und Cancel/Revert-Lebenszyklus spezifizieren; exportierende und spätere Operationen anbinden. NATS-Work-Queue, kindbezogene Timeouts, begrenzte Retries/Backoff und Crash-Recovery ergänzen und mit realer DB/Queue sowie Mehrinstanztests absichern. Generisches Jobcenter und die vier verlangten Metriken ergänzen. Revert-Fachlogik bleibt an die Phase von BLK-02 gebunden, fehlende generische Status-/Cancel-Funktionen blockieren bereits G1.

### API-08, BLK-01 — High: vermeintliches Bulk ist Einzelrequest-Fan-out

**Beschreibung/Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/CIListPage.tsx:71–89` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/AssetListPage.tsx:90–108` starten parallele PATCH-Aufrufe mit `Promise.all`. Weder maximal 500 IDs noch die Jobgrenze **ab einschließlich 100** werden serverseitig umgesetzt. Ein einziger Fehler beendet die gemeinsame Erfolgsauswertung; andere Requests können bereits erfolgreich oder noch unterwegs sein. Das ist kein Report je ID und kein serverseitiges Change-Set. Discovery-`/ingest/bulk` verarbeitet Beobachtungen und ist kein Ersatz für diese Statusaktion.

**Korrekturempfehlung (nicht umgesetzt):** Echte Bulk-API mit IDs, Rechte-/Scopeprüfung je Objekt, 500er Maximum, Jobübergabe ab 100 und persistentem Teilfehlerergebnis definieren; UI wertet das Ergebnis je ID aus. Für BLK-02 später Change-Sets und zeitlich begrenztes, overridekonformes Revert ergänzen. Tests müssen 99/100/500/501 und gemischte Erfolge/Misserfolge abdecken.

### JOB-02 — High: Schedulerpflichten nicht durch Ticker erfüllt

**Beschreibung/Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:266–291` startet Export-Polling (2 Sekunden), Alert-Evaluation (Minute) und Reservierungs-Sweeper (Minute). `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/reservation/sweeper.go:25–44` führt ohne Advisory-Lock aus; sein Ergebnis/Fehler wird verworfen. Diese Worker haben weder Cron-Definitionen noch persistierten letzten Lauf und begrenztes Nachholen.

| Verlangter Systemjob | Belegbarer Ausführungsbestand / Lücke |
|---|---|
| Offline-Detector | Collector-Status/Heartbeat vorhanden; kein im Server gestarteter periodischer Offline-Detector gefunden. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:Handler`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:266–291`. |
| Kanten-Cleanup | Topologie-/Beziehungs-Repositories vorhanden, kein periodischer Cleanup-Job registriert. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/relationship/pg_repository.go`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/topology.go:deriveTopology`. |
| Hard-Delete | CI-CRUD statt verdrahtetem zeitgesteuertem Hard-Delete. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/pg_repository.go:Delete`. |
| Audit-Runner | Audit-Verifikation als CLI/Handler und transaktionaler Recorder, kein Schedulerlauf. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/audit-verify/main.go:main`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/audit/audit.go:PGRecorder.Record`. |
| Index-DDL | SQL-Migrationsindizes und Such-Reindex sind keine geplanten Attribut-DDL-Jobs. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/search/handler.go:Reindex`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000030_search_ai.up.sql`. |
| Retention | Konfigurations-/Erasure-API, kein automatischer Audit-Retention-Runner. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/privacy/handler.go:RegisterRoutes`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/privacy/privacy.go:RunErasure`. |
| Zertifikatserneuerung | Kein serverseitiger Job-Kind/Schedulerlauf gefunden; Deployment-TLS ersetzt diesen Jobvertrag nicht. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:main`. |
| Entitlement-Warnungen | Requestgebundene Entitlement-Prüfung, keine geplante Warnzustellung. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:Service`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:227–232,307–319`. |
| Computed-Refresh [P2], Workflow-Zeitpläne [P3] | Kein geplanter Runner gefunden; das Akzeptieren des Trigger-Typs `schedule` ist keine Cron-Ausführung. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/workflow/executor.go:246`. |

**Korrekturempfehlung (nicht umgesetzt):** Baseline-Kinds mit vollständigen Cron-Definitionen verdrahten; pro Kind einen DB-Advisory-Lock über die gesamte Ausführung halten und erfolgreiche Läufe persistent erfassen. Begrenztes Catch-up sowie Tests mit mehreren Replikas und Neustarts festlegen. Audit-Hashkettenlocks sind nicht mit Schedulerlocks gleichzusetzen.

### JOB-04 — High: asynchroner Export ist noch keine allgemeine Jobarchitektur

| Große Operation | Tatsächlicher Ablauf |
|---|---|
| Export | `POST /api/v1/export/jobs` → persistenter Auftrag → Polling-Worker; zusätzlich synchroner direkter CI-Export. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job_handler.go:49–82`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/worker.go:95–117`. |
| Import | Kein IMP-IO-Importmodul; Discovery-Ingest ist synchron und kein Importjob. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:612`. |
| Bulk ab 100 | UI-Einzelrequests, kein Job. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/CIListPage.tsx:71–89`. |
| Merge | Review-„merge“ führt im Request aus; kein vollständiger CI-Merge-/Unmerge-Job. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/review.go:286–310`. |
| Template-Migration | Keine Migrationsjobanbindung; CI-Typ-/Formularversionen allein sind kein Datenmigrationsauftrag. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/citype/handler.go:RegisterRoutes`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/form/handler.go:RegisterRoutes`. |
| Org-Export/-Löschung | Kein generischer oder dedizierter Org-Job gefunden. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/router.go:NewRouter`. |
| Index-Anlage | Indizes in Migrationen; Reindex baut Suchdokumente synchron auf, nicht DDL als Job. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/search/handler.go:Reindex`. |
| Snapshot | Zustand wird synchron per `Replay` berechnet, keine Snapshot-Jobanbindung. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/history/handler.go:87–91`. |

**Korrekturempfehlung (nicht umgesetzt):** Vorhandene große Baseline-Operationen an den Jobvertrag delegieren; spätere Operationen bei ihrer Einführung anbinden, nicht allein wegen ihrer späteren Phase als zusätzliche G1-Fachblocker werten. Snapshot-Begriff vor Umsetzung klären.

## Offene Fragen

- Vollständiger v2-Text fehlt: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-bestandsaufnahme.md:219–225`; erneute Suche in Dokumenten und Root-README findet nur Referenzen/Übersichten, keinen v2-Anforderungskatalog. API-03-Fehlertabelle und lediglich referenzierte Detailanforderungen bleiben insoweit N/P.
- JOB-03 verlangt Revert schon unter [B], BLK-02 erst [P2]; zu klären ist, ob in G1 nur ein definierter Endpunkt oder bereits die gesamte Undo-Fachlogik fällig ist. Hier werden die ohnehin fehlenden generischen Jobfunktionen separat gewertet.
- JOB-04 nennt spätere Import-/Template-/Org-Funktionen pauschal unter [B]; ihre fehlende Jobdelegation wird dokumentiert, nicht entgegen der Phasenregel als vorgezogene Pflicht zur Umsetzung dieser gesamten Module gewertet.
- Bedeutet „Snapshot“ in JOB-04 Historien-State-at, einen CMDB-Gesamtsnapshot oder einen Betriebssnapshot? Ein synchroner State-at-Pfad ist vorhanden, ein generischer Snapshot-Job nicht.

## Stand

UNVOLLSTÄNDIG – fortsetzen ab ID API-01.

Prüfmethodik: Quellen einschließlich produktiver Verdrahtung und Migrationen statisch geprüft; keine Live-DB, kein NATS, kein Keycloak, kein SMTP/S3 gestartet. Keine neuen Projektabhängigkeiten installiert. Ein initiales `go version` mit dem Standardbinary löste die automatische Go-1.25.0-Toolchain-Auflösung aus; sämtliche eigentlichen Prüfungen wurden danach explizit mit der bereits vorhandenen `/opt/hostedtoolcache/go/1.25.14/x64/bin/go` und deaktivierter Netzauflösung durchgeführt.

Ausführung im Verzeichnis `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend`, jeweils `TEST_DATABASE_URL` entfernt, `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly`:

- `go test ./internal/api/... ./internal/middleware ./internal/ci ./internal/server ./internal/graphqlbff ./internal/search ./internal/savedview ./internal/export ./internal/user ./internal/identity ./internal/tenantapi ./internal/entitlement ./internal/discovery ./internal/privacy ./internal/monitoring ./internal/webhook ./internal/reservation ./internal/config`: Exit 1. `api/speccheck` und `config` grün; `api/generated` ohne Tests; Featurepakete `[setup failed]` mit `github.com/go-chi/chi/v5@v5.3.1: module lookup disabled by GOPROXY=off`. Insbesondere der eigentliche Router-Parity-Test ist **nicht grün nachgewiesen**.
- `go build ./cmd/server`: Exit 1 mit demselben Offline-Modulauflösungsfehler; kein fachlicher Builddefekt daraus abgeleitet.
- Frontendtests nicht ausgeführt: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/node_modules/.bin/vitest` fehlt; keine Installation gemäß Auftrag. Bestehendes Testkommando: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/package.json:15`.
- Rohlogs nur temporär: `/tmp/reticora-audit-07-test.log`, `/tmp/reticora-audit-07-build.log`; die Ergebnisse oben sind der dauerhafte Beleg. Kein PASS allein aufgrund von Memory-Repositories, HTTP-Stubs oder Testdateiexistenz (TST-04).
