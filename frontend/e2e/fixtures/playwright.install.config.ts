import { spawnSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { defineConfig, devices } from '@playwright/test';

// The onboarding suite (WP-200, INS-04, ABN-01 d) against a freshly
// installed stack: the install-smoke workflow installs Reticora, runs
// setup-onboarding.sh and then
//   npx playwright test -c e2e/fixtures/playwright.install.config.ts
// BASE_URL is the frontend of the installation; nothing is mocked.
//
// WP-558 (F-352): the job tls-domain-pebble runs the INS-04 tests over the
// TLS origin of its installation, whose certificate Pebble issued. The suite
// trusts that root instead of ignoring certificate errors: E2E_CA_CERT names
// the PEM file, which is imported into the NSS database of the runner's user
// ($HOME/.pki/nssdb, read by Chromium on Linux; certutil of libnss3-tools)
// and set as NODE_EXTRA_CA_CERTS, which the worker processes (request
// contexts) inherit.
const NSS_NICKNAME = 'reticora-e2e-ca';

function trustRootCA(pem: string) {
  if (process.env.NODE_EXTRA_CA_CERTS && process.env.NODE_EXTRA_CA_CERTS !== pem) {
    throw new Error(
      `NODE_EXTRA_CA_CERTS (${process.env.NODE_EXTRA_CA_CERTS}) is not E2E_CA_CERT (${pem})`,
    );
  }
  process.env.NODE_EXTRA_CA_CERTS = pem;
  const db = join(homedir(), '.pki', 'nssdb');
  const certutil = (...args: string[]) =>
    spawnSync('certutil', ['-d', `sql:${db}`, ...args], { encoding: 'utf8' });
  if (certutil('-L', '-n', NSS_NICKNAME).status === 0) {
    return;
  }
  mkdirSync(db, { recursive: true });
  if (certutil('-L').status !== 0) {
    const created = certutil('-N', '--empty-password');
    if (created.status !== 0) {
      throw new Error(`certutil -N (${db}): ${created.error ?? created.stderr}`);
    }
  }
  const added = certutil('-A', '-t', 'C,,', '-n', NSS_NICKNAME, '-i', pem);
  if (added.status !== 0) {
    throw new Error(`certutil -A ${pem} (${db}): ${added.error ?? added.stderr}`);
  }
}

if (process.env.E2E_CA_CERT) {
  trustRootCA(process.env.E2E_CA_CERT);
}

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
    // Certificate errors fail the suite (WP-558); a private CA is trusted
    // over E2E_CA_CERT.
    ignoreHTTPSErrors: false,
    locale: 'de-DE',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
});
