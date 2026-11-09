import type { Page } from '@playwright/test';
import { expect, mockBackend, signIn, test } from './support/backend';
import {
  BIN_A,
  BIN_B,
  P2_ASSET,
  RESERVER,
  TONER,
  mePermissions,
  stockHandlers,
} from './support/phase2';

// TST-03 [P2], UI-15, INV-03 (WP-324): the everyday flows of the warehouse
// on the inventory page (WP-806, WP-807). A consumable is received into a
// bin and moved from bin to bin (POST /stock-movements, the stock per bin of
// GET /consumables/{id}/stock limits the quantity), an asset is reserved for
// a person until a day with an expiry (POST /assets/{id}/reserve), and codes
// are read with a handheld scanner (keyboard wedge: the code and Enter in
// the text field) and typed by hand, resolved by GET /labels/lookup to their
// object. Every write waits behind the undo window of UI-15 before it goes
// out. The camera path of the scan dialog is checked by Vitest only
// (WP-807). The data come from support/phase2.ts; the strict mock checks
// every request and answer against api/openapi.yaml.

test.use({ locale: 'de-DE' });

/** The undo window of UI-15 (UNDO_WINDOW_MS, 6 s) and some room for the request. */
const AFTER_UNDO_WINDOW = { timeout: 15_000 };

/** openFlow opens a flow dialog of the inventory page by its button. */
async function openFlow(page: Page, button: string, title: string) {
  await page.getByRole('button', { name: button, exact: true }).click();
  const dialog = page.getByRole('dialog', { name: title });
  await expect(dialog).toBeVisible();
  return dialog;
}

async function pickConsumable(page: Page, scope: ReturnType<Page['getByRole']>) {
  await scope.getByRole('combobox', { name: /^Mengenartikel/ }).fill('Toner');
  await scope.getByRole('option', { name: new RegExp(TONER.name) }).click();
  await expect(scope.getByRole('list', { name: 'Ausgewählt' })).toContainText(TONER.name);
}

async function chooseBin(scope: ReturnType<Page['getByRole']>, label: string, bin: string) {
  await scope.getByRole('textbox', { name: `${label} filtern` }).fill(bin);
  await scope.getByRole('treeitem', { name: new RegExp(`^${bin}`) }).click();
  await expect(scope.getByRole('treeitem', { name: new RegExp(`^${bin}`) })).toHaveAttribute(
    'aria-selected',
    'true',
  );
}

test.beforeEach(async ({ page }) => {
  // No camera: the scan dialog reads codes from its text field in every browser.
  await page.addInitScript(() =>
    Object.defineProperty(navigator, 'mediaDevices', { value: undefined, configurable: true }),
  );
});

