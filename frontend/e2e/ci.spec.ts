import { expect, test } from '@playwright/test';
import { list, mockBackend, signIn } from './support/backend';

// TST-03 (WP-201): a CI is created in the list, appears there and opens in
// the detail view. Runs in every browser project of playwright.config.ts.

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
  await dialog.getByLabel('CI-Typ').selectOption({ index: 1 });
  await dialog.getByRole('button', { name: 'Anlegen' }).click();
  await expect(dialog).toBeHidden();

  const post = calls.find((c) => c.method === 'POST' && c.path === '/cis');
  expect(post?.body).toMatchObject({ name: 'core-sw-e2e', discovery_source: 'manual' });

  await page.getByRole('link', { name: 'core-sw-e2e' }).click();
  await expect(page).toHaveURL(/\/cmdb\/11111111-1111-4111-8111-111111111111$/);
  await expect(page.getByRole('heading', { name: 'core-sw-e2e' })).toBeVisible();
});
