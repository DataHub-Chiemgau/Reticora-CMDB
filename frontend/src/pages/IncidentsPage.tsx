import { Siren } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { API_MAX_LIMIT, INCIDENT_CAUSE_CLASSES, INCIDENT_STATES } from '../api/client';
import type { Incident, IncidentListParams } from '../api/client';
import { useIncidents } from '../api/cmdbHooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { useRowNavigation } from '../components/ui/useRowNavigation';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
import { TableEmptyRow } from './ListParts';

/**
 * The state filter of the list: recent (default) is the open and
 * acknowledged incidents and those resolved in the last 7 days; all is
 * every state.
 */
export const INCIDENT_STATE_FILTERS = ['recent', ...INCIDENT_STATES, 'all'] as const;

/** RECENT_DAYS is how long a resolved incident stays in the default view. */
const RECENT_DAYS = 7;
const DAY_MS = 24 * 60 * 60 * 1000;

function stateVariant(state: string): 'danger' | 'info' | 'success' | 'neutral' {
  switch (state) {
    case 'open':
      return 'danger';
    case 'acknowledged':
      return 'info';
    case 'resolved':
      return 'success';
    default:
      return 'neutral';
  }
}

/** usePageVisible reports whether the tab is visible (Page Visibility API). */
export function usePageVisible(): boolean {
  const [visible, setVisible] = useState(
    () => typeof document === 'undefined' || document.visibilityState !== 'hidden',
  );
  useEffect(() => {
    const update = () => setVisible(document.visibilityState !== 'hidden');
    document.addEventListener('visibilitychange', update);
    return () => document.removeEventListener('visibilitychange', update);
  }, []);
  return visible;
}

/**
 * localDay reads a date input value (YYYY-MM-DD) as the start of that day
 * in the browser's time zone, plus offset days.
 */
function localDay(value: string, offset = 0): string | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return undefined;
  const at = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]) + offset);
  return Number.isNaN(at.getTime()) ? undefined : at.toISOString();
}

/** dayStart is the local start of a date input value as RFC 3339. */
function dayStart(value: string): string | undefined {
  return localDay(value);
}

/** dayEnd is the exclusive end of a date input value: the next local day's start. */
function dayEnd(value: string): string | undefined {
  return localDay(value, 1);
}

/** count reads a counter of affected_counts; absent is no value. */
function count(incident: Incident, key: string): number | undefined {
  const value = incident.affected_counts?.[key];
  return typeof value === 'number' ? value : undefined;
}

/**
 * IncidentsPage is the incident list (ALR-10, WP-1045): state, root CI,
 * cause class and confidence, the affected connections and services, start
 * and duration. Filters stand in the URL; the client comes from the scope
 * switcher (SCOPED_LISTS). The loaded pages refresh every 15 s while the
 * tab is visible; Enter on a row opens the incident.
 */
