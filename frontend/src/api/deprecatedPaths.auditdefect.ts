// Phase-1 acceptance audit, group T2-auth-authz: defect test of the API
// paths the frontend uses (fails until the finding is fixed).
//
// WP-168 (API-05, API-01 catalogue paths), finding P2 "CI list export
// buttons use deprecated /api/v1/export/cis via plain links, 401 in the
// browser"; gap (high) "Nothing prevents frontend code from using deprecated
// API paths". The OpenAPI document marks the old paths deprecated (sunset
// 2027-10-15) in favour of the catalogue paths; frontend code must not call
// a path whose every operation is deprecated.
//
// The deprecated operations are read from the types generated from
// api/openapi.yaml (src/api/generated/schema.d.ts, kept in sync by
// generate:api:check; Vite does not serve files outside frontend/); a use is
// an API path literal (after fetchAPI(, ${API_BASE} or /api/v1) in
// frontend/src (generated code and tests excluded) and frontend/e2e.
//
// Retest: cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/api/deprecatedPaths.auditdefect.ts
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
  it('uses no path whose every operation is deprecated', () => {
    const deprecated = deprecatedPaths(spec);
    expect(deprecated, 'fully deprecated paths of the API contract').toContain('/export/cis');

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
