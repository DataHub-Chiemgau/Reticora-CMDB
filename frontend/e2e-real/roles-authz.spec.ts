import { test, expect, type Browser, type Page } from '@playwright/test';
import {
  BASE,
  KEYCLOAK,
  api,
  keycloakAdminToken,
  keycloakLogin,
  signupTenant,
  stamp,
  uiLogin,
  type ApiResult,
} from './support/stack';
import {
  ageInvitation,
  createKeycloakUser,
  databaseReachable,
  loadState,
  mailTo,
  psql,
  saveState,
  type RunState,
} from './support/roles-authz';

/*
 * Role and permission boundaries on the REAL stack (Phase-1 acceptance audit).
 * Chain: UI → API → PostgreSQL → UI. One fresh tenant (self-signup) per run;
 * its administrator invites a viewer, who accepts and signs in.
 *
 * Test → requirement → work package
 *  01 invitation issue/accept/sign-in ........ TLC-03, API-05, AUT-01, RBA-02 → WP-114, WP-043, WP-046
 *  02 invitation accepted only once ........... TLC-03 ("einmalig")          → WP-114
 *  03 revoked / renewed / conflicting invites . TLC-03, API-05              → WP-114
 *  04 expired invitation 410, renewable ....... TLC-03 ("7 Tage", "erneuerbar") → WP-114
 *  05 invitation e-mail ....................... NTF-05 ("Einladung"), TLC-03 → WP-140, WP-114
 *  05b invitation through the UI and its link .. TLC-03, API-05 (F-142)       → WP-510
 *  06 viewer API boundaries (403) ............. RBA-02, RBA-04, RBA-01, AUT-04 → WP-045, WP-046, WP-068, WP-067, WP-114
 *     incl. location writes, tree UI, engineer . LOC-07, RBA-02               → WP-474
 *  07 viewer self-escalation refused .......... RBA-02, RBA-03, RBA-04, TLC-03 → WP-045, WP-046, WP-009, WP-114
 *  08 viewer UI: CIs read-only ................ RBA-02, RBA-04 (UI-04/UI-05)  → WP-046, WP-045
 *  09 viewer UI: admin pages .................. RBA-02, RBA-04               → WP-046, WP-045
 *  09b roles tab labels system roles .......... RBA-02 ("Systemrollen")       → WP-046
 *  09c viewer UI: webhook save refused visibly . RBA-02, RBA-04               → WP-046, WP-045
 *  10 Keycloak user without invitation ........ AUT-01, AUT-09 (CH26)        → WP-043, WP-042
 *  10b Keycloak user without org attribute ..... AUT-09 (CH26, E-14)          → WP-043
 *  11 role upgrade effective at refresh/login . AUT-02, RBA-03, RBA-02       → WP-066, WP-009, WP-046
 *     incl. engineer reads the CI history ...... CI-06 (E-78)                → WP-475
 *  12 upgraded user creates a CI in the UI .... RBA-02, CI-01 (UI-04)        → WP-046, WP-120, WP-191
 *  13 role revocation effective ............... AUT-02, RBA-03, API-05       → WP-066, WP-009, WP-046
 *  14 SPA renews the session by cookie ........ AUT-02                       → WP-066, WP-009
 *  14a cookie-only refresh on the API ......... AUT-02, API-04               → WP-364 (F-018)
 *  14b SPA renews when the 15 minutes ran out .. AUT-02                       → WP-066
 *  15 IdP group reticora-viewer = viewer rights . RBA-02, AUT-09 (F-200)      → WP-046, WP-428
 *
 * The assertions encode the requirement texts (docs/spec/katalog-v3) and the
 * API contract (api/openapi.yaml); a failure is a product finding.
 */

// RBA-02: permissions the viewer must not hold (✓ only for org_admin/engineer).
const VIEWER_FORBIDDEN = [
  'ci:write',
  'ci:delete',
  'location:write',
  'rack:write',
  'relationship:write',
  'contact:write',
  'review:resolve',
  'export:run',
  'job:read',
  'discovery:manage',
  'collector:manage',
  'credential:manage',
  'vrf:manage',
  'citype:manage',
  'webhook:manage',
  'notification:manage',
  'user:manage',
  'team:manage',
  'role:manage',
  'entitlement:manage',
  'audit:read',
  'apikey:manage',
];
const VIEWER_REQUIRED = ['ci:read', 'location:read', 'topology:read'];

interface Invitation {
  id: string;
  email: string;
  role_id: string;
  scope_type: string;
  status: string;
  expires_at: string;
  created_at: string;
  accepted_at?: string;
  token?: string;
}
interface Accepted {
  user_id: string;
  organization_id: string;
  email: string;
  account_created: boolean;
}
interface RoleAssignment {
  id: string;
  user_id: string;
  role_id?: string;
  custom_role_id?: string;
}
interface CI {
  id: string;
  name: string;
  version: number;
}
interface User {
  id: string;
  email: string;
  status?: string;
}

const run = stamp();
const password = (label: string) => `Audit-${label}-${run}!aA`;
const email = (label: string) => `${label}-${run}@audit-e2e.de`;

function state(): RunState {
  return loadState();
}

/** adminCall calls the API as the tenant administrator. */
async function adminCall<T = unknown>(
  request: Parameters<typeof api>[0],
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
  headers?: Record<string, string>,
): Promise<ApiResult<T>> {
  return api<T>(request, state().adminToken!, method, path, data, headers);
}

/** acceptInvitation is the public acceptance call: no session, the token is the credential. */
async function acceptInvitation(
  request: Parameters<typeof api>[0],
  token: string,
  pw: string,
  displayName = 'Audit Invitee',
) {
  const res = await request.post(`${BASE}/api/v1/invitations/accept`, {
    data: { token, password: pw, display_name: displayName },
    failOnStatusCode: false,
  });
  const text = await res.text();
  let body: unknown = text;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    // keep raw text
  }
  return { status: res.status(), body: body as Accepted };
}

async function listUsers(request: Parameters<typeof api>[0]): Promise<User[]> {
  const res = await adminCall<{ data: User[] }>(request, 'GET', '/users?limit=200');
  expect(res.status, 'admin lists users').toBe(200);
  return res.body.data;
}

async function assignmentsOf(request: Parameters<typeof api>[0], userId: string) {
  const res = await adminCall<{ data: RoleAssignment[] }>(
    request,
    'GET',
    `/role-assignments?user_id=${userId}`,
  );
  expect(res.status, 'admin lists role assignments').toBe(200);
  return res.body.data;
}

async function permissionsOf(request: Parameters<typeof api>[0], token: string) {
  const res = await api<{ permissions: string[] }>(request, token, 'GET', '/me/permissions');
  expect(res.status, 'GET /me/permissions').toBe(200);
  return res.body.permissions;
}

/**
 * cookieRefresh renews the session with the HttpOnly refresh cookie of the
 * signed-in browser, as an API client following the OpenAPI contract sends it
 * (JSON request). Test 13 checks the request exactly as the SPA sends it.
 */
async function cookieRefresh(page: Page): Promise<string> {
  const res = await page.request.post(`${BASE}/api/v1/auth/refresh`, {
    headers: { 'Content-Type': 'application/json' },
    data: {},
  });
  expect(res.status(), `POST /auth/refresh: ${await res.text()}`).toBe(200);
  return ((await res.json()) as { token: string }).token;
}

/** signIn opens a fresh browser context (no shared Keycloak SSO) and signs in. */
async function signIn(browser: Browser, account: { email: string; password: string }) {
  const context = await browser.newContext({ locale: 'de-DE' });
  const page = await context.newPage();
  const token = await uiLogin(page, account);
  return { context, page, token };
}

/** issue issues an invitation as the administrator and checks the 201 contract. */
async function issue(
  request: Parameters<typeof api>[0],
  address: string,
  roleName: string,
): Promise<Invitation> {
  const res = await adminCall<Invitation>(request, 'POST', '/invitations', {
    email: address,
    role_id: state().roles![roleName],
  });
  expect(res.status, `issue invitation: ${JSON.stringify(res.body)}`).toBe(201);
  expect(res.body.token, 'the token is returned once on issue').toBeTruthy();
  return res.body;
}

/**
 * ensureViewer makes sure the invited viewer exists (test 01 creates it; a
 * later test re-creates it when test 01 failed before).
 */
