/**
 * InvitationAcceptPage (TLC-03; F-142, WP-510): the public page of the
 * invitation link. Success leads to the login, an expired or used link asks
 * for a renewal, a short password is refused at the field, 429 asks the
 * invitee to wait.
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/InvitationAcceptPage.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import { renderWithProviders } from '../test/utils';
import { InvitationAcceptPage } from './InvitationAcceptPage';
import { LoginPage } from './auth/LoginPage';

const TOKEN = '0b0e7b9e-5f0c-4b4e-9a53-0d5b3c1f2a10.secret';

function respond(status: number, body: Record<string, unknown>) {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json',
    },
  });
}

/** stubAccept answers POST /invitations/accept with `answer` and records the requests. */
function stubAccept(answer: () => Response) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString();
    if (url.endsWith('/invitations/accept') && init?.method === 'POST') return answer();
    return respond(404, { detail: `unexpected request: ${url}` });
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function renderPage(route = `/invitations/accept?token=${encodeURIComponent(TOKEN)}`) {
  return renderWithProviders(
    <Routes>
      <Route path="/invitations/accept" element={<InvitationAcceptPage />} />
      <Route path="/login" element={<LoginPage />} />
    </Routes>,
    { route },
  );
}

function fill(displayName: string, password: string) {
  fireEvent.change(screen.getByLabelText('Anzeigename'), { target: { value: displayName } });
  fireEvent.change(screen.getByLabelText('Passwort (mindestens 12 Zeichen)'), {
    target: { value: password },
  });
  fireEvent.click(screen.getByRole('button', { name: 'Konto anlegen' }));
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('InvitationAcceptPage', () => {
  it('accepts with display name and password and leads to the login', async () => {
    const fetchMock = stubAccept(() =>
      respond(201, {
        user_id: 'u-9',
        organization_id: 'org-1',
        email: 'new@example.invalid',
        account_created: true,
      }),
    );
    renderPage();
    fill('Neue Kollegin', 'Correct-Horse-42');

    expect(
      await screen.findByText(
        'Ihr Konto für new@example.invalid ist angelegt. Melden Sie sich jetzt an.',
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Anmelden' })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [, init] = fetchMock.mock.calls[0]!;
    expect(JSON.parse(String((init as RequestInit).body))).toEqual({
      token: TOKEN,
      display_name: 'Neue Kollegin',
      password: 'Correct-Horse-42',
    });
  });

  it('asks for a renewal when the link has expired (410) and hides the form', async () => {
    stubAccept(() => respond(410, { title: 'Gone', detail: 'invitation expired' }));
    renderPage();
    fill('Spät', 'Correct-Horse-42');

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Dieser Einladungslink ist abgelaufen. Bitten Sie die Person, die Sie eingeladen hat, die Einladung zu erneuern.',
    );
    expect(screen.queryByRole('button', { name: 'Konto anlegen' })).toBeNull();
    expect(screen.getByRole('link', { name: 'Zur Anmeldung' })).toBeInTheDocument();
  });

  it('says that a used link (404) cannot be used again', async () => {
    stubAccept(() => respond(404, { title: 'Not Found', detail: 'invitation not found' }));
    renderPage();
    fill('Zweiter Versuch', 'Correct-Horse-42');

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Dieser Einladungslink ist ungültig oder wurde bereits verwendet. Bitten Sie um eine neue Einladung.',
    );
    expect(screen.queryByRole('button', { name: 'Konto anlegen' })).toBeNull();
  });

  it('refuses a too short password at the field without calling the server', async () => {
    const fetchMock = stubAccept(() => respond(201, {}));
    renderPage();
    fill('Kurz', 'short-pw');

    expect(
      await screen.findByText('Das Passwort muss mindestens 12 Zeichen haben.'),
    ).toBeInTheDocument();
    expect(screen.getByLabelText('Passwort (mindestens 12 Zeichen)')).toHaveAttribute(
      'aria-invalid',
      'true',
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('shows the server refusal of a short password at the field', async () => {
    stubAccept(() =>
      respond(400, {
        type: 'urn:reticora:problem:invalid-field',
        title: 'Bad Request',
        detail: 'password: password must have at least 12 characters',
        field: 'password',
      }),
    );
    renderPage();
    // Twelve characters pass the page; the server applies its own rule.
    fill('Server', 'abcdefghijkl');

    expect(
      await screen.findByText('Das Passwort muss mindestens 12 Zeichen haben.'),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Konto anlegen' })).toBeInTheDocument();
  });

  it('asks the invitee to wait after 429 and keeps the form', async () => {
    stubAccept(() => respond(429, { title: 'Too Many Requests', detail: 'rate limit exceeded' }));
    renderPage();
    fill('Eilig', 'Correct-Horse-42');

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Zu viele Versuche. Bitte warten Sie einige Minuten und versuchen Sie es dann erneut.',
    );
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Konto anlegen' })).not.toBeDisabled(),
    );
  });

  it('explains an incomplete link without a token and offers no form', () => {
    stubAccept(() => respond(201, {}));
    renderPage('/invitations/accept');

    expect(screen.getByRole('alert')).toHaveTextContent('Der Einladungslink ist unvollständig.');
    expect(screen.queryByRole('button', { name: 'Konto anlegen' })).toBeNull();
  });
});
