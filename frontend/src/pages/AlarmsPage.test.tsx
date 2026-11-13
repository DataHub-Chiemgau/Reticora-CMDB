import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useLocation } from 'react-router-dom';
import { AlarmsPage } from './AlarmsPage';
import { renderWithProviders } from '../test/utils';

// The alarm list (ALR-01, ALR-10, WP-991): filters in the URL, ?id opens the
// detail drawer with the transitions in time order, bulk Ack per alarm with
// a summary, Ack disabled without alert:acknowledge, keyboard operation.

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const AT = '2026-11-09T10:00:00Z';

function alarm(id: string, summary: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    organization_id: 'org-1',
    client_id: 'client-a',
    site_id: null,
    ci_id: 'ci-1',
    source_kind: 'rule',
    source_id: 'rule-1',
    series_key: id,
    labels: {},
    dedup_key: `rule:rule-1:${id}`,
    severity: 'critical',
    state: 'open',
    suppressed_reason: null,
    incident_id: null,
    occurrence_count: 3,
    first_seen_at: AT,
    last_seen_at: AT,
    opened_at: AT,
    acknowledged_at: null,
    acknowledged_by: null,
    resolved_at: null,
    summary,
    details: { value: 97 },
    updated_at: AT,
    ...extra,
  };
}

const ALARMS = [
  alarm('a-1', 'cpu core-1'),
  alarm('a-2', 'cpu core-2'),
  alarm('a-3', 'cpu core-3', {
    state: 'acknowledged',
    acknowledged_at: AT,
    acknowledged_by: 'u-2',
  }),
];

/** fakeBackend answers the alarm API; acks of the ids in failIds fail. */
function fakeBackend({
  permissions = ['monitoring:read', 'alert:acknowledge'],
  failIds = [] as string[],
} = {}) {
  const lists: URLSearchParams[] = [];
  const acks: { id: string; body: unknown }[] = [];
  const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const path = url.pathname.replace(/^\/api\/v1/, '');
      if (path === '/me/permissions') return json({ user_id: 'u-1', permissions });
      if (path === '/clients') {
        return json({
          data: [{ id: 'client-a', organization_id: 'org-1', name: 'Kunde Alpha' }],
          total: 1,
          limit: 200,
          offset: 0,
        });
      }
      if (path === '/monitoring/alarms' && method === 'GET') {
        lists.push(url.searchParams);
        return json({ data: ALARMS, total: 3, limit: 50, offset: 0, has_more: false });
      }
      const ack = path.match(/^\/monitoring\/alarms\/([^/]+)\/ack$/);
      if (ack && method === 'POST') {
        const id = ack[1] ?? '';
        acks.push({ id, body: JSON.parse(String(init?.body ?? '{}')) });
        if (failIds.includes(id)) return json({ title: 'Conflict', status: 409 }, 409);
        return json(alarm(id, 'x', { state: 'acknowledged' }));
      }
      const detail = path.match(/^\/monitoring\/alarms\/([^/]+)$/);
      if (detail && method === 'GET') {
        const id = detail[1] ?? '';
        return json({
          ...alarm(id, `detail ${id}`),
          // In the order of the server (at, id); Go trims trailing zeros
          // of the fraction, so the text does not sort by time.
          transitions: [
            {
              id: 't-1',
              organization_id: 'org-1',
              client_id: null,
              site_id: null,
              alarm_id: id,
              from_state: null,
              to_state: 'pending',
              from_severity: null,
              to_severity: 'critical',
              at: '2026-11-09T10:00:05Z',
              actor_id: null,
              reason: 'pending',
              comment: null,
            },
            {
              id: 't-2',
              organization_id: 'org-1',
              client_id: null,
              site_id: null,
              alarm_id: id,
              from_state: 'pending',
              to_state: 'open',
              from_severity: null,
              to_severity: null,
              at: '2026-11-09T10:00:05.5Z',
              actor_id: null,
              reason: 'raised',
              comment: null,
            },
          ],
          transitions_truncated: false,
        });
      }
      return json({ detail: `unexpected ${method} ${path}` }, 404);
    }),
  );
  return { lists, acks };
}

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname + location.search}</div>;
}

function renderPage(route = '/alarms') {
  return renderWithProviders(
    <>
      <AlarmsPage />
      <LocationProbe />
    </>,
    { route },
  );
}

