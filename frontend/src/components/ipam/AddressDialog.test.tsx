/**
 * WP-917 (IPM-13, IPM-19): the address dialog. The interface choice follows
 * the chosen CI and clears when the CI changes; role and pin are sent; the
 * state of a scanned row is read only; a 422 appears at its field; the
 * history loads and pages; an open review item of the address is pointed
 * out with a link into the inbox; without ipam:write the dialog is read
 * only; axe finds nothing.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/ipam/AddressDialog.test.tsx
 */
import axe from 'axe-core';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import i18n from '../../i18n';
import type { IPAddress } from '../../api/cmdb';
import { AddressDialog } from './AddressDialog';

type Json = Record<string, unknown>;

const AT = '2026-01-01T00:00:00Z';
const ORG = 'org-1';
const NET = 'a0000000-0000-4000-8000-000000000001';
const VRF = 'b0000000-0000-4000-8000-000000000001';
const IP = 'c0000000-0000-4000-8000-000000000001';
const CI_A = 'd0000000-0000-4000-8000-000000000001';
const CI_B = 'd0000000-0000-4000-8000-000000000002';
const IF_A = 'e0000000-0000-4000-8000-000000000001';
const IF_B = 'e0000000-0000-4000-8000-000000000002';

const SUBNET = { id: NET, vrf_id: VRF, cidr: '10.9.0.0/24' };

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[], extra: Json = {}) => ({
  data,
  total: data.length,
  limit: 100,
  offset: 0,
  has_more: false,
  ...extra,
});

const nic = (id: string, ciId: string, name: string) => ({
  id,
  organization_id: ORG,
  ci_id: ciId,
  name,
  if_type: 'ethernet',
  is_management: false,
  is_uplink: false,
  admin_status: 'up',
  oper_status: 'up',
  miss_count: 0,
  created_at: AT,
  updated_at: AT,
});

const ci = (id: string, name: string) => ({ id, name, ci_type_id: 't', status: 'active' });

