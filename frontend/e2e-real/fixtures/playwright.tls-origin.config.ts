import { defineConfig } from '@playwright/test';
import realSuite from '../playwright.real.config';

// WP-646 (F-375): the check of the TLS origin before the real-stack suite in
// the WebKit projects of install-smoke.yml (job e2e-real, projects webkit and
// mobile-safari). Same projects and settings as playwright.real.config.ts,
// only tls-origin.check.ts: BASE_URL is https:// and not localhost, and the
// sign-in sets the Secure refresh cookie reticora_refresh. The Pebble root of
// the installation is trusted through the system trust store and
// NODE_EXTRA_CA_CERTS (setup-tls-origin.sh); certificate errors are not
// ignored.
//
// Run: npx playwright test -c e2e-real/fixtures/playwright.tls-origin.config.ts --project=webkit
export default defineConfig({
  ...realSuite,
  testDir: '.',
  testMatch: 'tls-origin.check.ts',
  testIgnore: [],
  use: { ...realSuite.use, ignoreHTTPSErrors: false },
});
