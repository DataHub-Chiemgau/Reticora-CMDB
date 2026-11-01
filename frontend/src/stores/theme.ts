import { create } from 'zustand';

export type ThemeMode = 'light' | 'dark' | 'system';

/** ResolvedTheme is the theme in effect: 'system' resolved by the OS preference. */
export type ResolvedTheme = 'light' | 'dark';

interface ThemeState {
  mode: ThemeMode;
  /**
   * resolved is the theme the page shows. It is state (not read from the DOM)
   * so components re-render when the OS preference changes in 'system' mode.
   */
  resolved: ResolvedTheme;
  setMode: (mode: ThemeMode) => void;
}

const STORAGE_KEY = 'reticora-theme-mode';
const DARK_QUERY = '(prefers-color-scheme: dark)';

// The order of the theme toggle: from an explicit theme it always leads back
// to following the OS, so a stored 'light' on a dark OS can be undone.
const NEXT_MODE: Record<ThemeMode, ThemeMode> = {
  light: 'dark',
  dark: 'system',
  system: 'light',
};

/** nextThemeMode is the mode the theme toggle switches to from mode. */
export function nextThemeMode(mode: ThemeMode): ThemeMode {
  return NEXT_MODE[mode];
}

function isThemeMode(value: string | null): value is ThemeMode {
  return value === 'light' || value === 'dark' || value === 'system';
}

function darkQuery(): MediaQueryList | null {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return null;
  }

  return window.matchMedia(DARK_QUERY);
}

function getSystemPreference(): ResolvedTheme {
  return darkQuery()?.matches ? 'dark' : 'light';
}

function resolveMode(mode: ThemeMode): ResolvedTheme {
  return mode === 'system' ? getSystemPreference() : mode;
}

// applyTheme puts the resolved theme on <html>: the 'dark' class drives the
// dark: variants (tailwind.config.ts darkMode 'class') and the dark surface of
// src/index.css; data-theme switches off the pre-JS fallback there
// (:root:not([data-theme])), so the OS preference no longer overrides a theme
// the user chose.
function applyTheme(mode: ThemeMode): ResolvedTheme {
  const resolved = resolveMode(mode);
  if (typeof document !== 'undefined') {
    const root = document.documentElement;
    root.classList.toggle('dark', resolved === 'dark');
    root.dataset.theme = resolved;
  }
  return resolved;
}

// Storage can be unavailable (blocked site data, some private modes) and then
// throws; the theme still applies, the choice just lasts for this page only.
function getInitialMode(): ThemeMode {
  if (typeof window === 'undefined') {
    return 'system';
  }

  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    return isThemeMode(stored) ? stored : 'system';
  } catch {
    return 'system';
  }
}

function storeMode(mode: ThemeMode) {
  if (typeof window === 'undefined') {
    return;
  }

  try {
    window.localStorage.setItem(STORAGE_KEY, mode);
  } catch {
    // See getInitialMode.
  }
}

const initialMode = getInitialMode();

export const useThemeStore = create<ThemeState>((set) => ({
  mode: initialMode,
  resolved: applyTheme(initialMode),
  setMode: (mode) => {
    storeMode(mode);
    set({ mode, resolved: applyTheme(mode) });
  },
}));

const mediaQuery = darkQuery();
if (mediaQuery) {
  const handleChange = () => {
    if (useThemeStore.getState().mode === 'system') {
      useThemeStore.setState({ resolved: applyTheme('system') });
    }
  };

  if (typeof mediaQuery.addEventListener === 'function') {
    mediaQuery.addEventListener('change', handleChange);
  } else {
    mediaQuery.addListener(handleChange);
  }
}
