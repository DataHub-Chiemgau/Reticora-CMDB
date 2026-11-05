import { create } from 'zustand';

/**
 * Layout state of the app shell that survives a reload: whether the sidebar
 * is collapsed to the icon rail and which navigation groups the user opened
 * or closed. The mobile drawer is not stored: it always starts closed.
 */
interface ShellState {
  collapsed: boolean;
  /** Group id -> open. A group without an entry follows its default. */
  groups: Record<string, boolean>;
  toggleCollapsed: () => void;
  setGroup: (id: string, open: boolean) => void;
}

const STORAGE_KEY = 'reticora-shell';

interface Stored {
  collapsed: boolean;
  groups: Record<string, boolean>;
}

function load(): Stored {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return { collapsed: false, groups: {} };
    const parsed = JSON.parse(raw) as Partial<Stored>;
    const groups: Record<string, boolean> = {};
    for (const [key, value] of Object.entries(parsed.groups ?? {})) {
      if (typeof value === 'boolean') groups[key] = value;
    }
    return { collapsed: parsed.collapsed === true, groups };
  } catch {
    return { collapsed: false, groups: {} };
  }
}

function persist(state: Stored) {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  } catch {
    // The layout still applies for this page view.
  }
}

export const useShellStore = create<ShellState>((set, get) => ({
  ...load(),
  toggleCollapsed: () => {
    const collapsed = !get().collapsed;
    persist({ collapsed, groups: get().groups });
    set({ collapsed });
  },
  setGroup: (id, open) => {
    const groups = { ...get().groups, [id]: open };
    persist({ collapsed: get().collapsed, groups });
    set({ groups });
  },
}));
