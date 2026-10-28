import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { LocationTreePage } from './LocationTreePage';
import { renderWithProviders } from '../test/utils';
import i18n from '../i18n';
import type { LocationTreeNode } from '../api/cmdb';
import type { components } from '../api/generated/schema';

afterEach(async () => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  await i18n.changeLanguage('de-DE');
});

function node(
  id: string,
  kind: LocationTreeNode['kind'],
  name: string,
  children: LocationTreeNode[] = [],
  parentId?: string,
): LocationTreeNode {
  return {
    id,
    organization_id: 'org-1',
    client_id: 'client-1',
    site_id: 'site-1',
    ...(parentId ? { parent_id: parentId } : {}),
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
    node(
      'b-1',
      'building',
      'Main building',
      [node('r-1', 'room', 'Server room', [node('k-1', 'rack', 'Rack A1', [], 'r-1')], 'b-1')],
      'site-1',
    ),
    node('w-1', 'warehouse', 'Depot', [], 'site-1'),
  ]),
  node('site-2', 'site', 'Branch', [
    node('b-2', 'building', 'Annex', [node('r-2', 'room', 'Lab', [], 'b-2')], 'site-2'),
  ]),
];

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

type Write = (body: Record<string, unknown>, url: string) => Response;

function stubApi(
  onCreate: Write,
  { onUpdate, onDelete }: { onUpdate?: Write; onDelete?: (url: string) => Response } = {},
) {
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
      return onCreate(JSON.parse(String(init.body)) as Record<string, unknown>, url);
    }
    if (url.includes('/locations/') && init?.method === 'PATCH' && onUpdate) {
      return onUpdate(JSON.parse(String(init.body)) as Record<string, unknown>, url);
    }
    if (url.includes('/locations/') && init?.method === 'DELETE' && onDelete) {
      return onDelete(url);
    }
    return json({ detail: `unexpected request: ${url}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function calls(fetchMock: ReturnType<typeof stubApi>, method: string) {
  return fetchMock.mock.calls.filter(([, init]) => init?.method === method);
}

function treeLoads(fetchMock: ReturnType<typeof stubApi>) {
  return fetchMock.mock.calls.filter(([input]) => String(input).includes('/locations/tree')).length;
}

/** row is the line of a node in the tree (name, kind badge and actions). */
async function row(name: string) {
  return (await screen.findByText(name)).closest('div') as HTMLElement;
}

async function openEdit(name: string) {
  fireEvent.click(within(await row(name)).getByRole('button', { name: /Bearbeiten|Edit/ }));
  return screen.findByRole('dialog');
}

const parentSelect = (dialog: HTMLElement) =>
  within(dialog).getByRole('combobox', {
    name: /Übergeordneter Knoten|Parent node/,
  }) as HTMLSelectElement;

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
      return json(node('site-3', 'site', 'Outpost'), 201);
    });
    renderWithProviders(<LocationTreePage />, { route: '/locations' });

    fireEvent.click(await screen.findByRole('button', { name: /Standort anlegen|Create site/ }));
    const dialog = await screen.findByRole('dialog');
    const client = within(dialog).getByRole('combobox') as HTMLSelectElement;
    await waitFor(() => expect(client.options).toHaveLength(2));
    fireEvent.change(client, { target: { value: 'client-1' } });
    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: 'Outpost' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /Anlegen|Create/ }));

    await waitFor(() =>
      expect(created).toEqual([{ kind: 'site', name: 'Outpost', client_id: 'client-1' }]),
    );
  });

  describe('edit dialog (LOC-07, F-079)', () => {
    it('renames a node with PATCH {name} and refetches the tree', async () => {
      const updates: [string, Record<string, unknown>][] = [];
      const fetchMock = stubApi(() => json({}, 500), {
        onUpdate: (body, url) => {
          updates.push([url, body]);
          return json(node('r-1', 'room', String(body.name), [], 'b-1'));
        },
      });
      renderWithProviders(<LocationTreePage />, { route: '/locations' });

      const dialog = await openEdit('Server room');
      expect(within(dialog).getByRole('heading', { name: 'Server room bearbeiten' })).toBeVisible();
      const name = within(dialog).getByRole('textbox', { name: 'Name' });
      expect(name).toHaveValue('Server room');
      const loadsBefore = treeLoads(fetchMock);
      fireEvent.change(name, { target: { value: 'Server room 2' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
      expect(updates).toEqual([['/api/v1/locations/r-1', { name: 'Server room 2' }]]);
      // useUpdateLocation invalidates the tree (and every location detail).
      await waitFor(() => expect(treeLoads(fetchMock)).toBeGreaterThan(loadsBefore));
    });

    it('moves a node with PATCH {parent_id} to a parent of another site', async () => {
      const updates: [string, Record<string, unknown>][] = [];
      stubApi(() => json({}, 500), {
        onUpdate: (body, url) => {
          updates.push([url, body]);
          return json(node('r-1', 'room', 'Server room', [], 'b-2'));
        },
      });
      renderWithProviders(<LocationTreePage />, { route: '/locations' });

      const dialog = await openEdit('Server room');
      const parent = parentSelect(dialog);
      expect(parent).toHaveValue('b-1');
      fireEvent.change(parent, { target: { value: 'b-2' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
      expect(updates).toEqual([['/api/v1/locations/r-1', { parent_id: 'b-2' }]]);
    });

    it('offers only parents allowed by the matrix, never the node itself or a descendant', async () => {
      stubApi(() => json({}, 500));
      renderWithProviders(<LocationTreePage />, { route: '/locations' });

      const options = (dialog: HTMLElement) =>
        Array.from(parentSelect(dialog).options).map((o) => [o.value, o.textContent]);
      const close = (dialog: HTMLElement) =>
        fireEvent.click(within(dialog).getByRole('button', { name: 'Abbrechen' }));

      // A room goes below buildings only; labels name the path from the site.
      let dialog = await openEdit('Server room');
      expect(options(dialog)).toEqual([
        ['b-1', 'HQ › Main building'],
        ['b-2', 'Branch › Annex'],
      ]);
      close(dialog);
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

      // A building goes below sites; its own subtree (room, rack) is not offered.
      dialog = await openEdit('Main building');
      const buildingParents = options(dialog).map(([value]) => value);
      expect(buildingParents).toEqual(['site-1', 'site-2']);
      for (const own of ['b-1', 'r-1', 'k-1']) expect(buildingParents).not.toContain(own);
      close(dialog);
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

      // A warehouse goes below sites only (never below a building).
      dialog = await openEdit('Depot');
      expect(options(dialog).map(([value]) => value)).toEqual(['site-1', 'site-2']);
      close(dialog);
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

      // A site is a root: it can be renamed, not moved.
      dialog = await openEdit('Branch');
      expect(within(dialog).queryByRole('combobox')).not.toBeInTheDocument();
      expect(within(dialog).getByRole('textbox', { name: 'Name' })).toHaveValue('Branch');
    });

    it('shows a 422 at the offending field and keeps the dialog open', async () => {
      const problem = (field: string, detail: string) =>
        json(
          {
            type: 'urn:reticora:problem:validation-error',
            title: 'Validation Error',
            status: 422,
            violations: [{ field, detail }],
          },
          422,
        );
      const answers = [
        problem('parent_id', 'a room cannot be placed below this node'),
        problem('name', 'name already exists at this level'),
      ];
      const updates: Record<string, unknown>[] = [];
      stubApi(() => json({}, 500), {
        onUpdate: (body) => {
          updates.push(body);
          return answers.shift() ?? json({}, 500);
        },
      });
      renderWithProviders(<LocationTreePage />, { route: '/locations' });

      const dialog = await openEdit('Server room');
      fireEvent.change(parentSelect(dialog), { target: { value: 'b-2' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
      const parentError = await within(dialog).findByText(
        'a room cannot be placed below this node',
      );
      expect(parentSelect(dialog)).toHaveAttribute('aria-invalid', 'true');
      expect(parentSelect(dialog)).toHaveAttribute('aria-describedby', parentError.id);

      fireEvent.change(within(dialog).getByRole('textbox', { name: 'Name' }), {
        target: { value: 'Lab' },
      });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
      const nameError = await within(dialog).findByText('name already exists at this level');
      const name = within(dialog).getByRole('textbox', { name: 'Name' });
      expect(name).toHaveAttribute('aria-invalid', 'true');
      expect(name.getAttribute('aria-describedby')).toContain(nameError.id);
      expect(screen.getByRole('dialog')).toBeVisible();
      expect(updates).toEqual([{ parent_id: 'b-2' }, { name: 'Lab', parent_id: 'b-2' }]);
    });

    it('shows a refused write (403) visibly instead of failing silently', async () => {
      stubApi(() => json({}, 500), {
        onUpdate: () => json({ status: 403, title: 'Forbidden', detail: 'forbidden' }, 403),
      });
      renderWithProviders(<LocationTreePage />, { route: '/locations' });

      const dialog = await openEdit('Depot');
      fireEvent.change(within(dialog).getByRole('textbox', { name: 'Name' }), {
        target: { value: 'Depot 2' },
      });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
      expect(await within(dialog).findByRole('alert')).toHaveTextContent(
        'Keine Berechtigung für diese Aktion.',
      );
    });
  });

  describe('delete confirmation and 409 (LOC-11, F-225)', () => {
    it('asks before deleting; cancelling sends no request', async () => {
      const fetchMock = stubApi(() => json({}, 500), { onDelete: () => json(null, 204) });
      renderWithProviders(<LocationTreePage />, { route: '/locations' });

      fireEvent.click(within(await row('Depot')).getByRole('button', { name: 'Löschen' }));
      const dialog = await screen.findByRole('dialog', { name: '„Depot“ löschen?' });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Abbrechen' }));
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
      expect(calls(fetchMock, 'DELETE')).toEqual([]);

      fireEvent.click(within(await row('Depot')).getByRole('button', { name: 'Löschen' }));
      const again = await screen.findByRole('dialog', { name: '„Depot“ löschen?' });
      fireEvent.click(within(again).getByRole('button', { name: 'Löschen' }));
      await waitFor(() => expect(calls(fetchMock, 'DELETE')).toHaveLength(1));
      expect(calls(fetchMock, 'DELETE')[0]?.[0]).toBe('/api/v1/locations/w-1');
      expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    });

    it('names the dependency kinds of a 409 as translated labels (de-DE and en-US)', async () => {
      stubApi(() => json({}, 500), {
        onDelete: () =>
          json(
            {
              type: 'urn:reticora:problem:conflict',
              title: 'Conflict',
              status: 409,
              detail: 'location is still referenced',
              dependencies: ['location', 'ci', 'rack_mount', 'agent_enrollment_token', 'ci_merge'],
            },
            409,
          ),
      });
      renderWithProviders(<LocationTreePage />, { route: '/locations' });

      fireEvent.click(within(await row('HQ')).getByRole('button', { name: 'Löschen' }));
      const dialog = await screen.findByRole('dialog', { name: '„HQ“ löschen?' });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Löschen' }));

      const alert = await screen.findByRole('alert');
      expect(alert).toHaveTextContent(
        'Löschen nicht möglich: noch verwendet von Unterstandorte, CIs, Rack-Montagen, gültige Agent-Enrollment-Tokens, CI-Zusammenführungen (Tombstones)',
      );
      expect(alert).not.toHaveTextContent('location is still referenced');

      await act(async () => {
        await i18n.changeLanguage('en-US');
      });
      expect(await screen.findByRole('alert')).toHaveTextContent(
        'Cannot delete: still referenced by child locations, CIs, rack mounts, valid agent enrollment tokens, CI merges (tombstones)',
      );
    });

    it('shows a refused delete (403) visibly', async () => {
      stubApi(() => json({}, 500), {
        onDelete: () => json({ status: 403, title: 'Forbidden', detail: 'forbidden' }, 403),
      });
      renderWithProviders(<LocationTreePage />, { route: '/locations' });

      fireEvent.click(within(await row('Depot')).getByRole('button', { name: 'Löschen' }));
      const dialog = await screen.findByRole('dialog', { name: '„Depot“ löschen?' });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Löschen' }));
      expect(await screen.findByRole('alert')).toHaveTextContent(
        'Löschen nicht möglich: Keine Berechtigung für diese Aktion.',
      );
    });

    it('has a label in both locales for every kind of DependencyProblem', () => {
      type DependencyKind = components['schemas']['DependencyProblem']['dependencies'][number];
      // The Record type makes tsc fail when the API adds a kind without a label here.
      const kinds: Record<DependencyKind, true> = {
        location: true,
        ci: true,
        asset: true,
        quantity_item: true,
        asset_movement: true,
        rack_mount: true,
        role_assignment: true,
        vlan: true,
        subnet: true,
        collector: true,
        discovery_scope: true,
        agent_enrollment_token: true,
        ci_merge: true,
      };
      for (const lng of ['de-DE', 'en-US']) {
        for (const kind of Object.keys(kinds)) {
          // getResource reads the locale itself, without the en-US fallback.
          const text: unknown = i18n.getResource(
            lng,
            'translation',
            `locations.dependencies.${kind}`,
          );
          expect(typeof text, `${lng}: locations.dependencies.${kind}`).toBe('string');
          expect(text).not.toBe(kind);
        }
      }
    });
  });

  // F-334 (WP-594): a search hit of a location links to /locations?node=<id>;
  // the page opens the path to that node, marks it and moves focus to it.
  describe('deep link ?node=<id>', () => {
    it('opens the path to a collapsed node, marks it and focuses it', async () => {
      stubApi(() => json({}, 500));
      renderWithProviders(<LocationTreePage />, { route: '/locations?node=k-1' });

      // Rack A1 sits below the room at depth 2, which starts collapsed.
      const rack = await row('Rack A1');
      expect(rack).toHaveAttribute('aria-current', 'true');
      await waitFor(() => expect(document.activeElement).toBe(rack));
      const room = await row('Server room');
      expect(within(room).getByRole('button', { name: 'collapse' })).toHaveAttribute(
        'aria-expanded',
        'true',
      );
      // Exactly the linked node is marked; the other branches keep their state.
      expect(document.querySelectorAll('[aria-current]')).toHaveLength(1);
      expect(screen.getByText('Lab')).toBeInTheDocument();
      expect(screen.queryByRole('status')).not.toBeInTheDocument();
    });

    it('marks a node of another site', async () => {
      stubApi(() => json({}, 500));
      renderWithProviders(<LocationTreePage />, { route: '/locations?node=r-2' });

      const lab = await row('Lab');
      expect(lab).toHaveAttribute('aria-current', 'true');
      await waitFor(() => expect(document.activeElement).toBe(lab));
      expect(await row('Server room')).not.toHaveAttribute('aria-current');
    });

    it('says so when the linked node is not in the visible tree', async () => {
      stubApi(() => json({}, 500));
      renderWithProviders(<LocationTreePage />, { route: '/locations?node=gone' });

      expect(await screen.findByText('HQ')).toBeInTheDocument();
      expect(await screen.findByRole('status')).toHaveTextContent(
        'Der verlinkte Knoten ist nicht vorhanden oder für Sie nicht sichtbar.',
      );
      expect(document.querySelectorAll('[aria-current]')).toHaveLength(0);
      // Without the link the tree starts as before: the rack stays collapsed.
      expect(screen.queryByText('Rack A1')).not.toBeInTheDocument();
    });
  });
});
