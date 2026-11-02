/**
 * Tests for the onboarding flow (UI-17): signup with the ALTCHA proof of
 * work, verification by link, collector enrollment, scope confirmation and
 * the first CIs, with the progress over all steps.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { Route, Routes, useLocation } from 'react-router-dom';
import { renderWithProviders } from '../test/utils';
import { OnboardingPage, currentStep, passwordPolicyViolations } from './OnboardingPage';
import { useAuthStore } from '../auth/authStore';

/** A password that meets the realm policy (WP-112, deploy/keycloak). */
const GOOD_PASSWORD = 'Ein-langes-Passwort-7';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function page(data: unknown[], total = data.length) {
  return { data, total, limit: 1, offset: 0, has_more: false };
}

const salt = 'abc123?expires=9999999999';
const challenge = { algorithm: 'SHA-256', challenge: '', maxnumber: 50, salt, signature: 'sig' };

beforeAll(async () => {
  // The server's challenge: SHA-256 of the salt and the secret number 7.
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(`${salt}7`));
  challenge.challenge = Array.from(new Uint8Array(digest), (b) =>
    b.toString(16).padStart(2, '0'),
  ).join('');
});

function signIn(user: { sub: string; email?: string } = { sub: 'user-1' }) {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user,
  });
}

/** LoginProbe stands in for the sign-in page and shows the router state it got. */
function LoginProbe() {
  const location = useLocation();
  return <pre data-testid="login-state">{JSON.stringify(location.state)}</pre>;
}

/** renderOnboarding renders the onboarding next to a stand-in for /login. */
function renderOnboarding(route: string) {
  return renderWithProviders(
    <Routes>
      <Route path="/onboarding" element={<OnboardingPage />} />
      <Route path="/login" element={<LoginProbe />} />
    </Routes>,
    { route },
  );
}

/** followLogin follows the sign-in link and returns the router state of /login. */
function followLogin(link: HTMLElement) {
  fireEvent.click(link);
  return JSON.parse(screen.getByTestId('login-state').textContent ?? 'null');
}

function problem(status: number, detail: string, field?: string) {
  return json({ title: 'Problem', status, detail, ...(field ? { field } : {}) }, status);
}

/** fillSignup fills the signup form and submits it. */
function fillSignup() {
  fireEvent.change(screen.getByLabelText('Organisation'), { target: { value: 'Muster GmbH' } });
  fireEvent.change(screen.getByLabelText('E-Mail-Adresse'), {
    target: { value: 'it@muster.example' },
  });
  fireEvent.click(screen.getByRole('button', { name: 'Konto anlegen' }));
}

