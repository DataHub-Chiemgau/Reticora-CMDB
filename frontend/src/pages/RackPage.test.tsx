import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import type { CI, RackMount } from '../api/client';
import { RackPage, buildRackUnits } from './RackPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

const mounts: RackMount[] = [
  {
    id: 'mount-1',
    organization_id: 'org-1',
    rack_id: 'rack-1',
    ci_id: 'ci-1',
    position_u: 10,
    height_u: 2,
    face: 'front',
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
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
];

const ci = {
  id: 'ci-1',
  organization_id: 'org-1',
  ci_type_id: 'server',
  name: 'srv-01',
  status: 'active',
  attributes: {},
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
} as CI;

const rack = {
  id: 'rack-1',
  organization_id: 'org-1',
  room_id: 'room-1',
  name: 'Rack A1',
  height_u: 42,
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
  it('labels mounts with the CI name and falls back to the CI id', () => {
    const units = buildRackUnits(mounts, new Map([[ci.id, ci]]));

    // default face is "front", so the rear-mounted unit is filtered out
    expect(units).toEqual([{ id: 'mount-1', position: 10, height: 2, label: 'srv-01' }]);
  });

  it('renders rear mounts when the rear face is selected', () => {
    const units = buildRackUnits(mounts, new Map([[ci.id, ci]]), 'rear');

    expect(units).toEqual([{ id: 'mount-2', position: 1, height: 1, label: 'ci-unknown' }]);
  });
});

describe('RackPage', () => {
  it('selects the first rack and renders its mounts', async () => {
    stubFetchRoutes({
      '/racks/rack-1/mounts': paginated(mounts),
      '/racks': paginated([rack]),
      '/cis/ci-1': ci,
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
      '/racks/rack-1/mounts': paginated(mounts),
      '/racks': paginated([rack]),
      '/cis/ci-1': ci,
    });

    renderWithProviders(<RackPage />, { route: '/racks' });

    const rearButton = await screen.findByRole('button', { name: 'Rückseite' });
    rearButton.click();

    expect(
      await screen.findByRole('heading', { name: 'Rack A1 · Rückseite' }),
    ).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'ci-unknown' })).toBeInTheDocument();
  });

  it('shows an empty state when no racks exist', async () => {
    stubFetchRoutes({ '/racks': paginated([]) });

    renderWithProviders(<RackPage />, { route: '/racks' });

    expect(await screen.findByText('Keine Racks vorhanden')).toBeInTheDocument();
  });
});