test('receives a consumable into a bin and moves it from bin to bin', async ({ page }) => {
  const stock = stockHandlers();
  const calls = await mockBackend(page, [
    mePermissions(['ci:read', 'stock:read', 'stock:write']),
    ...stock.handlers,
  ]);
  await signIn(page);
  await page.goto('/inventory');
  await expect(page.getByRole('heading', { name: 'Inventar', level: 1 })).toBeVisible();

  // Receive five into bin A-01.
  const receive = await openFlow(page, 'Empfangen', 'Ware empfangen');
  await pickConsumable(page, receive);
  await chooseBin(receive, 'Ziel-Fach', 'Fach A-01');
  await receive.getByRole('spinbutton', { name: 'Menge' }).fill('5');
  await receive.getByLabel('Grund').fill('Lieferung 4711');
  await receive.getByRole('button', { name: 'Empfangen', exact: true }).click();
  await expect(receive).toBeHidden();
  await expect(
    page.getByText(`5 × „${TONER.name} (${TONER.sku})“ wird in Fach A-01 gebucht`),
  ).toBeVisible();
  await expect.poll(() => stock.bookings.length, AFTER_UNDO_WINDOW).toBe(1);
  expect(stock.bookings[0]).toEqual({
    consumable_id: TONER.id,
    movement_type: 'receipt',
    to_location_id: BIN_A,
    quantity: 5,
    reason: 'Lieferung 4711',
  });

  // Move three of them to bin A-02: at most the free stock of the source.
  const move = await openFlow(page, 'Verschieben', 'Bestand verschieben');
  await pickConsumable(page, move);
  const from = move.getByLabel('Quell-Fach');
  await expect(from.locator('option')).toHaveText([
    'Fach wählen',
    'Fach A-01 – Bestand 15, reserviert 2',
    'Fach A-02 – Bestand 4, reserviert 0',
  ]);
  await from.selectOption({ label: 'Fach A-01 – Bestand 15, reserviert 2' });
  await chooseBin(move, 'Ziel-Fach', 'Fach A-02');
  await move.getByRole('spinbutton', { name: 'Menge' }).fill('20');
  await expect(
    move.getByText('Höchstens 13 verfügbar (Bestand abzüglich Reservierungen).'),
  ).toBeVisible();
  await expect(move.getByRole('button', { name: 'Verschieben', exact: true })).toBeDisabled();
  await move.getByRole('spinbutton', { name: 'Menge' }).fill('3');
  await move.getByRole('button', { name: 'Verschieben', exact: true }).click();
  await expect(move).toBeHidden();
  await expect.poll(() => stock.bookings.length, AFTER_UNDO_WINDOW).toBe(2);
  expect(stock.bookings[1]).toEqual({
    consumable_id: TONER.id,
    movement_type: 'transfer',
    from_location_id: BIN_A,
    to_location_id: BIN_B,
    quantity: 3,
  });

  // The journal shows both bookings.
  const journal = page.getByRole('tabpanel');
  await expect(journal.getByText('Lieferung 4711')).toBeVisible();
  await expect(journal.locator('tbody tr')).toHaveCount(2);
  expect(calls.filter((c) => c.path === `/consumables/${TONER.id}/stock`).length).toBeGreaterThan(
    0,
  );
});

test('reserves an asset for a person until a day with an expiry', async ({ page }) => {
  const stock = stockHandlers();
  await mockBackend(page, [mePermissions(['ci:read', 'asset:read']), ...stock.handlers]);
  await signIn(page);
  await page.goto('/inventory');

  const reserve = await openFlow(page, 'Reservieren', 'Asset reservieren');
  await reserve.getByRole('combobox', { name: /^Asset/ }).fill('Notebook');
  await reserve.getByRole('option', { name: new RegExp(P2_ASSET.name) }).click();
  await reserve.getByLabel(/^Reserviert bis/).fill('2026-12-18');
  await reserve.getByLabel('Verfällt am (optional)').fill('2026-12-11');
  await expect(reserve.getByLabel('Reserviert für')).toHaveValue('user');
  await reserve.getByRole('combobox', { name: /^Person/ }).fill('Erika');
  await reserve.getByRole('option', { name: new RegExp(RESERVER.display_name) }).click();
  await reserve.getByLabel('Grund').fill('Einarbeitung');
  await reserve.getByRole('button', { name: 'Reservieren', exact: true }).click();
  await expect(reserve).toBeHidden();
  await expect(
    page.getByText(`„${P2_ASSET.name} (${P2_ASSET.inventory_no})“ wird reserviert`),
  ).toBeVisible();
  await expect.poll(() => stock.reservations.length, AFTER_UNDO_WINDOW).toBe(1);

  // The end of the chosen days in the browser's time zone.
  const endOf = (day: string) => new Date(`${day}T23:59:59`).toISOString();
  expect(stock.reserveRequests).toEqual([
    {
      assetId: P2_ASSET.id,
      body: {
        reserved_until: endOf('2026-12-18'),
        expires_at: endOf('2026-12-11'),
        assignee_id: RESERVER.id,
        reason: 'Einarbeitung',
      },
    },
  ]);

  // The reservation is listed with its end and its expiry.
  await page.getByRole('tab', { name: 'Reservierungen' }).click();
  const row = page.getByRole('tabpanel').locator('tbody tr').filter({ hasText: P2_ASSET.id });
  await expect(row).toContainText('bis 18.12.2026');
  await expect(row.locator('td').nth(3)).toHaveText('11.12.2026');
});

