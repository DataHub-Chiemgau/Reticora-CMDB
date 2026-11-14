/**
 * WP-1080 (CRD-04, PRB-01): the credentials of a monitoring group. The panel
 * lists the bindings with target_kind device_group and the group as
 * target_id, adds one with POST /credential-bindings (target_kind
 * device_group), shows a 422 target_id at the field, is hidden without
 * credential:read and read only without credential:bind.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/monitoring/GroupCredentialsPanel.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import i18n from '../../i18n';
import type { MonitoringGroup } from '../../api/client';
import { GroupCredentialsPanel } from './GroupCredentialsPanel';

type Json = Record<string, unknown>;

const AT = '2026-01-01T00:00:00Z';
const ORG = 'a0000000-0000-4000-8000-0000000000aa';
const GROUP = 'f0000000-0000-4000-8000-000000000001';
const CLIENT = 'c0000000-0000-4000-8000-000000000001';
const CRED_SNMP = 'e0000000-0000-4000-8000-000000000001';
const CRED_SSH = 'e0000000-0000-4000-8000-000000000002';
const BINDING = 'b0000000-0000-4000-8000-000000000001';

const GROUP_ROW: MonitoringGroup = {
  id: GROUP,
  organization_id: ORG,
  client_id: CLIENT,
  name: 'Kernswitche',
  priority: 10,
  member_count: 2,
  created_at: AT,
  updated_at: AT,
};

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

function stubApi(
  permissions: string[],
  onWrite?: (method: string, path: string, body: Json) => Response | undefined,
) {
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
        if (method === 'DELETE') return json(null, 204);
        return json(
          { id: 'new', organization_id: ORG, ...body, created_at: AT, updated_at: AT },
          201,
        );
      }
      if (path === '/me/permissions') return json({ user_id: 'u', permissions });
      if (path === '/credential-bindings') {
        return json(
          page([
            {
              id: BINDING,
              organization_id: ORG,
              credential_id: CRED_SNMP,
              target_kind: 'device_group',
              target_id: GROUP,
              namespace: 'snmp',
              priority: 50,
              created_at: AT,
              updated_at: AT,
            },
          ]),
        );
      }
      if (path === '/credentials') {
        return json(
          page([
            {
              id: CRED_SNMP,
              organization_id: ORG,
              name: 'SNMP Kern',
              kind: 'snmp_v2c',
              key_version: 1,
              created_at: AT,
              updated_at: AT,
            },
            {
              id: CRED_SSH,
              organization_id: ORG,
              client_id: CLIENT,
              name: 'SSH Nord',
              kind: 'ssh_password',
              key_version: 1,
              created_at: AT,
              updated_at: AT,
            },
          ]),
        );
      }
      return json(page([]));
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

describe('GroupCredentialsPanel', () => {
  it('listet Bindungen mit target_kind device_group und target_id der Gruppe', async () => {
    const calls = stubApi(['credential:read', 'credential:bind']);
    renderWithProviders(<GroupCredentialsPanel group={GROUP_ROW} />);

    const row = await screen.findByTestId(`group-binding-${BINDING}`);
    await waitFor(() => expect(within(row).getByText('SNMP Kern')).toBeInTheDocument());
    expect(within(row).getByLabelText('Priorität von SNMP Kern')).toHaveValue('50');
    const list = calls.find((c) => c.path === '/credential-bindings');
    expect(list?.params.get('target_kind')).toBe('device_group');
    expect(list?.params.get('target_id')).toBe(GROUP);
  });

  it('Hinzufügen sendet POST /credential-bindings mit target_kind device_group', async () => {
    const calls = stubApi(['credential:read', 'credential:bind']);
    renderWithProviders(<GroupCredentialsPanel group={GROUP_ROW} />);

    const section = await screen.findByRole('region', { name: 'Zugangsdaten der Gruppe' });
    const select = within(section).getByLabelText('Zugangsart');
    await waitFor(() =>
      expect(
        within(select).getByRole('option', { name: 'SSH Nord (SSH (Passwort))' }),
      ).toBeInTheDocument(),
    );
    fireEvent.change(select, { target: { value: CRED_SSH } });
    fireEvent.change(within(section).getByLabelText('Protokoll'), { target: { value: 'ssh' } });
    fireEvent.change(within(section).getByLabelText('Priorität'), { target: { value: '20' } });
    fireEvent.click(within(section).getByRole('button', { name: 'Binden' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.path).toBe('/credential-bindings');
    expect(post?.body).toEqual({
      credential_id: CRED_SSH,
      namespace: 'ssh',
      priority: 20,
      target_kind: 'device_group',
      target_id: GROUP,
    });
  });

  it('changes the priority (PATCH) and removes a binding (DELETE)', async () => {
    const calls = stubApi(['credential:read', 'credential:bind']);
    renderWithProviders(<GroupCredentialsPanel group={GROUP_ROW} />);

    const row = await screen.findByTestId(`group-binding-${BINDING}`);
    const prio = within(row).getByLabelText(/Priorität von/);
    fireEvent.change(prio, { target: { value: '5' } });
    fireEvent.blur(prio);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ priority: 5 });
    expect(calls.find((c) => c.method === 'PATCH')?.path).toBe(`/credential-bindings/${BINDING}`);

    fireEvent.click(within(row).getByRole('button', { name: /Bindung .* entfernen/ }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE')).toBe(true));
    expect(calls.find((c) => c.method === 'DELETE')?.path).toBe(`/credential-bindings/${BINDING}`);
  });

  it('422 target_id erscheint am Feld', async () => {
    stubApi(['credential:read', 'credential:bind'], () =>
      json(
        {
          type: 'about:blank',
          title: 'Unprocessable Entity',
          status: 422,
          detail: 'target belongs to another client than the credential',
          field: 'target_id',
        },
        422,
      ),
    );
    renderWithProviders(<GroupCredentialsPanel group={GROUP_ROW} />);

    const section = await screen.findByRole('region', { name: 'Zugangsdaten der Gruppe' });
    const select = within(section).getByLabelText('Zugangsart');
    await waitFor(() => expect(within(select).getAllByRole('option').length).toBeGreaterThan(1));
    fireEvent.change(select, { target: { value: CRED_SNMP } });
    fireEvent.click(within(section).getByRole('button', { name: 'Binden' }));
    await waitFor(() => expect(select).toHaveAttribute('aria-invalid', 'true'));
    expect(
      within(section).getByText('target belongs to another client than the credential'),
    ).toBeInTheDocument();
  });

  it('verborgen ohne credential:read, schreibgeschützt ohne credential:bind', async () => {
    const hidden = stubApi(['monitoring:read']);
    const { container, unmount } = renderWithProviders(<GroupCredentialsPanel group={GROUP_ROW} />);
    await waitFor(() => expect(hidden.some((c) => c.path === '/me/permissions')).toBe(true));
    expect(container).toBeEmptyDOMElement();
    expect(hidden.some((c) => c.path === '/credential-bindings')).toBe(false);
    unmount();
    vi.unstubAllGlobals();

    const calls = stubApi(['credential:read']);
    renderWithProviders(<GroupCredentialsPanel group={GROUP_ROW} />);
    const row = await screen.findByTestId(`group-binding-${BINDING}`);
    expect(
      screen.getByText('Nur Lesen: Zum Binden fehlt das Recht credential:bind.'),
    ).toBeInTheDocument();
    expect(within(row).queryByRole('button')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Binden' })).toBeNull();
    expect(calls.filter((c) => c.method !== 'GET')).toEqual([]);
  });
});
