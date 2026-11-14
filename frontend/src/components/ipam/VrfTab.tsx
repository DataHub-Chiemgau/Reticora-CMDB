/**
 * VrfTab is the tab "VRFs" of the IPAM page (IPM-07, IPM-19, WP-918): the
 * VRFs with name, route distinguisher, import and export targets, their
 * sections and the mark of the default VRF. Creating, changing and
 * deleting needs vrf:manage and a principal without client or site
 * restriction (000148); otherwise the actions are hidden. The default VRF
 * has no delete action; its RD and targets are not editable (WP-907
 * refuses them with 422), its sections are. An invalid RD or RT format
 * (422) appears at its field, a VRF with subnets (409) as the hint of the
 * answer.
 */
import { Pencil, Plus, Trash2 } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, type FieldErrorInfo } from '../../api/client';
import type { IPAMVRF } from '../../api/cmdb';
import { useIPAMSections, useVRFList, useVRFMutations } from '../../api/ipamHooks';
import { ScrollRegion } from '../ScrollRegion';
import { Badge } from '../ui/Badge';
import { Button, IconButton } from '../ui/Button';
import { Card } from '../ui/Card';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Textarea } from '../ui/Textarea';
import { tableClasses, tdClasses, thClasses, theadClasses, trClasses } from '../ui/table';

export interface VrfTabProps {
  /** The caller holds vrf:manage and has no client or site restriction. */
  canManage: boolean;
}

