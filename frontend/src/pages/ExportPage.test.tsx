import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { ExportPage } from './ExportPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('ExportPage', () => {
  it('renders a completed job with a download link', async () => {
    stubFetchRoutes({
      '/export/jobs': paginated([
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
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:01:00Z',
        },
      ]),
    });

    renderWithProviders(<ExportPage />, { route: '/export' });

    expect(await screen.findByText('42')).toBeInTheDocument();
    const link = screen.getByText('Herunterladen');
    expect(link).toHaveAttribute('href', expect.stringContaining('job-1.csv'));
  });

  it('shows the empty state when no jobs exist', async () => {
    stubFetchRoutes({ '/export/jobs': paginated([]) });

    renderWithProviders(<ExportPage />, { route: '/export' });

    expect(await screen.findByText('Keine Exportaufträge')).toBeInTheDocument();
  });
});
