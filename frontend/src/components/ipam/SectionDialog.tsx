/**
 * SectionDialog creates, edits and deletes an IPAM section (IPM-01,
 * WP-915) and keeps its rights per team (IPM-10, WP-910). The tab
 * "General" holds name, description, client (only when creating; the
 * client of a section does not change), strict_mode, show_vlan and
 * show_vrf; deleting the default section or a section with subnets is 409
 * and says so. The tab "Permissions" lists the teams with level read or
 * write; teams are chosen with the UserTeamPicker (teams only). Without
 * ipam:write every write action is hidden. The server lets only principals
 * without client and site restriction write the rights; a 403 says so.
 */
import { Trash2 } from 'lucide-react';
import { useRef, useState, type FormEvent, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../../api/client';
import type { IPAMSection, IPAMSectionLevel } from '../../api/cmdb';
import { useClients } from '../../api/cmdbHooks';
import {
  useCreateIPAMSection,
  useDeleteIPAMSection,
  useIPAMSectionPermissionMutations,
  useIPAMSectionPermissions,
  useUpdateIPAMSection,
} from '../../api/ipamHooks';
import { UserTeamPicker, type PickedHolder } from '../pickers/UserTeamPicker';
import { Button, IconButton } from '../ui/Button';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import { cn } from '../ui/utils';

/** SECTION_LEVELS are the levels of a section right (ipam_section_permission). */
export const SECTION_LEVELS = ['read', 'write'] as const satisfies readonly IPAMSectionLevel[];

export interface SectionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The section to edit; absent to create one. */
  section?: IPAMSection;
  /** The caller holds ipam:write. */
  canWrite: boolean;
  /** Called with the created section. */
  onCreated?: (section: IPAMSection) => void;
  /** Called after the section was deleted. */
  onDeleted?: () => void;
}

type Tab = 'general' | 'permissions';

export function SectionDialog({
  open,
  onOpenChange,
  section,
  canWrite,
  onCreated,
  onDeleted,
}: SectionDialogProps) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<Tab>('general');
  const tabRefs = useRef<Record<Tab, HTMLButtonElement | null>>({
    general: null,
    permissions: null,
  });
  const tabs: Tab[] = section ? ['general', 'permissions'] : ['general'];
  const onTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0;
    if (step === 0) return;
    event.preventDefault();
    const next = tabs[(index + step + tabs.length) % tabs.length];
    if (!next) return;
    setTab(next);
    tabRefs.current[next]?.focus();
  };
  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      title={
        section
          ? t('ipam.section.editTitle', { name: section.name })
          : t('ipam.section.createTitle')
      }
    >
      {tabs.length > 1 && (
        <div
          role="tablist"
          aria-label={t('ipam.section.tabs')}
          className="mb-4 flex gap-1 border-b border-line"
        >
          {tabs.map((id, index) => (
            <button
              key={id}
              ref={(node) => {
                tabRefs.current[id] = node;
              }}
              type="button"
              role="tab"
              id={`ipam-section-tab-${id}`}
              aria-selected={tab === id}
              aria-controls="ipam-section-panel"
              tabIndex={tab === id ? 0 : -1}
              onClick={() => setTab(id)}
              onKeyDown={(event) => onTabKeyDown(event, index)}
              className={cn(
                '-mb-px border-b-2 px-3 py-2 text-sm font-medium focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
                tab === id
                  ? 'border-accent text-fg'
                  : 'border-transparent text-fg-muted hover:text-fg',
              )}
            >
              {t(`ipam.section.tab.${id}`)}
            </button>
          ))}
        </div>
      )}
      <div
        id="ipam-section-panel"
        role={tabs.length > 1 ? 'tabpanel' : undefined}
        aria-labelledby={tabs.length > 1 ? `ipam-section-tab-${tab}` : undefined}
      >
        {tab === 'general' || !section ? (
          <GeneralTab
            section={section}
            canWrite={canWrite}
            onDone={(created) => {
              if (created) onCreated?.(created);
              onOpenChange(false);
            }}
            onDeleted={() => {
              onDeleted?.();
              onOpenChange(false);
            }}
          />
        ) : (
          <PermissionsTab section={section} canWrite={canWrite} />
        )}
      </div>
    </Modal>
  );
}

