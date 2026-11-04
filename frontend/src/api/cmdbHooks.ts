/**
 * cmdbHooks — TanStack Query hooks for the enterprise CMDB extension API.
 * Mirrors the conventions of api/hooks.ts (query keys, invalidation).
 */
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query';
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
  sensitiveFieldApi,
  type AttributeDefinition,
  type AttributeDefinitionCreate,
  type AttributeDefinitionFilter,
  type AttributeDefinitionPatch,
  type CITypeCreateRequest,
  type CITypeUpdateRequest,
  type LocationUpdateRequest,
  type SavedView,
} from './cmdb';
import { ApiError } from './client';
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

/**
 * useAllCITypes loads all pages of the CI types (ciTypeApi.listAll) for a
 * selection; useCITypes stays the paged list of the type administration.
 */
export function useAllCITypes(
  params: { include_inactive?: boolean; category?: string; search?: string } = {},
  options: QueryControl = {},
) {
  return useQuery({
    queryKey: ['ci-types', 'all', params],
    queryFn: () => ciTypeApi.listAll(params),
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

/** PATCH /ci-types/{id}: name, labels, description, category and parent (WP-248, WP-313). */
export function useUpdateCIType() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...data }: CITypeUpdateRequest & { id: string }) =>
      ciTypeApi.update(id, data),
    // A new parent changes the inherited attributes of the type and its subtypes.
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['ci-types'] });
      qc.invalidateQueries({ queryKey: ['attribute-definitions'] });
    },
  });
}

/** The organization-wide and system fields of GET /ci-fields/global (MET-13). */
const GLOBAL_FIELDS_KEY = ['ci-fields', 'global'];

/**
 * The changes of an attribute definition: the types (schema version), the
 * global fields of the CI form and the definitions.
 */
function invalidateAttributes(qc: QueryClient) {
  qc.invalidateQueries({ queryKey: ['ci-types'] });
  qc.invalidateQueries({ queryKey: GLOBAL_FIELDS_KEY });
  qc.invalidateQueries({ queryKey: ['attribute-definitions'] });
}

/** The global fields: the organization-wide ones and the read-only system ones (MET-13). */
export function useGlobalFields(options: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: GLOBAL_FIELDS_KEY,
    queryFn: () => ciFieldApi.listGlobal(),
    enabled: options.enabled ?? true,
  });
}

/** The pause between two reads of a list while an index job is open (as useJobs). */
export const INDEX_POLL_INTERVAL_MS = 3_000;

/** indexPending reports whether an index job of a definition is queued or running (DB-04). */
function indexPending(data: { data: AttributeDefinition[] } | undefined): boolean {
  return (data?.data ?? []).some(
    (d) => d.index_status?.state === 'queued' || d.index_status?.state === 'running',
  );
}

function attributeDefinitionsQuery(filter: AttributeDefinitionFilter) {
  return {
    queryKey: ['attribute-definitions', filter],
    queryFn: () => attributeDefinitionApi.list(filter),
    // The list follows its open index jobs until they end (WP-796).
    refetchInterval: (query: { state: { data?: { data: AttributeDefinition[] } } }) =>
      indexPending(query.state.data) ? INDEX_POLL_INTERVAL_MS : false,
  };
}

/** The global attribute definitions of the organization and the system (MET-13). */
export function useGlobalAttributeDefinitions() {
  return useQuery(attributeDefinitionsQuery({ scope: 'global' }));
}

/**
 * The definitions of a CI type and the global definitions that apply to it
 * (GET /attribute-definitions?applicable_to=, WP-255).
 */
export function useApplicableAttributeDefinitions(ciTypeId: string | undefined) {
  return useQuery({
    ...attributeDefinitionsQuery({ applicable_to: ciTypeId }),
    enabled: !!ciTypeId,
  });
}

/** The definitions of each of the given CI types (?ci_type_id=), e.g. the ancestors of a type. */
export function useTypeAttributeDefinitions(ciTypeIds: string[]) {
  return useQueries({
    queries: ciTypeIds.map((id) => attributeDefinitionsQuery({ ci_type_id: id })),
  });
}

/** POST /attribute-definitions: a definition with its index flags in one request (MET-15). */
export function useCreateAttributeDefinition() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: AttributeDefinitionCreate) => attributeDefinitionApi.create(data),
    onSuccess: () => invalidateAttributes(qc),
  });
}

/** PATCH /attribute-definitions/{id}; a flag change queues the index job (DB-04). */
export function useUpdateAttributeDefinition() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: AttributeDefinitionPatch }) =>
      attributeDefinitionApi.update(id, patch),
    onSuccess: () => invalidateAttributes(qc),
  });
}

/** DELETE /attribute-definitions/{id}. */
export function useDeleteAttributeDefinition() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (definition: AttributeDefinition) => attributeDefinitionApi.delete(definition.id),
    onSuccess: () => invalidateAttributes(qc),
  });
}

/** POST /attribute-definitions/{id}/reindex: rebuilds a failed or cancelled index (WP-796). */
export function useReindexAttributeDefinition() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => attributeDefinitionApi.reindex(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['attribute-definitions'] }),
  });
}

/**
 * The sensitive fields of CIs (WP-726). Reading them needs
 * field_permission:manage; without it (403) the list stays empty and the
 * catalog shows no mark, without an error.
 */
export function useSensitiveCIFields() {
  return useQuery({
    queryKey: ['field-permissions', 'sensitive-fields', 'ci'],
    queryFn: async () => {
      try {
        return (await sensitiveFieldApi.list({ object_kind: 'ci' })).data;
      } catch (err) {
        if (err instanceof ApiError && err.status === 403) return [];
        throw err;
      }
    },
    retry: false,
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
    queryKey: ['lifecycle-models'],
    queryFn: () => lifecycleApi.list({ limit: 100 }),
  });
}

// The allowed transitions of an entity (WP-723); a transition invalidates
// them through the entity's query key.
export function useLifecycleOptions(entityType: 'assets' | 'cis', id: string, enabled = true) {
  return useQuery({
    queryKey: [entityType === 'assets' ? 'assets' : 'ci', id, 'lifecycle-options'],
    queryFn: () => lifecycleApi.options(entityType, id),
    enabled: enabled && !!id,
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
// The tree is read where a page needs it: the CI detail page names the
// location, site and room of a CI only when it has one (CI-06, DB-05).
export function useLocationTree({ enabled = true }: { enabled?: boolean } = {}) {
  return useQuery({ queryKey: ['locations', 'tree'], queryFn: () => locationApi.tree(), enabled });
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
