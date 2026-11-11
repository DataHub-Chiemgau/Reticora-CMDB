import type {
  Entitlement,
  EntitlementListResponse,
  PlanMatrixResponse,
  QuotaUsageResponse,
} from '../api/client';
import { currentOperatorToken, useOperatorAuth } from './operatorAuth';

const ADMIN_BASE = '/api/v1/admin';

/** OperatorApiError is a refused or failed request of the operator area. */
export class OperatorApiError extends Error {
  constructor(
    public readonly status: number,
    message: string,
    /** The request field a 4xx names (problem member field). */
    public readonly field?: string,
  ) {
    super(message);
    this.name = 'OperatorApiError';
  }
}

/**
 * operatorFetch sends a request to /api/v1/admin/* with the operator token
 * only (never the tenant token, WP-887). Without a valid session it fails with
 * 401 before sending; a 401 of the server ends the operator session, so the
 * area shows the login again.
 */
export async function operatorFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  if (!path.startsWith('/')) {
    throw new Error('operatorFetch: the path must start with "/"');
  }
  const token = currentOperatorToken();
  if (!token) {
    throw new OperatorApiError(401, 'no operator session');
  }
  const headers = new Headers(init.headers);
  headers.set('Authorization', `Bearer ${token}`);
  if (init.body !== undefined && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }
  const response = await fetch(`${ADMIN_BASE}${path}`, { ...init, headers, credentials: 'omit' });
  if (response.status === 401) {
    useOperatorAuth.getState().clearSession();
  }
  if (!response.ok) {
    const problem = (await response.json().catch(() => ({}))) as {
      detail?: string;
      title?: string;
      field?: string;
    };
    throw new OperatorApiError(
      response.status,
      problem.detail || problem.title || response.statusText,
      problem.field,
    );
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

/** An organization in the operator listing. */
export interface OrgSummary {
  id: string;
  name: string;
  slug: string;
  plan: string;
  created_at: string;
}

export interface OrgPage {
  data: OrgSummary[];
  total: number;
  has_more: boolean;
  next_cursor?: string;
}

/** An entry of operator_audit. */
export interface OperatorAuditEntry {
  id: number;
  timestamp: string;
  operator_id: string;
  operator_kind: string;
  action: string;
  target_org?: string;
  status: number;
  details: Record<string, unknown>;
}

/** The answer of a plan change (WP-882). */
export interface PlanChangeResult {
  plan: string;
  plan_before: string;
  plan_changed_at: string | null;
  unchanged: boolean;
  changed: string[];
  entitlements: Entitlement[];
}

/** The typed calls of the operator area (PKG-07, WP-888). */
export const operatorApi = {
  orgs(cursor?: string): Promise<OrgPage> {
    const query = new URLSearchParams({ limit: '50' });
    if (cursor) query.set('cursor', cursor);
    return operatorFetch(`/orgs?${query.toString()}`);
  },
  entitlements(orgId: string): Promise<EntitlementListResponse> {
    return operatorFetch(`/orgs/${encodeURIComponent(orgId)}/entitlements`);
  },
  usage(orgId: string): Promise<QuotaUsageResponse> {
    return operatorFetch(`/orgs/${encodeURIComponent(orgId)}/usage`);
  },
  plans(): Promise<PlanMatrixResponse> {
    return operatorFetch('/plans');
  },
  history(orgId: string): Promise<{ data: OperatorAuditEntry[]; total: number }> {
    const query = new URLSearchParams({ limit: '50', target_org: orgId });
    return operatorFetch(`/audit?${query.toString()}`);
  },
  changePlan(orgId: string, plan: string, reason: string): Promise<PlanChangeResult> {
    return operatorFetch(`/orgs/${encodeURIComponent(orgId)}/plan`, {
      method: 'PUT',
      body: JSON.stringify({ plan, reason }),
    });
  },
  bookAddon(orgId: string, key: string, validUntil?: string): Promise<Entitlement> {
    return operatorFetch(`/orgs/${encodeURIComponent(orgId)}/addons/${encodeURIComponent(key)}`, {
      method: 'POST',
      body: JSON.stringify(validUntil ? { valid_until: validUntil } : {}),
    });
  },
  cancelAddon(orgId: string, key: string): Promise<void> {
    return operatorFetch(`/orgs/${encodeURIComponent(orgId)}/addons/${encodeURIComponent(key)}`, {
      method: 'DELETE',
    });
  },
  grant(
    orgId: string,
    body: {
      feature_key: string;
      plan: string;
      enabled: boolean;
      limits: Record<string, number>;
      valid_until?: string;
      source?: string;
    },
  ): Promise<Entitlement> {
    return operatorFetch(`/orgs/${encodeURIComponent(orgId)}/entitlements`, {
      method: 'POST',
      body: JSON.stringify(body),
    });
  },
};
