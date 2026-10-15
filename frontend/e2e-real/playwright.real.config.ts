import { defineConfig, devices } from '@playwright/test';

// Phase-1 acceptance audit (audit/phase1-final): browser flows against a REAL
// running stack – server, PostgreSQL, NATS, Redis, object storage, Keycloak
// and a mail catcher. Nothing is mocked: every assertion travels
// UI → API → database → UI. The stack is not started here; see
// e2e-real/README.md for the endpoints the specs expect.
//
// Run: npx playwright test -c e2e-real/playwright.real.config.ts
const chromiumLaunch = process.env.PW_CHROMIUM_EXECUTABLE
  ? { executablePath: process.env.PW_CHROMIUM_EXECUTABLE }
  : {};

export default defineConfig({
  testDir: '.',
  testMatch: '**/*.spec.ts',
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
