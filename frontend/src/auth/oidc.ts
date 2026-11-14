import { useAuthStore, type AuthSession } from './authStore';
import { useOperatorAuth, type OperatorSession } from '../operator/operatorAuth';

const API_BASE = '/api/v1';
const AUTH_TRANSACTION_KEY = 'reticora-auth-transaction';
const AUTH_CONFIG_KEY = 'reticora-auth-config';

export class InsecureContextError extends Error {
  constructor() {
    super(
      'The Web Crypto API is not available. Sign-in requires a secure context: access Reticora CMDB via HTTPS or on localhost.',
    );
    this.name = 'InsecureContextError';
  }
}

/**
 * AuthCallbackError is a refused POST /auth/callback: the HTTP status and
 * the problem type, which the callback page maps to a localized text
 * (F-197); the message is the problem's detail.
 */
export class AuthCallbackError extends Error {
  constructor(
    readonly status: number,
    readonly problemType: string | undefined,
    detail: string,
  ) {
    super(detail);
    this.name = 'AuthCallbackError';
  }
}

export interface AuthConfig {
  issuer: string;
  client_id: string;
  redirect_uri: string;
  scopes: string[];
  authorization_endpoint: string;
  end_session_endpoint?: string;
  pkce_required: boolean;
}

/**
 * The kind of a login: the tenant session (default) or the operator session
 * of the operator area (WP-887), which the server issues at
 * /api/v1/admin/auth/callback.
 */
export type AuthKind = 'tenant' | 'operator';

/**
 * OPERATOR_ACR_VALUES asks the IdP for the second factor of an operator
 * login: the default of RETICORA_OPERATOR_MFA_ACR (realm flow browser-mfa).
 */
export const OPERATOR_ACR_VALUES = '2';

/** The start page of the operator area. */
export const OPERATOR_HOME = '/operator';

interface AuthTransaction {
  state: string;
  codeVerifier: string;
  returnTo: string;
  kind?: AuthKind;
}

interface CallbackResponse {
  token: string;
  expires_at?: string;
  expiresAt?: string;
  user: AuthSession['user'];
}

function base64UrlEncode(bytes: Uint8Array) {
  let binary = '';
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte);
  });

  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function randomBase64Url(bytes = 32) {
  const values = new Uint8Array(bytes);
  getWebCrypto().getRandomValues(values);
  return base64UrlEncode(values);
}

async function sha256Base64Url(value: string) {
  const data = new TextEncoder().encode(value);
  const digest = await getWebCrypto().subtle.digest('SHA-256', data);
  return base64UrlEncode(new Uint8Array(digest));
}

function getWebCrypto() {
  const cryptoApi = globalThis.crypto;
  if (!cryptoApi || typeof cryptoApi.getRandomValues !== 'function' || !cryptoApi.subtle) {
    throw new InsecureContextError();
  }
  return cryptoApi;
}

function readTransaction(): AuthTransaction | null {
  const raw = window.sessionStorage.getItem(AUTH_TRANSACTION_KEY);
  if (!raw) {
    return null;
  }

  try {
    return JSON.parse(raw) as AuthTransaction;
  } catch {
    window.sessionStorage.removeItem(AUTH_TRANSACTION_KEY);
    return null;
  }
}

function storeAuthConfig(config: AuthConfig) {
  window.sessionStorage.setItem(AUTH_CONFIG_KEY, JSON.stringify(config));
}

export function getStoredAuthConfig(): AuthConfig | null {
  const raw = window.sessionStorage.getItem(AUTH_CONFIG_KEY);
  if (!raw) {
    return null;
  }

  try {
    return JSON.parse(raw) as AuthConfig;
  } catch {
    window.sessionStorage.removeItem(AUTH_CONFIG_KEY);
    return null;
  }
}

export async function fetchAuthConfig() {
  const response = await fetch(`${API_BASE}/auth/config`);
  if (!response.ok) {
    throw new Error('Unable to load authentication configuration.');
  }

  const config = (await response.json()) as AuthConfig;
  assertUsableAuthConfig(config);
  storeAuthConfig(config);
  return config;
}

// The server omits fields it has no value for (e.g. an unset
// RETICORA_OIDC_REDIRECT_URL). Sending the browser to the identity provider
// with "undefined" parameters only produces an opaque provider error page, so
// report the missing configuration here instead.
function assertUsableAuthConfig(config: AuthConfig) {
  const missing = (['client_id', 'redirect_uri', 'authorization_endpoint'] as const).filter(
    (field) => !config?.[field],
  );

  if (missing.length > 0) {
    throw new Error(
      `Authentication is not configured completely on the server (missing: ${missing.join(', ')}).`,
    );
  }
}

const DEFAULT_RETURN_TO = '/dashboard';

/**
 * The target after the login if it is a local path of this origin, otherwise
 * /dashboard (SR-62, WP-874): exactly one leading slash, no "//" and no
 * backslash (also percent-encoded), and the path resolves to the own origin.
 * "//host", "/\host" or "https://host" would navigate away after the login.
 */
export function safeReturnTo(returnTo: unknown): string {
  if (typeof returnTo !== 'string' || !returnTo.startsWith('/')) {
    return DEFAULT_RETURN_TO;
  }
  let decoded: string;
  try {
    decoded = decodeURIComponent(returnTo);
  } catch {
    return DEFAULT_RETURN_TO;
  }
  for (const value of [returnTo, decoded]) {
    if (value.startsWith('//') || value.includes('\\')) {
      return DEFAULT_RETURN_TO;
    }
  }
  try {
    if (new URL(returnTo, window.location.origin).origin !== window.location.origin) {
      return DEFAULT_RETURN_TO;
    }
  } catch {
    return DEFAULT_RETURN_TO;
  }
  return returnTo;
}

