/**
 * WP-316 (MET-50, UI-11 [P2]): the form builder /ci-types/{id}/form-builder.
 *
 *   Entry     "Formular gestalten" in the type detail of CITypeAdminPage
 *             leads to the page and is missing with the module metamodel off
 *   Target    type or template of the type chain (GET /templates, never
 *             /ci-types/templates) and role (GET /roles), both in the URL
 *   Source    the own layout of target and role (GET /layouts), else a copy
 *             of the resolved form (with role_id and preview=true for a
 *             role) without _instance and _required, headed "Geerbt von
 *             <Typ>" (GET /layouts/{layout_id}) or "Standardlayout"
 *   Origin    own, inherited and global per field of the palette
 *   Publish   after the confirmation POST /layouts or PATCH /layouts/{id}
 *             with If-Match; 412 reloads with a note; 409 as a note; 422
 *             with field layout.sections[1].fields[0].name marks the field;
 *             403 with the trace id; "Eigenes Layout entfernen" sends DELETE
 *             and shows the inherited layout again
 *   Draft     with an active draft exactly one write with draft={id}, no
 *             follow-up call after the 202, the badge "im Draft"
 *   Module    a 404 of GET /layouts shows a note instead of the editor
 *   Model     no POST/PUT/PATCH/DELETE on /ci-types/{id}/fields,
 *             /ci-fields/global or /attribute-definitions (MET-50)
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/FormBuilderPage.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes, useLocation } from 'react-router';
import { renderWithProviders } from '../test/utils';
import { useAuthStore } from '../auth/authStore';
import { useMetamodelDraftStore } from '../stores/metamodelDraft';
import { FormBuilderPage, copyOfResolved, layoutKeyFor } from './FormBuilderPage';
import { CITypeAdminPage } from './CITypeAdminPage';
import i18n from '../i18n';

type Json = Record<string, unknown>;

interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
  headers: Headers;
  body: Json | undefined;
}

const AT = '2026-01-01T00:00:00Z';
const DRAFT = 'draft-1';
const ROLE = 'role-tech';

function json(body: unknown, status = 200, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function problem(status: number, body: Json) {
  return new Response(JSON.stringify({ status, title: 'Problem', ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function ciType(id: string, key: string, name: string, extra: Json = {}): Json {
  return {
    id,
    key,
    name,
    display_name: { de: name, en: name },
    is_builtin: false,
    is_system: false,
    is_active: true,
    is_logical: false,
    is_observable: false,
    schema_version: 1,
    version: 1,
    capabilities: {},
    allowed_relationship_types: [],
    ui_schema: {},
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const BASE = ciType('t-base', 'device', 'Gerät');
const NET = ciType('t-net', 'network', 'Netzwerk', { parent_type_id: 't-base' });
const OTHER = ciType('t-other', 'printer', 'Drucker');

function definition(key: string, extra: Json = {}): Json {
  return {
    id: `d-${key}`,
    key,
    scope: 'type',
    ci_type_id: 't-net',
    display_name: { de: key, en: key },
    data_type: 'text',
    required: false,
    multi_value: false,
    searchable: false,
    unique: false,
    unique_scope: 'org',
    inheritable: true,
    order: 0,
    is_system: false,
    applies_to: 'all',
    applies_to_categories: [],
    applies_to_type_ids: [],
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const OWN_DEF = definition('vendor', { required: true });
const GLOBAL_DEF = definition('cost_center', { scope: 'global', ci_type_id: undefined });
const INHERITED_DEF = definition('serial', { ci_type_id: 't-base', data_type: 'integer' });

function template(id: string, key: string, typeId: string): Json {
  return {
    id,
    organization_id: 'org-1',
    key,
    ci_type_id: typeId,
    display_name: { de: key, en: key },
    is_system: false,
    is_active: true,
    current_version: 1,
    created_at: AT,
    updated_at: AT,
  };
}

const TEMPLATE = template('tpl-1', 'core_switch', 't-base');
const FOREIGN_TEMPLATE = template('tpl-2', 'laser', 't-other');

function layout(id: string, extra: Json = {}): Json {
  return {
    id,
    organization_id: 'org-1',
    key: 'network',
    ci_type_id: 't-net',
    priority: 0,
    version: 3,
    is_active: true,
    layout: {
      sections: [
        {
          key: 'main',
          title: { de: 'Allgemein', en: 'General' },
          visible_when: [],
          fields: [{ name: 'vendor', width: 6 }],
        },
        {
          key: 'more',
          title: { de: 'Mehr', en: 'More' },
          visible_when: [],
          fields: [{ name: 'serial', width: 12 }],
        },
      ],
    },
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const OWN = layout('l-own');
const PARENT_LAYOUT = layout('l-base', { ci_type_id: 't-base', key: 'device' });

function resolvedFrom(source: 'layout' | 'default', extra: Json = {}): Json {
  const field = (name: string, width: number) => ({
    name,
    width,
    definition: { name, data_type: 'text', required: false, sort_order: 0 },
    state: { visible: true, required: false, read_only: false },
  });
  return {
    ci_type_id: 't-net',
    source,
    ...(source === 'layout' ? { layout_id: 'l-base' } : {}),
    sections: [
      {
        key: 'base',
        title: { de: 'Basis', en: 'Base' },
        visible: true,
        fields: [field('serial', 4)],
      },
      { key: '_instance', title: { de: 'Instanz', en: 'Instance' }, visible: true, fields: [] },
      {
        key: '_required',
        title: { de: 'Pflichtfelder', en: 'Required' },
        visible: true,
        fields: [field('vendor', 12)],
      },
    ],
    rules: [],
    ...extra,
  };
}

interface Backend {
  modules?: Json[];
  layouts?: Json[];
  layoutsStatus?: number;
  resolved?: Json;
  resolvedStatus?: number;
  post?: (body: Json) => Response;
  patch?: (body: Json) => Response;
  remove?: () => Response;
  draftItems?: Json[];
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  let layouts = b.layouts ?? [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const q = url.searchParams;
      calls.push({ method, path, query: q, headers: new Headers(init?.headers), body });
      if (q.has('draft') && method !== 'GET') {
        return json(
          {
            id: 'item-new',
            kind: 'layout',
            op: method === 'POST' ? 'create' : method === 'PATCH' ? 'update' : 'delete',
            target_id: method === 'POST' ? null : path.split('/').pop(),
            target_key: null,
            payload: body ?? {},
            base: null,
            schema_versions: {},
            route: `${method} /api/v1${path}`,
            created_by: 'u1',
            created_at: AT,
          },
          202,
        );
      }
      if (path === '/me/permissions') {
        return json({ user_id: 'u1', permissions: ['ci:read', 'citype:manage'] });
      }
      if (path === '/modules') return json({ data: b.modules ?? [] });
      if (path === '/ci-types/t-net') return json(NET);
      if (path === '/ci-types') {
        return json({ data: [BASE, NET, OTHER], total: 3, limit: 200, offset: 0, has_more: false });
      }
      if (path === '/attribute-definitions') {
        if (q.get('applicable_to') === 't-net') return json({ data: [OWN_DEF, GLOBAL_DEF] });
        if (q.get('applicable_to') === 't-base') return json({ data: [INHERITED_DEF, GLOBAL_DEF] });
        if (q.get('ci_type_id') === 't-base') return json({ data: [INHERITED_DEF] });
        return json({ data: [] });
      }
      if (path === '/attribute-sets') return json({ data: [], has_more: false });
      if (/^\/ci-types\/[^/]+\/attribute-sets$/.test(path)) {
        return json({ ci_type_id: path.split('/')[2], attribute_sets: [] });
      }
      if (path === '/templates') {
        return json({ data: [TEMPLATE, FOREIGN_TEMPLATE], has_more: false });
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
      if (path === '/rules') {
        return json({
          data: [
            {
              id: 'r-1',
              key: 'is_core',
              attribute_key: 'vendor',
              kind: 'visible',
              ci_type_id: 't-net',
              is_system: false,
              is_active: true,
              version: 1,
              created_at: AT,
              updated_at: AT,
            },
          ],
        });
      }
      if (path === `/metamodel/drafts/${DRAFT}`) {
        return json({ id: DRAFT, name: 'Umbau', status: 'draft', item_count: 0 });
      }
      if (path === `/metamodel/drafts/${DRAFT}/items`) {
        return json({ data: b.draftItems ?? [], has_more: false });
      }
      if (path === '/ci-types/t-net/form') {
        if (b.resolvedStatus) {
          return problem(b.resolvedStatus, { detail: 'forbidden', trace_id: 'trace-r' });
        }
        return json(b.resolved ?? resolvedFrom('default'));
      }
      if (path === '/layouts' && method === 'GET') {
        if (b.layoutsStatus) return problem(b.layoutsStatus, { detail: 'not found' });
        const tpl = q.get('template_id');
        const typeId = q.get('ci_type_id');
        return json({
          data: layouts.filter((l) => (tpl ? l.template_id === tpl : l.ci_type_id === typeId)),
          has_more: false,
        });
      }
      if (path === '/layouts' && method === 'POST') {
        if (b.post) return b.post(body!);
        const created = layout('l-new', { ...body, version: 1 });
        layouts = [...layouts, created];
        return json(created, 201, { ETag: '"1"' });
      }
      const one = /^\/layouts\/([^/]+)$/.exec(path);
      if (one && method === 'GET') {
        return json([...layouts, PARENT_LAYOUT].find((l) => l.id === one[1]) ?? PARENT_LAYOUT);
      }
      if (one && method === 'PATCH') {
        if (b.patch) return b.patch(body!);
        const current = layouts.find((l) => l.id === one[1])!;
        const updated = { ...current, ...body, version: Number(current.version) + 1 };
        layouts = layouts.map((l) => (l.id === one[1] ? updated : l));
        return json(updated);
      }
      if (one && method === 'DELETE') {
        if (b.remove) return b.remove();
        layouts = layouts.filter((l) => l.id !== one[1]);
        return new Response(null, { status: 204 });
      }
      return json({ data: [] });
    }),
  );
  return calls;
}

function Location() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname + location.search}</output>;
}

function renderAt(route = '/ci-types/t-net/form-builder') {
  return renderWithProviders(
    <>
      <Routes>
        <Route path="/ci-types" element={<CITypeAdminPage />} />
        <Route path="/ci-types/:id/form-builder" element={<FormBuilderPage />} />
      </Routes>
      <Location />
    </>,
    { route },
  );
}

const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET');
const editorReady = () => screen.findByRole('list', { name: 'Abschnitte' });

async function publish() {
  fireEvent.click(screen.getByRole('button', { name: 'Veröffentlichen' }));
  const dialog = await screen.findByRole('dialog', { name: 'Layout veröffentlichen?' });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Veröffentlichen' }));
}

/** The data model stays untouched: no write on fields or attribute definitions. */
function expectNoModelWrites(calls: Call[]) {
  expect(
    writes(calls).filter((c) =>
      /^\/(ci-types\/[^/]+\/fields|ci-fields\/global|attribute-definitions)/.test(c.path),
    ),
  ).toEqual([]);
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => undefined;
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

describe('FormBuilderPage entry (WP-316)', () => {
  it('opens from "Formular gestalten" in the type detail', async () => {
    stubBackend();
    renderAt('/ci-types');
    fireEvent.click(await screen.findByRole('treeitem', { name: /Netzwerk/ }));
    const link = await screen.findByRole('link', { name: 'Formular gestalten' });
    fireEvent.click(link);

    expect(
      await screen.findByRole('heading', { name: 'Formular gestalten: Netzwerk' }),
    ).toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent('/ci-types/t-net/form-builder');
  });

  it('has no entry with the module metamodel switched off', async () => {
    stubBackend({ modules: [{ key: 'metamodel', phase: 'P2', nav: [], enabled: false }] });
    renderAt('/ci-types');
    fireEvent.click(await screen.findByRole('treeitem', { name: /Netzwerk/ }));
    await screen.findByRole('button', { name: 'Klonen' });
    await waitFor(() =>
      expect(screen.queryByRole('link', { name: 'Formular gestalten' })).not.toBeInTheDocument(),
    );
  });

  it('shows a note instead of the editor when GET /layouts answers 404', async () => {
    stubBackend({ layoutsStatus: 404 });
    renderAt();
    expect(await screen.findByText(/Das Modul Metamodell ist abgeschaltet/)).toBeInTheDocument();
    expect(screen.queryByRole('list', { name: 'Abschnitte' })).not.toBeInTheDocument();
  });
});

describe('FormBuilderPage target and source (WP-316)', () => {
  it('offers the type and the templates of the type chain and the roles, in the URL', async () => {
    const calls = stubBackend();
    renderAt();
    await editorReady();

    const target = screen.getByRole('combobox', { name: 'Ziel' });
    await waitFor(() =>
      expect(
        within(target)
          .getAllByRole('option')
          .map((o) => o.textContent),
      ).toEqual(['Typ Netzwerk', 'Template core_switch']),
    );
    const role = screen.getByRole('combobox', { name: 'Rolle' });
    await waitFor(() =>
      expect(
        within(role)
          .getAllByRole('option')
          .map((o) => o.textContent),
      ).toEqual(['Ohne Rolle', 'Techniker']),
    );
    expect(calls.some((c) => c.path === '/ci-types/templates')).toBe(false);
    expect(calls.some((c) => c.path === '/templates')).toBe(true);
    expect(calls.some((c) => c.path === '/roles')).toBe(true);

    fireEvent.change(target, { target: { value: 'tpl-1' } });
    fireEvent.change(role, { target: { value: ROLE } });
    await waitFor(() =>
      expect(screen.getByTestId('location')).toHaveTextContent(
        `/ci-types/t-net/form-builder?template_id=tpl-1&role_id=${ROLE}`,
      ),
    );
    await waitFor(() =>
      expect(
        calls.some((c) => c.path === '/layouts' && c.query.get('template_id') === 'tpl-1'),
      ).toBe(true),
    );
    await waitFor(() => {
      const form = calls.filter((c) => c.path === '/ci-types/t-net/form').pop()!;
      expect(form.query.get('template_id')).toBe('tpl-1');
      expect(form.query.get('role_id')).toBe(ROLE);
      expect(form.query.get('preview')).toBe('true');
    });
  });

  it('loads the own layout of target and role', async () => {
    const calls = stubBackend({ layouts: [OWN] });
    renderAt();
    await editorReady();
    expect(await screen.findByText(/Eigenes Layout network, Version 3/)).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Titel (DE) von main' })).toHaveValue('Allgemein');
    expect(calls.some((c) => c.path === '/ci-types/t-net/form')).toBe(false);
  });

  it('starts from the resolved form without _instance and _required, inherited from the parent', async () => {
    const calls = stubBackend({ resolved: resolvedFrom('layout') });
    renderAt();
    await editorReady();

    expect(await screen.findByText('Geerbt von Gerät')).toBeInTheDocument();
    expect(calls.some((c) => c.path === '/layouts/l-base')).toBe(true);
    const form = calls.find((c) => c.path === '/ci-types/t-net/form')!;
    expect(form.query.has('role_id')).toBe(false);
    expect(screen.getByRole('textbox', { name: 'Titel (DE) von base' })).toHaveValue('Basis');
    expect(screen.queryByRole('textbox', { name: /_instance/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: /_required/ })).not.toBeInTheDocument();
    // The required field without a place is a note: the server appends it.
    expect(screen.getByRole('note')).toHaveTextContent(
      'Pflichtfelder ohne Platz im Layout: vendor',
    );

    expect(copyOfResolved(resolvedFrom('default') as never).sections.map((s) => s.key)).toEqual([
      'base',
    ]);
  });

  it('heads the standard layout and names the role of a copy', async () => {
    stubBackend({ resolved: resolvedFrom('default', { role_id: ROLE }) });
    renderAt();
    await editorReady();
    expect(await screen.findByText('Standardlayout')).toBeInTheDocument();
    expect(
      await screen.findByText('Kopie des Formulars für die Rolle Techniker'),
    ).toBeInTheDocument();
  });

  it('shows the origin of each field: own, inherited and global', async () => {
    stubBackend({ resolved: resolvedFrom('layout') });
    renderAt();
    await editorReady();
    const placed = screen.getByRole('list', { name: 'Felder von Basis' });
    await waitFor(() => expect(within(placed).getByText('geerbt von Gerät')).toBeInTheDocument());
    const palette = screen.getByRole('list', { name: 'Feldpalette' });
    const vendor = palette.querySelector<HTMLElement>('[data-palette-field="vendor"]')!;
    expect(within(vendor).getByText('eigen')).toBeInTheDocument();
    expect(within(vendor).getByText('Pflicht')).toBeInTheDocument();
    const cost = palette.querySelector<HTMLElement>('[data-palette-field="cost_center"]')!;
    expect(within(cost).getByText('global')).toBeInTheDocument();
  });

  it('shows a 403 of the resolved form of another role with the trace id', async () => {
    stubBackend({ resolvedStatus: 403 });
    renderAt(`/ci-types/t-net/form-builder?role_id=${ROLE}`);
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Das Formular dieser Rolle darf nur mit dem Recht citype:manage geladen werden. Trace-ID: trace-r',
    );
  });

  it('proposes a layout key from target and role', () => {
    expect(layoutKeyFor('network')).toBe('network');
    expect(layoutKeyFor('network', 'Techniker Netz')).toBe('network_techniker_netz');
  });
});

