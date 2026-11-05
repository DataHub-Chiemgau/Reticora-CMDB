import { ClipboardCheck, GitCompareArrows, KeyRound, ListTodo, PlugZap, Plus } from 'lucide-react';
import { useState } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import {
  useApproveIGAAccessRequest,
  useCreateIGAConnector,
  useIGAAccessRequests,
  useIGAConnectors,
  useIGADrift,
  useIGAReviews,
  useIGATasks,
  useRejectIGAAccessRequest,
  useRemediateIGADrift,
  useRetryIGATask,
  useSyncIGAConnector,
  useTestIGAConnector,
} from '../api/hooks';
import { ScrollRegion } from '../components/ScrollRegion';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';

function statusVariant(status: string) {
  if (['succeeded', 'approved', 'resolved', 'active'].includes(status)) return 'success';
  if (['failed', 'rejected', 'open'].includes(status)) return 'danger';
  return 'warning';
}

export function IGAPage() {
  const { t, i18n } = useTranslation();
  const connectors = useIGAConnectors();
  const tasks = useIGATasks();
  const requests = useIGAAccessRequests();
  const reviews = useIGAReviews();
  const drift = useIGADrift();
  const createConnector = useCreateIGAConnector();
  const testConnector = useTestIGAConnector();
  const syncConnector = useSyncIGAConnector();
  const retryTask = useRetryIGATask();
  const approve = useApproveIGAAccessRequest();
  const reject = useRejectIGAAccessRequest();
  const remediate = useRemediateIGADrift();
  const [form, setForm] = useState({ name: '', type: 'relay', base_url: '', collector_id: '' });

  const loading = [connectors, tasks, requests, reviews, drift].some((query) => query.isLoading);
  const firstError = [connectors, tasks, requests, reviews, drift].find((query) => query.isError);
  const header = <PageHeader title={t('iga.title')} description={t('iga.subtitle')} />;
  if (loading)
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={6} label={t('iga.loading')} />
      </div>
    );
  if (firstError?.isError) {
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('iga.errorTitle')}
          description={firstError.error.message}
          retryLabel={t('common.retry')}
          onRetry={() => connectors.refetch()}
        />
      </div>
    );
  }

  const submit = () => {
    if (!form.name) return;
    createConnector.mutate(form, { onSuccess: () => setForm({ ...form, name: '', base_url: '' }) });
  };

  return (
    <div className="space-y-6">
      {header}
      <Card title={t('iga.createConnector')}>
        <div className="grid gap-4 md:grid-cols-5 md:items-end">
          <Input
            label={t('iga.name')}
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
          <Select
            label={t('iga.type')}
            value={form.type}
            options={[
              { value: 'relay', label: 'Relay' },
              { value: 'scim', label: 'SCIM' },
            ]}
            onChange={(e) => setForm({ ...form, type: e.target.value })}
          />
          <Input
            label={t('iga.baseUrl')}
            value={form.base_url}
            onChange={(e) => setForm({ ...form, base_url: e.target.value })}
          />
          <Input
            label={t('iga.collectorId')}
            value={form.collector_id}
            onChange={(e) => setForm({ ...form, collector_id: e.target.value })}
          />
          <Button
            icon={<Plus />}
            onClick={submit}
            disabled={!form.name}
            loading={createConnector.isPending}
          >
            {t('common.save')}
          </Button>
        </div>
      </Card>

      <Section id="iga-connectors" title={t('iga.connectors')} count={connectors.data?.total ?? 0}>
        {connectors.data?.data.length ? (
          <DataTable
            label={t('iga.connectors')}
            columns={[t('iga.name'), t('iga.type'), t('iga.status')]}
            actions
          >
            {connectors.data.data.map((item) => (
              <tr key={item.id} className={trClasses}>
                <td className={tdPrimaryClasses}>{item.name}</td>
                <td className={tdMonoClasses}>{item.type}</td>
                <td className={tdClasses}>
                  <Badge variant={statusVariant(item.status)}>
                    {t(`iga.connectorStatuses.${item.status}`, item.status)}
                  </Badge>
                </td>
                <td className={tdClasses}>
                  <div className="flex items-center justify-end gap-1.5">
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => testConnector.mutate(item.id)}
                    >
                      {t('iga.test')}
                    </Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => syncConnector.mutate(item.id)}
                    >
                      {t('iga.sync')}
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </DataTable>
        ) : (
          <EmptyState
            icon={<PlugZap />}
            title={t('iga.emptyConnectors')}
            description={t('iga.emptyConnectorsHint')}
          />
        )}
      </Section>

      <div className="grid gap-6 xl:grid-cols-2">
        <Section id="iga-tasks" title={t('iga.tasks')}>
          {tasks.data?.data.length ? (
            <DataTable label={t('iga.tasks')} columns={[t('iga.action'), t('iga.status')]} actions>
              {tasks.data.data.map((task) => (
                <tr key={task.id} className={trClasses}>
                  <td className={tdMonoClasses}>{task.action}</td>
                  <td className={tdClasses}>
                    <Badge variant={statusVariant(task.status)}>
                      {t(`iga.taskStatuses.${task.status}`, task.status)}
                    </Badge>
                  </td>
                  <td className={tdClasses}>
                    <div className="flex justify-end">
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => retryTask.mutate(task.id)}
                      >
                        {t('iga.retry')}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </DataTable>
          ) : (
            <EmptyState
              icon={<ListTodo />}
              title={t('iga.emptyTasks')}
              description={t('iga.emptyTasksHint')}
            />
          )}
        </Section>
        <Section id="iga-requests" title={t('iga.accessRequests')}>
          {requests.data?.data.length ? (
            <DataTable
              label={t('iga.accessRequests')}
              columns={[t('iga.entitlement'), t('iga.status')]}
              actions
            >
              {requests.data.data.map((request) => (
                <tr key={request.id} className={trClasses}>
                  <td className={tdMonoClasses}>{request.entitlement}</td>
                  <td className={tdClasses}>
                    <Badge variant={statusVariant(request.status)}>
                      {t(`iga.requestStatuses.${request.status}`, request.status)}
                    </Badge>
                  </td>
                  <td className={tdClasses}>
                    <div className="flex items-center justify-end gap-1.5">
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => approve.mutate(request.id)}
                      >
                        {t('iga.approve')}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-danger"
                        onClick={() => reject.mutate(request.id)}
                      >
                        {t('iga.reject')}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </DataTable>
          ) : (
            <EmptyState
              icon={<KeyRound />}
              title={t('iga.emptyRequests')}
              description={t('iga.emptyRequestsHint')}
            />
          )}
        </Section>
      </div>
      <div className="grid gap-6 xl:grid-cols-2">
        <Section id="iga-reviews" title={t('iga.reviews')}>
          {reviews.data?.data.length ? (
            <DataTable
              label={t('iga.reviews')}
              columns={[t('iga.name'), t('iga.status'), t('iga.due')]}
            >
              {reviews.data.data.map((review) => (
                <tr key={review.id} className={trClasses}>
                  <td className={tdPrimaryClasses}>{review.name}</td>
                  <td className={tdClasses}>
                    <Badge variant={statusVariant(review.status)}>
                      {t(`iga.reviewStatuses.${review.status}`, review.status)}
                    </Badge>
                  </td>
                  <td className={`${tdClasses} whitespace-nowrap tabular-nums`}>
                    {review.due_at
                      ? new Date(review.due_at).toLocaleDateString(i18n.language)
                      : '—'}
                  </td>
                </tr>
              ))}
            </DataTable>
          ) : (
            <EmptyState
              icon={<ClipboardCheck />}
              title={t('iga.emptyReviews')}
              description={t('iga.emptyReviewsHint')}
            />
          )}
        </Section>
        <Section id="iga-drift" title={t('iga.drift')}>
          {drift.data?.data.length ? (
            <DataTable
              label={t('iga.drift')}
              columns={[t('iga.type'), t('compliance.severity')]}
              actions
            >
              {drift.data.data.map((finding) => (
                <tr key={finding.id} className={trClasses}>
                  <td className={tdMonoClasses}>{finding.drift_type}</td>
                  <td className={tdClasses}>
                    <Badge variant={statusVariant(finding.status)}>
                      {t(`iga.severities.${finding.severity}`, finding.severity)}
                    </Badge>
                  </td>
                  <td className={tdClasses}>
                    <div className="flex justify-end">
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => remediate.mutate(finding.id)}
                      >
                        {t('iga.remediate')}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </DataTable>
          ) : (
            <EmptyState
              icon={<GitCompareArrows />}
              title={t('iga.emptyDrift')}
              description={t('iga.emptyDriftHint')}
            />
          )}
        </Section>
      </div>
    </div>
  );
}

/** Section is a titled block of the page; its table or empty state sits below the h2. */
function Section({
  id,
  title,
  count,
  children,
}: {
  id: string;
  title: string;
  count?: number;
  children: ReactNode;
}) {
  return (
    <section className="min-w-0 space-y-3" aria-labelledby={id}>
      <h2 id={id} className="text-base font-semibold text-fg">
        {title}
        {count !== undefined ? (
          <span className="ml-1.5 tabular-nums text-fg-muted">({count})</span>
        ) : null}
      </h2>
      {children}
    </section>
  );
}

/** DataTable is the table of a section: a header row, the rows, an optional actions column. */
function DataTable({
  label,
  columns,
  actions = false,
  children,
}: {
  label: string;
  columns: string[];
  actions?: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <Card className="overflow-hidden p-0">
      <ScrollRegion label={label}>
        <table className={tableClasses}>
          <thead className={theadClasses}>
            <tr>
              {columns.map((column) => (
                <th key={column} scope="col" className={thClasses}>
                  {column}
                </th>
              ))}
              {actions ? (
                <th scope="col" className={`${thClasses} text-right`}>
                  {t('common.actions')}
                </th>
              ) : null}
            </tr>
          </thead>
          <tbody>{children}</tbody>
        </table>
      </ScrollRegion>
    </Card>
  );
}
