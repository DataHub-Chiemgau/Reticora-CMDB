/**
 * ipamHooks — TanStack Query hooks of the IPAM page (WP-915, Konvention 27):
 * sections, their rights per team, one level of the subnet tree and the
 * usage of a subnet. useVrfs moved here from hooks.ts with WP-918.
 * WP-916 adds the subnet detail: the subnet with its parent chain, its
 * addresses (one page for the list, all for the address map), the
 * allocation of the next free address and subnet, reservations and the
 * subnet form. WP-917 adds the address dialog: saving an address, the
 * interfaces of the chosen device, the change history of an address or a
 * subnet and the open review items of an address. WP-919 adds the network
 * section of the CI detail: the addresses of a CI and the assignment of an
 * address from a free range.
 */
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { sessionApi, vrfApi, type PaginatedResponse } from './client';
import {
  ipamApi,
  type NameserverSetCreate,
  type NameserverSetUpdate,
  type VLANCreate,
  type VLANDomainCreate,
  type VLANDomainUpdate,
  type VLANUpdate,
  type VRFCreate,
  type VRFUpdate,
  type IPAllocate,
  type IPReservationCreate,
  type SubnetAllocate,
  type SubnetCreate,
  type SubnetUpdate,
  type IPAMSectionCreate,
  type IPAMSectionLevel,
  type IPAMSectionUpdate,
  type SubnetQuery,
  type IPAddress,
  type IPAddressCreate,
  type IPAddressUpdate,
  type IPAddressRole,
  ipamScanApi,
  type PTRStatus,
  type SubnetScanUpdate,
} from './cmdb';
import { fetchAllReviewItems, type ReviewItem } from '../hooks/useReviews';

const SECTIONS = ['ipam', 'sections'] as const;

export function useIPAMSections() {
  return useQuery({
    queryKey: SECTIONS,
    queryFn: () => ipamApi.listSections({ limit: 200 }),
  });
}

export function useCreateIPAMSection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: IPAMSectionCreate) => ipamApi.createSection(data),
    onSuccess: () => qc.invalidateQueries({ queryKey: SECTIONS }),
  });
}

export function useUpdateIPAMSection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: IPAMSectionUpdate }) =>
      ipamApi.updateSection(id, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: SECTIONS }),
  });
}

export function useDeleteIPAMSection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => ipamApi.deleteSection(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: SECTIONS }),
  });
}

const permissionsKey = (sectionId: string) => ['ipam', 'sections', sectionId, 'permissions'];

export function useIPAMSectionPermissions(sectionId: string | undefined) {
  return useQuery({
    queryKey: permissionsKey(sectionId ?? ''),
    queryFn: () => ipamApi.listPermissions(sectionId ?? ''),
    enabled: !!sectionId,
  });
}

/**
 * useIPAMSectionPermissionMutations adds, changes and removes the rights of
 * a section; each refreshes the rights and the sections (the first right
 * restricts the section, acl_enabled).
 */
export function useIPAMSectionPermissionMutations(sectionId: string) {
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: permissionsKey(sectionId) });
    void qc.invalidateQueries({ queryKey: SECTIONS, exact: true });
  };
  const create = useMutation({
    mutationFn: (data: { team_id: string; level: IPAMSectionLevel }) =>
      ipamApi.createPermission(sectionId, data),
    onSuccess: refresh,
  });
  const update = useMutation({
    mutationFn: ({ id, level }: { id: string; level: IPAMSectionLevel }) =>
      ipamApi.updatePermission(sectionId, id, level),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: (id: string) => ipamApi.deletePermission(sectionId, id),
    onSuccess: refresh,
  });
  return { create, update, remove };
}

/**
 * useSubnetLevel loads one level of the subnet tree lazily (parent_id root
 * or the id of the parent) with the filters of the page, one page of at
 * most API_MAX_LIMIT subnets per request; fetchNextPage loads more.
 */
