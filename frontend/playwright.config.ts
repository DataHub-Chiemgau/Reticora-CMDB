import { defineConfig, devices } from '@playwright/test';

// Lets local runs use a preinstalled Chromium build for the Chromium projects.
const chromiumLaunch = process.env.PW_CHROMIUM_EXECUTABLE
  ? { executablePath: process.env.PW_CHROMIUM_EXECUTABLE }
  : {};

export default defineConfig({
  testDir: './e2e',
  // The onboarding suite runs against a freshly installed stack only
  // (e2e/fixtures/playwright.install.config.ts, install-smoke workflow).
  testIgnore: ['onboarding.spec.ts'],
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  // CI keeps an HTML report next to the annotations: the e2e job uploads it
  // per project as playwright-report-<project>, also on success (WP-531).
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'html',
  use: {
    baseURL: process.env.BASE_URL || 'http://localhost:5173',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  // NFR-07/UI-18 (WP-193, WP-531): the current versions of desktop Chrome,
  // Edge, Firefox and Safari (WebKit) plus mobile Chrome and Safari; the CI
  // job e2e runs every project with its own report artifact. Playwright
  // bundles one build per engine, so the previous versions run in the job
  // e2e-prev (.github/workflows/e2e-prev.yml, E-88): e2e-prev/ pins the
  // previous minor of @playwright/test (Chromium, Firefox, WebKit n-1) and
  // adds Chrome n-1 from Chrome for Testing. Limitation: Edge n-1 and
  // Safari n-1 (desktop and iOS) are approximated by their engines there,
  // and mobile-safari here is WebKit with an iPhone profile, not iOS Safari
  // (docs/README.md, Browser-Unterstützung).
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        launchOptions: chromiumLaunch,
      },
    },
    {
      name: 'edge',
      use: { ...devices['Desktop Edge'], channel: 'msedge' },
    },
    {
      name: 'firefox',
      use: { ...devices['Desktop Firefox'] },
    },
    {
      name: 'webkit',
      use: { ...devices['Desktop Safari'] },
    },
    {
      name: 'mobile-chrome',
      use: { ...devices['Pixel 7'], launchOptions: chromiumLaunch },
    },
    {
      name: 'mobile-safari',
      use: { ...devices['iPhone 14'] },
    },
  ],
  webServer: process.env.CI
    ? undefined
    : {
        command: 'npm run dev',
        url: 'http://localhost:5173',
        reuseExistingServer: true,
      },
});
