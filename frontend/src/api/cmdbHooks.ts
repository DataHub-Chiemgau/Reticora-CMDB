/**
 * cmdbHooks — TanStack Query hooks for the enterprise CMDB extension API.
 * Mirrors the conventions of api/hooks.ts (query keys, invalidation).
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ciTypeApi,
  ciFieldApi,
  relationshipTypeApi,
  lifecycleApi,
  locationApi,
  clientApi,
  inventoryApi,
  reservationApi,
  compositionApi,
  overrideApi,
  historyApi,
  impactApi,
  savedViewApi,
  type CITypeCreateRequest,
} from './cmdb';
import type { FieldDefinition } from '../lib/fieldmeta';

export function useCITypes(
  params: { include_inactive?: boolean; category?: string; search?: string } = {},
) {
  return useQuery({ queryKey: ['ci-types', params], queryFn: () => ciTypeApi.list(params) });
}

export function useCIType(id: string | undefined) {
  return useQuery({ queryKey: ['ci-types', id], queryFn: () => ciTypeApi.get(id!), enabled: !!id });
}

export function useCITypeTemplates() {
  return useQuery({ queryKey: ['ci-types', 'templates'], queryFn: () => ciTypeApi.templates() });
}

export function useCreateCIType() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CITypeCreateRequest) => ciTypeApi.create(data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['ci-types'] }),
  });
}

export function useCloneCIType() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string; key?: string; name?: string }) =>
      ciTypeApi.clone(id, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['ci-types'] }),
  });
}

export function useSetCITypeActive() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, active }: { id: string; active: boolean }) =>
      active ? ciTypeApi.activate(id) : ciTypeApi.deactivate(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['ci-types'] }),
  });
}

export function useUpsertCITypeField() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ typeId, field }: { typeId: string; field: FieldDefinition }) =>
      ciTypeApi.upsertField(typeId, field),
    onSuccess: (_d, v) => {
      qc.invalidateQueries({ queryKey: ['ci-types', v.typeId] });
      qc.invalidateQueries({ queryKey: ['ci-types'] });
    },
  });
}

export function useDeleteCITypeField() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ typeId, name }: { typeId: string; name: string }) =>
      ciTypeApi.deleteField(typeId, name),
    onSuccess: (_d, v) => qc.invalidateQueries({ queryKey: ['ci-types', v.typeId] }),
  });
}

// Instance fields
export function useInstanceFields(ciId: string | undefined) {
  return useQuery({
    queryKey: ['ci-field-defs', ciId],
    queryFn: () => ciFieldApi.listInstance(ciId!),
    enabled: !!ciId,
  });
}

export function useUpsertInstanceField(ciId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (field: FieldDefinition) => ciFieldApi.upsertInstance(ciId, field),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['ci-field-defs', ciId] });
      qc.invalidateQueries({ queryKey: ['ci', ciId] });
    },
  });
}

export function useDeleteInstanceField(ciId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => ciFieldApi.deleteInstance(ciId, name),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['ci-field-defs', ciId] }),
  });
}

// Relationship types
export function useRelationshipTypes() {
  return useQuery({
    queryKey: ['relationship-types'],
    queryFn: () => relationshipTypeApi.list({ limit: 100 }),
  });
}

// Lifecycle
export function useLifecycleDefinitions() {
  return useQuery({
    queryKey: ['lifecycle-definitions'],
    queryFn: () => lifecycleApi.list({ limit: 100 }),
  });
}

export function useLifecycleTransition(entityType: 'assets' | 'cis', id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      toState,
      context,
      reason,
    }: {
      toState: string;
      context?: Record<string, unknown>;
      reason?: string;
    }) => lifecycleApi.transition(entityType, id, toState, context, reason),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [entityType === 'assets' ? 'assets' : 'ci'] });
      qc.invalidateQueries({ queryKey: ['history'] });
    },
  });
}

// Locations
export function useLocationTree() {
  return useQuery({ queryKey: ['locations', 'tree'], queryFn: () => locationApi.tree() });
}

export function useCreateLocation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: locationApi.create,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['locations'] }),
  });
}

export function useDeleteLocation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => locationApi.delete(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['locations'] }),
  });
}

export function useClients(enabled = true) {
  return useQuery({ queryKey: ['clients'], queryFn: () => clientApi.list(), enabled });
}

// Inventory movements + items
export function useMovements(params: { asset_id?: string; movement_type?: string } = {}) {
  return useQuery({
    queryKey: ['stock-movements', params],
    queryFn: () => inventoryApi.listMovements(params),
  });
}

export function useQuantityItems() {
  return useQuery({
    queryKey: ['inventory-items'],
    queryFn: () => inventoryApi.listItems({ limit: 100 }),
  });
}

export function useAvailability(params: { item_kind?: string; item_id?: string } = {}) {
  return useQuery({
    queryKey: ['inventory-availability', params],
    queryFn: () => inventoryApi.availability(params),
  });
}

// Reservations
export function useReservations(state?: string) {
  return useQuery({
    queryKey: ['reservations', state],
    queryFn: () => reservationApi.list({ state }),
  });
}

export function useCreateReservation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: reservationApi.create,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['reservations'] }),
  });
}

export function useReleaseReservation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => reservationApi.release(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['reservations'] }),
  });
}

// Composition
export function useCompositionChildren(assetId: string | undefined) {
  return useQuery({
    queryKey: ['composition', assetId],
    queryFn: () => compositionApi.children(assetId!),
    enabled: !!assetId,
  });
}

/**
 * Resolves the parent asset a CI belongs to. A standalone CI legitimately has
 * no parent, so a 404 is an expected answer rather than an error worth
 * retrying.
 */
