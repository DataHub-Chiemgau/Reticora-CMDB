import { act } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useLocation } from 'react-router';
import i18n from '../i18n';
import { IncidentsPage } from './IncidentsPage';
import { renderWithProviders } from '../test/utils';

// The incident list (ALR-10, WP-1045): rows with root, cause, confidence and
// the affected connections and services, filters as query parameters,
// polling only while the tab is visible, Enter opens the detail, texts in
// de and en.

afterEach(async () => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
  await i18n.changeLanguage('de-DE');
});

const AT = new Date(Date.now() - 2 * 60 * 60 * 1000 - 5 * 60 * 1000).toISOString();

function incident(id: string, rootName: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    organization_id: 'org-1',
    client_id: 'client-a',
    site_id: null,
    root_alarm_id: `alarm-${id}`,
    root_ci_id: `ci-${id}`,
    cause_class: 'topology',
    confidence: 85,
    reasons: ['upstream down'],
    state: 'open',
    opened_at: AT,
    acknowledged_at: null,
    acknowledged_by: null,
    resolved_at: null,
    manual_root: false,
    affected_counts: { connections: 12, services: 3, cis: 40 },
    updated_at: AT,
    root: { alarm_id: `alarm-${id}`, ci_id: `ci-${id}`, ci_name: rootName },
    symptom_count: 4,
    ...extra,
  };
}

const DAY = 24 * 60 * 60 * 1000;

/**
 * fakeBackend answers the incident list like the API: from and to on
 * opened_at (422 when to is not after from), resolved_from on resolved_at;
 * it records the query parameters. The resolved incident opened 10 days
 * ago and was resolved yesterday.
 */
function fakeBackend() {
  const lists: URLSearchParams[] = [];
  const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const path = url.pathname.replace(/^\/api\/v1/, '');
      if (path === '/me/permissions')
        return json({ user_id: 'u-1', permissions: ['monitoring:read'] });
      if (path === '/monitoring/incidents') {
        lists.push(url.searchParams);
        const q = url.searchParams;
        const from = q.get('from') ? Date.parse(q.get('from')!) : undefined;
        const to = q.get('to') ? Date.parse(q.get('to')!) : undefined;
        if (from !== undefined && to !== undefined && to <= from)
          return json({ detail: 'to must be after from', field: 'to' }, 422);
        const resolvedFrom = q.get('resolved_from') ? Date.parse(q.get('resolved_from')!) : undefined;
        const state = q.get('state');
        const all =
          state === 'resolved'
            ? [
                incident('i-3', 'edge-3', {
                  state: 'resolved',
                  cause_class: 'collector_unreachable',
                  opened_at: new Date(Date.now() - 10 * DAY).toISOString(),
                  resolved_at: new Date(Date.now() - DAY).toISOString(),
                  affected_counts: {},
                }),
              ]
            : [
                incident('i-1', 'core-1'),
                incident('i-2', 'core-2', { state: 'acknowledged', confidence: 60 }),
              ];
        const data = all.filter((i) => {
          const opened = Date.parse(i.opened_at as string);
          const resolvedAt = i.resolved_at ? Date.parse(i.resolved_at as string) : undefined;
          return (
            (from === undefined || opened >= from) &&
            (to === undefined || opened < to) &&
            (resolvedFrom === undefined || resolvedAt === undefined || resolvedAt >= resolvedFrom)
          );
        });
        return json({ data, total: data.length, limit: 200, offset: 0, has_more: false });
      }
      return json({ detail: 'unexpected' }, 404);
    }),
  );
  return { lists };
}

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname + location.search}</div>;
}

function renderPage(route = '/incidents') {
  return renderWithProviders(
    <>
      <IncidentsPage />
      <LocationProbe />
    </>,
    { route },
  );
}

