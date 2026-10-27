import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ApiError } from '../api/client';
import {
  useCreateWebhook,
  useDeleteWebhook,
  useHasPermission,
  useRetryWebhookDelivery,
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

type FormField = 'name' | 'url' | 'secret' | 'events';

const formFields: readonly FormField[] = ['name', 'url', 'secret', 'events'];

/** A refusal of the server as the page shows it: a summary and per-field messages. */
interface RefusalView {
  message: string;
  fields: Partial<Record<FormField, string>>;
}

// The egress check (SEC-08) refuses a destination with this prefix and the
// reason after it, e.g. "egress: destination is not allowed: 127.0.0.1".
const egressRefusal = /^egress: destination is not allowed:?\s*(.*)$/i;
const invalidEvent = /^invalid event:\s*(.+)$/i;

/**
 * describeRefusal turns a failed webhook request into a localized message
 * (F-062): a refused destination names the reason at the URL field, a
 * validation error goes to its field, a 403 says that the right is missing.
 * The user sees why nothing was saved instead of an unchanged form.
 */
function describeRefusal(error: unknown, t: TFunction): RefusalView {
  if (!(error instanceof ApiError)) {
    const detail = error instanceof Error ? error.message : String(error);
    return { message: t('webhook.errors.failed', { detail }), fields: {} };
  }
  if (error.status === 403) {
    return { message: t('webhook.errors.forbidden'), fields: {} };
  }
  if (error.status === 404) {
    return { message: t('webhook.errors.notFound'), fields: {} };
  }
  if (error.status !== 400 && error.status !== 422) {
    return { message: t('webhook.errors.failed', { detail: error.message }), fields: {} };
  }

  const detail = error.detail ?? error.message;
  const egress = egressRefusal.exec(detail);
  if (egress || detail.toLowerCase().startsWith('egress:')) {
    const reason = egress?.[1] || detail;
    return {
      message: t('webhook.errors.notSaved'),
      fields: { url: t('webhook.errors.egress', { reason }) },
    };
  }
  const event = invalidEvent.exec(detail);
  if (event) {
    return {
      message: t('webhook.errors.notSaved'),
      fields: { events: t('webhook.errors.invalidEvent', { event: event[1] }) },
    };
  }
  if (error.hasViolations) {
    const fields: Partial<Record<FormField, string>> = {};
    const others: string[] = [];
    for (const [field, message] of Object.entries(error.violationsByField())) {
      if ((formFields as readonly string[]).includes(field)) {
        fields[field as FormField] = t('webhook.errors.invalidValue', { detail: message || field });
      } else {
        others.push(`${field}: ${message}`);
      }
    }
    return {
      message: others.length
        ? t('webhook.errors.rejected', { detail: others.join('; ') })
        : t('webhook.errors.notSaved'),
      fields,
    };
  }
  return { message: t('webhook.errors.rejected', { detail }), fields: {} };
}

export function WebhooksPage() {
  const { t } = useTranslation();
  const subscriptions = useWebhookList();
  const deadLetters = useWebhookDeadLetters();
  const createWebhook = useCreateWebhook();
  const deleteWebhook = useDeleteWebhook();
  const testWebhook = useTestWebhook();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const deliveries = useWebhookDeliveries(selectedId);
  const retryDelivery = useRetryWebhookDelivery();
  // Creating, deleting, testing and retrying need webhook:manage (RBA-02);
  // without it the page stays a read-only list (F-062). Unknown rights
  // (still loading) offer no action either.
  const managePermission = useHasPermission('webhook:manage');
  const canManage = managePermission === true;
  const [form, setForm] = useState({ name: '', url: '', secret: '', events: [] as string[] });
  const [createError, setCreateError] = useState<RefusalView | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

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
    setCreateError(null);
    // On a refusal the inputs stay as they are, so the user can correct them.
    createWebhook.mutate(form, {
      onSuccess: () => setForm({ name: '', url: '', secret: '', events: [] }),
      onError: (error) => setCreateError(describeRefusal(error, t)),
    });
  };

  const actionCallbacks = {
    onSuccess: () => setActionError(null),
    onError: (error: unknown) => setActionError(describeRefusal(error, t).message),
  };
  const runTest = (id: string) => {
    setActionError(null);
    testWebhook.mutate(id, actionCallbacks);
  };
  const runDelete = (id: string) => {
    setActionError(null);
    deleteWebhook.mutate(id, actionCallbacks);
  };
  const runRetry = (id: string, deliveryId: string) => {
    setActionError(null);
    retryDelivery.mutate({ id, deliveryId }, actionCallbacks);
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

      {canManage ? (
        <Card title={t('webhook.createSubscription')}>
          <div className="space-y-4">
            {createError ? (
              <p role="alert" className="text-sm text-red-600 dark:text-red-400">
                {createError.message}
              </p>
            ) : null}
            <div className="grid gap-4 md:grid-cols-3 md:items-end">
              <Input
                label={t('webhook.name')}
                value={form.name}
                error={createError?.fields.name}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
              />
              <Input
                label={t('webhook.url')}
                type="url"
                value={form.url}
                error={createError?.fields.url}
                onChange={(event) => setForm({ ...form, url: event.target.value })}
              />
              <Input
                label={t('webhook.secret')}
                type="password"
                value={form.secret}
                error={createError?.fields.secret}
                onChange={(event) => setForm({ ...form, secret: event.target.value })}
              />
            </div>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              {t('webhook.secretWriteOnly')}
            </p>
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
              {createError?.fields.events ? (
                <p className="mt-1 text-sm text-red-600 dark:text-red-400">
                  {createError.fields.events}
                </p>
              ) : null}
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
      ) : managePermission === false ? (
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('webhook.readOnly')}</p>
      ) : null}

      <Card title={`${t('webhook.subscriptions')} (${subscriptions.data?.total ?? 0})`}>
        {actionError ? (
          <p role="alert" className="mb-3 text-sm text-red-600 dark:text-red-400">
            {actionError}
          </p>
        ) : null}
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
                        {canManage ? (
                          <>
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => runTest(sub.id)}
                              disabled={testWebhook.isPending}
                            >
                              {t('webhook.test')}
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => runDelete(sub.id)}
                              disabled={deleteWebhook.isPending}
                            >
                              {t('common.delete')}
                            </Button>
                          </>
                        ) : null}
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
                      delivery.status === 'delivered'
                        ? 'success'
                        : delivery.status === 'dead'
                          ? 'danger'
                          : delivery.status === 'failed'
                            ? 'warning'
                            : 'info'
                    }
                  >
                    {t(`webhook.status.${delivery.status}`, delivery.status)}
                  </Badge>{' '}
                  <span className="text-gray-500 dark:text-gray-400">
                    {t('webhook.attemptOf', {
                      attempt: delivery.attempt,
                      max: delivery.max_attempts,
                    })}
                  </span>
                  {canManage && (delivery.status === 'failed' || delivery.status === 'dead') ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => selectedId && runRetry(selectedId, delivery.id)}
                      disabled={retryDelivery.isPending}
                    >
                      {t('webhook.retry')}
                    </Button>
                  ) : null}
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
