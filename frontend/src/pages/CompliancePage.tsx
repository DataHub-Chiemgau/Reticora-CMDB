import { CheckCircle2, FilePlus2, Pencil, Play, Plus, ShieldCheck } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  COMPLIANCE_STANDARDS,
  type ComplianceRule,
  type ComplianceScoreGroupBy,
  type ComplianceStandard,
} from '../api/client';
import {
  useComplianceCoverage,
  useComplianceResults,
  useComplianceRules,
  useComplianceRuleTemplates,
  useComplianceScore,
  useComplianceScoreBy,
  useComplianceScoreHistory,
  useEvaluateCompliance,
  useHasPermission,
  useInstantiateComplianceTemplate,
  useInvalidateCompliance,
} from '../api/hooks';
import { RuleEditor } from '../components/compliance/RuleEditor';
import { ScoreTrend } from '../components/compliance/ScoreTrend';
import { ScrollRegion } from '../components/ScrollRegion';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { MetricCard } from '../components/ui/MetricCard';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { isActive, useJob } from '../hooks/useJobs';

const severityVariant = {
  critical: 'danger',
  high: 'danger',
  medium: 'warning',
  low: 'info',
} as const;

/** The groupings of the score view (WP-1135 AK3). */
export const SCORE_GROUPS: readonly ComplianceScoreGroupBy[] = [
  'ci_type',
  'client',
  'site',
  'category',
  'severity',
];

/** The periods of the trend in days. */
export const TREND_DAYS = [30, 90, 400] as const;

/**
 * CompliancePage (SEC-03, SEC-04, WP-1135): score and score per group with
 * its trend over 30, 90 or 400 days, the rules with the editor and the
 * templates, the evaluation as a job with its progress, the controls of
 * each standard without a rule and the failed results.
 */
