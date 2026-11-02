/**
 * API-02 (F-362): a list endpoint answers at most 200 rows per page
 * (parameter Limit); a larger limit is capped by the server. A page that
 * asks for more and takes the answer as complete silently drops the rest:
 * the dashboard counted at most 200 collectors (limit=1000), the map showed
 * at most 200 sites (limit=500). The complete set is read with
 * fetchAllPages (src/api/client.ts).
 *
 * Source scan over the production sources of src/ (tests excluded): no
 * literal limit above API_MAX_LIMIT, neither as an object property
 * (`limit: 1000`) nor in a query string (`limit=1000`).
 */
import { describe, expect, it } from 'vitest';
import { API_MAX_LIMIT } from './client';

const sources = import.meta.glob<string>(['../**/*.ts', '../**/*.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
});

function isTestFile(file: string): boolean {
  return /\.(test|spec|auditdefect)\.tsx?$/.test(file) || file.includes('/test/');
}

const LIMIT = /\blimit\s*(?::|=)\s*(\d+)/g;

/** oversizedLimits lists every literal limit above API_MAX_LIMIT as file:line. */
function oversizedLimits(files: Record<string, string>): string[] {
  const found: string[] = [];
  for (const [file, source] of Object.entries(files)) {
    if (isTestFile(file)) continue;
    source.split('\n').forEach((line, i) => {
      for (const match of line.matchAll(LIMIT)) {
        if (Number(match[1]) > API_MAX_LIMIT) {
          found.push(`${file.replace(/^\.\.\//, 'src/')}:${i + 1}: ${line.trim()}`);
        }
      }
    });
  }
  return found;
}

describe('list limits (API-02, F-362)', () => {
  it('finds an oversized limit in a fixture', () => {
    expect(
      oversizedLimits({
        '../pages/Fixture.tsx': [
          'useCollectors({ limit: 1000, offset: 0 });',
          "fetchAPI('/sites?limit=500');",
          'siteApi.list({ limit: 200 });',
        ].join('\n'),
      }),
    ).toEqual([
      'src/pages/Fixture.tsx:1: useCollectors({ limit: 1000, offset: 0 });',
      "src/pages/Fixture.tsx:2: fetchAPI('/sites?limit=500');",
    ]);
  });

  it('no production source asks a list for more than 200 rows per page', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(50);
    expect(oversizedLimits(sources)).toEqual([]);
  });
});
