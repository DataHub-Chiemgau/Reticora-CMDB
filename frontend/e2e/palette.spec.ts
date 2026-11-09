import { expect, FEATURES, list, mockBackend, ORG_ID, signIn, test } from './support/backend';

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
    // The topology page labels its relationship filter from the types (WP-818).
    { path: '/relationship-types', reply: () => ({ body: list([]) }) },
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

// The palette offers the pages the navigation shows (navPageState, WP-857):
// a page locked by the plan is shown locked in the sidebar, the palette
// does not offer it.
test('does not offer a page locked by the plan', async ({ page }) => {
  await mockBackend(page, [
    { path: '/cis', reply: () => ({ body: list([]) }) },
    { path: '/search', reply: () => ({ body: list([]) }) },
    {
      path: '/entitlements',
      reply: () => ({
        body: list(
          FEATURES.map((feature_key) => ({
            organization_id: ORG_ID,
            feature_key,
            plan: 'essential',
            enabled: feature_key !== 'assets_basic',
            limits: {},
            source: 'manual',
          })),
        ),
      }),
    },
  ]);
  await signIn(page);
  await page.goto('/cmdb');
  await expect(page.locator('#main-content')).toBeVisible();
  // The plan has loaded: the inventory is locked in the navigation.
  await expect(page.locator('a[href="/assets"][aria-disabled="true"]')).toHaveCount(1);

  await page.keyboard.press('Control+k');
  const search = page.getByRole('combobox', { name: 'Befehle durchsuchen' });
  await expect(search).toBeVisible();
  await search.fill('Dokumente');
  await expect(page.getByRole('option', { name: 'Dokumente öffnen' })).toBeVisible();
  await search.fill('Inventar öffnen');
  await expect(page.getByRole('option', { name: 'Inventar öffnen' })).toHaveCount(0);
});
