/**
 * WFL-01 (WP-1133): the form editor. A field is created, a condition set
 * with the RuleBuilder and the live preview follows it; saving posts JSON
 * schema and ui_hints; a condition on the field itself is an error at the
 * field; a 422 appears at the field of its path; a 412 points to the newer
 * version.
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/FormEditorPage.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Route, Routes } from 'react-router';
import i18n from '../i18n';
import { renderWithProviders } from '../test/utils';
import { FormEditorPage } from './FormEditorPage';

const AT = '2026-01-01T00:00:00Z';
type Json = Record<string, unknown>;

const FORM = {
  id: 'form-1',
  organization_id: 'org-1',
  name: 'Gerät',
  schema: {
    type: 'object',
    properties: {
      kind: { type: 'string', title: 'Art' },
      serial: { type: 'string', title: 'Seriennummer' },
    },
  },
  ui_hints: {
    order: ['kind', 'serial'],
    fields: { kind: { label: { de: 'Art' } }, serial: { label: { de: 'Seriennummer' } } },
  },
  active: true,
  version: 4,
  schema_version: 1,
  current_version_id: 'v1',
  created_at: AT,
  updated_at: AT,
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

function stub(
  onWrite?: (method: string, path: string, body: Json) => Response | undefined,
  { permissions = ['form:read', 'form:write'], form = FORM as Json } = {},
) {
  const calls: { method: string; path: string; body?: Json; headers?: HeadersInit }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const { pathname } = new URL(String(input), 'http://localhost');
      const path = pathname.replace(/^\/api\/v1/, '');
      const method = init?.method ?? 'GET';
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      calls.push({ method, path, body, headers: init?.headers });
      if (method !== 'GET') {
        const answer = onWrite?.(method, path, body ?? {});
        if (answer) return answer;
        return json({ ...FORM, ...body, id: 'form-1', version: 5 }, method === 'POST' ? 201 : 200);
      }
      if (path === '/me/permissions') return json({ user_id: 'u', permissions });
      if (path === '/forms/form-1') return json(form);
      if (path === '/forms/form-1/versions') {
        return json({
          data: [
            {
              id: 'v1',
              organization_id: 'org-1',
              form_id: 'form-1',
              version: 1,
              schema: FORM.schema,
              ui_hints: FORM.ui_hints,
              created_by: 'user-7',
              created_at: AT,
            },
          ],
          total: 1,
          limit: 100,
          offset: 0,
          has_more: false,
        });
      }
      return json({ data: [], total: 0, limit: 100, offset: 0, has_more: false });
    }),
  );
  return calls;
}

function renderAt(route: string) {
  return renderWithProviders(
    <Routes>
      <Route path="/forms/new" element={<FormEditorPage />} />
      <Route path="/forms/:id/edit" element={<FormEditorPage />} />
    </Routes>,
    { route },
  );
}

/** setCondition fills the condition kind of field key with field op value. */
async function setCondition(
  user: ReturnType<typeof userEvent.setup>,
  key: string,
  kind: string,
  field: string,
  value: string,
) {
  const toggle = screen.getByRole('button', { name: `Bedingungen von ${key}` });
  if (toggle.getAttribute('aria-expanded') !== 'true') await user.click(toggle);
  const box = screen.getByTestId(`condition-${key}-${kind}`);
  await user.click(within(box).getAllByRole('button', { name: 'Bedingung hinzufügen' }).pop()!);
  const node = within(box).getByRole('group', { name: 'Bedingung 1' });
  await user.selectOptions(within(node).getByRole('combobox', { name: 'Feld' }), field);
  await user.selectOptions(within(node).getByRole('combobox', { name: 'Operator' }), 'eq');
  await user.type(within(node).getByLabelText('Wert'), value);
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('FormEditorPage', () => {
  it('creates fields, sets a condition with the RuleBuilder and the preview follows it', async () => {
    const user = userEvent.setup();
    const calls = stub();
    renderAt('/forms/new');

    await user.type(await screen.findByLabelText(/^Name/), 'Gerät');
    await user.click(await screen.findByRole('button', { name: 'Feld hinzufügen' }));
    await user.click(screen.getByRole('button', { name: 'Feld hinzufügen' }));
    const first = screen.getByRole('group', { name: 'Feld 1 (field_1)' });
    await user.clear(within(first).getByLabelText('Schlüssel'));
    await user.type(within(first).getByLabelText('Schlüssel'), 'kind');
    await user.type(within(first).getByLabelText('Titel (Deutsch)'), 'Art');
    const second = screen.getByRole('group', { name: 'Feld 2 (field_2)' });
    await user.clear(within(second).getByLabelText('Schlüssel'));
    await user.type(within(second).getByLabelText('Schlüssel'), 'serial');
    await user.type(within(second).getByLabelText('Titel (Deutsch)'), 'Seriennummer');
    await setCondition(user, 'serial', 'visible_when', 'kind', 'router');

    const preview = screen.getByTestId('form-preview');
    await waitFor(() =>
      expect(within(preview).queryByLabelText('Seriennummer')).not.toBeInTheDocument(),
    );
    await user.type(within(preview).getByLabelText('Art'), 'router');
    expect(await within(preview).findByLabelText('Seriennummer')).toBeInTheDocument();

    await user.click(await screen.findByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST' && c.path === '/forms')!;
    expect(post.body).toMatchObject({
      name: 'Gerät',
      active: true,
      schema: {
        type: 'object',
        properties: { kind: { type: 'string', title: 'Art' }, serial: { type: 'string' } },
      },
      ui_hints: {
        order: ['kind', 'serial'],
        fields: { serial: { visible_when: { op: 'eq', field: 'kind', value: 'router' } } },
      },
    });
  });

  it('shows a condition on the field itself as an error at the field', async () => {
    const user = userEvent.setup();
    const calls = stub();
    renderAt('/forms/form-1/edit');

    await screen.findByRole('group', { name: 'Feld 1 (kind)' });
    await setCondition(user, 'kind', 'required_when', 'kind', 'x');
    await user.click(await screen.findByRole('button', { name: 'Speichern' }));

    const field = screen.getByRole('group', { name: 'Feld 1 (kind)' });
    expect(
      await within(field).findByText('Die Bedingung „Pflicht wenn“ verweist auf das Feld selbst.'),
    ).toHaveAttribute('role', 'alert');
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('shows a 422 at the field of its path and sends If-Match', async () => {
    const user = userEvent.setup();
    const calls = stub((method) =>
      method === 'PATCH'
        ? json(
            {
              type: 'about:blank',
              title: 'Unprocessable',
              status: 422,
              field: 'ui_hints.fields.serial.visible_when',
              detail: 'is no valid condition',
            },
            422,
          )
        : undefined,
    );
    renderAt('/forms/form-1/edit');

    await screen.findByRole('group', { name: 'Feld 2 (serial)' });
    await user.click(await screen.findByRole('button', { name: 'Speichern' }));

    const field = screen.getByRole('group', { name: 'Feld 2 (serial)' });
    expect(await within(field).findByText('is no valid condition')).toBeInTheDocument();
    const patch = calls.find((c) => c.method === 'PATCH')!;
    expect(patch.path).toBe('/forms/form-1');
    expect(new Headers(patch.headers).get('If-Match')).toBe('"4"');
  });

  it('points to the newer version on 412', async () => {
    const user = userEvent.setup();
    stub((method) =>
      method === 'PATCH'
        ? json({ type: 'about:blank', title: 'Precondition Failed', status: 412 }, 412)
        : undefined,
    );
    renderAt('/forms/form-1/edit');

    await screen.findByRole('group', { name: 'Feld 1 (kind)' });
    await user.click(await screen.findByRole('button', { name: 'Speichern' }));
    expect(
      await screen.findByText(
        'Das Formular wurde inzwischen geändert. Bitte die neue Fassung laden.',
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Neu laden' })).toBeInTheDocument();
  });

  it('lists the versions and opens an older one read only in the preview', async () => {
    const user = userEvent.setup();
    stub();
    renderAt('/forms/form-1/edit');

    await user.click(
      await screen.findByRole('button', { name: 'Version 1 in der Vorschau öffnen' }),
    );
    const preview = screen.getByTestId('form-preview');
    expect(within(preview).getByText('Version 1 (schreibgeschützt)')).toBeInTheDocument();
    expect(within(preview).getByLabelText('Art')).toBeDisabled();
  });
  it('is read only without form:write, conditions included', async () => {
    const user = userEvent.setup();
    const calls = stub(undefined, { permissions: ['form:read'] });
    renderAt('/forms/form-1/edit');

    const field = await screen.findByRole('group', { name: 'Feld 1 (kind)' });
    expect(await screen.findByText(/ist das Formular schreibgeschützt/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Speichern' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Feld hinzufügen' })).not.toBeInTheDocument();
    expect(within(field).getByLabelText('Schlüssel')).toHaveAttribute('readonly');
    expect(within(field).getByLabelText('Typ')).toBeDisabled();
    await user.click(within(field).getByRole('button', { name: 'Bedingungen von kind' }));
    const box = screen.getByTestId('condition-kind-visible_when');
    for (const button of within(box).getAllByRole('button', { name: 'Bedingung hinzufügen' })) {
      expect(button).toBeDisabled();
    }
    expect(calls.some((c) => c.method !== 'GET')).toBe(false);
  });

  it('keeps the commas of the value list while typing', async () => {
    const user = userEvent.setup();
    const calls = stub();
    renderAt('/forms/form-1/edit');

    await screen.findByRole('button', { name: 'Speichern' });
    const field = screen.getByRole('group', { name: 'Feld 1 (kind)' });
    await user.selectOptions(within(field).getByLabelText('Typ'), 'enum');
    const list = within(field).getByLabelText('Werte (durch Komma getrennt)');
    await user.type(list, 'router, switch');
    expect(list).toHaveValue('router, switch');
    await user.click(screen.getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(calls.find((c) => c.method === 'PATCH')?.body).toMatchObject({
      schema: { properties: { kind: { enum: ['router', 'switch'] } } },
    });
  });

  it('opens a definition it cannot represent read only and keeps it', async () => {
    stub(undefined, {
      form: {
        ...FORM,
        schema: {
          type: 'object',
          properties: {
            device: { type: 'object', properties: { kind: { type: 'string' } } },
          },
        },
        ui_hints: { fields: { device: {} } },
      },
    });
    renderAt('/forms/form-1/edit');

    expect(
      await screen.findByText(/Teile, die der Editor nicht darstellen kann/),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Speichern' })).not.toBeInTheDocument();
  });
});
