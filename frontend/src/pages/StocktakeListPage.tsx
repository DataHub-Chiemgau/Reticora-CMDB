import { ClipboardCheck, Plus, Search } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import {
  useStocktakeList,
  useCreateStocktake,
  useDeleteStocktake,
  useHasPermission,
} from '../api/hooks';
import { ApiError, type Stocktake } from '../api/client';
import { useLocationNames } from '../components/cmdb/ObjectHistory';
import { LocationPicker } from '../components/pickers/LocationPicker';
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
import { useUndoableDelete } from '../hooks/useUndoableDelete';
import { ScrollRegion } from '../components/ScrollRegion';
import { TableEmptyRow } from './ListParts';

/** The stocktake statuses; their labels come from the catalog. */
export const STOCKTAKE_STATUSES = ['planned', 'in_progress', 'completed', 'cancelled'] as const;
/**
 * STOCKTAKE_SCOPES are the deprecated scopes of stocktakes of earlier
 * releases; the list shows them for a stocktake without area only. A new
 * stocktake names its area by location_id (WP-288, WP-813).
 */
export const STOCKTAKE_SCOPES = ['full', 'partial', 'site', 'room', 'rack'] as const;

export function stocktakeStatusVariant(status: string) {
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
  const canWrite = useHasPermission('stocktake:write') === true;
  const { scheduleDelete } = useUndoableDelete();
  const locations = useLocationNames(true);
  const [hidden, setHidden] = useState<string[]>([]);

  const emptyForm = { title: '', description: '', due_date: '', location_id: '' };
  const [form, setForm] = useState(emptyForm);

  // The area is a node of the location tree of any kind (WP-288); the
  // deprecated scope is no longer sent.
  const handleCreate = () => {
    if (!form.title || !form.location_id) return;
    createMutation.mutate(
      {
        title: form.title,
        location_id: form.location_id,
        ...(form.description ? { description: form.description } : {}),
        ...(form.due_date ? { due_date: form.due_date } : {}),
      },
      {
        onSuccess: () => {
          setShowCreate(false);
          setForm(emptyForm);
        },
      },
    );
  };

  // Only a planned stocktake is deleted, behind the undo window.
  const handleDelete = (st: Stocktake) => {
    setHidden((current) => [...current, st.id]);
    scheduleDelete({
      id: st.id,
      label: st.title,
      deleteFn: () => deleteMutation.mutateAsync(st.id),
      restoreFn: async () => setHidden((current) => current.filter((id) => id !== st.id)),
    });
  };

  const areaOf = (st: Stocktake) =>
    st.location_id ? locations.path(st.location_id) : t(`stocktake.scopes.${st.scope}`, st.scope);

  const stocktakes = (data?.data ?? []).filter((st) => !hidden.includes(st.id));
  const filtered = Boolean(search || status);

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('stocktake.title', 'Inventur')}
        description={t('stocktake.summary')}
        actions={
          canWrite ? (
            <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
              {t('stocktake.create', 'Inventur starten')}
            </Button>
          ) : null
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
                    {t('stocktake.area')}
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
                    <td className={tdPrimaryClasses}>
                      <Link to={`/stocktake/${st.id}`} className="hover:underline">
                        {st.title}
                      </Link>
                    </td>
                    <td className={tdClasses}>
                      <Badge variant={stocktakeStatusVariant(st.status)}>
                        {t(`stocktake.statuses.${st.status}`, st.status)}
                      </Badge>
                    </td>
                    <td className={tdClasses}>{areaOf(st)}</td>
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
                      {canWrite && st.status === 'planned' ? (
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={t('stocktake.deleteNamed', { name: st.title })}
                          onClick={() => handleDelete(st)}
                        >
                          {t('common.delete', 'Löschen')}
                        </Button>
                      ) : null}
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
          <LocationPicker
            label={t('stocktake.area')}
            value={form.location_id || null}
            onChange={(next) => setForm({ ...form, location_id: next?.id ?? '' })}
          />
          <Input
            type="date"
            label={t('stocktake.dueDate', 'Fällig am')}
            value={form.due_date}
            onChange={(e) => setForm({ ...form, due_date: e.target.value })}
          />
          {createMutation.error ? (
            <p role="alert" className="text-sm text-danger">
              {createMutation.error instanceof ApiError
                ? (createMutation.error.detail ?? createMutation.error.message)
                : createMutation.error.message}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={handleCreate}
              disabled={!form.title || !form.location_id}
              loading={createMutation.isPending}
            >
              {t('stocktake.create', 'Inventur starten')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
