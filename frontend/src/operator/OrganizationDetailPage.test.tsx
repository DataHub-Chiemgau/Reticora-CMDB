/**
 * WP-888 (PKG-07, UI-14): the operator's detail page of an organization. The
 * preview of a downgrade pro → essential names the locked features and the
 * changed limits; a 422 appears at the field reason; cancelling an add-on
 * updates the group "Add-on"; the history shows only the entries of the
 * organization (target_org).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import { renderWithProviders } from '../test/utils';
import { useOperatorAuth } from './operatorAuth';
import { OrganizationDetailPage } from './OrganizationDetailPage';
import { planPreview } from './planPreview';
import type { PlanInfo } from '../api/client';

const ORG = '11111111-1111-4111-8111-111111111111';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const MATRIX: PlanInfo[] = [
  {
    plan: 'essential',
    features: ['cmdb_core', 'documents'],
    quota_defaults: { max_cis: 500, max_users: 5, max_storage_mb: 1000 },
  },
  {
    plan: 'standard',
    features: ['cmdb_core', 'documents', 'monitoring'],
    quota_defaults: { max_cis: 2500, max_users: 25, max_storage_mb: 10000 },
  },
  {
    plan: 'pro',
    features: ['cmdb_core', 'documents', 'monitoring', 'workflows'],
    quota_defaults: { max_cis: 10000, max_users: 100, max_storage_mb: 50000 },
  },
  {
    plan: 'enterprise',
    features: ['cmdb_core', 'documents', 'monitoring', 'workflows', 'monitoring_analytics'],
    quota_defaults: { max_cis: 50000 },
  },
];

function row(
  feature_key: string,
  origin: string,
  limits: Record<string, number> = {},
  enabled = true,
) {
  return {
    organization_id: ORG,
    feature_key,
    plan: 'pro',
    enabled,
    limits,
    source: 'manual',
    origin,
    updated_at: '2026-11-01T00:00:00Z',
  };
}

const ROWS = [
  row('cmdb_core', 'plan', { max_cis: 3000, max_users: 100 }),
  row('documents', 'plan', { max_storage_mb: 50000 }),
  row('monitoring', 'plan'),
  row('workflows', 'plan'),
  row('iga', 'addon'),
];

function mockApi(overrides: { changePlan?: () => Response } = {}) {
  let iga = true;
  const fetchMock = vi
    .fn()
    .mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = init?.method ?? 'GET';
      const path = url.pathname;
      if (path === `/api/v1/admin/orgs/${ORG}/entitlements` && method === 'GET') {
        const rows = ROWS.map((r) => (r.feature_key === 'iga' ? { ...r, enabled: iga } : r));
        return json({
          data: rows,
          total: rows.length,
          limit: rows.length,
          offset: 0,
          has_more: false,
          plan: 'pro',
          plan_changed_at: null,
        });
      }
      if (path === `/api/v1/admin/orgs/${ORG}/usage`) {
        return json({
          plan: 'pro',
          data: [{ quota: 'max_cis', used: 3, limit: 3000, origin: 'override' }],
        });
      }
      if (path === '/api/v1/admin/plans') {
        return json({ data: MATRIX, addons: [{ key: 'iga', phase: 'A', description_key: 'x' }] });
      }
      if (path === '/api/v1/admin/audit') {
        const target = url.searchParams.get('target_org');
        const all = [
          {
            id: 2,
            timestamp: '2026-11-10T10:00:00Z',
            operator_id: 'op-1',
            operator_kind: 'oidc',
            action: 'plan.change',
            target_org: ORG,
            status: 200,
            details: {},
          },
          {
            id: 1,
            timestamp: '2026-11-09T10:00:00Z',
            operator_id: 'op-2',
            operator_kind: 'oidc',
            action: 'org.create',
            target_org: 'other',
            status: 200,
            details: {},
          },
        ];
        const data = all.filter((e) => !target || e.target_org === target);
        return json({ data, total: data.length, limit: 50, offset: 0, has_more: false });
      }
      if (path === `/api/v1/admin/orgs/${ORG}/plan` && method === 'PUT') {
        return overrides.changePlan ? overrides.changePlan() : json({});
      }
      if (path === `/api/v1/admin/orgs/${ORG}/addons/iga` && method === 'DELETE') {
        iga = false;
        return new Response(null, { status: 204 });
      }
      return json({ title: 'Not Found', status: 404 }, 404);
    });
  return fetchMock;
}

function lastOf<T>(items: T[]): T {
  return items[items.length - 1] as T;
}

function render() {
  return renderWithProviders(
    <Routes>
      <Route path="/operator/orgs/:id" element={<OrganizationDetailPage />} />
    </Routes>,
    { route: `/operator/orgs/${ORG}` },
  );
}

beforeEach(() => {
  useOperatorAuth.getState().setSession({
    token: 'op-token',
    expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
    operator: { id: 'op-1', kind: 'oidc' },
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  useOperatorAuth.getState().clearSession();
});

describe('planPreview', () => {
  it('names the locked features and the changed limits of pro → essential', () => {
    const preview = planPreview('pro', 'essential', ROWS as never, MATRIX);
    expect(preview.locked).toEqual(['monitoring', 'workflows']);
    expect(preview.unlocked).toEqual([]);
    // max_cis 3000 is an override and stays; the defaults follow essential.
    expect(preview.limits).toEqual([
      { quota: 'max_storage_mb', from: 50000, to: 1000 },
      { quota: 'max_users', from: 100, to: 5 },
    ]);
  });
});

describe('OrganizationDetailPage', () => {
  it('previews a downgrade with locked features and changed limits', async () => {
    vi.stubGlobal('fetch', mockApi());
    render();
    fireEvent.click(await screen.findByRole('button', { name: 'Paket wechseln' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Neues Paket'), {
      target: { value: 'essential' },
    });
    const preview = within(dialog).getByRole('region', { name: 'Vorschau' });
    expect(preview).toHaveTextContent(
      'Gesperrt (Zeilen bleiben erhalten): Monitoring, Workflows und Formulare',
    );
    expect(preview).toHaveTextContent('Benutzer: 100 → 5');
    expect(preview).toHaveTextContent('Dokumentenspeicher: 50000 → 1000');
    expect(preview).not.toHaveTextContent('CIs:');
  });

  it('shows a 422 at the field reason', async () => {
    vi.stubGlobal(
      'fetch',
      mockApi({
        changePlan: () =>
          json(
            {
              title: 'Unprocessable Entity',
              status: 422,
              detail: 'reason is required',
              field: 'reason',
            },
            422,
          ),
      }),
    );
    render();
    fireEvent.click(await screen.findByRole('button', { name: 'Paket wechseln' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Neues Paket'), {
      target: { value: 'essential' },
    });
    fireEvent.change(within(dialog).getByLabelText(/Grund/), { target: { value: 'x' } });
    fireEvent.click(lastOf(within(dialog).getAllByRole('button', { name: 'Paket wechseln' })));
    const reason = within(dialog).getByLabelText(/Grund/);
    await waitFor(() => expect(reason).toHaveAttribute('aria-invalid', 'true'));
    expect(within(dialog).getByText(/reason is required/)).toBeInTheDocument();
    expect(screen.queryByText(/Das Paket ist jetzt/)).toBeNull();
  });

  it('cancels an add-on and updates the add-on group', async () => {
    vi.stubGlobal('fetch', mockApi());
    render();
    fireEvent.click(await screen.findByRole('button', { name: 'IGA kündigen' }));
    expect(await screen.findByText('Das Add-on wurde gekündigt.')).toBeInTheDocument();
    const group = screen.getByRole('list', { name: 'Add-on' });
    await waitFor(() => expect(within(group).getByText('Inaktiv')).toBeInTheDocument());
    expect(within(group).queryByRole('button', { name: 'IGA kündigen' })).toBeNull();
  });

  it('shows only the history of the organization', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    render();
    const history = await screen.findByRole('list', { name: 'Verlauf' });
    expect(within(history).getByText(/Paketwechsel durch op-1/)).toBeInTheDocument();
    expect(within(history).queryByText(/op-2/)).toBeNull();
    const auditCall = fetchMock.mock.calls.find((c) => String(c[0]).includes('/admin/audit'));
    expect(String(auditCall?.[0])).toContain(`target_org=${ORG}`);
  });
});
