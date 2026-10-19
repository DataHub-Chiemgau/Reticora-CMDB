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
| `E2E_DATABASE_URL`                                   | `postgres://reticora:reticora@localhost:55432/e2e` | PostgreSQL of the stack (owner role), read with `psql` by `support/roles-authz.ts` and `support/locations-racks.ts` |

The server needs self-signup enabled (`RETICORA_SIGNUP_VERIFY_URL`, `RETICORA_SIGNUP_CAPTCHA_KEY`, Keycloak admin
client, SMTP) exactly as `e2e/fixtures/setup-onboarding.sh` configures it. Every spec creates its own tenants through
the self-signup UI (`support/stack.ts`, `signupTenant`), so specs do not depend on each other or on seed data.

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
limits like the local audit stack) and exports `E2E_DATABASE_URL` and the Keycloak administrator. Presigned export
URLs name the endpoint the server uses (`minio:9000`), so the host must resolve `minio` to the published port
`127.0.0.1:9000`; the script adds that `/etc/hosts` entry on GitHub Actions and fails locally without it. The Playwright
JSON report is compared with the `e2e-real` lines of `tests/audit/expected-failures.txt`
(`tests/audit/run-auditdefect.sh`, see `tests/audit/README.md`): a listed test that turned green, an unlisted red
test and a skipped or missing test fail the job. Report, traces and the comparison are uploaded as artifact
`e2e-real`.

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
