/**
 * SubnetDetailPage is the page /ipam/subnets/:id (IPM-19, UI-14, WP-916).
 * The head shows CIDR, name, VRF with route distinguisher, VLAN, gateway,
 * the effective DNS servers (IPM-05), client, site, the parent chain as
 * breadcrumb and the usage (total, occupied by state and type, free,
 * percent). Below follow the address map (IPv4 up to /22) and the address
 * list, one page of at most API_MAX_LIMIT addresses with a link to the CI
 * (and its interface). With ipam:write the page allocates the next free
 * address (a full subnet is 409 subnet-exhausted and says so), allocates a
 * child subnet of a prefix length, reserves and releases addresses and
 * edits or deletes the subnet in the SubnetFormModal. A search hit of an
 * address (?address=<id>) marks its row and its map cell; when the hit is
 * not on the loaded page it is shown as a marked row above the list.
 * Each subnet id mounts its own page, so the address page, the
 * reservation form and the chosen cell never carry over to another subnet.
 * WP-917: the address of each row opens the AddressDialog (edit with
 * ipam:write, else read only), "Adresse anlegen" opens it for a new address, and the
 * card History shows GET /subnets/{id}/history on demand (HistoryList).
 */
import { History, Pencil, Plus, Sparkles } from 'lucide-react';
import { useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { API_MAX_LIMIT, ApiError, orgReadOnlyMessage } from '../api/client';
import {
  IP_ADDRESS_STATES,
  IP_ADDRESS_TYPES,
  PROBLEM_SUBNET_EXHAUSTED,
  type IPAddress,
  type Subnet,
  type SubnetUsage,
} from '../api/cmdb';
import { useClients } from '../api/cmdbHooks';
import { useHasPermission, useSites } from '../api/hooks';
import {
  useAllSubnetAddresses,
  useIPAddress,
  useSubnet,
  useSubnetActions,
  useSubnetAddressPage,
  useSubnetAncestors,
  useSubnetUsage,
  useVLAN,
  useVRF,
} from '../api/ipamHooks';
import { ScrollRegion } from '../components/ScrollRegion';
import { AddressDialog } from '../components/ipam/AddressDialog';
import { AddressMap, mapSupported, type MapCell } from '../components/ipam/AddressMap';
import { HistoryList } from '../components/ipam/HistoryList';
import { SubnetFormModal } from '../components/ipam/SubnetFormModal';
import { ScanTab } from '../components/ipam/ScanTab';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  thClasses,
  theadClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';

export function SubnetDetailPage() {
  const { id = '' } = useParams<{ id: string }>();
  // The key remounts the detail per subnet: breadcrumb, tree and "open"
  // links reuse the route, and state of subnet A must not reach subnet B.
  return <SubnetDetail key={id} id={id} />;
}

function SubnetDetail({ id }: { id: string }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const subnet = useSubnet(id);
  const [editing, setEditing] = useState(false);
  // The address of the reservation form; a free cell of the map fills it.
  const [reserveAddress, setReserveAddress] = useState('');
  const canWrite = useHasPermission('ipam:write') === true;
  // The tab stays in the query (?tab=scan), so a reload keeps it.
  const [params, setParams] = useSearchParams();
  const tab: SubnetTab = params.get('tab') === 'scan' ? 'scan' : 'addresses';
  const chooseTab = (next: SubnetTab) =>
    setParams(
      (current) => {
        const copy = new URLSearchParams(current);
        if (next === 'addresses') copy.delete('tab');
        else copy.set('tab', next);
        return copy;
      },
      { replace: true },
    );

  if (subnet.isError) {
    const missing = subnet.error instanceof ApiError && subnet.error.status === 404;
    return (
      <ErrorState
        title={missing ? t('ipam.detail.notFound') : t('ipam.detail.loadFailed')}
        onRetry={missing ? undefined : () => void subnet.refetch()}
      />
    );
  }
  if (!subnet.data) {
    return <p className="text-sm text-fg-muted">{t('ipam.detail.loading')}</p>;
  }
  const s = subnet.data;
  return (
    <div className="space-y-6">
      <SubnetHead subnet={s} canWrite={canWrite} onEdit={() => setEditing(true)} />
      <SubnetTabList tab={tab} onChange={chooseTab} />
      {tab === 'scan' ? (
        <div role="tabpanel" id="subnet-panel-scan" aria-labelledby="subnet-tab-scan">
          <ScanTab subnet={s} canWrite={canWrite} />
        </div>
      ) : (
        <div
          role="tabpanel"
          id="subnet-panel-addresses"
          aria-labelledby="subnet-tab-addresses"
          className="space-y-6"
        >
          {canWrite && (
            <SubnetActions subnet={s} address={reserveAddress} setAddress={setReserveAddress} />
          )}
          <Addresses subnet={s} canWrite={canWrite} onPickFree={setReserveAddress} />
          <SubnetHistory subnet={s} />
        </div>
      )}
      {editing && (
        <SubnetFormModal
          open
          onOpenChange={(open) => !open && setEditing(false)}
          subnet={s}
          onDeleted={() => navigate('/ipam')}
        />
      )}
    </div>
  );
}

const SUBNET_TABS = ['addresses', 'scan'] as const;
type SubnetTab = (typeof SUBNET_TABS)[number];

/** SubnetTabList switches between the addresses and the scan (WP-1065); arrow keys move. */
function SubnetTabList({ tab, onChange }: { tab: SubnetTab; onChange: (tab: SubnetTab) => void }) {
  const { t } = useTranslation();
  const refs = useRef<Partial<Record<SubnetTab, HTMLButtonElement | null>>>({});
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0;
    if (step === 0) return;
    event.preventDefault();
    const next = SUBNET_TABS[(index + step + SUBNET_TABS.length) % SUBNET_TABS.length];
    if (!next) return;
    onChange(next);
    refs.current[next]?.focus();
  };
  return (
    <div
      role="tablist"
      aria-label={t('ipam.scan.tabs')}
      className="flex gap-1 border-b border-line"
    >
      {SUBNET_TABS.map((id, index) => (
        <button
          key={id}
          ref={(node) => {
            refs.current[id] = node;
          }}
          type="button"
          role="tab"
          id={`subnet-tab-${id}`}
          aria-selected={tab === id}
          aria-controls={`subnet-panel-${id}`}
          tabIndex={tab === id ? 0 : -1}
          onClick={() => onChange(id)}
          onKeyDown={(event) => onKeyDown(event, index)}
          className={cn(
            '-mb-px border-b-2 px-3 py-2 text-sm font-medium focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
            tab === id ? 'border-accent text-fg' : 'border-transparent text-fg-muted hover:text-fg',
          )}
        >
          {t(`ipam.scan.tab.${id}`)}
        </button>
      ))}
    </div>
  );
}

