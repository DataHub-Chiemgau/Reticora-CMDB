/**
 * Tests for the impact analysis of the CI detail page (IMP-03, F-098,
 * WP-491): the blast radius lists the certainly affected CIs and the CIs
 * reached only over connected_to (the network heuristic) apart, the latter
 * with the marker "Netzheuristik"; affected_count counts the certain hits
 * only (WP-490). Since WP-818 the section renders the ImpactPanel: the
 * services of WP-297 and the SPOF of the services, and it is not shown
 * without the entitlement topology.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../../i18n';
import { useAuthStore } from '../../auth/authStore';
import { renderWithProviders, stubFetchRoutes } from '../../test/utils';
import { ImpactSection } from './CIDetailSections';

const CI_ID = '11111111-1111-1111-1111-111111111111';

function node(id: string, name: string, ciType: string, heuristic: boolean) {
  return { id, name, ci_type: ciType, status: 'active', heuristic };
}

// A PDU fails: the server is powered by it and the VM is hosted on the
// server (certain); the switch is only connected_to the server (heuristic).
const blastRadius = {
  ci_id: CI_ID,
  affected_cis: [
    node('ci-srv', 'srv-01', 'server', false),
    node('ci-vm', 'vm-01', 'vm', false),
    node('ci-sw', 'core-sw-01', 'switch', true),
  ],
  affected_count: 2,
  heuristic_count: 1,
  client_ids: ['client-1'],
  site_ids: ['site-1'],
  single_points_of_failure: [],
};

afterEach(async () => {
  vi.unstubAllGlobals();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  vi.restoreAllMocks();
  await i18n.changeLanguage('de-DE');
});

async function computeBlastRadius(body: unknown) {
  const fetchMock = stubFetchRoutes({ [`/cis/${CI_ID}/blast-radius`]: body });
  renderWithProviders(<ImpactSection ciId={CI_ID} />);
  fireEvent.click(
    screen.getByRole('button', { name: /^(Blast Radius berechnen|Compute blast radius)$/ }),
  );
  return fetchMock;
}

/** The list that follows the heading with the given text. */
function listAfter(heading: string): HTMLElement {
  const list = screen.getByText(heading).parentElement?.querySelector('ul');
  if (!list) throw new Error(`no list after ${heading}`);
  return list;
}

