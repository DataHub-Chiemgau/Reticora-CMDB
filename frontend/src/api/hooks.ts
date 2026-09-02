import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ciApi,
  relationshipApi,
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
  searchApi,
  aiApi,
  webhookApi,
  exportApi,
  monitoringApi,
  auditApi,
  privacyApi,
  credentialApi,
} from '../api/client';
import type {
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

export function useCreateRelationship(ciId: string) {
  const invalidate = useRelationshipInvalidation(ciId);
  return useMutation({
    mutationFn: (data: RelationshipCreateRequest) => relationshipApi.create(data),
    onSuccess: invalidate,
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
  return useMutation({
    mutationFn: (id: string) => relationshipApi.delete(id),
    onSuccess: invalidate,
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

export function useCIImpact(id: string, relType?: string) {
  return useQuery({
    queryKey: ['topology-impact', id, relType],
    queryFn: () => topologyApi.impact(id, { rel_type: relType || undefined }),
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

export function useSecurityReport(standard = 'iso27001') {
  return useQuery({
    queryKey: ['security-report', standard],
    queryFn: () => complianceApi.report(standard),
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
