/**
 * WP-1080 (PRB-01, UI-14): the page /monitoring/settings. A group is
 * created and gets members from the ObjectPicker; a 422 for a CI of another
 * client appears at the picker; deleting names the deleted profiles;
 * auto_ping is disabled without user:manage and sends PATCH /org with the
 * other keys kept; without monitoring:write everything is read only.
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/MonitoringSettingsPage.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import i18n from '../i18n';
import { useAuthStore } from '../auth/authStore';
import { useScopeStore } from '../stores/scope';
import { MonitoringSettingsPage } from './MonitoringSettingsPage';

type Json = Record<string, unknown>;

const AT = '2026-01-01T00:00:00Z';
const ORG = 'a0000000-0000-4000-8000-0000000000aa';
const CLIENT = 'c0000000-0000-4000-8000-000000000001';
const GROUP = 'f0000000-0000-4000-8000-000000000001';
const NEW_GROUP = 'f0000000-0000-4000-8000-000000000009';
const CI_A = 'd0000000-0000-4000-8000-000000000001';
const CI_B = 'd0000000-0000-4000-8000-000000000002';

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 200,
  offset: 0,
  has_more: false,
});

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const problem = (status: number, detail: string, field?: string) =>
  json(
    { type: 'about:blank', title: 'Error', status, detail, ...(field ? { field } : {}) },
    status,
  );

const ICMP = {
  key: 'icmp',
  status: 'available',
  min_interval_s: 10,
  default_interval_s: 30,
  max_interval_s: 3600,
  min_timeout_s: 1,
  default_timeout_s: 10,
  max_timeout_s: 10,
  default_down_count: 5,
  default_up_count: 1,
  max_parallel_per_target: 1,
  max_parallel_per_collector: 256,
  max_rate_per_collector: 1000,
  max_probes_per_target: 4,
  max_payload_bytes: 1472,
  max_rows: 1,
  credential_namespaces: [],
  capabilities: ['icmp'],
  params: { type: 'object', properties: { count: { type: 'integer', default: 3 } } },
};

interface Options {
  permissions?: string[];
  onWrite?: (method: string, path: string, body: Json) => Response | undefined;
  /** Session claims of GET /me (cls, pscp); org-wide by default. */
  claims?: Json;
}

function stubApi({
  permissions = ['monitoring:read', 'monitoring:write', 'user:manage'],
  onWrite,
  claims = {},
}: Options = {}) {
  const groups: Json[] = [
    {
      id: GROUP,
      organization_id: ORG,
      client_id: CLIENT,
      name: 'Kernswitche',
      priority: 10,
      member_count: 0,
      created_at: AT,
      updated_at: AT,
    },
  ];
  const members: Record<string, string[]> = {};
  const calls: { method: string; path: string; params: URLSearchParams; body?: Json }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = init?.method ?? 'GET';
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      calls.push({ method, path, params: url.searchParams, body });
      if (method !== 'GET') {
        const custom = onWrite?.(method, path, body ?? {});
        if (custom) return custom;
        if (method === 'POST' && path === '/monitoring/groups') {
          const created = {
            id: NEW_GROUP,
            organization_id: ORG,
            member_count: 0,
            priority: 0,
            ...body,
            created_at: AT,
            updated_at: AT,
          };
          groups.push(created);
          return json(created, 201);
        }
        const m = /^\/monitoring\/groups\/([^/]+)\/members$/.exec(path);
        if (method === 'POST' && m) {
          const id = m[1] ?? '';
          members[id] = [...(members[id] ?? []), ...((body?.ci_ids as string[]) ?? [])];
          return json({ data: members[id].map((ci) => ({ group_id: id, ci_id: ci })) });
        }
        if (method === 'DELETE' && /^\/monitoring\/groups\/[^/]+$/.test(path)) {
          return json({ deleted_profiles: 3 });
        }
        if (method === 'PATCH' && path === '/org') {
          return json({
            id: ORG,
            name: 'Org',
            slug: 'org',
            plan: 'pro',
            settings: body?.settings,
            created_at: AT,
            updated_at: AT,
          });
        }
        return json({});
      }
      if (path === '/me/permissions') return json({ user_id: 'u', permissions });
      if (path === '/me') {
        return json({ sub: 'u', org: ORG, scopes: [], iat: 0, exp: 0, cls: null, ...claims });
      }
      if (path === '/monitoring/probe-types') return json(page([ICMP]));
      if (path === '/monitoring/profiles') return json(page([]));
      if (path === '/monitoring/groups') return json(page(groups));
      const m = /^\/monitoring\/groups\/([^/]+)\/members$/.exec(path);
      if (m)
        return json(page((members[m[1] ?? ''] ?? []).map((ci) => ({ group_id: m[1], ci_id: ci }))));
      if (path === '/clients') {
        return json(page([{ id: CLIENT, organization_id: ORG, name: 'Kunde Nord', slug: 'nord' }]));
      }
      if (path === '/org') {
        return json({
          id: ORG,
          name: 'Org',
          slug: 'org',
          plan: 'pro',
          settings: {
            branding: { sender_name: 'IT Nord' },
            monitoring: { auto_ping: false, x: 1 },
          },
          created_at: AT,
          updated_at: AT,
        });
      }
      if (path === '/cis') {
        return json(
          page([
            { id: CI_A, name: 'sw-a', ci_type_id: 't', status: 'active' },
            { id: CI_B, name: 'sw-b', ci_type_id: 't', status: 'active' },
          ]),
        );
      }
      if (path.startsWith('/cis/')) {
        const id = path.split('/')[2];
        return json({ id, name: id === CI_A ? 'sw-a' : 'sw-b', ci_type_id: 't', status: 'active' });
      }
      return json(page([]));
    }),
  );
  return calls;
}

