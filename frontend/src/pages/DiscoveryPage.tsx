import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useCollectors } from '../api/hooks';
import type { Collector } from '../api/client';
import { fetchAPI } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';

interface EnrollmentCodeResponse {
  id: string;
  code: string;
  label: string;
  expires_at: string;
}

function createEnrollmentCode(label: string): Promise<EnrollmentCodeResponse> {
  return fetchAPI('/collectors/enrollment-codes', {
    method: 'POST',
    body: JSON.stringify({ label }),
  });
}

/** EnrollWizard guides connecting a collector: mint a code, show the command. */
function EnrollWizard({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [label, setLabel] = useState('');
  const [code, setCode] = useState<EnrollmentCodeResponse | null>(null);
  const [copied, setCopied] = useState(false);

  const mint = useMutation({
    mutationFn: () => createEnrollmentCode(label || 'collector'),
    onSuccess: (data) => {
      setCode(data);
      queryClient.invalidateQueries({ queryKey: ['collectors'] });
    },
  });

  const enrollCommand = code
    ? `reticora-collector enroll --server <cloud-url> --code ${code.code}`
    : '';

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(enrollCommand);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      /* clipboard unavailable */
    }
  };

  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!o) {
          setCode(null);
          setLabel('');
          onClose();
        }
      }}
      title={t('discovery.enroll.title', 'Collector verbinden')}
    >
      {!code ? (
        <div className="space-y-4">
          <p className="text-sm text-gray-600 dark:text-gray-300">
            {t(
              'discovery.enroll.intro',
              'Schließen Sie einen Collector an: erzeugen Sie einen einmaligen Enrollment-Code und geben Sie ihn im Collector ein. Die Infrastruktur erscheint automatisch.',
            )}
          </p>
          <label className="block text-sm">
            <span className="mb-1 block text-gray-700 dark:text-gray-300">
              {t('discovery.enroll.label', 'Bezeichnung (z. B. Standort)')}
            </span>
            <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="site-a" />
          </label>
          {mint.isError ? <p className="text-sm text-red-600">{t('app.error')}</p> : null}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button onClick={() => mint.mutate()} disabled={mint.isPending}>
              {mint.isPending ? t('app.loading') : t('discovery.enroll.generate', 'Code erzeugen')}
            </Button>
          </div>
        </div>
      ) : (
        <div className="space-y-4">
          <p className="text-sm text-gray-600 dark:text-gray-300">
            {t(
              'discovery.enroll.once',
              'Dieser Code wird nur einmal angezeigt und läuft ab. Führen Sie im Kundennetz aus:',
            )}
          </p>
          <pre className="overflow-x-auto rounded-lg bg-gray-900 p-3 text-xs text-green-200 dark:bg-black">
            {enrollCommand}
          </pre>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            {t('discovery.enroll.expires', 'Gültig bis')}:{' '}
            {new Date(code.expires_at).toLocaleString()}
          </p>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={copy}>
              {copied ? t('common.copied', 'Kopiert!') : t('common.copy', 'Kopieren')}
            </Button>
            <Button
              onClick={() => {
                setCode(null);
                setLabel('');
                onClose();
              }}
            >
              {t('common.done', 'Fertig')}
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
}

function formatHeartbeat(value?: string) {
  if (!value) {
    return '—';
  }

  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }

  return new Intl.DateTimeFormat('de-DE', {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(date);
}

function getCollectorVariant(
  status: string,
): 'success' | 'warning' | 'danger' | 'neutral' | 'info' {
  switch (status) {
    case 'online':
      return 'success';
    case 'offline':
      return 'danger';
    case 'maintenance':
      return 'warning';
    default:
      return 'neutral';
  }
}

function CollectorCard({ collector }: { collector: Collector }) {
  const { t } = useTranslation();

  return (
    <Card
      title={collector.name}
      actions={<Badge variant={getCollectorVariant(collector.status)}>{collector.status}</Badge>}
    >
      <dl className="space-y-2 text-sm">
        <div className="flex justify-between gap-4">
          <dt className="text-gray-500 dark:text-gray-400">{t('discovery.version')}</dt>
          <dd className="font-medium text-gray-900 dark:text-gray-100">
            {collector.version || '—'}
          </dd>
        </div>
        <div className="flex justify-between gap-4">
          <dt className="text-gray-500 dark:text-gray-400">{t('discovery.lastHeartbeat')}</dt>
          <dd className="font-medium text-right text-gray-900 dark:text-gray-100">
            {formatHeartbeat(collector.last_heartbeat)}
          </dd>
        </div>
        <div className="flex justify-between gap-4">
          <dt className="text-gray-500 dark:text-gray-400">{t('discovery.createdAt')}</dt>
          <dd className="font-medium text-right text-gray-900 dark:text-gray-100">
            {formatHeartbeat(collector.created_at)}
          </dd>
        </div>
      </dl>
    </Card>
  );
}

export function DiscoveryPage() {
  const { t } = useTranslation();
  const { data, isLoading, error } = useCollectors({ limit: 100, offset: 0 });
  const [enrollOpen, setEnrollOpen] = useState(false);

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.discovery')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('discovery.summary')}</p>
        </div>
        <div className="flex items-center gap-3">
          <span className="text-sm text-gray-500 dark:text-gray-400">
            {data?.total ?? 0} {t('discovery.collectors')}
          </span>
          <Button size="sm" onClick={() => setEnrollOpen(true)}>
            {t('discovery.enroll.button', 'Collector verbinden')}
          </Button>
        </div>
      </div>

      <EnrollWizard open={enrollOpen} onClose={() => setEnrollOpen(false)} />

      {isLoading ? (
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('app.loading')}</p>
      ) : null}
      {error ? <p className="text-sm text-red-600 dark:text-red-400">{t('app.error')}</p> : null}

      {data?.data.length ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {data.data.map((collector) => (
            <CollectorCard key={collector.id} collector={collector} />
          ))}
        </div>
      ) : null}

      {data && data.data.length === 0 ? (
        <Card>
          <p className="text-sm text-gray-500 dark:text-gray-400">{t('common.noResults')}</p>
        </Card>
      ) : null}
    </div>
  );
}
