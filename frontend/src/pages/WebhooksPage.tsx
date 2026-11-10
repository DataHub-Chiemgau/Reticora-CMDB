import { Inbox, Plus, RotateCw, Webhook } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import type { components } from '../api/generated/schema';
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
import { AdminSection } from '../components/admin/AdminSection';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
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
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';

type WebhookEvent = components['schemas']['WebhookEvent'];

/**
 * eventOrigin sorts every name of the OpenAPI enum WebhookEvent, which
 * derives from the event register (webhook.ValidEvents,
 * backend/internal/events/types.go): `register` are the subscribable types
 * of the register (EVT-02, NTF-05 without webhook.dead, EVT-03 P2), `former`
 * the former names (legacyNames) and `deprecated` the names without an
 * event contract (deprecatedEvents). The Record names each enum value
 * exactly once, so a new type of the register fails the typecheck until it
 * is sorted here (WP-707).
 */
const eventOrigin: Record<WebhookEvent, 'register' | 'former' | 'deprecated'> = {
  'ci.created': 'register',
  'ci.changed': 'register',
  'ci.deleted': 'register',
  'ci.restored': 'register',
  'ci.merged': 'register',
  'ci.offline': 'register',
  'ci.online': 'register',
  'relationship.created': 'register',
  'relationship.removed': 'register',
  'collector.enrolled': 'register',
  'collector.offline': 'register',
  'review.created': 'register',
  'review.resolved': 'register',
  'export.completed': 'register',
  'job.failed': 'register',
  'entitlement.warning': 'register',
  'entitlement.expired': 'register',
  'invitation.created': 'register',
  'certificate.expiring': 'register',
  'ci.status_changed': 'register',
  'lifecycle.changed': 'register',
  'relationship.changed': 'register',
  'asset.created': 'register',
  'asset.changed': 'register',
  'asset.deleted': 'register',
  'asset.assigned': 'register',
  'asset.returned': 'register',
  'asset.warranty_expiring': 'register',
  'contract.expiring': 'register',
  'stock.movement': 'register',
  'stock.below_minimum': 'register',
  'reservation.created': 'register',
  'reservation.released': 'register',
  'reservation.expired': 'register',
  'reservation.converted': 'register',
  'ticket.updated': 'register',
  'ticket.sla_breached': 'register',
  'metamodel.published': 'register',
  'document.scanned': 'register',
  'import.completed': 'register',
  'probe.state_changed': 'register',
  'alarm.opened': 'register',
  'alarm.acknowledged': 'register',
  'alarm.escalated': 'register',
  'alarm.severity_changed': 'register',
  'alarm.resolved': 'register',
  'incident.opened': 'register',
  'incident.updated': 'register',
  'incident.resolved': 'register',
  'collector_group.unreachable': 'register',
  'collector_group.reachable': 'register',
  'collector_group.failover': 'register',
  'collector.clock_skew': 'register',
  'config.changed': 'register',
  'ci.config_changed': 'register',
  'monitoring.suspension_changed': 'register',
  'capacity.forecast_exceeded': 'register',
  'sla.violated': 'register',
  'maintenance.scheduled': 'register',
  'maintenance.started': 'register',
  'maintenance.ended': 'register',
  'workflow.run_started': 'register',
  'workflow.run_succeeded': 'register',
  'workflow.run_failed': 'register',
  'workflow.run_cancelled': 'register',
  'workflow.approval_requested': 'register',
  'workflow.approval_decided': 'register',
  'workflow.webhook': 'register',
  'finding.created': 'register',
  'finding.changed': 'register',
  'finding.resolved': 'register',
  'notification.escalated': 'register',
  'notification.acknowledged': 'register',
  'entitlement.changed': 'register',
  'org.export_completed': 'register',
  'org.deletion_requested': 'register',
  'org.deletion_cancelled': 'register',
  'ci.updated': 'former',
  'relationship.deleted': 'former',
  'lifecycle.transitioned': 'former',
  'inventory.movement': 'former',
  'asset.movement': 'former',
  'ci_type.created': 'deprecated',
  'ci_type.updated': 'deprecated',
  'ci_type.deactivated': 'deprecated',
  'ci_attribute.changed': 'deprecated',
  'composition.created': 'deprecated',
  'composition.updated': 'deprecated',
  'composition.deleted': 'deprecated',
  'override.changed': 'deprecated',
  'reconciliation.policy_changed': 'deprecated',
  'discovery.completed': 'deprecated',
};

