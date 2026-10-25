/**
 * Tests for the error state of the provenance section (F-350, REC-10): a
 * failed provenance request shows an error with retry, not the empty state
 * "no provenance data yet" that tells the user no provenance exists.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { ProvenanceSection } from './CIDetailSections';
import type { CIFieldProvenance } from './ProvenanceTooltip';

const CI_ID = '11111111-1111-1111-1111-111111111111';

const record: CIFieldProvenance = {
  field: 'name',
  value: 'sw-core-01',
  source: 'manual',
  rank: 100,
  reason: 'record',
  recorded_at: '2026-10-14T06:00:00Z',
  diverged: false,
};

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

describe('ProvenanceSection', () => {
  it('shows an error with retry instead of the empty state when the API fails', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        json({ title: 'Internal Server Error', status: 500, detail: 'internal error' }, 500),
      )
      .mockResolvedValueOnce(json({ data: [record], total: 1 }));
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<ProvenanceSection ciId={CI_ID} />);

    // The request has failed once the skeleton is gone.
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
    expect(screen.queryByText(/Noch keine Provenienz-Daten/)).toBeNull();
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Die Herkunftsdaten konnten nicht geladen werden');
    // Nor does the collapsed header count zero diverged fields.
    const summary = screen.getByText('Herkunft & Overrides');
    expect(summary).toHaveTextContent(/^Herkunft & Overrides$/);

    fireEvent.click(within(alert).getByRole('button', { name: 'Erneut versuchen' }));

    expect(await screen.findByRole('button', { name: 'Herkunft von name' })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[1]?.[0])).toBe(`/api/v1/cis/${CI_ID}/provenance`);
    expect(screen.queryByRole('alert')).toBeNull();
    expect(summary).toHaveTextContent(/^Herkunft & Overrides0$/);
  });
});
