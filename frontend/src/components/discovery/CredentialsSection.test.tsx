import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { CredentialsSection, CREDENTIAL_KINDS, SECRET_FIELDS } from './CredentialsSection';
import { renderWithProviders } from '../../test/utils';

/*
 * The credentials section of the Discovery page (UI-14, WP-856): the list
 * with name, kind, client and creation date; creating with the required
 * fields of each kind; secrets only writable, never shown and dropped from
 * the form state once sent; deleting only after a confirmation; without
 * credential:manage read-only, without credential:read nothing.
 */

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 200, offset: 0, has_more: false };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const AT = '2026-11-02T08:00:00Z';

const credentials = [
  {
    id: 'cred-org',
    organization_id: 'org-1',
    name: 'SNMP Org',
    kind: 'snmp_v2c',
    key_version: 1,
    created_at: AT,
    updated_at: AT,
  },
  {
    id: 'cred-a',
    organization_id: 'org-1',
    client_id: 'client-a',
    name: 'SSH Kunde A',
    kind: 'ssh_key',
    key_version: 1,
    created_at: AT,
    updated_at: AT,
  },
];

const clients = [{ id: 'client-a', organization_id: 'org-1', name: 'Kunde A', slug: 'kunde-a' }];

const READ = ['discovery:read', 'credential:read'];
const MANAGE = [...READ, 'credential:manage'];

interface Call {
  url: string;
  method: string;
  body?: unknown;
}

function stub({
  permissions = MANAGE,
  write = () => json({ ...credentials[0], id: 'cred-new' }, 201),
}: {
  permissions?: string[];
  write?: (url: string, method: string) => Response;
} = {}) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method ?? 'GET').toUpperCase();
      const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ url, method, body });
      if (method !== 'GET') return write(url, method);
      if (url.includes('/me/permissions')) return json({ permissions });
      if (url.includes('/clients')) return json(paginated(clients));
      if (url.includes('/credentials')) return json(paginated(credentials));
      return json({ detail: `unexpected request: ${url}` }, 404);
    }),
  );
  return calls;
}

const writes = (calls: Call[], method: string) => calls.filter((c) => c.method === method);
const reads = (calls: Call[]) =>
  calls.filter((c) => c.method === 'GET' && c.url.includes('/credentials'));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function openForm() {
  fireEvent.click(await screen.findByRole('button', { name: 'Zugangsdaten anlegen' }));
  return within(await screen.findByRole('dialog', { name: 'Zugangsdaten anlegen' }));
}

