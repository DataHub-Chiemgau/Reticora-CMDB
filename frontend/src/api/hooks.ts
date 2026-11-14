import {
  keepPreviousData,
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
  alarmApi,
  collectorApi,
  discoveryScopeApi,
  scopeSuggestionApi,
  topologyApi,
  IMPACT_COMPACT_FIELDS,
  siteApi,
  rackApi,
  assetApi,
  assignmentApi,
  supplierApi,
  contractApi,
  contactApi,
  consumableApi,
  orderApi,
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
  businessCalendarApi,
  disposalApi,
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
  sourceRankApi,
  type SourceRankEntry,
  type SourceRankParams,
  type FormDefinitionWrite,
  bffQueryPartial,
  ApiError,
} from '../api/client';
import type {
  BusinessCalendar,
  CreateBusinessCalendarRequest,
  CreateDisposalRecordRequest,
  DisposalListParams,
  UpdateBusinessCalendarRequest,
  DiscoveryScopeInput,
  DiscoveryScopePatch,
  CredentialInput,
  CICreateRequest,
  CIListParams,
  CIUpdateRequest,
  RelationshipCreateRequest,
  RelationshipUpdateRequest,
  ListParams,
  AssetCreateRequest,
  ConsumableAvailabilityList,
  CreateOrderRequest,
  CreateOrderItemRequest,
  ReceiveOrderRequest,
  UpdateOrderRequest,
  AssetUpdateRequest,
  AssetListParams,
  BffPartialResult,
  LinkAssetCIRequest,
  AssignAssetRequest,
  ReturnAssetRequest,
  TransferAssetRequest,
  AssignmentListParams,
  FilteredListParams,
  SupplierCreateRequest,
  SupplierUpdateRequest,
  ContractCreateRequest,
  ContractUpdateRequest,
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
  AlertRuleUpdateRequest,
  AlarmListPage,
  AlarmListParams,
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

/**
 * Creating and deleting a credential (credential:manage, UI-14). Both
 * invalidate the credential list, so the choice of the scope form is current
 * at once.
 */
export function useCredentialMutations() {
  const queryClient = useQueryClient();
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['credentials'] });
  return {
    create: useMutation({
      mutationFn: (input: CredentialInput) => credentialApi.create(input),
      onSuccess: invalidate,
    }),
    remove: useMutation({
      mutationFn: (id: string) => credentialApi.remove(id),
      onSuccess: invalidate,
    }),
  };
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

export function useSites(clientId?: string, options: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['sites', 'options', clientId],
    queryFn: () => siteApi.list({ limit: 200, ...(clientId ? { client_id: clientId } : {}) }),
    ...options,
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

/**
 * useAssetList lists the assets; with keepPrevious the previous page stays
 * shown while a changed column, sort, filter or page loads (WP-319).
 */
export function useAssetList(
  params: AssetListParams,
  { enabled = true, keepPrevious = false }: ListQueryOptions & { keepPrevious?: boolean } = {},
) {
  return useQuery({
    queryKey: ['assets', params],
    queryFn: () => assetApi.list(params),
    enabled,
    placeholderData: keepPrevious ? keepPreviousData : undefined,
  });
}

// --- Suppliers and contracts (MGT-12, WP-808) ---

/** useSuppliers reads one page of the suppliers (cursor, filter[name][like], sort). */
export function useSuppliers(
  params: FilteredListParams = {},
  { enabled = true, keepPrevious = false }: ListQueryOptions & { keepPrevious?: boolean } = {},
) {
  return useQuery({
    queryKey: ['suppliers', params],
    queryFn: () => supplierApi.list(params),
    enabled,
    placeholderData: keepPrevious ? keepPreviousData : undefined,
  });
}

export function useSupplier(id: string | undefined) {
  return useQuery({
    queryKey: ['suppliers', 'detail', id],
    queryFn: () => supplierApi.get(id ?? ''),
    enabled: !!id,
  });
}

export function useCreateSupplier() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: SupplierCreateRequest) => supplierApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['suppliers'] });
    },
  });
}

/** useUpdateSupplier sends version as If-Match (API-07). */
export function useUpdateSupplier() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      data,
      version,
    }: {
      id: string;
      data: SupplierUpdateRequest;
      version?: number;
    }) => supplierApi.update(id, data, version),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['suppliers'] });
    },
  });
}

export function useDeleteSupplier() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => supplierApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['suppliers'] });
    },
  });
}

/** useContracts reads one page of the contracts (cursor, filter[...], sort). */
export function useContracts(
  params: FilteredListParams = {},
  { enabled = true, keepPrevious = false }: ListQueryOptions & { keepPrevious?: boolean } = {},
) {
  return useQuery({
    queryKey: ['contracts', params],
    queryFn: () => contractApi.list(params),
    enabled,
    placeholderData: keepPrevious ? keepPreviousData : undefined,
  });
}

