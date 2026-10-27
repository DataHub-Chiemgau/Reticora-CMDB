import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { fetchAPI, type PaginatedResponse } from '../api/client';
import { useEffectivePermissions } from '../api/hooks';

export const JOB_STATUSES = ['queued', 'running', 'succeeded', 'failed', 'cancelled'] as const;
export type JobStatus = (typeof JOB_STATUSES)[number];

/** A background job of the job model (JOB-01). */
export interface Job {
  id: string;
  kind: string;
  status: JobStatus;
  progress: number;
  error?: string;
  created_by?: string;
  attempts: number;
  max_attempts: number;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  /** The result of a finished job (its shape depends on the kind). */
  result?: unknown;
  /** Whether DELETE /jobs/{id} cancels the job now. */
  cancellable?: boolean;
}

export interface JobFilter {
  status?: string;
  kind?: string;
}

const KEY = ['jobs'];

/** isActive reports whether a job may still change. */
export function isActive(job: Job): boolean {
  return job.status === 'queued' || job.status === 'running';
}

/**
 * isCancellable reports whether DELETE /jobs/{id} would cancel the job: a
 * queued job of any kind, a running one only where the server says so
 * (cancellable, JOB-03); a running job of another kind answers 409.
 */
export function isCancellable(job: Job): boolean {
  return job.status === 'queued' || (job.status === 'running' && job.cancellable === true);
}

/**
 * JOB_KIND_PERMISSIONS names the permission that controls the jobs of a kind
 * besides their creator, as the job API registers it (server/router.go,
 * jobs.APIHandler.WithKind). A kind without an entry is controlled by its
 * creator only.
 */
export const JOB_KIND_PERMISSIONS: Record<string, string> = {
  'ci.bulk': 'ci:write',
  export: 'export:run',
  'ci.merge': 'ci:delete',
};

/**
 * useMayControlJob returns whether the signed-in user may cancel a job: its
 * creator or a holder of the permission of its kind (jobs.mayControl). It
 * fails closed while the effective rights load or cannot be read; the server
 * still decides every request (403).
 */
export function useMayControlJob(): (job: Job) => boolean {
  const effective = useEffectivePermissions();
  const me = effective.data;
  return (job) => {
    if (!me) return false;
    if (me.user_id && job.created_by === me.user_id) return true;
    const permission = JOB_KIND_PERMISSIONS[job.kind];
    return permission !== undefined && (me.permissions ?? []).includes(permission);
  };
}

export interface JobsOptions {
  /** follow keeps reading the list while no job is active, e.g. for a partial report still due. */
  follow?: boolean;
}

/** useJobs lists the jobs; while one is active (or follow is set) the list refreshes. */
export function useJobs(filter: JobFilter, options: JobsOptions = {}) {
  return useQuery({
    queryKey: [...KEY, filter],
    queryFn: () => {
      const params = new URLSearchParams({ limit: '100' });
      if (filter.status) params.set('status', filter.status);
      if (filter.kind) params.set('kind', filter.kind);
      return fetchAPI<PaginatedResponse<Job>>(`/jobs?${params.toString()}`);
    },
    refetchInterval: (query) =>
      options.follow || query.state.data?.data.some(isActive) ? 3_000 : false,
  });
}

/** JOB_POLL_INTERVAL_MS is the pause between two reads of a followed job. */
export const JOB_POLL_INTERVAL_MS = 1_000;

/**
 * useJob follows one job (GET /jobs/{id}): it reads the job again while it
 * is queued or running and stops once it is finished.
 */
export function useJob(id: string | null) {
  return useQuery({
    queryKey: [...KEY, id],
    queryFn: () => fetchAPI<Job>(`/jobs/${id}`),
    enabled: !!id,
    refetchInterval: (query) =>
      query.state.data && !isActive(query.state.data) ? false : JOB_POLL_INTERVAL_MS,
  });
}

/** useJobResult reads the result of a succeeded job. */
export function useJobResult(id: string | null) {
  return useQuery({
    queryKey: [...KEY, id, 'result'],
    queryFn: () => fetchAPI<unknown>(`/jobs/${id}/result`),
    enabled: !!id,
    retry: false,
  });
}

export function useCancelJob() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => fetchAPI<Job>(`/jobs/${id}`, { method: 'DELETE' }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: KEY }),
  });
}
