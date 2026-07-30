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

const initialSession = readStoredSession();

export const useAuthStore = create<AuthState>((set, get) => ({
  token: initialSession?.token ?? null,
  expiresAt: initialSession?.expiresAt ?? null,
  user: initialSession?.user ?? null,
  setSession: (session) => {
    persistSession(session);
    set({ token: session.token, expiresAt: session.expiresAt, user: session.user });
  },
  clearSession: () => {
    persistSession(null);
    set({ token: null, expiresAt: null, user: null });
  },
  isAuthenticated: () => {
    const { token, expiresAt } = get();
    return hasValidSession(token, expiresAt);
  },
}));
