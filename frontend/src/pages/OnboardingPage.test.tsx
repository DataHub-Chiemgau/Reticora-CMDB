/**
 * Tests for the onboarding flow (UI-17): signup with the ALTCHA proof of
 * work, verification by link, collector enrollment, scope confirmation and
 * the first CIs, with the progress over all steps.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { OnboardingPage, currentStep } from './OnboardingPage';
import { useAuthStore } from '../auth/authStore';

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

function signIn() {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
}

interface ServerState {
  collectors: number;
  scopes: unknown[];
  cis: number;
}

function stubServer(state: ServerState) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith('/signup/challenge')) return json(challenge);
    if (url.endsWith('/signup') && init?.method === 'POST')
      return json({ status: 'verification_sent' }, 202);
    if (url.endsWith('/collectors/enrollment-codes'))
      return json({ code: 'ABCD-EFGH', expires_at: '2026-10-15T00:00:00Z' }, 201);
    if (url.includes('/review-items/') && init?.method === 'POST')
      return json({ status: 'resolved' });
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
});

describe('currentStep', () => {
  const none = { collectorDone: false, scopesDone: false, cisDone: false };
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
    renderWithProviders(<OnboardingPage />, { route: '/onboarding?token=tok-1' });

    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '1');
    fireEvent.change(screen.getByLabelText(/Passwort festlegen/), {
      target: { value: 'ein-langes-passwort' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Konto bestätigen' }));

    expect(await screen.findByText(/„muster-gmbh“ ist angelegt/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Jetzt anmelden und fortfahren' })).toHaveAttribute(
      'href',
      '/login',
    );
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      token: 'tok-1',
      password: 'ein-langes-passwort',
    });
  });

  it('explains an expired verification link', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json({ title: 'Gone', status: 410, detail: 'expired' }, 410)),
    );
    renderWithProviders(<OnboardingPage />, { route: '/onboarding?token=old' });

    fireEvent.change(screen.getByLabelText(/Passwort festlegen/), {
      target: { value: 'ein-langes-passwort' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Konto bestätigen' }));
    expect(await screen.findByText(/Bestätigungslink ist abgelaufen/)).toBeInTheDocument();
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
