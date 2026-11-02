import { readdirSync, readFileSync } from 'node:fs';
import type { Page } from '@playwright/test';
import {
  AT,
  CI_TYPES,
  LENIENT_SPECS,
  ORG_ID,
  expect,
  list,
  mockBackend,
  signIn,
  takeFailures,
  test,
} from './support/backend';
import { contract } from './support/contract';

// Self-test of the strict mocked backend and its contract check (TST-03,
// WP-530; binding retest of F-178): a request outside api/openapi.yaml (a
// type key as ci_type_id, an unspecified route or method), an unhandled call
// and a mock answer outside the schema are each detected with method, path
// and schema error, and fail the test after it ran.

const CI_ID = '11111111-1111-4111-8111-111111111111';
const SERVER_TYPE = CI_TYPES.find((t) => t.key === 'server')!.id;

const ci = {
  id: CI_ID,
  organization_id: ORG_ID,
  ci_type_id: SERVER_TYPE,
  name: 'contract-ci',
  status: 'active',
  attributes: {},
  is_manual: true,
  created_at: AT,
  updated_at: AT,
};

/** call sends a request from the page (through the mock) and returns status and body. */
async function call(page: Page, method: string, path: string, body?: unknown) {
  return page.evaluate(
    async ({ method, path, body }) => {
      const response = await fetch(path, {
        method,
        headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const text = await response.text();
      return { status: response.status, body: text ? JSON.parse(text) : undefined };
    },
    { method, path, body },
  );
}

test.describe('contract check (api/openapi.yaml)', () => {
  test('a POST /cis with ci_type_id "server" breaks the contract', () => {
    const query = new URLSearchParams();
    const violations = contract().checkRequest({
      method: 'POST',
      path: '/api/v1/cis',
      query,
      body: { name: 'key-ci', ci_type_id: 'server' },
    });
    expect(violations).toEqual([
      {
        field: 'ci_type_id',
        message: 'POST /api/v1/cis: request body: /ci_type_id must match format "uuid"',
      },
    ]);
    expect(
      contract().checkRequest({
        method: 'POST',
        path: '/api/v1/cis',
        query,
        body: { name: 'uuid-ci', ci_type_id: SERVER_TYPE },
      }),
    ).toEqual([]);
  });

  test('an unspecified route or method breaks the contract', () => {
    const query = new URLSearchParams();
    expect(
      contract().checkRequest({ method: 'GET', path: '/api/v1/configuration-items', query }),
    ).toEqual([
      {
        field: 'path',
        message: 'GET /api/v1/configuration-items: route not specified in api/openapi.yaml',
      },
    ]);
    expect(
      contract()
        .checkRequest({ method: 'DELETE', path: '/api/v1/entitlements', query })
        .map((v) => v.message),
    ).toEqual([
      'DELETE /api/v1/entitlements: method not specified for /api/v1/entitlements in api/openapi.yaml',
    ]);
  });

  test('parameters are checked: path format, unknown and malformed query values', () => {
    const check = (path: string, query: string) =>
      contract()
        .checkRequest({ method: 'GET', path, query: new URLSearchParams(query) })
        .map((v) => v.message);
    expect(check('/api/v1/impact/not-a-uuid', '')).toEqual([
      'GET /api/v1/impact/not-a-uuid: path parameter ci_id: / must match format "uuid"',
    ]);
    expect(check(`/api/v1/impact/${CI_ID}`, '')).toEqual([]);
    // A renamed filter on an operation with query parameters is caught.
    expect(check('/api/v1/cis', 'type=server')).toEqual([
      'GET /api/v1/cis: query parameter type not specified',
    ]);
    expect(check('/api/v1/cis', 'limit=ten')).toEqual([
      'GET /api/v1/cis: query parameter limit: / must be integer',
    ]);
    expect(
      check('/api/v1/cis', `limit=25&offset=0&status=active&ci_type_id=${SERVER_TYPE}`),
    ).toEqual([]);
  });

  test('a response outside the schema or with an undeclared status breaks the contract', () => {
    const request = { method: 'GET', path: `/api/v1/cis/${CI_ID}` };
    expect(contract().checkResponse(request, { status: 200, body: ci })).toEqual([]);
    const withoutManual: Record<string, unknown> = { ...ci };
    delete withoutManual.is_manual;
    expect(contract().checkResponse(request, { status: 200, body: withoutManual })).toEqual([
      `GET /api/v1/cis/${CI_ID} -> 200: response body: / must have required property 'is_manual'`,
    ]);
    expect(contract().checkResponse(request, { status: 418, body: {} })[0]).toMatch(
      new RegExp(`^GET /api/v1/cis/${CI_ID} -> 418: status not specified`),
    );
  });
});

test.describe('strict mocked backend', () => {
  test('refuses ci_type_id "server" like the server and records the violation', async ({
    page,
  }) => {
    const calls = await mockBackend(page, [
      { method: 'POST', path: '/cis', reply: () => ({ status: 201, body: ci }) },
    ]);
    await page.goto('/login');

    const refused = await call(page, 'POST', '/api/v1/cis', {
      name: 'key-ci',
      ci_type_id: 'server',
    });
    expect(refused).toMatchObject({
      status: 422,
      body: { status: 422, violations: [{ field: 'ci_type_id' }] },
    });
    expect(takeFailures(page)).toEqual([
      'contract: POST /api/v1/cis: request body: /ci_type_id must match format "uuid"',
    ]);

    const created = await call(page, 'POST', '/api/v1/cis', {
      name: 'uuid-ci',
      ci_type_id: SERVER_TYPE,
    });
    expect(created).toMatchObject({ status: 201, body: { id: CI_ID } });
    expect(takeFailures(page)).toEqual([]);
    expect(calls.filter((c) => c.method === 'POST' && c.path === '/cis')).toHaveLength(2);
  });

  test('detects a call of an unspecified route', async ({ page }) => {
    // A spec may even mock the route: it is not in the contract.
    await mockBackend(page, [
      { path: '/configuration-items', reply: () => ({ body: list([ci]) }) },
    ]);
    await page.goto('/login');
    const answer = await call(page, 'GET', '/api/v1/configuration-items');
    expect(answer.status).toBe(404);
    expect(takeFailures(page)).toEqual([
      'contract: GET /api/v1/configuration-items: route not specified in api/openapi.yaml',
    ]);
  });

  test('a page whose endpoint was renamed meets no handler: 501, method and path', async ({
    page,
  }) => {
    // The spec answers a renamed list endpoint; the CI list still calls /cis.
    const calls = await mockBackend(page, [
      { path: '/cis/search', reply: () => ({ body: list([ci]) }) },
    ]);
    await signIn(page);
    await page.goto('/cmdb');
    // The list shows its error state once the page gave up (after retries).
    await expect(page.getByRole('alert').first()).toBeVisible({ timeout: 15_000 });
    expect(calls.some((c) => c.method === 'GET' && c.path === '/cis')).toBe(true);
    const failures = takeFailures(page);
    expect(failures.length).toBeGreaterThan(0);
    for (const failure of failures) {
      expect(failure).toMatch(
        /^unhandled: GET \/api\/v1\/cis\?\S* \(no handler in contract\.spec\.ts, not in UNUSED\['contract\.spec\.ts'\] of e2e\/support\/backend\.ts\)$/,
      );
    }
  });

  test('detects a mock answer outside the schema', async ({ page }) => {
    const broken: Record<string, unknown> = { ...ci };
    delete broken.is_manual;
    await mockBackend(page, [{ path: `/cis/${CI_ID}`, reply: () => ({ body: broken }) }]);
    await page.goto('/login');
    expect((await call(page, 'GET', `/api/v1/cis/${CI_ID}`)).status).toBe(200);
    expect(takeFailures(page)).toEqual([
      `contract: GET /api/v1/cis/${CI_ID} -> 200: response body: / must have required property 'is_manual'`,
    ]);
  });

  test('an unhandled call fails the test after it ran (afterEach)', async ({ page }) => {
    // Expected to fail: the fixture's teardown reports the unhandled call.
    test.fail(true, 'the strict mock reports GET /api/v1/cis/{id} as unhandled');
    await mockBackend(page, []);
    await page.goto('/login');
    expect((await call(page, 'GET', `/api/v1/cis/${CI_ID}`)).status).toBe(501);
  });

  test('a contract violation fails the test after it ran (afterEach)', async ({ page }) => {
    test.fail(true, 'the strict mock reports the type key as a contract violation');
    await mockBackend(page, [
      { method: 'POST', path: '/cis', reply: () => ({ status: 201, body: ci }) },
    ]);
    await page.goto('/login');
    await call(page, 'POST', '/api/v1/cis', { name: 'key-ci', ci_type_id: 'server' });
  });
});

test('every mocked spec runs on the strict mock', () => {
  // Real-stack (onboarding) and the specs with their own mock (LENIENT_SPECS)
  // aside, a spec imports test from support/backend.ts and answers /api/v1
  // only through mockBackend, never with its own page.route.
  const dir = new URL('.', import.meta.url);
  const specs = readdirSync(dir).filter(
    (f: string) =>
      f.endsWith('.spec.ts') && f !== 'onboarding.spec.ts' && !LENIENT_SPECS.includes(f),
  );
  expect(specs).toContain('ci.spec.ts');
  const offenders: string[] = [];
  for (const file of specs) {
    const source = readFileSync(new URL(file, dir), 'utf8');
    if (/import\s*\{[^}]*\btest\b[^}]*\}\s*from\s*'@playwright\/test'/.test(source)) {
      offenders.push(`${file}: imports test from @playwright/test`);
    }
    if (/page\.route\(\s*['"`][^'"`]*api\/v1/.test(source)) {
      offenders.push(`${file}: answers /api/v1 with its own page.route`);
    }
  }
  expect(offenders).toEqual([]);
});
