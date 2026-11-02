import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import type { RackLayoutMount } from '../api/client';
import { RackPage, buildRackUnits } from './RackPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

const mounts: RackLayoutMount[] = [
  {
    id: 'mount-1',
    organization_id: 'org-1',
    rack_id: 'rack-1',
    ci_id: 'ci-1',
    position_u: 10,
    height_u: 2,
    face: 'front',
    ci_name: 'srv-01',
    ci_type: 'server',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 'mount-2',
    organization_id: 'org-1',
    rack_id: 'rack-1',
    ci_id: 'ci-unknown',
    position_u: 1,
    height_u: 0,
    face: 'rear',
    ci_name: '',
    ci_type: '',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
];

const rack = {
  id: 'rack-1',
  organization_id: 'org-1',
  room_id: 'room-1',
  name: 'Rack A1',
  height_units: 42,
  width_mm: 600,
  depth_mm: 1000,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 100, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('buildRackUnits', () => {
  it('labels mounts with the CI name of the layout and falls back to the CI id', () => {
    const units = buildRackUnits(mounts);

    // default face is "front", so the rear-mounted unit is filtered out
    expect(units).toEqual([{ id: 'mount-1', position: 10, height: 2, label: 'srv-01' }]);
  });

  it('renders rear mounts when the rear face is selected', () => {
    const units = buildRackUnits(mounts, 'rear');

    expect(units).toEqual([{ id: 'mount-2', position: 1, height: 1, label: 'ci-unknown' }]);
  });

  // F-381 (WP-651): a mount on both faces (RCK-01) occupies its units on the
  // front and on the rear; the faces are named in the accessible title.
  it('draws a mount on both faces on the front and on the rear', () => {
    const both: RackLayoutMount = { ...mounts[0]!, id: 'mount-3', ci_id: 'ci-3', face: 'both' };
    for (const face of ['front', 'rear'] as const) {
      expect(buildRackUnits([both], face, 'Vorder- und Rückseite')).toEqual([
        {
          id: 'mount-3',
          position: 10,
          height: 2,
          label: 'srv-01',
          title: 'srv-01 · Vorder- und Rückseite',
        },
      ]);
    }
  });
});

describe('RackPage', () => {
  it('selects the first rack and renders its mounts', async () => {
    stubFetchRoutes({
      '/racks/rack-1/layout': { rack, mounts, free: { front: [], rear: [] } },
      '/racks': paginated([rack]),
    });

    renderWithProviders(<RackPage />, { route: '/racks' });

    expect(
      await screen.findByRole('heading', { name: 'Rack A1 · Vorderseite' }),
    ).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'srv-01' })).toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: 'Rack auswählen' })).toHaveValue('rack-1');
  });

  it('switches to the rear face and shows rear mounts', async () => {
    stubFetchRoutes({
      '/racks/rack-1/layout': { rack, mounts, free: { front: [], rear: [] } },
      '/racks': paginated([rack]),
    });

    renderWithProviders(<RackPage />, { route: '/racks' });

    const rearButton = await screen.findByRole('button', { name: 'Rückseite' });
    rearButton.click();

    expect(await screen.findByRole('heading', { name: 'Rack A1 · Rückseite' })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'ci-unknown' })).toBeInTheDocument();
  });

  // F-381 (WP-651): PUT /racks/{id}/mounts/{ci} with face "both" (RCK-01);
  // before the fix the mount was missing on both faces, in the drawing and
  // in the list.
  it('shows a mount on both faces on the front and on the rear (F-381)', async () => {
    const both: RackLayoutMount = {
      ...mounts[0]!,
      id: 'mount-both',
      ci_id: 'ci-both',
      ci_name: 'pdu-01',
      ci_type: 'pdu',
      position_u: 1,
      height_u: 1,
      face: 'both',
    };
    stubFetchRoutes({
      '/racks/rack-1/layout': { rack, mounts: [both], free: { front: [], rear: [] } },
      '/racks': paginated([rack]),
    });

    const { container } = renderWithProviders(<RackPage />, { route: '/racks' });

    for (const face of ['Vorderseite', 'Rückseite']) {
      await screen.findByRole('heading', { name: `Rack A1 · ${face}` });
      expect(await screen.findByRole('button', { name: 'pdu-01' })).toBeInTheDocument();
      expect(screen.queryByText('Keine Einbauten in diesem Rack')).not.toBeInTheDocument();
      expect(screen.getByText(/Position 1 · 1 HE · Vorder- und Rückseite/)).toBeInTheDocument();
      // The drawing occupies unit 1 with the mount and names both faces.
      const titles = [...container.querySelectorAll('svg g title')].map((t) => t.textContent);
      expect(titles).toEqual(['pdu-01 · Vorder- und Rückseite']);
      expect([...container.querySelectorAll('svg g text')].map((t) => t.textContent)).toContain(
        'pdu-01',
      );
      if (face === 'Vorderseite') {
        screen.getByRole('button', { name: 'Rückseite' }).click();
      }
    }
  });

  it('renders a rack with five mounts from the layout with at most two requests (F-252)', async () => {
    const five: RackLayoutMount[] = [1, 5, 9, 13, 17].map((position, index) => ({
      id: `mount-${index}`,
      organization_id: 'org-1',
      rack_id: 'rack-1',
      ci_id: `ci-${index}`,
      position_u: position,
      height_u: 2,
      face: 'front',
      ci_name: `srv-${index}`,
      ci_type: 'server',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    }));
    const fetchMock = stubFetchRoutes({
      '/racks/rack-1/layout': { rack, mounts: five, free: { front: [], rear: [] } },
      '/racks': paginated([rack]),
    });

    renderWithProviders(<RackPage />, { route: '/racks' });

    for (const mount of five) {
      expect(await screen.findByRole('button', { name: mount.ci_name })).toBeInTheDocument();
    }
    expect(screen.getAllByText('server')).toHaveLength(5);
    const urls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(urls.filter((url) => url.includes('/cis/'))).toEqual([]);
    expect(urls.length).toBeLessThanOrEqual(2);
  });

  it('shows an empty state when no racks exist', async () => {
    stubFetchRoutes({ '/racks': paginated([]) });

    renderWithProviders(<RackPage />, { route: '/racks' });

    expect(await screen.findByText('Keine Racks vorhanden')).toBeInTheDocument();
  });
});
