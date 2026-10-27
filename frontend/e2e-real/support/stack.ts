import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, type APIRequestContext, type Browser, type Page } from '@playwright/test';
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
/** A build of collector/cmd/fakesnmp; without it startFakeSnmp builds one. */
export const FAKESNMP_BIN = process.env.E2E_FAKESNMP_BIN || '';
/** A build of backend/cmd/seed; without it seedDemo builds one. */
export const SEED_BIN = process.env.E2E_SEED_BIN || '';
/** PostgreSQL of the stack with the migration login (README.md); seedDemo seeds it. */
export const DATABASE_URL =
  process.env.E2E_DATABASE_URL || 'postgres://reticora:reticora@localhost:55432/e2e';

/** The repository root (this file is frontend/e2e-real/support/stack.ts). */
const REPO = fileURLToPath(new URL('../../../', import.meta.url));

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
 * expectModuleServed asserts that the stack serves the late module `key` (WP-541): GET
 * /modules reports it enabled. Late modules are off in the G1 default (E-03, WP-214) and their
 * routes answer 404; the specs of such a module run only in the module run of install-smoke.yml
 * against a server started with RETICORA_MODULES naming it (README.md). A spec never skips
 * itself: against a G1 default stack it fails here with the reason.
 */
export async function expectModuleServed(request: APIRequestContext, token: string, key: string) {
  const res = await api<{ data?: { key: string; enabled: boolean }[] }>(
    request,
    token,
    'GET',
    '/modules',
  );
  expect(res.status, `GET /modules: ${JSON.stringify(res.body)}`).toBe(200);
  expect(
    res.body?.data?.find((m) => m.key === key)?.enabled,
    `module ${key} served (start the server with RETICORA_MODULES=${key}, e2e-real/README.md)`,
  ).toBe(true);
}

/** A user of a tenant holding exactly one role assignment in a client or site scope. */
export interface ScopedUser {
  email: string;
  password: string;
  userId: string;
  /** The assignment made with POST /role-assignments. */
  assignmentId: string;
}

/**
 * scopedUser creates a Keycloak user of the administrator's organization holding exactly
 * one standard role (`role`, e.g. client_technician or viewer) in the scope of one client
 * or site (WP-535). An invitation carries a standard role, so the user is invited as
 * viewer of the client `inviteClientId` and accepts it through the API (the server
 * creates the Keycloak user); the role is then assigned with POST /api/v1/role-assignments
 * {user_id, role_id, scope_type, scope_id} and the viewer assignment of the invitation is
 * deleted. GET /role-assignments afterwards lists exactly the new assignment. Every call
 * goes through the API with the administrator's token; a refused call fails the setup.
 */
