/**
 * cmdbHooks — TanStack Query hooks for the enterprise CMDB extension API.
 * Mirrors the conventions of api/hooks.ts (query keys, invalidation).
 */
import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query';
import {
  attributeDefinitionApi,
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
  ATTRIBUTE_INDEX_JOB_KIND,
  type AttributeDefinition,
  type AttributeIndexFlags,
  type AttributeIndexJob,
  type CITypeCreateRequest,
  type LocationUpdateRequest,
  type SavedView,
} from './cmdb';
import type { FieldDefinition } from '../lib/fieldmeta';

/** Query options a caller may set; the CI dialog loads only while open, uncached (MET-04). */
interface QueryControl {
  enabled?: boolean;
  staleTime?: number;
}

export function useCITypes(
  params: { include_inactive?: boolean; category?: string; search?: string; limit?: number } = {},
  options: QueryControl = {},
) {
  return useQuery({
    queryKey: ['ci-types', params],
    queryFn: () => ciTypeApi.list(params),
    ...options,
  });
}

export function useCIType(
  id: string | undefined,
  { enabled = true, ...options }: QueryControl = {},
) {
  return useQuery({
    queryKey: ['ci-types', id],
    queryFn: () => ciTypeApi.get(id!),
    ...options,
    enabled: !!id && enabled,
  });
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

/** The index jobs of the attribute definitions (one shared query). */
const INDEX_JOBS_KEY = ['jobs', ATTRIBUTE_INDEX_JOB_KIND];

/**
 * FlagUpdateError is a partial success of useUpsertCITypeField: the field
 * is saved, its index flags are not (e.g. 409 while CIs share a value,
 * MET-15). refusal is the answer of PATCH /attribute-definitions/{id}.
 */
export class FlagUpdateError extends Error {
  readonly field: FieldDefinition;
  readonly refusal: unknown;

  constructor(field: FieldDefinition, refusal: unknown) {
    super(refusal instanceof Error ? refusal.message : String(refusal));
    this.name = 'FlagUpdateError';
    this.field = field;
    this.refusal = refusal;
  }
}

/** The changes of an attribute definition: the type, its definitions and the index jobs. */
function invalidateAttributes(qc: QueryClient, typeId: string) {
  qc.invalidateQueries({ queryKey: ['ci-types', typeId] });
  qc.invalidateQueries({ queryKey: ['ci-types'] });
  qc.invalidateQueries({ queryKey: ['attribute-definitions'] });
  qc.invalidateQueries({ queryKey: INDEX_JOBS_KEY });
}

export function useUpsertCITypeField() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      typeId,
      field,
      index,
    }: {
      typeId: string;
      field: FieldDefinition;
      index?: AttributeIndexFlags;
    }): Promise<{ field: FieldDefinition; definition?: AttributeDefinition }> => {
      const saved = await ciTypeApi.upsertField(typeId, field);
      const id = (saved as FieldDefinition & { id?: string }).id;
      if (!index || !id) return { field: saved };
      try {
        return { field: saved, definition: await attributeDefinitionApi.setIndexFlags(id, index) };
      } catch (err) {
        throw new FlagUpdateError(saved, err);
      }
    },
    // A refused flag update leaves the field saved: the list reloads either way.
    onSettled: (_d, _e, v) => invalidateAttributes(qc, v.typeId),
  });
}

/** The attribute definitions of a CI type with their index flags (MET-10, MET-15). */
export function useAttributeDefinitions(ciTypeId: string | undefined) {
  return useQuery({
    queryKey: ['attribute-definitions', { ci_type_id: ciTypeId }],
    queryFn: () => attributeDefinitionApi.list({ ci_type_id: ciTypeId, scope: 'type' }),
    enabled: !!ciTypeId,
  });
}

