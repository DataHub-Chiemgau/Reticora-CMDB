# Real-stack browser tests (Phase-1 acceptance audit)

These specs were added by the Phase-1 final acceptance audit (`audit/phase1-final/`). Unlike `e2e/*.spec.ts`,
which run against a mocked backend (`e2e/support/backend.ts`), nothing here is mocked: every flow goes
UI → API → PostgreSQL → UI on a running stack. They are not part of the CI job `e2e`; `install-smoke.yml` runs them against the installed stack (below).

## Expected stack

| Variable                                             | Default                                            | Component                                                                                                                                |
| ---------------------------------------------------- | -------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `BASE_URL`                                           | `http://localhost:5173`                            | frontend (production build) that proxies `/api` and `/bff` to the server                                                                 |
| `E2E_SERVER_URL`                                     | `http://localhost:8080`                            | Reticora server (`RETICORA_ENVIRONMENT=production`)                                                                                      |
| `E2E_KEYCLOAK_URL`                                   | `http://localhost:8180`                            | Keycloak with the realm `reticora` (`deploy/keycloak/realm-reticora-dev.json`)                                                           |
| `E2E_KEYCLOAK_ADMIN` / `E2E_KEYCLOAK_ADMIN_PASSWORD` | `admin` / `admin`                                  | Keycloak master administrator                                                                                                            |
| `E2E_MAILPIT_URL`                                    | `http://localhost:8025`                            | mail catcher receiving the signup and invitation mails (server SMTP)                                                                     |
| `E2E_COLLECTOR_BIN`                                  | –                                                  | collector binary (`collector/cmd/collector`) for discovery flows                                                                         |
| `E2E_FAKESNMP_BIN`                                   | built from `collector/cmd/fakesnmp`                | SNMP fake of `onboarding-bundle.spec.ts` and `discovery-errors.spec.ts` (below); without it `support/stack.ts` builds it with `go build` |
| `E2E_DATABASE_URL`                                   | `postgres://reticora:reticora@localhost:55432/e2e` | PostgreSQL of the stack (owner role), read with `psql` by `support/roles-authz.ts` and `support/locations-racks.ts`                      |
| `E2E_SEED_BIN`                                       | built from `backend/cmd/seed`                      | SIM-01 seed of `demo-seed.spec.ts` into `E2E_DATABASE_URL` (below); `support/stack.ts` builds it otherwise                               |
| `E2E_OPERATOR_TOKEN`                                 | –                                                  | break-glass token `RETICORA_OPERATOR_TOKEN` for `entitlement-expiry.spec.ts`; unset: personal operator login                             |

The server needs self-signup enabled (`RETICORA_SIGNUP_VERIFY_URL`, `RETICORA_SIGNUP_CAPTCHA_KEY`, Keycloak admin
client, SMTP) exactly as `e2e/fixtures/setup-onboarding.sh` configures it. Every spec creates its own tenants through
the self-signup UI (`support/stack.ts`, `signupTenant`), so specs do not depend on each other or on seed data; only
`demo-seed.spec.ts` runs the SIM-01 seed itself (below).

Invitations need `RETICORA_INVITATION_ACCEPT_URL`, the page the link of the invitation mail opens (WP-421, NTF-05);
without it `POST /api/v1/invitations` and `/invitations/{id}/renew` answer 503 and `roles-authz.spec.ts` fails. The
installed stack derives it from `RETICORA_PUBLIC_BASE_URL` (`deploy/docker-compose/docker-compose.yml`), the onboarding
overlay `e2e/fixtures/docker-compose.e2e.yml` sets it to `${E2E_BASE_URL:-http://localhost:3000}/invitations/accept`.
Issue and renewal mail the link to the invitee over the tenant's e-mail channel (`roles-authz.spec.ts` test 05 reads it
from the mail catcher).

## Client- and site-scoped users (`client-scope.spec.ts`, WP-535)

