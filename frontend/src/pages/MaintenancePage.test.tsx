import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MaintenancePage } from './MaintenancePage';
import { renderWithProviders } from '../test/utils';

// Retest of F-236 (WP-540, NTF-05, MGT-04, NFR-04): the maintenance form
// assigns CIs (ci_ids in the POST), and the window lists the notifications
// the server stores, so a pending notification is still shown after a
// reload and no text claims a delivery.

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const clients = [
  { id: 'client-a', organization_id: 'org-1', name: 'Kunde Alpha' },
  { id: 'client-b', organization_id: 'org-1', name: 'Kunde Beta' },
];
const cis = [
  { id: 'ci-1', organization_id: 'org-1', name: 'web-01', client_id: 'client-a' },
  { id: 'ci-2', organization_id: 'org-1', name: 'web-02', client_id: 'client-b' },
];

interface Notification {
  id: string;
  organization_id: string;
  maintenance_window_id: string;
  client_id: string;
  channel: string;
  status: string;
  created_at: string;
}

/**
 * fakeBackend answers the maintenance API like the server: windows and
 * notifications live across renders (a reload re-reads them), notify is
 * idempotent per client and channel and keeps every notification pending.
 */
function fakeBackend() {
  const windows: Record<string, unknown>[] = [];
  const notifications: Notification[] = [];
  const posts: Record<string, unknown>[] = [];
  const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
    const method = (init?.method ?? 'GET').toUpperCase();
    const path = url.pathname;
    if (path === '/api/v1/clients') {
      return json({ data: clients, total: clients.length, limit: 200, offset: 0 });
    }
    if (path === '/api/v1/cis') {
      const search = url.searchParams.get('search') ?? '';
      const data = cis.filter((ci) => ci.name.includes(search));
      return json({ data, total: data.length, limit: 20, offset: 0 });
    }
    if (path === '/api/v1/maintenance-windows' && method === 'GET') {
      return json({ data: windows, total: windows.length, limit: 100, offset: 0 });
    }
    if (path === '/api/v1/maintenance-windows' && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      posts.push(body);
      const w = {
        id: `win-${windows.length + 1}`,
        organization_id: 'org-1',
        status: 'scheduled',
        created_at: '2026-10-27T10:00:00Z',
        updated_at: '2026-10-27T10:00:00Z',
        ...body,
      };
      windows.push(w);
      return json(w, 201);
    }
    const notify = path.match(/^\/api\/v1\/maintenance-windows\/([^/]+)\/notify$/);
    if (notify && method === 'POST') {
      const windowID = notify[1] ?? '';
      const w = windows.find((x) => x.id === windowID);
      const ciIDs = (w?.ci_ids as string[] | undefined) ?? [];
      const clientIDs = [...new Set(ciIDs.map((id) => cis.find((ci) => ci.id === id)?.client_id))];
      const out = clientIDs.filter(Boolean).map((clientID) => {
        let n = notifications.find(
          (x) => x.maintenance_window_id === windowID && x.client_id === clientID,
        );
        if (!n) {
          n = {
            id: `mn-${notifications.length + 1}`,
            organization_id: 'org-1',
            maintenance_window_id: windowID,
            client_id: clientID as string,
            channel: 'webhook',
            status: 'pending',
            created_at: '2026-10-27T11:00:00Z',
          };
          notifications.push(n);
        }
        return n as Notification;
      });
      return json({ notified: 0, queued: out.length, notifications: out });
    }
    const list = path.match(/^\/api\/v1\/maintenance-windows\/([^/]+)\/notifications$/);
    if (list && method === 'GET') {
      return json({ data: notifications.filter((n) => n.maintenance_window_id === list[1]) });
    }
    return json({ detail: `unexpected request: ${method} ${path}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return { fetchMock, posts, windows, notifications };
}

async function createWindowWithTwoCIs(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('button', { name: 'Wartung planen' }));
  const dialog = await screen.findByRole('dialog');
  await user.type(within(dialog).getByLabelText('Titel'), 'Kernel-Update');
  fireEvent.change(within(dialog).getByLabelText('Beginn'), {
    target: { value: '2026-11-02T08:00' },
  });
  fireEvent.change(within(dialog).getByLabelText('Ende'), {
    target: { value: '2026-11-02T10:00' },
  });
  // The datetime-local inputs keep their local value (WP-540): an ISO
  // string with "Z" is not a valid value and a browser empties the field.
  expect(within(dialog).getByLabelText('Beginn')).toHaveValue('2026-11-02T08:00');
  expect(within(dialog).getByLabelText('Ende')).toHaveValue('2026-11-02T10:00');
  await user.type(within(dialog).getByLabelText('Betroffene CIs suchen'), 'web');
  await user.click(await within(dialog).findByRole('button', { name: 'web-01 hinzufügen' }));
  await user.click(await within(dialog).findByRole('button', { name: 'web-02 hinzufügen' }));
  const selected = within(dialog).getByRole('list', { name: 'Ausgewählte CIs' });
  expect(within(selected).getByText('web-01')).toBeInTheDocument();
  expect(within(selected).getByText('web-02')).toBeInTheDocument();
  await user.click(within(dialog).getByRole('button', { name: 'Anlegen' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
}

function noDeliveryClaimed() {
  const text = document.body.textContent ?? '';
  expect(text).not.toMatch(/versendet|zugestellt|Kunden benachrichtigt/);
}

describe('MaintenancePage', () => {
  it('sends the selected CIs as ci_ids when a window is created', async () => {
    const backend = fakeBackend();
    const user = userEvent.setup();
    renderWithProviders(<MaintenancePage />, { route: '/maintenance' });

    await createWindowWithTwoCIs(user);

    expect(backend.posts).toHaveLength(1);
    expect(backend.posts[0]).toMatchObject({ title: 'Kernel-Update', ci_ids: ['ci-1', 'ci-2'] });
    // The local input values go to the API as ISO instants.
    expect(backend.posts[0]).toMatchObject({
      starts_at: new Date('2026-11-02T08:00').toISOString(),
      ends_at: new Date('2026-11-02T10:00').toISOString(),
    });
    expect(await screen.findByText('Kernel-Update')).toBeInTheDocument();
    expect(screen.getByText(/^2 betroffene CIs$/)).toBeInTheDocument();
  });

  it('shows the stored pending notification per client after notify and reload', async () => {
    const backend = fakeBackend();
    const user = userEvent.setup();
    const first = renderWithProviders(<MaintenancePage />, { route: '/maintenance' });
    await createWindowWithTwoCIs(user);

    await user.click(await screen.findByRole('button', { name: 'Kunden benachrichtigen' }));
    expect(await screen.findByRole('status')).toHaveTextContent(
      '2 Benachrichtigungen vorgemerkt – Versand ausstehend',
    );
    expect(await screen.findAllByTestId('maintenance-notification')).toHaveLength(2);
    noDeliveryClaimed();
    // A second click queues nothing new.
    await user.click(screen.getByRole('button', { name: 'Kunden benachrichtigen' }));
    await waitFor(() => expect(backend.notifications).toHaveLength(2));
    first.unmount();

    // Reload: a fresh page reads the stored notifications from the server.
    const notificationReads = () =>
      backend.fetchMock.mock.calls.filter(([input]) =>
        String(input).endsWith('/maintenance-windows/win-1/notifications'),
      ).length;
    const readsBefore = notificationReads();
    renderWithProviders(<MaintenancePage />, { route: '/maintenance' });
    await user.click(await screen.findByRole('button', { name: 'Benachrichtigungen' }));
    const rows = await screen.findAllByTestId('maintenance-notification');
    expect(notificationReads()).toBeGreaterThan(readsBefore);
    expect(rows).toHaveLength(2);
    const [alpha, beta] = rows as [HTMLElement, HTMLElement];
    await waitFor(() => expect(within(alpha).getByText('Kunde Alpha')).toBeInTheDocument());
    expect(within(beta).getByText('Kunde Beta')).toBeInTheDocument();
    for (const row of rows) {
      expect(within(row).getByText('ausstehend')).toBeInTheDocument();
      expect(within(row).getByText('Webhook')).toBeInTheDocument();
    }
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    noDeliveryClaimed();
  });
});
