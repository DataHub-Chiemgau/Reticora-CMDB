/**
 * DraftBanner shows the active metamodel draft on every page (MET-53,
 * UI-11, WP-798): name, status and number of items, the pending items
 * (kind, operation, target) and the actions to open the draft in its work
 * area (/ci-types/drafts/{id}, WP-799), create a draft, switch to another
 * one and leave it. Without an active draft it offers "work in a
 * draft" in the metamodel area (/ci-types*). It needs citype:manage and the
 * module metamodel (E-105); with the module off, a vanished (404),
 * published or discarded draft the store is reset, so withDraft adds
 * nothing and the editors write directly again (MET-04).
 *
 * DraftMarker and the helpers below lay the items over the productive state
 * in the editors (E-113): the badge "in draft" at each target and
 * placeholder rows for new parts from the item payload.
 */
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  useCreateMetamodelDraft,
  useMetamodelDraft,
  useMetamodelDraftItems,
  useOpenMetamodelDrafts,
} from '../../api/cmdbHooks';
import { localizedText, type LocalizedText, type MetamodelDraftItem } from '../../api/cmdb';
import { ApiError } from '../../api/client';
import { useEffectivePermissions, useHasPermission } from '../../api/hooks';
import { useModules } from '../../hooks/useEntitlements';
import { useMetamodelDraftStore, type DraftItemKind } from '../../stores/metamodelDraft';
import { useToastStore } from '../../stores/toast';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';

/** How a draft changes a target: labels metamodelDrafts.marker.*. */
export type DraftChange = 'created' | 'updated' | 'deleted';

/** A target in an editor: the kinds that change it, its id and its key. */
export interface DraftTarget {
  kinds: readonly DraftItemKind[];
  id?: string;
  /** The field name of ci_type_field and global_field items. */
  key?: string;
  /** For ci_type_field: the type the field belongs to (target_id of the item). */
  typeId?: string;
}

const NEW_PART_OPS = new Set(['create', 'clone']);

function matches(item: MetamodelDraftItem, target: DraftTarget): boolean {
  if (!target.kinds.includes(item.kind)) return false;
  if (item.kind === 'ci_type_field' || item.kind === 'global_field') {
    if (!target.key || item.target_key !== target.key) return false;
    return item.kind !== 'ci_type_field' || !target.typeId || item.target_id === target.typeId;
  }
  // A clone names its source as target: the source itself stays unchanged.
  return !!target.id && item.target_id === target.id && item.op !== 'clone';
}

/**
 * draftChangeFor returns how the items change a target, or undefined: a
 * deletion wins, every other operation on an existing target changes it.
 */
export function draftChangeFor(
  items: readonly MetamodelDraftItem[],
  target: DraftTarget,
): DraftChange | undefined {
  const own = items.filter((item) => matches(item, target));
  if (own.length === 0) return undefined;
  return own.some((item) => item.op === 'delete') ? 'deleted' : 'updated';
}

/**
 * draftCreations returns the items of a kind that create a new part
 * (create or clone), optionally only those whose payload passes a filter:
 * the placeholder rows of the editors.
 */
export function draftCreations(
  items: readonly MetamodelDraftItem[],
  kind: DraftItemKind,
  filter: (payload: Record<string, unknown>) => boolean = () => true,
): MetamodelDraftItem[] {
  return items.filter(
    (item) => item.kind === kind && NEW_PART_OPS.has(item.op) && filter(payloadOf(item)),
  );
}

/** payloadOf is the captured request body of an item as a plain object. */
export function payloadOf(item: MetamodelDraftItem): Record<string, unknown> {
  const payload: unknown = item.payload;
  return typeof payload === 'object' && payload !== null && !Array.isArray(payload)
    ? (payload as Record<string, unknown>)
    : {};
}

/**
 * draftItemName names the part of an item from its payload (display name in
 * the UI language, name or key), else its target key; empty without one.
 */
