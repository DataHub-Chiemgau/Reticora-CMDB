/**
 * MonitoringSettingsPage is the page /monitoring/settings (PRB-01, CRD-04,
 * UI-14, WP-1080), reached over the action "Einstellungen" of the
 * MonitoringPage; lock and visibility follow the page monitoring
 * (pageOfPath). Without monitoring:read it shows a hint and sends no
 * request, without monitoring:write every field is read only.
 *
 * "Standardwerte": the level (organization, client, monitoring group) and
 * the probe type (only status available) choose the profile the
 * ProfileEditor edits; the values above (type default, org, client) are
 * the inherited ones. On the level organization stands the switch
 * monitoring.auto_ping (WP-949): it reads GET /org and sends the settings
 * with only this key changed (PATCH /org, user:manage).
 *
 * "Monitoring-Gruppen": name, client, priority and member count; create,
 * rename, change the priority, delete with a confirmation (the answer names
 * the deleted profiles); members are added with the ObjectPicker (CIs,
 * several, at most 500 per call) and removed; a 422 for a CI of another
 * client appears at the picker. Each group has its credential bindings
 * (GroupCredentialsPanel).
 */
import { Plus, Trash2, Users } from 'lucide-react';
import { useQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import {
  ApiError,
  MONITORING_GROUP_MAX_MEMBERS,
  type MonitoringGroup,
  type MonitoringProfileLevel,
  type ProbeType,
  type SessionClaims,
  sessionApi,
} from '../api/client';
import { useClients } from '../api/cmdbHooks';
import { useCI, useHasPermission } from '../api/hooks';
import {
  useGroupMemberMutations,
  useGroupMembers,
  useGroupMutations,
  useMonitoringGroups,
  useMonitoringProfiles,
  useOrganizationSettings,
  useProbeTypes,
  useSetAutoPing,
} from '../api/monitoringSettingsHooks';
import { GroupCredentialsPanel } from '../components/monitoring/GroupCredentialsPanel';
import { ProfileEditor, type InheritedLayer } from '../components/monitoring/ProfileEditor';
import { ObjectPicker, type PickedObject } from '../components/pickers/ObjectPicker';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  thClasses,
  theadClasses,
  trClasses,
} from '../components/ui/table';
import { ScrollRegion } from '../components/ScrollRegion';

type SettingsLevel = Exclude<MonitoringProfileLevel, 'ci'>;
const LEVELS: SettingsLevel[] = ['org', 'client', 'group'];

export function MonitoringSettingsPage() {
  const { t } = useTranslation();
  const canRead = useHasPermission('monitoring:read');
  const canWrite = useHasPermission('monitoring:write') === true;
  const header = (
    <PageHeader
      title={t('monitoring.settings.title')}
      description={t('monitoring.settings.subtitle')}
      actions={
        <Button asChild variant="secondary">
          <Link to="/monitoring">{t('monitoring.settings.back')}</Link>
        </Button>
      }
    />
  );
  if (canRead === undefined) {
    return (
      <div className="space-y-6">
        {header}
        <p className="text-sm text-fg-muted">{t('app.loading')}</p>
      </div>
    );
  }
  if (!canRead) {
    return (
      <div className="space-y-6">
        {header}
        <EmptyState
          title={t('monitoring.settings.noRead')}
          description={t('monitoring.settings.noReadHint')}
        />
      </div>
    );
  }
  return (
    <div className="space-y-6">
      {header}
      {!canWrite ? (
        <p role="note" className="text-sm text-fg-muted">
          {t('monitoring.settings.readOnly')}
        </p>
      ) : null}
      <DefaultsCard readOnly={!canWrite} />
      <GroupsCard readOnly={!canWrite} />
    </div>
  );
}

