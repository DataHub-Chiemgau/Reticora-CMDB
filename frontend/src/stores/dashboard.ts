import { create } from 'zustand';

export type DashboardRole = 'technician' | 'procurement' | 'management';

export const DASHBOARD_ROLES: readonly DashboardRole[] = [
  'technician',
  'procurement',
  'management',
];

export type DashboardWidget =
  | 'ciSummary'
  | 'collectors'
  | 'reviewEscalation'
  | 'slaBreaches'
  | 'compliance'
  | 'assets'
  | 'stocktake'
  | 'tickets';

const ROLE_STORAGE_KEY = 'reticora-dashboard-role';
// The users of this browser who hid the setup checklist (WP-857), stored
// like the role: in the browser, not on the server.
const SETUP_HIDDEN_STORAGE_KEY = 'reticora-dashboard-setup-hidden';

// reviewEscalation: review items open longer than 14 days (REC-08, F-247).
export const roleWidgets: Record<DashboardRole, DashboardWidget[]> = {
  technician: ['ciSummary', 'collectors', 'reviewEscalation', 'slaBreaches', 'compliance'],
  procurement: ['assets', 'stocktake'],
  management: ['ciSummary', 'reviewEscalation', 'tickets', 'compliance', 'slaBreaches'],
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

/** parseSetupHidden reads the stored user ids; anything else counts as none. */
export function parseSetupHidden(raw: string | null): string[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === 'string') : [];
  } catch {
    return [];
  }
}

function readSetupHidden(): string[] {
  if (typeof window === 'undefined') {
    return [];
  }
  try {
    return parseSetupHidden(window.localStorage.getItem(SETUP_HIDDEN_STORAGE_KEY));
  } catch {
    return [];
  }
}

interface DashboardState {
  role: DashboardRole;
  hidden: DashboardWidget[];
  /** The users (sub) who hid the setup checklist (WP-857). */
  setupHiddenFor: string[];
  setRole: (role: DashboardRole) => void;
  /** setSetupHidden hides or shows the setup checklist for one user. */
  setSetupHidden: (user: string, hidden: boolean) => void;
  toggleWidget: (widget: DashboardWidget) => void;
  isVisible: (widget: DashboardWidget) => boolean;
}

export const useDashboardStore = create<DashboardState>((set, get) => ({
  role: readStoredRole(),
  hidden: [],
  setupHiddenFor: readSetupHidden(),
  setRole: (role) => {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(ROLE_STORAGE_KEY, role);
    }
    set({ role });
  },
  setSetupHidden: (user, hidden) => {
    const others = get().setupHiddenFor.filter((id) => id !== user);
    const setupHiddenFor = hidden ? [...others, user] : others;
    if (typeof window !== 'undefined') {
      try {
        window.localStorage.setItem(SETUP_HIDDEN_STORAGE_KEY, JSON.stringify(setupHiddenFor));
      } catch {
        // Without storage the choice holds until the page is reloaded.
      }
    }
    set({ setupHiddenFor });
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
