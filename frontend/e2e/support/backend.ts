import type { Page, Request } from '@playwright/test';

// Mocked backend of the browser flows (TST-03, WP-201): one route on
// /api/v1/** answers from the handlers of a spec; everything else is an empty
// list (GET) or an empty object. The specs keep their own state in closures,
// so a write is visible to the next read like on a real server. Like the
// server, the mock refuses a ci_type_id that is no UUID (a type key such as
// 'server') with 422 (CI-03, WP-396/WP-397), so a form that posts a key fails
// here as well.

export const EMPTY_LIST = { data: [], items: [], total: 0, limit: 50, offset: 0, has_more: false };

export function list<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

export interface Reply {
  status?: number;
  body?: unknown;
}

export interface Handler {
  method?: string;
  /** Matched against the path below /api/v1 (without the query). */
  path: string | RegExp;
  reply: (request: Request, url: URL) => Reply;
}

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

function ciType(id: string, key: string, de: string, en: string, system = true) {
  return {
    id,
    organization_id: system ? undefined : '00000000-0000-0000-0000-0000000000aa',
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

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const DEFAULTS: Handler[] = [
  {
    path: '/entitlements',
    reply: () => ({ body: list(FEATURES.map((feature_key) => ({ feature_key, enabled: true }))) }),
  },
  { path: '/ci-types', reply: () => ({ body: list(CI_TYPES) }) },
  {
    path: /^\/ci-types\/[0-9a-f-]{36}$/,
    reply: (_request, url) => {
      const type = CI_TYPES.find((t) => url.pathname.endsWith(`/ci-types/${t.id}`));
      return type
        ? { body: type }
        : { status: 404, body: { status: 404, detail: 'ci type not found' } };
    },
  },
];

/** typeRefusal is the server's 422 for a ci_type_id that is no UUID, or undefined. */
function typeRefusal(method: string, path: string, body: unknown): Reply | undefined {
  if (method !== 'POST' || !(path === '/cis' || /^\/cis\/[^/]+\/type$/.test(path))) {
    return undefined;
  }
  const id = (body as { ci_type_id?: unknown } | null)?.ci_type_id;
  if (typeof id === 'string' && UUID.test(id)) {
    return undefined;
  }
  const detail = 'not the ID (UUID) of a CI type';
  return {
    status: 422,
    body: {
      type: 'https://reticora.io/problems/422',
      title: 'Unprocessable Entity',
      status: 422,
      detail: `ci_type_id: ${detail}`,
      violations: [{ field: 'ci_type_id', detail }],
    },
  };
}

/** mockBackend installs the handlers; calls records every API request. */
export async function mockBackend(page: Page, handlers: Handler[]) {
  const calls: { method: string; path: string; search: string; body: unknown }[] = [];
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
    const refused = typeRefusal(method, path, body);
    if (refused) {
      await route.fulfill({
        status: refused.status,
        contentType: 'application/problem+json',
        body: JSON.stringify(refused.body),
      });
      return;
    }
    const handler = [...handlers, ...DEFAULTS].find(
      (h) =>
        (h.method ?? 'GET') === method &&
        (typeof h.path === 'string' ? h.path === path : h.path.test(path)),
    );
    if (!handler) {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(method === 'GET' ? EMPTY_LIST : {}),
      });
      return;
    }
    const reply = handler.reply(request, url);
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