/**
 * availableEvents are the types a new subscription can choose, in the order
 * of the register; former and deprecated names stay valid for existing
 * subscriptions, whose list shows them unchanged, but are not offered.
 */
const availableEvents = (Object.keys(eventOrigin) as WebhookEvent[]).filter(
  (event) => eventOrigin[event] === 'register',
);

/** eventGroups sorts the offered events by their resource (the part before the dot). */
const eventGroups = availableEvents.reduce<{ group: string; events: WebhookEvent[] }[]>(
  (groups, event) => {
    const group = event.split('.')[0]!;
    const existing = groups.find((g) => g.group === group);
    if (existing) existing.events.push(event);
    else groups.push({ group, events: [event] });
    return groups;
  },
  [],
);

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
  // The webhook whose deletion awaits confirmation: it cannot be undone.
  const [pendingDelete, setPendingDelete] = useState<{
    id: string;
    name: string;
    url: string;
  } | null>(null);

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
    setPendingDelete(null);
  };
  const runRetry = (id: string, deliveryId: string) => {
    setActionError(null);
    retryDelivery.mutate({ id, deliveryId }, actionCallbacks);
  };

  const header = <PageHeader title={t('webhook.title')} description={t('webhook.subtitle')} />;

  if (subscriptions.isLoading || deadLetters.isLoading) {
    return (
      <div className="space-y-6">
        {header}
        <SkeletonList rows={6} label={t('webhook.loading')} />
      </div>
    );
  }

  if (subscriptions.isError || deadLetters.isError) {
    const failed = subscriptions.isError ? subscriptions : deadLetters;
    return (
      <div className="space-y-6">
        {header}
        <ErrorState
          title={t('webhook.errorTitle')}
          description={failed.error?.message}
          retryLabel={t('common.retry')}
          onRetry={() => failed.refetch()}
        />
      </div>
    );
  }

  const formComplete = form.name && form.url && form.secret && form.events.length > 0;

  return (
    <div className="space-y-8">
      {header}

      {canManage ? (
        <AdminSection title={t('webhook.createSubscription')} description={t('webhook.createHint')}>
          <Card>
            <div className="space-y-5">
              {createError ? (
                <p role="alert" className="text-sm text-danger">
                  {createError.message}
                </p>
              ) : null}
              <div className="grid gap-4 md:grid-cols-3 md:items-start">
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
                  className="font-mono text-xs"
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
              <p className="-mt-2 text-xs text-fg-muted">{t('webhook.secretWriteOnly')}</p>
              <fieldset>
                <legend className="mb-3 text-sm font-medium text-fg">{t('webhook.events')}</legend>
                <div className="columns-1 gap-x-6 sm:columns-2 lg:columns-3 xl:columns-4">
                  {eventGroups.map(({ group, events }) => (
                    <div
                      key={group}
                      role="group"
                      aria-label={group}
                      className="mb-4 break-inside-avoid space-y-1.5"
                    >
                      <p aria-hidden="true" className="text-xs font-medium text-fg-muted">
                        {group}
                      </p>
                      {events.map((event) => (
                        <label
                          key={event}
                          className="flex min-h-6 items-center gap-2 py-1 font-mono text-xs text-fg"
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
                  ))}
                </div>
                {createError?.fields.events ? (
                  <p className="mt-2 text-sm text-danger">{createError.fields.events}</p>
                ) : null}
              </fieldset>
              <div className="flex justify-end border-t border-line pt-4">
                <Button
                  icon={<Plus />}
                  onClick={submit}
                  loading={createWebhook.isPending}
                  disabled={!formComplete}
                >
                  {t('common.save')}
                </Button>
              </div>
            </div>
          </Card>
        </AdminSection>
      ) : managePermission === false ? (
        <p className="text-sm text-fg-muted">{t('webhook.readOnly')}</p>
      ) : null}

      <AdminSection
        title={`${t('webhook.subscriptions')} (${subscriptions.data?.total ?? 0})`}
        description={t('webhook.subscriptionsHint')}
      >
        {actionError ? (
          <p role="alert" className="text-sm text-danger">
            {actionError}
          </p>
        ) : null}
        {subscriptions.data?.data.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('webhook.subscriptions')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr className="text-fg-muted">
                    <th scope="col" className={thClasses}>
                      {t('webhook.name')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('webhook.url')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('webhook.events')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('webhook.active')}
                    </th>
                    <th scope="col" className={cn(thClasses, 'text-right')}>
                      {t('common.actions')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {subscriptions.data.data.map((sub) => (
                    <tr key={sub.id} className={trClasses}>
                      <td className={tdPrimaryClasses}>{sub.name}</td>
                      <td className={cn(tdMonoClasses, 'max-w-[18rem] break-all')}>{sub.url}</td>
                      <td className={cn(tdClasses, 'max-w-[18rem] font-mono text-xs')}>
                        {sub.events.join(', ')}
                      </td>
                      <td className={tdClasses}>
                        <Badge variant={sub.is_active ? 'success' : 'neutral'}>
                          {sub.is_active ? t('webhook.active') : t('webhook.inactive')}
                        </Badge>
                      </td>
                      <td className={cn(tdClasses, 'text-right')}>
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="sm"
                            aria-expanded={selectedId === sub.id}
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
                                className="text-danger hover:text-danger"
                                onClick={() =>
                                  setPendingDelete({ id: sub.id, name: sub.name, url: sub.url })
                                }
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
            </ScrollRegion>
          </Card>
        ) : (
          <EmptyState
            icon={<Webhook />}
            title={t('webhook.emptyTitle')}
            description={t('webhook.emptyHint')}
          />
        )}
      </AdminSection>

      {selectedId ? (
        <AdminSection
          title={`${t('webhook.deliveries')} (${deliveries.data?.total ?? 0})`}
          description={t('webhook.deliveriesHint')}
        >
          {deliveries.isLoading ? (
            <SkeletonList rows={3} label={t('webhook.loadingDeliveries')} />
          ) : deliveries.data?.data.length ? (
            <Card className="overflow-hidden p-0">
              <ul className="divide-y divide-line">
                {deliveries.data.data.map((delivery) => (
                  <li key={delivery.id} className="px-4 py-3 text-sm">
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                      <span className="font-mono text-xs font-medium text-fg">
                        {delivery.event}
                      </span>
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
                      </Badge>
                      <span className="text-fg-muted tabular-nums">
                        {t('webhook.attemptOf', {
                          attempt: delivery.attempt,
                          max: delivery.max_attempts,
                        })}
                      </span>
                      {canManage && (delivery.status === 'failed' || delivery.status === 'dead') ? (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="ml-auto"
                          icon={<RotateCw />}
                          onClick={() => selectedId && runRetry(selectedId, delivery.id)}
                          disabled={retryDelivery.isPending}
                        >
                          {t('webhook.retry')}
                        </Button>
                      ) : null}
                    </div>
                    {delivery.error ? (
                      <p className="mt-1 text-xs text-danger">{delivery.error}</p>
                    ) : null}
                  </li>
                ))}
              </ul>
            </Card>
          ) : (
            <EmptyState
              icon={<Inbox />}
              title={t('webhook.noDeliveries')}
              description={t('webhook.noDeliveriesHint')}
            />
          )}
        </AdminSection>
      ) : null}

      <AdminSection
        title={`${t('webhook.deadLetters')} (${deadLetters.data?.total ?? 0})`}
        description={t('webhook.deadLettersHint')}
      >
        {deadLetters.data?.data.length ? (
          <Card className="overflow-hidden p-0">
            <ul className="divide-y divide-line">
              {deadLetters.data.data.map((letter) => (
                <li key={letter.id} className="px-4 py-3 text-sm">
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                    <span className="font-mono text-xs font-medium text-fg">{letter.event}</span>
                    <Badge variant="danger">{t('webhook.dead')}</Badge>
                    <span className="text-fg-muted tabular-nums">
                      {t('webhook.attempts', { count: letter.attempts })}
                    </span>
                  </div>
                  {letter.last_error ? (
                    <p className="mt-1 text-xs text-danger">{letter.last_error}</p>
                  ) : null}
                </li>
              ))}
            </ul>
          </Card>
        ) : (
          <EmptyState
            icon={<Inbox />}
            title={t('webhook.noDeadLetters')}
            description={t('webhook.noDeadLettersHint')}
          />
        )}
      </AdminSection>

      <Modal
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null);
        }}
        title={t('webhook.deleteTitle')}
        description={
          pendingDelete
            ? t('webhook.deleteText', { name: pendingDelete.name, url: pendingDelete.url })
            : undefined
        }
        size="sm"
      >
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => setPendingDelete(null)}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="danger"
            loading={deleteWebhook.isPending}
            onClick={() => pendingDelete && runDelete(pendingDelete.id)}
          >
            {t('common.delete')}
          </Button>
        </div>
      </Modal>
    </div>
  );
}
