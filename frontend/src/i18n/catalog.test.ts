/**
 * GLO-10/UI-18: enumeration values are English keys and every value the UI
 * shows has a label in both catalogs; the pages render labels from the
 * catalog, not as literals.
 */
import { describe, expect, it } from 'vitest';
import deDE from './de-DE.json';
import enUS from './en-US.json';
import { DASHBOARD_ROLES, parseDashboardRole } from '../stores/dashboard';
import { STOCKTAKE_SCOPES, STOCKTAKE_STATUSES } from '../pages/StocktakeListPage';
import { FIELD_TYPES } from '../pages/CITypeAdminPage';
import { DISCOVERY_PLUGINS } from '../pages/DiscoveryPage';

type Catalog = Record<string, unknown>;

function lookup(catalog: Catalog, key: string): unknown {
  return key
    .split('.')
    .reduce<unknown>((node, part) => (node as Catalog | undefined)?.[part], catalog);
}

const pageSources = import.meta.glob('../pages/*.tsx', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const COLLECTOR_STATUSES = ['pending', 'active', 'offline', 'disabled', 'paused'];

const enumerations: Array<[string, readonly string[]]> = [
  ['dashboard.roles', DASHBOARD_ROLES],
  ['stocktake.statuses', STOCKTAKE_STATUSES],
  ['stocktake.scopes', STOCKTAKE_SCOPES],
  ['citypes.dataTypes', FIELD_TYPES],
  ['discovery.collectorStatus', COLLECTOR_STATUSES],
  ['discovery.scopes.plugin', DISCOVERY_PLUGINS],
];

describe('i18n catalog', () => {
  it.each(enumerations)('labels every value of %s in both languages', (prefix, values) => {
    for (const value of values) {
      for (const [lang, catalog] of [
        ['de-DE', deDE],
        ['en-US', enUS],
      ] as const) {
        const label = lookup(catalog, `${prefix}.${value}`);
        expect(typeof label === 'string' && label.length > 0, `${lang} ${prefix}.${value}`).toBe(
          true,
        );
      }
    }
  });

  it('keeps enumeration values as lower-case English keys', () => {
    for (const [, values] of enumerations) {
      for (const value of values) {
        expect(value).toMatch(/^[a-z][a-z0-9_]*$/);
      }
    }
    expect(DASHBOARD_ROLES).toEqual(['technician', 'procurement', 'management']);
  });

  it('maps the stored German dashboard roles onto the English values', () => {
    expect(parseDashboardRole('technik')).toBe('technician');
    expect(parseDashboardRole('einkauf')).toBe('procurement');
    expect(parseDashboardRole('management')).toBe('management');
    expect(parseDashboardRole(null)).toBe('technician');
    expect(parseDashboardRole('unknown')).toBe('technician');
  });

  it.each([
    '../pages/DashboardPage.tsx',
    '../pages/DiscoveryPage.tsx',
    '../pages/StocktakeListPage.tsx',
    '../pages/CITypeAdminPage.tsx',
  ])('%s has no literal option labels', (file) => {
    const source = pageSources[file];
    expect(source, file).toBeDefined();
    expect(source).not.toMatch(/label:\s*['"][A-Za-zÄÖÜäöüß]{2,}/);
    expect(source).not.toMatch(/'(technik|einkauf)'/);
  });
});
