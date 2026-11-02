/**
 * Tests for the metric series of the CI detail page (MON-02).
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { CIMetricsSection, sparklinePath } from './CIDetailSections';

const CI_ID = '11111111-1111-1111-1111-111111111111';

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

describe('sparklinePath', () => {
  it('scales the series into the box', () => {
    const path = sparklinePath(
      [
        { timestamp: '2026-10-14T10:00:00Z', value: 0 },
        { timestamp: '2026-10-14T10:15:00Z', value: 10 },
      ],
      100,
      20,
    );
    expect(path).toBe('M0.0,20.0 L100.0,0.0');
    expect(sparklinePath([], 100, 20)).toBe('');
  });
});

describe('CIMetricsSection', () => {
  it('loads the series of the CI on demand and draws one line each', async () => {
    const fetchMock = vi.fn().mockImplementation(async () =>
      json({
        ci_id: CI_ID,
        from: '2026-10-13T10:00:00Z',
        series: [
          {
            name: 'ups_battery_charge_percent',
            points: [
              { timestamp: '2026-10-14T10:00:00Z', value: 95 },
              { timestamp: '2026-10-14T10:15:00Z', value: 97 },
            ],
          },
        ],
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIMetricsSection ciId={CI_ID} />);
    expect(fetchMock).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: /metriken laden|load metrics/i }));

    expect(await screen.findByRole('img', { name: 'ups_battery_charge_percent' })).toBeTruthy();
    expect(screen.getByText('97')).toBeTruthy();
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(`/api/v1/cis/${CI_ID}/metrics?step=15m`);
  });

  it('says so when monitoring is not enabled', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockImplementation(async () =>
          json({ title: 'Forbidden', status: 403, detail: 'feature monitoring' }, 403),
        ),
    );

    renderWithProviders(<CIMetricsSection ciId={CI_ID} />);
    fireEvent.click(screen.getByRole('button', { name: /metriken laden|load metrics/i }));

    await waitFor(() => expect(screen.getByText(/nicht freigeschaltet|not enabled/i)).toBeTruthy());
  });

  it('shows the error instead of a line for a CI the API does not find (F-167)', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(async () =>
        json({ title: 'Not Found', status: 404, detail: 'ci not found' }, 404),
      );
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIMetricsSection ciId={CI_ID} />);
    fireEvent.click(screen.getByRole('button', { name: /metriken laden|load metrics/i }));

    expect(await screen.findByText('ci not found')).toBeTruthy();
    expect(screen.queryByRole('img')).toBeNull();
    expect(screen.queryByText(/keine messwerte|no samples/i)).toBeNull();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
