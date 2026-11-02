import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// Regression of the maintenance form in a real browser (WP-540, found by
// e2e-real/maintenance.spec.ts M1): the Beginn/Ende inputs are
// datetime-local fields, which only accept the local "YYYY-MM-DDTHH:mm"
// form. The form keeps that local value and converts it to an ISO time
// only for the POST body; a value the browser rejects (an ISO string with
// "Z") would empty the field and keep "Anlegen" disabled. jsdom does not
// validate the value, so only a browser test catches this.

test.use({ locale: 'de-DE', timezoneId: 'Europe/Berlin' });

const WEB_01 = '22222222-2222-4222-8222-000000000001';
const WEB_02 = '22222222-2222-4222-8222-000000000002';

function ci(id: string, name: string) {
  return {
    id,
    organization_id: ORG_ID,
    ci_type_id: '5e1b0c3a-7d2f-4a10-9c01-000000000001',
    name,
    status: 'active',
    attributes: {},
    is_manual: true,
    version: 1,
    created_at: AT,
    updated_at: AT,
  };
}

const CIS = [ci(WEB_01, 'web-01'), ci(WEB_02, 'web-02')];

test('creates a maintenance window with local start/end times and CIs', async ({ page }) => {
  const windows: Record<string, unknown>[] = [];
  const calls = await mockBackend(page, [
    { path: '/maintenance-windows', reply: () => ({ body: list(windows) }) },
    {
      method: 'POST',
      path: '/maintenance-windows',
      reply: (request) => {
        const body = request.postDataJSON() as Record<string, unknown>;
        const w = {
          id: '33333333-3333-4333-8333-000000000001',
          organization_id: ORG_ID,
          status: 'scheduled',
          created_at: AT,
          updated_at: AT,
          ...body,
        };
        windows.push(w);
        return { status: 201, body: w };
      },
    },
    {
      path: '/cis',
      reply: (_request, url) => {
        const search = url.searchParams.get('search') ?? '';
        return { body: list(CIS.filter((c) => c.name.includes(search))) };
      },
    },
  ]);
  await signIn(page);
  await page.goto('/maintenance');

  await page.getByRole('button', { name: 'Wartung planen' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Titel').fill('Kernel-Update');
  const starts = dialog.getByLabel('Beginn');
  const ends = dialog.getByLabel('Ende');
  await starts.fill('2026-11-03T22:18');
  await ends.fill('2026-11-04T01:30');
  // The browser keeps both values: the form state is a valid
  // datetime-local value, not an ISO string the field rejects.
  await expect(starts).toHaveValue('2026-11-03T22:18');
  await expect(ends).toHaveValue('2026-11-04T01:30');

  await dialog.getByLabel('Betroffene CIs suchen').fill('web');
  await dialog.getByRole('button', { name: 'web-01 hinzufügen' }).click();
  await dialog.getByRole('button', { name: 'web-02 hinzufügen' }).click();
  await expect(starts).toHaveValue('2026-11-03T22:18');
  await expect(ends).toHaveValue('2026-11-04T01:30');

  const create = dialog.getByRole('button', { name: 'Anlegen' });
  await expect(create).toBeEnabled();
  await create.click();
  await expect(dialog).toBeHidden();
  await expect(page.getByText('Kernel-Update')).toBeVisible();

  // Europe/Berlin is UTC+1 in November: the POST carries the UTC instants.
  const posts = calls.filter((c) => c.method === 'POST' && c.path === '/maintenance-windows');
  expect(posts.map((c) => c.body)).toEqual([
    {
      title: 'Kernel-Update',
      starts_at: '2026-11-03T21:18:00.000Z',
      ends_at: '2026-11-04T00:30:00.000Z',
      ci_ids: [WEB_01, WEB_02],
    },
  ]);
});
