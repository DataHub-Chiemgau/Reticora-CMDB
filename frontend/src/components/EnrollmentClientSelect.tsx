/**
 * EnrollmentClientSelect — the client choice of an enrollment code (COL-04,
 * TEN-09, F-425). The server binds a code to the client in which its creator
 * holds collector:manage (WP-677): a creator restricted to one client is
 * bound to it, an org-wide one stays unbound, and one restricted to several
 * clients must name one (422 otherwise). GET /me names that scope: the
 * permission scope of collector:manage (pscp) when it differs from the
 * session scope, the session scope (cls) otherwise. The choice is offered
 * exactly for several clients; the names come from GET /clients, a client
 * whose name the user may not read is shown by its id. The server still
 * decides every request.
 */
import { useQuery } from '@tanstack/react-query';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { ApiError, sessionApi, type SessionClaims } from '../api/client';
import { clientApi } from '../api/cmdb';
import { Select } from './ui/Select';

/**
 * enrollmentClientIds are the clients a code's creator must choose from: the
 * clients of its collector:manage scope when there are several, otherwise
 * null (one client or the whole organization: the server binds without a
 * choice).
 */
export function enrollmentClientIds(claims: SessionClaims | undefined): string[] | null {
  if (!claims) return null;
  const permission = claims.pscp?.['collector:manage'];
  const clients = permission ? permission.cls : claims.cls;
  return Array.isArray(clients) && clients.length > 1 ? clients : null;
}

export interface EnrollmentClientChoice {
  /**
   * required: the user must choose a client before a code is created. While
   * the session loads it is false; a code requested meanwhile is decided by
   * the server (422 at the choice once it shows).
   */
  required: boolean;
  options: { value: string; label: string }[];
}

/** useEnrollmentClientChoice reads the choice while `enabled` (e.g. the dialog is open). */
export function useEnrollmentClientChoice(enabled: boolean): EnrollmentClientChoice {
  const session = useQuery({
    queryKey: ['me'],
    queryFn: () => sessionApi.me(),
    enabled,
    retry: false,
    staleTime: 60_000,
  });
  const ids = enrollmentClientIds(session.data);
  // The same query as the scope banner, so the names come from its cache.
  const clients = useQuery({
    queryKey: ['scope', 'clients'],
    queryFn: () => clientApi.list(),
    enabled: enabled && ids !== null,
    retry: false,
    staleTime: 60_000,
  });
  return {
    required: ids !== null,
    options: (ids ?? []).map((id) => ({
      value: id,
      label: clients.data?.data?.find((c) => c.id === id)?.name ?? id,
    })),
  };
}

/** A refused enrollment code as the form shows it: at the client field or as a message. */
export interface EnrollmentRefusal {
  client?: string;
  message?: string;
}

/**
 * describeEnrollmentRefusal names why no code was created: the server's
 * problem detail (a FieldProblem of client_id at the client choice when it
 * is shown) instead of a generic error.
 */
export function describeEnrollmentRefusal(
  error: unknown,
  t: TFunction,
  choiceShown: boolean,
): EnrollmentRefusal {
  if (!error) return {};
  if (error instanceof ApiError) {
    const detail = error.detail || error.message;
    if (choiceShown && error.field === 'client_id') {
      // A FieldProblem's detail is "<field>: <message>".
      const prefix = 'client_id: ';
      return { client: detail.startsWith(prefix) ? detail.slice(prefix.length) : detail };
    }
    return { message: t('discovery.enroll.failed', { detail }) };
  }
  return {
    message: t('discovery.enroll.failed', {
      detail: error instanceof Error ? error.message : String(error),
    }),
  };
}

/** EnrollmentClientSelect is the required client choice; it renders nothing without one. */
export function EnrollmentClientSelect({
  choice,
  value,
  onChange,
  error,
}: {
  choice: EnrollmentClientChoice;
  value: string;
  onChange: (clientId: string) => void;
  error?: string;
}) {
  const { t } = useTranslation();
  if (!choice.required) return null;
  return (
    <div className="space-y-1">
      <Select
        label={t('discovery.enroll.client')}
        required
        value={value}
        onChange={(e) => onChange(e.target.value)}
        options={[{ value: '', label: t('discovery.enroll.clientChoose') }, ...choice.options]}
        error={error}
      />
      <p className="text-xs text-gray-500 dark:text-gray-400">{t('discovery.enroll.clientHint')}</p>
    </div>
  );
}
