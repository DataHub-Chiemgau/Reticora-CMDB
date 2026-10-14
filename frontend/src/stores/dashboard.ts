import { create } from 'zustand';

export type DashboardRole = 'technician' | 'procurement' | 'management';

export const DASHBOARD_ROLES: readonly DashboardRole[] = [
  'technician',
  'procurement',
  'management',
];

export type DashboardWidget =
  'ciSummary' | 'collectors' | 'slaBreaches' | 'compliance' | 'assets' | 'stocktake' | 'tickets';

const ROLE_STORAGE_KEY = 'reticora-dashboard-role';

export const roleWidgets: Record<DashboardRole, DashboardWidget[]> = {
  technician: ['ciSummary', 'collectors', 'slaBreaches', 'compliance'],
  procurement: ['assets', 'stocktake'],
  management: ['ciSummary', 'tickets', 'compliance', 'slaBreaches'],
};

// The values stored before GLO-10 (German enum values) map onto the
// English ones.
const LEGACY_ROLES: Record<string, DashboardRole> = {
  technik: 'technician',
  einkauf: 'procurement',
};

export function parseDashboardRole(raw: string | null): DashboardRole {
  if (raw && raw in LEGACY_ROLES) return LEGACY_ROLES[raw]!;
  return DASHBOARD_ROLES.find((role) => role === raw) ?? 'technician';
}

function readStoredRole(): DashboardRole {
  if (typeof window === 'undefined') {
    return 'technician';
  }
  try {
    return parseDashboardRole(window.localStorage.getItem(ROLE_STORAGE_KEY));
  } catch {
    return 'technician';
  }
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
