/**
 * WP-808 (UI-14, MGT-12): the contract pages. The list pages over cursor
 * with filters and sort; the expiry overview marks notice deadlines in 30,
 * 7 and 1 day and passed ones; a change sends If-Match, a 412 offers to
 * reload, a 409 on number and a 403 of the field permissions mark the
 * inputs; without contract:write there are no write actions; the detail
 * page reads the documents by entity_type=contract and links an upload;
 * the chosen client goes as filter[client_id], the suppliers stay unscoped.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import { renderWithProviders } from '../test/utils';
import { useScopeStore } from '../stores/scope';
import type { Contract, Document, Supplier } from '../api/client';
import {
  CONTRACT_KINDS,
  CONTRACT_STATUSES,
  ContractsPage,
  daysUntil,
  noticeDeadline,
  noticeStage,
} from './ContractsPage';

vi.mock('../auth/session', () => ({
  getSessionToken: () => 'token-1',
  clearSession: () => undefined,
  refreshSession: async () => true,
}));

/** MockXHR takes the content transfer of an upload; no test answers it. */
class MockXHR {
  static requests: MockXHR[] = [];
  upload = { onprogress: null };
  onload = null;
  onerror = null;
  onabort = null;
  open() {}
  setRequestHeader() {}
  send() {
    MockXHR.requests.push(this);
  }
  abort() {}
}

const TODAY = '2026-11-10';
const AT = '2026-01-01T00:00:00Z';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

function page<T>(data: T[], extra: Record<string, unknown> = {}) {
  return json({ data, total: data.length, limit: 50, offset: 0, has_more: false, ...extra });
}

function contract(id: string, number: string, extra: Partial<Contract> = {}): Contract {
  return {
    id,
    organization_id: 'org-1',
    client_id: null,
    supplier_id: 'sup-1',
    number,
    kind: 'maintenance',
    start_date: '2026-01-01',
    end_date: '2027-12-31',
    notice_period_days: 90,
    value: 1200,
    currency: 'EUR',
    status: 'active',
    notes: null,
    version: 3,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const supplier = (id: string, name: string, client_id: string | null = null): Supplier => ({
  id,
  organization_id: 'org-1',
  client_id,
  name,
  external_ref: null,
  contact_id: null,
  notes: null,
  version: 1,
  created_at: AT,
  updated_at: AT,
});

interface Call {
  method: string;
  path: string;
  search: URLSearchParams;
  headers: Headers;
  body: unknown;
}

type Route = (call: Call) => Response;

/**
 * stubApi answers fetch by "METHOD /path" (below /api/v1) and records the
 * calls; /me/permissions, /suppliers, /clients and the overview have defaults.
 */
function stubApi(
  routes: Record<string, Route>,
  { permissions = ['contract:read', 'contract:write', 'document:write'] } = {},
) {
  const calls: Call[] = [];
  const defaults: Record<string, Route> = {
    'GET /me/permissions': () => json({ user_id: 'user-1', permissions }),
    'GET /suppliers': () =>
      page([supplier('sup-1', 'Wartungs GmbH'), supplier('sup-2', 'Client AG', 'cl-1')]),
    'GET /clients': () => page([{ id: 'cl-1', name: 'Kunde Eins' }]),
  };
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const call: Call = {
        method,
        path: url.pathname.replace(/^\/api\/v1/, ''),
        search: url.searchParams,
        headers: new Headers(init?.headers),
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      };
      calls.push(call);
      const key = `${method} ${call.path}`;
      // The expiry overview reads the same list with end_date from today.
      if (key === 'GET /contracts' && call.search.has('filter[end_date][gte]')) {
        const overview = routes['GET /contracts?overview'];
        return overview ? overview(call) : page([]);
      }
      const route = routes[key] ?? defaults[key];
      return route ? route(call) : json({ status: 404, detail: `unexpected ${key}` }, 404);
    }),
  );
  return calls;
}

function renderPage(route = '/contracts') {
  return renderWithProviders(
    <Routes>
      <Route path="/contracts" element={<ContractsPage />} />
      <Route path="/contracts/:id" element={<ContractsPage />} />
    </Routes>,
    { route },
  );
}

const listCalls = (calls: Call[]) =>
  calls.filter(
    (c) => c.method === 'GET' && c.path === '/contracts' && !c.search.has('filter[end_date][gte]'),
  );

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(new Date(`${TODAY}T09:00:00`));
  MockXHR.requests = [];
  vi.stubGlobal('XMLHttpRequest', MockXHR);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useScopeStore.getState().reset();
});

