import { Armchair, Plus } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { deskApi } from '../api/client';
import type { Desk } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
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

function statusVariant(status: string): 'neutral' | 'warning' | 'success' | 'danger' {
  switch (status) {
    case 'available':
      return 'success';
    case 'occupied':
      return 'warning';
    case 'maintenance':
      return 'danger';
    default:
      return 'neutral';
  }
}

export function DeskListPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [bookFor, setBookFor] = useState<Desk | null>(null);
  const [name, setName] = useState('');
  const [booking, setBooking] = useState({ starts_at: '', ends_at: '' });
  const [bookingError, setBookingError] = useState<string | null>(null);

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['desks'],
    queryFn: () => deskApi.list({ limit: 100 }),
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['desks'] });
  const createMutation = useMutation({
    mutationFn: () => deskApi.create({ name }),
    onSuccess: () => {
      invalidate();
      setShowCreate(false);
      setName('');
    },
  });
  const bookMutation = useMutation({
    mutationFn: (id: string) =>
      deskApi.book(id, { starts_at: booking.starts_at, ends_at: booking.ends_at }),
    onSuccess: () => {
      invalidate();
      setBookFor(null);
      setBooking({ starts_at: '', ends_at: '' });
      setBookingError(null);
    },
    onError: (e) => setBookingError(e instanceof Error ? e.message : t('app.error')),
  });

  const desks = data?.data ?? [];

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.desks', 'Arbeitsplätze')}
        description={t('desks.summary', 'Wechselnde Arbeitsplätze buchen.')}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('desks.create', 'Arbeitsplatz anlegen')}
          </Button>
        }
      />

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
          <ScrollRegion label={t('nav.desks', 'Arbeitsplätze')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('common.name', 'Name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status', 'Status')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('common.actions', 'Aktionen')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {desks.map((d: Desk) => (
                  <tr key={d.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{d.name}</td>
                    <td className={tdClasses}>
                      <Badge variant={statusVariant(d.status)}>
                        {t(`desks.statuses.${d.status}`, d.status)}
                      </Badge>
                    </td>
                    <td className={cn(tdClasses, 'text-right')}>
                      <Button size="sm" variant="secondary" onClick={() => setBookFor(d)}>
                        {t('desks.book', 'Buchen')}
                      </Button>
                    </td>
                  </tr>
                ))}
                {desks.length === 0 ? (
                  <TableEmptyRow
                    colSpan={3}
                    icon={<Armchair />}
                    title={t('desks.emptyTitle')}
                    description={t('desks.emptyHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('desks.create', 'Arbeitsplatz anlegen')}
      >
        <div className="space-y-4">
          <Input
            label={t('common.name', 'Name')}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!name || createMutation.isPending}
            >
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </div>
      </Modal>

      <Modal
        open={bookFor !== null}
        onOpenChange={(o) => {
          if (!o) {
            setBookFor(null);
            setBookingError(null);
          }
        }}
        title={t('desks.bookTitle', 'Arbeitsplatz buchen')}
      >
        <div className="space-y-4">
          <Input
            label={t('desks.startsAt', 'Von')}
            type="datetime-local"
            value={booking.starts_at}
            onChange={(e) =>
              setBooking({
                ...booking,
                starts_at: e.target.value ? new Date(e.target.value).toISOString() : '',
              })
            }
          />
          <Input
            label={t('desks.endsAt', 'Bis')}
            type="datetime-local"
            value={booking.ends_at}
            onChange={(e) =>
              setBooking({
                ...booking,
                ends_at: e.target.value ? new Date(e.target.value).toISOString() : '',
              })
            }
          />
          {bookingError ? <p className="text-sm text-danger">{bookingError}</p> : null}
          <div className="flex justify-end gap-2">
            <Button
              variant="secondary"
              onClick={() => {
                setBookFor(null);
                setBookingError(null);
              }}
            >
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() => bookFor && bookMutation.mutate(bookFor.id)}
              disabled={!booking.starts_at || !booking.ends_at || bookMutation.isPending}
            >
              {t('desks.book', 'Buchen')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
