/**
 * IpamImportDialog is the dialog "Import/Export" of the IPAM page (IPM-09,
 * UI-14, WP-922) with the tabs Import and Export.
 *
 * Import (import:run and ipam:write): the kind (subnets or addresses)
 * leads into the import assistant with the phpIPAM profile of that entity
 * (/imports?profile=<id>; the route of the assistant is /imports). A missing profile named "phpIPAM Subnetze" resp.
 * "phpIPAM Adressen" is created first (POST /import-profiles, import:manage):
 * format csv, mode both, every phpIPAM column of WP-920 mapped to the
 * target of the same name and the identity of WP-920 (VRF with CIDR resp.
 * address). Without import:manage and without a profile a notice appears.
 * The profile is looked up over every page of GET /import-profiles; a name
 * held by a profile of another entity (unique per organization, 409 on
 * create) is explained instead of failing generically.
 * Dry run, row report and start stay those of the assistant.
 *
 * Export (export:run and ipam:read): the kind and the current filters of
 * the IPAM page, sent as filters.ipam with format csv and filters.entity
 * (POST /exports, WP-921); afterwards the status names the job and a link
 * leads into the job center (it has no deep link to a single job). An
 * address export takes only the filters GET /ips knows (client and text);
 * the others are shown as not applied. 403 and 400 appear in the dialog.
 */
import { useId, useRef, useState, type KeyboardEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import {
  ApiError,
  importApi,
  type CreateImportProfileRequest,
  type ExportJob,
  type ExportJobIPAMFilters,
  type ImportProfile,
} from '../../api/client';
import { useCreateExportJob, useCreateImportProfile, useHasPermission } from '../../api/hooks';
import { IMPORT_TARGETS } from '../import/MappingEditor';
import { Button } from '../ui/Button';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import { cn } from '../ui/utils';

/** The entities of the dialog: subnets and addresses (IPM-09). */
export type IpamIOEntity = 'subnet' | 'ip_address';
const ENTITIES: IpamIOEntity[] = ['subnet', 'ip_address'];

/**
 * PHPIPAM_PROFILE_NAMES are the names the dialog looks the profiles up by;
 * they are fixed (not translated) so every language finds the same profile.
 */
export const PHPIPAM_PROFILE_NAMES: Record<IpamIOEntity, string> = {
  subnet: 'phpIPAM Subnetze',
  ip_address: 'phpIPAM Adressen',
};

/**
 * listAllProfiles pages through GET /import-profiles (cursor) so a phpIPAM
 * profile beyond the first page is found too; the name is unique per
 * organization (import_profile UNIQUE (organization_id, name)).
 */
export async function listAllProfiles(): Promise<ImportProfile[]> {
  const all: ImportProfile[] = [];
  let cursor: string | undefined;
  for (let guard = 0; guard < 100; guard++) {
    const page = await importApi.listProfiles({ limit: 200, cursor });
    all.push(...page.data);
    if (!page.has_more || !page.next_cursor) break;
    cursor = page.next_cursor;
  }
  return all;
}

/** The identity of a row after WP-920: (VRF, CIDR) resp. (VRF, address). */
const IDENTITY_KEYS: Record<IpamIOEntity, string[]> = {
  subnet: ['vrf', 'cidr'],
  ip_address: ['vrf', 'ip_addr'],
};

/** phpIPAMProfile is the request creating the phpIPAM profile of an entity. */
export function phpIPAMProfile(entity: IpamIOEntity): CreateImportProfileRequest {
  const columns = Object.fromEntries(
    IMPORT_TARGETS[entity].map((target) => [target, { target, transforms: [] }]),
  );
  return {
    name: PHPIPAM_PROFILE_NAMES[entity],
    entity,
    format: 'csv',
    mode: 'both',
    mapping: { columns, defaults: {} },
    identity_keys: IDENTITY_KEYS[entity],
  };
}

/** IpamPageFilters are the filters of the IPAM page the export takes over. */
export interface IpamPageFilters {
  section_id?: string;
  vrf_id?: string;
  client_id?: string;
  site_id?: string;
  q?: string;
  contains?: string;
}

type FilterKey = keyof IpamPageFilters;
const FILTER_KEYS: FilterKey[] = ['section_id', 'vrf_id', 'client_id', 'site_id', 'q', 'contains'];
/** The filters of GET /ips; the others belong to subnets (a 400 for addresses). */
const ADDRESS_FILTERS: FilterKey[] = ['client_id', 'q'];

/** exportFilters are the page filters an export of the entity sends as filters.ipam. */
export function exportFilters(
  entity: IpamIOEntity,
  filters: IpamPageFilters,
): ExportJobIPAMFilters {
  const out: ExportJobIPAMFilters = {};
  for (const key of FILTER_KEYS) {
    const value = filters[key]?.trim();
    if (!value) continue;
    if (entity === 'ip_address' && !ADDRESS_FILTERS.includes(key)) continue;
    out[key] = value;
  }
  return out;
}

export interface IpamImportDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  filters: IpamPageFilters;
  /** The names of the filter values (section, VRF, client, site) by id. */
  labels?: Partial<Record<FilterKey, string>>;
}

