import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useToastStore } from '../stores/toast';
import { UNDO_WINDOW_MS } from './useUndoableDelete';

interface PendingSubmit {
  submitFn: () => Promise<unknown>;
  failedMessage: (error: unknown) => string;
  timer: ReturnType<typeof setTimeout>;
  toastId: number;
}

export interface UndoableSubmit {
  /** id names the request; a waiting request with the same id is sent before the new one waits. */
  id: string;
  /** message of the toast while the request waits, e.g. "„Laptop 7“ wird zugewiesen". */
  message: string;
  /** submitFn sends the request; it runs once the undo window expired. */
  submitFn: () => Promise<unknown>;
  /** failedMessage is the text of the error toast when the request fails. */
  failedMessage: (error: unknown) => string;
}

/**
 * useUndoableSubmit sends a write behind an undo window (UI-15, WP-318),
 * like useUndoableDelete does for deletes: the toast offers "Undo" for
 * UNDO_WINDOW_MS, and only when the window expires does the request go
 * out. "Undo" drops the request without a server call. A pending request
 * is sent at once when the component unmounts (the user leaves the page)
 * or the page is hidden, so nothing is silently dropped. A failure of the
 * delayed request is reported as an error toast. Once sent, a write is
 * corrected only by a counter-booking (Convention 14).
 */
export function useUndoableSubmit() {
  const { t } = useTranslation();
  const push = useToastStore((state) => state.push);
  const dismiss = useToastStore((state) => state.dismiss);
  const pendingRef = useRef(new Map<string, PendingSubmit>());

  function send(entry: PendingSubmit) {
    entry.submitFn().catch((error: unknown) => {
      push({ message: entry.failedMessage(error), variant: 'error' });
    });
  }

  function flush(id: string) {
    const pending = pendingRef.current.get(id);
    if (!pending) return;
    pendingRef.current.delete(id);
    clearTimeout(pending.timer);
    dismiss(pending.toastId);
    send(pending);
  }

  // Unmount and a hidden page send what is still waiting. The latest send
  // and dismiss are reached through a ref, the effect itself runs once.
  const flushAllRef = useRef<() => void>(() => undefined);
  flushAllRef.current = () => {
    for (const id of [...pendingRef.current.keys()]) flush(id);
  };
  useEffect(() => {
    const onHide = () => flushAllRef.current();
    window.addEventListener('pagehide', onHide);
    return () => {
      window.removeEventListener('pagehide', onHide);
      flushAllRef.current();
    };
  }, []);

  function scheduleSubmit({ id, message, submitFn, failedMessage }: UndoableSubmit) {
    // A request with the same id still waiting goes out first.
    flush(id);
    const timer = setTimeout(() => flush(id), UNDO_WINDOW_MS);
    const toastId = push({
      message,
      actionLabel: t('toast.undo'),
      durationMs: UNDO_WINDOW_MS,
      onAction: () => {
        const pending = pendingRef.current.get(id);
        if (!pending) return;
        pendingRef.current.delete(id);
        clearTimeout(pending.timer);
      },
    });
    pendingRef.current.set(id, { submitFn, failedMessage, timer, toastId });
  }

  return { scheduleSubmit };
}