function type(input: HTMLElement, text: string) {
  for (let i = 1; i <= text.length; i += 1) {
    fireEvent.change(input, { target: { value: text.slice(0, i) } });
  }
}

async function pick(name: string) {
  type(screen.getByRole('combobox', { name: 'CIs hinzufügen' }), 'sw');
  fireEvent.click(await screen.findByRole('option', { name: new RegExp(name) }));
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useScopeStore.setState({ clientId: '', siteId: '' });
  useAuthStore.setState({
    token: 't',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'u' },
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('MonitoringSettingsPage', () => {
  it('Gruppe anlegen und Mitglieder per Picker hinzufügen', async () => {
    const calls = stubApi();
    renderWithProviders(<MonitoringSettingsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Gruppe anlegen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Gruppe anlegen' });
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Router' } });
    fireEvent.change(within(dialog).getByLabelText('Priorität'), { target: { value: '5' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path === '/monitoring/groups')).toBe(true),
    );
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ name: 'Router', priority: 5 });

    // The new group opens with its members.
    await screen.findByRole('region', { name: 'Gruppe Router' });
    await pick('sw-a');
    await pick('sw-b');
    fireEvent.click(screen.getByRole('button', { name: 'Hinzufügen (2)' }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path.endsWith('/members'))).toBe(true),
    );
    const add = calls.find((c) => c.method === 'POST' && c.path.endsWith('/members'));
    expect(add?.path).toBe(`/monitoring/groups/${NEW_GROUP}/members`);
    expect(add?.body).toEqual({ ci_ids: [CI_A, CI_B] });
    const list = await screen.findByTestId('monitoring-group-members');
    expect(await within(list).findByText('sw-a')).toBeInTheDocument();
  });

  it('422 für CI eines anderen Kunden am Picker', async () => {
    stubApi({
      onWrite: (method, path) =>
        method === 'POST' && path.endsWith('/members')
          ? problem(422, 'ci_ids: CI of another client', 'ci_ids')
          : undefined,
    });
    renderWithProviders(<MonitoringSettingsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Mitglieder von Kernswitche' }));
    await screen.findByRole('region', { name: 'Gruppe Kernswitche' });
    await pick('sw-a');
    fireEvent.click(screen.getByRole('button', { name: 'Hinzufügen (1)' }));
    const error = await screen.findByTestId('member-picker-error');
    expect(error).toHaveTextContent('Ein CI gehört zu einem anderen Kunden als die Gruppe.');
  });

  it('Löschen nennt die Zahl der mitgelöschten Profile', async () => {
    const calls = stubApi();
    renderWithProviders(<MonitoringSettingsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Gruppe Kernswitche löschen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Gruppe löschen?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Endgültig löschen' }));
    expect(
      await screen.findByText('Die Gruppe Kernswitche wurde gelöscht; mitgelöschte Profile: 3.'),
    ).toBeInTheDocument();
    expect(calls.find((c) => c.method === 'DELETE')?.path).toBe(`/monitoring/groups/${GROUP}`);
  });

  it('auto_ping ohne user:manage deaktiviert, mit user:manage PATCH /org mit erhaltenen Schlüsseln', async () => {
    stubApi({ permissions: ['monitoring:read', 'monitoring:write'] });
    const { unmount } = renderWithProviders(<MonitoringSettingsPage />);
    const off = await screen.findByLabelText('Ping-Probes automatisch anlegen');
    await waitFor(() => expect(off).toBeDisabled());
    expect(
      screen.getByText('Nur mit dem Recht user:manage für die ganze Organisation änderbar.'),
    ).toBeInTheDocument();
    unmount();
    vi.unstubAllGlobals();

    const calls = stubApi();
    renderWithProviders(<MonitoringSettingsPage />);
    const toggle = await screen.findByLabelText('Ping-Probes automatisch anlegen');
    await waitFor(() => expect(toggle).toBeEnabled());
    fireEvent.click(toggle);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH');
    expect(patch?.path).toBe('/org');
    expect(patch?.body).toEqual({
      settings: { branding: { sender_name: 'IT Nord' }, monitoring: { auto_ping: true, x: 1 } },
    });
    await waitFor(() => expect(toggle).toBeChecked());
  });

  it('auto_ping disabled with user:manage for one client only', async () => {
    const calls = stubApi({ claims: { pscp: { 'user:manage': { cls: [CLIENT] } } } });
    renderWithProviders(<MonitoringSettingsPage />);
    const toggle = await screen.findByLabelText('Ping-Probes automatisch anlegen');
    await waitFor(() => expect(calls.some((c) => c.path === '/me')).toBe(true));
    await waitFor(() =>
      expect(screen.getByLabelText('Ping-Probes automatisch anlegen')).toBeDisabled(),
    );
    expect(toggle).toBeDisabled();
    expect(
      screen.getByText('Nur mit dem Recht user:manage für die ganze Organisation änderbar.'),
    ).toBeInTheDocument();
  });

  it('auto_ping shows the user:manage hint on 403', async () => {
    stubApi({
      onWrite: (method, path) =>
        method === 'PATCH' && path === '/org'
          ? json({ type: 'about:blank', title: 'Forbidden', status: 403 }, 403)
          : undefined,
    });
    renderWithProviders(<MonitoringSettingsPage />);
    const toggle = await screen.findByLabelText('Ping-Probes automatisch anlegen');
    await waitFor(() => expect(toggle).toBeEnabled());
    fireEvent.click(toggle);
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Nur mit dem Recht user:manage für die ganze Organisation änderbar.',
    );
  });

  it('auto_ping shows the read-only message on 423 org-pending-deletion', async () => {
    stubApi({
      onWrite: (method, path) =>
        method === 'PATCH' && path === '/org'
          ? json(
              {
                type: 'urn:reticora:problem:org-pending-deletion',
                title: 'Organization pending deletion',
                status: 423,
                due_at: '2026-12-14T08:00:00Z',
              },
              423,
            )
          : undefined,
    });
    renderWithProviders(<MonitoringSettingsPage />);
    const toggle = await screen.findByLabelText('Ping-Probes automatisch anlegen');
    await waitFor(() => expect(toggle).toBeEnabled());
    fireEvent.click(toggle);
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Organisation wird gelöscht – nur Lesen bis 14. Dezember 2026',
    );
  });

  it('schreibgeschützt ohne monitoring:write', async () => {
    const calls = stubApi({ permissions: ['monitoring:read'] });
    renderWithProviders(<MonitoringSettingsPage />);

    expect(
      await screen.findByText('Nur Lesen: Zum Ändern fehlt das Recht monitoring:write.'),
    ).toBeInTheDocument();
    const interval = await screen.findByLabelText('Intervall (s)');
    expect(interval).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Profil speichern' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Gruppe anlegen' })).toBeNull();
    expect(screen.queryByRole('button', { name: /löschen/ })).toBeNull();
    expect(calls.filter((c) => c.method !== 'GET')).toEqual([]);
  });

  it('shows a hint and sends no request without monitoring:read', async () => {
    const calls = stubApi({ permissions: ['ci:read'] });
    renderWithProviders(<MonitoringSettingsPage />);
    expect(await screen.findByText('Keine Berechtigung')).toBeInTheDocument();
    expect(calls.filter((c) => c.path !== '/me/permissions')).toEqual([]);
  });
});