`tenant-isolation.spec.ts` and `roles-authz.spec.ts` sign in organization-wide principals. `client-scope.spec.ts`
signs in users whose only role assignment lies in one client or one site. `scopedUser` in `support/stack.ts` creates
such a user through the API: an invitation as viewer of a client (an invitation carries a standard role, RBA-02),
the acceptance (the server creates the Keycloak user), the role assignment with `POST /api/v1/role-assignments`
`{user_id, role_id, scope_type: client|site, scope_id}` and the deletion of the viewer assignment of the invitation;
`GET /role-assignments` must then list exactly the new assignment. The spec signs in a client_technician of client C1
(CS01–CS03: CMDB list, CI URL, search and topology of the other client C2, the relationship form with the id of the C2
CI, an export and the job center of a client_technician of C2) and a viewer of site S1 (CS04: location tree, racks
page and `GET /racks/{id}`, `/layout` of the rack of site S2). Database probes (`dbRows` of
`support/locations-racks.ts`, `E2E_DATABASE_URL`) show that the hidden rows exist and that no edge was stored. It runs
in the G1 run of `install-smoke.yml` like every spec without a module.

## Real collector and SNMP fake (`onboarding-bundle.spec.ts`, WP-384)

`discovery-override.spec.ts` runs the collector with the simulated fleet (`RETICORA_SIMULATE=true`).
`onboarding-bundle.spec.ts` runs the same binary as installed (`RETICORA_SIMULATE` unset,
`RETICORA_ENVIRONMENT=production`): it reports the subnets of the host, the spec confirms one suggestion in the
Discovery UI, and the collector fetches its scopes and its encrypted credential bundle over the NATS edge. The device
it discovers is the SNMPv2c fake `collector/cmd/fakesnmp` (package `collector/internal/fakesnmp`, test
infrastructure, not part of the release build `./cmd/collector`). `support/stack.ts` (`startFakeSnmp`) builds it with
the Go toolchain of the host (or takes `E2E_FAKESNMP_BIN`) and starts it on `127.0.0.161:161`: it answers sysDescr,
sysObjectID and sysName only to the community of one credential and records the community of every request it
receives, so the spec proves that the collector sent exactly the bundle credential of the scope and no other secret.
The collector's SNMP plugin queries port 161, so the user running Playwright must be allowed to bind it:
`net.ipv4.ip_unprivileged_port_start` at most 161. `fixtures/setup-real-stack.sh` sets it to 0 on GitHub Actions
and fails locally while it is higher (`sudo sysctl -w net.ipv4.ip_unprivileged_port_start=0`). The suggested subnet is
a real network of the host; the spec stops the collector before its first scope pass and disables that scope before
it starts the collector again, so the host's networks are not scanned.

`discovery-errors.spec.ts` (WP-542, DIS-08) runs the same installed collector against two host scopes: the fake in
its SNMPv3 mode on `127.0.0.163:161` (`startFakeSnmp` with `v3`: one authPriv user; a wrong key gets the report
usmStatsWrongDigests) with an snmp_v3 credential carrying wrong keys, and the unreachable `192.0.2.10` (TEST-NET-1).
It checks the cause (`auth_failed`, `unreachable`), protocol and target in `last_result` and on the Discovery page.

## Master key rotation (`key-rotation.spec.ts`, WP-547)

`key-rotation.spec.ts` rotates the master key of the installed stack as `docs/ops/workers.md` describes it (SEC-06):
the new key in `RETICORA_MASTER_KEY`, the old one in `RETICORA_MASTER_KEY_PREVIOUS`, restart the server, let
`rotation.master_key` run, remove the old key, restart again. `restartServer` in `support/stack.ts` changes the `.env`
of the installation (`E2E_COMPOSE_DIR`, default `deploy/docker-compose` of the checkout; `E2E_COMPOSE_ENV_FILE`, default
`.env`) and recreates only the server with the compose files of `fixtures/setup-real-stack.sh` (`docker compose up -d
--no-deps --force-recreate server`; the signup secrets of the overlays come from the running server, the nginx.conf
from `E2E_NGINX_CONF` or `$RUNNER_TEMP/nginx-e2e-real.conf`); the frontend is recreated after a server that came up.
The rewrap run is made due through `scheduler_run` (`E2E_DATABASE_URL`). The spec needs `docker` and write access to
the `.env`; after each test the server runs again with the last keys it started with.

## SIM-01 demo seed (`demo-seed.spec.ts`, WP-433)