export function useSubnetLevel(query: SubnetQuery, enabled = true) {
  return useInfiniteQuery({
    queryKey: ['ipam', 'subnets', query],
    queryFn: ({ pageParam }) => ipamApi.listSubnets({ ...query, offset: pageParam }),
    initialPageParam: 0,
    getNextPageParam: (last) =>
      last.has_more ? last.offset + (last.data?.length ?? 0) : undefined,
    enabled,
  });
}

/** useSubnetChain reads the subnets that contain an address (contains). */
export function useSubnetChain(query: SubnetQuery, enabled: boolean) {
  return useQuery({
    queryKey: ['ipam', 'subnets', 'contains', query],
    queryFn: () => ipamApi.listSubnets(query),
    enabled,
  });
}

export function useSubnetUsage(id: string) {
  return useQuery({
    queryKey: ['ipam', 'subnets', id, 'usage'],
    queryFn: () => ipamApi.subnetUsage(id),
  });
}

// ─── Subnet detail (IPM-19, WP-916) ─────────────────────────────────────────

const subnetKey = (id: string) => ['ipam', 'subnet', id] as const;

export function useSubnet(id: string) {
  return useQuery({ queryKey: subnetKey(id), queryFn: () => ipamApi.getSubnet(id) });
}

/**
 * useSubnetAncestors walks parent_id up to the root (at most 128 levels, the
 * longest IPv6 chain): the breadcrumb of the subnet detail, root first. A
 * parent outside the caller's scope has no parent_id, so the chain ends
 * there.
 */
export function useSubnetAncestors(parentId: string | undefined) {
  return useQuery({
    queryKey: ['ipam', 'subnet-ancestors', parentId ?? ''],
    queryFn: async () => {
      const chain = [];
      let next = parentId;
      for (let depth = 0; next && depth < 128; depth++) {
        const parent = await ipamApi.getSubnet(next);
        chain.unshift(parent);
        next = parent.parent_id;
      }
      return chain;
    },
    enabled: !!parentId,
  });
}

/** useSubnetAddressPage reads one page of the address list of a subnet. */
export function useSubnetAddressPage(id: string, offset: number) {
  return useQuery({
    queryKey: ['ipam', 'subnet', id, 'addresses', offset],
    queryFn: () => ipamApi.subnetAddresses(id, { offset }),
    placeholderData: (previous) => previous,
  });
}

/**
 * useIPAddress reads one address; the subnet detail marks a search hit
 * (?address=<id>) with it even when it is not on the loaded page.
 */
export function useIPAddress(id: string | undefined) {
  return useQuery({
    queryKey: ['ipam', 'ip', id ?? ''],
    queryFn: () => ipamApi.getIPAddress(id ?? ''),
    enabled: !!id,
  });
}

/** useAllSubnetAddresses reads every address of a subnet for the address map. */
export function useAllSubnetAddresses(id: string, enabled: boolean) {
  return useQuery({
    queryKey: ['ipam', 'subnet', id, 'addresses', 'all'],
    queryFn: () => ipamApi.allSubnetAddresses(id),
    enabled,
  });
}

/**
 * useSubnetActions are the write actions of the subnet detail; each
 * refreshes the subnet, its addresses, its usage and the tree.
 */
export function useSubnetActions(id: string) {
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['ipam', 'subnet', id] });
    void qc.invalidateQueries({ queryKey: ['ipam', 'subnets'] });
  };
  const allocate = useMutation({
    mutationFn: (data: IPAllocate) => ipamApi.allocateAddress(id, data),
    onSuccess: refresh,
  });
  const allocateSubnet = useMutation({
    mutationFn: (data: SubnetAllocate) => ipamApi.allocateSubnet(id, data),
    onSuccess: refresh,
  });
  const reserve = useMutation({
    mutationFn: (data: IPReservationCreate) => ipamApi.createReservation(id, data),
    onSuccess: refresh,
  });
  const release = useMutation({
    mutationFn: (ipId: string) => ipamApi.deleteReservation(id, ipId),
    onSuccess: refresh,
  });
  return { allocate, allocateSubnet, reserve, release };
}

/** useSaveSubnet creates (no id) or changes a subnet in the subnet form. */
export function useSaveSubnet() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id?: string; data: SubnetCreate | SubnetUpdate }) =>
      id ? ipamApi.updateSubnet(id, data) : ipamApi.createSubnet(data as SubnetCreate),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['ipam'] });
    },
  });
}

