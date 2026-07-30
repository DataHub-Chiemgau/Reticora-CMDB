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
