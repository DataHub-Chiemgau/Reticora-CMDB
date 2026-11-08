import {
  useInfiniteQuery,
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import {
  ciApi,
  relationshipApi,
  relationshipSuppressionApi,
  collectorApi,
  discoveryScopeApi,
  scopeSuggestionApi,
  topologyApi,
  IMPACT_COMPACT_FIELDS,
  siteApi,
  rackApi,
  assetApi,
  assignmentApi,
  consumableApi,
  documentApi,
  importApi,
  fetchAPI,
  stocktakeApi,
  ticketApi,
  userApi,
  teamApi,
  roleApi,
  invitationApi,
  permissionApi,
  slaApi,
  formApi,
  workflowApi,
  complianceApi,
  igaApi,
  searchApi,
  aiApi,
  webhookApi,
  exportApi,
  monitoringApi,
  auditApi,
  privacyApi,
  credentialApi,
  vrfApi,
  ApiError,
} from '../api/client';
import type {
  DiscoveryScopeInput,
  DiscoveryScopePatch,
  CICreateRequest,
  CIListParams,
  CIUpdateRequest,
  RelationshipCreateRequest,
  RelationshipUpdateRequest,
  ListParams,
  AssetCreateRequest,
  ConsumableAvailabilityList,
  AssetUpdateRequest,
  AssetListParams,
  AssignAssetRequest,
  ReturnAssetRequest,
  TransferAssetRequest,
  AssignmentListParams,
  Document,
  DocumentCreateRequest,
  DocumentLinkTarget,
  DocumentListParams,
  DocumentVersion,
  CreateImportProfileRequest,
  ImportStartDocumentRequest,
  UpdateImportProfileRequest,
  StocktakeCreateRequest,
  StocktakeUpdateRequest,
  StockScanCreateRequest,
  CursorParams,
  StocktakeListParams,
  TicketCreateRequest,
  TicketUpdateRequest,
  TicketListParams,
  TicketComment,
  TicketLink,
  TicketLinkTarget,
  ReviewItemTicketRequest,
  Ticket,
  UserListParams,
  UserStatus,
  AppUser,
  PaginatedResponse,
  InvitationRequest,
  InvitationAcceptance,
  TopologyParams,
  RackListParams,
  SLAListParams,
  SLABreachParams,
  SLAPolicyRequest,
  IGACreateConnectorRequest,
  SearchParams,
  WebhookCreateRequest,
  ExportJobCreateRequest,
  MetricQueryParams,
  AlertRuleCreateRequest,
} from '../api/client';
import { isActive, type Job } from '../hooks/useJobs';

/**
 * ListQueryOptions switch a list query off, e.g. a picker until the user
 * typed a search (WP-318).
 */
export interface ListQueryOptions {
  enabled?: boolean;
}

export function useCIList(params: CIListParams, { enabled = true }: ListQueryOptions = {}) {
  return useQuery({
    queryKey: ['cis', params],
    queryFn: () => ciApi.list(params),
    enabled,
  });
}

export function useCI(id: string) {
  return useQuery({
    queryKey: ['ci', id],
    queryFn: () => ciApi.get(id),
    enabled: !!id,
  });
}

export function useCIRelationships(id: string) {
  return useQuery({
    queryKey: ['ci-relationships', id],
    queryFn: () => ciApi.relationships(id),
    enabled: !!id,
  });
}

/**
 * Relationship mutations invalidate every view that renders the graph: the
 * CI's own relationship list, the neighbor subgraph and the topology view,
 * so a verify/create/delete is reflected everywhere without a manual reload.
 */
function useRelationshipInvalidation(ciId: string) {
  const queryClient = useQueryClient();
  return () => {
    void queryClient.invalidateQueries({ queryKey: ['ci-relationships', ciId] });
    // Every edge mutation writes a history row (WP-296).
    void queryClient.invalidateQueries({ queryKey: ['relationship-changes'] });
    void queryClient.invalidateQueries({ queryKey: ['topology-neighbors', ciId] });
    void queryClient.invalidateQueries({ queryKey: ['topology'] });
  };
}

/**
 * onCreated runs after every successful create. It belongs to the hook: the
 * callbacks passed to mutate run only for the latest call, so a later call
 * would swallow the success of an earlier one (F-314).
 */
export function useCreateRelationship(
  ciId: string,
  { onCreated }: { onCreated?: () => void } = {},
) {
  const invalidate = useRelationshipInvalidation(ciId);
  return useMutation({
    mutationFn: (data: RelationshipCreateRequest) => relationshipApi.create(data),
    onSuccess: () => {
      invalidate();
      onCreated?.();
    },
  });
}

export function useUpdateRelationship(ciId: string) {
  const invalidate = useRelationshipInvalidation(ciId);
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: RelationshipUpdateRequest }) =>
      relationshipApi.update(id, data),
    onSuccess: invalidate,
  });
}

export function useDeleteRelationship(ciId: string) {
  const invalidate = useRelationshipInvalidation(ciId);
  const queryClient = useQueryClient();
  return useMutation({
    // A discovery edge is deleted with force and then suppressed with the
    // reason of the confirmation (REL-04, F-273).
    mutationFn: (arg: string | { id: string; force?: boolean; reason?: string }) =>
      typeof arg === 'string'
        ? relationshipApi.delete(arg)
        : relationshipApi.delete(arg.id, arg.force ?? false, arg.reason),
    onSuccess: () => {
      invalidate();
      void queryClient.invalidateQueries({ queryKey: ['relationship-suppressions', ciId] });
    },
  });
}

