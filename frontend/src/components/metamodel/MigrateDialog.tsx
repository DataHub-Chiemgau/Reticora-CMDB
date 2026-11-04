/**
 * MigrateDialog moves the CIs of a template version to another version
 * (UI-11, ABN-01 (c), WP-800) as the job template.migrate of WP-712: it
 * chooses the source version (published or retired), the target version
 * (only published) and the CIs — every visible CI with the template on the
 * source version, the filter AST `filter_spec.where` of a saved CI view
 * (sent as `where`; views without where cannot be chosen) or up to 500 CI
 * ids — and sends POST /api/v1/templates/{id}/migrations. The dry run is
 * the default; its report comes first, then the start with dry_run false.
 * Progress over useJob, the report (TemplateMigrationReport) over
 * useJobResult: totals, per CI outcome, defaults, the fields an override
 * keeps, findings and errors, and a note when it is truncated. The job
 * center (/jobs) has no deep link to one job, the dialog links the list.
 */
import { useMemo, useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  MIGRATION_MAX_IDS,
  localizedText,
  type LocalizedText,
  type TemplateMigrationReport,
  type TemplateMigrationRequest,
} from '../../api/cmdb';
import { useSavedViews, useStartTemplateMigration, useTemplateVersions } from '../../api/cmdbHooks';
import { ApiError } from '../../api/client';
import type { components } from '../../api/generated/schema';
import { isActive, useJob, useJobResult } from '../../hooks/useJobs';
import { Button } from '../ui/Button';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';

type FilterExpr = components['schemas']['FilterExpr'];

/** The ways to choose the CIs; labels templates.migrate.modes.*. */
export const MIGRATION_MODES = ['all', 'view', 'ids'] as const;
type Mode = (typeof MIGRATION_MODES)[number];

/** The outcomes of a CI in the report; labels templates.migrate.outcomes.*. */
export const MIGRATION_OUTCOMES = ['migrated', 'skipped', 'failed'] as const;

/** The error codes of a CI in the report; labels templates.migrate.errorCodes.*. */
export const MIGRATION_ERROR_CODES = ['unique', 'validation', 'not_found', 'internal'] as const;

const TOTALS = ['selected', 'migrated', 'skipped', 'failed', 'with_findings'] as const;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface MigrateDialogTemplate {
  id: string;
  key: string;
  display_name?: LocalizedText;
}

export interface MigrateDialogProps {
  template: MigrateDialogTemplate;
  open: boolean;
  onClose: () => void;
  /** A fixed selection of CIs (the CI page): no other way is offered. */
  ciIds?: string[];
  fromVersion?: number;
  toVersion?: number;
}

export function MigrateDialog({ template, open, onClose, ...rest }: MigrateDialogProps) {
  const { t, i18n } = useTranslation();
  const name = localizedText(template.display_name, i18n.language) || template.key;
  return (
    <Modal
      open={open}
      onOpenChange={(next) => (next ? undefined : onClose())}
      title={t('templates.migrate.title', { name })}
      description={t('templates.migrate.description')}
    >
      {open ? <MigrateForm template={template} onClose={onClose} {...rest} /> : null}
    </Modal>
  );
}

/** The CI ids of a text, one per line or separated by commas or spaces. */
function parseIds(text: string): string[] {
  return [...new Set(text.split(/[\s,;]+/).filter(Boolean))];
}

