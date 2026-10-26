/**
 * ReviewInboxPage — the review inbox of reconciliation (UI-16, REC-08):
 * filter by status, type and escalation; a diff of the observation against
 * the CI it concerns; resolution of one item (merge, new, dismiss, accept)
 * or of several items with one action. New of a device of an unknown type
 * asks for the type of the CI; for a device recorded as generic_device the
 * inbox shows the next step instead (F-176).
 */
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useCI } from '../api/hooks';
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
  const [checked, setChecked] = useState<Set<string>>(new Set());
  const [bulkAction, setBulkAction] = useState<ReviewAction | ''>('');
  const [bulkResult, setBulkResult] = useState<BulkResult | null>(null);

  const { data, isLoading, error, refetch } = useReviewItems({ status, kind, escalated });
  const bulk = useBulkResolveReviews();
  const items = useMemo(() => data?.data ?? [], [data]);
  const selected = items.find((item) => item.id === selectedId) ?? null;
  const checkedItems = items.filter((item) => checked.has(item.id));
  const bulkActions = commonActions(checkedItems);
  const openItems = items.filter((item) => item.status === 'open');

  function toggle(id: string) {
    setChecked((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function runBulk() {
    if (!bulkAction || checkedItems.length === 0) return;
    bulk.mutate(
      { ids: checkedItems.map((item) => item.id), action: bulkAction },
      {
        onSuccess: (result) => {
          setBulkResult(result);
          setChecked(new Set(result.failed.map((f) => f.id)));
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
              setChecked(new Set());
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
              setChecked(new Set());
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
              onChange={(event) => setEscalated(event.target.checked)}
            />
            {t('reviews.escalatedOnly')}
          </label>
        </div>
      </Card>

      {checkedItems.length > 0 ? (
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
            {openItems.length > 0 ? (
              <label className="mb-2 flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={openItems.every((item) => checked.has(item.id))}
                  onChange={(event) =>
                    setChecked(
                      event.target.checked ? new Set(openItems.map((item) => item.id)) : new Set(),
                    )
                  }
                />
                {t('reviews.selectAll')}
              </label>
            ) : null}
            <ul className="divide-y divide-gray-100 dark:divide-gray-800">
              {items.map((item) => (
                <li key={item.id} className="flex items-center gap-3 py-2 text-sm">
                  <input
                    type="checkbox"
                    aria-label={t('reviews.select', { name: summary(item) })}
                    checked={checked.has(item.id)}
                    disabled={item.status !== 'open'}
                    onChange={() => toggle(item.id)}
                  />
                  <button
                    type="button"
                    className="flex flex-1 flex-wrap items-center gap-2 text-left hover:underline"
                    aria-current={selectedId === item.id ? 'true' : undefined}
                    onClick={() => setSelectedId(item.id)}
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
          {selected ? <ReviewDetail key={selected.id} item={selected} /> : null}
        </div>
      ) : null}
    </div>
  );
}

function ReviewDetail({ item }: { item: ReviewItem }) {
  const { t, i18n } = useTranslation();
  const candidates = item.candidate_ci_ids ?? [];
  const [candidate, setCandidate] = useState(candidates[0] ?? '');
  const subject = candidate || (typeof item.payload?.ci_id === 'string' ? item.payload.ci_id : '');
  const { data: ci } = useCI(subject);
  const resolve = useResolveReview();
  const rows = reviewDiff(item, subject ? ci : undefined);
  const actions = reviewActions(item);
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
          options={candidates.map((id) => ({ value: id, label: id }))}
        />
      ) : null}
      <table className="mt-3 w-full text-left text-sm">
        <caption className="sr-only">{t('reviews.diff')}</caption>
        <thead className="text-xs uppercase text-gray-500">
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
      {resolve.error ? (
        <p role="alert" className="mt-2 text-sm text-red-600">
          {resolve.error instanceof Error ? resolve.error.message : String(resolve.error)}
        </p>
      ) : null}
    </Card>
  );
}
