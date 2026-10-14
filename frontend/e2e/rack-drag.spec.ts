import { expect, test } from '@playwright/test';
import { list, mockBackend, signIn } from './support/backend';

// TST-03 (WP-201): placing objects by drag and drop (and the keyboard
// alternative) in the room plan, and undoing a delete within the undo
// window. Runs in every browser project; the drag uses HTML5 drag events
// with a DataTransfer, which every engine (including the mobile emulations)
// dispatches the same way.

test.use({ locale: 'de-DE' });

const ROOM = 'room-1';
const CI = {
  id: 'ci-rack-1',
  name: 'Rack A1',
  status: 'active',
  attributes: {},
  ci_type_id: 'rack',
};

test('drags a rack in the room plan and moves it by keyboard', async ({ page }) => {
  let layout: Record<string, { x: number; y: number }> = {};
  const calls = await mockBackend(page, [
    { path: '/buildings', reply: () => ({ body: list([{ id: 'b-1', name: 'Haus Nord' }]) }) },
    {
      path: '/rooms',
      reply: () => ({ body: list([{ id: ROOM, name: 'Serverraum 1', building_id: 'b-1' }]) }),
    },
    {
      path: `/rooms/${ROOM}`,
      reply: () => ({ body: { id: ROOM, name: 'Serverraum 1', building_id: 'b-1', layout } }),
    },
    {
      method: 'PATCH',
      path: `/rooms/${ROOM}`,
      reply: (request) => {
        layout = (request.postDataJSON() as { layout: typeof layout }).layout;
        return { body: { id: ROOM, name: 'Serverraum 1', building_id: 'b-1', layout } };
      },
    },
    { path: '/cis', reply: () => ({ body: list([CI]) }) },
  ]);
  await signIn(page);
  await page.goto('/roomplan');
  await page.getByLabel('Gebäude').selectOption('b-1');
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
  expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(1);
  expect(layout[CI.id]?.x).toBeCloseTo(0.25, 1);
  expect(layout[CI.id]?.y).toBeCloseTo(0.75, 1);

  // Keyboard alternative: Shift+ArrowRight moves by ten percent.
  const moved = page.getByRole('button', { name: /^Rack A1, Position 25 %/ });
  await moved.focus();
  await moved.press('Shift+ArrowRight');
  await expect(
    page.getByRole('button', { name: 'Rack A1, Position 35 % von links, 75 % von oben' }),
  ).toBeVisible();
});

test('undoes a delete within the undo window', async ({ page }) => {
  const asset = {
    id: 'asset-1',
    asset_tag: 'INV-0001',
    name: 'Laptop E2E',
    category: 'laptop',
    status: 'in_use',
    organization_id: 'org-1',
    created_at: '2026-10-14T08:00:00Z',
    updated_at: '2026-10-14T08:00:00Z',
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