function SubnetHead({
  subnet,
  canWrite,
  onEdit,
}: {
  subnet: Subnet;
  canWrite: boolean;
  onEdit: () => void;
}) {
  const { t } = useTranslation();
  const ancestors = useSubnetAncestors(subnet.parent_id);
  const vrf = useVRF(subnet.vrf_id);
  const vlan = useVLAN(subnet.vlan_id);
  const clients = useClients();
  const sites = useSites();
  const usage = useSubnetUsage(subnet.id);
  const clientName = clients.data?.data?.find((c) => c.id === subnet.client_id)?.name;
  const siteName = sites.data?.data?.find((x) => x.id === subnet.site_id)?.name;
  const crumbs = [
    { label: t('ipam.title'), to: '/ipam' },
    ...(ancestors.data ?? []).map((p) => ({ label: p.cidr, to: `/ipam/subnets/${p.id}` })),
    { label: subnet.cidr },
  ];
  const dash = t('ipam.detail.none');
  const vrfText = vrf.data
    ? vrf.data.route_distinguisher
      ? t('ipam.detail.vrfWithRd', { vrf: vrf.data.name, rd: vrf.data.route_distinguisher })
      : vrf.data.name
    : (subnet.vrf_id ?? dash);
  const rows: [string, string][] = [
    [t('ipam.detail.vrf'), vrfText],
    [t('ipam.detail.vlan'), vlan.data ? `${vlan.data.vlan_id} ${vlan.data.name}` : dash],
    [t('ipam.detail.gateway'), subnet.gateway || dash],
    [t('ipam.detail.dns'), (subnet.effective_dns_servers ?? []).join(', ') || dash],
    [
      t('ipam.detail.client'),
      subnet.client_id ? (clientName ?? subnet.client_id) : t('ipam.detail.orgWide'),
    ],
    [t('ipam.detail.site'), subnet.site_id ? (siteName ?? subnet.site_id) : dash],
  ];
  return (
    <>
      <PageHeader
        breadcrumbs={crumbs}
        title={<span className="font-mono">{subnet.cidr}</span>}
        description={subnet.name || subnet.description}
        meta={subnet.is_pool ? <Badge>{t('ipam.pool.true')}</Badge> : undefined}
        actions={
          canWrite && (
            <Button variant="secondary" icon={<Pencil />} onClick={onEdit}>
              {t('ipam.detail.edit')}
            </Button>
          )
        }
      />
      <div className="grid gap-4 lg:grid-cols-2">
        <Card className="p-4">
          <h2 className="mb-3 text-base font-semibold text-fg">{t('ipam.detail.properties')}</h2>
          <dl className="grid grid-cols-[minmax(0,10rem)_1fr] gap-x-4 gap-y-2 text-sm">
            {rows.map(([label, value]) => (
              <div key={label} className="contents">
                <dt className="text-fg-muted">{label}</dt>
                <dd className="text-fg [overflow-wrap:anywhere]">{value}</dd>
              </div>
            ))}
          </dl>
        </Card>
        <Card className="p-4">
          <h2 className="mb-3 text-base font-semibold text-fg">{t('ipam.detail.usage')}</h2>
          {usage.data ? (
            <UsageFacts usage={usage.data} />
          ) : usage.isError ? (
            <p role="alert" className="text-sm text-red-700 dark:text-red-300">
              {t('ipam.detail.usageFailed')}
            </p>
          ) : (
            <p className="text-sm text-fg-muted">{t('ipam.detail.loading')}</p>
          )}
        </Card>
      </div>
    </>
  );
}

