import { currentOperatorToken, useOperatorAuth } from './operatorAuth';

const ADMIN_BASE = '/api/v1/admin';

/** OperatorApiError is a refused or failed request of the operator area. */
export class OperatorApiError extends Error {
  constructor(
    public readonly status: number,
    message: string,
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
    };
    throw new OperatorApiError(
      response.status,
      problem.detail || problem.title || response.statusText,
    );
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}
