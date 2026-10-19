import { execFileSync } from 'node:child_process';
import { expect, type APIRequestContext, type Browser, type Page } from '@playwright/test';
import { api, BASE, signupTenant, uiLogin, type Account } from './stack';

// Helpers of locations-racks.spec.ts only (Phase-1 acceptance audit): a tenant
// of its own, setup calls through the REST API the SPA uses, read-only probes
// of the stack database and locators of the location tree page.

export interface Tenant {
  page: Page;
  account: Account;
  token: string;
  orgId: string;
  userId: string;
}

export interface Location {
  id: string;
  organization_id: string;
  client_id: string;
  site_id: string;
  parent_id?: string;
  kind: string;
  name: string;
  path: string;
}

export interface Rack {
  id: string;
  organization_id: string;
  room_id: string;
  name: string;
  height_units: number;
  width_mm: number;
  depth_mm: number;
}

export interface RackMount {
  id: string;
  rack_id: string;
  ci_id: string;
  position_u: number;
  height_u: number;
  face: string;
}

export interface RackLayout {
  rack: Rack;
  mounts: RackMount[];
  free: Record<string, number[]>;
}

export interface CI {
  id: string;
  organization_id: string;
  name: string;
  client_id?: string;
  location_id?: string;
  site_id?: string;
  room_id?: string;
  parent_ci_id?: string;
  version?: number;
}

export interface List<T> {
  data: T[] | null;
  total?: number;
}

export interface Problem {
  status?: number;
  title?: string;
  detail?: string;
  violations?: { field: string; detail: string }[];
  dependencies?: string[];
}

const DATABASE_URL =
  process.env.E2E_DATABASE_URL || 'postgres://reticora:reticora@localhost:55432/e2e';

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** uuid returns id after checking it is a UUID, so that it can sit in SQL text. */
export function uuid(id: string | undefined): string {
  expect(UUID.test(id ?? ''), `UUID expected, got ${id}`).toBe(true);
  return id!;
}

/**
 * dbRows runs ONE read-only query against the stack database and returns its
 * rows as JSON objects. Only SELECT statements are accepted.
 */
export function dbRows<T = Record<string, unknown>>(select: string): T[] {
  expect(/^\s*select\b/i.test(select), 'read-only probe').toBe(true);
  const out = execFileSync(
    'psql',
    [DATABASE_URL, '-X', '-At', '-v', 'ON_ERROR_STOP=1', '-f', '-'],
    {
      input: `SELECT COALESCE(json_agg(q), '[]'::json) FROM (${select}) q;`,
      encoding: 'utf8',
      timeout: 15_000,
    },
  ).trim();
  return JSON.parse(out) as T[];
}

function claims(token: string): { sub: string; org: string } {
  return JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString()) as {
    sub: string;
    org: string;
  };
}

/**
 * openTenant signs up a new organization in its own browser context and signs
 * its administrator in. Each signup uses its own documentation address
 * (198.18.0.0/15) as X-Real-IP, as the signup budget is counted per client
 * address (E-33).
 */
export async function openTenant(
  browser: Browser,
  rq: APIRequestContext,
  label: string,
): Promise<Tenant> {
  const realIp = `198.19.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': realIp },
  });
  const page = await ctx.newPage();
  const account = await signupTenant(page, rq, label);
  const token = await uiLogin(page, account);
  const c = claims(token);
  return { page, account, token, orgId: c.org, userId: c.sub };
}

/** ok asserts a 2xx answer of a setup call and returns its body. */
export async function ok<T>(
  rq: APIRequestContext,
  token: string,
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
): Promise<T> {
  const res = await api<T>(rq, token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

export function rows<T>(body: List<T> | null | undefined): T[] {
  return body?.data ?? [];
}

/** label is the ltree label of a location id (path segments are ids with "_"). */
export function label(id: string): string {
  return id.replace(/-/g, '_');
}

/** row is the line of a node in the location tree page. */
export function treeRow(page: Page, name: string) {
  return page
    .locator('#main-content li > div')
    .filter({ has: page.getByText(name, { exact: true }) });
}

/** childRow is the line of `child` directly below the node `parent` in the tree page. */
export function childRow(page: Page, parent: string, child: string) {
  return treeRow(page, parent)
    .locator('xpath=following-sibling::ul')
    .locator(':scope > li > div')
    .filter({ has: page.getByText(child, { exact: true }) });
}

/** waitForApi waits for the SPA's call of an API path fragment. */
export function waitForApi(page: Page, fragment: string, method = 'GET') {
  return page.waitForResponse(
    (r) => r.url().includes(`/api/v1${fragment}`) && r.request().method() === method,
  );
}
