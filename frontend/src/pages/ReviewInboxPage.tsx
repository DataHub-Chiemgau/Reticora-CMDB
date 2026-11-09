/**
 * ReviewInboxPage — the review inbox of reconciliation (UI-16, REC-08):
 * filter by status, type and escalation; a diff of the observation against
 * the CI it concerns; resolution of one item (merge, new, dismiss, accept)
 * or of several items with one action. New of a device of an unknown type
 * asks for the type of the CI; for a device recorded as generic_device the
 * inbox shows the next step instead (F-176). The inbox pages through the
 * items, shows how many match and selects all of them for a bulk action,
 * not only the page shown (F-345); merge candidates are named by their CI
 * (F-346). Without review:resolve it offers no resolution (RBA-01). The
 * refusal of a single resolution (e.g. the 409 of an item another session
 * resolved meanwhile) stays visible on the page although the refetched list
 * no longer holds the item and its detail closes (WP-557). The escalation
 * filter lives in the URL (?escalated=true), so the link of the dashboard
 * widget opens the inbox on the escalated items (REC-08, F-247). A
 * duplicate_ip item of duplicate observations shows its address, VRF and
 * every side of payload.holders; accept names the stale side as ci_id and
 * is no bulk action (NET-06, E-53, WP-773). With the module ticketing, the
 * feature ticketing_essential and the rights ticket:write and
 * review:resolve the inbox opens a ticket for one item or for the selected
 * open items (WP-291, WP-814); that is no resolution, the item stays open,
 * and an item with a ticket links to it.
 */
