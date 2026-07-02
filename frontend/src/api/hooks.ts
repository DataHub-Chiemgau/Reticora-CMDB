import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { ciApi } from '../api/client';
import type { CIListParams, CICreateRequest, CIUpdateRequest } from '../api/client';

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
    mutationFn: ({ id, data }: { id: string; data: CIUpdateRequest }) =>
      ciApi.update(id, data),
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
