import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { DocumentListPage } from './DocumentListPage';
import { renderWithProviders } from '../test/utils';

// Retest of F-188 (WP-560, MGT-01, TEN-06): the server generates the storage
// key when the content is uploaded and ignores one in POST /documents. The
// form therefore asks for no storage key, creates a document from title and
// file name alone and sends no storage_key.

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

/** stubDocumentApi answers the list and records every POST /documents body. */
function stubDocumentApi() {
  const posts: Record<string, unknown>[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
    const method = (init?.method ?? 'GET').toUpperCase();
    if (url.pathname === '/api/v1/documents' && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      posts.push(body);
      return json(
        {
          id: 'doc-1',
          organization_id: 'org-1',
          title: body.title,
          file_name: body.file_name,
          file_size: 0,
          mime_type: 'application/octet-stream',
          storage_key: '',
          version: 1,
          category: body.category,
          tags: [],
          uploaded_by: 'user-1',
          created_at: '2026-10-28T00:00:00Z',
          updated_at: '2026-10-28T00:00:00Z',
        },
        201,
      );
    }
    if (url.pathname === '/api/v1/documents' && method === 'GET') {
      return json({ data: [], total: 0, limit: 50, offset: 0, has_more: false });
    }
    return json({ status: 404, detail: `unexpected request: ${method} ${url.pathname}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return posts;
}

describe('DocumentListPage create form', () => {
  it('creates a document without a storage key', async () => {
    const posts = stubDocumentApi();
    renderWithProviders(<DocumentListPage />, { route: '/documents' });

    fireEvent.click(await screen.findByRole('button', { name: 'Anlegen' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).queryByLabelText(/storage/i)).toBeNull();
    expect(within(dialog).queryByText(/storage/i)).toBeNull();

    fireEvent.change(within(dialog).getByLabelText(/Titel/), {
      target: { value: 'Wartungsvertrag' },
    });
    fireEvent.change(within(dialog).getByLabelText(/Dateiname/), {
      target: { value: 'vertrag.pdf' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({
      title: 'Wartungsvertrag',
      file_name: 'vertrag.pdf',
      category: 'general',
      file_size: 0,
    });
    expect(posts[0]).not.toHaveProperty('storage_key');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('does not create a document without title and file name', async () => {
    const posts = stubDocumentApi();
    renderWithProviders(<DocumentListPage />, { route: '/documents' });

    fireEvent.click(await screen.findByRole('button', { name: 'Anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/Titel/), {
      target: { value: 'Ohne Dateiname' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(posts).toHaveLength(0);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});
