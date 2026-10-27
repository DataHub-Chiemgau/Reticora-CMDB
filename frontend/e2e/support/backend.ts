import { basename } from 'node:path';
import { test as base, expect } from '@playwright/test';
import type { Page, Request, Route } from '@playwright/test';
import { contract } from './contract';

// Strict mocked backend of the browser flows (TST-03, WP-201; WP-530,
// F-178). The `test` of this module answers every /api/v1 request of the
// page from the handlers a spec installs with mockBackend (plus the shared
// DEFAULTS below); the specs keep their state in closures, so a write is
// visible to the next read like on a real server.
//
// Nothing passes silently:
// - a request no handler answers gets 501 and fails the test after it ran
//   (fixture teardown, i.e. in afterEach) with method and path, unless the
//   spec lists it in UNUSED below as a call it deliberately leaves
//   unanswered;
// - every request and every mocked response is checked against
//   api/openapi.yaml (support/contract.ts): an unspecified route or method,
//   a parameter, request body or response body outside its schema
//   (including `format: uuid`) or an undeclared status fails the test with
//   the path and the schema error. A request outside the contract is
//   refused like the server would (422 with `violations` where the
//   operation declares it, otherwise 400), e.g. a ci_type_id that is a type
//   key such as 'server' instead of a UUID (CI-03, WP-397).

export { expect };

export function list<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

export interface Reply {
  status?: number;
  body?: unknown;
  /** Defaults to the media type the operation declares for the status (JSON first). */
  contentType?: string;
}

export interface Handler {
  method?: string;
  /** Matched against the path below /api/v1 (without the query). */
  path: string | RegExp;
  reply: (request: Request, url: URL) => Reply;
}

export interface Call {
  method: string;
  /** The path below /api/v1. */
  path: string;
  search: string;
  body: unknown;
}

/** A call a spec deliberately leaves unanswered (501), with the reason. */
interface Unused {
  method: string;
  path: string | RegExp;
  why: string;
}

// UNUSED is the explicit allowlist per spec file of /api/v1 calls the page
// makes but the spec deliberately does not answer: the mock replies 501 and
// the test does not fail for them. The request itself is still checked
// against the contract. Everything else a page calls needs a handler.
const DETAIL_PANEL = 'panel of the CI detail page the spec does not look at';
const CI_DETAIL_PANELS: Unused[] = [
  { method: 'GET', path: /^\/cis\/[^/]+\/parent$/, why: DETAIL_PANEL },
  { method: 'GET', path: /^\/cis\/[^/]+\/relationships$/, why: DETAIL_PANEL },
  { method: 'GET', path: /^\/cis\/[^/]+\/field-definitions$/, why: DETAIL_PANEL },
  { method: 'GET', path: /^\/cis\/[^/]+\/provenance$/, why: DETAIL_PANEL },
  { method: 'GET', path: /^\/history\/ci\/[^/]+$/, why: DETAIL_PANEL },
  { method: 'GET', path: '/lifecycle-definitions', why: DETAIL_PANEL },
];

export const UNUSED: Record<string, Unused[]> = {
  'ci.spec.ts': CI_DETAIL_PANELS,
  'keyboard.spec.ts': CI_DETAIL_PANELS,
  // The provenance panel is the subject of conflict.spec.ts; it has a handler.
  'conflict.spec.ts': CI_DETAIL_PANELS.filter((u) => !String(u.path).includes('provenance')),
};

/** A known contract deviation of a page: the violation message it starts with, and why. */
interface Deviation {
  message: string;
  why: string;
}

// KNOWN_DEVIATIONS lists, per spec file, contract violations of the page
// itself that the server tolerates and that are reported as findings of
// their own (fixed outside WP-530). They are answered like a conforming
// request and do not fail the test; any other violation does.
export const KNOWN_DEVIATIONS: Record<string, Deviation[]> = {
  'login-roundtrip.spec.ts': [
    {
      message: 'GET /api/v1/collectors: query parameter limit: / must be <= 200',
      why:
        'DashboardPage asks for limit=1000; the server caps limit at 200 (parameter Limit), ' +
        'so the collector widget counts at most 200 collectors (finding of WP-530)',
    },
  ],
};

// Every product module is licensed in the mocked tenant, so the navigation
// and the command palette show all pages.
const FEATURES = [
  'ai',
  'compliance',
  'discovery',
  'documents',
  'endpoint_agent',
  'export_csv',
  'iga',
  'inventory',
  'monitoring',
  'rack_view',
  'stocktake',
  'ticketing',
  'topology',
  'webhooks',
  'workflow_forms',
];

export const ORG_ID = '00000000-0000-0000-0000-0000000000aa';

function ciType(id: string, key: string, de: string, en: string, system = true) {
  return {
    id,
    ...(system ? {} : { organization_id: ORG_ID }),
    key,
    name: key,
    display_name: { de, en },
    is_builtin: system,
    is_system: system,
    is_active: true,
    is_logical: false,
    is_observable: true,
    schema_version: 1,
    version: 1,
    capabilities: {},
    allowed_relationship_types: [],
    ui_schema: {},
    compliance_rules: [],
    discovery_mappings: [],
    fields: [],
    created_at: '2026-10-14T08:00:00Z',
    updated_at: '2026-10-14T08:00:00Z',
  };
}

