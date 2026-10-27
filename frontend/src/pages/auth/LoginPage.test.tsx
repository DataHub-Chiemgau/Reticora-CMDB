/**
 * Login error images of INS-04: an unreachable backend and an insecure
 * origin produce an explanation, not the browser's raw error. The entry to
 * the self-signup is always there (UI-17, F-351).
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';

const start = vi.fn();
vi.mock('../../auth/oidc', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../auth/oidc')>();
  return { ...actual, startAuthorizationCodeFlow: (...args: unknown[]) => start(...args) };
});

const { LoginPage } = await import('./LoginPage');
const { InsecureContextError } = await import('../../auth/oidc');

afterEach(() => {
  start.mockReset();
});

describe('LoginPage', () => {
  it('explains an unreachable backend instead of "Failed to fetch"', async () => {
    start.mockRejectedValue(new TypeError('Failed to fetch'));
    renderWithProviders(<LoginPage />, { route: '/login' });

    fireEvent.click(screen.getByRole('button'));
    expect(await screen.findByText(/Der Server ist nicht erreichbar/)).toBeInTheDocument();
    expect(screen.queryByText('Failed to fetch')).toBeNull();
  });

  it('always offers the self-signup (UI-17, F-351)', () => {
    renderWithProviders(<LoginPage />, { route: '/login' });

    expect(screen.getByText(/Noch kein Konto\?/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Konto anlegen' })).toHaveAttribute(
      'href',
      '/onboarding',
    );
  });

  it('asks for a secure connection on an insecure origin', async () => {
    start.mockRejectedValue(new InsecureContextError());
    renderWithProviders(<LoginPage />, { route: '/login' });

    fireEvent.click(screen.getByRole('button'));
    expect(await screen.findByText(/erfordert eine sichere Verbindung/)).toBeInTheDocument();
  });
});