async function ensureViewer(request: Parameters<typeof api>[0]) {
  const current = state().viewer;
  if (current) return current;
  const address = email('viewer');
  const inv = await issue(request, address, 'viewer');
  const accepted = await acceptInvitation(request, inv.token!, password('viewer'));
  expect(accepted.status, `accept: ${JSON.stringify(accepted.body)}`).toBe(201);
  return saveState({
    viewer: { email: address, password: password('viewer'), userId: accepted.body.user_id },
  }).viewer!;
}

/** ensureCI creates one CI as the administrator (the object the viewer reads). */
async function ensureCI(request: Parameters<typeof api>[0]) {
  const current = state().ci;
  if (current) return current;
  const name = `authz-ci-${run}`;
  const res = await adminCall<CI>(request, 'POST', '/cis', {
    name,
    ci_type_id: state().serverTypeId,
    status: 'active',
    discovery_source: 'manual',
  });
  expect(res.status, `admin creates CI: ${JSON.stringify(res.body)}`).toBe(201);
  return saveState({ ci: { id: res.body.id, name, version: res.body.version } }).ci!;
}

test.beforeAll(async ({ browser }) => {
  test.setTimeout(4 * 60_000);
  if (state().adminToken) return;
  // The self-signup allows five signups per client IP and hour (E-33). On
  // this shared audit stack every spec reaches the server through the same
  // local proxy, so the run presents its own client address the way the
  // production reverse proxy does (X-Real-IP from a trusted proxy).
  const clientIp = `198.51.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
  const context = await browser.newContext({
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': clientIp },
  });
  const page = await context.newPage();
  const admin = await signupTenant(page, context.request, 'authz');
  const adminToken = await uiLogin(page, admin);
  const org = await api<{ id: string; name: string }>(context.request, adminToken, 'GET', '/org');
  expect(org.status).toBe(200);
  expect(org.body.name).toBe(admin.org);
  const me = await api<{ sub: string }>(context.request, adminToken, 'GET', '/me');
  expect(me.status).toBe(200);
  const roles = await api<{ data: { id: string; name: string }[] }>(
    context.request,
    adminToken,
    'GET',
    '/roles?limit=100',
  );
  expect(roles.status).toBe(200);
  const byName = Object.fromEntries(roles.body.data.map((r) => [r.name, r.id]));
  for (const name of ['org_admin', 'engineer', 'viewer', 'client_technician']) {
    expect(byName[name], `system role ${name} in a fresh tenant (RBA-02)`).toBeTruthy();
  }
  const types = await api<{ data: { id: string; key: string }[] }>(
    context.request,
    adminToken,
    'GET',
    '/ci-types?limit=200',
  );
  expect(types.status).toBe(200);
  const serverTypeId = types.body.data.find((t) => t.key === 'server')?.id;
  expect(serverTypeId, 'seeded CI type server').toBeTruthy();
  saveState({
    createdAt: Date.now(),
    serverTypeId,
    admin,
    adminToken,
    adminUserId: me.body.sub,
    orgId: org.body.id,
    roles: byName,
  });
  await context.close();
});

test('01 admin invites a viewer; the invitee accepts and signs in with exactly the viewer rights', async ({
  browser,
  request,
}) => {
  const address = email('viewer');
  const before = Date.now();
  const inv = await issue(request, address, 'viewer');
  // TLC-03: valid for seven days, role and scope predefined, pending.
  expect(inv.status).toBe('pending');
  expect(inv.role_id).toBe(state().roles!.viewer);
  expect(inv.scope_type).toBe('org');
  const validity = Date.parse(inv.expires_at) - before;
  expect(validity).toBeGreaterThan(7 * 24 * 3600_000 - 5 * 60_000);
  expect(validity).toBeLessThan(7 * 24 * 3600_000 + 5 * 60_000);
  expect(inv.token!.length).toBeGreaterThan(40);

  // The list shows the invitation but never the token again (only its hash is stored).
  const listed = await adminCall<{ data: Invitation[] }>(request, 'GET', '/invitations');
  expect(listed.status).toBe(200);
  const row = listed.body.data.find((i) => i.id === inv.id);
  expect(row?.status).toBe('pending');
  expect(row?.token).toBeUndefined();
  if (databaseReachable()) {
    const stored = psql(
      `SELECT token_hash <> :'tok' AND length(token_hash) = 64 FROM user_invitation WHERE id = :'id';`,
      { tok: inv.token!, id: inv.id },
    );
    expect(stored, 'only a SHA-256 hash of the token is stored').toBe('t');
  }

  // Acceptance is public: the token is the credential (no session).
  const accepted = await acceptInvitation(request, inv.token!, password('viewer'));
  expect(accepted.status, JSON.stringify(accepted.body)).toBe(201);
  expect(accepted.body.email).toBe(address);
  expect(accepted.body.organization_id).toBe(state().orgId);
  expect(accepted.body.account_created, 'the IdP account is created with the password').toBe(true);
  saveState({
    viewer: { email: address, password: password('viewer'), userId: accepted.body.user_id },
  });

  // API → DB: the user exists once, with exactly the invited role (org-wide).
  const users = (await listUsers(request)).filter((u) => u.email === address);
  expect(users.map((u) => u.id)).toEqual([accepted.body.user_id]);
  const assignments = await assignmentsOf(request, accepted.body.user_id);
  expect(assignments.map((a) => a.role_id)).toEqual([state().roles!.viewer]);
  const afterAccept = await adminCall<{ data: Invitation[] }>(request, 'GET', '/invitations');
  expect(afterAccept.body.data.find((i) => i.id === inv.id)?.status).toBe('accepted');

  // UI: the invitee signs in through Keycloak and lands in the application.
  const { context, page, token } = await signIn(browser, {
    email: address,
    password: password('viewer'),
  });
  await expect(page.getByRole('button', { name: 'Abmelden' })).toBeVisible();
  const me = await api<{ sub: string; org: string }>(request, token, 'GET', '/me');
  expect(me.body.sub).toBe(accepted.body.user_id);
  expect(me.body.org).toBe(state().orgId);
  // RBA-02: viewer reads CIs, locations, topology and holds no write or admin right.
  const perms = await permissionsOf(request, token);
  for (const p of VIEWER_REQUIRED) expect.soft(perms, `viewer holds ${p}`).toContain(p);
  for (const p of VIEWER_FORBIDDEN) expect.soft(perms, `viewer lacks ${p}`).not.toContain(p);
  expect(
    perms.filter((p) => p.startsWith('credential:')),
    'viewer has no credential right (WP-046)',
  ).toEqual([]);
  await context.close();
});

test('02 an invitation is accepted only once: a second acceptance is refused and creates nothing', async ({
  request,
}) => {
  const viewer = await ensureViewer(request);
  // A fresh invitation, accepted once, then replayed with the same token.
  const address = email('once');
  const inv = await issue(request, address, 'viewer');
  const first = await acceptInvitation(request, inv.token!, password('once'));
  expect(first.status, JSON.stringify(first.body)).toBe(201);
  const replay = await acceptInvitation(request, inv.token!, password('once2'), 'Replay');
  // OpenAPI: 404 for an unknown, revoked or used token.
  expect(replay.status, JSON.stringify(replay.body)).toBe(404);
  // Two parallel acceptances of one token: exactly one wins (atomic).
  const raceAddress = email('race');
  const race = await issue(request, raceAddress, 'viewer');
  const results = await Promise.all([
    acceptInvitation(request, race.token!, password('race')),
    acceptInvitation(request, race.token!, password('race')),
  ]);
  expect(results.map((r) => r.status).sort()).toEqual([201, 404]);

  const users = await listUsers(request);
  expect(users.filter((u) => u.email === address)).toHaveLength(1);
  expect(users.filter((u) => u.email === raceAddress)).toHaveLength(1);
  expect(await assignmentsOf(request, first.body.user_id)).toHaveLength(1);
  // The viewer of test 01 is unaffected.
  expect(users.filter((u) => u.email === viewer.email)).toHaveLength(1);
});

test('03 revoked invitations and the old token of a renewed invitation are refused; conflicts are reported', async ({
  request,
}) => {
  const viewer = await ensureViewer(request);

  // Revoked: 204, gone from the list, its token no longer works.
  const revoked = await issue(request, email('revoked'), 'viewer');
  const del = await adminCall(request, 'DELETE', `/invitations/${revoked.id}`);
  expect(del.status).toBe(204);
  const list = await adminCall<{ data: Invitation[] }>(request, 'GET', '/invitations');
  expect(list.body.data.find((i) => i.id === revoked.id)).toBeUndefined();
  const useRevoked = await acceptInvitation(request, revoked.token!, password('revoked'));
  expect(useRevoked.status, JSON.stringify(useRevoked.body)).toBe(404);
  expect((await listUsers(request)).some((u) => u.email === revoked.email)).toBe(false);

  // Renewed: a new token and seven more days; the old token stops working.
  const renewedAddress = email('renewed');
  const original = await issue(request, renewedAddress, 'viewer');
  const renewed = await adminCall<Invitation>(request, 'POST', `/invitations/${original.id}/renew`);
  expect(renewed.status).toBe(200);
  expect(renewed.body.token).toBeTruthy();
  expect(renewed.body.token).not.toBe(original.token);
  const oldToken = await acceptInvitation(request, original.token!, password('renewed'));
  expect(oldToken.status, 'old token after renewal').toBe(404);
  const newToken = await acceptInvitation(request, renewed.body.token!, password('renewed'));
  expect(newToken.status, JSON.stringify(newToken.body)).toBe(201);

  // An accepted invitation stays as a record: neither revocable nor renewable (409).
  expect((await adminCall(request, 'DELETE', `/invitations/${original.id}`)).status).toBe(409);
  expect((await adminCall(request, 'POST', `/invitations/${original.id}/renew`)).status).toBe(409);

  // A second pending invitation for the same address is a conflict (renew instead).
  const pendingAddress = email('pending');
  await issue(request, pendingAddress, 'viewer');
  const duplicate = await adminCall(request, 'POST', '/invitations', {
    email: pendingAddress,
    role_id: state().roles!.engineer,
  });
  expect(duplicate.status).toBe(409);

  // Role and scope must exist in the organization (422); malformed input is 400.
  const foreignRole = await adminCall(request, 'POST', '/invitations', {
    email: email('norole'),
    role_id: '00000000-0000-4000-8000-000000000000',
  });
  expect(foreignRole.status).toBe(422);
  const badMail = await adminCall(request, 'POST', '/invitations', {
    email: 'not an address',
    role_id: state().roles!.viewer,
  });
  expect(badMail.status).toBe(400);

  // An invitation to an address that is already a user cannot create a second user (409).
  const existing = await issue(request, viewer.email, 'org_admin');
  const dup = await acceptInvitation(request, existing.token!, password('dupe'));
  expect(dup.status, JSON.stringify(dup.body)).toBe(409);
  const assignments = await assignmentsOf(request, viewer.userId);
  expect(
    assignments.map((a) => a.role_id),
    'the existing viewer did not gain org_admin through a second invitation',
  ).not.toContain(state().roles!.org_admin);
  await adminCall(request, 'DELETE', `/invitations/${existing.id}`);
});

test('04 an expired invitation is refused with 410 and can be renewed for seven more days', async ({
  request,
}) => {
  test.skip(!databaseReachable(), 'needs psql access to age an invitation (no API for it)');
  const address = email('expired');
  const inv = await issue(request, address, 'viewer');
  ageInvitation(state().orgId!, inv.id);

  const listed = await adminCall<{ data: Invitation[] }>(request, 'GET', '/invitations');
  expect(listed.body.data.find((i) => i.id === inv.id)?.status).toBe('expired');
  const late = await acceptInvitation(request, inv.token!, password('expired'));
  expect(late.status, JSON.stringify(late.body)).toBe(410);
  expect((await listUsers(request)).some((u) => u.email === address)).toBe(false);

  const before = Date.now();
  const renewed = await adminCall<Invitation>(request, 'POST', `/invitations/${inv.id}/renew`);
  expect(renewed.status).toBe(200);
  expect(renewed.body.status).toBe('pending');
  const validity = Date.parse(renewed.body.expires_at) - before;
  expect(validity).toBeGreaterThan(7 * 24 * 3600_000 - 5 * 60_000);
  const accepted = await acceptInvitation(request, renewed.body.token!, password('expired'));
  expect(accepted.status, JSON.stringify(accepted.body)).toBe(201);
});

test('05 an invitation sends the invitee an e-mail (NTF-05 event "Einladung")', async ({
  request,
}) => {
  // The tenant gets an e-mail channel over the installation's SMTP first, so
  // a missing channel cannot be the reason for a missing mail.
  const channel = await adminCall<{ id: string }>(request, 'POST', '/notification-channels', {
    name: `authz-mail-${run}`,
    kind: 'email',
    provider: 'central_smtp',
    config: {},
    enabled: true,
  });
  expect(channel.status, JSON.stringify(channel.body)).toBe(201);
  const address = email('mailed');
  await issue(request, address, 'viewer');
  const mail = await mailTo(request, address, 60_000);
  expect(mail, `invitation e-mail to ${address} in the mail catcher`).not.toBeNull();
  expect(`${mail!.Subject} ${mail!.Text ?? ''}`).toMatch(/einladung|invit/i);
  expect(mail!.Text ?? mail!.HTML ?? '').toContain(state().admin!.org);
});

test('05b the admin invites in the UI; the mailed link opens the accept page, creates the account and works only once', async ({
  browser,
  request,
}) => {
  // The invitation mail goes over the organization's e-mail channels (NTF-05).
  const channel = await adminCall<{ id: string }>(request, 'POST', '/notification-channels', {
    name: `authz-mail-ui-${run}`,
    kind: 'email',
    provider: 'central_smtp',
    config: {},
    enabled: true,
  });
  expect(channel.status, JSON.stringify(channel.body)).toBe(201);
  // Two clients: the invitee is scoped to the first and must not see the second.
  const clientName = `Invite Nord ${run}`;
  const otherName = `Invite Süd ${run}`;
  const client = await adminCall<{ id: string }>(request, 'POST', '/clients', {
    name: clientName,
    slug: `inv-nord-${run}`,
  });
  expect(client.status, JSON.stringify(client.body)).toBe(201);
  const other = await adminCall<{ id: string }>(request, 'POST', '/clients', {
    name: otherName,
    slug: `inv-sued-${run}`,
  });
  expect(other.status, JSON.stringify(other.body)).toBe(201);

  // UI: the administrator invites a viewer for the first client.
  const address = email('uiinvite');
  const admin = await signIn(browser, state().admin!);
  await admin.page.goto('/users');
  const main = admin.page.locator('#main-content');
  await main.getByRole('button', { name: 'Benutzer einladen' }).click();
  await main.getByLabel('E-Mail').fill(address);
  await main.getByLabel('Rolle').selectOption({ label: 'viewer' });
  await main.getByLabel('Scope-Typ').selectOption('client');
  await main.getByLabel('Scope-Ziel').selectOption({ label: clientName });
  await main.getByRole('button', { name: 'Einladung senden' }).click();
  await expect(main.getByRole('status')).toContainText(`Einladung an ${address} gesendet`);
  const pendingRow = main.getByRole('row', { name: new RegExp(address.replace(/[.+]/g, '\\$&')) });
  await expect(pendingRow).toContainText('viewer');
  await expect(pendingRow).toContainText(`Kunde: ${clientName}`);
  await expect(pendingRow).toContainText('Offen');

  // Mailpit: the mail carries the link to the accept page with the token.
  const mail = await mailTo(request, address, 60_000);
  expect(mail, `invitation e-mail to ${address} in the mail catcher`).not.toBeNull();
  const match = `${mail!.Text ?? ''} ${mail!.HTML ?? ''}`.match(
    /https?:\/\/[^\s"'<>]+\/invitations\/accept\?token=[A-Za-z0-9_\-.%]+/,
  );
  expect(match, 'the mail contains the accept link').not.toBeNull();
  const link = new URL(match![0].replace(/&amp;/g, '&'));
  expect(link.pathname).toBe('/invitations/accept');
  expect(link.searchParams.get('token')).toBeTruthy();

  // The link opens the public accept page (no session) and creates the account.
  const inviteeContext = await browser.newContext({ locale: 'de-DE' });
  const invitee = await inviteeContext.newPage();
  await invitee.goto(`${link.pathname}${link.search}`);
  await expect(invitee.getByRole('heading', { name: 'Einladung annehmen' })).toBeVisible();
  await invitee.getByLabel('Anzeigename').fill('UI Invitee');
  await invitee.getByLabel(/^Passwort/).fill(password('uiinvite'));
  await invitee.getByRole('button', { name: 'Konto anlegen' }).click();
  await invitee.waitForURL(/\/login$/);
  await expect(invitee.getByRole('status')).toContainText(`Ihr Konto für ${address} ist angelegt`);

  // API → DB: one user with the viewer role, scoped to the first client.
  const users = (await listUsers(request)).filter((u) => u.email === address);
  expect(users, 'exactly one account for the invitee').toHaveLength(1);
  const assignments = await adminCall<{
    data: (RoleAssignment & { scope_type?: string; scope_id?: string })[];
  }>(request, 'GET', `/role-assignments?user_id=${users[0]!.id}`);
  expect(assignments.status).toBe(200);
  expect(assignments.body.data.map((a) => [a.role_id, a.scope_type, a.scope_id])).toEqual([
    [state().roles!.viewer, 'client', client.body.id],
  ]);

  // Login: the invitee signs in; the UI shows the scope (only the invited
  // client is selectable) and the role (viewer rights, no write or admin right).
  await invitee.getByRole('button', { name: 'Anmelden' }).click();
  await keycloakLogin(
    invitee,
    { email: address, password: password('uiinvite') },
    /\/(dashboard|onboarding)/,
  );
  await invitee.goto('/permissions');
  const scopeClient = invitee
    .getByRole('group', { name: 'Arbeitsbereich' })
    .getByRole('combobox', { name: 'Kunde', exact: true });
  await expect(scopeClient).toBeEnabled();
  await expect(scopeClient.locator('option')).toHaveText(['Alle Kunden', clientName]);
  const permissionRow = (key: string) =>
    invitee.getByRole('row', { name: new RegExp(`^${key.replace(':', '\\:')}\\b`) });
  await expect(permissionRow('ci:read')).toContainText('Gewährt');
  await expect(permissionRow('ci:write')).toContainText('Nicht gewährt');
  await expect(permissionRow('user:manage')).toContainText('Nicht gewährt');
  await inviteeContext.close();

  // The admin list shows the invitation as accepted, without actions.
  await admin.page.reload();
  await main.getByRole('button', { name: 'Einladungen', exact: true }).click();
  await expect(pendingRow).toContainText('Angenommen');
  await expect(pendingRow.getByRole('button')).toHaveCount(0);
  await admin.context.close();

  // Negative: the same link a second time is refused and creates nothing.
  const replayContext = await browser.newContext({ locale: 'de-DE' });
  const replay = await replayContext.newPage();
  await replay.goto(`${link.pathname}${link.search}`);
  await replay.getByLabel('Anzeigename').fill('Replay');
  await replay.getByLabel(/^Passwort/).fill(password('uireplay'));
  await replay.getByRole('button', { name: 'Konto anlegen' }).click();
  await expect(replay.getByRole('alert')).toContainText('bereits verwendet');
  await expect(replay.getByRole('button', { name: 'Konto anlegen' })).toHaveCount(0);
  expect(replay.url()).toContain('/invitations/accept');
  await replayContext.close();
  expect((await listUsers(request)).filter((u) => u.email === address)).toHaveLength(1);
});

test('06 the viewer reads CIs, but every write, delete and administrative call is refused with 403', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const ci = await ensureCI(request);
  const { context, page, token } = await signIn(browser, viewer);
  const v = (method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, data?: unknown) =>
    api(request, token, method, path, data);

  // Location tree of the administrator: the viewer reads it but writes nothing (LOC-07).
  const locClient = await adminCall<{ id: string }>(request, 'POST', '/clients', {
    name: `authz-loc-client-${run}`,
    slug: `authz-loc-client-${run}`,
  });
  expect(locClient.status, JSON.stringify(locClient.body)).toBe(201);
  const locSite = await adminCall<{ id: string; name: string }>(request, 'POST', '/locations', {
    kind: 'site',
    name: `authz-site-${run}`,
    client_id: locClient.body.id,
  });
  expect(locSite.status, JSON.stringify(locSite.body)).toBe(201);
  const locBuilding = await adminCall<{ id: string; name: string }>(request, 'POST', '/locations', {
    kind: 'building',
    name: `authz-building-${run}`,
    parent_id: locSite.body.id,
  });
  expect(locBuilding.status, JSON.stringify(locBuilding.body)).toBe(201);
  expect((await v('GET', `/locations/${locBuilding.body.id}`)).status).toBe(200);

  // Read is allowed (ci:read, location:read).
  const list = await api<{ data: CI[] }>(request, token, 'GET', '/cis?limit=50');
  expect(list.status).toBe(200);
  expect(list.body.data.map((c) => c.id)).toContain(ci.id);
  expect((await v('GET', `/cis/${ci.id}`)).status).toBe(200);
  expect((await v('GET', '/org')).status).toBe(200);

  const pending = await issue(request, email('target'), 'viewer');
  const webhook = await adminCall<{ id: string }>(request, 'POST', '/webhooks', {
    name: `authz-hook-${run}`,
    url: 'https://example.invalid/hook',
    secret: 'audit-secret-0123456789',
    events: ['ci.created'],
  });
  expect(webhook.status, JSON.stringify(webhook.body)).toBe(201);

  const refused: [string, 'POST' | 'PUT' | 'PATCH' | 'DELETE' | 'GET', string, unknown?][] = [
    ['create CI', 'POST', '/cis', { name: `viewer-ci-${run}`, ci_type_id: state().serverTypeId }],
    ['edit CI', 'PATCH', `/cis/${ci.id}`, { name: `viewer-renamed-${run}` }],
    ['delete CI', 'DELETE', `/cis/${ci.id}`],
    ['bulk status', 'POST', '/cis/bulk', { ids: [ci.id], status: 'inactive' }],
    ['create user', 'POST', '/users', { email: email('vuser'), display_name: 'x' }],
    ['deactivate admin', 'PATCH', `/users/${state().adminUserId}`, { status: 'inactive' }],
    ['delete admin', 'DELETE', `/users/${state().adminUserId}`],
    ['create role', 'POST', '/roles', { name: `viewer-role-${run}`, permissions: ['ci:read'] }],
    ['edit viewer role', 'PATCH', `/roles/${state().roles!.viewer}`, { description: 'x' }],
    ['delete engineer role', 'DELETE', `/roles/${state().roles!.engineer}`],
    ['list invitations', 'GET', '/invitations'],
    [
      'issue invitation',
      'POST',
      '/invitations',
      { email: email('vinv'), role_id: state().roles!.viewer },
    ],
    ['renew invitation', 'POST', `/invitations/${pending.id}/renew`],
    ['revoke invitation', 'DELETE', `/invitations/${pending.id}`],
    ['list API keys', 'GET', '/api-keys'],
    [
      'create API key',
      'POST',
      '/api-keys',
      { name: `viewer-key-${run}`, permissions: ['ci:read'] },
    ],
    [
      'create webhook',
      'POST',
      '/webhooks',
      {
        name: `viewer-hook-${run}`,
        url: 'https://example.invalid/v',
        secret: 'x'.repeat(24),
        events: ['ci.created'],
      },
    ],
    ['delete webhook', 'DELETE', `/webhooks/${webhook.body.id}`],
    [
      'security settings',
      'PATCH',
      '/org',
      { settings: { require_if_match: true, viewer_probe: run } },
    ],
    ['create team', 'POST', '/teams', { name: `viewer-team-${run}` }],
    [
      'create notification channel',
      'POST',
      '/notification-channels',
      { name: `v-${run}`, provider: 'inapp' },
    ],
    ['audit log', 'GET', '/audit-log'],
    [
      'create location',
      'POST',
      '/locations',
      { kind: 'building', name: `viewer-building-${run}`, parent_id: locSite.body.id },
    ],
    [
      'rename/move location',
      'PATCH',
      `/locations/${locBuilding.body.id}`,
      { name: `viewer-renamed-building-${run}` },
    ],
    ['delete location', 'DELETE', `/locations/${locBuilding.body.id}`],
  ];
  for (const [label, method, path, data] of refused) {
    const res = await v(method, path, data);
    expect.soft(res.status, `${label}: ${method} ${path} → ${JSON.stringify(res.body)}`).toBe(403);
  }

  // Tree UI: every refused write is shown to the viewer, none fails silently (LOC-07).
  const forbidden = 'Keine Berechtigung für diese Aktion.';
  await page.goto('/locations');
  const row = page
    .locator('#main-content li > div')
    .filter({ has: page.getByText(locBuilding.body.name, { exact: true }) });
  await expect(row).toBeVisible();
  // Add a child.
  await row.getByRole('button', { name: '+ Unterpunkt' }).click();
  let dialog = page.getByRole('dialog', { name: `Unterpunkt in ${locBuilding.body.name}` });
  await dialog.getByLabel('Name', { exact: true }).fill(`viewer-room-${run}`);
  let sent = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/locations') && r.request().method() === 'POST',
  );
  await dialog.getByRole('button', { name: 'Anlegen' }).click();
  expect((await sent).status()).toBe(403);
  await expect(dialog.getByRole('alert')).toHaveText(forbidden);
  await dialog.getByRole('button', { name: 'Abbrechen' }).click();
  // Rename.
  await row.getByRole('button', { name: 'Bearbeiten' }).click();
  dialog = page.getByRole('dialog', { name: `${locBuilding.body.name} bearbeiten` });
  await dialog.getByLabel('Name', { exact: true }).fill(`viewer-ui-renamed-${run}`);
  sent = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/v1/locations/${locBuilding.body.id}`) &&
      r.request().method() === 'PATCH',
  );
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  expect((await sent).status()).toBe(403);
  await expect(dialog.getByRole('alert')).toHaveText(forbidden);
  await dialog.getByRole('button', { name: 'Abbrechen' }).click();
  // Delete after the confirmation.
  await row.getByRole('button', { name: 'Löschen' }).click();
  dialog = page.getByRole('dialog', { name: `„${locBuilding.body.name}“ löschen?` });
  sent = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/v1/locations/${locBuilding.body.id}`) &&
      r.request().method() === 'DELETE',
  );
  await dialog.getByRole('button', { name: 'Löschen' }).click();
  expect((await sent).status()).toBe(403);
  await expect(
    page.getByRole('alert').filter({ hasText: `Löschen nicht möglich: ${forbidden}` }),
  ).toBeVisible();
  await page.reload();
  await expect(row).toBeVisible();
  await context.close();

  // Nothing of it was stored: the building is unchanged, no child exists.
  const building = await adminCall<{ name: string; parent_id?: string }>(
    request,
    'GET',
    `/locations/${locBuilding.body.id}`,
  );
  expect(building.status, 'the building was not deleted').toBe(200);
  expect(building.body).toMatchObject({ name: locBuilding.body.name, parent_id: locSite.body.id });
  const children = await adminCall<{ data: { name: string }[] }>(
    request,
    'GET',
    `/locations?parent_id=${locBuilding.body.id}`,
  );
  expect(children.status).toBe(200);
  expect(children.body.data, 'no location was created below the building').toEqual([]);
  const siblings = await adminCall<{ data: { name: string }[] }>(
    request,
    'GET',
    `/locations?parent_id=${locSite.body.id}`,
  );
  expect(siblings.body.data.map((l) => l.name)).toEqual([locBuilding.body.name]);

  // Positive case: an engineer (location:write) creates, renames and deletes (201/200/204).
  const engineerAddress = email('loc-engineer');
  const engineerInv = await issue(request, engineerAddress, 'engineer');
  const engineerAccepted = await acceptInvitation(
    request,
    engineerInv.token!,
    password('loc-engineer'),
  );
  expect(engineerAccepted.status, JSON.stringify(engineerAccepted.body)).toBe(201);
  const engineer = await signIn(browser, {
    email: engineerAddress,
    password: password('loc-engineer'),
  });
  const e = (method: 'POST' | 'PATCH' | 'DELETE', path: string, data?: unknown) =>
    api<{ id: string; name: string }>(request, engineer.token, method, path, data);
  const created = await e('POST', '/locations', {
    kind: 'building',
    name: `engineer-building-${run}`,
    parent_id: locSite.body.id,
  });
  expect(created.status, JSON.stringify(created.body)).toBe(201);
  const renamedByEngineer = await e('PATCH', `/locations/${created.body.id}`, {
    name: `engineer-building-renamed-${run}`,
  });
  expect(renamedByEngineer.status, JSON.stringify(renamedByEngineer.body)).toBe(200);
  expect(renamedByEngineer.body.name).toBe(`engineer-building-renamed-${run}`);
  const deletedByEngineer = await e('DELETE', `/locations/${created.body.id}`);
  expect(deletedByEngineer.status, JSON.stringify(deletedByEngineer.body)).toBe(204);
  expect((await adminCall(request, 'GET', `/locations/${created.body.id}`)).status).toBe(404);
  await engineer.context.close();

  // DB (through the admin's view): nothing the viewer tried exists or changed.
  const after = await adminCall<CI>(request, 'GET', `/cis/${ci.id}`);
  expect(after.status, 'the CI was not deleted').toBe(200);
  expect(after.body.name).toBe(ci.name);
  expect(after.body.version).toBe(ci.version);
  const cis = await adminCall<{ total: number }>(request, 'GET', `/cis?search=viewer-ci-${run}`);
  expect(cis.body.total).toBe(0);
  expect((await listUsers(request)).some((u) => u.email === email('vuser'))).toBe(false);
  const invs = await adminCall<{ data: Invitation[] }>(request, 'GET', '/invitations');
  expect(invs.body.data.find((i) => i.id === pending.id)?.status).toBe('pending');
  expect(invs.body.data.some((i) => i.email === email('vinv'))).toBe(false);
  const roles = await adminCall<{ data: { name: string }[] }>(request, 'GET', '/roles?limit=100');
  expect(roles.body.data.map((r) => r.name)).not.toContain(`viewer-role-${run}`);
  expect(roles.body.data.map((r) => r.name)).toContain('engineer');
  const keys = await adminCall<{ data: { name: string }[] }>(request, 'GET', '/api-keys');
  expect(keys.body.data.map((k) => k.name)).not.toContain(`viewer-key-${run}`);
  const hooks = await adminCall<{ data: { id: string; name: string }[] }>(
    request,
    'GET',
    '/webhooks',
  );
  expect(hooks.body.data.map((h) => h.name)).not.toContain(`viewer-hook-${run}`);
  expect(hooks.body.data.map((h) => h.id)).toContain(webhook.body.id);
  const org = await adminCall<{ settings?: Record<string, unknown> }>(request, 'GET', '/org');
  expect(org.body.settings?.viewer_probe).toBeUndefined();
  const admin = (await listUsers(request)).find((u) => u.id === state().adminUserId);
  expect(admin?.status).toBe('active');
});

test('07 the viewer cannot raise its own rights through role, user, invitation or key endpoints', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const roles = state().roles!;
  const { context, page, token } = await signIn(browser, viewer);
  const self = viewer.userId;

  // exact403: a mapped write route answers 403 (RBA-04). The legacy path
  // /users/{id}/roles only exists for GET; another method must not succeed
  // either (any 4xx).
  const attempts: [string, 'POST' | 'PUT' | 'PATCH', string, unknown, boolean][] = [
    [
      'assign org_admin to self',
      'POST',
      '/role-assignments',
      { user_id: self, role_id: roles.org_admin },
      true,
    ],
    [
      'legacy assign to self',
      'POST',
      '/roles/assign',
      { user_id: self, role_id: roles.org_admin },
      true,
    ],
    ['PUT own roles', 'PUT', `/users/${self}/roles`, { role_ids: [roles.org_admin] }, false],
    ['POST own roles', 'POST', `/users/${self}/roles`, { role_id: roles.org_admin }, false],
    [
      'PATCH own user with a role',
      'PATCH',
      `/users/${self}`,
      { role: 'org_admin', role_id: roles.org_admin },
      true,
    ],
    [
      'grant user:manage to the viewer role',
      'PUT',
      `/roles/${roles.viewer}/permissions`,
      { permissions: ['ci:read', 'user:manage', 'role:manage'] },
      true,
    ],
    [
      'widen the viewer role',
      'PATCH',
      `/roles/${roles.viewer}`,
      { permissions: ['user:manage'] },
      true,
    ],
    [
      'invite own second address as admin',
      'POST',
      '/invitations',
      { email: email('selfadmin'), role_id: roles.org_admin },
      true,
    ],
    [
      'API key with admin scopes',
      'POST',
      '/api-keys',
      { name: `esc-${run}`, permissions: ['user:manage', 'role:manage'] },
      true,
    ],
  ];
  for (const [label, method, path, data, exact403] of attempts) {
    const res = await api(request, token, method, path, data);
    const what = `${label}: ${method} ${path} → ${res.status} ${JSON.stringify(res.body)}`;
    if (exact403) {
      expect.soft(res.status, what).toBe(403);
    } else {
      expect.soft(res.status, what).toBeGreaterThanOrEqual(400);
      expect.soft(res.status, what).toBeLessThan(500);
    }
  }

  // DB: the viewer still holds exactly the viewer role and the role is unchanged.
  const assignments = await assignmentsOf(request, self);
  expect(assignments.map((a) => a.role_id)).toEqual([roles.viewer]);
  const roleList = await adminCall<{ data: { id: string; permissions: string[] }[] }>(
    request,
    'GET',
    '/roles?limit=100',
  );
  const viewerRole = roleList.body.data.find((r) => r.id === roles.viewer);
  expect(viewerRole?.permissions).toContain('ci:read');
  expect(viewerRole?.permissions).not.toContain('user:manage');
  expect(viewerRole?.permissions).not.toContain('role:manage');

  // Session: a refresh still carries viewer rights only.
  const perms = await permissionsOf(request, await cookieRefresh(page));
  for (const p of ['user:manage', 'role:manage', 'apikey:manage', 'ci:write']) {
    expect(perms).not.toContain(p);
  }
  await context.close();
});

test('08 viewer UI: CIs are visible; creating or editing a CI is not offered or ends in a visible error', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const ci = await ensureCI(request);
  const { context, page } = await signIn(browser, viewer);

  await page.goto('/cmdb');
  await expect(page.getByRole('link', { name: ci.name })).toBeVisible();

  // Create: either no enabled control, or the attempt shows an error.
  const uiName = `viewer-ui-ci-${run}`;
  const create = page.getByRole('button', { name: 'CI erstellen' }).first();
  if ((await create.isVisible()) && (await create.isEnabled())) {
    await create.click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Name').fill(uiName);
    await dialog.getByLabel('CI-Typ').selectOption({ label: 'Server' });
    const post = page.waitForResponse(
      (r) => r.url().endsWith('/api/v1/cis') && r.request().method() === 'POST',
    );
    await dialog.getByRole('button', { name: 'Anlegen' }).click();
    expect((await post).status()).toBe(403);
    await expect(dialog, 'the dialog stays open on refusal').toBeVisible();
    await expect(dialog.getByRole('alert')).toBeVisible();
    await expect(dialog.getByRole('alert')).not.toBeEmpty();
    await dialog.getByRole('button', { name: 'Abbrechen' }).click();
    await expect(dialog).toBeHidden();
  }

  // Edit: same rule on the detail page.
  await page.getByRole('link', { name: ci.name }).click();
  await expect(page.getByRole('heading', { name: ci.name })).toBeVisible();
  const edit = page.getByRole('button', { name: 'Bearbeiten' }).first();
  if ((await edit.isVisible()) && (await edit.isEnabled())) {
    await edit.click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Name').fill(`${ci.name}-renamed`);
    const patch = page.waitForResponse(
      (r) => r.url().includes(`/api/v1/cis/${ci.id}`) && r.request().method() === 'PATCH',
    );
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    expect((await patch).status()).toBe(403);
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('alert')).toBeVisible();
    await expect(dialog.getByRole('alert')).not.toBeEmpty();
    await dialog.getByRole('button', { name: 'Abbrechen' }).click();
  }
  await context.close();

  // DB through the admin: nothing was created or renamed.
  const created = await adminCall<{ total: number }>(request, 'GET', `/cis?search=${uiName}`);
  expect(created.body.total).toBe(0);
  const after = await adminCall<CI>(request, 'GET', `/cis/${ci.id}`);
  expect(after.body.name).toBe(ci.name);
});

test('09 viewer UI: user, role and invitation management is not offered; refused pages say so', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const { context, page } = await signIn(browser, viewer);

  await page.goto('/users');
  await expect(page.getByRole('heading', { name: 'Benutzer & Teams' })).toBeVisible();
  const main = page.locator('#main-content');
  for (const tab of ['Benutzer', 'Teams', 'Rollen']) {
    await main.getByRole('button', { name: tab, exact: true }).click();
    await expect(main.getByRole('heading', { name: new RegExp(`^${tab} \\(`) })).toBeVisible();
    await expect(
      main.getByRole('button', {
        name: /einlad|anlegen|hinzuf|zuweis|lösch|deaktiv|entfern|invite|assign|delete|remove/i,
      }),
      `no management control on the ${tab} tab`,
    ).toHaveCount(0);
  }

  // The audit log needs audit:read (RBA-02: not for viewers): an explicit error, not an empty log.
  await page.goto('/audit');
  await expect(main.getByRole('alert')).toBeVisible();
  await expect(main.getByRole('alert')).toContainText(/permission|berechtigung|forbidden/i);
  await context.close();
});

test('09b the roles tab presents the four RBA-02 roles as system roles, not as custom roles', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const { context, page } = await signIn(browser, viewer);
  await page.goto('/users');
  const main = page.locator('#main-content');
  await main.getByRole('button', { name: 'Rollen', exact: true }).click();
  await expect(main.getByRole('heading', { name: /^Rollen \(/ })).toBeVisible();
  for (const role of ['org_admin', 'engineer', 'viewer', 'client_technician']) {
    await expect
      .soft(main.getByRole('row', { name: new RegExp(`^${role}\\b`) }), `${role} is a system role`)
      .toContainText('System', { timeout: 5_000 });
  }
  await context.close();
});

test('09c viewer UI: saving a webhook is refused visibly, not silently', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const { context, page } = await signIn(browser, viewer);
  // The form is shown to the viewer; saving must not end silently.
  await page.goto('/webhooks');
  await expect(page.getByRole('heading', { name: 'Webhooks' })).toBeVisible();
  const hookName = `viewer-ui-hook-${run}`;
  const save = page.getByRole('button', { name: 'Speichern' });
  if ((await save.count()) > 0) {
    await page.getByLabel('Name').fill(hookName);
    await page.getByLabel('URL').fill('https://example.invalid/viewer');
    await page.getByLabel('Geheimnis (HMAC-Signatur)').fill('viewer-secret-0123456789');
    await page.getByLabel('ci.created').check();
    if (await save.isEnabled()) {
      // Feedback is an alert, an error toast or an error text in the page
      // content; none of it may be on the page before the attempt.
      const feedback = page
        .getByRole('alert')
        .or(page.getByRole('status').filter({ hasText: /\S/ }))
        .or(
          page
            .locator('#main-content')
            .getByText(/permission|berechtigung|forbidden|verweigert|nicht erlaubt|403/i),
        );
      await expect(feedback).toHaveCount(0);
      const post = page.waitForResponse(
        (r) => r.url().endsWith('/api/v1/webhooks') && r.request().method() === 'POST',
      );
      await save.click();
      expect((await post).status()).toBe(403);
      await expect(
        feedback.first(),
        'the refused save is reported to the viewer (no silent failure)',
      ).toBeVisible({ timeout: 10_000 });
    }
  }
  await context.close();
  const hooks = await adminCall<{ data: { name: string }[] }>(request, 'GET', '/webhooks');
  expect(hooks.body.data.map((h) => h.name)).not.toContain(hookName);
});

/** loginExpectingRefusal signs in through Keycloak and returns the callback response. */
async function loginExpectingRefusal(page: Page, account: { email: string; password: string }) {
  await page.goto('/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('button', { name: /anmelden|sign in/i }).click();
  const callback = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/auth/callback') && r.request().method() === 'POST',
    { timeout: 90_000 },
  );
  await keycloakLogin(page, account, /\/auth\/callback/);
  return callback;
}

test('10 a Keycloak account of the organization without invitation is refused (403); with an invitation it is admitted', async ({
  browser,
  request,
}) => {
  const address = email('uninvited');
  const pw = password('uninvited');
  await createKeycloakUser(request, { email: address, password: pw, orgId: state().orgId! });

  const context = await browser.newContext({ locale: 'de-DE' });
  const page = await context.newPage();
  const response = await loginExpectingRefusal(page, { email: address, password: pw });
  expect(response.status(), 'AUT-01: first login without admission').toBe(403);
  await expect(page.getByText(/invitation|einladung/i)).toBeVisible();
  await expect(page).not.toHaveURL(/\/dashboard/);
  const session = await page.evaluate(() => window.sessionStorage.getItem('reticora-auth-session'));
  expect(session, 'no session after the refusal').toBeNull();
  await page.goto('/cmdb');
  await expect(page).toHaveURL(/\/login/);
  await context.close();
  expect(
    (await listUsers(request)).some((u) => u.email === address),
    'no app user was created',
  ).toBe(false);

  // The same account with a pending invitation is admitted on its first
  // login, which accepts the invitation with the invited role.
  const inv = await issue(request, address, 'viewer');
  const second = await signIn(browser, { email: address, password: pw });
  const me = await api<{ sub: string }>(request, second.token, 'GET', '/me');
  const perms = await permissionsOf(request, second.token);
  expect(perms).toContain('ci:read');
  expect(perms).not.toContain('ci:write');
  await second.context.close();
  const listed = await adminCall<{ data: Invitation[] }>(request, 'GET', '/invitations');
  expect(listed.body.data.find((i) => i.id === inv.id)?.status).toBe('accepted');
  expect((await assignmentsOf(request, me.body.sub)).map((a) => a.role_id)).toEqual([
    state().roles!.viewer,
  ]);
});

test('10b a Keycloak account without organization attribute is refused and gets no session', async ({
  browser,
  request,
}) => {
  const address = email('noorg');
  const pw = password('noorg');
  await createKeycloakUser(request, { email: address, password: pw });
  const context = await browser.newContext({ locale: 'de-DE' });
  const page = await context.newPage();
  const response = await loginExpectingRefusal(page, { email: address, password: pw });
  // AUT-09/CH26 (E-14): the organization comes from the attribute; without it the login is refused.
  expect([401, 403], `callback → ${response.status()}`).toContain(response.status());
  await expect(page).not.toHaveURL(/\/dashboard/);
  const session = await page.evaluate(() => window.sessionStorage.getItem('reticora-auth-session'));
  expect(session).toBeNull();
  await context.close();
  expect((await listUsers(request)).some((u) => u.email === address)).toBe(false);
});

test('11 an admin role change (viewer → engineer) takes effect at the next refresh and sign-in', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const { context, page, token } = await signIn(browser, viewer);
  const before = await api(request, token, 'POST', '/cis', {
    name: `pre-upgrade-${run}`,
    ci_type_id: state().serverTypeId,
  });
  expect(before.status).toBe(403);

  const assigned = await adminCall<RoleAssignment>(request, 'POST', '/role-assignments', {
    user_id: viewer.userId,
    role_id: state().roles!.engineer,
  });
  expect(assigned.status, JSON.stringify(assigned.body)).toBe(201);

  // AUT-02: role changes take effect at the latest with the next refresh.
  const refreshedToken = await cookieRefresh(page);
  const viaRefresh = await api<CI>(request, refreshedToken, 'POST', '/cis', {
    name: `after-refresh-${run}`,
    ci_type_id: state().serverTypeId,
    status: 'active',
    discovery_source: 'manual',
  });
  expect(viaRefresh.status, JSON.stringify(viaRefresh.body)).toBe(201);
  await context.close();

  // A new sign-in carries the engineer rights; the UI now saves an edit of
  // the CI that the viewer could not change (test 08): UI → API → DB → UI.
  const ci = await ensureCI(request);
  const again = await signIn(browser, viewer);
  const perms = await permissionsOf(request, again.token);
  expect(perms).toEqual(expect.arrayContaining(['ci:write', 'ci:delete', 'collector:manage']));
  expect(perms).not.toContain('user:manage');
  const renamed = `${ci.name}-by-engineer`;
  await again.page.goto(`/cmdb/${ci.id}`);
  await again.page.getByRole('button', { name: 'Bearbeiten' }).first().click();
  const dialog = again.page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(renamed);
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  await expect(dialog).toBeHidden();
  await again.page.reload();
  await expect(again.page.getByRole('heading', { name: renamed })).toBeVisible();

  // CI-06, E-78 (WP-475, retest F-094): the engineer (no audit:read) reads
  // the history of the changed CI: the API answers 200 with the rename and
  // without audit entries, the UI lists the rename without an error and
  // says that audit entries need audit:read.
  expect(perms).not.toContain('audit:read');
  const hist = await api<{
    data: { field_name?: string; source?: string }[];
    audit_included?: boolean;
  }>(request, again.token, 'GET', `/history/ci/${ci.id}`);
  expect(hist.status, `GET /history/ci/{id} as engineer: ${JSON.stringify(hist.body)}`).toBe(200);
  expect(hist.body.audit_included).toBe(false);
  expect(hist.body.data.some((e) => e.source === 'ci_change' && e.field_name === 'name')).toBe(
    true,
  );
  expect(hist.body.data.some((e) => e.source === 'audit')).toBe(false);
  await again.page.locator('#main-content summary', { hasText: 'Historie' }).click();
  const history = again.page.getByRole('list', { name: 'Historie' });
  await expect(history).toBeVisible();
  await expect(history.getByRole('listitem').filter({ hasText: /·\s*name\b/ })).not.toHaveCount(0);
  await expect(again.page.getByText('Die Historie konnte nicht geladen werden.')).toHaveCount(0);
  await expect(
    again.page.getByText(
      'Audit-Einträge sind nicht enthalten; sie erfordern die Berechtigung audit:read.',
    ),
  ).toBeVisible();
  await again.context.close();
  const stored = await adminCall<CI>(request, 'GET', `/cis/${ci.id}`);
  expect(stored.body.name).toBe(renamed);
  saveState({ ci: { id: ci.id, name: renamed, version: stored.body.version } });
});

test('12 after the upgrade the user creates a CI through the UI form (UI → API → DB → UI)', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const roles = state().roles!;
  if (!(await assignmentsOf(request, viewer.userId)).some((a) => a.role_id === roles.engineer)) {
    const assigned = await adminCall(request, 'POST', '/role-assignments', {
      user_id: viewer.userId,
      role_id: roles.engineer,
    });
    expect(assigned.status).toBe(201);
  }
  const { context, page } = await signIn(browser, viewer);
  const uiName = `engineer-ui-ci-${run}`;
  await page.goto('/cmdb');
  await page.getByRole('button', { name: 'CI erstellen' }).first().click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(uiName);
  await dialog.getByLabel('CI-Typ').selectOption({ label: 'Server' });
  const post = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/cis') && r.request().method() === 'POST',
  );
  await dialog.getByRole('button', { name: 'Anlegen' }).click();
  const response = await post;
  expect(
    response.status(),
    `POST /cis from the form (${response.request().postData()}) → ${await response.text()}`,
  ).toBe(201);
  await expect(dialog).toBeHidden();
  await page.reload();
  await expect(page.getByRole('link', { name: uiName })).toBeVisible();
  await context.close();
  const stored = await adminCall<{ total: number }>(request, 'GET', `/cis?search=${uiName}`);
  expect(stored.body.total).toBe(1);
});

test('13 an admin can revoke the role again and the user loses the rights at the next sign-in', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const roles = state().roles!;
  let assignments = await assignmentsOf(request, viewer.userId);
  let engineer = assignments.find((a) => a.role_id === roles.engineer);
  if (!engineer) {
    const assigned = await adminCall<RoleAssignment>(request, 'POST', '/role-assignments', {
      user_id: viewer.userId,
      role_id: roles.engineer,
    });
    expect(assigned.status).toBe(201);
    assignments = await assignmentsOf(request, viewer.userId);
    engineer = assignments.find((a) => a.role_id === roles.engineer);
  }
  expect(engineer?.id, 'the engineer assignment has an id').toBeTruthy();

  // API-05 resource role-assignments: removing an assignment is a role change (AUT-02).
  const removed = await adminCall(request, 'DELETE', `/role-assignments/${engineer!.id}`);
  expect(
    [200, 204],
    `DELETE /role-assignments/{id} → ${removed.status} ${JSON.stringify(removed.body)}`,
  ).toContain(removed.status);
  expect((await assignmentsOf(request, viewer.userId)).map((a) => a.role_id)).toEqual([
    roles.viewer,
  ]);
  const { context, token } = await signIn(browser, viewer);
  const perms = await permissionsOf(request, token);
  expect(perms).not.toContain('ci:write');
  const denied = await api(request, token, 'POST', '/cis', {
    name: `post-revoke-${run}`,
    ci_type_id: state().serverTypeId,
  });
  expect(denied.status).toBe(403);
  await context.close();
});

test('14 the SPA renews an expired access token with the refresh cookie instead of signing the user out', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const ci = await ensureCI(request);
  const { context, page } = await signIn(browser, viewer);
  await page.goto('/cmdb');
  await expect(page.getByRole('link', { name: ci.name })).toBeVisible();

  // The access token becomes unusable (as after its 15 minutes): the server
  // answers 401 and the SPA (src/api/client.ts) calls refreshSession().
  await page.evaluate(() => {
    const raw = window.sessionStorage.getItem('reticora-auth-session')!;
    const session = JSON.parse(raw) as { token: string };
    session.token = `${session.token.slice(0, -6)}AAAAAA`;
    window.sessionStorage.setItem('reticora-auth-session', JSON.stringify(session));
  });
  const refresh = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/auth/refresh') && r.request().method() === 'POST',
  );
  await page.reload();
  const response = await refresh;
  // AUT-02 / WP-066: rotating HttpOnly refresh cookie, used by the frontend.
  expect(
    response.status(),
    `SPA refresh (Content-Type: ${response.request().headers()['content-type'] ?? 'none'}) → ${await response.text()}`,
  ).toBe(200);
  await expect(page).toHaveURL(/\/cmdb/);
  await expect(page.getByRole('link', { name: ci.name })).toBeVisible();
  await context.close();
});

test('14a the cookie-only refresh (no body, no Content-Type) renews the session and rotates the cookie', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const { context, page } = await signIn(browser, viewer);
  const before = (await context.cookies()).find((c) => c.name === 'reticora_refresh');
  expect(before?.value, 'the sign-in sets the HttpOnly refresh cookie').toBeTruthy();

  // Exactly what the SPA sends (src/auth/session.ts): only the cookie.
  const res = await page.request.post(`${BASE}/api/v1/auth/refresh`);
  expect(res.headers()['content-type'] ?? '', 'response type').toContain('application/json');
  expect(
    res.status(),
    `cookie-only POST /auth/refresh (WP-364, before the fix 400 from the validator) → ${await res.text()}`,
  ).toBe(200);
  const body = (await res.json()) as { token: string };
  expect(body.token, 'a new access token').toBeTruthy();
  const setCookie = res
    .headersArray()
    .filter((h) => h.name.toLowerCase() === 'set-cookie')
    .map((h) => h.value)
    .find((v) => v.startsWith('reticora_refresh='));
  expect(setCookie, 'Set-Cookie with the rotated refresh cookie').toBeTruthy();
  expect(setCookie).toMatch(/HttpOnly/i);
  const rotated = setCookie!.split(';')[0]!.slice('reticora_refresh='.length);
  expect(rotated, 'the refresh cookie is rotated').not.toBe(before!.value);
  const me = await api(request, body.token, 'GET', '/me');
  expect(me.status, 'GET /me with the renewed token').toBe(200);
  await context.close();
});

test('14b the SPA renews the session when the access token lifetime has run out instead of sending the user to the login', async ({
  browser,
  request,
}) => {
  const viewer = await ensureViewer(request);
  const ci = await ensureCI(request);
  const { context, page } = await signIn(browser, viewer);
  await page.goto('/cmdb');
  await expect(page.getByRole('link', { name: ci.name })).toBeVisible();

  const refreshes: number[] = [];
  page.on('response', (r) => {
    if (r.url().endsWith('/api/v1/auth/refresh') && r.request().method() === 'POST') {
      refreshes.push(r.status());
    }
  });
  // As after the 15 minutes of AUT-02: the expiry the SPA stored with the
  // session has passed. The HttpOnly refresh cookie is still valid, so the
  // SPA must renew the session with it (WP-066) rather than sign the user out.
  await page.evaluate(() => {
    const raw = window.sessionStorage.getItem('reticora-auth-session')!;
    const session = JSON.parse(raw) as { expiresAt: string };
    session.expiresAt = new Date(Date.now() - 60_000).toISOString();
    window.sessionStorage.setItem('reticora-auth-session', JSON.stringify(session));
  });
  await page.reload();
  await expect
    .poll(() => refreshes, {
      message:
        'the SPA renews the expired session with the refresh cookie (POST /auth/refresh → 200)',
      timeout: 15_000,
    })
    .toContain(200);
  await expect(page).not.toHaveURL(/\/login/);
  await expect(page.getByRole('link', { name: ci.name })).toBeVisible();
  await context.close();
});

/** keycloakAdmin calls the Keycloak admin API of the realm reticora. */
async function keycloakAdmin(
  request: Parameters<typeof api>[0],
  method: 'GET' | 'POST' | 'PUT',
  path: string,
  data?: unknown,
) {
  const token = await keycloakAdminToken(request);
  return request.fetch(`${KEYCLOAK}/admin/realms/reticora${path}`, {
    method,
    headers: { Authorization: `Bearer ${token}` },
    data,
    failOnStatusCode: false,
  });
}

/**
 * ensureKeycloakGroup returns the id of the top-level realm group `name`,
 * created through the admin API when it does not exist (the realm files
 * contain only reticora-admin and operators, deploy/keycloak/README.md).
 */
async function ensureKeycloakGroup(request: Parameters<typeof api>[0], name: string) {
  const find = async () => {
    const res = await keycloakAdmin(request, 'GET', `/groups?search=${encodeURIComponent(name)}`);
    expect(res.status(), `Keycloak list groups: ${await res.text()}`).toBe(200);
    return ((await res.json()) as { id: string; name: string }[]).find((g) => g.name === name)?.id;
  };
  const existing = await find();
  if (existing) return existing;
  const created = await keycloakAdmin(request, 'POST', '/groups', { name });
  expect(created.status(), `Keycloak create group ${name}: ${await created.text()}`).toBe(201);
  const id = await find();
  expect(id, `group ${name} after its creation`).toBeTruthy();
  return id!;
}

test('15 a member of the IdP group reticora-viewer created through the Keycloak admin API holds exactly the viewer rights', async ({
  browser,
  request,
}) => {
  // F-200 retest (RBA-02): the app rights come from the IdP group (and the
  // role assignments), never from Keycloak realm roles.
  const groupId = await ensureKeycloakGroup(request, 'reticora-viewer');
  const address = email('groupviewer');
  const pw = password('groupviewer');
  // AUT-01: the administrator creates the account in Reticora, without a
  // role; that admits the first sign-in of the Keycloak account.
  const created = await adminCall<User>(request, 'POST', '/users', {
    email: address,
    display_name: 'Audit Group Viewer',
  });
  expect(created.status, `create user: ${JSON.stringify(created.body)}`).toBe(201);
  const accountId = await createKeycloakUser(request, {
    email: address,
    password: pw,
    orgId: state().orgId!,
  });
  const member = await keycloakAdmin(request, 'PUT', `/users/${accountId}/groups/${groupId}`);
  expect(
    member.status(),
    `Keycloak add the account to reticora-viewer: ${await member.text()}`,
  ).toBe(204);

  // The RBA-02 viewer row as the tenant's database holds it.
  const roles = await adminCall<{ data: { name: string; permissions: string[] }[] }>(
    request,
    'GET',
    '/roles?limit=100',
  );
  expect(roles.status).toBe(200);
  const viewerRow = [
    ...(roles.body.data.find((r) => r.name === 'viewer')?.permissions ?? []),
  ].sort();
  expect(viewerRow, 'the viewer row of RBA-02').toEqual(expect.arrayContaining(VIEWER_REQUIRED));

  const exactlyViewer = async (bearer: string, label: string) => {
    const perms = [...(await permissionsOf(request, bearer))].sort();
    expect(perms, `${label}: GET /me/permissions equals the RBA-02 viewer row`).toEqual(viewerRow);
    expect((await api(request, bearer, 'GET', '/cis')).status, `${label}: GET /cis`).toBe(200);
    const write = await api(request, bearer, 'POST', '/cis', {
      name: `group-viewer-${run}`,
      ci_type_id: state().serverTypeId,
      status: 'active',
      discovery_source: 'manual',
    });
    expect(write.status, `${label}: POST /cis`).toBe(403);
  };

  const { context, page, token } = await signIn(browser, { email: address, password: pw });
  await exactlyViewer(token, 'first sign-in');

  // The first sign-in also stores the default role of the installation
  // (RETICORA_DEFAULT_PROVISION_ROLE, viewer). Without any role assignment
  // the group alone grants the same rights at the next refresh.
  const me = await api<{ sub: string }>(request, token, 'GET', '/me');
  expect(me.status).toBe(200);
  for (const assignment of await assignmentsOf(request, me.body.sub)) {
    const removed = await adminCall(request, 'DELETE', `/role-assignments/${assignment.id}`);
    expect([200, 204], `revoke ${assignment.id}: ${removed.status}`).toContain(removed.status);
  }
  expect(await assignmentsOf(request, me.body.sub), 'no role assignment left').toEqual([]);
  await exactlyViewer(await cookieRefresh(page), 'group only, no role assignment');
  await context.close();
});
