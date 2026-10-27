import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';

// UI-18/NFR-07 (WP-193): every main page passes an automated WCAG 2.1 AA check
// (axe-core) in every browser project of playwright.config.ts. The backend is
// mocked: lists are empty, so the checks cover layout, navigation, forms and
// empty states of each page. Endpoints that answer no list get their empty
// answer in the shape of api/openapi.yaml (WP-606).

const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

const EMPTY_LIST = {
  data: [],
  items: [],
  total: 0,
  limit: 50,
  offset: 0,
  has_more: false,
};

// Main pages of UI-01..UI-10: dashboard, CI list, topology, racks, room plan,
// discovery, webhooks, export, audit viewer and the administration pages.
const MAIN_PAGES = [
  '/dashboard',
  '/cmdb',
  '/topology',
  '/racks',
  '/roomplan',
  '/discovery',
  '/webhooks',
  '/export',
  '/audit',
  '/users',
  '/permissions',
];

// The empty answers of the endpoints the main pages read that are no list:
// the compliance score of the dashboard (ComplianceEvaluationResponse) and
// the topology graph.
const EMPTY_OBJECTS: Record<string, unknown> = {
  '/api/v1/compliance/score': {
    overall: { passed: 0, failed: 0, not_applicable: 0, score: 0 },
    by_ci_type: [],
    results: [],
  },
  '/api/v1/topology': { nodes: [], edges: [] },
};

async function mockBackend(page: Page) {
  await page.route('**/api/v1/**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(EMPTY_OBJECTS[new URL(route.request().url()).pathname] ?? EMPTY_LIST),
    }),
  );
}

async function signIn(page: Page) {
  await page.addInitScript(() => {
    window.sessionStorage.setItem(
      'reticora-auth-session',
      JSON.stringify({
        token: 'header.payload.signature',
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        user: {
          sub: '00000000-0000-0000-0000-000000000001',
          name: 'Admin',
          org_id: '00000000-0000-0000-0000-0000000000aa',
          permissions: [],
        },
      }),
    );
  });
}

// pageDiagnostics collects what the browser reports while a page loads:
// uncaught errors, console errors and failed requests. When the app shell
// does not appear, the assertion names them, so a failure in CI shows its
// cause in the job log. pageErrors holds the uncaught errors alone.
function pageDiagnostics(page: Page): { describe: () => string; pageErrors: string[] } {
  const lines: string[] = [];
  const pageErrors: string[] = [];
  page.on('pageerror', (err) => {
    pageErrors.push(err.message);
    lines.push(`pageerror: ${err.message}`);
  });
  page.on('console', (msg) => {
    if (msg.type() === 'error') lines.push(`console.error: ${msg.text()}`);
  });
  page.on('requestfailed', (req) =>
    lines.push(`requestfailed: ${req.url()} ${req.failure()?.errorText ?? ''}`),
  );
  const describe = () =>
    lines.length ? lines.join('\n') : 'no page errors, console errors or failed requests';
  return { describe, pageErrors };
}

async function expectAppShell(page: Page, diagnostics: () => string) {
  try {
    await expect(page.locator('#main-content')).toBeVisible();
  } catch (err) {
    const state = await page
      .evaluate(
        () =>
          `${document.readyState}, #root children: ${document.getElementById('root')?.childElementCount ?? 'no #root'}`,
      )
      .catch((e: unknown) => `unavailable (${String(e)})`);
    throw new Error(
      `${(err as Error).message}\nurl: ${page.url()}\ndocument: ${state}\n${diagnostics()}`,
    );
  }
}

async function expectNoViolations(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).analyze();
  const summary = results.violations.map(
    (v) => `${v.id} (${v.impact}): ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`,
  );
  expect(summary, 'WCAG 2.1 AA violations').toEqual([]);
}

test.describe('accessibility (WCAG 2.1 AA)', () => {
  test('login page', async ({ page }) => {
    await mockBackend(page);
    await page.goto('/login');
    await expect(page.getByRole('button', { name: /sign in|log in|anmelden/i })).toBeVisible();
    await expectNoViolations(page);
  });

  for (const path of MAIN_PAGES) {
    test(`page ${path}`, async ({ page }) => {
      const diagnostics = pageDiagnostics(page);
      await mockBackend(page);
      await signIn(page);
      await page.goto(path);
      await expectAppShell(page, diagnostics.describe);
      await page.waitForLoadState('networkidle');
      // A page that throws once its data arrives unmounts the app; axe then
      // checks an empty document and finds nothing (WP-606). The shell must
      // still stand after the requests, without an uncaught error.
      await expectAppShell(page, diagnostics.describe);
      expect(diagnostics.pageErrors, `uncaught errors on ${path}`).toEqual([]);
      await expectNoViolations(page);
    });
  }
});
