import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import { AT, CI_TYPES, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';
import {
  ACCESS_SWITCH_TYPE,
  ACCESS_TEMPLATE,
  P2_ASSET,
  P2_TYPE_HANDLERS,
  accessSwitchForm,
  assetListHandlers,
} from './support/phase2';

// TST-03, MET-51, UI-12 (WP-317): the guided creation of a CI. A tile
// chooses the type, manufacturer and model offer the stored values of the
// type (GET /cis with filter[manufacturer][like] and fields), the fields
// come from the resolved form (GET /ci-types/{id}/form) and a conditional
// field appears and becomes required on input. The POST carries the UUID
// of the type, the master fields as columns and the attribute. axe finds
// no violation in any step of the open dialog. WP-324 adds the folded
// subtype tiles with the variant of the subtype and the creation of an
// asset through the same tiles (support/phase2.ts). Runs in every browser
// project of playwright.config.ts; the strict mock checks every request
// and answer against api/openapi.yaml.

test.use({ locale: 'de-DE' });

const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];
const switchType = CI_TYPES.find((t) => t.key === 'switch')!;

function storedCI(id: string, name: string, manufacturer: string, model: string) {
  return {
    id,
    name,
    status: 'active',
    ci_type_id: switchType.id,
    manufacturer,
    model,
    attributes: {},
    is_manual: true,
    version: 1,
    organization_id: ORG_ID,
    created_at: AT,
    updated_at: AT,
  };
}

const STOCK = [
  storedCI('22222222-2222-4222-8222-000000000001', 'sw-a', 'Contoso', 'CX-24'),
  storedCI('22222222-2222-4222-8222-000000000002', 'sw-b', 'Contoso', 'CX-48'),
  storedCI('22222222-2222-4222-8222-000000000003', 'sw-c', 'Contoso', 'CX-24'),
];

function field(
  name: string,
  label: string,
  data_type: string,
  extra: Record<string, unknown> = {},
  state: Record<string, unknown> = {},
) {
  return {
    name,
    width: 12,
    definition: { name, label, data_type, required: false, sort_order: 0, ...extra },
    state: { visible: true, required: false, read_only: false, ...state },
  };
}

/** The resolved form of the switch: a mounting choice and a conditional rack unit. */
const SWITCH_FORM = {
  ci_type_id: switchType.id,
  source: 'default',
  sections: [
    {
      key: 'identity',
      title: { de: 'identity', en: 'identity' },
      visible: true,
      fields: [
        field('manufacturer', 'Manufacturer', 'text'),
        field('model', 'Model', 'text'),
        field('serial_number', 'Seriennummer', 'text'),
        field('mount', 'Montage', 'enum', { enum_values: ['rack', 'desk'] }),
        field(
          'rack_unit',
          'Höheneinheit',
          'integer',
          {
            conditional: {
              visible_when: [{ field: 'mount', op: 'eq', value: 'rack' }],
              required_when: [{ field: 'mount', op: 'eq', value: 'rack' }],
            },
          },
          { visible: false },
        ),
      ],
    },
  ],
  rules: [],
};

async function expectNoAxeViolations(page: Page) {
  const results = await new AxeBuilder({ page })
    .withTags(WCAG_TAGS)
    .include('[role="dialog"]')
    .analyze();
  expect(results.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);
}

