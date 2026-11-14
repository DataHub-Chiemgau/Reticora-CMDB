/**
 * ScanTab is the tab "Scan" of the subnet detail (IPM-11, UI-14, WP-1065).
 * It holds the scan configuration of the subnet (GET/PATCH
 * /subnets/{id}/scan, WP-1062): scan_enabled, the collector group (GET
 * /collector-groups, the groups of the subnet's client and the org-wide
 * ones) with its members read only, the interval (empty is the default of
 * the type icmp), offline_after_s and discover_new_hosts. discover_new_hosts
 * is disabled for IPv6 and for subnets wider than /22 and says why. A 422
 * appears at its field.
 *
 * Below stand the last run (last_applied_at, targets per address state,
 * WP-1063) and the list "Neu gefunden": the addresses with origin scan and
 * type discovered; "Übernehmen" makes one static and pinned (PATCH
 * /ips/{id}).
 *
 * The scan is part of the feature monitoring: without it the tab shows the
 * hint of WP-857 instead of the form and sends no scan request. Without
 * ipam:write the form is read only and there is no action.
 */
import { Lock } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../../api/client';
import { SCAN_TARGET_STATES, type Subnet, type SubnetScan } from '../../api/cmdb';
import { useAllCollectors } from '../../api/hooks';
import {
  useAdoptScanHost,
  useCollectorGroups,
  useScanNewHosts,
  useSubnetScan,
  useUpdateSubnetScan,
} from '../../api/ipamHooks';
import { useEntitlements } from '../../hooks/useEntitlements';
import { lockedHint } from '../shell/navigation';
import { ScrollRegion } from '../ScrollRegion';
import { Button } from '../ui/Button';
import { Card } from '../ui/Card';
import { EmptyState } from '../ui/EmptyState';
import { ErrorState } from '../ui/ErrorState';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  thClasses,
  theadClasses,
  trClasses,
} from '../ui/table';

/** The widest IPv4 subnet discover_new_hosts accepts (WP-1062). */
const DISCOVER_MAX_PREFIX = 22;

/**
 * discoverBlocked names why discover_new_hosts is not possible for a
 * subnet: ipv6, or wide for an IPv4 subnet wider than /22.
 */
export function discoverBlocked(cidr: string): 'ipv6' | 'wide' | undefined {
  if (cidr.includes(':')) return 'ipv6';
  const prefix = Number(cidr.split('/')[1]);
  return Number.isFinite(prefix) && prefix < DISCOVER_MAX_PREFIX ? 'wide' : undefined;
}

/** ScanFeatureLock is the hint of WP-857 for a function outside the plan (feature monitoring). */
export function ScanFeatureLock({ title }: { title: string }) {
  const { t } = useTranslation();
  const { plan } = useEntitlements();
  return (
    <EmptyState
      icon={<Lock />}
      title={t('discoverability.locked.title', { name: title })}
      description={lockedHint(t, plan)}
    />
  );
}

export function ScanTab({ subnet, canWrite }: { subnet: Subnet; canWrite: boolean }) {
  const { t } = useTranslation();
  const { isEnabled, isLoading } = useEntitlements();
  if (isLoading) return <p className="text-sm text-fg-muted">{t('app.loading')}</p>;
  if (!isEnabled('monitoring')) {
    return (
      <Card>
        <ScanFeatureLock title={t('ipam.scan.title')} />
      </Card>
    );
  }
  return <ScanBody subnet={subnet} canWrite={canWrite} />;
}

function ScanBody({ subnet, canWrite }: { subnet: Subnet; canWrite: boolean }) {
  const { t } = useTranslation();
  const scan = useSubnetScan(subnet.id);
  if (scan.isError) {
    return (
      <ErrorState
        title={t('ipam.scan.loadFailed')}
        retryLabel={t('common.retry')}
        onRetry={() => void scan.refetch()}
      />
    );
  }
  if (!scan.data) return <p className="text-sm text-fg-muted">{t('app.loading')}</p>;
  return (
    <div className="space-y-6">
      <ScanForm subnet={subnet} scan={scan.data} canWrite={canWrite} />
      <LastRun scan={scan.data} />
      <NewHosts subnetId={subnet.id} canWrite={canWrite} />
    </div>
  );
}

