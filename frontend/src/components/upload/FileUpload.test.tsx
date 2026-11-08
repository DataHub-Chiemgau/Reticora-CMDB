// UI-14 (WP-321): FileUpload creates a document and sends its content per
// XMLHttpRequest (progress, Content-Type of the file, Bearer token, one
// refresh after a 401) or sends a new version; the failures of the server
// are worded by status and problem type. stubFetchRoutes replaces fetch
// only, so the transfer runs against a mock of XMLHttpRequest. LinkedDocuments
// reads through the function it is given, links an existing document whose
// scan is pending and offers the download only for clean content.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { ApiError, documentApi, type Document } from '../../api/client';
import { FileUpload } from './FileUpload';
import { LinkedDocuments } from '../documents/LinkedDocuments';

const session = vi.hoisted(() => ({ token: 'token-1' as string | null, refreshes: 0 }));

vi.mock('../../auth/session', () => ({
  getSessionToken: () => session.token,
  clearSession: () => {
    session.token = null;
  },
  refreshSession: async () => {
    session.refreshes += 1;
    session.token = 'token-2';
    return true;
  },
}));

/** MockXHR stands in for XMLHttpRequest; a test answers each request by hand. */
class MockXHR {
  static requests: MockXHR[] = [];
  method = '';
  url = '';
  headers: Record<string, string> = {};
  body: unknown;
  status = 0;
  statusText = '';
  responseText = '';
  upload: { onprogress: ((event: ProgressEvent) => void) | null } = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;

  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader(name: string, value: string) {
    this.headers[name] = value;
  }
  send(body: unknown) {
    this.body = body;
    MockXHR.requests.push(this);
  }
  abort() {
    this.onabort?.();
  }
  progress(loaded: number, total: number) {
    this.upload.onprogress?.({ loaded, total, lengthComputable: true } as ProgressEvent);
  }
  respond(status: number, body: unknown) {
    this.status = status;
    this.statusText = `status ${status}`;
    this.responseText = body === undefined ? '' : JSON.stringify(body);
    this.onload?.();
  }
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const doc = (over: Partial<Document> = {}): Document => ({
  id: 'doc-1',
  organization_id: 'org-1',
  title: 'vertrag',
  file_name: 'vertrag.pdf',
  file_size: 0,
  mime_type: 'application/pdf',
  storage_key: '',
  version: 1,
  category: 'general',
  tags: [],
  uploaded_by: 'user-1',
  created_at: '2026-11-01T08:00:00Z',
  updated_at: '2026-11-01T08:00:00Z',
  ...over,
});

interface Call {
  method: string;
  path: string;
  search: URLSearchParams;
  body: unknown;
}

/** stubApi answers fetch by "METHOD /path" (below /api/v1) and records the calls. */
function stubApi(routes: Record<string, (call: Call) => Response>) {
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
      return route
        ? route(call)
        : json({ status: 404, detail: `unexpected ${method} ${call.path}` }, 404);
    }),
  );
  return calls;
}

const pdf = (size = 4) =>
  new File([new Uint8Array(size)], 'vertrag.pdf', { type: 'application/pdf' });

async function nextRequest(index = 0): Promise<MockXHR> {
  await waitFor(() => expect(MockXHR.requests.length).toBeGreaterThan(index));
  return MockXHR.requests[index]!;
}

function choose(file: File) {
  fireEvent.change(screen.getByLabelText('Datei hochladen'), { target: { files: [file] } });
}

