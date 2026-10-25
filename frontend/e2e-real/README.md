# Real-stack browser tests (Phase-1 acceptance audit)

These specs were added by the Phase-1 final acceptance audit (`audit/phase1-final/`). Unlike `e2e/*.spec.ts`,
which run against a mocked backend (`e2e/support/backend.ts`), nothing here is mocked: every flow goes
UI → API → PostgreSQL → UI on a running stack. They are not part of the CI job `e2e`; `install-smoke.yml` runs them against the installed stack (below).

## Expected stack

| Variable                                             | Default                                            | Component                                                                                                           |
| ---------------------------------------------------- | -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `BASE_URL`                                           | `http://localhost:5173`                            | frontend (production build) that proxies `/api` and `/bff` to the server                                            |
| `E2E_SERVER_URL`                                     | `http://localhost:8080`                            | Reticora server (`RETICORA_ENVIRONMENT=production`)                                                                 |
| `E2E_KEYCLOAK_URL`                                   | `http://localhost:8180`                            | Keycloak with the realm `reticora` (`deploy/keycloak/realm-reticora-dev.json`)                                      |
| `E2E_KEYCLOAK_ADMIN` / `E2E_KEYCLOAK_ADMIN_PASSWORD` | `admin` / `admin`                                  | Keycloak master administrator                                                                                       |
| `E2E_MAILPIT_URL`                                    | `http://localhost:8025`                            | mail catcher receiving the signup and invitation mails (server SMTP)                                                |
| `E2E_COLLECTOR_BIN`                                  | –                                                  | collector binary (`collector/cmd/collector`) for discovery flows                                                    |
| `E2E_FAKESNMP_BIN`                                   | built from `collector/cmd/fakesnmp`                | SNMPv2c fake of `onboarding-bundle.spec.ts` (below); without it `support/stack.ts` builds it with `go build`        |
| `E2E_DATABASE_URL`                                   | `postgres://reticora:reticora@localhost:55432/e2e` | PostgreSQL of the stack (owner role), read with `psql` by `support/roles-authz.ts` and `support/locations-racks.ts` |

The server needs self-signup enabled (`RETICORA_SIGNUP_VERIFY_URL`, `RETICORA_SIGNUP_CAPTCHA_KEY`, Keycloak admin
client, SMTP) exactly as `e2e/fixtures/setup-onboarding.sh` configures it. Every spec creates its own tenants through
the self-signup UI (`support/stack.ts`, `signupTenant`), so specs do not depend on each other or on seed data.

Invitations need `RETICORA_INVITATION_ACCEPT_URL`, the page the link of the invitation mail opens (WP-421, NTF-05);
without it `POST /api/v1/invitations` and `/invitations/{id}/renew` answer 503 and `roles-authz.spec.ts` fails. The
installed stack derives it from `RETICORA_PUBLIC_BASE_URL` (`deploy/docker-compose/docker-compose.yml`), the onboarding
overlay `e2e/fixtures/docker-compose.e2e.yml` sets it to `${E2E_BASE_URL:-http://localhost:3000}/invitations/accept`.
Issue and renewal mail the link to the invitee over the tenant's e-mail channel (`roles-authz.spec.ts` test 05 reads it
from the mail catcher).

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

## Run

```sh
cd frontend
npx playwright test -c e2e-real/playwright.real.config.ts            # all specs
npx playwright test -c e2e-real/playwright.real.config.ts tenant      # one file
```

Set `PW_CHROMIUM_EXECUTABLE` to use a preinstalled Chromium build.

## CI (WP-338)

`install-smoke.yml` runs the suite on the reference platform (ubuntu-24.04) against the stack the cloud installer
has just installed. `frontend/e2e/fixtures/setup-onboarding.sh` prepares self-signup, the mail catcher and the
collector; `fixtures/setup-real-stack.sh` then applies `fixtures/docker-compose.e2e-real.yml` (an nginx.conf that
passes a client `X-Real-IP` through, so that each tenant of a spec signs up from its own address; raised rate
limits like the local audit stack), exports `E2E_DATABASE_URL` and the Keycloak administrator and lets the SNMP fake
bind UDP 161 (above). Presigned export URLs name the endpoint the server uses (`minio:9000`), so the host must resolve
`minio` to the published port `127.0.0.1:9000`; the script adds that `/etc/hosts` entry on GitHub Actions and fails
locally without it. The Playwright JSON report is compared with the `e2e-real` lines of
`tests/audit/expected-failures.txt` (`tests/audit/run-auditdefect.sh`, see `tests/audit/README.md`): a listed test
that turned green, an unlisted red test and a skipped or missing test fail the job. Report, traces and the comparison
are uploaded as artifact `e2e-real`.

Locally, after the two setup scripts (or against a hand-built stack with the variables above):

```sh
cd frontend
PLAYWRIGHT_JSON_OUTPUT_NAME=/tmp/e2e-real.json npx playwright test -c e2e-real/playwright.real.config.ts --reporter=list,json
../tests/audit/run-auditdefect.sh playwright --report /tmp/e2e-real.json --results /tmp/e2e-real.txt
../tests/audit/run-auditdefect.sh compare --kinds e2e-real --results /tmp/e2e-real.txt
```

A failing spec is a product finding, not a flaky test: the assertions encode the requirement (WP and requirement IDs
in the header of each spec) and must not be relaxed to match observed behaviour. Open findings are listed in
`audit/phase1-final/PHASE1_FINDINGS.md`.
