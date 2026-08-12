import { create } from 'zustand';

export type DashboardRole = 'technik' | 'einkauf' | 'management';

export type DashboardWidget =
  'ciSummary' | 'collectors' | 'slaBreaches' | 'compliance' | 'assets' | 'stocktake' | 'tickets';

const ROLE_STORAGE_KEY = 'reticora-dashboard-role';

export const roleWidgets: Record<DashboardRole, DashboardWidget[]> = {
  technik: ['ciSummary', 'collectors', 'slaBreaches', 'compliance'],
  einkauf: ['assets', 'stocktake'],
  management: ['ciSummary', 'tickets', 'compliance', 'slaBreaches'],
};

function readStoredRole(): DashboardRole {
  if (typeof window === 'undefined') {
    return 'technik';
  }
  const raw = window.localStorage.getItem(ROLE_STORAGE_KEY);
  return raw === 'einkauf' || raw === 'management' ? raw : 'technik';
}

interface DashboardState {
  role: DashboardRole;
  hidden: DashboardWidget[];
  setRole: (role: DashboardRole) => void;
  toggleWidget: (widget: DashboardWidget) => void;
  isVisible: (widget: DashboardWidget) => boolean;
}

export const useDashboardStore = create<DashboardState>((set, get) => ({
  role: readStoredRole(),
  hidden: [],
  setRole: (role) => {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(ROLE_STORAGE_KEY, role);
    }
    set({ role });
  },
  toggleWidget: (widget) => {
    set((state) => ({
      hidden: state.hidden.includes(widget)
        ? state.hidden.filter((entry) => entry !== widget)
        : [...state.hidden, widget],
    }));
  },
  isVisible: (widget) => roleWidgets[get().role].includes(widget) && !get().hidden.includes(widget),
}));
