import { expect, test } from '@playwright/test';
import { CI_TYPES, list, mockBackend, signIn } from './support/backend';

// TST-03 (WP-201): a CI is created in the list, appears there and opens in
// the detail view. Runs in every browser project of playwright.config.ts.
// The dialog offers the types of GET /ci-types and posts the chosen type's
// UUID; the mock refuses a type key with 422 like the server (CI-03, WP-397).

test.use({ locale: 'de-DE' });

interface CIRow {
  id: string;
  name: string;
  status: string;
  ci_type_id: string;
  attributes: Record<string, unknown>;
  version: number;
  organization_id: string;
  created_at: string;
  updated_at: string;
}

test('creates a CI and opens it', async ({ page }) => {
  const cis: CIRow[] = [];
  const calls = await mockBackend(page, [
    { path: '/cis', reply: () => ({ body: list(cis) }) },
    {
      method: 'POST',
      path: '/cis',
      reply: (request) => {
        const input = request.postDataJSON() as {
          name: string;
          ci_type_id: string;
          status: string;
        };
        const ci: CIRow = {
          id: '11111111-1111-4111-8111-111111111111',
          name: input.name,
          status: input.status,
          ci_type_id: input.ci_type_id,
          attributes: {},
          version: 1,
          organization_id: 'org-1',
          created_at: '2026-10-14T08:00:00Z',
          updated_at: '2026-10-14T08:00:00Z',
        };
        cis.push(ci);
        return { status: 201, body: ci };
      },
    },
    // The detail page reads the neighbors of the CI as a graph.
    {
      path: /^\/topology\/cis\/[^/]+\/neighbors$/,
      reply: () => ({ body: { nodes: [], edges: [] } }),
    },
    {
      path: /^\/cis\/[^/]+$/,
      reply: (_req, url) => ({ body: cis.find((c) => url.pathname.endsWith(c.id)) }),
    },
  ]);
  await signIn(page);
  await page.goto('/cmdb');

  await page.getByRole('button', { name: 'CI erstellen' }).first().click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill('core-sw-e2e');
  const typeSelect = dialog.getByLabel('CI-Typ');
  // System types and the organization type, labelled in the UI language.
  await expect(typeSelect.locator('option', { hasText: 'Org-PDU' })).toHaveCount(1);
  await typeSelect.selectOption({ label: 'Switch' });
  await dialog.getByRole('button', { name: 'Anlegen' }).click();
  await expect(dialog).toBeHidden();

  const switchType = CI_TYPES.find((t) => t.key === 'switch')!;
  const post = calls.find((c) => c.method === 'POST' && c.path === '/cis');
  expect(post?.body).toMatchObject({
    name: 'core-sw-e2e',
    ci_type_id: switchType.id,
    discovery_source: 'manual',
  });

  await page.getByRole('link', { name: 'core-sw-e2e' }).click();
  await expect(page).toHaveURL(/\/cmdb\/11111111-1111-4111-8111-111111111111$/);
  await expect(page.getByRole('heading', { name: 'core-sw-e2e' })).toBeVisible();
});

test('the mocked backend refuses a type key as ci_type_id like the server', async ({ page }) => {
  const calls = await mockBackend(page, [
    { method: 'POST', path: '/cis', reply: () => ({ status: 201, body: {} }) },
  ]);
  await signIn(page);
  await page.goto('/cmdb');
  const statuses = await page.evaluate(async () => {
    const post = (ciTypeId: string) =>
      fetch('/api/v1/cis', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: 'key-ci', ci_type_id: ciTypeId }),
      }).then(async (r) => ({ status: r.status, body: await r.json() }));
    return [await post('server'), await post('5e1b0c3a-7d2f-4a10-9c01-000000000001')];
  });
  expect(statuses[0]).toMatchObject({
    status: 422,
    body: { violations: [{ field: 'ci_type_id' }] },
  });
  expect(statuses[1]?.status).toBe(201);
  expect(calls.filter((c) => c.method === 'POST' && c.path === '/cis')).toHaveLength(2);
});
