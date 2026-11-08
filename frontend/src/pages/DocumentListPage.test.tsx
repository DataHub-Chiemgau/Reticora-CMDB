import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { DocumentListPage } from './DocumentListPage';
import { renderWithProviders } from '../test/utils';

// Retest of F-188 (WP-560, MGT-01, TEN-06): the server generates the storage
// key when the content is uploaded and ignores one in POST /documents. Since
// WP-321 a document is created only through FileUpload: title, file name and
// type come from the file, no storage key is asked for or sent. The page
// shows the storage usage of the organization (WP-731) with a warning from
// 90 % of the limit and after a refused upload (403 entitlement-limit).

const MIB = 1024 * 1024;

/** MockXHR answers the content transfer of FileUpload. */
class MockXHR {
  static requests: MockXHR[] = [];
  url = '';
  status = 0;
  statusText = '';
  responseText = '';
  upload = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  open(_method: string, url: string) {
    this.url = url;
  }
  setRequestHeader() {}
  send() {
    MockXHR.requests.push(this);
  }
  abort() {}
  respond(status: number, body: unknown) {
    this.status = status;
    this.responseText = JSON.stringify(body);
    this.onload?.();
  }
}

beforeEach(() => {
  MockXHR.requests = [];
  vi.stubGlobal('XMLHttpRequest', MockXHR);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const DOCUMENT = {
  id: 'doc-1',
  organization_id: 'org-1',
  title: 'Wartungsvertrag',
  file_name: 'wartungsvertrag.pdf',
  file_size: 2048,
  mime_type: 'application/pdf',
  storage_key: '',
  version: 2,
  category: 'contract',
  tags: [],
  uploaded_by: 'user-1',
  created_at: '2026-10-28T00:00:00Z',
  updated_at: '2026-10-28T00:00:00Z',
  scan_status: 'clean',
};

/** stubDocumentApi answers the list and the usage and records every POST /documents body. */
function stubDocumentApi(
  usage: { storage_bytes: number; limit_mb: number | null } = {
    storage_bytes: 10 * MIB,
    limit_mb: 100,
  },
  documents: unknown[] = [],
) {
  const posts: Record<string, unknown>[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
    const method = (init?.method ?? 'GET').toUpperCase();
    if (url.pathname === '/api/v1/documents' && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      posts.push(body);
      return json({ ...DOCUMENT, ...body, id: 'doc-new', file_size: 0, version: 1 }, 201);
    }
    if (url.pathname === '/api/v1/documents/usage' && method === 'GET') {
      return json(usage);
    }
    if (url.pathname === '/api/v1/documents' && method === 'GET') {
      return json({
        data: documents,
        total: documents.length,
        limit: 50,
        offset: 0,
        has_more: false,
      });
    }
    return json({ status: 404, detail: `unexpected request: ${method} ${url.pathname}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return posts;
}

describe('DocumentListPage upload', () => {
  it('creates a document only through FileUpload and without a storage key', async () => {
    const posts = stubDocumentApi();
    renderWithProviders(<DocumentListPage />, { route: '/documents' });

    fireEvent.click(await screen.findByRole('button', { name: 'Dokument hochladen' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).queryByLabelText(/storage/i)).toBeNull();
    expect(within(dialog).queryByText(/storage/i)).toBeNull();
    // No metadata form: title and file name come from the file.
    expect(within(dialog).queryByLabelText(/Titel/)).toBeNull();
    expect(within(dialog).queryByLabelText(/Dateiname/)).toBeNull();

    fireEvent.change(within(dialog).getByLabelText('Kategorie'), {
      target: { value: 'contract' },
    });
    fireEvent.change(within(dialog).getByLabelText('Datei hochladen'), {
      target: {
        files: [new File(['%PDF-'], 'wartungsvertrag.pdf', { type: 'application/pdf' })],
      },
    });

    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({
      title: 'wartungsvertrag',
      file_name: 'wartungsvertrag.pdf',
      mime_type: 'application/pdf',
      category: 'contract',
    });
    expect(posts[0]).not.toHaveProperty('storage_key');

    await waitFor(() => expect(MockXHR.requests).toHaveLength(1));
    expect(MockXHR.requests[0]?.url).toBe('/api/v1/documents/doc-new/content');
    act(() => MockXHR.requests[0]?.respond(200, { ...DOCUMENT, id: 'doc-new' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('links the title to the detail page and shows the scan status', async () => {
    stubDocumentApi(undefined, [DOCUMENT]);
    renderWithProviders(<DocumentListPage />, { route: '/documents' });
    expect(await screen.findByRole('link', { name: 'Wartungsvertrag' })).toHaveAttribute(
      'href',
      '/documents/doc-1',
    );
    expect(screen.getByText('Sauber')).toBeInTheDocument();
    expect(screen.getByText('v2')).toBeInTheDocument();
  });

  it('shows fields left out by the field permissions as missing', async () => {
    // RBA-07 (WP-267): file_name, category and file_size are not readable.
    const hidden: Record<string, unknown> = { ...DOCUMENT };
    for (const key of ['file_name', 'category', 'file_size']) delete hidden[key];
    stubDocumentApi(undefined, [hidden]);
    renderWithProviders(<DocumentListPage />, { route: '/documents' });
    const link = await screen.findByRole('link', { name: 'Wartungsvertrag' });
    const row = link.closest('tr');
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getAllByText('—')).toHaveLength(3);
    expect(within(row as HTMLElement).queryByText('wartungsvertrag.pdf')).toBeNull();
  });
});

describe('DocumentListPage storage usage', () => {
  it('shows the usage against the limit without a warning below 90 %', async () => {
    stubDocumentApi({ storage_bytes: 10 * MIB, limit_mb: 100 });
    renderWithProviders(<DocumentListPage />, { route: '/documents' });
    const meter = await screen.findByRole('meter', { name: 'Speicherverbrauch' });
    expect(meter).toHaveAttribute('aria-valuenow', '10');
    expect(screen.getByText('10,0 MB von 100,0 MB (10 %)')).toBeInTheDocument();
    expect(screen.queryByText(/fast voll/)).toBeNull();
  });

  it('warns from 90 % of the limit', async () => {
    stubDocumentApi({ storage_bytes: 90 * MIB, limit_mb: 100 });
    renderWithProviders(<DocumentListPage />, { route: '/documents' });
    expect(await screen.findByRole('meter')).toHaveAttribute('aria-valuenow', '90');
    expect(screen.getByRole('status')).toHaveTextContent(
      'Der Speicher der Organisation ist fast voll.',
    );
  });

  it('shows only the usage without a limit', async () => {
    stubDocumentApi({ storage_bytes: 3 * MIB, limit_mb: null });
    renderWithProviders(<DocumentListPage />, { route: '/documents' });
    expect(await screen.findByText('3,0 MB belegt')).toBeInTheDocument();
    expect(screen.queryByRole('meter')).toBeNull();
  });

  it('warns after an upload refused by the storage limit', async () => {
    stubDocumentApi({ storage_bytes: 10 * MIB, limit_mb: 100 });
    renderWithProviders(<DocumentListPage />, { route: '/documents' });
    fireEvent.click(await screen.findByRole('button', { name: 'Dokument hochladen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Datei hochladen'), {
      target: { files: [new File(['%PDF-'], 'a.pdf', { type: 'application/pdf' })] },
    });
    await waitFor(() => expect(MockXHR.requests).toHaveLength(1));
    act(() =>
      MockXHR.requests[0]?.respond(403, {
        type: 'https://reticora.io/problems/entitlement-limit',
        status: 403,
        detail: 'max_storage_mb reached',
      }),
    );
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Das Speicherlimit der Organisation ist erreicht.',
    );
    expect(
      await screen.findByText(
        'Das Speicherlimit der Organisation ist erreicht. Lösche Dokumente oder erweitere das Paket.',
      ),
    ).toBeInTheDocument();
  });
});
