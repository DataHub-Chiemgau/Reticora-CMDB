import { defineConfig, devices } from '@playwright/test';

// The onboarding suite (WP-200, INS-04, ABN-01 d) against a freshly
// installed stack: the install-smoke workflow installs Reticora, runs
// setup-onboarding.sh and then
//   npx playwright test -c e2e/fixtures/playwright.install.config.ts
// BASE_URL is the frontend of the installation; nothing is mocked.
export default defineConfig({
  testDir: '..',
  testMatch: 'onboarding.spec.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 10 * 60_000,
  expect: { timeout: 30_000 },
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  use: {
    ...devices['Desktop Chrome'],
    baseURL: process.env.BASE_URL || 'http://localhost:3000',
    locale: 'de-DE',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
});
