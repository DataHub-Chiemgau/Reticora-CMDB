/**
 * WP-797 (UI-11 [P2]): attribute sets in the metamodel administration.
 *
 *   Editor      the attributes of a set from GET /attribute-definitions
 *               ?attribute_set_id=, a new one by POST with attribute_set_id,
 *               the 422 of the composition (field key) at the key; a system
 *               set is read-only
 *   Order       SortableList (@dnd-kit, E-54): Space picks up, the arrow keys
 *               move, Space drops, Escape cancels; the buttons up and down;
 *               the changed `order` values are saved by PATCH; announcements
 *               in DE and EN
 *   Assignment  TypeSetAssignment: PUT /ci-types/{id}/attribute-sets with the
 *               position sequence, only active sets to choose, none for a
 *               system type, the 422 on attribute_set_id at the entry
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/metamodel/AttributeSetEditor.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../../i18n';
import type { AttributeSet, CIType } from '../../api/cmdb';
import { renderWithProviders } from '../../test/utils';
import { AttributeSetEditor, orderChanges } from './AttributeSetEditor';
import { TypeSetAssignment } from './TypeSetAssignment';

type Json = Record<string, unknown>;

interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
  body: Json | undefined;
}

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

function attributeSet(id: string, key: string, extra: Partial<AttributeSet> = {}): AttributeSet {
  return {
    id,
    key,
    display_name: { de: key, en: key },
    is_system: false,
    is_active: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...extra,
  };
}

function definition(id: string, key: string, order: number, extra: Json = {}): Json {
  return {
    id,
    key,
    scope: 'set',
    attribute_set_id: 's-net',
    display_name: { de: key, en: key },
    data_type: 'text',
    required: false,
    multi_value: false,
    searchable: false,
    unique: false,
    unique_scope: 'org',
    inheritable: true,
    order,
    is_system: false,
    applies_to: 'all',
    index_status: { state: 'none', index_name: '', valid: false, attempts: 0, max_attempts: 3 },
    ...extra,
  };
}

const NET = attributeSet('s-net', 'network');
const SYSTEM = attributeSet('s-sys', 'base', { is_system: true });
const OLD = attributeSet('s-old', 'legacy', { is_active: false });
const POWER = attributeSet('s-power', 'power');

const ROUTER = {
  id: 't-router',
  key: 'router',
  name: 'Router',
  display_name: { de: 'Router', en: 'Router' },
  is_system: false,
  is_active: true,
} as unknown as CIType;

interface Backend {
  definitions?: Json[];
  assigned?: { attribute_set_id: string; position: number }[];
  post?: (body: Json) => Response;
  put?: (body: Json) => Response;
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  let definitions = b.definitions ?? [
    definition('d-a', 'vlan', 10),
    definition('d-b', 'speed', 20),
    definition('d-c', 'duplex', 30),
  ];
  let assigned = b.assigned ?? [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({ method, path, query: url.searchParams, body });
      if (path === '/attribute-definitions' && method === 'GET') {
        const setId = url.searchParams.get('attribute_set_id');
        return json({ data: definitions.filter((d) => d.attribute_set_id === setId) });
      }
      if (path === '/attribute-definitions' && method === 'POST') {
        if (b.post) return b.post(body!);
        const saved = definition('d-new', String(body!.key), 40, body);
        definitions = [...definitions, saved];
        return json(saved, 201);
      }
      const one = /^\/attribute-definitions\/([^/]+)$/.exec(path);
      if (one && method === 'PATCH') {
        definitions = definitions.map((d) => (d.id === one[1] ? { ...d, ...body } : d));
        return json(definitions.find((d) => d.id === one[1]));
      }
      const sets = /^\/ci-types\/([^/]+)\/attribute-sets$/.exec(path);
      if (sets && method === 'PUT') {
        if (b.put) return b.put(body!);
        assigned = body!.attribute_sets as typeof assigned;
        return json({ ci_type_id: sets[1], attribute_sets: assigned });
      }
      if (sets) return json({ ci_type_id: sets[1], attribute_sets: assigned });
      return json({ detail: `unexpected ${method} ${url}` }, 404);
    }),
  );
  return calls;
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

const patches = (calls: Call[]) =>
  calls
    .filter((c) => c.method === 'PATCH')
    .map((c) => ({ id: c.path.split('/').pop(), ...c.body }));

const rowKeys = () =>
  within(screen.getByRole('list', { name: /Attribute des Sets/ }))
    .getAllByRole('listitem')
    .map((li) => li.querySelector('.font-medium')?.textContent);

beforeEach(() => {
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => undefined;
});

afterEach(async () => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  await act(async () => {
    await i18n.changeLanguage('de-DE');
  });
});

describe('AttributeSetEditor', () => {
  it('lists the set attributes by attribute_set_id and creates one with attribute_set_id', async () => {
    const calls = stubBackend();
    renderWithProviders(<AttributeSetEditor set={NET} />);

    await waitFor(() => expect(rowKeys()).toEqual(['vlan', 'speed', 'duplex']));
    const read = calls.find((c) => c.method === 'GET' && c.path === '/attribute-definitions')!;
    expect(read.query.get('attribute_set_id')).toBe('s-net');

    fireEvent.click(screen.getByRole('button', { name: 'Attribut zum Set hinzufügen' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Feldname'), { target: { value: 'mtu' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const post = calls.find((c) => c.method === 'POST')!;
    expect(post.body).toMatchObject({ key: 'mtu', attribute_set_id: 's-net' });
    expect(post.body).not.toHaveProperty('ci_type_id');
    await waitFor(() => expect(rowKeys()).toEqual(['vlan', 'speed', 'duplex', 'mtu']));
  });

  it('shows the 422 of the composition check at the key', async () => {
    stubBackend({
      post: () =>
        problem(422, {
          field: 'key',
          detail: 'key: data type conflicts with the attribute of the type chain',
        }),
    });
    renderWithProviders(<AttributeSetEditor set={NET} />);
    await waitFor(() => expect(rowKeys()).toHaveLength(3));

    fireEvent.click(screen.getByRole('button', { name: 'Attribut zum Set hinzufügen' }));
    const dialog = screen.getByRole('dialog');
    const keyInput = within(dialog).getByLabelText('Feldname');
    fireEvent.change(keyInput, { target: { value: 'hostname' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(keyInput).toHaveAttribute('aria-invalid', 'true'));
    expect(keyInput).toHaveAccessibleDescription(
      'data type conflicts with the attribute of the type chain',
    );
  });

  it('reorders with the keyboard: Space, arrow, Space saves the changed order values', async () => {
    layoutRows();
    const calls = stubBackend();
    renderWithProviders(<AttributeSetEditor set={NET} />);
    await waitFor(() => expect(rowKeys()).toHaveLength(3));

    const handle = screen.getByRole('button', { name: 'Verschieben: vlan' });
    handle.focus();
    await key(handle, 'Space');
    await key(handle, 'ArrowDown');
    await key(handle, 'Space');

    await waitFor(() =>
      expect(patches(calls)).toEqual([
        { id: 'd-b', order: 10 },
        { id: 'd-a', order: 20 },
      ]),
    );
    await waitFor(() => expect(rowKeys()).toEqual(['speed', 'vlan', 'duplex']));
  });

  it('cancels a keyboard move with Escape', async () => {
    layoutRows();
    const calls = stubBackend();
    renderWithProviders(<AttributeSetEditor set={NET} />);
    await waitFor(() => expect(rowKeys()).toHaveLength(3));

    const handle = screen.getByRole('button', { name: 'Verschieben: vlan' });
    handle.focus();
    await key(handle, 'Space');
    await key(handle, 'ArrowDown');
    await key(handle, 'Escape');

    expect(
      await screen.findByText('Verschieben abgebrochen. vlan bleibt auf Position 1 von 3.'),
    ).toBeInTheDocument();
    expect(patches(calls)).toEqual([]);
    expect(rowKeys()).toEqual(['vlan', 'speed', 'duplex']);
  });

  it('reorders with the buttons up and down', async () => {
    const calls = stubBackend();
    renderWithProviders(<AttributeSetEditor set={NET} />);
    await waitFor(() => expect(rowKeys()).toHaveLength(3));
    expect(screen.getByRole('button', { name: 'Nach oben: vlan' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Nach unten: duplex' })).toBeDisabled();

    fireEvent.click(screen.getByRole('button', { name: 'Nach oben: duplex' }));

    await waitFor(() =>
      expect(patches(calls)).toEqual([
        { id: 'd-c', order: 20 },
        { id: 'd-b', order: 30 },
      ]),
    );
    await waitFor(() => expect(rowKeys()).toEqual(['vlan', 'duplex', 'speed']));
    expect(screen.getByText('duplex auf Position 2 von 3 abgelegt.')).toBeInTheDocument();
  });

  it('announces the move to screen readers in German and English', async () => {
    layoutRows();
    stubBackend();
    renderWithProviders(<AttributeSetEditor set={NET} />);
    await waitFor(() => expect(rowKeys()).toHaveLength(3));
    expect(
      screen.getByText(
        'Zum Aufnehmen die Leertaste drücken. Mit den Pfeiltasten verschieben, mit der Leertaste ablegen, mit Escape abbrechen.',
      ),
    ).toBeInTheDocument();

    const handle = screen.getByRole('button', { name: 'Verschieben: vlan' });
    handle.focus();
    await key(handle, 'Space');
    expect(await screen.findByText('vlan aufgenommen, Position 1 von 3.')).toBeInTheDocument();
    await key(handle, 'Escape');

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    expect(
      screen.getByText(
        'Press Space to pick up. Move with the arrow keys, press Space to drop, Escape to cancel.',
      ),
    ).toBeInTheDocument();
    const english = screen.getByRole('button', { name: 'Move: vlan' });
    english.focus();
    await key(english, 'Space');
    expect(await screen.findByText('Picked up vlan, position 1 of 3.')).toBeInTheDocument();
    await key(english, 'ArrowDown');
    expect(await screen.findByText('vlan moved to position 2 of 3.')).toBeInTheDocument();
    await key(english, 'Escape');
  });

  it('keeps a system set read-only', async () => {
    stubBackend({
      definitions: [definition('d-s', 'serial', 10, { attribute_set_id: 's-sys' })],
    });
    renderWithProviders(<AttributeSetEditor set={SYSTEM} />);

    await waitFor(() => expect(rowKeys()).toEqual(['serial']));
    expect(
      screen.getByText('System-Set: Attribute und Reihenfolge sind schreibgeschützt.'),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Attribut zum Set hinzufügen' }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Verschieben/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Nach oben/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /bearbeiten: serial/ })).not.toBeInTheDocument();
  });

  it('keeps distinct order values and falls back to steps of ten', () => {
    const a = definition('a', 'a', 5) as never;
    const b = definition('b', 'b', 7) as never;
    expect(orderChanges([b, a])).toEqual([
      { id: 'b', order: 5 },
      { id: 'a', order: 7 },
    ]);
    const z1 = definition('z1', 'z1', 0) as never;
    const z2 = definition('z2', 'z2', 0) as never;
    expect(orderChanges([z2, z1])).toEqual([
      { id: 'z2', order: 10 },
      { id: 'z1', order: 20 },
    ]);
  });
});

describe('TypeSetAssignment', () => {
  it('saves the ordered list with PUT and the position sequence', async () => {
    const calls = stubBackend({ assigned: [{ attribute_set_id: 's-net', position: 1 }] });
    renderWithProviders(<TypeSetAssignment type={ROUTER} sets={[NET, SYSTEM, OLD, POWER]} />);

    const list = await screen.findByRole('list', { name: 'Attribut-Sets von Router' });
    await waitFor(() => expect(within(list).getAllByRole('listitem')).toHaveLength(1));
    const choose = screen.getByLabelText('Aktives Set');
    // Only active sets that are not assigned yet: no inactive set.
    expect(
      within(choose)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Set auswählen', 'base', 'power']);

    fireEvent.change(choose, { target: { value: 's-power' } });
    fireEvent.click(screen.getByRole('button', { name: 'Zuordnen' }));
    await waitFor(() => expect(within(list).getAllByRole('listitem')).toHaveLength(2));
    fireEvent.click(screen.getByRole('button', { name: 'Nach oben: power' }));

    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(2));
    const puts = calls.filter((c) => c.method === 'PUT');
    expect(puts[0]!.path).toBe('/ci-types/t-router/attribute-sets');
    expect(puts[0]!.body).toEqual({
      ci_type_id: 't-router',
      attribute_sets: [
        { attribute_set_id: 's-net', position: 1 },
        { attribute_set_id: 's-power', position: 2 },
      ],
    });
    expect(puts[1]!.body).toEqual({
      ci_type_id: 't-router',
      attribute_sets: [
        { attribute_set_id: 's-power', position: 1 },
        { attribute_set_id: 's-net', position: 2 },
      ],
    });
  });

  it('offers no assignment for a system type', async () => {
    const calls = stubBackend();
    renderWithProviders(
      <TypeSetAssignment type={{ ...ROUTER, is_system: true }} sets={[NET, POWER]} />,
    );

    expect(
      screen.getByText(
        'Systemtypen erhalten keine Attribut-Sets. Klonen Sie den Typ, um Sets zuzuordnen.',
      ),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText('Aktives Set')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Zuordnen' })).not.toBeInTheDocument();
    expect(calls.some((c) => c.path.endsWith('/attribute-sets'))).toBe(false);
  });

  it('shows the 422 on attribute_set_id at the entry', async () => {
    stubBackend({
      put: () =>
        problem(422, {
          field: 'attribute_set_id',
          detail: 'attribute_set_id: data type conflict (composition-type-conflict)',
        }),
    });
    renderWithProviders(<TypeSetAssignment type={ROUTER} sets={[NET, POWER]} />);
    await screen.findByText('Keine Sets zugeordnet.');

    fireEvent.change(screen.getByLabelText('Aktives Set'), { target: { value: 's-net' } });
    fireEvent.click(screen.getByRole('button', { name: 'Zuordnen' }));

    const list = screen.getByRole('list', { name: 'Attribut-Sets von Router' });
    const entry = await within(list).findByRole('alert');
    expect(entry).toHaveTextContent(
      'Nicht zugeordnet: data type conflict (composition-type-conflict)',
    );
    expect(entry.closest('li')).toHaveTextContent('network');
  });
});
