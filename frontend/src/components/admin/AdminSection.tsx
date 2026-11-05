import { useId, type ReactNode } from 'react';
import { cn } from '../ui/utils';

interface AdminSectionProps {
  /** The short heading of the section (an h2). */
  title: ReactNode;
  /** One sentence on what the section is for. */
  description?: ReactNode;
  /** Controls beside the heading, e.g. a secondary action. */
  actions?: ReactNode;
  className?: string;
  children: ReactNode;
}

/**
 * AdminSection is the block of a settings-like administration page: a short
 * heading, one sentence of context and the content (a form in a Card, or a
 * table in a Card p-0). The heading names the region for assistive technology.
 */
export function AdminSection({
  title,
  description,
  actions,
  className,
  children,
}: AdminSectionProps) {
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} className={cn('space-y-3', className)}>
      <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-2">
        <div className="min-w-0">
          <h2 id={headingId} className="text-base font-semibold text-fg [overflow-wrap:anywhere]">
            {title}
          </h2>
          {description ? (
            <p className="mt-0.5 max-w-[68ch] text-sm text-fg-muted">{description}</p>
          ) : null}
        </div>
        {actions ? <div className="shrink-0">{actions}</div> : null}
      </div>
      {children}
    </section>
  );
}
