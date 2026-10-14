/**
 * Tests for the job center (UI-16): progress, cancel and result.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { JobCenterPage } from './JobCenterPage';

const running = {
  id: 'job-1',
  kind: 'ci.bulk',
  status: 'running',
  progress: 40,
  attempts: 1,
  max_attempts: 3,
  created_at: '2026-10-14T08:00:00Z',
};
const done = {
  id: 'job-2',
  kind: 'history.snapshot',
  status: 'succeeded',
  progress: 100,
  attempts: 1,
  max_attempts: 3,
  created_at: '2026-10-14T07:00:00Z',
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function stubServer() {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (init?.method === 'DELETE') return json({ ...running, status: 'cancelled' });
    if (url.endsWith('/jobs/job-2/result')) return json({ state: { name: 'srv-01' } });
    return json({ data: [running, done], total: 2, limit: 100, offset: 0, has_more: false });
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('JobCenterPage', () => {
  it('shows the progress and cancels a running job', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<JobCenterPage />);

    const bar = await screen.findByRole('progressbar', { name: 'Fortschritt von ci.bulk' });
    expect(bar).toHaveAttribute('aria-valuenow', '40');
    expect(within(screen.getByRole('table')).getByText('Läuft')).toBeInTheDocument();
    // Only the running job can be cancelled.
    const cancel = screen.getAllByRole('button', { name: 'Abbrechen' });
    expect(cancel).toHaveLength(1);
    fireEvent.click(cancel[0]!);

    await waitFor(() => {
      const del = fetchMock.mock.calls.find((call) => call[1]?.method === 'DELETE');
      expect(String(del?.[0])).toBe('/api/v1/jobs/job-1');
    });
  });

  it('shows the result of a succeeded job', async () => {
    stubServer();
    renderWithProviders(<JobCenterPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Ergebnis' }));
    const dialog = await screen.findByRole('dialog');
    expect(await within(dialog).findByText(/srv-01/)).toBeInTheDocument();
  });

  it('filters by status', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<JobCenterPage />);

    await screen.findByRole('progressbar', { name: 'Fortschritt von ci.bulk' });
    fireEvent.change(screen.getByLabelText('Status'), { target: { value: 'failed' } });
    await waitFor(() =>
      expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('status=failed'))).toBe(
        true,
      ),
    );
  });
});
