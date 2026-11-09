import type { Page } from '@playwright/test';
import { AT, ORG_ID, expect, mockBackend, test } from './support/backend';
import type { Handler } from './support/backend';
import {
  ACCESS_SWITCH_TYPE,
  ACCESS_TEMPLATE,
  FORM_BUILDER_READS,
  SERVICE_ROLE,
  USER_ID,
  mePermissions,
  signInWith,
} from './support/phase2';

// TST-03 [P2], MET-50, MET-52, MET-53 (WP-324): the form builder of a CI
// type (WP-316). The layout is arranged with the keyboard (the sortable
// handles of @dnd-kit: Space picks up, the arrow keys move, Space drops)
// and with the mouse (drag and drop, the palette and the width), published
// as an own layout (POST /layouts) and published again as its next version
// (PATCH /layouts/{id} with If-Match). The preview shows the form as a role
// with a template (GET /ci-types/{id}/form with role_id, template_id and
// preview, WP-803). With an active draft of the work area (WP-799) the
// layout is captured as a draft item (202), tested and published with the
// draft. The data come from support/phase2.ts; the strict mock checks every
// request and answer against api/openapi.yaml.

test.use({ locale: 'de-DE' });

const RIGHTS = ['ci:read', 'citype:manage', 'ci_attribute:manage', 'metamodel:publish'];
const BUILDER = `/ci-types/${ACCESS_SWITCH_TYPE.id}/form-builder`;
const LAYOUT_ID = '1a000000-0000-4000-8000-0000000000b2';
const DRAFT_ID = 'df000000-0000-4000-8000-0000000000b1';
const DRAFT_ITEM_ID = 'df100000-0000-4000-8000-0000000000b1';
const TEST_JOB_ID = '7c000000-0000-4000-8000-0000000000b9';

interface LayoutWrites {
  handlers: Handler[];
  /** The bodies of POST /layouts with their query. */
  creates: { search: string; body: Record<string, unknown> }[];
  /** The PATCH /layouts/{id} requests with their If-Match. */
  patches: { ifMatch: string | undefined; body: Record<string, unknown> }[];
  /** The draft items the writes inside a draft captured (202). */
  draftItems: Record<string, unknown>[];
}

/**
 * layoutWrites keeps the own layouts of the access switch like the server:
 * POST creates version 1 (202 with a draft item inside a draft), PATCH with
 * the current version in If-Match raises it.
 */
function layoutWrites(): LayoutWrites {
  const own: Record<string, unknown>[] = [];
  const state: LayoutWrites = { handlers: [], creates: [], patches: [], draftItems: [] };
  state.handlers = [
    {
      path: '/layouts',
      reply: (_request, url) => {
        const typeId = url.searchParams.get('ci_type_id');
        const templateId = url.searchParams.get('template_id');
        const data = own.filter((l) =>
          templateId ? l.template_id === templateId : l.ci_type_id === typeId,
        );
        return { body: { data, has_more: false } };
      },
    },
    {
      method: 'POST',
      path: '/layouts',
      reply: (request, url) => {
        const body = request.postDataJSON() as Record<string, unknown>;
        state.creates.push({ search: url.search, body });
        const draft = url.searchParams.get('draft');
        if (draft) {
          const item = {
            id: DRAFT_ITEM_ID,
            kind: 'layout',
            op: 'create',
            target_id: null,
            target_key: String(body.key),
            payload: body,
            base: null,
            schema_versions: {},
            route: 'POST /api/v1/layouts',
            created_by: USER_ID,
            created_at: AT,
          };
          state.draftItems.push(item);
          return { status: 202, body: item };
        }
        const layout = {
          id: LAYOUT_ID,
          organization_id: ORG_ID,
          ...body,
          version: 1,
          created_at: AT,
          updated_at: AT,
        };
        own.push(layout);
        return { status: 201, body: layout };
      },
    },
    {
      method: 'PATCH',
      path: `/layouts/${LAYOUT_ID}`,
      reply: (request) => {
        const body = request.postDataJSON() as Record<string, unknown>;
        state.patches.push({ ifMatch: request.headers()['if-match'], body });
        const current = own[0]!;
        Object.assign(current, body, { version: Number(current.version) + 1 });
        return { body: current };
      },
    },
    { path: `/layouts/${LAYOUT_ID}`, reply: () => ({ body: own[0] }) },
  ];
  return state;
}

