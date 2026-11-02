/**
 * InvitationAcceptPage — the public page the link of the invitation mail
 * opens (TLC-03, F-142): /invitations/accept?token=…. The invitee chooses a
 * display name and a password; POST /invitations/accept creates the account
 * with the invited role and scope, then the page leads to the login. The
 * token is the credential, so the page runs without a session.
 */
import { useState } from 'react';
import type { FormEvent } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ApiError } from '../api/client';
import { useAcceptInvitation } from '../api/hooks';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';

/** MIN_PASSWORD_LENGTH mirrors the server's check before the IdP applies its policy. */
export const MIN_PASSWORD_LENGTH = 12;
const MAX_DISPLAY_NAME_LENGTH = 200;

type AcceptField = 'display_name' | 'password';

/**
 * A refusal as the page shows it. `final` marks a link that can no longer be
 * used (expired, used, revoked): the form is hidden and the invitee is asked
 * for a renewal.
 */
interface Refusal {
  message: string;
  fields: Partial<Record<AcceptField, string>>;
  final: boolean;
}

function describeRefusal(error: unknown, t: TFunction): Refusal {
  if (!(error instanceof ApiError)) {
    const detail = error instanceof Error ? error.message : String(error);
    return { message: t('invitationAccept.errors.failed', { detail }), fields: {}, final: false };
  }
  if (error.field === 'password') {
    return {
      message: t('invitationAccept.errors.notAccepted'),
      fields: {
        password: t('invitationAccept.errors.passwordTooShort', { min: MIN_PASSWORD_LENGTH }),
      },
      final: false,
    };
  }
  if (error.field === 'display_name') {
    return {
      message: t('invitationAccept.errors.notAccepted'),
      fields: {
        display_name: t('invitationAccept.errors.displayNameTooLong', {
          max: MAX_DISPLAY_NAME_LENGTH,
        }),
      },
      final: false,
    };
  }
  switch (error.status) {
    case 410:
      return { message: t('invitationAccept.errors.expired'), fields: {}, final: true };
    case 404:
      return { message: t('invitationAccept.errors.invalid'), fields: {}, final: true };
    case 409:
      return { message: t('invitationAccept.errors.userExists'), fields: {}, final: true };
    case 429:
      return { message: t('invitationAccept.errors.rateLimited'), fields: {}, final: false };
    case 403:
      return { message: t('invitationAccept.errors.userLimit'), fields: {}, final: false };
    case 503:
      return { message: t('invitationAccept.errors.unavailable'), fields: {}, final: false };
    default:
      return {
        message: t('invitationAccept.errors.failed', { detail: error.message }),
        fields: {},
        final: false,
      };
  }
}

export function InvitationAcceptPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const token = params.get('token')?.trim() ?? '';
  const accept = useAcceptInvitation();
  const [form, setForm] = useState({ display_name: '', password: '' });
  const [refusal, setRefusal] = useState<Refusal | null>(null);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    // The password is checked here as on the server; without the IdP admin
    // client the server needs none (the invitee signs in with an existing
    // IdP account), so an empty password is left to the server.
    if (form.password && form.password.length < MIN_PASSWORD_LENGTH) {
      setRefusal({
        message: t('invitationAccept.errors.notAccepted'),
        fields: {
          password: t('invitationAccept.errors.passwordTooShort', { min: MIN_PASSWORD_LENGTH }),
        },
        final: false,
      });
      return;
    }
    setRefusal(null);
    accept.mutate(
      {
        token,
        ...(form.display_name.trim() ? { display_name: form.display_name.trim() } : {}),
        ...(form.password ? { password: form.password } : {}),
      },
      {
        onSuccess: (accepted) =>
          navigate('/login', {
            replace: true,
            state: { invitationAccepted: accepted.email },
          }),
        onError: (error) => setRefusal(describeRefusal(error, t)),
      },
    );
  };

  const linkUnusable = !token || refusal?.final === true;

  return (
    <section className="mx-auto flex min-h-[60vh] max-w-md flex-col justify-center">
      <div className="rounded-2xl border border-gray-200 bg-white p-8 shadow-sm dark:border-gray-800 dark:bg-gray-900">
        <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-50">
          {t('invitationAccept.title')}
        </h1>
        <p className="mt-2 text-sm text-gray-600 dark:text-gray-400">
          {t('invitationAccept.description')}
        </p>
        {refusal ? (
          <p
            role="alert"
            className="mt-4 rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/40 dark:text-red-300"
          >
            {refusal.message}
          </p>
        ) : null}
        {!token ? (
          <p
            role="alert"
            className="mt-4 rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/40 dark:text-red-300"
          >
            {t('invitationAccept.errors.missingToken')}
          </p>
        ) : null}
        {linkUnusable ? (
          <p className="mt-4 text-sm">
            <Link to="/login" className="text-blue-600 hover:underline dark:text-blue-400">
              {t('invitationAccept.toLogin')}
            </Link>
          </p>
        ) : (
          <form className="mt-6 space-y-4" onSubmit={submit} noValidate>
            <Input
              label={t('invitationAccept.displayName')}
              autoComplete="name"
              maxLength={MAX_DISPLAY_NAME_LENGTH}
              value={form.display_name}
              error={refusal?.fields.display_name}
              onChange={(event) => setForm({ ...form, display_name: event.target.value })}
            />
            <Input
              label={t('invitationAccept.password', { min: MIN_PASSWORD_LENGTH })}
              type="password"
              autoComplete="new-password"
              value={form.password}
              error={refusal?.fields.password}
              onChange={(event) => setForm({ ...form, password: event.target.value })}
            />
            <Button type="submit" className="w-full" loading={accept.isPending}>
              {t('invitationAccept.submit')}
            </Button>
          </form>
        )}
      </div>
    </section>
  );
}
