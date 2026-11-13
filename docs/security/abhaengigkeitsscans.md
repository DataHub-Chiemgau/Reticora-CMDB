# Abhängigkeits- und Container-Scans

Anforderungen: SEC-11 ([Q]: „Abhängigkeits- und Container-Scans in der CI;
CVE-Fix-SLA Critical 7 Tage, High 30 Tage“) und TST-04. Werkzeuge nach E-31:
govulncheck, npm audit, Trivy. Umsetzung: WP-004, Abdeckung aller
Release-Images und SLA-Prüfung der Ausnahmen: WP-389.

## Jobs

Alle Jobs stehen in `.github/workflows/ci.yml`. Sie laufen bei jedem Pull
Request und bei jedem Push auf `main`.

| Job | Gegenstand | Blockiert bei | Bericht (Artefakt, 30 Tage) |
|---|---|---|---|
| `govulncheck` (Matrix `backend`, `collector`, `edgecore`) | Go-Abhängigkeiten und Standardbibliothek jedes go.work-Moduls | Schwachstellen, deren betroffenes Symbol vom Code des Moduls erreicht wird | `govulncheck-<modul>` (JSON, auch nur importierte Funde) |
| `npm-audit` | `frontend/package-lock.json` | High/Critical in Produktionsabhängigkeiten (`--omit=dev`, ausgeliefertes Bundle) | `npm-audit` (Produktion und alle inkl. Entwicklung) |
| `image-scan` (Matrix `backend`, `collector`, `frontend`, `postgres`, `keycloak-db`) | gebautes Container-Image: OS-Pakete und Anwendungsabhängigkeiten (Trivy) | Critical/High mit verfügbarem Fix (`ignore-unfixed`) | `trivy-<image>` (JSON) |
| `image-scan-thirdparty` (Matrix `clamd`) | gepinntes Fremdimage des Betriebs, das nicht vertrauenswürdige Eingaben parst: clamd aus WP-735 (`deploy/docker-compose/docker-compose.yml`, `deploy/k8s/base/components/clamav/clamd.yaml`); beide Pins müssen Version und Digest teilen (WP-329) | wie `image-scan` (Trivy 0.75.0 per Digest, `--severity CRITICAL,HIGH --ignore-unfixed`, `--ignorefile security/scan-exceptions.txt`) | `trivy-thirdparty-<image>` (JSON) |

### Abhängigkeiten der Phase 2

Die Phase 2 nimmt diese Abhängigkeiten neu auf; die Jobs erfassen sie ohne
Änderung, weil sie die go.work-Module bzw. `frontend/package-lock.json` als
Ganzes prüfen (Stand WP-329):

| Abhängigkeit | Entscheidung, WP | Ausgeliefert | Erfasst durch |
|---|---|---|---|
| `github.com/xuri/excelize/v2` v2.11.1 | E-49, WP-301 | ja (Server) | `govulncheck` (`backend`) |
| `github.com/boombuler/barcode` v1.1.0 | E-52, WP-763 | ja (Server) | `govulncheck` (`backend`) |
| `github.com/go-pdf/fpdf` v0.9.0 | E-52, WP-763 | ja (Server) | `govulncheck` (`backend`) |
| `github.com/makiuchi-d/gozxing` v0.1.1 | E-52, WP-763 | nein, nur in `backend/internal/labels/render_test.go` | nicht: `govulncheck` läuft ohne `-test`; geführt wie die nicht ausgelieferten Entwicklungsabhängigkeiten von npm |
| `@zxing/browser` | E-52, WP-807 | ja (Bundle) | `npm-audit` (Produktion, blockierend) |
| `@dnd-kit/core`, `@dnd-kit/sortable` | E-54, WP-797 | ja (Bundle) | `npm-audit` (Produktion, blockierend) |

### Abhängigkeiten der Phase 3