async function openBuilder(page: Page, handlers: Handler[]) {
  const calls = await mockBackend(page, [
    mePermissions(RIGHTS),
    ...handlers,
    ...FORM_BUILDER_READS,
  ]);
  await signInWith(page, RIGHTS);
  await page.goto(BUILDER);
  await expect(
    page.getByRole('heading', { name: 'Formular gestalten: Access-Switch' }),
  ).toBeVisible();
  await expect(page.locator('[data-section-key="p2_main"]')).toBeVisible();
  return calls;
}

/** fieldOrder reads the field names of a section in their order. */
function fieldOrder(page: Page, section: string) {
  return page
    .locator(`[data-section-key="${section}"] [data-field-name]`)
    .evaluateAll((nodes) => nodes.map((node) => node.getAttribute('data-field-name')));
}

test('arranges the layout with keyboard and mouse and saves it as layout versions', async ({
  page,
}) => {
  const writes = layoutWrites();
  await openBuilder(page, writes.handlers);
  // The copy of the inherited layout of the network device.
  await expect(page.getByText('Geerbt von Netzwerkgerät', { exact: true })).toBeVisible();
  await expect.poll(() => fieldOrder(page, 'p2_main')).toEqual(['p2_mgmt_vlan', 'p2_uplink']);

  // Keyboard: pick up the uplink with Space, move it up, drop it.
  const handle = page.getByRole('button', { name: 'Verschieben: Uplink' });
  const announced = (text: string) => expect(page.getByText(text).first()).toBeAttached();
  await handle.focus();
  await page.keyboard.press('Space');
  await announced('Uplink aufgenommen, Position 2 von 2.');
  await page.keyboard.press('ArrowUp');
  await announced('Uplink auf Position 1 von 2 verschoben.');
  await page.keyboard.press('Space');
  await announced('Uplink auf Position 1 von 2 abgelegt.');
  await expect.poll(() => fieldOrder(page, 'p2_main')).toEqual(['p2_uplink', 'p2_mgmt_vlan']);

  // Keyboard: place the ports from the palette with Enter.
  await page.getByRole('button', { name: 'Platzieren: Ports' }).focus();
  await page.keyboard.press('Enter');
  await expect
    .poll(() => fieldOrder(page, 'p2_main'))
    .toEqual(['p2_uplink', 'p2_mgmt_vlan', 'p2_ports']);

  // Mouse: drag the ports by their handle to the top of the section.
  const ports = page.getByRole('button', { name: 'Verschieben: Ports' });
  const target = page.getByRole('button', { name: 'Verschieben: Uplink' });
  // Both handles in the viewport (a phone shows the section further down).
  await ports.scrollIntoViewIfNeeded();
  const from = (await ports.boundingBox())!;
  const to = (await target.boundingBox())!;
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2 - 8, { steps: 4 });
  await page.mouse.move(to.x + to.width / 2, to.y - to.height, { steps: 12 });
  await page.mouse.up();
  await expect
    .poll(() => fieldOrder(page, 'p2_main'))
    .toEqual(['p2_ports', 'p2_uplink', 'p2_mgmt_vlan']);
  // Mouse: the width of the ports and the rule that shows the section.
  await page.getByLabel('Breite: Ports').selectOption('4');
  await page.getByRole('checkbox', { name: 'p2_rack_mounted' }).check();

  // Publish as own layout: version 1.
  await page.getByRole('button', { name: 'Veröffentlichen' }).click();
  const confirm = page.getByRole('dialog', { name: 'Layout veröffentlichen?' });
  await confirm.getByRole('button', { name: 'Veröffentlichen' }).click();
  await expect(page.getByText('Das Layout ist veröffentlicht.')).toBeVisible();
  await expect(page.getByText('Eigenes Layout p2_access_switch, Version 1')).toBeVisible();
  expect(writes.creates).toEqual([
    {
      search: '',
      body: {
        key: 'p2_access_switch',
        ci_type_id: ACCESS_SWITCH_TYPE.id,
        priority: 0,
        is_active: true,
        layout: {
          sections: [
            {
              key: 'p2_main',
              title: { de: 'Allgemein', en: 'General' },
              visible_when: ['p2_rack_mounted'],
              fields: [
                { name: 'p2_ports', width: 4 },
                { name: 'p2_uplink', width: 6 },
                { name: 'p2_mgmt_vlan', width: 6 },
              ],
            },
          ],
        },
      },
    },
  ]);

  // A change publishes the next version of the own layout (If-Match).
  await page.getByRole('button', { name: 'Nach unten: Ports' }).click();
  await page.getByRole('button', { name: 'Veröffentlichen' }).click();
  await confirm.getByRole('button', { name: 'Veröffentlichen' }).click();
  await expect(page.getByText('Eigenes Layout p2_access_switch, Version 2')).toBeVisible();
  expect(writes.patches).toHaveLength(1);
  expect(writes.patches[0]!.ifMatch).toBe('"1"');
  expect(
    (
      writes.patches[0]!.body.layout as { sections: { fields: { name: string }[] }[] }
    ).sections[0]!.fields.map((f) => f.name),
  ).toEqual(['p2_uplink', 'p2_ports', 'p2_mgmt_vlan']);
});

