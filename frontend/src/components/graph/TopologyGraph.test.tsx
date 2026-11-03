/**
 * Without WebGL the Sigma renderer throws: the graph shows a note instead
 * of taking the page down. The SPOF of the affected services get a mark of
 * their own in the nodeReducer (IMP-08, WP-818).
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';

type Reducer = (node: string, data: Record<string, unknown>) => Record<string, unknown>;

const sigma = vi.hoisted(() => ({
  fail: true,
  settings: new Map<string, unknown>(),
}));

vi.mock('sigma', () => ({
  default: class {
    constructor() {
      if (sigma.fail) throw new Error('WebGL not supported');
    }
    setSetting(key: string, value: unknown) {
      sigma.settings.set(key, value);
    }
    kill() {}
  },
}));

const { TopologyGraph, SPOF_COLOR } = await import('./TopologyGraph');

afterEach(() => {
  sigma.fail = true;
  sigma.settings.clear();
});

describe('TopologyGraph', () => {
  it('shows a note when the renderer cannot start', async () => {
    renderWithProviders(<TopologyGraph nodes={[{ id: 'a', label: 'core-sw-01' }]} edges={[]} />);
    expect(await screen.findByRole('status')).toHaveTextContent(/braucht WebGL/);
  });

  it('marks the SPOF nodes in the nodeReducer, above the dimming', () => {
    sigma.fail = false;
    renderWithProviders(
      <TopologyGraph
        nodes={[
          { id: 'pdu', label: 'pdu-01' },
          { id: 'srv', label: 'srv-01' },
          { id: 'vm', label: 'vm-01' },
        ]}
        edges={[]}
        highlightedNodes={new Set(['pdu'])}
        dimmedNodes={new Set(['srv', 'vm'])}
        spofNodes={new Set(['srv'])}
      />,
    );

    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    const reducer = sigma.settings.get('nodeReducer') as Reducer;
    expect(reducer).toBeTypeOf('function');
    const base = { size: 10, color: '#16a34a', label: 'x' };
    const spof = reducer('srv', base);
    expect(spof.color).toBe(SPOF_COLOR);
    expect(spof.size).toBeGreaterThan(10);
    expect(spof.forceLabel).toBe(true);
    // The dimmed node that is no SPOF stays grey, the failed one red.
    expect(reducer('vm', base).color).toBe('#d1d5db');
    expect(reducer('pdu', base).color).toBe('#dc2626');
  });

  it('sets the nodeReducer for SPOF nodes alone', () => {
    sigma.fail = false;
    renderWithProviders(
      <TopologyGraph
        nodes={[{ id: 'srv', label: 'srv-01' }]}
        edges={[]}
        spofNodes={new Set(['srv'])}
      />,
    );
    const reducer = sigma.settings.get('nodeReducer') as Reducer;
    expect(reducer('srv', { size: 10 }).color).toBe(SPOF_COLOR);
  });
});