describe('IncidentsPage (WP-1045)', () => {
  it('Zeilen mit Wurzel, Ursache und Konfidenz sowie Anschluss- und Dienstzahl', async () => {
    const backend = fakeBackend();
    renderPage();
    const rows = await screen.findAllByTestId('incident-row');
    // The active incidents and the one resolved in the last 7 days.
    expect(rows).toHaveLength(3);
    const first = within(rows[0]!);
    expect(first.getByRole('link', { name: 'core-1' })).toHaveAttribute('href', '/cmdb/ci-i-1');
    expect(rows[0]).toHaveTextContent('Topologie');
    expect(rows[0]).toHaveTextContent('85 %');
    expect(rows[0]).toHaveTextContent('12');
    expect(rows[0]).toHaveTextContent('3');
    expect(rows[0]).toHaveTextContent('2 Std. 5 Min.');
    expect(rows[1]).toHaveTextContent('Bestätigt');
    expect(rows[2]).toHaveTextContent('Collector nicht erreichbar');
    expect(rows[2]).toHaveTextContent('Gelöst');
    // Without counters the row shows no number.
    expect(within(rows[2]!).getAllByText('–').length).toBeGreaterThanOrEqual(2);
    const states = backend.lists.map((p) => p.get('state'));
    expect(states).toContain('open,acknowledged');
    expect(states).toContain('resolved');
    // The resolved ones by resolved_at, whenever they opened: i-3 opened 10
    // days ago and was resolved yesterday.
    const resolved = backend.lists.find((p) => p.get('state') === 'resolved');
    expect(resolved?.get('resolved_from')).toBeTruthy();
    expect(resolved?.has('from')).toBe(false);
    expect(backend.lists.every((p) => p.get('limit') === '200')).toBe(true);
  });

  it('Filter setzen die Abfrageparameter', async () => {
    const backend = fakeBackend();
    const user = userEvent.setup();
    renderPage();
    await screen.findAllByTestId('incident-row');
    await user.selectOptions(screen.getByLabelText('Zustand'), 'resolved');
    await user.selectOptions(screen.getByLabelText('Ursachenklasse'), 'collector_unreachable');
    await waitFor(() =>
      expect(screen.getByTestId('location')).toHaveTextContent(
        'state=resolved&cause_class=collector_unreachable',
      ),
    );
    await user.type(screen.getByLabelText('Begonnen ab'), '2026-11-01');
    await waitFor(() =>
      expect(
        backend.lists.some(
          (p) =>
            p.get('state') === 'resolved' &&
            p.get('cause_class') === 'collector_unreachable' &&
            // The local start of the day, not UTC midnight.
            p.get('from') === new Date(2026, 10, 1).toISOString(),
        ),
      ).toBe(true),
    );
    await user.selectOptions(screen.getByLabelText('Zustand'), 'all');
    await waitFor(() =>
      expect(
        backend.lists.some(
          (p) => !p.has('state') && p.get('cause_class') === 'collector_unreachable',
        ),
      ).toBe(true),
    );
  });

  it('Standardansicht mit Bis-Datum vor dem Stichtag zeigt die Liste statt eines Fehlers', async () => {
    const backend = fakeBackend();
    // Only "to" three weeks back: no query sends a from after it.
    const past = new Date(Date.now() - 21 * DAY);
    const day = `${past.getFullYear()}-${String(past.getMonth() + 1).padStart(2, '0')}-${String(past.getDate()).padStart(2, '0')}`;
    renderPage(`/incidents?to=${day}`);
    await waitFor(() => expect(backend.lists.some((p) => p.get('state') === 'resolved')).toBe(true));
    expect(screen.queryByText(i18n.t('app.error'))).not.toBeInTheDocument();
    expect(backend.lists.every((p) => !p.has('from'))).toBe(true);
  });

  it('Polling stoppt bei verdecktem Tab', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const backend = fakeBackend();
    renderPage('/incidents?state=open');
    await screen.findAllByTestId('incident-row');
    const initial = backend.lists.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_500);
    });
    await waitFor(() => expect(backend.lists.length).toBeGreaterThan(initial));
    act(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    const hidden = backend.lists.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(45_000);
    });
    expect(backend.lists.length).toBe(hidden);
    // Visible again: the refresh resumes.
    act(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_500);
    });
    await waitFor(() => expect(backend.lists.length).toBeGreaterThan(hidden));
  });

  it('Enter auf einer Zeile navigiert zur Detailseite', async () => {
    fakeBackend();
    const user = userEvent.setup();
    renderPage();
    const rows = await screen.findAllByTestId('incident-row');
    rows[1]?.focus();
    await user.keyboard('{Enter}');
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/incidents/i-2'));
  });

  it('Texte in de und en', async () => {
    fakeBackend();
    renderPage();
    expect(await screen.findByRole('heading', { name: 'Störungen' })).toBeInTheDocument();
    await screen.findAllByTestId('incident-row');
    expect(screen.getByRole('columnheader', { name: 'Anschlüsse' })).toBeInTheDocument();
    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    expect(await screen.findByRole('heading', { name: 'Incidents' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Connections' })).toBeInTheDocument();
    const rows = screen.getAllByTestId('incident-row');
    expect(rows[0]).toHaveTextContent('Topology');
    expect(rows[2]).toHaveTextContent('Collector unreachable');
  });
});
