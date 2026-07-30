import { useEffect, useRef, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { clearAuthTransaction, completeAuthorizationCodeFlow } from '../../auth/oidc';

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
