/**
 * MetamodelDraftsPage is the work area of the metamodel drafts (MET-53,
 * UI-11, WP-799): the list of GET /api/v1/metamodel/drafts (all pages over
 * next_cursor, filtered by status in the page) with creating, discarding and
 * choosing the active draft (stores/metamodelDraft, WP-798), and per draft
 * the patch list grouped by item kind, each item as a MetamodelDiff against
 * the current state of its target, removing an item (If-Match of the draft
 * version; a 412 reads the draft again), the test run as job
 * metamodel.test with its report (metamodel:publish; without the right
 * only the counters) and publishing a tested draft after a confirmation.
 * Published and discarded drafts are read-only. The page belongs to the
 * module metamodel (E-105); switched off, it shows a note instead.
 */
import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import {
  localizedText,
  staleItemsOf,
  type MetamodelDraft,
  type MetamodelDraftItem,
  type MetamodelPublication,
  type MetamodelTestReport,
  type StaleDraftItem,
} from '../api/cmdb';
import {
  useAllCITypes,
  useCreateMetamodelDraft,
  useDiscardMetamodelDraft,
  useDraftItemTarget,
  useMetamodelDraft,
  useMetamodelDraftItems,
  useMetamodelDraftTestReport,
  useMetamodelDrafts,
  usePublishMetamodelDraft,
  useRemoveMetamodelDraftItem,
  useTestMetamodelDraft,
} from '../api/cmdbHooks';
import { ApiError } from '../api/client';
import { useHasPermission } from '../api/hooks';
import { isActive, useJob } from '../hooks/useJobs';
import { useModules } from '../hooks/useEntitlements';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { ScrollRegion } from '../components/ScrollRegion';
import { MetamodelTabs } from '../components/metamodel/MetamodelTabs';
import { MetamodelDiff, type DiffEntry } from '../components/metamodel/MetamodelDiff';
import { draftItemName, payloadOf } from '../components/metamodel/DraftBanner';
import { DRAFT_ITEM_KINDS, DRAFT_STATUSES, useMetamodelDraftStore } from '../stores/metamodelDraft';

export { DRAFT_STATUSES };

type DraftStatus = MetamodelDraft['status'];

const STATUS_VARIANT: Record<DraftStatus, 'info' | 'success' | 'neutral' | 'warning'> = {
  draft: 'warning',
  tested: 'info',
  published: 'success',
  discarded: 'neutral',
};

/** Published and discarded drafts are immutable (WP-260). */
function isClosed(status: DraftStatus): boolean {
  return status === 'published' || status === 'discarded';
}

const notFound = (error: unknown) => error instanceof ApiError && error.status === 404;

export function MetamodelDraftsPage() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const modules = useModules();
  const moduleOff = !modules.isLoading && !modules.isModuleEnabled('metamodel');

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
          {t('citypes.title')}
        </h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
          {t('metamodelDrafts.page.subtitle')}
        </p>
      </div>
      <MetamodelTabs />
      {moduleOff ? (
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('templates.moduleOff')}</p>
      ) : id ? (
        <DraftDetail id={id} />
      ) : (
        <DraftList />
      )}
    </div>
  );
}

/**
 * ProblemNotice shows a refused request as its problem detail (RFC 7807):
 * status, title, detail and the trace id for support (API-03).
 */
function ProblemNotice({ error, prefix }: { error: unknown; prefix: string }) {
  const { t } = useTranslation();
  const api = error instanceof ApiError ? error : undefined;
  const detail = api ? api.detail || api.title || api.message : String(error);
  return (
    <div
      role="alert"
      className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-800 dark:border-red-800 dark:bg-red-950 dark:text-red-200"
    >
      <p className="font-medium">{prefix}</p>
      <p>{api ? t('metamodelDrafts.problem.detail', { status: api.status, detail }) : detail}</p>
      {api?.traceId ? (
        <p className="font-mono text-xs">
          {t('metamodelDrafts.problem.traceId', { traceId: api.traceId })}
        </p>
      ) : null}
    </div>
  );
}

function formatDate(value: string | null | undefined, language: string): string {
  if (!value) return '';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(language);
}

// ─── List ────────────────────────────────────────────────────────────────────

