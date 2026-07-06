import type { ReactNode } from 'react';
import { cn } from './utils';

type BadgeVariant = 'success' | 'warning' | 'danger' | 'neutral' | 'info';

const variantClasses: Record<BadgeVariant, string> = {
  success: 'bg-green-100 text-green-800 dark:bg-green-900/50 dark:text-green-200',
  warning: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/50 dark:text-yellow-200',
  danger: 'bg-red-100 text-red-800 dark:bg-red-900/50 dark:text-red-200',
  neutral: 'bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-200',
  info: 'bg-blue-100 text-blue-800 dark:bg-blue-900/50 dark:text-blue-200',
};

export function Badge({
  variant = 'neutral',
  children,
  className,
}: {
  variant?: BadgeVariant;
  children: ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center rounded-full px-2.5 py-1 text-xs font-medium',
        variantClasses[variant],
        className,
      )}
    >
      {children}
    </span>
  );
}
