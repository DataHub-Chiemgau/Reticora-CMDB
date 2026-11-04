/**
 * Global attributes (MET-13) on the CI type admin. Since WP-313 the section
 * 'Globale Attribute' replaces 'Organisationsweite Felder' and works on
 * /api/v1/attribute-definitions (scope global) instead of /ci-fields/global:
 * the organization's global attributes apply to the CI types of their
 * applicability (applies_to, WP-255), the system types and the CIs
 * discovery finds included, which a clone of a system type does not reach.
 *   (+) the section lists the organization's attributes and the read-only
 *       system attributes (is_system), without a selected type
 *   (+) adding writes one POST /attribute-definitions without ci_type_id
 *       (never a type, no PUT /ci-fields/global) with the index flags
 *   (-) the editor offers no 'Pflichtfeld' and the POST sends required:false
 *   (-) its add button does not contain 'Feld hinzufügen', so the type's
 *       button stays unique for a name-substring lookup (e2e-real
 *       metamodel-flags.spec.ts uses getByRole without exact)
 *   (+) the flags of an existing global attribute are edited through
 *       PATCH; deleting asks first and sends DELETE /attribute-definitions/{id}
 *   (-) system attributes offer neither editing nor deleting
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
/** A type of the organization: it offers 'Feld hinzufügen'. */
const ORG_TYPE = {
  id: 't-org',
  key: 'flagtyp',
  name: 'Flagtyp',
  display_name: { de: 'Flagtyp', en: 'Flagtyp' },
  is_active: true,
  is_system: false,
  organization_id: 'org-1',
};

/** The organization's global attribute cost_center and a read-only system attribute asset_tag. */
const ORG_DEFINITION = {
  id: 'gd-1',
  key: 'cost_center',
  scope: 'global',
  display_name: { de: 'cost_center', en: 'cost_center' },
  data_type: 'text',
  required: false,
  multi_value: false,
  searchable: false,
  unique: false,
  unique_scope: 'org',
  inheritable: true,
  order: 0,
  is_system: false,
  applies_to: 'all',
  applies_to_categories: [],
  applies_to_type_ids: [],
  index_status: { state: 'none', index_name: '', valid: false, attempts: 0, max_attempts: 3 },
};
const SYSTEM_DEFINITION = {
  ...ORG_DEFINITION,
  id: 'gd-sys',
  key: 'asset_tag',
  display_name: { de: 'asset_tag', en: 'asset_tag' },
  is_system: true,
};

interface Backend {
  /** The effective permissions of /me/permissions. */
  permissions?: string[];
  /** Answer of DELETE /attribute-definitions/{id}; default: 204. */
  remove?: () => Response;
}

function stubBackend({
  permissions = ['ci:read', 'citype:manage', 'ci_attribute:manage'],
  remove,
}: Backend = {}): Call[] {
  const calls: Call[] = [];
  // The stored global attributes: a saved one shows in later reads.
  let globals: Record<string, unknown>[] = [ORG_DEFINITION, SYSTEM_DEFINITION];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method ?? 'GET').toUpperCase();
      const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, url, body });
      if (url.includes('/me/permissions')) return json({ user_id: 'u1', permissions });
      if (url.includes('/field-permissions/'))
        return json({ data: [], limit: 200, has_more: false });
      if (method === 'POST' && /\/attribute-definitions$/.test(url)) {
        const saved = { ...ORG_DEFINITION, id: 'gd-2', ...(body as object) };
        globals = [...globals, saved];
        return json(saved, 201);
      }
      if (method === 'DELETE' && url.includes('/attribute-definitions/')) {
        if (remove) return remove();
        const id = url.slice(url.lastIndexOf('/') + 1);
        globals = globals.filter((d) => d.id !== id);
        return new Response(null, { status: 204 });
      }
      if (method === 'PATCH' && url.includes('/attribute-definitions/')) {
        const id = url.slice(url.lastIndexOf('/') + 1);
        return json({ ...ORG_DEFINITION, id, ...(body as object) });
      }
      if (url.includes('/attribute-definitions?')) {
        // ?scope=global and ?applicable_to=<type> both list the global attributes.
        return json({ data: globals });
      }
      if (url.includes('/ci-types')) {
        return json({
          data: [SYSTEM_TYPE, ORG_TYPE],
          total: 2,
          limit: 200,
          offset: 0,
          has_more: false,
        });
      }
      return json({ detail: `unexpected ${method} ${url}` }, 404);
    }),
  );
  return calls;
}

