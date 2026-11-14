/**
 * WP-923 (CRD-07, UI-14): the section "Zugangsdaten" of the CI detail.
 * The three ways send the expected requests (create, then bind; bind only;
 * create with vault_ref, then bind). The origin shows CI, device group
 * with the group name and subnet with the CIDR; the usage shows the error
 * class auth_failed. No secret appears in the DOM after sending or from an
 * answer. Without credential:bind there are no actions, without
 * credential:read no section; axe finds nothing.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/cmdb/CredentialBindingSection.test.tsx
 */
import axe from 'axe-core';
import { useAuthStore } from '../../auth/authStore';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import i18n from '../../i18n';
import { CredentialBindingSection } from './CredentialBindingSection';

type Json = Record<string, unknown>;

const AT = '2026-01-01T00:00:00Z';
const ORG = 'a0000000-0000-4000-8000-0000000000aa';
const CI = 'd0000000-0000-4000-8000-000000000001';
const CLIENT = 'c0000000-0000-4000-8000-000000000001';
const OTHER_CLIENT = 'c0000000-0000-4000-8000-000000000002';
const CRED_SNMP = 'e0000000-0000-4000-8000-000000000001';
const CRED_SSH = 'e0000000-0000-4000-8000-000000000002';
const CRED_REDFISH = 'e0000000-0000-4000-8000-000000000003';
const CRED_FOREIGN = 'e0000000-0000-4000-8000-000000000004';
const CRED_NEW = 'e0000000-0000-4000-8000-000000000009';
const BIND_CI = 'b0000000-0000-4000-8000-000000000001';
const BIND_GROUP = 'b0000000-0000-4000-8000-000000000002';
const BIND_SUBNET = 'b0000000-0000-4000-8000-000000000003';
const GROUP = 'f0000000-0000-4000-8000-000000000001';
const SUBNET = 'f0000000-0000-4000-8000-000000000002';
const VAULT = '90000000-0000-4000-8000-000000000001';
const SECRET = 'geheim-community-923';
const ALL = ['credential:read', 'credential:bind', 'credential:manage'];

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 200,
  offset: 0,
  has_more: false,
});

const credential = (id: string, name: string, kind: string, extra: Json = {}) => ({
  id,
  organization_id: ORG,
  name,
  kind,
  key_version: 1,
  source: 'stored',
  created_at: AT,
  updated_at: AT,
  ...extra,
});

const binding = (id: string, credentialId: string, namespace: string, extra: Json = {}) => ({
  id,
  organization_id: ORG,
  credential_id: credentialId,
  target_kind: 'ci',
  target_id: CI,
  namespace,
  priority: 100,
  created_at: AT,
  updated_at: AT,
  ...extra,
});

const resolved = (id: string, name: string, kind: string, origin: string, sourceId?: string) => ({
  credential: { id, name, kind },
  origin,
  ...(sourceId ? { source_id: sourceId, priority: 100 } : {}),
});

interface Options {
  permissions?: string[];
  /** Whether the plan includes the feature monitoring. */
  monitoring?: boolean;
  /** Whether the release serves the module monitoring. */
  monitoringModule?: boolean;
  ciBindings?: Json[];
  onWrite?: (method: string, path: string, body: Json) => Response | undefined;
}

