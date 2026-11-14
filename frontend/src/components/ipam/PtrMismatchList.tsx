/**
 * PtrMismatchList is the view "PTR-Abweichungen" of the IPAM page (IPM-12,
 * UI-14, WP-1065): the addresses whose PTR record differs from dns_name or
 * is missing (GET /ipam/ptr-mismatches, WP-1064), filtered by section,
 * subnet and status, with address, DNS name, PTR name, the time of the
 * check and the pin. Further pages load over the cursor (API-02).
 *
 * The PTR check runs as managed dns probes of the scan and belongs to the
 * feature monitoring: without it the view shows the hint of WP-857 and
 * sends no request.
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { PTR_STATUSES, type IPAMSection, type PTRStatus } from '../../api/cmdb';
import { usePtrMismatches, useSubnetLevel } from '../../api/ipamHooks';
import { useEntitlements } from '../../hooks/useEntitlements';
import { ScrollRegion } from '../ScrollRegion';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Card } from '../ui/Card';
import { Select } from '../ui/Select';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  thClasses,
  theadClasses,
  trClasses,
} from '../ui/table';
import { ScanFeatureLock } from './ScanTab';

export function PtrMismatchList({ sections }: { sections: IPAMSection[] }) {
  const { t } = useTranslation();
  const { isEnabled, isLoading } = useEntitlements();
  if (isLoading) return <p className="text-sm text-fg-muted">{t('app.loading')}</p>;
  if (!isEnabled('monitoring')) {
    return (
      <Card>
        <ScanFeatureLock title={t('ipam.ptr.title')} />
      </Card>
    );
  }
  return <PtrMismatchBody sections={sections} />;
}

function formatDateTime(value: string | undefined, language: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(language, { dateStyle: 'medium', timeStyle: 'short' }).format(
    date,
  );
}

function PtrMismatchBody({ sections }: { sections: IPAMSection[] }) {
  const { t, i18n } = useTranslation();
  const [sectionId, setSectionId] = useState('');
  const [subnetId, setSubnetId] = useState('');
  const [status, setStatus] = useState<PTRStatus>('mismatch');
  // The subnets of the chosen section (first page) are the subnet filter.
  const subnets = useSubnetLevel({ section_id: sectionId || undefined }, !!sectionId);
  const list = usePtrMismatches({
    section_id: sectionId || undefined,
    subnet_id: subnetId || undefined,
    status,
  });
  const rows = list.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const dash = t('ipam.detail.none');

  return (
    <Card title={t('ipam.ptr.title')}>
      <p className="mb-3 text-sm text-fg-muted">{t('ipam.ptr.intro')}</p>
      <div
        className="mb-4 grid gap-3 sm:grid-cols-3"
        role="group"
        aria-label={t('ipam.ptr.filters')}
      >
        <Select
          label={t('ipam.ptr.section')}
          value={sectionId}
          onChange={(event) => {
            setSectionId(event.target.value);
            setSubnetId('');
          }}
          options={[
            { value: '', label: t('ipam.filters.all') },
            ...sections.map((s) => ({ value: s.id, label: s.name })),
          ]}
        />
        <Select
          label={t('ipam.ptr.subnet')}
          value={subnetId}
          disabled={!sectionId}
          onChange={(event) => setSubnetId(event.target.value)}
          options={[
            { value: '', label: t('ipam.filters.all') },
            ...(subnets.data?.pages.flatMap((page) => page.data ?? []) ?? []).map((s) => ({
              value: s.id,
              label: s.cidr,
            })),
          ]}
        />
        <Select
          label={t('ipam.ptr.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value as PTRStatus)}
          options={PTR_STATUSES.map((value) => ({
            value,
            label: t(`ipam.ptr.statuses.${value}`),
          }))}
        />
      </div>
      {list.isError ? (
        <p role="alert" className="text-sm text-danger">
          {t('ipam.ptr.loadFailed')}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-fg-muted">
          {list.isPending ? t('app.loading') : t('ipam.ptr.none')}
        </p>
      ) : (
        <>
          <ScrollRegion label={t('ipam.ptr.title')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('ipam.ptr.columns.address')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.ptr.columns.dnsName')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.ptr.columns.ptrName')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.ptr.columns.checkedAt')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.ptr.columns.pinned')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.id} className={trClasses} data-testid={`ptr-row-${row.id}`}>
                    <td className={tdMonoClasses}>
                      {row.subnet_id ? (
                        <Link
                          to={`/ipam/subnets/${row.subnet_id}`}
                          className="text-accent hover:underline"
                        >
                          {row.address}
                        </Link>
                      ) : (
                        row.address
                      )}
                    </td>
                    <td className={tdClasses}>{row.dns_name || dash}</td>
                    <td className={tdClasses}>
                      {row.ptr_name || <Badge>{t(`ipam.ptr.statuses.${row.ptr_status}`)}</Badge>}
                    </td>
                    <td className={tdClasses}>
                      {formatDateTime(row.ptr_checked_at, i18n.language) ?? dash}
                    </td>
                    <td className={tdClasses}>
                      {row.pinned ? t('ipam.ptr.pinnedYes') : t('ipam.ptr.pinnedNo')}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
          {list.hasNextPage ? (
            <div className="mt-3 flex justify-center">
              <Button
                variant="secondary"
                loading={list.isFetchingNextPage}
                onClick={() => void list.fetchNextPage()}
              >
                {t('ipam.ptr.more')}
              </Button>
            </div>
          ) : null}
        </>
      )}
    </Card>
  );
}