test('creates a CI through the tiles, suggestions and a conditional field', async ({ page }) => {
  const created: unknown[] = [];
  const calls = await mockBackend(page, [
    {
      path: '/cis',
      reply: (_request, url) => {
        const like = url.searchParams.get('filter[manufacturer][like]');
        const models = url.searchParams.get('filter[model][like]');
        if (like !== null || models !== null) {
          return { body: list(STOCK) };
        }
        return { body: list(created) };
      },
    },
    {
      method: 'POST',
      path: '/cis',
      reply: (request) => {
        const input = request.postDataJSON() as Record<string, unknown>;
        const ci = {
          ...storedCI('11111111-1111-4111-8111-111111111111', String(input.name), '', ''),
          ...input,
        };
        created.push(ci);
        return { status: 201, body: ci };
      },
    },
    { path: '/templates', reply: () => ({ body: list([]) }) },
    { path: `/ci-types/${switchType.id}/form`, reply: () => ({ body: SWITCH_FORM }) },
    { path: '/locations/tree', reply: () => ({ body: { data: [] } }) },
  ]);
  await signIn(page);
  await page.goto('/cmdb');

  await page.getByRole('button', { name: 'CI anlegen' }).first().click();
  const dialog = page.getByRole('dialog');
  const steps = dialog.getByRole('list', { name: 'Schritte der Anlage' });

  // Step 1: the type tiles, grouped by category.
  await expect(dialog.getByRole('button', { name: 'Switch' })).toBeVisible();
  await expect(steps.locator('[aria-current="step"]')).toHaveText(/Typ/);
  await expectNoAxeViolations(page);
  await dialog.getByRole('button', { name: 'Switch' }).click();
  await dialog.getByRole('button', { name: 'Weiter' }).click();

  // Step 2: manufacturer and model with the stored values of the type.
  await expect(dialog.getByRole('heading', { name: 'Hersteller und Modell' })).toBeFocused();
  await dialog.getByRole('textbox', { name: 'Hersteller', exact: true }).fill('Con');
  const manufacturers = dialog.getByRole('list', { name: 'Vorschläge für Hersteller' });
  await expect(manufacturers.getByRole('button')).toHaveText(['Contoso']);
  await manufacturers.getByRole('button', { name: 'Contoso' }).click();
  await expect(dialog.getByRole('textbox', { name: 'Hersteller', exact: true })).toHaveValue(
    'Contoso',
  );
  const models = dialog.getByRole('list', { name: 'Vorschläge für Modell' });
  await expect(models.getByRole('button')).toHaveText(['CX-24', 'CX-48']);
  await models.getByRole('button', { name: 'CX-48' }).click();
  await expectNoAxeViolations(page);
  await dialog.getByRole('button', { name: 'Weiter' }).click();

  // Step 3: the fields of the resolved form.
  await expect(dialog.getByRole('heading', { name: 'Angaben' })).toBeFocused();
  await dialog.getByLabel('Name *').fill('core-sw-guided');
  await dialog.getByLabel('Seriennummer').fill('SN-0815');
  await expect(dialog.getByLabel(/Höheneinheit/)).toHaveCount(0);
  await dialog.getByLabel('Montage').selectOption('rack');
  const rackUnit = dialog.getByLabel('Höheneinheit *');
  await expect(rackUnit).toBeVisible();
  await expect(rackUnit).toHaveAttribute('required', '');
  await expectNoAxeViolations(page);
  await rackUnit.fill('12');
  await dialog.getByRole('button', { name: 'Anlegen' }).click();
  await expect(dialog).toBeHidden();

  const suggestion = calls.find(
    (c) => c.path === '/cis' && c.search.includes('filter%5Bmanufacturer%5D%5Blike%5D=Con'),
  );
  expect(suggestion?.search).toContain('fields=manufacturer%2Cmodel');
  expect(suggestion?.search).toContain(`filter%5Bci_type_id%5D=${switchType.id}`);
  const posts = calls.filter((c) => c.method === 'POST' && c.path === '/cis');
  expect(posts).toHaveLength(1);
  expect(posts[0]!.body).toEqual({
    name: 'core-sw-guided',
    ci_type_id: switchType.id,
    status: 'active',
    manufacturer: 'Contoso',
    model: 'CX-48',
    serial_number: 'SN-0815',
    attributes: { mount: 'rack', rack_unit: 12 },
    discovery_source: 'manual',
  });
});

// WP-324: the subtypes of a type fold under its tile (parent_type_id,
// WP-248); a subtype with exactly one variant (template, WP-251) reads its
// form with that template and prefills its default, and the CI carries the
// template.
test('creates a CI of a subtype from its folded tile with the variant of the type', async ({
  page,
}) => {
  const calls = await mockBackend(page, [
    ...P2_TYPE_HANDLERS,
    { path: '/cis', reply: () => ({ body: list([]) }) },
    {
      method: 'POST',
      path: '/cis',
      reply: (request) => {
        const input = request.postDataJSON() as Record<string, unknown>;
        return {
          status: 201,
          body: {
            ...storedCI('11111111-1111-4111-8111-111111111112', String(input.name), '', ''),
            ...input,
            // The stored CI names its templates with key and version.
            templates: [
              {
                template_id: ACCESS_TEMPLATE.id,
                key: ACCESS_TEMPLATE.key,
                version: 1,
                position: 0,
              },
            ],
          },
        };
      },
    },
    {
      path: '/templates',
      reply: (_request, url) => ({
        body: list(
          url.searchParams.get('ci_type_id') === ACCESS_SWITCH_TYPE.id ? [ACCESS_TEMPLATE] : [],
        ),
      }),
    },
    {
      path: `/ci-types/${ACCESS_SWITCH_TYPE.id}/form`,
      reply: (_request, url) => ({ body: accessSwitchForm(url.searchParams.get('template_id')) }),
    },
    { path: '/locations/tree', reply: () => ({ body: { data: [] } }) },
  ]);
  await signIn(page);
  await page.goto('/cmdb');

  await page.getByRole('button', { name: 'CI anlegen' }).first().click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('button', { name: 'Netzwerkgerät', exact: true })).toBeVisible();
  // The subtypes are folded under the tile of their parent.
  await expect(dialog.getByRole('button', { name: 'Access-Switch' })).toHaveCount(0);
  const fold = dialog.getByRole('button', { name: 'Untertypen von Netzwerkgerät anzeigen' });
  await expect(fold).toHaveAttribute('aria-expanded', 'false');
  await fold.click();
  await expect(fold).toHaveAttribute('aria-expanded', 'true');
  const subtypes = dialog.getByRole('list', { name: 'Untertypen von Netzwerkgerät' });
  await expect(subtypes.getByRole('button')).toHaveText(['Access-Switch', 'Core-Switch']);
  await expectNoAxeViolations(page);
  await subtypes.getByRole('button', { name: 'Access-Switch' }).click();
  await expect(subtypes.getByRole('button', { name: 'Access-Switch' })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  await dialog.getByRole('button', { name: 'Weiter' }).click();

  await expect(dialog.getByRole('heading', { name: 'Hersteller und Modell' })).toBeFocused();
  await dialog.getByRole('textbox', { name: 'Hersteller', exact: true }).fill('Contoso');
  await dialog.getByRole('textbox', { name: 'Modell', exact: true }).fill('AX-24');
  await dialog.getByRole('button', { name: 'Weiter' }).click();

  // The only variant of the subtype applies: its default is prefilled.
  await expect(dialog.getByRole('heading', { name: 'Angaben' })).toBeFocused();
  await expect(dialog.getByLabel(/^Ports/)).toHaveValue('24');
  await dialog.getByLabel('Name *').fill('access-sw-01');
  await dialog.getByLabel('Uplink').fill('core-sw-01');
  await expectNoAxeViolations(page);
  await dialog.getByRole('button', { name: 'Anlegen' }).click();
  await expect(dialog).toBeHidden();

  expect(
    calls
      .filter((c) => c.path === `/ci-types/${ACCESS_SWITCH_TYPE.id}/form`)
      .map((c) => new URLSearchParams(c.search).get('template_id')),
  ).toContain(ACCESS_TEMPLATE.id);
  const posts = calls.filter((c) => c.method === 'POST' && c.path === '/cis');
  expect(posts).toHaveLength(1);
  expect(posts[0]!.body).toEqual({
    name: 'access-sw-01',
    ci_type_id: ACCESS_SWITCH_TYPE.id,
    status: 'active',
    manufacturer: 'Contoso',
    model: 'AX-24',
    attributes: { p2_uplink: 'core-sw-01', p2_ports: 24 },
    templates: [{ template_id: ACCESS_TEMPLATE.id }],
    discovery_source: 'manual',
  });
});

