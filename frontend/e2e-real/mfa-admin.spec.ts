import { test, expect, type Browser, type Page } from '@playwright/test';
import { BASE, api, sessionToken, signupTenant, stamp } from './support/stack';
import { keycloakOtp, totp } from '../e2e/support/totp';

/*
 * MFA for every org_admin on the REAL stack (WP-358, AUT-09, CH26, E-14,
 * finding F-005). The realm asks holders of the realm role mfa_required for
 * a TOTP code; the server maps that role to the Keycloak account on every
 * grant of the app role org_admin. Each way to org_admin must make the next
 * login run through CONFIGURE_TOTP, refuse a wrong code and reach the app
 * only with a valid one (RFC 6238, e2e/support/totp.ts). A viewer signs in
 * without any OTP step (control).
 *
 * Test → requirement → work package
 *  01 self-signup administrator ............... AUT-09, TLC-01 → WP-357, WP-358
 *  02 org_admin of an accepted invitation ..... AUT-09, TLC-03 → WP-358
 *  03 viewer signs in without OTP (control) ... AUT-09         → WP-358
 *  04 viewer promoted through /role-assignments AUT-09, API-05 → WP-358
 *
 * Retest of F-005: "Signup-/Einladungs-Admin muss CONFIGURE_TOTP durchlaufen".
 */

const run = stamp();
const LANDING = /\/(dashboard|onboarding)/;
const email = (label: string) => `mfa-${label}-${run}@audit-e2e.de`;
const password = (label: string) => `Audit-${label}-${run}!aA`;

interface Credentials {
  email: string;
  password: string;
}

const tenant: {
  admin?: Credentials;
  token?: string;
  roles?: Record<string, string>;
  viewer?: Credentials & { userId: string };
} = {};

test.describe.configure({ mode: 'serial' });

/** passwordStep opens a fresh browser and submits the Keycloak password form. */
async function passwordStep(browser: Browser, account: Credentials) {
  const context = await browser.newContext({ locale: 'de-DE' });
  const page = await context.newPage();
  await page.goto('/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('button', { name: /anmelden|sign in/i }).click();
  await page.waitForURL(/\/protocol\/openid-connect\/auth/);
  await page.locator('#username').fill(account.email);
  await page.locator('#password').fill(account.password);
  await page.locator('#kc-login').click();
  return { context, page };
}

async function noSession(page: Page) {
  expect(new URL(page.url()).origin, 'still on Keycloak, not in the app').not.toBe(
    new URL(BASE).origin,
  );
}

/**
 * loginWithOtpSetup signs in an account that must set up OTP: Keycloak shows
 * the CONFIGURE_TOTP page after the password, a wrong code keeps the user
 * there, and only a valid code leads into the app. Returns the session token.
 */
async function loginWithOtpSetup(browser: Browser, account: Credentials): Promise<string> {
  const { context, page } = await passwordStep(browser, account);
  const setup = page.locator('#kc-totp-settings-form');
  await expect(setup, 'CONFIGURE_TOTP after the password (AUT-09)').toBeVisible({
    timeout: 60_000,
  });
  await noSession(page);

  // A code of a far away time step is no valid code.
  const secret = await page.locator('#totpSecret').inputValue();
  await page.locator('#totp').fill(totp(secret, Math.floor(Date.now() / 30_000) + 1000));
  const refused = page.waitForResponse(
    (r) => r.request().method() === 'POST' && r.url().includes('/login-actions/'),
  );
  await page.locator('#saveTOTPBtn').click();
  await refused;
  await page.waitForLoadState('domcontentloaded');
  await expect(setup, 'a wrong code keeps the user on the OTP setup').toBeVisible();
  await noSession(page);

  await keycloakOtp(page, account.email, LANDING);
  await page.waitForURL(LANDING, { timeout: 60_000 });
  const token = await sessionToken(page);
  await context.close();
  return token;
}

/** loginWithoutOtp signs in an account without mfa_required: no OTP step at all. */
async function loginWithoutOtp(browser: Browser, account: Credentials): Promise<string> {
  const { context, page } = await passwordStep(browser, account);
  await page.waitForURL(LANDING, { timeout: 60_000 });
  const token = await sessionToken(page);
  await context.close();
  return token;
}

async function permissions(request: Parameters<typeof api>[0], token: string) {
  const res = await api<{ permissions: string[] }>(request, token, 'GET', '/me/permissions');
  expect(res.status, 'GET /me/permissions').toBe(200);
  return res.body.permissions;
}

/** inviteAndAccept issues an invitation as the administrator and accepts it with a password. */
async function inviteAndAccept(
  request: Parameters<typeof api>[0],
  role: string,
  account: Credentials,
) {
  const inv = await api<{ token: string }>(request, tenant.token!, 'POST', '/invitations', {
    email: account.email,
    role_id: tenant.roles![role],
  });
  expect(inv.status, `issue ${role} invitation: ${JSON.stringify(inv.body)}`).toBe(201);
  const res = await request.post(`${BASE}/api/v1/invitations/accept`, {
    data: { token: inv.body.token, password: account.password, display_name: `Audit ${role}` },
    failOnStatusCode: false,
  });
  const body = (await res.json()) as { user_id: string; account_created: boolean };
  expect(res.status(), `accept ${role} invitation: ${JSON.stringify(body)}`).toBe(201);
  expect(body.account_created, 'the acceptance created the Keycloak account').toBe(true);
  return body.user_id;
}

test('01 the self-signup administrator sets up OTP at the first login and enters only with a valid code', async ({
  browser,
  request,
}) => {
  test.setTimeout(5 * 60_000);
  // Own client address for the signup budget (E-33), see support/stack.ts.
  const clientIp = `198.51.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
  const context = await browser.newContext({
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': clientIp },
  });
  const page = await context.newPage();
  const admin = await signupTenant(page, context.request, 'mfa');
  await context.close();

  const token = await loginWithOtpSetup(browser, admin);
  const roles = await api<{ data: { id: string; name: string }[] }>(
    request,
    token,
    'GET',
    '/roles?limit=100',
  );
  expect(roles.status).toBe(200);
  tenant.admin = admin;
  tenant.token = token;
  tenant.roles = Object.fromEntries(roles.body.data.map((r) => [r.name, r.id]));
  expect(tenant.roles.org_admin, 'standard role org_admin').toBeTruthy();
});

test('02 an org_admin of an accepted invitation sets up OTP at the first login and enters only with a valid code', async ({
  browser,
  request,
}) => {
  const account = { email: email('invited'), password: password('invited') };
  await inviteAndAccept(request, 'org_admin', account);
  const token = await loginWithOtpSetup(browser, account);
  expect(await permissions(request, token)).toContain('user:manage');
});

test('03 a viewer signs in without any OTP step (control)', async ({ browser, request }) => {
  const account = { email: email('viewer'), password: password('viewer') };
  const userId = await inviteAndAccept(request, 'viewer', account);
  const token = await loginWithoutOtp(browser, account);
  expect(await permissions(request, token)).not.toContain('user:manage');
  tenant.viewer = { ...account, userId };
});

test('04 a viewer promoted to org_admin through POST /role-assignments sets up OTP at the next login', async ({
  browser,
  request,
}) => {
  const viewer = tenant.viewer!;
  const assigned = await api(request, tenant.token!, 'POST', '/role-assignments', {
    user_id: viewer.userId,
    role_id: tenant.roles!.org_admin,
  });
  expect(assigned.status, JSON.stringify(assigned.body)).toBe(201);
  const token = await loginWithOtpSetup(browser, viewer);
  expect(await permissions(request, token)).toContain('user:manage');
});
