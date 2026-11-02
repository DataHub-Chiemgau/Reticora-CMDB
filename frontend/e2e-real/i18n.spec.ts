import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import { api, signupTenant, stamp, uiLogin, BASE, type Account } from './support/stack';

/*
 * UI language against real API data (WP-523, F-175, open retest of WP-194), nothing
 * mocked: UI → REST API → PostgreSQL → UI.
 *
 * Before WP-523 the CI detail showed the type UUID (ci_type_id) in the badge and in the
 * "CI-Typ" row instead of the type label of the metamodel (F-175).
 *
 * One organization is created through the self-signup UI; its administrator creates a
 * CI type with display_name de/en and a CI of that type in status "maintenance" through
 * the API.
 *
 * Test → requirement IDs proven:
 *  I1 the CI detail in German shows the de label of the type in the badge and in the
 *     type row, the status as "Wartung" and no type UUID .......... GLO-10, UI-18 (WP-194)
 *  I2 the language switch in the header (E-99) turns the same page to English: the en
 *     label, the status as "Maintenance", no type UUID; the choice is stored in the
 *     browser and holds after a reload .............................. GLO-10, UI-18 (WP-194)
 *  I3 without a stored choice an English browser (en-US) opens the CI detail in
 *     English (E-99) .................................................. GLO-10, UI-18
 *
 * A failing test is a product finding: the assertions encode GLO-10 and UI-18 in
 * docs/spec/katalog-v3 and the acceptance criteria of WP-523.
 */

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  account: Account;
  token: string;
  s: string;
  typeId: string;
  ciId: string;
  ciName: string;
}

let w: World;

const LABEL_DE = (s: string) => `Prüfserver ${s}`;
const LABEL_EN = (s: string) => `Test server ${s}`;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

/** ok asserts a 2xx answer of a setup call and returns its body. */
async function ok<T>(method: 'GET' | 'POST', path: string, data?: unknown): Promise<T> {
  const res = await api<T>(w.rq, w.token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

/**
 * englishPage opens the UI in a fresh English browser: preferred language en-US and
 * no stored language choice, signed in as the same administrator.
 */
async function englishPage(browser: Browser): Promise<{ ctx: BrowserContext; page: Page }> {
  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'en-US',
    extraHTTPHeaders: { 'X-Real-IP': realIp(), 'Accept-Language': 'en-US,en;q=0.9' },
  });
  const page = await ctx.newPage();
  await uiLogin(page, w.account);
  return { ctx, page };
}

/** expectTypeAndStatus checks the header badges and the type row of the CI detail. */
async function expectTypeAndStatus(
  page: Page,
  typeRow: string,
  typeLabel: string,
  status: string,
): Promise<void> {
  const main = page.locator('#main-content');
  await expect(main.getByRole('heading', { name: w.ciName })).toBeVisible();
  const header = main.getByRole('heading', { name: w.ciName }).locator('xpath=..');
  await expect(header.getByText(typeLabel, { exact: true })).toBeVisible();
  await expect(header.getByText(status, { exact: true })).toBeVisible();
  const row = main.locator('dt', { hasText: new RegExp(`^${typeRow}$`) }).locator('xpath=..');
  await expect(row.locator('dd')).toHaveText(typeLabel);
  await expect(main).not.toContainText(w.typeId);
  await expect(main).not.toContainText('maintenance');
}

test.describe('UI language of the CI detail against real API data', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'i18n');
    const token = await uiLogin(page, account);
    const s = stamp();
    w = { rq, ctx, page, account, token, s, typeId: '', ciId: '', ciName: `i18n-ci-${s}` };
    const type = await ok<{ id: string }>('POST', '/ci-types', {
      key: `i18n_${s}`,
      name: `i18n-type-${s}`,
      display_name: { de: LABEL_DE(s), en: LABEL_EN(s) },
    });
    w.typeId = type.id;
    const ci = await ok<{ id: string }>('POST', '/cis', {
      ci_type_id: w.typeId,
      name: w.ciName,
      status: 'maintenance',
    });
    w.ciId = ci.id;
  });

  test.afterAll(async () => {
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('I1 the CI detail in German shows the de type label and the translated status, no type UUID', async () => {
    const page = w.page;
    const typeLoaded = page.waitForResponse(
      (r) => new URL(r.url()).pathname.endsWith('/ci-types') && r.request().method() === 'GET',
    );
    await page.goto(`/cmdb/${w.ciId}`);
    expect((await typeLoaded).status(), 'GET /ci-types').toBe(200);
    await expectTypeAndStatus(page, 'CI-Typ', LABEL_DE(w.s), 'Wartung');
    await expect(page.locator('#main-content')).not.toContainText(LABEL_EN(w.s));
  });

  test('I2 the language switch in the header turns the CI detail to English and is stored', async () => {
    const page = w.page;
    await page.goto(`/cmdb/${w.ciId}`);
    await expectTypeAndStatus(page, 'CI-Typ', LABEL_DE(w.s), 'Wartung');

    const header = page.getByRole('banner');
    const german = header.getByRole('combobox', { name: 'Sprache' });
    await expect(german).toHaveValue('de-DE');
    await german.selectOption('en-US');

    // The same page switches without a reload.
    await expect(header.getByRole('combobox', { name: 'Language' })).toHaveValue('en-US');
    await expectTypeAndStatus(page, 'CI Type', LABEL_EN(w.s), 'Maintenance');
    await expect(page.locator('#main-content')).not.toContainText(LABEL_DE(w.s));
    await expect(page.locator('html')).toHaveAttribute('lang', 'en-US');

    // The choice is stored in this browser and wins over its German locale.
    expect(await page.evaluate(() => window.localStorage.getItem('reticora-language'))).toBe(
      'en-US',
    );
    await page.reload();
    await expectTypeAndStatus(page, 'CI Type', LABEL_EN(w.s), 'Maintenance');
    await expect(header.getByRole('combobox', { name: 'Language' })).toHaveValue('en-US');

    // Switching back by keyboard restores German.
    const english = header.getByRole('combobox', { name: 'Language' });
    await english.focus();
    await english.press('ArrowUp');
    await expect(header.getByRole('combobox', { name: 'Sprache' })).toHaveValue('de-DE');
    await expectTypeAndStatus(page, 'CI-Typ', LABEL_DE(w.s), 'Wartung');
  });

  test('I3 without a stored choice an English browser shows the CI detail in English', async ({
    browser,
  }) => {
    const en = await englishPage(browser);
    try {
      await en.page.goto(`/cmdb/${w.ciId}`);
      await expectTypeAndStatus(en.page, 'CI Type', LABEL_EN(w.s), 'Maintenance');
      await expect(en.page.locator('#main-content')).not.toContainText(LABEL_DE(w.s));
    } finally {
      await en.ctx.close();
    }
  });
});
