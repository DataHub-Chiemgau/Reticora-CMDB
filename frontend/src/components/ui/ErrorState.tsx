import { TriangleAlert } from 'lucide-react';
import { Button } from './Button';
import { cn } from './utils';

interface ErrorStateProps {
  title: string;
  description?: string;
  retryLabel?: string;
  onRetry?: () => void;
  className?: string;
}

/**
 * ErrorState reports a failed request in place, keeps the surrounding page
 * usable and offers a retry so a transient failure is not a dead end.
 */
export function ErrorState({
  title,
  description,
  retryLabel,
  onRetry,
  className,
}: ErrorStateProps) {
  return (
    <div
      role="alert"
      className={cn(
        'flex items-start gap-3 rounded-xl border border-red-200 bg-red-50 px-4 py-3.5 dark:border-red-500/30 dark:bg-red-500/10',
        className,
      )}
    >
      <TriangleAlert
        aria-hidden="true"
        className="mt-0.5 h-4 w-4 shrink-0 text-red-700 dark:text-red-300"
      />
      <div className="flex min-w-0 flex-col items-start gap-1">
        <p className="text-sm font-semibold text-red-900 dark:text-red-100">{title}</p>
        {description ? (
          <p className="text-sm text-red-800 dark:text-red-200">{description}</p>
        ) : null}
        {onRetry && retryLabel ? (
          <Button variant="secondary" size="sm" className="mt-2" onClick={onRetry}>
            {retryLabel}
          </Button>
        ) : null}
      </div>
    </div>
  );
}
