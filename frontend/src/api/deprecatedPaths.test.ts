// API paths the frontend uses (WP-168 API-05, API-01 catalogue paths;
// WP-468, F-077: the CI list export used the deprecated /api/v1/export/cis as
// a plain link, 401 in the browser). The OpenAPI document marks the old paths
// deprecated (sunset 2027-10-15) in favour of the catalogue paths; frontend
// code must not call a path whose every operation is deprecated, so no
// deprecated path can come back unnoticed.
//
// The deprecated operations are read from the types generated from
// api/openapi.yaml (src/api/generated/schema.d.ts, kept in sync by
// generate:api:check; Vite does not serve files outside frontend/); a use is
// an API path literal (after fetchAPI(, ${API_BASE} or /api/v1) in
// frontend/src (generated code and tests excluded) and frontend/e2e.
import { describe, expect, it } from 'vitest';
import spec from './generated/schema.d.ts?raw';

const sources = {
  ...import.meta.glob<string>('../**/*.{ts,tsx}', {
    query: '?raw',
    import: 'default',
    eager: true,
  }),
  ...import.meta.glob<string>('../../e2e/**/*.ts', {
    query: '?raw',
    import: 'default',
    eager: true,
  }),
};

// deprecatedPaths returns the paths (without /api/v1) whose every
// operation is marked @deprecated.
function deprecatedPaths(schema: string): string[] {
  const ops = new Map<string, { total: number; deprecated: number }>();
  let path = '';
  let marked = false;
  for (const line of schema.split('\n')) {
    const p = /^ {2}'(\/api\/v1\/[^']+)': \{\s*$/.exec(line);
    if (p?.[1]) {
      path = p[1];
      marked = false;
      ops.set(path, { total: 0, deprecated: 0 });
      continue;
    }
    if (!path) continue;
    const entry = ops.get(path);
    if (!entry) continue;
    if (/^ {5}\* @deprecated\s*$/.test(line)) marked = true;
    if (/^ {4}(get|put|post|delete|patch): operations\[/.test(line)) {
      entry.total += 1;
      if (marked) entry.deprecated += 1;
      marked = false;
    }
  }
  return [...ops.entries()]
    .filter(([, e]) => e.total > 0 && e.deprecated === e.total)
    .map(([p]) => p.replace(/^\/api\/v1/, ''));
}

function apiPathPattern(path: string): RegExp {
  const body = path
    .split('/')
    .map((segment) =>
      /^\{.+\}$/.test(segment)
        ? '(?:\\$\\{[^}]+\\}|[^/\'"`$?{}]+)'
        : segment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'),
    )
    .join('/');
  return new RegExp(
    `(?:fetchAPI(?:<.*?>)?\\(\\s*['"\`]|\\$\\{API_BASE\\}|/api/v1)${body}(?=[?'"\`$])`,
  );
}

describe('frontend API paths (catalogue contract)', () => {
  // The check below is only as good as its two helpers; they are pinned on a
  // fixed excerpt of the generated types, not on a path of today's contract.
  it('reads fully deprecated paths and finds their uses', () => {
    const excerpt = [
      "  '/api/v1/old/{id}': {",
      '    /**',
      '     * @deprecated',
      '     */',
      "    get: operations['old'];",
      '  };',
      "  '/api/v1/mixed': {",
      '    /**',
      '     * @deprecated',
      '     */',
      "    get: operations['mixedGet'];",
      "    post: operations['mixedPost'];",
      '  };',
    ].join('\n');
    expect(deprecatedPaths(excerpt)).toEqual(['/old/{id}']);
    const old = apiPathPattern('/old/{id}');
    expect(old.test('return fetchAPI(`/old/${id}`);')).toBe(true);
    expect(old.test('const u = `${API_BASE}/old/abc?x=1`;')).toBe(true);
    expect(old.test('await request.get(`${BASE}/api/v1/old/${id}`)')).toBe(true);
    expect(old.test("return fetchAPI('/older/1');")).toBe(false);
  });

  it('uses no path whose every operation is deprecated', () => {
    const deprecated = deprecatedPaths(spec);

    const uses: string[] = [];
    for (const [file, text] of Object.entries(sources)) {
      if (/\/generated\/|\.test\.tsx?$|\.auditdefect\.tsx?$|\/test\//.test(file)) continue;
      text.split('\n').forEach((line, i) => {
        for (const path of deprecated) {
          if (apiPathPattern(path).test(line))
            uses.push(`${file}:${i + 1} uses deprecated ${path}`);
        }
      });
    }
    expect(uses).toEqual([]);
  });
});
