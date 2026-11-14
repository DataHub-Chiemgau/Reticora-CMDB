/**
 * monitoringSettingsHooks — TanStack Query hooks of the monitoring settings
 * (PRB-01, CRD-04, WP-1080, Konvention 27): the probe type register, the
 * profiles per level (org, client, group, ci), the monitoring groups with
 * their members, the bindings of credentials to a group (target_kind
 * device_group), the probes of a CI with their effective values and the
 * organization setting monitoring.auto_ping (WP-949).
 */
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  fetchAPI,
  monitoringApi,
  type MonitoringGroupCreate,
  type MonitoringGroupUpdate,
  type MonitoringProfileCreate,
  type MonitoringProfileQuery,
  type ProbeOverrides,
} from './client';
import {
  credentialBindingApi,
  type CredentialBindingCreate,
  type CredentialBindingUpdate,
} from './cmdb';
import type { components } from './generated/schema';

type Organization = components['schemas']['Organization'];

const ROOT = ['monitoring-settings'] as const;

/** The probe type register (monitoring:read); enabled holds the request back. */
export function useProbeTypes(enabled = true) {
  return useQuery({
    queryKey: [...ROOT, 'probe-types'],
    queryFn: () => monitoringApi.probeTypes(),
    enabled,
    retry: false,
    staleTime: 5 * 60_000,
  });
}

/** The profiles of a filter (every page). */
export function useMonitoringProfiles(query: MonitoringProfileQuery, enabled = true) {
  return useQuery({
    queryKey: [...ROOT, 'profiles', query],
    queryFn: () => monitoringApi.profiles(query),
    enabled,
    retry: false,
  });
}

/**
 * Saving a profile: create, replace the params or delete it. Every write
 * invalidates the profiles and the effective values of the probes.
 */
export function useProfileMutations() {
  const qc = useQueryClient();
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: [...ROOT, 'profiles'] });
    void qc.invalidateQueries({ queryKey: [...ROOT, 'effective'] });
  };
  return {
    create: useMutation({
      mutationFn: (data: MonitoringProfileCreate) => monitoringApi.createProfile(data),
      onSuccess: invalidate,
    }),
    update: useMutation({
      mutationFn: ({ id, params }: { id: string; params: ProbeOverrides }) =>
        monitoringApi.updateProfile(id, params),
      onSuccess: invalidate,
    }),
    remove: useMutation({
      mutationFn: (id: string) => monitoringApi.deleteProfile(id),
      onSuccess: invalidate,
    }),
  };
}

/** Every monitoring group in the caller's scope. */
export function useMonitoringGroups(enabled = true) {
  return useQuery({
    queryKey: [...ROOT, 'groups'],
    queryFn: () => monitoringApi.groups(),
    enabled,
    retry: false,
  });
}

/** The profile of a monitoring group the CI is a member of, with its rank. */
export interface CIGroupProfile {
  groupId: string;
  name: string;
  priority: number;
  params: ProbeOverrides;
}

/**
 * useCIGroupProfiles returns the profiles of the probe type of the
 * monitoring groups the CI is a member of (PRB-01, E-178), lowest rank
 * first: the higher priority wins, on a tie the smaller name, then the
 * smaller id (probe.rankedGroups), so the last layer with a value decides.
 * Only groups with a profile of the type are asked for their members;
 * a group of another client than the CI's cannot hold it.
 */
export function useCIGroupProfiles(ciId: string, clientId: string | undefined, probeType?: string) {
  const profiles = useMonitoringProfiles({ level: 'group', probe_type: probeType }, !!probeType);
  const groups = useMonitoringGroups(!!probeType);
  const byId = new Map((groups.data ?? []).map((g) => [g.id, g]));
  const candidates = (profiles.data ?? [])
    .map((p) => ({ profile: p, group: p.level_id ? byId.get(p.level_id) : undefined }))
    .filter(
      (x): x is { profile: (typeof x)['profile']; group: NonNullable<(typeof x)['group']> } =>
        !!x.group && (!x.group.client_id || x.group.client_id === clientId),
    );
  const members = useQueries({
    queries: candidates.map(({ group }) => ({
      queryKey: [...ROOT, 'groups', group.id, 'members'],
      queryFn: () => monitoringApi.groupMembers(group.id),
      retry: false,
    })),
  });
  const isPending =
    !!probeType && (profiles.isPending || groups.isPending || members.some((m) => m.isPending));
  const data: CIGroupProfile[] = candidates
    .filter((_, i) => (members[i]?.data ?? []).some((m) => m.ci_id === ciId))
    .map(({ profile, group }) => ({
      groupId: group.id,
      name: group.name,
      priority: group.priority,
      params: profile.params,
    }))
    .sort((a, b) =>
      a.priority !== b.priority
        ? a.priority - b.priority
        : a.name !== b.name
          ? b.name.localeCompare(a.name)
          : b.groupId.localeCompare(a.groupId),
    );
  return { data, isPending };
}

