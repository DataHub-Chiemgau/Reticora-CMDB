import { MailPlus, Search, Send, UserPlus, Users } from 'lucide-react';
import { useRef, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ApiError } from '../api/client';
import type { AppUser, Invitation, InvitationRequest } from '../api/client';
import {
  useDeleteUser,
  useEffectivePermissions,
  useHasPermission,
  useInvitations,
  useIssueInvitation,
  useRenewInvitation,
  useRevokeInvitation,
  useRoleList,
  useSetUserStatus,
  useSites,
  useTeamList,
  useUserList,
} from '../api/hooks';
import { useClients } from '../api/cmdbHooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Toolbar } from '../components/ui/Toolbar';
import {
  tableClasses,
  tdClasses,
  tdNumericClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { AdminSection } from '../components/admin/AdminSection';
import { ScrollRegion } from '../components/ScrollRegion';

type Tab = 'users' | 'teams' | 'roles' | 'invitations';

const dangerTextClasses = 'text-danger hover:text-danger';

/**
 * tabId and panelId tie each tab to its panel (WAI-ARIA tabs pattern).
 */
const tabId = (tab: Tab) => `user-mgmt-tab-${tab}`;
const panelId = (tab: Tab) => `user-mgmt-panel-${tab}`;

/**
 * Tabs is the one tab pattern of the page after the WAI-ARIA tabs pattern:
 * role tablist/tab, aria-selected, one tab stop (roving tabindex), arrow keys
 * and Home/End move and select.
 */
function Tabs({
  tabs,
  current,
  onChange,
  label,
}: {
  tabs: { id: Tab; label: string }[];
  current: Tab;
  onChange: (tab: Tab) => void;
  label: string;
}) {
  const refs = useRef(new Map<Tab, HTMLButtonElement>());
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    let next: number;
    switch (event.key) {
      case 'ArrowRight':
        next = (index + 1) % tabs.length;
        break;
      case 'ArrowLeft':
        next = (index - 1 + tabs.length) % tabs.length;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = tabs.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    const target = tabs[next]!;
    onChange(target.id);
    refs.current.get(target.id)?.focus();
  };
  return (
    <div
      role="tablist"
      aria-label={label}
      className="flex gap-1 overflow-x-auto border-b border-line"
    >
      {tabs.map((tab, index) => {
        const selected = tab.id === current;
        return (
          <button
            key={tab.id}
            ref={(element) => {
              if (element) refs.current.set(tab.id, element);
              else refs.current.delete(tab.id);
            }}
            id={tabId(tab.id)}
            type="button"
            role="tab"
            aria-selected={selected}
            aria-controls={panelId(tab.id)}
            tabIndex={selected ? 0 : -1}
            onClick={() => onChange(tab.id)}
            onKeyDown={(event) => onKeyDown(event, index)}
            className={cn(
              '-mb-px shrink-0 whitespace-nowrap border-b-2 px-3.5 py-2.5 text-sm font-medium transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
              selected ? 'border-accent text-fg' : 'border-transparent text-fg-muted hover:text-fg',
            )}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}

/** SearchField filters the list of its section: a search box in a toolbar. */
function SearchField({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const { t } = useTranslation();
  return (
    <Toolbar>
      <Input
        type="text"
        icon={<Search />}
        placeholder={t('common.search', 'Suche')}
        aria-label={t('common.search', 'Suche')}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className="w-72 max-w-full"
      />
    </Toolbar>
  );
}

export function UserManagementPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<Tab>('users');
  const [search, setSearch] = useState('');
  // Invitations need user:manage (TLC-03); without it, or while the rights
  // load, the page offers neither the tab nor an action (fail closed).
  const canManage = useHasPermission('user:manage') === true;
  // The own account is never offered for deactivation or deletion (TLC-04):
  // /me/permissions names the caller's user id.
  const ownUserId = useEffectivePermissions().data?.user_id;
  const tabs: { id: Tab; label: string }[] = [
    { id: 'users', label: t('userMgmt.users', 'Benutzer') },
    { id: 'teams', label: t('userMgmt.teams', 'Teams') },
    { id: 'roles', label: t('userMgmt.roles', 'Rollen') },
    ...(canManage ? [{ id: 'invitations' as const, label: t('invitations.title') }] : []),
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('userMgmt.title', 'Benutzer & Teams')}
        description={t('userMgmt.subtitle')}
        actions={
          canManage && tab !== 'invitations' ? (
            <Button icon={<UserPlus />} onClick={() => setTab('invitations')}>
              {t('invitations.invite')}
            </Button>
          ) : null
        }
      />

      <div className="space-y-4">
        <Tabs
          tabs={tabs}
          current={tab}
          onChange={setTab}
          label={t('userMgmt.title', 'Benutzer & Teams')}
        />

        <div role="tabpanel" id={panelId(tab)} aria-labelledby={tabId(tab)}>
          {tab === 'users' && (
            <UsersTab
              search={search}
              onSearch={setSearch}
              canManage={canManage}
              ownUserId={ownUserId}
            />
          )}
          {tab === 'teams' && <TeamsTab search={search} onSearch={setSearch} />}
          {tab === 'roles' && <RolesTab />}
          {tab === 'invitations' && canManage && (
            <InvitationsTab search={search} onSearch={setSearch} />
          )}
        </div>
      </div>
    </div>
  );
}

type UserAction = 'deactivate' | 'reactivate' | 'delete';

/**
 * describeUserRefusal words a refused deactivation, reactivation or deletion
 * in UI language (TLC-04, F-196): a 409 of the deletion names the handover
 * of open objects, or, for a user closed records refer to, deactivation or
 * anonymization instead; the server's detail is never shown.
 */
function describeUserRefusal(error: unknown, name: string, t: TFunction): string {
  if (!(error instanceof ApiError)) {
    return t('userMgmt.errors.failed', { name, status: '–' });
  }
  switch (error.status) {
    case 409:
      return /referenced/i.test(error.detail ?? '')
        ? t('userMgmt.errors.referenced', { name })
        : t('userMgmt.errors.ownsOpenObjects', { name });
    case 403:
      return t('userMgmt.errors.forbidden', { name });
    case 404:
      return t('userMgmt.errors.notFound', { name });
    case 503:
      return t('userMgmt.errors.unavailable', { name });
    default:
      return t('userMgmt.errors.failed', { name, status: error.status });
  }
}

/**
 * UsersTab lists the users; user:manage holders deactivate, reactivate and
 * delete them after a confirmation (TLC-04, F-196). The own account offers
 * no action, and without user:manage, or while the caller's id is unknown,
 * no row offers one (fail closed). The server still decides every request.
 */
function UsersTab({
  search,
  onSearch,
  canManage,
  ownUserId,
}: {
  search: string;
  onSearch: (value: string) => void;
  canManage: boolean;
  ownUserId?: string;
}) {
  const { t } = useTranslation();
  const { data, isLoading, error, refetch } = useUserList({ search });
  const setStatus = useSetUserStatus();
  const deleteUser = useDeleteUser();
  const [pending, setPending] = useState<{ user: AppUser; action: UserAction } | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const withActions = canManage && !!ownUserId;
  const busy = setStatus.isPending || deleteUser.isPending;

  const confirm = () => {
    if (!pending) return;
    const { user, action } = pending;
    const callbacks = {
      onSettled: () => setPending(null),
      onError: (error: unknown) =>
        setActionError(describeUserRefusal(error, user.display_name || user.email, t)),
    };
    setActionError(null);
    if (action === 'delete') {
      deleteUser.mutate(user.id, callbacks);
    } else {
      setStatus.mutate(
        { id: user.id, status: action === 'deactivate' ? 'inactive' : 'active' },
        callbacks,
      );
    }
  };
  const confirmText = (action: UserAction, user: AppUser) => {
    const values = { name: user.display_name || user.email, email: user.email };
    switch (action) {
      case 'deactivate':
        return {
          title: t('userMgmt.confirm.deactivateTitle'),
          text: t('userMgmt.confirm.deactivateText', values),
          button: t('userMgmt.deactivate'),
        };
      case 'reactivate':
        return {
          title: t('userMgmt.confirm.reactivateTitle'),
          text: t('userMgmt.confirm.reactivateText', values),
          button: t('userMgmt.reactivate'),
        };
      case 'delete':
        return {
          title: t('userMgmt.confirm.deleteTitle'),
          text: t('userMgmt.confirm.deleteText', values),
          button: t('userMgmt.confirm.deleteConfirm'),
        };
    }
  };
  const dialog = pending ? confirmText(pending.action, pending.user) : null;
  const total = data?.total ?? 0;

  return (
    <AdminSection
      title={`${t('userMgmt.users', 'Benutzer')} (${total})`}
      description={t('userMgmt.usersHint')}
    >
      <SearchField value={search} onChange={onSearch} />
      {actionError ? (
        <p role="alert" className="text-sm text-danger">
          {actionError}
        </p>
      ) : null}
      {isLoading ? <SkeletonList rows={4} label={t('app.loading', 'Laden...')} /> : null}
      {error ? (
        <ErrorState
          title={t('userMgmt.loadError')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}
      {!isLoading && !error && (!data?.data || data.data.length === 0) ? (
        <EmptyState
          icon={<Users />}
          title={t(search ? 'common.noResults' : 'common.noData', 'Keine Daten')}
        />
      ) : null}
      {data?.data.length ? (
        <Card className="overflow-hidden p-0">
          <ScrollRegion label={t('userMgmt.users', 'Benutzer')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr className="text-fg-muted">
                  <th scope="col" className={thClasses}>
                    {t('userMgmt.displayName', 'Name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('userMgmt.email', 'E-Mail')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status', 'Status')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('userMgmt.lastLogin', 'Letzter Login')}
                  </th>
                  {withActions ? (
                    <th scope="col" className={cn(thClasses, 'text-right')}>
                      {t('common.actions')}
                    </th>
                  ) : null}
                </tr>
              </thead>
              <tbody>
                {data.data.map((user) => (
                  <tr key={user.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{user.display_name}</td>
                    <td className={tdClasses}>{user.email}</td>
                    <td className={tdClasses}>
                      <Badge variant={user.status === 'active' ? 'success' : 'danger'}>
                        {t(`userMgmt.statuses.${user.status}`, user.status)}
                      </Badge>
                    </td>
                    <td className={cn(tdClasses, 'whitespace-nowrap tabular-nums')}>
                      {user.last_login_at
                        ? new Date(user.last_login_at).toLocaleDateString('de-DE')
                        : '—'}
                    </td>
                    {withActions ? (
                      <td className={cn(tdClasses, 'text-right')}>
                        {user.id === ownUserId ? (
                          <span className="text-fg-subtle">{t('userMgmt.ownAccount')}</span>
                        ) : (
                          <div className="flex justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="sm"
                              disabled={busy}
                              onClick={() =>
                                setPending({
                                  user,
                                  action: user.status === 'active' ? 'deactivate' : 'reactivate',
                                })
                              }
                            >
                              {user.status === 'active'
                                ? t('userMgmt.deactivate')
                                : t('userMgmt.reactivate')}
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              className={dangerTextClasses}
                              disabled={busy}
                              onClick={() => setPending({ user, action: 'delete' })}
                            >
                              {t('userMgmt.delete')}
                            </Button>
                          </div>
                        )}
                      </td>
                    ) : null}
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}
      {withActions ? (
        <Modal
          open={pending !== null}
          onOpenChange={(open) => {
            if (!open && !busy) setPending(null);
          }}
          title={dialog?.title ?? ''}
          description={dialog?.text}
          size="sm"
        >
          <div className="flex justify-end gap-2">
            <Button variant="secondary" disabled={busy} onClick={() => setPending(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              variant={pending?.action === 'reactivate' ? 'primary' : 'danger'}
              disabled={busy}
              onClick={confirm}
            >
              {dialog?.button ?? ''}
            </Button>
          </div>
        </Modal>
      ) : null}
    </AdminSection>
  );
}

function TeamsTab({ search, onSearch }: { search: string; onSearch: (value: string) => void }) {
  const { t } = useTranslation();
  const { data, isLoading, error, refetch } = useTeamList({ search });

  return (
    <AdminSection
      title={`${t('userMgmt.teams', 'Teams')} (${data?.total ?? 0})`}
      description={t('userMgmt.teamsHint')}
    >
      <SearchField value={search} onChange={onSearch} />
      {isLoading ? <SkeletonList rows={4} label={t('app.loading', 'Laden...')} /> : null}
      {error ? (
        <ErrorState
          title={t('userMgmt.loadError')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}
      {!isLoading && !error && (!data?.data || data.data.length === 0) ? (
        <EmptyState
          icon={<Users />}
          title={t(search ? 'common.noResults' : 'common.noData', 'Keine Daten')}
        />
      ) : null}
      {data?.data.length ? (
        <Card className="overflow-hidden p-0">
          <ScrollRegion label={t('userMgmt.teams', 'Teams')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr className="text-fg-muted">
                  <th scope="col" className={thClasses}>
                    {t('ci.name', 'Name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('common.description', 'Beschreibung')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('userMgmt.memberCount', 'Mitglieder')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {data.data.map((team) => (
                  <tr key={team.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{team.name}</td>
                    <td className={tdClasses}>{team.description || '—'}</td>
                    <td className={tdNumericClasses}>{team.member_count}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}
    </AdminSection>
  );
}

function RolesTab() {
  const { t } = useTranslation();
  const { data, isLoading, error, refetch } = useRoleList();

  return (
    <AdminSection
      title={`${t('userMgmt.roles', 'Rollen')} (${data?.total ?? 0})`}
      description={t('userMgmt.rolesHint')}
    >
      {isLoading ? <SkeletonList rows={4} label={t('app.loading', 'Laden...')} /> : null}
      {error ? (
        <ErrorState
          title={t('userMgmt.loadError')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}
      {!isLoading && !error && (!data?.data || data.data.length === 0) ? (
        <EmptyState icon={<Users />} title={t('common.noData', 'Keine Daten')} />
      ) : null}
      {data?.data.length ? (
        <Card className="overflow-hidden p-0">
          <ScrollRegion label={t('userMgmt.roles', 'Rollen')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr className="text-fg-muted">
                  <th scope="col" className={thClasses}>
                    {t('ci.name', 'Name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('common.description', 'Beschreibung')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('userMgmt.permissions', 'Berechtigungen')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('userMgmt.type', 'Typ')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {data.data.map((role) => (
                  <tr key={role.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{role.name}</td>
                    <td className={tdClasses}>{role.description || '—'}</td>
                    <td className={tdNumericClasses}>{role.permissions.length}</td>
                    <td className={tdClasses}>
                      <Badge variant={role.is_system ? 'neutral' : 'info'}>
                        {role.is_system ? 'System' : 'Custom'}
                      </Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}
    </AdminSection>
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
 * INVITATION_MAIL_FAILED_DETAIL is the problem detail of an issue or renewal
 * whose invitation mail the central SMTP server did not take
 * (user.ErrInvitationMail): nothing was stored or changed.
 */
const INVITATION_MAIL_FAILED_DETAIL = 'the invitation e-mail could not be sent';

/**
 * describeRefusal turns a failed invitation request into localized messages
 * (F-142): a field problem (400, 409, 422) goes to its input, the grant
 * ceiling (403) to the role, an accepted invitation (409 on renew or revoke),
 * an invitation mail the server could not send and a missing configuration
 * (both 503) to the summary.
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
      return error.detail === INVITATION_MAIL_FAILED_DETAIL
        ? { message: t('invitations.errors.mailFailed'), fields: {} }
        : { message: t('invitations.errors.unavailable'), fields: {} };
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
function InvitationsTab({
  search,
  onSearch,
}: {
  search: string;
  onSearch: (value: string) => void;
}) {
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
    <div className="space-y-8">
      <AdminSection title={t('invitations.invite')} description={t('invitations.inviteHint')}>
        <Card>
          <div className="space-y-4">
            {issueError ? (
              <p role="alert" className="text-sm text-danger">
                {issueError.message}
              </p>
            ) : null}
            {sent ? (
              <div
                role="status"
                className="space-y-3 rounded-lg border border-green-600/20 bg-green-50 p-3 text-sm text-green-800 dark:border-green-400/25 dark:bg-green-500/10 dark:text-green-300"
              >
                <p>
                  {t('invitations.sent', {
                    email: sent.email,
                    expires: formatDate(sent.expires_at),
                  })}
                </p>
                {/* Where the acceptance creates the IdP account, the server
                  returns no token: it has mailed the link straight to the
                  invited address (a refused mail is a 503, nothing stored). */}
                {sent.token ? (
                  <Input
                    label={t('invitations.link')}
                    readOnly
                    value={invitationLink(sent.token)}
                    className="font-mono text-xs"
                    onFocus={(event) => event.target.select()}
                  />
                ) : (
                  <p>{t('invitations.mailOnly')}</p>
                )}
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
            <div className="flex justify-end border-t border-line pt-4">
              <Button
                icon={<Send />}
                loading={issueInvitation.isPending}
                onClick={submit}
                disabled={!form.email || !form.role_id}
              >
                {t('invitations.send')}
              </Button>
            </div>
          </div>
        </Card>
      </AdminSection>

      {invitations.isError ? (
        <ErrorState
          title={t('invitations.errorTitle')}
          description={describeRefusal(invitations.error, t).message}
          retryLabel={t('common.retry')}
          onRetry={() => invitations.refetch()}
        />
      ) : (
        <AdminSection
          title={`${t('invitations.title')} (${rows.length})`}
          description={t('invitations.listHint')}
        >
          <SearchField value={search} onChange={onSearch} />
          {actionError ? (
            <p role="alert" className="text-sm text-danger">
              {actionError}
            </p>
          ) : null}
          {invitations.isLoading ? (
            <SkeletonList rows={3} label={t('app.loading', 'Laden...')} />
          ) : null}
          {!invitations.isLoading && rows.length === 0 ? (
            <EmptyState
              icon={<MailPlus />}
              title={t(needle ? 'common.noResults' : 'common.noData', 'Keine Daten')}
            />
          ) : null}
          {rows.length > 0 ? (
            <Card className="overflow-hidden p-0">
              <ScrollRegion label={t('invitations.title')}>
                <table className={tableClasses}>
                  <thead className={theadClasses}>
                    <tr className="text-fg-muted">
                      <th scope="col" className={thClasses}>
                        {t('invitations.email')}
                      </th>
                      <th scope="col" className={thClasses}>
                        {t('invitations.role')}
                      </th>
                      <th scope="col" className={thClasses}>
                        {t('invitations.scope')}
                      </th>
                      <th scope="col" className={thClasses}>
                        {t('invitations.expires')}
                      </th>
                      <th scope="col" className={thClasses}>
                        {t('invitations.status')}
                      </th>
                      <th scope="col" className={cn(thClasses, 'text-right')}>
                        {t('common.actions')}
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((inv) => (
                      <tr key={inv.id} className={trClasses}>
                        <td className={tdPrimaryClasses}>{inv.email}</td>
                        <td className={tdClasses}>{roleName(inv.role_id)}</td>
                        <td className={tdClasses}>{scopeLabel(inv)}</td>
                        <td className={cn(tdClasses, 'whitespace-nowrap tabular-nums')}>
                          {formatDate(inv.expires_at)}
                        </td>
                        <td className={tdClasses}>
                          <Badge variant={statusVariant(inv.status)}>
                            {t(`invitations.statuses.${inv.status}`)}
                          </Badge>
                        </td>
                        <td className={cn(tdClasses, 'text-right')}>
                          {inv.status !== 'accepted' ? (
                            <div className="flex justify-end gap-1">
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
                                className={dangerTextClasses}
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
                  </tbody>
                </table>
              </ScrollRegion>
            </Card>
          ) : null}
        </AdminSection>
      )}
    </div>
  );
}
