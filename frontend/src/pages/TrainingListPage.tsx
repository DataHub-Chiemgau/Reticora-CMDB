import { GraduationCap, Plus } from 'lucide-react';
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
import { PageHeader } from '../components/ui/PageHeader';
import {
  tableClasses,
  tdClasses,
  tdNumericClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
import { TableEmptyRow } from './ListParts';

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

  const courses = data?.data ?? [];

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.trainings', 'Schulungen')}
        description={t('trainings.summary', 'Kurse mit Zuweisung, Fälligkeit und Nachweis.')}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('trainings.create', 'Kurs anlegen')}
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
          <ScrollRegion label={t('nav.trainings', 'Schulungen')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('common.title', 'Titel')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('trainings.category', 'Kategorie')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('trainings.validity', 'Gültigkeit')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {courses.map((c: TrainingCourse) => (
                  <tr key={c.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{c.title}</td>
                    <td className={tdClasses}>
                      <Badge variant="neutral">
                        {c.category || t('trainings.general', 'Allgemein')}
                      </Badge>
                    </td>
                    <td className={cn(tdNumericClasses, 'whitespace-nowrap')}>
                      {c.validity_months
                        ? `${c.validity_months} ${t('trainings.months', 'Monate')}`
                        : '—'}
                    </td>
                  </tr>
                ))}
                {courses.length === 0 ? (
                  <TableEmptyRow
                    colSpan={3}
                    icon={<GraduationCap />}
                    title={t('trainings.emptyTitle')}
                    description={t('trainings.emptyHint')}
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
        title={t('trainings.create', 'Kurs anlegen')}
      >
        <div className="space-y-4">
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
