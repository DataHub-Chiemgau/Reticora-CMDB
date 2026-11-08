import { afterEach, describe, expect, it, vi } from 'vitest';
import spec from './generated/schema.d.ts?raw';
import clientSource from './client.ts?raw';

const futureExpiry = '2999-01-01T00:00:00Z';

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

async function importClientWithSession(token = 'old-token') {
  vi.resetModules();
  const { useAuthStore } = await import('../auth/authStore');
  useAuthStore.getState().setSession({
    token,
    expiresAt: futureExpiry,
    user: { sub: 'user-1', email: 'user@example.com' },
  });
  const { fetchAPI } = await import('./client');
  return { fetchAPI, useAuthStore };
}

afterEach(() => {
  vi.useRealTimers();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.resetModules();
});

describe('fetchAPI authentication', () => {
  it('attaches the bearer token when a session exists', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ ok: true }));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI } = await importClientWithSession('attached-token');

    await expect(fetchAPI('/cis')).resolves.toEqual({ ok: true });

    const headers = fetchMock.mock.calls[0]?.[1]?.headers as Headers;
    expect(headers.get('Authorization')).toBe(['Bearer', 'attached-token'].join(' '));
  });

  it('refreshes once on 401 and retries the original request', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ detail: 'expired' }, 401))
      .mockResolvedValueOnce(jsonResponse({ token: 'new-token', expires_at: futureExpiry }))
      .mockResolvedValueOnce(jsonResponse({ data: ['ok'] }));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI, useAuthStore } = await importClientWithSession('old-token');

    await expect(fetchAPI('/cis')).resolves.toEqual({ data: ['ok'] });

    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/auth/refresh');
    // The refresh token travels only in the HttpOnly cookie (AUT-02): no
    // token in the body, cookies sent for the same origin.
    expect(fetchMock.mock.calls[1]?.[1]?.body).toBeUndefined();
    expect(fetchMock.mock.calls[1]?.[1]?.credentials).toBe('same-origin');
    const retryHeaders = fetchMock.mock.calls[2]?.[1]?.headers as Headers;
    expect(retryHeaders.get('Authorization')).toBe(['Bearer', 'new-token'].join(' '));
    expect(useAuthStore.getState().token).toBe('new-token');
  });

  it('reports the 401 but keeps the session when the refresh is rate limited twice (429)', async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ detail: 'expired' }, 401))
      .mockResolvedValueOnce(jsonResponse({ detail: 'too many' }, 429))
      .mockResolvedValueOnce(jsonResponse({ detail: 'too many' }, 429));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI, useAuthStore } = await importClientWithSession('old-token');

    const result = fetchAPI('/cis').catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(60_000);
    const error = await result;

    expect(error).toMatchObject({ name: 'ApiError', status: 401 });
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls[2]?.[0]).toBe('/api/v1/auth/refresh');
    expect(useAuthStore.getState().token).toBe('old-token');
    expect(useAuthStore.getState().user?.sub).toBe('user-1');
  });

  it('ends the session when the server refuses the refresh cookie', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ detail: 'expired' }, 401))
      .mockResolvedValueOnce(jsonResponse({ detail: 'refresh token already used' }, 401));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI, useAuthStore } = await importClientWithSession('old-token');

    await expect(fetchAPI('/cis')).rejects.toMatchObject({ status: 401 });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(useAuthStore.getState().token).toBeNull();
    expect(useAuthStore.getState().user).toBeNull();
  });
});

