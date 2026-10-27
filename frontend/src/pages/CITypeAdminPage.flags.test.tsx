/**
 * WP-142, WP-511 (DB-04, MET-15: searchable/unique flags of attribute definitions)
 *
 * The field editor of the CI type admin saves the field (PUT /ci-types/{id}/fields)
 * and then sends the index flags to PATCH /attribute-definitions/{id}:
 *   (+) searchable → PATCH {searchable:true, unique:false, unique_scope:'org'}
 *   (+) unique with scope client → PATCH {unique:true, unique_scope:'client'}
 *   (edge) no flags → no PATCH
 * A refused flag update (409 while CIs share a value, F-159) keeps the dialog
 * open with the reason in the UI language, tells the saved field from the
 * refused flags and reloads the field list. Existing attributes offer
 * "Bearbeiten" with their flags prefilled (F-325); saving sends PATCH and the
 * field list shows the queued index job with its status.
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/CITypeAdminPage.flags.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { CITypeAdminPage } from './CITypeAdminPage';
import { renderWithProviders } from '../test/utils';

interface Call {
  method: string;
  url: string;
  body: unknown;
}

const DETAIL = 'CIs already share a value of inventory_no';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, body: Record<string, unknown>) {
  return new Response(JSON.stringify({ title: 'Conflict', status, ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

/** The existing field inventory_no and its definition ad-1 (searchable, not unique). */
const EXISTING = {
  field: { name: 'inventory_no', data_type: 'text' },
  definition: {
    id: 'ad-1',
    key: 'inventory_no',
    ci_type_id: 't1',
    scope: 'type',
    data_type: 'text',
    searchable: true,
    unique: false,
    unique_scope: 'org',
    is_system: false,
  },
};

/** The CI type t1 as GET /ci-types lists it: without its fields. */
const TYPE = { id: 't1', key: 'server', name: 'Server', is_active: true, is_system: false };

interface Backend {
  /** Answer of PATCH /attribute-definitions/ad-1; default: the patched definition. */
  patch?: (body: unknown) => Response;
  /** Whether the type already has the field inventory_no. */
  existing?: boolean;
  /** Status of the index job of ad-1 the job list reports. */
  jobStatus?: string;
}

function stubBackend({ patch, existing = false, jobStatus = 'queued' }: Backend = {}): Call[] {
  const calls: Call[] = [];
  // The stored state: a saved field and its accepted flags show in later reads.
  let stored = existing;
  let accepted: object = {};
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method ?? 'GET').toUpperCase();
      const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, url, body });
      if (method === 'PUT' && url.includes('/ci-types/t1/fields')) {
        stored = true;
        return json({ id: 'ad-1', name: 'inventory_no', data_type: 'text' });
      }
      if (method === 'PATCH' && url.includes('/attribute-definitions/ad-1')) {
        if (patch) return patch(body);
        accepted = body as object;
        return json({ ...EXISTING.definition, ...accepted, updated_at: 'now' });
      }
      if (url.includes('/attribute-definitions')) {
        return json({ data: stored ? [{ ...EXISTING.definition, ...accepted }] : [] });
      }
      if (url.includes('/jobs')) {
        return json({
          data: [
            {
              id: 'job-2',
              kind: 'attribute.index',
              status: 'succeeded',
              params: { definition_id: 'ad-other' },
            },
            {
              id: 'job-1',
              kind: 'attribute.index',
              status: jobStatus,
              params: { definition_id: 'AD-1' },
            },
          ],
          total: 2,
          limit: 100,
          offset: 0,
          has_more: false,
        });
      }
      // As on the real stack: GET /ci-types/{id} carries the fields, the list does not
      // (citype.PGRepository.List leaves Type.Fields empty, WP-511 real-stack F1/F2).
      if (method === 'GET' && /\/ci-types\/t1(\?|$)/.test(url)) {
        return json({ ...TYPE, fields: stored ? [EXISTING.field] : [] });
      }
      if (url.includes('/ci-types')) {
        return json({
          data: [TYPE],
          total: 1,
          limit: 50,
          offset: 0,
          has_more: false,
        });
      }
      return json({ detail: `unexpected ${method} ${url}` }, 404);
    }),
  );
  return calls;
}

