import { useEffect, useState } from 'react';
import { Route, Routes } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { InsecureContextError, startAuthorizationCodeFlow } from '../auth/oidc';
import { AuthCard } from '../components/shell/AuthCard';
import { Button } from '../components/ui/Button';
import { PageHeader } from '../components/ui/PageHeader';
import { isSessionValid, useOperatorAuth } from './operatorAuth';
import type { OperatorSession } from './operatorAuth';

/**
 * OperatorApp is the operator area of the SPA under /operator/* (PKG-07,
 * SEC-07, WP-887). It runs outside the tenant login (RequireAuth): without a
 * valid operator session it shows the operator login; the session lasts until
 * expires_at (15 minutes, no refresh), then the area asks for a new login.
 * Signing out ends only the operator session; a tenant session stays.
 */
export function OperatorApp() {
  const session = useOperatorAuth((s) => s.session);
  const clearSession = useOperatorAuth((s) => s.clearSession);
  const [expired, setExpired] = useState(false);

  // End the session at expires_at without a request.
  useEffect(() => {
    if (!session) return undefined;
    const left = Date.parse(session.expiresAt) - Date.now();
    if (left <= 0) {
      clearSession();
      setExpired(true);
      return undefined;
    }
    const timer = window.setTimeout(() => {
      clearSession();
      setExpired(true);
    }, left);
    return () => window.clearTimeout(timer);
  }, [session, clearSession]);

  if (!isSessionValid(session)) {
    return <OperatorLogin expired={expired} />;
  }
  return <OperatorShell session={session as OperatorSession} onSignOut={clearSession} />;
}

function OperatorLogin({ expired }: { expired: boolean }) {
  const { t } = useTranslation();
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function signIn() {
    setPending(true);
    setError(null);
    try {
      await startAuthorizationCodeFlow('/operator', { kind: 'operator' });
    } catch (err) {
      setPending(false);
      setError(err instanceof InsecureContextError ? err.message : t('operator.login.startFailed'));
    }
  }

  return (
    <AuthCard title={t('operator.login.title')} description={t('operator.login.description')}>
      {expired && (
        <p role="status" className="mt-4 text-sm text-fg-muted">
          {t('operator.login.expired')}
        </p>
      )}
      {error && (
        <p
          role="alert"
          className="mt-4 rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-900 dark:border-red-500/40 dark:bg-red-500/10 dark:text-red-100"
        >
          {error}
        </p>
      )}
      <Button className="mt-6 w-full" onClick={signIn} disabled={pending}>
        {t('operator.login.submit')}
      </Button>
    </AuthCard>
  );
}

function OperatorShell({
  session,
  onSignOut,
}: {
  session: OperatorSession;
  onSignOut: () => void;
}) {
  const { t } = useTranslation();
  const who = session.operator.name || session.operator.email || session.operator.id;
  return (
    <div className="mx-auto max-w-6xl space-y-6 p-6">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 pb-4 dark:border-gray-800">
        <p className="text-sm font-semibold">{t('operator.shell.title')}</p>
        <div className="flex items-center gap-3 text-sm">
          <span>{t('operator.shell.signedInAs', { name: who })}</span>
          <Button variant="secondary" size="sm" onClick={onSignOut}>
            {t('operator.shell.signOut')}
          </Button>
        </div>
      </header>
      <main id="main-content">
        <Routes>
          <Route
            index
            element={
              <PageHeader
                title={t('operator.home.title')}
                description={t('operator.home.description')}
              />
            }
          />
          <Route path="*" element={<PageHeader title={t('operator.home.notFound')} />} />
        </Routes>
      </main>
    </div>
  );
}