// WP-813: fetchBlob loads a binary answer (the PDF label of an asset) on
// the session path of fetchResponse.
describe('fetchBlob', () => {
  async function importFetchBlob(token: string) {
    await importClientWithSession(token);
    const { fetchBlob, ApiError } = await import('./client');
    const { useAuthStore } = await import('../auth/authStore');
    return { fetchBlob, ApiError, useAuthStore };
  }

  const pdf = () =>
    new Response('%PDF-1.7', { status: 200, headers: { 'Content-Type': 'application/pdf' } });

  it('sets the bearer token and returns a Blob for a 2xx answer', async () => {
    const fetchMock = vi.fn().mockResolvedValue(pdf());
    vi.stubGlobal('fetch', fetchMock);
    const { fetchBlob } = await importFetchBlob('blob-token');

    const blob = await fetchBlob('/assets/a1/label?format=pdf&template=asset_50x25');

    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      '/api/v1/assets/a1/label?format=pdf&template=asset_50x25',
    );
    const headers = fetchMock.mock.calls[0]?.[1]?.headers as Headers;
    expect(headers.get('Authorization')).toBe(['Bearer', 'blob-token'].join(' '));
    expect(blob.type).toBe('application/pdf');
    expect(await blob.text()).toBe('%PDF-1.7');
  });

  it('refreshes once on 401 and repeats the request with the new token', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ detail: 'expired' }, 401))
      .mockResolvedValueOnce(jsonResponse({ token: 'new-token', expires_at: futureExpiry }))
      .mockResolvedValueOnce(pdf());
    vi.stubGlobal('fetch', fetchMock);
    const { fetchBlob } = await importFetchBlob('old-token');

    const blob = await fetchBlob('/assets/a1/label?format=pdf');

    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/auth/refresh');
    const retryHeaders = fetchMock.mock.calls[2]?.[1]?.headers as Headers;
    expect(retryHeaders.get('Authorization')).toBe(['Bearer', 'new-token'].join(' '));
    expect(blob.type).toBe('application/pdf');
  });

  it('throws ApiError with the detail of a failure', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        jsonResponse({ title: 'Service Unavailable', status: 503, detail: 'no blob storage' }, 503),
      );
    vi.stubGlobal('fetch', fetchMock);
    const { fetchBlob, ApiError } = await importFetchBlob('blob-token');

    const error = await fetchBlob('/assets/a1/label?format=pdf').catch((err: unknown) => err);

    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 503, detail: 'no blob storage' });
  });
});

describe('logout', () => {
  it('ends the server session with the access token and the cookie, then clears the local session', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);
    const { useAuthStore } = await importClientWithSession('live-token');
    const { logout } = await import('../auth/session');

    await logout();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/auth/logout');
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('same-origin');
    expect((init.headers as Record<string, string>).Authorization).toBe(
      ['Bearer', 'live-token'].join(' '),
    );
    expect(useAuthStore.getState().token).toBeNull();
  });

  it('clears the local session when the server is unreachable', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('offline')));
    const { useAuthStore } = await importClientWithSession('live-token');
    const { logout } = await import('../auth/session');

    await logout();

    expect(useAuthStore.getState().token).toBeNull();
  });
});

describe('ciApi.update optimistic concurrency', () => {
  it('sends the CI version as If-Match and surfaces a 409 conflict', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ id: 'ci-1', version: 4 }))
      .mockResolvedValueOnce(
        jsonResponse(
          {
            title: 'Conflict',
            detail: 'fields changed since the If-Match version: name',
            fields: ['name'],
          },
          409,
        ),
      )
      .mockResolvedValueOnce(jsonResponse({ id: 'ci-1', version: 5 }));
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession();
    const { ciApi, ApiError } = await import('./client');

    await expect(ciApi.update('ci-1', { name: 'a' }, 3)).resolves.toEqual({
      id: 'ci-1',
      version: 4,
    });
    expect((fetchMock.mock.calls[0]?.[1]?.headers as Headers).get('If-Match')).toBe('"3"');

    const conflict = await ciApi.update('ci-1', { name: 'b' }, 3).catch((err: unknown) => err);
    expect(conflict).toBeInstanceOf(ApiError);
    expect((conflict as InstanceType<typeof ApiError>).status).toBe(409);

    // Without a version no If-Match is sent.
    await ciApi.update('ci-1', { name: 'c' });
    expect((fetchMock.mock.calls[2]?.[1]?.headers as Headers).has('If-Match')).toBe(false);
  });
});

