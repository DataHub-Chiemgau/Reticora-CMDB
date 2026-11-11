/**
 * F-197 (WP-570, AUT-01, AUT-09): a refused login shows a localized text for
 * the problem type of POST /auth/callback, not the server's technical detail
 * ("identity: …").
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import { renderWithProviders } from '../../test/utils';
import { CallbackPage } from './CallbackPage';

const TRANSACTION_KEY = 'reticora-auth-transaction';

function problem(status: number, type: string, detail: string) {
  return new Response(JSON.stringify({ type, title: 'Forbidden', status, detail, trace_id: 't' }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

/** callback renders the callback page whose POST /auth/callback answers response. */
function callback(response: Response) {
  const fetchMock = vi.fn(async () => response);
  vi.stubGlobal('fetch', fetchMock);
  window.sessionStorage.setItem(
    TRANSACTION_KEY,
    JSON.stringify({ state: 'state-1', codeVerifier: 'verifier', returnTo: '/dashboard' }),
  );
  renderWithProviders(
    <Routes>
      <Route path="/auth/callback" element={<CallbackPage />} />
      <Route path="/dashboard" element={<p>dashboard</p>} />
    </Routes>,
    { route: '/auth/callback?code=code-1&state=state-1' },
  );
  return fetchMock;
}

afterEach(() => {
  window.sessionStorage.clear();
  vi.unstubAllGlobals();
});

describe('CallbackPage refusals', () => {
  it('shows the German text of a first login without invitation (403), not the detail', async () => {
    const fetchMock = callback(
      problem(
        403,
        'https://reticora.io/problems/login-not-admitted',
        'identity: first login requires an invitation for this organization and e-mail',
      ),
    );

    expect(
      await screen.findByText(
        'Für die erste Anmeldung ist eine Einladung Ihrer Organisation erforderlich. Bitten Sie die Administration Ihrer Organisation um eine Einladung.',
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/identity:/)).toBeNull();
    expect(screen.queryByText(/first login requires/)).toBeNull();
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/callback', expect.anything());
    expect(window.sessionStorage.getItem(TRANSACTION_KEY)).toBeNull();
    expect(screen.queryByText('dashboard')).toBeNull();
  });

  it.each([
    [
      403,
      'https://reticora.io/problems/login-user-inactive',
      'identity: user is deactivated',
      'Ihr Konto ist deaktiviert. Wenden Sie sich an die Administration Ihrer Organisation.',
    ],
    [
      401,
      'https://reticora.io/problems/login-no-organization',
      'identity: no organization attribute in ID token',
      'Ihr Konto ist keiner Organisation zugeordnet. Wenden Sie sich an die Administration Ihrer Organisation.',
    ],
    [
      // The attribute names no organization of the instance (F-395).
      401,
      'https://reticora.io/problems/login-no-organization',
      'identity: the organization attribute names no organization',
      'Ihr Konto ist keiner Organisation zugeordnet. Wenden Sie sich an die Administration Ihrer Organisation.',
    ],
    [
      403,
      'https://reticora.io/problems/login-no-role',
      'identity: no role is assigned to the user in this organization; ask an administrator for access',
      'Ihrem Konto ist in dieser Organisation keine Rolle zugewiesen. Bitten Sie die Administration Ihrer Organisation um Zugriff.',
    ],
    [
      403,
      'https://reticora.io/problems/entitlement-limit',
      'max_users reached',
      'Die Organisation hat ihre Höchstzahl an Benutzern erreicht. Wenden Sie sich an die Administration Ihrer Organisation.',
    ],
  ])('maps %i %s to its German text', async (status, type, detail, text) => {
    callback(problem(status, type, detail));

    expect(await screen.findByText(text)).toBeInTheDocument();
    expect(screen.queryByText(detail)).toBeNull();
    expect(screen.queryByText(/identity:/)).toBeNull();
  });

  it('shows the general German error for any other answer of the server', async () => {
    callback(problem(502, 'https://reticora.io/problems/502', 'the login could not be completed'));

    expect(
      await screen.findByText('Die Anmeldung konnte nicht abgeschlossen werden.'),
    ).toBeInTheDocument();
    expect(screen.queryByText('the login could not be completed')).toBeNull();
  });
});

describe('CallbackPage operator login (WP-887)', () => {
  function operatorCallback(response: Response) {
    const fetchMock = vi.fn(async () => response);
    vi.stubGlobal('fetch', fetchMock);
    window.sessionStorage.setItem(
      TRANSACTION_KEY,
      JSON.stringify({
        state: 'state-1',
        codeVerifier: 'verifier',
        returnTo: '/operator',
        kind: 'operator',
      }),
    );
    renderWithProviders(
      <Routes>
        <Route path="/auth/callback" element={<CallbackPage />} />
        <Route path="/operator" element={<p>operator area</p>} />
        <Route path="/dashboard" element={<p>dashboard</p>} />
      </Routes>,
      { route: '/auth/callback?code=code-1&state=state-1' },
    );
    return fetchMock;
  }

  it('calls the operator callback and opens the operator area', async () => {
    const fetchMock = operatorCallback(
      new Response(
        JSON.stringify({
          token: 'op',
          expires_at: '2099-01-01T00:00:00Z',
          operator: { id: 'op-1', kind: 'oidc' },
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    );
    expect(await screen.findByText('operator area')).toBeInTheDocument();
    expect((fetchMock.mock.calls[0] as unknown[] | undefined)?.[0]).toBe(
      '/api/v1/admin/auth/callback',
    );
    expect(window.sessionStorage.getItem('reticora-auth-session')).toBeNull();
  });

  it('shows a refusal without its cause on 403', async () => {
    operatorCallback(
      problem(403, 'about:blank', 'operator: login is not an operator with MFA: no second factor'),
    );
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Die Operator-Anmeldung wurde abgelehnt.',
    );
    expect(screen.queryByText(/second factor|MFA/)).toBeNull();
  });

  it('names a missing operator login setup on 503', async () => {
    operatorCallback(problem(503, 'about:blank', 'operator login is not configured'));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Die Operator-Anmeldung ist auf diesem Server nicht eingerichtet.',
    );
  });
});
