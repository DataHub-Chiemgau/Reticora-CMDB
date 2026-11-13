/**
 * ipamHooks — TanStack Query hooks of the IPAM page (WP-915, Konvention 27):
 * sections, their rights per team, one level of the subnet tree and the
 * usage of a subnet. The VRF hooks stay in hooks.ts until WP-918.
 */
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ipamApi,
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
