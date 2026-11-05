import { expect, mockBackend, problem, signIn, test } from './support/backend';

// The OIDC login start (AUT-01): /api/v1/auth/config is answered by the
// strict mock and checked against api/openapi.yaml (WP-530). The sign-out
// ends the IdP session with an RP-initiated logout that returns to the
// sign-in page (F-351).

const AUTH_CONFIG = {
  issuer: 'https://idp.example.com',
  client_id: 'reticora-test',
  redirect_uri: 'http://localhost:5173/auth/callback',
  scopes: ['openid', 'profile'],
  authorization_endpoint: 'https://idp.example.com/authorize',
  token_endpoint: 'https://idp.example.com/token',
  pkce_required: true,
};

test.describe('login flow', () => {
  test('redirects to the identity provider when signing in', async ({ page }) => {
    await mockBackend(page, [{ path: '/auth/config', reply: () => ({ body: AUTH_CONFIG }) }]);

    const redirectRequest = page.waitForRequest((request) =>
      request.url().startsWith(AUTH_CONFIG.authorization_endpoint),
    );

    await page.goto('/login');
    await page.getByRole('button', { name: /sign in|log in|anmelden/i }).click();

    const url = new URL((await redirectRequest).url());
    expect(url.searchParams.get('response_type')).toBe('code');
    expect(url.searchParams.get('client_id')).toBe(AUTH_CONFIG.client_id);
    expect(url.searchParams.get('redirect_uri')).toBe(AUTH_CONFIG.redirect_uri);
    expect(url.searchParams.get('state')).toBeTruthy();
    expect(url.searchParams.get('code_challenge')).toBeTruthy();
  });

  test('signing out ends the IdP session and returns to the sign-in page (F-351)', async ({
    page,
  }) => {
    const endSession = 'https://idp.example.com/logout';
    await signIn(page);
    const calls = await mockBackend(page, [
      {
        path: '/auth/config',
        reply: () => ({ body: { ...AUTH_CONFIG, end_session_endpoint: endSession } }),
      },
      { method: 'POST', path: '/auth/logout', reply: () => ({ status: 204 }) },
    ]);
    // The IdP is not reachable from the test: its logout page is a stub.
    await page.route(`${endSession}?**`, (route) =>
      route.fulfill({ contentType: 'text/html', body: '<p>logged out</p>' }),
    );
    const logoutRequest = page.waitForRequest((request) => request.url().startsWith(endSession));

    await page.goto('/login');
    // Sign-out lives in the account menu of the header.
    await page.getByRole('button', { name: /kontomenü|account menu/i }).click();
    await page.getByRole('menuitem', { name: /^(abmelden|log out)$/i }).click();

    const url = new URL((await logoutRequest).url());
    expect(url.searchParams.get('client_id')).toBe(AUTH_CONFIG.client_id);
    expect(url.searchParams.get('post_logout_redirect_uri')).toBe(
      new URL('/login', AUTH_CONFIG.redirect_uri).toString(),
    );
    expect(url.searchParams.has('id_token_hint')).toBe(false);
    // The Reticora session ended before the browser left for the IdP.
    expect(
      calls.filter((c) => c.path.startsWith('/auth/')).map((c) => `${c.method} ${c.path}`),
    ).toEqual(['POST /auth/logout', 'GET /auth/config']);
  });

  test('shows an error state when auth configuration cannot be loaded', async ({ page }) => {
    await mockBackend(page, [
      {
        path: '/auth/config',
        reply: () => ({ status: 500, body: problem(500, 'internal error') }),
      },
    ]);

    await page.goto('/login');
    await page.getByRole('button', { name: /sign in|log in|anmelden/i }).click();

    await expect(page.getByText(/unable to load authentication configuration/i)).toBeVisible();
    await expect(page).toHaveURL(/\/login/);
  });
});
