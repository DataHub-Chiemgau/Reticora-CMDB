import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { SLAPage } from './SLAPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
import i18n from '../i18n';

function paginated<T>(data: T[], extra: Record<string, unknown> = {}) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false, ...extra };
}

const POLICY = {
  id: 'sla-1',
  organization_id: 'org-1',
  name: 'High priority',
  priority: 'high',
  response_target_minutes: 30,
  resolution_target_minutes: 240,
  business_calendar: false,
  calendar_id: null,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const CLIENT_ID = 'c0000000-0000-4000-8000-000000000001';

const ORG_CALENDAR = {
  id: 'b0000000-0000-4000-8000-000000000001',
  organization_id: 'org-1',
  client_id: null,
  name: 'Bürozeiten Org',
  timezone: 'Europe/Berlin',
  weekly_hours: [{ weekday: 1, start: '08:00', end: '17:00' }],
  holidays: [{ date: '2026-12-24', name: 'Heiligabend' }],
  is_default: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const CLIENT_CALENDAR = {
  ...ORG_CALENDAR,
  id: 'b0000000-0000-4000-8000-000000000002',
  client_id: CLIENT_ID,
  name: 'Kunde Nord',
  timezone: null,
  is_default: false,
};

const OTHER_CLIENT_CALENDAR = {
  ...ORG_CALENDAR,
  id: 'b0000000-0000-4000-8000-000000000003',
  client_id: 'c0000000-0000-4000-8000-000000000099',
  name: 'Fremder Kunde',
  is_default: false,
};

interface Call {
  method: string;
  path: string;
  params: URLSearchParams;
  body?: unknown;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

/**
 * stubApi answers by "METHOD path" (path without /api/v1) and records every
 * request; a write handler decides the answer from the body.
 */
function stubApi({
  permissions = ['sla:read', 'sla:write'],
  calendars = [ORG_CALENDAR, CLIENT_CALENDAR, OTHER_CLIENT_CALENDAR],
  breaches = [] as unknown[],
  writes = {} as Record<string, (body: unknown) => Response>,
  calendarPages,
}: {
  permissions?: string[];
  calendars?: unknown[];
  breaches?: unknown[];
  writes?: Record<string, (body: unknown) => Response>;
  calendarPages?: (params: URLSearchParams) => Response;
} = {}) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const method = init?.method ?? 'GET';
      const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined;
      calls.push({ method, path, params: url.searchParams, body });
      const write = writes[`${method} ${path}`];
      if (write) return write(body);
      if (method !== 'GET') return json({ detail: `unexpected ${method} ${path}` }, 404);
      if (path === '/me/permissions') return json({ user_id: 'user-1', permissions });
      if (path === '/slas/breaches') return json(paginated(breaches));
      if (path === '/slas') return json(paginated([POLICY]));
      if (path === '/clients') {
        return json(
          paginated([
            { id: CLIENT_ID, organization_id: 'org-1', name: 'Kunde Nord', slug: 'nord' },
          ]),
        );
      }
      if (path === '/business-calendars') {
        return calendarPages ? calendarPages(url.searchParams) : json(paginated(calendars));
      }
      return json({ detail: `unexpected ${path}` }, 404);
    }),
  );
  return calls;
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function openCalendars() {
  fireEvent.click(await screen.findByRole('button', { name: /Business-Kalender/ }));
}

