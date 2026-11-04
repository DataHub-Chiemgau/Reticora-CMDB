import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  A11Y_HANDLERS,
  A11Y_MARKERS as M,
  CI_ID,
  TEMPLATE_ID,
  TEMPLATE_PERMISSIONS,
  TOPOLOGY_TOO_LARGE,
} from './support/a11y-fixtures';
import { EMPTY_LIST, mockBackend, signIn } from './support/backend';
import type { Handler } from './support/backend';

// UI-18/NFR-07 (WP-193, WP-522, WP-631): every route of the router
// (src/App.tsx) passes an automated WCAG 2.1 AA check (axe-core) in every
// browser project of playwright.config.ts, in both UI languages of E-99
// (de-DE and en-US) and in the light and the dark theme, also with the
// light theme chosen on a dark OS (colour scheme of the browser). The
// backend is mocked with data (support/a11y-fixtures.ts): every list holds at
// least one row, and the check waits for that row, so axe sees rows, badges
// and links instead of an empty page. The empty states of the list pages of
// F-172 are checked as well.

const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

type Theme = 'light' | 'dark';

// The theme the user chose (stored like the theme toggle) and the colour
// scheme the OS reports (prefers-color-scheme). The chosen theme alone
// decides: before, the light theme on a dark OS kept the dark canvas of the
// OS under the dark text of the light theme (1:1 on every route).
interface ThemeCase {
  theme: Theme;
  os: Theme;
}
const THEMES: ThemeCase[] = [
  { theme: 'light', os: 'light' },
  { theme: 'dark', os: 'light' },
  { theme: 'light', os: 'dark' },
];

/** themeName names a case in the test title: the theme, and the OS if it differs. */
function themeName({ theme, os }: ThemeCase): string {
  return os === theme || os === 'light' ? theme : `${theme}, ${os} OS`;
}

// The UI languages of E-99 (WP-523). The interface follows the browser
// language; each check runs once per language, because the labels change the
// layout on a phone (F-357: the tables scrolled sideways in de-DE only).
type Language = 'de-DE' | 'en-US';
const LANGUAGES: Language[] = ['de-DE', 'en-US'];

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
}

