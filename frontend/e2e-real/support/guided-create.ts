import { expect, type Locator, type Page } from '@playwright/test';

// The guided creation of a CI on /cmdb (WP-317, MET-51, UI-12), shared by the
// real-stack specs that create a CI through the UI. "CI anlegen" opens the
// dialog on the type tiles; "Weiter" leads to manufacturer and model (not for
// logical types) and then to the fields of the resolved form of the type
// (GET /ci-types/{id}/form) with the master fields name and status. The type
// is required: "Weiter" stays disabled until a tile is chosen. The mocked
// counterpart is frontend/e2e/guided-create.spec.ts.

/** The headings of the steps after the type step (create.steps in de-DE.json). */
const DEVICE_STEP = 'Hersteller und Modell';
const FIELDS_STEP = 'Angaben';

/**
 * openGuidedCreate clicks "CI anlegen" on the current page (no navigation, so
 * a caller can prove that the SPA was not reloaded) and waits for the type
 * step of the dialog.
 */
export async function openGuidedCreate(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: 'CI anlegen' }).first().click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('heading', { name: 'CI anlegen' })).toBeVisible();
  await expect(
    dialog.getByRole('list', { name: 'Schritte der Anlage' }).locator('[aria-current="step"]'),
  ).toHaveText(/Typ/);
  return dialog;
}

/** typeTile is the tile of a type in the type step, found by its shown label. */
export function typeTile(dialog: Locator, label: string): Locator {
  return dialog.getByRole('button', { name: label, exact: true });
}

/** nextButton is "Weiter" of the type and the device step. */
export function nextButton(dialog: Locator): Locator {
  return dialog.getByRole('button', { name: 'Weiter', exact: true });
}

/** nameField is the required master field "Name" of the fields step. */
export function nameField(dialog: Locator): Locator {
  return dialog.getByLabel('Name *', { exact: true });
}

/** statusField is the master field "Status" of the fields step. */
export function statusField(dialog: Locator): Locator {
  return dialog.getByLabel('Status', { exact: true });
}

/** createButton is "Anlegen" of the fields step. */
export function createButton(dialog: Locator): Locator {
  return dialog.getByRole('button', { name: 'Anlegen', exact: true });
}

export interface DeviceValues {
  manufacturer?: string;
  model?: string;
}

/**
 * chooseType picks the tile of the type and goes on to the fields step. A
 * physical type passes the step manufacturer and model, filled with device
 * when given; a logical type has no such step.
 */
export async function chooseType(
  dialog: Locator,
  label: string,
  device: DeviceValues = {},
): Promise<void> {
  const tile = typeTile(dialog, label);
  await tile.click();
  await expect(tile).toHaveAttribute('aria-pressed', 'true');
  await nextButton(dialog).click();
  const step = dialog.getByRole('heading', {
    name: new RegExp(`^(${DEVICE_STEP}|${FIELDS_STEP})$`),
  });
  await expect(step).toBeVisible();
  if ((await step.textContent())?.trim() === DEVICE_STEP) {
    if (device.manufacturer !== undefined) {
      await dialog
        .getByRole('textbox', { name: 'Hersteller', exact: true })
        .fill(device.manufacturer);
    }
    if (device.model !== undefined) {
      await dialog.getByRole('textbox', { name: 'Modell', exact: true }).fill(device.model);
    }
    await nextButton(dialog).click();
  } else {
    expect(device, `the logical type ${label} asks no manufacturer or model`).toEqual({});
  }
  await expect(dialog.getByRole('heading', { name: FIELDS_STEP, exact: true })).toBeVisible();
  await expect(nameField(dialog)).toBeVisible();
}

/**
 * startGuidedCreate opens the dialog on the current page, picks the type and
 * fills the name: what is left is the submit with createButton.
 */
export async function startGuidedCreate(
  page: Page,
  type: string,
  name: string,
  device: DeviceValues = {},
): Promise<Locator> {
  const dialog = await openGuidedCreate(page);
  await chooseType(dialog, type, device);
  await nameField(dialog).fill(name);
  return dialog;
}
