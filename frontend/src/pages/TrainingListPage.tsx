import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { trainingApi } from '../api/client';
import type { TrainingCourse } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

export function TrainingListPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ title: '', category: '', validity_months: '' });

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['trainings'],
    queryFn: () => trainingApi.list({ limit: 100 }),
  });

  const createMutation = useMutation({
    mutationFn: () =>
      trainingApi.create({
        title: form.title,
        category: form.category || undefined,
        validity_months: form.validity_months ? Number(form.validity_months) : undefined,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['trainings'] });
      setShowCreate(false);
      setForm({ title: '', category: '', validity_months: '' });
    },
  });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.trainings', 'Schulungen')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t('trainings.summary', 'Kurse mit Zuweisung, Fälligkeit und Nachweis.')}
          </p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          {t('trainings.create', 'Kurs anlegen')}
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
        {(data?.data ?? []).map((c: TrainingCourse) => (
          <Card
            key={c.id}
            title={c.title}
            actions={
              <Badge variant="info">{c.category || t('trainings.general', 'Allgemein')}</Badge>
            }
          >
            {c.validity_months ? (
              <p className="text-sm text-gray-600 dark:text-gray-300">
                {t('trainings.validity', 'Gültigkeit')}: {c.validity_months}{' '}
                {t('trainings.months', 'Monate')}
              </p>
            ) : null}
          </Card>
        ))}
      </div>

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('trainings.create', 'Kurs anlegen')}
      >
        <div className="space-y-3">
          <Input
            label={t('common.title', 'Titel')}
            value={form.title}
            onChange={(e) => setForm({ ...form, title: e.target.value })}
          />
          <Input
            label={t('trainings.category', 'Kategorie')}
            value={form.category}
            onChange={(e) => setForm({ ...form, category: e.target.value })}
          />
          <Input
            label={t('trainings.validityMonths', 'Gültigkeit (Monate)')}
            type="number"
            value={form.validity_months}
            onChange={(e) => setForm({ ...form, validity_months: e.target.value })}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!form.title || createMutation.isPending}
            >
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