function stubApi({
  permissions = ALL,
  ciBindings = [],
  onWrite,
  monitoring = true,
  monitoringModule = true,
}: Options = {}) {
  let bindings = [...ciBindings];
  const calls: { method: string; path: string; params: URLSearchParams; body?: Json }[] = [];
  const responses: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost');
    const method = init?.method ?? 'GET';
    const path = url.pathname.replace(/^\/api\/v1/, '');
    const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
    calls.push({ method, path, params: url.searchParams, body });
    const answer = (value: unknown, status = 200) => {
      responses.push(JSON.stringify(value));
      return json(value, status);
    };
    if (method !== 'GET') {
      const custom = onWrite?.(method, path, body ?? {});
      if (custom) return custom;
      if (method === 'POST' && path === '/credentials') {
        // Like the server: metadata only, never the secret.
        const created = credential(CRED_NEW, String(body?.name), String(body?.kind), {
          client_id: body?.client_id,
          source: body?.vault_ref ? 'vault_ref' : 'stored',
        });
        return answer(created, 201);
      }
      if (method === 'PUT' && path === `/cis/${CI}/credentials`) {
        const entries = (body?.bindings ?? []) as Json[];
        bindings = entries.map((e, i) =>
          binding(
            `b1000000-0000-4000-8000-00000000000${i}`,
            String(e.credential_id),
            String(e.namespace),
          ),
        );
        return answer({ data: bindings });
      }
      return answer({}, 200);
    }
    if (path === '/me/permissions') return json({ user_id: 'u', permissions });
    if (path === '/modules') {
      return json({
        data: [{ key: 'monitoring', phase: 'P3', enabled: monitoringModule, nav: [] }],
      });
    }
    if (path === '/entitlements') {
      return json(page([{ feature_key: 'monitoring', enabled: monitoring, plan: 'pro' }]));
    }
    if (path === `/cis/${CI}/credentials/effective`) {
      return answer({
        ci_id: CI,
        data: [
          {
            namespace: 'redfish',
            ...resolved(CRED_REDFISH, 'Redfish Subnetz', 'redfish', 'subnet', BIND_SUBNET),
            overridden: [],
          },
          {
            namespace: 'snmp',
            ...resolved(CRED_SNMP, 'SNMP Kern', 'snmp_v2c', 'ci', BIND_CI),
            overridden: [resolved(CRED_FOREIGN, 'SNMP Org', 'snmp_v2c', 'org')],
          },
          {
            namespace: 'ssh',
            ...resolved(CRED_SSH, 'SSH Gruppe', 'ssh_password', 'device_group', BIND_GROUP),
            overridden: [],
          },
        ],
      });
    }
    if (path === `/cis/${CI}/credentials`) return answer({ data: bindings });
    if (path === '/credentials') {
      return answer(
        page([
          credential(CRED_SNMP, 'SNMP Kern', 'snmp_v2c', { client_id: CLIENT }),
          credential(CRED_SSH, 'SSH Gruppe', 'ssh_password'),
          credential(CRED_REDFISH, 'Redfish Subnetz', 'redfish', { source: 'vault_ref' }),
          credential(CRED_FOREIGN, 'SNMP Fremd', 'snmp_v3', { client_id: OTHER_CLIENT }),
          credential('e0000000-0000-4000-8000-000000000005', 'SNMP Org', 'snmp_v3'),
        ]),
      );
    }
    if (path === `/credential-bindings/${BIND_GROUP}`) {
      return answer(
        binding(BIND_GROUP, CRED_SSH, 'ssh', { target_kind: 'device_group', target_id: GROUP }),
      );
    }
    if (path === `/credential-bindings/${BIND_SUBNET}`) {
      return answer(
        binding(BIND_SUBNET, CRED_REDFISH, 'redfish', { target_kind: 'subnet', target_id: SUBNET }),
      );
    }
    if (path === `/credential-bindings/${BIND_CI}`)
      return answer(binding(BIND_CI, CRED_SNMP, 'snmp'));
    if (path === `/monitoring/groups/${GROUP}`) {
      return answer({
        id: GROUP,
        organization_id: ORG,
        name: 'Kernswitche',
        priority: 10,
        created_at: AT,
        updated_at: AT,
      });
    }
    if (path === `/subnets/${SUBNET}`) {
      return answer({ id: SUBNET, organization_id: ORG, cidr: '10.20.0.0/24' });
    }
    if (path === `/credentials/${CRED_SNMP}/usage`) {
      return answer(
        page([
          {
            credential_id: CRED_SNMP,
            ci_id: CI,
            namespace: 'snmp',
            last_success_at: '2026-01-02T10:00:00Z',
            last_failure_at: '2026-01-03T10:00:00Z',
            last_error_class: 'auth_failed',
            client_id: CLIENT,
            site_id: null,
          },
        ]),
      );
    }
    if (path === '/clients') {
      return answer(page([{ id: CLIENT, organization_id: ORG, name: 'Kunde Nord', slug: 'nord' }]));
    }
    if (path === '/vault-connections') {
      return answer(
        page([
          {
            id: VAULT,
            organization_id: ORG,
            name: 'OpenBao Nord',
            kind: 'openbao_kv2',
            address: 'https://bao.example',
            mount: 'secret',
            auth_method: 'token',
            auth_credential_id: CRED_SSH,
            resolve_at: 'collector',
            enabled: true,
            collector_ids: [],
            created_at: AT,
            updated_at: AT,
          },
        ]),
      );
    }
    return answer(page([]));
  });
  vi.stubGlobal('fetch', fetchMock);
  return { calls, responses };
}

function renderSection() {
  return renderWithProviders(<CredentialBindingSection ciId={CI} clientId={CLIENT} />);
}

async function openSet(namespace: string) {
  fireEvent.click(
    await screen.findByRole('button', { name: `Zugangsart für ${namespace} setzen` }),
  );
  return screen.findByRole('dialog', { name: `Zugangsart für ${namespace} setzen` });
}

