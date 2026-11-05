import { Cpu } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { agentApi } from '../api/client';
import type { EndpointAgent } from '../api/client';
import { ScrollRegion } from '../components/ScrollRegion';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';

function statusVariant(status: string): 'success' | 'danger' | 'neutral' {
  switch (status) {
    case 'online':
      return 'success';
    case 'disabled':
      return 'danger';
    default:
      return 'neutral';
  }
}

export function AgentListPage() {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const [policyFor, setPolicyFor] = useState<EndpointAgent | null>(null);
  const [interval, setIntervalSec] = useState('');

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['agents'],
    queryFn: () => agentApi.list({ limit: 100 }),
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['agents'] });
  const policyMutation = useMutation({
    mutationFn: ({ id, interval_seconds }: { id: string; interval_seconds: number }) =>
      agentApi.updatePolicy(id, { interval_seconds }),
    onSuccess: () => {
      invalidate();
      setPolicyFor(null);
    },
  });
  const toggleMutation = useMutation({
    mutationFn: ({ id, enable }: { id: string; enable: boolean }) =>
      enable ? agentApi.enable(id) : agentApi.disable(id),
    onSuccess: invalidate,
  });

  const agents = data?.data ?? [];

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.agents', 'Endpoint-Agents')}
        description={t(
          'agents.summary',
          'Registrierte Endpoint-Agents mit Policy und Kill-Switch.',
        )}
      />

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}
      {data && agents.length === 0 ? (
        <EmptyState
          icon={<Cpu />}
          title={t('agents.emptyTitle')}
          description={t('agents.emptyHint')}
        />
      ) : null}

      {agents.length > 0 ? (
        <Card className="overflow-hidden p-0">
          <ScrollRegion label={t('nav.agents', 'Endpoint-Agents')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('common.name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('agents.platform')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('agents.lastHeartbeat', 'Letzter Heartbeat')}
                  </th>
                  <th scope="col" className={`${thClasses} text-right`}>
                    {t('agents.interval', 'Intervall')}
                  </th>
                  <th scope="col" className={`${thClasses} text-right`}>
                    {t('common.actions')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {agents.map((a: EndpointAgent) => (
                  <tr key={a.id} className={trClasses}>
                    <td className={`${tdPrimaryClasses} font-mono text-xs`}>{a.hostname}</td>
                    <td className={tdClasses}>
                      <Badge variant={statusVariant(a.status)}>
                        {t(`agents.statuses.${a.status}`, a.status)}
                      </Badge>
                    </td>
                    <td className={tdClasses}>
                      <span className="whitespace-nowrap">
                        {a.os || '—'} / {a.arch || '—'}
                      </span>{' '}
                      <span className="font-mono text-xs text-fg">v{a.version || '?'}</span>
                    </td>
                    <td className={`${tdClasses} whitespace-nowrap tabular-nums`}>
                      {a.last_heartbeat
                        ? new Date(a.last_heartbeat).toLocaleString(i18n.language)
                        : '—'}
                    </td>
                    <td className={`${tdClasses} whitespace-nowrap text-right tabular-nums`}>
                      {a.policy?.interval_seconds ?? 60} s
                      {a.policy?.metrics_enabled ? null : (
                        <span className="ml-2 inline-block align-middle">
                          <Badge>{t('agents.metricsOff', 'Metriken aus')}</Badge>
                        </span>
                      )}
                    </td>
                    <td className={tdClasses}>
                      <div className="flex items-center justify-end gap-1.5">
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => {
                            setPolicyFor(a);
                            setIntervalSec(String(a.policy?.interval_seconds ?? 60));
                          }}
                        >
                          {t('agents.policy', 'Policy')}
                        </Button>
                        {a.status === 'disabled' ? (
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() => toggleMutation.mutate({ id: a.id, enable: true })}
                          >
                            {t('agents.enable', 'Aktivieren')}
                          </Button>
                        ) : (
                          <Button
                            size="sm"
                            variant="secondary"
                            className="text-danger"
                            onClick={() => toggleMutation.mutate({ id: a.id, enable: false })}
                          >
                            {t('agents.killSwitch', 'Kill-Switch')}
                          </Button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

      <Modal
        open={policyFor !== null}
        onOpenChange={(o) => {
          if (!o) setPolicyFor(null);
        }}
        title={t('agents.policyTitle', 'Agent-Policy')}
      >
        <div className="space-y-3">
          <Input
            label={t('agents.intervalSeconds', 'Intervall (Sekunden)')}
            type="number"
            value={interval}
            onChange={(e) => setIntervalSec(e.target.value)}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setPolicyFor(null)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() =>
                policyFor &&
                policyMutation.mutate({ id: policyFor.id, interval_seconds: Number(interval) })
              }
              disabled={policyMutation.isPending}
            >
              {t('common.save', 'Speichern')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
