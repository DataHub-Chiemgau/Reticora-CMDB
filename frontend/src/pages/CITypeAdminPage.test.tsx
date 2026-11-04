/**
 * WP-313 (UI-11 [P2]): the metamodel administration I on /ci-types.
 *
 *   Tree     the types as an ARIA tree from parent_type_id (WP-248): levels,
 *            keyboard (arrows, Home/End, open/close, Enter selects), search
 *            with the ancestors of a match, all pages of GET /ci-types
 *            (limit=200, offset) beyond 50 types
 *   Type     one dialog for POST /ci-types and PATCH /ci-types/{id} with the
 *            parent; 409/422 and the deactivated parent at the parent field;
 *            409 at deactivate, activate and clone (WP-249); no deactivation
 *            of a system type
 *   Catalog  /attribute-definitions instead of PUT /ci-types/{id}/fields: one
 *            POST with the index flags, PATCH, origin own/inherited/global,
 *            only inheritable definitions of the ancestors, read-only
 *            inherited rows, inheritable editable, computed without required
 *            and unique, sensitive (WP-726) and no mark on 403, applies_to of
 *            global attributes (WP-255)
 *   Index    index_status (WP-796): every state, retry n/3, rebuild, polling
 *            until succeeded
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/CITypeAdminPage.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { CITypeAdminPage } from './CITypeAdminPage';
import { renderWithProviders } from '../test/utils';

interface Call {
  method: string;
  url: string;
  body: unknown;
}

type Json = Record<string, unknown>;

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

function type(id: string, name: string, extra: Json = {}): Json {
  return {
    id,
    key: name.toLowerCase().replace(/[^a-z0-9]+/g, '_'),
    name,
    display_name: { de: name, en: name },
    is_active: true,
    is_system: false,
    is_logical: false,
    is_observable: false,
    schema_version: 1,
    version: 1,
    ...extra,
  };
}

function definition(id: string, key: string, extra: Json = {}): Json {
  return {
    id,
    key,
    scope: 'type',
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
    index_status: { state: 'none', index_name: '', valid: false, attempts: 0, max_attempts: 3 },
    ...extra,
  };
}

/** Netzwerk (system) → Router → Edge-Router; Drucker is a second root. */
const NET = type('t-net', 'Netzwerk', { is_system: true });
const ROUTER = type('t-router', 'Router', { parent_type_id: 't-net' });
const EDGE = type('t-edge', 'Edge-Router', { parent_type_id: 't-router' });
const PRINTER = type('t-printer', 'Drucker');