// Every route passes without exception (WP-531, F-172/F-173). The findings
// of this check outside WP-522 are fixed: F-355 grey secondary text and F-361
// red delete actions by WP-630 (shared classes text-muted and text-danger,
// src/index.css; source scan src/test/darkVariant.test.ts), F-356 site markers
// inside the map image and F-357 tables that scroll sideways on a phone
// without taking the focus by WP-631 (audit/g1-reaudit/FINDINGS.md). A new
// violation is a new finding with its own WP (plan convention 16), not a mark.

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
  { path: '/inventory', url: '/inventory', ready: text('a11y-move') },
  { path: '/locations', url: '/locations', ready: text(M.location) },
  { path: '/saved-views', url: '/saved-views', ready: text(M.savedView) },
  {
    path: '/ci-types',
    url: '/ci-types',
    // WP-313: the type tree with parent and child; the child is selected, so
    // its attribute catalog (own, inherited, global; index states none,
    // queued and failed; the sensitive mark) is checked too. WP-797: the
    // child carries a set of two attributes, whose editor is open.
    prepare: async (page) => {
      await page.getByRole('treeitem', { name: M.ciTypeChild }).click();
      await expect(page.getByRole('treeitem', { name: M.ciTypeChild })).toHaveAttribute(
        'aria-selected',
        'true',
      );
      await page.locator('#attribute-sets button[aria-expanded]').first().click();
      const editor = page.locator('#attribute-sets [role="region"]');
      await expect(editor.getByText('a11y_set_vlan')).toBeVisible();
      await expect(editor.getByText('a11y_set_speed')).toBeVisible();
    },
    ready: (page) => page.locator('#main-content li').filter({ hasText: 'a11y_inventory' }),
  },
  {
    // WP-314: own and system template, with the rights to edit and publish.
    path: '/ci-types/templates',
    url: '/ci-types/templates',
    handlers: [TEMPLATE_PERMISSIONS],
    ready: (page) => page.getByRole('link', { name: M.systemTemplate }),
  },
  {
    // WP-314: the draft in the editor with its overrides and the comparison
    // of the two versions.
    path: '/ci-types/templates/:id',
    url: `/ci-types/templates/${TEMPLATE_ID}`,
    handlers: [TEMPLATE_PERMISSIONS],
    prepare: async (page) => {
      await expect(page.locator('[data-item-key="a11y_serial"]')).toBeVisible();
    },
    ready: (page) => page.locator('#main-content table'),
  },
  { path: '/assignments', url: '/assignments', ready: text(M.assignment) },
  { path: '/documents', url: '/documents', ready: text(M.document) },
  { path: '/stocktake', url: '/stocktake', ready: text(M.stocktake) },
  { path: '/consumables', url: '/consumables', ready: text(M.consumable) },
  { path: '/orders', url: '/orders', ready: text(M.order) },
  { path: '/maintenance', url: '/maintenance', ready: text(M.maintenance) },
  { path: '/disposal', url: '/disposal', ready: text(M.disposal) },
  { path: '/keys', url: '/keys', ready: text(M.key) },
  { path: '/trainings', url: '/trainings', ready: text(M.training) },
  { path: '/desks', url: '/desks', ready: text(M.desk) },
  { path: '/agents', url: '/agents', ready: text(M.agent) },
  { path: '/findings', url: '/findings', ready: text(M.finding) },
  { path: '/reviews', url: '/reviews', ready: text(M.review) },
  { path: '/jobs', url: '/jobs', ready: text(M.job) },
  { path: '/tickets', url: '/tickets', ready: text(M.ticket) },
  {
    path: '/users',
    url: '/users',
    ready: text(M.user),
  },
  {
    path: '/permissions',
    url: '/permissions',
    ready: text(M.permission),
  },
  { path: '/slas', url: '/slas', ready: text(M.sla) },
  { path: '/forms', url: '/forms', ready: text(M.form) },
  { path: '/workflows', url: '/workflows', ready: text(M.workflow) },
  { path: '/compliance', url: '/compliance', ready: text(M.compliance) },
  { path: '/iga', url: '/iga', ready: text(M.iga) },
  { path: '/assistant', url: '/assistant', ready: text(M.conversation) },
  { path: '/webhooks', url: '/webhooks', ready: text(M.webhook) },
  {
    path: '/export',
    url: '/export',
    ready: (page) => page.locator('#main-content tbody td', { hasText: 'csv' }).first(),
  },
  { path: '/monitoring', url: '/monitoring', ready: text(M.alert) },
  { path: '/audit', url: '/audit', ready: text(M.audit) },
  { path: '/security', url: '/security', ready: text(M.compliance) },
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

/** expectLanguage checks that the interface runs in the browser language. */
async function expectLanguage(page: Page, language: Language) {
  await expect(page.locator('html')).toHaveAttribute('lang', language);
}

/**
 * expectTheme checks that the chosen theme is in effect whatever the OS
 * prefers: the class of the dark: variants, data-theme (which switches off the
 * pre-JS OS fallback of src/index.css), the colour scheme of native controls
 * and the canvas colour (white, or the dark surface hsl(220 13% 10%)).
 */
async function expectTheme(page: Page, theme: Theme) {
  const html = page.locator('html');
  if (theme === 'dark') {
    await expect(html).toHaveClass(/\bdark\b/);
  } else {
    await expect(html).not.toHaveClass(/\bdark\b/);
  }
  await expect(html).toHaveAttribute('data-theme', theme);
  await expect(html).toHaveCSS('color-scheme', theme);
  const canvas = await html.evaluate((el) => getComputedStyle(el).backgroundColor);
  const channels = (canvas.match(/\d+(\.\d+)?/g) ?? []).slice(0, 3).map(Number);
  expect(channels, `html background ${canvas}`).toHaveLength(3);
  for (const channel of channels) {
    if (theme === 'dark') {
      expect(channel, `html background ${canvas}`).toBeLessThan(40);
    } else {
      expect(channel, `html background ${canvas}`).toBeGreaterThan(250);
    }
  }
}

