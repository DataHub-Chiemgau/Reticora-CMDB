import { create } from 'zustand';

/**
 * The scope the user works in (UI-01/UI-02): the organization is fixed by the
 * session; client and site narrow the lists further. Empty means "all I may
 * see" — the server's row-level security is the boundary either way, the
 * selection only narrows within it.
 */
export interface ScopeSelection {
  clientId: string;
  siteId: string;
}

interface ScopeState extends ScopeSelection {
  setClient: (clientId: string) => void;
  setSite: (siteId: string) => void;
  reset: () => void;
}

const STORAGE_KEY = 'reticora-scope';
const EMPTY: ScopeSelection = { clientId: '', siteId: '' };

function load(): ScopeSelection {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return EMPTY;
    const parsed = JSON.parse(raw) as Partial<ScopeSelection>;
    return {
      clientId: typeof parsed.clientId === 'string' ? parsed.clientId : '',
      siteId: typeof parsed.siteId === 'string' ? parsed.siteId : '',
    };
  } catch {
    return EMPTY;
  }
}

function persist(selection: ScopeSelection) {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(selection));
  } catch {
    // The selection still applies for this page view.
  }
}

export const useScopeStore = create<ScopeState>((set) => ({
  ...load(),
  // A new client invalidates the site: sites belong to one client.
  setClient: (clientId) => {
    persist({ clientId, siteId: '' });
    set({ clientId, siteId: '' });
  },
  setSite: (siteId) =>
    set((state) => {
      persist({ clientId: state.clientId, siteId });
      return { siteId };
    }),
  reset: () => {
    persist(EMPTY);
    set(EMPTY);
  },
}));
