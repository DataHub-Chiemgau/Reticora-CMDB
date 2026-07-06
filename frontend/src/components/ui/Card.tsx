import type { ReactNode } from 'react';
import { cn } from './utils';

interface CardProps {
  children: ReactNode;
  className?: string;
  title?: ReactNode;
  actions?: ReactNode;
}

export function Card({ children, className, title, actions }: CardProps) {
  return (
    <section
      className={cn(
        'rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-gray-800 dark:bg-gray-900',
        className,
      )}
    >
      {title || actions ? (
        <div className="mb-4 flex items-start justify-between gap-4">
          <div>{title ? <h3 className="text-base font-semibold text-gray-900 dark:text-gray-100">{title}</h3> : null}</div>
          {actions ? <div className="shrink-0">{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}