describe('ImpactSection', () => {
  it('lists certain and heuristic hits apart and marks the heuristic ones (IMP-03)', async () => {
    const fetchMock = await computeBlastRadius(blastRadius);

    expect(await screen.findByText('Betroffen:')).toBeInTheDocument();
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain(`/cis/${CI_ID}/blast-radius`);

    // The counter shows the certain hits; the heuristic ones are counted apart.
    expect(screen.getByText(/Betroffene CIs:/)).toHaveTextContent('Betroffene CIs: 2');
    expect(screen.getByText(/^Netzheuristik:/)).toHaveTextContent('Netzheuristik: 1');

    const certain = listAfter('Betroffen:');
    expect(
      within(certain)
        .getAllByRole('listitem')
        .map((item) => item.textContent),
    ).toEqual(['• srv-01 (server)', '• vm-01 (vm)']);
    expect(within(certain).queryByText('Netzheuristik')).not.toBeInTheDocument();
    expect(within(certain).queryByText('core-sw-01', { exact: false })).not.toBeInTheDocument();

    const heuristic = listAfter('Möglicherweise betroffen:');
    const items = within(heuristic).getAllByRole('listitem');
    expect(items).toHaveLength(1);
    expect(items[0]).toHaveTextContent('core-sw-01 (switch)');
    expect(within(items[0]!).getByText('Netzheuristik')).toBeInTheDocument();
    expect(screen.getByText(/ungerichtete Netzverbindung \(connected_to\)/)).toBeInTheDocument();
  });

  it('says that no CI is certainly affected when only the heuristic reaches CIs', async () => {
    await computeBlastRadius({
      ...blastRadius,
      affected_cis: [node('ci-sw', 'core-sw-01', 'switch', true)],
      affected_count: 0,
      heuristic_count: 1,
      client_ids: [],
      site_ids: [],
    });

    expect(await screen.findByText('Keine sicher betroffenen CIs.')).toBeInTheDocument();
    expect(screen.getByText(/Betroffene CIs:/)).toHaveTextContent('Betroffene CIs: 0');
    const items = within(listAfter('Möglicherweise betroffen:')).getAllByRole('listitem');
    expect(items).toHaveLength(1);
    expect(within(items[0]!).getByText('Netzheuristik')).toBeInTheDocument();
  });

  it('shows no heuristic block without heuristic hits', async () => {
    await computeBlastRadius({
      ...blastRadius,
      affected_cis: [node('ci-app', 'app-01', 'application', false)],
      affected_count: 1,
      heuristic_count: 0,
    });

    expect(await screen.findByText('Betroffen:')).toBeInTheDocument();
    expect(
      within(listAfter('Betroffen:'))
        .getAllByRole('listitem')
        .map((item) => item.textContent),
    ).toEqual(['• app-01 (application)']);
    expect(screen.getByText(/^Netzheuristik:/)).toHaveTextContent('Netzheuristik: 0');
    expect(screen.queryByText('Möglicherweise betroffen:')).not.toBeInTheDocument();
    expect(screen.queryByText('Netzheuristik')).not.toBeInTheDocument();
  });

  it('marks the heuristic hits in English as well', async () => {
    await i18n.changeLanguage('en-US');
    await computeBlastRadius(blastRadius);

    expect(await screen.findByText('Affected:')).toBeInTheDocument();
    const items = within(listAfter('Possibly affected:')).getAllByRole('listitem');
    expect(items).toHaveLength(1);
    expect(within(items[0]!).getByText('Network heuristic')).toBeInTheDocument();
    expect(screen.getByText(/^Network heuristic:/)).toHaveTextContent('Network heuristic: 1');
  });

  it('shows the affected services and the SPOF of the services through the panel (WP-818)', async () => {
    await computeBlastRadius({
      ...blastRadius,
      affected_services: [node('ci-vm', 'vm-01', 'vm', false)],
      single_points_of_failure: [node('ci-srv', 'srv-01', 'server', false)],
      service_spofs: [{ id: 'ci-srv', name: 'srv-01', ci_type: 'server', service_ids: ['ci-vm'] }],
      spof_truncated: false,
      blast_radius: { cis: 2, services: 1, services_heuristic: 0, clients: 1, sites: 1, spof: 1 },
    });

    expect(await screen.findByText('Betroffene Services:')).toBeInTheDocument();
    expect(
      within(listAfter('Betroffene Services:'))
        .getAllByRole('listitem')
        .map((item) => item.textContent),
    ).toEqual(['• vm-01 (vm)']);
    expect(
      within(listAfter('Single Points of Failure der Services:')).getByText(/srv-01/),
    ).toBeInTheDocument();
    expect(
      within(listAfter('Direkt betroffen ohne Alternative:')).getByText(/srv-01/),
    ).toBeInTheDocument();
  });

  it('is not shown without the entitlement topology', async () => {
    useAuthStore.setState({
      token: 'header.payload.signature',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    stubFetchRoutes({
      '/entitlements': {
        data: [
          {
            organization_id: 'org-1',
            feature_key: 'cmdb_core',
            plan: 'enterprise',
            enabled: true,
            limits: {},
            source: 'manual',
          },
        ],
        total: 1,
        limit: 200,
        offset: 0,
        has_more: false,
      },
    });
    renderWithProviders(<ImpactSection ciId={CI_ID} />);

    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Blast Radius berechnen' }),
      ).not.toBeInTheDocument(),
    );
    expect(screen.queryByText('Auswirkungsanalyse')).not.toBeInTheDocument();
  });
});
