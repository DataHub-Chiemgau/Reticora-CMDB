import { create } from 'zustand';

export interface AuthUser {
  sub: string;
  email?: string;
  name?: string;
  groups?: string[];
  org_id?: string;
  permissions?: string[];
}

export interface AuthSession {
  token: string;
  expiresAt: string;
  user: AuthUser;
}

interface AuthState {
  token: string | null;
  expiresAt: string | null;
  user: AuthUser | null;
  setSession: (session: AuthSession) => void;
  clearSession: () => void;
  isAuthenticated: () => boolean;
}

export const AUTH_STORAGE_KEY = 'reticora-auth-session';

function readStoredSession(): AuthSession | null {
  if (typeof window === 'undefined') {
    return null;
  }

  const raw = window.sessionStorage.getItem(AUTH_STORAGE_KEY);
  if (!raw) {
    return null;
  }

  try {
    const parsed = JSON.parse(raw) as Partial<AuthSession>;
    if (typeof parsed.token === 'string' && typeof parsed.expiresAt === 'string' && parsed.user) {
      return { token: parsed.token, expiresAt: parsed.expiresAt, user: parsed.user as AuthUser };
    }
  } catch {
    window.sessionStorage.removeItem(AUTH_STORAGE_KEY);
  }

  return null;
}

function persistSession(session: AuthSession | null) {
  if (typeof window === 'undefined') {
    return;
  }

  if (session) {
    window.sessionStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify(session));
    return;
  }

  window.sessionStorage.removeItem(AUTH_STORAGE_KEY);
}

function hasValidSession(token: string | null, expiresAt: string | null) {
  if (!token || !expiresAt) {
    return false;
  }

  const expires = Date.parse(expiresAt);
  return Number.isFinite(expires) && expires > Date.now();
}

// REFRESH_LEAD_MS: the access token is renewed this long before it expires
// (AUT-02), so a user who keeps working does not run into an expired token.
const REFRESH_LEAD_MS = 60_000;
// The longest delay setTimeout supports (about 24.8 days); a longer one fires
// at once. Access tokens live 15 minutes.
const MAX_TIMER_MS = 2_147_483_647;

let refreshTimer: ReturnType<typeof setTimeout> | undefined;
let refreshHandler: (() => unknown) | null = null;

// scheduleRefresh plans the renewal about a minute before expiresAt and
// replaces a planned one. An expired token is not planned: RequireAuth or the
// next 401 renews it (F-066).
function scheduleRefresh(expiresAt: string | null) {
  clearTimeout(refreshTimer);
  refreshTimer = undefined;
  if (!expiresAt || !refreshHandler) {
    return;
  }

  const expires = Date.parse(expiresAt);
  const delay = expires - REFRESH_LEAD_MS - Date.now();
  if (!Number.isFinite(expires) || expires <= Date.now() || delay > MAX_TIMER_MS) {
    return;
  }
  refreshTimer = setTimeout(() => void refreshHandler?.(), Math.max(delay, 0));
}

const initialSession = readStoredSession();

export const useAuthStore = create<AuthState>((set, get) => ({
  token: initialSession?.token ?? null,
  expiresAt: initialSession?.expiresAt ?? null,
  user: initialSession?.user ?? null,
  setSession: (session) => {
    persistSession(session);
    set({ token: session.token, expiresAt: session.expiresAt, user: session.user });
    scheduleRefresh(session.expiresAt);
  },
  clearSession: () => {
    persistSession(null);
    set({ token: null, expiresAt: null, user: null });
    scheduleRefresh(null);
  },
  isAuthenticated: () => {
    const { token, expiresAt } = get();
    return hasValidSession(token, expiresAt);
  },
}));

// setRefreshHandler registers the renewal the store plans before the access
// token expires (session.ts registers refreshSession; the store cannot import
// it without an import cycle) and plans it for the current session.
export function setRefreshHandler(handler: (() => unknown) | null) {
  refreshHandler = handler;
  scheduleRefresh(useAuthStore.getState().expiresAt);
}
