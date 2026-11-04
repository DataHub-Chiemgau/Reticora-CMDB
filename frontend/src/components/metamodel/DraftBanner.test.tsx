/**
 * WP-798 (UI-11, MET-53 [P2]): the draft context of the metamodel editors.
 *
 *   Banner   only with an active draft, citype:manage and the module
 *            metamodel; name, status, number and list of the items (all
 *            pages of …/items); create, switch, leave; "work in a draft" in
 *            /ci-types without one; the toast of a captured write
 *   Reset    the store is reset with the module off, on 404 of the draft and
 *            for a published or discarded draft
 *   Marker   DraftMarker/draftChangeFor: new, changed and deleted per target
 *            id and per target_key
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/metamodel/DraftBanner.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { useAuthStore } from '../../auth/authStore';
import { useMetamodelDraftStore } from '../../stores/metamodelDraft';
import { useToastStore } from '../../stores/toast';
import type { MetamodelDraftItem } from '../../api/cmdb';
import { DraftBanner, DraftMarker, draftChangeFor, draftCreations } from './DraftBanner';

type Json = Record<string, unknown>;

interface Call {
  method: string;
  url: string;
  body: unknown;
}

const DRAFT_A = '11111111-1111-4111-8111-111111111111';
const DRAFT_B = '22222222-2222-4222-8222-222222222222';
const DRAFT_NEW = '33333333-3333-4333-8333-333333333333';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function draft(id: string, name: string, extra: Json = {}): Json {
  return {
    id,
    organization_id: 'org-1',
    name,
    status: 'draft',
    item_count: 0,
    version: 1,
    created_at: '2026-11-01T10:00:00Z',
    updated_at: '2026-11-01T10:00:00Z',
    ...extra,
  };
}

function item(id: string, kind: string, op: string, extra: Partial<MetamodelDraftItem> = {}) {
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
    created_at: '2026-11-01T10:00:00Z',
    ...extra,
  } as MetamodelDraftItem;
}

interface Backend {
  permissions?: string[];
  metamodel?: boolean;
  drafts?: Record<string, Json>;
  /** Pages of GET …/items per draft. */
  itemPages?: Record<string, Json[][]>;
}

function stubBackend(b: Backend = {}): Call[] {
  const calls: Call[] = [];
  const drafts: Record<string, Json> = { ...(b.drafts ?? {}) };
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = (init?.body ? JSON.parse(String(init.body)) : undefined) as Json;
      calls.push({ method, url, body });
      const u = new URL(url, 'http://localhost');
      const path = u.pathname.replace(/^\/api\/v1/, '');
      if (path === '/me/permissions') {
        return json({ user_id: 'u1', permissions: b.permissions ?? ['citype:manage'] });
      }
      if (path === '/modules') {
        return json({
          data: [{ key: 'metamodel', phase: 'P2', nav: [], enabled: b.metamodel ?? true }],
        });
      }
      if (path === '/metamodel/drafts' && method === 'POST') {
        const created = draft(DRAFT_NEW, String(body.name));
        drafts[DRAFT_NEW] = created;
        return json(created, 201);
      }
      if (path === '/metamodel/drafts') {
        const status = u.searchParams.get('status');
        return json({ data: Object.values(drafts).filter((d) => d.status === status) });
      }
      const items = /^\/metamodel\/drafts\/([^/]+)\/items$/.exec(path);
      if (items) {
        const pages = b.itemPages?.[items[1]!] ?? [[]];
        const cursor = u.searchParams.get('cursor');
        const index = cursor ? Number(cursor) : 0;
        const next = index + 1 < pages.length ? { next_cursor: String(index + 1) } : {};
        return json({ data: pages[index] ?? [], ...next });
      }
      const one = /^\/metamodel\/drafts\/([^/]+)$/.exec(path);
      if (one) {
        const found = drafts[one[1]!];
        return found ? json(found) : json({ status: 404, title: 'Not Found' }, 404);
      }
      return json({ detail: `unexpected ${method} ${url}` }, 404);
    }),
  );
  return calls;
}

function activate(id: string, name: string) {
  act(() => useMetamodelDraftStore.getState().setDraft({ id, name }));
}

const banner = () => screen.findByRole('region', { name: 'Metamodell-Draft' });

beforeEach(() => {
  useAuthStore.getState().setSession({
    token: 'token',
    expiresAt: '2999-01-01T00:00:00Z',
    user: { sub: 'u1', email: 'admin@example.com' },
  });
});

