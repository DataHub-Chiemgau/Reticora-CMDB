import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { DiscoveryPage } from './DiscoveryPage';
import { renderWithProviders } from '../test/utils';

/*
 * The Discovery page: scopes with all fields of DIS-09 (F-107), deleting
 * only after a confirmation, feedback for failed scope actions and the
 * controls only with discovery:manage (F-267, WP-496).
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
  scopes?: unknown[];
  suggestions?: unknown[];
  /** write answers every POST, PATCH and DELETE; default 200 with the scope. */
  write?: (url: string, method: string) => Response;
}

interface Call {
  url: string;
  method: string;
  body?: unknown;
}

/** stubDiscovery answers the reads of the page and hands every write to `write`. */
function stubDiscovery({
  permissions = manage,
  scopes = [scope],
  suggestions = [],
  write = () => json(scope),
}: Options = {}) {
  const calls: Call[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString();
    const method = (init?.method ?? 'GET').toUpperCase();
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (method !== 'GET') return write(url, method);
    if (url.includes('/me/permissions')) return json({ permissions });
    if (url.includes('/review-items')) return json(paginated(suggestions));
    if (url.includes('/discovery-scopes?collector_id=c1')) return json(paginated(scopes));
    if (url.includes('/collectors')) return json(paginated([collector]));
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
      form.getByText('Ohne Auswahl gelten die Credentials des Clients und der Organisation.'),
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
