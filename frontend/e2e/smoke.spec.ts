import { expect, FEATURES, list, mockBackend, ORG_ID, signIn, test } from './support/backend';

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

    const nav = page.getByRole('navigation', { name: 'Hauptnavigation' });
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
});
