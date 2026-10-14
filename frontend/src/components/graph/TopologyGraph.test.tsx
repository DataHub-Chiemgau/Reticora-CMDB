/**
 * Without WebGL the Sigma renderer throws: the graph shows a note instead
 * of taking the page down.
 */
import { describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';

vi.mock('sigma', () => ({
  default: class {
    constructor() {
      throw new Error('WebGL not supported');
    }
  },
}));

const { TopologyGraph } = await import('./TopologyGraph');

describe('TopologyGraph', () => {
  it('shows a note when the renderer cannot start', async () => {
    renderWithProviders(<TopologyGraph nodes={[{ id: 'a', label: 'core-sw-01' }]} edges={[]} />);
    expect(await screen.findByRole('status')).toHaveTextContent(/braucht WebGL/);
  });
});