// Each violation names its elements with their markup, so a failure in CI
// shows which element of the page breaks the rule. include limits the check
// to one part of the page, e.g. an open dialog (Radix hides the page behind
// it with aria-hidden).
async function expectNoViolations(page: Page, include?: string) {
  const builder = new AxeBuilder({ page }).withTags(WCAG_TAGS);
  if (include) builder.include(include);
  const results = await builder.analyze();
  const summary = results.violations.map(
    (v) =>
      `${v.id} (${v.impact}): ${v.nodes
        .map((n) => `${n.target.join(' ')} ${n.html.slice(0, 160)}`)
        .join(', ')}`,
  );
  expect(summary, 'WCAG 2.1 AA violations').toEqual([]);
}

/**
 * checkPage opens a page with data in a theme on an OS colour scheme and runs
 * axe on it (on the part named by include, if given).
 */
async function checkPage(
  page: Page,
  route: Pick<A11yRoute, 'url' | 'signedIn' | 'handlers' | 'ready' | 'prepare'> & {
    include?: string;
  },
  { theme, os }: ThemeCase,
  language: Language,
) {
  const diagnostics = pageDiagnostics(page);
  await mockBackend(page, [...(route.handlers ?? []), ...A11Y_HANDLERS]);
  if (route.signedIn !== false) await signIn(page);
  await storeTheme(page, theme);
  await page.emulateMedia({ colorScheme: os });
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
  await expectLanguage(page, language);
  await expectTheme(page, theme);
  await expectNoViolations(page, route.include);
}

// The dialogs of the CI type administration on the data of the a11y fixtures
// (organisation type M.ciType with the child M.ciTypeChild, one template). Radix portals a dialog under
// <body>, outside the App shell that gives the pages their text colour: the
// unclassed checkbox labels of the field editor were black on the dark
// dialog (1.18:1). The page behind an open dialog is hidden (aria-hidden),
// so axe checks the dialog.
const DIALOGS: {
  name: string;
  prepare: (page: Page) => Promise<void>;
  /** The element whose text colour must not be the browser default black. */
  plainText: (page: Page) => Locator;
}[] = [
  {
    name: 'CI type field editor',
    prepare: async (page) => {
      await page.getByRole('treeitem', { name: M.ciType }).click();
      await page.getByRole('button', { name: /^(feld hinzufügen|add field)$/i }).click();
      await page
        .getByRole('dialog')
        .getByRole('checkbox', { name: /^(eindeutig|unique)$/i })
        .check();
      await expect(
        page.getByRole('dialog').getByRole('combobox', { name: /^(eindeutig je|unique per)$/i }),
      ).toBeVisible();
    },
    plainText: (page) =>
      page
        .getByRole('dialog')
        .locator('label')
        .filter({ has: page.getByRole('checkbox', { name: /^(eindeutig|unique)$/i }) }),
  },
  {
    name: 'CI type templates',
    prepare: async (page) => {
      await page.getByRole('button', { name: /^(aus typvorlage|from type template)$/i }).click();
      await expect(page.getByRole('dialog').getByRole('button', { name: /server/i })).toBeVisible();
    },
    plainText: (page) =>
      page
        .getByRole('dialog')
        .getByRole('button', { name: /server/i })
        .locator('p')
        .first(),
  },
];

