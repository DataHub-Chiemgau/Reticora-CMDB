/**
 * OnboardingPage — the guided first run (UI-17): signup, e-mail
 * verification, collector enrollment, confirmation of the reported scopes
 * and the first CIs, with the progress over all five steps. The first two
 * steps run without a session (the verification link carries the token),
 * the others after sign-in; each later step is done when its data exists.
 */
import { useState } from 'react';
import type { ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ApiError, ciApi, collectorApi, fetchAPI, scopeSuggestionApi } from '../api/client';
import { useAuthStore } from '../auth/authStore';
import { InsecureContextError } from '../auth/oidc';
import { OtherAccountHint, SignOutButton } from '../auth/SignOutButton';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { solveAltcha, type AltchaChallenge } from './auth/altcha';

export const ONBOARDING_STEPS = ['signup', 'verify', 'collector', 'scopes', 'cis'] as const;
export type OnboardingStep = (typeof ONBOARDING_STEPS)[number];

interface StepState {
  collectorDone: boolean;
  scopesDone: boolean;
  cisDone: boolean;
}

/**
 * currentStep is the first open step: the verification while the link's
 * token is present, even in a tab that is still signed in (the link must be
 * redeemable there); otherwise signup without a session, and with one the
 * first of collector, scopes and CIs whose data is missing.
 */
export function currentStep(
  authenticated: boolean,
  hasToken: boolean,
  state: StepState,
): OnboardingStep | 'done' {
  if (hasToken) return 'verify';
  if (!authenticated) return 'signup';
  if (!state.collectorDone) return 'collector';
  if (!state.scopesDone) return 'scopes';
  if (!state.cisDone) return 'cis';
  return 'done';
}

/** ONBOARDING_LOGIN_STATE leads the sign-in after the verification back to the onboarding. */
const ONBOARDING_LOGIN_STATE = { from: '/onboarding' } as const;

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.detail || err.message;
  return err instanceof Error ? err.message : String(err);
}

/** MIN_PASSWORD_LENGTH mirrors the server's check before the IdP applies its policy. */
export const MIN_PASSWORD_LENGTH = 12;

export type PasswordRule = 'length' | 'upper' | 'lower' | 'digit' | 'special';

/**
 * passwordPolicyViolations checks the password policy of the Reticora realm
 * (deploy/keycloak/realm-reticora.json) before the verification is sent: at
 * least 12 characters with an upper-case and a lower-case letter, a digit
 * and a special character, counted like Keycloak (a special character is
 * neither letter nor digit). Keycloak also refuses the e-mail address, the
 * username and recent passwords, which the page does not know; that refusal
 * comes back as a password error.
 */
export function passwordPolicyViolations(password: string): PasswordRule[] {
  const violations: PasswordRule[] = [];
  if (Array.from(password).length < MIN_PASSWORD_LENGTH) violations.push('length');
  if (!/\p{Lu}/u.test(password)) violations.push('upper');
  if (!/\p{Ll}/u.test(password)) violations.push('lower');
  if (!/\p{Nd}/u.test(password)) violations.push('digit');
  if (!/[^\p{L}\p{Nd}]/u.test(password)) violations.push('special');
  return violations;
}

/**
 * SIGNUP_UNAVAILABLE_DETAIL is the problem detail of a server without the
 * prerequisites of the self-signup (signup.ErrUnavailable: no central SMTP
 * server, Keycloak admin client or verify URL).
 */
const SIGNUP_UNAVAILABLE_DETAIL = 'self-signup is not available';

/**
 * ChallengeFailure is a failed GET /signup/challenge, the first request of
 * the signup: its 503 always means the self-signup is not set up, while a
 * 503 of POST /signup can also be the protection's store failing for a
 * moment.
 */
class ChallengeFailure extends Error {
  constructor(readonly reason: unknown) {
    super(reason instanceof Error ? reason.message : String(reason));
    this.name = 'ChallengeFailure';
  }
}

type SignupField = 'organization_name' | 'email' | 'display_name';

