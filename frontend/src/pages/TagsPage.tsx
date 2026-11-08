import { useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Pencil, Plus, Tags, Trash2 } from 'lucide-react';
import { ApiError } from '../api/client';
import { tagLabel, type Tag, type TagDisplayName } from '../api/cmdb';
import { useCreateTag, useDeleteTag, useTags, useUpdateTag } from '../api/cmdbHooks';
import { useHasPermission } from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { ScrollRegion } from '../components/ScrollRegion';

/** PAGE_SIZE is the page of the catalog the table shows (limit, API-02). */
const PAGE_SIZE = 50;

/** The key of a tag as the server accepts it (CreateTagRequest.key). */
const KEY_PATTERN = /^[a-z0-9][a-z0-9_-]{0,62}$/;
/** A color as the server accepts it: #rrggbb in lower case. */
const COLOR_PATTERN = /^#[0-9a-f]{6}$/;

/**
 * refusal words a refused tag write in the UI language: 409 for a taken key
 * or a tag still assigned (or a system tag), 422 with the named fields, 403
 * for a missing tag:manage.
 */
function refusal(error: unknown, t: TFunction, action: 'save' | 'delete'): string {
  if (error instanceof ApiError) {
    if (error.status === 409) {
      return action === 'delete' ? t('tags.errors.inUse') : t('tags.errors.keyTaken');
    }
    if (error.status === 403) return t('tags.errors.forbidden');
    if (error.status === 404) return t('tags.errors.notFound');
    if (error.status === 422 || error.status === 400) {
      const fields = error.violations.map((v) => v.field);
      if (error.field) fields.push(error.field);
      return t('tags.errors.invalid', {
        detail: fields.length > 0 ? fields.join(', ') : (error.detail ?? error.message),
      });
    }
    return t('tags.errors.reason', { detail: error.detail ?? error.message });
  }
  return t('tags.errors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

/** Swatch is the color of a tag as a small square; no color shows nothing. */
function Swatch({ color }: { color?: string | null }) {
  if (!color) return null;
  return (
    <span
      aria-hidden="true"
      className="inline-block h-3.5 w-3.5 shrink-0 rounded border border-line"
      style={{ backgroundColor: color }}
    />
  );
}

type DialogState = { mode: 'create' } | { mode: 'edit'; tag: Tag };

/**
 * TagsPage is the tag catalog of the organization (CI-08, UI-14, WP-817):
 * page by page over limit and cursor, with create, rename (de/en), color
 * and delete for holders of tag:manage (E-106). The key is unchangeable
 * after creation, system tags (archived) cannot be deleted, a tag still
 * assigned is refused with 409. Tags are not merged here.
 */
export function TagsPage() {
  const { t, i18n } = useTranslation();
  const mayManage = useHasPermission('tag:manage') === true;
  // The cursors of the pages shown so far; the last one is the current page.
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);
  const cursor = cursors[cursors.length - 1];
  const tags = useTags({ limit: PAGE_SIZE, ...(cursor ? { cursor } : {}) });
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const [deleting, setDeleting] = useState<Tag | null>(null);

  const rows = tags.data?.data ?? [];
  const nextCursor = tags.data && 'next_cursor' in tags.data ? tags.data.next_cursor : undefined;
  const forbidden = tags.error instanceof ApiError && tags.error.status === 403;

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('tags.title')}
        description={t('tags.subtitle')}
        actions={
          mayManage ? (
            <Button icon={<Plus />} onClick={() => setDialog({ mode: 'create' })}>
              {t('tags.create')}
            </Button>
          ) : null
        }
      />
      {!mayManage ? <p className="text-sm text-fg-muted">{t('tags.readOnly')}</p> : null}

      <Card className="overflow-hidden p-0">
        {tags.isLoading ? (
          <div className="p-5">
            <SkeletonList rows={4} label={t('app.loading')} />
          </div>
        ) : forbidden ? (
          <div className="p-5">
            <ErrorState title={t('tags.errors.forbiddenRead')} />
          </div>
        ) : tags.error ? (
          <div className="p-5">
            <ErrorState
              title={t('tags.loadError')}
              retryLabel={t('common.retry')}
              onRetry={() => void tags.refetch()}
            />
          </div>
        ) : rows.length === 0 ? (
          <div className="p-5">
            <EmptyState icon={<Tags />} title={t('tags.empty')} />
          </div>
        ) : (
          <ScrollRegion label={t('tags.title')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('tags.fields.name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('tags.fields.key')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('tags.fields.nameDe')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('tags.fields.nameEn')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('tags.fields.color')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('tags.fields.kind')}
                  </th>
                  {mayManage ? (
                    <th scope="col" className={thClasses}>
                      <span className="sr-only">{t('common.actions')}</span>
                    </th>
                  ) : null}
                </tr>
              </thead>
              <tbody>
                {rows.map((tag) => {
                  const label = tagLabel(tag, i18n.language);
                  return (
                    <tr key={tag.id} className={trClasses}>
                      <td className={tdPrimaryClasses}>
                        <span className="flex items-center gap-2">
                          <Swatch color={tag.color} />
                          <span className="[overflow-wrap:anywhere]">{label}</span>
                        </span>
                      </td>
                      <td className={tdClasses}>
                        <code className="font-mono text-xs">{tag.key}</code>
                      </td>
                      <td className={tdClasses}>{tag.display_name?.de ?? '–'}</td>
                      <td className={tdClasses}>{tag.display_name?.en ?? '–'}</td>
                      <td className={tdClasses}>
                        <code className="font-mono text-xs">{tag.color ?? '–'}</code>
                      </td>
                      <td className={tdClasses}>
                        {tag.is_system ? (
                          <Badge variant="info">{t('tags.system')}</Badge>
                        ) : (
                          <Badge variant="neutral">{t('tags.custom')}</Badge>
                        )}
                      </td>
                      {mayManage ? (
                        <td className={tdClasses}>
                          <span className="flex justify-end gap-1">
                            <IconButton
                              size="sm"
                              icon={<Pencil />}
                              label={t('tags.editLabel', { name: label })}
                              onClick={() => setDialog({ mode: 'edit', tag })}
                            />
                            {tag.is_system ? null : (
                              <IconButton
                                size="sm"
                                icon={<Trash2 />}
                                label={t('tags.deleteLabel', { name: label })}
                                onClick={() => setDeleting(tag)}
                                className="hover:text-danger"
                              />
                            )}
                          </span>
                        </td>
                      ) : null}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </ScrollRegion>
        )}
        {cursors.length > 1 || nextCursor ? (
          <div className="flex justify-end gap-2 border-t border-line px-5 py-3">
            <Button
              variant="secondary"
              size="sm"
              disabled={cursors.length <= 1}
              onClick={() => setCursors((c) => c.slice(0, -1))}
            >
              {t('common.back')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={!nextCursor}
              onClick={() => nextCursor && setCursors((c) => [...c, nextCursor])}
            >
              {t('common.next')}
            </Button>
          </div>
        ) : null}
      </Card>

      {dialog ? (
        <TagDialog
          key={dialog.mode === 'edit' ? dialog.tag.id : 'create'}
          state={dialog}
          onClose={() => setDialog(null)}
        />
      ) : null}
      <DeleteTagDialog tag={deleting} onClose={() => setDeleting(null)} />
    </div>
  );
}

/** TagDialog creates a tag or changes name and color of one; the key stays. */
function TagDialog({ state, onClose }: { state: DialogState; onClose: () => void }) {
  const { t } = useTranslation();
  const editing = state.mode === 'edit' ? state.tag : undefined;
  const [key, setKey] = useState(editing?.key ?? '');
  const [nameDe, setNameDe] = useState(editing?.display_name?.de ?? '');
  const [nameEn, setNameEn] = useState(editing?.display_name?.en ?? '');
  const [color, setColor] = useState(editing?.color ?? '');
  const [notice, setNotice] = useState<string>();
  const create = useCreateTag();
  const update = useUpdateTag();
  const saving = create.isPending || update.isPending;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setNotice(undefined);
    const trimmedKey = key.trim();
    const normalizedColor = color.trim().toLowerCase();
    if (!editing && !KEY_PATTERN.test(trimmedKey)) {
      setNotice(t('tags.errors.keyFormat'));
      return;
    }
    if (normalizedColor && !COLOR_PATTERN.test(normalizedColor)) {
      setNotice(t('tags.errors.colorFormat'));
      return;
    }
    const names: TagDisplayName = {};
    if (nameDe.trim()) names.de = nameDe.trim();
    if (nameEn.trim()) names.en = nameEn.trim();
    const hasName = names.de !== undefined || names.en !== undefined;
    const done = { onSuccess: onClose, onError: (e: unknown) => setNotice(refusal(e, t, 'save')) };
    if (editing) {
      // A tag keeps a name: the server refuses an empty display_name.
      if (!hasName) {
        setNotice(t('tags.errors.nameRequired'));
        return;
      }
      update.mutate(
        { id: editing.id, data: { display_name: names, color: normalizedColor || null } },
        done,
      );
      return;
    }
    // Without a name the server names the tag by its key.
    create.mutate(
      {
        key: trimmedKey,
        ...(hasName ? { display_name: names } : {}),
        ...(normalizedColor ? { color: normalizedColor } : {}),
      },
      done,
    );
  };

  return (
    <Modal
      open
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={editing ? t('tags.editTitle') : t('tags.createTitle')}
    >
      <form className="space-y-4" onSubmit={submit} noValidate>
        <div>
          <Input
            label={t('tags.fields.key')}
            value={key}
            disabled={!!editing}
            onChange={(e) => setKey(e.target.value)}
            className="font-mono"
          />
          <p className="mt-1 text-sm text-fg-muted">
            {editing ? t('tags.keyFixed') : t('tags.keyHint')}
          </p>
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Input
            label={t('tags.fields.nameDe')}
            value={nameDe}
            onChange={(e) => setNameDe(e.target.value)}
          />
          <Input
            label={t('tags.fields.nameEn')}
            value={nameEn}
            onChange={(e) => setNameEn(e.target.value)}
          />
        </div>
        <div>
          <div className="flex items-end gap-2">
            <div className="min-w-0 flex-1">
              <Input
                label={t('tags.fields.color')}
                value={color}
                placeholder="#rrggbb"
                onChange={(e) => setColor(e.target.value)}
                className="font-mono"
              />
            </div>
            {/* The preview of a valid color next to the field. */}
            <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-line">
              <Swatch
                color={COLOR_PATTERN.test(color.trim().toLowerCase()) ? color.trim() : null}
              />
            </span>
          </div>
          <p className="mt-1 text-sm text-fg-muted">{t('tags.colorHint')}</p>
        </div>
        {notice ? (
          <p role="alert" className="text-sm text-danger">
            {notice}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={saving}>
            {editing ? t('common.save') : t('common.create')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** DeleteTagDialog confirms the deletion; a tag still assigned is 409. */
function DeleteTagDialog({ tag, onClose }: { tag: Tag | null; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const remove = useDeleteTag();
  // The refusal of one tag is not shown for the next.
  const close = () => {
    remove.reset();
    onClose();
  };
  return (
    <Modal
      open={!!tag}
      onOpenChange={(open) => (open ? undefined : close())}
      title={t('tags.deleteTitle')}
      description={
        tag ? t('tags.deleteConfirm', { name: tagLabel(tag, i18n.language) }) : undefined
      }
    >
      {remove.error ? (
        <p role="alert" className="mb-3 text-sm text-danger">
          {refusal(remove.error, t, 'delete')}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={close}>
          {t('common.cancel')}
        </Button>
        <Button
          type="button"
          variant="danger"
          loading={remove.isPending}
          onClick={() => tag && remove.mutate(tag.id, { onSuccess: close })}
        >
          {t('common.delete')}
        </Button>
      </div>
    </Modal>
  );
}