interface Backend {
  types?: Json[];
  definitions?: Json[];
  /** Rows per page of GET /ci-types (the client asks for 200). */
  pageSize?: number;
  permissions?: string[];
  sensitive?: () => Response;
  post?: (body: Json) => Response;
  patch?: (body: Json) => Response;
  createType?: (body: Json) => Response;
  updateType?: (body: Json) => Response;
  clone?: () => Response;
  activate?: () => Response;
  deactivate?: () => Response;
  reindex?: () => Response;
  /** Changes the definitions before every read of the list (e.g. a job that ends). */
  onRead?: (definitions: Json[], query: URLSearchParams) => Json[];
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  let types = b.types ?? [NET, ROUTER, EDGE, PRINTER];
  let definitions = b.definitions ?? [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = (init?.body ? JSON.parse(String(init.body)) : undefined) as Json;
      calls.push({ method, url, body });
      const u = new URL(url, 'http://localhost');
      const q = u.searchParams;
      const path = u.pathname.replace(/^\/api\/v1/, '');
      if (path === '/me/permissions') {
        return json({
          user_id: 'u1',
          permissions: b.permissions ?? ['citype:manage', 'ci_attribute:manage'],
        });
      }
      if (path === '/field-permissions/sensitive-fields') {
        return b.sensitive ? b.sensitive() : json({ data: [], limit: 200, has_more: false });
      }
      if (path === '/attribute-definitions' && method === 'POST') {
        if (b.post) return b.post(body);
        const saved = definition('ad-new', String(body.key), body);
        definitions = [...definitions, saved];
        return json(saved, 201);
      }
      if (/^\/attribute-definitions\/[^/]+\/reindex$/.test(path)) {
        return b.reindex
          ? b.reindex()
          : json(
              { state: 'queued', index_name: 'idx', valid: false, attempts: 0, max_attempts: 3 },
              202,
            );
      }
      if (path.startsWith('/attribute-definitions/') && method === 'PATCH') {
        if (b.patch) return b.patch(body);
        const id = path.slice(path.lastIndexOf('/') + 1);
        definitions = definitions.map((d) => (d.id === id ? { ...d, ...body } : d));
        return json(definitions.find((d) => d.id === id));
      }
      if (path.startsWith('/attribute-definitions/') && method === 'DELETE') {
        const id = path.slice(path.lastIndexOf('/') + 1);
        definitions = definitions.filter((d) => d.id !== id);
        return new Response(null, { status: 204 });
      }
      if (path === '/attribute-definitions') {
        if (b.onRead) definitions = b.onRead(definitions, q);
        const applicable = q.get('applicable_to');
        const typeId = q.get('ci_type_id');
        const data = definitions.filter((d) =>
          applicable
            ? d.ci_type_id === applicable || d.scope === 'global'
            : typeId
              ? d.ci_type_id === typeId
              : q.get('scope') === 'global'
                ? d.scope === 'global'
                : true,
        );
        return json({ data });
      }
      if (path === '/ci-types/templates') return json({ data: [] });
      if (path === '/ci-types' && method === 'POST') {
        if (b.createType) return b.createType(body);
        const created = type('t-new', String(body.name), body);
        types = [...types, created];
        return json(created, 201);
      }
      if (path === '/ci-types') {
        const offset = Number(q.get('offset') ?? 0);
        const size = b.pageSize ?? 200;
        const data = types.slice(offset, offset + size);
        return json({
          data,
          total: types.length,
          limit: Number(q.get('limit') ?? 50),
          offset,
          has_more: offset + data.length < types.length,
        });
      }
      const action = /^\/ci-types\/([^/]+)\/(clone|activate|deactivate)$/.exec(path);
      if (action && method === 'POST') {
        const handler = { clone: b.clone, activate: b.activate, deactivate: b.deactivate }[
          action[2] as 'clone' | 'activate' | 'deactivate'
        ];
        if (handler) return handler();
        return json(types.find((ty) => ty.id === action[1]));
      }
      const one = /^\/ci-types\/([^/]+)$/.exec(path);
      if (one && method === 'PATCH') {
        if (b.updateType) return b.updateType(body);
        types = types.map((ty) => (ty.id === one[1] ? { ...ty, ...body } : ty));
        return json(types.find((ty) => ty.id === one[1]));
      }
      if (one) return json(types.find((ty) => ty.id === one[1]));
      return json({ detail: `unexpected ${method} ${url}` }, 404);
    }),
  );
  return calls;
}

const tree = () => screen.findByRole('tree');
const item = (name: string) => screen.getByRole('treeitem', { name });

async function open(name?: string) {
  renderWithProviders(<CITypeAdminPage />, { route: '/ci-types' });
  await tree();
  if (name) fireEvent.click(await screen.findByRole('treeitem', { name }));
}

