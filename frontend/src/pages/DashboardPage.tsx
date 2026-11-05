import { ArrowRight, Check, Plus } from 'lucide-react';
import type * as React from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import {
  useAssetList,
  useCIList,
  useAllCollectors,
  useCollectors,
  useComplianceScore,
  useHasPermission,
  useSLABreaches,
  useStocktakeList,
  useTicketList,
} from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { MetricCard } from '../components/ui/MetricCard';
import { PageHeader } from '../components/ui/PageHeader';
import { ErrorState } from '../components/ui/ErrorState';
import { Select } from '../components/ui/Select';
import { useEntitlements, useModules } from '../hooks/useEntitlements';
import { useEscalatedReviewCount } from '../hooks/useReviews';
import { DASHBOARD_ROLES, useDashboardStore } from '../stores/dashboard';
import type { DashboardRole, DashboardWidget } from '../stores/dashboard';

const allWidgets: DashboardWidget[] = [
  'ciSummary',
  'collectors',
  'reviewEscalation',
  'slaBreaches',
  'compliance',
  'assets',
  'stocktake',
  'tickets',
];

// The widgets that read a late module (WP-214); without it they are hidden.
const widgetModule: Partial<Record<DashboardWidget, string>> = {
  slaBreaches: 'ticketing',
  compliance: 'compliance',
  assets: 'inventory',
  stocktake: 'stocktake',
  tickets: 'ticketing',
};

// The feature key a widget needs besides its module (WP-705, ENT-06): without
// the entitlement the widget is hidden instead of showing a 403.
const widgetFeature: Partial<Record<DashboardWidget, string>> = {
  assets: 'assets_basic',
  stocktake: 'stocktake_standard',
  tickets: 'ticketing_essential',
  slaBreaches: 'ticketing_standard',
  compliance: 'compliance',
};

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
        loading={totalCIs.isLoading}
        to="/cmdb"
      />
      <MetricCard
        label={t('dashboard.activeCIs')}
        value={activeCIs.data?.total}
        loading={activeCIs.isLoading}
        to="/cmdb?status=active"
      />
      <MetricCard
        label={t('dashboard.maintenanceCIs')}
        value={maintenanceCIs.data?.total}
        loading={maintenanceCIs.isLoading}
        to="/cmdb?status=maintenance"
      />
    </>
  );
}

function CollectorsWidget() {
  const { t } = useTranslation();
  // Every page of the list: the API answers at most 200 per page (F-362).
  const collectors = useAllCollectors();

  const collectorList = collectors.data ?? [];
  const collectorCount = collectorList.filter((collector) => collector.status === 'active').length;

  return (
    <MetricCard
      label={t('dashboard.collectorsOnline')}
      value={collectorCount}
      loading={collectors.isLoading}
      to="/discovery"
    />
  );
}

/**
 * ReviewEscalationWidget counts the review items open longer than 14 days
 * (REC-08, F-247) and leads to the inbox filtered on them. Without
 * discovery:read (GET /review-items) it is not shown and asks nothing.
 */
function ReviewEscalationWidget() {
  const { t } = useTranslation();
  const canRead = useHasPermission('discovery:read');
  const escalated = useEscalatedReviewCount(canRead === true);
  if (canRead === false) {
    return null;
  }
  const count = escalated.data;
  const label = t('dashboard.widgets.reviewEscalation');

  if (escalated.error) {
    return (
      <MetricCard label={label} value="—">
        <ErrorState
          title={t('dashboard.reviewEscalation.loadError')}
          description={escalated.error instanceof Error ? escalated.error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void escalated.refetch()}
        />
      </MetricCard>
    );
  }
  return (
    <MetricCard
      label={label}
      value={count}
      loading={count === undefined}
      tone={count !== undefined && count > 0 ? 'warning' : 'default'}
      hint={
        count === undefined
          ? undefined
          : count > 0
            ? t('dashboard.reviewEscalation.hint')
            : t('dashboard.reviewEscalation.none')
      }
    >
      {count !== undefined && count > 0 ? (
        <Link
          to="/reviews?escalated=true"
          className="inline-flex items-center gap-1 text-sm font-medium text-accent hover:underline"
        >
          {t('dashboard.reviewEscalation.open')}
          <ArrowRight aria-hidden="true" className="h-3.5 w-3.5" />
        </Link>
      ) : null}
    </MetricCard>
  );
}

function SLABreachesWidget() {
  const { t } = useTranslation();
  const breaches = useSLABreaches({ limit: 1 });
  const total = breaches.data?.total;

  return (
    <MetricCard
      label={t('dashboard.widgets.slaBreaches')}
      value={total}
      loading={breaches.isLoading}
      tone={total !== undefined && total > 0 ? 'danger' : 'default'}
      to="/slas"
    />
  );
}

function ComplianceWidget() {
  const { t } = useTranslation();
  const score = useComplianceScore();

  return (
    <MetricCard
      label={t('dashboard.widgets.compliance')}
      value={score.data ? `${Math.round(score.data.overall.score)} %` : '—'}
      loading={score.isLoading}
      to="/compliance"
      badge={
        score.data ? (
          <Badge variant={score.data.overall.score >= 80 ? 'success' : 'warning'}>
            {t('compliance.scoreHint', {
              passed: score.data.overall.passed,
              failed: score.data.overall.failed,
            })}
          </Badge>
        ) : null
      }
    />
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
        loading={total.isLoading}
        to="/assets"
      />
      <MetricCard
        label={t('dashboard.widgets.assetsAssigned')}
        value={assigned.data?.total}
        loading={assigned.isLoading}
        to="/assignments"
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
      loading={open.isLoading}
      to="/stocktake"
    />
  );
}