type Tab = 'import' | 'export';

/**
 * useIpamIORights reports which tabs the session may use; undefined while
 * the rights load. The action of the IPAM page shows with either.
 */
export function useIpamIORights(): { import: boolean; export: boolean } | undefined {
  const run = useHasPermission('import:run');
  const write = useHasPermission('ipam:write');
  const exportRun = useHasPermission('export:run');
  const read = useHasPermission('ipam:read');
  if ([run, write, exportRun, read].some((r) => r === undefined)) return undefined;
  return { import: run === true && write === true, export: exportRun === true && read === true };
}

export function IpamImportDialog({ open, onOpenChange, filters, labels }: IpamImportDialogProps) {
  const { t } = useTranslation();
  return (
    <Modal open={open} onOpenChange={onOpenChange} size="lg" title={t('ipam.io.title')}>
      {open && <DialogBody filters={filters} labels={labels} onClose={() => onOpenChange(false)} />}
    </Modal>
  );
}

function DialogBody({
  filters,
  labels,
  onClose,
}: {
  filters: IpamPageFilters;
  labels?: IpamImportDialogProps['labels'];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const tabsId = useId();
  const rights = useIpamIORights();
  const tabs: Tab[] = rights ? (['import', 'export'] as const).filter((tab) => rights[tab]) : [];
  const [chosen, setChosen] = useState<Tab>();
  const tab = chosen && tabs.includes(chosen) ? chosen : tabs[0];
  const refs = useRef<Partial<Record<Tab, HTMLButtonElement | null>>>({});

  const onTabKey = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0;
    if (step === 0 || tabs.length < 2) return;
    event.preventDefault();
    const next = tabs[(index + step + tabs.length) % tabs.length];
    if (!next) return;
    setChosen(next);
    refs.current[next]?.focus();
  };

  if (!rights) return <p className="text-sm text-fg-muted">{t('ipam.detail.loading')}</p>;
  if (!tab) return <p className="text-sm text-fg-muted">{t('ipam.io.noRights')}</p>;

  return (
    <div className="space-y-4">
      <div
        role="tablist"
        aria-label={t('ipam.io.tabsLabel')}
        className="flex gap-1 border-b border-line"
      >
        {tabs.map((id, index) => (
          <button
            key={id}
            ref={(node) => {
              refs.current[id] = node;
            }}
            type="button"
            role="tab"
            id={`${tabsId}-tab-${id}`}
            aria-selected={tab === id}
            aria-controls={`${tabsId}-panel-${id}`}
            tabIndex={tab === id ? 0 : -1}
            onClick={() => setChosen(id)}
            onKeyDown={(event) => onTabKey(event, index)}
            className={cn(
              '-mb-px border-b-2 px-3 py-2 text-sm font-medium focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
              tab === id
                ? 'border-accent text-fg'
                : 'border-transparent text-fg-muted hover:text-fg',
            )}
          >
            {t(`ipam.io.tab_${id}`)}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`${tabsId}-panel-${tab}`} aria-labelledby={`${tabsId}-tab-${tab}`}>
        {tab === 'import' ? (
          <ImportPanel onClose={onClose} />
        ) : (
          <ExportPanel filters={filters} labels={labels} onClose={onClose} />
        )}
      </div>
    </div>
  );
}

function errorText(err: unknown, t: (key: string, options?: Record<string, unknown>) => string) {
  if (err instanceof ApiError) {
    if (err.status === 403) return t('ipam.io.forbidden');
    if (err.status === 400 || err.status === 422) {
      return t('ipam.io.badRequest', { detail: err.detail ?? err.title ?? '' });
    }
  }
  return t('ipam.io.failed');
}

function EntitySelect({
  value,
  onChange,
}: {
  value: IpamIOEntity;
  onChange: (value: IpamIOEntity) => void;
}) {
  const { t } = useTranslation();
  return (
    <Select
      label={t('ipam.io.entity')}
      value={value}
      onChange={(event) => onChange(event.target.value as IpamIOEntity)}
      options={ENTITIES.map((entity) => ({
        value: entity,
        label: t(`ipam.io.entities.${entity}`),
      }))}
    />
  );
}

function ImportPanel({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const canManage = useHasPermission('import:manage') === true;
  // The prefix ['import-profiles'] is invalidated by useCreateImportProfile.
  const profiles = useQuery({ queryKey: ['import-profiles', 'all'], queryFn: listAllProfiles });
  const create = useCreateImportProfile();
  const [entity, setEntity] = useState<IpamIOEntity>('subnet');
  const [message, setMessage] = useState<string>();

  const name = PHPIPAM_PROFILE_NAMES[entity];
  const named = (profiles.data ?? []).find((p) => p.name === name);
  const existing = named?.entity === entity ? named : undefined;
  // The name is taken by a profile of another entity: creating it would
  // answer 409, so the dialog says so instead.
  const taken = named !== undefined && !existing;
  const blocked = profiles.isSuccess && !existing && (taken || !canManage);

  const go = async () => {
    setMessage(undefined);
    try {
      const profile = existing ?? (await create.mutateAsync(phpIPAMProfile(entity)));
      onClose();
      void navigate(`/imports?profile=${encodeURIComponent(profile.id)}`);
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // Created in the meantime: look the name up again and reuse it.
        const fresh = (await profiles.refetch()).data?.find((p) => p.name === name);
        if (fresh?.entity === entity) {
          onClose();
          void navigate(`/imports?profile=${encodeURIComponent(fresh.id)}`);
          return;
        }
        setMessage(t('ipam.io.profileNameTaken', { name }));
        return;
      }
      setMessage(errorText(err, t));
    }
  };

  return (
    <div className="space-y-4">
      <p className="text-sm text-fg-muted">{t('ipam.io.importHint')}</p>
      <EntitySelect
        value={entity}
        onChange={(next) => {
          setEntity(next);
          setMessage(undefined);
        }}
      />
      {profiles.isError && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {errorText(profiles.error, t)}
        </p>
      )}
      {existing ? (
        <p className="text-sm text-fg-muted">
          {t('ipam.io.profileFound', { name: existing.name })}
        </p>
      ) : taken ? (
        <p
          role="note"
          className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-fg"
        >
          {t('ipam.io.profileNameTaken', { name })}
        </p>
      ) : profiles.isSuccess && canManage ? (
        <p className="text-sm text-fg-muted">
          {t('ipam.io.profileCreate', { name: PHPIPAM_PROFILE_NAMES[entity] })}
        </p>
      ) : null}
      {blocked && !taken && (
        <p
          role="note"
          className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-fg"
        >
          {t('ipam.io.profileMissing', { name: PHPIPAM_PROFILE_NAMES[entity] })}
        </p>
      )}
      {message && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {message}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onClose}>
          {t('ipam.addressDialog.cancel')}
        </Button>
        <Button
          type="button"
          onClick={() => void go()}
          loading={create.isPending}
          disabled={!profiles.isSuccess || blocked}
        >
          {t('ipam.io.toImport')}
        </Button>
      </div>
    </div>
  );
}

function ExportPanel({
  filters,
  labels,
  onClose,
}: {
  filters: IpamPageFilters;
  labels?: IpamImportDialogProps['labels'];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const create = useCreateExportJob();
  const [entity, setEntity] = useState<IpamIOEntity>('subnet');
  const [job, setJob] = useState<ExportJob>();
  const [message, setMessage] = useState<string>();
  const sent = exportFilters(entity, filters);
  const active = FILTER_KEYS.filter((key) => filters[key]?.trim());

  const start = () => {
    setMessage(undefined);
    setJob(undefined);
    create.mutate(
      { format: 'csv', filters: { entity, ipam: sent } },
      { onSuccess: setJob, onError: (err) => setMessage(errorText(err, t)) },
    );
  };

  return (
    <div className="space-y-4">
      <p className="text-sm text-fg-muted">{t('ipam.io.exportHint')}</p>
      <EntitySelect
        value={entity}
        onChange={(next) => {
          setEntity(next);
          setJob(undefined);
          setMessage(undefined);
        }}
      />
      <section aria-label={t('ipam.io.filters')} className="space-y-1 text-sm">
        <h3 className="font-medium text-fg">{t('ipam.io.filters')}</h3>
        {active.length === 0 ? (
          <p className="text-fg-muted">{t('ipam.io.noFilters')}</p>
        ) : (
          <ul className="space-y-0.5" data-testid="ipam-io-filters">
            {active.map((key) => (
              <li key={key} className="flex flex-wrap gap-x-2">
                <span className="text-fg-muted">{t(`ipam.io.filter_${key}`)}:</span>
                <span className="text-fg [overflow-wrap:anywhere]">
                  {labels?.[key] ?? filters[key]}
                </span>
                {!(key in sent) && (
                  <span className="text-fg-muted">({t('ipam.io.notApplied')})</span>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
      {message && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {message}
        </p>
      )}
      {job && (
        <p role="status" className="text-sm text-fg">
          {t('ipam.io.exportStarted', { id: job.id })}{' '}
          <Link to="/jobs" className="text-accent underline underline-offset-2">
            {t('ipam.io.openJobCenter')}
          </Link>
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onClose}>
          {job ? t('ipam.addressDialog.close') : t('ipam.addressDialog.cancel')}
        </Button>
        <Button type="button" onClick={start} loading={create.isPending}>
          {t('ipam.io.startExport')}
        </Button>
      </div>
    </div>
  );
}
