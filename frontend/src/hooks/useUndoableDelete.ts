import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useToastStore } from '../stores/toast';

export const UNDO_WINDOW_MS = 6000;

interface PendingDelete {
  label: string;
  deleteFn: () => Promise<unknown>;
  restoreFn: () => Promise<unknown>;
  timer: ReturnType<typeof setTimeout>;
  toastId: number;
}

/**
 * Buffers destructive deletes behind an undo window: the toast offers
 * "Undo" for UNDO_WINDOW_MS; only when the window expires is the delete
 * request actually sent. Pending deletes are flushed when the component
 * unmounts so nothing is silently dropped.
 */
export function useUndoableDelete() {
  const { t } = useTranslation();
  const push = useToastStore((state) => state.push);
  const dismiss = useToastStore((state) => state.dismiss);
  const pendingRef = useRef(new Map<string, PendingDelete>());

  function flush(id: string) {
    const pending = pendingRef.current.get(id);
    if (!pending) {
      return;
    }
    pendingRef.current.delete(id);
    clearTimeout(pending.timer);
    dismiss(pending.toastId);
    pending.deleteFn().catch(() => {
      push({
        message: t('toast.deleteFailed', { label: pending.label }),
        variant: 'error',
      });
    });
  }

  useEffect(() => {
    const pending = pendingRef.current;
    return () => {
      pending.forEach((entry) => {
        clearTimeout(entry.timer);
        entry.deleteFn().catch(() => undefined);
      });
      pending.clear();
    };
  }, []);

  function scheduleDelete(entry: {
    id: string;
    label: string;
    deleteFn: () => Promise<unknown>;
    restoreFn: () => Promise<unknown>;
  }) {
    const { id, label, deleteFn, restoreFn } = entry;
    const timer = setTimeout(() => flush(id), UNDO_WINDOW_MS);
    const toastId = push({
      message: t('toast.deletedPending', { label }),
      actionLabel: t('toast.undo'),
      durationMs: UNDO_WINDOW_MS,
      onAction: () => {
        const pending = pendingRef.current.get(id);
        if (!pending) {
          return;
        }
        pendingRef.current.delete(id);
        clearTimeout(pending.timer);
        pending.restoreFn().catch(() => {
          push({
            message: t('toast.restoreFailed', { label: pending.label }),
            variant: 'error',
          });
        });
      },
    });
    pendingRef.current.set(id, { label, deleteFn, restoreFn, timer, toastId });
  }

  return { scheduleDelete };
}