export function useDeleteSubnet() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => ipamApi.deleteSubnet(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['ipam'] });
    },
  });
}

export function useVRF(id: string | undefined) {
  return useQuery({
    queryKey: ['ipam', 'vrf', id ?? ''],
    queryFn: () => ipamApi.getVRF(id ?? ''),
    enabled: !!id,
  });
}

export function useVLAN(id: string | undefined) {
  return useQuery({
    queryKey: ['ipam', 'vlan', id ?? ''],
    queryFn: () => ipamApi.getVLAN(id ?? ''),
    enabled: !!id,
  });
}

/** useVLANOptions reads the VLANs of a site (or all) for the subnet form. */
export function useVLANOptions(siteId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['ipam', 'vlans', 'options', siteId ?? ''],
    queryFn: () => ipamApi.listVLANs(siteId ? { site_id: siteId } : {}),
    enabled,
  });
}

export function useNameserverSetOptions(enabled = true) {
  return useQuery({
    queryKey: ['ipam', 'nameserver-sets', 'options'],
    queryFn: () => ipamApi.listNameserverSets(),
    enabled,
  });
}

// ─── Tabs VLANs, VRFs and nameserver sets (IPM-19, WP-918) ──────────────────

/** The VRFs of the organization, the default first (TEN-10); the choices of forms and filters. */
export function useVrfs(enabled = true) {
  return useQuery({
    queryKey: ['vrfs', 'options'],
    queryFn: () => vrfApi.list({ limit: 200 }),
    enabled,
  });
}

/**
 * useOrgWideWriter reports whether the session has no client and no site
 * restriction (cls and sts null, GET /me): only such principals write VRFs,
 * VLAN domains and nameserver sets (E-09). undefined while it loads.
 */
export function useOrgWideWriter(): boolean | undefined {
  const session = useQuery({
    queryKey: ['me'],
    queryFn: () => sessionApi.me(),
    retry: false,
    staleTime: 60_000,
  });
  if (!session.data) return session.isError ? false : undefined;
  return (session.data.cls ?? null) === null && (session.data.sts ?? null) === null;
}

/** paged follows the offset pagination of a list, one page of API_MAX_LIMIT rows per request. */
function paged<T>(
  queryKey: readonly unknown[],
  fetchPage: (offset: number) => Promise<PaginatedResponse<T>>,
  enabled = true,
) {
  return {
    queryKey,
    queryFn: ({ pageParam }: { pageParam: number }) => fetchPage(pageParam),
    initialPageParam: 0,
    getNextPageParam: (last: PaginatedResponse<T>) =>
      last.has_more ? last.offset + (last.data?.length ?? 0) : undefined,
    enabled,
  };
}

const VLANS = ['ipam', 'vlans'] as const;
const DOMAINS = ['ipam', 'vlan-domains'] as const;
const VRFS = ['ipam', 'vrfs'] as const;
const NAMESERVERS = ['ipam', 'nameserver-sets'] as const;

/**
 * useVLANList lists the VLANs: of a domain over GET /vlan-domains/{id}/vlans
 * (GET /vlans has no domain filter), else GET /vlans with site_id.
 */
export function useVLANList(filter: { domainId?: string; siteId?: string }) {
  return useInfiniteQuery(
    paged([...VLANS, 'list', filter.domainId ?? '', filter.siteId ?? ''], (offset) =>
      filter.domainId
        ? ipamApi.listDomainVLANs(filter.domainId, { offset })
        : ipamApi.listVLANs({ site_id: filter.siteId, offset }),
    ),
  );
}

export function useVLANDomains() {
  return useInfiniteQuery(paged([...DOMAINS], (offset) => ipamApi.listVLANDomains({ offset })));
}

export function useVRFList() {
  return useInfiniteQuery(paged([...VRFS], (offset) => ipamApi.listVRFs({ offset })));
}