export async function scopedUser(
  request: APIRequestContext,
  adminToken: string,
  opts: {
    label: string;
    role: string;
    scope_type: 'client' | 'site';
    scope_id: string;
    inviteClientId: string;
  },
): Promise<ScopedUser> {
  const call = async <T>(
    method: 'GET' | 'POST' | 'DELETE',
    path: string,
    data?: unknown,
  ): Promise<T> => {
    const res = await api<T>(request, adminToken, method, path, data);
    expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(
      200,
    );
    expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
    return res.body;
  };
  type Assignment = { id: string; role_id?: string; scope_type: string; scope_id?: string };
  const roles = await call<{ data: { id: string; name: string }[] | null }>(
    'GET',
    '/roles?limit=100',
  );
  const roleId = (name: string) => {
    const id = (roles.data ?? []).find((r) => r.name === name)?.id ?? '';
    expect(id, `system role ${name} (RBA-02)`).not.toBe('');
    return id;
  };
  const viewer = roleId('viewer');
  const target = roleId(opts.role);
  // The assignment of the invitation must differ from the target one, or deleting it
  // would delete the target assignment (the server returns an existing assignment).
  expect(
    target === viewer && opts.scope_type === 'client' && opts.scope_id === opts.inviteClientId,
    'target assignment differs from the viewer assignment of the invitation',
  ).toBe(false);

  const s = stamp();
  const email = `${opts.label}-${s}@audit-e2e.de`;
  const password = `Audit-${opts.label}-${s}!aA`;
  const invitation = await call<{ token?: string }>('POST', '/invitations', {
    email,
    role_id: viewer,
    scope_type: 'client',
    scope_id: opts.inviteClientId,
  });
  expect(invitation.token, 'the token is returned once on issue').toBeTruthy();
  const accept = await request.post(`${BASE}/api/v1/invitations/accept`, {
    data: { token: invitation.token, password, display_name: `Audit ${opts.label}` },
    failOnStatusCode: false,
  });
  expect(accept.status(), `accept the invitation: ${await accept.text()}`).toBe(201);
  const userId = ((await accept.json()) as { user_id?: string }).user_id ?? '';
  expect(userId, 'the accepted user').not.toBe('');

  const assigned = await call<Assignment>('POST', '/role-assignments', {
    user_id: userId,
    role_id: target,
    scope_type: opts.scope_type,
    scope_id: opts.scope_id,
  });
  const listed = async () =>
    (await call<{ data: Assignment[] | null }>('GET', `/role-assignments?user_id=${userId}`))
      .data ?? [];
  const invited = (await listed()).filter(
    (a) => a.role_id === viewer && a.scope_type === 'client' && a.scope_id === opts.inviteClientId,
  );
  expect(invited, 'the viewer assignment of the invitation').toHaveLength(1);
  await call('DELETE', `/role-assignments/${invited[0]!.id}`);
  const left = (await listed()).map((a) => ({
    id: a.id,
    role_id: a.role_id,
    scope_type: a.scope_type,
    scope_id: a.scope_id,
  }));
  expect(left, `the only assignment of ${email}`).toEqual([
    { id: assigned.id, role_id: target, scope_type: opts.scope_type, scope_id: opts.scope_id },
  ]);
  return { email, password, userId, assignmentId: assigned.id };
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

/** seedId is the deterministic id of a seeded row (backend/cmd/seed ID, md5(key)::uuid). */
export function seedId(...parts: string[]): string {
  const h = createHash('md5').update(parts.join('/')).digest('hex');
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20, 32)}`;
}

/** An organization of the SIM-01 seed as the seed prints it. */
export interface SeededOrg {
  slug: string;
  id: string;
  admin: string;
}

let seedBin = SEED_BIN;

/** seedBinary builds backend/cmd/seed once (Go toolchain on PATH). */
function seedBinary(): string {
  if (seedBin) return seedBin;
  const bin = join(mkdtempSync(join(tmpdir(), 'reticora-seed-')), 'seed');
  const built = spawnSync('go', ['build', '-o', bin, './cmd/seed'], {
    cwd: join(REPO, 'backend'),
    env: { ...process.env, GOWORK: 'off', CGO_ENABLED: '0' },
    encoding: 'utf8',
    timeout: 8 * 60_000,
  });
  expect(
    built.status,
    `go build ./cmd/seed: ${built.error ?? ''} ${built.stderr} ${built.stdout}`,
  ).toBe(0);
  seedBin = bin;
  return bin;
}

/**
 * seedDemo runs the SIM-01 seed (backend/cmd/seed, WP-203) against the stack database
 * (DATABASE_URL): the organizations demo (plan pro, admin@demo.local) and acme
 * (admin@acme.local) with 40 CIs each, located and in the search index (WP-433). The seed
 * is deterministic and inserts only what is missing, so a second run changes nothing. It
 * returns the organizations the seed prints.
 */
export function seedDemo(): SeededOrg[] {
  const run = spawnSync(seedBinary(), ['-database-url', DATABASE_URL], {
    encoding: 'utf8',
    timeout: 5 * 60_000,
  });
  expect(run.status, `seed: ${run.error ?? ''} ${run.stderr}`).toBe(0);
  return run.stdout
    .split('\n')
    .filter((line) => line.trim() !== '')
    .map((line) => {
      const [slug = '', id = '', admin = ''] = line.split('\t');
      return { slug, id, admin };
    });
}

/** A user of the development realm deploy/keycloak/realm-reticora-dev.json. */
interface RealmUser {
  username: string;
  email: string;
  enabled: boolean;
  emailVerified?: boolean;
  firstName?: string;
  lastName?: string;
  attributes?: Record<string, string[]>;
  groups?: string[];
  realmRoles?: string[];
  credentials?: { type: string; value: string; temporary?: boolean }[];
}

/**
 * devRealmAccount returns the account of the user `email` of the development realm
 * deploy/keycloak/realm-reticora-dev.json and makes sure the realm reticora of the stack
 * holds it. The development stack imports that realm with its users. Keycloak imports a
 * realm only when it does not exist yet, and an installed stack imports the production
 * realm, which has no users (deploy/keycloak/README.md). Only when the realm exists without
 * the user (import of the development realm skipped), the user is created through the
 * Keycloak admin API with the attributes, groups, realm roles and password of the JSON; the
 * creation is logged and reported as `created`.
 */
export async function devRealmAccount(
  request: APIRequestContext,
  email: string,
): Promise<{ account: Pick<Account, 'email' | 'password'>; created: boolean }> {
  const realm = JSON.parse(
    readFileSync(join(REPO, 'deploy/keycloak/realm-reticora-dev.json'), 'utf8'),
  ) as { users?: RealmUser[] };
  const user = realm.users?.find((u) => u.username === email);
  const password = user?.credentials?.find((c) => c.type === 'password')?.value ?? '';
  expect(user, `${email} in realm-reticora-dev.json`).toBeTruthy();
  expect(password, `password of ${email} in realm-reticora-dev.json`).not.toBe('');
  const account = { email, password };

  const admin = `${KEYCLOAK}/admin/realms/reticora`;
  const headers = { Authorization: `Bearer ${await keycloakAdminToken(request)}` };
  const userId = async (): Promise<string> => {
    const res = await request.get(`${admin}/users`, {
      headers,
      params: { username: email, exact: 'true' },
    });
    expect(res.ok(), `GET users?username=${email}: ${res.status()}`).toBe(true);
    return ((await res.json()) as { id: string }[])[0]?.id ?? '';
  };
  if (await userId()) return { account, created: false };

  console.log(
    `realm reticora exists without ${email} (the development realm was not imported): ` +
      'creating the user from realm-reticora-dev.json through the Keycloak admin API',
  );
  const u = user!;
  const created = await request.post(`${admin}/users`, {
    headers,
    data: {
      username: u.username,
      email: u.email,
      enabled: u.enabled,
      emailVerified: u.emailVerified ?? false,
      firstName: u.firstName,
      lastName: u.lastName,
      attributes: u.attributes ?? {},
    },
  });
  expect(created.status(), `POST users ${email}: ${await created.text()}`).toBe(201);
  const id = await userId();
  expect(id, `${email} after its creation`).not.toBe('');
  const reset = await request.put(`${admin}/users/${id}/reset-password`, {
    headers,
    data: { type: 'password', value: password, temporary: false },
  });
  expect(reset.status(), `password of ${email}: ${await reset.text()}`).toBe(204);
  if (u.groups?.length) {
    const res = await request.get(`${admin}/groups`, { headers });
    expect(res.ok(), `GET groups: ${res.status()}`).toBe(true);
    const groups = (await res.json()) as { id: string; path: string }[];
    for (const path of u.groups) {
      const group = groups.find((g) => g.path === path);
      expect(group, `group ${path} in the realm`).toBeTruthy();
      const joined = await request.put(`${admin}/users/${id}/groups/${group!.id}`, { headers });
      expect(joined.status(), `${email} joins ${path}: ${await joined.text()}`).toBe(204);
    }
  }
  if (u.realmRoles?.length) {
    const roles: unknown[] = [];
    for (const name of u.realmRoles) {
      const res = await request.get(`${admin}/roles/${encodeURIComponent(name)}`, { headers });
      expect(res.ok(), `GET roles/${name}: ${res.status()}`).toBe(true);
      roles.push(await res.json());
    }
    const mapped = await request.post(`${admin}/users/${id}/role-mappings/realm`, {
      headers,
      data: roles,
    });
    expect(mapped.status(), `realm roles of ${email}: ${await mapped.text()}`).toBe(204);
  }
  console.log(
    `created ${email}: organization_id ${JSON.stringify(u.attributes?.organization_id ?? [])}, ` +
      `realm roles ${JSON.stringify(u.realmRoles ?? [])}, groups ${JSON.stringify(u.groups ?? [])}`,
  );
  return { account, created: true };
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
 * URL (password and OTP, e2e/support/totp.ts) and posts the code to
 * POST /api/v1/admin/auth/callback. The redirect to the redirect URI is the
 * SPA callback, which would redeem the code as a tenant login. It is taken
 * from Keycloak's answer instead: every form post to Keycloak goes through
 * route.fetch without following redirects, and the answer that redirects to
 * the redirect URI is replaced by a placeholder page. A redirect target is
 * not intercepted reliably (Chromium does not hand Playwright the redirect
 * from the Keycloak host to the loopback SPA of an installed stack). It
 * returns the server's answer without asserting the status.
 */
export async function operatorLogin(
  page: Page,
  request: APIRequestContext,
  account: Pick<Account, 'email' | 'password'>,
): Promise<ApiResult<OperatorSession>> {
  const auth = await operatorAuthorization(request);
  const keycloak = new URL(auth.url).origin;
  let location = '';
  await page.route(
    (url) => url.origin === keycloak && url.pathname.includes('/login-actions/'),
    async (route) => {
      if (route.request().method() !== 'POST') {
        await route.continue();
        return;
      }
      const response = await route.fetch({ maxRedirects: 0 });
      const target = response.headers()['location'] ?? '';
      if (
        response.status() >= 300 &&
        response.status() < 400 &&
        target.startsWith(auth.redirectUri)
      ) {
        location = target;
        await route.fulfill({
          status: 200,
          contentType: 'text/html',
          body: '<p id="operator-login-done">operator login</p>',
        });
        return;
      }
      await route.fulfill({ response });
    },
  );
  await page.goto(auth.url);
  await page.locator('#username').fill(account.email);
  await page.locator('#password').fill(account.password);
  await page.locator('#kc-login').click();
  // acr_values=2 always asks for the OTP (setup at the first login).
  await keycloakOtp(page, account.email, /#operator-login-done$/);
  await expect
    .poll(() => location, {
      message: `redirect to ${auth.redirectUri}; Keycloak stopped at ${page.url()}`,
      timeout: 30_000,
    })
    .not.toBe('');
  const redirect = new URL(location);
  const code = redirect.searchParams.get('code') ?? '';
  expect(
    code,
    `authorization code at the redirect URI, got ${location} (error ${redirect.searchParams.get('error')}: ${redirect.searchParams.get('error_description')})`,
  ).toBeTruthy();
  expect(redirect.searchParams.get('state'), 'state of the operator authorization').toBe(
    auth.state,
  );
  const state = auth.state;
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

/**
 * raiseCILimit lets an operator raise the max_cis quota of an organization
 * (POST /admin/orgs/{id}/entitlements, ENT-04): a trial tenant holds 100 CIs.
 * The operator is a new Keycloak user of the group operators (Keycloak admin
 * API) who signs in with operatorLogin; a refused step fails the setup.
 */
export async function raiseCILimit(
  browser: Browser,
  request: APIRequestContext,
  orgId: string,
  maxCIs: number,
): Promise<void> {
  const run = stamp();
  const operator = {
    email: `ci-limit-operator-${run}@audit-e2e.de`,
    password: `Audit-op-${run}!aA`,
  };
  const admin = await keycloakAdminToken(request);
  const created = await request.post(`${KEYCLOAK}/admin/realms/reticora/users`, {
    headers: { Authorization: `Bearer ${admin}` },
    data: {
      username: operator.email,
      email: operator.email,
      emailVerified: true,
      enabled: true,
      firstName: 'Audit',
      lastName: 'Operator',
      groups: ['/operators'],
      credentials: [{ type: 'password', value: operator.password, temporary: false }],
    },
    failOnStatusCode: false,
  });
  expect(created.status(), `create the operator account: ${await created.text()}`).toBe(201);
  const context = await browser.newContext({ locale: 'de-DE' });
  try {
    const login = await operatorLogin(await context.newPage(), context.request, operator);
    expect(login.status, `operator callback: ${JSON.stringify(login.body)}`).toBe(200);
    const granted = await api(
      request,
      login.body.token,
      'POST',
      `/admin/orgs/${orgId}/entitlements`,
      {
        feature_key: 'cmdb_core',
        plan: 'standard',
        enabled: true,
        limits: { max_cis: maxCIs },
      },
    );
    expect(granted.status, `raise max_cis: ${JSON.stringify(granted.body)}`).toBe(200);
  } finally {
    await context.close();
  }
}

/** A request the SNMP fake received (collector/internal/fakesnmp, Request). */
export interface FakeSnmpRequest {
  at: string;
  from: string;
  /** "1", "2c" or "3"; empty for a packet that is no SNMP message. */
  version: string;
  community: string;
  /** USM user name of an SNMPv3 message (empty for the engine discovery). */
  user?: string;
  pdu?: string;
  oids?: string[];
  /** usmStats report the SNMPv3 mode answered with, e.g. usmStatsWrongDigests. */
  report?: string;
  answered: boolean;
}

/** The one SNMPv3 user (authPriv) of the fake's SNMPv3 mode (WP-542). */
export interface FakeSnmpV3User {
  user: string;
  /** SHA, SHA224, SHA256, SHA384 or SHA512 (default SHA256). */
  authProtocol?: string;
  authPassword: string;
  /** AES, AES192 or AES256 (default AES). */
  privProtocol?: string;
  privPassword: string;
}

export interface FakeSnmpOptions {
  /** Address the fake listens on; the collector's SNMP plugin queries port 161. */
  ip: string;
  /** The only community the fake answers (SNMPv2c); omit for the SNMPv3 mode. */
  community?: string;
  /**
   * SNMPv3 mode: the fake answers only this user with its keys, reports a wrong key with
   * usmStatsWrongDigests and an unknown user with usmStatsUnknownUserNames (RFC 3414 3.2).
   */
  v3?: FakeSnmpV3User;
  sysName: string;
  sysDescr: string;
  sysObjectId: string;
}

export interface FakeSnmp {
  ip: string;
  /** Every request received so far, answered or not, with its community. */
  requests(): FakeSnmpRequest[];
  stop(): Promise<void>;
}

let fakeSnmpBin = FAKESNMP_BIN;

/** fakeSnmpBinary builds collector/cmd/fakesnmp once (Go toolchain on PATH). */
function fakeSnmpBinary(): string {
  if (fakeSnmpBin) return fakeSnmpBin;
  const bin = join(mkdtempSync(join(tmpdir(), 'reticora-fakesnmp-')), 'fakesnmp');
  const built = spawnSync('go', ['build', '-o', bin, './cmd/fakesnmp'], {
    cwd: join(REPO, 'collector'),
    env: { ...process.env, GOWORK: 'off', CGO_ENABLED: '0' },
    encoding: 'utf8',
    timeout: 5 * 60_000,
  });
  expect(
    built.status,
    `go build ./cmd/fakesnmp: ${built.error ?? ''} ${built.stderr} ${built.stdout}`,
  ).toBe(0);
  fakeSnmpBin = bin;
  return bin;
}

/**
 * startFakeSnmp builds and starts the SNMP fake of collector/cmd/fakesnmp on <ip>:161. As
 * SNMPv2c fake (WP-384) it answers sysDescr, sysObjectID and sysName only to requests with
 * its community; in the SNMPv3 mode (`v3`, WP-542) only to the authPriv requests of its user
 * with the right keys, a wrong key gets the report usmStatsWrongDigests. It records every
 * request it receives with the community or user it carried and the report it got, so a
 * test can prove which secret the collector sent. Port 161 needs
 * net.ipv4.ip_unprivileged_port_start <= 161 (fixtures/setup-real-stack.sh sets it on
 * GitHub Actions).
 */
export async function startFakeSnmp(opts: FakeSnmpOptions): Promise<FakeSnmp> {
  expect(
    Boolean(opts.community) !== Boolean(opts.v3),
    'the SNMP fake answers either a community (v2c) or an SNMPv3 user',
  ).toBe(true);
  const bin = fakeSnmpBinary();
  const requestsFile = join(
    mkdtempSync(join(tmpdir(), 'reticora-fakesnmp-run-')),
    'requests.jsonl',
  );
  const identity = opts.v3
    ? [
        '-v3-user',
        opts.v3.user,
        '-v3-auth-protocol',
        opts.v3.authProtocol ?? 'SHA256',
        '-v3-auth-password',
        opts.v3.authPassword,
        '-v3-priv-protocol',
        opts.v3.privProtocol ?? 'AES',
        '-v3-priv-password',
        opts.v3.privPassword,
      ]
    : ['-community', opts.community!];
  const proc = spawn(
    bin,
    [
      '-listen',
      `${opts.ip}:161`,
      ...identity,
      '-sysname',
      opts.sysName,
      '-sysdescr',
      opts.sysDescr,
      '-sysobjectid',
      opts.sysObjectId,
      '-requests',
      requestsFile,
    ],
    { stdio: ['ignore', 'pipe', 'pipe'] },
  );
  let output = '';
  proc.stdout?.on('data', (chunk: Buffer) => (output += chunk.toString()));
  proc.stderr?.on('data', (chunk: Buffer) => (output += chunk.toString()));
  const exited = new Promise<void>((resolve) => proc.once('exit', () => resolve()));
  const running = () => proc.exitCode === null && proc.signalCode === null;
  const deadline = Date.now() + 30_000;
  while (running() && !output.includes('fakesnmp listening on') && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  if (running() && !output.includes('fakesnmp listening on')) proc.kill('SIGKILL');
  expect(output, `SNMP fake on ${opts.ip}:161 (exit code ${proc.exitCode})`).toContain(
    'fakesnmp listening on',
  );
  return {
    ip: opts.ip,
    requests: () => {
      if (!existsSync(requestsFile)) return [];
      // One JSON line per request; what follows the last newline is still being written.
      const lines = readFileSync(requestsFile, 'utf8').split('\n');
      lines.pop();
      return lines
        .filter((line) => line.trim() !== '')
        .map((line) => JSON.parse(line) as FakeSnmpRequest);
    },
    stop: async () => {
      if (!running()) return;
      proc.kill('SIGTERM');
      await exited;
    },
  };
}

// ─── The compose project of the installed stack (WP-547) ────────────────────

/**
 * The compose project the cloud installer set up: install-smoke.yml installs into
 * deploy/docker-compose of the checkout with the configuration in .env. E2E_COMPOSE_DIR and
 * E2E_COMPOSE_ENV_FILE point elsewhere.
 */
export const COMPOSE_DIR = process.env.E2E_COMPOSE_DIR || join(REPO, 'deploy', 'docker-compose');
export const COMPOSE_ENV_FILE = process.env.E2E_COMPOSE_ENV_FILE || '.env';

function composeEnvPath(): string {
  return join(COMPOSE_DIR, COMPOSE_ENV_FILE);
}

/** readComposeEnv returns the variables of the installation's .env (last assignment wins). */
export function readComposeEnv(): Record<string, string> {
  const vars: Record<string, string> = {};
  for (const line of readFileSync(composeEnvPath(), 'utf8').split('\n')) {
    const match = /^([A-Z0-9_]+)=(.*)$/.exec(line);
    if (match) vars[match[1]] = match[2].replace(/^["']|["']$/g, '');
  }
  return vars;
}

/**
 * writeComposeEnv changes variables of the installation's .env the way an operator does
 * (docs/ops/workers.md): a value replaces every assignment of the variable, undefined
 * removes it. Other lines stay as they are.
 */
export function writeComposeEnv(changes: Record<string, string | undefined>) {
  const path = composeEnvPath();
  const lines = readFileSync(path, 'utf8')
    .split('\n')
    .filter((line) => !Object.keys(changes).some((key) => line.startsWith(`${key}=`)));
  while (lines.length > 0 && lines[lines.length - 1] === '') lines.pop();
  for (const [key, value] of Object.entries(changes)) {
    if (value !== undefined) lines.push(`${key}=${value}`);
  }
  writeFileSync(path, `${lines.join('\n')}\n`);
}

/** The settings of the test overlays the server was started with (setup-real-stack.sh). */
let overlayEnv: Record<string, string> | undefined;

function composeFiles(): string[] {
  const files = ['-f', 'docker-compose.yml'];
  if (existsSync(join(COMPOSE_DIR, 'docker-compose.override.yml'))) {
    files.push('-f', 'docker-compose.override.yml');
  }
  files.push(
    '-f',
    join(REPO, 'frontend', 'e2e', 'fixtures', 'docker-compose.e2e.yml'),
    '-f',
    join(REPO, 'frontend', 'e2e-real', 'fixtures', 'docker-compose.e2e-real.yml'),
  );
  return files;
}

function compose(args: string[]): string {
  return execFileSync(
    'docker',
    ['compose', '--env-file', COMPOSE_ENV_FILE, ...composeFiles(), ...args],
    {
      cwd: COMPOSE_DIR,
      env: { ...process.env, ...overlayEnv },
      encoding: 'utf8',
      timeout: 5 * 60_000,
    },
  );
}

/** A container of the compose project as docker reports it. */
export interface ServiceContainer {
  id: string;
  startedAt: string;
  restartCount: number;
  running: boolean;
  env: Record<string, string>;
}

/** serviceContainer inspects the (running or stopped) container of a compose service. */
export function serviceContainer(service: string): ServiceContainer {
  const id = execFileSync(
    'docker',
    ['compose', '--env-file', COMPOSE_ENV_FILE, 'ps', '-aq', service],
    { cwd: COMPOSE_DIR, encoding: 'utf8', timeout: 60_000 },
  )
    .trim()
    .split('\n')[0];
  expect(id, `container of the compose service ${service} in ${COMPOSE_DIR}`).toBeTruthy();
  const [state] = JSON.parse(
    execFileSync('docker', ['inspect', id], { encoding: 'utf8', timeout: 60_000 }),
  ) as {
    Id: string;
    RestartCount: number;
    State: { StartedAt: string; Running: boolean };
    Config: { Env: string[] };
  }[];
  const env: Record<string, string> = {};
  for (const entry of state.Config.Env) {
    const at = entry.indexOf('=');
    env[entry.slice(0, at)] = entry.slice(at + 1);
  }
  return {
    id: state.Id,
    startedAt: state.State.StartedAt,
    restartCount: state.RestartCount,
    running: state.State.Running,
    env,
  };
}

/** serviceLogs returns what the current container of a service logged since `since`. */
export function serviceLogs(service: string, since: Date): string {
  const run = spawnSync(
    'docker',
    ['logs', '--since', since.toISOString(), serviceContainer(service).id],
    {
      encoding: 'utf8',
      timeout: 60_000,
    },
  );
  return `${run.stdout}${run.stderr}`;
}

async function waitFor(url: string, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url);
      if (res.ok) return true;
    } catch {
      // not up yet
    }
    await new Promise((resolve) => setTimeout(resolve, 2_000));
  }
  return false;
}

/** The outcome of restartServer. */
export interface ServerRestart {
  /** /readyz of the restarted server answered 200 within the wait. */
  ready: boolean;
  /** What the server logged since the restart. */
  logs: string;
  /** The restarted server container (its environment as the server got it). */
  container: ServiceContainer;
}

/**
 * restartServer changes the installation's .env as given (writeComposeEnv) and restarts
 * only the server of the installed stack (k-e2e-real-ci, install-smoke.yml) with the
 * compose files setup-real-stack.sh applied and the settings of their test overlays
 * (signup secrets and nginx.conf, taken from the running server like the module run of
 * install-smoke.yml does): `docker compose up -d --no-deps --force-recreate server` (also when
 * only a variable changed that compose does not pass to the server). No other service is
 * restarted; only the frontend container is recreated after a server that came up,
 * because nginx resolves the server at its start (setup-real-stack.sh). It waits up to
 * readyWithinMs for /readyz and reports the outcome, it does not assert it: a start that
 * must fail is a test of its own.
 */
export async function restartServer(
  changes: Record<string, string | undefined>,
  readyWithinMs = 3 * 60_000,
): Promise<ServerRestart> {
  if (!overlayEnv) {
    const env = serviceContainer('server').env;
    overlayEnv = {
      E2E_CAPTCHA_KEY: env.RETICORA_SIGNUP_CAPTCHA_KEY ?? '',
      E2E_KC_CLIENT_ID: env.RETICORA_KEYCLOAK_ADMIN_CLIENT_ID ?? '',
      E2E_KC_CLIENT_SECRET: env.RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET ?? '',
      E2E_NGINX_CONF:
        process.env.E2E_NGINX_CONF ||
        join(process.env.RUNNER_TEMP || '/tmp', 'nginx-e2e-real.conf'),
    };
    expect(
      overlayEnv.E2E_CAPTCHA_KEY && overlayEnv.E2E_KC_CLIENT_SECRET,
      'the server runs with the overlays of setup-real-stack.sh (signup secrets)',
    ).toBeTruthy();
    expect(existsSync(overlayEnv.E2E_NGINX_CONF), overlayEnv.E2E_NGINX_CONF).toBe(true);
  }
  writeComposeEnv(changes);
  const since = new Date(Date.now() - 1_000);
  compose(['up', '-d', '--no-deps', '--force-recreate', 'server']);
  const ready = await waitFor(`${SERVER}/readyz`, readyWithinMs);
  if (ready) {
    compose(['up', '-d', '--no-deps', '--force-recreate', 'frontend']);
    expect(
      await waitFor(`${BASE}/api/v1/auth/config`, 2 * 60_000),
      'the recreated frontend reaches the restarted server',
    ).toBe(true);
  }
  return { ready, logs: serviceLogs('server', since), container: serviceContainer('server') };
}
