import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useTicketList, useCreateTicket, useUpdateTicket, useDeleteTicket } from '../api/hooks';
import { Card } from '../components/ui/Card';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';

const statusOptions = [
  { value: '', label: 'Alle' },
  { value: 'open', label: 'Offen' },
  { value: 'in_progress', label: 'In Bearbeitung' },
  { value: 'waiting', label: 'Wartend' },
  { value: 'resolved', label: 'Gelöst' },
  { value: 'closed', label: 'Geschlossen' },
];

const priorityOptions = [
  { value: '', label: 'Alle' },
  { value: 'low', label: 'Niedrig' },
  { value: 'medium', label: 'Mittel' },
  { value: 'high', label: 'Hoch' },
  { value: 'critical', label: 'Kritisch' },
];

const categoryOptions = [
  { value: 'incident', label: 'Störung' },
  { value: 'request', label: 'Anfrage' },
  { value: 'problem', label: 'Problem' },
  { value: 'change', label: 'Änderung' },
  { value: 'task', label: 'Aufgabe' },
];

function statusVariant(status: string) {
  switch (status) {
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
  const { t } = useTranslation();
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const [priority, setPriority] = useState('');
  const [showCreate, setShowCreate] = useState(false);

  const { data, isLoading } = useTicketList({ search, status, priority });
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
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
          {t('ticket.title', 'Tickets')}
        </h1>
        <Button onClick={() => setShowCreate(true)}>
          {t('ticket.create', 'Ticket erstellen')}
        </Button>
      </div>

      <div className="flex gap-4 flex-wrap">
        <Input
          label={t('common.search', 'Suche')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Select
          label={t('ci.status', 'Status')}
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={statusOptions}
        />
        <Select
          label={t('ticket.priority', 'Priorität')}
          value={priority}
          onChange={(e) => setPriority(e.target.value)}
          options={priorityOptions}
        />
      </div>

      {isLoading ? (
        <p className="text-gray-500 dark:text-gray-400">{t('app.loading', 'Laden...')}</p>
      ) : (
        <Card title={`${t('ticket.title', 'Tickets')} (${data?.total ?? 0})`}>
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th className="pb-2">#</th>
                  <th className="pb-2">{t('ticket.ticketTitle', 'Titel')}</th>
                  <th className="pb-2">{t('ci.status', 'Status')}</th>
                  <th className="pb-2">{t('ticket.priority', 'Priorität')}</th>
                  <th className="pb-2">{t('ticket.category', 'Kategorie')}</th>
                  <th className="pb-2">{t('ticket.createdAt', 'Erstellt')}</th>
                  <th className="pb-2">{t('common.actions', 'Aktionen')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {data?.data.map((ticket) => (
                  <tr key={ticket.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2 font-mono text-xs">#{ticket.ticket_number}</td>
                    <td className="py-2 font-medium">{ticket.title}</td>
                    <td className="py-2">
                      <Badge variant={statusVariant(ticket.status)}>{ticket.status}</Badge>
                    </td>
                    <td className="py-2">
                      <Badge variant={priorityVariant(ticket.priority)}>{ticket.priority}</Badge>
                    </td>
                    <td className="py-2">{ticket.category}</td>
                    <td className="py-2">
                      {new Date(ticket.created_at).toLocaleDateString('de-DE')}
                    </td>
                    <td className="py-2 space-x-1">
                      {ticket.status === 'open' && (
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
                        onClick={() => deleteMutation.mutate(ticket.id)}
                      >
                        {t('common.delete', 'Löschen')}
                      </Button>
                    </td>
                  </tr>
                ))}
                {(!data?.data || data.data.length === 0) && (
                  <tr>
                    <td colSpan={7} className="py-4 text-center text-gray-400">
                      {t('common.noData', 'Keine Daten')}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </Card>
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
          <Input
            label={t('common.description', 'Beschreibung')}
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
          />
          <Select
            label={t('ticket.priority', 'Priorität')}
            value={form.priority}
            onChange={(e) => setForm({ ...form, priority: e.target.value })}
            options={priorityOptions.slice(1)}
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
