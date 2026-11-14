import { useMemo } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Check, Minus } from 'lucide-react';
import { ApiError } from '../api/client';
import type { Entitlement, PlanInfo, QuotaUsage } from '../api/client';
import { usePlanOverview } from '../hooks/useEntitlements';
import { Badge } from '../components/ui/Badge';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { ScrollRegion } from '../components/ScrollRegion';

/**
 * PlanPage shows the organization's plan and add-ons (PKG-06, ENT-05,
 * WP-886): the plan with the time of its last change, the plan comparison
 * of the four plans with included and locked features, the booked add-ons
 * with their term, every quota with its use, limit and origin, and the
 * expiry dates of the features. It reads only; a purchase or a plan change
 * by the organization is not part of it (the operator changes plans).
 * Enum values (plan, origin, feature, quota) appear through the i18n
 * catalog only (GLO-10).
 */
export function PlanPage() {
  const { t, i18n } = useTranslation();
  const { rows, usage, plans } = usePlanOverview();
  const formatDate = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: 'long' }),
    [i18n.language],
  );
  const formatNumber = useMemo(() => new Intl.NumberFormat(i18n.language), [i18n.language]);

  const error = rows.error ?? usage.error ?? plans.error;
  if (error) {
    const forbidden = error instanceof ApiError && error.status === 403;
    return (
      <div className="space-y-6">
        <PageHeader title={t('planPage.title')} />
        <ErrorState
          title={forbidden ? t('planPage.forbidden') : t('planPage.loadError')}
          description={forbidden ? t('planPage.forbiddenHint') : undefined}
        />
      </div>
    );
  }
  if (!rows.data || !usage.data || !plans.data) {
    return (
      <div className="space-y-6">
        <PageHeader title={t('planPage.title')} />
        <SkeletonList />
      </div>
    );
  }

  const plan = rows.data.plan || usage.data.plan;
  const changedAt = rows.data.plan_changed_at;
  const entitlements = rows.data.data ?? [];
  const addonRows = new Map(
    entitlements.filter((e) => e.origin === 'addon').map((e) => [e.feature_key, e]),
  );
  const expiring = entitlements.filter((e) => e.origin !== 'addon' && e.valid_until);

  return (
    <div className="space-y-6">
      <PageHeader title={t('planPage.title')} description={t('planPage.subtitle')} />

      <Card title={t('planPage.current.title')}>
        <p className="text-lg font-semibold text-gray-900 dark:text-gray-100">
          {planName(t, plan)}
        </p>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
          {changedAt
            ? t('planPage.current.changedAt', { date: formatDate.format(new Date(changedAt)) })
            : t('planPage.current.sinceCreation')}
        </p>
        <p className="mt-3 text-sm text-gray-600 dark:text-gray-400">
          {t('planPage.current.changeHint')}
        </p>
      </Card>

      <Card title={t('planPage.quotas.title')}>
        <ScrollRegion label={t('planPage.quotas.title')}>
          <table className={tableClasses}>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('planPage.quotas.quota')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('planPage.quotas.usage')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('planPage.quotas.origin')}
                </th>
              </tr>
            </thead>
            <tbody>
              {usage.data.data.map((u) => (
                <tr key={u.quota} className={trClasses}>
                  <td className={tdPrimaryClasses}>{quotaName(t, u.quota)}</td>
                  <td className={tdClasses}>
                    <UsageBar usage={u} t={t} formatNumber={formatNumber} />
                  </td>
                  <td className={tdClasses}>
                    <Badge variant={u.origin === 'override' ? 'info' : 'neutral'}>
                      {t(`planPage.origins.${u.origin}`)}
                    </Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      </Card>

      <Card title={t('planPage.addons.title')}>
        <ul className="divide-y divide-gray-100 dark:divide-gray-800">
          {plans.data.addons.map((addon) => {
            const row = addonRows.get(addon.key);
            return (
              <li key={addon.key} className="flex flex-wrap items-center gap-3 py-2">
                <span className="font-medium text-gray-900 dark:text-gray-100">
                  {featureName(t, addon.key)}
                </span>
                <AddonStatus row={row} t={t} />
                {row?.valid_until && (
                  <span className="text-sm text-gray-600 dark:text-gray-400">
                    {t('planPage.addons.validUntil', {
                      date: formatDate.format(new Date(row.valid_until)),
                    })}
                  </span>
                )}
              </li>
            );
          })}
        </ul>
      </Card>

      <Card title={t('planPage.expiry.title')}>
        {expiring.length === 0 ? (
          <p className="text-sm text-gray-600 dark:text-gray-400">{t('planPage.expiry.none')}</p>
        ) : (
          <ul className="space-y-1 text-sm">
            {expiring.map((e) => (
              <li key={e.feature_key}>
                {t('planPage.expiry.entry', {
                  feature: featureName(t, e.feature_key),
                  date: formatDate.format(new Date(e.valid_until as string)),
                })}
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card title={t('planPage.compare.title')}>
        <PlanComparison plans={plans.data.data} current={plan} t={t} />
      </Card>
    </div>
  );
}

function planName(t: TFunction, plan: string): string {
  return t(`discoverability.plans.${plan}`, { defaultValue: plan });
}

function featureName(t: TFunction, key: string): string {
  return t(`planPage.features.${key}`, { defaultValue: key });
}

/** quotaName names a quota; max_targets_<type> names its probe type. */
function quotaName(t: TFunction, quota: string): string {
  if (quota.startsWith('max_targets_')) {
    return t('planPage.quotaNames.maxTargetsOfType', { type: quota.slice('max_targets_'.length) });
  }
  return t(`planPage.quotaNames.${quota}`, { defaultValue: quota });
}

function AddonStatus({ row, t }: { row?: Entitlement; t: TFunction }) {
  if (!row) return <Badge variant="neutral">{t('planPage.addons.notBooked')}</Badge>;
  if (!row.enabled) return <Badge variant="warning">{t('planPage.addons.cancelled')}</Badge>;
  return <Badge variant="success">{t('planPage.addons.booked')}</Badge>;
}

/**
 * UsageBar shows used against the limit: a bar when both are known, the
 * limit alone when the count is hidden (used null), "unlimited" without a
 * limit. min_probe_interval_s is a lower bound in seconds, no use.
 */
function UsageBar({
  usage,
  t,
  formatNumber,
}: {
  usage: QuotaUsage;
  t: TFunction;
  formatNumber: Intl.NumberFormat;
}) {
  const fmt = (value: number) =>
    usage.quota === 'min_probe_interval_s'
      ? t('planPage.quotas.seconds', { value: formatNumber.format(value) })
      : usage.quota === 'max_storage_mb'
        ? t('planPage.quotas.mib', { value: formatNumber.format(value) })
        : formatNumber.format(value);
  const limit = usage.limit === null ? t('planPage.quotas.unlimited') : fmt(usage.limit);
  if (usage.used === null) {
    return <span>{t('planPage.quotas.limitOnly', { limit })}</span>;
  }
  const text = t('planPage.quotas.usedOf', { used: fmt(usage.used), limit });
  if (usage.limit === null || usage.limit === 0) {
    return <span>{text}</span>;
  }
  const percent = Math.min(100, Math.round((usage.used / usage.limit) * 100));
  return (
    <div className="flex min-w-[12rem] items-center gap-3">
      <div
        role="progressbar"
        aria-label={quotaLabel(t, usage.quota)}
        aria-valuemin={0}
        aria-valuemax={usage.limit}
        aria-valuenow={usage.used}
        aria-valuetext={text}
        className="h-2 flex-1 overflow-hidden rounded bg-gray-200 dark:bg-gray-700"
      >
        <div
          className={
            percent >= 90
              ? 'h-full bg-red-600 dark:bg-red-400'
              : percent >= 75
                ? 'h-full bg-amber-500 dark:bg-amber-400'
                : 'h-full bg-blue-600 dark:bg-blue-400'
          }
          style={{ width: `${percent}%` }}
        />
      </div>
      <span className="whitespace-nowrap text-sm">{text}</span>
    </div>
  );
}

function quotaLabel(t: TFunction, quota: string): string {
  return quotaName(t, quota);
}

/**
 * PlanComparison lists every feature of the largest plan against the four
 * plans; the organization's plan is marked.
 */
function PlanComparison({
  plans,
  current,
  t,
}: {
  plans: PlanInfo[];
  current: string;
  t: TFunction;
}) {
  const features: string[] = [];
  for (const p of plans) {
    for (const f of p.features) if (!features.includes(f)) features.push(f);
  }
  const cell = (included: boolean): ReactNode =>
    included ? (
      <span className="inline-flex items-center gap-1 text-green-700 dark:text-green-300">
        <Check aria-hidden="true" className="h-4 w-4" />
        <span className="sr-only">{t('planPage.compare.included')}</span>
      </span>
    ) : (
      <span className="inline-flex items-center gap-1 text-muted">
        <Minus aria-hidden="true" className="h-4 w-4" />
        <span className="sr-only">{t('planPage.compare.locked')}</span>
      </span>
    );
  return (
    <ScrollRegion label={t('planPage.compare.title')}>
      <table className={tableClasses}>
        <thead className={theadClasses}>
          <tr>
            <th scope="col" className={thClasses}>
              {t('planPage.compare.feature')}
            </th>
            {plans.map((p) => (
              <th key={p.plan} scope="col" className={thClasses}>
                <span className="inline-flex items-center gap-2">
                  {planName(t, p.plan)}
                  {p.plan === current && (
                    <Badge variant="info">{t('planPage.compare.yourPlan')}</Badge>
                  )}
                </span>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {features.map((f) => (
            <tr key={f} className={trClasses}>
              <th scope="row" className={tdPrimaryClasses}>
                {featureName(t, f)}
              </th>
              {plans.map((p) => (
                <td key={p.plan} className={tdClasses}>
                  {cell(p.features.includes(f))}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </ScrollRegion>
  );
}
