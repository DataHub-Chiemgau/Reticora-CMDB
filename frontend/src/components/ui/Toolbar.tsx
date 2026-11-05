import type { ReactNode } from 'react';
import { cn } from './utils';

/**
 * Toolbar is the row above a list: search, filters and, pushed to the right
 * with ToolbarEnd, counters or secondary actions. It sits directly on the
 * page, not in a card: the filters belong to the list below them.
 */
export function Toolbar({
  children,
  className,
  label,
}: {
  children: ReactNode;
  className?: string;
  /** Names the group of controls for assistive technology. */
  label?: string;
}) {
  return (
    <div
      role={label ? 'group' : undefined}
      aria-label={label}
      className={cn('flex flex-wrap items-center gap-2', className)}
    >
      {children}
    </div>
  );
}

/** ToolbarEnd pushes its content to the right edge of the toolbar. */
export function ToolbarEnd({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('ml-auto flex items-center gap-2', className)}>{children}</div>;
}
