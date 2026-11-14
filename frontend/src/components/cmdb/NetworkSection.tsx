/**
 * NetworkSection is the section "Netzwerk" of the CI detail (IPM-13, UI-14,
 * WP-919). It lists the interfaces of the CI (GET /cis/{id}/interfaces)
 * with name, admin and operational status, VLAN, MAC and the marks
 * management and uplink; below each interface stand its addresses (GET
 * /cis/{id}/addresses: address, role, state, subnet as a link to the subnet
 * detail, VRF). Addresses of the CI without an interface form their own
 * group. The head shows the management IP of the CI with the subnet and VRF
 * of its address with role management, "nicht in IPAM" when IPAM has no
 * such address, and a conflict notice with a link into the review inbox
 * while a duplicate_ip item of that address is open.
 *
 * With ipam:write the action "Adresse zuweisen" allocates the next free
 * address of a chosen subnet (POST /subnets/{id}/allocate with interface,
 * type and DNS name) and binds it with PATCH /ips/{id} (ci_id, role); a
 * failed PATCH releases the address with DELETE /ips/{id}. The section
 * renders only with ipam:read; without it no IPAM request is sent.
 */
import { useDeferredValue, useId, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { Cable, Plus } from 'lucide-react';
import { ApiError } from '../../api/client';
import {
  IP_ADDRESS_ROLES,
  PROBLEM_SUBNET_EXHAUSTED,
  ipamApi,
  type CIAddress,
  type IPAddressRole,
  type NetworkInterface,
} from '../../api/cmdb';
import { useHasPermission } from '../../api/hooks';
import { useEntitlements, useModules } from '../../hooks/useEntitlements';
import {
  CIAddressAssignFailed,
  useAddressReviews,
  useAssignCIAddress,
  useCIAddresses,
  useCIInterfaces,
  useVLAN,
  useVrfs,
} from '../../api/ipamHooks';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Card } from '../ui/Card';
import { EmptyState } from '../ui/EmptyState';
import { ErrorState } from '../ui/ErrorState';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import { SkeletonList } from '../ui/Skeleton';

export interface NetworkSectionProps {
  ciId: string;
  /** ci.management_ip; the head looks up its address in IPAM. */
  managementIp?: string;
}

/**
 * ASSIGNABLE_ROLES leaves out management: the management address follows
 * ci.management_ip (IPM-13) and is set on the CI, not by an assignment here.
 */
const ASSIGNABLE_ROLES = IP_ADDRESS_ROLES.filter((value) => value !== 'management');

/** bareIP drops a prefix length (10.0.0.1/32 → 10.0.0.1). */
function bareIP(value: string | undefined): string {
  return (value ?? '').split('/')[0]?.trim() ?? '';
}

/**
 * NetworkSection renders nothing without ipam:read, without the feature
 * ipam in the plan of the organization (the server refuses the reads with
 * 403 there), when the release switches the module ipam off (then
 * /cis/{id}/addresses does not exist: 404) and while any of them loads, so
 * the CI detail sends no IPAM request for such a session.
 */
export function NetworkSection(props: NetworkSectionProps) {
  const canRead = useHasPermission('ipam:read');
  const entitlements = useEntitlements();
  const modules = useModules();
  const on =
    canRead === true &&
    !entitlements.isLoading &&
    entitlements.isEnabled('ipam') &&
    !modules.isLoading &&
    modules.isModuleEnabled('ipam');
  if (!on) return null;
  return <NetworkSectionBody {...props} />;
}

