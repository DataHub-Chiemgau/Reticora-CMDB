import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  useCreateWebhook,
  useDeleteWebhook,
  useTestWebhook,
  useWebhookDeadLetters,
  useWebhookDeliveries,
  useWebhookList,
} from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { SkeletonList } from '../components/ui/Skeleton';

const availableEvents = [
  'ci.created',
  'ci.updated',
  'ci.deleted',
  'ci.status_changed',
  'relationship.created',
  'relationship.deleted',
  'discovery.completed',
];

export function WebhooksPage() {
  const { t } = useTranslation();
  const subscriptions = useWebhookList();
  const deadLetters = useWebhookDeadLetters();
  const createWebhook = useCreateWebhook();
  const deleteWebhook = useDeleteWebhook();
  const testWebhook = useTestWebhook();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const deliveries = useWebhookDeliveries(selectedId);
  const [form, setForm] = useState({ name: '', url: '', secret: '', events: [] as string[] });

  const toggleEvent = (event: string) => {
    setForm((current) => ({
      ...current,
      events: current.events.includes(event)
        ? current.events.filter((e) => e !== event)
        : [...current.events, event],
    }));
  };

  const submit = () => {
    if (!form.name || !form.url || !form.secret || form.events.length === 0) return;
    createWebhook.mutate(form, {
      onSuccess: () => setForm({ name: '', url: '', secret: '', events: [] }),
    });
  };

  if (subscriptions.isLoading || deadLetters.isLoading) {
    return <SkeletonList rows={6} label={t('webhook.loading')} />;
  }

  if (subscriptions.isError) {
    return (
      <ErrorState
        title={t('webhook.errorTitle')}
        description={subscriptions.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => subscriptions.refetch()}
      />
    );
  }

  if (deadLetters.isError) {
    return (
      <ErrorState
        title={t('webhook.errorTitle')}
        description={deadLetters.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => deadLetters.refetch()}
      />
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">{t('webhook.title')}</h1>
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('webhook.subtitle')}</p>
      </div>

      <Card title={t('webhook.createSubscription')}>
        <div className="space-y-4">
          <div className="grid gap-4 md:grid-cols-3 md:items-end">
            <Input
              label={t('webhook.name')}
              value={form.name}
              onChange={(event) => setForm({ ...form, name: event.target.value })}
            />
            <Input
              label={t('webhook.url')}
              type="url"
              value={form.url}
              onChange={(event) => setForm({ ...form, url: event.target.value })}
            />
            <Input
              label={t('webhook.secret')}
              type="password"
              value={form.secret}
              onChange={(event) => setForm({ ...form, secret: event.target.value })}
            />
          </div>
          <p className="text-xs text-gray-500 dark:text-gray-400">{t('webhook.secretWriteOnly')}</p>
          <fieldset>
            <legend className="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
              {t('webhook.events')}
            </legend>
            <div className="flex flex-wrap gap-3">
              {availableEvents.map((event) => (
                <label
                  key={event}
                  className="flex items-center gap-1 text-sm text-gray-700 dark:text-gray-300"
                >
                  <input
                    type="checkbox"
                    checked={form.events.includes(event)}
                    onChange={() => toggleEvent(event)}
                  />
                  {event}
                </label>
              ))}
            </div>
          </fieldset>
          <Button
            onClick={submit}
            disabled={
              createWebhook.isPending ||
              !form.name ||
              !form.url ||
              !form.secret ||
              form.events.length === 0
            }
          >
            {t('common.save')}
          </Button>
        </div>
      </Card>

      <Card title={`${t('webhook.subscriptions')} (${subscriptions.data?.total ?? 0})`}>
        {subscriptions.data?.data.length ? (
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th className="pb-2">{t('webhook.name')}</th>
                  <th className="pb-2">{t('webhook.url')}</th>
                  <th className="pb-2">{t('webhook.events')}</th>
                  <th className="pb-2">{t('webhook.active')}</th>
                  <th className="pb-2">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {subscriptions.data.data.map((sub) => (
                  <tr key={sub.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2 font-medium">{sub.name}</td>
                    <td className="py-2 font-mono text-xs">{sub.url}</td>
                    <td className="py-2">{sub.events.join(', ')}</td>
                    <td className="py-2">
                      <Badge variant={sub.is_active ? 'success' : 'neutral'}>
                        {sub.is_active ? t('webhook.active') : t('webhook.inactive')}
                      </Badge>
                    </td>
                    <td className="py-2">
                      <div className="flex gap-2">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setSelectedId(selectedId === sub.id ? null : sub.id)}
                        >
                          {t('webhook.deliveries')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => testWebhook.mutate(sub.id)}
                          disabled={testWebhook.isPending}
                        >
                          {t('webhook.test')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => deleteWebhook.mutate(sub.id)}
                          disabled={deleteWebhook.isPending}
                        >
                          {t('common.delete')}
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState title={t('webhook.emptyTitle')} description={t('webhook.emptyHint')} />
        )}
      </Card>

      {selectedId ? (
        <Card title={`${t('webhook.deliveries')} (${deliveries.data?.total ?? 0})`}>
          {deliveries.isLoading ? (
            <SkeletonList rows={3} label={t('webhook.loadingDeliveries')} />
          ) : deliveries.data?.data.length ? (
            <ul className="space-y-2">
              {deliveries.data.data.map((delivery) => (
                <li
                  key={delivery.id}
                  className="rounded-lg border border-gray-200 p-3 text-sm dark:border-gray-800"
                >
                  <span className="font-medium">{delivery.event}</span>{' '}
                  <Badge
                    variant={
                      delivery.status === 'success'
                        ? 'success'
                        : delivery.status === 'dead' || delivery.status === 'failed'
                          ? 'danger'
                          : 'info'
                    }
                  >
                    {delivery.status}
                  </Badge>{' '}
                  <span className="text-gray-500 dark:text-gray-400">
                    {t('webhook.attemptOf', {
                      attempt: delivery.attempt,
                      max: delivery.max_attempts,
                    })}
                  </span>
                  {delivery.error ? (
                    <p className="mt-1 text-xs text-red-600 dark:text-red-400">{delivery.error}</p>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : (
            <EmptyState
              title={t('webhook.noDeliveries')}
              description={t('webhook.noDeliveriesHint')}
            />
          )}
        </Card>
      ) : null}

      <Card title={`${t('webhook.deadLetters')} (${deadLetters.data?.total ?? 0})`}>
        {deadLetters.data?.data.length ? (
          <ul className="space-y-2">
            {deadLetters.data.data.map((letter) => (
              <li
                key={letter.id}
                className="rounded-lg border border-gray-200 p-3 text-sm dark:border-gray-800"
              >
                <span className="font-medium">{letter.event}</span>{' '}
                <Badge variant="danger">{t('webhook.dead')}</Badge>{' '}
                <span className="text-gray-500 dark:text-gray-400">
                  {t('webhook.attempts', { count: letter.attempts })}
                </span>
                {letter.last_error ? (
                  <p className="mt-1 text-xs text-red-600 dark:text-red-400">{letter.last_error}</p>
                ) : null}
              </li>
            ))}
          </ul>
        ) : (
          <EmptyState
            title={t('webhook.noDeadLetters')}
            description={t('webhook.noDeadLettersHint')}
          />
        )}
      </Card>
    </div>
  );
}
