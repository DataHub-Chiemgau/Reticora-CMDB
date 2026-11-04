/**
 * WP-142, WP-511, WP-313 (DB-04, MET-15: searchable/unique flags of attribute definitions)
 *
 * Since WP-313 the attribute editor of the CI type admin creates a field
 * with its index flags in one POST /attribute-definitions (no PUT
 * /ci-types/{id}/fields, no second PATCH, no partial success):
 *   (+) searchable → POST {searchable:true, unique:false, unique_scope:'org'}
 *   (+) unique with scope client → POST {unique:true, unique_scope:'client'}
 *   (edge) no flags → POST with both flags off, no PATCH
 * A refused save (409 while CIs share a value, F-159) keeps the dialog open
 * with the reason in the UI language and at the field. Existing attributes
 * offer "Bearbeiten" with their flags prefilled (F-325); saving sends PATCH
 * and the attribute list shows the index status of the definition (WP-796).
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

const QUEUED = { state: 'queued', index_name: 'idx', valid: false, attempts: 0, max_attempts: 3 };

/** The existing definition ad-1 of inventory_no (searchable, not unique). */
const EXISTING = {
  id: 'ad-1',
  key: 'inventory_no',
  ci_type_id: 't1',
  scope: 'type',
  display_name: { de: 'inventory_no', en: 'inventory_no' },
  data_type: 'text',
  required: false,
  multi_value: false,
  searchable: true,
  unique: false,
  unique_scope: 'org',
  inheritable: true,
  order: 0,
  is_system: false,
  applies_to: 'all',
  applies_to_categories: [],
  applies_to_type_ids: [],
  index_status: { ...QUEUED, state: 'succeeded', valid: true },
};

/** The CI type t1 of the organization. */
const TYPE = {
  id: 't1',
  key: 'server',
  name: 'Server',
  display_name: { de: 'Server', en: 'Server' },
  is_active: true,
  is_system: false,
  schema_version: 1,
  version: 1,
};

interface Backend {
  /** Answer of POST /attribute-definitions; default: the created definition. */
  post?: (body: unknown) => Response;
  /** Answer of PATCH /attribute-definitions/ad-1; default: the patched definition. */
  patch?: (body: unknown) => Response;
  /** Whether the type already has the attribute inventory_no. */
  existing?: boolean;
}

function stubBackend({ post, patch, existing = false }: Backend = {}): Call[] {
  const calls: Call[] = [];
  // The stored definition: a saved field and its accepted flags show in later reads.
  let stored: Record<string, unknown> | null = existing ? { ...EXISTING } : null;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method ?? 'GET').toUpperCase();
      const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, url, body });
      if (method === 'POST' && /\/attribute-definitions$/.test(url)) {
        if (post) return post(body);
        stored = { ...EXISTING, ...(body as object), index_status: QUEUED };
        return json(stored, 201);
      }
      if (method === 'PATCH' && url.includes('/attribute-definitions/ad-1')) {
        if (patch) return patch(body);
        stored = { ...stored, ...(body as object), index_status: QUEUED };
        return json(stored);
      }
      if (url.includes('/attribute-definitions?') && url.includes('scope=global')) {
        return json({ data: [] });
      }
      if (url.includes('/attribute-definitions?')) return json({ data: stored ? [stored] : [] });
      if (url.includes('/field-permissions/'))
        return json({ data: [], limit: 200, has_more: false });
      if (url.includes('/me/permissions')) {
        return json({ user_id: 'u1', permissions: ['ci_attribute:manage'] });
      }
      if (url.includes('/ci-types')) {
        return json({ data: [TYPE], total: 1, limit: 200, offset: 0, has_more: false });
      }
      return json({ detail: `unexpected ${method} ${url}` }, 404);
    }),
  );
  return calls;
}

async function selectType() {
  renderWithProviders(<CITypeAdminPage />, { route: '/admin/ci-types' });
  fireEvent.click(await screen.findByRole('treeitem', { name: 'Server' }));
}

async function openFieldEditor() {
  await selectType();
  fireEvent.click(await screen.findByRole('button', { name: 'Feld hinzufügen' }));
  fireEvent.change(screen.getByLabelText('Feldname'), { target: { value: 'inventory_no' } });
}

async function openFlagEditor() {
  await selectType();
  fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten: inventory_no' }));
  return screen.getByRole('dialog');
}