describe('FormBuilderPage publish (WP-316)', () => {
  it('creates an own layout with POST after the confirmation', async () => {
    const calls = stubBackend();
    renderAt(`/ci-types/t-net/form-builder?role_id=${ROLE}`);
    await editorReady();
    const key = await screen.findByRole('textbox', { name: 'Schlüssel des Layouts' });
    await waitFor(() => expect(key).toHaveValue('network_techniker'));
    fireEvent.change(key, { target: { value: 'net_tech' } });

    await publish();
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const post = writes(calls)[0]!;
    expect(post.method).toBe('POST');
    expect(post.path).toBe('/layouts');
    expect(post.query.has('draft')).toBe(false);
    expect(post.body).toEqual({
      key: 'net_tech',
      ci_type_id: 't-net',
      role_id: ROLE,
      priority: 0,
      is_active: true,
      layout: {
        sections: [
          {
            key: 'base',
            title: { de: 'Basis', en: 'Base' },
            visible_when: [],
            fields: [{ name: 'serial', width: 4 }],
          },
        ],
      },
    });
    expect(await screen.findByText(/Eigenes Layout net_tech, Version 1/)).toBeInTheDocument();
    expectNoModelWrites(calls);
  });

  it('changes the own layout with PATCH and If-Match', async () => {
    const calls = stubBackend({ layouts: [OWN] });
    renderAt();
    await editorReady();
    fireEvent.change(screen.getByRole('combobox', { name: 'Breite: vendor' }), {
      target: { value: '3' },
    });
    await publish();

    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const patch = writes(calls)[0]!;
    expect(patch.method).toBe('PATCH');
    expect(patch.path).toBe('/layouts/l-own');
    expect(patch.headers.get('If-Match')).toBe('"3"');
    expect(
      (patch.body!.layout as { sections: { fields: unknown[] }[] }).sections[0]!.fields,
    ).toEqual([{ name: 'vendor', width: 3 }]);
    expect(await screen.findByText(/Version 4/)).toBeInTheDocument();
    expectNoModelWrites(calls);
  });

  it('reloads after a 412 and shows a note', async () => {
    let reads = 0;
    const calls = stubBackend({
      layouts: [OWN],
      patch: () => problem(412, { detail: 'version mismatch' }),
    });
    const base = vi.mocked(fetch).getMockImplementation()!;
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/layouts?') && (init?.method ?? 'GET') === 'GET') {
        reads += 1;
        if (reads > 1) {
          return json({ data: [{ ...OWN, version: 5 }], has_more: false });
        }
      }
      return base(input, init);
    });
    renderAt();
    await editorReady();
    fireEvent.change(screen.getByRole('combobox', { name: 'Breite: vendor' }), {
      target: { value: '3' },
    });
    await publish();

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Das Layout wurde inzwischen geändert und neu geladen.',
    );
    expect(await screen.findByText(/Version 5/)).toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: 'Breite: vendor' })).toHaveValue('6');
    expect(writes(calls)).toHaveLength(1);
  });

  it('shows a 409 as a note at the key', async () => {
    stubBackend({ post: () => problem(409, { detail: 'layout exists' }) });
    renderAt();
    await editorReady();
    await publish();
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Für dieses Ziel und diese Rolle gibt es schon ein Layout',
    );
    expect(
      screen.getByRole('textbox', { name: 'Schlüssel des Layouts' }),
    ).toHaveAccessibleDescription('Schlüssel oder Ziel und Rolle sind bereits belegt.');
  });

  it('marks the field a 422 names', async () => {
    const own = layout('l-own', {
      layout: {
        sections: [
          {
            key: 'main',
            title: { de: 'Allgemein', en: 'General' },
            visible_when: [],
            fields: [{ name: 'vendor', width: 6 }],
          },
          {
            key: 'more',
            title: { de: 'Mehr', en: 'More' },
            visible_when: [],
            fields: [{ name: 'serial', width: 12 }],
          },
        ],
      },
    });
    stubBackend({
      layouts: [own],
      patch: () =>
        problem(422, {
          type: 'urn:reticora:problem:invalid-field',
          field: 'layout.sections[1].fields[0].name',
          detail: 'the target has no field with this name',
        }),
    });
    renderAt();
    await editorReady();
    fireEvent.change(screen.getByRole('combobox', { name: 'Breite: vendor' }), {
      target: { value: '3' },
    });
    await publish();

    await waitFor(() =>
      expect(document.querySelector('[data-field-name="serial"]')).toHaveAttribute(
        'data-invalid',
        'true',
      ),
    );
    const serial = document.querySelector<HTMLElement>('[data-field-name="serial"]')!;
    expect(within(serial).getByRole('alert')).toHaveTextContent(
      'the target has no field with this name',
    );
  });

  it('shows a 403 on save with the trace id', async () => {
    stubBackend({
      post: () => problem(403, { detail: 'missing permission', trace_id: 'trace-1' }),
    });
    renderAt();
    await editorReady();
    await publish();
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Keine Berechtigung zum Bearbeiten der Formulare (citype:manage). Trace-ID: trace-1',
    );
  });

  it('removes the own layout with DELETE and shows the inherited layout again', async () => {
    const calls = stubBackend({ layouts: [OWN], resolved: resolvedFrom('layout') });
    renderAt();
    await editorReady();
    fireEvent.click(await screen.findByRole('button', { name: 'Eigenes Layout entfernen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Eigenes Layout entfernen?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Eigenes Layout entfernen' }));

    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const del = writes(calls)[0]!;
    expect(del.method).toBe('DELETE');
    expect(del.path).toBe('/layouts/l-own');
    expect(del.headers.get('If-Match')).toBe('"3"');
    expect(await screen.findByText('Geerbt von Gerät')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Titel (DE) von base' })).toHaveValue('Basis');
  });
});

