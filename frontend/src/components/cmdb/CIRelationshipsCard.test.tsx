/**
 * Regression tests for the relationship management surface.
 *
 * The backend has long exposed POST/DELETE and now PATCH for relationship
 * verification metadata, but the CI detail page rendered a read-only list, so
 * a discovered edge could never be confirmed or disputed and manual edges
 * could not be created. These tests pin the verify/delete wiring and the
 * create dialog: a double click creates one edge and closes the dialog
 * (F-314), an invisible or unknown endpoint is reported at the CI ID field in
 * the UI language (F-145). With a chosen client or site the card lists only
 * edges to CIs in that scope and names the hidden ones (E-87, F-169).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { CIRelationshipsCard, verificationBadgeVariant } from './CIRelationshipsCard';
import { useScopeStore } from '../../stores/scope';

const CI_ID = '11111111-1111-1111-1111-111111111111';
const OTHER_ID = '22222222-2222-2222-2222-222222222222';

function relationshipList(verificationState: string) {
  return {
    data: [
      {
        id: 'rel-1',
        organization_id: 'org-1',
        source_ci_id: CI_ID,
        target_ci_id: OTHER_ID,
        rel_type: 'depends_on',
        attributes: {},
        source: 'discovery',
        source_system: 'nmap',
        verification_state: verificationState,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
      },
    ],
    total: 1,
    limit: 50,
    offset: 0,
    has_more: false,
  };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('verificationBadgeVariant', () => {
  it('maps the verification vocabulary onto distinct badge variants', () => {
    expect(verificationBadgeVariant('verified')).toBe('success');
    expect(verificationBadgeVariant('disputed')).toBe('danger');
    expect(verificationBadgeVariant('stale')).toBe('warning');
    expect(verificationBadgeVariant('unverified')).toBe('neutral');
    // An unknown or absent state must not be shown as confirmed.
    expect(verificationBadgeVariant(undefined)).toBe('neutral');
  });
});

describe('CIRelationshipsCard', () => {
  it('verifies an unverified relationship with a PATCH', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'PATCH') return json({ id: 'rel-1' });
        return json(relationshipList('unverified'));
      });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    const verifyButton = await screen.findByRole('button', { name: /bestätigen|verify/i });
    fireEvent.click(verifyButton);

    await waitFor(() => {
      const patch = fetchMock.mock.calls.find((call) => call[1]?.method === 'PATCH');
      expect(patch).toBeDefined();
      expect(patch?.[0]).toBe('/api/v1/relationships/rel-1');
      expect(JSON.parse(patch?.[1]?.body as string)).toEqual({ verification_state: 'verified' });
    });
  });

  it('does not offer a verify action for an already verified relationship', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json(relationshipList('verified'))),
    );

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    // Wait for the list to settle before asserting on absence.
    await screen.findByText('depends_on');
    expect(screen.queryByRole('button', { name: /bestätigen|verify/i })).toBeNull();
  });

  it('removes a discovery edge with force so it stays suppressed (REL-04)', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'DELETE') return new Response(null, { status: 204 });
        return json(relationshipList('unverified'));
      });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    const deleteButton = await screen.findByRole('button', { name: /unterdrücken|suppress/i });
    fireEvent.click(deleteButton);

    await waitFor(() => {
      const del = fetchMock.mock.calls.find((call) => call[1]?.method === 'DELETE');
      expect(del?.[0]).toBe('/api/v1/relationships/rel-1?force=true');
    });
  });

  it('surfaces a rejected mutation instead of failing silently', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'PATCH') {
          return json({ status: 422, detail: 'invalid verification_state' }, 422);
        }
        return json(relationshipList('unverified'));
      }),
    );

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    fireEvent.click(await screen.findByRole('button', { name: /bestätigen|verify/i }));

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('invalid verification_state');
    });
  });
});

describe('CIRelationshipsCard create dialog', () => {
  const emptyList = { data: [], total: 0, limit: 50, offset: 0, has_more: false };

  /** Opens the create dialog and enters OTHER_ID as the other end. */
  async function openCreateDialog(direction?: 'outgoing' | 'incoming') {
    fireEvent.click(await screen.findByRole('button', { name: /Beziehung hinzufügen/ }));
    const dialog = await screen.findByRole('dialog');
    if (direction) {
      fireEvent.change(within(dialog).getByLabelText('Richtung'), {
        target: { value: direction },
      });
    }
    fireEvent.change(within(dialog).getByLabelText('CI-ID der Gegenstelle'), {
      target: { value: OTHER_ID },
    });
    return dialog;
  }

  function posts(fetchMock: ReturnType<typeof vi.fn>) {
    return fetchMock.mock.calls.filter((call) => call[1]?.method === 'POST');
  }

  it('creates the edge once on a double click and closes the dialog (F-314)', async () => {
    // The server stores the first create; a repeat is refused as a
    // conflict, as the unique edge key does on the real stack.
    let created = 0;
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') {
        created++;
        if (created > 1) {
          return json(
            {
              type: 'https://reticora.io/problems/409',
              title: 'Conflict',
              status: 409,
              detail: 'a record with the same unique value already exists',
            },
            409,
          );
        }
        return json({ id: 'rel-new', source_ci_id: CI_ID, target_ci_id: OTHER_ID }, 201);
      }
      return json(emptyList);
    });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    const dialog = await openCreateDialog();
    const submit = within(dialog).getByRole('button', { name: 'Anlegen' });
    // Both clicks land before React renders the pending state that disables
    // the button: the worst case of a real double click.
    act(() => {
      submit.click();
      submit.click();
    });

    // Settled: the dialog closed, or it reports an error.
    await waitFor(() => {
      const open = screen.queryByRole('dialog');
      expect(open === null || within(open).queryByRole('alert') !== null).toBe(true);
    });
    const open = screen.queryByRole('dialog');
    expect({
      posts: posts(fetchMock).length,
      dialogOpen: open !== null,
      error: open ? (within(open).queryByRole('alert')?.textContent ?? null) : null,
    }).toEqual({ posts: 1, dialogOpen: false, error: null });
    expect(JSON.parse(posts(fetchMock)[0]?.[1]?.body as string)).toMatchObject({
      source_ci_id: CI_ID,
      target_ci_id: OTHER_ID,
      source: 'manual',
    });
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it.each([
    ['outgoing', 'target_ci_id', { source_ci_id: CI_ID, target_ci_id: OTHER_ID }],
    ['incoming', 'source_ci_id', { source_ci_id: OTHER_ID, target_ci_id: CI_ID }],
  ] as const)(
    'shows a 422 on %s %s at the CI ID field in the UI language (F-145)',
    async (direction, field, ends) => {
      const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'POST') {
          return json(
            {
              type: 'https://reticora.io/problems/422',
              title: 'Unprocessable Entity',
              status: 422,
              detail: `${field}: ${field === 'target_ci_id' ? 'target' : 'source'} CI not found`,
              violations: [
                {
                  field,
                  detail: `${field === 'target_ci_id' ? 'target' : 'source'} CI not found`,
                },
              ],
            },
            422,
          );
        }
        return json(emptyList);
      });
      vi.stubGlobal('fetch', fetchMock);

      renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
      const dialog = await openCreateDialog(direction);
      fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

      const input = within(dialog).getByLabelText('CI-ID der Gegenstelle');
      await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'));
      expect(input).toHaveAccessibleDescription('Kein CI mit dieser ID gefunden.');
      expect(input).toHaveValue(OTHER_ID);
      // The server's English detail is not shown, neither in the dialog nor
      // on the card.
      expect(document.body).not.toHaveTextContent(/CI not found/);
      expect(screen.queryByRole('alert')).toBeNull();
      expect(screen.getByRole('dialog')).toBeInTheDocument();
      expect(posts(fetchMock)).toHaveLength(1);
      expect(JSON.parse(posts(fetchMock)[0]?.[1]?.body as string)).toMatchObject(ends);
    },
  );
});