function DraftList() {
  const { t, i18n } = useTranslation();
  const [status, setStatus] = useState<'' | DraftStatus>('');
  const [creating, setCreating] = useState(false);
  const [discarding, setDiscarding] = useState<MetamodelDraft | null>(null);
  const canManage = useHasPermission('citype:manage') === true;
  const query = useMetamodelDrafts();
  const active = useMetamodelDraftStore((s) => s.draft);
  const setDraft = useMetamodelDraftStore((s) => s.setDraft);
  const rows = (query.data ?? []).filter((d) => !status || d.status === status);

  return (
    <div className="space-y-4">
      <Card>
        <div className="flex flex-wrap items-end gap-3">
          <div className="min-w-[12rem]">
            <Select
              label={t('metamodelDrafts.page.filterStatus')}
              value={status}
              onChange={(e) => setStatus(e.target.value as '' | DraftStatus)}
              options={[
                { value: '', label: t('common.all') },
                ...DRAFT_STATUSES.map((value) => ({
                  value,
                  label: t(`metamodelDrafts.statuses.${value}`),
                })),
              ]}
            />
          </div>
          <div className="flex-1" />
          {canManage ? (
            <Button size="sm" onClick={() => setCreating(true)}>
              {t('metamodelDrafts.create')}
            </Button>
          ) : null}
        </div>
      </Card>

      {query.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
      {query.error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {query.isSuccess && rows.length === 0 ? (
        <p className="text-sm text-muted">{t('metamodelDrafts.page.empty')}</p>
      ) : null}
      {rows.length > 0 ? (
        <Card>
          <ul
            aria-label={t('metamodelDrafts.page.listLabel')}
            className="divide-y divide-gray-100 dark:divide-gray-800"
          >
            {rows.map((d) => {
              const open = !isClosed(d.status);
              const isActiveDraft = active?.id === d.id;
              return (
                <li
                  key={d.id}
                  data-draft-id={d.id}
                  className="flex flex-wrap items-center justify-between gap-3 py-3 text-sm"
                >
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Link
                        to={`/ci-types/drafts/${d.id}`}
                        className="font-medium text-primary hover:underline"
                      >
                        {d.name}
                      </Link>
                      <Badge variant={STATUS_VARIANT[d.status]}>
                        {t(`metamodelDrafts.statuses.${d.status}`)}
                      </Badge>
                      {isActiveDraft ? (
                        <Badge variant="warning">{t('metamodelDrafts.page.isActive')}</Badge>
                      ) : null}
                    </div>
                    <div className="text-xs text-muted">
                      {t('metamodelDrafts.page.meta', {
                        count: d.item_count,
                        created: formatDate(d.created_at, i18n.language),
                      })}
                    </div>
                  </div>
                  {canManage && open ? (
                    <div className="flex flex-wrap gap-3">
                      {!isActiveDraft ? (
                        <button
                          type="button"
                          className="text-xs text-primary hover:underline"
                          aria-label={t('metamodelDrafts.page.chooseActiveAction', {
                            name: d.name,
                          })}
                          onClick={() => setDraft({ id: d.id, name: d.name })}
                        >
                          {t('metamodelDrafts.page.chooseActive')}
                        </button>
                      ) : null}
                      <button
                        type="button"
                        className="text-xs text-primary hover:underline"
                        aria-label={t('metamodelDrafts.page.discardAction', { name: d.name })}
                        onClick={() => setDiscarding(d)}
                      >
                        {t('metamodelDrafts.page.discard')}
                      </button>
                    </div>
                  ) : null}
                </li>
              );
            })}
          </ul>
        </Card>
      ) : null}
      {creating ? <CreateDialog onClose={() => setCreating(false)} /> : null}
      {discarding ? <DiscardDialog draft={discarding} onClose={() => setDiscarding(null)} /> : null}
    </div>
  );
}

function CreateDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const create = useCreateMetamodelDraft();
  const [name, setName] = useState('');
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate(name.trim(), { onSuccess: onClose });
  };
  return (
    <Modal
      open
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={t('metamodelDrafts.create')}
    >
      <form onSubmit={submit} className="space-y-3">
        <Input
          label={t('metamodelDrafts.newName')}
          value={name}
          required
          maxLength={200}
          onChange={(e) => setName(e.target.value)}
        />
        {create.error ? (
          <ProblemNotice error={create.error} prefix={t('metamodelDrafts.createFailed')} />
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={create.isPending || !name.trim()}>
            {t('metamodelDrafts.create')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** The confirmation of DELETE /metamodel/drafts/{id}; the active draft is left. */
function DiscardDialog({
  draft,
  onClose,
  onDiscarded,
}: {
  draft: MetamodelDraft;
  onClose: () => void;
  onDiscarded?: () => void;
}) {
  const { t } = useTranslation();
  const discard = useDiscardMetamodelDraft();
  const active = useMetamodelDraftStore((s) => s.draft);
  const clear = useMetamodelDraftStore((s) => s.clear);
  return (
    <Modal
      open
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={t('metamodelDrafts.page.discardTitle', { name: draft.name })}
      description={t('metamodelDrafts.page.discardText')}
    >
      {discard.error ? (
        <ProblemNotice error={discard.error} prefix={t('metamodelDrafts.page.discardFailed')} />
      ) : null}
      <div className="mt-3 flex justify-end gap-2">
        <Button variant="ghost" onClick={onClose}>
          {t('common.cancel')}
        </Button>
        <Button
          variant="danger"
          disabled={discard.isPending}
          onClick={() =>
            discard.mutate(
              { id: draft.id, version: draft.version },
              {
                onSuccess: () => {
                  if (active?.id === draft.id) clear();
                  onDiscarded?.();
                  onClose();
                },
              },
            )
          }
        >
          {t('metamodelDrafts.page.discard')}
        </Button>
      </div>
    </Modal>
  );
}

// ─── Detail ──────────────────────────────────────────────────────────────────

function DraftDetail({ id }: { id: string }) {
  const { t, i18n } = useTranslation();
  const qc = useQueryClient();
  const draftQuery = useMetamodelDraft(id);
  const draft = draftQuery.data;
  const itemsQuery = useMetamodelDraftItems(id, { enabled: !!draft && !draft.items });
  const items = useMemo(() => draft?.items ?? itemsQuery.data ?? [], [draft, itemsQuery.data]);
  const canManage = useHasPermission('citype:manage') === true;
  const canPublish = useHasPermission('metamodel:publish') === true;
  const active = useMetamodelDraftStore((s) => s.draft);
  const setDraft = useMetamodelDraftStore((s) => s.setDraft);
  const clear = useMetamodelDraftStore((s) => s.clear);
  const remove = useRemoveMetamodelDraftItem();
  const [reloaded, setReloaded] = useState(false);
  const [dialog, setDialog] = useState<'test' | 'publish' | 'discard' | null>(null);
  const [jobId, setJobId] = useState<string | null>(null);
  const [published, setPublished] = useState<MetamodelPublication | null>(null);
  const test = useTestMetamodelDraft();
  const publish = usePublishMetamodelDraft();
  const job = useJob(jobId);

  // A finished test run changes the status and the report of the draft.
  const jobDone = !!job.data && !isActive(job.data);
  useEffect(() => {
    if (jobDone) void qc.invalidateQueries({ queryKey: ['metamodel-drafts', id] });
  }, [jobDone, id, qc]);

  if (draftQuery.isLoading) return <SkeletonList rows={3} label={t('app.loading')} />;
  if (notFound(draftQuery.error)) {
    return <p className="text-sm text-muted">{t('metamodelDrafts.page.notFound')}</p>;
  }
  if (draftQuery.error || !draft) {
    return (
      <ErrorState
        title={t('app.error')}
        retryLabel={t('common.retry')}
        onRetry={() => void draftQuery.refetch()}
      />
    );
  }

  const closed = isClosed(draft.status);
  const writable = canManage && !closed;
  const running = !!job.data && isActive(job.data);
  const isActiveDraft = active?.id === draft.id;

  const removeItem = (itemId: string) => {
    setReloaded(false);
    remove.mutate(
      { id: draft.id, itemId, version: draft.version },
      {
        onError: (error) => {
          // Changed in between (API-07): read the draft again and say so.
          if (error instanceof ApiError && error.status === 412) {
            setReloaded(true);
            void draftQuery.refetch();
          }
        },
        onSuccess: () => {
          publish.reset();
        },
      },
    );
  };

  const startTest = () => {
    setDialog(null);
    test.mutate(draft.id, { onSuccess: (started) => setJobId(started.job_id) });
  };

  const confirmPublish = () => {
    setDialog(null);
    publish.mutate(draft.id, {
      onSuccess: (result) => {
        setPublished(result);
        if (isActiveDraft) clear();
      },
    });
  };

  const stale = staleItemsOf(publish.error);
  const removeError = remove.error && !reloaded ? remove.error : null;

  return (
    <div className="space-y-4">
      <Link to="/ci-types/drafts" className="text-sm text-primary hover:underline">
        {t('metamodelDrafts.page.back')}
      </Link>
      <Card
        title={
          <span className="flex flex-wrap items-center gap-2">
            <span>{draft.name}</span>
            <Badge variant={STATUS_VARIANT[draft.status]}>
              {t(`metamodelDrafts.statuses.${draft.status}`)}
            </Badge>
            {isActiveDraft ? (
              <Badge variant="warning">{t('metamodelDrafts.page.isActive')}</Badge>
            ) : null}
          </span>
        }
      >
        <p className="text-sm text-gray-600 dark:text-gray-300">
          {t('metamodelDrafts.page.detailMeta', {
            count: draft.item_count,
            version: draft.version,
            created: formatDate(draft.created_at, i18n.language),
          })}
        </p>
        {draft.published_at ? (
          <p className="text-sm text-gray-600 dark:text-gray-300">
            {t('metamodelDrafts.page.publishedAt', {
              at: formatDate(draft.published_at, i18n.language),
            })}
          </p>
        ) : null}
        {closed ? (
          <p className="mt-2 text-sm text-muted">{t('metamodelDrafts.page.readOnly')}</p>
        ) : null}
        {canManage && !closed ? (
          <div className="mt-3 flex flex-wrap gap-2">
            {!isActiveDraft ? (
              <Button
                size="sm"
                variant="secondary"
                onClick={() => setDraft({ id: draft.id, name: draft.name })}
              >
                {t('metamodelDrafts.page.chooseActive')}
              </Button>
            ) : null}
            <Button
              size="sm"
              variant="secondary"
              disabled={!canPublish || running || test.isPending || items.length === 0}
              title={canPublish ? undefined : t('metamodelDrafts.page.needsPublish')}
              onClick={() => setDialog('test')}
            >
              {t('metamodelDrafts.page.test')}
            </Button>
            <Button
              size="sm"
              disabled={!canPublish || draft.status !== 'tested' || publish.isPending}
              title={canPublish ? undefined : t('metamodelDrafts.page.needsPublish')}
              onClick={() => setDialog('publish')}
            >
              {t('metamodelDrafts.page.publish')}
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setDialog('discard')}>
              {t('metamodelDrafts.page.discard')}
            </Button>
          </div>
        ) : null}
        {!canPublish && canManage && !closed ? (
          <p className="mt-2 text-xs text-muted">{t('metamodelDrafts.page.needsPublish')}</p>
        ) : null}
      </Card>

      {reloaded ? (
        <p
          role="status"
          className="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100"
        >
          {t('metamodelDrafts.page.reloaded')}
        </p>
      ) : null}
      {removeError ? (
        <ProblemNotice error={removeError} prefix={t('metamodelDrafts.page.removeFailed')} />
      ) : null}

      {published ? <PublicationResult result={published} /> : null}
      {stale.length > 0 ? (
        <StaleItems
          items={stale}
          all={items}
          writable={writable}
          busy={remove.isPending}
          onRemove={removeItem}
          onDiscard={() => setDialog('discard')}
        />
      ) : publish.error ? (
        <ProblemNotice error={publish.error} prefix={t('metamodelDrafts.page.publishFailed')} />
      ) : null}

      <TestSection
        draft={draft}
        canPublish={canPublish}
        jobStatus={job.data?.status}
        jobProgress={job.data?.progress}
        jobError={job.data?.error}
        jobResult={job.data?.result}
        startError={test.error}
      />

      <PatchList
        items={items}
        writable={writable}
        busy={remove.isPending}
        loading={!draft.items && itemsQuery.isLoading}
        onRemove={removeItem}
      />

      {dialog === 'test' ? (
        <Modal
          open
          onOpenChange={(open) => (open ? undefined : setDialog(null))}
          title={t('metamodelDrafts.page.testTitle', { name: draft.name })}
          description={t('metamodelDrafts.page.testText')}
        >
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setDialog(null)}>
              {t('common.cancel')}
            </Button>
            <Button disabled={!canPublish} onClick={startTest}>
              {t('metamodelDrafts.page.testConfirm')}
            </Button>
          </div>
        </Modal>
      ) : null}
      {dialog === 'publish' ? (
        <Modal
          open
          onOpenChange={(open) => (open ? undefined : setDialog(null))}
          title={t('metamodelDrafts.page.publishTitle', { name: draft.name })}
          description={t('metamodelDrafts.page.publishText')}
        >
          <ItemSummary items={items} />
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setDialog(null)}>
              {t('common.cancel')}
            </Button>
            <Button disabled={!canPublish || draft.status !== 'tested'} onClick={confirmPublish}>
              {t('metamodelDrafts.page.publishConfirm')}
            </Button>
          </div>
        </Modal>
      ) : null}
      {dialog === 'discard' ? (
        <DiscardDialog
          draft={draft}
          onClose={() => setDialog(null)}
          onDiscarded={() => publish.reset()}
        />
      ) : null}
    </div>
  );
}

/** ItemSummary counts the items of a draft per kind (publish confirmation). */
function ItemSummary({ items }: { items: readonly MetamodelDraftItem[] }) {
  const { t } = useTranslation();
  const counts = DRAFT_ITEM_KINDS.map((kind) => ({
    kind,
    count: items.filter((item) => item.kind === kind).length,
  })).filter((entry) => entry.count > 0);
  return (
    <ul aria-label={t('metamodelDrafts.page.summaryLabel')} className="space-y-1 text-sm">
      {counts.map(({ kind, count }) => (
        <li key={kind}>
          {t('metamodelDrafts.page.summaryLine', {
            kind: t(`metamodelDrafts.itemKinds.${kind}`),
            count,
          })}
        </li>
      ))}
    </ul>
  );
}

/** The answer of a publication: the new schema_version per affected type. */
function PublicationResult({ result }: { result: MetamodelPublication }) {
  const { t } = useTranslation();
  const typeName = useTypeName();
  return (
    <section
      role="status"
      aria-label={t('metamodelDrafts.page.publishedTitle')}
      className="rounded-lg border border-green-300 bg-green-50 p-3 text-sm text-green-900 dark:border-green-700 dark:bg-green-950 dark:text-green-100"
    >
      <p className="font-medium">{t('metamodelDrafts.page.publishedTitle')}</p>
      {result.types.length > 0 ? (
        <ul className="mt-1 space-y-0.5">
          {result.types.map((type) => (
            <li key={type.id}>
              {t('metamodelDrafts.page.schemaVersion', {
                type: typeName(type.id),
                version: type.schema_version,
              })}
            </li>
          ))}
        </ul>
      ) : (
        <p>{t('metamodelDrafts.page.noTypes')}</p>
      )}
    </section>
  );
}

/** The stale items of a refused publication (409) with removing and discarding. */
function StaleItems({
  items,
  all,
  writable,
  busy,
  onRemove,
  onDiscard,
}: {
  items: StaleDraftItem[];
  all: readonly MetamodelDraftItem[];
  writable: boolean;
  busy: boolean;
  onRemove: (itemId: string) => void;
  onDiscard: () => void;
}) {
  const { t, i18n } = useTranslation();
  return (
    <section
      role="alert"
      className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-800 dark:border-red-800 dark:bg-red-950 dark:text-red-200"
    >
      <p className="font-medium">{t('metamodelDrafts.page.staleTitle')}</p>
      <ul aria-label={t('metamodelDrafts.page.staleTitle')} className="mt-1 space-y-1">
        {items.map((stale) => {
          const item = all.find((i) => i.id === stale.item_id);
          const name =
            (item ? draftItemName(item, i18n.language) : '') ||
            stale.target_key ||
            stale.target_id ||
            stale.item_id;
          const kind = t(`metamodelDrafts.itemKinds.${stale.kind}`, stale.kind);
          return (
            <li key={stale.item_id} className="flex flex-wrap items-center gap-2">
              <span>
                {t('metamodelDrafts.page.staleLine', {
                  kind,
                  op: t(`metamodelDrafts.ops.${stale.op}`, stale.op),
                  name,
                  reason: t(`metamodelDrafts.page.staleReasons.${stale.reason}`, stale.reason),
                })}
              </span>
              {writable ? (
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={busy}
                  aria-label={t('metamodelDrafts.page.removeItemAction', { name })}
                  onClick={() => onRemove(stale.item_id)}
                >
                  {t('metamodelDrafts.page.removeItem')}
                </Button>
              ) : null}
            </li>
          );
        })}
      </ul>
      {writable ? (
        <div className="mt-2">
          <Button size="sm" variant="ghost" onClick={onDiscard}>
            {t('metamodelDrafts.page.discard')}
          </Button>
        </div>
      ) : null}
    </section>
  );
}

// ─── Patch list ──────────────────────────────────────────────────────────────

function PatchList({
  items,
  writable,
  busy,
  loading,
  onRemove,
}: {
  items: readonly MetamodelDraftItem[];
  writable: boolean;
  busy: boolean;
  loading: boolean;
  onRemove: (itemId: string) => void;
}) {
  const { t } = useTranslation();
  if (loading) return <SkeletonList rows={2} label={t('app.loading')} />;
  if (items.length === 0) {
    return <p className="text-sm text-muted">{t('metamodelDrafts.page.noItems')}</p>;
  }
  const groups = DRAFT_ITEM_KINDS.map((kind) => ({
    kind,
    items: items.filter((item) => item.kind === kind),
  })).filter((group) => group.items.length > 0);
  return (
    <section aria-labelledby="draft-patch-list" className="space-y-3">
      <h3 id="draft-patch-list" className="text-lg font-semibold text-gray-900 dark:text-gray-100">
        {t('metamodelDrafts.page.patchList')}
      </h3>
      {groups.map((group) => (
        <Card
          key={group.kind}
          title={t('metamodelDrafts.page.groupTitle', {
            kind: t(`metamodelDrafts.itemKinds.${group.kind}`),
            count: group.items.length,
          })}
        >
          <ul className="space-y-4">
            {group.items.map((item) => (
              <PatchItem
                key={item.id}
                item={item}
                writable={writable}
                busy={busy}
                onRemove={onRemove}
              />
            ))}
          </ul>
        </Card>
      ))}
    </section>
  );
}

/** The ops that create a part: the diff shows only the new values. */
const CREATING = new Set(['create', 'clone']);

/** A value of a target or payload as the fields of a diff entry. */
function valuesOf(value: Record<string, unknown> | null | undefined): Record<string, unknown> {
  return value ?? {};
}

/**
 * diffSides returns the two lists of the diff of an item: create and clone
 * only the new values, delete the removed object, every other operation the
 * payload against the current state of the target in the payload's fields.
 */
export function diffSides(
  item: MetamodelDraftItem,
  current: Record<string, unknown> | null | undefined,
  label: string,
): { before: DiffEntry[]; after: DiffEntry[] } {
  const payload = payloadOf(item);
  const key = item.id;
  if (CREATING.has(item.op)) {
    return { before: [], after: [{ key, label, values: payload }] };
  }
  if (item.op === 'delete') {
    return { before: [{ key, label, values: valuesOf(current) }], after: [] };
  }
  const state = valuesOf(current);
  const fields = Object.keys(payload);
  const shown: Record<string, unknown> = {};
  for (const field of fields) shown[field] = state[field];
  const after: Record<string, unknown> = { ...payload };
  if (item.op === 'activate' || item.op === 'deactivate') {
    shown.is_active = state.is_active;
    after.is_active = item.op === 'activate';
  }
  // An upsert of a missing field creates it.
  if (current === null && item.op === 'upsert') {
    return { before: [], after: [{ key, label, values: after }] };
  }
  return { before: [{ key, label, values: shown }], after: [{ key, label, values: after }] };
}

function PatchItem({
  item,
  writable,
  busy,
  onRemove,
}: {
  item: MetamodelDraftItem;
  writable: boolean;
  busy: boolean;
  onRemove: (itemId: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const needsTarget = !CREATING.has(item.op);
  const target = useDraftItemTarget(item, { enabled: needsTarget });
  const name =
    draftItemName(item, i18n.language) || item.target_id || t('metamodelDrafts.noTarget');
  const op = t(`metamodelDrafts.ops.${item.op}`);
  const current = needsTarget ? target.data : null;
  const { before, after } = diffSides(item, current, name);
  return (
    <li data-draft-item={item.id} className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="flex flex-wrap items-center gap-2 text-sm">
          <Badge variant="info">{op}</Badge>
          <span className="font-medium">{name}</span>
        </p>
        {writable ? (
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            aria-label={t('metamodelDrafts.page.removeItemAction', { name })}
            onClick={() => onRemove(item.id)}
          >
            {t('metamodelDrafts.page.removeItem')}
          </Button>
        ) : null}
      </div>
      {needsTarget && target.isLoading ? (
        <p className="text-xs text-muted">{t('app.loading')}</p>
      ) : null}
      {needsTarget && target.isSuccess && target.data === null && item.op !== 'upsert' ? (
        <p className="text-xs text-muted">{t('metamodelDrafts.page.targetMissing')}</p>
      ) : null}
      {needsTarget && target.error ? (
        <p className="text-xs text-muted">{t('metamodelDrafts.page.targetUnreadable')}</p>
      ) : null}
      {!needsTarget || !target.isLoading ? (
        <MetamodelDiff
          before={before}
          after={after}
          caption={t('metamodelDrafts.page.diffCaption', { op, name })}
        />
      ) : null}
    </li>
  );
}

// ─── Test ────────────────────────────────────────────────────────────────────

/** useTypeName names a CI type in the UI language; its id while unknown. */
function useTypeName(): (id: string) => string {
  const { i18n } = useTranslation();
  const types = useAllCITypes({ include_inactive: true });
  return (id) => {
    const type = types.data?.find((x) => x.id === id);
    return type ? localizedText(type.display_name, i18n.language) || type.name : id;
  };
}

/** reportOf reads the counters of a finished job (its result), if it has them. */
function reportOf(result: unknown): MetamodelTestReport | undefined {
  if (typeof result !== 'object' || result === null) return undefined;
  const r = result as Partial<MetamodelTestReport>;
  return r.totals && Array.isArray(r.types) ? (r as MetamodelTestReport) : undefined;
}

function TestSection({
  draft,
  canPublish,
  jobStatus,
  jobProgress,
  jobError,
  jobResult,
  startError,
}: {
  draft: MetamodelDraft;
  canPublish: boolean;
  jobStatus?: string;
  jobProgress?: number;
  jobError?: string;
  jobResult?: unknown;
  startError: unknown;
}) {
  const { t } = useTranslation();
  const counters = draft.test_report ?? reportOf(jobResult) ?? null;
  const running = jobStatus === 'queued' || jobStatus === 'running';
  return (
    <Card title={t('metamodelDrafts.page.testSection')}>
      <div className="space-y-3">
        {startError ? (
          <ProblemNotice error={startError} prefix={t('metamodelDrafts.page.testFailed')} />
        ) : null}
        {jobStatus ? (
          <p role="status" className="text-sm">
            {running
              ? t('metamodelDrafts.page.testRunning', { progress: jobProgress ?? 0 })
              : t('metamodelDrafts.page.testJob', {
                  status: t(`jobs.statuses.${jobStatus}`, jobStatus),
                })}
          </p>
        ) : null}
        {jobError ? <p className="text-sm text-red-600 dark:text-red-400">{jobError}</p> : null}
        {counters ? (
          <TestCounters report={counters} />
        ) : (
          <p className="text-sm text-muted">{t('metamodelDrafts.page.notTested')}</p>
        )}
        {counters && canPublish && !running ? <ViolationList draftId={draft.id} /> : null}
      </div>
    </Card>
  );
}

function TestCounters({ report }: { report: MetamodelTestReport }) {
  const { t } = useTranslation();
  const typeName = useTypeName();
  return (
    <div className="space-y-3">
      {report.blocked ? (
        <div
          role="alert"
          className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-800 dark:border-red-800 dark:bg-red-950 dark:text-red-200"
        >
          <p className="font-medium">{t('metamodelDrafts.page.blocked')}</p>
          <ul aria-label={t('metamodelDrafts.page.conflicts')} className="mt-1 space-y-0.5">
            {report.conflicts.map((conflict, i) => (
              <li key={`${conflict.code}-${conflict.key ?? ''}-${i}`}>
                {t('metamodelDrafts.page.conflictLine', {
                  code: conflict.code,
                  key: conflict.key || conflict.ci_type_id || conflict.item_id || '',
                  message: conflict.message,
                })}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {report.outdated ? (
        <p className="text-sm text-muted">{t('metamodelDrafts.page.outdated')}</p>
      ) : null}
      {report.error ? <p className="text-sm text-muted">{report.error}</p> : null}
      <dl className="grid grid-cols-2 gap-2 text-sm sm:grid-cols-4">
        {(['types', 'cis', 'invalid_cis', 'violations'] as const).map((key) => (
          <div key={key}>
            <dt className="text-xs text-muted">{t(`metamodelDrafts.page.totals.${key}`)}</dt>
            <dd className="font-semibold">{report.totals[key]}</dd>
          </div>
        ))}
      </dl>
      {report.types.length > 0 ? (
        <ScrollRegion label={t('metamodelDrafts.page.perType')}>
          <table className="w-full text-left text-sm">
            <caption className="sr-only">{t('metamodelDrafts.page.perType')}</caption>
            <thead className="text-xs uppercase text-muted">
              <tr>
                <th scope="col" className="py-2 pr-3">
                  {t('metamodelDrafts.page.columns.type')}
                </th>
                <th scope="col" className="py-2 pr-3">
                  {t('metamodelDrafts.page.totals.cis')}
                </th>
                <th scope="col" className="py-2 pr-3">
                  {t('metamodelDrafts.page.totals.invalid_cis')}
                </th>
                <th scope="col" className="py-2 pr-3">
                  {t('metamodelDrafts.page.totals.violations')}
                </th>
                <th scope="col" className="py-2">
                  {t('metamodelDrafts.page.columns.attributes')}
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {report.types.map((type) => (
                <tr key={type.ci_type_id}>
                  <th scope="row" className="py-2 pr-3 font-medium">
                    {typeName(type.ci_type_id)}
                  </th>
                  <td className="py-2 pr-3">{type.cis}</td>
                  <td className="py-2 pr-3">{type.invalid_cis}</td>
                  <td className="py-2 pr-3">{type.violations}</td>
                  <td className="py-2 text-xs">
                    {type.attributes
                      .map((a) =>
                        t('metamodelDrafts.page.attributeCount', {
                          key: a.key,
                          count: a.violations,
                        }),
                      )
                      .join(', ')}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      ) : null}
    </div>
  );
}

/** The violations of the report (GET …/test), page by page over next_cursor. */
function ViolationList({ draftId }: { draftId: string }) {
  const { t } = useTranslation();
  const typeName = useTypeName();
  const report = useMetamodelDraftTestReport(draftId);
  if (report.isLoading) return <p className="text-sm text-muted">{t('app.loading')}</p>;
  // Never tested, or the items changed since: no report.
  if (notFound(report.error)) return null;
  if (report.error) {
    return <ProblemNotice error={report.error} prefix={t('metamodelDrafts.page.reportFailed')} />;
  }
  const pages = report.data?.pages ?? [];
  const violations = pages.flatMap((page) => page.data);
  const first = pages[0];
  if (violations.length === 0) {
    return <p className="text-sm text-muted">{t('metamodelDrafts.page.noViolations')}</p>;
  }
  return (
    <div className="space-y-2">
      <ScrollRegion label={t('metamodelDrafts.page.violations')}>
        <table className="w-full text-left text-sm">
          <caption className="sr-only">{t('metamodelDrafts.page.violations')}</caption>
          <thead className="text-xs uppercase text-muted">
            <tr>
              <th scope="col" className="py-2 pr-3">
                {t('metamodelDrafts.page.columns.ci')}
              </th>
              <th scope="col" className="py-2 pr-3">
                {t('metamodelDrafts.page.columns.type')}
              </th>
              <th scope="col" className="py-2 pr-3">
                {t('metamodelDrafts.page.columns.field')}
              </th>
              <th scope="col" className="py-2">
                {t('metamodelDrafts.page.columns.message')}
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {violations.map((v, i) => (
              <tr key={`${v.ci_id}-${v.field}-${i}`}>
                <td className="py-2 pr-3 font-mono text-xs">
                  <Link
                    to={`/cmdb/${v.ci_id}`}
                    className="text-primary hover:underline"
                    aria-label={t('metamodelDrafts.page.openCI', { id: v.ci_id })}
                  >
                    {v.ci_id}
                  </Link>
                </td>
                <td className="py-2 pr-3">{typeName(v.ci_type_id)}</td>
                <td className="py-2 pr-3 font-mono text-xs">{v.field}</td>
                <td className="py-2">{v.message}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </ScrollRegion>
      <p className="text-xs text-muted">
        {t('metamodelDrafts.page.violationCount', {
          shown: violations.length,
          total: first?.total ?? violations.length,
        })}
        {first?.truncated ? ` ${t('metamodelDrafts.page.truncated')}` : ''}
      </p>
      {report.hasNextPage ? (
        <Button
          size="sm"
          variant="secondary"
          disabled={report.isFetchingNextPage}
          onClick={() => void report.fetchNextPage()}
        >
          {t('metamodelDrafts.page.more')}
        </Button>
      ) : null}
    </div>
  );
}
