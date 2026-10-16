/**
 * Phase-1 acceptance audit, group T6-epic-b – DEFECT test (fails until fixed).
 *
 * WP-142 (DB-04, MET-55 unique/searchable flags)
 * Finding P2 "CI type admin silently ignores a failed searchable/unique update":
 * after PUT /ci-types/{id}/fields succeeds, PATCH /attribute-definitions/{id}
 * answers 409 (existing duplicates); CITypeAdminPage.tsx passes only onSuccess
 * to upsertField.mutate and never renders upsertField.error, so the admin
 * believes the attribute is unique.
 * Gap (high): (-) PATCH 409 → error text rendered and modal open.
 *
 * Retest:
 *   cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/pages/CITypeAdminPage.auditdefect.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { CITypeAdminPage } from './CITypeAdminPage';
import { renderWithProviders } from '../test/utils';

const DETAIL = 'CIs already share a value of inventory_no';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CITypeAdminPage flag update failure (audit defect)', () => {
  it('shows the 409 reason of a refused unique flag and keeps the dialog open', async () => {
    const calls: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === 'string' ? input : input.toString();
        const method = (init?.method ?? 'GET').toUpperCase();
        calls.push(`${method} ${url}`);
        if (method === 'PUT' && url.includes('/ci-types/t1/fields')) {
          return json({ id: 'ad-1', name: 'inventory_no', data_type: 'text' });
        }
        if (method === 'PATCH' && url.includes('/attribute-definitions/ad-1')) {
          return new Response(JSON.stringify({ title: 'Conflict', status: 409, detail: DETAIL }), {
            status: 409,
            headers: { 'Content-Type': 'application/problem+json' },
          });
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

    renderWithProviders(<CITypeAdminPage />, { route: '/admin/ci-types' });
    fireEvent.click(await screen.findByRole('button', { name: /Server/ }));
    fireEvent.click(await screen.findByRole('button', { name: 'Feld hinzufügen' }));
    fireEvent.change(screen.getByLabelText('Feldname'), { target: { value: 'inventory_no' } });
    fireEvent.click(screen.getByLabelText('Eindeutig'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls.some((c) => c.startsWith('PATCH'))).toBe(true));
    await waitFor(() => expect(screen.getByText(new RegExp(DETAIL))).toBeInTheDocument(), {
      timeout: 2000,
    });
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});
