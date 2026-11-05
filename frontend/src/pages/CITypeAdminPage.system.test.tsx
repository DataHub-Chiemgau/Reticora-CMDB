/**
 * System CI types are read-only (E-10; since WP-313 the attribute editor
 * writes /attribute-definitions instead of PUT /ci-types/{id}/fields): on a fresh
 * install every CI type is a system type, and "Feld hinzufügen" on it was
 * answered with the 403 of row-level security, shown as "Keine Berechtigung"
 * although the administrator holds every permission.
 *   (+) a system type offers neither "Feld hinzufügen" nor "Deaktivieren",
 *       but "Klonen" and a hint to the two ways that work (a clone, the
 *       global attributes)
 *   (+) the 422 on ci_type_id of a field on a system type (a stale list or
 *       a direct call) reads as a read-only system type, not as a missing
 *       permission; so does the 403 of a system attribute definition
 *   (+) "Keine Berechtigung" is only the 403 of the route authorization
 *       (missing required permission: ...), with the permission named
 *   (+) the checkbox labels of the field editor and the template names of
 *       the template dialog carry explicit light and dark text colours
 *       (they render in a portal outside the app shell's text colour)
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/CITypeAdminPage.system.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
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

/** The seeded system type server (organization_id NULL) as GET /ci-types lists it. */
const SYSTEM_TYPE = {
  id: 't-sys',
  key: 'server',
  name: 'Server',
  display_name: { de: 'Server', en: 'Server' },
  is_active: true,
  is_system: true,
};

/** A type of the organization, e.g. the clone of a system type. */
const ORG_TYPE = {
  id: 't-org',
  key: 'server_copy',
  name: 'Server-Kopie',
  display_name: { de: 'Server-Kopie', en: 'Server copy' },
  is_active: true,
  is_system: false,
};

/** The clone of SYSTEM_TYPE (POST /ci-types/t-sys/clone): a type of the organization. */
const CLONE = {
  id: 't-clone',
  key: 'server_copy_2',
  name: 'Server (Kopie)',
  display_name: { de: 'Server (Kopie)', en: 'Server (copy)' },
  is_active: true,
  is_system: false,
};

function definition(id: string, key: string, ciTypeId: string, isSystem = false) {
  return {
    id,
    key,
    ci_type_id: ciTypeId,
    scope: 'type',
    display_name: { de: key, en: key },
    data_type: 'text',
    required: false,
    multi_value: false,
    searchable: false,
    unique: false,
    unique_scope: 'org',
    inheritable: true,
    order: 0,
    is_system: isSystem,
    applies_to: 'all',
    applies_to_categories: [],
    applies_to_type_ids: [],
  };
}

/** The organization's definition inventory_no of ORG_TYPE and the system attribute hostname. */
const DEFINITION = definition('ad-1', 'inventory_no', 't-org');
const DEFINITIONS = [
  DEFINITION,
  definition('ad-sys', 'hostname', 't-sys', true),
  definition('ad-clone', 'hostname', 't-clone'),
];

interface Backend {
  /** Answer of POST /attribute-definitions; default: the saved definition. */
  post?: () => Response;
  /** Answer of PATCH /attribute-definitions/ad-1; default: the patched definition. */
  patch?: () => Response;
}

