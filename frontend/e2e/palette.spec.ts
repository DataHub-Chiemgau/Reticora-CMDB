import { expect, list, mockBackend, signIn, test } from './support/backend';

// TST-03 (WP-201): the command palette opens with Ctrl+K (Meta+K on macOS),
// filters by keyboard input and navigates with Enter. Runs in every browser
// project.

test.use({ locale: 'de-DE' });

test('navigates with the command palette', async ({ page }) => {
  await mockBackend(page, [
    { path: '/cis', reply: () => ({ body: list([]) }) },
    // The palette also searches the CMDB for its input.
    { path: '/search', reply: () => ({ body: list([]) }) },
    { path: '/topology', reply: () => ({ body: { nodes: [], edges: [] } }) },
  ]);
  await signIn(page);
  await page.goto('/cmdb');
  await expect(page.locator('#main-content')).toBeVisible();

  await page.keyboard.press('Control+k');
  const search = page.getByRole('combobox', { name: 'Befehle durchsuchen' });
  await expect(search).toBeVisible();
  await search.fill('topolog');
  await expect(page.getByRole('option', { name: 'Topologie öffnen' })).toBeVisible();
  await search.press('Enter');
  await expect(page).toHaveURL(/\/topology$/);
  await expect(search).toBeHidden();

  // Escape closes the palette without navigating.
  await page.keyboard.press('Control+k');
  await expect(search).toBeVisible();
  await search.press('Escape');
  await expect(search).toBeHidden();
  await expect(page).toHaveURL(/\/topology$/);
});
