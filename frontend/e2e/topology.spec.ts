import { expect, test } from '@playwright/test';
import { list, mockBackend, signIn } from './support/backend';

// TST-03 (WP-201): the topology draws the graph, simulates the failure of
// several CIs together (IMP-05) and focuses a CI. Runs in every browser
// project.

test.use({ locale: 'de-DE' });

const graph = {
  nodes: [
    { id: 'ci-1', name: 'core-sw-01', ci_type: 'switch', status: 'active' },
    { id: 'ci-2', name: 'ups-01', ci_type: 'ups', status: 'active' },
    { id: 'ci-3', name: 'srv-01', ci_type: 'server', status: 'active' },
  ],
  edges: [
    { id: 'e-1', source_ci_id: 'ci-1', target_ci_id: 'ci-3', rel_type: 'connected_to' },
    { id: 'e-2', source_ci_id: 'ci-2', target_ci_id: 'ci-3', rel_type: 'powered_by' },
  ],
};

test('simulates a joint failure and focuses a CI', async ({ page }) => {
  const calls = await mockBackend(page, [
    { path: '/topology', reply: () => ({ body: graph }) },
    { path: '/ci-types', reply: () => ({ body: list([]) }) },
    {
      path: '/impact',
      reply: (_req, url) => {
        const failed = (url.searchParams.get('ci_ids') ?? '').split(',');
        const impacted = failed.length > 1 ? [graph.nodes[2]] : [];
        return { body: { failed_cis: [], impacted, count: impacted.length } };
      },
    },
  ]);
  await signIn(page);
  await page.goto('/topology');

  const nodeList = page.locator('li', { hasText: 'core-sw-01' });
  await expect(nodeList).toBeVisible();
  await nodeList.getByRole('button', { name: 'Ausfall simulieren' }).click();
  await expect(page.getByText('Ausfallsimulation')).toBeVisible();

  await page
    .locator('li', { hasText: 'ups-01' })
    .getByRole('button', { name: 'Ausfall hinzufügen' })
    .click();
  await expect(
    page.getByText(
      /Fallen 2 CIs \(core-sw-01, ups-01\) gemeinsam aus, sind 1 weitere CIs betroffen/,
    ),
  ).toBeVisible();
  expect(calls.some((c) => c.path === '/impact' && c.search.includes('ci_ids=ci-1%2Cci-2'))).toBe(
    true,
  );

  await page.getByRole('button', { name: 'Simulation beenden' }).first().click();
  await expect(page.getByText('Ausfallsimulation')).toBeHidden();

  await page
    .locator('li', { hasText: 'srv-01' })
    .getByRole('button', { name: 'Fokussieren' })
    .click();
  await expect(page).toHaveURL(/root=ci-3/);
  await expect
    .poll(() => calls.some((c) => c.path === '/topology' && c.search.includes('root_ci_id=ci-3')))
    .toBe(true);
});
