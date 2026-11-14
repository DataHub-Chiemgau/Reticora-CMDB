import AxeBuilder from '@axe-core/playwright';
import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// WFL-01 (WP-1133): the form editor adds a field with a condition (MET-45)
// to a form and saves it as its second version (PATCH /forms/{id} with
// If-Match); the forms page renders the new version, the conditional field
// appears once the condition holds, and the submission is listed under
// version 2. Requests and mocks are checked against api/openapi.yaml
// (support/contract.ts); axe finds no violations on editor and page.

test.use({ locale: 'de-DE' });

const USER = '00000000-0000-0000-0000-000000000001';
const FORM_ID = 'f1133000-0000-4000-8000-000000000001';
const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

interface Version {
  id: string;
  organization_id: string;
  form_id: string;
  version: number;
  schema: Record<string, unknown>;
  ui_hints: Record<string, unknown>;
  created_by: string;
  created_at: string;
}

test('creates a field with a condition, saves version 2 and submits under it', async ({ page }) => {
  const v1: Version = {
    id: 'f1133000-0000-4000-8000-0000000000a1',
    organization_id: ORG_ID,
    form_id: FORM_ID,
    version: 1,
    schema: { type: 'object', properties: { kind: { type: 'string', title: 'Art' } } },
    ui_hints: { order: ['kind'], fields: { kind: { label: { de: 'Art', en: 'Kind' } } } },
    created_by: USER,
    created_at: AT,
  };
  const versions: Version[] = [v1];
  const form = {
    id: FORM_ID,
    organization_id: ORG_ID,
    name: 'Geräteaufnahme',
    description: 'Aufnahme eines Geräts',
    schema: v1.schema,
    ui_hints: v1.ui_hints,
    active: true,
    version: 1,
    schema_version: 1,
    current_version_id: v1.id,
    created_at: AT,
    updated_at: AT,
  };
  const submissions: Record<string, unknown>[] = [];
  const calls = await mockBackend(page, [
    {
      path: '/me/permissions',
      reply: () => ({
        body: { user_id: USER, permissions: ['form:read', 'form:write', 'workflow:read'] },
      }),
    },
    {
      path: '/modules',
      reply: () => ({
        body: {
          data: [{ key: 'workflows', phase: 'P3', nav: ['workflows', 'forms'], enabled: true }],
        },
      }),
    },
    { path: '/forms', reply: () => ({ body: list([form]) }) },
    { path: `/forms/${FORM_ID}`, reply: () => ({ body: form }) },
    {
      method: 'PATCH',
      path: `/forms/${FORM_ID}`,
      reply: (request) => {
        const body = request.postDataJSON() as {
          schema: Record<string, unknown>;
          ui_hints: Record<string, unknown>;
        };
        const next: Version = {
          ...v1,
          id: 'f1133000-0000-4000-8000-0000000000a2',
          version: versions.length + 1,
          schema: body.schema,
          ui_hints: body.ui_hints,
        };
        versions.unshift(next);
        Object.assign(form, body, {
          version: form.version + 1,
          schema_version: next.version,
          current_version_id: next.id,
        });
        return { body: form };
      },
    },
    { path: `/forms/${FORM_ID}/versions`, reply: () => ({ body: list([...versions]) }) },
    { path: '/form-submissions', reply: () => ({ body: list([...submissions]) }) },
    {
      method: 'POST',
      path: `/forms/${FORM_ID}/submissions`,
      reply: (request) => {
        const body = request.postDataJSON() as { values: Record<string, unknown> };
        const created = {
          id: `f1133000-0000-4000-8000-00000000000${submissions.length + 1}`,
          organization_id: ORG_ID,
          form_id: FORM_ID,
          form_version_id: form.current_version_id,
          form_version: form.schema_version,
          values: body.values,
          submitted_by: USER,
          status: 'submitted',
          created_at: AT,
          updated_at: AT,
        };
        submissions.push(created);
        return { status: 201, body: created };
      },
    },
  ]);
  await signIn(page);
  await page.goto(`/forms/${FORM_ID}/edit`);

  await expect(page.getByRole('group', { name: 'Feld 1 (kind)' })).toBeVisible();
  await page.getByRole('button', { name: 'Feld hinzufügen' }).click();
  const field = page.getByRole('group', { name: 'Feld 2 (field_2)' });
  await field.getByLabel('Schlüssel').fill('serial');
  const serial = page.getByRole('group', { name: 'Feld 2 (serial)' });
  await serial.getByLabel('Titel (Deutsch)').fill('Seriennummer');
  await serial.getByRole('button', { name: 'Bedingungen von serial' }).click();
  const visible = page.getByTestId('condition-serial-visible_when');
  await visible.getByRole('button', { name: 'Bedingung hinzufügen' }).last().click();
  const node = visible.getByRole('group', { name: 'Bedingung 1' });
  await node.getByRole('combobox', { name: 'Feld' }).selectOption('kind');
  await node.getByRole('combobox', { name: 'Operator' }).selectOption('eq');
  await node.getByLabel('Wert').fill('router');

  // The preview follows the condition.
  const preview = page.getByTestId('form-preview');
  await expect(preview.getByLabel('Seriennummer')).toHaveCount(0);
  await preview.getByLabel('Art').fill('router');
  await expect(preview.getByLabel('Seriennummer')).toBeVisible();

  const editor = await new AxeBuilder({ page }).withTags(WCAG_TAGS).include('main').analyze();
  expect(editor.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);

  await page.getByRole('button', { name: 'Speichern' }).click();
  await expect(page.getByText('Das Formular wurde gespeichert.')).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Version 2 in der Vorschau öffnen' }),
  ).toBeVisible();

  await page.goto('/forms');
  const fill = page.getByLabel('Art');
  await fill.fill('router');
  await page.getByLabel('Seriennummer').fill('SN-1133');
  await page.getByRole('button', { name: 'Absenden' }).click();
  const v2 = page.getByTestId('submissions-v2');
  await expect(v2.getByText('SN-1133')).toBeVisible();
  await expect(v2.getByRole('heading', { name: 'Version 2' })).toBeVisible();

  const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).include('main').analyze();
  expect(results.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);

  const patch = calls.find((c) => c.method === 'PATCH');
  expect(patch?.body).toMatchObject({
    ui_hints: {
      fields: { serial: { visible_when: { op: 'eq', field: 'kind', value: 'router' } } },
    },
  });
  expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
    values: { kind: 'router', serial: 'SN-1133' },
  });
});
