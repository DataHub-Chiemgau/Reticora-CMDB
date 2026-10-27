import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import type { Site } from '../api/client';
import { MapPage } from './MapPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

// WP-631 (F-356): the site markers of the map are buttons the keyboard
// reaches; they do not lie inside an element with role="img", and Enter and
// Space open the site details.

const NOW = '2026-10-01T00:00:00Z';

function site(id: string, name: string, lat: number | null, lon: number | null): Site {
  return {
    id,
    organization_id: 'org-1',
    name,
    address: `${name} Street 1`,
    geo_lat: lat,
    geo_lon: lon,
    created_at: NOW,
    updated_at: NOW,
  };
}

const BERLIN = site('site-1', 'Berlin HQ', 52.52, 13.405);
const SYDNEY = site('site-2', 'Sydney Office', -33.8688, 151.2093);
const NOWHERE = site('site-3', 'Remote Depot', null, null);

function renderMap() {
  stubFetchRoutes({
    '/sites': { data: [BERLIN, SYDNEY, NOWHERE], total: 3, limit: 200, offset: 0, has_more: false },
  });
  return renderWithProviders(<MapPage />, { route: '/map' });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('MapPage', () => {
  it('shows every site, also beyond the 200 of one page (F-362)', async () => {
    const sites = Array.from({ length: 230 }, (_, i) =>
      site(`site-${i}`, `Standort ${i}`, 48 + i / 100, 11 + i / 100),
    );
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        // The server caps the limit at 200 (parameter Limit, API-02).
        const limit = Math.min(Number(url.searchParams.get('limit') ?? 50), 200);
        const offset = Number(url.searchParams.get('offset') ?? 0);
        return new Response(
          JSON.stringify({
            data: sites.slice(offset, offset + limit),
            total: sites.length,
            limit,
            offset,
            has_more: offset + limit < sites.length,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        );
      }),
    );
    renderWithProviders(<MapPage />, { route: '/map' });

    const map = await screen.findByRole('group', { name: 'Weltkarte mit Standortmarkern' });
    expect(await within(map).findByRole('button', { name: 'Standort 229' })).toBeInTheDocument();
    expect(within(map).getAllByRole('button')).toHaveLength(230);
  });

  it('offers the markers as buttons in a labelled group, not inside an image', async () => {
    renderMap();

    const map = await screen.findByRole('group', { name: 'Weltkarte mit Standortmarkern' });
    const markers = within(map).getAllByRole('button');
    expect(markers.map((marker) => marker.getAttribute('aria-label'))).toEqual([
      'Berlin HQ',
      'Sydney Office',
    ]);
    for (const marker of markers) {
      expect(marker.closest('[role="img"]')).toBeNull();
      expect(marker).toHaveAttribute('tabindex', '0');
    }
    // A site without coordinates has no marker; it is listed beside the map.
    expect(within(map).queryByRole('button', { name: 'Remote Depot' })).toBeNull();
    expect(screen.getByText('Remote Depot')).toBeInTheDocument();
    expect(screen.queryByRole('img', { name: 'Weltkarte mit Standortmarkern' })).toBeNull();
  });

  it('reaches a marker with the keyboard and opens the site with Enter', async () => {
    renderMap();

    const marker = await screen.findByRole('button', { name: 'Berlin HQ' });
    marker.focus();
    expect(marker).toHaveFocus();
    expect(screen.queryByRole('heading', { name: 'Berlin HQ' })).toBeNull();

    fireEvent.keyDown(marker, { key: 'Enter' });

    expect(screen.getByRole('heading', { name: 'Berlin HQ' })).toBeInTheDocument();
    expect(screen.getByText('Berlin HQ Street 1')).toBeInTheDocument();
  });

  it('opens the site with Space without scrolling the page', async () => {
    renderMap();

    const marker = await screen.findByRole('button', { name: 'Sydney Office' });
    marker.focus();
    const notCancelled = fireEvent.keyDown(marker, { key: ' ' });

    expect(notCancelled).toBe(false);
    expect(screen.getByRole('heading', { name: 'Sydney Office' })).toBeInTheDocument();
  });

  it('ignores other keys and opens the site on a click', async () => {
    renderMap();

    const marker = await screen.findByRole('button', { name: 'Berlin HQ' });
    fireEvent.keyDown(marker, { key: 'a' });
    expect(screen.queryByRole('heading', { name: 'Berlin HQ' })).toBeNull();

    fireEvent.click(marker);
    expect(screen.getByRole('heading', { name: 'Berlin HQ' })).toBeInTheDocument();
  });
});