describe('CIRelationshipsCard under a chosen scope (E-87)', () => {
  const CLIENT_A = 'aaaaaaaa-0000-4000-8000-000000000001';
  const CLIENT_B = 'aaaaaaaa-0000-4000-8000-000000000002';
  const SITE_A1 = 'bbbbbbbb-0000-4000-8000-000000000001';
  const SITE_A2 = 'bbbbbbbb-0000-4000-8000-000000000002';
  const IN_A = '33333333-3333-3333-3333-333333333331';
  const IN_A2 = '33333333-3333-3333-3333-333333333332';
  const IN_B = '33333333-3333-3333-3333-333333333333';
  const IN_B2 = '33333333-3333-3333-3333-333333333334';
  const UNREADABLE = '33333333-3333-3333-3333-333333333335';

  const others: Record<string, { client_id?: string; site_id?: string }> = {
    [IN_A]: { client_id: CLIENT_A, site_id: SITE_A1 },
    [IN_A2]: { client_id: CLIENT_A, site_id: SITE_A2 },
    [IN_B]: { client_id: CLIENT_B },
    [IN_B2]: { client_id: CLIENT_B },
  };

  function edge(id: string, source: string, target: string, extra: object = {}) {
    return {
      id,
      organization_id: 'org-1',
      source_ci_id: source,
      target_ci_id: target,
      rel_type: 'depends_on',
      attributes: {},
      source: 'manual',
      verification_state: 'verified',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
      ...extra,
    };
  }

  const edges = [
    edge('rel-a', CI_ID, IN_A),
    edge('rel-a2', IN_A2, CI_ID),
    edge('rel-b', CI_ID, IN_B),
    edge('rel-b2', IN_B2, CI_ID),
    edge('rel-x', CI_ID, UNREADABLE),
    edge('rel-loc', CI_ID, '', { target_location_id: 'loc-1', projected: true }),
  ];

  function stubServer() {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      if (url.pathname === `/api/v1/cis/${CI_ID}/relationships`)
        return json({ data: edges, total: edges.length, limit: 50, offset: 0, has_more: false });
      const id = url.pathname.replace('/api/v1/cis/', '');
      const other = others[id];
      if (other)
        return json({
          id,
          organization_id: 'org-1',
          ci_type_id: 'server',
          name: id,
          status: 'active',
          attributes: {},
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          ...other,
        });
      return json({ detail: 'not found' }, 404);
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  const listedIds = () =>
    screen.queryAllByRole('link').map((link) => link.getAttribute('href')?.replace('/cmdb/', ''));

  beforeEach(() => {
    window.localStorage.clear();
    useScopeStore.getState().reset();
  });
  afterEach(() => useScopeStore.getState().reset());

  it('lists only edges to CIs of the chosen client and names the hidden ones', async () => {
    stubServer();
    useScopeStore.setState({ clientId: CLIENT_A, siteId: '' });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    expect(await screen.findByText('2 außerhalb des Bereichs ausgeblendet')).toBeInTheDocument();
    // Both directions count; an edge to a location and one whose other CI
    // cannot be read are not known to lie outside and stay listed.
    expect(listedIds().sort()).toEqual([IN_A, IN_A2, UNREADABLE].sort());
    expect(screen.getByText('loc-1')).toBeInTheDocument();
    expect(screen.queryByText('Außerhalb des Bereichs')).toBeNull();

    // The toggle shows all edges and marks the outside ones.
    fireEvent.click(screen.getByRole('button', { name: 'Alle anzeigen' }));
    expect(listedIds().sort()).toEqual([IN_A, IN_A2, IN_B, IN_B2, UNREADABLE].sort());
    expect(screen.getAllByText('Außerhalb des Bereichs')).toHaveLength(2);
    expect(screen.getByText('2 außerhalb des Bereichs ausgeblendet')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Nur Bereich anzeigen' }));
    expect(listedIds()).not.toContain(IN_B);
  });

  it('narrows to the chosen site as well', async () => {
    stubServer();
    useScopeStore.setState({ clientId: CLIENT_A, siteId: SITE_A1 });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    expect(await screen.findByText('3 außerhalb des Bereichs ausgeblendet')).toBeInTheDocument();
    expect(listedIds().sort()).toEqual([IN_A, UNREADABLE].sort());
  });

  it('lists every edge and reads no CI without a selection', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    await waitFor(() => expect(listedIds()).toHaveLength(5));
    expect(screen.queryByText(/außerhalb des Bereichs/)).toBeNull();
    expect(screen.queryByRole('button', { name: 'Alle anzeigen' })).toBeNull();
    expect(fetchMock.mock.calls.map((call) => String(call[0]))).toEqual([
      `/api/v1/cis/${CI_ID}/relationships`,
    ]);
  });
});
