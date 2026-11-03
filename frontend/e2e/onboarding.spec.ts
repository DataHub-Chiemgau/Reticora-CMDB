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
// WP-529: the registration starts at the link "Konto anlegen" of the login
// page and the onboarding is reached over "Einrichtung fortsetzen" of the
// dashboard (F-351). The scope confirmation is a mandatory step: the spec
// waits for a suggestion, confirms it, checks the scope, the resolved item
// and the progress 3 → 4 → 5; a resolve broken with page.route keeps the
// progress at 3 and shows the error (F-177). The export file is loaded in
// the browser over the signed download_url of the job; a tampered signature
// is refused with 403 and fails the step (F-353).
// WP-558 (F-352): the INS-04 error image "Localhost-Redirect" is told apart.
// The install-smoke job tls-domain-pebble runs the tests tagged @ins-04 over
// the TLS origin of its installation (https://<domain>, Pebble certificate):
// authorize URL, redirect_uri and issuer must not name localhost and must
// share the origin of BASE_URL when BASE_URL is not a loopback origin; the
// test fails without E2E_ISSUER_URL. A self-test rewrites the redirect_uri of
// the authorize redirect to localhost (page.route) and expects the check to
// fail. @plain-http marks the test that needs a plain-HTTP origin of the UI,
// which a TLS installation redirects to HTTPS.

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

/** isLoopback: the host of the URL is localhost or a loopback address. */
function isLoopback(url: URL): boolean {
  const host = url.hostname.toLowerCase();
  return (
    host === 'localhost' ||
    host.endsWith('.localhost') ||
    /^127\.\d+\.\d+\.\d+$/.test(host) ||
    host === '[::1]'
  );
}

/**
 * authorizeRedirectProblems is the INS-04 check of the authorization
 * redirect (F-352): the authorize URL lies at the configured issuer and the
 * redirect_uri at the origin of BASE_URL. When BASE_URL is not a loopback
 * origin, none of authorize URL, redirect_uri and issuer names localhost or a
 * loopback address, and all three share the origin of BASE_URL. It returns
 * the violations, empty when the redirect is right.
 */
function authorizeRedirectProblems(authorize: URL): string[] {
  if (!ISSUER) return ['E2E_ISSUER_URL is not set (setup-onboarding.sh)'];
  const base = new URL(BASE);
  const issuer = new URL(ISSUER);
  const raw = authorize.searchParams.get('redirect_uri') ?? '';
  const problems: string[] = [];
  let redirect: URL | undefined;
  try {
    redirect = new URL(raw);
  } catch {
    problems.push(`redirect_uri is not a URL: '${raw}'`);
  }
  if (authorize.origin !== issuer.origin) {
    problems.push(`authorize URL ${authorize.origin} is not at the issuer ${issuer.origin}`);
  }
  if (redirect && redirect.origin !== base.origin) {
    problems.push(`redirect_uri ${redirect.origin} is not the origin of BASE_URL ${base.origin}`);
  }
  if (!isLoopback(base)) {
    const named: [string, string, URL | undefined][] = [
      ['authorize URL', authorize.href, authorize],
      ['redirect_uri', raw, redirect],
      ['issuer', ISSUER, issuer],
    ];
    for (const [name, value, url] of named) {
      if (/localhost/i.test(value) || (url && isLoopback(url))) {
        problems.push(`${name} names localhost: ${value}`);
      }
      // The origin of the redirect_uri is checked above in every case.
      if (url && url !== redirect && url.origin !== base.origin) {
        problems.push(`${name} ${url.origin} is not the origin of BASE_URL ${base.origin}`);
      }
    }
  }
  return problems;
}

/** authorizeRedirect clicks "Anmelden" and returns the authorize URL. */
async function authorizeRedirect(page: Page): Promise<URL> {
  await page.goto('/login');
  await page.getByRole('button', { name: 'Anmelden' }).click();
  await page.waitForURL(/\/protocol\/openid-connect\/auth/);
  return new URL(page.url());
}

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

/**
 * exportRows is the step "Export laden" (F-353): the browser loads the file
 * of the export job over its signed download_url, without the bearer token,
 * as the CI list does (WP-468); it returns the data rows and fails on any
 * other answer than 200. The page fetches from the object store's own origin
 * so that the answer does not depend on its CORS settings.
 */