describe('notice deadline', () => {
  it('is end_date minus the notice period, end_date without one', () => {
    expect(noticeDeadline({ end_date: '2026-12-31', notice_period_days: 30 })).toBe('2026-12-01');
    expect(noticeDeadline({ end_date: '2026-12-31', notice_period_days: null })).toBe('2026-12-31');
    expect(noticeDeadline({ end_date: null, notice_period_days: 30 })).toBeUndefined();
  });

  it('falls into the stages 30, 7 and 1 day and passed', () => {
    expect(noticeStage('2026-11-10', TODAY)).toBe('d1');
    expect(noticeStage('2026-11-11', TODAY)).toBe('d1');
    expect(noticeStage('2026-11-17', TODAY)).toBe('d7');
    expect(noticeStage('2026-12-10', TODAY)).toBe('d30');
    expect(noticeStage('2026-12-11', TODAY)).toBeUndefined();
    expect(noticeStage('2026-11-09', TODAY)).toBe('passed');
    expect(daysUntil('2026-11-17', TODAY)).toBe(7);
  });

  it('offers exactly the values of WP-269 and E-119', () => {
    expect([...CONTRACT_STATUSES]).toEqual(['draft', 'active', 'terminated', 'ended']);
    expect([...CONTRACT_KINDS]).toEqual([
      'maintenance',
      'support',
      'lease',
      'license',
      'purchase',
      'service',
      'other',
    ]);
  });
});