function DefaultsCard({ readOnly }: { readOnly: boolean }) {
  const { t } = useTranslation();
  const types = useProbeTypes();
  const groups = useMonitoringGroups();
  const clients = useClients();
  const [level, setLevel] = useState<SettingsLevel>('org');
  const [clientId, setClientId] = useState('');
  const [groupId, setGroupId] = useState('');
  const [typeKey, setTypeKey] = useState('');

  const available = (types.data ?? []).filter((x) => x.status === 'available');
  const type: ProbeType | undefined = available.find((x) => x.key === typeKey) ?? available[0];
  const group = (groups.data ?? []).find((g) => g.id === groupId);
  const levelId = level === 'client' ? clientId : level === 'group' ? groupId : undefined;
  // The client whose profile lies above: the chosen one or the group's.
  const upperClient = level === 'group' ? group?.client_id : undefined;
  const ready = !!type && (level === 'org' || !!levelId);

  const orgProfiles = useMonitoringProfiles({ level: 'org', probe_type: type?.key }, !!type);
  const clientProfiles = useMonitoringProfiles(
    {
      level: 'client',
      level_id: level === 'client' ? clientId : upperClient,
      probe_type: type?.key,
    },
    !!type && (level === 'client' ? !!clientId : !!upperClient),
  );
  const groupProfiles = useMonitoringProfiles(
    { level: 'group', level_id: groupId, probe_type: type?.key },
    !!type && level === 'group' && !!groupId,
  );
  const orgProfile = orgProfiles.data?.[0];
  const clientProfile = clientProfiles.data?.[0];
  const own =
    level === 'org' ? orgProfile : level === 'client' ? clientProfile : groupProfiles.data?.[0];
  const inherited: InheritedLayer[] = [];
  if (level !== 'org' && orgProfile) inherited.push({ source: 'org', params: orgProfile.params });
  if (level === 'group' && clientProfile) {
    inherited.push({ source: 'client', params: clientProfile.params });
  }
  const loading = orgProfiles.isFetching || clientProfiles.isFetching || groupProfiles.isFetching;

  return (
    <Card title={t('monitoring.settings.defaults')}>
      {types.isError ? (
        <ErrorState title={t('monitoring.settings.typesFailed')} />
      ) : (
        <div className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-3">
            <Select
              label={t('monitoring.settings.level')}
              value={level}
              onChange={(event) => setLevel(event.target.value as SettingsLevel)}
              options={LEVELS.map((l) => ({
                value: l,
                label: t(`monitoring.settings.levels.${l}`),
              }))}
            />
            {level === 'client' ? (
              <Select
                label={t('monitoring.settings.client')}
                value={clientId}
                onChange={(event) => setClientId(event.target.value)}
                options={[
                  { value: '', label: t('monitoring.settings.choose') },
                  ...(clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name })),
                ]}
              />
            ) : null}
            {level === 'group' ? (
              <Select
                label={t('monitoring.settings.group')}
                value={groupId}
                onChange={(event) => setGroupId(event.target.value)}
                options={[
                  { value: '', label: t('monitoring.settings.choose') },
                  ...(groups.data ?? []).map((g) => ({ value: g.id, label: g.name })),
                ]}
              />
            ) : null}
            <Select
              label={t('monitoring.settings.type')}
              value={type?.key ?? ''}
              onChange={(event) => setTypeKey(event.target.value)}
              options={available.map((x) => ({ value: x.key, label: x.key }))}
            />
          </div>
          {level === 'org' ? <AutoPingSwitch /> : null}
          {!ready ? (
            <p className="text-sm text-fg-muted">
              {types.isPending ? t('app.loading') : t('monitoring.settings.chooseLevel')}
            </p>
          ) : loading && !own && !orgProfiles.data ? (
            <p className="text-sm text-fg-muted">{t('app.loading')}</p>
          ) : (
            <ProfileEditor
              // A new level, target or type starts a fresh form.
              key={`${level}|${levelId ?? ''}|${type.key}|${own?.id ?? 'new'}`}
              probeType={type}
              level={level}
              levelId={levelId}
              profile={own}
              inherited={inherited}
              readOnly={readOnly}
              onChanged={() => {
                void orgProfiles.refetch();
                void clientProfiles.refetch();
                void groupProfiles.refetch();
              }}
            />
          )}
        </div>
      )}
    </Card>
  );
}

/** AutoPingSwitch is monitoring.auto_ping of organization.settings (WP-949). */
/**
 * holdsOrgWide tells whether the session holds a permission for the whole
 * organization: its permission scope (pscp) or else the session scope has
 * no client, site or team restriction (null). PATCH /org needs user:manage
 * org-wide (identity.HoldsOrgWide); the server still decides.
 */
export function holdsOrgWide(claims: SessionClaims | undefined, permission: string): boolean {
  if (!claims) return false;
  const scope = claims.pscp?.[permission] ?? claims;
  return [scope.cls, scope.sts, scope.tms].every((x) => x === null || x === undefined);
}

