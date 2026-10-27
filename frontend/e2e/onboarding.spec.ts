import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { keycloakOtp } from './support/totp';

// WP-200 — onboarding against a freshly installed stack (INS-04, TST-03,
// ABN-01 d). Nothing is mocked: the install-smoke workflow installs
// Reticora, applies e2e/fixtures/setup-onboarding.sh (mail catcher, signup
// configuration, collector build) and runs this file with
// e2e/fixtures/playwright.install.config.ts.
//
// INS-04 error images on the real install: the API answers through the
// frontend origin (no "failed to fetch"), the authorization redirect goes to
// the configured issuer instead of localhost, an insecure origin explains the
// secure-context requirement of crypto.subtle (PKCE).
// ABN-01 (d): self-signup with verification and trial, login, dashboard,
// collector enrollment, scope confirmation, 30 simulated devices, a review
// resolved, an impact query and an export. The impact step sets a dependency
// between two simulated CIs and expects it in the impact; a CI without any
// relationship impacts nothing (WP-456, F-299).

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const SERVER = process.env.E2E_SERVER_URL || 'http://localhost:8080';
const MAILPIT = process.env.E2E_MAILPIT_URL || 'http://localhost:8025';
const INSECURE = process.env.E2E_INSECURE_URL || '';
const ISSUER = process.env.E2E_ISSUER_URL || '';
const COLLECTOR_BIN = process.env.E2E_COLLECTOR_BIN || '';

const stamp = Date.now();
const email = `admin-${stamp}@muster-e2e.de`;
const password = `Onboarding-${stamp}!a`;
const SIMULATED_DEVICES = 30;

let collector: ChildProcess | undefined;

test.afterAll(() => {
  collector?.kill('SIGTERM');
});

/** sessionToken reads the bearer token of the signed-in browser session. */
async function sessionToken(page: Page): Promise<string> {
  const raw = await page.evaluate(() => window.sessionStorage.getItem('reticora-auth-session'));
  const token = raw ? (JSON.parse(raw) as { token?: string }).token : undefined;
  expect(token, 'session token after login').toBeTruthy();
  return token!;
}

