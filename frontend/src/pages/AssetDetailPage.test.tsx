// UI-14, AST-04 (WP-809): the asset detail page. The master data with
// version, holder and active reservation come from GET /assets/{id}; the
// CIs, the children (child_mode) and the documents from exactly one POST
// /bff/graphql with asset(id). PATCH /assets/{id} sends If-Match; a 409 with
// fields shows the conflict with "Neu laden", a 403 with fields marks the
// input. CIs are linked with POST and released with DELETE
// /assets/{id}/cis; children of configuration and standalone differ; the
// warranty banner shows from 30 and 7 days and after the end; the holder
// appears and return/transfer exist only with one; the active reservation
// is released; "Asset verschieben" opens the dialog of StockFlowDialogs;
// FORBIDDEN on documents hides only the document section. The page calls
// none of the legacy paths (children, movements, parent, lifecycle
// definitions, compositions).
//
// Retest: cd frontend && npx vitest run src/pages/AssetDetailPage.test.tsx
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import i18n from '../i18n';
import { renderWithProviders } from '../test/utils';
import { AssetDetailPage, noticeDate, warrantyLevel } from './AssetDetailPage';

const ASSET = 'a1000000-0000-4000-8000-000000000001';
const CI_PRIMARY = 'c1000000-0000-4000-8000-000000000001';
const CI_OTHER = 'c1000000-0000-4000-8000-000000000002';
const CI_NEW = 'c1000000-0000-4000-8000-000000000003';
const CHILD_CONFIG = 'c1000000-0000-4000-8000-000000000004';
const CHILD_STANDALONE = 'c1000000-0000-4000-8000-000000000005';
const CHILD_ASSET = 'a1000000-0000-4000-8000-000000000002';
const USER = 'u1000000-0000-4000-8000-000000000001';
const RESERVATION = 'r1000000-0000-4000-8000-000000000001';
const DOC = 'd1000000-0000-4000-8000-000000000001';
const RIGHTS = [
  'asset:read',
  'asset:write',
  'asset:move',
  'ci:write',
  'stock:write',
  'document:write',
  'user:read',
];

interface Call {
  method: string;
  path: string;
  search: string;
  body: unknown;
  headers: Headers;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 50,
  offset: 0,
  has_more: false,
});

/** day is the calendar day `offset` days from today (local date, as the banner counts). */
function day(offset: number): string {
  const date = new Date();
  date.setDate(date.getDate() + offset);
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

const baseAsset = {
  id: ASSET,
  organization_id: 'org-1',
  inventory_no: 'INV-0042',
  name: 'Notebook Lisa',
  category: 'hardware',
  manufacturer: 'Lenovo',
  serial_number: 'SN-42',
  lifecycle_state: 'deployed',
  primary_ci_id: CI_PRIMARY,
  custom_fields: {},
  version: 3,
  created_at: '2026-11-01T10:00:00Z',
  updated_at: '2026-11-01T10:00:00Z',
};

const ci = (id: string, name: string, extra: Record<string, unknown> = {}) => ({
  id,
  name,
  status: 'active',
  lifecycleState: null,
  serialNumber: null,
  locationId: null,
  assetId: ASSET,
  parentCiId: null,
  childMode: null,
  ...extra,
});

const aggregate = {
  data: {
    asset: {
      id: ASSET,
      primaryCiId: CI_PRIMARY,
      cis: [ci(CI_PRIMARY, 'nb-lisa'), ci(CI_OTHER, 'nb-lisa-dock')],
      children: [
        ci(CHILD_CONFIG, 'ssd-1', {
          assetId: null,
          parentCiId: CI_PRIMARY,
          childMode: 'configuration',
          serialNumber: 'SN-42',
          lifecycleState: 'deployed',
        }),
        ci(CHILD_STANDALONE, 'monitor-1', {
          assetId: CHILD_ASSET,
          parentCiId: CI_PRIMARY,
          childMode: 'standalone',
          serialNumber: 'MON-7',
        }),
      ],
      documents: [
        {
          id: DOC,
          title: 'Lieferschein 42',
          description: null,
          fileName: 'lieferschein.pdf',
          fileSize: 1024,
          mimeType: 'application/pdf',
          version: 2,
          category: 'invoice',
          tags: [],
          uploadedBy: USER,
          scanStatus: 'clean',
          createdAt: '2026-11-01T10:00:00Z',
          updatedAt: '2026-11-01T10:00:00Z',
        },
      ],
    },
  },
};

interface Stub {
  asset?: Record<string, unknown>;
  bff?: unknown;
  write?: (method: string, path: string, body: unknown) => Response | undefined;
}

/** stubApi answers the reads of the page and records every request. */
function stubApi({ asset = {}, bff = aggregate, write }: Stub = {}) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({ method, path, search: url.search, body, headers: new Headers(init?.headers) });
      if (path === '/bff/graphql') return json(bff);
      if (method !== 'GET') return write?.(method, path, body) ?? json({}, 200);
      if (path === '/me/permissions') return json({ user_id: 'user-1', permissions: RIGHTS });
      if (path === `/assets/${ASSET}`) return json({ ...baseAsset, ...asset });
      if (path === `/assets/${ASSET}/lifecycle-transitions`) {
        return json({ current_state: 'deployed', transitions: [] });
      }
      if (path === `/users/${USER}`) return json({ id: USER, display_name: 'Anna Berg' });
      if (path === '/cis') return json(page([{ id: CI_NEW, name: 'nb-neu', status: 'active' }]));
      if (path === '/suppliers' || path === '/contracts') return json(page([]));
      if (path.startsWith('/history/')) return json(page([]));
      return json(page([]));
    }),
  );
  return calls;
}

