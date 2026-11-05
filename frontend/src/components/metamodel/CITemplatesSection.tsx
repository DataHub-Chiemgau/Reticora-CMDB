/**
 * CITemplatesSection shows and changes the templates of a CI (MET-16,
 * UI-11, ABN-01 (c), WP-800) among the advanced sections of the CI page:
 * the ordered list of GET /api/v1/cis/{id}/templates with template, version
 * and position, adding (only published versions of active templates whose
 * CI type is the type of the CI or a type of its chain) and removing, the
 * order by mouse and keyboard (SortableList, E-54) or the buttons up and
 * down, and saving the list [{template_id, version}] by PUT with If-Match
 * of the CI version (ci:write; 412 reloads, a 422 shows the violations per
 * field). "Migrate to version n" opens the MigrateDialog for this CI.
 *
 * The section needs citype:manage (fail-closed) and the module metamodel
 * (E-105); otherwise it renders nothing and reads nothing, so users without
 * the right never see the word "template" (UI-12).
 */
import { Plus, Rocket, X } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { localizedText, type CITemplate, type Template } from '../../api/cmdb';
import {
  useAllCITypes,
  useCITemplates,
  useReplaceCITemplates,
  useTemplates,
  useTemplateVersions,
} from '../../api/cmdbHooks';
import { ApiError } from '../../api/client';
import { useHasPermission } from '../../api/hooks';
import { useModules } from '../../hooks/useEntitlements';
import { CollapsibleSection } from '../cmdb/CIDetailSections';
import { Button } from '../ui/Button';
import { Select } from '../ui/Select';
import { SkeletonList } from '../ui/Skeleton';
import { SortableList } from './SortableList';
import { ancestorsOf } from './TypeTree';
import { MigrateDialog, ProblemNotice } from './MigrateDialog';

export interface CITemplatesSectionProps {
  ciId: string;
  ciTypeId: string;
  /** The CI version the page shows, sent as If-Match. */
  version?: number;
}

export function CITemplatesSection(props: CITemplatesSectionProps) {
  const canManage = useHasPermission('citype:manage') === true;
  const modules = useModules();
  const on = canManage && !modules.isLoading && modules.isModuleEnabled('metamodel');
  if (!on) return null;
  return <TemplatesBody {...props} />;
}

type Entry = Pick<CITemplate, 'template_id' | 'key' | 'version'>;

