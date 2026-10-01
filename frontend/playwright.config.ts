import { defineConfig, devices } from '@playwright/test';

// Lets local runs use a preinstalled Chromium build for the Chromium projects.
const chromiumLaunch = process.env.PW_CHROMIUM_EXECUTABLE
  ? { executablePath: process.env.PW_CHROMIUM_EXECUTABLE }
  : {};

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? 'github' : 'html',
  use: {
    baseURL: process.env.BASE_URL || 'http://localhost:5173',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  // NFR-07/UI-18 (WP-193): desktop Chrome, Edge, Firefox and Safari (WebKit)
  // plus mobile Chrome and Safari. Playwright bundles one current build per
  // engine, so the "previous version" of UI-18 is not covered here.
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
