/**
 * Tests for the bulk bar (UI-14, BLK-02, WP-816): the request body of every
 * operation at POST /cis/bulk and POST /assets/bulk, the selection by a
 * filter resolved over POST /search/query up to 500 ids, the synchronous
 * partial failure report, the job with progress and its report from
 * /jobs/{id}/result, and "Rückgängig" over POST /jobs/{id}/revert.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { useState } from 'react';
import { renderWithProviders } from '../../test/utils';
import { ToastViewport } from '../ui/Toast';
import { useToastStore } from '../../stores/toast';
import {
  BULK_OPERATIONS,
  BulkBar,
  EMPTY_BULK_PARAMS,
  buildBulkRequest,
  bulkReportOf,
  resolveFilter,
  sendBulk,
  type BulkEntity,
  type BulkOperation,
  type BulkParams,
  type CollectedMatches,
} from './BulkBar';

interface Call {
  method: string;
  path: string;
  url: URL;
  body: unknown;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, detail: string) {
  return new Response(JSON.stringify({ title: 'Conflict', status, detail }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

/** stub answers through `answer`; an unanswered request is 404 and fails the test's checks. */
function stub(answer: (call: Call) => Response | undefined) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://localhost');
      const call: Call = {
        method: (init?.method ?? 'GET').toUpperCase(),
        path: url.pathname.replace('/api/v1', ''),
        url,
        body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
      };
      calls.push(call);
      return answer(call) ?? json({ detail: `unexpected request: ${call.path}` }, 404);
    }),
  );
  return calls;
}

const names = new Map([
  ['ci-1', 'srv-01'],
  ['ci-2', 'srv-02'],
  ['ci-3', 'srv-03'],
]);

function Harness({
  entity = 'ci',
  initial = ['ci-1', 'ci-2', 'ci-3'],
  total = 3,
  collect = async () => ({ items: [], total: 0 }),
}: {
  entity?: BulkEntity;
  initial?: string[];
  total?: number;
  collect?: (max: number) => Promise<CollectedMatches>;
}) {
  const [ids, setIds] = useState(initial);
  return (
    <>
      <span data-testid="selection">{ids.length}</span>
      <BulkBar
        entity={entity}
        selectedIds={ids}
        onSelectionChange={setIds}
        total={total}
        collectMatches={collect}
        filterKey="k"
        names={names}
        onChanged={() => {}}
      />
      <ToastViewport />
    </>
  );
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  useToastStore.setState({ toasts: [] });
});

const ids = ['a-1', 'a-2'];
const params = (extra: Partial<BulkParams>): BulkParams => ({ ...EMPTY_BULK_PARAMS, ...extra });

