import { create } from 'zustand';

/**
 * The operator session of the SPA (PKG-07, SEC-07, WP-887): the session the
 * server issues at POST /api/v1/admin/auth/callback to a member of the group
 * operators with a second factor. It is kept apart from the tenant session
 * (AUTH_STORAGE_KEY): its own key in sessionStorage, its own store. It lasts
 * 15 minutes without refresh; after expires_at the operator area drops it and
 * asks for a new login.
 */
export const OPERATOR_SESSION_KEY = 'reticora-operator-session';

/** The operator of a session (operator.Identity on the server). */
export interface OperatorIdentity {
  id: string;
  name?: string;
  email?: string;
  kind: string;
}

export interface OperatorSession {
  token: string;
  expiresAt: string;
  operator: OperatorIdentity;
}

interface OperatorAuthState {
  session: OperatorSession | null;
  setSession: (session: OperatorSession) => void;
  clearSession: () => void;
}

/** isSessionValid: the session exists and expires_at lies after now. */
export function isSessionValid(session: OperatorSession | null, now = Date.now()): boolean {
  if (!session) return false;
  const expires = Date.parse(session.expiresAt);
  return Number.isFinite(expires) && expires > now;
}

function readStoredSession(): OperatorSession | null {
  if (typeof window === 'undefined') return null;
  const raw = window.sessionStorage.getItem(OPERATOR_SESSION_KEY);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as Partial<OperatorSession>;
    if (!parsed.token || !parsed.expiresAt || !parsed.operator?.id) {
      window.sessionStorage.removeItem(OPERATOR_SESSION_KEY);
      return null;
    }
    const session = parsed as OperatorSession;
    if (!isSessionValid(session)) {
      window.sessionStorage.removeItem(OPERATOR_SESSION_KEY);
      return null;
    }
    return session;
  } catch {
    window.sessionStorage.removeItem(OPERATOR_SESSION_KEY);
    return null;
  }
}

export const useOperatorAuth = create<OperatorAuthState>((set) => ({
  session: readStoredSession(),
  setSession: (session) => {
    window.sessionStorage.setItem(OPERATOR_SESSION_KEY, JSON.stringify(session));
    set({ session });
  },
  clearSession: () => {
    window.sessionStorage.removeItem(OPERATOR_SESSION_KEY);
    set({ session: null });
  },
}));

/**
 * currentOperatorToken returns the token of a valid session; an expired
 * session is dropped (no refresh).
 */
export function currentOperatorToken(now = Date.now()): string | null {
  const { session, clearSession } = useOperatorAuth.getState();
  if (!session) return null;
  if (!isSessionValid(session, now)) {
    clearSession();
    return null;
  }
  return session.token;
}
