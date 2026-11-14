import { ChevronRight } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { CI_FINDINGS_LIMIT, useCIP3Detail, type CIP3Detail } from '../../api/hooks';
import { useModules } from '../../hooks/useEntitlements';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { SkeletonList } from '../ui/Skeleton';

/** The BFF fields of the three sections and their modules (WP-1137 AK2). */
export type CIP3Field = 'findings' | 'complianceResults' | 'workflowRuns';

/** The codes of a refused or omitted field: the section is hidden. */
const HIDDEN_CODES = new Set(['UNAVAILABLE', 'FORBIDDEN']);

/** The severities in their order, highest first. */
export const SEVERITY_ORDER = ['critical', 'high', 'medium', 'low', 'info'] as const;

export function severityVariant(severity: string) {
  switch (severity) {
    case 'critical':
    case 'high':
      return 'danger' as const;
    case 'medium':
      return 'warning' as const;
    case 'low':
      return 'info' as const;
  }
  return 'neutral' as const;
}

/**
 * CIP3Section is one of the sections Findings, Compliance and Automationen
 * of the CI detail (WP-1137): an expandable panel like the other sections
 * that reads the shared BFF query of the three on its first expand, shows
 * nothing while its module is off or the server refuses or leaves out its
 * field (UNAVAILABLE, FORBIDDEN), and an error with retry otherwise.
 */
export function CIP3Section({
  ciId,
  module,
  field,
  title,
  summary,
  children,
}: {
  ciId: string;
  module: string;
  field: CIP3Field;
  title: string;
  /** summary is shown beside the title once the data is there. */
  summary?: (ci: NonNullable<CIP3Detail['ci']>) => ReactNode;
  children: (ci: NonNullable<CIP3Detail['ci']>) => ReactNode;
}) {
  const { t } = useTranslation();
  const modules = useModules();
  const [opened, setOpened] = useState(false);
  const detail = useCIP3Detail(ciId, opened && modules.isModuleEnabled(module));
  if (!modules.isModuleEnabled(module)) return null;
  const fieldErrors = (detail.data?.errors ?? []).filter(
    (e) => e.path[0] === 'ci' && e.path[1] === field,
  );
  if (fieldErrors.some((e) => HIDDEN_CODES.has(e.code ?? ''))) return null;
  const ci = detail.data?.data?.ci ?? null;
  const failed = detail.isError || fieldErrors.length > 0 || (detail.isSuccess && !ci);
  return (
    <details
      className="group rounded-xl border border-line bg-panel"
      data-testid={`ci-section-${field}`}
      onToggle={(e) => {
        if (e.currentTarget.open) setOpened(true);
      }}
    >
      <summary className="flex min-h-11 cursor-pointer select-none list-none items-center gap-2 rounded-xl px-4 py-2.5 text-sm font-medium text-fg outline-none transition-colors hover:bg-fg/[0.03] focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent group-open:rounded-b-none [&::-webkit-details-marker]:hidden">
        <ChevronRight
          aria-hidden="true"
          className="h-4 w-4 shrink-0 text-fg-subtle transition-transform group-open:rotate-90"
        />
        {title}
        {ci && !failed && summary ? summary(ci) : null}
      </summary>
      <div className="border-t border-line px-4 py-4">
        {/* Nothing while closed: a skeleton there would pulse unseen and
            keep the page from ever settling (its animation runs forever). */}
        {!opened ? null : detail.isLoading ? (
          <SkeletonList rows={2} label={t('ciP3.loading')} />
        ) : failed ? (
          <div className="flex flex-wrap items-center gap-3" role="alert">
            <p className="text-sm text-danger">{t('ciP3.error')}</p>
            <Button size="sm" variant="secondary" onClick={() => void detail.refetch()}>
              {t('common.retry')}
            </Button>
          </div>
        ) : ci ? (
          children(ci)
        ) : null}
      </div>
    </details>
  );
}

/**
 * CIFindingsSection shows the open and acknowledged findings of the CI
 * (SEC-02, WP-1137 AK3): severity, title, CVE ids, KEV, status and fixed
 * version, the count per severity in the head and a link to the findings
 * page filtered on the CI.
 */
export function CIFindingsSection({ ciId }: { ciId: string }) {
  const { t } = useTranslation();
  return (
    <CIP3Section
      ciId={ciId}
      module="findings"
      field="findings"
      title={t('ciP3.findings.title')}
      summary={(ci) => {
        // A full page may hide more findings: the counts are then a lower
        // bound (n+), the link shows all of them.
        const capped = ci.findings.length >= CI_FINDINGS_LIMIT;
        return (
          <span className="flex flex-wrap gap-1" data-testid="finding-counts">
            {SEVERITY_ORDER.map((sev) => {
              const n = ci.findings.filter((f) => f.severity === sev).length;
              return n > 0 ? (
                <Badge key={sev} variant={severityVariant(sev)}>
                  {t(capped ? 'ciP3.findings.countAtLeast' : 'ciP3.findings.count', {
                    severity: t(`compliance.severities.${sev}`, sev),
                    count: n,
                  })}
                </Badge>
              ) : null;
            })}
          </span>
        );
      }}
    >
      {(ci) => (
        <div className="space-y-3">
          {ci.findings.length === 0 ? (
            <p className="text-sm text-fg-muted">{t('ciP3.findings.empty')}</p>
          ) : (
            <ul className="space-y-2">
              {ci.findings.length >= CI_FINDINGS_LIMIT ? (
                <li className="text-xs text-fg-muted">
                  {t('ciP3.findings.capped', { count: CI_FINDINGS_LIMIT })}
                </li>
              ) : null}
              {ci.findings.map((f) => (
                <li key={f.id} className="flex flex-wrap items-center gap-2 text-sm">
                  <Badge variant={severityVariant(f.severity)}>
                    {t(`compliance.severities.${f.severity}`, f.severity)}
                  </Badge>
                  <span className="font-medium text-fg">{f.title}</span>
                  {f.cveIds.length ? (
                    <span className="font-mono text-xs text-fg-muted">{f.cveIds.join(', ')}</span>
                  ) : null}
                  {f.kev ? <Badge variant="danger">{t('ciP3.findings.kev')}</Badge> : null}
                  <Badge variant="neutral">
                    {t(`ciP3.findings.statuses.${f.status}`, f.status)}
                  </Badge>
                  {f.fixedVersion ? (
                    <span className="text-xs text-fg-muted">
                      {t('ciP3.findings.fixedVersion', { version: f.fixedVersion })}
                    </span>
                  ) : null}
                </li>
              ))}
            </ul>
          )}
          <Link
            className="text-sm text-accent underline"
            to={`/findings?ci_id=${encodeURIComponent(ciId)}`}
          >
            {t('ciP3.findings.all')}
          </Link>
        </div>
      )}
    </CIP3Section>
  );
}
