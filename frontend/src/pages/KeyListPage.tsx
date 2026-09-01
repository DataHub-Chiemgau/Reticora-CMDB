import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { keyApi, userApi } from '../api/client';
import type { KeyItem } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

function statusVariant(status: string): 'neutral' | 'warning' | 'success' | 'danger' {
  switch (status) {
    case 'available': return 'success';
    case 'issued': return 'warning';
    case 'lost': return 'danger';
    default: return 'neutral';
  }
}

export function KeyListPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [issueFor, setIssueFor] = useState<KeyItem | null>(null);
  const [issueUser, setIssueUser] = useState('');
  const [form, setForm] = useState({ name: '', key_type: 'physical', identifier: '', location: '' });

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['keys'],
    queryFn: () => keyApi.list({ limit: 100 }),
  });
  const users = useQuery({ queryKey: ['users', 'for-keys'], queryFn: () => userApi.list({ limit: 200 }) });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['keys'] });
  const createMutation = useMutation({
    mutationFn: () => keyApi.create(form),
    onSuccess: () => { invalidate(); setShowCreate(false); setForm({ name: '', key_type: 'physical', identifier: '', location: '' }); },
  });
  const issueMutation = useMutation({
    mutationFn: ({ id, userId }: { id: string; userId: string }) => keyApi.issue(id, userId),
    onSuccess: () => { invalidate(); setIssueFor(null); setIssueUser(''); },
  });
  const returnMutation = useMutation({ mutationFn: (id: string) => keyApi.returnKey(id), onSuccess: invalidate });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{t('nav.keys', 'Schlüssel')}</h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('keys.summary', 'Physische und digitale Schlüssel mit Ausgabe/Rückgabe.')}</p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>{t('keys.create', 'Schlüssel anlegen')}</Button>
      </div>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? <ErrorState title={t('app.error')} retryLabel={t('common.retry')} onRetry={() => void refetch()} /> : null}

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
        {(data?.data ?? []).map((k: KeyItem) => (
          <Card key={k.id} title={k.name} actions={<Badge variant={statusVariant(k.status)}>{k.status}</Badge>}>
            <p className="text-sm text-gray-600 dark:text-gray-300">{k.key_type} · {k.identifier || '—'}</p>
            <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">{k.location || '—'}</p>
            <div className="mt-3 flex gap-2">
              {k.status === 'available' ? (
                <Button size="sm" onClick={() => setIssueFor(k)}>{t('keys.issue', 'Ausgeben')}</Button>
              ) : null}
              {k.status === 'issued' ? (
                <Button size="sm" variant="secondary" onClick={() => returnMutation.mutate(k.id)}>{t('keys.return', 'Zurücknehmen')}</Button>
              ) : null}
            </div>
          </Card>
        ))}
      </div>

      <Modal open={showCreate} onOpenChange={setShowCreate} title={t('keys.create', 'Schlüssel anlegen')}>
        <div className="space-y-3">
          <Input label={t('common.name', 'Name')} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          <Select label={t('keys.type', 'Typ')} value={form.key_type} onChange={(e) => setForm({ ...form, key_type: e.target.value })} options={[{ value: 'physical', label: t('keys.physical', 'Physisch') }, { value: 'digital', label: t('keys.digital', 'Digital') }]} />
          <Input label={t('keys.identifier', 'Kennung')} value={form.identifier} onChange={(e) => setForm({ ...form, identifier: e.target.value })} />
          <Input label={t('common.location', 'Standort')} value={form.location} onChange={(e) => setForm({ ...form, location: e.target.value })} />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>{t('common.cancel', 'Abbrechen')}</Button>
            <Button onClick={() => createMutation.mutate()} disabled={!form.name || createMutation.isPending}>{t('common.create', 'Anlegen')}</Button>
          </div>
        </div>
      </Modal>

      <Modal open={issueFor !== null} onOpenChange={(o) => { if (!o) setIssueFor(null); }} title={t('keys.issueTitle', 'Schlüssel ausgeben')}>
        <div className="space-y-3">
          <Select
            label={t('keys.assignTo', 'Ausgeben an')}
            value={issueUser}
            onChange={(e) => setIssueUser(e.target.value)}
            options={[{ value: '', label: t('keys.selectUser', 'Nutzer wählen') }, ...(users.data?.data ?? []).map((u) => ({ value: u.id, label: u.display_name || u.email }))]}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setIssueFor(null)}>{t('common.cancel', 'Abbrechen')}</Button>
            <Button onClick={() => issueFor && issueUser && issueMutation.mutate({ id: issueFor.id, userId: issueUser })} disabled={!issueUser || issueMutation.isPending}>{t('keys.issue', 'Ausgeben')}</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