/** stubSignup answers the challenge and the signup with the given responses. */
function stubSignup(answers: { challenge?: () => Response; submit?: () => Response }) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith('/signup/challenge')) return answers.challenge?.() ?? json(challenge);
    if (url.endsWith('/signup') && init?.method === 'POST')
      return answers.submit?.() ?? json({ status: 'verification_sent' }, 202);
    return json({ detail: 'unexpected' }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function signupPosted(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls.some(
    (call) => String(call[0]) === '/api/v1/signup' && (call[1] as RequestInit)?.method === 'POST',
  );
}

interface ServerState {
  collectors: number;
  scopes: unknown[];
  cis: number;
  /** resolveFails answers the resolve with a 500 problem (WP-529). */
  resolveFails?: boolean;
}

function stubServer(state: ServerState) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith('/signup/challenge')) return json(challenge);
    if (url.endsWith('/signup') && init?.method === 'POST')
      return json({ status: 'verification_sent' }, 202);
    if (url.endsWith('/collectors/enrollment-codes'))
      return json({ code: 'ABCD-EFGH', expires_at: '2026-10-15T00:00:00Z' }, 201);
    if (url.includes('/review-items/') && init?.method === 'POST') {
      if (state.resolveFails) {
        return json(
          { title: 'Internal Server Error', status: 500, detail: 'scope could not be created' },
          500,
        );
      }
      // A resolved suggestion is no longer open.
      state.scopes = state.scopes.filter(
        (item) => !url.includes(`/review-items/${(item as { id: string }).id}/`),
      );
      return json({ status: 'resolved' });
    }
    if (url.includes('/review-items')) return json(page(state.scopes));
    if (url.includes('/collectors')) return json(page([], state.collectors));
    if (url.includes('/cis')) return json(page([], state.cis));
    return json({ detail: 'unexpected' }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

beforeEach(() => {
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});

describe('currentStep', () => {
  const none = { collectorDone: false, scopesDone: false, cisDone: false };
  it('lets the verification link win over a session (F-351)', () => {
    // A superadmin who pastes the link into a signed-in tab must reach the
    // verification, not the collector step of their own organization.
    expect(currentStep(true, true, none)).toBe('verify');
    expect(currentStep(true, true, { collectorDone: true, scopesDone: true, cisDone: true })).toBe(
      'verify',
    );
  });

  it('orders the steps', () => {
    expect(currentStep(false, false, none)).toBe('signup');
    expect(currentStep(false, true, none)).toBe('verify');
    expect(currentStep(true, false, none)).toBe('collector');
    expect(currentStep(true, false, { ...none, collectorDone: true })).toBe('scopes');
    expect(
      currentStep(true, false, { collectorDone: true, scopesDone: true, cisDone: false }),
    ).toBe('cis');
    expect(currentStep(true, false, { collectorDone: true, scopesDone: true, cisDone: true })).toBe(
      'done',
    );
  });
});

describe('passwordPolicyViolations', () => {
  it('checks the rules of the realm policy like Keycloak', () => {
    expect(passwordPolicyViolations(GOOD_PASSWORD)).toEqual([]);
    expect(passwordPolicyViolations('')).toEqual(['length', 'upper', 'lower', 'digit', 'special']);
    expect(passwordPolicyViolations('Kurz-1a')).toEqual(['length']);
    expect(passwordPolicyViolations('KEIN-KLEINBUCHSTABE-1')).toEqual(['lower']);
    expect(passwordPolicyViolations('OhneSonderzeichen12')).toEqual(['special']);
    // Umlauts are letters; a space counts as a special character.
    expect(passwordPolicyViolations('Größere Übung 12')).toEqual([]);
    // Twelve code points, not UTF-16 units.
    expect(passwordPolicyViolations('Aa1-😀😀😀😀😀😀😀')).toEqual(['length']);
  });
});

describe('OnboardingPage', () => {
  it('signs up with the solved ALTCHA challenge', async () => {
    const fetchMock = stubServer({ collectors: 0, scopes: [], cis: 0 });
    renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '0');
    fireEvent.change(screen.getByLabelText('Organisation'), { target: { value: 'Muster GmbH' } });
    fireEvent.change(screen.getByLabelText('E-Mail-Adresse'), {
      target: { value: 'it@muster.example' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Konto anlegen' }));

    expect(await screen.findByText(/Bestätigungslink an it@muster.example/)).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(
      (call) => String(call[0]) === '/api/v1/signup' && call[1]?.method === 'POST',
    );
    const body = JSON.parse(String(post?.[1]?.body));
    expect(body.organization_name).toBe('Muster GmbH');
    expect(JSON.parse(atob(body.altcha))).toMatchObject({ number: 7, salt, signature: 'sig' });
  });

  it('verifies the link and leads to the sign-in', async () => {
    const fetchMock = vi.fn(async () =>
      json({ organization_slug: 'muster-gmbh', trial_ends_at: '2026-11-13T00:00:00Z' }, 201),
    );
    vi.stubGlobal('fetch', fetchMock);
    renderOnboarding('/onboarding?token=tok-1');

    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '1');
    expect(screen.getByText(/Mindestens 12 Zeichen mit Groß- und Kleinbuchstaben/)).toBeVisible();
    fireEvent.change(screen.getByLabelText(/Passwort festlegen/), {
      target: { value: GOOD_PASSWORD },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Konto bestätigen' }));

    expect(await screen.findByText(/„muster-gmbh“ ist angelegt/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Abmelden' })).toBeNull();
    expect(
      screen.getByText(
        `Ihr Testzeitraum läuft bis ${new Date('2026-11-13T00:00:00Z').toLocaleDateString('de-DE')}.`,
      ),
    ).toBeInTheDocument();
    const login = screen.getByRole('link', { name: 'Jetzt anmelden und fortfahren' });
    expect(login).toHaveAttribute('href', '/login');
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      token: 'tok-1',
      password: GOOD_PASSWORD,
    });
    // A tab without a session can still share the IdP session of another
    // account: the sign-in asks the IdP for a fresh login.
    expect(followLogin(login)).toEqual({ from: '/onboarding', forceLogin: true });
  });

  it('explains an expired verification link', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json({ title: 'Gone', status: 410, detail: 'expired' }, 410)),
    );
    renderWithProviders(<OnboardingPage />, { route: '/onboarding?token=old' });

    fireEvent.change(screen.getByLabelText(/Passwort festlegen/), {
      target: { value: GOOD_PASSWORD },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Konto bestätigen' }));
    expect(await screen.findByText(/Bestätigungslink ist abgelaufen/)).toBeInTheDocument();
  });

  it('checks the realm password policy before sending the verification', async () => {
    const fetchMock = vi.fn(async () => json({ detail: 'unexpected' }, 500));
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<OnboardingPage />, { route: '/onboarding?token=tok-1' });

    // Twelve characters, but no upper-case letter, digit or special character.
    fireEvent.change(screen.getByLabelText(/Passwort festlegen/), {
      target: { value: 'einlangeswort' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Konto bestätigen' }));

    expect(
      await screen.findByText(
        'Das Passwort erfüllt die Richtlinie noch nicht. Es fehlt: ein Großbuchstabe, eine Ziffer, ein Sonderzeichen.',
      ),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(/Passwort festlegen/)).toHaveAttribute('aria-invalid', 'true');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('localizes a password the IdP refuses', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        problem(422, 'Invalid password: must not be equal to the email.', 'password'),
      ),
    );
    renderWithProviders(<OnboardingPage />, { route: '/onboarding?token=tok-1' });

    fireEvent.change(screen.getByLabelText(/Passwort festlegen/), {
      target: { value: GOOD_PASSWORD },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Konto bestätigen' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Das Konto wurde nicht bestätigt. Bitte prüfen Sie das Passwort.',
    );
    expect(screen.getByText(/darf nicht Ihre E-Mail-Adresse sein/)).toBeInTheDocument();
    expect(screen.queryByText(/must not be equal/)).toBeNull();
  });

  it('verifies in a signed-in tab, ends only its session and then offers the sign-in (F-351)', async () => {
    signIn({ sub: 'admin-1', email: 'admin@reticora.example' });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/signup/verify'))
        return json(
          { organization_slug: 'muster-gmbh', trial_ends_at: '2026-11-13T00:00:00Z' },
          201,
        );
      if (url.endsWith('/auth/logout') && init?.method === 'POST')
        return new Response(null, { status: 204 });
      if (url.endsWith('/auth/config'))
        // The IdP has an end-session endpoint, as every real deployment does.
        return json({
          issuer: 'https://id.example/realms/reticora',
          client_id: 'reticora-app',
          redirect_uri: 'https://cmdb.example/auth/callback',
          scopes: ['openid'],
          authorization_endpoint: 'https://id.example/realms/reticora/protocol/openid-connect/auth',
          end_session_endpoint: 'https://id.example/realms/reticora/protocol/openid-connect/logout',
          pkce_required: true,
        });
      if (url.includes('/review-items')) return json(page([]));
      if (url.includes('/collectors')) return json(page([], 1));
      if (url.includes('/cis')) return json(page([], 3));
      return json({ detail: 'unexpected' }, 404);
    });
    vi.stubGlobal('fetch', fetchMock);
    const assign = vi.fn();
    vi.stubGlobal('location', { ...window.location, assign });
    renderOnboarding('/onboarding?token=tok-1');

    // The token wins over the session: the verification, not the collector.
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '1');
    expect(
      screen.getByText(/Dieser Tab ist noch als admin@reticora.example angemeldet/),
    ).toBeVisible();
    fireEvent.change(screen.getByLabelText(/Passwort festlegen/), {
      target: { value: GOOD_PASSWORD },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Konto bestätigen' }));

    expect(await screen.findByText(/„muster-gmbh“ ist angelegt/)).toBeInTheDocument();
    expect(
      screen.getByText(
        /Sie sind noch als admin@reticora.example angemeldet. Melden Sie sich zuerst ab/,
      ),
    ).toBeVisible();
    // Signing in now would reuse the superadmin's session: no sign-in yet.
    expect(screen.queryByRole('link', { name: 'Jetzt anmelden und fortfahren' })).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Abmelden' }));
    expect(
      await screen.findByRole('link', { name: 'Jetzt anmelden und fortfahren' }),
    ).toHaveAttribute('href', '/login');
    expect(
      fetchMock.mock.calls.some(
        (call) => String(call[0]) === '/api/v1/auth/logout' && call[1]?.method === 'POST',
      ),
    ).toBe(true);
    expect(useAuthStore.getState().isAuthenticated()).toBe(false);
    expect(screen.queryByRole('button', { name: 'Abmelden' })).toBeNull();
    // The page stays: no redirect to the IdP's end-session endpoint, whose
    // "logged out" page has no way back to the sign-in with the new account.
    expect(assign).not.toHaveBeenCalled();
    // Only the local session ended: the IdP session of the superadmin is
    // still there, so the sign-in must ask for a fresh login.
    expect(
      followLogin(screen.getByRole('link', { name: 'Jetzt anmelden und fortfahren' })),
    ).toEqual({ from: '/onboarding', forceLogin: true });
  });

  it('says that the server has no self-signup set up (503 on the challenge)', async () => {
    const fetchMock = stubSignup({
      challenge: () => problem(503, 'self-signup is not available'),
    });
    renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    fillSignup();

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Die Registrierung ist auf diesem Server noch nicht eingerichtet: Es ist kein E-Mail-Versand (SMTP) konfiguriert. Wenden Sie sich an den Betreiber.',
    );
    expect(screen.queryByText(/self-signup is not available/)).toBeNull();
    expect(signupPosted(fetchMock)).toBe(false);
  });

  it('tells a passing outage of the signup apart from a missing setup', async () => {
    stubSignup({
      submit: () => problem(503, 'signup protection is unavailable, try again later'),
    });
    renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    fillSignup();

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Die Registrierung ist gerade nicht möglich. Bitte versuchen Sie es in einigen Minuten erneut.',
    );
  });

  it('localizes the per-address limit (429)', async () => {
    stubSignup({
      submit: () => problem(429, 'too many signups from this address, try again later'),
    });
    renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    fillSignup();

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Zu viele Versuche. Bitte warten Sie einige Minuten und versuchen Sie es dann erneut.',
    );
  });

  it('localizes a refused e-mail domain and a failed captcha', async () => {
    stubSignup({
      submit: () =>
        problem(422, 'e-mail addresses of disposable domains are not accepted', 'email'),
    });
    const { unmount } = renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    fillSignup();

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /Diese E-Mail-Domain wird für die Registrierung nicht angenommen/,
    );
    expect(screen.getByLabelText('E-Mail-Adresse')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByLabelText('Organisation')).not.toHaveAttribute('aria-invalid');
    unmount();

    stubSignup({
      submit: () =>
        problem(
          400,
          'the captcha solution is missing, invalid, expired or was already used',
          'altcha',
        ),
    });
    renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    fillSignup();

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Die automatische Sicherheitsprüfung ist fehlgeschlagen oder abgelaufen. Bitte senden Sie das Formular erneut ab.',
    );
  });

  it('keeps the detail of other refusals', async () => {
    stubSignup({ submit: () => problem(500, 'signup failed') });
    renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    fillSignup();

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Die Registrierung ist fehlgeschlagen: signup failed',
    );
  });

  it('asks for a secure connection when the proof of work cannot run', async () => {
    const original = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
    const fetchMock = stubSignup({});
    // Plain HTTP on a host name: crypto.subtle is undefined.
    Object.defineProperty(globalThis, 'crypto', {
      value: { getRandomValues: (values: Uint8Array) => values },
      configurable: true,
    });
    try {
      renderWithProviders(<OnboardingPage />, { route: '/onboarding' });
      fillSignup();
      expect(await screen.findByRole('alert')).toHaveTextContent(
        'Die Anmeldung erfordert eine sichere Verbindung.',
      );
      expect(signupPosted(fetchMock)).toBe(false);
    } finally {
      if (original) Object.defineProperty(globalThis, 'crypto', original);
    }
  });

  it('creates an enrollment code while no collector is connected', async () => {
    signIn();
    stubServer({ collectors: 0, scopes: [], cis: 0 });
    renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    fireEvent.click(await screen.findByRole('button', { name: 'Enrollment-Code erzeugen' }));
    expect(await screen.findByText('ABCD-EFGH')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '2');
  });

  it('confirms a reported scope', async () => {
    signIn();
    const fetchMock = stubServer({
      collectors: 1,
      scopes: [
        { id: 'rev-1', kind: 'scope_suggested', status: 'open', payload: { cidr: '10.0.0.0/24' } },
      ],
      cis: 0,
    });
    renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    expect(await screen.findByText('10.0.0.0/24')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '3');
    fireEvent.click(screen.getByRole('button', { name: 'Bestätigen' }));
    await waitFor(() => {
      const post = fetchMock.mock.calls.find((call) => call[1]?.method === 'POST');
      expect(String(post?.[0])).toBe('/api/v1/review-items/rev-1/resolve');
      expect(JSON.parse(String(post?.[1]?.body))).toEqual({ action: 'accept' });
    });
    // The confirmed scope completes the step: the first CIs are next (F-177).
    await waitFor(() =>
      expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '4'),
    );
    expect(screen.getByText(/Die erste Discovery läuft/)).toBeInTheDocument();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('shows a failed confirmation and keeps the suggestion (WP-529, F-177)', async () => {
    signIn();
    stubServer({
      collectors: 1,
      scopes: [
        { id: 'rev-1', kind: 'scope_suggested', status: 'open', payload: { cidr: '10.0.0.0/24' } },
      ],
      cis: 0,
      resolveFails: true,
    });
    renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    expect(await screen.findByText('10.0.0.0/24')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Bestätigen' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Der Vorschlag wurde nicht übernommen: scope could not be created',
    );
    expect(screen.getByText('10.0.0.0/24')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Bestätigen' })).toBeEnabled();
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '3');
  });

  it('finishes once the first CIs are there', async () => {
    signIn();
    stubServer({ collectors: 1, scopes: [], cis: 12 });
    renderWithProviders(<OnboardingPage />, { route: '/onboarding' });

    expect(await screen.findByText('12 CIs sind bereits in Ihrer CMDB.')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '5');
    expect(screen.getByRole('link', { name: 'Zur CMDB' })).toHaveAttribute('href', '/cmdb');
  });
});