function ScanForm({
  subnet,
  scan,
  canWrite,
}: {
  subnet: Subnet;
  scan: SubnetScan;
  canWrite: boolean;
}) {
  const { t } = useTranslation();
  const [enabled, setEnabled] = useState(scan.scan_enabled);
  const [groupId, setGroupId] = useState(scan.collector_group_id ?? '');
  const [interval, setInterval] = useState(
    scan.scan_interval_s === null ? '' : String(scan.scan_interval_s),
  );
  const [offlineAfter, setOfflineAfter] = useState(String(scan.offline_after_s));
  const [discover, setDiscover] = useState(scan.discover_new_hosts);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string>();
  const [saved, setSaved] = useState(false);
  const groups = useCollectorGroups();
  const collectors = useAllCollectors();
  const save = useUpdateSubnetScan(subnet.id);
  const readOnly = !canWrite;
  const blocked = discoverBlocked(subnet.cidr);

  // The groups of the subnet's client and the org-wide ones (PRB-03).
  // A subnet without a client offers only the org-wide groups.
  const options = (groups.data ?? []).filter(
    (g) => !g.client_id || (!!subnet.client_id && g.client_id === subnet.client_id),
  );
  const group = options.find((g) => g.id === groupId);
  const collectorName = (id: string) =>
    collectors.data?.find((c) => c.id === id)?.name ?? id.slice(0, 8);
  const members = group
    ? group.members.map((m) => ({
        collector_id: m.collector_id,
        priority: m.priority,
        member_status: m.member_status,
      }))
    : groupId && groupId === scan.collector_group_id
      ? scan.members
      : [];

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setErrors({});
    setFailure(undefined);
    setSaved(false);
    const found: Record<string, string> = {};
    const intervalValue = interval.trim() === '' ? null : Number(interval.trim());
    if (intervalValue !== null && !Number.isInteger(intervalValue)) {
      found.scan_interval_s = t('ipam.scan.integerRequired');
    }
    const offlineValue = Number(offlineAfter.trim());
    if (!offlineAfter.trim() || !Number.isInteger(offlineValue)) {
      found.offline_after_s = t('ipam.scan.integerRequired');
    }
    if (Object.keys(found).length > 0) {
      setErrors(found);
      return;
    }
    save.mutate(
      {
        scan_enabled: enabled,
        collector_group_id: groupId || null,
        scan_interval_s: intervalValue,
        offline_after_s: offlineValue,
        discover_new_hosts: blocked ? false : discover,
      },
      {
        onSuccess: () => setSaved(true),
        onError: (err) => {
          if (err instanceof ApiError && err.status === 422 && err.field) {
            setErrors({ [err.field]: err.detail || t('ipam.scan.invalid') });
            return;
          }
          setFailure(
            err instanceof ApiError && err.status === 403
              ? t('ipam.scan.forbidden')
              : t('ipam.scan.saveFailed'),
          );
        },
      },
    );
  };

  return (
    <Card title={t('ipam.scan.title')}>
      <form onSubmit={submit} className="space-y-4" noValidate aria-label={t('ipam.scan.title')}>
        <p className="text-sm text-fg-muted">{t('ipam.scan.intro')}</p>
        <label className="flex items-center gap-2 text-sm text-fg">
          <input
            type="checkbox"
            className="h-4 w-4 rounded border-line-strong"
            checked={enabled}
            disabled={readOnly}
            onChange={(event) => setEnabled(event.target.checked)}
          />
          {t('ipam.scan.enabled')}
        </label>
        <div className="grid gap-4 sm:grid-cols-3">
          <div className="space-y-1">
            <Select
              label={t('ipam.scan.collectorGroup')}
              value={groupId}
              disabled={readOnly}
              error={errors.collector_group_id}
              onChange={(event) => setGroupId(event.target.value)}
              options={[
                { value: '', label: t('ipam.scan.noGroup') },
                ...options.map((g) => ({
                  value: g.id,
                  label: g.client_id ? g.name : t('ipam.scan.orgWideGroup', { name: g.name }),
                })),
                // The stored group stays visible while the groups are loading
                // or not readable (monitoring:read), so the select shows what
                // saving sends and "no group" can still be chosen.
                ...(groupId && !group
                  ? [
                      {
                        value: groupId,
                        label: t('ipam.scan.currentGroup', { id: groupId.slice(0, 8) }),
                      },
                    ]
                  : []),
              ]}
            />
            {groups.isError ? (
              <p className="text-xs text-fg-muted" data-testid="scan-groups-unavailable">
                {t('ipam.scan.groupsUnavailable')}
              </p>
            ) : null}
          </div>
          <Input
            label={t('ipam.scan.interval')}
            inputMode="numeric"
            value={interval}
            disabled={readOnly}
            placeholder={t('ipam.scan.intervalDefault')}
            error={errors.scan_interval_s}
            onChange={(event) => setInterval(event.target.value)}
          />
          <Input
            label={t('ipam.scan.offlineAfter')}
            inputMode="numeric"
            value={offlineAfter}
            disabled={readOnly}
            error={errors.offline_after_s}
            onChange={(event) => setOfflineAfter(event.target.value)}
          />
        </div>
        <div className="space-y-1">
          <label className="flex items-center gap-2 text-sm text-fg">
            <input
              type="checkbox"
              className="h-4 w-4 rounded border-line-strong"
              checked={blocked ? false : discover}
              disabled={readOnly || !!blocked}
              aria-describedby={blocked ? 'ipam-scan-discover-why' : undefined}
              onChange={(event) => setDiscover(event.target.checked)}
            />
            {t('ipam.scan.discoverNewHosts')}
          </label>
          {blocked ? (
            <p id="ipam-scan-discover-why" className="text-xs text-fg-muted">
              {t(`ipam.scan.discoverBlocked.${blocked}`)}
            </p>
          ) : null}
          {errors.discover_new_hosts ? (
            <p className="text-sm text-danger">{errors.discover_new_hosts}</p>
          ) : null}
        </div>
        <section aria-label={t('ipam.scan.members')}>
          <h4 className="mb-1 text-sm font-medium text-fg">{t('ipam.scan.members')}</h4>
          {members.length === 0 ? (
            <p className="text-sm text-fg-muted">{t('ipam.scan.noMembers')}</p>
          ) : (
            <ul className="divide-y divide-line text-sm" data-testid="scan-members">
              {members.map((m) => (
                <li key={m.collector_id} className="flex flex-wrap gap-x-4 py-1">
                  <span className="font-medium text-fg">{collectorName(m.collector_id)}</span>
                  <span className="text-fg-muted">
                    {t('ipam.scan.priority', { priority: m.priority })}
                  </span>
                  <span className="text-fg-muted">
                    {t(`ipam.scan.memberStatuses.${m.member_status}`)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>
        {failure ? (
          <p role="alert" className="text-sm text-danger">
            {failure}
          </p>
        ) : null}
        {saved ? (
          <p role="status" className="text-sm text-fg-muted">
            {t('ipam.scan.saved')}
          </p>
        ) : null}
        {canWrite ? (
          <div className="flex justify-end">
            <Button type="submit" loading={save.isPending}>
              {t('ipam.scan.save')}
            </Button>
          </div>
        ) : null}
      </form>
    </Card>
  );
}

function formatDateTime(value: string | null | undefined, language: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(language, { dateStyle: 'medium', timeStyle: 'short' }).format(
    date,
  );
}

function LastRun({ scan }: { scan: SubnetScan }) {
  const { t, i18n } = useTranslation();
  const at = formatDateTime(scan.last_applied_at, i18n.language);
  return (
    <Card title={t('ipam.scan.lastRun')}>
      <p className="mb-3 text-sm text-fg" data-testid="scan-last-run">
        {at ? t('ipam.scan.lastRunAt', { at }) : t('ipam.scan.neverRun')}
      </p>
      <dl className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-5">
        <div>
          <dt className="text-fg-muted">{t('ipam.scan.targets')}</dt>
          <dd className="tabular-nums text-fg">{scan.targets}</dd>
        </div>
        {SCAN_TARGET_STATES.map((state) => (
          <div key={state}>
            <dt className="text-fg-muted">{t(`ipam.scan.targetStates.${state}`)}</dt>
            <dd className="tabular-nums text-fg">{String(scan.target_states[state])}</dd>
          </div>
        ))}
      </dl>
    </Card>
  );
}

function NewHosts({ subnetId, canWrite }: { subnetId: string; canWrite: boolean }) {
  const { t, i18n } = useTranslation();
  const hosts = useScanNewHosts(subnetId);
  const adopt = useAdoptScanHost(subnetId);
  const [failure, setFailure] = useState<string>();
  const rows = hosts.data?.data ?? [];
  return (
    <Card title={t('ipam.scan.newHosts')}>
      {failure ? (
        <p role="alert" className="mb-2 text-sm text-danger">
          {failure}
        </p>
      ) : null}
      {hosts.isError ? (
        <p role="alert" className="text-sm text-danger">
          {t('ipam.scan.newHostsFailed')}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-fg-muted">
          {hosts.isPending ? t('app.loading') : t('ipam.scan.noNewHosts')}
        </p>
      ) : (
        <ScrollRegion label={t('ipam.scan.newHosts')}>
          <table className={tableClasses}>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('ipam.scan.columns.address')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('ipam.scan.columns.state')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('ipam.scan.columns.lastSeen')}
                </th>
                {canWrite ? (
                  <th scope="col" className={thClasses}>
                    <span className="sr-only">{t('common.actions')}</span>
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.id} className={trClasses} data-testid={`scan-host-${row.id}`}>
                  <td className={tdMonoClasses}>{row.address}</td>
                  <td className={tdClasses}>
                    {row.state ? t(`ipam.states.${row.state}`) : t('ipam.detail.none')}
                  </td>
                  <td className={tdClasses}>
                    {formatDateTime(row.last_seen_at, i18n.language) ?? t('ipam.detail.none')}
                  </td>
                  {canWrite ? (
                    <td className={tdClasses}>
                      <Button
                        size="sm"
                        variant="secondary"
                        disabled={adopt.isPending}
                        aria-label={t('ipam.scan.adoptNamed', { address: row.address })}
                        onClick={() => {
                          setFailure(undefined);
                          adopt.mutate(row.id, {
                            onError: () => setFailure(t('ipam.scan.adoptFailed')),
                          });
                        }}
                      >
                        {t('ipam.scan.adopt')}
                      </Button>
                    </td>
                  ) : null}
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      )}
    </Card>
  );
}
