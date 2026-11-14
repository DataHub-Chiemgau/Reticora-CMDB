import { useMemo, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Link, useNavigate, useParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { CalendarClock, Handshake, Pencil, Plus, Trash2 } from 'lucide-react';
import {
  API_MAX_LIMIT,
  ApiError,
  contractApi,
  documentApi,
  type AssetFilter,
  type Contract,
  type ContractCreateRequest,
  type ContractUpdateRequest,
  type FieldErrorInfo,
  type Supplier,
} from '../api/client';
import {
  useContract,
  useContracts,
  useCreateContract,
  useDeleteContract,
  useHasPermission,
  useSuppliers,
  useUpdateContract,
} from '../api/hooks';
import { useClients } from '../api/cmdbHooks';
import { LinkedDocuments } from '../components/documents/LinkedDocuments';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Textarea } from '../components/ui/Textarea';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { ScrollRegion } from '../components/ScrollRegion';
import { useScopeStore } from '../stores/scope';

/** The values of contract.status (CHECK of WP-269), labelled under contract.statuses. */
export const CONTRACT_STATUSES = ['draft', 'active', 'terminated', 'ended'] as const;
export type ContractStatus = (typeof CONTRACT_STATUSES)[number];

/** The values of contract.kind after E-119 (CHECK of WP-269), labelled under contract.kinds. */
export const CONTRACT_KINDS = [
  'maintenance',
  'support',
  'lease',
  'license',
  'purchase',
  'service',
  'other',
] as const;
export type ContractKind = (typeof CONTRACT_KINDS)[number];

/** The sort fields of GET /contracts the list offers (ascending). */
export const CONTRACT_SORTS = ['number', 'start_date', 'end_date'] as const;

/** PAGE_SIZE is the page of the contract list (limit, API-02). */
const PAGE_SIZE = 50;

const STATUS_VARIANTS: Record<ContractStatus, 'success' | 'warning' | 'neutral' | 'info'> = {
  draft: 'info',
  active: 'success',
  terminated: 'warning',
  ended: 'neutral',
};

const DAY_MS = 24 * 60 * 60 * 1000;

/** parseDay reads a date (YYYY-MM-DD) as milliseconds of its UTC midnight. */
function parseDay(day: string): number {
  const [year = 0, month = 1, date = 1] = day.slice(0, 10).split('-').map(Number);
  return Date.UTC(year, month - 1, date);
}

/** todayISO is the local calendar day of now as YYYY-MM-DD. */
export function todayISO(now: Date = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

/**
 * noticeDeadline is the last day to give notice: end_date minus
 * notice_period_days, end_date itself without a period; none without an end.
 */
export function noticeDeadline(
  contract: Pick<Contract, 'end_date' | 'notice_period_days'>,
): string | undefined {
  if (!contract.end_date) return undefined;
  const days = contract.notice_period_days ?? 0;
  return new Date(parseDay(contract.end_date) - days * DAY_MS).toISOString().slice(0, 10);
}

/** daysUntil counts the calendar days from today to day (negative when passed). */
export function daysUntil(day: string, today: string): number {
  return Math.round((parseDay(day) - parseDay(today)) / DAY_MS);
}

/**
 * NoticeStage is the highlight of a notice deadline after the stages of
 * WP-270 (30, 7 and 1 day ahead) and a passed deadline.
 */
export type NoticeStage = 'passed' | 'd1' | 'd7' | 'd30';

/** noticeStage puts a deadline into its stage; one further away than 30 days has none. */
export function noticeStage(deadline: string, today: string): NoticeStage | undefined {
  const days = daysUntil(deadline, today);
  if (days < 0) return 'passed';
  if (days <= 1) return 'd1';
  if (days <= 7) return 'd7';
  if (days <= 30) return 'd30';
  return undefined;
}

const STAGE_VARIANTS: Record<NoticeStage, 'danger' | 'warning' | 'info'> = {
  passed: 'danger',
  d1: 'danger',
  d7: 'warning',
  d30: 'info',
};

/** isRunning tells a contract that still needs a notice: not terminated or ended. */
function isRunning(contract: Contract): boolean {
  return contract.status !== 'terminated' && contract.status !== 'ended';
}

/** useDateFormat formats a date (YYYY-MM-DD) in the UI language, as a calendar day. */
function useDateFormat() {
  const { i18n } = useTranslation();
  return useMemo(() => {
    const format = new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeZone: 'UTC' });
    return (day?: string | null) => (day ? format.format(parseDay(day)) : '–');
  }, [i18n.language]);
}