test('previews the form as a role with a template', async ({ page }) => {
  const writes = layoutWrites();
  const calls = await openBuilder(page, writes.handlers);

  const preview = page.getByRole('region', { name: 'Vorschau' });
  await preview.getByRole('button', { name: 'Vorschau öffnen' }).click();
  await expect(preview.getByRole('status')).toContainText(
    'Die Vorschau zeigt den veröffentlichten Stand.',
  );
  await preview.getByLabel('Template der Vorschau').selectOption({ label: 'Access 24 Port' });
  await preview.getByLabel('Rolle der Vorschau').selectOption({ label: SERVICE_ROLE.name });
  const form = preview.getByRole('form', { name: 'Formularvorschau' });
  // The template prefills the ports, the role may not change the uplink.
  await expect(form.getByLabel(/^Ports/)).toHaveValue('24');
  await expect(form.getByLabel(/^Uplink/)).toBeDisabled();

  const asRole = calls.filter(
    (c) =>
      c.path === `/ci-types/${ACCESS_SWITCH_TYPE.id}/form` &&
      new URLSearchParams(c.search).get('role_id') === SERVICE_ROLE.id,
  );
  expect(asRole.length).toBeGreaterThan(0);
  const params = new URLSearchParams(asRole.at(-1)!.search);
  expect(params.get('template_id')).toBe(ACCESS_TEMPLATE.id);
  expect(params.get('preview')).toBe('true');
});