import { ChevronLeft, ChevronRight, Inbox, MousePointerClick, Ticket } from 'lucide-react';
import { memo, useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router-dom';
import {
  BULK_TICKET_LIMIT,
  useBulkCreateTicketsFromReviews,
  useCI,
  useCreateTicketFromReview,
  useHasPermission,
  useMountedCIs,
  type BulkTicketResult,
} from '../api/hooks';
import { useEntitlements, useModules } from '../hooks/useEntitlements';
import { useCITypes } from '../api/cmdbHooks';
import { localizedText } from '../api/cmdb';
import { ApiError, type CI } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trInteractiveClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { useRowNavigation, type RowNavigationProps } from '../components/ui/useRowNavigation';
import { ScrollRegion } from '../components/ScrollRegion';
import {
  REVIEW_KINDS,
  REVIEW_PAGE_SIZE,
  fetchAllReviewItems,
  needsCIType,
  reviewActions,
  useBulkResolveReviews,
  useResolveReview,
  useReviewItems,
  type BulkResult,
  type ReviewAction,
  type ReviewItem,
  type ReviewResolution,
} from '../hooks/useReviews';

const STATUSES = ['open', 'resolved', 'dismissed'] as const;

// Payload keys that describe the item rather than the observed device.
const META_KEYS = new Set(['reason', 'criterion', 'ci_id', 'fingerprint', 'source', 'raw_data']);

export interface DiffRow {
  field: string;
  observed: string;
  current: string;
  differs: boolean;
}

function display(value: unknown): string {
  if (value === undefined || value === null || value === '') return '';
  return typeof value === 'object' ? JSON.stringify(value) : String(value);
}

function currentValue(ci: CI | undefined, field: string): unknown {
  if (!ci) return undefined;
  const top = (ci as unknown as Record<string, unknown>)[field];
  return top !== undefined ? top : ci.attributes?.[field];
}

/**
 * reviewDiff compares the observation of an item with the CI it concerns:
 * one row per observed field (a value conflict names its field), the
 * current value of the CI next to it.
 */
export function reviewDiff(item: ReviewItem, ci: CI | undefined): DiffRow[] {
  const payload = item.payload ?? {};
  const observed: [string, unknown][] =
    typeof payload.field === 'string'
      ? [[payload.field, payload.observed_value]]
      : Object.entries(payload).filter(
          ([key, value]) =>
            !META_KEYS.has(key) &&
            (value === null || typeof value !== 'object') &&
            display(value) !== '',
        );
  return observed.map(([field, value]) => {
    const current = display(currentValue(ci, field));
    const shown = display(value);
    return { field, observed: shown, current, differs: ci !== undefined && current !== shown };
  });
}

/** DuplicateHolder is one ip_address row of a duplicate_ip item (WP-300). */
export interface DuplicateHolder {
  ip_address_id: string;
  interface_id?: string;
  ci_id?: string;
  type: string;
}

/**
 * duplicateHolders returns the sides of a duplicate_ip item of duplicate
 * observations (payload.holders), null for any other item, such as a
 * reservation conflict (WP-772).
 */
export function duplicateHolders(item: ReviewItem): DuplicateHolder[] | null {
  const holders = item.payload?.holders;
  if (item.kind !== 'duplicate_ip' || !Array.isArray(holders)) return null;
  return holders.filter(
    (h): h is DuplicateHolder =>
      typeof h === 'object' &&
      h !== null &&
      typeof (h as DuplicateHolder).ip_address_id === 'string',
  );
}

/** summary is the one-line description of an item in the list. */
function summary(item: ReviewItem): string {
  const p = item.payload ?? {};
  if (item.kind === 'duplicate_ip' && typeof p.address === 'string' && p.address !== '') {
    return p.address;
  }
  return display(
    p.name ?? p.hostname ?? p.field ?? p.management_ip ?? p.address ?? item.object_key ?? item.id,
  );
}

/**
 * candidateLabel names a merge candidate by its CI: the name with the
 * management IP and serial number that tell devices of the same name apart;
 * the id while the CI is not loaded (or not readable).
 */
export function candidateLabel(id: string, ci: CI | undefined): string {
  if (!ci) return id;
  const keys = [ci.management_ip, ci.serial_number].filter((value) => !!value);
  return keys.length > 0 ? `${ci.name} (${keys.join(', ')})` : ci.name;
}

/** commonActions are the actions every selected item allows. */
export function commonActions(items: ReviewItem[]): ReviewAction[] {
  if (items.length === 0) return [];
  // merge needs a CI per item, new of a device of an unknown type a type
  // and accept of duplicate observations a side (ci_id): none of them is
  // offered in bulk.
  return (['new', 'accept', 'dismiss'] as ReviewAction[]).filter((action) =>
    items.every(
      (item) =>
        reviewActions(item).includes(action) &&
        !(action === 'new' && needsCIType(item)) &&
        !(action === 'accept' && duplicateHolders(item) !== null),
    ),
  );
}

/**
 * useCanCreateTickets tells whether the inbox offers "Ticket erstellen":
 * the module ticketing is on, the organization holds ticketing_essential
 * (fail closed while the entitlements load) and the user holds
 * ticket:write and review:resolve (WP-291).
 */
function useCanCreateTickets(canResolve: boolean): boolean {
  const { isModuleEnabled } = useModules();
  const { entitlements, isEnabled } = useEntitlements();
  const canWriteTickets = useHasPermission('ticket:write') === true;
  return (
    canResolve &&
    canWriteTickets &&
    isModuleEnabled('ticketing') &&
    entitlements !== undefined &&
    isEnabled('ticketing_essential')
  );
}

/** ItemTickets links the tickets of an item (WP-291). */
function ItemTickets({ item, className }: { item: ReviewItem; className?: string }) {
  const { t } = useTranslation();
  const tickets = item.tickets ?? [];
  if (tickets.length === 0) return null;
  return (
    <span className={cn('inline-flex flex-wrap items-center gap-1.5', className)}>
      <span className="sr-only">{t('reviews.ticket.linked')}</span>
      {tickets.map((ticket) => (
        <Link
          key={ticket.id}
          to={`/tickets/${ticket.id}`}
          onClick={(event) => event.stopPropagation()}
          className="inline-flex items-center gap-1 font-mono text-xs text-accent hover:underline"
        >
          <Ticket aria-hidden="true" className="h-3 w-3" />#{ticket.ticket_number}
        </Link>
      ))}
    </span>
  );
}

// One formatter per language: a page of 100 rows would otherwise build a new
// Intl.RelativeTimeFormat per row and render.
const relativeFormatters = new Map<string, Intl.RelativeTimeFormat>();

function relativeFormatter(language: string): Intl.RelativeTimeFormat {
  let formatter = relativeFormatters.get(language);
  if (!formatter) {
    formatter = new Intl.RelativeTimeFormat(language, { numeric: 'auto' });
    relativeFormatters.set(language, formatter);
  }
  return formatter;
}

/** The handlers of the page a row calls: always those of the latest render. */
interface RowActions {
  open: (item: ReviewItem) => void;
  toggle: (item: ReviewItem) => void;
  rowProps: (id: string) => RowNavigationProps;
}

/**
 * ReviewRow is one item of the queue. It is memoized: it renders again only
 * when its item, its box, its opening or its place in the Tab order change.
 */
const ReviewRow = memo(function ReviewRow({
  item,
  canResolve,
  checked,
  selected,
  tabbable,
  actions,
}: {
  item: ReviewItem;
  canResolve: boolean;
  checked: boolean;
  selected: boolean;
  tabbable: boolean;
  actions: RefObject<RowActions>;
}) {
  const { t } = useTranslation();
  const navigation = (id: string) => actions.current.rowProps(id);
  return (
    <tr
      ref={(element) => navigation(item.id).ref(element)}
      tabIndex={tabbable ? 0 : -1}
      onFocus={(event) => navigation(item.id).onFocus(event)}
      onKeyDown={(event) => navigation(item.id).onKeyDown(event)}
      onClick={() => actions.current.open(item)}
      className={cn(trInteractiveClasses, selected && 'bg-accent-soft hover:bg-accent-soft')}
    >
      {canResolve ? (
        <td className={`${tdClasses} w-10 pr-0`}>
          <input
            type="checkbox"
            aria-label={t('reviews.select', { name: summary(item) })}
            checked={checked}
            disabled={item.status !== 'open'}
            onChange={() => actions.current.toggle(item)}
            onClick={(event) => event.stopPropagation()}
            // The row is the Tab stop; Space on it toggles this box.
            tabIndex={-1}
          />
        </td>
      ) : null}
      <td className={tdClasses}>
        <Badge variant="info">{t(`reviews.kinds.${item.kind}`, item.kind)}</Badge>
      </td>
      <td className={cn(tdPrimaryClasses, 'min-w-[11rem] max-w-[18rem] [overflow-wrap:anywhere]')}>
        <button
          type="button"
          // The row is the Tab stop; Enter on it opens this item.
          tabIndex={-1}
          className="rounded text-left underline-offset-2 hover:text-accent hover:underline focus-visible:outline-none"
          aria-current={selected ? 'true' : undefined}
        >
          <span className="sr-only">{t(`reviews.kinds.${item.kind}`, item.kind)} </span>
          {summary(item)}
        </button>
        {item.status !== 'open' ? (
          <span className="ml-2 align-middle">
            <Badge variant="neutral">{t(`reviews.statuses.${item.status}`)}</Badge>
          </span>
        ) : null}
        <ItemTickets item={item} className="ml-2 align-middle" />
      </td>
      <td className={tdClasses}>
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <Age iso={item.created_at} />
          {item.escalated ? <Badge variant="danger">{t('reviews.escalated')}</Badge> : null}
        </div>
      </td>
    </tr>
  );
});

/** relativeAge words the age of an item ("3 days ago"); the full date is the tooltip. */
function relativeAge(iso: string, language: string): string {
  const created = new Date(iso).getTime();
  if (Number.isNaN(created)) return '—';
  const formatter = relativeFormatter(language);
  const minutes = Math.round((created - Date.now()) / 60_000);
  if (Math.abs(minutes) < 60) return formatter.format(Math.min(minutes, 0), 'minute');
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return formatter.format(hours, 'hour');
  const days = Math.round(hours / 24);
  if (Math.abs(days) < 60) return formatter.format(days, 'day');
  return formatter.format(Math.round(days / 30), 'month');
}

function Age({ iso }: { iso: string }) {
  const { i18n } = useTranslation();
  const date = new Date(iso);
  return (
    <time
      dateTime={iso}
      title={Number.isNaN(date.getTime()) ? undefined : date.toLocaleString(i18n.language)}
      className="whitespace-nowrap tabular-nums"
    >
      {relativeAge(iso, i18n.language)}
    </time>
  );
}

export function ReviewInboxPage() {
  const { t } = useTranslation();
  const [status, setStatus] = useState('open');
  const [kind, setKind] = useState('');
  const [searchParams, setSearchParams] = useSearchParams();
  const escalated = searchParams.get('escalated') === 'true';
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [offset, setOffset] = useState(0);
  // The selection keeps the items themselves, so it holds across pages.
  const [checked, setChecked] = useState<Map<string, ReviewItem>>(new Map());
  const [bulkAction, setBulkAction] = useState<ReviewAction | ''>('');
  const [bulkResult, setBulkResult] = useState<BulkResult | null>(null);
  const [selectingAll, setSelectingAll] = useState(false);
  const [selectAllError, setSelectAllError] = useState('');
  const detailRef = useRef<HTMLDivElement>(null);
  // The server decides every resolution; without review:resolve the inbox
  // offers none (fail closed while the rights load).
  const canResolve = useHasPermission('review:resolve') === true;
  const canCreateTickets = useCanCreateTickets(canResolve);
  const bulkTickets = useBulkCreateTicketsFromReviews();
  const [ticketResult, setTicketResult] = useState<BulkTicketResult | null>(null);
  // The names of the items of the last bulk ticket run, for its report.
  const [ticketNames, setTicketNames] = useState<Map<string, string>>(new Map());

  const filter = useMemo(() => ({ status, kind, escalated }), [status, kind, escalated]);
  const { data, isLoading, error, refetch } = useReviewItems(filter, offset);
  const bulk = useBulkResolveReviews();
  // The single resolution lives on the page, not in the detail: a refused
  // resolution refetches the list, which may drop the item and unmount its
  // detail; its message must outlive that.
  const resolve = useResolveReview();
  const items = useMemo(() => data?.data ?? [], [data]);
  const total = data?.total ?? items.length;
  const selected = items.find((item) => item.id === selectedId) ?? null;
  const checkedItems = [...checked.values()];
  const bulkActions = commonActions(checkedItems);
  const openItems = items.filter((item) => item.status === 'open');
  const pageSelected = openItems.filter((item) => checked.has(item.id)).length;
  // Open items of the filter beyond this page: only the open filter counts
  // them in total.
  const moreOpen = status === 'open' && total > openItems.length;
  // A page emptied by resolutions falls back to the first page.
  useEffect(() => {
    if (data && items.length === 0 && offset > 0) setOffset(0);
  }, [data, items.length, offset]);

  // On a narrow screen the detail sits below the queue: bring it into view.
  useEffect(() => {
    if (selectedId) detailRef.current?.scrollIntoView?.({ block: 'nearest' });
  }, [selectedId]);

  function resetSelection() {
    resolve.reset();
    setChecked(new Map());
    setOffset(0);
  }

  function toggle(item: ReviewItem) {
    setChecked((prev) => {
      const next = new Map(prev);
      if (next.has(item.id)) next.delete(item.id);
      else next.set(item.id, item);
      return next;
    });
  }

  function selectPageItems(on: boolean) {
    setChecked((prev) => {
      const next = new Map(prev);
      for (const item of openItems) {
        if (on) next.set(item.id, item);
        else next.delete(item.id);
      }
      return next;
    });
  }

  async function selectAllMatching() {
    setSelectingAll(true);
    setSelectAllError('');
    try {
      const all = await fetchAllReviewItems(filter);
      setChecked(new Map(all.filter((i) => i.status === 'open').map((i) => [i.id, i])));
    } catch (err) {
      setSelectAllError(err instanceof Error ? err.message : String(err));
    } finally {
      setSelectingAll(false);
    }
  }

  function runBulk() {
    if (!bulkAction || checkedItems.length === 0) return;
    bulk.mutate(
      { ids: checkedItems.map((item) => item.id), action: bulkAction },
      {
        onSuccess: (result) => {
          setBulkResult(result);
          const failed = new Set(result.failed.map((f) => f.id));
          setChecked(new Map(checkedItems.filter((i) => failed.has(i.id)).map((i) => [i.id, i])));
          setBulkAction('');
        },
      },
    );
  }

  function runBulkTickets() {
    const targets = checkedItems.filter((item) => item.status === 'open');
    if (targets.length === 0 || targets.length > BULK_TICKET_LIMIT) return;
    setBulkResult(null);
    setTicketNames(new Map(targets.map((item) => [item.id, summary(item)])));
    bulkTickets.mutate(
      { ids: targets.map((item) => item.id) },
      { onSuccess: (result) => setTicketResult(result) },
    );
  }

  function open(item: ReviewItem) {
    if (item.id !== selectedId) resolve.reset();
    setSelectedId(item.id);
  }

  // Table keyboard pattern: one row in the Tab order, arrows move, Enter
  // opens the item next to the queue, Space toggles its selection.
  const rowNavigation = useRowNavigation({
    rowIds: items.map((item) => item.id),
    onOpen: (id) => {
      const item = items.find((entry) => entry.id === id);
      if (item) open(item);
    },
    onToggle: (id) => {
      const item = items.find((entry) => entry.id === id);
      if (item && canResolve && item.status === 'open') toggle(item);
    },
  });

  // The rows are memoized and take the handlers of this render through a
  // ref: a change of one row (its box, the opened item) or of the bar above
  // the table does not re-render all rows of the page (up to 100).
  const rowActions = useRef<RowActions>({ open, toggle, rowProps: rowNavigation.getRowProps });
  rowActions.current = { open, toggle, rowProps: rowNavigation.getRowProps };

  return (
    <div className="space-y-4">
      <PageHeader title={t('reviews.title')} description={t('reviews.subtitle')} />

      <Toolbar>
        <Select
          aria-label={t('reviews.status')}
          value={status}
          onChange={(event) => {
            setStatus(event.target.value);
            resetSelection();
          }}
          className="w-44"
          options={[
            { value: '', label: t('reviews.allStatuses') },
            ...STATUSES.map((value) => ({ value, label: t(`reviews.statuses.${value}`) })),
          ]}
        />
        <Select
          aria-label={t('reviews.kind')}
          value={kind}
          onChange={(event) => {
            setKind(event.target.value);
            resetSelection();
          }}
          className="w-56"
          options={[
            { value: '', label: t('reviews.allKinds') },
            ...REVIEW_KINDS.map((value) => ({ value, label: t(`reviews.kinds.${value}`) })),
          ]}
        />
        <label className="inline-flex h-9 items-center gap-2 px-1 text-sm text-fg">
          <input
            type="checkbox"
            checked={escalated}
            onChange={(event) => {
              const on = event.target.checked;
              setSearchParams(
                (prev) => {
                  const next = new URLSearchParams(prev);
                  if (on) next.set('escalated', 'true');
                  else next.delete('escalated');
                  return next;
                },
                { replace: true },
              );
              resetSelection();
            }}
          />
          {t('reviews.escalatedOnly')}
        </label>
        {data ? (
          <ToolbarEnd>
            <span className="text-sm text-fg-muted tabular-nums">
              {total} {t('common.entries')}
            </span>
          </ToolbarEnd>
        ) : null}
      </Toolbar>

      {bulkResult ? (
        <p role="status" className="text-sm text-fg-muted">
          {t('reviews.bulk.result', {
            resolved: bulkResult.resolved.length,
            failed: bulkResult.failed.length,
          })}
        </p>
      ) : null}

      {ticketResult ? (
        <div role="status" className="space-y-1 text-sm text-fg-muted">
          <p>
            {t('reviews.ticket.result', {
              created: ticketResult.created.length,
              failed: ticketResult.failed.length,
            })}
          </p>
          {ticketResult.failed.length > 0 ? (
            <ul className="list-disc pl-5 text-danger">
              {ticketResult.failed.map((failure) => {
                return (
                  <li key={failure.id}>
                    {t('reviews.ticket.failedItem', {
                      name: ticketNames.get(failure.id) ?? failure.id,
                      message: failure.message,
                    })}
                  </li>
                );
              })}
            </ul>
          ) : null}
        </div>
      ) : null}

      {resolve.error ? (
        <ErrorState
          title={t('reviews.resolveError', {
            message: resolve.error instanceof Error ? resolve.error.message : String(resolve.error),
          })}
        />
      ) : null}

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('reviews.loadError')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}
      {data && items.length === 0 ? (
        <EmptyState
          icon={<Inbox />}
          title={t('reviews.empty')}
          description={t('reviews.emptyHint')}
        />
      ) : null}

      {items.length > 0 ? (
        <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,26rem)] xl:grid-cols-[minmax(0,1fr)_minmax(0,30rem)]">
          <Card className="min-w-0 overflow-hidden p-0">
            {canResolve && checkedItems.length > 0 ? (
              <div
                className="flex flex-wrap items-center gap-2 border-b border-line bg-accent-soft px-4 py-2.5"
                role="group"
                aria-label={t('reviews.bulk.label')}
              >
                <span className="mr-1 text-sm font-medium text-fg">
                  {t('reviews.bulk.selected', { count: checkedItems.length })}
                </span>
                <Select
                  aria-label={t('reviews.bulk.action')}
                  value={bulkAction}
                  onChange={(event) => setBulkAction(event.target.value as ReviewAction | '')}
                  className="h-8 w-44"
                  options={[
                    { value: '', label: t('reviews.bulk.choose') },
                    ...bulkActions.map((value) => ({
                      value,
                      label: t(`reviews.actions.${value}`),
                    })),
                  ]}
                />
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={runBulk}
                  disabled={!bulkAction}
                  loading={bulk.isPending}
                >
                  {t('reviews.bulk.run')}
                </Button>
                {canCreateTickets ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    icon={<Ticket />}
                    onClick={runBulkTickets}
                    disabled={checkedItems.length > BULK_TICKET_LIMIT || bulk.isPending}
                    loading={bulkTickets.isPending}
                    title={
                      checkedItems.length > BULK_TICKET_LIMIT
                        ? t('reviews.ticket.limit', { limit: BULK_TICKET_LIMIT })
                        : undefined
                    }
                  >
                    {t('reviews.ticket.bulkCreate')}
                  </Button>
                ) : null}
                {moreOpen && checkedItems.length < total ? (
                  <Button
                    size="sm"
                    variant="ghost"
                    loading={selectingAll}
                    onClick={() => void selectAllMatching()}
                  >
                    {t('reviews.selectAllMatching', { total })}
                  </Button>
                ) : null}
                <Button
                  size="sm"
                  variant="ghost"
                  className="ml-auto"
                  onClick={() => setChecked(new Map())}
                >
                  {t('reviews.bulk.clear')}
                </Button>
                {selectAllError ? (
                  <p role="alert" className="w-full text-sm text-danger">
                    {t('reviews.selectAllError', { message: selectAllError })}
                  </p>
                ) : null}
              </div>
            ) : null}
            <ScrollRegion label={t('reviews.title')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    {canResolve ? (
                      <th scope="col" className={`${thClasses} w-10 pr-0`}>
                        <input
                          type="checkbox"
                          className="align-middle"
                          aria-label={t('reviews.selectAll', { count: openItems.length })}
                          checked={openItems.length > 0 && pageSelected === openItems.length}
                          disabled={openItems.length === 0}
                          ref={(element) => {
                            if (element) {
                              element.indeterminate =
                                pageSelected > 0 && pageSelected < openItems.length;
                            }
                          }}
                          onChange={(event) => selectPageItems(event.target.checked)}
                        />
                      </th>
                    ) : null}
                    <th scope="col" className={thClasses}>
                      {t('reviews.kind')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('reviews.object')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('reviews.age')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((item) => (
                    <ReviewRow
                      key={item.id}
                      item={item}
                      canResolve={canResolve}
                      checked={checked.has(item.id)}
                      selected={selectedId === item.id}
                      tabbable={rowNavigation.activeId === item.id}
                      actions={rowActions}
                    />
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
            <div className="flex items-center justify-between gap-3 border-t border-line px-4 py-2.5 text-sm text-fg-muted">
              <span className="tabular-nums">
                {t('reviews.paging.range', { from: offset + 1, to: offset + items.length, total })}
              </span>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="secondary"
                  icon={<ChevronLeft />}
                  disabled={offset === 0}
                  onClick={() => setOffset(Math.max(0, offset - REVIEW_PAGE_SIZE))}
                >
                  {t('reviews.paging.previous')}
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  iconAfter={<ChevronRight />}
                  disabled={!data?.has_more}
                  onClick={() => setOffset(offset + items.length)}
                >
                  {t('reviews.paging.next')}
                </Button>
              </div>
            </div>
          </Card>
          <div ref={detailRef} className="min-w-0 lg:sticky lg:top-4">
            {selected ? (
              <ReviewDetail
                key={selected.id}
                item={selected}
                canResolve={canResolve}
                canCreateTicket={canCreateTickets}
                resolve={resolve}
              />
            ) : (
              <EmptyState
                icon={<MousePointerClick />}
                title={t('reviews.selectTitle')}
                description={t('reviews.selectHint')}
                className="py-10"
              />
            )}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function ReviewDetail({
  item,
  canResolve,
  canCreateTicket,
  resolve,
}: {
  item: ReviewItem;
  canResolve: boolean;
  canCreateTicket: boolean;
  resolve: ReturnType<typeof useResolveReview>;
}) {
  const { t, i18n } = useTranslation();
  // Opening a ticket is no resolution: the item stays open (WP-291).
  const createTicket = useCreateTicketFromReview();
  const offerTicket = canCreateTicket && item.status === 'open';
  const candidates = item.candidate_ci_ids ?? [];
  const [candidate, setCandidate] = useState(candidates[0] ?? '');
  // The candidates are named by their CIs (F-346).
  // The sides of duplicate observations replace the diff, which shows
  // scalar fields only (WP-773).
  const holders = duplicateHolders(item);
  const candidateCIs = useMountedCIs(candidates.length > 1 || holders ? candidates : []);
  const subject = holders
    ? ''
    : candidate || (typeof item.payload?.ci_id === 'string' ? item.payload.ci_id : '');
  const { data: ci } = useCI(subject);
  const rows = reviewDiff(item, subject ? ci : undefined);
  const actions = canResolve ? reviewActions(item) : [];
  // New of a device of an unknown type names the type (ci_type_id): the
  // active types of /ci-types; the type the device reported is preselected
  // once it exists.
  const askType = item.status === 'open' && needsCIType(item) && actions.includes('new');
  const ciTypes = useCITypes({ limit: 200 }, { enabled: askType });
  const reportedType =
    typeof item.payload?.ci_type_name === 'string' ? item.payload.ci_type_name : '';
  const typeOptions = useMemo(
    () =>
      (ciTypes.data?.data ?? [])
        .map((ty) => ({
          value: ty.id,
          key: ty.key,
          label: localizedText(ty.display_name, i18n.language) || ty.key,
        }))
        .sort((a, b) => a.label.localeCompare(b.label, i18n.language)),
    [ciTypes.data, i18n.language],
  );
  const [chosenType, setChosenType] = useState('');
  const ciTypeId = chosenType || typeOptions.find((ty) => ty.key === reportedType)?.value || '';
  // The generic_device CI of a device no profile matched exists: its type is
  // set on the CI, new is not offered.
  const genericCI =
    item.status === 'open' &&
    item.kind === 'unclassified_device' &&
    item.payload?.reason === 'no_profile_match' &&
    typeof item.payload?.ci_id === 'string'
      ? item.payload.ci_id
      : '';

  function resolution(action: ReviewAction): ReviewResolution {
    if (action === 'merge') return { action, ci_id: candidate };
    if (action === 'accept' && holders && candidate) return { action, ci_id: candidate };
    if (action === 'new' && askType) return { action, ci_type_id: ciTypeId };
    return { action };
  }

  // The first decision that is not dismiss is the primary one; dismiss is
  // the quiet way out, set apart at the right.
  const primaryAction = actions.find((action) => action !== 'dismiss');
  const resolving = resolve.isPending ? resolve.variables?.resolution.action : undefined;

  return (
    <Card className="flex flex-col overflow-hidden p-0">
      <div className="space-y-4 p-5 lg:max-h-[calc(100vh-17rem)] lg:overflow-y-auto">
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-base font-semibold text-fg [overflow-wrap:anywhere]">
              {t(`reviews.kinds.${item.kind}`, item.kind)}
            </h3>
            {item.escalated ? <Badge variant="danger">{t('reviews.escalated')}</Badge> : null}
            {item.status !== 'open' ? (
              <Badge variant="neutral">{t(`reviews.statuses.${item.status}`)}</Badge>
            ) : null}
          </div>
          <p className="flex flex-wrap items-center gap-x-2 text-sm text-fg-muted">
            <span className="font-mono text-xs [overflow-wrap:anywhere]">{summary(item)}</span>
            <Age iso={item.created_at} />
          </p>
          <ItemTickets item={item} />
        </div>
        {createTicket.data ? (
          <p role="status" className="text-sm text-fg">
            <Link to={`/tickets/${createTicket.data.id}`} className="text-accent hover:underline">
              {t('reviews.ticket.created', { number: createTicket.data.ticket_number })}
            </Link>
          </p>
        ) : null}
        {createTicket.error ? (
          <p role="alert" className="text-sm text-danger">
            {createTicket.error instanceof ApiError
              ? (createTicket.error.detail ?? createTicket.error.message)
              : String(createTicket.error)}
          </p>
        ) : null}
        {candidates.length > 1 ? (
          <Select
            label={holders ? t('reviews.holders.side') : t('reviews.candidate')}
            value={candidate}
            onChange={(event) => setCandidate(event.target.value)}
            options={candidates.map((id) => ({
              value: id,
              label: candidateLabel(id, candidateCIs.get(id)),
            }))}
          />
        ) : null}
        {holders ? (
          <DuplicateSides item={item} holders={holders} cis={candidateCIs} />
        ) : (
          <DiffTable rows={rows} ci={ci} />
        )}
        {genericCI ? (
          <p className="text-sm text-fg-muted">
            {t('reviews.noProfileMatch')}{' '}
            <Link to={`/cmdb/${genericCI}`} className="text-accent hover:underline">
              {t('reviews.openCI')}
            </Link>
          </p>
        ) : null}
        {askType ? (
          <div className="space-y-1.5">
            <Select
              label={t('reviews.ciType')}
              value={ciTypeId}
              onChange={(event) => setChosenType(event.target.value)}
              error={ciTypes.isError ? t('reviews.ciTypesLoadError') : undefined}
              options={[
                { value: '', label: t('reviews.chooseCIType') },
                ...typeOptions.map(({ value, label }) => ({ value, label })),
              ]}
            />
            <p className="text-xs text-fg-muted">
              {t('reviews.ciTypeHint', { name: reportedType || '—' })}
            </p>
          </div>
        ) : null}
      </div>
      {item.status === 'open' && (actions.length > 0 || offerTicket) ? (
        <div className="flex flex-wrap items-center gap-2 border-t border-line bg-canvas/60 px-5 py-3">
          {offerTicket ? (
            <Button
              size="sm"
              variant="secondary"
              icon={<Ticket />}
              loading={createTicket.isPending}
              disabled={createTicket.isPending || resolve.isPending}
              onClick={() => createTicket.mutate({ id: item.id })}
            >
              {t('reviews.ticket.create')}
            </Button>
          ) : null}
          {actions.map((action) => (
            <Button
              key={action}
              size="sm"
              variant={
                action === primaryAction ? 'primary' : action === 'dismiss' ? 'ghost' : 'secondary'
              }
              className={action === 'dismiss' ? 'ml-auto' : undefined}
              loading={resolving === action}
              disabled={
                resolve.isPending ||
                (action === 'merge' && !candidate) ||
                (action === 'new' && askType && !ciTypeId)
              }
              onClick={() => resolve.mutate({ id: item.id, resolution: resolution(action) })}
            >
              {t(`reviews.actions.${action}`)}
            </Button>
          ))}
        </div>
      ) : null}
    </Card>
  );
}

const detailThClasses = 'py-2 pr-3 text-left text-xs font-medium text-fg-muted';
const detailTdClasses = 'border-t border-line py-2 pr-3 align-top';

/** DiffTable compares the observed fields of an item with its CI. */
function DiffTable({ rows, ci }: { rows: DiffRow[]; ci: CI | undefined }) {
  const { t } = useTranslation();
  // Fixed columns that wrap long names, serials and addresses keep the table
  // inside a phone-wide viewport (UI-18 reflow).
  return (
    <table className="w-full table-fixed text-left text-sm [overflow-wrap:anywhere]">
      <caption className="sr-only">{t('reviews.diff')}</caption>
      <thead>
        <tr>
          <th scope="col" className={cn(detailThClasses, 'w-[28%]')}>
            {t('reviews.field')}
          </th>
          <th scope="col" className={detailThClasses}>
            {t('reviews.observed')}
          </th>
          <th scope="col" className={cn(detailThClasses, 'pr-0')}>
            {ci ? ci.name : t('reviews.current')}
          </th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr
            key={row.field}
            className={row.differs ? 'bg-amber-500/10' : undefined}
            data-differs={row.differs ? 'true' : undefined}
          >
            <td className={cn(detailTdClasses, 'pl-2 font-mono text-xs text-fg-muted')}>
              {row.field}
            </td>
            <td className={cn(detailTdClasses, 'text-fg')}>{row.observed || '—'}</td>
            <td className={cn(detailTdClasses, 'pr-2 text-fg')}>
              {row.current || '—'}
              {row.differs ? (
                <span className="ml-2 inline-block">
                  <Badge variant="warning">{t('reviews.differs')}</Badge>
                </span>
              ) : null}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/**
 * DuplicateSides shows a duplicate_ip item of duplicate observations: the
 * address, the VRF and per row of payload.holders the CI (linked), the
 * interface and the type (NET-06, WP-773).
 */
function DuplicateSides({
  item,
  holders,
  cis,
}: {
  item: ReviewItem;
  holders: DuplicateHolder[];
  cis: Map<string, CI>;
}) {
  const { t } = useTranslation();
  const p = item.payload ?? {};
  return (
    <section
      className="space-y-3 text-sm [overflow-wrap:anywhere]"
      aria-label={t('reviews.holders.title')}
    >
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
        <dt className="text-fg-muted">{t('reviews.holders.address')}</dt>
        <dd className="font-mono text-xs text-fg">{display(p.address) || '—'}</dd>
        <dt className="text-fg-muted">{t('reviews.holders.vrf')}</dt>
        <dd className="font-mono text-xs text-fg">{display(p.vrf_id) || '—'}</dd>
      </dl>
      <table className="w-full table-fixed text-left">
        <caption className="sr-only">{t('reviews.holders.title')}</caption>
        <thead>
          <tr>
            <th scope="col" className={detailThClasses}>
              {t('reviews.holders.ci')}
            </th>
            <th scope="col" className={detailThClasses}>
              {t('reviews.holders.interface')}
            </th>
            <th scope="col" className={cn(detailThClasses, 'pr-0')}>
              {t('reviews.holders.type')}
            </th>
          </tr>
        </thead>
        <tbody>
          {holders.map((holder) => (
            <tr key={holder.ip_address_id}>
              <td className={detailTdClasses}>
                {holder.ci_id ? (
                  <Link to={`/cmdb/${holder.ci_id}`} className="text-accent hover:underline">
                    {cis.get(holder.ci_id)?.name ?? holder.ci_id}
                  </Link>
                ) : (
                  t('reviews.holders.noCI')
                )}
              </td>
              <td className={cn(detailTdClasses, 'font-mono text-xs')}>
                {holder.interface_id || '—'}
              </td>
              <td className={cn(detailTdClasses, 'pr-0')}>{holder.type}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
