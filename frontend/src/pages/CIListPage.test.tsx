import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { useLocation } from 'react-router-dom';
import { CIListPage } from './CIListPage';
import { ToastViewport } from '../components/ui/Toast';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

const ci = {
  id: 'ci-1',
  organization_id: 'org-1',
  ci_type_id: 'switch',
  name: 'core-sw-01',
  status: 'active',
  manufacturer: 'Acme',
  attributes: {},
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CIListPage', () => {
  it('links every row to the deep-linkable detail page', async () => {
    stubFetchRoutes({
      '/cis': { data: [ci], total: 1, limit: 25, offset: 0, has_more: false },
    });

    renderWithProviders(<CIListPage onCreateCI={() => {}} />, { route: '/cmdb' });

    expect(await screen.findByRole('link', { name: 'core-sw-01' })).toHaveAttribute(
      'href',
      '/cmdb/ci-1',
    );
  });

  it('offers the create action from the empty state', async () => {
    stubFetchRoutes({
      '/cis': { data: [], total: 0, limit: 25, offset: 0, has_more: false },
    });
    const onCreateCI = vi.fn();

    renderWithProviders(<CIListPage onCreateCI={onCreateCI} />, { route: '/cmdb' });

    expect(await screen.findByText('Noch keine Configuration Items')).toBeInTheDocument();
    const createButtons = screen.getAllByRole('button', { name: 'CI erstellen' });
    fireEvent.click(createButtons[createButtons.length - 1]!);
    expect(onCreateCI).toHaveBeenCalledTimes(1);
  });

  it('bulk-updates the status of selected CIs', async () => {
    const fetchMock = stubFetchRoutes({
      '/cis': { data: [ci], total: 1, limit: 25, offset: 0, has_more: false },
    });

    renderWithProviders(
      <>
        <CIListPage onCreateCI={() => {}} />
        <ToastViewport />
      </>,
      { route: '/cmdb' },
    );

    // Select the single visible row; the bulk toolbar appears.
    fireEvent.click(await screen.findByRole('checkbox', { name: 'core-sw-01 auswählen' }));
    expect(await screen.findByText('1 ausgewählt')).toBeInTheDocument();

    // Choose a new status and apply it.
    fireEvent.change(screen.getByRole('combobox', { name: 'Status setzen…' }), {
      target: { value: 'maintenance' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Anwenden' }));

    const calls = fetchMock.mock.calls as unknown as [string, RequestInit | undefined][];
    const isPatch = (call: [string, RequestInit | undefined]) =>
      call[0].includes('/cis/ci-1') && call[1]?.method === 'PATCH';

    await waitFor(() => {
      expect(calls.find(isPatch)).toBeTruthy();
    });
    const patchCall = calls.find(isPatch);
    expect(JSON.parse(patchCall?.[1]?.body as string)).toEqual({ status: 'maintenance' });

    // Success toast is announced in the live region.
    expect(await screen.findByText('Status von 1 CIs aktualisiert')).toBeInTheDocument();
  });
});

/**
 * Filter state used to live only in an in-memory store, so reloading the page
 * or sharing its URL silently dropped every active filter and showed an
 * unfiltered list instead. These tests pin the filters to the query string.
 */
/** Renders the router's current query string so it can be asserted on. */
function LocationProbe() {
  const location = useLocation();
  return <span data-testid="location-search">{location.search}</span>;
}

describe('CIListPage filter persistence', () => {
  function emptyList() {
    return vi.fn().mockImplementation(
      async () =>
        new Response(
          JSON.stringify({ data: [], total: 0, limit: 25, offset: 0, has_more: false }),
          {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          },
        ),
    );
  }

  /** The CI list request URLs the page issued, oldest first. */
  function listUrls(fetchMock: ReturnType<typeof emptyList>) {
    return fetchMock.mock.calls.map((c) => String(c[0])).filter((u) => u.includes('/cis'));
  }

  it('applies filters taken from the URL on first render', async () => {
    const fetchMock = emptyList();
    vi.stubGlobal('fetch', fetchMock);

    // This is what a reload or a shared link looks like.
    renderWithProviders(<CIListPage onCreateCI={() => {}} />, {
      route: '/cmdb?q=srv-app&status=active',
    });

    await waitFor(() => expect(listUrls(fetchMock).length).toBeGreaterThan(0));
    const url = listUrls(fetchMock)[0];
    expect(url).toContain('search=srv-app');
    expect(url).toContain('status=active');

    // The controls reflect the shared state rather than appearing empty.
    expect(screen.getByDisplayValue('srv-app')).toBeInTheDocument();
  });

  it('writes typed filters into the URL so they survive a reload', async () => {
    const fetchMock = emptyList();
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(
      <>
        <CIListPage onCreateCI={() => {}} />
        <LocationProbe />
      </>,
      { route: '/cmdb' },
    );

    const searchBox = await screen.findByRole('textbox');
    fireEvent.change(searchBox, { target: { value: 'db-01' } });

    await waitFor(() => {
      expect(listUrls(fetchMock).some((u) => u.includes('search=db-01'))).toBe(true);
    });
    // The filter must land in the address bar; that is what makes the view
    // reload-safe and shareable rather than merely held in memory.
    await waitFor(() => {
      expect(screen.getByTestId('location-search').textContent).toContain('q=db-01');
    });
  });

  it('drops cleared filters from the query string', async () => {
    const fetchMock = emptyList();
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIListPage onCreateCI={() => {}} />, { route: '/cmdb?q=stale' });

    const searchBox = await screen.findByRole('textbox');
    expect(searchBox).toHaveValue('stale');

    fireEvent.change(searchBox, { target: { value: '' } });

    await waitFor(() => expect(searchBox).toHaveValue(''));
    // An empty filter must not linger as stale criteria in a shared link.
    await waitFor(() => {
      const urls = listUrls(fetchMock);
      expect(urls[urls.length - 1]).not.toContain('search=stale');
    });
  });
});
