import { cn } from './utils';

/**
 * Skeleton renders a neutral placeholder block while data is loading.
 * Perceived performance: the layout is reserved before the data arrives so the
 * page does not jump once the response is rendered.
 */
export function Skeleton({ className }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn('block animate-pulse rounded-lg bg-gray-200 dark:bg-gray-800', className)}
    />
  );
}

export function SkeletonList({
  rows = 3,
  label,
  className,
}: {
  rows?: number;
  label?: string;
  className?: string;
}) {
  return (
    <div className={cn('space-y-2', className)} role="status" aria-busy="true">
      {label ? <span className="sr-only">{label}</span> : null}
      {Array.from({ length: rows }, (_, index) => (
        <Skeleton key={index} className="h-10 w-full" />
      ))}
    </div>
  );
}
