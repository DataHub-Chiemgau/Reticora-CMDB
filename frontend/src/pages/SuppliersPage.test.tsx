/**
 * WP-808 (UI-14, MGT-12): the supplier page. Create and edit with If-Match
 * (a 412 offers to reload), the contact person chosen from GET /contacts
 * by display_name, a 409 at name, a 422 on delete (still referenced) as a
 * message, the search over filter[name][like] and no scope parameter.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { useScopeStore } from '../stores/scope';
import type { Supplier } from '../api/client';
import { SuppliersPage } from './SuppliersPage';

vi.mock('../auth/session', () => ({
  getSessionToken: () => 'token-1',
  clearSession: () => undefined,
  refreshSession: async () => true,
}));

const AT = '2026-01-01T00:00:00Z';

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

function page<T>(data: T[], extra: Record<string, unknown> = {}) {
  return json({ data, total: data.length, limit: 50, offset: 0, has_more: false, ...extra });
}

const supplier = (id: string, name: string, extra: Partial<Supplier> = {}): Supplier => ({
  id,
  organization_id: 'org-1',
  client_id: null,
  name,
  external_ref: 'K-100',
  contact_id: 'ct-1',
  notes: null,
  version: 2,
  created_at: AT,
  updated_at: AT,
  ...extra,
});

const contact = (id: string, display_name: string) => ({
  id,
  organization_id: 'org-1',
  display_name,
  is_24_7: false,
  created_at: AT,
  updated_at: AT,
});

interface Call {
  method: string;
  path: string;
  search: URLSearchParams;
  headers: Headers;
  body: unknown;
}

type Route = (call: Call) => Response;

function stubApi(
  routes: Record<string, Route>,
  { permissions = ['contract:read', 'contract:write'] } = {},
) {
  const calls: Call[] = [];
  const defaults: Record<string, Route> = {
    'GET /me/permissions': () => json({ user_id: 'user-1', permissions }),
    'GET /contacts': () => page([contact('ct-1', 'Erika Muster'), contact('ct-2', 'Max Probe')]),
    'GET /clients': () => page([{ id: 'cl-1', name: 'Kunde Eins' }]),
  };
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const call: Call = {
        method,
        path: url.pathname.replace(/^\/api\/v1/, ''),
        search: url.searchParams,
        headers: new Headers(init?.headers),
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      };
      calls.push(call);
      const key = `${method} ${call.path}`;
      const route = routes[key] ?? defaults[key];
      return route ? route(call) : json({ status: 404, detail: `unexpected ${key}` }, 404);
    }),
  );
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useScopeStore.getState().reset();
});

describe('SuppliersPage', () => {
  it('lists the suppliers with the contact name, sorted by name, without scope', async () => {
    useScopeStore.getState().setClient('cl-1');
    const calls = stubApi({
      'GET /suppliers': () => page([supplier('s-1', 'Wartungs GmbH')]),
    });
    renderWithProviders(<SuppliersPage />);

    const row = (await screen.findByText('Wartungs GmbH')).closest('tr')!;
    await waitFor(() => expect(within(row).getByText('Erika Muster')).toBeInTheDocument());
    expect(within(row).getByText('K-100')).toBeInTheDocument();
    expect(within(row).getByText('Organisationsweit')).toBeInTheDocument();
    const list = calls.find((c) => c.path === '/suppliers')!;
    expect(list.search.get('sort')).toBe('name');
    expect(list.search.has('client_id')).toBe(false);
    expect(list.search.has('filter[client_id]')).toBe(false);

    fireEvent.change(screen.getByRole('searchbox', { name: 'Lieferanten suchen' }), {
      target: { value: 'Wart' },
    });
    await waitFor(
      () =>
        expect(
          calls
            .filter((c) => c.path === '/suppliers')
            .slice(-1)[0]
            ?.search.get('filter[name][like]'),
        ).toBe('Wart'),
      { timeout: 2000 },
    );
  });

  it('creates a supplier with the chosen contact and shows a 409 at name', async () => {
    let taken = true;
    const calls = stubApi({
      'GET /suppliers': () => page([]),
      'POST /suppliers': (call) =>
        taken
          ? json({ status: 409, detail: 'name: taken', field: 'name' }, 409)
          : json(supplier('s-9', String((call.body as { name: string }).name)), 201),
    });
    renderWithProviders(<SuppliersPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Lieferant anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Wartungs GmbH' } });
    const contactSelect = within(dialog).getByLabelText('Kontaktperson');
    await within(contactSelect).findByRole('option', { name: 'Max Probe' });
    fireEvent.change(contactSelect, { target: { value: 'ct-2' } });
    fireEvent.change(within(dialog).getByLabelText('Externe Referenz'), {
      target: { value: 'K-7' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    await waitFor(() =>
      expect(within(dialog).getByLabelText('Name')).toHaveAttribute('aria-invalid', 'true'),
    );
    expect(
      within(dialog).getAllByText('Diesen Namen hat bereits ein anderer Lieferant.').length,
    ).toBe(2);
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
      name: 'Wartungs GmbH',
      external_ref: 'K-7',
      contact_id: 'ct-2',
    });

    taken = false;
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('edits with If-Match and reloads after a 412', async () => {
    let version = 2;
    const calls = stubApi({
      'GET /suppliers': () => page([supplier('s-1', 'Wartungs GmbH', { version })]),
      'GET /suppliers/s-1': () => {
        version = 3;
        return json(supplier('s-1', 'Wartungs GmbH', { version: 3, notes: 'geändert' }));
      },
      'PATCH /suppliers/s-1': (call) =>
        call.headers.get('If-Match') === '"2"'
          ? json({ status: 412, detail: 'the supplier has changed' }, 412)
          : json(supplier('s-1', 'Wartungs GmbH', { version: 4 })),
    });
    renderWithProviders(<SuppliersPage />);

    fireEvent.click(
      await screen.findByRole('button', { name: 'Lieferant Wartungs GmbH bearbeiten' }),
    );
    let dialog = await screen.findByRole('dialog');
    const contactSelect = within(dialog).getByLabelText('Kontaktperson');
    await within(contactSelect).findByRole('option', { name: 'Max Probe' });
    fireEvent.change(contactSelect, { target: { value: 'ct-2' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    expect(await within(dialog).findByText(/inzwischen geändert/)).toBeInTheDocument();
    const patch = calls.find((c) => c.method === 'PATCH')!;
    expect(patch.headers.get('If-Match')).toBe('"2"');
    expect(patch.body).toEqual({ contact_id: 'ct-2' });

    fireEvent.click(within(dialog).getByRole('button', { name: 'Neu laden' }));
    // The dialog opens again with the version read anew.
    await waitFor(() =>
      expect(within(screen.getByRole('dialog')).getByLabelText('Notizen')).toHaveValue('geändert'),
    );
    dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Wartung AG' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    const last = calls.filter((c) => c.method === 'PATCH').slice(-1)[0]!;
    expect(last.headers.get('If-Match')).toBe('"3"');
    expect(last.body).toEqual({ name: 'Wartung AG' });
  });

  it('shows a 422 on delete as message', async () => {
    stubApi({
      'GET /suppliers': () => page([supplier('s-1', 'Wartungs GmbH')]),
      'DELETE /suppliers/s-1': () =>
        json({ status: 422, detail: 'the supplier is still referenced' }, 422),
    });
    renderWithProviders(<SuppliersPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Lieferant Wartungs GmbH löschen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Löschen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Der Lieferant wird noch verwendet',
    );
  });

  it('shows no write actions without contract:write', async () => {
    stubApi(
      { 'GET /suppliers': () => page([supplier('s-1', 'Wartungs GmbH')]) },
      { permissions: ['contract:read'] },
    );
    renderWithProviders(<SuppliersPage />);
    expect(await screen.findByText('Wartungs GmbH')).toBeInTheDocument();
    expect(await screen.findByText(/contract:write fehlt/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Lieferant anlegen' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Lieferant Wartungs GmbH bearbeiten' })).toBeNull();
  });
});
