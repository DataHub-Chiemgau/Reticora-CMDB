import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ciApi, maintenanceApi } from '../api/client';
import type { MaintenanceNotification, MaintenanceWindow } from '../api/client';
import { clientApi } from '../api/cmdb';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

function statusVariant(status: string): 'neutral' | 'warning' | 'success' | 'danger' | 'info' {
  switch (status) {
    case 'scheduled':
      return 'warning';
    case 'in_progress':
      return 'info';
    case 'completed':
      return 'success';
    case 'cancelled':
      return 'danger';
    default:
      return 'neutral';
  }
}

function notificationVariant(status: string): 'neutral' | 'warning' | 'success' | 'danger' {
  switch (status) {
    case 'pending':
      return 'warning';
    case 'sent':
      return 'success';
    case 'failed':
      return 'danger';
    default:
      return 'neutral';
  }
}

const emptyForm = { title: '', starts_at: '', ends_at: '' };

/**
 * WindowNotifications lists the stored notifications of a window from
 * GET /maintenance-windows/{id}/notifications (WP-540, F-236): one row per
 * client and channel with the status the server keeps, so a pending
 * notification stays visible after a reload and is never shown as delivered.
 */
function WindowNotifications({ windowId }: { windowId: string }) {
  const { t } = useTranslation();
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['maintenance-notifications', windowId],
    queryFn: () => maintenanceApi.notifications(windowId),
  });
  const clients = useQuery({
    queryKey: ['scope', 'clients'],
    queryFn: () => clientApi.list(),
    retry: false,
    staleTime: 60_000,
  });
  const clientName = (id?: string) =>
    (id && clients.data?.data.find((c) => c.id === id)?.name) || id || '–';

  if (isLoading) {
    return <SkeletonList rows={2} label={t('app.loading')} />;
  }
  if (error) {
    return (
      <ErrorState
        title={t('app.error')}
        retryLabel={t('common.retry')}
        onRetry={() => void refetch()}
      />
    );
  }
  const rows: MaintenanceNotification[] = data?.data ?? [];
  if (rows.length === 0) {
    return (
      <p className="text-sm text-gray-500 dark:text-gray-400">
        {t('maintenance.noNotifications', 'Keine Benachrichtigungen gespeichert')}
      </p>
    );
  }
  return (
    <table className="w-full text-left text-sm">
      <caption className="sr-only">{t('maintenance.notifications', 'Benachrichtigungen')}</caption>
      <thead className="text-gray-500 dark:text-gray-400">
        <tr>
          <th scope="col" className="py-1 pr-3 font-medium">
            {t('maintenance.client', 'Kunde')}
          </th>
          <th scope="col" className="py-1 pr-3 font-medium">
            {t('maintenance.channel', 'Kanal')}
          </th>
          <th scope="col" className="py-1 pr-3 font-medium">
            {t('maintenance.status', 'Status')}
          </th>
          <th scope="col" className="py-1 font-medium">
            {t('maintenance.time', 'Zeit')}
          </th>
        </tr>
      </thead>
      <tbody className="text-gray-700 dark:text-gray-200">
        {rows.map((n) => (
          <tr key={n.id} data-testid="maintenance-notification">
            <td className="py-1 pr-3">{clientName(n.client_id)}</td>
            <td className="py-1 pr-3">{t(`maintenance.channels.${n.channel}`, n.channel)}</td>
            <td className="py-1 pr-3">
              <Badge variant={notificationVariant(n.status)}>
                {t(`maintenance.notificationStatuses.${n.status}`, n.status)}
              </Badge>
            </td>
            <td className="py-1">
              {/* sent_at only exists once a channel has delivered it. */}
              {new Date(n.sent_at ?? n.created_at).toLocaleString()}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function MaintenancePage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState(emptyForm);
  const [ciSearch, setCISearch] = useState('');
  const [selectedCIs, setSelectedCIs] = useState<{ id: string; name: string }[]>([]);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [notifyResult, setNotifyResult] = useState<string | null>(null);

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['maintenance-windows'],
    queryFn: () => maintenanceApi.list({ limit: 100 }),
  });
  const term = ciSearch.trim();
  const ciResults = useQuery({
    queryKey: ['maintenance-ci-search', term],
    queryFn: () => ciApi.list({ search: term, limit: 20 }),
    enabled: showCreate && term.length > 0,
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['maintenance-windows'] });
  const resetForm = () => {
    setForm(emptyForm);
    setCISearch('');
    setSelectedCIs([]);
  };
  const createMutation = useMutation({
    mutationFn: () =>
      maintenanceApi.create({
        title: form.title,
        starts_at: form.starts_at,
        ends_at: form.ends_at,
        ci_ids: selectedCIs.map((ci) => ci.id),
      }),
    onSuccess: () => {
      invalidate();
      setShowCreate(false);
      resetForm();
    },
  });
  const statusMutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: string }) =>
      maintenanceApi.update(id, { status }),
    onSuccess: invalidate,
  });
  const notifyMutation = useMutation({
    mutationFn: (id: string) => maintenanceApi.notify(id),
    // Show the real delivery status: notifications stay pending until the
    // notification chain has delivered them (WP-063); the stored list of the
    // window shows them per client (WP-540).
    onSuccess: (res, id) => {
      setExpanded((prev) => ({ ...prev, [id]: true }));
      void queryClient.invalidateQueries({ queryKey: ['maintenance-notifications', id] });
      setNotifyResult(
        [
          res.notified > 0
            ? t('maintenance.notified', '{{count}} Kunden benachrichtigt', { count: res.notified })
            : null,
          res.queued > 0
            ? t(
                'maintenance.queued',
                '{{count}} Benachrichtigungen vorgemerkt – Versand ausstehend',
                {
                  count: res.queued,
                },
              )
            : null,
        ]
          .filter(Boolean)
          .join(' · ') || t('maintenance.noClients', 'Keine betroffenen Kunden'),
      );
    },
  });

  const selectedIDs = new Set(selectedCIs.map((ci) => ci.id));

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.maintenance', 'Wartungsfenster')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t('maintenance.summary', 'Wartungen planen und betroffene Kunden benachrichtigen.')}
          </p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          {t('maintenance.create', 'Wartung planen')}
        </Button>
      </div>

      {notifyResult ? (
        <Card className="border-amber-300 bg-amber-50 dark:bg-amber-950">
          <p role="status" className="text-sm text-amber-900 dark:text-amber-100">
            {notifyResult}
          </p>
        </Card>
      ) : null}
      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      <div className="space-y-3">
        {(data?.data ?? []).map((w: MaintenanceWindow) => (
          <Card
            key={w.id}
            title={w.title}
            actions={
              <Badge variant={statusVariant(w.status)}>
                {t(`maintenance.statuses.${w.status}`, w.status)}
              </Badge>
            }
          >
            <p className="text-sm text-gray-600 dark:text-gray-300">
              {new Date(w.starts_at).toLocaleString()} → {new Date(w.ends_at).toLocaleString()}
            </p>
            <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {(w.ci_ids ?? []).length} {t('maintenance.affectedCIs', 'betroffene CIs')}
            </p>
            <div className="mt-3 flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="secondary"
                aria-expanded={!!expanded[w.id]}
                onClick={() => setExpanded((prev) => ({ ...prev, [w.id]: !prev[w.id] }))}
              >
                {t('maintenance.notifications', 'Benachrichtigungen')}
              </Button>
              {w.status === 'scheduled' ? (
                <>
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => statusMutation.mutate({ id: w.id, status: 'in_progress' })}
                  >
                    {t('maintenance.start', 'Starten')}
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => notifyMutation.mutate(w.id)}
                    disabled={notifyMutation.isPending}
                  >
                    {t('maintenance.notify', 'Kunden benachrichtigen')}
                  </Button>
                </>
              ) : null}
              {w.status === 'in_progress' ? (
                <Button
                  size="sm"
                  onClick={() => statusMutation.mutate({ id: w.id, status: 'completed' })}
                >
                  {t('maintenance.complete', 'Abschließen')}
                </Button>
              ) : null}
            </div>
            {expanded[w.id] ? (
              <div className="mt-3 border-t border-gray-200 pt-3 dark:border-gray-700">
                <WindowNotifications windowId={w.id} />
              </div>
            ) : null}
          </Card>
        ))}
      </div>

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('maintenance.create', 'Wartung planen')}
      >
        <div className="space-y-3">
          <Input
            label={t('common.title', 'Titel')}
            value={form.title}
            onChange={(e) => setForm({ ...form, title: e.target.value })}
          />
          <Input
            label={t('maintenance.startsAt', 'Beginn')}
            type="datetime-local"
            value={form.starts_at}
            onChange={(e) =>
              setForm({
                ...form,
                starts_at: e.target.value ? new Date(e.target.value).toISOString() : '',
              })
            }
          />
          <Input
            label={t('maintenance.endsAt', 'Ende')}
            type="datetime-local"
            value={form.ends_at}
            onChange={(e) =>
              setForm({
                ...form,
                ends_at: e.target.value ? new Date(e.target.value).toISOString() : '',
              })
            }
          />
          <div className="space-y-2">
            <Input
              label={t('maintenance.searchCIs', 'Betroffene CIs suchen')}
              type="search"
              value={ciSearch}
              onChange={(e) => setCISearch(e.target.value)}
            />
            {term && ciResults.data ? (
              <ul
                aria-label={t('maintenance.ciResults', 'Gefundene CIs')}
                className="max-h-40 space-y-1 overflow-y-auto"
              >
                {ciResults.data.data
                  .filter((ci) => !selectedIDs.has(ci.id))
                  .map((ci) => (
                    <li key={ci.id}>
                      <button
                        type="button"
                        className="w-full rounded px-2 py-1 text-left text-sm hover:bg-gray-100 dark:hover:bg-gray-800"
                        onClick={() =>
                          setSelectedCIs((prev) => [...prev, { id: ci.id, name: ci.name }])
                        }
                      >
                        {t('maintenance.addCI', '{{name}} hinzufügen', { name: ci.name })}
                      </button>
                    </li>
                  ))}
              </ul>
            ) : null}
            {selectedCIs.length > 0 ? (
              <ul
                aria-label={t('maintenance.selectedCIs', 'Ausgewählte CIs')}
                className="flex flex-wrap gap-2"
              >
                {selectedCIs.map((ci) => (
                  <li
                    key={ci.id}
                    className="flex items-center gap-1 rounded-full bg-gray-100 px-2 py-0.5 text-sm dark:bg-gray-800"
                  >
                    {ci.name}
                    <button
                      type="button"
                      aria-label={t('maintenance.removeCI', '{{name}} entfernen', {
                        name: ci.name,
                      })}
                      className="px-1 text-muted hover:text-gray-900 dark:hover:text-gray-100"
                      onClick={() =>
                        setSelectedCIs((prev) => prev.filter((other) => other.id !== ci.id))
                      }
                    >
                      ×
                    </button>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-sm text-gray-500 dark:text-gray-400">
                {t(
                  'maintenance.noCIsSelected',
                  'Ohne CI gilt die Wartung für die ganze Organisation.',
                )}
              </p>
            )}
          </div>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!form.title || !form.starts_at || !form.ends_at || createMutation.isPending}
            >
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
