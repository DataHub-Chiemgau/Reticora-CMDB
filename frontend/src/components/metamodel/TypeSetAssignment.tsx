/**
 * TypeSetAssignment assigns attribute sets to a CI type in order (MET-16,
 * UI-11, WP-250, WP-797): GET /ci-types/{id}/attribute-sets and a PUT of the
 * whole ordered list {ci_type_id, attribute_sets: [{attribute_set_id,
 * position}]} after every change (add, remove, move with SortableList).
 * Only active sets can be added. A system type gets no sets (422 of
 * WP-250), so the component offers none for it. A 422 on attribute_set_id
 * (an inactive or invisible set, a composition conflict of WP-709) shows at
 * the entry the change concerned; the list stays as changed until it is
 * corrected.
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../../api/client';
import { localizedText, type AttributeSet, type CIType } from '../../api/cmdb';
import { useCITypeAttributeSets, useReplaceCITypeAttributeSets } from '../../api/cmdbHooks';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Select } from '../ui/Select';
import { describeFlagRefusal, fieldDetail } from './AttributeEditor';
import { SortableList } from './SortableList';

export interface TypeSetAssignmentProps {
  type: CIType;
  /** Every set the organization sees (GET /attribute-sets). */
  sets: AttributeSet[];
}

export function TypeSetAssignment({ type, sets }: TypeSetAssignmentProps) {
  const { t, i18n } = useTranslation();
  const assignment = useCITypeAttributeSets(type.id, { enabled: !type.is_system });
  const replace = useReplaceCITypeAttributeSets();
  // The list as changed until the PUT succeeded, and the entry it concerned.
  const [draft, setDraft] = useState<string[] | null>(null);
  const [changed, setChanged] = useState<string | undefined>();
  const [choice, setChoice] = useState('');

  const setName = (id: string) => {
    const set = sets.find((s) => s.id === id);
    return set ? localizedText(set.display_name, i18n.language) || set.key : id;
  };
  const headingId = `type-sets-${type.id}`;

  if (type.is_system) {
    return (
      <div className="mt-4">
        <h4 className="text-sm font-semibold">{t('citypes.sets.assignment.title')}</h4>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
          {t('citypes.sets.assignment.systemType')}
        </p>
      </div>
    );
  }

  const saved = [...(assignment.data?.attribute_sets ?? [])]
    .sort((a, b) => a.position - b.position)
    .map((a) => a.attribute_set_id);
  const ids = draft ?? saved;
  const addable = sets
    .filter((s) => s.is_active && !ids.includes(s.id))
    .sort((a, b) => setName(a.id).localeCompare(setName(b.id)));
  // A type without the route (404, module metamodel off) has no sets.
  const notFound = assignment.error instanceof ApiError && assignment.error.status === 404;

  const error = replace.error;
  const atEntry =
    error instanceof ApiError &&
    error.field === 'attribute_set_id' &&
    !!changed &&
    ids.includes(changed)
      ? changed
      : undefined;

  const save = (next: string[], entry: string | undefined) => {
    setDraft(next);
    setChanged(entry);
    replace.mutate(
      {
        ciTypeId: type.id,
        sets: next.map((id, index) => ({ attribute_set_id: id, position: index + 1 })),
      },
      {
        onSuccess: () => {
          setDraft(null);
          setChanged(undefined);
        },
      },
    );
  };

  return (
    <div className="mt-4" role="group" aria-labelledby={headingId}>
      <h4 id={headingId} className="text-sm font-semibold">
        {t('citypes.sets.assignment.title')}
      </h4>
      <p className="mt-1 text-xs text-muted">{t('citypes.sets.assignment.hint')}</p>
      {assignment.error && !notFound ? (
        <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-400">
          {t('citypes.sets.assignment.loadFailed', {
            reason: describeFlagRefusal(assignment.error, t),
          })}
        </p>
      ) : null}
      {error && !atEntry ? (
        <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-400">
          {t('citypes.sets.assignment.notSaved', { reason: describeFlagRefusal(error, t) })}
        </p>
      ) : null}
      {assignment.isSuccess && ids.length === 0 ? (
        <p className="mt-2 text-sm text-muted">{t('citypes.sets.assignment.empty')}</p>
      ) : null}
      <SortableList
        items={ids}
        getId={(id) => id}
        getName={setName}
        onReorder={(next, moved) => {
          if (!replace.isPending) save(next, moved);
        }}
        label={t('citypes.sets.assignment.listLabel', {
          type: localizedText(type.display_name, i18n.language) || type.name,
        })}
        className="mt-2 divide-y divide-gray-100 dark:divide-gray-800"
        renderItem={(id) => {
          const set = sets.find((s) => s.id === id);
          const entryError = atEntry === id && error instanceof ApiError ? error : undefined;
          const errorId = `type-set-${type.id}-${id}-error`;
          return (
            <div className="space-y-1 text-sm">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium" aria-describedby={entryError ? errorId : undefined}>
                    {setName(id)}
                  </span>
                  {set?.is_system ? <Badge variant="info">{t('citypes.system')}</Badge> : null}
                  {set && !set.is_active ? (
                    <Badge variant="warning">{t('citypes.inactive')}</Badge>
                  ) : null}
                </div>
                <button
                  type="button"
                  className="text-xs text-danger hover:underline"
                  disabled={replace.isPending}
                  aria-label={t('citypes.sets.assignment.removeAction', { name: setName(id) })}
                  onClick={() =>
                    save(
                      ids.filter((x) => x !== id),
                      undefined,
                    )
                  }
                >
                  {t('citypes.sets.assignment.remove')}
                </button>
              </div>
              {entryError ? (
                <p id={errorId} role="alert" className="text-sm text-red-600 dark:text-red-400">
                  {t('citypes.sets.assignment.entryError', { detail: fieldDetail(entryError) })}
                </p>
              ) : null}
            </div>
          );
        }}
      />
      <div className="mt-3 flex flex-wrap items-end gap-2">
        <Select
          label={t('citypes.sets.assignment.choose')}
          options={[
            { value: '', label: t('citypes.sets.assignment.choosePlaceholder') },
            ...addable.map((s) => ({ value: s.id, label: setName(s.id) })),
          ]}
          value={choice}
          onChange={(e) => setChoice(e.target.value)}
          disabled={addable.length === 0}
        />
        <Button
          size="sm"
          variant="secondary"
          disabled={!choice || replace.isPending}
          onClick={() => {
            save([...ids, choice], choice);
            setChoice('');
          }}
        >
          {t('citypes.sets.assignment.add')}
        </Button>
      </div>
    </div>
  );
}
