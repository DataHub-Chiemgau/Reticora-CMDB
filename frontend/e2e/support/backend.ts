import type { Page, Request } from '@playwright/test';

// Mocked backend of the browser flows (TST-03, WP-201): one route on
// /api/v1/** answers from the handlers of a spec; everything else is an empty
// list (GET) or an empty object. The specs keep their own state in closures,
// so a write is visible to the next read like on a real server.

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
const DEFAULTS: Handler[] = [
  {
    path: '/entitlements',
    reply: () => ({ body: list(FEATURES.map((feature_key) => ({ feature_key, enabled: true }))) }),
  },
];

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
