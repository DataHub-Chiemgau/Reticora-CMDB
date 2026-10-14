import { expect, test } from '@playwright/test';
import { list, mockBackend, signIn } from './support/backend';

// TST-03 (WP-201): a review item is compared with its CI and merged; two
// items are dismissed together. Runs in every browser project.

test.use({ locale: 'de-DE' });

const CI = '11111111-1111-4111-8111-111111111111';

test('compares, merges and dismisses review items', async ({ page }) => {
  const items = [
    {
      id: 'rev-1',
      kind: 'ambiguous_identity',
      status: 'open',
      payload: { name: 'sw-core-01', serial_number: 'SN-NEW' },
      candidate_ci_ids: [CI],
      escalated: false,
      created_at: '2026-10-14T08:00:00Z',
    },
    {
      id: 'rev-2',
      kind: 'duplicate_ip',
      status: 'open',
      payload: { management_ip: '10.0.0.9' },
      escalated: false,
      created_at: '2026-10-14T08:00:00Z',
    },
    {
      id: 'rev-3',
      kind: 'serial_mismatch',
      status: 'open',
      payload: { name: 'srv-07' },
      escalated: false,
      created_at: '2026-10-14T08:00:00Z',
    },
  ];
  const calls = await mockBackend(page, [
    {
      path: '/review-items',
      reply: () => ({ body: list(items.filter((i) => i.status === 'open')) }),
    },
    {
      method: 'POST',
      path: /^\/review-items\/[^/]+\/resolve$/,
      reply: (_req, url) => {
        const item = items.find((i) => url.pathname.includes(`/${i.id}/`))!;
        item.status = 'resolved';
        return { body: item };
      },
    },
    {
      path: `/cis/${CI}`,
      reply: () => ({
        body: {
          id: CI,
          name: 'sw-core-01',
          serial_number: 'SN-OLD',
          status: 'active',
          attributes: {},
        },
      }),
    },
  ]);
  await signIn(page);
  await page.goto('/reviews');

  await page.getByRole('button', { name: /sw-core-01/ }).click();
  const diff = page.getByRole('table', { name: 'Vergleich von Beobachtung und CI' });
  await expect(diff.getByText('SN-OLD')).toBeVisible();
  await expect(diff.getByText('abweichend')).toBeVisible();
  await page.getByRole('button', { name: 'Zusammenführen' }).click();
  await expect(page.getByRole('button', { name: /sw-core-01/ })).toBeHidden();
  expect(calls.find((c) => c.path === '/review-items/rev-1/resolve')?.body).toEqual({
    action: 'merge',
    ci_id: CI,
  });

  await page.getByLabel('Alle offenen auswählen').check();
  await page.getByLabel('Aktion', { exact: true }).selectOption('dismiss');
  await page.getByRole('button', { name: 'Ausführen' }).click();
  await expect(page.getByText('2 erledigt, 0 fehlgeschlagen.')).toBeVisible();
  await expect(page.getByText('Keine Review-Items')).toBeVisible();
});