function AutoPingSwitch() {
  const { t } = useTranslation();
  const hasManage = useHasPermission('user:manage') === true;
  const session = useQuery({
    queryKey: ['me'],
    queryFn: () => sessionApi.me(),
    enabled: hasManage,
    retry: false,
    staleTime: 60_000,
  });
  const canManage = hasManage && holdsOrgWide(session.data, 'user:manage');
  const org = useOrganizationSettings();
  const save = useSetAutoPing();
  const [failure, setFailure] = useState<string>();
  const settings = (org.data?.settings ?? {}) as Record<string, unknown>;
  const monitoring = settings.monitoring as Record<string, unknown> | undefined;
  const checked = monitoring?.auto_ping === true;
  return (
    <div className="space-y-1 rounded-lg border border-line p-3">
      <label className="flex items-center gap-2 text-sm text-fg">
        <input
          type="checkbox"
          className="h-4 w-4 rounded border-line-strong"
          checked={checked}
          disabled={!canManage || !org.data || save.isPending}
          aria-describedby="monitoring-auto-ping-hint"
          onChange={(event) => {
            setFailure(undefined);
            save.mutate(
              { settings, value: event.target.checked },
              {
                onError: (err) =>
                  setFailure(
                    err instanceof ApiError && err.status === 403
                      ? t('monitoring.settings.autoPing.needsUserManage')
                      : t('monitoring.settings.autoPing.failed'),
                  ),
              },
            );
          }}
        />
        {t('monitoring.settings.autoPing.label')}
      </label>
      <p id="monitoring-auto-ping-hint" className="text-xs text-fg-muted">
        {canManage
          ? t('monitoring.settings.autoPing.hint')
          : t('monitoring.settings.autoPing.needsUserManage')}
      </p>
      {failure ? (
        <p role="alert" className="text-sm text-danger">
          {failure}
        </p>
      ) : null}
    </div>
  );
}

