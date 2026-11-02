import { create } from 'zustand';

export type ThemeMode = 'light' | 'dark' | 'system';

/** ResolvedTheme is the theme in effect: 'system' resolved by the OS preference. */
export type ResolvedTheme = 'light' | 'dark';

interface ThemeState {
  mode: ThemeMode;
  /**
   * os is the OS preference. It is tracked in every mode because the next
   * step of the theme toggle depends on it (nextThemeMode).
   */
  os: ResolvedTheme;
  /**
   * resolved is the theme the page shows. It is state (not read from the DOM)
   * so components re-render when the OS preference changes in 'system' mode.
   */
  resolved: ResolvedTheme;
  setMode: (mode: ThemeMode) => void;
}

const STORAGE_KEY = 'reticora-theme-mode';
const DARK_QUERY = '(prefers-color-scheme: dark)';

const OPPOSITE: Record<ResolvedTheme, ResolvedTheme> = { light: 'dark', dark: 'light' };

/**
 * nextThemeMode is the mode the theme toggle switches to from mode when the
 * OS prefers os. The cycle is relative to the OS theme: system -> the theme
 * opposite to the OS -> the OS theme -> system. So every click away from
 * 'system' changes the page, only the return to following the OS (which
 * shows the OS theme already shown) looks the same, and an explicit theme,
 * such as a stored 'light' on a dark OS, can always be undone.
 */
export function nextThemeMode(mode: ThemeMode, os: ResolvedTheme): ThemeMode {
  if (mode === 'system') {
    return OPPOSITE[os];
  }
  return mode === os ? 'system' : os;
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

// applyTheme puts the theme of mode on <html> when the OS prefers os: the
// 'dark' class drives the dark: variants (tailwind.config.ts darkMode 'class')
// and the dark surface of src/index.css; data-theme switches off the pre-JS
// fallback there (:root:not([data-theme])), so the OS preference no longer
// overrides a theme the user chose.
function applyTheme(mode: ThemeMode, os: ResolvedTheme): ResolvedTheme {
  const resolved = mode === 'system' ? os : mode;
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
const initialOS = getSystemPreference();

export const useThemeStore = create<ThemeState>((set) => ({
  mode: initialMode,
  os: initialOS,
  resolved: applyTheme(initialMode, initialOS),
  setMode: (mode) => {
    storeMode(mode);
    const os = getSystemPreference();
    set({ mode, os, resolved: applyTheme(mode, os) });
  },
}));

const mediaQuery = darkQuery();
if (mediaQuery) {
  // An OS change re-applies the theme in 'system' mode (an explicit theme
  // stays) and updates os in every mode, so the toggle's next step is current.
  const handleChange = () => {
    const os = getSystemPreference();
    useThemeStore.setState((state) => ({ os, resolved: applyTheme(state.mode, os) }));
  };

  if (typeof mediaQuery.addEventListener === 'function') {
    mediaQuery.addEventListener('change', handleChange);
  } else {
    mediaQuery.addListener(handleChange);
  }
}
