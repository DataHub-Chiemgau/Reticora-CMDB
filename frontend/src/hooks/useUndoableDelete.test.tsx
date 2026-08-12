import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';
import type { ReactNode } from 'react';
import i18n from '../i18n';
import { UNDO_WINDOW_MS, useUndoableDelete } from './useUndoableDelete';
import { ToastViewport } from '../components/ui/Toast';

function Wrapper({ children }: { children: ReactNode }) {
  return <I18nextProvider i18n={i18n}>{children}</I18nextProvider>;
}

function Harness({
  deleteFn,
  restoreFn,
}: {
  deleteFn: () => Promise<unknown>;
  restoreFn: () => Promise<unknown>;
}) {
  const { scheduleDelete } = useUndoableDelete();
  return (
    <>
      <button
        type="button"
        onClick={() => scheduleDelete({ id: 'x', label: 'srv-01', deleteFn, restoreFn })}
      >
        delete
      </button>
      <ToastViewport />
    </>
  );
}

describe('useUndoableDelete', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('executes the delete only after the undo window expires', async () => {
    const deleteFn = vi.fn().mockResolvedValue(undefined);
    render(<Harness deleteFn={deleteFn} restoreFn={vi.fn()} />, { wrapper: Wrapper });

    fireEvent.click(screen.getByRole('button', { name: 'delete' }));
    expect(screen.getByText('„srv-01“ wird gelöscht')).toBeInTheDocument();
    expect(deleteFn).not.toHaveBeenCalled();

    await act(async () => {
      vi.advanceTimersByTime(UNDO_WINDOW_MS + 50);
    });
    expect(deleteFn).toHaveBeenCalledTimes(1);
  });

  it('cancels the pending delete when undo is pressed', async () => {
    const deleteFn = vi.fn().mockResolvedValue(undefined);
    const restoreFn = vi.fn().mockResolvedValue(undefined);
    render(<Harness deleteFn={deleteFn} restoreFn={restoreFn} />, { wrapper: Wrapper });

    fireEvent.click(screen.getByRole('button', { name: 'delete' }));
    fireEvent.click(screen.getByRole('button', { name: 'Rückgängig' }));

    await act(async () => {
      vi.advanceTimersByTime(UNDO_WINDOW_MS * 2);
    });
    expect(deleteFn).not.toHaveBeenCalled();
    expect(restoreFn).toHaveBeenCalledTimes(1);
  });
});
