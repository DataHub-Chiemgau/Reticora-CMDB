import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { ExportPage } from './ExportPage';
import * as exportDownload from '../lib/exportDownload';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
import { useAuthStore } from '../auth/authStore';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

function signIn() {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
}

function entitlements(features: Record<string, boolean>) {
  return paginated(
    Object.entries(features).map(([feature_key, enabled]) => ({
      organization_id: 'org-1',
      feature_key,
      enabled,
    })),
  );
}

function modules(inventory: boolean) {
  return { data: [{ key: 'inventory', phase: 'P2', nav: ['assets'], enabled: inventory }] };
}

async function formatOptions() {
  const select = await screen.findByLabelText('Format');
  return within(select)
    .getAllByRole('option')
    .map((option) => option.textContent);
}

describe('ExportPage', () => {
  it('renders a completed job with a download link', async () => {
    stubFetchRoutes({
      '/exports': paginated([
        {
          id: 'job-1',
          organization_id: 'org-1',
          format: 'csv',
          status: 'completed',
          filters: {},
          object_key: 'org-1/job-1.csv',
          row_count: 42,
          file_size_bytes: 1024,
          expires_at: '2099-01-01T00:00:00Z',
          download_url: 'https://minio.example/exports/org-1/job-1.csv?X-Amz-Signature=abc',
          manifest: {
            format: 'csv',
            fields: ['id', 'name'],
            omitted: [
              { field: 'hostname', reason: 'layout' },
              { field: 'attributes', reason: 'layout' },
            ],
          },
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:01:00Z',
        },
      ]),
    });

    renderWithProviders(<ExportPage />, { route: '/export' });

    expect(await screen.findByText('42')).toBeInTheDocument();
    // The manifest names the fields the file leaves out (EXP-01).
    expect(screen.getByText('2 Felder nicht enthalten')).toHaveAttribute(
      'title',
      'hostname, attributes',
    );
    const link = screen.getByText('Herunterladen');
    expect(link).toHaveAttribute('href', expect.stringContaining('job-1.csv'));
  });

  it('shows the empty state when no jobs exist', async () => {
    stubFetchRoutes({ '/exports': paginated([]) });

    renderWithProviders(<ExportPage />, { route: '/export' });

    expect(await screen.findByText('Keine Exportaufträge')).toBeInTheDocument();
  });

  // The texts tell the CI export (CSV/JSON) from the asset export (DATEV).
  it('distinguishes the CI export from the asset export', async () => {
    stubFetchRoutes({ '/exports': paginated([]) });

    renderWithProviders(<ExportPage />, { route: '/export' });

    const hint = await screen.findByText(/als CSV oder JSON oder die Anlagen als DATEV-Datei/);
    expect(hint).toHaveTextContent('die CIs als CSV oder JSON');
    expect(screen.getByText(/CSV und JSON exportieren CIs/)).toHaveTextContent(
      'DATEV exportiert die Anlagen (Assets)',
    );
    expect(screen.queryByText(/CI-Daten als CSV, DATEV oder JSON/)).not.toBeInTheDocument();
  });

  // The error report of a DATEV job lists the asset rows the file leaves out
  // (EXP-01, WP-310).
  describe('DATEV error report', () => {
    const reportURL = 'https://minio.example/exports/org-1/job-d.errors.csv?X-Amz-Signature=e';

    function job(overrides: Record<string, unknown>) {
      return {
        id: 'job-d',
        organization_id: 'org-1',
        format: 'datev',
        status: 'completed',
        filters: { lifecycle_state: 'in_use' },
        row_count: 7,
        expires_at: '2099-01-01T00:00:00Z',
        download_url: 'https://minio.example/exports/org-1/job-d.datev?X-Amz-Signature=d',
        error_report_url: reportURL,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:01:00Z',
        ...overrides,
      };
    }

    it('links the error report of a completed DATEV job and downloads it as a blob', async () => {
      const save = vi.spyOn(exportDownload, 'downloadExport').mockResolvedValue(undefined);
      stubFetchRoutes({
        '/exports': paginated([
          job({
            manifest: {
              format: 'datev',
              fields: ['inventory_no', 'name'],
              omitted: [{ field: 'purchase_price', reason: 'field_permission' }],
            },
          }),
        ]),
      });

      renderWithProviders(<ExportPage />, { route: '/export' });

      const link = await screen.findByText('Fehlerreport');
      expect(link).toHaveAttribute('href', reportURL);
      expect(screen.getByText('Herunterladen')).toBeInTheDocument();
      // The manifest also names the DATEV columns left empty.
      expect(screen.getByText('1 Felder nicht enthalten')).toHaveAttribute(
        'title',
        'purchase_price',
      );

      fireEvent.click(link);
      expect(save).toHaveBeenCalledWith(reportURL, 'export_job-d.errors.csv');
    });

    it.each([
      ['a CSV job', { format: 'csv' }],
      ['an expired job', { status: 'expired', download_url: undefined }],
      ['a job past its expiry', { expires_at: '2020-01-01T00:00:00Z' }],
      ['a running job', { status: 'running', download_url: undefined }],
    ])('is not offered for %s', async (_name, overrides) => {
      stubFetchRoutes({ '/exports': paginated([job(overrides)]) });

      renderWithProviders(<ExportPage />, { route: '/export' });

      expect(await screen.findByText('7')).toBeInTheDocument();
      expect(screen.queryByText('Fehlerreport')).not.toBeInTheDocument();
    });
  });

  // The DATEV option follows the gate of the server (ENT-05, WP-790).
  describe('DATEV option', () => {
    const cases: Array<{
      name: string;
      features: Record<string, boolean>;
      inventory: boolean;
      shown: boolean;
    }> = [
      {
        name: 'without export_datev',
        features: { export_datev: false, assets_basic: true },
        inventory: true,
        shown: false,
      },
      {
        name: 'without assets_basic',
        features: { export_datev: true, assets_basic: false },
        inventory: true,
        shown: false,
      },
      {
        name: 'with the module inventory switched off',
        features: { export_datev: true, assets_basic: true },
        inventory: false,
        shown: false,
      },
      {
        name: 'with everything granted',
        features: { export_datev: true, assets_basic: true },
        inventory: true,
        shown: true,
      },
    ];

    it.each(cases)('is shown=$shown $name', async ({ features, inventory, shown }) => {
      signIn();
      const fetchMock = stubFetchRoutes({
        '/exports': paginated([]),
        '/entitlements': entitlements(features),
        '/modules': modules(inventory),
      });

      renderWithProviders(<ExportPage />, { route: '/export' });

      await waitFor(() => {
        const urls = fetchMock.mock.calls.map(([url]) => String(url));
        expect(urls.some((url) => url.includes('/entitlements'))).toBe(true);
        expect(urls.some((url) => url.includes('/modules'))).toBe(true);
      });
      if (shown) {
        await waitFor(async () => expect(await formatOptions()).toContain('DATEV'));
      } else {
        await waitFor(async () => expect(await formatOptions()).not.toContain('DATEV'));
      }
      expect(await formatOptions()).toEqual(expect.arrayContaining(['CSV', 'JSON']));
    });
  });
});
