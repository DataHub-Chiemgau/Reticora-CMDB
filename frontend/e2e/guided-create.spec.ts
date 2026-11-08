import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import { AT, CI_TYPES, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// TST-03, MET-51, UI-12 (WP-317): the guided creation of a CI. A tile
// chooses the type, manufacturer and model offer the stored values of the
// type (GET /cis with filter[manufacturer][like] and fields), the fields
// come from the resolved form (GET /ci-types/{id}/form) and a conditional
// field appears and becomes required on input. The POST carries the UUID
// of the type, the master fields as columns and the attribute. axe finds
// no violation in any step of the open dialog. Runs in every browser
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
