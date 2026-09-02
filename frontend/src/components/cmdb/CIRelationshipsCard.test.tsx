/**
 * Regression tests for the relationship management surface.
 *
 * The backend has long exposed POST/DELETE and now PATCH for relationship
 * verification metadata, but the CI detail page rendered a read-only list, so
 * a discovered edge could never be confirmed or disputed and manual edges
 * could not be created. These tests pin the verify/delete wiring.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { CIRelationshipsCard, verificationBadgeVariant } from './CIRelationshipsCard';

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

  it('deletes a relationship through the relationships endpoint', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'DELETE') return new Response(null, { status: 204 });
        return json(relationshipList('unverified'));
      });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    const deleteButton = await screen.findByRole('button', { name: /löschen|delete/i });
    fireEvent.click(deleteButton);

    await waitFor(() => {
      const del = fetchMock.mock.calls.find((call) => call[1]?.method === 'DELETE');
      expect(del?.[0]).toBe('/api/v1/relationships/rel-1');
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
