/**
 * Tests for the error state of the provenance section (F-350, REC-10): a
 * failed provenance request shows an error with retry, not the empty state
 * "no provenance data yet" that tells the user no provenance exists.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../../i18n';
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

  it('shows the status through the catalog, not the raw value (WP-523, GLO-10)', async () => {
    const status: CIFieldProvenance = { ...record, field: 'status', value: 'maintenance' };
    const override: CIFieldProvenance = {
      ...record,
      field: 'status',
      value: 'maintenance',
      reason: 'override',
      override_author: 'admin',
      override_reason: 'Wartungsfenster',
      discovered_value: 'active',
      discovered_source: 'snmp',
      diverged: true,
    };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json({ data: [status], total: 1 })),
    );

    const first = renderWithProviders(<ProvenanceSection ciId={CI_ID} />);
    const button = await screen.findByRole('button', { name: 'Herkunft von status' });
    const row = button.closest('li')!;
    expect(within(row).getByText('Discovery').nextElementSibling).toHaveTextContent(/^Wartung$/);
    expect(within(row).getByText('Effektiv').nextElementSibling).toHaveTextContent(/^Wartung$/);
    expect(first.container.textContent).not.toContain('maintenance');
    first.unmount();

    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json({ data: [override], total: 1 })),
    );
    const { container } = renderWithProviders(<ProvenanceSection ciId={CI_ID} />);
    const overridden = await screen.findByRole('button', { name: 'Herkunft von status' });
    const overriddenRow = overridden.closest('li')!;
    expect(within(overriddenRow).getByText('Discovery').nextElementSibling).toHaveTextContent(
      /^Aktiv$/,
    );
    expect(within(overriddenRow).getByText('Override').nextElementSibling).toHaveTextContent(
      /^Wartung$/,
    );
    fireEvent.focus(overridden);
    expect(screen.getByRole('tooltip')).toHaveTextContent('Aktiv (snmp)');
    expect(container.textContent).not.toMatch(/maintenance|active/);

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    try {
      await waitFor(() => expect(screen.getByRole('tooltip')).toHaveTextContent('Active (snmp)'));
      expect(container.textContent).toContain('Maintenance');
      expect(container.textContent).not.toMatch(/maintenance|active/);
    } finally {
      await act(async () => {
        await i18n.changeLanguage('de-DE');
      });
    }
  });
});
