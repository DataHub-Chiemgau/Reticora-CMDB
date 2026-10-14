/**
 * Tests for the review inbox (UI-16): filter, diff, single and bulk
 * resolution.
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

function stubServer(items: ReviewItem[], resolve?: (id: string, body: unknown) => Response) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const match = url.match(/\/review-items\/([^/]+)\/resolve$/);
    if (match && init?.method === 'POST') {
      return resolve
        ? resolve(match[1]!, JSON.parse(String(init.body)))
        : json({ id: match[1], status: 'resolved' });
    }
    if (url.includes('/review-items')) return json(page(items));
    if (url.includes(`/cis/${CI_A}`)) return json(ciA);
    if (url.includes(`/cis/${CI_B}`)) return json({ ...ciA, id: CI_B, name: 'sw-core-02' });
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

    fireEvent.click(await screen.findByLabelText('Alle offenen auswählen'));
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
});