export function useNameserverSets() {
  return useInfiniteQuery(
    paged([...NAMESERVERS, 'list'], (offset) => ipamApi.listNameserverSets({ offset })),
  );
}

export function useVLANMutations() {
  const qc = useQueryClient();
  // The detail ['ipam','vlan',id] of the subnet detail refreshes as well.
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: VLANS });
    void qc.invalidateQueries({ queryKey: ['ipam', 'vlan'] });
  };
  return {
    create: useMutation({
      mutationFn: (d: VLANCreate) => ipamApi.createVLAN(d),
      onSuccess: refresh,
    }),
    update: useMutation({
      mutationFn: ({ id, data }: { id: string; data: VLANUpdate }) => ipamApi.updateVLAN(id, data),
      onSuccess: refresh,
    }),
    remove: useMutation({ mutationFn: (id: string) => ipamApi.deleteVLAN(id), onSuccess: refresh }),
  };
}

export function useVLANDomainMutations() {
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: DOMAINS });
    void qc.invalidateQueries({ queryKey: VLANS });
    // Deleting a domain clears domain_id of its VLANs.
    void qc.invalidateQueries({ queryKey: ['ipam', 'vlan'] });
  };
  return {
    create: useMutation({
      mutationFn: (d: VLANDomainCreate) => ipamApi.createVLANDomain(d),
      onSuccess: refresh,
    }),
    update: useMutation({
      mutationFn: ({ id, data }: { id: string; data: VLANDomainUpdate }) =>
        ipamApi.updateVLANDomain(id, data),
      onSuccess: refresh,
    }),
    remove: useMutation({
      mutationFn: (id: string) => ipamApi.deleteVLANDomain(id),
      onSuccess: refresh,
    }),
  };
}

export function useVRFMutations() {
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: VRFS });
    void qc.invalidateQueries({ queryKey: ['vrfs'] });
    // The detail ['ipam','vrf',id] of the subnet detail (name, RD, targets).
    void qc.invalidateQueries({ queryKey: ['ipam', 'vrf'] });
  };
  return {
    create: useMutation({ mutationFn: (d: VRFCreate) => ipamApi.createVRF(d), onSuccess: refresh }),
    update: useMutation({
      mutationFn: ({ id, data }: { id: string; data: VRFUpdate }) => ipamApi.updateVRF(id, data),
      onSuccess: refresh,
    }),
    remove: useMutation({ mutationFn: (id: string) => ipamApi.deleteVRF(id), onSuccess: refresh }),
  };
}

export function useNameserverSetMutations() {
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: NAMESERVERS });
  return {
    create: useMutation({
      mutationFn: (d: NameserverSetCreate) => ipamApi.createNameserverSet(d),
      onSuccess: refresh,
    }),
    update: useMutation({
      mutationFn: ({ id, data }: { id: string; data: NameserverSetUpdate }) =>
        ipamApi.updateNameserverSet(id, data),
      onSuccess: refresh,
    }),
    remove: useMutation({
      mutationFn: (id: string) => ipamApi.deleteNameserverSet(id),
      onSuccess: refresh,
    }),
  };
}

// ─── Address dialog (IPM-13, IPM-14, IPM-19, WP-917) ────────────────────────

/**
 * IPAddressPinFailed reports that a new address was created but the pin that
 * follows as PATCH failed; created is the stored address (unpinned).
 */
export class IPAddressPinFailed extends Error {
  readonly created: IPAddress;

  constructor(created: IPAddress) {
    super('pin failed');
    this.name = 'IPAddressPinFailed';
    this.created = created;
  }
}

/**
 * useSaveIPAddress creates (no id) or changes an address; it refreshes the
 * subnet detail, the address and the tree (usage). POST takes no pin: with
 * pin set, a create is followed by PATCH pinned inside the same mutation, so
 * isPending and the refresh cover both calls. A failed pin rejects with
 * IPAddressPinFailed carrying the created address.
 */
