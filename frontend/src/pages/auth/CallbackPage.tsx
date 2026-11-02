import { useEffect, useRef, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  AuthCallbackError,
  clearAuthTransaction,
  completeAuthorizationCodeFlow,
} from '../../auth/oidc';

// The refused logins the server names by problem type (F-197): each has its
// own localized text instead of the server's detail.
const REFUSAL_KEYS: Record<string, string> = {
  'https://reticora.io/problems/login-not-admitted': 'auth.callbackRefused.notAdmitted',
  'https://reticora.io/problems/login-no-organization': 'auth.callbackRefused.noOrganization',
  'https://reticora.io/problems/login-user-inactive': 'auth.callbackRefused.userInactive',
  'https://reticora.io/problems/login-no-role': 'auth.callbackRefused.noRole',
  'https://reticora.io/problems/entitlement-limit': 'auth.callbackRefused.userLimit',
};

export function CallbackPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const processed = useRef(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (processed.current) {
      return;
    }

    processed.current = true;
    completeAuthorizationCodeFlow(location.search)
      .then(({ returnTo }) => navigate(returnTo || '/dashboard', { replace: true }))
      .catch((err) => {
        clearAuthTransaction();
        if (err instanceof AuthCallbackError) {
          // Any other answer of the server is shown as the general error;
          // its detail is technical.
          const key = err.problemType ? REFUSAL_KEYS[err.problemType] : undefined;
          setError(t(key ?? 'auth.callbackError'));
          return;
        }
        setError(err instanceof Error ? err.message : t('auth.callbackError'));
      });
  }, [location.search, navigate, t]);

  return (
    <section className="mx-auto flex min-h-[60vh] max-w-md flex-col justify-center text-center">
      <div className="rounded-2xl border border-gray-200 bg-white p-8 shadow-sm dark:border-gray-800 dark:bg-gray-900">
        <h2 className="text-xl font-semibold text-gray-900 dark:text-gray-50">
          {t('auth.callbackTitle')}
        </h2>
        {error ? (
          <p className="mt-4 rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/40 dark:text-red-300">
            {error}
          </p>
        ) : (
          <p className="mt-2 text-sm text-gray-600 dark:text-gray-400">
            {t('auth.callbackProgress')}
          </p>
        )}
      </div>
    </section>
  );
}
