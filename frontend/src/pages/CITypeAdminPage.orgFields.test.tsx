/**
 * Organization-wide fields (MET-13) on the CI type admin: the global fields
 * of GET /ci-fields/global apply to every CI type of the organization, the
 * system types and the CIs discovery finds included, which a clone of a
 * system type does not reach.
 *   (+) the section lists the organization's fields and the read-only system
 *       fields (is_system), without a selected type
 *   (+) adding writes PUT /ci-fields/global (never a type) and then the
 *       index flags through PATCH /attribute-definitions/{id}
 *   (+) the flags of an existing organization-wide field are edited through
 *       PATCH; deleting asks first and sends DELETE /ci-fields/global/{name}
 *   (-) system fields offer neither editing nor deleting
 *   (-) without ci_attribute:manage (fail closed while /me/permissions loads)
 *       the section is read-only and says why
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/CITypeAdminPage.orgFields.test.tsx
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

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, body: Record<string, unknown>) {
  return new Response(JSON.stringify({ status, ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

const SYSTEM_TYPE = {
  id: 't-sys',
  key: 'server',
  name: 'Server',
  display_name: { de: 'Server', en: 'Server' },
  is_active: true,
  is_system: true,
};

/** The organization's field cost_center and a read-only system field asset_tag. */
const ORG_FIELD = { id: 'gd-1', scope: 'global', name: 'cost_center', data_type: 'text' };
const SYSTEM_FIELD = {
  id: 'gd-sys',
  scope: 'global',
  name: 'asset_tag',
  data_type: 'text',
  is_system: true,
};
const ORG_DEFINITION = {
  id: 'gd-1',
  key: 'cost_center',
  scope: 'global',
  data_type: 'text',
  searchable: false,
  unique: false,
  unique_scope: 'org',
  is_system: false,
};
const SYSTEM_DEFINITION = { ...ORG_DEFINITION, id: 'gd-sys', key: 'asset_tag', is_system: true };

interface Backend {
  /** The effective permissions of /me/permissions. */
  permissions?: string[];
  /** Answer of DELETE /ci-fields/global/{name}; default: 204. */
  remove?: () => Response;
}

function stubBackend({
  permissions = ['ci:read', 'citype:manage', 'ci_attribute:manage'],
  remove,
}: Backend = {}): Call[] {
  const calls: Call[] = [];
  // The stored global fields: a saved field shows in later reads.
  let globals: Record<string, unknown>[] = [ORG_FIELD, SYSTEM_FIELD];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method ?? 'GET').toUpperCase();
      const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, url, body });
      if (url.includes('/me/permissions')) return json({ user_id: 'u1', permissions });
      if (method === 'PUT' && url.endsWith('/ci-fields/global')) {
        const saved = { id: 'gd-2', scope: 'global', ...(body as object) };
        globals = [...globals, saved];
        return json(saved);
      }
      if (method === 'DELETE' && url.includes('/ci-fields/global/')) {
        if (remove) return remove();
        const name = decodeURIComponent(url.slice(url.lastIndexOf('/') + 1));
        globals = globals.filter((f) => f.name !== name || f.is_system);
        return new Response(null, { status: 204 });
      }
      if (url.includes('/ci-fields/global')) return json({ data: globals });
      if (method === 'PATCH' && url.includes('/attribute-definitions/')) {
        const id = url.slice(url.lastIndexOf('/') + 1);
        return json({ ...ORG_DEFINITION, id, ...(body as object) });
      }
      if (url.includes('/attribute-definitions')) {
        return json({
          data: url.includes('scope=global') ? [ORG_DEFINITION, SYSTEM_DEFINITION] : [],
        });
      }
      if (url.includes('/jobs')) {
        return json({ data: [], total: 0, limit: 100, offset: 0, has_more: false });
      }
      if (method === 'GET' && /\/ci-types\/t-sys(\?|$)/.test(url)) {
        return json({ ...SYSTEM_TYPE, fields: [] });
      }
      if (url.includes('/ci-types')) {
        return json({ data: [SYSTEM_TYPE], total: 1, limit: 50, offset: 0, has_more: false });
      }
      return json({ detail: `unexpected ${method} ${url}` }, 404);
    }),
  );
  return calls;
}

/** section renders the page and returns the organization-wide fields once listed. */
async function section() {
  renderWithProviders(<CITypeAdminPage />, { route: '/admin/ci-types' });
  const heading = await screen.findByRole('heading', { name: 'Organisationsweite Felder' });
  const card = heading.closest('section');
  expect(card).not.toBeNull();
  await within(card!).findByText('cost_center');
  return card!;
}

