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
import {
  Check,
  CheckCircle2,
  FlaskConical,
  GitBranch,
  Plus,
  Rocket,
  Trash2,
  TriangleAlert,
} from 'lucide-react';
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react';
import { Link, useParams } from 'react-router';
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
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { Skeleton, SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdNumericClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
} from '../components/ui/table';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
import { MetamodelTabs } from '../components/metamodel/MetamodelTabs';
import { ImportReportNotice } from '../components/metamodel/MetamodelTransfer';
import { MetamodelDiff, type DiffEntry } from '../components/metamodel/MetamodelDiff';
import {
  DRAFT_STATUS_VARIANT,
  draftItemName,
  payloadOf,
} from '../components/metamodel/DraftBanner';
import { DRAFT_ITEM_KINDS, DRAFT_STATUSES, useMetamodelDraftStore } from '../stores/metamodelDraft';

export { DRAFT_STATUSES };

type DraftStatus = MetamodelDraft['status'];

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

  if (moduleOff) {
    return (
      <div className="space-y-6">
        <DraftsHeader />
        <MetamodelTabs />
        <EmptyState icon={<GitBranch />} title={t('templates.moduleOff')} />
      </div>
    );
  }
  return id ? <DraftDetail id={id} /> : <DraftList />;
}

/** DraftsHeader is the heading the three metamodel pages share (title, sentence, actions). */
function DraftsHeader({ actions }: { actions?: ReactNode }) {
  const { t } = useTranslation();
  return (
    <PageHeader
      title={t('citypes.title')}
      description={t('metamodelDrafts.page.subtitle')}
      actions={actions}
    />
  );
}

/** A danger-coloured action on the page canvas: red-600 stays under 4.5:1 there. */
const dangerOnCanvas = 'text-red-700 hover:text-red-700 dark:text-red-300 dark:hover:text-red-300';

/** The panel of a refusal or a problem: the red of ErrorState, with room for details. */
const dangerPanel =
  'rounded-xl border border-red-200 bg-red-50 px-4 py-3.5 text-sm text-red-900 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-100';

/**
 * ProblemNotice shows a refused request as its problem detail (RFC 7807):
 * status, title, detail and the trace id for support (API-03).
 */
