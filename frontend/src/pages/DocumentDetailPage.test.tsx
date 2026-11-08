// UI-14 (WP-321): the document page lists the versions with scan status and
// a download per version (page by page over next_cursor), reads a pending
// scan again until it ends, offers no download and no new link for a
// quarantined document, shows a 409 document-quarantined as the quarantine
// notice, and creates and removes a link to a CI.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import { renderWithProviders } from '../test/utils';
import { SCAN_POLL_MS } from '../api/hooks';
import { DocumentDetailPage } from './DocumentDetailPage';

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const AT = '2026-11-01T08:00:00Z';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[], nextCursor?: string) => ({
  data,
  total: data.length,
  limit: 50,
  offset: 0,
  has_more: !!nextCursor,
  ...(nextCursor ? { next_cursor: nextCursor } : {}),
});

type Scan = 'pending_scan' | 'clean' | 'infected' | 'scan_failed';

const documentOf = (scan: Scan, version = 2) => ({
  id: 'doc-1',
  organization_id: 'org-1',
  title: 'Betriebshandbuch',
  file_name: 'handbuch.pdf',
  file_size: 4096,
  mime_type: 'application/pdf',
  storage_key: 'org/org-1/documents/doc-1/2',
  version,
  category: 'manual',
  tags: [],
  uploaded_by: 'user-1',
  created_at: AT,
  updated_at: AT,
  scan_status: scan,
});

const versionOf = (version: number, scan: Scan) => ({
  id: `v-${version}`,
  organization_id: 'org-1',
  document_id: 'doc-1',
  version,
  size: 1024 * version,
  mime_type: 'application/pdf',
  checksum_sha256: `${version}`.repeat(64),
  scan_status: scan,
  created_at: AT,
});

interface Call {
  method: string;
  path: string;
  search: URLSearchParams;
  body: unknown;
}

type Route = (call: Call) => Response;

function stubApi(routes: Record<string, Route>) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const call: Call = {
        method,
        path: url.pathname.replace(/^\/api\/v1/, ''),
        search: url.searchParams,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      };
      calls.push(call);
      const route = routes[`${method} ${call.path}`];
      return route ? route(call) : json({ status: 404, detail: 'not found' }, 404);
    }),
  );
  return calls;
}

function renderPage() {
  return renderWithProviders(
    <Routes>
      <Route path="/documents/:id" element={<DocumentDetailPage />} />
    </Routes>,
    { route: '/documents/doc-1' },
  );
}