describe('AlarmsPage (WP-991)', () => {
  it('Ack-Button deaktiviert ohne alert:acknowledge', async () => {
    fakeBackend({ permissions: ['monitoring:read'] });
    renderPage();
    const rows = await screen.findAllByTestId('alarm-row');
    expect(rows).toHaveLength(3);
    await waitFor(() =>
      expect(
        screen.getAllByText('Bestätigen erfordert das Recht alert:acknowledge.').length,
      ).toBeGreaterThan(0),
    );
    for (const box of screen.getAllByRole('checkbox')) expect(box).toBeDisabled();
    expect(screen.getByRole('button', { name: /Auswahl bestätigen/ })).toBeDisabled();
  });

  it('Bulk-Ack bestätigt die Auswahl und meldet Fehler je Alarm', async () => {
    const backend = fakeBackend({ failIds: ['a-2'] });
    const user = userEvent.setup();
    renderPage();
    await screen.findAllByTestId('alarm-row');
    const boxes = await screen.findAllByRole('checkbox');
    const [first, second] = boxes;
    await waitFor(() => expect(first).toBeEnabled());
    await user.click(first!);
    await user.click(second!);
    await user.type(screen.getByLabelText('Kommentar (optional)'), 'wird geprüft');
    await user.click(screen.getByRole('button', { name: 'Auswahl bestätigen (2)' }));
    expect(await screen.findByRole('status')).toHaveTextContent('1 bestätigt, 1 fehlgeschlagen');
    expect(backend.acks.map((a) => a.id)).toEqual(['a-1', 'a-2']);
    expect(backend.acks[0]?.body).toEqual({ comment: 'wird geprüft' });
  });

  it('Nur offene Alarme sind auswählbar', async () => {
    const backend = fakeBackend();
    const user = userEvent.setup();
    renderPage();
    const rows = await screen.findAllByTestId('alarm-row');
    const boxes = await screen.findAllByRole('checkbox');
    await waitFor(() => expect(boxes[0]).toBeEnabled());
    expect(boxes[2]).toBeDisabled();
    rows[2]?.focus();
    await user.keyboard(' ');
    expect(rows[2]).toHaveAttribute('aria-selected', 'false');
    expect(screen.getByRole('button', { name: /Auswahl bestätigen/ })).toBeDisabled();
    expect(backend.acks).toHaveLength(0);
  });

  it('Filter stehen in der URL, ?id öffnet den Drawer', async () => {
    const backend = fakeBackend();
    const user = userEvent.setup();
    renderPage('/alarms?ci_id=ci-1&id=a-2');
    expect(await screen.findByTestId('alarm-drawer')).toBeInTheDocument();
    expect(backend.lists[0]?.get('ci_id')).toBe('ci-1');
    expect(backend.lists[0]?.get('state')).toBe('open,acknowledged');
    expect(backend.lists[0]?.get('suppressed')).toBe('false');
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByTestId('alarm-drawer')).not.toBeInTheDocument());
    await user.selectOptions(screen.getByLabelText('Schwere'), 'warning');
    await waitFor(() =>
      expect(screen.getByTestId('location')).toHaveTextContent('severity=warning'),
    );
    expect(screen.getByTestId('location')).toHaveTextContent('ci_id=ci-1');
    await waitFor(() =>
      expect(backend.lists.some((p) => p.get('severity') === 'warning')).toBe(true),
    );
    await user.selectOptions(screen.getByLabelText('Unterdrückte'), 'only');
    await waitFor(() =>
      expect(backend.lists.some((p) => p.get('suppressed') === 'true')).toBe(true),
    );
  });

  it('Drawer zeigt die Transitionen in zeitlicher Reihenfolge', async () => {
    fakeBackend();
    renderPage('/alarms?id=a-1');
    const drawer = await screen.findByTestId('alarm-drawer');
    const items = within(drawer).getAllByTestId('alarm-transition');
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent('neu → Ausstehend');
    expect(items[1]).toHaveTextContent('Ausstehend → Offen');
    expect(items[1]).toHaveTextContent('raised');
  });

  it('Enter öffnet, Esc schließt mit Fokusrückgabe', async () => {
    fakeBackend();
    const user = userEvent.setup();
    renderPage();
    const rows = await screen.findAllByTestId('alarm-row');
    rows[1]?.focus();
    await user.keyboard('{Enter}');
    expect(await screen.findByTestId('alarm-drawer')).toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent('id=a-2');
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByTestId('alarm-drawer')).not.toBeInTheDocument());
    await waitFor(() => expect(screen.getAllByTestId('alarm-row')[1]).toHaveFocus());
  });
});
