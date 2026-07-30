import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useUserList, useTeamList, useRoleList } from '../api/hooks';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';

type Tab = 'users' | 'teams' | 'roles';

export function UserManagementPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<Tab>('users');
  const [search, setSearch] = useState('');

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
          {t('userMgmt.title', 'Benutzer & Teams')}
        </h1>
      </div>

      <div className="flex gap-2 border-b border-gray-200 dark:border-gray-700">
        <button
          className={`px-4 py-2 text-sm font-medium border-b-2 ${tab === 'users' ? 'border-blue-500 text-blue-600 dark:text-blue-400' : 'border-transparent text-gray-500 hover:text-gray-700 dark:text-gray-400'}`}
          onClick={() => setTab('users')}
        >
          {t('userMgmt.users', 'Benutzer')}
        </button>
        <button
          className={`px-4 py-2 text-sm font-medium border-b-2 ${tab === 'teams' ? 'border-blue-500 text-blue-600 dark:text-blue-400' : 'border-transparent text-gray-500 hover:text-gray-700 dark:text-gray-400'}`}
          onClick={() => setTab('teams')}
        >
          {t('userMgmt.teams', 'Teams')}
        </button>
        <button
          className={`px-4 py-2 text-sm font-medium border-b-2 ${tab === 'roles' ? 'border-blue-500 text-blue-600 dark:text-blue-400' : 'border-transparent text-gray-500 hover:text-gray-700 dark:text-gray-400'}`}
          onClick={() => setTab('roles')}
        >
          {t('userMgmt.roles', 'Rollen')}
        </button>
      </div>

      <Input
        label={t('common.search', 'Suche')}
        value={search}
        onChange={(e) => setSearch(e.target.value)}
      />

      {tab === 'users' && <UsersTab search={search} />}
      {tab === 'teams' && <TeamsTab search={search} />}
      {tab === 'roles' && <RolesTab />}
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
                <td colSpan={4} className="py-4 text-center text-gray-400">
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
                <td colSpan={3} className="py-4 text-center text-gray-400">
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
                <td colSpan={4} className="py-4 text-center text-gray-400">
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
