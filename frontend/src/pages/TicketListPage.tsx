import { Plus, Search, Ticket } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useTicketList, useCreateTicket, useUpdateTicket, useDeleteTicket } from '../api/hooks';
import { ApiError } from '../api/client';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { SkeletonList } from '../components/ui/Skeleton';
import { Textarea } from '../components/ui/Textarea';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { ScrollRegion } from '../components/ScrollRegion';

// Stored ticket statuses and categories (English keys); the labels come
// from the catalog. A new ticket starts as new (TKT-01, WP-290).
export const TICKET_STATUSES = [
  'new',
  'open',
  'in_progress',
  'waiting',
  'resolved',
  'closed',
] as const;

export const TICKET_CATEGORIES = ['incident', 'request', 'problem', 'change', 'task'] as const;

export const TICKET_PRIORITIES = ['low', 'medium', 'high', 'critical'] as const;

function statusVariant(status: string) {
  switch (status) {
    case 'new':
      return 'info' as const;
    case 'open':
      return 'warning' as const;
    case 'in_progress':
      return 'info' as const;
    case 'waiting':
      return 'neutral' as const;
    case 'resolved':
      return 'success' as const;
    case 'closed':
      return 'neutral' as const;
    default:
      return 'neutral' as const;
  }
}

function priorityVariant(priority: string) {
  switch (priority) {
    case 'critical':
      return 'danger' as const;
    case 'high':
      return 'warning' as const;
    case 'medium':
      return 'neutral' as const;
    case 'low':
      return 'success' as const;
    default:
      return 'neutral' as const;
  }
}

export function TicketListPage() {
  const { t, i18n } = useTranslation();
  const statusOptions = [
    { value: '', label: t('ci.allStatus') },
    ...TICKET_STATUSES.map((value) => ({ value, label: t(`ticket.statuses.${value}`, value) })),
  ];
  const priorityLabelOptions = TICKET_PRIORITIES.map((value) => ({
    value,
    label: t(`ticket.priorities.${value}`, value),
  }));
  const priorityOptions = [
    { value: '', label: t('ticket.allPriorities') },
    ...priorityLabelOptions,
  ];
  const categoryOptions = TICKET_CATEGORIES.map((value) => ({
    value,
    label: t(`ticket.categories.${value}`, value),
  }));
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const [priority, setPriority] = useState('');
  const [showCreate, setShowCreate] = useState(false);

  const { data, isLoading, error, refetch } = useTicketList({ search, status, priority });
  const createMutation = useCreateTicket();
  const updateMutation = useUpdateTicket();
  const deleteMutation = useDeleteTicket();

  const [form, setForm] = useState({
    title: '',
    description: '',
    priority: 'medium',
    category: 'incident',
  });

  const handleCreate = () => {
    if (!form.title) return;
    createMutation.mutate(form, {
      onSuccess: () => {
        setShowCreate(false);
        setForm({ title: '', description: '', priority: 'medium', category: 'incident' });
      },
    });
  };

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('ticket.title', 'Tickets')}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('ticket.create', 'Ticket erstellen')}
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
          className="w-64"
        />
        <Select
          aria-label={t('ci.status', 'Status')}
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={statusOptions}
          className="w-44"
        />
        <Select
          aria-label={t('ticket.priority', 'Priorität')}
          value={priority}
          onChange={(e) => setPriority(e.target.value)}
          options={priorityOptions}
          className="w-44"
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

      {!isLoading && !error ? (
        <Card className="overflow-hidden p-0">
          <ScrollRegion label={t('ticket.title', 'Tickets')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    #
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ticket.ticketTitle', 'Titel')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status', 'Status')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ticket.priority', 'Priorität')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ticket.category', 'Kategorie')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ticket.createdAt', 'Erstellt')}
                  </th>
                  <th scope="col" className={`${thClasses} text-right`}>
                    {t('common.actions', 'Aktionen')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {data?.data.map((ticket) => (
                  <tr key={ticket.id} className={trClasses}>
                    <td className={tdMonoClasses}>#{ticket.ticket_number}</td>
                    <td className={`${tdPrimaryClasses} min-w-[14rem]`}>{ticket.title}</td>
                    <td className={tdClasses}>
                      <Badge variant={statusVariant(ticket.status)}>
                        {t(`ticket.statuses.${ticket.status}`, ticket.status)}
                      </Badge>
                    </td>
                    <td className={tdClasses}>
                      <Badge variant={priorityVariant(ticket.priority)}>
                        {t(`ticket.priorities.${ticket.priority}`, ticket.priority)}
                      </Badge>
                    </td>
                    <td className={tdClasses}>
                      {t(`ticket.categories.${ticket.category}`, ticket.category)}
                    </td>
                    <td className={`${tdClasses} whitespace-nowrap tabular-nums`}>
                      {new Date(ticket.created_at).toLocaleDateString(i18n.language)}
                    </td>
                    <td className={tdClasses}>
                      <div className="flex items-center justify-end gap-1.5">
                        {(ticket.status === 'new' || ticket.status === 'open') && (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() =>
                              updateMutation.mutate({
                                id: ticket.id,
                                data: { status: 'in_progress' },
                              })
                            }
                          >
                            {t('ticket.startWork', 'Bearbeiten')}
                          </Button>
                        )}
                        <Button
                          variant="ghost"
                          size="sm"
                          className="text-danger"
                          onClick={() => deleteMutation.mutate(ticket.id)}
                        >
                          {t('common.delete', 'Löschen')}
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
                {(!data?.data || data.data.length === 0) && (
                  <tr>
                    <td colSpan={7} className="px-4 py-12 text-center">
                      <div className="flex flex-col items-center gap-3">
                        <span
                          aria-hidden="true"
                          className="inline-flex h-10 w-10 items-center justify-center rounded-lg border border-line bg-panel text-fg-muted"
                        >
                          <Ticket className="h-5 w-5" />
                        </span>
                        <p className="text-sm text-fg-muted">{t('common.noData', 'Keine Daten')}</p>
                      </div>
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

      {updateMutation.error && (
        <ErrorState
          title={
            updateMutation.error instanceof ApiError && updateMutation.error.status === 409
              ? (updateMutation.error.detail ?? updateMutation.error.message)
              : updateMutation.error.message
          }
        />
      )}

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('ticket.create', 'Ticket erstellen')}
      >
        <div className="space-y-4">
          <Input
            label={t('ticket.ticketTitle', 'Titel')}
            value={form.title}
            onChange={(e) => setForm({ ...form, title: e.target.value })}
            required
          />
          <Textarea
            label={t('common.description', 'Beschreibung')}
            rows={3}
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
          />
          <Select
            label={t('ticket.priority', 'Priorität')}
            value={form.priority}
            onChange={(e) => setForm({ ...form, priority: e.target.value })}
            options={priorityLabelOptions}
          />
          <Select
            label={t('ticket.category', 'Kategorie')}
            value={form.category}
            onChange={(e) => setForm({ ...form, category: e.target.value })}
            options={categoryOptions}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button onClick={handleCreate}>{t('common.save', 'Speichern')}</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
