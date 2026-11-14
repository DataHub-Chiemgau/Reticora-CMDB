import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { Badge } from '../ui/Badge';
import { CIP3Section } from './CIFindingsSection';

/** runLink is the run view of a workflow run (route /workflows/runs/:id of WP-1132). */
export function runLink(runId: string): string {
  return `/workflows/runs/${encodeURIComponent(runId)}`;
}

function statusVariant(status: string) {
  switch (status) {
    case 'succeeded':
      return 'success' as const;
    case 'failed':
      return 'danger' as const;
    case 'cancelled':
      return 'neutral' as const;
  }
  return 'info' as const;
}

/**
 * CIRunsSection shows the last 20 workflow runs on the CI (WFL-02, WP-1137
 * AK5): workflow, version, trigger, status and times, each linked to the
 * run view.
 */
export function CIRunsSection({ ciId }: { ciId: string }) {
  const { t, i18n } = useTranslation();
  const time = (value?: string | null) =>
    value
      ? new Date(value).toLocaleString(i18n.language, { dateStyle: 'medium', timeStyle: 'short' })
      : '–';
  return (
    <CIP3Section ciId={ciId} module="workflows" field="workflowRuns" title={t('ciP3.runs.title')}>
      {(ci) =>
        ci.workflowRuns.length === 0 ? (
          <p className="text-sm text-fg-muted">{t('ciP3.runs.empty')}</p>
        ) : (
          <ul className="space-y-2">
            {ci.workflowRuns.slice(0, 20).map((run) => (
              <li key={run.id} className="flex flex-wrap items-center gap-2 text-sm">
                <Link className="font-medium text-accent underline" to={runLink(run.id)}>
                  {t('ciP3.runs.link', { name: run.workflowName, version: run.version })}
                </Link>
                <Badge variant={statusVariant(run.status)}>
                  {t(`ciP3.runs.statuses.${run.status}`, run.status)}
                </Badge>
                <span className="text-xs text-fg-muted">
                  {t('ciP3.runs.times', {
                    trigger: run.trigger,
                    // A run not started yet shows no start, its creation
                    // separately.
                    created: time(run.createdAt),
                    started: time(run.startedAt),
                    finished: time(run.finishedAt),
                  })}
                </span>
              </li>
            ))}
          </ul>
        )
      }
    </CIP3Section>
  );
}
