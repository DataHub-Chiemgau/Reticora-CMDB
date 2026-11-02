import { AT, expect, list, mockBackend, signIn, test } from './support/backend';

// TST-03 (WP-201): the topology draws the graph, simulates the failure of
// two CIs together (IMP-05) and focuses a CI. Runs in every browser
// project. The answers follow api/openapi.yaml (WP-530).

test.use({ locale: 'de-DE' });

const SWITCH = '11111111-1111-4111-8111-0000000000a1';
const UPS = '11111111-1111-4111-8111-0000000000a2';
const SERVER = '11111111-1111-4111-8111-0000000000a3';

const graph = {
  nodes: [
    { id: SWITCH, name: 'core-sw-01', ci_type: 'switch', status: 'active' },
    { id: UPS, name: 'ups-01', ci_type: 'ups', status: 'active' },
    { id: SERVER, name: 'srv-01', ci_type: 'server', status: 'active' },
  ],
  edges: [
    { id: 'e-1', source_ci_id: SWITCH, target_ci_id: SERVER, rel_type: 'connected_to' },
    { id: 'e-2', source_ci_id: SERVER, target_ci_id: UPS, rel_type: 'powered_by' },
  ],
};

function relationshipType(
  key: string,
  category: string,
  impact_direction: string,
  directed: boolean,
) {
  return {
    id: `22222222-2222-4222-8222-${key === 'powered_by' ? '000000000001' : '000000000002'}`,
    key,
    display_name: key,
    reverse_name: key,
    directed,
    category,
    impact_direction,
    redundant: key === 'powered_by',
    allowed_source_types: [],
    allowed_target_types: [],
    cardinality: 'many_to_many',
    is_system: true,
    created_at: AT,
    updated_at: AT,
  };
}

test('simulates a joint failure of two CIs and focuses a CI', async ({ page }) => {
  const calls = await mockBackend(page, [
    { path: '/topology', reply: () => ({ body: graph }) },
    { path: '/ci-types', reply: () => ({ body: list([]) }) },
    {
      path: '/relationship-types',
      reply: () => ({
        body: list([
          relationshipType('connected_to', 'network', 'undirected', false),
          relationshipType('powered_by', 'power', 'source_depends_on_target', true),
        ]),
      }),
    },
    {
      path: '/impact',
      reply: (_req, url) => {
        const failed = (url.searchParams.get('ci_ids') ?? '').split(',');
        // Only the joint failure of the switch and the UPS takes the server
        // down; it hangs on the failed UPS.
        const impacted =
          failed.length > 1
            ? [
                {
                  ...graph.nodes[2]!,
                  heuristic: false,
                  hops: 1,
                  via: { ci_id: UPS, rel_type: 'powered_by' },
                  path: [
                    {
                      from_ci_id: UPS,
                      to_ci_id: SERVER,
                      rel_type: 'powered_by',
                      category: 'power',
                    },
                  ],
                },
              ]
            : [];
        return {
          body: {
            failed_cis: graph.nodes.filter((n) => failed.includes(n.id)),
            impacted,
            count: impacted.length,
            summary: {
              failed: failed.length,
              impacted: impacted.length,
              certain: impacted.length,
              heuristic: 0,
              by_category: impacted.length ? { power: impacted.length } : {},
            },
          },
        };
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
  // The second failure asks the server for the joint impact of both CIs.
  await expect
    .poll(() =>
      calls
        .filter((c) => c.path === '/impact')
        .map((c) => new URLSearchParams(c.search).get('ci_ids')),
    )
    .toContain(`${SWITCH},${UPS}`);

  await page.getByRole('button', { name: 'Simulation beenden' }).first().click();
  await expect(page.getByText('Ausfallsimulation')).toBeHidden();

  await page
    .locator('li', { hasText: 'srv-01' })
    .getByRole('button', { name: 'Fokussieren' })
    .click();
  await expect(page).toHaveURL(new RegExp(`root=${SERVER}`));
  await expect
    .poll(() =>
      calls.some(
        (c) => c.path === '/topology' && new URLSearchParams(c.search).get('root_ci_id') === SERVER,
      ),
    )
    .toBe(true);
});