beforeEach(() => {
  MockXHR.requests = [];
  session.token = 'token-1';
  session.refreshes = 0;
  vi.stubGlobal('XMLHttpRequest', MockXHR);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('FileUpload', () => {
  it('creates the document and sends the content with progress, type and token', async () => {
    const calls = stubApi({ 'POST /documents': () => json(doc(), 201) });
    const onUploaded = vi.fn();
    renderWithProviders(<FileUpload category="manual" onUploaded={onUploaded} />);

    choose(pdf());
    const xhr = await nextRequest();
    expect(calls.filter((c) => c.method === 'POST')[0]?.body).toEqual({
      title: 'vertrag',
      file_name: 'vertrag.pdf',
      mime_type: 'application/pdf',
      category: 'manual',
    });
    expect(calls[0]?.body).not.toHaveProperty('storage_key');
    expect(xhr.method).toBe('PUT');
    expect(xhr.url).toBe('/api/v1/documents/doc-1/content');
    expect(xhr.headers['Content-Type']).toBe('application/pdf');
    expect(xhr.headers.Authorization).toBe('Bearer token-1');
    expect(xhr.body).toBeInstanceOf(File);

    act(() => xhr.progress(50, 100));
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '50');

    act(() => xhr.respond(200, doc({ file_size: 4, scan_status: 'pending_scan' })));
    await waitFor(() => expect(onUploaded).toHaveBeenCalledTimes(1));
    expect(onUploaded.mock.calls[0]?.[0]).toMatchObject({ documentId: 'doc-1' });
    expect(screen.queryByRole('progressbar')).toBeNull();
    expect(screen.getByRole('status')).toHaveTextContent('vertrag.pdf hochgeladen.');
  });

  it('links a new document to the object of the props in POST /documents', async () => {
    const calls = stubApi({ 'POST /documents': () => json(doc(), 201) });
    renderWithProviders(<FileUpload entityType="ticket" entityId="t-1" />);
    choose(pdf());
    await nextRequest();
    expect(calls[0]?.body).toMatchObject({
      links: [{ entity_type: 'ticket', entity_id: 't-1' }],
    });
  });

  it('refreshes the session once after a 401 and repeats the transfer', async () => {
    stubApi({});
    const onUploaded = vi.fn();
    renderWithProviders(<FileUpload documentId="doc-1" onUploaded={onUploaded} />);
    choose(pdf());

    const first = await nextRequest();
    expect(first.method).toBe('POST');
    expect(first.url).toBe('/api/v1/documents/doc-1/versions');
    act(() => first.respond(401, { status: 401, detail: 'expired' }));

    const second = await nextRequest(1);
    expect(session.refreshes).toBe(1);
    expect(second.headers.Authorization).toBe('Bearer token-2');
    act(() =>
      second.respond(201, {
        id: 'v-2',
        organization_id: 'org-1',
        document_id: 'doc-1',
        version: 2,
        size: 4,
        mime_type: 'application/pdf',
        checksum_sha256: 'ab',
        scan_status: 'pending_scan',
        created_at: '2026-11-01T08:00:00Z',
      }),
    );
    await waitFor(() => expect(onUploaded).toHaveBeenCalledTimes(1));
    expect(onUploaded.mock.calls[0]?.[0]).toMatchObject({ version: { version: 2 } });
  });

  it.each([
    [413, undefined, 'Die Datei ist zu groß für das Upload-Limit des Servers.'],
    [415, undefined, 'Dieser Dateityp ist nicht erlaubt.'],
    [422, 'https://reticora.io/problems/mime-mismatch', 'Der Inhalt passt nicht zum Dateityp.'],
    [
      403,
      'https://reticora.io/problems/entitlement-limit',
      'Das Speicherlimit der Organisation ist erreicht.',
    ],
    [503, undefined, 'Der Upload ist gerade nicht möglich. Bitte später erneut versuchen.'],
    [500, undefined, 'Speicher kaputt (Trace-ID: trace-7)'],
  ])('words a %s (%s) of the transfer', async (status, type, message) => {
    stubApi({});
    const onError = vi.fn();
    renderWithProviders(<FileUpload documentId="doc-1" onError={onError} />);
    choose(pdf());
    const xhr = await nextRequest();
    act(() =>
      xhr.respond(status, { type, status, detail: 'Speicher kaputt', trace_id: 'trace-7' }),
    );
    expect(await screen.findByRole('alert')).toHaveTextContent(message);
    expect(onError).toHaveBeenCalledTimes(1);
  });

  it('shows the detail of a 403 without problem type when creating (scoped principal)', async () => {
    stubApi({
      'POST /documents': () => json({ status: 403, detail: 'the operation is not permitted' }, 403),
    });
    renderWithProviders(<FileUpload />);
    choose(pdf());
    expect(await screen.findByRole('alert')).toHaveTextContent('the operation is not permitted');
    expect(MockXHR.requests).toHaveLength(0);
  });

  it('rejects with an ApiError carrying type and traceId on the XHR path', async () => {
    const pending = documentApi.uploadContent('doc-1', pdf());
    const xhr = await nextRequest();
    xhr.respond(422, {
      type: 'https://reticora.io/problems/mime-mismatch',
      status: 422,
      detail: 'content does not match',
      trace_id: 'trace-9',
    });
    const error = await pending.catch((failure: unknown) => failure);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      status: 422,
      type: 'https://reticora.io/problems/mime-mismatch',
      traceId: 'trace-9',
      detail: 'content does not match',
    });
  });

  it('offers to repeat or discard when the content fails after the document was created', async () => {
    const calls = stubApi({
      'POST /documents': () => json(doc(), 201),
      'DELETE /documents/doc-1': () => new Response(null, { status: 204 }),
    });
    const onUploaded = vi.fn();
    renderWithProviders(<FileUpload onUploaded={onUploaded} />);
    choose(pdf());
    const first = await nextRequest();
    act(() => first.respond(503, { status: 503, detail: 'busy' }));
    await screen.findByRole('alert');

    fireEvent.click(screen.getByRole('button', { name: 'Erneut hochladen' }));
    const second = await nextRequest(1);
    expect(second.url).toBe('/api/v1/documents/doc-1/content');
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1);
    act(() => second.respond(415, { status: 415, detail: 'type' }));
    await screen.findByText('Dieser Dateityp ist nicht erlaubt.');

    fireEvent.click(screen.getByRole('button', { name: 'Verwerfen' }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'DELETE' && c.path === '/documents/doc-1')).toBe(true),
    );
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
    expect(onUploaded).not.toHaveBeenCalled();
  });

  it('checks type and size before the transfer', async () => {
    const calls = stubApi({});
    renderWithProviders(<FileUpload maxBytes={2} />);
    choose(pdf(4));
    expect(await screen.findByRole('alert')).toHaveTextContent('Die Datei ist zu groß');
    choose(new File(['MZ'], 'tool.exe', { type: 'application/x-msdownload' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Dieser Dateityp ist nicht erlaubt.',
    );
    expect(calls).toHaveLength(0);
    expect(MockXHR.requests).toHaveLength(0);
  });

  it('takes a dropped file', async () => {
    stubApi({});
    renderWithProviders(<FileUpload documentId="doc-1" />);
    fireEvent.drop(screen.getByTestId('file-drop-zone'), { dataTransfer: { files: [pdf()] } });
    expect((await nextRequest()).url).toBe('/api/v1/documents/doc-1/versions');
  });
});