function NetworkSectionBody({ ciId, managementIp }: NetworkSectionProps) {
  const { t } = useTranslation();
  const canWrite = useHasPermission('ipam:write') === true;
  const interfaces = useCIInterfaces(ciId);
  const addresses = useCIAddresses(ciId);
  const vrfs = useVrfs();
  const [assignOpen, setAssignOpen] = useState(false);

  const vrfName = (id: string | undefined) =>
    (id && vrfs.data?.data?.find((vrf) => vrf.id === id)?.name) || id || t('ipam.detail.none');

  const loading = interfaces.isPending || addresses.isPending;
  const failed = interfaces.isError || addresses.isError;
  const nics = interfaces.data?.data ?? [];
  const rows = addresses.data ?? [];
  const byInterface = new Map<string, CIAddress[]>();
  const unbound: CIAddress[] = [];
  const known = new Set(nics.map((nic) => nic.id));
  for (const row of rows) {
    if (row.interface_id && known.has(row.interface_id)) {
      byInterface.set(row.interface_id, [...(byInterface.get(row.interface_id) ?? []), row]);
    } else {
      unbound.push(row);
    }
  }

  return (
    <div role="region" aria-label={t('ipam.ciNetwork.title')}>
      <Card
        title={t('ipam.ciNetwork.title')}
        actions={
          canWrite ? (
            <Button
              size="sm"
              variant="secondary"
              icon={<Plus />}
              onClick={() => setAssignOpen(true)}
            >
              {t('ipam.ciNetwork.assign')}
            </Button>
          ) : null
        }
      >
        {loading ? (
          <SkeletonList rows={3} label={t('app.loading')} />
        ) : failed ? (
          <ErrorState
            title={t('ipam.ciNetwork.loadFailed')}
            retryLabel={t('common.retry')}
            onRetry={() => {
              void interfaces.refetch();
              void addresses.refetch();
            }}
          />
        ) : (
          <div className="space-y-4">
            <ManagementHead managementIp={managementIp} addresses={rows} vrfName={vrfName} />
            {nics.length === 0 ? (
              <EmptyState
                title={t('ipam.ciNetwork.noInterfaces')}
                icon={<Cable />}
                className="py-6"
              />
            ) : (
              <ul className="divide-y divide-line" aria-label={t('ipam.ciNetwork.interfaces')}>
                {nics.map((nic) => (
                  <InterfaceRow
                    key={nic.id}
                    nic={nic}
                    addresses={byInterface.get(nic.id) ?? []}
                    vrfName={vrfName}
                  />
                ))}
              </ul>
            )}
            {rows.length === 0 ? (
              <p className="text-sm text-fg-muted">{t('ipam.ciNetwork.noAddresses')}</p>
            ) : unbound.length > 0 ? (
              <section aria-label={t('ipam.ciNetwork.withoutInterface')}>
                <h4 className="mb-1 text-sm font-medium text-fg">
                  {t('ipam.ciNetwork.withoutInterface')}
                </h4>
                <AddressList addresses={unbound} vrfName={vrfName} />
              </section>
            ) : null}
          </div>
        )}
        {canWrite && (
          <AssignAddressDialog
            open={assignOpen}
            onOpenChange={setAssignOpen}
            ciId={ciId}
            interfaces={nics}
          />
        )}
      </Card>
    </div>
  );
}

/** ManagementHead shows the management IP with subnet, VRF and notices. */
function ManagementHead({
  managementIp,
  addresses,
  vrfName,
}: {
  managementIp?: string;
  addresses: CIAddress[];
  vrfName: (id: string | undefined) => string;
}) {
  const { t } = useTranslation();
  const ip = bareIP(managementIp);
  const match =
    addresses.find((a) => a.role === 'management' && (!ip || bareIP(a.address) === ip)) ??
    (ip ? addresses.find((a) => bareIP(a.address) === ip) : undefined);
  return (
    <div
      className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-line bg-canvas px-3 py-2 text-sm"
      data-testid="ci-network-management"
    >
      <span className="font-medium text-fg">{t('ipam.ciNetwork.managementIp')}</span>
      {ip || match ? (
        <span className="font-mono text-fg">{ip || bareIP(match?.address)}</span>
      ) : (
        <span className="text-fg-muted">{t('ipam.detail.none')}</span>
      )}
      {match ? (
        <>
          <SubnetLink address={match} />
          <span className="text-fg-muted">
            {t('ipam.detail.vrf')}: {vrfName(match.vrf_id)}
          </span>
          <DuplicateHint address={match} />
        </>
      ) : ip ? (
        <Badge variant="warning">{t('ipam.ciNetwork.notInIpam')}</Badge>
      ) : null}
    </div>
  );
}

/** DuplicateHint points out an open duplicate_ip item of the management address. */
function DuplicateHint({ address }: { address: CIAddress }) {
  const { t } = useTranslation();
  const canReadReviews = useHasPermission('discovery:read') === true;
  const reviews = useAddressReviews(address, canReadReviews);
  if (!(reviews.data ?? []).some((item) => item.kind === 'duplicate_ip')) return null;
  return (
    <span role="note" className="flex flex-wrap items-center gap-1.5">
      <Badge variant="danger">{t('ipam.ciNetwork.duplicate')}</Badge>
      <Link to="/reviews?kind=duplicate_ip" className="text-accent underline underline-offset-2">
        {t('ipam.ciNetwork.openInbox')}
      </Link>
    </span>
  );
}

