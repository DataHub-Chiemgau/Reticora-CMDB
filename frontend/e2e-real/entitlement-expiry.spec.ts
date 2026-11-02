import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import {
  api,
  keycloakAdminToken,
  operatorLogin,
  sessionToken,
  signupTenant,
  stamp,
  uiLogin,
  BASE,
  KEYCLOAK,
  type ApiResult,
  type OperatorSession,
} from './support/stack';

/*
 * Entitlement expiry banner on the real stack (WP-464, ENT-07, F-071; nothing mocked):
 * operator API → PostgreSQL (entitlement.valid_until) → GET /api/v1/entitlements → layout.
 * ENT-07 demands a dashboard banner 14, 7 and 1 day before valid_until; the notifications
 * of these thresholds come from the server run entitlement.notices (WP-425,
 * TestEntitlementNotices).
 *
 * One organization is created through the self-signup UI. The operator sets valid_until of
 * its discovery entitlement to now + 7 days (POST /admin/orgs/{id}/entitlements, ENT-04),
 * then restores the previous row. The operator authenticates with the break-glass token of
 * the test environment (E2E_OPERATOR_TOKEN = RETICORA_OPERATOR_TOKEN of the server, header
 * X-Operator-Token) when it is set, otherwise as a personal operator (realm group operators,
 * password and OTP, SEC-07) like ci-metrics.spec.ts.
 *
 * Test → requirement IDs proven:
 *  X1 valid_until in 7 days → after a reload the layout shows the banner with the feature
 *     and the date ...................................................... ENT-07 (WP-464)
 *  X2 valid_until restored → after a reload no banner ................... ENT-07 (WP-464)
 *
 * A failing test is a product finding: the assertions encode ENT-07
 * (docs/spec/katalog-v3/02-mandanten-auth-entitlements.md) and the acceptance criteria of
 * WP-464.
 */

const BREAK_GLASS = process.env.E2E_OPERATOR_TOKEN || '';
const DAY = 24 * 60 * 60 * 1000;
const FEATURE = 'discovery';
const BANNER = 'Ablaufende Funktionen';

interface Entitlement {
  organization_id: string;
  feature_key: string;
  plan: string;
  enabled: boolean;
  limits: Record<string, number>;
  valid_until?: string;
  source: string;
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  orgId: string;
  /** Authorization of the operator API: break-glass or operator session header. */
  operator: Record<string, string>;
  /** The discovery row before the test; undefined when the organization had none. */
  before?: Entitlement;
}

let w: World;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

function claims(token: string): { org: string } {
  return JSON.parse(Buffer.from(token.split('.')[1] ?? '', 'base64url').toString()) as {
    org: string;
  };
}

/** operatorApi calls /api/v1/admin/* with the operator's authorization. */
async function operatorApi<T>(
  method: 'GET' | 'POST',
  path: string,
  data?: unknown,
): Promise<ApiResult<T>> {
  const res = await w.rq.fetch(`${BASE}/api/v1${path}`, {
    method,
    headers: w.operator,
    data,
    failOnStatusCode: false,
  });
  const text = await res.text();
  let body: unknown = text;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    // keep the raw text
  }
  return { status: res.status(), body: body as T };
}

/**
 * operatorHeaders returns the break-glass header of the test environment or, without one,
 * the bearer token of a personal operator created in Keycloak (password and OTP).
 */
async function operatorHeaders(
  browser: Browser,
  rq: APIRequestContext,
): Promise<Record<string, string>> {
  if (BREAK_GLASS) return { 'X-Operator-Token': BREAK_GLASS };
  const run = stamp();
  const operator = {
    email: `expiry-operator-${run}@audit-e2e.de`,
    password: `Audit-op-${run}!aA`,
  };
  const admin = await keycloakAdminToken(rq);
  const created = await rq.post(`${KEYCLOAK}/admin/realms/reticora/users`, {
    headers: { Authorization: `Bearer ${admin}` },
    data: {
      username: operator.email,
      email: operator.email,
      emailVerified: true,
      enabled: true,
      firstName: 'Audit',
      lastName: 'Operator',
      groups: ['/operators'],
      credentials: [{ type: 'password', value: operator.password, temporary: false }],
    },
    failOnStatusCode: false,
  });
  expect(created.status(), `create the operator account: ${await created.text()}`).toBe(201);
  const context = await browser.newContext({ locale: 'de-DE' });
  try {
    const login = await operatorLogin(await context.newPage(), context.request, operator);
    expect(login.status, `operator callback: ${JSON.stringify(login.body)}`).toBe(200);
    return { Authorization: `Bearer ${(login.body as OperatorSession).token}` };
  } finally {
    await context.close();
  }
}