/** contractValue shows the value of a contract with its currency; an unknown code stays as text. */
function contractValue(contract: Pick<Contract, 'value' | 'currency'>, lang: string) {
  const { value, currency } = contract;
  if (value === null || value === undefined) return '–';
  if (!currency) return new Intl.NumberFormat(lang).format(value);
  try {
    return new Intl.NumberFormat(lang, { style: 'currency', currency }).format(value);
  } catch {
    return `${new Intl.NumberFormat(lang).format(value)} ${currency}`;
  }
}

/** NoticeBadge highlights a deadline by its stage; outside the stages it is plain text. */
function NoticeBadge({ deadline, today }: { deadline?: string; today: string }) {
  const { t } = useTranslation();
  const formatDate = useDateFormat();
  if (!deadline) return <span>–</span>;
  const stage = noticeStage(deadline, today);
  if (!stage) return <span className="tabular-nums">{formatDate(deadline)}</span>;
  const days = daysUntil(deadline, today);
  const label =
    stage === 'passed'
      ? t('contract.expiry.passed')
      : days === 0
        ? t('contract.expiry.today')
        : days === 1
          ? t('contract.expiry.tomorrow')
          : t('contract.expiry.inDays', { count: days });
  return (
    <span className="flex flex-wrap items-center gap-2">
      <span className="tabular-nums">{formatDate(deadline)}</span>
      <Badge variant={STAGE_VARIANTS[stage]}>{label}</Badge>
    </span>
  );
}

/** clientFilter is filter[client_id] of the chosen scope: /contracts reads no client_id. */
function useClientFilter(): AssetFilter[] {
  const clientId = useScopeStore((state) => state.clientId);
  return useMemo(() => (clientId ? [{ field: 'client_id', value: clientId }] : []), [clientId]);
}

/** useSupplierNames reads the suppliers (one page of 200) for names and the selection. */
function useSupplierCatalog() {
  const suppliers = useSuppliers({ limit: API_MAX_LIMIT, sort: 'name' });
  const rows = useMemo(() => suppliers.data?.data ?? [], [suppliers.data]);
  const names = useMemo(() => new Map(rows.map((s) => [s.id, s.name])), [rows]);
  return { rows, names };
}