beforeEach(async () => {
  // Signed in, so the plan (GET /entitlements) is read as in the app.
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('CredentialBindingSection', () => {
  it('shows the effective credential per namespace with origin, source and usage', async () => {
    stubApi();
    renderSection();

    const snmp = await screen.findByTestId('credential-namespace-snmp');
    expect(within(snmp).getByText('SNMP Kern')).toBeInTheDocument();
    expect(within(snmp).getByTestId('credential-origin')).toHaveTextContent('CI');
    expect(await within(snmp).findByText(/gespeichert/)).toBeInTheDocument();
    const usage = await within(snmp).findByTestId('credential-usage');
    expect(usage).toHaveTextContent('Letzter Erfolg');
    expect(usage).toHaveTextContent('Anmeldung abgelehnt');

    const ssh = screen.getByTestId('credential-namespace-ssh');
    await waitFor(() =>
      expect(within(ssh).getByTestId('credential-origin')).toHaveTextContent(
        'Gerätegruppe: Kernswitche',
      ),
    );
    const redfish = screen.getByTestId('credential-namespace-redfish');
    await waitFor(() =>
      expect(within(redfish).getByTestId('credential-origin')).toHaveTextContent(
        'Subnetz: 10.20.0.0/24',
      ),
    );
    expect(await within(redfish).findByText(/Tresor-Referenz/)).toBeInTheDocument();

    const ipmi = screen.getByTestId('credential-namespace-ipmi');
    expect(within(ipmi).getByText('keine')).toBeInTheDocument();
  });

  it('creates a device-specific credential, then binds it to the CI', async () => {
    const { calls, responses } = stubApi({ ciBindings: [binding(BIND_CI, CRED_SNMP, 'snmp')] });
    renderSection();

    const dialog = await openSet('SSH');
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'SSH Gerät' } });
    await waitFor(() => expect(within(dialog).getByLabelText('Kunde')).toHaveValue(CLIENT));
    fireEvent.change(within(dialog).getByLabelText('Benutzer'), { target: { value: 'admin' } });
    const password = within(dialog).getByLabelText('Passwort');
    expect(password).toHaveAttribute('type', 'password');
    fireEvent.change(password, { target: { value: SECRET } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true));
    const writes = calls.filter((c) => c.method !== 'GET');
    expect(writes.map((c) => `${c.method} ${c.path}`)).toEqual([
      'POST /credentials',
      `PUT /cis/${CI}/credentials`,
    ]);
    expect(writes[0]?.body).toEqual({
      name: 'SSH Gerät',
      kind: 'ssh_password',
      client_id: CLIENT,
      secret: { username: 'admin', password: SECRET },
    });
    // The former binding of snmp stays, ssh is added with the new credential.
    expect(writes[1]?.body).toEqual({
      bindings: [
        { credential_id: CRED_SNMP, namespace: 'snmp', priority: 100 },
        { credential_id: CRED_NEW, namespace: 'ssh', priority: 100 },
      ],
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    // No secret in the DOM after sending, none in any answer.
    expect(document.body.innerHTML).not.toContain(SECRET);
    expect(responses.join('\n')).not.toContain(SECRET);
  });

  it('offers only the organisation-wide choice for a CI without a client', async () => {
    stubApi();
    renderWithProviders(<CredentialBindingSection ciId={CI} />);

    const dialog = await openSet('SSH');
    const select = within(dialog).getByLabelText('Kunde');
    // The clients are loaded, yet none is offered: a client credential
    // cannot be bound to a CI without a client.
    await waitFor(() => expect(within(dialog).getByLabelText('Kunde')).toHaveValue(''));
    const options = within(select as HTMLElement).getAllByRole('option');
    expect(options.map((o) => o.getAttribute('value'))).toEqual(['']);
    expect(within(select as HTMLElement).queryByText('Kunde Nord')).not.toBeInTheDocument();
  });

  it('binds an existing credential of the namespace and the CI client only', async () => {
    const { calls } = stubApi();
    renderSection();

    const dialog = await openSet('SNMP');
    fireEvent.click(within(dialog).getByLabelText('bestehende wählen'));
    const select = within(dialog).getByLabelText('Bestehende Zugangsart');
    const options = within(select)
      .getAllByRole('option')
      .map((o) => o.textContent);
    expect(options).toEqual(['Bitte wählen', 'SNMP Kern (SNMP v2c)', 'SNMP Org (SNMP v3)']);
    fireEvent.change(select, { target: { value: CRED_SNMP } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Binden' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true));
    const writes = calls.filter((c) => c.method !== 'GET');
    expect(writes.map((c) => `${c.method} ${c.path}`)).toEqual([`PUT /cis/${CI}/credentials`]);
    expect(writes[0]?.body).toEqual({
      bindings: [{ credential_id: CRED_SNMP, namespace: 'snmp', priority: 100 }],
    });
  });

  it('creates a vault reference, then binds it', async () => {
    const { calls } = stubApi();
    renderSection();

    const dialog = await openSet('SNMP');
    fireEvent.click(
      within(dialog).getByLabelText('Tresor-Referenz', { selector: 'input[value="vault"]' }),
    );
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'SNMP Tresor' } });
    const connection = within(dialog).getByLabelText('Tresorverbindung');
    await waitFor(() =>
      expect(within(connection).getByRole('option', { name: 'OpenBao Nord' })).toBeInTheDocument(),
    );
    fireEvent.change(connection, { target: { value: VAULT } });
    fireEvent.change(within(dialog).getByLabelText('Pfad'), { target: { value: 'netz/kern' } });
    fireEvent.change(within(dialog).getByLabelText('Version (optional)'), {
      target: { value: '3' },
    });
    fireEvent.change(within(dialog).getByLabelText('Tresorfeld für Community'), {
      target: { value: 'community' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true));
    const writes = calls.filter((c) => c.method !== 'GET');
    expect(writes.map((c) => `${c.method} ${c.path}`)).toEqual([
      'POST /credentials',
      `PUT /cis/${CI}/credentials`,
    ]);
    expect(writes[0]?.body).toEqual({
      name: 'SNMP Tresor',
      kind: 'snmp_v2c',
      client_id: CLIENT,
      vault_ref: {
        connection_id: VAULT,
        path: 'netz/kern',
        version: 3,
        field_map: { community: 'community' },
      },
    });
    expect(writes[1]?.body).toEqual({
      bindings: [{ credential_id: CRED_NEW, namespace: 'snmp', priority: 100 }],
    });
  });

  it('removes a CI binding', async () => {
    const { calls } = stubApi({
      ciBindings: [binding(BIND_CI, CRED_SNMP, 'snmp'), binding(BIND_GROUP, CRED_SSH, 'ssh')],
    });
    renderSection();

    fireEvent.click(await screen.findByRole('button', { name: 'Bindung für SNMP entfernen' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true));
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({
      bindings: [{ credential_id: CRED_SSH, namespace: 'ssh', priority: 100 }],
    });
  });

  it('has no actions without credential:bind', async () => {
    const { calls } = stubApi({
      permissions: ['credential:read'],
      ciBindings: [binding(BIND_CI, CRED_SNMP, 'snmp')],
    });
    renderSection();

    expect(await screen.findByTestId('credential-namespace-snmp')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /setzen/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /entfernen/ })).not.toBeInTheDocument();
    expect(calls.filter((c) => c.method !== 'GET')).toEqual([]);
  });

  it('offers only existing credentials without credential:manage', async () => {
    stubApi({ permissions: ['credential:read', 'credential:bind'] });
    renderSection();

    const dialog = await openSet('SNMP');
    expect(within(dialog).queryByLabelText('gerätespezifisch neu anlegen')).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText('Bestehende Zugangsart')).toBeInTheDocument();
  });

  it('renders nothing and reads nothing without credential:read', async () => {
    const { calls } = stubApi({ permissions: ['ci:read'] });
    const { container } = renderSection();

    await waitFor(() => expect(calls.some((c) => c.path === '/me/permissions')).toBe(true));
    expect(container).toBeEmptyDOMElement();
    expect(
      calls.filter((c) => !['/me/permissions', '/entitlements', '/modules'].includes(c.path)),
    ).toEqual([]);
  });

  it('sends no credential request when the plan lacks the feature monitoring', async () => {
    const { calls } = stubApi({ monitoring: false });
    const { container } = renderSection();

    await waitFor(() => expect(calls.some((c) => c.path === '/entitlements')).toBe(true));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(container).toBeEmptyDOMElement();
    expect(
      calls.filter((c) => !['/me/permissions', '/entitlements', '/modules'].includes(c.path)),
    ).toEqual([]);
  });

  it('sends no credential request when the release switches the module monitoring off', async () => {
    const { calls } = stubApi({ monitoringModule: false });
    const { container } = renderSection();

    await waitFor(() => expect(calls.some((c) => c.path === '/modules')).toBe(true));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(container).toBeEmptyDOMElement();
    expect(
      calls.filter((c) => !['/me/permissions', '/entitlements', '/modules'].includes(c.path)),
    ).toEqual([]);
  });

  it('has no axe findings', async () => {
    stubApi();
    const { container } = renderSection();
    await screen.findByTestId('credential-namespace-snmp');
    await screen.findByTestId('credential-usage');
    const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } });
    expect(result.violations.map((v) => v.id)).toEqual([]);

    const dialog = await openSet('SNMP');
    const dialogResult = await axe.run(dialog, {
      rules: { 'color-contrast': { enabled: false } },
    });
    expect(dialogResult.violations.map((v) => v.id)).toEqual([]);
  });
});