interface SignupRefusal {
  message: string;
  field?: SignupField;
}

/** describeSignupError maps a failed signup to a localized text (F-351). */
function describeSignupError(error: unknown, t: TFunction): SignupRefusal {
  const challenge = error instanceof ChallengeFailure;
  const reason = error instanceof ChallengeFailure ? error.reason : error;
  if (reason instanceof InsecureContextError) {
    return { message: t('auth.secureContextRequired') };
  }
  if (!(reason instanceof ApiError)) {
    if (reason instanceof TypeError) {
      // fetch rejects with a TypeError when the server is unreachable.
      return { message: t('auth.backendUnreachable') };
    }
    return { message: t('onboarding.signup.failed', { detail: errorMessage(reason) }) };
  }
  switch (reason.field) {
    case 'altcha':
      return { message: t('onboarding.signup.captchaFailed') };
    case 'email':
      return reason.status === 422
        ? { message: t('onboarding.signup.emailBlocked'), field: 'email' }
        : { message: t('onboarding.signup.emailInvalid'), field: 'email' };
    case 'organization_name':
      return { message: t('onboarding.signup.organizationInvalid'), field: 'organization_name' };
    case 'display_name':
      return { message: t('onboarding.signup.nameTooLong'), field: 'display_name' };
  }
  switch (reason.status) {
    case 429:
      return { message: t('onboarding.signup.rateLimited') };
    case 503:
      return challenge || reason.detail === SIGNUP_UNAVAILABLE_DETAIL
        ? { message: t('onboarding.signup.unavailable') }
        : { message: t('onboarding.signup.temporarilyUnavailable') };
    default:
      return { message: t('onboarding.signup.failed', { detail: errorMessage(reason) }) };
  }
}

interface VerifyRefusal {
  message: string;
  password?: string;
}

/** describeVerifyError maps a failed verification to a localized text. */
function describeVerifyError(error: unknown, t: TFunction): VerifyRefusal {
  if (!(error instanceof ApiError)) {
    if (error instanceof TypeError) return { message: t('auth.backendUnreachable') };
    return { message: t('onboarding.verify.failed', { detail: errorMessage(error) }) };
  }
  if (error.field === 'password') {
    // The IdP refused the password (realm policy, e.g. the e-mail address).
    return {
      message: t('onboarding.verify.notAccepted'),
      password: t('onboarding.verify.passwordRejected'),
    };
  }
  if (error.status === 409) {
    return error.field === 'organization_name'
      ? { message: t('onboarding.verify.organizationTaken') }
      : { message: t('onboarding.verify.accountExists') };
  }
  switch (error.status) {
    case 410:
      return { message: t('onboarding.verify.expired') };
    case 404:
      return { message: t('onboarding.verify.invalid') };
    case 429:
      return { message: t('onboarding.signup.rateLimited') };
    case 503:
      return { message: t('onboarding.verify.unavailable') };
    default:
      return { message: t('onboarding.verify.failed', { detail: errorMessage(error) }) };
  }
}

function Progress({ current }: { current: OnboardingStep | 'done' }) {
  const { t } = useTranslation();
  const index = current === 'done' ? ONBOARDING_STEPS.length : ONBOARDING_STEPS.indexOf(current);
  return (
    <div>
      <div
        role="progressbar"
        aria-label={t('onboarding.progress')}
        aria-valuemin={0}
        aria-valuemax={ONBOARDING_STEPS.length}
        aria-valuenow={index}
        aria-valuetext={t('onboarding.progressText', {
          done: index,
          total: ONBOARDING_STEPS.length,
        })}
        className="h-2 overflow-hidden rounded bg-gray-200 dark:bg-gray-700"
      >
        <div
          className="h-full bg-primary transition-all"
          style={{ width: `${(index / ONBOARDING_STEPS.length) * 100}%` }}
        />
      </div>
      <ol className="mt-3 grid grid-cols-5 gap-2 text-xs">
        {ONBOARDING_STEPS.map((step, i) => (
          <li
            key={step}
            aria-current={step === current ? 'step' : undefined}
            className={
              i < index
                ? 'text-green-700 dark:text-green-400'
                : step === current
                  ? 'font-semibold text-primary'
                  : 'text-muted'
            }
          >
            {i < index ? '✓ ' : `${i + 1}. `}
            {t(`onboarding.steps.${step}`)}
          </li>
        ))}
      </ol>
    </div>
  );
}

