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

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.desks', 'Arbeitsplätze')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t('desks.summary', 'Wechselnde Arbeitsplätze buchen.')}
          </p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          {t('desks.create', 'Arbeitsplatz anlegen')}
        </Button>
      </div>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
        {(data?.data ?? []).map((d: Desk) => (
          <Card
            key={d.id}
            title={d.name}
            actions={<Badge variant={statusVariant(d.status)}>{d.status}</Badge>}
          >
            <div className="mt-3">
              <Button size="sm" onClick={() => setBookFor(d)}>
                {t('desks.book', 'Buchen')}
              </Button>
            </div>
          </Card>
        ))}
      </div>

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('desks.create', 'Arbeitsplatz anlegen')}
      >
        <div className="space-y-3">
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
        <div className="space-y-3">
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
          {bookingError ? <p className="text-sm text-red-600">{bookingError}</p> : null}
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