function ProblemNotice({ error, prefix }: { error: unknown; prefix: string }) {
  const { t } = useTranslation();
  const api = error instanceof ApiError ? error : undefined;
  const detail = api ? api.detail || api.title || api.message : String(error);
  return (
    <div role="alert" className={cn(dangerPanel, 'flex items-start gap-3')}>
      <TriangleAlert
        aria-hidden="true"
        className="mt-0.5 h-4 w-4 shrink-0 text-red-700 dark:text-red-300"
      />
      <div className="min-w-0 space-y-0.5">
        <p className="font-semibold">{prefix}</p>
        <p className="text-red-800 dark:text-red-200">
          {api ? t('metamodelDrafts.problem.detail', { status: api.status, detail }) : detail}
        </p>
        {api?.traceId ? (
          <p className="font-mono text-xs text-red-800 dark:text-red-200">
            {t('metamodelDrafts.problem.traceId', { traceId: api.traceId })}
          </p>
        ) : null}
      </div>
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
    <div className="space-y-6">
      <DraftsHeader
        actions={
          canManage ? (
            <Button icon={<Plus />} onClick={() => setCreating(true)}>
              {t('metamodelDrafts.create')}
            </Button>
          ) : null
        }
      />
      <MetamodelTabs />

      <div className="space-y-4">
        <Toolbar className="items-end">
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
          {query.isSuccess ? (
            <ToolbarEnd>
              <span className="text-sm tabular-nums text-fg-muted">
                {t('metamodelDrafts.page.count', { count: rows.length })}
              </span>
            </ToolbarEnd>
          ) : null}
        </Toolbar>

        {query.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
        {query.error ? (
          <ErrorState
            title={t('app.error')}
            retryLabel={t('common.retry')}
            onRetry={() => void query.refetch()}
          />
        ) : null}
        {query.isSuccess && rows.length === 0 ? (
          <EmptyState
            icon={<GitBranch />}
            title={t('metamodelDrafts.page.empty')}
            action={
              canManage ? (
                <Button variant="secondary" icon={<Plus />} onClick={() => setCreating(true)}>
                  {t('metamodelDrafts.create')}
                </Button>
              ) : undefined
            }
          />
        ) : null}
        {rows.length > 0 ? (
          <Card className="p-0 overflow-hidden">
            <ul aria-label={t('metamodelDrafts.page.listLabel')} className="divide-y divide-line">
              {rows.map((d) => {
                const open = !isClosed(d.status);
                const isActiveDraft = active?.id === d.id;
                return (
                  <li
                    key={d.id}
                    data-draft-id={d.id}
                    className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 px-4 py-3 text-sm transition-colors hover:bg-fg/[0.025]"
                  >
                    <div className="min-w-0 flex-1 basis-60 space-y-1">
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                        <Link
                          to={`/ci-types/drafts/${d.id}`}
                          className="rounded font-medium text-accent underline-offset-2 hover:underline"
                        >
                          {d.name}
                        </Link>
                        <Badge variant={DRAFT_STATUS_VARIANT[d.status]}>
                          {t(`metamodelDrafts.statuses.${d.status}`)}
                        </Badge>
                        {isActiveDraft ? (
                          <Badge variant="neutral" dot>
                            {t('metamodelDrafts.page.isActive')}
                          </Badge>
                        ) : null}
                      </div>
                      <div className="flex flex-wrap items-center gap-x-4 gap-y-0.5 text-xs text-fg-muted">
                        <span className="tabular-nums">
                          {t('metamodelDrafts.itemCount', { count: d.item_count })}
                        </span>
                        <span>
                          {t('metamodelDrafts.page.createdAt', {
                            at: formatDate(d.created_at, i18n.language),
                          })}
                        </span>
                      </div>
                    </div>
                    {canManage && open ? (
                      <div className="flex flex-wrap items-center gap-1">
                        {!isActiveDraft ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            icon={<Check />}
                            aria-label={t('metamodelDrafts.page.chooseActiveAction', {
                              name: d.name,
                            })}
                            onClick={() => setDraft({ id: d.id, name: d.name })}
                          >
                            {t('metamodelDrafts.page.chooseActive')}
                          </Button>
                        ) : null}
                        <IconButton
                          size="sm"
                          icon={<Trash2 />}
                          label={t('metamodelDrafts.page.discardAction', { name: d.name })}
                          className="hover:text-danger"
                          onClick={() => setDiscarding(d)}
                        />
                      </div>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          </Card>
        ) : null}
      </div>
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
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" icon={<Plus />} disabled={create.isPending || !name.trim()}>
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
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          {t('common.cancel')}
        </Button>
        <Button
          variant="danger"
          icon={<Trash2 />}
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

  if (draftQuery.isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-16 w-full max-w-lg" />
        <MetamodelTabs />
        <SkeletonList rows={3} label={t('app.loading')} />
      </div>
    );
  }
  if (notFound(draftQuery.error)) {
    return (
      <div className="space-y-6">
        <DraftsHeader />
        <MetamodelTabs />
        <EmptyState
          icon={<GitBranch />}
          title={t('metamodelDrafts.page.notFound')}
          action={
            <Button variant="secondary" asChild>
              <Link to="/ci-types/drafts">{t('metamodelDrafts.page.back')}</Link>
            </Button>
          }
        />
      </div>
    );
  }
  if (draftQuery.error || !draft) {
    return (
      <div className="space-y-6">
        <DraftsHeader />
        <MetamodelTabs />
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void draftQuery.refetch()}
        />
      </div>
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
    <div className="space-y-6">
      <PageHeader
        breadcrumbs={[
          { label: t('citypes.title'), to: '/ci-types' },
          { label: t('templates.tabs.drafts'), to: '/ci-types/drafts' },
          { label: draft.name },
        ]}
        title={draft.name}
        meta={
          <>
            <Badge variant={DRAFT_STATUS_VARIANT[draft.status]}>
              {t(`metamodelDrafts.statuses.${draft.status}`)}
            </Badge>
            {isActiveDraft ? (
              <Badge variant="neutral" dot>
                {t('metamodelDrafts.page.isActive')}
              </Badge>
            ) : null}
          </>
        }
        description={
          <span className="flex flex-wrap items-center gap-x-4 gap-y-0.5">
            <span className="tabular-nums">
              {t('metamodelDrafts.itemCount', { count: draft.item_count })}
            </span>
            <span className="tabular-nums">
              {t('metamodelDrafts.page.versionLabel', { version: draft.version })}
            </span>
            <span>
              {t('metamodelDrafts.page.createdAt', {
                at: formatDate(draft.created_at, i18n.language),
              })}
            </span>
            {draft.published_at ? (
              <span>
                {t('metamodelDrafts.page.publishedAt', {
                  at: formatDate(draft.published_at, i18n.language),
                })}
              </span>
            ) : null}
          </span>
        }
        actions={
          canManage && !closed ? (
            <>
              {!isActiveDraft ? (
                <Button
                  variant="secondary"
                  icon={<Check />}
                  onClick={() => setDraft({ id: draft.id, name: draft.name })}
                >
                  {t('metamodelDrafts.page.chooseActive')}
                </Button>
              ) : null}
              <Button
                variant="secondary"
                icon={<FlaskConical />}
                disabled={!canPublish || running || test.isPending || items.length === 0}
                title={canPublish ? undefined : t('metamodelDrafts.page.needsPublish')}
                onClick={() => setDialog('test')}
              >
                {t('metamodelDrafts.page.test')}
              </Button>
              <Button
                variant="ghost"
                icon={<Trash2 />}
                className={dangerOnCanvas}
                onClick={() => setDialog('discard')}
              >
                {t('metamodelDrafts.page.discard')}
              </Button>
              <Button
                icon={<Rocket />}
                disabled={!canPublish || draft.status !== 'tested' || publish.isPending}
                title={canPublish ? undefined : t('metamodelDrafts.page.needsPublish')}
                onClick={() => setDialog('publish')}
              >
                {t('metamodelDrafts.page.publish')}
              </Button>
            </>
          ) : null
        }
      />
      <MetamodelTabs />

      {closed ? (
        <p className="text-sm text-fg-muted">{t('metamodelDrafts.page.readOnly')}</p>
      ) : null}
      {!canPublish && canManage && !closed ? (
        <p className="text-sm text-fg-muted">{t('metamodelDrafts.page.needsPublish')}</p>
      ) : null}
      {/* The report of the import that created this draft (WP-800). */}
      <ImportReportNotice />

      {reloaded ? (
        <p
          role="status"
          className="rounded-xl border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-100"
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
            <Button variant="secondary" onClick={() => setDialog(null)}>
              {t('common.cancel')}
            </Button>
            <Button icon={<FlaskConical />} disabled={!canPublish} onClick={startTest}>
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
            <Button variant="secondary" onClick={() => setDialog(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              icon={<Rocket />}
              disabled={!canPublish || draft.status !== 'tested'}
              onClick={confirmPublish}
            >
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
    <ul aria-label={t('metamodelDrafts.page.summaryLabel')} className="space-y-1 text-sm text-fg">
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
      className="flex items-start gap-3 rounded-xl border border-green-300 bg-green-50 px-4 py-3.5 text-sm text-green-900 dark:border-green-500/30 dark:bg-green-500/10 dark:text-green-100"
    >
      <CheckCircle2
        aria-hidden="true"
        className="mt-0.5 h-4 w-4 shrink-0 text-green-700 dark:text-green-300"
      />
      <div className="min-w-0">
        <p className="font-semibold">{t('metamodelDrafts.page.publishedTitle')}</p>
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
      </div>
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
    <section role="alert" className={dangerPanel}>
      <p className="font-semibold">{t('metamodelDrafts.page.staleTitle')}</p>
      <ul aria-label={t('metamodelDrafts.page.staleTitle')} className="mt-2 space-y-2">
        {items.map((stale) => {
          const item = all.find((i) => i.id === stale.item_id);
          const name =
            (item ? draftItemName(item, i18n.language) : '') ||
            stale.target_key ||
            stale.target_id ||
            stale.item_id;
          const kind = t(`metamodelDrafts.itemKinds.${stale.kind}`, stale.kind);
          return (
            <li key={stale.item_id} className="flex flex-wrap items-center gap-x-3 gap-y-1">
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
        <div className="mt-3">
          <Button
            size="sm"
            variant="secondary"
            icon={<Trash2 />}
            className="text-danger hover:text-danger"
            onClick={onDiscard}
          >
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
    return <EmptyState icon={<GitBranch />} title={t('metamodelDrafts.page.noItems')} />;
  }
  const groups = DRAFT_ITEM_KINDS.map((kind) => ({
    kind,
    items: items.filter((item) => item.kind === kind),
  })).filter((group) => group.items.length > 0);
  return (
    <section aria-labelledby="draft-patch-list" className="space-y-4">
      <h3 id="draft-patch-list" className="text-lg font-semibold text-fg">
        {t('metamodelDrafts.page.patchList')}
      </h3>
      {groups.map((group) => (
        <Card key={group.kind} className="p-0 overflow-hidden">
          <h4 className="border-b border-line bg-canvas/60 px-5 py-3 text-sm font-semibold text-fg">
            {t('metamodelDrafts.page.groupTitle', {
              kind: t(`metamodelDrafts.itemKinds.${group.kind}`),
              count: group.items.length,
            })}
          </h4>
          <ul className="divide-y divide-line">
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

/**
 * The badge of an operation reads like the change column of the diff: what
 * adds is green, what removes is red, what changes is amber.
 */
const OP_VARIANT: Record<string, 'success' | 'danger' | 'warning' | 'info' | 'neutral'> = {
  create: 'success',
  clone: 'success',
  upsert: 'warning',
  update: 'warning',
  delete: 'danger',
  activate: 'info',
  deactivate: 'neutral',
  assign: 'info',
  publish: 'info',
};

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
    <li data-draft-item={item.id} className="space-y-3 px-5 py-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="flex min-w-0 flex-wrap items-center gap-2 text-sm">
          <Badge variant={OP_VARIANT[item.op] ?? 'neutral'}>{op}</Badge>
          <span className="font-medium text-fg [overflow-wrap:anywhere]">{name}</span>
        </p>
        {writable ? (
          <Button
            size="sm"
            variant="ghost"
            icon={<Trash2 />}
            className="hover:text-danger"
            disabled={busy}
            aria-label={t('metamodelDrafts.page.removeItemAction', { name })}
            onClick={() => onRemove(item.id)}
          >
            {t('metamodelDrafts.page.removeItem')}
          </Button>
        ) : null}
      </div>
      {needsTarget && target.isLoading ? (
        <p className="text-xs text-fg-muted">{t('app.loading')}</p>
      ) : null}
      {needsTarget && target.isSuccess && target.data === null && item.op !== 'upsert' ? (
        <p className="text-xs text-fg-muted">{t('metamodelDrafts.page.targetMissing')}</p>
      ) : null}
      {needsTarget && target.error ? (
        <p className="text-xs text-fg-muted">{t('metamodelDrafts.page.targetUnreadable')}</p>
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
    <Card className="p-0 overflow-hidden">
      <div className="space-y-3 px-5 py-4">
        <h3 className="text-base font-semibold text-fg">{t('metamodelDrafts.page.testSection')}</h3>
        {startError ? (
          <ProblemNotice error={startError} prefix={t('metamodelDrafts.page.testFailed')} />
        ) : null}
        {jobStatus ? (
          <p role="status" className="text-sm text-fg">
            {running
              ? t('metamodelDrafts.page.testRunning', { progress: jobProgress ?? 0 })
              : t('metamodelDrafts.page.testJob', {
                  status: t(`jobs.statuses.${jobStatus}`, jobStatus),
                })}
          </p>
        ) : null}
        {jobError ? <p className="text-sm text-danger">{jobError}</p> : null}
        {counters ? null : (
          <p className="text-sm text-fg-muted">{t('metamodelDrafts.page.notTested')}</p>
        )}
      </div>
      {counters ? <TestCounters report={counters} /> : null}
      {counters && canPublish && !running ? <ViolationList draftId={draft.id} /> : null}
    </Card>
  );
}

/** The tables of the test report sit flush in their card: no border of their own. */
const flushTable = '[&_tbody_tr:last-child>*]:border-b-0';

function TestCounters({ report }: { report: MetamodelTestReport }) {
  const { t } = useTranslation();
  const typeName = useTypeName();
  return (
    <div>
      {report.blocked || report.outdated || report.error ? (
        <div className="space-y-3 px-5 pb-4">
          {report.blocked ? (
            <div role="alert" className={dangerPanel}>
              <p className="font-semibold">{t('metamodelDrafts.page.blocked')}</p>
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
            <p className="text-sm text-fg-muted">{t('metamodelDrafts.page.outdated')}</p>
          ) : null}
          {report.error ? <p className="text-sm text-fg-muted">{report.error}</p> : null}
        </div>
      ) : null}
      <dl className="grid grid-cols-2 border-t border-line sm:grid-cols-4 [&>div:nth-child(even)]:border-l [&>div:nth-child(n+3)]:border-t sm:[&>div:nth-child(n+3)]:border-t-0 sm:[&>div+div]:border-l [&>div]:border-line">
        {(['types', 'cis', 'invalid_cis', 'violations'] as const).map((key) => (
          <div key={key} className="px-5 py-3">
            <dt className="text-xs text-fg-muted">{t(`metamodelDrafts.page.totals.${key}`)}</dt>
            <dd
              className={cn(
                'mt-0.5 text-xl font-semibold tabular-nums',
                (key === 'invalid_cis' || key === 'violations') && report.totals[key] > 0
                  ? 'text-danger'
                  : 'text-fg',
              )}
            >
              {report.totals[key]}
            </dd>
          </div>
        ))}
      </dl>
      {report.types.length > 0 ? (
        <div className={cn('border-t border-line', flushTable)}>
          <h4 className="px-5 pb-2 pt-3 text-sm font-medium text-fg">
            {t('metamodelDrafts.page.perType')}
          </h4>
          <ScrollRegion label={t('metamodelDrafts.page.perType')}>
            <table className={tableClasses}>
              <caption className="sr-only">{t('metamodelDrafts.page.perType')}</caption>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('metamodelDrafts.page.columns.type')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('metamodelDrafts.page.totals.cis')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('metamodelDrafts.page.totals.invalid_cis')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('metamodelDrafts.page.totals.violations')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('metamodelDrafts.page.columns.attributes')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {report.types.map((type) => (
                  <tr key={type.ci_type_id}>
                    <th scope="row" className={cn(tdPrimaryClasses, 'whitespace-nowrap text-left')}>
                      {typeName(type.ci_type_id)}
                    </th>
                    <td className={tdNumericClasses}>{type.cis}</td>
                    <td className={tdNumericClasses}>{type.invalid_cis}</td>
                    <td className={tdNumericClasses}>{type.violations}</td>
                    <td className={cn(tdClasses, 'text-xs')}>
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
        </div>
      ) : null}
    </div>
  );
}

/** The violations of the report (GET …/test), page by page over next_cursor. */
function ViolationList({ draftId }: { draftId: string }) {
  const { t } = useTranslation();
  const typeName = useTypeName();
  const report = useMetamodelDraftTestReport(draftId);
  if (report.isLoading) {
    return (
      <p className="border-t border-line px-5 py-4 text-sm text-fg-muted">{t('app.loading')}</p>
    );
  }
  // Never tested, or the items changed since: no report.
  if (notFound(report.error)) return null;
  if (report.error) {
    return (
      <div className="border-t border-line px-5 py-4">
        <ProblemNotice error={report.error} prefix={t('metamodelDrafts.page.reportFailed')} />
      </div>
    );
  }
  const pages = report.data?.pages ?? [];
  const violations = pages.flatMap((page) => page.data);
  const first = pages[0];
  if (violations.length === 0) {
    return (
      <p className="border-t border-line px-5 py-4 text-sm text-fg-muted">
        {t('metamodelDrafts.page.noViolations')}
      </p>
    );
  }
  return (
    <div className="border-t border-line">
      <div className={flushTable}>
        <h4 className="px-5 pb-2 pt-3 text-sm font-medium text-fg">
          {t('metamodelDrafts.page.violations')}
        </h4>
        <ScrollRegion label={t('metamodelDrafts.page.violations')}>
          <table className={tableClasses}>
            <caption className="sr-only">{t('metamodelDrafts.page.violations')}</caption>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('metamodelDrafts.page.columns.ci')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('metamodelDrafts.page.columns.type')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('metamodelDrafts.page.columns.field')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('metamodelDrafts.page.columns.message')}
                </th>
              </tr>
            </thead>
            <tbody>
              {violations.map((v, i) => (
                <tr key={`${v.ci_id}-${v.field}-${i}`}>
                  <td className={cn(tdMonoClasses, 'whitespace-nowrap')}>
                    <Link
                      to={`/cmdb/${v.ci_id}`}
                      className="rounded text-accent underline-offset-2 hover:underline"
                      aria-label={t('metamodelDrafts.page.openCI', { id: v.ci_id })}
                    >
                      {v.ci_id}
                    </Link>
                  </td>
                  <td className={cn(tdPrimaryClasses, 'whitespace-nowrap')}>
                    {typeName(v.ci_type_id)}
                  </td>
                  <td className={tdMonoClasses}>{v.field}</td>
                  <td className={tdClasses}>{v.message}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      </div>
      <div className="flex flex-wrap items-center gap-3 border-t border-line px-5 py-3">
        <p className="text-xs text-fg-muted">
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
    </div>
  );
}