/**
 * The history of an edge (WP-296), newest first; every edge mutation
 * invalidates it (useRelationshipInvalidation).
 */
export function useRelationshipChanges(id: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['relationship-changes', id],
    queryFn: () => relationshipApi.changes(id!),
    enabled: !!id && enabled,
  });
}

/** The suppressions of a CI's removed discovery edges (F-273); enabled when shown. */
export function useRelationshipSuppressions(ciId: string, enabled = true) {
  return useQuery({
    queryKey: ['relationship-suppressions', ciId],
    queryFn: () => relationshipSuppressionApi.list(ciId),
    enabled: !!ciId && enabled,
  });
}

/** Lifting a suppression lets the next discovery cycle create the edge again (F-273). */
export function useLiftSuppression(ciId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => relationshipSuppressionApi.lift(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['relationship-suppressions', ciId] });
    },
  });
}

export function useCollectors(params: ListParams = {}) {
  return useQuery({
    queryKey: ['collectors', params],
    queryFn: () => collectorApi.list(params),
  });
}

/** Every collector of the organization, not only the first page (F-362). */
export function useAllCollectors() {
  return useQuery({
    queryKey: ['collectors', 'all'],
    queryFn: () => collectorApi.listAll(),
  });
}

export function useDiscoveryScopes(collectorId: string) {
  return useQuery({
    queryKey: ['discovery-scopes', collectorId],
    queryFn: () => discoveryScopeApi.list(collectorId),
  });
}

export function useDiscoveryScopeMutations(collectorId: string) {
  const queryClient = useQueryClient();
  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['discovery-scopes', collectorId] });
  return {
    create: useMutation({
      mutationFn: (input: DiscoveryScopeInput) => discoveryScopeApi.create(input),
      onSuccess: invalidate,
    }),
    update: useMutation({
      mutationFn: ({ id, patch }: { id: string; patch: DiscoveryScopePatch }) =>
        discoveryScopeApi.update(id, patch),
      onSuccess: invalidate,
    }),
    remove: useMutation({
      mutationFn: (id: string) => discoveryScopeApi.delete(id),
      onSuccess: invalidate,
    }),
  };
}

/** The credentials (metadata only) a scope form offers; needs credential:read. */
export function useCredentials(enabled = true) {
  return useQuery({
    queryKey: ['credentials', 'options'],
    queryFn: () => credentialApi.list({ limit: 200 }),
    enabled,
    retry: false,
  });
}

/** The VRFs of the organization, the default first (TEN-10). */
export function useVrfs(enabled = true) {
  return useQuery({
    queryKey: ['vrfs', 'options'],
    queryFn: () => vrfApi.list({ limit: 200 }),
    enabled,
  });
}

export function useScopeSuggestions() {
  return useQuery({
    queryKey: ['scope-suggestions'],
    queryFn: () => scopeSuggestionApi.list(),
  });
}

/** Accepting a suggestion creates the scope; dismissing keeps it away. */
export function useResolveScopeSuggestion() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, action }: { id: string; action: 'accept' | 'dismiss' }) =>
      scopeSuggestionApi.resolve(id, action),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['scope-suggestions'] });
      queryClient.invalidateQueries({ queryKey: ['discovery-scopes'] });
    },
  });
}

export function useTopology(params: TopologyParams = {}) {
  return useQuery({
    queryKey: ['topology', params],
    queryFn: () => topologyApi.get(params),
  });
}

export function useCINeighbors(id: string) {
  return useQuery({
    queryKey: ['topology-neighbors', id],
    queryFn: () => topologyApi.neighbors(id),
    enabled: !!id,
  });
}

export function useCIImpact(id: string, relType?: string) {
  return useQuery({
    queryKey: ['topology-impact', id, relType],
    queryFn: () =>
      topologyApi.impact(id, { rel_type: relType || undefined, fields: IMPACT_COMPACT_FIELDS }),
    enabled: !!id,
  });
}

/** The most CIs one joint impact analysis takes (GET /impact?ci_ids=, 422 beyond). */
export const MAX_IMPACT_CIS = 100;

/**
 * useImpactMany runs the joint impact analysis of the failed CIs (IMP-05).
 * Its error is the page's answer when the analysis fails: there is no
 * client-side replacement (F-326). include asks for the blocks of WP-297
 * (IMPACT_PANEL_INCLUDE: services, clients, sites, SPOF) next to the
 * compact CIs; clients and sites come as aggregates, not as node fields.
 */
export function useImpactMany(ids: string[], relType?: string, include?: string) {
  return useQuery({
    queryKey: ['topology-impact-many', ids, relType, include],
    queryFn: () =>
      topologyApi.impactMany(ids, {
        rel_type: relType || undefined,
        fields: IMPACT_COMPACT_FIELDS,
        include: include || undefined,
      }),
    enabled: ids.length > 0,
    retry: false,
  });
}

export function useSites(clientId?: string) {
  return useQuery({
    queryKey: ['sites', 'options', clientId],
    queryFn: () => siteApi.list({ limit: 200, ...(clientId ? { client_id: clientId } : {}) }),
  });
}