test('publishes the layout through the draft work area', async ({ page }) => {
  const writes = layoutWrites();
  let created = false;
  let status = 'draft';
  const items = writes.draftItems;
  // The test run of the draft: no CI of the access switch breaks the layout.
  const report = {
    job_id: TEST_JOB_ID,
    state: 'completed',
    draft_version: 2,
    started_at: AT,
    finished_at: AT,
    blocked: false,
    outdated: false,
    conflicts: [],
    totals: { types: 1, cis: 3, invalid_cis: 0, violations: 0 },
    types: [
      { ci_type_id: ACCESS_SWITCH_TYPE.id, cis: 3, invalid_cis: 0, violations: 0, attributes: [] },
    ],
    truncated: false,
  };
  const draft = () => ({
    id: DRAFT_ID,
    organization_id: ORG_ID,
    name: 'Formular Access-Switch',
    status,
    version: items.length + 1,
    item_count: items.length,
    created_by: USER_ID,
    test_report: status === 'draft' ? null : report,
    created_at: AT,
    updated_at: AT,
    published_at: status === 'published' ? AT : null,
  });
  const calls = await openBuilder(page, [
    ...writes.handlers,
    {
      method: 'POST',
      path: '/metamodel/drafts',
      reply: () => {
        created = true;
        return { status: 201, body: draft() };
      },
    },
    {
      path: '/metamodel/drafts',
      reply: () => ({ body: { data: created ? [draft()] : [], has_more: false } }),
    },
    { path: `/metamodel/drafts/${DRAFT_ID}`, reply: () => ({ body: draft() }) },
    {
      path: `/metamodel/drafts/${DRAFT_ID}/items`,
      reply: () => ({ body: { data: items, has_more: false } }),
    },
    {
      method: 'POST',
      path: `/metamodel/drafts/${DRAFT_ID}/test`,
      reply: () => {
        status = 'tested';
        return {
          status: 202,
          body: { job_id: TEST_JOB_ID, status: 'queued', kind: 'metamodel.test' },
        };
      },
    },
    {
      path: `/jobs/${TEST_JOB_ID}`,
      reply: () => ({
        body: {
          id: TEST_JOB_ID,
          organization_id: ORG_ID,
          kind: 'metamodel.test',
          status: 'succeeded',
          progress: 100,
          result: {},
          created_by: USER_ID,
          attempts: 1,
          max_attempts: 1,
          created_at: AT,
          updated_at: AT,
          started_at: AT,
          finished_at: AT,
          cancellable: false,
        },
      }),
    },
    {
      path: `/metamodel/drafts/${DRAFT_ID}/test`,
      reply: () => ({ body: { ...report, data: [], total: 0, has_more: false } }),
    },
    {
      method: 'POST',
      path: `/metamodel/drafts/${DRAFT_ID}/publish`,
      reply: () => {
        status = 'published';
        return {
          body: {
            draft: draft(),
            counts: { layout: items.length },
            types: [{ id: ACCESS_SWITCH_TYPE.id, schema_version: 2 }],
          },
        };
      },
    },
  ]);

  // Open a draft in the work area and choose it as the active draft.
  await page.goto('/ci-types/drafts');
  await page.getByRole('button', { name: 'Draft anlegen' }).first().click();
  await page.getByLabel('Name des neuen Drafts').fill('Formular Access-Switch');
  await page.getByRole('dialog').getByRole('button', { name: 'Draft anlegen' }).click();
  await page
    .getByRole('button', { name: 'Draft Formular Access-Switch als aktiven Draft wählen' })
    .click();
  await expect(page.getByText('aktiver Draft')).toBeVisible();

  // The form builder writes into the draft (202): nothing is productive yet.
  await page.goto(BUILDER);
  await expect(page.getByText('Änderungen landen im Draft Formular Access-Switch.')).toBeVisible();
  await page.getByRole('button', { name: 'Nach oben: Uplink' }).click();
  await page.getByRole('button', { name: 'Im Draft speichern' }).click();
  await expect(page.getByText('Im Draft Formular Access-Switch gespeichert.')).toBeVisible();
  expect(writes.creates).toHaveLength(1);
  expect(new URLSearchParams(writes.creates[0]!.search).get('draft')).toBe(DRAFT_ID);

  // Test and publish the draft in the work area.
  await page.goto(`/ci-types/drafts/${DRAFT_ID}`);
  await expect(
    page.getByRole('heading', { name: 'Formular Access-Switch', level: 1 }),
  ).toBeVisible();
  await expect(page.locator('#main-content').getByText('p2_access_switch').first()).toBeVisible();
  await page.getByRole('button', { name: 'Testen' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Test starten' }).click();
  await expect(page.getByText('Getestet').first()).toBeVisible();
  await page.getByRole('button', { name: 'Publizieren' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Publizieren' }).click();
  await expect(page.getByText('Publiziert').first()).toBeVisible();
  expect(calls.filter((c) => c.method === 'POST' && c.path.endsWith('/publish'))).toHaveLength(1);
});
