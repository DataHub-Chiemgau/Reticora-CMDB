/**
 * Tests for the scope control of the layout (UI-01/UI-02): only accessible
 * clients and sites are offered, and the chosen scope narrows the lists.
 * WP-556 (F-340): the options load page by page, and a stored client or site
 * beyond the first 200 is kept after a check by id instead of being dropped.
 * Retest: npx vitest run src/components/ScopeSwitcher.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { ScopeSwitcher } from './ScopeSwitcher';
import { ciApi, withScope } from '../api/client';
import { useScopeStore } from '../stores/scope';

const CLIENT_A = 'aaaaaaaa-0000-4000-8000-000000000001';
const CLIENT_B = 'aaaaaaaa-0000-4000-8000-000000000002';
const SITE_A1 = 'bbbbbbbb-0000-4000-8000-000000000001';

interface Row {
  id: string;
  name: string;
  client_id?: string;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function notFound() {
  return new Response(JSON.stringify({ title: 'Not Found', status: 404 }), {
    status: 404,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function page(data: unknown[]) {
  return { data, total: data.length, limit: 200, offset: 0, has_more: false };
}

/** paged answers a list request like the server: limit capped to 200, offset paging. */
function paged(rows: Row[], url: URL) {
  const limit = Math.min(Number(url.searchParams.get('limit') ?? '50'), 200);
  const offset = Number(url.searchParams.get('offset') ?? '0');
  return json({
    data: rows.slice(offset, offset + limit),
    total: rows.length,
    limit,
    offset,
    has_more: offset + limit < rows.length,
  });
}

const DEFAULT_CLIENTS: Row[] = [
  { id: CLIENT_A, name: 'Kunde A' },
  { id: CLIENT_B, name: 'Kunde B' },
];
const DEFAULT_SITES: Row[] = [{ id: SITE_A1, client_id: CLIENT_A, name: 'RZ Nord' }];

/**
 * stubServer answers like the API for the rows the user may read: the lists
 * page by page (client_id filters the sites), a detail read 200 for a readable
 * row and 404 otherwise.
 */
function stubServer({ clients = DEFAULT_CLIENTS, sites = DEFAULT_SITES } = {}) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://localhost');
    if (url.pathname === '/api/v1/org') return json({ id: 'org-1', name: 'Muster GmbH' });
    if (url.pathname === '/api/v1/clients') return paged(clients, url);
    if (url.pathname === '/api/v1/sites') {
      const client = url.searchParams.get('client_id');
      return paged(
        sites.filter((s) => client === null || s.client_id === client),
        url,
      );
    }
    const detail = /^\/api\/v1\/(clients|sites)\/([^/]+)$/.exec(url.pathname);
    if (detail) {
      const row = (detail[1] === 'clients' ? clients : sites).find((r) => r.id === detail[2]);
      return row ? json(row) : notFound();
    }
    return json(page([]));
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

/** numbered returns n rows named `${prefix} 1` … `${prefix} n` in list order. */
function numbered(n: number, prefix: string, idPrefix: string, clientId?: string): Row[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `${idPrefix}-0000-4000-8000-${String(i + 1).padStart(12, '0')}`,
    name: `${prefix} ${i + 1}`,
    ...(clientId ? { client_id: clientId } : {}),
  }));
}

function requested(fetchMock: ReturnType<typeof stubServer>, fragment: string): boolean {
  return fetchMock.mock.calls.some((call) => String(call[0]).includes(fragment));
}