function SubnetLink({ address }: { address: CIAddress }) {
  const { t } = useTranslation();
  if (!address.subnet_id) {
    return <span className="text-fg-muted">{t('ipam.ciNetwork.noSubnet')}</span>;
  }
  return (
    <Link
      to={`/ipam/subnets/${address.subnet_id}`}
      className="font-mono text-accent underline-offset-2 hover:underline"
    >
      {address.subnet_cidr ?? address.subnet_id}
    </Link>
  );
}

function InterfaceRow({
  nic,
  addresses,
  vrfName,
}: {
  nic: NetworkInterface;
  addresses: CIAddress[];
  vrfName: (id: string | undefined) => string;
}) {
  const { t } = useTranslation();
  return (
    <li className="space-y-1.5 py-3 first:pt-0" data-testid={`ci-interface-${nic.id}`}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
        <span className="font-medium text-fg [overflow-wrap:anywhere]">{nic.name}</span>
        <span className="text-fg-muted">
          {t('ipam.ciNetwork.status', {
            admin: nic.admin_status || t('ipam.detail.none'),
            oper: nic.oper_status || t('ipam.detail.none'),
          })}
        </span>
        {nic.vlan_id && <VLANLabel id={nic.vlan_id} />}
        {nic.mac && <span className="font-mono text-xs text-fg-muted">{nic.mac}</span>}
        {nic.is_management && <Badge variant="info">{t('ipam.ciNetwork.management')}</Badge>}
        {nic.is_uplink && <Badge variant="neutral">{t('ipam.ciNetwork.uplink')}</Badge>}
      </div>
      {addresses.length > 0 ? (
        <AddressList addresses={addresses} vrfName={vrfName} />
      ) : (
        <p className="text-xs text-fg-muted">{t('ipam.ciNetwork.interfaceNoAddresses')}</p>
      )}
    </li>
  );
}

function VLANLabel({ id }: { id: string }) {
  const { t } = useTranslation();
  const vlan = useVLAN(id);
  const label = vlan.data ? `${vlan.data.vlan_id} ${vlan.data.name}` : id;
  return (
    <span className="text-fg-muted">
      {t('ipam.detail.vlan')} {label}
    </span>
  );
}