/** The active CI types of GET /ci-types: system types and an organization type. */
export const CI_TYPES = [
  ciType('5e1b0c3a-7d2f-4a10-9c01-000000000001', 'server', 'Server', 'Server'),
  ciType('5e1b0c3a-7d2f-4a10-9c01-000000000002', 'switch', 'Switch', 'Switch'),
  ciType('5e1b0c3a-7d2f-4a10-9c01-000000000003', 'ups', 'USV', 'UPS'),
  ciType('5e1b0c3a-7d2f-4a10-9c01-0000000000a1', 'audit_pdu', 'Org-PDU', 'Org PDU', false),
];

export const AT = '2026-10-14T08:00:00Z';

const ORGANIZATION = {
  id: ORG_ID,
  name: 'E2E Org',
  slug: 'e2e-org',
  plan: 'enterprise',
  settings: {},
  timezone: 'Europe/Berlin',
  currency: 'EUR',
  locale: 'de-DE',
  ai_opt_in: false,
  created_at: AT,
  updated_at: AT,
};

// DEFAULTS answer the reads of the application shell (organization, scope
// selection, navigation, notifications) and the CI types on every page.
const DEFAULTS: Handler[] = [
  { path: '/org', reply: () => ({ body: ORGANIZATION }) },
  { path: '/clients', reply: () => ({ body: list([]) }) },
  { path: '/sites', reply: () => ({ body: list([]) }) },
  { path: '/notifications', reply: () => ({ body: list([]) }) },
  // No late module is switched on (API-06, WP-214).
  { path: '/modules', reply: () => ({ body: { data: [] } }) },
  {
    path: '/entitlements',
    reply: () => ({
      body: list(
        FEATURES.map((feature_key) => ({
          organization_id: ORG_ID,
          feature_key,
          plan: 'enterprise',
          enabled: true,
          limits: {},
          source: 'manual',
        })),
      ),
    }),
  },
  { path: '/ci-types', reply: () => ({ body: list(CI_TYPES) }) },
  {
    path: /^\/ci-types\/[0-9a-f-]{36}$/,
    reply: (_request, url) => {
      const type = CI_TYPES.find((t) => url.pathname.endsWith(`/ci-types/${t.id}`));
      return type ? { body: type } : { status: 404, body: problem(404, 'ci type not found') };
    },
  },
];

/** problem is an RFC 7807 body as the server sends it (API-03). */
export function problem(status: number, detail: string, extra: Record<string, unknown> = {}) {
  const titles: Record<number, string> = {
    400: 'Bad Request',
    401: 'Unauthorized',
    403: 'Forbidden',
    404: 'Not Found',
    405: 'Method Not Allowed',
    409: 'Conflict',
    422: 'Unprocessable Entity',
    500: 'Internal Server Error',
    501: 'Not Implemented',
    503: 'Service Unavailable',
  };
  return {
    type: `https://reticora.io/problems/${status}`,
    title: titles[status] ?? 'Error',
    status,
    detail,
    trace_id: 'e2e-mock',
    ...extra,
  };
}

interface State {
  spec: string;
  handlers: Handler[];
  calls: Call[];
  failures: string[];
}

const states = new WeakMap<Page, State>();

function matches(entry: { method?: string; path: string | RegExp }, method: string, path: string) {
  return (
    (entry.method ?? 'GET') === method &&
    (typeof entry.path === 'string' ? entry.path === path : entry.path.test(path))
  );
}

async function fulfillProblem(route: Route, status: number, body: unknown) {
  await route.fulfill({
    status,
    contentType: 'application/problem+json',
    body: JSON.stringify(body),
  });
}