export function useContract(id: string | undefined) {
  return useQuery({
    queryKey: ['contracts', 'detail', id],
    queryFn: () => contractApi.get(id ?? ''),
    enabled: !!id,
  });
}

export function useCreateContract() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: ContractCreateRequest) => contractApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['contracts'] });
    },
  });
}

/** useUpdateContract sends version as If-Match (API-07). */
export function useUpdateContract() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      data,
      version,
    }: {
      id: string;
      data: ContractUpdateRequest;
      version?: number;
    }) => contractApi.update(id, data, version),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['contracts'] });
    },
  });
}

export function useDeleteContract() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => contractApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['contracts'] });
    },
  });
}

/** useContacts reads one page of the contacts for a selection (display_name). */
export function useContacts({ enabled = true }: ListQueryOptions = {}) {
  return useQuery({
    queryKey: ['contacts', 'picker'],
    queryFn: () => contactApi.list({ limit: 200 }),
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
    // version (WP-740) is sent as If-Match like useUpdateCI.
    mutationFn: ({
      id,
      data,
      version,
    }: {
      id: string;
      data: AssetUpdateRequest;
      version?: number;
    }) => assetApi.update(id, data, version),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['assets'] });
    },
  });
}

/** useAsset reads one asset with version, holder and active reservation (WP-809). */
export function useAsset(id: string | undefined) {
  return useQuery({
    queryKey: ['assets', 'detail', id],
    queryFn: () => assetApi.get(id!),
    enabled: !!id,
  });
}

/** AssetDetailCI is a CI of the asset aggregate: a linked CI or a child CI (AST-05). */
export interface AssetDetailCI {
  id: string;
  name: string;
  status: string;
  lifecycleState?: string | null;
  serialNumber?: string | null;
  locationId?: string | null;
  assetId?: string | null;
  parentCiId?: string | null;
  childMode?: 'configuration' | 'standalone' | null;
}

/** AssetDetailDocument is a document of the asset aggregate (GQL-03). */
export interface AssetDetailDocument {
  id: string;
  title: string;
  description?: string | null;
  fileName: string;
  fileSize: number;
  mimeType: string;
  version: number;
  category: string;
  tags: string[];
  uploadedBy?: string | null;
  scanStatus?: string | null;
  createdAt: string;
  updatedAt: string;
}

/** AssetDetailAggregate holds the lists of the asset detail page; a refused list is null. */
export interface AssetDetailAggregate {
  asset: {
    id: string;
    primaryCiId?: string | null;
    cis?: AssetDetailCI[] | null;
    children?: AssetDetailCI[] | null;
    documents?: AssetDetailDocument[] | null;
  } | null;
}

const ASSET_DETAIL_CI_FIELDS =
  'id name status lifecycleState serialNumber locationId assetId parentCiId childMode';

/**
 * ASSET_DETAIL_QUERY reads the CIs, the child CIs and the documents of an
 * asset in one BFF request (GQL-03, WP-311); the writes go through REST.
 */
export const ASSET_DETAIL_QUERY = `query AssetDetail($id: ID!) {
  asset(id: $id) {
    id
    primaryCiId
    cis(first: 50) { ${ASSET_DETAIL_CI_FIELDS} }
    children(first: 50) { ${ASSET_DETAIL_CI_FIELDS} }
    documents(first: 50) {
      id title description fileName fileSize mimeType version category tags uploadedBy scanStatus createdAt updatedAt
    }
  }
}`;

/** assetDetailQuery is the query of useAssetDetail, also for reads outside the hook. */
export function assetDetailQuery(id: string) {
  return {
    queryKey: ['assets', 'detail', id, 'aggregate'] as const,
    queryFn: (): Promise<BffPartialResult<AssetDetailAggregate>> =>
      bffQueryPartial<AssetDetailAggregate>(ASSET_DETAIL_QUERY, { id }),
  };
}

/**
 * useAssetDetail reads the lists of the asset detail page in exactly one
 * BFF query (WP-809); a field the server refuses comes back as an error of
 * its path, the other lists stay.
 */
export function useAssetDetail(id: string | undefined) {
  return useQuery({ ...assetDetailQuery(id ?? ''), enabled: !!id });
}

/** invalidateAsset reads an asset, its aggregate and the lists again. */
function invalidateAsset(queryClient: ReturnType<typeof useQueryClient>) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: ['assets'] }),
    queryClient.invalidateQueries({ queryKey: ['ci'] }),
  ]);
}

