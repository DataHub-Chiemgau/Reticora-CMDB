/**
 * Tests for the scope control of the layout (UI-01/UI-02): only accessible
 * clients and sites are offered, and the chosen scope narrows the lists.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { ScopeSwitcher } from './ScopeSwitcher';
import { ciApi, withScope } from '../api/client';
import { useScopeStore } from '../stores/scope';

const CLIENT_A = 'aaaaaaaa-0000-4000-8000-000000000001';
const CLIENT_B = 'aaaaaaaa-0000-4000-8000-000000000002';
const SITE_A1 = 'bbbbbbbb-0000-4000-8000-000000000001';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function page(data: unknown[]) {
  return { data, total: data.length, limit: 200, offset: 0, has_more: false };
}

function stubServer() {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://localhost');
    if (url.pathname === '/api/v1/org') return json({ id: 'org-1', name: 'Muster GmbH' });
    if (url.pathname === '/api/v1/clients')
      return json(
        page([
          { id: CLIENT_A, name: 'Kunde A' },
          { id: CLIENT_B, name: 'Kunde B' },
        ]),
      );
    if (url.pathname === '/api/v1/sites') {
      const client = url.searchParams.get('client_id');
      return json(
        page(
          client === CLIENT_A || client === null
            ? [{ id: SITE_A1, client_id: CLIENT_A, name: 'RZ Nord' }]
            : [],
        ),
      );
    }
    return json(page([]));
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
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
    // Detail reads, writes and unscoped lists stay as they are.
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
  });

  it('changing the client resets the site', () => {
    useScopeStore.setState({ clientId: CLIENT_A, siteId: SITE_A1 });
    useScopeStore.getState().setClient(CLIENT_B);
    expect(useScopeStore.getState()).toMatchObject({ clientId: CLIENT_B, siteId: '' });
  });
});