describe('DocumentDetailPage', () => {
  it('lists the versions page by page with a download per version', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    const calls = stubApi({
      'GET /documents/doc-1': () => json(documentOf('clean', 3)),
      'GET /documents/doc-1/versions': ({ search }) =>
        search.get('cursor') === 'c-2'
          ? json(page([versionOf(1, 'clean')]))
          : json(page([versionOf(3, 'clean'), versionOf(2, 'clean')], 'c-2')),
      'GET /documents/doc-1/links': () => json(page([])),
      'GET /documents/doc-1/content': ({ search }) =>
        json({ download_url: `https://blob.test/doc-1/${search.get('version')}` }),
    });
    renderPage();

    const table = await screen.findByRole('table');
    expect(await within(table).findByText('v3')).toBeInTheDocument();
    expect(within(table).getByText('3'.repeat(12))).toBeInTheDocument();
    expect(within(table).getAllByText('application/pdf')).toHaveLength(2);

    fireEvent.click(screen.getByRole('button', { name: 'Weitere Versionen laden' }));
    expect(await within(table).findByText('v1')).toBeInTheDocument();
    expect(
      calls.some((c) => c.path === '/documents/doc-1/versions' && c.search.get('cursor') === 'c-2'),
    ).toBe(true);
    expect(screen.queryByRole('button', { name: 'Weitere Versionen laden' })).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Version 1 herunterladen' }));
    await waitFor(() =>
      expect(open).toHaveBeenCalledWith(
        'https://blob.test/doc-1/1',
        '_blank',
        'noopener,noreferrer',
      ),
    );
    expect(calls.find((c) => c.path === '/documents/doc-1/content')?.search.get('version')).toBe(
      '1',
    );
  });

  it('reads a pending scan again until it is clean', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let scan: Scan = 'pending_scan';
    const calls = stubApi({
      'GET /documents/doc-1': () => json(documentOf(scan)),
      'GET /documents/doc-1/versions': () => json(page([versionOf(2, scan)])),
      'GET /documents/doc-1/links': () => json(page([])),
    });
    renderPage();

    expect(await screen.findByText(/wird auf Schadsoftware geprüft/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Version 2 herunterladen' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Verknüpfung hinzufügen' })).toBeNull();
    const reads = () => calls.filter((c) => c.path === '/documents/doc-1/versions').length;
    const before = reads();

    scan = 'clean';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SCAN_POLL_MS);
    });
    expect(
      await screen.findByRole('button', { name: 'Version 2 herunterladen' }),
    ).toBeInTheDocument();
    expect(reads()).toBeGreaterThan(before);
    expect(screen.queryByText(/wird auf Schadsoftware geprüft/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Verknüpfung hinzufügen' })).toBeInTheDocument();

    // A final status ends the polling.
    const settled = reads();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SCAN_POLL_MS * 2);
    });
    expect(reads()).toBe(settled);
  });

  it('offers no download and no link for a quarantined document', async () => {
    stubApi({
      'GET /documents/doc-1': () => json(documentOf('infected')),
      'GET /documents/doc-1/versions': () => json(page([versionOf(2, 'infected')])),
      'GET /documents/doc-1/links': () => json(page([])),
    });
    renderPage();

    expect(await screen.findByText(/^Quarantäne:/)).toBeInTheDocument();
    expect(await screen.findAllByText('Infiziert')).not.toHaveLength(0);
    expect(screen.queryByRole('button', { name: /herunterladen/ })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Verknüpfung hinzufügen' })).toBeNull();
  });

  it('shows a 409 document-quarantined of the download as the quarantine notice', async () => {
    stubApi({
      'GET /documents/doc-1': () => json(documentOf('clean')),
      'GET /documents/doc-1/versions': () => json(page([versionOf(2, 'clean')])),
      'GET /documents/doc-1/links': () => json(page([])),
      'GET /documents/doc-1/content': () =>
        json(
          {
            type: 'https://reticora.io/problems/document-quarantined',
            status: 409,
            detail: 'quarantined',
          },
          409,
        ),
    });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Version 2 herunterladen' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/^Quarantäne:/);
  });

  it('links the document to a CI and removes the link', async () => {
    let links: unknown[] = [];
    const calls = stubApi({
      'GET /documents/doc-1': () => json(documentOf('clean')),
      'GET /documents/doc-1/versions': () => json(page([versionOf(2, 'clean')])),
      'GET /documents/doc-1/links': () => json(page(links)),
      'GET /cis': () =>
        json(
          page([{ id: 'ci-1', name: 'web-01', status: 'active', ci_type_id: 't', attributes: {} }]),
        ),
      'GET /cis/ci-1': () =>
        json({ id: 'ci-1', name: 'web-01', status: 'active', ci_type_id: 't', attributes: {} }),
      'POST /documents/doc-1/links': ({ body }) => {
        const link = {
          id: 'l-1',
          organization_id: 'org-1',
          document_id: 'doc-1',
          ...(body as object),
          created_at: AT,
        };
        links = [link];
        return json(link, 201);
      },
      'DELETE /documents/doc-1/links/l-1': () => {
        links = [];
        return new Response(null, { status: 204 });
      },
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: 'Verknüpfung hinzufügen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Objekt' }), {
      target: { value: 'web' },
    });
    fireEvent.click(
      await within(dialog).findByRole('option', { name: /web-01/ }, { timeout: 2000 }),
    );
    fireEvent.click(within(dialog).getByRole('button', { name: 'Verknüpfen' }));

    await waitFor(() =>
      expect(calls.find((c) => c.method === 'POST')).toMatchObject({
        path: '/documents/doc-1/links',
        body: { entity_type: 'ci', entity_id: 'ci-1' },
      }),
    );
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    const list = await screen.findByRole('list', { name: 'Verknüpfungen' });
    expect(await within(list).findByRole('link', { name: 'web-01' })).toHaveAttribute(
      'href',
      '/cmdb/ci-1',
    );

    fireEvent.click(within(list).getByRole('button', { name: 'Verknüpfung (CI) lösen' }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === 'DELETE' && c.path === '/documents/doc-1/links/l-1'),
      ).toBe(true),
    );
    expect(await screen.findByText('Noch keine Verknüpfungen.')).toBeInTheDocument();
  });

  it('shows a refused link as message', async () => {
    stubApi({
      'GET /documents/doc-1': () => json(documentOf('clean')),
      'GET /documents/doc-1/versions': () => json(page([versionOf(2, 'clean')])),
      'GET /documents/doc-1/links': () => json(page([])),
      'GET /cis': () =>
        json(
          page([{ id: 'ci-1', name: 'web-01', status: 'active', ci_type_id: 't', attributes: {} }]),
        ),
      'POST /documents/doc-1/links': () =>
        json({ status: 422, detail: 'entity_type is not a link target' }, 422),
    });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Verknüpfung hinzufügen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Objekt' }), {
      target: { value: 'web' },
    });
    fireEvent.click(
      await within(dialog).findByRole('option', { name: /web-01/ }, { timeout: 2000 }),
    );
    fireEvent.click(within(dialog).getByRole('button', { name: 'Verknüpfen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'entity_type is not a link target',
    );
  });
});
