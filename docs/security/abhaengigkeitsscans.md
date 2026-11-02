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
| `image-scan` (Matrix `backend`, `collector`, `frontend`, `postgres`) | gebautes Container-Image: OS-Pakete und Anwendungsabhängigkeiten (Trivy) | Critical/High mit verfügbarem Fix (`ignore-unfixed`) | `trivy-<image>` (JSON) |

Die Matrix von `image-scan` deckt jedes Image ab, das `release.yml` baut,
signiert und veröffentlicht:

| image-scan | Release-Image | Dockerfile (Kontext) |
|---|---|---|
| `backend` | `reticora-server` | `backend/Dockerfile` (`.`) |
| `collector` | `reticora-collector` | `collector/Dockerfile` (`.`) |
| `frontend` | `reticora-frontend` | `frontend/Dockerfile` (`frontend`) |
| `postgres` | `reticora-postgres` | `deploy/docker-compose/postgres/Dockerfile` (`deploy/docker-compose/postgres`) |

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
`image-scan` und im Job `images` von `release.yml` vor dem jeweiligen Scan.
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
  (`GO_IMAGE`, `golang:1.26.10-alpine` per Digest), in der Modulversion, die
  das Basisimage ausliefert; `go install` prüft jedes Modul gegen
  sum.golang.org.
  - `timescaledb-tune` (`TIMESCALEDB_TUNE_VERSION`) stimmt beim ersten Init
    `postgresql.conf` ab (`/docker-entrypoint-initdb.d/001_timescaledb_tune.sh`).
  - `gosu` (`GOSU_VERSION`, Commit des Tags 1.19; gosu hat keine
    Semver-Tags): Der Entrypoint wechselt damit von root zu postgres. Der
    Build prüft, dass `gosu postgres` als postgres läuft.
  - `timescaledb-parallel-copy` nutzen weder der Entrypoint noch die
    Init-Skripte noch Reticora; es wird entfernt.
- `libexpat` des Basisimages wird per `apk upgrade` auf den Stand mit Fix
  gehoben.
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
