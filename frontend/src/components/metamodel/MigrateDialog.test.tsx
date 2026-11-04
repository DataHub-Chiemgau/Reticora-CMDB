/**
 * WP-800 (UI-11 [P2], ABN-01 (c)): the template migration as a dialog.
 *
 *   Versions   source published or retired, target only published
 *   Selection  all CIs, a saved CI view by its filter_spec.where (sent as
 *              where; views without where cannot be chosen) or CI ids
 *   Run        the dry run first (dry_run: true) with its report per CI:
 *              defaults, fields an override keeps, findings, the error
 *              unique and the totals; then the start with dry_run: false
 *   Job        progress over useJob (GET /jobs/{id}), the report over
 *              GET /jobs/{id}/result, a link to /jobs
 *   Refusal    a 422 as problem detail with the trace id
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/metamodel/MigrateDialog.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { useAuthStore } from '../../auth/authStore';
import { MigrateDialog } from './MigrateDialog';

type Json = Record<string, unknown>;

interface Call {
  method: string;
  path: string;
  body: Json | undefined;
  headers: Headers;
}

const AT = '2026-01-01T00:00:00Z';
const TEMPLATE = { id: 'tp-1', key: 'router_base', display_name: { de: 'Routerbasis' } };
const CI_A = '11111111-1111-4111-8111-111111111111';
const CI_B = '22222222-2222-4222-8222-222222222222';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function version(n: number, status: string): Json {
  return {
    id: `v-${n}`,
    template_id: 'tp-1',
    version: n,
    status,
    created_at: AT,
    updated_at: AT,
    items: [],
  };
}

const WHERE = { field: 'status', op: 'eq', value: 'active' };
const VIEWS = [
  {
    id: 'sv-where',
    name: 'Aktive Router',
    entity_kind: 'ci',
    filter_spec: { where: WHERE },
    shared: false,
    created_at: AT,
  },
  {
    id: 'sv-plain',
    name: 'Ohne Filter',
    entity_kind: 'ci',
    filter_spec: { query: 'router' },
    shared: false,
    created_at: AT,
  },
  {
    id: 'sv-asset',
    name: 'Assets',
    entity_kind: 'asset',
    filter_spec: { where: WHERE },
    shared: false,
    created_at: AT,
  },
];

const REPORT = {
  dry_run: true,
  totals: { selected: 3, migrated: 1, skipped: 1, failed: 1, with_findings: 1 },
  items: [
    {
      ci_id: CI_A,
      outcome: 'migrated',
      defaults: ['ports'],
      overridden: ['hostname'],
      findings: [{ field: 'role', detail: 'value is required' }],
    },
    {
      ci_id: CI_B,
      outcome: 'failed',
      defaults: [],
      overridden: [],
      findings: [],
      error: { code: 'unique', field: 'serial', detail: 'value already used' },
    },
  ],
  truncated: true,
};

function job(id: string, status: string, progress: number): Json {
  return {
    id,
    kind: 'template.migrate',
    status,
    progress,
    attempts: 1,
    max_attempts: 3,
    created_at: AT,
  };
}

interface Backend {
  migrate?: (body: Json) => Response;
  /** The states GET /jobs/{id} answers one after another (the last repeats). */
  jobStates?: [string, number][];
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  let jobRead = 0;
  let jobCount = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({ method, path, body, headers: new Headers(init?.headers) });
      if (path === '/me/permissions') {
        return json({ user_id: 'u1', permissions: ['citype:manage'] });
      }
      if (path === '/templates/tp-1/versions') {
        return json({
          data: [
            version(4, 'draft'),
            version(3, 'published'),
            version(2, 'retired'),
            version(1, 'retired'),
          ],
          has_more: false,
        });
      }
      if (path === '/saved-views') {
        return json({ data: VIEWS, total: VIEWS.length, limit: 100, offset: 0, has_more: false });
      }
      if (path === '/templates/tp-1/migrations' && method === 'POST') {
        if (b.migrate) return b.migrate(body!);
        jobCount += 1;
        return json({ job_id: `job-${jobCount}`, status: 'queued', kind: 'template.migrate' }, 202);
      }
      const result = /^\/jobs\/([^/]+)\/result$/.exec(path);
      if (result) {
        return json({ ...REPORT, dry_run: result[1] === 'job-1' });
      }
      const one = /^\/jobs\/([^/]+)$/.exec(path);
      if (one) {
        const states = b.jobStates ?? [['succeeded', 100]];
        const [status, progress] = states[Math.min(jobRead, states.length - 1)]!;
        jobRead += 1;
        return json(job(one[1]!, status, progress));
      }
      return json({ data: [] });
    }),
  );
  return calls;
}

