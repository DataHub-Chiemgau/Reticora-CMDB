import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useStocktakeList, useCreateStocktake, useDeleteStocktake } from '../api/hooks';
import { Card } from '../components/ui/Card';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';

const statusOptions = [
  { value: '', label: 'Alle' },
  { value: 'planned', label: 'Geplant' },
  { value: 'in_progress', label: 'Laufend' },
  { value: 'completed', label: 'Abgeschlossen' },
  { value: 'cancelled', label: 'Abgebrochen' },
];

const scopeOptions = [
  { value: 'full', label: 'Vollständig' },
  { value: 'partial', label: 'Teilweise' },
  { value: 'site', label: 'Standort' },
  { value: 'room', label: 'Raum' },
  { value: 'rack', label: 'Rack' },
];

function statusVariant(status: string) {
  switch (status) {
    case 'planned':
      return 'neutral' as const;
    case 'in_progress':
      return 'warning' as const;
    case 'completed':
      return 'success' as const;
    case 'cancelled':
      return 'danger' as const;
    default:
      return 'neutral' as const;
  }
}

export function StocktakeListPage() {
  const { t } = useTranslation();
  const [status, setStatus] = useState('');
  const [search, setSearch] = useState('');
  const [showCreate, setShowCreate] = useState(false);

  const { data, isLoading } = useStocktakeList({ status, search });
  const createMutation = useCreateStocktake();
  const deleteMutation = useDeleteStocktake();

  const [form, setForm] = useState({ title: '', description: '', scope: 'full', due_date: '' });

  const handleCreate = () => {
    if (!form.title) return;
    createMutation.mutate(form, {
      onSuccess: () => {
        setShowCreate(false);
        setForm({ title: '', description: '', scope: 'full', due_date: '' });
      },
    });
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
          {t('stocktake.title', 'Inventur')}
        </h1>
        <Button onClick={() => setShowCreate(true)}>
          {t('stocktake.create', 'Inventur starten')}
        </Button>
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
        <Card title={`${t('stocktake.title', 'Inventur')} (${data?.total ?? 0})`}>
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th className="pb-2">{t('stocktake.name', 'Bezeichnung')}</th>
                  <th className="pb-2">{t('ci.status', 'Status')}</th>
                  <th className="pb-2">{t('stocktake.scope', 'Umfang')}</th>
                  <th className="pb-2">{t('stocktake.progress', 'Fortschritt')}</th>
                  <th className="pb-2">{t('stocktake.missing', 'Fehlend')}</th>
                  <th className="pb-2">{t('common.actions', 'Aktionen')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {data?.data.map((st) => (
                  <tr key={st.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2 font-medium">{st.title}</td>
                    <td className="py-2">
                      <Badge variant={statusVariant(st.status)}>{st.status}</Badge>
                    </td>
                    <td className="py-2">{st.scope}</td>
                    <td className="py-2">
                      {st.total_scanned}/{st.total_expected}
                    </td>
                    <td className="py-2">
                      {st.total_missing > 0 ? (
                        <Badge variant="danger">{st.total_missing}</Badge>
                      ) : (
                        '0'
                      )}
                    </td>
                    <td className="py-2">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => deleteMutation.mutate(st.id)}
                      >
                        {t('common.delete', 'Löschen')}
                      </Button>
                    </td>
                  </tr>
                ))}
                {(!data?.data || data.data.length === 0) && (
                  <tr>
                    <td colSpan={6} className="py-4 text-center text-gray-400">
                      {t('common.noData', 'Keine Daten')}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('stocktake.create', 'Inventur starten')}
      >
        <div className="space-y-4">
          <Input
            label={t('stocktake.name', 'Bezeichnung')}
            value={form.title}
            onChange={(e) => setForm({ ...form, title: e.target.value })}
            required
          />
          <Input
            label={t('common.description', 'Beschreibung')}
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
          />
          <Select
            label={t('stocktake.scope', 'Umfang')}
            value={form.scope}
            onChange={(e) => setForm({ ...form, scope: e.target.value })}
            options={scopeOptions}
          />
          <Input
            label={t('stocktake.dueDate', 'Fällig am')}
            value={form.due_date}
            onChange={(e) => setForm({ ...form, due_date: e.target.value })}
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
