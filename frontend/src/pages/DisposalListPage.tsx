import { Plus, Recycle } from 'lucide-react';
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
import { PageHeader } from '../components/ui/PageHeader';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
import { TableEmptyRow } from './ListParts';

const METHODS = [
  'reuse',
  'recycling',
  'destruction',
  'secure_erasure',
  'physical_destruction',
  'return_to_vendor',
] as const;

export function DisposalListPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({
    method: 'destruction',
    asset_id: '',
    certificate_ref: '',
    data_carrier: '',
  });

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['disposal-records'],
    queryFn: () => disposalApi.list({ limit: 100 }),
  });
  const assets = useQuery({
    queryKey: ['assets', 'for-disposal'],
    queryFn: () => assetApi.list({ limit: 200 }),
  });

  const createMutation = useMutation({
    mutationFn: () =>
      disposalApi.create({
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

  const records = data?.data ?? [];
  const methodLabel = (method: string) => t(`disposal.methods.${method}`, method);
  const methodOptions = METHODS.map((value) => ({ value, label: t(`disposal.methods.${value}`) }));

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.disposal', 'Entsorgung')}
        description={t(
          'disposal.summary',
          'Revisionssichere Entsorgungsdokumentation (append-only, BSI/ISO).',
        )}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('disposal.create', 'Entsorgung dokumentieren')}
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
          <ScrollRegion label={t('nav.disposal', 'Entsorgung')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('disposal.certificate', 'Zertifikats-Referenz')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('disposal.method', 'Methode')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('disposal.performedAt', 'Durchgeführt')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('disposal.dataCarrier', 'Datenträger')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {records.map((rec: DisposalRecord) => (
                  <tr key={rec.id} className={trClasses}>
                    <td className={cn(tdMonoClasses, 'font-medium')}>
                      {rec.certificate_ref || rec.id.slice(0, 8)}
                    </td>
                    <td className={tdClasses}>
                      <Badge variant="neutral">{methodLabel(rec.method)}</Badge>
                    </td>
                    <td className={tdClasses}>
                      <span className="tabular-nums">
                        {new Date(rec.performed_at).toLocaleString()}
                      </span>
                      {rec.performed_by ? (
                        <span
                          className="block max-w-[16ch] truncate text-xs text-fg-subtle"
                          title={rec.performed_by}
                        >
                          {rec.performed_by}
                        </span>
                      ) : null}
                    </td>
                    <td className={tdClasses}>{rec.data_carrier || '—'}</td>
                  </tr>
                ))}
                {records.length === 0 ? (
                  <TableEmptyRow
                    colSpan={4}
                    icon={<Recycle />}
                    title={t('disposal.emptyTitle')}
                    description={t('disposal.emptyHint')}
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
        title={t('disposal.create', 'Entsorgung dokumentieren')}
      >
        <div className="space-y-4">
          <Select
            label={t('disposal.method', 'Methode')}
            value={form.method}
            onChange={(e) => setForm({ ...form, method: e.target.value })}
            options={methodOptions}
          />
          <Select
            label={t('disposal.asset', 'Asset')}
            value={form.asset_id}
            onChange={(e) => setForm({ ...form, asset_id: e.target.value })}
            options={[
              { value: '', label: t('disposal.selectAsset', 'Asset wählen') },
              ...(assets.data?.data ?? []).map((a) => ({
                value: a.id,
                label: `${a.inventory_no ?? a.id} — ${a.name}`,
              })),
            ]}
          />
          <Input
            label={t('disposal.certificate', 'Zertifikats-Referenz')}
            value={form.certificate_ref}
            onChange={(e) => setForm({ ...form, certificate_ref: e.target.value })}
          />
          <Input
            label={t('disposal.dataCarrier', 'Datenträger-Vernichtung')}
            value={form.data_carrier}
            onChange={(e) => setForm({ ...form, data_carrier: e.target.value })}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!form.asset_id || createMutation.isPending}
            >
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
