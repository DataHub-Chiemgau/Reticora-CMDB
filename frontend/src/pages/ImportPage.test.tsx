/**
 * WP-322 (UI-11, UI-14): the import assistant. A CSV or XLSX file is
 * uploaded as a document (FileUpload of WP-321) and waited for until its
 * scan is clean; infected and scan_failed abort. The preview shows the
 * columns, the MappingEditor maps them and the profile is saved with that
 * mapping (POST /import-profiles); the dry run and the run start import.run
 * jobs whose report shows the counters and a partial failure. Running needs
 * import:run, saving a profile import:manage.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { ImportPage } from './ImportPage';

const AT = '2026-11-01T08:00:00Z';
const MIB = 1024 * 1024;

/** MockXHR answers the content upload (PUT /documents/{id}/content) at once. */
class MockXHR {
  static requests: MockXHR[] = [];
  static status = 200;
  method = '';
  url = '';
  headers: Record<string, string> = {};
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
  send() {
    MockXHR.requests.push(this);
    queueMicrotask(() => {
      this.status = MockXHR.status;
      this.statusText = `status ${MockXHR.status}`;
      this.responseText = JSON.stringify(
        MockXHR.status === 200
          ? documentBody('pending_scan')
          : { status: MockXHR.status, title: 'Refused', detail: 'refused' },
      );
      this.onload?.();
    });
  }
  abort() {
    this.onabort?.();
  }
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

function documentBody(scan: string) {
  return {
    id: 'doc-1',
    organization_id: 'org-1',
    title: 'server',
    file_name: 'server.csv',
    file_size: 64,
    mime_type: 'text/csv',
    storage_key: '',
    version: 1,
    category: 'general',
    tags: [],
    uploaded_by: 'user-1',
    created_at: AT,
    updated_at: AT,
    scan_status: scan,
  };
}

const PREVIEW = {
  format: 'csv',
  sheets: [],
  columns: ['Hostname', 'Seriennummer', 'Typ'],
  sample_rows: [
    ['srv-01', 'SN-1', 'server'],
    ['srv-02', 'SN-2', 'server'],
  ],
};

function profile(extra: Record<string, unknown> = {}) {
  return {
    id: 'prof-1',
    organization_id: 'org-1',
    name: 'Server-Import',
    description: null,
    entity: 'ci',
    format: 'csv',
    mode: 'both',
    sheet: null,
    mapping: { columns: { Hostname: { target: 'name', transforms: [] } }, defaults: {} },
    identity_keys: ['name'],
    created_by: 'user-1',
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

function job(id: string, status: string, result?: Record<string, unknown>, progress = 100) {
  return {
    id,
    kind: 'import.run',
    status,
    progress,
    attempts: 1,
    max_attempts: 3,
    created_at: AT,
    updated_at: AT,
    cancellable: false,
    ...(result ? { result } : {}),
  };
}

function run(id: string, dryRun: boolean, total: number, failed: number) {
  return {
    id,
    kind: 'import.run',
    status: 'succeeded',
    progress: 100,
    source: 'document',
    document_id: 'doc-1',
    document_version: 1,
    row_count: null,
    profile_id: 'prof-1',
    mode: 'both',
    dry_run: dryRun,
    sheet: null,
    rows_total: total,
    rows_failed: failed,
    error_code: null,
    error: null,
    cancellable: false,
    created_at: AT,
    started_at: AT,
    finished_at: AT,
  };
}

interface Options {
  permissions?: string[];
  scan?: string;
  profiles?: Record<string, unknown>[];
  jobs?: Record<string, Record<string, unknown>>;
}

interface Call {
  method: string;
  path: string;
  search: URLSearchParams;
  body: unknown;
}

/** stubApi answers the requests of the assistant and records them. */
function stubApi(options: Options = {}) {
  const calls: Call[] = [];
  const profiles = [...(options.profiles ?? [])];
  let started = 0;
  const jobs: Record<string, Record<string, unknown>> = options.jobs ?? {
    'job-1': job('job-1', 'succeeded', {
      report_key: 'r1',
      dry_run: true,
      total: 3,
      created: 2,
      updated: 0,
      skipped: 0,
      failed: 1,
    }),
    'job-2': job('job-2', 'succeeded', {
      report_key: 'r2',
      dry_run: false,
      total: 3,
      created: 1,
      updated: 1,
      skipped: 0,
      failed: 1,
    }),
  };
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const body = init?.body ? (JSON.parse(String(init.body)) as unknown) : undefined;
      calls.push({ method, path, search: url.searchParams, body });
      const route = `${method} ${path}`;
      if (route === 'GET /me/permissions') {
        return json({
          user_id: 'user-1',
          permissions: options.permissions ?? ['import:run', 'import:manage'],
        });
      }
      if (route === 'POST /documents') return json(documentBody('pending_scan'), 201);
      if (route === 'GET /documents/doc-1') return json(documentBody(options.scan ?? 'clean'));
      if (route === 'POST /imports/preview') return json(PREVIEW);
      if (route === 'GET /import-profiles') {
        return json({
          data: profiles,
          total: profiles.length,
          limit: 200,
          offset: 0,
          has_more: false,
        });
      }
      if (route === 'POST /import-profiles') {
        const created = profile({ ...(body as Record<string, unknown>), id: 'prof-1' });
        profiles.push(created);
        return json(created, 201);
      }
      if (route === 'GET /ci-types') {
        return json({ data: [], total: 0, limit: 200, offset: 0, has_more: false });
      }
      if (route === 'GET /relationship-types') {
        return json({ data: [], total: 0, limit: 100, offset: 0, has_more: false });
      }
      if (route === 'POST /imports') {
        started += 1;
        return json({ job_id: `job-${started}`, status: 'queued', kind: 'import.run' }, 202);
      }
      const jobMatch = /^\/jobs\/(job-\d)$/.exec(path);
      if (method === 'GET' && jobMatch?.[1] && jobs[jobMatch[1]]) return json(jobs[jobMatch[1]]);
      const runMatch = /^\/imports\/(job-\d)$/.exec(path);
      if (method === 'GET' && runMatch?.[1]) {
        const dry = runMatch[1] === 'job-1';
        return json(run(runMatch[1], dry, 3, 1));
      }
      const reportMatch = /^\/imports\/(job-\d)\/report$/.exec(path);
      if (method === 'GET' && reportMatch?.[1]) {
        return json({ download_url: `https://blob.example/${reportMatch[1]}.csv` });
      }
      const errorsMatch = /^\/imports\/(job-\d)\/errors$/.exec(path);
      if (method === 'GET' && errorsMatch?.[1]) {
        // Two pages: the file error of a missing column, then row 3.
        const second = url.searchParams.get('cursor') === 'page-2';
        return json({
          data: second
            ? [{ line: 3, field: 'serial_number', code: 'invalid_value', message: 'is required' }]
            : [{ line: null, field: 'Typ', code: 'column_missing', message: 'Typ is missing' }],
          total: 2,
          limit: 50,
          offset: second ? 1 : 0,
          has_more: !second,
          ...(second ? {} : { next_cursor: 'page-2' }),
        });
      }
      return json({ status: 404, title: 'Not Found', detail: `unexpected ${route}` }, 404);
    }),
  );
  return calls;
}