describe('bffQuery', () => {
  it('posts the query to /bff/graphql with the session and returns data', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ data: { currentUser: { id: 'user-1' } } }));
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession('bff-token');
    const { bffQuery } = await import('./client');

    await expect(bffQuery('{ currentUser { id } }')).resolves.toEqual({
      currentUser: { id: 'user-1' },
    });

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/bff/graphql');
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe('POST');
    const headers = fetchMock.mock.calls[0]?.[1]?.headers as Headers;
    expect(headers.get('Authorization')).toBe(['Bearer', 'bff-token'].join(' '));
  });

  it('throws the GraphQL errors', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({
          data: null,
          errors: [{ message: 'missing required permission: ci:read' }],
        }),
      ),
    );
    await importClientWithSession();
    const { bffQuery } = await import('./client');

    await expect(bffQuery('{ cis { totalCount } }')).rejects.toThrow('ci:read');
  });
});

describe('impact clients', () => {
  it('ask for the fields they read, never for the full answer with paths (NFR-02)', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ failed_ci_id: 'ci-1', impacted: [], count: 0 }));
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession();
    const { impactApi } = await import('./cmdb');

    await impactApi.impact('ci-1', { depth: 3 });

    const url = new URL(String(fetchMock.mock.calls[0]?.[0]), 'http://localhost');
    expect(url.pathname).toBe('/api/v1/impact/ci-1');
    expect(url.searchParams.get('depth')).toBe('3');
    expect(url.searchParams.get('fields')).toBe('name,ci_type,status');
  });
});