// WP-324 (UI-12, WP-319): the asset creation runs through the same tiles
// (the asset categories) and the fields of the category, the details
// folded. Contracts have no tiles (WP-808) and are not part of this flow.
test('creates an asset through the category tiles of the same flow', async ({ page }) => {
  const created: Record<string, unknown>[] = [];
  const calls = await mockBackend(page, [
    ...assetListHandlers(created),
    {
      method: 'POST',
      path: '/assets',
      reply: (request) => {
        const asset = {
          ...P2_ASSET,
          ...(request.postDataJSON() as Record<string, unknown>),
          id: 'a5000000-0000-4000-8000-0000000000c2',
        };
        created.push(asset);
        return { status: 201, body: asset };
      },
    },
  ]);
  await signIn(page);
  await page.goto('/assets');
  await expect(page.getByRole('link', { name: P2_ASSET.name })).toBeVisible();

  await page.getByRole('button', { name: 'Asset anlegen' }).first().click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByText('Welche Art von Asset legst du an?')).toBeVisible();
  const tiles = dialog.getByRole('region', { name: 'Kategorie' });
  // One tile per category, in the order of the labels.
  await expect(tiles.getByRole('button')).toHaveText([
    'Cloud-Ressource',
    'Hardware',
    'Lizenz',
    'Software',
    'Sonstiges',
    'Zubehör',
  ]);
  await expectNoAxeViolations(page);
  await tiles.getByRole('button', { name: 'Hardware' }).click();

  await expect(dialog.getByText('Kategorie: Hardware')).toBeVisible();
  await dialog.getByLabel(/^Inventarnummer/).fill('INV-2001');
  await dialog.getByLabel(/^Name/).fill('Notebook 2001');
  await dialog.getByLabel(/^Hersteller/).fill('Contoso');
  await dialog.getByLabel(/^Modell/).fill('NB-14');
  const more = dialog.getByRole('button', { name: 'Weitere Angaben' });
  await expect(more).toHaveAttribute('aria-expanded', 'false');
  await more.click();
  await expect(dialog.getByRole('button', { name: 'Weniger Angaben' })).toHaveAttribute(
    'aria-expanded',
    'true',
  );
  await dialog.getByLabel(/^Seriennummer/).fill('SN-2001');
  await expectNoAxeViolations(page);
  await dialog.getByRole('button', { name: 'Anlegen' }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole('link', { name: 'Notebook 2001' })).toBeVisible();

  expect(
    calls.filter((c) => c.method === 'POST' && c.path === '/assets').map((c) => c.body),
  ).toEqual([
    {
      inventory_no: 'INV-2001',
      name: 'Notebook 2001',
      category: 'hardware',
      manufacturer: 'Contoso',
      model: 'NB-14',
      serial_number: 'SN-2001',
    },
  ]);
});
