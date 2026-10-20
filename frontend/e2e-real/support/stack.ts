import { createHash, randomBytes } from 'node:crypto';
import { expect, type APIRequestContext, type Page } from '@playwright/test';
import { keycloakOtp } from '../../e2e/support/totp';

// Shared helpers of the real-stack specs (Phase-1 acceptance audit). They
// drive the same paths a user takes: the SPA login through Keycloak (PKCE),
// self-signup with the verification e-mail from the mail catcher, and the API
// through the frontend origin with the session token of the signed-in
// browser.

export const BASE = process.env.BASE_URL || 'http://localhost:5173';
export const SERVER = process.env.E2E_SERVER_URL || 'http://localhost:8080';
export const MAILPIT = process.env.E2E_MAILPIT_URL || 'http://localhost:8025';
export const KEYCLOAK = process.env.E2E_KEYCLOAK_URL || 'http://localhost:8180';
export const KEYCLOAK_ADMIN = process.env.E2E_KEYCLOAK_ADMIN || 'admin';
export const KEYCLOAK_ADMIN_PASSWORD = process.env.E2E_KEYCLOAK_ADMIN_PASSWORD || 'admin';
export const COLLECTOR_BIN = process.env.E2E_COLLECTOR_BIN || '';

export interface Account {
  email: string;
  password: string;
  /** Organization name chosen at signup. */
  org: string;
}

/** stamp returns a value that keeps names of one run apart from other runs. */
export function stamp(): string {
  return `${Date.now()}${Math.floor(Math.random() * 1000)}`;
}

/** sessionToken reads the bearer token of the signed-in browser session. */
export async function sessionToken(page: Page): Promise<string> {
  const raw = await page.evaluate(() => window.sessionStorage.getItem('reticora-auth-session'));
  const token = raw ? (JSON.parse(raw) as { token?: string }).token : undefined;
  expect(token, 'session token after login').toBeTruthy();
  return token!;
}

export interface ApiResult<T = unknown> {
  status: number;
  body: T;
}

/**
 * api calls the REST API through the frontend origin, like the SPA does. It
 * never asserts the status: negative tests check it themselves.
 */
export async function api<T = unknown>(
  request: APIRequestContext,
  token: string,
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
  headers: Record<string, string> = {},
): Promise<ApiResult<T>> {
  const res = await request.fetch(`${BASE}/api/v1${path}`, {
    method,
    headers: { Authorization: `Bearer ${token}`, ...headers },
    data,
    failOnStatusCode: false,
  });
  const text = await res.text();
  let body: unknown = text;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    // keep the raw text (exports, plain errors)
  }
  return { status: res.status(), body: body as T };
}

/**
 * keycloakLogin fills the Keycloak form, completes the second factor of an
 * account holding mfa_required (OTP setup at the first login, the code at
 * later ones; e2e/support/totp.ts, WP-357) and a required profile step should
 * the realm ask for one; it fails with the page Keycloak stopped at.
 */
export async function keycloakLogin(
  page: Page,
  account: Pick<Account, 'email' | 'password'>,
  landing: RegExp,
) {
  await page.waitForURL(/\/protocol\/openid-connect\/auth/);
  await page.locator('#username').fill(account.email);
  await page.locator('#password').fill(account.password);
  await page.locator('#kc-login').click();
  await keycloakOtp(page, account.email, landing);
  const lastName = page.locator('#lastName');
  await Promise.race([
    page.waitForURL(landing, { timeout: 60_000 }),
    lastName.waitFor({ state: 'visible', timeout: 60_000 }),
  ]).catch(() => undefined);
  if (await lastName.isVisible().catch(() => false)) {
    if (
      !(await page
        .locator('#firstName')
        .inputValue()
        .catch(() => ''))
    ) {
      await page.locator('#firstName').fill('E2E');
    }
    await lastName.fill('Audit');
    await page.locator('input[type="submit"], button[type="submit"]').first().click();
  }
  if (!landing.test(page.url())) {
    await page.waitForURL(landing, { timeout: 30_000 }).catch(async () => {
      const text = (
        await page
          .locator('body')
          .innerText()
          .catch(() => '')
      ).slice(0, 500);
      throw new Error(`login stopped at ${page.url()}: ${text}`);
    });
  }
}