function renderDetail() {
  return renderWithProviders(
    <Routes>
      <Route path="/assets/:id" element={<AssetDetailPage />} />
    </Routes>,
    { route: `/assets/${ASSET}` },
  );
}

const bffCalls = (calls: Call[]) => calls.filter((c) => c.path === '/bff/graphql');

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('AssetDetailPage (UI-14, AST-04, WP-809)', () => {
  it('reads the lists in exactly one BFF query and no legacy path', async () => {
    const calls = stubApi();
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Notebook Lisa', level: 1 })).toBeVisible();
    const cis = (await screen.findByText('nb-lisa')).closest('li')!;
    expect(within(cis).getByText('Primär-CI')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'nb-lisa' })).toHaveAttribute(
      'href',
      `/cmdb/${CI_PRIMARY}`,
    );
    expect(await screen.findByRole('link', { name: 'Lieferschein 42' })).toHaveAttribute(
      'href',
      `/documents/${DOC}`,
    );
    await waitFor(() => expect(calls.some((c) => c.path.startsWith('/history/'))).toBe(true));

    expect(bffCalls(calls)).toHaveLength(1);
    const request = bffCalls(calls)[0]!;
    expect(request.method).toBe('POST');
    const { query, variables } = request.body as { query: string; variables: { id: string } };
    expect(query).toContain('asset(id: $id)');
    expect(query).toMatch(/cis\(first: 50\)/);
    expect(query).toMatch(/children\(first: 50\)/);
    expect(query).toMatch(/documents\(first: 50\)/);
    expect(variables).toEqual({ id: ASSET });
    for (const call of calls) {
      expect(call.path).not.toMatch(
        /\/assets\/[^/]+\/(children|movements)|\/cis\/[^/]+\/parent|lifecycle-definitions|compositions/,
      );
    }
    // The movements come from the ledger with asset_id (WP-806).
    expect(
      calls.some((c) => c.path === '/stock-movements' && c.search.includes(`asset_id=${ASSET}`)),
    ).toBe(true);
  });

  it('shows configuration children as from the asset and links standalone children', async () => {
    stubApi();
    renderDetail();

    const config = (await screen.findByText('ssd-1')).closest('li')!;
    expect(config).toHaveAttribute('data-child-mode', 'configuration');
    expect(within(config).getByText('Konfiguration')).toBeInTheDocument();
    expect(within(config).queryByRole('link')).not.toBeInTheDocument();
    expect(within(config).getAllByText(/\(vom Asset\)/)).toHaveLength(3);

    const standalone = screen.getByRole('link', { name: 'monitor-1' }).closest('li')!;
    expect(standalone).toHaveAttribute('data-child-mode', 'standalone');
    expect(within(standalone).getByText('Eigenständig')).toBeInTheDocument();
    expect(within(standalone).getByRole('link', { name: 'monitor-1' })).toHaveAttribute(
      'href',
      `/assets/${CHILD_ASSET}`,
    );
    expect(within(standalone).queryByText(/\(vom Asset\)/)).not.toBeInTheDocument();
  });

  it('sends the PATCH with If-Match and shows a 409 conflict with its fields and reload', async () => {
    const calls = stubApi({
      write: (method, path) =>
        method === 'PATCH' && path === `/assets/${ASSET}`
          ? json(
              {
                type: 'urn:reticora:problem:conflict',
                title: 'Conflict',
                status: 409,
                detail: 'changed since version 3',
                current_version: 4,
                fields: ['model', 'serial_number'],
              },
              409,
            )
          : undefined,
    });
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Modell'), { target: { value: 'T14' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    expect(
      await within(dialog).findByText(/zwischenzeitlich geändert \(model, serial_number\)/),
    ).toBeInTheDocument();
    const patch = calls.find((c) => c.method === 'PATCH')!;
    expect(patch.path).toBe(`/assets/${ASSET}`);
    expect(patch.headers.get('If-Match')).toBe('"3"');
    expect(patch.body).toEqual({ model: 'T14' });

    const reads = calls.filter((c) => c.method === 'GET' && c.path === `/assets/${ASSET}`).length;
    fireEvent.click(within(dialog).getByRole('button', { name: 'Neu laden' }));
    await waitFor(() =>
      expect(
        calls.filter((c) => c.method === 'GET' && c.path === `/assets/${ASSET}`).length,
      ).toBeGreaterThan(reads),
    );
  });

  it('marks the inputs a 403 with fields refuses', async () => {
    stubApi({
      write: (method) =>
        method === 'PATCH'
          ? json(
              {
                type: 'urn:reticora:problem:forbidden',
                title: 'Forbidden',
                status: 403,
                detail: 'field cost_center is not writable',
                fields: ['cost_center'],
              },
              403,
            )
          : undefined,
    });
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    const input = within(dialog).getByLabelText('Kostenstelle');
    fireEvent.change(input, { target: { value: 'KST-9' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'));
    expect(within(dialog).getAllByText('field cost_center is not writable').length).toBeGreaterThan(
      0,
    );
    expect(within(dialog).getByLabelText('Modell')).not.toHaveAttribute('aria-invalid', 'true');
  });

  it('links a CI with POST and releases one with DELETE /assets/{id}/cis', async () => {
    const calls = stubApi({
      write: (method, path, body) => {
        if (method === 'POST' && path === `/assets/${ASSET}/cis`) {
          return json({ asset_id: ASSET, ...(body as object) }, 201);
        }
        if (method === 'DELETE') return new Response(null, { status: 204 });
        return undefined;
      },
    });
    renderDetail();

    const picker = await screen.findByRole('combobox', { name: 'CI verknüpfen' });
    fireEvent.change(picker, { target: { value: 'nb-neu' } });
    fireEvent.click(await screen.findByRole('option', { name: /nb-neu/ }));
    fireEvent.click(screen.getByLabelText('Als Primär-CI setzen'));
    fireEvent.click(screen.getByRole('button', { name: 'Verknüpfen' }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path === `/assets/${ASSET}/cis`)).toBe(
        true,
      ),
    );
    expect(
      calls.find((c) => c.method === 'POST' && c.path === `/assets/${ASSET}/cis`)!.body,
    ).toEqual({ ci_id: CI_NEW, primary: true });

    fireEvent.click(screen.getByRole('button', { name: 'nb-lisa-dock vom Asset lösen' }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === 'DELETE' && c.path === `/assets/${ASSET}/cis/${CI_OTHER}`),
      ).toBe(true),
    );
  });

  it('shows a 422 on asset_id of a refused link', async () => {
    stubApi({
      write: (method) =>
        method === 'POST'
          ? json(
              {
                type: 'urn:reticora:problem:invalid-field',
                status: 422,
                field: 'asset_id',
                detail: 'the CI belongs to another client',
              },
              422,
            )
          : undefined,
    });
    renderDetail();

    const picker = await screen.findByRole('combobox', { name: 'CI verknüpfen' });
    fireEvent.change(picker, { target: { value: 'nb-neu' } });
    fireEvent.click(await screen.findByRole('option', { name: /nb-neu/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Verknüpfen' }));
    expect(
      await screen.findByText(
        'Das CI kann nicht verknüpft werden: the CI belongs to another client',
      ),
    ).toBeInTheDocument();
  });

  it('shows the warranty banner from 30 and 7 days and after the end', async () => {
    expect(warrantyLevel(day(31))).toBeUndefined();
    expect(warrantyLevel(day(30))?.level).toBe('notice');
    expect(warrantyLevel(day(8))?.level).toBe('notice');
    expect(warrantyLevel(day(7))?.level).toBe('warning');
    expect(warrantyLevel(day(0))?.level).toBe('warning');
    expect(warrantyLevel(day(-1))?.level).toBe('expired');
    expect(noticeDate('2027-03-31', 90)).toBe('2026-12-31');

    for (const [offset, level, text] of [
      [30, 'notice', /in 30 Tagen/],
      [7, 'warning', /endet bald.*in 7 Tagen/],
      [-3, 'expired', /Garantie abgelaufen/],
    ] as const) {
      stubApi({ asset: { warranty_until: day(offset) } });
      const { unmount } = renderDetail();
      const banner = await screen.findByText(text);
      expect(banner).toHaveAttribute('data-warranty', level);
      unmount();
      vi.unstubAllGlobals();
    }
  });

  it('shows the holder and offers return and transfer only with one', async () => {
    stubApi({
      asset: { assigned_user_id: USER, assignment_due_at: '2026-12-01T00:00:00Z' },
    });
    const { unmount } = renderDetail();
    expect(await screen.findByText('Anna Berg')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Zurücknehmen' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Übertragen' })).toBeInTheDocument();
    unmount();
    vi.unstubAllGlobals();

    stubApi();
    renderDetail();
    expect(await screen.findByRole('button', { name: 'Zuweisen' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Zurücknehmen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Übertragen' })).not.toBeInTheDocument();
  });

  it('releases the active reservation', async () => {
    const calls = stubApi({
      asset: {
        active_reservation: {
          id: RESERVATION,
          reserved_until: '2026-12-15',
          project_ref: 'PRJ-7',
        },
      },
      write: (_method, path) =>
        path === `/reservations/${RESERVATION}/release`
          ? json({ id: RESERVATION, status: 'released' })
          : undefined,
    });
    renderDetail();

    expect(await screen.findByText('PRJ-7')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reservieren' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Reservierung freigeben' }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === 'POST' && c.path === `/reservations/${RESERVATION}/release`),
      ).toBe(true),
    );
  });

  it('opens the move dialog of StockFlowDialogs with the asset', async () => {
    stubApi();
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: 'Asset verschieben' }));
    const dialog = await screen.findByRole('dialog', {
      name: 'Asset an einen Standort verschieben',
    });
    expect(within(dialog).getByText(/Notebook Lisa/)).toBeInTheDocument();
  });

  it('hides only the document section on FORBIDDEN of documents', async () => {
    const calls = stubApi({
      bff: {
        data: { asset: { ...aggregate.data.asset, documents: null } },
        errors: [
          {
            message: 'missing permission document:read',
            path: ['asset', 'documents'],
            extensions: { code: 'FORBIDDEN' },
          },
        ],
      },
    });
    renderDetail();

    expect(await screen.findByText('nb-lisa')).toBeInTheDocument();
    expect(screen.getByText('ssd-1')).toBeInTheDocument();
    expect(screen.queryByText('Dokumente')).not.toBeInTheDocument();
    expect(screen.queryByText(/missing permission/)).not.toBeInTheDocument();
    expect(bffCalls(calls)).toHaveLength(1);
  });
});