test('reads codes with a handheld scanner and by hand and resolves them to their objects', async ({
  page,
}) => {
  const stock = stockHandlers();
  const calls = await mockBackend(page, [
    mePermissions(['ci:read', 'stock:read', 'stock:write']),
    ...stock.handlers,
  ]);
  await signIn(page);
  await page.goto('/inventory');

  // Handheld scanner inside a flow: the scanner types the bin label and
  // Enter into the text field of the scan dialog; the bin fills the picker.
  const receive = await openFlow(page, 'Empfangen', 'Ware empfangen');
  await pickConsumable(page, receive);
  await receive.getByRole('button', { name: 'Ziel-Fach per Scan wählen' }).click();
  const scan = page.getByRole('dialog', { name: 'Code scannen' });
  const code = scan.getByLabel('Code (Handscanner oder Eingabe)');
  await expect(code).toBeFocused();
  await page.keyboard.type('BIN-A02');
  await page.keyboard.press('Enter');
  await expect(scan).toBeHidden();
  await expect(receive.getByRole('treeitem', { name: /^Fach A-02/ })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  await receive.getByRole('spinbutton', { name: 'Menge' }).fill('2');
  await receive.getByRole('button', { name: 'Empfangen', exact: true }).click();
  await expect.poll(() => stock.bookings.length, AFTER_UNDO_WINDOW).toBe(1);
  expect(stock.bookings[0]).toMatchObject({ to_location_id: BIN_B, quantity: 2 });

  // By hand outside a flow: the code typed and taken over with the button,
  // resolved by the lookup to the consumable with its actions.
  await page.keyboard.press('Control+k');
  await page.getByRole('combobox', { name: 'Befehle durchsuchen' }).fill('Scan');
  await page.getByRole('option', { name: /^Scannen/ }).click();
  const manual = page.getByRole('dialog', { name: 'Code scannen' });
  // Without a camera the dialog says so and takes the code from the field.
  await expect(manual.getByRole('status').first()).toContainText(
    'Code mit dem Handscanner lesen oder eingeben und mit Enter abschließen.',
  );
  await expect(manual.getByLabel('Code (Handscanner oder Eingabe)')).toBeFocused();
  await manual.getByLabel('Code (Handscanner oder Eingabe)').fill(TONER.barcode);
  await manual.getByRole('button', { name: 'Übernehmen' }).click();
  const result = manual.getByRole('region', { name: 'Gescanntes Objekt' });
  await expect(result).toContainText('Mengenartikel');
  await expect(result).toContainText(TONER.name);

  // An unknown code says so, a known one leads to its flow.
  await manual.getByRole('button', { name: 'Weiter scannen' }).click();
  await manual.getByLabel('Code (Handscanner oder Eingabe)').fill('UNBEKANNT-1');
  await page.keyboard.press('Enter');
  await expect(manual.getByText('„UNBEKANNT-1“ nicht gefunden.')).toBeVisible();
  await manual.getByLabel('Code (Handscanner oder Eingabe)').fill(P2_ASSET.barcode);
  await manual.getByRole('button', { name: 'Übernehmen' }).click();
  await expect(manual.getByRole('region', { name: 'Gescanntes Objekt' })).toContainText(
    P2_ASSET.name,
  );
  await manual.getByRole('button', { name: 'Reservieren' }).click();
  const reserve = page.getByRole('dialog', { name: 'Asset reservieren' });
  await expect(reserve.getByRole('list', { name: 'Ausgewählt' })).toContainText(P2_ASSET.name);

  expect(
    calls
      .filter((c) => c.path === '/labels/lookup')
      .map((c) => new URLSearchParams(c.search).get('code')),
  ).toEqual(['BIN-A02', TONER.barcode, 'UNBEKANNT-1', P2_ASSET.barcode]);
});
