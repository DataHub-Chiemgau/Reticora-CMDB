import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// TST-03 (WP-201): a review item is compared with its CI candidates and
// merged into the candidate the user chose; two items are dismissed
// together. Runs in every browser project.

test.use({ locale: 'de-DE' });

const FIRST = '11111111-1111-4111-8111-111111111111';
const CHOSEN = '11111111-1111-4111-8111-222222222222';

function ci(id: string, name: string, serial: string) {
  return {
    id,
    organization_id: ORG_ID,
    ci_type_id: '5e1b0c3a-7d2f-4a10-9c01-000000000002',
    name,
    serial_number: serial,
    status: 'active',
    attributes: {},
    is_manual: false,
    version: 1,
    created_at: AT,
    updated_at: AT,
  };
}

const CIS = [ci(FIRST, 'sw-core-01', 'SN-OLD'), ci(CHOSEN, 'sw-core-01b', 'SN-OTHER')];

test('compares, merges into a chosen candidate and dismisses review items', async ({ page }) => {
  const item = (id: string, kind: string, payload: Record<string, string>, extra = {}) => ({
    id,
    organization_id: ORG_ID,
    kind,
    status: 'open',
    payload,
    escalated: false,
    created_at: AT,
    updated_at: AT,
    ...extra,
  });
  const items = [
    item(
      'rev-1',
      'ambiguous_identity',
      { name: 'sw-core-01', serial_number: 'SN-NEW' },
      { candidate_ci_ids: [FIRST, CHOSEN] },
    ),
    item('rev-2', 'duplicate_ip', { management_ip: '10.0.0.9' }),
    item('rev-3', 'serial_mismatch', { name: 'srv-07' }),
  ];
  const calls = await mockBackend(page, [
    {
      path: '/review-items',
      reply: () => ({ body: list(items.filter((i) => i.status === 'open')) }),
    },
    {
      method: 'POST',
      path: /^\/review-items\/[^/]+\/resolve$/,
      reply: (request, url) => {
        const target = items.find((i) => url.pathname.includes(`/${i.id}/`))!;
        const { action } = request.postDataJSON() as { action: string };
        target.status = action === 'dismiss' ? 'dismissed' : 'resolved';
        return { body: { ...target, action } };
      },
    },
    {
      path: /^\/cis\/[^/]+$/,
      reply: (_req, url) => ({ body: CIS.find((c) => url.pathname.endsWith(`/${c.id}`)) }),
    },
  ]);
  await signIn(page);
  await page.goto('/reviews');

  await page.getByRole('button', { name: /sw-core-01/ }).click();
  const diff = page.getByRole('table', { name: 'Vergleich von Beobachtung und CI' });
  // The first candidate is compared first; the user picks the second one.
  await expect(diff.getByText('SN-OLD')).toBeVisible();
  await page.getByLabel('Kandidat').selectOption(CHOSEN);
  await expect(diff.getByText('SN-OTHER')).toBeVisible();
  await expect(diff.getByRole('columnheader', { name: 'sw-core-01b' })).toBeVisible();
  await expect(
    diff.getByRole('row', { name: /serial_number/ }).getByText('abweichend'),
  ).toBeVisible();
  await page.getByRole('button', { name: 'Zusammenführen' }).click();
  await expect(page.getByRole('button', { name: /sw-core-01/ })).toBeHidden();
  expect(calls.filter((c) => c.path === '/review-items/rev-1/resolve').map((c) => c.body)).toEqual([
    { action: 'merge', ci_id: CHOSEN },
  ]);

  await page.getByLabel('Alle offenen auswählen').check();
  await page.getByLabel('Aktion', { exact: true }).selectOption('dismiss');
  await page.getByRole('button', { name: 'Ausführen' }).click();
  await expect(page.getByText('2 erledigt, 0 fehlgeschlagen.')).toBeVisible();
  await expect(page.getByText('Keine Review-Items')).toBeVisible();
  for (const id of ['rev-2', 'rev-3']) {
    expect(calls.find((c) => c.path === `/review-items/${id}/resolve`)?.body).toEqual({
      action: 'dismiss',
    });
  }
});
