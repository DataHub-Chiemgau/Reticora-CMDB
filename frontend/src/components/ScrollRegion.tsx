/**
 * ScrollRegion — the shared wrapper of a table that scrolls sideways on
 * narrow screens (WP-631, F-357). The container is a labelled region that
 * takes the focus, so keyboard users reach the columns on the right with the
 * arrow keys and screen readers announce what scrolls (WCAG 2.1.1,
 * axe scrollable-region-focusable).
 */
import type { ReactNode } from 'react';
import { cn } from './ui/utils';

export interface ScrollRegionProps {
  /** Accessible name of the region, usually the title of the table. */
  label: string;
  className?: string;
  children: ReactNode;
}

export function ScrollRegion({ label, className, children }: ScrollRegionProps) {
  return (
    <div
      role="region"
      aria-label={label}
      tabIndex={0}
      className={cn(
        'overflow-x-auto rounded focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40',
        className,
      )}
    >
      {children}
    </div>
  );
}