describe('SLAPage', () => {
  it('renders SLA policies and empty breach state', async () => {
    stubFetchRoutes({
      '/slas/breaches': paginated([]),
      '/slas': paginated([POLICY]),
      '/business-calendars': paginated([]),
      '/clients': paginated([]),
      '/me/permissions': { user_id: 'user-1', permissions: ['sla:read'] },
    });

    renderWithProviders(<SLAPage />, { route: '/slas' });

    expect(await screen.findByText('High priority')).toBeInTheDocument();
    expect(screen.getByText('Keine Verletzungen')).toBeInTheDocument();
  });

  it('keeps the breach list', async () => {
    stubApi({
      breaches: [
        {
          id: 'state-1',
          organization_id: 'org-1',
          ticket_id: 'ticket-77',
          sla_id: 'sla-1',
          response_due_at: '2026-01-01T10:00:00Z',
          resolution_due_at: '2026-01-02T10:00:00Z',
          response_breached: true,
          resolution_breached: false,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ],
    });
    renderWithProviders(<SLAPage />, { route: '/slas' });

    const region = await screen.findByRole('region', { name: 'Verletzungen' });
    expect(within(region).getByText('ticket-77')).toBeInTheDocument();
    expect(within(region).getByText('Verletzt')).toBeInTheDocument();
  });

  it('creates a calendar with time zone, weekly hours and a holiday', async () => {
    const calls = stubApi({
      calendars: [],
      writes: {
        'POST /business-calendars': (body) => json({ ...ORG_CALENDAR, ...(body as object) }, 201),
      },
    });
    renderWithProviders(<SLAPage />, { route: '/slas' });
    await openCalendars();
    fireEvent.click(await screen.findByRole('button', { name: 'Kalender anlegen' }));

    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Service Desk' } });
    fireEvent.change(within(dialog).getByLabelText('Kunde'), { target: { value: CLIENT_ID } });
    fireEvent.change(within(dialog).getByLabelText('Zeitzone'), {
      target: { value: 'Europe/Vienna' },
    });
    // Saturday 09:00–12:00 in addition to the standard Monday to Friday.
    fireEvent.click(
      within(dialog).getByRole('button', { name: 'Zeitfenster für Samstag hinzufügen' }),
    );
    fireEvent.change(within(dialog).getByLabelText('Samstag, Zeitfenster 1: Beginn'), {
      target: { value: '09:00' },
    });
    fireEvent.change(within(dialog).getByLabelText('Samstag, Zeitfenster 1: Ende'), {
      target: { value: '12:00' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Feiertag hinzufügen' }));
    fireEvent.change(within(dialog).getByLabelText('Feiertag 1: Datum'), {
      target: { value: '2026-12-25' },
    });
    fireEvent.change(within(dialog).getByLabelText('Feiertag 1: Name'), {
      target: { value: '1. Weihnachtstag' },
    });
    fireEvent.click(within(dialog).getByLabelText(/Standardkalender/));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path === '/business-calendars')).toBe(true),
    );
    const post = calls.find((c) => c.method === 'POST' && c.path === '/business-calendars')!;
    expect(post.body).toEqual({
      name: 'Service Desk',
      client_id: CLIENT_ID,
      timezone: 'Europe/Vienna',
      weekly_hours: [
        { weekday: 1, start: '08:00', end: '18:00' },
        { weekday: 2, start: '08:00', end: '18:00' },
        { weekday: 3, start: '08:00', end: '18:00' },
        { weekday: 4, start: '08:00', end: '18:00' },
        { weekday: 5, start: '08:00', end: '18:00' },
        { weekday: 6, start: '09:00', end: '12:00' },
      ],
      holidays: [{ date: '2026-12-25', name: '1. Weihnachtstag' }],
      is_default: true,
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('shows a 400 on overlapping weekly hours at the field', async () => {
    stubApi({
      calendars: [],
      writes: {
        'POST /business-calendars': () =>
          json(
            {
              type: 'urn:reticora:problem:invalid-field',
              title: 'Bad Request',
              status: 400,
              detail: 'weekly_hours: windows on weekday 1 overlap',
              field: 'weekly_hours',
            },
            400,
          ),
      },
    });
    renderWithProviders(<SLAPage />, { route: '/slas' });
    await openCalendars();
    fireEvent.click(await screen.findByRole('button', { name: 'Kalender anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Doppelt' } });
    fireEvent.click(
      within(dialog).getByRole('button', { name: 'Zeitfenster für Montag hinzufügen' }),
    );
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    const weekly = within(dialog).getByRole('group', { name: 'Wochenzeiten' });
    expect(await within(weekly).findByText(/windows on weekday 1 overlap/)).toBeInTheDocument();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('shows a 409 of a second default at the default flag', async () => {
    stubApi({
      calendars: [],
      writes: {
        'POST /business-calendars': () =>
          json(
            {
              type: 'urn:reticora:problem:conflict',
              title: 'Conflict',
              status: 409,
              detail: 'is_default: already exists',
              field: 'is_default',
            },
            409,
          ),
      },
    });
    renderWithProviders(<SLAPage />, { route: '/slas' });
    await openCalendars();
    fireEvent.click(await screen.findByRole('button', { name: 'Kalender anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Zweiter' } });
    fireEvent.click(within(dialog).getByLabelText(/Standardkalender/));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    const checkbox = within(dialog).getByLabelText(/Standardkalender/);
    await waitFor(() => expect(checkbox).toHaveAttribute('aria-invalid', 'true'));
    expect(checkbox).toHaveAccessibleDescription(/bereits einen Standardkalender/);
  });

  it('shows a 403 of an org-wide calendar as message', async () => {
    stubApi({
      calendars: [],
      writes: {
        'POST /business-calendars': () =>
          json({ title: 'Forbidden', status: 403, detail: 'row-level security' }, 403),
      },
    });
    renderWithProviders(<SLAPage />, { route: '/slas' });
    await openCalendars();
    fireEvent.click(await screen.findByRole('button', { name: 'Kalender anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Org' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(/Keine Berechtigung/);
  });

  it('edits a calendar without sending client_id', async () => {
    const calls = stubApi({
      writes: {
        [`PATCH /business-calendars/${CLIENT_CALENDAR.id}`]: (body) =>
          json({ ...CLIENT_CALENDAR, ...(body as object) }),
      },
    });
    renderWithProviders(<SLAPage />, { route: '/slas' });
    await openCalendars();
    fireEvent.click(await screen.findByRole('button', { name: 'Kalender Kunde Nord bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByLabelText('Kunde')).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Kunde Nord 2' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH')!;
    expect(patch.body).not.toHaveProperty('client_id');
    expect(patch.body).toEqual({
      name: 'Kunde Nord 2',
      timezone: null,
      weekly_hours: [{ weekday: 1, start: '08:00', end: '17:00' }],
      holidays: [{ date: '2026-12-24', name: 'Heiligabend' }],
      is_default: false,
    });
  });

  it('pages the calendars over next_cursor', async () => {
    const calls = stubApi({
      calendarPages: (params) =>
        params.get('cursor') === 'next-1'
          ? json(paginated([CLIENT_CALENDAR], { total: 2 }))
          : json(paginated([ORG_CALENDAR], { total: 2, has_more: true, next_cursor: 'next-1' })),
    });
    renderWithProviders(<SLAPage />, { route: '/slas' });
    await openCalendars();
    const section = await screen.findByRole('region', { name: /^Business-Kalender \(/ });
    expect(await within(section).findByText('Bürozeiten Org')).toBeInTheDocument();
    fireEvent.click(within(section).getByRole('button', { name: 'Weitere laden' }));
    expect(
      await within(section).findByRole('button', { name: 'Kalender Kunde Nord bearbeiten' }),
    ).toBeInTheDocument();
    expect(
      calls.some((c) => c.path === '/business-calendars' && c.params.get('cursor') === 'next-1'),
    ).toBe(true);
  });

  it('sends calendar_id and the business hours switch of the policy form', async () => {
    const calls = stubApi({
      writes: { 'POST /slas': (body) => json({ ...POLICY, ...(body as object) }, 201) },
    });
    renderWithProviders(<SLAPage />, { route: '/slas' });
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Nord kritisch' } });
    fireEvent.change(screen.getByLabelText('Kunde'), { target: { value: CLIENT_ID } });
    fireEvent.change(screen.getByLabelText('Priorität'), { target: { value: 'critical' } });
    const calendar = screen.getByLabelText('Business-Kalender');
    await waitFor(() =>
      expect(within(calendar).getByRole('option', { name: 'Kunde Nord' })).toBeInTheDocument(),
    );
    // Only calendars without client or of the policy's client are offered.
    expect(within(calendar).getByRole('option', { name: 'Bürozeiten Org' })).toBeInTheDocument();
    expect(within(calendar).queryByRole('option', { name: 'Fremder Kunde' })).toBeNull();
    fireEvent.change(calendar, { target: { value: CLIENT_CALENDAR.id } });
    fireEvent.click(screen.getByLabelText('Geschäftszeiten anwenden'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST' && c.path === '/slas')!;
    expect(post.body).toEqual({
      name: 'Nord kritisch',
      client_id: CLIENT_ID,
      priority: 'critical',
      response_target_minutes: 60,
      resolution_target_minutes: 480,
      calendar_id: CLIENT_CALENDAR.id,
      business_calendar: false,
    });
  });

  it('shows a 400 on calendar_id at the field', async () => {
    stubApi({
      writes: {
        'POST /slas': () =>
          json(
            {
              title: 'Bad Request',
              status: 400,
              detail: 'calendar_id: must belong to no client or to the policy client',
              field: 'calendar_id',
            },
            400,
          ),
      },
    });
    renderWithProviders(<SLAPage />, { route: '/slas' });
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'X' } });
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    await waitFor(() =>
      expect(screen.getByLabelText('Business-Kalender')).toHaveAttribute('aria-invalid', 'true'),
    );
    expect(screen.getByLabelText('Business-Kalender')).toHaveAccessibleDescription(
      /must belong to no client/,
    );
  });

  it('offers no writes without sla:write', async () => {
    stubApi({ permissions: ['sla:read'] });
    renderWithProviders(<SLAPage />, { route: '/slas' });
    await openCalendars();
    expect(await screen.findByText('Bürozeiten Org')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Kalender anlegen' })).toBeNull();
    expect(screen.queryByRole('button', { name: /bearbeiten/ })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'SLA-Richtlinie anlegen' })).toBeNull();
  });
});