export function useRackList(params: RackListParams = {}) {
  return useQuery({
    queryKey: ['racks', params],
    queryFn: () => rackApi.list(params),
  });
}

export function useRackLayout(rackId: string) {
  return useQuery({
    queryKey: ['rack-layout', rackId],
    queryFn: () => rackApi.layout(rackId),
    enabled: !!rackId,
  });
}

/**
 * Resolves a few CIs by id (the candidates of a review item), one request per
 * CI. The rack page does not use it: its layout names the mounted CIs
 * (RCK-02, F-252).
 */
export function useMountedCIs(ciIds: string[]) {
  const uniqueIds = Array.from(new Set(ciIds));

  return useQueries({
    queries: uniqueIds.map((id) => ({
      queryKey: ['ci', id],
      queryFn: () => ciApi.get(id),
    })),
    combine: (results) =>
      new Map(
        results.flatMap((result) => (result.data ? [[result.data.id, result.data] as const] : [])),
      ),
  });
}

export function useCreateCI() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CICreateRequest) => ciApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['cis'] });
    },
  });
}

export function useUpdateCI() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data, version }: { id: string; data: CIUpdateRequest; version?: number }) =>
      ciApi.update(id, data, version),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ['cis'] });
      queryClient.invalidateQueries({ queryKey: ['ci', variables.id] });
    },
  });
}

export function useDeleteCI() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => ciApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['cis'] });
    },
  });
}

// --- Search / AI assistant ---
export function useSearch(params: SearchParams) {
  return useQuery({
    queryKey: ['search', params],
    queryFn: () => searchApi.query(params),
    enabled: Boolean(params.q && params.q.trim().length >= 2),
  });
}

export function useAIConversations() {
  return useQuery({ queryKey: ['ai-conversations'], queryFn: () => aiApi.conversations() });
}

export function useAskAI() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: { question: string; conversation_id?: string }) => aiApi.ask(data),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['ai-conversations'] }),
  });
}

// --- Assets ---

export function useAssetList(params: AssetListParams, { enabled = true }: ListQueryOptions = {}) {
  return useQuery({
    queryKey: ['assets', params],
    queryFn: () => assetApi.list(params),
    enabled,
  });
}

// --- Consumables ---

/** useConsumableList lists the consumables; /consumables is a scoped list (client_id). */
export function useConsumableList(
  params: ListParams & { low_stock?: boolean },
  { enabled = true }: ListQueryOptions = {},
) {
  return useQuery({
    queryKey: ['consumables', params],
    queryFn: () => consumableApi.list(params),
    enabled,
  });
}

/** useConsumable reads one consumable (GET /consumables/{id}). */
export function useConsumable(id: string | undefined) {
  return useQuery({
    queryKey: ['consumables', 'detail', id],
    queryFn: () => consumableApi.get(id!),
    enabled: !!id,
  });
}

/**
 * useConsumableAvailability reads on_hand, reserved and available of a
 * consumable per bin page by page over next_cursor (GET
 * /consumables/{id}/availability, WP-759); bookings and reservations read it
 * again (invalidateStock).
 */
export function useConsumableAvailability(id: string | undefined) {
  return useInfiniteQuery({
    queryKey: ['consumable-availability', id],
    queryFn: ({ pageParam }) => consumableApi.availability(id!, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: ConsumableAvailabilityList) =>
      last.has_more ? last.next_cursor || undefined : undefined,
    enabled: !!id,
  });
}

export function useCreateAsset() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: AssetCreateRequest) => assetApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['assets'] });
    },
  });
}

export function useUpdateAsset() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: AssetUpdateRequest }) =>
      assetApi.update(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['assets'] });
    },
  });
}

export function useDeleteAsset() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => assetApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['assets'] });
    },
  });
}

// --- Assignments ---

export function useAssignmentList(params: AssignmentListParams) {
  return useQuery({
    queryKey: ['assignments', params],
    queryFn: () => assignmentApi.list(params),
  });
}

// The writes go through the asset (WP-746) and refresh the assignment
// lists and the assets (holder and status change with them, WP-318).
export function useCreateAssignment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ assetId, data }: { assetId: string; data: AssignAssetRequest }) =>
      assignmentApi.assign(assetId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['assignments'] });
      queryClient.invalidateQueries({ queryKey: ['assets'] });
    },
  });
}

export function useReturnAssignment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ assetId, data }: { assetId: string; data: ReturnAssetRequest }) =>
      assignmentApi.returnAsset(assetId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['assignments'] });
      queryClient.invalidateQueries({ queryKey: ['assets'] });
    },
  });
}

export function useTransferAssignment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ assetId, data }: { assetId: string; data: TransferAssetRequest }) =>
      assignmentApi.transfer(assetId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['assignments'] });
      queryClient.invalidateQueries({ queryKey: ['assets'] });
    },
  });
}

// --- Documents ---

export function useDocumentList(
  params: DocumentListParams,
  { enabled = true }: ListQueryOptions = {},
) {
  return useQuery({
    queryKey: ['documents', params],
    queryFn: () => documentApi.list(params),
    enabled,
  });
}

export function useCreateDocument() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: DocumentCreateRequest) => documentApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['documents'] });
    },
  });
}

export function useDeleteDocument() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => documentApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['documents'] });
    },
  });
}

