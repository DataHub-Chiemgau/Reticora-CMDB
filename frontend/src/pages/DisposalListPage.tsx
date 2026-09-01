import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { disposalApi, assetApi } from '../api/client';
import type { DisposalRecord } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

const methodOptions = [
  { value: 'reuse', label: 'Wiederverwendung' },
  { value: 'recycling', label: 'Recycling' },
  { value: 'destruction', label: 'Vernichtung' },
  { value: 'secure_erasure', label: 'Sichere Löschung' },
  { value: 'physical_destruction', label: 'Physische Vernichtung' },
  { value: 'return_to_vendor', label: 'Rückgabe an Hersteller' },
];

export function DisposalListPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ method: 'destruction', asset_id: '', certificate_ref: '', data_carrier: '' });

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['disposal-records'],
    queryFn: () => disposalApi.list({ limit: 100 }),
  });
  const assets = useQuery({ queryKey: ['assets', 'for-disposal'], queryFn: () => assetApi.list({ limit: 200 }) });

  const createMutation = useMutation({
    mutationFn: () => disposalApi.create({
      method: form.method,
      asset_id: form.asset_id || undefined,
      certificate_ref: form.certificate_ref || undefined,
      data_carrier: form.data_carrier || undefined,
    }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['disposal-records'] });
      setShowCreate(false);
      setForm({ method: 'destruction', asset_id: '', certificate_ref: '', data_carrier: '' });
    },
  });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{t('nav.disposal', 'Entsorgung')}</h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('disposal.summary', 'Revisionssichere Entsorgungsdokumentation (append-only, BSI/ISO).')}</p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>{t('disposal.create', 'Entsorgung dokumentieren')}</Button>
      </div>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? <ErrorState title={t('app.error')} retryLabel={t('common.retry')} onRetry={() => void refetch()} /> : null}

      <div className="space-y-3">
        {(data?.data ?? []).map((rec: DisposalRecord) => (
          <Card key={rec.id} title={rec.certificate_ref || rec.id.slice(0, 8)} actions={<Badge variant="neutral">{rec.method}</Badge>}>
            <p className="text-sm text-gray-600 dark:text-gray-300">
              {t('disposal.performedAt', 'Durchgeführt')}: {new Date(rec.performed_at).toLocaleString()}
              {rec.performed_by ? ` · ${rec.performed_by}` : ''}
            </p>
            {rec.data_carrier ? <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">{t('disposal.dataCarrier', 'Datenträger')}: {rec.data_carrier}</p> : null}
          </Card>
        ))}
      </div>

      <Modal open={showCreate} onOpenChange={setShowCreate} title={t('disposal.create', 'Entsorgung dokumentieren')}>
        <div className="space-y-3">
          <Select label={t('disposal.method', 'Methode')} value={form.method} onChange={(e) => setForm({ ...form, method: e.target.value })} options={methodOptions} />
          <Select
            label={t('disposal.asset', 'Asset')}
            value={form.asset_id}
            onChange={(e) => setForm({ ...form, asset_id: e.target.value })}
            options={[{ value: '', label: t('disposal.selectAsset', 'Asset wählen') }, ...(assets.data?.data ?? []).map((a) => ({ value: a.id, label: `${a.asset_tag} — ${a.name}` }))]}
          />
          <Input label={t('disposal.certificate', 'Zertifikats-Referenz')} value={form.certificate_ref} onChange={(e) => setForm({ ...form, certificate_ref: e.target.value })} />
          <Input label={t('disposal.dataCarrier', 'Datenträger-Vernichtung')} value={form.data_carrier} onChange={(e) => setForm({ ...form, data_carrier: e.target.value })} />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>{t('common.cancel', 'Abbrechen')}</Button>
            <Button onClick={() => createMutation.mutate()} disabled={!form.asset_id || createMutation.isPending}>{t('common.create', 'Anlegen')}</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