function SignupStep() {
  const { t } = useTranslation();
  const [form, setForm] = useState({ organization_name: '', email: '', display_name: '' });
  const submit = useMutation({
    mutationFn: async () => {
      const challenge = await fetchAPI<AltchaChallenge>('/signup/challenge').catch(
        (err: unknown) => {
          throw new ChallengeFailure(err);
        },
      );
      const altcha = await solveAltcha(challenge);
      return fetchAPI<{ status: string }>('/signup', {
        method: 'POST',
        body: JSON.stringify({ ...form, altcha }),
      });
    },
  });
  const refusal = submit.error ? describeSignupError(submit.error, t) : null;
  // The field's message is the alert; the field is only marked.
  const invalid = (field: SignupField) => (refusal?.field === field ? true : undefined);

  if (submit.isSuccess) {
    return (
      <p role="status" className="text-sm">
        {t('onboarding.signup.sent', { email: form.email })}
      </p>
    );
  }
  return (
    <form
      className="space-y-3"
      onSubmit={(event) => {
        event.preventDefault();
        submit.mutate();
      }}
    >
      <Input
        label={t('onboarding.signup.organization')}
        required
        aria-invalid={invalid('organization_name')}
        value={form.organization_name}
        onChange={(e) => setForm({ ...form, organization_name: e.target.value })}
      />
      <Input
        label={t('onboarding.signup.email')}
        type="email"
        required
        aria-invalid={invalid('email')}
        value={form.email}
        onChange={(e) => setForm({ ...form, email: e.target.value })}
      />
      <Input
        label={t('onboarding.signup.name')}
        aria-invalid={invalid('display_name')}
        value={form.display_name}
        onChange={(e) => setForm({ ...form, display_name: e.target.value })}
      />
      {refusal ? (
        <p role="alert" className="text-sm text-danger">
          {refusal.message}
        </p>
      ) : null}
      <Button type="submit" loading={submit.isPending}>
        {t('onboarding.signup.submit')}
      </Button>
    </form>
  );
}

/**
 * SignedInHint warns a tab that is still signed in with another account:
 * the verification works there, but the sign-in afterwards must not meet
 * that account's IdP session, which would sign it in again (F-351).
 */
function SignedInHint({ children }: { children: ReactNode }) {
  return (
    <div className="space-y-2 rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-950/40 dark:text-amber-200">
      {children}
    </div>
  );
}

