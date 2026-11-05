import { ArrowUpRight } from 'lucide-react';
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { cn } from './utils';
import { Skeleton } from './Skeleton';
import { useTranslation } from 'react-i18next';

type Tone = 'default' | 'warning' | 'danger';

const valueTone: Record<Tone, string> = {
  default: 'text-fg',
  warning: 'text-amber-700 dark:text-amber-300',
  danger: 'text-red-700 dark:text-red-300',
};

interface MetricCardProps {
  label: string;
  /** The figure. Undefined while it loads. */
  value?: ReactNode;
  loading?: boolean;
  /** Colours the figure when it asks for attention. */
  tone?: Tone;
  /** One line of context under the figure. */
  hint?: ReactNode;
  /** Where the figure leads to: the whole card becomes a link. */
  to?: string;
  /** A badge beside the figure. */
  badge?: ReactNode;
  /** Content below the hint (links, retry). */
  children?: ReactNode;
  className?: string;
}

/**
 * MetricCard shows one number the user watches: the label (an h3), the
 * figure, a line of context. With `to` the whole card leads to the page
 * where the figure can be acted on.
 */
export function MetricCard({
  label,
  value,
  loading = false,
  tone = 'default',
  hint,
  to,
  badge,
  children,
  className,
}: MetricCardProps) {
  const { t } = useTranslation();
  return (
    <section
      className={cn(
        'group relative flex min-h-[7.5rem] flex-col rounded-xl border border-line bg-panel p-4 transition-colors',
        to && 'hover:border-line-strong hover:bg-fg/[0.02]',
        className,
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <h3 className="text-sm font-medium text-fg-muted [overflow-wrap:anywhere]">
          {to ? (
            <Link
              to={to}
              className="rounded after:absolute after:inset-0 after:rounded-xl focus-visible:outline-none focus-visible:after:outline focus-visible:after:outline-2 focus-visible:after:outline-offset-2 focus-visible:after:outline-accent"
            >
              {label}
            </Link>
          ) : (
            label
          )}
        </h3>
        {to ? (
          <ArrowUpRight
            aria-hidden="true"
            className="h-4 w-4 shrink-0 text-fg-subtle opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100"
          />
        ) : null}
      </div>
      <div className="mt-2 flex items-center gap-2.5">
        {loading ? (
          <>
            <Skeleton className="h-9 w-16" />
            <span className="sr-only">{t('app.loading')}</span>
          </>
        ) : (
          <p
            className={cn(
              'text-3xl font-semibold tabular-nums leading-9 tracking-tight',
              valueTone[tone],
            )}
          >
            {value ?? '—'}
          </p>
        )}
        {badge}
      </div>
      {hint ? <p className="mt-1.5 text-sm text-fg-muted">{hint}</p> : null}
      {children ? <div className="relative mt-auto pt-3">{children}</div> : null}
    </section>
  );
}
