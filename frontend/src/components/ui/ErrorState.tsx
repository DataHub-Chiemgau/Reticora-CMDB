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
        'flex flex-col items-start gap-2 rounded-2xl border border-red-200 bg-red-50 px-5 py-4 dark:border-red-900 dark:bg-red-950/40',
        className,
      )}
    >
      <p className="text-sm font-semibold text-red-800 dark:text-red-200">{title}</p>
      {description ? <p className="text-sm text-red-700 dark:text-red-300">{description}</p> : null}
      {onRetry && retryLabel ? (
        <Button variant="secondary" size="sm" onClick={onRetry}>
          {retryLabel}
        </Button>
      ) : null}
    </div>
  );
}
