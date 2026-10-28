import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
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
  documentApi,
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
  AssetUpdateRequest,
  AssetListParams,
  AssignmentCreateRequest,
  AssignmentListParams,
  DocumentCreateRequest,
  DocumentListParams,
  StocktakeCreateRequest,
  StocktakeListParams,
  TicketCreateRequest,
  TicketUpdateRequest,
  TicketListParams,
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

export function useCIList(params: CIListParams) {
  return useQuery({
    queryKey: ['cis', params],
    queryFn: () => ciApi.list(params),
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
 * client-side replacement (F-326).
 */
export function useImpactMany(ids: string[], relType?: string) {
  return useQuery({
    queryKey: ['topology-impact-many', ids, relType],
    queryFn: () =>
      topologyApi.impactMany(ids, {
        rel_type: relType || undefined,
        fields: IMPACT_COMPACT_FIELDS,
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
 * Resolves the CIs referenced by rack mounts by id. A rack holds at most a few
 * dozen mounts, so the CIs are fetched individually instead of relying on a
 * single CI page, which would leave every CI beyond the page limit unnamed.
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

export function useAssetList(params: AssetListParams) {
  return useQuery({
    queryKey: ['assets', params],
    queryFn: () => assetApi.list(params),
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

export function useCreateAssignment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: AssignmentCreateRequest) => assignmentApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['assignments'] });
    },
  });
}

export function useReturnAssignment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      data,
    }: {
      id: string;
      data: { return_condition?: string; notes?: string };
    }) => assignmentApi.returnAssignment(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['assignments'] });
    },
  });
}

export function useTransferAssignment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: { new_assignee: string; notes?: string } }) =>
      assignmentApi.transfer(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['assignments'] });
    },
  });
}

// --- Documents ---

export function useDocumentList(params: DocumentListParams) {
  return useQuery({
    queryKey: ['documents', params],
    queryFn: () => documentApi.list(params),
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

// --- Stocktakes ---

export function useStocktakeList(params: StocktakeListParams) {
  return useQuery({
    queryKey: ['stocktakes', params],
    queryFn: () => stocktakeApi.list(params),
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

export function useDeleteStocktake() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => stocktakeApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['stocktakes'] });
    },
  });
}

export function useStocktakeDifference(stocktakeId: string, params: ListParams = {}) {
  return useQuery({
    queryKey: ['stocktakes', stocktakeId, 'difference', params],
    queryFn: () => stocktakeApi.difference(stocktakeId, params),
    enabled: !!stocktakeId,
  });
}

export function useCompleteStocktake() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, applyCorrections = true }: { id: string; applyCorrections?: boolean }) =>
      stocktakeApi.complete(id, applyCorrections),
    onSuccess: (_data, { id }) => {
      queryClient.invalidateQueries({ queryKey: ['stocktakes'] });
      queryClient.invalidateQueries({ queryKey: ['stocktakes', id] });
      queryClient.invalidateQueries({ queryKey: ['assets'] });
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

// --- Users / Teams / Roles ---

export function useUserList(params: UserListParams) {
  return useQuery({
    queryKey: ['users', params],
    queryFn: () => userApi.list(params),
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

export function useTeamList(params: UserListParams) {
  return useQuery({
    queryKey: ['teams', params],
    queryFn: () => teamApi.list(params),
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