/** Creating, changing and deleting a monitoring group (monitoring:write). */
export function useGroupMutations() {
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: [...ROOT] });
  return {
    create: useMutation({
      mutationFn: (data: MonitoringGroupCreate) => monitoringApi.createGroup(data),
      onSuccess: invalidate,
    }),
    update: useMutation({
      mutationFn: ({ id, data }: { id: string; data: MonitoringGroupUpdate }) =>
        monitoringApi.updateGroup(id, data),
      onSuccess: invalidate,
    }),
    remove: useMutation({
      mutationFn: (id: string) => monitoringApi.deleteGroup(id),
      onSuccess: invalidate,
    }),
  };
}

/** The member CIs of a group. */
export function useGroupMembers(groupId: string | undefined) {
  return useQuery({
    queryKey: [...ROOT, 'groups', groupId, 'members'],
    queryFn: () => monitoringApi.groupMembers(groupId ?? ''),
    enabled: !!groupId,
    retry: false,
  });
}

/** Adding and removing members of a group (monitoring:write). */
export function useGroupMemberMutations(groupId: string) {
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: [...ROOT, 'groups'] });
  return {
    add: useMutation({
      mutationFn: (ciIds: string[]) => monitoringApi.addGroupMembers(groupId, ciIds),
      onSuccess: invalidate,
    }),
    remove: useMutation({
      mutationFn: (ciId: string) => monitoringApi.removeGroupMember(groupId, ciId),
      onSuccess: invalidate,
    }),
  };
}

/** The bindings of credentials to a monitoring group (target_kind device_group, CRD-04). */
export function useGroupCredentialBindings(groupId: string, enabled = true) {
  return useQuery({
    queryKey: [...ROOT, 'groups', groupId, 'credential-bindings'],
    queryFn: () => credentialBindingApi.list({ target_kind: 'device_group', target_id: groupId }),
    enabled,
    retry: false,
  });
}

/** Binding, changing and removing credentials of a group (credential:bind). */
export function useGroupCredentialMutations(groupId: string) {
  const qc = useQueryClient();
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: [...ROOT, 'groups', groupId, 'credential-bindings'] });
    void qc.invalidateQueries({ queryKey: ['credentials'] });
  };
  return {
    create: useMutation({
      mutationFn: (data: Omit<CredentialBindingCreate, 'target_kind' | 'target_id'>) =>
        credentialBindingApi.create({ ...data, target_kind: 'device_group', target_id: groupId }),
      onSuccess: invalidate,
    }),
    update: useMutation({
      mutationFn: ({ id, data }: { id: string; data: CredentialBindingUpdate }) =>
        credentialBindingApi.update(id, data),
      onSuccess: invalidate,
    }),
    remove: useMutation({
      mutationFn: (id: string) => credentialBindingApi.remove(id),
      onSuccess: invalidate,
    }),
  };
}

/** The probes of a CI; enabled holds the request back until the section opens. */
export function useCIProbes(ciId: string, enabled: boolean) {
  return useQuery({
    queryKey: [...ROOT, 'ci', ciId, 'probes'],
    queryFn: () => monitoringApi.ciProbes(ciId),
    enabled,
    retry: false,
  });
}

/** The effective values of a probe with the origin of each field. */
export function useProbeEffective(probeId: string) {
  return useQuery({
    queryKey: [...ROOT, 'effective', probeId],
    queryFn: () => monitoringApi.probeEffective(probeId),
    retry: false,
  });
}

/** The organization with its settings (monitoring.auto_ping, WP-949). */
export function useOrganizationSettings(enabled = true) {
  return useQuery({
    queryKey: [...ROOT, 'org'],
    queryFn: () => fetchAPI<Organization>('/org'),
    enabled,
    retry: false,
  });
}

/**
 * Switching monitoring.auto_ping: sends the settings with only this key
 * changed, so every other key stays (PATCH /org, user:manage).
 */
export function useSetAutoPing() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ settings, value }: { settings: Record<string, unknown>; value: boolean }) => {
      const monitoring =
        typeof settings.monitoring === 'object' && settings.monitoring !== null
          ? (settings.monitoring as Record<string, unknown>)
          : {};
      return fetchAPI<Organization>('/org', {
        method: 'PATCH',
        body: JSON.stringify({
          settings: { ...settings, monitoring: { ...monitoring, auto_ping: value } },
        }),
      });
    },
    onSuccess: (org) => {
      qc.setQueryData([...ROOT, 'org'], org);
      void qc.invalidateQueries({ queryKey: ['scope', 'org'] });
    },
  });
}
