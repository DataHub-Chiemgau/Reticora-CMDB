import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { DiscoveryPage } from './DiscoveryPage';
import { renderWithProviders } from '../test/utils';

/*
 * The Discovery page: scopes with all fields of DIS-09 (F-107), deleting
 * only after a confirmation, feedback for failed scope actions and the
 * controls only with discovery:manage (F-267, WP-496), and the hints why a
 * collector delivers nothing: no edge connection, no enabled scope.
 */

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 100, offset: 0, has_more: false };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, body: Record<string, unknown>) {
  return new Response(JSON.stringify({ status, title: 'Error', ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

const manage = ['discovery:read', 'discovery:manage', 'collector:manage', 'review:resolve'];

const collector = {
  id: 'c1',
  organization_id: 'org-1',
  client_id: 'client-1',
  name: 'collector-a',
  status: 'active',
  last_heartbeat_at: '2026-10-13T08:00:00Z',
  config: {},
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const scope = {
  id: 's1',
  organization_id: 'org-1',
  client_id: 'client-1',
  site_id: 'site-1',
  collector_id: 'c1',
  name: 'Office LAN',
  kind: 'cidr',
  target: '10.0.0.0/24',
  vrf_id: 'vrf-1',
  schedule: '24h',
  credential_ids: ['cred-org'],
  plugins: ['sweep', 'snmp'],
  enabled: true,
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
};

const sites = [
  { id: 'site-1', organization_id: 'org-1', client_id: 'client-1', name: 'Werk Nord' },
  { id: 'site-2', organization_id: 'org-1', client_id: 'client-1', name: 'Werk Süd' },
];

const vrfs = [
  { id: 'vrf-0', organization_id: 'org-1', name: 'global', is_default: true },
  { id: 'vrf-1', organization_id: 'org-1', name: 'kunde-a', is_default: false },
];

const credentials = [
  { id: 'cred-org', organization_id: 'org-1', name: 'SNMP Org', kind: 'snmp_v2c', key_version: 1 },
  {
    id: 'cred-client',
    organization_id: 'org-1',
    client_id: 'client-1',
    name: 'SSH Client A',
    kind: 'ssh_password',
    key_version: 1,
  },
  {
    id: 'cred-foreign',
    organization_id: 'org-1',
    client_id: 'client-2',
    name: 'SSH Client B',
    kind: 'ssh_password',
    key_version: 1,
  },
];

interface Options {
  permissions?: string[];
  collectors?: unknown[];
  scopes?: unknown[];
  suggestions?: unknown[];
  /** write answers every POST, PATCH and DELETE; default 200 with the scope. */
  write?: (url: string, method: string, body: unknown) => Response;
  /** session answers GET /me (session claims); without it /me is 404. */
  session?: unknown;
  /** clients answers GET /clients. */
  clients?: unknown[];
}

interface Call {
  url: string;
  method: string;
  body?: unknown;
}

/** stubDiscovery answers the reads of the page and hands every write to `write`. */
function stubDiscovery({
  permissions = manage,
  collectors = [collector],
  scopes = [scope],
  suggestions = [],
  write = () => json(scope),
  session,
  clients = [],
}: Options = {}) {
  const calls: Call[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString();
    const method = (init?.method ?? 'GET').toUpperCase();
    const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url, method, body });
    if (method !== 'GET') return write(url, method, body);
    if (url.includes('/me/permissions')) return json({ permissions });
    if (url.endsWith('/api/v1/me') && session) return json(session);
    if (url.includes('/clients')) return json(paginated(clients));
    if (url.includes('/review-items')) return json(paginated(suggestions));
    if (url.includes('/discovery-scopes?collector_id=c1')) return json(paginated(scopes));
    if (url.includes('/collectors')) return json(paginated(collectors));
    if (url.includes('/sites')) return json(paginated(sites));
    if (url.includes('/vrfs')) return json(paginated(vrfs));
    if (url.includes('/credentials')) return json(paginated(credentials));
    return problem(404, { detail: `unexpected request: ${url}` });
  });
  vi.stubGlobal('fetch', fetchMock);
  return calls;
}

function writes(calls: Call[], method: string) {
  return calls.filter((c) => c.method === method);
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('DiscoveryPage scopes (DIS-09)', () => {
  it('creates a scope with name, site, VRF, plugins and credentials (F-107)', async () => {
    const calls = stubDiscovery({ write: () => json({ ...scope, id: 's2' }, 201) });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    expect(await screen.findByText('10.0.0.0/24')).toBeInTheDocument();
    expect(screen.getByText(/sweep, snmp/)).toBeInTheDocument();
    fireEvent.click(await screen.findByRole('button', { name: 'Scope anlegen' }));

    const dialog = await screen.findByRole('dialog');
    const form = within(dialog);
    // The hints of E-67 (no credential selected) and E-84 (cron time zone).
    expect(
      form.getByText('Ohne Auswahl gelten die Credentials des Kunden und der Organisation.'),
    ).toBeInTheDocument();
    expect(form.getByText(/Cron in der Zeitzone der Organisation/)).toBeInTheDocument();

    fireEvent.change(form.getByLabelText('Name'), { target: { value: 'Lager' } });
    fireEvent.change(form.getByLabelText('Ziel'), { target: { value: '192.168.1.0/24' } });
    await form.findByRole('option', { name: 'Werk Süd' });
    fireEvent.change(form.getByLabelText('Site'), { target: { value: 'site-2' } });
    await form.findByRole('option', { name: 'kunde-a' });
    fireEvent.change(form.getByLabelText('VRF'), { target: { value: 'vrf-1' } });
    fireEvent.click(form.getByLabelText('SSH'));
    fireEvent.click(await form.findByLabelText(/SSH Client A/));
    fireEvent.click(form.getByLabelText(/SNMP Org/));
    // Only organization-wide credentials and those of the scope's client.
    expect(form.queryByLabelText(/SSH Client B/)).toBeNull();

    fireEvent.click(form.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(writes(calls, 'POST')).toHaveLength(1));
    const post = writes(calls, 'POST')[0]!;
    expect(post.url).toMatch(/\/discovery-scopes$/);
    expect(post.body).toEqual({
      collector_id: 'c1',
      name: 'Lager',
      kind: 'cidr',
      target: '192.168.1.0/24',
      site_id: 'site-2',
      vrf_id: 'vrf-1',
      schedule: '24h',
      credential_ids: ['cred-client', 'cred-org'],
      plugins: ['sweep', 'snmp', 'ssh'],
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('edits a scope: the form starts with its values and sends every field (F-107)', async () => {
    const calls = stubDiscovery();
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    fireEvent.click(await screen.findByRole('button', { name: 'Scope Office LAN bearbeiten' }));
    const form = within(await screen.findByRole('dialog'));
    expect(form.getByLabelText('Name')).toHaveValue('Office LAN');
    expect(form.getByLabelText('Ziel')).toHaveValue('10.0.0.0/24');
    await form.findByRole('option', { name: 'kunde-a' });
    expect(form.getByLabelText('VRF')).toHaveValue('vrf-1');
    expect(await form.findByLabelText(/SNMP Org/)).toBeChecked();

    fireEvent.change(form.getByLabelText('Name'), { target: { value: 'Office LAN 2' } });
    fireEvent.change(form.getByLabelText('VRF'), { target: { value: '' } });
    fireEvent.click(form.getByLabelText(/SNMP Org/));
    fireEvent.click(form.getByLabelText('Redfish'));
    fireEvent.change(form.getByLabelText(/Zeitplan/), { target: { value: '0 2 * * *' } });
    fireEvent.click(form.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(writes(calls, 'PATCH')).toHaveLength(1));
    const patch = writes(calls, 'PATCH')[0]!;
    expect(patch.url).toMatch(/\/discovery-scopes\/s1$/);
    expect(patch.body).toEqual({
      name: 'Office LAN 2',
      kind: 'cidr',
      target: '10.0.0.0/24',
      site_id: 'site-1',
      vrf_id: '',
      schedule: '0 2 * * *',
      credential_ids: [],
      plugins: ['sweep', 'snmp', 'redfish'],
    });
  });

  it('shows a 422 of the server at its field and keeps the form open', async () => {
    stubDiscovery({
      write: () =>
        problem(422, {
          type: 'urn:reticora:problem:invalid-field',
          detail: 'target: not a CIDR',
          field: 'target',
        }),
    });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    fireEvent.click(await screen.findByRole('button', { name: 'Scope anlegen' }));
    const form = within(await screen.findByRole('dialog'));
    fireEvent.change(form.getByLabelText('Ziel'), { target: { value: '10.0.0.0/33' } });
    fireEvent.click(form.getByRole('button', { name: 'Speichern' }));

    expect(await form.findByText('Ungültiger Wert: not a CIDR')).toBeInTheDocument();
    expect(form.getByLabelText('Ziel')).toHaveAttribute('aria-invalid', 'true');
    expect(form.getByRole('alert')).toHaveTextContent('Der Scope wurde nicht gespeichert');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('deletes only after the confirmation (F-267)', async () => {
    const calls = stubDiscovery({ write: () => new Response(null, { status: 204 }) });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    fireEvent.click(await screen.findByRole('button', { name: 'Scope Office LAN löschen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Scope löschen?' });
    expect(dialog).toHaveTextContent('Office LAN');
    expect(writes(calls, 'DELETE')).toHaveLength(0);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Abbrechen' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(writes(calls, 'DELETE')).toHaveLength(0);

    fireEvent.click(screen.getByRole('button', { name: 'Scope Office LAN löschen' }));
    const again = await screen.findByRole('dialog', { name: 'Scope löschen?' });
    fireEvent.click(within(again).getByRole('button', { name: 'Endgültig löschen' }));
    await waitFor(() => expect(writes(calls, 'DELETE')).toHaveLength(1));
    expect(writes(calls, 'DELETE')[0]!.url).toMatch(/\/discovery-scopes\/s1$/);
  });

  it('shows an error when deleting or switching fails with 500 (F-267)', async () => {
    stubDiscovery({ write: () => problem(500, { detail: 'database unavailable' }) });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    fireEvent.click(await screen.findByRole('button', { name: 'Scope Office LAN löschen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Scope löschen?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Endgültig löschen' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Scope Office LAN konnte nicht gelöscht werden: database unavailable',
    );

    fireEvent.click(screen.getByRole('button', { name: 'Scope Office LAN deaktivieren' }));
    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(
        'Scope Office LAN konnte nicht umgeschaltet werden: database unavailable',
      ),
    );
  });

  it('shows an error when resolving a suggestion fails with 500 (F-267)', async () => {
    stubDiscovery({
      scopes: [],
      suggestions: [
        {
          id: 'r1',
          kind: 'scope_suggested',
          status: 'open',
          payload: { collector_id: 'c1', collector_name: 'collector-a', cidr: '192.168.50.0/24' },
          created_at: '2026-10-13T08:00:00Z',
        },
      ],
      write: () => problem(500, { detail: 'database unavailable' }),
    });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    fireEvent.click(await screen.findByRole('button', { name: 'Als Scope übernehmen' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Der Vorschlag konnte nicht bearbeitet werden: database unavailable',
    );
  });

  it('offers no scope controls without discovery:manage', async () => {
    const calls = stubDiscovery({
      permissions: ['discovery:read', 'collector:manage', 'review:resolve'],
      suggestions: [
        {
          id: 'r1',
          kind: 'scope_suggested',
          status: 'open',
          payload: { collector_id: 'c1', cidr: '192.168.50.0/24' },
          created_at: '2026-10-13T08:00:00Z',
        },
      ],
    });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    // The rights have loaded: collector:manage offers the enrollment.
    expect(await screen.findByRole('button', { name: 'Collector verbinden' })).toBeInTheDocument();
    expect(await screen.findByText('10.0.0.0/24')).toBeInTheDocument();
    expect(screen.getByText('192.168.50.0/24')).toBeInTheDocument();
    for (const name of [
      'Scope anlegen',
      'Scope Office LAN bearbeiten',
      'Scope Office LAN deaktivieren',
      'Scope Office LAN löschen',
      'Als Scope übernehmen',
      'Verwerfen',
    ]) {
      expect(screen.queryByRole('button', { name }), name).toBeNull();
    }
    expect(calls.filter((c) => c.method !== 'GET')).toHaveLength(0);
  });

  it('shows the state and the causes of failed scope runs (DIS-08)', async () => {
    stubDiscovery({
      scopes: [
        {
          ...scope,
          plugins: ['sweep', 'redfish'],
          last_run_at: '2026-10-13T08:00:00Z',
          health: {
            state: 'partial',
            errors: [
              {
                cause: 'auth_failed',
                protocol: 'redfish',
                target: '10.0.0.9',
                message: 'HTTP 401: Unauthorized',
              },
            ],
          },
        },
      ],
    });

    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    expect(await screen.findByText('Teilweise fehlgeschlagen')).toBeInTheDocument();
    expect(screen.getByText('Anmeldung fehlgeschlagen')).toBeInTheDocument();
    expect(screen.getByText(/10\.0\.0\.9/)).toBeInTheDocument();
    expect(screen.getByText(/HTTP 401: Unauthorized/)).toBeInTheDocument();
  });

  it('confirms a suggested scope (INS-06)', async () => {
    const calls = stubDiscovery({
      scopes: [],
      suggestions: [
        {
          id: 'r1',
          kind: 'scope_suggested',
          status: 'open',
          payload: { collector_id: 'c1', collector_name: 'collector-a', cidr: '192.168.50.0/24' },
          created_at: '2026-10-13T08:00:00Z',
        },
      ],
      write: () => json({ id: 'r1', status: 'resolved' }),
    });

    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    expect(await screen.findByText('192.168.50.0/24')).toBeInTheDocument();
    expect(screen.getByText('gemeldet von collector-a')).toBeInTheDocument();
    fireEvent.click(await screen.findByRole('button', { name: 'Als Scope übernehmen' }));

    await waitFor(() => {
      const resolve = writes(calls, 'POST').find((c) => c.url.includes('/review-items/r1/resolve'));
      expect(resolve?.body).toEqual({ action: 'accept' });
    });
  });
});

const suggestion = {
  id: 'r1',
  kind: 'scope_suggested',
  status: 'open',
  payload: { collector_id: 'c1', collector_name: 'collector-a', cidr: '192.168.50.0/24' },
  created_at: '2026-10-13T08:00:00Z',
};

describe('DiscoveryPage collector hints (COL-08, INS-06)', () => {
  it('warns about a missing edge connection when offline or without a heartbeat', async () => {
    for (const state of [
      { status: 'offline', last_heartbeat_at: '2026-10-13T08:00:00Z' },
      { status: 'pending', last_heartbeat_at: undefined },
      { status: 'active', last_heartbeat_at: undefined },
    ]) {
      stubDiscovery({ collectors: [{ ...collector, ...state }] });
      const { unmount } = renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

      const note = await screen.findByText(/Keine Edge-Verbindung/);
      const hint = note.closest('[role="note"]');
      expect(hint, state.status).not.toBeNull();
      expect(hint).toHaveTextContent('4222/TCP (mTLS)');
      expect(hint).toHaveTextContent(/Firewall/);
      expect(hint).toHaveTextContent(/nach dem Enrollment nicht neu gestartet/);
      expect(hint).toHaveTextContent(/Server-Zertifikat passt nicht zum Hostnamen/);
      expect(hint).toHaveTextContent(
        'docs/troubleshooting-installation.md, Abschnitt „Collector delivers nothing“',
      );
      unmount();
      vi.unstubAllGlobals();
    }
  });

  it('shows no hint for a connected collector with an enabled scope, nor for a disabled one', async () => {
    stubDiscovery();
    const { unmount } = renderWithProviders(<DiscoveryPage />, { route: '/discovery' });
    expect(await screen.findByText('10.0.0.0/24')).toBeInTheDocument();
    expect(screen.queryAllByRole('note')).toHaveLength(0);
    // Without suggestions their anchor stays empty (hidden, no gap).
    expect(document.getElementById('scope-suggestions')).toBeEmptyDOMElement();
    unmount();
    vi.unstubAllGlobals();

    stubDiscovery({
      collectors: [{ ...collector, status: 'disabled', last_heartbeat_at: undefined }],
      scopes: [],
    });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });
    expect(await screen.findByText('Deaktiviert')).toBeInTheDocument();
    expect(await screen.findByText(/Noch keine Scopes/)).toBeInTheDocument();
    expect(screen.queryAllByRole('note')).toHaveLength(0);
  });

  it('says the collector scans nothing without an enabled scope and links to its suggestions', async () => {
    stubDiscovery({
      scopes: [{ ...scope, enabled: false }],
      suggestions: [
        suggestion,
        { ...suggestion, id: 'r2', payload: { ...suggestion.payload, cidr: '192.168.51.0/24' } },
        { ...suggestion, id: 'r3', payload: { collector_id: 'c2', cidr: '10.9.0.0/24' } },
      ],
    });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    const text = await screen.findByText(
      'Kein Discovery-Scope – der Collector scannt nichts. Vorgeschlagene Subnetze bestätigen oder Scope anlegen.',
    );
    const hint = text.closest('[role="note"]') as HTMLElement;
    expect(hint).not.toBeNull();
    // Only the suggestions of this collector count.
    const link = within(hint).getByRole('link', { name: 'Vorgeschlagene Subnetze anzeigen (2)' });
    expect(link).toHaveAttribute('href', '#scope-suggestions');
    const target = document.getElementById('scope-suggestions');
    expect(target).not.toBeNull();
    expect(within(target!).getByText('Vorgeschlagene Scopes')).toBeInTheDocument();
    expect(within(target!).getAllByRole('button', { name: 'Als Scope übernehmen' })).toHaveLength(
      3,
    );
    // The user may confirm and add scopes: no note on the rights.
    expect(hint).not.toHaveTextContent('review:resolve');
  });

  it('names the rights needed to confirm when the user lacks them', async () => {
    stubDiscovery({
      permissions: ['discovery:read', 'discovery:manage', 'collector:manage'],
      scopes: [],
      suggestions: [suggestion],
    });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    const text = await screen.findByText(/Kein Discovery-Scope/);
    const hint = text.closest('[role="note"]') as HTMLElement;
    expect(
      await within(hint).findByRole('link', { name: 'Vorgeschlagene Subnetze anzeigen (1)' }),
    ).toBeInTheDocument();
    expect(hint).toHaveTextContent(
      'Scope anlegen erfordert die Berechtigung discovery:manage, Vorschläge bestätigen zusätzlich review:resolve.',
    );
    expect(screen.queryByRole('button', { name: 'Als Scope übernehmen' })).toBeNull();
  });

  it('tells in the enroll dialog that 4222/TCP must be reachable and subnets must be confirmed', async () => {
    stubDiscovery();
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    fireEvent.click(await screen.findByRole('button', { name: 'Collector verbinden' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('Port 4222/TCP');
    expect(dialog).toHaveTextContent(/erscheinen hier als Vorschläge/);
    expect(dialog).not.toHaveTextContent(/erscheint automatisch/);
  });
});

/*
 * The enrollment code of a user restricted to several clients (F-425,
 * COL-04, TEN-09, WP-677): the server binds the code to the client of the
 * creator's collector:manage scope and refuses a creator of several clients
 * without client_id (422). The dialog offers exactly those clients (GET /me:
 * pscp of collector:manage, cls otherwise) and shows the server's reason.
 */
describe('DiscoveryPage enrollment code (COL-04, F-425)', () => {
  const clients = [
    { id: 'client-1', organization_id: 'org-1', name: 'Kunde A', slug: 'kunde-a' },
    { id: 'client-2', organization_id: 'org-1', name: 'Kunde B', slug: 'kunde-b' },
    { id: 'client-3', organization_id: 'org-1', name: 'Kunde C', slug: 'kunde-c' },
  ];
  const rights = ['discovery:read', 'collector:manage', 'review:resolve', 'location:read'];

  /** claims are the session claims of GET /me with the client scope `cls` and `pscp`. */
  function claims(cls: string[] | null, pscp?: Record<string, { cls: string[] | null }>) {
    return {
      sub: 'user-1',
      org: 'org-1',
      scopes: rights,
      cls,
      sts: null,
      tms: null,
      ...(pscp ? { pscp } : {}),
      iat: 1,
      exp: 2,
    };
  }

  /**
   * enrollAPI answers POST /collectors/enrollment-codes like the server for a
   * creator holding collector:manage in `allowed` (null: org-wide).
   */
  function enrollAPI(allowed: string[] | null) {
    return (url: string, method: string, body: unknown) => {
      if (!url.endsWith('/collectors/enrollment-codes') || method !== 'POST') {
        return problem(404, { detail: `unexpected write: ${method} ${url}` });
      }
      const requested = (body as { client_id?: string }).client_id;
      const created = (clientId?: string) =>
        json(
          {
            id: 'code-1',
            code: 'ABCD-EFGH',
            label: (body as { label: string }).label,
            expires_at: '2026-10-15T00:00:00Z',
            ...(clientId ? { client_id: clientId } : {}),
          },
          201,
        );
      const fieldProblem = (status: number, message: string) =>
        problem(status, {
          type: 'urn:reticora:problem:invalid-field',
          detail: `client_id: ${message}`,
          field: 'client_id',
        });
      if (allowed === null) return created(requested);
      if (requested) {
        return allowed.includes(requested)
          ? created(requested)
          : fieldProblem(403, 'outside your scope');
      }
      if (allowed.length === 1) return created(allowed[0]);
      return fieldProblem(422, 'required: your scope holds several clients');
    };
  }

  async function openDialog() {
    fireEvent.click(await screen.findByRole('button', { name: 'Collector verbinden' }));
    return within(await screen.findByRole('dialog'));
  }

  it('offers the client choice to a user restricted to several clients (WP-677)', async () => {
    const calls = stubDiscovery({
      permissions: rights,
      // The session reaches client C as well, collector:manage only A and B.
      session: claims(['client-1', 'client-2', 'client-3'], {
        'collector:manage': { cls: ['client-1', 'client-2'] },
      }),
      clients,
      write: enrollAPI(['client-1', 'client-2']),
    });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    const dialog = await openDialog();
    const select = await dialog.findByRole('combobox', { name: 'Kunde' });
    expect(select).toBeRequired();
    await waitFor(() =>
      expect(
        within(select)
          .getAllByRole('option')
          .map((o) => o.textContent),
      ).toEqual(['Kunde wählen', 'Kunde A', 'Kunde B']),
    );
    expect(
      dialog.getByText(
        'Ihr Zugriff umfasst mehrere Kunden: Wählen Sie den Kunden, an den der Collector gebunden wird.',
      ),
    ).toBeInTheDocument();
    const generate = dialog.getByRole('button', { name: 'Code erzeugen' });
    expect(generate).toBeDisabled();

    fireEvent.change(dialog.getByRole('textbox'), { target: { value: 'site-b' } });
    fireEvent.change(select, { target: { value: 'client-2' } });
    expect(generate).toBeEnabled();
    fireEvent.click(generate);

    expect(await screen.findByText(/--code ABCD-EFGH/)).toBeInTheDocument();
    expect(writes(calls, 'POST').map((c) => c.body)).toEqual([
      { label: 'site-b', client_id: 'client-2' },
    ]);
  });

  it('shows the reason of the server instead of a generic error (WP-677)', async () => {
    // Without readable session claims there is no choice: the 422 is shown as is.
    stubDiscovery({ permissions: rights, write: enrollAPI(['client-1', 'client-2']) });
    const first = renderWithProviders(<DiscoveryPage />, { route: '/discovery' });
    let dialog = await openDialog();
    const generate = dialog.getByRole('button', { name: 'Code erzeugen' });
    await waitFor(() => expect(generate).toBeEnabled());
    fireEvent.click(generate);
    expect(await dialog.findByRole('alert')).toHaveTextContent(
      'Der Enrollment-Code wurde nicht erzeugt: client_id: required: your scope holds several clients',
    );
    expect(dialog.queryByText('Ein Fehler ist aufgetreten')).toBeNull();
    first.unmount();
    vi.unstubAllGlobals();

    // With the choice the refusal of a client stands at the client field.
    stubDiscovery({
      permissions: rights,
      session: claims(['client-1', 'client-2']),
      clients,
      write: enrollAPI(['client-1']),
    });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });
    dialog = await openDialog();
    const select = await dialog.findByRole('combobox', { name: 'Kunde' });
    fireEvent.change(select, { target: { value: 'client-2' } });
    fireEvent.click(dialog.getByRole('button', { name: 'Code erzeugen' }));
    expect(await dialog.findByText('outside your scope')).toBeInTheDocument();
    expect(select).toHaveAttribute('aria-invalid', 'true');
    expect(dialog.queryByText('Ein Fehler ist aufgetreten')).toBeNull();
  });

  it.each([
    ['org-wide', claims(null), null],
    ['restricted to one client', claims(['client-1']), ['client-1']],
    [
      'holding collector:manage in one of two clients',
      claims(['client-1', 'client-2'], { 'collector:manage': { cls: ['client-1'] } }),
      ['client-1'],
    ],
  ])('creates the code without a choice for a user %s', async (_name, session, allowed) => {
    const calls = stubDiscovery({
      permissions: rights,
      session,
      clients,
      write: enrollAPI(allowed),
    });
    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });
    const dialog = await openDialog();
    const generate = dialog.getByRole('button', { name: 'Code erzeugen' });
    await waitFor(() => expect(calls.some((c) => c.url.endsWith('/api/v1/me'))).toBe(true));
    await waitFor(() => expect(generate).toBeEnabled());
    expect(dialog.queryByRole('combobox')).toBeNull();
    fireEvent.click(generate);
    expect(await screen.findByText(/--code ABCD-EFGH/)).toBeInTheDocument();
    expect(writes(calls, 'POST').map((c) => c.body)).toEqual([{ label: 'collector' }]);
  });
});
