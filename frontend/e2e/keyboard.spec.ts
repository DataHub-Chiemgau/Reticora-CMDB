import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';
import { list, mockBackend, signIn } from './support/backend';

// UI-01..UI-10 (WP-519): the four shared building blocks — modal, select,
// CI table and command palette — are operable with the keyboard alone in a
// real browser. Binding retests of F-170 (table keyboard pattern) and F-171
// (palette returns focus). Runs in every project of playwright.config.ts.

test.use({ locale: 'de-DE' });

function ci(n: number) {
  return {
    id: `11111111-1111-4111-8111-00000000000${n}`,
    organization_id: '00000000-0000-0000-0000-0000000000aa',
    ci_type_id: '5e1b0c3a-7d2f-4a10-9c01-000000000002',
    name: `core-sw-0${n}`,
    status: 'active',
    attributes: {},
    version: 1,
    created_at: '2026-10-14T08:00:00Z',
    updated_at: '2026-10-14T08:00:00Z',
  };
}

const CIS = [ci(1), ci(2), ci(3)];

async function openList(page: Page) {
  const calls = await mockBackend(page, [
    { path: '/cis', reply: () => ({ body: list(CIS) }) },
    {
      path: /^\/topology\/cis\/[^/]+\/neighbors$/,
      reply: () => ({ body: { nodes: [], edges: [] } }),
    },
    {
      path: /^\/cis\/[^/]+$/,
      reply: (_request, url) => ({ body: CIS.find((c) => url.pathname.endsWith(c.id)) }),
    },
  ]);
  await signIn(page);
  await page.goto('/cmdb');
  await expect(page.getByRole('link', { name: 'core-sw-01' })).toBeVisible();
  return calls;
}

function row(page: Page, name: string): Locator {
  return page.getByRole('row').filter({ has: page.getByRole('link', { name }) });
}

test('modal: focus moves in, Shift+Tab stays inside, Escape in a field returns focus', async ({
  page,
}) => {
  await openList(page);
  const trigger = page.getByRole('button', { name: 'CI erstellen' }).first();
  await trigger.focus();
  await page.keyboard.press('Enter');

  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog.locator(':focus')).toHaveCount(1);
  // Shift+Tab walks backwards through the dialog and wraps at its start; it
  // never reaches the page behind the dialog.
  for (let i = 0; i < 12; i++) {
    await page.keyboard.press('Shift+Tab');
    await expect(dialog.locator(':focus')).toHaveCount(1);
  }

  const name = dialog.getByLabel('Name');
  await name.focus();
  await page.keyboard.type('draft');
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
});

test('select: the status filter changes with the arrow keys', async ({ page }) => {
  const calls = await openList(page);
  const select = page.getByRole('combobox', { name: 'CI-Status filtern' });
  await select.focus();
  await page.keyboard.press('ArrowDown');
  await expect(select).toHaveValue('active');
  await expect
    .poll(() => calls.some((c) => c.path === '/cis' && c.search.includes('status=active')))
    .toBe(true);
  await page.keyboard.press('End');
  await expect(select).toHaveValue('decommissioned');
  await page.keyboard.press('Home');
  await expect(select).toHaveValue('');
});

test('CI table: Tab enters once, arrows move, Space selects, Enter opens', async ({ page }) => {
  await openList(page);
  const selectAll = page.getByRole('checkbox', { name: 'Alle sichtbaren CIs auswählen' });
  await selectAll.focus();

  // One Tab enters the table on its first row; the controls inside the rows
  // are no separate Tab stops.
  await page.keyboard.press('Tab');
  await expect(row(page, 'core-sw-01')).toBeFocused();
  await expect(page.locator('tbody tr[tabindex="0"]')).toHaveCount(1);

  await page.keyboard.press('ArrowDown');
  await expect(row(page, 'core-sw-02')).toBeFocused();
  await expect(row(page, 'core-sw-02')).toHaveAttribute('tabindex', '0');
  await expect(row(page, 'core-sw-01')).toHaveAttribute('tabindex', '-1');

  await page.keyboard.press('Space');
  await expect(page.getByRole('checkbox', { name: 'core-sw-02 auswählen' })).toBeChecked();
  await expect(page.getByText('1 ausgewählt')).toBeVisible();
  await expect(row(page, 'core-sw-02')).toBeFocused();
  await page.keyboard.press('Space');
  await expect(page.getByRole('checkbox', { name: 'core-sw-02 auswählen' })).not.toBeChecked();

  await page.keyboard.press('End');
  await expect(row(page, 'core-sw-03')).toBeFocused();
  await page.keyboard.press('Home');
  await expect(row(page, 'core-sw-01')).toBeFocused();
  await page.keyboard.press('ArrowDown');

  // Shift+Tab out of the table and Tab back return to the active row.
  await page.keyboard.press('Shift+Tab');
  await expect(selectAll).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(row(page, 'core-sw-02')).toBeFocused();

  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(new RegExp(`/cmdb/${CIS[1]!.id}$`));
});

test('palette: Ctrl+K and Escape return focus to the previous element', async ({ page }) => {
  await openList(page);
  const previous = page.getByRole('button', { name: 'Dunkelmodus umschalten' });
  await previous.focus();

  await page.keyboard.press('Control+k');
  const search = page.getByRole('combobox', { name: 'Befehle durchsuchen' });
  await expect(search).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(search).toBeHidden();
  await expect(previous).toBeFocused();

  // After a navigation focus lands on the main content of the new page.
  await page.keyboard.press('Control+k');
  await expect(search).toBeFocused();
  await search.fill('dashboard');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.locator('#main-content')).toBeFocused();
});