function MigrateForm({
  template,
  onClose,
  ciIds,
  fromVersion,
  toVersion,
}: Omit<MigrateDialogProps, 'open'>) {
  const { t } = useTranslation();
  const versions = useTemplateVersions(template.id);
  const views = useSavedViews();
  const start = useStartTemplateMigration();

  const list = useMemo(
    () => [...(versions.data?.data ?? [])].sort((a, b) => b.version - a.version),
    [versions.data],
  );
  const sources = list.filter((v) => v.status === 'published' || v.status === 'retired');
  const targets = list.filter((v) => v.status === 'published');
  const ciViews = (views.data?.data ?? []).filter((v) => v.entity_kind === 'ci');

  const [from, setFrom] = useState<number | undefined>(fromVersion);
  const [to, setTo] = useState<number | undefined>(toVersion);
  const [mode, setMode] = useState<Mode>(ciIds ? 'ids' : 'all');
  const [viewId, setViewId] = useState('');
  const [idsText, setIdsText] = useState('');
  const [dryRun, setDryRun] = useState(true);
  const [localError, setLocalError] = useState<string | null>(null);
  const [run, setRun] = useState<{ jobId: string; request: TemplateMigrationRequest } | null>(null);

  const target = to ?? targets[0]?.version;
  const source = from ?? sources.find((v) => v.version !== target)?.version;

  const build = (): TemplateMigrationRequest | string => {
    if (source === undefined || target === undefined) return t('templates.migrate.chooseVersions');
    if (source === target) return t('templates.migrate.sameVersion');
    const request: TemplateMigrationRequest = {
      from_version: source,
      to_version: target,
      dry_run: true,
    };
    if (ciIds) {
      request.ci_ids = ciIds;
    } else if (mode === 'ids') {
      const ids = parseIds(idsText);
      if (ids.length === 0) return t('templates.migrate.idsEmpty');
      if (ids.length > MIGRATION_MAX_IDS) {
        return t('templates.migrate.idsTooMany', { max: MIGRATION_MAX_IDS });
      }
      const wrong = ids.find((id) => !UUID.test(id));
      if (wrong) return t('templates.migrate.idInvalid', { id: wrong });
      request.ci_ids = ids;
    } else if (mode === 'view') {
      const where = whereOf(ciViews.find((v) => v.id === viewId)?.filter_spec);
      if (!where) return t('templates.migrate.viewMissing');
      request.where = where;
    }
    return request;
  };

  const send = (request: TemplateMigrationRequest) => {
    setLocalError(null);
    start.mutate(
      { templateId: template.id, request },
      { onSuccess: (started) => setRun({ jobId: started.job_id, request }) },
    );
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const request = build();
    if (typeof request === 'string') {
      setLocalError(request);
      return;
    }
    send({ ...request, dry_run: dryRun });
  };

  const versionLabel = (n: number, status: string) =>
    t('templates.versionOption', { version: n, status: t(`templates.versionStatuses.${status}`) });

  return (
    <div className="space-y-4">
      <form onSubmit={submit} className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <Select
            label={t('templates.migrate.from')}
            value={source !== undefined ? String(source) : ''}
            onChange={(e) => setFrom(Number(e.target.value))}
            options={[
              ...(source === undefined ? [{ value: '', label: t('templates.migrate.none') }] : []),
              ...sources.map((v) => ({
                value: String(v.version),
                label: versionLabel(v.version, v.status),
              })),
            ]}
          />
          <Select
            label={t('templates.migrate.to')}
            value={target !== undefined ? String(target) : ''}
            onChange={(e) => setTo(Number(e.target.value))}
            options={[
              ...(target === undefined ? [{ value: '', label: t('templates.migrate.none') }] : []),
              ...targets.map((v) => ({
                value: String(v.version),
                label: versionLabel(v.version, v.status),
              })),
            ]}
          />
        </div>

        {ciIds ? (
          <p className="text-sm text-gray-700 dark:text-gray-300">
            {t('templates.migrate.fixedSelection', { count: ciIds.length })}
          </p>
        ) : (
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium text-gray-700 dark:text-gray-200">
              {t('templates.migrate.selection')}
            </legend>
            {MIGRATION_MODES.map((value) => (
              <label key={value} className="flex items-center gap-2 text-sm">
                <input
                  type="radio"
                  name="migrate-mode"
                  value={value}
                  checked={mode === value}
                  onChange={() => setMode(value)}
                />
                {t(`templates.migrate.modes.${value}`)}
              </label>
            ))}
            {mode === 'view' ? (
              <fieldset className="ml-6 space-y-1">
                <legend className="text-xs text-muted">{t('templates.migrate.viewLegend')}</legend>
                {ciViews.length === 0 ? (
                  <p className="text-xs text-muted">{t('templates.migrate.noViews')}</p>
                ) : null}
                {ciViews.map((view) => {
                  const usable = !!whereOf(view.filter_spec);
                  return (
                    <label key={view.id} className="flex items-center gap-2 text-sm">
                      <input
                        type="radio"
                        name="migrate-view"
                        value={view.id}
                        disabled={!usable}
                        checked={viewId === view.id}
                        onChange={() => setViewId(view.id)}
                      />
                      {view.name}
                      {!usable ? (
                        <span className="text-xs text-muted">
                          {t('templates.migrate.viewNoWhere')}
                        </span>
                      ) : null}
                    </label>
                  );
                })}
              </fieldset>
            ) : null}
            {mode === 'ids' ? (
              <div className="ml-6 space-y-1">
                <label htmlFor="migrate-ids" className="block text-sm">
                  {t('templates.migrate.idsLabel', { max: MIGRATION_MAX_IDS })}
                </label>
                <textarea
                  id="migrate-ids"
                  rows={3}
                  value={idsText}
                  onChange={(e) => setIdsText(e.target.value)}
                  className="w-full rounded-lg border border-gray-300 bg-white px-3 py-2 font-mono text-xs dark:border-gray-700 dark:bg-gray-900"
                />
              </div>
            ) : null}
          </fieldset>
        )}

        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} />
          {t('templates.migrate.dryRun')}
        </label>

        {localError ? (
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">
            {localError}
          </p>
        ) : null}
        {start.error ? (
          <ProblemNotice error={start.error} prefix={t('templates.migrate.refused')} />
        ) : null}

        <div className="flex flex-wrap justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t('templates.migrate.close')}
          </Button>
          <Button type="submit" disabled={start.isPending || versions.isLoading}>
            {dryRun ? t('templates.migrate.startDryRun') : t('templates.migrate.start')}
          </Button>
        </div>
      </form>

      {run ? (
        <MigrationRun
          key={run.jobId}
          jobId={run.jobId}
          dryRun={run.request.dry_run === true}
          starting={start.isPending}
          onStart={() => send({ ...run.request, dry_run: false })}
        />
      ) : null}
    </div>
  );
}