Die Phase 3 nimmt nach [E-195](../plan/phase3/entscheidungen.md#e-195)
(Fremdbibliothek im Collector nur für gNMI/gRPC, mit Lizenzprüfung und SBOM)
diese Abhängigkeiten neu auf; `govulncheck` erfasst sie ohne Änderung, weil
der Job das Modul `collector` als Ganzes prüft, die SBOM des Releases
(syft) führt sie automatisch (Stand WP-1015):

| Abhängigkeit | Version | Lizenz | Entscheidung, WP | Ausgeliefert | Erfasst durch |
|---|---|---|---|---|---|
| `github.com/openconfig/gnmi` | v0.14.1 | Apache-2.0 | E-195, E-193, WP-1015 | ja (Collector, gNMI-Client `collector/probe/gnmi`) | `govulncheck` (`collector`) |
| `google.golang.org/grpc` | v1.84.0 | Apache-2.0 | E-195, WP-1015 | ja (Collector) | `govulncheck` (`collector`) |
| `google.golang.org/protobuf` (transitiv) | v1.36.12 (Upd-03; WP-1015: v1.36.11) | BSD-3-Clause | E-195, WP-1015 | ja (Collector) | `govulncheck` (`collector`) |
| `google.golang.org/genproto/googleapis/rpc` (transitiv) | v0.0.0-20260928230214-8a89bd6388cc (Upd-03; WP-1015: v0.0.0-20260706201446-f0a921348800) | Apache-2.0 | E-195, WP-1015 | ja (Collector) | `govulncheck` (`collector`) |
| `golang.org/x/text` (transitiv) | v0.44.0 (Upd-03; WP-1015: v0.43.0) | BSD-3-Clause | E-195, WP-1015 | ja (Collector) | `govulncheck` (`collector`) |

Der Client verwendet TLS immer (kein `insecure.NewCredentials`, kein
`grpc.WithInsecure`) und hat weder `Set` noch Subscribe STREAM/POLL.
Binärgröße des Collectors (linux/amd64, Build wie `collector/Dockerfile`,
`-ldflags="-s -w"`): vor und nach WP-1015 10.637.474 Byte, weil
`cmd/collector` den Client erst mit dem Probetyp `gnmi` (WP-1016) einbindet;
WP-1016 misst die Größe erneut.

### Nachtrag 2026-11-13: Go-Module mit Repository-Advisories (Update-Plan Upd-03)

`govulncheck` (v1.8.0 wie v1.9.0, Datenbank vom 2026-11-11) meldete auf dem
Stand `167ec380` nur GO-2026-6865 (klauspost/compress) und GO-2026-5932
(x/crypto/openpgp), beide nur importiert. Mehrere eingesetzte Module waren
laut den Repository-Advisories ihrer Hersteller aber verwundbar; diese
Advisories stehen weder in vuln.go.dev noch in der globalen GitHub Advisory
Database bzw. OSV und sind deshalb für `govulncheck`, OSV-Scanner, Trivy und
Dependabot unsichtbar (Abfrage `gh api repos/<owner>/<repo>/security-advisories`).
Behoben durch Aktualisierung (Quelle der Versionen: proxy.golang.org `@latest`):

| Modul | vorher → nachher | Advisories |
|---|---|---|
| `github.com/gosnmp/gosnmp` (Collector, SNMPv3-Polling) | v1.43.2 → v1.47.0 | GHSA-w96x-5v55-24xx (critical: SNMPv3-Auth-Bypass, Remote-Panic im USM-Parsing) |
| `github.com/jackc/pgx/v5` (+ `jackc/puddle/v2` v2.2.2 → v2.2.3) | v5.10.0 → v5.13.0 | GHSA-j38q-h3ph-jjpq (high), GHSA-95fj-3fxw-g62g (high), GHSA-w9rp-vfrh-q8vv (low) |
| `go.opentelemetry.io/otel*` (otel, sdk, sdk/metric, metric, trace, log, OTLP-HTTP-Exporter; `proto/otlp` v1.11.0 → v1.11.1, `metric/x` v0.69.0) | v1.45.0 → v1.47.0 | GHSA-rrfc-gjm9-cgrj (medium, Baggage-Propagator), GHSA-q7gw-24fr-5p69 (medium) |
| `github.com/go-chi/chi/v5` | v5.3.1 → v5.5.0 | GHSA-j6qf-3m9c-vwj3 / CVE-2026-114437 (medium), GHSA-rqrp-8cg2-5958 (low) |
| `github.com/klauspost/compress` (indirekt, explizit angehoben) | v1.20.0 → v1.20.4 | GO-2026-6865 / CVE-2026-116591 / GHSA-fpmh-frq8-7rj8 (high) |
| `golang.org/x/crypto` (+ `x/sys` v0.50.0, `x/term` v0.48.0, `x/text` v0.44.0) | v0.58.0 → v0.59.0 | GO-2026-5932 |
| `golang.org/x/image` (indirekt über excelize, Backend) | v0.44.0 → v0.48.0 | GO-2026-6222 / CVE-2026-46603 |

Durch die Mindestversionen der neuen Module steigen außerdem `golang.org/x/tools`
(v0.52.0), `x/mod`, `x/sync`, `grpc-gateway/v2`, `genproto` und `protobuf`.

**Graph-Pins.** `github.com/go-jose/go-jose/v4` v4.1.4 (GHSA-mgcc-cww5-fp2q
high u. a., Fix 4.1.7), `github.com/yuin/goldmark` v1.4.13 (GO-2026-5320, Fix
1.7.17) und im Collector `go.opentelemetry.io/otel*` v1.44.0 stehen nur im
Modulgraphen, weil die `go.mod` von `google.golang.org/grpc` (auch v1.85.0)
bzw. `golang.org/x/tools` (auch v0.52.0) sie nennt; gebaut wird keines
(`go mod why -m`: „main module does not need module“). `go mod tidy` entfernt
eine bloße Anforderung darauf, deshalb heben versionierte `replace`-Direktiven
in `backend/go.mod` und `collector/go.mod` genau diese Versionen an
(go-jose v4.1.9, goldmark v1.8.7, otel v1.47.0). Sie greifen nur für die
genannte Version und werden wirkungslos, sobald grpc oder x/tools höher
ziehen; bei jedem Update dieser Module ist eine OSV-Abfrage über
`go list -m all` der drei Module zu wiederholen. `go.work.sum` ist neu erzeugt
und enthält keine verwundbare Version mehr.

Nachweis (lokal, 2026-11-13): `govulncheck` v1.9.0 meldet für `backend`,
`collector` und `edgecore` keinen Fund; die OSV-Abfrage (`api.osv.dev`
querybatch) über `go list -m all` jedes Moduls und über `go.work.sum` ergibt
keinen Eintrag. Die fehlende Erkennung der Repository-Advisories im Gate
schließt das Paket Upd-04.


### Fremdimages des Betriebs

Neben den eigenen Release-Images startet der Betrieb Fremdimages. Jedes ist
per Version und Digest gepinnt und wird im Job `image-scan-thirdparty`
(`.github/workflows/ci.yml`) mit Trivy gescannt (Schwelle wie `image-scan`:
CRITICAL und HIGH mit verfügbarem Fix). Compose und Kubernetes nennen für
dasselbe Image dieselbe Referenz; `tests/ci-config.test.sh`
(`thirdparty_image_pins`) prüft Pin, Matrixeintrag und diese Tabelle
(SR-60, WP-873).

Befunde in Abhängigkeiten der Hersteller-Images ohne neueres Release stehen
in `security/scan-exceptions-thirdparty.txt` (E-246): gleiches Format und
gleiche SLA wie `security/scan-exceptions.txt` (Critical 7, High 30 Tage ab
dem ersten Scan), geprüft von `scripts/ci/check-scan-exceptions.sh`. Nur
`image-scan-thirdparty` liest diese Datei, so verdeckt ein Hersteller-Befund
keine ID in den Scans der eigenen Images. Stand 2026-11-10: 70 IDs (6
Critical, Ablauf 2026-11-17) nach WP-1178 (Backup-CronJob auf eigenem Image);
Risikoakzeptanz für MinIO, OpenSearch und keycloak-db offen (A-85).
Nachtrag 2026-11-13: keycloak-db läuft auf `postgres:16.16-alpine`; das
Hersteller-Release behebt die neuen High-Befunde in `krb5-libs` und
`libxslt`, `gosu` trägt weiter Go 1.24.6 (dieselben IDs, A-85).
Nachtrag 2026-11-13: Amazon Linux 2023 hat Fixes für `sqlite-libs`
(CVE-2026-119371, -119372, -119373) und `rpm` (CVE-2026-95520, -95521)
veröffentlicht; `opensearchproject/opensearch:2.19.7` ist weiter das neueste
2.x-Release (Digest unverändert) und enthält sie nicht. Fünf neue High-Ausnahmen
bis 2026-12-13 (75 IDs); OpenSearch ist optional und nur intern erreichbar (A-85).
Nachtrag 2026-11-13 (Update-Plan Upd-01): Der Objektspeicher läuft auf
`pgsty/silo` (Release 2026-11-12, Pin in der Tabelle) statt auf dem
eingefrorenen `pgsty/minio` (Release 2026-08-04; Upstream `minio/minio` ist
archiviert; Silo ist derselbe Fork unter neuem Namen). Trivy findet im neuen
Image keinen Critical- oder High-Befund mit Fix (amd64 und arm64; das alte
Image: 31 IDs, 3 Critical und 28 High). Die 31 MinIO-IDs sind aus der
Ausnahmedatei entfernt (20 Einträge gestrichen, 11 bleiben für keycloak-db):
55 IDs. Das Standard-Image, nicht `-distroless`: Healthcheck
(`mc ready local`), `tests/backup-config.test.sh` und `tests/k8s/kind-smoke.sh`
brauchen `mc` und `sh` im Container. Alle Referenzen auf das Image sind
gleich, `tests/ci-config.test.sh` (`objectstorage_image_refs`) prüft das.
Restrisiko Bus-Faktor des Forks: E-246.
Nachtrag 2026-11-13 (Update-Plan Upd-01, keycloak-db): keycloak-db läuft auf
dem eigenen Image `reticora-keycloak-db` (siehe unten, E-96, A-85), das
`image-scan` (`keycloak-db`) ohne Hersteller-Ausnahmen scannt. Die 26 IDs
seines Hersteller-`gosu` (Go 1.24.6, darunter CVE-2025-68121 Critical) sind
aus `security/scan-exceptions-thirdparty.txt` entfernt: 15 Zeilen entfallen,
11 gelten weiter nur für minio (60 IDs, 5 Critical).

| Image | Ort | Scan |
|---|---|---|
| `clamav/clamav:1.5.4@sha256:6d87c712e3391e13c1ba6e05dfb074d4b1ddb5a6834b23bb9da990d2a4ef042a` | `deploy/docker-compose/docker-compose.yml` (`clamd`), `deploy/k8s/base/components/clamav/clamd.yaml`, Dev-Stack `docker-compose.yml` | `image-scan-thirdparty` (`clamd`) |
| `timescale/timescaledb:2.31.0-pg16@sha256:322ae364eca3321cf2a48f1ef31213b87843413b3cf8feeebf54e999a26bb731` | Basis von `reticora-postgres` (der logische Backup-CronJob nutzt seit WP-1178 das eigene Image `reticora-cmdb/postgres`) | über `image-scan` (`postgres`), dieselbe Referenz (E-96, `timescale_image_refs`) |
| `nats:2.14.10-alpine@sha256:311da47e2f31c26e99c623cd78d486a1e8ab039c49b32652770bb91a4f70512e` | Compose (`nats`, `nats-edge`), `deploy/k8s/base/components/nats-edge/nats-edge.yaml` | `image-scan-thirdparty` (`nats`) |
| `redis:7.4.12-alpine@sha256:7b1753b4c5ad8458906c272a8132b8c15f5557b000379eb115fb239706a384b7` | Compose (`redis`) | `image-scan-thirdparty` (`redis`) |
| `pgsty/silo:RELEASE.2026-11-12T00-00-00Z@sha256:c20d112390b9a9ce8c11b8af95ad35bcaa62f4759663c09c936cd8d2b511f329` | Compose (`minio`) | `image-scan-thirdparty` (`minio`) |
| `postgres:16.16-alpine@sha256:0adbf25a9e96d38823bb813f3ac0ed41fadb665c88f1df7fe6dbfb060cbdc106` | Compose (`keycloak-db`) | `image-scan-thirdparty` (`keycloak-db`) |
| `pgsty/minio:RELEASE.2026-08-04T00-00-00Z@sha256:b6bfe7239bfc83fb90d31612d9704d86039dd714f7904b3f1ad68f211e602372` | Compose (`minio`) | `image-scan-thirdparty` (`minio`) |
| `postgres:16.16-alpine@sha256:0adbf25a9e96d38823bb813f3ac0ed41fadb665c88f1df7fe6dbfb060cbdc106` | Basis von `reticora-keycloak-db` (Compose `keycloak-db`, eigenes Image seit 2026-11-13) | über `image-scan` (`keycloak-db`), `keycloak_db_image_pin` |
| `quay.io/keycloak/keycloak:26.8.2@sha256:723b1d6e254c0fc96d5cffec860c7e0744d6a3277822b865bb538cf891575e41` | Compose (`keycloak`); vom Hersteller gepflegte Major-Version 26 | `image-scan-thirdparty` (`keycloak`) |
| `opensearchproject/opensearch:2.19.7@sha256:3bfa1ec74d56928c2e018033d45c460ce2c0ea65a5cedd577de3102326c31b89` | Compose (`opensearch`), `deploy/k8s/base/components/opensearch/statefulset.yaml` | `image-scan-thirdparty` (`opensearch`) |
| `certbot/certbot:v5.9.0@sha256:bc93cb710b413e6b456d622f9b0133b3e109c0a78c2ed9976af43d209b0a1805` | Compose (`certbot`, TLS-Zertifikate) | `image-scan-thirdparty` (`certbot`) |
| Images von `docker-compose.yml` (Entwicklung) | Dev-Stack | Ausnahme: nur Entwicklung, nicht ausgeliefert; TimescaleDB, MinIO (Silo) und clamd tragen dieselben Pins wie der Betrieb (`timescale_image_refs`, `objectstorage_image_refs`) |

Die Matrix von `image-scan` deckt jedes Image ab, das `release.yml` baut,
signiert und veröffentlicht:

| image-scan | Release-Image | Dockerfile (Kontext) |
|---|---|---|
| `backend` | `reticora-server` | `backend/Dockerfile` (`.`) |
| `collector` | `reticora-collector` | `collector/Dockerfile` (`.`) |
| `frontend` | `reticora-frontend` | `frontend/Dockerfile` (`frontend`) |
| `postgres` | `reticora-postgres` | `deploy/docker-compose/postgres/Dockerfile` (`deploy/docker-compose/postgres`) |
| `keycloak-db` | `reticora-keycloak-db` | `deploy/docker-compose/keycloak-db/Dockerfile` (`deploy/docker-compose/keycloak-db`) |

`tests/ci-config.test.sh` (Job `gate`) prüft, dass jedes Image der
Release-Matrix mit Dockerfile und Kontext in der Scan-Matrix steht; ein neues
Release-Image ohne Scan-Eintrag lässt `gate` scheitern.

Zusätzlich scannt `release.yml` jedes Image vor dem Push: Es wird lokal
gebaut, mit derselben Trivy-Version (per Digest gepinnt) und denselben
Schwellen wie in `image-scan` geprüft und erst danach gepusht und signiert.
Der Push baut aus demselben Cache; ein Schritt vergleicht die Layer des
gepushten mit denen des gescannten Images. Der Bericht liegt als Artefakt
`trivy-<release-image>` am Release-Lauf (90 Tage).

Begründung der Schwellen:

- **Go:** govulncheck bewertet ohne Schweregrad. Es weist aber nach, ob der
  verwundbare Code tatsächlich aufgerufen wird. Erreichbare Funde blockieren.
  Nur importierte Funde erscheinen im Bericht und werden mit dem nächsten
  Abhängigkeits-Update behoben.
- **npm:** Entwicklungswerkzeuge (Build, Tests, Lint) werden nicht
  ausgeliefert. Ihre Funde stehen im Bericht, blockieren aber nicht.
- **Container:** Funde ohne verfügbaren Fix lassen sich nicht innerhalb der SLA
  beheben. Sie stehen im Bericht und blockieren, sobald ein Fix erscheint.

## Ausnahmen und CVE-Fix-SLA

Akzeptierte Funde stehen in `security/scan-exceptions.txt`, je Zeile:

```text
<ID> exp:<JJJJ-MM-TT> since:<JJJJ-MM-TT> sev:<critical|high> # <Begründung, Ticket>
```

- IDs: `GO-…` (govulncheck), `GHSA-…` (npm audit), `CVE-…` (Trivy). Das
  Format ist das `.trivyignore`-Format; Trivy liest die Datei direkt und
  übergeht `since:` und `sev:`.
- `since:` ist der Tag der ersten Meldung des Funds, `sev:` sein Schweregrad
  (für govulncheck-Funde ohne eigenen Schweregrad die Einstufung im Review).
- Jede Ausnahme braucht ein Ablaufdatum innerhalb der SLA, gerechnet ab
  `since:`: **Critical höchstens 7 Tage, High höchstens 30 Tage**.
- Nach Ablauf greift die Ausnahme nicht mehr, der Job schlägt wieder fehl. Eine
  Verlängerung über die SLA hinaus lässt die Prüfung nicht zu; `since:` bleibt
  der Tag der ersten Meldung. Ohne verfügbaren Fix wird die Abhängigkeit
  ersetzt oder entfernt, oder die Abweichung von SEC-11 wird als Entscheidung
  festgehalten. Trivy meldet Funde ohne Fix ohnehin nicht (`ignore-unfixed`).

### SLA-Prüfung

`scripts/ci/check-scan-exceptions.sh` läuft in `govulncheck`, `npm-audit`,
`image-scan`, `image-scan-thirdparty` und im Job `images` von `release.yml`
vor dem jeweiligen Scan.
Der Job scheitert, sobald eine Nicht-Kommentarzeile

- kein `exp:` hat (Trivy würde den Fund sonst unbefristet unterdrücken),
- kein `since:` oder kein `sev:critical`/`sev:high` hat,
- ein `exp:` später als `since:` + 7 Tage (Critical) bzw. + 30 Tage (High)
  oder früher als `since:` hat,
- ein `since:` in der Zukunft hat (das verschöbe das SLA-Fenster),
- ein Feld doppelt oder im Kommentar trägt (Trivy liest das erste `exp:`, die
  Auswertung in `ci.yml` das letzte) oder kein gültiges Datum bzw. keine
  `CVE-`/`GHSA-`/`GO-`-ID enthält.

Abgelaufene Einträge sind gültig; sie unterdrücken nichts und können beim
nächsten Aufräumen entfallen. Ob `since:` dem Tag der ersten Meldung
entspricht, prüft das Review (Job-Log oder Artefakt des ersten roten Laufs).
Die Fälle stehen in `tests/ci-config.test.sh`.

## Ablauf bei einem neuen Fund

1. Job-Log und Artefakt zeigen die ID, das Paket und die Fix-Version.
2. Abhängigkeit aktualisieren (`go get …@<fix>` und `go mod tidy`,
   `npm update`/`npm audit fix`, Basis-Image-Tag) und den PR erneut prüfen.
   Für `reticora-postgres` werden Version und Digest von `TIMESCALE_IMAGE`
   (an allen Stellen, siehe unten) bzw. `PGBACKREST_VERSION` und
   `PGBACKREST_SHA256` gemeinsam erneuert.
3. Ist das innerhalb des PRs nicht möglich, eine Ausnahme mit `since:`,
   `sev:`, SLA-Datum und Ticket eintragen.

## Datenbank-Image `reticora-postgres`

`deploy/docker-compose/postgres/Dockerfile` (WP-100, WP-389, E-96) ist
reproduzierbar und integritätsgeprüft:

- `TIMESCALE_IMAGE` pinnt die Basis per Version und Digest des
  Multi-Arch-Index:
  `timescale/timescaledb:2.31.0-pg16@sha256:322ae364eca3321cf2a48f1ef31213b87843413b3cf8feeebf54e999a26bb731`.
  Dieselbe Referenz nutzen die Datenbanken in `ci.yml`, `load.yml` und
  `restore-test.yml`, der Dev-Stack `docker-compose.yml` und der logische
  Backup-CronJob in `deploy/k8s` (E-96).
- Die Go-Hilfsprogramme des Basisimages tragen Funde ihrer Go-Toolchain. Die
  Stufe `go-tools` baut sie mit der Go-Toolchain des Repositorys neu
  (`GO_IMAGE`, `golang:1.26.11-alpine` per Digest), in der Modulversion, die
  das Basisimage ausliefert; `go install` prüft jedes Modul gegen
  sum.golang.org.
  - `timescaledb-tune` (`TIMESCALEDB_TUNE_VERSION`) stimmt beim ersten Init
    `postgresql.conf` ab (`/docker-entrypoint-initdb.d/001_timescaledb_tune.sh`).
  - `gosu` (`GOSU_VERSION`, Commit des Tags 1.19; gosu hat keine
    Semver-Tags): Der Entrypoint wechselt damit von root zu postgres. Der
    Build prüft, dass `gosu postgres` als postgres läuft.
  - `timescaledb-parallel-copy` nutzen weder der Entrypoint noch die
    Init-Skripte noch Reticora; es wird entfernt.
- `libexpat`, Python 3 und `musl` (CVE-2026-107891, behoben in 1.2.5-r24)
  des Basisimages werden per `apk upgrade` auf den Stand mit Fix gehoben.
- Der pgBackRest-Quell-Tarball wird gegen `PGBACKREST_SHA256` geprüft, bevor
  er entpackt wird; eine andere `PGBACKREST_VERSION` ohne neue Prüfsumme
  bricht den Build ab.

`tests/ci-config.test.sh` prüft das statisch: `db_image_pin` (Dockerfile) und
`timescale_image_refs` (jede TimescaleDB-Referenz des Repositorys außer
Audit-, Plan- und Abnahmeunterlagen ist Version und Digest von
`TIMESCALE_IMAGE`). Eine neue TimescaleDB-Version erneuert Version und Digest
an allen Stellen gemeinsam; liefert das neue Basisimage andere Versionen von
`timescaledb-tune` oder `gosu` aus (`go version -m /usr/local/bin/<binary>`),
werden `TIMESCALEDB_TUNE_VERSION` und `GOSU_VERSION` mitgezogen.

### Umstieg bestehender Datenbanken auf TimescaleDB 2.31.0

Bis zu E-96 baute `reticora-postgres` auf dem gleitenden Tag `latest-pg16`
auf, die CI auf 2.17.2. Geprüft am 2026-10-21 mit einer Datenbank, die mit
2.17.2 angelegt und bis Migration 153 migriert war, und dem gepinnten Image:

- Das Image liefert die Bibliotheken der Erweiterungsversionen 2.13.0 bis
  2.31.0 (`ls /usr/local/lib/postgresql/timescaledb-*.so`). Eine bestehende
  Datenbank behält nach dem Wechsel ihre Erweiterungsversion und bleibt
  nutzbar (geprüft mit 2.17.2: Hypertable, Continuous Aggregate, Policies und
  Migrationsstand unverändert).
- `ALTER EXTENSION timescaledb UPDATE` hebt sie auf 2.31.0. Die Anweisung
  muss die erste einer neuen Sitzung sein (`psql -X`). In der
  Reticora-Datenbank läuft davor der Event-Trigger `reticora_restrict_app_ddl`
  (Migration 000078) und lädt die alte Version; die Sitzung bricht dann mit
  `FATAL: "timescaledb" already loaded with a different version` ab, die
  Version bleibt.
- Das Update führt `scripts/ops/timescaledb-update.sh` aus (WP-604): Es liest
  `extversion` und `default_version`; ist die Erweiterung älter, schaltet es
  den Event-Trigger aus, führt das Update aus und schaltet den Trigger wieder
  ein, jede Anweisung in einer eigenen `psql -X`-Sitzung als
  Bootstrap-Superuser. Solange ein Login der App-Rollen (`reticora_app`,
  `reticora_owner`, ohne SUPERUSER) mit der Datenbank verbunden ist, schaltet
  es den Trigger nicht aus und bricht nach `TSDB_UPDATE_SESSION_WAIT`
  Sekunden (15) ab. Bricht es ab, schaltet ein Trap den Trigger trotzdem
  wieder ein; einen von einem früheren Abbruch ausgeschalteten Trigger
  schaltet es ein. `postgres` und `template1` (ohne den Event-Trigger)
  erhalten nur das Update. Bei aktueller Version ändert es nichts.
  `install-cloud.sh` stoppt bei jedem Lauf den Dienst `server` und ruft das
  Skript zwischen dem Passwortabgleich und den Migrationen auf; bei
  Kubernetes mit externer Datenbank führt es der Betreiber vor dem Rollout
  aus (`deploy/README.md`, Abschnitt 3). Der Server startet nicht und
  `/readyz` meldet `database: unavailable`, solange der Trigger ausgeschaltet
  ist oder fehlt; eine Erweiterung unter `default_version` meldet die
  Startprüfung als Warnung im Log. Manuell (Compose):

  ```sh
  compose="docker compose --env-file deploy/docker-compose/.env -f deploy/docker-compose/docker-compose.yml"
  $compose stop server
  scripts/ops/timescaledb-update.sh -- $compose exec -T postgres psql -U reticora
  $compose up -d server
  ```

  Die Kompressions-Policy heißt danach `Columnstore Policy` statt
  `Compression Policy`; Schema-Baseline-Test und die Metrik-Tests
  (`internal/monitoring`) waren auf der so aktualisierten Datenbank grün.
  `tests/timescaledb-update.test.sh` prüft den Ablauf mit Stubs und, mit
  `TSDB_UPDATE_TEST_DATABASE_URL` (CI-Job `migrations`), gegen eine mit
  2.17.2 angelegte und migrierte Datenbank: danach `default_version`,
  Trigger aktiv, Migrationen vollständig und
  `TestSchemaBaselineMatchesMigratedSchema` grün.
- Eine Datenbank, deren Erweiterung neuer als 2.31.0 ist, ist mit diesem
  Image nicht nutzbar: Der Server startet, aber jede Anweisung in ihr
  scheitert mit `could not access file "$libdir/timescaledb-<Version>"`
  (nachgestellt mit einem Katalogeintrag 2.99.0). Am 2026-10-21 gibt es auf
  Docker Hub keine 2.x-pg16-Version über 2.31.0; `latest-pg16` zeigt auf
  denselben Digest.

### Nachtrag 2026-11-13: PostgreSQL 16.15 in reticora-postgres (E-257)

`image-scan (postgres)` meldet 38 High-CVEs in PostgreSQL 16.15 (behoben in 16.16). Der Server der TimescaleDB-Basis ist ein Quell-Build in `/usr/local`, den Trivy nicht sieht; die gemeldeten apk-Pakete tragen dieselbe Version und stehen deshalb für das Risiko des Servers und der von pgBackRest, `psql` und `pg_dump` geladenen `libpq`. Ein `apk upgrade` würde den Befund verdecken und wird nicht verwendet. Die CVEs stehen befristet bis 2026-12-13 in `security/scan-exceptions.txt`; bis dahin wird auf ein TimescaleDB-Image mit 16.16 gewechselt oder PostgreSQL 16.16 aus dem Quelltext gebaut (Prüfpunkt 2026-12-06).

## Datenbank-Image `reticora-keycloak-db`

`deploy/docker-compose/keycloak-db/Dockerfile` (E-96, A-85, Update-Plan
Upd-01) ist die PostgreSQL-Datenbank von Keycloak (Compose-Dienst
`keycloak-db`). Das offizielle `postgres:16.16-alpine` liefert `gosu` mit
Go 1.24.6 aus und baut es nicht neu; Trivy meldete darin 26 IDs der
Go-Standardbibliothek (CVE-2025-68121 Critical, 25 High). Das eigene Image:

- `POSTGRES_IMAGE` pinnt die Basis per Version und Digest des
  Multi-Arch-Index:
  `postgres:16.16-alpine@sha256:0adbf25a9e96d38823bb813f3ac0ed41fadb665c88f1df7fe6dbfb060cbdc106`.
- Die Stufe `go-tools` baut `gosu` mit der Go-Toolchain des Repositorys neu
  (`GO_IMAGE`, `golang:1.26.11-alpine` per Digest) in `GOSU_VERSION`
  (`v0.0.0-20261025094355-29b939aa5bd4`, Commit des Tags 1.20);
  `go install` prüft das Modul gegen sum.golang.org. Das Binary ersetzt
  `/usr/local/bin/gosu` der Basis, denn `docker-entrypoint.sh` ruft `gosu`
  beim Namen auf (`su-exec` ist dort nur ein Link darauf). Der Build prüft,
  dass `gosu postgres` und `su-exec postgres` als UID 70 laufen.
- Alles andere bleibt das Hersteller-Image: Major 16, `PGDATA`
  `/var/lib/postgresql/data`, Benutzer `postgres` mit UID/GID 70. Ein Volume
  `keycloakdb` von `postgres:16.15-alpine` oder `16.16-alpine` läuft
  unverändert weiter; `tests/keycloak-db-image.test.sh` (install-smoke, Job
  `reinstall`) weist das mit beiden Vorgängern und einem leeren Volume nach.
- `release.yml` baut, scannt, signiert und attestiert es wie die übrigen
  Release-Images; `install-cloud.sh` prüft es im Release-Modus mit
  `cosign verify` und pinnt es als `RETICORA_KEYCLOAK_DB_IMAGE`, der
  Quell-Build baut es über `docker-compose.override.yml`.

Trivy 0.75.0 am 2026-11-13: 0 Critical/High (Hersteller-Image: 1 Critical,
25 High); `govulncheck -mode=binary` auf dem neuen `gosu`: keine Funde.
`tests/ci-config.test.sh` (`keycloak_db_image_pin`) prüft die Pins, die
Toolchain-Gleichheit mit `backend/Dockerfile` und den Ersatz von `gosu`. Ein
neues Hersteller-Release erneuert Version und Digest von `POSTGRES_IMAGE`;
eine neue Repository-Toolchain zieht `GO_IMAGE` mit.