`support/stack.ts` (`seedDemo`) runs the seed `backend/cmd/seed` against `E2E_DATABASE_URL` (built with the Go
toolchain of the host, or `E2E_SEED_BIN`): the organizations demo and acme with 40 CIs each. The seed is
deterministic and inserts only what is missing, so the spec can run again on the same stack. It signs in as
`admin@demo.local`, a user of `deploy/keycloak/realm-reticora-dev.json`. Keycloak imports a realm only when it does
not exist yet, and an installed stack imports the production realm without users; then `devRealmAccount` creates
the user through the Keycloak admin API (`E2E_KEYCLOAK_ADMIN`) with the attributes, groups, realm roles and password
of the JSON and logs that. The user holds `org_admin` and sets up its OTP at the first login.

## Run

```sh
cd frontend
npx playwright test -c e2e-real/playwright.real.config.ts --project=chromium          # all specs of the G1 run
npx playwright test -c e2e-real/playwright.real.config.ts --project=chromium tenant   # one file
```

The config defines the six browser projects of `frontend/playwright.config.ts` (`chromium`, `edge`, `firefox`,
`webkit`, `mobile-chrome`, `mobile-safari`; it imports them, so both suites run the same matrix, WP-532) with
`locale: de-DE`. Without `--project` every project runs one after the other on the same stack. Set
`PW_CHROMIUM_EXECUTABLE` to use a preinstalled Chromium build for `chromium` and `mobile-chrome`; the other projects
need their browser (`npx playwright install --with-deps <browser>`, `msedge` for `edge`).

## CI (WP-338, WP-532)

`install-smoke.yml` runs the suite on the reference platform (ubuntu-24.04) against the stack the cloud installer
has just installed, as the matrix job `e2e-real` with one job per browser project (`--project=<project>`, the
browser from `npx playwright install --with-deps`) and its own installation. `frontend/e2e/fixtures/setup-onboarding.sh` prepares self-signup, the mail catcher and the
collector; `fixtures/setup-real-stack.sh` then applies `fixtures/docker-compose.e2e-real.yml` (an nginx.conf that
passes a client `X-Real-IP` through, so that each tenant of a spec signs up from its own address; raised rate
limits like the local audit stack), exports `E2E_DATABASE_URL` and the Keycloak administrator and lets the SNMP fake
bind UDP 161 (above). Presigned export URLs name the endpoint the server uses (`minio:9000`), so the host must resolve
`minio` to the published port `127.0.0.1:9000`; the script adds that `/etc/hosts` entry on GitHub Actions and fails
locally without it. The Playwright JSON report is compared with the `e2e-real` lines of
`tests/audit/expected-failures.txt` (`tests/audit/run-auditdefect.sh`, see `tests/audit/README.md`): a listed test
that turned green, an unlisted red test and a skipped or missing test fail the job. The lines name the project
(`<project>:frontend/e2e-real/<spec file>`, from `projectName` of the report), and each job compares only the lines of
its project (`compare --projects <project>`): a test that is red in firefox but listed only for chromium fails the
firefox job. Report, traces and the comparison are uploaded per project as artifact `e2e-real-<project>`, the result
lines as `e2e-real-results-<project>`.

The job `e2e-real-ratchet` runs after the matrix: every project of the config must have delivered results, every
project lists the same tests (`--list`), no spec or support file decides by browser (`browserName`, `isMobile`,
`projectName`, …; no test is skipped per browser), and the results of all projects together are compared with every
`e2e-real` line, so a line for a project that did not run is `verwaist`. A deviation that only one browser shows is
fixed or listed as a finding of its own with the project in `ort`.

### WebKit over a TLS origin (WP-646, F-375)

