import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  A11Y_HANDLERS,
  A11Y_MARKERS as M,
  CI_ID,
  TOPOLOGY_TOO_LARGE,
} from './support/a11y-fixtures';
import { EMPTY_LIST, mockBackend, signIn } from './support/backend';
import type { Handler } from './support/backend';

// UI-18/NFR-07 (WP-193, WP-522): every route of the router (src/App.tsx)
// passes an automated WCAG 2.1 AA check (axe-core) in every browser project
// of playwright.config.ts, once in the light and once in the dark theme. The
// backend is mocked with data (support/a11y-fixtures.ts): every list holds at
// least one row, and the check waits for that row, so axe sees rows, badges
// and links instead of an empty page. The empty states of the list pages of
// F-172 are checked as well.

const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

type Theme = 'light' | 'dark';
const THEMES: Theme[] = ['light', 'dark'];

interface A11yRoute {
  /** The path pattern exactly as src/App.tsx declares it. */
  path: string;
  /** The URL the check opens. */
  url: string;
  /** False for the public pages that run without a session. */
  signedIn?: boolean;
  /** Handlers ahead of the fixtures, e.g. an error answer. */
  handlers?: Handler[];
  /** What must be visible before axe runs: the fixture row of the page. */
  ready: (page: Page) => Locator;
  /** Steps that bring the page into its data view (selections). */
  prepare?: (page: Page) => Promise<void>;
  /**
   * Violations found by this check in files outside WP-522, per theme, as
   * the ID of their finding. The check is expected to fail (test.fail) until
   * the follow-up WP fixes them; a marked check that passes fails.
   */
  known?: Partial<Record<Theme, string>>;
  /** The same for the mobile projects (isMobile) where they differ. */
  knownMobile?: Partial<Record<Theme, string>>;
}

// New findings of this check in pages outside WP-522 (IDs from F-355, plan
// convention 16; each gets its own follow-up WP):
// F-355 grey secondary text without a dark variant (and text-gray-400 of the
// inventory in the light theme), F-356 site markers inside the map image,
// F-357 tables that scroll sideways on a phone without taking the focus.
const F_CONTRAST = 'F-355';
const F_MAP_MARKERS = 'F-356';
const F_SCROLL_REGION = 'F-357';
const DARK_CONTRAST: A11yRoute['known'] = { dark: F_CONTRAST };

const text = (value: string) => (page: Page) => page.getByText(value, { exact: false }).first();