afterEach(() => {
  act(() => useMetamodelDraftStore.getState().clear());
  useToastStore.setState({ toasts: [] });
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  window.sessionStorage.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('DraftBanner visibility', () => {
  it('shows the active draft with name, status and number of items on every page', async () => {
    stubBackend({
      drafts: { [DRAFT_A]: draft(DRAFT_A, 'Umbau Netz', { status: 'tested', item_count: 1 }) },
      itemPages: { [DRAFT_A]: [[item('i-1', 'ci_type', 'update', { target_id: 't-1' })]] },
    });
    activate(DRAFT_A, 'Umbau Netz');
    renderWithProviders(<DraftBanner />, { route: '/assets' });

    const region = await banner();
    expect(region).toHaveTextContent('Draft „Umbau Netz“ aktiv');
    expect(await within(region).findByText('Getestet')).toBeInTheDocument();
    expect(region).toHaveTextContent('Änderungen: 1');
  });

  it('stays hidden without citype:manage', async () => {
    const calls = stubBackend({
      permissions: ['ci:read'],
      drafts: { [DRAFT_A]: draft(DRAFT_A, 'Umbau') },
    });
    activate(DRAFT_A, 'Umbau');
    renderWithProviders(<DraftBanner />, { route: '/ci-types' });

    await waitFor(() => expect(calls.some((c) => c.url.includes('/me/permissions'))).toBe(true));
    await waitFor(() => expect(useMetamodelDraftStore.getState().draft).toBeNull());
    expect(screen.queryByRole('region', { name: 'Metamodell-Draft' })).not.toBeInTheDocument();
    expect(calls.some((c) => c.url.includes('/metamodel/drafts'))).toBe(false);
  });

  it('resets the store and stays hidden with the module metamodel off (E-105)', async () => {
    const calls = stubBackend({ metamodel: false, drafts: { [DRAFT_A]: draft(DRAFT_A, 'Umbau') } });
    activate(DRAFT_A, 'Umbau');
    renderWithProviders(<DraftBanner />, { route: '/ci-types' });

    await waitFor(() => expect(useMetamodelDraftStore.getState().draft).toBeNull());
    expect(window.sessionStorage.getItem('reticora-metamodel-draft')).toBeNull();
    expect(screen.queryByRole('region', { name: 'Metamodell-Draft' })).not.toBeInTheDocument();
    expect(calls.some((c) => c.url.includes('/metamodel/drafts'))).toBe(false);
  });

  it('offers "work in a draft" only in the metamodel area without an active draft', async () => {
    stubBackend();
    const { unmount } = renderWithProviders(<DraftBanner />, { route: '/assets' });
    await waitFor(() => expect(useAuthStore.getState().token).toBe('token'));
    expect(screen.queryByRole('button', { name: 'In Draft arbeiten' })).not.toBeInTheDocument();
    unmount();

    renderWithProviders(<DraftBanner />, { route: '/ci-types/templates' });
    expect(await screen.findByRole('button', { name: 'In Draft arbeiten' })).toBeInTheDocument();
  });
});

describe('DraftBanner actions', () => {
  it('creates a draft with POST /metamodel/drafts and makes it the active one', async () => {
    const calls = stubBackend();
    renderWithProviders(<DraftBanner />, { route: '/ci-types' });

    fireEvent.click(await screen.findByRole('button', { name: 'In Draft arbeiten' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Name des neuen Drafts'), {
      target: { value: 'Release Q4' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Draft anlegen' }));

    await waitFor(() =>
      expect(useMetamodelDraftStore.getState().draft).toEqual({
        id: DRAFT_NEW,
        name: 'Release Q4',
      }),
    );
    const post = calls.find((c) => c.method === 'POST' && c.url.endsWith('/metamodel/drafts'));
    expect(post?.body).toEqual({ name: 'Release Q4' });
    expect((await banner()).textContent).toContain('Draft „Release Q4“ aktiv');
    expect(JSON.parse(window.sessionStorage.getItem('reticora-metamodel-draft') ?? '{}')).toEqual({
      id: DRAFT_NEW,
      name: 'Release Q4',
    });
  });

  it('switches to another draft in status draft or tested and leaves the draft', async () => {
    const calls = stubBackend({
      drafts: {
        [DRAFT_A]: draft(DRAFT_A, 'Umbau'),
        [DRAFT_B]: draft(DRAFT_B, 'Bereinigung', { status: 'tested' }),
        old: draft('old', 'Alt', { status: 'published' }),
      },
    });
    activate(DRAFT_A, 'Umbau');
    renderWithProviders(<DraftBanner />, { route: '/dashboard' });

    fireEvent.click(
      await within(await banner()).findByRole('button', { name: 'Wechseln oder neu' }),
    );
    const dialog = screen.getByRole('dialog');
    fireEvent.click(
      await within(dialog).findByRole('button', { name: 'Zu Draft Bereinigung wechseln' }),
    );
    expect(within(dialog).queryByText(/Alt/)).not.toBeInTheDocument();
    expect(useMetamodelDraftStore.getState().draft).toEqual({ id: DRAFT_B, name: 'Bereinigung' });
    expect(calls.some((c) => c.url.includes('status=draft'))).toBe(true);
    expect(calls.some((c) => c.url.includes('status=tested'))).toBe(true);

    fireEvent.click(await within(await banner()).findByRole('button', { name: 'Draft verlassen' }));
    expect(useMetamodelDraftStore.getState().draft).toBeNull();
    expect(screen.queryByRole('region', { name: 'Metamodell-Draft' })).not.toBeInTheDocument();
  });

  it('confirms a captured write with a toast', async () => {
    stubBackend({ drafts: { [DRAFT_A]: draft(DRAFT_A, 'Umbau') } });
    activate(DRAFT_A, 'Umbau');
    renderWithProviders(<DraftBanner />, { route: '/ci-types' });
    await banner();

    act(() => useMetamodelDraftStore.getState().bumpItemRevision());
    await waitFor(() =>
      expect(useToastStore.getState().toasts.map((toast) => toast.message)).toEqual([
        'Im Draft Umbau erfasst, noch nicht produktiv',
      ]),
    );
  });
});

describe('DraftBanner reset', () => {
  it('resets the store when the draft is gone (404)', async () => {
    stubBackend({ drafts: {} });
    activate(DRAFT_A, 'Umbau');
    renderWithProviders(<DraftBanner />, { route: '/ci-types' });

    await waitFor(() => expect(useMetamodelDraftStore.getState().draft).toBeNull());
    // Back to direct writes: the offer to work in a draft again.
    expect(await screen.findByRole('button', { name: 'In Draft arbeiten' })).toBeInTheDocument();
  });

  it.each(['published', 'discarded'])('resets the store for a %s draft', async (status) => {
    stubBackend({ drafts: { [DRAFT_A]: draft(DRAFT_A, 'Umbau', { status }) } });
    activate(DRAFT_A, 'Umbau');
    renderWithProviders(<DraftBanner />, { route: '/ci-types' });

    await waitFor(() => expect(useMetamodelDraftStore.getState().draft).toBeNull());
  });
});

describe('DraftBanner items', () => {
  it('lists the pending items of every page of …/items with kind, operation and target', async () => {
    const calls = stubBackend({
      drafts: { [DRAFT_A]: draft(DRAFT_A, 'Umbau', { item_count: 3 }) },
      itemPages: {
        [DRAFT_A]: [
          [
            item('i-1', 'attribute_definition', 'create', { payload: { key: 'serial' } as never }),
            item('i-2', 'global_field', 'delete', { target_key: 'owner' }),
          ],
          [item('i-3', 'ci_type', 'update', { target_id: 't-1' })],
        ],
      },
    });
    activate(DRAFT_A, 'Umbau');
    renderWithProviders(<DraftBanner />, { route: '/cis' });

    const region = await banner();
    fireEvent.click(await within(region).findByText('Ausstehende Änderungen (3)'));
    const list = within(region).getByRole('list', { name: 'Ausstehende Änderungen' });
    expect(
      within(list)
        .getAllByRole('listitem')
        .map((li) => li.textContent),
    ).toEqual([
      'Attribut · anlegen · serial',
      'Globales Feld · löschen · owner',
      'CI-Typ · ändern · t-1',
    ]);
    const pages = calls.filter((c) => c.url.includes(`/metamodel/drafts/${DRAFT_A}/items`));
    expect(pages).toHaveLength(2);
    expect(pages[1]!.url).toContain('cursor=1');
  });
});

describe('DraftMarker', () => {
  const items = [
    item('i-1', 'ci_type', 'update', { target_id: 't-1' }),
    item('i-2', 'attribute_definition', 'delete', { target_id: 'ad-1' }),
    item('i-3', 'attribute_definition', 'update', { target_id: 'ad-1' }),
    item('i-4', 'ci_type_field', 'upsert', { target_id: 't-2', target_key: 'serial' }),
    item('i-5', 'global_field', 'delete', { target_key: 'owner' }),
    item('i-6', 'ci_type', 'clone', { target_id: 't-3' }),
    item('i-7', 'attribute_definition', 'create', { payload: { key: 'asset_tag' } as never }),
  ];

  it('tells new, changed and deleted per target id and target_key', () => {
    expect(draftChangeFor(items, { kinds: ['ci_type'], id: 't-1' })).toBe('updated');
    expect(draftChangeFor(items, { kinds: ['attribute_definition'], id: 'ad-1' })).toBe('deleted');
    expect(draftChangeFor(items, { kinds: ['ci_type_field'], key: 'serial', typeId: 't-2' })).toBe(
      'updated',
    );
    expect(
      draftChangeFor(items, { kinds: ['ci_type_field'], key: 'serial', typeId: 't-9' }),
    ).toBeUndefined();
    expect(draftChangeFor(items, { kinds: ['global_field'], key: 'owner' })).toBe('deleted');
    // A clone leaves its source unchanged; the clone is a new part.
    expect(draftChangeFor(items, { kinds: ['ci_type'], id: 't-3' })).toBeUndefined();
    expect(draftCreations(items, 'ci_type').map((i) => i.id)).toEqual(['i-6']);
    expect(draftCreations(items, 'attribute_definition').map((i) => i.id)).toEqual(['i-7']);
    expect(draftChangeFor(items, { kinds: ['ci_type'], id: 't-4' })).toBeUndefined();
  });

  it('renders the badge of each change', () => {
    renderWithProviders(
      <>
        <DraftMarker change="created" />
        <DraftMarker change="updated" />
        <DraftMarker change="deleted" />
      </>,
    );
    expect(screen.getByText('im Draft: neu')).toBeInTheDocument();
    expect(screen.getByText('im Draft: geändert')).toBeInTheDocument();
    expect(screen.getByText('im Draft: gelöscht')).toBeInTheDocument();
  });
});