describe('BulkBar request contract', () => {
  const cases: [BulkEntity, BulkOperation, Partial<BulkParams>, string, unknown][] = [
    [
      'ci',
      'set_status',
      { status: 'maintenance' },
      '/cis/bulk',
      { action: 'set_status', ids, status: 'maintenance' },
    ],
    [
      'asset',
      'set_status',
      { status: 'retired' },
      '/assets/bulk',
      { action: 'set_status', ids, status: 'retired' },
    ],
    [
      'ci',
      'set_fields',
      { field: 'manufacturer', value: ' Acme ' },
      '/cis/bulk',
      { action: 'set_fields', ids, params: { fields: { manufacturer: 'Acme' } } },
    ],
    [
      'asset',
      'set_fields',
      { field: 'cost_center', value: '' },
      '/assets/bulk',
      { action: 'set_fields', ids, params: { fields: { cost_center: null } } },
    ],
    [
      'ci',
      'set_location',
      { location: { id: 'loc-1', name: 'R1', kind: 'room', path: [] } },
      '/cis/bulk',
      { action: 'set_location', ids, params: { location_id: 'loc-1' } },
    ],
    [
      'asset',
      'set_location',
      { location: { id: 'loc-1', name: 'R1', kind: 'room', path: [] } },
      '/assets/bulk',
      { action: 'set_location', ids, params: { to_location_id: 'loc-1' } },
    ],
    [
      'ci',
      'add_tags',
      { tags: ['prod'] },
      '/cis/bulk',
      { action: 'add_tags', ids, params: { tags: ['prod'] } },
    ],
    [
      'asset',
      'remove_tags',
      { tags: ['prod', 'eol'] },
      '/assets/bulk',
      { action: 'remove_tags', ids, params: { tags: ['prod', 'eol'] } },
    ],
    [
      'ci',
      'change_template',
      { templateId: 'tpl-1', toVersion: '3' },
      '/cis/bulk',
      { action: 'change_template', ids, params: { template_id: 'tpl-1', to_version: 3 } },
    ],
    [
      'ci',
      'change_template',
      { templateId: 'tpl-1' },
      '/cis/bulk',
      { action: 'change_template', ids, params: { template_id: 'tpl-1' } },
    ],
    [
      'ci',
      'add_relationship',
      { relType: 'runs_on', target: [{ kind: 'ci', id: 'ci-9', name: 'host' }] },
      '/cis/bulk',
      {
        action: 'add_relationship',
        ids,
        params: { rel_type: 'runs_on', target_ci_id: 'ci-9', direction: 'outgoing' },
      },
    ],
    [
      'ci',
      'remove_relationship',
      {
        relType: 'runs_on',
        direction: 'incoming',
        target: [{ kind: 'ci', id: 'ci-9', name: 'host' }],
      },
      '/cis/bulk',
      {
        action: 'remove_relationship',
        ids,
        params: { rel_type: 'runs_on', target_ci_id: 'ci-9', direction: 'incoming' },
      },
    ],
    [
      'ci',
      'assign',
      { holder: { kind: 'team', id: 'team-1', name: 'Ops' } },
      '/cis/bulk',
      { action: 'set_owner', ids, params: { owner_team_id: 'team-1' } },
    ],
    [
      'asset',
      'assign',
      { holder: { kind: 'user', id: 'user-1', name: 'Eva' } },
      '/assets/bulk',
      { action: 'assign', ids, params: { assigned_user_id: 'user-1' } },
    ],
    [
      'ci',
      'lifecycle_transition',
      { toState: 'retired', reason: ' EOL ' },
      '/cis/bulk',
      { action: 'lifecycle_transition', ids, params: { to_state: 'retired', reason: 'EOL' } },
    ],
    [
      'asset',
      'lifecycle_transition',
      { toState: 'repair' },
      '/assets/bulk',
      { action: 'lifecycle_transition', ids, params: { to_state: 'repair' } },
    ],
    [
      'ci',
      'remove_tags',
      { tags: ['eol'] },
      '/cis/bulk',
      { action: 'remove_tags', ids, params: { tags: ['eol'] } },
    ],
    [
      'asset',
      'add_tags',
      { tags: ['prod'] },
      '/assets/bulk',
      { action: 'add_tags', ids, params: { tags: ['prod'] } },
    ],
    ['ci', 'archive', {}, '/cis/bulk', { action: 'archive', ids }],
    ['asset', 'archive', {}, '/assets/bulk', { action: 'archive', ids }],
    [
      'ci',
      'export',
      { format: 'json' },
      '/cis/bulk',
      { action: 'export', ids, params: { format: 'json' } },
    ],
    ['asset', 'export', {}, '/assets/bulk', { action: 'export', ids, params: { format: 'csv' } }],
  ];

  it.each(cases)('%s %s posts its body to its endpoint', async (entity, op, extra, path, body) => {
    const calls = stub(() => json({ action: op, total: 2, succeeded: 2, failed: 0, items: [] }));
    await sendBulk(entity, buildBulkRequest(entity, op, ids, params(extra)));
    expect(calls).toHaveLength(1);
    expect(calls[0]!.method).toBe('POST');
    expect(calls[0]!.path).toBe(path);
    expect(calls[0]!.body).toEqual(body);
  });

  it('covers every operation of BLK-02 and offers template and relationships for CIs only', () => {
    const covered = new Set(cases.map(([entity, op]) => `${entity}:${op}`));
    for (const entity of ['ci', 'asset'] as const) {
      for (const op of BULK_OPERATIONS[entity]) expect(covered).toContain(`${entity}:${op}`);
    }
    expect(BULK_OPERATIONS.asset).not.toContain('change_template');
    expect(BULK_OPERATIONS.asset).not.toContain('add_relationship');
  });

  it('applies an operation chosen in the bar with the tags of the catalog', async () => {
    const calls = stub((call) => {
      if (call.path === '/tags') {
        return json({
          data: [{ id: 't-1', key: 'prod', display_name: { de: 'Produktion' } }],
          has_more: false,
        });
      }
      if (call.path === '/cis/bulk') {
        return json({
          action: 'add_tags',
          total: 3,
          succeeded: 3,
          failed: 0,
          bulk_job_id: 'job-s',
          items: [],
        });
      }
      return undefined;
    });
    renderWithProviders(<Harness />);
    const toolbar = screen.getByRole('toolbar', { name: 'Massenbearbeitung' });
    expect(toolbar).toHaveTextContent('3 ausgewählt');
    const operation = within(toolbar).getByRole('combobox', { name: 'Aktion' });
    expect(within(operation).getAllByRole('option')).toHaveLength(BULK_OPERATIONS.ci.length);
    fireEvent.change(operation, { target: { value: 'add_tags' } });
    const apply = within(toolbar).getByRole('button', { name: 'Anwenden' });
    expect(apply).toBeDisabled();
    fireEvent.click(await within(toolbar).findByRole('checkbox', { name: 'Produktion' }));
    fireEvent.click(apply);
    expect(await screen.findByText('3 CIs geändert')).toBeInTheDocument();
    expect(calls.find((c) => c.path === '/cis/bulk')?.body).toEqual({
      action: 'add_tags',
      ids: ['ci-1', 'ci-2', 'ci-3'],
      params: { tags: ['prod'] },
    });
    // The selection was consumed by the change.
    expect(screen.getByTestId('selection')).toHaveTextContent('0');
  });
});