function stubBackend({ post, patch }: Backend = {}): Call[] {
  const calls: Call[] = [];
  let cloned = false;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method ?? 'GET').toUpperCase();
      const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, url, body });
      if (method === 'POST' && /\/attribute-definitions$/.test(url)) {
        return post ? post() : json(definition('ad-2', 'x', 't-org'), 201);
      }
      if (method === 'PATCH' && url.includes('/attribute-definitions/ad-1')) {
        return patch ? patch() : json({ ...DEFINITION, ...(body as object) });
      }
      if (method === 'POST' && url.includes('/ci-types/t-sys/clone')) {
        cloned = true;
        return json(CLONE, 201);
      }
      if (url.includes('/attribute-definitions?')) {
        const typeId = /(?:applicable_to|ci_type_id)=([^&]+)/.exec(url)?.[1];
        return json({ data: DEFINITIONS.filter((d) => d.ci_type_id === typeId) });
      }
      if (url.includes('/field-permissions/'))
        return json({ data: [], limit: 200, has_more: false });
      if (url.includes('/me/permissions')) {
        return json({ user_id: 'u1', permissions: ['citype:manage', 'ci_attribute:manage'] });
      }
      if (url.includes('/ci-types/templates')) {
        return json({
          data: [
            {
              ...ORG_TYPE,
              id: 'tpl-1',
              key: 'tpl_server',
              name: 'Vorlage Server',
              display_name: { de: 'Vorlage Server', en: 'Server template' },
            },
          ],
        });
      }
      if (method === 'GET' && /\/ci-types\/t-sys(\?|$)/.test(url)) {
        return json({ ...SYSTEM_TYPE, fields: [{ name: 'hostname', data_type: 'text' }] });
      }
      if (method === 'GET' && /\/ci-types\/t-clone(\?|$)/.test(url)) {
        return json({ ...CLONE, fields: [{ name: 'hostname', data_type: 'text' }] });
      }
      if (method === 'GET' && /\/ci-types\/t-org(\?|$)/.test(url)) {
        return json({ ...ORG_TYPE, fields: [{ name: 'inventory_no', data_type: 'text' }] });
      }
      if (url.includes('/ci-types')) {
        return json({
          data: cloned ? [SYSTEM_TYPE, ORG_TYPE, CLONE] : [SYSTEM_TYPE, ORG_TYPE],
          total: cloned ? 3 : 2,
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

async function openType(name: RegExp) {
  renderWithProviders(<CITypeAdminPage />, { route: '/admin/ci-types' });
  fireEvent.click(await screen.findByRole('treeitem', { name }));
}

/** saveNewField opens the editor on the organization's type and saves a field. */
async function saveNewField() {
  await openType(/Server-Kopie/);
  fireEvent.click(await screen.findByRole('button', { name: 'Feld hinzufügen' }));
  fireEvent.change(screen.getByLabelText('Feldname'), { target: { value: 'x' } });
  fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
  return within(screen.getByRole('dialog')).findByRole('alert');
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CITypeAdminPage system types', () => {
  it('offers no field and no deactivation on a system type, but the clone and the hint', async () => {
    const calls = stubBackend();
    await openType(/^Server(?!-)/);

    expect(await screen.findByText('hostname')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Feld hinzufügen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Deaktivieren' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Löschen' })).not.toBeInTheDocument();
    const note = screen.getByRole('note');
    expect(note).toHaveTextContent('Systemtyp: schreibgeschützt.');
    expect(note).toHaveTextContent('„Klonen“ legt einen eigenen Typ mit allen Feldern');
    expect(note).toHaveTextContent('Globale Attribute gelten für alle CI-Typen');

    // The clone is selected: a type of the organization that takes fields.
    fireEvent.click(screen.getByRole('button', { name: 'Klonen' }));
    expect(
      await screen.findByRole('heading', { name: 'Server (Kopie)', level: 3 }),
    ).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Feld hinzufügen' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Deaktivieren' })).toBeInTheDocument();
    expect(screen.queryByRole('note')).not.toBeInTheDocument();
    expect(calls.some((c) => c.method === 'POST' && c.url.includes('/t-sys/clone'))).toBe(true);
    expect(calls.some((c) => c.method === 'PUT')).toBe(false);
  });

  it('keeps adding fields and deactivating on a type of the organization', async () => {
    stubBackend();
    await openType(/Server-Kopie/);

    expect(await screen.findByRole('button', { name: 'Feld hinzufügen' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Deaktivieren' })).toBeInTheDocument();
    expect(screen.queryByRole('note')).not.toBeInTheDocument();
  });

  it('shows the 422 on ci_type_id as a read-only system type, not as a missing permission', async () => {
    stubBackend({
      post: () =>
        problem(422, {
          title: 'Unprocessable Entity',
          detail: 'ci_type_id: system ci types cannot get organization attributes',
          field: 'ci_type_id',
        }),
    });
    const alert = await saveNewField();

    expect(alert).toHaveTextContent(
      'Feld nicht gespeichert. Systemtypen sind schreibgeschützt und erhalten keine eigenen Felder.',
    );
    expect(alert).not.toHaveTextContent('Keine Berechtigung');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('names the missing permission of the route authorization', async () => {
    stubBackend({
      post: () =>
        problem(403, { title: 'Forbidden', detail: 'missing required permission: citype:manage' }),
    });
    const alert = await saveNewField();

    expect(alert).toHaveTextContent(
      'Feld nicht gespeichert. Keine Berechtigung: Ihrer Sitzung fehlt das Recht „citype:manage“.',
    );
  });

  it('shows any other 403 with the reason of the server, not as a missing permission', async () => {
    stubBackend({
      post: () => problem(403, { title: 'Forbidden', detail: 'the operation is not permitted' }),
    });
    const alert = await saveNewField();

    expect(alert).toHaveTextContent(
      'Feld nicht gespeichert. Grund: the operation is not permitted',
    );
    expect(alert).not.toHaveTextContent('Keine Berechtigung');
  });

  it('shows the 403 of a system attribute definition as read-only', async () => {
    stubBackend({
      patch: () =>
        problem(403, { title: 'Forbidden', detail: 'system attribute definitions are read-only' }),
    });
    await openType(/Server-Kopie/);
    fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten: inventory_no' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.click(within(dialog).getByLabelText('Durchsuchbar (indiziert)'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    const alert = await within(dialog).findByRole('alert');
    expect(alert).toHaveTextContent(
      'Änderungen nicht gespeichert. System-Attribute sind schreibgeschützt.',
    );
    expect(alert).not.toHaveTextContent('Keine Berechtigung');
  });
});

describe('CITypeAdminPage dialog text colours', () => {
  it('gives the checkbox labels of the field editor an explicit theme text colour', async () => {
    stubBackend();
    await openType(/Server-Kopie/);
    fireEvent.click(await screen.findByRole('button', { name: 'Feld hinzufügen' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.click(within(dialog).getByLabelText('Eindeutig'));

    for (const name of ['Pflichtfeld', 'Durchsuchbar (indiziert)', 'Eindeutig']) {
      expect(within(dialog).getByLabelText(name).closest('label'), name).toHaveClass('text-fg');
    }
  });

  it('gives the template names an explicit theme text colour', async () => {
    stubBackend();
    renderWithProviders(<CITypeAdminPage />, { route: '/admin/ci-types' });
    fireEvent.click(await screen.findByRole('button', { name: 'Aus Typvorlage' }));
    const dialog = screen.getByRole('dialog');

    expect(await within(dialog).findByText('Vorlage Server')).toHaveClass('text-fg');
  });
});