// One entry per route of src/App.tsx; the parity test below fails when a
// route is added there without an entry here.
const ROUTES: A11yRoute[] = [
  {
    path: '/login',
    url: '/login',
    signedIn: false,
    ready: (page) => page.getByRole('button', { name: /sign in|log in|anmelden/i }),
  },
  {
    path: '/auth/callback',
    url: '/auth/callback?code=a11y&state=unknown',
    signedIn: false,
    ready: (page) => page.locator('section h2'),
  },
  {
    path: '/onboarding',
    url: '/onboarding',
    signedIn: false,
    ready: (page) => page.locator('#main-content form, #main-content h2').first(),
    known: DARK_CONTRAST,
  },
  {
    path: '/invitations/accept',
    url: '/invitations/accept?token=a11y-token',
    signedIn: false,
    ready: (page) => page.locator('#main-content form'),
  },
  { path: '/', url: '/', ready: text(`${80} %`) },
  { path: '/dashboard', url: '/dashboard', ready: text(`${80} %`) },
  { path: '/cmdb', url: '/cmdb', ready: text(M.ci) },
  {
    path: '/cmdb/:id',
    url: `/cmdb/${CI_ID}`,
    // The advanced sections (fields, provenance, lifecycle, impact, metrics,
    // history) are opened and the blast radius and metrics loaded, so their
    // content is checked too.
    prepare: async (page) => {
      const sections = page.locator('#main-content details:not([open]) > summary');
      await expect(page.getByRole('heading', { name: M.ci })).toBeVisible();
      while ((await sections.count()) > 0) await sections.first().click();
      await page.getByRole('button', { name: /blast radius/i }).click();
      await page.getByRole('button', { name: /metriken laden|load metrics/i }).click();
    },
    ready: (page) => page.getByRole('heading', { name: M.ci }),
  },
  {
    path: '/topology',
    url: '/topology',
    // More than 2,000 CIs without a filter: the start dialog (422).
    handlers: [TOPOLOGY_TOO_LARGE],
    ready: (page) => page.locator('#main-content form'),
  },
  {
    path: '/map',
    url: '/map',
    // The marker of the site opens its details.
    prepare: (page) => page.getByRole('button', { name: M.site }).click(),
    ready: (page) => page.getByRole('heading', { name: M.site }),
    // Site markers (role=button) inside the map image (role=img).
    known: { light: F_MAP_MARKERS, dark: F_MAP_MARKERS },
  },
  {
    path: '/roomplan',
    url: '/roomplan',
    ready: (page) => page.locator('#main-content [aria-label]', { hasText: M.ci }).first(),
    prepare: async (page) => {
      const selects = page.locator('#main-content select');
      await expect(selects.first().locator('option', { hasText: M.building })).toHaveCount(1);
      await selects.first().selectOption({ label: M.building });
      await expect(selects.nth(1).locator('option', { hasText: M.room })).toHaveCount(1);
      await selects.nth(1).selectOption({ label: M.room });
    },
  },
  { path: '/racks', url: '/racks', ready: text(M.ci) },
  { path: '/discovery', url: '/discovery', ready: text(M.collector) },
  { path: '/assets', url: '/assets', ready: text(M.asset) },
  {
    path: '/inventory',
    url: '/inventory',
    ready: text('a11y-move'),
    known: { light: F_CONTRAST, dark: F_CONTRAST },
  },
  {
    path: '/locations',
    url: '/locations',
    ready: text(M.location),
    known: DARK_CONTRAST,
    // On a phone the row actions wrap; axe does not decide their contrast.
    knownMobile: {},
  },
  { path: '/saved-views', url: '/saved-views', ready: text(M.savedView), known: DARK_CONTRAST },
  { path: '/ci-types', url: '/ci-types', ready: text(M.ciType), known: DARK_CONTRAST },
  { path: '/assignments', url: '/assignments', ready: text(M.assignment) },
  { path: '/documents', url: '/documents', ready: text(M.document) },
  { path: '/stocktake', url: '/stocktake', ready: text(M.stocktake) },
  { path: '/consumables', url: '/consumables', ready: text(M.consumable), known: DARK_CONTRAST },
  { path: '/orders', url: '/orders', ready: text(M.order) },
  { path: '/maintenance', url: '/maintenance', ready: text(M.maintenance) },
  { path: '/disposal', url: '/disposal', ready: text(M.disposal) },
  { path: '/keys', url: '/keys', ready: text(M.key) },
  { path: '/trainings', url: '/trainings', ready: text(M.training) },
  { path: '/desks', url: '/desks', ready: text(M.desk) },
  { path: '/agents', url: '/agents', ready: text(M.agent) },
  { path: '/findings', url: '/findings', ready: text(M.finding), known: DARK_CONTRAST },
  { path: '/reviews', url: '/reviews', ready: text(M.review) },
  { path: '/jobs', url: '/jobs', ready: text(M.job), known: DARK_CONTRAST },
  { path: '/tickets', url: '/tickets', ready: text(M.ticket) },
  {
    path: '/users',
    url: '/users',
    ready: text(M.user),
    knownMobile: { light: F_SCROLL_REGION, dark: F_SCROLL_REGION },
  },
  {
    path: '/permissions',
    url: '/permissions',
    ready: text(M.permission),
    knownMobile: { light: F_SCROLL_REGION, dark: F_SCROLL_REGION },
  },
  { path: '/slas', url: '/slas', ready: text(M.sla) },
  { path: '/forms', url: '/forms', ready: text(M.form), known: DARK_CONTRAST },
  { path: '/workflows', url: '/workflows', ready: text(M.workflow), known: DARK_CONTRAST },
  { path: '/compliance', url: '/compliance', ready: text(M.compliance), known: DARK_CONTRAST },
  { path: '/iga', url: '/iga', ready: text(M.iga), known: DARK_CONTRAST },
  { path: '/assistant', url: '/assistant', ready: text(M.conversation) },
  { path: '/webhooks', url: '/webhooks', ready: text(M.webhook) },
  {
    path: '/export',
    url: '/export',
    ready: (page) => page.locator('#main-content tbody td', { hasText: 'csv' }).first(),
  },
  { path: '/monitoring', url: '/monitoring', ready: text(M.alert) },
  { path: '/audit', url: '/audit', ready: text(M.audit) },
  { path: '/security', url: '/security', ready: text(M.compliance), known: DARK_CONTRAST },
  // Unknown paths fall back to the CI list.
  { path: '*', url: '/a11y-unknown-route', ready: text(M.ci) },
];

// The list pages of F-172 whose empty state had too little contrast: they are
// checked with an empty list as well.
const EMPTY_PAGES: { url: string; endpoint: string }[] = [
  { url: '/assets', endpoint: '/assets' },
  { url: '/stocktake', endpoint: '/stocktakes' },
  { url: '/assignments', endpoint: '/assignments' },
  { url: '/documents', endpoint: '/documents' },
  { url: '/tickets', endpoint: '/tickets' },
];

/** routerPaths reads the path of every <Route> of src/App.tsx. */
function routerPaths(): string[] {
  const app = readFileSync(fileURLToPath(new URL('../src/App.tsx', import.meta.url)), 'utf8');
  return [...app.matchAll(/<Route\b[^>]*?\bpath="([^"]+)"/g)].map((match) => match[1]!);
}

