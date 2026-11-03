/**
 * The impact panel of the CI detail and the topology page (UI-14, IMP-08,
 * WP-818): services, clients and sites by name (the id where the name is
 * missing), the SPOF of the services (service_spofs) apart from the direct
 * dependents without an alternative and those protected by redundancy, the
 * counts of blast_radius and summary.by_category, certain and heuristic
 * hits apart (IMP-03); without the entitlement topology nothing.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import i18n from '../../i18n';
import { useAuthStore } from '../../auth/authStore';
import { renderWithProviders, stubFetchRoutes } from '../../test/utils';
import { ImpactPanel, type ImpactPanelData } from './ImpactPanel';

function page<T>(data: T[]) {
  return { data, total: data.length, limit: 200, offset: 0, has_more: false };
}

function entitlements(features: string[]) {
  return page(
    features.map((feature_key) => ({
      organization_id: 'org-1',
      feature_key,
      plan: 'enterprise',
      enabled: true,
      limits: {},
      source: 'manual',
    })),
  );
}

const routes = {
  '/entitlements': entitlements(['cmdb_core', 'topology']),
  // client-2 has no readable name: the panel shows its id.
  '/clients': page([
    { id: 'client-1', name: 'Stadtwerke Nord' },
    { id: 'client-2', name: '' },
  ]),
  '/sites': page([{ id: 'site-1', name: 'RZ Nord' }]),
};

// A PDU fails: the server and the ERP application on it are certainly
// affected, the switch only over connected_to; the server is the SPOF of
// the ERP service.
const impact: ImpactPanelData = {
  affected: [
    { id: 'ci-srv', name: 'srv-01', ci_type: 'server', heuristic: false },
    { id: 'ci-erp', name: 'erp', ci_type: 'application', heuristic: false },
    { id: 'ci-sw', name: 'core-sw-01', ci_type: 'switch', heuristic: true },
  ],
  affected_count: 2,
  heuristic_count: 1,
  affected_services: [
    { id: 'ci-erp', name: 'erp', ci_type: 'application', heuristic: false },
    { id: 'ci-mail', name: 'mail', ci_type: 'business_service', heuristic: true },
  ],
  client_ids: ['client-1', 'client-2', 'client-gone'],
  site_ids: ['site-1', 'site-2'],
  service_spofs: [{ id: 'ci-srv', name: 'srv-01', ci_type: 'server', service_ids: ['ci-erp'] }],
  spof_truncated: false,
  single_points_of_failure: [{ id: 'ci-vm', name: 'vm-01', ci_type: 'vm' }],
  protected_by_redundancy: [{ id: 'ci-db', name: 'db-01', ci_type: 'server' }],
  blast_radius: { cis: 2, services: 1, services_heuristic: 1, clients: 3, sites: 2, spof: 1 },
  summary: {
    failed: 1,
    impacted: 3,
    certain: 2,
    heuristic: 1,
    by_category: { power: 1, hosting: 1 },
  },
};

beforeEach(() => {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
});

afterEach(async () => {
  vi.unstubAllGlobals();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  await i18n.changeLanguage('de-DE');
});

/** The list that follows the heading with the given text. */
function listAfter(heading: string): HTMLElement {
  const list = screen.getByText(heading).parentElement?.querySelector('ul');
  if (!list) throw new Error(`no list after ${heading}`);
  return list;
}

function items(heading: string): string[] {
  return within(listAfter(heading))
    .getAllByRole('listitem')
    .map((item) => item.textContent ?? '');
}