function GeneralTab({
  section,
  canWrite,
  onDone,
  onDeleted,
}: {
  section?: IPAMSection;
  canWrite: boolean;
  onDone: (created?: IPAMSection) => void;
  onDeleted: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(section?.name ?? '');
  const [description, setDescription] = useState(section?.description ?? '');
  const [clientId, setClientId] = useState(section?.client_id ?? '');
  const [strictMode, setStrictMode] = useState(section?.strict_mode ?? false);
  const [showVLAN, setShowVLAN] = useState(section?.show_vlan ?? true);
  const [showVRF, setShowVRF] = useState(section?.show_vrf ?? true);
  const [error, setError] = useState<string>();
  const [nameError, setNameError] = useState<string>();
  const clients = useClients(!section && canWrite);
  const create = useCreateIPAMSection();
  const update = useUpdateIPAMSection();
  const remove = useDeleteIPAMSection();
  const readOnly = !canWrite;

  const failed = (err: unknown) => {
    if (err instanceof ApiError && err.field === 'name') {
      setNameError(
        err.status === 409
          ? t('ipam.section.nameTaken')
          : (err.detail ?? t('ipam.section.saveFailed')),
      );
      return;
    }
    if (err instanceof ApiError && err.status === 403) {
      setError(t('ipam.section.forbidden'));
      return;
    }
    setError(t('ipam.section.saveFailed'));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setError(undefined);
    setNameError(undefined);
    if (!name.trim()) {
      setNameError(t('ipam.section.nameRequired'));
      return;
    }
    const data = {
      name: name.trim(),
      description,
      strict_mode: strictMode,
      show_vlan: showVLAN,
      show_vrf: showVRF,
    };
    if (section) {
      update.mutate({ id: section.id, data }, { onSuccess: () => onDone(), onError: failed });
    } else {
      create.mutate(
        { ...data, ...(clientId ? { client_id: clientId } : {}) },
        { onSuccess: (created) => onDone(created), onError: failed },
      );
    }
  };

  const deleteSection = () => {
    if (!section) return;
    setError(undefined);
    remove.mutate(section.id, {
      onSuccess: onDeleted,
      onError: (err) =>
        setError(
          err instanceof ApiError && err.status === 409
            ? t(section.is_default ? 'ipam.section.deleteDefault' : 'ipam.section.deleteInUse')
            : t('ipam.section.deleteFailed'),
        ),
    });
  };

  const checkbox = (id: string, checked: boolean, set: (v: boolean) => void, label: string) => (
    <label htmlFor={id} className="flex items-center gap-2 text-sm text-fg">
      <input
        id={id}
        type="checkbox"
        className="h-4 w-4 rounded border-line-strong"
        checked={checked}
        disabled={readOnly}
        onChange={(event) => set(event.target.checked)}
      />
      {label}
    </label>
  );

  return (
    <form onSubmit={submit} className="space-y-4" noValidate>
      <Input
        label={t('ipam.section.name')}
        value={name}
        onChange={(event) => setName(event.target.value)}
        error={nameError}
        disabled={readOnly}
        required
      />
      <Input
        label={t('ipam.section.description')}
        value={description}
        onChange={(event) => setDescription(event.target.value)}
        disabled={readOnly}
      />
      {section ? (
        <p className="text-sm text-fg-muted">
          {section.client_id ? t('ipam.section.clientFixed') : t('ipam.section.orgWide')}
        </p>
      ) : (
        <Select
          label={t('ipam.section.client')}
          value={clientId}
          onChange={(event) => setClientId(event.target.value)}
          disabled={readOnly}
          options={[
            { value: '', label: t('ipam.section.orgWide') },
            ...(clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name })),
          ]}
        />
      )}
      <fieldset className="space-y-2">
        <legend className="mb-1 text-sm font-medium text-fg">{t('ipam.section.settings')}</legend>
        {checkbox('ipam-section-strict', strictMode, setStrictMode, t('ipam.section.strictMode'))}
        {checkbox('ipam-section-vlan', showVLAN, setShowVLAN, t('ipam.section.showVlan'))}
        {checkbox('ipam-section-vrf', showVRF, setShowVRF, t('ipam.section.showVrf'))}
      </fieldset>
      {error && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {error}
        </p>
      )}
      {canWrite && (
        <div className="flex flex-wrap justify-between gap-2">
          {section && !section.is_default ? (
            <Button
              type="button"
              variant="danger"
              icon={<Trash2 />}
              loading={remove.isPending}
              onClick={deleteSection}
            >
              {t('ipam.section.delete')}
            </Button>
          ) : (
            <span />
          )}
          <Button type="submit" loading={create.isPending || update.isPending}>
            {section ? t('ipam.section.save') : t('ipam.section.create')}
          </Button>
        </div>
      )}
    </form>
  );
}

