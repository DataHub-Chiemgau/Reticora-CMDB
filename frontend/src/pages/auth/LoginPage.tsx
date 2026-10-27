import { useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '../../components/ui/Button';
import { startAuthorizationCodeFlow, InsecureContextError } from '../../auth/oidc';

type LoginLocationState = {
  from?: string;
  expired?: boolean;
  /** invitationAccepted is the address of an account the invitation page just created (TLC-03). */
  invitationAccepted?: string;
};

export function LoginPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const state = location.state as LoginLocationState | null;
  const from = state?.from ?? '/dashboard';

  async function handleLogin() {
    setError(null);
    setLoading(true);

    try {
      await startAuthorizationCodeFlow(from);
    } catch (err) {
      if (err instanceof InsecureContextError) {
        setError(t('auth.secureContextRequired'));
      } else if (err instanceof TypeError) {
        // fetch rejects with a TypeError ("Failed to fetch") when the backend
        // is unreachable (INS-04): say so instead of the browser's message.
        setError(t('auth.backendUnreachable'));
      } else {
        setError(err instanceof Error ? err.message : t('auth.loginError'));
      }
      setLoading(false);
    }
  }

  return (
    <section className="mx-auto flex min-h-[60vh] max-w-md flex-col justify-center">
      <div className="rounded-2xl border border-gray-200 bg-white p-8 shadow-sm dark:border-gray-800 dark:bg-gray-900">
        <h2 className="text-2xl font-semibold text-gray-900 dark:text-gray-50">
          {t('auth.loginTitle')}
        </h2>
        <p className="mt-2 text-sm text-gray-600 dark:text-gray-400">
          {t('auth.loginDescription')}
        </p>
        {state?.expired ? (
          <p className="mt-4 rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-950/40 dark:text-amber-200">
            {t('auth.sessionExpired')}
          </p>
        ) : null}
        {state?.invitationAccepted ? (
          <p
            role="status"
            className="mt-4 rounded-lg bg-green-50 p-3 text-sm text-green-800 dark:bg-green-950/40 dark:text-green-200"
          >
            {t('auth.invitationAccepted', { email: state.invitationAccepted })}
          </p>
        ) : null}
        {error ? (
          <p className="mt-4 rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/40 dark:text-red-300">
            {error}
          </p>
        ) : null}
        <Button className="mt-6 w-full" onClick={handleLogin} loading={loading}>
          {t('auth.loginButton')}
        </Button>
        {/* Self-signup has no switch (config.go), so the entry is always
            shown (UI-17, F-351). */}
        <p className="mt-4 text-center text-sm text-gray-600 dark:text-gray-400">
          {t('auth.signupPrompt')}{' '}
          <Link to="/onboarding" className="font-medium text-primary underline">
            {t('auth.signupLink')}
          </Link>
        </p>
      </div>
    </section>
  );
}
