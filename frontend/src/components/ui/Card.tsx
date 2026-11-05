import type { ReactNode } from 'react';
import { cn } from './utils';

interface CardProps {
  children: ReactNode;
  className?: string;
  title?: ReactNode;
  actions?: ReactNode;
}

/**
 * Card is the one panel of the system: a hairline border on the panel
 * colour, no shadow (shadows are for floating layers). Tables and lists put
 * `p-0 overflow-hidden` on it and bring their own padding.
 */
export function Card({ children, className, title, actions }: CardProps) {
  return (
    <section className={cn('rounded-xl border border-line bg-panel p-5', className)}>
      {title || actions ? (
        <div className="mb-4 flex items-start justify-between gap-4">
          <div className="min-w-0">
            {title ? (
              <h3 className="text-base font-semibold text-fg [overflow-wrap:anywhere]">{title}</h3>
            ) : null}
          </div>
          {actions ? <div className="shrink-0">{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}