describe('complete lists over several pages (API-02)', () => {
  function permissions(from: number, count: number) {
    return Array.from({ length: count }, (_, i) => ({
      key: `res${from + i}:read`,
      resource: `res${from + i}`,
      action: 'read',
      description: '',
    }));
  }

  it('follows next_cursor with limit=200 until the end: two pages with 103 entries', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        jsonResponse({
          data: permissions(0, 100),
          total: 103,
          limit: 200,
          offset: 0,
          has_more: true,
          next_cursor: 'cursor-2',
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          data: permissions(100, 3),
          total: 103,
          limit: 200,
          offset: 100,
          has_more: false,
        }),
      );
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession();
    const { permissionApi } = await import('./client');

    const catalogue = await permissionApi.list();

    expect(catalogue).toHaveLength(103);
    expect(new Set(catalogue.map((p) => p.key)).size).toBe(103);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const first = new URL(String(fetchMock.mock.calls[0]?.[0]), 'http://localhost');
    expect(first.pathname).toBe('/api/v1/permissions');
    expect(first.searchParams.get('limit')).toBe('200');
    expect(first.searchParams.has('cursor')).toBe(false);
    const second = new URL(String(fetchMock.mock.calls[1]?.[0]), 'http://localhost');
    expect(second.searchParams.get('limit')).toBe('200');
    expect(second.searchParams.get('cursor')).toBe('cursor-2');
  });

  it('reads data of the role grants and team members', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      if (url.pathname === '/api/v1/roles/role-1/permissions') {
        return Promise.resolve(
          jsonResponse({
            data: [{ role_id: 'role-1', permission_key: 'ci:read' }],
            total: 1,
            limit: 200,
            offset: 0,
            has_more: false,
          }),
        );
      }
      return Promise.resolve(
        jsonResponse({ data: [], total: 0, limit: 200, offset: 0, has_more: false }),
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession();
    const { permissionApi, teamApi } = await import('./client');

    await expect(permissionApi.listRole('role-1')).resolves.toEqual([
      { role_id: 'role-1', permission_key: 'ci:read' },
    ]);
    await expect(teamApi.listMembers('team-1')).resolves.toEqual([]);
    const members = new URL(String(fetchMock.mock.calls[1]?.[0]), 'http://localhost');
    expect(members.pathname).toBe('/api/v1/teams/team-1/members');
    expect(members.searchParams.get('limit')).toBe('200');
  });

  it('reads data of the module lists and keeps the metric filters on every page', async () => {
    const page = (data: unknown[], extra: Record<string, unknown> = {}) =>
      jsonResponse({ data, total: data.length, limit: 200, offset: 0, has_more: false, ...extra });
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      switch (url.pathname) {
        case '/api/v1/monitoring/metrics':
          return Promise.resolve(
            url.searchParams.has('cursor')
              ? page([{ timestamp: '2026-01-01T00:01:00Z', value: 2 }])
              : page([{ timestamp: '2026-01-01T00:00:00Z', value: 1 }], {
                  has_more: true,
                  next_cursor: 'metrics-2',
                }),
          );
        case '/api/v1/monitoring/alerts':
          return Promise.resolve(page([{ id: 'rule-1', name: 'High CPU' }]));
        case '/api/v1/documents/doc-1/links':
          return Promise.resolve(page([{ id: 'link-1', entity_type: 'ci', entity_id: 'ci-1' }]));
        case '/api/v1/ai/conversations':
          return Promise.resolve(page([{ id: 'conv-1', title: 'Router' }]));
      }
      return Promise.resolve(jsonResponse({ detail: 'unexpected' }, 404));
    });
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession();
    const { monitoringApi, documentApi, aiApi } = await import('./client');

    const points = await monitoringApi.queryMetrics({ ci_id: 'ci-1', name: 'cpu', step: '5m' });
    expect(points.map((p) => p.value)).toEqual([1, 2]);
    for (const call of fetchMock.mock.calls.slice(0, 2)) {
      const url = new URL(String(call[0]), 'http://localhost');
      expect(url.searchParams.get('ci_id')).toBe('ci-1');
      expect(url.searchParams.get('name')).toBe('cpu');
      expect(url.searchParams.get('step')).toBe('5m');
      expect(url.searchParams.get('limit')).toBe('200');
    }
    const second = new URL(String(fetchMock.mock.calls[1]?.[0]), 'http://localhost');
    expect(second.searchParams.get('cursor')).toBe('metrics-2');

    await expect(monitoringApi.listAlerts()).resolves.toMatchObject([{ id: 'rule-1' }]);
    await expect(documentApi.getLinks('doc-1')).resolves.toMatchObject([{ id: 'link-1' }]);
    await expect(aiApi.conversations()).resolves.toMatchObject([{ id: 'conv-1' }]);
  });

  // F-362: /collectors and /sites page by offset only (no next_cursor) and
  // answer at most 200 rows per page; the complete list follows the offset.
  it.each([
    ['collectorApi', '/api/v1/collectors'],
    ['siteApi', '/api/v1/sites'],
  ] as const)(
    '%s.listAll follows the offset when the list has no cursor: 450 rows in three pages',
    async (api, path) => {
      const rows = Array.from({ length: 450 }, (_, i) => ({ id: `row-${i}`, name: `row ${i}` }));
      const fetchMock = vi.fn((input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname !== path) {
          return Promise.resolve(jsonResponse({ detail: 'unexpected' }, 404));
        }
        // The server caps the limit at 200 (parameter Limit) and sends no cursor.
        const limit = Math.min(Number(url.searchParams.get('limit') ?? 50), 200);
        const offset = Number(url.searchParams.get('offset') ?? 0);
        return Promise.resolve(
          jsonResponse({
            data: rows.slice(offset, offset + limit),
            total: rows.length,
            limit,
            offset,
            has_more: offset + limit < rows.length,
          }),
        );
      });
      vi.stubGlobal('fetch', fetchMock);
      await importClientWithSession();
      const client = await import('./client');

      const all = await client[api].listAll();

      expect(all).toHaveLength(450);
      expect(new Set(all.map((row) => row.id)).size).toBe(450);
      const offsets = fetchMock.mock.calls.map((call) => {
        const url = new URL(String(call[0]), 'http://localhost');
        expect(url.searchParams.get('limit')).toBe('200');
        return url.searchParams.get('offset') ?? '0';
      });
      expect(offsets).toEqual(['0', '200', '400']);
    },
  );

  // F-215: attribute definitions and invitations page at 200 with a keyset
  // cursor; the field editor and the user administration read every page.
  it.each([
    ['attribute definitions', '/api/v1/attribute-definitions'],
    ['invitations', '/api/v1/invitations'],
  ] as const)('the %s list follows next_cursor: 230 rows in two pages', async (_name, path) => {
    const rows = Array.from({ length: 230 }, (_, i) => ({ id: `row-${i}`, key: `k${i}` }));
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      if (url.pathname !== path) {
        return Promise.resolve(jsonResponse({ detail: 'unexpected' }, 404));
      }
      // The server answers 50 rows without limit and at most 200 (parameter Limit).
      const limit = Math.min(Number(url.searchParams.get('limit') ?? 50), 200);
      const start = url.searchParams.get('cursor') === 'after-200' ? 200 : 0;
      const end = Math.min(start + limit, rows.length);
      return Promise.resolve(
        jsonResponse({
          data: rows.slice(start, end),
          total: rows.length,
          limit,
          offset: 0,
          has_more: end < rows.length,
          ...(end < rows.length ? { next_cursor: `after-${end}` } : {}),
        }),
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession();
    const { invitationApi } = await import('./client');
    const { attributeDefinitionApi } = await import('./cmdb');

    const all =
      path === '/api/v1/invitations'
        ? await invitationApi.list()
        : (await attributeDefinitionApi.list({ ci_type_id: 'type-1', scope: 'type' })).data;

    expect(all).toHaveLength(230);
    expect(new Set(all.map((row) => row.id)).size).toBe(230);
    const urls = fetchMock.mock.calls.map((call) => new URL(String(call[0]), 'http://localhost'));
    expect(urls.map((url) => url.searchParams.get('limit'))).toEqual(['200', '200']);
    expect(urls.map((url) => url.searchParams.get('cursor'))).toEqual([null, 'after-200']);
    if (path === '/api/v1/attribute-definitions') {
      for (const url of urls) {
        expect(url.searchParams.get('ci_type_id')).toBe('type-1');
        expect(url.searchParams.get('scope')).toBe('type');
      }
    }
  });
});

