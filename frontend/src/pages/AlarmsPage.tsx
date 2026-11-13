import { BellRing, X } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { alarmApi } from '../api/client';
import type { Alarm, AlarmListParams } from '../api/client';
import { clientApi } from '../api/cmdb';
import { useAlarm, useAlarms, useHasPermission } from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
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

/** The states, severities and suppression filters of the alarm list (ALR-01). */
export const ALARM_STATES = ['pending', 'open', 'acknowledged', 'resolved'] as const;
export const ALARM_SEVERITIES = ['critical', 'warning', 'info'] as const;
export const ALARM_SUPPRESSION_FILTERS = ['hide', 'show', 'only'] as const;
export const ALARM_SUPPRESSED_REASONS = ['maintenance', 'symptom', 'flapping'] as const;

/** The default states of the list: the alarms that need attention. */
const DEFAULT_STATES = 'open,acknowledged';
const ACK_PERMISSION = 'alert:acknowledge';

function severityVariant(severity: string): 'danger' | 'warning' | 'info' | 'neutral' {
  switch (severity) {
    case 'critical':
      return 'danger';
    case 'warning':
      return 'warning';
    case 'info':
      return 'info';
    default:
      return 'neutral';
  }
}

function stateVariant(state: string): 'danger' | 'warning' | 'success' | 'neutral' | 'info' {
  switch (state) {
    case 'open':
      return 'danger';
    case 'acknowledged':
      return 'info';
    case 'pending':
      return 'warning';
    case 'resolved':
      return 'success';
    default:
      return 'neutral';
  }
}

function formatTime(value: string | null | undefined): string {
  return value ? new Date(value).toLocaleString() : '–';
}

/** listParams reads the filters of the URL (?state=…&severity=…&client_id=…&ci_id=…&suppressed=…). */
function listParams(search: URLSearchParams): Omit<AlarmListParams, 'cursor'> {
  const params: Omit<AlarmListParams, 'cursor'> = {
    state: search.get('state') ?? DEFAULT_STATES,
    severity: search.get('severity') || undefined,
    client_id: search.get('client_id') || undefined,
    ci_id: search.get('ci_id') || undefined,
    incident_id: search.get('incident_id') || undefined,
    limit: 50,
  };
  const suppressed = search.get('suppressed') ?? 'hide';
  if (suppressed === 'hide') params.suppressed = false;
  if (suppressed === 'only') params.suppressed = true;
  return params;
}

/**
 * AlarmDrawer shows one alarm (WP-991): its header data, details and the
 * timeline of its transitions in time order, with Ack and Un-Ack and a
 * comment. ?id=<alarm_id> opens it directly (the link of the notification,
 * WP-987); closing it returns the focus to the row that opened it.
 */
