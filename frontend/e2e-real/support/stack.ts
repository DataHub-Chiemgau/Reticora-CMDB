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
