import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { Panel } from './Panel';

describe('Panel', () => {
  it('is a section named by its heading', () => {
    render(
      <Panel title="Details">
        <p>Body</p>
      </Panel>,
    );
    const section = screen.getByRole('region', { name: 'Details' });
    expect(section.tagName).toBe('SECTION');
    expect(screen.getByRole('heading', { name: 'Details' })).toBeInTheDocument();
  });

  it('closes through a button with the given accessible name', () => {
    const onClose = vi.fn();
    render(
      <Panel title="Details" onClose={onClose} closeLabel="Close details">
        <p>Body</p>
      </Panel>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Close details' }));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('shows no close button without onClose', () => {
    render(<Panel title="Details">Body</Panel>);
    expect(screen.queryByRole('button')).toBeNull();
  });
});