function UsageFacts({ usage }: { usage: SubnetUsage }) {
  const { t } = useTranslation();
  const total = usage.total_text ?? String(usage.total ?? '');
  const free = usage.free_text ?? String(usage.free ?? '');
  const used = usage.is_pool
    ? (usage.covered_text ?? String(usage.covered ?? 0))
    : String(usage.used);
  const percent = Math.max(0, Math.min(100, usage.percent));
  return (
    <div className="space-y-3 text-sm">
      <p className="text-fg" data-testid="subnet-usage">
        {t('ipam.usage.text', { used, total, percent: percent.toFixed(1) })}
      </p>
      <div
        role="meter"
        aria-label={t('ipam.usage.label', { subnet: usage.cidr })}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
        className="h-2 w-full overflow-hidden rounded-full bg-fg/[0.08]"
      >
        <span
          className={cn(
            'block h-full rounded-full',
            percent >= 90 ? 'bg-red-600' : percent >= 70 ? 'bg-amber-500' : 'bg-accent',
          )}
          style={{ width: `${percent}%` }}
        />
      </div>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-1">
        <dt className="text-fg-muted">{t('ipam.detail.free')}</dt>
        <dd className="tabular-nums text-fg">{free || t('ipam.detail.none')}</dd>
        {!usage.is_pool &&
          IP_ADDRESS_STATES.map((state) => (
            <div key={state} className="contents">
              <dt className="text-fg-muted">{t(`ipam.states.${state}`)}</dt>
              <dd className="tabular-nums text-fg">{usage.used_by_state?.[state] ?? 0}</dd>
            </div>
          ))}
        {!usage.is_pool &&
          IP_ADDRESS_TYPES.map((type) => (
            <div key={type} className="contents">
              <dt className="text-fg-muted">{t(`ipam.types.${type}`)}</dt>
              <dd className="tabular-nums text-fg">{usage.used_by_type?.[type] ?? 0}</dd>
            </div>
          ))}
      </dl>
    </div>
  );
}

/**
 * SubnetActions are the write actions of ipam:write: the next free address
 * and the reservation (not for a pool, it has no hosts of its own) and the
 * next free child subnet of a prefix length.
 */
