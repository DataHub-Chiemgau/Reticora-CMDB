import { BellRing, Pencil, Play, Plus, Settings2 } from 'lucide-react';
import { Link } from 'react-router';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useAlertRules, useDeleteAlertRule, useMetricPoints, useSites } from '../api/hooks';
import { useCITypes, useClients } from '../api/cmdbHooks';
import { localizedText } from '../api/cmdb';
import type { AlertRule } from '../api/client';
import { AlertRuleForm, STAGES } from '../components/monitoring/AlertRuleForm';
import { summarizeCondition, thresholdLines, opSymbol } from '../components/monitoring/thresholds';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
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
import { ScrollRegion } from '../components/ScrollRegion';

const severityVariants = {
  critical: 'danger',
  warning: 'warning',
  info: 'info',
} as const;

// The colours of the threshold lines: the tokens of the severity (WP-990 AK5).
const lineClasses = {
  critical: 'border-danger',
  warning: 'border-warning',
} as const;

// The severity of a rule: its old form, else its highest stage (WP-985).
function ruleSeverity(rule: AlertRule): 'critical' | 'warning' | 'info' {
  if (rule.severity) return rule.severity;
  if (rule.critical_condition) return 'critical';
  if (rule.warning_condition) return 'warning';
  return 'info';
}

// The names the scope of a rule is shown with: client, site and CI type by
// id (the id itself while unknown).
interface ScopeNames {
  client: (id: string) => string;
  site: (id: string) => string;
  ciType: (id: string) => string;
}

// scopeParts renders the scope of a rule in short form (WP-990 AK4):
// client, site, the number of CIs, the CI types and the label pairs; empty
// for a rule over all series.
export function scopeParts(rule: AlertRule, names: ScopeNames, t: TFunction): string[] {
  const s = rule.scope ?? {};
  const out: string[] = [];
  if (s.client_id)
    out.push(t('monitoring.rule.scopeClientShort', { name: names.client(s.client_id) }));
  if (s.site_id) out.push(t('monitoring.rule.scopeSiteShort', { name: names.site(s.site_id) }));
  if (s.ci_ids?.length) out.push(t('monitoring.rule.scopeCIsShort', { count: s.ci_ids.length }));
  if (s.ci_type_ids?.length) {
    out.push(
      t('monitoring.rule.scopeCITypesShort', { names: s.ci_type_ids.map(names.ciType).join(', ') }),
    );
  }
  const labels = Object.entries(s.labels ?? {});
  if (labels.length) {
    out.push(
      t('monitoring.rule.scopeLabelsShort', {
        labels: labels.map(([k, v]) => `${k}=${v}`).join(', '),
      }),
    );
  }
  return out;
}

