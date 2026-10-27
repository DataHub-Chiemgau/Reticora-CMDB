// GLO-10/UI-18 (WP-525, F-174): the asset list shows status and category
// through the i18n catalog ("Auf Lager", not the stored key in_stock).
import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import i18n from '../i18n';
import { AssetListPage } from './AssetListPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

const asset = {
  id: 'asset-1',
  organization_id: 'org-1',
  asset_tag: 'INV-1',
  name: 'Laptop 1',
  category: 'license',
  status: 'in_stock',
  custom_fields: {},
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

afterEach(async () => {
  vi.unstubAllGlobals();
  await i18n.changeLanguage('de-DE');
});

async function assetRow() {
  stubFetchRoutes({
    '/assets': { data: [asset], total: 1, limit: 50, offset: 0, has_more: false },
  });
  renderWithProviders(<AssetListPage />, { route: '/assets' });
  return screen.findByRole('row', { name: /INV-1/ });
}

describe('AssetListPage', () => {
  it('shows status and category labels from the catalog in de', async () => {
    await i18n.changeLanguage('de-DE');
    const row = await assetRow();
    expect(within(row).getByText('Auf Lager')).toBeInTheDocument();
    expect(within(row).getByText('Lizenz')).toBeInTheDocument();
    expect(within(row).queryByText('in_stock')).not.toBeInTheDocument();
    expect(within(row).queryByText('license')).not.toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Auf Lager' })).toBeInTheDocument();
  });

  it('shows the en labels after a language switch', async () => {
    await i18n.changeLanguage('en-US');
    const row = await assetRow();
    expect(within(row).getByText('In stock')).toBeInTheDocument();
    expect(within(row).getByText('License')).toBeInTheDocument();
    expect(within(row).queryByText('in_stock')).not.toBeInTheDocument();
  });

  it('falls back to the stored value for an unknown status', async () => {
    await i18n.changeLanguage('de-DE');
    stubFetchRoutes({
      '/assets': {
        data: [{ ...asset, status: 'quarantined' }],
        total: 1,
        limit: 50,
        offset: 0,
        has_more: false,
      },
    });
    renderWithProviders(<AssetListPage />, { route: '/assets' });
    const row = await screen.findByRole('row', { name: /INV-1/ });
    expect(within(row).getByText('quarantined')).toBeInTheDocument();
  });
});