function VerifyStep({ token, signedInAs }: { token: string; signedInAs?: string }) {
  const { t, i18n } = useTranslation();
  const [password, setPassword] = useState('');
  const [policyError, setPolicyError] = useState<string | null>(null);
  const verify = useMutation({
    mutationFn: () =>
      fetchAPI<{ organization_slug: string; trial_ends_at: string }>('/signup/verify', {
        method: 'POST',
        body: JSON.stringify({ token, password }),
      }),
  });
  const refusal = verify.error ? describeVerifyError(verify.error, t) : null;
  const passwordError = policyError ?? refusal?.password;

  if (verify.isSuccess) {
    return (
      <div role="status" className="space-y-3 text-sm">
        <p>{t('onboarding.verify.done', { slug: verify.data.organization_slug })}</p>
        <p>
          {t('onboarding.verify.trial', {
            date: new Date(verify.data.trial_ends_at).toLocaleDateString(i18n.language),
          })}
        </p>
        {signedInAs ? (
          // Signing in now would reuse the IdP session of the other account:
          // the logout ends it as well and returns to the sign-in page, where
          // the new account signs in with its credentials.
          <SignedInHint>
            <p>{t('onboarding.verify.signOutFirst', { user: signedInAs })}</p>
            <SignOutButton size="sm">{t('onboarding.verify.signOutAndSignIn')}</SignOutButton>
          </SignedInHint>
        ) : (
          // A tab without a Reticora session (a new tab) can still share the
          // IdP session of an account signed in in another tab.
          <>
            <Link
              to="/login"
              state={ONBOARDING_LOGIN_STATE}
              className="inline-block font-medium text-primary underline"
            >
              {t('onboarding.verify.login')}
            </Link>
            <OtherAccountHint />
          </>
        )}
      </div>
    );
  }
  return (
    <form
      className="space-y-3"
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        const violations = passwordPolicyViolations(password);
        if (violations.length > 0) {
          setPolicyError(
            t('onboarding.verify.policyNotMet', {
              rules: violations
                .map((rule) => t(`onboarding.verify.rules.${rule}`, { min: MIN_PASSWORD_LENGTH }))
                .join(', '),
            }),
          );
          return;
        }
        setPolicyError(null);
        verify.mutate();
      }}
    >
      {signedInAs ? (
        <SignedInHint>
          <p>{t('onboarding.verify.signedIn', { user: signedInAs })}</p>
        </SignedInHint>
      ) : null}
      <Input
        id="onboarding-password"
        label={t('onboarding.verify.password')}
        type="password"
        autoComplete="new-password"
        required
        value={password}
        error={passwordError}
        aria-describedby={
          passwordError
            ? 'onboarding-password-error onboarding-password-policy'
            : 'onboarding-password-policy'
        }
        onChange={(e) => {
          setPassword(e.target.value);
          setPolicyError(null);
        }}
      />
      <p id="onboarding-password-policy" className="text-xs text-muted">
        {t('onboarding.verify.policy', { min: MIN_PASSWORD_LENGTH })}
      </p>
      {refusal ? (
        <p role="alert" className="text-sm text-danger">
          {refusal.message}
        </p>
      ) : null}
      <Button type="submit" loading={verify.isPending}>
        {t('onboarding.verify.submit')}
      </Button>
    </form>
  );
}

function CollectorStep() {
  const { t } = useTranslation();
  const code = useMutation({
    mutationFn: () =>
      fetchAPI<{ code: string; expires_at: string }>('/collectors/enrollment-codes', {
        method: 'POST',
        body: JSON.stringify({ label: 'onboarding' }),
      }),
  });
  return (
    <div className="space-y-3 text-sm">
      <p>{t('onboarding.collector.hint')}</p>
      {code.data ? (
        <div>
          <p className="text-gray-600 dark:text-gray-300">{t('onboarding.collector.code')}</p>
          <code className="block rounded bg-gray-100 p-3 font-mono text-base dark:bg-gray-800">
            {code.data.code}
          </code>
          <p className="mt-2 text-muted">{t('onboarding.collector.waiting')}</p>
        </div>
      ) : (
        <Button onClick={() => code.mutate()} loading={code.isPending}>
          {t('onboarding.collector.create')}
        </Button>
      )}
      {code.error ? (
        <p role="alert" className="text-danger">
          {errorMessage(code.error)}
        </p>
      ) : null}
    </div>
  );
}

/**
 * ScopesStep lists the networks the collector reported. A confirmed network
 * completes the step (ABN-01 d, F-177); further suggestions stay open in the
 * review inbox. A failed resolve shows the server's message and keeps the
 * suggestion.
 */