/**
 * pendingAuthKind is the kind of the login whose callback is due (tenant
 * without a transaction).
 */
export function pendingAuthKind(): AuthKind {
  return readTransaction()?.kind === 'operator' ? 'operator' : 'tenant';
}

export async function startAuthorizationCodeFlow(
  returnTo: string,
  options: { kind?: AuthKind } = {},
) {
  const kind: AuthKind = options.kind ?? 'tenant';
  // The PKCE code challenge is derived from the Web Crypto API, which browsers
  // only expose in secure contexts (HTTPS or localhost). Fail early with a
  // readable message instead of a "Cannot read properties of undefined" error.
  getWebCrypto();
  const config = await fetchAuthConfig();
  const state = randomBase64Url();
  const codeVerifier = randomBase64Url();
  const codeChallenge = await sha256Base64Url(codeVerifier);
  window.sessionStorage.setItem(
    AUTH_TRANSACTION_KEY,
    JSON.stringify({
      state,
      codeVerifier,
      returnTo: kind === 'operator' ? OPERATOR_HOME : safeReturnTo(returnTo),
      kind,
    } satisfies AuthTransaction),
  );

  const authorizationUrl = new URL(config.authorization_endpoint);
  authorizationUrl.searchParams.set('response_type', 'code');
  authorizationUrl.searchParams.set('client_id', config.client_id);
  authorizationUrl.searchParams.set('redirect_uri', config.redirect_uri);
  authorizationUrl.searchParams.set('scope', config.scopes.join(' '));
  authorizationUrl.searchParams.set('state', state);
  authorizationUrl.searchParams.set('code_challenge_method', 'S256');
  authorizationUrl.searchParams.set('code_challenge', codeChallenge);
  if (kind === 'operator') {
    // The operator session needs a second factor (SEC-07): the server checks
    // amr/acr, the IdP is asked for it here.
    authorizationUrl.searchParams.set('acr_values', OPERATOR_ACR_VALUES);
  }

  window.location.assign(authorizationUrl.toString());
  return authorizationUrl.toString();
}

export async function completeAuthorizationCodeFlow(search: string) {
  const params = new URLSearchParams(search);
  const error = params.get('error');
  if (error) {
    throw new Error(params.get('error_description') || error);
  }

  const code = params.get('code');
  const state = params.get('state');
  const transaction = readTransaction();

  if (!code || !state || !transaction || state !== transaction.state) {
    throw new Error('Invalid authentication state.');
  }

  if (transaction.kind === 'operator') {
    return completeOperatorCallback(code, state, transaction);
  }

  // The response sets the HttpOnly refresh cookie (AUT-02).
  const response = await fetch(`${API_BASE}/auth/callback`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ code, state, code_verifier: transaction.codeVerifier }),
  });

  if (!response.ok) {
    const payload = (await response.json().catch(() => ({ detail: response.statusText }))) as {
      type?: string;
      detail?: string;
    };
    throw new AuthCallbackError(
      response.status,
      payload.type,
      payload.detail || response.statusText,
    );
  }

  const payload = (await response.json()) as CallbackResponse;
  const expiresAt = payload.expires_at ?? payload.expiresAt;
  if (!payload.token || !expiresAt) {
    throw new Error('Invalid authentication response.');
  }

  const session: AuthSession = { token: payload.token, expiresAt, user: payload.user };
  useAuthStore.getState().setSession(session);
  window.sessionStorage.removeItem(AUTH_TRANSACTION_KEY);

  // The stored target is checked again: sessionStorage is writable by any
  // script of the origin.
  return {
    session,
    operator: null,
    returnTo: safeReturnTo(transaction.returnTo),
    kind: 'tenant' as const,
  };
}

export function clearAuthTransaction() {
  window.sessionStorage.removeItem(AUTH_TRANSACTION_KEY);
}

/**
 * completeOperatorCallback exchanges the code at the operator callback
 * (POST /api/v1/admin/auth/callback) and stores the operator session; the
 * tenant session stays as it is (WP-887). No refresh cookie: the operator
 * session ends after 15 minutes.
 */
async function completeOperatorCallback(code: string, state: string, transaction: AuthTransaction) {
  const response = await fetch(`${API_BASE}/admin/auth/callback`, {
    method: 'POST',
    credentials: 'omit',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ code, state, code_verifier: transaction.codeVerifier }),
  });
  if (!response.ok) {
    const payload = (await response.json().catch(() => ({ detail: response.statusText }))) as {
      type?: string;
      detail?: string;
    };
    throw new AuthCallbackError(
      response.status,
      payload.type,
      payload.detail || response.statusText,
    );
  }
  const payload = (await response.json()) as {
    token?: string;
    expires_at?: string;
    operator?: OperatorSession['operator'];
  };
  if (!payload.token || !payload.expires_at || !payload.operator) {
    throw new Error('Invalid authentication response.');
  }
  const session: OperatorSession = {
    token: payload.token,
    expiresAt: payload.expires_at,
    operator: payload.operator,
  };
  useOperatorAuth.getState().setSession(session);
  window.sessionStorage.removeItem(AUTH_TRANSACTION_KEY);
  return { session: null, operator: session, returnTo: OPERATOR_HOME, kind: 'operator' as const };
}
