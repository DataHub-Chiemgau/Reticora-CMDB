/**
 * Tests for the scope notice of detail pages (UI-01/UI-02, E-87 option 1,
 * F-169): an object outside the chosen client or site gets the banner with
 * the chosen scope, an object inside or a page without a selection none;
 * "Bereich wechseln" moves the scope to the object, "Alle Kunden" drops the
 * selection.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { ScopeBanner, inScope } from './ScopeBanner';
import { useScopeStore } from '../stores/scope';

const CLIENT_A = 'aaaaaaaa-0000-4000-8000-000000000001';
const CLIENT_B = 'aaaaaaaa-0000-4000-8000-000000000002';
const SITE_A1 = 'bbbbbbbb-0000-4000-8000-000000000001';
const SITE_B1 = 'bbbbbbbb-0000-4000-8000-000000000002';

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
    if (url.pathname === '/api/v1/clients')
      return json(
        page([
          { id: CLIENT_A, name: 'Kunde A' },
          { id: CLIENT_B, name: 'Kunde B' },
        ]),
      );
    if (url.pathname === '/api/v1/sites')
      return json(page([{ id: SITE_A1, client_id: CLIENT_A, name: 'RZ Nord' }]));
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

describe('inScope', () => {
  it('matches client and site like the scoped lists', () => {
    const ciB = { client_id: CLIENT_B, site_id: SITE_B1 };
    expect(inScope({ clientId: '', siteId: '' }, ciB)).toBe(true);
    expect(inScope({ clientId: CLIENT_B, siteId: '' }, ciB)).toBe(true);
    expect(inScope({ clientId: CLIENT_B, siteId: SITE_B1 }, ciB)).toBe(true);
    expect(inScope({ clientId: CLIENT_A, siteId: '' }, ciB)).toBe(false);
    expect(inScope({ clientId: CLIENT_B, siteId: 'other' }, ciB)).toBe(false);
    // An org-wide CI has no client: a chosen client excludes it, as in the list.
    expect(inScope({ clientId: CLIENT_A, siteId: '' }, {})).toBe(false);
  });
});

describe('ScopeBanner', () => {
  it('shows the notice with the chosen scope for an object outside it', async () => {
    stubServer();
    useScopeStore.setState({ clientId: CLIENT_A, siteId: '' });
    renderWithProviders(<ScopeBanner object={{ client_id: CLIENT_B, site_id: SITE_B1 }} />);

    const banner = await screen.findByRole('status');
    await waitFor(() =>
      expect(banner).toHaveTextContent(
        'Dieses CI liegt außerhalb des gewählten Bereichs (Kunde A).',
      ),
    );
    expect(screen.getByRole('button', { name: 'Bereich wechseln' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Alle Kunden' })).toBeInTheDocument();
  });

  it('names client and site when a site is chosen', async () => {
    stubServer();
    useScopeStore.setState({ clientId: CLIENT_A, siteId: SITE_A1 });
    renderWithProviders(<ScopeBanner object={{ client_id: CLIENT_A, site_id: SITE_B1 }} />);

    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent('(Kunde A / RZ Nord)'),
    );
  });

  it('shows nothing for an object inside the scope', () => {
    const fetchMock = stubServer();
    useScopeStore.setState({ clientId: CLIENT_B, siteId: SITE_B1 });
    renderWithProviders(<ScopeBanner object={{ client_id: CLIENT_B, site_id: SITE_B1 }} />);

    expect(screen.queryByRole('status')).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('shows nothing without a selection', () => {
    stubServer();
    renderWithProviders(<ScopeBanner object={{ client_id: CLIENT_B, site_id: SITE_B1 }} />);

    expect(screen.queryByRole('status')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Bereich wechseln' })).toBeNull();
  });

  it('"Bereich wechseln" sets the client of the object in the store and hides the notice', async () => {
    stubServer();
    useScopeStore.setState({ clientId: CLIENT_A, siteId: '' });
    renderWithProviders(<ScopeBanner object={{ client_id: CLIENT_B, site_id: SITE_B1 }} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Bereich wechseln' }));

    // Only a client was chosen: the switch keeps that granularity.
    expect(useScopeStore.getState()).toMatchObject({ clientId: CLIENT_B, siteId: '' });
    expect(JSON.parse(window.localStorage.getItem('reticora-scope') ?? '{}')).toEqual({
      clientId: CLIENT_B,
      siteId: '',
    });
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
  });

  it('"Bereich wechseln" sets client and site of the object when a site was chosen', async () => {
    stubServer();
    useScopeStore.setState({ clientId: CLIENT_A, siteId: SITE_A1 });
    renderWithProviders(<ScopeBanner object={{ client_id: CLIENT_B, site_id: SITE_B1 }} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Bereich wechseln' }));

    expect(useScopeStore.getState()).toMatchObject({ clientId: CLIENT_B, siteId: SITE_B1 });
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
  });

  it('"Alle Kunden" drops the selection', async () => {
    stubServer();
    useScopeStore.setState({ clientId: CLIENT_A, siteId: SITE_A1 });
    renderWithProviders(<ScopeBanner object={{ client_id: CLIENT_B }} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Alle Kunden' }));

    expect(useScopeStore.getState()).toMatchObject({ clientId: '', siteId: '' });
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
  });
});