function PermissionsTab({ section, canWrite }: { section: IPAMSection; canWrite: boolean }) {
  const { t } = useTranslation();
  const permissions = useIPAMSectionPermissions(section.id);
  const { create, update, remove } = useIPAMSectionPermissionMutations(section.id);
  const updateSection = useUpdateIPAMSection();
  const [team, setTeam] = useState<PickedHolder | null>(null);
  const [level, setLevel] = useState<IPAMSectionLevel>('read');
  const [error, setError] = useState<string>();
  const levelOptions = SECTION_LEVELS.map((value) => ({ value, label: t(`ipam.levels.${value}`) }));

  const failed = (err: unknown) => {
    if (err instanceof ApiError && err.status === 403) {
      setError(t('ipam.permissions.forbidden'));
    } else if (err instanceof ApiError && err.status === 409 && err.field === 'acl_enabled') {
      setError(t('ipam.permissions.releaseHasEntries'));
    } else if (err instanceof ApiError && err.status === 409) {
      setError(t('ipam.permissions.duplicate'));
    } else if (err instanceof ApiError && err.status === 422 && err.field === 'team_id') {
      setError(t('ipam.permissions.teamNotFound'));
    } else {
      setError(t('ipam.permissions.saveFailed'));
    }
  };

  const add = (event: FormEvent) => {
    event.preventDefault();
    setError(undefined);
    if (!team) return;
    create.mutate({ team_id: team.id, level }, { onSuccess: () => setTeam(null), onError: failed });
  };

  const rows = permissions.data?.data ?? [];
  // total counts every entry, rows only those of teams the caller sees
  // (row level security on the team): a section with hidden entries is not
  // empty and offers no release.
  const total = permissions.data?.total ?? rows.length;
  const empty = permissions.isSuccess && total === 0;
  return (
    <div className="space-y-4">
      <p className="text-sm text-fg-muted">
        {section.acl_enabled ? t('ipam.permissions.restricted') : t('ipam.permissions.open')}
      </p>
      {permissions.isError && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {t('ipam.permissions.loadFailed')}
        </p>
      )}
      {rows.length > 0 && (
        <ul className="divide-y divide-line rounded-lg border border-line">
          {rows.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center gap-3 px-3 py-2">
              <span className="flex-1 text-sm text-fg">{p.team_name ?? p.team_id}</span>
              {canWrite ? (
                <>
                  <Select
                    aria-label={t('ipam.permissions.levelOf', { team: p.team_name ?? p.team_id })}
                    value={p.level}
                    options={levelOptions}
                    onChange={(event) => {
                      setError(undefined);
                      update.mutate(
                        { id: p.id, level: event.target.value as IPAMSectionLevel },
                        { onError: failed },
                      );
                    }}
                  />
                  <IconButton
                    label={t('ipam.permissions.remove', { team: p.team_name ?? p.team_id })}
                    icon={<Trash2 />}
                    onClick={() => {
                      setError(undefined);
                      remove.mutate(p.id, { onError: failed });
                    }}
                  />
                </>
              ) : (
                <span className="text-sm text-fg-muted">{t(`ipam.levels.${p.level}`)}</span>
              )}
            </li>
          ))}
        </ul>
      )}
      {empty && <p className="text-sm text-fg-muted">{t('ipam.permissions.none')}</p>}
      {permissions.isSuccess && total > rows.length && (
        <p className="text-sm text-fg-muted">
          {t('ipam.permissions.hidden', { count: total - rows.length })}
        </p>
      )}
      {/* E-256: a restricted section without teams stays locked until it is
          opened explicitly (PATCH acl_enabled false, audited). */}
      {empty && section.acl_enabled && (
        <div className="flex flex-wrap items-center gap-3">
          <p className="flex-1 text-sm text-fg-muted">{t('ipam.permissions.lockedEmpty')}</p>
          {canWrite && (
            <Button
              type="button"
              variant="secondary"
              loading={updateSection.isPending}
              onClick={() => {
                setError(undefined);
                updateSection.mutate(
                  { id: section.id, data: { acl_enabled: false } },
                  { onError: failed },
                );
              }}
            >
              {t('ipam.permissions.release')}
            </Button>
          )}
        </div>
      )}
      {canWrite && (
        <form onSubmit={add} className="flex flex-wrap items-end gap-3">
          <div className="min-w-[16rem] flex-1">
            <UserTeamPicker
              label={t('ipam.permissions.team')}
              value={team}
              onChange={setTeam}
              kinds={['team']}
            />
          </div>
          <Select
            label={t('ipam.permissions.level')}
            value={level}
            options={levelOptions}
            onChange={(event) => setLevel(event.target.value as IPAMSectionLevel)}
          />
          <Button type="submit" disabled={!team} loading={create.isPending}>
            {t('ipam.permissions.add')}
          </Button>
        </form>
      )}
      {error && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {error}
        </p>
      )}
    </div>
  );
}