export function MonitoringPage() {
  const { t, i18n } = useTranslation();
  const alerts = useAlertRules();
  const deleteAlert = useDeleteAlertRule();
  // The names of the scopes in the list, read only when a rule needs them.
  const scopes = (alerts.data ?? []).map((r) => r.scope ?? {});
  const clients = useClients(scopes.some((s) => s.client_id));
  const sites = useSites(undefined, { enabled: scopes.some((s) => s.site_id) });
  const ciTypes = useCITypes(
    { limit: 200 },
    { enabled: scopes.some((s) => s.ci_type_ids?.length) },
  );
  const names: ScopeNames = {
    client: (id) => clients.data?.data.find((c) => c.id === id)?.name ?? id,
    site: (id) => sites.data?.data.find((x) => x.id === id)?.name ?? id,
    ciType: (id) => {
      const ct = ciTypes.data?.data.find((x) => x.id === id);
      return ct ? localizedText(ct.display_name, i18n.language) || ct.name : id;
    },
  };

  const [metricName, setMetricName] = useState('');
  const [ciId, setCiId] = useState('');
  const [query, setQuery] = useState({ name: '', ci_id: '' });
  const points = useMetricPoints({ name: query.name, ci_id: query.ci_id });

  // The rule form: closed, a new rule (null) or the rule to edit.
  const [editing, setEditing] = useState<AlertRule | null | undefined>(undefined);

  const header = (
    <PageHeader
      title={t('monitoring.title')}
      description={t('monitoring.subtitle')}
      actions={
        <>
          <Button asChild variant="secondary">
            <Link to="/monitoring/settings">
              <Settings2 aria-hidden="true" className="mr-1.5" />
              {t('monitoring.settings.action')}
            </Link>
          </Button>
          <Button icon={<Plus />} onClick={() => setEditing(null)}>
            {t('monitoring.createAlert')}
          </Button>
        </>
      }
    />
  );

  const form = (
    <AlertRuleForm
      open={editing !== undefined}
      onOpenChange={(open) => {
        if (!open) setEditing(undefined);
      }}
      rule={editing ?? undefined}
    />
  );

  if (alerts.isLoading) {
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={6} label={t('monitoring.loading')} />
      </div>
    );
  }

  if (alerts.isError) {
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('monitoring.errorTitle')}
          description={alerts.error.message}
          retryLabel={t('common.retry')}
          onRetry={() => alerts.refetch()}
        />
      </div>
    );
  }

  const { lines, skipped } = thresholdLines(
    alerts.data ?? [],
    query.name,
    query.ci_id || undefined,
  );
  const maxValue = Math.max(
    points.data?.reduce((max, p) => Math.max(max, p.value), 0) ?? 0,
    ...lines.map((l) => l.value),
    0,
  );
  const lineText = lines
    .map(
      (l) =>
        `${l.ruleName} ${t(`monitoring.rule.stage_${l.stage}`)}: ${l.field} ${opSymbol(l.op)} ${l.value}` +
        (l.scopeUnchecked ? ` (${t('monitoring.rule.lineScopeUnchecked')})` : ''),
    )
    .join('; ');

  return (
    <div className="space-y-6">
      {header}
      {form}

      <Card title={t('monitoring.queryMetrics')}>
        <div className="grid gap-4 md:grid-cols-3 md:items-end">
          <Input
            label={t('monitoring.metricName')}
            value={metricName}
            onChange={(event) => setMetricName(event.target.value)}
          />
          <Input
            label={t('monitoring.ciId')}
            value={ciId}
            onChange={(event) => setCiId(event.target.value)}
          />
          <Button
            variant="secondary"
            icon={<Play />}
            onClick={() => setQuery({ name: metricName, ci_id: ciId })}
            disabled={!metricName}
          >
            {t('monitoring.runQuery')}
          </Button>
        </div>
        {points.isFetching ? (
          <p className="mt-4 text-sm text-fg-muted">{t('monitoring.loading')}</p>
        ) : points.data?.length ? (
          <div className="mt-4">
            <div
              className="relative flex h-40 items-end gap-1"
              role="img"
              aria-label={
                lines.length
                  ? `${t('monitoring.series')}. ${t('monitoring.rule.lines', { lines: lineText })}`
                  : t('monitoring.series')
              }
            >
              {points.data.map((point) => (
                <div
                  key={point.timestamp}
                  className="flex-1 rounded-t-sm bg-accent/70"
                  style={{
                    height: `${maxValue > 0 ? Math.max((point.value / maxValue) * 100, 2) : 2}%`,
                  }}
                  title={`${new Date(point.timestamp).toLocaleString()}: ${point.value}`}
                />
              ))}
              {lines.map((line) => (
                <div
                  key={`${line.ruleId}-${line.stage}`}
                  data-testid={`threshold-${line.stage}`}
                  aria-hidden="true"
                  className={`pointer-events-none absolute inset-x-0 border-t-2 border-dashed ${lineClasses[line.stage]}`}
                  style={{ bottom: `${maxValue > 0 ? (line.value / maxValue) * 100 : 0}%` }}
                  title={`${line.ruleName}: ${line.field} ${opSymbol(line.op)} ${line.value}`}
                />
              ))}
            </div>
            <p className="mt-2 text-xs text-fg-muted tabular-nums">
              {t('monitoring.points', { count: points.data.length })}
            </p>
            {lines.length ? (
              <p className="mt-1 text-xs text-fg-muted">
                {t('monitoring.rule.lines', { lines: lineText })}
              </p>
            ) : null}
            {skipped.map((s) => (
              <p key={`${s.ruleId}-${s.stage}`} className="mt-1 text-xs text-fg-muted">
                {t('monitoring.rule.noLine', {
                  rule: s.ruleName,
                  stage: t(`monitoring.rule.stage_${s.stage}`),
                })}
              </p>
            ))}
          </div>
        ) : query.name ? (
          <div className="mt-4">
            <EmptyState title={t('monitoring.noData')} description={t('monitoring.noDataHint')} />
          </div>
        ) : null}
      </Card>

      <section className="space-y-3" aria-labelledby="monitoring-rules">
        <h2 id="monitoring-rules" className="text-base font-semibold text-fg">
          {t('monitoring.alertRules')}{' '}
          <span className="tabular-nums text-fg-muted">({alerts.data?.length ?? 0})</span>
        </h2>
        {alerts.data?.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('monitoring.alertRules')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('monitoring.ruleName')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('monitoring.metricName')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('monitoring.stagesColumn')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('monitoring.scopeColumn')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('monitoring.actionsColumn')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('monitoring.severity')}
                    </th>
                    <th scope="col" className={`${thClasses} text-right`}>
                      {t('common.actions')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {alerts.data.map((rule) => {
                    const scope = scopeParts(rule, names, t);
                    const actions = [
                      rule.actions?.notify?.enabled !== false
                        ? t('monitoring.rule.actionNotify')
                        : null,
                      rule.actions?.webhook?.enabled !== false
                        ? t('monitoring.rule.actionWebhook')
                        : null,
                    ].filter(Boolean);
                    return (
                      <tr key={rule.id} className={trClasses}>
                        <td className={tdPrimaryClasses}>{rule.name}</td>
                        <td className={tdMonoClasses}>{rule.metric_name}</td>
                        <td className={`${tdClasses} font-mono text-xs`}>
                          <ul>
                            {STAGES.map((stage) => {
                              const c = rule[`${stage}_condition`];
                              return c ? (
                                <li key={stage}>
                                  {stage}: {summarizeCondition(c)}
                                </li>
                              ) : null;
                            })}
                            {rule.clear_condition ? (
                              <li>clear: {summarizeCondition(rule.clear_condition)}</li>
                            ) : null}
                          </ul>
                          <span className="text-fg-muted">({rule.duration})</span>
                        </td>
                        <td className={tdClasses}>
                          {scope.length ? (
                            <ul>
                              {scope.map((part) => (
                                <li key={part}>{part}</li>
                              ))}
                            </ul>
                          ) : (
                            t('monitoring.rule.scopeAll')
                          )}
                        </td>
                        <td className={tdClasses}>{actions.join(', ') || '–'}</td>
                        <td className={tdClasses}>
                          <Badge variant={severityVariants[ruleSeverity(rule)] ?? 'neutral'}>
                            {t(`monitoring.severity_${ruleSeverity(rule)}`)}
                          </Badge>
                        </td>
                        <td className={tdClasses}>
                          <div className="flex justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="sm"
                              icon={<Pencil />}
                              onClick={() => setEditing(rule)}
                              aria-label={`${t('monitoring.rule.edit')}: ${rule.name}`}
                            >
                              {t('monitoring.rule.edit')}
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="text-danger"
                              onClick={() => deleteAlert.mutate(rule.id)}
                              disabled={deleteAlert.isPending}
                            >
                              {t('common.delete')}
                            </Button>
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : (
          <EmptyState
            icon={<BellRing />}
            title={t('monitoring.noAlerts')}
            description={t('monitoring.noAlertsHint')}
          />
        )}
      </section>
    </div>
  );
}