async function openFieldEditor() {
  renderWithProviders(<CITypeAdminPage />, { route: '/admin/ci-types' });
  fireEvent.click(await screen.findByRole('button', { name: /Server/ }));
  fireEvent.click(await screen.findByRole('button', { name: 'Feld hinzufügen' }));
  fireEvent.change(screen.getByLabelText('Feldname'), { target: { value: 'inventory_no' } });
}

async function openFlagEditor() {
  renderWithProviders(<CITypeAdminPage />, { route: '/admin/ci-types' });
  fireEvent.click(await screen.findByRole('button', { name: /Server/ }));
  fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten: inventory_no' }));
  return screen.getByRole('dialog');
}

const typeLoads = (calls: Call[]) =>
  calls.filter((c) => c.method === 'GET' && c.url.includes('/ci-types')).length;

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CITypeAdminPage index flags', () => {
  it('sends searchable after saving the field', async () => {
    const calls = stubBackend();
    await openFieldEditor();
    fireEvent.click(screen.getByLabelText('Durchsuchbar (indiziert)'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const put = calls.findIndex((c) => c.method === 'PUT' && c.url.includes('/ci-types/t1/fields'));
    const patch = calls.findIndex((c) => c.method === 'PATCH');
    expect(put).toBeGreaterThanOrEqual(0);
    expect(patch).toBeGreaterThan(put);
    expect(calls[patch]?.url).toContain('/attribute-definitions/ad-1');
    expect(calls[patch]?.body).toEqual({ searchable: true, unique: false, unique_scope: 'org' });
  });

  it('sends unique with the client scope', async () => {
    const calls = stubBackend();
    await openFieldEditor();
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    fireEvent.change(await screen.findByLabelText('Eindeutig je'), { target: { value: 'client' } });
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH');
    expect(patch?.body).toEqual({ searchable: false, unique: true, unique_scope: 'client' });
  });

  // Real-stack F1 (e2e-real/metamodel-flags.spec.ts): the list of the type has no fields;
  // the field list follows GET /ci-types/{id} and shows the new field with its flag.
  it('shows a field saved with a unique flag in the field list of the type', async () => {
    const calls = stubBackend();
    await openFieldEditor();
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const row = (await screen.findByText('inventory_no')).closest('li');
    expect(row).not.toBeNull();
    expect(await within(row!).findByText('eindeutig')).toBeInTheDocument();
    expect(
      within(row!).getByRole('button', { name: 'Bearbeiten: inventory_no' }),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === 'GET' && /\/ci-types\/t1$/.test(c.url))).toBe(true);
  });

  it('sends no flag update for a field without flags', async () => {
    const calls = stubBackend();
    await openFieldEditor();
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() =>
      expect(calls.some((c) => c.method === 'PUT' && c.url.includes('/ci-types/t1/fields'))).toBe(
        true,
      ),
    );
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  // Moved from CITypeAdminPage.auditdefect.tsx (F-159, WP-511).
  it('shows the 409 reason of a refused unique flag and keeps the dialog open', async () => {
    const calls = stubBackend({ patch: () => problem(409, { detail: DETAIL }) });

    renderWithProviders(<CITypeAdminPage />, { route: '/admin/ci-types' });
    fireEvent.click(await screen.findByRole('button', { name: /Server/ }));
    fireEvent.click(await screen.findByRole('button', { name: 'Feld hinzufügen' }));
    fireEvent.change(screen.getByLabelText('Feldname'), { target: { value: 'inventory_no' } });
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method.startsWith('PATCH'))).toBe(true));
    await waitFor(() => expect(screen.getByText(new RegExp(DETAIL))).toBeInTheDocument(), {
      timeout: 2000,
    });
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('reports the saved field with the refused flag as a partial success and reloads the field list', async () => {
    const calls = stubBackend({
      patch: () =>
        problem(409, {
          detail:
            'unique: CIs already share a value of this attribute; resolve the duplicates first',
          field: 'unique',
        }),
    });
    await openFieldEditor();
    await waitFor(() => expect(typeLoads(calls)).toBeGreaterThan(0));
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    const loadsBefore = typeLoads(calls);
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    const alert = await within(screen.getByRole('dialog')).findByRole('alert');
    expect(alert).toHaveTextContent('Das Feld „inventory_no“ wurde gespeichert');
    expect(alert).toHaveTextContent('die Index-Einstellungen aber nicht');
    expect(alert).toHaveTextContent('Eindeutigkeit nicht aktiviert');
    expect(alert).toHaveTextContent(
      'Grund: CIs already share a value of this attribute; resolve the duplicates first',
    );
    expect(alert).not.toHaveTextContent('unique: CIs');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    // The field is stored: the list is invalidated although the mutation failed.
    await waitFor(() => expect(typeLoads(calls)).toBeGreaterThan(loadsBefore));
  });

  it('shows a refused field save as not saved', async () => {
    stubBackend();
    vi.mocked(fetch).mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      if ((init?.method ?? 'GET').toUpperCase() === 'PUT') {
        return problem(400, { title: 'Bad Request', detail: 'invalid field name' });
      }
      if (url.includes('/attribute-definitions')) return json({ data: [] });
      if (/\/ci-types\/t1(\?|$)/.test(url)) return json({ ...TYPE, fields: [] });
      return json({
        data: [TYPE],
        total: 1,
        limit: 50,
        offset: 0,
        has_more: false,
      });
    });
    await openFieldEditor();
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    const alert = await within(screen.getByRole('dialog')).findByRole('alert');
    expect(alert).toHaveTextContent('Feld nicht gespeichert. Grund: invalid field name');
    expect(vi.mocked(fetch).mock.calls.some(([, init]) => init?.method === 'PATCH')).toBe(false);
  });

  it('edits the flags of an existing attribute: prefilled, PATCH, index job with status', async () => {
    const calls = stubBackend({ existing: true, jobStatus: 'running' });
    const dialog = await openFlagEditor();

    expect(
      within(dialog).getByRole('heading', { name: 'Feld „inventory_no“ bearbeiten' }),
    ).toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Feldname')).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText('Durchsuchbar (indiziert)')).toBeChecked();
    expect(within(dialog).getByLabelText('Eindeutig')).not.toBeChecked();
    fireEvent.click(within(dialog).getByLabelText('Eindeutig'));
    expect(within(dialog).getByLabelText('Eindeutig je')).toHaveValue('org');
    fireEvent.change(within(dialog).getByLabelText('Eindeutig je'), {
      target: { value: 'client' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const patches = calls.filter((c) => c.method === 'PATCH');
    expect(patches).toHaveLength(1);
    expect(patches[0]?.url).toContain('/attribute-definitions/ad-1');
    expect(patches[0]?.body).toEqual({ searchable: true, unique: true, unique_scope: 'client' });
    // The field is not written again: only the flags change.
    expect(calls.some((c) => c.method === 'PUT')).toBe(false);

    // The queued index job of ad-1 (not the one of another definition) with its status.
    expect(await screen.findByRole('status')).toHaveTextContent('Index-Job: läuft');
    expect(calls.some((c) => c.url.includes('/jobs?kind=attribute.index'))).toBe(true);
  });

  it('prefills a unique attribute with its scope', async () => {
    stubBackend({ existing: true });
    EXISTING.definition.unique = true;
    EXISTING.definition.unique_scope = 'client';
    try {
      renderWithProviders(<CITypeAdminPage />, { route: '/admin/ci-types' });
      fireEvent.click(await screen.findByRole('button', { name: /Server/ }));
      expect(await screen.findByText('eindeutig je Kunde')).toBeInTheDocument();
      expect(screen.getByText('durchsuchbar')).toBeInTheDocument();
      fireEvent.click(screen.getByRole('button', { name: 'Bearbeiten: inventory_no' }));
      const dialog = screen.getByRole('dialog');
      expect(within(dialog).getByLabelText('Eindeutig')).toBeChecked();
      expect(within(dialog).getByLabelText('Eindeutig je')).toHaveValue('client');
    } finally {
      EXISTING.definition.unique = false;
      EXISTING.definition.unique_scope = 'org';
    }
  });

  it('keeps the flag editor open with the reason when the server refuses the flags', async () => {
    const calls = stubBackend({
      existing: true,
      patch: () =>
        problem(409, {
          detail:
            'unique: CIs already share a value of this attribute; resolve the duplicates first',
          field: 'unique',
        }),
    });
    const dialog = await openFlagEditor();
    fireEvent.click(within(dialog).getByLabelText('Eindeutig'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    const alert = await within(dialog).findByRole('alert');
    expect(alert).toHaveTextContent('Index-Einstellungen nicht gespeichert.');
    expect(alert).toHaveTextContent('Eindeutigkeit nicht aktiviert');
    expect(alert).toHaveTextContent('resolve the duplicates first');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(1);
  });
});
