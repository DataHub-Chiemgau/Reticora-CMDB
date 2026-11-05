import { ClipboardList, Search, UserPlus } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueries } from '@tanstack/react-query';
import { assetApi, teamApi, userApi, type Assignment } from '../api/client';
import { useAssignmentList } from '../api/hooks';
import {
  AssignDialog,
  ReturnDialog,
  TransferDialog,
  type HeldAsset,
} from '../components/flows/AssignDialogs';
import { Card } from '../components/ui/Card';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
import { TableEmptyRow } from './ListParts';

// Statuses of the projection assignment_projection (000257, WP-747):
// English keys, the labels come from the catalog.
export const ASSIGNMENT_STATUSES = ['active', 'returned', 'transferred', 'overdue'] as const;

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

/** holderOf is the current holder of a row: its user, else its team. */
function holderOf(item: Assignment): string | undefined {
  return item.assigned_to || item.assigned_team_id || undefined;
}

const unique = (values: (string | undefined)[]) => [
  ...new Set(values.filter((value): value is string => !!value)),
];

const NAME_STALE_MS = 5 * 60_000;

/**
 * useRowNames names the assets and holders of the rows. The projection
 * carries only their ids (WP-747), so the page reads each asset, user and
 * team once (cached); a name that cannot be read (no right, deleted) falls
 * back to what the row holds.
 */
function useRowNames(rows: Assignment[]) {
  const assetIds = unique(rows.map((row) => row.asset_id));
  const userIds = unique(rows.map((row) => (row.assigned_team_id ? undefined : row.assigned_to)));
  const teamIds = unique(rows.map((row) => row.assigned_team_id));
  const common = { retry: false, staleTime: NAME_STALE_MS } as const;
  const assets = useQueries({
    queries: assetIds.map((id) => ({
      ...common,
      queryKey: ['asset', id],
      queryFn: () => assetApi.get(id),
    })),
  });
  const users = useQueries({
    queries: userIds.map((id) => ({
      ...common,
      queryKey: ['user', id],
      queryFn: () => userApi.get(id),
    })),
  });
  const teams = useQueries({
    queries: teamIds.map((id) => ({
      ...common,
      queryKey: ['team', id],
      queryFn: () => teamApi.get(id),
    })),
  });

  const assetNames = new Map<string, string>();
  assets.forEach((query, index) => {
    const asset = query.data;
    if (typeof asset?.name !== 'string') return;
    const code = asset.inventory_no ?? asset.asset_tag;
    assetNames.set(assetIds[index]!, code ? `${asset.name} (${code})` : asset.name);
  });
  const holderNames = new Map<string, string>();
  users.forEach((query, index) => {
    if (typeof query.data?.display_name === 'string') {
      holderNames.set(userIds[index]!, query.data.display_name);
    }
  });
  teams.forEach((query, index) => {
    if (typeof query.data?.name === 'string') holderNames.set(teamIds[index]!, query.data.name);
  });
  return { assetNames, holderNames };
}

type OpenDialog = { kind: 'return' | 'transfer'; target: HeldAsset } | null;

export function AssignmentListPage() {
  const { t } = useTranslation();
  const statusOptions = [
    { value: '', label: t('common.allStatuses') },
    ...ASSIGNMENT_STATUSES.map((value) => ({
      value,
      label: t(`assignment.statuses.${value}`, value),
    })),
  ];
  const [status, setStatus] = useState('');
  const [search, setSearch] = useState('');
  const [assigning, setAssigning] = useState(false);
  const [dialog, setDialog] = useState<OpenDialog>(null);

  const { data, isLoading, error, refetch } = useAssignmentList({ status, search });
  const rows = data?.data ?? [];
  const { assetNames, holderNames } = useRowNames(rows);

  const assetLabel = (item: Assignment) =>
    item.asset_id ? (assetNames.get(item.asset_id) ?? t('assignment.unknownAsset')) : '';
  const holderLabel = (item: Assignment) => {
    const holder = holderOf(item);
    if (!holder) return '';
    const name = holderNames.get(holder) ?? holder;
    return item.assigned_team_id ? t('assignment.teamHolder', { name }) : name;
  };

  /** heldAsset is the target of return and transfer of an open row. */
  const heldAsset = (item: Assignment): HeldAsset | null => {
    const holder = holderOf(item);
    if (!item.asset_id || !holder) return null;
    if (item.status !== 'active' && item.status !== 'overdue') return null;
    return {
      assetId: item.asset_id,
      assetLabel: assetLabel(item),
      holder,
      holderLabel: holderLabel(item),
    };
  };

  const filtered = Boolean(search || status);

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('assignment.title')}
        description={t('assignment.summary')}
        actions={
          <Button icon={<UserPlus />} onClick={() => setAssigning(true)}>
            {t('assignment.assign')}
          </Button>
        }
      />

      <Toolbar>
        <Input
          type="text"
          icon={<Search />}
          placeholder={t('common.search')}
          aria-label={t('common.search')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full sm:w-64"
        />
        <Select
          aria-label={t('ci.status')}
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={statusOptions}
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

      {isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
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
          <ScrollRegion label={t('assignment.title')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('assignment.asset')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('assignment.assignedTo')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('assignment.type')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('assignment.assignedAt')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('assignment.dueAt')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('common.actions')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((item) => {
                  const target = heldAsset(item);
                  return (
                    <tr key={item.id} className={trClasses}>
                      <td className={tdPrimaryClasses}>{assetLabel(item)}</td>
                      <td className={tdClasses}>{holderLabel(item)}</td>
                      <td className={tdClasses}>
                        {t(`assignment.types.${item.assignment_type}`, item.assignment_type)}
                      </td>
                      <td className={tdClasses}>
                        <Badge variant={statusVariant(item.status)}>
                          {t(`assignment.statuses.${item.status}`, item.status)}
                        </Badge>
                      </td>
                      <td className={cn(tdClasses, 'whitespace-nowrap tabular-nums')}>
                        {new Date(item.assigned_at).toLocaleDateString('de-DE')}
                      </td>
                      <td className={cn(tdClasses, 'whitespace-nowrap tabular-nums')}>
                        {item.due_at ? new Date(item.due_at).toLocaleDateString('de-DE') : '—'}
                      </td>
                      <td className={cn(tdClasses, 'text-right')}>
                        {target ? (
                          <div className="flex justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="sm"
                              aria-label={t('assignment.returnOf', { asset: target.assetLabel })}
                              onClick={() => setDialog({ kind: 'return', target })}
                            >
                              {t('assignment.return')}
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              aria-label={t('assignment.transferOf', { asset: target.assetLabel })}
                              onClick={() => setDialog({ kind: 'transfer', target })}
                            >
                              {t('assignment.transfer')}
                            </Button>
                          </div>
                        ) : null}
                      </td>
                    </tr>
                  );
                })}
                {rows.length === 0 ? (
                  <TableEmptyRow
                    colSpan={7}
                    icon={<ClipboardList />}
                    title={filtered ? t('common.noResults') : t('assignment.emptyTitle')}
                    description={filtered ? t('common.noResultsHint') : t('assignment.emptyHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

      <AssignDialog open={assigning} onOpenChange={setAssigning} />
      <ReturnDialog
        open={dialog?.kind === 'return'}
        onOpenChange={(open) => !open && setDialog(null)}
        target={dialog?.kind === 'return' ? dialog.target : null}
      />
      <TransferDialog
        open={dialog?.kind === 'transfer'}
        onOpenChange={(open) => !open && setDialog(null)}
        target={dialog?.kind === 'transfer' ? dialog.target : null}
      />
    </div>
  );
}
