import { useState } from 'react';
import { useTranslation } from 'react-i18next';
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

  const { data, isLoading } = useAssignmentList({ status, search });
  const createMutation = useCreateAssignment();
  const returnMutation = useReturnAssignment();

  const [form, setForm] = useState({ asset_id: '', assigned_to: '', notes: '' });

  const handleCreate = () => {
    if (!form.assigned_to || !form.asset_id) return;
    createMutation.mutate(form, {
      onSuccess: () => {
        setShowCreate(false);
        setForm({ asset_id: '', assigned_to: '', notes: '' });
      },
    });
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
          {t('assignment.title', 'Zuweisungen')}
        </h1>
        <Button onClick={() => setShowCreate(true)}>{t('assignment.assign', 'Zuweisen')}</Button>
      </div>

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
                  <th className="pb-2">{t('common.actions', 'Aktionen')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {data?.data.map((item) => (
                  <tr key={item.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2">{item.assignment_type}</td>
                    <td className="py-2">{item.assigned_to}</td>
                    <td className="py-2">
                      <Badge variant={statusVariant(item.status)}>
                        {t(`assignment.statuses.${item.status}`, item.status)}
                      </Badge>
                    </td>
                    <td className="py-2">
                      {new Date(item.assigned_at).toLocaleDateString('de-DE')}
                    </td>
                    <td className="py-2">
                      {item.status === 'active' && (
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => returnMutation.mutate({ id: item.id, data: {} })}
                        >
                          {t('assignment.return', 'Rückgabe')}
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
                {(!data?.data || data.data.length === 0) && (
                  <tr>
                    <td colSpan={5} className="py-4 text-center text-gray-600 dark:text-gray-300">
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
          <Input
            label={t('assignment.assignedTo', 'Zugewiesen an (User-ID)')}
            value={form.assigned_to}
            onChange={(e) => setForm({ ...form, assigned_to: e.target.value })}
            required
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
            <Button onClick={handleCreate}>{t('common.save', 'Speichern')}</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
