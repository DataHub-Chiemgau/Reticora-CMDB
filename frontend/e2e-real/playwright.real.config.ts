import { defineConfig, devices } from '@playwright/test';

// Phase-1 acceptance audit (audit/phase1-final): browser flows against a REAL
// running stack – server, PostgreSQL, NATS, Redis, object storage, Keycloak
// and a mail catcher. Nothing is mocked: every assertion travels
// UI → API → database → UI. The stack is not started here; see
// e2e-real/README.md for the endpoints the specs expect.
//
// Run: npx playwright test -c e2e-real/playwright.real.config.ts
// Module run: E2E_MODULE_RUN=maintenance npx playwright test -c e2e-real/playwright.real.config.ts
const chromiumLaunch = process.env.PW_CHROMIUM_EXECUTABLE
  ? { executablePath: process.env.PW_CHROMIUM_EXECUTABLE }
  : {};

// Specs of late modules (phase P2 and later, E-03): their routes answer 404 in the G1
// default, so the G1 run leaves them out. The module run of install-smoke.yml restarts the
// server with RETICORA_MODULES=<key> and sets E2E_MODULE_RUN=<key>; then only the specs of
// that module run (README.md, "Module run", WP-541).
const MODULE_SPECS: Record<string, string> = {
  maintenance: '**/maintenance.spec.ts',
};
const moduleRun = process.env.E2E_MODULE_RUN ?? '';
if (moduleRun && !MODULE_SPECS[moduleRun]) {
  throw new Error(`E2E_MODULE_RUN=${moduleRun}: no specs for this module`);
}

export default defineConfig({
  testDir: '.',
  testMatch: moduleRun ? MODULE_SPECS[moduleRun] : '**/*.spec.ts',
  testIgnore: moduleRun ? [] : Object.values(MODULE_SPECS),
  // The specs share one stack and create their own tenants; run them one
  // after another so the timing assertions are not distorted.
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 5 * 60_000,
  expect: { timeout: 30_000 },
  reporter: [['list']],
  use: {
    ...devices['Desktop Chrome'],
    baseURL: process.env.BASE_URL || 'http://localhost:5173',
    locale: 'de-DE',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    launchOptions: chromiumLaunch,
  },
});