/** The catalog row of an attribute key in the detail card of the type. */
async function row(key: string) {
  const card = (await screen.findByRole('heading', { name: /^Attribute/ })).closest('section')!;
  const text = await within(card).findByText(key, { exact: true });
  return text.closest('li')!;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe('CITypeAdminPage type tree', () => {
  it('builds the tree from parent_type_id', async () => {
    stubBackend();
    await open();

    expect(item('Netzwerk')).toHaveAttribute('aria-level', '1');
    expect(item('Netzwerk')).toHaveAttribute('aria-expanded', 'true');
    expect(item('Router')).toHaveAttribute('aria-level', '2');
    expect(item('Edge-Router')).toHaveAttribute('aria-level', '3');
    expect(item('Edge-Router')).not.toHaveAttribute('aria-expanded');
    expect(item('Drucker')).toHaveAttribute('aria-level', '1');
    const group = within(item('Netzwerk')).getAllByRole('group')[0]!;
    expect(within(group).getByRole('treeitem', { name: 'Router' })).toBeInTheDocument();
    // The badges stay: a system type is marked.
    expect(within(item('Netzwerk')).getAllByText('System')[0]).toBeInTheDocument();
  });

  it('moves with the arrow keys, Home and End, opens and closes and selects with Enter', async () => {
    stubBackend();
    await open();
    // Sorted by label: Drucker, Netzwerk (→ Router → Edge-Router).
    const first = item('Drucker');
    expect(first).toHaveAttribute('tabindex', '0');
    expect(item('Netzwerk')).toHaveAttribute('tabindex', '-1');
    first.focus();

    fireEvent.keyDown(first, { key: 'ArrowDown' });
    expect(item('Netzwerk')).toHaveFocus();
    fireEvent.keyDown(item('Netzwerk'), { key: 'ArrowLeft' });
    expect(item('Netzwerk')).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByRole('treeitem', { name: 'Router' })).not.toBeInTheDocument();
    fireEvent.keyDown(item('Netzwerk'), { key: 'ArrowRight' });
    expect(item('Netzwerk')).toHaveAttribute('aria-expanded', 'true');
    fireEvent.keyDown(item('Netzwerk'), { key: 'ArrowRight' });
    expect(item('Router')).toHaveFocus();
    fireEvent.keyDown(item('Router'), { key: 'End' });
    expect(item('Edge-Router')).toHaveFocus();
    fireEvent.keyDown(item('Edge-Router'), { key: 'ArrowLeft' });
    expect(item('Router')).toHaveFocus();
    fireEvent.keyDown(item('Router'), { key: 'Home' });
    expect(item('Drucker')).toHaveFocus();
    fireEvent.keyDown(item('Drucker'), { key: 'ArrowDown' });
    fireEvent.keyDown(item('Netzwerk'), { key: 'ArrowUp' });
    expect(item('Drucker')).toHaveFocus();

    fireEvent.keyDown(item('Drucker'), { key: 'Enter' });
    expect(item('Drucker')).toHaveAttribute('aria-selected', 'true');
    expect(await screen.findByRole('heading', { name: 'Drucker', level: 3 })).toBeInTheDocument();
  });

  it('keeps the ancestors of a search match visible', async () => {
    stubBackend();
    await open();
    fireEvent.change(screen.getByRole('textbox', { name: 'Suchen' }), {
      target: { value: 'edge' },
    });

    expect(item('Netzwerk')).toBeInTheDocument();
    expect(item('Router')).toBeInTheDocument();
    expect(item('Edge-Router')).toBeInTheDocument();
    expect(screen.queryByRole('treeitem', { name: 'Drucker' })).not.toBeInTheDocument();
  });

  it('loads more than 50 types over two pages', async () => {
    const many = Array.from({ length: 60 }, (_, i) =>
      type(`t-${i}`, `Typ ${String(i).padStart(2, '0')}`),
    );
    const calls = stubBackend({ types: many, pageSize: 30 });
    await open();

    expect(await screen.findByRole('treeitem', { name: 'Typ 59' })).toBeInTheDocument();
    expect(screen.getAllByRole('treeitem')).toHaveLength(60);
    const lists = calls.filter((c) => c.method === 'GET' && /\/ci-types\?/.test(c.url));
    expect(lists).toHaveLength(2);
    expect(lists[0]!.url).toContain('limit=200');
    expect(lists[1]!.url).toContain('offset=30');
  });
});