/** splitTargets reads route targets separated by commas, spaces or lines. */
export function splitTargets(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

export function VrfTab({ canManage }: VrfTabProps) {
  const { t } = useTranslation();
  const vrfs = useVRFList();
  const sections = useIPAMSections();
  const { remove } = useVRFMutations();
  const [editing, setEditing] = useState<IPAMVRF | 'new'>();
  const [message, setMessage] = useState<string>();
  const rows = vrfs.data?.pages.flatMap((p) => p.data ?? []) ?? [];
  const sectionName = (id: string) => sections.data?.data?.find((s) => s.id === id)?.name ?? id;

  const onDelete = (vrf: IPAMVRF) => {
    setMessage(undefined);
    remove.mutate(vrf.id, {
      onError: (err) =>
        setMessage(
          err instanceof ApiError && err.detail ? err.detail : t('ipam.vrfs.deleteFailed'),
        ),
    });
  };

  return (
    <Card className="space-y-3 p-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <h2 className="text-base font-semibold text-fg">{t('ipam.vrfs.title')}</h2>
        {canManage && (
          <Button icon={<Plus />} onClick={() => setEditing('new')}>
            {t('ipam.vrfs.new')}
          </Button>
        )}
      </div>
      {message && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {message}
        </p>
      )}
      {vrfs.isError ? (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {t('ipam.vrfs.loadFailed')}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-fg-muted">
          {vrfs.isPending ? t('ipam.detail.loading') : t('ipam.vrfs.empty')}
        </p>
      ) : (
        <ScrollRegion label={t('ipam.vrfs.title')}>
          <table className={tableClasses}>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('ipam.vrfs.name')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('ipam.vrfs.rd')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('ipam.vrfs.importTargets')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('ipam.vrfs.exportTargets')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('ipam.vrfs.sections')}
                </th>
                {canManage && (
                  <th scope="col" className={thClasses}>
                    <span className="sr-only">{t('ipam.vrfs.actions')}</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {rows.map((vrf) => (
                <tr key={vrf.id} className={trClasses} data-testid={`vrf-row-${vrf.id}`}>
                  <td className={tdClasses}>
                    <span className="font-medium text-fg">{vrf.name}</span>{' '}
                    {vrf.is_default && <Badge>{t('ipam.vrfs.default')}</Badge>}
                  </td>
                  <td className={tdClasses}>{vrf.route_distinguisher}</td>
                  <td className={tdClasses}>{vrf.import_targets.join(', ')}</td>
                  <td className={tdClasses}>{vrf.export_targets.join(', ')}</td>
                  <td className={tdClasses}>{vrf.section_ids.map(sectionName).join(', ')}</td>
                  {canManage && (
                    <td className={tdClasses}>
                      <span className="flex gap-1">
                        <IconButton
                          icon={<Pencil />}
                          label={t('ipam.vrfs.edit', { name: vrf.name })}
                          onClick={() => setEditing(vrf)}
                        />
                        {!vrf.is_default && (
                          <IconButton
                            icon={<Trash2 />}
                            label={t('ipam.vrfs.delete', { name: vrf.name })}
                            onClick={() => onDelete(vrf)}
                          />
                        )}
                      </span>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      )}
      {vrfs.hasNextPage && (
        <Button variant="ghost" size="sm" onClick={() => void vrfs.fetchNextPage()}>
          {t('ipam.tree.loadMore')}
        </Button>
      )}
      {editing && (
        <VrfForm
          vrf={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(undefined)}
        />
      )}
    </Card>
  );
}

function VrfForm({ vrf, onClose }: { vrf?: IPAMVRF; onClose: () => void }) {
  const { t } = useTranslation();
  const [name, setName] = useState(vrf?.name ?? '');
  const [rd, setRd] = useState(vrf?.route_distinguisher ?? '');
  const [imports, setImports] = useState((vrf?.import_targets ?? []).join('\n'));
  const [exports, setExports] = useState((vrf?.export_targets ?? []).join('\n'));
  const [sectionIds, setSectionIds] = useState<string[]>(vrf?.section_ids ?? []);
  const [errors, setErrors] = useState<Record<string, FieldErrorInfo>>({});
  const [message, setMessage] = useState<string>();
  const sections = useIPAMSections();
  const { create, update } = useVRFMutations();
  const isDefault = vrf?.is_default === true;

  const failed = (err: unknown) => {
    if (err instanceof ApiError) {
      const fields = err.fieldErrors();
      // A field error of a target names its list (import_targets[2] → import_targets).
      const byList: Record<string, FieldErrorInfo> = {};
      for (const [field, info] of Object.entries(fields)) {
        byList[field.replace(/\[\d+\]$/, '')] ??= info;
      }
      if (Object.keys(byList).length > 0) {
        setErrors(byList);
        return;
      }
      if (err.detail) {
        setMessage(err.detail);
        return;
      }
    }
    setMessage(t('ipam.vrfs.saveFailed'));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setErrors({});
    setMessage(undefined);
    if (!name.trim()) {
      setErrors({ name: { kind: 'invalid', message: t('ipam.vrfs.nameRequired') } });
      return;
    }
    const done = { onSuccess: onClose, onError: failed };
    if (isDefault && vrf) {
      // The default VRF keeps RD and targets; name and sections change.
      update.mutate({ id: vrf.id, data: { name: name.trim(), section_ids: sectionIds } }, done);
      return;
    }
    const data = {
      name: name.trim(),
      route_distinguisher: rd.trim(),
      import_targets: splitTargets(imports),
      export_targets: splitTargets(exports),
      section_ids: sectionIds,
    };
    if (vrf) update.mutate({ id: vrf.id, data }, done);
    else
      create.mutate({ ...data, route_distinguisher: data.route_distinguisher || undefined }, done);
  };

  const toggle = (id: string, on: boolean) =>
    setSectionIds((ids) => (on ? [...ids, id] : ids.filter((x) => x !== id)));

  return (
    <Modal
      open
      onOpenChange={(open) => !open && onClose()}
      title={vrf ? t('ipam.vrfs.editTitle', { name: vrf.name }) : t('ipam.vrfs.new')}
    >
      <form onSubmit={submit} className="space-y-3" noValidate>
        <Input
          label={t('ipam.vrfs.name')}
          value={name}
          onChange={(event) => setName(event.target.value)}
          error={errors.name?.message}
        />
        {isDefault && <p className="text-sm text-fg-muted">{t('ipam.vrfs.defaultHint')}</p>}
        <Input
          label={t('ipam.vrfs.rd')}
          value={rd}
          onChange={(event) => setRd(event.target.value)}
          error={errors.route_distinguisher?.message}
          placeholder="65000:100"
          disabled={isDefault}
        />
        <Textarea
          label={t('ipam.vrfs.importTargets')}
          value={imports}
          onChange={(event) => setImports(event.target.value)}
          error={errors.import_targets?.message}
          rows={2}
          disabled={isDefault}
        />
        <Textarea
          label={t('ipam.vrfs.exportTargets')}
          value={exports}
          onChange={(event) => setExports(event.target.value)}
          error={errors.export_targets?.message}
          rows={2}
          disabled={isDefault}
        />
        <fieldset
          className="space-y-1"
          aria-describedby={errors.section_ids ? 'vrf-sections-error' : undefined}
        >
          <legend className="text-sm font-medium text-fg">{t('ipam.vrfs.sections')}</legend>
          {(sections.data?.data ?? []).map((section) => (
            <label key={section.id} className="flex items-center gap-2 text-sm text-fg">
              <input
                type="checkbox"
                checked={sectionIds.includes(section.id)}
                onChange={(event) => toggle(section.id, event.target.checked)}
              />
              {section.name}
            </label>
          ))}
          {errors.section_ids && (
            <p id="vrf-sections-error" className="text-sm text-red-700 dark:text-red-300">
              {errors.section_ids.message}
            </p>
          )}
        </fieldset>
        {message && (
          <p role="alert" className="text-sm text-red-700 dark:text-red-300">
            {message}
          </p>
        )}
        <div className="flex justify-end">
          <Button type="submit" loading={create.isPending || update.isPending}>
            {t('ipam.vrfs.save')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
