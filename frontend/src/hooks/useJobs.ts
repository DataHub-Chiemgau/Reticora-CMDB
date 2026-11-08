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
  /** When the succeeded job was reverted (POST /jobs/{id}/revert, WP-553); absent while it is not. */
  reverted_at?: string;
  /** The user who reverted the job. */
  reverted_by?: string;
  /** Until when POST /jobs/{id}/revert takes the job (WP-305); absent: finished_at plus the window of its kind. */
  revertible_until?: string;
}

/**
 * JOB_KINDS are the job kinds the server registers (server/jobkinds.go,
 * citype/ddl_job.go, ci/computed_refresh.go) and the tracked kinds of the
 * job API (ci.merge); each has a label under jobs.kinds.<kind>.
 */
export const JOB_KINDS = [
  'ci.bulk',
  'asset.bulk',
  'bulk.revert',
  'ci.merge',
  'export',
  'history.snapshot',
  'template.migrate',
  'metamodel.test',
  'import.run',
  'document.scan',
  'search.reindex',
  'labels.batch',
  'attribute.index',
  'attribute.index.rebuild',
  'attribute.index.key',
  'attribute.computed_backfill',
] as const;
export type JobKind = (typeof JOB_KINDS)[number];

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
  'asset.bulk': 'asset:write',
  'bulk.revert': 'ci:write',
  export: 'export:run',
  'ci.merge': 'ci:delete',
  // E-106: the test run of a metamodel draft (WP-721, WP-799).
  'metamodel.test': 'metamodel:publish',
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

const HOUR_MS = 60 * 60 * 1000;

/**
 * REVERT_WINDOWS_MS are the kinds with a defined revert and its window after
 * the end of the job (E-25): the bulk kinds 24 hours (BLK-02), the merge 30
 * days (CI-12). bulk.revert and every other kind have none.
 */
export const REVERT_WINDOWS_MS: Readonly<Record<string, number>> = {
  'ci.bulk': 24 * HOUR_MS,
  'asset.bulk': 24 * HOUR_MS,
  'ci.merge': 30 * 24 * HOUR_MS,
};

/**
 * revertibleUntil is the end of the revert window of a job: revertible_until
 * of the job, else finished_at plus the window of its kind; undefined for a
 * kind without revert or a job without end.
 */
export function revertibleUntil(job: Job): Date | undefined {
  const window = REVERT_WINDOWS_MS[job.kind];
  if (window === undefined) return undefined;
  if (job.revertible_until) return new Date(job.revertible_until);
  if (!job.finished_at) return undefined;
  return new Date(new Date(job.finished_at).getTime() + window);
}

/**
 * isRevertible reports whether POST /jobs/{id}/revert would take the job: a
 * succeeded job of a kind with revert, not reverted yet (WP-553), within its
 * window. The server still decides (409).
 */
export function isRevertible(job: Job, now: number = Date.now()): boolean {
  if (job.status !== 'succeeded' || job.reverted_at) return false;
  const until = revertibleUntil(job);
  return until !== undefined && until.getTime() > now;
}

/** The answer 200 of a revert without revert job (ci.merge). */
export interface JobReverted {
  job_id: string;
  reverted: boolean;
  reverted_at?: string;
  reverted_by?: string;
}

/** The outcome of POST /jobs/{id}/revert: done (200) or the revert job bulk.revert (202). */
export type RevertOutcome = { kind: 'reverted'; result: JobReverted } | { kind: 'job'; job: Job };

/** revertJob posts the revert of a job and tells 200 from 202 by the body. */
export async function revertJob(id: string): Promise<RevertOutcome> {
  const body = await fetchAPI<JobReverted | Job>(`/jobs/${id}/revert`, { method: 'POST' });
  if (body && typeof body === 'object' && 'reverted' in body) {
    return { kind: 'reverted', result: body };
  }
  return { kind: 'job', job: body as Job };
}

/**
 * useRevertJob reverts a job (E-25): 200 for the merge, 202 with the revert
 * job bulk.revert for a bulk (WP-305), whose progress useJob follows. 409
 * (revert-expired, no revert, already reverted) and 403 reach the caller as
 * ApiError.
 */
export function useRevertJob() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => revertJob(id),
    onSettled: () => queryClient.invalidateQueries({ queryKey: KEY }),
  });
}
