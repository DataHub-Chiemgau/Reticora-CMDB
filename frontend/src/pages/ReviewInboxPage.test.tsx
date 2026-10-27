/**
 * Tests for the review inbox (UI-16): filter, diff, single and bulk
 * resolution; new of a device of an unknown type asks for the type, a
 * device recorded as generic_device is not offered new (WP-528, F-176).
 * Paging with "x von total" and the selection of all open items beyond the
 * page (F-345), merge candidates named by their CI (F-346) and no
 * resolution without review:resolve (WP-557).
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { ReviewInboxPage, commonActions, reviewDiff } from './ReviewInboxPage';
import type { ReviewItem } from '../hooks/useReviews';
import type { CI } from '../api/client';

const CI_A = '11111111-1111-4111-8111-111111111111';
const CI_B = '22222222-2222-4222-8222-222222222222';

const ambiguous: ReviewItem = {
  id: 'rev-1',
  kind: 'ambiguous_identity',
  status: 'open',
  payload: { name: 'sw-core-01', serial_number: 'SN-NEW', management_ip: '10.0.0.5' },
  candidate_ci_ids: [CI_A, CI_B],
  escalated: true,
  created_at: '2026-10-01T10:00:00Z',
};
const conflict: ReviewItem = {
  id: 'rev-2',
  kind: 'conflicting_values',
  status: 'open',
  payload: { ci_id: CI_A, field: 'firmware_version', observed_value: '2.1', source: 'snmp' },
  escalated: false,
  created_at: '2026-10-10T10:00:00Z',
};
const duplicateIP: ReviewItem = {
  id: 'rev-3',
  kind: 'duplicate_ip',
  status: 'open',
  payload: { management_ip: '10.0.0.9' },
  escalated: false,
  created_at: '2026-10-11T10:00:00Z',
};

const SERVER_TYPE = '33333333-3333-4333-8333-333333333333';
const WIDGET_TYPE = '44444444-4444-4444-8444-444444444444';
const ciTypes = [
  { id: SERVER_TYPE, key: 'server', name: 'server', display_name: { de: 'Server', en: 'Server' } },
  {
    id: WIDGET_TYPE,
    key: 'pv-widget',
    name: 'pv-widget',
    display_name: { de: 'Widget', en: 'Widget' },
  },
];
const unknownType: ReviewItem = {
  id: 'rev-4',
  kind: 'unclassified_device',
  status: 'open',
  payload: { reason: 'unknown_ci_type', ci_type_name: 'pv-unknown-type', name: 'pv-gadget-01' },
  escalated: false,
  created_at: '2026-10-12T10:00:00Z',
};
const noProfile: ReviewItem = {
  id: 'rev-5',
  kind: 'unclassified_device',
  status: 'open',
  payload: {
    reason: 'no_profile_match',
    ci_id: CI_A,
    ci_type_name: 'generic_device',
    name: 'mystery-box',
  },
  candidate_ci_ids: [CI_A],
  escalated: false,
  created_at: '2026-10-13T10:00:00Z',
};

const ciA = {
  id: CI_A,
  name: 'sw-core-01',
  status: 'active',
  ci_type_id: 'type-switch',
  serial_number: 'SN-OLD',
  management_ip: '10.0.0.5',
  firmware_version: '2.0',
  attributes: {},
} as unknown as CI;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function page(data: unknown[]) {
  return { data, total: data.length, limit: 100, offset: 0, has_more: false };
}

/** reviewPage answers GET /review-items like the server: limit/offset of the query. */
function reviewPage(items: ReviewItem[], url: string) {
  const query = new URL(url, 'http://localhost').searchParams;
  const limit = Number(query.get('limit') ?? '50');
  const offset = Number(query.get('offset') ?? '0');
  const data = items.slice(offset, offset + limit);
  return {
    data,
    total: items.length,
    limit,
    offset,
    has_more: offset + data.length < items.length,
  };
}

const RESOLVER = ['discovery:read', 'review:resolve'];