describe('BulkBar selection by filter', () => {
  function rows(count: number) {
    return Array.from({ length: count }, (_, i) => ({
      id: `m-${String(i + 1).padStart(3, '0')}`,
      entity_kind: 'ci',
      name: `match-${i + 1}`,
    }));
  }

  function searchServer(all: ReturnType<typeof rows>) {
    return stub((call) => {
      if (call.path === '/attribute-definitions') return json({ data: [], has_more: false });
      if (call.path === '/relationship-types')
        return json({ data: [], total: 0, limit: 100, offset: 0, has_more: false });
      if (call.path === '/search/query' && call.method === 'POST') {
        const limit = Number(call.url.searchParams.get('limit'));
        const offset = Number(call.url.searchParams.get('offset') ?? 0);
        return json({
          data: all.slice(offset, offset + limit),
          total: all.length,
          limit,
          offset,
          has_more: offset + limit < all.length,
        });
      }
      return undefined;
    });
  }

  it('resolves the ids page by page with limit 200 up to 500', async () => {
    const calls = searchServer(rows(620));
    const where = { field: 'name', op: 'like', value: 'match-*' };
    const resolved = await resolveFilter('ci', where);
    expect(resolved.items).toHaveLength(500);
    expect(resolved.total).toBe(620);
    const queries = calls.filter((c) => c.path === '/search/query');
    expect(queries.map((c) => c.url.searchParams.get('offset'))).toEqual(['0', '200', '400']);
    expect(queries.every((c) => c.url.searchParams.get('limit') === '200')).toBe(true);
    expect(queries[0]!.body).toEqual({ entity_kind: 'ci', where });
  });

  it('selects the matches of a filter of the click builder and says when it has more than 500', async () => {
    const calls = searchServer(rows(620));
    renderWithProviders(<Harness initial={[]} total={10} />);
    expect(screen.queryByRole('toolbar', { name: 'Massenbearbeitung' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Auswahl per Filter…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Auswahl per Filter' });
    const resolve = within(dialog).getByRole('button', { name: 'Treffer auswählen' });
    // An empty filter selects nothing.
    expect(resolve).toBeDisabled();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Bedingung hinzufügen' }));
    fireEvent.change(within(dialog).getByLabelText('Feld'), { target: { value: 'name' } });
    fireEvent.change(within(dialog).getByLabelText('Wert'), { target: { value: 'match-*' } });
    fireEvent.change(within(dialog).getByLabelText('Operator'), { target: { value: 'like' } });
    await waitFor(() => expect(resolve).toBeEnabled());
    fireEvent.click(resolve);

    const toolbar = await screen.findByRole('toolbar', { name: 'Massenbearbeitung' });
    expect(toolbar).toHaveTextContent('500 ausgewählt');
    expect(toolbar).toHaveTextContent(
      'Der Filter hat 620 Treffer; eine Massenänderung umfasst höchstens 500, die ersten 500 sind ausgewählt. Schränken Sie den Filter ein oder nutzen Sie den Export.',
    );
    expect(toolbar).toHaveTextContent('Ab 100 CIs läuft die Änderung als Job');
    const first = calls.find((c) => c.path === '/search/query');
    expect(first?.body).toEqual({
      entity_kind: 'ci',
      where: { and: [{ field: 'name', op: 'like', value: 'match-*' }] },
    });
  });
});

describe('BulkBar reports and undo', () => {
  function setStatus() {
    const toolbar = screen.getByRole('toolbar', { name: 'Massenbearbeitung' });
    fireEvent.change(within(toolbar).getByRole('combobox', { name: 'Status setzen…' }), {
      target: { value: 'inactive' },
    });
    fireEvent.click(within(toolbar).getByRole('button', { name: 'Anwenden' }));
  }

  it('shows the synchronous partial failure per id and offers the undo of its job', async () => {
    const calls = stub((call) => {
      if (call.path === '/cis/bulk') {
        return json({
          action: 'set_status',
          total: 3,
          succeeded: 1,
          failed: 2,
          bulk_job_id: 'job-sync',
          items: [
            { id: 'ci-1', result: 'ok', code: 200 },
            { id: 'ci-2', result: 'error', code: 404, error: 'CI not found' },
            { id: 'ci-3', result: 'error', code: 422, error: 'status: invalid' },
          ],
        });
      }
      if (call.path === '/jobs/job-sync/revert') {
        return json(
          {
            id: 'rev-1',
            kind: 'bulk.revert',
            status: 'queued',
            progress: 0,
            attempts: 0,
            max_attempts: 1,
            created_at: '2026-01-01T00:00:00Z',
          },
          202,
        );
      }
      if (call.path === '/jobs/rev-1') {
        return json({
          id: 'rev-1',
          kind: 'bulk.revert',
          status: 'succeeded',
          progress: 100,
          attempts: 1,
          max_attempts: 1,
          created_at: '2026-01-01T00:00:00Z',
          result: {
            bulk_job_id: 'job-sync',
            total: 1,
            reverted: 1,
            skipped: 0,
            failed: 0,
            items: [{ id: 'ci-1', result: 'reverted', code: 200 }],
          },
        });
      }
      return undefined;
    });
    renderWithProviders(<Harness />);
    setStatus();

    const failures = await screen.findByRole('list', { name: 'Fehlgeschlagene CIs' });
    expect(within(failures).getAllByRole('listitem')).toHaveLength(2);
    expect(failures).toHaveTextContent('srv-02: CI not found');
    expect(failures).toHaveTextContent('srv-03: status: invalid');
    expect(await screen.findByText('1 CIs aktualisiert, 2 fehlgeschlagen')).toBeInTheDocument();

    // The toast offers the undo of the bulk job; 202 starts the revert job.
    fireEvent.click(screen.getByRole('button', { name: 'Rückgängig' }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path === '/jobs/job-sync/revert')).toBe(
        true,
      ),
    );
    expect(await screen.findByText('Das Rückgängigmachen läuft als Job.')).toBeInTheDocument();
    expect(
      await screen.findByText(
        'Rückgängig abgeschlossen: 1 von 1 zurückgesetzt, 0 übersprungen, 0 fehlgeschlagen',
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole('row', { name: /srv-01/ })).toHaveTextContent('Zurückgesetzt');
  });

  it('shows the 409 of the undo as a message', async () => {
    stub((call) => {
      if (call.path === '/cis/bulk') {
        return json({
          action: 'set_status',
          total: 3,
          succeeded: 3,
          failed: 0,
          bulk_job_id: 'job-sync',
          items: [],
        });
      }
      if (call.path === '/jobs/job-sync/revert') return problem(409, 'the job is already reverted');
      return undefined;
    });
    renderWithProviders(<Harness />);
    setStatus();
    expect(await screen.findByText('Status von 3 CIs aktualisiert')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Rückgängig' }));
    expect(
      await screen.findByText('Rückgängig nicht möglich: the job is already reverted'),
    ).toBeInTheDocument();
  });

  it('follows a job with its progress, shows the report of /jobs/{id}/result and offers its undo', async () => {
    const job = (status: string, progress: number) => ({
      id: 'job-a',
      kind: 'asset.bulk',
      status,
      progress,
      attempts: 1,
      max_attempts: 1,
      created_at: '2026-01-01T00:00:00Z',
    });
    const reads = [job('running', 30), job('succeeded', 100)];
    const calls = stub((call) => {
      if (call.path === '/assets/bulk')
        return json({ job_id: 'job-a', status: 'queued', kind: 'asset.bulk' }, 202);
      if (call.path === '/jobs/job-a') return json(reads.length > 1 ? reads.shift() : reads[0]);
      if (call.path === '/jobs/job-a/result') {
        return json({
          action: 'set_status',
          total: 2,
          succeeded: 1,
          failed: 1,
          items: [
            { id: 'ci-1', result: 'ok', code: 200 },
            { id: 'ci-2', result: 'error', code: 409, error: 'lifecycle guard' },
          ],
        });
      }
      if (call.path === '/jobs/job-a/revert') return problem(409, 'revert-expired');
      return undefined;
    });
    renderWithProviders(<Harness entity="asset" initial={['ci-1', 'ci-2']} />);
    const toolbar = screen.getByRole('toolbar', { name: 'Massenbearbeitung' });
    fireEvent.change(within(toolbar).getByRole('combobox', { name: 'Status setzen…' }), {
      target: { value: 'retired' },
    });
    fireEvent.click(within(toolbar).getByRole('button', { name: 'Anwenden' }));

    const progress = await screen.findByRole('progressbar', {
      name: 'Fortschritt der Massenänderung',
    });
    expect(progress).toHaveAttribute('aria-valuemax', '100');
    expect(
      await screen.findByText('Statusänderung für 2 Assets läuft als Job'),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(
        'Job abgeschlossen: 1 von 2 Assets aktualisiert, 1 fehlgeschlagen',
        undefined,
        { timeout: 4000 },
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole('list', { name: 'Fehlgeschlagene Assets' })).toHaveTextContent(
      'srv-02: lifecycle guard',
    );
    expect(
      within(screen.getByRole('table', { name: 'Bericht je Asset' })).getAllByRole('row'),
    ).toHaveLength(3);
    expect(calls.some((c) => c.path === '/jobs/job-a/result')).toBe(true);

    fireEvent.click(await screen.findByRole('button', { name: 'Rückgängig' }));
    expect(await screen.findByText('Rückgängig nicht möglich: revert-expired')).toBeInTheDocument();
  }, 15_000);

  it('reads the report of a revert job like that of a bulk', () => {
    expect(bulkReportOf({ total: 4, reverted: 3, skipped: 1, failed: 0, items: [] })).toEqual({
      total: 4,
      succeeded: 3,
      failed: 0,
      skipped: 1,
      cancelled: false,
      items: [],
    });
    expect(bulkReportOf({ state: {} })).toBeUndefined();
  });
});
