/**
 * UI-14 (WP-323): the relationship type administration. It lists system and
 * organization types; system types are read-only (no edit, no delete). An
 * organization type is created with all REL-01 fields and its attribute
 * schema (metadata_schema, WP-295), whose data types are the phase 1 types
 * of MET-11 only. Refusals show at the form: 409 for a taken key, a system
 * key or a type in use, 403 as an error state.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import deDE from '../i18n/de-DE.json';
import { RELATIONSHIP_ATTRIBUTE_TYPES, RelationshipTypesPage } from './RelationshipTypesPage';

const AT = '2026-01-01T00:00:00Z';

function relType(key: string, extra: Record<string, unknown> = {}) {
  return {
    id: `rt-${key}`,
    key,
    display_name: key,
    reverse_name: key,
    directed: true,
    category: 'logical',
    impact_direction: 'source_depends_on_target',
    redundant: false,
    allowed_source_types: [],
    allowed_target_types: [],
    cardinality: 'many_to_many',
    is_system: true,
    metadata_schema: [],
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const SYSTEM = relType('connected_to', {
  display_name: 'verbunden mit',
  reverse_name: 'verbunden mit',
  directed: false,
  category: 'network',
  impact_direction: 'undirected',
});
const OWN = relType('backs_up', {
  organization_id: 'org-1',
  is_system: false,
  display_name: 'sichert',
  reverse_name: 'wird gesichert von',
  category: 'hosting',
  impact_direction: 'target_depends_on_source',
  cardinality: 'one_to_many',
  allowed_source_types: ['server'],
  metadata_schema: [
    {
      key: 'schedule',
      display_name: { de: 'Zeitplan', en: 'Schedule' },
      data_type: 'text',
      required: true,
      validation: {
        op: 'all',
        rules: [{ op: 'required' }, { op: 'range', of: 'length', max: 20 }],
      },
    },
  ],
});

function ciType(id: string, key: string) {
  return {
    id,
    organization_id: 'org-1',
    key,
    name: key,
    display_name: { de: key, en: key },
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
  };
}

const page = <T,>(data: T[]) => ({
  data,
  total: data.length,
  limit: 100,
  offset: 0,
  has_more: false,
});

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, detail: string) {
  return json(
    { type: `https://reticora.io/problems/${status}`, title: 'Error', status, detail },
    status,
  );
}

interface Server {
  list?: () => Response;
  write?: (init: RequestInit) => Response;
}

function stubServer({ list, write }: Server = {}) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost');
    if (url.pathname.startsWith('/api/v1/relationship-types')) {
      if (init?.method && init.method !== 'GET') {
        return write ? write(init) : json(OWN, init.method === 'POST' ? 201 : 200);
      }
      return list ? list() : json(page([SYSTEM, OWN]));
    }
    if (url.pathname === '/api/v1/ci-types') {
      return json(page([ciType('t-server', 'server'), ciType('t-app', 'application')]));
    }
    return json(page([]));
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

const writes = (fetchMock: ReturnType<typeof vi.fn>, method: string) =>
  fetchMock.mock.calls.filter((call) => (call[1] as RequestInit | undefined)?.method === method);

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function openCreate() {
  fireEvent.click(await screen.findByRole('button', { name: 'Beziehungstyp anlegen' }));
  const dialog = await screen.findByRole('dialog');
  // The CI types of the allowed lists are read.
  await within(dialog).findAllByRole('checkbox', { name: /application/ });
  return dialog;
}

describe('RelationshipTypesPage', () => {
  it('lists system and organization types; a system type has neither edit nor delete', async () => {
    stubServer();
    renderWithProviders(<RelationshipTypesPage />);

    const systemRow = (await screen.findByText('connected_to')).closest('tr')!;
    const ownRow = screen.getByText('backs_up').closest('tr')!;
    expect(systemRow).toHaveTextContent('System');
    expect(within(systemRow).queryByRole('button')).toBeNull();
    expect(ownRow).toHaveTextContent('Eigener Typ');
    expect(ownRow).toHaveTextContent('sichert');
    expect(ownRow).toHaveTextContent('wird gesichert von');
    expect(within(ownRow).getByRole('button', { name: 'sichert bearbeiten' })).toBeInTheDocument();
    expect(within(ownRow).getByRole('button', { name: 'sichert löschen' })).toBeInTheDocument();
  });

  it('labels category and impact direction from the catalog', async () => {
    stubServer();
    renderWithProviders(<RelationshipTypesPage />);

    const systemRow = (await screen.findByText('connected_to')).closest('tr')!;
    const ownRow = screen.getByText('backs_up').closest('tr')!;
    const catalog = deDE.relationshipTypes;
    expect(systemRow).toHaveTextContent(catalog.categories.network);
    expect(systemRow).toHaveTextContent(catalog.impactDirections.undirected);
    expect(ownRow).toHaveTextContent(catalog.categories.hosting);
    expect(ownRow).toHaveTextContent(catalog.impactDirections.target_depends_on_source);
    expect(ownRow).toHaveTextContent(catalog.cardinalities.one_to_many);
    expect(systemRow).not.toHaveTextContent('undirected');
    expect(ownRow).not.toHaveTextContent('hosting');
  });

  it('creates an organization type with all REL-01 fields and its attribute schema', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<RelationshipTypesPage />);
    const dialog = await openCreate();
    const field = (label: string) => within(dialog).getByLabelText(label);

    fireEvent.change(field('Schlüssel'), { target: { value: 'replicates_to' } });
    fireEvent.change(field('Name (ausgehend)'), { target: { value: 'repliziert nach' } });
    fireEvent.change(field('Name der Gegenrichtung (eingehend)'), {
      target: { value: 'Replikat von' },
    });
    fireEvent.change(field('Beschreibung'), { target: { value: 'Datenbankreplikation' } });
    fireEvent.change(field('Kategorie'), { target: { value: 'hosting' } });
    fireEvent.change(field('Auswirkungsrichtung'), {
      target: { value: 'target_depends_on_source' },
    });
    fireEvent.change(field('Kardinalität'), { target: { value: 'one_to_one' } });
    fireEvent.click(field('Redundant (Auswirkung erst, wenn alle Kanten ausfallen)'));
    const source = within(dialog).getByRole('group', { name: 'Erlaubte Quelltypen' });
    fireEvent.click(within(source).getByRole('checkbox', { name: /server/ }));
    const target = within(dialog).getByRole('group', { name: 'Erlaubte Zieltypen' });
    fireEvent.click(within(target).getByRole('checkbox', { name: /application/ }));

    fireEvent.click(within(dialog).getByRole('button', { name: 'Attribut hinzufügen' }));
    const attribute = within(dialog).getByRole('group', { name: 'Attribut 1' });
    fireEvent.change(within(attribute).getByLabelText('Schlüssel'), {
      target: { value: 'lag_seconds' },
    });
    fireEvent.change(within(attribute).getByLabelText('Bezeichnung (Deutsch)'), {
      target: { value: 'Verzögerung (s)' },
    });
    fireEvent.change(within(attribute).getByLabelText('Bezeichnung (Englisch)'), {
      target: { value: 'Lag (s)' },
    });
    fireEvent.change(within(attribute).getByLabelText('Datentyp'), {
      target: { value: 'integer' },
    });
    fireEvent.click(within(attribute).getByLabelText('Pflichtfeld'));
    fireEvent.change(within(attribute).getByLabelText('Vorgabewert'), { target: { value: '5' } });
    fireEvent.change(within(attribute).getByLabelText('Minimum'), { target: { value: '0' } });
    fireEvent.change(within(attribute).getByLabelText('Maximum'), { target: { value: '3600' } });

    fireEvent.click(within(dialog).getByRole('button', { name: 'Attribut hinzufügen' }));
    const second = within(dialog).getByRole('group', { name: 'Attribut 2' });
    fireEvent.change(within(second).getByLabelText('Schlüssel'), { target: { value: 'mode' } });
    fireEvent.change(within(second).getByLabelText('Datentyp'), { target: { value: 'enum' } });
    fireEvent.change(within(second).getByLabelText('Erlaubte Werte (kommagetrennt)'), {
      target: { value: 'sync, async' },
    });

    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));
    await waitFor(() => expect(writes(fetchMock, 'POST')).toHaveLength(1));
    const [url, init] = writes(fetchMock, 'POST')[0]!;
    expect(url).toBe('/api/v1/relationship-types');
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({
      key: 'replicates_to',
      display_name: 'repliziert nach',
      reverse_name: 'Replikat von',
      directed: true,
      category: 'hosting',
      impact_direction: 'target_depends_on_source',
      redundant: true,
      allowed_source_types: ['server'],
      allowed_target_types: ['application'],
      cardinality: 'one_to_one',
      description: 'Datenbankreplikation',
      metadata_schema: [
        {
          key: 'lag_seconds',
          display_name: { de: 'Verzögerung (s)', en: 'Lag (s)' },
          data_type: 'integer',
          required: true,
          default: 5,
          validation: { min: 0, max: 3600 },
        },
        { key: 'mode', data_type: 'enum', required: false, enum_values: ['sync', 'async'] },
      ],
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('offers only the data types WP-295 accepts, without json, file and references', async () => {
    stubServer();
    renderWithProviders(<RelationshipTypesPage />);
    const dialog = await openCreate();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Attribut hinzufügen' }));
    const select = within(dialog).getByLabelText('Datentyp') as HTMLSelectElement;
    const offered = Array.from(select.options).map((option) => option.value);

    expect(offered).toEqual([...RELATIONSHIP_ATTRIBUTE_TYPES]);
    for (const refused of [
      'json',
      'file',
      'user',
      'team',
      'location',
      'ci_ref',
      'asset_ref',
      'contract_ref',
    ]) {
      expect(offered).not.toContain(refused);
    }
    // The labels come from the catalog of the data types.
    expect(Array.from(select.options).map((option) => option.textContent)).toContain(
      deDE.citypes.dataTypes.integer,
    );
  });

  it('edits an organization type with a PATCH, keeping its schema with the stored validation', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<RelationshipTypesPage />);
    fireEvent.click(await screen.findByRole('button', { name: 'sichert bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('Schlüssel: backs_up (unveränderlich)');
    const attribute = within(dialog).getByRole('group', { name: 'Attribut 1' });
    expect(within(attribute).getByLabelText('Höchstlänge')).toHaveValue(20);
    fireEvent.change(within(dialog).getByLabelText('Name (ausgehend)'), {
      target: { value: 'sichert täglich' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(writes(fetchMock, 'PATCH')).toHaveLength(1));
    const [url, init] = writes(fetchMock, 'PATCH')[0]!;
    expect(url).toBe('/api/v1/relationship-types/backs_up');
    const body = JSON.parse((init as RequestInit).body as string);
    expect(body).not.toHaveProperty('key');
    expect(body).toMatchObject({
      display_name: 'sichert täglich',
      reverse_name: 'wird gesichert von',
      allowed_source_types: ['server'],
      metadata_schema: [
        {
          key: 'schedule',
          display_name: { de: 'Zeitplan', en: 'Schedule' },
          data_type: 'text',
          required: true,
          validation: { max_length: 20 },
        },
      ],
    });
  });

  it('shows a 403 of the list as an error state', async () => {
    stubServer({ list: () => problem(403, 'missing required permission: relationship_type:read') });
    renderWithProviders(<RelationshipTypesPage />);

    expect(
      await screen.findByText('Keine Berechtigung, die Beziehungstypen zu lesen.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it.each([
    [
      'a taken key',
      'replicates_to',
      problem(409, 'a relationship type with this key already exists'),
      'Ein Beziehungstyp mit diesem Schlüssel existiert bereits.',
    ],
    [
      'the key of a system type',
      'connected_to',
      problem(409, 'a relationship type with this key already exists'),
      'Der Schlüssel gehört einem Systemtyp und kann nicht verwendet werden.',
    ],
    [
      'a missing relationship_type:manage',
      'replicates_to',
      problem(403, 'missing required permission: relationship_type:manage'),
      'Keine Berechtigung: Für Beziehungstypen ist das Recht relationship_type:manage nötig.',
    ],
    [
      'an invalid definition',
      'replicates_to',
      problem(400, 'metadata_schema[0].data_type: data_type must be one of [text …]'),
      'Ungültige Definition: metadata_schema[0].data_type: data_type must be one of [text …]',
    ],
  ])('names the refusal of %s at the form', async (_case, key, answer, message) => {
    stubServer({ write: () => answer.clone() });
    renderWithProviders(<RelationshipTypesPage />);
    const dialog = await openCreate();
    fireEvent.change(within(dialog).getByLabelText('Schlüssel'), { target: { value: key } });
    fireEvent.change(within(dialog).getByLabelText('Name (ausgehend)'), {
      target: { value: 'x' },
    });
    fireEvent.change(within(dialog).getByLabelText('Name der Gegenrichtung (eingehend)'), {
      target: { value: 'y' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(message);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('names a type in use when its deletion is refused with 409', async () => {
    const fetchMock = stubServer({
      write: () => problem(409, 'the relationship type is used by relationships or suppressions'),
    });
    renderWithProviders(<RelationshipTypesPage />);
    fireEvent.click(await screen.findByRole('button', { name: 'sichert löschen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Löschen' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Der Beziehungstyp wird von Beziehungen oder Unterdrückungen verwendet und kann nicht gelöscht werden.',
    );
    expect(writes(fetchMock, 'DELETE')[0]?.[0]).toBe('/api/v1/relationship-types/backs_up');
  });
});
