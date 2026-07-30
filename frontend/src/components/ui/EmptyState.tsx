import type { ReactNode } from 'react';
import { cn } from './utils';

interface EmptyStateProps {
  title: string;
  description?: string;
  action?: ReactNode;
  className?: string;
}

/**
 * EmptyState explains why a view has no content and offers the next step,
 * instead of leaving the user in front of an empty table.
 */
export function EmptyState({ title, description, action, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center gap-2 rounded-2xl border border-dashed border-gray-300 px-6 py-12 text-center dark:border-gray-700',
        className,
      )}
    >
      <p className="text-base font-semibold text-gray-900 dark:text-gray-100">{title}</p>
      {description ? (
        <p className="max-w-md text-sm text-gray-600 dark:text-gray-300">{description}</p>
      ) : null}
      {action ? <div className="mt-2">{action}</div> : null}
    </div>
  );
}