function ScopesStep({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const suggestions = useQuery({
    queryKey: ['onboarding', 'scopes'],
    queryFn: () => scopeSuggestionApi.list(),
  });
  const resolve = useMutation({
    mutationFn: ({ id, action }: { id: string; action: 'accept' | 'dismiss' }) =>
      scopeSuggestionApi.resolve(id, action),
    onSuccess: (_data, { action }) => {
      if (action === 'accept') onDone();
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['onboarding'] }),
  });
  const items = suggestions.data?.data ?? [];
  return (
    <div className="space-y-3 text-sm">
      <p>{t('onboarding.scopes.hint')}</p>
      {resolve.error ? (
        <p role="alert" className="text-danger">
          {t('onboarding.scopes.resolveFailed', { message: errorMessage(resolve.error) })}
        </p>
      ) : null}
      {items.length === 0 ? (
        <p className="text-muted">{t('onboarding.scopes.waiting')}</p>
      ) : (
        <ul className="space-y-2">
          {items.map((item) => (
            <li key={item.id} className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-mono">{item.payload.cidr}</span>
              <span className="flex gap-2">
                <Button
                  size="sm"
                  disabled={resolve.isPending}
                  onClick={() => resolve.mutate({ id: item.id, action: 'accept' })}
                >
                  {t('onboarding.scopes.accept')}
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={resolve.isPending}
                  onClick={() => resolve.mutate({ id: item.id, action: 'dismiss' })}
                >
                  {t('onboarding.scopes.dismiss')}
                </Button>
              </span>
            </li>
          ))}
        </ul>
      )}
      <Button variant="ghost" size="sm" onClick={onDone}>
        {t('onboarding.scopes.continue')}
      </Button>
    </div>
  );
}

export function OnboardingPage() {
  const { t } = useTranslation();
  const [params] = useSearchParams();
  const token = params.get('token') ?? '';
  const authenticated = useAuthStore((s) => s.isAuthenticated());
  const user = useAuthStore((s) => s.user);
  const signedInAs = authenticated ? (user?.email ?? user?.name ?? user?.sub) : undefined;
  const [scopesConfirmed, setScopesConfirmed] = useState(false);

  // Collector and CIs are polled so the flow advances on its own.
  const collectors = useQuery({
    queryKey: ['onboarding', 'collectors'],
    queryFn: () => collectorApi.list({ limit: 1 }),
    enabled: authenticated,
    refetchInterval: 10_000,
  });
  const scopes = useQuery({
    queryKey: ['onboarding', 'scopes'],
    queryFn: () => scopeSuggestionApi.list(),
    enabled: authenticated,
    refetchInterval: 10_000,
  });
  const cis = useQuery({
    queryKey: ['onboarding', 'cis'],
    queryFn: () => ciApi.list({ limit: 1 }),
    enabled: authenticated,
    refetchInterval: 10_000,
  });

  const step = currentStep(authenticated, token !== '', {
    collectorDone: (collectors.data?.total ?? 0) > 0,
    // The scopes are confirmed once one is accepted (or the step skipped),
    // or none is open any more and the first CIs are there.
    scopesDone:
      scopesConfirmed ||
      (scopes.isSuccess && (scopes.data?.data.length ?? 0) === 0 && (cis.data?.total ?? 0) > 0),
    cisDone: (cis.data?.total ?? 0) > 0,
  });

  return (
    <section className="mx-auto max-w-2xl space-y-4">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
          {t('onboarding.title')}
        </h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('onboarding.subtitle')}</p>
      </div>
      <Progress current={step} />
      <Card title={step === 'done' ? t('onboarding.done.title') : t(`onboarding.steps.${step}`)}>
        {step === 'signup' ? <SignupStep /> : null}
        {step === 'verify' ? <VerifyStep token={token} signedInAs={signedInAs} /> : null}
        {step === 'collector' ? <CollectorStep /> : null}
        {step === 'scopes' ? <ScopesStep onDone={() => setScopesConfirmed(true)} /> : null}
        {step === 'cis' ? <p className="text-sm">{t('onboarding.cis.waiting')}</p> : null}
        {step === 'done' ? (
          <div className="space-y-3 text-sm">
            <p>{t('onboarding.done.text', { count: cis.data?.total ?? 0 })}</p>
            <Link to="/cmdb" className="font-medium text-primary underline">
              {t('onboarding.done.open')}
            </Link>
          </div>
        ) : null}
      </Card>
    </section>
  );
}
