/**
 * WP-817 (CI-08, UI-14): the tag administration. The catalog pages over
 * limit and cursor; holders of tag:manage create, rename (de/en), color and
 * delete tags; system tags cannot be deleted, a tag still assigned is
 * refused with 409; without tag:manage the page only reads.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import deDE from '../i18n/de-DE.json';
import enUS from '../i18n/en-US.json';
import { renderWithProviders } from '../test/utils';
import { TagsPage } from './TagsPage';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const AT = '2026-01-01T00:00:00Z';

function tag(id: string, key: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    organization_id: 'org-1',
    key,
    display_name: { de: key, en: key },
    color: null,
    is_system: false,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const archived = tag('tag-1', 'archived', {
  is_system: true,
  display_name: { de: 'Archiviert', en: 'Archived' },
});
const backup = tag('tag-2', 'backup', {
  display_name: { de: 'Sicherung', en: 'Backup' },
  color: '#1f6feb',
});
const legacy = tag('tag-3', 'legacy', { display_name: { de: 'Altsystem', en: 'Legacy' } });

function mockApi(
  overrides: {
    permissions?: string[];
    del?: () => Response;
  } = {},
) {
  return vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost');
    const method = init?.method ?? 'GET';
    if (url.pathname.endsWith('/me/permissions')) {
      return json({
        user_id: 'user-1',
        permissions: overrides.permissions ?? ['ci:read', 'tag:manage'],
      });
    }
    if (url.pathname.endsWith('/tags') && method === 'GET') {
      // Two pages: the second one follows the cursor of the first.
      if (url.searchParams.get('cursor') === 'c2') {
        return json({ data: [legacy], total: 3, limit: 50, offset: 0, has_more: false });
      }
      return json({
        data: [archived, backup],
        total: 3,
        limit: 50,
        offset: 0,
        has_more: true,
        next_cursor: 'c2',
      });
    }
    if (url.pathname.endsWith('/tags') && method === 'POST') {
      return json(tag('tag-9', 'new', JSON.parse(String(init?.body))), 201);
    }
    if (/\/tags\/[^/]+$/.test(url.pathname) && method === 'PATCH') {
      return json({ ...backup, ...JSON.parse(String(init?.body)) });
    }
    if (/\/tags\/[^/]+$/.test(url.pathname) && method === 'DELETE') {
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
});

describe('TagsPage', () => {
  it('pages through the catalog with limit and cursor', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<TagsPage />);

    const table = await screen.findByRole('table');
    expect(within(table).getAllByText('Sicherung')[0]).toBeInTheDocument();
    expect(within(table).getByText('#1f6feb')).toBeInTheDocument();
    const first = String(calls(fetchMock, 'GET', /\/tags$/)[0]?.[0]);
    expect(first).toContain('limit=50');
    expect(first).not.toContain('cursor');

    fireEvent.click(screen.getByRole('button', { name: 'Weiter' }));
    expect((await screen.findAllByText('Altsystem'))[0]).toBeInTheDocument();
    expect(String(calls(fetchMock, 'GET', /\/tags$/).slice(-1)[0]?.[0])).toContain('cursor=c2');
    expect(screen.getByRole('button', { name: 'Weiter' })).toBeDisabled();

    fireEvent.click(screen.getByRole('button', { name: 'Zurück' }));
    expect((await screen.findAllByText('Sicherung'))[0]).toBeInTheDocument();
  });

  it('creates a tag with key, names in de and en and a color', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<TagsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Tag anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Schlüssel'), {
      target: { value: 'prod-critical' },
    });
    fireEvent.change(within(dialog).getByLabelText('Name (Deutsch)'), {
      target: { value: 'Produktionskritisch' },
    });
    fireEvent.change(within(dialog).getByLabelText('Name (Englisch)'), {
      target: { value: 'Production critical' },
    });
    fireEvent.change(within(dialog).getByLabelText('Farbe'), { target: { value: '#D73A49' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    await waitFor(() => expect(calls(fetchMock, 'POST', /\/tags$/)).toHaveLength(1));
    expect(bodyOf(calls(fetchMock, 'POST', /\/tags$/)[0])).toEqual({
      key: 'prod-critical',
      display_name: { de: 'Produktionskritisch', en: 'Production critical' },
      color: '#d73a49',
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('refuses an invalid key or color before sending', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<TagsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Tag anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Schlüssel'), { target: { value: 'Prod X' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Der Schlüssel ist ungültig',
    );
    fireEvent.change(within(dialog).getByLabelText('Schlüssel'), { target: { value: 'prod' } });
    fireEvent.change(within(dialog).getByLabelText('Farbe'), { target: { value: 'red' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('#rrggbb');
    expect(calls(fetchMock, 'POST', /\/tags$/)).toHaveLength(0);
  });

  it('renames a tag in de and en and changes its color; the key stays', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<TagsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten: Sicherung' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByLabelText('Schlüssel')).toBeDisabled();
    expect(within(dialog).getByLabelText('Schlüssel')).toHaveValue('backup');
    fireEvent.change(within(dialog).getByLabelText('Name (Deutsch)'), {
      target: { value: 'Datensicherung' },
    });
    fireEvent.change(within(dialog).getByLabelText('Name (Englisch)'), {
      target: { value: 'Data backup' },
    });
    fireEvent.change(within(dialog).getByLabelText('Farbe'), { target: { value: '#2da44e' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls(fetchMock, 'PATCH', /\/tags\/tag-2$/)).toHaveLength(1));
    const body = bodyOf(calls(fetchMock, 'PATCH', /\/tags\/tag-2$/)[0]);
    expect(body).toEqual({
      display_name: { de: 'Datensicherung', en: 'Data backup' },
      color: '#2da44e',
    });
    expect('key' in body).toBe(false);
  });

  it('removes the color with an empty field (color null)', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<TagsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten: Sicherung' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Farbe'), { target: { value: '' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls(fetchMock, 'PATCH', /\/tags\/tag-2$/)).toHaveLength(1));
    expect(bodyOf(calls(fetchMock, 'PATCH', /\/tags\/tag-2$/)[0]).color).toBeNull();
  });

  it('deletes a tag and shows the 409 of an assigned tag as a message', async () => {
    const fetchMock = mockApi({
      del: () => json({ title: 'Conflict', status: 409, detail: 'tag is assigned to 3 CIs' }, 409),
    });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<TagsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Löschen: Sicherung' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Löschen' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Der Tag ist noch zugeordnet oder ein System-Tag und kann nicht gelöscht werden.',
    );
    expect(calls(fetchMock, 'DELETE', /\/tags\/tag-2$/)).toHaveLength(1);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('offers no delete for a system tag', async () => {
    vi.stubGlobal('fetch', mockApi());
    renderWithProviders(<TagsPage />);

    expect(await screen.findByRole('button', { name: 'Bearbeiten: Archiviert' })).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Löschen: Archiviert' })).toBeNull();
    const row = screen.getAllByText('Archiviert')[0]!.closest('tr')!;
    expect(row).toHaveTextContent('System');
  });

  it('only reads without tag:manage', async () => {
    const fetchMock = mockApi({ permissions: ['ci:read'] });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<TagsPage />);

    expect((await screen.findAllByText('Sicherung'))[0]).toBeInTheDocument();
    expect(screen.getByText(/erfordern das Recht tag:manage/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Tag anlegen' })).toBeNull();
    expect(screen.queryByRole('button', { name: /^Bearbeiten:/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /^Löschen:/ })).toBeNull();
  });

  it('names the page in the navigation and the command palette in both catalogs', () => {
    expect(deDE.nav.tags).toBe('Tags');
    expect(enUS.nav.tags).toBe('Tags');
    expect(deDE.commandPalette.commands.tags).toBe('Tags öffnen');
    expect(enUS.commandPalette.commands.tags).toBe('Open tags');
  });
});