/** refusal words a refused contract write that no field shows. */
function refusal(error: unknown, t: TFunction, fields: Record<string, FieldErrorInfo>): string {
  if (error instanceof ApiError) {
    const marked = Object.keys(fields).length > 0;
    if (error.status === 409) return t('contract.errors.numberTaken');
    if (error.status === 403) {
      return marked ? t('contract.errors.forbiddenFields') : t('contract.errors.forbidden');
    }
    if (error.status === 404) return t('contract.errors.notFound');
    if ((error.status === 422 || error.status === 400) && marked) {
      return t('contract.errors.invalid');
    }
    return t('contract.errors.reason', { detail: error.detail ?? error.message });
  }
  return t('contract.errors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

/** fieldKey maps a field of a problem onto the request field (core.value → value). */
function fieldKey(field: string): string {
  return field.startsWith('core.') ? field.slice(5) : field;
}

/**
 * ContractsPage is the contract administration (MGT-12, UI-14, WP-808):
 * /contracts lists the contracts with filters, sort and the expiry
 * overview; /contracts/:id shows one with its documents.
 */
export function ContractsPage() {
  const { id } = useParams<{ id: string }>();
  return id ? <ContractDetail id={id} /> : <ContractList />;
}

type DialogState = { mode: 'create' } | { mode: 'edit'; contract: Contract };

/** useContractDialog keeps the open dialog and reloads a stale contract (412). */
function useContractDialog() {
  const queryClient = useQueryClient();
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const reload = async (id: string) => {
    const fresh = await contractApi.get(id);
    void queryClient.invalidateQueries({ queryKey: ['contracts'] });
    setDialog({ mode: 'edit', contract: fresh });
  };
  return { dialog, setDialog, reload };
}

function ContractList() {
  const { t, i18n } = useTranslation();
  const formatDate = useDateFormat();
  const mayWrite = useHasPermission('contract:write') === true;
  const clientFilter = useClientFilter();
  const { rows: suppliers, names } = useSupplierCatalog();
  const [status, setStatus] = useState('');
  const [kind, setKind] = useState('');
  const [supplierId, setSupplierId] = useState('');
  const [sort, setSort] = useState<string>('number');
  // The cursors of the pages shown so far; the last one is the current page.
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);
  const cursor = cursors[cursors.length - 1];
  const filter: AssetFilter[] = [
    ...(status ? [{ field: 'status', value: status }] : []),
    ...(kind ? [{ field: 'kind', value: kind }] : []),
    ...(supplierId ? [{ field: 'supplier_id', value: supplierId }] : []),
    ...clientFilter,
  ];
  const contracts = useContracts(
    { limit: PAGE_SIZE, sort, filter, ...(cursor ? { cursor } : {}) },
    { keepPrevious: true },
  );
  const { dialog, setDialog, reload } = useContractDialog();
  const [deleting, setDeleting] = useState<Contract | null>(null);
  const today = todayISO();

  const rows = contracts.data?.data ?? [];
  const nextCursor = contracts.data?.next_cursor;
  const forbidden = contracts.error instanceof ApiError && contracts.error.status === 403;
  const resetPages = () => setCursors([undefined]);

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('contract.title')}
        description={t('contract.subtitle')}
        actions={
          mayWrite ? (
            <Button icon={<Plus />} onClick={() => setDialog({ mode: 'create' })}>
              {t('contract.create')}
            </Button>
          ) : null
        }
      />
      {!mayWrite ? <p className="text-sm text-fg-muted">{t('contract.readOnly')}</p> : null}

      <ExpiryOverview names={names} />

      <Toolbar label={t('contract.title')}>
        <Select
          aria-label={t('contract.filters.status')}
          value={status}
          onChange={(e) => {
            setStatus(e.target.value);
            resetPages();
          }}
          options={[
            { value: '', label: t('common.allStatuses') },
            ...CONTRACT_STATUSES.map((value) => ({
              value,
              label: t(`contract.statuses.${value}`),
            })),
          ]}
          className="w-40"
        />
        <Select
          aria-label={t('contract.filters.kind')}
          value={kind}
          onChange={(e) => {
            setKind(e.target.value);
            resetPages();
          }}
          options={[
            { value: '', label: t('contract.filters.allKinds') },
            ...CONTRACT_KINDS.map((value) => ({ value, label: t(`contract.kinds.${value}`) })),
          ]}
          className="w-40"
        />
        <Select
          aria-label={t('contract.filters.supplier')}
          value={supplierId}
          onChange={(e) => {
            setSupplierId(e.target.value);
            resetPages();
          }}
          options={[
            { value: '', label: t('contract.filters.allSuppliers') },
            ...suppliers.map((s) => ({ value: s.id, label: s.name })),
          ]}
          className="w-48"
        />
        <Select
          aria-label={t('contract.filters.sort')}
          value={sort}
          onChange={(e) => {
            setSort(e.target.value);
            resetPages();
          }}
          options={CONTRACT_SORTS.map((value) => ({
            value,
            label: t(`contract.sorts.${value}`),
          }))}
          className="w-36"
        />
        {contracts.data ? (
          <ToolbarEnd>
            <span className="text-sm text-fg-muted tabular-nums">
              {contracts.data.total} {t('common.entries')}
            </span>
          </ToolbarEnd>
        ) : null}
      </Toolbar>

      <Card className="overflow-hidden p-0">
        {contracts.isLoading ? (
          <div className="p-5">
            <SkeletonList rows={4} label={t('app.loading')} />
          </div>
        ) : forbidden ? (
          <div className="p-5">
            <ErrorState title={t('contract.forbiddenRead')} />
          </div>
        ) : contracts.error ? (
          <div className="p-5">
            <ErrorState
              title={t('contract.loadError')}
              retryLabel={t('common.retry')}
              onRetry={() => void contracts.refetch()}
            />
          </div>
        ) : rows.length === 0 ? (
          <div className="p-5">
            <EmptyState icon={<Handshake />} title={t('contract.empty')} />
          </div>
        ) : (
          <ScrollRegion label={t('contract.title')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('contract.fields.number')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('contract.fields.kind')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('contract.fields.supplier')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('contract.fields.term')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('contract.fields.noticeDate')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('contract.fields.status')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('contract.fields.value')}
                  </th>
                  {mayWrite ? (
                    <th scope="col" className={thClasses}>
                      <span className="sr-only">{t('common.actions')}</span>
                    </th>
                  ) : null}
                </tr>
              </thead>
              <tbody>
                {rows.map((contract) => (
                  <tr key={contract.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>
                      <Link to={`/contracts/${contract.id}`} className="hover:underline">
                        {contract.number}
                      </Link>
                    </td>
                    <td className={tdClasses}>
                      {contract.kind ? t(`contract.kinds.${contract.kind}`, contract.kind) : '–'}
                    </td>
                    <td className={tdClasses}>
                      {contract.supplier_id ? (names.get(contract.supplier_id) ?? '–') : '–'}
                    </td>
                    <td className={`${tdClasses} tabular-nums`}>
                      {formatDate(contract.start_date)} – {formatDate(contract.end_date)}
                    </td>
                    <td className={tdClasses}>
                      <NoticeBadge
                        deadline={isRunning(contract) ? noticeDeadline(contract) : undefined}
                        today={today}
                      />
                    </td>
                    <td className={tdClasses}>
                      {contract.status ? (
                        <Badge variant={STATUS_VARIANTS[contract.status]}>
                          {t(`contract.statuses.${contract.status}`, contract.status)}
                        </Badge>
                      ) : (
                        '–'
                      )}
                    </td>
                    <td className={`${tdClasses} tabular-nums`}>
                      {contractValue(contract, i18n.language)}
                    </td>
                    {mayWrite ? (
                      <td className={tdClasses}>
                        <span className="flex justify-end gap-1">
                          <IconButton
                            size="sm"
                            icon={<Pencil />}
                            label={t('contract.editLabel', { name: contract.number })}
                            onClick={() => setDialog({ mode: 'edit', contract })}
                          />
                          <IconButton
                            size="sm"
                            icon={<Trash2 />}
                            label={t('contract.deleteLabel', { name: contract.number })}
                            onClick={() => setDeleting(contract)}
                            className="hover:text-danger"
                          />
                        </span>
                      </td>
                    ) : null}
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        )}
        {cursors.length > 1 || nextCursor ? (
          <div className="flex justify-end gap-2 border-t border-line px-5 py-3">
            <Button
              variant="secondary"
              size="sm"
              disabled={cursors.length <= 1}
              onClick={() => setCursors((c) => c.slice(0, -1))}
            >
              {t('common.back')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={!nextCursor}
              onClick={() => nextCursor && setCursors((c) => [...c, nextCursor])}
            >
              {t('common.next')}
            </Button>
          </div>
        ) : null}
      </Card>

      {dialog ? (
        <ContractDialog
          key={dialog.mode === 'edit' ? `${dialog.contract.id}:${dialog.contract.version}` : 'new'}
          state={dialog}
          suppliers={suppliers}
          onClose={() => setDialog(null)}
          onReload={reload}
        />
      ) : null}
      <DeleteContractDialog contract={deleting} onClose={() => setDeleting(null)} />
    </div>
  );
}

/**
 * ExpiryOverview lists the running contracts by end date from today
 * (sort=end_date, filter[end_date][gte]) whose notice deadline falls into a
 * stage of WP-270; a passed deadline is a notice of its own.
 */
function ExpiryOverview({ names }: { names: Map<string, string> }) {
  const { t } = useTranslation();
  const formatDate = useDateFormat();
  const today = todayISO();
  const clientFilter = useClientFilter();
  const expiring = useContracts({
    sort: 'end_date',
    limit: API_MAX_LIMIT,
    filter: [{ field: 'end_date', op: 'gte', value: today }, ...clientFilter],
  });
  const entries = (expiring.data?.data ?? [])
    .filter(isRunning)
    .map((contract) => {
      const deadline = noticeDeadline(contract);
      return { contract, deadline, stage: deadline ? noticeStage(deadline, today) : undefined };
    })
    .filter(
      (entry): entry is { contract: Contract; deadline: string; stage: NoticeStage } =>
        entry.stage !== undefined && entry.deadline !== undefined,
    )
    .sort((a, b) => a.deadline.localeCompare(b.deadline));
  const passed = entries.filter((entry) => entry.stage === 'passed');
  const upcoming = entries.filter((entry) => entry.stage !== 'passed');

  if (expiring.error) return null;
  return (
    <Card title={t('contract.expiry.title')}>
      <p className="mb-3 text-sm text-fg-muted">{t('contract.expiry.subtitle')}</p>
      {expiring.isLoading ? <SkeletonList rows={2} label={t('app.loading')} /> : null}
      {passed.length > 0 ? (
        <ul className="mb-3 space-y-2" aria-label={t('contract.expiry.passed')}>
          {passed.map(({ contract, deadline }) => (
            <li key={contract.id}>
              <div
                role="note"
                className="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-fg dark:border-red-500/30 dark:bg-red-500/10"
              >
                <span className="flex flex-wrap items-center gap-2">
                  <Badge variant="danger">{t('contract.expiry.passed')}</Badge>
                  <Link to={`/contracts/${contract.id}`} className="font-medium hover:underline">
                    {contract.number}
                  </Link>
                </span>
                <span className="mt-1 block">
                  {t('contract.expiry.passedHint', {
                    date: formatDate(deadline),
                    end: formatDate(contract.end_date),
                  })}
                </span>
              </div>
            </li>
          ))}
        </ul>
      ) : null}
      {upcoming.length > 0 ? (
        <ul className="divide-y divide-line" aria-label={t('contract.expiry.title')}>
          {upcoming.map(({ contract, deadline }) => (
            <li key={contract.id} className="flex flex-wrap items-center gap-3 py-2 text-sm">
              <Link
                to={`/contracts/${contract.id}`}
                className="min-w-0 font-medium text-fg hover:underline"
              >
                {contract.number}
              </Link>
              <span className="text-fg-muted">
                {contract.supplier_id ? (names.get(contract.supplier_id) ?? '') : ''}
              </span>
              <span className="ml-auto flex flex-wrap items-center gap-3">
                <NoticeBadge deadline={deadline} today={today} />
                <span className="text-fg-muted tabular-nums">
                  {t('contract.expiry.ends', { date: formatDate(contract.end_date) })}
                </span>
              </span>
            </li>
          ))}
        </ul>
      ) : null}
      {expiring.data && entries.length === 0 ? (
        <p className="flex items-center gap-2 text-sm text-fg-muted">
          <CalendarClock aria-hidden="true" className="h-4 w-4" />
          {t('contract.expiry.empty')}
        </p>
      ) : null}
    </Card>
  );
}

/** ContractDetail shows the master data and the documents of one contract. */
function ContractDetail({ id }: { id: string }) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const formatDate = useDateFormat();
  const mayWrite = useHasPermission('contract:write') === true;
  const mayWriteDocuments = useHasPermission('document:write') === true;
  const contract = useContract(id);
  const { rows: suppliers, names } = useSupplierCatalog();
  const clients = useClients();
  const { dialog, setDialog, reload } = useContractDialog();
  const [deleting, setDeleting] = useState<Contract | null>(null);
  const today = todayISO();
  const data = contract.data;
  const crumbs = [{ label: t('contract.title'), to: '/contracts' }];

  if (contract.isLoading) {
    return <SkeletonList rows={4} label={t('app.loading')} />;
  }
  if (!data) {
    const status = contract.error instanceof ApiError ? contract.error.status : undefined;
    return (
      <div className="space-y-4">
        <PageHeader title={t('contract.title')} breadcrumbs={crumbs} />
        <ErrorState
          title={
            status === 403
              ? t('contract.forbiddenRead')
              : status === 404
                ? t('contract.notFound')
                : t('contract.loadError')
          }
        />
        <Link to="/contracts" className="text-sm text-accent hover:underline">
          {t('contract.backToList')}
        </Link>
      </div>
    );
  }

  const clientName = data.client_id
    ? (clients.data?.data.find((c) => c.id === data.client_id)?.name ?? data.client_id)
    : t('contract.orgWide');
  const facts: [string, ReactNode][] = [
    [t('contract.fields.number'), data.number],
    [t('contract.fields.kind'), data.kind ? t(`contract.kinds.${data.kind}`, data.kind) : '–'],
    [
      t('contract.fields.supplier'),
      data.supplier_id ? (names.get(data.supplier_id) ?? data.supplier_id) : '–',
    ],
    [t('contract.fields.client'), clientName],
    [t('contract.fields.startDate'), formatDate(data.start_date)],
    [t('contract.fields.endDate'), formatDate(data.end_date)],
    [
      t('contract.fields.noticePeriodDays'),
      data.notice_period_days === null || data.notice_period_days === undefined
        ? '–'
        : String(data.notice_period_days),
    ],
    [
      t('contract.fields.noticeDate'),
      <NoticeBadge
        key="notice"
        deadline={isRunning(data) ? noticeDeadline(data) : undefined}
        today={today}
      />,
    ],
    [
      t('contract.fields.status'),
      data.status ? (
        <Badge key="status" variant={STATUS_VARIANTS[data.status]}>
          {t(`contract.statuses.${data.status}`, data.status)}
        </Badge>
      ) : (
        '–'
      ),
    ],
    [t('contract.fields.value'), contractValue(data, i18n.language)],
    [t('contract.fields.notes'), data.notes ? data.notes : '–'],
  ];

  return (
    <div className="space-y-4">
      <PageHeader
        title={data.number}
        breadcrumbs={crumbs}
        actions={
          mayWrite ? (
            <div className="flex gap-2">
              <Button
                variant="secondary"
                icon={<Trash2 />}
                onClick={() => setDeleting(data)}
                aria-label={t('contract.deleteLabel', { name: data.number })}
              >
                {t('common.delete')}
              </Button>
              <Button
                icon={<Pencil />}
                onClick={() => setDialog({ mode: 'edit', contract: data })}
                aria-label={t('contract.editLabel', { name: data.number })}
              >
                {t('common.edit')}
              </Button>
            </div>
          ) : null
        }
      />
      <Card title={t('contract.details')}>
        <dl className="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2">
          {facts.map(([label, value]) => (
            <div key={label} className="min-w-0">
              <dt className="text-xs font-medium text-fg-muted">{label}</dt>
              <dd className="text-sm text-fg [overflow-wrap:anywhere]">{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <LinkedDocuments
        entityType="contract"
        entityId={data.id}
        title={t('contract.documents')}
        canWrite={mayWrite && mayWriteDocuments}
        load={async () =>
          (
            await documentApi.list({
              entity_type: 'contract',
              entity_id: data.id,
              limit: API_MAX_LIMIT,
            })
          ).data
        }
      />

      {dialog ? (
        <ContractDialog
          key={dialog.mode === 'edit' ? `${dialog.contract.id}:${dialog.contract.version}` : 'new'}
          state={dialog}
          suppliers={suppliers}
          onClose={() => setDialog(null)}
          onReload={reload}
        />
      ) : null}
      <DeleteContractDialog
        contract={deleting}
        onClose={() => setDeleting(null)}
        onDeleted={() => navigate('/contracts')}
      />
    </div>
  );
}

/** The inputs of the contract dialog as text. */
interface ContractForm {
  number: string;
  kind: string;
  supplier_id: string;
  client_id: string;
  start_date: string;
  end_date: string;
  notice_period_days: string;
  value: string;
  currency: string;
  status: string;
  notes: string;
}

function formOf(contract: Contract | undefined, clientId: string): ContractForm {
  return {
    number: contract?.number ?? '',
    kind: contract?.kind ?? '',
    supplier_id: contract?.supplier_id ?? '',
    client_id: contract ? (contract.client_id ?? '') : clientId,
    start_date: contract?.start_date ?? '',
    end_date: contract?.end_date ?? '',
    notice_period_days:
      contract?.notice_period_days === null || contract?.notice_period_days === undefined
        ? ''
        : String(contract.notice_period_days),
    value: contract?.value === null || contract?.value === undefined ? '' : String(contract.value),
    currency: contract?.currency ?? '',
    status: contract?.status ?? 'active',
    notes: contract?.notes ?? '',
  };
}

/** orNull is the value of an optional text input: empty is null. */
function orNull(value: string): string | null {
  const trimmed = value.trim();
  return trimmed === '' ? null : trimmed;
}

function numberOrNull(value: string): number | null {
  const trimmed = value.trim();
  return trimmed === '' ? null : Number(trimmed);
}

/** valuesOf turns the form into the request fields; client_id stays apart. */
function valuesOf(form: ContractForm) {
  return {
    number: form.number.trim(),
    kind: orNull(form.kind) as ContractKind | null,
    supplier_id: orNull(form.supplier_id),
    start_date: orNull(form.start_date),
    end_date: orNull(form.end_date),
    notice_period_days: numberOrNull(form.notice_period_days),
    value: numberOrNull(form.value),
    currency: orNull(form.currency)?.toUpperCase() ?? null,
    status: form.status as ContractStatus,
    notes: orNull(form.notes),
  };
}

interface ContractDialogProps {
  state: DialogState;
  suppliers: Supplier[];
  onClose: () => void;
  /** onReload reads the contract again after a 412 and reopens the dialog with it. */
  onReload: (id: string) => Promise<void>;
}

/**
 * ContractDialog creates a contract or changes one. A change sends only the
 * changed fields with If-Match from version; a 412 offers to reload, a 409
 * on number, a 422 and a 403 of the field permissions mark the inputs.
 */
function ContractDialog({ state, suppliers, onClose, onReload }: ContractDialogProps) {
  const { t } = useTranslation();
  const editing = state.mode === 'edit' ? state.contract : undefined;
  const scopeClient = useScopeStore((s) => s.clientId);
  const clients = useClients();
  const [form, setForm] = useState<ContractForm>(() => formOf(editing, scopeClient));
  const [fieldErrors, setFieldErrors] = useState<Record<string, FieldErrorInfo>>({});
  const [notice, setNotice] = useState<string>();
  const [stale, setStale] = useState(false);
  const [reloading, setReloading] = useState(false);
  const create = useCreateContract();
  const update = useUpdateContract();
  const saving = create.isPending || update.isPending;

  const set = (key: keyof ContractForm) => (value: string) =>
    setForm((current) => ({ ...current, [key]: value }));
  // Offered are org-wide suppliers and those of the contract's client.
  const supplierOptions = suppliers.filter(
    (s) => !s.client_id || s.client_id === form.client_id || s.id === form.supplier_id,
  );

  const fieldError = (field: string): string | undefined => {
    const info = fieldErrors[field];
    if (!info) return undefined;
    if (info.kind === 'conflict') return t('contract.errors.numberTaken');
    if (info.kind === 'forbidden') return t('contract.errors.forbidden');
    return info.message;
  };

  const fail = (error: unknown) => {
    if (error instanceof ApiError && error.status === 412) {
      setStale(true);
      return;
    }
    const fields: Record<string, FieldErrorInfo> = {};
    if (error instanceof ApiError) {
      for (const [field, info] of Object.entries(error.fieldErrors())) {
        fields[fieldKey(field)] = info;
      }
    }
    setFieldErrors(fields);
    setNotice(refusal(error, t, fields));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setNotice(undefined);
    setFieldErrors({});
    setStale(false);
    const values = valuesOf(form);
    if (editing) {
      const before = valuesOf(formOf(editing, ''));
      const changed: Record<string, unknown> = {};
      for (const key of Object.keys(values) as (keyof typeof values)[]) {
        if (values[key] !== before[key]) changed[key] = values[key];
      }
      // currency cannot be cleared: empty keeps the stored currency.
      if (changed.currency === null) delete changed.currency;
      if (Object.keys(changed).length === 0) {
        onClose();
        return;
      }
      update.mutate(
        { id: editing.id, data: changed as ContractUpdateRequest, version: editing.version },
        { onSuccess: onClose, onError: fail },
      );
      return;
    }
    const data: ContractCreateRequest = { number: values.number, status: values.status };
    for (const [key, value] of Object.entries(values)) {
      if (value !== null && key !== 'number' && key !== 'status') {
        (data as Record<string, unknown>)[key] = value;
      }
    }
    if (form.client_id) data.client_id = form.client_id;
    create.mutate(data, { onSuccess: onClose, onError: fail });
  };

  return (
    <Modal
      open
      size="lg"
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={editing ? t('contract.editTitle') : t('contract.createTitle')}
    >
      <form className="space-y-4" onSubmit={submit} noValidate>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Input
            label={t('contract.fields.number')}
            value={form.number}
            required
            onChange={(e) => set('number')(e.target.value)}
            error={fieldError('number')}
          />
          <Select
            label={t('contract.fields.kind')}
            value={form.kind}
            onChange={(e) => set('kind')(e.target.value)}
            error={fieldError('kind')}
            options={[
              { value: '', label: t('contract.none') },
              ...CONTRACT_KINDS.map((value) => ({ value, label: t(`contract.kinds.${value}`) })),
            ]}
          />
          <div>
            <Select
              label={t('contract.fields.client')}
              value={form.client_id}
              disabled={!!editing}
              onChange={(e) => set('client_id')(e.target.value)}
              error={fieldError('client_id')}
              options={[
                { value: '', label: t('contract.orgWide') },
                ...(clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name })),
                ...(form.client_id &&
                !(clients.data?.data ?? []).some((c) => c.id === form.client_id)
                  ? [{ value: form.client_id, label: form.client_id }]
                  : []),
              ]}
            />
            {editing ? (
              <p className="mt-1 text-sm text-fg-muted">{t('contract.clientFixed')}</p>
            ) : null}
          </div>
          <div>
            <Select
              label={t('contract.fields.supplier')}
              value={form.supplier_id}
              onChange={(e) => set('supplier_id')(e.target.value)}
              error={fieldError('supplier_id')}
              options={[
                { value: '', label: t('contract.none') },
                ...supplierOptions.map((s) => ({ value: s.id, label: s.name })),
              ]}
            />
            <p className="mt-1 text-sm text-fg-muted">{t('contract.supplierHint')}</p>
          </div>
          <Input
            type="date"
            label={t('contract.fields.startDate')}
            value={form.start_date}
            onChange={(e) => set('start_date')(e.target.value)}
            error={fieldError('start_date')}
          />
          <Input
            type="date"
            label={t('contract.fields.endDate')}
            value={form.end_date}
            onChange={(e) => set('end_date')(e.target.value)}
            error={fieldError('end_date')}
          />
          <Input
            type="number"
            min={0}
            label={t('contract.fields.noticePeriodDays')}
            value={form.notice_period_days}
            onChange={(e) => set('notice_period_days')(e.target.value)}
            error={fieldError('notice_period_days')}
          />
          <Select
            label={t('contract.fields.status')}
            value={form.status}
            onChange={(e) => set('status')(e.target.value)}
            error={fieldError('status')}
            options={CONTRACT_STATUSES.map((value) => ({
              value,
              label: t(`contract.statuses.${value}`),
            }))}
          />
          <Input
            type="number"
            step="0.01"
            label={t('contract.fields.value')}
            value={form.value}
            onChange={(e) => set('value')(e.target.value)}
            error={fieldError('value')}
          />
          <div>
            <Input
              label={t('contract.fields.currency')}
              value={form.currency}
              maxLength={3}
              placeholder="EUR"
              onChange={(e) => set('currency')(e.target.value)}
              error={fieldError('currency')}
              className="uppercase"
            />
            <p className="mt-1 text-sm text-fg-muted">{t('contract.currencyHint')}</p>
          </div>
        </div>
        <Textarea
          label={t('contract.fields.notes')}
          value={form.notes}
          rows={3}
          onChange={(e) => set('notes')(e.target.value)}
          error={fieldError('notes')}
        />
        {stale && editing ? (
          <div
            role="alert"
            className="flex flex-wrap items-center gap-3 rounded-lg border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-fg dark:border-amber-400/30 dark:bg-amber-500/10"
          >
            <span className="min-w-0 flex-1">{t('contract.errors.stale')}</span>
            <Button
              type="button"
              size="sm"
              variant="secondary"
              loading={reloading}
              onClick={() => {
                setReloading(true);
                void onReload(editing.id).catch((error: unknown) => {
                  setReloading(false);
                  setStale(false);
                  fail(error);
                });
              }}
            >
              {t('contract.errors.reload')}
            </Button>
          </div>
        ) : null}
        {notice ? (
          <p role="alert" className="text-sm text-danger">
            {notice}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={saving}>
            {editing ? t('common.save') : t('common.create')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** DeleteContractDialog confirms the deletion (DELETE /contracts/{id}). */
function DeleteContractDialog({
  contract,
  onClose,
  onDeleted,
}: {
  contract: Contract | null;
  onClose: () => void;
  onDeleted?: () => void;
}) {
  const { t } = useTranslation();
  const remove = useDeleteContract();
  const close = () => {
    remove.reset();
    onClose();
  };
  return (
    <Modal
      open={!!contract}
      onOpenChange={(open) => (open ? undefined : close())}
      title={t('contract.deleteTitle')}
      description={contract ? t('contract.deleteConfirm', { name: contract.number }) : undefined}
    >
      {remove.error ? (
        <p role="alert" className="mb-3 text-sm text-danger">
          {refusal(remove.error, t, {})}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={close}>
          {t('common.cancel')}
        </Button>
        <Button
          type="button"
          variant="danger"
          loading={remove.isPending}
          onClick={() =>
            contract &&
            remove.mutate(contract.id, {
              onSuccess: () => {
                close();
                onDeleted?.();
              },
            })
          }
        >
          {t('common.delete')}
        </Button>
      </div>
    </Modal>
  );
}
