import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { LocationTreePage } from './LocationTreePage';
import { renderWithProviders } from '../test/utils';
import type { LocationTreeNode } from '../api/cmdb';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function node(
  id: string,
  kind: LocationTreeNode['kind'],
  name: string,
  children: LocationTreeNode[] = [],
): LocationTreeNode {
  return {
    id,
    organization_id: 'org-1',
    client_id: 'client-1',
    site_id: 'site-1',
    kind,
    name,
    path: id,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    children,
  };
}

const tree = [
  node('site-1', 'site', 'HQ', [
    node('b-1', 'building', 'Main building', [
      node('r-1', 'room', 'Server room', [node('k-1', 'rack', 'Rack A1')]),
    ]),
    node('w-1', 'warehouse', 'Depot'),
  ]),
];

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

function stubApi(onCreate: (body: Record<string, unknown>) => Response) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString();
    if (url.includes('/locations/tree')) return json({ data: tree });
    if (url.includes('/clients')) {
      return json({
        data: [{ id: 'client-1', organization_id: 'org-1', name: 'Acme', slug: 'acme' }],
        total: 1,
        limit: 200,
        offset: 0,
        has_more: false,
      });
    }
    if (url.endsWith('/locations') && init?.method === 'POST') {
      return onCreate(JSON.parse(String(init.body)) as Record<string, unknown>);
    }
    return json({ detail: `unexpected request: ${url}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

describe('LocationTreePage', () => {
  it('renders the canonical tree with all descendants', async () => {
    stubApi(() => json({}, 500));
    renderWithProviders(<LocationTreePage />, { route: '/locations' });

    expect(await screen.findByText('HQ')).toBeInTheDocument();
    expect(screen.getByText('Main building')).toBeInTheDocument();
    expect(screen.getByText('Server room')).toBeInTheDocument();
    expect(screen.getByText('Depot')).toBeInTheDocument();
    // The rack is the third level below the site and collapsed initially.
    const room = screen.getByText('Server room').closest('div') as HTMLElement;
    fireEvent.click(within(room).getByRole('button', { name: 'expand' }));
    expect(await screen.findByText('Rack A1')).toBeInTheDocument();
  });

  it('offers only the child kinds of the parent matrix and shows field violations', async () => {
    const fetchMock = stubApi(() =>
      json(
        {
          type: 'urn:reticora:problem:validation-error',
          title: 'Validation Error',
          status: 422,
          violations: [{ field: 'parent_id', detail: 'a zone cannot be placed below a site' }],
        },
        422,
      ),
    );
    renderWithProviders(<LocationTreePage />, { route: '/locations' });

    const depot = (await screen.findByText('Depot')).closest('div') as HTMLElement;
    fireEvent.click(within(depot).getByRole('button', { name: /Unterpunkt|child/ }));

    const dialog = await screen.findByRole('dialog');
    const kind = within(dialog).getByRole('combobox') as HTMLSelectElement;
    expect(Array.from(kind.options).map((o) => o.value)).toEqual(['zone']);

    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: 'Zone A' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /Anlegen|Create/ }));

    expect(
      await within(dialog).findByText('a zone cannot be placed below a site'),
    ).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST');
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({
      kind: 'zone',
      name: 'Zone A',
      parent_id: 'w-1',
    });
  });

  it('creates a site for a client', async () => {
    const created: Record<string, unknown>[] = [];
    stubApi((body) => {
      created.push(body);
      return json(node('site-2', 'site', 'Branch'), 201);
    });
    renderWithProviders(<LocationTreePage />, { route: '/locations' });

    fireEvent.click(await screen.findByRole('button', { name: /Standort anlegen|Create site/ }));
    const dialog = await screen.findByRole('dialog');
    const client = within(dialog).getByRole('combobox') as HTMLSelectElement;
    await waitFor(() => expect(client.options).toHaveLength(2));
    fireEvent.change(client, { target: { value: 'client-1' } });
    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: 'Branch' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /Anlegen|Create/ }));

    await waitFor(() =>
      expect(created).toEqual([{ kind: 'site', name: 'Branch', client_id: 'client-1' }]),
    );
  });
});