/** useLinkAssetCI links a CI to an asset (POST /assets/{id}/cis, AST-04). */
export function useLinkAssetCI() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: LinkAssetCIRequest }) =>
      assetApi.linkCI(id, data),
    onSuccess: () => invalidateAsset(queryClient),
  });
}

/** useUnlinkAssetCI releases a CI from an asset (DELETE /assets/{id}/cis/{ci_id}). */
export function useUnlinkAssetCI() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ciId }: { id: string; ciId: string }) => assetApi.unlinkCI(id, ciId),
    onSuccess: () => invalidateAsset(queryClient),
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

/** IMPORT_ERRORS_PAGE_LIMIT is the number of row errors on one page of the report. */
export const IMPORT_ERRORS_PAGE_LIMIT = 50;

/**
 * useImportErrors reads one page of the row errors of a finished run (GET
 * /imports/{id}/errors, limit and cursor); the next page follows from
 * next_cursor. The previous page stays visible while the next one loads.
 */
export function useImportErrors(id: string, cursor: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: ['imports', 'errors', id, cursor ?? null],
    queryFn: () => importApi.errors(id, { limit: IMPORT_ERRORS_PAGE_LIMIT, cursor }),
    enabled: !!id && enabled,
    placeholderData: keepPreviousData,
  });
}

// --- Stocktakes ---

// --- Orders (UI-14, WP-812) ---

export type OrderListQuery = ListParams & { status?: string };

export function useOrderList(params: OrderListQuery, { enabled = true }: ListQueryOptions = {}) {
  return useQuery({
    queryKey: ['orders', params],
    queryFn: () => orderApi.list(params),
    enabled,
  });
}

/** useOrder reads one order with its positions (GET /orders/{id}). */
export function useOrder(id: string | undefined) {
  return useQuery({
    queryKey: ['orders', 'detail', id],
    queryFn: () => orderApi.get(id as string),
    enabled: !!id,
  });
}

/** useOrderMutation runs an order request and reloads every order query afterwards. */
function useOrderMutation<V>(mutationFn: (vars: V) => Promise<unknown>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn,
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['orders'] }),
  });
}

export function useCreateOrder() {
  return useOrderMutation((data: CreateOrderRequest) => orderApi.create(data));
}

export function useUpdateOrder() {
  return useOrderMutation(({ id, data }: { id: string; data: UpdateOrderRequest }) =>
    orderApi.update(id, data),
  );
}

export function useDeleteOrder() {
  return useOrderMutation((id: string) => orderApi.delete(id));
}

export function useAddOrderItem() {
  return useOrderMutation(({ id, data }: { id: string; data: CreateOrderItemRequest }) =>
    orderApi.addItem(id, data),
  );
}

/** OrderTransition is an action of the order state machine without a body. */
export type OrderTransition = 'submit' | 'approve' | 'reject' | 'place' | 'cancel' | 'close';

export function useOrderTransition() {
  return useOrderMutation(({ id, action }: { id: string; action: OrderTransition }) =>
    orderApi[action](id),
  );
}

export function useReceiveOrder() {
  return useOrderMutation(
    ({ id, data, key }: { id: string; data: ReceiveOrderRequest; key: string }) =>
      orderApi.receive(id, data, key),
  );
}

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

/**
 * useUser reads one user (GET /users/{id}, user:read) to show its name, e.g.
 * the owner of a CI (CI-07, WP-805). A refusal (403 without user:read) or an
 * unknown id is not retried: the caller shows the kind without a name.
 */
export function useUser(id: string | undefined, { enabled = true }: ListQueryOptions = {}) {
  return useQuery({
    queryKey: ['user', id],
    queryFn: () => userApi.get(id!),
    enabled: enabled && !!id,
    retry: false,
  });
}

/** useTeam reads one team (GET /teams/{id}, user:read) like useUser (WP-805). */
export function useTeam(id: string | undefined, { enabled = true }: ListQueryOptions = {}) {
  return useQuery({
    queryKey: ['team', id],
    queryFn: () => teamApi.get(id!),
    enabled: enabled && !!id,
    retry: false,
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

// --- Business calendars (TKT-02, WP-768, WP-815) ---

/**
 * useBusinessCalendarPages reads the business calendars page by page over
 * next_cursor (API-02); the calendar section of the SLA page loads more on
 * request.
 */
export function useBusinessCalendarPages() {
  return useInfiniteQuery({
    queryKey: ['business-calendars', 'pages'],
    queryFn: ({ pageParam }) => businessCalendarApi.list({ cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: { has_more: boolean; next_cursor?: string }) =>
      last.has_more ? last.next_cursor || undefined : undefined,
  });
}

/** useAllBusinessCalendars reads every visible calendar for the calendar choice of a policy. */
export function useAllBusinessCalendars() {
  return useQuery({
    queryKey: ['business-calendars', 'all'],
    queryFn: () => businessCalendarApi.listAll(),
  });
}

export function useCreateBusinessCalendar() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateBusinessCalendarRequest) => businessCalendarApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['business-calendars'] });
    },
  });
}