test.describe('accessibility (WCAG 2.1 AA)', () => {
  test('every route of the router has a check', () => {
    const covered = ROUTES.map((route) => route.path).sort();
    expect(covered, 'routes of src/App.tsx without an entry in ROUTES (or stale entries)').toEqual(
      [...new Set(routerPaths())].sort(),
    );
  });

  // The theme toggle cycles relative to the OS theme (system -> opposite of
  // the OS -> OS theme -> system): every click away from system changes the
  // page, a light theme stored on a dark OS returns to following the OS, and
  // the toggle and the page follow an OS change at runtime (no stale label).
  test.describe('theme toggle', () => {
    test.use({ locale: 'de-DE' });

    test('cycles relative to the OS theme', async ({ page }) => {
      await mockBackend(page, A11Y_HANDLERS);
      await signIn(page);
      await storeTheme(page, 'light');
      await page.emulateMedia({ colorScheme: 'dark' });
      await page.goto('/dashboard');
      const toggle = page.getByRole('button', { name: /^Dunkelmodus umschalten/ });
      const named = (current: string, next: string) =>
        expect(toggle).toHaveAccessibleName(
          `Dunkelmodus umschalten – aktuell: ${current}, als Nächstes: ${next}`,
        );

      // Dark OS: light -> dark (the OS theme) -> system.
      await named('Hell', 'Dunkel');
      await expectTheme(page, 'light');
      await toggle.click();
      await named('Dunkel', 'System');
      await expectTheme(page, 'dark');
      await toggle.click();
      await named('System (Dunkel)', 'Hell');
      await expectTheme(page, 'dark');
      // The OS turns light: the page and the next step follow it.
      await page.emulateMedia({ colorScheme: 'light' });
      await named('System (Hell)', 'Dunkel');
      await expectTheme(page, 'light');
      expect(await page.evaluate(() => localStorage.getItem('reticora-theme-mode'))).toBe('system');
      // Light OS: system -> dark -> light, each click changes the page; only
      // the return to system keeps it.
      await toggle.click();
      await named('Dunkel', 'Hell');
      await expectTheme(page, 'dark');
      await toggle.click();
      await named('Hell', 'System');
      await expectTheme(page, 'light');
      // An explicit theme stays on an OS change, its next step follows the OS.
      await page.emulateMedia({ colorScheme: 'dark' });
      await named('Hell', 'Dunkel');
      await expectTheme(page, 'light');
      await page.emulateMedia({ colorScheme: 'light' });
      await named('Hell', 'System');
      await toggle.click();
      await named('System (Hell)', 'Dunkel');
      await expectTheme(page, 'light');
      expect(await page.evaluate(() => localStorage.getItem('reticora-theme-mode'))).toBe('system');
    });
  });

  // Each check runs once per UI language (E-99): the browser language picks
  // it (src/i18n), and checkPage asserts the lang attribute it sets.
  for (const language of LANGUAGES) {
    test.describe(language, () => {
      test.use({ locale: language });

      for (const route of ROUTES) {
        for (const theme of THEMES) {
          test(`page ${route.path} (${themeName(theme)})`, async ({ page }) => {
            await checkPage(page, route, theme, language);
          });
        }
      }

      for (const dialog of DIALOGS) {
        for (const theme of THEMES) {
          test(`dialog ${dialog.name} (${themeName(theme)})`, async ({ page }) => {
            await checkPage(
              page,
              {
                url: '/ci-types',
                prepare: async (p) => {
                  await expect(p.getByText(M.ciType).first()).toBeVisible();
                  await dialog.prepare(p);
                },
                ready: (p) => p.getByRole('dialog'),
                include: '[role="dialog"]',
              },
              theme,
              language,
            );
            await expect(dialog.plainText(page)).not.toHaveCSS('color', 'rgb(0, 0, 0)');
          });
        }
      }

      // WP-524 (E-87): the CI detail of a CI outside the client chosen in the
      // scope control, with the notice, the counter of the hidden relationships
      // and, after "show all", the outside badge.
      for (const theme of THEMES) {
        test(`page /cmdb/:id outside the chosen scope (${themeName(theme)})`, async ({ page }) => {
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
            language,
          );
        });
      }

      for (const { url, endpoint } of EMPTY_PAGES) {
        for (const theme of THEMES) {
          test(`empty state ${url} (${themeName(theme)})`, async ({ page }) => {
            await checkPage(
              page,
              {
                url,
                handlers: [{ path: endpoint, reply: () => ({ body: EMPTY_LIST }) }],
                ready: (p) => p.locator('#main-content td[colspan]'),
              },
              theme,
              language,
            );
          });
        }
      }
    });
  }
});
