/**
 * VlanTab is the tab "VLANs" of the IPAM page (IPM-06, IPM-19, WP-918): the
 * VLAN domains with their sites and the VLANs with number, name, domain,
 * site and client. The domain filter reads GET /vlan-domains/{id}/vlans
 * (GET /vlans has no domain filter), the site filter GET /vlans?site_id;
 * both together narrow the VLANs of the domain to the site in the browser,
 * as does the site chosen in the layout (UI-01; the domain endpoint is not
 * in SCOPED_LISTS). Such a narrowed list loads the following pages until
 * one holds a match or none is left, so a match on a later page is never
 * hidden behind the empty text. With ipam:write
 * VLANs are created, changed and deleted; a site outside the domain (422)
 * and a number taken in the domain or the site (409) appear at their
 * fields. Domains are written only by principals without client and site
 * restriction (orgWide); for others those actions are hidden.
 */
import { Pencil, Plus, Trash2 } from 'lucide-react';
import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, type FieldErrorInfo } from '../../api/client';
import type { VLAN, VLANDomain } from '../../api/cmdb';
import { useClients } from '../../api/cmdbHooks';
import { useSites } from '../../api/hooks';
import {
  useVLANDomainMutations,
  useVLANDomains,
  useVLANList,
  useVLANMutations,
} from '../../api/ipamHooks';
import { ScrollRegion } from '../ScrollRegion';
import { Button, IconButton } from '../ui/Button';
import { Card } from '../ui/Card';
import { Input } from '../ui/Input';
import { useScopeStore } from '../../stores/scope';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import { tableClasses, tdClasses, thClasses, theadClasses, trClasses } from '../ui/table';

export interface VlanTabProps {
  /** The caller holds ipam:write. */
  canWrite: boolean;
  /** The caller has no client and no site restriction (domains). */
  orgWide: boolean;
}

