import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// TST-03 (WP-201): placing objects by drag and drop (and the keyboard
// alternative) in the room plan, and undoing a delete within the undo
// window. Runs in every browser project; the drag uses HTML5 drag events
// with a DataTransfer, which every engine (including the mobile emulations)
// dispatches the same way. The PATCH bodies are checked field by field and
// against api/openapi.yaml (WP-530).

test.use({ locale: 'de-DE' });

const SITE = '33333333-3333-4333-8333-000000000001';
const BUILDING = '33333333-3333-4333-8333-000000000002';
const ROOM = '33333333-3333-4333-8333-000000000003';
const CI = {
  id: '11111111-1111-4111-8111-0000000000c1',
  organization_id: ORG_ID,
  ci_type_id: '5e1b0c3a-7d2f-4a10-9c01-000000000001',
  name: 'Rack A1',
  status: 'active',
  attributes: {},
  is_manual: true,
  created_at: AT,
  updated_at: AT,
};

type Layout = Record<string, { x: number; y: number }>;

test('drags a rack in the room plan and moves it by keyboard', async ({ page }) => {
  let layout: Layout = {};
  const room = () => ({
    id: ROOM,
    organization_id: ORG_ID,
    building_id: BUILDING,
    name: 'Serverraum 1',
    layout,
    created_at: AT,
    updated_at: AT,
  });
  const calls = await mockBackend(page, [
    {
      path: '/buildings',
      reply: () => ({
        body: list([
          {
            id: BUILDING,
            organization_id: ORG_ID,
            site_id: SITE,
            name: 'Haus Nord',
            created_at: AT,
            updated_at: AT,
          },
        ]),
      }),
    },
    { path: '/rooms', reply: () => ({ body: list([room()]) }) },
    { path: `/rooms/${ROOM}`, reply: () => ({ body: room() }) },
    {
      method: 'PATCH',
      path: `/rooms/${ROOM}`,
      reply: (request) => {
        layout = (request.postDataJSON() as { layout: Layout }).layout;
        return { body: room() };
      },
    },
    { path: '/cis', reply: () => ({ body: list([CI]) }) },
  ]);
  await signIn(page);
  await page.goto('/roomplan');
  await page.getByLabel('Gebäude').selectOption(BUILDING);
  await page.getByLabel('Raum').selectOption(ROOM);

  const marker = page.getByRole('button', { name: /^Rack A1, Position/ });
  await expect(marker).toBeVisible();
  const plan = page.getByTestId('room-plan');
  const box = (await plan.boundingBox())!;

  // Drop at a quarter of the width and three quarters of the height.
  const dataTransfer = await page.evaluateHandle(() => new DataTransfer());
  await marker.dispatchEvent('dragstart', { dataTransfer });
  await plan.dispatchEvent('dragover', { dataTransfer });
  await plan.dispatchEvent('drop', {
    dataTransfer,
    clientX: box.x + box.width * 0.25,
    clientY: box.y + box.height * 0.75,
  });
  await expect(
    page.getByRole('button', { name: 'Rack A1, Position 25 % von links, 75 % von oben' }),
  ).toBeVisible();
  // The marker moves at once; the PATCH follows (WP-526: local position).
  // It carries the layout only: the dropped CI with its relative position.
  const patches = () =>
    calls
      .filter((c) => c.method === 'PATCH' && c.path === `/rooms/${ROOM}`)
      .map((c) => c.body as { layout: Layout });
  await expect.poll(() => patches().length).toBe(1);
  const first = patches()[0]!;
  expect(Object.keys(first)).toEqual(['layout']);
  expect(Object.keys(first.layout)).toEqual([CI.id]);
  expect(first.layout[CI.id]!.x).toBeCloseTo(0.25, 1);
  expect(first.layout[CI.id]!.y).toBeCloseTo(0.75, 1);

  // Keyboard alternative: Shift+ArrowRight moves by ten percent.
  const moved = page.getByRole('button', { name: /^Rack A1, Position 25 %/ });
  await moved.focus();
  await moved.press('Shift+ArrowRight');
  await expect(
    page.getByRole('button', { name: 'Rack A1, Position 35 % von links, 75 % von oben' }),
  ).toBeVisible();
  // The debounced PATCH stores the step.
  await expect.poll(() => patches().length).toBe(2);
  const second = patches()[1]!;
  expect(Object.keys(second.layout)).toEqual([CI.id]);
  expect(second.layout[CI.id]!.x).toBeCloseTo(0.35, 2);
  expect(second.layout[CI.id]!.y).toBeCloseTo(first.layout[CI.id]!.y, 5);
});

test('undoes a delete within the undo window', async ({ page }) => {
  const asset = {
    id: '44444444-4444-4444-8444-000000000001',
    asset_tag: 'INV-0001',
    name: 'Laptop E2E',
    category: 'laptop',
    status: 'in_use',
    custom_fields: {},
    organization_id: ORG_ID,
    created_at: AT,
    updated_at: AT,
  };
  const calls = await mockBackend(page, [
    { path: '/assets', reply: () => ({ body: list([asset]) }) },
  ]);
  await signIn(page);
  await page.goto('/assets');

  await page
    .getByRole('row', { name: /Laptop E2E/ })
    .getByRole('button', { name: 'Löschen' })
    .click();
  await expect(page.getByText('„Laptop E2E“ wird gelöscht')).toBeVisible();
  await page.getByRole('button', { name: 'Rückgängig' }).click();
  await expect(page.getByText('„Laptop E2E“ wird gelöscht')).toBeHidden();

  // The undo window (6 s) passes without the DELETE request.
  await page.waitForTimeout(6_500);
  expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(0);
  await expect(page.getByRole('row', { name: /Laptop E2E/ })).toBeVisible();
});