/** storeTheme stores the theme the way the theme toggle does (stores/theme.ts). */
async function storeTheme(page: Page, theme: Theme) {
  await page.addInitScript((mode) => {
    window.localStorage.setItem('reticora-theme-mode', mode);
  }, theme);
}

// pageDiagnostics collects what the browser reports while a page loads:
// uncaught errors, console errors and failed requests. When the page does
// not appear, the assertion names them, so a failure in CI shows its cause
// in the job log. pageErrors holds the uncaught errors alone.
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

async function expectVisible(page: Page, locator: Locator, diagnostics: () => string) {
  try {
    await expect(locator).toBeVisible();
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

async function expectTheme(page: Page, theme: Theme) {
  if (theme === 'dark') {
    await expect(page.locator('html')).toHaveClass(/\bdark\b/);
  } else {
    await expect(page.locator('html')).not.toHaveClass(/\bdark\b/);
  }
}

// Each violation names its elements with their markup, so a failure in CI
// shows which element of the page breaks the rule.
async function expectNoViolations(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).analyze();
  const summary = results.violations.map(
    (v) =>
      `${v.id} (${v.impact}): ${v.nodes
        .map((n) => `${n.target.join(' ')} ${n.html.slice(0, 160)}`)
        .join(', ')}`,
  );
  expect(summary, 'WCAG 2.1 AA violations').toEqual([]);
}

/** checkPage opens a page with data in a theme and runs axe on it. */
async function checkPage(
  page: Page,
  route: Pick<A11yRoute, 'url' | 'signedIn' | 'handlers' | 'ready' | 'prepare'>,
  theme: Theme,
) {
  const diagnostics = pageDiagnostics(page);
  await mockBackend(page, [...(route.handlers ?? []), ...A11Y_HANDLERS]);
  if (route.signedIn !== false) await signIn(page);
  await storeTheme(page, theme);
  await page.goto(route.url);
  await expectVisible(page, page.locator('#main-content'), diagnostics.describe);
  await route.prepare?.(page);
  await expectVisible(page, route.ready(page), diagnostics.describe);
  await page.waitForLoadState('networkidle');
  // A page that throws once its data arrives unmounts the app; axe then
  // checks an empty document and finds nothing (WP-606). The page must still
  // stand after the requests, without an uncaught error.
  await expectVisible(page, route.ready(page), diagnostics.describe);
  expect(diagnostics.pageErrors, `uncaught errors on ${route.url}`).toEqual([]);
  await expectTheme(page, theme);
  await expectNoViolations(page);
}

test.describe('accessibility (WCAG 2.1 AA)', () => {
  test('every route of the router has a check', () => {
    const covered = ROUTES.map((route) => route.path).sort();
    expect(covered, 'routes of src/App.tsx without an entry in ROUTES (or stale entries)').toEqual(
      [...new Set(routerPaths())].sort(),
    );
  });

  for (const route of ROUTES) {
    for (const theme of THEMES) {
      test(`page ${route.path} (${theme})`, async ({ page, isMobile }) => {
        const finding = ((isMobile && route.knownMobile) || route.known)?.[theme];
        test.fail(finding !== undefined, finding);
        await checkPage(page, route, theme);
      });
    }
  }

  // WP-524 (E-87): the CI detail of a CI outside the client chosen in the
  // scope control, with the notice, the counter of the hidden relationships
  // and, after "show all", the outside badge.
  for (const theme of THEMES) {
    test(`page /cmdb/:id outside the chosen scope (${theme})`, async ({ page }) => {
      const otherClient = 'c7000000-0000-4000-8000-0000000000aa';
      await page.addInitScript((clientId) => {
        window.localStorage.setItem('reticora-scope', JSON.stringify({ clientId, siteId: '' }));
      }, otherClient);
      const clients = [{ id: otherClient, name: 'a11y Other Client', slug: 'a11y-other' }];
      await checkPage(
        page,
        {
          url: `/cmdb/${CI_ID}`,
          handlers: [
            {
              path: '/clients',
              reply: () => ({
                body: { data: clients, total: 1, limit: 200, offset: 0, has_more: false },
              }),
            },
          ],
          prepare: async (p) => {
            await p.getByRole('button', { name: /alle anzeigen|show all/i }).click();
          },
          ready: (p) => p.getByRole('status', { name: /arbeitsbereich|scope/i }),
        },
        theme,
      );
    });
  }

  for (const { url, endpoint } of EMPTY_PAGES) {
    for (const theme of THEMES) {
      test(`empty state ${url} (${theme})`, async ({ page }) => {
        await checkPage(
          page,
          {
            url,
            handlers: [{ path: endpoint, reply: () => ({ body: EMPTY_LIST }) }],
            ready: (p) => p.locator('#main-content td[colspan]'),
          },
          theme,
        );
      });
    }
  }
});
