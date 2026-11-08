/**
 * Tests for the job center (UI-16): progress, cancel only where the server
 * cancels and the user may (JOB-03, F-162), its refusals as messages, the
 * partial report of a cancelled job, and the result only where a job has
 * one (F-347).
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { JobCenterPage } from './JobCenterPage';

const ME = 'u-1';
const OTHER = 'u-2';

const running = {
  id: 'job-1',
  kind: 'ci.bulk',
  status: 'running',
  progress: 40,
  attempts: 1,
  max_attempts: 3,
  created_by: OTHER,
  created_at: '2026-10-14T08:00:00Z',
  cancellable: true,
};
const done = {
  id: 'job-2',
  kind: 'history.snapshot',
  status: 'succeeded',
  progress: 100,
  attempts: 1,
  max_attempts: 3,
  created_by: ME,
  created_at: '2026-10-14T07:00:00Z',
  cancellable: false,
  result: { state: { name: 'srv-01' } },
};
/** A mirrored job (review.merge) that finished without a result. */
const mirrored = {
  id: 'job-3',
  kind: 'review.merge',
  status: 'succeeded',
  progress: 100,
  attempts: 1,
  max_attempts: 1,
  created_by: ME,
  created_at: '2026-10-14T06:00:00Z',
  cancellable: false,
};
/** A running job of a kind that cannot be cancelled while running. */
const merging = {
  id: 'job-4',
  kind: 'review.merge',
  status: 'running',
  progress: 10,
  attempts: 1,
  max_attempts: 1,
  created_by: ME,
  created_at: '2026-10-14T05:00:00Z',
  cancellable: false,
};
/** A queued export of another user: export:run controls it. */
const queuedExport = {
  id: 'job-5',
  kind: 'export',
  status: 'queued',
  progress: 0,
  attempts: 0,
  max_attempts: 1,
  created_by: OTHER,
  created_at: '2026-10-14T04:00:00Z',
  cancellable: true,
};
const partial = {
  action: 'set_status',
  total: 500,
  succeeded: 25,
  failed: 0,
  skipped: 475,
  cancelled: true,
  items: [{ id: 'ci-1', result: 'ok', code: 200 }],
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, detail: string) {
  return new Response(JSON.stringify({ title: 'Error', status, detail }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

interface Stub {
  jobs?: unknown[];
  permissions?: string[];
  /** The answer of DELETE /jobs/{id}; the default cancels the job with a partial report. */
  cancel?: () => Response;
  /** The answer of GET /me/permissions; the default grants permissions. */
  me?: () => Response;
  /** The answer of POST /jobs/{id}/revert. */
  revert?: (id: string) => Response;
}

function stubServer(stub: Stub = {}) {
  let jobs = stub.jobs ?? [running, done];
  const permissions = stub.permissions ?? ['job:read', 'ci:write', 'export:run'];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith('/me/permissions')) {
      return stub.me ? stub.me() : json({ user_id: ME, permissions });
    }
    if (init?.method === 'POST' && url.endsWith('/revert')) {
      const id = url.split('/').slice(-2)[0]!;
      return stub.revert ? stub.revert(id) : problem(409, 'no revert');
    }
    if (init?.method === 'DELETE') {
      if (stub.cancel) return stub.cancel();
      const id = url.split('/').pop();
      jobs = jobs.map((job) =>
        (job as { id: string }).id === id
          ? { ...(job as object), status: 'cancelled', cancellable: false, result: partial }
          : job,
      );
      return json(jobs.find((job) => (job as { id: string }).id === id));
    }
    if (url.includes('/result')) return problem(409, 'the job has no result');
    return json({ data: jobs, total: jobs.length, limit: 100, offset: 0, has_more: false });
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function rowOf(kind: string, status: string) {
  return screen
    .getAllByRole('row')
    .find((row) => row.textContent?.includes(kind) && row.textContent?.includes(status));
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('JobCenterPage', () => {
  it('cancels a running cancellable job and shows "Abgebrochen" with the partial report', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<JobCenterPage />);

    const bar = await screen.findByRole('progressbar', { name: 'Fortschritt von ci.bulk' });
    expect(bar).toHaveAttribute('aria-valuenow', '40');
    expect(within(screen.getByRole('table')).getByText('Läuft')).toBeInTheDocument();
    const cancel = await screen.findByRole('button', { name: 'Abbrechen' });
    expect(screen.getAllByRole('button', { name: 'Abbrechen' })).toHaveLength(1);
    fireEvent.click(cancel);

    await waitFor(() => {
      const del = fetchMock.mock.calls.find((call) => call[1]?.method === 'DELETE');
      expect(String(del?.[0])).toBe('/api/v1/jobs/job-1');
    });
    expect(await screen.findByText('Der Job wurde abgebrochen.')).toBeInTheDocument();
    await waitFor(() => expect(rowOf('ci.bulk', 'Abgebrochen')).toBeTruthy());
    const row = rowOf('ci.bulk', 'Abgebrochen')!;
    expect(
      within(row).getByText(
        'Teilbericht: 25 von 500 erfolgreich, 0 fehlgeschlagen, 475 übersprungen',
      ),
    ).toBeInTheDocument();
    expect(within(row).queryByRole('button', { name: 'Abbrechen' })).toBeNull();

    // The partial report of a bulk kind is a table per id, not JSON (WP-816).
    fireEvent.click(within(row).getByRole('button', { name: 'Teilbericht' }));
    const dialog = await screen.findByRole('dialog', { name: 'Teilbericht' });
    expect(
      within(dialog).getByText('25 von 500 erfolgreich, 0 fehlgeschlagen, 475 übersprungen'),
    ).toBeInTheDocument();
    const table = within(dialog).getByRole('table', { name: 'Bericht je CI' });
    expect(within(table).getAllByRole('row')).toHaveLength(2);
    expect(within(table).getByRole('row', { name: /ci-1/ })).toHaveTextContent('OK');
    expect(within(dialog).queryByText(/"cancelled"/)).toBeNull();
  });

  it('offers cancel only for queued jobs and running jobs with cancellable=true', async () => {
    stubServer({ jobs: [running, merging, queuedExport, done] });
    renderWithProviders(<JobCenterPage />);

    await screen.findByRole('progressbar', { name: 'Fortschritt von review.merge' });
    await waitFor(() =>
      expect(screen.getAllByRole('button', { name: 'Abbrechen' })).toHaveLength(2),
    );
    expect(
      within(rowOf('ci.bulk', 'Läuft')!).getByRole('button', { name: 'Abbrechen' }),
    ).toBeInTheDocument();
    expect(
      within(rowOf('export', 'Wartend')!).getByRole('button', { name: 'Abbrechen' }),
    ).toBeInTheDocument();
    // A running review.merge answers 409: no button.
    expect(
      within(rowOf('review.merge', 'Läuft')!).queryByRole('button', { name: 'Abbrechen' }),
    ).toBeNull();
  });

  it('offers no cancel without the permission of the kind unless the user created the job', async () => {
    const own = { ...running, id: 'job-6', kind: 'history.snapshot', created_by: ME };
    stubServer({ jobs: [running, queuedExport, own], permissions: ['job:read'] });
    renderWithProviders(<JobCenterPage />);

    await screen.findByRole('progressbar', { name: 'Fortschritt von history.snapshot' });
    // Only the own job; ci.bulk needs ci:write, export export:run.
    await waitFor(() =>
      expect(screen.getAllByRole('button', { name: 'Abbrechen' })).toHaveLength(1),
    );
    expect(
      within(rowOf('history.snapshot', 'Läuft')!).getByRole('button', { name: 'Abbrechen' }),
    ).toBeInTheDocument();
    expect(
      within(rowOf('ci.bulk', 'Läuft')!).queryByRole('button', { name: 'Abbrechen' }),
    ).toBeNull();
    expect(
      within(rowOf('export', 'Wartend')!).queryByRole('button', { name: 'Abbrechen' }),
    ).toBeNull();
  });

  it('offers no cancel while the effective rights cannot be read (fail closed)', async () => {
    stubServer({ me: () => problem(500, 'boom') });
    renderWithProviders(<JobCenterPage />);

    await screen.findByRole('progressbar', { name: 'Fortschritt von ci.bulk' });
    await waitFor(() =>
      expect(
        screen
          .getAllByRole('button')
          .some((b) => b.textContent === 'Ergebnis' || b.textContent === 'Teilbericht'),
      ).toBe(true),
    );
    expect(screen.queryByRole('button', { name: 'Abbrechen' })).toBeNull();
  });

  it.each([
    [409, 'Der Job lässt sich nicht mehr abbrechen'],
    [403, 'Sie dürfen diesen Job nicht abbrechen'],
  ])('shows the refusal %i of the server as a message', async (status, text) => {
    stubServer({ cancel: () => problem(status, 'refused') });
    renderWithProviders(<JobCenterPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Abbrechen' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(text);
    expect(screen.queryByText('Der Job wurde abgebrochen.')).toBeNull();
  });

  it('offers the result only where the job has one (F-347)', async () => {
    const fetchMock = stubServer({ jobs: [done, mirrored] });
    renderWithProviders(<JobCenterPage />);

    await screen.findByRole('progressbar', { name: 'Fortschritt von review.merge' });
    // succeeded without result: no button.
    expect(
      within(rowOf('review.merge', 'Erfolgreich')!).queryByRole('button', { name: 'Ergebnis' }),
    ).toBeNull();
    expect(screen.getAllByRole('button', { name: 'Ergebnis' })).toHaveLength(1);

    // succeeded with result: the dialog shows it.
    fireEvent.click(
      within(rowOf('history.snapshot', 'Erfolgreich')!).getByRole('button', { name: 'Ergebnis' }),
    );
    const dialog = await screen.findByRole('dialog', { name: 'Ergebnis' });
    expect(within(dialog).getByText(/srv-01/)).toBeInTheDocument();
    // The result comes with the job; no request can answer 409.
    expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('/result'))).toBe(false);
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

  it('labels the job kinds from the catalog and keeps the technical key', async () => {
    stubServer({ jobs: [running, done] });
    renderWithProviders(<JobCenterPage />);
    await screen.findByRole('progressbar', { name: 'Fortschritt von ci.bulk' });
    const row = rowOf('ci.bulk', 'Läuft')!;
    expect(within(row).getByText('Massenänderung CIs')).toBeInTheDocument();
    expect(rowOf('history.snapshot', 'Erfolgreich')).toHaveTextContent('Zustands-Snapshot');
  });
});

describe('JobCenterPage revert (E-25, WP-816)', () => {
  const finishedAt = (hoursAgo: number) =>
    new Date(Date.now() - hoursAgo * 3_600_000).toISOString();
  const succeeded = (id: string, kind: string, extra: Record<string, unknown> = {}) => ({
    id,
    kind,
    status: 'succeeded',
    progress: 100,
    attempts: 1,
    max_attempts: 1,
    created_by: ME,
    created_at: finishedAt(2),
    finished_at: finishedAt(1),
    cancellable: false,
    ...extra,
  });
  const report = {
    action: 'set_status',
    total: 2,
    succeeded: 1,
    failed: 1,
    items: [
      { id: 'ci-a', result: 'ok', code: 200 },
      { id: 'ci-b', result: 'error', code: 404, error: 'CI not found' },
    ],
  };
  const bulk = succeeded('j-bulk', 'ci.bulk', { result: report });
  const assetBulk = succeeded('j-asset', 'asset.bulk', { result: report });
  const merge = succeeded('j-merge', 'ci.merge', { finished_at: finishedAt(24 * 10) });
  const expired = succeeded('j-old', 'ci.bulk', { finished_at: finishedAt(25) });
  const reverted = succeeded('j-rev', 'ci.bulk', { reverted_at: finishedAt(0.5) });
  const revertJob = succeeded('j-undo', 'bulk.revert', {
    result: { bulk_job_id: 'j-x', total: 2, reverted: 2, skipped: 0, failed: 0, items: [] },
  });
  const extended = succeeded('j-win', 'ci.bulk', {
    finished_at: finishedAt(30),
    revertible_until: new Date(Date.now() + 3_600_000).toISOString(),
  });
  const failedBulk = { ...succeeded('j-fail', 'ci.bulk'), status: 'failed' };

  function revertRow(id: string) {
    return document.querySelector(`tr[data-job-id="${id}"]`) as HTMLElement;
  }

  it('offers "Rückgängig" only for succeeded jobs of kinds with revert within the window', async () => {
    stubServer({
      jobs: [bulk, assetBulk, merge, expired, reverted, revertJob, extended, failedBulk, done],
      permissions: ['job:read', 'ci:write', 'asset:write', 'ci:delete'],
    });
    renderWithProviders(<JobCenterPage />);
    await screen.findByRole('progressbar', { name: 'Fortschritt von ci.merge' });
    const offered = (id: string) =>
      within(revertRow(id)).queryByRole('button', { name: 'Rückgängig' }) !== null;
    await waitFor(() => expect(offered('j-bulk')).toBe(true));
    expect(offered('j-asset')).toBe(true);
    // ci.merge: 30 days (CI-12).
    expect(offered('j-merge')).toBe(true);
    // revertible_until of the job wins over finished_at plus the window.
    expect(offered('j-win')).toBe(true);
    // 24 hours over, reverted already, the revert job itself, failed, no revert kind.
    expect(offered('j-old')).toBe(false);
    expect(offered('j-rev')).toBe(false);
    expect(revertRow('j-rev')).toHaveTextContent('Rückgängig gemacht');
    expect(offered('j-undo')).toBe(false);
    expect(offered('j-fail')).toBe(false);
    expect(offered('job-2')).toBe(false);
  });

  it('offers no revert without the right of the kind to another user', async () => {
    stubServer({
      jobs: [{ ...bulk, created_by: OTHER }],
      permissions: ['job:read'],
    });
    renderWithProviders(<JobCenterPage />);
    await screen.findByRole('progressbar', { name: 'Fortschritt von ci.bulk' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Ergebnis' })).toBeVisible());
    expect(screen.queryByRole('button', { name: 'Rückgängig' })).toBeNull();
  });

  it('reverts a merge (200) and a bulk (202 with the revert job)', async () => {
    const posted: string[] = [];
    stubServer({
      jobs: [bulk, merge],
      permissions: ['job:read', 'ci:write', 'ci:delete'],
      revert: (id) => {
        posted.push(id);
        return id === 'j-merge'
          ? json({ job_id: id, reverted: true, reverted_at: finishedAt(0), reverted_by: ME })
          : json({ ...succeeded('j-new', 'bulk.revert'), status: 'queued' }, 202);
      },
    });
    renderWithProviders(<JobCenterPage />);
    await screen.findByRole('progressbar', { name: 'Fortschritt von ci.merge' });

    fireEvent.click(
      await within(revertRow('j-merge')).findByRole('button', { name: 'Rückgängig' }),
    );
    expect(await screen.findByText('Der Job wurde rückgängig gemacht.')).toBeInTheDocument();

    fireEvent.click(within(revertRow('j-bulk')).getByRole('button', { name: 'Rückgängig' }));
    expect(
      await screen.findByText(
        'Das Rückgängigmachen läuft als Job Rückgängig einer Massenänderung.',
      ),
    ).toBeInTheDocument();
    expect(posted).toEqual(['j-merge', 'j-bulk']);
  });

  it('shows the 409 of a second revert as a message', async () => {
    stubServer({
      jobs: [bulk],
      revert: () => problem(409, 'the job is already reverted'),
    });
    renderWithProviders(<JobCenterPage />);
    fireEvent.click(await screen.findByRole('button', { name: 'Rückgängig' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(
      'Der Job lässt sich nicht rückgängig machen: the job is already reverted',
    );
    expect(screen.queryByText('Der Job wurde rückgängig gemacht.')).toBeNull();
  });

  it('shows the 403 of a revert as a message', async () => {
    stubServer({ jobs: [bulk], revert: () => problem(403, 'forbidden') });
    renderWithProviders(<JobCenterPage />);
    fireEvent.click(await screen.findByRole('button', { name: 'Rückgängig' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Sie dürfen diesen Job nicht rückgängig machen.',
    );
  });

  it('shows the results of the bulk kinds as a table and other kinds as JSON', async () => {
    stubServer({ jobs: [assetBulk, revertJob, done] });
    renderWithProviders(<JobCenterPage />);
    await screen.findByRole('progressbar', { name: 'Fortschritt von asset.bulk' });

    fireEvent.click(within(revertRow('j-asset')).getByRole('button', { name: 'Ergebnis' }));
    let dialog = await screen.findByRole('dialog', { name: 'Ergebnis' });
    expect(
      within(dialog).getByText('1 von 2 erfolgreich, 1 fehlgeschlagen, 0 übersprungen'),
    ).toBeInTheDocument();
    const table = within(dialog).getByRole('table', { name: 'Bericht je Asset' });
    expect(within(table).getByRole('row', { name: /ci-b/ })).toHaveTextContent('404');
    expect(within(table).getByRole('row', { name: /ci-b/ })).toHaveTextContent('CI not found');
    fireEvent.keyDown(dialog, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    fireEvent.click(within(revertRow('j-undo')).getByRole('button', { name: 'Ergebnis' }));
    dialog = await screen.findByRole('dialog', { name: 'Ergebnis' });
    expect(
      within(dialog).getByText('2 von 2 erfolgreich, 0 fehlgeschlagen, 0 übersprungen'),
    ).toBeInTheDocument();
    fireEvent.keyDown(dialog, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    fireEvent.click(within(revertRow('job-2')).getByRole('button', { name: 'Ergebnis' }));
    dialog = await screen.findByRole('dialog', { name: 'Ergebnis' });
    expect(within(dialog).getByText(/"name": "srv-01"/)).toBeInTheDocument();
  });
});
