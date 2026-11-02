/**
 * Tests for the impact analysis of the CI detail page (IMP-03, F-098,
 * WP-491): the blast radius lists the certainly affected CIs and the CIs
 * reached only over connected_to (the network heuristic) apart, the latter
 * with the marker "Netzheuristik"; affected_count counts the certain hits
 * only (WP-490).
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import i18n from '../../i18n';
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
});
