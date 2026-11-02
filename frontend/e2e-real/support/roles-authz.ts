import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, type APIRequestContext } from '@playwright/test';
import { KEYCLOAK, MAILPIT, keycloakAdminToken, type Account } from './stack';

// Helpers of roles-authz.spec.ts only (Phase-1 acceptance audit).

/**
 * RunState is what the tests of roles-authz.spec.ts share. Playwright starts
 * a new worker after a failed test, which re-evaluates the module; the state
 * is therefore kept in a file keyed by the test runner process, so a product
 * failure in one test does not force the next test to sign up a new tenant.
 */
export interface RunState {
  /** Epoch ms of the setup; a state older than the session lifetime is stale. */
  createdAt?: number;
  admin?: Account;
  adminToken?: string;
  adminUserId?: string;
  orgId?: string;
  roles?: Record<string, string>;
  /** Id of the CI type with the key `server` (CI types are referenced by id). */
  serverTypeId?: string;
  viewer?: { email: string; password: string; userId: string };
  ci?: { id: string; name: string; version: number };
}

const stateFile = join(tmpdir(), `reticora-roles-authz-${process.ppid}.json`);

export function loadState(): RunState {
  if (!existsSync(stateFile)) return {};
  try {
    const state = JSON.parse(readFileSync(stateFile, 'utf8')) as RunState;
    // Sessions last 15 minutes (AUT-02); an older file is from another run.
    return state.createdAt && Date.now() - state.createdAt < 12 * 60_000 ? state : {};
  } catch {
    return {};
  }
}

export function saveState(patch: Partial<RunState>): RunState {
  const next = { ...loadState(), ...patch };
  writeFileSync(stateFile, JSON.stringify(next));
  return next;
}

const DATABASE_URL =
  process.env.E2E_DATABASE_URL || 'postgres://reticora:reticora@localhost:55432/e2e';

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** databaseReachable reports whether psql can reach the stack's database. */
export function databaseReachable(): boolean {
  try {
    return psql('SELECT 1') === '1';
  } catch {
    return false;
  }
}

/** psql runs one statement against the stack's database and returns the rows. */
export function psql(sql: string, vars: Record<string, string> = {}): string {
  const args = [DATABASE_URL, '-X', '-At', '-v', 'ON_ERROR_STOP=1'];
  for (const [name, value] of Object.entries(vars)) {
    args.push('-v', `${name}=${value}`);
  }
  args.push('-f', '-');
  return execFileSync('psql', args, { input: sql, encoding: 'utf8', timeout: 15_000 }).trim();
}

/**
 * ageInvitation moves the expiry of ONE invitation of this spec's own tenant
 * into the past. No API can produce an expired invitation (the validity is
 * seven days), so this fixture simulates the passing of time for exactly the
 * row the test issued; nothing else is written.
 */
export function ageInvitation(orgId: string, invitationId: string) {
  expect(UUID.test(orgId) && UUID.test(invitationId), 'ids are UUIDs').toBe(true);
  const updated = psql(
    `UPDATE user_invitation SET expires_at = now() - interval '1 minute'
       WHERE organization_id = :'org' AND id = :'inv' AND accepted_at IS NULL RETURNING id;`,
    { org: orgId, inv: invitationId },
  );
  expect(updated.split('\n')[0], 'the aged invitation').toBe(invitationId);
}

/**
 * createKeycloakUser creates a realm user, without any invitation; without
 * orgId the user carries no organization attribute.
 */
export async function createKeycloakUser(
  request: APIRequestContext,
  user: { email: string; password: string; orgId?: string },
): Promise<string> {
  const token = await keycloakAdminToken(request);
  const res = await request.post(`${KEYCLOAK}/admin/realms/reticora/users`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      username: user.email,
      email: user.email,
      emailVerified: true,
      enabled: true,
      firstName: 'Audit',
      lastName: 'Uninvited',
      attributes: user.orgId ? { organization_id: [user.orgId] } : {},
      credentials: [{ type: 'password', value: user.password, temporary: false }],
    },
  });
  expect(res.status(), `Keycloak create user: ${await res.text()}`).toBe(201);
  const id = (res.headers()['location'] ?? '').split('/').pop() ?? '';
  expect(id).toMatch(UUID);
  // The attribute is what the backend reads (CH26); make sure the realm kept it.
  const read = await request.get(`${KEYCLOAK}/admin/realms/reticora/users/${id}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const body = (await read.json()) as { attributes?: Record<string, string[]> };
  expect(body.attributes?.organization_id, 'organization_id attribute in Keycloak').toEqual(
    user.orgId ? [user.orgId] : undefined,
  );
  return id;
}

export interface MailpitMessage {
  ID: string;
  Subject: string;
  Text?: string;
  HTML?: string;
}

/** mailTo waits up to timeoutMs for a message to `email`; null when none arrives. */
export async function mailTo(
  request: APIRequestContext,
  email: string,
  timeoutMs: number,
): Promise<MailpitMessage | null> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const search = await request.get(`${MAILPIT}/api/v1/search`, {
      params: { query: `to:"${email}"` },
    });
    if (search.ok()) {
      const found = (await search.json()) as { messages?: { ID: string }[] };
      const id = found.messages?.[0]?.ID;
      if (id) {
        return (await (
          await request.get(`${MAILPIT}/api/v1/message/${id}`)
        ).json()) as MailpitMessage;
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 2_000));
  }
  return null;
}
