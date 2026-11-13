import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { cn } from '../ui/utils';

/**
 * BrandMark is the reticle: a sighting ring with four ticks and a centre
 * point, the instrument that picks one device out of a network.
 */
export function BrandMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      aria-hidden="true"
      className={cn('h-6 w-6', className)}
    >
      <circle cx="12" cy="12" r="6.25" />
      <path d="M12 2.5v4M12 17.5v4M2.5 12h4M17.5 12h4" />
      <circle cx="12" cy="12" r="1.5" fill="currentColor" stroke="none" />
    </svg>
  );
}

/** Brand is the mark with the wordmark; it links to the start page. */
export function Brand({ compact = false, className }: { compact?: boolean; className?: string }) {
  const { t } = useTranslation();
  return (
    <Link
      to="/"
      aria-label={t('app.title')}
      className={cn(
        'inline-flex h-9 items-center gap-2 rounded-lg px-1.5 text-fg focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent',
        className,
      )}
    >
      <BrandMark className="text-accent" />
      {compact ? null : (
        <span className="text-[15px] font-semibold tracking-tight">
          Reticora
          <span className="ml-1.5 font-medium text-fg-subtle">CMDB</span>
        </span>
      )}
    </Link>
  );
}
