import { expect, list, mockBackend, problem, test } from './support/backend';

// Epic A4 — OIDC login roundtrip E2E test.
//
// Drives the full browser-side authorization code flow against a mocked
// identity provider and backend: /login → authorization redirect (with PKCE)
// → /auth/callback → session stored → dashboard. This covers the regressions
// fixed in PR #18 (crypto.digest on insecure origins), PR #21 (localhost
// issuer redirect), PR #22 (failed to fetch) and PR #29 (blank dashboard).
// The backend is the strict mock of support/backend.ts, checked against
// api/openapi.yaml (WP-530).

const BASE = process.env.BASE_URL || 'http://localhost:4173';

const AUTH_CONFIG = {
  issuer: 'https://idp.example.com/realms/reticora',
  client_id: 'reticora-app',
  redirect_uri: `${BASE}/auth/callback`,
  scopes: ['openid', 'profile', 'email'],
  authorization_endpoint: 'https://idp.example.com/realms/reticora/protocol/openid-connect/auth',
  token_endpoint: 'https://idp.example.com/realms/reticora/protocol/openid-connect/token',
  pkce_required: true,
};

const SESSION = {
  token: 'header.payload.signature',
  expires_at: new Date(Date.now() + 3600_000).toISOString(),
  user: {
    sub: '00000000-0000-0000-0000-000000000001',
    email: 'admin@reticora.local',
    name: 'Admin',
    org_id: '00000000-0000-0000-0000-0000000000aa',
    permissions: [],
  },
};

/** DASHBOARD answers the reads of the dashboard after the login. */
const DASHBOARD = [
  // Totals of the CI widgets (limit=1, per status).
  { path: '/cis', reply: () => ({ body: list([]) }) },
  { path: '/collectors', reply: () => ({ body: list([]) }) },
  // Escalated open review items (REC-08, F-247): the total is the count.
  { path: '/review-items', reply: () => ({ body: list([]) }) },
  { path: '/slas/breaches', reply: () => ({ body: list([]) }) },
  {
    path: '/compliance/score',
    reply: () => ({
      body: {
        overall: { passed: 0, failed: 0, not_applicable: 0, score: 100 },
        by_ci_type: [],
        results: [],
      },
    }),
  },
];

test.describe('login roundtrip (mocked IdP)', () => {
  test('completes the full PKCE flow and lands on the dashboard', async ({ page }) => {
    // 3. The backend exchanges the code for a session token. The config
    // must be answered *before* navigation — the SPA may prefetch
    // /api/v1/auth/config on page load, and the first response is cached in
    // sessionStorage for the whole flow. The dashboard data loads (empty
    // lists must serialize as arrays — regression from PR #29 where null
    // broke the dashboard render).
    let callbackBody: { code?: string; state?: string; code_verifier?: string } = {};
    await mockBackend(page, [
      { path: '/auth/config', reply: () => ({ body: AUTH_CONFIG }) },
      {
        method: 'POST',
        path: '/auth/callback',
        reply: (request) => {
          callbackBody = request.postDataJSON() as typeof callbackBody;
          return { body: SESSION };
        },
      },
      ...DASHBOARD,
    ]);

    // 2. The mocked identity provider immediately redirects back with an
    //    authorization code, keeping the state parameter intact.
    await page.route('**/idp.example.com/**', async (route) => {
      const url = new URL(route.request().url());
      const state = url.searchParams.get('state') ?? '';
      const redirectUri = url.searchParams.get('redirect_uri') ?? AUTH_CONFIG.redirect_uri;
      // A script redirect instead of HTTP 302: WebKit cannot fulfill
      // intercepted requests with a redirect status in Playwright.
      const target = `${redirectUri}?code=mock-auth-code&state=${encodeURIComponent(state)}`;
      await route.fulfill({
        status: 200,
        contentType: 'text/html',
        body: `<!doctype html><script>location.replace(${JSON.stringify(target)})</script>`,
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
    // The dashboard renders with its data (every read is answered).
    await page.waitForLoadState('networkidle');
    await expect(page.locator('#main-content')).toBeVisible();

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
    // The app must refuse a foreign state before it calls the backend.
    let callbackCalled = false;
    await mockBackend(page, [
      { path: '/auth/config', reply: () => ({ body: AUTH_CONFIG }) },
      {
        method: 'POST',
        path: '/auth/callback',
        reply: () => {
          callbackCalled = true;
          return { status: 400, body: problem(400, 'bad state') };
        },
      },
    ]);

    // Seed a transaction by starting the flow. The mocked IdP answers with
    // an empty page instead of redirecting back, and the test waits until
    // that navigation has committed: an aborted IdP request would end in an
    // error page navigation that races with (and interrupts) page.goto below.
    await page.route('**/idp.example.com/**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'text/html',
        body: '<!doctype html><title>IdP</title>',
      }),
    );
    await page.goto('/login');
    await page.getByRole('button', { name: /sign in|log in|anmelden/i }).click();
    await page.waitForURL(/idp\.example\.com/);

    // Now hit the callback with a foreign state.
    // The IdP mock is no longer needed (defensive hygiene).
    await page.unroute('**/idp.example.com/**');
    await page.goto('/auth/callback?code=attacker-code&state=wrong-state');
    await expect(page.getByText(/invalid authentication state/i)).toBeVisible();
    expect(callbackCalled).toBe(false);
  });
});