/** whereOf is the filter AST of a saved view, undefined without one. */
function whereOf(spec: Record<string, unknown> | undefined): FilterExpr | undefined {
  const where = spec?.where;
  return where && typeof where === 'object' ? (where as FilterExpr) : undefined;
}

function MigrationRun({
  jobId,
  dryRun,
  starting,
  onStart,
}: {
  jobId: string;
  dryRun: boolean;
  starting: boolean;
  onStart: () => void;
}) {
  const { t } = useTranslation();
  const job = useJob(jobId);
  const done = job.data?.status === 'succeeded';
  const result = useJobResult(done ? jobId : null);
  const report = result.data as TemplateMigrationReport | undefined;
  const kind = t('jobs.kinds.template.migrate');

  return (
    <section aria-label={t('templates.migrate.runLabel')} className="space-y-3">
      <div className="flex flex-wrap items-center gap-3 text-sm">
        <span>
          {dryRun ? t('templates.migrate.dryRunJob') : t('templates.migrate.realJob')}
          {job.data ? `: ${t(`jobs.statuses.${job.data.status}`)}` : null}
        </span>
        {job.data && isActive(job.data) ? (
          <div
            role="progressbar"
            aria-label={t('jobs.progressOf', { kind })}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={job.data.progress}
            className="h-2 w-32 overflow-hidden rounded bg-gray-200 dark:bg-gray-700"
          >
            <div className="h-full bg-primary" style={{ width: `${job.data.progress}%` }} />
          </div>
        ) : null}
        <Link to="/jobs" className="text-primary hover:underline">
          {t('templates.migrate.jobsLink')}
        </Link>
      </div>
      {job.data?.status === 'failed' ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {t('templates.migrate.jobFailed', { error: job.data.error ?? '' })}
        </p>
      ) : null}
      {job.data?.status === 'cancelled' ? (
        <p className="text-sm text-muted">{t('templates.migrate.jobCancelled')}</p>
      ) : null}
      {result.error ? (
        <ProblemNotice error={result.error} prefix={t('templates.migrate.reportFailed')} />
      ) : null}
      {report ? <MigrationReport report={report} /> : null}
      {report && dryRun ? (
        <div className="flex justify-end">
          <Button disabled={starting} onClick={onStart}>
            {t('templates.migrate.start')}
          </Button>
        </div>
      ) : null}
    </section>
  );
}

