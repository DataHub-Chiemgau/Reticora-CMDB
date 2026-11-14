/**
 * WP-1115 (REC-04): the page /source-ranks. The matrix attribute x source
 * shows the effective rank with its origin (System, Org, Typ); manual is
 * fixed at 100; the type selector loads the ranks of a CI type; holders of
 * reconciliation:manage edit a cell (1..99, checked before sending), reset
 * an entry to the inherited rank and save the complete list of the
 * organization (PUT replaces every entry); a 422 appears at its cell;
 * without the right the page only reads.
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/SourceRanksPage.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import deDE from '../i18n/de-DE.json';
import enUS from '../i18n/en-US.json';
import { renderWithProviders } from '../test/utils';
import { RANK_SOURCES } from '../api/client';
import { DEFAULT_RANKS, SourceRanksPage } from './SourceRanksPage';

type Json = Record<string, unknown>;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const AT = '2026-01-01T00:00:00Z';
const SERVER = 'c0000000-0000-4000-8000-000000000001';
const SWITCH = 'c0000000-0000-4000-8000-000000000002';

function ciType(id: string, key: string, de: string) {
  return {
    id,
    key,
    name: key,
    display_name: { de, en: key },
    is_builtin: true,
    is_system: true,
    is_active: true,
    is_logical: false,
    is_observable: true,
    schema_version: 1,
    version: 1,
    capabilities: {},
    allowed_relationship_types: [],
    ui_schema: {},
    created_at: AT,
    updated_at: AT,
  };
}

// Stored: snmp 70 org-wide for serial_number, agent 88 for servers, and an
// entry of the switch type that another view never shows.
const ENTRIES = [
  { attribute_key: 'serial_number', source: 'snmp', rank: 70 },
  { ci_type_id: SERVER, attribute_key: 'serial_number', source: 'agent', rank: 88 },
  { ci_type_id: SWITCH, attribute_key: 'firmware_version', source: 'ssh', rank: 65 },
];

/** effectiveFor resolves the ranks like the server (type, org, default). */
function effectiveFor(typeId: string, attributes: string[]) {
  return attributes.map((attribute) => ({
    ...(typeId ? { ci_type_id: typeId } : {}),
    attribute_key: attribute,
    ranks: RANK_SOURCES.map((source) => {
      const typed = ENTRIES.find(
        (e) =>
          e.ci_type_id === typeId && typeId && e.attribute_key === attribute && e.source === source,
      );
      if (typed) return { source, rank: typed.rank, origin: 'type' };
      const org = ENTRIES.find(
        (e) => !e.ci_type_id && e.attribute_key === attribute && e.source === source,
      );
      if (org) return { source, rank: org.rank, origin: 'org' };
      return { source, rank: DEFAULT_RANKS[source], origin: 'default' };
    }),
  }));
}

function mockApi(overrides: { permissions?: string[]; put?: (body: Json) => Response } = {}) {
  return vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost');
    const method = init?.method ?? 'GET';
    if (url.pathname.endsWith('/me/permissions')) {
      return json({
        user_id: 'user-1',
        permissions: overrides.permissions ?? ['discovery:read', 'reconciliation:manage'],
      });
    }
    if (url.pathname.endsWith('/ci-types')) {
      return json({
        data: [ciType(SERVER, 'server', 'Server'), ciType(SWITCH, 'switch', 'Switch')],
        total: 2,
        limit: 200,
        offset: 0,
        has_more: false,
      });
    }
    if (url.pathname.endsWith('/reconciliation/source-ranks') && method === 'GET') {
      const typeId = url.searchParams.get('ci_type_id') ?? '';
      const keys = (url.searchParams.get('attribute_key') ?? '').split(',').filter(Boolean);
      return json({ entries: ENTRIES, effective: effectiveFor(typeId, keys) });
    }
    if (url.pathname.endsWith('/reconciliation/source-ranks') && method === 'PUT') {
      const body = JSON.parse(String(init?.body)) as Json;
      return overrides.put ? overrides.put(body) : json({ entries: body.entries, effective: [] });
    }
    return json({ title: 'Not Found', status: 404 }, 404);
  });
}