async function apiGet<T>(request: APIRequestContext, token: string, path: string): Promise<T> {
  const res = await request.get(`${BASE}/api/v1${path}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(res.ok(), `GET ${path}: ${res.status()}`).toBe(true);
  return (await res.json()) as T;
}

/** verificationLink waits for the signup e-mail and returns its link. */
async function verificationLink(request: APIRequestContext): Promise<string> {
  let link = '';
  await expect
    .poll(
      async () => {
        const search = await request.get(`${MAILPIT}/api/v1/search`, {
          params: { query: `to:"${email}"` },
        });
        if (!search.ok()) return '';
        const found = (await search.json()) as { messages?: { ID: string }[] };
        const id = found.messages?.[0]?.ID;
        if (!id) return '';
        const message = (await (await request.get(`${MAILPIT}/api/v1/message/${id}`)).json()) as {
          Text?: string;
          HTML?: string;
        };
        const match = `${message.Text ?? ''} ${message.HTML ?? ''}`.match(
          /https?:\/\/[^\s"'<>]+\/onboarding\?token=[A-Za-z0-9_\-.%]+/,
        );
        link = match ? match[0].replace(/&amp;/g, '&') : '';
        return link;
      },
      { message: `verification e-mail to ${email}`, timeout: 60_000, intervals: [1_000, 2_000] },
    )
    .not.toBe('');
  return link;
}

/**
 * keycloakLogin signs in on the Keycloak form, completes the second factor
 * the signup administrator holds (OTP setup at the first login, the code at
 * later ones; support/totp.ts, WP-357) and a required profile step should
 * the realm ask for one; it fails with the page Keycloak stopped at instead
 * of a bare timeout.
 */
async function keycloakLogin(page: Page, landing: RegExp) {
  await page.waitForURL(/\/protocol\/openid-connect\/auth/);
  await page.locator('#username').fill(email);
  await page.locator('#password').fill(password);
  await page.locator('#kc-login').click();
  await keycloakOtp(page, email, landing);
  const lastName = page.locator('#lastName');
  await Promise.race([
    page.waitForURL(landing, { timeout: 60_000 }),
    lastName.waitFor({ state: 'visible', timeout: 60_000 }),
  ]).catch(() => undefined);
  if (await lastName.isVisible().catch(() => false)) {
    if (
      !(await page
        .locator('#firstName')
        .inputValue()
        .catch(() => ''))
    ) {
      await page.locator('#firstName').fill('E2E');
    }
    await lastName.fill('Admin');
    await page.locator('input[type="submit"], button[type="submit"]').first().click();
  }
  if (!landing.test(page.url())) {
    await page.waitForURL(landing, { timeout: 30_000 }).catch(async () => {
      const text = (
        await page
          .locator('body')
          .innerText()
          .catch(() => '')
      ).slice(0, 500);
      throw new Error(`login stopped at ${page.url()}: ${text}`);
    });
  }
}

test.describe.serial('install → signup/login → dashboard (INS-04, ABN-01 d)', () => {
  test('the API answers through the frontend origin (no "failed to fetch")', async ({ page }) => {
    const response = page.waitForResponse((r) => r.url().endsWith('/api/v1/auth/config'));
    await page.goto('/login');
    await page.getByRole('button', { name: 'Anmelden' }).click();
    const config = await response;
    expect(config.ok()).toBe(true);
    expect(new URL(config.url()).origin).toBe(new URL(BASE).origin);
    await page.waitForURL(/\/protocol\/openid-connect\/auth/);
  });

  test('the authorization redirect targets the configured issuer with PKCE', async ({ page }) => {
    await page.goto('/login');
    await page.getByRole('button', { name: 'Anmelden' }).click();
    await page.waitForURL(/\/protocol\/openid-connect\/auth/);
    const url = new URL(page.url());
    if (ISSUER) {
      expect(url.origin).toBe(new URL(ISSUER).origin);
    }
    expect(url.searchParams.get('code_challenge_method')).toBe('S256');
    expect(url.searchParams.get('code_challenge')).toBeTruthy();
    expect(new URL(url.searchParams.get('redirect_uri') ?? '').origin).toBe(new URL(BASE).origin);
  });

  test('an insecure origin explains the secure-context requirement', async ({ page }) => {
    expect(INSECURE, 'E2E_INSECURE_URL (set by setup-onboarding.sh)').toBeTruthy();
    await page.goto(`${INSECURE}/login`);
    await page.getByRole('button', { name: 'Anmelden' }).click();
    await expect(page.getByText(/erfordert eine sichere Verbindung/)).toBeVisible();
  });

  test('an unreachable backend is explained', async ({ page }) => {
    await page.route('**/api/v1/auth/config', (route) => route.abort('connectionrefused'));
    await page.goto('/login');
    await page.evaluate(() => window.sessionStorage.clear());
    await page.getByRole('button', { name: 'Anmelden' }).click();
    await expect(page.getByText(/Der Server ist nicht erreichbar/)).toBeVisible();
  });

  test('self-signup, verification, trial, login and dashboard', async ({ page, request }) => {
    await page.goto('/onboarding');
    await expect(
      page.getByRole('progressbar', { name: 'Fortschritt der Einrichtung' }),
    ).toHaveAttribute('aria-valuenow', '0');
    await page.getByLabel('Organisation').fill(`Muster E2E ${stamp}`);
    await page.getByLabel('E-Mail-Adresse').fill(email);
    await page.getByLabel('Ihr Name').fill('E2E Admin');
    await page.getByRole('button', { name: 'Konto anlegen' }).click();
    await expect(page.getByText(`Bestätigungslink an ${email}`, { exact: false })).toBeVisible();

    const link = await verificationLink(request);
    expect(new URL(link).origin).toBe(new URL(BASE).origin);
    await page.goto(link);
    await page.getByLabel(/Passwort festlegen/).fill(password);
    await page.getByRole('button', { name: 'Konto bestätigen' }).click();
    await expect(page.getByText(/ist angelegt/)).toBeVisible();
    await expect(page.getByText(/Ihr Testzeitraum läuft bis/)).toBeVisible();

    await page.getByRole('link', { name: 'Jetzt anmelden und fortfahren' }).click();
    await page.getByRole('button', { name: 'Anmelden' }).click();
    await keycloakLogin(page, /\/onboarding/);

    await page.goto('/dashboard');
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
    const token = await sessionToken(page);
    const org = await apiGet<{ name: string }>(request, token, '/org');
    expect(org.name).toBe(`Muster E2E ${stamp}`);
  });

  test('collector, scopes, 30 simulated devices, review, impact and export', async ({
    page,
    request,
  }) => {
    expect(COLLECTOR_BIN, 'E2E_COLLECTOR_BIN (set by setup-onboarding.sh)').toBeTruthy();
    // Every test has its own browser context: sign in with the new account.
    await page.goto('/login', { waitUntil: 'domcontentloaded' });
    await page.getByRole('button', { name: 'Anmelden' }).click();
    await keycloakLogin(page, /\/dashboard|\/onboarding/);
    const token = await sessionToken(page);

    // Collector enrollment with the code the onboarding shows.
    await page.goto('/onboarding');
    await page.getByRole('button', { name: 'Enrollment-Code erzeugen' }).click();
    const code = (await page.locator('code').first().textContent())?.trim() ?? '';
    expect(code).not.toBe('');
    const credentials = join(mkdtempSync(join(tmpdir(), 'reticora-e2e-')), 'credentials.json');
    const enroll = spawnSync(
      COLLECTOR_BIN,
      [
        'enroll',
        '--server',
        SERVER,
        '--code',
        code,
        '--name',
        'e2e-collector',
        '--credentials',
        credentials,
      ],
      { encoding: 'utf8', timeout: 60_000 },
    );
    expect(enroll.status, `collector enroll: ${enroll.stderr}${enroll.stdout}`).toBe(0);
    collector = spawn(COLLECTOR_BIN, [], {
      env: {
        ...process.env,
        RETICORA_ENVIRONMENT: 'development',
        RETICORA_SIMULATE: 'true',
        RETICORA_CREDENTIALS_PATH: credentials,
        RETICORA_SPOOL_DIR: join(credentials, '..', 'spool'),
        RETICORA_HEARTBEAT_INTERVAL: '10s',
      },
      stdio: 'inherit',
    });

    // The flow advances by itself once the collector reports.
    await expect(
      page.getByRole('progressbar', { name: 'Fortschritt der Einrichtung' }),
    ).not.toHaveAttribute('aria-valuenow', '2', { timeout: 120_000 });
    // Confirm the reported networks, then continue.
    const confirm = page.getByRole('button', { name: 'Bestätigen' });
    if (
      await confirm
        .first()
        .isVisible()
        .catch(() => false)
    ) {
      await confirm.first().click();
    }
    const next = page.getByRole('button', { name: 'Weiter' });
    if (await next.isVisible().catch(() => false)) {
      await next.click();
    }

    // The simulated fleet arrives as CIs.
    await expect
      .poll(async () => (await apiGet<{ total: number }>(request, token, '/cis?limit=1')).total, {
        message: 'CIs of the simulated fleet',
        timeout: 180_000,
        intervals: [3_000],
      })
      .toBeGreaterThanOrEqual(SIMULATED_DEVICES);
    await expect(page.getByText(/CIs sind bereits in Ihrer CMDB/)).toBeVisible({ timeout: 30_000 });

    // A review item of the discovery is resolved in the inbox.
    await page.goto('/reviews');
    const firstItem = page.locator('main ul li button').first();
    await expect(firstItem, 'an open review item of the simulated fleet').toBeVisible({
      timeout: 60_000,
    });
    await firstItem.click();
    const before = (
      await apiGet<{ total: number }>(request, token, '/review-items?status=open&limit=1')
    ).total;
    await page.getByRole('button', { name: 'Verwerfen' }).click();
    await expect
      .poll(
        async () =>
          (await apiGet<{ total: number }>(request, token, '/review-items?status=open&limit=1'))
            .total,
      )
      .toBeLessThan(before);

    // Impact of one CI (failure simulation in the topology). The simulated fleet
    // guarantees no dependency (F-299): the server sim-srv-01 of the fleet
    // (SIM-02) is made to depend on the NAS sim-nas-01, so the impact of
    // sim-nas-01 must name it.
    const headers = { Authorization: `Bearer ${token}` };
    const fleet = (
      await apiGet<{ data: { id: string; name: string; ci_type_id: string }[] }>(
        request,
        token,
        '/cis?limit=100',
      )
    ).data;
    const simulated = (name: string) => {
      const ci = fleet.find((c) => c.name === name);
      expect(ci, `simulated CI ${name}`).toBeTruthy();
      return ci!;
    };
    const dependent = simulated('sim-srv-01');
    const provider = simulated('sim-nas-01');
    const related = await request.post(`${BASE}/api/v1/relationships`, {
      headers,
      data: { source_ci_id: dependent.id, target_ci_id: provider.id, rel_type: 'depends_on' },
    });
    expect(related.status(), `POST /relationships: ${await related.text()}`).toBe(201);
    const impact = await apiGet<{ impacted: { id: string; heuristic: boolean }[] }>(
      request,
      token,
      `/impact/${provider.id}`,
    );
    expect(impact.impacted.length).toBeGreaterThanOrEqual(1);
    expect(impact.impacted.map((n) => n.id)).toContain(dependent.id);
    // Reached over the dependency, not only over the network heuristic (IMP-03).
    expect(impact.impacted.find((n) => n.id === dependent.id)?.heuristic).toBe(false);
    const simulation = page
      .locator('section')
      .filter({ has: page.getByRole('heading', { name: 'Ausfallsimulation', exact: true }) });
    await page.goto(`/topology?simulate=${provider.id}`);
    await expect(simulation.getByText(dependent.name, { exact: true })).toBeVisible();

    // Negative case: a CI without any relationship impacts nothing, and the
    // failure simulation says so.
    const lone = await request.post(`${BASE}/api/v1/cis`, {
      headers,
      data: { name: `e2e-ohne-beziehung-${stamp}`, ci_type_id: dependent.ci_type_id },
    });
    expect(lone.status(), `POST /cis: ${await lone.text()}`).toBe(201);
    const loneId = ((await lone.json()) as { id: string }).id;
    const relations = await apiGet<{ total: number }>(
      request,
      token,
      `/cis/${loneId}/relationships`,
    );
    expect(relations.total).toBe(0);
    const none = await apiGet<{ impacted: unknown[]; count: number }>(
      request,
      token,
      `/impact/${loneId}`,
    );
    expect(none.impacted).toEqual([]);
    expect(none.count).toBe(0);
    await page.goto(`/topology?simulate=${loneId}`);
    await expect(simulation.getByText(/Dieses CI hat keine abhängigen CIs/)).toBeVisible();

    // Export: the job completes with every CI, the file downloads.
    const created = await request.post(`${BASE}/api/v1/exports`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { format: 'csv' },
    });
    expect(created.status()).toBeLessThan(300);
    const job = (await created.json()) as { id: string };
    await expect
      .poll(
        async () =>
          (
            await apiGet<{ status: string; row_count?: number }>(
              request,
              token,
              `/exports/${job.id}`,
            )
          ).status,
        { message: 'export job', timeout: 120_000, intervals: [2_000] },
      )
      .toBe('completed');
    const done = await apiGet<{ row_count?: number; download_url?: string }>(
      request,
      token,
      `/exports/${job.id}`,
    );
    expect(done.row_count ?? 0).toBeGreaterThanOrEqual(SIMULATED_DEVICES);
    // The file of the job, over its signed URL and without the bearer token
    // (WP-468, F-077); /api/v1/export/cis is deprecated.
    expect(done.download_url, 'signed download URL of the completed job').toBeTruthy();
    const file = await request.get(done.download_url!);
    expect(file.ok(), `GET download_url: ${file.status()}`).toBe(true);
    const lines = (await file.text()).trim().split('\n');
    expect(lines.length).toBeGreaterThan(SIMULATED_DEVICES);
  });
});
