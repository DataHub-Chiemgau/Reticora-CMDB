/**
 * OnboardingPage — the guided first run (UI-17): signup, e-mail
 * verification, collector enrollment, confirmation of the reported scopes
 * and the first CIs, with the progress over all five steps. The first two
 * steps run without a session (the verification link carries the token),
 * the others after sign-in; each later step is done when its data exists.
 */
import { useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { ApiError, ciApi, collectorApi, fetchAPI, scopeSuggestionApi } from '../api/client';
import { useAuthStore } from '../auth/authStore';
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
 * currentStep is the first open step: without a session signup (or the
 * verification when the link's token is present), with one the first of
 * collector, scopes and CIs whose data is missing.
 */
export function currentStep(
  authenticated: boolean,
  hasToken: boolean,
  state: StepState,
): OnboardingStep | 'done' {
  if (!authenticated) return hasToken ? 'verify' : 'signup';
  if (!state.collectorDone) return 'collector';
  if (!state.scopesDone) return 'scopes';
  if (!state.cisDone) return 'cis';
  return 'done';
}

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.detail || err.message;
  return err instanceof Error ? err.message : String(err);
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
                  : 'text-gray-500'
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
      const challenge = await fetchAPI<AltchaChallenge>('/signup/challenge');
      const altcha = await solveAltcha(challenge);
      return fetchAPI<{ status: string }>('/signup', {
        method: 'POST',
        body: JSON.stringify({ ...form, altcha }),
      });
    },
  });

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
        value={form.organization_name}
        onChange={(e) => setForm({ ...form, organization_name: e.target.value })}
      />
      <Input
        label={t('onboarding.signup.email')}
        type="email"
        required
        value={form.email}
        onChange={(e) => setForm({ ...form, email: e.target.value })}
      />
      <Input
        label={t('onboarding.signup.name')}
        value={form.display_name}
        onChange={(e) => setForm({ ...form, display_name: e.target.value })}
      />
      {submit.error ? (
        <p role="alert" className="text-sm text-red-600">
          {errorMessage(submit.error)}
        </p>
      ) : null}
      <Button type="submit" loading={submit.isPending}>
        {t('onboarding.signup.submit')}
      </Button>
    </form>
  );
}

function VerifyStep({ token }: { token: string }) {
  const { t, i18n } = useTranslation();
  const [password, setPassword] = useState('');
  const verify = useMutation({
    mutationFn: () =>
      fetchAPI<{ organization_slug: string; trial_ends_at: string }>('/signup/verify', {
        method: 'POST',
        body: JSON.stringify({ token, password }),
      }),
  });

  if (verify.isSuccess) {
    return (
      <div role="status" className="space-y-3 text-sm">
        <p>{t('onboarding.verify.done', { slug: verify.data.organization_slug })}</p>
        <p>
          {t('onboarding.verify.trial', {
            date: new Date(verify.data.trial_ends_at).toLocaleDateString(i18n.language),
          })}
        </p>
        <Link
          to="/login"
          state={{ from: '/onboarding' }}
          className="inline-block font-medium text-primary underline"
        >
          {t('onboarding.verify.login')}
        </Link>
      </div>
    );
  }
  return (
    <form
      className="space-y-3"
      onSubmit={(event) => {
        event.preventDefault();
        verify.mutate();
      }}
    >
      <Input
        label={t('onboarding.verify.password')}
        type="password"
        autoComplete="new-password"
        minLength={12}
        required
        value={password}
        onChange={(e) => setPassword(e.target.value)}
      />
      {verify.error ? (
        <p role="alert" className="text-sm text-red-600">
          {verify.error instanceof ApiError && verify.error.status === 410
            ? t('onboarding.verify.expired')
            : verify.error instanceof ApiError && verify.error.status === 404
              ? t('onboarding.verify.invalid')
              : errorMessage(verify.error)}
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
          <p className="mt-2 text-gray-500">{t('onboarding.collector.waiting')}</p>
        </div>
      ) : (
        <Button onClick={() => code.mutate()} loading={code.isPending}>
          {t('onboarding.collector.create')}
        </Button>
      )}
      {code.error ? (
        <p role="alert" className="text-red-600">
          {errorMessage(code.error)}
        </p>
      ) : null}
    </div>
  );
}

function ScopesStep({ onSkip }: { onSkip: () => void }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const suggestions = useQuery({
    queryKey: ['onboarding', 'scopes'],
    queryFn: () => scopeSuggestionApi.list(),
  });
  const resolve = useMutation({
    mutationFn: ({ id, action }: { id: string; action: 'accept' | 'dismiss' }) =>
      scopeSuggestionApi.resolve(id, action),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['onboarding'] }),
  });
  const items = suggestions.data?.data ?? [];
  return (
    <div className="space-y-3 text-sm">
      <p>{t('onboarding.scopes.hint')}</p>
      {items.length === 0 ? (
        <p className="text-gray-500">{t('onboarding.scopes.waiting')}</p>
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
      <Button variant="ghost" size="sm" onClick={onSkip}>
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
    // The scopes are confirmed once none is open any more (or skipped).
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
        {step === 'verify' ? <VerifyStep token={token} /> : null}
        {step === 'collector' ? <CollectorStep /> : null}
        {step === 'scopes' ? <ScopesStep onSkip={() => setScopesConfirmed(true)} /> : null}
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