const posts = (calls: Call[]) =>
  calls.filter((c) => c.method === 'POST' && /\/attribute-definitions$/.test(c.url));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CITypeAdminPage index flags', () => {
  it('sends searchable with the new field in one POST', async () => {
    const calls = stubBackend();
    await openFieldEditor();
    fireEvent.click(screen.getByLabelText('Durchsuchbar (indiziert)'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(posts(calls)).toHaveLength(1));
    expect(posts(calls)[0]?.body).toMatchObject({
      key: 'inventory_no',
      ci_type_id: 't1',
      searchable: true,
      unique: false,
      unique_scope: 'org',
    });
    expect(calls.some((c) => c.method === 'PUT' || c.method === 'PATCH')).toBe(false);
  });

  it('sends unique with the client scope', async () => {
    const calls = stubBackend();
    await openFieldEditor();
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    fireEvent.change(await screen.findByLabelText('Eindeutig je'), { target: { value: 'client' } });
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(posts(calls)).toHaveLength(1));
    expect(posts(calls)[0]?.body).toMatchObject({
      searchable: false,
      unique: true,
      unique_scope: 'client',
    });
  });

  // Real-stack F1 (e2e-real/metamodel-flags.spec.ts): the attribute list shows the new
  // field with its flag and its index status.
  it('shows a field saved with a unique flag in the attribute list of the type', async () => {
    stubBackend();
    await openFieldEditor();
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const row = (await screen.findByText('inventory_no')).closest('li');
    expect(row).not.toBeNull();
    expect(await within(row!).findByText('eindeutig')).toBeInTheDocument();
    expect(within(row!).getByRole('status')).toHaveTextContent('Index: Wartend');
    expect(
      within(row!).getByRole('button', { name: 'Bearbeiten: inventory_no' }),
    ).toBeInTheDocument();
  });

  it('sends both flags off for a field without flags and no PATCH', async () => {
    const calls = stubBackend();
    await openFieldEditor();
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(posts(calls)[0]?.body).toMatchObject({ searchable: false, unique: false });
    expect(calls.some((c) => c.method === 'PATCH' || c.method === 'PUT')).toBe(false);
  });

  // Moved from CITypeAdminPage.auditdefect.tsx (F-159, WP-511).
  it('shows the 409 reason of a refused unique flag and keeps the dialog open', async () => {
    const calls = stubBackend({ post: () => problem(409, { detail: DETAIL }) });
    await openFieldEditor();
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(posts(calls)).toHaveLength(1));
    const alert = await within(screen.getByRole('dialog')).findByRole('alert');
    expect(alert).toHaveTextContent(DETAIL);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('shows the 409 on unique with its reason in the dialog and at the checkbox', async () => {
    stubBackend({
      post: () =>
        problem(409, {
          detail:
            'unique: CIs already share a value of this attribute; resolve the duplicates first',
          field: 'unique',
        }),
    });
    await openFieldEditor();
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    const dialog = screen.getByRole('dialog');
    const alert = await within(dialog).findByRole('alert');
    expect(alert).toHaveTextContent('Feld nicht gespeichert.');
    expect(alert).toHaveTextContent('Eindeutigkeit nicht aktiviert');
    expect(alert).toHaveTextContent(
      'Grund: CIs already share a value of this attribute; resolve the duplicates first',
    );
    expect(alert).not.toHaveTextContent('unique: CIs');
    expect(within(dialog).getByLabelText('Eindeutig')).toHaveAccessibleDescription(
      'CIs already share a value of this attribute; resolve the duplicates first',
    );
    expect(within(dialog).getByLabelText('Eindeutig')).toHaveAttribute('aria-invalid', 'true');
  });

  it('shows a refused field save as not saved', async () => {
    const calls = stubBackend({
      post: () => problem(400, { title: 'Bad Request', detail: 'invalid field name' }),
    });
    await openFieldEditor();
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    const alert = await within(screen.getByRole('dialog')).findByRole('alert');
    expect(alert).toHaveTextContent('Feld nicht gespeichert. Grund: invalid field name');
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('edits the flags of an existing attribute: prefilled, PATCH, index status', async () => {
    const calls = stubBackend({ existing: true });
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
    expect(calls.some((c) => c.method === 'PUT' || c.method === 'POST')).toBe(false);

    // The index status of the definition comes with the list (WP-796), no /jobs read.
    const row = screen.getByText('inventory_no').closest('li')!;
    expect(await within(row).findByText('Index: Wartend')).toBeInTheDocument();
    expect(calls.some((c) => c.url.includes('/jobs'))).toBe(false);
  });

  it('prefills a unique attribute with its scope', async () => {
    EXISTING.unique = true;
    EXISTING.unique_scope = 'client';
    try {
      stubBackend({ existing: true });
      await selectType();
      expect(await screen.findByText('eindeutig je Kunde')).toBeInTheDocument();
      expect(screen.getByText('durchsuchbar')).toBeInTheDocument();
      fireEvent.click(screen.getByRole('button', { name: 'Bearbeiten: inventory_no' }));
      const dialog = screen.getByRole('dialog');
      expect(within(dialog).getByLabelText('Eindeutig')).toBeChecked();
      expect(within(dialog).getByLabelText('Eindeutig je')).toHaveValue('client');
    } finally {
      EXISTING.unique = false;
      EXISTING.unique_scope = 'org';
    }
  });

  it('keeps the attribute editor open with the reason when the server refuses the flags', async () => {
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
    expect(alert).toHaveTextContent('Änderungen nicht gespeichert.');
    expect(alert).toHaveTextContent('Eindeutigkeit nicht aktiviert');
    expect(alert).toHaveTextContent('resolve the duplicates first');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(1);
  });
});
