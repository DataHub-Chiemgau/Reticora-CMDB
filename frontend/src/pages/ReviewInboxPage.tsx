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
 * no longer holds the item and its detail closes (WP-557).
 */
import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useCI, useHasPermission, useMountedCIs } from '../api/hooks';
import { useCITypes } from '../api/cmdbHooks';
import { localizedText } from '../api/cmdb';
import type { CI } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
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

/** summary is the one-line description of an item in the list. */
function summary(item: ReviewItem): string {
  const p = item.payload ?? {};
  return display(p.name ?? p.hostname ?? p.field ?? p.management_ip ?? item.object_key ?? item.id);
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
  // merge needs a CI per item and new of a device of an unknown type a
  // type: neither is offered in bulk.
  return (['new', 'accept', 'dismiss'] as ReviewAction[]).filter((action) =>
    items.every(
      (item) => reviewActions(item).includes(action) && !(action === 'new' && needsCIType(item)),
    ),
  );
}

export function ReviewInboxPage() {
  const { t } = useTranslation();
  const [status, setStatus] = useState('open');
  const [kind, setKind] = useState('');
  const [escalated, setEscalated] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [offset, setOffset] = useState(0);
  // The selection keeps the items themselves, so it holds across pages.
  const [checked, setChecked] = useState<Map<string, ReviewItem>>(new Map());
  const [bulkAction, setBulkAction] = useState<ReviewAction | ''>('');
  const [bulkResult, setBulkResult] = useState<BulkResult | null>(null);
  const [selectingAll, setSelectingAll] = useState(false);
  const [selectAllError, setSelectAllError] = useState('');
  // The server decides every resolution; without review:resolve the inbox
  // offers none (fail closed while the rights load).
  const canResolve = useHasPermission('review:resolve') === true;

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
  // Open items of the filter beyond this page: only the open filter counts
  // them in total.
  const moreOpen = status === 'open' && total > openItems.length;
  // A page emptied by resolutions falls back to the first page.
  useEffect(() => {
    if (data && items.length === 0 && offset > 0) setOffset(0);
  }, [data, items.length, offset]);

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

  function selectPage(on: boolean) {
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

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
          {t('reviews.title')}
        </h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('reviews.subtitle')}</p>
      </div>

      <Card>
        <div className="flex flex-wrap items-end gap-3">
          <Select
            label={t('reviews.status')}
            value={status}
            onChange={(event) => {
              setStatus(event.target.value);
              resetSelection();
            }}
            options={[
              { value: '', label: t('reviews.allStatuses') },
              ...STATUSES.map((value) => ({ value, label: t(`reviews.statuses.${value}`) })),
            ]}
          />
          <Select
            label={t('reviews.kind')}
            value={kind}
            onChange={(event) => {
              setKind(event.target.value);
              resetSelection();
            }}
            options={[
              { value: '', label: t('reviews.allKinds') },
              ...REVIEW_KINDS.map((value) => ({ value, label: t(`reviews.kinds.${value}`) })),
            ]}
          />
          <label className="flex items-center gap-2 pb-2 text-sm text-gray-700 dark:text-gray-300">
            <input
              type="checkbox"
              checked={escalated}
              onChange={(event) => {
                setEscalated(event.target.checked);
                resetSelection();
              }}
            />
            {t('reviews.escalatedOnly')}
          </label>
        </div>
      </Card>

      {canResolve && checkedItems.length > 0 ? (
        <Card>
          <div
            className="flex flex-wrap items-end gap-3"
            role="group"
            aria-label={t('reviews.bulk.label')}
          >
            <p className="pb-2 text-sm">
              {t('reviews.bulk.selected', { count: checkedItems.length })}
            </p>
            <Select
              label={t('reviews.bulk.action')}
              value={bulkAction}
              onChange={(event) => setBulkAction(event.target.value as ReviewAction | '')}
              options={[
                { value: '', label: t('reviews.bulk.choose') },
                ...bulkActions.map((value) => ({ value, label: t(`reviews.actions.${value}`) })),
              ]}
            />
            <Button onClick={runBulk} disabled={!bulkAction || bulk.isPending}>
              {t('reviews.bulk.run')}
            </Button>
          </div>
        </Card>
      ) : null}

      {bulkResult ? (
        <p role="status" className="text-sm text-gray-700 dark:text-gray-300">
          {t('reviews.bulk.result', {
            resolved: bulkResult.resolved.length,
            failed: bulkResult.failed.length,
          })}
        </p>
      ) : null}

      {resolve.error ? (
        <p role="alert" className="text-sm text-danger">
          {t('reviews.resolveError', {
            message: resolve.error instanceof Error ? resolve.error.message : String(resolve.error),
          })}
        </p>
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
        <EmptyState title={t('reviews.empty')} description={t('reviews.emptyHint')} />
      ) : null}

      {items.length > 0 ? (
        <div className="grid gap-4 lg:grid-cols-2">
          <Card>
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2 text-sm">
              <p className="text-gray-700 dark:text-gray-300">
                {t('reviews.paging.range', {
                  from: offset + 1,
                  to: offset + items.length,
                  total,
                })}
              </p>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={offset === 0}
                  onClick={() => setOffset(Math.max(0, offset - REVIEW_PAGE_SIZE))}
                >
                  {t('reviews.paging.previous')}
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={!data?.has_more}
                  onClick={() => setOffset(offset + items.length)}
                >
                  {t('reviews.paging.next')}
                </Button>
              </div>
            </div>
            {canResolve && openItems.length > 0 ? (
              <div className="mb-2 flex flex-wrap items-center gap-3 text-sm">
                <label className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={openItems.every((item) => checked.has(item.id))}
                    onChange={(event) => selectPage(event.target.checked)}
                  />
                  {t('reviews.selectAll', { count: openItems.length })}
                </label>
                {moreOpen ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={selectingAll}
                    onClick={() => void selectAllMatching()}
                  >
                    {t('reviews.selectAllMatching', { total })}
                  </Button>
                ) : null}
              </div>
            ) : null}
            {selectAllError ? (
              <p role="alert" className="mb-2 text-sm text-danger">
                {t('reviews.selectAllError', { message: selectAllError })}
              </p>
            ) : null}
            <ul className="divide-y divide-gray-100 dark:divide-gray-800">
              {items.map((item) => (
                <li key={item.id} className="flex items-center gap-3 py-2 text-sm">
                  {canResolve ? (
                    <input
                      type="checkbox"
                      aria-label={t('reviews.select', { name: summary(item) })}
                      checked={checked.has(item.id)}
                      disabled={item.status !== 'open'}
                      onChange={() => toggle(item)}
                    />
                  ) : null}
                  <button
                    type="button"
                    className="flex flex-1 flex-wrap items-center gap-2 text-left hover:underline"
                    aria-current={selectedId === item.id ? 'true' : undefined}
                    onClick={() => {
                      if (item.id !== selectedId) resolve.reset();
                      setSelectedId(item.id);
                    }}
                  >
                    <Badge variant="info">{t(`reviews.kinds.${item.kind}`, item.kind)}</Badge>
                    <span className="font-medium">{summary(item)}</span>
                    {item.escalated ? (
                      <Badge variant="danger">{t('reviews.escalated')}</Badge>
                    ) : null}
                    {item.status !== 'open' ? (
                      <Badge variant="neutral">{t(`reviews.statuses.${item.status}`)}</Badge>
                    ) : null}
                  </button>
                </li>
              ))}
            </ul>
          </Card>
          {selected ? (
            <ReviewDetail
              key={selected.id}
              item={selected}
              canResolve={canResolve}
              resolve={resolve}
            />
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function ReviewDetail({
  item,
  canResolve,
  resolve,
}: {
  item: ReviewItem;
  canResolve: boolean;
  resolve: ReturnType<typeof useResolveReview>;
}) {
  const { t, i18n } = useTranslation();
  const candidates = item.candidate_ci_ids ?? [];
  const [candidate, setCandidate] = useState(candidates[0] ?? '');
  // The candidates are named by their CIs (F-346).
  const candidateCIs = useMountedCIs(candidates.length > 1 ? candidates : []);
  const subject = candidate || (typeof item.payload?.ci_id === 'string' ? item.payload.ci_id : '');
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
    if (action === 'new' && askType) return { action, ci_type_id: ciTypeId };
    return { action };
  }

  return (
    <Card title={t(`reviews.kinds.${item.kind}`, item.kind)}>
      {candidates.length > 1 ? (
        <Select
          label={t('reviews.candidate')}
          value={candidate}
          onChange={(event) => setCandidate(event.target.value)}
          options={candidates.map((id) => ({
            value: id,
            label: candidateLabel(id, candidateCIs.get(id)),
          }))}
        />
      ) : null}
      <table className="mt-3 w-full text-left text-sm">
        <caption className="sr-only">{t('reviews.diff')}</caption>
        <thead className="text-xs uppercase text-muted">
          <tr>
            <th className="pb-2">{t('reviews.field')}</th>
            <th className="pb-2">{t('reviews.observed')}</th>
            <th className="pb-2">{ci ? ci.name : t('reviews.current')}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr
              key={row.field}
              className={row.differs ? 'bg-amber-50 dark:bg-amber-950' : undefined}
              data-differs={row.differs ? 'true' : undefined}
            >
              <td className="py-1 font-mono text-xs">{row.field}</td>
              <td className="py-1">{row.observed || '—'}</td>
              <td className="py-1">
                {row.current || '—'}
                {row.differs ? (
                  <span className="ml-2">
                    <Badge variant="warning">{t('reviews.differs')}</Badge>
                  </span>
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {genericCI ? (
        <p className="mt-4 text-sm text-gray-700 dark:text-gray-300">
          {t('reviews.noProfileMatch')}{' '}
          <Link to={`/cmdb/${genericCI}`} className="text-blue-600 hover:underline">
            {t('reviews.openCI')}
          </Link>
        </p>
      ) : null}
      {askType ? (
        <div className="mt-4">
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
          <p className="mt-1 text-xs text-gray-600 dark:text-gray-400">
            {t('reviews.ciTypeHint', { name: reportedType || '—' })}
          </p>
        </div>
      ) : null}
      {item.status === 'open' ? (
        <div className="mt-4 flex flex-wrap gap-2">
          {actions.map((action) => (
            <Button
              key={action}
              size="sm"
              variant={action === 'dismiss' ? 'secondary' : 'primary'}
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