describe('CITypeAdminPage type dialog', () => {
  it('creates a type with name and a system type as parent', async () => {
    const calls = stubBackend();
    await open();
    fireEvent.click(screen.getByRole('button', { name: 'Typ anlegen' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Firewall' } });
    const parent = within(dialog).getByLabelText('Elterntyp');
    expect(within(parent).getByRole('option', { name: 'Netzwerk (System)' })).toBeInTheDocument();
    fireEvent.change(parent, { target: { value: 't-net' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const post = calls.find((c) => c.method === 'POST' && /\/ci-types$/.test(c.url));
    expect(post?.body).toEqual({ name: 'Firewall', parent_type_id: 't-net' });
  });

  it('edits a type with PATCH and keeps the key read-only', async () => {
    const calls = stubBackend();
    await open('Edge-Router');
    fireEvent.click(await screen.findByRole('button', { name: 'Typ bearbeiten' }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByLabelText('Schlüssel')).toBeDisabled();
    const parent = within(dialog).getByLabelText('Elterntyp');
    // The type itself and its subtypes are no parents.
    expect(within(parent).queryByRole('option', { name: 'Edge-Router' })).not.toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText('Beschreibung'), {
      target: { value: 'Am Rand' },
    });
    fireEvent.change(parent, { target: { value: '' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const patch = calls.find((c) => c.method === 'PATCH');
    expect(patch?.url).toMatch(/\/ci-types\/t-edge$/);
    expect(patch?.body).toEqual({ description: 'Am Rand', parent_type_id: '' });
  });

  it.each([
    [409, 'parent_type_id: the type chain would contain a cycle', 'Konflikt in der Typkette'],
    [422, 'parent_type_id: the type chain is deeper than 6', 'Elterntyp nicht zulässig'],
    [422, 'parent_type_id: the CI type is deactivated', 'Der gewählte Elterntyp ist deaktiviert.'],
  ])('shows the %i of the parent at the parent field', async (status, detail, text) => {
    stubBackend({
      updateType: () => problem(status, { detail, field: 'parent_type_id' }),
    });
    await open('Router');
    fireEvent.click(await screen.findByRole('button', { name: 'Typ bearbeiten' }));
    const dialog = screen.getByRole('dialog');
    const parent = within(dialog).getByLabelText('Elterntyp');
    fireEvent.change(parent, { target: { value: 't-printer' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(parent).toHaveAttribute('aria-invalid', 'true'));
    const described = document.getElementById(parent.getAttribute('aria-describedby')!);
    expect(described).toHaveTextContent(text);
    expect(within(dialog).getByRole('alert')).toHaveTextContent(
      'Bitte prüfen Sie die markierten Felder',
    );
  });

  it('shows the 409 of deactivate, activate and clone at the action', async () => {
    const inactive = type('t-old', 'Altgerät', { is_active: false, parent_type_id: 't-printer' });
    stubBackend({
      types: [NET, ROUTER, EDGE, PRINTER, inactive],
      deactivate: () =>
        problem(409, {
          detail: 'is_active: the ci type has active subtypes; deactivate them first',
          field: 'is_active',
        }),
      activate: () =>
        problem(409, {
          detail: 'parent_type_id: the parent ci type is deactivated',
          field: 'parent_type_id',
        }),
      clone: () =>
        problem(409, {
          detail: 'parent_type_id: the parent ci type is deactivated',
          field: 'parent_type_id',
        }),
    });
    await open('Router');
    fireEvent.click(await screen.findByRole('button', { name: 'Deaktivieren' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Deaktivieren nicht möglich: Der Typ hat aktive Untertypen.',
    );
    fireEvent.click(screen.getByRole('button', { name: 'Klonen' }));
    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(
        'Klonen nicht möglich: Der Elterntyp ist deaktiviert.',
      ),
    );

    fireEvent.click(screen.getByRole('checkbox', { name: 'Deaktivierte anzeigen' }));
    fireEvent.click(await screen.findByRole('treeitem', { name: 'Altgerät' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Aktivieren' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Aktivieren nicht möglich: Der Elterntyp ist deaktiviert.',
    );
  });

  it('offers no deactivation and no editing on a system type, but the clone', async () => {
    stubBackend();
    await open('Netzwerk');
    expect(await screen.findByRole('button', { name: 'Klonen' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Deaktivieren' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Typ bearbeiten' })).not.toBeInTheDocument();
  });
});

describe('CITypeAdminPage attribute catalog', () => {
  it('creates an attribute with its index flags in one POST, without PUT /fields', async () => {
    const calls = stubBackend();
    await open('Drucker');
    fireEvent.click(await screen.findByRole('button', { name: 'Feld hinzufügen' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Feldname'), { target: { value: 'serial' } });
    fireEvent.change(within(dialog).getByLabelText('Anzeigename (Deutsch)'), {
      target: { value: 'Seriennummer' },
    });
    fireEvent.change(within(dialog).getByLabelText('Anzeigename (Englisch)'), {
      target: { value: 'Serial number' },
    });
    fireEvent.click(within(dialog).getByLabelText('Eindeutig'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const posts = calls.filter(
      (c) => c.method === 'POST' && c.url.includes('/attribute-definitions'),
    );
    expect(posts).toHaveLength(1);
    expect(posts[0]!.body).toEqual({
      key: 'serial',
      data_type: 'text',
      group: 'general',
      display_name: { de: 'Seriennummer', en: 'Serial number' },
      ci_type_id: 't-printer',
      required: false,
      inheritable: true,
      searchable: false,
      unique: true,
      unique_scope: 'org',
    });
    expect(calls.some((c) => c.method === 'PUT')).toBe(false);
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
    expect(await row('serial')).toHaveTextContent('eindeutig');
  });

  it('edits an attribute with a PATCH of the changed properties', async () => {
    const calls = stubBackend({
      definitions: [definition('ad-1', 'toner', { ci_type_id: 't-printer' })],
    });
    await open('Drucker');
    fireEvent.click(within(await row('toner')).getByRole('button', { name: 'Bearbeiten: toner' }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByLabelText('Feldname')).not.toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText('Anzeigename (Deutsch)'), {
      target: { value: 'Tonerart' },
    });
    fireEvent.change(within(dialog).getByLabelText('Reihenfolge'), { target: { value: '5' } });
    fireEvent.click(within(dialog).getByLabelText('Pflichtfeld'));
    fireEvent.click(within(dialog).getByLabelText('Vererbbar an Untertypen'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const patches = calls.filter((c) => c.method === 'PATCH');
    expect(patches).toHaveLength(1);
    expect(patches[0]!.url).toMatch(/\/attribute-definitions\/ad-1$/);
    expect(patches[0]!.body).toEqual({
      display_name: { de: 'Tonerart', en: 'toner' },
      order: 5,
      required: true,
      inheritable: false,
    });
  });

  it('shows own, inherited (inheritable only) and global attributes with their origin', async () => {
    stubBackend({
      definitions: [
        definition('ad-own', 'edge_port', { ci_type_id: 't-edge' }),
        definition('ad-router', 'firmware', { ci_type_id: 't-router' }),
        definition('ad-private', 'router_secret', { ci_type_id: 't-router', inheritable: false }),
        definition('ad-net', 'mgmt_ip', { ci_type_id: 't-net', is_system: true }),
        definition('ad-glob', 'cost_center', { scope: 'global' }),
      ],
    });
    await open('Edge-Router');

    const own = await row('edge_port');
    expect(within(own).getByText('eigen')).toBeInTheDocument();
    expect(within(own).getByText('vererbbar')).toBeInTheDocument();
    expect(within(own).getByRole('button', { name: 'Bearbeiten: edge_port' })).toBeInTheDocument();

    const inherited = await row('firmware');
    expect(within(inherited).getByText('geerbt von Router')).toBeInTheDocument();
    expect(within(inherited).queryByRole('button', { name: /Bearbeiten|Löschen/ })).toBeNull();
    expect(await row('mgmt_ip')).toHaveTextContent('geerbt von Netzwerk');
    const card = screen.getByRole('heading', { name: /^Attribute/ }).closest('section')!;
    expect(within(card).queryByText('router_secret')).not.toBeInTheDocument();

    const global = await row('cost_center');
    expect(within(global).getByText('global')).toBeInTheDocument();
    expect(within(global).queryByRole('button', { name: /Bearbeiten/ })).toBeNull();

    // The inherited attribute points to its origin.
    fireEvent.click(within(inherited).getByRole('button', { name: 'Zum Ursprung: Router' }));
    expect(await screen.findByRole('heading', { name: 'Router', level: 3 })).toBeInTheDocument();
    expect(item('Router')).toHaveAttribute('aria-selected', 'true');
  });

  it('marks a computed attribute and offers neither required nor unique for it', async () => {
    const calls = stubBackend({
      definitions: [
        definition('ad-c', 'age_days', {
          ci_type_id: 't-printer',
          data_type: 'integer',
          computed: { op: 'today' },
        }),
      ],
    });
    await open('Drucker');
    const computed = await row('age_days');
    expect(within(computed).getByText('berechnet')).toBeInTheDocument();
    fireEvent.click(within(computed).getByRole('button', { name: 'Bearbeiten: age_days' }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByLabelText('Pflichtfeld')).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Eindeutig')).not.toBeInTheDocument();
    fireEvent.click(within(dialog).getByLabelText('Durchsuchbar (indiziert)'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(calls.find((c) => c.method === 'PATCH')!.body).toEqual({
      searchable: true,
      unique: false,
      unique_scope: 'org',
    });
  });

  it('marks sensitive attributes of the type chain', async () => {
    stubBackend({
      definitions: [
        definition('ad-1', 'pin', { ci_type_id: 't-printer' }),
        definition('ad-2', 'model', { ci_type_id: 't-printer' }),
      ],
      sensitive: () =>
        json({
          data: [
            {
              id: 'sf-1',
              organization_id: 'o',
              object_kind: 'ci',
              ci_type_id: 't-printer',
              field_key: 'attributes.pin',
              created_by: null,
              created_at: 'now',
            },
          ],
          limit: 200,
          has_more: false,
        }),
    });
    await open('Drucker');
    expect(await within(await row('pin')).findByText('sensibel')).toBeInTheDocument();
    expect(within(await row('model')).queryByText('sensibel')).not.toBeInTheDocument();
  });

  it('shows no sensitive mark and no error without field_permission:manage (403)', async () => {
    const calls = stubBackend({
      definitions: [definition('ad-1', 'pin', { ci_type_id: 't-printer' })],
      sensitive: () =>
        problem(403, { detail: 'missing required permission: field_permission:manage' }),
    });
    await open('Drucker');
    await row('pin');
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes('/field-permissions/sensitive-fields'))).toBe(true),
    );
    expect(screen.queryByText('sensibel')).not.toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('creates a global attribute for chosen types; the 422 shows at the field', async () => {
    const calls = stubBackend({
      post: () =>
        problem(422, {
          detail: 'applies_to_type_ids: unknown ci type t-gone',
          field: 'applies_to_type_ids',
        }),
    });
    await open();
    fireEvent.click(await screen.findByRole('button', { name: 'Neues globales Attribut' }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByLabelText('Pflichtfeld')).not.toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText('Feldname'), { target: { value: 'owner' } });
    fireEvent.change(within(dialog).getByLabelText('Gilt für'), { target: { value: 'types' } });
    fireEvent.click(within(dialog).getByRole('checkbox', { name: 'Router' }));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    const field = await within(dialog).findByText('unknown ci type t-gone');
    expect(field.closest('fieldset')).toHaveAccessibleDescription('unknown ci type t-gone');
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.body).toMatchObject({
      key: 'owner',
      required: false,
      applies_to: 'types',
      applies_to_type_ids: ['t-router'],
    });
    expect(post?.body).not.toHaveProperty('ci_type_id');
  });

  it('shows the applicability of global attributes and changes it with PATCH', async () => {
    const calls = stubBackend({
      definitions: [
        definition('g-1', 'cost_center', {
          scope: 'global',
          applies_to: 'categories',
          applies_to_categories: ['network'],
        }),
        definition('g-sys', 'asset_tag', { scope: 'global', is_system: true }),
      ],
    });
    await open();
    const section = (await screen.findByRole('heading', { name: 'Globale Attribute' })).closest(
      'section',
    )!;
    const own = (await within(section).findByText('cost_center')).closest('li')!;
    expect(own).toHaveTextContent('Kategorien: network');
    const system = within(section).getByText('asset_tag').closest('li')!;
    expect(within(system).queryByRole('button')).toBeNull();

    fireEvent.click(
      await within(own).findByRole('button', { name: 'Globales Attribut bearbeiten: cost_center' }),
    );
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Gilt für'), { target: { value: 'all' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(calls.find((c) => c.method === 'PATCH')!.body).toEqual({ applies_to: 'all' });
  });
});

describe('CITypeAdminPage index status', () => {
  const status = (state: string, extra: Json = {}) => ({
    index_status: {
      state,
      index_name: 'idx_attr_x',
      valid: state === 'succeeded',
      attempts: 0,
      max_attempts: 3,
      ...extra,
    },
  });

  it('shows every state, the retry n/3 with its error and rebuilds a failed index', async () => {
    const calls = stubBackend({
      definitions: [
        definition('a-none', 'plain', { ci_type_id: 't-printer', ...status('none') }),
        definition('a-q', 'waiting', {
          ci_type_id: 't-printer',
          searchable: true,
          ...status('queued'),
        }),
        definition('a-r', 'retrying', {
          ci_type_id: 't-printer',
          searchable: true,
          ...status('queued', { attempts: 2, error: 'lock timeout' }),
        }),
        definition('a-run', 'building', {
          ci_type_id: 't-printer',
          searchable: true,
          ...status('running'),
        }),
        definition('a-ok', 'done', {
          ci_type_id: 't-printer',
          searchable: true,
          ...status('succeeded'),
        }),
        definition('a-f', 'dupes', {
          ci_type_id: 't-printer',
          unique: true,
          ...status('failed', { error: 'could not create unique index: duplicate values' }),
        }),
        definition('a-c', 'stopped', {
          ci_type_id: 't-printer',
          searchable: true,
          ...status('cancelled'),
        }),
        definition('a-sys', 'hostname', {
          ci_type_id: 't-printer',
          is_system: true,
          searchable: true,
          ...status('succeeded'),
        }),
      ],
    });
    await open('Drucker');
    const state = async (key: string) => within(await row(key)).getByRole('status');
    expect(await state('plain')).toHaveTextContent('Index: kein Index');
    expect(await state('waiting')).toHaveTextContent('Index: Wartend');
    expect(await state('retrying')).toHaveTextContent('Index: erneuter Versuch 2/3');
    expect(await row('retrying')).toHaveTextContent('Fehler: lock timeout');
    expect(await state('building')).toHaveTextContent('Index: Läuft');
    expect(await state('done')).toHaveTextContent('Index: Erfolgreich');
    expect(await state('hostname')).toHaveTextContent('Index: Erfolgreich');
    expect(await state('dupes')).toHaveTextContent('Index: Fehlgeschlagen');
    expect(await row('dupes')).toHaveTextContent('duplicate values');
    expect(await state('stopped')).toHaveTextContent('Index: Abgebrochen');

    expect(screen.getAllByRole('button', { name: /^Index neu aufbauen/ })).toHaveLength(2);
    fireEvent.click(screen.getByRole('button', { name: 'Index neu aufbauen: dupes' }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === 'POST' && c.url.endsWith('/attribute-definitions/a-f/reindex'),
        ),
      ).toBe(true),
    );
  });

  it('polls while an index is queued or running until it succeeded', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let reads = 0;
    const calls = stubBackend({
      definitions: [
        definition('a-1', 'serial', {
          ci_type_id: 't-printer',
          searchable: true,
          ...status('queued'),
        }),
      ],
      onRead: (defs, query) => {
        if (!query.get('applicable_to')) return defs;
        reads += 1;
        const next = reads >= 3 ? 'succeeded' : reads === 2 ? 'running' : 'queued';
        return defs.map((d) => ({ ...d, ...status(next) }));
      },
    });
    await open('Drucker');
    expect(within(await row('serial')).getByRole('status')).toHaveTextContent('Wartend');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_000);
    });
    await waitFor(() =>
      expect(
        within(screen.getByText('serial').closest('li')!).getByRole('status'),
      ).toHaveTextContent('Läuft'),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_000);
    });
    await waitFor(() =>
      expect(
        within(screen.getByText('serial').closest('li')!).getByRole('status'),
      ).toHaveTextContent('Erfolgreich'),
    );
    const before = calls.filter((c) => c.url.includes('applicable_to=t-printer')).length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6_000);
    });
    // Once the index succeeded the list stops polling.
    expect(calls.filter((c) => c.url.includes('applicable_to=t-printer')).length).toBe(before);
  });
});