export function IncidentsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [search, setSearch] = useSearchParams();
  const visible = usePageVisible();
  // The start of the default view, fixed per visit so the query key stays.
  const [recentSince] = useState(() => new Date(Date.now() - RECENT_DAYS * DAY_MS).toISOString());

  const stateFilter = search.get('state') || 'recent';
  const causeClass = search.get('cause_class') || undefined;
  const fromDay = search.get('from') ?? '';
  const toDay = search.get('to') ?? '';
  const base: Omit<IncidentListParams, 'cursor' | 'state'> = {
    cause_class: causeClass,
    from: fromDay ? dayStart(fromDay) : undefined,
    to: toDay ? dayEnd(toDay) : undefined,
    limit: API_MAX_LIMIT,
  };
  const recent = stateFilter === 'recent';
  // Default view: the active incidents, and the ones resolved in the last
  // 7 days whenever they opened (resolved_from applies to resolved_at;
  // from and to of the period filter apply to opened_at in both queries).
  const primary = useIncidents(
    {
      ...base,
      state: recent ? 'open,acknowledged' : stateFilter === 'all' ? undefined : stateFilter,
    },
    { poll: visible },
  );
  const resolved = useIncidents(
    { ...base, state: 'resolved', resolved_from: recentSince },
    { enabled: recent, poll: visible },
  );

  const rows: Incident[] = useMemo(() => {
    const active = primary.data?.pages.flatMap((p) => p.data) ?? [];
    if (!recent) return active;
    return [...active, ...(resolved.data?.pages.flatMap((p) => p.data) ?? [])];
  }, [primary.data, resolved.data, recent]);
  const rowIds = useMemo(() => rows.map((i) => i.id), [rows]);
  const open = (id: string) => void navigate(`/incidents/${id}`);
  const { getRowProps } = useRowNavigation({ rowIds, onOpen: open });

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(search);
    if (value === '') next.delete(key);
    else next.set(key, value);
    setSearch(next);
  };

  const hasMore = primary.hasNextPage || (recent && resolved.hasNextPage);
  const loadMore = () => {
    if (primary.hasNextPage) void primary.fetchNextPage();
    else if (recent && resolved.hasNextPage) void resolved.fetchNextPage();
  };
  const loading = primary.isLoading || (recent && resolved.isLoading);
  const error = primary.error || (recent ? resolved.error : null);

  const duration = (incident: Incident): string => {
    const end = incident.resolved_at ? Date.parse(incident.resolved_at) : Date.now();
    const minutes = Math.max(0, Math.floor((end - Date.parse(incident.opened_at)) / 60_000));
    const days = Math.floor(minutes / 1440);
    const hours = Math.floor((minutes % 1440) / 60);
    if (days > 0) return t('incidents.durationDays', { d: days, h: hours });
    if (hours > 0) return t('incidents.durationHours', { h: hours, m: minutes % 60 });
    return t('incidents.durationMinutes', { m: minutes });
  };
  const number = (value: number | undefined) => (value === undefined ? '–' : String(value));

  return (
    <div className="space-y-4">
      <PageHeader title={t('incidents.title')} description={t('incidents.description')} />

      <Card className="p-4">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Select
            label={t('incidents.filters.state')}
            value={
              (INCIDENT_STATE_FILTERS as readonly string[]).includes(stateFilter)
                ? stateFilter
                : 'recent'
            }
            options={INCIDENT_STATE_FILTERS.map((s) => ({
              value: s,
              label: t(`incidents.stateFilter.${s}`),
            }))}
            onChange={(e) => setParam('state', e.target.value === 'recent' ? '' : e.target.value)}
          />
          <Select
            label={t('incidents.filters.causeClass')}
            value={causeClass ?? ''}
            options={[
              { value: '', label: t('incidents.filters.all') },
              ...INCIDENT_CAUSE_CLASSES.map((c) => ({
                value: c,
                label: t(`incidents.causeClasses.${c}`),
              })),
            ]}
            onChange={(e) => setParam('cause_class', e.target.value)}
          />
          <Input
            type="date"
            label={t('incidents.filters.from')}
            value={fromDay}
            onChange={(e) => setParam('from', e.target.value)}
          />
          <Input
            type="date"
            label={t('incidents.filters.to')}
            value={toDay}
            onChange={(e) => setParam('to', e.target.value)}
          />
        </div>
        <p className="mt-2 text-xs text-fg-muted">{t('incidents.clientHint')}</p>
      </Card>

      {loading ? (
        <SkeletonList rows={5} label={t('app.loading')} />
      ) : error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => {
            void primary.refetch();
            if (recent) void resolved.refetch();
          }}
        />
      ) : (
        <Card className="p-0">
          <ScrollRegion label={t('incidents.title')}>
            <table className={tableClasses}>
              <caption className="sr-only">{t('incidents.title')}</caption>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('incidents.columns.state')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('incidents.columns.root')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('incidents.columns.cause')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('incidents.columns.confidence')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('incidents.columns.connections')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('incidents.columns.services')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('incidents.columns.opened')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('incidents.columns.duration')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.length === 0 ? (
                  <TableEmptyRow
                    colSpan={8}
                    icon={<Siren className="h-5 w-5" aria-hidden />}
                    title={t('incidents.empty')}
                    description={t('incidents.emptyDescription')}
                  />
                ) : (
                  rows.map((i) => {
                    const rootName = i.root.ci_name ?? i.root.ci_id ?? null;
                    return (
                      <tr
                        key={i.id}
                        data-testid="incident-row"
                        aria-label={t('incidents.rowLabel', {
                          root: rootName ?? t('incidents.noRoot'),
                          cause: t(`incidents.causeClasses.${i.cause_class}`),
                        })}
                        className={cn(trClasses, 'cursor-pointer focus-visible:outline-2')}
                        onClick={() => open(i.id)}
                        {...getRowProps(i.id)}
                      >
                        <td className={tdClasses}>
                          <Badge variant={stateVariant(i.state)}>
                            {t(`incidents.states.${i.state}`)}
                          </Badge>
                        </td>
                        <td className={tdPrimaryClasses}>
                          {i.root.ci_id ? (
                            <Link
                              className="text-accent underline"
                              to={`/cmdb/${i.root.ci_id}`}
                              tabIndex={-1}
                              onClick={(e) => e.stopPropagation()}
                            >
                              {rootName}
                            </Link>
                          ) : (
                            t('incidents.noRoot')
                          )}
                        </td>
                        <td className={tdClasses}>
                          {t(`incidents.causeClasses.${i.cause_class}`)}
                        </td>
                        <td className={cn(tdClasses, 'tabular-nums')}>
                          {t('incidents.confidenceValue', { value: i.confidence })}
                        </td>
                        <td className={cn(tdClasses, 'tabular-nums')}>
                          {number(count(i, 'connections'))}
                        </td>
                        <td className={cn(tdClasses, 'tabular-nums')}>
                          {number(count(i, 'services'))}
                        </td>
                        <td className={cn(tdClasses, 'tabular-nums')}>
                          {new Date(i.opened_at).toLocaleString()}
                        </td>
                        <td className={cn(tdClasses, 'tabular-nums')}>{duration(i)}</td>
                      </tr>
                    );
                  })
                )}
              </tbody>
            </table>
          </ScrollRegion>
          {hasMore ? (
            <div className="border-t border-line p-3">
              <Button
                variant="secondary"
                disabled={primary.isFetchingNextPage || resolved.isFetchingNextPage}
                onClick={loadMore}
              >
                {t('incidents.loadMore')}
              </Button>
            </div>
          ) : null}
        </Card>
      )}
    </div>
  );
}
