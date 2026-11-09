import {
  expect,
  FEATURES,
  list,
  mainNavigation,
  mockBackend,
  ORG_ID,
  problem,
  signIn,
  test,
} from './support/backend';

test('app loads and shows login or dashboard', async ({ page }) => {
  await page.goto('/');
  await expect(page).toHaveTitle(/Reticora/);
});

/**
 * entitlementsWithout answers GET /entitlements for an organization of the
 * plan essential without the given feature (WP-857).
 */
function entitlementsWithout(feature: string) {
  return {
    path: '/entitlements',
    reply: () => ({
      body: list(
        FEATURES.map((feature_key) => ({
          organization_id: ORG_ID,
          feature_key,
          plan: 'essential',
          enabled: feature_key !== feature,
          limits: {},
          source: 'manual',
        })),
      ),
    }),
  };
}

test.describe('locked functions of the plan (WP-857)', () => {
  test.use({ locale: 'de-DE' });

  test('an organization without assets_basic sees the inventory locked and the hint page', async ({
    page,
  }) => {
    // No handler for /assets: the hint page replaces the page, which would
    // only collect 403 errors.
    await mockBackend(page, [entitlementsWithout('assets_basic')]);
    await signIn(page);
    await page.goto('/assets');

    await expect(page.getByRole('heading', { level: 1, name: 'Inventar' })).toBeVisible();
    await expect(page.getByText('„Inventar“ gehört nicht zu Ihrem Paket')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Zum Dashboard' })).toHaveAttribute(
      'href',
      '/dashboard',
    );

    // On narrow viewports the navigation is a drawer that opens first.
    const nav = await mainNavigation(page);
    // The group Assets is closed by default; a locked page keeps it.
    await nav.getByRole('button', { name: 'Assets', exact: true }).click();
    const locked = nav.getByRole('link', { name: 'Inventar', exact: true });
    await expect(locked).toHaveAttribute('aria-disabled', 'true');
    await expect(locked).toHaveAccessibleDescription('Nicht im Paket Essential enthalten');
    // Its neighbours of the plan stay usable.
    await expect(nav.getByRole('link', { name: 'Dokumente', exact: true })).not.toHaveAttribute(
      'aria-disabled',
    );
  });

  // A client-scoped technician (role client_technician) lacks
  // entitlement:read: GET /entitlements and GET /modules answer 403. The
  // plan is then unknown and the page stays reachable (the server answers
  // its own 403 where needed); it must neither wait forever nor flip
  // between the page and the loading state after a reload (WP-857 regression
  // of e2e-real CS01/TP05).
  test('a gated page stays when the plan cannot be read (403 on /entitlements)', async ({
    page,
  }) => {
    const calls = await mockBackend(page, [
      {
        path: '/entitlements',
        reply: () => ({ status: 403, body: problem(403, 'missing permission entitlement:read') }),
      },
      {
        path: '/modules',
        reply: () => ({ status: 403, body: problem(403, 'missing permission entitlement:read') }),
      },
      {
        path: '/me/permissions',
        reply: () => ({
          body: {
            user_id: '00000000-0000-0000-0000-000000000001',
            permissions: ['ci:read', 'relationship:read', 'topology:read'],
          },
        }),
      },
      { path: '/topology', reply: () => ({ body: { nodes: [], edges: [] } }) },
      { path: '/relationship-types', reply: () => ({ body: list([]) }) },
    ]);
    await signIn(page, { name: 'Technikerin' });
    await page.goto('/topology');
    const heading = page.getByRole('heading', { level: 1, name: 'Topologie' });
    await expect(heading).toBeVisible();

    // The direct call after a reload: the page comes and stays.
    await page.reload();
    await expect(heading).toBeVisible();
    const entitlementReads = () => calls.filter((c) => c.path === '/entitlements').length;
    const afterReload = entitlementReads();
    await page.waitForTimeout(3_000);
    await expect(heading).toBeVisible();
    // No refetch loop: a refused read is not repeated by every mounting part.
    expect(entitlementReads()).toBe(afterReload);
    await expect(page.getByText('gehört nicht zu Ihrem Paket')).toHaveCount(0);
  });
});
