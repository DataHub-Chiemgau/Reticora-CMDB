import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';

// UI-18/NFR-07 (WP-193): every main page passes an automated WCAG 2.1 AA check
// (axe-core) in every browser project of playwright.config.ts. The backend is
// mocked: lists are empty, so the checks cover layout, navigation, forms and
// empty states of each page.

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

async function mockBackend(page: Page) {
  await page.route('**/api/v1/**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(EMPTY_LIST),
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
      await mockBackend(page);
      await signIn(page);
      await page.goto(path);
      await expect(page.locator('#main-content')).toBeVisible();
      await page.waitForLoadState('networkidle');
      await expectNoViolations(page);
    });
  }
});