// scopedListsOfSpec reads, from the types generated from api/openapi.yaml
// (src/api/generated/schema.d.ts, kept in sync by generate:api:check), every
// GET route with the query parameter client_id or site_id: path without
// /api/v1 → its scope parameters, sorted.
function scopedListsOfSpec(schema: string): Record<string, string[]> {
  const getOf = new Map<string, string>();
  const params = new Map<string, Set<string>>();
  let path = '';
  let operation = '';
  let inOperations = false;
  let inQuery = false;
  for (const line of schema.split('\n')) {
    if (/^export interface operations \{/.test(line)) {
      inOperations = true;
      continue;
    }
    if (!inOperations) {
      const p = /^ {2}'(\/api\/v1\/[^']+)': \{\s*$/.exec(line);
      if (p?.[1]) path = p[1];
      const g = /^ {4}get: operations\['([^']+)'\];/.exec(line);
      if (g?.[1] && path) getOf.set(path, g[1]);
      continue;
    }
    const o = /^ {2}([A-Za-z0-9_]+): \{\s*$/.exec(line);
    if (o?.[1]) {
      operation = o[1];
      inQuery = false;
      params.set(operation, new Set());
      continue;
    }
    if (/^ {6}query\??: \{\s*$/.test(line)) {
      inQuery = true;
      continue;
    }
    if (inQuery && /^ {6}\};/.test(line)) {
      inQuery = false;
      continue;
    }
    const k = inQuery ? /^ {8}(client_id|site_id)\??: /.exec(line) : null;
    if (k?.[1]) params.get(operation)?.add(k[1]);
  }
  const out: Record<string, string[]> = {};
  for (const [p, op] of getOf) {
    const keys = [...(params.get(op) ?? [])].sort();
    if (keys.length > 0) out[p.replace(/^\/api\/v1/, '')] = keys;
  }
  return out;
}

