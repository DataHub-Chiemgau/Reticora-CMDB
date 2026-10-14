/**
 * Tests for the provenance tooltip (REC-10): source, rank, time and the
 * selection reason of a field, on hover and on keyboard focus; the
 * provenance section lists every typed field the API answers.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { ProvenanceTooltip, type CIFieldProvenance } from './ProvenanceTooltip';
import { ProvenanceSection } from './CIDetailSections';

const observed: CIFieldProvenance = {
  field: 'firmware_version',
  value: '17.9',
  source: 'snmp',
  rank: 80,
  reason: 'source_rank',
  recorded_at: '2026-10-14T08:00:00Z',
  observed_at: '2026-10-14T07:55:00Z',
  diverged: false,
};
const overridden: CIFieldProvenance = {
  field: 'model',
  value: 'C9300-48P-E',
  source: 'manual_override',
  rank: 100,
  reason: 'override',
  recorded_at: '2026-10-14T09:00:00Z',
  override_author: 'anna',
  override_reason: 'Typenschild abgelesen',
  protected: true,
  discovered_value: 'C9300-48P',
  discovered_source: 'redfish',
  diverged: true,
};
const record: CIFieldProvenance = {
  field: 'name',
  value: 'sw-core-01',
  source: 'manual',
  rank: 100,
  reason: 'record',
  recorded_at: '2026-10-01T08:00:00Z',
  diverged: false,
};

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('ProvenanceTooltip', () => {
  it('shows source, rank, times and reason of an observed value on focus', () => {
    renderWithProviders(<ProvenanceTooltip provenance={observed} />);
    const trigger = screen.getByRole('button', { name: 'Herkunft von firmware_version' });
    expect(screen.queryByRole('tooltip')).toBeNull();

    fireEvent.focus(trigger);
    const tooltip = screen.getByRole('tooltip');
    expect(trigger).toHaveAttribute('aria-describedby', tooltip.id);
    expect(within(tooltip).getByText('Höchstrangige Quelle')).toBeInTheDocument();
    expect(within(tooltip).getByText('snmp')).toBeInTheDocument();
    expect(within(tooltip).getByText('80')).toBeInTheDocument();
    expect(
      within(tooltip).getByText(new Date(observed.recorded_at!).toLocaleString('de-DE')),
    ).toBeInTheDocument();
    expect(
      within(tooltip).getByText(new Date(observed.observed_at!).toLocaleString('de-DE')),
    ).toBeInTheDocument();

    fireEvent.keyDown(trigger, { key: 'Escape' });
    expect(screen.queryByRole('tooltip')).toBeNull();
  });

  it('names author and reason of an override and the value it covers', () => {
    renderWithProviders(<ProvenanceTooltip provenance={overridden} />);
    fireEvent.mouseEnter(screen.getByRole('button').parentElement!);
    const tooltip = screen.getByRole('tooltip');
    expect(within(tooltip).getByText('Manueller Override')).toBeInTheDocument();
    expect(within(tooltip).getByText('Typenschild abgelesen')).toBeInTheDocument();
    expect(within(tooltip).getByText('anna')).toBeInTheDocument();
    expect(within(tooltip).getByText('C9300-48P (redfish)')).toBeInTheDocument();
  });

  it('marks a value of the CI record', () => {
    renderWithProviders(<ProvenanceTooltip provenance={record} />);
    fireEvent.focus(screen.getByRole('button'));
    expect(screen.getByText('Wert des CI-Datensatzes')).toBeInTheDocument();
  });
});

describe('ProvenanceSection', () => {
  it('lists every typed field of the provenance API with a tooltip', async () => {
    const requested: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      requested.push(String(input));
      return new Response(JSON.stringify({ data: [overridden, observed, record], total: 3 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<ProvenanceSection ciId="ci-1" />);

    expect(
      await screen.findByRole('button', { name: 'Herkunft von firmware_version' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Herkunft von name' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Herkunft von model' })).toBeInTheDocument();
    expect(requested).toEqual(['/api/v1/cis/ci-1/provenance']);
    // Only the overridden field can be cleared.
    expect(screen.getAllByRole('button', { name: 'Override aufheben' })).toHaveLength(1);
  });
});
