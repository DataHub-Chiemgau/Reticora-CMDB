# Real-stack browser tests (Phase-1 acceptance audit)

These specs were added by the Phase-1 final acceptance audit (`audit/phase1-final/`). Unlike `e2e/*.spec.ts`,
which run against a mocked backend (`e2e/support/backend.ts`), nothing here is mocked: every flow goes
UI → API → PostgreSQL → UI on a running stack. They are not part of the CI job `e2e`.

## Expected stack

| Variable                                             | Default                 | Component                                                                      |
| ---------------------------------------------------- | ----------------------- | ------------------------------------------------------------------------------ |
| `BASE_URL`                                           | `http://localhost:5173` | frontend (production build) that proxies `/api` and `/bff` to the server       |
| `E2E_SERVER_URL`                                     | `http://localhost:8080` | Reticora server (`RETICORA_ENVIRONMENT=production`)                            |
| `E2E_KEYCLOAK_URL`                                   | `http://localhost:8180` | Keycloak with the realm `reticora` (`deploy/keycloak/realm-reticora-dev.json`) |
| `E2E_KEYCLOAK_ADMIN` / `E2E_KEYCLOAK_ADMIN_PASSWORD` | `admin` / `admin`       | Keycloak master administrator                                                  |
| `E2E_MAILPIT_URL`                                    | `http://localhost:8025` | mail catcher receiving the signup and invitation mails (server SMTP)           |
| `E2E_COLLECTOR_BIN`                                  | –                       | collector binary (`collector/cmd/collector`) for discovery flows               |

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

A failing spec is a product finding, not a flaky test: the assertions encode the requirement (WP and requirement IDs
in the header of each spec) and must not be relaxed to match observed behaviour. Open findings are listed in
`audit/phase1-final/PHASE1_FINDINGS.md`.