function AlarmDrawer({
  id,
  canAck,
  onClose,
}: {
  id: string;
  canAck: boolean | undefined;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { data: alarm, isLoading, error, refetch } = useAlarm(id);
  const [comment, setComment] = useState('');
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  // details are the technical data of the source (value, threshold,
  // rule or probe), shown as JSON.
  const detailsText = useMemo(() => JSON.stringify(alarm?.details ?? {}, null, 2), [alarm]);
  // The server returns the transitions in order (at, id); a client-side
  // sort by the RFC 3339 text would swap changes within one second, as
  // Go trims trailing zeros of the fraction.
  const transitions = alarm?.transitions ?? [];

  const run = async (action: 'ack' | 'unack') => {
    setBusy(true);
    setActionError(null);
    try {
      if (action === 'ack') await alarmApi.acknowledge(id, comment.trim() || undefined);
      else await alarmApi.unacknowledge(id, comment.trim() || undefined);
      setComment('');
      await queryClient.invalidateQueries({ queryKey: ['alarm', id] });
      await queryClient.invalidateQueries({ queryKey: ['alarms'] });
    } catch (err) {
      setActionError(err instanceof Error ? err.message : t('app.error'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onOpenChange={(open) => !open && onClose()}
      title={alarm?.summary || t('alarms.detailTitle')}
      size="lg"
    >
      {isLoading ? (
        <SkeletonList rows={3} label={t('app.loading')} />
      ) : error || !alarm ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : (
        <div className="space-y-5" data-testid="alarm-drawer">
          <dl className="grid grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
            <div>
              <dt className="text-fg-muted">{t('alarms.columns.severity')}</dt>
              <dd>
                <Badge variant={severityVariant(alarm.severity)}>
                  {t(`alarms.severities.${alarm.severity}`)}
                </Badge>
              </dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('alarms.columns.state')}</dt>
              <dd>
                <Badge variant={stateVariant(alarm.state)}>
                  {t(`alarms.states.${alarm.state}`)}
                </Badge>
              </dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('alarms.columns.ci')}</dt>
              <dd>
                {alarm.ci_id ? (
                  <Link className="text-accent underline" to={`/cmdb/${alarm.ci_id}`}>
                    {alarm.ci_id}
                  </Link>
                ) : (
                  '–'
                )}
              </dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('alarms.columns.suppressed')}</dt>
              <dd>
                {alarm.suppressed_reason
                  ? t(`alarms.suppressedReasons.${alarm.suppressed_reason}`)
                  : '–'}
              </dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('alarms.columns.count')}</dt>
              <dd className="tabular-nums">{alarm.occurrence_count}</dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('alarms.columns.opened')}</dt>
              <dd className="tabular-nums">{formatTime(alarm.opened_at)}</dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('alarms.source')}</dt>
              <dd className="font-mono text-xs">{alarm.dedup_key}</dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('alarms.resolvedAt')}</dt>
              <dd className="tabular-nums">{formatTime(alarm.resolved_at)}</dd>
            </div>
          </dl>

          <section>
            <h3 className="mb-2 text-sm font-semibold text-fg">{t('alarms.details')}</h3>
            <pre className="max-h-48 overflow-auto rounded-lg border border-line bg-canvas p-3 font-mono text-xs text-fg">
              {detailsText}
            </pre>
          </section>

          <section>
            <h3 className="mb-2 text-sm font-semibold text-fg">{t('alarms.timeline')}</h3>
            <ol className="space-y-2" aria-label={t('alarms.timeline')}>
              {transitions.map((tr) => (
                <li
                  key={tr.id}
                  data-testid="alarm-transition"
                  className="rounded-lg border border-line px-3 py-2 text-sm"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="tabular-nums text-fg-muted">{formatTime(tr.at)}</span>
                    <span>
                      {tr.from_state ? t(`alarms.states.${tr.from_state}`) : t('alarms.new')} →{' '}
                      {t(`alarms.states.${tr.to_state}`)}
                    </span>
                    {tr.to_severity && tr.from_severity !== tr.to_severity ? (
                      <Badge variant={severityVariant(tr.to_severity)}>
                        {t(`alarms.severities.${tr.to_severity}`)}
                      </Badge>
                    ) : null}
                  </div>
                  <div className="text-fg-muted">
                    {t('alarms.actor')}: {tr.actor_id ?? t('alarms.system')} · {t('alarms.reason')}:{' '}
                    {tr.reason}
                    {tr.comment ? ` · ${tr.comment}` : ''}
                  </div>
                </li>
              ))}
            </ol>
            {alarm.transitions_truncated ? (
              <p className="mt-2 text-xs text-fg-muted">{t('alarms.truncated')}</p>
            ) : null}
          </section>

          <section className="space-y-2">
            <Input
              label={t('alarms.comment')}
              value={comment}
              disabled={!canAck}
              onChange={(e) => setComment(e.target.value)}
            />
            {!canAck ? (
              <p className="text-xs text-fg-muted">{t('alarms.noAckPermission')}</p>
            ) : null}
            {actionError ? (
              <p role="alert" className="text-sm text-red-600 dark:text-red-400">
                {actionError}
              </p>
            ) : null}
            <div className="flex gap-2">
              <Button
                disabled={!canAck || busy || alarm.state !== 'open'}
                onClick={() => void run('ack')}
              >
                {t('alarms.ack')}
              </Button>
              <Button
                variant="secondary"
                disabled={!canAck || busy || alarm.state !== 'acknowledged'}
                onClick={() => void run('unack')}
              >
                {t('alarms.unack')}
              </Button>
            </div>
          </section>
        </div>
      )}
    </Modal>
  );
}

/**
 * AlarmsPage is the alarm list (ALR-01, ALR-10, WP-991): filters in the URL,
 * cursor paging, bulk Ack over the selected rows and the detail drawer.
 */