export function VlanTab({ canWrite, orgWide }: VlanTabProps) {
  const { t } = useTranslation();
  const [domainId, setDomainId] = useState('');
  const [siteId, setSiteId] = useState('');
  const [editing, setEditing] = useState<VLAN | 'new'>();
  const [message, setMessage] = useState<string>();
  const domains = useVLANDomains();
  const vlans = useVLANList({ domainId: domainId || undefined, siteId: siteId || undefined });
  const sites = useSites();
  const clients = useClients();
  const { remove } = useVLANMutations();
  const domainList = domains.data?.pages.flatMap((p) => p.data ?? []) ?? [];
  const siteName = (id?: string) => sites.data?.data?.find((s) => s.id === id)?.name ?? id ?? '';
  const clientName = (id?: string) => clients.data?.data?.find((c) => c.id === id)?.name ?? id;
  const domainName = (id?: string) => domainList.find((d) => d.id === id)?.name ?? id ?? '';
  // The domain endpoint has no site filter and is not scoped by the layout:
  // the chosen site (local or from the layout) narrows its VLANs here.
  const scopeSiteId = useScopeStore((state) => state.siteId);
  const narrowSite = domainId ? siteId || scopeSiteId || '' : '';
  const rows = (vlans.data?.pages.flatMap((p) => p.data ?? []) ?? []).filter(
    (v) => !narrowSite || v.site_id === narrowSite,
  );
  // A narrowed list may hold no match on the loaded pages: follow them.
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = vlans;
  useEffect(() => {
    if (narrowSite && rows.length === 0 && hasNextPage && !isFetchingNextPage) {
      void fetchNextPage();
    }
  }, [narrowSite, rows.length, hasNextPage, isFetchingNextPage, fetchNextPage]);

  const onDelete = (vlan: VLAN) => {
    setMessage(undefined);
    remove.mutate(vlan.id, {
      onError: (err) =>
        setMessage(
          err instanceof ApiError && err.detail ? err.detail : t('ipam.vlans.deleteFailed'),
        ),
    });
  };

  return (
    <div className="space-y-4">
      <DomainSection
        domains={domainList}
        canWrite={canWrite && orgWide}
        siteName={siteName}
        hasMore={!!domains.hasNextPage}
        onMore={() => void domains.fetchNextPage()}
      />
      <Card className="space-y-3 p-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <h2 className="text-base font-semibold text-fg">{t('ipam.vlans.title')}</h2>
          {canWrite && (
            <Button icon={<Plus />} onClick={() => setEditing('new')}>
              {t('ipam.vlans.new')}
            </Button>
          )}
        </div>
        <div
          className="grid gap-3 sm:grid-cols-2"
          role="group"
          aria-label={t('ipam.vlans.filters')}
        >
          <Select
            label={t('ipam.vlans.domain')}
            value={domainId}
            onChange={(event) => setDomainId(event.target.value)}
            options={[
              { value: '', label: t('ipam.vlans.allDomains') },
              ...domainList.map((d) => ({ value: d.id, label: d.name })),
            ]}
          />
          <Select
            label={t('ipam.vlans.site')}
            value={siteId}
            onChange={(event) => setSiteId(event.target.value)}
            options={[
              { value: '', label: t('ipam.vlans.allSites') },
              ...(sites.data?.data ?? []).map((s) => ({ value: s.id, label: s.name })),
            ]}
          />
        </div>
        {message && (
          <p role="alert" className="text-sm text-red-700 dark:text-red-300">
            {message}
          </p>
        )}
        {vlans.isError ? (
          <p role="alert" className="text-sm text-red-700 dark:text-red-300">
            {t('ipam.vlans.loadFailed')}
          </p>
        ) : rows.length === 0 ? (
          <p className="text-sm text-fg-muted">
            {vlans.isPending || hasNextPage ? t('ipam.detail.loading') : t('ipam.vlans.empty')}
          </p>
        ) : (
          <ScrollRegion label={t('ipam.vlans.title')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('ipam.vlans.number')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.vlans.name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.vlans.domain')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.vlans.site')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.vlans.client')}
                  </th>
                  {canWrite && (
                    <th scope="col" className={thClasses}>
                      <span className="sr-only">{t('ipam.vlans.actions')}</span>
                    </th>
                  )}
                </tr>
              </thead>
              <tbody>
                {rows.map((vlan) => (
                  <tr key={vlan.id} className={trClasses} data-testid={`vlan-row-${vlan.id}`}>
                    <td className={tdClasses}>{vlan.vlan_id}</td>
                    <td className={tdClasses}>{vlan.name}</td>
                    <td className={tdClasses}>{domainName(vlan.domain_id)}</td>
                    <td className={tdClasses}>{siteName(vlan.site_id)}</td>
                    <td className={tdClasses}>
                      {vlan.client_id ? clientName(vlan.client_id) : t('ipam.detail.orgWide')}
                    </td>
                    {canWrite && (
                      <td className={tdClasses}>
                        <span className="flex gap-1">
                          <IconButton
                            icon={<Pencil />}
                            label={t('ipam.vlans.edit', { number: vlan.vlan_id })}
                            onClick={() => setEditing(vlan)}
                          />
                          <IconButton
                            icon={<Trash2 />}
                            label={t('ipam.vlans.delete', { number: vlan.vlan_id })}
                            onClick={() => onDelete(vlan)}
                          />
                        </span>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        )}
        {vlans.hasNextPage && (
          <Button variant="ghost" size="sm" onClick={() => void vlans.fetchNextPage()}>
            {t('ipam.tree.loadMore')}
          </Button>
        )}
      </Card>
      {editing && (
        <VlanForm
          vlan={editing === 'new' ? undefined : editing}
          domains={domainList}
          defaultDomain={domainId}
          onClose={() => setEditing(undefined)}
        />
      )}
    </div>
  );
}

function VlanForm({
  vlan,
  domains,
  defaultDomain,
  onClose,
}: {
  vlan?: VLAN;
  domains: VLANDomain[];
  defaultDomain: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [number, setNumber] = useState(vlan ? String(vlan.vlan_id) : '');
  const [name, setName] = useState(vlan?.name ?? '');
  const [siteId, setSiteId] = useState(vlan?.site_id ?? '');
  const [domainId, setDomainId] = useState(vlan?.domain_id ?? defaultDomain);
  const [errors, setErrors] = useState<Record<string, FieldErrorInfo>>({});
  const [message, setMessage] = useState<string>();
  const sites = useSites();
  const { create, update } = useVLANMutations();

  const failed = (err: unknown) => {
    if (err instanceof ApiError) {
      const fields = err.fieldErrors();
      // A taken number (409) comes without field: it belongs to the number.
      if (err.status === 409 && Object.keys(fields).length === 0) {
        fields.vlan_id = { kind: 'conflict', message: t('ipam.vlans.numberTaken') };
      }
      if (Object.keys(fields).length > 0) {
        setErrors(fields);
        return;
      }
    }
    setMessage(t('ipam.vlans.saveFailed'));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setErrors({});
    setMessage(undefined);
    const n = Number(number);
    if (!Number.isInteger(n) || n < 1 || n > 4094) {
      setErrors({ vlan_id: { kind: 'invalid', message: t('ipam.vlans.numberInvalid') } });
      return;
    }
    if (!name.trim()) {
      setErrors({ name: { kind: 'invalid', message: t('ipam.vlans.nameRequired') } });
      return;
    }
    const done = { onSuccess: onClose, onError: failed };
    if (vlan) {
      update.mutate(
        { id: vlan.id, data: { vlan_id: n, name: name.trim(), domain_id: domainId } },
        done,
      );
    } else {
      create.mutate(
        {
          vlan_id: n,
          name: name.trim(),
          ...(siteId ? { site_id: siteId } : {}),
          ...(domainId ? { domain_id: domainId } : {}),
        },
        done,
      );
    }
  };

  return (
    <Modal
      open
      onOpenChange={(open) => !open && onClose()}
      title={vlan ? t('ipam.vlans.editTitle', { number: vlan.vlan_id }) : t('ipam.vlans.new')}
    >
      <form onSubmit={submit} className="space-y-3" noValidate>
        <Input
          label={t('ipam.vlans.number')}
          type="number"
          inputMode="numeric"
          min={1}
          max={4094}
          value={number}
          onChange={(event) => setNumber(event.target.value)}
          error={errors.vlan_id?.message}
        />
        <Input
          label={t('ipam.vlans.name')}
          value={name}
          onChange={(event) => setName(event.target.value)}
          error={errors.name?.message}
        />
        <Select
          label={t('ipam.vlans.site')}
          value={siteId}
          onChange={(event) => setSiteId(event.target.value)}
          error={errors.site_id?.message}
          disabled={!!vlan}
          options={[
            { value: '', label: t('ipam.vlans.noSite') },
            ...(sites.data?.data ?? []).map((s) => ({ value: s.id, label: s.name })),
          ]}
        />
        <Select
          label={t('ipam.vlans.domain')}
          value={domainId}
          onChange={(event) => setDomainId(event.target.value)}
          error={errors.domain_id?.message}
          options={[
            { value: '', label: t('ipam.vlans.noDomain') },
            ...domains.map((d) => ({ value: d.id, label: d.name })),
          ]}
        />
        {message && (
          <p role="alert" className="text-sm text-red-700 dark:text-red-300">
            {message}
          </p>
        )}
        <div className="flex justify-end">
          <Button type="submit" loading={create.isPending || update.isPending}>
            {t('ipam.vlans.save')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function DomainSection({
  domains,
  canWrite,
  siteName,
  hasMore,
  onMore,
}: {
  domains: VLANDomain[];
  canWrite: boolean;
  siteName: (id?: string) => string;
  hasMore: boolean;
  onMore: () => void;
}) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState<VLANDomain | 'new'>();
  const [message, setMessage] = useState<string>();
  const { remove } = useVLANDomainMutations();
  const onDelete = (domain: VLANDomain) => {
    setMessage(undefined);
    remove.mutate(domain.id, {
      onError: (err) =>
        setMessage(
          err instanceof ApiError && err.detail ? err.detail : t('ipam.vlans.domainDeleteFailed'),
        ),
    });
  };
  return (
    <Card className="space-y-3 p-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <h2 className="text-base font-semibold text-fg">{t('ipam.vlans.domains')}</h2>
        {canWrite && (
          <Button variant="secondary" icon={<Plus />} onClick={() => setEditing('new')}>
            {t('ipam.vlans.newDomain')}
          </Button>
        )}
      </div>
      {message && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {message}
        </p>
      )}
      {domains.length === 0 ? (
        <p className="text-sm text-fg-muted">{t('ipam.vlans.noDomains')}</p>
      ) : (
        <ul className="divide-y divide-line text-sm">
          {domains.map((domain) => (
            <li key={domain.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
              <span>
                <span className="font-medium text-fg">{domain.name}</span>{' '}
                <span className="text-fg-muted">
                  {domain.site_ids.length > 0
                    ? domain.site_ids.map(siteName).join(', ')
                    : t('ipam.vlans.domainNoSites')}
                </span>
              </span>
              {canWrite && (
                <span className="flex gap-1">
                  <IconButton
                    icon={<Pencil />}
                    label={t('ipam.vlans.editDomain', { name: domain.name })}
                    onClick={() => setEditing(domain)}
                  />
                  <IconButton
                    icon={<Trash2 />}
                    label={t('ipam.vlans.deleteDomain', { name: domain.name })}
                    onClick={() => onDelete(domain)}
                  />
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
      {hasMore && (
        <Button variant="ghost" size="sm" onClick={onMore}>
          {t('ipam.tree.loadMore')}
        </Button>
      )}
      {editing && (
        <DomainForm
          domain={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(undefined)}
        />
      )}
    </Card>
  );
}

function DomainForm({ domain, onClose }: { domain?: VLANDomain; onClose: () => void }) {
  const { t } = useTranslation();
  const [name, setName] = useState(domain?.name ?? '');
  const [siteIds, setSiteIds] = useState<string[]>(domain?.site_ids ?? []);
  const [errors, setErrors] = useState<Record<string, FieldErrorInfo>>({});
  const [message, setMessage] = useState<string>();
  const sites = useSites();
  const { create, update } = useVLANDomainMutations();
  const failed = (err: unknown) => {
    if (err instanceof ApiError) {
      const fields = err.fieldErrors();
      if (Object.keys(fields).length > 0) {
        setErrors(fields);
        return;
      }
      if (err.detail) {
        setMessage(err.detail);
        return;
      }
    }
    setMessage(t('ipam.vlans.saveFailed'));
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    setErrors({});
    setMessage(undefined);
    if (!name.trim()) {
      setErrors({ name: { kind: 'invalid', message: t('ipam.vlans.nameRequired') } });
      return;
    }
    const data = { name: name.trim(), site_ids: siteIds };
    const done = { onSuccess: onClose, onError: failed };
    if (domain) update.mutate({ id: domain.id, data }, done);
    else create.mutate(data, done);
  };
  const toggle = (id: string, on: boolean) =>
    setSiteIds((ids) => (on ? [...ids, id] : ids.filter((x) => x !== id)));
  return (
    <Modal
      open
      onOpenChange={(open) => !open && onClose()}
      title={domain ? t('ipam.vlans.editDomain', { name: domain.name }) : t('ipam.vlans.newDomain')}
    >
      <form onSubmit={submit} className="space-y-3" noValidate>
        <Input
          label={t('ipam.vlans.domainName')}
          value={name}
          onChange={(event) => setName(event.target.value)}
          error={errors.name?.message}
        />
        <fieldset
          className="space-y-1"
          aria-describedby={errors.site_ids ? 'vlan-domain-sites-error' : undefined}
        >
          <legend className="text-sm font-medium text-fg">{t('ipam.vlans.domainSites')}</legend>
          {(sites.data?.data ?? []).map((site) => (
            <label key={site.id} className="flex items-center gap-2 text-sm text-fg">
              <input
                type="checkbox"
                checked={siteIds.includes(site.id)}
                onChange={(event) => toggle(site.id, event.target.checked)}
              />
              {site.name}
            </label>
          ))}
          {errors.site_ids && (
            <p id="vlan-domain-sites-error" className="text-sm text-red-700 dark:text-red-300">
              {errors.site_ids.message}
            </p>
          )}
        </fieldset>
        {message && (
          <p role="alert" className="text-sm text-red-700 dark:text-red-300">
            {message}
          </p>
        )}
        <div className="flex justify-end">
          <Button type="submit" loading={create.isPending || update.isPending}>
            {t('ipam.vlans.save')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