/** SCAN_POLL_MS is the interval at which a pending scan is read again (WP-321). */
export const SCAN_POLL_MS = 5000;

const pendingScan = (status: string | undefined) => status === 'pending_scan';

/** useDocument reads a document; while its current version is pending_scan every 5 s. */
export function useDocument(id: string) {
  return useQuery({
    queryKey: ['documents', 'detail', id],
    queryFn: () => documentApi.get(id),
    enabled: !!id,
    refetchInterval: (query) =>
      pendingScan((query.state.data as Document | undefined)?.scan_status) ? SCAN_POLL_MS : false,
  });
}

/**
 * useDocumentVersions reads the versions of a document page by page over
 * next_cursor; while a loaded version is pending_scan every 5 s.
 */
export function useDocumentVersions(id: string) {
  return useInfiniteQuery({
    queryKey: ['documents', 'versions', id],
    queryFn: ({ pageParam }) => documentApi.listVersions(id, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: PaginatedResponse<DocumentVersion>) =>
      last.has_more ? last.next_cursor || undefined : undefined,
    enabled: !!id,
    refetchInterval: (query) =>
      (query.state.data?.pages ?? []).some((page) =>
        page.data.some((version) => pendingScan(version.scan_status)),
      )
        ? SCAN_POLL_MS
        : false,
  });
}

export function useUpdateDocument(id: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: { title?: string; description?: string; category?: string }) =>
      documentApi.update(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['documents'] });
    },
  });
}

export function useDocumentLinks(id: string) {
  return useQuery({
    queryKey: ['documents', 'links', id],
    queryFn: () => documentApi.getLinks(id),
    enabled: !!id,
  });
}

/** useLinkDocument links a document to a target (POST /documents/{id}/links, WP-267). */
export function useLinkDocument() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, target }: { id: string; target: DocumentLinkTarget }) =>
      documentApi.link(id, target),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['documents'] });
    },
  });
}

/** useUnlinkDocument removes a link (DELETE /documents/{id}/links/{link_id}, WP-267). */
export function useUnlinkDocument() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, linkId }: { id: string; linkId: string }) => documentApi.unlink(id, linkId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['documents'] });
    },
  });
}

/** useDocumentUsage reads the storage usage of the organization (WP-731). */
export function useDocumentUsage() {
  return useQuery({
    queryKey: ['documents', 'usage'],
    queryFn: () => documentApi.usage(),
  });
}

// --- Import (IMP-IO-01, WP-322) ---

/** useImportProfiles lists the mapping profiles of the organization (sorted by name). */
export function useImportProfiles(enabled = true) {
  return useQuery({
    queryKey: ['import-profiles'],
    queryFn: () => importApi.listProfiles({ limit: 200 }),
    enabled,
  });
}

export function useCreateImportProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateImportProfileRequest) => importApi.createProfile(data),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['import-profiles'] }),
  });
}

export function useUpdateImportProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdateImportProfileRequest }) =>
      importApi.updateProfile(id, data),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['import-profiles'] }),
  });
}

export function useDeleteImportProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => importApi.deleteProfile(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['import-profiles'] }),
  });
}

/**
 * useImportPreview reads the header and up to 20 rows of a scanned file
 * (POST /imports/preview), for XLSX of the chosen sheet. A refused file
 * (409, 422) is not retried.
 */
export function useImportPreview(documentId: string | undefined, sheet?: string, enabled = true) {
  return useQuery({
    queryKey: ['imports', 'preview', documentId, sheet ?? ''],
    queryFn: () =>
      importApi.preview({ document_id: documentId ?? '', ...(sheet ? { sheet } : {}) }),
    enabled: enabled && !!documentId,
    retry: false,
  });
}

/** useStartImport starts a run (POST /imports, 202 with the job). */
export function useStartImport() {
  return useMutation({
    mutationFn: (data: ImportStartDocumentRequest) => importApi.start(data),
  });
}

/** IMPORT_POLL_MS is the pause between two reads of a running import job. */
export const IMPORT_POLL_MS = 3000;

/**
 * useImportRun follows an import run: the job (GET /jobs/{id}) every 3 s
 * while isActive holds, then once the projection of the run (GET
 * /imports/{id}) with its counters and error code.
 */
export function useImportRun(id: string | null) {
  const job = useQuery({
    queryKey: ['jobs', id, 'import'],
    queryFn: () => fetchAPI<Job>(`/jobs/${id}`),
    enabled: !!id,
    refetchInterval: (query) =>
      query.state.data && !isActive(query.state.data) ? false : IMPORT_POLL_MS,
  });
  const finished = !!job.data && !isActive(job.data);
  const run = useQuery({
    queryKey: ['imports', 'run', id],
    queryFn: () => importApi.get(id ?? ''),
    enabled: !!id && finished,
  });
  return { job, run, finished };
}

// --- Stocktakes ---

export function useStocktakeList(params: StocktakeListParams) {
  return useQuery({
    queryKey: ['stocktakes', params],
    queryFn: () => stocktakeApi.list(params),
  });
}

export function useStocktake(id: string) {
  return useQuery({
    queryKey: ['stocktakes', id],
    queryFn: () => stocktakeApi.get(id),
    enabled: !!id,
  });
}