/** MigrationReport shows a TemplateMigrationReport: totals and the CIs. */
export function MigrationReport({ report }: { report: TemplateMigrationReport }) {
  const { t } = useTranslation();
  const keys = (list: string[]) => (list.length > 0 ? list.join(', ') : '—');
  return (
    <div className="space-y-2">
      <h4 className="text-sm font-semibold">
        {report.dry_run ? t('templates.migrate.reportDryRun') : t('templates.migrate.report')}
      </h4>
      <dl
        aria-label={t('templates.migrate.totals')}
        className="grid grid-cols-2 gap-2 text-sm sm:grid-cols-5"
      >
        {TOTALS.map((key) => (
          <div key={key}>
            <dt className="text-xs text-muted">{t(`templates.migrate.totalsFields.${key}`)}</dt>
            <dd className="font-medium" data-total={key}>
              {report.totals[key]}
            </dd>
          </div>
        ))}
      </dl>
      {report.items.length > 0 ? (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs">
            <caption className="sr-only">{t('templates.migrate.itemsCaption')}</caption>
            <thead>
              <tr>
                <th scope="col" className="pb-1 pr-2">
                  {t('templates.migrate.columns.ci')}
                </th>
                <th scope="col" className="pb-1 pr-2">
                  {t('templates.migrate.columns.outcome')}
                </th>
                <th scope="col" className="pb-1 pr-2">
                  {t('templates.migrate.columns.defaults')}
                </th>
                <th scope="col" className="pb-1 pr-2">
                  {t('templates.migrate.columns.overridden')}
                </th>
                <th scope="col" className="pb-1 pr-2">
                  {t('templates.migrate.columns.findings')}
                </th>
                <th scope="col" className="pb-1">
                  {t('templates.migrate.columns.error')}
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {report.items.map((item) => (
                <tr key={item.ci_id} data-ci-id={item.ci_id} className="align-top">
                  <td className="py-1 pr-2 font-mono">
                    <Link to={`/cmdb/${item.ci_id}`} className="text-primary hover:underline">
                      {item.ci_id}
                    </Link>
                  </td>
                  <td className="py-1 pr-2">{t(`templates.migrate.outcomes.${item.outcome}`)}</td>
                  <td className="py-1 pr-2">{keys(item.defaults)}</td>
                  <td className="py-1 pr-2">{keys(item.overridden)}</td>
                  <td className="py-1 pr-2">
                    {item.findings.length > 0 ? (
                      <ul>
                        {item.findings.map((f, i) => (
                          <li key={`${f.field}-${i}`}>
                            {f.field}: {f.detail}
                          </li>
                        ))}
                      </ul>
                    ) : (
                      '—'
                    )}
                  </td>
                  <td className="py-1">
                    {item.error
                      ? t('templates.migrate.errorText', {
                          code: t(`templates.migrate.errorCodes.${item.error.code}`),
                          field: item.error.field ?? '—',
                          detail: item.error.detail,
                        })
                      : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="text-sm text-muted">{t('templates.migrate.noItems')}</p>
      )}
      {report.truncated ? (
        <p className="text-sm text-amber-700 dark:text-amber-300">
          {t('templates.migrate.truncated')}
        </p>
      ) : null}
    </div>
  );
}

/**
 * ProblemNotice shows a refused request as its problem detail (RFC 7807):
 * status, detail, the violations per field and the trace id (API-03).
 */
export function ProblemNotice({
  error,
  prefix,
  children,
}: {
  error: unknown;
  prefix: string;
  children?: React.ReactNode;
}) {
  const { t } = useTranslation();
  const api = error instanceof ApiError ? error : undefined;
  const detail = api ? api.detail || api.title || api.message : String(error);
  return (
    <div
      role="alert"
      className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-800 dark:border-red-800 dark:bg-red-950 dark:text-red-200"
    >
      <p className="font-medium">{prefix}</p>
      <p>{api ? t('metamodelDrafts.problem.detail', { status: api.status, detail }) : detail}</p>
      {api && api.violations.length > 0 ? (
        <ul className="mt-1 list-disc pl-5">
          {api.violations.map((v, i) => (
            <li key={`${v.field}-${i}`}>
              {v.field}: {v.detail}
            </li>
          ))}
        </ul>
      ) : null}
      {children}
      {api?.traceId ? (
        <p className="font-mono text-xs">
          {t('metamodelDrafts.problem.traceId', { traceId: api.traceId })}
        </p>
      ) : null}
    </div>
  );
}
