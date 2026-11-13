import { useEffect, useRef, useState } from 'react';
import { useLocation, useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';
import { AuthCard } from '../../components/shell/AuthCard';
import {
  AuthCallbackError,
  clearAuthTransaction,
  completeAuthorizationCodeFlow,
  pendingAuthKind,
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
    // An operator login (WP-887) ends at the operator callback; its refusal
    // names no cause (not in the group operators or without a second factor).
    const kind = pendingAuthKind();
    completeAuthorizationCodeFlow(location.search)
      .then(({ returnTo }) => navigate(returnTo || '/dashboard', { replace: true }))
      .catch((err) => {
        clearAuthTransaction();
        if (kind === 'operator') {
          const unavailable = err instanceof AuthCallbackError && err.status === 503;
          setError(t(unavailable ? 'operator.login.unavailable' : 'operator.login.refused'));
          return;
        }
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
    <AuthCard title={t('auth.callbackTitle')}>
      {error ? (
        <p
          role="alert"
          className="mt-4 rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-900 dark:border-red-500/40 dark:bg-red-500/10 dark:text-red-100"
        >
          {error}
        </p>
      ) : (
        <p role="status" className="mt-2 flex items-center gap-2.5 text-sm text-fg-muted">
          <span
            aria-hidden="true"
            className="h-4 w-4 animate-spin rounded-full border-2 border-accent border-t-transparent"
          />
          {t('auth.callbackProgress')}
        </p>
      )}
    </AuthCard>
  );
}
