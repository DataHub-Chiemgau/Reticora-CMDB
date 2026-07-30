import { afterEach, describe, expect, it, vi } from 'vitest';
import { AUTH_STORAGE_KEY } from './authStore';

const futureExpiry = '2999-01-01T00:00:00Z';
const session = {
  token: 'session-token',
  expiresAt: futureExpiry,
  user: {
    sub: 'user-1',
    email: 'user@example.com',
    name: 'Test User',
    groups: [],
    permissions: [],
  },
};

afterEach(() => {
  window.sessionStorage.clear();
  vi.resetModules();
});

describe('authStore', () => {
  it('sets, persists, and clears the current session', async () => {
    const { useAuthStore } = await import('./authStore');

    useAuthStore.getState().setSession(session);

    expect(useAuthStore.getState().token).toBe('session-token');
    expect(useAuthStore.getState().user?.email).toBe('user@example.com');
    expect(useAuthStore.getState().isAuthenticated()).toBe(true);
    expect(JSON.parse(window.sessionStorage.getItem(AUTH_STORAGE_KEY) ?? '{}')).toMatchObject(
      session,
    );

    useAuthStore.getState().clearSession();

    expect(useAuthStore.getState().token).toBeNull();
    expect(useAuthStore.getState().user).toBeNull();
    expect(useAuthStore.getState().isAuthenticated()).toBe(false);
    expect(window.sessionStorage.getItem(AUTH_STORAGE_KEY)).toBeNull();
  });

  it('rehydrates a stored session on load', async () => {
    window.sessionStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify(session));
    vi.resetModules();

    const { useAuthStore } = await import('./authStore');

    expect(useAuthStore.getState().token).toBe('session-token');
    expect(useAuthStore.getState().user?.name).toBe('Test User');
    expect(useAuthStore.getState().isAuthenticated()).toBe(true);
  });
});