const rowOf = (card: HTMLElement, name: string) => within(card).getByText(name).closest('li')!;

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CITypeAdminPage organization-wide fields', () => {
  it('lists the organization fields and the read-only system fields without a selected type', async () => {
    stubBackend();
    const card = await section();

    expect(card).toHaveTextContent('Diese Felder gelten für alle CI-Typen der Organisation');
    const own = rowOf(card, 'cost_center');
    expect(
      await within(own).findByRole('button', {
        name: 'Organisationsweites Feld bearbeiten: cost_center',
      }),
    ).toBeInTheDocument();
    expect(
      within(own).getByRole('button', { name: 'Organisationsweites Feld löschen: cost_center' }),
    ).toBeInTheDocument();

    const system = rowOf(card, 'asset_tag');
    expect(within(system).getByText('System')).toBeInTheDocument();
    expect(within(system).queryByRole('button')).not.toBeInTheDocument();
  });

  it('adds an organization-wide field through PUT /ci-fields/global with its index flags', async () => {
    const calls = stubBackend();
    const card = await section();
    fireEvent.click(
      await within(card).findByRole('button', { name: 'Organisationsweites Feld hinzufügen' }),
    );
    const dialog = screen.getByRole('dialog');
    expect(
      within(dialog).getByRole('heading', { name: 'Organisationsweites Feld hinzufügen' }),
    ).toBeInTheDocument();
    expect(dialog).toHaveTextContent('Das Feld gilt für alle CI-Typen der Organisation.');
    fireEvent.change(within(dialog).getByLabelText('Feldname'), {
      target: { value: 'contract_owner' },
    });
    fireEvent.click(within(dialog).getByLabelText('Durchsuchbar (indiziert)'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const put = calls.findIndex((c) => c.method === 'PUT');
    expect(calls[put]?.url).toMatch(/\/ci-fields\/global$/);
    expect(calls[put]?.body).toEqual({
      name: 'contract_owner',
      data_type: 'text',
      ui_group: 'general',
    });
    const patch = calls.findIndex((c) => c.method === 'PATCH');
    expect(patch).toBeGreaterThan(put);
    expect(calls[patch]?.url).toContain('/attribute-definitions/gd-2');
    expect(calls[patch]?.body).toEqual({ searchable: true, unique: false, unique_scope: 'org' });
    expect(calls.some((c) => c.method === 'PUT' && c.url.includes('/ci-types/'))).toBe(false);
    // The list reloads with the new field.
    expect(await within(card).findByText('contract_owner')).toBeInTheDocument();
  });

  it('edits the index flags of an organization-wide field', async () => {
    const calls = stubBackend();
    const card = await section();
    fireEvent.click(
      await within(card).findByRole('button', {
        name: 'Organisationsweites Feld bearbeiten: cost_center',
      }),
    );
    const dialog = screen.getByRole('dialog');
    expect(
      within(dialog).getByRole('heading', { name: 'Feld „cost_center“ bearbeiten' }),
    ).toBeInTheDocument();
    fireEvent.click(within(dialog).getByLabelText('Eindeutig'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const patches = calls.filter((c) => c.method === 'PATCH');
    expect(patches).toHaveLength(1);
    expect(patches[0]?.url).toContain('/attribute-definitions/gd-1');
    expect(patches[0]?.body).toEqual({ searchable: false, unique: true, unique_scope: 'org' });
    expect(calls.some((c) => c.method === 'PUT')).toBe(false);
  });

  it('deletes an organization-wide field after the confirmation only', async () => {
    const calls = stubBackend();
    const confirm = vi
      .spyOn(window, 'confirm')
      .mockReturnValueOnce(false)
      .mockReturnValueOnce(true);
    const card = await section();
    const button = await within(rowOf(card, 'cost_center')).findByRole('button', {
      name: 'Organisationsweites Feld löschen: cost_center',
    });

    fireEvent.click(button);
    expect(confirm).toHaveBeenCalledWith(
      'Organisationsweites Feld „cost_center“ löschen? Es entfällt danach bei allen CI-Typen der Organisation.',
    );
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);

    fireEvent.click(button);
    await waitFor(() => expect(within(card).queryByText('cost_center')).not.toBeInTheDocument());
    const deletes = calls.filter((c) => c.method === 'DELETE');
    expect(deletes).toHaveLength(1);
    expect(deletes[0]?.url).toMatch(/\/ci-fields\/global\/cost_center$/);
    expect(within(card).getByText('asset_tag')).toBeInTheDocument();
  });

  it('reports a refused delete in the section', async () => {
    stubBackend({
      remove: () =>
        problem(403, {
          title: 'Forbidden',
          detail: 'missing required permission: ci_attribute:manage',
        }),
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const card = await section();
    fireEvent.click(
      await within(card).findByRole('button', {
        name: 'Organisationsweites Feld löschen: cost_center',
      }),
    );

    expect(await within(card).findByRole('alert')).toHaveTextContent(
      'Feld „cost_center“ nicht gelöscht. Keine Berechtigung: Ihrer Sitzung fehlt das Recht „ci_attribute:manage“.',
    );
  });

  it('is read-only without ci_attribute:manage', async () => {
    const calls = stubBackend({ permissions: ['ci:read', 'citype:manage'] });
    const card = await section();

    expect(
      await within(card).findByText(
        'Nur lesbar: Zum Ändern brauchen Sie das Recht „ci_attribute:manage“.',
      ),
    ).toBeInTheDocument();
    expect(within(card).queryByRole('button')).not.toBeInTheDocument();
    expect(calls.some((c) => c.url.includes('/me/permissions'))).toBe(true);
  });
});