export function draftItemName(item: MetamodelDraftItem, language: string): string {
  const payload = payloadOf(item);
  const display =
    typeof payload.display_name === 'object' && payload.display_name !== null
      ? localizedText(payload.display_name as LocalizedText, language)
      : '';
  for (const value of [display, payload.name, payload.key, item.target_key]) {
    if (typeof value === 'string' && value !== '') return value;
  }
  return '';
}

/** DraftMarker is the badge "in draft" of a target (new, changed or deleted). */
export function DraftMarker({ change }: { change: DraftChange }) {
  const { t } = useTranslation();
  return (
    <Badge variant="warning" className="whitespace-nowrap">
      {t(`metamodelDrafts.marker.${change}`)}
    </Badge>
  );
}

const notFound = (error: unknown) => error instanceof ApiError && error.status === 404;

export function DraftBanner() {
  const { t, i18n } = useTranslation();
  const location = useLocation();
  const draft = useMetamodelDraftStore((s) => s.draft);
  const itemRevision = useMetamodelDraftStore((s) => s.itemRevision);
  const clear = useMetamodelDraftStore((s) => s.clear);
  const pushToast = useToastStore((s) => s.push);
  const permission = useHasPermission('citype:manage');
  // Reset only on a confirmed missing right, not while the rights cannot be read.
  const lacksRight = useEffectivePermissions().isSuccess && permission === false;
  const modules = useModules();
  const moduleOff = !!modules.modules && !modules.isModuleEnabled('metamodel');
  const active = !!draft && permission === true && !moduleOff;
  const draftQuery = useMetamodelDraft(draft?.id, { enabled: active });
  const items = useMetamodelDraftItems(draft?.id, { enabled: active });
  const [chooser, setChooser] = useState(false);

  // The module off (E-105), a missing right, a vanished or a closed draft:
  // the editors write directly again.
  const status = draftQuery.data?.status;
  const gone =
    moduleOff ||
    lacksRight ||
    notFound(draftQuery.error) ||
    notFound(items.error) ||
    status === 'published' ||
    status === 'discarded';
  useEffect(() => {
    if (draft && gone) clear();
  }, [draft, gone, clear]);

  // Each captured write (202) is confirmed once; the status may have
  // returned from tested to draft.
  const seenRevision = useRef(itemRevision);
  const { refetch } = draftQuery;
  useEffect(() => {
    if (itemRevision === seenRevision.current) return;
    seenRevision.current = itemRevision;
    if (!draft) return;
    pushToast({ message: t('metamodelDrafts.captured', { name: draft.name }), variant: 'success' });
    if (active) void refetch();
  }, [itemRevision, draft, active, pushToast, refetch, t]);

  if (permission !== true || moduleOff) return null;
  const inMetamodel =
    location.pathname === '/ci-types' || location.pathname.startsWith('/ci-types/');

  if (!draft) {
    if (!inMetamodel) return null;
    return (
      <section
        aria-label={t('metamodelDrafts.bannerLabel')}
        className="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 bg-gray-50 px-6 py-2 text-sm text-gray-700 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-200"
      >
        <p>{t('metamodelDrafts.directHint')}</p>
        <Button size="sm" variant="secondary" onClick={() => setChooser(true)}>
          {t('metamodelDrafts.workInDraft')}
        </Button>
        {chooser ? <DraftChooser onClose={() => setChooser(false)} /> : null}
      </section>
    );
  }

  const list = items.data ?? [];
  const count = items.data ? list.length : (draftQuery.data?.item_count ?? 0);
  return (
    <section
      aria-label={t('metamodelDrafts.bannerLabel')}
      className="border-b border-amber-300 bg-amber-50 px-6 py-3 text-sm text-amber-900 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="flex flex-wrap items-center gap-2">
          <span>{t('metamodelDrafts.activeDraft', { name: draft.name })}</span>
          {status ? <Badge variant="info">{t(`metamodelDrafts.statuses.${status}`)}</Badge> : null}
          <span>{t('metamodelDrafts.itemCount', { count })}</span>
        </p>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="secondary" asChild>
            <Link to={`/ci-types/drafts/${draft.id}`}>{t('metamodelDrafts.open')}</Link>
          </Button>
          <Button size="sm" variant="secondary" onClick={() => setChooser(true)}>
            {t('metamodelDrafts.switch')}
          </Button>
          <Button size="sm" variant="ghost" onClick={clear}>
            {t('metamodelDrafts.leave')}
          </Button>
        </div>
      </div>
      <p className="mt-1 text-xs">{t('metamodelDrafts.notProductive')}</p>
      {list.length > 0 ? (
        <details className="mt-2">
          <summary className="cursor-pointer text-xs font-medium">
            {t('metamodelDrafts.showItems', { count: list.length })}
          </summary>
          <ul aria-label={t('metamodelDrafts.itemsLabel')} className="mt-1 space-y-0.5 text-xs">
            {list.map((item) => (
              <li key={item.id}>
                {t('metamodelDrafts.itemLine', {
                  kind: t(`metamodelDrafts.itemKinds.${item.kind}`),
                  op: t(`metamodelDrafts.ops.${item.op}`),
                  target:
                    draftItemName(item, i18n.language) ||
                    item.target_id ||
                    t('metamodelDrafts.noTarget'),
                })}
              </li>
            ))}
          </ul>
        </details>
      ) : null}
      {chooser ? <DraftChooser onClose={() => setChooser(false)} /> : null}
    </section>
  );
}

