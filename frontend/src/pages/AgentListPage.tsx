import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { agentApi } from '../api/client';
import type { EndpointAgent } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

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
  const { t } = useTranslation();
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

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
          {t('nav.agents', 'Endpoint-Agents')}
        </h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
          {t('agents.summary', 'Registrierte Endpoint-Agents mit Policy und Kill-Switch.')}
        </p>
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
        {(data?.data ?? []).map((a: EndpointAgent) => (
          <Card
            key={a.id}
            title={a.hostname}
            actions={<Badge variant={statusVariant(a.status)}>{a.status}</Badge>}
          >
            <p className="text-sm text-gray-600 dark:text-gray-300">
              {a.os || '—'} · {a.arch || '—'} · v{a.version || '?'}
            </p>
            <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {t('agents.lastHeartbeat', 'Letzter Heartbeat')}:{' '}
              {a.last_heartbeat ? new Date(a.last_heartbeat).toLocaleString() : '—'}
            </p>
            <p className="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {t('agents.interval', 'Intervall')}: {a.policy?.interval_seconds ?? 60}s
              {a.policy?.metrics_enabled ? '' : ` · ${t('agents.metricsOff', 'Metriken aus')}`}
            </p>
            <div className="mt-3 flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="secondary"
                onClick={() => {
                  setPolicyFor(a);
                  setIntervalSec(String(a.policy?.interval_seconds ?? 60));
                }}
              >
                {t('agents.policy', 'Policy')}
              </Button>
              {a.status === 'disabled' ? (
                <Button size="sm" onClick={() => toggleMutation.mutate({ id: a.id, enable: true })}>
                  {t('agents.enable', 'Aktivieren')}
                </Button>
              ) : (
                <Button
                  size="sm"
                  variant="danger"
                  onClick={() => toggleMutation.mutate({ id: a.id, enable: false })}
                >
                  {t('agents.killSwitch', 'Kill-Switch')}
                </Button>
              )}
            </div>
          </Card>
        ))}
      </div>

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