export function useSaveIPAddress() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      data,
      pin,
    }: {
      id?: string;
      data: IPAddressCreate | IPAddressUpdate;
      pin?: boolean;
    }): Promise<IPAddress> => {
      if (id) return ipamApi.updateIPAddress(id, data);
      const created = await ipamApi.createIPAddress(data as IPAddressCreate);
      if (!pin) return created;
      try {
        return await ipamApi.updateIPAddress(created.id, { pinned: true });
      } catch {
        throw new IPAddressPinFailed(created);
      }
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ['ipam'] });
    },
  });
}

/**
 * useCIInterfaces reads the interfaces of the chosen device; nothing without
 * a CI or while enabled is false (the network section without ipam:read).
 */
export function useCIInterfaces(ciId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['ipam', 'ci-interfaces', ciId ?? ''],
    queryFn: () => ipamApi.ciInterfaces(ciId ?? ''),
    enabled: enabled && !!ciId,
  });
}

// ─── Network section of the CI detail (IPM-13, UI-14, WP-919) ───────────────

/** useCIAddresses reads every address bound to a CI (GET /cis/{id}/addresses). */
export function useCIAddresses(ciId: string, enabled = true) {
  return useQuery({
    queryKey: ['ipam', 'ci-addresses', ciId],
    queryFn: () => ipamApi.ciAddresses(ciId),
    enabled: enabled && !!ciId,
  });
}

/**
 * CIAddressAssignFailed reports that the address was allocated but the
 * binding to the CI (PATCH /ips/{id}) failed; cause is the error of the
 * PATCH and released whether DELETE /ips/{id} gave the address back.
 */
export class CIAddressAssignFailed extends Error {
  readonly cause: unknown;
  readonly released: boolean;

  constructor(cause: unknown, released: boolean) {
    super('binding failed');
    this.name = 'CIAddressAssignFailed';
    this.cause = cause;
    this.released = released;
  }
}

export interface CIAddressAssign {
  subnetId: string;
  ciId: string;
  role: IPAddressRole;
  type: 'static' | 'reserved';
  interfaceId?: string;
  dnsName?: string;
}

/**
 * useAssignCIAddress allocates the next free address of a subnet (POST
 * /subnets/{id}/allocate with interface, type and DNS name) and binds it to
 * the CI with its role (PATCH /ips/{id} with ci_id and role). A failed PATCH
 * releases the allocated address again (DELETE /ips/{id}) and rejects with
 * CIAddressAssignFailed. Interfaces, addresses and the IPAM views refresh
 * either way.
 */
export function useAssignCIAddress() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: CIAddressAssign): Promise<IPAddress> => {
      const data: IPAllocate = { type: input.type };
      if (input.interfaceId) data.interface_id = input.interfaceId;
      if (input.dnsName) data.dns_name = input.dnsName;
      const allocated = await ipamApi.allocateAddress(input.subnetId, data);
      try {
        return await ipamApi.updateIPAddress(allocated.id, {
          ci_id: input.ciId,
          role: input.role,
        });
      } catch (err) {
        let released = true;
        try {
          await ipamApi.deleteIPAddress(allocated.id);
        } catch {
          released = false;
        }
        throw new CIAddressAssignFailed(err, released);
      }
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ['ipam'] });
    },
  });
}

/** IPAMHistoryKind is the object whose history HistoryList shows. */
export type IPAMHistoryKind = 'ip' | 'subnet';

/** useIPAMHistory reads one page of GET /ips/{id}/history or /subnets/{id}/history. */
export function useIPAMHistory(kind: IPAMHistoryKind, id: string, offset: number, enabled = true) {
  return useQuery({
    queryKey: ['ipam', 'history', kind, id, offset],
    queryFn: () =>
      kind === 'ip' ? ipamApi.ipHistory(id, { offset }) : ipamApi.subnetHistory(id, { offset }),
    placeholderData: (previous) => previous,
    enabled: enabled && !!id,
  });
}

/**
 * addressReviewMatches reports whether an open review item concerns the
 * address: a duplicate_ip item by its object key ip:<vrf>/<address> (or
 * address and VRF of its payload), an ip_binding_conflict item by the
 * address row of its payload (ip_address_id) or its object key
 * ipbind:<id>/<interface>.
 */