function TemplatesBody({ ciId, ciTypeId, version }: CITemplatesSectionProps) {
  const { t, i18n } = useTranslation();
  const qc = useQueryClient();
  const canWrite = useHasPermission('ci:write') === true;
  const assigned = useCITemplates(ciId);
  const templates = useTemplates();
  const types = useAllCITypes({ include_inactive: true });
  const replace = useReplaceCITemplates(ciId);
  const [entries, setEntries] = useState<Entry[] | null>(null);
  const [reloaded, setReloaded] = useState(false);
  const [migrate, setMigrate] = useState<{ template: Template; from: number; to: number } | null>(
    null,
  );

  const byId = useMemo(
    () => new Map((templates.data?.data ?? []).map((tpl) => [tpl.id, tpl])),
    [templates.data],
  );
  const server = useMemo(
    () =>
      [...(assigned.data?.data ?? [])]
        .sort((a, b) => a.position - b.position)
        .map(({ template_id, key, version: v }) => ({ template_id, key, version: v })),
    [assigned.data],
  );
  const shown = entries ?? server;
  const dirty = entries !== null;
  const chain = useMemo(
    () => new Set([ciTypeId, ...ancestorsOf(ciTypeId, types.data ?? []).map((ty) => ty.id)]),
    [ciTypeId, types.data],
  );
  const candidates = (templates.data?.data ?? []).filter(
    (tpl) =>
      tpl.is_active &&
      chain.has(tpl.ci_type_id) &&
      !shown.some((entry) => entry.template_id === tpl.id),
  );

  const nameOf = (entry: Entry) => {
    const tpl = byId.get(entry.template_id);
    return (tpl && localizedText(tpl.display_name, i18n.language)) || entry.key;
  };
  const apiError = replace.error instanceof ApiError ? replace.error : undefined;
  const violationsAt = (index: number) =>
    (apiError?.status === 422 ? apiError.violations : []).filter(
      (v) => v.field === `templates[${index}]` || v.field.startsWith(`templates[${index}].`),
    );

  const save = () => {
    setReloaded(false);
    replace.mutate(
      {
        templates: shown.map((entry) => ({
          template_id: entry.template_id,
          version: entry.version,
        })),
        version,
      },
      {
        onSuccess: () => setEntries(null),
        onError: (error) => {
          if (error instanceof ApiError && error.status === 412) {
            // Changed meanwhile: the CI and its list are read again.
            setEntries(null);
            setReloaded(true);
            void qc.invalidateQueries({ queryKey: ['ci', ciId] });
          }
        },
      },
    );
  };

  return (
    <CollapsibleSection title={t('templates.ciSection.title')} count={assigned.data?.data.length}>
      <div className="space-y-3">
        <p className="text-xs text-fg-muted">{t('templates.ciSection.hint')}</p>
        {assigned.isLoading ? <SkeletonList rows={2} label={t('app.loading')} /> : null}
        {assigned.error ? (
          <ProblemNotice error={assigned.error} prefix={t('templates.ciSection.loadError')} />
        ) : null}
        {assigned.isSuccess && shown.length === 0 ? (
          <p className="text-sm text-fg-muted">{t('templates.ciSection.empty')}</p>
        ) : null}
        {shown.length > 0 ? (
          <SortableList
            items={shown}
            getId={(entry) => entry.template_id}
            getName={nameOf}
            label={t('templates.ciSection.listLabel')}
            disabled={!canWrite}
            className="divide-y divide-line"
            onReorder={(next) => setEntries(next)}
            renderItem={(entry, index) => {
              const tpl = byId.get(entry.template_id);
              const current = tpl?.current_version;
              const errors = violationsAt(index);
              return (
                <div className="space-y-1 text-sm" data-template-key={entry.key}>
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="font-medium text-fg">{nameOf(entry)}</span>
                    <span className="text-xs text-fg-muted">
                      {t('templates.ciSection.entryMeta', {
                        version: entry.version,
                        position: index + 1,
                      })}
                    </span>
                    {!dirty && tpl && current != null && current > entry.version ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        icon={<Rocket />}
                        className="text-accent hover:text-accent-strong"
                        onClick={() =>
                          setMigrate({ template: tpl, from: entry.version, to: current })
                        }
                      >
                        {t('templates.ciSection.migrate', { version: current })}
                      </Button>
                    ) : null}
                    {canWrite ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        icon={<X />}
                        className="text-danger hover:text-danger"
                        aria-label={t('templates.ciSection.removeAction', { name: nameOf(entry) })}
                        onClick={() =>
                          setEntries(shown.filter((e) => e.template_id !== entry.template_id))
                        }
                      >
                        {t('templates.ciSection.remove')}
                      </Button>
                    ) : null}
                  </div>
                  {errors.map((v, i) => (
                    <p key={i} className="text-xs text-danger">
                      {v.detail}
                    </p>
                  ))}
                </div>
              );
            }}
          />
        ) : null}

        {canWrite ? (
          <AddTemplate candidates={candidates} onAdd={(entry) => setEntries([...shown, entry])} />
        ) : (
          <p className="text-xs text-fg-muted">{t('templates.ciSection.needsWrite')}</p>
        )}

        {reloaded ? (
          <p role="status" className="text-sm text-amber-800 dark:text-amber-300">
            {t('templates.ciSection.reloaded')}
          </p>
        ) : null}
        {replace.error && !(apiError?.status === 412) ? (
          <ProblemNotice error={replace.error} prefix={t('templates.ciSection.notSaved')} />
        ) : null}

        {canWrite && dirty ? (
          <div className="flex flex-wrap justify-end gap-2">
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                replace.reset();
                setEntries(null);
              }}
            >
              {t('templates.ciSection.discard')}
            </Button>
            <Button size="sm" disabled={replace.isPending} onClick={save}>
              {t('templates.ciSection.save')}
            </Button>
          </div>
        ) : null}
      </div>
      {migrate ? (
        <MigrateDialog
          open
          template={migrate.template}
          ciIds={[ciId]}
          fromVersion={migrate.from}
          toVersion={migrate.to}
          onClose={() => setMigrate(null)}
        />
      ) : null}
    </CollapsibleSection>
  );
}

/** AddTemplate chooses a template of the type chain and one of its published versions. */
function AddTemplate({
  candidates,
  onAdd,
}: {
  candidates: Template[];
  onAdd: (entry: Entry) => void;
}) {
  const { t, i18n } = useTranslation();
  const [templateId, setTemplateId] = useState('');
  const [version, setVersion] = useState('');
  const chosen = candidates.find((tpl) => tpl.id === templateId);
  const versions = useTemplateVersions(chosen?.id);
  const published = (versions.data?.data ?? [])
    .filter((v) => v.status === 'published')
    .sort((a, b) => b.version - a.version);
  const selectedVersion = version || (published[0] ? String(published[0].version) : '');

  return (
    <div className="flex flex-wrap items-end gap-3">
      <div className="min-w-[12rem]">
        <Select
          label={t('templates.ciSection.addTemplate')}
          value={templateId}
          onChange={(e) => {
            setTemplateId(e.target.value);
            setVersion('');
          }}
          options={[
            { value: '', label: t('templates.ciSection.chooseTemplate') },
            ...candidates.map((tpl) => ({
              value: tpl.id,
              label: localizedText(tpl.display_name, i18n.language) || tpl.key,
            })),
          ]}
        />
      </div>
      <div className="min-w-[8rem]">
        <Select
          label={t('templates.ciSection.addVersion')}
          value={selectedVersion}
          disabled={!chosen || published.length === 0}
          onChange={(e) => setVersion(e.target.value)}
          options={
            published.length > 0
              ? published.map((v) => ({
                  value: String(v.version),
                  label: t('templates.versionShort', { version: v.version }),
                }))
              : [{ value: '', label: t('templates.ciSection.noPublished') }]
          }
        />
      </div>
      <Button
        variant="secondary"
        icon={<Plus />}
        disabled={!chosen || !selectedVersion}
        onClick={() => {
          if (!chosen || !selectedVersion) return;
          onAdd({ template_id: chosen.id, key: chosen.key, version: Number(selectedVersion) });
          setTemplateId('');
          setVersion('');
        }}
      >
        {t('templates.ciSection.add')}
      </Button>
    </div>
  );
}
