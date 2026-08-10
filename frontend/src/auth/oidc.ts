import { useAuthStore, type AuthSession } from './authStore';

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

export interface AuthConfig {
  issuer: string;
  client_id: string;
  redirect_uri: string;
  scopes: string[];
  authorization_endpoint: string;
  end_session_endpoint?: string;
  pkce_required: boolean;
}

interface AuthTransaction {
  state: string;
  codeVerifier: string;
  returnTo: string;
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

export async function startAuthorizationCodeFlow(returnTo: string) {
  // The PKCE code challenge is derived from the Web Crypto API, which browsers
  // only expose in secure contexts (HTTPS or localhost). Fail early with a
  // readable message instead of a "Cannot read properties of undefined" error.
  getWebCrypto();
  const config = await fetchAuthConfig();
  const state = randomBase64Url();
  const codeVerifier = randomBase64Url();
  const codeChallenge = await sha256Base64Url(codeVerifier);
  const safeReturnTo = returnTo.startsWith('/') ? returnTo : '/dashboard';

  window.sessionStorage.setItem(
    AUTH_TRANSACTION_KEY,
    JSON.stringify({ state, codeVerifier, returnTo: safeReturnTo } satisfies AuthTransaction),
  );

  const authorizationUrl = new URL(config.authorization_endpoint);
  authorizationUrl.searchParams.set('response_type', 'code');
  authorizationUrl.searchParams.set('client_id', config.client_id);
  authorizationUrl.searchParams.set('redirect_uri', config.redirect_uri);
  authorizationUrl.searchParams.set('scope', config.scopes.join(' '));
  authorizationUrl.searchParams.set('state', state);
  authorizationUrl.searchParams.set('code_challenge_method', 'S256');
  authorizationUrl.searchParams.set('code_challenge', codeChallenge);

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

  const response = await fetch(`${API_BASE}/auth/callback`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ code, state, code_verifier: transaction.codeVerifier }),
  });

  if (!response.ok) {
    const payload = await response.json().catch(() => ({ detail: response.statusText }));
    throw new Error(payload.detail || response.statusText);
  }

  const payload = (await response.json()) as CallbackResponse;
  const expiresAt = payload.expires_at ?? payload.expiresAt;
  if (!payload.token || !expiresAt) {
    throw new Error('Invalid authentication response.');
  }

  const session: AuthSession = { token: payload.token, expiresAt, user: payload.user };
  useAuthStore.getState().setSession(session);
  window.sessionStorage.removeItem(AUTH_TRANSACTION_KEY);

  return { session, returnTo: transaction.returnTo || '/dashboard' };
}

export function clearAuthTransaction() {
  window.sessionStorage.removeItem(AUTH_TRANSACTION_KEY);
}