async function exportRows(page: Page, downloadURL: string): Promise<string[]> {
  const store = new URL(downloadURL).origin;
  if (new URL(page.url()).origin !== store) {
    await page.goto(`${store}/minio/health/live`);
  }
  const file = await page.evaluate(async (url) => {
    const res = await fetch(url);
    return { status: res.status, text: await res.text() };
  }, downloadURL);
  if (file.status !== 200) {
    throw new Error(`GET download_url: ${file.status} ${file.text.slice(0, 200)}`);
  }
  // The first line is the header.
  return file.text.trim().split('\n').slice(1);
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
  test(
    'the API answers through the frontend origin (no "failed to fetch")',
    {
      tag: '@ins-04',
    },
    async ({ page }) => {
      const response = page.waitForResponse((r) => r.url().endsWith('/api/v1/auth/config'));
      await page.goto('/login');
      await page.getByRole('button', { name: 'Anmelden' }).click();
      const config = await response;
      expect(config.ok()).toBe(true);
      expect(new URL(config.url()).origin).toBe(new URL(BASE).origin);
      await page.waitForURL(/\/protocol\/openid-connect\/auth/);
    },
  );

  test(
    'the authorization redirect targets the configured issuer with PKCE',
    {
      tag: '@ins-04',
    },
    async ({ page }) => {
      expect(ISSUER, 'E2E_ISSUER_URL (set by setup-onboarding.sh)').toBeTruthy();
      const url = await authorizeRedirect(page);
      expect(authorizeRedirectProblems(url), `authorize URL ${url.href}`).toEqual([]);
      expect(url.searchParams.get('code_challenge_method')).toBe('S256');
      expect(url.searchParams.get('code_challenge')).toBeTruthy();
    },
  );

  test(
    'self-test: a localhost redirect_uri fails the redirect check',
    {
      tag: '@ins-04',
    },
    async ({ page }) => {
      expect(ISSUER, 'E2E_ISSUER_URL (set by setup-onboarding.sh)').toBeTruthy();
      // The authorize redirect of the SPA is answered with a redirect to the
      // same URL with a localhost redirect_uri (another port than BASE_URL, so
      // that a loopback BASE_URL is told apart as well).
      const injected = 'http://localhost:1/auth/callback';
      const authorize = (url: URL) =>
        url.pathname.endsWith('/protocol/openid-connect/auth') &&
        url.searchParams.get('redirect_uri') !== injected;
      let rewritten = 0;
      await page.route(authorize, (route) => {
        const url = new URL(route.request().url());
        url.searchParams.set('redirect_uri', injected);
        rewritten++;
        return route.fulfill({ status: 302, headers: { location: url.toString() } });
      });
      const url = await authorizeRedirect(page);
      expect(rewritten, 'the authorize redirect went through the rewriting route').toBe(1);
      expect(url.searchParams.get('redirect_uri')).toBe(injected);
      const problems = authorizeRedirectProblems(url);
      expect(problems).toContainEqual(
        expect.stringMatching(/^redirect_uri http:\/\/localhost:1 is not the origin of BASE_URL/),
      );
      if (!isLoopback(new URL(BASE))) {
        expect(problems).toContainEqual(
          expect.stringMatching(/^redirect_uri names localhost: http:\/\/localhost:1\//),
        );
        expect(problems).toContainEqual(expect.stringMatching(/^authorize URL names localhost: /));
      }
    },
  );

  test(
    'an insecure origin explains the secure-context requirement',
    {
      tag: ['@ins-04', '@plain-http'],
    },
    async ({ page }) => {
      expect(INSECURE, 'E2E_INSECURE_URL (set by setup-onboarding.sh)').toBeTruthy();
      await page.goto(`${INSECURE}/login`);
      await page.getByRole('button', { name: 'Anmelden' }).click();
      await expect(page.getByText(/erfordert eine sichere Verbindung/)).toBeVisible();
    },
  );

  test('an unreachable backend is explained', { tag: '@ins-04' }, async ({ page }) => {
    await page.route('**/api/v1/auth/config', (route) => route.abort('connectionrefused'));
    await page.goto('/login');
    await page.evaluate(() => window.sessionStorage.clear());
    await page.getByRole('button', { name: 'Anmelden' }).click();
    await expect(page.getByText(/Der Server ist nicht erreichbar/)).toBeVisible();
  });

  test('self-signup, verification, trial, login and dashboard', async ({ page, request }) => {
    // The registration starts at the entry of the login page (F-351).
    await page.goto('/login');
    await page.getByRole('link', { name: 'Konto anlegen' }).click();
    await page.waitForURL(/\/onboarding$/);
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
    // Without a collector the dashboard leads back to the onboarding (F-351).
    await expect(page.getByRole('link', { name: 'Einrichtung fortsetzen' })).toHaveAttribute(
      'href',
      '/onboarding',
    );
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

    // The CI count of the onboarding (GET /cis?limit=1) is held at 0 in this
    // browser until the scope step is confirmed: the simulated fleet may
    // arrive before the confirmation, and the progress has to show the step
    // "Erste CIs" (4) before the real count completes it (5).
    const onboardingCICount = (url: URL) =>
      url.pathname === '/api/v1/cis' && url.search === '?limit=1';
    await page.route(onboardingCICount, (route) =>
      route.request().method() === 'GET'
        ? route.fulfill({
            json: { data: [], total: 0, limit: 1, offset: 0, has_more: false },
          })
        : route.continue(),
    );

    // Collector enrollment with the code the onboarding shows; the onboarding
    // is reached over the entry of the dashboard (F-351).
    await page.goto('/dashboard');
    await page.getByRole('link', { name: 'Einrichtung fortsetzen' }).click();
    await page.waitForURL(/\/onboarding$/);
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

    // Scope confirmation, a mandatory step (ABN-01 d, F-177): the collector
    // reports its networks, the server offers them as scope_suggested.
    type Suggestion = { id: string; payload: { cidr: string; collector_id: string } };
    let suggestion: Suggestion | undefined;
    await expect
      .poll(
        async () => {
          const open = await apiGet<{ data: Suggestion[] }>(
            request,
            token,
            '/review-items?kind=scope_suggested&status=open&limit=100',
          );
          suggestion = open.data[0];
          return open.data.length;
        },
        { message: 'a scope suggestion of the collector', timeout: 120_000, intervals: [2_000] },
      )
      .toBeGreaterThan(0);
    const { id: suggestionId, payload } = suggestion!;
    const progress = page.getByRole('progressbar', { name: 'Fortschritt der Einrichtung' });
    await expect(progress).toHaveAttribute('aria-valuenow', '3');
    const row = page.getByRole('listitem').filter({ hasText: payload.cidr });
    const confirm = row.getByRole('button', { name: 'Bestätigen' });
    await expect(confirm).toBeVisible();

    // Negative case: a broken resolve keeps the step and says so; the
    // suggestion stays open.
    const resolvePath = `**/api/v1/review-items/${suggestionId}/resolve`;
    await page.route(resolvePath, (route) =>
      route.fulfill({
        status: 500,
        contentType: 'application/problem+json',
        body: JSON.stringify({
          title: 'Internal Server Error',
          status: 500,
          detail: 'resolve broken by the onboarding suite',
        }),
      }),
    );
    await confirm.click();
    await expect(
      page.getByRole('alert').filter({ hasText: 'Der Vorschlag wurde nicht übernommen' }),
    ).toContainText('resolve broken by the onboarding suite');
    await expect(progress).toHaveAttribute('aria-valuenow', '3');
    await expect(row).toBeVisible();
    expect(
      (await apiGet<{ status: string }>(request, token, `/review-items/${suggestionId}`)).status,
    ).toBe('open');
    await page.unroute(resolvePath);

    // Positive case: the confirmation creates the scope of the collector,
    // resolves the item with accept and completes the step.
    const resolved = page.waitForResponse(
      (r) =>
        r.url().endsWith(`/review-items/${suggestionId}/resolve`) &&
        r.request().method() === 'POST',
    );
    await confirm.click();
    expect((await resolved).status()).toBe(200);
    await expect(progress).toHaveAttribute('aria-valuenow', '4');
    const item = await apiGet<{ status: string; action?: string }>(
      request,
      token,
      `/review-items/${suggestionId}`,
    );
    expect(item.status).toBe('resolved');
    expect(item.action).toBe('accept');
    const scopes = await apiGet<{ data: { target: string; collector_id: string }[] }>(
      request,
      token,
      `/discovery-scopes?collector_id=${encodeURIComponent(payload.collector_id)}&limit=100`,
    );
    expect(scopes.data.map((s) => s.target)).toContain(payload.cidr);
    await page.unroute(onboardingCICount);

    // The simulated fleet arrives as CIs.
    await expect
      .poll(async () => (await apiGet<{ total: number }>(request, token, '/cis?limit=1')).total, {
        message: 'CIs of the simulated fleet',
        timeout: 180_000,
        intervals: [3_000],
      })
      .toBeGreaterThanOrEqual(SIMULATED_DEVICES);
    await expect(page.getByText(/CIs sind bereits in Ihrer CMDB/)).toBeVisible({ timeout: 30_000 });
    await expect(progress).toHaveAttribute('aria-valuenow', '5');

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
    const downloadURL = done.download_url!;
    expect(new URL(downloadURL).searchParams.get('X-Amz-Signature')).toBeTruthy();

    // Negative case (F-353): with a tampered signature the object store
    // answers 403 and the step fails.
    const signedDownload = (url: URL) => url.searchParams.has('X-Amz-Signature');
    let tampered = 0;
    await page.route(signedDownload, (route) => {
      const url = new URL(route.request().url());
      const signature = url.searchParams.get('X-Amz-Signature')!;
      url.searchParams.set(
        'X-Amz-Signature',
        signature.slice(0, -1) + (signature.endsWith('0') ? '1' : '0'),
      );
      tampered++;
      return route.continue({ url: url.toString() });
    });
    await expect(exportRows(page, downloadURL)).rejects.toThrow(/^GET download_url: 403\b/);
    expect(tampered, 'the download went through the tampering route').toBe(1);
    await page.unroute(signedDownload);

    // Positive case: at least one data row per simulated device.
    const rows = await exportRows(page, downloadURL);
    expect(rows.length).toBeGreaterThanOrEqual(SIMULATED_DEVICES);
  });
});
