import { KeyRound, Plus } from 'lucide-react';
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
import { PageHeader } from '../components/ui/PageHeader';
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
import { TableEmptyRow } from './ListParts';

function statusVariant(status: string): 'neutral' | 'warning' | 'success' | 'danger' {
  switch (status) {
    case 'available':
      return 'success';
    case 'issued':
      return 'warning';
    case 'lost':
      return 'danger';
    default:
      return 'neutral';
  }
}

export function KeyListPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [issueFor, setIssueFor] = useState<KeyItem | null>(null);
  const [issueUser, setIssueUser] = useState('');
  const [form, setForm] = useState({
    name: '',
    key_type: 'physical',
    identifier: '',
    location: '',
  });

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['keys'],
    queryFn: () => keyApi.list({ limit: 100 }),
  });
  const users = useQuery({
    queryKey: ['users', 'for-keys'],
    queryFn: () => userApi.list({ limit: 200 }),
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['keys'] });
  const createMutation = useMutation({
    mutationFn: () => keyApi.create(form),
    onSuccess: () => {
      invalidate();
      setShowCreate(false);
      setForm({ name: '', key_type: 'physical', identifier: '', location: '' });
    },
  });
  const issueMutation = useMutation({
    mutationFn: ({ id, userId }: { id: string; userId: string }) => keyApi.issue(id, userId),
    onSuccess: () => {
      invalidate();
      setIssueFor(null);
      setIssueUser('');
    },
  });
  const returnMutation = useMutation({
    mutationFn: (id: string) => keyApi.returnKey(id),
    onSuccess: invalidate,
  });

  const keys = data?.data ?? [];

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.keys', 'Schlüssel')}
        description={t('keys.summary', 'Physische und digitale Schlüssel mit Ausgabe/Rückgabe.')}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('keys.create', 'Schlüssel anlegen')}
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
          <ScrollRegion label={t('nav.keys', 'Schlüssel')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('common.name', 'Name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('keys.type', 'Typ')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('keys.identifier', 'Kennung')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('common.location', 'Standort')}
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
                {keys.map((k: KeyItem) => (
                  <tr key={k.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{k.name}</td>
                    <td className={tdClasses}>{t(`keys.${k.key_type}`, k.key_type)}</td>
                    <td className={tdMonoClasses}>{k.identifier || '—'}</td>
                    <td className={tdClasses}>{k.location || '—'}</td>
                    <td className={tdClasses}>
                      <Badge variant={statusVariant(k.status)}>
                        {t(`keys.statuses.${k.status}`, k.status)}
                      </Badge>
                    </td>
                    <td className={cn(tdClasses, 'text-right')}>
                      {k.status === 'available' ? (
                        <Button size="sm" variant="secondary" onClick={() => setIssueFor(k)}>
                          {t('keys.issue', 'Ausgeben')}
                        </Button>
                      ) : null}
                      {k.status === 'issued' ? (
                        <Button
                          size="sm"
                          variant="secondary"
                          onClick={() => returnMutation.mutate(k.id)}
                        >
                          {t('keys.return', 'Zurücknehmen')}
                        </Button>
                      ) : null}
                    </td>
                  </tr>
                ))}
                {keys.length === 0 ? (
                  <TableEmptyRow
                    colSpan={6}
                    icon={<KeyRound />}
                    title={t('keys.emptyTitle')}
                    description={t('keys.emptyHint')}
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
        title={t('keys.create', 'Schlüssel anlegen')}
      >
        <div className="space-y-4">
          <Input
            label={t('common.name', 'Name')}
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
          <Select
            label={t('keys.type', 'Typ')}
            value={form.key_type}
            onChange={(e) => setForm({ ...form, key_type: e.target.value })}
            options={[
              { value: 'physical', label: t('keys.physical', 'Physisch') },
              { value: 'digital', label: t('keys.digital', 'Digital') },
            ]}
          />
          <Input
            label={t('keys.identifier', 'Kennung')}
            value={form.identifier}
            onChange={(e) => setForm({ ...form, identifier: e.target.value })}
          />
          <Input
            label={t('common.location', 'Standort')}
            value={form.location}
            onChange={(e) => setForm({ ...form, location: e.target.value })}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!form.name || createMutation.isPending}
            >
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </div>
      </Modal>

      <Modal
        open={issueFor !== null}
        onOpenChange={(o) => {
          if (!o) setIssueFor(null);
        }}
        title={t('keys.issueTitle', 'Schlüssel ausgeben')}
      >
        <div className="space-y-4">
          <Select
            label={t('keys.assignTo', 'Ausgeben an')}
            value={issueUser}
            onChange={(e) => setIssueUser(e.target.value)}
            options={[
              { value: '', label: t('keys.selectUser', 'Nutzer wählen') },
              ...(users.data?.data ?? []).map((u) => ({
                value: u.id,
                label: u.display_name || u.email,
              })),
            ]}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setIssueFor(null)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() =>
                issueFor &&
                issueUser &&
                issueMutation.mutate({ id: issueFor.id, userId: issueUser })
              }
              disabled={!issueUser || issueMutation.isPending}
            >
              {t('keys.issue', 'Ausgeben')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
