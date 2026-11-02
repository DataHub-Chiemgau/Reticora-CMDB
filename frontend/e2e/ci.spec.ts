import { AT, CI_TYPES, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// TST-03 (WP-201): a CI is created in the list, appears there and opens in
// the detail view. Runs in every browser project of playwright.config.ts.
// The dialog offers the types of GET /ci-types and posts the chosen type's
// UUID (CI-03, WP-397). The strict mock checks the POST against
// api/openapi.yaml (WP-530): a type key such as 'server' instead of the UUID
// breaks the contract (format: uuid) and fails the test (F-178).

test.use({ locale: 'de-DE' });

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

interface CIRow {
  id: string;
  name: string;
  status: string;
  ci_type_id: string;
  attributes: Record<string, unknown>;
  is_manual: boolean;
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
          is_manual: true,
          version: 1,
          organization_id: ORG_ID,
          created_at: AT,
          updated_at: AT,
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

  // The POST carries the UUID of the chosen entry of the mocked GET /ci-types.
  const switchType = CI_TYPES.find((t) => t.key === 'switch')!;
  const posts = calls.filter((c) => c.method === 'POST' && c.path === '/cis');
  expect(posts).toHaveLength(1);
  const body = posts[0]!.body as { ci_type_id: string };
  expect(body.ci_type_id).toMatch(UUID);
  expect(CI_TYPES.map((t) => t.id)).toContain(body.ci_type_id);
  expect(body).toMatchObject({
    name: 'core-sw-e2e',
    ci_type_id: switchType.id,
    discovery_source: 'manual',
  });

  await page.getByRole('link', { name: 'core-sw-e2e' }).click();
  await expect(page).toHaveURL(/\/cmdb\/11111111-1111-4111-8111-111111111111$/);
  await expect(page.getByRole('heading', { name: 'core-sw-e2e' })).toBeVisible();
});
