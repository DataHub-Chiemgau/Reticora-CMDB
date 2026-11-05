import { ClipboardCheck, Plus, Search } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useStocktakeList, useCreateStocktake, useDeleteStocktake } from '../api/hooks';
import { Card } from '../components/ui/Card';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';
import { ErrorState } from '../components/ui/ErrorState';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
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
import { ScrollRegion } from '../components/ScrollRegion';
import { TableEmptyRow } from './ListParts';

/** The stocktake statuses and scopes; their labels come from the catalog. */
export const STOCKTAKE_STATUSES = ['planned', 'in_progress', 'completed', 'cancelled'] as const;
export const STOCKTAKE_SCOPES = ['full', 'partial', 'site', 'room', 'rack'] as const;

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

  const { data, isLoading, error, refetch } = useStocktakeList({ status, search });
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

  const stocktakes = data?.data ?? [];
  const filtered = Boolean(search || status);

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('stocktake.title', 'Inventur')}
        description={t('stocktake.summary')}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('stocktake.create', 'Inventur starten')}
          </Button>
        }
      />

      <Toolbar>
        <Input
          type="text"
          icon={<Search />}
          placeholder={t('common.search', 'Suche')}
          aria-label={t('common.search', 'Suche')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full sm:w-64"
        />
        <Select
          aria-label={t('ci.status', 'Status')}
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={[
            { value: '', label: t('common.allStatuses') },
            ...STOCKTAKE_STATUSES.map((value) => ({
              value,
              label: t(`stocktake.statuses.${value}`),
            })),
          ]}
          className="w-40 sm:w-44"
        />
        {data ? (
          <ToolbarEnd>
            <span className="text-sm text-fg-muted tabular-nums">
              {data.total} {t('common.entries')}
            </span>
          </ToolbarEnd>
        ) : null}
      </Toolbar>

      {isLoading ? <SkeletonList rows={5} label={t('app.loading', 'Laden...')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      {data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('stocktake.title', 'Inventur')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('stocktake.name', 'Bezeichnung')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status', 'Status')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('stocktake.scope', 'Umfang')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('stocktake.progress', 'Fortschritt')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('stocktake.missing', 'Fehlend')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('common.actions', 'Aktionen')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {stocktakes.map((st) => (
                  <tr key={st.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{st.title}</td>
                    <td className={tdClasses}>
                      <Badge variant={statusVariant(st.status)}>
                        {t(`stocktake.statuses.${st.status}`, st.status)}
                      </Badge>
                    </td>
                    <td className={tdClasses}>{t(`stocktake.scopes.${st.scope}`, st.scope)}</td>
                    <td className={tdNumericClasses}>
                      {st.total_scanned}/{st.total_expected}
                    </td>
                    <td className={tdNumericClasses}>
                      {st.total_missing > 0 ? (
                        <Badge variant="danger">{st.total_missing}</Badge>
                      ) : (
                        '0'
                      )}
                    </td>
                    <td className={cn(tdClasses, 'text-right')}>
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
                {stocktakes.length === 0 ? (
                  <TableEmptyRow
                    colSpan={6}
                    icon={<ClipboardCheck />}
                    title={filtered ? t('common.noResults') : t('stocktake.emptyTitle')}
                    description={filtered ? t('common.noResultsHint') : t('stocktake.emptyHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

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
            options={STOCKTAKE_SCOPES.map((value) => ({
              value,
              label: t(`stocktake.scopes.${value}`),
            }))}
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
            <Button onClick={handleCreate}>{t('stocktake.create', 'Inventur starten')}</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
