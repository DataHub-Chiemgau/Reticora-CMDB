// UI-14 (WP-814): the ticket list links each ticket to its detail page and
// offers the queue of my teams (team_id=mine, E-143) with Ticketing
// Standard only; the detail page offers only the next states of the state
// machine (WP-290) and shows a refused change (409) as a conflict, a 422
// of the assignment at the picker, links a CI and removes the link (409 of
// a duplicate link as a message), writes internal and external comments
// and names a system comment "System", uploads an attachment with the link
// to the ticket and shows the SLA only with ticketing_standard (E-39).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import i18n from '../i18n';
import { renderWithProviders } from '../test/utils';
import { useAuthStore } from '../auth/authStore';
import { useScopeStore } from '../stores/scope';
import type { Ticket, TicketComment, TicketLink } from '../api/client';
import { TicketListPage } from './TicketListPage';
import { TICKET_TRANSITIONS, TicketDetailPage, formatRemaining } from './TicketDetailPage';

const TICKET_ID = 'dc000000-0000-4000-8000-000000000001';
const CI_ID = 'c1000000-0000-4000-8000-000000000001';
const LINK_ID = 'e1000000-0000-4000-8000-000000000001';

const ticket = (over: Partial<Ticket> = {}): Ticket => ({
  id: TICKET_ID,
  organization_id: 'org-1',
  ticket_number: 42,
  title: 'Drucker defekt',
  description: 'Papierstau im 2. OG',
  status: 'new',
  priority: 'high',
  category: 'incident',
  reporter_id: 'user-1',
  tags: [],
  created_at: '2026-11-01T08:00:00Z',
  updated_at: '2026-11-01T08:00:00Z',
  ...over,
});

const comment = (over: Partial<TicketComment>): TicketComment => ({
  id: 'cm-1',
  ticket_id: TICKET_ID,
  author_id: 'user-1',
  author_type: 'user',
  content: 'Hallo',
  is_internal: false,
  created_at: '2026-11-01T09:00:00Z',
  updated_at: '2026-11-01T09:00:00Z',
  ...over,
});

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[], over: Record<string, unknown> = {}) => ({
  data,
  total: data.length,
  limit: 50,
  offset: 0,
  has_more: false,
  ...over,
});

interface Call {
  method: string;
  path: string;
  search: URLSearchParams;
  body: unknown;
}

type Route = (call: Call) => Response;

interface ServerOptions {
  standard?: boolean;
  permissions?: string[];
  routes?: Record<string, Route>;
}

/**
 * stubServer answers fetch by "METHOD /path" (below /api/v1) ahead of the
 * defaults of a ticket page and records every call.
 */
