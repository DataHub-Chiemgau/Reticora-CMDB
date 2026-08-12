import { test, expect } from '@playwright/test';

// Epic A4 — OIDC login roundtrip E2E test.
//
// Drives the full browser-side authorization code flow against a mocked
// identity provider and backend: /login → authorization redirect (with PKCE)
// → /auth/callback → session stored → dashboard. This covers the regressions
// fixed in PR #18 (crypto.digest on insecure origins), PR #21 (localhost
// issuer redirect), PR #22 (failed to fetch) and PR #29 (blank dashboard).

const BASE = process.env.BASE_URL || 'http://localhost:4173';

const AUTH_CONFIG = {
  issuer: 'https://idp.example.com/realms/reticora',
  client_id: 'reticora-app',
  redirect_uri: `${BASE}/auth/callback`,
  scopes: ['openid', 'profile', 'email'],
  authorization_endpoint: 'https://idp.example.com/realms/reticora/protocol/openid-connect/auth',
  pkce_required: true,
};

const SESSION = {
  token: 'header.payload.signature',
  expires_at: new Date(Date.now() + 3600_000).toISOString(),
  user: {
    id: '00000000-0000-0000-0000-000000000001',
    email: 'admin@reticora.local',
    display_name: 'Admin',
    roles: ['org_admin'],
  },
};

test.describe('login roundtrip (mocked IdP)', () => {
  test('completes the full PKCE flow and lands on the dashboard', async ({ page }) => {
    // The dashboard data loads (empty lists must serialize as arrays —
    // regression from PR #29 where null broke the dashboard render).
    // Registered first: Playwright matches routes in reverse registration
    // order, so the specific auth routes below take precedence.
    await page.route('**/api/v1/**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: '{"items":[],"total":0}',
      }),
    );

    // The config route must be registered *before* navigation — the SPA may
    // prefetch /api/v1/auth/config on page load, and an unmocked first
    // response is cached in sessionStorage for the whole flow.
    await page.route('**/api/v1/auth/config', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(AUTH_CONFIG),
      }),
    );

    // 2. The mocked identity provider immediately redirects back with an
    //    authorization code, keeping the state parameter intact.
    await page.route('**/idp.example.com/**', async (route) => {
      const url = new URL(route.request().url());
      const state = url.searchParams.get('state') ?? '';
      const redirectUri = url.searchParams.get('redirect_uri') ?? AUTH_CONFIG.redirect_uri;
      await route.fulfill({
        status: 302,
        headers: { Location: `${redirectUri}?code=mock-auth-code&state=${state}` },
      });
    });

    // 3. The backend exchanges the code for a session token.
    let callbackBody: { code?: string; state?: string; code_verifier?: string } = {};
    await page.route('**/api/v1/auth/callback', async (route) => {
      callbackBody = route.request().postDataJSON() as typeof callbackBody;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(SESSION),
      });
    });

    // Start from a clean slate so no cached auth config or transaction leaks
    // in from a previous test in the same context.
    await page.goto('/login');
    await page.evaluate(() => window.sessionStorage.clear());
    await page.goto('/login');
    await page.getByRole('button', { name: /sign in|log in|anmelden/i }).click();

    // After the mocked IdP redirect and the callback exchange the app must
    // navigate to the dashboard.
    await expect(page).toHaveURL(/\/dashboard/, { timeout: 15_000 });

    // The callback must have carried the PKCE verifier and the state that the
    // login page generated.
    expect(callbackBody.code).toBe('mock-auth-code');
    expect(callbackBody.state).toBeTruthy();
    expect(callbackBody.code_verifier).toBeTruthy();

    // The session is persisted (sessionStorage, key from authStore) so a
    // reload keeps the user signed in.
    const storedToken = await page.evaluate(() =>
      window.sessionStorage.getItem('reticora-auth-session'),
    );
    expect(storedToken).toBeTruthy();
    expect(JSON.parse(storedToken ?? '{}')).toMatchObject({ token: SESSION.token });
  });

  test('rejects a callback with a mismatched state parameter', async ({ page }) => {
    await page.route('**/api/v1/auth/config', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(AUTH_CONFIG),
      }),
    );

    // Seed a transaction by starting the flow, but block the redirect. The
    // aborted navigation attempt throws in page.goto below unless we catch it.
    await page.route('**/idp.example.com/**', (route) => route.abort());
    await page.goto('/login');
    await page.getByRole('button', { name: /sign in|log in|anmelden/i }).click();

    // Now hit the callback with a foreign state — the app must refuse it
    // before calling the backend.
    let callbackCalled = false;
    await page.route('**/api/v1/auth/callback', (route) => {
      callbackCalled = true;
      return route.fulfill({ status: 400, body: '{"detail":"bad state"}' });
    });

    // Remove the IdP abort so the SPA navigation to /auth/callback is not
    // blocked (the callback is a same-origin route, but the aborted IdP route
    // pattern also matches nothing here — unroute is just defensive hygiene).
    await page.unroute('**/idp.example.com/**');
    await page.goto('/auth/callback?code=attacker-code&state=wrong-state');
    await expect(page.getByText(/invalid authentication state/i)).toBeVisible();
    expect(callbackCalled).toBe(false);
  });
});
