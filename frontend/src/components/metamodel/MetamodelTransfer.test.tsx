/**
 * WP-800 (UI-11 [P2], MET-54, MET-53): export and import of the metamodel.
 *
 *   Export   GET /ci-types/export?format_version=2 over fetchAPI with the
 *            Authorization header, saved as a Blob download
 *   Import   only with citype:manage; 201 makes the draft active and opens
 *            /ci-types/drafts/{id} with the report; 200 with
 *            report.unchanged is a note without a draft; 422 shows the
 *            conflict report with problem detail and trace id
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/metamodel/MetamodelTransfer.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { Route, Routes, useLocation } from 'react-router';
import { renderWithProviders } from '../../test/utils';
import { useAuthStore } from '../../auth/authStore';
import { useMetamodelDraftStore } from '../../stores/metamodelDraft';
import { ImportReportNotice, MetamodelTransfer } from './MetamodelTransfer';

interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
  body: string | undefined;
  headers: Headers;
}

const TOKEN = 'header.payload.signature';
const DRAFT_ID = 'df000000-0000-4000-8000-000000000800';
const EXPORT = { format_version: 2, ci_types: [{ key: 'router' }] };

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const REPORT = {
  unchanged: false,
  items: [
    { kind: 'ci_type', key: 'router', action: 'create' },
    { kind: 'attribute_definition', key: 'ports', owner: 'ci_type:router', action: 'update' },
    { kind: 'ci_type', key: 'server', action: 'unchanged' },
  ],
  conflicts: [],
  notes: [
    {
      code: 'history-skipped',
      kind: 'template',
      key: 'router_base',
      detail: 'older versions are not imported',
    },
  ],
};

interface Backend {
  permissions?: string[];
  importReply?: () => Response;
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({
        method,
        path,
        query: url.searchParams,
        body: init?.body ? String(init.body) : undefined,
        headers: new Headers(init?.headers),
      });
      if (path === '/me/permissions') {
        return json({ user_id: 'u1', permissions: b.permissions ?? ['citype:manage'] });
      }
      if (path === '/ci-types/export') return json(EXPORT);
      if (path === '/metamodel/import' && method === 'POST') {
        if (b.importReply) return b.importReply();
        return json({ draft_id: DRAFT_ID, report: REPORT }, 201);
      }
      if (path === `/metamodel/drafts/${DRAFT_ID}`) {
        return json({ id: DRAFT_ID, name: 'Import Router', status: 'draft', version: 1 });
      }
      return json({ data: [] });
    }),
  );
  return calls;
}

function DraftProbe() {
  const location = useLocation();
  return (
    <div>
      <p data-testid="path">{location.pathname}</p>
      <ImportReportNotice />
    </div>
  );
}

function renderTransfer() {
  return renderWithProviders(
    <Routes>
      <Route path="/ci-types" element={<MetamodelTransfer />} />
      <Route path="/ci-types/drafts/:id" element={<DraftProbe />} />
    </Routes>,
    { route: '/ci-types' },
  );
}

async function upload(text: string, name = 'metamodel.json') {
  const input = await screen.findByLabelText('Datei für den Metamodell-Import');
  const file = new File([text], name, { type: 'application/json' });
  fireEvent.change(input, { target: { files: [file] } });
}

const imports = (calls: Call[]) =>
  calls.filter((c) => c.method === 'POST' && c.path === '/metamodel/import');

beforeEach(() => {
  useAuthStore.setState({
    token: TOKEN,
    expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
    user: { sub: 'u1' },
  });
  useMetamodelDraftStore.getState().clear();
});

afterEach(() => {
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  useMetamodelDraftStore.getState().clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('MetamodelTransfer export', () => {
  it('reads format 2 with the Authorization header and saves a Blob download', async () => {
    const calls = stubBackend();
    const blobs: Blob[] = [];
    const createObjectURL = vi.fn((blob: Blob) => {
      blobs.push(blob);
      return 'blob:metamodel';
    });
    // jsdom has no object URLs; the stubs are removed again at the end.
    const original = { create: URL.createObjectURL, revoke: URL.revokeObjectURL };
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = vi.fn();
    onTestFinished(() => {
      URL.createObjectURL = original.create;
      URL.revokeObjectURL = original.revoke;
    });
    const downloads: { href: string; download: string }[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      downloads.push({ href: this.getAttribute('href') ?? '', download: this.download });
    });

    renderTransfer();
    fireEvent.click(screen.getByRole('button', { name: 'Metamodell exportieren' }));
    await waitFor(() => expect(downloads).toHaveLength(1));

    const read = calls.find((c) => c.path === '/ci-types/export')!;
    expect(read.method).toBe('GET');
    expect(read.query.get('format_version')).toBe('2');
    expect(read.headers.get('Authorization')).toBe(`Bearer ${TOKEN}`);
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(blobs[0]!.type).toBe('application/json');
    const content = await new Promise<string>((resolve) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result));
      reader.readAsText(blobs[0]!);
    });
    expect(JSON.parse(content)).toEqual(EXPORT);
    expect(downloads[0]).toEqual({
      href: 'blob:metamodel',
      download: expect.stringMatching(/^metamodel-\d{4}-\d{2}-\d{2}\.json$/),
    });
  });
});

describe('MetamodelTransfer import', () => {
  it('is offered only with citype:manage', async () => {
    const calls = stubBackend({ permissions: ['ci:read'] });
    renderTransfer();
    await waitFor(() => expect(calls.some((c) => c.path === '/me/permissions')).toBe(true));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(
      screen.queryByRole('button', { name: 'Metamodell importieren' }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Metamodell exportieren' })).toBeInTheDocument();
  });

  it('makes the draft of a 201 the active one and opens it with the report', async () => {
    const calls = stubBackend();
    renderTransfer();
    await screen.findByRole('button', { name: 'Metamodell importieren' });
    const text = JSON.stringify(EXPORT);
    await upload(text);

    await waitFor(() =>
      expect(screen.getByTestId('path')).toHaveTextContent(`/ci-types/drafts/${DRAFT_ID}`),
    );
    expect(imports(calls)).toHaveLength(1);
    expect(imports(calls)[0]!.body).toBe(text);
    expect(useMetamodelDraftStore.getState().draft).toEqual({
      id: DRAFT_ID,
      name: 'Import Router',
    });
    const report = screen.getByRole('region', { name: 'Bericht des Imports' });
    expect(report).toHaveTextContent('Neu: ci_type router');
    expect(report).toHaveTextContent('Geändert: attribute_definition ports (ci_type:router)');
    expect(report).not.toHaveTextContent('server');
    expect(report).toHaveTextContent(
      'Historie übersprungen: template router_base — older versions are not imported',
    );
  });

  it('shows a note without a draft for 200 with report.unchanged', async () => {
    stubBackend({
      importReply: () =>
        json({
          draft_id: null,
          report: { unchanged: true, items: [], conflicts: [], notes: [] },
        }),
    });
    renderTransfer();
    await screen.findByRole('button', { name: 'Metamodell importieren' });
    await upload(JSON.stringify(EXPORT));
    expect(
      await screen.findByText(
        'Die Datei weicht nicht vom Metamodell ab; es wurde kein Entwurf angelegt.',
      ),
    ).toBeInTheDocument();
    expect(useMetamodelDraftStore.getState().draft).toBeNull();
    expect(screen.queryByTestId('path')).not.toBeInTheDocument();
  });

  it('shows the conflict report of a 422 with problem detail and trace id', async () => {
    stubBackend({
      importReply: () =>
        new Response(
          JSON.stringify({
            type: 'https://reticora.dev/problems/datatype-conflict',
            status: 422,
            title: 'Unprocessable Entity',
            detail: 'the import conflicts with the metamodel',
            trace_id: 'trace-import',
            report: {
              unchanged: false,
              items: [],
              conflicts: [
                {
                  code: 'datatype-conflict',
                  kind: 'attribute_definition',
                  key: 'ports',
                  owner: 'ci_type:router',
                  detail: 'integer in the file, text in the organization',
                },
                {
                  code: 'missing-reference',
                  kind: 'ci_type',
                  key: 'switch',
                  reference: 'parent_type',
                  reference_key: 'network',
                  detail: 'unknown parent type',
                },
              ],
              notes: [],
            },
          }),
          { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
        ),
    });
    renderTransfer();
    await screen.findByRole('button', { name: 'Metamodell importieren' });
    await upload(JSON.stringify(EXPORT));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Der Import wurde abgelehnt');
    expect(alert).toHaveTextContent('Fehler 422: the import conflicts with the metamodel');
    expect(alert).toHaveTextContent(
      'Datentyp-Konflikt: attribute_definition ports (ci_type:router) — integer in the file, text in the organization',
    );
    expect(alert).toHaveTextContent(
      'Fehlender Verweis: ci_type switch → parent_type network — unknown parent type',
    );
    expect(alert).toHaveTextContent('Trace-ID: trace-import');
    expect(useMetamodelDraftStore.getState().draft).toBeNull();
  });

  it('refuses a file without JSON before sending it', async () => {
    const calls = stubBackend();
    renderTransfer();
    await screen.findByRole('button', { name: 'Metamodell importieren' });
    await upload('not json', 'broken.json');
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Die Datei broken.json enthält kein gültiges JSON.',
    );
    expect(imports(calls)).toHaveLength(0);
  });
});