const callsOf = (fetchMock: ReturnType<typeof mockApi>, method: string) =>
  fetchMock.mock.calls.filter(
    (call) =>
      (call[1]?.method ?? 'GET') === method &&
      new URL(String(call[0]), 'http://localhost').pathname.endsWith(
        '/reconciliation/source-ranks',
      ),
  );

/** cell is the table cell of an attribute and a source. */
async function cell(attribute: string, source: string) {
  await screen.findByRole('table');
  return await waitFor(() => {
    const found = document.querySelector(`[data-cell="${attribute}/${source}"]`);
    if (!found) throw new Error(`no cell ${attribute}/${source}`);
    return found as HTMLElement;
  });
}

/** edit opens a cell with Enter and types a rank, then presses key. */
async function edit(attribute: string, source: string, value: string, key = 'Enter') {
  const td = await cell(attribute, source);
  fireEvent.click(within(td).getByRole('button', { name: /^Rang von/ }));
  const input = await within(td).findByRole('textbox');
  fireEvent.change(input, { target: { value } });
  fireEvent.keyDown(input, { key });
  return td;
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('SourceRanksPage', () => {
  it('renders effective rank and origin per attribute and source', async () => {
    vi.stubGlobal('fetch', mockApi());
    renderWithProviders(<SourceRanksPage />);
    // Org-wide view: serial_number is a row, firmware_version (switch only) is not.
    await waitFor(async () =>
      expect((await cell('serial_number', 'snmp')).textContent).toContain('70'),
    );
    const snmp = await cell('serial_number', 'snmp');
    expect(within(snmp).getByText('Org')).toBeInTheDocument();
    const agent = await cell('serial_number', 'agent');
    expect(agent.textContent).toContain('85');
    expect(within(agent).getByText('System')).toBeInTheDocument();
    expect(document.querySelector('[data-cell="firmware_version/ssh"]')).toBeNull();
    // Every automation source is a column, translated.
    const table = screen.getByRole('table');
    for (const name of ['Import', 'Workflow', 'SNMP', 'Netzscan']) {
      expect(
        within(table).getByRole('columnheader', { name: new RegExp(name) }),
      ).toBeInTheDocument();
    }
  });

  it('manual column is fixed at 100 and not editable', async () => {
    vi.stubGlobal('fetch', mockApi());
    renderWithProviders(<SourceRanksPage />);
    const manual = await cell('serial_number', 'manual');
    expect(manual.textContent).toBe('100');
    expect(within(manual).queryByRole('button')).toBeNull();
    expect(screen.getByText('manuelle Werte haben immer Vorrang')).toBeInTheDocument();
  });

  it('type selector loads the ranks of a ci type', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SourceRanksPage />);
    await cell('serial_number', 'agent');
    const select = await screen.findByLabelText('CI-Typ');
    await waitFor(() =>
      expect(within(select).getByRole('option', { name: 'Server' })).toBeInTheDocument(),
    );
    fireEvent.change(select, { target: { value: SERVER } });
    await waitFor(async () =>
      expect((await cell('serial_number', 'agent')).textContent).toContain('88'),
    );
    expect(within(await cell('serial_number', 'agent')).getByText('Typ')).toBeInTheDocument();
    expect(within(await cell('serial_number', 'snmp')).getByText('Org')).toBeInTheDocument();
    const urls = callsOf(fetchMock, 'GET').map((call) => String(call[0]));
    expect(
      urls.some(
        (u) => u.includes(`ci_type_id=${SERVER}`) && u.includes('attribute_key=serial_number'),
      ),
    ).toBe(true);
  });

  it('rejects a rank outside 1..99 before saving', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SourceRanksPage />);
    const td = await edit('serial_number', 'agent', '120');
    expect(
      await within(td).findByText('Der Rang muss eine ganze Zahl von 1 bis 99 sein.'),
    ).toBeInTheDocument();
    const input = within(td).getByRole('textbox');
    fireEvent.change(input, { target: { value: '0' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(within(td).getByRole('textbox')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Speichern' })).toBeDisabled();
    // Escape discards the edit.
    fireEvent.keyDown(within(td).getByRole('textbox'), { key: 'Escape' });
    expect(within(td).queryByRole('textbox')).toBeNull();
    expect(td.textContent).toContain('85');
    expect(callsOf(fetchMock, 'PUT')).toHaveLength(0);
  });

  it('sends the full entry list including other ci types', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SourceRanksPage />);
    const td = await edit('serial_number', 'agent', '87');
    expect(td.textContent).toContain('87');
    expect(within(td).getByText('Org')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(callsOf(fetchMock, 'PUT')).toHaveLength(1));
    const body = JSON.parse(String(callsOf(fetchMock, 'PUT')[0]?.[1]?.body)) as { entries: Json[] };
    expect(body.entries).toEqual([
      { attribute_key: 'serial_number', source: 'snmp', rank: 70 },
      { ci_type_id: SERVER, attribute_key: 'serial_number', source: 'agent', rank: 88 },
      { ci_type_id: SWITCH, attribute_key: 'firmware_version', source: 'ssh', rank: 65 },
      { attribute_key: 'serial_number', source: 'agent', rank: 87 },
    ]);
    expect(await screen.findByText('Quellränge gespeichert.')).toBeInTheDocument();
  });

  it('reset falls back to the inherited rank', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SourceRanksPage />);
    const td = await cell('serial_number', 'snmp');
    await waitFor(() => expect(td.textContent).toContain('70'));
    fireEvent.click(within(td).getByRole('button', { name: /zurücksetzen/ }));
    await waitFor(() => expect(td.textContent).toContain('80'));
    expect(within(td).getByText('System')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(callsOf(fetchMock, 'PUT')).toHaveLength(1));
    const body = JSON.parse(String(callsOf(fetchMock, 'PUT')[0]?.[1]?.body)) as { entries: Json[] };
    expect(body.entries).toHaveLength(2);
    expect(body.entries.some((e) => e.source === 'snmp')).toBe(false);
  });

  it('shows a 422 field error at the cell', async () => {
    const fetchMock = mockApi({
      put: () =>
        json(
          {
            type: 'about:blank',
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'must be between 1 and 99 (manual ranks 100)',
            field: 'entries[3].rank',
          },
          422,
        ),
    });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SourceRanksPage />);
    await edit('serial_number', 'wmi', '42');
    await edit('serial_number', 'agent', '87');
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    const wmi = await cell('serial_number', 'wmi');
    expect(
      await within(wmi).findByText('must be between 1 and 99 (manual ranks 100)'),
    ).toBeInTheDocument();
    // The other changes stay.
    expect((await cell('serial_number', 'agent')).textContent).toContain('87');
    expect(wmi.textContent).toContain('42');
    expect(screen.getByRole('button', { name: 'Speichern' })).toBeEnabled();
  });

  it('is read-only without reconciliation:manage', async () => {
    vi.stubGlobal('fetch', mockApi({ permissions: ['discovery:read'] }));
    renderWithProviders(<SourceRanksPage />);
    const td = await cell('serial_number', 'snmp');
    await waitFor(() => expect(td.textContent).toContain('70'));
    expect(within(td).queryByRole('button')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Speichern' })).toBeNull();
    expect(screen.queryByLabelText('Attribut')).toBeNull();
    expect(screen.getByText(/Nur lesend/)).toBeInTheDocument();
  });

  it('has its texts in de and en', () => {
    const de = deDE as unknown as {
      sourceRanks: Json;
      nav: Json;
      commandPalette: { commands: Json };
    };
    const en = enUS as unknown as typeof de;
    expect(Object.keys(de.sourceRanks).sort()).toEqual(Object.keys(en.sourceRanks).sort());
    for (const catalog of [de, en]) {
      expect(typeof catalog.nav['source-ranks']).toBe('string');
      expect(typeof catalog.commandPalette.commands['source-ranks']).toBe('string');
      const sources = catalog.sourceRanks.sources as Json;
      const origins = catalog.sourceRanks.origins as Json;
      for (const source of ['manual', ...RANK_SOURCES])
        expect(typeof sources[source]).toBe('string');
      for (const origin of ['default', 'org', 'type'])
        expect(typeof origins[origin]).toBe('string');
    }
    expect((en.sourceRanks.origins as Json).type).toBe('Type');
  });
});