describe('ContractsPage list', () => {
  it('pages over the cursor with sort and filters and shows the columns', async () => {
    const calls = stubApi({
      'GET /contracts': (call) =>
        call.search.get('cursor') === 'c2'
          ? page([contract('k-2', 'V-2', { kind: 'license', supplier_id: null })])
          : page([contract('k-1', 'V-1')], { has_more: true, next_cursor: 'c2', total: 2 }),
    });
    renderPage();

    const row = (await screen.findByRole('link', { name: 'V-1' })).closest('tr')!;
    expect(within(row).getByText('Wartung')).toBeInTheDocument();
    expect(within(row).getByText('Wartungs GmbH')).toBeInTheDocument();
    expect(within(row).getByText('Aktiv')).toBeInTheDocument();
    expect(within(row).getByText(/1\.200,00\s€/)).toBeInTheDocument();
    expect(listCalls(calls)[0]?.search.get('sort')).toBe('number');
    expect(listCalls(calls)[0]?.search.get('limit')).toBe('50');

    fireEvent.click(screen.getByRole('button', { name: 'Weiter' }));
    expect(await screen.findByRole('link', { name: 'V-2' })).toBeInTheDocument();
    expect(listCalls(calls).slice(-1)[0]?.search.get('cursor')).toBe('c2');

    fireEvent.change(screen.getByRole('combobox', { name: 'Status' }), {
      target: { value: 'terminated' },
    });
    fireEvent.change(screen.getByRole('combobox', { name: 'Art' }), {
      target: { value: 'lease' },
    });
    fireEvent.change(screen.getByRole('combobox', { name: 'Lieferant' }), {
      target: { value: 'sup-1' },
    });
    fireEvent.change(screen.getByRole('combobox', { name: 'Sortierung' }), {
      target: { value: 'end_date' },
    });
    await waitFor(() => {
      const last = listCalls(calls).slice(-1)[0]!;
      expect(last.search.get('filter[status]')).toBe('terminated');
      expect(last.search.get('filter[kind]')).toBe('lease');
      expect(last.search.get('filter[supplier_id]')).toBe('sup-1');
      expect(last.search.get('sort')).toBe('end_date');
      // A new filter starts again on the first page.
      expect(last.search.has('cursor')).toBe(false);
    });
  });

  it('marks the notice deadlines of the overview by stage and passed ones apart', async () => {
    const calls = stubApi({
      'GET /contracts': () => page([]),
      'GET /contracts?overview': () =>
        page([
          // 2026-11-17 − 0: in 7 days.
          contract('k-7', 'V-SIEBEN', { end_date: '2026-11-17', notice_period_days: 0 }),
          // 2026-12-10 − 30 = today.
          contract('k-0', 'V-HEUTE', { end_date: '2026-12-10', notice_period_days: 30 }),
          // 2026-11-11 without a period: tomorrow.
          contract('k-1', 'V-MORGEN', { end_date: '2026-11-11', notice_period_days: null }),
          // 2026-12-30 − 20 = 2026-12-10: in 30 days.
          contract('k-30', 'V-DREISSIG', { end_date: '2026-12-30', notice_period_days: 20 }),
          // 2026-12-01 − 60 = 2026-10-02: passed.
          contract('k-p', 'V-VORBEI', { end_date: '2026-12-01', notice_period_days: 60 }),
          // Far ahead and a terminated one: not shown.
          contract('k-far', 'V-FERN', { end_date: '2027-06-30', notice_period_days: 30 }),
          contract('k-t', 'V-GEKUENDIGT', {
            end_date: '2026-11-12',
            notice_period_days: 0,
            status: 'terminated',
          }),
        ]),
    });
    renderPage();

    const overview = (await screen.findByRole('heading', { name: 'Ablaufübersicht' })).closest(
      'section',
    )!;
    const item = async (name: string) =>
      (await within(overview).findByRole('link', { name })).closest('li')!;
    expect(within(await item('V-HEUTE')).getByText('Heute')).toBeInTheDocument();
    expect(within(await item('V-MORGEN')).getByText('Morgen')).toBeInTheDocument();
    expect(within(await item('V-SIEBEN')).getByText('In 7 Tagen')).toBeInTheDocument();
    expect(within(await item('V-DREISSIG')).getByText('In 30 Tagen')).toBeInTheDocument();
    const passed = await item('V-VORBEI');
    expect(within(passed).getByRole('note')).toBeInTheDocument();
    expect(within(passed).getByText('Kündigungstermin verstrichen')).toBeInTheDocument();
    expect(within(overview).queryByRole('link', { name: 'V-FERN' })).toBeNull();
    expect(within(overview).queryByRole('link', { name: 'V-GEKUENDIGT' })).toBeNull();

    const request = calls.find((c) => c.search.has('filter[end_date][gte]'))!;
    expect(request.search.get('sort')).toBe('end_date');
    expect(request.search.get('filter[end_date][gte]')).toBe(TODAY);
  });

  it('shows no write actions without contract:write', async () => {
    stubApi(
      { 'GET /contracts': () => page([contract('k-1', 'V-1')]) },
      { permissions: ['contract:read'] },
    );
    renderPage();
    expect(await screen.findByRole('link', { name: 'V-1' })).toBeInTheDocument();
    expect(await screen.findByText(/contract:write fehlt/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Vertrag anlegen' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Vertrag V-1 bearbeiten' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Vertrag V-1 löschen' })).toBeNull();
  });

  it('sends If-Match, shows a 412 as conflict and reloads the contract', async () => {
    let fresh = false;
    const calls = stubApi({
      'GET /contracts': () => page([contract('k-1', 'V-1')]),
      'GET /contracts/k-1': () => {
        fresh = true;
        return json(contract('k-1', 'V-1', { version: 4, notes: 'neu' }));
      },
      'PATCH /contracts/k-1': (call) =>
        call.headers.get('If-Match') === '"3"'
          ? json({ status: 412, detail: 'the contract has changed' }, 412)
          : json(contract('k-1', 'V-1', { version: 5 })),
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: 'Vertrag V-1 bearbeiten' }));
    let dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Wert'), { target: { value: '1500' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    expect(await within(dialog).findByText(/inzwischen geändert/)).toBeInTheDocument();
    const patch = calls.find((c) => c.method === 'PATCH')!;
    expect(patch.headers.get('If-Match')).toBe('"3"');
    // Only the changed field is sent.
    expect(patch.body).toEqual({ value: 1500 });

    fireEvent.click(within(dialog).getByRole('button', { name: 'Neu laden' }));
    await waitFor(() => expect(fresh).toBe(true));
    // The dialog opens again with the version read anew.
    await waitFor(() =>
      expect(within(screen.getByRole('dialog')).getByLabelText('Notizen')).toHaveValue('neu'),
    );
    dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Wert'), { target: { value: '1600' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(
      calls
        .filter((c) => c.method === 'PATCH')
        .slice(-1)[0]
        ?.headers.get('If-Match'),
    ).toBe('"4"');
  });

  it('shows a 409 at number and a 403 of the field permissions at the input', async () => {
    const calls = stubApi({
      'GET /contracts': () => page([contract('k-1', 'V-1')]),
      'POST /contracts': () => json({ status: 409, detail: 'number: taken', field: 'number' }, 409),
      'PATCH /contracts/k-1': () =>
        json(
          {
            type: 'urn:reticora:problem:forbidden',
            status: 403,
            detail: 'fields not writable',
            fields: ['value'],
          },
          403,
        ),
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: 'Vertrag anlegen' }));
    let dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Nummer'), { target: { value: 'V-1' } });
    fireEvent.change(within(dialog).getByLabelText('Art'), { target: { value: 'support' } });
    fireEvent.change(within(dialog).getByLabelText('Lieferant'), { target: { value: 'sup-1' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));
    await waitFor(() =>
      expect(within(dialog).getByLabelText('Nummer')).toHaveAttribute('aria-invalid', 'true'),
    );
    expect(
      within(dialog).getAllByText('Diese Nummer hat bereits ein anderer Vertrag.').length,
    ).toBe(2);
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
      number: 'V-1',
      status: 'active',
      kind: 'support',
      supplier_id: 'sup-1',
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Abbrechen' }));

    fireEvent.click(await screen.findByRole('button', { name: 'Vertrag V-1 bearbeiten' }));
    dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Wert'), { target: { value: '99' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() =>
      expect(within(dialog).getByLabelText('Wert')).toHaveAttribute('aria-invalid', 'true'),
    );
    expect(within(dialog).getByText(/Einzelne Felder darfst du nicht ändern/)).toBeInTheDocument();
  });

  it('offers only org-wide suppliers and those of the chosen client', async () => {
    stubApi({ 'GET /contracts': () => page([]) });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Vertrag anlegen' }));
    const dialog = await screen.findByRole('dialog');
    const supplierSelect = within(dialog).getByLabelText('Lieferant');
    await waitFor(() =>
      expect(within(supplierSelect).getByRole('option', { name: 'Wartungs GmbH' })).toBeTruthy(),
    );
    expect(within(supplierSelect).queryByRole('option', { name: 'Client AG' })).toBeNull();
    fireEvent.change(await within(dialog).findByLabelText('Kunde'), {
      target: { value: 'cl-1' },
    });
    expect(within(supplierSelect).getByRole('option', { name: 'Client AG' })).toBeTruthy();
  });

  it('sends the chosen client as filter[client_id] and none to the suppliers', async () => {
    useScopeStore.getState().setClient('cl-1');
    const calls = stubApi({ 'GET /contracts': () => page([contract('k-1', 'V-1')]) });
    renderPage();
    expect(await screen.findByRole('link', { name: 'V-1' })).toBeInTheDocument();

    for (const call of calls.filter((c) => c.path === '/contracts')) {
      expect(call.search.get('filter[client_id]')).toBe('cl-1');
      expect(call.search.has('client_id')).toBe(false);
    }
    const suppliers = calls.filter((c) => c.path === '/suppliers');
    expect(suppliers.length).toBeGreaterThan(0);
    for (const call of suppliers) {
      expect(call.search.has('client_id')).toBe(false);
      expect(call.search.has('filter[client_id]')).toBe(false);
    }
  });
});