/** PATCH of the index flags of an existing definition; it queues the index job (DB-04). */
export function useSetAttributeIndexFlags() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, flags }: { id: string; typeId: string; flags: AttributeIndexFlags }) =>
      attributeDefinitionApi.setIndexFlags(id, flags),
    onSettled: (_d, _e, v) => invalidateAttributes(qc, v.typeId),
  });
}

function isActiveJob(job: AttributeIndexJob) {
  return job.status === 'queued' || job.status === 'running';
}

function definitionOf(job: AttributeIndexJob): string {
  const params = job.params as { definition_id?: unknown } | null | undefined;
  return typeof params?.definition_id === 'string' ? params.definition_id.toLowerCase() : '';
}

/**
 * useAttributeIndexJob follows the newest index job of a definition: the
 * server queues it before it answers the flag change, the list is newest
 * first. It refreshes while the job is queued or running. The jobs of all
 * definitions share one request.
 */
export function useAttributeIndexJob(definitionId: string | undefined) {
  return useQuery({
    queryKey: INDEX_JOBS_KEY,
    queryFn: () => attributeDefinitionApi.indexJobs(),
    enabled: !!definitionId,
    retry: false,
    select: (page) =>
      (page.data ?? []).find((job) => definitionOf(job) === definitionId?.toLowerCase()) ?? null,
    refetchInterval: (query) =>
      (query.state.data?.data ?? []).some(
        (job) => definitionOf(job) === definitionId?.toLowerCase() && isActiveJob(job),
      )
        ? 2_000
        : false,
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
export function useRelationshipTypes(enabled = true) {
  return useQuery({
    queryKey: ['relationship-types'],
    queryFn: () => relationshipTypeApi.list({ limit: 100 }),
    enabled,
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

/**
 * useUpdateLocation renames and/or moves a node (PATCH /locations/{id},
 * LOC-07). A move re-derives path, site and client of the whole subtree, so
 * the tree, every location detail and the history are refetched.
 */
export function useUpdateLocation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: LocationUpdateRequest }) =>
      locationApi.update(id, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['locations'] });
      qc.invalidateQueries({ queryKey: ['history'] });
    },
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

export function useFieldProvenance(ciId: string | undefined) {
  return useQuery({
    queryKey: ['ci-fields', ciId, 'provenance'],
    queryFn: () => overrideApi.provenance(ciId!),
    enabled: !!ciId,
  });
}

/**
 * Setting or clearing an override changes the effective value of the CI and
 * raises its version (API-07): the CI detail, the CI lists and the field values
 * are read again, so the page shows the new value without a reload (CI-10,
 * F-100) and the edit form sends the new version as If-Match.
 */
function invalidateOverride(qc: QueryClient, ciId: string) {
  return Promise.all([
    qc.invalidateQueries({ queryKey: ['ci-fields', ciId] }),
    qc.invalidateQueries({ queryKey: ['ci', ciId] }),
    qc.invalidateQueries({ queryKey: ['cis'] }),
  ]);
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
    onSuccess: () => invalidateOverride(qc, ciId),
  });
}

export function useClearOverride(ciId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (field: string) => overrideApi.clearOverride(ciId, field),
    onSuccess: () => invalidateOverride(qc, ciId),
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

/** Changes an own view (PATCH, owner only, E-86): sort, columns, sharing. */
export function useUpdateSavedView() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: Partial<SavedView> }) =>
      savedViewApi.update(id, data),
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

/**
 * Runs a structured filter spec (POST /search/query, search:read plus the
 * read permission of the entity kind). A query rather than a mutation, so a
 * failed run can be repeated with refetch(); null runs nothing. Not retried
 * automatically: a 403 is an answer, not a transient failure (F-055).
 */
export function useFilterQuery(spec: (Record<string, unknown> & { entity_kind?: string }) | null) {
  return useQuery({
    queryKey: ['search-query', spec],
    queryFn: () => savedViewApi.query(spec!),
    enabled: spec !== null,
    // Every run of a view asks the server; a cached result only bridges
    // the wait.
    staleTime: 0,
    retry: false,
  });
}