/** useUpdateBusinessCalendar sends the changes of a calendar; never client_id. */
export function useUpdateBusinessCalendar() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdateBusinessCalendarRequest }) =>
      businessCalendarApi.update(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['business-calendars'] });
    },
  });
}

export function useDeleteBusinessCalendar() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (calendar: Pick<BusinessCalendar, 'id'>) => businessCalendarApi.delete(calendar.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['business-calendars'] });
      // Policies naming the calendar fall back to the calendar chain.
      queryClient.invalidateQueries({ queryKey: ['slas'] });
    },
  });
}

// --- Disposal (E-55, WP-751, WP-815) ---

export function useDisposalRecords(params: DisposalListParams = {}) {
  return useQuery({
    queryKey: ['disposal-records', params],
    queryFn: () => disposalApi.list(params),
  });
}

/** useCreateDisposalRecord appends a record; the asset or CI changes its lifecycle state. */
export function useCreateDisposalRecord() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateDisposalRecordRequest) => disposalApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['disposal-records'] });
      queryClient.invalidateQueries({ queryKey: ['assets'] });
      queryClient.invalidateQueries({ queryKey: ['cis'] });
    },
  });
}

// --- Forms / Workflows / Compliance ---

export function useFormDefinitions() {
  return useQuery({ queryKey: ['forms'], queryFn: () => formApi.list({ active: true }) });
}

/** useFormDefinition reads one form definition for the editor (WP-1133). */
export function useFormDefinition(id: string | undefined) {
  return useQuery({
    queryKey: ['forms', 'detail', id ?? ''],
    queryFn: () => formApi.get(id ?? ''),
    enabled: !!id,
  });
}

/** useFormVersions lists the schema versions of a form, newest first. */
export function useFormVersions(id: string | undefined) {
  return useQuery({
    queryKey: ['forms', 'versions', id ?? ''],
    queryFn: () => formApi.versions(id ?? ''),
    enabled: !!id,
  });
}

/**
 * useSaveForm creates (no id) or changes a form with If-Match on version;
 * the lists, the definition and its versions refresh.
 */
export function useSaveForm() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      data,
      version,
    }: {
      id?: string;
      data: FormDefinitionWrite;
      version?: number;
    }) => (id ? formApi.update(id, data, version) : formApi.create(data)),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['forms'] }),
  });
}

export function useFormSubmissions(formId?: string) {
  return useQuery({
    queryKey: ['form-submissions', formId],
    queryFn: () => formApi.submissions({ form_id: formId }),
  });
}

/** useAllFormSubmissions reads every submission of a form (grouped by version, WP-1133). */
export function useAllFormSubmissions(formId: string | undefined) {
  return useQuery({
    queryKey: ['form-submissions', 'all', formId ?? ''],
    queryFn: () => formApi.allSubmissions(formId ?? ''),
    enabled: !!formId,
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

/** useAlarms loads the alarms of the filters page by page over next_cursor (WP-991). */
export function useAlarms(params: Omit<AlarmListParams, 'cursor'>) {
  return useInfiniteQuery({
    queryKey: ['alarms', params],
    queryFn: ({ pageParam }) => alarmApi.list({ ...params, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: AlarmListPage) =>
      last.has_more ? last.next_cursor || undefined : undefined,
  });
}

/** useAlarm reads an alarm with its transitions. */
export function useAlarm(id: string | undefined) {
  return useQuery({
    queryKey: ['alarm', id],
    queryFn: () => alarmApi.get(id!),
    enabled: !!id,
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

/** PATCH /monitoring/alerts/{id} (WP-990). */
export function useUpdateAlertRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: AlertRuleUpdateRequest }) =>
      monitoringApi.updateAlert(id, data),
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

/**
 * useSourceRanks reads the source ranks of the organization (GET
 * /reconciliation/source-ranks, WP-1115): without parameters every stored
 * entry, with ci_type_id and attribute_keys the effective tables.
 */
export function useSourceRanks(params: SourceRankParams = {}, enabled = true) {
  return useQuery({
    queryKey: ['source-ranks', params],
    queryFn: () => sourceRankApi.get(params),
    enabled,
  });
}

/** useReplaceSourceRanks replaces every entry of the organization (PUT). */
export function useReplaceSourceRanks() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (entries: SourceRankEntry[]) => sourceRankApi.replace(entries),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['source-ranks'] }),
  });
}
