/**
 * WP-314 (UI-11 [P2]): templates of MET-16 in the metamodel administration.
 *
 *   List      GET /templates, all pages of 200 over next_cursor; own and
 *             system templates (badge, only clone); create (POST), deactivate
 *             (PATCH is_active); filter ci_type_id on the server, active and
 *             inactive in the page
 *   Versions  "new version" as a draft copy (POST …/versions); the editor of a
 *             draft: order by keyboard and buttons, PUT …/versions/{v} with
 *             position, a default of a wrong type refused before the save, a
 *             422 at its item; a published version is read-only
 *   Compare   MetamodelDiff of two versions: added, removed, changed items
 *   Publish   only with metamodel:publish, after a confirmation; a
 *             composition conflict (422, field = attribute key) at the item
 *   Tabs      "Types"/"Templates" as links with aria-current in
 *             CITypeAdminPage and TemplatesPage; no "Templates" with the
 *             module metamodel off; "From type template" still reads
 *             GET /ci-types/templates
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/TemplatesPage.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import { renderWithProviders } from '../test/utils';
import { useAuthStore } from '../auth/authStore';
import { TemplatesPage } from './TemplatesPage';
import { CITypeAdminPage } from './CITypeAdminPage';
import { diffEntries } from '../components/metamodel/MetamodelDiff';

type Json = Record<string, unknown>;

interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
  body: Json | undefined;
}

const AT = '2026-01-01T00:00:00Z';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, body: Json) {
  return new Response(JSON.stringify({ status, title: 'Problem', ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function ciType(id: string, name: string, extra: Json = {}): Json {
  return {
    id,
    key: name.toLowerCase(),
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

const NET = ciType('t-net', 'Netzwerk');
const ROUTER = ciType('t-router', 'Router', { parent_type_id: 't-net' });
const PRINTER = ciType('t-printer', 'Drucker');

function definition(id: string, key: string, extra: Json = {}): Json {
  return {
    id,
    key,
    scope: 'type',
    ci_type_id: 't-router',
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

const DEFINITIONS: Record<string, Json[]> = {
  't-router': [
    definition('d-host', 'hostname'),
    definition('d-ports', 'ports', { data_type: 'integer' }),
    definition('d-role', 'role', { data_type: 'enum', enum_values: ['core', 'edge', 'access'] }),
  ],
  't-net': [definition('d-vendor', 'vendor', { ci_type_id: 't-net' })],
};
const GLOBALS = [definition('d-cost', 'cost_center', { scope: 'global', ci_type_id: undefined })];

function template(id: string, key: string, extra: Json = {}): Json {
  return {
    id,
    key,
    ci_type_id: 't-router',
    display_name: { de: key, en: key },
    is_system: false,
    is_active: true,
    current_version: 1,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const OWN = template('tp-own', 'Routerbasis');
const SYSTEM = template('tp-sys', 'Systemvorlage', { is_system: true, organization_id: '' });
const PRINTERS = template('tp-print', 'Druckerbasis', { ci_type_id: 't-printer' });

function item(id: string, key: string, position: number, extra: Json = {}): Json {
  return { attribute_definition_id: id, key, position, ...extra };
}

function version(n: number, status: string, items: Json[]): Json {
  return {
    id: `v-${n}`,
    template_id: 'tp-own',
    version: n,
    status,
    created_at: AT,
    updated_at: AT,
    items,
  };
}

const V1 = version(1, 'published', [
  item('d-host', 'hostname', 0),
  item('d-ports', 'ports', 1, { default_override: 24 }),
  item('d-vendor', 'vendor', 2),
]);
const V2 = version(2, 'draft', [
  item('d-host', 'hostname', 0, { required_override: true }),
  item('d-ports', 'ports', 1, { default_override: 48 }),
  item('d-role', 'role', 2),
]);

interface Backend {
  permissions?: string[];
  modules?: Json[];
  templates?: Json[];
  versions?: Json[];
  put?: (body: Json) => Response;
  publish?: () => Response;
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  let templates = b.templates ?? [OWN, SYSTEM];
  let versions = b.versions ?? [V2, V1];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const q = url.searchParams;
      calls.push({ method, path, query: q, body });
      if (path === '/me/permissions') {
        return json({ user_id: 'u1', permissions: b.permissions ?? ['citype:manage'] });
      }
      if (path === '/modules') return json({ data: b.modules ?? [] });
      if (path === '/ci-types/templates') return json({ data: [] });
      if (path === '/ci-types') {
        const data = [NET, ROUTER, PRINTER];
        return json({ data, total: data.length, limit: 200, offset: 0, has_more: false });
      }
      if (path === '/attribute-definitions') {
        if (q.get('scope') === 'global') return json({ data: GLOBALS });
        return json({ data: DEFINITIONS[q.get('ci_type_id') ?? ''] ?? [] });
      }
      if (path === '/templates' && method === 'GET') {
        const typeId = q.get('ci_type_id');
        const all = templates.filter((x) => !typeId || x.ci_type_id === typeId);
        // Two pages: the last template comes over next_cursor.
        if (!q.get('cursor') && all.length > 1) {
          return json({ data: all.slice(0, -1), next_cursor: 'page-2', has_more: true });
        }
        return json({ data: q.get('cursor') ? all.slice(-1) : all, has_more: false });
      }
      if (path === '/templates' && method === 'POST') {
        const created = template('tp-new', String(body!.key), { ...body, current_version: null });
        templates = [created, ...templates];
        return json(created, 201);
      }
      const one = /^\/templates\/([^/]+)$/.exec(path);
      if (one && method === 'PATCH') {
        templates = templates.map((x) => (x.id === one[1] ? { ...x, ...body } : x));
        return json(templates.find((x) => x.id === one[1]));
      }
      if (one) return json(templates.find((x) => x.id === one[1]));
      const list = /^\/templates\/([^/]+)\/versions$/.exec(path);
      if (list && method === 'POST') {
        const next = Math.max(...versions.map((v) => Number(v.version))) + 1;
        const created = version(next, 'draft', V1.items as Json[]);
        versions = [created, ...versions];
        return json(created, 201);
      }
      if (list) return json({ data: versions, has_more: false });
      const publish = /^\/templates\/([^/]+)\/versions\/(\d+)\/publish$/.exec(path);
      if (publish) {
        if (b.publish) return b.publish();
        versions = versions.map((v) =>
          v.version === Number(publish[2]) ? { ...v, status: 'published' } : v,
        );
        return json(versions.find((v) => v.version === Number(publish[2])));
      }
      const put = /^\/templates\/([^/]+)\/versions\/(\d+)$/.exec(path);
      if (put && method === 'PUT') {
        if (b.put) return b.put(body!);
        const n = Number(put[2]);
        versions = versions.map((v) =>
          v.version === n ? { ...v, items: body!.items, updated_at: '2026-02-01T00:00:00Z' } : v,
        );
        return json(versions.find((v) => v.version === n));
      }
      return json({ data: [] });
    }),
  );
  return calls;
}

function renderAt(route: string) {
  return renderWithProviders(
    <Routes>
      <Route path="/ci-types" element={<CITypeAdminPage />} />
      <Route path="/ci-types/templates" element={<TemplatesPage />} />
      <Route path="/ci-types/templates/:id" element={<TemplatesPage />} />
    </Routes>,
    { route },
  );
}

/** Lays the entries of a sortable list out one below the other (jsdom has no layout). */
function layoutRows() {
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    const index = this.getAttribute('data-sortable-index');
    const top = index === null ? 0 : Number(index) * 40;
    const height = index === null ? 0 : 40;
    const width = index === null ? 0 : 400;
    return {
      x: 0,
      y: top,
      top,
      left: 0,
      width,
      height,
      right: width,
      bottom: top + height,
      toJSON: () => ({}),
    } as DOMRect;
  });
}

