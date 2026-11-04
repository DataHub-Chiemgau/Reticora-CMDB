import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, type Assignment } from '../api/client';
import { useAssignmentList, useCreateAssignment, useReturnAssignment } from '../api/hooks';
import { Card } from '../components/ui/Card';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { ScrollRegion } from '../components/ScrollRegion';

// Stored assignment statuses (English keys); the labels come from the catalog.
export const ASSIGNMENT_STATUSES = [
  'active',
  'returned',
  'transferred',
  'overdue',
  'cancelled',
] as const;

function statusVariant(status: string) {
  switch (status) {
    case 'active':
      return 'success' as const;
    case 'returned':
      return 'neutral' as const;
    case 'transferred':
      return 'info' as const;
    case 'overdue':
      return 'danger' as const;
    default:
      return 'neutral' as const;
  }
}

type HolderKind = 'user' | 'team';

interface AssignForm {
  asset_id: string;
  holder_kind: HolderKind;
  holder_id: string;
  due_date: string;
  notes: string;
}

const EMPTY_FORM: AssignForm = {
  asset_id: '',
  holder_kind: 'user',
  holder_id: '',
  due_date: '',
  notes: '',
};

/** holderOf is the current holder of a row: its user, else its team. */
function holderOf(item: Assignment): string | undefined {
  return item.assigned_to || item.assigned_team_id || undefined;
}

/** dueAt turns the chosen day into the end of that day in local time. */
function dueAt(day: string): string | undefined {
  if (!day) return undefined;
  const end = new Date(`${day}T23:59:59`);
  return Number.isNaN(end.getTime()) ? undefined : end.toISOString();
}

/** isConflict tells the 409 of a holder that changed meanwhile. */
function isConflict(error: unknown): error is ApiError {
  return error instanceof ApiError && error.status === 409;
}

