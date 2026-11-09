/**
 * The information architecture is data (navigation.ts). These checks keep it
 * complete and translated: every page of the router is reachable from the
 * sidebar exactly once, and every label exists in both catalogs.
 */
import { describe, expect, it } from 'vitest';
import deDE from '../../i18n/de-DE.json';
import enUS from '../../i18n/en-US.json';
import { pageToPath } from '../CommandPalette';
import { NAV_GROUPS, navPageState, navPageStates } from './navigation';
import type { NavAccess } from './navigation';

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

/**
 * The one visibility rule (WP-857): only a page that fails on the plan
 * alone is shown locked; a switched-off release module and a missing right
 * hide it.
 */
describe('navPageState', () => {
  const access = (overrides: Partial<NavAccess> = {}): NavAccess => ({
    isEntitled: () => true,
    isPageEnabled: () => true,
    isPermitted: () => true,
    ...overrides,
  });

  it('shows a page every gate admits', () => {
    expect(navPageState('assets', access())).toBe('visible');
  });

  it('locks a page whose only obstacle is the plan', () => {
    expect(navPageState('assets', access({ isEntitled: (page) => page !== 'assets' }))).toBe(
      'locked',
    );
  });

  it('hides a page of a switched-off release module, also without the plan', () => {
    expect(
      navPageState('monitoring', access({ isEntitled: () => false, isPageEnabled: () => false })),
    ).toBe('hidden');
  });

  it('hides a page without the right, also without the plan', () => {
    expect(
      navPageState('ci-types', access({ isEntitled: () => false, isPermitted: () => false })),
    ).toBe('hidden');
  });

  it('rates every page of the navigation', () => {
    const states = navPageStates(access({ isEntitled: (page) => page !== 'assets' }));
    const pages = NAV_GROUPS.flatMap((group) => group.items.map((item) => item.page));
    expect([...states.keys()].sort()).toEqual([...pages].sort());
    expect(states.get('assets')).toBe('locked');
    expect(states.get('cmdb')).toBe('visible');
  });

  it('translates the plan names and the hints in both catalogs', () => {
    const keys = [
      'discoverability.lockedInPlan',
      'discoverability.lockedNoPlan',
      ...['essential', 'standard', 'pro', 'enterprise'].map((p) => `discoverability.plans.${p}`),
    ];
    for (const key of keys) {
      expect(typeof lookup(deDE, key), `de-DE ${key}`).toBe('string');
      expect(typeof lookup(enUS, key), `en-US ${key}`).toBe('string');
    }
  });
});
