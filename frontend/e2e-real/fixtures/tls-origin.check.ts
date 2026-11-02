import { expect, test } from '@playwright/test';
import { BASE, signupTenant, uiLogin } from '../support/stack';

// WP-646 (F-375): WebKit drops the Secure refresh cookie over
// http://localhost, so the WebKit projects of the real-stack suite run over
// the TLS origin of setup-tls-origin.sh. This check runs before the suite
// (playwright.tls-origin.config.ts) and stops the job with a clear message
// when the origin or the cookie is not as required; the suite's own cases
// (roles-authz.spec.ts 07, 11, 14, 14a, 14b, 15) assert the refresh itself.

const LOOPBACK = /^(localhost|.*\.localhost|127\.\d+\.\d+\.\d+|0\.0\.0\.0|\[::1?\])$/i;

test('the sign-in over the TLS origin sets the Secure refresh cookie reticora_refresh', async ({
  browser,
}) => {
  const origin = new URL(BASE);
  expect(origin.protocol, `BASE_URL ${BASE} must be a TLS origin https://<name> (F-375)`).toBe(
    'https:',
  );
  expect(LOOPBACK.test(origin.hostname), `BASE_URL ${BASE} must not name localhost (F-375)`).toBe(
    false,
  );

  // Own client address for the signup budget (E-33), as the specs do.
  const clientIp = `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
  const context = await browser.newContext({
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': clientIp },
  });
  try {
    const page = await context.newPage();
    const admin = await signupTenant(page, context.request, 'tls-origin');
    await uiLogin(page, admin);
    expect(new URL(page.url()).origin, 'the SPA stays on the TLS origin after the sign-in').toBe(
      origin.origin,
    );
    // All cookies of the context: the refresh cookie is scoped to the path of
    // the auth endpoints, not to /.
    const cookie = (await context.cookies()).find(
      (c) => c.name === 'reticora_refresh' && c.domain.replace(/^\./, '') === origin.hostname,
    );
    expect(
      cookie?.value,
      `the sign-in over ${origin.origin} sets the refresh cookie reticora_refresh in this browser (F-375: none over http://localhost in WebKit)`,
    ).toBeTruthy();
    expect(cookie?.secure, 'reticora_refresh is Secure (SEC-01)').toBe(true);
    expect(cookie?.httpOnly, 'reticora_refresh is HttpOnly (AUT-02)').toBe(true);
  } finally {
    await context.close();
  }
});