describe('FormBuilderPage draft (WP-316)', () => {
  it('saves in the active draft with one write and no follow-up call', async () => {
    act(() => useMetamodelDraftStore.getState().setDraft({ id: DRAFT, name: 'Umbau' }));
    const calls = stubBackend({
      layouts: [OWN],
      draftItems: [
        {
          id: 'item-1',
          kind: 'layout',
          op: 'update',
          target_id: 'l-own',
          target_key: 'network',
          payload: {},
          base: null,
          schema_versions: {},
          route: 'PATCH /api/v1/layouts/l-own',
          created_by: 'u1',
          created_at: AT,
        },
      ],
    });
    renderAt();
    await editorReady();
    expect(await screen.findByText('im Draft: geändert')).toBeInTheDocument();
    fireEvent.change(screen.getByRole('combobox', { name: 'Breite: vendor' }), {
      target: { value: '3' },
    });
    const before = calls.length;
    fireEvent.click(screen.getByRole('button', { name: 'Im Draft speichern' }));

    expect(await screen.findByText('Im Draft Umbau gespeichert.')).toBeInTheDocument();
    expect(writes(calls)).toHaveLength(1);
    const write = writes(calls)[0]!;
    expect(write.method).toBe('PATCH');
    expect(write.query.get('draft')).toBe(DRAFT);
    // No read of a layout by an id and no second write after the 202.
    const after = calls.slice(before).filter((c) => c !== write);
    expect(after.filter((c) => c.path.startsWith('/layouts'))).toEqual([]);
    // The editor keeps its state.
    expect(screen.getByRole('combobox', { name: 'Breite: vendor' })).toHaveValue('3');
    expectNoModelWrites(calls);
  });
});