function SubnetActions({
  subnet,
  address,
  setAddress,
}: {
  subnet: Subnet;
  address: string;
  setAddress: (address: string) => void;
}) {
  const { t } = useTranslation();
  const { allocate, allocateSubnet, reserve } = useSubnetActions(subnet.id);
  const [status, setStatus] = useState<{ text: string; error?: boolean; link?: string }>();
  const [prefix, setPrefix] = useState('');
  const [prefixError, setPrefixError] = useState<string>();
  const [addressError, setAddressError] = useState<string>();
  const [creating, setCreating] = useState(false);

  const problem = (err: unknown, fallback: string) => {
    if (err instanceof ApiError && err.type === PROBLEM_SUBNET_EXHAUSTED) {
      return t('ipam.actions.exhausted');
    }
    if (err instanceof ApiError && err.status === 403) return t('ipam.actions.forbidden');
    return err instanceof ApiError && err.detail ? err.detail : fallback;
  };

  const onAllocate = () => {
    setStatus(undefined);
    allocate.mutate(
      { type: 'static' },
      {
        onSuccess: (ip) =>
          setStatus({ text: t('ipam.actions.allocated', { address: ip.address }) }),
        onError: (err) =>
          setStatus({ text: problem(err, t('ipam.actions.allocateFailed')), error: true }),
      },
    );
  };

  const onAllocateSubnet = (event: FormEvent) => {
    event.preventDefault();
    setStatus(undefined);
    setPrefixError(undefined);
    const length = Number(prefix);
    if (!Number.isInteger(length) || length < 1 || length > 128) {
      setPrefixError(t('ipam.actions.prefixInvalid'));
      return;
    }
    allocateSubnet.mutate(
      { prefix_length: length, is_pool: false },
      {
        onSuccess: (child) =>
          setStatus({
            text: t('ipam.actions.subnetAllocated', { cidr: child.cidr }),
            link: `/ipam/subnets/${child.id}`,
          }),
        onError: (err) => {
          if (err instanceof ApiError && err.field === 'prefix_length') {
            setPrefixError(err.detail ?? t('ipam.actions.prefixInvalid'));
            return;
          }
          setStatus({ text: problem(err, t('ipam.actions.allocateFailed')), error: true });
        },
      },
    );
  };

  const onReserve = (event: FormEvent) => {
    event.preventDefault();
    setStatus(undefined);
    setAddressError(undefined);
    if (!address.trim()) {
      setAddressError(t('ipam.actions.addressRequired'));
      return;
    }
    reserve.mutate(
      { address: address.trim() },
      {
        onSuccess: (ip) => {
          setAddress('');
          setStatus({ text: t('ipam.actions.reserved', { address: ip.address }) });
        },
        onError: (err) => {
          if (err instanceof ApiError && (err.status === 409 || err.field === 'address')) {
            setAddressError(
              err.status === 409 ? t('ipam.actions.addressTaken') : (err.detail ?? ''),
            );
            return;
          }
          setStatus({ text: problem(err, t('ipam.actions.reserveFailed')), error: true });
        },
      },
    );
  };

  return (
    <Card className="space-y-4 p-4">
      <h2 className="text-base font-semibold text-fg">{t('ipam.actions.title')}</h2>
      <div className="flex flex-wrap items-end gap-6">
        {!subnet.is_pool && (
          <Button icon={<Sparkles />} loading={allocate.isPending} onClick={onAllocate}>
            {t('ipam.actions.allocate')}
          </Button>
        )}
        {!subnet.is_pool && (
          <Button variant="secondary" icon={<Plus />} onClick={() => setCreating(true)}>
            {t('ipam.addresses.create')}
          </Button>
        )}
        <form onSubmit={onAllocateSubnet} className="flex items-end gap-2" noValidate>
          <Input
            label={t('ipam.actions.prefixLength')}
            type="number"
            inputMode="numeric"
            min={1}
            max={128}
            value={prefix}
            onChange={(event) => setPrefix(event.target.value)}
            error={prefixError}
            className="w-28"
          />
          <Button
            type="submit"
            variant="secondary"
            icon={<Plus />}
            loading={allocateSubnet.isPending}
          >
            {t('ipam.actions.allocateSubnet')}
          </Button>
        </form>
        {!subnet.is_pool && (
          <form
            onSubmit={onReserve}
            className="flex items-end gap-2"
            noValidate
            id="subnet-reserve-form"
          >
            <Input
              id="subnet-reserve-address"
              label={t('ipam.actions.reserveAddress')}
              value={address}
              onChange={(event) => setAddress(event.target.value)}
              error={addressError}
              placeholder={subnet.cidr.split('/')[0]}
            />
            <Button type="submit" variant="secondary" loading={reserve.isPending}>
              {t('ipam.actions.reserve')}
            </Button>
          </form>
        )}
      </div>
      {status && (
        <p
          role={status.error ? 'alert' : 'status'}
          className={cn('text-sm', status.error ? 'text-red-700 dark:text-red-300' : 'text-fg')}
        >
          {status.text}{' '}
          {status.link && (
            <Link to={status.link} className="text-accent underline underline-offset-2">
              {t('ipam.actions.open')}
            </Link>
          )}
        </p>
      )}
      {creating && (
        <AddressDialog
          open
          onOpenChange={(open) => !open && setCreating(false)}
          subnet={subnet}
          onSaved={(ip) =>
            setStatus({ text: t('ipam.addresses.created', { address: ip.address }) })
          }
        />
      )}
    </Card>
  );
}