function address(extra: Partial<IPAddress> = {}): IPAddress {
  return {
    id: IP,
    organization_id: ORG,
    subnet_id: NET,
    address: '10.9.0.5',
    vrf_id: VRF,
    type: 'static',
    role: 'primary',
    origin: 'manual',
    pinned: false,
    state: 'active',
    ci_id: CI_A,
    interface_id: IF_A,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const historyRow = (n: number) => ({
  id: `h-${n}`,
  object_kind: 'ip_address',
  object_id: IP,
  source: n % 2 === 0 ? 'discovery' : 'manual',
  change_type: 'update',
  field_name: 'dns_name',
  old_value: `old-${n}`,
  new_value: `new-${n}`,
  changed_at: AT,
});

interface Options {
  permissions?: string[];
  reviews?: Json[];
  onWrite?: (method: string, path: string, body: Json) => Response | undefined;
}

function stubApi({
  permissions = ['ipam:read', 'ipam:write', 'discovery:read'],
  reviews = [],
  onWrite,
}: Options = {}) {
  const calls: { method: string; path: string; params: URLSearchParams; body?: Json }[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost');
    const method = init?.method ?? 'GET';
    const path = url.pathname.replace(/^\/api\/v1/, '');
    const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
    calls.push({ method, path, params: url.searchParams, body });
    if (method !== 'GET') {
      const answer = onWrite?.(method, path, body ?? {});
      if (answer) return answer;
      if (method === 'POST') return json(address({ ...(body as Partial<IPAddress>), id: IP }), 201);
      return json(address(body as Partial<IPAddress>));
    }
    if (path === '/me/permissions') return json({ user_id: 'u', permissions });
    if (path === `/vrfs/${VRF}`) {
      return json({ id: VRF, organization_id: ORG, name: 'global', is_default: true });
    }
    if (path === `/cis/${CI_A}/interfaces`) return json(page([nic(IF_A, CI_A, 'eth0')]));
    if (path === `/cis/${CI_B}/interfaces`) return json(page([nic(IF_B, CI_B, 'ge-0/0/1')]));
    if (path === `/cis/${CI_A}`) return json(ci(CI_A, 'sw-a'));
    if (path === `/cis/${CI_B}`) return json(ci(CI_B, 'sw-b'));
    if (path === '/cis') return json(page([ci(CI_B, 'sw-b')]));
    if (path === `/ips/${IP}/history`) {
      const offset = Number(url.searchParams.get('offset') ?? '0');
      const rows = offset === 0 ? [historyRow(1), historyRow(2)] : [historyRow(3)];
      return json(page(rows, { total: 26, limit: 25, offset, has_more: offset === 0 }));
    }
    if (path === '/review-items') {
      const kind = url.searchParams.get('kind');
      return json(page(reviews.filter((r) => r.kind === kind)));
    }
    return json(page([]));
  });
  vi.stubGlobal('fetch', fetchMock);
  return calls;
}

function renderDialog(ip?: IPAddress) {
  return renderWithProviders(
    <AddressDialog open onOpenChange={() => {}} subnet={SUBNET} address={ip} />,
  );
}

/** type enters the text letter by letter without a pause (picker search). */
function type(input: HTMLElement, text: string) {
  for (let i = 1; i <= text.length; i += 1) {
    fireEvent.change(input, { target: { value: text.slice(0, i) } });
  }
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('AddressDialog', () => {
  it('offers the interfaces of the chosen CI and clears the interface when the CI changes', async () => {
    const calls = stubApi();
    renderDialog(address());

    const nicSelect = await screen.findByLabelText('Interface');
    await waitFor(() => expect(nicSelect).toHaveValue(IF_A));
    expect(within(nicSelect).getByRole('option', { name: 'eth0' })).toBeInTheDocument();
    // The bound CI is named, not shown as id.
    expect(await screen.findByText('sw-a')).toBeInTheDocument();

    type(screen.getByRole('combobox', { name: 'Gerät' }), 'sw-b');
    fireEvent.click(await screen.findByRole('option', { name: /sw-b/ }));

    await waitFor(() =>
      expect(within(nicSelect).getByRole('option', { name: 'ge-0/0/1' })).toBeInTheDocument(),
    );
    expect(nicSelect).toHaveValue('');
    expect(within(nicSelect).queryByRole('option', { name: 'eth0' })).not.toBeInTheDocument();

    fireEvent.change(nicSelect, { target: { value: IF_B } });
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    await waitFor(() => {
      const patch = calls.find((c) => c.method === 'PATCH');
      expect(patch?.path).toBe(`/ips/${IP}`);
      expect(patch?.body).toEqual({ interface_id: IF_B, ci_id: CI_B });
    });
  });

  it('sends role and pin', async () => {
    const calls = stubApi();
    renderDialog(address());

    fireEvent.change(await screen.findByLabelText('Rolle'), { target: { value: 'management' } });
    fireEvent.click(screen.getByLabelText('Bindung fixieren (Pin)'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => {
      const patch = calls.find((c) => c.method === 'PATCH');
      expect(patch?.body).toEqual({ role: 'management', pinned: true });
    });
  });

  it('creates an address in the subnet and sets the pin afterwards', async () => {
    const calls = stubApi();
    renderDialog();

    fireEvent.change(await screen.findByLabelText('Adresse'), { target: { value: '10.9.0.77' } });
    fireEvent.change(screen.getByLabelText('Rolle'), { target: { value: 'vip' } });
    fireEvent.change(screen.getByLabelText('DNS-Name'), { target: { value: 'vip.example' } });
    fireEvent.click(screen.getByLabelText('Bindung fixieren (Pin)'));
    expect(screen.queryByRole('tab')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => {
      const writes = calls.filter((c) => c.method !== 'GET');
      expect(writes.map((c) => [c.method, c.path, c.body])).toEqual([
        [
          'POST',
          '/ips',
          {
            subnet_id: NET,
            address: '10.9.0.77',
            type: 'static',
            role: 'vip',
            dns_name: 'vip.example',
          },
        ],
        ['PATCH', `/ips/${IP}`, { pinned: true }],
      ]);
    });
  });

  it('retries a failed pin as PATCH and never posts the address twice', async () => {
    let pinAttempts = 0;
    const calls = stubApi({
      onWrite: (method) => {
        if (method !== 'PATCH') return undefined;
        pinAttempts += 1;
        return pinAttempts === 1
          ? json({ type: 'about:blank', title: 'Internal', status: 500 }, 500)
          : undefined;
      },
    });
    renderDialog();

    fireEvent.change(await screen.findByLabelText('Adresse'), { target: { value: '10.9.0.78' } });
    fireEvent.click(screen.getByLabelText('Bindung fixieren (Pin)'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    expect(
      await screen.findByText('Die Adresse wurde angelegt, der Pin konnte nicht gesetzt werden.'),
    ).toBeInTheDocument();
    // The created address is now edited: the address is no longer editable.
    expect(screen.getByLabelText('Adresse')).toHaveAttribute('readonly');
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => {
      const writes = calls.filter((c) => c.method !== 'GET');
      expect(writes.map((c) => [c.method, c.path, c.body])).toEqual([
        ['POST', '/ips', { subnet_id: NET, address: '10.9.0.78', type: 'static', role: 'primary' }],
        ['PATCH', `/ips/${IP}`, { pinned: true }],
        ['PATCH', `/ips/${IP}`, { pinned: true }],
      ]);
    });
  });

  it('shows the state of a scanned row read only and the origin', async () => {
    stubApi();
    renderDialog(address({ origin: 'scan', state: 'offline' }));

    const state = await screen.findByLabelText('Zustand');
    expect(state).toBeDisabled();
    expect(state).toHaveValue('offline');
    expect(screen.getByText('Den Zustand setzt der Scan.')).toBeInTheDocument();
    expect(screen.getByTestId('address-origin')).toHaveTextContent('Scan');
  });

  it('shows a 422 of the server at its field', async () => {
    stubApi({
      onWrite: () =>
        json(
          {
            type: 'urn:reticora:problem:validation',
            title: 'Unprocessable Entity',
            status: 422,
            field: 'role',
            detail: 'role must be one of primary, secondary',
          },
          422,
        ),
    });
    renderDialog(address());

    const role = await screen.findByLabelText('Rolle');
    fireEvent.change(role, { target: { value: 'loopback' } });
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    expect(await screen.findByText('role must be one of primary, secondary')).toBeInTheDocument();
    expect(role).toHaveAttribute('aria-invalid', 'true');
  });

  it('shows a 422 ci_id below the device picker', async () => {
    stubApi({
      onWrite: () =>
        json(
          { title: 'Unprocessable Entity', status: 422, field: 'ci_id', detail: 'unknown CI' },
          422,
        ),
    });
    renderDialog(address());

    fireEvent.click(await screen.findByLabelText('Bindung fixieren (Pin)'));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('unknown CI');
  });

  it('loads the history and pages through it', async () => {
    const calls = stubApi();
    renderDialog(address());

    fireEvent.click(await screen.findByRole('tab', { name: 'Verlauf' }));
    const history = await screen.findByRole('region', { name: 'Änderungsverlauf' });
    expect(within(history).getByText('old-1 → new-1')).toBeInTheDocument();
    expect(within(history).getByText('Discovery')).toBeInTheDocument();
    expect(within(history).getAllByText('System')).toHaveLength(2);
    expect(screen.getByText('1–2 von 26')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Weiter' }));
    expect(await screen.findByText('old-3 → new-3')).toBeInTheDocument();
    const pages = calls.filter((c) => c.path === `/ips/${IP}/history`);
    expect(pages.map((c) => c.params.get('offset'))).toEqual(['0', '25']);
    expect(pages[0]!.params.get('limit')).toBe('25');
  });

  it('points out an open review item of the address with a link into the inbox', async () => {
    stubApi({
      reviews: [
        {
          id: 'r1',
          kind: 'duplicate_ip',
          status: 'open',
          payload: { address: '10.9.0.5', vrf_id: VRF },
          object_key: `ip:${VRF}/10.9.0.5`,
          escalated: false,
          created_at: AT,
        },
        {
          id: 'r2',
          kind: 'duplicate_ip',
          status: 'open',
          payload: { address: '10.9.0.6', vrf_id: VRF },
          object_key: `ip:${VRF}/10.9.0.6`,
          escalated: false,
          created_at: AT,
        },
        {
          id: 'r3',
          kind: 'ip_binding_conflict',
          status: 'open',
          payload: { address: '10.9.0.5', vrf_id: VRF, ip_address_id: IP },
          object_key: `ipbind:${IP}/${IF_B}`,
          escalated: false,
          created_at: AT,
        },
      ],
    });
    renderDialog(address({ pinned: true }));

    const note = await screen.findByRole('note', { name: 'Offener Konflikt' });
    await waitFor(() => expect(within(note).getAllByRole('link')).toHaveLength(2));
    const links = within(note).getAllByRole('link', { name: 'In der Review-Inbox öffnen' });
    expect(links.map((l) => l.getAttribute('href'))).toEqual([
      '/reviews?kind=duplicate_ip',
      '/reviews?kind=ip_binding_conflict',
    ]);
    expect(note).toHaveTextContent('Doppelte IP-Adresse');
  });

  it('is read only without ipam:write', async () => {
    const calls = stubApi({ permissions: ['ipam:read'] });
    renderDialog(address());

    expect(
      await screen.findByText('Nur lesend: Zum Bearbeiten fehlt das Recht ipam:write.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Speichern' })).not.toBeInTheDocument();
    expect(screen.getByLabelText('Rolle')).toBeDisabled();
    expect(screen.getByLabelText('Interface')).toBeDisabled();
    expect(screen.getByLabelText('Bindung fixieren (Pin)')).toBeDisabled();
    expect(screen.queryByRole('combobox', { name: 'Gerät' })).not.toBeInTheDocument();
    expect(await screen.findByRole('link', { name: 'sw-a' })).toHaveAttribute(
      'href',
      `/cmdb/${CI_A}`,
    );
    // Without discovery:read no review request.
    expect(calls.some((c) => c.path === '/review-items')).toBe(false);
  });

  it('has no axe findings', async () => {
    stubApi();
    renderDialog(address());
    await screen.findByText('sw-a');
    const dialog = screen.getByRole('dialog');
    const result = await axe.run(dialog, { rules: { 'color-contrast': { enabled: false } } });
    expect(result.violations.map((v) => v.id)).toEqual([]);
  });
});