/** STOCKTAKE_PAGE_LIMIT is the page size the stocktake lists are read with (API-02 maximum). */
export const STOCKTAKE_PAGE_LIMIT = 200;
/** STOCKTAKE_MAX_PAGES caps how many cursor pages of one list are read. */
export const STOCKTAKE_MAX_PAGES = 25;

/**
 * collectPages reads a cursor list page by page (next_cursor) up to
 * STOCKTAKE_MAX_PAGES and returns all rows; truncated tells that rows are
 * left unread.
 */
async function collectPages<T>(
  read: (params: CursorParams) => Promise<PaginatedResponse<T>>,
): Promise<{ data: T[]; truncated: boolean }> {
  const data: T[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < STOCKTAKE_MAX_PAGES; page += 1) {
    const res = await read({ limit: STOCKTAKE_PAGE_LIMIT, cursor });
    data.push(...res.data);
    if (!res.has_more || !res.next_cursor) return { data, truncated: false };
    cursor = res.next_cursor;
  }
  return { data, truncated: true };
}

/**
 * useStocktakeItems reads the frozen expected stock of a stocktake (GET
 * …/items, all pages). The snapshot does not follow scans or movements, so
 * its key lies outside ['stocktakes'] and only start and completion read it
 * again.
 */
export function useStocktakeItems(id: string, enabled = true) {
  return useQuery({
    queryKey: ['stocktake-items', id],
    queryFn: () => collectPages((params) => stocktakeApi.items(id, params)),
    enabled: !!id && enabled,
  });
}

/** useStocktakeScans reads the scans of a stocktake, newest first (GET …/scans, all pages). */
export function useStocktakeScans(id: string, enabled = true) {
  return useQuery({
    queryKey: ['stocktakes', id, 'scans'],
    queryFn: () => collectPages((params) => stocktakeApi.listScans(id, params)),
    enabled: !!id && enabled,
  });
}

/** useStocktakeDifferences reads the difference list (GET …/differences, all pages). */
export function useStocktakeDifferences(id: string, enabled = true) {
  return useQuery({
    queryKey: ['stocktakes', id, 'differences'],
    queryFn: () => collectPages((params) => stocktakeApi.differences(id, params)),
    enabled: !!id && enabled,
  });
}

export function useCreateStocktake() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: StocktakeCreateRequest) => stocktakeApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['stocktakes'] });
    },
  });
}

export function useUpdateStocktake(id: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: StocktakeUpdateRequest) => stocktakeApi.update(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['stocktakes'] });
    },
  });
}

export function useDeleteStocktake() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => stocktakeApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['stocktakes'] });
    },
  });
}

/** useStartStocktake starts a planned stocktake; the snapshot is read afresh. */
export function useStartStocktake() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => stocktakeApi.start(id),
    onSuccess: (_data, id) => {
      queryClient.invalidateQueries({ queryKey: ['stocktakes'] });
      queryClient.invalidateQueries({ queryKey: ['stocktake-items', id] });
    },
  });
}

export function useCancelStocktake() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => stocktakeApi.cancel(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['stocktakes'] });
    },
  });
}

/** useAddScan records a scan; counters, scans and the difference list are read again. */
export function useAddScan(stocktakeId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: StockScanCreateRequest) => stocktakeApi.addScan(stocktakeId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['stocktakes'] });
    },
  });
}

/** useCompleteStocktake completes a stocktake; the server books its corrections (WP-761). */
export function useCompleteStocktake() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => stocktakeApi.complete(id),
    onSuccess: (_data, id) => {
      queryClient.invalidateQueries({ queryKey: ['stocktakes'] });
      queryClient.invalidateQueries({ queryKey: ['stocktake-items', id] });
      queryClient.invalidateQueries({ queryKey: ['assets'] });
      queryClient.invalidateQueries({ queryKey: ['consumables'] });
    },
  });
}

// --- Tickets ---

export function useTicketList(params: TicketListParams) {
  return useQuery({
    queryKey: ['tickets', params],
    queryFn: () => ticketApi.list(params),
  });
}

export function useCreateTicket() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: TicketCreateRequest) => ticketApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tickets'] });
    },
  });
}

export function useUpdateTicket() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: TicketUpdateRequest }) =>
      ticketApi.update(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tickets'] });
    },
  });
}

export function useDeleteTicket() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => ticketApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tickets'] });
    },
  });
}

/** useTicket reads one ticket (GET /tickets/{id}). */
export function useTicket(id: string) {
  return useQuery({
    queryKey: ['tickets', 'detail', id],
    queryFn: () => ticketApi.get(id),
    enabled: !!id,
  });
}

/** useTicketComments reads the comments of a ticket page by page (limit/offset). */
export function useTicketComments(ticketId: string, pageSize = 50) {
  return useInfiniteQuery({
    queryKey: ['tickets', 'comments', ticketId],
    queryFn: ({ pageParam }) =>
      ticketApi.listComments(ticketId, { limit: pageSize, offset: pageParam }),
    initialPageParam: 0,
    getNextPageParam: (last: PaginatedResponse<TicketComment>, pages) =>
      last.has_more ? pages.reduce((sum, page) => sum + page.data.length, 0) : undefined,
    enabled: !!ticketId,
  });
}

export function useAddTicketComment(ticketId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: { content: string; is_internal?: boolean }) =>
      ticketApi.addComment(ticketId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tickets', 'comments', ticketId] });
    },
  });
}