describe('ImpactPanel', () => {
  it('lists the affected services, the heuristic ones marked', async () => {
    stubFetchRoutes(routes);
    renderWithProviders(<ImpactPanel impact={impact} />);

    expect(await screen.findByText('Betroffene Services:')).toBeInTheDocument();
    expect(items('Betroffene Services:')).toEqual([
      '• erp (application)',
      '• mail (business_service) Netzheuristik',
    ]);
    expect(screen.getByText(/^Services:/, { selector: 'span' })).toHaveTextContent(
      'Services: 1 (+1 über Netzheuristik)',
    );
  });

  it('names clients and sites, with the id where no name is readable', async () => {
    stubFetchRoutes(routes);
    renderWithProviders(<ImpactPanel impact={impact} />);

    await waitFor(() =>
      expect(items('Betroffene Kunden:')).toEqual([
        '• Stadtwerke Nord',
        '• client-2',
        '• client-gone',
      ]),
    );
    await waitFor(() => expect(items('Betroffene Standorte:')).toEqual(['• RZ Nord', '• site-2']));
  });

  it('shows the SPOF of the services apart from the direct and the redundant dependents', async () => {
    stubFetchRoutes(routes);
    renderWithProviders(<ImpactPanel impact={impact} />);

    const spof = await screen.findByText('Single Points of Failure der Services:');
    expect(items('Single Points of Failure der Services:')).toEqual([
      '• srv-01 (server) SPOF für erp',
    ]);
    expect(within(spof.parentElement as HTMLElement).queryByRole('note')).not.toBeInTheDocument();
    // single_points_of_failure are the direct dependents without an
    // alternative, not the SPOF of IMP-08.
    expect(items('Direkt betroffen ohne Alternative:')).toEqual(['• vm-01 (vm)']);
    expect(items('Durch Redundanz geschützt:')).toEqual(['• db-01 (server)']);
    expect(screen.getByText(/^SPOF:/)).toHaveTextContent('SPOF: 1');
  });

  it('says that the SPOF list is shortened (spof_truncated)', async () => {
    stubFetchRoutes(routes);
    renderWithProviders(<ImpactPanel impact={{ ...impact, spof_truncated: true }} />);

    const heading = await screen.findByText('Single Points of Failure der Services:');
    expect(within(heading.parentElement as HTMLElement).getByRole('note')).toHaveTextContent(
      /Die Liste ist gekürzt/,
    );
  });

  it('shows the counts of blast_radius and summary.by_category', async () => {
    stubFetchRoutes(routes);
    renderWithProviders(
      <ImpactPanel impact={{ ...impact, blast_radius: { ...impact.blast_radius!, cis: 7 } }} />,
    );

    expect(await screen.findByText(/^Betroffene CIs:/)).toHaveTextContent('Betroffene CIs: 7');
    expect(screen.getByText(/^Kunden:/)).toHaveTextContent('Kunden: 3');
    expect(screen.getByText(/^Standorte:/)).toHaveTextContent('Standorte: 2');
    expect(items('Betroffen je Beziehungskategorie:')).toEqual(['Strom: 1', 'Hosting: 1']);
  });

  it('lists certain and heuristic hits apart (IMP-03)', async () => {
    stubFetchRoutes(routes);
    renderWithProviders(<ImpactPanel impact={impact} typeLabel={(type) => `T:${type}`} />);

    expect(await screen.findByText('Betroffen:')).toBeInTheDocument();
    expect(items('Betroffen:')).toEqual(['• srv-01 (T:server)', '• erp (T:application)']);
    expect(items('Möglicherweise betroffen:')).toEqual(['• core-sw-01 (T:switch) Netzheuristik']);
    expect(screen.getByText(/^Netzheuristik:/)).toHaveTextContent('Netzheuristik: 1');
  });

  it('labels the blocks in English as well', async () => {
    await i18n.changeLanguage('en-US');
    stubFetchRoutes(routes);
    renderWithProviders(<ImpactPanel impact={{ ...impact, spof_truncated: true }} />);

    expect(await screen.findByText('Affected services:')).toBeInTheDocument();
    expect(screen.getByText('Single points of failure of the services:')).toBeInTheDocument();
    expect(screen.getByText('Directly affected without an alternative:')).toBeInTheDocument();
    expect(screen.getByText('Protected by redundancy:')).toBeInTheDocument();
    expect(screen.getByText('Affected per relationship category:')).toBeInTheDocument();
    expect(screen.getByText(/only the 500 candidates/)).toBeInTheDocument();
  });

  it('is not rendered without the entitlement topology', async () => {
    const fetchMock = stubFetchRoutes({
      ...routes,
      '/entitlements': entitlements(['cmdb_core']),
    });
    renderWithProviders(<ImpactPanel impact={impact} />);

    await waitFor(() =>
      expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('/entitlements'))).toBe(
        true,
      ),
    );
    await waitFor(() => expect(screen.queryByTestId('impact-panel')).not.toBeInTheDocument());
    expect(screen.queryByText('Betroffen:')).not.toBeInTheDocument();
  });
});