/** section renders the page and returns the global attributes once listed. */
async function section() {
  renderWithProviders(<CITypeAdminPage />, { route: '/admin/ci-types' });
  const heading = await screen.findByRole('heading', { name: 'Globale Attribute' });
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

describe('CITypeAdminPage global attributes', () => {
  it('lists the organization attributes and the read-only system attributes without a selected type', async () => {
    const calls = stubBackend();
    const card = await section();

    expect(card).toHaveTextContent(
      'Globale Attribute gelten für die CI-Typen ihres Geltungsbereichs',
    );
    const own = rowOf(card, 'cost_center');
    expect(own).toHaveTextContent('gilt für alle Typen');
    expect(
      await within(own).findByRole('button', {
        name: 'Globales Attribut bearbeiten: cost_center',
      }),
    ).toBeInTheDocument();
    expect(
      within(own).getByRole('button', { name: 'Globales Attribut löschen: cost_center' }),
    ).toBeInTheDocument();

    const system = rowOf(card, 'asset_tag');
    expect(within(system).getByText('System')).toBeInTheDocument();
    expect(within(system).queryByRole('button')).not.toBeInTheDocument();
    expect(
      calls.some(
        (c) => c.url.includes('/attribute-definitions?') && c.url.includes('scope=global'),
      ),
    ).toBe(true);
    expect(calls.some((c) => c.url.includes('/ci-fields/global'))).toBe(false);
  });

  it('adds a global attribute through one POST /attribute-definitions with its index flags', async () => {
    const calls = stubBackend();
    const card = await section();
    fireEvent.click(await within(card).findByRole('button', { name: 'Neues globales Attribut' }));
    const dialog = screen.getByRole('dialog');
    expect(
      within(dialog).getByRole('heading', { name: 'Neues globales Attribut' }),
    ).toBeInTheDocument();
    expect(dialog).toHaveTextContent(
      'Das Attribut gilt für die CI-Typen seines Geltungsbereichs. Hier angelegte globale Attribute sind optional.',
    );
    expect(within(dialog).queryByLabelText('Pflichtfeld')).not.toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText('Feldname'), {
      target: { value: 'contract_owner' },
    });
    fireEvent.click(within(dialog).getByLabelText('Durchsuchbar (indiziert)'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const posts = calls.filter((c) => c.method === 'POST');
    expect(posts).toHaveLength(1);
    expect(posts[0]?.url).toMatch(/\/attribute-definitions$/);
    expect(posts[0]?.body).toEqual({
      key: 'contract_owner',
      data_type: 'text',
      group: 'general',
      required: false,
      applies_to: 'all',
      searchable: true,
      unique: false,
      unique_scope: 'org',
    });
    expect(calls.some((c) => c.method === 'PUT' || c.method === 'PATCH')).toBe(false);
    // The list reloads with the new attribute.
    expect(await within(card).findByText('contract_owner')).toBeInTheDocument();
  });

  it("keeps the type's 'Feld hinzufügen' the only button whose name contains it", async () => {
    stubBackend();
    const card = await section();
    fireEvent.click(await screen.findByRole('treeitem', { name: 'Flagtyp' }));
    expect(await screen.findByRole('button', { name: 'Feld hinzufügen' })).toBeInTheDocument();
    expect(
      within(card).getByRole('button', { name: 'Neues globales Attribut' }),
    ).toBeInTheDocument();
    // Playwright's getByRole({ name }) matches a case-insensitive substring.
    expect(screen.getAllByRole('button', { name: /feld hinzufügen/i })).toHaveLength(1);
  });

  it('offers the required checkbox for a field of a type', async () => {
    stubBackend();
    await section();
    fireEvent.click(await screen.findByRole('treeitem', { name: 'Flagtyp' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Feld hinzufügen' }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByLabelText('Pflichtfeld')).toBeInTheDocument();
    expect(dialog).not.toHaveTextContent('globale Attribute sind optional');
  });

  it('edits the index flags of a global attribute', async () => {
    const calls = stubBackend();
    const card = await section();
    fireEvent.click(
      await within(card).findByRole('button', {
        name: 'Globales Attribut bearbeiten: cost_center',
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

  it('deletes a global attribute after the confirmation only', async () => {
    const calls = stubBackend();
    const confirm = vi
      .spyOn(window, 'confirm')
      .mockReturnValueOnce(false)
      .mockReturnValueOnce(true);
    const card = await section();
    const button = await within(rowOf(card, 'cost_center')).findByRole('button', {
      name: 'Globales Attribut löschen: cost_center',
    });

    fireEvent.click(button);
    expect(confirm).toHaveBeenCalledWith(
      'Globales Attribut „cost_center“ löschen? Es entfällt danach bei allen CI-Typen seines Geltungsbereichs.',
    );
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);

    fireEvent.click(button);
    await waitFor(() => expect(within(card).queryByText('cost_center')).not.toBeInTheDocument());
    const deletes = calls.filter((c) => c.method === 'DELETE');
    expect(deletes).toHaveLength(1);
    expect(deletes[0]?.url).toMatch(/\/attribute-definitions\/gd-1$/);
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
        name: 'Globales Attribut löschen: cost_center',
      }),
    );

    expect(await within(card).findByRole('alert')).toHaveTextContent(
      'Attribut „cost_center“ nicht gelöscht. Keine Berechtigung: Ihrer Sitzung fehlt das Recht „ci_attribute:manage“.',
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
