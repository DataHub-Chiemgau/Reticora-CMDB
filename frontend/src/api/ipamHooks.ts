/**
 * ipamHooks — TanStack Query hooks of the IPAM page (WP-915, Konvention 27):
 * sections, their rights per team, one level of the subnet tree and the
 * usage of a subnet. The VRF hooks stay in hooks.ts until WP-918.
 * WP-916 adds the subnet detail: the subnet with its parent chain, its
 * addresses (one page for the list, all for the address map), the
 * allocation of the next free address and subnet, reservations and the
 * subnet form.
 */
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ipamApi,
  type IPAllocate,
  type IPReservationCreate,
  type SubnetAllocate,
  type SubnetCreate,
  type SubnetUpdate,
  type IPAMSectionCreate,
  type IPAMSectionLevel,
  type IPAMSectionUpdate,
  type SubnetQuery,
} from './cmdb';

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