/**
 * DraftChooser creates a draft (POST /metamodel/drafts) or switches to an
 * open one (status draft or tested).
 */
function DraftChooser({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const current = useMetamodelDraftStore((s) => s.draft);
  const setDraft = useMetamodelDraftStore((s) => s.setDraft);
  const create = useCreateMetamodelDraft();
  const open = useOpenMetamodelDrafts();
  const [name, setName] = useState('');
  const others = (open.data ?? []).filter((d) => d.id !== current?.id);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate(name.trim(), {
      onSuccess: (created) => {
        setDraft({ id: created.id, name: created.name });
        onClose();
      },
    });
  };

  return (
    <Modal
      open
      onOpenChange={(isOpen) => (isOpen ? undefined : onClose())}
      title={t('metamodelDrafts.chooserTitle')}
    >
      <form onSubmit={submit} className="space-y-2">
        <Input
          label={t('metamodelDrafts.newName')}
          value={name}
          required
          maxLength={200}
          onChange={(e) => setName(e.target.value)}
        />
        {create.error ? (
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">
            {t('metamodelDrafts.createFailed')}
          </p>
        ) : null}
        <div className="flex justify-end">
          <Button type="submit" size="sm" disabled={create.isPending || !name.trim()}>
            {t('metamodelDrafts.create')}
          </Button>
        </div>
      </form>
      <h3 className="mt-4 text-sm font-semibold">{t('metamodelDrafts.openDrafts')}</h3>
      {open.isLoading ? <p className="text-sm text-muted">{t('app.loading')}</p> : null}
      {open.error ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {t('app.error')}
        </p>
      ) : null}
      {open.isSuccess && others.length === 0 ? (
        <p className="text-sm text-muted">{t('metamodelDrafts.noOpenDrafts')}</p>
      ) : null}
      {others.length > 0 ? (
        <ul className="mt-1 divide-y divide-gray-100 text-sm dark:divide-gray-800">
          {others.map((d) => (
            <li key={d.id} className="flex items-center justify-between gap-2 py-1.5">
              <span>
                {d.name} · {t(`metamodelDrafts.statuses.${d.status}`)} ·{' '}
                {t('metamodelDrafts.itemCount', { count: d.item_count })}
              </span>
              <Button
                size="sm"
                variant="secondary"
                aria-label={t('metamodelDrafts.switchTo', { name: d.name })}
                onClick={() => {
                  setDraft({ id: d.id, name: d.name });
                  onClose();
                }}
              >
                {t('metamodelDrafts.switchAction')}
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
    </Modal>
  );
}
