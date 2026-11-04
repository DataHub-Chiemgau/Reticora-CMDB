/**
 * WP-799 (UI-11, MET-53 [P2]): the work area of the metamodel drafts.
 *
 *   List      GET /metamodel/drafts over next_cursor, status filter in the
 *             page; create (POST name), discard (DELETE with If-Match),
 *             choose the active draft (store of WP-798)
 *   Patch     the items grouped by kind, each a MetamodelDiff: update
 *             against the current target, create only the new values,
 *             delete the removed object; removing an item sends If-Match,
 *             a 412 reads the draft again; published drafts are read-only
 *   Test      only with metamodel:publish; POST …/test, useJob follows the
 *             job until succeeded, the report links /cmdb/{id}; without
 *             the right only the counters of the draft
 *   Publish   only from tested and after a confirmation with the summary;
 *             the new schema versions; the active draft is reset; 409 with
 *             stale items lists them, a unique 409 and a 403 show the
 *             problem detail with trace_id
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/MetamodelDraftsPage.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import { renderWithProviders } from '../test/utils';
import { useAuthStore } from '../auth/authStore';
import { useMetamodelDraftStore } from '../stores/metamodelDraft';
import type { MetamodelDraftItem } from '../api/cmdb';
import { MetamodelDraftsPage, diffSides } from './MetamodelDraftsPage';
import { DraftBanner } from '../components/metamodel/DraftBanner';

type Json = Record<string, unknown>;

interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
  headers: Headers;
  body: Json | undefined;
}

const AT = '2026-11-01T10:00:00Z';
const D_OPEN = '11111111-1111-4111-8111-111111111111';
const D_TESTED = '22222222-2222-4222-8222-222222222222';
const D_DONE = '33333333-3333-4333-8333-333333333333';
const D_NEW = '44444444-4444-4444-8444-444444444444';
const TYPE = '55555555-5555-4555-8555-555555555555';
const TEMPLATE = '66666666-6666-4666-8666-666666666666';
const CI = '77777777-7777-4777-8777-777777777777';
const JOB = '88888888-8888-4888-8888-888888888888';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, body: Json) {
  return new Response(JSON.stringify({ status, title: 'Problem', ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function item(id: string, kind: string, op: string, extra: Json = {}): MetamodelDraftItem {
  return {
    id,
    kind,
    op,
    target_id: null,
    target_key: null,
    payload: {},
    base: null,
    schema_versions: {},
    route: 'POST /api/v1/ci-types',
    created_by: null,
    created_at: AT,
    ...extra,
  } as MetamodelDraftItem;
}

const ITEMS = [
  item('i-type', 'ci_type', 'update', {
    target_id: TYPE,
    payload: { name: 'Router neu', description: 'Kernrouter' },
  }),
  item('i-attr', 'attribute_definition', 'create', {
    payload: { key: 'rack_unit', data_type: 'integer', ci_type_id: TYPE },
  }),
  item('i-tpl', 'template', 'delete', { target_id: TEMPLATE }),
];

const REPORT = {
  job_id: JOB,
  state: 'completed',
  draft_version: 3,
  started_at: AT,
  finished_at: AT,
  blocked: false,
  outdated: false,
  conflicts: [],
  totals: { types: 1, cis: 12, invalid_cis: 2, violations: 3 },
  types: [
    {
      ci_type_id: TYPE,
      cis: 12,
      invalid_cis: 2,
      violations: 3,
      attributes: [{ key: 'rack_unit', violations: 3 }],
    },
  ],
  truncated: false,
};

function draft(id: string, name: string, extra: Json = {}): Json {
  return {
    id,
    organization_id: 'org-1',
    name,
    status: 'draft',
    item_count: ITEMS.length,
    version: 3,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

interface Backend {
  permissions?: string[];
  drafts?: Json[];
  removeItem?: () => Response;
  publish?: () => Response;
  test?: () => Response;
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  let drafts: Json[] = b.drafts ?? [
    draft(D_OPEN, 'Rack-Felder'),
    draft(D_TESTED, 'Getestet', { status: 'tested', test_report: REPORT }),
    draft(D_DONE, 'Alt', { status: 'published', published_at: AT }),
  ];
  let jobReads = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const q = url.searchParams;
      calls.push({ method, path, query: q, headers: new Headers(init?.headers), body });
      if (path === '/me/permissions') {
        return json({ user_id: 'u1', permissions: b.permissions ?? ['citype:manage'] });
      }
      if (path === '/modules') return json({ data: [] });
      if (path === '/ci-types') {
        const data = [
          {
            id: TYPE,
            name: 'Router',
            display_name: { de: 'Router', en: 'Router' },
            is_active: true,
          },
        ];
        return json({ data, has_more: false });
      }
      if (path === `/ci-types/${TYPE}`) {
        return json({ id: TYPE, name: 'Router', description: 'Randrouter', is_active: true });
      }
      if (path === `/templates/${TEMPLATE}`) {
        return json({ id: TEMPLATE, key: 'routerbasis', is_active: true });
      }
      if (path === '/metamodel/drafts' && method === 'POST') {
        const created = draft(D_NEW, String(body!.name), { item_count: 0 });
        drafts = [created, ...drafts];
        return json(created, 201);
      }
      if (path === '/metamodel/drafts') {
        // Two pages: the last draft comes over next_cursor.
        if (!q.get('cursor') && drafts.length > 1) {
          return json({ data: drafts.slice(0, -1), next_cursor: 'p2', has_more: true });
        }
        return json({ data: q.get('cursor') ? drafts.slice(-1) : drafts, has_more: false });
      }
      const items = /^\/metamodel\/drafts\/([^/]+)\/items$/.exec(path);
      if (items && method === 'GET') return json({ data: ITEMS, has_more: false });
      const removeItem = /^\/metamodel\/drafts\/([^/]+)\/items\/([^/]+)$/.exec(path);
      if (removeItem) {
        if (b.removeItem) return b.removeItem();
        return json(drafts.find((d) => d.id === removeItem[1]));
      }
      const test = /^\/metamodel\/drafts\/([^/]+)\/test$/.exec(path);
      if (test && method === 'POST') {
        if (b.test) return b.test();
        return json({ job_id: JOB, status: 'queued', kind: 'metamodel.test' }, 202);
      }
      if (test) {
        return json({
          ...REPORT,
          data: [
            {
              ci_id: CI,
              ci_type_id: TYPE,
              field: 'rack_unit',
              code: 'required',
              message: 'Pflichtfeld fehlt',
            },
          ],
          total: 3,
        });
      }
      const publish = /^\/metamodel\/drafts\/([^/]+)\/publish$/.exec(path);
      if (publish) {
        if (b.publish) return b.publish();
        const done = { ...drafts.find((d) => d.id === publish[1]), status: 'published' };
        drafts = drafts.map((d) => (d.id === publish[1] ? done : d));
        return json({
          draft: done,
          counts: { ci_type: 1 },
          types: [{ id: TYPE, schema_version: 7 }],
        });
      }
      const one = /^\/metamodel\/drafts\/([^/]+)$/.exec(path);
      if (one && method === 'DELETE') {
        const discarded = { ...drafts.find((d) => d.id === one[1]), status: 'discarded' };
        drafts = drafts.map((d) => (d.id === one[1] ? discarded : d));
        return json(discarded);
      }
      if (one) {
        const found = drafts.find((d) => d.id === one[1]);
        return found ? json({ ...found, items: ITEMS }) : problem(404, {});
      }
      if (path === `/jobs/${JOB}`) {
        jobReads += 1;
        const done = jobReads > 1;
        if (done) {
          drafts = drafts.map((d) =>
            d.id === D_OPEN ? { ...d, status: 'tested', test_report: REPORT } : d,
          );
        }
        return json({
          id: JOB,
          kind: 'metamodel.test',
          status: done ? 'succeeded' : 'running',
          progress: done ? 100 : 50,
          attempts: 1,
          max_attempts: 1,
          created_at: AT,
          ...(done ? { result: REPORT } : {}),
        });
      }
      return json({ detail: `unexpected ${method} ${path}` }, 404);
    }),
  );
  return calls;
}

function renderAt(route: string) {
  return renderWithProviders(
    <Routes>
      <Route path="/ci-types/drafts" element={<MetamodelDraftsPage />} />
      <Route path="/ci-types/drafts/:id" element={<MetamodelDraftsPage />} />
      <Route path="/cmdb/:id" element={<p>CI-Seite</p>} />
    </Routes>,
    { route },
  );
}

const PUBLISHER = ['citype:manage', 'metamodel:publish'];

beforeEach(() => {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
    user: { sub: 'u1' },
  });
  useMetamodelDraftStore.getState().clear();
});

afterEach(() => {
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  useMetamodelDraftStore.getState().clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('MetamodelDraftsPage list', () => {
  it('lists every page of drafts and filters by status in the page', async () => {
    const calls = stubBackend();
    renderAt('/ci-types/drafts');

    const list = await screen.findByRole('list', { name: 'Metamodell-Drafts' });
    await within(list).findByRole('link', { name: 'Alt' });
    const reads = calls.filter((c) => c.method === 'GET' && c.path === '/metamodel/drafts');
    expect(reads.map((c) => c.query.get('cursor'))).toEqual([null, 'p2']);
    expect(reads.every((c) => !c.query.has('status'))).toBe(true);

    fireEvent.change(screen.getByLabelText('Status'), { target: { value: 'tested' } });
    expect(within(list).getByRole('link', { name: 'Getestet' })).toBeInTheDocument();
    expect(within(list).queryByRole('link', { name: 'Rack-Felder' })).toBeNull();
    expect(within(list).queryByRole('link', { name: 'Alt' })).toBeNull();
    // A published draft is closed: no actions.
    fireEvent.change(screen.getByLabelText('Status'), { target: { value: 'published' } });
    const done = within(list).getByRole('link', { name: 'Alt' }).closest('li')!;
    expect(within(done).queryByRole('button')).toBeNull();
  });

  it('creates, discards with If-Match and chooses the active draft', async () => {
    const calls = stubBackend();
    renderAt('/ci-types/drafts');
    await screen.findByRole('link', { name: 'Rack-Felder' });

    fireEvent.click(screen.getByRole('button', { name: 'Draft anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/Name des neuen Drafts/), {
      target: { value: 'Neue Felder' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Draft anlegen' }));
    await screen.findByRole('link', { name: 'Neue Felder' });
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ name: 'Neue Felder' });

    fireEvent.click(
      screen.getByRole('button', { name: 'Draft Rack-Felder als aktiven Draft wählen' }),
    );
    expect(useMetamodelDraftStore.getState().draft).toEqual({ id: D_OPEN, name: 'Rack-Felder' });

    fireEvent.click(screen.getByRole('button', { name: 'Draft Rack-Felder verwerfen' }));
    const confirm = await screen.findByRole('dialog', { name: 'Draft „Rack-Felder“ verwerfen?' });
    fireEvent.click(within(confirm).getByRole('button', { name: 'Verwerfen' }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === 'DELETE' && c.path === `/metamodel/drafts/${D_OPEN}`),
      ).toBe(true),
    );
    const del = calls.find((c) => c.method === 'DELETE')!;
    expect(del.headers.get('If-Match')).toBe('"3"');
    // The discarded draft was the active one: the editors write directly again.
    await waitFor(() => expect(useMetamodelDraftStore.getState().draft).toBeNull());
  });
});

describe('MetamodelDraftsPage navigation', () => {
  it('has the tab "Drafts" and the banner opens the active draft', async () => {
    stubBackend();
    useMetamodelDraftStore.getState().setDraft({ id: D_OPEN, name: 'Rack-Felder' });
    renderWithProviders(
      <>
        <DraftBanner />
        <MetamodelDraftsPage />
      </>,
      { route: '/ci-types/drafts' },
    );

    const tabs = screen.getByRole('navigation', { name: 'Metamodell' });
    expect(within(tabs).getByRole('link', { name: 'Drafts' })).toHaveAttribute(
      'href',
      '/ci-types/drafts',
    );
    const banner = await screen.findByRole('region', { name: 'Metamodell-Draft' });
    expect(within(banner).getByRole('link', { name: 'Öffnen' })).toHaveAttribute(
      'href',
      `/ci-types/drafts/${D_OPEN}`,
    );
  });
});

describe('MetamodelDraftsPage patch list', () => {
  it('groups the items by kind and diffs each against the current target', async () => {
    stubBackend();
    renderAt(`/ci-types/drafts/${D_OPEN}`);

    expect(await screen.findByRole('heading', { name: 'CI-Typ (1)' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Attribut (1)' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Template (1)' })).toBeInTheDocument();

    // update: the payload against the current state of the type.
    const update = await screen.findByRole('table', { name: 'ändern: Router neu' });
    await waitFor(() => expect(within(update).getByText('Randrouter')).toBeInTheDocument());
    expect(within(update).getByText('Router')).toBeInTheDocument();
    expect(within(update).getByText('Kernrouter')).toBeInTheDocument();
    // create: only the new values.
    const create = screen.getByRole('table', { name: 'anlegen: rack_unit' });
    expect(within(create).getByText('Hinzugefügt')).toBeInTheDocument();
    expect(within(create).getByText('integer')).toBeInTheDocument();
    // delete: the removed object.
    const removed = await screen.findByRole('table', { name: `löschen: ${TEMPLATE}` });
    await waitFor(() => expect(within(removed).getByText('routerbasis')).toBeInTheDocument());
    expect(within(removed).getByText('Entfernt')).toBeInTheDocument();
  });

  it('removes an item with If-Match of the draft version; a 412 reads the draft again', async () => {
    const calls = stubBackend({
      removeItem: () => problem(412, { detail: 'version mismatch' }),
    });
    renderAt(`/ci-types/drafts/${D_OPEN}`);

    fireEvent.click(
      await screen.findByRole('button', { name: 'Änderung rack_unit aus dem Draft entfernen' }),
    );
    await screen.findByText(/zwischenzeitlich geändert und neu geladen/);
    const del = calls.find((c) => c.method === 'DELETE')!;
    expect(del.path).toBe(`/metamodel/drafts/${D_OPEN}/items/i-attr`);
    expect(del.headers.get('If-Match')).toBe('"3"');
    const reads = calls.filter(
      (c) => c.method === 'GET' && c.path === `/metamodel/drafts/${D_OPEN}`,
    );
    expect(reads.length).toBeGreaterThanOrEqual(2);
  });

  it('keeps a published draft read-only', async () => {
    stubBackend({ permissions: PUBLISHER });
    renderAt(`/ci-types/drafts/${D_DONE}`);

    await screen.findByText('Dieser Draft ist abgeschlossen und schreibgeschützt.');
    await screen.findByRole('table', { name: 'ändern: Router neu' });
    expect(screen.queryByRole('button', { name: /entfernen/ })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Testen' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Publizieren' })).toBeNull();
  });
});

describe('MetamodelDraftsPage test', () => {
  it('locks the test without metamodel:publish and shows only the counters', async () => {
    const calls = stubBackend();
    renderAt(`/ci-types/drafts/${D_TESTED}`);

    expect(await screen.findByRole('button', { name: 'Testen' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Publizieren' })).toBeDisabled();
    expect(screen.getByText('Ungültige CIs', { selector: 'dt' }).nextSibling).toHaveTextContent(
      '2',
    );
    expect(screen.getByRole('table', { name: 'Zähler je Typ' })).toBeInTheDocument();
    expect(screen.queryByRole('table', { name: 'Verstöße' })).toBeNull();
    expect(calls.some((c) => c.path.endsWith('/test'))).toBe(false);
  });

  it('starts the test, follows the job until it succeeded and links the CIs of the report', async () => {
    const calls = stubBackend({ permissions: PUBLISHER });
    renderAt(`/ci-types/drafts/${D_OPEN}`);

    fireEvent.click(await screen.findByRole('button', { name: 'Testen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Draft „Rack-Felder“ testen' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test starten' }));

    await screen.findByText('Der Test läuft (50 %).');
    const violations = await screen.findByRole('table', { name: 'Verstöße' }, { timeout: 4000 });
    expect(within(violations).getByRole('link', { name: `CI ${CI} öffnen` })).toHaveAttribute(
      'href',
      `/cmdb/${CI}`,
    );
    expect(within(violations).getByText('Pflichtfeld fehlt')).toBeInTheDocument();
    expect(screen.getByText(/^Test-Job: /)).toBeInTheDocument();
    expect(calls.filter((c) => c.path === `/jobs/${JOB}`).length).toBeGreaterThanOrEqual(2);
    expect(
      calls.some((c) => c.method === 'POST' && c.path === `/metamodel/drafts/${D_OPEN}/test`),
    ).toBe(true);
    const report = calls.find(
      (c) => c.method === 'GET' && c.path === `/metamodel/drafts/${D_OPEN}/test`,
    )!;
    expect(report.query.get('limit')).toBe('50');
  });

  it('shows blocking composition conflicts apart from the counters', async () => {
    stubBackend({
      permissions: PUBLISHER,
      drafts: [
        draft(D_OPEN, 'Rack-Felder', {
          test_report: {
            ...REPORT,
            blocked: true,
            conflicts: [
              { code: 'datatype-conflict', key: 'rack_unit', message: 'text vs integer' },
            ],
          },
        }),
      ],
    });
    renderAt(`/ci-types/drafts/${D_OPEN}`);

    const conflicts = await screen.findByRole('list', { name: 'Blockierende Konflikte' });
    expect(conflicts).toHaveTextContent('datatype-conflict · rack_unit: text vs integer');
    expect(screen.getByRole('button', { name: 'Publizieren' })).toBeDisabled();
  });
});

describe('MetamodelDraftsPage publish', () => {
  it('publishes only a tested draft after the confirmation and resets the active draft', async () => {
    const calls = stubBackend({ permissions: PUBLISHER });
    useMetamodelDraftStore.getState().setDraft({ id: D_TESTED, name: 'Getestet' });
    renderAt(`/ci-types/drafts/${D_TESTED}`);

    fireEvent.click(await screen.findByRole('button', { name: 'Publizieren' }));
    const dialog = await screen.findByRole('dialog', { name: 'Draft „Getestet“ publizieren' });
    const summary = within(dialog).getByRole('list', { name: 'Änderungen je Art' });
    expect(summary).toHaveTextContent('CI-Typ: 1');
    expect(summary).toHaveTextContent('Attribut: 1');
    expect(summary).toHaveTextContent('Template: 1');
    expect(calls.some((c) => c.path.endsWith('/publish'))).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Jetzt publizieren' }));
    const result = await screen.findByRole('status', { name: 'Der Draft wurde publiziert.' });
    await waitFor(() => expect(result).toHaveTextContent('Router: Schemaversion 7'));
    expect(useMetamodelDraftStore.getState().draft).toBeNull();
  });

  it('offers publishing only for a tested draft', async () => {
    stubBackend({ permissions: PUBLISHER });
    renderAt(`/ci-types/drafts/${D_OPEN}`);
    expect(await screen.findByRole('button', { name: 'Publizieren' })).toBeDisabled();
  });

  it('lists the stale items of a 409 and offers removing them', async () => {
    const calls = stubBackend({
      permissions: PUBLISHER,
      publish: () =>
        problem(409, {
          detail: 'the targets of items of the draft changed',
          stale_items: [
            {
              item_id: 'i-type',
              kind: 'ci_type',
              op: 'update',
              target_id: TYPE,
              target_key: null,
              reason: 'changed',
            },
          ],
        }),
    });
    renderAt(`/ci-types/drafts/${D_TESTED}`);

    fireEvent.click(await screen.findByRole('button', { name: 'Publizieren' }));
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Jetzt publizieren' }),
    );
    const stale = await screen.findByRole('list', { name: /Veraltete Änderungen/ });
    expect(stale).toHaveTextContent('CI-Typ · ändern · Router neu (geändert)');
    fireEvent.click(
      within(stale).getByRole('button', { name: 'Änderung Router neu aus dem Draft entfernen' }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === 'DELETE' && c.path === `/metamodel/drafts/${D_TESTED}/items/i-type`,
        ),
      ).toBe(true),
    );
  });

  it.each([
    [409, 'unique value conflict: rack_unit'],
    [403, 'missing permission: metamodel:publish'],
  ])('shows a %s as problem detail with trace_id', async (status, detail) => {
    stubBackend({
      permissions: PUBLISHER,
      publish: () => problem(status, { detail, trace_id: 'trace-799' }),
    });
    renderAt(`/ci-types/drafts/${D_TESTED}`);

    fireEvent.click(await screen.findByRole('button', { name: 'Publizieren' }));
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Jetzt publizieren' }),
    );
    const alert = await screen.findByText('Der Draft konnte nicht publiziert werden.');
    const box = alert.closest('[role="alert"]')!;
    expect(box).toHaveTextContent(`Fehler ${status}: ${detail}`);
    expect(box).toHaveTextContent('Trace-ID: trace-799');
  });
});

describe('diffSides', () => {
  it('compares an update only in the fields of the payload', () => {
    const it = item('x', 'ci_type', 'update', { payload: { name: 'B' } });
    const { before, after } = diffSides(it, { name: 'A', key: 'k' }, 'X');
    expect(before[0]!.values).toEqual({ name: 'A' });
    expect(after[0]!.values).toEqual({ name: 'B' });
  });

  it('turns activate into is_active and an upsert of a missing field into a creation', () => {
    const act = diffSides(item('a', 'ci_type', 'activate'), { is_active: false }, 'A');
    expect(act.before[0]!.values).toEqual({ is_active: false });
    expect(act.after[0]!.values).toEqual({ is_active: true });
    const up = diffSides(
      item('u', 'ci_type_field', 'upsert', { payload: { name: 'f' } }),
      null,
      'f',
    );
    expect(up.before).toEqual([]);
  });
});