/** serve answers one /api/v1 request of the page and records what is wrong with it. */
async function serve(route: Route, state: State) {
  const request = route.request();
  const url = new URL(request.url());
  const fullPath = url.pathname;
  const path = fullPath.replace(/^\/api\/v1/, '');
  const method = request.method();
  const raw = request.postData();
  let body: unknown;
  if (raw !== null && raw !== '') {
    try {
      body = JSON.parse(raw);
    } catch {
      body = raw;
    }
  }
  state.calls.push({ method, path, search: url.search, body });

  const spec = contract();
  const known = KNOWN_DEVIATIONS[state.spec] ?? [];
  const violations = spec
    .checkRequest({ method, path: fullPath, query: url.searchParams, body })
    .filter((v) => !known.some((k) => v.message.startsWith(k.message)));
  if (violations.length > 0) {
    state.failures.push(...violations.map((v) => `contract: ${v.message}`));
    const found = spec.operation(method, fullPath);
    if ('error' in found) {
      const status = found.error.includes('method not specified') ? 405 : 404;
      await fulfillProblem(route, status, problem(status, found.error));
      return;
    }
    const status = spec.response(method, fullPath, 422) ? 422 : 400;
    const detail = violations.map((v) => `${v.field}: ${v.message}`).join('; ');
    await fulfillProblem(
      route,
      status,
      problem(status, detail, {
        violations: violations.map((v) => ({ field: v.field, detail: v.message })),
      }),
    );
    return;
  }

  const handler = [...state.handlers, ...DEFAULTS].find((h) => matches(h, method, path));
  if (!handler) {
    if (!(UNUSED[state.spec] ?? []).some((u) => matches(u, method, path))) {
      state.failures.push(
        `unhandled: ${method} ${fullPath}${url.search} (no handler in ${state.spec}, not in UNUSED['${state.spec}'] of e2e/support/backend.ts)`,
      );
    }
    await fulfillProblem(route, 501, problem(501, `no mock for ${method} ${fullPath}`));
    return;
  }

  const reply = handler.reply(request, url);
  const status = reply.status ?? 200;
  const contentType =
    reply.contentType ?? spec.mediaType(method, fullPath, status) ?? 'application/json';
  state.failures.push(
    ...spec
      .checkResponse(
        { method, path: fullPath },
        {
          status,
          contentType: reply.body === undefined ? undefined : contentType,
          body: reply.body,
        },
      )
      .map((message) => `contract: ${message}`),
  );
  if (reply.body === undefined) {
    await route.fulfill({ status });
    return;
  }
  const text =
    typeof reply.body === 'string' && !/json/.test(contentType)
      ? reply.body
      : JSON.stringify(reply.body);
  await route.fulfill({ status, contentType, body: text });
}

/**
 * test is Playwright's test with the strict mocked backend: every /api/v1
 * request of the page goes through serve, and the test fails after it ran
 * when a call was unhandled or broke the contract.
 */
export const test = base.extend<{ strictBackend: void }>({
  strictBackend: [
    async ({ page }, use, testInfo) => {
      const state: State = {
        spec: basename(testInfo.file),
        handlers: [],
        calls: [],
        failures: [],
      };
      states.set(page, state);
      await page.route('**/api/v1/**', (route) => serve(route, state));
      await use();
      if (state.failures.length > 0) {
        throw new Error(
          `mocked backend (e2e/support/backend.ts): ${state.failures.length} unhandled call(s) or contract violation(s):\n` +
            state.failures.map((f) => `  - ${f}`).join('\n'),
        );
      }
    },
    { auto: true },
  ],
});

// LENIENT_SPECS run with their own mock data outside the contract check:
// a11y.spec.ts (WP-522, support/a11y-fixtures.ts) is not part of WP-530.
// For them an unhandled call is answered with an empty list (GET) or an
// empty object, as before WP-530. Every other spec uses the strict `test`.
export const LENIENT_SPECS = ['a11y.spec.ts'];

export const EMPTY_LIST = { data: [], items: [], total: 0, limit: 50, offset: 0, has_more: false };

async function lenient(page: Page, handlers: Handler[]) {
  const calls: Call[] = [];
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^\/api\/v1/, '');
    const method = request.method();
    let body: unknown;
    try {
      body = request.postDataJSON();
    } catch {
      body = request.postData();
    }
    calls.push({ method, path, search: url.search, body });
    const handler = [...handlers, ...DEFAULTS].find((h) => matches(h, method, path));
    const reply = handler
      ? handler.reply(request, url)
      : { body: method === 'GET' ? EMPTY_LIST : {} };
    if (reply.status === 204) {
      await route.fulfill({ status: 204 });
      return;
    }
    await route.fulfill({
      status: reply.status ?? 200,
      contentType: 'application/json',
      body: JSON.stringify(reply.body ?? {}),
    });
  });
  return calls;
}

/** mockBackend installs the handlers of a spec; calls records every API request. */
export async function mockBackend(page: Page, handlers: Handler[]) {
  const state = states.get(page);
  if (!state) {
    const spec = basename(base.info().file);
    if (LENIENT_SPECS.includes(spec)) {
      return lenient(page, handlers);
    }
    throw new Error(
      `${spec}: mockBackend needs the test of e2e/support/backend.ts (strict mock with contract check)`,
    );
  }
  state.handlers.push(...handlers);
  return state.calls;
}

/**
 * takeFailures returns the unhandled calls and contract violations recorded
 * so far and clears them; only the self-test of the contract check uses it.
 */
export function takeFailures(page: Page): string[] {
  const state = states.get(page);
  return state ? state.failures.splice(0) : [];
}

/** signIn stores a session so the protected pages render. */
export async function signIn(page: Page) {
  await page.addInitScript(() => {
    window.sessionStorage.setItem(
      'reticora-auth-session',
      JSON.stringify({
        token: 'header.payload.signature',
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        user: {
          sub: '00000000-0000-0000-0000-000000000001',
          name: 'Admin',
          org_id: '00000000-0000-0000-0000-0000000000aa',
          permissions: [],
        },
      }),
    );
  });
}