function stubServer({
  standard = true,
  permissions = ['ticket:read', 'ticket:write', 'document:read', 'document:write'],
  routes = {},
}: ServerOptions = {}) {
  const calls: Call[] = [];
  const defaults: Record<string, Route> = {
    'GET /me/permissions': () => json({ user_id: 'user-1', permissions }),
    'GET /entitlements': () =>
      json(
        page([
          { organization_id: 'org-1', feature_key: 'ticketing_essential', enabled: true },
          { organization_id: 'org-1', feature_key: 'ticketing_standard', enabled: standard },
        ]),
      ),
    'GET /modules': () =>
      json({ data: [{ key: 'ticketing', phase: 'P2', nav: ['tickets', 'slas'], enabled: true }] }),
    'GET /tickets': () => json(page([ticket()])),
    [`GET /tickets/${TICKET_ID}`]: () => json(ticket()),
    [`GET /tickets/${TICKET_ID}/comments`]: () => json(page([])),
    [`GET /tickets/${TICKET_ID}/links`]: () => json(page([])),
    [`GET /tickets/${TICKET_ID}/attachments`]: () => json(page([])),
    [`GET /tickets/${TICKET_ID}/sla`]: () => json({ status: 404, title: 'Not Found' }, 404),
    [`GET /cis/${CI_ID}`]: () =>
      json({ id: CI_ID, name: 'Drucker-2OG', ci_type_id: 't1', status: 'active' }),
    'GET /cis': () =>
      json(page([{ id: CI_ID, name: 'Drucker-2OG', ci_type_id: 't1', status: 'active' }])),
  };
  const table = { ...defaults, ...routes };
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
    const method = (init?.method ?? 'GET').toUpperCase();
    const call: Call = {
      method,
      path: url.pathname.replace(/^\/api\/v1/, ''),
      search: url.searchParams,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    };
    calls.push(call);
    const route = table[`${method} ${call.path}`];
    return route
      ? route(call)
      : json({ status: 404, detail: `unexpected ${method} ${call.path}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return calls;
}

/** MockXHR answers the content transfer of FileUpload at once. */
class MockXHR {
  static requests: MockXHR[] = [];
  method = '';
  url = '';
  status = 0;
  statusText = '';
  responseText = '';
  upload: { onprogress: ((event: ProgressEvent) => void) | null } = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader() {}
  send() {
    MockXHR.requests.push(this);
    this.status = 200;
    this.responseText = JSON.stringify({ id: 'doc-1', title: 'protokoll', version: 1 });
    queueMicrotask(() => this.onload?.());
  }
  abort() {
    this.onabort?.();
  }
  getResponseHeader() {
    return 'application/json';
  }
}

function renderDetail() {
  return renderWithProviders(
    <Routes>
      <Route path="/tickets/:id" element={<TicketDetailPage />} />
    </Routes>,
    { route: `/tickets/${TICKET_ID}` },
  );
}

/** type enters the text letter by letter without a pause. */
function type(input: HTMLElement, text: string) {
  for (let i = 1; i <= text.length; i += 1) {
    fireEvent.change(input, { target: { value: text.slice(0, i) } });
  }
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useScopeStore.setState({ clientId: '', siteId: '' });
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  MockXHR.requests = [];
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('TicketListPage (WP-814)', () => {
  it('links number and title to the detail page', async () => {
    stubServer();
    renderWithProviders(<TicketListPage />, { route: '/tickets' });

    const title = await screen.findByRole('link', { name: 'Drucker defekt' });
    expect(title).toHaveAttribute('href', `/tickets/${TICKET_ID}`);
    expect(screen.getByRole('link', { name: '#42' })).toHaveAttribute(
      'href',
      `/tickets/${TICKET_ID}`,
    );
    // Priority and category read as catalog labels.
    const row = screen.getByRole('link', { name: 'Drucker defekt' }).closest('tr')!;
    expect(within(row).getByText('Hoch')).toBeInTheDocument();
    expect(within(row).getByText('Störung')).toBeInTheDocument();
  });

  it('sends team_id=mine for the queue of my teams', async () => {
    const calls = stubServer();
    renderWithProviders(<TicketListPage />, { route: '/tickets' });

    fireEvent.click(await screen.findByLabelText('Meine Teams'));
    await waitFor(() =>
      expect(calls.some((c) => c.path === '/tickets' && c.search.get('team_id') === 'mine')).toBe(
        true,
      ),
    );
  });

  it('offers no queue of my teams without ticketing_standard', async () => {
    const calls = stubServer({ standard: false });
    renderWithProviders(<TicketListPage />, { route: '/tickets' });

    await screen.findByRole('link', { name: 'Drucker defekt' });
    await waitFor(() => expect(calls.some((c) => c.path === '/entitlements')).toBe(true));
    expect(screen.queryByLabelText('Meine Teams')).toBeNull();
    expect(calls.some((c) => c.search.get('team_id') === 'mine')).toBe(false);
  });
});

describe('TicketDetailPage (WP-814)', () => {
  it('offers only the next states of the state machine', async () => {
    stubServer();
    renderDetail();

    const group = await screen.findByRole('group', { name: 'Status' });
    const labels = within(group)
      .getAllByRole('button')
      .map((button) => button.textContent);
    expect(labels).toEqual(['→ Offen', '→ In Bearbeitung', '→ Geschlossen']);
    expect(TICKET_TRANSITIONS.resolved).toEqual(['closed', 'open']);
  });

  it('offers reopen for a closed ticket', async () => {
    stubServer({
      routes: { [`GET /tickets/${TICKET_ID}`]: () => json(ticket({ status: 'closed' })) },
    });
    renderDetail();

    const group = await screen.findByRole('group', { name: 'Status' });
    expect(
      within(group)
        .getAllByRole('button')
        .map((b) => b.textContent),
    ).toEqual(['Wieder öffnen']);
  });

  it('shows a refused change as conflict and reloads the ticket', async () => {
    let reads = 0;
    const calls = stubServer({
      routes: {
        [`GET /tickets/${TICKET_ID}`]: () => {
          reads += 1;
          return json(ticket());
        },
        [`PATCH /tickets/${TICKET_ID}`]: () =>
          json(
            {
              status: 409,
              title: 'Conflict',
              detail: 'a ticket cannot change from closed to open',
            },
            409,
          ),
      },
    });
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: '→ Offen' }));
    const alert = await screen.findByText(/Statuswechsel abgelehnt/);
    expect(alert).toHaveTextContent('a ticket cannot change from closed to open');
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ status: 'open' });
    await waitFor(() => expect(reads).toBeGreaterThan(1));
  });

  it('shows a 422 of the assignment at the picker', async () => {
    const calls = stubServer({
      routes: {
        'GET /users': () => json(page([{ id: 'u-1', display_name: 'Anna Berg', email: 'a@x' }])),
        'GET /teams': () => json(page([])),
        [`PATCH /tickets/${TICKET_ID}`]: () =>
          json(
            {
              status: 422,
              title: 'Unprocessable Entity',
              detail: 'assignee_id is outside the scope of the ticket',
              field: 'assignee_id',
            },
            422,
          ),
      },
    });
    renderDetail();

    type(await screen.findByRole('combobox', { name: 'Zuweisung an Person oder Team' }), 'Ann');
    fireEvent.click(await screen.findByRole('option', { name: /Anna Berg/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Zuweisen' }));

    expect(
      await screen.findByText('assignee_id is outside the scope of the ticket'),
    ).toBeInTheDocument();
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ assignee_id: 'u-1' });
  });

  it('links a CI, removes the link and reports a duplicate link', async () => {
    let links: TicketLink[] = [];
    let duplicate = false;
    const calls = stubServer({
      routes: {
        [`GET /tickets/${TICKET_ID}/links`]: () => json(page(links)),
        [`POST /tickets/${TICKET_ID}/links`]: (call) => {
          if (duplicate) {
            return json({ status: 409, title: 'Conflict', detail: 'already linked' }, 409);
          }
          const body = call.body as { entity_type: 'ci'; entity_id: string };
          links = [
            {
              id: LINK_ID,
              organization_id: 'org-1',
              ticket_id: TICKET_ID,
              created_at: '2026-11-01T10:00:00Z',
              ...body,
            },
          ];
          return json(links[0], 201);
        },
        [`DELETE /tickets/${TICKET_ID}/links/${LINK_ID}`]: () => {
          links = [];
          return json(null, 204);
        },
      },
    });
    renderDetail();

    const linkCard = async () =>
      (await screen.findByText('Verknüpfte Objekte')).closest('section') ?? document.body;
    fireEvent.click(await screen.findByRole('button', { name: 'Objekt verknüpfen' }));
    const dialog = await screen.findByRole('dialog');
    type(within(dialog).getByRole('combobox', { name: 'CI oder Asset' }), 'Druck');
    fireEvent.click(await within(dialog).findByRole('option', { name: /Drucker-2OG/ }));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Verknüpfen' }));

    const list = await screen.findByRole('list', { name: 'Verknüpfte Objekte' });
    const target = await within(list).findByRole('link', { name: 'Drucker-2OG' });
    expect(target).toHaveAttribute('href', `/cmdb/${CI_ID}`);
    expect(calls.find((c) => c.method === 'POST' && c.path.endsWith('/links'))?.body).toEqual({
      entity_type: 'ci',
      entity_id: CI_ID,
    });

    fireEvent.click(within(list).getByRole('button', { name: 'Verknüpfung lösen: CI' }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'DELETE' && c.path.endsWith(LINK_ID))).toBe(true),
    );
    expect(await within(await linkCard()).findByText('Keine verknüpften Objekte.')).toBeVisible();

    duplicate = true;
    fireEvent.click(screen.getByRole('button', { name: 'Objekt verknüpfen' }));
    const again = await screen.findByRole('dialog');
    type(within(again).getByRole('combobox', { name: 'CI oder Asset' }), 'Druck');
    fireEvent.click(await within(again).findByRole('option', { name: /Drucker-2OG/ }));
    fireEvent.click(within(again).getByRole('button', { name: 'Verknüpfen' }));
    expect(
      await within(again).findByText('Dieses Objekt ist bereits mit dem Ticket verknüpft.'),
    ).toBeInTheDocument();
  });

  it('writes internal and external comments and names system comments', async () => {
    const calls = stubServer({
      routes: {
        [`GET /tickets/${TICKET_ID}/comments`]: () =>
          json(
            page([
              comment({ id: 'cm-1', content: 'Bitte prüfen', is_internal: false }),
              comment({
                id: 'cm-2',
                author_id: null,
                author_type: 'system',
                content: 'Review-Eintrag aufgelöst',
                is_internal: true,
              }),
            ]),
          ),
        [`POST /tickets/${TICKET_ID}/comments`]: (call) =>
          json(comment({ id: 'cm-3', ...(call.body as object) }), 201),
      },
    });
    renderDetail();

    const list = await screen.findByRole('list', { name: 'Kommentare' });
    const system = within(list).getByText('Review-Eintrag aufgelöst').closest('li')!;
    expect(within(system).getByText('System')).toBeInTheDocument();
    expect(within(system).getByText('Intern')).toBeInTheDocument();
    const external = within(list).getByText('Bitte prüfen').closest('li')!;
    expect(within(external).getByText('Extern')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Neuer Kommentar'), { target: { value: 'Extern' } });
    fireEvent.click(screen.getByRole('button', { name: 'Kommentieren' }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === 'POST' && c.path.endsWith('/comments'))).toHaveLength(
        1,
      ),
    );

    fireEvent.click(screen.getByLabelText('Interner Kommentar'));
    fireEvent.change(screen.getByLabelText('Neuer Kommentar'), { target: { value: 'Intern' } });
    fireEvent.click(screen.getByRole('button', { name: 'Kommentieren' }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === 'POST' && c.path.endsWith('/comments'))).toHaveLength(
        2,
      ),
    );
    const bodies = calls
      .filter((c) => c.method === 'POST' && c.path.endsWith('/comments'))
      .map((c) => c.body);
    expect(bodies).toEqual([
      { content: 'Extern', is_internal: false },
      { content: 'Intern', is_internal: true },
    ]);
  });

  it('uploads an attachment through FileUpload with the link to the ticket', async () => {
    vi.stubGlobal('XMLHttpRequest', MockXHR);
    const calls = stubServer({
      routes: {
        'POST /documents': (call) =>
          json({ id: 'doc-1', title: 'protokoll', version: 1, ...(call.body as object) }, 201),
      },
    });
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: 'Dokument hinzufügen' }));
    const dialog = await screen.findByRole('dialog');
    const input = dialog.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File(['%PDF-1.4'], 'protokoll.pdf', { type: 'application/pdf' });
    fireEvent.change(input, { target: { files: [file] } });

    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path === '/documents')).toBe(true),
    );
    const created = calls.find((c) => c.method === 'POST' && c.path === '/documents')!;
    expect((created.body as { links: unknown }).links).toEqual([
      { entity_type: 'ticket', entity_id: TICKET_ID },
    ]);
    await waitFor(() => expect(MockXHR.requests[0]?.url).toContain('/documents/doc-1/content'));
    // The attachments are read from the ticket (WP-765).
    expect(calls.some((c) => c.path === `/tickets/${TICKET_ID}/attachments`)).toBe(true);
  });

  it('offers no upload without document:write', async () => {
    stubServer({ permissions: ['ticket:read', 'ticket:write', 'document:read'] });
    renderDetail();

    await screen.findByText(/Anhänge/);
    await waitFor(() => expect(screen.queryByText('Noch keine Kommentare.')).toBeInTheDocument());
    expect(screen.queryByRole('button', { name: 'Dokument hinzufügen' })).toBeNull();
  });

  it('shows the SLA with a breach badge only with ticketing_standard', async () => {
    stubServer({
      routes: {
        [`GET /tickets/${TICKET_ID}/sla`]: () =>
          json({
            id: 'sla-t-1',
            organization_id: 'org-1',
            ticket_id: TICKET_ID,
            sla_id: 'sla-1',
            response_due_at: '2026-11-01T09:00:00Z',
            resolution_due_at: '2026-11-02T08:00:00Z',
            response_breached: true,
            resolution_breached: false,
            response_remaining_minutes: -90,
            resolution_remaining_minutes: 125,
            calendar_id: null,
            paused: true,
            created_at: '2026-11-01T08:00:00Z',
            updated_at: '2026-11-01T08:00:00Z',
          }),
      },
    });
    renderDetail();

    const heading = await screen.findByText('SLA');
    const card = heading.closest('section') ?? document.body;
    expect(await within(card).findByText('verletzt')).toBeInTheDocument();
    expect(within(card).getByText('pausiert')).toBeInTheDocument();
    expect(within(card).getByText('überfällig seit 1 h 30 min')).toBeInTheDocument();
    expect(within(card).getByText('noch 2 h 5 min')).toBeInTheDocument();
  });

  it('reads no SLA without ticketing_standard', async () => {
    const calls = stubServer({ standard: false });
    renderDetail();

    await screen.findByRole('group', { name: 'Status' });
    await waitFor(() => expect(calls.some((c) => c.path === '/entitlements')).toBe(true));
    expect(screen.queryByText('SLA')).toBeNull();
    expect(calls.some((c) => c.path.endsWith('/sla'))).toBe(false);
  });

  it('says that no SLA is assigned on 404', async () => {
    stubServer();
    renderDetail();
    expect(await screen.findByText('Kein SLA zugeordnet.')).toBeInTheDocument();
  });

  it('words the remaining minutes', () => {
    const t = i18n.getFixedT('de-DE');
    expect(formatRemaining(null, t)).toBe('erfüllt');
    expect(formatRemaining(61, t)).toBe('noch 1 h 1 min');
    expect(formatRemaining(-5, t)).toBe('überfällig seit 0 h 5 min');
  });
});
