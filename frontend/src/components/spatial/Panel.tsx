import { X } from 'lucide-react';
import { useId } from 'react';
import type { ReactNode } from 'react';
import { IconButton } from '../ui/Button';
import { cn } from '../ui/utils';

interface PanelProps {
  /** The heading of the panel (an h3 below the page's h1). */
  title: ReactNode;
  /** One quiet line under the title. */
  description?: ReactNode;
  /** Icon or badge in front of the title. */
  leading?: ReactNode;
  /** Controls on the right of the header (badges, buttons). */
  actions?: ReactNode;
  /** Shows a close button in the header; it needs its accessible name. */
  onClose?: () => void;
  closeLabel?: string;
  /** Without body padding the content (a table, a list) runs to the edges. */
  padded?: boolean;
  className?: string;
  bodyClassName?: string;
  children?: ReactNode;
}

/**
 * Panel is the quiet side panel of the spatial views (details, impact,
 * objects, mounts): a hairline border on the panel colour, a header row with
 * the title and, when the panel can be dismissed, a close IconButton, and
 * the body below a hairline. It is a labelled section, so a reader can jump
 * to it as a landmark-like region.
 */
export function Panel({
  title,
  description,
  leading,
  actions,
  onClose,
  closeLabel,
  padded = true,
  className,
  bodyClassName,
  children,
}: PanelProps) {
  const headingId = useId();
  return (
    <section
      aria-labelledby={headingId}
      className={cn('min-w-0 rounded-xl border border-line bg-panel', className)}
    >
      <div
        className={cn(
          'flex items-center justify-between gap-3 px-4 py-2.5',
          children ? 'border-b border-line' : false,
        )}
      >
        <div className="flex min-w-0 items-center gap-2">
          {leading ? <span className="shrink-0 [&_svg]:h-4 [&_svg]:w-4">{leading}</span> : null}
          <div className="min-w-0">
            <h3 id={headingId} className="text-sm font-semibold text-fg [overflow-wrap:anywhere]">
              {title}
            </h3>
            {description ? <p className="text-xs text-fg-muted">{description}</p> : null}
          </div>
        </div>
        {actions || onClose ? (
          <div className="flex shrink-0 items-center gap-1.5">
            {actions}
            {onClose ? (
              <IconButton
                size="sm"
                label={closeLabel ?? ''}
                icon={<X />}
                onClick={onClose}
                className="-mr-1.5"
              />
            ) : null}
          </div>
        ) : null}
      </div>
      {children ? (
        <div className={cn(padded ? 'p-4' : false, bodyClassName)}>{children}</div>
      ) : null}
    </section>
  );
}
