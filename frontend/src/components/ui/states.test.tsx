import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { EmptyState } from './EmptyState';
import { ErrorState } from './ErrorState';
import { SkeletonList } from './Skeleton';

describe('EmptyState', () => {
  it('explains the empty view and renders the offered action', () => {
    render(
      <EmptyState
        title="Keine Racks vorhanden"
        description="Legen Sie ein Rack an."
        action={<button type="button">Rack anlegen</button>}
      />,
    );

    expect(screen.getByText('Keine Racks vorhanden')).toBeInTheDocument();
    expect(screen.getByText('Legen Sie ein Rack an.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Rack anlegen' })).toBeInTheDocument();
  });
});

describe('ErrorState', () => {
  it('announces the failure and retries on demand', () => {
    const onRetry = vi.fn();
    render(<ErrorState title="Fehlgeschlagen" retryLabel="Erneut versuchen" onRetry={onRetry} />);

    expect(screen.getByRole('alert')).toHaveTextContent('Fehlgeschlagen');
    fireEvent.click(screen.getByRole('button', { name: 'Erneut versuchen' }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it('omits the retry button when no handler is supplied', () => {
    render(<ErrorState title="Fehlgeschlagen" retryLabel="Erneut versuchen" />);

    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });
});

describe('SkeletonList', () => {
  it('marks the placeholder as busy and exposes the loading label', () => {
    render(<SkeletonList rows={4} label="Wird geladen…" />);

    const status = screen.getByRole('status');
    expect(status).toHaveAttribute('aria-busy', 'true');
    expect(status).toHaveTextContent('Wird geladen…');
  });
});
