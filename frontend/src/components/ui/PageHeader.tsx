import { ChevronRight } from 'lucide-react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { cn } from './utils';

export interface Crumb {
  label: string;
  /** Omit on the last crumb: it names the current page. */
  to?: string;
}

interface PageHeaderProps {
  /** The h1 of the page: what this view is. */
  title: ReactNode;
  /** One sentence on what the user can do here (under 80 characters per line). */
  description?: ReactNode;
  /** Parents of the page, nearest last. */
  breadcrumbs?: Crumb[];
  /** Status or counters beside the title. */
  meta?: ReactNode;
  /** Page-level actions, primary action last. */
  actions?: ReactNode;
  className?: string;
}

/**
 * PageHeader opens every page: the one h1, an optional sentence of context
 * and the page's actions. Actions wrap under the title on narrow screens.
 */
export function PageHeader({
  title,
  description,
  breadcrumbs,
  meta,
  actions,
  className,
}: PageHeaderProps) {
  const { t } = useTranslation();
  return (
    <header
      className={cn(
        'flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between sm:gap-6',
        className,
      )}
    >
      <div className="min-w-0">
        {breadcrumbs && breadcrumbs.length > 0 ? (
          <nav aria-label={t('accessibility.breadcrumb')} className="mb-1.5">
            <ol className="flex flex-wrap items-center gap-1 text-sm text-fg-muted">
              {breadcrumbs.map((crumb, index) => (
                <li key={`${crumb.label}-${index}`} className="inline-flex items-center gap-1">
                  {index > 0 ? (
                    <ChevronRight aria-hidden="true" className="h-3.5 w-3.5 text-fg-subtle" />
                  ) : null}
                  {crumb.to ? (
                    <Link
                      to={crumb.to}
                      className="rounded hover:text-fg hover:underline underline-offset-2"
                    >
                      {crumb.label}
                    </Link>
                  ) : (
                    <span aria-current="page">{crumb.label}</span>
                  )}
                </li>
              ))}
            </ol>
          </nav>
        ) : null}
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <h1 className="text-2xl font-semibold text-fg [overflow-wrap:anywhere]">{title}</h1>
          {meta}
        </div>
        {description ? (
          <p className="mt-1 max-w-[68ch] text-sm text-fg-muted">{description}</p>
        ) : null}
      </div>
      {actions ? (
        <div className="flex flex-wrap items-center gap-2 sm:shrink-0">{actions}</div>
      ) : null}
    </header>
  );
}
