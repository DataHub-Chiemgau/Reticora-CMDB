/**
 * WP-1122 (SEC-02, E-236): the product mapping view of the findings page.
 * The groups without product mapping are listed, the dialog takes the
 * manufacturer and model of a group, suggests vendor and product from the
 * catalog, and after saving the match job is linked; without finding:write
 * the view is read only; the texts exist in de and en.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/findings/ProductMappingPanel.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import i18n from '../../i18n';
import de from '../../i18n/de-DE.json';
import en from '../../i18n/en-US.json';
import { ProductMappingPanel } from './ProductMappingPanel';

type Json = Record<string, unknown>;

const AT = '2026-11-01T00:00:00Z';
const MAPPING_ID = 'a0000000-0000-4000-8000-000000000001';
const JOB_ID = 'b0000000-0000-4000-8000-000000000001';

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 200,
  offset: 0,
  has_more: false,
});

const group = {
  manufacturer: 'Obscura',
  model: 'Box 1',
  sys_object_id: '',
  ci_count: 3,
  example_ci_id: 'c0000000-0000-4000-8000-000000000001',
};

const mapping = {
  id: MAPPING_ID,
  match_kind: 'sys_object_id',
  sys_object_id_prefix: '1.3.6.1.4.1.99',
  vendor: 'acme',
  products: ['acmeos'],
  version_field: 'firmware_version',
  version_scheme: 'generic',
  priority: 100,
  created_at: AT,
  updated_at: AT,
};

function stubApi(
  permissions = ['finding:read', 'finding:write'],
  opts: { clients?: string[]; unmappedPages?: unknown[][] } = {},
) {
  const calls: { method: string; path: string; params: URLSearchParams; body?: Json }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = init?.method ?? 'GET';
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      calls.push({ method, path, params: url.searchParams, body });
      if (path === '/me/permissions') return json({ user_id: 'u', permissions });
      if (path === '/me') return json({ sub: 'u', org: 'o', cls: opts.clients ?? null, sts: null });
      if (path === '/security/unmapped') {
        const pages = opts.unmappedPages;
        if (!pages) return json(page([group]));
        const offset = Number(url.searchParams.get('offset') ?? '0');
        const index = offset === 0 ? 0 : 1;
        const total = pages.reduce((n, p) => n + p.length, 0);
        return json({
          data: pages[index],
          total,
          limit: 200,
          offset,
          has_more: index < pages.length - 1,
        });
      }
      if (path === '/security/cpe-mappings' && method === 'GET') return json(page([mapping]));
      if (path === '/security/vuln-products') {
        return json({ data: [{ vendor: 'obscura', product: 'boxos' }] });
      }
      if (path === '/security/cpe-mappings' && method === 'POST') {
        return json({ ...mapping, ...body, id: 'new', match_job_id: JOB_ID }, 201);
      }
      return json(page([]));
    }),
  );
  return calls;
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function openDialogOfGroup() {
  fireEvent.click(await screen.findByRole('button', { name: 'Obscura zuordnen' }));
  return screen.findByRole('dialog', { name: 'Produktzuordnung' });
}

describe('ProductMappingPanel', () => {
  it('lists groups without product mapping', async () => {
    stubApi();
    renderWithProviders(<ProductMappingPanel />);
    const table = await screen.findByRole('region', { name: 'Ohne Produktzuordnung' });
    expect(await within(table).findByText('Obscura')).toBeInTheDocument();
    expect(within(table).getByText('Box 1')).toBeInTheDocument();
    expect(within(table).getByText('3')).toBeInTheDocument();
    expect(await screen.findByText('1.3.6.1.4.1.99')).toBeInTheDocument();
  });

  it('prefills the dialog from a group', async () => {
    stubApi();
    renderWithProviders(<ProductMappingPanel />);
    const dialog = await openDialogOfGroup();
    expect(within(dialog).getByLabelText('Zuordnung über')).toHaveValue('manufacturer_model');
    expect(within(dialog).getByLabelText('Herstellermuster (* und ?)')).toHaveValue('Obscura');
    expect(within(dialog).getByLabelText('Modellmuster (optional)')).toHaveValue('Box 1');
    expect(within(dialog).getByLabelText('Katalog durchsuchen')).toHaveValue('Obscura');
  });

  it('suggests vendor and product from the catalog', async () => {
    const calls = stubApi();
    renderWithProviders(<ProductMappingPanel />);
    const dialog = await openDialogOfGroup();
    fireEvent.click(await within(dialog).findByRole('button', { name: 'obscura / boxos' }));
    expect(within(dialog).getByLabelText('Hersteller im Katalog')).toHaveValue('obscura');
    expect(within(dialog).getByLabelText('Produkte (kommagetrennt)')).toHaveValue('boxos');
    expect(
      calls.some((c) => c.path === '/security/vuln-products' && c.params.get('q') === 'Obscura'),
    ).toBe(true);
  });

  it('shows the match job after saving', async () => {
    const calls = stubApi();
    renderWithProviders(<ProductMappingPanel />);
    const dialog = await openDialogOfGroup();
    fireEvent.click(await within(dialog).findByRole('button', { name: 'obscura / boxos' }));
    fireEvent.change(within(dialog).getByLabelText('Versionsschema'), {
      target: { value: 'junos' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    const link = await screen.findByRole('link', { name: `Abgleich-Job ${JOB_ID} ansehen` });
    expect(link).toHaveAttribute('href', '/jobs');
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.body).toEqual({
      match_kind: 'manufacturer_model',
      manufacturer_pattern: 'Obscura',
      model_pattern: 'Box 1',
      vendor: 'obscura',
      products: ['boxos'],
      version_field: 'firmware_version',
      version_scheme: 'junos',
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('is read-only without finding:write', async () => {
    stubApi(['finding:read']);
    renderWithProviders(<ProductMappingPanel />);
    expect(await screen.findByText('Obscura')).toBeInTheDocument();
    expect(await screen.findByText('1.3.6.1.4.1.99')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Obscura zuordnen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Zuordnung anlegen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /löschen/ })).not.toBeInTheDocument();
  });

  it('is read-only for a principal bound to a client', async () => {
    stubApi(['finding:read', 'finding:write'], { clients: ['c1'] });
    renderWithProviders(<ProductMappingPanel />);
    expect(await screen.findByText('Obscura')).toBeInTheDocument();
    expect(await screen.findByText('1.3.6.1.4.1.99')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Obscura zuordnen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Zuordnung anlegen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /löschen/ })).not.toBeInTheDocument();
  });

  it('loads further pages of the groups', async () => {
    const second = { ...group, manufacturer: 'Umbra', model: 'Box 9' };
    const calls = stubApi(['finding:read'], { unmappedPages: [[group], [second]] });
    renderWithProviders(<ProductMappingPanel />);
    expect(await screen.findByText('1 von 2 angezeigt')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Weitere laden' }));
    expect(await screen.findByText('Umbra')).toBeInTheDocument();
    expect(screen.getByText('Obscura')).toBeInTheDocument();
    expect(
      calls.some((c) => c.path === '/security/unmapped' && c.params.get('offset') === '1'),
    ).toBe(true);
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Weitere laden' })).not.toBeInTheDocument(),
    );
  });

  it('has its texts in de and en', async () => {
    const keys = (o: object, prefix = ''): string[] =>
      Object.entries(o).flatMap(([k, v]) =>
        v && typeof v === 'object' ? keys(v as object, `${prefix}${k}.`) : [`${prefix}${k}`],
      );
    const deKeys = keys(de.findings.mapping).concat(keys(de.findings.tabs));
    const enKeys = keys(en.findings.mapping).concat(keys(en.findings.tabs));
    expect(deKeys.sort()).toEqual(enKeys.sort());
    stubApi();
    await i18n.changeLanguage('en-US');
    renderWithProviders(<ProductMappingPanel />);
    expect(
      await screen.findByRole('heading', { name: 'Without product mapping' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Mappings' })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Map Obscura' })).toBeInTheDocument();
  });
});