describe('CredentialsSection (UI-14)', () => {
  it('lists name, kind, client and creation date', async () => {
    stub();
    renderWithProviders(<CredentialsSection />);

    const table = within(await screen.findByRole('region', { name: 'Liste der Zugangsdaten' }));
    expect(table.getByText('SNMP Org')).toBeInTheDocument();
    expect(table.getByText('SNMP v2c')).toBeInTheDocument();
    expect(table.getByText('Alle Kunden (organisationsweit)')).toBeInTheDocument();
    expect(table.getByText('SSH (Schlüssel)')).toBeInTheDocument();
    expect(await table.findByText('Kunde A')).toBeInTheDocument();
    for (const header of ['Name', 'Art', 'Kunde', 'Angelegt']) {
      expect(table.getByRole('columnheader', { name: header })).toBeInTheDocument();
    }
    expect(table.getAllByText(/2026/)).toHaveLength(2);
  });

  it('asks for the required fields of every kind; secret fields are password fields', async () => {
    const calls = stub();
    renderWithProviders(<CredentialsSection />);
    const form = await openForm();

    fireEvent.change(form.getByLabelText('Name'), { target: { value: 'x' } });
    for (const kind of CREDENTIAL_KINDS) {
      fireEvent.change(form.getByLabelText('Art'), { target: { value: kind } });
      fireEvent.click(form.getByRole('button', { name: 'Speichern' }));
      for (const field of SECRET_FIELDS[kind]) {
        if (field.type === 'select') continue;
        const label = i18nField(field.key);
        const input = form.getByLabelText(field.required ? label : `${label} (optional)`);
        expect(input).toHaveAttribute('autocomplete', 'off');
        if (field.type === 'secret') expect(input).toHaveAttribute('type', 'password');
        if (field.type === 'file') expect(input).toHaveAttribute('type', 'file');
        if (field.required) {
          expect(form.getByText(`${label} ist erforderlich.`)).toBeInTheDocument();
        } else {
          expect(form.queryByText(`${label} ist erforderlich.`)).toBeNull();
        }
      }
    }
    expect(writes(calls, 'POST')).toHaveLength(0);
  });

  it('creates an SNMPv3 credential; the secret leaves the form and is never shown', async () => {
    const calls = stub();
    renderWithProviders(<CredentialsSection />);
    const form = await openForm();

    fireEvent.change(form.getByLabelText('Name'), { target: { value: ' SNMP v3 Core ' } });
    fireEvent.change(form.getByLabelText('Art'), { target: { value: 'snmp_v3' } });
    await form.findByRole('option', { name: 'Kunde A' });
    fireEvent.change(form.getByLabelText('Kunde'), { target: { value: 'client-a' } });
    fireEvent.change(form.getByLabelText('Benutzer'), { target: { value: 'monitor' } });
    fireEvent.change(form.getByLabelText('Auth-Protokoll'), { target: { value: 'sha512' } });
    fireEvent.change(form.getByLabelText('Auth-Passwort'), { target: { value: 'auth-geheim' } });
    fireEvent.change(form.getByLabelText('Privacy-Protokoll'), { target: { value: 'aes256' } });
    fireEvent.change(form.getByLabelText('Privacy-Passwort'), { target: { value: 'priv-geheim' } });
    expect(form.getByLabelText('Auth-Passwort')).toHaveAttribute('type', 'password');
    expect(screen.queryByText(/geheim/)).toBeNull();

    fireEvent.click(form.getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(writes(calls, 'POST')).toHaveLength(1));
    const post = writes(calls, 'POST')[0]!;
    expect(post.url).toMatch(/\/api\/v1\/credentials$/);
    expect(post.body).toEqual({
      name: 'SNMP v3 Core',
      kind: 'snmp_v3',
      client_id: 'client-a',
      secret: {
        username: 'monitor',
        auth_protocol: 'sha512',
        auth_password: 'auth-geheim',
        priv_protocol: 'aes256',
        priv_password: 'priv-geheim',
      },
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    // The list is read again, so the scope form offers the new credential.
    await waitFor(() => expect(reads(calls).length).toBeGreaterThan(1));
  });

  it('drops the secret from the form when the server refuses and asks to enter it again', async () => {
    stub({
      write: () =>
        new Response(JSON.stringify({ status: 403, title: 'Forbidden', detail: 'scope' }), {
          status: 403,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
    });
    renderWithProviders(<CredentialsSection />);
    const form = await openForm();

    fireEvent.change(form.getByLabelText('Name'), { target: { value: 'Core' } });
    fireEvent.change(form.getByLabelText('Community'), { target: { value: 'public-geheim' } });
    fireEvent.click(form.getByRole('button', { name: 'Speichern' }));

    const alert = await form.findByRole('alert');
    expect(alert).toHaveTextContent('Dafür fehlt Ihnen das Recht (credential:manage)');
    expect(alert).toHaveTextContent('geben Sie sie erneut ein');
    expect(form.getByLabelText('Community')).toHaveValue('');
    expect(form.getByLabelText('Name')).toHaveValue('Core');
    expect(screen.queryByDisplayValue('public-geheim')).toBeNull();
  });

  it('reads the private key of an SSH key credential from a file', async () => {
    const calls = stub();
    renderWithProviders(<CredentialsSection />);
    const form = await openForm();

    fireEvent.change(form.getByLabelText('Name'), { target: { value: 'Jump host' } });
    fireEvent.change(form.getByLabelText('Art'), { target: { value: 'ssh_key' } });
    fireEvent.change(form.getByLabelText('Benutzer'), { target: { value: 'ops' } });
    const key = '-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n';
    fireEvent.change(form.getByLabelText('Privater Schlüssel (Datei)'), {
      target: { files: [new File([key], 'id_ed25519')] },
    });
    // The key is read asynchronously; until then the form asks for it.
    for (let i = 0; i < 40; i++) {
      await new Promise((r) => setTimeout(r, 25));
      if (writes(calls, 'POST').length > 0) break;
      fireEvent.click(form.getByRole('button', { name: 'Speichern' }));
    }
    await waitFor(() => expect(writes(calls, 'POST')).toHaveLength(1));
    // The key is never shown.
    expect(screen.queryByText(/BEGIN OPENSSH/)).toBeNull();
    expect(writes(calls, 'POST')[0]!.body).toEqual({
      name: 'Jump host',
      kind: 'ssh_key',
      secret: { username: 'ops', private_key: key },
    });
  });

  it('deletes only after the confirmation', async () => {
    const calls = stub({ write: () => new Response(null, { status: 204 }) });
    renderWithProviders(<CredentialsSection />);

    const del = await screen.findByRole('button', { name: 'Zugangsdaten SNMP Org löschen' });
    fireEvent.click(del);
    let dialog = within(await screen.findByRole('dialog', { name: 'Zugangsdaten löschen?' }));
    expect(dialog.getByText(/SNMP Org/)).toBeInTheDocument();
    fireEvent.click(dialog.getByRole('button', { name: 'Abbrechen' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(writes(calls, 'DELETE')).toHaveLength(0);

    fireEvent.click(del);
    dialog = within(await screen.findByRole('dialog', { name: 'Zugangsdaten löschen?' }));
    const before = reads(calls).length;
    fireEvent.click(dialog.getByRole('button', { name: 'Endgültig löschen' }));
    await waitFor(() => expect(writes(calls, 'DELETE')).toHaveLength(1));
    expect(writes(calls, 'DELETE')[0]!.url).toMatch(/\/api\/v1\/credentials\/cred-org$/);
    await waitFor(() => expect(reads(calls).length).toBeGreaterThan(before));
  });

  it('is read-only without credential:manage', async () => {
    stub({ permissions: READ });
    renderWithProviders(<CredentialsSection />);

    expect(await screen.findByText('SNMP Org')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Zugangsdaten anlegen' })).toBeNull();
    expect(screen.queryByRole('button', { name: /löschen/ })).toBeNull();
    expect(screen.queryByRole('columnheader', { name: 'Aktionen' })).toBeNull();
  });

  it('shows nothing and reads no credential without credential:read', async () => {
    const calls = stub({ permissions: ['discovery:read'] });
    const { container } = renderWithProviders(<CredentialsSection />);

    await waitFor(() => expect(calls.some((c) => c.url.includes('/me/permissions'))).toBe(true));
    await new Promise((r) => setTimeout(r, 20));
    expect(container).toBeEmptyDOMElement();
    expect(reads(calls)).toHaveLength(0);
  });
});

/** The German label of a secret field (discovery.credentials.fields). */
function i18nField(key: string): string {
  const labels: Record<string, string> = {
    community: 'Community',
    username: 'Benutzer',
    password: 'Passwort',
    auth_password: 'Auth-Passwort',
    priv_password: 'Privacy-Passwort',
    private_key: 'Privater Schlüssel (Datei)',
    passphrase: 'Passphrase des Schlüssels',
    token: 'Token',
  };
  return labels[key] ?? key;
}