WebKit does not store the `Secure` refresh cookie `reticora_refresh` over `http://localhost` (Chromium and Firefox
do), so the jobs of the projects `webkit` and `mobile-safari` (matrix `origin: tls`) do not use the loopback
installation. `fixtures/setup-tls-origin.sh` installs with `RETICORA_TLS_DOMAIN` on `https://cmdb.<runner IP with
dashes>.sslip.io` with a certificate of Pebble (the helpers of `tests/install-acme-pebble.test.sh`, as in the job
`tls-domain-pebble`), puts the Pebble root into the system trust store of the runner (`update-ca-certificates`;
Playwright's WebKit on Linux reads `/etc/ssl/certs/ca-certificates.crt`) and sets `NODE_EXTRA_CA_CERTS` for the
request contexts; no `ignoreHTTPSErrors`. `setup-onboarding.sh` then sets `BASE_URL` and the issuer to that origin,
and `setup-real-stack.sh` also rewrites the TLS template of the installation (server block on 443) with the
`X-Real-IP` passthrough (`fixtures/docker-compose.e2e-real-tls.yml`). Before the suite the job checks that `BASE_URL`
starts with `https://` without localhost (`setup-tls-origin.sh --check-base-url`) and runs
`fixtures/tls-origin.check.ts` (`fixtures/playwright.tls-origin.config.ts`): the sign-in in the project must set the
`Secure`, `HttpOnly` cookie `reticora_refresh`, else the job stops there. The other four projects stay on
`http://localhost:3000`. `tests/e2e-real-tls-origin.test.sh` tests the scripts with stubs (job
`installer-script-tests`).

Locally, after the two setup scripts (or against a hand-built stack with the variables above):

```sh
cd frontend
PLAYWRIGHT_JSON_OUTPUT_NAME=/tmp/e2e-real.json npx playwright test -c e2e-real/playwright.real.config.ts --project=chromium --reporter=list,json
../tests/audit/run-auditdefect.sh playwright --report /tmp/e2e-real.json --results /tmp/e2e-real.txt
../tests/audit/run-auditdefect.sh compare --kinds e2e-real --projects chromium --results /tmp/e2e-real.txt
```

## Module run (`maintenance.spec.ts`, WP-541)

The installed stack runs in the G1 default: the late modules (phase P2 and later, E-03, WP-214) are off and their
routes answer 404. The specs of such a module therefore do not belong to the G1 run: `playwright.real.config.ts` lists
them in `MODULE_SPECS` and leaves them out unless `E2E_MODULE_RUN` names the module; with `E2E_MODULE_RUN=<key>` only
the specs of that module run. All other specs run unchanged against the G1 default. Today this is
`maintenance.spec.ts` (module `maintenance`, MGT-04, NTF-05, NFR-04): a window planned in the maintenance page for the
CIs of two clients, „Kunden benachrichtigen“, the stored notifications after a reload ('ausstehend' per client), the
`maintenance_notification` rows in the database (`dbRows` of `support/locations-racks.ts`: two rows, status pending,
`sent_at` NULL; a second click adds none), and a principal of a second organization that gets 404 for the window and
does not find it in the list. The spec checks first that `GET /api/v1/modules` reports the module enabled
(`expectModuleServed` in `support/stack.ts`); against a G1 default stack it fails there, it never skips.

`install-smoke.yml` runs it after the G1 run on the same installation, in every job of the matrix `e2e-real`: the
step "Restart the server with RETICORA_MODULES=maintenance" applies a one-line overlay (`RETICORA_MODULES: maintenance`
for the server) on top of the files `fixtures/setup-real-stack.sh` applies, recreates the frontend after the server
and checks the variable in the running container; the next step runs the suite with `E2E_MODULE_RUN=maintenance` in
the project of the job (report `e2e-real-maintenance.json`, traces under `maintenance-test-results/` of the artifact
`e2e-real-<project>`). The ratchet compares the results of both runs together with the `e2e-real` lines of that
project in `tests/audit/expected-failures.txt`, so the module run follows the same rules as the G1 run; the step also
fails when the module run executed no test of `maintenance.spec.ts` in the project or another spec or project.

Locally, against a server started with `RETICORA_MODULES=maintenance`:

```sh
cd frontend
E2E_MODULE_RUN=maintenance npx playwright test -c e2e-real/playwright.real.config.ts --project=chromium
```

A failing spec is a product finding, not a flaky test: the assertions encode the requirement (WP and requirement IDs
in the header of each spec) and must not be relaxed to match observed behaviour. Open findings are listed in
`audit/phase1-final/PHASE1_FINDINGS.md`.