export function AlarmsPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [search, setSearch] = useSearchParams();
  const params = listParams(search);
  const openId = search.get('id') ?? undefined;
  const canAck = useHasPermission(ACK_PERMISSION);
  const alarms = useAlarms(params);
  const clients = useQuery({
    queryKey: ['scope', 'clients'],
    queryFn: () => clientApi.list(),
    retry: false,
    staleTime: 60_000,
  });
  const [selected, setSelected] = useState<string[]>([]);
  const [bulkComment, setBulkComment] = useState('');
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkResult, setBulkResult] = useState<string | null>(null);

  const rows: Alarm[] = useMemo(
    () => alarms.data?.pages.flatMap((p) => p.data) ?? [],
    [alarms.data],
  );
  const rowIds = useMemo(() => rows.map((a) => a.id), [rows]);

  const setParam = (key: string, value: string | undefined) => {
    const next = new URLSearchParams(search);
    if (value === undefined || value === '') next.delete(key);
    else next.set(key, value);
    setSearch(next);
    setSelected([]);
  };
  const openAlarm = (id: string) => {
    const next = new URLSearchParams(search);
    next.set('id', id);
    setSearch(next);
  };
  const closeAlarm = () => {
    const next = new URLSearchParams(search);
    next.delete('id');
    setSearch(next);
  };
  // Only an open alarm can be acknowledged (the API answers 409 for the
  // others), so only open rows are selectable.
  const ackable = useMemo(
    () => new Set(rows.filter((a) => a.state === 'open').map((a) => a.id)),
    [rows],
  );
  const toggle = (id: string) => {
    if (!canAck || !ackable.has(id)) return;
    setSelected((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  };
  const { getRowProps } = useRowNavigation({ rowIds, onOpen: openAlarm, onToggle: toggle });

  const bulkAck = async () => {
    setBulkBusy(true);
    let ok = 0;
    let failed = 0;
    for (const id of selected) {
      try {
        await alarmApi.acknowledge(id, bulkComment.trim() || undefined);
        ok++;
      } catch {
        failed++;
      }
    }
    setBulkResult(t('alarms.bulkResult', { ok, failed }));
    setSelected([]);
    setBulkComment('');
    setBulkBusy(false);
    await queryClient.invalidateQueries({ queryKey: ['alarms'] });
  };

  const stateValue = search.get('state') ?? DEFAULT_STATES;
  const stateOptions = [
    { value: DEFAULT_STATES, label: t('alarms.stateFilter.active') },
    ...ALARM_STATES.map((s) => ({ value: s, label: t(`alarms.states.${s}`) })),
  ];

  return (
    <div className="space-y-4">
      <PageHeader title={t('alarms.title')} description={t('alarms.description')} />

      <Card className="space-y-3 p-4">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Select
            label={t('alarms.filters.state')}
            value={stateOptions.some((o) => o.value === stateValue) ? stateValue : DEFAULT_STATES}
            options={stateOptions}
            onChange={(e) => setParam('state', e.target.value)}
          />
          <Select
            label={t('alarms.filters.severity')}
            value={search.get('severity') ?? ''}
            options={[
              { value: '', label: t('alarms.filters.all') },
              ...ALARM_SEVERITIES.map((s) => ({ value: s, label: t(`alarms.severities.${s}`) })),
            ]}
            onChange={(e) => setParam('severity', e.target.value)}
          />
          <Select
            label={t('alarms.filters.client')}
            value={search.get('client_id') ?? ''}
            options={[
              { value: '', label: t('alarms.filters.all') },
              ...(clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name })),
            ]}
            onChange={(e) => setParam('client_id', e.target.value)}
          />
          <Select
            label={t('alarms.filters.suppressed')}
            value={search.get('suppressed') ?? 'hide'}
            options={ALARM_SUPPRESSION_FILTERS.map((s) => ({
              value: s,
              label: t(`alarms.suppression.${s}`),
            }))}
            onChange={(e) => setParam('suppressed', e.target.value)}
          />
        </div>
        {params.ci_id || params.incident_id ? (
          <div className="flex flex-wrap gap-2">
            {params.ci_id ? (
              <Button variant="secondary" size="sm" onClick={() => setParam('ci_id', undefined)}>
                {t('alarms.filters.ci')}: {params.ci_id} <X className="h-3.5 w-3.5" aria-hidden />
              </Button>
            ) : null}
            {params.incident_id ? (
              <Button
                variant="secondary"
                size="sm"
                onClick={() => setParam('incident_id', undefined)}
              >
                {t('alarms.filters.incident')}: {params.incident_id}{' '}
                <X className="h-3.5 w-3.5" aria-hidden />
              </Button>
            ) : null}
          </div>
        ) : null}
        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-[16rem] flex-1">
            <Input
              label={t('alarms.bulkComment')}
              value={bulkComment}
              disabled={!canAck}
              onChange={(e) => setBulkComment(e.target.value)}
            />
          </div>
          <Button
            disabled={!canAck || selected.length === 0 || bulkBusy}
            onClick={() => void bulkAck()}
          >
            {t('alarms.bulkAck', { count: selected.length })}
          </Button>
        </div>
        {!canAck ? <p className="text-xs text-fg-muted">{t('alarms.noAckPermission')}</p> : null}
        {bulkResult ? (
          <p role="status" className="text-sm text-fg">
            {bulkResult}
          </p>
        ) : null}
      </Card>

      {alarms.isLoading ? (
        <SkeletonList rows={5} label={t('app.loading')} />
      ) : alarms.error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void alarms.refetch()}
        />
      ) : (
        <Card className="p-0">
          <ScrollRegion label={t('alarms.title')}>
            <table className={tableClasses}>
              <caption className="sr-only">{t('alarms.title')}</caption>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    <span className="sr-only">{t('alarms.select')}</span>
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('alarms.columns.severity')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('alarms.columns.ci')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('alarms.columns.summary')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('alarms.columns.state')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('alarms.columns.suppressed')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('alarms.columns.count')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('alarms.columns.opened')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.length === 0 ? (
                  <TableEmptyRow
                    colSpan={8}
                    icon={<BellRing className="h-5 w-5" aria-hidden />}
                    title={t('alarms.empty')}
                    description={t('alarms.emptyDescription')}
                  />
                ) : (
                  rows.map((a) => (
                    <tr
                      key={a.id}
                      data-testid="alarm-row"
                      aria-label={a.summary}
                      aria-selected={selected.includes(a.id)}
                      className={cn(trClasses, 'cursor-pointer focus-visible:outline-2')}
                      onClick={() => openAlarm(a.id)}
                      {...getRowProps(a.id)}
                    >
                      <td className={tdClasses} onClick={(e) => e.stopPropagation()}>
                        <input
                          type="checkbox"
                          tabIndex={-1}
                          aria-label={t('alarms.selectOne', { summary: a.summary })}
                          checked={selected.includes(a.id)}
                          disabled={!canAck || a.state !== 'open'}
                          onChange={() => toggle(a.id)}
                        />
                      </td>
                      <td className={tdClasses}>
                        <Badge variant={severityVariant(a.severity)}>
                          {t(`alarms.severities.${a.severity}`)}
                        </Badge>
                      </td>
                      <td className={cn(tdClasses, 'font-mono text-xs')}>{a.ci_id ?? '–'}</td>
                      <td className={tdPrimaryClasses}>{a.summary}</td>
                      <td className={tdClasses}>
                        <Badge variant={stateVariant(a.state)}>
                          {t(`alarms.states.${a.state}`)}
                        </Badge>
                      </td>
                      <td className={tdClasses}>
                        {a.suppressed_reason
                          ? t(`alarms.suppressedReasons.${a.suppressed_reason}`)
                          : '–'}
                      </td>
                      <td className={cn(tdClasses, 'tabular-nums')}>{a.occurrence_count}</td>
                      <td className={cn(tdClasses, 'tabular-nums')}>{formatTime(a.opened_at)}</td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </ScrollRegion>
          {alarms.hasNextPage ? (
            <div className="border-t border-line p-3">
              <Button
                variant="secondary"
                disabled={alarms.isFetchingNextPage}
                onClick={() => void alarms.fetchNextPage()}
              >
                {t('alarms.loadMore')}
              </Button>
            </div>
          ) : null}
        </Card>
      )}

      {openId ? <AlarmDrawer id={openId} canAck={canAck} onClose={closeAlarm} /> : null}
    </div>
  );
}
