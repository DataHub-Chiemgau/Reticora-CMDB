import { useAuthStore } from './authStore';

const API_BASE = '/api/v1';

export function getSessionToken() {
  return useAuthStore.getState().token;
}

export function clearSession() {
  useAuthStore.getState().clearSession();
}

export async function refreshSession() {
  const { token, user } = useAuthStore.getState();
  if (!token || !user) {
    return false;
  }

  const response = await fetch(`${API_BASE}/auth/refresh`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
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
