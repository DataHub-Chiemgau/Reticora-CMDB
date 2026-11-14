/**
 * OrgDataPage is the page /org-data "Organisationsdaten" (TLC-05, TLC-06,
 * UI-14, WP-1157) of the module org_lifecycle; the navigation shows it to
 * holders of org:export or org:delete.
 *
 * "Datenexport" (org:export): starts the full export (POST /org/exports),
 * lists the exports with status, progress and the end of the download
 * period and loads the objects of a completed one with their signed URLs.
 * A session without a second factor sees the hint "Mit zweitem Faktor
 * anmelden" instead of an error (403 mfa-required, E-165).
 *
 * "Organisation löschen" (org:delete): the note to export first (E-166),
 * the slug as confirmation (the button is enabled only when it matches),
 * the MFA hint, the due date and the time left of the grace period and the
 * withdrawal while the state is requested.
 */
import { useQuery } from '@tanstack/react-query';
import { Database, Download, Trash2, Undo2 } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import {
  ApiError,
  fetchAPI,
  isMfaRequired,
  type OrganizationExport,
  type OrganizationExportDownload,
} from '../api/client';
import type { components } from '../api/generated/schema';
import {
  useHasPermission,
  useOrgDeletion,
  useOrgDeletionMutations,
  useOrgExportDownload,
  useOrgExports,
  useStartOrgExport,
} from '../api/hooks';
import { ScrollRegion } from '../components/ScrollRegion';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import {
  tableClasses,
  tdClasses,
  thClasses,
  theadClasses,
  trClasses,
} from '../components/ui/table';

type Organization = components['schemas']['Organization'];

function useFormatDate() {
  const { i18n } = useTranslation();
  return (value: string | null | undefined) => {
    if (!value) return undefined;
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return new Intl.DateTimeFormat(i18n.language, {
      dateStyle: 'medium',
      timeStyle: 'short',
    }).format(date);
  };
}

export function OrgDataPage() {
  const { t } = useTranslation();
  const canExport = useHasPermission('org:export') === true;
  const canDelete = useHasPermission('org:delete') === true;
  return (
    <div className="space-y-6">
      <PageHeader title={t('orgData.title')} description={t('orgData.description')} />
      {!canExport && !canDelete ? (
        <EmptyState icon={<Database />} title={t('orgData.noRights')} />
      ) : null}
      {canExport ? <ExportSection /> : null}
      {canDelete ? <DeletionSection /> : null}
    </div>
  );
}

function MfaHint() {
  const { t } = useTranslation();
  return (
    <p role="status" className="text-sm text-fg" data-testid="mfa-hint">
      {t('orgData.mfaHint')}
    </p>
  );
}