describe('scoped lists match the API (WP-556, UI-01/UI-02)', () => {
  it('reads the scope parameters of known routes from the generated schema', () => {
    // Guards the parser: a format change of the generator must not turn the
    // parity check below into a comparison of two empty sets.
    const fromSpec = scopedListsOfSpec(spec);
    expect(fromSpec['/cis']).toEqual(['client_id', 'site_id']);
    expect(fromSpec['/sites']).toEqual(['client_id']);
    expect(fromSpec['/clients']).toBeUndefined();
    expect(fromSpec['/cis/{id}']).toBeUndefined();
  });

  it('SCOPED_LISTS holds every GET list with client_id/site_id and nothing else', async () => {
    const { SCOPED_LISTS } = await import('./client');
    const fromClient = Object.fromEntries(
      Object.entries(SCOPED_LISTS).map(([p, keys]) => [p, [...keys].sort()]),
    );
    expect(fromClient).toEqual(scopedListsOfSpec(spec));
  });
});

describe('agentApi and the enrollment contract (WP-560, F-189, AGT-06)', () => {
  it('offers no enroll function without enrollment_token', async () => {
    const { agentApi } = await import('./client');
    // The agent enrolls itself (POST /agents/enroll) with a single-use
    // enrollment_token; the server answers 400 without it. The handwritten
    // client had an unused enroll without the token.
    expect(spec).toMatch(/AgentEnrollRequest: \{[^}]*\benrollment_token: string;/);
    expect(Object.keys(agentApi)).not.toContain('enroll');
    expect(clientSource).not.toMatch(/\/agents\/enroll['"`]/);
  });
});

// writeRoutesOfSpec reads every POST, PUT, PATCH and DELETE route of the
// generated schema as "METHOD /path" without /api/v1.
function writeRoutesOfSpec(schema: string): string[] {
  const out: string[] = [];
  let path = '';
  for (const line of schema.split('\n')) {
    if (/^export interface operations \{/.test(line)) break;
    const p = /^ {2}'\/api\/v1(\/[^']+)': \{\s*$/.exec(line);
    if (p?.[1]) path = p[1];
    const m = /^ {4}(post|put|patch|delete): operations\[/.exec(line);
    if (m?.[1] && path) out.push(`${m[1].toUpperCase()} ${path}`);
  }
  return out;
}

const DRAFT_ID = '7f1c2a9e-0b1d-4c55-9a51-3e0f2b6d8c11';

async function importClientWithDraft(active = true) {
  // The active draft lives in sessionStorage: a fresh store reads it again.
  if (!active) window.sessionStorage.removeItem('reticora-metamodel-draft');
  const imported = await importClientWithSession();
  const { useMetamodelDraftStore } = await import('../stores/metamodelDraft');
  if (active) useMetamodelDraftStore.getState().setDraft({ id: DRAFT_ID, name: 'Umbau' });
  const { fetchAPIVersioned, withDraft } = await import('./client');
  return { ...imported, fetchAPIVersioned, withDraft, useMetamodelDraftStore };
}

describe('metamodel draft parameter (WP-798, MET-53)', () => {
  const affected: [string, string][] = [
    ['POST', '/attribute-definitions'],
    ['PATCH', '/ci-types/t-1'],
    ['POST', '/ci-types/t-1/clone'],
    ['DELETE', '/ci-fields/global/serial'],
    ['POST', '/templates/tp-1/versions/2/publish'],
  ];
  const unaffected: [string, string][] = [
    ['GET', '/ci-types'],
    ['GET', '/attribute-definitions?scope=global'],
    ['PUT', '/cis/c-1/field-definitions'],
    ['POST', '/metamodel/drafts'],
    ['POST', `/metamodel/drafts/${DRAFT_ID}/items`],
    ['POST', '/templates/tp-1/migrations'],
    ['POST', '/attribute-definitions/ad-1/reindex'],
  ];

  it('adds draft={id} to the metamodel writes while a draft is active', async () => {
    const fetchMock = vi.fn().mockImplementation(async () => jsonResponse({ ok: true }));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI } = await importClientWithDraft();

    for (const [method, path] of affected) {
      await fetchAPI(path, { method, body: method === 'DELETE' ? undefined : '{}' });
    }
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual(
      affected.map(([, path]) => `/api/v1${path}?draft=${DRAFT_ID}`),
    );
  });

  it('leaves reads, instance fields, the draft routes and the job routes unchanged', async () => {
    const fetchMock = vi.fn().mockImplementation(async () => jsonResponse({ ok: true }));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI } = await importClientWithDraft();

    for (const [method, path] of unaffected) {
      await fetchAPI(path, method === 'GET' ? undefined : { method, body: '{}' });
    }
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual(
      unaffected.map(([, path]) => `/api/v1${path}`),
    );
  });

  it('keeps a draft the caller set and changes no URL without an active draft', async () => {
    const { withDraft } = await importClientWithDraft();
    expect(withDraft('/ci-types?draft=other', 'POST')).toBe('/ci-types?draft=other');
    expect(withDraft('/ci-types?x=1', 'POST')).toBe(`/ci-types?x=1&draft=${DRAFT_ID}`);

    expect(JSON.parse(window.sessionStorage.getItem('reticora-metamodel-draft') ?? '{}')).toEqual({
      id: DRAFT_ID,
      name: 'Umbau',
    });
    const inactive = await importClientWithDraft(false);
    for (const [method, path] of [...affected, ...unaffected]) {
      expect(inactive.withDraft(path, method)).toBe(path);
    }
  });

  it('adds the draft in fetchAPIVersioned as well', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ ok: true }));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPIVersioned } = await importClientWithDraft();
    await fetchAPIVersioned('/layouts/l-1', { method: 'PATCH', body: '{}' });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/layouts/l-1?draft=${DRAFT_ID}`);
  });

  it('raises itemRevision after a write captured in the draft (202), not after another 202', async () => {
    const item = {
      id: 'item-1',
      kind: 'ci_type',
      op: 'update',
      route: 'PATCH /api/v1/ci-types/{id}',
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(item, 202))
      .mockResolvedValueOnce(jsonResponse({ state: 'queued' }, 202))
      .mockResolvedValueOnce(jsonResponse({ id: 't-1' }, 200));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI, useMetamodelDraftStore } = await importClientWithDraft();
    const before = useMetamodelDraftStore.getState().itemRevision;

    await expect(fetchAPI('/ci-types/t-1', { method: 'PATCH', body: '{}' })).resolves.toEqual(item);
    expect(useMetamodelDraftStore.getState().itemRevision).toBe(before + 1);
    // The index job of reindex answers 202 without ?draft: no item.
    await fetchAPI('/attribute-definitions/ad-1/reindex', { method: 'POST', body: '{}' });
    await fetchAPI('/ci-types/t-1');
    expect(useMetamodelDraftStore.getState().itemRevision).toBe(before + 1);
  });

  it('follows the capturable routes of the API: every metamodel write but the two jobs', async () => {
    const { draftCapable, DRAFT_PREFIXES } = await import('./client');
    const writes = writeRoutesOfSpec(spec);
    // Guards the parser.
    expect(writes).toContain('POST /attribute-definitions');
    expect(writes).toContain('POST /templates/{id}/versions/{version}/publish');
    const under = (path: string) =>
      [...DRAFT_PREFIXES, '/ci-fields'].some((p) => path === p || path.startsWith(`${p}/`));
    const split = (route: string) => route.split(' ') as [string, string];
    const exempt = writes.filter((route) => {
      const [method, path] = split(route);
      return under(path) && !draftCapable(path, method);
    });
    expect(exempt.sort()).toEqual([
      'POST /attribute-definitions/{id}/reindex',
      'POST /templates/{id}/migrations',
    ]);
    const outside = writes.filter((route) => {
      const [method, path] = split(route);
      return !under(path) && draftCapable(path, method);
    });
    expect(outside).toEqual([]);
  });
});