/** The KeyboardSensor listens for the next key after a tick. */
async function key(target: Element, code: string) {
  await act(async () => {
    fireEvent.keyDown(target, { key: code === 'Space' ? ' ' : code, code });
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

const itemList = (n: number) => screen.getByRole('list', { name: `Items der Version ${n}` });
const itemKeys = (n: number) =>
  within(itemList(n))
    .getAllByRole('listitem')
    .filter((li) => li.querySelector('[data-item-key]'))
    .map((li) => li.querySelector('[data-item-key]')!.getAttribute('data-item-key'));
const itemOf = (n: number, k: string) =>
  within(itemList(n))
    .getAllByRole('listitem')
    .find((li) => li.querySelector(`[data-item-key="${k}"]`))!;

const templateRow = (name: string) =>
  within(screen.getByRole('list', { name: 'Templates' }))
    .getByRole('link', { name })
    .closest('li')!;

beforeEach(() => {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
    user: { sub: 'u1' },
  });
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => undefined;
});

afterEach(() => {
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('TemplatesPage list', () => {
  it('lists own and system templates over all pages; the system template offers only clone', async () => {
    const calls = stubBackend();
    renderAt('/ci-types/templates');

    await screen.findByRole('link', { name: 'Systemvorlage' });
    const reads = calls.filter((c) => c.method === 'GET' && c.path === '/templates');
    expect(reads.map((c) => c.query.get('limit'))).toEqual(['200', '200']);
    expect(reads[1]!.query.get('cursor')).toBe('page-2');

    const sys = templateRow('Systemvorlage');
    expect(within(sys).getByText('System')).toBeInTheDocument();
    expect(
      within(sys).getByRole('button', { name: 'Template klonen: Systemvorlage' }),
    ).toBeInTheDocument();
    expect(within(sys).queryByRole('button', { name: /bearbeiten/ })).not.toBeInTheDocument();
    expect(within(sys).queryByRole('button', { name: /deaktivieren/ })).not.toBeInTheDocument();

    const own = templateRow('Routerbasis');
    expect(within(own).queryByText('System')).not.toBeInTheDocument();
    expect(
      within(own).getByRole('button', { name: 'Template bearbeiten: Routerbasis' }),
    ).toBeInTheDocument();
  });

  it('clones a system template with a new key', async () => {
    const calls = stubBackend();
    renderAt('/ci-types/templates');
    fireEvent.click(await screen.findByRole('button', { name: 'Template klonen: Systemvorlage' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Schlüssel'), {
      target: { value: 'my_router' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Klonen' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const clone = calls.find((c) => c.method === 'POST' && c.path === '/templates/tp-sys/clone')!;
    expect(clone.body).toMatchObject({ key: 'my_router' });
  });

  it('creates a template and deactivates one by PATCH', async () => {
    const calls = stubBackend();
    renderAt('/ci-types/templates');
    fireEvent.click(await screen.findByRole('button', { name: 'Template anlegen' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Schlüssel'), { target: { value: 'edge' } });
    await waitFor(() =>
      expect(within(dialog).getByRole('option', { name: 'Router' })).toBeInTheDocument(),
    );
    fireEvent.change(within(dialog).getByLabelText('CI-Typ'), { target: { value: 't-router' } });
    fireEvent.change(within(dialog).getByLabelText('Anzeigename (Deutsch)'), {
      target: { value: 'Edge-Vorlage' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const post = calls.find((c) => c.method === 'POST' && c.path === '/templates')!;
    expect(post.body).toMatchObject({
      key: 'edge',
      ci_type_id: 't-router',
      display_name: { de: 'Edge-Vorlage' },
    });

    fireEvent.click(
      await screen.findByRole('button', { name: 'Template deaktivieren: Routerbasis' }),
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'PATCH')).toMatchObject({
        path: '/templates/tp-own',
        body: { is_active: false },
      }),
    );
    expect(
      await screen.findByRole('button', { name: 'Template aktivieren: Routerbasis' }),
    ).toBeInTheDocument();
  });

  it('filters by ci_type_id on the server and by activity in the page', async () => {
    const calls = stubBackend({
      templates: [OWN, PRINTERS, template('tp-off', 'Altvorlage', { is_active: false })],
    });
    renderAt('/ci-types/templates');
    await screen.findByRole('link', { name: 'Altvorlage' });

    fireEvent.change(screen.getByLabelText('Aktivität'), { target: { value: 'inactive' } });
    expect(screen.queryByRole('link', { name: 'Routerbasis' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Altvorlage' })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Aktivität'), { target: { value: 'all' } });

    await waitFor(() =>
      expect(screen.getByRole('option', { name: 'Drucker' })).toBeInTheDocument(),
    );
    fireEvent.change(screen.getByLabelText('CI-Typ'), { target: { value: 't-printer' } });
    await waitFor(() =>
      expect(screen.queryByRole('link', { name: 'Routerbasis' })).not.toBeInTheDocument(),
    );
    expect(await screen.findByRole('link', { name: 'Druckerbasis' })).toBeInTheDocument();
    const filtered = calls.filter(
      (c) => c.path === '/templates' && c.query.get('ci_type_id') === 't-printer',
    );
    expect(filtered.length).toBeGreaterThan(0);
  });
});

describe('TemplatesPage versions', () => {
  it('creates a new version as a draft copy of the current one', async () => {
    const calls = stubBackend();
    renderAt('/ci-types/templates/tp-own');
    fireEvent.click(await screen.findByRole('button', { name: 'Neue Version' }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveTextContent('Die neue Version entsteht als Entwurf');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Version anlegen' }));

    await waitFor(() =>
      expect(
        calls.some((c) => c.method === 'POST' && c.path === '/templates/tp-own/versions'),
      ).toBe(true),
    );
    const editor = (await screen.findByRole('heading', { name: 'Items der Version 3' })).closest(
      'section',
    )!;
    expect(within(editor).getByText('Entwurf')).toBeInTheDocument();
    await waitFor(() => expect(itemKeys(3)).toEqual(['hostname', 'ports', 'vendor']));
    expect(within(editor).getByRole('button', { name: 'Version speichern' })).toBeInTheDocument();
  });

  it('reorders items by keyboard and button and saves the positions by PUT', async () => {
    layoutRows();
    const calls = stubBackend();
    renderAt('/ci-types/templates/tp-own');
    await screen.findByRole('heading', { name: 'Items der Version 2' });
    expect(itemKeys(2)).toEqual(['hostname', 'ports', 'role']);

    const handle = screen.getByRole('button', { name: 'Verschieben: hostname' });
    handle.focus();
    await key(handle, 'Space');
    await key(handle, 'ArrowDown');
    await key(handle, 'Space');
    await waitFor(() => expect(itemKeys(2)).toEqual(['ports', 'hostname', 'role']));

    fireEvent.click(screen.getByRole('button', { name: 'Nach oben: role' }));
    expect(itemKeys(2)).toEqual(['ports', 'role', 'hostname']);

    fireEvent.click(screen.getByRole('button', { name: 'Version speichern' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true));
    const put = calls.find((c) => c.method === 'PUT')!;
    expect(put.path).toBe('/templates/tp-own/versions/2');
    expect(put.body).toEqual({
      items: [
        { attribute_definition_id: 'd-ports', position: 0, default_override: 48 },
        { attribute_definition_id: 'd-role', position: 1 },
        { attribute_definition_id: 'd-host', position: 2, required_override: true },
      ],
    });
    expect(await screen.findByText('Version gespeichert.')).toBeInTheDocument();
  });

  it('adds an attribute of the type chain and narrows the allowed values', async () => {
    const calls = stubBackend({ versions: [version(2, 'draft', [item('d-role', 'role', 0)])] });
    renderAt('/ci-types/templates/tp-own');
    await screen.findByRole('heading', { name: 'Items der Version 2' });

    const add = screen.getByLabelText('Attribut hinzufügen');
    await waitFor(() =>
      expect(within(add).getByRole('option', { name: 'vendor (vendor, Typ)' })).toBeInTheDocument(),
    );
    expect(within(add).getByRole('option', { name: 'cost_center (cost_center, global)' }));
    fireEvent.change(add, { target: { value: 'd-vendor' } });
    fireEvent.click(screen.getByRole('button', { name: 'Hinzufügen' }));
    expect(itemKeys(2)).toEqual(['role', 'vendor']);

    const role = itemOf(2, 'role');
    fireEvent.click(within(role).getByRole('checkbox', { name: 'core' }));
    fireEvent.click(within(role).getByRole('checkbox', { name: 'edge' }));
    fireEvent.click(screen.getByRole('button', { name: 'Version speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true));
    expect(calls.find((c) => c.method === 'PUT')!.body).toEqual({
      items: [
        {
          attribute_definition_id: 'd-role',
          position: 0,
          validation_override: { op: 'all', rules: [{ op: 'enum', values: ['core', 'edge'] }] },
        },
        { attribute_definition_id: 'd-vendor', position: 1 },
      ],
    });
  });

  it('refuses a default of the wrong type before the save', async () => {
    const calls = stubBackend();
    renderAt('/ci-types/templates/tp-own');
    await screen.findByRole('heading', { name: 'Items der Version 2' });
    await waitFor(() =>
      expect(within(itemOf(2, 'ports')).getByText('Ganzzahl')).toBeInTheDocument(),
    );

    fireEvent.change(screen.getByLabelText('Default: ports'), { target: { value: 'many' } });
    fireEvent.click(screen.getByRole('button', { name: 'Version speichern' }));

    const alert = await within(itemOf(2, 'ports')).findByRole('alert');
    expect(alert).toHaveTextContent('ganze Zahl');
    expect(calls.some((c) => c.method === 'PUT')).toBe(false);
  });

  it('shows a 422 of the save at its item', async () => {
    stubBackend({
      put: () =>
        problem(422, {
          detail: 'validation failed',
          violations: [
            {
              field: 'items[1].default_override',
              detail: 'items[1].default_override: 48 exceeds the maximum 32',
            },
          ],
        }),
    });
    renderAt('/ci-types/templates/tp-own');
    await screen.findByRole('heading', { name: 'Items der Version 2' });
    fireEvent.click(screen.getByRole('button', { name: 'Version speichern' }));

    const alert = await within(itemOf(2, 'ports')).findByRole('alert');
    expect(alert).toHaveTextContent('48 exceeds the maximum 32');
    expect(within(itemOf(2, 'hostname')).queryByRole('alert')).not.toBeInTheDocument();
  });

  it('shows a published version read-only', async () => {
    stubBackend();
    renderAt('/ci-types/templates/tp-own');
    await screen.findByRole('heading', { name: 'Items der Version 2' });
    fireEvent.click(screen.getByRole('button', { name: /^v1/ }));

    await screen.findByRole('heading', { name: 'Items der Version 1' });
    expect(
      screen.getByText(/Veröffentlichte und zurückgezogene Versionen sind schreibgeschützt/),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Version speichern' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Verschieben/ })).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Default: ports')).not.toBeInTheDocument();
    expect(within(itemOf(1, 'ports')).getByText('Default 24')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /publizieren/ })).not.toBeInTheDocument();
  });

  it('compares two versions: added, removed and changed items', async () => {
    stubBackend();
    renderAt('/ci-types/templates/tp-own');
    const table = await screen.findByRole('table', { name: 'Unterschiede von v1 zu v2' });
    const rows = within(table).getAllByRole('row').slice(1);
    const changeOf = (name: string) =>
      rows.find((r) => within(r).queryByRole('rowheader', { name }))?.getAttribute('data-change');
    expect(changeOf('role')).toBe('added');
    expect(changeOf('vendor')).toBe('removed');
    expect(changeOf('hostname')).toBe('changed');
    expect(changeOf('ports')).toBe('changed');
    const ports = rows.find((r) => within(r).queryByRole('rowheader', { name: 'ports' }))!;
    expect(ports).toHaveTextContent('Default');
    expect(ports).toHaveTextContent('24');
    expect(ports).toHaveTextContent('48');
    expect(within(table).getAllByText('Hinzugefügt')).toHaveLength(1);
    expect(within(table).getAllByText('Entfernt')).toHaveLength(1);
  });

  it('publishes only with metamodel:publish', async () => {
    stubBackend({ permissions: ['citype:manage'] });
    renderAt('/ci-types/templates/tp-own');
    await screen.findByRole('heading', { name: 'Items der Version 2' });
    expect(screen.getByRole('button', { name: 'Version 2 publizieren' })).toBeDisabled();
    expect(
      screen.getByText('Publizieren erfordert das Recht „metamodel:publish“.'),
    ).toBeInTheDocument();
  });

  it('publishes after the confirmation and shows a composition conflict at the item', async () => {
    let refuse = true;
    const calls = stubBackend({
      permissions: ['citype:manage', 'metamodel:publish'],
      publish: () =>
        refuse
          ? problem(422, {
              field: 'hostname',
              detail: 'hostname: data type conflicts with the type chain',
              code: 'datatype-conflict',
            })
          : json({ ...V2, status: 'published' }),
    });
    renderAt('/ci-types/templates/tp-own');
    await screen.findByRole('heading', { name: 'Items der Version 2' });

    fireEvent.click(screen.getByRole('button', { name: 'Version 2 publizieren' }));
    const dialog = screen.getByRole('dialog', { name: 'Version 2 publizieren' });
    expect(calls.some((c) => c.path.endsWith('/publish'))).toBe(false);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Publizieren' }));

    const alert = await within(itemOf(2, 'hostname')).findByRole('alert');
    expect(alert).toHaveTextContent('data type conflicts with the type chain');
    expect(calls.find((c) => c.method === 'POST' && c.path.endsWith('/publish'))!.path).toBe(
      '/templates/tp-own/versions/2/publish',
    );

    refuse = false;
    fireEvent.click(screen.getByRole('button', { name: 'Version 2 publizieren' }));
    fireEvent.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Publizieren' }),
    );
    await waitFor(() => expect(calls.filter((c) => c.path.endsWith('/publish'))).toHaveLength(2));
  });
});

describe('MetamodelTabs', () => {
  it('shows "Types" and "Templates" as links with aria-current', async () => {
    stubBackend();
    renderAt('/ci-types');
    const nav = await screen.findByRole('navigation', { name: 'Metamodell' });
    expect(within(nav).getByRole('link', { name: 'Typen' })).toHaveAttribute(
      'aria-current',
      'page',
    );
    const templates = within(nav).getByRole('link', { name: 'Templates' });
    expect(templates).not.toHaveAttribute('aria-current');
    expect(templates).toHaveAttribute('href', '/ci-types/templates');

    fireEvent.click(templates);
    const next = await screen.findByRole('navigation', { name: 'Metamodell' });
    await waitFor(() =>
      expect(within(next).getByRole('link', { name: 'Templates' })).toHaveAttribute(
        'aria-current',
        'page',
      ),
    );
    expect(within(next).getByRole('link', { name: 'Typen' })).not.toHaveAttribute('aria-current');
  });

  it('hides "Templates" with the module metamodel off', async () => {
    const calls = stubBackend({
      modules: [{ key: 'metamodel', phase: 'P2', nav: [], enabled: false }],
    });
    renderAt('/ci-types');
    const nav = await screen.findByRole('navigation', { name: 'Metamodell' });
    await waitFor(() => expect(calls.some((c) => c.path === '/modules')).toBe(true));
    await waitFor(() =>
      expect(within(nav).queryByRole('link', { name: 'Templates' })).not.toBeInTheDocument(),
    );
    expect(within(nav).getByRole('link', { name: 'Typen' })).toBeInTheDocument();
  });

  it('keeps the dialog "From type template" on GET /ci-types/templates', async () => {
    const calls = stubBackend();
    renderAt('/ci-types');
    fireEvent.click(await screen.findByRole('button', { name: 'Aus Typvorlage' }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(calls.some((c) => c.method === 'GET' && c.path === '/ci-types/templates')).toBe(true);
    expect(calls.some((c) => c.path === '/templates')).toBe(false);
  });
});

describe('diffEntries', () => {
  it('lists added, changed and removed entries with old and new values', () => {
    expect(
      diffEntries(
        [
          { key: 'a', values: { x: 1 } },
          { key: 'b', values: { x: 2, y: 'k' } },
        ],
        [
          { key: 'b', values: { x: 3, y: 'k' } },
          { key: 'c', label: 'C', values: { x: 4, y: undefined } },
        ],
      ),
    ).toEqual([
      { key: 'b', label: 'b', change: 'changed', fields: [{ field: 'x', before: 2, after: 3 }] },
      {
        key: 'c',
        label: 'C',
        change: 'added',
        fields: [{ field: 'x', before: undefined, after: 4 }],
      },
      {
        key: 'a',
        label: 'a',
        change: 'removed',
        fields: [{ field: 'x', before: 1, after: undefined }],
      },
    ]);
  });
});
