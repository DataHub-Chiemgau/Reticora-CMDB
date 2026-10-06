import { useAuthStore } from './authStore';

const API_BASE = '/api/v1';

export function getSessionToken() {
  return useAuthStore.getState().token;
}

export function clearSession() {
  useAuthStore.getState().clearSession();
}

// refreshSession renews the 15-minute access token with the HttpOnly refresh
// cookie (AUT-02). The refresh token is never readable by JavaScript; the
// server rotates the cookie on every refresh.
export async function refreshSession() {
  const { user } = useAuthStore.getState();
  if (!user) {
    return false;
  }

  const response = await fetch(`${API_BASE}/auth/refresh`, {
    method: 'POST',
    credentials: 'same-origin',
  });

  if (!response.ok) {
    useAuthStore.getState().clearSession();
    return false;
  }

  const refreshed = (await response.json()) as {
    token: string;
    expires_at?: string;
    expiresAt?: string;
  };
  const expiresAt = refreshed.expires_at ?? refreshed.expiresAt;
  if (!refreshed.token || !expiresAt) {
    useAuthStore.getState().clearSession();
    return false;
  }

  useAuthStore.getState().setSession({ token: refreshed.token, expiresAt, user });
  return true;
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