/** useTicketLinks reads the object links of a ticket page by page over next_cursor (WP-764). */
export function useTicketLinks(ticketId: string) {
  return useInfiniteQuery({
    queryKey: ['tickets', 'links', ticketId],
    queryFn: ({ pageParam }) => ticketApi.listLinks(ticketId, { cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: PaginatedResponse<TicketLink>) =>
      last.has_more ? last.next_cursor || undefined : undefined,
    enabled: !!ticketId,
  });
}

export function useAddTicketLink(ticketId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (target: TicketLinkTarget) => ticketApi.addLink(ticketId, target),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tickets', 'links', ticketId] });
    },
  });
}

export function useRemoveTicketLink(ticketId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (linkId: string) => ticketApi.removeLink(ticketId, linkId),
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['tickets', 'links', ticketId] });
    },
  });
}

/**
 * fetchTicketAttachments reads every document attached to a ticket (GET
 * /tickets/{id}/attachments, WP-765), following next_cursor page by page.
 */
export async function fetchTicketAttachments(ticketId: string): Promise<Document[]> {
  const all: Document[] = [];
  let cursor: string | undefined;
  for (;;) {
    const page = await ticketApi.listAttachments(ticketId, { limit: 200, cursor });
    all.push(...(page.data ?? []));
    if (!page.has_more || !page.next_cursor) return all;
    cursor = page.next_cursor;
  }
}

/**
 * ticketAttachmentsKey is the key LinkedDocuments uses for the documents
 * of a ticket, so the page and the card share one request.
 */
export const ticketAttachmentsKey = (ticketId: string) =>
  ['documents', 'linked', 'ticket', ticketId] as const;

/** useTicketAttachments reads the documents of a ticket (WP-765). */
export function useTicketAttachments(ticketId: string, { enabled = true } = {}) {
  return useQuery({
    queryKey: ticketAttachmentsKey(ticketId),
    queryFn: () => fetchTicketAttachments(ticketId),
    enabled: enabled && !!ticketId,
  });
}

/** TICKET_SLA_POLL_MS: the SLA state of a ticket is read again every minute. */
export const TICKET_SLA_POLL_MS = 60_000;

/**
 * useTicketSLA reads the SLA state of a ticket (GET /tickets/{id}/sla,
 * WP-292) every minute; enabled holds the request back without the
 * feature ticketing_standard (E-39), so no 403 arises.
 */
export function useTicketSLA(ticketId: string, { enabled = true } = {}) {
  return useQuery({
    queryKey: ['tickets', 'sla', ticketId],
    queryFn: () => slaApi.getTicket(ticketId),
    enabled: enabled && !!ticketId,
    refetchInterval: TICKET_SLA_POLL_MS,
  });
}

/** useCreateTicketFromReview opens a ticket for one review item (WP-291). */
export function useCreateTicketFromReview() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data?: ReviewItemTicketRequest }) =>
      ticketApi.createFromReview(id, data),
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['review-items'] });
      queryClient.invalidateQueries({ queryKey: ['tickets'] });
    },
  });
}

/** BulkTicketResult reports the tickets opened for several review items. */
export interface BulkTicketResult {
  created: { id: string; ticket: Ticket }[];
  failed: { id: string; status?: number; message: string }[];
}

/** BULK_TICKET_LIMIT is the most review items one bulk action opens tickets for. */
export const BULK_TICKET_LIMIT = 100;

/**
 * useBulkCreateTicketsFromReviews opens a ticket for each of several review
 * items, one request after the other (POST /review-items/{id}/ticket, like
 * useBulkResolveReviews); an item that fails (403, 404, 409 of an open
 * ticket) is reported with the detail of the server and does not stop the
 * others.
 */
export function useBulkCreateTicketsFromReviews() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ ids }: { ids: string[] }): Promise<BulkTicketResult> => {
      const result: BulkTicketResult = { created: [], failed: [] };
      for (const id of ids.slice(0, BULK_TICKET_LIMIT)) {
        try {
          const ticket = await ticketApi.createFromReview(id);
          result.created.push({ id, ticket });
        } catch (err) {
          result.failed.push({
            id,
            status: err instanceof ApiError ? err.status : undefined,
            message:
              err instanceof ApiError
                ? (err.detail ?? err.message)
                : err instanceof Error
                  ? err.message
                  : String(err),
          });
        }
      }
      return result;
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['review-items'] });
      queryClient.invalidateQueries({ queryKey: ['tickets'] });
    },
  });
}

// --- Users / Teams / Roles ---

export function useUserList(params: UserListParams, { enabled = true }: ListQueryOptions = {}) {
  return useQuery({
    queryKey: ['users', params],
    queryFn: () => userApi.list(params),
    enabled,
  });
}

/**
 * useSetUserStatus deactivates or reactivates a user (TLC-04). The answer
 * replaces the user in every cached list at once, so the status badge
 * follows the server's answer; the lists are refetched afterwards.
 */
export function useSetUserStatus() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, status }: { id: string; status: UserStatus }) =>
      userApi.update(id, { status }),
    onSuccess: (user) => {
      queryClient.setQueriesData<PaginatedResponse<AppUser>>({ queryKey: ['users'] }, (list) =>
        list ? { ...list, data: list.data.map((u) => (u.id === user.id ? user : u)) } : list,
      );
      queryClient.invalidateQueries({ queryKey: ['users'] });
    },
  });
}

