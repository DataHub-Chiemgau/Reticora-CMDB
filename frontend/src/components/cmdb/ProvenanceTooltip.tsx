/**
 * ProvenanceTooltip — where the shown value of a field comes from (REC-10):
 * source, rank, when it was recorded and observed, and why it was chosen
 * (override with author – by display name, else the id – and reason, the highest-ranked source, the asset or
 * the CI record). It opens on hover and on keyboard focus.
 */
import { Info } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { components } from '../../api/generated/schema';
import { fieldValueText } from './fieldValue';

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
        className="inline-flex h-6 w-6 items-center justify-center rounded-md text-fg-subtle transition-colors hover:text-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
        aria-label={t('provenance.show', { field: provenance.field })}
        aria-describedby={open ? tooltipId : undefined}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onKeyDown={(event) => {
          if (event.key === 'Escape') setOpen(false);
        }}
      >
        <Info aria-hidden="true" className="h-4 w-4" />
      </button>
      {open ? (
        <span
          role="tooltip"
          id={tooltipId}
          className="absolute left-7 top-0 z-30 w-72 max-w-[calc(100vw-4rem)] rounded-lg border border-line bg-raised p-3 text-xs font-normal text-fg shadow-lg"
        >
          <span className="mb-2 block text-sm font-semibold">
            {t(`provenance.reasons.${provenance.reason}`)}
          </span>
          <dl className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-1">
            <dt className="text-fg-muted">{t('provenance.source')}</dt>
            <dd>{provenance.source}</dd>
            <dt className="text-fg-muted">{t('provenance.rank')}</dt>
            <dd>{provenance.rank}</dd>
            <dt className="text-fg-muted">{t('provenance.recordedAt')}</dt>
            <dd>{time(provenance.recorded_at)}</dd>
            <dt className="text-fg-muted">{t('provenance.observedAt')}</dt>
            <dd>{time(provenance.observed_at)}</dd>
            {provenance.reason === 'override' ? (
              <>
                <dt className="text-fg-muted">{t('provenance.overrideReason')}</dt>
                <dd>{provenance.override_reason || '—'}</dd>
                <dt className="text-fg-muted">{t('provenance.overrideAuthor')}</dt>
                {/* The display name the server resolves (UI-15); the id when it has none. */}
                <dd>{provenance.override_author_name || provenance.override_author || '—'}</dd>
                <dt className="text-fg-muted">{t('provenance.covers')}</dt>
                <dd>
                  {fieldValueText(t, provenance.field, provenance.discovered_value, display)}
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
