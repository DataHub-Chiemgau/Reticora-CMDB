import { test, expect } from '@playwright/test';
import {
  KEYCLOAK,
  api,
  keycloakAdminToken,
  operatorLogin,
  signupTenant,
  stamp,
  type OperatorSession,
} from './support/stack';

/*
 * Personalized operator login on the REAL stack (WP-361, SEC-07, finding
 * F-069). Operators are members of the realm group operators (realm role
 * operator, which contains mfa_required). The operator login requests
 * acr_values=2 (support/stack.ts, operatorAuthorization); the realm flow
 * browser-mfa reaches level 2 only with an OTP, and the server issues an
 * operator session only for the group and acr 2. The OTP is computed like an
 * authenticator app does (e2e/support/totp.ts). A login without the second
 * factor (acr 1) cannot be produced here – the role operator makes Keycloak
 * ask for the OTP – and is covered by
 * backend/internal/operator/operator_test.go.
 *
 * Test → requirement → work package
 *  01 operator with OTP: callback 200, /admin/me oidc, audited ... SEC-07 → WP-361
 *  02 tenant admin without the group: callback 403, audited ...... SEC-07 → WP-361
 *
 * Retest of F-069: "Operator mit OTP → admin/auth/callback 200, admin/me 200".
 */

const run = stamp();
const REALM_ADMIN = `${KEYCLOAK}/admin/realms/reticora`;

interface AuditEntry {
  operator_id: string;
  operator_kind: string;
  action: string;
  status: number;
}

const state: { operatorToken?: string } = {};

test.describe.configure({ mode: 'serial' });

/** auditEntries reads the newest operator_audit entries with an operator session. */
async function auditEntries(request: Parameters<typeof api>[0], token: string) {
  const res = await api<{ data: AuditEntry[] }>(request, token, 'GET', '/admin/audit?limit=100');
  expect(res.status, `GET /admin/audit: ${JSON.stringify(res.body)}`).toBe(200);
  return res.body.data;
}

/** keycloakUserId returns the Keycloak id (the ID token subject) of an e-mail. */
async function keycloakUserId(
  request: Parameters<typeof api>[0],
  admin: string,
  email: string,
): Promise<string> {
  const res = await request.get(`${REALM_ADMIN}/users`, {
    params: { email, exact: 'true' },
    headers: { Authorization: `Bearer ${admin}` },
  });
  expect(res.ok(), `Keycloak user ${email}: ${res.status()}`).toBe(true);
  const users = (await res.json()) as { id: string }[];
  expect(users, `exactly one Keycloak account for ${email}`).toHaveLength(1);
  return users[0]!.id;
}

test('01 a member of the realm group operators sets up OTP and gets an operator session (callback 200, /admin/me oidc, operator_audit)', async ({
  browser,
  request,
}) => {
  const admin = await keycloakAdminToken(request);
  const account = {
    email: `operator-${run}@audit-e2e.de`,
    password: `Audit-op-${run}!aA`,
  };
  const created = await request.post(`${REALM_ADMIN}/users`, {
    headers: { Authorization: `Bearer ${admin}` },
    data: {
      username: account.email,
      email: account.email,
      emailVerified: true,
      enabled: true,
      firstName: 'Audit',
      lastName: 'Operator',
      groups: ['/operators'],
      credentials: [{ type: 'password', value: account.password, temporary: false }],
    },
    failOnStatusCode: false,
  });
  expect(created.status(), `create the operator account: ${await created.text()}`).toBe(201);
  const subject = await keycloakUserId(request, admin, account.email);
  const groups = await request.get(`${REALM_ADMIN}/users/${subject}/groups`, {
    headers: { Authorization: `Bearer ${admin}` },
  });
  expect(
    ((await groups.json()) as { name: string }[]).map((g) => g.name),
    'member of the realm group operators',
  ).toContain('operators');

  const context = await browser.newContext({ locale: 'de-DE' });
  const page = await context.newPage();
  const login = await operatorLogin(page, context.request, account);
  await context.close();
  expect(login.status, `operator callback: ${JSON.stringify(login.body)}`).toBe(200);
  const session = login.body as OperatorSession;
  expect(session.token).toBeTruthy();
  expect(session.operator).toMatchObject({ id: subject, kind: 'oidc', email: account.email });

  const me = await api<{ id: string; kind: string }>(request, session.token, 'GET', '/admin/me');
  expect(me.status, `GET /admin/me: ${JSON.stringify(me.body)}`).toBe(200);
  expect(me.body).toMatchObject({ id: subject, kind: 'oidc' });

  const entries = await auditEntries(request, session.token);
  expect(
    entries.some(
      (e) =>
        e.action === 'login' &&
        e.operator_id === subject &&
        e.operator_kind === 'oidc' &&
        e.status === 200,
    ),
    'operator_audit records the login',
  ).toBe(true);
  state.operatorToken = session.token;
});

test('02 a tenant administrator outside the group operators is refused (callback 403, audited)', async ({
  browser,
  request,
}) => {
  test.setTimeout(5 * 60_000);
  expect(state.operatorToken, 'operator session of test 01').toBeTruthy();
  // Own client address for the signup budget (E-33), see support/stack.ts.
  const clientIp = `198.51.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
  const signupContext = await browser.newContext({
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': clientIp },
  });
  const tenantAdmin = await signupTenant(
    await signupContext.newPage(),
    signupContext.request,
    'op',
  );
  await signupContext.close();
  const subject = await keycloakUserId(
    request,
    await keycloakAdminToken(request),
    tenantAdmin.email,
  );

  const context = await browser.newContext({ locale: 'de-DE' });
  const page = await context.newPage();
  // The org_admin holds mfa_required: the login itself runs through the OTP.
  const login = await operatorLogin(page, context.request, tenantAdmin);
  await context.close();
  expect(login.status, `operator callback of a tenant admin: ${JSON.stringify(login.body)}`).toBe(
    403,
  );
  expect(JSON.stringify(login.body)).not.toContain('"token"');

  const entries = await auditEntries(request, state.operatorToken!);
  expect(
    entries.some((e) => e.action === 'login' && e.operator_id === subject && e.status === 403),
    'operator_audit records the refused login',
  ).toBe(true);
});
