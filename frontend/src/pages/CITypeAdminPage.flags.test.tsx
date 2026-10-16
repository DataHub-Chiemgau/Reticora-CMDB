/**
 * Phase-1 acceptance audit, group T6-epic-b – REGRESSION test (must pass).
 *
 * WP-142 (DB-04: searchable/unique flags of attribute definitions)
 * Gap (high) "Frontend unit tests of the flag UI": the field editor of the CI
 * type admin saves the field (PUT /ci-types/{id}/fields) and then sends the
 * index flags to PATCH /attribute-definitions/{id}:
 *   (+) searchable → PATCH {searchable:true, unique:false, unique_scope:'org'}
 *   (+) unique with scope client → PATCH {unique:true, unique_scope:'client'}
 *   (edge) no flags → no PATCH
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/CITypeAdminPage.flags.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
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

function stubBackend(): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method ?? 'GET').toUpperCase();
      const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, url, body });
      if (method === 'PUT' && url.includes('/ci-types/t1/fields')) {
        return json({ id: 'ad-1', name: 'inventory_no', data_type: 'text' });
      }
      if (method === 'PATCH' && url.includes('/attribute-definitions/ad-1')) {
        return json({ id: 'ad-1', ...(body as object) });
      }
      if (url.includes('/ci-types')) {
        return json({
          data: [
            {
              id: 't1',
              key: 'server',
              name: 'Server',
              is_active: true,
              is_system: false,
              fields: [],
            },
          ],
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
});
