/**
 * WP-887 (PKG-07, SEC-07): the operator session of the SPA. It ends at
 * expires_at without refresh; operator and tenant session are stored apart
 * and signing out of one leaves the other; operatorClient sends only the
 * operator token, a 401 ends the operator session; the tenant client never
 * sends the operator token.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useAuthStore } from '../auth/authStore';
import {
  OPERATOR_SESSION_KEY,
  currentOperatorToken,
  isSessionValid,
  useOperatorAuth,
} from './operatorAuth';
import { OperatorApiError, operatorFetch } from './operatorClient';
import { fetchAPI } from '../api/client';

const OPERATOR = { id: 'op-1', name: 'Olga', kind: 'oidc' };

function operatorSession(expiresInMs: number) {
  return {
    token: 'op-token',
    expiresAt: new Date(Date.now() + expiresInMs).toISOString(),
    operator: OPERATOR,
  };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

beforeEach(() => {
  useOperatorAuth.getState().clearSession();
  useAuthStore.getState().clearSession();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});

describe('operator session', () => {
  it('expires at expires_at and asks for a new login', () => {
    vi.useFakeTimers();
    useOperatorAuth.getState().setSession(operatorSession(15 * 60_000));
    expect(currentOperatorToken()).toBe('op-token');
    vi.advanceTimersByTime(15 * 60_000 + 1);
    expect(isSessionValid(useOperatorAuth.getState().session)).toBe(false);
    expect(currentOperatorToken()).toBeNull();
    expect(useOperatorAuth.getState().session).toBeNull();
    expect(window.sessionStorage.getItem(OPERATOR_SESSION_KEY)).toBeNull();
  });

  it('is stored apart from the tenant session; signing out of one keeps the other', () => {
    useAuthStore.getState().setSession({
      token: 'tenant-token',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    useOperatorAuth.getState().setSession(operatorSession(60_000));
    expect(window.sessionStorage.getItem(OPERATOR_SESSION_KEY)).toContain('op-token');
    expect(window.sessionStorage.getItem(OPERATOR_SESSION_KEY)).not.toContain('tenant-token');

    useOperatorAuth.getState().clearSession();
    expect(useAuthStore.getState().token).toBe('tenant-token');

    useOperatorAuth.getState().setSession(operatorSession(60_000));
    useAuthStore.getState().clearSession();
    expect(useOperatorAuth.getState().session?.token).toBe('op-token');
  });
});

describe('operatorClient', () => {
  it('sends only the operator token to /api/v1/admin', async () => {
    useAuthStore.getState().setSession({
      token: 'tenant-token',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    useOperatorAuth.getState().setSession(operatorSession(60_000));
    const fetchMock = vi.fn().mockResolvedValue(json(OPERATOR));
    vi.stubGlobal('fetch', fetchMock);
    await operatorFetch('/me');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/admin/me');
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer op-token');
  });

  it('ends the operator session on 401', async () => {
    useOperatorAuth.getState().setSession(operatorSession(60_000));
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(json({ title: 'Unauthorized', status: 401 }, 401)),
    );
    await expect(operatorFetch('/orgs')).rejects.toBeInstanceOf(OperatorApiError);
    expect(useOperatorAuth.getState().session).toBeNull();
  });

  it('sends nothing without a valid session', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    await expect(operatorFetch('/orgs')).rejects.toMatchObject({ status: 401 });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('the tenant client never sends the operator token', async () => {
    useOperatorAuth.getState().setSession(operatorSession(60_000));
    const fetchMock = vi.fn().mockResolvedValue(json({ data: [] }));
    vi.stubGlobal('fetch', fetchMock);
    await fetchAPI('/modules').catch(() => undefined);
    for (const call of fetchMock.mock.calls) {
      const headers = new Headers((call[1] as RequestInit | undefined)?.headers);
      expect(headers.get('Authorization') ?? '').not.toContain('op-token');
    }
  });
});