function ExportSection() {
  const { t } = useTranslation();
  const format = useFormatDate();
  const exports = useOrgExports();
  const start = useStartOrgExport();
  const download = useOrgExportDownload();
  const [objects, setObjects] = useState<OrganizationExportDownload>();
  const [failure, setFailure] = useState<string>();
  const [mfa, setMfa] = useState(false);
  const rows = exports.data?.data ?? [];

  const refused = (err: unknown) => {
    if (isMfaRequired(err)) {
      setMfa(true);
      return;
    }
    setFailure(err instanceof ApiError ? err.message : t('orgData.export.failed'));
  };

  return (
    <Card
      title={t('orgData.export.title')}
      actions={
        <Button
          size="sm"
          icon={<Download />}
          loading={start.isPending}
          onClick={() => {
            setFailure(undefined);
            setMfa(false);
            start.mutate(undefined, { onError: refused });
          }}
        >
          {t('orgData.export.start')}
        </Button>
      }
    >
      <p className="mb-3 text-sm text-fg-muted">{t('orgData.export.intro')}</p>
      {mfa ? <MfaHint /> : null}
      {failure ? (
        <p role="alert" className="text-sm text-danger">
          {failure}
        </p>
      ) : null}
      {exports.isError ? (
        <p role="alert" className="text-sm text-danger">
          {t('orgData.export.loadFailed')}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-fg-muted">
          {exports.isPending ? t('app.loading') : t('orgData.export.none')}
        </p>
      ) : (
        <ScrollRegion label={t('orgData.export.title')}>
          <table className={tableClasses}>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('orgData.export.columns.created')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('orgData.export.columns.status')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('orgData.export.columns.progress')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('orgData.export.columns.expires')}
                </th>
                <th scope="col" className={thClasses}>
                  <span className="sr-only">{t('common.actions')}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row: OrganizationExport) => (
                <tr key={row.id} className={trClasses} data-testid={`org-export-${row.id}`}>
                  <td className={tdClasses}>{format(row.created_at)}</td>
                  <td className={tdClasses}>{t(`orgData.exportStatuses.${row.status}`)}</td>
                  <td className={`${tdClasses} tabular-nums`}>{`${row.progress} %`}</td>
                  <td className={tdClasses}>{format(row.expires_at) ?? '—'}</td>
                  <td className={tdClasses}>
                    {row.status === 'completed' ? (
                      <Button
                        size="sm"
                        variant="secondary"
                        loading={download.isPending && download.variables === row.id}
                        onClick={() => {
                          setFailure(undefined);
                          setMfa(false);
                          download.mutate(row.id, { onSuccess: setObjects, onError: refused });
                        }}
                      >
                        {t('orgData.export.objects')}
                      </Button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      )}
      {objects ? (
        <section
          aria-label={t('orgData.export.objectsTitle')}
          className="mt-4 space-y-2"
          data-testid="org-export-objects"
        >
          <h4 className="text-sm font-semibold text-fg">
            {t('orgData.export.objectsTitle')}{' '}
            <span className="font-normal text-fg-muted">
              {t('orgData.export.until', { date: format(objects.expires_at) })}
            </span>
          </h4>
          <ul className="divide-y divide-line text-sm">
            {objects.objects.map((o) => (
              <li key={o.name} className="flex flex-wrap items-center justify-between gap-2 py-1">
                <a href={o.url} className="break-all text-accent hover:underline" rel="noreferrer">
                  {o.name}
                </a>
                <span className="text-xs text-fg-muted">
                  {t('orgData.export.objectMeta', {
                    bytes: o.bytes,
                    date: format(o.url_expires_at),
                  })}
                </span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </Card>
  );
}

/** remainingSplit splits the rest of the grace period into days and hours. */
function remainingSplit(seconds: number | null | undefined) {
  if (seconds === null || seconds === undefined) return undefined;
  return { days: Math.floor(seconds / 86_400), hours: Math.floor((seconds % 86_400) / 3_600) };
}

function DeletionSection() {
  const { t } = useTranslation();
  const format = useFormatDate();
  const deletion = useOrgDeletion();
  const org = useQuery({
    queryKey: ['scope', 'org'],
    queryFn: () => fetchAPI<Organization>('/org'),
    retry: false,
    staleTime: 60_000,
  });
  const { request, cancel } = useOrgDeletionMutations();
  const [slug, setSlug] = useState('');
  const [error, setError] = useState<string>();
  const [mfa, setMfa] = useState(false);
  const state = deletion.data?.state ?? 'none';
  const expected = org.data?.slug ?? '';
  const matches = expected !== '' && slug.trim() === expected;
  const remaining = remainingSplit(deletion.data?.remaining_seconds);

  const refused = (err: unknown) => {
    if (isMfaRequired(err)) {
      setMfa(true);
      return;
    }
    setError(err instanceof ApiError ? err.detail || err.message : t('orgData.delete.failed'));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!matches) return;
    setError(undefined);
    setMfa(false);
    request.mutate(slug.trim(), { onSuccess: () => setSlug(''), onError: refused });
  };

  return (
    <Card title={t('orgData.delete.title')}>
      <div className="space-y-4">
        <p className="text-sm text-fg" data-testid="deletion-state">
          {t('orgData.delete.state', { state: t(`orgData.states.${state}`) })}
        </p>
        {mfa ? <MfaHint /> : null}
        {error ? (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        ) : null}
        {state === 'requested' || state === 'purging' ? (
          <div className="space-y-2 rounded-lg border border-line p-3">
            <p className="text-sm text-fg" data-testid="deletion-due">
              {t('orgData.delete.due', { date: format(deletion.data?.due_at) })}
            </p>
            <p className="text-sm text-fg-muted">
              {remaining ? t('orgData.delete.remaining', remaining) : null}
            </p>
            {state === 'requested' ? (
              <Button
                variant="secondary"
                icon={<Undo2 />}
                loading={cancel.isPending}
                onClick={() => {
                  setError(undefined);
                  setMfa(false);
                  cancel.mutate(undefined, { onError: refused });
                }}
              >
                {t('orgData.delete.cancel')}
              </Button>
            ) : null}
          </div>
        ) : state === 'none' ? (
          <form onSubmit={submit} noValidate className="space-y-3">
            <p className="text-sm text-fg">{t('orgData.delete.exportFirst')}</p>
            <p className="text-sm text-fg-muted">{t('orgData.delete.grace')}</p>
            <p className="text-sm text-fg-muted">{t('orgData.delete.mfaNote')}</p>
            <Input
              label={t('orgData.delete.confirmLabel', { slug: expected })}
              value={slug}
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => setSlug(event.target.value)}
            />
            <Button
              type="submit"
              variant="danger"
              icon={<Trash2 />}
              disabled={!matches}
              loading={request.isPending}
            >
              {t('orgData.delete.request')}
            </Button>
          </form>
        ) : null}
      </div>
    </Card>
  );
}
