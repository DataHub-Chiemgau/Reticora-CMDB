import { create } from 'zustand';

export type ThemeMode = 'light' | 'dark' | 'system';

interface ThemeState {
  mode: ThemeMode;
  setMode: (mode: ThemeMode) => void;
}

const STORAGE_KEY = 'reticora-theme-mode';

function isThemeMode(value: string | null): value is ThemeMode {
  return value === 'light' || value === 'dark' || value === 'system';
}

function getSystemPreference(): 'light' | 'dark' {
  if (typeof window === 'undefined') {
    return 'light';
  }

  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function resolveMode(mode: ThemeMode): 'light' | 'dark' {
  return mode === 'system' ? getSystemPreference() : mode;
}

function applyTheme(mode: ThemeMode) {
  if (typeof document === 'undefined') {
    return;
  }

  document.documentElement.classList.toggle('dark', resolveMode(mode) === 'dark');
}

function getInitialMode(): ThemeMode {
  if (typeof window === 'undefined') {
    return 'system';
  }

  const stored = window.localStorage.getItem(STORAGE_KEY);
  return isThemeMode(stored) ? stored : 'system';
}

const initialMode = getInitialMode();

export const useThemeStore = create<ThemeState>((set) => ({
  mode: initialMode,
  setMode: (mode) => {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(STORAGE_KEY, mode);
    }
    applyTheme(mode);
    set({ mode });
  },
}));

applyTheme(initialMode);

if (typeof window !== 'undefined') {
  const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
  const handleChange = () => {
    if (useThemeStore.getState().mode === 'system') {
      applyTheme('system');
    }
  };

  if (typeof mediaQuery.addEventListener === 'function') {
    mediaQuery.addEventListener('change', handleChange);
  } else {
    mediaQuery.addListener(handleChange);
  }
}
