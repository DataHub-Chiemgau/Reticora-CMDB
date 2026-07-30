import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ciApi,
  collectorApi,
  topologyApi,
  rackApi,
  assetApi,
  assignmentApi,
  documentApi,
  stocktakeApi,
  ticketApi,
  userApi,
  teamApi,
  roleApi,
  permissionApi,
  slaApi,
  formApi,
  workflowApi,
  complianceApi,
  igaApi,
} from '../api/client';
import type {
  CICreateRequest,
  CIListParams,
  CIUpdateRequest,
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
  TopologyParams,
  RackListParams,
  SLAListParams,
  SLABreachParams,
  SLAPolicyRequest,
  IGACreateConnectorRequest,
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

export function useCollectors(params: ListParams = {}) {
  return useQuery({
    queryKey: ['collectors', params],
    queryFn: () => collectorApi.list(params),
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

export function useRackList(params: RackListParams = {}) {
  return useQuery({
    queryKey: ['racks', params],
    queryFn: () => rackApi.list(params),
  });
}

export function useRackMounts(rackId: string) {
  return useQuery({
    queryKey: ['rack-mounts', rackId],
    queryFn: () => rackApi.listMounts(rackId, { limit: 100 }),
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
    mutationFn: ({ id, data }: { id: string; data: CIUpdateRequest }) => ciApi.update(id, data),
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
