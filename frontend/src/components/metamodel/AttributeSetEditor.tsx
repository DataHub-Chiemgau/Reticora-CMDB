/**
 * AttributeSetEditor maintains the attributes of an attribute set (MET-16,
 * UI-11, WP-250, WP-797): the list of GET /attribute-definitions
 * ?attribute_set_id=<id>, new attributes by POST with attribute_set_id and
 * changes by PATCH/DELETE, both through the AttributeEditor of WP-313. The
 * order in the set is the `order` of the definitions: after a move (drag and
 * drop, keyboard or the buttons of SortableList) the editor saves the changed
 * values by PATCH and reads the list again. A system set is read-only.
 */
import { Pencil, Plus, Trash2, X } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { localizedText, type AttributeDefinition, type AttributeSet } from '../../api/cmdb';
import {
  useDeleteAttributeDefinition,
  useReorderAttributeDefinitions,
  useSetAttributeDefinitions,
} from '../../api/cmdbHooks';
import { Badge } from '../ui/Badge';
import { Button, IconButton } from '../ui/Button';
import { ErrorState } from '../ui/ErrorState';
import { SkeletonList } from '../ui/Skeleton';
import {
  AttributeEditor,
  describeFlagRefusal,
  type AttributeEditorTarget,
} from './AttributeEditor';
import { SortableList } from './SortableList';

const byOrder = (a: AttributeDefinition, b: AttributeDefinition) =>
  (a.order ?? 0) - (b.order ?? 0) || a.key.localeCompare(b.key);

/**
 * orderChanges returns the PATCHes of a new order: the former values in
 * their sorted sequence when they are distinct (only the moved definitions
 * change), otherwise steps of ten.
 */
export function orderChanges(next: AttributeDefinition[]): { id: string; order: number }[] {
  const values = next.map((d) => d.order ?? 0).sort((a, b) => a - b);
  const distinct = values.every((v, i) => i === 0 || v > values[i - 1]!);
  return next
    .map((d, i) => ({ id: d.id, order: distinct ? values[i]! : (i + 1) * 10, before: d.order }))
    .filter((c) => c.order !== (c.before ?? 0))
    .map(({ id, order }) => ({ id, order }));
}

export interface AttributeSetEditorProps {
  set: AttributeSet;
  onClose?: () => void;
}

export function AttributeSetEditor({ set, onClose }: AttributeSetEditorProps) {
  const { t, i18n } = useTranslation();
  const [query] = useSetAttributeDefinitions([set.id]);
  const reorder = useReorderAttributeDefinitions();
  const deleteDefinition = useDeleteAttributeDefinition();
  const [editor, setEditor] = useState<AttributeEditorTarget | null>(null);
  // The order shown while the PATCHes run and the list is read again.
  const [pending, setPending] = useState<string[] | null>(null);
  const readOnly = set.is_system;
  const setName = localizedText(set.display_name, i18n.language) || set.key;
  const loaded = [...(query?.data?.data ?? [])].sort(byOrder);
  const list = pending
    ? pending
        .map((id) => loaded.find((d) => d.id === id))
        .filter((d): d is AttributeDefinition => !!d)
    : loaded;
  const nameOf = (d: AttributeDefinition) => localizedText(d.display_name, i18n.language) || d.key;

  const onReorder = (next: AttributeDefinition[]) => {
    const changes = orderChanges(next);
    if (changes.length === 0 || reorder.isPending) return;
    setPending(next.map((d) => d.id));
    reorder.mutate(changes, { onSettled: () => setPending(null) });
  };

  const remove = (d: AttributeDefinition) => {
    if (!window.confirm(t('citypes.attributes.deleteConfirm', { name: d.key }))) return;
    deleteDefinition.mutate(d);
  };

  const headingId = `attribute-set-editor-${set.id}`;
  return (
    <div role="region" aria-labelledby={headingId} className="mt-4 border-t border-line pt-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 id={headingId} className="text-sm font-semibold text-fg">
          {t('citypes.sets.editorTitle', { name: setName })}
        </h4>
        <div className="flex flex-wrap gap-2">
          {!readOnly ? (
            <Button
              size="sm"
              variant="secondary"
              icon={<Plus />}
              onClick={() => setEditor({ mode: 'create', scope: 'set', attributeSetId: set.id })}
            >
              {t('citypes.sets.addAttribute')}
            </Button>
          ) : null}
          {onClose ? (
            <Button size="sm" variant="ghost" icon={<X />} onClick={onClose}>
              {t('citypes.sets.closeEditor')}
            </Button>
          ) : null}
        </div>
      </div>
      {readOnly ? (
        <p className="mt-2 text-sm text-fg-muted">{t('citypes.sets.systemReadOnly')}</p>
      ) : (
        <p className="mt-2 text-xs text-fg-muted">{t('citypes.sets.orderHint')}</p>
      )}
      {query?.isLoading ? <SkeletonList rows={2} label={t('app.loading')} /> : null}
      {query?.error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {reorder.error ? (
        <p role="alert" className="mt-2 text-sm text-danger">
          {t('citypes.sets.reorderFailed', { reason: describeFlagRefusal(reorder.error, t) })}
        </p>
      ) : null}
      {deleteDefinition.error ? (
        <p role="alert" className="mt-2 text-sm text-danger">
          {t('citypes.attributes.deleteFailed', {
            name: deleteDefinition.variables?.key,
            reason: describeFlagRefusal(deleteDefinition.error, t),
          })}
        </p>
      ) : null}
      {query?.isSuccess && list.length === 0 ? (
        <p className="mt-2 text-sm text-fg-muted">{t('citypes.sets.noAttributes')}</p>
      ) : null}
      <SortableList
        items={list}
        getId={(d) => d.id}
        getName={nameOf}
        onReorder={onReorder}
        disabled={readOnly}
        label={t('citypes.sets.attributesLabel', { name: setName })}
        className="mt-2 divide-y divide-line"
        renderItem={(d) => (
          <div className="flex flex-wrap items-start justify-between gap-2 text-sm">
            <div className="min-w-0 space-y-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium text-fg">{nameOf(d)}</span>
                {nameOf(d) !== d.key ? (
                  <span className="font-mono text-xs text-fg-muted">{d.key}</span>
                ) : null}
                <span className="text-xs text-fg-muted">
                  {t(`citypes.dataTypes.${d.data_type}`, d.data_type)}
                </span>
              </div>
              <div className="flex flex-wrap gap-1.5">
                {d.required ? <Badge variant="warning">{t('citypes.flagRequired')}</Badge> : null}
                {d.searchable ? (
                  <Badge variant="neutral">{t('citypes.flagSearchable')}</Badge>
                ) : null}
                {d.unique ? <Badge variant="neutral">{t('citypes.flagUnique')}</Badge> : null}
                {d.inheritable ? (
                  <Badge variant="neutral">{t('citypes.flagInheritable')}</Badge>
                ) : null}
              </div>
            </div>
            {!readOnly && !d.is_system ? (
              <div className="flex items-center gap-1">
                <IconButton
                  size="sm"
                  icon={<Pencil />}
                  label={t('citypes.editFieldAction', { name: d.key })}
                  onClick={() => setEditor({ mode: 'edit', definition: d })}
                />
                <IconButton
                  size="sm"
                  icon={<Trash2 />}
                  label={t('citypes.deleteFieldAction', { name: d.key })}
                  className="hover:text-danger"
                  onClick={() => remove(d)}
                />
              </div>
            ) : null}
          </div>
        )}
      />
      {editor ? <AttributeEditor target={editor} onClose={() => setEditor(null)} /> : null}
    </div>
  );
}
