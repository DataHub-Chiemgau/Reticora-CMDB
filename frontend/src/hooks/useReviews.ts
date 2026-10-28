import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { fetchAPI, type PaginatedResponse } from '../api/client';

/** The review types of REC-08 in catalog order. */
export const REVIEW_KINDS = [
  'unclassified_device',
  'ambiguous_identity',
  'conflicting_values',
  'probable_duplicate',
  'override_conflict',
  'resurrected_device',
  'unlicensed_ci',
  'scope_suggested',
  'serial_mismatch',
  'duplicate_ip',
  'unknown_relationship',
] as const;

export type ReviewAction = 'merge' | 'new' | 'dismiss' | 'accept';

/** The actions that resolve an item of each type (reviews.AllowedActions). */
export const REVIEW_ACTIONS: Record<string, ReviewAction[]> = {
  unclassified_device: ['new', 'dismiss'],
  ambiguous_identity: ['merge', 'new', 'dismiss'],
  conflicting_values: ['accept', 'dismiss'],
  probable_duplicate: ['merge', 'dismiss'],
  override_conflict: ['accept', 'dismiss'],
  resurrected_device: ['merge', 'new', 'dismiss'],
  unlicensed_ci: ['new', 'dismiss'],
  scope_suggested: ['accept', 'dismiss'],
  serial_mismatch: ['dismiss'],
  duplicate_ip: ['dismiss'],
  unknown_relationship: ['accept', 'dismiss'],
};

/**
 * reviewActions are the actions the inbox offers for an item: those of its
 * type, without new for an unclassified_device item whose generic_device CI
 * exists (reason no_profile_match): new is refused (409) for it; the next
 * step is to set the type on that CI (UI-16, F-176).
 */
export function reviewActions(item: Pick<ReviewItem, 'kind' | 'payload'>): ReviewAction[] {
  const actions = REVIEW_ACTIONS[item.kind] ?? ['dismiss'];
  if (item.kind === 'unclassified_device' && item.payload?.reason === 'no_profile_match') {
    return actions.filter((action) => action !== 'new');
  }
  return actions;
}

/**
 * needsCIType reports whether new must ask for the type of the CI: the type
 * of an unclassified_device item with reason unknown_ci_type is unknown to
 * the organization, the resolution names it (ci_type_id).
 */
export function needsCIType(item: Pick<ReviewItem, 'kind' | 'payload'>): boolean {
  return item.kind === 'unclassified_device' && item.payload?.reason === 'unknown_ci_type';
}

export interface ReviewItem {
  id: string;
  kind: string;
  status: 'open' | 'resolved' | 'dismissed';
  payload: Record<string, unknown>;
  candidate_ci_ids?: string[];
  object_key?: string;
  action?: string;
  resolved_by?: string;
  resolved_at?: string;
  escalated: boolean;
  created_at: string;
}

export interface ReviewFilter {
  status?: string;
  kind?: string;
  escalated?: boolean;
}

export interface ReviewResolution {
  action: ReviewAction;
  ci_id?: string;
  /** ci_type_id is the type of the CI new creates (UUID of a CI type). */
  ci_type_id?: string;
}

/** REVIEW_PAGE_SIZE is the number of items one page of the inbox lists. */
export const REVIEW_PAGE_SIZE = 100;

function query(filter: ReviewFilter, offset = 0) {
  const params = new URLSearchParams({ limit: String(REVIEW_PAGE_SIZE) });
  if (offset > 0) params.set('offset', String(offset));
  if (filter.status) params.set('status', filter.status);
  if (filter.kind) params.set('kind', filter.kind);
  if (filter.escalated) params.set('escalated', 'true');
  return params.toString();
}

function reviewPage(filter: ReviewFilter, offset: number) {
  return fetchAPI<PaginatedResponse<ReviewItem>>(`/review-items?${query(filter, offset)}`);
}

export function resolveReview(id: string, resolution: ReviewResolution): Promise<ReviewItem> {
  return fetchAPI(`/review-items/${id}/resolve`, {
    method: 'POST',
    body: JSON.stringify(resolution),
  });
}

/**
 * useReviewItems lists one page of the review inbox (UI-16): offset is the
 * first item of the page; total and has_more of the answer tell the inbox
 * how many items match and whether another page follows (F-345).
 */
export function useReviewItems(filter: ReviewFilter, offset = 0) {
  return useQuery({
    queryKey: ['review-items', filter, offset],
    queryFn: () => reviewPage(filter, offset),
  });
}

/**
 * useEscalatedReviewCount reads how many open items are escalated, i.e. open
 * longer than 14 days (REC-08): the total of GET
 * /review-items?escalated=true&status=open with one row. enabled holds the
 * request back, e.g. while the right to read the items is not confirmed.
 * The key starts with review-items, so a resolution refreshes it.
 */
export function useEscalatedReviewCount(enabled = true) {
  return useQuery({
    queryKey: ['review-items', 'escalated-count'],
    queryFn: () =>
      fetchAPI<PaginatedResponse<ReviewItem>>('/review-items?escalated=true&status=open&limit=1'),
    select: (page) => page.total ?? 0,
    enabled,
  });
}

/**
 * fetchAllReviewItems loads every item that matches the filter page by page,
 * following has_more with the next offset, so a bulk resolution can cover
 * all of them, not only the page shown (F-345).
 */
export async function fetchAllReviewItems(filter: ReviewFilter): Promise<ReviewItem[]> {
  const all: ReviewItem[] = [];
  for (let offset = 0; ;) {
    const page = await reviewPage(filter, offset);
    const rows = page.data ?? [];
    all.push(...rows);
    if (!page.has_more || rows.length === 0) return all;
    offset += rows.length;
  }
}

/** useResolveReview resolves one item. */
export function useResolveReview() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, resolution }: { id: string; resolution: ReviewResolution }) =>
      resolveReview(id, resolution),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['review-items'] }),
  });
}

export interface BulkResult {
  resolved: string[];
  failed: { id: string; message: string }[];
}

/**
 * useBulkResolveReviews resolves several items with one action, one request
 * per item; an item that fails (already resolved, action not allowed) is
 * reported and does not stop the others.
 */
export function useBulkResolveReviews() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      ids,
      action,
    }: {
      ids: string[];
      action: ReviewAction;
    }): Promise<BulkResult> => {
      const result: BulkResult = { resolved: [], failed: [] };
      for (const id of ids) {
        try {
          await resolveReview(id, { action });
          result.resolved.push(id);
        } catch (err) {
          result.failed.push({ id, message: err instanceof Error ? err.message : String(err) });
        }
      }
      return result;
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['review-items'] }),
  });
}