function GroupsCard({ readOnly }: { readOnly: boolean }) {
  const { t } = useTranslation();
  const groups = useMonitoringGroups();
  const clients = useClients();
  const { create, remove } = useGroupMutations();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<MonitoringGroup | null>(null);
  const [deleting, setDeleting] = useState<MonitoringGroup | null>(null);
  const [opened, setOpened] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const clientName = (id?: string) =>
    id
      ? ((clients.data?.data ?? []).find((c) => c.id === id)?.name ?? id)
      : t('monitoring.settings.groups.orgWide');
  const list = groups.data ?? [];
  const open = list.find((g) => g.id === opened);

  return (
    <Card
      title={t('monitoring.settings.groupsTitle')}
      actions={
        readOnly ? null : (
          <Button size="sm" variant="secondary" icon={<Plus />} onClick={() => setCreating(true)}>
            {t('monitoring.settings.groups.create')}
          </Button>
        )
      }
    >
      {notice ? (
        <p role="status" className="mb-3 text-sm text-fg-muted">
          {notice}
        </p>
      ) : null}
      {groups.isError ? (
        <ErrorState title={t('monitoring.settings.groups.loadFailed')} />
      ) : list.length === 0 ? (
        <p className="text-sm text-fg-muted">
          {groups.isPending ? t('app.loading') : t('monitoring.settings.groups.none')}
        </p>
      ) : (
        <ScrollRegion label={t('monitoring.settings.groupsTitle')}>
          <table className={tableClasses}>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('monitoring.settings.groups.name')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('monitoring.settings.groups.client')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('monitoring.settings.groups.priority')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('monitoring.settings.groups.members')}
                </th>
                <th scope="col" className={thClasses}>
                  <span className="sr-only">{t('common.actions')}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.map((g) => (
                <tr key={g.id} className={trClasses} data-testid={`monitoring-group-${g.id}`}>
                  <td className={tdPrimaryClasses}>{g.name}</td>
                  <td className={tdClasses}>{clientName(g.client_id)}</td>
                  <td className={tdClasses}>{g.priority}</td>
                  <td className={tdClasses}>{g.member_count ?? 0}</td>
                  <td className={`${tdClasses} whitespace-nowrap text-right`}>
                    <div className="flex justify-end gap-1">
                      <Button
                        size="sm"
                        variant="secondary"
                        icon={<Users />}
                        aria-expanded={opened === g.id}
                        onClick={() => setOpened(opened === g.id ? undefined : g.id)}
                      >
                        {t('monitoring.settings.groups.open', { name: g.name })}
                      </Button>
                      {readOnly ? null : (
                        <>
                          <Button size="sm" variant="ghost" onClick={() => setEditing(g)}>
                            {t('monitoring.settings.groups.edit')}
                          </Button>
                          <IconButton
                            size="sm"
                            icon={<Trash2 />}
                            className="text-danger hover:text-danger"
                            label={t('monitoring.settings.groups.deleteNamed', { name: g.name })}
                            onClick={() => setDeleting(g)}
                          />
                        </>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      )}
      {open ? <GroupDetail key={open.id} group={open} readOnly={readOnly} /> : null}

      <Modal
        open={creating || editing !== null}
        onOpenChange={(o) => {
          if (!o) {
            setCreating(false);
            setEditing(null);
          }
        }}
        title={
          editing
            ? t('monitoring.settings.groups.editTitle', { name: editing.name })
            : t('monitoring.settings.groups.create')
        }
      >
        {creating || editing ? (
          <GroupForm
            group={editing ?? undefined}
            clients={(clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name }))}
            busy={create.isPending}
            onDone={(created) => {
              setCreating(false);
              setEditing(null);
              if (created) setOpened(created.id);
            }}
          />
        ) : null}
      </Modal>

      <Modal
        open={deleting !== null}
        onOpenChange={(o) => {
          if (!o) setDeleting(null);
        }}
        title={t('monitoring.settings.groups.confirmDelete')}
        description={t('monitoring.settings.groups.confirmDeleteText', {
          name: deleting?.name ?? '',
        })}
      >
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => setDeleting(null)}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="danger"
            loading={remove.isPending}
            onClick={() => {
              if (!deleting) return;
              const target = deleting;
              remove.mutate(target.id, {
                onSuccess: (result) => {
                  setNotice(
                    t('monitoring.settings.groups.deleted', {
                      name: target.name,
                      count: result.deleted_profiles,
                    }),
                  );
                  if (opened === target.id) setOpened(undefined);
                },
                onError: () => setNotice(t('monitoring.settings.groups.deleteFailed')),
                onSettled: () => setDeleting(null),
              });
            }}
          >
            {t('monitoring.settings.groups.deleteConfirm')}
          </Button>
        </div>
      </Modal>
    </Card>
  );
}

