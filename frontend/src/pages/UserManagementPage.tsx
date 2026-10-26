import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ApiError } from '../api/client';
import type { Invitation, InvitationRequest } from '../api/client';
import {
  useHasPermission,
  useInvitations,
  useIssueInvitation,
  useRenewInvitation,
  useRevokeInvitation,
  useRoleList,
  useSites,
  useTeamList,
  useUserList,
} from '../api/hooks';
import { useClients } from '../api/cmdbHooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';

type Tab = 'users' | 'teams' | 'roles' | 'invitations';

export function UserManagementPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<Tab>('users');
  const [search, setSearch] = useState('');
  // Invitations need user:manage (TLC-03); without it, or while the rights
  // load, the page offers neither the tab nor an action (fail closed).
  const canManage = useHasPermission('user:manage') === true;
  const tabClass = (current: Tab) =>
    `px-4 py-2 text-sm font-medium border-b-2 ${tab === current ? 'border-blue-500 text-blue-600 dark:text-blue-400' : 'border-transparent text-gray-500 hover:text-gray-700 dark:text-gray-400'}`;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
          {t('userMgmt.title', 'Benutzer & Teams')}
        </h1>
        {canManage && tab !== 'invitations' ? (
          <Button onClick={() => setTab('invitations')}>{t('invitations.invite')}</Button>
        ) : null}
      </div>

      <div className="flex gap-2 border-b border-gray-200 dark:border-gray-700">
        <button className={tabClass('users')} onClick={() => setTab('users')}>
          {t('userMgmt.users', 'Benutzer')}
        </button>
        <button className={tabClass('teams')} onClick={() => setTab('teams')}>
          {t('userMgmt.teams', 'Teams')}
        </button>
        <button className={tabClass('roles')} onClick={() => setTab('roles')}>
          {t('userMgmt.roles', 'Rollen')}
        </button>
        {canManage ? (
          <button className={tabClass('invitations')} onClick={() => setTab('invitations')}>
            {t('invitations.title')}
          </button>
        ) : null}
      </div>

      <Input
        label={t('common.search', 'Suche')}
        value={search}
        onChange={(e) => setSearch(e.target.value)}
      />

      {tab === 'users' && <UsersTab search={search} />}
      {tab === 'teams' && <TeamsTab search={search} />}
      {tab === 'roles' && <RolesTab />}
      {tab === 'invitations' && canManage && <InvitationsTab search={search} />}
    </div>
  );
}