describe('ContractsPage detail', () => {
  const doc = (over: Partial<Document> = {}): Document => ({
    id: 'doc-1',
    organization_id: 'org-1',
    title: 'Wartungsvertrag',
    file_name: 'vertrag.pdf',
    file_size: 0,
    mime_type: 'application/pdf',
    storage_key: '',
    version: 1,
    category: 'contract',
    tags: [],
    uploaded_by: 'user-1',
    created_at: AT,
    updated_at: AT,
    scan_status: 'clean',
    ...over,
  });

  it('reads the documents by entity_type=contract and links an upload', async () => {
    const calls = stubApi({
      'GET /contracts/k-1': () => json(contract('k-1', 'V-1')),
      'GET /documents': () => page([doc()]),
      'POST /documents': () => json(doc({ id: 'doc-9' }), 201),
    });
    renderPage('/contracts/k-1');

    expect(await screen.findByRole('heading', { name: 'V-1', level: 1 })).toBeInTheDocument();
    expect(await screen.findByRole('link', { name: 'Wartungsvertrag' })).toHaveAttribute(
      'href',
      '/documents/doc-1',
    );
    const read = calls.find((c) => c.method === 'GET' && c.path === '/documents')!;
    expect(read.search.get('entity_type')).toBe('contract');
    expect(read.search.get('entity_id')).toBe('k-1');

    fireEvent.click(await screen.findByRole('button', { name: 'Dokument hinzufügen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Datei hochladen'), {
      target: {
        files: [new File([new Uint8Array(4)], 'vertrag.pdf', { type: 'application/pdf' })],
      },
    });
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    expect(calls.find((c) => c.method === 'POST')?.body).toMatchObject({
      links: [{ entity_type: 'contract', entity_id: 'k-1' }],
    });
  });

  it('offers no upload without document:write', async () => {
    stubApi(
      {
        'GET /contracts/k-1': () => json(contract('k-1', 'V-1')),
        'GET /documents': () => page([]),
      },
      { permissions: ['contract:read', 'contract:write'] },
    );
    renderPage('/contracts/k-1');
    expect(
      await screen.findByRole('button', { name: 'Vertrag V-1 bearbeiten' }),
    ).toBeInTheDocument();
    expect(await screen.findByText('Noch keine Dokumente verknüpft.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Dokument hinzufügen' })).toBeNull();
  });
});