function Addresses({
  subnet,
  canWrite,
  onPickFree,
}: {
  subnet: Subnet;
  canWrite: boolean;
  /** A free cell of the map was chosen: fill the reservation form. */
  onPickFree: (ip: string) => void;
}) {
  const { t } = useTranslation();
  const [params] = useSearchParams();
  const marked = params.get('address') ?? undefined;
  const [offset, setOffset] = useState(0);
  const page = useSubnetAddressPage(subnet.id, offset);
  // The search hit is read on its own: in a large subnet it may lie on a
  // later page than the loaded one.
  const hitQuery = useIPAddress(marked);
  const hit = hitQuery.data?.subnet_id === subnet.id ? hitQuery.data : undefined;
  const showMap = !subnet.is_pool && mapSupported(subnet.cidr);
  const all = useAllSubnetAddresses(subnet.id, showMap);
  const { release } = useSubnetActions(subnet.id);
  const [chosen, setChosen] = useState<MapCell>();
  const [releaseError, setReleaseError] = useState<string>();
  // The address the AddressDialog shows (edit, or read only without ipam:write).
  const [opened, setOpened] = useState<IPAddress>();
  const rows = page.data?.data ?? [];
  const total = page.data?.total ?? 0;
  const hitOffPage = hit !== undefined && !rows.some((ip) => ip.id === hit.id);
  const mapAddresses = useMemo(() => all.data ?? [], [all.data]);

  const onRelease = (ip: IPAddress) => {
    setReleaseError(undefined);
    release.mutate(ip.id, {
      onError: (err) =>
        setReleaseError(orgReadOnlyMessage(err) ?? t('ipam.addresses.releaseFailed')),
    });
  };

  const renderRow = (ip: IPAddress, offPage: boolean) => (
    <tr
      key={ip.id}
      className={cn(trClasses, marked === ip.id && 'bg-accent/10')}
      aria-current={marked === ip.id ? 'true' : undefined}
      data-testid={`address-row-${ip.id}`}
    >
      <td className={tdMonoClasses}>
        <button
          type="button"
          onClick={() => setOpened(ip)}
          aria-label={t(canWrite ? 'ipam.addresses.editLabel' : 'ipam.addresses.viewLabel', {
            address: ip.address,
          })}
          className="text-accent underline underline-offset-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
        >
          {ip.address}
        </button>
        {offPage && (
          <span className="ml-2 font-sans">
            <Badge>{t('ipam.addresses.searchHit')}</Badge>
          </span>
        )}
      </td>
      <td className={tdClasses}>
        {ip.state ? t(`ipam.states.${ip.state}`) : t('ipam.detail.none')}
      </td>
      <td className={tdClasses}>{t(`ipam.types.${ip.type}`)}</td>
      <td className={tdClasses}>{t(`ipam.roles.${ip.role}`)}</td>
      <td className={tdClasses}>{ip.is_gateway ? t('ipam.addresses.isGateway') : ''}</td>
      <td className={tdClasses}>
        {ip.dns_name}
        {ip.pinned && (
          <span className="ml-2">
            <Badge>{t('ipam.addresses.pinned')}</Badge>
          </span>
        )}
      </td>
      <td className={tdClasses}>
        {ip.ci_id ? (
          <Link to={`/cmdb/${ip.ci_id}`} className="text-accent underline underline-offset-2">
            {ip.interface_id ? t('ipam.addresses.deviceInterface') : t('ipam.addresses.deviceLink')}
          </Link>
        ) : (
          ''
        )}
      </td>
      {canWrite && (
        <td className={tdClasses}>
          {ip.type === 'reserved' && (
            <Button
              variant="ghost"
              size="sm"
              loading={release.isPending && release.variables === ip.id}
              onClick={() => onRelease(ip)}
              aria-label={t('ipam.addresses.releaseLabel', {
                address: ip.address,
              })}
            >
              {t('ipam.addresses.release')}
            </Button>
          )}
        </td>
      )}
    </tr>
  );

  return (
    <>
      {!subnet.is_pool && (
        <Card className="space-y-3 p-4">
          <h2 className="text-base font-semibold text-fg">{t('ipam.map.title')}</h2>
          {showMap ? (
            all.isError ? (
              <p role="alert" className="text-sm text-red-700 dark:text-red-300">
                {t('ipam.addresses.loadFailed')}
              </p>
            ) : all.data ? (
              <>
                <AddressMap
                  cidr={subnet.cidr}
                  gateway={subnet.gateway}
                  addresses={mapAddresses}
                  selected={chosen?.ip ?? hit?.address}
                  onSelect={(cell) => {
                    setChosen(cell);
                    if (canWrite && cell.kind === 'free') onPickFree(cell.ip);
                  }}
                />
                {chosen && (
                  <p role="status" className="text-sm text-fg">
                    {chosen.ip}: {t(`ipam.map.kinds.${chosen.kind}`)}
                    {chosen.address?.dns_name ? ` – ${chosen.address.dns_name}` : ''}
                    {canWrite && chosen.kind === 'free' && (
                      <span className="ml-2 text-fg-muted">{t('ipam.map.reserveHint')}</span>
                    )}
                  </p>
                )}
              </>
            ) : (
              <p className="text-sm text-fg-muted">{t('ipam.detail.loading')}</p>
            )
          ) : (
            <p className="text-sm text-fg-muted">{t('ipam.map.listOnly')}</p>
          )}
        </Card>
      )}
      <Card className="overflow-hidden p-0">
        <h2 className="px-4 pt-4 text-base font-semibold text-fg">
          {t('ipam.addresses.title', { count: total })}
        </h2>
        {page.isError ? (
          <p role="alert" className="p-4 text-sm text-red-700 dark:text-red-300">
            {t('ipam.addresses.loadFailed')}
          </p>
        ) : rows.length === 0 && !hitOffPage ? (
          <p className="p-4 text-sm text-fg-muted">
            {page.isPending ? t('ipam.detail.loading') : t('ipam.addresses.empty')}
          </p>
        ) : (
          <ScrollRegion label={t('ipam.addresses.label')} className="mt-3">
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('ipam.addresses.address')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.addresses.state')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.addresses.type')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.addresses.role')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.addresses.gateway')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.addresses.dnsName')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ipam.addresses.device')}
                  </th>
                  {canWrite && (
                    <th scope="col" className={thClasses}>
                      <span className="sr-only">{t('ipam.addresses.actions')}</span>
                    </th>
                  )}
                </tr>
              </thead>
              <tbody>
                {hitOffPage && renderRow(hit, true)}
                {rows.map((ip) => renderRow(ip, false))}
              </tbody>
            </table>
          </ScrollRegion>
        )}
        {releaseError && (
          <p role="alert" className="px-4 pb-2 text-sm text-red-700 dark:text-red-300">
            {releaseError}
          </p>
        )}
        {(offset > 0 || page.data?.has_more) && (
          <div className="flex items-center justify-end gap-2 p-3">
            <Button
              variant="secondary"
              size="sm"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - API_MAX_LIMIT))}
            >
              {t('ipam.addresses.previous')}
            </Button>
            <span className="text-xs tabular-nums text-fg-muted">
              {t('ipam.addresses.range', {
                from: offset + 1,
                to: offset + rows.length,
                total,
              })}
            </span>
            <Button
              variant="secondary"
              size="sm"
              disabled={!page.data?.has_more}
              onClick={() => setOffset(offset + API_MAX_LIMIT)}
            >
              {t('ipam.addresses.next')}
            </Button>
          </div>
        )}
      </Card>
      {opened && (
        <AddressDialog
          open
          onOpenChange={(open) => !open && setOpened(undefined)}
          subnet={subnet}
          address={opened}
        />
      )}
    </>
  );
}

/** SubnetHistory shows the change history of the subnet once it is opened. */
function SubnetHistory({ subnet }: { subnet: Subnet }) {
  const { t } = useTranslation();
  const [shown, setShown] = useState(false);
  const panelId = `subnet-history-${subnet.id}`;
  return (
    <Card className="space-y-3 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold text-fg">{t('ipam.detail.historyTitle')}</h2>
        <Button
          variant="secondary"
          size="sm"
          icon={<History />}
          aria-expanded={shown}
          aria-controls={panelId}
          onClick={() => setShown((v) => !v)}
        >
          {shown ? t('ipam.detail.historyHide') : t('ipam.detail.historyShow')}
        </Button>
      </div>
      <div id={panelId} hidden={!shown}>
        {shown && <HistoryList kind="subnet" id={subnet.id} />}
      </div>
    </Card>
  );
}
