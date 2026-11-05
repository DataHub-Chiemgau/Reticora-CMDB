import { useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AuthCard } from '../../components/shell/AuthCard';
import { Button } from '../../components/ui/Button';
import { startAuthorizationCodeFlow, InsecureContextError } from '../../auth/oidc';
import { OtherAccountHint } from '../../auth/SignOutButton';

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
    <AuthCard title={t('auth.loginTitle')} description={t('auth.loginDescription')}>
      {state?.expired ? (
        <p
          role="status"
          className="mt-5 rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-100"
        >
          {t('auth.sessionExpired')}
        </p>
      ) : null}
      {state?.invitationAccepted ? (
        <>
          <p
            role="status"
            className="mt-5 rounded-lg border border-green-300 bg-green-50 p-3 text-sm text-green-900 dark:border-green-500/40 dark:bg-green-500/10 dark:text-green-100"
          >
            {t('auth.invitationAccepted', { email: state.invitationAccepted })}
          </p>
          {/* The new account must not meet the IdP session of another
              account in this browser (F-351). */}
          <div className="mt-3">
            <OtherAccountHint />
          </div>
        </>
      ) : null}
      {error ? (
        <p
          role="alert"
          className="mt-5 rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-900 dark:border-red-500/40 dark:bg-red-500/10 dark:text-red-100"
        >
          {error}
        </p>
      ) : null}
      <Button size="lg" className="mt-6 w-full" onClick={handleLogin} loading={loading}>
        {t('auth.loginButton')}
      </Button>
      {/* Self-signup has no switch (config.go), so the entry is always
          shown (UI-17, F-351). */}
      <p className="mt-5 text-center text-sm text-fg-muted">
        {t('auth.signupPrompt')}{' '}
        <Link to="/onboarding" className="font-medium text-accent underline underline-offset-2">
          {t('auth.signupLink')}
        </Link>
      </p>
    </AuthCard>
  );
}
