/**
 * ProvenanceTooltip — where the shown value of a field comes from (REC-10):
 * source, rank, when it was recorded and observed, and why it was chosen
 * (override with author and reason, the highest-ranked source, the asset or
 * the CI record). It opens on hover and on keyboard focus.
 */
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { components } from '../../api/generated/schema';

export type CIFieldProvenance = components['schemas']['CIFieldProvenance'];

function display(value: unknown): string {
  if (value === undefined || value === null || value === '') return '—';
  return typeof value === 'object' ? JSON.stringify(value) : String(value);
}

export function ProvenanceTooltip({ provenance }: { provenance: CIFieldProvenance }) {
  const { t, i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  const tooltipId = useId();
  const time = (value?: string) => (value ? new Date(value).toLocaleString(i18n.language) : '—');

  return (
    <span
      className="relative inline-flex"
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
    >
      <button
        type="button"
        className="rounded-full px-1 text-xs text-gray-500 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
        aria-label={t('provenance.show', { field: provenance.field })}
        aria-describedby={open ? tooltipId : undefined}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onKeyDown={(event) => {
          if (event.key === 'Escape') setOpen(false);
        }}
      >
        ⓘ
      </button>
      {open ? (
        <span
          role="tooltip"
          id={tooltipId}
          className="absolute left-6 top-0 z-30 w-72 rounded-md border border-gray-200 bg-white p-3 text-xs shadow-lg dark:border-gray-700 dark:bg-gray-900"
        >
          <span className="mb-1 block font-semibold">
            {t(`provenance.reasons.${provenance.reason}`)}
          </span>
          <dl className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-1">
            <dt className="text-gray-500">{t('provenance.source')}</dt>
            <dd>{provenance.source}</dd>
            <dt className="text-gray-500">{t('provenance.rank')}</dt>
            <dd>{provenance.rank}</dd>
            <dt className="text-gray-500">{t('provenance.recordedAt')}</dt>
            <dd>{time(provenance.recorded_at)}</dd>
            <dt className="text-gray-500">{t('provenance.observedAt')}</dt>
            <dd>{time(provenance.observed_at)}</dd>
            {provenance.reason === 'override' ? (
              <>
                <dt className="text-gray-500">{t('provenance.overrideReason')}</dt>
                <dd>{provenance.override_reason || '—'}</dd>
                <dt className="text-gray-500">{t('provenance.overrideAuthor')}</dt>
                <dd>{provenance.override_author || '—'}</dd>
                <dt className="text-gray-500">{t('provenance.covers')}</dt>
                <dd>
                  {display(provenance.discovered_value)}
                  {provenance.discovered_source ? ` (${provenance.discovered_source})` : ''}
                </dd>
              </>
            ) : null}
          </dl>
        </span>
      ) : null}
    </span>
  );
}
