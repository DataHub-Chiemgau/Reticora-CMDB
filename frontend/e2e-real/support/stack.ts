import { spawn, spawnSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { existsSync, mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
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

/** A request the SNMP fake received (collector/internal/fakesnmp, Request). */
export interface FakeSnmpRequest {
  at: string;
  from: string;
  /** "1", "2c" or "3"; empty for a packet that is no SNMP message. */
  version: string;
  community: string;
  pdu?: string;
  oids?: string[];
  answered: boolean;
}

export interface FakeSnmpOptions {
  /** Address the fake listens on; the collector's SNMP plugin queries port 161. */
  ip: string;
  /** The only community the fake answers. */
  community: string;
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
 * startFakeSnmp builds and starts the SNMPv2c fake of collector/cmd/fakesnmp (WP-384) on
 * <ip>:161. It answers sysDescr, sysObjectID and sysName only to requests with its
 * community and records every request it receives with the community it carried, so a
 * test can prove which secret the collector sent. Port 161 needs
 * net.ipv4.ip_unprivileged_port_start <= 161 (fixtures/setup-real-stack.sh sets it on
 * GitHub Actions).
 */
export async function startFakeSnmp(opts: FakeSnmpOptions): Promise<FakeSnmp> {
  const bin = fakeSnmpBinary();
  const requestsFile = join(
    mkdtempSync(join(tmpdir(), 'reticora-fakesnmp-run-')),
    'requests.jsonl',
  );
  const proc = spawn(
    bin,
    [
      '-listen',
      `${opts.ip}:161`,
      '-community',
      opts.community,
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
