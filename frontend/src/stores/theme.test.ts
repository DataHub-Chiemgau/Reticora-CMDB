/**
 * UI-18: the chosen theme alone decides the page theme. A light theme stored
 * on a dark OS has no .dark class and sets data-theme, which switches off the
 * pre-JS fallback of src/index.css (:root:not([data-theme])); 'system' follows
 * the OS, also when it changes at runtime, and the store carries the resolved
 * theme so the header toggle re-renders. The toggle cycles light, dark and
 * system, so a stored 'light' on a dark OS can be undone in the UI.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const STORAGE_KEY = 'reticora-theme-mode';
const DARK_QUERY = '(prefers-color-scheme: dark)';

interface MediaStub {
  /** The OS preference the stub reports for the dark query. */
  dark: boolean;
  /** The change listeners theme.ts registered. */
  listeners: Array<() => void>;
}

// stubMatchMedia stands in for the browser's matchMedia (jsdom has none) and
// captures the change listener, so a test can flip the OS preference.
function stubMatchMedia(dark: boolean): MediaStub {
  const stub: MediaStub = { dark, listeners: [] };
  vi.stubGlobal(
    'matchMedia',
    vi.fn((query: string) => ({
      get matches() {
        return query === DARK_QUERY && stub.dark;
      },
      media: query,
      onchange: null,
      addEventListener: (_type: string, listener: () => void) => {
        stub.listeners.push(listener);
      },
      removeEventListener: () => undefined,
      addListener: (listener: () => void) => {
        stub.listeners.push(listener);
      },
      removeListener: () => undefined,
      dispatchEvent: () => false,
    })),
  );
  return stub;
}

// changeOS flips the OS preference and notifies the listeners like a browser.
function changeOS(stub: MediaStub, dark: boolean) {
  stub.dark = dark;
  for (const listener of stub.listeners) listener();
}

// loadTheme evaluates stores/theme.ts afresh: it reads the stored mode and
// applies the theme at import, as at the start of the app.
async function loadTheme() {
  vi.resetModules();
  return import('./theme');
}

const html = () => document.documentElement;

beforeEach(() => {
  window.localStorage.clear();
  html().className = '';
  delete html().dataset.theme;
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('theme store', () => {
  it('keeps a stored light theme on a dark OS', async () => {
    stubMatchMedia(true);
    window.localStorage.setItem(STORAGE_KEY, 'light');
    const { useThemeStore } = await loadTheme();

    expect(html()).not.toHaveClass('dark');
    expect(html().dataset.theme).toBe('light');
    expect(useThemeStore.getState()).toMatchObject({ mode: 'light', resolved: 'light' });
  });

  it('follows the OS without a stored choice', async () => {
    stubMatchMedia(true);
    const { useThemeStore } = await loadTheme();

    expect(html()).toHaveClass('dark');
    expect(html().dataset.theme).toBe('dark');
    expect(useThemeStore.getState()).toMatchObject({ mode: 'system', resolved: 'dark' });
  });

  it('applies and stores the chosen mode', async () => {
    const os = stubMatchMedia(true);
    window.localStorage.setItem(STORAGE_KEY, 'light');
    const { useThemeStore } = await loadTheme();
    const { setMode } = useThemeStore.getState();

    setMode('dark');
    expect(html()).toHaveClass('dark');
    expect(html().dataset.theme).toBe('dark');
    expect(window.localStorage.getItem(STORAGE_KEY)).toBe('dark');
    expect(useThemeStore.getState()).toMatchObject({ mode: 'dark', resolved: 'dark' });

    setMode('system');
    expect(html()).toHaveClass('dark');
    expect(html().dataset.theme).toBe('dark');
    expect(window.localStorage.getItem(STORAGE_KEY)).toBe('system');
    expect(useThemeStore.getState()).toMatchObject({ mode: 'system', resolved: 'dark' });

    os.dark = false;
    setMode('system');
    expect(html()).not.toHaveClass('dark');
    expect(html().dataset.theme).toBe('light');
    expect(useThemeStore.getState().resolved).toBe('light');

    setMode('light');
    expect(html()).not.toHaveClass('dark');
    expect(html().dataset.theme).toBe('light');
    expect(window.localStorage.getItem(STORAGE_KEY)).toBe('light');
  });

  it('re-applies the theme when the OS changes in system mode', async () => {
    const os = stubMatchMedia(false);
    const { useThemeStore } = await loadTheme();
    expect(os.listeners).toHaveLength(1);
    const seen: string[] = [];
    const unsubscribe = useThemeStore.subscribe((state) => seen.push(state.resolved));

    changeOS(os, true);
    expect(html()).toHaveClass('dark');
    expect(html().dataset.theme).toBe('dark');
    expect(useThemeStore.getState()).toMatchObject({ mode: 'system', resolved: 'dark' });

    changeOS(os, false);
    expect(html()).not.toHaveClass('dark');
    expect(html().dataset.theme).toBe('light');
    // Subscribers (the header toggle) see every change: no stale state.
    expect(seen).toEqual(['dark', 'light']);
    unsubscribe();
  });

  it('keeps an explicit theme when the OS changes', async () => {
    const os = stubMatchMedia(false);
    window.localStorage.setItem(STORAGE_KEY, 'light');
    const { useThemeStore } = await loadTheme();

    changeOS(os, true);
    expect(html()).not.toHaveClass('dark');
    expect(html().dataset.theme).toBe('light');
    expect(useThemeStore.getState()).toMatchObject({ mode: 'light', resolved: 'light' });

    useThemeStore.getState().setMode('dark');
    changeOS(os, false);
    expect(html()).toHaveClass('dark');
    expect(html().dataset.theme).toBe('dark');
  });

  it('cycles light, dark and system', async () => {
    stubMatchMedia(false);
    const { nextThemeMode } = await loadTheme();

    expect(nextThemeMode('light')).toBe('dark');
    expect(nextThemeMode('dark')).toBe('system');
    expect(nextThemeMode('system')).toBe('light');
  });

  it('applies the theme when the storage is unavailable', async () => {
    stubMatchMedia(true);
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    const { useThemeStore } = await loadTheme();

    expect(useThemeStore.getState()).toMatchObject({ mode: 'system', resolved: 'dark' });
    expect(() => useThemeStore.getState().setMode('light')).not.toThrow();
    expect(html()).not.toHaveClass('dark');
    expect(html().dataset.theme).toBe('light');
  });

  it('falls back to the light theme without matchMedia', async () => {
    vi.stubGlobal('matchMedia', undefined);
    const { useThemeStore } = await loadTheme();

    expect(useThemeStore.getState()).toMatchObject({ mode: 'system', resolved: 'light' });
    expect(html()).not.toHaveClass('dark');
    expect(html().dataset.theme).toBe('light');
  });
});