function GroupForm({
  group,
  clients,
  busy,
  onDone,
}: {
  group?: MonitoringGroup;
  clients: { value: string; label: string }[];
  busy: boolean;
  onDone: (created?: MonitoringGroup) => void;
}) {
  const { t } = useTranslation();
  const { create, update } = useGroupMutations();
  const [name, setName] = useState(group?.name ?? '');
  const [clientId, setClientId] = useState(group?.client_id ?? '');
  const [priority, setPriority] = useState(String(group?.priority ?? 0));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string>();

  const refused = (err: unknown) => {
    if (err instanceof ApiError && err.status === 409) {
      setErrors({ name: t('monitoring.settings.groups.nameTaken') });
      return;
    }
    if (err instanceof ApiError && err.status === 422 && err.field) {
      setErrors({ [err.field]: err.detail || t('monitoring.settings.groups.invalid') });
      return;
    }
    setFailure(t('monitoring.settings.groups.saveFailed'));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setErrors({});
    setFailure(undefined);
    if (!name.trim()) {
      setErrors({ name: t('monitoring.settings.groups.nameRequired') });
      return;
    }
    const prio = Number(priority);
    if (!Number.isInteger(prio)) {
      setErrors({ priority: t('monitoring.settings.groups.priorityInvalid') });
      return;
    }
    if (group) {
      update.mutate(
        { id: group.id, data: { name: name.trim(), priority: prio } },
        { onSuccess: () => onDone(), onError: refused },
      );
      return;
    }
    create.mutate(
      { name: name.trim(), priority: prio, ...(clientId ? { client_id: clientId } : {}) },
      { onSuccess: (created) => onDone(created), onError: refused },
    );
  };

  return (
    <form onSubmit={submit} noValidate className="space-y-4">
      <Input
        label={t('monitoring.settings.groups.name')}
        value={name}
        error={errors.name}
        onChange={(event) => setName(event.target.value)}
      />
      {group ? null : (
        <Select
          label={t('monitoring.settings.groups.client')}
          value={clientId}
          error={errors.client_id}
          onChange={(event) => setClientId(event.target.value)}
          options={[{ value: '', label: t('monitoring.settings.groups.orgWide') }, ...clients]}
        />
      )}
      <Input
        label={t('monitoring.settings.groups.priority')}
        inputMode="numeric"
        value={priority}
        error={errors.priority}
        onChange={(event) => setPriority(event.target.value)}
      />
      <p className="text-xs text-fg-muted">{t('monitoring.settings.groups.priorityHint')}</p>
      {failure ? (
        <p role="alert" className="text-sm text-danger">
          {failure}
        </p>
      ) : null}
      <div className="flex justify-end">
        <Button type="submit" loading={busy || create.isPending || update.isPending}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/** CIName shows the name of a member CI as a link to its detail. */
function CIName({ id }: { id: string }) {
  const ci = useCI(id);
  return (
    <Link to={`/cmdb/${id}`} className="text-accent hover:underline">
      {ci.data?.name ?? id.slice(0, 8)}
    </Link>
  );
}

function GroupDetail({ group, readOnly }: { group: MonitoringGroup; readOnly: boolean }) {
  const { t } = useTranslation();
  const members = useGroupMembers(group.id);
  const { add, remove } = useGroupMemberMutations(group.id);
  const [picked, setPicked] = useState<PickedObject[]>([]);
  const [pickerError, setPickerError] = useState<string>();
  const rows = members.data ?? [];

  const addMembers = () => {
    setPickerError(undefined);
    const ids = picked.map((p) => p.id);
    if (ids.length === 0) return;
    if (ids.length > MONITORING_GROUP_MAX_MEMBERS) {
      setPickerError(
        t('monitoring.settings.members.tooMany', { max: MONITORING_GROUP_MAX_MEMBERS }),
      );
      return;
    }
    add.mutate(ids, {
      onSuccess: () => setPicked([]),
      onError: (err) =>
        setPickerError(
          err instanceof ApiError && err.status === 422
            ? t('monitoring.settings.members.otherClient', { detail: err.detail ?? '' }).trim()
            : err instanceof ApiError && err.status === 404
              ? t('monitoring.settings.members.notFound')
              : t('monitoring.settings.members.addFailed'),
        ),
    });
  };

  return (
    <section
      aria-label={t('monitoring.settings.members.title', { name: group.name })}
      className="mt-4 space-y-4 rounded-lg border border-line p-3"
    >
      <h3 className="text-sm font-semibold text-fg">
        {t('monitoring.settings.members.title', { name: group.name })} <Badge>{rows.length}</Badge>
      </h3>
      {members.isError ? (
        <p role="alert" className="text-sm text-danger">
          {t('monitoring.settings.members.loadFailed')}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-fg-muted">
          {members.isPending ? t('app.loading') : t('monitoring.settings.members.none')}
        </p>
      ) : (
        <ul className="divide-y divide-line text-sm" data-testid="monitoring-group-members">
          {rows.map((m) => (
            <li key={m.ci_id} className="flex items-center justify-between gap-2 py-1">
              <CIName id={m.ci_id} />
              {readOnly ? null : (
                <IconButton
                  size="sm"
                  icon={<Trash2 />}
                  className="text-danger hover:text-danger"
                  label={t('monitoring.settings.members.remove')}
                  disabled={remove.isPending}
                  onClick={() => remove.mutate(m.ci_id)}
                />
              )}
            </li>
          ))}
        </ul>
      )}
      {readOnly ? null : (
        <div className="space-y-2">
          <ObjectPicker
            label={t('monitoring.settings.members.add')}
            kinds={['ci']}
            multiple
            value={picked}
            onChange={(value) => {
              setPicked(value);
              setPickerError(undefined);
            }}
          />
          {pickerError ? (
            <p role="alert" className="text-sm text-danger" data-testid="member-picker-error">
              {pickerError}
            </p>
          ) : null}
          <Button
            size="sm"
            variant="secondary"
            disabled={picked.length === 0}
            loading={add.isPending}
            onClick={addMembers}
          >
            {t('monitoring.settings.members.addAction', { count: picked.length })}
          </Button>
        </div>
      )}
      <GroupCredentialsPanel group={group} />
    </section>
  );
}