export function addressReviewMatches(
  item: Pick<ReviewItem, 'kind' | 'object_key' | 'payload'>,
  address: { id: string; address: string; vrf_id: string },
): boolean {
  const p = item.payload ?? {};
  if (item.kind === 'duplicate_ip') {
    return (
      item.object_key === `ip:${address.vrf_id}/${address.address}` ||
      (p.address === address.address && p.vrf_id === address.vrf_id)
    );
  }
  if (item.kind === 'ip_binding_conflict') {
    return (
      p.ip_address_id === address.id || (item.object_key ?? '').startsWith(`ipbind:${address.id}/`)
    );
  }
  return false;
}

/**
 * useAddressReviews reads the open duplicate_ip and ip_binding_conflict
 * items (GET /review-items?status=open&kind=…, all pages) and keeps those of
 * the address. GET /review-items filters neither by object key nor by
 * payload, so the match runs here. enabled holds the request back without
 * the right to read review items (discovery:read).
 */
export function useAddressReviews(
  address: { id: string; address: string; vrf_id: string } | undefined,
  enabled = true,
) {
  return useQuery({
    queryKey: ['review-items', 'address', address?.id ?? ''],
    queryFn: async () => {
      const [duplicates, conflicts] = await Promise.all([
        fetchAllReviewItems({ status: 'open', kind: 'duplicate_ip' }),
        fetchAllReviewItems({ status: 'open', kind: 'ip_binding_conflict' }),
      ]);
      return [...duplicates, ...conflicts].filter(
        (item) => address !== undefined && addressReviewMatches(item, address),
      );
    },
    enabled: enabled && !!address,
  });
}

// ─── Subnet scan and PTR check (IPM-11, IPM-12, WP-1065) ────────────────────

/** The scan settings, last run and counters of a subnet; held back while enabled is false. */
export function useSubnetScan(subnetId: string, enabled = true) {
  return useQuery({
    queryKey: ['ipam', 'subnet', subnetId, 'scan'],
    queryFn: () => ipamScanApi.getScan(subnetId),
    enabled,
    retry: false,
  });
}

/** The collector groups the scan of a subnet may use. */
export function useCollectorGroups(enabled = true) {
  return useQuery({
    queryKey: ['collector-groups', 'all'],
    queryFn: () => ipamScanApi.collectorGroups(),
    enabled,
    retry: false,
    staleTime: 60_000,
  });
}

/** The hosts the scan of a subnet found (origin scan, type discovered). */
export function useScanNewHosts(subnetId: string, enabled = true) {
  return useQuery({
    queryKey: ['ipam', 'subnet', subnetId, 'scan', 'new-hosts'],
    queryFn: () => ipamScanApi.newHosts(subnetId),
    enabled,
    retry: false,
  });
}

/** Saving the scan settings (PATCH /subnets/{id}/scan, ipam:write). */
export function useUpdateSubnetScan(subnetId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: SubnetScanUpdate) => ipamScanApi.updateScan(subnetId, data),
    onSuccess: (scan) => {
      qc.setQueryData(['ipam', 'subnet', subnetId, 'scan'], scan);
      void qc.invalidateQueries({ queryKey: ['ipam', 'subnet', subnetId, 'scan', 'new-hosts'] });
    },
  });
}

/**
 * Adopting a found host: type static and pinned (PATCH /ips/{id}); it
 * leaves the list "Neu gefunden".
 */
export function useAdoptScanHost(subnetId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => ipamApi.updateIPAddress(id, { type: 'static', pinned: true }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['ipam', 'subnet', subnetId] });
    },
  });
}

/** The PTR deviations of the filter, page by page over next_cursor (API-02). */
export function usePtrMismatches(
  filter: { section_id?: string; subnet_id?: string; status: PTRStatus },
  enabled = true,
) {
  return useInfiniteQuery({
    queryKey: ['ipam', 'ptr-mismatches', filter],
    queryFn: ({ pageParam }) =>
      ipamScanApi.ptrMismatches({ ...filter, cursor: pageParam || undefined }),
    initialPageParam: '',
    getNextPageParam: (page) => page.next_cursor || undefined,
    enabled,
    retry: false,
  });
}
