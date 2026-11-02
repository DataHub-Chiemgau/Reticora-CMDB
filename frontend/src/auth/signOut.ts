import { fetchAuthConfig, getStoredAuthConfig, type AuthConfig } from './oidc';
import { logout } from './session';

/** POST_LOGOUT_PATH is the page the IdP sends the browser back to after its logout. */
export const POST_LOGOUT_PATH = '/login';

/**
 * endSessionUrl is the OpenID Connect RP-initiated logout request of the
 * configuration: the IdP's end_session_endpoint with client_id and
 * post_logout_redirect_uri, null without an end_session_endpoint.
 *
 * The return address is the sign-in page on the origin of the configured
 * redirect_uri (relative to this page's origin if it is relative): that
 * origin is the one registered at the IdP (the bundled realm accepts the
 * client's redirect URIs as post-logout redirect URIs,
 * "post.logout.redirect.uris": "+", which include <origin>/*), and the one
 * where the next sign-in can complete. With client_id Keycloak accepts the
 * redirect without id_token_hint; the SPA keeps no ID token (the server
 * issues its own session), so Keycloak asks once to confirm the logout while
 * its session is still alive and then returns to the sign-in page.
 */
export function endSessionUrl(config: AuthConfig): string | null {
  if (!config.end_session_endpoint) {
    return null;
  }
  try {
    const url = new URL(config.end_session_endpoint);
    const app = new URL(config.redirect_uri, window.location.origin);
    url.searchParams.set('client_id', config.client_id);
    url.searchParams.set('post_logout_redirect_uri', new URL(POST_LOGOUT_PATH, app).toString());
    return url.toString();
  } catch {
    // An end-session endpoint or redirect URI that is no URL: no IdP logout.
    return null;
  }
}

/**
 * signOut ends the Reticora session (logout) and then the IdP's single
 * sign-on session: the browser leaves for the end-session endpoint, which
 * returns to the sign-in page (endSessionUrl). Without the IdP session the
 * next sign-in asks for credentials instead of signing the previous account
 * in again, e.g. after a self-signup in a browser where the superadmin is
 * still signed in (F-351). Returns 'idp' when the browser is on its way to
 * the IdP and 'local' when only the Reticora session ended (no
 * end_session_endpoint, or no configuration); the caller then shows the
 * sign-in itself.
 */
export async function signOut(): Promise<'idp' | 'local'> {
  await logout();

  let url: string | null = null;
  try {
    url = endSessionUrl(getStoredAuthConfig() ?? (await fetchAuthConfig()));
  } catch {
    // The configuration is unreachable: the local logout above has to do.
  }
  if (!url) {
    return 'local';
  }
  window.location.assign(url);
  return 'idp';
}