/** useDeleteUser deletes a user; a 409 (open objects, references) leaves the lists as they are. */
export function useDeleteUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => userApi.delete(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['users'] }),
  });
}

export function useTeamList(params: UserListParams, { enabled = true }: ListQueryOptions = {}) {
  return useQuery({
    queryKey: ['teams', params],
    queryFn: () => teamApi.list(params),
    enabled,
  });
}

export function useRoleList(params: ListParams = {}) {
  return useQuery({
    queryKey: ['roles', params],
    queryFn: () => roleApi.list(params),
  });
}

// --- Invitations (TLC-03) ---

/** useInvitations lists the organization's invitations; enabled only for user:manage holders. */
export function useInvitations(enabled = true) {
  return useQuery({
    queryKey: ['invitations'],
    queryFn: () => invitationApi.list(),
    enabled,
  });
}

export function useIssueInvitation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: InvitationRequest) => invitationApi.issue(data),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['invitations'] }),
  });
}

export function useRenewInvitation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => invitationApi.renew(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['invitations'] }),
  });
}

export function useRevokeInvitation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => invitationApi.revoke(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['invitations'] }),
  });
}

/** useAcceptInvitation is the public acceptance; it touches no cached list. */
export function useAcceptInvitation() {
  return useMutation({
    mutationFn: (data: InvitationAcceptance) => invitationApi.accept(data),
  });
}

// --- Permissions / SLA ---

export function usePermissionCatalogue() {
  return useQuery({
    queryKey: ['permissions'],
    queryFn: () => permissionApi.list(),
  });
}

export function useEffectivePermissions() {
  return useQuery({
    queryKey: ['me-permissions'],
    queryFn: () => permissionApi.effective(),
  });
}

/**
 * useHasPermission tells whether the signed-in user holds a permission in the
 * effective rights of /me/permissions. It fails closed: while the rights load
 * or when they cannot be read, the permission counts as missing, so a page
 * offers an action only once the server confirmed the right. The server still
 * decides every request; this only keeps refused actions out of the UI.
 * The result is undefined while the rights load, so a page can tell a
 * missing right from one not yet known.
 */
export function useHasPermission(permission: string): boolean | undefined {
  const effective = useEffectivePermissions();
  if (effective.isPending) return undefined;
  return effective.data?.permissions?.includes(permission) ?? false;
}

export function useSLAList(params: SLAListParams = {}) {
  return useQuery({
    queryKey: ['slas', params],
    queryFn: () => slaApi.list(params),
  });
}

export function useSLABreaches(params: SLABreachParams = {}) {
  return useQuery({
    queryKey: ['sla-breaches', params],
    queryFn: () => slaApi.breaches(params),
  });
}

export function useCreateSLA() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: SLAPolicyRequest) => slaApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['slas'] });
    },
  });
}

// --- Forms / Workflows / Compliance ---

export function useFormDefinitions() {
  return useQuery({ queryKey: ['forms'], queryFn: () => formApi.list({ active: true }) });
}

export function useFormSubmissions(formId?: string) {
  return useQuery({
    queryKey: ['form-submissions', formId],
    queryFn: () => formApi.submissions({ form_id: formId }),
  });
}

export function useSubmitForm() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, values }: { id: string; values: Record<string, unknown> }) =>
      formApi.submit(id, { values }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['form-submissions'] }),
  });
}

export function useWorkflowDefinitions() {
  return useQuery({ queryKey: ['workflows'], queryFn: () => workflowApi.list({ active: true }) });
}

export function useWorkflowRuns() {
  return useQuery({ queryKey: ['workflow-runs'], queryFn: () => workflowApi.runs() });
}

export function useTriggerWorkflow() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => workflowApi.trigger(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['workflow-runs'] }),
  });
}

export function useApproveWorkflowRun() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, decision }: { id: string; decision: 'approved' | 'rejected' }) =>
      workflowApi.approve(id, decision),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['workflow-runs'] }),
  });
}

export function useComplianceRules() {
  return useQuery({ queryKey: ['compliance-rules'], queryFn: () => complianceApi.rules() });
}

export function useComplianceResults() {
  return useQuery({ queryKey: ['compliance-results'], queryFn: () => complianceApi.results() });
}

export function useComplianceScore() {
  return useQuery({ queryKey: ['compliance-score'], queryFn: () => complianceApi.score() });
}

export function useEvaluateCompliance() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => complianceApi.evaluate(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['compliance-results'] });
      queryClient.invalidateQueries({ queryKey: ['compliance-score'] });
    },
  });
}

// --- IGA ---

