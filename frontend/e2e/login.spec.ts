import { test, expect } from '@playwright/test';

const AUTH_CONFIG = {
  issuer: 'https://idp.example.com',
  client_id: 'reticora-test',
  redirect_uri: 'http://localhost:5173/auth/callback',
  scopes: ['openid', 'profile'],
  authorization_endpoint: 'https://idp.example.com/authorize',
  pkce_required: true,
};

test.describe('login flow', () => {
  test('redirects to the identity provider when signing in', async ({ page }) => {
    await page.route('**/api/v1/auth/config', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(AUTH_CONFIG) }),
    );

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

  test('shows an error state when auth configuration cannot be loaded', async ({ page }) => {
    await page.route('**/api/v1/auth/config', (route) =>
      route.fulfill({ status: 500, body: 'Internal Server Error' }),
    );

    await page.goto('/login');
    await page.getByRole('button', { name: /sign in|log in|anmelden/i }).click();

    await expect(page.getByText(/unable to load authentication configuration/i)).toBeVisible();
    await expect(page).toHaveURL(/\/login/);
  });
});
