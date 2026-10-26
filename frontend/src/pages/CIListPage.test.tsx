import { afterEach, describe, expect, it, onTestFinished, vi } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useLocation } from 'react-router-dom';
import { CIListPage } from './CIListPage';
import { ToastViewport } from '../components/ui/Toast';
import { useScopeStore } from '../stores/scope';
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

  it('bulk-updates the status of selected CIs in one request', async () => {
    const fetchMock = stubFetchRoutes({
      '/cis/bulk': { action: 'set_status', total: 1, succeeded: 1, failed: 0, items: [] },
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
    const isBulk = (call: [string, RequestInit | undefined]) =>
      call[0].includes('/cis/bulk') && call[1]?.method === 'POST';

    await waitFor(() => {
      expect(calls.find(isBulk)).toBeTruthy();
    });
    const bulkCall = calls.find(isBulk);
    expect(JSON.parse(bulkCall?.[1]?.body as string)).toEqual({
      action: 'set_status',
      ids: ['ci-1'],
      status: 'maintenance',
    });
    expect(calls.some((call) => call[1]?.method === 'PATCH')).toBe(false);

    // Success toast is announced in the live region.
    expect(await screen.findByText('Status von 1 CIs aktualisiert')).toBeInTheDocument();
  });

  it('shows the error of every CI the bulk change failed for', async () => {
    stubFetchRoutes({
      '/cis/bulk': {
        action: 'set_status',
        total: 1,
        succeeded: 0,
        failed: 1,
        items: [{ id: 'ci-1', result: 'error', code: 404, error: 'CI not found' }],
      },
      '/cis': { data: [ci], total: 1, limit: 25, offset: 0, has_more: false },
    });

    renderWithProviders(
      <>
        <CIListPage onCreateCI={() => {}} />
        <ToastViewport />
      </>,
      { route: '/cmdb' },
    );

    fireEvent.click(await screen.findByRole('checkbox', { name: 'core-sw-01 auswählen' }));
    fireEvent.change(screen.getByRole('combobox', { name: 'Status setzen…' }), {
      target: { value: 'inactive' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Anwenden' }));

    const failures = await screen.findByRole('list', { name: 'Fehlgeschlagene CIs' });
    expect(failures).toHaveTextContent('core-sw-01: CI not found');
    expect(await screen.findByText('0 CIs aktualisiert, 1 fehlgeschlagen')).toBeInTheDocument();
  });
});

/**
 * WP-468 (F-077): the export buttons were plain links to the deprecated
 * /api/v1/export/cis; the browser sent no bearer token and showed the 401 as
 * raw JSON. They now queue an export job with the list filters, follow it
 * and download the file over its signed URL.
 */
describe('CIListPage export', () => {
  const list = { data: [ci], total: 1, limit: 25, offset: 0, has_more: false };
  const signedUrl = 'http://minio:9000/exports/org-1/job-1.csv?X-Amz-Signature=abc';

  function json(body: unknown, status = 200) {
    return new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  function job(status: string, extra: Record<string, unknown> = {}) {
    return {
      id: 'job-1',
      organization_id: 'org-1',
      format: 'csv',
      status,
      filters: {},
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
      ...extra,
    };
  }

  /** stubExport answers the list, the job creation and the job reads in turn. */
  function stubExport(created: Response, reads: Response[]) {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url === signedUrl) {
        return new Response('﻿id;name\nci-1;core-sw-01\n', {
          status: 200,
          headers: { 'Content-Type': 'text/csv' },
        });
      }
      if (url.includes('/exports/job-1')) {
        return (reads.length > 1 ? reads.shift()! : reads[0]!).clone();
      }
      if (url.includes('/exports') && init?.method === 'POST') {
        return created.clone();
      }
      if (url.includes('/cis')) {
        return json(list);
      }
      return json({ detail: `unexpected request: ${url}` }, 404);
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  function calls(fetchMock: ReturnType<typeof stubExport>) {
    return fetchMock.mock.calls.map(([input, init]) => ({
      url: typeof input === 'string' ? input : input.toString(),
      init,
    }));
  }

  it('queues a job with the list filters, polls it and downloads the file', async () => {
    const fetchMock = stubExport(json(job('pending'), 202), [
      json(job('running')),
      json(job('completed', { row_count: 1, download_url: signedUrl })),
    ]);
    // jsdom has no object URLs; the stubs are restored after the test.
    const createObjectURL = vi.fn(() => 'blob:export-1');
    const original = { create: URL.createObjectURL, revoke: URL.revokeObjectURL };
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = vi.fn();
    onTestFinished(() => {
      URL.createObjectURL = original.create;
      URL.revokeObjectURL = original.revoke;
    });
    const clicked: { href: string; download: string }[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      clicked.push({ href: this.href, download: this.download });
    });

    // The client chosen in the layout narrows the list and the export alike.
    useScopeStore.setState({ clientId: 'client-1', siteId: '' });
    onTestFinished(() => useScopeStore.setState({ clientId: '', siteId: '' }));

    renderWithProviders(<CIListPage onCreateCI={() => {}} />, {
      route: '/cmdb?status=active&type=switch&q=core',
    });
    await screen.findByRole('link', { name: 'core-sw-01' });
    fireEvent.click(screen.getByRole('button', { name: 'CSV exportieren' }));

    // Progress is shown while the worker renders the file; the search is no
    // job filter and the line says so.
    const progress = await screen.findByText(/CSV-Export: (Ausstehend|Läuft)/);
    expect(progress).toHaveTextContent('Suche und Standort werden beim Export nicht angewendet');
    expect(
      await screen.findByText('CSV-Export heruntergeladen (1 Zeilen)', undefined, {
        timeout: 4000,
      }),
    ).toBeInTheDocument();

    const all = calls(fetchMock);
    const create = all.find((c) => c.url.endsWith('/api/v1/exports') && c.init?.method === 'POST');
    expect(create, 'POST /api/v1/exports').toBeTruthy();
    expect(JSON.parse(create!.init!.body as string)).toEqual({
      format: 'csv',
      filters: { status: 'active', ci_type_id: 'switch', client_id: 'client-1' },
    });
    // Polled until completed: one read while running, one completed.
    expect(all.filter((c) => c.url.endsWith('/api/v1/exports/job-1'))).toHaveLength(2);
    // The signed URL is fetched as is, without the session's headers.
    const download = all.find((c) => c.url === signedUrl);
    expect(download, 'download over download_url').toBeTruthy();
    expect(download!.init).toBeUndefined();
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(clicked).toEqual([{ href: 'blob:export-1', download: 'cis_export.csv' }]);
    // The deprecated streaming endpoint is not used.
    expect(all.some((c) => c.url.includes('/export/cis'))).toBe(false);
  });

  it('shows a localized message when the export is not allowed (403)', async () => {
    const fetchMock = stubExport(
      json({ title: 'Forbidden', status: 403, detail: 'feature exports is not entitled' }, 403),
      [],
    );

    renderWithProviders(<CIListPage onCreateCI={() => {}} />, { route: '/cmdb' });
    await screen.findByRole('link', { name: 'core-sw-01' });
    fireEvent.click(screen.getByRole('button', { name: 'JSON exportieren' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Export nicht erlaubt: Ihnen fehlt das Recht oder Ihr Tarif enthält den Export nicht.',
    );
    const create = calls(fetchMock).find((c) => c.init?.method === 'POST');
    expect(JSON.parse(create!.init!.body as string)).toEqual({ format: 'json', filters: {} });
    expect(calls(fetchMock).some((c) => c.url.includes('/exports/job-1'))).toBe(false);
    // The buttons are usable again for a new attempt.
    expect(screen.getByRole('button', { name: 'JSON exportieren' })).toBeEnabled();
  });

  it('shows a localized message for server errors and failed jobs', async () => {
    stubExport(json({ title: 'Service Unavailable', status: 503 }, 503), []);
    const { unmount } = renderWithProviders(<CIListPage onCreateCI={() => {}} />, {
      route: '/cmdb',
    });
    await screen.findByRole('link', { name: 'core-sw-01' });
    fireEvent.click(screen.getByRole('button', { name: 'CSV exportieren' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Der Export ist derzeit nicht verfügbar.',
    );
    unmount();

    stubExport(json(job('pending'), 202), [json(job('failed', { error_message: 'boom' }))]);
    renderWithProviders(<CIListPage onCreateCI={() => {}} />, { route: '/cmdb' });
    await screen.findByRole('link', { name: 'core-sw-01' });
    fireEvent.click(screen.getByRole('button', { name: 'CSV exportieren' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Der Export ist fehlgeschlagen. Bitte versuchen Sie es erneut.',
    );
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

function PathProbe() {
  const location = useLocation();
  return <span data-testid="location-path">{location.pathname}</span>;
}

// UI-04 / UI-10 (WP-519, F-170): the CI table follows the row keyboard pattern.
describe('CIListPage keyboard', () => {
  const second = { ...ci, id: 'ci-2', name: 'core-sw-02' };
  const third = { ...ci, id: 'ci-3', name: 'core-sw-03' };

  function renderList() {
    stubFetchRoutes({
      '/cis': { data: [ci, second, third], total: 3, limit: 25, offset: 0, has_more: false },
    });
    return renderWithProviders(
      <>
        <CIListPage onCreateCI={() => {}} />
        <PathProbe />
      </>,
      { route: '/cmdb' },
    );
  }

  function row(name: string) {
    return screen.getByRole('link', { name }).closest('tr') as HTMLTableRowElement;
  }

  it('has one row in the Tab order instead of a link and a checkbox per row', async () => {
    const user = userEvent.setup();
    renderList();
    await screen.findByRole('link', { name: 'core-sw-01' });

    const bodyRows = [row('core-sw-01'), row('core-sw-02'), row('core-sw-03')];
    expect(bodyRows.map((r) => r.tabIndex)).toEqual([0, -1, -1]);
    for (const name of ['core-sw-01', 'core-sw-02', 'core-sw-03']) {
      expect(screen.getByRole('link', { name }).tabIndex).toBe(-1);
      expect(screen.getByRole('checkbox', { name: `${name} auswählen` }).tabIndex).toBe(-1);
    }

    // Tab from the select-all box lands on the first row, the next Tab
    // leaves the table for the paging buttons.
    act(() => screen.getByRole('checkbox', { name: 'Alle sichtbaren CIs auswählen' }).focus());
    await user.tab();
    expect(document.activeElement).toBe(row('core-sw-01'));
    await user.tab();
    expect(document.activeElement).not.toBe(row('core-sw-02'));
    expect(row('core-sw-01').contains(document.activeElement)).toBe(false);
  });

  it('moves with ArrowDown, toggles the selection with Space and opens with Enter', async () => {
    const user = userEvent.setup();
    renderList();
    await screen.findByRole('link', { name: 'core-sw-01' });
    act(() => screen.getByRole('checkbox', { name: 'Alle sichtbaren CIs auswählen' }).focus());
    await user.tab();
    expect(document.activeElement).toBe(row('core-sw-01'));

    await user.keyboard('{ArrowDown}');
    expect(document.activeElement).toBe(row('core-sw-02'));
    expect(row('core-sw-02').tabIndex).toBe(0);
    expect(row('core-sw-01').tabIndex).toBe(-1);

    // Space selects the focused row only; the bulk toolbar appears.
    await user.keyboard(' ');
    expect(screen.getByRole('checkbox', { name: 'core-sw-02 auswählen' })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'core-sw-01 auswählen' })).not.toBeChecked();
    expect(await screen.findByText('1 ausgewählt')).toBeInTheDocument();
    expect(document.activeElement).toBe(row('core-sw-02'));
    await user.keyboard(' ');
    expect(screen.getByRole('checkbox', { name: 'core-sw-02 auswählen' })).not.toBeChecked();

    await user.keyboard('{End}');
    expect(document.activeElement).toBe(row('core-sw-03'));
    await user.keyboard('{Home}{ArrowDown}');
    expect(screen.getByTestId('location-path')).toHaveTextContent('/cmdb');
    await user.keyboard('{Enter}');
    await waitFor(() =>
      expect(screen.getByTestId('location-path')).toHaveTextContent('/cmdb/ci-2'),
    );
  });
});
