import type * as React from 'react';
import { useTranslation } from 'react-i18next';
import {
  useAssetList,
  useCIList,
  useCollectors,
  useComplianceScore,
  useSLABreaches,
  useStocktakeList,
  useTicketList,
} from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Select } from '../components/ui/Select';
import { useDashboardStore } from '../stores/dashboard';
import type { DashboardRole, DashboardWidget } from '../stores/dashboard';

const allWidgets: DashboardWidget[] = [
  'ciSummary',
  'collectors',
  'slaBreaches',
  'compliance',
  'assets',
  'stocktake',
  'tickets',
];

function MetricCard({
  label,
  value,
  isLoading,
}: {
  label: string;
  value?: number;
  isLoading: boolean;
}) {
  const { t } = useTranslation();
  return (
    <Card title={label}>
      <p className="text-3xl font-semibold text-gray-900 dark:text-gray-100">
        {isLoading ? t('app.loading') : (value ?? 0)}
      </p>
    </Card>
  );
}

function CISummaryWidget() {
  const { t } = useTranslation();
  const totalCIs = useCIList({ limit: 1, offset: 0 });
  const activeCIs = useCIList({ limit: 1, offset: 0, status: 'active' });
  const maintenanceCIs = useCIList({ limit: 1, offset: 0, status: 'maintenance' });

  return (
    <>
      <MetricCard
        label={t('dashboard.totalCIs')}
        value={totalCIs.data?.total}
        isLoading={totalCIs.isLoading}
      />
      <MetricCard
        label={t('dashboard.activeCIs')}
        value={activeCIs.data?.total}
        isLoading={activeCIs.isLoading}
      />
      <MetricCard
        label={t('dashboard.maintenanceCIs')}
        value={maintenanceCIs.data?.total}
        isLoading={maintenanceCIs.isLoading}
      />
    </>
  );
}

function CollectorsWidget() {
  const { t } = useTranslation();
  const collectors = useCollectors({ limit: 1000, offset: 0 });

  // Guard against a malformed/legacy payload where `data` is null instead of
  // an array — `.filter` on null throws during render and blanks the page.
  const collectorList = collectors.data?.data ?? [];
  const collectorCount = collectorList.filter((collector) => collector.status === 'online').length;

  return (
    <MetricCard
      label={t('dashboard.collectorsOnline')}
      value={collectorCount}
      isLoading={collectors.isLoading}
    />
  );
}

function SLABreachesWidget() {
  const { t } = useTranslation();
  const breaches = useSLABreaches({ limit: 1 });

  return (
    <MetricCard
      label={t('dashboard.widgets.slaBreaches')}
      value={breaches.data?.total}
      isLoading={breaches.isLoading}
    />
  );
}

function ComplianceWidget() {
  const { t } = useTranslation();
  const score = useComplianceScore();

  return (
    <Card title={t('dashboard.widgets.compliance')}>
      {score.isLoading ? (
        <p className="text-3xl font-semibold text-gray-900 dark:text-gray-100">
          {t('app.loading')}
        </p>
      ) : (
        <p className="flex items-center gap-2 text-3xl font-semibold text-gray-900 dark:text-gray-100">
          {score.data ? `${Math.round(score.data.overall.score)} %` : '—'}
          {score.data ? (
            <Badge variant={score.data.overall.score >= 80 ? 'success' : 'warning'}>
              {t('compliance.scoreHint', {
                passed: score.data.overall.passed,
                failed: score.data.overall.failed,
              })}
            </Badge>
          ) : null}
        </p>
      )}
    </Card>
  );
}

function AssetsWidget() {
  const { t } = useTranslation();
  const total = useAssetList({ limit: 1, offset: 0 });
  const assigned = useAssetList({ limit: 1, offset: 0, status: 'assigned' });

  return (
    <>
      <MetricCard
        label={t('dashboard.widgets.assetsTotal')}
        value={total.data?.total}
        isLoading={total.isLoading}
      />
      <MetricCard
        label={t('dashboard.widgets.assetsAssigned')}
        value={assigned.data?.total}
        isLoading={assigned.isLoading}
      />
    </>
  );
}

function StocktakeWidget() {
  const { t } = useTranslation();
  const open = useStocktakeList({ status: 'in_progress', limit: 1 });

  return (
    <MetricCard
      label={t('dashboard.widgets.stocktakeOpen')}
      value={open.data?.total}
      isLoading={open.isLoading}
    />
  );
}

function TicketsWidget() {
  const { t } = useTranslation();
  const openTickets = useTicketList({ status: 'open', limit: 1 });
  const critical = useTicketList({ status: 'open', priority: 'critical', limit: 1 });

  return (
    <>
      <MetricCard
        label={t('dashboard.widgets.openTickets')}
        value={openTickets.data?.total}
        isLoading={openTickets.isLoading}
      />
      <MetricCard
        label={t('dashboard.widgets.criticalTickets')}
        value={critical.data?.total}
        isLoading={critical.isLoading}
      />
    </>
  );
}

const widgetRenderers: Record<DashboardWidget, () => React.JSX.Element> = {
  ciSummary: CISummaryWidget,
  collectors: CollectorsWidget,
  slaBreaches: SLABreachesWidget,
  compliance: ComplianceWidget,
  assets: AssetsWidget,
  stocktake: StocktakeWidget,
  tickets: TicketsWidget,
};

export function DashboardPage() {
  const { t } = useTranslation();
  const role = useDashboardStore((state) => state.role);
  const setRole = useDashboardStore((state) => state.setRole);
  const hidden = useDashboardStore((state) => state.hidden);
  const toggleWidget = useDashboardStore((state) => state.toggleWidget);
  const isVisible = useDashboardStore((state) => state.isVisible);

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.dashboard')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t('dashboard.summary')} {t('dashboard.roleHint')}
          </p>
        </div>
        <Select
          value={role}
          onChange={(event) => setRole(event.target.value as DashboardRole)}
          aria-label={t('dashboard.roleLabel')}
          className="max-w-xs"
          options={(['technik', 'einkauf', 'management'] as const).map((value) => ({
            value,
            label: t(`dashboard.roles.${value}`),
          }))}
        />
      </div>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
        {allWidgets.map((widget) => {
          if (!isVisible(widget)) {
            return null;
          }
          const Widget = widgetRenderers[widget];
          return <Widget key={widget} />;
        })}
      </div>

      <Card title={t('dashboard.customize')}>
        <div className="flex flex-wrap gap-2">
          {allWidgets.map((widget) => {
            const active = !hidden.includes(widget);
            return (
              <Button
                key={widget}
                variant={active ? 'secondary' : 'ghost'}
                size="sm"
                aria-pressed={active}
                onClick={() => toggleWidget(widget)}
              >
                {t(`dashboard.widgets.${widget}`)}
              </Button>
            );
          })}
        </div>
      </Card>
    </div>
  );
}
