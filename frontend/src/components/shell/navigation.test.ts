/**
 * The information architecture is data (navigation.ts). These checks keep it
 * complete and translated: every page of the router is reachable from the
 * sidebar exactly once, and every label exists in both catalogs.
 */
import { describe, expect, it } from 'vitest';
import deDE from '../../i18n/de-DE.json';
import enUS from '../../i18n/en-US.json';
import { pageToPath } from '../CommandPalette';
import { NAV_GROUPS } from './navigation';

type Catalog = Record<string, unknown>;

function lookup(catalog: Catalog, key: string): unknown {
  return key
    .split('.')
    .reduce<unknown>((node, part) => (node as Catalog | undefined)?.[part], catalog);
}

describe('navigation', () => {
  const pages = NAV_GROUPS.flatMap((group) => group.items.map((item) => item.page));

  it('lists every page exactly once', () => {
    expect([...pages].sort()).toEqual(Object.keys(pageToPath).sort());
  });

  it('gives every group but the first a heading and a unique id', () => {
    const ids = NAV_GROUPS.map((group) => group.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(NAV_GROUPS[0]?.labelKey).toBeUndefined();
    for (const group of NAV_GROUPS.slice(1)) {
      expect(group.labelKey, group.id).toBeDefined();
    }
  });

  it('translates every label in both catalogs', () => {
    const keys = [
      ...pages.map((page) => `nav.${page}`),
      ...NAV_GROUPS.flatMap((group) => (group.labelKey ? [group.labelKey] : [])),
    ];
    for (const key of keys) {
      expect(typeof lookup(deDE, key), `de-DE ${key}`).toBe('string');
      expect(typeof lookup(enUS, key), `en-US ${key}`).toBe('string');
    }
  });

  it('uses no label twice, so two entries never read the same', () => {
    const labels = pages.map((page) => lookup(deDE, `nav.${page}`));
    expect(new Set(labels).size).toBe(labels.length);
  });
});
