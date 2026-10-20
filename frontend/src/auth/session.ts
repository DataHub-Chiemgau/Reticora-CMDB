import { setRefreshHandler, useAuthStore } from './authStore';

const API_BASE = '/api/v1';
// RETRY_AFTER_MAX_MS caps the wait after a 429 on the refresh. Without a
// usable Retry-After the SPA waits the cap: the pre-auth budget is counted
// per minute (AUT-10).
const RETRY_AFTER_MAX_MS = 60_000;
// REFRESH_LOCK is the Web Lock that tabs of the same browser share.
const REFRESH_LOCK = 'reticora-auth-refresh';

export function getSessionToken() {
  return useAuthStore.getState().token;
}

export function clearSession() {
  useAuthStore.getState().clearSession();
}

let inflight: Promise<boolean> | null = null;

// refreshSession renews the 15-minute access token with the HttpOnly refresh
// cookie (AUT-02). The refresh token is never readable by JavaScript; the
// server rotates the cookie on every refresh and treats a second use of a
// consumed cookie as reuse that revokes the session. Concurrent callers
// (parallel 401s, RequireAuth, the planned renewal) therefore share one
// request (single flight, F-065). Only 401 and 403 end the local session; a
// 429 is waited out once (Retry-After, at most 60 s, F-063), and a network
// or server error keeps the session for the next attempt.
export function refreshSession(): Promise<boolean> {
  inflight ??= withRefreshLock(renewSession).finally(() => {
    inflight = null;
  });
  return inflight;
}

// The store plans the renewal about a minute before the token expires.
setRefreshHandler(refreshSession);

// withRefreshLock lets one tab at a time use the shared refresh cookie: a
// duplicated tab whose planned renewal falls due at the same moment sends the
// rotated cookie instead of the consumed one. Without the Web Locks API
// (insecure context) the single flight covers this tab only.
function withRefreshLock(renew: () => Promise<boolean>): Promise<boolean> {
  const locks = navigator.locks as LockManager | undefined;
  return locks ? (locks.request(REFRESH_LOCK, renew) as Promise<boolean>) : renew();
}

async function renewSession() {
  if (!useAuthStore.getState().user) {
    return false;
  }

  let response = await postRefresh();
  if (response?.status === 429) {
    const wait = retryAfterMs(response);
    await new Promise((resolve) => setTimeout(resolve, wait));
    if (!useAuthStore.getState().user) {
      return false;
    }
    response = await postRefresh();
  }
  if (!response) {
    return false;
  }
  if (response.status === 401 || response.status === 403) {
    clearSession();
    return false;
  }
  if (!response.ok) {
    return false;
  }

  const refreshed = (await response.json().catch(() => null)) as {
    token?: string;
    expires_at?: string;
    expiresAt?: string;
  } | null;
  const expiresAt = refreshed?.expires_at ?? refreshed?.expiresAt;
  // A logout while the request ran has cleared the user: no new session.
  const { user } = useAuthStore.getState();
  if (!refreshed?.token || !expiresAt || !user) {
    return false;
  }

  useAuthStore.getState().setSession({ token: refreshed.token, expiresAt, user });
  return true;
}

// postRefresh sends the refresh cookie only; null when the server is not
// reachable.
function postRefresh() {
  return fetch(`${API_BASE}/auth/refresh`, {
    method: 'POST',
    credentials: 'same-origin',
  }).catch(() => null);
}

// retryAfterMs reads Retry-After in seconds or as HTTP date (RFC 9110) and
// keeps it within 0 to 60 s.
function retryAfterMs(response: Response) {
  const value = response.headers.get('Retry-After')?.trim() ?? '';
  const ms = /^\d+$/.test(value) ? Number(value) * 1000 : Date.parse(value) - Date.now();
  if (!Number.isFinite(ms)) {
    return RETRY_AFTER_MAX_MS;
  }
  return Math.min(Math.max(ms, 0), RETRY_AFTER_MAX_MS);
}

// logout ends the session on the server: the refresh cookie's session is
// revoked and the access token blacklisted. The local session is cleared even
// when the server cannot be reached.
export async function logout() {
  const token = getSessionToken();
  try {
    await fetch(`${API_BASE}/auth/logout`, {
      method: 'POST',
      credentials: 'same-origin',
      headers: token ? { Authorization: ['Bearer', token].join(' ') } : undefined,
    });
  } catch {
    // The local session is cleared below in any case.
  } finally {
    clearSession();
  }
}