/** grant writes the discovery row of the organization through the operator API (ENT-04). */
async function grant(row: Omit<Entitlement, 'organization_id'>): Promise<Entitlement> {
  const res = await operatorApi<Entitlement>('POST', `/admin/orgs/${w.orgId}/entitlements`, {
    feature_key: row.feature_key,
    plan: row.plan,
    enabled: row.enabled,
    limits: row.limits,
    source: row.source,
    ...(row.valid_until ? { valid_until: row.valid_until } : {}),
  });
  expect(res.status, `grant ${row.feature_key}: ${JSON.stringify(res.body)}`).toBe(200);
  return res.body;
}

/** millis is a timestamp of the API as epoch milliseconds, null without one. */
function millis(value: string | undefined): number | null {
  return value ? new Date(value).getTime() : null;
}

/**
 * reloadDashboard reloads the dashboard, waits for the tenant's entitlements of the reloaded
 * page and lets the layout render them.
 */
async function reloadDashboard() {
  if (new URL(w.page.url()).pathname !== '/dashboard') {
    await w.page.goto('/dashboard');
  }
  const listed = w.page.waitForResponse(
    (res) => new URL(res.url()).pathname === '/api/v1/entitlements' && res.status() === 200,
  );
  await w.page.reload();
  await listed;
  await w.page.evaluate(
    () => new Promise((done) => requestAnimationFrame(() => setTimeout(done, 0))),
  );
}

test.describe('Entitlement expiry: the layout warns of a feature that ends in at most 14 days', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'expiry');
    const token = await uiLogin(page, account);
    const orgId = claims(token).org;
    expect(orgId, 'organization of the session').toBeTruthy();
    w = { rq, ctx, page, orgId, operator: {} };
    w.operator = await operatorHeaders(browser, rq);

    // Precondition: no feature of the new organization ends within 14 days.
    const listed = await operatorApi<{ data: Entitlement[] }>(
      'GET',
      `/admin/orgs/${orgId}/entitlements`,
    );
    expect(listed.status, `list the entitlements: ${JSON.stringify(listed.body)}`).toBe(200);
    const rows = listed.body.data ?? [];
    const soon = rows.filter(
      (e) => e.valid_until && new Date(e.valid_until).getTime() - Date.now() <= 14 * DAY,
    );
    expect(soon, 'entitlements of the new organization ending within 14 days').toEqual([]);
    w.before = rows.find((e) => e.feature_key === FEATURE);
    // An organization without discovery row starts with a plain grant.
    if (!w.before) {
      w.before = await grant({
        feature_key: FEATURE,
        plan: 'standard',
        enabled: true,
        limits: {},
        source: 'manual',
      });
    }
    await reloadDashboard();
    await expect(w.page.getByRole('region', { name: BANNER })).toHaveCount(0);
  });

  test.afterAll(async () => {
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('X1 valid_until in 7 days: the banner names the feature and the date after a reload', async () => {
    const until = new Date(Date.now() + 7 * DAY);
    const granted = await grant({ ...w.before!, valid_until: until.toISOString() });
    const stored = millis(granted.valid_until);
    expect(stored, 'stored valid_until').not.toBeNull();
    expect(Math.abs(stored! - until.getTime()), 'stored valid_until').toBeLessThan(1000);

    await reloadDashboard();
    const banner = w.page.getByRole('region', { name: BANNER });
    await expect(banner).toBeVisible();
    const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'long' }).format(
      new Date(granted.valid_until!),
    );
    await expect(banner.locator(`li[data-feature="${FEATURE}"]`)).toHaveText(
      `Die Funktion „Discovery“ läuft am ${date} ab (in 7 Tagen).`,
    );
  });

  test('X2 valid_until restored: no banner after a reload', async () => {
    const restored = await grant(w.before!);
    expect(millis(restored.valid_until), 'restored valid_until').toBe(
      millis(w.before!.valid_until),
    );

    await reloadDashboard();
    await expect(w.page.getByRole('region', { name: BANNER })).toHaveCount(0);
    // The tenant's own list agrees: discovery no longer ends within 14 days.
    const token = await sessionToken(w.page);
    const own = await api<{ data: Entitlement[] }>(w.rq, token, 'GET', '/entitlements');
    expect(own.status).toBe(200);
    const discovery = own.body.data.find((e) => e.feature_key === FEATURE);
    expect(discovery, 'discovery in GET /entitlements').toBeTruthy();
    expect(millis(discovery!.valid_until)).toBe(millis(w.before!.valid_until));
  });
});
