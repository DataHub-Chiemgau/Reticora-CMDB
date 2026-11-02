/**
 * Login error images of INS-04: an unreachable backend and an insecure
 * origin produce an explanation, not the browser's raw error. The entry to
 * the self-signup is always there (UI-17, F-351). After an accepted
 * invitation the page offers to sign out another account of this browser,
 * whose IdP session the sign-in would reuse (F-351).
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';

const start = vi.fn();
vi.mock('../../auth/oidc', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../auth/oidc')>();
  return { ...actual, startAuthorizationCodeFlow: (...args: unknown[]) => start(...args) };
});

const { LoginPage } = await import('./LoginPage');
const { InsecureContextError } = await import('../../auth/oidc');

const AUTH_CONFIG = {
  issuer: 'https://id.example/realms/reticora',
  client_id: 'reticora-app',
  redirect_uri: 'https://cmdb.example/auth/callback',
  scopes: ['openid'],
  authorization_endpoint: 'https://id.example/realms/reticora/protocol/openid-connect/auth',
  end_session_endpoint: 'https://id.example/realms/reticora/protocol/openid-connect/logout',
  pkce_required: true,
};

afterEach(() => {
  start.mockReset();
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});

describe('LoginPage', () => {
  it('explains an unreachable backend instead of "Failed to fetch"', async () => {
    start.mockRejectedValue(new TypeError('Failed to fetch'));
    renderWithProviders(<LoginPage />, { route: '/login' });

    fireEvent.click(screen.getByRole('button'));
    expect(await screen.findByText(/Der Server ist nicht erreichbar/)).toBeInTheDocument();
    expect(screen.queryByText('Failed to fetch')).toBeNull();
  });

  it('always offers the self-signup (UI-17, F-351)', () => {
    renderWithProviders(<LoginPage />, { route: '/login' });

    expect(screen.getByText(/Noch kein Konto\?/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Konto anlegen' })).toHaveAttribute(
      'href',
      '/onboarding',
    );
  });

  it('asks for a secure connection on an insecure origin', async () => {
    start.mockRejectedValue(new InsecureContextError());
    renderWithProviders(<LoginPage />, { route: '/login' });

    fireEvent.click(screen.getByRole('button'));
    expect(await screen.findByText(/erfordert eine sichere Verbindung/)).toBeInTheDocument();
  });

  it('starts the sign-in for the page it came from', async () => {
    start.mockResolvedValue('https://idp.example.com/auth');
    renderWithProviders(<LoginPage />, {
      route: { pathname: '/login', state: { from: '/onboarding' } },
    });

    fireEvent.click(screen.getByRole('button', { name: 'Anmelden' }));
    await vi.waitFor(() => expect(start).toHaveBeenCalledWith('/onboarding'));
  });

  it('offers to sign out another account after an accepted invitation (F-351)', async () => {
    const calls: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push(`${init?.method ?? 'GET'} ${String(input)}`);
        if (String(input).endsWith('/auth/config')) {
          return new Response(JSON.stringify(AUTH_CONFIG), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          });
        }
        return new Response(null, { status: 204 });
      }),
    );
    const assign = vi.fn();
    vi.stubGlobal('location', { ...window.location, assign });
    renderWithProviders(<LoginPage />, {
      route: { pathname: '/login', state: { invitationAccepted: 'new@example.invalid' } },
    });

    expect(screen.getByRole('status')).toHaveTextContent('Ihr Konto für new@example.invalid');
    expect(screen.getByText(/noch ein anderes Konto angemeldet/)).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: 'Anderes Konto abmelden' }));

    // The Reticora session ends, then the IdP session, which returns to the
    // sign-in page.
    await vi.waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
    expect(calls).toEqual(['POST /api/v1/auth/logout', 'GET /api/v1/auth/config']);
    const url = new URL(String(assign.mock.calls[0]?.[0]));
    expect(`${url.origin}${url.pathname}`).toBe(AUTH_CONFIG.end_session_endpoint);
    expect(url.searchParams.get('client_id')).toBe('reticora-app');
    expect(url.searchParams.get('post_logout_redirect_uri')).toBe('https://cmdb.example/login');
    expect(start).not.toHaveBeenCalled();
  });

  it('shows no sign-out hint for an ordinary sign-in', () => {
    renderWithProviders(<LoginPage />, { route: '/login' });

    expect(screen.queryByRole('button', { name: 'Anderes Konto abmelden' })).toBeNull();
  });
});