/** uiLogin signs in through the SPA login page and returns the session token. */
export async function uiLogin(
  page: Page,
  account: Pick<Account, 'email' | 'password'>,
  landing: RegExp = /\/(dashboard|onboarding)/,
): Promise<string> {
  await page.goto('/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('button', { name: /anmelden|sign in/i }).click();
  await keycloakLogin(page, account, landing);
  return sessionToken(page);
}

/** verificationLink waits for the signup e-mail to `email` and returns its link. */
export async function verificationLink(request: APIRequestContext, email: string): Promise<string> {
  let link = '';
  await expect
    .poll(
      async () => {
        const search = await request.get(`${MAILPIT}/api/v1/search`, {
          params: { query: `to:"${email}"` },
        });
        if (!search.ok()) return '';
        const found = (await search.json()) as { messages?: { ID: string }[] };
        const id = found.messages?.[0]?.ID;
        if (!id) return '';
        const message = (await (await request.get(`${MAILPIT}/api/v1/message/${id}`)).json()) as {
          Text?: string;
          HTML?: string;
        };
        const match = `${message.Text ?? ''} ${message.HTML ?? ''}`.match(
          /https?:\/\/[^\s"'<>]+\/onboarding\?token=[A-Za-z0-9_\-.%]+/,
        );
        link = match ? match[0].replace(/&amp;/g, '&') : '';
        return link;
      },
      { message: `verification e-mail to ${email}`, timeout: 60_000, intervals: [1_000, 2_000] },
    )
    .not.toBe('');
  return link;
}

/**
 * signupTenant creates a NEW organization with its first administrator through
 * the self-signup UI (form, verification e-mail, password) and returns the
 * account. The caller signs in with uiLogin afterwards.
 */
export async function signupTenant(
  page: Page,
  request: APIRequestContext,
  label: string,
): Promise<Account> {
  const s = stamp();
  const account: Account = {
    email: `${label}-${s}@audit-e2e.de`,
    password: `Audit-${s}!aA`,
    org: `Audit ${label} ${s}`,
  };
  await page.goto('/onboarding');
  await page.getByLabel('Organisation').fill(account.org);
  await page.getByLabel('E-Mail-Adresse').fill(account.email);
  await page.getByLabel('Ihr Name').fill(`Audit ${label}`);
  await page.getByRole('button', { name: 'Konto anlegen' }).click();
  await expect(
    page.getByText(`Bestätigungslink an ${account.email}`, { exact: false }),
  ).toBeVisible();
  await page.goto(await verificationLink(request, account.email));
  await page.getByLabel(/Passwort festlegen/).fill(account.password);
  await page.getByRole('button', { name: 'Konto bestätigen' }).click();
  await expect(page.getByText(/ist angelegt/)).toBeVisible();
  return account;
}

/** keycloakAdminToken returns an access token of the Keycloak master admin. */
export async function keycloakAdminToken(request: APIRequestContext): Promise<string> {
  const res = await request.post(`${KEYCLOAK}/realms/master/protocol/openid-connect/token`, {
    form: {
      grant_type: 'password',
      client_id: 'admin-cli',
      username: KEYCLOAK_ADMIN,
      password: KEYCLOAK_ADMIN_PASSWORD,
    },
  });
  expect(res.ok(), `Keycloak admin token: ${res.status()}`).toBe(true);
  return ((await res.json()) as { access_token: string }).access_token;
}

/**
 * OPERATOR_ACR is the acr value the operator login requests (acr_values):
 * level of authentication 2 of the realm flow browser-mfa, reached only with
 * an OTP. The server issues an operator session only for an ID token with
 * this acr (RETICORA_OPERATOR_MFA_ACR) and the group operators (SEC-07,
 * WP-361, deploy/keycloak/README.md).
 */
export const OPERATOR_ACR = '2';

export interface OperatorAuthorization {
  /** Authorization URL of the operator login (PKCE, acr_values=2). */
  url: string;
  state: string;
  codeVerifier: string;
  /** The redirect URI the server exchanges the code with. */
  redirectUri: string;
}

/**
 * operatorAuthorization builds the authorization URL of the operator login
 * from the server's OIDC configuration (GET /api/v1/auth/config): the
 * reticora-app client, the configured redirect URI, PKCE (S256) and
 * acr_values=2, which makes Keycloak ask for the OTP.
 */
export async function operatorAuthorization(
  request: APIRequestContext,
): Promise<OperatorAuthorization> {
  const res = await request.get(`${BASE}/api/v1/auth/config`);
  expect(res.ok(), `GET /api/v1/auth/config: ${res.status()}`).toBe(true);
  const config = (await res.json()) as {
    client_id: string;
    redirect_uri?: string;
    scopes: string[];
    authorization_endpoint: string;
  };
  expect(config.redirect_uri, 'configured OIDC redirect URI').toBeTruthy();
  const codeVerifier = randomBytes(48).toString('base64url');
  const state = randomBytes(16).toString('hex');
  const url = new URL(config.authorization_endpoint);
  url.search = new URLSearchParams({
    client_id: config.client_id,
    response_type: 'code',
    redirect_uri: config.redirect_uri!,
    scope: config.scopes.join(' '),
    state,
    code_challenge: createHash('sha256').update(codeVerifier).digest('base64url'),
    code_challenge_method: 'S256',
    acr_values: OPERATOR_ACR,
  }).toString();
  return { url: url.toString(), state, codeVerifier, redirectUri: config.redirect_uri! };
}

export interface OperatorSession {
  token: string;
  expires_at: string;
  operator: { id: string; name?: string; email?: string; kind: string };
}

/**
 * operatorLogin signs in through Keycloak with the operator authorization
 * URL (password and OTP, keycloakLogin), catches the redirect with the code
 * before the SPA could redeem it as a tenant login, and posts the code to
 * POST /api/v1/admin/auth/callback. It returns the server's answer without
 * asserting the status.
 */
export async function operatorLogin(
  page: Page,
  request: APIRequestContext,
  account: Pick<Account, 'email' | 'password'>,
): Promise<ApiResult<OperatorSession>> {
  const auth = await operatorAuthorization(request);
  const redirect = new URL(auth.redirectUri);
  let code = '';
  let state = '';
  await page.route(
    (url) => url.origin === redirect.origin && url.pathname === redirect.pathname,
    async (route) => {
      const url = new URL(route.request().url());
      code = url.searchParams.get('code') ?? '';
      state = url.searchParams.get('state') ?? '';
      await route.fulfill({ status: 200, contentType: 'text/html', body: '<p>operator login</p>' });
    },
  );
  await page.goto(auth.url);
  const landing = new RegExp(`^${auth.redirectUri.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\?`);
  await keycloakLogin(page, account, landing);
  expect(code, 'authorization code at the redirect URI').toBeTruthy();
  expect(state, 'state of the operator authorization').toBe(auth.state);
  const res = await request.post(`${BASE}/api/v1/admin/auth/callback`, {
    data: { code, state, code_verifier: auth.codeVerifier },
    failOnStatusCode: false,
  });
  const text = await res.text();
  let body: unknown = text;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    // keep the raw text
  }
  return { status: res.status(), body: body as OperatorSession };
}