function renderDialog(props: Partial<Parameters<typeof MigrateDialog>[0]> = {}) {
  const onClose = vi.fn();
  renderWithProviders(<MigrateDialog open template={TEMPLATE} onClose={onClose} {...props} />);
  return onClose;
}

const migrations = (calls: Call[]) =>
  calls.filter((c) => c.method === 'POST' && c.path === '/templates/tp-1/migrations');

beforeEach(() => {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
    user: { sub: 'u1' },
  });
});

afterEach(() => {
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('MigrateDialog', () => {
  it('offers published and retired source versions and only published target versions', async () => {
    stubBackend();
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Template migrieren: Routerbasis' });
    await waitFor(() => expect(within(dialog).getAllByRole('option').length).toBeGreaterThan(2));
    const options = (label: string) =>
      within(within(dialog).getByLabelText(label) as HTMLElement)
        .getAllByRole('option')
        .map((o) => o.textContent);
    expect(options('Ausgangsversion')).toEqual([
      'v3 (Veröffentlicht)',
      'v2 (Zurückgezogen)',
      'v1 (Zurückgezogen)',
    ]);
    expect(options('Zielversion')).toEqual(['v3 (Veröffentlicht)']);
  });

  it('sends the where of a saved CI view; views without where cannot be chosen', async () => {
    const calls = stubBackend();
    renderDialog();
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByLabelText('Eingeschränkt durch eine gespeicherte Sicht'));
    const withWhere = await within(dialog).findByRole('radio', { name: /Aktive Router/ });
    const plain = within(dialog).getByRole('radio', { name: /Ohne Filter/ });
    expect(plain).toBeDisabled();
    expect(withWhere).toBeEnabled();
    // A view of assets is no CI view.
    expect(within(dialog).queryByRole('radio', { name: /Assets/ })).not.toBeInTheDocument();
    fireEvent.click(withWhere);
    await waitFor(() => expect(within(dialog).getByLabelText('Zielversion')).toHaveValue('3'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Probelauf starten' }));
    await waitFor(() => expect(migrations(calls)).toHaveLength(1));
    expect(migrations(calls)[0]!.body).toEqual({
      from_version: 2,
      to_version: 3,
      where: WHERE,
      dry_run: true,
    });
  });

  it('runs the dry run first, shows its report per CI and then starts with dry_run false', async () => {
    const calls = stubBackend({
      jobStates: [
        ['running', 40],
        ['succeeded', 100],
      ],
    });
    renderDialog({ ciIds: [CI_A], fromVersion: 2, toVersion: 3 });
    const dialog = await screen.findByRole('dialog');
    expect(
      within(dialog).getByText('Migriert werden nur die gewählten CIs (1).'),
    ).toBeInTheDocument();
    await waitFor(() => expect(within(dialog).getByLabelText('Zielversion')).toHaveValue('3'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Probelauf starten' }));
    await waitFor(() => expect(migrations(calls)).toHaveLength(1));
    expect(migrations(calls)[0]!.body).toEqual({
      from_version: 2,
      to_version: 3,
      ci_ids: [CI_A],
      dry_run: true,
    });

    // Progress over useJob while the job runs, the link to the job center.
    const bar = await within(dialog).findByRole('progressbar', {
      name: 'Fortschritt von Template-Migration',
    });
    expect(bar).toHaveAttribute('aria-valuenow', '40');
    expect(within(dialog).getByRole('link', { name: 'Zum Jobcenter' })).toHaveAttribute(
      'href',
      '/jobs',
    );

    // The report of the dry run.
    await within(dialog).findByText('Bericht des Probelaufs', undefined, { timeout: 4000 });
    expect(calls.some((c) => c.path === '/jobs/job-1/result')).toBe(true);
    const total = (key: string) => dialog.querySelector(`[data-total="${key}"]`)?.textContent;
    expect(total('selected')).toBe('3');
    expect(total('failed')).toBe('1');
    expect(total('with_findings')).toBe('1');
    const rowA = dialog.querySelector(`[data-ci-id="${CI_A}"]`) as HTMLElement;
    expect(within(rowA).getByText('Migriert')).toBeInTheDocument();
    expect(within(rowA).getByText('ports')).toBeInTheDocument();
    expect(within(rowA).getByText('hostname')).toBeInTheDocument();
    expect(within(rowA).getByText('role: value is required')).toBeInTheDocument();
    const rowB = dialog.querySelector(`[data-ci-id="${CI_B}"]`) as HTMLElement;
    expect(
      within(rowB).getByText('Eindeutigkeit verletzt (serial): value already used'),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/Der Bericht ist gekürzt/)).toBeInTheDocument();

    // Then the start.
    const sections = within(dialog).getByRole('region', { name: 'Lauf der Migration' });
    fireEvent.click(within(sections).getByRole('button', { name: 'Migration starten' }));
    await waitFor(() => expect(migrations(calls)).toHaveLength(2));
    expect(migrations(calls)[1]!.body).toEqual({
      from_version: 2,
      to_version: 3,
      ci_ids: [CI_A],
      dry_run: false,
    });
    await within(dialog).findByText('Bericht der Migration');
  });

  it('starts directly without the dry run when it is switched off', async () => {
    const calls = stubBackend();
    renderDialog();
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByLabelText('Bestimmte CIs (IDs)'));
    fireEvent.change(within(dialog).getByLabelText(/CI-IDs/), {
      target: { value: `${CI_A}\n${CI_B}, ${CI_A}` },
    });
    fireEvent.click(within(dialog).getByLabelText('Probelauf (schreibt nichts)'));
    await waitFor(() => expect(within(dialog).getByLabelText('Zielversion')).toHaveValue('3'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Migration starten' }));
    await waitFor(() => expect(migrations(calls)).toHaveLength(1));
    expect(migrations(calls)[0]!.body).toMatchObject({ ci_ids: [CI_A, CI_B], dry_run: false });
  });

  it('refuses an invalid CI id before sending', async () => {
    const calls = stubBackend();
    renderDialog();
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByLabelText('Bestimmte CIs (IDs)'));
    fireEvent.change(within(dialog).getByLabelText(/CI-IDs/), { target: { value: 'nope' } });
    await waitFor(() => expect(within(dialog).getByLabelText('Zielversion')).toHaveValue('3'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Probelauf starten' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Keine gültige CI-ID: nope');
    expect(migrations(calls)).toHaveLength(0);
  });

  it('shows a 422 as problem detail with the trace id', async () => {
    stubBackend({
      migrate: () =>
        new Response(
          JSON.stringify({
            status: 422,
            title: 'Unprocessable Entity',
            detail: 'to_version is not published',
            trace_id: 'trace-422',
          }),
          { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
        ),
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog');
    await waitFor(() => expect(within(dialog).getByLabelText('Zielversion')).toHaveValue('3'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Probelauf starten' }));
    const alert = await within(dialog).findByRole('alert');
    expect(alert).toHaveTextContent('Die Migration wurde abgelehnt');
    expect(alert).toHaveTextContent('Fehler 422: to_version is not published');
    expect(alert).toHaveTextContent('Trace-ID: trace-422');
  });
});