export function useIGAConnectors(params: ListParams = {}) {
  return useQuery({
    queryKey: ['iga-connectors', params],
    queryFn: () => igaApi.connectors(params),
  });
}
export function useCreateIGAConnector() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: IGACreateConnectorRequest) => igaApi.createConnector(data),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['iga-connectors'] }),
  });
}
export function useTestIGAConnector() {
  return useMutation({ mutationFn: (id: string) => igaApi.testConnector(id) });
}
export function useSyncIGAConnector() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => igaApi.syncConnector(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['iga-drift'] }),
  });
}
export function useIGATasks(params: ListParams & { status?: string } = {}) {
  return useQuery({ queryKey: ['iga-tasks', params], queryFn: () => igaApi.tasks(params) });
}
export function useRetryIGATask() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => igaApi.retryTask(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['iga-tasks'] }),
  });
}
export function useIGAAccessRequests(params: ListParams = {}) {
  return useQuery({
    queryKey: ['iga-access-requests', params],
    queryFn: () => igaApi.accessRequests(params),
  });
}
export function useApproveIGAAccessRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => igaApi.approveAccessRequest(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['iga-access-requests'] }),
  });
}
export function useRejectIGAAccessRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => igaApi.rejectAccessRequest(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['iga-access-requests'] }),
  });
}
export function useIGAReviews(params: ListParams = {}) {
  return useQuery({ queryKey: ['iga-reviews', params], queryFn: () => igaApi.reviews(params) });
}
export function useIGADrift(params: ListParams = {}) {
  return useQuery({ queryKey: ['iga-drift', params], queryFn: () => igaApi.drift(params) });
}
export function useRemediateIGADrift() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => igaApi.remediateDrift(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['iga-drift'] }),
  });
}

// --- Webhooks ---

export function useWebhookList(params: ListParams = {}) {
  return useQuery({
    queryKey: ['webhooks', params],
    queryFn: () => webhookApi.list(params),
  });
}

export function useCreateWebhook() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: WebhookCreateRequest) => webhookApi.create(data),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['webhooks'] }),
  });
}

export function useDeleteWebhook() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => webhookApi.delete(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['webhooks'] }),
  });
}

export function useTestWebhook() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => webhookApi.test(id),
    onSuccess: (_data, id) => {
      queryClient.invalidateQueries({ queryKey: ['webhook-deliveries', id] });
      queryClient.invalidateQueries({ queryKey: ['webhook-dead-letters'] });
    },
  });
}

export function useRetryWebhookDelivery() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, deliveryId }: { id: string; deliveryId: string }) =>
      webhookApi.retry(id, deliveryId),
    onSuccess: (_data, { id }) => {
      queryClient.invalidateQueries({ queryKey: ['webhook-deliveries', id] });
      queryClient.invalidateQueries({ queryKey: ['webhook-dead-letters'] });
    },
  });
}

export function useWebhookDeliveries(id: string | null, params: ListParams = {}) {
  return useQuery({
    queryKey: ['webhook-deliveries', id, params],
    queryFn: () => webhookApi.deliveries(id as string, params),
    enabled: Boolean(id),
  });
}

export function useWebhookDeadLetters(params: ListParams = {}) {
  return useQuery({
    queryKey: ['webhook-dead-letters', params],
    queryFn: () => webhookApi.deadLetters(params),
  });
}

// --- Export ---

export function useExportJobs(params: ListParams = {}) {
  return useQuery({
    queryKey: ['export-jobs', params],
    queryFn: () => exportApi.listJobs(params),
    // Poll while jobs are in flight so the page reflects worker progress.
    refetchInterval: (query) =>
      query.state.data?.data.some((job) => job.status === 'pending' || job.status === 'running')
        ? 2000
        : false,
  });
}

export function useCreateExportJob() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: ExportJobCreateRequest) => exportApi.createJob(data),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['export-jobs'] }),
  });
}

// --- Monitoring ---

export function useMetricPoints(params: MetricQueryParams) {
  return useQuery({
    queryKey: ['monitoring-metrics', params],
    queryFn: () => monitoringApi.queryMetrics(params),
    enabled: Boolean(params.name),
  });
}

export function useCIMetrics(ciId: string | undefined, enabled = false) {
  return useQuery({
    queryKey: ['ci-metrics', ciId],
    queryFn: () => monitoringApi.ciMetrics(ciId!, { step: '15m' }),
    enabled: !!ciId && enabled,
    retry: false,
  });
}

export function useAlertRules() {
  return useQuery({
    queryKey: ['alert-rules'],
    queryFn: () => monitoringApi.listAlerts(),
  });
}

export function useCreateAlertRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: AlertRuleCreateRequest) => monitoringApi.createAlert(data),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['alert-rules'] }),
  });
}

export function useDeleteAlertRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => monitoringApi.deleteAlert(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['alert-rules'] }),
  });
}

// --- Audit ---

export function useAuditLog(params: ListParams = {}) {
  return useQuery({
    queryKey: ['audit', params],
    queryFn: () => auditApi.list(params),
  });
}

export function useVerifyAuditChain() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => auditApi.verify(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['audit'] });
    },
  });
}

// --- Security & Privacy (Epic F) ---

export function useSecurityReport(standard = 'iso27001', enabled = true) {
  return useQuery({
    queryKey: ['security-report', standard],
    queryFn: () => complianceApi.report(standard),
    enabled,
  });
}

export function useRetentionPolicy() {
  return useQuery({
    queryKey: ['privacy-retention'],
    queryFn: () => privacyApi.retention(),
    // A missing policy (404) is a normal first-run state, not an error to retry.
    retry: false,
  });
}

export function useSaveRetentionPolicy() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: { retention_days?: number; mode?: string }) =>
      privacyApi.saveRetention(data),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['privacy-retention'] }),
  });
}

export function useRunErasure() {
  return useMutation({
    mutationFn: () => privacyApi.runErasure(),
  });
}

export function useRotateCredentialKeys() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => credentialApi.rotateKeys(),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['credentials'] }),
  });
}