const csv = (name = 'server.csv', type = 'text/csv') =>
  new File(['Hostname;Seriennummer;Typ\nsrv-01;SN-1;server\n'], name, { type });

function choose(file: File) {
  fireEvent.change(screen.getByLabelText('Importdatei hochladen'), {
    target: { files: [file] },
  });
}

/** toMapping uploads a clean file and opens the mapping step. */
async function toMapping() {
  choose(csv());
  fireEvent.click(await screen.findByRole('button', { name: 'Weiter zur Zuordnung' }));
  await screen.findByRole('region', { name: 'Spaltenvorschau' });
}

beforeEach(() => {
  MockXHR.requests = [];
  MockXHR.status = 200;
  vi.stubGlobal('XMLHttpRequest', MockXHR);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('ImportPage', () => {
  it('runs file, mapping, dry run and run with the report of each run', async () => {
    const calls = stubApi();
    const opened = vi.spyOn(window, 'open').mockImplementation(() => null);
    renderWithProviders(<ImportPage />);

    // Step file: POST /documents, then the content with the type of the file.
    choose(csv());
    fireEvent.click(await screen.findByRole('button', { name: 'Weiter zur Zuordnung' }));
    expect(calls.find((c) => c.method === 'POST' && c.path === '/documents')?.body).toMatchObject({
      file_name: 'server.csv',
      mime_type: 'text/csv',
    });
    expect(MockXHR.requests[0]?.method).toBe('PUT');
    expect(MockXHR.requests[0]?.url).toBe('/api/v1/documents/doc-1/content');
    expect(MockXHR.requests[0]?.headers['Content-Type']).toBe('text/csv');

    // Step mapping: the preview and the mapping of the profile.
    const region = await screen.findByRole('region', { name: 'Spaltenvorschau' });
    expect(within(region).getByText('srv-02')).toBeInTheDocument();
    expect(calls.find((c) => c.path === '/imports/preview')?.body).toEqual({
      document_id: 'doc-1',
    });
    fireEvent.change(screen.getByLabelText('Name des Profils'), {
      target: { value: 'Server-Import' },
    });
    fireEvent.change(screen.getByLabelText('Ziel von Hostname'), { target: { value: 'name' } });
    fireEvent.change(screen.getByLabelText('Ziel von Seriennummer'), {
      target: { value: 'serial_number' },
    });
    fireEvent.change(screen.getByLabelText('Transformation für Hostname hinzufügen'), {
      target: { value: 'trim' },
    });
    fireEvent.change(screen.getByLabelText('Transformation für Seriennummer hinzufügen'), {
      target: { value: 'enum_map' },
    });
    fireEvent.change(screen.getByLabelText('Zuordnung (Werte umsetzen)'), {
      target: { value: 'SN-1=A-1, SN-2=A-2' },
    });
    fireEvent.change(screen.getByLabelText('Standardwert für Seriennummer'), {
      target: { value: 'unbekannt' },
    });
    fireEvent.click(screen.getByRole('checkbox', { name: 'Seriennummer' }));
    fireEvent.click(screen.getByRole('button', { name: 'Profil anlegen' }));

    await screen.findByText('Profil „Server-Import“ gespeichert.');
    expect(calls.find((c) => c.method === 'POST' && c.path === '/import-profiles')?.body).toEqual({
      name: 'Server-Import',
      entity: 'ci',
      format: 'csv',
      mode: 'both',
      sheet: null,
      mapping: {
        columns: {
          Hostname: { target: 'name', transforms: [{ op: 'trim' }] },
          Seriennummer: {
            target: 'serial_number',
            transforms: [{ op: 'enum_map', values: { 'SN-1': 'A-1', 'SN-2': 'A-2' } }],
          },
        },
        defaults: { serial_number: 'unbekannt' },
      },
      identity_keys: ['serial_number'],
    });

    // Step check: the dry run and its report.
    fireEvent.click(screen.getByRole('button', { name: 'Weiter zum Prüfen' }));
    fireEvent.click(screen.getByRole('button', { name: 'Dry Run starten' }));
    const dryReport = await screen.findByRole('region', { name: 'Bericht' });
    expect(calls.find((c) => c.method === 'POST' && c.path === '/imports')?.body).toEqual({
      document_id: 'doc-1',
      profile_id: 'prof-1',
      mode: 'both',
      dry_run: true,
    });
    await within(dryReport).findByText('Teilerfolg: 1 von 3 Zeilen fehlerhaft');
    expect(within(dryReport).getByText('Dry Run')).toBeInTheDocument();
    const counters = within(dryReport)
      .getAllByRole('definition')
      .map((d) => d.textContent);
    expect(counters).toEqual(['3', '2', '0', '0', '1']);
    expect(within(dryReport).getByText(/Fehlerhafte Zeilen: 1/)).toBeInTheDocument();
    await waitFor(() => expect(calls.some((c) => c.path === '/imports/job-1')).toBe(true));

    // Step run: the mode of the profile, the same report and the download.
    fireEvent.click(screen.getByRole('button', { name: 'Weiter zum Ausführen' }));
    expect(screen.getByText(/im Modus „Anlegen und aktualisieren“/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Import ausführen' }));
    const report = await screen.findByRole('region', { name: 'Bericht' });
    await within(report).findByText('Teilerfolg: 1 von 3 Zeilen fehlerhaft');
    expect(within(report).queryByText('Dry Run')).toBeNull();
    expect(
      calls.filter((c) => c.method === 'POST' && c.path === '/imports').map((c) => c.body),
    ).toContainEqual({ document_id: 'doc-1', profile_id: 'prof-1', mode: 'both', dry_run: false });
    expect(
      within(report)
        .getAllByRole('definition')
        .map((d) => d.textContent),
    ).toEqual(['3', '1', '1', '0', '1']);

    fireEvent.click(within(report).getByRole('button', { name: 'Bericht herunterladen' }));
    await waitFor(() =>
      expect(opened).toHaveBeenCalledWith('https://blob.example/job-2.csv', '_blank', 'noopener'),
    );
  });

  it('lists the row errors of the report page by page over next_cursor', async () => {
    const calls = stubApi({ profiles: [profile()] });
    renderWithProviders(<ImportPage />);
    await toMapping();
    fireEvent.change(await screen.findByLabelText('Profil'), { target: { value: 'prof-1' } });
    fireEvent.click(screen.getByRole('button', { name: 'Weiter zum Prüfen' }));
    fireEvent.click(screen.getByRole('button', { name: 'Dry Run starten' }));
    const list = await screen.findByRole('region', { name: 'Fehlerliste' });
    const header = within(list)
      .getAllByRole('columnheader')
      .map((h) => h.textContent);
    expect(header).toEqual(['Zeile', 'Feld', 'Code', 'Meldung']);
    expect(within(list).getByText('Datei')).toBeInTheDocument();
    expect(within(list).getByText('column_missing')).toBeInTheDocument();
    expect(within(list).getByText('1–1 von 2 Fehlern')).toBeInTheDocument();
    const first = calls.find((c) => c.path === '/imports/job-1/errors');
    expect(first?.search.get('limit')).toBe('50');
    expect(first?.search.has('cursor')).toBe(false);
    expect(within(list).getByRole('button', { name: 'Zurück' })).toBeDisabled();

    fireEvent.click(within(list).getByRole('button', { name: 'Weiter' }));
    await within(list).findByText('invalid_value');
    expect(within(list).getByText('3')).toBeInTheDocument();
    expect(within(list).getByText('2–2 von 2 Fehlern')).toBeInTheDocument();
    expect(
      calls.filter((c) => c.path === '/imports/job-1/errors').map((c) => c.search.get('cursor')),
    ).toEqual([null, 'page-2']);
    expect(within(list).getByRole('button', { name: 'Weiter' })).toBeDisabled();

    fireEvent.click(within(list).getByRole('button', { name: 'Zurück' }));
    await within(list).findByText('column_missing');
  });

  it('follows a running job with its progress', async () => {
    stubApi({
      profiles: [profile()],
      jobs: { 'job-1': job('job-1', 'running', undefined, 40) },
    });
    renderWithProviders(<ImportPage />);
    await toMapping();
    fireEvent.change(await screen.findByLabelText('Profil'), { target: { value: 'prof-1' } });
    fireEvent.click(screen.getByRole('button', { name: 'Weiter zum Prüfen' }));
    fireEvent.click(screen.getByRole('button', { name: 'Dry Run starten' }));
    const progress = await screen.findByRole('progressbar', { name: 'Fortschritt des Imports' });
    expect(progress).toHaveAttribute('aria-valuenow', '40');
    expect(screen.getByRole('button', { name: 'Weiter zum Ausführen' })).toBeDisabled();
  });

  it.each([
    ['daten.txt', 1, 'Dieser Dateityp ist nicht erlaubt.'],
    ['server.csv', 51 * MIB, 'Die Datei ist zu groß (höchstens 50,0 MB).'],
  ])('checks %s (%i bytes) before the upload', async (name, size, message) => {
    const calls = stubApi();
    renderWithProviders(<ImportPage />);
    const file = csv(name);
    Object.defineProperty(file, 'size', { value: size });
    choose(file);
    expect(await screen.findByRole('alert')).toHaveTextContent(message);
    expect(calls.some((c) => c.path === '/documents')).toBe(false);
    expect(MockXHR.requests).toHaveLength(0);
  });

  it('shows a refusal of the server (413) at the field', async () => {
    stubApi();
    MockXHR.status = 413;
    renderWithProviders(<ImportPage />);
    choose(csv());
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Die Datei ist zu groß für das Upload-Limit des Servers.',
    );
  });

  it.each([
    ['infected', 'Die Datei ist infiziert und in Quarantäne. Der Import ist abgebrochen.'],
    ['scan_failed', 'Die Prüfung der Datei ist fehlgeschlagen. Der Import ist abgebrochen.'],
  ])('aborts when the scan reports %s', async (scan, message) => {
    const calls = stubApi({ scan });
    renderWithProviders(<ImportPage />);
    choose(csv());
    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Weiter zur Zuordnung' })).toBeNull();
    expect(calls.some((c) => c.path === '/imports/preview')).toBe(false);
    fireEvent.click(screen.getByRole('button', { name: 'Andere Datei hochladen' }));
    expect(screen.queryByText(message)).toBeNull();
  });

  it('waits while the scan is pending', async () => {
    stubApi({ scan: 'pending_scan' });
    renderWithProviders(<ImportPage />);
    choose(csv());
    expect(
      await screen.findByText('Die Datei wird auf Schadsoftware geprüft …'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Weiter zur Zuordnung' })).toBeNull();
  });

  it('saves no profile without import:manage but runs a stored one', async () => {
    const calls = stubApi({ permissions: ['import:run'], profiles: [profile()] });
    renderWithProviders(<ImportPage />);
    await toMapping();
    await screen.findByText('Profile speichern braucht die Berechtigung import:manage.');
    expect(screen.getByRole('button', { name: 'Profil anlegen' })).toBeDisabled();
    expect(screen.getByLabelText('Ziel von Hostname')).toBeDisabled();

    fireEvent.change(screen.getByLabelText('Profil'), { target: { value: 'prof-1' } });
    expect(screen.getByRole('button', { name: 'Profil speichern' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Weiter zum Prüfen' }));
    fireEvent.click(screen.getByRole('button', { name: 'Dry Run starten' }));
    await screen.findByRole('region', { name: 'Bericht' });
    expect(calls.some((c) => c.method === 'POST' && c.path === '/import-profiles')).toBe(false);
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('runs nothing without import:run', async () => {
    const calls = stubApi({ permissions: ['import:manage'], profiles: [profile()] });
    renderWithProviders(<ImportPage />);
    await toMapping();
    fireEvent.change(screen.getByLabelText('Profil'), { target: { value: 'prof-1' } });
    fireEvent.click(screen.getByRole('button', { name: 'Weiter zum Prüfen' }));
    expect(screen.getByRole('button', { name: 'Dry Run starten' })).toBeDisabled();
    expect(
      screen.getByText('Prüfen und Ausführen brauchen die Berechtigung import:run.'),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === 'POST' && c.path === '/imports')).toBe(false);
  });

  it('describes the API import', () => {
    stubApi();
    renderWithProviders(<ImportPage />);
    expect(
      screen.getByText(/POST \/api\/v1\/imports mit rows \(höchstens 5\.000 Zeilen/),
    ).toBeInTheDocument();
  });
});
