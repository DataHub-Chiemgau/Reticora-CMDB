/**
 * Regression tests for the inherited parent-asset surface.
 *
 * The backend rejects writes to parent-owned inventory fields on a child, but
 * until now nothing ever *read* them back: `composition.ParentOf` was exposed
 * by no route and the CI detail page showed none of the parent's data. A child
 * CI was therefore unreadable as part of its composition. These tests pin the
 * read path and the read-only presentation.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { CIParentAssetCard } from './CIParentAssetCard';

const CI_ID = '11111111-1111-1111-1111-111111111111';
const ASSET_ID = '22222222-2222-2222-2222-222222222222';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const parentPayload = {
  composition: {
    id: 'comp-1',
    parent_asset_id: ASSET_ID,
    child_ci_id: CI_ID,
    role: 'system',
    configuration_only: true,
    independently_serialized: false,
    independently_assignable: false,
    independently_locatable: false,
    independently_lifecycle_managed: false,
  },
  parent_asset: {
    id: ASSET_ID,
    name: 'Server Chassis ABC123',
    asset_tag: 'AST-CHASSIS-1',
    serial_number: 'SN-ABC123',
    warranty_end: '2030-01-01',
    location: 'DC1/Rack4',
    purchase_cost: 4200.5,
    currency: 'EUR',
  },
  inherited_fields: ['serial_number', 'warranty_end', 'location'],
};

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('CIParentAssetCard', () => {
  it('renders the inventory data inherited from the parent asset', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json(parentPayload)),
    );

    renderWithProviders(<CIParentAssetCard ciId={CI_ID} />);

    expect(await screen.findByText('Server Chassis ABC123')).toBeInTheDocument();
    // Values owned by the parent must be visible on the child.
    expect(screen.getByText('SN-ABC123')).toBeInTheDocument();
    expect(screen.getByText('2030-01-01')).toBeInTheDocument();
    expect(screen.getByText('DC1/Rack4')).toBeInTheDocument();
    // Money is shown with its currency rather than as a bare number.
    expect(screen.getByText('4200.5 EUR')).toBeInTheDocument();
  });

  it('marks the inherited block read-only and explains where the data comes from', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json(parentPayload)),
    );

    renderWithProviders(<CIParentAssetCard ciId={CI_ID} />);

    await screen.findByText('Server Chassis ABC123');
    expect(screen.getByText(/schreibgeschützt|read-only/i)).toBeInTheDocument();
    expect(screen.getByText(/parent-asset|parent asset/i)).toBeInTheDocument();
    // Read-only means no edit affordance at all.
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('renders nothing for a standalone CI so the simple workflow stays simple', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockImplementation(async () =>
          json({ title: 'Not Found', detail: 'CI has no parent asset', status: 404 }, 404),
        ),
    );

    const { container } = renderWithProviders(<CIParentAssetCard ciId={CI_ID} />);

    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });

  it('renders nothing when the payload has no composition link', async () => {
    // The card is mounted on the CI detail page, so an unexpected body must
    // degrade to "no parent" rather than crashing the whole page.
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json({ unexpected: true })),
    );

    const { container } = renderWithProviders(<CIParentAssetCard ciId={CI_ID} />);

    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });

  it('still identifies the parent when its asset record cannot be read', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json({ composition: parentPayload.composition })),
    );

    renderWithProviders(<CIParentAssetCard ciId={CI_ID} />);

    // The link is still meaningful even without the projection.
    expect(await screen.findByText(ASSET_ID)).toBeInTheDocument();
  });
});