function UsersTab({ search }: { search: string }) {
  const { t } = useTranslation();
  const { data, isLoading } = useUserList({ search });

  if (isLoading)
    return <p className="text-gray-500 dark:text-gray-400">{t('app.loading', 'Laden...')}</p>;

  return (
    <Card title={`${t('userMgmt.users', 'Benutzer')} (${data?.total ?? 0})`}>
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
          <thead>
            <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
              <th className="pb-2">{t('userMgmt.displayName', 'Name')}</th>
              <th className="pb-2">{t('userMgmt.email', 'E-Mail')}</th>
              <th className="pb-2">{t('ci.status', 'Status')}</th>
              <th className="pb-2">{t('userMgmt.lastLogin', 'Letzter Login')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {data?.data.map((user) => (
              <tr key={user.id} className="text-sm text-gray-700 dark:text-gray-300">
                <td className="py-2 font-medium">{user.display_name}</td>
                <td className="py-2">{user.email}</td>
                <td className="py-2">
                  <Badge variant={user.status === 'active' ? 'success' : 'danger'}>
                    {user.status}
                  </Badge>
                </td>
                <td className="py-2">
                  {user.last_login_at
                    ? new Date(user.last_login_at).toLocaleDateString('de-DE')
                    : '—'}
                </td>
              </tr>
            ))}
            {(!data?.data || data.data.length === 0) && (
              <tr>
                <td colSpan={4} className="py-4 text-center text-gray-600 dark:text-gray-400">
                  {t('common.noData', 'Keine Daten')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function TeamsTab({ search }: { search: string }) {
  const { t } = useTranslation();
  const { data, isLoading } = useTeamList({ search });

  if (isLoading)
    return <p className="text-gray-500 dark:text-gray-400">{t('app.loading', 'Laden...')}</p>;

  return (
    <Card title={`${t('userMgmt.teams', 'Teams')} (${data?.total ?? 0})`}>
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
          <thead>
            <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
              <th className="pb-2">{t('ci.name', 'Name')}</th>
              <th className="pb-2">{t('common.description', 'Beschreibung')}</th>
              <th className="pb-2">{t('userMgmt.memberCount', 'Mitglieder')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {data?.data.map((team) => (
              <tr key={team.id} className="text-sm text-gray-700 dark:text-gray-300">
                <td className="py-2 font-medium">{team.name}</td>
                <td className="py-2">{team.description || '—'}</td>
                <td className="py-2">
                  <Badge variant="info">{team.member_count}</Badge>
                </td>
              </tr>
            ))}
            {(!data?.data || data.data.length === 0) && (
              <tr>
                <td colSpan={3} className="py-4 text-center text-gray-600 dark:text-gray-400">
                  {t('common.noData', 'Keine Daten')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function RolesTab() {
  const { t } = useTranslation();
  const { data, isLoading } = useRoleList();

  if (isLoading)
    return <p className="text-gray-500 dark:text-gray-400">{t('app.loading', 'Laden...')}</p>;

  return (
    <Card title={`${t('userMgmt.roles', 'Rollen')} (${data?.total ?? 0})`}>
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
          <thead>
            <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
              <th className="pb-2">{t('ci.name', 'Name')}</th>
              <th className="pb-2">{t('common.description', 'Beschreibung')}</th>
              <th className="pb-2">{t('userMgmt.permissions', 'Berechtigungen')}</th>
              <th className="pb-2">{t('userMgmt.type', 'Typ')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {data?.data.map((role) => (
              <tr key={role.id} className="text-sm text-gray-700 dark:text-gray-300">
                <td className="py-2 font-medium">{role.name}</td>
                <td className="py-2">{role.description || '—'}</td>
                <td className="py-2">{role.permissions.length}</td>
                <td className="py-2">
                  <Badge variant={role.is_system ? 'neutral' : 'info'}>
                    {role.is_system ? 'System' : 'Custom'}
                  </Badge>
                </td>
              </tr>
            ))}
            {(!data?.data || data.data.length === 0) && (
              <tr>
                <td colSpan={4} className="py-4 text-center text-gray-600 dark:text-gray-400">
                  {t('common.noData', 'Keine Daten')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

const SCOPE_TYPES = ['org', 'client', 'site'] as const;
type ScopeType = (typeof SCOPE_TYPES)[number];

type InviteField = 'email' | 'role_id' | 'scope_type' | 'scope_id';
const inviteFields: readonly string[] = ['email', 'role_id', 'scope_type', 'scope_id'];

/** A refusal of the server as the page shows it: a summary and per-field messages. */
interface RefusalView {
  message: string;
  fields: Partial<Record<InviteField, string>>;
}

/** fieldMessage words a field problem of an issue in UI language. */
function fieldMessage(field: InviteField, status: number, t: TFunction): string {
  switch (field) {
    case 'email':
      return status === 409
        ? t('invitations.errors.emailPending')
        : t('invitations.errors.emailInvalid');
    case 'role_id':
      return t('invitations.errors.roleInvalid');
    case 'scope_type':
      return t('invitations.errors.scopeTypeMismatch');
    case 'scope_id':
      return t('invitations.errors.scopeInvalid');
  }
}

/**
 * describeRefusal turns a failed invitation request into localized messages
 * (F-142): a field problem (400, 409, 422) goes to its input, the grant
 * ceiling (403) to the role, an accepted invitation (409 on renew or revoke)
 * and a missing configuration (503) to the summary.
 */
function describeRefusal(error: unknown, t: TFunction): RefusalView {
  if (!(error instanceof ApiError)) {
    const detail = error instanceof Error ? error.message : String(error);
    return { message: t('invitations.errors.failed', { detail }), fields: {} };
  }
  if (error.field && inviteFields.includes(error.field)) {
    const field = error.field as InviteField;
    return {
      message: t('invitations.errors.notSaved'),
      fields: { [field]: fieldMessage(field, error.status, t) },
    };
  }
  switch (error.status) {
    case 403:
      return {
        message: t('invitations.errors.forbidden'),
        fields: { role_id: t('invitations.errors.ceiling') },
      };
    case 404:
      return { message: t('invitations.errors.notFound'), fields: {} };
    case 409:
      return { message: t('invitations.errors.accepted'), fields: {} };
    case 503:
      return { message: t('invitations.errors.unavailable'), fields: {} };
    case 400:
    case 422:
      return { message: t('invitations.errors.rejected', { detail: error.message }), fields: {} };
    default:
      return { message: t('invitations.errors.failed', { detail: error.message }), fields: {} };
  }
}

const emptyInvite = { email: '', role_id: '', scope_type: 'org' as ScopeType, scope_id: '' };

/** invitationLink is the accept page of this installation with the token of an issue or renewal. */
function invitationLink(token: string): string {
  return `${window.location.origin}/invitations/accept?token=${encodeURIComponent(token)}`;
}

/**
 * InvitationsTab issues, renews and revokes invitations (TLC-03, F-142). It
 * is rendered for user:manage holders only; the server still decides every
 * request (grant ceiling, RBA-03).
 */
function InvitationsTab({ search }: { search: string }) {
  const { t, i18n } = useTranslation();
  const invitations = useInvitations();
  const roles = useRoleList({ limit: 200 });
  const clients = useClients();
  const sites = useSites();
  const issueInvitation = useIssueInvitation();
  const renewInvitation = useRenewInvitation();
  const revokeInvitation = useRevokeInvitation();
  const [form, setForm] = useState(emptyInvite);
  const [issueError, setIssueError] = useState<RefusalView | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [sent, setSent] = useState<Invitation | null>(null);

  // Only the standard roles can be invited (the server reads table role).
  const invitableRoles = (roles.data?.data ?? []).filter((role) => role.is_builtin);
  const roleName = (id: string) => roles.data?.data.find((role) => role.id === id)?.name ?? id;
  const clientOptions = (clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name }));
  const siteOptions = (sites.data?.data ?? []).map((s) => ({ value: s.id, label: s.name }));
  const scopeLabel = (inv: Invitation) => {
    const type = t(`invitations.scopeTypes.${inv.scope_type}`);
    if (!inv.scope_id) return type;
    const options = inv.scope_type === 'client' ? clientOptions : siteOptions;
    const target = options.find((o) => o.value === inv.scope_id)?.label ?? inv.scope_id;
    return `${type}: ${target}`;
  };
  const formatDate = (value: string) => new Date(value).toLocaleString(i18n.language);

  const submit = () => {
    if (!form.email || !form.role_id) return;
    if (form.scope_type !== 'org' && !form.scope_id) {
      setIssueError({
        message: t('invitations.errors.notSaved'),
        fields: { scope_id: t('invitations.errors.scopeRequired') },
      });
      return;
    }
    setIssueError(null);
    setSent(null);
    const request: InvitationRequest = {
      email: form.email.trim(),
      role_id: form.role_id,
      scope_type: form.scope_type,
      ...(form.scope_type !== 'org' ? { scope_id: form.scope_id } : {}),
    };
    // On a refusal the inputs stay as they are, so the user can correct them.
    issueInvitation.mutate(request, {
      onSuccess: (invitation) => {
        setSent(invitation);
        setForm(emptyInvite);
      },
      onError: (error) => setIssueError(describeRefusal(error, t)),
    });
  };

  const renew = (inv: Invitation) => {
    setActionError(null);
    setSent(null);
    renewInvitation.mutate(inv.id, {
      onSuccess: (invitation) => setSent(invitation),
      onError: (error) => setActionError(`${inv.email}: ${describeRefusal(error, t).message}`),
    });
  };
  const revoke = (inv: Invitation) => {
    setActionError(null);
    setSent(null);
    revokeInvitation.mutate(inv.id, {
      onError: (error) => setActionError(`${inv.email}: ${describeRefusal(error, t).message}`),
    });
  };

  const statusVariant = (status: Invitation['status']) =>
    status === 'pending' ? 'info' : status === 'accepted' ? 'success' : 'warning';
  const needle = search.trim().toLowerCase();
  const rows = (invitations.data ?? []).filter(
    (inv) => !needle || inv.email.toLowerCase().includes(needle),
  );

  return (
    <div className="space-y-6">
      <Card title={t('invitations.invite')}>
        <div className="space-y-4">
          {issueError ? (
            <p role="alert" className="text-sm text-red-600 dark:text-red-400">
              {issueError.message}
            </p>
          ) : null}
          {sent ? (
            <div role="status" className="space-y-2 text-sm text-green-700 dark:text-green-400">
              <p>
                {t('invitations.sent', { email: sent.email, expires: formatDate(sent.expires_at) })}
              </p>
              {sent.token ? (
                <Input
                  label={t('invitations.link')}
                  readOnly
                  value={invitationLink(sent.token)}
                  onFocus={(event) => event.target.select()}
                />
              ) : null}
            </div>
          ) : null}
          <div className="grid gap-4 md:grid-cols-2">
            <Input
              label={t('invitations.email')}
              type="email"
              value={form.email}
              maxLength={254}
              error={issueError?.fields.email}
              onChange={(event) => setForm({ ...form, email: event.target.value })}
            />
            <Select
              label={t('invitations.role')}
              value={form.role_id}
              error={issueError?.fields.role_id}
              options={[
                { value: '', label: t('invitations.chooseRole') },
                ...invitableRoles.map((role) => ({ value: role.id, label: role.name })),
              ]}
              onChange={(event) => setForm({ ...form, role_id: event.target.value })}
            />
            <Select
              label={t('invitations.scopeType')}
              value={form.scope_type}
              error={issueError?.fields.scope_type}
              options={SCOPE_TYPES.map((type) => ({
                value: type,
                label: t(`invitations.scopeTypes.${type}`),
              }))}
              onChange={(event) =>
                setForm({ ...form, scope_type: event.target.value as ScopeType, scope_id: '' })
              }
            />
            {form.scope_type !== 'org' ? (
              <Select
                label={t('invitations.scopeTarget')}
                value={form.scope_id}
                error={issueError?.fields.scope_id}
                options={[
                  { value: '', label: t('invitations.chooseTarget') },
                  ...(form.scope_type === 'client' ? clientOptions : siteOptions),
                ]}
                onChange={(event) => setForm({ ...form, scope_id: event.target.value })}
              />
            ) : null}
          </div>
          <Button
            onClick={submit}
            disabled={issueInvitation.isPending || !form.email || !form.role_id}
          >
            {t('invitations.send')}
          </Button>
        </div>
      </Card>

      {invitations.isError ? (
        <ErrorState
          title={t('invitations.errorTitle')}
          description={describeRefusal(invitations.error, t).message}
          retryLabel={t('common.retry')}
          onRetry={() => invitations.refetch()}
        />
      ) : (
        <Card title={`${t('invitations.title')} (${rows.length})`}>
          {actionError ? (
            <p role="alert" className="mb-3 text-sm text-red-600 dark:text-red-400">
              {actionError}
            </p>
          ) : null}
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th className="pb-2">{t('invitations.email')}</th>
                  <th className="pb-2">{t('invitations.role')}</th>
                  <th className="pb-2">{t('invitations.scope')}</th>
                  <th className="pb-2">{t('invitations.expires')}</th>
                  <th className="pb-2">{t('invitations.status')}</th>
                  <th className="pb-2">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {rows.map((inv) => (
                  <tr key={inv.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2 font-medium">{inv.email}</td>
                    <td className="py-2">{roleName(inv.role_id)}</td>
                    <td className="py-2">{scopeLabel(inv)}</td>
                    <td className="py-2">{formatDate(inv.expires_at)}</td>
                    <td className="py-2">
                      <Badge variant={statusVariant(inv.status)}>
                        {t(`invitations.statuses.${inv.status}`)}
                      </Badge>
                    </td>
                    <td className="py-2">
                      {inv.status !== 'accepted' ? (
                        <div className="flex gap-2">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => renew(inv)}
                            disabled={renewInvitation.isPending}
                          >
                            {t('invitations.renew')}
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => revoke(inv)}
                            disabled={revokeInvitation.isPending}
                          >
                            {t('invitations.revoke')}
                          </Button>
                        </div>
                      ) : null}
                    </td>
                  </tr>
                ))}
                {!invitations.isLoading && rows.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="py-4 text-center text-gray-600 dark:text-gray-400">
                      {t('common.noData', 'Keine Daten')}
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  );
}
