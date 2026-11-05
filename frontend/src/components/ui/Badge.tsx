import type { ReactNode } from 'react';
import { cn } from './utils';

type BadgeVariant = 'success' | 'warning' | 'danger' | 'neutral' | 'info';

const variantClasses: Record<BadgeVariant, string> = {
  success:
    'bg-green-50 text-green-800 ring-green-600/20 dark:bg-green-500/10 dark:text-green-300 dark:ring-green-400/25',
  warning:
    'bg-amber-50 text-amber-900 ring-amber-600/25 dark:bg-amber-500/10 dark:text-amber-200 dark:ring-amber-400/25',
  danger:
    'bg-red-50 text-red-800 ring-red-600/20 dark:bg-red-500/10 dark:text-red-300 dark:ring-red-400/25',
  neutral:
    'bg-gray-100 text-gray-700 ring-gray-500/15 dark:bg-gray-800 dark:text-gray-300 dark:ring-gray-600/40',
  info: 'bg-blue-50 text-blue-800 ring-blue-600/20 dark:bg-blue-400/10 dark:text-blue-200 dark:ring-blue-300/25',
};

// The status lamp in front of the label: hardware-style state indication.
// The label always carries the meaning; the colour only reinforces it.
const dotClasses: Record<BadgeVariant, string> = {
  success: 'bg-green-600 dark:bg-green-400',
  warning: 'bg-amber-500 dark:bg-amber-400',
  danger: 'bg-red-600 dark:bg-red-400',
  neutral: 'bg-gray-400 dark:bg-gray-500',
  info: 'bg-blue-600 dark:bg-blue-300',
};

export function Badge({
  variant = 'neutral',
  children,
  className,
  dot,
}: {
  variant?: BadgeVariant;
  children: ReactNode;
  className?: string;
  /** Shows the status lamp. Defaults to on for every variant but neutral. */
  dot?: boolean;
}) {
  const showDot = dot ?? variant !== 'neutral';
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 whitespace-nowrap rounded-md px-2 py-0.5 text-xs font-medium ring-1 ring-inset',
        variantClasses[variant],
        className,
      )}
    >
      {showDot ? (
        <span
          aria-hidden="true"
          className={cn('h-1.5 w-1.5 shrink-0 rounded-full', dotClasses[variant])}
        />
      ) : null}
      {children}
    </span>
  );
}
