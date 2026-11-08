/**
 * WP-803 (MET-52, UI-11 [P2]): the preview of the form builder as role X
 * with template Y.
 *
 *   Request   GET /ci-types/{id}/form?template_id=…&role_id=…&preview=true
 *             per selection, without role_id and preview for "Eigene Rollen"
 *   Rendering sections and fields in server order through DynamicForm with
 *             its layout, the defaults as start values
 *   Local     inputs change local state only: no POST/PUT/PATCH/DELETE
 *   Role      the role selection is off without citype:manage; a 403 shows
 *             the note and loads without role_id
 *   Draft     with an active draft the call carries draft=<id>
 *   Unsaved   a note while the editor has unsaved changes
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/metamodel/LayoutPreview.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { useAuthStore } from '../../auth/authStore';
import { useMetamodelDraftStore } from '../../stores/metamodelDraft';
import type { Template } from '../../api/cmdb';
import i18n from '../../i18n';
import { LayoutPreview } from './LayoutPreview';

type Json = Record<string, unknown>;

interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
}

const AT = '2026-01-01T00:00:00Z';
const ROLE = 'role-tech';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const TEMPLATE = {
  id: 'tpl-1',
  organization_id: 'org-1',
  key: 'core_switch',
  ci_type_id: 't-net',
  display_name: { de: 'Core-Switch', en: 'Core switch' },
  is_system: false,
  is_active: true,
  current_version: 1,
  created_at: AT,
  updated_at: AT,
} as unknown as Template;

function field(name: string, width: number, extra: Json = {}, state: Json = {}): Json {
  return {
    name,
    width,
    definition: { name, label: name, data_type: 'text', required: false, sort_order: 0, ...extra },
    state: { visible: true, required: false, read_only: false, ...state },
  };
}

function resolved(label: string): Json {
  return {
    ci_type_id: 't-net',
    source: 'layout',
    layout_id: 'l-1',
    sections: [
      {
        key: 'second',
        title: { de: `Netz ${label}`, en: `Network ${label}` },
        visible: true,
        fields: [field('vlan', 6, { default_value: '42', data_type: 'integer' }), field('ip', 6)],
      },
      {
        key: 'first',
        title: { de: 'Allgemein', en: 'General' },
        visible: true,
        fields: [field('hostname', 12, { default_value: 'srv-01' }, { required: true })],
      },
    ],
    rules: [],
  };
}

interface Backend {
  permissions?: string[];
  roleStatus?: number;
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const q = url.searchParams;
      calls.push({ method, path, query: q });
      if (path === '/me/permissions') {
        return json({ user_id: 'u1', permissions: b.permissions ?? ['ci:read', 'citype:manage'] });
      }
      if (path === '/roles') {
        return json({
          data: [
            {
              id: ROLE,
              organization_id: 'org-1',
              name: 'Techniker',
              is_system: false,
              permissions: [],
              created_at: AT,
              updated_at: AT,
            },
          ],
          total: 1,
          limit: 200,
          offset: 0,
          has_more: false,
        });
      }
      if (path === '/ci-types/t-net/form') {
        if (q.get('role_id') && b.roleStatus) {
          return new Response(
            JSON.stringify({ status: b.roleStatus, title: 'Forbidden', trace_id: 'trace-p' }),
            { status: b.roleStatus, headers: { 'Content-Type': 'application/problem+json' } },
          );
        }
        const label = [q.get('template_id') ?? 'typ', q.get('role_id') ?? 'eigen'].join('/');
        return json(resolved(label));
      }
      return json({ data: [] });
    }),
  );
  return calls;
}

const formCalls = (calls: Call[]) => calls.filter((c) => c.path === '/ci-types/t-net/form');
const lastForm = (calls: Call[]) => formCalls(calls).slice(-1)[0]!;
const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET');

function renderPreview(props: Partial<Parameters<typeof LayoutPreview>[0]> = {}) {
  return renderWithProviders(<LayoutPreview ciTypeId="t-net" templates={[TEMPLATE]} {...props} />);
}

async function roleSelect() {
  const select = await screen.findByLabelText('Rolle der Vorschau');
  await waitFor(() =>
    expect(within(select).getByRole('option', { name: 'Techniker' })).toBeInTheDocument(),
  );
  return select as HTMLSelectElement;
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
    user: { sub: 'u1' },
  });
});

afterEach(() => {
  act(() => useMetamodelDraftStore.getState().clear());
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('LayoutPreview request per selection (WP-803)', () => {
  it('asks for template and role with preview=true per selection', async () => {
    const calls = stubBackend();
    renderPreview();
    expect(await screen.findByText('Netz typ/eigen')).toBeInTheDocument();
    const own = lastForm(calls).query;
    expect(own.has('role_id')).toBe(false);
    expect(own.has('preview')).toBe(false);
    expect(own.has('template_id')).toBe(false);

    fireEvent.change(screen.getByLabelText('Template der Vorschau'), {
      target: { value: 'tpl-1' },
    });
    expect(await screen.findByText('Netz tpl-1/eigen')).toBeInTheDocument();
    expect(lastForm(calls).query.get('template_id')).toBe('tpl-1');

    const select = await roleSelect();
    await waitFor(() => expect(select).not.toBeDisabled());
    fireEvent.change(select, { target: { value: ROLE } });
    expect(await screen.findByText(`Netz tpl-1/${ROLE}`)).toBeInTheDocument();
    const asRole = lastForm(calls).query;
    expect(asRole.get('template_id')).toBe('tpl-1');
    expect(asRole.get('role_id')).toBe(ROLE);
    expect(asRole.get('preview')).toBe('true');
    expect(asRole.has('draft')).toBe(false);
  });

  it('starts with the target and role of the builder', async () => {
    const calls = stubBackend();
    renderPreview({ initialTemplateId: 'tpl-1', initialRoleId: ROLE });
    expect(await screen.findByText(`Netz tpl-1/${ROLE}`)).toBeInTheDocument();
    expect(lastForm(calls).query.get('preview')).toBe('true');
  });
});

describe('LayoutPreview rendering (WP-803)', () => {
  it('shows sections and fields in server order with the defaults', async () => {
    stubBackend();
    renderPreview();
    const form = await screen.findByRole('form', { name: 'Formularvorschau' });
    await within(form).findByText('Netz typ/eigen');

    const sections = Array.from(form.querySelectorAll('[data-section-key]')).map((s) =>
      s.getAttribute('data-section-key'),
    );
    expect(sections).toEqual(['second', 'first']);
    const names = Array.from(form.querySelectorAll('[data-field-name]')).map((f) =>
      f.getAttribute('data-field-name'),
    );
    expect(names).toEqual(['vlan', 'ip', 'hostname']);

    expect(within(form).getByLabelText('vlan')).toHaveValue(42);
    expect(within(form).getByLabelText('hostname *')).toHaveValue('srv-01');
  });

  it('keeps inputs local without any write', async () => {
    const calls = stubBackend();
    renderPreview();
    const form = await screen.findByRole('form', { name: 'Formularvorschau' });
    const hostname = await within(form).findByLabelText('hostname *');
    fireEvent.change(hostname, { target: { value: 'srv-99' } });
    fireEvent.change(within(form).getByLabelText('ip'), { target: { value: '10.0.0.1' } });
    fireEvent.submit(form);

    expect(hostname).toHaveValue('srv-99');
    expect(within(form).getByLabelText('ip')).toHaveValue('10.0.0.1');
    expect(writes(calls)).toEqual([]);
  });
});

describe('LayoutPreview role X (WP-803)', () => {
  it('locks the role selection without citype:manage', async () => {
    const calls = stubBackend({ permissions: ['ci:read'] });
    renderPreview({ initialRoleId: ROLE });
    const select = await roleSelect();
    await waitFor(() =>
      expect(
        screen.getByText('Die Vorschau als andere Rolle erfordert das Recht citype:manage.'),
      ).toBeInTheDocument(),
    );
    expect(select).toBeDisabled();
    expect(select.value).toBe('');
    await screen.findByText('Netz typ/eigen');
    expect(formCalls(calls).every((c) => !c.query.has('role_id'))).toBe(true);
    expect(screen.getByRole('status')).toHaveTextContent('citype:manage');
  });

  it('shows the note on 403 and loads without role_id', async () => {
    const calls = stubBackend({ roleStatus: 403 });
    renderPreview({ initialRoleId: ROLE });
    expect(await screen.findByText('Netz typ/eigen')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent(
      'Die Vorschau als diese Rolle wurde abgelehnt (citype:manage).',
    );
    const forms = formCalls(calls);
    expect(forms.some((c) => c.query.get('role_id') === ROLE)).toBe(true);
    const fallback = forms[forms.length - 1]!.query;
    expect(fallback.has('role_id')).toBe(false);
    expect(fallback.has('preview')).toBe(false);
  });
});

describe('LayoutPreview draft and editor state (WP-803)', () => {
  it('shows the published state without a draft', async () => {
    stubBackend();
    renderPreview();
    await screen.findByText('Netz typ/eigen');
    expect(screen.getByRole('status')).toHaveTextContent('veröffentlichten Stand');
  });

  it('adds draft=<id> with an active draft', async () => {
    const calls = stubBackend();
    act(() => useMetamodelDraftStore.getState().setDraft({ id: 'draft-1', name: 'Umbau' }));
    renderPreview({ initialRoleId: ROLE });
    await screen.findByText(`Netz typ/${ROLE}`);
    expect(lastForm(calls).query.get('draft')).toBe('draft-1');
    expect(screen.getByRole('status')).toHaveTextContent('Stand des Drafts Umbau');
  });

  it('notes unsaved changes in the editor', async () => {
    stubBackend();
    renderPreview({ unsaved: true });
    await screen.findByText('Netz typ/eigen');
    expect(screen.getByRole('status')).toHaveTextContent(
      'Der Editor enthält ungespeicherte Änderungen.',
    );
  });

  it('names the controls and the notes in English', async () => {
    stubBackend();
    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    renderPreview({ unsaved: true });
    expect(await screen.findByText('Network typ/eigen')).toBeInTheDocument();
    expect(screen.getByLabelText('Template of the preview')).toBeInTheDocument();
    expect(screen.getByLabelText('Role of the preview')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('The editor has unsaved changes.');
  });
});
