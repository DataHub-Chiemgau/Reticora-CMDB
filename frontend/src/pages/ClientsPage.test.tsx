/**
 * WP-855 (UI-14): the client administration. The list shows name and short
 * name (slug); holders of user:manage create (slug suggested from the
 * name), change and delete clients; a taken slug (409) is shown at its
 * field, a refused delete names the dependencies of the problem; without
 * user:manage the page only reads.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import deDE from '../i18n/de-DE.json';
import enUS from '../i18n/en-US.json';
import { renderWithProviders } from '../test/utils';
import { useScopeStore } from '../stores/scope';
import { ClientsPage, suggestSlug } from './ClientsPage';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const AT = '2026-01-01T00:00:00Z';

function client(id: string, name: string, slug: string) {
  return {
    id,
    organization_id: 'org-1',
    name,
    slug,
    external_ref: null,
    timezone: null,
    settings: {},
    created_at: AT,
    updated_at: AT,
  };
}

const acme = client('client-1', 'Acme GmbH', 'acme');
const globex = client('client-2', 'Globex AG', 'globex');

function mockApi(
  overrides: {
    permissions?: string[];
    create?: (body: Record<string, unknown>) => Response;
    del?: () => Response;
  } = {},
) {
  const clients = [acme, globex];
  return vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost');
    const method = init?.method ?? 'GET';
    if (url.pathname.endsWith('/me/permissions')) {
      return json({
        user_id: 'user-1',
        permissions: overrides.permissions ?? ['location:read', 'user:manage'],
      });
    }
    if (url.pathname.endsWith('/clients') && method === 'GET') {
      return json({ data: clients, total: clients.length, limit: 200, offset: 0, has_more: false });
    }
    if (url.pathname.endsWith('/clients') && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      if (overrides.create) return overrides.create(body);
      const created = client('client-9', String(body.name), String(body.slug));
      clients.push(created);
      return json(created, 201);
    }
    if (/\/clients\/[^/]+$/.test(url.pathname) && method === 'PATCH') {
      return json({ ...acme, ...(JSON.parse(String(init?.body)) as Record<string, unknown>) });
    }
    if (/\/clients\/[^/]+$/.test(url.pathname) && method === 'DELETE') {
      return overrides.del ? overrides.del() : new Response(null, { status: 204 });
    }
    return json({ title: 'Not Found', status: 404 }, 404);
  });
}

const calls = (fetchMock: ReturnType<typeof mockApi>, method: string, path: RegExp) =>
  fetchMock.mock.calls.filter(
    (call) =>
      (call[1]?.method ?? 'GET') === method &&
      path.test(new URL(String(call[0]), 'http://localhost').pathname),
  );
const bodyOf = (call: unknown[] | undefined) =>
  JSON.parse(String((call?.[1] as RequestInit | undefined)?.body)) as Record<string, unknown>;

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  useScopeStore.getState().reset();
});

describe('suggestSlug', () => {
  it('derives a lower-case short name from the name', () => {
    expect(suggestSlug('Müller & Söhne GmbH')).toBe('mueller-soehne-gmbh');
    expect(suggestSlug('  Café Straße 12 ')).toBe('cafe-strasse-12');
    expect(suggestSlug('---')).toBe('');
    expect(suggestSlug('a'.repeat(70))).toHaveLength(63);
  });
});

describe('ClientsPage', () => {
  it('lists the clients with name and short name', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<ClientsPage />);

    const table = await screen.findByRole('table');
    expect(within(table).getByText('Acme GmbH')).toBeInTheDocument();
    expect(within(table).getByText('acme')).toBeInTheDocument();
    expect(within(table).getByText('Globex AG')).toBeInTheDocument();
    expect(String(calls(fetchMock, 'GET', /\/clients$/)[0]?.[0])).toContain('limit=200');
    expect(await screen.findByRole('button', { name: 'Kunde anlegen' })).toBeVisible();
  });

  it('creates a client with the short name suggested from the name', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<ClientsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Kunde anlegen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Kunde anlegen' });
    fireEvent.change(within(dialog).getByLabelText('Name'), {
      target: { value: 'Müller & Söhne GmbH' },
    });
    expect(within(dialog).getByLabelText('Kürzel')).toHaveValue('mueller-soehne-gmbh');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    await waitFor(() => expect(calls(fetchMock, 'POST', /\/clients$/)).toHaveLength(1));
    expect(bodyOf(calls(fetchMock, 'POST', /\/clients$/)[0])).toEqual({
      name: 'Müller & Söhne GmbH',
      slug: 'mueller-soehne-gmbh',
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    // The list is read again and shows the new client.
    expect(await screen.findByText('Müller & Söhne GmbH')).toBeInTheDocument();
  });

  it('keeps a short name edited by hand and shows a taken one (409) at the field', async () => {
    const fetchMock = mockApi({
      create: () =>
        json(
          {
            title: 'Conflict',
            status: 409,
            detail: 'the value is already taken',
            field: 'slug',
          },
          409,
        ),
    });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<ClientsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Kunde anlegen' }));
    const dialog = await screen.findByRole('dialog');
    const slug = within(dialog).getByLabelText('Kürzel');
    fireEvent.change(slug, { target: { value: 'acme' } });
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Acme Two' } });
    expect(slug).toHaveValue('acme');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    expect(await within(dialog).findByText('Dieses Kürzel ist bereits vergeben.')).toBeVisible();
    expect(slug).toHaveAttribute('aria-invalid', 'true');
    expect(within(dialog).queryByRole('alert')).toBeNull();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('refuses an empty name before sending', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<ClientsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Kunde anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));
    expect(await within(dialog).findByText('Geben Sie einen Namen an.')).toBeVisible();
    expect(calls(fetchMock, 'POST', /\/clients$/)).toHaveLength(0);
  });

  it('changes a client with PATCH of the changed fields only', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<ClientsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten: Acme GmbH' }));
    const dialog = await screen.findByRole('dialog', { name: '„Acme GmbH“ bearbeiten' });
    // The short name of an existing client does not follow the name.
    fireEvent.change(within(dialog).getByLabelText('Name'), {
      target: { value: 'Acme Holding GmbH' },
    });
    expect(within(dialog).getByLabelText('Kürzel')).toHaveValue('acme');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls(fetchMock, 'PATCH', /\/clients\/client-1$/)).toHaveLength(1));
    expect(bodyOf(calls(fetchMock, 'PATCH', /\/clients\/client-1$/)[0])).toEqual({
      name: 'Acme Holding GmbH',
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('deletes a client after confirmation and leaves it as scope', async () => {
    useScopeStore.getState().setClient('client-1');
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<ClientsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Löschen: Acme GmbH' }));
    const dialog = await screen.findByRole('dialog', { name: 'Kunde „Acme GmbH“ löschen?' });
    expect(dialog).toHaveTextContent('seine Standorte mit ihm');
    expect(calls(fetchMock, 'DELETE', /\/clients\//)).toHaveLength(0);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Löschen' }));

    await waitFor(() => expect(calls(fetchMock, 'DELETE', /\/clients\/client-1$/)).toHaveLength(1));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(useScopeStore.getState().clientId).toBe('');
  });

  it('shows the dependencies of a refused delete instead of a general error', async () => {
    const fetchMock = mockApi({
      del: () =>
        json(
          {
            title: 'Conflict',
            status: 409,
            detail: 'the client is still referenced',
            dependencies: ['ci', 'asset'],
          },
          409,
        ),
    });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<ClientsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Löschen: Globex AG' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Löschen' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Der Kunde wird noch verwendet von: CIs, Assets',
    );
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('explains a delete the server answers with 404', async () => {
    vi.stubGlobal(
      'fetch',
      mockApi({
        del: () => json({ title: 'Not Found', status: 404, detail: 'client not found' }, 404),
      }),
    );
    renderWithProviders(<ClientsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Löschen: Globex AG' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Löschen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Es gibt ihn nicht mehr, oder es verweisen noch Objekte auf ihn.',
    );
  });

  it('only reads without user:manage', async () => {
    vi.stubGlobal('fetch', mockApi({ permissions: ['location:read'] }));
    renderWithProviders(<ClientsPage />);

    expect(await screen.findByText('Acme GmbH')).toBeInTheDocument();
    expect(await screen.findByText(/erfordern das Recht user:manage/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Kunde anlegen' })).toBeNull();
    expect(screen.queryByRole('button', { name: /^Bearbeiten:/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /^Löschen:/ })).toBeNull();
  });

  it('names the page "Kunden" / "Customers" in the navigation', () => {
    expect(deDE.nav.clients).toBe('Kunden');
    expect(enUS.nav.clients).toBe('Customers');
    expect(deDE.clients.title).toBe('Kunden');
    expect(enUS.clients.title).toBe('Customers');
  });
});
