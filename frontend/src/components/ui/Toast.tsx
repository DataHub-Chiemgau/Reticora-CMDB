import { CircleAlert, CircleCheck, Info, X } from 'lucide-react';
import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useToastStore } from '../../stores/toast';
import type { Toast } from '../../stores/toast';
import { cn } from './utils';

const variantClasses: Record<Toast['variant'], string> = {
  info: 'border-line-strong bg-raised text-fg',
  success:
    'border-green-300 bg-green-50 text-green-900 dark:border-green-500/40 dark:bg-green-950 dark:text-green-100',
  error:
    'border-red-300 bg-red-50 text-red-900 dark:border-red-500/40 dark:bg-red-950 dark:text-red-100',
};

const iconClasses: Record<Toast['variant'], string> = {
  info: 'text-accent',
  success: 'text-green-700 dark:text-green-300',
  error: 'text-red-700 dark:text-red-300',
};

const icons = { info: Info, success: CircleCheck, error: CircleAlert } as const;

function ToastItem({ toast }: { toast: Toast }) {
  const { t } = useTranslation();
  const dismiss = useToastStore((state) => state.dismiss);
  const Icon = icons[toast.variant];

  useEffect(() => {
    const timer = setTimeout(() => dismiss(toast.id), toast.durationMs);
    return () => clearTimeout(timer);
  }, [toast.id, toast.durationMs, dismiss]);

  return (
    <div
      className={cn(
        'pointer-events-auto flex animate-toast-in items-start gap-3 rounded-xl border px-4 py-3 shadow-lg',
        variantClasses[toast.variant],
      )}
    >
      <Icon
        aria-hidden="true"
        className={cn('mt-0.5 h-4 w-4 shrink-0', iconClasses[toast.variant])}
      />
      <p className="min-w-0 flex-1 text-sm">{toast.message}</p>
      {toast.actionLabel && toast.onAction ? (
        <button
          type="button"
          onClick={() => {
            toast.onAction?.();
            dismiss(toast.id);
          }}
          className="shrink-0 rounded-md px-1.5 text-sm font-semibold underline underline-offset-2 hover:no-underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
        >
          {toast.actionLabel}
        </button>
      ) : null}
      <button
        type="button"
        onClick={() => dismiss(toast.id)}
        aria-label={t('toast.dismiss')}
        className="-mr-1 shrink-0 rounded-md p-0.5 text-current opacity-70 hover:opacity-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
      >
        <X aria-hidden="true" className="h-4 w-4" />
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