export function useCIParent(ciId: string | undefined) {
  return useQuery({
    queryKey: ['ci-parent', ciId],
    queryFn: () => compositionApi.parentOfCI(ciId!),
    enabled: !!ciId,
    retry: false,
  });
}

// Provenance / overrides
export function useFieldValues(ciId: string | undefined) {
  return useQuery({
    queryKey: ['ci-fields', ciId],
    queryFn: () => overrideApi.listForCI(ciId!),
    enabled: !!ciId,
  });
}

export function useSetOverride(ciId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      field,
      value,
      reason,
      protectedFlag,
    }: {
      field: string;
      value: unknown;
      reason: string;
      protectedFlag?: boolean;
    }) => overrideApi.setOverride(ciId, field, value, reason, protectedFlag),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['ci-fields', ciId] });
      qc.invalidateQueries({ queryKey: ['ci', ciId] });
    },
  });
}

export function useClearOverride(ciId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (field: string) => overrideApi.clearOverride(ciId, field),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['ci-fields', ciId] }),
  });
}

export function useReconciliationConflicts() {
  return useQuery({
    queryKey: ['reconciliation-conflicts'],
    queryFn: () => overrideApi.conflicts(),
  });
}

// History
export function useEntityHistory(entityType: string, id: string | undefined) {
  return useQuery({
    queryKey: ['history', entityType, id],
    queryFn: () => historyApi.list(entityType, id!),
    enabled: !!id,
  });
}

// Impact
export function useBlastRadius(ciId: string | undefined, enabled = false) {
  return useQuery({
    queryKey: ['blast-radius', ciId],
    queryFn: () => impactApi.blastRadius(ciId!),
    enabled: !!ciId && enabled,
  });
}

// Saved views + query
export function useSavedViews() {
  return useQuery({ queryKey: ['saved-views'], queryFn: () => savedViewApi.list({ limit: 100 }) });
}

export function useSavedViewPresets() {
  return useQuery({ queryKey: ['saved-views', 'presets'], queryFn: () => savedViewApi.presets() });
}

export function useSaveView() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: savedViewApi.create,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-views'] }),
  });
}

export function useDeleteSavedView() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => savedViewApi.delete(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-views'] }),
  });
}

export function useFilterQuery() {
  return useMutation({
    mutationFn: (spec: Record<string, unknown> & { entity_kind?: string }) =>
      savedViewApi.query(spec),
  });
}
