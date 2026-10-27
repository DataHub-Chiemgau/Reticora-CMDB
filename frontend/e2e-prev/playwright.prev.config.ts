import { defineConfig, devices } from '@playwright/test';

// UI-18/NFR-07 (WP-531, E-88 option 1): the previous browser versions. This
// directory pins the previous minor of @playwright/test (package.json,
// package-lock.json), whose bundled Chromium, Firefox and WebKit are one
// release older than those of frontend/package.json, and the workflow
// e2e-prev (.github/workflows/e2e-prev.yml) adds Chrome n-1 from Chrome for
// Testing through PW_CHROME_PREV_EXECUTABLE. Edge n-1 is approximated by the
// Chromium engine, Safari n-1 (desktop and iOS) by WebKit: a documented
// limitation (docs/README.md, Browser-Unterstützung).
//
// The specs are those of frontend/e2e (smoke, a11y, keyboard and the core
// flows with the strict, contract-checked mock backend); nothing is copied.
// Their imports (@playwright/test, @axe-core/playwright, ajv, ajv-formats,
// yaml) resolve from e2e-prev/node_modules through tsconfig.json here, so the
// specs run on the pinned runner even when frontend/node_modules holds the
// current one.
//
// Run from frontend/e2e-prev against a served build:
//   CI=1 BASE_URL=http://localhost:4173 npx playwright test --config playwright.prev.config.ts --project=<name>

// Lets local runs use a preinstalled Chromium build for the Chromium
// projects, as in frontend/playwright.config.ts. Such a run checks the
// configuration only; it is not the n-1 engine.
const chromiumLaunch = process.env.PW_CHROMIUM_EXECUTABLE
  ? { executablePath: process.env.PW_CHROMIUM_EXECUTABLE }
  : {};

// Chrome n-1 from Chrome for Testing. The project exists only when the
// executable is given, so a run that asks for it without one fails with
// "Project(s) "chrome-prev" not found" instead of falling back to the
// bundled Chromium.
const chromePrev = process.env.PW_CHROME_PREV_EXECUTABLE
  ? [
      {
        name: 'chrome-prev',
        use: {
          ...devices['Desktop Chrome'],
          launchOptions: { executablePath: process.env.PW_CHROME_PREV_EXECUTABLE },
        },
      },
    ]
  : [];

export default defineConfig({
  testDir: '../e2e',
  // As in frontend/playwright.config.ts: the onboarding suite needs an
  // installed stack.
  testIgnore: ['onboarding.spec.ts'],
  tsconfig: './tsconfig.json',
  outputDir: '../test-results/prev',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI
    ? [['github'], ['html', { open: 'never', outputFolder: '../playwright-report/prev' }]]
    : [['html', { open: 'never', outputFolder: '../playwright-report/prev' }]],
  use: {
    baseURL: process.env.BASE_URL || 'http://localhost:4173',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'], launchOptions: chromiumLaunch },
    },
    ...chromePrev,
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
});
