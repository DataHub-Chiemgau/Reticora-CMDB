/**
 * ScrollRegion — the shared wrapper of a table that scrolls sideways on
 * narrow screens (WP-631, F-357). The container is a labelled region that
 * takes the focus, so keyboard users reach the columns on the right with the
 * arrow keys and screen readers announce what scrolls (WCAG 2.1.1,
 * axe scrollable-region-focusable).
 *
 * The region is positioned (relative): it is the containing block of
 * absolutely positioned content such as the sr-only label of an actions
 * column. Without it that content is placed against an ancestor outside the
 * region, escapes its clipping and widens the page, so a phone zooms the
 * layout out and dialogs open partly outside the screen.
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
        'relative overflow-x-auto rounded focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40',
        className,
      )}
    >
      {children}
    </div>
  );
}