function stubServer(
  items: ReviewItem[],
  resolve?: (id: string, body: unknown) => Response,
  permissions: string[] = RESOLVER,
) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const match = url.match(/\/review-items\/([^/]+)\/resolve$/);
    if (match && init?.method === 'POST') {
      return resolve
        ? resolve(match[1]!, JSON.parse(String(init.body)))
        : json({ id: match[1], status: 'resolved' });
    }
    if (url.includes('/me/permissions')) return json({ permissions });
    if (url.includes('/review-items')) return json(reviewPage(items, url));
    if (url.includes('/ci-types')) return json(page(ciTypes));
    if (url.includes(`/cis/${CI_A}`)) return json(ciA);
    if (url.includes(`/cis/${CI_B}`))
      return json({
        ...ciA,
        id: CI_B,
        name: 'sw-core-02',
        management_ip: '10.0.0.6',
        serial_number: 'SN-B',
      });
    return json({ detail: 'unexpected' }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('reviewDiff', () => {
  it('compares the observed fields with the CI', () => {
    const rows = reviewDiff(ambiguous, ciA);
    expect(rows).toEqual([
      { field: 'name', observed: 'sw-core-01', current: 'sw-core-01', differs: false },
      { field: 'serial_number', observed: 'SN-NEW', current: 'SN-OLD', differs: true },
      { field: 'management_ip', observed: '10.0.0.5', current: '10.0.0.5', differs: false },
    ]);
  });

  it('shows the conflicting field of a value conflict', () => {
    expect(reviewDiff(conflict, ciA)).toEqual([
      { field: 'firmware_version', observed: '2.1', current: '2.0', differs: true },
    ]);
  });
});

describe('commonActions', () => {
  it('offers only actions every selected item allows, never merge', () => {
    expect(commonActions([ambiguous])).toEqual(['new', 'dismiss']);
    expect(commonActions([ambiguous, conflict])).toEqual(['dismiss']);
    expect(commonActions([])).toEqual([]);
  });

  it('never offers new for an unclassified device in bulk', () => {
    expect(commonActions([unknownType])).toEqual(['dismiss']);
    expect(commonActions([noProfile])).toEqual(['dismiss']);
  });
});

describe('ReviewInboxPage', () => {
  it('filters by type and escalation', async () => {
    const fetchMock = stubServer([ambiguous, conflict]);
    renderWithProviders(<ReviewInboxPage />);

    await screen.findByText('sw-core-01');
    fireEvent.change(screen.getByLabelText('Typ'), { target: { value: 'conflicting_values' } });
    fireEvent.click(screen.getByLabelText('Nur eskalierte'));

    await waitFor(() => {
      const urls = fetchMock.mock.calls.map((call) => String(call[0]));
      expect(
        urls.some(
          (url) =>
            url.includes('status=open') &&
            url.includes('kind=conflicting_values') &&
            url.includes('escalated=true'),
        ),
      ).toBe(true);
    });
  });

  it('shows the diff and merges into the chosen candidate', async () => {
    const fetchMock = stubServer([ambiguous]);
    renderWithProviders(<ReviewInboxPage />);

    fireEvent.click(await screen.findByRole('button', { name: /sw-core-01/ }));
    const table = await screen.findByRole('table', { name: 'Vergleich von Beobachtung und CI' });
    await waitFor(() => expect(within(table).getByText('SN-OLD')).toBeInTheDocument());
    expect(within(table).getByText('abweichend')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Kandidat'), { target: { value: CI_B } });
    fireEvent.click(screen.getByRole('button', { name: 'Zusammenführen' }));

    await waitFor(() => {
      const post = fetchMock.mock.calls.find((call) => call[1]?.method === 'POST');
      expect(String(post?.[0])).toBe('/api/v1/review-items/rev-1/resolve');
      expect(JSON.parse(String(post?.[1]?.body))).toEqual({ action: 'merge', ci_id: CI_B });
    });
  });

  it('offers only the allowed actions of a type', async () => {
    stubServer([duplicateIP]);
    renderWithProviders(<ReviewInboxPage />);

    fireEvent.click(await screen.findByRole('button', { name: /10\.0\.0\.9/ }));
    expect(await screen.findByRole('button', { name: 'Verwerfen' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Zusammenführen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Übernehmen' })).not.toBeInTheDocument();
  });

  it('resolves several items with one action and reports failures', async () => {
    const fetchMock = stubServer([ambiguous, conflict, duplicateIP], (id) =>
      id === 'rev-2'
        ? json({ title: 'Conflict', status: 409, detail: 'already resolved' }, 409)
        : json({ id, status: 'dismissed' }),
    );
    renderWithProviders(<ReviewInboxPage />);

    fireEvent.click(await screen.findByLabelText('Alle offenen auswählen (3 auf dieser Seite)'));
    expect(screen.getByText('3 ausgewählt')).toBeInTheDocument();
    const action = screen.getByLabelText('Aktion');
    // dismiss is the only action every selected type allows.
    expect(
      within(action)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Aktion wählen', 'Verwerfen']);
    fireEvent.change(action, { target: { value: 'dismiss' } });
    fireEvent.click(screen.getByRole('button', { name: 'Ausführen' }));

    expect(await screen.findByText('2 erledigt, 1 fehlgeschlagen.')).toBeInTheDocument();
    const posts = fetchMock.mock.calls.filter((call) => call[1]?.method === 'POST');
    expect(posts).toHaveLength(3);
    for (const post of posts) {
      expect(JSON.parse(String(post[1]?.body))).toEqual({ action: 'dismiss' });
    }
  });

  it('asks for the type of a device of an unknown type and sends ci_type_id', async () => {
    const fetchMock = stubServer([unknownType]);
    renderWithProviders(<ReviewInboxPage />);

    fireEvent.click(await screen.findByRole('button', { name: /pv-gadget-01/ }));
    const create = await screen.findByRole('button', { name: 'Neu anlegen' });
    // Without a type new cannot succeed: the button waits for the choice.
    expect(create).toBeDisabled();
    expect(screen.getByText(/meldete den Typ „pv-unknown-type“/)).toBeInTheDocument();
    const select = screen.getByLabelText('CI-Typ des neuen CIs');
    await waitFor(() =>
      expect(
        within(select)
          .getAllByRole('option')
          .map((o) => o.textContent),
      ).toEqual(['CI-Typ wählen', 'Server', 'Widget']),
    );
    fireEvent.change(select, { target: { value: WIDGET_TYPE } });
    expect(create).toBeEnabled();
    fireEvent.click(create);

    await waitFor(() => {
      const post = fetchMock.mock.calls.find((call) => call[1]?.method === 'POST');
      expect(String(post?.[0])).toBe('/api/v1/review-items/rev-4/resolve');
      expect(JSON.parse(String(post?.[1]?.body))).toEqual({
        action: 'new',
        ci_type_id: WIDGET_TYPE,
      });
    });
  });

  it('preselects the reported type once it exists', async () => {
    const fetchMock = stubServer([
      { ...unknownType, payload: { ...unknownType.payload, ci_type_name: 'pv-widget' } },
    ]);
    renderWithProviders(<ReviewInboxPage />);

    fireEvent.click(await screen.findByRole('button', { name: /pv-gadget-01/ }));
    const create = await screen.findByRole('button', { name: 'Neu anlegen' });
    await waitFor(() => expect(create).toBeEnabled());
    expect(screen.getByLabelText('CI-Typ des neuen CIs')).toHaveValue(WIDGET_TYPE);
    fireEvent.click(create);

    await waitFor(() => {
      const post = fetchMock.mock.calls.find((call) => call[1]?.method === 'POST');
      expect(JSON.parse(String(post?.[1]?.body))).toEqual({
        action: 'new',
        ci_type_id: WIDGET_TYPE,
      });
    });
  });

  it('shows the 4xx message of the server', async () => {
    stubServer([unknownType], () =>
      json(
        {
          type: 'urn:reticora:problem:invalid-field',
          title: 'Unprocessable Entity',
          status: 422,
          detail: 'ci_type_id: choose a CI type for the new CI',
          field: 'ci_type_id',
        },
        422,
      ),
    );
    renderWithProviders(<ReviewInboxPage />);

    fireEvent.click(await screen.findByRole('button', { name: /pv-gadget-01/ }));
    const select = await screen.findByLabelText('CI-Typ des neuen CIs');
    await waitFor(() => expect(within(select).getAllByRole('option')).toHaveLength(3));
    fireEvent.change(select, { target: { value: SERVER_TYPE } });
    fireEvent.click(screen.getByRole('button', { name: 'Neu anlegen' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'ci_type_id: choose a CI type for the new CI',
    );
  });

  it('does not offer new for a device recorded as generic_device and shows the next step', async () => {
    stubServer([noProfile]);
    renderWithProviders(<ReviewInboxPage />);

    fireEvent.click(await screen.findByRole('button', { name: /mystery-box/ }));
    expect(await screen.findByRole('button', { name: 'Verwerfen' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Neu anlegen' })).not.toBeInTheDocument();
    expect(screen.getByText(/Setzen Sie den Typ am CI/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'CI öffnen' })).toHaveAttribute(
      'href',
      `/cmdb/${CI_A}`,
    );
    expect(screen.queryByLabelText('CI-Typ des neuen CIs')).not.toBeInTheDocument();
  });

  it('names the merge candidates by CI name, management IP and serial number (F-346)', async () => {
    stubServer([ambiguous]);
    renderWithProviders(<ReviewInboxPage />);

    fireEvent.click(await screen.findByRole('button', { name: /sw-core-01/ }));
    const select = await screen.findByLabelText('Kandidat');
    await waitFor(() =>
      expect(
        within(select)
          .getAllByRole('option')
          .map((o) => o.textContent),
      ).toEqual(['sw-core-01 (10.0.0.5, SN-OLD)', 'sw-core-02 (10.0.0.6, SN-B)']),
    );
    // The value stays the id of the CI the merge names.
    expect(
      within(select)
        .getAllByRole('option')
        .map((o) => (o as HTMLOptionElement).value),
    ).toEqual([CI_A, CI_B]);
  });

  it('pages with offset when has_more is true and shows x of total (F-345)', async () => {
    const many = Array.from({ length: 150 }, (_, i) => ({
      ...duplicateIP,
      id: `dup-${i}`,
      payload: { management_ip: `10.1.${Math.floor(i / 250)}.${i}` },
    }));
    const fetchMock = stubServer(many);
    renderWithProviders(<ReviewInboxPage />);

    expect(await screen.findByText('1–100 von 150')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Vorherige Seite' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Nächste Seite' }));

    expect(await screen.findByText('101–150 von 150')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /10\.1\.0\.149/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /10\.1\.0\.0$/ })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Nächste Seite' })).toBeDisabled();
    const urls = fetchMock.mock.calls
      .map((call) => String(call[0]))
      .filter((url) => url.includes('/review-items?'));
    expect(urls[0]).toContain('limit=100');
    expect(urls[0]).not.toContain('offset=');
    expect(urls.some((url) => url.includes('limit=100') && url.includes('offset=100'))).toBe(true);
  });

  it('names the scope of the page selection and selects all open items beyond the page (F-345)', async () => {
    const many = Array.from({ length: 150 }, (_, i) => ({
      ...duplicateIP,
      id: `dup-${i}`,
      payload: { management_ip: `10.1.0.${i}` },
    }));
    const fetchMock = stubServer(many);
    renderWithProviders(<ReviewInboxPage />);

    fireEvent.click(await screen.findByLabelText('Alle offenen auswählen (100 auf dieser Seite)'));
    expect(screen.getByText('100 ausgewählt')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Alle 150 offenen auswählen' }));
    expect(await screen.findByText('150 ausgewählt')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Aktion'), { target: { value: 'dismiss' } });
    fireEvent.click(screen.getByRole('button', { name: 'Ausführen' }));
    expect(await screen.findByText('150 erledigt, 0 fehlgeschlagen.')).toBeInTheDocument();
    const posted = fetchMock.mock.calls
      .filter((call) => call[1]?.method === 'POST')
      .map((call) => String(call[0]));
    expect(new Set(posted).size).toBe(150);
    expect(posted).toContain('/api/v1/review-items/dup-149/resolve');
  });

  it('offers no resolution without review:resolve', async () => {
    const fetchMock = stubServer([ambiguous, duplicateIP], undefined, ['discovery:read']);
    renderWithProviders(<ReviewInboxPage />);

    fireEvent.click(await screen.findByRole('button', { name: /sw-core-01/ }));
    const table = await screen.findByRole('table', { name: 'Vergleich von Beobachtung und CI' });
    await waitFor(() => expect(within(table).getByText('SN-OLD')).toBeInTheDocument());
    await waitFor(() =>
      expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('/me/permissions'))).toBe(
        true,
      ),
    );
    for (const name of ['Zusammenführen', 'Neu anlegen', 'Verwerfen']) {
      expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
    }
    expect(screen.queryByRole('checkbox', { name: /auswählen/ })).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/Alle offenen auswählen/)).not.toBeInTheDocument();
  });
});