export function CompliancePage() {
  const { t } = useTranslation();
  const rules = useComplianceRules();
  const results = useComplianceResults();
  const score = useComplianceScore();
  const evaluate = useEvaluateCompliance();
  const invalidate = useInvalidateCompliance();
  const canWrite = useHasPermission('compliance:write') === true;
  const [jobId, setJobId] = useState<string | null>(null);
  const job = useJob(jobId);
  const running = !!jobId && (!job.data || isActive(job.data));
  const [editing, setEditing] = useState<ComplianceRule | 'new' | null>(null);
  const [templatesOpen, setTemplatesOpen] = useState(false);

  // The job ended: score and results are read again (AK4).
  const finished = job.data && !isActive(job.data) ? job.data.status : undefined;
  useEffect(() => {
    if (finished) invalidate();
    // invalidate is stable in behavior; the effect follows the end of the job.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [finished]);

  const header = (
    <PageHeader
      title={t('compliance.title')}
      description={t('compliance.subtitle')}
      actions={
        <>
          {canWrite ? (
            <>
              <Button
                variant="secondary"
                icon={<FilePlus2 />}
                onClick={() => setTemplatesOpen(true)}
              >
                {t('compliance.templates.open')}
              </Button>
              <Button variant="secondary" icon={<Plus />} onClick={() => setEditing('new')}>
                {t('compliance.editor.create')}
              </Button>
            </>
          ) : null}
          <Button
            icon={<Play />}
            onClick={() => evaluate.mutate(undefined, { onSuccess: (j) => setJobId(j.id) })}
            loading={evaluate.isPending || running}
            disabled={running || !canWrite}
          >
            {t('compliance.evaluate')}
          </Button>
        </>
      }
    />
  );
  if (rules.isLoading || results.isLoading || score.isLoading)
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={6} label={t('compliance.loading')} />
      </div>
    );
  if (rules.isError)
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('compliance.errorTitle')}
          description={rules.error.message}
          retryLabel={t('common.retry')}
          onRetry={() => rules.refetch()}
        />
      </div>
    );
  const failures = results.data?.data.filter((result) => result.status === 'fail') ?? [];
  return (
    <div className="space-y-6">
      {header}
      {jobId ? (
        <p role="status" className="text-sm text-fg-muted" data-testid="evaluation-job">
          {job.data
            ? t('compliance.job.status', {
                status: t(`jobs.statuses.${job.data.status}`, job.data.status),
                progress: job.data.progress,
              })
            : t('compliance.job.queued')}
        </p>
      ) : null}
      <div className="grid gap-4 sm:grid-cols-3">
        <MetricCard
          label={t('compliance.score')}
          value={`${Math.round(score.data?.overall.score ?? 0)}%`}
          hint={t('compliance.scoreHint', {
            passed: score.data?.overall.passed ?? 0,
            failed: score.data?.overall.failed ?? 0,
          })}
        />
        <MetricCard
          label={t('compliance.resultStatuses.pass')}
          value={score.data?.overall.passed ?? 0}
        />
        <MetricCard
          label={t('compliance.resultStatuses.fail')}
          value={score.data?.overall.failed ?? 0}
          tone={(score.data?.overall.failed ?? 0) > 0 ? 'danger' : 'default'}
        />
      </div>
      <ScoreGroups />
      <TrendSection />
      <section className="space-y-3" aria-labelledby="compliance-rules">
        <h2 id="compliance-rules" className="text-base font-semibold text-fg">
          {t('compliance.rules')}{' '}
          <span className="tabular-nums text-fg-muted">({rules.data?.total ?? 0})</span>
        </h2>
        {rules.data?.data.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('compliance.rules')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('common.name')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('ticket.category')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('compliance.severity')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('compliance.editor.active')}
                    </th>
                    <th scope="col" className={thClasses}>
                      <span className="sr-only">{t('common.actions')}</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rules.data.data.map((rule) => (
                    <tr key={rule.id} className={trClasses}>
                      <td className={tdPrimaryClasses}>{rule.name}</td>
                      <td className={tdClasses}>
                        <Badge variant="info">
                          {t(`compliance.categories.${rule.category}`, rule.category)}
                        </Badge>
                      </td>
                      <td className={tdClasses}>
                        <Badge
                          variant={
                            severityVariant[rule.severity as keyof typeof severityVariant] ??
                            'neutral'
                          }
                        >
                          {t(`compliance.severities.${rule.severity}`, rule.severity)}
                        </Badge>
                      </td>
                      <td className={tdClasses}>
                        {rule.active
                          ? t('compliance.editor.activeYes')
                          : t('compliance.editor.activeNo')}
                      </td>
                      <td className={tdClasses}>
                        <Button
                          variant="ghost"
                          size="sm"
                          icon={<Pencil />}
                          onClick={() => setEditing(rule)}
                          aria-label={t(
                            canWrite ? 'compliance.editor.editRule' : 'compliance.editor.viewRule',
                            { name: rule.name },
                          )}
                        >
                          {canWrite ? t('common.edit') : t('compliance.editor.view')}
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : (
          <EmptyState
            icon={<ShieldCheck />}
            title={t('compliance.emptyRules')}
            description={t('compliance.emptyRulesHint')}
          />
        )}
      </section>
      <CoverageSection />
      <section className="space-y-3" aria-labelledby="compliance-failures">
        <h2 id="compliance-failures" className="text-base font-semibold text-fg">
          {t('compliance.failures')}{' '}
          <span className="tabular-nums text-fg-muted">({failures.length})</span>
        </h2>
        {failures.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('compliance.failures')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('compliance.ci')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('ci.status')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('compliance.details')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {failures.map((result) => (
                    <tr key={result.id} className={trClasses}>
                      <td className={tdMonoClasses}>{result.ci_id}</td>
                      <td className={tdClasses}>
                        <Badge variant="danger">
                          {t(`compliance.resultStatuses.${result.status}`, result.status)}
                        </Badge>
                      </td>
                      <td className={tdClasses}>{result.details || '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : (
          <EmptyState
            icon={<CheckCircle2 />}
            title={t('compliance.noFailures')}
            description={t('compliance.noFailuresHint')}
          />
        )}
      </section>
      <RuleEditor
        open={editing !== null}
        onOpenChange={(open) => {
          if (!open) setEditing(null);
        }}
        rule={editing && editing !== 'new' ? editing : undefined}
        readOnly={!canWrite}
      />
      <TemplateDialog
        open={templatesOpen}
        onOpenChange={setTemplatesOpen}
        onCreated={(rule) => {
          setTemplatesOpen(false);
          setEditing(rule);
        }}
      />
    </div>
  );
}

/** ScoreGroups is the score per group with its choice (AK3). */
function ScoreGroups() {
  const { t } = useTranslation();
  const [groupBy, setGroupBy] = useState<ComplianceScoreGroupBy>('ci_type');
  const grouped = useComplianceScoreBy(groupBy);
  const groups = grouped.data?.groups ?? [];
  return (
    <section className="space-y-3" aria-labelledby="compliance-groups">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <h2 id="compliance-groups" className="text-base font-semibold text-fg">
          {t('compliance.groups.title')}
        </h2>
        <Select
          label={t('compliance.groups.groupBy')}
          className="w-48"
          value={groupBy}
          onChange={(e) => setGroupBy(e.target.value as ComplianceScoreGroupBy)}
          options={SCORE_GROUPS.map((g) => ({ value: g, label: t(`compliance.groups.by.${g}`) }))}
        />
      </div>
      {groups.length ? (
        <Card className="overflow-hidden p-0">
          <ScrollRegion label={t('compliance.groups.title')}>
            <table className={tableClasses} data-testid="score-groups">
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t(`compliance.groups.by.${groupBy}`)}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('compliance.score')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('compliance.resultStatuses.pass')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('compliance.resultStatuses.fail')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => (
                  <tr key={g.key || '-'} className={trClasses}>
                    <td className={tdPrimaryClasses}>{g.label || t('compliance.groups.none')}</td>
                    <td className={tdClasses}>{`${Math.round(g.score)}%`}</td>
                    <td className={tdClasses}>{g.passed}</td>
                    <td className={tdClasses}>{g.failed}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : (
        <p className="text-sm text-fg-muted">{t('compliance.groups.empty')}</p>
      )}
    </section>
  );
}

/** TrendSection is the score trend of 30, 90 or 400 days (AK3). */
function TrendSection() {
  const { t } = useTranslation();
  const [days, setDays] = useState<(typeof TREND_DAYS)[number]>(30);
  const history = useComplianceScoreHistory(days);
  const points = (history.data?.data ?? [])
    .filter((p) => p.key === '' || history.data?.group_by === 'overall')
    .map((p) => ({ day: p.day, score: p.score }));
  return (
    <section className="space-y-3" aria-labelledby="compliance-trend">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <h2 id="compliance-trend" className="text-base font-semibold text-fg">
          {t('compliance.trend.title')}
        </h2>
        <div role="group" aria-label={t('compliance.trend.period')} className="flex gap-1">
          {TREND_DAYS.map((d) => (
            <Button
              key={d}
              size="sm"
              variant={d === days ? 'primary' : 'secondary'}
              aria-pressed={d === days}
              onClick={() => setDays(d)}
            >
              {t('compliance.trend.days', { count: d })}
            </Button>
          ))}
        </div>
      </div>
      <Card>
        <ScoreTrend points={points} label={t('compliance.trend.table', { count: days })} />
      </Card>
    </section>
  );
}

/** CoverageSection lists the controls of each standard without a rule (AK5). */
function CoverageSection() {
  const { t } = useTranslation();
  return (
    <section className="space-y-3" aria-labelledby="compliance-coverage">
      <h2 id="compliance-coverage" className="text-base font-semibold text-fg">
        {t('compliance.coverage.title')}
      </h2>
      <div className="grid gap-4 md:grid-cols-3">
        {COMPLIANCE_STANDARDS.map((s) => (
          <CoverageCard key={s} standard={s} />
        ))}
      </div>
    </section>
  );
}

function CoverageCard({ standard }: { standard: ComplianceStandard }) {
  const { t, i18n } = useTranslation();
  const gaps = useComplianceCoverage(standard);
  const de = i18n.language.startsWith('de');
  const list = gaps.data?.data ?? [];
  return (
    <Card title={t(`compliance.standards.${standard}`)}>
      <p className="text-sm text-fg-muted">
        {t('compliance.coverage.gaps', { count: gaps.data?.total ?? list.length })}
      </p>
      {list.length ? (
        <ul className="mt-2 max-h-48 space-y-1 overflow-y-auto text-sm">
          {list.map((c) => (
            <li key={c.id}>
              <span className="font-mono">{c.control_id}</span> {de ? c.title_de : c.title_en}
            </li>
          ))}
        </ul>
      ) : null}
    </Card>
  );
}

/** TemplateDialog lists the shipped templates; one is created as an inactive rule (AK2). */
function TemplateDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (rule: ComplianceRule) => void;
}) {
  const { t } = useTranslation();
  const templates = useComplianceRuleTemplates(open);
  const instantiate = useInstantiateComplianceTemplate();
  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      title={t('compliance.templates.title')}
      description={t('compliance.templates.description')}
      size="lg"
    >
      <ul className="space-y-2">
        {(templates.data?.data ?? []).map((tpl) => (
          <li
            key={tpl.key}
            className="flex items-start justify-between gap-3 rounded border border-line p-3"
          >
            <div>
              <p className="font-medium text-fg">{tpl.name}</p>
              <p className="text-sm text-fg-muted">{tpl.description}</p>
            </div>
            <Button
              size="sm"
              loading={instantiate.isPending && instantiate.variables === tpl.key}
              onClick={() => instantiate.mutate(tpl.key, { onSuccess: onCreated })}
              aria-label={t('compliance.templates.useNamed', { name: tpl.name })}
            >
              {t('compliance.templates.use')}
            </Button>
          </li>
        ))}
      </ul>
      {instantiate.isError ? (
        <p role="alert" className="mt-2 text-sm text-danger">
          {instantiate.error instanceof Error ? instantiate.error.message : ''}
        </p>
      ) : null}
    </Modal>
  );
}