describe('LinkedDocuments', () => {
  it('reads through the load function and downloads clean documents only', async () => {
    stubApi({
      'GET /documents/doc-2/content': () => json({ download_url: 'https://blob.test/doc-2' }),
    });
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    const load = vi.fn(async () => [
      doc({ id: 'doc-1', title: 'Neu', scan_status: 'pending_scan' }),
      doc({ id: 'doc-2', title: 'Geprüft', scan_status: 'clean', version: 3 }),
    ]);
    renderWithProviders(<LinkedDocuments entityType="contract" entityId="k-1" load={load} />);

    expect(await screen.findByRole('link', { name: 'Neu' })).toHaveAttribute(
      'href',
      '/documents/doc-1',
    );
    expect(load).toHaveBeenCalledTimes(1);
    expect(screen.getByText('Wird geprüft')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Neu herunterladen' })).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Geprüft herunterladen' }));
    await waitFor(() =>
      expect(open).toHaveBeenCalledWith('https://blob.test/doc-2', '_blank', 'noopener,noreferrer'),
    );
  });

  it('uploads a new document with the object as link', async () => {
    const calls = stubApi({ 'POST /documents': () => json(doc({ id: 'doc-9' }), 201) });
    renderWithProviders(
      <LinkedDocuments entityType="contract" entityId="k-1" load={async () => []} />,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Dokument hinzufügen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Datei hochladen'), {
      target: { files: [pdf()] },
    });
    await nextRequest();
    expect(calls.find((c) => c.method === 'POST')?.body).toMatchObject({
      links: [{ entity_type: 'contract', entity_id: 'k-1' }],
    });
  });

  it('links an existing document whose scan is pending', async () => {
    const linked: Document[] = [];
    const calls = stubApi({
      'GET /documents': () =>
        json({
          data: [doc({ id: 'doc-5', title: 'Lieferschein', scan_status: 'pending_scan' })],
          total: 1,
          limit: 20,
          offset: 0,
          has_more: false,
        }),
      'POST /documents/doc-5/links': () => {
        linked.push(doc({ id: 'doc-5', title: 'Lieferschein', scan_status: 'pending_scan' }));
        return json(
          {
            id: 'l-1',
            organization_id: 'org-1',
            document_id: 'doc-5',
            entity_type: 'contract',
            entity_id: 'k-1',
            created_at: '2026-11-01T08:00:00Z',
          },
          201,
        );
      },
    });
    renderWithProviders(
      <LinkedDocuments entityType="contract" entityId="k-1" load={async () => [...linked]} />,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Vorhandenes verknüpfen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Dokument suchen' }), {
      target: { value: 'Liefer' },
    });
    fireEvent.click(await within(dialog).findByRole('option', {}, { timeout: 2000 }));

    await waitFor(() =>
      expect(calls.find((c) => c.method === 'POST')).toMatchObject({
        path: '/documents/doc-5/links',
        body: { entity_type: 'contract', entity_id: 'k-1' },
      }),
    );
    expect(await screen.findByRole('link', { name: 'Lieferschein' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Lieferschein herunterladen' })).toBeNull();
  });
});