beforeEach(() => {
  window.localStorage.clear();
  useScopeStore.getState().reset();
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('withScope', () => {
  it('adds client and site only to GETs of scoped lists', () => {
    useScopeStore.setState({ clientId: CLIENT_A, siteId: SITE_A1 });
    expect(withScope('/cis?limit=10')).toBe(
      `/cis?limit=10&client_id=${CLIENT_A}&site_id=${SITE_A1}`,
    );
    expect(withScope('/assets')).toBe(`/assets?client_id=${CLIENT_A}`);
    // An explicit parameter of the caller wins.
    expect(withScope(`/cis?client_id=${CLIENT_B}`)).toBe(
      `/cis?client_id=${CLIENT_B}&site_id=${SITE_A1}`,
    );
    // Detail reads, writes and unscoped lists stay as they are. This stays
    // valid under E-87 (option 1, docs/plan/entscheidungen.md): the CI detail
    // loads unscoped, and the page shows a notice for a CI outside the chosen
    // scope (ScopeBanner) and filters its relationships in the browser
    // (CIRelationshipsCard). The server's row-level security decides access.
    expect(withScope('/cis/abc')).toBe('/cis/abc');
    expect(withScope('/cis', 'POST')).toBe('/cis');
    expect(withScope('/users')).toBe('/users');
  });

  it('leaves lists unchanged without a selection', () => {
    expect(withScope('/cis?limit=10')).toBe('/cis?limit=10');
  });
});

describe('ScopeSwitcher', () => {
  it('offers only the accessible clients and the sites of the chosen client', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<ScopeSwitcher />);

    expect(await screen.findByText('Muster GmbH')).toBeTruthy();
    const clientSelect = screen.getByRole('combobox', { name: /kunde|client/i });
    await waitFor(() => expect(screen.getByRole('option', { name: 'Kunde B' })).toBeTruthy());

    fireEvent.change(clientSelect, { target: { value: CLIENT_A } });
    expect(useScopeStore.getState().clientId).toBe(CLIENT_A);
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some((call) =>
          String(call[0]).includes(`/sites?limit=200&client_id=${CLIENT_A}`),
        ),
      ).toBe(true),
    );
    const siteSelect = screen.getByRole('combobox', { name: /standort|site/i });
    await waitFor(() => expect(screen.getByRole('option', { name: 'RZ Nord' })).toBeTruthy());
    fireEvent.change(siteSelect, { target: { value: SITE_A1 } });
    expect(useScopeStore.getState().siteId).toBe(SITE_A1);

    // The CI list now carries the scope.
    await ciApi.list({ limit: 10 });
    const ciCall = fetchMock.mock.calls.find((call) => String(call[0]).startsWith('/api/v1/cis'));
    expect(String(ciCall?.[0])).toContain(`client_id=${CLIENT_A}`);
    expect(String(ciCall?.[0])).toContain(`site_id=${SITE_A1}`);
  });

  it('drops a stored client the user can no longer access', async () => {
    useScopeStore.setState({ clientId: 'gone', siteId: '' });
    stubServer();
    renderWithProviders(<ScopeSwitcher />);

    await waitFor(() => expect(useScopeStore.getState().clientId).toBe(''));
    expect(screen.queryByRole('option', { name: 'gone' })).toBeNull();
  });

  it('drops a stored site that is gone or belongs to another client', async () => {
    const SITE_B1 = 'bbbbbbbb-0000-4000-8000-000000000002';
    useScopeStore.setState({ clientId: CLIENT_A, siteId: SITE_B1 });
    stubServer({ sites: [...DEFAULT_SITES, { id: SITE_B1, client_id: CLIENT_B, name: 'RZ Süd' }] });
    renderWithProviders(<ScopeSwitcher />);

    await waitFor(() => expect(useScopeStore.getState().siteId).toBe(''));
    expect(useScopeStore.getState().clientId).toBe(CLIENT_A);

    act(() => useScopeStore.setState({ clientId: CLIENT_A, siteId: 'gone' }));
    await waitFor(() => expect(useScopeStore.getState().siteId).toBe(''));
  });

  // F-340: the options were the first 200 clients; a stored client beyond
  // them was dropped on every reload.
  it('keeps a stored client beyond the first 200 options (F-340)', async () => {
    const clients = numbered(201, 'Kunde', 'cccccccc');
    const client201 = clients[200]!;
    useScopeStore.setState({ clientId: client201.id, siteId: '' });
    const fetchMock = stubServer({ clients });
    renderWithProviders(<ScopeSwitcher />);

    // The first page is loaded; client #201 is not on it.
    expect(await screen.findByRole('option', { name: 'Kunde 200' })).toBeTruthy();
    expect(useScopeStore.getState().clientId).toBe(client201.id);

    // It is checked with GET /clients/{id}, kept and offered, without the
    // second page being read.
    expect(await screen.findByRole('option', { name: 'Kunde 201' })).toBeTruthy();
    expect(requested(fetchMock, `/api/v1/clients/${client201.id}`)).toBe(true);
    expect(requested(fetchMock, '/api/v1/clients?limit=200&offset=200')).toBe(false);
    expect(useScopeStore.getState().clientId).toBe(client201.id);
    const clientSelect = screen.getByRole('combobox', { name: /kunde|client/i });
    expect((clientSelect as HTMLSelectElement).value).toBe(client201.id);
    expect(screen.getAllByRole('option', { name: 'Kunde 201' })).toHaveLength(1);
  });

  it('loads further clients page by page', async () => {
    const clients = numbered(201, 'Kunde', 'cccccccc');
    const fetchMock = stubServer({ clients });
    renderWithProviders(<ScopeSwitcher />);

    expect(await screen.findByRole('option', { name: 'Kunde 200' })).toBeTruthy();
    expect(screen.queryByRole('option', { name: 'Kunde 201' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /weitere kunden|more clients/i }));

    expect(await screen.findByRole('option', { name: 'Kunde 201' })).toBeTruthy();
    expect(requested(fetchMock, '/api/v1/clients?limit=200&offset=200')).toBe(true);
    // All 201 clients and "all clients"; nothing more to load.
    const clientSelect = screen.getByRole('combobox', { name: /kunde|client/i });
    expect(clientSelect.querySelectorAll('option')).toHaveLength(202);
    expect(screen.queryByRole('button', { name: /weitere kunden|more clients/i })).toBeNull();

    fireEvent.change(clientSelect, { target: { value: clients[200]!.id } });
    expect(useScopeStore.getState().clientId).toBe(clients[200]!.id);
  });

  it('keeps a stored site beyond the first 200 and loads further sites page by page', async () => {
    const sites = numbered(201, 'Standort', 'dddddddd', CLIENT_A);
    const site201 = sites[200]!;
    useScopeStore.setState({ clientId: CLIENT_A, siteId: site201.id });
    const fetchMock = stubServer({ sites });
    renderWithProviders(<ScopeSwitcher />);

    expect(await screen.findByRole('option', { name: 'Standort 200' })).toBeTruthy();
    expect(useScopeStore.getState().siteId).toBe(site201.id);
    expect(await screen.findByRole('option', { name: 'Standort 201' })).toBeTruthy();
    expect(requested(fetchMock, `/api/v1/sites/${site201.id}`)).toBe(true);
    expect(useScopeStore.getState()).toMatchObject({ clientId: CLIENT_A, siteId: site201.id });
    const siteSelect = screen.getByRole('combobox', { name: /standort|site/i });
    expect((siteSelect as HTMLSelectElement).value).toBe(site201.id);

    fireEvent.click(screen.getByRole('button', { name: /weitere standorte|more sites/i }));
    await waitFor(() =>
      expect(requested(fetchMock, `/api/v1/sites?limit=200&offset=200&client_id=${CLIENT_A}`)).toBe(
        true,
      ),
    );
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: /weitere standorte|more sites/i })).toBeNull(),
    );
    // Site #201 is listed once, now from the second page.
    expect(siteSelect.querySelectorAll('option')).toHaveLength(202);
    expect(screen.getAllByRole('option', { name: 'Standort 201' })).toHaveLength(1);
  });

  it('changing the client resets the site', () => {
    useScopeStore.setState({ clientId: CLIENT_A, siteId: SITE_A1 });
    useScopeStore.getState().setClient(CLIENT_B);
    expect(useScopeStore.getState()).toMatchObject({ clientId: CLIENT_B, siteId: '' });
  });
});