function AddressList({
  addresses,
  vrfName,
}: {
  addresses: CIAddress[];
  vrfName: (id: string | undefined) => string;
}) {
  const { t } = useTranslation();
  return (
    <ul className="space-y-1 pl-3 text-sm">
      {addresses.map((row) => (
        <li
          key={row.id}
          className="flex flex-wrap items-center gap-x-3 gap-y-1"
          data-testid={`ci-address-${row.id}`}
        >
          <span className="font-mono text-fg">{row.address}</span>
          <Badge variant="neutral">{t(`ipam.roles.${row.role}`)}</Badge>
          {row.state && (
            <Badge variant={row.state === 'active' ? 'success' : 'neutral'}>
              {t(`ipam.states.${row.state}`)}
            </Badge>
          )}
          <SubnetLink address={row} />
          <span className="text-fg-muted">
            {t('ipam.detail.vrf')}: {vrfName(row.vrf_id)}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** looksLikeIP reports whether the search names an address (contains=) rather than text (q=). */
function looksLikeIP(text: string): boolean {
  return (
    /^\d{1,3}(\.\d{1,3}){3}$/.test(text) || (text.includes(':') && /^[0-9a-f:.]+$/i.test(text))
  );
}

function AssignAddressDialog({
  open,
  onOpenChange,
  ciId,
  interfaces,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  ciId: string;
  interfaces: NetworkInterface[];
}) {
  const { t } = useTranslation();
  return (
    <Modal open={open} onOpenChange={onOpenChange} title={t('ipam.ciNetwork.assignTitle')}>
      {open && (
        <AssignForm ciId={ciId} interfaces={interfaces} onClose={() => onOpenChange(false)} />
      )}
    </Modal>
  );
}

function AssignForm({
  ciId,
  interfaces,
  onClose,
}: {
  ciId: string;
  interfaces: NetworkInterface[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const formId = useId();
  const [search, setSearch] = useState('');
  const [subnetId, setSubnetId] = useState('');
  const [interfaceId, setInterfaceId] = useState('');
  const [role, setRole] = useState<IPAddressRole>('primary');
  const [type, setType] = useState<'static' | 'reserved'>('static');
  const [dnsName, setDnsName] = useState('');
  const [subnetError, setSubnetError] = useState<string>();
  const [message, setMessage] = useState<string>();
  const assign = useAssignCIAddress();

  const term = useDeferredValue(search.trim());
  const subnets = useQuery({
    queryKey: ['ipam', 'subnets', 'assign', term],
    queryFn: () =>
      ipamApi.listSubnets(looksLikeIP(term) ? { contains: term } : { q: term, limit: 25 }),
    enabled: term !== '',
  });
  const hits = subnets.data?.data ?? [];

  const subnetOptions = [
    {
      value: '',
      label:
        term === ''
          ? t('ipam.ciNetwork.searchFirst')
          : subnets.isPending
            ? t('ipam.ciNetwork.searching')
            : hits.length === 0
              ? t('ipam.ciNetwork.noSubnets')
              : t('ipam.ciNetwork.chooseSubnet'),
    },
    ...hits.map((subnet) => ({
      value: subnet.id,
      label: subnet.name ? `${subnet.cidr} – ${subnet.name}` : subnet.cidr,
    })),
  ];

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setSubnetError(undefined);
    setMessage(undefined);
    if (!subnetId) {
      setSubnetError(t('ipam.ciNetwork.subnetRequired'));
      return;
    }
    assign.mutate(
      {
        subnetId,
        ciId,
        role,
        type,
        interfaceId: interfaceId || undefined,
        dnsName: dnsName.trim() || undefined,
      },
      {
        onSuccess: onClose,
        onError: (err) => {
          if (err instanceof CIAddressAssignFailed) {
            const cause = err.cause;
            const detail =
              cause instanceof ApiError && cause.status === 422 && cause.detail
                ? cause.detail
                : undefined;
            setMessage(
              t(err.released ? 'ipam.ciNetwork.bindFailed' : 'ipam.ciNetwork.bindFailedKept', {
                detail: detail ?? t('ipam.ciNetwork.assignFailed'),
              }),
            );
            return;
          }
          if (err instanceof ApiError && err.type === PROBLEM_SUBNET_EXHAUSTED) {
            setSubnetError(t('ipam.ciNetwork.exhausted'));
            return;
          }
          if (err instanceof ApiError && err.status === 403) {
            setMessage(t('ipam.ciNetwork.forbidden'));
            return;
          }
          setMessage(t('ipam.ciNetwork.assignFailed'));
        },
      },
    );
  };

  return (
    <form onSubmit={submit} className="space-y-4" noValidate aria-describedby={`${formId}-hint`}>
      <p id={`${formId}-hint`} className="text-sm text-fg-muted">
        {t('ipam.ciNetwork.assignHint')} {t('ipam.ciNetwork.managementRoleHint')}
      </p>
      <Input
        label={t('ipam.ciNetwork.searchSubnet')}
        value={search}
        onChange={(event) => {
          setSearch(event.target.value);
          setSubnetId('');
        }}
        placeholder={t('ipam.ciNetwork.searchPlaceholder')}
      />
      <Select
        label={t('ipam.ciNetwork.subnet')}
        value={subnetId}
        onChange={(event) => setSubnetId(event.target.value)}
        options={subnetOptions}
        error={subnetError}
        required
      />
      <div className="grid gap-3 sm:grid-cols-2">
        <Select
          label={t('ipam.ciNetwork.interface')}
          value={interfaceId}
          onChange={(event) => setInterfaceId(event.target.value)}
          options={[
            { value: '', label: t('ipam.ciNetwork.noInterfaceChoice') },
            ...interfaces.map((nic) => ({ value: nic.id, label: nic.name })),
          ]}
        />
        <Select
          label={t('ipam.ciNetwork.role')}
          value={role}
          onChange={(event) => setRole(event.target.value as IPAddressRole)}
          options={ASSIGNABLE_ROLES.map((value) => ({
            value,
            label: t(`ipam.roles.${value}`),
          }))}
        />
        <Select
          label={t('ipam.ciNetwork.type')}
          value={type}
          onChange={(event) => setType(event.target.value as 'static' | 'reserved')}
          options={(['static', 'reserved'] as const).map((value) => ({
            value,
            label: t(`ipam.types.${value}`),
          }))}
        />
        <Input
          label={t('ipam.ciNetwork.dnsName')}
          value={dnsName}
          onChange={(event) => setDnsName(event.target.value)}
        />
      </div>
      {message && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {message}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onClose}>
          {t('ipam.addressDialog.cancel')}
        </Button>
        <Button type="submit" loading={assign.isPending}>
          {t('ipam.ciNetwork.assignSubmit')}
        </Button>
      </div>
    </form>
  );
}