function TicketsWidget() {
  const { t } = useTranslation();
  const openTickets = useTicketList({ status: 'open', limit: 1 });
  const critical = useTicketList({ status: 'open', priority: 'critical', limit: 1 });
  const criticalTotal = critical.data?.total;

  return (
    <>
      <MetricCard
        label={t('dashboard.widgets.openTickets')}
        value={openTickets.data?.total}
        loading={openTickets.isLoading}
        to="/tickets"
      />
      <MetricCard
        label={t('dashboard.widgets.criticalTickets')}
        value={criticalTotal}
        loading={critical.isLoading}
        tone={criticalTotal !== undefined && criticalTotal > 0 ? 'danger' : 'default'}
        to="/tickets"
      />
    </>
  );
}

/**
 * SetupBanner leads back to the onboarding until it is finished: without a
 * collector or without CIs (UI-17, F-351). The queries are the widgets' own.
 */
function SetupBanner() {
  const { t } = useTranslation();
  const collectors = useCollectors({ limit: 1, offset: 0 });
  const cis = useCIList({ limit: 1, offset: 0 });
  if (!collectors.isSuccess || !cis.isSuccess) {
    return null;
  }
  if ((collectors.data?.total ?? 0) > 0 && (cis.data?.total ?? 0) > 0) {
    return null;
  }
  return (
    <section
      aria-labelledby="dashboard-setup-title"
      className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-accent/30 bg-accent-soft px-4 py-3 text-sm"
    >
      <div>
        <h3 id="dashboard-setup-title" className="font-semibold text-fg">
          {t('dashboard.setup.title')}
        </h3>
        <p className="mt-0.5 text-fg-muted">{t('dashboard.setup.text')}</p>
      </div>
      <Button asChild size="sm">
        <Link to="/onboarding">{t('dashboard.setup.continue')}</Link>
      </Button>
    </section>
  );
}

const widgetRenderers: Record<DashboardWidget, () => React.JSX.Element | null> = {
  ciSummary: CISummaryWidget,
  collectors: CollectorsWidget,
  reviewEscalation: ReviewEscalationWidget,
  slaBreaches: SLABreachesWidget,
  compliance: ComplianceWidget,
  assets: AssetsWidget,
  stocktake: StocktakeWidget,
  tickets: TicketsWidget,
};

/**
 * The sections the widgets are grouped in: what the estate looks like,
 * what needs a decision, what runs day to day. A section without a visible
 * widget is not shown.
 */
const sections: { id: string; widgets: DashboardWidget[] }[] = [
  { id: 'infrastructure', widgets: ['ciSummary', 'collectors'] },
  { id: 'attention', widgets: ['reviewEscalation', 'slaBreaches', 'compliance'] },
  { id: 'operations', widgets: ['assets', 'stocktake', 'tickets'] },
];

export function DashboardPage() {
  const { t } = useTranslation();
  const role = useDashboardStore((state) => state.role);
  const setRole = useDashboardStore((state) => state.setRole);
  const hidden = useDashboardStore((state) => state.hidden);
  const toggleWidget = useDashboardStore((state) => state.toggleWidget);
  const isVisible = useDashboardStore((state) => state.isVisible);
  const { isModuleEnabled } = useModules();
  const { isEnabled } = useEntitlements();
  const canReadReviews = useHasPermission('discovery:read');
  const widgets = allWidgets.filter((widget) => {
    const module = widgetModule[widget];
    return (
      (!module || isModuleEnabled(module)) &&
      isEnabled(widgetFeature[widget]) &&
      // The escalation widget needs discovery:read and is not rendered without it.
      (widget !== 'reviewEscalation' || canReadReviews !== false)
    );
  });

  return (
    <div className="space-y-8">
      <PageHeader
        title={t('nav.dashboard')}
        description={`${t('dashboard.summary')} ${t('dashboard.roleHint')}`}
        actions={
          <Select
            value={role}
            onChange={(event) => setRole(event.target.value as DashboardRole)}
            aria-label={t('dashboard.roleLabel')}
            className="w-44"
            options={DASHBOARD_ROLES.map((value) => ({
              value,
              label: t(`dashboard.roles.${value}`),
            }))}
          />
        }
      />

      <SetupBanner />

      {sections.map((section) => {
        const shown = section.widgets.filter(
          (widget) => widgets.includes(widget) && isVisible(widget),
        );
        if (shown.length === 0) {
          return null;
        }
        return (
          <div key={section.id} role="group" aria-labelledby={`dashboard-section-${section.id}`}>
            <h2
              id={`dashboard-section-${section.id}`}
              className="mb-3 text-sm font-semibold text-fg"
            >
              {t(`dashboard.sections.${section.id}`)}
            </h2>
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
              {shown.map((widget) => {
                const Widget = widgetRenderers[widget];
                return <Widget key={widget} />;
              })}
            </div>
          </div>
        );
      })}

      <section aria-labelledby="dashboard-customize" className="border-t border-line pt-5">
        <h2 id="dashboard-customize" className="mb-3 text-sm font-semibold text-fg">
          {t('dashboard.customize')}
        </h2>
        <div className="flex flex-wrap gap-2">
          {widgets.map((widget) => {
            const active = !hidden.includes(widget);
            return (
              <Button
                key={widget}
                variant={active ? 'secondary' : 'ghost'}
                size="sm"
                aria-pressed={active}
                icon={active ? <Check /> : <Plus />}
                onClick={() => toggleWidget(widget)}
              >
                {t(`dashboard.widgets.${widget}`)}
              </Button>
            );
          })}
        </div>
      </section>
    </div>
  );
}
