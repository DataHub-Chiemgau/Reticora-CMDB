import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useToastStore } from '../../stores/toast';
import type { Toast } from '../../stores/toast';
import { cn } from './utils';

const variantClasses: Record<Toast['variant'], string> = {
  info: 'border-gray-200 bg-white text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100',
  success:
    'border-green-300 bg-green-50 text-green-900 dark:border-green-800 dark:bg-green-950 dark:text-green-100',
  error:
    'border-red-300 bg-red-50 text-red-900 dark:border-red-800 dark:bg-red-950 dark:text-red-100',
};

function ToastItem({ toast }: { toast: Toast }) {
  const { t } = useTranslation();
  const dismiss = useToastStore((state) => state.dismiss);

  useEffect(() => {
    const timer = setTimeout(() => dismiss(toast.id), toast.durationMs);
    return () => clearTimeout(timer);
  }, [toast.id, toast.durationMs, dismiss]);

  return (
    <div
      className={cn(
        'pointer-events-auto flex items-center gap-3 rounded-xl border px-4 py-3 shadow-lg',
        variantClasses[toast.variant],
      )}
    >
      <p className="text-sm">{toast.message}</p>
      {toast.actionLabel && toast.onAction ? (
        <button
          type="button"
          onClick={() => {
            toast.onAction?.();
            dismiss(toast.id);
          }}
          className="shrink-0 rounded-lg px-2 py-1 text-sm font-semibold text-primary underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
        >
          {toast.actionLabel}
        </button>
      ) : null}
      <button
        type="button"
        onClick={() => dismiss(toast.id)}
        aria-label={t('toast.dismiss')}
        className="shrink-0 rounded-lg p-1 text-current opacity-70 hover:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
      >
        <svg
          viewBox="0 0 24 24"
          className="h-4 w-4"
          aria-hidden="true"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
        >
          <path d="M18 6 6 18" />
          <path d="m6 6 12 12" />
        </svg>
      </button>
    </div>
  );
}

export function ToastViewport() {
  const toasts = useToastStore((state) => state.toasts);

  if (toasts.length === 0) {
    return null;
  }

  return (
    <div
      aria-live="polite"
      role="status"
      className="pointer-events-none fixed bottom-4 right-4 z-[60] flex w-[min(92vw,24rem)] flex-col gap-2"
    >
      {toasts.map((toast) => (
        <ToastItem key={toast.id} toast={toast} />
      ))}
    </div>
  );
}
