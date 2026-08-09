import { useState } from 'react';
import { useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '../../components/ui/Button';
import { startAuthorizationCodeFlow } from '../../auth/oidc';

type LoginLocationState = {
  from?: string;
  expired?: boolean;
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
      if (err instanceof Error && err.message.startsWith('The Web Crypto API is not available')) {
        setError(t('auth.secureContextRequired'));
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
        {error ? (
          <p className="mt-4 rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/40 dark:text-red-300">
            {error}
          </p>
        ) : null}
        <Button className="mt-6 w-full" onClick={handleLogin} loading={loading}>
          {t('auth.loginButton')}
        </Button>
      </div>
    </section>
  );
}
