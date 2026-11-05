import type { ReactNode } from 'react';
import { cn } from './utils';

interface EmptyStateProps {
  title: string;
  description?: string;
  action?: ReactNode;
  /** A 20px icon that hints at what would be here. */
  icon?: ReactNode;
  className?: string;
}

/**
 * EmptyState explains why a view has no content and offers the next step,
 * instead of leaving the user in front of an empty table.
 */
export function EmptyState({ title, description, action, icon, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center gap-1.5 rounded-xl border border-dashed border-line-strong px-6 py-12 text-center',
        className,
      )}
    >
      {icon ? (
        <span
          aria-hidden="true"
          className="mb-2 inline-flex h-10 w-10 items-center justify-center rounded-lg border border-line bg-panel text-fg-muted [&_svg]:h-5 [&_svg]:w-5"
        >
          {icon}
        </span>
      ) : null}
      <p className="text-base font-semibold text-fg">{title}</p>
      {description ? <p className="max-w-md text-sm text-fg-muted">{description}</p> : null}
      {action ? <div className="mt-3">{action}</div> : null}
    </div>
  );
}