export function AssignmentListPage() {
  const { t } = useTranslation();
  const statusOptions = [
    { value: '', label: t('common.all', 'Alle') },
    ...ASSIGNMENT_STATUSES.map((value) => ({
      value,
      label: t(`assignment.statuses.${value}`, value),
    })),
  ];
  const [status, setStatus] = useState('');
  const [search, setSearch] = useState('');
  const [showCreate, setShowCreate] = useState(false);

  const { data, isLoading, refetch } = useAssignmentList({ status, search });
  const createMutation = useCreateAssignment();
  const returnMutation = useReturnAssignment();

  const [form, setForm] = useState<AssignForm>(EMPTY_FORM);
  // conflict is the message of a 409 (the holder changed meanwhile); the
  // list is loaded again so the rows show the current holder.
  const [conflict, setConflict] = useState('');
  const onConflict = (error: unknown) => {
    if (!isConflict(error)) return;
    setConflict(t('assignment.conflict', 'Der Halter hat sich zwischenzeitlich geändert.'));
    void refetch();
  };

  const handleCreate = () => {
    if (!form.asset_id || !form.holder_id) return;
    setConflict('');
    const holder =
      form.holder_kind === 'team'
        ? { assigned_team_id: form.holder_id }
        : { assigned_user_id: form.holder_id };
    createMutation.mutate(
      {
        assetId: form.asset_id,
        data: { ...holder, due_at: dueAt(form.due_date), notes: form.notes || undefined },
      },
      {
        onSuccess: () => {
          setShowCreate(false);
          setForm(EMPTY_FORM);
        },
        onError: (error) => {
          if (isConflict(error)) setShowCreate(false);
          onConflict(error);
        },
      },
    );
  };

  const handleReturn = (item: Assignment) => {
    const holder = holderOf(item);
    if (!item.asset_id || !holder) return;
    setConflict('');
    returnMutation.mutate(
      { assetId: item.asset_id, data: { expected_holder: holder } },
      { onError: onConflict },
    );
  };

  const writeError = [createMutation.error, returnMutation.error].find(
    (error) => error && !isConflict(error),
  );

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
          {t('assignment.title', 'Zuweisungen')}
        </h1>
        <Button onClick={() => setShowCreate(true)}>{t('assignment.assign', 'Zuweisen')}</Button>
      </div>

      {conflict ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {conflict}
        </p>
      ) : null}
      {writeError ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {writeError instanceof ApiError
            ? (writeError.detail ?? writeError.message)
            : writeError.message}
        </p>
      ) : null}

      <div className="flex gap-4 flex-wrap">
        <Input
          label={t('common.search', 'Suche')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Select
          label={t('ci.status', 'Status')}
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={statusOptions}
        />
      </div>

      {isLoading ? (
        <p className="text-gray-500 dark:text-gray-400">{t('app.loading', 'Laden...')}</p>
      ) : (
        <Card title={`${t('assignment.title', 'Zuweisungen')} (${data?.total ?? 0})`}>
          <ScrollRegion label={t('assignment.title', 'Zuweisungen')}>
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th className="pb-2">{t('assignment.type', 'Typ')}</th>
                  <th className="pb-2">{t('assignment.assignedTo', 'Zugewiesen an')}</th>
                  <th className="pb-2">{t('ci.status', 'Status')}</th>
                  <th className="pb-2">{t('assignment.assignedAt', 'Datum')}</th>
                  <th className="pb-2">{t('assignment.dueAt', 'Fällig am')}</th>
                  <th className="pb-2">{t('common.actions', 'Aktionen')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {data?.data.map((item) => (
                  <tr key={item.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2">{item.assignment_type}</td>
                    <td className="py-2">
                      {item.assigned_team_id
                        ? `${t('assignment.team', 'Team')} ${item.assigned_team_id}`
                        : item.assigned_to}
                    </td>
                    <td className="py-2">
                      <Badge variant={statusVariant(item.status)}>
                        {t(`assignment.statuses.${item.status}`, item.status)}
                      </Badge>
                    </td>
                    <td className="py-2">
                      {new Date(item.assigned_at).toLocaleDateString('de-DE')}
                    </td>
                    <td className="py-2">
                      {item.due_at ? new Date(item.due_at).toLocaleDateString('de-DE') : ''}
                    </td>
                    <td className="py-2">
                      {(item.status === 'active' || item.status === 'overdue') &&
                      item.asset_id &&
                      holderOf(item) ? (
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={returnMutation.isPending}
                          onClick={() => handleReturn(item)}
                        >
                          {t('assignment.return', 'Rückgabe')}
                        </Button>
                      ) : null}
                    </td>
                  </tr>
                ))}
                {(!data?.data || data.data.length === 0) && (
                  <tr>
                    <td colSpan={6} className="py-4 text-center text-gray-600 dark:text-gray-300">
                      {t('common.noData', 'Keine Daten')}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      )}

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('assignment.assign', 'Zuweisen')}
      >
        <div className="space-y-4">
          <Input
            label={t('assignment.assetId', 'Asset-ID')}
            value={form.asset_id}
            onChange={(e) => setForm({ ...form, asset_id: e.target.value })}
            required
          />
          <Select
            label={t('assignment.holderKind', 'Halter')}
            value={form.holder_kind}
            onChange={(e) => setForm({ ...form, holder_kind: e.target.value as HolderKind })}
            options={[
              { value: 'user', label: t('assignment.holderUser', 'Nutzer') },
              { value: 'team', label: t('assignment.holderTeam', 'Team') },
            ]}
          />
          <Input
            label={
              form.holder_kind === 'team'
                ? t('assignment.teamId', 'Team-ID')
                : t('assignment.userId', 'Nutzer-ID')
            }
            value={form.holder_id}
            onChange={(e) => setForm({ ...form, holder_id: e.target.value })}
            required
          />
          <Input
            type="date"
            label={t('assignment.dueAt', 'Fällig am')}
            value={form.due_date}
            onChange={(e) => setForm({ ...form, due_date: e.target.value })}
          />
          <Input
            label={t('common.notes', 'Notizen')}
            value={form.notes}
            onChange={(e) => setForm({ ...form, notes: e.target.value })}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button onClick={handleCreate} disabled={createMutation.isPending}>
              {t('common.save', 'Speichern')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
