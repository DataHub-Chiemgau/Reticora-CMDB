import { useState } from 'react';
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
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';

function statusVariant(status: string) {
  if (['succeeded', 'approved', 'resolved', 'active'].includes(status)) return 'success';
  if (['failed', 'rejected', 'open'].includes(status)) return 'danger';
  return 'warning';
}

export function IGAPage() {
  const { t } = useTranslation();
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
  if (loading) return <SkeletonList rows={6} label={t('iga.loading')} />;
  if (firstError?.isError) {
    return (
      <ErrorState
        title={t('iga.errorTitle')}
        description={firstError.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => connectors.refetch()}
      />
    );
  }

  const submit = () => {
    if (!form.name) return;
    createConnector.mutate(form, { onSuccess: () => setForm({ ...form, name: '', base_url: '' }) });
  };

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">{t('iga.title')}</h1>
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('iga.subtitle')}</p>
      </div>
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
          <Button onClick={submit} disabled={!form.name || createConnector.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </Card>
      <Card title={`${t('iga.connectors')} (${connectors.data?.total ?? 0})`}>
        {connectors.data?.data.length ? (
          <div className="space-y-3">
            {connectors.data.data.map((item) => (
              <div
                key={item.id}
                className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-gray-200 p-3 dark:border-gray-800"
              >
                <div>
                  <p className="font-medium">{item.name}</p>
                  <p className="text-sm text-gray-500">{item.type}</p>
                </div>
                <Badge variant={statusVariant(item.status)}>{item.status}</Badge>
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => testConnector.mutate(item.id)}
                  >
                    {t('iga.test')}
                  </Button>
                  <Button size="sm" onClick={() => syncConnector.mutate(item.id)}>
                    {t('iga.sync')}
                  </Button>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <EmptyState title={t('iga.emptyConnectors')} description={t('iga.emptyConnectorsHint')} />
        )}
      </Card>
      <div className="grid gap-6 xl:grid-cols-2">
        <Card title={t('iga.tasks')}>
          {tasks.data?.data.length ? (
            <ul className="space-y-2">
              {tasks.data.data.map((task) => (
                <li
                  key={task.id}
                  className="flex items-center justify-between rounded-lg border border-gray-200 p-3 text-sm dark:border-gray-800"
                >
                  <span>{task.action}</span>
                  <Badge variant={statusVariant(task.status)}>{task.status}</Badge>
                  <Button size="sm" variant="secondary" onClick={() => retryTask.mutate(task.id)}>
                    {t('iga.retry')}
                  </Button>
                </li>
              ))}
            </ul>
          ) : (
            <EmptyState title={t('iga.emptyTasks')} description={t('iga.emptyTasksHint')} />
          )}
        </Card>
        <Card title={t('iga.accessRequests')}>
          {requests.data?.data.length ? (
            <ul className="space-y-2">
              {requests.data.data.map((request) => (
                <li
                  key={request.id}
                  className="flex items-center justify-between rounded-lg border border-gray-200 p-3 text-sm dark:border-gray-800"
                >
                  <span>{request.entitlement}</span>
                  <Badge variant={statusVariant(request.status)}>{request.status}</Badge>
                  <div className="flex gap-2">
                    <Button size="sm" onClick={() => approve.mutate(request.id)}>
                      {t('iga.approve')}
                    </Button>
                    <Button size="sm" variant="secondary" onClick={() => reject.mutate(request.id)}>
                      {t('iga.reject')}
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          ) : (
            <EmptyState title={t('iga.emptyRequests')} description={t('iga.emptyRequestsHint')} />
          )}
        </Card>
      </div>
      <div className="grid gap-6 xl:grid-cols-2">
        <Card title={t('iga.reviews')}>
          {reviews.data?.data.length ? (
            <ul className="space-y-2">
              {reviews.data.data.map((review) => (
                <li
                  key={review.id}
                  className="rounded-lg border border-gray-200 p-3 text-sm dark:border-gray-800"
                >
                  <span className="font-medium">{review.name}</span>{' '}
                  <Badge variant={statusVariant(review.status)}>{review.status}</Badge>
                </li>
              ))}
            </ul>
          ) : (
            <EmptyState title={t('iga.emptyReviews')} description={t('iga.emptyReviewsHint')} />
          )}
        </Card>
        <Card title={t('iga.drift')}>
          {drift.data?.data.length ? (
            <ul className="space-y-2">
              {drift.data.data.map((finding) => (
                <li
                  key={finding.id}
                  className="flex items-center justify-between rounded-lg border border-gray-200 p-3 text-sm dark:border-gray-800"
                >
                  <span>{finding.drift_type}</span>
                  <Badge variant={statusVariant(finding.status)}>{finding.severity}</Badge>
                  <Button size="sm" onClick={() => remediate.mutate(finding.id)}>
                    {t('iga.remediate')}
                  </Button>
                </li>
              ))}
            </ul>
          ) : (
            <EmptyState title={t('iga.emptyDrift')} description={t('iga.emptyDriftHint')} />
          )}
        </Card>
      </div>
    </div>
  );
}
